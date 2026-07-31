// Package service contains stakecli's application use cases. CLI, TUI and MCP
// are adapters over this package and must not reimplement business workflows.
package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mnemoo/cli/internal/operations"
	"github.com/mnemoo/cli/internal/stake"
	"github.com/mnemoo/cli/internal/upload"
)

var ErrPlanChanged = errors.New("upload plan changed; call plan_upload again")

// Gateway is the outbound Stake Engine boundary used by application services.
// It intentionally contains behavior, while HTTP request mechanics stay in api.
type Gateway interface {
	upload.Client
	GetCurrentUser(context.Context) (*stake.User, error)
	ListTeams(context.Context) ([]stake.Team, error)
	ListTeamGames(context.Context, string) ([]stake.Game, error)
	GetGameDetail(context.Context, string, string) (*stake.GameDetail, error)
	GetGameVersions(context.Context, string, string) ([]stake.GameVersion, error)
	GetTeamBalance(context.Context, string) (*stake.TeamBalance, error)
	GetTeamStats(context.Context, string, time.Time, time.Time) ([]stake.TeamOperatorStat, error)
	GetPaymentMethod(context.Context, string) (*stake.PaymentMethod, error)
	GetGameStats(context.Context, string, string) (*stake.GameStats, error)
	GetGameStatsRange(context.Context, string, string, time.Time, time.Time) (*stake.GameStats, error)
	GetGameGraph(context.Context, string, string, time.Time, time.Time) (*stake.StatsGraph, error)
	Publish(context.Context, string, string, string) (*stake.PublishResult, error)
}

type Service struct {
	gateway      Gateway
	uploadClient upload.Client
}

func New(gateway Gateway) *Service {
	return &Service{gateway: gateway, uploadClient: gateway}
}

// NewWithUploadClient keeps all Stake API and publish behavior on gateway,
// while allowing the presigned object transport to be replaced. The
// accelerator uses this seam and therefore never receives the Stake SID.
func NewWithUploadClient(gateway Gateway, uploadClient upload.Client) *Service {
	if uploadClient == nil {
		uploadClient = gateway
	}
	return &Service{gateway: gateway, uploadClient: uploadClient}
}

// Direct returns the same application service with the Stake gateway restored
// as the object upload transport.
func (s *Service) Direct() *Service {
	if s == nil {
		return nil
	}
	return New(s.gateway)
}

// WithUploadTransport derives a service that keeps the authenticated Stake
// gateway for planning and publishing while wrapping only object uploads.
func (s *Service) WithUploadTransport(wrap func(upload.Client) upload.Client) *Service {
	if s == nil {
		return nil
	}
	uploadClient := upload.Client(s.gateway)
	if wrap != nil {
		if wrapped := wrap(uploadClient); wrapped != nil {
			uploadClient = wrapped
		}
	}
	return NewWithUploadClient(s.gateway, uploadClient)
}

type TeamOverview struct {
	Team          stake.Team           `json:"team"`
	Balance       *stake.TeamBalance   `json:"balance,omitempty"`
	Earnings      *stake.TeamEarnings  `json:"earnings,omitempty"`
	PaymentMethod *stake.PaymentMethod `json:"paymentMethod,omitempty"`
	OnlinePlayers *int                 `json:"onlinePlayers,omitempty"`
}

type GameSnapshot struct {
	Detail   *stake.GameDetail   `json:"detail"`
	Versions []stake.GameVersion `json:"versions"`
	Stats    *stake.GameStats    `json:"stats,omitempty"`
}

type UploadTarget struct {
	Team       string                    `json:"team"`
	Game       string                    `json:"game"`
	Type       string                    `json:"type"`
	Path       string                    `json:"path"`
	Compliance *upload.ComplianceOptions `json:"compliance,omitempty"`
}

type PreparedUpload struct {
	PlanID     string                   `json:"planId"`
	Target     UploadTarget             `json:"target"`
	Safety     []upload.SafetyWarning   `json:"safety"`
	Compliance *upload.ComplianceResult `json:"compliance,omitempty"`
	Plan       *upload.UploadPlan       `json:"-"`
}

