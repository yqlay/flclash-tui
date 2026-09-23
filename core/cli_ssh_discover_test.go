//go:build linux && !cgo && cli

package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseSSHCommandLineDynamicForwardAndDestination(t *testing.T) {
	parsed := parseSSHCommandLine([]string{
		"ssh", "-T", "-D", "0", "-p", "2222", "-l", "deploy", "gateway.example.com",
	})
	if parsed.Host != "gateway.example.com" || parsed.User != "deploy" ||
		parsed.Port != 2222 || len(parsed.DynamicPorts) != 1 || parsed.DynamicPorts[0] != 0 {
		t.Fatalf("parsed = %+v", parsed)
	}
	parsed = parseSSHCommandLine([]string{
		"/usr/bin/ssh", "-D127.0.0.1:1080", "user@10.0.0.5", "-N",
	})
	if parsed.User != "user" || parsed.Host != "10.0.0.5" ||
		len(parsed.DynamicPorts) != 1 || parsed.DynamicPorts[0] != 1080 {
		t.Fatalf("combined -D parsed = %+v", parsed)
	}
	parsed = parseSSHCommandLine([]string{
		"ssh", "-o", "DynamicForward=9050", "-o", "User=git", "github.com",
	})
	if parsed.User != "git" || parsed.Host != "github.com" ||
		len(parsed.DynamicPorts) != 1 || parsed.DynamicPorts[0] != 9050 {
		t.Fatalf("option parsed = %+v", parsed)
	}
	parsed = parseSSHCommandLine([]string{"ssh", "-O", "check", "-S", "/tmp/cm", "host"})
	if !parsed.ControlOp {
		t.Fatal("control operation was not detected")
	}
}

func TestAttachSOCKSTunnelLeavesUpstreamListener(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	go acceptCLITestSOCKS(upstream)
	port := upstream.Addr().(*net.TCPAddr).Port
	previousRelay := startCLISSHRelayForOperation
	startCLISSHRelayForOperation = func(state *cliSSHTunnelState) error {
		return startTestSSHRelay(t, state)
	}
	t.Cleanup(func() { startCLISSHRelayForOperation = previousRelay })
	if err := addCLISSHProfile(cliSSHProfile{
		Name:     "vscode-lab",
		Username: "deploy",
		Host:     "lab.example.com",
		Port:     22,
	}); err != nil {
		t.Fatal(err)
	}
	profile, err := loadCLISSHProfile("vscode-lab")
	if err != nil {
		t.Fatal(err)
	}
	state, err := attachCLISSHSocksTunnel(profile, port)
	if err != nil {
		t.Fatal(err)
	}
	if state.Kind != cliSSHAttachedSOCKSKind || state.UpstreamPort != port {
		t.Fatalf("socks attach state = %+v", state)
	}
	if err := stopCLIStateTunnel(state); err != nil {
		t.Fatal(err)
	}
	if probeCLISSHSOCKS(port, time.Second) != nil {
		t.Fatal("detach stopped the captured ssh -D listener")
	}
}

func TestEnsureCaptureProfileReusesMatchingHost(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := addCLISSHProfile(cliSSHProfile{
		Name:     "home",
		Username: "deploy",
		Host:     "gateway.example.com",
		Port:     22,
	}); err != nil {
		t.Fatal(err)
	}
	profile, err := ensureCLISSHProfileForCapture(cliSSHCaptureCandidate{
		Name:     "vscode-gateway",
		Username: "deploy",
		Host:     "gateway.example.com",
		Port:     22,
		Kind:     cliSSHCaptureSOCKSKind,
	})
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "home" {
		t.Fatalf("reused profile = %q", profile.Name)
	}
}

func TestUniqueCLISSHProfileNameAddsSuffix(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := addCLISSHProfile(cliSSHProfile{
		Name:     "vscode-lab",
		Username: "deploy",
		Host:     "lab.example.com",
		Port:     22,
	}); err != nil {
		t.Fatal(err)
	}
	if name := uniqueCLISSHProfileName("vscode-lab"); name != "vscode-lab-2" {
		t.Fatalf("unique name = %q", name)
	}
}

func TestCaptureSourceFromProcessNames(t *testing.T) {
	previousComm := readProcessComm
	previousParent := readProcessParentComm
	t.Cleanup(func() {
		readProcessComm = previousComm
		readProcessParentComm = previousParent
	})
	readProcessComm = func(int) string { return "ssh" }
	readProcessParentComm = func(int) string { return "code" }
	if source := captureSourceFromProcess(1); source != "vscode" {
		t.Fatalf("source = %q", source)
	}
}

