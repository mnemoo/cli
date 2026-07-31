package upload

import (
	"bufio"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type weightedOutcome struct {
	PayoutBaseBet float64
	Normalized    float64
	Weight        uint64
}

type lutStats struct {
	Entries              int
	TotalWeight          uint64
	NonZeroWeight        uint64
	MaxPayoutWeight      uint64
	MaxPayoutBaseBet     float64
	MaxNormalizedPayout  float64
	RTP                  float64
	Volatility           float64
	NonZeroProbability   float64
	MaxWinProbability    float64
	TailProbability5000  float64
	TailProbability10000 float64
	CVaR001              float64
	ETL40                float64
	ETL10000             float64
}

func validateLUT(result *ComplianceResult, path, prefix string, mode mathIndexMode) *lutStats {
	if mode.Cost <= 0 || math.IsNaN(mode.Cost) || math.IsInf(mode.Cost, 0) {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		result.addFail("mode.lut.open", prefix+" LUT", "mode", mode.Name, "", true, fmt.Sprintf("Cannot open: %v", err))
		return nil
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lineNum := 0
	expectedID := uint64(0)
	nonSequentialCount := 0
	firstNonSequentialLine := 0
	zeroWeightRows := 0
	totalWeight := uint64(0)
	payoutWeights := make(map[uint64]uint64)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lineNum++
		parts := strings.Split(line, ",")
		if len(parts) != 3 {
			result.addFail("mode.lut.columns", prefix+" LUT", "mode", mode.Name, "", true, fmt.Sprintf("Line %d: expected 3 columns, got %d", lineNum, len(parts)))
			return nil
		}

		simID, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil {
			result.addFail("mode.lut.sim_id", prefix+" LUT", "mode", mode.Name, "", true, fmt.Sprintf("Line %d: invalid sim_id %q", lineNum, parts[0]))
			return nil
		}
		weight, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil {
			result.addFail("mode.lut.weight", prefix+" LUT", "mode", mode.Name, "", true, fmt.Sprintf("Line %d: invalid weight %q", lineNum, parts[1]))
			return nil
		}
		payout, err := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 64)
		if err != nil {
			result.addFail("mode.lut.payout", prefix+" LUT", "mode", mode.Name, "", true, fmt.Sprintf("Line %d: invalid payout_multiplier %q", lineNum, parts[2]))
			return nil
		}

		if simID != expectedID {
			nonSequentialCount++
			if firstNonSequentialLine == 0 {
				firstNonSequentialLine = lineNum
			}
		}
		expectedID = simID + 1
		if weight == 0 {
			zeroWeightRows++
			continue
		}
		if ^uint64(0)-totalWeight < weight {
			result.addFail("mode.lut.total_weight_overflow", prefix+" LUT", "mode", mode.Name, "", true, fmt.Sprintf("Line %d: total weight exceeds uint64", lineNum))
			return nil
		}
		totalWeight += weight
		current := payoutWeights[payout]
		if ^uint64(0)-current < weight {
			result.addFail("mode.lut.payout_weight_overflow", prefix+" LUT", "mode", mode.Name, "", true, fmt.Sprintf("Line %d: weight for payout %d exceeds uint64", lineNum, payout))
			return nil
		}
		payoutWeights[payout] = current + weight
	}
	if err := scanner.Err(); err != nil {
		result.addFail("mode.lut.read", prefix+" LUT", "mode", mode.Name, "", true, fmt.Sprintf("Read error: %v", err))
		return nil
	}
	if lineNum == 0 {
		result.addFail("mode.lut.non_empty", prefix+" LUT", "mode", mode.Name, "", true, "File is empty")
		return nil
	}
	if totalWeight == 0 {
		result.addFail("mode.lut.positive_weight", prefix+" LUT", "mode", mode.Name, "", true, "Total positive weight is zero")
		return nil
	}
	result.addPass("mode.lut.valid", prefix+" LUT", "mode", mode.Name, "", true, fmt.Sprintf("%s valid (%d entries, total weight %d)", filepath.Base(path), lineNum, totalWeight))
	if nonSequentialCount > 0 {
		result.addWarn("mode.lut.sequential_ids", prefix+" LUT IDs", "mode", mode.Name, "", false, fmt.Sprintf("%d non-sequential sim_id transition(s), first at line %d", nonSequentialCount, firstNonSequentialLine))
	} else {
		result.addPass("mode.lut.sequential_ids", prefix+" LUT IDs", "mode", mode.Name, "", false, "sim_id values are sequential")
	}
	if zeroWeightRows > 0 {
		result.addWarn("mode.lut.zero_weight_rows", prefix+" LUT weights", "mode", mode.Name, "", false, fmt.Sprintf("%d row(s) have zero weight and do not affect the distribution", zeroWeightRows))
	}

	stats := calculateLUTStats(lineNum, totalWeight, payoutWeights, mode.Cost)
	result.addInfo("metric.rtp", prefix+" RTP", "mode", mode.Name, "", false, fmt.Sprintf("%.4f%%", stats.RTP))
	result.addInfo("metric.volatility", prefix+" volatility", "mode", mode.Name, "", false, fmt.Sprintf("%s (sigma=%.4f)", classifyVolatility(stats.Volatility), stats.Volatility))
	result.addInfo("metric.max_win", prefix+" max win", "mode", mode.Name, "", false, fmt.Sprintf("%.2fx base bet; %.2fx paid mode cost", stats.MaxPayoutBaseBet, stats.MaxNormalizedPayout))
	result.addInfo("metric.hit_rate", prefix+" hit rate", "mode", mode.Name, "", false, fmt.Sprintf("%.4f%% weighted non-zero hit rate", stats.NonZeroProbability*100))
	result.addInfo("metric.tail_risk", prefix+" tail risk", "mode", mode.Name, "", false, fmt.Sprintf("P(≥5000x)=%.8f, P(≥10000x)=%.8f, CVaR0.1%%=%.4fx, ETL sum=%.6f", stats.TailProbability5000, stats.TailProbability10000, stats.CVaR001, stats.ETL40+stats.ETL10000))
	return stats
}

