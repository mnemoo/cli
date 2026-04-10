package upload

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type ComplianceCheck struct {
	Name    string // e.g. "index.json exists", "Mode 'base': LUT valid"
	Status  string // "pass", "warn", "fail", "info"
	Details string
}

type ComplianceResult struct {
	Checks        []ComplianceCheck
	ModeSummaries []ComplianceModeSummary
	HasError      bool
	WarningsCount int
	FailuresCount int
}

type ComplianceModeSummary struct {
	Name          string
	Cost          float64
	HasStats      bool
	RTP           float64
	Volatility    float64
	VolatilityTag string
	HitRate       float64
	MaxWin        float64
	MaxWinHitRate string
	SimCount      int
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

type lutStats struct {
	Entries         int
	TotalWeight     uint64
	NonZeroWeight   uint64
	MaxPayout       uint64
	MaxPayoutWeight uint64
	RTP             float64
	Volatility      float64
}

const (
	rtpMin          = 90.0
	rtpMax          = 98.0
	rtpVariationMax = 0.5
	minSimulations  = 100_000
)

func RunMathCompliance(dirPath string) ComplianceResult {
	var result ComplianceResult

	indexPath := filepath.Join(dirPath, "index.json")
	data, err := os.ReadFile(indexPath)
	if err != nil {
		result.addFail("index.json", "File not found in "+dirPath)
		result.HasError = true
		return result
	}
	result.addPass("index.json", "Found")

	var idx mathIndex
	if err := json.Unmarshal(data, &idx); err != nil {
		result.addFail("index.json parse", fmt.Sprintf("Invalid JSON: %v", err))
		result.HasError = true
		return result
	}

	if len(idx.Modes) == 0 {
		result.addFail("index.json modes", "No bet modes defined (modes array is empty)")
		result.HasError = true
		return result
	}
	result.addPass("Bet modes", fmt.Sprintf("%d mode(s) defined", len(idx.Modes)))

	seen := make(map[string]bool)
	modeRTPs := make(map[string]float64)

	for _, mode := range idx.Modes {
		prefix := fmt.Sprintf("Mode %q", mode.Name)

		if mode.Name == "" {
			result.addFail(prefix, "Mode name is empty")
			result.HasError = true
			continue
		}
		if seen[mode.Name] {
			result.addFail(prefix, "Duplicate mode name")
			result.HasError = true
			continue
		}
		seen[mode.Name] = true

		if mode.Cost <= 0 {
			result.addFail(prefix, fmt.Sprintf("Invalid cost: %v (must be > 0)", mode.Cost))
			result.HasError = true
		} else {
			result.addPass(prefix, fmt.Sprintf("cost=%.2f", mode.Cost))
		}

		stats := validateModeFiles(&result, dirPath, mode)
		if stats != nil {
			modeRTPs[mode.Name] = stats.RTP
		}
		result.ModeSummaries = append(result.ModeSummaries, buildModeSummary(mode, stats))
	}

	validateCrossMode(&result, modeRTPs)

	return result
}

func validateModeFiles(result *ComplianceResult, dirPath string, mode mathIndexMode) *lutStats {
	prefix := fmt.Sprintf("Mode %q", mode.Name)

	if mode.Events == "" {
		result.addFail(prefix+" events", "Events filename is empty")
		result.HasError = true
	} else {
		eventsPath := filepath.Join(dirPath, mode.Events)
		if info, err := os.Stat(eventsPath); err != nil {
			result.addFail(prefix+" events", fmt.Sprintf("%s not found", mode.Events))
			result.HasError = true
		} else {
			result.addPass(prefix+" events", fmt.Sprintf("%s (%s)", mode.Events, FormatSize(info.Size())))
		}
	}

	if mode.Weights == "" {
		result.addFail(prefix+" weights", "Weights filename is empty")
		result.HasError = true
		return nil
	}

	weightsPath := filepath.Join(dirPath, mode.Weights)
	if _, err := os.Stat(weightsPath); err != nil {
		result.addFail(prefix+" weights", fmt.Sprintf("%s not found", mode.Weights))
		result.HasError = true
		return nil
	}

	return validateLUT(result, weightsPath, prefix, mode.Cost)
}

func validateLUT(result *ComplianceResult, path, prefix string, modeCost float64) *lutStats {
	f, err := os.Open(path)
	if err != nil {
		result.addFail(prefix+" LUT", fmt.Sprintf("Cannot open: %v", err))
		result.HasError = true
		return nil
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNum := 0
	var totalWeight uint64
	var nonZeroWeight uint64
	var weightedPayout float64
	var weightedX float64
	var weightedX2 float64
	var maxPayout uint64
	var maxPayoutWeight uint64
	expectedID := 0

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		lineNum++

		parts := strings.Split(line, ",")
		if len(parts) != 3 {
			result.addFail(prefix+" LUT", fmt.Sprintf("Line %d: expected 3 columns, got %d", lineNum, len(parts)))
			result.HasError = true
			return nil
		}

		simID, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 64)
		if err != nil {
			result.addFail(prefix+" LUT", fmt.Sprintf("Line %d: invalid sim_id %q", lineNum, parts[0]))
			result.HasError = true
			return nil
		}

		weight, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64)
		if err != nil {
			result.addFail(prefix+" LUT", fmt.Sprintf("Line %d: invalid weight %q", lineNum, parts[1]))
			result.HasError = true
			return nil
		}

		payout, err := strconv.ParseUint(strings.TrimSpace(parts[2]), 10, 64)
		if err != nil {
			result.addFail(prefix+" LUT", fmt.Sprintf("Line %d: invalid payout_multiplier %q (must be uint64)", lineNum, parts[2]))
			result.HasError = true
			return nil
		}

		if int(simID) != expectedID {
			result.addWarn(prefix+" LUT", fmt.Sprintf("Line %d: sim_id=%d, expected %d (non-sequential)", lineNum, simID, expectedID))
		}
		expectedID = int(simID) + 1

		if payout > 0 {
			nonZeroWeight += weight
		}
		if payout > maxPayout {
			maxPayout = payout
			maxPayoutWeight = weight
		} else if payout == maxPayout {
			maxPayoutWeight += weight
		}

		totalWeight += weight
		weightedPayout += float64(weight) * float64(payout)
		if modeCost > 0 {
			x := float64(payout) / 100.0 / modeCost
			weightedX += float64(weight) * x
			weightedX2 += float64(weight) * x * x
		}
	}

	if err := scanner.Err(); err != nil {
		result.addFail(prefix+" LUT", fmt.Sprintf("Read error: %v", err))
		result.HasError = true
		return nil
	}

	if lineNum == 0 {
		result.addFail(prefix+" LUT", "File is empty")
		result.HasError = true
		return nil
	}

	result.addPass(prefix+" LUT", fmt.Sprintf("%s valid (%d entries)", filepath.Base(path), lineNum))

	stats := &lutStats{
		Entries:         lineNum,
		TotalWeight:     totalWeight,
		NonZeroWeight:   nonZeroWeight,
		MaxPayout:       maxPayout,
		MaxPayoutWeight: maxPayoutWeight,
	}

	if totalWeight > 0 && modeCost > 0 {
		stats.RTP = (weightedPayout / float64(totalWeight) / 100.0 / modeCost) * 100.0
		result.addInfo(prefix+" RTP", fmt.Sprintf("%.4f%%", stats.RTP))

		if stats.RTP < rtpMin || stats.RTP > rtpMax {
			result.addWarn(prefix+" RTP range", fmt.Sprintf("%.4f%% is outside required %.1f%%–%.1f%% range", stats.RTP, rtpMin, rtpMax))
		}

		meanX := weightedX / float64(totalWeight)
		variance := (weightedX2 / float64(totalWeight)) - (meanX * meanX)
		if variance < 0 {
			variance = 0
		}
		stats.Volatility = math.Sqrt(variance)
		result.addInfo(prefix+" volatility", fmt.Sprintf("%s (sigma=%.4f)", classifyVolatility(stats.Volatility), stats.Volatility))
	}

	maxWinX := float64(maxPayout) / 100.0
	result.addInfo(prefix+" max win", fmt.Sprintf("%.2fx", maxWinX))

	if lineNum < minSimulations {
		result.addWarn(prefix+" simulations", fmt.Sprintf("%d entries (recommended minimum: %d)", lineNum, minSimulations))
	} else {
		result.addPass(prefix+" simulations", fmt.Sprintf("%d entries", lineNum))
	}

	if lineNum > 0 && totalWeight > 0 {
		hitRate := float64(nonZeroWeight) / float64(totalWeight) * 100.0
		result.addInfo(prefix+" hit rate", fmt.Sprintf("%.2f%% weighted non-zero hit rate", hitRate))

		zeroRatio := float64(totalWeight-nonZeroWeight) / float64(totalWeight)
		if zeroRatio > 0.90 {
			result.addWarn(prefix+" hit rate", fmt.Sprintf("%.1f%% weighted spins are non-paying (>90%% may be rejected)", zeroRatio*100))
		}
	}

	return stats
}

