package mcpserver

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMLRPreviewAndChangeWithoutFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lut.csv")
	original := []byte("0,900,0\n1,100,967\n")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	server := &Server{}
	_, preview, err := server.mlrPreviewChange(context.Background(), nil, mlrChangeInput{
		Path: path, TargetN: 5,
	})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.Saved || preview.ChangedRows == 0 || preview.Mode != "auto" {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	if current, err := os.ReadFile(path); err != nil || string(current) != string(original) {
		t.Fatalf("preview changed LUT: %v %q", err, current)
	}
	if _, _, err := server.mlrChange(context.Background(), nil, mlrApplyInput{
		Path: path, TargetN: 5,
	}); err == nil {
		t.Fatal("MLR change succeeded without confirm=true")
	}
	_, changed, err := server.mlrChange(context.Background(), nil, mlrApplyInput{
		Path: path, TargetN: 5, Confirm: true,
	})
	if err != nil {
		t.Fatalf("change: %v", err)
	}
	if !changed.Saved || changed.BackupPath == "" {
		t.Fatalf("unexpected change output: %#v", changed)
	}
	if backup, err := os.ReadFile(changed.BackupPath); err != nil || string(backup) != string(original) {
		t.Fatalf("backup mismatch: %v %q", err, backup)
	}
	info, err := os.Stat(changed.BackupPath)
	if err != nil {
		t.Fatalf("stat backup: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup permissions = %v", info.Mode().Perm())
	}
}

func TestParseAnalyticsRangeDefaultsAndRejectsReverseRange(t *testing.T) {
	start, end, err := parseAnalyticsRange("", "")
	if err != nil {
		t.Fatal(err)
	}
	if end.Sub(start) != 30*24*time.Hour {
		t.Fatalf("default range = %s", end.Sub(start))
	}
	if _, _, err := parseAnalyticsRange("2026-02-02T00:00:00Z", "2026-02-01T00:00:00Z"); err == nil {
		t.Fatal("reverse analytics range was accepted")
	}
}
