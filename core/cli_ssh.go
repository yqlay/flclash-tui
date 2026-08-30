//go:build linux && !cgo && cli

package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const cliSSHMasterReadyTimeout = 2 * time.Minute

type cliSSHOptions struct {
	RemotePort  int
	SSHArgs     []string
	Destination string
	Command     []string
}

type cliExitCodeError struct {
	code int
}

func (e *cliExitCodeError) Error() string {
	return ""
}

func (e *cliExitCodeError) ExitCode() int {
	return e.code
}

func flcSSHCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		printFLCSSHUsage(os.Stdout)
		return nil
	}
	options, err := parseFLCSSHOptions(args)
	if err != nil {
		return err
	}
	proxyAddress, err := activeCLIProxyURL()
	if err != nil {
		return err
	}
	proxyURL, err := url.Parse(proxyAddress)
	if err != nil || proxyURL.Scheme == "" || proxyURL.Hostname() == "" ||
		proxyURL.Port() == "" {
		return errors.New("FlClash returned an invalid command proxy URL")
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return errors.New("OpenSSH client `ssh` is required for `flc ssh`")
	}
	return runFLCSSH(sshPath, options, proxyURL)
}

func printFLCSSHUsage(w io.Writer) {
	fmt.Fprintln(w, "Use B through A's active FlClash proxy over an SSH reverse tunnel.")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  flc ssh [--remote-port auto|PORT] [SSH_OPTIONS...] DESTINATION")
	fmt.Fprintln(w, "  flc ssh [--remote-port auto|PORT] [SSH_OPTIONS...] DESTINATION -- COMMAND [ARG...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "The SSH connection itself is direct. Programs on B must honor proxy environment variables.")
}

func parseFLCSSHOptions(args []string) (cliSSHOptions, error) {
	options := cliSSHOptions{}
	separator := len(args)
	for index, argument := range args {
		if argument == "--" {
			separator = index
			options.Command = append([]string(nil), args[index+1:]...)
			break
		}
	}
	connectionArgs := append([]string(nil), args[:separator]...)
	filtered := make([]string, 0, len(connectionArgs))
	for index := 0; index < len(connectionArgs); index++ {
		argument := connectionArgs[index]
		switch {
		case argument == "--remote-port":
			if index+1 >= len(connectionArgs) {
				return options, errors.New("--remote-port requires auto or a port number")
			}
			index++
			port, err := parseFLCSSHRemotePort(connectionArgs[index])
			if err != nil {
				return options, err
			}
			options.RemotePort = port
		case strings.HasPrefix(argument, "--remote-port="):
			port, err := parseFLCSSHRemotePort(strings.TrimPrefix(argument, "--remote-port="))
			if err != nil {
				return options, err
			}
			options.RemotePort = port
		default:
			filtered = append(filtered, argument)
		}
	}
	if len(filtered) == 0 {
		return options, errors.New("flc ssh requires an SSH destination")
	}
	options.Destination = filtered[len(filtered)-1]
	if strings.HasPrefix(options.Destination, "-") {
		return options, errors.New("flc ssh requires the destination as the final argument before --")
	}
	options.SSHArgs = append([]string(nil), filtered[:len(filtered)-1]...)
	if err := validateFLCSSHArguments(options.SSHArgs); err != nil {
		return options, err
	}
	if separator < len(args) && len(options.Command) == 0 {
		return options, errors.New("flc ssh requires a command after --")
	}
	return options, nil
}

func parseFLCSSHRemotePort(value string) (int, error) {
	if value == "auto" {
		return 0, nil
	}
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("invalid SSH remote port %q", value)
	}
	return port, nil
}

func validateFLCSSHArguments(args []string) error {
	disallowedShort := "MSORLNDfWtTnsGQVg"
	disallowedOptions := map[string]bool{
		"clearallforwardings":     true,
		"controlmaster":           true,
		"controlpath":             true,
		"controlpersist":          true,
		"exitonforwardfailure":    true,
		"forkafterauthentication": true,
		"localcommand":            true,
		"permitlocalcommand":      true,
		"remotecommand":           true,
		"sessiontype":             true,
		"stdioforwarding":         true,
	}
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if argument == "-o" {
			if index+1 >= len(args) {
				return errors.New("SSH option -o requires a value")
			}
			index++
			if disallowedFLCSSHOption(args[index], disallowedOptions) {
				return fmt.Errorf("SSH option %q conflicts with flc ssh tunnel management", args[index])
			}
			continue
		}
		if strings.HasPrefix(argument, "-o") && len(argument) > 2 {
			if disallowedFLCSSHOption(argument[2:], disallowedOptions) {
				return fmt.Errorf("SSH option %q conflicts with flc ssh tunnel management", argument)
			}
			continue
		}
		if strings.HasPrefix(argument, "-") && !strings.HasPrefix(argument, "--") {
			for _, flag := range argument[1:] {
				if strings.ContainsRune(disallowedShort, flag) {
					return fmt.Errorf("SSH option -%c conflicts with flc ssh tunnel management", flag)
				}
			}
		}
	}
	return nil
}

