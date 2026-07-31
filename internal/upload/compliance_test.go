package upload

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type complianceFixtureMode struct {
	name string
	cost float64
	rows []string
}

func TestRunMathComplianceCompleteTwoStarAndThreeStarPass(t *testing.T) {
	dir := writeComplianceFixture(t, []complianceFixtureMode{
		{name: "base", cost: 1, rows: []string{"0,900,0", "1,100,967"}},
		{name: "bonus", cost: 100, rows: []string{"0,900,0", "1,100,96700"}},
	})
	result := RunMathCompliance(dir)

	if result.Status != CompliancePass || result.HasError || !result.Complete {
		t.Fatalf("expected complete pass, got status=%s error=%v complete=%v checks=%#v", result.Status, result.HasError, result.Complete, failingChecks(result.Checks))
	}
	if len(result.GameSummary.EligibleTiers) != 2 {
		t.Fatalf("expected both eligible tiers, got %v", result.GameSummary.EligibleTiers)
	}
	if len(result.TierResults) != 2 ||
		result.TierResults[0].MaxCompliantBetLevel != 200 ||
		result.TierResults[1].MaxCompliantBetLevel != 1000 {
		t.Fatalf("unexpected maximum compliant bet levels: %#v", result.TierResults)
	}
	if len(result.ModeSummaries) != 2 || !almostEqual(result.ModeSummaries[0].RTP, 96.7, 1e-9) || !almostEqual(result.ModeSummaries[1].NormalizedMaxWin, 9.67, 1e-9) {
		t.Fatalf("unexpected mode summaries: %#v", result.ModeSummaries)
	}
}

func TestRunMathComplianceAnalyzesBothTiersWithoutConfigurationInput(t *testing.T) {
	dir := writeComplianceFixture(t, []complianceFixtureMode{
		{name: "base", cost: 1, rows: []string{"0,900,0", "1,100,967"}},
	})
	result := RunMathCompliance(dir)

	if result.HasError {
		t.Fatalf("automatic tier analysis must not fail hard checks: %#v", failingChecks(result.Checks))
	}
	if !result.Complete || result.Status != CompliancePass {
		t.Fatalf("expected complete math-only report, got complete=%v status=%s", result.Complete, result.Status)
	}
	missing := 0
	for _, check := range result.Checks {
		if check.Status == ComplianceNotEvaluated {
			missing++
		}
	}
	if missing != 0 {
		t.Fatalf("expected no required platform checks, got %d", missing)
	}
	if len(result.TierResults) != 2 ||
		result.TierResults[0].Tier != "2-star" || result.TierResults[0].MaxCompliantBetLevel != 200 ||
		result.TierResults[1].Tier != "3-star" || result.TierResults[1].MaxCompliantBetLevel != 1000 {
		t.Fatalf("unexpected tier reports: %#v", result.TierResults)
	}
	if err := result.ValidateForPublish(); err != nil {
		t.Fatalf("math-only tier report should be publishable: %v", err)
	}
}

func TestTierVerdictExcludesMonetaryInputsAndReportsSafeCaps(t *testing.T) {
	dir := writeComplianceFixture(t, []complianceFixtureMode{
		{name: "base", cost: 1, rows: []string{"0,900,0", "1,100,967"}},
		{name: "buy", cost: 1_000, rows: []string{"0,900,0", "1,100,967000"}},
	})
	result := RunMathCompliance(dir)

	if len(result.TierResults) != 2 {
		t.Fatalf("expected both tier reports: %#v", result.TierResults)
	}
	if result.TierResults[0].MaxCompliantBetLevel != 100 || result.TierResults[1].MaxCompliantBetLevel != 500 {
		t.Fatalf("unexpected safe caps: %#v", result.TierResults)
	}
	if result.TierResults[0].Status != CompliancePass || result.TierResults[1].Status != CompliancePass {
		t.Fatalf("monetary inputs incorrectly affected the verdict: %#v", result.TierResults)
	}
	for _, check := range result.Checks {
		if check.Name == "Bet level" || check.Name == "Max exposure" || check.Name == "Max bet cost" {
			t.Fatalf("excluded monetary check leaked into verdict: %#v", check)
		}
	}
}

func TestTierMaxPayoutUsesAbsoluteBaseStakePayout(t *testing.T) {
	dir := writeComplianceFixture(t, []complianceFixtureMode{
		{name: "base", cost: 1, rows: []string{"0,900,0", "1,100,967"}},
		{name: "buy", cost: 1_000, rows: []string{"0,950000,0", "1,17767,1", "2,32233,3000000"}},
	})
	result := RunMathCompliance(dir)

	if !hasModeTierCheckStatus(result.Checks, "tier.2-star.max_payout_multiplier", "buy", "2-star", ComplianceFail) {
		t.Fatalf("2-star did not reject the 30,000x absolute payout: %#v", result.Checks)
	}
	if !hasModeTierCheckStatus(result.Checks, "tier.3-star.max_payout_multiplier", "buy", "3-star", CompliancePass) {
		t.Fatalf("3-star did not accept the 30,000x absolute payout: %#v", result.Checks)
	}
}

