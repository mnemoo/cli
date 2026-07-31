package upload

import "testing"

func TestTransferTrackerResetsFailedAttemptBytes(t *testing.T) {
	entry := PlanEntry{RemoteKey: "/math/book.zst", Size: 32}
	tracker := NewTransferTracker([]PlanEntry{entry})
	tracker.StartFile(entry.RemoteKey)
	tracker.BeginPart(entry.RemoteKey, 1)
	tracker.AddBytes(entry.RemoteKey, 1, 32)
	tracker.PartError(entry.RemoteKey, 1, errTestUpload, true)

	if got := tracker.Snapshot().UploadedBytes; got != 0 {
		t.Fatalf("bytes after failed attempt = %d, want 0", got)
	}

	tracker.BeginPart(entry.RemoteKey, 1)
	tracker.AddBytes(entry.RemoteKey, 1, 12)
	snapshot := tracker.Snapshot()
	if snapshot.UploadedBytes != 12 {
		t.Fatalf("bytes during retry = %d, want 12", snapshot.UploadedBytes)
	}
	if snapshot.Files[0].Retries != 1 {
		t.Fatalf("retries = %d, want 1", snapshot.Files[0].Retries)
	}

	tracker.CompletePart(entry.RemoteKey, 1)
	tracker.CompleteFile(entry.RemoteKey)
	snapshot = tracker.Snapshot()
	if snapshot.UploadedBytes != entry.Size || snapshot.Files[0].Status != "complete" {
		t.Fatalf("completed snapshot = %#v", snapshot)
	}
}

func TestTransferTrackerDistinguishesConfirmingAndFinalizing(t *testing.T) {
	entry := PlanEntry{RemoteKey: "/math/book.zst", Size: 10}
	tracker := NewTransferTracker([]PlanEntry{entry})
	tracker.StartFile(entry.RemoteKey)
	tracker.BeginPart(entry.RemoteKey, 1)
	tracker.AddBytes(entry.RemoteKey, 1, entry.Size)

	file := tracker.Snapshot().Files[0]
	if file.ActiveParts != 0 || file.ConfirmingParts != 1 || file.Status != "uploading" {
		t.Fatalf("sent part state = active:%d confirming:%d status:%s", file.ActiveParts, file.ConfirmingParts, file.Status)
	}

	tracker.CompletePart(entry.RemoteKey, 1)
	tracker.FinalizeFile(entry.RemoteKey)
	snapshot := tracker.Snapshot()
	file = snapshot.Files[0]
	if file.CompletedParts != 1 || file.ConfirmingParts != 0 || file.Status != "finalizing" {
		t.Fatalf("finalizing state = completed:%d confirming:%d status:%s", file.CompletedParts, file.ConfirmingParts, file.Status)
	}
	if file.Confirmed != entry.Size || snapshot.ConfirmedBytes != entry.Size {
		t.Fatalf("confirmed bytes = file:%d total:%d, want %d", file.Confirmed, snapshot.ConfirmedBytes, entry.Size)
	}
	if file.LastActivity.IsZero() || snapshot.LastActivity.IsZero() {
		t.Fatal("last activity was not recorded")
	}
}

type testUploadError string

func (e testUploadError) Error() string { return string(e) }

const errTestUpload testUploadError = "temporary upload failure"
