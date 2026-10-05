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
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAuditCoreStopPreservesIndependentSSH(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	model.snapshot.Connections = []tuiConnection{
		{ID: "proxy-1", Source: tuiTrafficSourceProxy},
		{ID: "ssh:1-1", Source: tuiTrafficSourceSSH},
	}
	model.snapshot.SelectedConnection = 1
	model.snapshot.ConnectionsDetailOpen = true
	model.snapshot.Requests = []tuiRequest{
		{TuiConnection: model.snapshot.Connections[0], Active: true},
		{TuiConnection: model.snapshot.Connections[1], Active: true},
	}
	model.reconcileStoppedCoreState()
	if len(model.snapshot.Connections) != 1 || model.snapshot.Connections[0].ID != "ssh:1-1" ||
		model.snapshot.SelectedConnection != 0 || !model.snapshot.ConnectionsDetailOpen {
		t.Fatal("stopping Mihomo discarded the independent SSH connection or selection")
	}
	if model.snapshot.Requests[0].Active || !model.snapshot.Requests[1].Active {
		t.Fatal("Core stop must complete only proxy history")
	}
}

func TestAuditSourceScopedHistoryClearPreservesOtherSource(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.snapshot.Page = tuiPageRequests
	model.snapshot.TrafficSource = tuiTrafficSourceSSH
	model.snapshot.Requests = []tuiRequest{
		{TuiConnection: tuiConnection{ID: "proxy-1", Source: tuiTrafficSourceProxy}},
		{TuiConnection: tuiConnection{ID: "ssh:1-1", Source: tuiTrafficSourceSSH}},
	}
	model.dangerConfirmed = true
	model.handleKey(tuiKeyCloseConnections)
	if len(model.snapshot.Requests) != 1 || model.snapshot.Requests[0].ID != "proxy-1" {
		t.Fatal("clearing SSH history removed proxy history")
	}
}

