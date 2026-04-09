package auth

import (
	"os"
	"sync"

	"github.com/zalando/go-keyring"
)

const serviceName = "stakecli"

// memStore is a process-scoped fallback for when the OS keychain is
// disabled. SIDs live only until the process exits — exactly the right
// lifetime for a demo recording or a CI job.
var (
	memMu    sync.Mutex
	memStore = map[string]string{}
)

// keyringDisabled reports whether STAKE_KEYRING_DISABLE is set.
// When disabled, Set/Get/Delete use an in-memory map instead of the OS
// keychain. SIDs survive within the current process but are gone on restart
// — so the TUI shows the login screen on every launch.
func keyringDisabled() bool {
	return os.Getenv("STAKE_KEYRING_DISABLE") != ""
}

func SetSID(accountID, sid string) error {
	if keyringDisabled() {
		memMu.Lock()
		memStore[accountID] = sid
		memMu.Unlock()
		return nil
	}
	return keyring.Set(serviceName, accountID, sid)
}

func GetSID(accountID string) (string, error) {
	if keyringDisabled() {
		memMu.Lock()
		sid, ok := memStore[accountID]
		memMu.Unlock()
		if !ok {
			return "", keyring.ErrNotFound
		}
		return sid, nil
	}
	return keyring.Get(serviceName, accountID)
}

func DeleteSID(accountID string) error {
	if keyringDisabled() {
		memMu.Lock()
		delete(memStore, accountID)
		memMu.Unlock()
		return nil
	}
	return keyring.Delete(serviceName, accountID)
}

func GetActiveSID() (string, error) {
	if sid := os.Getenv("STAKE_SID"); sid != "" {
		return sid, nil
	}

	cfg, err := LoadConfig()
	if err != nil {
		return "", err
	}
	active := cfg.ActiveAccount()
	if active == nil {
		return "", ErrAccountNotFound
	}
	return GetSID(active.ID)
}
