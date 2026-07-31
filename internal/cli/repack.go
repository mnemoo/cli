package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/mnemoo/cli/internal/repack"
	"github.com/mnemoo/cli/internal/upload"
)

func RunRepack(args []string, output, errorOutput io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		printRepackUsage(output)
		return nil
	}
	if args[0] != "estimate" {
		return fmt.Errorf("unknown repack command %q", args[0])
	}
	flags := flag.NewFlagSet("stakecli repack estimate", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	level := flags.Int("level", repack.DefaultLevel, "Target Zstd compression level")
	sampleMiB := flags.Int64("sample-mib", repack.DefaultSampleBytes/(1024*1024), "Uncompressed sample per book in MiB")
	jsonOutput := flags.Bool("json", false, "Print structured JSON")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: stakecli repack estimate [--level 10] [--sample-mib 64] PATH")
	}
	if *level < 1 || *level > 22 {
		return errors.New("--level must be between 1 and 22")
	}
	if *sampleMiB < 1 || *sampleMiB > 1024 {
		return errors.New("--sample-mib must be between 1 and 1024")
	}
	report, err := repack.EstimateDirectory(context.Background(), flags.Arg(0), repack.Options{
		Level:       *level,
		SampleBytes: *sampleMiB * 1024 * 1024,
	})
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(output)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}
	printRepackReport(output, report, false)
	return nil
}

func printRepackReport(output io.Writer, report repack.Report, opportunityOnly bool) {
	if len(report.Files) == 0 {
		if !opportunityOnly {
			fmt.Fprintln(output, "No books_*.jsonl.zst files found.")
		}
		return
	}
	if opportunityOnly && report.RecommendedFiles == 0 {
		return
	}
	workers := report.Workers
	if workers <= 0 {
		workers = repack.RecommendedWorkers()
	}
	fmt.Fprintf(output, "\nZstd repack estimate (level %d, %d CPU workers, read-only):\n", report.Level, workers)
	for _, file := range report.Files {
		if opportunityOnly && !file.Recommended {
			continue
		}
		if file.Error != "" {
			if !opportunityOnly {
				fmt.Fprintf(output, "  ! %s: %s\n", file.Path, file.Error)
			}
			continue
		}
		accuracy := "sampled estimate"
		if file.Exact {
			accuracy = "exact"
		}
		fmt.Fprintf(
			output,
			"  %s: %s → ~%s  saved %s (%.1f%%), %s, repack ~%s\n",
			file.Path,
			upload.FormatSize(file.OriginalBytes),
			upload.FormatSize(file.EstimatedBytes),
			upload.FormatSize(max(0, file.EstimatedSavingsBytes)),
			file.EstimatedSavingsPercent,
			accuracy,
			formatEstimateDuration(file.EstimatedRepackDuration),
		)
	}
	if report.RecommendedFiles > 0 {
		fmt.Fprintf(
			output,
			"  Opportunity: %d book(s) appear under-compressed; approximately %s (%.1f%%) could be saved.\n",
			report.RecommendedFiles,
			upload.FormatSize(max(0, report.RecommendedSavingsBytes)),
			report.RecommendedSavingsPercent(),
		)
		fmt.Fprintln(output, "  No files were changed. StakeCLI can offer a verified repack step before upload.")
	} else if !opportunityOnly {
		fmt.Fprintln(output, "  Recommendation: keep the current books; Level 10 is unlikely to save enough.")
	}
}

func printAppliedRepackReport(output io.Writer, report repack.ApplyReport) {
	if len(report.Files) == 0 {
		fmt.Fprintln(output, "Verified repack: no book met the minimum estimated savings; original bundle will be uploaded.")
		return
	}
	fmt.Fprintln(output, "\nVerified repack result:")
	for _, file := range report.Files {
		status := "kept original"
		if file.Applied {
			status = "temporary repack applied"
		}
		fmt.Fprintf(
			output,
			"  %s: %s → %s, saved %s (%.1f%%), %s, SHA-256 verified\n",
			file.Path,
			upload.FormatSize(file.OriginalBytes),
			upload.FormatSize(file.RepackedBytes),
			upload.FormatSize(max(0, file.SavingsBytes)),
			file.SavingsPercent,
			status,
		)
	}
	if report.AppliedFiles == 0 {
		fmt.Fprintln(output, "  Actual savings did not meet the threshold; original bundle will be uploaded.")
		return
	}
	fmt.Fprintf(
		output,
		"  Upload staging: %d book(s), %s saved (%.1f%%). Original files remain unchanged.\n",
		report.AppliedFiles,
		upload.FormatSize(report.SavingsBytes),
		report.SavingsPercent(),
	)
}

func formatEstimateDuration(duration time.Duration) string {
	if duration <= 0 {
		return "unknown"
	}
	if duration < time.Second {
		return duration.Round(10 * time.Millisecond).String()
	}
	return duration.Round(time.Second).String()
}

func printRepackUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage:")
	fmt.Fprintln(output, "  stakecli repack estimate [--level 10] [--sample-mib 64] [--json] PATH")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Estimates whether books_*.jsonl.zst can be compressed better without changing files.")
}

func estimateUploadRepack(path string) (repack.Report, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return repack.Report{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	return repack.EstimateDirectory(ctx, absolute, repack.Options{})
}
