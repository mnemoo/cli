package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mnemoo/cli/internal/operations"
	"github.com/mnemoo/cli/internal/stake"
	"github.com/mnemoo/cli/internal/upload"
)

type planningGateway struct {
	upload.Client
	scratch []stake.ScratchObject
}

type wrappedUploadClient struct {
	upload.Client
}

type overviewGateway struct {
	planningGateway
}

func (*overviewGateway) ListTeams(context.Context) ([]stake.Team, error) {
	return []stake.Team{{Name: "Team", Slug: "team"}}, nil
}

func (*overviewGateway) GetTeamBalance(context.Context, string) (*stake.TeamBalance, error) {
	return &stake.TeamBalance{Position: 999_000_000, Carry: 3_000_000}, nil
}

func (*overviewGateway) ListTeamGames(context.Context, string) ([]stake.Game, error) {
	return []stake.Game{{Slug: "one", OnlinePlayers: 12}, {Slug: "two", OnlinePlayers: 30}}, nil
}

func (*overviewGateway) GetTeamStats(context.Context, string, time.Time, time.Time) ([]stake.TeamOperatorStat, error) {
	return []stake.TeamOperatorStat{
		{Profit: 100_000_000, ExpectedProfit: 80_000_000, Operator: "stake"},
		{Profit: 20_000_000, ExpectedProfit: 40_000_000, Operator: "stake_us"},
	}, nil
}

func (*overviewGateway) GetPaymentMethod(context.Context, string) (*stake.PaymentMethod, error) {
	return &stake.PaymentMethod{Current: "turnover", Next: "profit"}, nil
}

func (g *planningGateway) ListScratch(context.Context, string, string, string) ([]stake.ScratchObject, error) {
	return g.scratch, nil
}

func (*planningGateway) GetCurrentUser(context.Context) (*stake.User, error) {
	return nil, errors.New("not implemented")
}

func (*planningGateway) ListTeams(context.Context) ([]stake.Team, error) {
	return nil, errors.New("not implemented")
}

func (*planningGateway) ListTeamGames(context.Context, string) ([]stake.Game, error) {
	return nil, errors.New("not implemented")
}

func (*planningGateway) GetGameDetail(context.Context, string, string) (*stake.GameDetail, error) {
	return nil, errors.New("not implemented")
}

func (*planningGateway) GetGameVersions(context.Context, string, string) ([]stake.GameVersion, error) {
	return nil, errors.New("not implemented")
}

func (*planningGateway) GetTeamBalance(context.Context, string) (*stake.TeamBalance, error) {
	return nil, errors.New("not implemented")
}

func (*planningGateway) GetTeamStats(context.Context, string, time.Time, time.Time) ([]stake.TeamOperatorStat, error) {
	return nil, errors.New("not implemented")
}

func (*planningGateway) GetPaymentMethod(context.Context, string) (*stake.PaymentMethod, error) {
	return nil, errors.New("not implemented")
}

func (*planningGateway) GetGameStats(context.Context, string, string) (*stake.GameStats, error) {
	return nil, errors.New("not implemented")
}

func (*planningGateway) GetGameStatsRange(context.Context, string, string, time.Time, time.Time) (*stake.GameStats, error) {
	return nil, errors.New("not implemented")
}

func (*planningGateway) GetGameGraph(context.Context, string, string, time.Time, time.Time) (*stake.StatsGraph, error) {
	return nil, errors.New("not implemented")
}

func (*planningGateway) Publish(context.Context, string, string, string) (*stake.PublishResult, error) {
	return nil, errors.New("not implemented")
}

func TestWithUploadTransportPreservesGatewayAndDirectCanRestoreIt(t *testing.T) {
	gateway := &planningGateway{}
	base := New(gateway)
	wrapped := base.WithUploadTransport(func(client upload.Client) upload.Client {
		if client != gateway {
			t.Fatalf("wrapper received %T, want original gateway", client)
		}
		return &wrappedUploadClient{Client: client}
	})
	if wrapped.gateway != gateway {
		t.Fatal("upload transport replacement changed the Stake gateway")
	}
	if _, ok := wrapped.uploadClient.(*wrappedUploadClient); !ok {
		t.Fatalf("upload transport was not wrapped: %T", wrapped.uploadClient)
	}
	direct := wrapped.Direct()
	if direct.gateway != gateway || direct.uploadClient != gateway {
		t.Fatal("direct service did not restore the Stake gateway transport")
	}
}