type ExecuteOptions struct {
	Gate        *upload.PauseGate
	ByteCounter *atomic.Int64
	Tracker     *upload.TransferTracker
	Progress    func(upload.ProgressEvent)
	Source      string
	Path        string
	PlanID      string
	OperationID string
}

func (s *Service) CurrentUser(ctx context.Context) (*stake.User, error) {
	return s.gateway.GetCurrentUser(ctx)
}

func (s *Service) ListTeams(ctx context.Context) ([]stake.Team, error) {
	return s.gateway.ListTeams(ctx)
}

func (s *Service) ListTeamOverviews(ctx context.Context) ([]TeamOverview, error) {
	teams, err := s.ListTeams(ctx)
	if err != nil {
		return nil, err
	}

	overviews := make([]TeamOverview, len(teams))
	now := time.Now().UTC()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	var wg sync.WaitGroup
	for i := range teams {
		overviews[i].Team = teams[i]
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			team := teams[index].Slug
			balance, balanceErr := s.GetTeamBalance(ctx, team)
			if balanceErr == nil {
				overviews[index].Balance = balance
			}
			stats, statsErr := s.GetTeamStats(ctx, team, monthStart, now)
			if statsErr == nil {
				earnings := &stake.TeamEarnings{}
				for _, row := range stats {
					earnings.ProfitShare += row.Profit * 0.10
					earnings.ExpectedProfit += row.ExpectedProfit * 0.075
				}
				overviews[index].Earnings = earnings
			}
			method, methodErr := s.GetPaymentMethod(ctx, team)
			if methodErr == nil {
				overviews[index].PaymentMethod = method
			}
			games, gamesErr := s.ListGames(ctx, team)
			if gamesErr == nil {
				online := 0
				for _, game := range games {
					online += game.OnlinePlayers
				}
				overviews[index].OnlinePlayers = &online
			}
		}(i)
	}
	wg.Wait()
	return overviews, nil
}

func (s *Service) ListGames(ctx context.Context, team string) ([]stake.Game, error) {
	if strings.TrimSpace(team) == "" {
		return nil, errors.New("team is required")
	}
	return s.gateway.ListTeamGames(ctx, team)
}

func (s *Service) GetGame(ctx context.Context, team, game string) (*stake.GameDetail, error) {
	if err := validateGameRef(team, game); err != nil {
		return nil, err
	}
	return s.gateway.GetGameDetail(ctx, team, game)
}

func (s *Service) ListGameVersions(ctx context.Context, team, game string) ([]stake.GameVersion, error) {
	if err := validateGameRef(team, game); err != nil {
		return nil, err
	}
	versions, err := s.gateway.GetGameVersions(ctx, team, game)
	if err != nil {
		return nil, err
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].Created > versions[j].Created })
	return versions, nil
}

func (s *Service) GetGameStats(ctx context.Context, team, game string) (*stake.GameStats, error) {
	if err := validateGameRef(team, game); err != nil {
		return nil, err
	}
	return s.gateway.GetGameStats(ctx, team, game)
}

func (s *Service) GetGameStatsRange(ctx context.Context, team, game string, start, end time.Time) (*stake.GameStats, error) {
	if err := validateGameRef(team, game); err != nil {
		return nil, err
	}
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return nil, errors.New("stats start must be before end")
	}
	return s.gateway.GetGameStatsRange(ctx, team, game, start, end)
}

func (s *Service) GetGameGraph(ctx context.Context, team, game string, start, end time.Time) (*stake.StatsGraph, error) {
	if err := validateGameRef(team, game); err != nil {
		return nil, err
	}
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return nil, errors.New("graph start must be before end")
	}
	return s.gateway.GetGameGraph(ctx, team, game, start, end)
}

func (s *Service) GetTeamBalance(ctx context.Context, team string) (*stake.TeamBalance, error) {
	if strings.TrimSpace(team) == "" {
		return nil, errors.New("team is required")
	}
	return s.gateway.GetTeamBalance(ctx, team)
}

