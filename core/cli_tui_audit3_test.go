//go:build linux && !cgo && cli

package main

import (
	"context"
	"core/internal/i18n"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAudit3RefreshKeepsManualDelayAndDropsRemovedNodes(t *testing.T) {
	manual := tuiDelayResult{MedianMillis: 120, MinMillis: 100, MaxMillis: 150, JitterMillis: 50, Samples: 5}
	current := []tuiGroup{{Name: "group", Nodes: []string{"node", "removed"}, Delays: map[string]tuiDelayResult{"node": manual, "removed": manual}, Speeds: map[string]tuiSpeedResult{"removed": {BytesPerSecond: 100}}}}
	updated := []tuiGroup{{Name: "group", Nodes: []string{"node"}, Delays: map[string]tuiDelayResult{"node": {MedianMillis: 50, Samples: 1}}, Speeds: map[string]tuiSpeedResult{}}}
	mergeTUIGroupDelays(current, updated)
	if got := updated[0].Delays["node"]; got != manual {
		t.Fatalf("controller single sample replaced manual measurement: %+v", got)
	}
	if _, exists := updated[0].Delays["removed"]; exists {
		t.Fatal("removed node kept stale delay")
	}
	if _, exists := updated[0].Speeds["removed"]; exists {
		t.Fatal("removed node kept stale speed")
	}
}

func audit3ExitModel(t *testing.T) *tuiModel {
	t.Helper()
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir(), ConfigPath: "/tmp/audit3-no-such-profile.yaml"}, nil, true)
	model.coreRunning = true
	model.snapshot.Settings.Mode = "rule"
	model.snapshot.Settings.MixedPort, model.snapshot.ActiveProxyPort = 7890, 7890
	model.snapshot.Groups = []tuiGroup{{Name: "group", Now: "old", Nodes: []string{"old", "new"}}}
	model.syncNetworkExit()
	return model
}

func TestAudit3SameListenerNewNodeRejectsOldPublicIP(t *testing.T) {
	model := audit3ExitModel(t)
	model.snapshot.Network = tuiNetworkInfo{PublicIP: "192.0.2.1", IntranetIP: "192.168.1.2", CheckedAt: time.Now()}
	model.snapshot.DashboardDelay = tuiDelayResult{Samples: 5, MedianMillis: 100}
	model.snapshot.DashboardSpeed = tuiSpeedResult{BytesPerSecond: 1000}
	model.startNetworkCheck()
	sequence, generation, route := model.networkCheckSequence, model.networkExitGeneration, model.networkCheckRoute()
	model.snapshot.Groups[0].Now = "new"
	if !model.syncNetworkExit() || model.networkCheckRoute() != route {
		t.Fatal("node change on same listener did not advance exit generation")
	}
	_, command := model.update(tuiNetworkResultMsg{route: route, sequence: sequence, generation: generation, info: tuiNetworkInfo{PublicIP: "192.0.2.1", CheckedAt: time.Now()}})
	if command == nil || !model.networkCheckActive || model.snapshot.Network.PublicIP != "" || model.snapshot.Network.IntranetIP != "192.168.1.2" || model.snapshot.DashboardDelay.Samples != 0 || model.snapshot.DashboardSpeed.BytesPerSecond != 0 {
		t.Fatal("old exit metrics survived a node change or did not refresh")
	}
	model.update(tuiNetworkResultMsg{route: route, sequence: model.networkCheckSequence, generation: model.networkExitGeneration, info: tuiNetworkInfo{PublicIP: "192.0.2.2", CheckedAt: time.Now()}})
	if model.snapshot.Network.PublicIP != "192.0.2.2" || model.networkCheckActive {
		t.Fatal("new exit result was not applied")
	}
}

func TestAudit3IPRefreshCoalescesOnePendingRequest(t *testing.T) {
	model := audit3ExitModel(t)
	model.startNetworkCheck()
	for range 3 {
		if model.startNetworkCheck() != nil {
			t.Fatal("parallel IP request was started")
		}
	}
	sequence, route := model.networkCheckSequence, model.networkCheckRoute()
	_, command := model.update(tuiNetworkResultMsg{route: route, sequence: sequence, generation: model.networkExitGeneration, info: tuiNetworkInfo{PublicIP: "192.0.2.1", CheckedAt: time.Now()}})
	if command == nil || model.networkCheckSequence != sequence+1 || !model.networkCheckActive || model.networkCheckPending {
		t.Fatal("pending IP refresh was lost or started more than once")
	}
	model.update(tuiNetworkResultMsg{route: route, sequence: sequence, generation: model.networkExitGeneration, info: tuiNetworkInfo{PublicIP: "192.0.2.99"}})
	if !model.networkCheckActive || model.snapshot.Network.PublicIP == "192.0.2.99" {
		t.Fatal("duplicate old result stopped the new IP request")
	}
	_, command = model.update(tuiNetworkResultMsg{route: route, sequence: sequence + 1, generation: model.networkExitGeneration, info: tuiNetworkInfo{PublicIP: "192.0.2.2", CheckedAt: time.Now()}})
	if command != nil || model.networkCheckActive || model.snapshot.Network.PublicIP != "192.0.2.2" {
		t.Fatal("coalesced refresh did not finish cleanly")
	}
}

