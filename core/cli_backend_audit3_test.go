//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func audit3BackendRuntime(t *testing.T, mode string, tun bool) *tuiServiceRuntime {
	t.Helper()
	r := newTestTUIServiceRuntime(t)
	text := "mixed-port: 0\nmode: " + mode + "\nrules:\n  - MATCH,DIRECT\ntun:\n  enable: "
	if tun {
		text += "true\n"
	} else {
		text += "false\n"
	}
	if err := os.WriteFile(r.paths.ConfigPath, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	r.configureManagedRuntimePolicy(mode, 0, 0, r.paths.ConfigPath, tuiFLCListenerState{}, tuiTunScopeUser, tun)
	t.Cleanup(r.closeCoreController)
	return r
}

func audit3RuntimeYAML(t *testing.T, r *tuiServiceRuntime) map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(r.actualConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]interface{}
	if err := yaml.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func audit3UseReloadStub(t *testing.T, failMode string) {
	t.Helper()
	previous := reloadTUIServiceActualConfig
	reloadTUIServiceActualConfig = func(_ string, _ string, path string, _ string, _ string, setup []byte, _ bool, options ...tuiConfigReloadOptions) ([]byte, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if failMode != "" && strings.Contains(string(data), "MATCH,"+strings.ToUpper(failMode)) {
			if len(options) > 0 && options[0].preparedTargetFD > 0 {
				_ = syscall.Close(options[0].preparedTargetFD)
			}
			return nil, errors.New("reload rejected")
		}
		return append([]byte(nil), setup...), nil
	}
	t.Cleanup(func() { reloadTUIServiceActualConfig = previous })
}

func TestAudit3NativeModeKeepsUIDGuardAndRestoresProfileRules(t *testing.T) {
	audit3UseReloadStub(t, "")
	r := audit3BackendRuntime(t, "rule", false)
	r.setRunning(true)
	for _, mode := range []string{"global", "direct", "rule"} {
		changed, err := r.applyTrafficMode(mode)
		if err != nil || !changed {
			t.Fatalf("mode %s: changed=%v err=%v", mode, changed, err)
		}
		value := audit3RuntimeYAML(t, r)
		rules := value["rules"].([]interface{})
		if value["mode"] != "rule" || !strings.Contains(rules[0].(string), "UID,") || len(rules) != 2 {
			t.Fatalf("managed mode %s lost guard: %v", mode, value)
		}
		want := "MATCH,DIRECT"
		if mode == "global" {
			want = "MATCH,GLOBAL"
		}
		if rules[1] != want || r.snapshot("").Mode != mode {
			t.Fatalf("mode %s route/status mismatch: rules=%v status=%+v", mode, rules, r.snapshot(""))
		}
	}
}

func TestAudit3NativeModeFailureRestoresProfileAndRuntime(t *testing.T) {
	audit3UseReloadStub(t, "global")
	r := audit3BackendRuntime(t, "rule", false)
	changed, err := r.applyTrafficMode("global")
	if err == nil || changed || loadTUIConfiguredSettings(r.paths.ConfigPath, true).Mode != "rule" || r.snapshot("").Mode != "rule" {
		t.Fatalf("mode rollback failed: changed=%v err=%v status=%+v", changed, err, r.snapshot(""))
	}
}