func TestMaxCompliantBetLevelUsesExposureAndCostMinimum(t *testing.T) {
	result := ComplianceResult{
		Policy:      CurrentCompliancePolicy(),
		Checks:      make([]ComplianceCheck, 0),
		TierResults: make([]ComplianceTierResult, 0),
		GameSummary: ComplianceGameSummary{EligibleTiers: make([]string, 0)},
		Complete:    true,
	}
	modes := []analyzedMode{{
		definition: mathIndexMode{Name: "high_max", Cost: 1},
		stats: &lutStats{
			MaxPayoutBaseBet: 80_000,
			Volatility:       1,
		},
	}}

	evaluateTiers(&result, modes)

	if len(result.TierResults) != 2 {
		t.Fatalf("unexpected tier results: %#v", result.TierResults)
	}
	if !almostEqual(result.TierResults[0].MaxCompliantBetLevel, 187.5, 1e-12) {
		t.Fatalf("2-star exposure cap = %v", result.TierResults[0].MaxCompliantBetLevel)
	}
	if !almostEqual(result.TierResults[1].MaxCompliantBetLevel, 625, 1e-12) {
		t.Fatalf("3-star exposure cap = %v", result.TierResults[1].MaxCompliantBetLevel)
	}
}

func TestHardRTPUpperBoundary(t *testing.T) {
	tests := []struct {
		name     string
		rows     []string
		wantFail bool
	}{
		{name: "boundary passes", rows: []string{"0,900,0", "1,100,967"}, wantFail: false},
		{name: "floating point noise at boundary passes", rows: []string{"0,299999,96", "1,700001,97"}, wantFail: false},
		{name: "next hundredth fails", rows: []string{"0,29,96", "1,71,97"}, wantFail: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := writeComplianceFixture(t, []complianceFixtureMode{
				{name: "base", cost: 1, rows: test.rows},
			})
			result := RunMathCompliance(dir)
			failed := hasCheckStatus(result.Checks, "hard.mode.rtp_range", ComplianceFail)
			if failed != test.wantFail {
				t.Fatalf("rtp failure=%v, want %v; checks=%#v", failed, test.wantFail, result.Checks)
			}
		})
	}
}

func TestHardNonZeroHitRateBoundary(t *testing.T) {
	tests := []struct {
		name     string
		zero     string
		win      string
		payout   string
		wantFail bool
	}{
		{name: "five percent passes", zero: "95", win: "5", payout: "1900", wantFail: false},
		{name: "below five percent fails", zero: "96", win: "4", payout: "2375", wantFail: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := writeComplianceFixture(t, []complianceFixtureMode{
				{name: "base", cost: 1, rows: []string{"0," + test.zero + ",0", "1," + test.win + "," + test.payout}},
			})
			result := RunMathCompliance(dir)
			failed := hasCheckStatus(result.Checks, "hard.mode.non_zero_hit_rate", ComplianceFail)
			if failed != test.wantFail {
				t.Fatalf("hit-rate failure=%v, want %v", failed, test.wantFail)
			}
		})
	}
}

func TestHardMaxWinAchievabilityBoundary(t *testing.T) {
	tests := []struct {
		name       string
		zeroWeight string
		wantFail   bool
	}{
		{name: "one in twenty million passes", zeroWeight: "19999999", wantFail: false},
		{name: "rarer beyond report epsilon fails", zeroWeight: "20001000", wantFail: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := writeComplianceFixture(t, []complianceFixtureMode{
				{name: "base", cost: 1, rows: []string{"0," + test.zeroWeight + ",0", "1,1,1920000000"}},
			})
			result := RunMathCompliance(dir)
			failed := hasCheckStatus(result.Checks, "hard.mode.max_win_achievability", ComplianceFail)
			if failed != test.wantFail {
				t.Fatalf("max-win failure=%v, want %v", failed, test.wantFail)
			}
		})
	}
}

