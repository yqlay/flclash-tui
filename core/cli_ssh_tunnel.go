//go:build linux && !cgo && cli

package main

import (
	"context"
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
	"strconv"
	"strings"
	"syscall"
	"time"
)

func runCLISSHCommand(
	command *exec.Cmd,
	event string,
	logSuccessOutput bool,
) error {
	var stdout, stderr cliSSHCappedBuffer
	prepareCLISSHNonInteractiveCommand(command)
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	output := strings.TrimSpace(strings.Join(
		[]string{stdout.String(), stderr.String()},
		"\n",
	))
	if output != "" && (err != nil || logSuccessOutput) {
		level := "WARN"
		if err != nil {
			level = "ERROR"
		}
		if paths, pathErr := resolvePaths("", ""); pathErr == nil {
			appendCLIApplicationLog(paths.HomeDir, level, event, output)
		} else {
			appendTUILogEvent(level, event+" · "+output)
		}
	}
	if err == nil {
		return nil
	}
	summary := cliSSHOutputSummary(stderr.String())
	if summary == "" {
		summary = cliSSHOutputSummary(stdout.String())
	}
	if summary == "" {
		return err
	}
	return fmt.Errorf("%w: %s", err, formatCLISSHErrorSummary(summary))
}

func formatCLISSHErrorSummary(summary string) string {
	friendly := cliSSHFriendlyError(summary)
	if friendly == summary {
		return summary
	}
	return friendly + " · " + summary
}

func runCLISSHProbe(command *exec.Cmd) error {
	var stdout, stderr cliSSHCappedBuffer
	prepareCLISSHNonInteractiveCommand(command)
	command.Stdout = &stdout
	command.Stderr = &stderr
	return command.Run()
}

// prepareCLISSHNonInteractiveCommand makes every OpenSSH helper incapable of
// taking over the terminal. Authentication is either supplied through our
// private askpass file during initial connect or rejected by the control-only
// arguments below; a TUI must never be obscured by an OpenSSH prompt.
func prepareCLISSHNonInteractiveCommand(command *exec.Cmd) {
	command.Stdin = nil
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
}

func probeCLISSHRemote(state cliSSHTunnelState) (cliSSHRemoteProbe, error) {
	if strings.TrimSpace(state.ControlPath) == "" || strings.TrimSpace(state.Destination) == "" {
		return cliSSHRemoteProbe{}, errors.New("SSH control connection is unavailable")
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return cliSSHRemoteProbe{}, errors.New("OpenSSH client `ssh` is required")
	}
	arguments := cliSSHControlClientArguments(state)
	arguments = append(
		arguments,
		state.Destination,
		"flclash", "ssh", "probe", "--json",
	)
	command := exec.Command(sshPath, arguments...)
	var stdout, stderr cliSSHCappedBuffer
	prepareCLISSHNonInteractiveCommand(command)
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		summary := cliSSHOutputSummary(stderr.String())
		if summary == "" {
			summary = cliSSHOutputSummary(stdout.String())
		}
		if summary != "" {
			return cliSSHRemoteProbe{}, fmt.Errorf("run remote FlClash probe: %w: %s", err, formatCLISSHErrorSummary(summary))
		}
		return cliSSHRemoteProbe{}, fmt.Errorf("run remote FlClash probe: %w", err)
	}
	var probe cliSSHRemoteProbe
	if err := json.Unmarshal([]byte(stdout.String()), &probe); err != nil {
		return cliSSHRemoteProbe{}, fmt.Errorf("parse remote FlClash probe: %w", err)
	}
	return probe, nil
}

func cliSSHOutputSummary(output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	for index := len(lines) - 1; index >= 0; index-- {
		line := strings.Join(strings.Fields(lines[index]), " ")
		if line == "" || strings.Contains(line, "post-quantum key exchange") ||
			strings.Contains(line, "store now, decrypt later") ||
			strings.Contains(line, "openssh.com/pq.html") {
			continue
		}
		return truncateTUI(line, 320)
	}
	return ""
}