func (s *Service) GetTeamStats(ctx context.Context, team string, start, end time.Time) ([]stake.TeamOperatorStat, error) {
	if strings.TrimSpace(team) == "" {
		return nil, errors.New("team is required")
	}
	if start.IsZero() || end.IsZero() || !start.Before(end) {
		return nil, errors.New("team stats start must be before end")
	}
	return s.gateway.GetTeamStats(ctx, team, start, end)
}

func (s *Service) GetPaymentMethod(ctx context.Context, team string) (*stake.PaymentMethod, error) {
	if strings.TrimSpace(team) == "" {
		return nil, errors.New("team is required")
	}
	return s.gateway.GetPaymentMethod(ctx, team)
}

func (s *Service) GetGameSnapshot(ctx context.Context, team, game string) (*GameSnapshot, error) {
	if err := validateGameRef(team, game); err != nil {
		return nil, err
	}

	var (
		detail                 *stake.GameDetail
		versions               []stake.GameVersion
		stats                  *stake.GameStats
		detailErr, versionsErr error
		wg                     sync.WaitGroup
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		detail, detailErr = s.GetGame(ctx, team, game)
	}()
	go func() {
		defer wg.Done()
		versions, versionsErr = s.ListGameVersions(ctx, team, game)
	}()
	go func() {
		defer wg.Done()
		stats, _ = s.GetGameStats(ctx, team, game)
	}()
	wg.Wait()
	if detailErr != nil {
		return nil, detailErr
	}
	if versionsErr != nil {
		return nil, versionsErr
	}
	return &GameSnapshot{Detail: detail, Versions: versions, Stats: stats}, nil
}

func (s *Service) ListScratch(ctx context.Context, team, game, uploadType string) ([]stake.ScratchObject, error) {
	if err := validateTarget(UploadTarget{Team: team, Game: game, Type: uploadType, Path: "."}, false); err != nil {
		return nil, err
	}
	return s.gateway.ListScratch(ctx, team, game, uploadType)
}

func (s *Service) CheckMath(path string) upload.ComplianceResult {
	return upload.RunMathCompliance(path)
}

func (s *Service) CheckMathWithOptions(path string, options upload.ComplianceOptions) upload.ComplianceResult {
	return upload.RunMathComplianceWithOptions(path, options)
}

func (s *Service) CompliancePolicy() upload.CompliancePolicy {
	return upload.CurrentCompliancePolicy()
}

func (s *Service) ValidateUploadPath(path string) []upload.SafetyWarning {
	return upload.ValidatePath(path)
}

func (s *Service) PrepareUpload(ctx context.Context, target UploadTarget) (*PreparedUpload, error) {
	if err := validateTarget(target, true); err != nil {
		return nil, err
	}

	safety := upload.ValidatePath(target.Path)
	if upload.HasErrors(safety) {
		return nil, fmt.Errorf("upload path safety validation failed: %s", warningMessages(safety, "error"))
	}

	var compliance *upload.ComplianceResult
	if target.Type == "math" && target.Compliance != nil {
		result := upload.RunMathComplianceWithOptions(target.Path, *target.Compliance)
		compliance = &result
	}

	uploader := upload.NewUploader(s.uploadClient)
	plan, err := uploader.Plan(ctx, target.Team, target.Game, target.Type, target.Path)
	if err != nil {
		return nil, fmt.Errorf("planning upload: %w", err)
	}

	return &PreparedUpload{
		PlanID:     fingerprintPlan(target, plan),
		Target:     target,
		Safety:     safety,
		Compliance: compliance,
		Plan:       plan,
	}, nil
}

func (s *Service) ExecutePrepared(ctx context.Context, prepared *PreparedUpload, progress func(upload.ProgressEvent)) error {
	return s.ExecutePreparedWithOptions(ctx, prepared, ExecuteOptions{Progress: progress})
}

