package conversations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mnemoo/cli/internal/service"
	"github.com/mnemoo/cli/internal/stake"
)

type conversationGateway struct {
	service.Gateway
	mu           sync.Mutex
	games        []stake.Game
	conversation *stake.ApprovalConversation
	sent         []string
}

func (g *conversationGateway) ListApprovalGames(context.Context, string) ([]stake.Game, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]stake.Game(nil), g.games...), nil
}

func (g *conversationGateway) GetApprovalConversation(context.Context, string, string) (*stake.ApprovalConversation, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	data, _ := json.Marshal(g.conversation)
	var copyConversation stake.ApprovalConversation
	_ = json.Unmarshal(data, &copyConversation)
	return &copyConversation, nil
}

func (g *conversationGateway) PostApprovalComment(_ context.Context, _, _, comment string, private bool, _ []string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.sent = append(g.sent, fmt.Sprintf("%t:%s", private, comment))
	return nil
}

func TestManagerTracksIDsAndDeliversNewMessageHookOnce(t *testing.T) {
	t.Setenv("STAKE_SID", "must-not-reach-hook")
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, "hooks.json")
	config := HookConfig{Hooks: []Hook{{
		Name: "test", Command: []string{os.Args[0], "-test.run=TestHookHelperProcess", "--", "hook-helper"},
	}}}
	data, err := json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal hooks: %v", err)
	}
	if err := os.WriteFile(hooksPath, data, 0o600); err != nil {
		t.Fatalf("write hooks: %v", err)
	}

	gateway := &conversationGateway{
		games: []stake.Game{{Name: "Game", Slug: "game", Approval: &stake.GameApproval{Open: true}}},
		conversation: &stake.ApprovalConversation{
			Team: stake.Team{Name: "Team", Slug: "team"}, Game: stake.ApprovalOperator{Name: "Game", Slug: "game"},
			Open: true, EventCount: 1, Events: []stake.ApprovalEvent{messageEvent(1, "reviewer", "existing")},
		},
	}
	manager := NewManager(service.New(gateway), "me", NewStore(filepath.Join(dir, "messages.json")), NewHookRunner(hooksPath))

	baseline, err := manager.RefreshTeam(context.Background(), "team")
	if err != nil {
		t.Fatalf("baseline refresh: %v", err)
	}
	if len(baseline.Hooks) != 0 {
		t.Fatalf("historical events triggered hooks: %#v", baseline.Hooks)
	}
	if got := baseline.Conversations[0].Stats.Unread; got != 1 {
		t.Fatalf("baseline unread = %d, want 1", got)
	}

	gateway.mu.Lock()
	gateway.conversation.Events = append(gateway.conversation.Events, messageEvent(2, "reviewer", "new"))
	gateway.conversation.EventCount = 2
	gateway.conversation.Updated = 2
	gateway.mu.Unlock()

	refreshed, err := manager.RefreshTeam(context.Background(), "team")
	if err != nil {
		t.Fatalf("new message refresh: %v", err)
	}
	if len(refreshed.Hooks) != 1 || !refreshed.Hooks[0].Success || refreshed.Hooks[0].EventID != 2 {
		t.Fatalf("unexpected hook results: %#v", refreshed.Hooks)
	}
	if refreshed.Conversations[0].Stats.Unread != 2 {
		t.Fatalf("new message was not counted unread: %#v", refreshed.Conversations[0].Stats)
	}

	repeated, err := manager.RefreshTeam(context.Background(), "team")
	if err != nil {
		t.Fatalf("repeat refresh: %v", err)
	}
	if len(repeated.Hooks) != 0 {
		t.Fatalf("hook repeated for an observed ID: %#v", repeated.Hooks)
	}

	thread, err := manager.GetThread(context.Background(), "team", "game", true)
	if err != nil {
		t.Fatalf("mark thread read: %v", err)
	}
	if thread.Stats.Unread != 0 || thread.Stats.Read != 2 {
		t.Fatalf("unexpected read stats: %#v", thread.Stats)
	}
	stats, err := manager.SetRead(context.Background(), "team", "game", []int64{2}, false)
	if err != nil {
		t.Fatalf("mark message unread: %v", err)
	}
	if stats.Unread != 1 || stats.Read != 1 {
		t.Fatalf("unexpected unread stats: %#v", stats)
	}
}

func TestPrivateMessageRequiresExplicitHookOptIn(t *testing.T) {
	dir := t.TempDir()
	hooksPath := filepath.Join(dir, "hooks.json")
	config := HookConfig{Hooks: []Hook{{Name: "public-only", Command: []string{os.Args[0], "-test.run=TestHookHelperProcess", "--", "hook-helper"}}}}
	data, _ := json.Marshal(config)
	if err := os.WriteFile(hooksPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	gateway := &conversationGateway{
		games: []stake.Game{{Name: "Game", Slug: "game"}},
		conversation: &stake.ApprovalConversation{
			Team: stake.Team{Name: "Team", Slug: "team"}, Game: stake.ApprovalOperator{Name: "Game", Slug: "game"},
			Events: []stake.ApprovalEvent{messageEvent(1, "reviewer", "baseline")},
		},
	}
	manager := NewManager(service.New(gateway), "me", NewStore(filepath.Join(dir, "messages.json")), NewHookRunner(hooksPath))
	if _, err := manager.RefreshTeam(context.Background(), "team"); err != nil {
		t.Fatal(err)
	}
	gateway.mu.Lock()
	private := messageEvent(2, "reviewer", "internal")
	private.Type = "private_comment"
	gateway.conversation.Events = append(gateway.conversation.Events, private)
	gateway.mu.Unlock()
	result, err := manager.RefreshTeam(context.Background(), "team")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Hooks) != 0 {
		t.Fatalf("private message leaked to public-only hook: %#v", result.Hooks)
	}
}

