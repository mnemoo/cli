package upload

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUploadPreferencesDefaultToPublish(t *testing.T) {
	t.Setenv("STAKE_CONFIG_DIR", t.TempDir())
	preferences, path, err := loadUploadPreferences()
	if err != nil {
		t.Fatalf("load defaults: %v", err)
	}
	if !preferences.PublishAfterUpload || preferences.ChangeApprovalVersion {
		t.Fatalf("unexpected defaults: %#v", preferences)
	}
	if filepath.Base(path) != "upload-preferences.json" {
		t.Fatalf("unexpected path: %s", path)
	}
}

func TestUploadPreferencesRoundTrip(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("STAKE_CONFIG_DIR", dir)
	path, err := saveUploadPreferences(uploadPreferences{
		PublishAfterUpload:    false,
		ChangeApprovalVersion: true,
	})
	if err != nil {
		t.Fatalf("save preferences: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat preferences: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("preferences permissions = %o, want 600", info.Mode().Perm())
	}
	loaded, _, err := loadUploadPreferences()
	if err != nil {
		t.Fatalf("reload preferences: %v", err)
	}
	if !loaded.PublishAfterUpload || !loaded.ChangeApprovalVersion {
		t.Fatalf("dependency normalization was not preserved: %#v", loaded)
	}
}
