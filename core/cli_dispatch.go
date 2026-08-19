//go:build linux && !cgo && cli

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

func dispatchCLI(program string, args []string) error {
	if program == "flc" {
		return dispatchFLC(args)
	}
	if len(args) == 0 {
		return tuiCommand(nil)
	}
	if isCLIHelpArg(args[0]) || args[0] == "help" {
		printUsage(os.Stdout)
		return nil
	}

	command := args[0]
	commandArgs := args[1:]
	switch command {
	case "tui", "ui":
		return tuiCommand(commandArgs)
	case "run":
		return runCommand(commandArgs)
	case "core":
		return coreCommand(commandArgs)
	case "start":
		return startManagedCommand(commandArgs)
	case "stop":
		return stopCommand(commandArgs)
	case "restart":
		return restartManagedCommand(commandArgs)
	case "reload":
		return reloadManagedCommand(commandArgs)
	case "status":
		return statusManagedCommand(commandArgs)
	case "logs":
		return logsManagedCommand(commandArgs)
	case "backend", "service":
		return serviceManagementCommand(commandArgs)
	case "shutdown":
		return serviceManagementCommand([]string{"stop"})
	case "_service":
		return serviceCommand(commandArgs)
	case "check", "validate":
		return checkCommand(commandArgs)
	case "proxy":
		return proxyCommand(commandArgs)
	case "profile":
		return profileCommand(commandArgs)
	case "config":
		return configCommand(commandArgs)
	case "sys", "system-proxy":
		return systemProxyCommand(commandArgs)
	case "tun":
		return tunCommand(commandArgs)
	case "tun-helper":
		if len(commandArgs) != 1 || commandArgs[0] != "serve" {
			return errors.New("usage: flclash tun-helper serve")
		}
		return runTUITunHelper()
	case "mode", "outbound-mode":
		return modeCommand(commandArgs)
	case "port", "mixed-port":
		return portCommand(commandArgs)
	case "flc":
		return flcManagementCommand(commandArgs)
	case "history", "requests":
		return historyCommand(commandArgs)
	case "net":
		return networkCommand(commandArgs)
	case "connections", "connection":
		return connectionsCommand(commandArgs)
	case "geo":
		return geoCommand(commandArgs)
	case "env":
		return envCommand(commandArgs)
	case "doctor":
		return doctorCommand(commandArgs)
	case "completion", "completions":
		return completionCommand(commandArgs)
	case "update", "upgrade":
		return updateCommand(commandArgs)
	case "exec":
		if cliSubcommandHelp(commandArgs) {
			printFLCUsage(os.Stdout)
			return nil
		}
		if len(commandArgs) > 0 && commandArgs[0] == "--" {
			commandArgs = commandArgs[1:]
		}
		return wrappedCommand(commandArgs)
	case "--":
		return wrappedCommand(commandArgs)
	case "version", "--version", "-v":
		fmt.Printf("FlClash TUI %s (Mihomo core)\n", cliVersion)
		return nil
	default:
		if strings.HasPrefix(command, "-") {
			return fmt.Errorf(
				"unknown option %q; use `flclash -help`",
				command,
			)
		}
		return fmt.Errorf(
			"unknown flclash command %q; use `flclash -help`, or `flc %s ...` to run an external command",
			command,
			command,
		)
	}
}

func dispatchFLC(args []string) error {
	if len(args) == 0 || isCLIHelpArg(args[0]) || args[0] == "help" {
		printFLCUsage(os.Stdout)
		return nil
	}
	if args[0] == "--version" || args[0] == "-v" || args[0] == "version" {
		fmt.Printf("flc %s (FlClash command proxy)\n", cliVersion)
		return nil
	}
	if args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		return errors.New("flc requires a command after --")
	}
	return wrappedCommand(args)
}

func printFLCUsage(w io.Writer) {
	fmt.Fprintln(w, "flc - run one external command through the active FlClash proxy entry")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  flc COMMAND [ARG...]")
	fmt.Fprintln(w, "  flc -- COMMAND [ARG...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  flc curl https://example.com")
	fmt.Fprintln(w, "  flc wget https://example.com/file")
	fmt.Fprintln(w, "  flc git clone https://github.com/owner/repository.git")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "In silent mode flc uses an authenticated private listener; other modes use Proxy port.")
	fmt.Fprintln(w, "flc fails closed when its proxy entry is unavailable; it never silently runs direct.")
}

func isCLIHelpArg(value string) bool {
	switch value {
	case "-help", "--help", "-h":
		return true
	default:
		return false
	}
}
