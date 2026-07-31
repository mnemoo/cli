package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mnemoo/cli/internal/upload"
)

func TestParseComplianceCommandOptionsAcceptsPathAndFlagsInAnyOrder(t *testing.T) {
	options, help, err := parseComplianceCommandOptions([]string{
		".", "--base-mode=base", "--json",
	})
	if err != nil || help {
		t.Fatalf("parse options: help=%v err=%v", help, err)
	}
	if options.path != "." || !options.jsonOutput || options.compliance.BaseMode != "base" {
		t.Fatalf("unexpected options: %#v", options)
	}
}

func TestParseComplianceCommandOptionsRejectsInvalidArguments(t *testing.T) {
	tests := [][]string{
		{},
		{".", "./other"},
		{".", "--unknown"},
		{".", "--bet-level", "500"},
		{".", "--base-mode="},
		{".", "--base-mode", "--json"},
	}
	for _, args := range tests {
		if _, _, err := parseComplianceCommandOptions(args); err == nil {
			t.Fatalf("expected error for arguments %q", args)
		}
	}
}

func TestRunCompliancePrintsHumanReportWithoutAuthentication(t *testing.T) {
	dir := writeCLIComplianceFixture(t, "967")
	var stdout, stderr bytes.Buffer
	if err := runCompliance([]string{dir}, &stdout, &stderr); err != nil {
		t.Fatalf("run compliance: %v", err)
	}
	output := stdout.String()
	for _, expected := range []string{"Math compliance:", "Result: PASS", "2-star: PASS", "max compliant bet level 200.00x", "3-star: PASS", "max compliant bet level 1000.00x", "Bet level and currency are excluded from the verdict.", "Modes: 1"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("output does not contain %q:\n%s", expected, output)
		}
	}
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
}

func TestRunComplianceJSONReturnsCompleteTypedResult(t *testing.T) {
	dir := writeCLIComplianceFixture(t, "967")
	var stdout, stderr bytes.Buffer
	err := runCompliance([]string{
		dir, "--json",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("run compliance: %v", err)
	}
	var result upload.ComplianceResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode JSON output: %v\n%s", err, stdout.String())
	}
	if result.Status != upload.CompliancePass || result.HasError || !result.Complete {
		t.Fatalf("unexpected result: status=%s error=%v complete=%v", result.Status, result.HasError, result.Complete)
	}
	if len(result.GameSummary.EligibleTiers) != 2 {
		t.Fatalf("unexpected eligible tiers: %v", result.GameSummary.EligibleTiers)
	}
}

func TestRunComplianceReturnsFailureForBlockingViolation(t *testing.T) {
	dir := writeCLIComplianceFixture(t, "980")
	var stdout, stderr bytes.Buffer
	err := runCompliance([]string{dir}, &stdout, &stderr)
	if !errors.Is(err, ErrComplianceFailed) {
		t.Fatalf("got %v, want ErrComplianceFailed", err)
	}
	if !strings.Contains(stdout.String(), "Result: FAIL") {
		t.Fatalf("missing failure result:\n%s", stdout.String())
	}
}

func writeCLIComplianceFixture(t *testing.T, winnerPayout string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"index.json":   `{"modes":[{"name":"base","cost":1,"events":"events.jsonl","weights":"weights.csv"}]}`,
		"events.jsonl": "{}\n",
		"weights.csv":  "0,900,0\n1,100," + winnerPayout + "\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("write fixture %s: %v", name, err)
		}
	}
	return dir
}
