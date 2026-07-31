package cli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/mnemoo/cli/internal/accelerator"
)

func RunAccelerator(args []string) error {
	if len(args) == 0 {
		printAcceleratorUsage(os.Stdout)
		return nil
	}
	switch args[0] {
	case "add":
		return runAcceleratorAdd(args[1:])
	case "list":
		return runAcceleratorList(args[1:])
	case "tasks":
		return runAcceleratorTasks(args[1:])
	case "status":
		return runAcceleratorStatus(args[1:])
	case "speedtest":
		return runAcceleratorSpeedTest(args[1:])
	case "remove":
		return runAcceleratorRemove(args[1:])
	case "help", "--help", "-h":
		printAcceleratorUsage(os.Stdout)
		return nil
	default:
		return fmt.Errorf("unknown accelerator command %q", args[0])
	}
}

func runAcceleratorAdd(args []string) error {
	flags := flag.NewFlagSet("stakecli accelerator add", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("name", "", "Profile name")
	password := flags.String("password", "", "Accelerator password (prefer prompt or environment)")
	passwordFile := flags.String("password-file", "", "Read password from a protected file")
	makeDefault := flags.Bool("default", false, "Make this the default accelerator")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: stakecli accelerator add [--name NAME] ADDRESS")
	}
	address := flags.Arg(0)
	secret, err := readAcceleratorPassword(*password, *passwordFile)
	if err != nil {
		return err
	}
	client, err := accelerator.NewClient(address, secret, "")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := client.Connect(ctx); err != nil {
		return err
	}
	profileName := strings.TrimSpace(*name)
	if profileName == "" {
		profileName = accelerator.AutoProfileName(address)
	}
	if err := accelerator.SaveProfile(profileName, accelerator.Profile{
		Address: address, Fingerprint: client.Fingerprint(),
	}, *makeDefault); err != nil {
		return err
	}
	fmt.Printf("Accelerator %q connected and pinned automatically.\n", profileName)
	fmt.Printf("Address: %s\nFingerprint: sha256:%s\n", address, client.Fingerprint())
	return nil
}

func runAcceleratorList(args []string) error {
	if len(args) != 0 {
		return errors.New("usage: stakecli accelerator list")
	}
	profiles, err := accelerator.LoadProfiles()
	if err != nil {
		return err
	}
	if len(profiles.Profiles) == 0 {
		fmt.Println("No accelerators configured.")
		return nil
	}
	for name, profile := range profiles.Profiles {
		marker := " "
		if profiles.Default == name {
			marker = "*"
		}
		fmt.Printf("%s %-20s %s  sha256:%s\n", marker, name, profile.Address, profile.Fingerprint)
	}
	return nil
}

