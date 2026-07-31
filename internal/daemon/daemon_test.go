package daemon

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mnemoo/cli/internal/api"
	"github.com/mnemoo/cli/internal/operations"
	"github.com/mnemoo/cli/internal/service"
	"github.com/mnemoo/cli/internal/stake"
)

type daemonGateway struct{}

func (*daemonGateway) ListScratch(context.Context, string, string, string) ([]api.ScratchObject, error) {
	return nil, nil
}
func (*daemonGateway) InitUpload(_ context.Context, body api.FileUploadBody) (*api.UploadResponse, error) {
	return &api.UploadResponse{Key: body.Path, URL: "memory://upload"}, nil
}
func (*daemonGateway) InitMultiUpload(context.Context, api.FileMultiUploadBody) (*api.MultiUploadInitResponse, error) {
	return nil, errors.New("not used")
}
func (*daemonGateway) CompleteMultiUpload(context.Context, api.FileCompleteBody) (*api.CompleteResponse, error) {
	return nil, errors.New("not used")
}
func (*daemonGateway) CopyFile(context.Context, string, string, string, string) (*api.CopyResponse, error) {
	return nil, errors.New("not used")
}
func (*daemonGateway) MultiCopy(context.Context, api.FileMulticopyBody) (*api.MultiUploadInitResponse, error) {
	return nil, errors.New("not used")
}
func (*daemonGateway) DeleteFiles(context.Context, string, string, []string) (*api.DeleteResponse, error) {
	return &api.DeleteResponse{}, nil
}
func (*daemonGateway) UploadToS3(_ context.Context, _ string, body io.Reader, size int64, _ string, onRead func(int64), onBodySent func()) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	if int64(len(data)) != size {
		return errors.New("unexpected upload size")
	}
	if onRead != nil {
		onRead(size)
	}
	if onBodySent != nil {
		onBodySent()
	}
	return nil
}
func (*daemonGateway) GetCurrentUser(context.Context) (*stake.User, error) {
	return &stake.User{ID: "account"}, nil
}
func (*daemonGateway) ListTeams(context.Context) ([]stake.Team, error) { return nil, nil }
func (*daemonGateway) ListTeamGames(context.Context, string) ([]stake.Game, error) {
	return nil, nil
}
func (*daemonGateway) GetGameDetail(context.Context, string, string) (*stake.GameDetail, error) {
	return nil, errors.New("not used")
}
func (*daemonGateway) GetGameVersions(context.Context, string, string) ([]stake.GameVersion, error) {
	return nil, errors.New("not used")
}
func (*daemonGateway) GetTeamBalance(context.Context, string) (*stake.TeamBalance, error) {
	return nil, errors.New("not used")
}
func (*daemonGateway) GetTeamStats(context.Context, string, time.Time, time.Time) ([]stake.TeamOperatorStat, error) {
	return nil, errors.New("not used")
}
func (*daemonGateway) GetPaymentMethod(context.Context, string) (*stake.PaymentMethod, error) {
	return nil, errors.New("not used")
}
func (*daemonGateway) GetGameStats(context.Context, string, string) (*stake.GameStats, error) {
	return nil, errors.New("not used")
}
func (*daemonGateway) GetGameStatsRange(context.Context, string, string, time.Time, time.Time) (*stake.GameStats, error) {
	return nil, errors.New("not used")
}
func (*daemonGateway) GetGameGraph(context.Context, string, string, time.Time, time.Time) (*stake.StatsGraph, error) {
	return nil, errors.New("not used")
}
func (*daemonGateway) Publish(context.Context, string, string, string) (*stake.PublishResult, error) {
	return nil, errors.New("not used")
}

func TestDaemonCompletesQueuedUploadIndependently(t *testing.T) {
	root := t.TempDir()
	t.Setenv("STAKE_CONFIG_DIR", filepath.Join(root, "config"))
	t.Setenv("STAKE_STATE_DIR", filepath.Join(root, "state", "uploads"))
	bundle := filepath.Join(root, "bundle")
	if err := os.MkdirAll(bundle, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bundle, "index.html"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	appService := service.New(&daemonGateway{})
	prepared, err := appService.PrepareUpload(context.Background(), service.UploadTarget{
		Team: "team", Game: "game", Type: "front", Path: bundle,
	})
	if err != nil {
		t.Fatal(err)
	}
	store, err := operations.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	operationID := "daemon-upload"
	if _, err := store.Enqueue(operations.Record{
		ID: operationID, AccountID: "account", Team: "team", Game: "game", Type: "front", Path: bundle,
		PlanID: prepared.PlanID, Desired: operations.DesiredRunning,
		Status: operations.StatusQueued, TotalActions: prepared.Plan.TotalActions(),
		TotalBytes: prepared.Plan.TotalUploadBytes(),
	}); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, appService, "account", Options{ReviewInterval: time.Hour})
	}()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		record, getErr := store.Get(operationID)
		if getErr == nil && record.Status == operations.StatusCompleted {
			cancel()
			if err := <-done; err != nil {
				t.Fatalf("daemon exit: %v", err)
			}
			if record.ConfirmedBytes != int64(len("ready")) {
				t.Fatalf("confirmed bytes = %d", record.ConfirmedBytes)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	<-done
	record, _ := store.Get(operationID)
	t.Fatalf("upload did not complete: %#v", record)
}

func TestDaemonDoesNotClaimAnotherAccountsUpload(t *testing.T) {
	root := t.TempDir()
	t.Setenv("STAKE_STATE_DIR", filepath.Join(root, "state", "uploads"))
	store, err := operations.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Enqueue(operations.Record{
		ID: "other-account", AccountID: "account-b", Team: "team", Game: "game",
		Type: "front", Path: filepath.Join(root, "bundle"), PlanID: "plan",
		Desired: operations.DesiredRunning, Status: operations.StatusQueued,
	}); err != nil {
		t.Fatal(err)
	}
	if record, ok := nextQueuedOperation("account-a"); ok {
		t.Fatalf("claimed another account's upload: %#v", record)
	}
	record, err := store.Get("other-account")
	if err != nil {
		t.Fatal(err)
	}
	if record.PID != 0 || record.Status != operations.StatusQueued {
		t.Fatalf("other account's upload was mutated: %#v", record)
	}
}
