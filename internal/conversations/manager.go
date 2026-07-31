package conversations

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mnemoo/cli/internal/service"
	"github.com/mnemoo/cli/internal/stake"
)

const refreshWorkers = 6
const hookClaimTTL = 10 * time.Minute

type hookJob struct {
	hook    Hook
	payload HookPayload
}

type Manager struct {
	service   *service.Service
	accountID string
	store     *Store
	hooks     *HookRunner
}

type ThreadSummary struct {
	TeamSlug    string             `json:"team"`
	TeamName    string             `json:"teamName"`
	GameSlug    string             `json:"game"`
	GameName    string             `json:"gameName"`
	Open        bool               `json:"open"`
	Locked      bool               `json:"locked"`
	Updated     int64              `json:"updated"`
	EventCount  int                `json:"eventCount"`
	LastEventID int64              `json:"lastEventId,omitempty"`
	Preview     string             `json:"preview,omitempty"`
	Stats       stake.MessageStats `json:"stats"`
}

type RefreshError struct {
	Game  string `json:"game"`
	Error string `json:"error"`
}

type RefreshResult struct {
	Conversations []ThreadSummary `json:"conversations"`
	Hooks         []HookResult    `json:"hooks,omitempty"`
	Errors        []RefreshError  `json:"errors,omitempty"`
}

type ThreadView struct {
	Conversation *stake.ApprovalConversation `json:"conversation"`
	Stats        stake.MessageStats          `json:"stats"`
	Hooks        []HookResult                `json:"hooks,omitempty"`
}

func NewManager(appService *service.Service, accountID string, store *Store, hooks *HookRunner) *Manager {
	return &Manager{service: appService, accountID: accountID, store: store, hooks: hooks}
}

func NewDefaultManager(appService *service.Service, accountID string) (*Manager, error) {
	if strings.TrimSpace(accountID) == "" {
		return nil, errors.New("account ID is required")
	}
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	return NewManager(
		appService,
		accountID,
		NewStore(filepath.Join(dir, "messages.json")),
		NewHookRunner(filepath.Join(dir, "hooks.json")),
	), nil
}

func configDir() (string, error) {
	if override := os.Getenv("STAKE_CONFIG_DIR"); override != "" {
		return override, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config dir: %w", err)
	}
	return filepath.Join(dir, "stakecli"), nil
}

func (m *Manager) AccountID() string { return m.accountID }
func (m *Manager) StatePath() string { return m.store.Path() }
func (m *Manager) HooksPath() string { return m.hooks.Path() }

func (m *Manager) ListHooks() ([]Hook, error) { return m.hooks.Load() }

func (m *Manager) UpsertHook(hook Hook) ([]Hook, error) { return m.hooks.Upsert(hook) }

func (m *Manager) DeleteHook(name string) ([]Hook, error) { return m.hooks.Delete(name) }

func (m *Manager) TestHook(ctx context.Context, name, team, game string, eventID int64) (HookResult, error) {
	hooks, err := m.hooks.Load()
	if err != nil {
		return HookResult{}, err
	}
	var selected *Hook
	for index := range hooks {
		if hooks[index].Name == name {
			selected = &hooks[index]
			break
		}
	}
	if selected == nil {
		return HookResult{}, fmt.Errorf("hook %q not found", name)
	}
	conversation, err := m.service.GetConversation(ctx, team, game)
	if err != nil {
		return HookResult{}, err
	}
	var message *stake.ApprovalEvent
	for index := range conversation.Events {
		if conversation.Events[index].ID == eventID {
			message = &conversation.Events[index]
			break
		}
	}
	if message == nil {
		return HookResult{}, fmt.Errorf("approval event ID %d not found", eventID)
	}
	state, err := m.store.Snapshot(m.accountID, conversationKey(team, game))
	if err != nil {
		return HookResult{}, err
	}
	payload := makeHookPayload(
		m.accountID, conversation, *message,
		calculateStats(conversation.Events, idSet(state.ReadIDs)),
	)
	if selected.IncludeThread {
		payload.Thread = threadForHook(conversation, selected.IncludePrivate)
	}
	return m.hooks.Run(ctx, *selected, payload), nil
}

