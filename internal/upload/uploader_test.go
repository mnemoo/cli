package upload

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mnemoo/cli/internal/api"
)

type scriptedUploadClient struct {
	Client
	mu        sync.Mutex
	errors    []error
	callTimes []time.Time
}

func (c *scriptedUploadClient) UploadToS3(_ context.Context, _ string, body io.Reader, _ int64, _ string, onRead func(int64), onBodySent func()) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if onRead != nil {
		onRead(int64(len(data)))
	}
	if onBodySent != nil {
		onBodySent()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	call := len(c.callTimes)
	c.callTimes = append(c.callTimes, time.Now())
	if call < len(c.errors) {
		return c.errors[call]
	}
	return nil
}

func TestRetryPutRetriesImmediatelyAndKeepsLogicalProgress(t *testing.T) {
	client := &scriptedUploadClient{errors: []error{
		&api.S3UploadError{StatusCode: 503, Size: 10, Body: "SlowDown"},
		nil,
	}}
	entry := PlanEntry{RemoteKey: "/math/book.zst", Size: 10}
	uploader := NewUploader(client)
	uploader.Tracker = NewTransferTracker([]PlanEntry{entry})

	err := uploader.retryPut(context.Background(), nil, entry.RemoteKey, 1, entry.Size, "test", "application/octet-stream", func() io.Reader {
		return strings.NewReader("0123456789")
	})
	if err != nil {
		t.Fatalf("retryPut: %v", err)
	}
	if len(client.callTimes) != 2 {
		t.Fatalf("calls = %d, want 2", len(client.callTimes))
	}
	if gap := client.callTimes[1].Sub(client.callTimes[0]); gap > 250*time.Millisecond {
		t.Fatalf("first retry was not immediate: %s", gap)
	}

	snapshot := uploader.Tracker.Snapshot()
	if snapshot.UploadedBytes != entry.Size {
		t.Fatalf("logical uploaded bytes = %d, want %d", snapshot.UploadedBytes, entry.Size)
	}
	if got := snapshot.Files[0].Retries; got != 1 {
		t.Fatalf("retries = %d, want 1", got)
	}
	if got := uploader.ByteCounter.Load(); got != 2*entry.Size {
		t.Fatalf("wire byte counter = %d, want %d", got, 2*entry.Size)
	}
}

func TestRetryPutSurvivesExtendedEOFSequence(t *testing.T) {
	errorsByAttempt := make([]error, maxPUTAttempts)
	for attempt := 0; attempt < maxPUTAttempts-1; attempt++ {
		errorsByAttempt[attempt] = &api.S3UploadError{Size: 10, Err: io.EOF}
	}
	client := &scriptedUploadClient{errors: errorsByAttempt}
	entry := PlanEntry{RemoteKey: "/math/book.zst", Size: 10}
	uploader := NewUploader(client)
	uploader.retryWait = func(context.Context, time.Duration) error { return nil }
	uploader.Tracker = NewTransferTracker([]PlanEntry{entry})

	err := uploader.retryPut(context.Background(), nil, entry.RemoteKey, 1, entry.Size, "test", "application/octet-stream", func() io.Reader {
		return strings.NewReader("0123456789")
	})
	if err != nil {
		t.Fatalf("retryPut after EOF sequence: %v", err)
	}
	if got := len(client.callTimes); got != maxPUTAttempts {
		t.Fatalf("PUT calls = %d, want %d", got, maxPUTAttempts)
	}
	progress := uploader.Tracker.Snapshot().Files[0]
	if progress.Confirmed != entry.Size || progress.Retries != maxPUTAttempts-1 {
		t.Fatalf("progress after recovery = %+v", progress)
	}
}

type blockingUploadClient struct {
	Client
	started chan struct{}
	release chan struct{}
	mu      sync.Mutex
	active  int
	maximum int
}