func TestDiscoverLiveSSHSocksUsesEphemeralListenPort(t *testing.T) {
	previousListen := listProcessLoopbackListenPorts
	previousParent := readProcessParentComm
	t.Cleanup(func() {
		listProcessLoopbackListenPorts = previousListen
		readProcessParentComm = previousParent
	})
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	go acceptCLITestSOCKS(upstream)
	port := upstream.Addr().(*net.TCPAddr).Port
	listProcessLoopbackListenPorts = func(pid int) []int {
		if pid != 4242 {
			return nil
		}
		return []int{port}
	}
	readProcessParentComm = func(int) string { return "cursor" }
	candidates := captureSOCKSCandidatesFromSSHProcess(
		4242,
		[]string{"ssh", "-D", "0", "deploy@gateway.example.com"},
	)
	if len(candidates) != 1 || candidates[0].SocksPort != port ||
		candidates[0].Host != "gateway.example.com" || candidates[0].Source != "cursor" {
		t.Fatalf("socks candidates = %+v", candidates)
	}
}

func TestParseSSHCommandLineClusteredFlagsAndControlPath(t *testing.T) {
	parsed := parseSSHCommandLine([]string{
		"ssh", "-4TN", "-M", "-S", "/tmp/cm.sock", "deploy@lab",
	})
	if !parsed.Master || parsed.ControlSocket != "/tmp/cm.sock" ||
		parsed.User != "deploy" || parsed.Host != "lab" {
		t.Fatalf("clustered parse = %+v", parsed)
	}
	parsed = parseSSHCommandLine([]string{
		"ssh", "-o", "ControlPath", "/tmp/other.sock", "-o", "DynamicForward=0", "lab",
	})
	if parsed.ControlSocket != "/tmp/other.sock" ||
		len(parsed.DynamicPorts) != 1 || parsed.DynamicPorts[0] != 0 || parsed.Host != "lab" {
		t.Fatalf("option parse = %+v", parsed)
	}
}

func TestSSHCaptureProfileMatchesEmptyUsername(t *testing.T) {
	profile := cliSSHProfile{Username: "deploy", Host: "lab.example.com", Port: 22}
	if !sshCaptureProfileMatches(profile, cliSSHCaptureCandidate{
		Host: "lab.example.com",
		Port: 22,
	}) {
		t.Fatal("empty candidate username should match host and port")
	}
	if sshCaptureProfileMatches(profile, cliSSHCaptureCandidate{
		Username: "other",
		Host:     "lab.example.com",
		Port:     22,
	}) {
		t.Fatal("different username must not match")
	}
}

func TestSanitizeImportedNameFromVSCodeHost(t *testing.T) {
	if name := captureCLISSHNameHint(cliSSHCaptureCandidate{
		Host:   "lab.example.com",
		Source: "vscode",
	}); name != "vscode-lab.example.com" {
		t.Fatalf("hint = %q", name)
	}
}

func TestParseSSHCommandLineFlagsAfterDestinationAndConfigFile(t *testing.T) {
	parsed := parseSSHCommandLine([]string{
		"ssh", "deploy@gateway.example.com", "-p", "2222", "-D", "0", "-N",
	})
	if parsed.User != "deploy" || parsed.Host != "gateway.example.com" ||
		parsed.Port != 2222 || len(parsed.DynamicPorts) != 1 || parsed.DynamicPorts[0] != 0 {
		t.Fatalf("flags after destination = %+v", parsed)
	}
	parsed = parseSSHCommandLine([]string{
		"ssh", "-T", "-D", "0", "-F", "/tmp/vscode-linux/ssh.config", "lab",
	})
	if parsed.ConfigFile != "/tmp/vscode-linux/ssh.config" || parsed.Host != "lab" ||
		len(parsed.DynamicPorts) != 1 || parsed.DynamicPorts[0] != 0 {
		t.Fatalf("vscode -F parse = %+v", parsed)
	}
	parsed = parseSSHCommandLine([]string{
		"ssh", "-S/tmp/cm.sock", "-Jjump.example", "lab",
	})
	if parsed.ControlSocket != "/tmp/cm.sock" || parsed.Jump != "jump.example" || parsed.Host != "lab" {
		t.Fatalf("combined -S/-J parse = %+v", parsed)
	}
}

