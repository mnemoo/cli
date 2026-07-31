package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/mnemoo/cli/internal/tui/app"
	"github.com/mnemoo/cli/internal/upload"
)

type publishOptions struct {
	uploadType      string
	path            string
	headless        bool
	team            string
	game            string
	compliance      upload.ComplianceOptions
	checkCompliance bool
	updateApproval  bool
	accelerator     string
	accelPassword   string
	accelPassFile   string
	accelFallback   string
	repack          bool
}

func RunPublish(args []string) error {
	options, help, err := parsePublishOptions(args)
	if help {
		printPublishUsage(os.Stdout)
		return nil
	}
	if err != nil {
		printPublishUsage(os.Stderr)
		return err
	}

	absPath, err := filepath.Abs(options.path)
	if err != nil {
		return fmt.Errorf("resolving publish path: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return fmt.Errorf("opening publish path: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("publish path must be a directory: %s", absPath)
	}

	if options.headless {
		uploadArgs := []string{
			"--team", options.team,
			"--game", options.game,
			"--type", options.uploadType,
			"--path", absPath,
			"--yes",
			"--publish",
		}
		if options.updateApproval {
			uploadArgs = append(uploadArgs, "--update-approval-version")
		}
		if options.repack {
			uploadArgs = append(uploadArgs, "--repack")
		}
		if options.uploadType == "math" {
			if options.checkCompliance {
				uploadArgs = append(uploadArgs, "--compliance")
				if options.compliance.BaseMode != "" {
					uploadArgs = append(uploadArgs, "--base-mode", options.compliance.BaseMode)
				}
			}
		}
		if options.accelerator != "" {
			uploadArgs = append(uploadArgs, "--accelerator", options.accelerator)
		}
		if options.accelPassword != "" {
			uploadArgs = append(uploadArgs, "--accelerator-password", options.accelPassword)
		}
		if options.accelPassFile != "" {
			uploadArgs = append(uploadArgs, "--accelerator-password-file", options.accelPassFile)
		}
		if options.accelFallback != "" {
			uploadArgs = append(uploadArgs, "--accelerator-fallback", options.accelFallback)
		}
		return RunUpload(uploadArgs)
	}

	program := tea.NewProgram(app.NewPublish(options.uploadType, absPath, options.compliance, options.updateApproval))
	finalModel, err := program.Run()
	if model, ok := finalModel.(app.Model); ok {
		model.Cleanup()
	}
	return err
}

func parsePublishOptions(args []string) (publishOptions, bool, error) {
	var options publishOptions
	positionals := make([]string, 0, 2)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help":
			return publishOptions{}, true, nil
		case arg == "--headless":
			options.headless = true
		case arg == "--compliance":
			options.checkCompliance = true
		case arg == "--update-approval-version":
			options.updateApproval = true
		case arg == "--repack":
			options.repack = true
		case arg == "--team" || arg == "--game":
			if i+1 >= len(args) {
				return publishOptions{}, false, fmt.Errorf("%s requires a value", arg)
			}
			i++
			if arg == "--team" {
				options.team = args[i]
			} else {
				options.game = args[i]
			}
		case arg == "--accelerator" || arg == "--accelerator-password" || arg == "--accelerator-password-file" || arg == "--accelerator-fallback":
			if i+1 >= len(args) {
				return publishOptions{}, false, fmt.Errorf("%s requires a value", arg)
			}
			i++
			switch arg {
			case "--accelerator":
				options.accelerator = args[i]
			case "--accelerator-password":
				options.accelPassword = args[i]
			case "--accelerator-password-file":
				options.accelPassFile = args[i]
			case "--accelerator-fallback":
				options.accelFallback = args[i]
			}
		case strings.HasPrefix(arg, "--team="):
			options.team = strings.TrimPrefix(arg, "--team=")
		case strings.HasPrefix(arg, "--game="):
			options.game = strings.TrimPrefix(arg, "--game=")
		case strings.HasPrefix(arg, "--accelerator="):
			options.accelerator = strings.TrimPrefix(arg, "--accelerator=")
		case strings.HasPrefix(arg, "--accelerator-password="):
			options.accelPassword = strings.TrimPrefix(arg, "--accelerator-password=")
		case strings.HasPrefix(arg, "--accelerator-password-file="):
			options.accelPassFile = strings.TrimPrefix(arg, "--accelerator-password-file=")
		case strings.HasPrefix(arg, "--accelerator-fallback="):
			options.accelFallback = strings.TrimPrefix(arg, "--accelerator-fallback=")
		case arg == "--base-mode":
			value, next, err := complianceFlagValue(args, i, arg)
			if err != nil {
				return publishOptions{}, false, err
			}
			options.compliance.BaseMode = value
			i = next
		case strings.HasPrefix(arg, "--base-mode="):
			value := strings.TrimPrefix(arg, "--base-mode=")
			if value == "" {
				return publishOptions{}, false, errors.New("--base-mode requires a value")
			}
			options.compliance.BaseMode = value
		case strings.HasPrefix(arg, "-"):
			return publishOptions{}, false, fmt.Errorf("unknown flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}

	if len(positionals) != 2 {
		return publishOptions{}, false, errors.New("publish requires TYPE and PATH")
	}
	options.uploadType = positionals[0]
	options.path = positionals[1]
	if options.uploadType != "math" && options.uploadType != "front" {
		return publishOptions{}, false, fmt.Errorf("TYPE must be math or front, got %q", options.uploadType)
	}
	if options.uploadType == "math" && options.headless {
		hasContext := options.compliance.BaseMode != ""
		if !options.checkCompliance && hasContext {
			return publishOptions{}, false, errors.New("headless compliance parameters require --compliance")
		}
	} else if options.uploadType == "front" && (options.checkCompliance || options.compliance.BaseMode != "") {
		return publishOptions{}, false, errors.New("compliance flags are only valid for math publish")
	}
	if options.repack && (options.uploadType != "math" || !options.headless) {
		return publishOptions{}, false, errors.New("--repack requires headless math publish")
	}
	if options.headless {
		if options.team == "" || options.game == "" {
			return publishOptions{}, false, errors.New("--headless requires --team and --game")
		}
	} else if options.team != "" || options.game != "" {
		return publishOptions{}, false, errors.New("--team and --game require --headless; interactive mode asks for them")
	}
	if !options.headless && (options.accelerator != "" || options.accelPassword != "" || options.accelPassFile != "") {
		return publishOptions{}, false, errors.New("accelerator publish flags currently require --headless")
	}
	if options.accelPassword != "" && options.accelPassFile != "" {
		return publishOptions{}, false, errors.New("--accelerator-password and --accelerator-password-file are mutually exclusive")
	}
	if options.accelFallback != "" && options.accelFallback != "fail" && options.accelFallback != "direct" {
		return publishOptions{}, false, errors.New("--accelerator-fallback must be fail or direct")
	}
	if (options.accelPassword != "" || options.accelPassFile != "") && options.accelerator == "" && os.Getenv("STAKE_ACCELERATOR") == "" {
		return publishOptions{}, false, errors.New("accelerator password requires --accelerator or STAKE_ACCELERATOR")
	}
	return options, false, nil
}

func printPublishUsage(output *os.File) {
	fmt.Fprintln(output, "Usage:")
	fmt.Fprintln(output, "  stakecli publish <math|front> <path>")
	fmt.Fprintln(output, "  stakecli publish <math|front> <path> --headless --team TEAM --game GAME")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Flags:")
	fmt.Fprintln(output, "  --headless             Run without the TUI or prompts")
	fmt.Fprintln(output, "  --team TEAM            Team slug (headless only)")
	fmt.Fprintln(output, "  --game GAME            Game slug (headless only)")
	fmt.Fprintln(output, "  --compliance           Show advisory math compliance in headless mode")
	fmt.Fprintln(output, "  --update-approval-version  Update an open approval to the published version")
	fmt.Fprintln(output, "  --repack               CPU-intensive verified temporary Zstd repack (headless math only)")
	fmt.Fprintln(output, "  --base-mode NAME       Optional display base-mode hint")
	fmt.Fprintln(output, "  --accelerator NAME     Route upload PUTs through the configured accelerator")
	fmt.Fprintln(output, "  --accelerator-password-file PATH  Read relay password from a protected file")
	fmt.Fprintln(output, "  --accelerator-fallback MODE  Relay failure behavior: fail or direct")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Interactive math publish always checks compliance and offers an extra confirmation on problems.")
	fmt.Fprintln(output, "Headless mode skips compliance unless --compliance is set; reported issues never block upload or publish.")
	fmt.Fprintln(output, "Interactive mode asks for account, team, and game, then uploads and publishes.")
}
