//go:build linux && !cgo && cli

package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	cliSSHConfigFilename       = ".flclash-ssh.json"
	cliSSHConfigLockFilename   = ".flclash-ssh.lock"
	cliSSHRuntimeDirectoryName = "ssh"
	cliSSHPersistentStateFile  = "persistent.json"
	cliSSHLastErrorFile        = "last-error.json"
	cliSSHConfigVersion        = 2
	cliSSHAskpassFileEnv       = "FLCLASH_SSH_ASKPASS_FILE"
	cliSSHRemoteProbeVersion   = 1
)

type cliExitCodeError struct{ code int }

func (e *cliExitCodeError) Error() string { return "" }
func (e *cliExitCodeError) ExitCode() int { return e.code }

type cliSSHProfile struct {
	Name               string   `json:"name"`
	Username           string   `json:"username,omitempty"`
	Host               string   `json:"host,omitempty"`
	Destination        string   `json:"-"`
	Port               int      `json:"port"`
	LocalPort          int      `json:"local_port,omitempty"`
	Jump               string   `json:"jump,omitempty"`
	Identity           string   `json:"identity,omitempty"`
	IdentityPassphrase string   `json:"identity_passphrase,omitempty"`
	Password           string   `json:"password,omitempty"`
	Options            []string `json:"options,omitempty"`
}

type cliSSHConfig struct {
	Version  int             `json:"version"`
	Default  string          `json:"default,omitempty"`
	Profiles []cliSSHProfile `json:"profiles"`
}

type cliSSHLastError struct {
	Name  string    `json:"name"`
	Error string    `json:"error"`
	At    time.Time `json:"at"`
}

type cliSSHTunnelState struct {
	Name         string    `json:"name"`
	Destination  string    `json:"destination"`
	Port         int       `json:"port"`
	UpstreamPort int       `json:"upstream_port,omitempty"`
	ControlPath  string    `json:"control_path"`
	RelayControl string    `json:"relay_control,omitempty"`
	RelayPID     int       `json:"relay_pid,omitempty"`
	Kind         string    `json:"kind"`
	StartedAt    time.Time `json:"started_at"`
	StatePath    string    `json:"-"`
}

