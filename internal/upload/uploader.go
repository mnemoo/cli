package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mnemoo/cli/internal/api"
)

const (
	maxConcurrentFiles      = 32
	maxConcurrentTransfers  = 16
	maxOutstandingTransfers = 32
	maxPUTAttempts          = 8
	maxUploadReinitializes  = 3
	putRetryBaseDelay       = 500 * time.Millisecond
	putRetryMaxDelay        = 15 * time.Second
	uploadReinitDelay       = time.Second
)

type FileAction string

const (
	ActionUpload    FileAction = "upload"
	ActionCopy      FileAction = "copy"
	ActionDelete    FileAction = "delete"
	ActionUnchanged FileAction = "unchanged"
)

type PlanEntry struct {
	Action     FileAction
	LocalFile  *LocalFile
	RemoteKey  string
	SourceKey  string
	RemoteETag string
	Size       int64
}

type UploadPlan struct {
	Team       string
	Game       string
	UploadType string
	ToUpload   []PlanEntry
	ToCopy     []PlanEntry
	ToDelete   []PlanEntry
	Unchanged  []PlanEntry
}

func (p *UploadPlan) TotalActions() int {
	return len(p.ToUpload) + len(p.ToCopy) + len(p.ToDelete)
}

func (p *UploadPlan) TotalUploadBytes() int64 {
	var total int64
	for _, e := range p.ToUpload {
		total += e.Size
	}
	return total
}

type ProgressEvent struct {
	Phase    string
	Current  int
	Total    int
	FileName string
	FileSize int64
	Error    error
}

// PauseGate blocks goroutines when paused and unblocks on resume.
type PauseGate struct {
	mu     sync.Mutex
	ch     chan struct{}
	paused bool
}

func NewPauseGate() *PauseGate {
	ch := make(chan struct{})
	close(ch)
	return &PauseGate{ch: ch}
}

// Wait blocks until unpaused or context is cancelled.
func (g *PauseGate) Wait(ctx context.Context) error {
	g.mu.Lock()
	ch := g.ch
	g.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Toggle switches between paused and resumed. Returns true if now paused.
func (g *PauseGate) Toggle() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.paused {
		close(g.ch)
		g.paused = false
	} else {
		g.ch = make(chan struct{})
		g.paused = true
	}
	return g.paused
}

func (g *PauseGate) IsPaused() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.paused
}

type Uploader struct {
	Client      Client
	ByteCounter *atomic.Int64
	Tracker     *TransferTracker
	transfers   *transferLimiter
	retryWait   func(context.Context, time.Duration) error
}

// Client is the infrastructure boundary required by the upload engine. The
// concrete Stake Engine HTTP client is one adapter; tests and other frontends
// can provide deterministic implementations without network access.
type Client interface {
	ListScratch(context.Context, string, string, string) ([]api.ScratchObject, error)
	InitUpload(context.Context, api.FileUploadBody) (*api.UploadResponse, error)
	InitMultiUpload(context.Context, api.FileMultiUploadBody) (*api.MultiUploadInitResponse, error)
	CompleteMultiUpload(context.Context, api.FileCompleteBody) (*api.CompleteResponse, error)
	CopyFile(context.Context, string, string, string, string) (*api.CopyResponse, error)
	MultiCopy(context.Context, api.FileMulticopyBody) (*api.MultiUploadInitResponse, error)
	DeleteFiles(context.Context, string, string, []string) (*api.DeleteResponse, error)
	UploadToS3(context.Context, string, io.Reader, int64, string, func(int64), func()) error
}

func NewUploader(client Client) *Uploader {
	return &Uploader{
		Client: client, ByteCounter: new(atomic.Int64),
		transfers: newTransferLimiter(maxConcurrentTransfers, maxOutstandingTransfers),
		retryWait: waitForRetry,
	}
}

