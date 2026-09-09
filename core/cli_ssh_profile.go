//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	cryptossh "golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

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
	jumpAssigned := arguments["--jump"]
	for argument := range arguments {
		if strings.HasPrefix(argument, "--jump=") {
			jumpAssigned = true
		}
	}
	if jumpAssigned && arguments["--clear-jump"] {
		return edit, false, errors.New("--jump and --clear-jump cannot be used together")
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
		case argument == "--jump":
			value, err := next()
			if err != nil {
				return edit, false, err
			}
			edit.Jump = value
			edit.JumpSet = true
		case strings.HasPrefix(argument, "--jump="):
			edit.Jump = strings.TrimPrefix(argument, "--jump=")
			edit.JumpSet = true
		case argument == "--clear-jump":
			edit.ClearJump = true
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
		case argument == "--user":
			value, err := next()
			if err != nil {
				return edit, false, err
			}
			edit.Username = strings.TrimSpace(value)
		case strings.HasPrefix(argument, "--user="):
			edit.Username = strings.TrimSpace(strings.TrimPrefix(argument, "--user="))
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
	if !editing && edit.ClearJump {
		return edit, false, errors.New("--clear-jump is only valid with `ssh edit`")
	}
	if editing {
		if len(positionals) < 1 || len(positionals) > 2 {
			return edit, false, errors.New("usage: flclash ssh edit NAME [HOST] [--user USER] [OPTIONS]")
		}
		edit.Name = positionals[0]
		if len(positionals) == 2 {
			parsedUser, parsedHost := splitCLISSHDestination(positionals[1])
			if edit.Username != "" && parsedUser != "" && edit.Username != parsedUser {
				return edit, false, errors.New("SSH username conflicts with user@host destination")
			}
			if edit.Username == "" {
				edit.Username = parsedUser
			}
			edit.Host = parsedHost
			edit.Destination = formatCLISSHDestination(edit.Username, edit.Host)
		}
		return edit, len(args) == 1, nil
	}
	if len(positionals) != 2 {
		return edit, false, errors.New("usage: flclash ssh add NAME HOST --user USER [OPTIONS]")
	}
	edit.Name = positionals[0]
	parsedUser, parsedHost := splitCLISSHDestination(positionals[1])
	if edit.Username != "" && parsedUser != "" && edit.Username != parsedUser {
		return edit, false, errors.New("SSH username conflicts with user@host destination")
	}
	if edit.Username == "" {
		edit.Username = parsedUser
	}
	edit.Host = parsedHost
	edit.Destination = formatCLISSHDestination(edit.Username, edit.Host)
	return edit, false, nil
}

