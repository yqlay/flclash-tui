//go:build linux && !cgo && cli

package main

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// Labeled rows and the existing command each Enter must share:
//
//	Dashboard Core          flclash core start|stop
//	Dashboard System proxy  flclash sys on|off
//	Dashboard TUN           flclash tun on|off
//	Dashboard Mode          flclash mode
//	Dashboard FLC outbound  flclash proxy select (opens that group)
//	Dashboard Mixed port    flclash port
//	Dashboard Public IP     flclash net refresh
//	Dashboard Intranet      flclash net refresh
//	Dashboard Delay         flclash net delay
//	Dashboard Speed         flclash net speed
//	Dashboard overview      highlight only
//	Proxies node            flclash proxy select GROUP NODE
//	Profiles profile        flclash profile use
//	SSH profile             flclash ssh connect
//	SSH Tunnel              flclash ssh connect|disconnect
//	SSH HTTP/SOCKS          in-place port edit
//	SSH Proxy IP            same refresh as n; no CLI command
//	SSH Inet/Speed/Traffic  highlight only
//	Settings                flclash settings set
//	Maintenance backup      flclash config backup
//	Maintenance geo         flclash geo update
//	Maintenance reset       flclash traffic reset

func TestTUIContentCursorMatchesLabeledRows(t *testing.T) {
	model := newRowEnterModel(t)
	model.width = 141
	model.height = 46
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.SelectedDashboard = tuiDashboardServiceRow
	model.snapshot.TrafficHistory = []trafficSnapshot{{Up: 1000, Down: 4000}, {Up: 8000, Down: 2000}}
	count := tuiDashboardSelectableCount(model.cursorDashboardSnapshot())
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyUp})
	if model.snapshot.SelectedDashboard != count-1 {
		t.Fatalf("dashboard up wrapped to %d, want %d", model.snapshot.SelectedDashboard, count-1)
	}
	for index := 0; index < count; index++ {
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
		if model.snapshot.SelectedDashboard != index {
			t.Fatalf("dashboard down = %d, want %d", model.snapshot.SelectedDashboard, index)
		}
	}

	speed := renderAlignFrame(model, tuiDashboardSpeedRow)
	next := renderAlignFrame(model, tuiDashboardSpeedRow+1)
	speedLine, speedChart := alignHighlightAndChart(t, speed, "Cloudflare")
	nextLine, nextChart := alignHighlightAndChart(t, next, "")
	if speedChart <= speedLine || nextChart <= nextLine || nextLine <= speedLine || nextLine-speedLine > 6 {
		t.Fatalf("speed line %d chart %d; next line %d chart %d", speedLine, speedChart, nextLine, nextChart)
	}
	t.Logf("DASHBOARD highlight line %d %q chart %d\n%s", nextLine, alignHighlightText(next), nextChart, stripTUIANSI(next))

	ssh := newRowEnterModel(t)
	ssh.width = 141
	ssh.height = 46
	ssh.snapshot.Page = tuiPageSSH
	ssh.snapshot.SSHDashboardFocus = true
	ssh.snapshot.SSHDetailName = "row-enter"
	ssh.snapshot.SSHDetailRow = tuiSSHTunnelRow
	ssh.snapshot.SSHProfiles = []tuiSSHProfile{{
		Name: "row-enter", Connected: true, Ready: true, SocksPort: 1080,
	}}
	ssh.snapshot.SSHTrafficHistory = model.snapshot.TrafficHistory
	var frame string
	for index := 0; index < ssh.sshDetailSelectableCount(); index++ {
		if ssh.snapshot.SSHDetailRow != index {
			t.Fatalf("ssh detail row %d, want %d", ssh.snapshot.SSHDetailRow, index)
		}
		if index == tuiSSHProxyPortRow {
			frame = renderTUIAtSize(ssh.snapshot, ssh.paths, "", true, false, 141, 46)
		}
		pressRowEnterKey(ssh, tea.KeyMsg{Type: tea.KeyDown})
	}
	if ssh.snapshot.SSHDetailRow != tuiSSHTunnelRow {
		t.Fatalf("ssh detail did not wrap: %d", ssh.snapshot.SSHDetailRow)
	}
	line, chart := alignHighlightAndChart(t, frame, "HTTP/SOCKS")
	if chart >= 0 && chart < line {
		t.Fatalf("SSH chart line %d is before HTTP/SOCKS line %d", chart, line)
	}
	t.Logf("SSH highlight line %d %q chart %d\n%s", line, alignHighlightText(frame), chart, stripTUIANSI(frame))

	walkAlignPage(t, tuiPageProxies, func(m *tuiModel) int { return len(m.snapshot.Groups) })
	walkAlignPage(t, tuiPageProfiles, func(m *tuiModel) int {
		return len(m.snapshot.Profiles) + tuiProfileImportRowCount
	})
	walkAlignPage(t, tuiPageConnections, func(m *tuiModel) int { return len(m.snapshot.Connections) })
	walkAlignPage(t, tuiPageRequests, func(m *tuiModel) int { return len(m.snapshot.Requests) })
	walkAlignPage(t, tuiPageLogs, func(m *tuiModel) int { return len(m.snapshot.Logs) })
	walkAlignPage(t, tuiPageTools, func(m *tuiModel) int { return tuiSettingsRowCount })
	walkAlignPage(t, tuiPageMaintenance, func(m *tuiModel) int { return tuiMaintenanceRowCount })
}

