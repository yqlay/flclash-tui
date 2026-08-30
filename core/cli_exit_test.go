//go:build linux && !cgo && cli

package main

import (
	"os"
	"path/filepath"
	"testing"
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