func TestParseSSHCommandLineDoesNotStealHostForBooleanOption(t *testing.T) {
	parsed := parseSSHCommandLine([]string{
		"ssh", "-o", "RequestTTY", "deploy@lab.example.com",
	})
	if parsed.Host != "lab.example.com" || parsed.User != "deploy" {
		t.Fatalf("boolean -o stole destination: %+v", parsed)
	}
}

func TestEnrichSSHParsedCommandUsesDashGAndConfigFile(t *testing.T) {
	binDirectory := t.TempDir()
	sshPath := filepath.Join(binDirectory, "ssh")
	logPath := filepath.Join(t.TempDir(), "ssh.log")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> \"" + logPath + "\"\n" +
		"case \" $* \" in\n" +
		"  *' -F /tmp/vscode.cfg '*|*' -F /tmp/vscode.cfg')\n" +
		"    printf 'user vscodeuser\\nport 2222\\ndynamicforward 0\\ncontrolmaster auto\\ncontrolpath /tmp/vscode.sock\\n' ;;\n" +
		"  *' -G '*)\n" +
		"    printf 'user deploy\\nport 2200\\ncontrolmaster auto\\ncontrolpath /tmp/cm.sock\\n' ;;\n" +
		"  *) exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(sshPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDirectory)
	parsed := enrichSSHParsedCommand(cliSSHParsedCommand{Host: "lab-enrich-g", Port: 22})
	if parsed.User != "deploy" || parsed.Port != 2200 || parsed.ControlSocket != "/tmp/cm.sock" || !parsed.Master {
		t.Fatalf("ssh -G fill = %+v", parsed)
	}
	parsed = enrichSSHParsedCommand(cliSSHParsedCommand{
		Host:       "lab-enrich-f",
		Port:       22,
		ConfigFile: "/tmp/vscode.cfg",
	})
	if parsed.User != "vscodeuser" || parsed.Port != 2222 ||
		len(parsed.DynamicPorts) != 1 || parsed.DynamicPorts[0] != 0 {
		t.Fatalf("ssh -G -F fill = %+v", parsed)
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(logged), "-F /tmp/vscode.cfg") {
		t.Fatalf("ssh -G did not receive VS Code -F:\n%s", logged)
	}
}

func TestEnrichSSHParsedCommandIgnoresControlPathWhenMasterDisabled(t *testing.T) {
	binDirectory := t.TempDir()
	sshPath := filepath.Join(binDirectory, "ssh")
	script := "#!/bin/sh\nprintf 'user deploy\\nport 22\\ncontrolmaster no\\ncontrolpath /tmp/stale.sock\\n'\n"
	if err := os.WriteFile(sshPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDirectory)
	parsed := enrichSSHParsedCommand(cliSSHParsedCommand{Host: "lab-master-no", Port: 22})
	if parsed.ControlSocket != "" || parsed.Master {
		t.Fatalf("disabled ControlMaster still filled ControlPath: %+v", parsed)
	}
}

func TestProcNetListenAddressOKAcceptsMappedAndUnspecified(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "::1", "::ffff:127.0.0.1", "0.0.0.0", "::"} {
		if !procNetListenAddressOK(host) {
			t.Fatalf("%s should be treated as a local SSH listen address", host)
		}
	}
	if procNetListenAddressOK("8.8.8.8") {
		t.Fatal("public address must not be treated as a local SSH listen address")
	}
	host, port, ok := parseProcNetAddress("0000000000000000FFFF00000100007F:1F90", 6)
	if !ok || port != 8080 || !procNetListenAddressOK(host) {
		t.Fatalf("mapped loopback tcp6 = %s %d %t", host, port, ok)
	}
}

func TestLooksLikeSSHControlSocketSkipsKeysAndAgent(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/ssh-auth/agent.123")
	if looksLikeSSHControlSocket("/tmp/ssh-auth/agent.123", os.ModeSocket) {
		t.Fatal("ssh-agent socket must not be treated as ControlMaster")
	}
	if looksLikeSSHControlSocket("/home/user/.ssh/id_ed25519", 0o600) {
		t.Fatal("private key must not be treated as ControlMaster")
	}
	if !looksLikeSSHControlSocket("/home/user/.ssh/sockets/cm-lab.sock", os.ModeSocket) {
		t.Fatal("unix ControlMaster socket should be accepted")
	}
	if !looksLikeSSHControlSocket("/home/user/.ssh/sockets/cm-lab.sock", 0o600) {
		t.Fatal("named control file in sockets/ should be accepted")
	}
}

