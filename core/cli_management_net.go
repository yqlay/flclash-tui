//go:build linux && !cgo && cli

package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

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
	if enabled && status.Mode == tuiSilentMode {
		return errors.New("System proxy cannot be enabled in silent mode; switch mode first")
	}
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
		fmt.Println("Usage: flclash tun [user|system] [on|off]")
		fmt.Println("       flclash tun status")
		fmt.Println("Without a scope, on/off uses the current-user TUN scope.")
		return nil
	}
	if len(args) == 0 {
		args = []string{"status"}
	}
	if len(args) > 2 {
		return errors.New("usage: flclash tun [user|system] [on|off]")
	}
	scope := tuiTunScopeUser
	action := strings.ToLower(args[len(args)-1])
	if len(args) == 2 {
		scope = strings.ToLower(args[0])
	}
	if action != "enable" && action != "disable" &&
		action != "on" && action != "off" && action != "status" {
		return errors.New("tun requires on, off, or status")
	}
	client, status, err := currentManagedService()
	if err != nil {
		return err
	}
	if action == "status" {
		if status.TunState == "on" {
			fmt.Printf("%s ON", strings.ToUpper(status.TunScope))
			if status.TunScope == tuiTunScopeSystem {
				fmt.Printf(" (UID %d, PID %d)", status.TunOwnerUID, status.TunOwnerPID)
			}
			fmt.Println()
		} else {
			fmt.Printf("OFF · %s\n", status.TunScope)
		}
		return nil
	}
	enabled := action == "enable" || action == "on"
	status, err = client.setTun(enabled, scope, status.Revision)
	if err != nil {
		return err
	}
	fmt.Printf("TUN %s %s (revision %d)\n", strings.ToUpper(status.TunScope), cliUpperOnOff(enabled), status.Revision)
	return nil
}

func modeCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash mode [rule|global|direct|silent]")
		fmt.Println("No value shows the current mode; silent is the default and allows only flc commands.")
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
		proxyPort := status.ConfiguredProxyPort
		if proxyPort <= 0 {
			fmt.Println("OFF")
		} else {
			fmt.Println(proxyPort)
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
		fmt.Println("Usage: flclash flc [status|select NAME|test|env|ssh ...]")
		fmt.Println("Inspect the private command proxy used by flc in silent mode.")
		fmt.Println("Selecting a node in Proxies, or `flclash proxy select GROUP NODE`, points flc at that group.")
		fmt.Println("`flc select NAME` is an optional override.")
		return nil
	}
	if len(args) == 0 {
		args = []string{"status"}
	}
	if args[0] == "ssh" {
		return flcSSHCommand(args[1:])
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
		fmt.Printf("Exit:     %s\n", formatCLIFLCOutbound(status))
		return nil
	case "select":
		if len(args) != 2 {
			return errors.New("usage: flclash flc select NAME")
		}
		status, err = client.setFLCOutbound(args[1], status.Revision)
		if err != nil {
			return err
		}
		fmt.Printf("flc exit %s (revision %d)\n", status.FLCOutbound, status.Revision)
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
		fmt.Printf(
			"FLC route ready · %s · %s\n",
			formatTUIDelay(delay),
			cliDisplayValue(status.FLCOutbound),
		)
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
		fmt.Println("Usage: flclash history show [--follow] [--json] [--source mixed|proxy|ssh] [--state all|active|done] [--search TEXT] [--limit N] | clear [--source mixed|proxy|ssh]")
		fmt.Println("History is the shared recent connection history shown by the TUI.")
		fmt.Println("Entries are labeled proxy (Mihomo) or ssh (independent reverse proxy).")
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
		sourceFilter := fs.String("source", tuiTrafficSourceMixed, "filter by mixed, proxy, or ssh")
		stateFilter := fs.String("state", "all", "filter by all, active, or done")
		search := fs.String("search", "", "match host, process, network, or proxy chain")
		limit := fs.Int("limit", 0, "maximum matching entries; 0 shows all")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 0 {
			return errors.New("usage: flclash history show [--follow] [--json] [--source mixed|proxy|ssh] [--state all|active|done] [--search TEXT] [--limit N]")
		}
		if err := validateTrafficSourceFlag(*sourceFilter); err != nil {
			return err
		}
		*stateFilter = strings.ToLower(strings.TrimSpace(*stateFilter))
		if *stateFilter != "all" && *stateFilter != "active" && *stateFilter != "done" {
			return errors.New("history state must be all, active, or done")
		}
		if *limit < 0 || *limit > tuiRequestHistoryLimit {
			return fmt.Errorf("history limit must be between 0 and %d", tuiRequestHistoryLimit)
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
			filtered := filterCLIHistory(
				status.History,
				*sourceFilter,
				*stateFilter,
				*search,
				*limit,
			)
			if *jsonOutput {
				if err := writeCLIJSON(os.Stdout, filtered); err != nil {
					return err
				}
			} else {
				printCLIHistory(filtered, seen, *follow)
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
		fs := newCLIFlagSet("history clear")
		sourceFilter := fs.String("source", tuiTrafficSourceMixed, "clear mixed, proxy, or ssh history")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 0 {
			return errors.New("usage: flclash history clear [--source mixed|proxy|ssh]")
		}
		if err := validateTrafficSourceFlag(*sourceFilter); err != nil {
			return err
		}
		status, err = client.clearHistoryForSource(*sourceFilter, status.Revision)
		if err != nil {
			return err
		}
		fmt.Printf("History cleared (revision %d)\n", status.Revision)
		return nil
	default:
		return fmt.Errorf("unknown history command %q; use `flclash history -help`", args[0])
	}
}

func validateTrafficSourceFlag(value string) error {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", tuiTrafficSourceMixed, tuiTrafficSourceProxy, tuiTrafficSourceSSH:
		return nil
	default:
		return errors.New("source must be mixed, proxy, or ssh")
	}
}

