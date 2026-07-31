package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/term"

	"github.com/mnemoo/cli/internal/accelerator"
	"github.com/mnemoo/cli/internal/api"
	"github.com/mnemoo/cli/internal/auth"
	"github.com/mnemoo/cli/internal/repack"
	"github.com/mnemoo/cli/internal/service"
	"github.com/mnemoo/cli/internal/stake"
	"github.com/mnemoo/cli/internal/upload"
)

func RunUpload(args []string) error {
	fs := flag.NewFlagSet("upload", flag.ExitOnError)
	team := fs.String("team", "", "Team slug (required)")
	game := fs.String("game", "", "Game slug (required)")
	uploadType := fs.String("type", "", "Upload type: math or front (required)")
	dirPath := fs.String("path", "", "Path to local directory (required)")
	yes := fs.Bool("yes", false, "Skip confirmation prompt")
	publish := fs.Bool("publish", false, "Publish after upload")
	updateApprovalVersion := fs.Bool("update-approval-version", false, "Update an open approval to the published version")
	baseMode := fs.String("base-mode", "", "Optional display base-mode hint")
	checkCompliance := fs.Bool("compliance", false, "Run advisory math compliance before upload")
	acceleratorRef := fs.String("accelerator", os.Getenv("STAKE_ACCELERATOR"), "Accelerator profile or IP address")
	acceleratorPassword := fs.String("accelerator-password", "", "Accelerator password (prefer prompt or environment)")
	acceleratorPasswordFile := fs.String("accelerator-password-file", "", "Read accelerator password from a protected file")
	acceleratorFallback := fs.String("accelerator-fallback", envOrDefault("STAKE_ACCELERATOR_FALLBACK", "fail"), "Relay failure behavior: fail or direct")
	direct := fs.Bool("direct", false, "Upload directly without asking about an accelerator")
	estimateRepack := fs.Bool("estimate-repack", false, "Estimate whether Zstd math books can be compressed better")
	applyRepack := fs.Bool("repack", false, "Repack worthwhile Zstd math books in temporary staging before upload")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: stakecli upload [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if *team == "" || *game == "" || *uploadType == "" || *dirPath == "" {
		fs.Usage()
		return fmt.Errorf("all flags --team, --game, --type, --path are required")
	}

	if *uploadType != "math" && *uploadType != "front" {
		return fmt.Errorf("--type must be 'math' or 'front', got %q", *uploadType)
	}
	if *updateApprovalVersion && !*publish {
		return fmt.Errorf("--update-approval-version requires --publish")
	}
	if *acceleratorFallback != "fail" && *acceleratorFallback != "direct" {
		return fmt.Errorf("--accelerator-fallback must be fail or direct")
	}
	if *direct && strings.TrimSpace(*acceleratorRef) != "" {
		return fmt.Errorf("--direct and --accelerator are mutually exclusive")
	}
	if *estimateRepack && *applyRepack {
		return fmt.Errorf("--estimate-repack and --repack are mutually exclusive")
	}
	if *uploadType == "front" && (*checkCompliance || *baseMode != "") {
		return fmt.Errorf("compliance flags are only valid with --type math")
	}

	// Validate safety
	warnings := upload.ValidatePath(*dirPath)
	if len(warnings) > 0 {
		fmt.Println("\nSafety check:")
		for _, w := range warnings {
			prefix := "  WARNING"
			if w.Level == "error" {
				prefix = "  ERROR  "
			}
			fmt.Printf("  %s: %s\n", prefix, w.Message)
		}
		if upload.HasErrors(warnings) {
			return fmt.Errorf("safety validation failed -- cannot proceed")
		}
		if !*yes {
			fmt.Print("\n  Continue despite warnings? [y/N]: ")
			reader := bufio.NewReader(os.Stdin)
			input, _ := reader.ReadString('\n')
			if strings.TrimSpace(strings.ToLower(input)) != "y" {
				return fmt.Errorf("aborted by user")
			}
		}
	}

	var complianceOptions *upload.ComplianceOptions
	allowComplianceFailures := false
	interactiveCompliance := *uploadType == "math" && !*yes
	shouldCheckCompliance := *uploadType == "math" && (*checkCompliance || interactiveCompliance)
	if *uploadType == "math" && !shouldCheckCompliance && *baseMode != "" {
		return fmt.Errorf("--base-mode requires --compliance in headless mode")
	}
	if shouldCheckCompliance {
		options := upload.ComplianceOptions{BaseMode: *baseMode}
		complianceOptions = &options
		result := upload.RunMathComplianceWithOptions(*dirPath, options)
		printComplianceReport(os.Stdout, *dirPath, result)
		validationErr := result.ValidateForPublish()
		needsAcknowledgement := validationErr != nil || result.FailuresCount > 0 || result.WarningsCount > 0
		if needsAcknowledgement {
			warning := "the compliance report contains issues"
			if validationErr != nil {
				warning = validationErr.Error()
			}
			if *yes {
				fmt.Printf("\n  Compliance warning: %s\n", warning)
				fmt.Println("  Continuing because --yes explicitly accepts compliance issues.")
				allowComplianceFailures = true
			} else {
				fmt.Printf("\n  Compliance did not pass completely. Are you sure you want to continue anyway? [y/N]: ")
				reader := bufio.NewReader(os.Stdin)
				input, _ := reader.ReadString('\n')
				if strings.TrimSpace(strings.ToLower(input)) != "y" {
					return fmt.Errorf("aborted by user after compliance warning")
				}
				allowComplianceFailures = true
			}
		}
	}

	if *uploadType == "front" && (*estimateRepack || *applyRepack) {
		return fmt.Errorf("repack flags are only valid with --type math")
	}
	if *uploadType == "math" && *estimateRepack {
		report, estimateErr := estimateUploadRepack(*dirPath)
		if estimateErr != nil {
			fmt.Fprintf(os.Stderr, "Repack estimate unavailable; upload is unchanged: %v\n", estimateErr)
		} else {
			printRepackReport(os.Stdout, report, false)
		}
	}

	effectivePath := *dirPath
	if *uploadType == "math" && *applyRepack {
		fmt.Println("\nAnalyzing Zstd books for verified CI repack...")
		estimate, estimateErr := repack.EstimateDirectory(context.Background(), *dirPath, repack.Options{})
		if estimateErr != nil {
			return fmt.Errorf("estimating repack: %w", estimateErr)
		}
		printRepackReport(os.Stdout, estimate, false)
		bundleBytes, sizeErr := repack.BundleSize(*dirPath)
		if sizeErr != nil {
			return sizeErr
		}
		resources := repack.EstimateResources(estimate, bundleBytes)
		fmt.Printf("  Temporary workspace: %s\n", resources.TemporaryDirectory)
		fmt.Printf("  Compression workers: %d CPU threads (automatic, capped).\n", resources.Workers)
		fmt.Printf("  Allow up to ~%s temporary space and ~%s working RAM.\n",
			upload.FormatSize(resources.PeakTemporaryBytes), upload.FormatSize(resources.PeakMemoryBytes))
		fmt.Println("  Plaintext JSONL is streamed and is never written to temporary storage.")
		fmt.Println("  Originals remain read-only; temporary files are removed after this command.")
		applied, cleanup, applyErr := repack.ApplyRecommended(context.Background(), *dirPath, estimate, repack.Options{})
		if applyErr != nil {
			return fmt.Errorf("verified repack: %w", applyErr)
		}
		defer cleanup()
		printAppliedRepackReport(os.Stdout, applied)
		effectivePath = applied.UploadDirectory
	}

	if !*yes && !*direct && strings.TrimSpace(*acceleratorRef) == "" && term.IsTerminal(int(os.Stdin.Fd())) {
		selected, selectErr := chooseUploadRoute()
		if selectErr != nil {
			return selectErr
		}
		*acceleratorRef = selected
	}

	// Auth is resolved only after local safety and compliance preflight passes.
	sid, err := auth.GetActiveSID()
	if err != nil {
		return fmt.Errorf("not logged in (for CI/CD set STAKE_SID; for interactive use run 'stakecli' to login first): %w", err)
	}

	client := api.NewClient(sid)
	appService := service.New(client)
	if strings.TrimSpace(*acceleratorRef) != "" {
		relay, profile, relayErr := connectConfiguredAccelerator(*acceleratorRef, *acceleratorPassword, *acceleratorPasswordFile)
		if relayErr != nil {
			if *acceleratorFallback != "direct" {
				return fmt.Errorf("connecting accelerator: %w", relayErr)
			}
			fmt.Fprintf(os.Stderr, "Accelerator unavailable; using explicit direct fallback: %v\n", relayErr)
		} else {
			appService = service.NewWithUploadClient(client, &accelerator.UploadClient{
				Client: client, Accelerator: relay, FallbackDirect: *acceleratorFallback == "direct",
			})
			if profile == "" {
				profile = *acceleratorRef
			}
			fmt.Printf("Using accelerator %s (certificate pinned automatically).\n", profile)
		}
	}
	ctx := context.Background()

	// Plan
	fmt.Printf("\nPlanning upload: %s → %s/%s (%s)\n", *dirPath, *team, *game, *uploadType)
	prepared, err := appService.PrepareUpload(ctx, service.UploadTarget{
		Team: *team, Game: *game, Type: *uploadType, Path: effectivePath, Compliance: complianceOptions,
	})
	if err != nil {
		return fmt.Errorf("planning failed: %w", err)
	}
	plan := prepared.Plan

	// Print plan
	printPlan(plan)

	if plan.TotalActions() == 0 {
		fmt.Println("\nNothing to do -- all files are up to date.")
	}

	// Confirm
	if plan.TotalActions() > 0 && !*yes {
		fmt.Printf("\n  Proceed with upload? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(input)) != "y" {
			return fmt.Errorf("aborted by user")
		}
	}

	// Execute
	if plan.TotalActions() > 0 {
		fmt.Println("\nUploading...")
		err = appService.ExecutePreparedWithOptions(ctx, prepared, service.ExecuteOptions{
			Source: "cli",
			Path:   effectivePath,
			Progress: func(evt upload.ProgressEvent) {
				status := "ok"
				if evt.Error != nil {
					status = fmt.Sprintf("ERROR: %v", evt.Error)
				}
				fmt.Printf("  [%d/%d] %s %s -- %s\n", evt.Current, evt.Total, evt.Phase, evt.FileName, status)
			},
		})
		if err != nil {
			return fmt.Errorf("upload failed: %w", err)
		}

		fmt.Printf("\nUpload complete! %d actions executed.\n", plan.TotalActions())
	}

	// Publish
	if *publish {
		fmt.Printf("Publishing %s...\n", *uploadType)
		var result *stake.PublishResult
		if *uploadType == "math" {
			if shouldCheckCompliance {
				result, err = appService.PublishMath(ctx, service.UploadTarget{
					Team: *team, Game: *game, Type: *uploadType, Path: effectivePath, Compliance: complianceOptions,
				}, allowComplianceFailures)
			} else {
				result, err = appService.PublishMathUnchecked(ctx, *team, *game)
			}
		} else {
			result, err = appService.Publish(ctx, *team, *game, *uploadType)
		}
		if err != nil {
			return fmt.Errorf("publish %s failed: %w", *uploadType, err)
		}
		if result.IsError() {
			return fmt.Errorf("publish %s error [%s]: %s", *uploadType, result.Code, result.Error())
		}
		fmt.Printf("Published %s v%d (changed: %v)\n", *uploadType, result.Version, result.Changed)
		if *updateApprovalVersion {
			printApprovalVersionUpdate(ctx, appService, *team, *game, *uploadType, result.Version)
		}
	}

	return nil
}

func chooseUploadRoute() (string, error) {
	profiles, err := accelerator.LoadProfiles()
	if err != nil {
		return "", err
	}
	names := make([]string, 0, len(profiles.Profiles))
	for name := range profiles.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)

	fmt.Println("\nUpload connection:")
	fmt.Println("  1) Direct to Stake")
	for index, name := range names {
		profile := profiles.Profiles[name]
		marker := ""
		if name == profiles.Default {
			marker = " (default)"
		}
		fmt.Printf("  %d) Accelerator %s — %s%s\n", index+2, name, profile.Address, marker)
	}
	addIndex := len(names) + 2
	fmt.Printf("  %d) Add a new accelerator\n", addIndex)
	fmt.Print("Select [1]: ")
	input, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && len(input) == 0 {
		return "", err
	}
	input = strings.TrimSpace(input)
	if input == "" || input == "1" {
		return "", nil
	}
	choice, err := strconv.Atoi(input)
	if err != nil || choice < 2 || choice > addIndex {
		return "", fmt.Errorf("invalid upload connection selection %q", input)
	}
	if choice < addIndex {
		return names[choice-2], nil
	}
	fmt.Print("Accelerator IP or address (port defaults to 7775): ")
	address, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && len(address) == 0 {
		return "", err
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return "", errors.New("accelerator address is required")
	}
	return address, nil
}