func (m *Manager) RefreshTeam(ctx context.Context, team string) (RefreshResult, error) {
	games, err := m.service.ListApprovalGames(ctx, team)
	if err != nil {
		return RefreshResult{}, err
	}
	type fetched struct {
		index        int
		game         stake.Game
		conversation *stake.ApprovalConversation
		err          error
	}
	jobs := make(chan int)
	results := make(chan fetched, len(games))
	workers := refreshWorkers
	if len(games) < workers {
		workers = len(games)
	}
	var wait sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := range jobs {
				game := games[index]
				conversation, fetchErr := m.service.GetConversation(ctx, team, game.Slug)
				results <- fetched{index: index, game: game, conversation: conversation, err: fetchErr}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index := range games {
			select {
			case jobs <- index:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wait.Wait()
		close(results)
	}()

	fetchedConversations := make([]fetched, 0, len(games))
	for result := range results {
		fetchedConversations = append(fetchedConversations, result)
	}
	sort.Slice(fetchedConversations, func(i, j int) bool { return fetchedConversations[i].index < fetchedConversations[j].index })

	var refresh RefreshResult
	for _, result := range fetchedConversations {
		if result.err != nil {
			refresh.Errors = append(refresh.Errors, RefreshError{Game: result.game.Slug, Error: result.err.Error()})
			continue
		}
		stats, hookResults, observeErr := m.observe(ctx, result.conversation)
		if observeErr != nil {
			refresh.Errors = append(refresh.Errors, RefreshError{Game: result.game.Slug, Error: observeErr.Error()})
			continue
		}
		refresh.Hooks = append(refresh.Hooks, hookResults...)
		refresh.Conversations = append(refresh.Conversations, summarize(result.conversation, stats))
	}
	sort.SliceStable(refresh.Conversations, func(i, j int) bool {
		return refresh.Conversations[i].Updated > refresh.Conversations[j].Updated
	})
	return refresh, nil
}

func (m *Manager) GetThread(ctx context.Context, team, game string, markRead bool) (*ThreadView, error) {
	conversation, err := m.service.GetConversation(ctx, team, game)
	if err != nil {
		return nil, err
	}
	stats, hookResults, err := m.observe(ctx, conversation)
	if err != nil {
		return nil, err
	}
	if markRead {
		stats, err = m.setRead(conversation, nil, true)
		if err != nil {
			return nil, err
		}
	}
	return &ThreadView{Conversation: conversation, Stats: stats, Hooks: hookResults}, nil
}

func (m *Manager) SetRead(ctx context.Context, team, game string, ids []int64, read bool) (stake.MessageStats, error) {
	conversation, err := m.service.GetConversation(ctx, team, game)
	if err != nil {
		return stake.MessageStats{}, err
	}
	if _, _, err := m.observe(ctx, conversation); err != nil {
		return stake.MessageStats{}, err
	}
	return m.setRead(conversation, ids, read)
}

func (m *Manager) SendMessage(ctx context.Context, team, game, comment string, private bool, assets []string) (*ThreadView, error) {
	if err := m.service.SendMessage(ctx, team, game, comment, private, assets); err != nil {
		return nil, err
	}
	return m.GetThread(ctx, team, game, true)
}

func (m *Manager) observe(ctx context.Context, conversation *stake.ApprovalConversation) (stake.MessageStats, []HookResult, error) {
	if conversation == nil {
		return stake.MessageStats{}, nil, errors.New("conversation is required")
	}
	hooks, err := m.hooks.Load()
	if err != nil {
		return stake.MessageStats{}, nil, err
	}
	key := conversationKey(conversation.Team.Slug, conversation.Game.Slug)
	var stats stake.MessageStats
	var jobs []hookJob
	err = m.store.Update(m.accountID, key, func(state *ConversationState) error {
		observed := idSet(state.ObservedIDs)
		read := idSet(state.ReadIDs)
		if !state.Initialized {
			for _, event := range conversation.Events {
				observed[event.ID] = struct{}{}
				if event.IsMessage() && event.User.ID != "" && event.User.ID == m.accountID {
					read[event.ID] = struct{}{}
				}
			}
			state.Initialized = true
			state.ObservedIDs = setIDs(observed)
			state.ReadIDs = setIDs(read)
			stats = calculateStats(conversation.Events, read)
			return nil
		}

		for _, event := range conversation.Events {
			if _, exists := observed[event.ID]; exists {
				continue
			}
			if event.IsMessage() && event.User.ID != "" && event.User.ID == m.accountID {
				read[event.ID] = struct{}{}
			}

			payload := makeHookPayload(m.accountID, conversation, event, calculateStats(conversation.Events, read))
			matched := m.hooks.Matching(hooks, payload)
			allDelivered := true
			for _, hook := range matched {
				receipts := idSet(state.HookReceipts[hook.Name])
				if _, delivered := receipts[event.ID]; delivered {
					continue
				}
				allDelivered = false
				claims := state.HookClaims[hook.Name]
				if claims == nil {
					claims = make(map[int64]int64)
					state.HookClaims[hook.Name] = claims
				}
				if claimedAt, claimed := claims[event.ID]; claimed && time.Since(time.UnixMilli(claimedAt)) < hookClaimTTL {
					continue
				}
				claims[event.ID] = time.Now().UnixMilli()
				hookPayload := payload
				if hook.IncludeThread {
					hookPayload.Thread = threadForHook(conversation, hook.IncludePrivate)
				}
				jobs = append(jobs, hookJob{hook: hook, payload: hookPayload})
			}
			if allDelivered {
				observed[event.ID] = struct{}{}
			}
		}
		state.ObservedIDs = setIDs(observed)
		state.ReadIDs = setIDs(read)
		stats = calculateStats(conversation.Events, read)
		return nil
	})
	if err != nil || len(jobs) == 0 {
		return stats, nil, err
	}

	hookResults := make([]HookResult, len(jobs))
	for index, job := range jobs {
		hookResults[index] = m.hooks.Run(ctx, job.hook, job.payload)
	}
	err = m.store.Update(m.accountID, key, func(state *ConversationState) error {
		for index, job := range jobs {
			claims := state.HookClaims[job.hook.Name]
			delete(claims, job.payload.Message.ID)
			if hookResults[index].Success {
				receipts := idSet(state.HookReceipts[job.hook.Name])
				receipts[job.payload.Message.ID] = struct{}{}
				state.HookReceipts[job.hook.Name] = setIDs(receipts)
			}
		}

		observed := idSet(state.ObservedIDs)
		read := idSet(state.ReadIDs)
		for _, event := range conversation.Events {
			if _, exists := observed[event.ID]; exists {
				continue
			}
			payload := makeHookPayload(m.accountID, conversation, event, calculateStats(conversation.Events, read))
			matched := m.hooks.Matching(hooks, payload)
			allDelivered := true
			for _, hook := range matched {
				if _, delivered := idSet(state.HookReceipts[hook.Name])[event.ID]; !delivered {
					allDelivered = false
					break
				}
			}
			if allDelivered {
				observed[event.ID] = struct{}{}
			}
		}
		state.ObservedIDs = setIDs(observed)
		stats = calculateStats(conversation.Events, read)
		return nil
	})
	return stats, hookResults, err
}

func makeHookPayload(accountID string, conversation *stake.ApprovalConversation, event stake.ApprovalEvent, stats stake.MessageStats) HookPayload {
	name := "approval_event"
	if event.IsMessage() {
		name = "new_message"
	}
	return HookPayload{
		Event: name, EventType: event.Type, AccountID: accountID,
		Team: conversation.Team.Slug, Game: conversation.Game.Slug,
		GameName: conversation.Game.Name, Message: event, Stats: stats,
	}
}

func (m *Manager) setRead(conversation *stake.ApprovalConversation, ids []int64, readValue bool) (stake.MessageStats, error) {
	requested := idSet(ids)
	all := len(ids) == 0
	valid := make(map[int64]struct{})
	for _, event := range conversation.Events {
		if event.IsMessage() {
			valid[event.ID] = struct{}{}
		}
	}
	for id := range requested {
		if _, exists := valid[id]; !exists {
			return stake.MessageStats{}, fmt.Errorf("message ID %d does not belong to this conversation", id)
		}
	}

	key := conversationKey(conversation.Team.Slug, conversation.Game.Slug)
	var stats stake.MessageStats
	err := m.store.Update(m.accountID, key, func(state *ConversationState) error {
		read := idSet(state.ReadIDs)
		for id := range valid {
			if !all {
				if _, selected := requested[id]; !selected {
					continue
				}
			}
			if readValue {
				read[id] = struct{}{}
			} else {
				delete(read, id)
			}
		}
		state.ReadIDs = setIDs(read)
		stats = calculateStats(conversation.Events, read)
		return nil
	})
	return stats, err
}

func summarize(conversation *stake.ApprovalConversation, stats stake.MessageStats) ThreadSummary {
	summary := ThreadSummary{
		TeamSlug: conversation.Team.Slug, TeamName: conversation.Team.Name,
		GameSlug: conversation.Game.Slug, GameName: conversation.Game.Name,
		Open: conversation.Open, Locked: conversation.Locked,
		Updated: conversation.Updated, EventCount: conversation.EventCount, Stats: stats,
	}
	for index := len(conversation.Events) - 1; index >= 0; index-- {
		event := conversation.Events[index]
		if !event.IsMessage() {
			continue
		}
		summary.LastEventID = event.ID
		if event.Data.Comment != nil {
			summary.Preview = compact(*event.Data.Comment, 80)
		}
		break
	}
	return summary
}

func calculateStats(events []stake.ApprovalEvent, read map[int64]struct{}) stake.MessageStats {
	stats := stake.MessageStats{}
	for _, event := range events {
		if !event.IsMessage() {
			continue
		}
		stats.Total++
		if _, ok := read[event.ID]; ok {
			stats.Read++
		} else {
			stats.Unread++
		}
	}
	return stats
}

func conversationKey(team, game string) string { return team + "/" + game }

func idSet(ids []int64) map[int64]struct{} {
	set := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

func setIDs(set map[int64]struct{}) []int64 {
	ids := make([]int64, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func compact(value string, limit int) string {
	value = strings.Join(strings.Fields(value), " ")
	if len(value) <= limit {
		return value
	}
	return value[:limit-1] + "…"
}

func threadForHook(conversation *stake.ApprovalConversation, includePrivate bool) *stake.ApprovalConversation {
	copyConversation := *conversation
	copyConversation.Events = make([]stake.ApprovalEvent, 0, len(conversation.Events))
	for _, event := range conversation.Events {
		if event.IsPrivate() && !includePrivate {
			continue
		}
		copyConversation.Events = append(copyConversation.Events, event)
	}
	return &copyConversation
}