type cliSSHProfileView struct {
	Name          string    `json:"name"`
	Username      string    `json:"username,omitempty"`
	Host          string    `json:"host,omitempty"`
	Destination   string    `json:"destination"`
	Port          int       `json:"port"`
	LocalPort     int       `json:"local_port,omitempty"`
	Jump          string    `json:"jump,omitempty"`
	Identity      string    `json:"identity,omitempty"`
	Options       []string  `json:"options,omitempty"`
	PassphraseSet bool      `json:"identity_passphrase_set"`
	PasswordSet   bool      `json:"password_set"`
	NeedsUsername bool      `json:"needs_username"`
	Default       bool      `json:"default,omitempty"`
	Connected     bool      `json:"connected"`
	Attached      bool      `json:"attached,omitempty"`
	Attachable    bool      `json:"attachable,omitempty"`
	Ready         bool      `json:"ready"`
	SocksPort     int       `json:"socks_port,omitempty"`
	StartedAt     time.Time `json:"started_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
}

type cliSSHProfileEdit struct {
	Name, Username, Host, Destination            string
	Jump, Identity, IdentityPassphrase, Password string
	Port, LocalPort                              int
	Options                                      []string
	PortSet, LocalPortSet, IdentitySet           bool
	JumpSet, ClearJump                           bool
	PassphraseSet, ClearPassphrase               bool
	PasswordSet, ClearPassword, OptionsSet       bool
}

type cliSSHProfileJSON struct {
	Name               string   `json:"name"`
	Username           string   `json:"username,omitempty"`
	Host               string   `json:"host,omitempty"`
	Destination        string   `json:"destination,omitempty"`
	Port               int      `json:"port"`
	LocalPort          int      `json:"local_port,omitempty"`
	Jump               string   `json:"jump,omitempty"`
	Identity           string   `json:"identity,omitempty"`
	IdentityPassphrase string   `json:"identity_passphrase,omitempty"`
	Password           string   `json:"password,omitempty"`
	Options            []string `json:"options,omitempty"`
}

func (profile cliSSHProfile) MarshalJSON() ([]byte, error) {
	profile = normalizeCLISSHProfile(profile)
	return json.Marshal(cliSSHProfileJSON{
		Name:               profile.Name,
		Username:           profile.Username,
		Host:               profile.Host,
		Port:               profile.Port,
		LocalPort:          profile.LocalPort,
		Jump:               profile.Jump,
		Identity:           profile.Identity,
		IdentityPassphrase: profile.IdentityPassphrase,
		Password:           profile.Password,
		Options:            profile.Options,
	})
}

func (profile *cliSSHProfile) UnmarshalJSON(data []byte) error {
	var value cliSSHProfileJSON
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*profile = normalizeCLISSHProfile(cliSSHProfile{
		Name:               value.Name,
		Username:           value.Username,
		Host:               value.Host,
		Destination:        value.Destination,
		Port:               value.Port,
		LocalPort:          value.LocalPort,
		Jump:               value.Jump,
		Identity:           value.Identity,
		IdentityPassphrase: value.IdentityPassphrase,
		Password:           value.Password,
		Options:            value.Options,
	})
	return nil
}

func splitCLISSHDestination(destination string) (string, string) {
	destination = strings.TrimSpace(destination)
	separator := strings.LastIndex(destination, "@")
	if separator <= 0 || separator >= len(destination)-1 {
		return "", destination
	}
	return destination[:separator], destination[separator+1:]
}

func formatCLISSHDestination(username, host string) string {
	if username == "" {
		return host
	}
	return username + "@" + host
}

func normalizeCLISSHProfile(profile cliSSHProfile) cliSSHProfile {
	profile.Username = strings.TrimSpace(profile.Username)
	profile.Host = strings.TrimSpace(profile.Host)
	profile.Jump = strings.TrimSpace(profile.Jump)
	if profile.Host == "" && profile.Destination != "" {
		profile.Username, profile.Host = splitCLISSHDestination(profile.Destination)
	}
	profile.Destination = formatCLISSHDestination(profile.Username, profile.Host)
	return profile
}

type cliSSHAskpassSecrets struct {
	IdentityPassphrase string `json:"identity_passphrase,omitempty"`
	Password           string `json:"password,omitempty"`
}

type cliSSHCredentials struct {
	IdentityPassphrase string
}

// cliSSHRemoteProbe is intentionally small and read-only. It is executed on
// the SSH host before a direct command is allowed: once a remote transparent
// TUN is active, a normal SSH dynamic forward can no longer honestly promise
// to bypass that host's FlClash traffic policy.
type cliSSHRemoteProbe struct {
	ProtocolVersion int    `json:"protocol_version"`
	Available       bool   `json:"available"`
	Version         string `json:"version,omitempty"`
	BackendRunning  bool   `json:"backend_running"`
	CoreRunning     bool   `json:"core_running"`
	TunEnabled      bool   `json:"tun_enabled"`
	DirectAllowed   bool   `json:"direct_allowed"`
	IntranetIP      string `json:"intranet_ip,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

type cliSSHIdentityKind byte

const (
	cliSSHIdentityNone cliSSHIdentityKind = iota
	cliSSHIdentityUnencrypted
	cliSSHIdentityEncrypted
)

type cliSSHCredentialRequiredError struct {
	Profile  string
	Identity string
}

const cliSSHCommandOutputLimit = 64 * 1024

var cliSSHAskpassExecutable = os.Executable

type cliSSHCappedBuffer struct {
	buffer bytes.Buffer
}

func (buffer *cliSSHCappedBuffer) Write(data []byte) (int, error) {
	originalLength := len(data)
	remaining := cliSSHCommandOutputLimit - buffer.buffer.Len()
	if remaining > 0 {
		if len(data) > remaining {
			data = data[:remaining]
		}
		_, _ = buffer.buffer.Write(data)
	}
	return originalLength, nil
}

func (buffer *cliSSHCappedBuffer) String() string {
	return buffer.buffer.String()
}

func (err *cliSSHCredentialRequiredError) Error() string {
	return fmt.Sprintf(
		"private key %q is encrypted; enter its passphrase to connect",
		err.Identity,
	)
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
	case "default":
		return cliSSHDefaultCommand(args[1:])
	case "import":
		return cliSSHImportCommand(args[1:])
	case "attach":
		return cliSSHAttachCommand(args[1:])
	case "probe":
		return cliSSHProbeCommand(args[1:])
	default:
		return fmt.Errorf("unknown ssh command %q; use `flclash ssh -help`", args[0])
	}
}

