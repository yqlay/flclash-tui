//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestSettingsSetChangesOnlyTheNamedField(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	var requests []tuiServiceRequest
	serveAlignService(t, func(request tuiServiceRequest) tuiServiceStatus {
		requests = append(requests, request)
		status := alignServiceStatus(7, configPath)
		if request.Action == "apply_settings" {
			status.Revision = 8
		}
		return status
	})
	if err := settingsCommand([]string{"set", "allow-lan", "on"}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].Action != "status" || requests[1].Action != "apply_settings" {
		t.Fatalf("requests = %+v", requestActions(requests))
	}
	settings := requests[1].Settings
	if settings == nil || !settings.AllowLAN || settings.IPv6 || settings.LogLevel != "info" || settings.Mode != "rule" || settings.TunEnabled {
		t.Fatalf("settings payload = %+v", settings)
	}
	if requests[1].ExpectedRevision == nil || *requests[1].ExpectedRevision != 7 || requests[1].ExpectedInstanceID != "align" {
		t.Fatalf("settings transaction = %+v", requests[1])
	}
}

func TestSettingsSetRejectsBadInputAndBusyTunScope(t *testing.T) {
	setupCLICommandTestDirectories(t)
	for _, args := range [][]string{
		{"set"}, {"set", "nope", "on"}, {"set", "allow-lan", "maybe"},
		{"set", "log-level", "trace"}, {"set", "tun-scope", "global"},
	} {
		if err := settingsCommand(args); err == nil || strings.Contains(err.Error(), "backend") {
			t.Errorf("%q should fail before the backend, got %v", args, err)
		}
	}
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	requested := true
	var sawSetTun bool
	serveAlignService(t, func(request tuiServiceRequest) tuiServiceStatus {
		if request.Action == "set_tun" {
			sawSetTun = true
		}
		status := alignServiceStatus(7, configPath)
		status.TunRequested = &requested
		status.TunState = "off"
		return status
	})
	err := settingsCommand([]string{"set", "tun-scope", "system"})
	if err == nil || !strings.Contains(err.Error(), "Turn TUN off before changing its scope") || sawSetTun {
		t.Fatalf("tun-scope while requested: err=%v set_tun=%v", err, sawSetTun)
	}
}

func TestSettingsConflictIsReturned(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	serveAlignService(t, func(request tuiServiceRequest) tuiServiceStatus {
		status := alignServiceStatus(7, configPath)
		if request.Action == "apply_settings" {
			status.OK = false
			status.ErrorCode = tuiServiceErrorConflict
			status.Error = "backend changed from revision 7 to 8; refresh and retry"
		}
		return status
	})
	err := settingsCommand([]string{"set", "log-level", "debug"})
	if err == nil || !strings.Contains(err.Error(), "revision 7 to 8") {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestProxyGroupDelayUsesTheSameSampler(t *testing.T) {
	var delays int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/proxies":
			_, _ = w.Write([]byte(`{"proxies":{"GROUP":{"type":"Selector","now":"a","all":["a","b"]}}}`))
		case strings.HasSuffix(r.URL.Path, "/delay"):
			delays++
			if strings.Contains(r.URL.Path, "/b/") {
				http.Error(w, "down", http.StatusBadGateway)
				return
			}
			_, _ = w.Write([]byte(`{"delay":42}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	output := captureCLIOutput(t, func() error {
		return proxyCommand([]string{"delay", "--group", "GROUP", "--json", "--controller", server.URL})
	})
	if delays != tuiDelayTestSamples+1 {
		t.Fatalf("delay requests = %d, want %d successful samples plus the failing node", delays, tuiDelayTestSamples)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(output), &rows); err != nil {
		t.Fatalf("stdout = %q: %v", output, err)
	}
	if len(rows) != 2 || rows[0]["node"] != "a" || rows[0]["samples"] != float64(tuiDelayTestSamples) || rows[1]["node"] != "b" || rows[1]["error"] == "" {
		t.Fatalf("group delay rows = %#v", rows)
	}
	setupCLICommandTestDirectories(t)
	if err := proxyCommand([]string{"delay", "--group", "missing", "--controller", server.URL}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing group error = %v", err)
	}
}

func TestProxyGroupSpeedRequiresManagedBackend(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = w.Write([]byte(`{"proxies":{"GROUP":{"all":["a"]}}}`))
	}))
	defer server.Close()
	setupCLICommandTestDirectories(t)
	err := proxyCommand([]string{"speed", "--group", "GROUP", "--controller", server.URL})
	if err == nil || !strings.Contains(err.Error(), "managed FlClash backend") || requests != 0 {
		t.Fatalf("external speed error=%v requests=%d", err, requests)
	}
}

func TestProxyGroupSpeedAndProviderUpdate(t *testing.T) {
	core := serveUnixHTTP(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/proxies" {
			_, _ = w.Write([]byte(`{"proxies":{"GROUP":{"type":"Selector","all":["n1","n2"]}}}`))
			return
		}
		http.NotFound(w, r)
	}))
	var speedNames []string
	unusedConfig := filepath.Join(t.TempDir(), "unused.yaml")
	serveAlignService(t, func(request tuiServiceRequest) tuiServiceStatus {
		status := alignServiceStatus(3, unusedConfig)
		status.CoreSocket = core
		if request.Action == "speed_proxy" {
			speedNames = append(speedNames, request.ProxyName)
			status.Speed = &tuiSpeedResult{Bytes: 100, DurationMillis: 1000, BytesPerSecond: 100, Complete: true}
		}
		return status
	})
	output := captureCLIOutput(t, func() error {
		return proxyCommand([]string{"speed", "--group", "GROUP", "--json"})
	})
	if strings.Join(speedNames, ",") != "n1,n2" {
		t.Fatalf("speed names = %v", speedNames)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(output), &rows); err != nil {
		t.Fatalf("stdout = %q: %v", output, err)
	}
	if len(rows) != 2 || rows[0]["node"] != "n1" || rows[1]["complete"] != true {
		t.Fatalf("group speed rows = %#v", rows)
	}

	var updated string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut {
			updated = r.URL.EscapedPath()
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"providers":{"b":{"name":"b","type":"Proxy","vehicle-type":"HTTP","proxies":[{}]},"a/b":{"type":"Proxy","vehicle-type":"File","proxies":[{},{}]}}}`))
	}))
	defer provider.Close()
	list := captureCLIOutput(t, func() error {
		return proxyCommand([]string{"providers", "--json", "--controller", provider.URL})
	})
	var providers []map[string]any
	if err := json.Unmarshal([]byte(list), &providers); err != nil {
		t.Fatalf("providers = %q: %v", list, err)
	}
	if len(providers) != 2 || providers[0]["name"] != "a/b" || providers[0]["count"] != float64(2) {
		t.Fatalf("provider list = %#v", providers)
	}
	if err := proxyCommand([]string{"provider", "update", "a/b", "--controller", provider.URL}); err != nil {
		t.Fatal(err)
	}
	if updated != "/providers/proxies/a%2Fb" {
		t.Fatalf("update path = %q", updated)
	}
}