func TestTUIEnterMatchesCLICommand(t *testing.T) {
	home := t.TempDir()
	configPath := filepath.Join(home, "config.yaml")
	if err := osWriteAlign(configPath, defaultTUIConfig); err != nil {
		t.Fatal(err)
	}
	office := filepath.Join(home, "office.yaml")
	if err := osWriteAlign(office, defaultTUIConfig); err != nil {
		t.Fatal(err)
	}
	var requests []tuiServiceRequest
	serveRowAlignService(t, func(request tuiServiceRequest) tuiServiceStatus {
		requests = append(requests, request)
		status := alignServiceStatus(7, configPath)
		status.HomeDir = home
		status.Running = true
		status.Mode = "rule"
		status.ProxyPort = 7890
		status.ActiveProxyPort = 7890
		status.ConfiguredProxyPort = 7890
		status.TunScope = tuiTunScopeUser
		status.TunState = "off"
		if request.Action == "set_system_proxy" && request.Enabled != nil {
			status.SystemProxy = *request.Enabled
		}
		if request.Action == "set_tun" {
			status.TunState = "on"
			if request.TunScope != "" {
				status.TunScope = request.TunScope
			}
		}
		if request.Action == "set_mode" {
			status.Mode = request.Mode
		}
		if request.Action == "apply_settings" && request.Settings != nil {
			status.ConfiguredProxyPort = request.Settings.MixedPort
			status.Revision = 8
		}
		if request.Action == "reload" || request.Action == "select_proxy" || request.Action == "backup_profile" || request.Action == "reset_traffic" {
			status.Revision = 8
		}
		return status
	})

	runtime, err := cliRuntimeDirectory()
	if err != nil {
		t.Fatal(err)
	}
	service := newTUIServiceClientAt(runtime)

	clear := func() { requests = nil }
	mutation := func(action string) tuiServiceRequest {
		t.Helper()
		for _, request := range requests {
			if request.Action == action {
				return request
			}
		}
		t.Fatalf("action %s missing in %v", action, requestActions(requests))
		return tuiServiceRequest{}
	}

	model := alignCommandModel(t, service, home, configPath)
	model.coreRunning = true
	model.snapshot.Settings.Mode = "rule"
	model.snapshot.Settings.MixedPort = 7890
	model.snapshot.ActiveProxyPort = 7890
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.SelectedDashboard = tuiDashboardServiceRow
	clear()
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	tuiStop := mutation("stop")
	clear()
	if err := coreCommand([]string{"stop"}); err != nil {
		t.Fatal(err)
	}
	cliStop := mutation("stop")
	if tuiStop.Action != cliStop.Action {
		t.Fatalf("core stop tui %+v cli %+v", tuiStop, cliStop)
	}

	model = alignCommandModel(t, service, home, configPath)
	model.coreRunning = true
	model.snapshot.Settings.Mode = "rule"
	model.snapshot.Settings.SystemProxy = false
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.SelectedDashboard = tuiDashboardSystemProxyRow
	clear()
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	tuiProxy := mutation("set_system_proxy")
	clear()
	if err := systemProxyCommand([]string{"on"}); err != nil {
		t.Fatal(err)
	}
	cliProxy := mutation("set_system_proxy")
	if tuiProxy.Enabled == nil || cliProxy.Enabled == nil || *tuiProxy.Enabled != *cliProxy.Enabled || !*cliProxy.Enabled {
		t.Fatalf("system proxy tui %+v cli %+v", tuiProxy.Enabled, cliProxy.Enabled)
	}

	model = alignCommandModel(t, service, home, configPath)
	model.coreRunning = true
	model.snapshot.Settings.Mode = "rule"
	model.snapshot.Settings.TunEnabled = false
	model.snapshot.Settings.TunScope = tuiTunScopeUser
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.SelectedDashboard = tuiDashboardTunRow
	clear()
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	tuiTun := mutation("set_tun")
	clear()
	if err := tunCommand([]string{"on"}); err != nil {
		t.Fatal(err)
	}
	cliTun := mutation("set_tun")
	if tuiTun.Enabled == nil || cliTun.Enabled == nil || *tuiTun.Enabled != *cliTun.Enabled || tuiTun.TunScope != cliTun.TunScope {
		t.Fatalf("tun tui %+v cli %+v", tuiTun, cliTun)
	}

	model = alignCommandModel(t, service, home, configPath)
	model.coreRunning = true
	model.snapshot.Settings.Mode = "rule"
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.SelectedDashboard = tuiDashboardModeRow
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	clear()
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	tuiMode := mutation("set_mode")
	clear()
	if err := modeCommand([]string{"direct"}); err != nil {
		t.Fatal(err)
	}
	cliMode := mutation("set_mode")
	if tuiMode.Mode != "direct" || cliMode.Mode != tuiMode.Mode {
		t.Fatalf("mode tui %q cli %q", tuiMode.Mode, cliMode.Mode)
	}

	model = alignCommandModel(t, service, home, configPath)
	model.coreRunning = true
	model.backendRevision = 7
	model.snapshot.Settings = tuiSettings{Mode: "rule", MixedPort: 7890, LogLevel: "info", UnifiedDelay: true, TCPConcurrent: true}
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.SelectedDashboard = tuiDashboardMixedPortRow
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	for _, r := range []rune("7891") {
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	clear()
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	tuiPort := mutation("apply_settings")
	clear()
	if err := portCommand([]string{"7891"}); err != nil {
		t.Fatal(err)
	}
	cliPort := mutation("apply_settings")
	if tuiPort.Settings == nil || cliPort.Settings == nil || tuiPort.Settings.MixedPort != 7891 || cliPort.Settings.MixedPort != tuiPort.Settings.MixedPort || tuiPort.Settings.AllowLAN != cliPort.Settings.AllowLAN || tuiPort.Settings.Mode != cliPort.Settings.Mode {
		t.Fatalf("port tui %+v cli %+v", tuiPort.Settings, cliPort.Settings)
	}

	var probes []string
	previousRoute := detectTUINetworkRouteForCommand
	detectTUINetworkRouteForCommand = func(proxyURL, label string) tuiNetworkInfo {
		probes = append(probes, proxyURL+"|"+label)
		return tuiNetworkInfo{PublicIP: "203.0.113.8", IntranetIP: "192.0.2.10", Route: label}
	}
	t.Cleanup(func() { detectTUINetworkRouteForCommand = previousRoute })
	model = alignCommandModel(t, service, home, configPath)
	model.coreRunning = true
	model.snapshot.Settings.Mode = "rule"
	model.snapshot.ActiveProxyPort = 7890
	model.snapshot.Settings.MixedPort = 7890
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.SelectedDashboard = tuiDashboardPublicIPRow
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	if err := networkCommand([]string{"refresh"}); err != nil {
		t.Fatal(err)
	}
	model.snapshot.SelectedDashboard = tuiDashboardIntranetRow
	settleRowEnter(model)
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	if len(probes) != 3 || probes[0] != probes[1] || probes[0] != probes[2] || !strings.Contains(probes[0], "127.0.0.1:7890") {
		t.Fatalf("network probes = %v", probes)
	}

	var delays []string
	previousDelay := testManagedRouteDelayForCommand
	testManagedRouteDelayForCommand = func(client *tuiServiceClient, port int, testURL string) (tuiDelayResult, error) {
		delays = append(delays, strings.Join([]string{itoaAlign(port), testURL}, "|"))
		return tuiDelayResult{MedianMillis: 12, Samples: 1, MinMillis: 12, MaxMillis: 12}, nil
	}
	t.Cleanup(func() { testManagedRouteDelayForCommand = previousDelay })
	model.snapshot.SelectedDashboard = tuiDashboardDelayRow
	model.snapshot.DashboardDelay = tuiDelayResult{}
	settleRowEnter(model)
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	if err := networkCommand([]string{"delay"}); err != nil {
		t.Fatal(err)
	}
	if len(delays) != 2 || delays[0] != delays[1] || !strings.HasPrefix(delays[0], "7890|") || !strings.Contains(delays[0], defaultCLITestURL) {
		t.Fatalf("delay calls = %v", delays)
	}
	if strings.Contains(strings.Join(probes, "\n"), "delay") {
		t.Fatal("delay reused the public IP probe")
	}

	var speeds []int
	previousSpeed := testManagedRouteSpeedForCommand
	testManagedRouteSpeedForCommand = func(client *tuiServiceClient, port int) (tuiSpeedResult, error) {
		speeds = append(speeds, port)
		return tuiSpeedResult{BytesPerSecond: 1000, Complete: true}, nil
	}
	t.Cleanup(func() { testManagedRouteSpeedForCommand = previousSpeed })
	model.snapshot.SelectedDashboard = tuiDashboardSpeedRow
	settleRowEnter(model)
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	if err := networkCommand([]string{"speed"}); err != nil {
		t.Fatal(err)
	}
	if len(speeds) != 2 || speeds[0] != 7890 || speeds[1] != 7890 {
		t.Fatalf("speed ports = %v", speeds)
	}
	before := len(delays)
	model.snapshot.SelectedDashboard = tuiDashboardOverviewStartRow
	settleRowEnter(model)
	if command := pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}); command != nil || len(delays) != before || len(speeds) != 2 {
		t.Fatal("overview enter started an operation")
	}

	model = alignCommandModel(t, service, home, configPath)
	model.snapshot.Page = tuiPageProxies
	model.snapshot.ProxyView = tuiProxyViewGroups
	model.snapshot.ProxyNodeFocus = true
	model.snapshot.Groups = []tuiGroup{{Name: "PROXY", Type: "Selector", Now: "DIRECT", Nodes: []string{"DIRECT"}}}
	model.snapshot.SelectedGroup = 0
	model.snapshot.SelectedNode = 0
	clear()
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	tuiSelect := mutation("select_proxy")
	clear()
	if err := proxyCommand([]string{"select", "PROXY", "DIRECT"}); err != nil {
		t.Fatal(err)
	}
	cliSelect := mutation("select_proxy")
	if tuiSelect.ProxyGroup != "PROXY" || tuiSelect.ProxyName != "DIRECT" || cliSelect.ProxyGroup != tuiSelect.ProxyGroup || cliSelect.ProxyName != tuiSelect.ProxyName {
		t.Fatalf("select tui %+v cli %+v", tuiSelect, cliSelect)
	}

	model = alignCommandModel(t, service, home, configPath)
	model.snapshot.Page = tuiPageProfiles
	model.snapshot.Profiles = []tuiProfile{{Name: "office.yaml", Path: office}}
	model.snapshot.SelectedRow = 0
	clear()
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	tuiProfile := mutation("reload")
	clear()
	if err := profileCommand([]string{"use", "office.yaml"}); err != nil {
		t.Fatal(err)
	}
	cliProfile := mutation("reload")
	if tuiProfile.ConfigPath != office || cliProfile.ConfigPath != tuiProfile.ConfigPath || tuiProfile.ExpectedSHA256 == "" || cliProfile.ExpectedSHA256 != tuiProfile.ExpectedSHA256 {
		t.Fatalf("profile tui %+v cli %+v", tuiProfile, cliProfile)
	}

	model = alignCommandModel(t, service, home, configPath)
	model.snapshot.Page = tuiPageTools
	model.snapshot.SelectedTool = tuiSettingsAllowLANRow
	model.snapshot.Settings = tuiSettings{Mode: "rule", MixedPort: 7890, LogLevel: "info", UnifiedDelay: true, TCPConcurrent: true}
	clear()
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	tuiSetting := mutation("apply_settings")
	clear()
	if err := settingsCommand([]string{"set", "allow-lan", "on"}); err != nil {
		t.Fatal(err)
	}
	cliSetting := mutation("apply_settings")
	if tuiSetting.Settings == nil || cliSetting.Settings == nil || !tuiSetting.Settings.AllowLAN || tuiSetting.Settings.AllowLAN != cliSetting.Settings.AllowLAN || tuiSetting.Settings.IPv6 != cliSetting.Settings.IPv6 || tuiSetting.Settings.LogLevel != cliSetting.Settings.LogLevel {
		t.Fatalf("allow-lan tui %+v cli %+v", tuiSetting.Settings, cliSetting.Settings)
	}

	model = alignCommandModel(t, service, home, configPath)
	model.snapshot.Page = tuiPageMaintenance
	model.snapshot.SelectedMaintenance = tuiMaintenanceBackupRow
	clear()
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	tuiBackup := mutation("backup_profile")
	clear()
	if err := configCommand([]string{"backup"}); err != nil {
		t.Fatal(err)
	}
	cliBackup := mutation("backup_profile")
	if tuiBackup.ConfigPath != configPath || cliBackup.ConfigPath != tuiBackup.ConfigPath {
		t.Fatalf("backup tui %s cli %s", tuiBackup.ConfigPath, cliBackup.ConfigPath)
	}

	var geos int
	previousGeo := updateGeoForCommand
	updateGeoForCommand = func(controllerClient) error {
		geos++
		return nil
	}
	t.Cleanup(func() { updateGeoForCommand = previousGeo })
	model.snapshot.SelectedMaintenance = tuiMaintenanceGeoUpdateRow
	settleRowEnter(model)
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	if err := geoCommand([]string{"update"}); err != nil {
		t.Fatal(err)
	}
	if geos != 2 {
		t.Fatalf("geo updates = %d", geos)
	}

	model.snapshot.SelectedMaintenance = tuiMaintenanceResetTrafficRow
	settleRowEnter(model)
	clear()
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	tuiReset := mutation("reset_traffic")
	clear()
	if err := trafficCommand([]string{"reset"}); err != nil {
		t.Fatal(err)
	}
	cliReset := mutation("reset_traffic")
	if tuiReset.Action != cliReset.Action {
		t.Fatalf("reset tui %+v cli %+v", tuiReset, cliReset)
	}

	var connected []string
	previousConnect := connectCLISSHProfileWithContextForCommand
	connectCLISSHProfileWithContextForCommand = func(_ context.Context, name string, _ cliSSHCredentials) (cliSSHTunnelState, bool, error) {
		connected = append(connected, name)
		return cliSSHTunnelState{Name: name, Port: 1080}, false, nil
	}
	t.Cleanup(func() { connectCLISSHProfileWithContextForCommand = previousConnect })
	model = alignCommandModel(t, service, home, configPath)
	model.snapshot.Page = tuiPageSSH
	model.snapshot.SSHProfiles = []tuiSSHProfile{{Name: "lab", Username: "user", Host: "203.0.113.10", SocksPort: 1080}}
	model.snapshot.SelectedSSH = 0
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	if _, _, err := connectCLISSHProfile("lab"); err != nil {
		t.Fatal(err)
	}
	if len(connected) != 2 || connected[0] != "lab" || connected[1] != "lab" {
		t.Fatalf("connect calls = %v", connected)
	}

	var disconnected []string
	previousDisconnect := disconnectCLISSHProfileForCommand
	disconnectCLISSHProfileForCommand = func(name string) (cliSSHTunnelState, bool, error) {
		disconnected = append(disconnected, name)
		return cliSSHTunnelState{Name: "lab"}, true, nil
	}
	t.Cleanup(func() { disconnectCLISSHProfileForCommand = previousDisconnect })
	model.snapshot.SSHDashboardFocus = true
	model.snapshot.SSHDetailName = "lab"
	model.snapshot.SSHDetailRow = tuiSSHTunnelRow
	model.snapshot.SSHProfiles[0].Connected = true
	model.snapshot.SSHProfiles[0].Ready = true
	settleRowEnter(model)
	drainTea(pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}))
	if err := cliSSHDisconnectCommand(nil); err != nil {
		t.Fatal(err)
	}
	if len(disconnected) != 2 || disconnected[0] != "lab" || disconnected[1] != "" {
		t.Fatalf("disconnect calls = %v", disconnected)
	}

	model.snapshot.SSHDetailRow = tuiSSHProxyPortRow
	settleRowEnter(model)
	if command := pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}); command != nil || !model.sshProxyPortEditing || len(disconnected) != 2 {
		t.Fatal("HTTP/SOCKS enter did not stay an in-place edit")
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEsc})
	model.snapshot.SSHDetailRow = tuiSSHProxyIPRow
	command := pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil || !model.snapshot.SSHNetwork.Loading || len(disconnected) != 2 {
		t.Fatal("proxy IP enter did not start the proxy-IP refresh")
	}
	model.snapshot.SSHDetailRow = tuiSSHSpeedRow
	settleRowEnter(model)
	if command := pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}); command != nil || model.snapshot.SSHDelay.Testing || model.snapshot.SSHSpeed.Testing || model.sshBusy {
		t.Fatal("SSH speed enter started a test")
	}
}

