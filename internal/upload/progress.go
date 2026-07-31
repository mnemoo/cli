package upload

import (
	"sort"
	"sync"
	"time"
)

// TransferTracker stores logical upload progress. Bytes from a failed attempt
// are reset before retrying, so snapshots never exceed the real file size.
type TransferTracker struct {
	mu    sync.RWMutex
	files map[string]*trackedFile
}

type trackedFile struct {
	name         string
	size         int64
	status       string
	lastError    string
	lastActivity time.Time
	parts        map[int]*trackedPart
}

type trackedPart struct {
	size     int64
	loaded   int64
	attempts int
	active   bool
	complete bool
}

type FileProgress struct {
	Name            string
	Size            int64
	Uploaded        int64
	Confirmed       int64
	Status          string
	LastError       string
	LastActivity    time.Time
	CompletedParts  int
	ActiveParts     int
	ConfirmingParts int
	TotalParts      int
	Retries         int
}

type TransferSnapshot struct {
	Files          []FileProgress
	UploadedBytes  int64
	ConfirmedBytes int64
	TotalBytes     int64
	LastActivity   time.Time
}

func NewTransferTracker(entries []PlanEntry) *TransferTracker {
	t := &TransferTracker{files: make(map[string]*trackedFile, len(entries))}
	for _, entry := range entries {
		parts := make(map[int]*trackedPart)
		if entry.LocalFile != nil && entry.LocalFile.ETagResult != nil && len(entry.LocalFile.ETagResult.Parts) > 0 {
			for _, part := range entry.LocalFile.ETagResult.Parts {
				parts[part.Number] = &trackedPart{size: part.Size}
			}
		} else {
			parts[1] = &trackedPart{size: entry.Size}
		}
		t.files[entry.RemoteKey] = &trackedFile{
			name: entry.RemoteKey, size: entry.Size, status: "pending", parts: parts,
		}
	}
	return t
}

func (t *TransferTracker) StartFile(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if file := t.files[name]; file != nil {
		file.status = "starting"
		file.lastError = ""
	}
}

func (t *TransferTracker) BeginPart(name string, number int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	file := t.files[name]
	if file == nil {
		return
	}
	part := file.parts[number]
	if part == nil {
		return
	}
	part.loaded = 0
	part.active = true
	part.attempts++
	file.status = "uploading"
	file.lastError = ""
	file.lastActivity = time.Now()
}

func (t *TransferTracker) AddBytes(name string, number int, delta int64) {
	if t == nil || delta <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	file := t.files[name]
	if file == nil {
		return
	}
	part := file.parts[number]
	if part == nil || part.complete {
		return
	}
	part.loaded += delta
	if part.loaded > part.size {
		part.loaded = part.size
	}
	file.lastActivity = time.Now()
}

func (t *TransferTracker) CompletePart(name string, number int) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	file := t.files[name]
	if file == nil {
		return
	}
	part := file.parts[number]
	if part == nil {
		return
	}
	part.loaded = part.size
	part.active = false
	part.complete = true
	file.lastActivity = time.Now()
}

func (t *TransferTracker) PartError(name string, number int, err error, retrying bool) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	file := t.files[name]
	if file == nil {
		return
	}
	if part := file.parts[number]; part != nil {
		part.active = false
		part.loaded = 0
	}
	file.lastActivity = time.Now()
	if err != nil {
		file.lastError = err.Error()
	}
	if retrying {
		file.status = "retrying"
	} else {
		file.status = "failed"
	}
}

func (t *TransferTracker) CompleteFile(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if file := t.files[name]; file != nil {
		file.status = "complete"
		file.lastError = ""
		file.lastActivity = time.Now()
		for _, part := range file.parts {
			part.loaded = part.size
			part.active = false
			part.complete = true
		}
	}
}

// FinalizeFile marks a multipart upload whose individual PUTs are confirmed
// but whose CompleteMultipartUpload request has not completed yet.
func (t *TransferTracker) FinalizeFile(name string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if file := t.files[name]; file != nil {
		file.status = "finalizing"
		file.lastError = ""
		file.lastActivity = time.Now()
	}
}

func (t *TransferTracker) FailFile(name string, err error) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if file := t.files[name]; file != nil {
		file.status = "failed"
		file.lastActivity = time.Now()
		if err != nil {
			file.lastError = err.Error()
		}
	}
}

func (t *TransferTracker) Snapshot() TransferSnapshot {
	if t == nil {
		return TransferSnapshot{}
	}
	t.mu.RLock()
	defer t.mu.RUnlock()

	snapshot := TransferSnapshot{Files: make([]FileProgress, 0, len(t.files))}
	for _, file := range t.files {
		progress := FileProgress{
			Name: file.name, Size: file.size, Status: file.status,
			LastError: file.lastError, LastActivity: file.lastActivity, TotalParts: len(file.parts),
		}
		if progress.LastError != "" && progress.Status != "failed" && progress.Status != "complete" {
			progress.Status = "retrying"
		}
		for _, part := range file.parts {
			progress.Uploaded += part.loaded
			if part.complete {
				progress.CompletedParts++
				progress.Confirmed += part.size
			}
			if part.active {
				if part.loaded >= part.size {
					progress.ConfirmingParts++
				} else {
					progress.ActiveParts++
				}
			}
			if part.attempts > 1 {
				progress.Retries += part.attempts - 1
			}
		}
		if progress.Uploaded > progress.Size {
			progress.Uploaded = progress.Size
		}
		snapshot.UploadedBytes += progress.Uploaded
		snapshot.ConfirmedBytes += progress.Confirmed
		snapshot.TotalBytes += progress.Size
		if progress.LastActivity.After(snapshot.LastActivity) {
			snapshot.LastActivity = progress.LastActivity
		}
		snapshot.Files = append(snapshot.Files, progress)
	}
	sort.Slice(snapshot.Files, func(i, j int) bool {
		return snapshot.Files[i].Name < snapshot.Files[j].Name
	})
	return snapshot
}
