package accelerator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Profile struct {
	Address     string `json:"address"`
	Fingerprint string `json:"fingerprint"`
}

type Profiles struct {
	Default  string             `json:"default,omitempty"`
	Profiles map[string]Profile `json:"profiles"`
}

var profilesMu sync.Mutex

func profilesPath() (string, error) {
	if override := os.Getenv("STAKE_CONFIG_DIR"); override != "" {
		return filepath.Join(override, "accelerators.json"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locating user config: %w", err)
	}
	return filepath.Join(dir, "stakecli", "accelerators.json"), nil
}

func LoadProfiles() (*Profiles, error) {
	profilesMu.Lock()
	defer profilesMu.Unlock()
	return loadProfilesUnlocked()
}

func loadProfilesUnlocked() (*Profiles, error) {
	path, err := profilesPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Profiles{Profiles: make(map[string]Profile)}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading accelerator profiles: %w", err)
	}
	var profiles Profiles
	if err := json.Unmarshal(data, &profiles); err != nil {
		return nil, fmt.Errorf("parsing accelerator profiles: %w", err)
	}
	if profiles.Profiles == nil {
		profiles.Profiles = make(map[string]Profile)
	}
	return &profiles, nil
}

func SaveProfile(name string, profile Profile, makeDefault bool) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("accelerator profile name is required")
	}
	profilesMu.Lock()
	defer profilesMu.Unlock()
	profiles, err := loadProfilesUnlocked()
	if err != nil {
		return err
	}
	profiles.Profiles[name] = profile
	if makeDefault || profiles.Default == "" {
		profiles.Default = name
	}
	return saveProfilesUnlocked(profiles)
}

func RemoveProfile(name string) error {
	profilesMu.Lock()
	defer profilesMu.Unlock()
	profiles, err := loadProfilesUnlocked()
	if err != nil {
		return err
	}
	if _, ok := profiles.Profiles[name]; !ok {
		return fmt.Errorf("accelerator profile %q does not exist", name)
	}
	delete(profiles.Profiles, name)
	if profiles.Default == name {
		profiles.Default = ""
		for candidate := range profiles.Profiles {
			profiles.Default = candidate
			break
		}
	}
	return saveProfilesUnlocked(profiles)
}

func saveProfilesUnlocked(profiles *Profiles) error {
	path, err := profilesPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating accelerator config directory: %w", err)
	}
	data, err := json.MarshalIndent(profiles, "", "  ")
	if err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".accelerators-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

func ResolveProfile(reference string) (string, string, string, error) {
	profiles, err := LoadProfiles()
	if err != nil {
		return "", "", "", err
	}
	name := strings.TrimSpace(reference)
	if name == "" {
		name = profiles.Default
	}
	if profile, ok := profiles.Profiles[name]; ok {
		return name, profile.Address, profile.Fingerprint, nil
	}
	if strings.Contains(name, ".") || strings.Contains(name, ":") {
		for profileName, profile := range profiles.Profiles {
			if normalizeProfileAddress(profile.Address) == normalizeProfileAddress(name) {
				return profileName, profile.Address, profile.Fingerprint, nil
			}
		}
		return "", name, "", nil
	}
	if name == "" {
		return "", "", "", errors.New("no default accelerator is configured")
	}
	return "", "", "", fmt.Errorf("accelerator profile %q does not exist", name)
}

func AutoProfileName(address string) string {
	value := normalizeProfileAddress(address)
	value = strings.TrimPrefix(value, "https://")
	value = strings.ReplaceAll(value, "[", "")
	value = strings.ReplaceAll(value, "]", "")
	value = strings.ReplaceAll(value, ":", "-")
	value = strings.ReplaceAll(value, ".", "-")
	value = strings.Trim(value, "-")
	if value == "" {
		return "relay"
	}
	return value
}

func normalizeProfileAddress(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	value = strings.TrimSuffix(value, "/")
	return value
}
