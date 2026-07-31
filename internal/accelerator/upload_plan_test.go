package accelerator_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/mnemoo/cli/internal/accelerator"
	"github.com/mnemoo/cli/internal/api"
	"github.com/mnemoo/cli/internal/upload"
)

type matchingRemoteClient struct {
	upload.Client
	etag        string
	size        int64
	uploadCalls atomic.Int32
}

func (c *matchingRemoteClient) ListScratch(context.Context, string, string, string) ([]api.ScratchObject, error) {
	return []api.ScratchObject{{Key: "math/book.bin", ETag: c.etag, Size: c.size}}, nil
}

func (c *matchingRemoteClient) UploadToS3(context.Context, string, io.Reader, int64, string, func(int64), func()) error {
	c.uploadCalls.Add(1)
	return nil
}

func TestMatchingRemoteHashNeverReachesAcceleratorTransport(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "book.bin")
	data := []byte("already uploaded")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	etag, err := upload.ComputeETag(path, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	base := &matchingRemoteClient{etag: etag.ETag, size: int64(len(data))}
	proxy := &accelerator.UploadClient{Client: base}
	uploader := upload.NewUploader(proxy)
	plan, err := uploader.Plan(context.Background(), "team", "game", "math", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.ToUpload) != 0 || len(plan.Unchanged) != 1 {
		t.Fatalf("plan upload=%d unchanged=%d", len(plan.ToUpload), len(plan.Unchanged))
	}
	if err := uploader.Execute(context.Background(), plan, nil, nil); err != nil {
		t.Fatal(err)
	}
	if calls := base.uploadCalls.Load(); calls != 0 {
		t.Fatalf("accelerator transport called %d times for matching file", calls)
	}
}
