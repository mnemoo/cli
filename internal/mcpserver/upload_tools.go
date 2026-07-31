package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mnemoo/cli/internal/daemon"
	"github.com/mnemoo/cli/internal/operations"
	"github.com/mnemoo/cli/internal/service"
	"github.com/mnemoo/cli/internal/stake"
	"github.com/mnemoo/cli/internal/upload"
)

type pathInput struct {
	Path string `json:"path" jsonschema:"absolute or working-directory-relative path to a local asset directory"`
}

type validatePathOutput struct {
	Valid    bool                   `json:"valid"`
	Warnings []upload.SafetyWarning `json:"warnings"`
}

type complianceOutput struct {
	Compliance upload.ComplianceResult `json:"compliance"`
}

type compliancePolicyOutput struct {
	SchemaVersion string                  `json:"schema_version"`
	Policy        upload.CompliancePolicy `json:"policy"`
}

type complianceInput struct {
	Path       string                    `json:"path" jsonschema:"absolute or working-directory-relative path to a local math directory"`
	Compliance *upload.ComplianceOptions `json:"compliance,omitempty" jsonschema:"optional display base-mode hint"`
}

type uploadTargetInput struct {
	Team       string                    `json:"team" jsonschema:"Stake Engine team slug"`
	Game       string                    `json:"game" jsonschema:"Stake Engine game slug"`
	Type       string                    `json:"type" jsonschema:"asset type: math or front"`
	Path       string                    `json:"path" jsonschema:"local asset directory"`
	Compliance *upload.ComplianceOptions `json:"compliance,omitempty" jsonschema:"optional compliance context for math assets"`
}

type executeUploadInput struct {
	Team                    string                    `json:"team" jsonschema:"Stake Engine team slug"`
	Game                    string                    `json:"game" jsonschema:"Stake Engine game slug"`
	Type                    string                    `json:"type" jsonschema:"asset type: math or front"`
	Path                    string                    `json:"path" jsonschema:"local asset directory used by plan_upload"`
	PlanID                  string                    `json:"plan_id" jsonschema:"exact plan_id returned by the latest plan_upload call"`
	AllowWarnings           bool                      `json:"allow_warnings,omitempty" jsonschema:"explicitly accept non-blocking path safety warnings"`
	AllowComplianceFailures bool                      `json:"allow_compliance_failures,omitempty" jsonschema:"explicitly accept failed math compliance checks"`
	Compliance              *upload.ComplianceOptions `json:"compliance,omitempty" jsonschema:"same compliance context used by plan_upload"`
}

type planEntryOutput struct {
	Path       string `json:"path"`
	SourcePath string `json:"source_path,omitempty"`
	Size       int64  `json:"size"`
}

type uploadPlanOutput struct {
	PlanID          string                   `json:"plan_id"`
	Target          service.UploadTarget     `json:"target"`
	Safety          []upload.SafetyWarning   `json:"safety"`
	Compliance      *upload.ComplianceResult `json:"compliance,omitempty"`
	Upload          []planEntryOutput        `json:"upload"`
	Copy            []planEntryOutput        `json:"copy"`
	Delete          []planEntryOutput        `json:"delete"`
	Unchanged       []planEntryOutput        `json:"unchanged"`
	TotalActions    int                      `json:"total_actions"`
	TotalUploadSize int64                    `json:"total_upload_size"`
}

type executeUploadOutput struct {
	Success       bool   `json:"success"`
	PlanID        string `json:"plan_id"`
	Actions       int    `json:"actions"`
	UploadedBytes int64  `json:"uploaded_bytes"`
}

type startUploadOutput struct {
	Accepted      bool   `json:"accepted"`
	OperationID   string `json:"operation_id"`
	PlanID        string `json:"plan_id"`
	DaemonRunning bool   `json:"daemon_running"`
	Warning       string `json:"warning,omitempty"`
}

