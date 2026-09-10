//go:build linux && !cgo && cli

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIRequestHistoryTracksLifecycleAndLimit(t *testing.T) {
	start := time.Unix(100, 0)
	connection := tuiConnection{
		ID:      "request-1",
		Host:    "example.com",
		Process: "curl",
		Network: "tcp",
		Chain:   "PROXY",
	}
	history := updateTUIRequestHistory(nil, []tuiConnection{connection}, start)
	if len(history) != 1 || !history[0].Active {
		t.Fatalf("new request was not captured as active: %+v", history)
	}
	connection.Download = 1024
	history = updateTUIRequestHistory(
		history,
		[]tuiConnection{connection},
		start.Add(time.Second),
	)
	if len(history) != 1 || history[0].Download != 1024 {
		t.Fatalf("active request was duplicated or not updated: %+v", history)
	}
	history = updateTUIRequestHistory(
		history,
		nil,
		start.Add(2*time.Second),
	)
	if history[0].Active {
		t.Fatalf("completed request remained active: %+v", history[0])
	}
	history = updateTUIRequestHistory(
		history,
		[]tuiConnection{{Host: "missing-id.example"}},
		start.Add(3*time.Second),
	)
	if len(history) != 1 {
		t.Fatalf("connection without an ID polluted History: %+v", history)
	}

	oversized := make([]tuiRequest, tuiRequestHistoryLimit+20)
	for index := range oversized {
		oversized[index] = tuiRequest{
			TuiConnection: tuiConnection{ID: fmt.Sprintf("id-%d", index)},
			LastSeen:      start.Add(time.Duration(index) * time.Second),
		}
	}
	limited := updateTUIRequestHistory(oversized, nil, start)
	if len(limited) != tuiRequestHistoryLimit {
		t.Fatalf("request history size = %d, want %d", len(limited), tuiRequestHistoryLimit)
	}
}

func TestTUIStoppedCoreClearsActiveConnectionsAndClosesHistory(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Connections = []tuiConnection{{ID: "connection-1"}}
	model.snapshot.SelectedConnection = 0
	model.snapshot.ConnectionsDetailOpen = true
	model.snapshot.Requests = []tuiRequest{{
		TuiConnection: tuiConnection{ID: "request-1"},
		Active:        true,
	}}
	model.coreRunning = false

	model.reconcileStoppedCoreState()

	if len(model.snapshot.Connections) != 0 ||
		model.snapshot.SelectedConnection != -1 ||
		model.snapshot.ConnectionsDetailOpen {
		t.Fatalf("stopped Core left stale Connections: %+v", model.snapshot)
	}
	if len(model.snapshot.Requests) != 1 || model.snapshot.Requests[0].Active {
		t.Fatalf("stopped Core left active History: %+v", model.snapshot.Requests)
	}
}

func TestTUIRefreshDoesNotSelectAnotherConnectionAfterSelectedOneCloses(t *testing.T) {
	current := tuiSnapshot{
		Page:                  tuiPageConnections,
		Connections:           []tuiConnection{{ID: "closed"}},
		SelectedConnection:    0,
		ConnectionsDetailOpen: true,
	}
	refreshed := tuiSnapshot{
		Connections: []tuiConnection{{ID: "still-open"}},
	}
	merged := mergeTUIRefresh(current, refreshed)
	if merged.SelectedConnection != -1 {
		t.Fatalf("closed connection selected replacement row %d", merged.SelectedConnection)
	}
	if merged.ConnectionsDetailOpen {
		t.Fatal("connection detail remained open after its selected entry closed")
	}
}

func TestTUIRefreshClosesStaleHistoryAndLogDetailsAndSelectsNewLogs(t *testing.T) {
	current := tuiSnapshot{
		Page:                  tuiPageLogs,
		Requests:              []tuiRequest{{TuiConnection: tuiConnection{ID: "closed-request"}}},
		SelectedRequest:       0,
		HistoryDetailOpen:     true,
		Logs:                  []string{"old log"},
		SelectedLog:           0,
		LogDetailOpen:         true,
		Connections:           []tuiConnection{{ID: "closed-connection"}},
		SelectedConnection:    0,
		ConnectionsDetailOpen: true,
	}
	refreshed := current
	refreshed.Requests = []tuiRequest{{TuiConnection: tuiConnection{ID: "new-request"}}}
	refreshed.Connections = []tuiConnection{{ID: "new-connection"}}
	refreshed.Logs = []string{"newest log"}

	merged := mergeTUIRefresh(current, refreshed)
	if merged.SelectedConnection != -1 || merged.ConnectionsDetailOpen {
		t.Fatalf("closed connection retained selection/detail: %+v", merged)
	}
	if merged.SelectedRequest != -1 || merged.HistoryDetailOpen {
		t.Fatalf("closed History entry retained selection/detail: %+v", merged)
	}
	if merged.SelectedLog != 0 || merged.LogDetailOpen {
		t.Fatalf("replaced log retained stale detail or did not select new log: %+v", merged)
	}
}

func TestTUIRefreshKeepsSelectedLogByContentAfterReordering(t *testing.T) {
	current := tuiSnapshot{
		Logs:        []string{"older", "selected"},
		SelectedLog: 1,
	}
	refreshed := current
	refreshed.Logs = []string{"selected", "newer"}
	merged := mergeTUIRefresh(current, refreshed)
	if merged.SelectedLog != 0 {
		t.Fatalf("selected log moved to %d instead of following its content", merged.SelectedLog)
	}

	current.SelectedLog = -1
	current.Logs = nil
	refreshed = current
	refreshed.Logs = []string{"first new log"}
	merged = mergeTUIRefresh(current, refreshed)
	if merged.SelectedLog != 0 {
		t.Fatalf("new log was not selected after an empty log state: %+v", merged)
	}
}

func TestTUIRefreshReplacesTransientRefreshErrorsAfterRecovery(t *testing.T) {
	for _, status := range []string{
		"Controller unavailable: dial failed",
		"Invalid controller response: bad JSON",
		"Connections refresh failed: temporary failure",
		"Refresh incomplete · History: temporary failure",
		"SSH profiles unavailable: read config failed",
	} {
		merged := mergeTUIRefresh(
			tuiSnapshot{Status: status},
			tuiSnapshot{Status: "Connected"},
		)
		if merged.Status != "Connected" {
			t.Fatalf("refresh recovery kept stale %q status as %q", status, merged.Status)
		}
	}
}

func TestTUIRefreshClearsStaleTransientStatusBeforeCollectingNewData(t *testing.T) {
	for _, status := range []string{
		"Controller unavailable: dial failed",
		"Invalid controller response: bad JSON",
		"Connections refresh failed: temporary failure",
		"Refresh incomplete · Logs: temporary failure",
		"SSH profiles unavailable: read config failed",
	} {
		snapshot := tuiSnapshot{Status: status}
		clearTUITransientRefreshStatus(&snapshot)
		if snapshot.Status != "Loading..." {
			t.Fatalf("transient refresh status %q was retained as %q", status, snapshot.Status)
		}
	}

	snapshot := tuiSnapshot{Status: "Settings committed"}
	clearTUITransientRefreshStatus(&snapshot)
	if snapshot.Status != "Settings committed" {
		t.Fatalf("action status should remain visible: %q", snapshot.Status)
	}
}

