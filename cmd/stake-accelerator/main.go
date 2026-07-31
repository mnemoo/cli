package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"

	"golang.org/x/term"

	"github.com/mnemoo/cli/internal/accelerator"
	"github.com/mnemoo/cli/internal/version"
)

const (
	defaultConfig = "/etc/stake-accelerator/config.json"
	defaultState  = "/var/lib/stake-accelerator"
)

func main() {
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	var err error
	switch command {
	case "init":
		err = runInit(os.Args[2:])
	case "serve":
		err = runServe(os.Args[2:])
	case "fingerprint":
		err = runFingerprint(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version.String())
	case "help", "--help", "-h":
		printUsage()
	default:
		printUsage()
		err = fmt.Errorf("unknown command %q", command)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runInit(args []string) error {
	flags := flag.NewFlagSet("stake-accelerator init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", defaultConfig, "Path to daemon config")
	stateDir := flags.String("state-dir", defaultState, "Private state directory")
	listen := flags.String("listen", ":7775", "Listen address")
	passwordFile := flags.String("password-file", "", "Read password from a protected file")
	passwordStdin := flags.Bool("password-stdin", false, "Read password from standard input")
	allowPrivateTargets := flags.Bool("allow-private-targets", false, "Allow local HTTP targets (testing only)")
	if err := flags.Parse(args); err != nil {
		return err
	}
	password, err := readPassword(*passwordFile, *passwordStdin, true)
	if err != nil {
		return err
	}
	fingerprint, err := accelerator.InitializeWithOptions(*configPath, *stateDir, *listen, password, *allowPrivateTargets)
	clearString(&password)
	if err != nil {
		return err
	}
	fmt.Printf("accelerator initialized\nfingerprint: sha256:%s\n", fingerprint)
	return nil
}

func runServe(args []string) error {
	flags := flag.NewFlagSet("stake-accelerator serve", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", defaultConfig, "Path to daemon config")
	if err := flags.Parse(args); err != nil {
		return err
	}
	config, err := accelerator.LoadDaemonConfig(*configPath)
	if err != nil {
		return err
	}
	server, err := accelerator.NewServer(config, log.New(os.Stderr, "", log.LstdFlags|log.LUTC))
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), terminationSignals()...)
	defer stop()
	return server.Serve(ctx)
}

func runFingerprint(args []string) error {
	flags := flag.NewFlagSet("stake-accelerator fingerprint", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", defaultConfig, "Path to daemon config")
	if err := flags.Parse(args); err != nil {
		return err
	}
	fingerprint, err := accelerator.ConfigFingerprint(*configPath)
	if err != nil {
		return err
	}
	fmt.Printf("sha256:%s\n", fingerprint)
	return nil
}

func readPassword(path string, stdin, confirm bool) (string, error) {
	if path != "" && stdin {
		return "", errors.New("--password-file and --password-stdin are mutually exclusive")
	}
	if path != "" {
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if info.Mode().Perm()&0077 != 0 {
			return "", fmt.Errorf("password file must not be accessible by group or others: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	if stdin {
		line, err := bufio.NewReader(io.LimitReader(os.Stdin, 64*1024)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		return strings.TrimSpace(line), nil
	}
	if value := os.Getenv("STAKE_ACCELERATOR_PASSWORD"); value != "" {
		return value, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("password required: use --password-stdin or --password-file")
	}
	fmt.Fprint(os.Stderr, "Accelerator password: ")
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if confirm {
		fmt.Fprint(os.Stderr, "Confirm password: ")
		second, secondErr := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if secondErr != nil {
			return "", secondErr
		}
		if string(first) != string(second) {
			return "", errors.New("passwords do not match")
		}
	}
	return strings.TrimSpace(string(first)), nil
}

func clearString(value *string) {
	if value != nil {
		*value = ""
	}
}

func printUsage() {
	fmt.Println("stake-accelerator - encrypted Stake upload relay")
	fmt.Println()
	fmt.Println("Usage:")
	fmt.Println("  stake-accelerator init [--config PATH] [--state-dir PATH] [--listen :7775]")
	fmt.Println("  stake-accelerator serve [--config PATH]")
	fmt.Println("  stake-accelerator fingerprint [--config PATH]")
	fmt.Println("  stake-accelerator version")
}