type listUploadProcessesInput struct {
	IncludeFinished bool `json:"include_finished,omitempty" jsonschema:"include completed, failed, and cancelled uploads retained for recent inspection"`
}

type listUploadProcessesOutput struct {
	Processes []operations.Record `json:"processes"`
}

type getUploadProcessInput struct {
	OperationID string `json:"operation_id" jsonschema:"operation_id returned by start_upload or list_upload_processes"`
}

type getUploadProcessOutput struct {
	Process operations.Record `json:"process"`
}

type operationInput struct {
	OperationID string `json:"operation_id" jsonschema:"upload operation ID"`
}

type confirmedOperationInput struct {
	OperationID string `json:"operation_id" jsonschema:"upload operation ID"`
	Confirm     bool   `json:"confirm" jsonschema:"must be true for cancellation or retry"`
}

type operationOutput struct {
	Process operations.Record `json:"process"`
}

type publishInput struct {
	Team            string                    `json:"team" jsonschema:"Stake Engine team slug"`
	Game            string                    `json:"game" jsonschema:"Stake Engine game slug"`
	Type            string                    `json:"type" jsonschema:"asset type: math or front"`
	Path            string                    `json:"path,omitempty" jsonschema:"local math bundle path required for math publish compliance and scratch verification"`
	Compliance      *upload.ComplianceOptions `json:"compliance,omitempty" jsonschema:"optional display base-mode hint for the advisory math report"`
	CheckCompliance bool                      `json:"check_compliance,omitempty" jsonschema:"when true, run advisory math compliance and verify scratch matches the local bundle; false skips math compliance"`
	UpdateApproval  bool                      `json:"update_approval_version,omitempty" jsonschema:"when true, update an open approval to the version returned by publish; a closed approval is a warning-only no-op"`
	Confirm         bool                      `json:"confirm" jsonschema:"must be true to publish a new game asset version"`
}

type publishOutput struct {
	Published       bool                         `json:"published"`
	Result          *stake.PublishResult         `json:"result,omitempty"`
	ApprovalVersion *stake.ApprovalVersionUpdate `json:"approval_version,omitempty"`
	ApprovalWarning string                       `json:"approval_warning,omitempty"`
}

