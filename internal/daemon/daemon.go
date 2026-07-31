// Package daemon runs StakeCLI background work independently from MCP clients.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mnemoo/cli/internal/conversations"
	"github.com/mnemoo/cli/internal/operations"
	"github.com/mnemoo/cli/internal/service"
	"github.com/mnemoo/cli/internal/upload"
)

const (
	defaultReviewInterval = 15 * time.Second
	heartbeatInterval     = 2 * time.Second
	heartbeatStaleAfter   = 10 * time.Second
)

type Paths struct {
	Dir   string
	State string
	Stop  string
	Lock  string
	Log   string
}

type State struct {
	Running          bool      `json:"running"`
	PID              int       `json:"pid,omitempty"`
	AccountID        string    `json:"account_id,omitempty"`
	StartedAt        time.Time `json:"started_at,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
	ReviewIntervalMS int64     `json:"review_interval_ms"`
	LastReviewPoll   time.Time `json:"last_review_poll,omitempty"`
	LastReviewError  string    `json:"last_review_error,omitempty"`
	TeamsPolled      int       `json:"teams_polled"`
	HooksDelivered   int       `json:"hooks_delivered"`
	ActiveOperation  string    `json:"active_operation,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
}

type Options struct {
	ReviewInterval time.Duration
}

type runtimeState struct {
	mu    sync.Mutex
	state State
}

func DefaultPaths() (Paths, error) {
	dir := os.Getenv("STAKE_STATE_DIR")
	if dir != "" {
		dir = filepath.Join(filepath.Dir(dir), "daemon")
	} else if configDir := os.Getenv("STAKE_CONFIG_DIR"); configDir != "" {
		dir = filepath.Join(configDir, "state", "daemon")
	} else {
		cacheDir, err := os.UserCacheDir()
		if err != nil {
			return Paths{}, fmt.Errorf("locating daemon state directory: %w", err)
		}
		dir = filepath.Join(cacheDir, "stakecli", "daemon")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return Paths{}, err
	}
	return Paths{
		Dir: absolute, State: filepath.Join(absolute, "status.json"),
		Stop: filepath.Join(absolute, "stop"), Lock: filepath.Join(absolute, "daemon.lock"),
		Log: filepath.Join(absolute, "daemon.log"),
	}, nil
}

func ReadState() (State, error) {
	paths, err := DefaultPaths()
	if err != nil {
		return State{}, err
	}
	data, err := os.ReadFile(paths.State)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return State{}, nil
		}
		return State{}, err
	}
	var state State
	if err := json.Unmarshal(data, &state); err != nil {
		return State{}, err
	}
	if state.Running && time.Since(state.UpdatedAt) > heartbeatStaleAfter {
		state.Running = false
		state.LastError = "daemon heartbeat is stale"
	}
	return state, nil
}

func RequestStop() error {
	paths, err := DefaultPaths()
	if err != nil {
		return err
	}
	if err := secureDir(paths.Dir); err != nil {
		return err
	}
	return os.WriteFile(paths.Stop, []byte("stop\n"), 0o600)
}

func Run(ctx context.Context, appService *service.Service, accountID string, options Options) error {
	if appService == nil {
		return errors.New("daemon service is required")
	}
	if accountID == "" {
		return errors.New("daemon account ID is required")
	}
	paths, err := DefaultPaths()
	if err != nil {
		return err
	}
	if err := secureDir(paths.Dir); err != nil {
		return err
	}
	lock, err := acquireDaemonLock(paths.Lock)
	if err != nil {
		return err
	}
	defer releaseDaemonLock(lock)
	_ = os.Remove(paths.Stop)

	interval := options.ReviewInterval
	if interval <= 0 {
		interval = defaultReviewInterval
	}
	started := time.Now().UTC()
	runtime := &runtimeState{state: State{
		Running: true, PID: os.Getpid(), AccountID: accountID, StartedAt: started, UpdatedAt: started,
		ReviewIntervalMS: interval.Milliseconds(),
	}}
	if err := runtime.write(paths.State); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	manager, err := conversations.NewDefaultManager(appService, accountID)
	if err != nil {
		return err
	}

	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		reviewLoop(runCtx, appService, manager, interval, runtime)
	}()
	go func() {
		defer workers.Done()
		uploadLoop(runCtx, appService, accountID, runtime)
	}()

	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			workers.Wait()
			runtime.mu.Lock()
			runtime.state.Running = false
			runtime.state.UpdatedAt = time.Now().UTC()
			runtime.mu.Unlock()
			_ = runtime.write(paths.State)
			return nil
		case <-ticker.C:
			if _, err := os.Stat(paths.Stop); err == nil {
				_ = os.Remove(paths.Stop)
				cancel()
				continue
			}
			runtime.mu.Lock()
			runtime.state.UpdatedAt = time.Now().UTC()
			runtime.mu.Unlock()
			_ = runtime.write(paths.State)
		}
	}
}

