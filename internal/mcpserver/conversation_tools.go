package mcpserver

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mnemoo/cli/internal/conversations"
	"github.com/mnemoo/cli/internal/stake"
)

const reviewerReplyGuide = `Reviewer reply guide:
1. Read the complete latest approval timeline before drafting. Answer the reviewer's concrete question or request, not a broader imagined issue.
2. Never include SID/cookies, Authorization headers, API tokens, presigned URLs, environment values, local absolute paths, private notes, raw stack traces, or unrelated proprietary implementation details.
3. Do not claim a change, test, upload, approval, or publish succeeded unless the available tool or repository evidence proves it.
4. Public is the normal reviewer-facing visibility. Private is only for an intentionally internal note; never copy a private event into a public reply.
5. Keep the answer concise, factual, and scoped. Describe observable behavior and verified outcomes; redact secrets instead of partially masking them.
6. Use prepare_review_reply first. If the timeline changes or the draft is rejected, reread the conversation and prepare a new draft.`

const replyDraftTTL = 30 * time.Minute

type listConversationsInput struct {
	Team string `json:"team" jsonschema:"Stake Engine team slug"`
}

type listConversationsOutput struct {
	Result conversations.RefreshResult `json:"result"`
}

type getConversationInput struct {
	Team     string `json:"team" jsonschema:"Stake Engine team slug"`
	Game     string `json:"game" jsonschema:"Stake Engine game slug"`
	MarkRead bool   `json:"mark_read,omitempty" jsonschema:"mark every message in the returned thread as read"`
}

type getConversationOutput struct {
	Thread *conversations.ThreadView `json:"thread"`
}

type setConversationReadInput struct {
	Team string  `json:"team" jsonschema:"Stake Engine team slug"`
	Game string  `json:"game" jsonschema:"Stake Engine game slug"`
	IDs  []int64 `json:"message_ids,omitempty" jsonschema:"message IDs to change; empty means all messages in the conversation"`
	Read bool    `json:"read" jsonschema:"true marks read, false marks unread"`
}

type messageStatsOutput struct {
	Stats stake.MessageStats `json:"stats"`
}

type replyGuideOutput struct {
	Guide string `json:"guide"`
}

type prepareReviewReplyInput struct {
	Team       string `json:"team" jsonschema:"Stake Engine team slug"`
	Game       string `json:"game" jsonschema:"Stake Engine game slug"`
	Message    string `json:"message" jsonschema:"reviewer reply text"`
	Visibility string `json:"visibility" jsonschema:"reply visibility: public or private"`
}

type replyDraft struct {
	ID            string   `json:"draftId"`
	Team          string   `json:"team"`
	Game          string   `json:"game"`
	Message       string   `json:"message"`
	Visibility    string   `json:"visibility"`
	LastEventID   int64    `json:"lastEventId"`
	TimelineHash  string   `json:"timelineHash"`
	Warnings      []string `json:"warnings,omitempty"`
	Blocked       bool     `json:"blocked"`
	BlockedReason string   `json:"blockedReason,omitempty"`
	CreatedAt     int64    `json:"createdAt"`
}

type prepareReviewReplyOutput struct {
	Draft replyDraft `json:"draft"`
	Guide string     `json:"guide"`
}

type sendReviewReplyInput struct {
	DraftID string `json:"draft_id" jsonschema:"exact draft_id returned by prepare_review_reply"`
	Confirm bool   `json:"confirm" jsonschema:"must be true to send the prepared reply"`
}

type sendReviewReplyOutput struct {
	Sent   bool                      `json:"sent"`
	Thread *conversations.ThreadView `json:"thread,omitempty"`
}

type listHooksOutput struct {
	ConfigPath string               `json:"configPath"`
	Hooks      []conversations.Hook `json:"hooks"`
}

type upsertHookInput struct {
	Hook    conversations.Hook `json:"hook" jsonschema:"complete hook definition; command is an argv array and is never interpreted by a shell unless the command explicitly invokes one"`
	Confirm bool               `json:"confirm" jsonschema:"must be true to write hooks.json"`
}

type deleteHookInput struct {
	Name    string `json:"name" jsonschema:"hook name"`
	Confirm bool   `json:"confirm" jsonschema:"must be true to delete the hook"`
}

type testHookInput struct {
	Name      string `json:"name" jsonschema:"hook name"`
	Team      string `json:"team" jsonschema:"Stake Engine team slug"`
	Game      string `json:"game" jsonschema:"Stake Engine game slug"`
	MessageID int64  `json:"message_id" jsonschema:"existing message ID used as the test payload"`
	Confirm   bool   `json:"confirm" jsonschema:"must be true because the configured command will execute"`
}

