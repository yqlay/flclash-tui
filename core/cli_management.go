//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const defaultCLITestURL = "https://www.gstatic.com/generate_204"

func startManagedCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash start [--config PATH] [--directory PATH]")
		fmt.Println("Start the shared backend when needed, then start Core listeners.")
		return nil
	}
	paths, testURL, configExplicit, directoryExplicit, err := parseManagedPaths("start", args)
	if err != nil {
		return err
	}
	paths = preferManagedActivePaths(paths, configExplicit, directoryExplicit)
	if configExplicit {
		if err := ensureTUIConfig(paths, false); err != nil {
			return err
		}
	}
	client, current, err := ensureTUIService(
		paths,
		testURL,
		configExplicit,
		directoryExplicit,
	)
	if err != nil {
		return err
	}
	status, err := client.startAtRevision(current.Revision)
	if err != nil {
		return err
	}
	fmt.Printf("Core started (PID %d, revision %d)\n", status.PID, status.Revision)
	return nil
}

func restartManagedCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash restart")
		fmt.Println("Stop and start Core listeners without terminating the backend.")
		return nil
	}
	client, status, err := currentManagedService()
	if err != nil {
		return err
	}
	if status.Running {
		status, err = client.stopAtRevision(status.Revision)
		if err != nil {
			return err
		}
	}
	status, err = client.startAtRevision(status.Revision)
	if err != nil {
		return err
	}
	fmt.Printf("Core restarted (revision %d)\n", status.Revision)
	return nil
}

func reloadManagedCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash reload [--config PATH]")
		fmt.Println("Validate and atomically switch or reload the active profile.")
		return nil
	}
	fs := newCLIFlagSet("reload")
	configPath := fs.String("config", "", "profile YAML path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, status, err := currentManagedService()
	if err != nil {
		return err
	}
	status, err = client.reloadAtRevision(*configPath, status.Revision)
	if err != nil {
		return err
	}
	fmt.Printf("Reloaded %s (revision %d)\n", status.ConfigPath, status.Revision)
	return nil
}

func statusManagedCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash status [--json] [--watch]")
		fmt.Println("Show Backend, Core, active profile, Proxy port, mode, and frontends.")
		return nil
	}
	fs := newCLIFlagSet("status")
	jsonOutput := fs.Bool("json", false, "print JSON")
	watch := fs.Bool("watch", false, "watch revision changes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	client, status, err := currentManagedServiceRaw()
	if err != nil {
		if *jsonOutput {
			return writeCLIJSON(os.Stdout, map[string]any{
				"backend": "stopped",
				"core":    "stopped",
			})
		}
		fmt.Println("Backend: stopped")
		fmt.Println("Core:    stopped")
		return nil
	}
	for {
		if err := printManagedStatus(status, *jsonOutput); err != nil {
			return err
		}
		if !*watch {
			return nil
		}
		if err := validateCurrentTUIService(status); err != nil {
			return fmt.Errorf("status watch: %w", err)
		}
		next, watchErr := client.watch(status.Revision, 30*time.Second)
		if watchErr != nil {
			return watchErr
		}
		if next.Revision == status.Revision {
			continue
		}
		status = next
	}
}

func printManagedStatus(status tuiServiceStatus, jsonOutput bool) error {
	proxyPort := status.ProxyPort
	mode := status.Mode
	if (proxyPort == 0 || mode == "") && status.Running {
		if config, err := managedConfig(status); err == nil {
			if proxyPort == 0 {
				proxyPort = config.MixedPort
			}
			if mode == "" {
				mode = config.Mode
			}
		}
	}
	if jsonOutput {
		return writeCLIJSON(os.Stdout, map[string]any{
			"protocol_version": status.ProtocolVersion,
			"backend_version":  status.Version,
			"revision":         status.Revision,
			"backend":          "running",
			"backend_pid":      status.PID,
			"core":             cliOnOff(status.Running),
			"profile":          status.ConfigPath,
			"proxy_port":       proxyPort,
			"mixed_port":       proxyPort,
			"mode":             mode,
			"flc_enabled":      status.FLCEnabled,
			"flc_outbound":     status.FLCOutbound,
			"system_proxy":     status.SystemProxy,
			"frontends":        status.FrontendCount,
		})
	}
	fmt.Printf("Backend:      running (PID %d, revision %d)\n", status.PID, status.Revision)
	fmt.Printf("Version:      %s (protocol %d)\n", status.Version, status.ProtocolVersion)
	fmt.Printf("Core:         %s\n", cliOnOff(status.Running))
	fmt.Printf("Profile:      %s\n", status.ConfigPath)
	if mode == tuiSilentMode {
		fmt.Printf("Proxy port:   off (configured %d)\n", proxyPort)
	} else if proxyPort > 0 {
		fmt.Printf("Proxy port:   %d (Mihomo mixed-port)\n", proxyPort)
	}
	if mode != "" {
		fmt.Printf("Mode:         %s\n", mode)
	}
	if status.FLCOutbound != "" {
		fmt.Printf("FLC outbound: %s\n", status.FLCOutbound)
	}
	fmt.Printf("System proxy: %s\n", cliOnOff(status.SystemProxy))
	fmt.Printf("Frontends:    %d\n", status.FrontendCount)
	return nil
}

func logsManagedCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash logs [--follow] [--lines N]")
		return nil
	}
	fs := newCLIFlagSet("logs")
	follow := fs.Bool("follow", false, "follow appended log data")
	lines := fs.Int("lines", 100, "number of trailing lines")
	if err := fs.Parse(args); err != nil {
		return err
	}
	paths, err := resolvePaths("", "")
	if err != nil {
		return err
	}
	if _, status, statusErr := currentManagedService(); statusErr == nil && status.HomeDir != "" {
		paths.homeDir = status.HomeDir
	}
	return readManagedLog(filepath.Join(paths.homeDir, tuiServiceLogFilename), *lines, *follow)
}

func serviceManagementCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash backend [start|stop|restart|status|logs|clients]")
		fmt.Println("backend stop terminates Backend and Core and disconnects all frontends.")
		fmt.Println("Compatibility alias: flclash service")
		return nil
	}
	if len(args) == 0 {
		args = []string{"status"}
	}
	switch args[0] {
	case "start":
		paths, testURL, configExplicit, directoryExplicit, err := parseManagedPaths(
			"service start",
			args[1:],
		)
		if err != nil {
			return err
		}
		paths = preferManagedActivePaths(paths, configExplicit, directoryExplicit)
		if configExplicit {
			if err := ensureTUIConfig(paths, false); err != nil {
				return err
			}
		}
		client, status, err := ensureTUIService(
			paths,
			testURL,
			configExplicit,
			directoryExplicit,
		)
		if err != nil {
			return err
		}
		_ = client
		fmt.Printf("Backend running (PID %d, revision %d)\n", status.PID, status.Revision)
		return nil
	case "stop":
		client, _, err := currentManagedServiceRaw()
		if err != nil {
			return err
		}
		if err := client.shutdown(); err != nil {
			return err
		}
		fmt.Println("Backend stopped")
		return nil
	case "restart":
		client, status, err := currentManagedServiceRaw()
		wasRunning := false
		if err == nil {
			wasRunning = status.Running
			if err := client.shutdown(); err != nil {
				return err
			}
			waitForManagedServiceExit(client, 3*time.Second)
		}
		paths, pathErr := resolvePaths("", "")
		if pathErr != nil {
			return pathErr
		}
		if status.ConfigPath != "" {
			paths.homeDir = status.HomeDir
			paths.configPath = status.ConfigPath
		} else {
			paths = preferManagedActivePaths(paths, false, false)
		}
		client, status, err = ensureTUIService(paths, defaultCLITestURL, false, false)
		if err != nil {
			return err
		}
		if wasRunning {
			status, err = client.startAtRevision(status.Revision)
			if err != nil {
				return err
			}
		}
		fmt.Printf("Backend restarted (PID %d)\n", status.PID)
		return nil
	case "status":
		return statusManagedCommand(args[1:])
	case "logs":
		return logsManagedCommand(args[1:])
	case "clients":
		frontends, err := listCLIFrontends()
		if err != nil {
			return err
		}
		if len(frontends) == 0 {
			fmt.Println("No TUI frontends attached")
			return nil
		}
		for _, frontend := range frontends {
			fmt.Printf("PID %-7d TTY %-16s started %s\n", frontend.PID, frontend.TTY, frontend.StartedAt.Format(time.RFC3339))
		}
		return nil
	default:
		return fmt.Errorf("unknown backend command %q; use `flclash backend -help`", args[0])
	}
}

func configCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash config path|show|validate|edit|backup|restore")
		return nil
	}
	paths, err := activeCLIPaths()
	if err != nil {
		return err
	}
	switch args[0] {
	case "path":
		fmt.Println(paths.configPath)
	case "show":
		data, err := os.ReadFile(paths.configPath)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(data)
		return err
	case "validate":
		if message := handleValidateConfig(paths.configPath); message != "" {
			return errors.New(message)
		}
		fmt.Printf("configuration is valid: %s\n", paths.configPath)
	case "backup":
		client, status, err := currentManagedService()
		if err != nil {
			return err
		}
		status, err = client.backupProfile(paths.configPath, status.Revision)
		if err != nil {
			return err
		}
		fmt.Println(status.ResultPath)
	case "restore":
		client, status, err := currentManagedService()
		if err != nil {
			return err
		}
		status, err = client.restoreProfile(paths.configPath, status.Revision)
		if err != nil {
			return err
		}
		fmt.Printf("Restored %s\n", status.ResultPath)
	case "edit":
		return editManagedConfig(paths.configPath)
	default:
		return fmt.Errorf("unknown config command %q", args[0])
	}
	return nil
}

func coreCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash core [start|stop|restart|reload|status]")
		fmt.Println("This command matches the Core row on Dashboard.")
		return nil
	}
	if len(args) == 0 {
		args = []string{"status"}
	}
	switch args[0] {
	case "start":
		return startManagedCommand(args[1:])
	case "stop":
		return stopCommand(args[1:])
	case "restart":
		return restartManagedCommand(args[1:])
	case "reload":
		return reloadManagedCommand(args[1:])
	case "status":
		if len(args) != 1 {
			return errors.New("usage: flclash core status")
		}
		_, status, err := currentManagedServiceRaw()
		if err != nil {
			fmt.Println("STOPPED")
			return nil
		}
		fmt.Println(cliUpperRunning(status.Running))
		return nil
	default:
		return fmt.Errorf(
			"unknown core command %q; use `flclash core -help`",
			args[0],
		)
	}
}

func systemProxyCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash sys [on|off|status]")
		fmt.Println("`sys on` starts Core when necessary; silent mode rejects it.")
		fmt.Println("Compatibility alias: flclash system-proxy")
		return nil
	}
	if len(args) == 0 {
		args = []string{"status"}
	}
	if len(args) != 1 {
		return errors.New("usage: flclash sys [on|off|status]")
	}
	action := strings.ToLower(args[0])
	if action != "enable" && action != "disable" &&
		action != "on" && action != "off" && action != "status" {
		return errors.New("sys requires on, off, or status")
	}
	client, status, err := currentManagedService()
	if err != nil {
		return err
	}
	if action == "status" {
		fmt.Println(cliEnabledDisabled(status.SystemProxy))
		return nil
	}
	enabled := action == "enable" || action == "on"
	autoStarted := false
	if enabled && !status.Running {
		status, err = client.startAtRevision(status.Revision)
		if err != nil {
			return fmt.Errorf("start Core for System proxy: %w", err)
		}
		autoStarted = true
	}
	status, err = client.setSystemProxy(enabled, status.Revision)
	if err != nil {
		if autoStarted {
			if rolledBack, rollbackErr := client.stopAtRevision(status.Revision); rollbackErr != nil {
				return fmt.Errorf(
					"update System proxy: %v; automatic Core rollback failed: %w",
					err,
					rollbackErr,
				)
			} else {
				status = rolledBack
			}
		}
		return fmt.Errorf("update System proxy: %w", err)
	}
	fmt.Printf(
		"System proxy %s (revision %d)\n",
		cliEnabledDisabled(status.SystemProxy),
		status.Revision,
	)
	return nil
}

func tunCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash tun [on|off|status]")
		fmt.Println("This command matches the TUN row on Dashboard.")
		return nil
	}
	if len(args) == 0 {
		args = []string{"status"}
	}
	if len(args) != 1 {
		return errors.New("usage: flclash tun [on|off|status]")
	}
	action := strings.ToLower(args[0])
	if action != "enable" && action != "disable" &&
		action != "on" && action != "off" && action != "status" {
		return errors.New("tun requires on, off, or status")
	}
	client, status, settings, err := currentManagedSettings()
	if err != nil {
		return err
	}
	if action == "status" {
		fmt.Println(cliUpperOnOff(settings.TunEnabled))
		return nil
	}
	settings.TunEnabled = action == "enable" || action == "on"
	status, err = client.applySettings(*settings, status.Revision)
	if err != nil {
		return err
	}
	fmt.Printf("TUN %s (revision %d)\n", cliUpperOnOff(settings.TunEnabled), status.Revision)
	return nil
}

func modeCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash mode [rule|global|direct|silent]")
		fmt.Println("No value shows the current mode; silent allows only flc commands.")
		fmt.Println("Compatibility syntax: flclash mode get|set MODE")
		return nil
	}
	if len(args) == 0 || len(args) == 1 && (args[0] == "get" || args[0] == "status") {
		_, status, err := currentManagedService()
		if err != nil {
			return err
		}
		fmt.Println(strings.ToLower(status.Mode))
		return nil
	}
	if len(args) == 2 && args[0] == "set" {
		args = args[1:]
	}
	if len(args) != 1 {
		return errors.New("usage: flclash mode [rule|global|direct|silent]")
	}
	mode := strings.ToLower(args[0])
	if mode != "rule" && mode != "global" && mode != "direct" && mode != tuiSilentMode {
		return errors.New("mode must be rule, global, direct, or silent")
	}
	client, status, err := currentManagedService()
	if err != nil {
		return err
	}
	status, err = client.setMode(mode, status.Revision)
	if err != nil {
		return err
	}
	fmt.Printf("Mode %s (revision %d)\n", status.Mode, status.Revision)
	return nil
}

func portCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash port [PORT|off]")
		fmt.Println("No value shows the configured HTTP/SOCKS proxy port.")
		fmt.Println("Silent mode saves the value but keeps the public listener off.")
		fmt.Println("Compatibility syntax: flclash port get|set PORT")
		return nil
	}
	if len(args) == 0 || len(args) == 1 && (args[0] == "get" || args[0] == "status") {
		_, status, err := currentManagedService()
		if err != nil {
			return err
		}
		if status.Mode == tuiSilentMode {
			fmt.Printf("OFF (configured %d)\n", status.ProxyPort)
		} else if status.ProxyPort <= 0 {
			fmt.Println("OFF")
		} else {
			fmt.Println(status.ProxyPort)
		}
		return nil
	}
	if len(args) == 2 && args[0] == "set" {
		args = args[1:]
	}
	if len(args) != 1 {
		return errors.New("usage: flclash port [PORT|off]")
	}
	value := strings.ToLower(args[0])
	port := 0
	var err error
	if value != "off" {
		port, err = strconv.Atoi(value)
	}
	if err != nil || port < 0 || port > 65535 {
		return errors.New("proxy port must be a number from 1 to 65535, or off")
	}
	client, status, settings, err := currentManagedSettings()
	if err != nil {
		return err
	}
	settings.MixedPort = port
	status, err = client.applySettings(*settings, status.Revision)
	if err != nil {
		return err
	}
	if port == 0 {
		fmt.Printf("Proxy port OFF (revision %d)\n", status.Revision)
	} else {
		fmt.Printf("Proxy port %d (revision %d)\n", port, status.Revision)
	}
	return nil
}

func flcManagementCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash flc [status|select NAME|test|env]")
		fmt.Println("Manage the private command proxy used by flc in silent mode.")
		return nil
	}
	if len(args) == 0 {
		args = []string{"status"}
	}
	client, status, err := currentManagedService()
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return errors.New("usage: flclash flc status")
		}
		fmt.Printf("Mode:     %s\n", status.Mode)
		fmt.Printf("Listener: %s\n", cliEnabledDisabled(status.FLCEnabled))
		fmt.Printf("Outbound: %s\n", cliDisplayValue(status.FLCOutbound))
		return nil
	case "select":
		if len(args) != 2 {
			return errors.New("usage: flclash flc select NAME")
		}
		status, err = client.setFLCOutbound(args[1], status.Revision)
		if err != nil {
			return err
		}
		fmt.Printf("FLC outbound %s (revision %d)\n", status.FLCOutbound, status.Revision)
		return nil
	case "test":
		if len(args) != 1 {
			return errors.New("usage: flclash flc test")
		}
		proxyAddress, err := activeCLIProxyURL()
		if err != nil {
			return err
		}
		proxyURL, err := url.Parse(proxyAddress)
		if err != nil || proxyURL.Scheme == "" || proxyURL.Host == "" {
			return errors.New("FlClash returned an invalid command proxy URL")
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = http.ProxyURL(proxyURL)
		transport.DisableCompression = true
		defer transport.CloseIdleConnections()
		delay, err := runTUIRouteDelayTest(
			context.Background(),
			&http.Client{Transport: transport},
			defaultCLITestURL,
		)
		if err != nil {
			return fmt.Errorf("FLC route test failed: %w", err)
		}
		fmt.Printf("FLC route ready · %d ms · %s\n", delay, cliDisplayValue(status.FLCOutbound))
		return nil
	case "env":
		return envCommand(args[1:])
	default:
		return fmt.Errorf("unknown flc command %q; use `flclash flc -help`", args[0])
	}
}

func cliDisplayValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "NOT SELECTED"
	}
	return value
}

func historyCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash history show [--follow] [--json] | clear")
		fmt.Println("History is the shared recent connection history shown by the TUI.")
		fmt.Println("Compatibility alias: flclash requests")
		return nil
	}
	if len(args) == 0 {
		args = []string{"show"}
	}
	client, status, err := currentManagedService()
	if err != nil {
		return err
	}
	switch args[0] {
	case "show", "watch":
		fs := newCLIFlagSet("history show")
		follow := fs.Bool("follow", args[0] == "watch", "follow new history entries")
		jsonOutput := fs.Bool("json", false, "print JSON")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 0 {
			return errors.New("usage: flclash history show [--follow] [--json]")
		}
		seen := map[string]bool{}
		interrupt := make(chan os.Signal, 1)
		if *follow {
			signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)
			defer signal.Stop(interrupt)
		}
		for {
			status, err = client.history()
			if err != nil {
				return err
			}
			if *jsonOutput {
				if err := writeCLIJSON(os.Stdout, status.History); err != nil {
					return err
				}
			} else {
				printCLIHistory(status.History, seen, *follow)
			}
			if !*follow {
				return nil
			}
			select {
			case <-interrupt:
				return nil
			case <-time.After(time.Second):
			}
		}
	case "clear":
		if len(args) != 1 {
			return errors.New("usage: flclash history clear")
		}
		status, err = client.clearHistory(status.Revision)
		if err != nil {
			return err
		}
		fmt.Printf("History cleared (revision %d)\n", status.Revision)
		return nil
	default:
		return fmt.Errorf("unknown history command %q; use `flclash history -help`", args[0])
	}
}

func printCLIHistory(history []tuiRequest, seen map[string]bool, onlyNew bool) {
	for index := len(history) - 1; index >= 0; index-- {
		request := history[index]
		if onlyNew && seen[request.ID] {
			continue
		}
		state := "done"
		if request.Active {
			state = "active"
		}
		fmt.Printf(
			"%s %-6s %-4s %-36s %s\n",
			request.FirstSeen.Format("15:04:05"),
			state,
			strings.ToUpper(request.Network),
			request.Host,
			request.Chain,
		)
		seen[request.ID] = true
	}
}

func networkCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash net [show|refresh|delay|speed]")
		fmt.Println("Matches Network detection on Dashboard.")
		return nil
	}
	if len(args) == 0 {
		args = []string{"show"}
	}
	if len(args) != 1 {
		return errors.New("usage: flclash net [show|refresh|delay|speed]")
	}
	client, status, err := currentManagedService()
	if err != nil {
		return err
	}
	proxyPort := 0
	if status.Running && status.Mode != tuiSilentMode {
		proxyPort = status.ProxyPort
	}
	switch args[0] {
	case "show", "refresh":
		info := detectTUINetwork(proxyPort)
		fmt.Printf("Public IP:   %s\n", cliDisplayValue(info.PublicIP))
		fmt.Printf("Country:     %s\n", cliDisplayValue(info.Country))
		fmt.Printf("Intranet IP: %s\n", cliDisplayValue(info.IntranetIP))
		fmt.Printf("Route:       %s\n", info.Route)
		if info.Error != "" {
			return errors.New(info.Error)
		}
		return nil
	case "delay":
		if proxyPort <= 0 {
			return errors.New("normal Proxy port is not active; use `flclash flc test` in silent mode")
		}
		delay, err := client.testRouteDelay(proxyPort, defaultCLITestURL)
		if err != nil {
			return err
		}
		fmt.Printf("Route latency: %d ms\n", delay)
		return nil
	case "speed":
		if proxyPort <= 0 {
			return errors.New("normal Proxy port is not active; use `flclash flc test` in silent mode")
		}
		result, err := client.testRouteSpeed(proxyPort)
		if err != nil {
			return err
		}
		fmt.Printf("Download test: %s\n", formatTUISpeed(result))
		return nil
	default:
		return fmt.Errorf("unknown net command %q; use `flclash net -help`", args[0])
	}
}

func currentManagedSettings() (
	*tuiServiceClient,
	tuiServiceStatus,
	*tuiSettings,
	error,
) {
	client, status, err := currentManagedService()
	if err != nil {
		return nil, tuiServiceStatus{}, nil, err
	}
	settings := loadTUIConfiguredSettings(status.ConfigPath, true)
	if settings == nil {
		return nil, tuiServiceStatus{}, nil, errors.New("could not load active settings")
	}
	return client, status, settings, nil
}

func cliUpperRunning(value bool) string {
	if value {
		return "RUNNING"
	}
	return "STOPPED"
}

func cliEnabledDisabled(value bool) string {
	if value {
		return "ENABLED"
	}
	return "DISABLED"
}

