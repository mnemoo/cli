package auth

import "fmt"

func Init() (*User, bool, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, false, fmt.Errorf("loading config: %w", err)
	}

	active := cfg.ActiveAccount()
	if active == nil {
		return nil, true, nil
	}

	sid, err := GetSID(active.ID)
	if err != nil {
		return nil, true, nil
	}

	user, err := ValidateSession(sid)
	if err != nil {
		return nil, true, nil
	}

	cfg.UpsertAccount(user)
	_ = SaveConfig(cfg)

	return user, false, nil
}

func Login(sid string) (*User, error) {
	user, err := ValidateSession(sid)
	if err != nil {
		return nil, fmt.Errorf("validating session: %w", err)
	}

	if err := SetSID(user.ID, sid); err != nil {
		return nil, fmt.Errorf("saving to keyring: %w", err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	cfg.UpsertAccount(user)
	cfg.Active = user.ID

	if err := SaveConfig(cfg); err != nil {
		return nil, fmt.Errorf("saving config: %w", err)
	}

	return user, nil
}

func Logout(accountID string) error {
	_ = DeleteSID(accountID)

	cfg, err := LoadConfig()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	cfg.RemoveAccount(accountID)

	if err := SaveConfig(cfg); err != nil {
		return fmt.Errorf("saving config: %w", err)
	}

	return nil
}

func SwitchAccount(accountID string) (*User, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}

	acc := cfg.FindAccount(accountID)
	if acc == nil {
		return nil, ErrAccountNotFound
	}

	sid, err := GetSID(accountID)
	if err != nil {
		return nil, fmt.Errorf("reading keyring: %w", err)
	}

	user, err := ValidateSession(sid)
	if err != nil {
		return nil, fmt.Errorf("validating session: %w", err)
	}

	cfg.UpsertAccount(user)
	cfg.Active = user.ID

	if err := SaveConfig(cfg); err != nil {
		return nil, fmt.Errorf("saving config: %w", err)
	}

	return user, nil
}

func ListAccounts() ([]Account, string, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, "", fmt.Errorf("loading config: %w", err)
	}
	return cfg.Accounts, cfg.Active, nil
}