func validateCrossMode(result *ComplianceResult, modeRTPs map[string]float64) {
	if len(modeRTPs) < 2 {
		return
	}

	var minRTP, maxRTP float64
	minRTP = math.MaxFloat64
	for _, rtp := range modeRTPs {
		if rtp < minRTP {
			minRTP = rtp
		}
		if rtp > maxRTP {
			maxRTP = rtp
		}
	}

	variation := maxRTP - minRTP
	if variation > rtpVariationMax {
		result.addWarn("Cross-mode RTP", fmt.Sprintf("%.4f%% variation between modes (max allowed: %.1f%%)", variation, rtpVariationMax))
	} else if len(modeRTPs) > 1 {
		result.addPass("Cross-mode RTP", fmt.Sprintf("%.4f%% variation (within %.1f%% limit)", variation, rtpVariationMax))
	}
}

func (r *ComplianceResult) addPass(name, details string) {
	r.Checks = append(r.Checks, ComplianceCheck{Name: name, Status: "pass", Details: details})
}

func (r *ComplianceResult) addFail(name, details string) {
	r.Checks = append(r.Checks, ComplianceCheck{Name: name, Status: "fail", Details: details})
	r.FailuresCount++
}

func (r *ComplianceResult) addWarn(name, details string) {
	r.Checks = append(r.Checks, ComplianceCheck{Name: name, Status: "warn", Details: details})
	r.WarningsCount++
}