func TestTUIRefreshDoesNotSwitchToAnotherSSHProfileWhenSelectedProfileDisappears(t *testing.T) {
	current := tuiSnapshot{
		SSHProfiles: []tuiSSHProfile{{Name: "gone"}},
		SelectedSSH: 0,
	}
	refreshed := current
	refreshed.SSHProfiles = []tuiSSHProfile{{Name: "other"}}
	merged := mergeTUIRefresh(current, refreshed)
	if merged.SelectedSSH != -1 {
		t.Fatalf("refresh switched disappeared SSH selection to profile %d", merged.SelectedSSH)
	}
}

func TestTUIRefreshDoesNotApplyAnotherProxyWhenSelectionDisappears(t *testing.T) {
	current := tuiSnapshot{
		Groups:         []tuiGroup{{Name: "gone", Nodes: []string{"old-node"}}},
		SelectedGroup:  0,
		SelectedNode:   0,
		ProxyNodeFocus: true,
	}
	refreshed := current
	refreshed.Groups = []tuiGroup{{Name: "other", Nodes: []string{"other-node"}}}
	merged := mergeTUIRefresh(current, refreshed)
	if merged.SelectedGroup != -1 || merged.SelectedNode != -1 || merged.ProxyNodeFocus {
		t.Fatalf("refresh switched a removed proxy group: %+v", merged)
	}

	refreshed = current
	refreshed.Groups = []tuiGroup{{Name: "gone", Nodes: []string{"replacement-node"}}}
	merged = mergeTUIRefresh(current, refreshed)
	if merged.SelectedGroup != 0 || merged.SelectedNode != -1 || merged.ProxyNodeFocus {
		t.Fatalf("refresh retained a removed proxy node as another node: %+v", merged)
	}

	model := &tuiModel{snapshot: tuiSnapshot{
		Page:           tuiPageProxies,
		Groups:         refreshed.Groups,
		SelectedGroup:  0,
		SelectedNode:   -1,
		ProxyNodeFocus: true,
	}}
	if command := model.selectCurrent(); command != nil || model.busy ||
		!strings.Contains(model.snapshot.Status, "Select a proxy node") {
		t.Fatalf("invalid proxy selection started an operation: %+v", model.snapshot)
	}
}

func TestTUIInvalidSelectionsReturnActionableStatus(t *testing.T) {
	state := tuiOperationState{}
	selectTUIServiceProxy(&state, nil, controllerClient{})
	if state.snapshot.Status != "Select a proxy group before applying it" {
		t.Fatalf("managed proxy selection status = %q", state.snapshot.Status)
	}

	snapshot := tuiSnapshot{}
	selectTUIProxy(&snapshot, controllerClient{}, "")
	if snapshot.Status != "Select a proxy group before applying it" {
		t.Fatalf("direct proxy selection status = %q", snapshot.Status)
	}
	updateTUIProvider(&snapshot, controllerClient{})
	if snapshot.Status != "Select a provider before updating it" {
		t.Fatalf("provider selection status = %q", snapshot.Status)
	}

	model := &tuiModel{snapshot: tuiSnapshot{
		Page:        tuiPageProfiles,
		SelectedRow: 0,
	}}
	if command := model.selectCurrent(); command != nil || model.busy ||
		model.snapshot.Status != "Select a profile before activating it" {
		t.Fatalf("invalid profile selection started an operation: %+v", model.snapshot)
	}

	model.snapshot = tuiSnapshot{Page: tuiPageConnections, SelectedConnection: -1}
	if command := model.handleKey(tuiKeyCloseConnection); command != nil ||
		model.snapshot.Status != "Select an active connection before closing it" {
		t.Fatalf("invalid connection close did not explain itself: %+v", model.snapshot)
	}
}

func TestTUIFilteredSelectionClearsAndNavigationRecoversAtEdges(t *testing.T) {
	snapshot := tuiSnapshot{
		Connections: []tuiConnection{{ID: "first"}, {ID: "second"}},
		Requests: []tuiRequest{
			{TuiConnection: tuiConnection{ID: "first"}},
			{TuiConnection: tuiConnection{ID: "second"}},
		},
		Logs: []string{"first", "second"},
	}
	snapshot.ConnectionsQuery = "missing"
	snapshot.HistoryQuery = "missing"
	snapshot.LogsQuery = "missing"
	if firstTUIConnectionMatch(snapshot) != -1 ||
		firstTUIRequestMatch(snapshot) != -1 ||
		firstTUILogMatch(snapshot) != -1 {
		t.Fatal("a no-match filter retained a selectable row")
	}

	snapshot.ConnectionsQuery = ""
	snapshot.HistoryQuery = ""
	snapshot.SelectedConnection = -1
	snapshot.SelectedRequest = -1
	moveTUIConnectionMatch(&snapshot, 1)
	moveTUIRequestMatch(&snapshot, 1)
	if snapshot.SelectedConnection != 0 || snapshot.SelectedRequest != 0 {
		t.Fatalf("down did not recover first match: %+v", snapshot)
	}
	snapshot.SelectedConnection = -1
	snapshot.SelectedRequest = -1
	moveTUIConnectionMatch(&snapshot, -1)
	moveTUIRequestMatch(&snapshot, -1)
	if snapshot.SelectedConnection != 1 || snapshot.SelectedRequest != 1 {
		t.Fatalf("up did not recover last match: %+v", snapshot)
	}
}

func TestTUISearchAndFilterCloseStaleDetails(t *testing.T) {
	model := &tuiModel{
		snapshot: tuiSnapshot{
			Page:                  tuiPageRequests,
			Requests:              []tuiRequest{{TuiConnection: tuiConnection{ID: "active"}, Active: true}},
			SelectedRequest:       0,
			HistoryDetailOpen:     true,
			Connections:           []tuiConnection{{ID: "connection"}},
			SelectedConnection:    0,
			ConnectionsDetailOpen: true,
			Logs:                  []string{"INFO visible"},
			SelectedLog:           0,
			LogDetailOpen:         true,
		},
	}

	model.handleKey(tuiKeyFilter)
	if model.snapshot.HistoryDetailOpen {
		t.Fatal("changing the History filter retained a stale detail")
	}

	model.inputMode = tuiInputConnectionsSearch
	model.inputValue = []rune("missing")
	model.submitInput()
	if model.snapshot.ConnectionsDetailOpen || model.snapshot.SelectedConnection != -1 {
		t.Fatalf("connections search retained stale detail or selection: %+v", model.snapshot)
	}

	model.inputMode = tuiInputLogsSearch
	model.inputValue = []rune("missing")
	model.submitInput()
	if model.snapshot.LogDetailOpen || model.snapshot.SelectedLog != -1 {
		t.Fatalf("log search retained stale detail or selection: %+v", model.snapshot)
	}

	model.snapshot.Page = tuiPageLogs
	model.snapshot.LogDetailOpen = true
	model.handleKey(tuiKeyFilter)
	if model.snapshot.LogDetailOpen {
		t.Fatal("changing the log level filter retained a stale detail")
	}
}

func TestTUILogExportsNeverOverwritePreviousExport(t *testing.T) {
	directory := t.TempDir()
	seen := map[string]bool{}
	for range 10 {
		path, err := exportTUILogs(directory, []string{"retained log"})
		if err != nil {
			t.Fatal(err)
		}
		if seen[path] {
			t.Fatalf("export reused %s", path)
		}
		seen[path] = true
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("export permissions: %v", err)
		}
	}
	for path := range seen {
		data, err := os.ReadFile(path)
		if err != nil || string(data) != "retained log\n" {
			t.Fatalf("export lost: %v", err)
		}
	}
}