func TestAudit3ProfileReloadUsesTargetModeForRules(t *testing.T) {
	audit3UseReloadStub(t, "")
	r := audit3BackendRuntime(t, "direct", false)
	next := filepath.Join(r.paths.HomeDir, "next.yaml")
	if err := os.WriteFile(next, []byte("mode: global\nmixed-port: 0\nrules: [MATCH,DIRECT]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.reload(next); err != nil {
		t.Fatal(err)
	}
	value := audit3RuntimeYAML(t, r)
	if r.snapshot("").Mode != "global" || value["rules"].([]interface{})[1] != "MATCH,GLOBAL" {
		t.Fatalf("target profile mode mismatch: status=%+v config=%v", r.snapshot(""), value)
	}
}

func TestAudit3NativeStartupUsesConfiguredPolicyButSilentStaysSilent(t *testing.T) {
	for _, policy := range []string{"rule", "direct", "global"} {
		if actual := resolveTUIRuntimeTrafficMode(policy, &tuiSettings{Mode: "global"}); actual != "global" {
			t.Fatalf("native startup carried stale %s policy instead of profile global: %s", policy, actual)
		}
	}
	if actual := resolveTUIRuntimeTrafficMode(tuiSilentMode, &tuiSettings{Mode: "global"}); actual != tuiSilentMode {
		t.Fatalf("profile global bypassed silent policy: %s", actual)
	}
}

func TestAudit3StoppedTunIsIntentNotActiveLease(t *testing.T) {
	r := audit3BackendRuntime(t, "rule", true)
	if status := r.snapshot(""); status.TunState != "off" || status.TunOwnerPID != 0 || status.TunRequested == nil || !*status.TunRequested {
		t.Fatalf("stopped TUN presented active: %+v", status)
	}
	audit3UseReloadStub(t, "")
	if _, err := r.reload(""); err != nil {
		t.Fatalf("stopped TUN edit required lease: %v", err)
	}
	if value := audit3RuntimeYAML(t, r); value["tun"].(map[string]interface{})["enable"] != false {
		t.Fatalf("stopped runtime should not own TUN: %v", value)
	}
	if !r.tunEnabled || !loadTUIConfiguredSettings(r.paths.ConfigPath, true).TunEnabled {
		t.Fatal("stopped reload forgot TUN intent")
	}
}

func TestAudit3TunPreloadUsesLeaseBeforeListenersStart(t *testing.T) {
	audit3UseReloadStub(t, "")
	r := audit3BackendRuntime(t, "rule", true)
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	r.tunLease = &tuiTunLease{file: file, scope: tuiTunScopeUser}
	t.Cleanup(r.releaseTunLease)
	if _, err := r.reload(""); err != nil {
		t.Fatal(err)
	}
	value := audit3RuntimeYAML(t, r)
	tun := value["tun"].(map[string]interface{})
	if tun["enable"] != true || tun["file-descriptor"].(int) <= 0 || r.snapshot("").TunState != "off" {
		t.Fatalf("preload failed: tun=%v status=%+v", tun, r.snapshot(""))
	}
	// The real Core owns the duplicate on success; this stub must close it.
	_ = syscall.Close(tun["file-descriptor"].(int))
}

func TestAudit3HistoryCountIsLightweightAndNotRevisionMutation(t *testing.T) {
	r := newTestTUIServiceRuntime(t)
	before := r.snapshot("")
	r.recordHistoryUpdate([]tuiRequest{{TuiConnection: tuiConnection{ID: "old"}, FirstSeen: time.Now(), LastSeen: time.Now()}})
	after := r.snapshot("")
	if after.HistoryCount == nil || *after.HistoryCount != 1 || after.Revision != before.Revision || tuiServiceStateChanged(before, after) || len(after.History) != 0 {
		t.Fatalf("history count should be cached and non-mutating: before=%+v after=%+v", before, after)
	}
	data, err := json.Marshal(after)
	if err != nil || !strings.Contains(string(data), `"history_count":1`) {
		t.Fatalf("optional count JSON: %s err=%v", data, err)
	}
	var old tuiServiceStatus
	if err := json.Unmarshal([]byte(`{"ok":true}`), &old); err != nil || old.HistoryCount != nil {
		t.Fatalf("old backend must remain unknown: %+v %v", old, err)
	}
}

func TestAudit3UpgradeRefusesOtherExplicitConfigBeforeShutdown(t *testing.T) {
	runtimeDir := t.TempDir()
	previous := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = runtimeDir
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previous })
	listener, err := net.Listen("unix", filepath.Join(runtimeDir, tuiServiceSocketFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var mutations atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			var request tuiServiceRequest
			_ = json.NewDecoder(conn).Decode(&request)
			status := tuiServiceStatus{OK: true, ProtocolVersion: tuiServiceProtocolVersion, Version: "0.5.1", HomeDir: runtimeDir, ConfigPath: filepath.Join(runtimeDir, "original.yaml")}
			if request.Action != "status" {
				mutations.Add(1)
				status.OK = false
				status.Error = "test rejects all modifications"
			}
			_ = json.NewEncoder(conn).Encode(status)
			_ = conn.Close()
		}
	}()
	_, _, err = ensureTUIService(cliPaths{HomeDir: runtimeDir, ConfigPath: filepath.Join(runtimeDir, "another.yaml")}, defaultCLITestURL, true, false)
	_ = listener.Close()
	<-done
	if err == nil || !strings.Contains(err.Error(), "already using") || mutations.Load() != 0 {
		t.Fatalf("upgrade changed a different explicit target: err=%v mutations=%d", err, mutations.Load())
	}
}

