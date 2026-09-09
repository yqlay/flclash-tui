//go:build linux && !cgo && cli

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	if status.Mode == tuiSilentMode && strings.TrimSpace(status.FLCOutbound) != "" {
		fmt.Printf(
			"Core started in silent mode · flc follows %s (PID %d, revision %d)\n",
			formatCLIFLCOutbound(status),
			status.PID,
			status.Revision,
		)
		return nil
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
			"protocol_version":      status.ProtocolVersion,
			"backend_version":       status.Version,
			"revision":              status.Revision,
			"backend":               "running",
			"backend_pid":           status.PID,
			"core":                  cliOnOff(status.Running),
			"profile":               status.ConfigPath,
			"proxy_port":            proxyPort,
			"configured_proxy_port": status.ConfiguredProxyPort,
			"active_proxy_port":     status.ActiveProxyPort,
			"mixed_port":            status.ConfiguredProxyPort,
			"mode":                  mode,
			"flc_enabled":           status.FLCEnabled,
			"flc_outbound":          status.FLCOutbound,
			"system_proxy":          status.SystemProxy,
			"tun_scope":             status.TunScope,
			"tun_state":             status.TunState,
			"tun_owner_uid":         status.TunOwnerUID,
			"tun_owner_pid":         status.TunOwnerPID,
			"frontends":             status.FrontendCount,
		})
	}
	fmt.Printf("Backend:      running (PID %d, revision %d)\n", status.PID, status.Revision)
	fmt.Printf("Version:      %s (protocol %d)\n", status.Version, status.ProtocolVersion)
	fmt.Printf("Core:         %s\n", cliOnOff(status.Running))
	fmt.Printf("Profile:      %s\n", status.ConfigPath)
	if proxyPort > 0 {
		fmt.Printf("Proxy port:   %d\n", proxyPort)
	}
	if mode != "" {
		fmt.Printf("Mode:         %s\n", mode)
	}
	if status.FLCOutbound != "" {
		fmt.Printf("flc:          %s\n", formatCLIFLCOutbound(status))
	} else if strings.EqualFold(status.Mode, tuiSilentMode) {
		fmt.Println("flc:          pick a node in Proxies")
	}
	fmt.Printf("System proxy: %s\n", cliOnOff(status.SystemProxy))
	fmt.Printf("TUN:          %s · %s\n", strings.ToUpper(status.TunState), status.TunScope)
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
		fmt.Println("SSH tunnels stay up; use `flclash ssh disconnect` or `flclash exit`.")
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
		if err := client.shutdownAndWait(tuiServiceShutdownTimeout); err != nil {
			return err
		}
		fmt.Println("Backend stopped")
		return nil
	case "restart":
		client, status, err := currentManagedServiceRaw()
		wasRunning := false
		if err == nil {
			wasRunning = status.Running
			if err := client.shutdownPIDAndWait(
				status.PID,
				tuiServiceShutdownTimeout,
			); err != nil {
				return err
			}
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