func (s *Server) registerUploadTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "get_compliance_policy", Title: "Get math compliance policy",
		Description: "Return the current hard limits and 2-star/3-star policy thresholds used by stakecli. Does not inspect files or contact Stake Engine.",
		Annotations: readOnlyAnnotations("Get math compliance policy", false),
	}, s.getCompliancePolicy)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "validate_upload_path", Title: "Validate upload path",
		Description: "Inspect a local directory for unsafe locations, secrets, and missing uploadable files. Does not contact Stake Engine.",
		Annotations: readOnlyAnnotations("Validate upload path", false),
	}, s.validateUploadPath)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "check_math_compliance", Title: "Check math compliance",
		Description: "Validate a local math bundle with the moptimizer report rules for both 2-star and 3-star. Bet level and currency are excluded from the verdict; the result includes each tier's maximum compliant bet level.",
		Annotations: readOnlyAnnotations("Check math compliance", false),
	}, s.checkMathCompliance)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "plan_upload", Title: "Plan Stake upload",
		Description: "Compare local assets with remote scratch and return exact uploads, server-side copies, deletions, unchanged files, and a plan_id. Never mutates remote state.",
		Annotations: readOnlyAnnotations("Plan Stake upload", true),
	}, s.planUpload)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "execute_upload", Title: "Execute Stake upload",
		Description: "Synchronize local assets to Stake scratch using a current plan_id. The operation may upload, copy, and delete remote scratch files.",
		Annotations: mutationAnnotations("Execute Stake upload", true, true),
	}, s.executeUpload)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "start_upload", Title: "Start Stake upload",
		Description: "Validate the current plan_id, start the upload asynchronously, and return an operation_id immediately. Progress remains visible across MCP and CLI sessions.",
		Annotations: mutationAnnotations("Start Stake upload", true, false),
	}, s.startUpload)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "list_upload_processes", Title: "List upload processes",
		Description: "List active uploads started by stakecli in any local CLI, TUI, or MCP session. Optionally includes recently finished uploads.",
		Annotations: readOnlyAnnotations("List upload processes", false),
	}, s.listUploadProcesses)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "get_upload_process", Title: "Get upload process",
		Description: "Return current byte, file, part, retry, status, and full error details for one local upload operation.",
		Annotations: readOnlyAnnotations("Get upload process", false),
	}, s.getUploadProcess)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "pause_upload", Title: "Pause upload",
		Description: "Pause a queued or running daemon-owned upload at the next safe transfer boundary.",
		Annotations: mutationAnnotations("Pause upload", false, true),
	}, s.pauseUpload)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "resume_upload", Title: "Resume upload",
		Description: "Resume a paused upload and ensure the independent StakeCLI daemon is running.",
		Annotations: mutationAnnotations("Resume upload", false, true),
	}, s.resumeUpload)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "cancel_upload", Title: "Cancel upload",
		Description: "Cancel a queued or running upload. Requires confirm=true.",
		Annotations: mutationAnnotations("Cancel upload", true, true),
	}, s.cancelUpload)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "retry_upload", Title: "Retry upload",
		Description: "Queue a new attempt for a failed or cancelled upload and revalidate its existing upload plan before mutation. Requires confirm=true.",
		Annotations: mutationAnnotations("Retry upload", true, false),
	}, s.retryUpload)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "publish", Title: "Publish Stake assets",
		Description: "Publish staged assets as a game version. Math compliance is skipped by default; check_compliance=true runs the advisory check without blocking and verifies scratch matches the local bundle. update_approval_version=true updates an open approval to the published version and reports a warning instead of failing when the approval is closed. Requires confirm=true.",
		Annotations: mutationAnnotations("Publish Stake assets", false, false),
	}, s.publish)
}

func (s *Server) getCompliancePolicy(_ context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, compliancePolicyOutput, error) {
	return nil, compliancePolicyOutput{SchemaVersion: upload.ComplianceSchemaVersion, Policy: s.service.CompliancePolicy()}, nil
}

func (s *Server) validateUploadPath(_ context.Context, _ *mcp.CallToolRequest, input pathInput) (*mcp.CallToolResult, validatePathOutput, error) {
	warnings := s.service.ValidateUploadPath(input.Path)
	return nil, validatePathOutput{Valid: !upload.HasErrors(warnings), Warnings: warnings}, nil
}

func (s *Server) checkMathCompliance(_ context.Context, _ *mcp.CallToolRequest, input complianceInput) (*mcp.CallToolResult, complianceOutput, error) {
	options := upload.ComplianceOptions{}
	if input.Compliance != nil {
		options = *input.Compliance
	}
	result := s.service.CheckMathWithOptions(input.Path, options)
	return nil, complianceOutput{Compliance: result}, nil
}

func (s *Server) planUpload(ctx context.Context, _ *mcp.CallToolRequest, input uploadTargetInput) (*mcp.CallToolResult, uploadPlanOutput, error) {
	if err := s.requireAuth(); err != nil {
		return nil, uploadPlanOutput{}, err
	}
	prepared, err := s.service.PrepareUpload(ctx, service.UploadTarget{
		Team: input.Team, Game: input.Game, Type: input.Type, Path: input.Path, Compliance: input.Compliance,
	})
	if err != nil {
		return nil, uploadPlanOutput{}, err
	}
	return nil, presentPlan(prepared), nil
}