func TestAudit3TunStartPreloadsLeaseAndStopRetainsIntent(t *testing.T) {
	audit3UseReloadStub(t, "")
	r := audit3BackendRuntime(t, "rule", true)
	oldAcquire := acquireTUIServiceTunLease
	oldStart, oldStop := startTUIServiceCoreListeners, stopTUIServiceCoreListeners
	t.Cleanup(func() {
		acquireTUIServiceTunLease = oldAcquire
		startTUIServiceCoreListeners, stopTUIServiceCoreListeners = oldStart, oldStop
	})
	var duplicate int
	acquireTUIServiceTunLease = func(scope string) (*tuiTunLease, tuiTunHelperResponse, error) {
		file, err := os.Open("/dev/null")
		if err != nil {
			return nil, tuiTunHelperResponse{}, err
		}
		return &tuiTunLease{scope: scope, file: file}, tuiTunHelperResponse{OK: true}, nil
	}
	startTUIServiceCoreListeners = func() bool {
		value := audit3RuntimeYAML(t, r)
		tun := value["tun"].(map[string]interface{})
		if tun["enable"] != true || r.running {
			t.Fatalf("TUN not preloaded before StartListener: config=%v running=%v", value, r.running)
		}
		duplicate = tun["file-descriptor"].(int)
		return true
	}
	stopTUIServiceCoreListeners = func() bool { return true }
	if changed, err := r.startCoreListeners(); err != nil || !changed {
		t.Fatalf("start TUN: changed=%v err=%v", changed, err)
	}
	defer syscall.Close(duplicate)
	if status := r.snapshot(""); status.TunState != "on" || status.TunOwnerPID == 0 {
		t.Fatalf("started TUN missing owner: %+v", status)
	}
	if changed, err := r.stopCoreAndProxy(r.snapshot("")); err != nil || !changed {
		t.Fatalf("stop TUN: changed=%v err=%v", changed, err)
	}
	if status := r.snapshot(""); status.TunState != "off" || status.TunOwnerPID != 0 || r.tunLease != nil || !r.tunEnabled {
		t.Fatalf("stop lost desired TUN or retained lease: %+v", status)
	}
	settings := *loadTUIConfiguredSettings(r.paths.ConfigPath, true)
	settings.IPv6 = !settings.IPv6
	if _, err := r.applySettings(settings); err != nil {
		t.Fatalf("stopped TUN settings failed: %v", err)
	}
	if !r.tunEnabled || audit3RuntimeYAML(t, r)["tun"].(map[string]interface{})["enable"] != false {
		t.Fatal("stopped edit forgot desired TUN or applied it without a lease")
	}
}

