package conversations

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

const stateVersion = 1

type ConversationState struct {
	Initialized  bool                       `json:"initialized"`
	ObservedIDs  []int64                    `json:"observedIds,omitempty"`
	ReadIDs      []int64                    `json:"readIds,omitempty"`
	HookReceipts map[string][]int64         `json:"hookReceipts,omitempty"`
	HookClaims   map[string]map[int64]int64 `json:"hookClaims,omitempty"`
	UpdatedAt    int64                      `json:"updatedAt"`
}

type accountState struct {
	Conversations map[string]*ConversationState `json:"conversations"`
}

type stateFile struct {
	Version  int                      `json:"version"`
	Accounts map[string]*accountState `json:"accounts"`
}

type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) Path() string { return s.path }

func (s *Store) Snapshot(accountID, conversation string) (ConversationState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireFileLock(s.path, "message state")
	if err != nil {
		return ConversationState{}, err
	}
	defer releaseFileLock(lock)
	state, err := s.load()
	if err != nil {
		return ConversationState{}, err
	}
	conversationState := ensureConversation(state, accountID, conversation)
	return cloneState(conversationState), nil
}

func (s *Store) Update(accountID, conversation string, update func(*ConversationState) error) error {
	if accountID == "" {
		return errors.New("account ID is required")
	}
	if conversation == "" {
		return errors.New("conversation key is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	lock, err := acquireFileLock(s.path, "message state")
	if err != nil {
		return err
	}
	defer releaseFileLock(lock)

	state, err := s.load()
	if err != nil {
		return err
	}
	conversationState := ensureConversation(state, accountID, conversation)
	if err := update(conversationState); err != nil {
		return err
	}
	conversationState.ObservedIDs = sortedUnique(conversationState.ObservedIDs)
	conversationState.ReadIDs = sortedUnique(conversationState.ReadIDs)
	for name, ids := range conversationState.HookReceipts {
		conversationState.HookReceipts[name] = sortedUnique(ids)
	}
	conversationState.UpdatedAt = time.Now().UnixMilli()
	return s.save(state)
}

func acquireFileLock(path, subject string) (*os.File, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating %s directory: %w", subject, err)
	}
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s lock: %w", subject, err)
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("locking %s: %w", subject, err)
	}
	return file, nil
}

func releaseFileLock(file *os.File) {
	_ = unlockFile(file)
	_ = file.Close()
}

func (s *Store) load() (*stateFile, error) {
	state := &stateFile{Version: stateVersion, Accounts: make(map[string]*accountState)}
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return nil, fmt.Errorf("reading message state: %w", err)
	}
	if err := json.Unmarshal(data, state); err != nil {
		return nil, fmt.Errorf("parsing message state: %w", err)
	}
	if state.Version != stateVersion {
		return nil, fmt.Errorf("unsupported message state version %d", state.Version)
	}
	if state.Accounts == nil {
		state.Accounts = make(map[string]*accountState)
	}
	return state, nil
}

func (s *Store) save(state *stateFile) error {
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating message state directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".messages-*.json")
	if err != nil {
		return fmt.Errorf("creating temporary message state: %w", err)
	}
	tempPath := file.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("securing message state: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(state); err != nil {
		_ = file.Close()
		return fmt.Errorf("encoding message state: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("syncing message state: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("closing message state: %w", err)
	}
	if err := replaceFile(tempPath, s.path); err != nil {
		return fmt.Errorf("installing message state: %w", err)
	}
	return nil
}

func ensureConversation(state *stateFile, accountID, conversation string) *ConversationState {
	account := state.Accounts[accountID]
	if account == nil {
		account = &accountState{Conversations: make(map[string]*ConversationState)}
		state.Accounts[accountID] = account
	}
	if account.Conversations == nil {
		account.Conversations = make(map[string]*ConversationState)
	}
	conversationState := account.Conversations[conversation]
	if conversationState == nil {
		conversationState = &ConversationState{
			HookReceipts: make(map[string][]int64),
			HookClaims:   make(map[string]map[int64]int64),
		}
		account.Conversations[conversation] = conversationState
	}
	if conversationState.HookReceipts == nil {
		conversationState.HookReceipts = make(map[string][]int64)
	}
	if conversationState.HookClaims == nil {
		conversationState.HookClaims = make(map[string]map[int64]int64)
	}
	return conversationState
}

func cloneState(state *ConversationState) ConversationState {
	copyState := *state
	copyState.ObservedIDs = append([]int64(nil), state.ObservedIDs...)
	copyState.ReadIDs = append([]int64(nil), state.ReadIDs...)
	copyState.HookReceipts = make(map[string][]int64, len(state.HookReceipts))
	for name, ids := range state.HookReceipts {
		copyState.HookReceipts[name] = append([]int64(nil), ids...)
	}
	copyState.HookClaims = make(map[string]map[int64]int64, len(state.HookClaims))
	for name, claims := range state.HookClaims {
		copyClaims := make(map[int64]int64, len(claims))
		for id, claimedAt := range claims {
			copyClaims[id] = claimedAt
		}
		copyState.HookClaims[name] = copyClaims
	}
	return copyState
}

func sortedUnique(ids []int64) []int64 {
	if len(ids) == 0 {
		return nil
	}
	seen := make(map[int64]struct{}, len(ids))
	result := make([]int64, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}
