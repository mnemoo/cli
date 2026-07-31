package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/mnemoo/cli/internal/api"
	"github.com/mnemoo/cli/internal/auth"
	"github.com/mnemoo/cli/internal/daemon"
	"github.com/mnemoo/cli/internal/service"
)

func RunDaemon(args []string, out, errOut io.Writer) error {
	if len(args) == 0 {
		return printDaemonUsage(out)
	}
	switch args[0] {
	case "start":
		state, err := daemon.EnsureStarted()
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "StakeCLI daemon running (pid %d)\n", state.PID)
		return nil
	case "status":
		return runDaemonStatus(args[1:], out)
	case "stop":
		if err := daemon.RequestStop(); err != nil {
			return err
		}
		deadline := time.Now().Add(12 * time.Second)
		for time.Now().Before(deadline) {
			state, err := daemon.ReadState()
			if err == nil && !state.Running {
				fmt.Fprintln(out, "StakeCLI daemon stopped")
				return nil
			}
			time.Sleep(200 * time.Millisecond)
		}
		return errors.New("daemon did not stop within 12 seconds")
	case "run":
		return runDaemonForeground(args[1:], errOut)
	case "help", "--help", "-h":
		return printDaemonUsage(out)
	default:
		return fmt.Errorf("unknown daemon command %q", args[0])
	}
}

func runDaemonForeground(args []string, errOut io.Writer) error {
	fs := flag.NewFlagSet("daemon run", flag.ContinueOnError)
	fs.SetOutput(errOut)
	interval := fs.Duration("review-interval", daemonReviewInterval(), "approval polling interval")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected daemon arguments: %v", fs.Args())
	}
	sid, err := auth.GetActiveSID()
	if err != nil {
		return fmt.Errorf("daemon authentication: %w", err)
	}
	appService := service.New(api.NewClient(sid))
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	user, err := appService.CurrentUser(ctx)
	if err != nil {
		return fmt.Errorf("daemon authentication check: %w", err)
	}
	return daemon.Run(ctx, appService, user.ID, daemon.Options{ReviewInterval: *interval})
}

func runDaemonStatus(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("daemon status", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	state, err := daemon.ReadState()
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(state)
	}
	status := "stopped"
	if state.Running {
		status = "running"
	}
	fmt.Fprintf(out, "StakeCLI daemon: %s\n", status)
	if state.PID != 0 {
		fmt.Fprintf(out, "PID: %d\n", state.PID)
	}
	if !state.UpdatedAt.IsZero() {
		fmt.Fprintf(out, "Heartbeat: %s\n", state.UpdatedAt.Format(time.RFC3339))
	}
	if state.ActiveOperation != "" {
		fmt.Fprintf(out, "Upload: %s\n", state.ActiveOperation)
	}
	if state.LastReviewError != "" {
		fmt.Fprintf(out, "Review polling warning: %s\n", state.LastReviewError)
	}
	return nil
}

func daemonReviewInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("STAKE_DAEMON_REVIEW_INTERVAL"))
	if raw == "" {
		return 15 * time.Second
	}
	if value, err := time.ParseDuration(raw); err == nil && value >= time.Second {
		return value
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 1 {
		return time.Duration(seconds) * time.Second
	}
	return 15 * time.Second
}

func printDaemonUsage(out io.Writer) error {
	_, err := fmt.Fprintln(out, `Usage:
  stakecli daemon start
  stakecli daemon status [--json]
  stakecli daemon stop
  stakecli daemon run [--review-interval 15s]

The daemon polls approval conversations, delivers command/webhook hooks, and
runs queued uploads independently from MCP, Codex, Claude, or a terminal.`)
	return err
}