func TestListTeamOverviewsSeparatesPositionFromEarningModels(t *testing.T) {
	overviews, err := New(&overviewGateway{}).ListTeamOverviews(context.Background())
	if err != nil || len(overviews) != 1 {
		t.Fatalf("ListTeamOverviews() = %#v, %v", overviews, err)
	}
	overview := overviews[0]
	if overview.Balance == nil || overview.Balance.Position != 999_000_000 {
		t.Fatalf("balance position not preserved: %#v", overview.Balance)
	}
	if overview.Earnings == nil || overview.Earnings.ProfitShare != 12_000_000 || overview.Earnings.ExpectedProfit != 9_000_000 {
		t.Fatalf("wrong earning totals: %#v", overview.Earnings)
	}
	if overview.PaymentMethod == nil || overview.PaymentMethod.Current != "turnover" {
		t.Fatalf("wrong payment method: %#v", overview.PaymentMethod)
	}
	if overview.OnlinePlayers == nil || *overview.OnlinePlayers != 42 {
		t.Fatalf("wrong provider online total: %#v", overview.OnlinePlayers)
	}
}

func TestFingerprintPlanIsCanonical(t *testing.T) {
	target := UploadTarget{Team: "team", Game: "game", Type: "math", Path: "/bundle"}
	entryA := upload.PlanEntry{Action: upload.ActionUpload, RemoteKey: "/math/a", Size: 10}
	entryB := upload.PlanEntry{Action: upload.ActionDelete, RemoteKey: "/math/b", Size: 20}

	first := fingerprintPlan(target, &upload.UploadPlan{
		ToUpload: []upload.PlanEntry{entryA},
		ToDelete: []upload.PlanEntry{entryB},
	})
	second := fingerprintPlan(target, &upload.UploadPlan{
		ToDelete: []upload.PlanEntry{entryB},
		ToUpload: []upload.PlanEntry{entryA},
	})

	if first != second {
		t.Fatalf("equivalent plans produced different IDs: %s != %s", first, second)
	}
}

func TestFingerprintPlanChangesWithCopySource(t *testing.T) {
	target := UploadTarget{Team: "team", Game: "game", Type: "front", Path: "/bundle"}
	base := upload.PlanEntry{
		Action: upload.ActionCopy, RemoteKey: "/front/index.html",
		SourceKey: "/front/old.html", RemoteETag: "etag", Size: 100,
	}
	first := fingerprintPlan(target, &upload.UploadPlan{ToCopy: []upload.PlanEntry{base}})
	base.SourceKey = "/front/other.html"
	second := fingerprintPlan(target, &upload.UploadPlan{ToCopy: []upload.PlanEntry{base}})

	if first == second {
		t.Fatal("changing a server-side copy source did not change the plan ID")
	}
}

func TestFingerprintPlanChangesWithComplianceContext(t *testing.T) {
	firstTarget := UploadTarget{
		Team: "team", Game: "game", Type: "math", Path: "/bundle",
		Compliance: &upload.ComplianceOptions{BaseMode: "base"},
	}
	secondTarget := firstTarget
	secondTarget.Compliance = &upload.ComplianceOptions{BaseMode: "main"}
	plan := &upload.UploadPlan{}

	first := fingerprintPlan(firstTarget, plan)
	second := fingerprintPlan(secondTarget, plan)
	if first == second {
		t.Fatal("changing compliance context did not change the plan ID")
	}
}