func TestTUILogExportAndClear(t *testing.T) {
	clearTUILogs()
	sendMessage(Message{
		Type: LogMessage,
		Data: map[string]string{"Payload": "first event"},
	})
	sendMessage(Message{
		Type: LogMessage,
		Data: map[string]string{"Payload": "second event"},
	})
	logs := cliLogSnapshot()
	if len(logs) != 2 {
		t.Fatalf("captured logs = %v, want two entries", logs)
	}

	path, err := exportTUILogs(t.TempDir(), logs)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first event\nsecond event\n" {
		t.Fatalf("exported logs = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("exported log mode = %o, want 600", info.Mode().Perm())
	}

	model := &tuiModel{
		snapshot: tuiSnapshot{
			Page:          tuiPageLogs,
			Logs:          logs,
			SelectedLog:   1,
			LogDetailOpen: true,
		},
	}
	if command := model.handleKey(tuiKeyCloseConnections); command != nil {
		t.Fatal("clearing logs unexpectedly started an asynchronous command")
	}
	if !model.dangerConfirmOpen {
		t.Fatal("clearing logs did not require confirmation")
	}
	if command := model.handleDangerConfirm(tea.KeyMsg{Type: tea.KeyEnter}); command != nil {
		t.Fatal("confirmed local log clear unexpectedly started an asynchronous command")
	}
	if len(model.snapshot.Logs) != 0 || len(cliLogSnapshot()) != 0 {
		t.Fatal("clearing logs left captured entries behind")
	}
	if model.snapshot.SelectedLog != -1 || model.snapshot.LogDetailOpen {
		t.Fatalf("clearing logs retained stale selection/detail: %+v", model.snapshot)
	}
}

func TestCLIApplicationLogRedactsURLsAndDataDirectory(t *testing.T) {
	directory := t.TempDir()
	appendCLIApplicationLog(
		directory,
		"WARN",
		"profile_import",
		"source https://secret.example/token in "+directory,
	)
	data, err := os.ReadFile(filepath.Join(directory, tuiServiceLogFilename))
	if err != nil {
		t.Fatal(err)
	}
	line := string(data)
	if strings.Contains(line, "secret.example") || strings.Contains(line, directory) {
		t.Fatalf("application log leaked sensitive detail: %q", line)
	}
	if !strings.Contains(line, "[redacted-url]") || !strings.Contains(line, "$DATA") {
		t.Fatalf("application log did not mark redaction: %q", line)
	}
	clearTUILogs()
}

func TestTUIPersistentLogsCanBeReadAndCleared(t *testing.T) {
	directory := t.TempDir()
	appendCLIApplicationLog(directory, "INFO", "first", "one")
	appendCLIApplicationLog(directory, "ERROR", "second", "two")
	logs := readTUIPersistentLogs(directory, 10)
	if len(logs) != 2 || !strings.Contains(logs[0], "first") || !strings.Contains(logs[1], "second") {
		t.Fatalf("persistent logs = %v", logs)
	}
	if err := os.WriteFile(filepath.Join(directory, tuiServiceLogFilename)+".1", []byte("old\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := clearTUIPersistentLogs(directory); err != nil {
		t.Fatal(err)
	}
	if logs := readTUIPersistentLogs(directory, 10); len(logs) != 0 {
		t.Fatalf("cleared persistent logs = %v", logs)
	}
	if _, err := os.Stat(filepath.Join(directory, tuiServiceLogFilename) + ".1"); !os.IsNotExist(err) {
		t.Fatalf("rotated log was not removed: %v", err)
	}
}

func TestTUIEditKeyIsPageScoped(t *testing.T) {
	model := &tuiModel{
		snapshot: tuiSnapshot{Page: tuiPageDashboard},
		paths:    cliPaths{ConfigPath: "/tmp/config.yaml"},
	}
	if command := model.handleKey(tuiKeyEdit); command != nil {
		t.Fatal("Dashboard edit key unexpectedly opened an editor")
	}
	if !strings.Contains(model.snapshot.Status, "Profiles and Maintenance") {
		t.Fatalf("Dashboard edit guidance = %q", model.snapshot.Status)
	}

	model.snapshot.Page = tuiPageProfiles
	model.snapshot.SelectedRow = -1
	if command := model.handleKey(tuiKeyEdit); command != nil {
		t.Fatal("profile import row unexpectedly opened an editor")
	}
	if !strings.Contains(model.snapshot.Status, "Select a profile") {
		t.Fatalf("Profiles edit guidance = %q", model.snapshot.Status)
	}
}

func TestFormatTUIDestination(t *testing.T) {
	for _, test := range []struct {
		host string
		port string
		want string
	}{
		{host: "1.1.1.1", port: "443", want: "1.1.1.1:443"},
		{host: "2001:db8::1", port: "53", want: "[2001:db8::1]:53"},
		{host: "example.com", want: "example.com"},
		{port: "443"},
	} {
		if got := formatTUIDestination(test.host, test.port); got != test.want {
			t.Fatalf(
				"formatTUIDestination(%q, %q) = %q, want %q",
				test.host,
				test.port,
				got,
				test.want,
			)
		}
	}
}

func TestTUIRequestsCanBeClearedWithoutClosingConnections(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, false)
	model.snapshot.Page = tuiPageRequests
	model.snapshot.FocusSidebar = false
	model.snapshot.Requests = []tuiRequest{{
		TuiConnection: tuiConnection{ID: "request-1"},
	}}
	model.snapshot.SelectedRequest = 0
	model.snapshot.HistoryDetailOpen = true
	model.snapshot.Connections = []tuiConnection{{ID: "active-1"}}

	if command := model.handleKey(tuiKeyCloseConnections); command != nil {
		t.Fatal("clearing request history unexpectedly called the controller")
	}
	if !model.dangerConfirmOpen {
		t.Fatal("clearing request history did not require confirmation")
	}
	if command := model.handleDangerConfirm(tea.KeyMsg{Type: tea.KeyEnter}); command != nil {
		t.Fatal("confirmed local History clear unexpectedly called the controller")
	}
	if len(model.snapshot.Requests) != 0 {
		t.Fatalf("request history was not cleared: %+v", model.snapshot.Requests)
	}
	if model.snapshot.SelectedRequest != -1 || model.snapshot.HistoryDetailOpen {
		t.Fatalf("clearing History retained stale selection/detail: %+v", model.snapshot)
	}
	if len(model.snapshot.Connections) != 1 {
		t.Fatal("clearing request history changed active connections")
	}
}

func TestControllerClientTestsSelectedProxyDelay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/proxies/HK%20Node/delay" &&
			request.URL.Path != "/proxies/HK Node/delay" {
			t.Fatalf("delay path = %q", request.URL.Path)
		}
		if got := request.URL.Query().Get("url"); got != "https://example.test/204?a=1" {
			t.Fatalf("delay test URL = %q", got)
		}
		if got := request.URL.Query().Get("timeout"); got != "5000" {
			t.Fatalf("delay timeout = %q", got)
		}
		_, _ = io.WriteString(w, `{"delay":42}`)
	}))
	defer server.Close()
	client := controllerClient{
		options: controllerOptions{address: server.URL},
		client:  server.Client(),
	}
	delay, err := client.testProxyDelay(
		"HK Node",
		"https://example.test/204?a=1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if delay != 42 {
		t.Fatalf("delay = %d, want 42", delay)
	}
}