func (u *Uploader) Plan(ctx context.Context, team, game, uploadType, localDir string) (*UploadPlan, error) {
	localFiles, err := ScanDirectory(localDir, uploadType)
	if err != nil {
		return nil, fmt.Errorf("scanning directory: %w", err)
	}

	remoteObjects, err := u.Client.ListScratch(ctx, team, game, uploadType)
	if err != nil {
		return nil, fmt.Errorf("listing remote scratch: %w", err)
	}

	remoteByKey := make(map[string]api.ScratchObject, len(remoteObjects))
	remoteByETag := make(map[string]api.ScratchObject, len(remoteObjects))
	for _, obj := range remoteObjects {
		key := "/" + obj.Key
		remoteByKey[key] = obj
		remoteByETag[obj.ETag] = obj
	}

	plan := &UploadPlan{
		Team:       team,
		Game:       game,
		UploadType: uploadType,
	}

	localKeys := make(map[string]bool, len(localFiles))

	for i := range localFiles {
		lf := &localFiles[i]
		localKeys[lf.RelPath] = true
		etag := lf.ETagResult.ETag

		if remote, exists := remoteByKey[lf.RelPath]; exists && remote.ETag == etag {
			plan.Unchanged = append(plan.Unchanged, PlanEntry{
				Action:    ActionUnchanged,
				LocalFile: lf,
				RemoteKey: lf.RelPath,
				Size:      lf.Size,
			})
			continue
		}

		if source, found := remoteByETag[etag]; found {
			plan.ToCopy = append(plan.ToCopy, PlanEntry{
				Action:     ActionCopy,
				LocalFile:  lf,
				RemoteKey:  lf.RelPath,
				SourceKey:  "/" + source.Key,
				RemoteETag: source.ETag,
				Size:       lf.Size,
			})
			continue
		}

		plan.ToUpload = append(plan.ToUpload, PlanEntry{
			Action:    ActionUpload,
			LocalFile: lf,
			RemoteKey: lf.RelPath,
			Size:      lf.Size,
		})
	}

	for _, obj := range remoteObjects {
		key := "/" + obj.Key
		if !localKeys[key] {
			plan.ToDelete = append(plan.ToDelete, PlanEntry{
				Action:    ActionDelete,
				RemoteKey: key,
				Size:      obj.Size,
			})
		}
	}

	return plan, nil
}

// Execute runs the upload plan. Pass gate for pause/resume support (nil to disable).
func (u *Uploader) Execute(ctx context.Context, plan *UploadPlan, gate *PauseGate, progressFn func(ProgressEvent)) error {
	if u.Tracker == nil {
		u.Tracker = NewTransferTracker(plan.ToUpload)
	}
	total := plan.TotalActions()
	var done atomic.Int32

	report := func(phase, fileName string, fileSize int64, err error) {
		cur := int(done.Add(1))
		if progressFn != nil {
			progressFn(ProgressEvent{
				Phase:    phase,
				Current:  cur,
				Total:    total,
				FileName: fileName,
				FileSize: fileSize,
				Error:    err,
			})
		}
	}

	if err := u.executeUploads(ctx, plan, gate, &done, total, progressFn); err != nil {
		return err
	}

	for _, entry := range plan.ToCopy {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if gate != nil {
			if err := gate.Wait(ctx); err != nil {
				return err
			}
		}
		err := u.executeCopy(ctx, plan.Team, plan.Game, entry)
		report("copy", entry.RemoteKey, entry.Size, err)
		if err != nil {
			return fmt.Errorf("copying %s: %w", entry.RemoteKey, err)
		}
	}

	if len(plan.ToDelete) > 0 {
		if gate != nil {
			if err := gate.Wait(ctx); err != nil {
				return err
			}
		}
		paths := make([]string, len(plan.ToDelete))
		for i, e := range plan.ToDelete {
			paths[i] = e.RemoteKey
		}
		_, err := u.Client.DeleteFiles(ctx, plan.Team, plan.Game, paths)
		for _, e := range plan.ToDelete {
			report("delete", e.RemoteKey, 0, err)
		}
		if err != nil {
			return fmt.Errorf("deleting files: %w", err)
		}
	}

	return nil
}