func (c *blockingUploadClient) UploadToS3(ctx context.Context, _ string, body io.Reader, _ int64, _ string, onRead func(int64), onBodySent func()) error {
	c.mu.Lock()
	c.active++
	if c.active > c.maximum {
		c.maximum = c.active
	}
	c.mu.Unlock()
	c.started <- struct{}{}

	select {
	case <-c.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	data, err := io.ReadAll(body)
	if onRead != nil {
		onRead(int64(len(data)))
	}
	if onBodySent != nil {
		onBodySent()
	}
	c.mu.Lock()
	c.active--
	c.mu.Unlock()
	return err
}

func TestRetryPutUsesConfiguredGlobalTransferSlots(t *testing.T) {
	const extraTransfers = 6
	transferCount := maxConcurrentTransfers + extraTransfers
	client := &blockingUploadClient{
		started: make(chan struct{}, transferCount),
		release: make(chan struct{}),
	}
	entries := make([]PlanEntry, transferCount)
	for i := range entries {
		entries[i] = PlanEntry{RemoteKey: "/math/file-" + string(rune('a'+i)), Size: 1}
	}
	uploader := NewUploader(client)
	uploader.Tracker = NewTransferTracker(entries)

	var wg sync.WaitGroup
	for _, entry := range entries {
		entry := entry
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := uploader.retryPut(context.Background(), nil, entry.RemoteKey, 1, 1, "test", "", func() io.Reader {
				return strings.NewReader("x")
			}); err != nil {
				t.Errorf("retryPut(%s): %v", entry.RemoteKey, err)
			}
		}()
	}

	for range maxConcurrentTransfers {
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatalf("%d transfers did not start concurrently", maxConcurrentTransfers)
		}
	}
	select {
	case <-client.started:
		t.Fatalf("more than %d transfers started", maxConcurrentTransfers)
	case <-time.After(50 * time.Millisecond):
	}

	close(client.release)
	wg.Wait()
	client.mu.Lock()
	maximum := client.maximum
	client.mu.Unlock()
	if maximum != maxConcurrentTransfers {
		t.Fatalf("maximum concurrency = %d, want %d", maximum, maxConcurrentTransfers)
	}
}

func TestTransferLimiterPrioritizesFileWithoutActivePUT(t *testing.T) {
	limiter := newTransferLimiter(maxConcurrentTransfers, maxOutstandingTransfers)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial := make([]*transferPermit, 0, maxConcurrentTransfers)
	for range maxConcurrentTransfers {
		permit, err := limiter.acquire(ctx, "large")
		if err != nil {
			t.Fatalf("acquire initial large permit: %v", err)
		}
		initial = append(initial, permit)
	}

	type permitResult struct {
		permit *transferPermit
		err    error
	}
	nextLarge := make(chan permitResult, 1)
	go func() {
		permit, err := limiter.acquire(ctx, "large")
		nextLarge <- permitResult{permit: permit, err: err}
	}()
	time.Sleep(10 * time.Millisecond)

	small := make(chan permitResult, 1)
	go func() {
		permit, err := limiter.acquire(ctx, "small")
		small <- permitResult{permit: permit, err: err}
	}()
	time.Sleep(10 * time.Millisecond)

	initial[0].markBodySent()
	select {
	case result := <-small:
		if result.err != nil {
			t.Fatalf("acquire small permit: %v", result.err)
		}
		result.permit.release()
	case result := <-nextLarge:
		if result.err == nil {
			result.permit.release()
		}
		t.Fatal("large file took another permit before the waiting small file")
	case <-time.After(time.Second):
		t.Fatal("small file did not receive the released permit")
	}

	select {
	case result := <-nextLarge:
		if result.err != nil {
			t.Fatalf("acquire next large permit: %v", result.err)
		}
		result.permit.release()
	case <-time.After(time.Second):
		t.Fatal("large file did not resume after the small file")
	}

	for _, permit := range initial {
		permit.release()
	}
}

func TestTransferLimiterPipelinesNextBodyBeforeConfirmation(t *testing.T) {
	limiter := newTransferLimiter(1, 2)
	ctx := context.Background()
	first, err := limiter.acquire(ctx, "book")
	if err != nil {
		t.Fatalf("acquire first permit: %v", err)
	}

	next := make(chan *transferPermit, 1)
	go func() {
		permit, acquireErr := limiter.acquire(ctx, "book")
		if acquireErr == nil {
			next <- permit
		}
	}()

	first.markBodySent()
	select {
	case second := <-next:
		second.release()
	case <-time.After(time.Second):
		t.Fatal("next request did not start after the first body was written")
	}
	first.release()
}

func TestRetryablePUTStatusPolicy(t *testing.T) {
	if !isRetryablePUT(&api.S3UploadError{StatusCode: 503}) {
		t.Fatal("503 should be retryable")
	}
	if isRetryablePUT(&api.S3UploadError{StatusCode: 400}) {
		t.Fatal("400 should not be retryable")
	}
	if isRetryablePUT(context.Canceled) {
		t.Fatal("cancellation should not be retryable")
	}
}