func TestAudit3RunningSettingsAcquireAndReleaseUserTun(t *testing.T) {
	audit3UseReloadStub(t, "")
	r := audit3BackendRuntime(t, "rule", false)
	r.setRunning(true)
	previousAcquire := acquireTUIServiceTunLease
	t.Cleanup(func() { acquireTUIServiceTunLease = previousAcquire })
	var grantedFile *os.File
	acquireTUIServiceTunLease = func(scope string) (*tuiTunLease, tuiTunHelperResponse, error) {
		var err error
		grantedFile, err = os.Open("/dev/null")
		return &tuiTunLease{scope: scope, file: grantedFile}, tuiTunHelperResponse{OK: err == nil}, err
	}
	settings := *loadTUIConfiguredSettings(r.paths.ConfigPath, true)
	settings.TunEnabled = true
	if _, err := r.applySettings(settings); err != nil {
		t.Fatalf("settings enable TUN: %v", err)
	}
	duplicate := audit3RuntimeYAML(t, r)["tun"].(map[string]interface{})["file-descriptor"].(int)
	defer syscall.Close(duplicate)
	if status := r.snapshot(""); status.TunState != "on" || r.tunLease == nil {
		t.Fatalf("running settings did not acquire lease: %+v", status)
	}
	settings.TunEnabled = false
	if _, err := r.applySettings(settings); err != nil {
		t.Fatalf("settings disable TUN: %v", err)
	}
	if status := r.snapshot(""); status.TunState != "off" || r.tunLease != nil || r.tunEnabled || *status.TunRequested {
		t.Fatalf("running settings retained lease: %+v", status)
	}
	if _, err := grantedFile.Stat(); err == nil {
		t.Fatal("disabled TUN lease FD was left open")
	}
}

func TestAudit3FailedRunningTunReloadReleasesNewLease(t *testing.T) {
	audit3UseReloadStub(t, "global")
	r := audit3BackendRuntime(t, "rule", false)
	r.setRunning(true)
	previousAcquire := acquireTUIServiceTunLease
	t.Cleanup(func() { acquireTUIServiceTunLease = previousAcquire })
	var grantedFile *os.File
	acquireTUIServiceTunLease = func(scope string) (*tuiTunLease, tuiTunHelperResponse, error) {
		var err error
		grantedFile, err = os.Open("/dev/null")
		return &tuiTunLease{scope: scope, file: grantedFile}, tuiTunHelperResponse{OK: err == nil}, err
	}
	settings := *loadTUIConfiguredSettings(r.paths.ConfigPath, true)
	settings.Mode = "global"
	settings.TunEnabled = true
	changed, err := r.applySettings(settings)
	if err == nil || changed || r.tunLease != nil || r.tunEnabled || r.snapshot("").Mode != "rule" {
		t.Fatalf("failed settings retained TUN lease: changed=%v err=%v status=%+v", changed, err, r.snapshot(""))
	}
	if grantedFile == nil {
		t.Fatal("running settings did not attempt to acquire a lease")
	}
	if _, err := grantedFile.Stat(); err == nil {
		t.Fatal("failed reload leaked newly acquired lease FD")
	}
	if settings := loadTUIConfiguredSettings(r.paths.ConfigPath, true); settings.Mode != "rule" || settings.TunEnabled {
		t.Fatalf("failed settings changed saved configuration: %+v", settings)
	}
}

type audit3RollbackHub struct {
	cliHubCore
	path       string
	target     string
	rollbackFD int
	initFails  bool
}

func (h *audit3RollbackHub) Init(params string) bool {
	var value InitParams
	_ = json.Unmarshal([]byte(params), &value)
	h.path = value.ConfigPath
	return !(h.initFails && h.path != h.target)
}

func (h *audit3RollbackHub) ValidateConfig(string) string { return "" }
func (h *audit3RollbackHub) StartListener() bool          { return true }
func (h *audit3RollbackHub) StopListener() bool           { return true }
func (h *audit3RollbackHub) SetupConfig([]byte) string {
	if h.path == h.target {
		return "simulated configuration failure"
	}
	data, err := os.ReadFile(h.path)
	if err != nil {
		return err.Error()
	}
	var value map[string]interface{}
	_ = yaml.Unmarshal(data, &value)
	h.rollbackFD = value["tun"].(map[string]interface{})["file-descriptor"].(int)
	// Simulate Core taking ownership without any real TUN/network changes.
	_ = syscall.Close(h.rollbackFD)
	return ""
}

