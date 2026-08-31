//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

const (
	cliSSHConfigFilename       = ".flclash-ssh.json"
	cliSSHConfigLockFilename   = ".flclash-ssh.lock"
	cliSSHRuntimeDirectoryName = "ssh"
	cliSSHPersistentStateFile  = "persistent.json"
	cliSSHConfigVersion        = 1
	cliSSHAskpassFileEnv       = "FLCLASH_SSH_ASKPASS_FILE"
)

type cliExitCodeError struct{ code int }

func (e *cliExitCodeError) Error() string { return "" }
func (e *cliExitCodeError) ExitCode() int { return e.code }

type cliSSHProfile struct {
	Name               string   `json:"name"`
	Destination        string   `json:"destination"`
	Port               int      `json:"port"`
	LocalPort          int      `json:"local_port,omitempty"`
	Identity           string   `json:"identity,omitempty"`
	IdentityPassphrase string   `json:"identity_passphrase,omitempty"`
	Password           string   `json:"password,omitempty"`
	Options            []string `json:"options,omitempty"`
}

type cliSSHConfig struct {
	Version  int             `json:"version"`
	Profiles []cliSSHProfile `json:"profiles"`
}

type cliSSHTunnelState struct {
	Name        string    `json:"name"`
	Destination string    `json:"destination"`
	Port        int       `json:"port"`
	ControlPath string    `json:"control_path"`
	Kind        string    `json:"kind"`
	StartedAt   time.Time `json:"started_at"`
	StatePath   string    `json:"-"`
}

type cliSSHProfileView struct {
	Name          string   `json:"name"`
	Destination   string   `json:"destination"`
	Port          int      `json:"port"`
	LocalPort     int      `json:"local_port,omitempty"`
	Identity      string   `json:"identity,omitempty"`
	Options       []string `json:"options,omitempty"`
	PassphraseSet bool     `json:"identity_passphrase_set"`
	PasswordSet   bool     `json:"password_set"`
	Connected     bool     `json:"connected"`
	Ready         bool     `json:"ready"`
	SocksPort     int      `json:"socks_port,omitempty"`
}

type cliSSHProfileEdit struct {
	Name, Destination, Identity, IdentityPassphrase, Password string
	Port, LocalPort                                           int
	Options                                                   []string
	PortSet, LocalPortSet, IdentitySet                        bool
	PassphraseSet, ClearPassphrase                            bool
	PasswordSet, ClearPassword, OptionsSet                    bool
}

type cliSSHAskpassSecrets struct {
	IdentityPassphrase string `json:"identity_passphrase,omitempty"`
	Password           string `json:"password,omitempty"`
}

func sshManagementCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		printSSHManagementUsage(os.Stdout)
		return nil
	}
	switch args[0] {
	case "add":
		return cliSSHAddCommand(args[1:])
	case "edit":
		return cliSSHEditCommand(args[1:])
	case "delete", "remove", "rm":
		return cliSSHDeleteCommand(args[1:])
	case "list", "ls":
		return cliSSHListCommand(args[1:])
	case "show":
		return cliSSHShowCommand(args[1:])
	case "connect", "open":
		return cliSSHConnectCommand(args[1:])
	case "disconnect", "close":
		return cliSSHDisconnectCommand(args[1:])
	case "status":
		return cliSSHStatusCommand(args[1:])
	case "test":
		return cliSSHTestCommand(args[1:])
	default:
		return fmt.Errorf("unknown ssh command %q; use `flclash ssh -help`", args[0])
	}
}

func printSSHManagementUsage(w io.Writer) {
	fmt.Fprintln(w, "Proxy local traffic through an SSH host's network exit.")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  flclash ssh add NAME user@host [--port PORT] [--local-port PORT|auto] [--identity PATH] [--passphrase] [--password] [--option KEY=VALUE]")
	fmt.Fprintln(w, "  flclash ssh edit NAME [user@host] [OPTIONS]")
	fmt.Fprintln(w, "  flclash ssh delete|show NAME")
	fmt.Fprintln(w, "  flclash ssh list|status [--json]")
	fmt.Fprintln(w, "  flclash ssh connect|disconnect [NAME|all]")
	fmt.Fprintln(w, "  flclash ssh test [NAME]")
	fmt.Fprintln(w, "`ssh add` with no arguments and `ssh edit NAME` open an interactive prompt.")
}

var (
	activeCLIPersistentSSHTunnelForCommand = activeCLIPersistentSSHTunnel
	startCLITransientSSHTunnelForCommand   = startCLITransientSSHTunnel
	stopCLITransientSSHTunnelForCommand    = stopCLITransientSSHTunnel
	runCLICommandWithSSHProxyForCommand    = runCLICommandWithSSHProxy
)

func flcSSHCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		printFLCSSHUsage(os.Stdout)
		return nil
	}
	profileName := ""
	if args[0] == "-u" || args[0] == "--use" {
		if len(args) < 3 {
			return errors.New("usage: flc ssh -u NAME COMMAND [ARG...]")
		}
		profileName, args = args[1], args[2:]
	} else if strings.HasPrefix(args[0], "--use=") {
		profileName, args = strings.TrimPrefix(args[0], "--use="), args[1:]
	}
	if len(args) == 0 {
		return errors.New("flc ssh requires a local command")
	}
	if profileName != "" {
		profile, err := loadCLISSHProfile(profileName)
		if err != nil {
			return err
		}
		state, err := startCLITransientSSHTunnelForCommand(profile)
		if err != nil {
			return err
		}
		commandErr := runCLICommandWithSSHProxyForCommand(args, state.Port)
		stopErr := stopCLITransientSSHTunnelForCommand(state)
		return errors.Join(commandErr, stopErr)
	}
	state, active, err := activeCLIPersistentSSHTunnelForCommand()
	if err != nil {
		return err
	}
	if !active {
		return errors.New("no SSH tunnel is open; run `flclash ssh connect NAME` or use `flc ssh -u NAME COMMAND`")
	}
	if !cliSSHSOCKSReady(state.Port) {
		return fmt.Errorf(
			"SSH tunnel %q is connected but its SOCKS5 listener is unavailable; reconnect it before running a command",
			state.Name,
		)
	}
	return runCLICommandWithSSHProxyForCommand(args, state.Port)
}

func printFLCSSHUsage(w io.Writer) {
	fmt.Fprintln(w, "Run one local command through an SSH host's network exit.")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  flc ssh COMMAND [ARG...]")
	fmt.Fprintln(w, "  flc ssh -u NAME COMMAND [ARG...]")
	fmt.Fprintln(w, "-u creates a temporary tunnel which is always closed after the command.")
}

func cliSSHAddCommand(args []string) error {
	edit, interactive, err := parseCLISSHProfileEdit(args, false)
	if err != nil {
		return err
	}
	if interactive {
		edit, err = promptCLISSHProfile(cliSSHProfile{}, false)
		if err != nil {
			return err
		}
	}
	profile, err := cliSSHProfileFromEdit(cliSSHProfile{}, edit, false)
	if err != nil {
		return err
	}
	err = addCLISSHProfile(profile)
	if err == nil {
		fmt.Printf("SSH profile %s added (%s)\n", profile.Name, profile.Destination)
	}
	return err
}

func cliSSHEditCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		return errors.New("usage: flclash ssh edit NAME [user@host] [OPTIONS]")
	}
	existing, err := loadCLISSHProfile(args[0])
	if err != nil {
		return err
	}
	if connected, err := cliSSHProfileConnected(existing.Name); err != nil {
		return err
	} else if connected {
		return fmt.Errorf(
			"SSH profile %q is connected; disconnect it before editing",
			existing.Name,
		)
	}
	originalFingerprint, err := cliSSHProfileFingerprint(existing)
	if err != nil {
		return err
	}
	edit, interactive, err := parseCLISSHProfileEdit(args, true)
	if err != nil {
		return err
	}
	if interactive {
		edit, err = promptCLISSHProfile(existing, true)
		if err != nil {
			return err
		}
	}
	updated, err := cliSSHProfileFromEdit(existing, edit, true)
	if err != nil {
		return err
	}
	err = replaceCLISSHProfile(args[0], originalFingerprint, updated)
	if err == nil {
		fmt.Printf("SSH profile %s updated\n", updated.Name)
	}
	return err
}

func cliSSHDeleteCommand(args []string) error {
	if len(args) != 1 || cliSubcommandHelp(args) {
		return errors.New("usage: flclash ssh delete NAME")
	}
	err := deleteCLISSHProfile(args[0])
	if err == nil {
		fmt.Printf("SSH profile %s deleted\n", args[0])
	}
	return err
}

func cliSSHListCommand(args []string) error {
	jsonOutput := len(args) == 1 && args[0] == "--json"
	if len(args) > 0 && !jsonOutput {
		return errors.New("usage: flclash ssh list [--json]")
	}
	views, err := loadCLISSHProfileViews()
	if err != nil {
		return err
	}
	if jsonOutput {
		return writeCLIJSON(os.Stdout, views)
	}
	if len(views) == 0 {
		fmt.Println("No SSH profiles configured")
		return nil
	}
	for _, view := range views {
		status, endpoint := "DISCONNECTED", ""
		if view.Connected && view.Ready {
			configured := "auto"
			if view.LocalPort > 0 {
				configured = strconv.Itoa(view.LocalPort)
			}
			status, endpoint = "CONNECTED", fmt.Sprintf(
				" · SOCKS5 127.0.0.1:%d · configured %s",
				view.SocksPort,
				configured,
			)
		} else if view.Connected {
			status, endpoint = "BROKEN", fmt.Sprintf(
				" · SOCKS5 127.0.0.1:%d unavailable",
				view.SocksPort,
			)
		} else if view.LocalPort > 0 {
			endpoint = fmt.Sprintf(" · local 127.0.0.1:%d", view.LocalPort)
		} else {
			endpoint = " · local auto"
		}
		auth := cliSSHAuthenticationLabel(
			view.Identity,
			view.PassphraseSet,
			view.PasswordSet,
		)
		fmt.Printf("%-18s %-12s %-28s · %s%s\n", view.Name, status, view.Destination, auth, endpoint)
	}
	return nil
}

func cliSSHShowCommand(args []string) error {
	if len(args) < 1 || len(args) > 2 {
		return errors.New("usage: flclash ssh show NAME [--json]")
	}
	views, err := loadCLISSHProfileViews()
	if err != nil {
		return err
	}
	for _, view := range views {
		if !strings.EqualFold(view.Name, args[0]) {
			continue
		}
		if len(args) == 2 {
			if args[1] != "--json" {
				return fmt.Errorf("unknown option %q", args[1])
			}
			return writeCLIJSON(os.Stdout, view)
		}
		localPort := "auto"
		if view.LocalPort > 0 {
			localPort = strconv.Itoa(view.LocalPort)
		}
		fmt.Printf(
			"Name:                  %s\nSSH host:              %s\nSSH port:              %d\nLocal SOCKS:           %s\nIdentity(private key): %s\nKey passphrase:        %s\nSSH password:          %s\nConnected:             %s\nSOCKS5 ready:          %s\n",
			view.Name,
			view.Destination,
			view.Port,
			localPort,
			cliDisplayValue(view.Identity),
			cliSSHMaskedSecret(view.PassphraseSet),
			cliSSHMaskedSecret(view.PasswordSet),
			cliOnOff(view.Connected),
			cliOnOff(view.Ready),
		)
		if view.SocksPort > 0 {
			fmt.Printf("SOCKS5:      127.0.0.1:%d\n", view.SocksPort)
		}
		return nil
	}
	return fmt.Errorf("SSH profile %q does not exist", args[0])
}

func cliSSHConnectCommand(args []string) error {
	if len(args) != 1 || cliSubcommandHelp(args) {
		return errors.New("usage: flclash ssh connect NAME")
	}
	state, alreadyConnected, err := connectCLISSHProfile(args[0])
	if err != nil {
		return err
	}
	if alreadyConnected {
		fmt.Printf("SSH %s already connected · SOCKS5 127.0.0.1:%d\n", state.Name, state.Port)
		return nil
	}
	fmt.Printf("SSH %s connected · SOCKS5 127.0.0.1:%d\n", state.Name, state.Port)
	return nil
}

func cliSSHDisconnectCommand(args []string) error {
	if len(args) > 1 {
		return errors.New("usage: flclash ssh disconnect [NAME|all]")
	}
	if len(args) == 1 && args[0] == "all" {
		if err := stopAllCLISSHTunnels(); err != nil {
			return err
		}
		fmt.Println("All SSH tunnels disconnected")
		return nil
	}
	state, disconnected, err := disconnectCLISSHProfile(firstCLIArgument(args))
	if err != nil {
		return err
	}
	if !disconnected {
		fmt.Println("No persistent SSH tunnel is open")
		return nil
	}
	fmt.Printf("SSH %s disconnected\n", state.Name)
	return nil
}

func cliSSHStatusCommand(args []string) error {
	if len(args) == 0 {
		return cliSSHListCommand(nil)
	}
	if len(args) == 1 && args[0] == "--json" {
		return cliSSHListCommand(args)
	}
	return cliSSHShowCommand(args)
}

func cliSSHTestCommand(args []string) error {
	if len(args) > 1 {
		return errors.New("usage: flclash ssh test [NAME]")
	}
	name := ""
	if len(args) == 1 {
		name = args[0]
	}
	state, latency, err := testCLISSHProfile(name)
	if err != nil {
		return err
	}
	fmt.Printf("SSH %s ready · SOCKS5 127.0.0.1:%d · TCP %s\n", state.Name, state.Port, latency)
	return nil
}

