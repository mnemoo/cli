package upload

import (
	"fmt"
	"math"
)

const (
	compliancePolicyVersion = "2026-07"
	rtpHardMin              = 90.0
	rtpHardMax              = 96.70
	rtpValidationScale      = 100.0
	maxWinOddsHardMax       = 20_000_000.0
	nonZeroHitHardMin       = 0.05
	crossModeDeviationMax   = 0.50
)

type CompliancePolicy struct {
	Version                      string                 `json:"version"`
	RTPMinimum                   float64                `json:"rtp_minimum"`
	RTPMaximum                   float64                `json:"rtp_maximum"`
	MaxWinOddsMaximum            float64                `json:"max_win_odds_maximum"`
	NonZeroHitProbabilityMinimum float64                `json:"non_zero_hit_probability_minimum"`
	CrossModeDeviationMaximum    float64                `json:"cross_mode_deviation_maximum"`
	Tiers                        []ComplianceTierPolicy `json:"tiers"`
}

type ComplianceTierPolicy struct {
	Tier                  string  `json:"tier"`
	BetLevelMaximum       float64 `json:"bet_level_maximum"`
	MaxExposureMaximum    float64 `json:"max_exposure_maximum"`
	MaxPayoutMaximum      float64 `json:"max_payout_multiplier_maximum"`
	MaxBetCostMaximum     float64 `json:"max_bet_cost_maximum"`
	CostMultiplierMaximum float64 `json:"cost_multiplier_maximum"`
	BaseVolatilityMinimum float64 `json:"base_volatility_minimum"`
	BaseVolatilityMaximum float64 `json:"base_volatility_maximum"`
	Tail5000Maximum       float64 `json:"tail_probability_5000_maximum"`
	Tail10000Maximum      float64 `json:"tail_probability_10000_maximum"`
	CVaRMaximum           float64 `json:"cvar_maximum"`
	ETL40Maximum          float64 `json:"etl_40_maximum"`
	ETL10000Maximum       float64 `json:"etl_10000_maximum"`
	ETLSumMaximum         float64 `json:"etl_sum_maximum"`
}

func CurrentCompliancePolicy() CompliancePolicy {
	return CompliancePolicy{
		Version:                      compliancePolicyVersion,
		RTPMinimum:                   rtpHardMin,
		RTPMaximum:                   rtpHardMax,
		MaxWinOddsMaximum:            maxWinOddsHardMax,
		NonZeroHitProbabilityMinimum: nonZeroHitHardMin,
		CrossModeDeviationMaximum:    crossModeDeviationMax,
		Tiers: []ComplianceTierPolicy{
			{
				Tier: "2-star", BetLevelMaximum: 200, MaxExposureMaximum: 15_000_000,
				MaxPayoutMaximum: 25_000, MaxBetCostMaximum: 100_000, CostMultiplierMaximum: 1_000,
				BaseVolatilityMinimum: 0.6, BaseVolatilityMaximum: 50,
				Tail5000Maximum: 0.010, Tail10000Maximum: 0.002, CVaRMaximum: 700,
				ETL40Maximum: 0.800, ETL10000Maximum: 0.600, ETLSumMaximum: 1.300,
			},
			{
				Tier: "3-star", BetLevelMaximum: 1_000, MaxExposureMaximum: 50_000_000,
				MaxPayoutMaximum: 100_000, MaxBetCostMaximum: 500_000, CostMultiplierMaximum: 1_500,
				BaseVolatilityMinimum: 0.6, BaseVolatilityMaximum: 60,
				Tail5000Maximum: 0.010, Tail10000Maximum: 0.005, CVaRMaximum: 800,
				ETL40Maximum: 0.900, ETL10000Maximum: 0.800, ETLSumMaximum: 1.500,
			},
		},
	}
}