func cliSSHProfileFromEdit(existing cliSSHProfile, edit cliSSHProfileEdit, editing bool) (cliSSHProfile, error) {
	profile := existing
	if edit.Name != "" {
		profile.Name = edit.Name
	}
	if edit.Host != "" || edit.Destination != "" {
		profile.Host = edit.Host
		if profile.Host == "" {
			_, profile.Host = splitCLISSHDestination(edit.Destination)
		}
	}
	if edit.Username != "" || !editing {
		profile.Username = edit.Username
	}
	if !editing || edit.PortSet || existing.Port == 0 {
		profile.Port = edit.Port
	}
	if !editing || edit.LocalPortSet {
		profile.LocalPort = edit.LocalPort
	}
	if edit.JumpSet || !editing {
		profile.Jump = edit.Jump
	} else if edit.ClearJump {
		profile.Jump = ""
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
	profile = normalizeCLISSHProfile(profile)
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
	profile = normalizeCLISSHProfile(profile)
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
		if strings.TrimSpace(config.Default) == "" {
			config.Default = profile.Name
		}
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
	profile = normalizeCLISSHProfile(profile)
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
		if strings.EqualFold(config.Default, originalName) {
			config.Default = profile.Name
		}
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
		if strings.EqualFold(config.Default, name) {
			config.Default = ""
		}
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
	startCLISSHRelayForOperation             = startCLISSHRelay
)

func cliSSHProfileConnected(name string) (bool, error) {
	state, active, err := activeCLIPersistentSSHTunnelForOperation()
	if err != nil {
		return false, err
	}
	return active && strings.EqualFold(state.Name, name), nil
}

func connectCLISSHProfile(name string) (cliSSHTunnelState, bool, error) {
	return connectCLISSHProfileWithCredentials(name, cliSSHCredentials{})
}

func connectCLISSHProfileWithCredentials(
	name string,
	credentials cliSSHCredentials,
) (cliSSHTunnelState, bool, error) {
	lock, err := lockCLISSHTunnelOperation()
	if err != nil {
		return cliSSHTunnelState{}, false, err
	}
	defer lock.release()
	profile, err := loadCLISSHProfile(name)
	if err != nil {
		_ = saveCLISSHLastError(name, err.Error())
		return cliSSHTunnelState{}, false, err
	}
	profile, err = prepareCLISSHProfileCredentials(profile, credentials)
	if err != nil {
		_ = saveCLISSHLastError(profile.Name, err.Error())
		return cliSSHTunnelState{}, false, err
	}
	old, oldActive, err := activeCLIPersistentSSHTunnelForOperation()
	if err != nil {
		return cliSSHTunnelState{}, false, err
	}
	if oldActive && strings.EqualFold(old.Name, profile.Name) {
		if cliSSHTunnelReady(old) {
			_ = clearCLISSHLastError(profile.Name)
			return old, true, nil
		}
		if err := stopCLIStateTunnelForOperation(old); err != nil {
			_ = saveCLISSHLastError(old.Name, err.Error())
			return cliSSHTunnelState{}, false,
				fmt.Errorf("stop broken SSH tunnel %q: %w", old.Name, err)
		}
		state, err := startCLIPersistentSSHTunnelForOperation(profile)
		if err != nil {
			_ = saveCLISSHLastError(profile.Name, err.Error())
			return cliSSHTunnelState{}, false,
				fmt.Errorf("restart broken SSH tunnel %q: %w", profile.Name, err)
		}
		_ = clearCLISSHLastError(profile.Name)
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
		_ = clearCLISSHLastError(profile.Name)
		return state, false, nil
	}
	_ = saveCLISSHLastError(profile.Name, err.Error())
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
	resolved, err := resolveCLISSHConnectName(name)
	if err != nil {
		return cliSSHTunnelState{}, 0, err
	}
	started := time.Now()
	state, err := ensureCLIPersistentSSHTunnel(resolved)
	if err != nil {
		state, err = retryCLISSHConnectWithPassphrase(resolved, err)
	}
	if err != nil {
		return cliSSHTunnelState{}, 0, err
	}
	if err := probeCLISSHSOCKS(state.Port, 2*time.Second); err != nil {
		return state, 0, fmt.Errorf("SSH SOCKS5 handshake failed: %w", err)
	}
	return state, time.Since(started).Round(time.Millisecond), nil
}

func setCLISSHDefault(name string) error {
	name = strings.TrimSpace(name)
	return updateCLISSHConfig(func(config *cliSSHConfig) error {
		if name == "" {
			config.Default = ""
			return nil
		}
		if _, found := findCLISSHProfile(config.Profiles, name); !found {
			return fmt.Errorf("SSH profile %q does not exist", name)
		}
		config.Default = name
		return nil
	})
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
	edit.Username, err = promptCLISSHLine(reader, "SSH username", existing.Username, false)
	if err != nil {
		return edit, err
	}
	edit.Host, err = promptCLISSHLine(reader, "SSH host or alias", existing.Host, false)
	if err != nil {
		return edit, err
	}
	edit.Destination = formatCLISSHDestination(edit.Username, edit.Host)
	edit.Jump, err = promptCLISSHLine(reader, "Jump host (optional)", existing.Jump, true)
	if err != nil {
		return edit, err
	}
	edit.JumpSet = true
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

func promptCLISSHSecretOnce(label string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", fmt.Errorf("%s requires an interactive terminal", label)
	}
	fmt.Fprintf(os.Stderr, "%s: ", label)
	value, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if len(value) == 0 {
		return "", fmt.Errorf("%s must not be empty", strings.ToLower(label))
	}
	return string(value), nil
}

func validateCLISSHProfile(profile cliSSHProfile) error {
	profile = normalizeCLISSHProfile(profile)
	if err := validateCLISSHProfileStructure(profile); err != nil {
		return err
	}
	if profile.Username == "" {
		return errors.New("SSH username is required")
	}
	if profile.Identity == "" && profile.IdentityPassphrase != "" {
		return errors.New("private key passphrase requires an Identity(private key)")
	}
	if profile.Jump != "" &&
		(cliSSHOptionConfigured(profile.Options, "ProxyJump") ||
			cliSSHOptionConfigured(profile.Options, "JumpHost")) {
		return errors.New("Jump host cannot be combined with a ProxyJump/JumpHost option")
	}
	for _, option := range profile.Options {
		if err := validateCLISSHOption(option); err != nil {
			return err
		}
	}
	return nil
}

func validateCLISSHProfileStructure(profile cliSSHProfile) error {
	profile = normalizeCLISSHProfile(profile)
	if profile.Name == "" || len(profile.Name) > 64 {
		return errors.New("SSH profile name must contain 1 to 64 characters")
	}
	for _, value := range profile.Name {
		if !((value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') || strings.ContainsRune("._-", value)) {
			return errors.New("SSH profile name may contain only letters, numbers, dot, underscore, and hyphen")
		}
	}
	if profile.Username != "" && (strings.HasPrefix(profile.Username, "-") ||
		strings.ContainsAny(profile.Username, "@ \t\r\n\x00")) {
		return errors.New("SSH username is invalid")
	}
	if profile.Host == "" || strings.HasPrefix(profile.Host, "-") ||
		strings.ContainsAny(profile.Host, "@ \t\r\n\x00") {
		return errors.New("SSH host is invalid")
	}
	if profile.Port < 1 || profile.Port > 65535 {
		return errors.New("SSH port must be between 1 and 65535")
	}
	if profile.LocalPort < 0 || profile.LocalPort > 65535 {
		return errors.New("local SOCKS5 port must be auto or between 1 and 65535")
	}
	if err := validateCLISSHJump(profile.Jump); err != nil {
		return err
	}
	for _, option := range profile.Options {
		if err := validateCLISSHOptionSyntax(option); err != nil {
			return err
		}
	}
	return nil
}

func expandCLISSHIdentityPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			if path == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(path, "~/"))
		}
	}
	return path
}