func firstCLIArgument(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func cliSSHAuthenticationLabel(identity string, passphraseSet, passwordSet bool) string {
	if passwordSet && (identity != "" || passphraseSet) {
		key := "key"
		if identity == "" {
			key = "default key"
		}
		if passphraseSet {
			key += " + passphrase ****"
		}
		return key + " + password ****"
	}
	parts := make([]string, 0, 2)
	key := ""
	if identity != "" {
		key = "key " + identity
	} else if passphraseSet {
		key = "default key"
	}
	if key != "" {
		if passphraseSet {
			key += " (passphrase ********)"
		}
		parts = append(parts, key)
	}
	if passwordSet {
		parts = append(parts, "password ********")
	}
	if len(parts) == 0 {
		return "agent/default key"
	}
	return strings.Join(parts, " + ")
}

func parseCLISSHProfileEdit(args []string, editing bool) (cliSSHProfileEdit, bool, error) {
	edit := cliSSHProfileEdit{Port: 22}
	if len(args) == 0 {
		return edit, true, nil
	}
	arguments := make(map[string]bool, len(args))
	for _, argument := range args {
		arguments[argument] = true
	}
	if arguments["--passphrase"] && arguments["--clear-passphrase"] {
		return edit, false, errors.New("--passphrase and --clear-passphrase cannot be used together")
	}
	if arguments["--password"] && arguments["--clear-password"] {
		return edit, false, errors.New("--password and --clear-password cannot be used together")
	}
	positionals := []string{}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		next := func() (string, error) {
			if index+1 >= len(args) {
				return "", fmt.Errorf("%s requires a value", argument)
			}
			index++
			return args[index], nil
		}
		switch {
		case argument == "--port":
			value, err := next()
			if err != nil {
				return edit, false, err
			}
			edit.Port, err = strconv.Atoi(value)
			if err != nil || edit.Port < 1 || edit.Port > 65535 {
				return edit, false, fmt.Errorf("invalid SSH port %q", value)
			}
			edit.PortSet = true
		case strings.HasPrefix(argument, "--port="):
			value := strings.TrimPrefix(argument, "--port=")
			var err error
			edit.Port, err = strconv.Atoi(value)
			if err != nil || edit.Port < 1 || edit.Port > 65535 {
				return edit, false, fmt.Errorf("invalid SSH port %q", value)
			}
			edit.PortSet = true
		case argument == "--local-port":
			value, err := next()
			if err != nil {
				return edit, false, err
			}
			edit.LocalPort, err = parseCLISSHLocalPort(value)
			if err != nil {
				return edit, false, err
			}
			edit.LocalPortSet = true
		case strings.HasPrefix(argument, "--local-port="):
			value := strings.TrimPrefix(argument, "--local-port=")
			var err error
			edit.LocalPort, err = parseCLISSHLocalPort(value)
			if err != nil {
				return edit, false, err
			}
			edit.LocalPortSet = true
		case argument == "--identity":
			value, err := next()
			if err != nil {
				return edit, false, err
			}
			edit.Identity = value
			edit.IdentitySet = true
		case strings.HasPrefix(argument, "--identity="):
			edit.Identity = strings.TrimPrefix(argument, "--identity=")
			edit.IdentitySet = true
		case argument == "--passphrase":
			value, err := promptCLISSHSecret("Private key passphrase")
			if err != nil {
				return edit, false, err
			}
			edit.IdentityPassphrase, edit.PassphraseSet = value, true
		case argument == "--clear-passphrase":
			edit.ClearPassphrase = true
		case argument == "--option":
			value, err := next()
			if err != nil {
				return edit, false, err
			}
			if err := validateCLISSHOption(value); err != nil {
				return edit, false, err
			}
			edit.Options = append(edit.Options, value)
			edit.OptionsSet = true
		case strings.HasPrefix(argument, "--option="):
			value := strings.TrimPrefix(argument, "--option=")
			if err := validateCLISSHOption(value); err != nil {
				return edit, false, err
			}
			edit.Options = append(edit.Options, value)
			edit.OptionsSet = true
		case argument == "--password":
			value, err := promptCLISSHSecret("SSH password")
			if err != nil {
				return edit, false, err
			}
			edit.Password, edit.PasswordSet = value, true
		case argument == "--clear-password":
			edit.ClearPassword = true
		case strings.HasPrefix(argument, "-"):
			return edit, false, fmt.Errorf("unknown SSH profile option %q", argument)
		default:
			positionals = append(positionals, argument)
		}
	}
	if !editing && edit.ClearPassphrase {
		return edit, false, errors.New("--clear-passphrase is only valid with `ssh edit`")
	}
	if !editing && edit.ClearPassword {
		return edit, false, errors.New("--clear-password is only valid with `ssh edit`")
	}
	if editing {
		if len(positionals) < 1 || len(positionals) > 2 {
			return edit, false, errors.New("usage: flclash ssh edit NAME [user@host] [OPTIONS]")
		}
		edit.Name = positionals[0]
		if len(positionals) == 2 {
			edit.Destination = positionals[1]
		}
		return edit, len(args) == 1, nil
	}
	if len(positionals) != 2 {
		return edit, false, errors.New("usage: flclash ssh add NAME user@host [OPTIONS]")
	}
	edit.Name, edit.Destination = positionals[0], positionals[1]
	return edit, false, nil
}

func cliSSHProfileFromEdit(existing cliSSHProfile, edit cliSSHProfileEdit, editing bool) (cliSSHProfile, error) {
	profile := existing
	if edit.Name != "" {
		profile.Name = edit.Name
	}
	if edit.Destination != "" {
		profile.Destination = edit.Destination
	}
	if !editing || edit.PortSet || existing.Port == 0 {
		profile.Port = edit.Port
	}
	if !editing || edit.LocalPortSet {
		profile.LocalPort = edit.LocalPort
	}
	if edit.IdentitySet || !editing {
		profile.Identity = edit.Identity
	}
	if edit.PassphraseSet {
		profile.IdentityPassphrase = edit.IdentityPassphrase
	} else if edit.ClearPassphrase {
		profile.IdentityPassphrase = ""
	}
	if edit.OptionsSet || !editing {
		profile.Options = append([]string(nil), edit.Options...)
	}
	if edit.PasswordSet {
		profile.Password = edit.Password
	} else if edit.ClearPassword {
		profile.Password = ""
	}
	if profile.Port == 0 {
		profile.Port = 22
	}
	return profile, validateCLISSHProfile(profile)
}

func parseCLISSHLocalPort(value string) (int, error) {
	value = strings.TrimSpace(value)
	if strings.EqualFold(value, "auto") || value == "0" {
		return 0, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid local SOCKS5 port %q; use 1-65535 or auto", value)
	}
	return port, nil
}

func addCLISSHProfile(profile cliSSHProfile) error {
	if err := validateCLISSHProfile(profile); err != nil {
		return err
	}
	return updateCLISSHConfig(func(config *cliSSHConfig) error {
		if _, found := findCLISSHProfile(config.Profiles, profile.Name); found {
			return fmt.Errorf("SSH profile %q already exists", profile.Name)
		}
		profile.Options = append([]string(nil), profile.Options...)
		config.Profiles = append(config.Profiles, profile)
		sort.Slice(config.Profiles, func(i, j int) bool {
			return strings.ToLower(config.Profiles[i].Name) <
				strings.ToLower(config.Profiles[j].Name)
		})
		return nil
	})
}

