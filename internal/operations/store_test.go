package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/mnemoo/cli/internal/upload"
)

func TestStoreTracksProgressAndSecuresFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "uploads")
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if info, err := os.Stat(dir); err != nil {
		t.Fatalf("stat store: %v", err)
	} else if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("store permissions = %o, want 700", got)
	}

	handle, err := store.Start(Record{
		ID: "test-operation", Source: "test", Team: "team", Game: "game", Type: "math",
		TotalActions: 2, TotalBytes: 100,
	})
	if err != nil {
		t.Fatalf("start operation: %v", err)
	}
	if info, err := os.Stat(store.path(handle.ID())); err != nil {
		t.Fatalf("stat record: %v", err)
	} else if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("record permissions = %o, want 600", got)
	}

	snapshot := upload.TransferSnapshot{
		UploadedBytes: 75, ConfirmedBytes: 50, TotalBytes: 100,
		Files: []upload.FileProgress{{
			Name: "/math/book.zst", Size: 100, Uploaded: 75, Confirmed: 50,
			Status: "uploading", CompletedParts: 1, ActiveParts: 1, TotalParts: 2, Retries: 1,
		}},
	}
	if err := handle.Update(snapshot, 1); err != nil {
		t.Fatalf("update operation: %v", err)
	}
	record, err := store.Get(handle.ID())
	if err != nil {
		t.Fatalf("get operation: %v", err)
	}
	if record.Status != StatusUploading || record.SentBytes != 75 || record.ConfirmedBytes != 50 || len(record.Files) != 1 {
		t.Fatalf("unexpected progress record: %#v", record)
	}

	if err := handle.Finish(StatusCompleted, nil, snapshot, 2); err != nil {
		t.Fatalf("finish operation: %v", err)
	}
	active, err := store.List(false)
	if err != nil {
		t.Fatalf("list active operations: %v", err)
	}
	if len(active) != 0 {
		t.Fatalf("active operations = %d, want 0", len(active))
	}
	all, err := store.List(true)
	if err != nil {
		t.Fatalf("list all operations: %v", err)
	}
	if len(all) != 1 || all[0].Status != StatusCompleted || all[0].FinishedAt == nil {
		t.Fatalf("unexpected finished operations: %#v", all)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read store: %v", err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == ".tmp" {
			t.Fatalf("temporary atomic-write file was not removed: %s", entry.Name())
		}
	}
}

func TestStoreRejectsUnsafeOperationID(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := store.Get("../secret"); err == nil {
		t.Fatal("expected unsafe operation id to be rejected")
	}
}

func TestQueuedOperationCanBeControlledClaimedAndRetried(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	record := Record{
		ID: "queued", AccountID: "account", Team: "team", Game: "game", Type: "math", Path: "bundle",
		PlanID: "plan", Desired: DesiredRunning, Status: StatusQueued,
	}
	if _, err := store.Enqueue(record); err != nil {
		t.Fatal(err)
	}
	if paused, err := store.Control(record.ID, DesiredPaused); err != nil {
		t.Fatal(err)
	} else if paused.Status != StatusPaused || paused.Desired != DesiredPaused {
		t.Fatalf("unexpected paused record: %#v", paused)
	}
	if resumed, err := store.Control(record.ID, DesiredRunning); err != nil {
		t.Fatal(err)
	} else if resumed.Status != StatusQueued {
		t.Fatalf("resumed queued status = %s, want queued", resumed.Status)
	}
	claimed, ok, err := store.Claim(record.ID, 1234)
	if err != nil || !ok {
		t.Fatalf("claim = %#v, %v, %v", claimed, ok, err)
	}
	if claimed.PID != 1234 || claimed.Status != StatusUploading {
		t.Fatalf("unexpected claimed record: %#v", claimed)
	}
	if _, err := store.Control(record.ID, DesiredCancelled); err != nil {
		t.Fatal(err)
	}
	handle, err := store.Attach(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := handle.Finish(StatusCancelled, nil, upload.TransferSnapshot{}, 0); err != nil {
		t.Fatal(err)
	}
	retry, err := store.Retry(record.ID)
	if err != nil {
		t.Fatal(err)
	}
	if retry.RetryOf != record.ID || retry.AccountID != record.AccountID || retry.Attempt != 2 || retry.Status != StatusQueued || retry.PID != 0 {
		t.Fatalf("unexpected retry: %#v", retry)
	}
}