func (u *Uploader) executeUploads(ctx context.Context, plan *UploadPlan, gate *PauseGate, done *atomic.Int32, total int, progressFn func(ProgressEvent)) error {
	sem := make(chan struct{}, maxConcurrentFiles)
	var mu sync.Mutex
	var firstErr error

	var wg sync.WaitGroup
	for _, entry := range plan.ToUpload {
		if ctx.Err() != nil {
			break
		}
		mu.Lock()
		if firstErr != nil {
			mu.Unlock()
			break
		}
		mu.Unlock()

		if gate != nil {
			if err := gate.Wait(ctx); err != nil {
				return err
			}
		}

		wg.Add(1)
		sem <- struct{}{}

		go func(e PlanEntry) {
			defer wg.Done()
			defer func() { <-sem }()

			if progressFn != nil {
				progressFn(ProgressEvent{
					Phase:    "start",
					FileName: e.RemoteKey,
					FileSize: e.Size,
				})
			}

			u.Tracker.StartFile(e.RemoteKey)
			err := u.uploadSingleFile(ctx, plan.Team, plan.Game, e, gate)
			if err != nil {
				u.Tracker.FailFile(e.RemoteKey, err)
			} else {
				u.Tracker.CompleteFile(e.RemoteKey)
			}

			cur := int(done.Add(1))
			if progressFn != nil {
				progressFn(ProgressEvent{
					Phase:    "upload",
					Current:  cur,
					Total:    total,
					FileName: e.RemoteKey,
					FileSize: e.Size,
					Error:    err,
				})
			}

			if err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = fmt.Errorf("uploading %s: %w", e.RemoteKey, err)
				}
				mu.Unlock()
			}
		}(entry)
	}

	wg.Wait()
	return firstErr
}

func (u *Uploader) uploadSingleFile(ctx context.Context, team, game string, entry PlanEntry, gate *PauseGate) error {
	lf := entry.LocalFile

	file, err := os.Open(lf.AbsPath)
	if err != nil {
		return fmt.Errorf("opening file: %w", err)
	}
	defer func() { _ = file.Close() }()

	ct := guessContentType(lf.AbsPath)

	if lf.Size <= MultipartThreshold {
		return u.doSinglePartUpload(ctx, team, game, lf, file, ct, gate)
	}
	return u.doMultiPartUpload(ctx, team, game, lf, file, ct, gate)
}

func (u *Uploader) doSinglePartUpload(ctx context.Context, team, game string, lf *LocalFile, file *os.File, contentType string, gate *PauseGate) error {
	body := api.FileUploadBody{
		Team: team,
		Game: game,
		Path: lf.RelPath,
		ETag: lf.ETagResult.ETag,
		Size: lf.Size,
	}

	for reinitAttempt := 0; ; reinitAttempt++ {
		resp, err := u.Client.InitUpload(ctx, body)
		if err != nil {
			return err
		}
		err = u.retryPut(ctx, gate, lf.RelPath, 1, lf.Size, resp.URL, contentType, func() io.Reader {
			return io.NewSectionReader(file, 0, lf.Size)
		})
		if err == nil {
			return nil
		}
		if !canReinitializeUpload(err) {
			return err
		}
		if reinitAttempt >= maxUploadReinitializes {
			return fmt.Errorf("single-part upload recovery exhausted after %d sessions: %w", reinitAttempt+1, err)
		}
		u.Tracker.StartFile(lf.RelPath)
		if err := u.waitBeforeRetry(ctx, uploadReinitDelay); err != nil {
			return err
		}
	}
}