func disallowedFLCSSHOption(value string, disallowed map[string]bool) bool {
	key, _, _ := strings.Cut(value, "=")
	return disallowed[strings.ToLower(strings.TrimSpace(key))]
}

func runFLCSSH(sshPath string, options cliSSHOptions, localProxy *url.URL) error {
	temporaryDirectory, err := os.MkdirTemp("", "flclash-ssh-")
	if err != nil {
		return fmt.Errorf("create SSH control directory: %w", err)
	}
	defer os.RemoveAll(temporaryDirectory)
	controlPath := filepath.Join(temporaryDirectory, "control")

	masterArgs := append([]string(nil), options.SSHArgs...)
	masterArgs = append(masterArgs,
		"-M", "-S", controlPath,
		"-o", "ControlMaster=yes",
		"-o", "ControlPersist=no",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "ClearAllForwardings=yes",
		"-o", "RemoteCommand=none",
		"-N", options.Destination,
	)
	master := exec.Command(sshPath, masterArgs...)
	master.Stdin = os.Stdin
	master.Stdout = os.Stdout
	master.Stderr = os.Stderr
	master.SysProcAttr = cliSSHProcessAttributes()
	if err := master.Start(); err != nil {
		return fmt.Errorf("start SSH control connection: %w", err)
	}
	masterDone := make(chan error, 1)
	go func() {
		masterDone <- master.Wait()
	}()
	defer cleanupFLCSSHMaster(sshPath, controlPath, options, master, masterDone)
	interrupt := make(chan os.Signal, 2)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(interrupt)
	if err := waitForFLCSSHMaster(
		sshPath,
		controlPath,
		options.Destination,
		master,
		masterDone,
		interrupt,
	); err != nil {
		return err
	}

	requestedPort := strconv.Itoa(options.RemotePort)
	forwardSpec := net.JoinHostPort("127.0.0.1", requestedPort) + ":" +
		net.JoinHostPort(localProxy.Hostname(), localProxy.Port())
	forwardArgs := []string{
		"-S", controlPath,
		"-o", "ClearAllForwardings=no",
		"-O", "forward",
		"-R", forwardSpec,
		options.Destination,
	}
	forward := exec.Command(sshPath, forwardArgs...)
	forward.SysProcAttr = cliSSHProcessAttributes()
	var forwardOutput bytes.Buffer
	var forwardError bytes.Buffer
	forward.Stdout = &forwardOutput
	forward.Stderr = &forwardError
	if err := runFLCSSHControlCommand(forward, interrupt); err != nil {
		var exitError *cliExitCodeError
		if errors.As(err, &exitError) {
			return err
		}
		message := strings.TrimSpace(forwardError.String())
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("SSH reverse forwarding failed: %s", message)
	}
	remotePort := options.RemotePort
	if remotePort == 0 {
		remotePort, err = parseAllocatedFLCSSHPort(forwardOutput.String())
		if err != nil {
			return err
		}
	}
	allocatedSpec := net.JoinHostPort("127.0.0.1", strconv.Itoa(remotePort)) + ":" +
		net.JoinHostPort(localProxy.Hostname(), localProxy.Port())
	defer cancelFLCSSHForward(sshPath, controlPath, options.Destination, allocatedSpec)

	remoteProxy := *localProxy
	remoteProxy.Host = net.JoinHostPort("127.0.0.1", strconv.Itoa(remotePort))
	fmt.Printf("FLC SSH tunnel ready · remote 127.0.0.1:%d\n", remotePort)
	return runFLCSSHSession(
		sshPath,
		controlPath,
		options,
		remoteProxy.String(),
		interrupt,
	)
}

func parseAllocatedFLCSSHPort(output string) (int, error) {
	fields := strings.Fields(output)
	for _, field := range fields {
		port, err := strconv.Atoi(field)
		if err == nil && port > 0 && port <= 65535 {
			return port, nil
		}
	}
	return 0, fmt.Errorf("OpenSSH did not report the allocated remote port: %q", strings.TrimSpace(output))
}