func TestDiscoverDiskSSHMastersSkipsIdentityFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_RUNTIME_DIR", "")
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(sshDir, "id_ed25519")
	if err := os.WriteFile(keyPath, []byte("fake-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	binDirectory := t.TempDir()
	sshPath := filepath.Join(binDirectory, "ssh")
	logPath := filepath.Join(t.TempDir(), "ssh.log")
	script := "#!/bin/sh\necho \"$*\" >> \"" + logPath + "\"\nexit 1\n"
	if err := os.WriteFile(sshPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDirectory)
	_ = discoverDiskSSHMasters()
	logged, err := os.ReadFile(logPath)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.Contains(string(logged), "id_ed25519") {
		t.Fatalf("disk discovery probed a private key:\n%s", logged)
	}
}

func TestIsFlClashManagedSSHPathAndSOCKSPort(t *testing.T) {
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = t.TempDir()
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previousRuntime })
	directory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "control.sock")
	if !isFlClashManagedSSHPath(path) {
		t.Fatal("runtime ControlPath must be skipped")
	}
	if isFlClashManagedSSHPath(filepath.Join(t.TempDir(), "user.sock")) {
		t.Fatal("foreign ControlPath must not be skipped")
	}
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	port := upstream.Addr().(*net.TCPAddr).Port
	state := cliSSHTunnelState{
		Name:         "home",
		Port:         port + 1,
		UpstreamPort: port,
		Kind:         cliSSHAttachedSOCKSKind,
		StatePath:    filepath.Join(directory, cliSSHPersistentStateFile),
	}
	if err := saveCLISSHTunnelState(state); err != nil {
		t.Fatal(err)
	}
	if !isFlClashManagedSOCKSPort(port) {
		t.Fatal("active captured SOCKS port must be skipped")
	}
}

func TestProbeCLISSHSOCKSAcceptsIPv6Loopback(t *testing.T) {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skip("IPv6 loopback is unavailable")
	}
	defer listener.Close()
	go acceptCLITestSOCKS(listener)
	port := listener.Addr().(*net.TCPAddr).Port
	if err := probeCLISSHSOCKS(port, time.Second); err != nil {
		t.Fatalf("::1 SOCKS probe: %v", err)
	}
	if !cliSSHSOCKSReady(port) {
		t.Fatal("::1 SOCKS ready check failed")
	}
}

func TestCaptureSourceFromVSCodeConfigPath(t *testing.T) {
	if source := captureSourceFromCommand(1, cliSSHParsedCommand{
		ConfigFile: "/tmp/vscode-linux-abc/ssh.config",
	}); source != "vscode" {
		t.Fatalf("vscode config source = %q", source)
	}
	previousCmd := readProcessCommandLine
	t.Cleanup(func() { readProcessCommandLine = previousCmd })
	self := os.Getpid()
	readProcessCommandLine = func(pid int) []string {
		if pid == self {
			return []string{"ssh", "-T", "-D", "0", "-F", "/tmp/vscode-linux-abc/ssh.config", "lab"}
		}
		return nil
	}
	if !captureHasRemoteSSHProcess() {
		t.Fatal("VS Code -F ssh process should be recognized without a code parent comm")
	}
}

func TestCaptureMasterCandidatesFromProcessControlPath(t *testing.T) {
	binDirectory := t.TempDir()
	sshPath := filepath.Join(binDirectory, "ssh")
	controlPath := filepath.Join(t.TempDir(), "cm.sock")
	if err := os.WriteFile(controlPath, []byte("master"), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"control=''\noperation=''\nprevious=''\n" +
		"for argument in \"$@\"; do\n" +
		"  if [ \"$previous\" = '-S' ]; then control=\"$argument\"; fi\n" +
		"  if [ \"$previous\" = '-O' ]; then operation=\"$argument\"; fi\n" +
		"  previous=\"$argument\"\n" +
		"done\n" +
		"if [ \"$operation\" = check ]; then test -e \"$control\"; exit $?; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(sshPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDirectory)
	candidates := captureMasterCandidatesFromSSHProcess(7, []string{
		"ssh", "-M", "-S", controlPath, "deploy@lab.example.com",
	})
	if len(candidates) != 1 || candidates[0].ControlPath != controlPath ||
		candidates[0].Host != "lab.example.com" || candidates[0].Username != "deploy" {
		t.Fatalf("process master candidates = %+v", candidates)
	}
}