func (s *Server) executeUpload(ctx context.Context, request *mcp.CallToolRequest, input executeUploadInput) (*mcp.CallToolResult, executeUploadOutput, error) {
	if err := s.requireAuth(); err != nil {
		return nil, executeUploadOutput{}, err
	}
	target := service.UploadTarget{Team: input.Team, Game: input.Game, Type: input.Type, Path: input.Path, Compliance: input.Compliance}
	progressToken := request.Params.GetProgressToken()
	var progressMu sync.Mutex
	lastProgress := 0
	prepared, err := s.service.PrepareExpected(ctx, target, input.PlanID, input.AllowWarnings, input.AllowComplianceFailures)
	if err == nil {
		err = s.service.ExecutePreparedWithOptions(ctx, prepared, service.ExecuteOptions{Source: "mcp", Path: input.Path, Progress: func(event upload.ProgressEvent) {
			if progressToken == nil || event.Current == 0 {
				return
			}
			progressMu.Lock()
			defer progressMu.Unlock()
			if event.Current <= lastProgress {
				return
			}
			lastProgress = event.Current
			message := fmt.Sprintf("%s %s", event.Phase, event.FileName)
			if event.Error != nil {
				message += ": " + event.Error.Error()
			}
			_ = request.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: progressToken,
				Progress:      float64(event.Current),
				Total:         float64(event.Total),
				Message:       message,
			})
		}})
	}
	if err != nil {
		if errors.Is(err, service.ErrPlanChanged) && prepared != nil {
			return nil, executeUploadOutput{}, fmt.Errorf("%w; new plan_id is %s", err, prepared.PlanID)
		}
		return nil, executeUploadOutput{}, err
	}
	return nil, executeUploadOutput{
		Success:       true,
		PlanID:        prepared.PlanID,
		Actions:       prepared.Plan.TotalActions(),
		UploadedBytes: prepared.Plan.TotalUploadBytes(),
	}, nil
}

func (s *Server) startUpload(ctx context.Context, _ *mcp.CallToolRequest, input executeUploadInput) (*mcp.CallToolResult, startUploadOutput, error) {
	if err := s.requireAuth(); err != nil {
		return nil, startUploadOutput{}, err
	}
	absolutePath, err := filepath.Abs(input.Path)
	if err != nil {
		return nil, startUploadOutput{}, fmt.Errorf("resolving upload path: %w", err)
	}
	target := service.UploadTarget{Team: input.Team, Game: input.Game, Type: input.Type, Path: absolutePath, Compliance: input.Compliance}
	prepared, err := s.service.PrepareExpected(ctx, target, input.PlanID, input.AllowWarnings, input.AllowComplianceFailures)
	if err != nil {
		if errors.Is(err, service.ErrPlanChanged) && prepared != nil {
			return nil, startUploadOutput{}, fmt.Errorf("%w; new plan_id is %s", err, prepared.PlanID)
		}
		return nil, startUploadOutput{}, err
	}
	user, err := s.service.CurrentUser(ctx)
	if err != nil {
		return nil, startUploadOutput{}, fmt.Errorf("resolving daemon account: %w", err)
	}

	store, err := operations.OpenDefault()
	if err != nil {
		return nil, startUploadOutput{}, fmt.Errorf("opening upload process registry: %w", err)
	}
	operationID := operations.NewID()
	if _, err := store.Enqueue(operations.Record{
		ID: operationID, Source: "mcp", AccountID: user.ID, Team: input.Team, Game: input.Game, Type: input.Type,
		Path: absolutePath, PlanID: prepared.PlanID, Status: operations.StatusQueued,
		AllowWarnings: input.AllowWarnings, AllowCompliance: input.AllowComplianceFailures,
		Compliance: input.Compliance, Desired: operations.DesiredRunning,
		TotalActions: prepared.Plan.TotalActions(), TotalBytes: prepared.Plan.TotalUploadBytes(),
	}); err != nil {
		return nil, startUploadOutput{}, fmt.Errorf("registering upload process: %w", err)
	}
	output := startUploadOutput{Accepted: true, OperationID: operationID, PlanID: prepared.PlanID}
	if _, err := daemon.EnsureStartedForAccount(user.ID); err != nil {
		output.Warning = "upload queued, but the independent daemon could not be started: " + err.Error()
		return nil, output, nil
	}
	output.DaemonRunning = true
	return nil, output, nil
}

