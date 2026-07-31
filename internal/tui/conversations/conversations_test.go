package conversations

import (
	"strings"
	"testing"

	conversationcore "github.com/mnemoo/cli/internal/conversations"
	"github.com/mnemoo/cli/internal/stake"
)

func TestRenderPrivateCommentHidesTechnicalIDs(t *testing.T) {
	comment := "Please keep this internal."
	event := stake.ApprovalEvent{
		ID: 42, Time: 1_700_000_000_000, Type: "private_comment",
		User: stake.ApprovalUser{Name: "Reviewer"},
		Data: stake.ApprovalEventData{Comment: &comment, Images: []string{"asset-id"}},
	}
	view := renderTimelineBlock(timelineBlock{kind: timelineMessage, events: []stake.ApprovalEvent{event}}, true, false, 80)
	for _, expected := range []string{"Reviewer", "PRIVATE", comment, "1 attachment"} {
		if !strings.Contains(view, expected) {
			t.Errorf("rendered event missing %q:\n%s", expected, view)
		}
	}
	for _, hidden := range []string{"#42", "asset-id", "/api/asset/image/"} {
		if strings.Contains(view, hidden) {
			t.Errorf("rendered event exposed technical ID %q:\n%s", hidden, view)
		}
	}
}

func TestTimelineStacksOnlyOriginalRoutineEvents(t *testing.T) {
	comment := "Please verify this."
	events := []stake.ApprovalEvent{
		{ID: 1, Type: "version_publish"},
		{ID: 2, Type: "guideline_checked"},
		{ID: 3, Type: "guideline_unchecked"},
		{ID: 4, Type: "comment", Data: stake.ApprovalEventData{Comment: &comment}},
		{ID: 5, Type: "version_change"},
		{ID: 6, Type: "rating"},
	}
	blocks := buildTimelineBlocks(events)
	if len(blocks) != 4 {
		t.Fatalf("got %d blocks, want 4: %#v", len(blocks), blocks)
	}
	if blocks[0].kind != timelineStack || len(blocks[0].events) != 3 {
		t.Fatalf("first routine event stack = %#v", blocks[0])
	}
	if blocks[1].kind != timelineMessage || blocks[2].kind != timelineStack || blocks[3].kind != timelineSystem {
		t.Fatalf("unexpected block kinds: %#v", blocks)
	}
	collapsed := renderTimelineBlock(blocks[0], true, false, 80)
	if !strings.Contains(collapsed, "+2 more") || strings.Contains(collapsed, "└") {
		t.Fatalf("unexpected collapsed stack:\n%s", collapsed)
	}
	expanded := renderTimelineBlock(blocks[0], true, true, 80)
	if !strings.Contains(expanded, "└") || strings.Count(expanded, " • ") < 3 {
		t.Fatalf("unexpected expanded stack:\n%s", expanded)
	}
}

func TestRatingSubmissionCommentRemainsVisibleAsMessage(t *testing.T) {
	comment := "The replay information is incomplete."
	event := stake.ApprovalEvent{
		ID: 9, Type: "rating_submission", User: stake.ApprovalUser{Name: "Reviewer"},
		Data: stake.ApprovalEventData{Comment: &comment},
	}
	blocks := buildTimelineBlocks([]stake.ApprovalEvent{event})
	if len(blocks) != 1 || blocks[0].kind != timelineMessage {
		t.Fatalf("rating comment was not promoted to a message: %#v", blocks)
	}
	view := renderTimelineBlock(blocks[0], false, false, 80)
	if !strings.Contains(view, comment) || !strings.Contains(view, "RATING NOTE") {
		t.Fatalf("rating comment is not visible:\n%s", view)
	}
}

func TestUnreadFilter(t *testing.T) {
	model := Model{unreadOnly: true, threads: []conversationcore.ThreadSummary{
		{GameSlug: "read", Stats: stake.MessageStats{Unread: 0}},
		{GameSlug: "unread", Stats: stake.MessageStats{Unread: 2}},
	}}
	visible := model.visibleThreads()
	if len(visible) != 1 || visible[0].GameSlug != "unread" {
		t.Fatalf("unexpected filtered threads: %#v", visible)
	}
}

func TestThreadSearchFiltersWithoutUnreadMode(t *testing.T) {
	model := New(nil, nil, 100, 30)
	model.threads = []conversationcore.ThreadSummary{
		{GameSlug: "alpha", GameName: "Alpha Game", Preview: "first"},
		{GameSlug: "beta", GameName: "Beta Game", Preview: "review replay"},
	}
	model.search.SetValue("replay")
	visible := model.visibleThreads()
	if len(visible) != 1 || visible[0].GameSlug != "beta" {
		t.Fatalf("unexpected search results: %#v", visible)
	}
}

func TestDirectThreadEscReturnsToParent(t *testing.T) {
	model := NewThread(nil, nil, stake.Team{Slug: "team"}, "game", "Game", 100, 30)
	updated, command := model.handleKey("esc")
	if !updated.directThread || command == nil {
		t.Fatalf("direct thread did not return to parent: direct=%t command=%v", updated.directThread, command)
	}
	if _, ok := command().(GoBackMsg); !ok {
		t.Fatalf("direct thread returned unexpected message %T", command())
	}
}

func TestPollingPreservesExpandedStackAndSelection(t *testing.T) {
	model := New(nil, nil, 100, 30)
	model.screen = screenThread
	model.thread = &conversationcore.ThreadView{Conversation: &stake.ApprovalConversation{Events: []stake.ApprovalEvent{
		{ID: 1, Type: "version_publish"},
		{ID: 2, Type: "guideline_checked"},
		{ID: 3, Type: "comment"},
	}}}
	model.setTimelineContent()
	model.timelineCursor = 0
	model.toggleTimelineGroup()
	stackKey := model.timelineBlocks[0].key

	model.thread.Conversation.Events = []stake.ApprovalEvent{
		{ID: 1, Type: "version_publish"},
		{ID: 2, Type: "guideline_checked"},
		{ID: 4, Type: "version_change"},
		{ID: 3, Type: "comment"},
	}
	model.setTimelineContent()
	if model.timelineCursor != 0 || model.timelineBlocks[0].key != stackKey || !model.expandedGroups[stackKey] {
		t.Fatalf("polling lost timeline state: cursor=%d key=%q expanded=%t", model.timelineCursor, model.timelineBlocks[0].key, model.expandedGroups[stackKey])
	}
}

func TestAutoRefreshIgnoresStaleTimerAndSchedulesCurrentGeneration(t *testing.T) {
	model := Model{
		screen: screenThreads, selectedTeam: &stake.Team{Slug: "team"},
		refreshGeneration: 100, refreshScheduled: true,
	}
	stale, command := model.Update(refreshTickMsg{generation: 99})
	if command != nil || stale.refreshing {
		t.Fatalf("stale refresh tick changed the model: refreshing=%t command=%v", stale.refreshing, command)
	}
	current, command := model.Update(refreshTickMsg{generation: 100})
	if command == nil || !current.refreshing || !current.refreshScheduled {
		t.Fatalf("current refresh tick did not schedule work: refreshing=%t scheduled=%t command=%v", current.refreshing, current.refreshScheduled, command)
	}
}
