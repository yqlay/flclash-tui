//go:build linux && !cgo && cli

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	cliExitFrontendGracePeriod = 2 * time.Second
	cliExitTerminatePeriod     = 2 * time.Second
	cliExitKillPeriod          = time.Second
)

var completeCLIExitForTUI = completeCLIExit

func exitCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash exit")
		fmt.Println("Stop every TUI frontend, the shared Backend, Core, and SSH tunnels.")
		return nil
	}
	if len(args) != 0 {
		return errors.New("usage: flclash exit")
	}
	if err := completeCLIExit(0); err != nil {
		return err
	}
	fmt.Println("FlClash exited")
	return nil
}

// completeCLIExit shuts down the complete per-user FlClash CLI runtime. The
// caller can exclude its own PID so its defers release the current terminal and
// frontend lock normally.
func completeCLIExit(excludePID int) error {
	if err := stopAllCLISSHTunnels(); err != nil {
		return fmt.Errorf("stop SSH tunnels: %w", err)
	}
	backendPID := 0
	if client, status, err := currentManagedServiceRaw(); err == nil {
		backendPID = status.PID
		_ = client.shutdown()
		if waitForTUIServiceExit(client, backendPID, tuiServiceShutdownTimeout) {
			backendPID = 0
		}
	}

	waitForCLIFrontends(excludePID, cliExitFrontendGracePeriod)
	if err := signalCLIFrontends(excludePID, syscall.SIGTERM); err != nil {
		return err
	}
	waitForCLIFrontends(excludePID, cliExitTerminatePeriod)
	if err := signalCLIFrontends(excludePID, syscall.SIGKILL); err != nil {
		return err
	}
	waitForCLIFrontends(excludePID, cliExitKillPeriod)

	if backendPID == 0 {
		if owner, active, err := activeCLIBackendOwner(); err == nil && active {
			backendPID = owner.PID
		}
	}
	if backendPID > 0 && backendPID != excludePID {
		if err := signalCLIBackend(backendPID, syscall.SIGTERM); err != nil {
			return err
		}
		waitForCLIProcess(backendPID, cliExitTerminatePeriod)
		if cliProcessRunning(backendPID) {
			if err := signalCLIBackend(backendPID, syscall.SIGKILL); err != nil {
				return err
			}
			waitForCLIProcess(backendPID, cliExitKillPeriod)
		}
	}

	frontends, err := activeCLIFrontendsExcept(excludePID)
	if err != nil {
		return fmt.Errorf("verify TUI frontend exit: %w", err)
	}
	if len(frontends) != 0 {
		return fmt.Errorf(
			"TUI frontend PID(s) still running: %s",
			formatCLIPIDs(frontends),
		)
	}
	if owner, active, err := activeCLIBackendOwner(); err != nil {
		return fmt.Errorf("verify Backend exit: %w", err)
	} else if active && owner.PID != excludePID {
		return fmt.Errorf("Backend PID %d is still running", owner.PID)
	}
	return cleanupCLIExitArtifacts(excludePID)
}

func signalCLIFrontends(excludePID int, signal syscall.Signal) error {
	frontends, err := activeCLIFrontendsExcept(excludePID)
	if err != nil {
		return fmt.Errorf("list TUI frontends: %w", err)
	}
	for _, frontend := range frontends {
		if err := syscall.Kill(frontend.PID, signal); err != nil &&
			!errors.Is(err, syscall.ESRCH) {
			return fmt.Errorf(
				"signal TUI frontend PID %d: %w",
				frontend.PID,
				err,
			)
		}
	}
	return nil
}

func signalCLIBackend(pid int, signal syscall.Signal) error {
	owner, active, err := activeCLIBackendOwner()
	if err != nil {
		return fmt.Errorf("verify Backend PID %d: %w", pid, err)
	}
	if !active || owner.PID != pid {
		return nil
	}
	switch owner.Kind {
	case "service", "service-starting", "foreground":
	default:
		return fmt.Errorf(
			"refusing to signal unrecognized Backend owner %q (PID %d)",
			owner.Kind,
			owner.PID,
		)
	}
	if err := syscall.Kill(pid, signal); err != nil &&
		!errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("signal Backend PID %d: %w", pid, err)
	}
	return nil
}

func activeCLIFrontendsExcept(excludePID int) ([]cliProcessOwner, error) {
	frontends, err := listCLIFrontends()
	if err != nil {
		return nil, err
	}
	result := frontends[:0]
	for _, frontend := range frontends {
		if frontend.PID != excludePID {
			result = append(result, frontend)
		}
	}
	return result, nil
}

func waitForCLIFrontends(excludePID int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		frontends, err := activeCLIFrontendsExcept(excludePID)
		if err == nil && len(frontends) == 0 {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func waitForCLIProcess(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for cliProcessRunning(pid) && time.Now().Before(deadline) {
		time.Sleep(25 * time.Millisecond)
	}
	return !cliProcessRunning(pid)
}

func formatCLIPIDs(frontends []cliProcessOwner) string {
	pids := make([]int, 0, len(frontends))
	for _, frontend := range frontends {
		pids = append(pids, frontend.PID)
	}
	sort.Ints(pids)
	values := make([]string, 0, len(pids))
	for _, pid := range pids {
		values = append(values, strconv.Itoa(pid))
	}
	return strings.Join(values, ", ")
}

func cleanupCLIExitArtifacts(excludePID int) error {
	runtimeDirectory, err := cliRuntimeDirectory()
	if err != nil {
		return err
	}
	for _, name := range []string{
		tuiServiceSocketFilename,
		tuiCoreSocketFilename,
	} {
		if err := os.Remove(filepath.Join(runtimeDirectory, name)); err != nil &&
			!os.IsNotExist(err) {
			return fmt.Errorf("remove stale runtime socket %q: %w", name, err)
		}
	}
	frontends, err := activeCLIFrontendsExcept(excludePID)
	if err != nil {
		return err
	}
	if len(frontends) != 0 {
		return fmt.Errorf("TUI frontend lock(s) remain: %s", formatCLIPIDs(frontends))
	}
	lockPath, err := cliRuntimeLockPath()
	if err != nil {
		return err
	}
	if owner, active, activeErr := activeCLIBackendOwner(); activeErr != nil {
		return activeErr
	} else if !active && owner.PID != excludePID {
		_ = os.Remove(lockPath)
	}
	return nil
}
