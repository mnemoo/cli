// Package mcpserver exposes stakecli application services over native MCP.
package mcpserver

import (
	"context"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mnemoo/cli/internal/conversations"
	"github.com/mnemoo/cli/internal/service"
)

const instructions = "Use plan_upload before execute_upload or start_upload; both require the exact plan_id returned by the latest plan. start_upload queues work for the independent StakeCLI daemon, so uploads continue after the MCP client exits; progress and controls are available through upload operation tools. Publishing requires confirm=true. For approval conversations, read the latest thread before drafting a reviewer response, call prepare_review_reply, inspect its warnings, then call send_review_reply with the exact draft_id and confirm=true. Never expose a Stake SID, cookies, authorization headers, tokens, presigned URLs, local absolute paths, private notes, or unrelated implementation details in reviewer replies or outbound hooks. Public replies are visible in the approval conversation; use private only for intentionally internal discussion. Do not claim a fix, test, upload, or publish happened unless verified. Authentication is configured when the MCP process starts. Read-only tools may call the Stake Engine API; local validation and MLR tools only inspect local files unless the explicitly confirmed mlr_change tool is used. Upload synchronizes scratch state and may delete remote scratch files."

type AuthInfo struct {
	Configured bool
	Source     string
}

type Server struct {
	service   *service.Service
	auth      AuthInfo
	mcp       *mcp.Server
	managerMu sync.Mutex
	manager   *conversations.Manager
	draftsMu  sync.Mutex
	drafts    map[string]replyDraft
}

func New(appService *service.Service, authInfo AuthInfo, version string) *Server {
	s := &Server{service: appService, auth: authInfo, drafts: make(map[string]replyDraft)}
	s.mcp = mcp.NewServer(&mcp.Implementation{
		Name:    "stakecli",
		Title:   "Stake CLI",
		Version: version,
	}, &mcp.ServerOptions{Instructions: instructions})
	s.registerReadTools()
	s.registerUploadTools()
	s.registerConversationTools()
	s.registerDaemonTools()
	s.registerMLRTools()
	return s
}

func (s *Server) MCP() *mcp.Server {
	return s.mcp
}

func readOnlyAnnotations(title string, openWorld bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    true,
		DestructiveHint: boolPtr(false),
		IdempotentHint:  true,
		OpenWorldHint:   boolPtr(openWorld),
	}
}

func mutationAnnotations(title string, destructive, idempotent bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    false,
		DestructiveHint: boolPtr(destructive),
		IdempotentHint:  idempotent,
		OpenWorldHint:   boolPtr(true),
	}
}

func boolPtr(value bool) *bool {
	return &value
}

func (s *Server) conversationManager(ctx context.Context) (*conversations.Manager, error) {
	if err := s.requireAuth(); err != nil {
		return nil, err
	}
	s.managerMu.Lock()
	defer s.managerMu.Unlock()
	if s.manager != nil {
		return s.manager, nil
	}
	user, err := s.service.CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	manager, err := conversations.NewDefaultManager(s.service, user.ID)
	if err != nil {
		return nil, err
	}
	s.manager = manager
	return manager, nil
}