func cliSSHProfileFingerprint(profile cliSSHProfile) (string, error) {
	data, err := json.Marshal(profile)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest[:]), nil
}

func replaceCLISSHProfile(
	originalName,
	expectedFingerprint string,
	profile cliSSHProfile,
) error {
	if err := validateCLISSHProfile(profile); err != nil {
		return err
	}
	lock, err := lockCLISSHTunnelOperation()
	if err != nil {
		return err
	}
	defer lock.release()
	if state, active, stateErr := activeCLIPersistentSSHTunnelForOperation(); stateErr != nil {
		return stateErr
	} else if active && strings.EqualFold(state.Name, originalName) {
		return fmt.Errorf(
			"SSH profile %q is connected; disconnect it before editing",
			originalName,
		)
	}
	return updateCLISSHConfigForOperation(func(config *cliSSHConfig) error {
		index, found := findCLISSHProfile(config.Profiles, originalName)
		if !found {
			return fmt.Errorf("SSH profile %q does not exist", originalName)
		}
		fingerprint, err := cliSSHProfileFingerprint(config.Profiles[index])
		if err != nil {
			return err
		}
		if expectedFingerprint != "" && fingerprint != expectedFingerprint {
			return fmt.Errorf(
				"SSH profile %q changed in another frontend; reopen it before saving",
				originalName,
			)
		}
		if other, duplicate := findCLISSHProfile(config.Profiles, profile.Name); duplicate && other != index {
			return fmt.Errorf("SSH profile %q already exists", profile.Name)
		}
		profile.Options = append([]string(nil), profile.Options...)
		config.Profiles[index] = profile
		sort.Slice(config.Profiles, func(i, j int) bool {
			return strings.ToLower(config.Profiles[i].Name) <
				strings.ToLower(config.Profiles[j].Name)
		})
		return nil
	})
}

func deleteCLISSHProfile(name string) error {
	lock, err := lockCLISSHTunnelOperation()
	if err != nil {
		return err
	}
	defer lock.release()
	profile, err := loadCLISSHProfile(name)
	if err != nil {
		return err
	}
	state, active, err := activeCLIPersistentSSHTunnelForOperation()
	if err != nil {
		return err
	}
	wasConnected := active && strings.EqualFold(state.Name, name)
	if wasConnected {
		if err := stopCLIStateTunnelForOperation(state); err != nil {
			return err
		}
	}
	err = updateCLISSHConfigForOperation(func(config *cliSSHConfig) error {
		index, found := findCLISSHProfile(config.Profiles, name)
		if !found {
			return fmt.Errorf("SSH profile %q does not exist", name)
		}
		config.Profiles = append(config.Profiles[:index], config.Profiles[index+1:]...)
		return nil
	})
	if err == nil || !wasConnected {
		return err
	}
	_, restoreErr := startCLIPersistentSSHTunnelForOperation(profile)
	if restoreErr != nil {
		return fmt.Errorf(
			"delete SSH profile %q: %v; restore previous tunnel: %w",
			name,
			err,
			restoreErr,
		)
	}
	return fmt.Errorf(
		"delete SSH profile %q: %w; previous tunnel restored",
		name,
		err,
	)
}

var (
	activeCLIPersistentSSHTunnelForOperation = activeCLIPersistentSSHTunnel
	startCLIPersistentSSHTunnelForOperation  = startCLIPersistentSSHTunnel
	stopCLIStateTunnelForOperation           = stopCLIStateTunnel
	updateCLISSHConfigForOperation           = updateCLISSHConfig
	addCLISSHDynamicForwardForOperation      = addCLISSHDynamicForward
)

func cliSSHProfileConnected(name string) (bool, error) {
	state, active, err := activeCLIPersistentSSHTunnelForOperation()
	if err != nil {
		return false, err
	}
	return active && strings.EqualFold(state.Name, name), nil
}

func connectCLISSHProfile(name string) (cliSSHTunnelState, bool, error) {
	lock, err := lockCLISSHTunnelOperation()
	if err != nil {
		return cliSSHTunnelState{}, false, err
	}
	defer lock.release()
	profile, err := loadCLISSHProfile(name)
	if err != nil {
		return cliSSHTunnelState{}, false, err
	}
	old, oldActive, err := activeCLIPersistentSSHTunnelForOperation()
	if err != nil {
		return cliSSHTunnelState{}, false, err
	}
	if oldActive && strings.EqualFold(old.Name, profile.Name) {
		if cliSSHSOCKSReady(old.Port) {
			return old, true, nil
		}
		if err := stopCLIStateTunnelForOperation(old); err != nil {
			return cliSSHTunnelState{}, false,
				fmt.Errorf("stop broken SSH tunnel %q: %w", old.Name, err)
		}
		state, err := startCLIPersistentSSHTunnelForOperation(profile)
		if err != nil {
			return cliSSHTunnelState{}, false,
				fmt.Errorf("restart broken SSH tunnel %q: %w", profile.Name, err)
		}
		return state, false, nil
	}
	var oldProfile cliSSHProfile
	if oldActive {
		oldProfile, err = loadCLISSHProfile(old.Name)
		if err != nil {
			return cliSSHTunnelState{}, false, fmt.Errorf(
				"active SSH profile %q cannot be restored; disconnect it before switching: %w",
				old.Name,
				err,
			)
		}
		if err := stopCLIStateTunnelForOperation(old); err != nil {
			return cliSSHTunnelState{}, false,
				fmt.Errorf("stop previous SSH tunnel %q: %w", old.Name, err)
		}
	}
	state, err := startCLIPersistentSSHTunnelForOperation(profile)
	if err == nil {
		return state, false, nil
	}
	if !oldActive {
		return cliSSHTunnelState{}, false, err
	}
	_, restoreErr := startCLIPersistentSSHTunnelForOperation(oldProfile)
	if restoreErr != nil {
		return cliSSHTunnelState{}, false, fmt.Errorf(
			"connect SSH profile %q: %v; restore previous tunnel %q: %w",
			profile.Name,
			err,
			old.Name,
			restoreErr,
		)
	}
	return cliSSHTunnelState{}, false, fmt.Errorf(
		"connect SSH profile %q: %w; previous tunnel %q restored",
		profile.Name,
		err,
		old.Name,
	)
}

func disconnectCLISSHProfile(name string) (cliSSHTunnelState, bool, error) {
	lock, err := lockCLISSHTunnelOperation()
	if err != nil {
		return cliSSHTunnelState{}, false, err
	}
	defer lock.release()
	state, active, err := activeCLIPersistentSSHTunnelForOperation()
	if err != nil {
		return cliSSHTunnelState{}, false, err
	}
	if !active {
		return cliSSHTunnelState{}, false, nil
	}
	if name != "" && !strings.EqualFold(name, state.Name) {
		return cliSSHTunnelState{}, false,
			fmt.Errorf("SSH profile %q is not connected", name)
	}
	if err := stopCLIStateTunnelForOperation(state); err != nil {
		return cliSSHTunnelState{}, false, err
	}
	return state, true, nil
}

