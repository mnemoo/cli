package mcpserver

import (
	"context"
	"errors"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mnemoo/cli/internal/daemon"
)

type daemonStatusOutput struct {
	State daemon.State `json:"state"`
}

type daemonStopInput struct {
	Confirm bool `json:"confirm" jsonschema:"must be true to stop background upload and reviewer polling"`
}

func (s *Server) registerDaemonTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "daemon_status", Title: "Get StakeCLI daemon status",
		Description: "Inspect the independent background worker that runs queued uploads and polls reviewer conversations.",
		Annotations: readOnlyAnnotations("Get StakeCLI daemon status", false),
	}, s.daemonStatus)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "start_daemon", Title: "Start StakeCLI daemon",
		Description: "Start or reuse the independent background process for queued uploads and reviewer hooks.",
		Annotations: mutationAnnotations("Start StakeCLI daemon", false, true),
	}, s.startDaemon)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "stop_daemon", Title: "Stop StakeCLI daemon",
		Description: "Request a graceful stop of the independent background process. Requires confirm=true.",
		Annotations: mutationAnnotations("Stop StakeCLI daemon", true, true),
	}, s.stopDaemon)
}

func (s *Server) daemonStatus(context.Context, *mcp.CallToolRequest, emptyInput) (*mcp.CallToolResult, daemonStatusOutput, error) {
	state, err := daemon.ReadState()
	return nil, daemonStatusOutput{State: state}, err
}

func (s *Server) startDaemon(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, daemonStatusOutput, error) {
	if err := s.requireAuth(); err != nil {
		return nil, daemonStatusOutput{}, err
	}
	user, err := s.service.CurrentUser(ctx)
	if err != nil {
		return nil, daemonStatusOutput{}, err
	}
	state, err := daemon.EnsureStartedForAccount(user.ID)
	return nil, daemonStatusOutput{State: state}, err
}

func (s *Server) stopDaemon(_ context.Context, _ *mcp.CallToolRequest, input daemonStopInput) (*mcp.CallToolResult, daemonStatusOutput, error) {
	if !input.Confirm {
		return nil, daemonStatusOutput{}, errors.New("stopping the daemon requires confirm=true")
	}
	if err := daemon.RequestStop(); err != nil {
		return nil, daemonStatusOutput{}, err
	}
	state, err := daemon.ReadState()
	return nil, daemonStatusOutput{State: state}, err
}