type multipartUploadClient struct {
	Client
	started  chan struct{}
	release  chan struct{}
	mu       sync.Mutex
	complete int
}

type recoveringMultipartClient struct {
	Client
	mu            sync.Mutex
	initCalls     int
	uploadCalls   int
	completeCalls int
}

func (c *recoveringMultipartClient) InitMultiUpload(_ context.Context, body api.FileMultiUploadBody) (*api.MultiUploadInitResponse, error) {
	c.mu.Lock()
	c.initCalls++
	initCall := c.initCalls
	c.mu.Unlock()
	parts := make([]api.MultiUploadPartResponse, len(body.Parts))
	for index, part := range body.Parts {
		parts[index] = api.MultiUploadPartResponse{
			URL: fmt.Sprintf("part-url-%d", initCall), Size: part.Size, Number: part.Number,
		}
	}
	return &api.MultiUploadInitResponse{Key: "key", UploadID: fmt.Sprintf("upload-%d", initCall), Parts: parts}, nil
}

func (c *recoveringMultipartClient) UploadToS3(_ context.Context, _ string, body io.Reader, size int64, _ string, onRead func(int64), onBodySent func()) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if onRead != nil {
		onRead(int64(len(data)))
	}
	if onBodySent != nil {
		onBodySent()
	}
	c.mu.Lock()
	c.uploadCalls++
	call := c.uploadCalls
	c.mu.Unlock()
	if call <= maxPUTAttempts {
		return &api.S3UploadError{Size: size, Err: io.EOF}
	}
	return nil
}

func (c *recoveringMultipartClient) CompleteMultiUpload(_ context.Context, _ api.FileCompleteBody) (*api.CompleteResponse, error) {
	c.mu.Lock()
	c.completeCalls++
	c.mu.Unlock()
	return &api.CompleteResponse{}, nil
}

func TestMultipartReinitializesAfterExhaustedTransportRetries(t *testing.T) {
	data := []byte("multipart-payload")
	path := filepath.Join(t.TempDir(), "books_base.jsonl.zst")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	local := &LocalFile{
		RelPath: "/math/books_base.jsonl.zst", AbsPath: path, Size: int64(len(data)),
		ETagResult: &ETagResult{ETag: "etag", Parts: []PartHash{{Number: 1, Size: int64(len(data))}}},
	}
	entry := PlanEntry{RemoteKey: local.RelPath, LocalFile: local, Size: local.Size}
	client := &recoveringMultipartClient{}
	uploader := NewUploader(client)
	uploader.retryWait = func(context.Context, time.Duration) error { return nil }
	uploader.Tracker = NewTransferTracker([]PlanEntry{entry})

	if err := uploader.doMultiPartUpload(context.Background(), "team", "game", local, file, "application/octet-stream", nil); err != nil {
		t.Fatalf("multipart recovery: %v", err)
	}
	client.mu.Lock()
	initCalls, uploadCalls, completeCalls := client.initCalls, client.uploadCalls, client.completeCalls
	client.mu.Unlock()
	if initCalls != 2 || uploadCalls != maxPUTAttempts+1 || completeCalls != 1 {
		t.Fatalf("init/upload/complete calls = %d/%d/%d", initCalls, uploadCalls, completeCalls)
	}
	progress := uploader.Tracker.Snapshot().Files[0]
	if progress.Confirmed != local.Size || progress.Retries != maxPUTAttempts {
		t.Fatalf("progress after multipart recovery = %+v", progress)
	}
}

func (c *multipartUploadClient) InitMultiUpload(_ context.Context, body api.FileMultiUploadBody) (*api.MultiUploadInitResponse, error) {
	parts := make([]api.MultiUploadPartResponse, len(body.Parts))
	for i, part := range body.Parts {
		parts[i] = api.MultiUploadPartResponse{URL: "part", Size: part.Size, Number: part.Number}
	}
	return &api.MultiUploadInitResponse{Key: "key", UploadID: "upload", Parts: parts}, nil
}

