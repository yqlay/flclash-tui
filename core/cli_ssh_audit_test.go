//go:build linux && !cgo && cli

package main

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

// The CLI starts detached helpers by re-executing itself. In a Go test that
// executable is core.test; dispatch helpers instead of accidentally starting
// another complete test suite. The normal test entry remains unchanged.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "_ssh_relay" {
		if err := runCLISSHRelayCommand(os.Args[2:]); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(runIsolatedCLITestSuite(m))
}

func runIsolatedCLITestSuite(m *testing.M) int {
	directory, err := os.MkdirTemp("", "flclash-cli-test-suite-")
	if err != nil {
		return 1
	}
	defer os.RemoveAll(directory)
	// Older tests override only config home. Give every test process a safe
	// runtime default so concurrent suites cannot overwrite each other's SSH
	// records, and so no unit test can accidentally inspect a user's live tunnel.
	cliRuntimeDirectoryOverride = filepath.Join(directory, "runtime")
	if err := os.Setenv("XDG_CONFIG_HOME", filepath.Join(directory, "config")); err != nil {
		return 1
	}
	return m.Run()
}

func TestSSHAuditSavedCredentialsOverrideGlobalAuthenticationDefaults(t *testing.T) {
	for _, profile := range []cliSSHProfile{
		{Password: "password"},
		{Identity: "/private/key", IdentityPassphrase: "passphrase"},
		{Identity: "/private/key", Password: "password"},
	} {
		joined := strings.Join(cliSSHTunnelArguments(profile, "/private/control"), " ")
		if !strings.Contains(joined, "BatchMode=no") {
			t.Errorf("saved credentials did not explicitly disable BatchMode: %s", joined)
		}
		if profile.Identity != "" && !strings.Contains(joined, "PubkeyAuthentication=yes") {
			t.Errorf("explicit private key authentication can be disabled by ssh_config: %s", joined)
		}
		if profile.Password != "" && (!strings.Contains(joined, "PasswordAuthentication=yes") || !strings.Contains(joined, "KbdInteractiveAuthentication=yes")) {
			t.Errorf("saved password authentication can be disabled by ssh_config: %s", joined)
		}
	}
}

func TestSSHAuditWrappedChildSignalExitCode(t *testing.T) {
	err := runCLICommandWithSSHProxy([]string{"sh", "-c", "kill -TERM $$"}, 1080)
	var exitCode *cliExitCodeError
	if !errors.As(err, &exitCode) || exitCode.code != 143 {
		t.Fatalf("signal terminated child error = %v (%#v), want exit 143", err, exitCode)
	}
}

