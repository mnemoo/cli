package upload

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
)

const (
	ComplianceSchemaVersion = "1.1"
	complianceMethod        = "moptimizer_compliance_rs_and_optimization_guideline_v1"
	complianceScope         = "final_integer_weighted_lut"
)

type ComplianceStatus string

const (
	CompliancePass         ComplianceStatus = "pass"
	ComplianceWarn         ComplianceStatus = "warn"
	ComplianceFail         ComplianceStatus = "fail"
	ComplianceInfo         ComplianceStatus = "info"
	ComplianceNotEvaluated ComplianceStatus = "not_evaluated"
)

type ComplianceCheck struct {
	Code    string           `json:"code"`
	Name    string           `json:"name"`
	Scope   string           `json:"scope"`
	Mode    string           `json:"mode,omitempty"`
	Tier    string           `json:"tier,omitempty"`
	Status  ComplianceStatus `json:"status"`
	Details string           `json:"details"`
	Hard    bool             `json:"hard"`
	Actual  *float64         `json:"actual,omitempty"`
	Minimum *float64         `json:"minimum,omitempty"`
	Maximum *float64         `json:"maximum,omitempty"`
	Unit    string           `json:"unit,omitempty"`
}

type ComplianceOptions struct {
	BaseMode string `json:"base_mode,omitempty"`
}

type ComplianceResult struct {
	SchemaVersion  string                  `json:"schema_version"`
	Method         string                  `json:"method"`
	Scope          string                  `json:"scope"`
	Policy         CompliancePolicy        `json:"policy"`
	Status         ComplianceStatus        `json:"status"`
	Complete       bool                    `json:"complete"`
	Checks         []ComplianceCheck       `json:"checks"`
	ModeSummaries  []ComplianceModeSummary `json:"mode_summaries"`
	GameSummary    ComplianceGameSummary   `json:"game_summary"`
	TierResults    []ComplianceTierResult  `json:"tier_results"`
	HasError       bool                    `json:"has_error"`
	WarningsCount  int                     `json:"warnings_count"`
	FailuresCount  int                     `json:"failures_count"`
	ExcludedInputs []string                `json:"excluded_inputs"`
}

type ComplianceModeSummary struct {
	Name                 string           `json:"name"`
	Kind                 string           `json:"kind"`
	Cost                 float64          `json:"cost"`
	IsBase               bool             `json:"is_base"`
	HasStats             bool             `json:"has_stats"`
	Status               ComplianceStatus `json:"status"`
	RTP                  float64          `json:"rtp"`
	RTPDeviationPP       float64          `json:"rtp_deviation_pp"`
	Volatility           float64          `json:"volatility"`
	VolatilityTag        string           `json:"volatility_tag"`
	HitRate              float64          `json:"hit_rate"`
	MaxWin               float64          `json:"max_win"`
	NormalizedMaxWin     float64          `json:"normalized_max_win"`
	MaxWinProbability    float64          `json:"max_win_probability"`
	MaxWinHitRate        string           `json:"max_win_hit_rate"`
	TailProbability5000  float64          `json:"tail_probability_5000"`
	TailProbability10000 float64          `json:"tail_probability_10000"`
	CVaR001              float64          `json:"cvar_0_1_percent"`
	ETL40                float64          `json:"etl_40"`
	ETL10000             float64          `json:"etl_10000"`
	ETLSum               float64          `json:"etl_sum"`
	SimCount             int              `json:"sim_count"`
	TotalWeight          uint64           `json:"total_weight"`
}