func cliSSHFriendlyError(summary string) string {
	lower := strings.ToLower(summary)
	switch {
	case strings.Contains(lower, "permission denied"):
		return "authentication failed; check username, private key, passphrase, or password"
	case strings.Contains(lower, "connection timed out") || strings.Contains(lower, "operation timed out"):
		return "connection timed out; check the SSH host, port, and network path"
	case strings.Contains(lower, "connection refused"):
		return "connection refused; the SSH server is not accepting connections on that port"
	case strings.Contains(lower, "no route to host"):
		return "no route to the SSH host; check the destination and local network"
	case strings.Contains(lower, "network is unreachable"):
		return "network is unreachable; check the destination and local network"
	case strings.Contains(lower, "could not resolve hostname") || strings.Contains(lower, "name or service not known"):
		return "SSH host name could not be resolved"
	case strings.Contains(lower, "host key verification failed"):
		return "SSH host key changed or is not trusted; inspect known_hosts before retrying"
	case strings.Contains(lower, "too many authentication"):
		return "too many authentication failures; specify Identity(private key) or a saved password"
	case strings.Contains(lower, "identity file") && strings.Contains(lower, "not accessible"):
		return "private key is not accessible; copy it out of /mnt/c and chmod 600"
	case strings.Contains(lower, "bad permissions"):
		return "private key permissions are too open; copy it to ~/.ssh and chmod 600"
	case strings.Contains(lower, "connection reset"):
		return "SSH connection reset by the remote host"
	default:
		return summary
	}
}

func cliSSHMaskedSecret(set bool) string {
	if set {
		return "********"
	}
	return "not saved"
}

func startCLISSHTunnel(profile cliSSHProfile, kind string) (cliSSHTunnelState, error) {
	var err error
	profile, err = prepareCLISSHProfileCredentials(profile, cliSSHCredentials{})
	if err != nil {
		return cliSSHTunnelState{}, err
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return cliSSHTunnelState{}, errors.New("OpenSSH client `ssh` is required")
	}
	runtimeDirectory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		return cliSSHTunnelState{}, err
	}
	socketDirectory, err := ensureCLISSHSocketDirectory(runtimeDirectory)
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
		controlPath := filepath.Join(socketDirectory, fmt.Sprintf("ctl-%x.sock", digest[:8]))
		upstreamPort := port
		if kind == "persistent" {
			upstreamPort, err = allocateCLISSHPort()
			if err != nil {
				return cliSSHTunnelState{}, err
			}
			for upstreamPort == port {
				upstreamPort, err = allocateCLISSHPort()
				if err != nil {
					return cliSSHTunnelState{}, err
				}
			}
		}
		state := cliSSHTunnelState{
			Name:         profile.Name,
			Destination:  formatCLISSHDestination(profile.Username, profile.Host),
			Port:         port,
			UpstreamPort: upstreamPort,
			ControlPath:  controlPath,
			Kind:         kind,
			StartedAt:    time.Now(),
		}
		if kind != "persistent" {
			state.UpstreamPort = 0
		}
		args := cliSSHTunnelArguments(profile, controlPath)
		args = append(args, profile.Host)
		command := exec.Command(sshPath, args...)
		cleanup, err := configureCLISSHAskpass(
			command,
			profile.IdentityPassphrase,
			profile.Password,
		)
		if err != nil {
			return state, err
		}
		runErr := runCLISSHCommand(command, "ssh_connect", true)
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
		if kind == "persistent" {
			deadline := time.Now().Add(5 * time.Second)
			for !cliSSHSOCKSReady(state.UpstreamPort) && time.Now().Before(deadline) {
				time.Sleep(25 * time.Millisecond)
			}
			if !cliSSHSOCKSReady(state.UpstreamPort) {
				_ = stopCLIStateTunnel(state)
				return state, fmt.Errorf("SSH tunnel %q upstream did not become ready", profile.Name)
			}
			if relayErr := startCLISSHRelayForOperation(&state); relayErr != nil {
				_ = stopCLIStateTunnel(state)
				return state, fmt.Errorf("start SSH traffic meter: %w", relayErr)
			}
			if err := saveCLISSHTunnelState(state); err != nil {
				_ = stopCLIStateTunnel(state)
				return state, err
			}
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
		"-l", profile.Username,
		"-o", "ControlMaster=yes",
		"-o", "ControlPersist=no",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ClearAllForwardings=yes",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=3",
	}
	if !cliSSHOptionConfigured(profile.Options, "ConnectTimeout") {
		args = append(args, "-o", "ConnectTimeout=15")
	}
	if !cliSSHOptionConfigured(profile.Options, "ConnectionAttempts") {
		args = append(args, "-o", "ConnectionAttempts=1")
	}
	if profile.IdentityPassphrase == "" && profile.Password == "" {
		args = append(args, "-o", "BatchMode=yes")
	}
	switch {
	case profile.Identity != "" && profile.Password != "":
		args = append(args,
			"-o", "IdentitiesOnly=yes",
			"-o", "PreferredAuthentications=publickey,keyboard-interactive,password",
			"-o", "NumberOfPasswordPrompts=1",
		)
	case profile.Identity != "":
		args = append(args,
			"-o", "IdentitiesOnly=yes",
			"-o", "PreferredAuthentications=publickey",
			"-o", "PasswordAuthentication=no",
			"-o", "KbdInteractiveAuthentication=no",
		)
	case profile.Password != "":
		args = append(args,
			"-o", "PubkeyAuthentication=no",
			"-o", "PreferredAuthentications=keyboard-interactive,password",
			"-o", "NumberOfPasswordPrompts=1",
		)
	default:
		args = append(args,
			"-o", "PreferredAuthentications=publickey",
			"-o", "PasswordAuthentication=no",
			"-o", "KbdInteractiveAuthentication=no",
		)
	}
	if !cliSSHOptionConfigured(profile.Options, "StrictHostKeyChecking") {
		args = append(args, "-o", "StrictHostKeyChecking=accept-new")
	}
	if profile.Identity != "" {
		args = append(args, "-i", profile.Identity)
	}
	if profile.Jump != "" &&
		!cliSSHOptionConfigured(profile.Options, "ProxyJump") &&
		!cliSSHOptionConfigured(profile.Options, "JumpHost") {
		args = append(args, "-o", "ProxyJump="+profile.Jump)
	}
	for _, option := range profile.Options {
		args = append(args, "-o", option)
	}
	return args
}

