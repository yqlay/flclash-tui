//go:build linux && !cgo && cli

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	cliSSHConnectTimeout     = 30 * time.Second
	cliSSHControlTimeout     = 5 * time.Second
	cliSSHRemoteProbeTimeout = 10 * time.Second
)

var cliSSHPendingOperations sync.WaitGroup

func beginCLISSHOperation(parent context.Context) (context.Context, func()) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, cliSSHConnectTimeout)
	cliSSHPendingOperations.Add(1)
	return ctx, func() { cancel(); cliSSHPendingOperations.Done() }
}

func waitCLISSHPendingOperations(timeout time.Duration) error {
	done := make(chan struct{})
	go func() { cliSSHPendingOperations.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return errors.New("pending SSH cleanup is still running; ownership records have been retained")
	}
}

func cliSSHOperationContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func waitCLISSHOperation(ctx context.Context, delay time.Duration) error {
	select {
	case <-cliSSHOperationContext(ctx).Done():
		return ctx.Err()
	case <-time.After(delay):
		return nil
	}
}

// Bound the entire helper, not only OpenSSH's TCP ConnectTimeout. A stalled
// ProxyCommand, authentication helper or inherited pipe must not outlive a TUI.
func runCLISSHProcess(ctx context.Context, command *exec.Cmd, state *cliSSHTunnelState) error {
	ctx = cliSSHOperationContext(ctx)
	if err := ctx.Err(); err != nil {
		return err
	}
	prepareCLISSHNonInteractiveCommand(command)
	command.WaitDelay = time.Second
	if err := command.Start(); err != nil {
		return err
	}
	kill := func() {
		if command.SysProcAttr != nil && command.SysProcAttr.Setsid {
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		} else {
			_ = command.Process.Kill()
		}
	}
	if state != nil {
		state.HelperPID = command.Process.Pid
		state.HelperStart, _ = linuxProcessStartTime(state.HelperPID)
		if err := saveCLISSHTunnelState(*state); err != nil {
			kill()
			_ = command.Wait()
			return err
		}
	}
	cancelDone := make(chan struct{})
	stopCancel := context.AfterFunc(ctx, func() { kill(); close(cancelDone) })
	err := command.Wait()
	if !stopCancel() {
		<-cancelDone
	}
	if state != nil {
		state.HelperPID, state.HelperStart = 0, ""
		if saveErr := saveCLISSHTunnelState(*state); saveErr != nil {
			err = errors.Join(err, saveErr)
		}
	}
	if ctx.Err() != nil {
		return errors.Join(ctx.Err(), err)
	}
	return err
}

func prepareCLISSHPendingState(state *cliSSHTunnelState) error {
	state.Pending = true
	state.OwnerPID = os.Getpid()
	var err error
	state.OwnerStart, err = linuxProcessStartTime(state.OwnerPID)
	if err != nil {
		return err
	}
	if err := saveCLISSHTunnelState(*state); err != nil {
		return err
	}
	started := false
	defer func() {
		if !started {
			_ = os.Remove(state.StatePath)
		}
	}()
	// The guardian is deliberately independent of the frontend. It only owns
	// resources listed in this private, per-operation record and exits on commit.
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.Command(executable, "_ssh_relay", "--guard-state", state.StatePath)
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		return fmt.Errorf("start pending SSH guardian: %w", err)
	}
	started = true
	go func() { _ = command.Wait() }()
	return nil
}

func cliSSHRecordedProcessAlive(pid int, start string) bool {
	if pid <= 0 || start == "" || !cliProcessRunning(pid) {
		return false
	}
	current, err := linuxProcessStartTime(pid)
	if err != nil || current != start {
		return false
	}
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	end := strings.LastIndex(string(data), ")")
	if end >= 0 {
		fields := strings.Fields(string(data[end+1:]))
		if len(fields) > 0 && (fields[0] == "Z" || fields[0] == "X") {
			return false
		}
	}
	return true
}

func runCLISSHPendingGuard(path string) error {
	if !filepath.IsAbs(path) || !strings.HasSuffix(path, ".json") {
		return errors.New("invalid pending SSH ownership record")
	}
	initial, err := loadCLISSHTunnelState(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !initial.Pending {
		return nil
	}
	for {
		state, readErr := loadCLISSHTunnelState(path)
		if os.IsNotExist(readErr) {
			state, readErr = loadCLISSHTunnelState(filepath.Join(filepath.Dir(path), cliSSHPersistentStateFile))
			if readErr != nil || state.OwnerPID != initial.OwnerPID || state.OwnerStart != initial.OwnerStart || !state.StartedAt.Equal(initial.StartedAt) {
				return nil
			}
		}
		if readErr != nil {
			return readErr
		}
		if !state.Pending {
			return nil
		}
		if !cliSSHRecordedProcessAlive(state.OwnerPID, state.OwnerStart) {
			lock, err := lockCLISSHRuntimeOperation(filepath.Dir(state.StatePath))
			if err != nil {
				var busy *cliLockBusyError
				if errors.As(err, &busy) {
					time.Sleep(100 * time.Millisecond)
					continue
				}
				return err
			}
			defer lock.release()
			latest, err := loadCLISSHTunnelState(state.StatePath)
			if os.IsNotExist(err) || (err == nil && (!latest.Pending || latest.OwnerPID != state.OwnerPID || latest.OwnerStart != state.OwnerStart || !latest.StartedAt.Equal(state.StartedAt))) {
				return nil
			}
			if err != nil {
				return err
			}
			state = latest
			if state.AskpassPath != "" && filepath.Dir(state.AskpassPath) == filepath.Dir(state.StatePath) {
				_ = os.Remove(state.AskpassPath)
			}
			if cliSSHRecordedProcessAlive(state.HelperPID, state.HelperStart) {
				if err := syscall.Kill(-state.HelperPID, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
					return fmt.Errorf("stop abandoned SSH helper; ownership retained: %w", err)
				}
				deadline := time.Now().Add(time.Second)
				for cliSSHRecordedProcessAlive(state.HelperPID, state.HelperStart) && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if cliSSHRecordedProcessAlive(state.HelperPID, state.HelperStart) {
					return errors.New("abandoned SSH helper did not stop; ownership retained")
				}
			}
			return stopCLIStateTunnel(state)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
