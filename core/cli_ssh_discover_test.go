//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
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
	profile, wasCreated, err := ensureCLISSHProfileForCapture(cliSSHCaptureCandidate{
		Name:     "vscode-gateway",
		Username: "deploy",
		Host:     "gateway.example.com",
		Port:     22,
		Kind:     cliSSHCaptureSOCKSKind,
	})
	if err != nil {
		t.Fatal(err)
	}
	if wasCreated {
		t.Fatal("expected profile to be reused, not created")
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

func TestSplitWindowsCommandLineAndSSHExe(t *testing.T) {
	args := splitWindowsCommandLine(
		`"C:\Program Files\OpenSSH\ssh.exe" -T -D 58569 "RDMA_DCN_10_xpipe" bash`,
	)
	if len(args) != 6 || args[0] != `C:\Program Files\OpenSSH\ssh.exe` ||
		args[4] != "RDMA_DCN_10_xpipe" || args[5] != "bash" {
		t.Fatalf("windows argv = %#v", args)
	}
	if !isOpenSSHClientName(args[0]) {
		t.Fatal("ssh.exe must be recognized as OpenSSH")
	}
	parsed := parseSSHCommandLine(args)
	if parsed.Host != "RDMA_DCN_10_xpipe" || parsed.User != "" ||
		len(parsed.DynamicPorts) != 1 || parsed.DynamicPorts[0] != 58569 {
		t.Fatalf("vscode ssh.exe parse = %+v", parsed)
	}
}

func TestWindowsPathToWSL(t *testing.T) {
	if path := windowsPathToWSL(`C:\Users\Y.Q.Lay\.ssh\config`); path != "/mnt/c/Users/Y.Q.Lay/.ssh/config" {
		t.Fatalf("wsl path = %q", path)
	}
}

func TestIsProxyEngineComm(t *testing.T) {
	if !isProxyEngineComm("mihomo") || !isProxyEngineComm("clash-meta") || isProxyEngineComm("ssh") {
		t.Fatal("proxy engine classification")
	}
}

func TestDiscoverWindowsSSHExeSOCKS(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	go acceptCLITestSOCKS(upstream)
	port := upstream.Addr().(*net.TCPAddr).Port
	configDir := t.TempDir()
	configPath := filepath.Join(configDir, "config")
	if err := os.WriteFile(configPath, []byte("Host lab\n  HostName 172.28.9.43\n  User dell\n  Port 10110\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	binDirectory := t.TempDir()
	sshPath := filepath.Join(binDirectory, "ssh")
	script := "#!/bin/sh\n" +
		"case \" $* \" in\n" +
		"  *' -F '*lab*) printf 'user dell\\nport 10110\\nhostname 172.28.9.43\\ncontrolmaster no\\n' ;;\n" +
		"  *) exit 1 ;;\n" +
		"esac\n"
	if err := os.WriteFile(sshPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDirectory)
	resetCachedLoopbackListenOwners()
	previousWSL := cliRunningOnWSL
	previousList := listWindowsSSHSnapshot
	previousOwners := linuxLoopbackListenOwners
	cliRunningOnWSL = func() bool { return true }
	listWindowsSSHSnapshot = func() (windowsSSHSnapshot, error) {
		return windowsSSHSnapshot{
			ConfigPath: configPath,
			Processes: []windowsSSHProcess{{
				PID:         42,
				CommandLine: `"C:\Program Files\OpenSSH\ssh.exe" -T -D ` + strconv.Itoa(port) + ` "lab" bash`,
			}},
		}, nil
	}
	linuxLoopbackListenOwners = func() map[int]string { return map[int]string{} }
	t.Cleanup(func() {
		cliRunningOnWSL = previousWSL
		listWindowsSSHSnapshot = previousList
		linuxLoopbackListenOwners = previousOwners
	})
	candidates := discoverWindowsSSHSocksCandidates()
	if len(candidates) != 1 || candidates[0].SocksPort != port ||
		candidates[0].Host != "172.28.9.43" || candidates[0].Username != "dell" ||
		candidates[0].Port != 10110 || candidates[0].Kind != cliSSHCaptureSOCKSKind {
		t.Fatalf("windows socks candidates = %+v", candidates)
	}
}

func TestDiscoverWindowsSSHExeTwoDynamicPorts(t *testing.T) {
	up1 := listenCLITestTCP(t)
	defer up1.Close()
	go acceptCLITestSOCKS(up1)
	up2 := listenCLITestTCP(t)
	defer up2.Close()
	go acceptCLITestSOCKS(up2)
	port1 := up1.Addr().(*net.TCPAddr).Port
	port2 := up2.Addr().(*net.TCPAddr).Port
	resetCachedLoopbackListenOwners()
	previousWSL := cliRunningOnWSL
	previousList := listWindowsSSHSnapshot
	previousOwners := linuxLoopbackListenOwners
	cliRunningOnWSL = func() bool { return true }
	listWindowsSSHSnapshot = func() (windowsSSHSnapshot, error) {
		return windowsSSHSnapshot{
			Processes: []windowsSSHProcess{
				{CommandLine: `ssh.exe -T -D ` + strconv.Itoa(port1) + ` host-a bash`},
				{CommandLine: `ssh.exe -T -D ` + strconv.Itoa(port2) + ` host-b bash`},
			},
		}, nil
	}
	linuxLoopbackListenOwners = func() map[int]string { return map[int]string{} }
	t.Cleanup(func() {
		cliRunningOnWSL = previousWSL
		listWindowsSSHSnapshot = previousList
		linuxLoopbackListenOwners = previousOwners
	})
	candidates := discoverWindowsSSHSocksCandidates()
	if len(candidates) != 2 {
		t.Fatalf("want 2 windows ssh -D candidates, got %+v", candidates)
	}
}

func TestProxyEngineListenPortIsNotCaptured(t *testing.T) {
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	go acceptCLITestSOCKS(upstream)
	port := upstream.Addr().(*net.TCPAddr).Port
	previousOwners := linuxLoopbackListenOwners
	resetCachedLoopbackListenOwners()
	linuxLoopbackListenOwners = func() map[int]string {
		return map[int]string{port: "mihomo"}
	}
	t.Cleanup(func() {
		linuxLoopbackListenOwners = previousOwners
		resetCachedLoopbackListenOwners()
	})
	if !isProxyEngineListenPort(port) {
		t.Fatal("mihomo listen port must be skipped")
	}
	candidates := captureSOCKSCandidatesFromSSHProcess(1, []string{
		"ssh", "-D", strconv.Itoa(port), "deploy@gateway.example.com",
	})
	if len(candidates) != 0 {
		t.Fatalf("mihomo SOCKS captured as SSH: %+v", candidates)
	}
}

func TestCaptureEmptyHintForRemoteProfiles(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	previousWSL := cliRunningOnWSL
	cliRunningOnWSL = func() bool { return false }
	lastWindowsSSHSnapshot = windowsSSHSnapshot{}
	t.Cleanup(func() { cliRunningOnWSL = previousWSL })
	if err := addCLISSHProfile(cliSSHProfile{
		Name:     "dcn9",
		Username: "dell",
		Host:     "172.28.9.43",
		Port:     10109,
	}); err != nil {
		t.Fatal(err)
	}
	hint := formatCLICaptureEmptyHint()
	if !strings.Contains(hint, "another host") || !strings.Contains(hint, "profile") {
		t.Fatalf("hint = %q", hint)
	}
}

func TestWSLEmptyHintMentionsWindowsSSH(t *testing.T) {
	previousWSL := cliRunningOnWSL
	cliRunningOnWSL = func() bool { return true }
	lastWindowsSSHSnapshot = windowsSSHSnapshot{ProcessCount: 2, HadSOCKS: false}
	t.Cleanup(func() {
		cliRunningOnWSL = previousWSL
		lastWindowsSSHSnapshot = windowsSSHSnapshot{}
	})
	hint := formatCLICaptureEmptyHint()
	if !strings.Contains(hint, "ssh.exe") || !strings.Contains(hint, "ControlMaster no") {
		t.Fatalf("hint = %q", hint)
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

func TestDiscoverReverseSocksCandidateAndCapture(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	go acceptCLITestSOCKS(upstream)
	port := upstream.Addr().(*net.TCPAddr).Port

	previousSSHD := hasSSHDProcess
	previousPorts := collectLoopbackListenPorts
	previousOwners := linuxLoopbackListenOwners
	resetCachedLoopbackListenOwners()

	hasSSHDProcess = func() bool { return true }
	collectLoopbackListenPorts = func() []procListenPort {
		return []procListenPort{{port: port, inode: 99999}}
	}
	linuxLoopbackListenOwners = func() map[int]string { return map[int]string{} }

	t.Cleanup(func() {
		hasSSHDProcess = previousSSHD
		collectLoopbackListenPorts = previousPorts
		linuxLoopbackListenOwners = previousOwners
		resetCachedLoopbackListenOwners()
	})

	candidates := discoverReverseSocksCandidates()
	if len(candidates) != 1 {
		t.Fatalf("want 1 reverse socks candidate, got %+v", candidates)
	}
	c := candidates[0]
	if c.Kind != cliSSHCaptureReverseSOCKSKind || c.SocksPort != port || c.Source != "sshd" {
		t.Fatalf("unexpected reverse candidate: %+v", c)
	}

	label := formatCLICaptureCandidate(c)
	if !strings.Contains(label, "←R") || !strings.Contains(label, strconv.Itoa(port)) {
		t.Fatalf("unexpected label: %q", label)
	}

	previousRelay := startCLISSHRelayForOperation
	startCLISSHRelayForOperation = func(state *cliSSHTunnelState) error {
		return startTestSSHRelay(t, state)
	}
	t.Cleanup(func() { startCLISSHRelayForOperation = previousRelay })

	state, already, err := captureCLISSHCandidate(c)
	if err != nil {
		t.Fatalf("capture reverse candidate failed: %v", err)
	}
	if already {
		t.Fatal("first capture should not be reported as already active")
	}
	if state.Kind != cliSSHAttachedSOCKSKind || state.UpstreamPort != port {
		t.Fatalf("unexpected state after attach: %+v", state)
	}
	if !state.AutoCreated {
		t.Fatal("expected state.AutoCreated to be true")
	}
	if _, err := loadCLISSHProfile(c.Name); err != nil {
		t.Fatalf("expected profile %q to be saved before detach: %v", c.Name, err)
	}

	if err := stopCLIStateTunnel(state); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCLISSHProfile(c.Name); err == nil {
		t.Fatalf("expected profile %q to be deleted after detach", c.Name)
	}
	if probeCLISSHSOCKS(port, time.Second) != nil {
		t.Fatal("detach must leave upstream ssh -R listener running")
	}
}

func TestDiscoverReverseSocksExcludesProxyEngine(t *testing.T) {
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	go acceptCLITestSOCKS(upstream)
	port := upstream.Addr().(*net.TCPAddr).Port

	previousSSHD := hasSSHDProcess
	previousPorts := collectLoopbackListenPorts
	previousOwners := linuxLoopbackListenOwners
	resetCachedLoopbackListenOwners()

	hasSSHDProcess = func() bool { return true }
	collectLoopbackListenPorts = func() []procListenPort {
		return []procListenPort{{port: port, inode: 99999}}
	}
	linuxLoopbackListenOwners = func() map[int]string {
		return map[int]string{port: "mihomo"}
	}

	t.Cleanup(func() {
		hasSSHDProcess = previousSSHD
		collectLoopbackListenPorts = previousPorts
		linuxLoopbackListenOwners = previousOwners
		resetCachedLoopbackListenOwners()
	})

	candidates := discoverReverseSocksCandidates()
	if len(candidates) != 0 {
		t.Fatalf("proxy engine port must not be discovered as reverse SOCKS: %+v", candidates)
	}
}

func TestDiscoverReverseSocksExcludesNonSOCKS(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port

	previousSSHD := hasSSHDProcess
	previousPorts := collectLoopbackListenPorts
	previousOwners := linuxLoopbackListenOwners
	resetCachedLoopbackListenOwners()

	hasSSHDProcess = func() bool { return true }
	collectLoopbackListenPorts = func() []procListenPort {
		return []procListenPort{{port: port, inode: 99999}}
	}
	linuxLoopbackListenOwners = func() map[int]string { return map[int]string{} }

	t.Cleanup(func() {
		hasSSHDProcess = previousSSHD
		collectLoopbackListenPorts = previousPorts
		linuxLoopbackListenOwners = previousOwners
		resetCachedLoopbackListenOwners()
	})

	candidates := discoverReverseSocksCandidates()
	if len(candidates) != 0 {
		t.Fatalf("non-SOCKS TCP port must not be discovered: %+v", candidates)
	}
}

func TestDiscoverReverseSocksWithoutSSHD(t *testing.T) {
	previousSSHD := hasSSHDProcess
	hasSSHDProcess = func() bool { return false }
	t.Cleanup(func() { hasSSHDProcess = previousSSHD })

	candidates := discoverReverseSocksCandidates()
	if len(candidates) != 0 {
		t.Fatalf("without sshd process, candidates must be empty: %+v", candidates)
	}
}

func TestCaptureEmptyHintMentionsReverseSOCKS(t *testing.T) {
	previousWSL := cliRunningOnWSL
	previousInbound := captureHasInboundSSHConnection
	cliRunningOnWSL = func() bool { return false }
	captureHasInboundSSHConnection = func() bool { return true }
	t.Cleanup(func() {
		cliRunningOnWSL = previousWSL
		captureHasInboundSSHConnection = previousInbound
	})

	hint := formatCLICaptureEmptyHint()
	if !strings.Contains(hint, "ssh -R") || !strings.Contains(hint, "inbound SSH") {
		t.Fatalf("expected hint to mention ssh -R, got: %q", hint)
	}
}

func TestFindCLILiveSSHSocksReverse(t *testing.T) {
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	go acceptCLITestSOCKS(upstream)
	port := upstream.Addr().(*net.TCPAddr).Port

	previousSSHD := hasSSHDProcess
	previousPorts := collectLoopbackListenPorts
	previousOwners := linuxLoopbackListenOwners
	resetCachedLoopbackListenOwners()

	hasSSHDProcess = func() bool { return true }
	collectLoopbackListenPorts = func() []procListenPort {
		return []procListenPort{{port: port, inode: 88888}}
	}
	linuxLoopbackListenOwners = func() map[int]string { return map[int]string{} }

	t.Cleanup(func() {
		hasSSHDProcess = previousSSHD
		collectLoopbackListenPorts = previousPorts
		linuxLoopbackListenOwners = previousOwners
		resetCachedLoopbackListenOwners()
	})

	profile := cliSSHProfile{
		Name: fmt.Sprintf("sshd-reverse-%d", port),
		Host: "127.0.0.1",
		Port: 22,
	}
	socksPort, ok := findCLILiveSSHSocks(profile)
	if !ok || socksPort != port {
		t.Fatalf("want socksPort=%d, got %d (ok=%v)", port, socksPort, ok)
	}

	outbound := cliSSHProfile{
		Name: "my-outbound-server",
		Host: "127.0.0.1",
		Port: 22,
	}
	if outPort, ok := findCLILiveSSHSocks(outbound); ok || outPort != 0 {
		t.Fatalf("outbound profile must not match reverse socks, got port=%d, ok=%v", outPort, ok)
	}
}

func TestDiscoverReverseSocksFallbackClientIP(t *testing.T) {
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	go acceptCLITestSOCKS(upstream)
	port := upstream.Addr().(*net.TCPAddr).Port

	previousSSHD := hasSSHDProcess
	previousPorts := collectLoopbackListenPorts
	previousOwners := linuxLoopbackListenOwners
	previousClientIP := findAnyInboundSSHClientIP
	resetCachedLoopbackListenOwners()

	hasSSHDProcess = func() bool { return true }
	collectLoopbackListenPorts = func() []procListenPort {
		return []procListenPort{{port: port, inode: 77777}}
	}
	linuxLoopbackListenOwners = func() map[int]string { return map[int]string{} }
	findAnyInboundSSHClientIP = func() string { return "192.168.10.50" }

	t.Cleanup(func() {
		hasSSHDProcess = previousSSHD
		collectLoopbackListenPorts = previousPorts
		linuxLoopbackListenOwners = previousOwners
		findAnyInboundSSHClientIP = previousClientIP
		resetCachedLoopbackListenOwners()
	})

	candidates := discoverReverseSocksCandidates()
	if len(candidates) != 1 {
		t.Fatalf("want 1 candidate, got %+v", candidates)
	}
	if candidates[0].Host != "192.168.10.50" {
		t.Fatalf("want host 192.168.10.50, got %q", candidates[0].Host)
	}
}

func TestAttachCLISSHProfileReverseSocks(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	go acceptCLITestSOCKS(upstream)
	port := upstream.Addr().(*net.TCPAddr).Port

	previousSSHD := hasSSHDProcess
	previousPorts := collectLoopbackListenPorts
	previousOwners := linuxLoopbackListenOwners
	resetCachedLoopbackListenOwners()

	hasSSHDProcess = func() bool { return true }
	collectLoopbackListenPorts = func() []procListenPort {
		return []procListenPort{{port: port, inode: 66666}}
	}
	linuxLoopbackListenOwners = func() map[int]string { return map[int]string{} }

	previousRelay := startCLISSHRelayForOperation
	startCLISSHRelayForOperation = func(state *cliSSHTunnelState) error {
		return startTestSSHRelay(t, state)
	}

	t.Cleanup(func() {
		hasSSHDProcess = previousSSHD
		collectLoopbackListenPorts = previousPorts
		linuxLoopbackListenOwners = previousOwners
		startCLISSHRelayForOperation = previousRelay
		resetCachedLoopbackListenOwners()
	})

	profileName := fmt.Sprintf("sshd-reverse-%d", port)
	err := addCLISSHProfile(cliSSHProfile{
		Name:     profileName,
		Username: "testuser",
		Host:     "127.0.0.1",
		Port:     22,
	})
	if err != nil {
		t.Fatal(err)
	}

	state, already, err := attachCLISSHProfile(profileName)
	if err != nil {
		t.Fatalf("attachCLISSHProfile failed: %v", err)
	}
	if already {
		t.Fatal("first attach should not be already active")
	}
	if state.Kind != cliSSHAttachedSOCKSKind || state.UpstreamPort != port {
		t.Fatalf("unexpected state: %+v", state)
	}

	if err := stopCLIStateTunnel(state); err != nil {
		t.Fatal(err)
	}
	if _, err := loadCLISSHProfile(profileName); err != nil {
		t.Fatalf("manually-added profile should survive stop: %v", err)
	}
}

func TestDiscoverReverseSocksCandidatePreExistingProfileNotDeleted(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	go acceptCLITestSOCKS(upstream)
	port := upstream.Addr().(*net.TCPAddr).Port

	profileName := fmt.Sprintf("sshd-reverse-%d", port)
	if err := addCLISSHProfile(cliSSHProfile{
		Name:     profileName,
		Username: "preexisting-user",
		Host:     "192.168.1.100",
		Port:     22,
	}); err != nil {
		t.Fatal(err)
	}

	previousSSHD := hasSSHDProcess
	previousPorts := collectLoopbackListenPorts
	previousOwners := linuxLoopbackListenOwners
	resetCachedLoopbackListenOwners()

	hasSSHDProcess = func() bool { return true }
	collectLoopbackListenPorts = func() []procListenPort {
		return []procListenPort{{port: port, inode: 99998}}
	}
	linuxLoopbackListenOwners = func() map[int]string { return map[int]string{} }

	t.Cleanup(func() {
		hasSSHDProcess = previousSSHD
		collectLoopbackListenPorts = previousPorts
		linuxLoopbackListenOwners = previousOwners
		resetCachedLoopbackListenOwners()
	})

	candidates := discoverReverseSocksCandidates()
	if len(candidates) != 1 {
		t.Fatalf("want 1 reverse socks candidate, got %+v", candidates)
	}
	c := candidates[0]

	previousRelay := startCLISSHRelayForOperation
	startCLISSHRelayForOperation = func(state *cliSSHTunnelState) error {
		return startTestSSHRelay(t, state)
	}
	t.Cleanup(func() { startCLISSHRelayForOperation = previousRelay })

	state, already, err := captureCLISSHCandidate(c)
	if err != nil {
		t.Fatalf("capture reverse candidate failed: %v", err)
	}
	if already {
		t.Fatal("first capture should not be reported as already active")
	}
	if state.AutoCreated {
		t.Fatal("pre-existing profile must not have state.AutoCreated=true")
	}

	if err := stopCLIStateTunnel(state); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadCLISSHProfile(profileName)
	if err != nil {
		t.Fatalf("pre-existing profile must not be deleted after detach: %v", err)
	}
	if loaded.Username != "preexisting-user" {
		t.Fatalf("profile was modified unexpectedly: %+v", loaded)
	}
}

func TestCachedLoopbackListenOwnersConcurrency(t *testing.T) {
	previousOwners := linuxLoopbackListenOwners
	linuxLoopbackListenOwners = func() map[int]string {
		return map[int]string{10808: "mihomo", 10809: "sshd"}
	}
	t.Cleanup(func() {
		linuxLoopbackListenOwners = previousOwners
		resetCachedLoopbackListenOwners()
	})

	done := make(chan struct{})
	for i := 0; i < 8; i++ {
		go func(id int) {
			for j := 0; j < 50; j++ {
				if j%5 == 0 {
					resetCachedLoopbackListenOwners()
				} else {
					_ = isProxyEngineListenPort(10808)
					_ = isProxyEngineListenPort(10809)
				}
			}
			done <- struct{}{}
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
}

func TestFormatCLICaptureCandidateClientIP(t *testing.T) {
	cUnknown := cliSSHCaptureCandidate{
		Name:      "sshd-reverse-10808",
		Username:  "alice",
		Host:      "127.0.0.1",
		Kind:      cliSSHCaptureReverseSOCKSKind,
		SocksPort: 10808,
		Source:    "sshd",
	}
	labelUnknown := formatCLICaptureCandidate(cUnknown)
	if !strings.Contains(labelUnknown, "(client IP unknown)") {
		t.Fatalf("expected '(client IP unknown)' in label, got %q", labelUnknown)
	}
	if strings.Contains(labelUnknown, "@127.0.0.1") {
		t.Fatalf("label should not contain '@127.0.0.1', got %q", labelUnknown)
	}

	cEmpty := cliSSHCaptureCandidate{
		Name:      "sshd-reverse-10808",
		Username:  "alice",
		Host:      "",
		Kind:      cliSSHCaptureReverseSOCKSKind,
		SocksPort: 10808,
		Source:    "sshd",
	}
	labelEmpty := formatCLICaptureCandidate(cEmpty)
	if !strings.Contains(labelEmpty, "(client IP unknown)") {
		t.Fatalf("expected '(client IP unknown)' in empty host label, got %q", labelEmpty)
	}

	cKnown := cliSSHCaptureCandidate{
		Name:      "sshd-reverse-10808",
		Username:  "alice",
		Host:      "192.168.1.50",
		Kind:      cliSSHCaptureReverseSOCKSKind,
		SocksPort: 10808,
		Source:    "sshd",
	}
	labelKnown := formatCLICaptureCandidate(cKnown)
	if !strings.Contains(labelKnown, "alice@192.168.1.50") {
		t.Fatalf("expected 'alice@192.168.1.50' in label, got %q", labelKnown)
	}
}

func TestActiveCLIPersistentSSHTunnelDeadSOCKSDeletesAutoCreatedProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = t.TempDir()
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previousRuntime })

	directory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		t.Fatal(err)
	}

	profileName := "sshd-reverse-49999"
	if err := addCLISSHProfile(cliSSHProfile{
		Name:     profileName,
		Username: "alice",
		Host:     "127.0.0.1",
		Port:     22,
	}); err != nil {
		t.Fatal(err)
	}

	statePath := filepath.Join(directory, cliSSHPersistentStateFile)
	state := cliSSHTunnelState{
		Name:         profileName,
		Kind:         cliSSHAttachedSOCKSKind,
		UpstreamPort: 49999,
		Port:         50000,
		AutoCreated:  true,
		StatePath:    statePath,
	}
	if err := saveCLISSHTunnelState(state); err != nil {
		t.Fatal(err)
	}

	loaded, active, err := activeCLIPersistentSSHTunnel()
	if err != nil {
		t.Fatalf("activeCLIPersistentSSHTunnel failed: %v", err)
	}
	if active {
		t.Fatalf("expected inactive for dead SOCKS listener, got active state: %+v", loaded)
	}

	if _, err := loadCLISSHProfile(profileName); err == nil {
		t.Fatalf("expected auto-created profile %q to be deleted on dead state recovery", profileName)
	}

	userProfile := "user-manual-profile"
	if err := addCLISSHProfile(cliSSHProfile{
		Name:     userProfile,
		Username: "alice",
		Host:     "127.0.0.1",
		Port:     22,
	}); err != nil {
		t.Fatal(err)
	}
	userState := cliSSHTunnelState{
		Name:         userProfile,
		Kind:         cliSSHAttachedSOCKSKind,
		UpstreamPort: 49998,
		Port:         50001,
		AutoCreated:  false,
		StatePath:    statePath,
	}
	if err := saveCLISSHTunnelState(userState); err != nil {
		t.Fatal(err)
	}
	_, active, err = activeCLIPersistentSSHTunnel()
	if err != nil {
		t.Fatalf("activeCLIPersistentSSHTunnel failed: %v", err)
	}
	if active {
		t.Fatal("expected inactive for dead SOCKS listener")
	}
	if _, err := loadCLISSHProfile(userProfile); err != nil {
		t.Fatalf("user profile %q must be preserved on dead state recovery: %v", userProfile, err)
	}
}

func TestStopCLISSHAttachedSOCKSRelayErrorKeepsAutoCreatedProfile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	profileName := "sshd-reverse-49997"
	if err := addCLISSHProfile(cliSSHProfile{
		Name:     profileName,
		Username: "alice",
		Host:     "127.0.0.1",
		Port:     22,
	}); err != nil {
		t.Fatal(err)
	}

	state := cliSSHTunnelState{
		Name:         profileName,
		Kind:         cliSSHAttachedSOCKSKind,
		UpstreamPort: 49997,
		Port:         50002,
		AutoCreated:  true,
		RelayPID:     os.Getpid(),
		RelayControl: "/nonexistent/socket/path",
	}

	err := stopCLISSHAttachedSOCKS(state)
	if err == nil {
		t.Fatal("expected error stopping non-existent relay control")
	}

	if _, err := loadCLISSHProfile(profileName); err != nil {
		t.Fatalf("profile %q should be kept when relay stop fails: %v", profileName, err)
	}
}