func (s *Server) listUploadProcesses(_ context.Context, _ *mcp.CallToolRequest, input listUploadProcessesInput) (*mcp.CallToolResult, listUploadProcessesOutput, error) {
	store, err := operations.OpenDefault()
	if err != nil {
		return nil, listUploadProcessesOutput{}, err
	}
	processes, err := store.List(input.IncludeFinished)
	if err != nil {
		return nil, listUploadProcessesOutput{}, err
	}
	return nil, listUploadProcessesOutput{Processes: processes}, nil
}

func (s *Server) getUploadProcess(_ context.Context, _ *mcp.CallToolRequest, input getUploadProcessInput) (*mcp.CallToolResult, getUploadProcessOutput, error) {
	store, err := operations.OpenDefault()
	if err != nil {
		return nil, getUploadProcessOutput{}, err
	}
	process, err := store.Get(input.OperationID)
	if err != nil {
		return nil, getUploadProcessOutput{}, err
	}
	return nil, getUploadProcessOutput{Process: process}, nil
}

func (s *Server) pauseUpload(_ context.Context, _ *mcp.CallToolRequest, input operationInput) (*mcp.CallToolResult, operationOutput, error) {
	store, err := operations.OpenDefault()
	if err != nil {
		return nil, operationOutput{}, err
	}
	current, err := store.Get(input.OperationID)
	if err != nil {
		return nil, operationOutput{}, err
	}
	if err := requireDaemonOperation(current); err != nil {
		return nil, operationOutput{}, err
	}
	process, err := store.Control(input.OperationID, operations.DesiredPaused)
	return nil, operationOutput{Process: process}, err
}

func (s *Server) resumeUpload(_ context.Context, _ *mcp.CallToolRequest, input operationInput) (*mcp.CallToolResult, operationOutput, error) {
	store, err := operations.OpenDefault()
	if err != nil {
		return nil, operationOutput{}, err
	}
	current, err := store.Get(input.OperationID)
	if err != nil {
		return nil, operationOutput{}, err
	}
	if err := requireDaemonOperation(current); err != nil {
		return nil, operationOutput{}, err
	}
	process, err := store.Control(input.OperationID, operations.DesiredRunning)
	if err != nil {
		return nil, operationOutput{}, err
	}
	if _, err := daemon.EnsureStartedForAccount(process.AccountID); err != nil {
		return nil, operationOutput{}, fmt.Errorf("upload resumed but daemon could not be started: %w", err)
	}
	return nil, operationOutput{Process: process}, nil
}

func (s *Server) cancelUpload(_ context.Context, _ *mcp.CallToolRequest, input confirmedOperationInput) (*mcp.CallToolResult, operationOutput, error) {
	if !input.Confirm {
		return nil, operationOutput{}, errors.New("cancelling an upload requires confirm=true")
	}
	store, err := operations.OpenDefault()
	if err != nil {
		return nil, operationOutput{}, err
	}
	current, err := store.Get(input.OperationID)
	if err != nil {
		return nil, operationOutput{}, err
	}
	if err := requireDaemonOperation(current); err != nil {
		return nil, operationOutput{}, err
	}
	process, err := store.Control(input.OperationID, operations.DesiredCancelled)
	return nil, operationOutput{Process: process}, err
}

func (s *Server) retryUpload(_ context.Context, _ *mcp.CallToolRequest, input confirmedOperationInput) (*mcp.CallToolResult, operationOutput, error) {
	if !input.Confirm {
		return nil, operationOutput{}, errors.New("retrying an upload requires confirm=true")
	}
	store, err := operations.OpenDefault()
	if err != nil {
		return nil, operationOutput{}, err
	}
	process, err := store.Retry(input.OperationID)
	if err != nil {
		return nil, operationOutput{}, err
	}
	if _, err := daemon.EnsureStartedForAccount(process.AccountID); err != nil {
		return nil, operationOutput{}, fmt.Errorf("retry queued but daemon could not be started: %w", err)
	}
	return nil, operationOutput{Process: process}, nil
}

