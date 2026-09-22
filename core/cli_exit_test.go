//go:build linux && !cgo && cli

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompleteCLIExitIsIdempotentWithoutRuntime(t *testing.T) {
	runtimeDirectory := useTestCLIRuntimeDirectory(t)
	for index := 0; index < 2; index++ {
		if err := completeCLIExit(0); err != nil {
			t.Fatalf("completeCLIExit call %d: %v", index+1, err)
		}
	}
	for _, name := range []string{
		tuiServiceSocketFilename,
		tuiCoreSocketFilename,
		cliRuntimeLockFilename,
	} {
		if _, err := os.Stat(filepath.Join(runtimeDirectory, name)); !os.IsNotExist(err) {
			t.Fatalf("runtime artifact %q remains: %v", name, err)
		}
	}
}

func TestCompleteCLIExitContinuesAfterSSHCleanupFailure(t *testing.T) {
	runtimeDirectory := useTestCLIRuntimeDirectory(t)
	sshDirectory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(sshDirectory, "broken.json"),
		[]byte("not-json"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		tuiServiceSocketFilename,
		tuiCoreSocketFilename,
	} {
		if err := os.WriteFile(
			filepath.Join(runtimeDirectory, name),
			[]byte("stale"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}

	err = completeCLIExit(0)
	if err == nil || !strings.Contains(err.Error(), "stop SSH tunnels") {
		t.Fatalf("complete exit SSH cleanup error = %v", err)
	}
	for _, name := range []string{
		tuiServiceSocketFilename,
		tuiCoreSocketFilename,
	} {
		if _, statErr := os.Stat(filepath.Join(runtimeDirectory, name)); !os.IsNotExist(statErr) {
			t.Fatalf("complete exit stopped before removing %q: %v", name, statErr)
		}
	}
}

func TestCleanupCLIExitArtifactsKeepsSocketsWhileBackendAlive(t *testing.T) {
	runtimeDirectory := useTestCLIRuntimeDirectory(t)
	lock, err := acquireCLIBackendLock(cliProcessOwner{
		Kind:      "service",
		PID:       os.Getpid(),
		StartedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { lock.release() })
	socketPath := filepath.Join(runtimeDirectory, tuiServiceSocketFilename)
	if err := os.WriteFile(socketPath, []byte("live"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupCLIExitArtifacts(0); err != nil {
		t.Fatalf("cleanup while backend alive: %v", err)
	}
	if _, err := os.Stat(socketPath); err != nil {
		t.Fatalf("live backend socket was removed: %v", err)
	}
}