func printSSHManagementUsage(w io.Writer) {
	fmt.Fprintln(w, "Proxy local traffic through an SSH host's network exit.")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  flclash ssh add NAME HOST --user USER [--port PORT] [--local-port PORT|auto] [--jump HOST] [--identity PATH] [--passphrase] [--password] [--option KEY=VALUE]")
	fmt.Fprintln(w, "  flclash ssh add NAME user@host [OPTIONS]  # compatibility syntax")
	fmt.Fprintln(w, "  flclash ssh edit NAME [HOST] [--user USER] [OPTIONS]")
	fmt.Fprintln(w, "  flclash ssh import [HOST...] [--file PATH]")
	fmt.Fprintln(w, "  flclash ssh default [NAME|--clear]")
	fmt.Fprintln(w, "  flclash ssh delete|show NAME")
	fmt.Fprintln(w, "  flclash ssh list|status [--json]")
	fmt.Fprintln(w, "  flclash ssh connect [NAME]")
	fmt.Fprintln(w, "  flclash ssh attach [NAME] | --list")
	fmt.Fprintln(w, "  flclash ssh disconnect [NAME|all]")
	fmt.Fprintln(w, "  flclash ssh test [NAME]")
	fmt.Fprintln(w, "  flclash ssh probe --json  # read-only endpoint check for flc ssh -d")
	fmt.Fprintln(w, "`ssh add` with no arguments and `ssh edit NAME` open an interactive prompt.")
	fmt.Fprintln(w, "`ssh connect` and `flc ssh COMMAND` use the default profile, or the only profile, when NAME is omitted.")
	fmt.Fprintln(w, "`ssh connect` reuses a live OpenSSH ControlMaster for that host when one exists; `ssh attach` only captures, never starts a new login.")
	fmt.Fprintln(w, "A broken persistent tunnel is rebuilt automatically before `flc ssh COMMAND` runs.")
}

var (
	activeCLIPersistentSSHTunnelForCommand        = activeCLIPersistentSSHTunnel
	connectCLISSHProfileForCommand                = connectCLISSHProfile
	connectCLISSHProfileWithCredentialsForCommand = connectCLISSHProfileWithCredentials
	ensureCLIPersistentSSHTunnelForCommand        = ensureCLIPersistentSSHTunnel
	promptCLISSHSecretOnceForCommand              = promptCLISSHSecretOnce
	startCLITransientSSHTunnelForCommand          = startCLITransientSSHTunnel
	stopCLITransientSSHTunnelForCommand           = stopCLITransientSSHTunnel
	runCLICommandWithSSHProxyForCommand           = runCLICommandWithSSHProxy
	probeCLISSHRemoteForCommand                   = probeCLISSHRemote
)

func flcSSHCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		printFLCSSHUsage(os.Stdout)
		return nil
	}
	profileName := ""
	direct := false
	for len(args) > 0 {
		switch {
		case args[0] == "-d" || args[0] == "--direct":
			direct = true
			args = args[1:]
		case args[0] == "-u" || args[0] == "--use":
			if len(args) < 2 || strings.TrimSpace(args[1]) == "" {
				return errors.New("usage: flc ssh [-d] -u NAME COMMAND [ARG...]")
			}
			if profileName != "" {
				return errors.New("SSH profile may only be selected once")
			}
			profileName, args = args[1], args[2:]
		case strings.HasPrefix(args[0], "--use="):
			if profileName != "" || strings.TrimSpace(strings.TrimPrefix(args[0], "--use=")) == "" {
				return errors.New("SSH profile may only be selected once")
			}
			profileName, args = strings.TrimPrefix(args[0], "--use="), args[1:]
		default:
			goto command
		}
	}

