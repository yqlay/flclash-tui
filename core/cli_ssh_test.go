//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCLISSHProfileEdit(t *testing.T) {
	edit, interactive, err := parseCLISSHProfileEdit([]string{
		"school",
		"student@example.edu",
		"--port",
		"2222",
		"--identity",
		"/tmp/id_ed25519",
		"--option",
		"StrictHostKeyChecking=yes",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if interactive || edit.Name != "school" ||
		edit.Destination != "student@example.edu" || edit.Port != 2222 ||
		edit.Identity != "/tmp/id_ed25519" || len(edit.Options) != 1 {
		t.Fatalf("unexpected parsed profile: %+v", edit)
	}
}

func TestCLISSHConfigIsPrivateAndViewsMaskPassword(t *testing.T) {
	configRoot := t.TempDir()
	runtimeRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = runtimeRoot
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previousRuntime })

	err := updateCLISSHConfig(func(config *cliSSHConfig) error {
		config.Profiles = []cliSSHProfile{{
			Name:        "school",
			Destination: "student@example.edu",
			Port:        22,
			Password:    "do-not-display",
		}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configRoot, "flclash", cliSSHConfigFilename)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("SSH config mode = %o, want 0600", info.Mode().Perm())
	}
	views, err := loadCLISSHProfileViews()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(views)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || !views[0].PasswordSet ||
		strings.Contains(string(encoded), "do-not-display") {
		t.Fatalf("password leaked through view: %s", encoded)
	}
}

func TestTUISSHPageRendersMaskedAuthentication(t *testing.T) {
	snapshot := tuiSnapshot{
		Page:         tuiPageSSH,
		SelectedMenu: int(tuiPageSSH),
		SSHProfiles: []tuiSSHProfile{{
			Name:        "school",
			Destination: "student@example.edu",
			Port:        22,
			PasswordSet: true,
			Connected:   true,
			SocksPort:   45678,
		}},
	}
	output := stripTUIANSI(renderTUIAtSize(
		snapshot,
		cliPaths{},
		"private Unix socket",
		true,
		false,
		180,
		30,
	))
	for _, expected := range []string{
		"SSH",
		"school",
		"CONNECTED",
		"password ********",
		"SOCKS5 127.0.0.1:45678",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("SSH page does not contain %q:\n%s", expected, output)
		}
	}
}

func TestCLISSHRejectsManagedForwardingOptions(t *testing.T) {
	for _, option := range []string{
		"ControlPath=/tmp/socket",
		"DynamicForward=127.0.0.1:9999",
		"LocalCommand=id",
	} {
		if err := validateCLISSHOption(option); err == nil {
			t.Fatalf("managed option %q was accepted", option)
		}
	}
}

func TestRunCLICommandWithSSHProxySetsEnvironmentAndExitCode(t *testing.T) {
	err := runCLICommandWithSSHProxy([]string{
		"sh",
		"-c",
		"test \"$ALL_PROXY\" = socks5h://127.0.0.1:45678 && exit 7",
	}, 45678)
	var exitError *cliExitCodeError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 7 {
		t.Fatalf("SSH wrapper exit = %v, want exit code 7", err)
	}
}
