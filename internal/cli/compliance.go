package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mnemoo/cli/internal/upload"
)

var ErrComplianceFailed = errors.New("math compliance failed")

type complianceCommandOptions struct {
	path       string
	jsonOutput bool
	compliance upload.ComplianceOptions
}

func RunCompliance(args []string) error {
	return runCompliance(args, os.Stdout, os.Stderr)
}

func runCompliance(args []string, stdout, stderr io.Writer) error {
	options, help, err := parseComplianceCommandOptions(args)
	if help {
		printComplianceUsage(stdout)
		return nil
	}
	if err != nil {
		printComplianceUsage(stderr)
		return err
	}

	absPath, err := filepath.Abs(options.path)
	if err != nil {
		return fmt.Errorf("resolving compliance path: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("opening compliance path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("compliance path must be a directory: %s", absPath)
	}

	result := upload.RunMathComplianceWithOptions(absPath, options.compliance)
	if options.jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(result); err != nil {
			return fmt.Errorf("writing compliance JSON: %w", err)
		}
	} else {
		printComplianceReport(stdout, absPath, result)
	}
	if result.HasError {
		return ErrComplianceFailed
	}
	return nil
}

func parseComplianceCommandOptions(args []string) (complianceCommandOptions, bool, error) {
	var options complianceCommandOptions
	positionals := make([]string, 0, 1)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			return complianceCommandOptions{}, true, nil
		case arg == "--json":
			options.jsonOutput = true
		case arg == "--":
			positionals = append(positionals, args[i+1:]...)
			i = len(args)
		case arg == "--base-mode":
			value, next, err := complianceFlagValue(args, i, arg)
			if err != nil {
				return complianceCommandOptions{}, false, err
			}
			options.compliance.BaseMode = value
			i = next
		case strings.HasPrefix(arg, "--base-mode="):
			value := strings.TrimPrefix(arg, "--base-mode=")
			if value == "" {
				return complianceCommandOptions{}, false, errors.New("--base-mode requires a value")
			}
			options.compliance.BaseMode = value
		case strings.HasPrefix(arg, "-"):
			return complianceCommandOptions{}, false, fmt.Errorf("unknown flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}

	if len(positionals) != 1 {
		return complianceCommandOptions{}, false, errors.New("compliance requires exactly one PATH")
	}
	options.path = positionals[0]
	return options, false, nil
}

func complianceFlagValue(args []string, index int, name string) (string, int, error) {
	if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" || strings.HasPrefix(args[index+1], "--") {
		return "", index, fmt.Errorf("%s requires a value", name)
	}
	return args[index+1], index + 1, nil
}

func printComplianceReport(output io.Writer, path string, result upload.ComplianceResult) {
	fmt.Fprintf(output, "Math compliance: %s\n\n", path)
	for _, check := range result.Checks {
		fmt.Fprintf(output, "  %-6s %s: %s\n", complianceStatusLabel(check.Status), check.Name, check.Details)
	}

	fmt.Fprintln(output)
	fmt.Fprintf(output, "Result: %s", strings.ToUpper(string(result.Status)))
	if len(result.GameSummary.EligibleTiers) > 0 {
		fmt.Fprintf(output, " • eligible tiers: %s", strings.Join(result.GameSummary.EligibleTiers, ", "))
	}
	fmt.Fprintln(output)
	for _, tier := range result.TierResults {
		fmt.Fprintf(
			output,
			"  %s: %s • max compliant bet level %.2fx",
			tier.Tier,
			strings.ToUpper(string(tier.Status)),
			tier.MaxCompliantBetLevel,
		)
		fmt.Fprintln(output)
	}
	if len(result.TierResults) > 0 {
		fmt.Fprintln(output, "  Bet level and currency are excluded from the verdict.")
	}
	fmt.Fprintf(output, "Modes: %d • failures: %d • warnings: %d\n", len(result.ModeSummaries), result.FailuresCount, result.WarningsCount)
}

func complianceStatusLabel(status upload.ComplianceStatus) string {
	switch status {
	case upload.CompliancePass:
		return "PASS"
	case upload.ComplianceFail:
		return "FAIL"
	case upload.ComplianceWarn:
		return "WARN"
	case upload.ComplianceInfo:
		return "INFO"
	case upload.ComplianceNotEvaluated:
		return "N/A"
	default:
		return "-"
	}
}

func printComplianceUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage:")
	fmt.Fprintln(output, "  stakecli compliance [flags] PATH")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Examples:")
	fmt.Fprintln(output, "  stakecli compliance .")
	fmt.Fprintln(output, "  stakecli compliance ./publish_files --base-mode base")
	fmt.Fprintln(output, "  stakecli compliance . --json")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Flags:")
	fmt.Fprintln(output, "  --base-mode NAME       Optional display base-mode hint")
	fmt.Fprintln(output, "  --json                 Print the structured compliance result as JSON")
}