func TestAudit3TunRollbackRefreshesFDWithoutClosingReusedInteger(t *testing.T) {
	r := audit3BackendRuntime(t, "rule", true)
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	lease := &tuiTunLease{file: file}
	defer lease.release()
	staleFD, err := lease.duplicateFD()
	if err != nil {
		t.Fatal(err)
	}
	previousPath, err := writeTUIManagedRuntimeConfig(r.paths, "rule", 0, true, tuiTunScopeUser, staleFD)
	if err != nil {
		t.Fatal(err)
	}
	_ = syscall.Close(staleFD)
	sentinel, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer sentinel.Close()
	targetPath := r.paths.ConfigPath
	hub := &audit3RollbackHub{target: targetPath}
	previousHub := cliHub
	cliHub = hub
	t.Cleanup(func() { cliHub = previousHub })
	_, err = reloadTUIActualConfig(r.paths.HomeDir, previousPath, targetPath, defaultCLITestURL, r.coreSocket, nil, true, tuiConfigReloadOptions{rollbackTunLease: lease})
	if err == nil || !strings.Contains(err.Error(), "simulated configuration failure") || hub.rollbackFD <= 0 || hub.rollbackFD == int(sentinel.Fd()) {
		t.Fatalf("TUN rollback reused stale FD: old=%d sentinel=%d rollback=%d err=%v", staleFD, sentinel.Fd(), hub.rollbackFD, err)
	}
	if _, err := sentinel.Stat(); err != nil {
		t.Fatalf("rollback closed unrelated file occupying old FD: %v", err)
	}
}

func TestAudit3TunRollbackClosesDuplicateBeforeFailedInit(t *testing.T) {
	r := audit3BackendRuntime(t, "rule", true)
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	lease := &tuiTunLease{file: file}
	defer lease.release()
	fd, err := lease.duplicateFD()
	if err != nil {
		t.Fatal(err)
	}
	previousPath, err := writeTUIManagedRuntimeConfig(r.paths, "rule", 0, true, tuiTunScopeUser, fd)
	_ = syscall.Close(fd)
	if err != nil {
		t.Fatal(err)
	}
	hub := &audit3RollbackHub{target: r.paths.ConfigPath, initFails: true}
	previousHub := cliHub
	cliHub = hub
	t.Cleanup(func() { cliHub = previousHub })
	_, err = reloadTUIActualConfig(r.paths.HomeDir, previousPath, r.paths.ConfigPath, defaultCLITestURL, r.coreSocket, nil, true, tuiConfigReloadOptions{rollbackTunLease: lease})
	if err == nil || !strings.Contains(err.Error(), "rollback failed") {
		t.Fatalf("expected rejected rollback initialization: %v", err)
	}
	data, err := os.ReadFile(previousPath)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]interface{}
	_ = yaml.Unmarshal(data, &value)
	fd = value["tun"].(map[string]interface{})["file-descriptor"].(int)
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("duplicate handed to failed Init was not closed: fd=%d err=%v", fd, err)
	}
}