func TestHookRunnerUpsertAndDelete(t *testing.T) {
	runner := NewHookRunner(filepath.Join(t.TempDir(), "hooks.json"))
	hooks, err := runner.Upsert(Hook{Name: "notify", Command: []string{"notifier"}})
	if err != nil {
		t.Fatalf("upsert hook: %v", err)
	}
	if len(hooks) != 1 || len(hooks[0].Events) != 1 || hooks[0].Events[0] != "new_message" {
		t.Fatalf("unexpected saved hook: %#v", hooks)
	}
	hooks, err = runner.Upsert(Hook{Name: "notify", Command: []string{"replacement"}, IncludePrivate: true})
	if err != nil {
		t.Fatalf("replace hook: %v", err)
	}
	if len(hooks) != 1 || hooks[0].Command[0] != "replacement" || !hooks[0].IncludePrivate {
		t.Fatalf("hook was not replaced: %#v", hooks)
	}
	hooks, err = runner.Delete("notify")
	if err != nil {
		t.Fatalf("delete hook: %v", err)
	}
	if len(hooks) != 0 {
		t.Fatalf("hook was not deleted: %#v", hooks)
	}
}

func TestStoreCoordinatesConcurrentProcesses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messages.json")
	first := NewStore(path)
	second := NewStore(path)
	var wait sync.WaitGroup
	for index, store := range []*Store{first, second} {
		index, store := index, store
		wait.Add(1)
		go func() {
			defer wait.Done()
			for iteration := 0; iteration < 20; iteration++ {
				conversation := fmt.Sprintf("team/game-%d", index)
				if err := store.Update("account", conversation, func(state *ConversationState) error {
					state.ReadIDs = append(state.ReadIDs, int64(iteration+1))
					return nil
				}); err != nil {
					t.Errorf("concurrent update: %v", err)
					return
				}
			}
		}()
	}
	wait.Wait()
	for index := 0; index < 2; index++ {
		state, err := first.Snapshot("account", fmt.Sprintf("team/game-%d", index))
		if err != nil {
			t.Fatal(err)
		}
		if len(state.ReadIDs) != 20 {
			t.Fatalf("conversation %d lost updates: %#v", index, state.ReadIDs)
		}
	}
}

func TestConcurrentManagersClaimNewMessageHookOnce(t *testing.T) {
	dir := t.TempDir()
	outputPath := filepath.Join(dir, "deliveries.log")
	hooksPath := filepath.Join(dir, "hooks.json")
	config := HookConfig{Hooks: []Hook{{
		Name: "once", Command: []string{os.Args[0], "-test.run=TestHookHelperProcess", "--", "hook-helper", outputPath},
	}}}
	data, _ := json.Marshal(config)
	if err := os.WriteFile(hooksPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	gateway := &conversationGateway{
		games: []stake.Game{{Name: "Game", Slug: "game"}},
		conversation: &stake.ApprovalConversation{
			Team: stake.Team{Name: "Team", Slug: "team"}, Game: stake.ApprovalOperator{Name: "Game", Slug: "game"},
			Events: []stake.ApprovalEvent{messageEvent(1, "reviewer", "baseline")},
		},
	}
	statePath := filepath.Join(dir, "messages.json")
	first := NewManager(service.New(gateway), "me", NewStore(statePath), NewHookRunner(hooksPath))
	second := NewManager(service.New(gateway), "me", NewStore(statePath), NewHookRunner(hooksPath))
	if _, err := first.RefreshTeam(context.Background(), "team"); err != nil {
		t.Fatal(err)
	}
	gateway.mu.Lock()
	gateway.conversation.Events = append(gateway.conversation.Events, messageEvent(2, "reviewer", "new"))
	gateway.mu.Unlock()

	var wait sync.WaitGroup
	results := make(chan RefreshResult, 2)
	for _, manager := range []*Manager{first, second} {
		manager := manager
		wait.Add(1)
		go func() {
			defer wait.Done()
			result, err := manager.RefreshTeam(context.Background(), "team")
			if err != nil {
				t.Errorf("concurrent refresh: %v", err)
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	var runs int
	for result := range results {
		runs += len(result.Hooks)
	}
	if runs != 1 {
		t.Fatalf("hook executed %d times, want once", runs)
	}
	deliveries, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read deliveries: %v", err)
	}
	if strings.Count(strings.TrimSpace(string(deliveries)), "delivered") != 1 {
		t.Fatalf("unexpected deliveries: %q", deliveries)
	}
}

func TestHookHelperProcess(t *testing.T) {
	helperIndex := -1
	for index, argument := range os.Args {
		if argument == "hook-helper" {
			helperIndex = index
			break
		}
	}
	if helperIndex == -1 {
		return
	}
	if os.Getenv("STAKE_SID") != "" {
		_, _ = fmt.Fprintln(os.Stderr, "credential leaked")
		os.Exit(3)
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil || len(data) == 0 {
		os.Exit(4)
	}
	if helperIndex+1 < len(os.Args) {
		file, openErr := os.OpenFile(os.Args[helperIndex+1], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if openErr != nil {
			os.Exit(5)
		}
		_, _ = fmt.Fprintln(file, "delivered")
		_ = file.Close()
	}
	_, _ = fmt.Fprintln(os.Stdout, "ok")
	os.Exit(0)
}

func messageEvent(id int64, userID, comment string) stake.ApprovalEvent {
	return stake.ApprovalEvent{
		ID: id, Time: id, Type: "comment", User: stake.ApprovalUser{ID: userID, Name: "Reviewer"},
		Data: stake.ApprovalEventData{Comment: &comment},
	}
}
