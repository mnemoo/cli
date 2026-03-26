package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mnemoo/cli/internal/api"
	"github.com/mnemoo/cli/internal/auth"
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

	// Auth
	sid, err := auth.GetActiveSID()
	if err != nil {
		return fmt.Errorf("not logged in: %w (run 'stakecli' to login first)", err)
	}

	client := api.NewClient(sid)
	u := upload.NewUploader(client)
	ctx := context.Background()

	// Plan
	fmt.Printf("\nPlanning upload: %s → %s/%s (%s)\n", *dirPath, *team, *game, *uploadType)
	plan, err := u.Plan(ctx, *team, *game, *uploadType, *dirPath)
	if err != nil {
		return fmt.Errorf("planning failed: %w", err)
	}

	// Print plan
	printPlan(plan)

	if plan.TotalActions() == 0 {
		fmt.Println("\nNothing to do -- all files are up to date.")
		return nil
	}

	// Confirm
	if !*yes {
		fmt.Printf("\n  Proceed with upload? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(input)) != "y" {
			return fmt.Errorf("aborted by user")
		}
	}

	// Execute
	fmt.Println("\nUploading...")
	err = u.Execute(ctx, plan, nil, func(evt upload.ProgressEvent) {
		status := "ok"
		if evt.Error != nil {
			status = fmt.Sprintf("ERROR: %v", evt.Error)
		}
		fmt.Printf("  [%d/%d] %s %s -- %s\n", evt.Current, evt.Total, evt.Phase, evt.FileName, status)
	})
	if err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}

	fmt.Printf("\nUpload complete! %d actions executed.\n", plan.TotalActions())

	// Publish
	if *publish {
		fmt.Printf("Publishing %s...\n", *uploadType)
		switch *uploadType {
		case "math":
			resp, err := client.PublishMath(ctx, *team, *game)
			if err != nil {
				return fmt.Errorf("publish math failed: %w", err)
			}
			fmt.Printf("Published math v%d (changed: %v)\n", resp.Version, resp.Changed)
		case "front":
			resp, err := client.PublishFront(ctx, *team, *game)
			if err != nil {
				return fmt.Errorf("publish front failed: %w", err)
			}
			fmt.Printf("Published front v%d (changed: %v)\n", resp.Version, resp.Changed)
		}
	}

	return nil
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
