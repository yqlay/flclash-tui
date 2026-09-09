//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
)

const cliVersion = "0.5.28"

type cliPaths struct {
	homeDir    string
	configPath string
}

type controllerOptions struct {
	address    string
	unixSocket string
	secret     string
}

func main() {
	applyCLIGoMemoryPolicy()
	program := filepath.Base(os.Args[0])
	err := dispatchCLI(program, os.Args[1:])

	if err != nil {
		if program != "flc" {
			program = "flclash"
		}
		if err.Error() != "" {
			fmt.Fprintf(os.Stderr, "%s: %v\n", program, err)
		}
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		var exitCoder interface{ ExitCode() int }
		if errors.As(err, &exitCoder) {
			os.Exit(exitCoder.ExitCode())
		}
		os.Exit(1)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "FlClash TUI - one backend, multiple terminal clients")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  flclash                         Open a TUI frontend")
	fmt.Fprintln(w, "  flclash COMMAND [OPTIONS]       Manage the shared backend")
	fmt.Fprintln(w, "  flc COMMAND [ARG...]            Run a command through FlClash")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Dashboard commands:")
	fmt.Fprintln(w, "  core              Start, stop, restart, reload, or inspect Core")
	fmt.Fprintln(w, "  sys               Turn System proxy on/off or inspect it")
	fmt.Fprintln(w, "  tun               Turn TUN on/off or inspect it")
	fmt.Fprintln(w, "  mode              Select rule/global/direct/silent mode")
	fmt.Fprintln(w, "  port              Get, set, or disable the normal Proxy port")
	fmt.Fprintln(w, "  flc               Inspect silent flc; Proxies selection sets its group")
	fmt.Fprintln(w, "  ssh               Manage independent SSH SOCKS5 profiles and tunnels")
	fmt.Fprintln(w, "  net               Show or test Dashboard network detection")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Lifecycle shortcuts:")
	fmt.Fprintln(w, "  start             Alias for core start")
	fmt.Fprintln(w, "  stop              Alias for core stop; keep the backend available")
	fmt.Fprintln(w, "  restart           Alias for core restart")
	fmt.Fprintln(w, "  reload            Alias for core reload")
	fmt.Fprintln(w, "  status            Show backend, Core, profile, port, and frontend state")
	fmt.Fprintln(w, "  logs              Read or follow the managed backend log")
	fmt.Fprintln(w, "  backend           Manage the per-user Backend process")
	fmt.Fprintln(w, "  shutdown          Stop Backend and Core; disconnect all frontends")
	fmt.Fprintln(w, "  exit              Fully exit frontends, Backend, Core, and SSH tunnels")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Configuration and runtime:")
	fmt.Fprintln(w, "  profile           Import, select, update, rename, edit, or list profiles")
	fmt.Fprintln(w, "  proxy             List groups/nodes, select nodes, or run tests")
	fmt.Fprintln(w, "  config            Inspect, validate, edit, back up, or restore config")
	fmt.Fprintln(w, "  history           Show, follow, or clear recent connection history")
	fmt.Fprintln(w, "  connections       List or close active connections")
	fmt.Fprintln(w, "  geo               Inspect or update Mihomo Geo resources")
	fmt.Fprintln(w, "  env               Print the active proxy environment")
	fmt.Fprintln(w, "  doctor            Diagnose backend, Core, port, and configuration")
	fmt.Fprintln(w, "  completion        Generate shell completion scripts")
	fmt.Fprintln(w, "  check, validate   Validate a Clash/Mihomo YAML configuration")
	fmt.Fprintln(w, "  update, upgrade   Check and securely install a GitHub Release")
	fmt.Fprintln(w, "  exec              Compatibility alias for flc")
	fmt.Fprintln(w, "  run               Run the Core in the foreground (advanced)")
	fmt.Fprintln(w, "  version           Print the version")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Help:")
	fmt.Fprintln(w, "  flclash -help | --help | help")
	fmt.Fprintln(w, "  flclash COMMAND -help")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Unknown flclash commands are rejected. Use flc for external commands.")
}

func runCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash run [--config PATH] [--directory PATH]")
		fmt.Println("Run Core in the foreground. This advanced mode does not use the shared backend.")
		return nil
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configArg := fs.String("config", "", "path to config.yaml")
	directoryArg := fs.String("directory", "", "FlClash data directory")
	testURL := fs.String("test-url", "https://www.gstatic.com/generate_204", "URL used by proxy-group delay tests")
	if err := fs.Parse(args); err != nil {
		return err
	}

	paths, err := resolvePaths(*configArg, *directoryArg)
	if err != nil {
		return err
	}
	backendLock, err := acquireCLIBackendLock(cliProcessOwner{
		Kind:       "foreground",
		HomeDir:    paths.homeDir,
		ConfigPath: paths.configPath,
	})
	if err != nil {
		return err
	}
	defer backendLock.release()
	if _, err := startCore(paths, *testURL, "", ""); err != nil {
		return err
	}
	setupParams, err := json.Marshal(SetupParams{TestURL: *testURL, SelectedMap: map[string]string{}})
	if err != nil {
		return err
	}

	fmt.Printf("FlClash TUI is running\n")
	fmt.Printf("  config: %s\n", paths.configPath)
	fmt.Printf("  data:   %s\n", paths.homeDir)
	fmt.Println("Press Ctrl-C to stop.")

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)
	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)
	defer signal.Stop(interrupt)
	defer signal.Stop(reload)

	for {
		select {
		case <-interrupt:
			handleShutdown()
			return nil
		case <-reload:
			if message := handleSetupConfig(setupParams); message != "" {
				fmt.Fprintf(os.Stderr, "flclash: reload failed: %s\n", message)
			} else {
				fmt.Println("configuration reloaded")
			}
		}
	}
}

func checkCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash check --config PATH")
		fmt.Println("Validate a Clash/Mihomo YAML configuration without starting Core.")
		return nil
	}
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configArg := fs.String("config", "", "path to config.yaml")
	directoryArg := fs.String("directory", "", "directory used to resolve config.yaml")
	if err := fs.Parse(args); err != nil {
		return err
	}
	paths, err := resolvePaths(*configArg, *directoryArg)
	if err != nil {
		return err
	}
	if _, err := os.Stat(paths.configPath); err != nil {
		return fmt.Errorf("config file %q: %w", paths.configPath, err)
	}
	if message := handleValidateConfig(paths.configPath); message != "" {
		return errors.New(message)
	}
	fmt.Printf("configuration is valid: %s\n", paths.configPath)
	return nil
}

func resolvePaths(configArg, directoryArg string) (cliPaths, error) {
	var homeDir string
	var configPath string

	if directoryArg != "" {
		homeDir = directoryArg
		if configArg == "" {
			configArg = "config.yaml"
		}
		if !filepath.IsAbs(configArg) {
			configArg = filepath.Join(homeDir, configArg)
		}
	} else if configArg != "" {
		configPath = configArg
		homeDir = filepath.Dir(configArg)
	} else {
		configRoot, err := os.UserConfigDir()
		if err != nil {
			return cliPaths{}, fmt.Errorf("resolve user config directory: %w", err)
		}
		homeDir = filepath.Join(configRoot, "flclash")
		configPath = filepath.Join(homeDir, "config.yaml")
	}

	absoluteHome, err := filepath.Abs(homeDir)
	if err != nil {
		return cliPaths{}, err
	}
	if configPath == "" {
		configPath = configArg
	}
	absoluteConfig, err := filepath.Abs(configPath)
	if err != nil {
		return cliPaths{}, err
	}
	return cliPaths{homeDir: absoluteHome, configPath: absoluteConfig}, nil
}