func addCLISSHDynamicForward(sshPath string, state cliSSHTunnelState) error {
	command := exec.Command(sshPath, cliSSHDynamicForwardArguments(state)...)
	return runCLISSHCommand(command, "ssh_forward", true)
}

func cliSSHDynamicForwardArguments(state cliSSHTunnelState) []string {
	arguments := cliSSHControlClientArguments(state)
	arguments = append(arguments,
		"-O", "forward",
		"-D", net.JoinHostPort("127.0.0.1", strconv.Itoa(cliSSHUpstreamPort(state))),
	)
	return append(arguments, state.Destination)
}

// cliSSHControlClientArguments is used only after an authenticated master has
// been created. If its socket has disappeared, OpenSSH must fail locally
// rather than fall back to a new network connection and prompt on /dev/tty.
func cliSSHControlClientArguments(state cliSSHTunnelState) []string {
	return []string{
		"-S", state.ControlPath,
		// A missing master must never fall back to a fresh network connection.
		"-o", "ProxyCommand=false",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=yes",
		"-o", "PubkeyAuthentication=no",
		"-o", "PasswordAuthentication=no",
		"-o", "KbdInteractiveAuthentication=no",
		"-o", "PreferredAuthentications=none",
	}
}

func cliSSHControlOperationArguments(
	state cliSSHTunnelState,
	operation string,
) []string {
	arguments := cliSSHControlClientArguments(state)
	arguments = append(arguments, "-O", operation)
	return append(arguments, state.Destination)
}

func cliSSHUpstreamPort(state cliSSHTunnelState) int {
	if state.UpstreamPort > 0 {
		return state.UpstreamPort
	}
	return state.Port
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
	if controlPath, ok := findCLILiveSSHMaster(profile); ok {
		state, err := attachCLISSHTunnel(profile, controlPath)
		if err != nil {
			return cliSSHTunnelState{}, fmt.Errorf(
				"reuse existing SSH connection %q: %w",
				profile.Name,
				err,
			)
		}
		return state, nil
	}
	state, err := startCLISSHTunnel(profile, "persistent")
	if err != nil {
		return cliSSHTunnelState{}, err
	}
	return persistCLISSHTunnelState(state)
}