command:
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
			state, err = startCLITransientSSHTunnelAfterPassphrase(profile, err)
		}
		if err != nil {
			return err
		}
		commandErr := runCLICommandThroughSSHForCommand(args, state, direct)
		stopErr := stopCLITransientSSHTunnelForCommand(state)
		return errors.Join(commandErr, stopErr)
	}
	state, err := ensureCLIPersistentSSHTunnelForCommand("")
	if err != nil {
		state, err = retryCLISSHConnectWithPassphrase("", err)
	}
	if err != nil {
		return err
	}
	if !cliSSHTunnelReady(state) {
		return fmt.Errorf(
			"SSH tunnel %q is connected but its SOCKS5 listener is unavailable",
			state.Name,
		)
	}
	return runCLICommandThroughSSHForCommand(args, state, direct)
}

func startCLITransientSSHTunnelAfterPassphrase(
	profile cliSSHProfile,
	err error,
) (cliSSHTunnelState, error) {
	passphrase, name, promptErr := promptCLISSHPassphraseFromError(err)
	if promptErr != nil {
		return cliSSHTunnelState{}, promptErr
	}
	if name != "" && !strings.EqualFold(name, profile.Name) {
		return cliSSHTunnelState{}, err
	}
	profile, prepErr := prepareCLISSHProfileCredentials(
		profile,
		cliSSHCredentials{IdentityPassphrase: passphrase},
	)
	if prepErr != nil {
		return cliSSHTunnelState{}, prepErr
	}
	return startCLITransientSSHTunnelForCommand(profile)
}

func retryCLISSHConnectWithPassphrase(
	name string,
	err error,
) (cliSSHTunnelState, error) {
	passphrase, profileName, promptErr := promptCLISSHPassphraseFromError(err)
	if promptErr != nil {
		return cliSSHTunnelState{}, promptErr
	}
	if name == "" {
		name = profileName
	}
	state, _, connErr := connectCLISSHProfileWithCredentialsForCommand(
		name,
		cliSSHCredentials{IdentityPassphrase: passphrase},
	)
	return state, connErr
}

func promptCLISSHPassphraseFromError(err error) (string, string, error) {
	var required *cliSSHCredentialRequiredError
	if !errors.As(err, &required) {
		return "", "", err
	}
	passphrase, promptErr := promptCLISSHSecretOnceForCommand("Private key passphrase")
	if promptErr != nil {
		return "", required.Profile, promptErr
	}
	return passphrase, required.Profile, nil
}

func ensureCLIPersistentSSHTunnel(name string) (cliSSHTunnelState, error) {
	state, active, err := activeCLIPersistentSSHTunnelForCommand()
	if err != nil {
		return cliSSHTunnelState{}, err
	}
	if active && cliSSHTunnelReady(state) &&
		(name == "" || strings.EqualFold(state.Name, name)) {
		return state, nil
	}
	if name == "" && active {
		name = state.Name
	}
	if name == "" {
		name, err = resolveCLISSHConnectName("")
		if err != nil {
			return cliSSHTunnelState{}, err
		}
	} else if !active || !strings.EqualFold(state.Name, name) {
		if _, err := resolveCLISSHConnectName(name); err != nil {
			return cliSSHTunnelState{}, err
		}
	}
	state, _, err = connectCLISSHProfileForCommand(name)
	if err != nil {
		return cliSSHTunnelState{}, fmt.Errorf("open SSH tunnel %q: %w", name, err)
	}
	if !cliSSHTunnelReady(state) {
		message := fmt.Sprintf(
			"SSH tunnel %q is connected but its SOCKS5 listener is unavailable",
			state.Name,
		)
		_ = saveCLISSHLastError(state.Name, message)
		return state, errors.New(message)
	}
	return state, nil
}

