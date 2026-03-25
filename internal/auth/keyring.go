package auth

import "github.com/zalando/go-keyring"

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