func validateHardModeLimits(result *ComplianceResult, mode mathIndexMode, stats *lutStats) {
	prefix := fmt.Sprintf("Mode %q", mode.Name)
	validatedRTP := math.Round(stats.RTP*rtpValidationScale) / rtpValidationScale
	if validatedRTP < rtpHardMin || validatedRTP > rtpHardMax {
		result.addFailMetric("hard.mode.rtp_range", prefix+" RTP range", "mode", mode.Name, "", true, stats.RTP, floatPointer(rtpHardMin), floatPointer(rtpHardMax), "percent", fmt.Sprintf("%.4f%% is outside %.2f%%–%.2f%%", stats.RTP, rtpHardMin, rtpHardMax))
	} else {
		result.addPassMetric("hard.mode.rtp_range", prefix+" RTP range", "mode", mode.Name, "", true, stats.RTP, floatPointer(rtpHardMin), floatPointer(rtpHardMax), "percent", fmt.Sprintf("%.4f%% is within %.2f%%–%.2f%%", stats.RTP, rtpHardMin, rtpHardMax))
	}

	if stats.MaxPayoutWeight == 0 || stats.MaxPayoutBaseBet <= 0 {
		result.addFail("hard.mode.max_win_achievability", prefix+" max-win achievability", "mode", mode.Name, "", true, "No positive maximum win is achievable")
	} else {
		odds := float64(stats.TotalWeight) / float64(stats.MaxPayoutWeight)
		if stats.MaxWinProbability+1e-12 < 1/maxWinOddsHardMax {
			result.addFailMetric("hard.mode.max_win_achievability", prefix+" max-win achievability", "mode", mode.Name, "", true, odds, nil, floatPointer(maxWinOddsHardMax), "rounds_per_hit", fmt.Sprintf("Maximum win occurs %s; required no rarer than 1 in %.0f", formatOdds(true, stats.MaxPayoutWeight, stats.TotalWeight), maxWinOddsHardMax))
		} else {
			result.addPassMetric("hard.mode.max_win_achievability", prefix+" max-win achievability", "mode", mode.Name, "", true, odds, nil, floatPointer(maxWinOddsHardMax), "rounds_per_hit", fmt.Sprintf("Maximum win occurs %s", formatOdds(true, stats.MaxPayoutWeight, stats.TotalWeight)))
		}
	}

	if stats.NonZeroProbability+1e-12 < nonZeroHitHardMin {
		result.addFailMetric("hard.mode.non_zero_hit_rate", prefix+" non-zero hit rate", "mode", mode.Name, "", true, stats.NonZeroProbability, floatPointer(nonZeroHitHardMin), nil, "probability", fmt.Sprintf("%.4f%% is below the required 5%%", stats.NonZeroProbability*100))
	} else {
		result.addPassMetric("hard.mode.non_zero_hit_rate", prefix+" non-zero hit rate", "mode", mode.Name, "", true, stats.NonZeroProbability, floatPointer(nonZeroHitHardMin), nil, "probability", fmt.Sprintf("%.4f%% is at least 5%%", stats.NonZeroProbability*100))
	}
}