func configuredCLISSHLocalPort(profile cliSSHProfile, kind string) int {
	if kind != "persistent" && kind != cliSSHAttachedKind {
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
	executable, err := cliSSHAskpassExecutable()
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

func ensureCLISSHSocketDirectory(runtimeDirectory string) (string, error) {
	maximumPath := len((syscall.RawSockaddrUnix{}).Path) - 1
	probePath := filepath.Join(runtimeDirectory, "ctl-0000000000000000.sock")
	// OpenSSH creates a temporary control socket with an additional random
	// suffix before atomically moving it into place.
	if len(probePath)+24 <= maximumPath {
		return runtimeDirectory, nil
	}
	digest := sha256.Sum256([]byte(runtimeDirectory))
	directory := filepath.Join(
		"/tmp",
		fmt.Sprintf("flclash-ssh-%d-%x", os.Getuid(), digest[:4]),
	)
	if err := os.Mkdir(directory, 0o700); err != nil && !os.IsExist(err) {
		return "", err
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		!cliPathOwnedByCurrentUser(info) {
		return "", fmt.Errorf("unsafe SSH socket directory %q", directory)
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(directory, 0o700); err != nil {
			return "", err
		}
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
	return cliSSHTunnelReady(state)
}

func cliSSHTunnelReady(state cliSSHTunnelState) bool {
	if state.RelayControl != "" || state.RelayPID > 0 ||
		(state.UpstreamPort > 0 && (state.Kind == "persistent" || state.Kind == cliSSHAttachedKind)) {
		return cliSSHSOCKSReady(cliSSHUpstreamPort(state)) && cliSSHRelayReady(state)
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

func probeCLISSHSOCKS(port int, timeout time.Duration) error {
	if port < 1 || port > 65535 {
		return errors.New("SSH SOCKS5 port is invalid")
	}
	connection, err := net.DialTimeout(
		"tcp",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(port)),
		timeout,
	)
	if err != nil {
		return err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(timeout))
	if _, err := connection.Write([]byte{5, 1, 0}); err != nil {
		return err
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(connection, reply); err != nil {
		return err
	}
	if reply[0] != 5 {
		return errors.New("listener is not SOCKS5")
	}
	if reply[1] != 0 {
		return errors.New("SOCKS5 authentication is required")
	}
	return nil
}

func saveCLISSHLastError(name, message string) error {
	name = strings.TrimSpace(name)
	message = strings.TrimSpace(message)
	if name == "" || message == "" {
		return nil
	}
	directory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		return err
	}
	data, err := json.Marshal(cliSSHLastError{
		Name:  name,
		Error: truncateTUI(message, 320),
		At:    time.Now(),
	})
	if err != nil {
		return err
	}
	return writeCLISSHFileAtomically(
		filepath.Join(directory, cliSSHLastErrorFile),
		append(data, '\n'),
	)
}

func clearCLISSHLastError(name string) error {
	lastError, err := loadCLISSHLastError()
	if err != nil || lastError.Name == "" {
		return err
	}
	if name != "" && !strings.EqualFold(lastError.Name, name) {
		return nil
	}
	directory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(directory, cliSSHLastErrorFile)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func loadCLISSHLastError() (cliSSHLastError, error) {
	directory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		return cliSSHLastError{}, err
	}
	data, err := os.ReadFile(filepath.Join(directory, cliSSHLastErrorFile))
	if os.IsNotExist(err) {
		return cliSSHLastError{}, nil
	}
	if err != nil {
		return cliSSHLastError{}, err
	}
	var lastError cliSSHLastError
	if err := json.Unmarshal(data, &lastError); err != nil {
		return cliSSHLastError{}, err
	}
	return lastError, nil
}

func cliSSHMasterAlive(sshPath string, state cliSSHTunnelState) bool {
	if state.ControlPath == "" {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	check := exec.CommandContext(
		ctx,
		sshPath,
		cliSSHControlOperationArguments(state, "check")...,
	)
	check.WaitDelay = time.Second
	return runCLISSHProbe(check) == nil
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
	_ = stopCLISSHRelay(state)
	if cliSSHTunnelOwnsMaster(state) {
		_ = os.Remove(state.ControlPath)
	}
	_ = os.Remove(path)
	return cliSSHTunnelState{}, false, nil
}

func stopCLIStateTunnel(state cliSSHTunnelState) error {
	if !cliSSHTunnelOwnsMaster(state) {
		return stopCLIAttachedTunnel(state)
	}
	var cleanupErrors []error
	if err := stopCLISSHRelay(state); err != nil {
		cleanupErrors = append(cleanupErrors, fmt.Errorf("stop SSH traffic meter: %w", err))
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		cleanupErrors = append(cleanupErrors, errors.New("OpenSSH client `ssh` is required to stop the SSH tunnel"))
		return errors.Join(cleanupErrors...)
	}
	if cliSSHMasterAlive(sshPath, state) {
		command := exec.Command(
			sshPath,
			cliSSHControlOperationArguments(state, "exit")...,
		)
		if err := runCLISSHCommand(command, "ssh_disconnect", false); err != nil &&
			cliSSHMasterAlive(sshPath, state) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("stop SSH tunnel %q: %w", state.Name, err))
		}
		deadline := time.Now().Add(time.Second)
		for cliSSHMasterAlive(sshPath, state) && time.Now().Before(deadline) {
			time.Sleep(25 * time.Millisecond)
		}
		if cliSSHMasterAlive(sshPath, state) {
			cleanupErrors = append(cleanupErrors, fmt.Errorf("SSH tunnel %q did not stop", state.Name))
			return errors.Join(cleanupErrors...)
		}
	}
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
		if entry.IsDir() || entry.Name() == cliSSHLastErrorFile || !strings.HasSuffix(entry.Name(), ".json") {
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