func TestAudit3StaleOperationCannotRestoreOldExitOrClearNewSpinner(t *testing.T) {
	model := audit3ExitModel(t)
	model.snapshot.Network = tuiNetworkInfo{PublicIP: "192.0.2.1", CheckedAt: time.Now()}
	command := model.startOperation(func(state *tuiOperationState) {
		state.snapshot.DashboardDelay = tuiDelayResult{Samples: 5, MedianMillis: 20}
	})
	message := command().(tuiOperationResultMsg)
	model.snapshot.Groups[0].Now = "new"
	model.syncNetworkExit()
	model.update(message)
	if model.snapshot.Network.PublicIP != "" || model.snapshot.DashboardDelay.Samples != 0 || model.snapshot.Groups[0].Now != "new" || model.busy {
		t.Fatal("old operation restored previous exit metrics")
	}
	model.operationSequence++
	model.snapshot.DashboardDelay = tuiDelayResult{Testing: true}
	model.busy = true
	model.update(message)
	if !model.busy || !model.snapshot.DashboardDelay.Testing {
		t.Fatal("old operation cleared a newer operation's busy state")
	}
}

func TestAudit3SSHLocalRefreshOnlyUsesLocalSavedState(t *testing.T) {
	useTestCLIRuntimeDirectory(t)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveCLISSHConfigLockedForAudit3(cliSSHConfig{Profiles: []cliSSHProfile{{Name: "one", Username: "user", Host: "192.0.2.1", Port: 22}}}); err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.snapshot.Page, model.snapshot.SelectedSSH = tuiPageSSH, tuiSSHCaptureRow
	command := model.startSSHLocalRefresh()
	if command == nil {
		t.Fatal("SSH page did not start local state refresh")
	}
	model.update(command())
	if len(model.snapshot.SSHProfiles) != 1 || model.snapshot.SelectedSSH != tuiSSHCaptureRow || model.networkCheckActive || model.refreshInFlight {
		t.Fatal("local SSH refresh changed focus or scheduled HTTP/exit probes")
	}
	model.snapshot.SSHDetailName, model.snapshot.SelectedSSH = "one", 0
	model.snapshot.SSHNetwork = tuiNetworkInfo{PublicIP: "192.0.2.99"}
	if err := saveCLISSHConfigLockedForAudit3(cliSSHConfig{Profiles: []cliSSHProfile{{Name: "two", Username: "user", Host: "192.0.2.2", Port: 22}}}); err != nil {
		t.Fatal(err)
	}
	model.update(model.startSSHLocalRefresh()())
	if len(model.snapshot.SSHProfiles) != 1 || model.snapshot.SSHProfiles[0].Name != "two" || model.snapshot.SSHDetailName != "" || model.snapshot.SSHNetwork.PublicIP != "" {
		t.Fatal("external SSH configuration change was not reconciled")
	}
}

func saveCLISSHConfigLockedForAudit3(config cliSSHConfig) error {
	return updateCLISSHConfig(func(current *cliSSHConfig) error { *current = config; return nil })
}

func TestAudit3DashboardUsesOptionalHistoryCountAndTunIntent(t *testing.T) {
	count, requested := 42, true
	for _, language := range i18n.Languages() {
		snapshot := tuiSnapshot{ManagedService: true, Language: language.Code}
		applyTUIBackendDisplay(&snapshot, tuiServiceStatus{HistoryCount: &count, TunRequested: &requested, Mode: "rule", TunState: "off"})
		if snapshot.Settings.TunEnabled || !tuiRequestedTun(snapshot) {
			t.Fatal("TUN intent was confused with active TUN")
		}
		fields := tuiDashboardOverviewFields(snapshot, cliPaths{}, language.Code)
		if !strings.Contains(fields[len(fields)-3].value, "42") {
			t.Fatalf("%s Dashboard ignored aggregate history count", language.Code)
		}
		applyTUIBackendDisplay(&snapshot, tuiServiceStatus{Mode: "rule", TunState: "off"})
		fields = tuiDashboardOverviewFields(snapshot, cliPaths{}, language.Code)
		if !strings.Contains(fields[len(fields)-3].value, "?") || strings.Contains(fields[len(fields)-3].value, "%!") {
			t.Fatalf("%s missing history count was not unknown: %s", language.Code, fields[len(fields)-3].value)
		}
	}
}