func testCLISSHProfile(name string) (cliSSHTunnelState, time.Duration, error) {
	state, active, err := activeCLIPersistentSSHTunnel()
	if err != nil {
		return cliSSHTunnelState{}, 0, err
	}
	if !active {
		return cliSSHTunnelState{}, 0, errors.New("no persistent SSH tunnel is open")
	}
	if name != "" && !strings.EqualFold(name, state.Name) {
		return cliSSHTunnelState{}, 0,
			fmt.Errorf("SSH profile %q is not connected", name)
	}
	started := time.Now()
	connection, err := net.DialTimeout(
		"tcp",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(state.Port)),
		2*time.Second,
	)
	if err != nil {
		return cliSSHTunnelState{}, 0,
			fmt.Errorf("SSH SOCKS5 listener is unavailable: %w", err)
	}
	_ = connection.Close()
	return state, time.Since(started).Round(time.Millisecond), nil
}

func promptCLISSHProfile(existing cliSSHProfile, editing bool) (cliSSHProfileEdit, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return cliSSHProfileEdit{}, errors.New("interactive SSH profile input requires a terminal")
	}
	reader := bufio.NewReader(os.Stdin)
	edit := cliSSHProfileEdit{Port: 22}
	var err error
	edit.Name, err = promptCLISSHLine(reader, "Name", existing.Name, false)
	if err != nil {
		return edit, err
	}
	edit.Destination, err = promptCLISSHLine(reader, "SSH host (user@host or SSH alias)", existing.Destination, false)
	if err != nil {
		return edit, err
	}
	port := existing.Port
	if port == 0 {
		port = 22
	}
	portText, err := promptCLISSHLine(reader, "SSH port", strconv.Itoa(port), false)
	if err != nil {
		return edit, err
	}
	edit.Port, err = strconv.Atoi(portText)
	if err != nil || edit.Port < 1 || edit.Port > 65535 {
		return edit, fmt.Errorf("invalid SSH port %q", portText)
	}
	edit.PortSet = true
	localPortText := "auto"
	if existing.LocalPort > 0 {
		localPortText = strconv.Itoa(existing.LocalPort)
	}
	localPortText, err = promptCLISSHLine(
		reader,
		"Local SOCKS5 port (auto or 1-65535)",
		localPortText,
		false,
	)
	if err != nil {
		return edit, err
	}
	edit.LocalPort, err = parseCLISSHLocalPort(localPortText)
	if err != nil {
		return edit, err
	}
	edit.LocalPortSet = true
	edit.Identity, err = promptCLISSHLine(reader, "Identity (private key, optional)", existing.Identity, true)
	if err != nil {
		return edit, err
	}
	edit.IdentitySet = true
	answer, err := promptCLISSHLine(reader, "Save/replace private key passphrase? [y/N]", "n", true)
	if err != nil {
		return edit, err
	}
	if strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes") {
		edit.IdentityPassphrase, err = promptCLISSHSecret("Private key passphrase")
		edit.PassphraseSet = err == nil
		if err != nil {
			return edit, err
		}
	}
	if editing && existing.IdentityPassphrase != "" && !edit.PassphraseSet {
		answer, err = promptCLISSHLine(
			reader,
			"Clear saved private key passphrase? [y/N]",
			"n",
			true,
		)
		if err != nil {
			return edit, err
		}
		edit.ClearPassphrase = strings.EqualFold(answer, "y") ||
			strings.EqualFold(answer, "yes")
	}
	answer, err = promptCLISSHLine(reader, "Save/replace SSH password? [y/N]", "n", true)
	if err != nil {
		return edit, err
	}
	if strings.EqualFold(answer, "y") || strings.EqualFold(answer, "yes") {
		edit.Password, err = promptCLISSHSecret("SSH password")
		edit.PasswordSet = err == nil
		if err != nil {
			return edit, err
		}
	}
	if editing && existing.Password != "" && !edit.PasswordSet {
		answer, err = promptCLISSHLine(
			reader,
			"Clear saved password? [y/N]",
			"n",
			true,
		)
		if err != nil {
			return edit, err
		}
		edit.ClearPassword = strings.EqualFold(answer, "y") ||
			strings.EqualFold(answer, "yes")
	}
	if editing {
		edit.Options, edit.OptionsSet = append([]string(nil), existing.Options...), true
	}
	return edit, nil
}

func promptCLISSHLine(reader *bufio.Reader, label, defaultValue string, optional bool) (string, error) {
	if defaultValue == "" {
		fmt.Fprintf(os.Stderr, "%s: ", label)
	} else {
		fmt.Fprintf(os.Stderr, "%s [%s]: ", label, defaultValue)
	}
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = defaultValue
	}
	if value == "" && !optional {
		return "", fmt.Errorf("%s must not be empty", label)
	}
	return value, nil
}

func promptCLISSHSecret(label string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("%s requires an interactive terminal", label)
	}
	fmt.Fprintf(os.Stderr, "%s: ", label)
	first, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(os.Stderr, "Confirm %s: ", strings.ToLower(label))
	second, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if len(first) == 0 {
		return "", fmt.Errorf("%s must not be empty", strings.ToLower(label))
	}
	if string(first) != string(second) {
		return "", fmt.Errorf("%s values do not match", strings.ToLower(label))
	}
	return string(first), nil
}

func validateCLISSHProfile(profile cliSSHProfile) error {
	if profile.Name == "" || len(profile.Name) > 64 {
		return errors.New("SSH profile name must contain 1 to 64 characters")
	}
	for _, value := range profile.Name {
		if !((value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') || strings.ContainsRune("._-", value)) {
			return errors.New("SSH profile name may contain only letters, numbers, dot, underscore, and hyphen")
		}
	}
	if profile.Destination == "" || strings.HasPrefix(profile.Destination, "-") ||
		strings.ContainsAny(profile.Destination, " \t\r\n\x00") {
		return errors.New("SSH destination is invalid")
	}
	if profile.Port < 1 || profile.Port > 65535 {
		return errors.New("SSH port must be between 1 and 65535")
	}
	if profile.LocalPort < 0 || profile.LocalPort > 65535 {
		return errors.New("local SOCKS5 port must be auto or between 1 and 65535")
	}
	for _, option := range profile.Options {
		if err := validateCLISSHOption(option); err != nil {
			return err
		}
	}
	return nil
}

func validateCLISSHOption(option string) error {
	key, value, found := strings.Cut(option, "=")
	if !found || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" || strings.ContainsAny(option, "\r\n\x00") {
		return fmt.Errorf("SSH option %q must use KEY=VALUE", option)
	}
	disallowed := map[string]bool{"clearallforwardings": true, "controlmaster": true, "controlpath": true, "controlpersist": true, "dynamicforward": true, "exitonforwardfailure": true, "forkafterauthentication": true, "localcommand": true, "permitlocalcommand": true, "remotecommand": true, "sessiontype": true}
	if disallowed[strings.ToLower(strings.TrimSpace(key))] {
		return fmt.Errorf("SSH option %q conflicts with FlClash tunnel management", key)
	}
	return nil
}