func resolveCLISSHConnectName(name string) (string, error) {
	name = strings.TrimSpace(name)
	config, err := loadCLISSHConfig()
	if err != nil {
		return "", err
	}
	if name != "" {
		if _, found := findCLISSHProfile(config.Profiles, name); !found {
			return "", fmt.Errorf("SSH profile %q does not exist; add it with `flclash ssh add` or `flclash ssh import`", name)
		}
		return name, nil
	}
	if defaultName := strings.TrimSpace(config.Default); defaultName != "" {
		if _, found := findCLISSHProfile(config.Profiles, defaultName); found {
			return defaultName, nil
		}
	}
	switch len(config.Profiles) {
	case 0:
		return "", errors.New("no SSH profiles configured; add one with `flclash ssh add` or `flclash ssh import`")
	case 1:
		return config.Profiles[0].Name, nil
	default:
		return "", errors.New("no SSH tunnel is open; run `flclash ssh connect NAME`, set a default with `flclash ssh default NAME`, or use `flc ssh -u NAME COMMAND`")
	}
}

func runCLICommandThroughSSHForCommand(
	args []string,
	state cliSSHTunnelState,
	direct bool,
) error {
	if direct {
		if err := requireCLISSHDirectExit(state); err != nil {
			return err
		}
	}
	return runCLICommandWithSSHProxyForCommand(args, state.Port)
}

func requireCLISSHDirectExit(state cliSSHTunnelState) error {
	probe, err := probeCLISSHRemoteForCommand(state)
	if err != nil {
		return fmt.Errorf("verify direct SSH exit on %q: %w", state.Name, err)
	}
	if probe.ProtocolVersion != cliSSHRemoteProbeVersion {
		return fmt.Errorf(
			"SSH host direct check is incompatible (protocol %d); update FlClash on the SSH host",
			probe.ProtocolVersion,
		)
	}
	if !probe.Available {
		reason := cliDisplayValue(probe.Reason)
		return fmt.Errorf(
			"SSH host cannot verify a direct exit: %s; start/update FlClash on the SSH host",
			reason,
		)
	}
	if !probe.DirectAllowed {
		reason := probe.Reason
		if reason == "" {
			reason = "transparent TUN is enabled"
		}
		return fmt.Errorf(
			"SSH direct exit refused: %s; use `flc ssh COMMAND` to follow the SSH host network policy",
			reason,
		)
	}
	return nil
}

func printFLCSSHUsage(w io.Writer) {
	fmt.Fprintln(w, "Run one local command through an SSH host's network policy.")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  flc ssh COMMAND [ARG...]")
	fmt.Fprintln(w, "  flc ssh [-d|--direct] [-u NAME|--use NAME] COMMAND [ARG...]")
	fmt.Fprintln(w, "Without -u, a persistent tunnel is opened for the default or only SSH profile.")
	fmt.Fprintln(w, "A broken persistent tunnel is rebuilt before the command runs.")
	fmt.Fprintln(w, "-u creates a temporary tunnel which is always closed after the command.")
	fmt.Fprintln(w, "Without -d, the SSH host decides whether Clash/TUN/routing handles the flow.")
	fmt.Fprintln(w, "-d fails closed when the SSH host reports a transparent FlClash TUN.")
}