func reviewLoop(ctx context.Context, appService *service.Service, manager *conversations.Manager, interval time.Duration, runtime *runtimeState) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			pollReviews(ctx, appService, manager, runtime)
			timer.Reset(interval)
		}
	}
}

func pollReviews(ctx context.Context, appService *service.Service, manager *conversations.Manager, runtime *runtimeState) {
	teams, err := appService.ListTeams(ctx)
	hooksDelivered := 0
	teamsPolled := 0
	lastError := ""
	if err == nil {
		for _, team := range teams {
			if ctx.Err() != nil {
				return
			}
			result, refreshErr := manager.RefreshTeam(ctx, team.Slug)
			if refreshErr != nil {
				lastError = refreshErr.Error()
				continue
			}
			teamsPolled++
			for _, hook := range result.Hooks {
				if hook.Success {
					hooksDelivered++
				}
			}
			if len(result.Errors) > 0 {
				lastError = result.Errors[len(result.Errors)-1].Error
			}
		}
	} else {
		lastError = err.Error()
	}
	runtime.mu.Lock()
	runtime.state.LastReviewPoll = time.Now().UTC()
	runtime.state.LastReviewError = lastError
	runtime.state.TeamsPolled = teamsPolled
	runtime.state.HooksDelivered += hooksDelivered
	runtime.mu.Unlock()
}

func uploadLoop(ctx context.Context, appService *service.Service, accountID string, runtime *runtimeState) {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			record, ok := nextQueuedOperation(accountID)
			if !ok {
				continue
			}
			runUpload(ctx, appService, record, runtime)
		}
	}
}

func nextQueuedOperation(accountID string) (operations.Record, bool) {
	store, err := operations.OpenDefault()
	if err != nil {
		return operations.Record{}, false
	}
	records, err := store.List(false)
	if err != nil {
		return operations.Record{}, false
	}
	for _, record := range records {
		if record.AccountID != accountID || record.Status != operations.StatusQueued || record.Desired != operations.DesiredRunning {
			continue
		}
		claimed, ok, err := store.Claim(record.ID, os.Getpid())
		if err == nil && ok {
			return claimed, true
		}
	}
	return operations.Record{}, false
}

func runUpload(parent context.Context, appService *service.Service, record operations.Record, runtime *runtimeState) {
	store, err := operations.OpenDefault()
	if err != nil {
		return
	}
	handle, err := store.Attach(record.ID)
	if err != nil {
		return
	}
	runtime.mu.Lock()
	runtime.state.ActiveOperation = record.ID
	runtime.mu.Unlock()
	defer func() {
		runtime.mu.Lock()
		runtime.state.ActiveOperation = ""
		runtime.mu.Unlock()
	}()

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	gate := upload.NewPauseGate()
	monitorDone := make(chan struct{})
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				current, getErr := store.Get(record.ID)
				if getErr != nil {
					cancel()
					return
				}
				switch current.Desired {
				case operations.DesiredCancelled:
					cancel()
					return
				case operations.DesiredPaused:
					if !gate.IsPaused() {
						gate.Toggle()
					}
				case operations.DesiredRunning:
					if gate.IsPaused() {
						gate.Toggle()
					}
				}
			}
		}
	}()

	target := service.UploadTarget{
		Team: record.Team, Game: record.Game, Type: record.Type,
		Path: record.Path, Compliance: record.Compliance,
	}
	prepared, err := appService.PrepareExpected(
		ctx, target, record.PlanID, record.AllowWarnings, record.AllowCompliance,
	)
	if err == nil {
		err = appService.ExecutePreparedWithOptions(ctx, prepared, service.ExecuteOptions{
			Source: "daemon", Path: record.Path, PlanID: record.PlanID,
			OperationID: record.ID, Gate: gate,
		})
	} else {
		status := operations.StatusFailed
		if errors.Is(err, context.Canceled) {
			status = operations.StatusCancelled
		}
		_ = handle.Finish(status, err, upload.TransferSnapshot{}, 0)
	}
	cancel()
	<-monitorDone
	if err != nil {
		runtime.mu.Lock()
		runtime.state.LastError = fmt.Sprintf("upload %s: %v", record.ID, err)
		runtime.mu.Unlock()
	}
}

func (r *runtimeState) write(path string) error {
	r.mu.Lock()
	state := r.state
	r.mu.Unlock()
	return writePrivateJSON(path, state)
}

func secureDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func writePrivateJSON(path string, value any) error {
	if err := secureDir(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".daemon-*.tmp")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer func() { _ = os.Remove(temp) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
