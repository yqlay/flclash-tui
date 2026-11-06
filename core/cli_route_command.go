//go:build linux && !cgo && cli

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// reloadProxyRouteBackend reloads a running Backend after a proxy-list change.
// An explicit --directory never calls it, so command tests stay off the live socket.
var reloadProxyRouteBackend = reloadProxyRouteIfBackendRunning

func routeCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		printProxyRouteUsage(os.Stdout)
		return nil
	}
	return runRouteCommand(args, proxyRoutePolicyFilename, true)
}

func sshRouteCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		printSSHRouteUsage(os.Stdout)
		return nil
	}
	return runRouteCommand(args, sshRoutePolicyFilename, false)
}

func printProxyRouteUsage(w io.Writer) {
	fmt.Fprintln(w, "Whitelist must use the selected proxy. Blacklist stays DIRECT.")
	fmt.Fprintln(w, "Usage: flclash route whitelist|blacklist list|add|delete [ENTRY] [--directory PATH]")
	fmt.Fprintln(w, "Dashboard stores this list. It is separate from `flclash ssh route`.")
}

func printSSHRouteUsage(w io.Writer) {
	fmt.Fprintln(w, "Blacklist dials this machine directly. The whitelist is stored and does not change the dial.")
	fmt.Fprintln(w, "Usage: flclash ssh route whitelist|blacklist list|add|delete [ENTRY] [--directory PATH]")
	fmt.Fprintln(w, "`flc ssh` uses the blacklist only, and only without -d. This list is separate from Dashboard.")
}

type routeInvocation struct {
	list              string
	action            string
	entry             string
	directory         string
	config            string
	explicitDirectory bool
}

func parseRouteInvocation(args []string) (routeInvocation, error) {
	var invocation routeInvocation
	var positionals []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		switch {
		case arg == "--directory" || arg == "--config":
			if index+1 >= len(args) || strings.HasPrefix(args[index+1], "-") {
				return routeInvocation{}, errors.New("usage: flclash route whitelist|blacklist list|add|delete [ENTRY]")
			}
			if arg == "--directory" {
				invocation.directory = args[index+1]
				invocation.explicitDirectory = true
			} else {
				invocation.config = args[index+1]
			}
			index++
		case strings.HasPrefix(arg, "--directory="):
			invocation.directory = strings.TrimPrefix(arg, "--directory=")
			invocation.explicitDirectory = true
		case strings.HasPrefix(arg, "--config="):
			invocation.config = strings.TrimPrefix(arg, "--config=")
		case arg == "--":
			positionals = append(positionals, args[index+1:]...)
			index = len(args)
		case strings.HasPrefix(arg, "-"):
			return routeInvocation{}, fmt.Errorf("unknown flag %q", arg)
		default:
			positionals = append(positionals, arg)
		}
	}
	if len(positionals) < 2 {
		return routeInvocation{}, errors.New("usage: flclash route whitelist|blacklist list|add|delete [ENTRY]")
	}
	invocation.list = positionals[0]
	invocation.action = positionals[1]
	if invocation.list != routeListWhitelist && invocation.list != routeListBlacklist {
		return routeInvocation{}, fmt.Errorf("unknown route list %q", invocation.list)
	}
	switch invocation.action {
	case "list", "ls":
		if len(positionals) != 2 {
			return routeInvocation{}, errors.New("usage: flclash route whitelist|blacklist list")
		}
		invocation.action = "list"
	case "add":
		if len(positionals) != 3 {
			return routeInvocation{}, errors.New("usage: flclash route whitelist|blacklist add ENTRY")
		}
		invocation.entry = positionals[2]
	case "delete", "remove", "rm":
		if len(positionals) != 3 {
			return routeInvocation{}, errors.New("usage: flclash route whitelist|blacklist delete ENTRY")
		}
		invocation.action = "delete"
		invocation.entry = positionals[2]
	default:
		return routeInvocation{}, fmt.Errorf("unknown route action %q", invocation.action)
	}
	return invocation, nil
}

func runRouteCommand(args []string, filename string, reload bool) error {
	invocation, err := parseRouteInvocation(args)
	if err != nil {
		return err
	}
	paths, err := resolvePaths(invocation.config, invocation.directory)
	if err != nil {
		return err
	}
	switch invocation.action {
	case "list":
		policy, err := loadRoutePolicy(paths.HomeDir, filename)
		if err != nil {
			return err
		}
		for _, entry := range policy.entries(invocation.list) {
			fmt.Println(entry)
		}
	case "add":
		added, err := addRouteEntry(paths.HomeDir, filename, invocation.list, invocation.entry)
		if err != nil {
			return err
		}
		fmt.Printf("Added %s\n", added)
	case "delete":
		removed, err := deleteRouteEntry(paths.HomeDir, filename, invocation.list, invocation.entry)
		if err != nil {
			return err
		}
		fmt.Printf("Removed %s\n", removed)
	}
	if routeCommandShouldReload(reload, invocation) {
		return reloadProxyRouteBackend()
	}
	return nil
}

func routeCommandShouldReload(reload bool, invocation routeInvocation) bool {
	return reload && !invocation.explicitDirectory && invocation.action != "list"
}

func reloadProxyRouteIfBackendRunning() error {
	client, status, err := currentManagedServiceRaw()
	if err != nil {
		return nil
	}
	if _, err := client.reload(status.ConfigPath); err != nil {
		return fmt.Errorf("saved route list, but reload failed: %w", err)
	}
	fmt.Println("Reloaded")
	return nil
}
