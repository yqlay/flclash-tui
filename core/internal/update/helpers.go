//go:build linux && !cgo && cli

package update

import "fmt"

func cliSubcommandHelp(args []string) bool {
	return len(args) > 0 && (isCLIHelpArg(args[0]) || args[0] == "help")
}

func isCLIHelpArg(value string) bool {
	switch value {
	case "-h", "-help", "--help":
		return true
	default:
		return false
	}
}

func formatBytes(value int64) string {
	units := []string{"B", "KB", "MB", "GB"}
	amount := float64(value)
	unit := 0
	for amount >= 1024 && unit < len(units)-1 {
		amount /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", amount, units[unit])
}
