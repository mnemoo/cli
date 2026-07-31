// Package operations provides a local, cross-process upload operation registry.
package operations

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mnemoo/cli/internal/upload"
)

const (
	terminalRetention = time.Hour
	staleAfter        = 15 * time.Second
	staleRetention    = 24 * time.Hour
)

type Status string

const (
	StatusQueued    Status = "queued"
	StatusUploading Status = "uploading"
	StatusPaused    Status = "paused"
	StatusCompleted Status = "completed"
	StatusFailed    Status = "failed"
	StatusCancelled Status = "cancelled"
)

type DesiredState string

const (
	DesiredRunning   DesiredState = "running"
	DesiredPaused    DesiredState = "paused"
	DesiredCancelled DesiredState = "cancelled"
)

type File struct {
	Name            string    `json:"name"`
	Size            int64     `json:"size"`
	Sent            int64     `json:"sent"`
	Confirmed       int64     `json:"confirmed"`
	Status          string    `json:"status"`
	LastError       string    `json:"last_error,omitempty"`
	LastActivity    time.Time `json:"last_activity,omitempty"`
	CompletedParts  int       `json:"completed_parts"`
	ActiveParts     int       `json:"active_parts"`
	ConfirmingParts int       `json:"confirming_parts"`
	TotalParts      int       `json:"total_parts"`
	Retries         int       `json:"retries"`
}

type Record struct {
	ID               string                    `json:"id"`
	PID              int                       `json:"pid"`
	Source           string                    `json:"source"`
	AccountID        string                    `json:"account_id,omitempty"`
	Team             string                    `json:"team"`
	Game             string                    `json:"game"`
	Type             string                    `json:"type"`
	Path             string                    `json:"path"`
	PlanID           string                    `json:"plan_id,omitempty"`
	AllowWarnings    bool                      `json:"allow_warnings,omitempty"`
	AllowCompliance  bool                      `json:"allow_compliance_failures,omitempty"`
	Compliance       *upload.ComplianceOptions `json:"compliance,omitempty"`
	Desired          DesiredState              `json:"desired"`
	RetryOf          string                    `json:"retry_of,omitempty"`
	Attempt          int                       `json:"attempt"`
	Status           Status                    `json:"status"`
	StartedAt        time.Time                 `json:"started_at"`
	UpdatedAt        time.Time                 `json:"updated_at"`
	FinishedAt       *time.Time                `json:"finished_at,omitempty"`
	TotalActions     int                       `json:"total_actions"`
	CompletedActions int                       `json:"completed_actions"`
	TotalBytes       int64                     `json:"total_bytes"`
	SentBytes        int64                     `json:"sent_bytes"`
	ConfirmedBytes   int64                     `json:"confirmed_bytes"`
	Error            string                    `json:"error,omitempty"`
	Files            []File                    `json:"files,omitempty"`
	Stale            bool                      `json:"stale,omitempty"`
}

func (r Record) Terminal() bool {
	return r.Status == StatusCompleted || r.Status == StatusFailed || r.Status == StatusCancelled
}

type Store struct {
	dir string
}

type Handle struct {
	mu     sync.Mutex
	store  *Store
	record Record
}

func OpenDefault() (*Store, error) {
	dir := os.Getenv("STAKE_STATE_DIR")
	if dir == "" {
		if configDir := os.Getenv("STAKE_CONFIG_DIR"); configDir != "" {
			dir = filepath.Join(configDir, "state", "uploads")
		} else {
			cacheDir, err := os.UserCacheDir()
			if err != nil {
				return nil, fmt.Errorf("locating operation state directory: %w", err)
			}
			dir = filepath.Join(cacheDir, "stakecli", "uploads")
		}
	}
	return Open(dir)
}

func Open(dir string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("operation state directory is required")
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolving operation state directory: %w", err)
	}
	if err := os.MkdirAll(absDir, 0o700); err != nil {
		return nil, fmt.Errorf("creating operation state directory: %w", err)
	}
	if err := os.Chmod(absDir, 0o700); err != nil {
		return nil, fmt.Errorf("securing operation state directory: %w", err)
	}
	store := &Store{dir: absDir}
	store.cleanup()
	return store, nil
}

func NewID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err == nil {
		return hex.EncodeToString(bytes)
	}
	return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
}

func (s *Store) Start(record Record) (*Handle, error) {
	if record.PID == 0 {
		record.PID = os.Getpid()
	}
	return s.start(record)
}