type ComplianceGameSummary struct {
	BaseMode                string   `json:"base_mode,omitempty"`
	MeanRTP                 float64  `json:"mean_rtp"`
	CrossModeMaxDeviationPP float64  `json:"cross_mode_max_deviation_pp"`
	MaxCostMultiplier       float64  `json:"max_cost_multiplier"`
	MaxPayoutMultiplier     float64  `json:"max_payout_multiplier"`
	MaxPayoutBaseBet        float64  `json:"max_payout_base_bet"`
	BaseVolatility          float64  `json:"base_volatility"`
	TailProbability5000     float64  `json:"tail_probability_5000"`
	TailProbability10000    float64  `json:"tail_probability_10000"`
	CVaR001                 float64  `json:"cvar_0_1_percent"`
	ETL40                   float64  `json:"etl_40"`
	ETL10000                float64  `json:"etl_10000"`
	ETLSum                  float64  `json:"etl_sum"`
	EligibleTiers           []string `json:"eligible_tiers"`
	DirectBonusModes        int      `json:"direct_bonus_modes"`
	MysteryModes            int      `json:"mystery_modes"`
	AmbiguousBuyModes       int      `json:"ambiguous_buy_modes"`
}

type ComplianceTierResult struct {
	Tier                 string           `json:"tier"`
	Status               ComplianceStatus `json:"status"`
	LUTMathStatus        ComplianceStatus `json:"lut_math_status"`
	Complete             bool             `json:"complete"`
	Eligible             bool             `json:"eligible"`
	MaxCompliantBetLevel float64          `json:"max_compliant_bet_level"`
	CheckCodes           []string         `json:"check_codes"`
}

type mathIndex struct {
	Modes []mathIndexMode `json:"modes"`
}

type mathIndexMode struct {
	Name    string  `json:"name"`
	Cost    float64 `json:"cost"`
	Events  string  `json:"events"`
	Weights string  `json:"weights"`
}

type analyzedMode struct {
	definition mathIndexMode
	stats      *lutStats
}

func RunMathCompliance(dirPath string) ComplianceResult {
	return RunMathComplianceWithOptions(dirPath, ComplianceOptions{})
}