func alignCommandModel(t *testing.T, service *tuiServiceClient, home, configPath string) *tuiModel {
	t.Helper()
	model := newTUIModel(controllerClient{options: controllerOptions{address: "127.0.0.1:1"}}, cliPaths{HomeDir: home, ConfigPath: configPath}, nil, true)
	model.service = service
	model.backendRevision = 7
	model.snapshot.FocusSidebar = false
	model.snapshot.Language = "en"
	model.width = 141
	model.height = 46
	return model
}

func renderAlignFrame(model *tuiModel, row int) string {
	snapshot := model.snapshot
	snapshot.SelectedDashboard = row
	snapshot.FocusSidebar = false
	return renderTUIAtSize(snapshot, model.paths, "", true, false, model.width, model.height)
}

func alignHighlightAndChart(t *testing.T, rendered, label string) (int, int) {
	t.Helper()
	highlight, chart := -1, -1
	for index, line := range strings.Split(rendered, "\n") {
		if highlight < 0 && strings.Contains(line, tuiSelect) {
			if label == "" || strings.Contains(stripTUIANSI(line), label) || label == "Cloudflare" && strings.Contains(stripTUIANSI(line), "Cloudflare") {
				highlight = index
			}
		}
		if chart < 0 && tuiHasBraille(line) {
			chart = index
		}
	}
	if highlight < 0 {
		t.Fatalf("no highlight for %q", label)
	}
	return highlight, chart
}