func TestSSHAuditCloseFlowFinalizesStalledUpstream(t *testing.T) {
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, err := upstream.Accept()
		if err != nil {
			return
		}
		if _, err := readCLIInboundSOCKS5(connection); err != nil {
			connection.Close()
			return
		}
		if err := writeCLISOCKS5Reply(connection, 0); err != nil {
			connection.Close()
			return
		}
		accepted <- connection
	}()
	publicPort := availableCLITestPort(t)
	relay := &cliSSHRelay{
		listenPort: publicPort, upstreamPort: upstream.Addr().(*net.TCPAddr).Port,
		controlPath: filepath.Join(t.TempDir(), "relay.sock"), startedAt: time.Now(),
		shutdown: make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() { done <- relay.run() }()
	defer func() { relay.stop(); <-done }()
	dialer, err := proxy.SOCKS5("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(publicPort)), nil, proxy.Direct)
	if err != nil {
		t.Fatal(err)
	}
	var client net.Conn
	deadline := time.Now().Add(time.Second)
	for client == nil && time.Now().Before(deadline) {
		client, _ = dialer.Dial("tcp", "example.test:443")
		if client == nil {
			time.Sleep(time.Millisecond)
		}
	}
	if client == nil {
		t.Fatal("relay never became ready")
	}
	defer client.Close()
	remote := <-accepted
	defer remote.Close()
	if _, err := client.Write([]byte("payload")); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadFull(remote, make([]byte, 7)); err != nil {
		t.Fatal(err)
	}
	if err := relay.closeFlows("all"); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		flows := relay.listFlows()
		if len(flows) == 1 && !flows[0].Active && relay.connections.Load() == 0 {
			if flows[0].Upload != 7 || flows[0].LastSeen.Before(flows[0].StartedAt) {
				t.Fatalf("closed flow lost final accounting: %+v", flows[0])
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("closed flow retained active upstream: flows=%+v active=%d", relay.listFlows(), relay.connections.Load())
}

func TestSSHAuditFailedSwitchPreservesReverseCaptureConfigAndDefault(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = t.TempDir()
	previousRelay, previousStart := startCLISSHRelayForOperation, startCLIPersistentSSHTunnelForOperation
	t.Cleanup(func() {
		cliRuntimeDirectoryOverride = previousRuntime
		startCLISSHRelayForOperation, startCLIPersistentSSHTunnelForOperation = previousRelay, previousStart
	})
	for _, name := range []string{"captured", "new"} {
		if err := addCLISSHProfile(cliSSHProfile{Name: name, Username: "user", Host: "example.invalid", Port: 22}); err != nil {
			t.Fatal(err)
		}
	}
	if err := setCLISSHDefault("captured"); err != nil {
		t.Fatal(err)
	}
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	go acceptCLITestSOCKS(upstream)
	startCLISSHRelayForOperation = func(state *cliSSHTunnelState) error { return startTestSSHRelay(t, state) }
	profile, err := loadCLISSHProfile("captured")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := attachCLISSHSocksTunnel(profile, upstream.Addr().(*net.TCPAddr).Port, true, true); err != nil {
		t.Fatal(err)
	}
	startCLIPersistentSSHTunnelForOperation = func(cliSSHProfile) (cliSSHTunnelState, error) {
		return cliSSHTunnelState{}, errors.New("intentional new connection failure")
	}
	if _, _, err := connectCLISSHProfile("new"); err == nil {
		t.Fatal("failed switch unexpectedly succeeded")
	}
	config, err := loadCLISSHConfig()
	if err != nil {
		t.Fatal(err)
	}
	_, exists := findCLISSHProfile(config.Profiles, "captured")
	if !exists || config.Default != "captured" {
		t.Fatalf("failed switch removed capture configuration/default: %+v", config)
	}
	state, active, err := activeCLIPersistentSSHTunnel()
	if err != nil || !active || !state.AutoCreated || !state.Reverse {
		t.Fatalf("old reverse capture was not restored: %+v active=%t err=%v", state, active, err)
	}
	if err := stopCLIStateTunnel(state); err != nil {
		t.Fatal(err)
	}
}

func TestSSHAuditCanceledConnectReclaimsPendingHelperAndAskpass(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = t.TempDir()
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previousRuntime })
	bin := t.TempDir()
	script := "#!/bin/sh\ncase \" $* \" in *' -G '*|*' -O '*) exit 0;; esac\n/bin/sleep 60\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if err := addCLISSHProfile(cliSSHProfile{Name: "pending", Username: "user", Host: "example.invalid", Port: 22, Password: "secret"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := connectCLISSHProfileWithCredentialsContext(ctx, "pending", cliSSHCredentials{})
		done <- err
	}()
	runtime, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		t.Fatal(err)
	}
	var pending cliSSHTunnelState
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && pending.HelperPID == 0 {
		entries, _ := os.ReadDir(runtime)
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), "persistent-") && strings.HasSuffix(entry.Name(), ".json") {
				pending, _ = loadCLISSHTunnelState(filepath.Join(runtime, entry.Name()))
			}
		}
		time.Sleep(time.Millisecond)
	}
	if pending.HelperPID == 0 || !pending.Pending || pending.AskpassPath == "" || pending.OwnerStart == "" {
		t.Fatalf("helper was not registered before authentication: %+v", pending)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled connection error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pending helper ignored frontend cancellation")
	}
	if cliSSHRecordedProcessAlive(pending.HelperPID, pending.HelperStart) {
		t.Fatal("pending SSH authentication helper was not reaped")
	}
	for _, path := range []string{pending.StatePath, pending.AskpassPath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("canceled helper left resource %s: %v", filepath.Base(path), err)
		}
	}
	if err := waitCLISSHPendingOperations(time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestSSHAuditHelperDeadlineKillsDescendantPipes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	command := exec.Command("sh", "-c", "sleep 60 & wait")
	started := time.Now()
	err := runCLISSHCommandContext(ctx, command, "test_ssh_timeout", false, nil)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > time.Second {
		t.Fatalf("blocked helper/pipes were not bounded: elapsed=%s err=%v", time.Since(started), err)
	}
}