func RunMathComplianceWithOptions(dirPath string, options ComplianceOptions) ComplianceResult {
	result := ComplianceResult{
		SchemaVersion: ComplianceSchemaVersion,
		Method:        complianceMethod,
		Scope:         complianceScope,
		Policy:        CurrentCompliancePolicy(),
		Status:        CompliancePass,
		Complete:      true,
		Checks:        make([]ComplianceCheck, 0),
		ModeSummaries: make([]ComplianceModeSummary, 0),
		TierResults:   make([]ComplianceTierResult, 0),
		ExcludedInputs: []string{
			"Maximum configured platform bet level",
			"Currency context for Max Exposure and Max Bet Cost",
			"Bonus-trigger and shape/event metadata for Base and Ante modes",
			"Mystery 50/30/10/10 event split where a Mystery mode exists",
			"Bonus-buy 100% hit rate and minimum-return preferences",
		},
	}

	root, err := filepath.Abs(dirPath)
	if err != nil {
		result.addFail("bundle.path", "Bundle path", "bundle", "", "", true, fmt.Sprintf("Cannot resolve bundle path: %v", err))
		result.finalize()
		return result
	}

	indexPath, err := resolveBundleFile(root, "index.json")
	if err != nil {
		result.addFail("bundle.index.path", "index.json", "bundle", "", "", true, err.Error())
		result.finalize()
		return result
	}
	data, err := os.ReadFile(indexPath)
	if err != nil {
		result.addFail("bundle.index.exists", "index.json", "bundle", "", "", true, "File not found in "+root)
		result.finalize()
		return result
	}
	result.addPass("bundle.index.exists", "index.json", "bundle", "", "", true, "Found")

	var idx mathIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		result.addFail("bundle.index.parse", "index.json parse", "bundle", "", "", true, fmt.Sprintf("Invalid JSON: %v", err))
		result.finalize()
		return result
	}
	if len(idx.Modes) == 0 {
		result.addFail("bundle.modes.present", "Bet modes", "bundle", "", "", true, "No bet modes defined")
		result.finalize()
		return result
	}
	result.addPass("bundle.modes.present", "Bet modes", "bundle", "", "", true, fmt.Sprintf("%d mode(s) defined", len(idx.Modes)))

	baseMode, foundBase := selectBaseMode(idx.Modes, options.BaseMode)
	if options.BaseMode != "" && !foundBase {
		result.addWarn("bundle.base_mode", "Base mode", "bundle", "", "", false, fmt.Sprintf("Display hint %q is not present; using automatic selection", options.BaseMode))
		baseMode, _ = selectBaseMode(idx.Modes, "")
	} else if foundBase {
		result.addInfo("bundle.base_mode", "Base mode", "bundle", baseMode, "", false, fmt.Sprintf("Using %q as base mode", baseMode))
	}

	seen := make(map[string]struct{}, len(idx.Modes))
	analyzed := make([]analyzedMode, 0, len(idx.Modes))
	for _, mode := range idx.Modes {
		prefix := fmt.Sprintf("Mode %q", mode.Name)
		if strings.TrimSpace(mode.Name) == "" {
			result.addFail("mode.name", "Mode name", "mode", mode.Name, "", true, "Mode name is empty")
			analyzed = append(analyzed, analyzedMode{definition: mode})
			continue
		}
		if _, exists := seen[mode.Name]; exists {
			result.addFail("mode.name.unique", prefix, "mode", mode.Name, "", true, "Duplicate mode name")
			analyzed = append(analyzed, analyzedMode{definition: mode})
			continue
		}
		seen[mode.Name] = struct{}{}

		if mode.Cost <= 0 || math.IsNaN(mode.Cost) || math.IsInf(mode.Cost, 0) {
			result.addFail("mode.cost.positive", prefix+" cost", "mode", mode.Name, "", true, fmt.Sprintf("Invalid cost: %v", mode.Cost))
		} else {
			result.addPassMetric("mode.cost.positive", prefix+" cost", "mode", mode.Name, "", true, mode.Cost, floatPointer(math.SmallestNonzeroFloat64), nil, "base_bet_x", fmt.Sprintf("cost=%.4f", mode.Cost))
		}

		stats := validateModeFiles(&result, root, mode)
		if stats != nil {
			validateHardModeLimits(&result, mode, stats)
		}
		analyzed = append(analyzed, analyzedMode{definition: mode, stats: stats})
	}

	validateCrossMode(&result, analyzed)
	result.ModeSummaries = buildModeSummaries(analyzed, baseMode, result.Checks)
	result.GameSummary = buildGameSummary(analyzed, baseMode)
	evaluateTiers(&result, analyzed)
	result.finalize()
	return result
}

func validateModeFiles(result *ComplianceResult, root string, mode mathIndexMode) *lutStats {
	prefix := fmt.Sprintf("Mode %q", mode.Name)
	if mode.Events == "" {
		result.addFail("mode.events.reference", prefix+" events", "mode", mode.Name, "", true, "Events filename is empty")
	} else if eventsPath, err := resolveBundleFile(root, mode.Events); err != nil {
		result.addFail("mode.events.path", prefix+" events", "mode", mode.Name, "", true, err.Error())
	} else if info, err := os.Stat(eventsPath); err != nil {
		result.addFail("mode.events.exists", prefix+" events", "mode", mode.Name, "", true, fmt.Sprintf("%s not found", mode.Events))
	} else if !info.Mode().IsRegular() {
		result.addFail("mode.events.regular_file", prefix+" events", "mode", mode.Name, "", true, fmt.Sprintf("%s is not a regular file", mode.Events))
	} else {
		result.addPass("mode.events.exists", prefix+" events", "mode", mode.Name, "", true, fmt.Sprintf("%s (%s)", mode.Events, FormatSize(info.Size())))
	}

	if mode.Weights == "" {
		result.addFail("mode.weights.reference", prefix+" weights", "mode", mode.Name, "", true, "Weights filename is empty")
		return nil
	}
	weightsPath, err := resolveBundleFile(root, mode.Weights)
	if err != nil {
		result.addFail("mode.weights.path", prefix+" weights", "mode", mode.Name, "", true, err.Error())
		return nil
	}
	info, err := os.Stat(weightsPath)
	if err != nil {
		result.addFail("mode.weights.exists", prefix+" weights", "mode", mode.Name, "", true, fmt.Sprintf("%s not found", mode.Weights))
		return nil
	}
	if !info.Mode().IsRegular() {
		result.addFail("mode.weights.regular_file", prefix+" weights", "mode", mode.Name, "", true, fmt.Sprintf("%s is not a regular file", mode.Weights))
		return nil
	}
	return validateLUT(result, weightsPath, prefix, mode)
}