func TestAudit3QuitCancelsOnlyPendingFrontendSSHContext(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.coreRunning = true
	model.handleKey(tuiKeyQuit)
	if model.sshOperationContext.Err() != context.Canceled || !model.coreRunning || model.shutdownRequested {
		t.Fatal("q did not cancel pending frontend operations independently of backend")
	}
}

func TestAudit3DetailScrollSurvivesRefreshAndResetsWithSelection(t *testing.T) {
	current := tuiSnapshot{Page: tuiPageConnections, SelectedConnection: 0, ConnectionsDetailOpen: true, ConnectionsDetailScroll: 7,
		Connections: []tuiConnection{{ID: "selected", Host: strings.Repeat("host", 400)}, {ID: "other"}}}
	updated := current
	updated.Connections = []tuiConnection{current.Connections[1], current.Connections[0]}
	merged := mergeTUIRefresh(current, updated)
	if merged.SelectedConnection != 1 || merged.ConnectionsDetailScroll != 7 || !merged.ConnectionsDetailOpen {
		t.Fatal("same detail lost scroll after asynchronous reordering")
	}
	moveTUIConnectionMatch(&merged, 1)
	if merged.ConnectionsDetailScroll != 0 {
		t.Fatal("new detail inherited old item's scroll")
	}
	current.Logs, current.SelectedLog, current.LogDetailOpen, current.LogDetailScroll = []string{"selected line"}, 0, true, 4
	updated = current
	updated.Logs = []string{"replacement line"}
	merged = mergeTUIRefresh(current, updated)
	if merged.LogDetailOpen || merged.LogDetailScroll != 0 {
		t.Fatal("removed log retained an invisible detail scroll")
	}
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.snapshot = current
	model.width, model.height = 80, 15
	model.snapshot.ConnectionsDetailScroll = 1_000_000
	model.reflowTUI()
	if model.snapshot.ConnectionsDetailScroll >= 1_000_000 {
		t.Fatal("resize did not clamp detail to visible content")
	}
	model.handleKey(tuiKeyBack)
	if model.snapshot.ConnectionsDetailScroll != 0 || model.snapshot.ConnectionsDetailOpen {
		t.Fatal("Esc did not reset detail viewport")
	}
}

func TestAudit3WholeGroupStaleSpeedResultFinishesOnlyOwnedIndicators(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.snapshot.Groups = []tuiGroup{{Name: "group", Nodes: []string{"node"}, Speeds: map[string]tuiSpeedResult{"node": {Testing: true}}}}
	model.operationSequence, model.backendRevision, model.busy = 3, 2, true
	model.groupSpeedIndicators = captureTUIOperationTestingIndicators(model.snapshot)
	model.update(tuiProxyGroupSpeedResultMsg{operationID: 3, backendRevision: 1, groupName: "group", node: "node", result: tuiSpeedResult{BytesPerSecond: 999}})
	if model.busy || model.snapshot.Groups[0].Speeds["node"].Testing || model.snapshot.Groups[0].Speeds["node"].BytesPerSecond != 0 {
		t.Fatal("stale group-speed result remained testing or was applied")
	}
	model.busy = true
	model.operationSequence = 4
	model.snapshot.Groups[0].Speeds["node"] = tuiSpeedResult{Testing: true}
	model.update(tuiProxyGroupSpeedResultMsg{operationID: 3, backendRevision: 1, groupName: "group", node: "node"})
	if !model.busy || !model.snapshot.Groups[0].Speeds["node"].Testing {
		t.Fatal("old group-speed chain cleared new operation")
	}
}

