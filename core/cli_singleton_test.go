//go:build linux && !cgo && cli

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func useTestCLIRuntimeDirectory(t *testing.T) string {
	t.Helper()
	previous := cliRuntimeDirectoryOverride
	directory := t.TempDir()
	cliRuntimeDirectoryOverride = directory
	t.Cleanup(func() {
		cliRuntimeDirectoryOverride = previous
	})
	return directory
}

func TestCLIBackendLockAllowsOnlyOnePerUser(t *testing.T) {
	useTestCLIRuntimeDirectory(t)
	first, err := acquireCLIBackendLock(cliProcessOwner{
		Kind:       "service",
		ConfigPath: "/tmp/first.yaml",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer first.release()

	_, err = acquireCLIBackendLock(cliProcessOwner{
		Kind:       "foreground",
		ConfigPath: "/tmp/second.yaml",
	})
	var busyErr *cliLockBusyError
	if !errors.As(err, &busyErr) {
		t.Fatalf("second backend lock error = %v, want cliLockBusyError", err)
	}
	if busyErr.owner.PID != os.Getpid() ||
		busyErr.owner.Kind != "service" {
		t.Fatalf("busy owner = %+v", busyErr.owner)
	}
	if !strings.Contains(err.Error(), "/tmp/first.yaml") {
		t.Fatalf("busy error does not identify active config: %v", err)
	}

	first.release()
	replacement, err := acquireCLIBackendLock(cliProcessOwner{
		Kind: "foreground",
	})
	if err != nil {
		t.Fatalf("released backend lock was not reusable: %v", err)
	}
	replacement.release()
}

func TestActiveCLIBackendOwnerRequiresHeldLock(t *testing.T) {
	useTestCLIRuntimeDirectory(t)
	lock, err := acquireCLIBackendLock(cliProcessOwner{Kind: "service"})
	if err != nil {
		t.Fatal(err)
	}
	owner, active, err := activeCLIBackendOwner()
	if err != nil || !active || owner.PID != os.Getpid() {
		t.Fatalf("active owner = (%+v, %t, %v)", owner, active, err)
	}
	path := lock.path
	lock.release()
	owner, active, err = activeCLIBackendOwner()
	if err != nil || active || owner.PID != 0 {
		t.Fatalf("released owner = (%+v, %t, %v)", owner, active, err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale backend lock was not removed: %v", err)
	}
}

func TestCLIFrontendSessionsAllowMultipleAndCleanStaleFiles(t *testing.T) {
	runtimeDirectory := useTestCLIRuntimeDirectory(t)
	first, existing, err := registerCLIFrontend(
		"/tmp/flclash",
		"/tmp/flclash/config.yaml",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer first.close()
	if len(existing) != 0 {
		t.Fatalf("first frontend saw %d existing sessions", len(existing))
	}

	second, existing, err := registerCLIFrontend(
		"/tmp/flclash",
		"/tmp/flclash/config.yaml",
	)
	if err != nil {
		t.Fatal(err)
	}
	defer second.close()
	if len(existing) != 1 || existing[0].PID != os.Getpid() {
		t.Fatalf("second frontend existing sessions = %+v", existing)
	}
	active, err := listCLIFrontends()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("active frontend count = %d, want 2", len(active))
	}

	stalePath := filepath.Join(
		runtimeDirectory,
		cliFrontendDirectoryName,
		"stale.lock",
	)
	stale, err := acquireCLIFileLock(stalePath, cliProcessOwner{
		Kind: "tui",
		PID:  999999,
	})
	if err != nil {
		t.Fatal(err)
	}
	stale.release()
	active, err = listCLIFrontends()
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("stale frontend changed active count to %d", len(active))
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("stale frontend file was not removed: %v", err)
	}

	notice := formatCLIFrontendNotice(existing)
	if !strings.Contains(notice, "PID") ||
		!strings.Contains(notice, "other TUI frontend") {
		t.Fatalf("frontend notice = %q", notice)
	}
}

func TestTUIServiceClientUsesPerUserSocket(t *testing.T) {
	runtimeDirectory := useTestCLIRuntimeDirectory(t)
	first := newTUIServiceClient("/tmp/flclash-a")
	second := newTUIServiceClient("/tmp/flclash-b")
	want := filepath.Join(runtimeDirectory, tuiServiceSocketFilename)
	if first.socketPath() != want || second.socketPath() != want {
		t.Fatalf(
			"service sockets = %q and %q, want %q",
			first.socketPath(),
			second.socketPath(),
			want,
		)
	}
}

func TestValidateTUIServiceTargetRejectsSecondExplicitBackend(
	t *testing.T,
) {
	paths := cliPaths{
		homeDir:    "/tmp/flclash-b",
		configPath: "/tmp/flclash-b/config.yaml",
	}
	status := tuiServiceStatus{
		HomeDir:    "/tmp/flclash-a",
		ConfigPath: "/tmp/flclash-a/config.yaml",
	}
	if err := validateTUIServiceTarget(
		paths,
		status,
		false,
		false,
	); err != nil {
		t.Fatalf("implicit reconnect was rejected: %v", err)
	}
	err := validateTUIServiceTarget(paths, status, true, false)
	if err == nil ||
		!strings.Contains(err.Error(), "per-user FlClash backend") {
		t.Fatalf("explicit second backend error = %v", err)
	}
	sameDirectory := cliPaths{
		homeDir:    "/tmp/flclash-a",
		configPath: "/tmp/flclash-a/config.yaml",
	}
	if err := validateTUIServiceTarget(
		sameDirectory,
		status,
		false,
		true,
	); err != nil {
		t.Fatalf("same explicit directory was rejected: %v", err)
	}
}
