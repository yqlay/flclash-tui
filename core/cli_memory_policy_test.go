//go:build linux && !cgo && cli

package main

import (
	"os"
	"runtime/debug"
	"testing"
	"time"
)

func TestCLIManagedCoreMemoryLimitIsBounded(t *testing.T) {
	if cliManagedCoreMemoryLimit < 256<<20 || cliManagedCoreMemoryLimit > 768<<20 {
		t.Fatalf(
			"managed Core memory limit = %d, want 256MiB..768MiB",
			cliManagedCoreMemoryLimit,
		)
	}
	if tuiCoreMemoryScavengeInterval < time.Minute {
		t.Fatalf(
			"Core memory scavenge interval = %s, want at least 1m",
			tuiCoreMemoryScavengeInterval,
		)
	}
}

func TestApplyCLIGoMemoryPolicySetsSoftLimit(t *testing.T) {
	previousEnv := os.Getenv("GOMEMLIMIT")
	_ = os.Unsetenv("GOMEMLIMIT")
	previousLimit := debug.SetMemoryLimit(-1)
	t.Cleanup(func() {
		debug.SetMemoryLimit(previousLimit)
		if previousEnv == "" {
			_ = os.Unsetenv("GOMEMLIMIT")
			return
		}
		_ = os.Setenv("GOMEMLIMIT", previousEnv)
	})

	applyCLIGoMemoryPolicy()
	got := debug.SetMemoryLimit(-1)
	if got != cliManagedCoreMemoryLimit {
		t.Fatalf("memory limit = %d, want %d", got, cliManagedCoreMemoryLimit)
	}
}

func TestApplyCLIGoMemoryPolicyRespectsGOMEMLIMIT(t *testing.T) {
	t.Setenv("GOMEMLIMIT", "256MiB")
	previousLimit := debug.SetMemoryLimit(384 << 20)
	t.Cleanup(func() {
		debug.SetMemoryLimit(previousLimit)
	})

	applyCLIGoMemoryPolicy()
	got := debug.SetMemoryLimit(-1)
	if got != 384<<20 {
		t.Fatalf("GOMEMLIMIT was overridden: limit = %d", got)
	}
}