func TestAuditCloseConnectionRejectsWrongSource(t *testing.T) {
	useTestCLIRuntimeDirectory(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"connections":[{"id":"proxy-1","metadata":{"uid":1001}}]}`))
		} else {
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	client := controllerClient{options: controllerOptions{address: server.URL}, client: server.Client()}
	for _, item := range []struct{ id, source string }{
		{"proxy-1", tuiTrafficSourceSSH}, {"ssh:1-1", tuiTrafficSourceProxy},
	} {
		err := closeTUIVisibleConnectionsForSource(client, 1001, false, item.id, item.source)
		if err == nil || !strings.Contains(err.Error(), "source") {
			t.Errorf("wrong-source close %s/%s = %v", item.id, item.source, err)
		}
	}
	if calls != 0 {
		t.Fatal("wrong-source close contacted Mihomo")
	}
}

func TestAuditLateWatchCannotRollBackRevision(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	model.backendRevision = 12
	model.coreRunning = true
	model.snapshot.Settings.Mode = "rule"
	model.update(tuiServiceWatchMsg{status: tuiServiceStatus{Revision: 11, Mode: tuiSilentMode}})
	if model.backendRevision != 12 || !model.coreRunning || model.snapshot.Settings.Mode != "rule" {
		t.Fatal("late watch rolled back authoritative backend state")
	}
}

func TestAuditNotificationKeepsBackendLogs(t *testing.T) {
	cliLogMu.Lock()
	previous := append([]string(nil), cliLogs...)
	cliLogs = nil
	cliLogMu.Unlock()
	t.Cleanup(func() {
		cliLogMu.Lock()
		cliLogs = previous
		cliLogMu.Unlock()
	})
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	model.snapshot.Logs = []string{"backend log to retain"}
	model.enqueueNotification(tuiNotification{level: tuiNotificationError, message: "operation failed"})
	if !strings.Contains(strings.Join(model.snapshot.Logs, "\n"), "backend log to retain") {
		t.Fatal("notification overwrote backend logs")
	}
}

func TestAuditConfirmationUsesCapturedIDAfterRowDisappears(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	model.service = newTUIServiceClientAt(t.TempDir())
	model.snapshot.Page = tuiPageConnections
	model.snapshot.Connections = []tuiConnection{{ID: "connection-1"}}
	model.snapshot.SelectedConnection = 0
	model.handleKey(tuiKeyCloseConnection)
	model.snapshot.Connections = nil
	model.snapshot.SelectedConnection = -1
	if command := model.handleDangerConfirm(tea.KeyMsg{Type: tea.KeyEnter}); command == nil {
		t.Fatal("confirmation silently depended on the now-missing selected row")
	}
}

func TestAuditBackendSupportsTrafficReset(t *testing.T) {
	runtime := newTestTUIServiceRuntime(t)
	status := runtime.handle(tuiServiceRequest{Action: "reset_traffic", RequestID: "reset"})
	if !status.OK || status.Revision <= 1 {
		t.Fatalf("backend traffic reset unavailable: %+v", status)
	}
}

func TestAuditClosedSSHHistoryKeepsFinalCounters(t *testing.T) {
	now := time.Now()
	history := []tuiRequest{{
		TuiConnection: tuiConnection{ID: "ssh:1-1", Source: tuiTrafficSourceSSH, Download: 1},
		FirstSeen:     now.Add(-time.Minute), LastSeen: now.Add(-time.Second),
	}}
	updated := rememberClosedSSHHistory(history, []tuiRequest{
		{TuiConnection: tuiConnection{ID: "ssh:1-1", Source: tuiTrafficSourceSSH, Download: 100}},
	}, now)
	if len(updated) != 1 || updated[0].Download != 100 {
		t.Fatal("completed SSH flow lost bytes transferred since the last active poll")
	}
}

func TestAuditSSHClearCutoffPersistsAndFiltersRelayReplay(t *testing.T) {
	useTestCLIRuntimeDirectory(t)
	runtime := newTestTUIServiceRuntime(t)
	old := time.Now().Add(-time.Minute)
	if changed, err := runtime.clearPersistentHistoryForSource(tuiTrafficSourceSSH); err != nil || !changed {
		t.Fatalf("clear empty history with pending relay records = %t, %v", changed, err)
	}
	restored := newTUIServiceRuntime(runtime.paths, defaultCLITestURL, "", nil, nil)
	if err := restored.restoreHistory(); err != nil {
		t.Fatal(err)
	}
	if restored.sshHistoryClearedBefore.IsZero() {
		t.Fatal("clear cutoff lost on restart")
	}
	_, recent := splitCLISSHRelayConnections([]cliSSHRelayFlow{
		{ID: "ssh:old", StartedAt: old.Add(-time.Minute), LastSeen: old},
		{ID: "ssh:new", StartedAt: old, LastSeen: time.Now().Add(time.Second), Download: 123},
	})
	for range 3 {
		restored.history = rememberClosedSSHHistory(restored.history, recent, time.Now(), restored.sshHistoryClearedBefore)
	}
	if len(restored.history) != 1 || restored.history[0].ID != "ssh:new" || restored.history[0].Download != 123 || !restored.history[0].FirstSeen.Equal(old) {
		t.Fatalf("replayed history = %+v", restored.history)
	}
}

func TestAuditSSHCompletionSortedBeforeHistoryLimit(t *testing.T) {
	now := time.Now()
	history := make([]tuiRequest, tuiRequestHistoryLimit)
	for index := range history {
		history[index] = tuiRequest{TuiConnection: tuiConnection{ID: fmt.Sprint(index)}, FirstSeen: now.Add(-time.Hour), LastSeen: now.Add(-time.Minute)}
	}
	_, recent := splitCLISSHRelayConnections([]cliSSHRelayFlow{{ID: "ssh:new", StartedAt: now.Add(-time.Second), LastSeen: now}})
	updated := rememberClosedSSHHistory(history, recent, now)
	if len(updated) != tuiRequestHistoryLimit || updated[0].ID != "ssh:new" {
		t.Fatal("new closed SSH flow was evicted before sorting")
	}
}

func TestAuditHistoryClearFailureRollsBackCutoff(t *testing.T) {
	runtime := newTestTUIServiceRuntime(t)
	if err := os.Mkdir(filepath.Join(runtime.paths.HomeDir, tuiHistoryFilename), 0o700); err != nil {
		t.Fatal(err)
	}
	runtime.history = []tuiRequest{{TuiConnection: tuiConnection{ID: "ssh:old"}}}
	if _, err := runtime.clearPersistentHistory(); err == nil {
		t.Fatal("expected persistence failure")
	}
	if !runtime.sshHistoryClearedBefore.IsZero() || len(runtime.history) != 1 {
		t.Fatal("failed clear did not roll back")
	}
}

func TestAuditSnapshotCommandsDoNotShareMutableState(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	model.snapshot.Groups = []tuiGroup{{Name: "group", Nodes: []string{"node"}, Delays: map[string]tuiDelayResult{"node": {Samples: 1}}}}
	model.snapshot.Connections = []tuiConnection{{ID: "original"}}
	command := model.startOperation(func(state *tuiOperationState) {
		state.snapshot.Groups[0].Nodes[0] = "changed"
		state.snapshot.Groups[0].Delays["node"] = tuiDelayResult{}
		state.snapshot.Connections[0].ID = "changed"
	})
	_ = command()
	if model.snapshot.Groups[0].Nodes[0] != "node" || model.snapshot.Groups[0].Delays["node"].Samples != 1 || model.snapshot.Connections[0].ID != "original" {
		t.Fatal("background operation mutated the live snapshot before its result")
	}
}

func TestAuditStaleRefreshAndOperationKeepNewState(t *testing.T) {
	for _, kind := range []string{"refresh", "operation"} {
		t.Run(kind, func(t *testing.T) {
			model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
			model.backendInstanceID, model.backendRevision = "current", 12
			model.coreRunning = true
			model.snapshot.Settings.Mode = "rule"
			if kind == "refresh" {
				model.update(tuiRefreshResultMsg{serviceStatus: &tuiServiceStatus{InstanceID: "current", Revision: 11}})
			} else {
				model.update(tuiOperationResultMsg{state: tuiOperationState{backendInstanceID: "current", backendRevision: 11}})
			}
			if model.backendRevision != 12 || !model.coreRunning || model.snapshot.Settings.Mode != "rule" {
				t.Fatal("outdated result replaced current state")
			}
		})
	}
}

func TestAuditBackendRestartRejectsOldInstanceAndOldGeneration(t *testing.T) {
	runtime := newTestTUIServiceRuntime(t)
	revision := uint64(1)
	status := runtime.handle(tuiServiceRequest{Action: "reset_traffic", ExpectedRevision: &revision, ExpectedInstanceID: "old", RequestID: "old-instance"})
	if status.OK || status.ErrorCode != tuiServiceErrorConflict || status.Revision != revision {
		t.Fatal("old instance mutation was accepted")
	}
	start := time.Now()
	watch := runtime.watch(tuiServiceRequest{ExpectedInstanceID: "old", AfterRevision: 99, WatchTimeoutMS: 1000})
	if watch.InstanceID != runtime.instanceID || time.Since(start) > 500*time.Millisecond {
		t.Fatal("watch did not detect a replacement Backend promptly")
	}
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	model.backendInstanceID, model.backendRevision = "old", 99
	model.update(tuiBackendHandshakeMsg{status: tuiServiceStatus{InstanceID: "new", Revision: 1, Mode: "rule", Running: true}})
	model.update(tuiServiceWatchMsg{status: tuiServiceStatus{InstanceID: "old", Revision: 100, ShuttingDown: true}})
	if model.backendInstanceID != "new" || model.backendRevision != 1 || !model.coreRunning {
		t.Fatal("old watch overwrote the new Backend")
	}
}

func TestAuditSettingsProxyFailureRollsBack(t *testing.T) {
	for _, target := range []int{0, 7892} {
		t.Run(fmt.Sprint(target), func(t *testing.T) {
			useTestCLIRuntimeDirectory(t)
			runtime := newTestTUIServiceRuntime(t)
			original := []byte("mixed-port: 7891\nmode: rule\nlog-level: silent\n")
			if err := os.WriteFile(runtime.paths.ConfigPath, original, 0o600); err != nil {
				t.Fatal(err)
			}
			runtime.trafficMode, runtime.configuredPort, runtime.activePort = "rule", 7891, 7891
			runtime.systemProxy, runtime.proxyPort = true, 7891
			oldSet, oldMatch, oldReload := setTUIServiceSystemProxy, tuiServiceSystemProxyMatches, reloadTUIServiceSettings
			t.Cleanup(func() {
				setTUIServiceSystemProxy, tuiServiceSystemProxyMatches, reloadTUIServiceSettings = oldSet, oldMatch, oldReload
			})
			tuiServiceSystemProxyMatches = func(port int) bool { return port == 7891 }
			setTUIServiceSystemProxy = func(port int, enable bool) error {
				if enable && port == 7891 {
					return nil
				}
				return errors.New("simulated desktop failure")
			}
			reloadTUIServiceSettings = func(r *tuiServiceRuntime, path string) (bool, error) {
				settings := loadTUIConfiguredSettings(path, true)
				r.configuredPort, r.activePort = settings.MixedPort, settings.MixedPort
				return true, nil
			}
			settings := loadTUIConfiguredSettings(runtime.paths.ConfigPath, true)
			settings.MixedPort = target
			status := runtime.handle(tuiServiceRequest{Action: "apply_settings", Settings: settings})
			data, err := os.ReadFile(runtime.paths.ConfigPath)
			if err != nil || string(data) != string(original) || status.OK || !status.SystemProxy || status.ConfiguredProxyPort != 7891 || status.ActiveProxyPort != 7891 {
				t.Fatalf("failed setting left partial state: %+v, %s, %v", status, data, err)
			}
		})
	}
}

func TestAuditBoundIPCClientKeepsOriginalInstance(t *testing.T) {
	directory := t.TempDir()
	listener, err := net.Listen("unix", filepath.Join(directory, tuiServiceSocketFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requests := make(chan tuiServiceRequest, 3)
	done := make(chan error, 1)
	go func() {
		for index := range 3 {
			connection, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			var request tuiServiceRequest
			err = json.NewDecoder(connection).Decode(&request)
			if err != nil {
				_ = connection.Close()
				done <- err
				return
			}
			requests <- request
			id := "old"
			if index > 0 {
				id = "replacement"
			}
			err = json.NewEncoder(connection).Encode(tuiServiceStatus{OK: true, InstanceID: id, Revision: 1})
			_ = connection.Close()
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	client := newTUIServiceClientAt(directory)
	status, err := client.status()
	if err != nil {
		t.Fatal(err)
	}
	operation := client.forInstance(status.InstanceID)
	if _, err := client.status(); err != nil {
		t.Fatal(err)
	}
	if _, err := operation.resetTraffic(1); err != nil {
		t.Fatal(err)
	}
	<-requests
	<-requests
	mutation := <-requests
	if mutation.ExpectedInstanceID != "old" {
		t.Fatalf("operation silently rebound to %q", mutation.ExpectedInstanceID)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestAuditInvalidSourceCannotWidenDestructiveOperation(t *testing.T) {
	runtime := newTestTUIServiceRuntime(t)
	for _, action := range []string{"clear_history", "close_connection", "close_all_connections"} {
		status := runtime.handle(tuiServiceRequest{Action: action, Source: "typo", ConnectionID: "proxy-1"})
		if status.OK || status.ErrorCode != tuiServiceErrorInvalidRequest {
			t.Fatalf("invalid source accepted: %+v", status)
		}
	}
}

func TestAuditResetResultClearsOnlyProxyTotals(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	model.snapshot.TotalTraffic = trafficSnapshot{Up: 123}
	model.snapshot.SSHTotalTraffic = trafficSnapshot{Up: 456}
	model.update(tuiOperationResultMsg{state: tuiOperationState{snapshot: cloneTUISnapshot(model.snapshot), trafficReset: true}})
	if model.snapshot.TotalTraffic.Up != 0 || model.snapshot.SSHTotalTraffic.Up != 456 {
		t.Fatal("traffic reset discarded SSH totals or retained proxy totals")
	}
}

func TestAuditRollbackFailurePublishesActualBackendState(t *testing.T) {
	useTestCLIRuntimeDirectory(t)
	runtime := newTestTUIServiceRuntime(t)
	if err := os.WriteFile(runtime.paths.ConfigPath, []byte("mixed-port: 7891\nmode: rule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime.trafficMode, runtime.configuredPort, runtime.activePort = "rule", 7891, 7891
	runtime.systemProxy, runtime.proxyPort = true, 7891
	oldSet, oldMatch, oldReload := setTUIServiceSystemProxy, tuiServiceSystemProxyMatches, reloadTUIServiceSettings
	t.Cleanup(func() {
		setTUIServiceSystemProxy, tuiServiceSystemProxyMatches, reloadTUIServiceSettings = oldSet, oldMatch, oldReload
	})
	setTUIServiceSystemProxy = func(int, bool) error { return errors.New("desktop command failed") }
	matchCount := 0
	tuiServiceSystemProxyMatches = func(int) bool {
		matchCount++
		return matchCount == 1
	}
	reloadCount := 0
	reloadTUIServiceSettings = func(r *tuiServiceRuntime, path string) (bool, error) {
		reloadCount++
		if reloadCount == 2 {
			return false, errors.New("core rollback failed")
		}
		r.configuredPort, r.activePort = 7892, 7892
		return true, nil
	}
	settings := *loadTUIConfiguredSettings(runtime.paths.ConfigPath, true)
	settings.MixedPort = 7892
	status := runtime.handle(tuiServiceRequest{Action: "apply_settings", Settings: &settings})
	if status.OK || status.Revision <= 1 || status.ActiveProxyPort != 7892 || status.SystemProxy || !strings.Contains(status.Error, "rollback failed") {
		t.Fatalf("partial rollback was hidden from observers: %+v", status)
	}
}

func TestAuditRefreshRejectsCrossRevisionSnapshot(t *testing.T) {
	useTestCLIRuntimeDirectory(t)
	directory := t.TempDir()
	listener, err := net.Listen("unix", filepath.Join(directory, tuiServiceSocketFilename))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		for index := range 2 {
			connection, err := listener.Accept()
			if err != nil {
				done <- err
				return
			}
			var request tuiServiceRequest
			err = json.NewDecoder(connection).Decode(&request)
			if err == nil {
				err = json.NewEncoder(connection).Encode(tuiServiceStatus{OK: true, InstanceID: "backend", Revision: uint64(index + 1)})
			}
			_ = connection.Close()
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	model := newTUIModel(controllerClient{options: controllerOptions{address: server.URL}, client: server.Client()}, cliPaths{HomeDir: directory}, nil, true)
	model.service = newTUIServiceClientAt(directory)
	message := model.startRefresh()().(tuiRefreshResultMsg)
	if !message.retry || message.serviceStatus != nil {
		t.Fatal("snapshot spanning two Backend revisions was accepted")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