func TestControllerClientDelayTestOverridesShortRefreshTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		time.Sleep(60 * time.Millisecond)
		_, _ = io.WriteString(w, `{"delay":35}`)
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.Timeout = 10 * time.Millisecond
	client := controllerClient{
		options: controllerOptions{address: server.URL},
		client:  httpClient,
	}

	delay, err := client.testProxyDelay("Node", "https://example.test/204")
	if err != nil {
		t.Fatalf("delay test reused the short refresh timeout: %v", err)
	}
	if delay != 35 {
		t.Fatalf("delay = %d, want 35", delay)
	}
}

func TestTUIWholeGroupDelayTestCollectsReachableAndTimeoutNodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		request *http.Request,
	) {
		node := strings.TrimSuffix(
			strings.TrimPrefix(request.URL.Path, "/proxies/"),
			"/delay",
		)
		switch node {
		case "fast":
			_, _ = io.WriteString(w, `{"delay":18}`)
		case "slow":
			_, _ = io.WriteString(w, `{"delay":240}`)
		default:
			http.Error(w, "unreachable", http.StatusGatewayTimeout)
		}
	}))
	defer server.Close()
	client := controllerClient{
		options: controllerOptions{address: server.URL},
		client:  server.Client(),
	}

	delays := testTUIProxyDelays(
		client,
		[]string{"fast", "slow", "dead"},
		"https://example.test/204",
	)
	if delays["fast"].MedianMillis != 18 || delays["fast"].Samples != 5 ||
		delays["slow"].MedianMillis != 240 || delays["dead"].Error == "" {
		t.Fatalf("whole-group delays = %#v", delays)
	}
}

func TestTUIWholeGroupDelayKeyUpdatesVisibleNodeStates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		request *http.Request,
	) {
		if strings.Contains(request.URL.Path, "dead") {
			http.Error(w, "unreachable", http.StatusGatewayTimeout)
			return
		}
		_, _ = io.WriteString(w, `{"delay":27}`)
	}))
	defer server.Close()
	model := newTUIModel(
		controllerClient{
			options: controllerOptions{address: server.URL},
			client:  server.Client(),
		},
		cliPaths{},
		nil,
		false,
	)
	model.snapshot.Page = tuiPageProxies
	model.snapshot.ProxyView = tuiProxyViewGroups
	model.snapshot.FocusSidebar = false
	model.snapshot.Groups = []tuiGroup{{
		Name:   "Proxy",
		Nodes:  []string{"fast", "dead"},
		Delays: map[string]tuiDelayResult{},
	}}

	command := model.handleTeaKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'d'},
	})
	if command == nil {
		t.Fatal("d did not test all nodes from proxy-group mode")
	}
	for _, node := range model.snapshot.Groups[0].Nodes {
		if !model.snapshot.Groups[0].Delays[node].Testing {
			t.Fatalf("%s was not marked Testing", node)
		}
	}
	_, _ = model.Update(command())
	if model.snapshot.Groups[0].Delays["fast"].MedianMillis != 27 ||
		model.snapshot.Groups[0].Delays["fast"].Samples != 5 ||
		model.snapshot.Groups[0].Delays["dead"].Error == "" {
		t.Fatalf("visible node delays = %#v", model.snapshot.Groups[0].Delays)
	}
	if !strings.Contains(model.snapshot.Status, "1/2 reachable") {
		t.Fatalf("whole-group status = %q", model.snapshot.Status)
	}
}

func TestTUINetworkDetectionUsesConfiguredMixedPortProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Host != "public-ip.invalid" {
			t.Fatalf("proxy target host = %q", request.URL.Host)
		}
		_, _ = io.WriteString(w, `{"ip":"203.0.113.8","country_code":"SG"}`)
	}))
	defer proxy.Close()
	result := detectTUIPublicIP(
		newTUINetworkHTTPClient(proxy.URL),
		[]string{"http://public-ip.invalid/json"},
	)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.IP != "203.0.113.8" || result.Country != "SG" {
		t.Fatalf("public IP result = %+v", result)
	}
}

func TestTUINetworkDetectionAuthenticatesSilentProxy(t *testing.T) {
	authenticated := false
	proxy := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		request *http.Request,
	) {
		authenticated = strings.HasPrefix(
			request.Header.Get("Proxy-Authorization"),
			"Basic ",
		)
		_, _ = io.WriteString(w, `{"ip":"198.51.100.40","country_code":"JP"}`)
	}))
	defer proxy.Close()
	proxyURL := strings.Replace(proxy.URL, "http://", "http://flc:secret@", 1)

	result := detectTUIPublicIP(
		newTUINetworkHTTPClient(proxyURL),
		[]string{"http://public-ip.invalid/json"},
	)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if !authenticated {
		t.Fatal("silent network detection omitted proxy authentication")
	}
}

func TestTUINetworkDetectionUsesActiveFallbackPort(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.coreRunning = true
	model.snapshot.Settings.Mode = "rule"
	model.snapshot.Settings.MixedPort = 7890
	model.snapshot.ActiveProxyPort = 17890

	if port := model.networkCheckProxyPort(); port != 17890 {
		t.Fatalf("network detection port = %d, want active fallback 17890", port)
	}
	if route := model.networkCheckRoute(); route != "proxy:17890" {
		t.Fatalf("network detection route = %q", route)
	}

	model.snapshot.Settings.Mode = tuiSilentMode
	if port := model.networkCheckProxyPort(); port != 0 {
		t.Fatalf("silent network detection exposed private listener %d", port)
	}
	if route := model.networkCheckRoute(); !strings.HasPrefix(route, "silent:") {
		t.Fatalf("silent network detection route = %q", route)
	}
}

func TestTUIPublicIPDetectionAcceptsOriginalFlClashResponseShapes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/invalid":
			http.Error(w, "failed", http.StatusServiceUnavailable)
		default:
			_, _ = io.WriteString(w, `{"query":"198.51.100.9","countryCode":"US"}`)
		}
	}))
	defer server.Close()

	result := detectTUIPublicIP(
		server.Client(),
		[]string{server.URL + "/invalid", server.URL + "/valid"},
	)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.IP != "198.51.100.9" || result.Country != "US" {
		t.Fatalf("public IP result = %+v", result)
	}
}

func TestTUIIntranetIPPrefersPrivateWiFiIPv4(t *testing.T) {
	selected, ok := selectTUIIntranetIP([]tuiLocalIPCandidate{
		{Interface: "eth0", Address: net.ParseIP("2001:db8::8")},
		{Interface: "docker0", Address: net.ParseIP("172.17.0.1")},
		{Interface: "wlp2s0", Address: net.ParseIP("192.168.1.23")},
		{Interface: "eth0", Address: net.ParseIP("198.51.100.4")},
	})
	if !ok {
		t.Fatal("no intranet IP was selected")
	}
	if selected.Interface != "wlp2s0" ||
		selected.Address.String() != "192.168.1.23" {
		t.Fatalf("selected intranet IP = %+v", selected)
	}
}