func TestAudit3SSHMeasurementSequenceRejectsEarlierSameProfileResult(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.snapshot.SSHDetailName = "profile"
	model.sshDelaySequence[0], model.sshSpeedSequence[0] = 2, 2
	model.snapshot.SSHDelay, model.snapshot.SSHSpeed = tuiDelayResult{Testing: true}, tuiSpeedResult{Testing: true}
	model.update(tuiSSHDelayResultMsg{name: "profile", sequence: 1, result: tuiDelayResult{MedianMillis: 999}})
	model.update(tuiSSHSpeedResultMsg{name: "profile", sequence: 1, result: tuiSpeedResult{BytesPerSecond: 999}})
	if !model.snapshot.SSHDelay.Testing || !model.snapshot.SSHSpeed.Testing {
		t.Fatal("earlier same-profile measurement cleared later spinner")
	}
	model.update(tuiSSHDelayResultMsg{name: "profile", sequence: 2, result: tuiDelayResult{MedianMillis: 20, Samples: 5}})
	model.update(tuiSSHSpeedResultMsg{name: "profile", sequence: 2, result: tuiSpeedResult{BytesPerSecond: 1000}})
	if model.snapshot.SSHDelay.Testing || model.snapshot.SSHSpeed.Testing || model.snapshot.SSHDelay.MedianMillis != 20 || model.snapshot.SSHSpeed.BytesPerSecond != 1000 {
		t.Fatal("current same-profile measurement was not applied")
	}
}

func TestAudit3RenderingSanitizesOpaqueTextAndKeepsTrustedChartANSI(t *testing.T) {
	unsafe := "keep\x1b[2J\x1b]52;c;ZXZpbA==\a\x1bPpayload\x1b\\\r\nnext"
	for _, language := range i18n.Languages() {
		for _, page := range []tuiPage{tuiPageDashboard, tuiPageSSH, tuiPageProxies, tuiPageProfiles, tuiPageRequests, tuiPageConnections, tuiPageLogs} {
			snapshot := populatedTUISnapshot(page)
			snapshot.Language = language.Code
			snapshot.FocusSidebar = false
			snapshot.Network.PublicIP, snapshot.Network.IntranetIP = unsafe, unsafe
			snapshot.FLCOutbound = unsafe
			snapshot.Groups[0].Name, snapshot.Groups[0].Now = unsafe, unsafe
			snapshot.Groups[0].Nodes = []string{unsafe}
			snapshot.Profiles = []tuiProfile{{Name: unsafe, Path: unsafe}}
			snapshot.SSHProfiles = []tuiSSHProfile{{Name: unsafe, Host: unsafe, Destination: unsafe, Identity: unsafe, LastError: unsafe}}
			snapshot.SSHDetailName, snapshot.SelectedSSH = unsafe, 0
			snapshot.Connections = []tuiConnection{{ID: unsafe, Host: unsafe, Process: unsafe, ProcessPath: unsafe, Chain: unsafe}}
			snapshot.Requests = []tuiRequest{{TuiConnection: snapshot.Connections[0]}}
			snapshot.Logs = []string{unsafe}
			snapshot.Notifications = []tuiNotification{{message: unsafe, title: unsafe}}
			frame := renderTUIAtSize(snapshot, cliPaths{HomeDir: unsafe, ConfigPath: unsafe}, unsafe, true, true, 140, 45)
			for _, forbidden := range []string{"\x1b[2J", "\x1b]52", "\x1bP", "payload", "\r"} {
				if strings.Contains(frame, forbidden) {
					t.Fatalf("%s page %d leaked control sequence %q", language.Code, page, forbidden)
				}
			}
			if snapshot.Groups[0].Name != unsafe || snapshot.Connections[0].ID != unsafe {
				t.Fatal("rendering changed backend identifiers")
			}
			if page == tuiPageDashboard && (!strings.Contains(frame, tuiTrafficChartUpload) || !strings.Contains(frame, tuiTrafficChartDownload)) {
				t.Fatal("safe opaque text removed trusted traffic colors")
			}
		}
	}
}

func TestAudit3FLCIdentityPreservesWhitespaceAndCase(t *testing.T) {
	const group, node = " Proxy ", " Node "
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.snapshot.FLCOutbound = group
	model.snapshot.Groups = []tuiGroup{{Name: "Proxy", Now: "other", Nodes: []string{"other"}}, {Name: group, Now: node, Nodes: []string{"Node", node}}}
	model.openProxiesForFLCOutbound()
	if model.snapshot.SelectedGroup != 1 || model.snapshot.SelectedNode != 1 {
		t.Fatal("FLC focus stripped group/node identity")
	}
	if got := tuiFLCOutboundLabel(model.snapshot); got != group+" → "+node {
		t.Fatalf("FLC display resolved the wrong group: %q", got)
	}
	model.snapshot.FLCOutbound = "proxy"
	if got := tuiFLCOutboundLabel(model.snapshot); got != "proxy" {
		t.Fatalf("FLC display used case-insensitive group identity: %q", got)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		_, _ = w.Write([]byte(`{"proxies":{"Proxy":{"now":"other"}," Proxy ":{"now":" Node "}}}`))
	}))
	defer server.Close()
	client := controllerClient{options: controllerOptions{address: server.URL}, client: server.Client()}
	if got := tuiProxyGroupNow(client, group); got != node {
		t.Fatalf("controller lookup stripped group/node identity: %q", got)
	}
}