func resolveBundleFile(root, reference string) (string, error) {
	if strings.TrimSpace(reference) == "" {
		return "", fmt.Errorf("empty bundle file reference")
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolving bundle root: %w", err)
	}
	candidate := reference
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(rootAbs, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", fmt.Errorf("resolving %q: %w", reference, err)
	}
	rel, err := filepath.Rel(rootAbs, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%q escapes the math bundle root", reference)
	}

	resolvedRoot, rootErr := filepath.EvalSymlinks(rootAbs)
	resolvedCandidate, candidateErr := filepath.EvalSymlinks(candidate)
	if rootErr == nil && candidateErr == nil {
		resolvedRel, relErr := filepath.Rel(resolvedRoot, resolvedCandidate)
		if relErr != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("%q resolves outside the math bundle root", reference)
		}
	}
	return candidate, nil
}

func selectBaseMode(modes []mathIndexMode, requested string) (string, bool) {
	if requested != "" {
		for _, mode := range modes {
			if mode.Name == requested {
				return mode.Name, true
			}
		}
		return "", false
	}
	for _, mode := range modes {
		if strings.EqualFold(mode.Name, "base") {
			return mode.Name, true
		}
	}
	if len(modes) == 0 {
		return "", false
	}
	selected := modes[0]
	for _, mode := range modes[1:] {
		if mode.Cost > 0 && (selected.Cost <= 0 || mode.Cost < selected.Cost) {
			selected = mode
		}
	}
	return selected.Name, selected.Name != ""
}

func buildModeSummaries(modes []analyzedMode, baseMode string, checks []ComplianceCheck) []ComplianceModeSummary {
	meanRTP := complianceMeanRTP(modes)
	summaries := make([]ComplianceModeSummary, 0, len(modes))
	for _, mode := range modes {
		summary := ComplianceModeSummary{
			Name:   mode.definition.Name,
			Kind:   complianceModeKind(mode.definition),
			Cost:   mode.definition.Cost,
			IsBase: mode.definition.Name == baseMode,
			Status: ComplianceFail,
		}
		if mode.stats != nil {
			stats := mode.stats
			summary.HasStats = true
			summary.RTP = stats.RTP
			summary.RTPDeviationPP = math.Abs(complianceRTPPercent(stats.RTP) - meanRTP)
			summary.Volatility = stats.Volatility
			summary.VolatilityTag = classifyVolatility(stats.Volatility)
			summary.HitRate = stats.NonZeroProbability * 100
			summary.MaxWin = stats.MaxPayoutBaseBet
			summary.NormalizedMaxWin = stats.MaxNormalizedPayout
			summary.MaxWinProbability = stats.MaxWinProbability
			summary.MaxWinHitRate = formatOdds(stats.MaxPayoutBaseBet > 0 && stats.MaxPayoutWeight > 0, stats.MaxPayoutWeight, stats.TotalWeight)
			summary.TailProbability5000 = stats.TailProbability5000
			summary.TailProbability10000 = stats.TailProbability10000
			summary.CVaR001 = stats.CVaR001
			summary.ETL40 = stats.ETL40
			summary.ETL10000 = stats.ETL10000
			summary.ETLSum = stats.ETL40 + stats.ETL10000
			summary.SimCount = stats.Entries
			summary.TotalWeight = stats.TotalWeight
			summary.Status = CompliancePass
		}
		for _, check := range checks {
			if check.Mode != mode.definition.Name {
				continue
			}
			if check.Status == ComplianceFail {
				summary.Status = ComplianceFail
				break
			}
			if check.Status == ComplianceWarn || check.Status == ComplianceNotEvaluated {
				summary.Status = ComplianceWarn
			}
		}
		summaries = append(summaries, summary)
	}
	return summaries
}