func TestTUIDashboardRendersPublicAndIntranetIP(t *testing.T) {
	snapshot := populatedTUISnapshot(tuiPageDashboard)
	snapshot.Network = tuiNetworkInfo{
		PublicIP:   "203.0.113.8",
		Country:    "SG",
		IntranetIP: "192.168.1.23 (wlp2s0)",
		Route:      "PROXY 127.0.0.1:7890",
		CheckedAt:  time.Date(2026, 7, 29, 12, 34, 56, 0, time.Local),
	}
	var output strings.Builder
	drawTUIDashboard(
		&output,
		snapshot,
		cliPaths{ConfigPath: "/tmp/config.yaml"},
		100,
		26,
	)
	plain := stripTUIANSI(output.String())
	for _, expected := range []string{
		"Network detection",
		"Public IP",
		"203.0.113.8",
		"[SG]",
		"Intranet IP",
		"192.168.1.23 (wlp2s0)",
		"PROXY 127.0.0.1:7890",
		"n refresh",
	} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("Dashboard does not contain %q:\n%s", expected, plain)
		}
	}
}

func TestTUINetworkCheckDiscardsResultFromOldRoute(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Settings.MixedPort = 7890
	model.coreRunning = true
	model.networkCheckActive = true

	_, command := model.Update(tuiNetworkResultMsg{
		route: "direct",
		info: tuiNetworkInfo{
			PublicIP:  "198.51.100.2",
			Route:     "DIRECT",
			CheckedAt: time.Now(),
		},
	})
	if command == nil {
		t.Fatal("route change did not schedule a fresh network check")
	}
	if !model.networkCheckActive {
		t.Fatal("fresh network check is not marked active")
	}
	if model.snapshot.Network.PublicIP != "" {
		t.Fatal("stale direct-route IP replaced the proxy-route result")
	}
}

func TestTUINetworkCheckHasNoPeriodicCooldown(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Network.CheckedAt = time.Now()

	if command := model.startNetworkCheck(); command == nil {
		t.Fatal("event-triggered network check was suppressed by a cooldown")
	}
}

func TestTUIOperationRefreshesNetworkAfterExitChange(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.coreRunning = true
	model.snapshot.Settings.Mode = "rule"
	model.snapshot.Settings.MixedPort = 7890
	model.snapshot.ActiveProxyPort = 7890

	state := tuiOperationState{
		snapshot:        model.snapshot,
		paths:           model.paths,
		coreRunning:     model.coreRunning,
		backendRevision: model.backendRevision,
		networkChanged:  true,
	}
	_, command := model.Update(tuiOperationResultMsg{state: state})
	if command == nil || !model.networkCheckActive {
		t.Fatal("exit-changing operation did not request a network refresh")
	}
}

func TestTUIMemoryRefreshIntervalIsOneSecond(t *testing.T) {
	if tuiMemoryRefreshInterval < 2*time.Second {
		t.Fatalf("memory refresh interval = %s, want at least 2s", tuiMemoryRefreshInterval)
	}
	if tuiRefreshInterval < 2*time.Second {
		t.Fatalf("TUI tick interval = %s, want at least 2s", tuiRefreshInterval)
	}
	if tuiProgramFPS > 15 || tuiProgramFPS <= 0 {
		t.Fatalf("TUI FPS = %d, want 1..15", tuiProgramFPS)
	}
}

func TestTUIIdleTickSkipsHistoryLogsAndPublicIPOffThosePages(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Page = tuiPageDashboard
	_, command := model.Update(tuiTickMsg{})
	if command == nil {
		t.Fatal("Dashboard tick did not reschedule idle work")
	}
	if model.networkCheckActive || model.lastIdleTick.CheckNetwork {
		t.Fatal("tick started a public-IP network check")
	}
	if model.lastIdleTick.FetchHistory || model.lastIdleTick.FetchLogs ||
		model.refreshIncludesHistory || model.refreshIncludesLogs {
		t.Fatalf(
			"Dashboard tick started History/Logs bulk fetch: %+v history=%t logs=%t",
			model.lastIdleTick,
			model.refreshIncludesHistory,
			model.refreshIncludesLogs,
		)
	}
	if !model.lastIdleTick.RefreshSnapshot || !model.lastIdleTick.SampleMemory ||
		model.lastIdleTick.PollSSH {
		t.Fatalf("Dashboard idle plan = %+v", model.lastIdleTick)
	}

	model.refreshInFlight = false
	model.snapshot.Page = tuiPageTools
	_, _ = model.Update(tuiTickMsg{})
	if model.lastIdleTick.RefreshSnapshot || model.lastIdleTick.SampleMemory ||
		model.lastIdleTick.PollSSH || model.lastIdleTick.FetchHistory ||
		model.lastIdleTick.FetchLogs || model.refreshIncludesHistory ||
		model.refreshIncludesLogs || model.networkCheckActive {
		t.Fatalf("Settings tick still did idle bulk work: %+v", model.lastIdleTick)
	}
}

func TestTUILiveMonitorsFollowDashboardPage(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.service = newTUIServiceClientAt(t.TempDir())
	defer model.stopTrafficMonitor()
	defer model.stopCoreMemoryMonitor()
	model.snapshot.Page = tuiPageDashboard
	model.startTrafficMonitor()
	model.startCoreMemoryMonitor()
	if model.stopTraffic == nil || model.stopCoreMemory == nil {
		t.Fatal("Dashboard did not start Core traffic/memory streams")
	}
	if !tuiPageShowsLiveCoreStats(tuiPageDashboard) {
		t.Fatal("Dashboard is not treated as a live traffic/memory page")
	}

	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'8'}})
	if model.snapshot.Page != tuiPageTools {
		t.Fatalf("Settings key opened %s", tuiPageName(model.snapshot.Page))
	}
	if model.stopTraffic != nil || model.stopCoreMemory != nil {
		t.Fatal("Core traffic/memory streams stayed up on Settings")
	}

	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	if model.snapshot.Page != tuiPageProfiles {
		t.Fatalf("Profiles key opened %s", tuiPageName(model.snapshot.Page))
	}
	if model.stopTraffic != nil || model.stopCoreMemory != nil {
		t.Fatal("Core traffic/memory streams started on Profiles")
	}

	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'9'}})
	if model.snapshot.Page != tuiPageMaintenance {
		t.Fatalf("Maintenance key opened %s", tuiPageName(model.snapshot.Page))
	}
	if model.stopTraffic != nil || model.stopCoreMemory != nil {
		t.Fatal("Core traffic/memory streams started on Maintenance")
	}

	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	if model.snapshot.Page != tuiPageDashboard {
		t.Fatalf("Dashboard key opened %s", tuiPageName(model.snapshot.Page))
	}
	if model.stopTraffic == nil || model.stopCoreMemory == nil {
		t.Fatal("entering Dashboard did not resume Core traffic/memory streams")
	}

	model.snapshot.FocusSidebar = false
	model.snapshot.FLCOutbound = "PROXY"
	model.snapshot.Groups = []tuiGroup{{
		Name:  "PROXY",
		Now:   "hk-1",
		Nodes: []string{"DIRECT", "hk-1"},
	}}
	model.snapshot.SelectedDashboard = tuiDashboardFLCOutboundRow
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if model.snapshot.Page != tuiPageProxies {
		t.Fatalf("flc Enter opened %s", tuiPageName(model.snapshot.Page))
	}
	if model.stopTraffic != nil || model.stopCoreMemory != nil {
		t.Fatal("Core traffic/memory streams stayed up after flc Enter opened Proxies")
	}

	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	if model.snapshot.Page != tuiPageDashboard ||
		model.stopTraffic == nil || model.stopCoreMemory == nil {
		t.Fatalf(
			"Dashboard resume after flc = page:%s traffic:%t memory:%t",
			tuiPageName(model.snapshot.Page),
			model.stopTraffic != nil,
			model.stopCoreMemory != nil,
		)
	}

	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
	if model.snapshot.Page != tuiPageProxies {
		t.Fatalf("P opened %s", tuiPageName(model.snapshot.Page))
	}
	if model.stopTraffic != nil || model.stopCoreMemory != nil {
		t.Fatal("Core traffic/memory streams stayed up after P opened Proxies")
	}
}