func (u *Uploader) doMultiPartUpload(ctx context.Context, team, game string, lf *LocalFile, file *os.File, contentType string, gate *PauseGate) error {
	parts := make([]api.FilePart, len(lf.ETagResult.Parts))
	for i, p := range lf.ETagResult.Parts {
		parts[i] = api.FilePart{
			ETag:   p.ETag,
			Number: p.Number,
			Size:   p.Size,
		}
	}

	filename := filepath.Base(lf.AbsPath)
	initBody := api.FileMultiUploadBody{
		Filename: filename,
		Team:     team,
		Game:     game,
		Path:     lf.RelPath,
		Size:     lf.Size,
		Parts:    parts,
	}

	initResp, err := u.Client.InitMultiUpload(ctx, initBody)
	if err != nil {
		return err
	}

	pendingParts := initResp.Parts
	for reinitAttempt := 0; ; reinitAttempt++ {
		err := u.uploadMultipartParts(ctx, gate, lf, file, contentType, pendingParts)
		if err == nil {
			break
		}
		if !canReinitializeUpload(err) {
			return err
		}
		if reinitAttempt >= maxUploadReinitializes {
			return fmt.Errorf("multipart upload recovery exhausted after %d sessions: %w", reinitAttempt+1, err)
		}

		// Reconcile the multipart session and obtain fresh URLs. The API marks
		// already accepted parts complete, so only unfinished parts are retried.
		u.Tracker.StartFile(lf.RelPath)
		if err := u.waitBeforeRetry(ctx, uploadReinitDelay); err != nil {
			return err
		}
		initResp, err = u.Client.InitMultiUpload(ctx, initBody)
		if err != nil {
			return fmt.Errorf("re-initializing multipart upload (attempt %d): %w", reinitAttempt+1, err)
		}
		pendingParts = initResp.Parts
	}
	u.Tracker.FinalizeFile(lf.RelPath)

	completeParts := make([]api.FileCompletePartBody, len(lf.ETagResult.Parts))
	for i, p := range lf.ETagResult.Parts {
		completeParts[i] = api.FileCompletePartBody{
			ETag:   p.ETag,
			Size:   p.Size,
			Number: p.Number,
		}
	}

	completeBody := api.FileCompleteBody{
		Team:     team,
		Game:     game,
		UploadID: initResp.UploadID,
		Key:      initResp.Key,
		Parts:    completeParts,
	}

	_, err = u.Client.CompleteMultiUpload(ctx, completeBody)
	return err
}

func (u *Uploader) uploadMultipartParts(ctx context.Context, gate *PauseGate, lf *LocalFile, file *os.File, contentType string, parts []api.MultiUploadPartResponse) error {
	partByNumber := make(map[int]PartHash, len(lf.ETagResult.Parts))
	for _, part := range lf.ETagResult.Parts {
		partByNumber[part.Number] = part
	}

	partCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, 1)
	var wg sync.WaitGroup

	for _, remotePart := range parts {
		localPart, ok := partByNumber[remotePart.Number]
		if !ok {
			return fmt.Errorf("multipart response contains unknown part %d", remotePart.Number)
		}
		if remotePart.Complete {
			u.Tracker.CompletePart(lf.RelPath, remotePart.Number)
			continue
		}

		offset := int64(localPart.Number-1) * PartSize
		if offset < 0 || localPart.Size <= 0 || offset+localPart.Size > lf.Size {
			return fmt.Errorf("invalid bounds for part %d: offset=%d size=%d file=%d", localPart.Number, offset, localPart.Size, lf.Size)
		}
		part := remotePart
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := u.retryPut(partCtx, gate, lf.RelPath, part.Number, localPart.Size, part.URL, contentType, func() io.Reader {
				return io.NewSectionReader(file, offset, localPart.Size)
			})
			if err == nil {
				return
			}
			wrapped := fmt.Errorf("uploading part %d (%s): %w", part.Number, FormatSize(localPart.Size), err)
			select {
			case errCh <- wrapped:
				cancel()
			default:
			}
		}()
	}

	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

func isExpiredTokenErr(err error) bool {
	s := err.Error()
	return strings.Contains(s, "ExpiredToken") || strings.Contains(s, "token has expired") ||
		strings.Contains(s, "Request has expired")
}

func canReinitializeUpload(err error) bool {
	return isExpiredTokenErr(err) || isRetryablePUT(err)
}