func TestAudit3TunRollbackNeverRewritesUserProfile(t *testing.T) {
	r := audit3BackendRuntime(t, "rule", true)
	before, err := os.ReadFile(r.paths.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if fd, err := refreshTUIManagedRuntimeTunFD(r.paths.ConfigPath, nil); err != nil || fd != 0 {
		t.Fatalf("user profile should not duplicate a lease: fd=%d err=%v", fd, err)
	}
	after, err := os.ReadFile(r.paths.ConfigPath)
	if err != nil || string(after) != string(before) {
		t.Fatalf("TUN rollback changed user profile: before=%s after=%s err=%v", before, after, err)
	}
}

func TestAudit3TunModeSaveFailureRetainsLeaseForRollback(t *testing.T) {
	audit3UseReloadStub(t, "")
	r := audit3BackendRuntime(t, "rule", true)
	r.setRunning(true)
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	lease := &tuiTunLease{file: file}
	r.tunLease = lease
	t.Cleanup(r.releaseTunLease)
	if err := os.Mkdir(filepath.Join(r.paths.HomeDir, tuiStateFilename), 0o700); err != nil {
		t.Fatal(err)
	}
	changed, err := r.applyTrafficMode(tuiSilentMode)
	if err == nil || changed || !strings.Contains(err.Error(), "save silent mode") {
		t.Fatalf("mode save should fail: changed=%v err=%v", changed, err)
	}
	if status := r.snapshot(""); status.Mode != "rule" || status.TunState != "on" || r.tunLease != lease || r.rollbackTunLease != nil {
		t.Fatalf("mode failure lost original TUN: %+v", status)
	}
	if _, err := file.Stat(); err != nil {
		t.Fatalf("mode transaction released lease before it could roll back: %v", err)
	}
	duplicate := audit3RuntimeYAML(t, r)["tun"].(map[string]interface{})["file-descriptor"].(int)
	_ = syscall.Close(duplicate)
}

func TestAudit3TunPreparedTargetFDClosesBeforeValidationFailure(t *testing.T) {
	r := audit3BackendRuntime(t, "rule", false)
	file, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	lease := &tuiTunLease{file: file}
	defer lease.release()
	fd, err := lease.duplicateFD()
	if err != nil {
		t.Fatal(err)
	}
	_, err = reloadTUIActualConfig(r.paths.HomeDir, r.paths.ConfigPath, filepath.Join(r.paths.HomeDir, "missing.yaml"), defaultCLITestURL, r.coreSocket, nil, false, tuiConfigReloadOptions{preparedTargetFD: fd})
	if err == nil {
		t.Fatal("missing target validation should fail")
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(fd, &stat); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("prepared target leaked before ownership transfer: fd=%d err=%v", fd, err)
	}
}

func TestAudit3TunPreloadFailureClosesUnconsumedFD(t *testing.T) {
	r := audit3BackendRuntime(t, "rule", true)
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	port := occupied.Addr().(*net.TCPAddr).Port
	settings := *loadTUIConfiguredSettings(r.paths.ConfigPath, true)
	settings.MixedPort = port
	if err := persistTUISettings(r.paths.ConfigPath, settings); err != nil {
		t.Fatal(err)
	}
	r.configuredPort, r.runtimePort = port, port
	previousAcquire, previousReload := acquireTUIServiceTunLease, reloadTUIServiceActualConfig
	t.Cleanup(func() {
		acquireTUIServiceTunLease, reloadTUIServiceActualConfig = previousAcquire, previousReload
	})
	var grantedFile *os.File
	acquireTUIServiceTunLease = func(scope string) (*tuiTunLease, tuiTunHelperResponse, error) {
		var err error
		grantedFile, err = os.Open("/dev/null")
		return &tuiTunLease{file: grantedFile, scope: scope}, tuiTunHelperResponse{OK: err == nil}, err
	}
	var bound []net.Listener
	var prepared []int
	defer func() {
		for _, listener := range bound {
			_ = listener.Close()
		}
	}()
	reloadTUIServiceActualConfig = func(_ string, _ string, path string, _ string, _ string, setup []byte, running bool, options ...tuiConfigReloadOptions) ([]byte, error) {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var value map[string]interface{}
		_ = yaml.Unmarshal(data, &value)
		listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", value["mixed-port"].(int)))
		if err != nil {
			return nil, err
		}
		bound = append(bound, listener)
		if running || len(options) == 0 || options[0].preparedTargetFD == 0 {
			t.Fatal("expected a stopped TUN preload")
		}
		prepared = append(prepared, options[0].preparedTargetFD)
		options[0].acceptPendingTunFD(options[0].preparedTargetFD)
		return setup, nil
	}
	changed, err := r.startCoreListeners()
	if err == nil || changed || len(prepared) != 2 || r.running || r.tunLease != nil || r.pendingTunFD != 0 || !r.tunEnabled {
		t.Fatalf("failed stopped preload cleanup: changed=%v err=%v prepared=%v lease=%v pending=%d", changed, err, prepared, r.tunLease, r.pendingTunFD)
	}
	if _, err := grantedFile.Stat(); err == nil {
		t.Fatal("failed preload retained original lease FD")
	}
	var stat syscall.Stat_t
	if err := syscall.Fstat(prepared[len(prepared)-1], &stat); !errors.Is(err, syscall.EBADF) {
		t.Fatalf("unconsumed preload FD remained open: %v", err)
	}
}

func TestAudit3ManagedSelectionAndFLCUseExactSpacedIdentity(t *testing.T) {
	const group, node = "  Group / 组  ", "  Node / 节点  "
	r := audit3BackendRuntime(t, "rule", false)
	var selectedGroup, selectedNode string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPut {
			selectedGroup = strings.TrimPrefix(request.URL.Path, "/proxies/")
			var value struct {
				Name string `json:"name"`
			}
			_ = json.NewDecoder(request.Body).Decode(&value)
			selectedNode = value.Name
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"proxies": map[string]any{group: map[string]any{
			"type": "Selector", "now": "DIRECT", "all": []string{node, "DIRECT"},
		}}})
	}))
	defer server.Close()
	r.coreController = controllerClient{options: controllerOptions{address: server.URL}, client: server.Client()}
	changed, err := r.selectProxy(group, node)
	if err != nil || !changed || selectedGroup != group || selectedNode != node {
		t.Fatalf("managed selection normalized identity: changed=%v err=%v group=%q node=%q", changed, err, selectedGroup, selectedNode)
	}
	if saved := loadTUISelectedProxies(r.paths.HomeDir)[group]; saved != node {
		t.Fatalf("saved node normalized: %q", saved)
	}
	if saved := loadTUIFLCOutbound(r.paths.HomeDir); saved != group || r.snapshot("").FLCOutbound != group {
		t.Fatalf("saved/runtime FLC normalized: saved=%q runtime=%q", saved, r.snapshot("").FLCOutbound)
	}
	state, err := newTUIFLCListenerStateAtPort(loadTUIFLCOutbound(r.paths.HomeDir), 17659)
	if err != nil || state.Outbound != group {
		t.Fatalf("listener normalized group: state=%+v err=%v", state, err)
	}
	path, err := writeTUISilentRuntimeConfig(r.paths, state)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]interface{}
	_ = yaml.Unmarshal(data, &value)
	listener := value["listeners"].([]interface{})[0].(map[string]interface{})
	if listener["proxy"] != group {
		t.Fatalf("runtime listener identity changed: %q", listener["proxy"])
	}
	r.trafficMode = tuiSilentMode
	r.flc = state
	if changed, err := r.repairFLCOutbound(); err != nil || changed || r.flc.Outbound != group {
		t.Fatalf("saved spaced FLC was wrongly repaired: changed=%v err=%v outbound=%q", changed, err, r.flc.Outbound)
	}
}

func TestAudit3FLCEmptyValidationStillRejectsWhitespace(t *testing.T) {
	r := audit3BackendRuntime(t, "rule", false)
	for _, empty := range []string{"", "  ", "\t\n"} {
		if _, err := r.selectProxy(empty, "DIRECT"); err == nil {
			t.Fatalf("empty proxy group accepted: %q", empty)
		}
		if _, err := r.applyFLCOutbound(empty); err == nil {
			t.Fatalf("empty FLC group accepted: %q", empty)
		}
		if _, err := newTUIFLCListenerStateAtPort(empty, 17659); err == nil {
			t.Fatalf("empty FLC listener accepted: %q", empty)
		}
		if err := rememberTUIFLCOutbound(r.paths.HomeDir, empty); err == nil {
			t.Fatalf("empty saved FLC accepted: %q", empty)
		}
	}
}
