//go:build linux && !cgo && cli

package main

import (
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFLCSSHOptions(t *testing.T) {
	options, err := parseFLCSSHOptions([]string{
		"--remote-port", "18080",
		"-p", "2222",
		"user@example.test",
		"--", "printf", "%s", "hello world",
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.RemotePort != 18080 ||
		options.Destination != "user@example.test" ||
		strings.Join(options.SSHArgs, " ") != "-p 2222" ||
		strings.Join(options.Command, "|") != "printf|%s|hello world" {
		t.Fatalf("parsed options = %#v", options)
	}
}

func TestParseFLCSSHOptionsDefaultsToAllocatedPort(t *testing.T) {
	options, err := parseFLCSSHOptions([]string{"host.example"})
	if err != nil {
		t.Fatal(err)
	}
	if options.RemotePort != 0 || options.Destination != "host.example" ||
		len(options.Command) != 0 {
		t.Fatalf("parsed options = %#v", options)
	}
}

func TestParseFLCSSHOptionsRejectsTunnelConflicts(t *testing.T) {
	tests := [][]string{
		{"-R", "9000:localhost:9000", "host"},
		{"-N", "host"},
		{"-s", "host"},
		{"-o", "ControlPath=/tmp/other", "host"},
		{"-oRemoteCommand=uname", "host"},
		{"host", "--"},
		{"--remote-port", "70000", "host"},
	}
	for _, args := range tests {
		if _, err := parseFLCSSHOptions(args); err == nil {
			t.Errorf("parseFLCSSHOptions(%q) unexpectedly succeeded", args)
		}
	}
}

func TestFLCSSHRemoteBootstrapPreservesCredentialsAndQuotesCommand(t *testing.T) {
	bootstrap := flcSSHRemoteBootstrap(
		"http://flc-user:s3cret@127.0.0.1:23456",
		[]string{"printf", "%s", "it's safe"},
	)
	for _, key := range []string{
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY",
		"http_proxy", "https_proxy", "all_proxy",
	} {
		if !strings.Contains(bootstrap, key+"=") {
			t.Fatalf("bootstrap is missing %s: %s", key, bootstrap)
		}
	}
	if !strings.Contains(bootstrap, "flc-user:s3cret@127.0.0.1:23456") ||
		!strings.Contains(bootstrap, "'it'\"'\"'s safe'") {
		t.Fatalf("unsafe or incomplete bootstrap: %s", bootstrap)
	}
}

func TestRunFLCSSHUsesAllocatedForwardAndPreservesRemoteExit(t *testing.T) {
	temporaryDirectory := t.TempDir()
	logPath := filepath.Join(temporaryDirectory, "ssh.log")
	sshPath := filepath.Join(temporaryDirectory, "ssh")
	script := `#!/bin/sh
control=""
previous=""
for argument in "$@"; do
  if [ "$previous" = "-S" ]; then control="$argument"; fi
  previous="$argument"
done
printf '%s\n' "$*" >> "$FLC_SSH_TEST_LOG"
case " $* " in
  *" -M "*)
    : > "$control"
    printf '%s\n' "$$" > "${control}.pid"
    trap 'exit 0' TERM INT
    while :; do sleep 1; done
    ;;
  *" -O check "*) exit 0 ;;
  *" -O forward "*)
    if [ "${FLC_SSH_TEST_FORWARD_FAIL:-0}" = 1 ]; then
      printf '%s\n' 'remote port forwarding failed' >&2
      exit 1
    fi
    printf '%s\n' 23456
    exit 0
    ;;
  *" -O cancel "*) exit 0 ;;
  *" -O exit "*)
    if [ -f "${control}.pid" ]; then kill "$(sed -n '1p' "${control}.pid")" 2>/dev/null || true; fi
    exit 0
    ;;
esac
exit "${FLC_SSH_TEST_EXIT:-0}"
`
	if err := os.WriteFile(sshPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLC_SSH_TEST_LOG", logPath)
	t.Setenv("FLC_SSH_TEST_EXIT", "37")
	proxyURL, err := url.Parse("http://user:password@127.0.0.1:17891")
	if err != nil {
		t.Fatal(err)
	}
	err = runFLCSSH(sshPath, cliSSHOptions{
		Destination: "user@remote",
		Command:     []string{"command", "argument with space"},
	}, proxyURL)
	var exitError *cliExitCodeError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 37 {
		t.Fatalf("runFLCSSH error = %v, want exit code 37", err)
	}
	logData, readErr := os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	logText := string(logData)
	for _, expected := range []string{
		"-O forward -R 127.0.0.1:0:127.0.0.1:17891",
		"user:password@127.0.0.1:23456",
		"-O cancel -R 127.0.0.1:23456:127.0.0.1:17891",
		"-O exit",
	} {
		if !strings.Contains(logText, expected) {
			t.Errorf("SSH log is missing %q:\n%s", expected, logText)
		}
	}

	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLC_SSH_TEST_FORWARD_FAIL", "1")
	err = runFLCSSH(sshPath, cliSSHOptions{
		Destination: "user@remote",
		Command:     []string{"must-not-run"},
	}, proxyURL)
	if err == nil || !strings.Contains(err.Error(), "reverse forwarding failed") {
		t.Fatalf("forwarding denial error = %v", err)
	}
	logData, readErr = os.ReadFile(logPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if strings.Contains(string(logData), "HTTP_PROXY") ||
		strings.Contains(string(logData), "must-not-run") {
		t.Fatalf("forwarding denial entered a remote session:\n%s", logData)
	}
}

func TestFLCSSHInteractiveBootstrapUsesLoginShell(t *testing.T) {
	bootstrap := flcSSHRemoteBootstrap("http://127.0.0.1:23456", nil)
	if !strings.HasSuffix(bootstrap, `exec "${SHELL:-/bin/sh}" -l`) {
		t.Fatalf("interactive bootstrap = %s", bootstrap)
	}
}