func TestAudit3LogLevelsUseMetadataNotMessageWords(t *testing.T) {
	logs := []string{
		"12:34:56 WARNING Something happened",
		"time=2026-10-07T12:34:56Z level=warning msg=warning message",
		"12:34:56 INFO server returned ERROR to a request",
		"time=2026-10-07T12:34:56Z level=info msg=\"payload LEVEL=ERROR\"",
	}
	warnings := matchedTUILogIndexes(tuiSnapshot{Logs: logs, LogsLevel: "WARN"})
	if len(warnings) != 2 || warnings[0] != 0 || warnings[1] != 1 {
		t.Fatalf("WARN/WARNING metadata was not normalized: %v", warnings)
	}
	if errors := matchedTUILogIndexes(tuiSnapshot{Logs: logs, LogsLevel: "ERROR"}); len(errors) != 0 {
		t.Fatalf("message words were mistaken for log level: %v", errors)
	}
	if tuiLogLineColor(logs[2]) != tuiCyan || tuiLogLineColor(logs[3]) != tuiCyan {
		t.Fatal("message words changed INFO color")
	}
}

func TestAudit3DetailHasViewportAndVisibleBottom(t *testing.T) {
	for _, page := range []tuiPage{tuiPageRequests, tuiPageConnections, tuiPageLogs} {
		snapshot := tuiSnapshot{Page: page, SelectedRequest: 0, SelectedConnection: 0, SelectedLog: 0}
		connection := tuiConnection{ID: "id", Host: strings.Repeat("host.", 100), ProcessPath: strings.Repeat("path/", 100)}
		snapshot.Requests = []tuiRequest{{TuiConnection: connection, FirstSeen: time.Now(), LastSeen: time.Now()}}
		snapshot.Connections = []tuiConnection{connection}
		var log strings.Builder
		for index := 0; index < 300; index++ {
			fmt.Fprintf(&log, "log text %03d ", index)
		}
		snapshot.Logs = []string{log.String() + "END-OF-LOG"}
		snapshot.HistoryDetailOpen = page == tuiPageRequests
		snapshot.ConnectionsDetailOpen = page == tuiPageConnections
		snapshot.LogDetailOpen = page == tuiPageLogs
		model := newTUIModel(controllerClient{}, cliPaths{}, nil, false)
		model.width, model.height = 80, 16
		model.snapshot = snapshot
		before := tuiRenderPage(model.snapshot, model.paths, 76, 12)
		if lines := strings.Split(strings.TrimSuffix(before, "\n"), "\n"); len(lines) != 12 || !strings.Contains(stripTUIANSI(lines[len(lines)-1]), "┘") {
			t.Fatalf("page %d detail did not retain its bottom at viewport height: %d lines", page, len(lines))
		}
		model.handleKey(tuiKeyPageDown)
		after := tuiRenderPage(model.snapshot, model.paths, 76, 12)
		if after == before {
			t.Fatalf("page %d PgDn did not scroll detail", page)
		}
		model.update(tea.WindowSizeMsg{Width: 100, Height: 40})
	}
}

func TestAudit3RejectedOperationFinishesTesting(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.DashboardDelay = tuiDelayResult{Testing: true}
	model.snapshot.DashboardSpeed = tuiSpeedResult{Testing: true}
	model.snapshot.Groups = []tuiGroup{{Name: "group", Nodes: []string{"node"}, Delays: map[string]tuiDelayResult{"node": {Testing: true}}, Speeds: map[string]tuiSpeedResult{"node": {Testing: true}}}}
	command := model.startOperation(func(state *tuiOperationState) {})
	message := command().(tuiOperationResultMsg)
	model.backendRevision++
	model.update(message)
	if model.busy || model.snapshot.DashboardDelay.Testing || model.snapshot.DashboardSpeed.Testing || model.snapshot.Groups[0].Delays["node"].Testing || model.snapshot.Groups[0].Speeds["node"].Testing {
		t.Fatal("discarded operation left a permanent Testing indicator")
	}
}