func walkAlignPage(t *testing.T, page tuiPage, count func(*tuiModel) int) {
	t.Helper()
	model := newRowEnterModel(t)
	model.snapshot = populatedTUISnapshot(page)
	model.snapshot.FocusSidebar = false
	model.snapshot.Language = "en"
	model.snapshot.ProxyNodeFocus = false
	model.snapshot.SelectedGroup = 0
	model.snapshot.SelectedRow = tuiProfileImportSubscriptionRow
	model.snapshot.SelectedConnection = 0
	model.snapshot.SelectedRequest = 0
	model.snapshot.SelectedLog = 0
	model.snapshot.SelectedTool = 0
	model.snapshot.SelectedMaintenance = 0
	total := count(model)
	if total < 2 {
		t.Fatalf("page %d has %d rows", page, total)
	}
	if alignPageIndex(model) != 0 {
		t.Fatalf("page %d did not start at 0: %d", page, alignPageIndex(model))
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyUp})
	if alignPageIndex(model) != total-1 {
		t.Fatalf("page %d up wrapped to %d, want %d", page, alignPageIndex(model), total-1)
	}
	for index := 0; index < total; index++ {
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
		if alignPageIndex(model) != index {
			t.Fatalf("page %d down = %d, want %d", page, alignPageIndex(model), index)
		}
	}
}