type hookMutationOutput struct {
	ConfigPath string                    `json:"configPath"`
	Hooks      []conversations.Hook      `json:"hooks,omitempty"`
	Result     *conversations.HookResult `json:"result,omitempty"`
}

var sensitiveReplyPatterns = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{name: "credential or authorization material", pattern: regexp.MustCompile(`(?i)(authorization\s*:|bearer\s+[a-z0-9._-]+|cookie\s*:|\bsid\s*[:=]|api[_ -]?key\s*[:=]|token\s*[:=]|-----BEGIN [A-Z ]*PRIVATE KEY-----)`)},
	{name: "presigned URL", pattern: regexp.MustCompile(`(?i)(x-amz-(signature|credential|security-token)|awsaccesskeyid)`)},
	{name: "local absolute path", pattern: regexp.MustCompile(`(?m)(?:^|[\s("'])(~/[^\s)"']+|/Users/[^\s)"']+|/home/[^\s)"']+|[A-Za-z]:\\Users\\[^\s)"']+)`)},
}

func (s *Server) registerConversationTools() {
	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "list_conversations", Title: "List approval conversations",
		Description: "Refresh all approval conversations for a team, return read/unread statistics by message ID, and run configured new_message hooks exactly once per new incoming message.",
		Annotations: mutationAnnotations("List approval conversations", false, false),
	}, s.listConversations)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "get_conversation", Title: "Read approval conversation",
		Description: "Read the complete current approval timeline for one game, including public/private comments, system events, assets, and read/unread statistics.",
		Annotations: mutationAnnotations("Read approval conversation", false, false),
	}, s.getConversation)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "set_conversation_read", Title: "Set message read state",
		Description: "Persistently mark selected message IDs, or every message in a conversation, as read or unread for the authenticated account.",
		Annotations: mutationAnnotations("Set message read state", false, true),
	}, s.setConversationRead)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "conversation_reply_guide", Title: "Reviewer reply safety guide",
		Description: "Return the mandatory safety and evidence rules for AI replies to Stake Engine reviewers.",
		Annotations: readOnlyAnnotations("Reviewer reply safety guide", false),
	}, s.conversationReplyGuide)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "prepare_review_reply", Title: "Prepare reviewer reply",
		Description: "Prepare and safety-check a public or private reviewer reply against the latest timeline. Does not send anything. The returned draft is bound to the latest event ID.",
		Annotations: mutationAnnotations("Prepare reviewer reply", false, false),
	}, s.prepareReviewReply)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "send_review_reply", Title: "Send prepared reviewer reply",
		Description: "Send a previously prepared reviewer reply. Requires confirm=true and rejects the send if the approval timeline changed since preparation.",
		Annotations: mutationAnnotations("Send prepared reviewer reply", false, false),
	}, s.sendReviewReply)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "list_message_hooks", Title: "List message hooks",
		Description: "List hot-reloaded command and HTTPS webhook hooks for reviewer messages and approval events.",
		Annotations: readOnlyAnnotations("List message hooks", false),
	}, s.listMessageHooks)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "upsert_message_hook", Title: "Create or update message hook",
		Description: "Create or replace a hot-reloaded command or HTTPS webhook hook. Commands receive sanitized JSON on stdin; webhooks receive signed JSON when webhookSecretEnv is configured. Requires confirm=true.",
		Annotations: mutationAnnotations("Create or update message hook", false, true),
	}, s.upsertMessageHook)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "delete_message_hook", Title: "Delete message hook",
		Description: "Delete a named hook from hooks.json. Requires confirm=true.",
		Annotations: mutationAnnotations("Delete message hook", true, true),
	}, s.deleteMessageHook)

	mcp.AddTool(s.mcp, &mcp.Tool{
		Name: "test_message_hook", Title: "Test message hook",
		Description: "Execute one configured hook with an existing approval message as JSON input. The run is not recorded as a new-message delivery and requires confirm=true.",
		Annotations: mutationAnnotations("Test message hook", false, false),
	}, s.testMessageHook)
}

func (s *Server) listConversations(ctx context.Context, _ *mcp.CallToolRequest, input listConversationsInput) (*mcp.CallToolResult, listConversationsOutput, error) {
	manager, err := s.conversationManager(ctx)
	if err != nil {
		return nil, listConversationsOutput{}, err
	}
	result, err := manager.RefreshTeam(ctx, input.Team)
	return nil, listConversationsOutput{Result: result}, err
}

func (s *Server) getConversation(ctx context.Context, _ *mcp.CallToolRequest, input getConversationInput) (*mcp.CallToolResult, getConversationOutput, error) {
	manager, err := s.conversationManager(ctx)
	if err != nil {
		return nil, getConversationOutput{}, err
	}
	thread, err := manager.GetThread(ctx, input.Team, input.Game, input.MarkRead)
	return nil, getConversationOutput{Thread: thread}, err
}