func TestLogsClearExportAndTrafficReset(t *testing.T) {
	setupCLICommandTestDirectories(t)
	for _, args := range [][]string{{"clear", "extra"}, {"export", "extra"}} {
		if err := logsManagedCommand(args); err == nil || strings.Contains(err.Error(), "backend") {
			t.Errorf("%q should fail before the backend, got %v", args, err)
		}
	}
	if err := trafficCommand([]string{"reset", "extra"}); err == nil || strings.Contains(err.Error(), "backend") {
		t.Fatalf("traffic validation = %v", err)
	}

	homeDir := t.TempDir()
	var actions []string
	serveAlignService(t, func(request tuiServiceRequest) tuiServiceStatus {
		actions = append(actions, request.Action)
		status := alignServiceStatus(4, filepath.Join(homeDir, "config.yaml"))
		status.HomeDir = homeDir
		if request.Action != "status" && (request.ExpectedRevision == nil || *request.ExpectedRevision != 4) {
			status.OK = false
			status.Error = "missing revision"
		}
		if request.Action == "clear_logs" || request.Action == "reset_traffic" {
			status.Revision = 5
		}
		return status
	})
	cliLogMu.Lock()
	previousLogs := cliLogs
	cliLogs = []string{"secret"}
	cliLogMu.Unlock()
	t.Cleanup(func() {
		cliLogMu.Lock()
		cliLogs = previousLogs
		cliLogMu.Unlock()
	})
	if err := logsManagedCommand([]string{"clear"}); err != nil {
		t.Fatal(err)
	}
	cliLogMu.Lock()
	cleared := cliLogs == nil
	cliLogMu.Unlock()
	if !cleared {
		t.Fatal("local log ring was not cleared")
	}
	if err := os.WriteFile(filepath.Join(homeDir, tuiServiceLogFilename), []byte("backend line\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	exported := captureCLIOutput(t, func() error { return logsManagedCommand([]string{"export"}) })
	path := strings.TrimSpace(exported)
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), "backend line") {
		t.Fatalf("export %q: %v\n%s", path, err, data)
	}
	if err := trafficCommand([]string{"reset"}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(actions, ",") != "status,clear_logs,status,status,reset_traffic" {
		t.Fatalf("actions = %s", strings.Join(actions, ","))
	}
}

func alignServiceStatus(revision uint64, configPath string) tuiServiceStatus {
	return tuiServiceStatus{
		OK: true, Version: cliVersion, ProtocolVersion: tuiServiceProtocolVersion,
		Revision: revision, InstanceID: "align", ConfigPath: configPath,
		HomeDir: filepath.Dir(configPath), TunScope: tuiTunScopeUser, TunState: "off",
		Mode: tuiSilentMode,
	}
}

func serveAlignService(t *testing.T, handle func(tuiServiceRequest) tuiServiceStatus) {
	t.Helper()
	setupCLICommandTestDirectories(t)
	runtimeDir, err := cliRuntimeDirectory()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(runtimeDir, tuiServiceSocketFilename))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	var mu sync.Mutex
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var request tuiServiceRequest
			if json.NewDecoder(conn).Decode(&request) != nil {
				_ = conn.Close()
				continue
			}
			mu.Lock()
			status := handle(request)
			mu.Unlock()
			_ = json.NewEncoder(conn).Encode(status)
			_ = conn.Close()
		}
	}()
}

func serveUnixHTTP(t *testing.T, handler http.Handler) string {
	t.Helper()
	socketPath := filepath.Join(t.TempDir(), "core.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return socketPath
}

func requestActions(requests []tuiServiceRequest) []string {
	actions := make([]string, len(requests))
	for index, request := range requests {
		actions[index] = request.Action
	}
	return actions
}
