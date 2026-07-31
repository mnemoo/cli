package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"
)

func EnsureStarted() (State, error) {
	return EnsureStartedForAccount("")
}

func EnsureStartedForAccount(accountID string) (State, error) {
	state, err := ReadState()
	if err == nil && state.Running {
		if accountID != "" && state.AccountID != accountID {
			return State{}, fmt.Errorf("StakeCLI daemon is running for a different account (pid %d); stop it before switching accounts", state.PID)
		}
		return state, nil
	}
	if err := StartProcess(); err != nil {
		return State{}, err
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
		state, err = ReadState()
		if err == nil && state.Running {
			if accountID != "" && state.AccountID != accountID {
				return State{}, errors.New("StakeCLI daemon started with a different account")
			}
			return state, nil
		}
	}
	return State{}, errors.New("StakeCLI daemon did not become ready within 5 seconds")
}

func StartProcess() error {
	paths, err := DefaultPaths()
	if err != nil {
		return err
	}
	if err := secureDir(paths.Dir); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locating stakecli executable: %w", err)
	}
	logFile, err := os.OpenFile(paths.Log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening daemon log: %w", err)
	}
	cmd := exec.Command(executable, "daemon", "run")
	cmd.Env = append(os.Environ(), "STAKE_NO_UPDATE_CHECK=1")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	configureDetached(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("starting StakeCLI daemon: %w", err)
	}
	_ = logFile.Close()
	return cmd.Process.Release()
}