func waitForFLCSSHMaster(
	sshPath,
	controlPath,
	destination string,
	master *exec.Cmd,
	masterDone <-chan error,
	interrupt <-chan os.Signal,
) error {
	deadline := time.Now().Add(cliSSHMasterReadyTimeout)
	for time.Now().Before(deadline) {
		select {
		case err := <-masterDone:
			if err == nil {
				return errors.New("SSH control connection exited before becoming ready")
			}
			return fmt.Errorf("SSH control connection failed: %w", err)
		case received := <-interrupt:
			_ = master.Process.Signal(received)
			if received == syscall.SIGINT {
				return &cliExitCodeError{code: 130}
			}
			return &cliExitCodeError{code: 143}
		default:
		}
		if _, err := os.Stat(controlPath); err == nil {
			check := exec.Command(
				sshPath,
				"-S", controlPath,
				"-O", "check",
				destination,
			)
			if check.Run() == nil {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("timed out waiting for the SSH control connection")
}

func runFLCSSHSession(
	sshPath,
	controlPath string,
	options cliSSHOptions,
	proxyAddress string,
	interrupt <-chan os.Signal,
) error {
	bootstrap := flcSSHRemoteBootstrap(proxyAddress, options.Command)
	args := append([]string(nil), options.SSHArgs...)
	args = append(
		args,
		"-S", controlPath,
		"-o", "ClearAllForwardings=yes",
		"-o", "RemoteCommand=none",
	)
	if len(options.Command) == 0 {
		args = append(args, "-t")
	} else {
		args = append(args, "-T")
	}
	args = append(args, options.Destination, bootstrap)
	command := exec.Command(sshPath, args...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	command.SysProcAttr = cliSSHProcessAttributes()
	if err := command.Start(); err != nil {
		return fmt.Errorf("start SSH proxy session: %w", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	select {
	case err := <-done:
		return flcSSHSessionResult(err)
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
}

func runFLCSSHControlCommand(
	command *exec.Cmd,
	interrupt <-chan os.Signal,
) error {
	if err := command.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	select {
	case err := <-done:
		return err
	case received := <-interrupt:
		_ = command.Process.Signal(received)
		select {
		case <-done:
		case <-time.After(cliExitKillPeriod):
			_ = command.Process.Kill()
			<-done
		}
		if received == syscall.SIGINT {
			return &cliExitCodeError{code: 130}
		}
		return &cliExitCodeError{code: 143}
	}
}

func flcSSHSessionResult(err error) error {
	if err == nil {
		return nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return &cliExitCodeError{code: exitError.ExitCode()}
	}
	return err
}

func flcSSHRemoteBootstrap(proxyAddress string, command []string) string {
	quotedProxy := quotePOSIXShell(proxyAddress)
	keys := []string{
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "all_proxy",
	}
	assignments := make([]string, 0, len(keys))
	for _, key := range keys {
		assignments = append(assignments, key+"="+quotedProxy)
	}
	bootstrap := "export " + strings.Join(assignments, " ") + "; exec "
	if len(command) == 0 {
		return bootstrap + "\"${SHELL:-/bin/sh}\" -l"
	}
	quotedCommand := make([]string, 0, len(command))
	for _, argument := range command {
		quotedCommand = append(quotedCommand, quotePOSIXShell(argument))
	}
	return bootstrap + strings.Join(quotedCommand, " ")
}

func quotePOSIXShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func cleanupFLCSSHMaster(
	sshPath,
	controlPath string,
	options cliSSHOptions,
	master *exec.Cmd,
	masterDone <-chan error,
) {
	if master.ProcessState != nil {
		return
	}
	exit := exec.Command(
		sshPath,
		"-S", controlPath,
		"-O", "exit",
		options.Destination,
	)
	_ = exit.Run()
	select {
	case <-masterDone:
		return
	case <-time.After(cliExitTerminatePeriod):
	}
	if master.ProcessState != nil {
		return
	}
	_ = master.Process.Signal(syscall.SIGTERM)
	select {
	case <-masterDone:
		return
	case <-time.After(cliExitKillPeriod):
	}
	if master.ProcessState != nil {
		return
	}
	_ = master.Process.Kill()
	<-masterDone
}

func cancelFLCSSHForward(
	sshPath,
	controlPath,
	destination,
	forwardSpec string,
) {
	cancel := exec.Command(
		sshPath,
		"-S", controlPath,
		"-O", "cancel",
		"-R", forwardSpec,
		destination,
	)
	_ = cancel.Run()
}

func cliSSHProcessAttributes() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		Pdeathsig: syscall.SIGTERM,
	}
}