func TestTUIIdleTickFetchesHistoryOnlyOnHistoryPage(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Page = tuiPageRequests
	_, _ = model.Update(tuiTickMsg{})
	if !model.lastIdleTick.FetchHistory || !model.refreshIncludesHistory {
		t.Fatalf("History tick skipped History fetch: %+v", model.lastIdleTick)
	}
	if model.lastIdleTick.FetchLogs || model.refreshIncludesLogs ||
		model.lastIdleTick.CheckNetwork || model.networkCheckActive {
		t.Fatalf("History tick started extra probes: %+v", model.lastIdleTick)
	}

	model.refreshInFlight = false
	model.snapshot.Page = tuiPageLogs
	_, _ = model.Update(tuiTickMsg{})
	if !model.lastIdleTick.FetchLogs || !model.refreshIncludesLogs {
		t.Fatalf("Logs tick skipped Logs fetch: %+v", model.lastIdleTick)
	}
	if model.lastIdleTick.FetchHistory || model.refreshIncludesHistory {
		t.Fatalf("Logs tick started History bulk fetch: %+v", model.lastIdleTick)
	}
}

func TestTUIDashboardOwnsDailyControlsAndSettingsDoesNotRepeatThem(t *testing.T) {
	snapshot := tuiSnapshot{
		Page: tuiPageDashboard,
		Settings: tuiSettings{
			Mode:       "rule",
			MixedPort:  17890,
			TunEnabled: true,
			TunScope:   tuiTunScopeUser,
		},
	}
	var dashboard strings.Builder
	drawTUIDashboard(
		&dashboard,
		snapshot,
		cliPaths{ConfigPath: "/tmp/config.yaml"},
		100,
		26,
	)
	dashboardPlain := stripTUIANSI(dashboard.String())
	for _, row := range []string{
		"Core          STOPPED · Enter to start",
		"Mode          rule",
		"flc           pick a node in Proxies",
		"Proxy port    17890",
		"System proxy",
		"TUN           USER ON",
	} {
		if !strings.Contains(dashboardPlain, row) {
			t.Fatalf("Dashboard does not contain %q:\n%s", row, dashboardPlain)
		}
	}

	snapshot.Page = tuiPageTools
	var settings strings.Builder
	drawTUITools(&settings, snapshot, 100, 30)
	settingsPlain := stripTUIANSI(settings.String())
	for _, daily := range []string{
		"Core          STOPPED · Enter to start",
		"Mode          rule",
		"flc           pick a node in Proxies",
		"Proxy port    17890",
		"System proxy",
		"TUN           USER ON",
	} {
		if strings.Contains(settingsPlain, daily) {
			t.Fatalf("Settings repeats daily control %q:\n%s", daily, settingsPlain)
		}
	}
	for _, row := range []string{
		"Allow LAN",
		"IPv6",
		"Unified delay",
		"Log level",
		"TUN scope",
	} {
		if !strings.Contains(settingsPlain, row) {
			t.Fatalf("Settings does not contain %q:\n%s", row, settingsPlain)
		}
	}
}

func TestTUIParsesLinuxSystemMemoryUsingMemAvailable(t *testing.T) {
	total, available, err := parseTUISystemMemory([]byte(`MemTotal:       8192000 kB
MemFree:        1000000 kB
MemAvailable:   3072000 kB
Buffers:         100000 kB
Cached:          500000 kB
`))
	if err != nil {
		t.Fatal(err)
	}
	if total != 8192000*1024 || available != 3072000*1024 {
		t.Fatalf("memory = total %d available %d", total, available)
	}
}

func TestTUIParsesLinuxSystemMemoryFallback(t *testing.T) {
	total, available, err := parseTUISystemMemory([]byte(`MemTotal: 4096 kB
MemFree: 512 kB
Buffers: 128 kB
Cached: 1024 kB
`))
	if err != nil {
		t.Fatal(err)
	}
	if total != 4096*1024 || available != (512+128+1024)*1024 {
		t.Fatalf("fallback memory = total %d available %d", total, available)
	}
}

func TestTUIReadsProcessRSSFromLinuxStatm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "statm")
	if err := os.WriteFile(path, []byte("100 25 3 2 0 0 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rss, err := readTUIProcessRSS(path, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if rss != 25*4096 {
		t.Fatalf("RSS = %d, want %d", rss, 25*4096)
	}
}

func TestTUILocalMemorySampleIncludesSystemProcessAndGoHeap(t *testing.T) {
	info := sampleTUIMemory(tuiMemoryInfo{}, false)
	if info.SystemTotal == 0 || info.SystemUsed == 0 {
		t.Fatalf("system memory was not sampled: %+v", info)
	}
	if info.ProcessRSS == 0 {
		t.Fatalf("process RSS was not sampled: %+v", info)
	}
	if info.GoHeap == 0 {
		t.Fatalf("Go heap was not sampled: %+v", info)
	}
	if info.ExternalCore {
		t.Fatal("embedded sample was marked as external")
	}
	if info.UpdatedAt.IsZero() {
		t.Fatal("memory sample has no timestamp")
	}
}

func TestTUIMemoryRefreshPreservesNewerExternalCoreSample(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, false)
	newer := time.Now()
	model.memoryRefreshActive = true
	model.snapshot.Memory = tuiMemoryInfo{
		CoreRSS:     222 << 20,
		CoreUpdated: newer,
	}
	_, _ = model.Update(tuiMemoryResultMsg{
		info: tuiMemoryInfo{
			SystemTotal: 8 << 30,
			SystemUsed:  4 << 30,
			ProcessRSS:  20 << 20,
			CoreRSS:     111 << 20,
			CoreUpdated: newer.Add(-time.Second),
			UpdatedAt:   newer,
		},
	})
	if model.memoryRefreshActive {
		t.Fatal("memory refresh remained active after receiving a sample")
	}
	if model.snapshot.Memory.CoreRSS != 222<<20 {
		t.Fatalf("newer external Core RSS was overwritten: %+v", model.snapshot.Memory)
	}
	if model.snapshot.Memory.SystemTotal != 8<<30 {
		t.Fatalf("local memory sample was not applied: %+v", model.snapshot.Memory)
	}
}