func (s *Service) ExecutePreparedWithOptions(ctx context.Context, prepared *PreparedUpload, options ExecuteOptions) error {
	if prepared == nil || prepared.Plan == nil {
		return errors.New("prepared upload is required")
	}
	if options.Path == "" {
		options.Path = prepared.Target.Path
	}
	if options.PlanID == "" {
		options.PlanID = prepared.PlanID
	}
	return s.ExecutePlan(ctx, prepared.Plan, options)
}

func (s *Service) ExecutePlan(ctx context.Context, plan *upload.UploadPlan, options ExecuteOptions) error {
	if plan == nil {
		return errors.New("upload plan is required")
	}
	uploader := upload.NewUploader(s.uploadClient)
	tracker := options.Tracker
	if tracker == nil {
		tracker = upload.NewTransferTracker(plan.ToUpload)
	}
	uploader.Tracker = tracker
	if options.ByteCounter != nil {
		uploader.ByteCounter = options.ByteCounter
	}

	var completedActions atomic.Int64
	progress := options.Progress
	trackedProgress := func(event upload.ProgressEvent) {
		for {
			current := completedActions.Load()
			if int64(event.Current) <= current || completedActions.CompareAndSwap(current, int64(event.Current)) {
				break
			}
		}
		if progress != nil {
			progress(event)
		}
	}

	handle := startOperation(plan, options)
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	if handle != nil {
		_ = handle.Update(tracker.Snapshot(), 0)
		go func() {
			defer close(heartbeatDone)
			ticker := time.NewTicker(500 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					_ = handle.Update(tracker.Snapshot(), int(completedActions.Load()))
				case <-stopHeartbeat:
					return
				}
			}
		}()
	}

	err := uploader.Execute(ctx, plan, options.Gate, trackedProgress)
	if handle != nil {
		close(stopHeartbeat)
		<-heartbeatDone
		status := operations.StatusCompleted
		if err != nil {
			status = operations.StatusFailed
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				status = operations.StatusCancelled
			}
		}
		_ = handle.Finish(status, err, tracker.Snapshot(), int(completedActions.Load()))
	}
	return err
}

func (s *Service) ExecuteExpected(ctx context.Context, target UploadTarget, expectedPlanID string, allowWarnings, allowComplianceFailures bool, progress func(upload.ProgressEvent)) (*PreparedUpload, error) {
	prepared, err := s.PrepareExpected(ctx, target, expectedPlanID, allowWarnings, allowComplianceFailures)
	if err != nil {
		return prepared, err
	}
	if err := s.ExecutePrepared(ctx, prepared, progress); err != nil {
		return prepared, err
	}
	return prepared, nil
}

func (s *Service) PrepareExpected(ctx context.Context, target UploadTarget, expectedPlanID string, allowWarnings, allowComplianceFailures bool) (*PreparedUpload, error) {
	if strings.TrimSpace(expectedPlanID) == "" {
		return nil, errors.New("plan_id is required; call plan_upload first")
	}
	prepared, err := s.PrepareUpload(ctx, target)
	if err != nil {
		return nil, err
	}
	if prepared.PlanID != expectedPlanID {
		return prepared, ErrPlanChanged
	}
	if len(prepared.Safety) > 0 && !allowWarnings {
		return prepared, errors.New("safety warnings require allow_warnings=true")
	}
	if prepared.Compliance != nil && !allowComplianceFailures {
		if err := prepared.Compliance.ValidateForPublish(); err != nil {
			return prepared, fmt.Errorf("math compliance requires allow_compliance_failures=true: %w", err)
		}
	}
	return prepared, nil
}

func startOperation(plan *upload.UploadPlan, options ExecuteOptions) *operations.Handle {
	store, err := operations.OpenDefault()
	if err != nil {
		return nil
	}
	if options.OperationID != "" {
		if handle, attachErr := store.Attach(options.OperationID); attachErr == nil {
			return handle
		}
	}
	path := options.Path
	if path != "" {
		if absolute, absErr := filepath.Abs(path); absErr == nil {
			path = absolute
		}
	}
	handle, err := store.Start(operations.Record{
		ID:           options.OperationID,
		Source:       options.Source,
		Team:         plan.Team,
		Game:         plan.Game,
		Type:         plan.UploadType,
		Path:         path,
		PlanID:       options.PlanID,
		Status:       operations.StatusQueued,
		TotalActions: plan.TotalActions(),
		TotalBytes:   plan.TotalUploadBytes(),
	})
	if err != nil {
		return nil
	}
	return handle
}

