package auth

import (
	"os"

	"github.com/zalando/go-keyring"
)

const serviceName = "stakecli"

func SetSID(accountID, sid string) error {
	return keyring.Set(serviceName, accountID, sid)
}

func GetSID(accountID string) (string, error) {
	return keyring.Get(serviceName, accountID)
}

func DeleteSID(accountID string) error {
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
