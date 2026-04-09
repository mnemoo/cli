package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/mnemoo/cli/internal/updater"
)

// RunUpdate implements `stakecli update`.
func RunUpdate(args []string) error {
	fs := flag.NewFlagSet("update", flag.ExitOnError)
	checkOnly := fs.Bool("check", false, "Check for a newer release and print the result, without installing")
	yes := fs.Bool("yes", false, "Install without prompting for confirmation")

	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: stakecli update [flags]\n\nFlags:\n")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	ctx := context.Background()

	fmt.Println("Checking for updates...")
	result, err := updater.CheckFresh(ctx)
	if err != nil {
		return fmt.Errorf("update check failed: %w", err)
	}
	if result == nil {
		fmt.Println("Update checks are not applicable for this build (dev build, disabled via STAKE_NO_UPDATE_CHECK, or no releases published yet).")
		return nil
	}

	if !result.HasUpdate {
		fmt.Printf("Already on the latest version (%s).\n", result.Current)
		return nil
	}

	fmt.Printf("New version available: %s -> %s\n", result.Current, result.Latest)
	fmt.Printf("Release notes: %s\n", result.HTMLURL)

	if *checkOnly {
		return nil
	}

	if !*yes {
		fmt.Print("\nInstall now? [y/N]: ")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		if strings.TrimSpace(strings.ToLower(input)) != "y" {
			return fmt.Errorf("aborted by user")
		}
	}

	fmt.Println()
	inst := &updater.Installer{
		Progress: func(msg string) { fmt.Printf("  %s\n", msg) },
	}
	if err := inst.Install(ctx, result.Release); err != nil {
		return fmt.Errorf("install failed: %w", err)
	}

	fmt.Printf("\nUpdated to %s. Restart any running stakecli processes to pick up the new binary.\n", result.Latest)
	return nil
}