func loadCLISSHConfig() (cliSSHConfig, error) {
	paths, err := resolvePaths("", "")
	if err != nil {
		return cliSSHConfig{}, err
	}
	path := filepath.Join(paths.homeDir, cliSSHConfigFilename)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return cliSSHConfig{Version: cliSSHConfigVersion}, nil
	}
	if err != nil {
		return cliSSHConfig{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !cliPathOwnedByCurrentUser(info) || info.Mode().Perm()&0o077 != 0 {
		return cliSSHConfig{}, fmt.Errorf("unsafe SSH configuration %q; it must be an owned regular file with mode 0600", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cliSSHConfig{}, err
	}
	var config cliSSHConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return config, fmt.Errorf("parse SSH configuration: %w", err)
	}
	if config.Version != cliSSHConfigVersion {
		return config, fmt.Errorf("unsupported SSH configuration version %d", config.Version)
	}
	for _, profile := range config.Profiles {
		if err := validateCLISSHProfile(profile); err != nil {
			return config, fmt.Errorf("invalid SSH profile %q: %w", profile.Name, err)
		}
	}
	return config, nil
}

func updateCLISSHConfig(update func(*cliSSHConfig) error) error {
	paths, err := resolvePaths("", "")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(paths.homeDir, 0o700); err != nil {
		return err
	}
	lock, err := acquireCLIFileLock(filepath.Join(paths.homeDir, cliSSHConfigLockFilename), cliProcessOwner{Kind: "ssh-config", PID: os.Getpid(), HomeDir: paths.homeDir, StartedAt: time.Now()})
	if err != nil {
		return fmt.Errorf("lock SSH configuration: %w", err)
	}
	defer lock.release()
	config, err := loadCLISSHConfig()
	if err != nil {
		return err
	}
	if err := update(&config); err != nil {
		return err
	}
	config.Version = cliSSHConfigVersion
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return writeCLISSHFileAtomically(filepath.Join(paths.homeDir, cliSSHConfigFilename), append(data, '\n'))
}

func writeCLISSHFileAtomically(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".flclash-ssh-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func loadCLISSHProfile(name string) (cliSSHProfile, error) {
	config, err := loadCLISSHConfig()
	if err != nil {
		return cliSSHProfile{}, err
	}
	index, found := findCLISSHProfile(config.Profiles, name)
	if !found {
		return cliSSHProfile{}, fmt.Errorf("SSH profile %q does not exist; add it with `flclash ssh add`", name)
	}
	return config.Profiles[index], nil
}
func findCLISSHProfile(profiles []cliSSHProfile, name string) (int, bool) {
	for index, profile := range profiles {
		if strings.EqualFold(profile.Name, name) {
			return index, true
		}
	}
	return -1, false
}

func loadCLISSHProfileViews() ([]cliSSHProfileView, error) {
	config, err := loadCLISSHConfig()
	if err != nil {
		return nil, err
	}
	active, connected, err := activeCLIPersistentSSHTunnel()
	if err != nil {
		return nil, err
	}
	views := make([]cliSSHProfileView, 0, len(config.Profiles))
	for _, profile := range config.Profiles {
		view := cliSSHProfileView{
			Name:          profile.Name,
			Destination:   profile.Destination,
			Port:          profile.Port,
			LocalPort:     profile.LocalPort,
			Identity:      profile.Identity,
			Options:       append([]string(nil), profile.Options...),
			PassphraseSet: profile.IdentityPassphrase != "",
			PasswordSet:   profile.Password != "",
		}
		if connected && strings.EqualFold(active.Name, profile.Name) {
			view.Connected = true
			view.Ready = cliSSHSOCKSReady(active.Port)
			view.SocksPort = active.Port
		}
		views = append(views, view)
	}
	return views, nil
}

func cliSSHMaskedSecret(set bool) string {
	if set {
		return "********"
	}
	return "not saved"
}

func startCLISSHTunnel(profile cliSSHProfile, kind string) (cliSSHTunnelState, error) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return cliSSHTunnelState{}, errors.New("OpenSSH client `ssh` is required")
	}
	runtimeDirectory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		return cliSSHTunnelState{}, err
	}
	configuredPort := configuredCLISSHLocalPort(profile, kind)
	fixedPort := configuredPort > 0
	attemptLimit := 3
	if fixedPort {
		attemptLimit = 1
	}
	for attempt := 0; attempt < attemptLimit; attempt++ {
		port := configuredPort
		if !fixedPort {
			port, err = allocateCLISSHPort()
			if err != nil {
				return cliSSHTunnelState{}, err
			}
		} else if err := waitCLISSHPortAvailable(port, time.Second); err != nil {
			return cliSSHTunnelState{}, err
		}
		digest := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d:%s", profile.Name, os.Getpid(), time.Now().UnixNano(), kind)))
		controlPath := filepath.Join(runtimeDirectory, fmt.Sprintf("ctl-%x.sock", digest[:8]))
		state := cliSSHTunnelState{Name: profile.Name, Destination: profile.Destination, Port: port, ControlPath: controlPath, Kind: kind, StartedAt: time.Now()}
		args := cliSSHTunnelArguments(profile, controlPath)
		args = append(args, profile.Destination)
		command := exec.Command(sshPath, args...)
		command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
		cleanup, err := configureCLISSHAskpass(
			command,
			profile.IdentityPassphrase,
			profile.Password,
		)
		if err != nil {
			return state, err
		}
		runErr := command.Run()
		cleanup()
		if runErr == nil {
			if forwardErr := addCLISSHDynamicForwardForOperation(sshPath, state); forwardErr != nil {
				runErr = errors.Join(
					fmt.Errorf("configure SSH SOCKS5 forward: %w", forwardErr),
					stopCLIStateTunnel(state),
				)
			}
		}
		if runErr != nil {
			_ = os.Remove(controlPath)
			if attempt < attemptLimit-1 {
				continue
			}
			return state, fmt.Errorf("start SSH SOCKS5 tunnel %q: %w", profile.Name, runErr)
		}
		state.StatePath = filepath.Join(runtimeDirectory, fmt.Sprintf("%s-%d-%d.json", kind, os.Getpid(), time.Now().UnixNano()))
		if err := saveCLISSHTunnelState(state); err != nil {
			_ = stopCLIStateTunnel(state)
			return state, err
		}
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			if cliSSHTunnelAlive(sshPath, state) {
				return state, nil
			}
			time.Sleep(50 * time.Millisecond)
		}
		_ = stopCLIStateTunnel(state)
		return state, fmt.Errorf("SSH tunnel %q did not become ready", profile.Name)
	}
	return cliSSHTunnelState{}, errors.New("could not allocate a local SSH SOCKS5 port")
}

func startCLITransientSSHTunnel(profile cliSSHProfile) (cliSSHTunnelState, error) {
	lock, err := lockCLISSHTunnelOperation()
	if err != nil {
		return cliSSHTunnelState{}, err
	}
	defer lock.release()
	return startCLISSHTunnel(profile, "transient")
}