func TestTUIExternalCoreMemoryErrorKeepsLastRSS(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, false)
	model.snapshot.Memory.CoreRSS = 256 << 20
	_, command := model.Update(tuiCoreMemoryMsg{
		update: tuiCoreMemoryUpdate{
			Error:     "temporary disconnect",
			UpdatedAt: time.Now(),
		},
	})
	if command != nil {
		t.Fatal("model without a monitor scheduled another monitor read")
	}
	if model.snapshot.Memory.CoreRSS != 256<<20 {
		t.Fatalf("temporary error erased the last Core RSS: %+v", model.snapshot.Memory)
	}
	if model.snapshot.Memory.CoreError != "temporary disconnect" {
		t.Fatalf("Core memory error = %q", model.snapshot.Memory.CoreError)
	}
}

func TestTUIExternalCoreMemoryStreamIgnoresInitialZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/memory" {
			t.Fatalf("memory path = %q", request.URL.Path)
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test response does not support flushing")
		}
		_, _ = io.WriteString(w, "{\"inuse\":0,\"oslimit\":0}\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "{\"inuse\":98765432,\"oslimit\":0}\n")
		flusher.Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	values := make(chan uint64, 1)
	errors := make(chan error, 1)
	go func() {
		errors <- streamTUICoreMemory(
			ctx,
			controllerClient{
				options: controllerOptions{address: server.URL},
				client:  server.Client(),
			},
			func(value uint64) {
				values <- value
				cancel()
			},
		)
	}()
	select {
	case value := <-values:
		if value != 98765432 {
			t.Fatalf("external Core RSS = %d", value)
		}
	case <-time.After(time.Second):
		t.Fatal("external Core memory stream did not produce a value")
	}
	select {
	case <-errors:
	case <-time.After(time.Second):
		t.Fatal("external Core memory stream did not stop after cancellation")
	}
}

func TestTUITrafficUpdateAppliesLiveAndTotalCounters(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, false)
	_, command := model.Update(tuiTrafficMsg{
		update: tuiTrafficUpdate{
			Traffic: trafficSnapshot{
				Up:        1024,
				Down:      2048,
				UpTotal:   4096,
				DownTotal: 8192,
			},
		},
	})
	if command != nil {
		t.Fatal("model without a traffic monitor scheduled another monitor read")
	}
	if model.snapshot.Traffic.Up != 1024 ||
		model.snapshot.Traffic.Down != 2048 ||
		model.snapshot.TotalTraffic.Up != 4096 ||
		model.snapshot.TotalTraffic.Down != 8192 {
		t.Fatalf("traffic update was not applied: %+v", model.snapshot)
	}
	if len(model.snapshot.TrafficHistory) != 1 ||
		model.snapshot.TrafficHistory[0].Up != 1024 ||
		model.snapshot.TrafficHistory[0].Down != 2048 {
		t.Fatalf("traffic history was not updated: %+v", model.snapshot.TrafficHistory)
	}
}

func TestTUITrafficHistoryKeepsLatestThirtySamples(t *testing.T) {
	history := []trafficSnapshot{}
	for index := 0; index < tuiTrafficHistoryLimit+7; index++ {
		history = appendTUITrafficHistory(history, trafficSnapshot{
			Up:   int64(index),
			Down: int64(index * 2),
		})
	}
	if len(history) != tuiTrafficHistoryLimit {
		t.Fatalf("traffic history length = %d", len(history))
	}
	if history[0].Up != 7 || history[len(history)-1].Up != 36 {
		t.Fatalf("traffic history retained the wrong range: %+v", history)
	}
}

func TestTUITrafficChartOverlaysUploadAndDownload(t *testing.T) {
	history := make([]trafficSnapshot, tuiTrafficHistoryLimit)
	for index := range history {
		history[index] = trafficSnapshot{
			Up:   int64(index * 1024),
			Down: int64((tuiTrafficHistoryLimit - index) * 2048),
		}
	}
	chart := buildTUITrafficChart(history, 32, 4)
	if chart.peak != int64(tuiTrafficHistoryLimit*2048) {
		t.Fatalf("traffic chart peak = %d", chart.peak)
	}
	if len(chart.lines) != 4 {
		t.Fatalf("traffic chart line count = %d", len(chart.lines))
	}
	rendered := strings.Join(chart.lines, "\n")
	if !strings.Contains(rendered, tuiTrafficChartUpload) ||
		!strings.Contains(rendered, tuiTrafficChartDownload) ||
		!strings.Contains(rendered, tuiTrafficChartOverlap) {
		t.Fatalf("traffic chart does not contain both series and overlap: %q", rendered)
	}
	if tuiTrafficChartUpload != "\x1b[38;5;33m" ||
		tuiTrafficChartDownload != tuiGreen ||
		tuiTrafficChartOverlap != tuiCyan ||
		strings.Contains(rendered, "\x1b[97m") {
		t.Fatalf("traffic chart colors are not blue/green/cyan: %q", rendered)
	}
	empty := buildTUITrafficChart(nil, 12, 3)
	if strings.Contains(stripTUIANSI(empty.lines[0]), "·") ||
		strings.Contains(stripTUIANSI(empty.lines[1]), "·") ||
		stripTUIANSI(empty.lines[2]) != strings.Repeat("·", 12) ||
		!strings.Contains(empty.lines[2], tuiTrafficChartBaseline) {
		t.Fatalf("traffic chart baseline is not white dotted: %q", empty.lines)
	}
}

func TestTUITrafficLegendMatchesSeriesColorsAndResetsBorder(t *testing.T) {
	traffic := trafficSnapshot{Up: 1024, Down: 2048}
	legend := formatTUITrafficLegend(traffic, 4096)
	if !strings.Contains(
		legend,
		tuiTrafficChartUpload+"↑ 1.0 KB/s"+tuiReset,
	) || !strings.Contains(
		legend,
		tuiTrafficChartDownload+"↓ 2.0 KB/s"+tuiReset,
	) {
		t.Fatalf("traffic legend does not match series colors: %q", legend)
	}
	example := formatTUITrafficLegend(
		trafficSnapshot{Up: 1021, Down: 18},
		2048,
	)
	if !strings.Contains(
		example,
		tuiTrafficChartUpload+"↑ 1021.0 B/s"+tuiReset,
	) || !strings.Contains(
		example,
		tuiTrafficChartDownload+"↓ 18.0 B/s"+tuiReset,
	) {
		t.Fatalf("live traffic values do not match chart colors: %q", example)
	}

	var output strings.Builder
	tuiTrafficTitle(&output, traffic, 4096, 60)
	lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[1], tuiReset+"│") {
		t.Fatalf("traffic title color polluted its border: %q", output.String())
	}
}

func TestTUICompactTrafficChartAdaptsToViewportHeight(t *testing.T) {
	for _, test := range []struct {
		height int
		want   int
	}{
		{height: 8, want: 1},
		{height: 10, want: 1},
		{height: 11, want: 2},
		{height: 14, want: 2},
		{height: 15, want: 3},
		{height: 30, want: 3},
	} {
		if got := tuiCompactTrafficChartHeight(test.height); got != test.want {
			t.Fatalf(
				"compact chart height at %d = %d, want %d",
				test.height,
				got,
				test.want,
			)
		}
	}
}