func inspectCLISSHIdentity(
	path,
	passphrase string,
) (cliSSHIdentityKind, error) {
	if strings.TrimSpace(path) == "" {
		return cliSSHIdentityNone, nil
	}
	path = expandCLISSHIdentityPath(path)
	info, err := os.Stat(path)
	if err != nil {
		return cliSSHIdentityNone, fmt.Errorf("inspect SSH private key %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return cliSSHIdentityNone, fmt.Errorf("SSH private key %q must be a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return cliSSHIdentityNone, fmt.Errorf(
			"SSH private key %q permissions are too open (%04o); copy it into ~/.ssh and run chmod 600",
			path,
			info.Mode().Perm(),
		)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return cliSSHIdentityNone, fmt.Errorf("read SSH private key %q: %w", path, err)
	}
	if _, err := cryptossh.ParseRawPrivateKey(data); err == nil {
		return cliSSHIdentityUnencrypted, nil
	} else {
		var missing *cryptossh.PassphraseMissingError
		if !errors.As(err, &missing) {
			return cliSSHIdentityNone, fmt.Errorf("parse SSH private key %q: %w", path, err)
		}
	}
	if passphrase == "" {
		return cliSSHIdentityEncrypted, nil
	}
	if _, err := cryptossh.ParseRawPrivateKeyWithPassphrase(
		data,
		[]byte(passphrase),
	); err != nil {
		return cliSSHIdentityEncrypted,
			fmt.Errorf("private key passphrase for %q is incorrect: %w", path, err)
	}
	return cliSSHIdentityEncrypted, nil
}

func prepareCLISSHProfileCredentials(
	profile cliSSHProfile,
	credentials cliSSHCredentials,
) (cliSSHProfile, error) {
	profile = normalizeCLISSHProfile(profile)
	if err := validateCLISSHProfile(profile); err != nil {
		return profile, err
	}
	if profile.Identity == "" {
		if profile.IdentityPassphrase != "" {
			return profile, errors.New("private key passphrase requires an Identity(private key)")
		}
		return profile, nil
	}
	passphrase := profile.IdentityPassphrase
	if credentials.IdentityPassphrase != "" {
		passphrase = credentials.IdentityPassphrase
	}
	kind, err := inspectCLISSHIdentity(profile.Identity, passphrase)
	if err != nil {
		return profile, err
	}
	switch kind {
	case cliSSHIdentityUnencrypted:
		profile.IdentityPassphrase = ""
	case cliSSHIdentityEncrypted:
		if passphrase == "" {
			return profile, &cliSSHCredentialRequiredError{
				Profile:  profile.Name,
				Identity: expandCLISSHIdentityPath(profile.Identity),
			}
		}
		profile.IdentityPassphrase = passphrase
	}
	return profile, nil
}

func validateCLISSHOption(option string) error {
	if err := validateCLISSHOptionSyntax(option); err != nil {
		return err
	}
	key, value, found := strings.Cut(option, "=")
	_ = value
	_ = found
	disallowed := map[string]bool{
		"batchmode": true, "clearallforwardings": true, "controlmaster": true,
		"controlpath": true, "controlpersist": true, "dynamicforward": true,
		"exitonforwardfailure": true, "forkafterauthentication": true,
		"identitiesonly": true, "identityfile": true,
		"kbdinteractiveauthentication": true, "localcommand": true,
		"numberofpasswordprompts": true, "passwordauthentication": true,
		"permitlocalcommand": true, "preferredauthentications": true,
		"pubkeyauthentication": true, "remotecommand": true,
		"sessiontype": true, "user": true,
	}
	if disallowed[strings.ToLower(strings.TrimSpace(key))] {
		return fmt.Errorf("SSH option %q conflicts with FlClash tunnel management", key)
	}
	return nil
}

func validateCLISSHJump(jump string) error {
	jump = strings.TrimSpace(jump)
	if jump == "" {
		return nil
	}
	if strings.HasPrefix(jump, "-") || strings.ContainsAny(jump, " \t\r\n\x00") {
		return errors.New("SSH jump host is invalid")
	}
	return nil
}

func validateCLISSHOptionSyntax(option string) error {
	key, value, found := strings.Cut(option, "=")
	if !found || strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" ||
		strings.ContainsAny(option, "\r\n\x00") {
		return fmt.Errorf("SSH option %q must use KEY=VALUE", option)
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
	if config.Version != 1 && config.Version != cliSSHConfigVersion {
		return config, fmt.Errorf("unsupported SSH configuration version %d", config.Version)
	}
	for index, profile := range config.Profiles {
		profile = normalizeCLISSHProfile(profile)
		config.Profiles[index] = profile
		if err := validateCLISSHProfileStructure(profile); err != nil {
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
	lastError, _ := loadCLISSHLastError()
	views := make([]cliSSHProfileView, 0, len(config.Profiles))
	for _, profile := range config.Profiles {
		profile = normalizeCLISSHProfile(profile)
		view := cliSSHProfileView{
			Name:          profile.Name,
			Username:      profile.Username,
			Host:          profile.Host,
			Destination:   profile.Destination,
			Port:          profile.Port,
			LocalPort:     profile.LocalPort,
			Jump:          profile.Jump,
			Identity:      profile.Identity,
			Options:       append([]string(nil), profile.Options...),
			PassphraseSet: profile.IdentityPassphrase != "",
			PasswordSet:   profile.Password != "",
			NeedsUsername: profile.Username == "",
			Default:       strings.EqualFold(config.Default, profile.Name),
		}
		if connected && strings.EqualFold(active.Name, profile.Name) {
			view.Connected = true
			view.Attached = active.Kind == cliSSHAttachedKind
			view.Ready = cliSSHTunnelReady(active)
			view.SocksPort = active.Port
			view.StartedAt = active.StartedAt
		}
		if strings.EqualFold(lastError.Name, profile.Name) {
			view.LastError = lastError.Error
		}
		views = append(views, view)
	}
	return views, nil
}
