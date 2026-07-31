package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunUploadYesTreatsIncompleteComplianceAsAdvisory(t *testing.T) {
	dir := writeCLIComplianceFixture(t, "967")
	t.Setenv("STAKE_SID", "test-session")
	err := RunUpload([]string{
		"--team", " ", "--game", "game", "--type", "math", "--path", dir, "--compliance", "--yes",
	})
	if err == nil || !strings.Contains(err.Error(), "planning failed: team and game are required") {
		t.Fatalf("expected advisory compliance to advance to planning, got %v", err)
	}
}

func TestRunUploadYesTreatsFailedComplianceAsAdvisory(t *testing.T) {
	dir := writeCLIComplianceFixture(t, "980")
	t.Setenv("STAKE_SID", "test-session")
	err := RunUpload([]string{
		"--team", " ", "--game", "game", "--type", "math", "--path", dir,
		"--compliance", "--yes",
	})
	if err == nil || !strings.Contains(err.Error(), "planning failed: team and game are required") {
		t.Fatalf("expected advisory compliance to advance to planning, got %v", err)
	}
}

func TestRunUploadHeadlessMathSkipsComplianceByDefault(t *testing.T) {
	dir := writeCLIComplianceFixture(t, "980")
	t.Setenv("STAKE_SID", "test-session")
	err := RunUpload([]string{
		"--team", " ", "--game", "game", "--type", "math", "--path", dir, "--yes",
	})
	if err == nil || !strings.Contains(err.Error(), "planning failed: team and game are required") {
		t.Fatalf("expected unchecked headless upload to advance to planning, got %v", err)
	}
}

func TestRunUploadInteractiveMathFailureCanContinueAfterExtraConfirmation(t *testing.T) {
	dir := writeCLIComplianceFixture(t, "980")
	t.Setenv("STAKE_SID", "test-session")

	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stdin pipe: %v", err)
	}
	previousStdin := os.Stdin
	os.Stdin = readEnd
	t.Cleanup(func() {
		os.Stdin = previousStdin
		_ = readEnd.Close()
	})
	if _, err := writeEnd.WriteString("y\n"); err != nil {
		t.Fatalf("write confirmation: %v", err)
	}
	if err := writeEnd.Close(); err != nil {
		t.Fatalf("close confirmation input: %v", err)
	}

	err = RunUpload([]string{
		"--team", " ", "--game", "game", "--type", "math", "--path", dir,
		"--base-mode", "base",
	})
	if err == nil || !strings.Contains(err.Error(), "planning failed: team and game are required") {
		t.Fatalf("expected accepted override to advance to planning, got %v", err)
	}
}

func TestRunUploadRejectsMathComplianceFlagsForFrontAssets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("ok"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	err := RunUpload([]string{
		"--team", "team", "--game", "game", "--type", "front", "--path", dir,
		"--compliance", "--base-mode", "base", "--yes",
	})
	if err == nil || !strings.Contains(err.Error(), "only valid with --type math") {
		t.Fatalf("expected front compliance flag error, got %v", err)
	}
}

func TestRunUploadApprovalVersionFlagRequiresPublish(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("ok"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	err := RunUpload([]string{
		"--team", "team", "--game", "game", "--type", "front", "--path", dir,
		"--update-approval-version", "--yes",
	})
	if err == nil || !strings.Contains(err.Error(), "requires --publish") {
		t.Fatalf("expected publish requirement, got %v", err)
	}
}