func (r *ComplianceResult) addInfo(name, details string) {
	r.Checks = append(r.Checks, ComplianceCheck{Name: name, Status: "info", Details: details})
}

func buildModeSummary(mode mathIndexMode, stats *lutStats) ComplianceModeSummary {
	summary := ComplianceModeSummary{
		Name: mode.Name,
		Cost: mode.Cost,
	}

	if stats == nil {
		return summary
	}

	summary.HasStats = true
	summary.RTP = stats.RTP
	summary.Volatility = stats.Volatility
	summary.VolatilityTag = classifyVolatility(stats.Volatility)
	summary.SimCount = stats.Entries
	if stats.TotalWeight > 0 {
		summary.HitRate = float64(stats.NonZeroWeight) / float64(stats.TotalWeight) * 100.0
	}
	summary.MaxWin = float64(stats.MaxPayout) / 100.0
	summary.MaxWinHitRate = formatOdds(stats.MaxPayout, stats.MaxPayoutWeight, stats.TotalWeight)
	return summary
}

func classifyVolatility(sigma float64) string {
	switch {
	case sigma < 1.5:
		return "Very Low"
	case sigma < 3.0:
		return "Low"
	case sigma < 5.0:
		return "Medium-Low"
	case sigma < 8.0:
		return "Medium"
	case sigma < 12.0:
		return "Medium-High"
	case sigma < 18.0:
		return "High"
	case sigma < 30.0:
		return "Very High"
	default:
		return "Extreme"
	}
}

func formatOdds(maxPayout, maxPayoutWeight, totalWeight uint64) string {
	if maxPayout == 0 || maxPayoutWeight == 0 || totalWeight == 0 {
		return "N/A"
	}
	odds := float64(totalWeight) / float64(maxPayoutWeight)
	switch {
	case odds >= 1_000_000:
		return fmt.Sprintf("1 in %.1fM", odds/1_000_000)
	case odds >= 1_000:
		return fmt.Sprintf("1 in %.1fK", odds/1_000)
	default:
		return fmt.Sprintf("1 in %.0f", odds)
	}
}