func cliUpperOnOff(value bool) string {
	if value {
		return "ON"
	}
	return "OFF"
}

func connectionsCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash connections [show] | close ID | close all")
		return nil
	}
	if len(args) == 0 {
		args = []string{"show"}
	}
	_, status, err := currentManagedService()
	if err != nil {
		return err
	}
	client := managedController(status)
	switch args[0] {
	case "list", "show":
		data, err := client.request(http.MethodGet, "/connections", nil)
		if err != nil {
			return err
		}
		if len(args) > 1 && args[1] == "--json" {
			_, err = os.Stdout.Write(append(data, '\n'))
			return err
		}
		return printCLIConnections(data)
	case "close":
		if len(args) != 2 {
			return errors.New("usage: flclash connections close ID|all")
		}
		if args[1] == "all" {
			return client.closeAllConnections()
		}
		return client.closeConnection(args[1])
	case "close-all":
		return client.closeAllConnections()
	default:
		return fmt.Errorf("unknown connections command %q", args[0])
	}
}

func geoCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash geo status|update")
		return nil
	}
	paths, err := activeCLIPaths()
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		for _, file := range tuiBundledGeoFiles {
			path := filepath.Join(paths.homeDir, file.name)
			state := "missing"
			if tuiGeoTargetIsUsable(path, file) {
				state = "ready"
			}
			fmt.Printf("%-16s %s\n", file.name, state)
		}
		return nil
	case "update":
		_, status, err := currentManagedService()
		if err != nil {
			return err
		}
		if err := managedController(status).updateGeo(); err != nil {
			return err
		}
		fmt.Println("Geo database update started")
		return nil
	default:
		return fmt.Errorf("unknown geo command %q", args[0])
	}
}

func envCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash env [--json]")
		return nil
	}
	proxyURL, err := activeCLIProxyURL()
	if err != nil {
		return err
	}
	if len(args) == 1 && args[0] == "--json" {
		return writeCLIJSON(os.Stdout, map[string]string{
			"HTTP_PROXY": proxyURL, "HTTPS_PROXY": proxyURL, "ALL_PROXY": proxyURL,
		})
	}
	fmt.Printf("export HTTP_PROXY=%s\n", shellQuote(proxyURL))
	fmt.Printf("export HTTPS_PROXY=%s\n", shellQuote(proxyURL))
	fmt.Printf("export ALL_PROXY=%s\n", shellQuote(proxyURL))
	return nil
}

func doctorCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash doctor [--json]")
		return nil
	}
	checks := map[string]any{"version": cliVersion}
	client, status, err := currentManagedService()
	checks["backend"] = err == nil
	if err != nil {
		checks["error"] = err.Error()
		return printDoctor(checks, args)
	}
	checks["protocol"] = status.ProtocolVersion
	checks["revision"] = status.Revision
	checks["core_running"] = status.Running
	checks["config_path"] = status.ConfigPath
	checks["config_valid"] = handleValidateConfig(status.ConfigPath) == ""
	_, controllerErr := managedController(status).request(http.MethodGet, "/version", nil)
	checks["controller"] = controllerErr == nil
	if status.Running {
		_, proxyErr := activeCLIProxyURL()
		checks["proxy_entry"] = proxyErr == nil
		if proxyErr != nil {
			checks["proxy_entry_error"] = proxyErr.Error()
		}
	}
	_ = client
	return printDoctor(checks, args)
}

func completionCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash completion bash|zsh|fish")
		return nil
	}
	commands := "tui core sys tun mode port flc net start stop restart reload status backend shutdown profile proxy history connections logs config geo env doctor completion check update run version help"
	groups := []struct {
		command string
		values  string
	}{
		{"tui", "--config --directory --controller --secret --test-url --no-start"},
		{"core", "start stop restart reload status"},
		{"sys", "on off status"},
		{"tun", "on off status"},
		{"mode", "rule global direct silent"},
		{"port", "off"},
		{"flc", "status select test env"},
		{"net", "show refresh delay speed"},
		{"status", "--json --watch"},
		{"backend", "start stop restart status logs clients"},
		{"profile", "list import current use update rename edit delete link"},
		{"proxy", "groups nodes select delay speed"},
		{"history", "show clear"},
		{"connections", "show close"},
		{"logs", "--follow --lines"},
		{"config", "path show validate edit backup restore"},
		{"geo", "status update"},
		{"env", "--json"},
		{"doctor", "--json"},
		{"completion", "bash zsh fish"},
		{"check", "--config --directory"},
		{"update", "--check --download-only --yes"},
		{"run", "--config --directory --test-url"},
	}
	switch args[0] {
	case "bash":
		fmt.Println("_flclash() {")
		fmt.Println("  local current words")
		fmt.Println("  current=\"${COMP_WORDS[COMP_CWORD]}\"")
		fmt.Println("  if (( COMP_CWORD == 1 )); then")
		fmt.Printf("    words='%s'\n", commands)
		fmt.Println("  elif (( COMP_CWORD == 3 )) && [[ ${COMP_WORDS[1]} == connections && ${COMP_WORDS[2]} == close ]]; then")
		fmt.Println("    words='all'")
		fmt.Println("  else")
		fmt.Println("    case \"${COMP_WORDS[1]}\" in")
		for _, group := range groups {
			fmt.Printf("      %s) words='%s' ;;\n", group.command, group.values)
		}
		fmt.Println("      *) words='' ;;")
		fmt.Println("    esac")
		fmt.Println("  fi")
		fmt.Println("  COMPREPLY=( $(compgen -W \"$words\" -- \"$current\") )")
		fmt.Println("}")
		fmt.Println("complete -F _flclash flclash")
	case "zsh":
		fmt.Printf("#compdef flclash\n_arguments '1:command:(%s)' '*::argument:->args'\n", commands)
		fmt.Println("case $words[2] in")
		for _, group := range groups {
			if group.command == "connections" {
				fmt.Printf(
					"  connections) if (( CURRENT >= 4 )) && [[ $words[3] == close ]]; then _values 'argument' all; else _values 'argument' %s; fi ;;\n",
					group.values,
				)
				continue
			}
			fmt.Printf("  %s) _values 'argument' %s ;;\n", group.command, group.values)
		}
		fmt.Println("esac")
	case "fish":
		for _, command := range strings.Fields(commands) {
			fmt.Printf("complete -c flclash -f -n '__fish_use_subcommand' -a %s\n", command)
		}
		for _, group := range groups {
			for _, value := range strings.Fields(group.values) {
				fmt.Printf(
					"complete -c flclash -f -n '__fish_seen_subcommand_from %s' -a %s\n",
					group.command,
					value,
				)
			}
		}
		fmt.Println("complete -c flclash -f -n '__fish_seen_subcommand_from connections; and __fish_seen_subcommand_from close' -a all")
	default:
		return fmt.Errorf("unsupported shell %q", args[0])
	}
	return nil
}

func parseManagedPaths(
	name string,
	args []string,
) (cliPaths, string, bool, bool, error) {
	fs := newCLIFlagSet(name)
	configArg := fs.String("config", "", "path to config.yaml")
	directoryArg := fs.String("directory", "", "FlClash data directory")
	testURL := fs.String("test-url", defaultCLITestURL, "proxy delay test URL")
	if err := fs.Parse(args); err != nil {
		return cliPaths{}, "", false, false, err
	}
	paths, err := resolvePaths(*configArg, *directoryArg)
	return paths, *testURL, *configArg != "", *directoryArg != "", err
}

func preferManagedActivePaths(
	paths cliPaths,
	explicitConfig,
	explicitDirectory bool,
) cliPaths {
	if explicitConfig || explicitDirectory {
		return paths
	}
	if _, status, err := currentManagedServiceRaw(); err == nil &&
		status.HomeDir != "" && status.ConfigPath != "" {
		if _, pathErr := tuiProfileStateKey(
			status.HomeDir,
			status.ConfigPath,
		); pathErr == nil {
			paths.homeDir = status.HomeDir
			paths.configPath = status.ConfigPath
			return paths
		}
	}
	if restored, err := restoreTUIActiveProfile(paths); err == nil {
		return restored
	}
	return paths
}

func currentManagedService() (*tuiServiceClient, tuiServiceStatus, error) {
	client, status, err := currentManagedServiceRaw()
	if err != nil {
		return nil, tuiServiceStatus{}, err
	}
	if err := validateCurrentTUIService(status); err != nil {
		return nil, tuiServiceStatus{}, err
	}
	return client, status, nil
}

func currentManagedServiceRaw() (*tuiServiceClient, tuiServiceStatus, error) {
	paths, err := resolvePaths("", "")
	if err != nil {
		return nil, tuiServiceStatus{}, err
	}
	client := newTUIServiceClient(paths.homeDir)
	status, err := client.status()
	if err != nil {
		return nil, tuiServiceStatus{}, errors.New("no FlClash backend is running")
	}
	return client, status, nil
}

func validateCurrentTUIService(status tuiServiceStatus) error {
	if status.Version == cliVersion &&
		status.ProtocolVersion == tuiServiceProtocolVersion {
		return nil
	}
	return fmt.Errorf(
		"backend %s uses protocol %d; run `flclash backend restart` to upgrade to %s protocol %d",
		status.Version,
		status.ProtocolVersion,
		cliVersion,
		tuiServiceProtocolVersion,
	)
}