func (s *Store) Enqueue(record Record) (*Handle, error) {
	record.PID = 0
	return s.start(record)
}

func (s *Store) start(record Record) (*Handle, error) {
	now := time.Now().UTC()
	if record.ID == "" {
		record.ID = NewID()
	}
	if record.Source == "" {
		record.Source = "unknown"
	}
	if record.Desired == "" {
		record.Desired = DesiredRunning
	}
	if record.Attempt == 0 {
		record.Attempt = 1
	}
	if record.Status == "" {
		record.Status = StatusQueued
	}
	if record.StartedAt.IsZero() {
		record.StartedAt = now
	}
	record.UpdatedAt = now
	handle := &Handle{store: s, record: record}
	if err := s.mutate(record.ID, true, func(current *Record) error {
		if current.ID != "" && !current.Terminal() {
			return fmt.Errorf("upload operation %q already exists", record.ID)
		}
		*current = record
		return nil
	}); err != nil {
		return nil, err
	}
	return handle, nil
}

func (s *Store) Attach(id string) (*Handle, error) {
	record, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	return &Handle{store: s, record: record}, nil
}

func (s *Store) Claim(id string, pid int) (Record, bool, error) {
	var claimed bool
	var result Record
	err := s.mutate(id, false, func(record *Record) error {
		if record.Terminal() || record.Status != StatusQueued || record.Desired != DesiredRunning {
			result = *record
			return nil
		}
		record.PID = pid
		record.Status = StatusUploading
		record.UpdatedAt = time.Now().UTC()
		record.Stale = false
		result = *record
		claimed = true
		return nil
	})
	return result, claimed, err
}

func (s *Store) Control(id string, desired DesiredState) (Record, error) {
	if desired != DesiredRunning && desired != DesiredPaused && desired != DesiredCancelled {
		return Record{}, fmt.Errorf("invalid desired upload state %q", desired)
	}
	var result Record
	err := s.mutate(id, false, func(record *Record) error {
		if record.Terminal() {
			return fmt.Errorf("upload operation %q is already %s", id, record.Status)
		}
		record.Desired = desired
		switch desired {
		case DesiredPaused:
			record.Status = StatusPaused
		case DesiredRunning:
			if record.Status == StatusPaused {
				if record.PID == 0 {
					record.Status = StatusQueued
				} else {
					record.Status = StatusUploading
				}
			}
		case DesiredCancelled:
			if record.PID == 0 {
				now := time.Now().UTC()
				record.Status = StatusCancelled
				record.FinishedAt = &now
				record.Error = "cancelled before upload started"
			}
		}
		record.UpdatedAt = time.Now().UTC()
		result = *record
		return nil
	})
	return result, err
}

func (s *Store) Retry(id string) (Record, error) {
	original, err := s.Get(id)
	if err != nil {
		return Record{}, err
	}
	if !original.Terminal() {
		return Record{}, fmt.Errorf("upload operation %q is still %s", id, original.Status)
	}
	if strings.TrimSpace(original.AccountID) == "" {
		return Record{}, fmt.Errorf("upload operation %q predates daemon account scoping and cannot be retried safely; create a new upload plan", id)
	}
	retry := Record{
		ID: NewID(), Source: "retry", AccountID: original.AccountID, Team: original.Team, Game: original.Game,
		Type: original.Type, Path: original.Path, PlanID: original.PlanID,
		AllowWarnings: original.AllowWarnings, AllowCompliance: original.AllowCompliance,
		Compliance: original.Compliance, Desired: DesiredRunning, RetryOf: original.ID,
		Attempt: original.Attempt + 1, Status: StatusQueued,
		TotalActions: original.TotalActions, TotalBytes: original.TotalBytes,
	}
	if _, err := s.Enqueue(retry); err != nil {
		return Record{}, err
	}
	return retry, nil
}

func (h *Handle) ID() string {
	if h == nil {
		return ""
	}
	return h.record.ID
}

func (h *Handle) Update(snapshot upload.TransferSnapshot, completedActions int) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.store.mutate(h.record.ID, false, func(record *Record) error {
		switch record.Desired {
		case DesiredPaused:
			record.Status = StatusPaused
		default:
			record.Status = StatusUploading
		}
		record.UpdatedAt = time.Now().UTC()
		record.CompletedActions = completedActions
		record.SentBytes = snapshot.UploadedBytes
		record.ConfirmedBytes = snapshot.ConfirmedBytes
		record.Files = presentFiles(snapshot.Files)
		h.record = *record
		return nil
	})
}