func (s *Server) setConversationRead(ctx context.Context, _ *mcp.CallToolRequest, input setConversationReadInput) (*mcp.CallToolResult, messageStatsOutput, error) {
	manager, err := s.conversationManager(ctx)
	if err != nil {
		return nil, messageStatsOutput{}, err
	}
	stats, err := manager.SetRead(ctx, input.Team, input.Game, input.IDs, input.Read)
	return nil, messageStatsOutput{Stats: stats}, err
}

func (s *Server) conversationReplyGuide(context.Context, *mcp.CallToolRequest, emptyInput) (*mcp.CallToolResult, replyGuideOutput, error) {
	return nil, replyGuideOutput{Guide: reviewerReplyGuide}, nil
}

func (s *Server) prepareReviewReply(ctx context.Context, _ *mcp.CallToolRequest, input prepareReviewReplyInput) (*mcp.CallToolResult, prepareReviewReplyOutput, error) {
	manager, err := s.conversationManager(ctx)
	if err != nil {
		return nil, prepareReviewReplyOutput{}, err
	}
	message := strings.TrimSpace(input.Message)
	if message == "" {
		return nil, prepareReviewReplyOutput{}, errors.New("message is required")
	}
	visibility := strings.ToLower(strings.TrimSpace(input.Visibility))
	if visibility != "public" && visibility != "private" {
		return nil, prepareReviewReplyOutput{}, errors.New("visibility must be public or private")
	}
	thread, err := manager.GetThread(ctx, input.Team, input.Game, false)
	if err != nil {
		return nil, prepareReviewReplyOutput{}, err
	}
	lastEventID := latestEventID(thread.Conversation.Events)
	timelineHash, err := timelineFingerprint(thread.Conversation.Events)
	if err != nil {
		return nil, prepareReviewReplyOutput{}, err
	}
	warnings, blockedReason := reviewReplyWarnings(message, visibility, thread.Conversation)
	draft := replyDraft{
		Team: input.Team, Game: input.Game, Message: message, Visibility: visibility,
		LastEventID: lastEventID, TimelineHash: timelineHash, Warnings: warnings, Blocked: blockedReason != "",
		BlockedReason: blockedReason, CreatedAt: time.Now().UnixMilli(),
	}
	draft.ID = replyDraftID(draft)
	s.draftsMu.Lock()
	for id, existing := range s.drafts {
		if time.Since(time.UnixMilli(existing.CreatedAt)) > replyDraftTTL {
			delete(s.drafts, id)
		}
	}
	s.drafts[draft.ID] = draft
	s.draftsMu.Unlock()
	return nil, prepareReviewReplyOutput{Draft: draft, Guide: reviewerReplyGuide}, nil
}

func (s *Server) sendReviewReply(ctx context.Context, _ *mcp.CallToolRequest, input sendReviewReplyInput) (*mcp.CallToolResult, sendReviewReplyOutput, error) {
	if !input.Confirm {
		return nil, sendReviewReplyOutput{}, errors.New("confirm=true is required")
	}
	draft, exists := s.takeReplyDraft(input.DraftID)
	if !exists {
		return nil, sendReviewReplyOutput{}, errors.New("unknown or expired draft_id; call prepare_review_reply")
	}
	if draft.Blocked {
		return nil, sendReviewReplyOutput{}, fmt.Errorf("draft is blocked: %s", draft.BlockedReason)
	}
	manager, err := s.conversationManager(ctx)
	if err != nil {
		return nil, sendReviewReplyOutput{}, err
	}
	current, err := manager.GetThread(ctx, draft.Team, draft.Game, false)
	if err != nil {
		return nil, sendReviewReplyOutput{}, err
	}
	if latest := latestEventID(current.Conversation.Events); latest != draft.LastEventID {
		return nil, sendReviewReplyOutput{}, fmt.Errorf("conversation changed from event %d to %d; reread it and prepare a new reply", draft.LastEventID, latest)
	}
	currentHash, err := timelineFingerprint(current.Conversation.Events)
	if err != nil {
		return nil, sendReviewReplyOutput{}, err
	}
	if currentHash != draft.TimelineHash {
		return nil, sendReviewReplyOutput{}, errors.New("conversation content changed after the draft was prepared; reread it and prepare a new reply")
	}
	if _, blockedReason := reviewReplyWarnings(draft.Message, draft.Visibility, current.Conversation); blockedReason != "" {
		return nil, sendReviewReplyOutput{}, fmt.Errorf("draft is blocked: %s", blockedReason)
	}
	thread, err := manager.SendMessage(ctx, draft.Team, draft.Game, draft.Message, draft.Visibility == "private", nil)
	if err != nil {
		return nil, sendReviewReplyOutput{}, err
	}
	return nil, sendReviewReplyOutput{Sent: true, Thread: thread}, nil
}

