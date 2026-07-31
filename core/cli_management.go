//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
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
	if err := ensureTUIConfig(paths, !configExplicit); err != nil {
		return err
	}
	if err := ensureTUIFlClashDefaults(paths.configPath); err != nil {
		return fmt.Errorf("apply FlClash defaults: %w", err)
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
		fmt.Println("Show backend, Core, active profile, Mixed Port, and frontend state.")
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
	mixedPort := 0
	mode := ""
	if config, err := managedConfig(status); err == nil {
		mixedPort = config.MixedPort
		mode = config.Mode
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
			"mixed_port":       mixedPort,
			"mode":             mode,
			"system_proxy":     status.SystemProxy,
			"frontends":        status.FrontendCount,
		})
	}
	fmt.Printf("Backend:      running (PID %d, revision %d)\n", status.PID, status.Revision)
	fmt.Printf("Version:      %s (protocol %d)\n", status.Version, status.ProtocolVersion)
	fmt.Printf("Core:         %s\n", cliOnOff(status.Running))
	fmt.Printf("Profile:      %s\n", status.ConfigPath)
	if mixedPort > 0 {
		fmt.Printf("Mixed Port:   %d\n", mixedPort)
	}
	if mode != "" {
		fmt.Printf("Mode:         %s\n", mode)
	}
	fmt.Printf("System Proxy: %s\n", cliOnOff(status.SystemProxy))
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
	if len(args) == 0 || cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash service start|stop|restart|status|logs|clients")
		fmt.Println("service stop terminates the backend and disconnects all frontends.")
		return nil
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
		if err := ensureTUIConfig(paths, !configExplicit); err != nil {
			return err
		}
		if err := ensureTUIFlClashDefaults(paths.configPath); err != nil {
			return fmt.Errorf("apply FlClash defaults: %w", err)
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
		if err := ensureTUIConfig(paths, true); err != nil {
			return err
		}
		if err := ensureTUIFlClashDefaults(paths.configPath); err != nil {
			return fmt.Errorf("apply FlClash defaults: %w", err)
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
		return fmt.Errorf("unknown service command %q; use `flclash service -help`", args[0])
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
		backupPath, err := backupTUIConfig(paths.configPath)
		if err != nil {
			return err
		}
		fmt.Println(backupPath)
	case "restore":
		backupPath, backup, err := restoreLatestTUIConfigLocked(
			paths.homeDir,
			paths.configPath,
		)
		if err != nil {
			return err
		}
		defer backup.release()
		if client, status, statusErr := currentManagedService(); statusErr == nil {
			backup.release()
			if _, err := client.reloadAtRevisionWithDigest(
				paths.configPath,
				status.Revision,
				backup.updatedSHA256,
			); err != nil {
				rollbackErr := rollbackManagedProfile(
					client,
					status,
					paths.homeDir,
					paths.configPath,
					backup,
				)
				if rollbackErr != nil {
					return fmt.Errorf(
						"restored %s but reload failed: %v; rollback skipped: %w",
						backupPath,
						err,
						rollbackErr,
					)
				}
				return fmt.Errorf(
					"restored %s but reload failed: %w; original restored",
					backupPath,
					err,
				)
			}
		}
		fmt.Printf("Restored %s\n", backupPath)
	case "edit":
		return editManagedConfig(paths.configPath)
	default:
		return fmt.Errorf("unknown config command %q", args[0])
	}
	return nil
}

func systemProxyCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash system-proxy on|off|status")
		return nil
	}
	client, status, err := currentManagedService()
	if err != nil {
		return err
	}
	switch args[0] {
	case "status":
		fmt.Println(cliOnOff(status.SystemProxy))
		return nil
	case "on", "off":
		enabled := args[0] == "on"
		status, err = client.setSystemProxy(enabled, status.Revision)
		if err != nil {
			return err
		}
		fmt.Printf("System proxy %s (revision %d)\n", cliOnOff(enabled), status.Revision)
		return nil
	default:
		return errors.New("system-proxy requires explicit on, off, or status")
	}
}

func tunCommand(args []string) error {
	return changeManagedSetting("tun", args, func(settings *tuiSettings, value string) error {
		switch value {
		case "on":
			settings.TunEnabled = true
		case "off":
			settings.TunEnabled = false
		default:
			return errors.New("tun requires explicit on, off, or status")
		}
		return nil
	})
}

