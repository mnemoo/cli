package upload

import (
	"context"
	"fmt"
	"math"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mnemoo/cli/internal/api"
)

const maxConcurrent = 4

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
	Client      *api.Client
	ByteCounter *atomic.Int64
}

func NewUploader(client *api.Client) *Uploader {
	return &Uploader{Client: client, ByteCounter: new(atomic.Int64)}
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
	sem := make(chan struct{}, maxConcurrent)
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

			err := u.uploadSingleFile(ctx, plan.Team, plan.Game, e)

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

func (u *Uploader) uploadSingleFile(ctx context.Context, team, game string, entry PlanEntry) error {
	lf := entry.LocalFile

	data, err := os.ReadFile(lf.AbsPath)
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}

	ct := guessContentType(lf.AbsPath)

	if lf.Size <= ChunkSize {
		return u.doSinglePartUpload(ctx, team, game, lf, data, ct)
	}
	return u.doMultiPartUpload(ctx, team, game, lf, data, ct)
}

func (u *Uploader) doSinglePartUpload(ctx context.Context, team, game string, lf *LocalFile, data []byte, contentType string) error {
	body := api.FileUploadBody{
		Team: team,
		Game: game,
		Path: lf.RelPath,
		ETag: lf.ETagResult.ETag,
		Size: lf.Size,
	}

	resp, err := u.Client.InitUpload(ctx, body)
	if err != nil {
		return err
	}

	return retryPut(ctx, u.Client, resp.URL, data, contentType, u.ByteCounter)
}

func (u *Uploader) doMultiPartUpload(ctx context.Context, team, game string, lf *LocalFile, data []byte, contentType string) error {
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
		var expiredPart *api.MultiUploadPartResponse
		for i := range pendingParts {
			part := &pendingParts[i]
			if part.Complete {
				continue
			}

			start := int64(part.Number-1) * ChunkSize
			end := start + part.Size
			if end > int64(len(data)) {
				end = int64(len(data))
			}
			chunk := data[start:end]

			if err := retryPut(ctx, u.Client, part.URL, chunk, contentType, u.ByteCounter); err != nil {
				if isExpiredTokenErr(err) && reinitAttempt < 3 {
					expiredPart = part
					break
				}
				return fmt.Errorf("uploading part %d (%s): %w", part.Number, FormatSize(int64(len(chunk))), err)
			}
		}

		if expiredPart == nil {
			break
		}

		// Presigned URLs expired; re-init to get fresh URLs for remaining parts
		initResp, err = u.Client.InitMultiUpload(ctx, initBody)
		if err != nil {
			return fmt.Errorf("re-initializing multipart upload (attempt %d): %w", reinitAttempt+1, err)
		}
		pendingParts = initResp.Parts
	}

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

func isExpiredTokenErr(err error) bool {
	s := err.Error()
	return strings.Contains(s, "ExpiredToken") || strings.Contains(s, "token has expired") ||
		strings.Contains(s, "Request has expired")
}

func (u *Uploader) executeCopy(ctx context.Context, team, game string, entry PlanEntry) error {
	lf := entry.LocalFile

	if lf.Size <= ChunkSize {
		_, err := u.Client.CopyFile(ctx, team, game, entry.RemoteKey, lf.RelPath)
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
		PathFrom: entry.RemoteKey,
		ETag:     lf.ETagResult.ETag,
		Size:     lf.Size,
		Parts:    parts,
	}

	_, err := u.Client.MultiCopy(ctx, body)
	return err
}

func retryPut(ctx context.Context, client *api.Client, url string, data []byte, contentType string, counter *atomic.Int64) error {
	const maxRetries = 3
	for attempt := range maxRetries {
		err := client.UploadToS3WithCounter(ctx, url, data, contentType, counter)
		if err == nil {
			return nil
		}
		if attempt == maxRetries-1 {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		backoff := time.Duration(math.Pow(2, float64(attempt))) * time.Second
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
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