func stopCLITransientSSHTunnel(state cliSSHTunnelState) error {
	lock, err := waitCLISSHTunnelOperationLock(15 * time.Second)
	if err != nil {
		return err
	}
	defer lock.release()
	return stopCLIStateTunnel(state)
}

func waitCLISSHTunnelOperationLock(timeout time.Duration) (*cliFileLock, error) {
	deadline := time.Now().Add(timeout)
	for {
		lock, err := lockCLISSHTunnelOperation()
		if err == nil {
			return lock, nil
		}
		var busy *cliLockBusyError
		if !errors.As(err, &busy) || time.Now().After(deadline) {
			return nil, err
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func cliSSHTunnelArguments(
	profile cliSSHProfile,
	controlPath string,
) []string {
	args := []string{
		"-f",
		"-N",
		"-M",
		"-S", controlPath,
		"-p", strconv.Itoa(profile.Port),
		"-o", "ControlMaster=yes",
		"-o", "ControlPersist=no",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ClearAllForwardings=yes",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
	}
	if !cliSSHOptionConfigured(profile.Options, "StrictHostKeyChecking") {
		args = append(args, "-o", "StrictHostKeyChecking=accept-new")
	}
	if profile.Identity != "" {
		args = append(args, "-i", profile.Identity)
	}
	for _, option := range profile.Options {
		args = append(args, "-o", option)
	}
	return args
}

func addCLISSHDynamicForward(sshPath string, state cliSSHTunnelState) error {
	command := exec.Command(sshPath, cliSSHDynamicForwardArguments(state)...)
	command.Stdout, command.Stderr = os.Stdout, os.Stderr
	return command.Run()
}

func cliSSHDynamicForwardArguments(state cliSSHTunnelState) []string {
	return []string{
		"-S", state.ControlPath,
		"-O", "forward",
		"-D", net.JoinHostPort("127.0.0.1", strconv.Itoa(state.Port)),
		state.Destination,
	}
}

func cliSSHOptionConfigured(options []string, wantedKey string) bool {
	for _, option := range options {
		key, _, found := strings.Cut(option, "=")
		if found && strings.EqualFold(strings.TrimSpace(key), wantedKey) {
			return true
		}
	}
	return false
}

func startCLIPersistentSSHTunnel(profile cliSSHProfile) (cliSSHTunnelState, error) {
	state, err := startCLISSHTunnel(profile, "persistent")
	if err != nil {
		return cliSSHTunnelState{}, err
	}
	runtimeDirectory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		return cliSSHTunnelState{}, errors.Join(err, stopCLIStateTunnel(state))
	}
	persistentPath := filepath.Join(runtimeDirectory, cliSSHPersistentStateFile)
	if state.StatePath == persistentPath {
		return state, nil
	}
	if err := os.Rename(state.StatePath, persistentPath); err != nil {
		return cliSSHTunnelState{}, errors.Join(err, stopCLIStateTunnel(state))
	}
	state.StatePath = persistentPath
	if err := saveCLISSHTunnelState(state); err != nil {
		return cliSSHTunnelState{}, errors.Join(err, stopCLIStateTunnel(state))
	}
	return state, nil
}

func configuredCLISSHLocalPort(profile cliSSHProfile, kind string) int {
	if kind != "persistent" {
		return 0
	}
	return profile.LocalPort
}

func configureCLISSHAskpass(
	command *exec.Cmd,
	identityPassphrase,
	password string,
) (func(), error) {
	if identityPassphrase == "" && password == "" {
		return func() {}, nil
	}
	directory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		return nil, err
	}
	if err := cleanupCLISSHAskpassSecrets(directory); err != nil {
		return nil, err
	}
	secret, err := os.CreateTemp(directory, "askpass-*.secret")
	if err != nil {
		return nil, err
	}
	path := secret.Name()
	cleanup := func() { _ = secret.Close(); _ = os.Remove(path) }
	if err := secret.Chmod(0o600); err != nil {
		cleanup()
		return nil, err
	}
	data, err := json.Marshal(cliSSHAskpassSecrets{
		IdentityPassphrase: identityPassphrase,
		Password:           password,
	})
	if err != nil {
		cleanup()
		return nil, err
	}
	if _, err := secret.Write(data); err != nil {
		cleanup()
		return nil, err
	}
	if err := secret.Close(); err != nil {
		cleanup()
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		cleanup()
		return nil, err
	}
	command.Env = cliSSHAskpassEnvironment(os.Environ(), executable, path)
	return cleanup, nil
}

func cliSSHAskpassEnvironment(environment []string, executable, secretPath string) []string {
	replaced := map[string]bool{
		"SSH_ASKPASS":         true,
		"SSH_ASKPASS_REQUIRE": true,
		"DISPLAY":             true,
		"LC_ALL":              true,
		cliSSHAskpassFileEnv:  true,
	}
	result := make([]string, 0, len(environment)+len(replaced))
	for _, item := range environment {
		key, _, found := strings.Cut(item, "=")
		if found && replaced[key] {
			continue
		}
		result = append(result, item)
	}
	return append(
		result,
		"SSH_ASKPASS="+executable,
		"SSH_ASKPASS_REQUIRE=force",
		"DISPLAY=flclash-askpass",
		"LC_ALL=C",
		cliSSHAskpassFileEnv+"="+secretPath,
	)
}

func cleanupCLISSHAskpassSecrets(directory string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var cleanupErrors []error
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "askpass-") ||
			!strings.HasSuffix(name, ".secret") {
			continue
		}
		path := filepath.Join(directory, name)
		info, err := os.Lstat(path)
		if err != nil {
			if !os.IsNotExist(err) {
				cleanupErrors = append(cleanupErrors, err)
			}
			continue
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
			!cliPathOwnedByCurrentUser(info) || info.Mode().Perm()&0o077 != 0 {
			cleanupErrors = append(
				cleanupErrors,
				fmt.Errorf("unsafe stale SSH askpass secret %q", path),
			)
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			cleanupErrors = append(
				cleanupErrors,
				fmt.Errorf("remove stale SSH askpass secret %q: %w", path, err),
			)
		}
	}
	return errors.Join(cleanupErrors...)
}

func cliSSHAskpassCommand(args []string) error {
	path := os.Getenv(cliSSHAskpassFileEnv)
	answer, err := cliSSHAskpassAnswer(strings.Join(args, " "), path)
	if err != nil {
		return err
	}
	_, err = io.WriteString(os.Stdout, answer)
	return err
}