func runAcceleratorTasks(args []string) error {
	flags := flag.NewFlagSet("stakecli accelerator tasks", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	reference := flags.String("accelerator", os.Getenv("STAKE_ACCELERATOR"), "Profile name or address")
	password := flags.String("password", "", "Accelerator password")
	passwordFile := flags.String("password-file", "", "Read password from a protected file")
	watch := flags.Bool("watch", false, "Continuously refresh task state")
	if err := flags.Parse(args); err != nil {
		return err
	}
	client, _, err := connectConfiguredAccelerator(*reference, *password, *passwordFile)
	if err != nil {
		return err
	}
	var previous map[string]accelerator.Task
	previousAt := time.Time{}
	for {
		tasks, err := client.ListTasks(context.Background())
		if err != nil {
			return err
		}
		now := time.Now()
		printAcceleratorTasksWithLiveRates(tasks, previous, now.Sub(previousAt))
		if !*watch {
			return nil
		}
		previous = make(map[string]accelerator.Task, len(tasks))
		for _, task := range tasks {
			previous[task.ID] = task
		}
		previousAt = now
		time.Sleep(time.Second)
		fmt.Println()
	}
}

func runAcceleratorSpeedTest(args []string) error {
	flags := flag.NewFlagSet("stakecli accelerator speedtest", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	reference := flags.String("accelerator", os.Getenv("STAKE_ACCELERATOR"), "Profile name or address")
	password := flags.String("password", "", "Accelerator password")
	passwordFile := flags.String("password-file", "", "Read password from a protected file")
	sizeMiB := flags.Int64("size", 64, "Payload size in MiB")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *sizeMiB <= 0 || *sizeMiB > 1024 {
		return errors.New("usage: stakecli accelerator speedtest [--accelerator NAME] [--size 64]")
	}
	client, profile, err := connectConfiguredAccelerator(*reference, *password, *passwordFile)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := client.SpeedTest(ctx, *sizeMiB*1024*1024)
	if err != nil {
		return err
	}
	label := profile
	if label == "" {
		label = *reference
	}
	clientRate := float64(result.Bytes) / result.ClientDuration.Seconds() / (1024 * 1024)
	serverRate := float64(result.Bytes) / result.ServerDuration.Seconds() / (1024 * 1024)
	fmt.Printf("Accelerator %s: %.1f MiB/s client-to-relay (%.1f MiB/s server receive/decrypt), %d MiB in %s over %d connections\n",
		label, clientRate, serverRate, *sizeMiB, result.ClientDuration.Round(time.Millisecond), result.Connections)
	return nil
}

func runAcceleratorStatus(args []string) error {
	flags := flag.NewFlagSet("stakecli accelerator status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	reference := flags.String("accelerator", os.Getenv("STAKE_ACCELERATOR"), "Profile name or address")
	password := flags.String("password", "", "Accelerator password")
	passwordFile := flags.String("password-file", "", "Read password from a protected file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 1 {
		return errors.New("usage: stakecli accelerator status [--accelerator NAME] TASK_ID")
	}
	client, _, err := connectConfiguredAccelerator(*reference, *password, *passwordFile)
	if err != nil {
		return err
	}
	task, err := client.GetTask(context.Background(), flags.Arg(0))
	if err != nil {
		return err
	}
	printAcceleratorTasks([]accelerator.Task{task})
	return nil
}

func runAcceleratorRemove(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: stakecli accelerator remove NAME")
	}
	if err := accelerator.RemoveProfile(args[0]); err != nil {
		return err
	}
	fmt.Printf("Removed accelerator profile %q.\n", args[0])
	return nil
}

func connectConfiguredAccelerator(reference, password, passwordFile string) (*accelerator.Client, string, error) {
	secret, err := readAcceleratorPassword(password, passwordFile)
	if err != nil {
		return nil, "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return accelerator.ConnectConfigured(ctx, reference, secret)
}

func readAcceleratorPassword(value, path string) (string, error) {
	if value != "" && path != "" {
		return "", errors.New("--password and --password-file are mutually exclusive")
	}
	if value != "" {
		return value, nil
	}
	if path != "" {
		info, err := os.Stat(path)
		if err != nil {
			return "", err
		}
		if info.Mode().Perm()&0077 != 0 {
			return "", fmt.Errorf("accelerator password file must have mode 0600 or stricter: %s", path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(data)), nil
	}
	if password := os.Getenv("STAKE_ACCELERATOR_PASSWORD"); password != "" {
		return password, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		line, err := bufio.NewReader(io.LimitReader(os.Stdin, 64*1024)).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
		if strings.TrimSpace(line) == "" {
			return "", errors.New("accelerator password is required")
		}
		return strings.TrimSpace(line), nil
	}
	fmt.Fprint(os.Stderr, "Accelerator password: ")
	secret, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(string(secret)) == "" {
		return "", errors.New("accelerator password is required")
	}
	return strings.TrimSpace(string(secret)), nil
}

func printAcceleratorTasks(tasks []accelerator.Task) {
	printAcceleratorTasksWithLiveRates(tasks, nil, 0)
}

func printAcceleratorTasksWithLiveRates(tasks []accelerator.Task, previous map[string]accelerator.Task, elapsed time.Duration) {
	if len(tasks) == 0 {
		fmt.Println("No accelerator tasks.")
		return
	}
	var intakeBytes, remoteBytes int64
	var intakeStart, intakeEnd, remoteStart, remoteEnd time.Time
	for _, task := range tasks {
		fmt.Printf("%s  %-20s  staged=%d", task.ID, task.Status, task.Received)
		if task.Expected > 0 {
			fmt.Printf("/%d", task.Expected)
		}
		fmt.Printf("  uploaded=%d", task.Uploaded)
		if prior, ok := previous[task.ID]; ok && elapsed > 0 {
			if delta := task.Received - prior.Received; delta > 0 {
				fmt.Printf("  intake-now=%.1fMiB/s", float64(delta)/elapsed.Seconds()/(1024*1024))
			}
			if delta := task.Uploaded - prior.Uploaded; delta > 0 {
				fmt.Printf("  remote-now=%.1fMiB/s", float64(delta)/elapsed.Seconds()/(1024*1024))
			}
		}
		if task.StagedAt != nil {
			intakeBytes += task.Received
			if intakeStart.IsZero() || task.CreatedAt.Before(intakeStart) {
				intakeStart = task.CreatedAt
			}
			if task.StagedAt.After(intakeEnd) {
				intakeEnd = *task.StagedAt
			}
			duration := task.StagedAt.Sub(task.CreatedAt)
			if duration > 0 {
				rate := float64(task.Received) / duration.Seconds() / (1024 * 1024)
				fmt.Printf("  intake=%.1fMiB/s", rate)
			}
		}
		if task.StartedAt != nil && task.DoneAt != nil {
			remoteBytes += task.Uploaded
			if remoteStart.IsZero() || task.StartedAt.Before(remoteStart) {
				remoteStart = *task.StartedAt
			}
			if task.DoneAt.After(remoteEnd) {
				remoteEnd = *task.DoneAt
			}
			duration := task.DoneAt.Sub(*task.StartedAt)
			if duration > 0 {
				rate := float64(task.Uploaded) / duration.Seconds() / (1024 * 1024)
				fmt.Printf("  remote=%.1fMiB/s", rate)
			}
		}
		if task.Error != "" {
			fmt.Printf("  error=%s", task.Error)
		}
		fmt.Println()
	}
	fmt.Printf("Total: %d task(s)", len(tasks))
	if duration := intakeEnd.Sub(intakeStart); intakeBytes > 0 && duration > 0 {
		fmt.Printf("  intake=%.1fMiB/s", float64(intakeBytes)/duration.Seconds()/(1024*1024))
	}
	if duration := remoteEnd.Sub(remoteStart); remoteBytes > 0 && duration > 0 {
		fmt.Printf("  remote=%.1fMiB/s", float64(remoteBytes)/duration.Seconds()/(1024*1024))
	}
	fmt.Println()
}

func printAcceleratorUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage:")
	fmt.Fprintln(output, "  stakecli accelerator add [--name NAME] ADDRESS")
	fmt.Fprintln(output, "  stakecli accelerator list")
	fmt.Fprintln(output, "  stakecli accelerator tasks [--watch]")
	fmt.Fprintln(output, "  stakecli accelerator status [--accelerator NAME] TASK_ID")
	fmt.Fprintln(output, "  stakecli accelerator speedtest [--accelerator NAME] [--size 64]")
	fmt.Fprintln(output, "  stakecli accelerator remove NAME")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "The first successful password-authenticated connection pins the server certificate automatically.")
}
