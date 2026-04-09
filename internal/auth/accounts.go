package auth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

var ErrSessionExpired = errors.New("session expired")
var ErrAccountNotFound = errors.New("account not found")

type Account struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
	Image string `json:"image"`
}

type AccountsConfig struct {
	Active   string    `json:"active"`
	Accounts []Account `json:"accounts"`
}

var (
	configMu   sync.Mutex
	configDir  string
	configFile string
)

func configPath() (string, error) {
	if configFile != "" {
		return configFile, nil
	}

	// STAKE_CONFIG_DIR is an absolute override — useful for CI, container
	// deployments, and recording demos against the mock API without
	// touching the user's real config under ~/Library/Application Support
	// (macOS) / ~/.config (Linux) / %AppData% (Windows).
	if override := os.Getenv("STAKE_CONFIG_DIR"); override != "" {
		configDir = override
		configFile = filepath.Join(configDir, "accounts.json")
		return configFile, nil
	}

	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config dir: %w", err)
	}

	configDir = filepath.Join(dir, "stakecli")
	configFile = filepath.Join(configDir, "accounts.json")
	return configFile, nil
}

func LoadConfig() (*AccountsConfig, error) {
	configMu.Lock()
	defer configMu.Unlock()

	path, err := configPath()
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &AccountsConfig{}, nil
		}
		return nil, fmt.Errorf("reading config: %w", err)
	}

	var cfg AccountsConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}
	return &cfg, nil
}

func SaveConfig(cfg *AccountsConfig) error {
	configMu.Lock()
	defer configMu.Unlock()

	path, err := configPath()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling config: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	return nil
}

func (cfg *AccountsConfig) FindAccount(id string) *Account {
	for i := range cfg.Accounts {
		if cfg.Accounts[i].ID == id {
			return &cfg.Accounts[i]
		}
	}
	return nil
}

func (cfg *AccountsConfig) UpsertAccount(user *User) {
	for i := range cfg.Accounts {
		if cfg.Accounts[i].ID == user.ID {
			cfg.Accounts[i].Name = user.Name
			cfg.Accounts[i].Email = user.Email
			cfg.Accounts[i].Image = user.Image
			return
		}
	}
	cfg.Accounts = append(cfg.Accounts, Account{
		ID:    user.ID,
		Name:  user.Name,
		Email: user.Email,
		Image: user.Image,
	})
}

func (cfg *AccountsConfig) RemoveAccount(id string) {
	for i := range cfg.Accounts {
		if cfg.Accounts[i].ID == id {
			cfg.Accounts = append(cfg.Accounts[:i], cfg.Accounts[i+1:]...)
			break
		}
	}
	if cfg.Active == id {
		cfg.Active = ""
		if len(cfg.Accounts) > 0 {
			cfg.Active = cfg.Accounts[0].ID
		}
	}
}

func (cfg *AccountsConfig) ActiveAccount() *Account {
	if cfg.Active == "" {
		return nil
	}
	return cfg.FindAccount(cfg.Active)
}