func (s *Service) Publish(ctx context.Context, team, game, kind string) (*stake.PublishResult, error) {
	if err := validateGameRef(team, game); err != nil {
		return nil, err
	}
	if kind != "math" && kind != "front" {
		return nil, fmt.Errorf("type must be math or front, got %q", kind)
	}
	if kind == "math" {
		return nil, errors.New("math publish requires PublishMath with a compliant local bundle")
	}
	return s.gateway.Publish(ctx, team, game, kind)
}

func (s *Service) PublishMath(ctx context.Context, target UploadTarget, allowComplianceFailures bool) (*stake.PublishResult, error) {
	if target.Type != "math" {
		return nil, fmt.Errorf("PublishMath requires type math, got %q", target.Type)
	}
	if target.Compliance == nil && !allowComplianceFailures {
		return nil, errors.New("checked math publish requires compliance context")
	}
	prepared, err := s.PrepareUpload(ctx, target)
	if err != nil {
		return nil, err
	}
	if !allowComplianceFailures {
		if prepared.Compliance == nil {
			return nil, errors.New("math publish did not produce a compliance result")
		}
		if err := prepared.Compliance.ValidateForPublish(); err != nil {
			return nil, fmt.Errorf("math publish blocked: %w", err)
		}
	}
	if prepared.Plan.TotalActions() != 0 {
		return nil, fmt.Errorf("math publish blocked: remote scratch differs from the compliant local bundle (%d pending action(s)); execute upload first", prepared.Plan.TotalActions())
	}
	return s.gateway.Publish(ctx, target.Team, target.Game, target.Type)
}

func (s *Service) PublishMathUnchecked(ctx context.Context, team, game string) (*stake.PublishResult, error) {
	if err := validateGameRef(team, game); err != nil {
		return nil, err
	}
	return s.gateway.Publish(ctx, team, game, "math")
}

func validateGameRef(team, game string) error {
	if strings.TrimSpace(team) == "" || strings.TrimSpace(game) == "" {
		return errors.New("team and game are required")
	}
	return nil
}

func validateTarget(target UploadTarget, requirePath bool) error {
	if err := validateGameRef(target.Team, target.Game); err != nil {
		return err
	}
	if target.Type != "math" && target.Type != "front" {
		return fmt.Errorf("type must be math or front, got %q", target.Type)
	}
	if requirePath && strings.TrimSpace(target.Path) == "" {
		return errors.New("path is required")
	}
	return nil
}

func warningMessages(warnings []upload.SafetyWarning, level string) string {
	var messages []string
	for _, warning := range warnings {
		if warning.Level == level {
			messages = append(messages, warning.Message)
		}
	}
	return strings.Join(messages, "; ")
}

func fingerprintPlan(target UploadTarget, plan *upload.UploadPlan) string {
	lines := []string{target.Team, target.Game, target.Type, target.Path}
	if target.Compliance != nil {
		if encoded, err := json.Marshal(target.Compliance); err == nil {
			lines = append(lines, "compliance="+string(encoded))
		}
	}
	entryStart := len(lines)
	appendEntries := func(entries []upload.PlanEntry) {
		for _, entry := range entries {
			localETag := ""
			if entry.LocalFile != nil && entry.LocalFile.ETagResult != nil {
				localETag = entry.LocalFile.ETagResult.ETag
			}
			lines = append(lines, fmt.Sprintf("%s\x00%s\x00%s\x00%s\x00%s\x00%d", entry.Action, entry.RemoteKey, entry.SourceKey, entry.RemoteETag, localETag, entry.Size))
		}
	}
	appendEntries(plan.ToUpload)
	appendEntries(plan.ToCopy)
	appendEntries(plan.ToDelete)
	appendEntries(plan.Unchanged)
	sort.Strings(lines[entryStart:])
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}