func evaluateTiers(result *ComplianceResult, modes []analyzedMode) {
	for _, tier := range result.Policy.Tiers {
		start := len(result.Checks)
		maxCompliantBetLevel := tier.BetLevelMaximum
		complete := true

		for _, mode := range modes {
			if mode.stats == nil {
				complete = false
				continue
			}
			stats := mode.stats
			modeMaxCompliantBetLevel := tier.BetLevelMaximum
			if stats.MaxPayoutBaseBet > 0 {
				modeMaxCompliantBetLevel = math.Min(
					modeMaxCompliantBetLevel,
					tier.MaxExposureMaximum/stats.MaxPayoutBaseBet,
				)
			}
			if mode.definition.Cost > 0 {
				modeMaxCompliantBetLevel = math.Min(
					modeMaxCompliantBetLevel,
					tier.MaxBetCostMaximum/mode.definition.Cost,
				)
			}
			maxCompliantBetLevel = math.Min(maxCompliantBetLevel, modeMaxCompliantBetLevel)

			checkModeMaximum(result, "max_payout_multiplier", "Max payout multiplier", tier, mode.definition.Name, stats.MaxPayoutBaseBet, tier.MaxPayoutMaximum, "base_bet_x")
			checkModeMaximum(result, "cost_multiplier", "Cost multiplier", tier, mode.definition.Name, mode.definition.Cost, tier.CostMultiplierMaximum, "base_bet_x")
			if math.Abs(mode.definition.Cost-1) <= 1e-12 {
				checkModeRange(result, "base_volatility", "Base volatility", tier, mode.definition.Name, stats.Volatility, tier.BaseVolatilityMinimum, tier.BaseVolatilityMaximum, "sigma")
			}
			checkModeMaximum(result, "tail_probability_5000", "Tail probability 5,000x", tier, mode.definition.Name, stats.TailProbability5000, tier.Tail5000Maximum, "probability")
			checkModeMaximum(result, "tail_probability_10000", "Tail probability 10,000x", tier, mode.definition.Name, stats.TailProbability10000, tier.Tail10000Maximum, "probability")
			checkModeMaximum(result, "cvar", "CVaR 0.1%", tier, mode.definition.Name, stats.CVaR001, tier.CVaRMaximum, "paid_bet_x")
			checkModeMaximum(result, "etl_40", "ETL 40x", tier, mode.definition.Name, stats.ETL40, tier.ETL40Maximum, "rtp_share")
			checkModeMaximum(result, "etl_10000", "ETL 10,000x", tier, mode.definition.Name, stats.ETL10000, tier.ETL10000Maximum, "rtp_share")
			checkModeMaximum(result, "etl_sum", "ETL sum", tier, mode.definition.Name, stats.ETL40+stats.ETL10000, tier.ETLSumMaximum, "rtp_share_sum")
		}

		tierResult := ComplianceTierResult{
			Tier:                 tier.Tier,
			Status:               CompliancePass,
			LUTMathStatus:        CompliancePass,
			Complete:             complete,
			Eligible:             complete,
			MaxCompliantBetLevel: maxCompliantBetLevel,
			CheckCodes:           make([]string, 0, len(result.Checks)-start),
		}
		for _, check := range result.Checks[:start] {
			if !check.Hard {
				continue
			}
			tierResult.CheckCodes = append(tierResult.CheckCodes, check.Code)
			if check.Status == ComplianceFail {
				tierResult.Status = ComplianceFail
				tierResult.LUTMathStatus = ComplianceFail
				tierResult.Eligible = false
			}
		}
		for _, check := range result.Checks[start:] {
			tierResult.CheckCodes = append(tierResult.CheckCodes, check.Code)
			switch check.Status {
			case ComplianceFail:
				tierResult.Status = ComplianceFail
				tierResult.LUTMathStatus = ComplianceFail
				tierResult.Eligible = false
			case ComplianceNotEvaluated:
				tierResult.Complete = false
				tierResult.Eligible = false
				if tierResult.Status != ComplianceFail {
					tierResult.Status = ComplianceNotEvaluated
				}
			case ComplianceWarn:
				tierResult.Eligible = false
				if tierResult.Status == CompliancePass {
					tierResult.Status = ComplianceWarn
				}
			}
		}
		if !tierResult.Complete {
			tierResult.Eligible = false
		}
		if tierResult.Eligible {
			result.GameSummary.EligibleTiers = append(result.GameSummary.EligibleTiers, tier.Tier)
		}
		result.TierResults = append(result.TierResults, tierResult)
	}
}

func checkModeMaximum(result *ComplianceResult, metric, name string, tier ComplianceTierPolicy, mode string, actual, maximum float64, unit string) {
	code := "tier." + tier.Tier + "." + metric
	if actual > maximum+1e-12 {
		result.addFailMetric(code, name, "tier", mode, tier.Tier, false, actual, nil, floatPointer(maximum), unit, fmt.Sprintf("Mode %q value %.8g exceeds %s limit %.8g", mode, actual, tier.Tier, maximum))
	} else {
		result.addPassMetric(code, name, "tier", mode, tier.Tier, false, actual, nil, floatPointer(maximum), unit, fmt.Sprintf("Mode %q value %.8g is within %s limit %.8g", mode, actual, tier.Tier, maximum))
	}
}

func checkModeRange(result *ComplianceResult, metric, name string, tier ComplianceTierPolicy, mode string, actual, minimum, maximum float64, unit string) {
	code := "tier." + tier.Tier + "." + metric
	if actual < minimum-1e-12 || actual > maximum+1e-12 {
		result.addFailMetric(code, name, "tier", mode, tier.Tier, false, actual, floatPointer(minimum), floatPointer(maximum), unit, fmt.Sprintf("Mode %q value %.8g is outside %s range %.8g–%.8g", mode, actual, tier.Tier, minimum, maximum))
	} else {
		result.addPassMetric(code, name, "tier", mode, tier.Tier, false, actual, floatPointer(minimum), floatPointer(maximum), unit, fmt.Sprintf("Mode %q value %.8g is within %s range %.8g–%.8g", mode, actual, tier.Tier, minimum, maximum))
	}
}
