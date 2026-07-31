package upload

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const uploadPreferencesVersion = 1

type uploadPreferences struct {
	Version               int  `json:"version"`
	PublishAfterUpload    bool `json:"publishAfterUpload"`
	ChangeApprovalVersion bool `json:"changeApprovalVersion"`
}

var uploadPreferencesMu sync.Mutex

func defaultUploadPreferences() uploadPreferences {
	return uploadPreferences{
		Version:            uploadPreferencesVersion,
		PublishAfterUpload: true,
	}
}

func uploadPreferencesPath() (string, error) {
	if override := os.Getenv("STAKE_CONFIG_DIR"); override != "" {
		return filepath.Join(override, "upload-preferences.json"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("config dir: %w", err)
	}
	return filepath.Join(dir, "stakecli", "upload-preferences.json"), nil
}

func loadUploadPreferences() (uploadPreferences, string, error) {
	uploadPreferencesMu.Lock()
	defer uploadPreferencesMu.Unlock()

	preferences := defaultUploadPreferences()
	path, err := uploadPreferencesPath()
	if err != nil {
		return preferences, "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return preferences, path, nil
		}
		return preferences, path, fmt.Errorf("reading upload preferences: %w", err)
	}
	if err := json.Unmarshal(data, &preferences); err != nil {
		return defaultUploadPreferences(), path, fmt.Errorf("parsing upload preferences: %w", err)
	}
	if preferences.Version != uploadPreferencesVersion {
		return defaultUploadPreferences(), path, fmt.Errorf("unsupported upload preferences version %d", preferences.Version)
	}
	if preferences.ChangeApprovalVersion {
		preferences.PublishAfterUpload = true
	}
	return preferences, path, nil
}

func saveUploadPreferences(preferences uploadPreferences) (string, error) {
	uploadPreferencesMu.Lock()
	defer uploadPreferencesMu.Unlock()

	preferences.Version = uploadPreferencesVersion
	if preferences.ChangeApprovalVersion {
		preferences.PublishAfterUpload = true
	}
	path, err := uploadPreferencesPath()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return path, fmt.Errorf("creating upload preferences directory: %w", err)
	}
	file, err := os.CreateTemp(dir, ".upload-preferences-*.json")
	if err != nil {
		return path, fmt.Errorf("creating temporary upload preferences: %w", err)
	}
	tempPath := file.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return path, fmt.Errorf("securing upload preferences: %w", err)
	}
	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(preferences); err != nil {
		_ = file.Close()
		return path, fmt.Errorf("encoding upload preferences: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return path, fmt.Errorf("syncing upload preferences: %w", err)
	}
	if err := file.Close(); err != nil {
		return path, fmt.Errorf("closing upload preferences: %w", err)
	}
	if err := replacePreferencesFile(tempPath, path); err != nil {
		return path, fmt.Errorf("installing upload preferences: %w", err)
	}
	return path, nil
}