func cliSSHAskpassAnswer(prompt, path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !cliPathOwnedByCurrentUser(info) || info.Mode().Perm()&0o077 != 0 {
		return "", errors.New("unsafe SSH askpass secret")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var secrets cliSSHAskpassSecrets
	if err := json.Unmarshal(data, &secrets); err != nil {
		return "", errors.New("invalid SSH askpass secret")
	}
	lowerPrompt := strings.ToLower(prompt)
	switch {
	case strings.Contains(lowerPrompt, "passphrase"):
		if secrets.IdentityPassphrase == "" {
			return "", errors.New("no private key passphrase is saved")
		}
		return secrets.IdentityPassphrase, nil
	case strings.Contains(lowerPrompt, "password"):
		if secrets.Password == "" {
			return "", errors.New("no SSH password is saved")
		}
		return secrets.Password, nil
	default:
		return "", errors.New("unsupported SSH authentication prompt")
	}
}

func allocateCLISSHPort() (int, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port, nil
}

func checkCLISSHPortAvailable(port int) error {
	listener, err := net.Listen(
		"tcp4",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
	)
	if err != nil {
		return fmt.Errorf("local SSH SOCKS5 port %d is already in use: %w", port, err)
	}
	return listener.Close()
}

func waitCLISSHPortAvailable(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		lastErr = checkCLISSHPortAvailable(port)
		if lastErr == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return lastErr
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func ensureCLISSHRuntimeDirectory() (string, error) {
	base, err := ensureCLIRuntimeDirectory()
	if err != nil {
		return "", err
	}
	directory := filepath.Join(base, cliSSHRuntimeDirectoryName)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || !cliPathOwnedByCurrentUser(info) {
		return "", fmt.Errorf("unsafe SSH runtime directory %q", directory)
	}
	if info.Mode().Perm()&0o077 != 0 {
		_ = os.Chmod(directory, 0o700)
	}
	return directory, nil
}

func lockCLISSHTunnelOperation() (*cliFileLock, error) {
	directory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		return nil, err
	}
	lock, err := acquireCLIFileLock(
		filepath.Join(directory, "operation.lock"),
		cliProcessOwner{
			Kind:      "ssh-tunnel",
			PID:       os.Getpid(),
			StartedAt: time.Now(),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("another SSH tunnel operation is running: %w", err)
	}
	return lock, nil
}

func cliSSHTunnelAlive(sshPath string, state cliSSHTunnelState) bool {
	if state.Port < 1 || state.ControlPath == "" {
		return false
	}
	if !cliSSHMasterAlive(sshPath, state) {
		return false
	}
	return cliSSHSOCKSReady(state.Port)
}

func cliSSHSOCKSReady(port int) bool {
	if port < 1 || port > 65535 {
		return false
	}
	connection, err := net.DialTimeout(
		"tcp",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		250*time.Millisecond,
	)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func cliSSHMasterAlive(sshPath string, state cliSSHTunnelState) bool {
	if state.ControlPath == "" {
		return false
	}
	check := exec.Command(sshPath, "-S", state.ControlPath, "-O", "check", state.Destination)
	return check.Run() == nil
}
func saveCLISSHTunnelState(state cliSSHTunnelState) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeCLISSHFileAtomically(state.StatePath, append(data, '\n'))
}
func loadCLISSHTunnelState(path string) (cliSSHTunnelState, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return cliSSHTunnelState{}, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !cliPathOwnedByCurrentUser(info) || info.Mode().Perm()&0o077 != 0 {
		return cliSSHTunnelState{}, fmt.Errorf("unsafe SSH runtime state %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cliSSHTunnelState{}, err
	}
	var state cliSSHTunnelState
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	state.StatePath = path
	return state, nil
}

func activeCLIPersistentSSHTunnel() (cliSSHTunnelState, bool, error) {
	directory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		return cliSSHTunnelState{}, false, err
	}
	path := filepath.Join(directory, cliSSHPersistentStateFile)
	state, err := loadCLISSHTunnelState(path)
	if os.IsNotExist(err) {
		return cliSSHTunnelState{}, false, nil
	}
	if err != nil {
		return state, false, err
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return state, false,
			errors.New("OpenSSH client `ssh` is required to inspect the SSH tunnel")
	}
	if cliSSHMasterAlive(sshPath, state) {
		return state, true, nil
	}
	_ = os.Remove(state.ControlPath)
	_ = os.Remove(path)
	return cliSSHTunnelState{}, false, nil
}

func stopCLIStateTunnel(state cliSSHTunnelState) error {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return errors.New("OpenSSH client `ssh` is required to stop the SSH tunnel")
	}
	if cliSSHMasterAlive(sshPath, state) {
		command := exec.Command(
			sshPath,
			"-S",
			state.ControlPath,
			"-O",
			"exit",
			state.Destination,
		)
		if err := command.Run(); err != nil && cliSSHMasterAlive(sshPath, state) {
			return fmt.Errorf("stop SSH tunnel %q: %w", state.Name, err)
		}
		deadline := time.Now().Add(time.Second)
		for cliSSHMasterAlive(sshPath, state) && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
		}
		if cliSSHMasterAlive(sshPath, state) {
			return fmt.Errorf("SSH tunnel %q did not stop", state.Name)
		}
	}
	var cleanupErrors []error
	if state.ControlPath != "" {
		if err := os.Remove(state.ControlPath); err != nil && !os.IsNotExist(err) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove SSH control socket: %w", err))
		}
	}
	if state.StatePath != "" {
		if err := os.Remove(state.StatePath); err != nil && !os.IsNotExist(err) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("remove SSH runtime state: %w", err))
		}
	}
	return errors.Join(cleanupErrors...)
}

func stopAllCLISSHTunnels() error {
	lock, err := waitCLISSHTunnelOperationLock(15 * time.Second)
	if err != nil {
		return err
	}
	defer lock.release()
	directory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	var stopErrors []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(directory, entry.Name())
		state, stateErr := loadCLISSHTunnelState(path)
		if stateErr == nil {
			if err := stopCLIStateTunnelForOperation(state); err != nil {
				stopErrors = append(stopErrors, err)
			}
		} else {
			stopErrors = append(
				stopErrors,
				fmt.Errorf("inspect SSH runtime state %q: %w", path, stateErr),
			)
		}
	}
	if err := cleanupCLISSHAskpassSecrets(directory); err != nil {
		stopErrors = append(stopErrors, err)
	}
	return errors.Join(stopErrors...)
}

func runCLICommandWithSSHProxy(args []string, port int) error {
	executable, err := exec.LookPath(args[0])
	if err != nil {
		return fmt.Errorf("command not found or cannot be executed: %q (%v)", args[0], err)
	}
	args = cliWrappedCommandArguments(executable, args)
	proxyURL := "socks5h://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	command := exec.Command(executable, args[1:]...)
	command.Args, command.Env = args, cliProxyEnvironment(os.Environ(), proxyURL)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	if err := command.Start(); err != nil {
		return fmt.Errorf("cannot start command %q: %w", args[0], err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	interrupt := make(chan os.Signal, 2)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(interrupt)
	var result error
	select {
	case result = <-done:
	case received := <-interrupt:
		_ = command.Process.Signal(received)
		select {
		case <-done:
		case <-time.After(cliExitTerminatePeriod):
			_ = command.Process.Kill()
			<-done
		}
		if received == syscall.SIGINT {
			return &cliExitCodeError{code: 130}
		}
		return &cliExitCodeError{code: 143}
	}
	if result != nil {
		var exitError *exec.ExitError
		if errors.As(result, &exitError) {
			return &cliExitCodeError{code: exitError.ExitCode()}
		}
		return fmt.Errorf("command %q failed: %w", args[0], result)
	}
	return nil
}