func buildGameSummary(modes []analyzedMode, baseMode string) ComplianceGameSummary {
	summary := ComplianceGameSummary{
		BaseMode:      baseMode,
		MeanRTP:       complianceMeanRTP(modes),
		EligibleTiers: make([]string, 0),
	}
	for _, mode := range modes {
		switch complianceModeKind(mode.definition) {
		case "direct_bonus":
			summary.DirectBonusModes++
		case "mystery":
			summary.MysteryModes++
		case "ambiguous_buy":
			summary.AmbiguousBuyModes++
		}
		if mode.definition.Cost > summary.MaxCostMultiplier {
			summary.MaxCostMultiplier = mode.definition.Cost
		}
		if mode.stats == nil {
			continue
		}
		stats := mode.stats
		if stats.MaxPayoutBaseBet > summary.MaxPayoutMultiplier {
			summary.MaxPayoutMultiplier = stats.MaxPayoutBaseBet
		}
		if stats.MaxPayoutBaseBet > summary.MaxPayoutBaseBet {
			summary.MaxPayoutBaseBet = stats.MaxPayoutBaseBet
		}
		if mode.definition.Name == baseMode {
			summary.BaseVolatility = stats.Volatility
		}
		summary.TailProbability5000 = math.Max(summary.TailProbability5000, stats.TailProbability5000)
		summary.TailProbability10000 = math.Max(summary.TailProbability10000, stats.TailProbability10000)
		summary.CVaR001 = math.Max(summary.CVaR001, stats.CVaR001)
		summary.ETL40 = math.Max(summary.ETL40, stats.ETL40)
		summary.ETL10000 = math.Max(summary.ETL10000, stats.ETL10000)
		summary.ETLSum = math.Max(summary.ETLSum, stats.ETL40+stats.ETL10000)
		summary.CrossModeMaxDeviationPP = math.Max(
			summary.CrossModeMaxDeviationPP,
			math.Abs(complianceRTPPercent(stats.RTP)-summary.MeanRTP),
		)
	}
	return summary
}

func validateCrossMode(result *ComplianceResult, modes []analyzedMode) {
	meanRTP := complianceMeanRTP(modes)
	for _, mode := range modes {
		if mode.stats == nil {
			continue
		}
		modeRTP := complianceRTPPercent(mode.stats.RTP)
		deviation := math.Abs(modeRTP - meanRTP)
		name := fmt.Sprintf("Mode %q cross-mode RTP consistency", mode.definition.Name)
		details := fmt.Sprintf("Rounded mode RTP %.2f%% differs from game mean %.4f%% by %.4f percentage points", modeRTP, meanRTP, deviation)
		if deviation > crossModeDeviationMax+1e-12 {
			result.addFailMetric("hard.cross_mode_rtp", name, "mode", mode.definition.Name, "", true, deviation, nil, floatPointer(crossModeDeviationMax), "percentage_points", details)
		} else {
			result.addPassMetric("hard.cross_mode_rtp", name, "mode", mode.definition.Name, "", true, deviation, nil, floatPointer(crossModeDeviationMax), "percentage_points", details)
		}
	}
}

func complianceRTPPercent(rtpPercent float64) float64 {
	return math.Round(rtpPercent*100) / 100
}