func (h *Handle) Finish(status Status, err error, snapshot upload.TransferSnapshot, completedActions int) error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now().UTC()
	if writeErr := h.store.mutate(h.record.ID, false, func(record *Record) error {
		record.Status = status
		record.UpdatedAt = now
		record.FinishedAt = &now
		record.CompletedActions = completedActions
		record.SentBytes = snapshot.UploadedBytes
		record.ConfirmedBytes = snapshot.ConfirmedBytes
		record.Files = presentFiles(snapshot.Files)
		if err != nil {
			record.Error = err.Error()
		} else {
			record.Error = ""
		}
		h.record = *record
		return nil
	}); writeErr != nil {
		return writeErr
	}
	path := h.store.path(h.record.ID)
	time.AfterFunc(terminalRetention, func() { _ = os.Remove(path) })
	return nil
}

func (s *Store) List(includeFinished bool) ([]Record, error) {
	s.cleanup()
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	records := make([]Record, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		record, err := s.read(filepath.Join(s.dir, entry.Name()))
		if err != nil {
			continue
		}
		if record.Terminal() && !includeFinished {
			continue
		}
		if !record.Terminal() && now.Sub(record.UpdatedAt) > staleAfter {
			record.Stale = true
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].StartedAt.After(records[j].StartedAt) })
	return records, nil
}

func (s *Store) Get(id string) (Record, error) {
	if err := validateID(id); err != nil {
		return Record{}, err
	}
	record, err := s.read(s.path(id))
	if os.IsNotExist(err) {
		return Record{}, fmt.Errorf("upload operation %q not found", id)
	}
	if err == nil && !record.Terminal() && time.Since(record.UpdatedAt) > staleAfter {
		record.Stale = true
	}
	return record, err
}

func validateID(id string) error {
	if strings.TrimSpace(id) == "" || strings.ContainsAny(id, `/\\`) {
		return errors.New("invalid operation id")
	}
	return nil
}

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Store) mutate(id string, create bool, update func(*Record) error) error {
	if err := validateID(id); err != nil {
		return err
	}
	lock, err := acquireRecordLock(s.dir, id)
	if err != nil {
		return err
	}
	defer releaseRecordLock(lock)
	record, err := s.read(s.path(id))
	if err != nil && (!create || !os.IsNotExist(err)) {
		return err
	}
	if err := update(&record); err != nil {
		return err
	}
	return s.write(record)
}

func (s *Store) write(record Record) error {
	temp, err := os.CreateTemp(s.dir, ".upload-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	encoder := json.NewEncoder(temp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(record); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	target := s.path(record.ID)
	if err := os.Rename(tempPath, target); err != nil {
		_ = os.Remove(target)
		if retryErr := os.Rename(tempPath, target); retryErr != nil {
			return retryErr
		}
	}
	return os.Chmod(target, 0o600)
}

func (s *Store) read(path string) (Record, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Record{}, err
	}
	var record Record
	if err := json.Unmarshal(data, &record); err != nil {
		return Record{}, err
	}
	return record, nil
}

func (s *Store) cleanup() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	now := time.Now().UTC()
	for _, entry := range entries {
		path := filepath.Join(s.dir, entry.Name())
		if strings.HasSuffix(entry.Name(), ".tmp") {
			if info, err := entry.Info(); err == nil && now.Sub(info.ModTime()) > time.Hour {
				_ = os.Remove(path)
			}
			continue
		}
		if filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		record, err := s.read(path)
		if err != nil {
			continue
		}
		if record.Terminal() && record.FinishedAt != nil && now.Sub(*record.FinishedAt) > terminalRetention {
			_ = os.Remove(path)
		} else if !record.Terminal() && now.Sub(record.UpdatedAt) > staleRetention {
			_ = os.Remove(path)
		}
	}
}

func presentFiles(files []upload.FileProgress) []File {
	result := make([]File, len(files))
	for i, file := range files {
		result[i] = File{
			Name: file.Name, Size: file.Size, Sent: file.Uploaded, Confirmed: file.Confirmed,
			Status: file.Status, LastError: file.LastError, LastActivity: file.LastActivity,
			CompletedParts: file.CompletedParts, ActiveParts: file.ActiveParts,
			ConfirmingParts: file.ConfirmingParts, TotalParts: file.TotalParts, Retries: file.Retries,
		}
	}
	return result
}