func activeCLIPaths() (cliPaths, error) {
	paths, err := resolvePaths("", "")
	if err != nil {
		return cliPaths{}, err
	}
	if _, status, statusErr := currentManagedService(); statusErr == nil {
		paths.homeDir = status.HomeDir
		paths.configPath = status.ConfigPath
	} else if restored, restoreErr := restoreTUIActiveProfile(paths); restoreErr == nil {
		paths = restored
	}
	return paths, nil
}

func managedController(status tuiServiceStatus) controllerClient {
	options := controllerOptions{unixSocket: status.CoreSocket}
	return controllerClient{
		options: options,
		client:  controllerHTTPClientForOptions(options, controllerRequestTimeout),
	}
}

func managedConfig(status tuiServiceStatus) (tuiConfigResponse, error) {
	data, err := managedController(status).request(http.MethodGet, "/configs", nil)
	if err != nil {
		return tuiConfigResponse{}, err
	}
	var config tuiConfigResponse
	if err := json.Unmarshal(data, &config); err != nil {
		return tuiConfigResponse{}, err
	}
	return config, nil
}

func newCLIFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func cliSubcommandHelp(args []string) bool {
	return len(args) > 0 && (isCLIHelpArg(args[0]) || args[0] == "help")
}

func writeCLIJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func cliOnOff(value bool) string {
	if value {
		return "running"
	}
	return "stopped"
}

func waitForManagedServiceExit(client *tuiServiceClient, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := client.status(); err != nil {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func readManagedLog(path string, lineCount int, follow bool) error {
	if lineCount < 0 {
		return errors.New("lines must not be negative")
	}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	if lineCount > 0 && len(lines) > lineCount {
		lines = lines[len(lines)-lineCount:]
	}
	for _, line := range lines {
		fmt.Println(line)
	}
	if !follow {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(interrupt)
	reader := bufio.NewReader(file)
	for {
		line, readErr := reader.ReadString('\n')
		if line != "" {
			fmt.Print(line)
		}
		if readErr == nil {
			continue
		}
		if !errors.Is(readErr, io.EOF) {
			return readErr
		}
		select {
		case <-interrupt:
			return nil
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func editManagedConfig(path string) error {
	before, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	expectedSHA256 := tuiBytesSHA256(before)
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		return errors.New("set $VISUAL or $EDITOR before using config edit")
	}
	temporary, err := os.CreateTemp("", "flclash-edit-*.yaml")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(before); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	command := exec.Command(
		"sh",
		"-c",
		editor+" -- \"$1\"",
		"flclash-editor",
		temporaryPath,
	)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return err
	}
	edited, err := os.ReadFile(temporaryPath)
	if err != nil {
		return err
	}
	if message := validateConfigBytes(edited); message != "" {
		return errors.New("edited configuration is invalid: " + message)
	}
	client, status, err := currentManagedService()
	if err != nil {
		return err
	}
	_, err = client.putProfile(
		path,
		edited,
		expectedSHA256,
		false,
		nil,
		status.Revision,
	)
	if err != nil {
		return err
	}
	return nil
}

func printCLIConnections(data []byte) error {
	var response struct {
		Connections []struct {
			ID       string `json:"id"`
			Metadata struct {
				Host            string `json:"host"`
				DestinationIP   string `json:"destinationIP"`
				DestinationPort string `json:"destinationPort"`
				Process         string `json:"process"`
			} `json:"metadata"`
			Chains []string `json:"chains"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return err
	}
	for _, connection := range response.Connections {
		host := connection.Metadata.Host
		if host == "" {
			host = formatTUIDestination(
				connection.Metadata.DestinationIP,
				connection.Metadata.DestinationPort,
			)
		}
		chain := "DIRECT"
		if len(connection.Chains) > 0 {
			chain = connection.Chains[len(connection.Chains)-1]
		}
		fmt.Printf("%s\t%s\t%s\t%s\n", connection.ID, host, connection.Metadata.Process, chain)
	}
	return nil
}

func printDoctor(checks map[string]any, args []string) error {
	if len(args) == 1 && args[0] == "--json" {
		return writeCLIJSON(os.Stdout, checks)
	}
	keys := make([]string, 0, len(checks))
	for key := range checks {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Printf("%-18s %v\n", key, checks[key])
	}
	return nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func parseCLIInt(value, name string) (int, error) {
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s %q", name, value)
	}
	return parsed, nil
}