func complianceMeanRTP(modes []analyzedMode) float64 {
	total := 0.0
	count := 0
	for _, mode := range modes {
		if mode.stats == nil {
			continue
		}
		total += complianceRTPPercent(mode.stats.RTP)
		count++
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func complianceModeKind(mode mathIndexMode) string {
	normalized := strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(mode.Name))
	if mode.Cost < 100-1e-12 {
		return "base_or_ante"
	}
	if strings.Contains(normalized, "mystery") {
		return "mystery"
	}
	if strings.Contains(normalized, "bonus") {
		return "direct_bonus"
	}
	return "ambiguous_buy"
}

func (r *ComplianceResult) addCheck(check ComplianceCheck) {
	r.Checks = append(r.Checks, check)
	switch check.Status {
	case ComplianceFail:
		r.FailuresCount++
		if check.Hard {
			r.HasError = true
		}
	case ComplianceWarn, ComplianceNotEvaluated:
		r.WarningsCount++
		if check.Status == ComplianceNotEvaluated {
			r.Complete = false
		}
	}
}

func (r *ComplianceResult) addPass(code, name, scope, mode, tier string, hard bool, details string) {
	r.addCheck(ComplianceCheck{Code: code, Name: name, Scope: scope, Mode: mode, Tier: tier, Status: CompliancePass, Details: details, Hard: hard})
}

func (r *ComplianceResult) addInfo(code, name, scope, mode, tier string, hard bool, details string) {
	r.addCheck(ComplianceCheck{Code: code, Name: name, Scope: scope, Mode: mode, Tier: tier, Status: ComplianceInfo, Details: details, Hard: hard})
}

func (r *ComplianceResult) addWarn(code, name, scope, mode, tier string, hard bool, details string) {
	r.addCheck(ComplianceCheck{Code: code, Name: name, Scope: scope, Mode: mode, Tier: tier, Status: ComplianceWarn, Details: details, Hard: hard})
}

func (r *ComplianceResult) addFail(code, name, scope, mode, tier string, hard bool, details string) {
	r.addCheck(ComplianceCheck{Code: code, Name: name, Scope: scope, Mode: mode, Tier: tier, Status: ComplianceFail, Details: details, Hard: hard})
}

func (r *ComplianceResult) addPassMetric(code, name, scope, mode, tier string, hard bool, actual float64, minimum, maximum *float64, unit, details string) {
	r.addCheck(ComplianceCheck{Code: code, Name: name, Scope: scope, Mode: mode, Tier: tier, Status: CompliancePass, Details: details, Hard: hard, Actual: floatPointer(actual), Minimum: minimum, Maximum: maximum, Unit: unit})
}

func (r *ComplianceResult) addFailMetric(code, name, scope, mode, tier string, hard bool, actual float64, minimum, maximum *float64, unit, details string) {
	r.addCheck(ComplianceCheck{Code: code, Name: name, Scope: scope, Mode: mode, Tier: tier, Status: ComplianceFail, Details: details, Hard: hard, Actual: floatPointer(actual), Minimum: minimum, Maximum: maximum, Unit: unit})
}

func (r *ComplianceResult) finalize() {
	if r.HasError {
		r.Status = ComplianceFail
		return
	}
	if len(r.TierResults) > 0 && len(r.GameSummary.EligibleTiers) == 0 {
		r.Status = ComplianceFail
		return
	}
	if r.WarningsCount > 0 || !r.Complete {
		r.Status = ComplianceWarn
		return
	}
	r.Status = CompliancePass
}

func (r ComplianceResult) ValidateForPublish() error {
	if r.HasError {
		return fmt.Errorf("math compliance has blocking failures")
	}
	if len(r.GameSummary.EligibleTiers) == 0 {
		return fmt.Errorf("math compliance did not qualify for a supported tier")
	}
	return nil
}

func floatPointer(value float64) *float64 {
	return &value
}