func alignHighlightText(rendered string) string {
	for _, line := range strings.Split(rendered, "\n") {
		if strings.Contains(line, tuiSelect) {
			return strings.TrimSpace(stripTUIANSI(line))
		}
	}
	return ""
}

func alignPageIndex(model *tuiModel) int {
	switch model.snapshot.Page {
	case tuiPageProxies:
		return model.snapshot.SelectedGroup
	case tuiPageProfiles:
		if model.snapshot.SelectedRow < 0 {
			return model.snapshot.SelectedRow + tuiProfileImportRowCount
		}
		return model.snapshot.SelectedRow + tuiProfileImportRowCount
	case tuiPageConnections:
		return model.snapshot.SelectedConnection
	case tuiPageRequests:
		return model.snapshot.SelectedRequest
	case tuiPageLogs:
		return model.snapshot.SelectedLog
	case tuiPageTools:
		return model.snapshot.SelectedTool
	case tuiPageMaintenance:
		return model.snapshot.SelectedMaintenance
	default:
		return model.snapshot.SelectedDashboard
	}
}

func drainTea(command tea.Cmd) tea.Msg {
	if command == nil {
		return nil
	}
	message := command()
	batch, ok := message.(tea.BatchMsg)
	if !ok {
		return message
	}
	var last tea.Msg
	for _, cmd := range batch {
		last = drainTea(cmd)
	}
	return last
}

func serveRowAlignService(t *testing.T, handle func(tuiServiceRequest) tuiServiceStatus) {
	t.Helper()
	previous := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = t.TempDir()
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previous })
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
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
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

func osWriteAlign(path, data string) error {
	return os.WriteFile(path, []byte(data), 0o600)
}

func itoaAlign(value int) string {
	return strings.TrimPrefix(strings.ReplaceAll(" "+jsonNumber(value), " ", ""), "+")
}

func jsonNumber(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
