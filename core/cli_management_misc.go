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
	commands := "tui core sys tun mode port flc ssh net start stop restart reload status backend shutdown exit profile proxy history connections logs config geo env doctor completion check update run version help"
	groups := []struct {
		command string
		values  string
	}{
		{"tui", "--config --directory --controller --secret --test-url --no-start"},
		{"core", "start stop restart reload status"},
		{"sys", "on off status"},
		{"tun", "user system on off status"},
		{"mode", "rule global direct silent"},
		{"port", "off"},
		{"flc", "status select test env ssh"},
		{"ssh", "add edit delete list show connect disconnect status test default import probe --user --port --local-port --jump --identity --passphrase --clear-passphrase --password --clear-password --clear-jump --option --file"},
		{"net", "show refresh delay speed"},
		{"status", "--json --watch"},
		{"backend", "start stop restart status logs clients"},
		{"profile", "list import import-file current use update rename edit delete link"},
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
		fmt.Println("  elif (( COMP_CWORD >= 3 )) && [[ ${COMP_WORDS[1]} == flc && ${COMP_WORDS[2]} == ssh ]]; then")
		fmt.Println("    words='-d --direct -u --use'")
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
		fmt.Println("_flc() {")
		fmt.Println("  local current words")
		fmt.Println("  current=\"${COMP_WORDS[COMP_CWORD]}\"")
		fmt.Println("  if (( COMP_CWORD == 1 )); then words='ssh'; else words='-d --direct -u --use'; fi")
		fmt.Println("  COMPREPLY=( $(compgen -W \"$words\" -- \"$current\") )")
		fmt.Println("}")
		fmt.Println("complete -F _flc flc")
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
		fmt.Println("_flc() {")
		fmt.Println("  _arguments '1:command:(ssh)' '*:ssh argument:(-d --direct -u --use)'")
		fmt.Println("}")
		fmt.Println("compdef _flc flc")
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
		fmt.Println("complete -c flc -f -n '__fish_use_subcommand' -a ssh")
		fmt.Println("complete -c flc -f -n '__fish_seen_subcommand_from ssh' -a '-d --direct -u --use'")
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
	status, err := client.compatibleStatus()
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

func readManagedLog(path string, lineCount int, follow bool) error {
	var interrupt chan os.Signal
	if follow {
		interrupt = make(chan os.Signal, 1)
		signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(interrupt)
	}
	return readManagedLogTo(path, lineCount, follow, os.Stdout, interrupt)
}

func readManagedLogTo(
	path string,
	lineCount int,
	follow bool,
	output io.Writer,
	interrupt <-chan os.Signal,
) error {
	if lineCount < 0 {
		return errors.New("lines must not be negative")
	}
	var file *os.File
	var reader *bufio.Reader
	open := func() error {
		if file != nil {
			_ = file.Close()
			file = nil
			reader = nil
		}
		opened, err := os.Open(path)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		file = opened
		reader = bufio.NewReader(file)
		return nil
	}
	if err := open(); err != nil {
		return err
	}
	defer func() {
		if file != nil {
			_ = file.Close()
		}
	}()
	var data []byte
	if file != nil {
		var err error
		data, err = io.ReadAll(file)
		if err != nil {
			return err
		}
		reader = bufio.NewReader(file)
	}
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	if lineCount > 0 && len(lines) > lineCount {
		lines = lines[len(lines)-lineCount:]
	}
	for _, line := range lines {
		fmt.Fprintln(output, line)
	}
	if !follow {
		return nil
	}
	for {
		if reader != nil {
			line, readErr := reader.ReadString('\n')
			if line != "" {
				if _, err := io.WriteString(output, line); err != nil {
					return err
				}
			}
			if readErr == nil {
				continue
			}
			if !errors.Is(readErr, io.EOF) {
				return readErr
			}
		}
		select {
		case <-interrupt:
			return nil
		case <-time.After(200 * time.Millisecond):
		}
		pathInfo, pathErr := os.Stat(path)
		if os.IsNotExist(pathErr) {
			if file != nil {
				_ = file.Close()
				file = nil
				reader = nil
			}
			continue
		}
		if pathErr != nil {
			return pathErr
		}
		if file == nil {
			if err := open(); err != nil {
				return err
			}
			continue
		}
		fileInfo, fileErr := file.Stat()
		position, seekErr := file.Seek(0, io.SeekCurrent)
		if fileErr != nil || seekErr != nil ||
			!os.SameFile(pathInfo, fileInfo) || pathInfo.Size() < position {
			if err := open(); err != nil {
				return err
			}
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