func filterCLIHistory(
	history []tuiRequest,
	sourceFilter,
	stateFilter,
	search string,
	limit int,
) []tuiRequest {
	needle := strings.ToLower(strings.TrimSpace(search))
	filtered := make([]tuiRequest, 0, len(history))
	for _, request := range history {
		if !trafficSourceMatches(sourceFilter, tuiConnectionSource(request.TuiConnection)) {
			continue
		}
		if stateFilter == "active" && !request.Active ||
			stateFilter == "done" && request.Active {
			continue
		}
		if needle != "" {
			haystack := strings.ToLower(strings.Join([]string{
				request.Host,
				request.Process,
				request.ProcessPath,
				request.Network,
				request.Chain,
				tuiConnectionSource(request.TuiConnection),
			}, " "))
			if !strings.Contains(haystack, needle) {
				continue
			}
		}
		filtered = append(filtered, request)
		if limit > 0 && len(filtered) == limit {
			break
		}
	}
	return filtered
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
			"%s %-5s %-6s %-4s %-36s %s\n",
			request.FirstSeen.Format("15:04:05"),
			tuiConnectionSource(request.TuiConnection),
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
		fmt.Printf("Route latency: %s\n", formatTUIDelay(delay))
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
		fmt.Println("Usage: flclash connections [show [--json] [--source mixed|proxy|ssh]] | close ID | close all [--source mixed|proxy|ssh]")
		fmt.Println("Active Mihomo proxy flows and SSH reverse-proxy flows share this list.")
		return nil
	}
	if len(args) == 0 {
		args = []string{"show"}
	}
	service, status, err := currentManagedService()
	if err != nil {
		return err
	}
	switch args[0] {
	case "list", "show":
		fs := newCLIFlagSet("connections show")
		jsonOutput := fs.Bool("json", false, "print JSON")
		sourceFilter := fs.String("source", tuiTrafficSourceMixed, "filter by mixed, proxy, or ssh")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if len(fs.Args()) != 0 {
			return errors.New("usage: flclash connections [show [--json] [--source mixed|proxy|ssh]] | close ID|all")
		}
		if err := validateTrafficSourceFlag(*sourceFilter); err != nil {
			return err
		}
		connectionStatus, err := service.connections()
		if err != nil {
			return err
		}
		connections := filterConnectionsBySource(connectionStatus.Connections, *sourceFilter)
		if *jsonOutput {
			if connections == nil {
				connections = []tuiConnection{}
			}
			return writeCLIJSON(os.Stdout, connections)
		}
		return printCLIManagedConnections(connections)
	case "close":
		fs := newCLIFlagSet("connections close")
		sourceFilter := fs.String("source", tuiTrafficSourceMixed, "close mixed, proxy, or ssh flows")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		rest := fs.Args()
		if len(rest) != 1 {
			return errors.New("usage: flclash connections close ID|all [--source mixed|proxy|ssh]")
		}
		if err := validateTrafficSourceFlag(*sourceFilter); err != nil {
			return err
		}
		if rest[0] == "all" {
			_, err = service.closeAllConnectionsManagedForSource(*sourceFilter, status.Revision)
			return err
		}
		_, err = service.closeConnectionManagedForSource(rest[0], *sourceFilter, status.Revision)
		return err
	case "close-all":
		if len(args) != 1 {
			return errors.New("usage: flclash connections close all")
		}
		_, err = service.closeAllConnectionsManaged(status.Revision)
		return err
	default:
		return fmt.Errorf("unknown connections command %q", args[0])
	}
}

func printCLIManagedConnections(connections []tuiConnection) error {
	for _, connection := range connections {
		fmt.Printf("%s\t%s\t%s\t%s\tUID %d\t%s\n", tuiConnectionSource(connection), connection.ID, connection.Host, connection.Process, connection.UID, connection.Chain)
	}
	return nil
}