func TestSSHAuditGuardianDetachesWithoutKillingExternalMaster(t *testing.T) {
	directory := t.TempDir()
	control := filepath.Join(directory, "external-control")
	if err := os.WriteFile(control, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(directory, "operations")
	t.Setenv("SSH_AUDIT_OPERATIONS", log)
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$SSH_AUDIT_OPERATIONS\"\nexit 0\n"
	if err := os.WriteFile(filepath.Join(directory, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	state := cliSSHTunnelState{
		Name: "captured", Destination: "user@example.invalid", Port: 1080,
		ControlPath: control, Kind: cliSSHAttachedKind, Pending: true,
		OwnerPID: os.Getpid(), OwnerStart: "invalid-reused-process-token",
		StatePath: filepath.Join(directory, "pending.json"),
	}
	if err := saveCLISSHTunnelState(state); err != nil {
		t.Fatal(err)
	}
	if err := runCLISSHPendingGuard(state.StatePath); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "-O cancel") || strings.Contains(string(data), "-O exit") {
		t.Fatalf("guardian affected external master ownership: %s", data)
	}
	if _, err := os.Stat(control); err != nil {
		t.Fatalf("guardian deleted external control socket: %v", err)
	}
	if _, err := os.Stat(state.StatePath); !os.IsNotExist(err) {
		t.Fatalf("guardian did not remove its own pending record: %v", err)
	}
}

func TestSSHAuditCanceledOperationDoesNotTouchCommittedTunnel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := connectCLISSHProfileWithCredentialsContext(ctx, "nonexistent", cliSSHCredentials{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("already-canceled connect started validation/mutation: %v", err)
	}
	state := cliSSHTunnelState{StatePath: filepath.Join(t.TempDir(), "persistent.json"), Pending: false, OwnerPID: os.Getpid(), OwnerStart: "old-frontend"}
	if err := saveCLISSHTunnelState(state); err != nil {
		t.Fatal(err)
	}
	if err := runCLISSHPendingGuard(state.StatePath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(state.StatePath); err != nil {
		t.Fatalf("guardian touched a committed persistent tunnel: %v", err)
	}
}

func TestSSHAuditGuardianProcessReclaimsAfterOwnerDeath(t *testing.T) {
	binary := os.Getenv("FLCLASH_SSH_GUARD_TEST_BINARY")
	if binary == "" {
		var err error
		binary, err = os.Executable()
		if err != nil {
			t.Fatal(err)
		}
	}
	directory := t.TempDir()
	control := filepath.Join(directory, "owned-master.sock")
	secret := filepath.Join(directory, "askpass-owned.secret")
	for _, path := range []string{control, secret} {
		if err := os.WriteFile(path, []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\ncase \" $* \" in *' -O exit '*) /bin/rm -- \"$SSH_AUDIT_CONTROL\"; exit $?;; *' -O check '*) test -e \"$SSH_AUDIT_CONTROL\"; exit $?;; esac\nexit 1\n"
	if err := os.WriteFile(filepath.Join(directory, "ssh"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	owner := exec.Command("/bin/sleep", "60")
	if err := owner.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = owner.Process.Kill(); _ = owner.Wait() }()
	helper := exec.Command("/bin/sleep", "60")
	helper.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = helper.Process.Kill(); _ = helper.Wait() }()
	ownerStart, err := linuxProcessStartTime(owner.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	helperStart, err := linuxProcessStartTime(helper.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	state := cliSSHTunnelState{
		Name: "pending", Destination: "user@example.invalid", Kind: "persistent", ControlPath: control,
		StatePath: filepath.Join(directory, "pending.json"), StartedAt: time.Now(), Pending: true,
		OwnerPID: owner.Process.Pid, OwnerStart: ownerStart, HelperPID: helper.Process.Pid, HelperStart: helperStart, AskpassPath: secret,
	}
	if err := saveCLISSHTunnelState(state); err != nil {
		t.Fatal(err)
	}
	guardian := exec.Command(binary, "_ssh_relay", "--guard-state", state.StatePath)
	guardian.Env = append(os.Environ(), "PATH="+directory, "SSH_AUDIT_CONTROL="+control)
	if err := guardian.Start(); err != nil {
		t.Fatal(err)
	}
	defer guardian.Process.Kill()
	done := make(chan error, 1)
	go func() { done <- guardian.Wait() }()
	_ = owner.Process.Kill()
	_ = owner.Wait()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("guardian failed after frontend death: %v", err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("guardian did not reclaim the abandoned operation")
	}
	if cliSSHRecordedProcessAlive(helper.Process.Pid, helperStart) {
		t.Fatal("guardian left the pending authentication helper running")
	}
	for _, path := range []string{control, secret, state.StatePath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("guardian left %s after frontend death: %v", filepath.Base(path), err)
		}
	}
}

func TestSSHAuditIdleCleanupDoesNotRaceTunnelOperation(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = t.TempDir()
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previousRuntime })
	profile := cliSSHProfile{Name: "captured", Username: "user", Host: "example.invalid", Port: 22}
	if err := addCLISSHProfile(profile); err != nil {
		t.Fatal(err)
	}
	directory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		t.Fatal(err)
	}
	state := cliSSHTunnelState{
		Name: profile.Name, Kind: cliSSHAttachedSOCKSKind, AutoCreated: true,
		UpstreamPort: availableCLITestPort(t), StartedAt: time.Now(),
		StatePath: filepath.Join(directory, cliSSHPersistentStateFile),
	}
	if err := saveCLISSHTunnelState(state); err != nil {
		t.Fatal(err)
	}
	lock, err := lockCLISSHTunnelOperation()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.release()
	observed, active, err := activeCLIPersistentSSHTunnel()
	if err != nil || !active || observed.Name != profile.Name {
		t.Fatalf("idle status did not preserve in-flight operation: %+v active=%t err=%v", observed, active, err)
	}
	if _, err := os.Stat(state.StatePath); err != nil {
		t.Fatalf("idle status unlinked state while a switch owned its path: %v", err)
	}
	if _, err := loadCLISSHProfile(profile.Name); err != nil {
		t.Fatalf("idle status deleted the switch's rollback profile: %v", err)
	}
	lock.release()
	if _, active, err := activeCLIPersistentSSHTunnel(); err != nil || active {
		t.Fatalf("dead tunnel was not cleaned once operation completed: active=%t err=%v", active, err)
	}
}

func TestSSHAuditDeleteConnectedAutoCaptureIsTransactional(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = t.TempDir()
	previousRelay := startCLISSHRelayForOperation
	t.Cleanup(func() {
		cliRuntimeDirectoryOverride = previousRuntime
		startCLISSHRelayForOperation = previousRelay
	})
	profile := cliSSHProfile{Name: "captured", Username: "user", Host: "example.invalid", Port: 22}
	if err := addCLISSHProfile(profile); err != nil {
		t.Fatal(err)
	}
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	go acceptCLITestSOCKS(upstream)
	startCLISSHRelayForOperation = func(state *cliSSHTunnelState) error { return startTestSSHRelay(t, state) }
	if _, err := attachCLISSHSocksTunnel(profile, upstream.Addr().(*net.TCPAddr).Port, true, true); err != nil {
		t.Fatal(err)
	}
	if err := deleteCLISSHProfile(profile.Name); err != nil {
		t.Fatalf("auto-created Capture was deleted twice: %v", err)
	}
	config, err := loadCLISSHConfig()
	if err != nil || len(config.Profiles) != 0 || config.Default != "" {
		t.Fatalf("delete left capture config/default behind: %+v err=%v", config, err)
	}
	if _, active, err := activeCLIPersistentSSHTunnel(); err != nil || active {
		t.Fatalf("delete left active tunnel behind: active=%t err=%v", active, err)
	}
	if err := probeCLISSHSOCKS(upstream.Addr().(*net.TCPAddr).Port, time.Second); err != nil {
		t.Fatalf("delete killed external SSH listener: %v", err)
	}
}