func TestValidateTarget(t *testing.T) {
	tests := []struct {
		name   string
		target UploadTarget
		valid  bool
	}{
		{name: "math", target: UploadTarget{Team: "t", Game: "g", Type: "math", Path: "/x"}, valid: true},
		{name: "front", target: UploadTarget{Team: "t", Game: "g", Type: "front", Path: "/x"}, valid: true},
		{name: "missing team", target: UploadTarget{Game: "g", Type: "math", Path: "/x"}},
		{name: "bad type", target: UploadTarget{Team: "t", Game: "g", Type: "other", Path: "/x"}},
		{name: "missing path", target: UploadTarget{Team: "t", Game: "g", Type: "math"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateTarget(test.target, true)
			if test.valid && err != nil {
				t.Fatalf("expected valid target: %v", err)
			}
			if !test.valid && err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestExecuteExpectedRejectsChangedLocalFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "index.html")
	if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
		t.Fatalf("write first fixture: %v", err)
	}
	appService := New(&planningGateway{})
	target := UploadTarget{Team: "team", Game: "game", Type: "front", Path: dir}
	prepared, err := appService.PrepareUpload(context.Background(), target)
	if err != nil {
		t.Fatalf("prepare upload: %v", err)
	}

	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatalf("change fixture: %v", err)
	}
	changed, err := appService.ExecuteExpected(context.Background(), target, prepared.PlanID, false, false, nil)
	if !errors.Is(err, ErrPlanChanged) {
		t.Fatalf("got %v, want ErrPlanChanged", err)
	}
	if changed == nil || changed.PlanID == prepared.PlanID {
		t.Fatal("changed upload did not return a new plan ID")
	}
}

func TestPrepareExpectedUsesAutomaticTwoAndThreeStarAnalysis(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"index.json":   `{"modes":[{"name":"base","cost":1,"events":"events.jsonl","weights":"weights.csv"}]}`,
		"events.jsonl": "{}\n",
		"weights.csv":  "0,900,0\n1,100,967\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}

	appService := New(&planningGateway{})
	compliance := upload.ComplianceOptions{}
	target := UploadTarget{Team: "team", Game: "game", Type: "math", Path: dir, Compliance: &compliance}
	prepared, err := appService.PrepareUpload(context.Background(), target)
	if err != nil {
		t.Fatalf("prepare upload: %v", err)
	}
	if prepared.Compliance == nil || !prepared.Compliance.Complete || len(prepared.Compliance.TierResults) != 2 {
		t.Fatalf("expected complete 2-star and 3-star analysis, got %#v", prepared.Compliance)
	}
	if prepared.Compliance.TierResults[0].MaxCompliantBetLevel != 200 ||
		prepared.Compliance.TierResults[1].MaxCompliantBetLevel != 1000 {
		t.Fatalf("unexpected maximum compliant bet levels: %#v", prepared.Compliance.TierResults)
	}
	if _, err := appService.PrepareExpected(context.Background(), target, prepared.PlanID, false, false); err != nil {
		t.Fatalf("automatic tier analysis should allow upload preparation: %v", err)
	}
}

func TestGenericPublishRejectsMathWithoutExplicitCheckedOrUncheckedPath(t *testing.T) {
	appService := New(&planningGateway{})
	if _, err := appService.Publish(context.Background(), "team", "game", "math"); err == nil || !strings.Contains(err.Error(), "PublishMath") {
		t.Fatalf("expected generic math publish rejection, got %v", err)
	}
}

func TestExecutePlanPublishesCrossProcessOperationState(t *testing.T) {
	stateDir := t.TempDir()
	t.Setenv("STAKE_STATE_DIR", stateDir)
	appService := New(&planningGateway{})
	plan := &upload.UploadPlan{Team: "team", Game: "game", UploadType: "front"}

	err := appService.ExecutePlan(context.Background(), plan, ExecuteOptions{
		Source: "test", Path: ".", PlanID: "plan-id", OperationID: "operation-id",
	})
	if err != nil {
		t.Fatalf("execute empty plan: %v", err)
	}

	store, err := operations.OpenDefault()
	if err != nil {
		t.Fatalf("open operation store: %v", err)
	}
	record, err := store.Get("operation-id")
	if err != nil {
		t.Fatalf("get operation: %v", err)
	}
	if record.Status != operations.StatusCompleted || record.Source != "test" || record.PlanID != "plan-id" || record.FinishedAt == nil {
		t.Fatalf("unexpected operation: %#v", record)
	}
}