func TestTUIRefreshKeepsNewerTrafficHistory(t *testing.T) {
	current := tuiSnapshot{
		Traffic: trafficSnapshot{Up: 30, Down: 40},
		TrafficHistory: []trafficSnapshot{
			{Up: 10, Down: 20},
			{Up: 30, Down: 40},
		},
		TotalTraffic: trafficSnapshot{Up: 50, Down: 60},
	}
	refreshed := tuiSnapshot{
		Traffic:        trafficSnapshot{Up: 1, Down: 2},
		TrafficHistory: []trafficSnapshot{{Up: 1, Down: 2}},
		TotalTraffic:   trafficSnapshot{Up: 3, Down: 4},
	}
	merged := mergeTUIRefresh(current, refreshed)
	if merged.Traffic != current.Traffic ||
		merged.TotalTraffic != current.TotalTraffic ||
		!slices.Equal(merged.TrafficHistory, current.TrafficHistory) {
		t.Fatalf("refresh restored stale traffic: %+v", merged)
	}
}

func TestTUIRefreshSelectsImportWhenLastProfileDisappears(t *testing.T) {
	current := tuiSnapshot{
		SelectedRow: 0,
		Profiles: []tuiProfile{{
			Name: "deleted.yaml",
			Path: "/tmp/deleted.yaml",
		}},
	}
	merged := mergeTUIRefresh(current, tuiSnapshot{})
	if merged.SelectedRow != tuiProfileImportSubscriptionRow {
		t.Fatalf(
			"empty Profile refresh selected row %d, want subscription import",
			merged.SelectedRow,
		)
	}
	merged = mergeTUIRefresh(current, tuiSnapshot{Profiles: []tuiProfile{{
		Name: "other.yaml",
		Path: "/tmp/other.yaml",
	}}})
	if merged.SelectedRow != tuiProfileImportSubscriptionRow {
		t.Fatalf(
			"Profile refresh selected unrelated profile row %d instead of import",
			merged.SelectedRow,
		)
	}
}

func TestTUITrafficStreamOutlivesControllerRequestTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/traffic" {
			t.Fatalf("traffic path = %q", request.URL.Path)
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test response does not support flushing")
		}
		select {
		case <-time.After(900 * time.Millisecond):
		case <-request.Context().Done():
			return
		}
		_, _ = io.WriteString(
			w,
			"{\"up\":1024,\"down\":2048,\"upTotal\":4096,\"downTotal\":8192}\n",
		)
		flusher.Flush()
		<-request.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	values := make(chan trafficSnapshot, 1)
	errors := make(chan error, 1)
	baseClient := server.Client()
	go func() {
		errors <- streamTUITraffic(
			ctx,
			controllerClient{
				options: controllerOptions{address: server.URL},
				client: &http.Client{
					Transport: baseClient.Transport,
					Timeout:   750 * time.Millisecond,
				},
			},
			func(value trafficSnapshot) {
				values <- value
				cancel()
			},
		)
	}()
	select {
	case value := <-values:
		if value.Up != 1024 || value.Down != 2048 ||
			value.UpTotal != 4096 || value.DownTotal != 8192 {
			t.Fatalf("traffic stream value = %+v", value)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("traffic stream was stopped by the short controller timeout")
	}
	select {
	case <-errors:
	case <-time.After(time.Second):
		t.Fatal("traffic stream did not stop after cancellation")
	}
}

func TestTUIDashboardRendersEmbeddedAndExternalMemory(t *testing.T) {
	base := populatedTUISnapshot(tuiPageDashboard)
	base.Memory = tuiMemoryInfo{
		SystemTotal: 8 << 30,
		SystemUsed:  4 << 30,
		ProcessRSS:  120 << 20,
		GoHeap:      48 << 20,
		UpdatedAt:   time.Now(),
	}
	var embedded strings.Builder
	drawTUIDashboard(
		&embedded,
		base,
		cliPaths{ConfigPath: "/tmp/config.yaml"},
		100,
		26,
	)
	embeddedPlain := stripTUIANSI(embedded.String())
	for _, expected := range []string{
		"memory refresh 2s",
		"System memory 4.0 GB / 8.0 GB  50.0%",
		"CLI + Mihomo  120.0 MB RSS · shared process",
		"Go heap       48.0 MB",
	} {
		if !strings.Contains(embeddedPlain, expected) {
			t.Fatalf("embedded Dashboard does not contain %q:\n%s", expected, embeddedPlain)
		}
	}

	externalSnapshot := base
	externalSnapshot.ExternalCore = true
	externalSnapshot.Memory.ExternalCore = true
	externalSnapshot.Memory.CoreRSS = 256 << 20
	externalSnapshot.Memory.CoreUpdated = time.Now()
	var external strings.Builder
	drawTUIDashboard(
		&external,
		externalSnapshot,
		cliPaths{ConfigPath: "/tmp/config.yaml"},
		100,
		26,
	)
	externalPlain := stripTUIANSI(external.String())
	for _, expected := range []string{
		"TUI process   120.0 MB",
		"External Core 256.0 MB",
	} {
		if !strings.Contains(externalPlain, expected) {
			t.Fatalf("external Dashboard does not contain %q:\n%s", expected, externalPlain)
		}
	}
}

func TestTUIDashboardRendersAdaptiveLiveTrafficChart(t *testing.T) {
	snapshot := populatedTUISnapshot(tuiPageDashboard)
	for index := 0; index < tuiTrafficHistoryLimit; index++ {
		snapshot.TrafficHistory = append(snapshot.TrafficHistory, trafficSnapshot{
			Up:   int64((index + 1) * 1024),
			Down: int64((tuiTrafficHistoryLimit - index) * 2048),
		})
	}
	snapshot.Traffic = snapshot.TrafficHistory[len(snapshot.TrafficHistory)-1]
	output := renderTUIAtSize(
		snapshot,
		cliPaths{ConfigPath: "/tmp/config.yaml"},
		"private Unix socket",
		true,
		true,
		120,
		40,
	)
	plain := stripTUIANSI(output)
	for _, expected := range []string{
		"Live traffic",
		"30 samples",
		"Traffic total",
	} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("Dashboard traffic chart does not contain %q:\n%s", expected, plain)
		}
	}
	if !strings.Contains(output, tuiTrafficChartUpload+"↑ ") ||
		!strings.Contains(output, tuiTrafficChartDownload+"↓ ") {
		t.Fatalf("Dashboard traffic legend is missing series colors: %q", output)
	}
}

func TestTUIProxiesExposeSelectedAndWholeGroupDelayTests(t *testing.T) {
	snapshot := populatedTUISnapshot(tuiPageProxies)
	snapshot.FocusSidebar = false
	snapshot.SelectedGroup = 0
	snapshot.SelectedNode = 0
	snapshot.Groups = []tuiGroup{{
		Name:  "Proxy",
		Type:  "Selector",
		Now:   "fast",
		Nodes: []string{"fast", "slow", "dead", "new"},
		Delays: map[string]tuiDelayResult{
			"fast": {MedianMillis: 18, Samples: 5},
			"slow": {Testing: true},
			"dead": {Error: "timeout"},
		},
	}}
	var output strings.Builder
	drawTUIProxies(&output, snapshot, 100, 24)
	plain := stripTUIANSI(output.String())
	for _, expected := range []string{
		"↑↓/ws group",
		"d test group",
		"Enter apply",
		"18 ms",
		"Testing...",
		"Timeout · d retry",
		"[d test]",
	} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("Proxies does not contain %q:\n%s", expected, plain)
		}
	}
	key, ok := tuiKeyFromTea(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'A'},
	})
	if !ok || key != tuiKeyDelayTestAll {
		t.Fatalf("A key = (%v, %v)", key, ok)
	}
}
