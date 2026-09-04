//go:build linux && !cgo && cli

package main

import (
	"os"
	"runtime/debug"
	"strings"
	"time"
)

// cliManagedCoreMemoryLimit is a soft cap on Go runtime mappings for the
// Backend/Core process. Live proxy, rules, and Geo still work; unused heap
// is returned to the OS instead of sitting at 1GB+ RSS. Users can raise or
// disable it with GOMEMLIMIT.
const cliManagedCoreMemoryLimit = 512 << 20

const tuiCoreMemoryScavengeInterval = 5 * time.Minute

func applyCLIGoMemoryPolicy() {
	if strings.TrimSpace(os.Getenv("GOMEMLIMIT")) != "" {
		return
	}
	debug.SetMemoryLimit(cliManagedCoreMemoryLimit)
}