func modeCommand(args []string) error {
	if len(args) > 0 && args[0] == "get" {
		args[0] = "status"
	} else if len(args) > 1 && args[0] == "set" {
		args = args[1:]
	}
	return changeManagedSetting("mode", args, func(settings *tuiSettings, value string) error {
		switch strings.ToLower(value) {
		case "rule", "global", "direct":
			settings.Mode = strings.ToLower(value)
		default:
			return errors.New("mode must be rule, global, or direct")
		}
		return nil
	})
}

func changeManagedSetting(
	name string,
	args []string,
	change func(*tuiSettings, string) error,
) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		fmt.Printf("Usage: flclash %s status|VALUE\n", name)
		return nil
	}
	client, status, err := currentManagedService()
	if err != nil {
		return err
	}
	settings := loadTUIConfiguredSettings(status.ConfigPath, true)
	if settings == nil {
		return errors.New("could not load active settings")
	}
	if args[0] == "status" {
		if name == "tun" {
			fmt.Println(cliOnOff(settings.TunEnabled))
		} else {
			fmt.Println(settings.Mode)
		}
		return nil
	}
	if err := change(settings, args[0]); err != nil {
		return err
	}
	status, err = client.applySettings(*settings, status.Revision)
	if err != nil {
		return err
	}
	fmt.Printf("%s updated (revision %d)\n", name, status.Revision)
	return nil
}

func connectionsCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash connections list|close ID|close-all [--json]")
		return nil
	}
	_, status, err := currentManagedService()
	if err != nil {
		return err
	}
	client := managedController(status)
	switch args[0] {
	case "list":
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
			return errors.New("usage: flclash connections close ID")
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
		checks["mixed_port"] = proxyErr == nil
		if proxyErr != nil {
			checks["mixed_port_error"] = proxyErr.Error()
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
	commands := "tui run start stop restart reload status logs service profile proxy config system-proxy tun mode connections geo env doctor completion update exec version help"
	switch args[0] {
	case "bash":
		fmt.Printf("_flclash(){ COMPREPLY=( $(compgen -W '%s' -- \"${COMP_WORDS[1]}\") ); }; complete -F _flclash flclash\n", commands)
	case "zsh":
		fmt.Printf("#compdef flclash\n_arguments '1:command:(%s)'\n", commands)
	case "fish":
		for _, command := range strings.Fields(commands) {
			fmt.Printf("complete -c flclash -f -n '__fish_use_subcommand' -a %s\n", command)
		}
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
		"backend %s uses protocol %d; run `flclash service restart` to upgrade to %s protocol %d",
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
	homeDir := filepath.Dir(path)
	lease, err := acquireTUIProfileLocks(homeDir, path)
	if err != nil {
		return err
	}
	defer lease.release()
	before, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		return errors.New("set $VISUAL or $EDITOR before using config edit")
	}
	command := exec.Command("sh", "-c", editor+" -- \"$1\"", "flclash-editor", path)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		_ = writeTUIProfileAtomically(path, before, info.Mode())
		return err
	}
	if message := handleValidateConfig(path); message != "" {
		_ = writeTUIProfileAtomically(path, before, info.Mode())
		return errors.New("edited configuration is invalid; original restored: " + message)
	}
	editedSHA256, err := tuiFileSHA256(path)
	if err != nil {
		return err
	}
	if client, status, statusErr := currentManagedService(); statusErr == nil &&
		filepath.Clean(status.ConfigPath) == filepath.Clean(path) {
		lease.release()
		backup := tuiProfileBackup{
			data:          before,
			mode:          info.Mode(),
			updatedSHA256: editedSHA256,
		}
		if _, err := client.reloadAtRevisionWithDigest(
			path,
			status.Revision,
			editedSHA256,
		); err != nil {
			rollbackErr := rollbackManagedProfile(
				client,
				status,
				homeDir,
				path,
				backup,
			)
			if rollbackErr != nil {
				return fmt.Errorf(
					"reload edited configuration: %v; rollback failed: %w",
					err,
					rollbackErr,
				)
			}
			return fmt.Errorf("reload edited configuration: %w; original restored", err)
		}
	}
	return nil
}

func rollbackManagedProfile(
	client *tuiServiceClient,
	status tuiServiceStatus,
	homeDir,
	path string,
	backup tuiProfileBackup,
) error {
	if err := restoreTUIProfileIfUnchanged(homeDir, path, backup); err != nil {
		return err
	}
	_, err := client.reloadAtRevisionWithDigest(
		path,
		status.Revision,
		tuiBytesSHA256(backup.data),
	)
	return err
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