func cliSSHProbeCommand(args []string) error {
	if len(args) != 1 || args[0] != "--json" {
		return errors.New("usage: flclash ssh probe --json")
	}
	probe := cliSSHRemoteProbe{
		ProtocolVersion: cliSSHRemoteProbeVersion,
		Version:         cliVersion,
	}
	_, status, err := currentManagedServiceRaw()
	if err != nil {
		probe.Reason = "FlClash Backend is unavailable"
		return writeCLIJSON(os.Stdout, probe)
	}
	probe.Available = true
	probe.BackendRunning = true
	probe.CoreRunning = status.Running
	probe.TunEnabled = status.TunState == "on"
	probe.DirectAllowed = !probe.TunEnabled
	probe.IntranetIP = detectTUIIntranetIP()
	if probe.TunEnabled {
		probe.Reason = "FlClash transparent TUN is enabled"
	} else {
		probe.Reason = "FlClash TUN is off"
	}
	return writeCLIJSON(os.Stdout, probe)
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
		return errors.New("usage: flclash ssh edit NAME [HOST] [--user USER] [OPTIONS]")
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
		marker := "  "
		if view.Default {
			marker = "* "
		}
		if view.NeedsUsername {
			status, endpoint = "NEEDS USER", " · edit before connecting"
		} else if view.Connected && view.Ready {
			configured := "auto"
			if view.LocalPort > 0 {
				configured = strconv.Itoa(view.LocalPort)
			}
			status = "CONNECTED"
			if view.Attached {
				status = "ATTACHED"
			}
			endpoint = fmt.Sprintf(
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
		if view.Jump != "" {
			endpoint += " · via " + view.Jump
		}
		if view.LastError != "" && !(view.Connected && view.Ready) {
			endpoint += " · last error: " + view.LastError
		}
		auth := cliSSHAuthenticationLabel(
			view.Identity,
			view.PassphraseSet,
			view.PasswordSet,
		)
		fmt.Printf("%s%-16s %-12s %-28s · %s%s\n", marker, view.Name, status, view.Destination, auth, endpoint)
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
			"Name:                  %s\nDefault:               %s\nSSH username:          %s\nSSH host:              %s\nJump host:             %s\nSSH port:              %d\nLocal SOCKS:           %s\nIdentity(private key): %s\nKey passphrase:        %s\nSSH password:          %s\nConnected:             %s\nSOCKS5 ready:          %s\n",
			view.Name,
			cliOnOff(view.Default),
			cliDisplayValue(view.Username),
			view.Host,
			cliDisplayValue(view.Jump),
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
		if view.LastError != "" {
			fmt.Printf("Last error:            %s\n", view.LastError)
		}
		return nil
	}
	return fmt.Errorf("SSH profile %q does not exist", args[0])
}

func cliSSHConnectCommand(args []string) error {
	if cliSubcommandHelp(args) || len(args) > 1 {
		return errors.New("usage: flclash ssh connect [NAME]")
	}
	name := firstCLIArgument(args)
	resolved, err := resolveCLISSHConnectName(name)
	if err != nil {
		return err
	}
	state, alreadyConnected, err := connectCLISSHProfile(resolved)
	if err != nil {
		state, err = retryCLISSHConnectWithPassphrase(resolved, err)
		alreadyConnected = false
	}
	if err != nil {
		return err
	}
	if alreadyConnected {
		fmt.Printf("SSH %s already connected · SOCKS5 127.0.0.1:%d\n", state.Name, state.Port)
		return nil
	}
	if state.Kind == cliSSHAttachedKind {
		fmt.Printf("SSH %s attached · SOCKS5 127.0.0.1:%d\n", state.Name, state.Port)
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
	state, latency, err := testCLISSHProfile(firstCLIArgument(args))
	if err != nil {
		return err
	}
	fmt.Printf("SSH %s ready · SOCKS5 127.0.0.1:%d · handshake %s\n", state.Name, state.Port, latency)
	return nil
}

func cliSSHDefaultCommand(args []string) error {
	if cliSubcommandHelp(args) || len(args) > 1 {
		return errors.New("usage: flclash ssh default [NAME|--clear]")
	}
	if len(args) == 0 {
		config, err := loadCLISSHConfig()
		if err != nil {
			return err
		}
		if strings.TrimSpace(config.Default) == "" {
			fmt.Println("No default SSH profile")
			return nil
		}
		fmt.Printf("Default SSH profile %s\n", config.Default)
		return nil
	}
	if args[0] == "--clear" || args[0] == "clear" {
		if err := setCLISSHDefault(""); err != nil {
			return err
		}
		fmt.Println("Default SSH profile cleared")
		return nil
	}
	if err := setCLISSHDefault(args[0]); err != nil {
		return err
	}
	fmt.Printf("Default SSH profile %s\n", args[0])
	return nil
}

func firstCLIArgument(args []string) string {
	if len(args) == 0 {
		return ""
	}
	return args[0]
}

func cliSSHAuthenticationLabel(identity string, passphraseSet, passwordSet bool) string {
	if identity != "" {
		key := "key"
		if passphraseSet {
			key += " + passphrase ****"
		}
		if passwordSet {
			return key + " → password **** fallback/MFA"
		}
		return key + " only"
	}
	if passwordSet {
		return "password **** only"
	}
	return "agent/default key only"
}