func TestCrossModeRTPDeviationIsMeasuredFromGameMean(t *testing.T) {
	tests := []struct {
		name         string
		secondPayout string
		wantFail     bool
	}{
		{name: "half point deviation passes", secondPayout: "910", wantFail: false},
		{name: "over half point deviation fails", secondPayout: "911", wantFail: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dir := writeComplianceFixture(t, []complianceFixtureMode{
				{name: "base", cost: 1, rows: []string{"0,900,0", "1,100,900"}},
				{name: "bonus", cost: 1, rows: []string{"0,900,0", "1,100," + test.secondPayout}},
			})
			result := RunMathCompliance(dir)
			failed := hasCheckStatus(result.Checks, "hard.cross_mode_rtp", ComplianceFail)
			if failed != test.wantFail {
				t.Fatalf("cross-mode failure=%v, want %v", failed, test.wantFail)
			}
		})
	}
}

func TestTailCVaRAndETLMetricsUseNormalizedPayout(t *testing.T) {
	stats := calculateLUTStats(3, 1000, map[uint64]uint64{
		0:         998,
		500_000:   1,
		1_000_000: 1,
	}, 1)

	if !almostEqual(stats.TailProbability5000, 0.002, 1e-12) {
		t.Fatalf("tail 5000 = %.12f", stats.TailProbability5000)
	}
	if !almostEqual(stats.TailProbability10000, 0.001, 1e-12) {
		t.Fatalf("tail 10000 = %.12f", stats.TailProbability10000)
	}
	if !almostEqual(stats.CVaR001, 10_000, 1e-9) {
		t.Fatalf("CVaR = %.12f", stats.CVaR001)
	}
	if !almostEqual(stats.ETL40, 1, 1e-12) || !almostEqual(stats.ETL10000, 2.0/3.0, 1e-12) {
		t.Fatalf("ETL40=%.12f ETL10000=%.12f", stats.ETL40, stats.ETL10000)
	}

	normalized := calculateLUTStats(3, 1000, map[uint64]uint64{
		0:           998,
		50_000_000:  1,
		100_000_000: 1,
	}, 100)
	if !almostEqual(normalized.TailProbability5000, stats.TailProbability5000, 1e-12) || !almostEqual(normalized.CVaR001, stats.CVaR001, 1e-9) {
		t.Fatalf("cost normalization changed equivalent metrics: %#v vs %#v", normalized, stats)
	}

	etlBoundary := calculateLUTStats(2, 2, map[uint64]uint64{
		0:    1,
		4000: 1,
	}, 1)
	if !almostEqual(etlBoundary.ETL40, 1, 1e-12) {
		t.Fatalf("40x payout must be included in ETL40, got %.12f", etlBoundary.ETL40)
	}
}

func TestRunMathComplianceRejectsFileReferenceOutsideBundle(t *testing.T) {
	dir := t.TempDir()
	index := map[string]any{"modes": []map[string]any{{
		"name": "base", "cost": 1, "events": "../events.jsonl", "weights": "../weights.csv",
	}}}
	writeJSON(t, filepath.Join(dir, "index.json"), index)
	result := RunMathCompliance(dir)

	if !result.HasError || !hasCheckStatus(result.Checks, "mode.weights.path", ComplianceFail) || !hasCheckStatus(result.Checks, "mode.events.path", ComplianceFail) {
		t.Fatalf("expected escaping references to fail: %#v", result.Checks)
	}
}

func writeComplianceFixture(t *testing.T, modes []complianceFixtureMode) string {
	t.Helper()
	dir := t.TempDir()
	indexModes := make([]map[string]any, 0, len(modes))
	for _, mode := range modes {
		events := mode.name + "_events.jsonl"
		weights := mode.name + "_weights.csv"
		if err := os.WriteFile(filepath.Join(dir, events), []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write events: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, weights), []byte(strings.Join(mode.rows, "\n")+"\n"), 0o600); err != nil {
			t.Fatalf("write weights: %v", err)
		}
		indexModes = append(indexModes, map[string]any{"name": mode.name, "cost": mode.cost, "events": events, "weights": weights})
	}
	writeJSON(t, filepath.Join(dir, "index.json"), map[string]any{"modes": indexModes})
	return dir
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func hasCheckStatus(checks []ComplianceCheck, code string, status ComplianceStatus) bool {
	for _, check := range checks {
		if check.Code == code && check.Status == status {
			return true
		}
	}
	return false
}

func hasModeTierCheckStatus(checks []ComplianceCheck, code, mode, tier string, status ComplianceStatus) bool {
	for _, check := range checks {
		if check.Code == code && check.Mode == mode && check.Tier == tier && check.Status == status {
			return true
		}
	}
	return false
}

func failingChecks(checks []ComplianceCheck) []ComplianceCheck {
	failures := make([]ComplianceCheck, 0)
	for _, check := range checks {
		if check.Status == ComplianceFail || check.Status == ComplianceWarn || check.Status == ComplianceNotEvaluated {
			failures = append(failures, check)
		}
	}
	return failures
}

func almostEqual(left, right, tolerance float64) bool {
	return math.Abs(left-right) <= tolerance
}