func printApprovalVersionUpdate(ctx context.Context, appService *service.Service, team, game, kind string, version int) {
	result, err := appService.UpdateOpenApprovalVersion(ctx, team, game, kind, version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: published successfully, but approval version was not updated: %v\n", err)
		return
	}
	if result.Warning != "" {
		fmt.Fprintf(os.Stderr, "Warning: %s (%s remains v%d; published v%d).\n",
			result.Warning, result.Type, result.PreviousVersion, result.TargetVersion)
		return
	}
	if result.Updated {
		fmt.Printf("Approval %s version updated: v%d -> v%d\n", result.Type, result.PreviousVersion, result.TargetVersion)
		return
	}
	fmt.Printf("Approval already uses %s v%d; no update needed.\n", result.Type, result.TargetVersion)
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func printPlan(plan *upload.UploadPlan) {
	if len(plan.ToUpload) > 0 {
		fmt.Printf("\n  Upload (%d files):\n", len(plan.ToUpload))
		for _, e := range plan.ToUpload {
			fmt.Printf("    + %s (%s)\n", e.RemoteKey, upload.FormatSize(e.Size))
		}
	}

	if len(plan.ToCopy) > 0 {
		fmt.Printf("\n  Copy (%d files):\n", len(plan.ToCopy))
		for _, e := range plan.ToCopy {
			fmt.Printf("    ~ %s (%s)\n", e.RemoteKey, upload.FormatSize(e.Size))
		}
	}

	if len(plan.ToDelete) > 0 {
		fmt.Printf("\n  Delete (%d files):\n", len(plan.ToDelete))
		for _, e := range plan.ToDelete {
			fmt.Printf("    - %s\n", e.RemoteKey)
		}
	}

	if len(plan.Unchanged) > 0 {
		fmt.Printf("\n  Unchanged: %d files\n", len(plan.Unchanged))
	}

	fmt.Printf("\n  Total upload size: %s\n", upload.FormatSize(plan.TotalUploadBytes()))
	fmt.Printf("  Total actions: %d\n", plan.TotalActions())
}