func (s *Server) takeReplyDraft(id string) (replyDraft, bool) {
	s.draftsMu.Lock()
	defer s.draftsMu.Unlock()
	draft, exists := s.drafts[id]
	if !exists {
		return replyDraft{}, false
	}
	delete(s.drafts, id)
	if time.Since(time.UnixMilli(draft.CreatedAt)) > replyDraftTTL {
		return replyDraft{}, false
	}
	return draft, true
}

func (s *Server) listMessageHooks(ctx context.Context, _ *mcp.CallToolRequest, _ emptyInput) (*mcp.CallToolResult, listHooksOutput, error) {
	manager, err := s.conversationManager(ctx)
	if err != nil {
		return nil, listHooksOutput{}, err
	}
	hooks, err := manager.ListHooks()
	return nil, listHooksOutput{ConfigPath: manager.HooksPath(), Hooks: hooks}, err
}

func (s *Server) upsertMessageHook(ctx context.Context, _ *mcp.CallToolRequest, input upsertHookInput) (*mcp.CallToolResult, hookMutationOutput, error) {
	if !input.Confirm {
		return nil, hookMutationOutput{}, errors.New("confirm=true is required")
	}
	manager, err := s.conversationManager(ctx)
	if err != nil {
		return nil, hookMutationOutput{}, err
	}
	hooks, err := manager.UpsertHook(input.Hook)
	return nil, hookMutationOutput{ConfigPath: manager.HooksPath(), Hooks: hooks}, err
}

func (s *Server) deleteMessageHook(ctx context.Context, _ *mcp.CallToolRequest, input deleteHookInput) (*mcp.CallToolResult, hookMutationOutput, error) {
	if !input.Confirm {
		return nil, hookMutationOutput{}, errors.New("confirm=true is required")
	}
	manager, err := s.conversationManager(ctx)
	if err != nil {
		return nil, hookMutationOutput{}, err
	}
	hooks, err := manager.DeleteHook(input.Name)
	return nil, hookMutationOutput{ConfigPath: manager.HooksPath(), Hooks: hooks}, err
}

func (s *Server) testMessageHook(ctx context.Context, _ *mcp.CallToolRequest, input testHookInput) (*mcp.CallToolResult, hookMutationOutput, error) {
	if !input.Confirm {
		return nil, hookMutationOutput{}, errors.New("confirm=true is required")
	}
	manager, err := s.conversationManager(ctx)
	if err != nil {
		return nil, hookMutationOutput{}, err
	}
	result, err := manager.TestHook(ctx, input.Name, input.Team, input.Game, input.MessageID)
	return nil, hookMutationOutput{ConfigPath: manager.HooksPath(), Result: &result}, err
}

func latestEventID(events []stake.ApprovalEvent) int64 {
	var latest int64
	for _, event := range events {
		if event.ID > latest {
			latest = event.ID
		}
	}
	return latest
}

func replyDraftID(draft replyDraft) string {
	payload := strings.Join([]string{draft.Team, draft.Game, draft.Visibility, draft.Message, strconv.FormatInt(draft.LastEventID, 10), draft.TimelineHash}, "\x00")
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func reviewReplyWarnings(message, visibility string, conversation *stake.ApprovalConversation) ([]string, string) {
	for _, sensitive := range sensitiveReplyPatterns {
		if sensitive.pattern.MatchString(message) {
			return nil, "message contains " + sensitive.name + "; redact it and prepare a new draft"
		}
	}
	if visibility == "public" && conversation != nil {
		lowerMessage := strings.ToLower(message)
		for _, event := range conversation.Events {
			if !event.IsPrivate() {
				continue
			}
			if event.Data.Comment != nil {
				privateComment := strings.TrimSpace(*event.Data.Comment)
				if len(privateComment) >= 16 && strings.Contains(lowerMessage, strings.ToLower(privateComment)) {
					return nil, "public reply copies content from a private event; summarize without exposing the private note"
				}
			}
			for _, asset := range event.Data.Images {
				if asset != "" && strings.Contains(message, asset) {
					return nil, "public reply references an asset from a private event"
				}
			}
		}
	}
	var warnings []string
	if visibility == "private" {
		warnings = append(warnings, "private visibility is for intentionally internal discussion and is not the normal reviewer-facing reply")
	}
	if len(message) > 4000 {
		warnings = append(warnings, "reply is unusually long; keep it scoped to the reviewer request")
	}
	if conversation != nil && !conversation.Open {
		warnings = append(warnings, "approval is currently closed")
	}
	return warnings, ""
}

func timelineFingerprint(events []stake.ApprovalEvent) (string, error) {
	data, err := json.Marshal(events)
	if err != nil {
		return "", fmt.Errorf("fingerprinting conversation: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