func calculateLUTStats(entries int, totalWeight uint64, payoutWeights map[uint64]uint64, modeCost float64) *lutStats {
	stats := &lutStats{Entries: entries, TotalWeight: totalWeight}
	outcomes := make([]weightedOutcome, 0, len(payoutWeights))
	weightedX := 0.0
	weightedX2 := 0.0
	tailWeight5000 := 0.0
	tailWeight10000 := 0.0
	tailRTP40 := 0.0
	tailRTP10000 := 0.0

	for payout, weight := range payoutWeights {
		payoutBaseBet := float64(payout) / 100
		normalized := payoutBaseBet / modeCost
		weightFloat := float64(weight)
		outcomes = append(outcomes, weightedOutcome{PayoutBaseBet: payoutBaseBet, Normalized: normalized, Weight: weight})
		weightedX += weightFloat * normalized
		weightedX2 += weightFloat * normalized * normalized
		if payout > 0 {
			stats.NonZeroWeight += weight
		}
		if normalized > stats.MaxNormalizedPayout {
			stats.MaxNormalizedPayout = normalized
			stats.MaxPayoutBaseBet = payoutBaseBet
			stats.MaxPayoutWeight = weight
		} else if normalized == stats.MaxNormalizedPayout {
			stats.MaxPayoutWeight += weight
			stats.MaxPayoutBaseBet = math.Max(stats.MaxPayoutBaseBet, payoutBaseBet)
		}
		if normalized >= 5000 {
			tailWeight5000 += weightFloat
		}
		if normalized >= 10000 {
			tailWeight10000 += weightFloat
			tailRTP10000 += weightFloat * normalized
		}
		if normalized >= 40 {
			tailRTP40 += weightFloat * normalized
		}
	}

	totalWeightFloat := float64(totalWeight)
	mean := weightedX / totalWeightFloat
	variance := weightedX2/totalWeightFloat - mean*mean
	if variance < 0 && variance > -1e-12 {
		variance = 0
	}
	stats.RTP = mean * 100
	stats.Volatility = math.Sqrt(math.Max(variance, 0))
	stats.NonZeroProbability = float64(stats.NonZeroWeight) / totalWeightFloat
	stats.MaxWinProbability = float64(stats.MaxPayoutWeight) / totalWeightFloat
	stats.TailProbability5000 = tailWeight5000 / totalWeightFloat
	stats.TailProbability10000 = tailWeight10000 / totalWeightFloat
	stats.CVaR001 = calculateCVaR(outcomes, totalWeightFloat, 0.001)
	if weightedX > 0 {
		stats.ETL40 = tailRTP40 / weightedX
		stats.ETL10000 = tailRTP10000 / weightedX
	}
	return stats
}

func calculateCVaR(outcomes []weightedOutcome, totalWeight, tailFraction float64) float64 {
	if totalWeight <= 0 || tailFraction <= 0 || len(outcomes) == 0 {
		return 0
	}
	sort.Slice(outcomes, func(i, j int) bool { return outcomes[i].Normalized > outcomes[j].Normalized })
	tailMass := totalWeight * tailFraction
	remaining := tailMass
	weightedTail := 0.0
	for _, outcome := range outcomes {
		if remaining <= 0 {
			break
		}
		take := math.Min(float64(outcome.Weight), remaining)
		weightedTail += take * outcome.Normalized
		remaining -= take
	}
	if tailMass == 0 {
		return 0
	}
	return weightedTail / tailMass
}

func classifyVolatility(sigma float64) string {
	switch {
	case sigma < 1.5:
		return "Very Low"
	case sigma < 3:
		return "Low"
	case sigma < 5:
		return "Medium-Low"
	case sigma < 8:
		return "Medium"
	case sigma < 12:
		return "Medium-High"
	case sigma < 18:
		return "High"
	case sigma < 30:
		return "Very High"
	default:
		return "Extreme"
	}
}

func formatOdds(hasOutcome bool, outcomeWeight, totalWeight uint64) string {
	if !hasOutcome || outcomeWeight == 0 || totalWeight == 0 {
		return "N/A"
	}
	odds := float64(totalWeight) / float64(outcomeWeight)
	switch {
	case odds >= 1_000_000:
		return fmt.Sprintf("1 in %.1fM", odds/1_000_000)
	case odds >= 1_000:
		return fmt.Sprintf("1 in %.1fK", odds/1_000)
	default:
		return fmt.Sprintf("1 in %.0f", odds)
	}
}