func (c *multipartUploadClient) UploadToS3(ctx context.Context, _ string, body io.Reader, _ int64, _ string, _ func(int64), _ func()) error {
	if _, ok := body.(*io.SectionReader); !ok {
		return testUploadError("multipart body is not a section reader")
	}
	c.started <- struct{}{}
	select {
	case <-c.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *multipartUploadClient) CompleteMultiUpload(_ context.Context, _ api.FileCompleteBody) (*api.CompleteResponse, error) {
	c.mu.Lock()
	c.complete++
	c.mu.Unlock()
	return &api.CompleteResponse{}, nil
}

func TestMultipartUploadStreamsConfiguredPartsInParallel(t *testing.T) {
	partCount := maxConcurrentTransfers + 1
	file, err := os.CreateTemp(t.TempDir(), "sparse-book-*.zst")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	if err := file.Truncate(int64(partCount) * PartSize); err != nil {
		t.Fatal(err)
	}

	parts := make([]PartHash, partCount)
	for i := range parts {
		parts[i] = PartHash{Number: i + 1, Size: PartSize}
	}
	local := &LocalFile{
		RelPath: "/math/book.zst", AbsPath: file.Name(), Size: int64(partCount) * PartSize,
		ETagResult: &ETagResult{Parts: parts},
	}
	entry := PlanEntry{RemoteKey: local.RelPath, Size: local.Size, LocalFile: local}
	client := &multipartUploadClient{started: make(chan struct{}, partCount), release: make(chan struct{})}
	uploader := NewUploader(client)
	uploader.Tracker = NewTransferTracker([]PlanEntry{entry})

	done := make(chan error, 1)
	go func() {
		done <- uploader.doMultiPartUpload(context.Background(), "team", "game", local, file, "application/octet-stream", nil)
	}()
	for range maxConcurrentTransfers {
		select {
		case <-client.started:
		case <-time.After(time.Second):
			t.Fatalf("%d multipart PUTs did not start", maxConcurrentTransfers)
		}
	}
	select {
	case <-client.started:
		t.Fatal("seventh multipart PUT bypassed the global limit")
	case <-time.After(50 * time.Millisecond):
	}
	close(client.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("doMultiPartUpload: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("multipart upload did not finish")
	}
	client.mu.Lock()
	complete := client.complete
	client.mu.Unlock()
	if complete != 1 {
		t.Fatalf("complete calls = %d, want 1", complete)
	}
}

type expiringSingleUploadClient struct {
	Client
	mu          sync.Mutex
	initCalls   int
	uploadCalls int
}

func (c *expiringSingleUploadClient) InitUpload(_ context.Context, _ api.FileUploadBody) (*api.UploadResponse, error) {
	c.mu.Lock()
	c.initCalls++
	call := c.initCalls
	c.mu.Unlock()
	return &api.UploadResponse{URL: fmt.Sprintf("url-%d", call)}, nil
}

func (c *expiringSingleUploadClient) UploadToS3(_ context.Context, _ string, body io.Reader, _ int64, _ string, onRead func(int64), onBodySent func()) error {
	c.mu.Lock()
	c.uploadCalls++
	call := c.uploadCalls
	c.mu.Unlock()
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if onRead != nil {
		onRead(int64(len(data)))
	}
	if onBodySent != nil {
		onBodySent()
	}
	if call == 1 {
		return &api.S3UploadError{StatusCode: 403, Body: "<Code>ExpiredToken</Code>"}
	}
	return nil
}

func TestSinglePartRefreshesExpiredPresignedURL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "small.bin")
	if err := os.WriteFile(path, []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	local := &LocalFile{RelPath: "/front/small.bin", AbsPath: path, Size: 7, ETagResult: &ETagResult{ETag: "etag"}}
	entry := PlanEntry{RemoteKey: local.RelPath, LocalFile: local, Size: local.Size}
	client := &expiringSingleUploadClient{}
	uploader := NewUploader(client)
	uploader.Tracker = NewTransferTracker([]PlanEntry{entry})

	if err := uploader.doSinglePartUpload(context.Background(), "team", "game", local, file, "application/octet-stream", nil); err != nil {
		t.Fatalf("doSinglePartUpload: %v", err)
	}
	client.mu.Lock()
	initCalls, uploadCalls := client.initCalls, client.uploadCalls
	client.mu.Unlock()
	if initCalls != 2 || uploadCalls != 2 {
		t.Fatalf("init/upload calls = %d/%d, want 2/2", initCalls, uploadCalls)
	}
	if retries := uploader.Tracker.Snapshot().Files[0].Retries; retries != 1 {
		t.Fatalf("retries = %d, want 1", retries)
	}
}