func (u *Uploader) executeCopy(ctx context.Context, team, game string, entry PlanEntry) error {
	lf := entry.LocalFile

	if lf.Size <= MultipartThreshold {
		_, err := u.Client.CopyFile(ctx, team, game, entry.SourceKey, lf.RelPath)
		return err
	}

	parts := make([]api.FilePart, len(lf.ETagResult.Parts))
	for i, p := range lf.ETagResult.Parts {
		parts[i] = api.FilePart{
			ETag:   p.ETag,
			Number: p.Number,
			Size:   p.Size,
		}
	}

	body := api.FileMulticopyBody{
		Team:     team,
		Game:     game,
		PathTo:   lf.RelPath,
		PathFrom: entry.SourceKey,
		ETag:     lf.ETagResult.ETag,
		Size:     lf.Size,
		Parts:    parts,
	}

	_, err := u.Client.MultiCopy(ctx, body)
	return err
}

func (u *Uploader) retryPut(ctx context.Context, gate *PauseGate, fileName string, partNumber int, size int64, url, contentType string, source func() io.Reader) error {
	for attempt := 1; attempt <= maxPUTAttempts; attempt++ {
		if gate != nil {
			if err := gate.Wait(ctx); err != nil {
				return err
			}
		}
		permit, err := u.transfers.acquire(ctx, fileName)
		if err != nil {
			return err
		}
		u.Tracker.BeginPart(fileName, partNumber)
		err = func() error {
			defer permit.release()
			return u.Client.UploadToS3(ctx, url, source(), size, contentType, func(delta int64) {
				if u.ByteCounter != nil {
					u.ByteCounter.Add(delta)
				}
				u.Tracker.AddBytes(fileName, partNumber, delta)
			}, permit.markBodySent)
		}()

		if err == nil {
			u.Tracker.CompletePart(fileName, partNumber)
			return nil
		}
		if ctx.Err() != nil {
			u.Tracker.PartError(fileName, partNumber, ctx.Err(), false)
			return ctx.Err()
		}

		retrying := attempt < maxPUTAttempts && isRetryablePUT(err) && !isExpiredTokenErr(err)
		u.Tracker.PartError(fileName, partNumber, err, retrying)
		if !retrying {
			if attempt >= maxPUTAttempts && isRetryablePUT(err) {
				return fmt.Errorf("PUT failed after %d attempts: %w", attempt, err)
			}
			return err
		}

		delay := putRetryDelay(fileName, partNumber, attempt)
		if err := u.waitBeforeRetry(ctx, delay); err != nil {
			return err
		}
	}
	return errors.New("upload retries exhausted")
}

func (u *Uploader) waitBeforeRetry(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	if u.retryWait != nil {
		return u.retryWait(ctx, delay)
	}
	return waitForRetry(ctx, delay)
}

func waitForRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// putRetryDelay retries the first isolated transport failure immediately, then
// backs off to avoid a retry storm across parallel multipart PUTs. The stable
// per-part spread keeps retries from all 16 transfer lanes synchronized.
func putRetryDelay(fileName string, partNumber, attempt int) time.Duration {
	if attempt <= 1 {
		return 0
	}
	shift := min(attempt-2, 5)
	delay := putRetryBaseDelay << shift
	if delay > putRetryMaxDelay {
		delay = putRetryMaxDelay
	}
	spreadPercent := (len(fileName)*13+partNumber*37+attempt*17)%41 - 20
	delay += delay * time.Duration(spreadPercent) / 100
	return delay
}

func isRetryablePUT(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var uploadErr *api.S3UploadError
	if !errors.As(err, &uploadErr) {
		return true
	}
	if uploadErr.StatusCode == 0 {
		return true
	}
	switch uploadErr.StatusCode {
	case 408, 409, 425, 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

func guessContentType(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ct := mime.TypeByExtension(ext); ct != "" {
		return ct
	}
	return "application/octet-stream"
}

func FormatSize(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(bytes)/float64(div), "KMG"[exp])
}