func requireDaemonOperation(record operations.Record) error {
	if record.AccountID == "" {
		return fmt.Errorf("upload operation %q is not daemon-owned and cannot be controlled through MCP", record.ID)
	}
	return nil
}

func (s *Server) publish(ctx context.Context, _ *mcp.CallToolRequest, input publishInput) (*mcp.CallToolResult, publishOutput, error) {
	if err := s.requireAuth(); err != nil {
		return nil, publishOutput{}, err
	}
	if !input.Confirm {
		return nil, publishOutput{}, errors.New("publishing requires confirm=true")
	}
	if input.Type == "math" {
		if !input.CheckCompliance {
			if input.Path != "" || input.Compliance != nil {
				return nil, publishOutput{}, errors.New("path and compliance require check_compliance=true")
			}
			result, err := s.service.PublishMathUnchecked(ctx, input.Team, input.Game)
			if err != nil {
				return nil, publishOutput{}, err
			}
			if result.IsError() {
				return nil, publishOutput{}, fmt.Errorf("publish error [%s]: %s", result.Code, result.Error())
			}
			return s.publishedOutput(ctx, input, result)
		}
		if input.Path == "" {
			return nil, publishOutput{}, errors.New("math publish requires path for compliance and scratch verification")
		}
		compliance := input.Compliance
		if compliance == nil {
			compliance = &upload.ComplianceOptions{}
		}
		result, err := s.service.PublishMath(ctx, service.UploadTarget{
			Team: input.Team, Game: input.Game, Type: input.Type, Path: input.Path, Compliance: compliance,
		}, true)
		if err != nil {
			return nil, publishOutput{}, err
		}
		if result.IsError() {
			return nil, publishOutput{}, fmt.Errorf("publish error [%s]: %s", result.Code, result.Error())
		}
		return s.publishedOutput(ctx, input, result)
	}
	result, err := s.service.Publish(ctx, input.Team, input.Game, input.Type)
	if err != nil {
		return nil, publishOutput{}, err
	}
	if result.IsError() {
		return nil, publishOutput{}, fmt.Errorf("publish error [%s]: %s", result.Code, result.Error())
	}
	return s.publishedOutput(ctx, input, result)
}

func (s *Server) publishedOutput(ctx context.Context, input publishInput, result *stake.PublishResult) (*mcp.CallToolResult, publishOutput, error) {
	output := publishOutput{Published: true, Result: result}
	if !input.UpdateApproval {
		return nil, output, nil
	}
	update, err := s.service.UpdateOpenApprovalVersion(ctx, input.Team, input.Game, input.Type, result.Version)
	if err != nil {
		output.ApprovalWarning = fmt.Sprintf("published successfully, but approval version was not updated: %v", err)
		return nil, output, nil
	}
	output.ApprovalVersion = update
	output.ApprovalWarning = update.Warning
	return nil, output, nil
}

func presentPlan(prepared *service.PreparedUpload) uploadPlanOutput {
	return uploadPlanOutput{
		PlanID:          prepared.PlanID,
		Target:          prepared.Target,
		Safety:          prepared.Safety,
		Compliance:      prepared.Compliance,
		Upload:          presentEntries(prepared.Plan.ToUpload),
		Copy:            presentEntries(prepared.Plan.ToCopy),
		Delete:          presentEntries(prepared.Plan.ToDelete),
		Unchanged:       presentEntries(prepared.Plan.Unchanged),
		TotalActions:    prepared.Plan.TotalActions(),
		TotalUploadSize: prepared.Plan.TotalUploadBytes(),
	}
}

func presentEntries(entries []upload.PlanEntry) []planEntryOutput {
	result := make([]planEntryOutput, len(entries))
	for i, entry := range entries {
		result[i] = planEntryOutput{Path: entry.RemoteKey, SourcePath: entry.SourceKey, Size: entry.Size}
	}
	return result
}
