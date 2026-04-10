package main

import (
	"context"
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/mnemoo/cli/internal/cli"
	"github.com/mnemoo/cli/internal/tui/app"
	"github.com/mnemoo/cli/internal/updater"
	"github.com/mnemoo/cli/internal/version"
)

// updateNoticeDeadline caps how long we wait after the main command
// finishes for the background update check to complete before exiting.
const updateNoticeDeadline = 500 * time.Millisecond

func main() {
	// Clean up any stakecli.exe.old left by a previous Windows self-update.
	updater.CleanupOldBinary()

	// Decide whether a subcommand is present and whether it should trigger
	// a background update check. Explicit version/update/help subcommands
	// don't get a notice (update runs its own check; version/help should
	// stay fast and deterministic).
	cmd := ""
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	updateCh := startBackgroundUpdateCheck(cmd)

	exitCode := run(cmd)
	printUpdateNotice(updateCh)
	os.Exit(exitCode)
}

func run(cmd string) int {
	switch cmd {
	case "upload":
		if err := cli.RunUpload(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		return 0
	case "update":
		if err := cli.RunUpdate(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		return 0
	case "version", "--version", "-v":
		fmt.Println(version.String())
		return 0
	case "help", "--help", "-h":
		printUsage()
		return 0
	}

	p := tea.NewProgram(app.New())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// startBackgroundUpdateCheck kicks off the update check in a goroutine so
// its latency is hidden behind the main command. Returns a buffered channel
// that will receive at most one result, or a closed channel if the check
// should be skipped entirely.
func startBackgroundUpdateCheck(cmd string) <-chan *updater.CheckResult {
	ch := make(chan *updater.CheckResult, 1)

	skip := false
	switch cmd {
	case "update", "version", "--version", "-v", "help", "--help", "-h":
		skip = true
	}
	if skip {
		close(ch)
		return ch
	}

	go func() {
		defer close(ch)
		result, err := updater.Check(context.Background())
		if err != nil {
			return // silent failure — no notice
		}
		ch <- result
	}()
	return ch
}

// printUpdateNotice drains the background check channel with a deadline
// and prints a one-line notice to stderr if an update is available. We
// never block the user on a slow network check: if it hasn't completed
// shortly after the command, the notice is dropped for this run.
func printUpdateNotice(ch <-chan *updater.CheckResult) {
	if ch == nil {
		return
	}
	select {
	case result, ok := <-ch:
		if !ok || result == nil {
			return
		}
		if msg := result.Notice(); msg != "" {
			fmt.Fprintln(os.Stderr)
			fmt.Fprintln(os.Stderr, msg)
		}
	case <-time.After(updateNoticeDeadline):
		// Background check didn't finish in time; skip the notice.
	}
}

func printUsage() {
	fmt.Printf("stakecli %s - Stake Engine CLI\n", version.Short())
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  stakecli              Launch interactive TUI")
	fmt.Println("  stakecli upload       Upload files to Stake Engine")
	fmt.Println("  stakecli update       Check for and install new releases")
	fmt.Println("  stakecli version      Show version and build info")
	fmt.Println("  stakecli help         Show this help")
	fmt.Println()
	fmt.Println("Upload flags:")
	fmt.Println("  --team    Team slug (required)")
	fmt.Println("  --game    Game slug (required)")
	fmt.Println("  --type    Upload type: math or front (required)")
	fmt.Println("  --path    Path to local directory (required)")
	fmt.Println("  --yes     Skip confirmation prompts (for CI/CD)")
	fmt.Println("  --publish Publish after upload")
	fmt.Println()
	fmt.Println("Update flags:")
	fmt.Println("  --check   Check for a newer release without installing")
	fmt.Println("  --yes     Install without confirmation")
	fmt.Println()
	fmt.Println("Environment variables:")
	fmt.Println("  STAKE_SID               Session ID for authentication (bypasses keyring, required for CI/CD)")
	fmt.Println("  STAKE_API_URL           Override API base URL (default: https://stake-engine.com/api)")
	fmt.Println("  STAKE_NO_UPDATE_CHECK   Set to any value to disable the background update check")
	fmt.Println()
	fmt.Println("CI/CD example:")
	fmt.Println("  STAKE_SID=$SECRET stakecli upload --team myteam --game mygame --type math --path ./dist --yes --publish")
}
