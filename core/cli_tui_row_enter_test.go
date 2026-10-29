//go:build linux && !cgo && cli

package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func newRowEnterModel(t *testing.T) *tuiModel {
	t.Helper()
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	model.snapshot.FocusSidebar = false
	model.snapshot.Language = "en"
	model.width = 160
	model.height = 60
	return model
}

func pressRowEnterKey(model *tuiModel, message tea.KeyMsg) tea.Cmd {
	_, command := model.update(message)
	return command
}

func settleRowEnter(model *tuiModel) {
	model.coreBusy = false
	model.sshBusy = false
	model.editorBusy = false
	model.syncLaneBusy()
	model.networkCheckActive = false
	model.networkCheckPending = false
	model.snapshot.Network.Loading = false
	model.snapshot.Network.Error = ""
	model.snapshot.SSHNetwork.Loading = false
	model.snapshot.SSHNetwork.Error = ""
	model.snapshot.SSHDelay.Testing = false
	model.snapshot.SSHSpeed.Testing = false
	model.snapshot.DashboardDelay.Testing = false
	model.snapshot.DashboardSpeed.Testing = false
	model.languageSelectionOpen = false
	model.modeSelectionOpen = false
	model.inputMode = tuiInputNone
	model.sshCaptureOpen = false
	model.sshProxyPortEditing = false
	model.snapshot.FocusSidebar = false
	model.snapshot.ConnectionsDetailOpen = false
	model.snapshot.HistoryDetailOpen = false
	model.snapshot.LogDetailOpen = false
}

func finishRowEnter(model *tuiModel) {
	if model.sshProxyPortEditing ||
		model.inputMode != tuiInputNone ||
		model.languageSelectionOpen ||
		model.modeSelectionOpen ||
		model.sshCaptureOpen ||
		model.snapshot.ConnectionsDetailOpen ||
		model.snapshot.HistoryDetailOpen ||
		model.snapshot.LogDetailOpen {
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEsc})
	}
	settleRowEnter(model)
}

func tuiSelectedPlainLines(raw string) []string {
	lines := make([]string, 0, 2)
	for _, line := range strings.Split(raw, "\n") {
		if strings.Contains(line, tuiSelect) {
			lines = append(lines, stripTUIANSI(line))
		}
	}
	return lines
}

func tuiHasBraille(value string) bool {
	for _, r := range value {
		if r >= 0x2800 && r <= 0x28FF {
			return true
		}
	}
	return false
}

func TestTUIRowEnterWalksDashboard(t *testing.T) {
	model := newRowEnterModel(t)
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.SelectedDashboard = tuiDashboardServiceRow
	controls := tuiDashboardControlFields(model.snapshot)
	network := tuiDashboardNetworkFields(model.snapshot)
	if len(controls) != tuiDashboardPublicIPRow || len(controls)+len(network) != tuiDashboardOverviewStartRow {
		t.Fatalf("dashboard labels do not match the cursor: controls=%d network=%d", len(controls), len(network))
	}
	count := tuiDashboardSelectableCount(model.snapshot)
	if count <= tuiDashboardSpeedRow {
		t.Fatalf("dashboard cursor count = %d", count)
	}

	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyUp})
	if model.snapshot.SelectedDashboard != count-1 {
		t.Fatalf("up from the first dashboard row = %d, want %d", model.snapshot.SelectedDashboard, count-1)
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	if model.snapshot.SelectedDashboard != tuiDashboardServiceRow {
		t.Fatalf("down did not return to the first dashboard row: %d", model.snapshot.SelectedDashboard)
	}

	for index := 0; index < count; index++ {
		if model.snapshot.SelectedDashboard != index {
			t.Fatalf("dashboard highlight = %d, want %d", model.snapshot.SelectedDashboard, index)
		}
		tunBefore := model.snapshot.Settings.TunEnabled
		if index >= tuiDashboardOverviewStartRow {
			model.snapshot.Status = "row-idle"
		}
		command := pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
		switch index {
		case tuiDashboardServiceRow:
			if command == nil || model.snapshot.Status != "Working..." || model.snapshot.Page != tuiPageDashboard || model.snapshot.Network.Loading || model.snapshot.Settings.TunEnabled != tunBefore {
				t.Fatalf("service enter status=%q page=%d loading=%t", model.snapshot.Status, model.snapshot.Page, model.snapshot.Network.Loading)
			}
		case tuiDashboardSystemProxyRow:
			if command != nil || !strings.Contains(model.snapshot.Status, "System proxy") || model.snapshot.Network.Loading {
				t.Fatalf("system proxy enter status=%q command=%t", model.snapshot.Status, command != nil)
			}
		case tuiDashboardTunRow:
			if command != nil || model.snapshot.Settings.TunEnabled == tunBefore || !strings.Contains(model.snapshot.Status, "staged") {
				t.Fatalf("TUN enter status=%q toggled=%t", model.snapshot.Status, model.snapshot.Settings.TunEnabled != tunBefore)
			}
		case tuiDashboardModeRow:
			if command != nil || !model.modeSelectionOpen || model.snapshot.Network.Loading {
				t.Fatalf("mode enter open=%t status=%q", model.modeSelectionOpen, model.snapshot.Status)
			}
		case tuiDashboardFLCOutboundRow:
			if model.snapshot.Page != tuiPageProxies || model.snapshot.Network.Loading || strings.Contains(model.snapshot.Status, "delay test") {
				t.Fatalf("FLC enter page=%d status=%q", model.snapshot.Page, model.snapshot.Status)
			}
		case tuiDashboardMixedPortRow:
			if model.inputMode != tuiInputMixedPort || model.snapshot.Network.Loading {
				t.Fatalf("mixed port enter mode=%d", model.inputMode)
			}
		case tuiDashboardPublicIPRow, tuiDashboardIntranetRow:
			if command == nil || !model.snapshot.Network.Loading || !model.networkCheckActive || model.snapshot.DashboardDelay.Testing || model.snapshot.DashboardSpeed.Testing || strings.Contains(model.snapshot.Status, "delay test") || strings.Contains(model.snapshot.Status, "speed test") {
				t.Fatalf("address row %d enter status=%q loading=%t command=%t", index, model.snapshot.Status, model.snapshot.Network.Loading, command != nil)
			}
		case tuiDashboardDelayRow:
			if command != nil || !strings.Contains(model.snapshot.Status, "delay test") || strings.Contains(model.snapshot.Status, "speed test") || model.snapshot.Network.Loading || model.snapshot.DashboardSpeed.Testing {
				t.Fatalf("delay enter status=%q", model.snapshot.Status)
			}
		case tuiDashboardSpeedRow:
			if command != nil || !strings.Contains(model.snapshot.Status, "speed test") || model.snapshot.Network.Loading || model.snapshot.DashboardDelay.Testing {
				t.Fatalf("speed enter status=%q", model.snapshot.Status)
			}
		default:
			if command != nil || model.snapshot.Status != "row-idle" || model.snapshot.Network.Loading || model.networkCheckActive || model.snapshot.DashboardDelay.Testing || model.snapshot.DashboardSpeed.Testing || model.snapshot.Page != tuiPageDashboard || model.inputMode != tuiInputNone || model.snapshot.Settings.TunEnabled != tunBefore {
				t.Fatalf("overview row %d enter status=%q loading=%t page=%d", index, model.snapshot.Status, model.snapshot.Network.Loading, model.snapshot.Page)
			}
		}
		finishRowEnter(model)
		if model.snapshot.Page != tuiPageDashboard {
			model.snapshot.Page = tuiPageDashboard
			model.snapshot.FocusSidebar = false
			model.snapshot.ProxyNodeFocus = false
		}
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	}
	if model.snapshot.SelectedDashboard != tuiDashboardServiceRow {
		t.Fatalf("dashboard cursor did not wrap: %d", model.snapshot.SelectedDashboard)
	}
}

func TestTUIRowEnterWalksSSHDetail(t *testing.T) {
	model := newRowEnterModel(t)
	model.snapshot.Page = tuiPageSSH
	model.snapshot.SSHDashboardFocus = true
	model.snapshot.SSHDetailName = "row-enter"
	model.snapshot.SSHDetailRow = tuiSSHTunnelRow
	model.snapshot.SSHProfiles = []tuiSSHProfile{{
		Name: "row-enter", Connected: true, Ready: true, Attached: true, SocksPort: 1080,
	}}
	count := model.sshDetailSelectableCount()
	if count != tuiSSHDetailReadyRowCount {
		t.Fatalf("connected detail rows = %d", count)
	}
	for index := 0; index < count; index++ {
		if model.snapshot.SSHDetailRow != index {
			t.Fatalf("SSH detail row = %d, want %d", model.snapshot.SSHDetailRow, index)
		}
		command := pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
		switch index {
		case tuiSSHTunnelRow:
			if command == nil || !model.sshBusy || model.sshProxyPortEditing || !strings.Contains(model.snapshot.Status, "disconnect") || model.snapshot.SSHNetwork.Loading {
				t.Fatalf("tunnel enter status=%q busy=%t editing=%t", model.snapshot.Status, model.sshBusy, model.sshProxyPortEditing)
			}
		case tuiSSHProxyPortRow:
			if command != nil || !model.sshProxyPortEditing || model.sshBusy || model.snapshot.SSHNetwork.Loading {
				t.Fatalf("port enter editing=%t busy=%t command=%t", model.sshProxyPortEditing, model.sshBusy, command != nil)
			}
		case tuiSSHProxyIPRow:
			if command == nil || !model.snapshot.SSHNetwork.Loading || model.sshBusy || model.sshProxyPortEditing || strings.Contains(model.snapshot.Status, "disconnect") {
				t.Fatalf("proxy IP enter status=%q loading=%t command=%t", model.snapshot.Status, model.snapshot.SSHNetwork.Loading, command != nil)
			}
		case tuiSSHInetRow, tuiSSHSpeedRow, tuiSSHTrafficRow:
			if command != nil || model.sshBusy || model.sshProxyPortEditing || model.snapshot.SSHNetwork.Loading || model.snapshot.SSHDelay.Testing || model.snapshot.SSHSpeed.Testing || strings.Contains(model.snapshot.Status, "disconnect") || strings.Contains(model.snapshot.Status, "speed test") || strings.Contains(model.snapshot.Status, "delay test") {
				t.Fatalf("metric row %d enter status=%q busy=%t loading=%t", index, model.snapshot.Status, model.sshBusy, model.snapshot.SSHNetwork.Loading)
			}
		}
		if index == tuiSSHSpeedRow {
			pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
			pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'v'}})
			pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'D'}})
			if model.snapshot.SSHDelay.Testing || model.snapshot.SSHSpeed.Testing || model.sshBusy || model.sshProxyPortEditing {
				t.Fatal("d/v on the SSH page started a delay or speed test")
			}
		}
		finishRowEnter(model)
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	}
	if model.snapshot.SSHDetailRow != tuiSSHTunnelRow || !model.snapshot.SSHDashboardFocus {
		t.Fatalf("SSH detail did not wrap: row=%d focus=%t", model.snapshot.SSHDetailRow, model.snapshot.SSHDashboardFocus)
	}

	idle := newRowEnterModel(t)
	idle.snapshot.Page = tuiPageSSH
	idle.snapshot.SSHDashboardFocus = true
	idle.snapshot.SSHDetailName = "row-enter"
	idle.snapshot.SSHDetailRow = tuiSSHSpeedRow
	idle.snapshot.SSHProfiles = []tuiSSHProfile{{Name: "row-enter", Connected: true, Ready: false}}
	if idle.sshDetailSelectableCount() != tuiSSHDetailIdleRowCount {
		t.Fatalf("idle detail rows = %d", idle.sshDetailSelectableCount())
	}
	pressRowEnterKey(idle, tea.KeyMsg{Type: tea.KeyDown})
	if idle.snapshot.SSHDetailRow != tuiSSHProxyPortRow {
		t.Fatalf("disconnected detail kept a metric row: %d", idle.snapshot.SSHDetailRow)
	}
	pressRowEnterKey(idle, tea.KeyMsg{Type: tea.KeyDown})
	if idle.snapshot.SSHDetailRow != tuiSSHTunnelRow {
		t.Fatalf("disconnected detail did not wrap to tunnel: %d", idle.snapshot.SSHDetailRow)
	}
}

func TestTUIRowEnterWalksSSHList(t *testing.T) {
	model := newRowEnterModel(t)
	model.snapshot.Page = tuiPageSSH
	model.snapshot.SSHDashboardFocus = false
	model.snapshot.SelectedSSH = tuiSSHCaptureRow
	model.snapshot.SSHProfiles = []tuiSSHProfile{
		{Name: "alpha"},
		{Name: "beta"},
	}
	want := []int{tuiSSHCaptureRow, 0, 1}
	for step, index := range want {
		if model.snapshot.SelectedSSH != index {
			t.Fatalf("SSH list step %d = %d, want %d", step, model.snapshot.SelectedSSH, index)
		}
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	}
	if model.snapshot.SelectedSSH != tuiSSHCaptureRow {
		t.Fatalf("SSH list did not wrap to Capture: %d", model.snapshot.SelectedSSH)
	}
	command := pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil || !model.sshCaptureOpen || model.sshBusy || !strings.Contains(strings.Join(model.sshCaptureOptions, "\n"), "Looking for SOCKS") {
		t.Fatalf("Capture enter open=%t busy=%t options=%v", model.sshCaptureOpen, model.sshBusy, model.sshCaptureOptions)
	}
	finishRowEnter(model)
	model.snapshot.SelectedSSH = 0
	model.snapshot.SSHDashboardFocus = false
	command = pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil || !model.sshBusy || !model.snapshot.SSHDashboardFocus || model.snapshot.SSHDetailName != "alpha" || !strings.Contains(model.snapshot.Status, "connect") || strings.Contains(model.snapshot.Status, "disconnect") {
		t.Fatalf("profile enter status=%q focus=%t", model.snapshot.Status, model.snapshot.SSHDashboardFocus)
	}
}

func TestTUIRowEnterWalksProxiesProfilesAndLists(t *testing.T) {
	model := newRowEnterModel(t)
	model.snapshot.Page = tuiPageProxies
	model.snapshot.ProxyView = tuiProxyViewGroups
	model.snapshot.ProxyNodeFocus = false
	model.snapshot.SelectedGroup = 0
	model.snapshot.Groups = []tuiGroup{
		{Name: "group-a", Now: "a-1", Nodes: []string{"a-1", "a-2"}},
		{Name: "group-b", Now: "b-2", Nodes: []string{"b-1", "b-2"}},
	}
	for index := 0; index < len(model.snapshot.Groups); index++ {
		if model.snapshot.SelectedGroup != index || model.snapshot.ProxyNodeFocus {
			t.Fatalf("group highlight = %d focus=%t", model.snapshot.SelectedGroup, model.snapshot.ProxyNodeFocus)
		}
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	}
	if model.snapshot.SelectedGroup != 0 {
		t.Fatalf("groups did not wrap: %d", model.snapshot.SelectedGroup)
	}
	command := pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if command != nil || !model.snapshot.ProxyNodeFocus || !strings.Contains(model.snapshot.Status, "group-a") || model.coreBusy {
		t.Fatalf("group enter status=%q focus=%t", model.snapshot.Status, model.snapshot.ProxyNodeFocus)
	}
	for index := 0; index < len(model.snapshot.Groups[0].Nodes); index++ {
		if model.snapshot.SelectedNode != index || !model.snapshot.ProxyNodeFocus {
			t.Fatalf("node highlight = %d focus=%t", model.snapshot.SelectedNode, model.snapshot.ProxyNodeFocus)
		}
		if index == 1 {
			command = pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
			if command == nil || model.snapshot.Status != "Working..." || model.snapshot.SelectedNode != 1 || model.snapshot.Groups[0].Now != "a-1" {
				t.Fatalf("node enter status=%q node=%d now=%s", model.snapshot.Status, model.snapshot.SelectedNode, model.snapshot.Groups[0].Now)
			}
			finishRowEnter(model)
		}
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	}
	if model.snapshot.SelectedNode != 0 || !model.snapshot.ProxyNodeFocus {
		t.Fatalf("nodes did not wrap: %d focus=%t", model.snapshot.SelectedNode, model.snapshot.ProxyNodeFocus)
	}

	model.snapshot.ProxyView = tuiProxyViewProviders
	model.snapshot.ProxyNodeFocus = false
	model.snapshot.SelectedProvider = 0
	model.snapshot.Providers = []tuiProvider{{Name: "prov-a"}, {Name: "prov-b"}}
	for index := 0; index < len(model.snapshot.Providers); index++ {
		if model.snapshot.SelectedProvider != index {
			t.Fatalf("provider highlight = %d, want %d", model.snapshot.SelectedProvider, index)
		}
		command = pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
		if command == nil || model.snapshot.Status != "Working..." || model.snapshot.SelectedProvider != index || model.snapshot.ProxyNodeFocus {
			t.Fatalf("provider enter status=%q index=%d", model.snapshot.Status, model.snapshot.SelectedProvider)
		}
		finishRowEnter(model)
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	}
	if model.snapshot.SelectedProvider != 0 {
		t.Fatalf("providers did not wrap: %d", model.snapshot.SelectedProvider)
	}

	model.snapshot.Page = tuiPageProfiles
	model.snapshot.SelectedRow = tuiProfileImportSubscriptionRow
	model.snapshot.Profiles = []tuiProfile{
		{Name: "alpha", Path: "/tmp/flclash-row-enter-alpha.yaml"},
		{Name: "beta", Path: "/tmp/flclash-row-enter-beta.yaml", SubscriptionURL: "https://example.invalid/sub", SubscriptionInfo: &tuiSubscriptionInfo{}},
	}
	profileRows := []int{tuiProfileImportSubscriptionRow, tuiProfileImportFileRow, 0, 1}
	for _, index := range profileRows {
		if model.snapshot.SelectedRow != index {
			t.Fatalf("profile highlight = %d, want %d", model.snapshot.SelectedRow, index)
		}
		command = pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
		switch index {
		case tuiProfileImportSubscriptionRow:
			if model.inputMode != tuiInputSubscription || command != nil {
				t.Fatalf("subscription import mode=%d", model.inputMode)
			}
		case tuiProfileImportFileRow:
			if model.inputMode != tuiInputProfileFile || command != nil {
				t.Fatalf("file import mode=%d", model.inputMode)
			}
		default:
			if command != nil || !strings.Contains(model.snapshot.Status, "Profile activation") || model.inputMode != tuiInputNone {
				t.Fatalf("profile enter status=%q mode=%d", model.snapshot.Status, model.inputMode)
			}
		}
		finishRowEnter(model)
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	}
	if model.snapshot.SelectedRow != tuiProfileImportSubscriptionRow {
		t.Fatalf("profiles did not wrap: %d", model.snapshot.SelectedRow)
	}
	model.snapshot.SelectedRow = 1
	rendered := renderTUIAtSize(model.snapshot, model.paths, "unix:///tmp/flclash-row-enter.sock", true, false, 160, 60)
	selected := tuiSelectedPlainLines(rendered)
	joined := strings.Join(selected, "\n")
	if !strings.Contains(joined, "beta") {
		t.Fatalf("profile highlight missed beta:\n%s", joined)
	}
	for _, line := range selected {
		if strings.Contains(line, "Used") || strings.Contains(line, "Quota") {
			t.Fatalf("subscription usage became a cursor row:\n%s", line)
		}
	}

	model.snapshot.Page = tuiPageConnections
	model.snapshot.SelectedConnection = 0
	model.snapshot.Connections = []tuiConnection{{ID: "c1", Host: "one.example"}, {ID: "c2", Host: "two.example"}}
	for index := 0; index < len(model.snapshot.Connections); index++ {
		if model.snapshot.SelectedConnection != index {
			t.Fatalf("connection highlight = %d, want %d", model.snapshot.SelectedConnection, index)
		}
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
		if !model.snapshot.ConnectionsDetailOpen || model.snapshot.SelectedConnection != index {
			t.Fatalf("connection enter index=%d open=%t", model.snapshot.SelectedConnection, model.snapshot.ConnectionsDetailOpen)
		}
		finishRowEnter(model)
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	}
	if model.snapshot.SelectedConnection != 0 {
		t.Fatalf("connections did not wrap: %d", model.snapshot.SelectedConnection)
	}

	model.snapshot.Page = tuiPageRequests
	model.snapshot.SelectedRequest = 0
	model.snapshot.Requests = []tuiRequest{
		{TuiConnection: tuiConnection{ID: "h1", Host: "hist-one.example"}},
		{TuiConnection: tuiConnection{ID: "h2", Host: "hist-two.example"}},
	}
	for index := 0; index < len(model.snapshot.Requests); index++ {
		if model.snapshot.SelectedRequest != index {
			t.Fatalf("history highlight = %d, want %d", model.snapshot.SelectedRequest, index)
		}
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
		if !model.snapshot.HistoryDetailOpen || model.snapshot.SelectedRequest != index {
			t.Fatalf("history enter index=%d open=%t", model.snapshot.SelectedRequest, model.snapshot.HistoryDetailOpen)
		}
		finishRowEnter(model)
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	}
	if model.snapshot.SelectedRequest != 0 {
		t.Fatalf("history did not wrap: %d", model.snapshot.SelectedRequest)
	}

	model.snapshot.Page = tuiPageLogs
	model.snapshot.SelectedLog = 0
	model.snapshot.Logs = []string{"info: alpha", "info: beta"}
	for index := 0; index < len(model.snapshot.Logs); index++ {
		if model.snapshot.SelectedLog != index {
			t.Fatalf("log highlight = %d, want %d", model.snapshot.SelectedLog, index)
		}
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
		if !model.snapshot.LogDetailOpen || model.snapshot.SelectedLog != index {
			t.Fatalf("log enter index=%d open=%t", model.snapshot.SelectedLog, model.snapshot.LogDetailOpen)
		}
		finishRowEnter(model)
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	}
	if model.snapshot.SelectedLog != 0 {
		t.Fatalf("logs did not wrap: %d", model.snapshot.SelectedLog)
	}
}

func TestTUIRowEnterWalksSettingsAndMaintenance(t *testing.T) {
	model := newRowEnterModel(t)
	model.snapshot.Page = tuiPageTools
	model.snapshot.SelectedTool = tuiSettingsLanguageRow
	model.snapshot.Settings.LogLevel = "info"
	for index := 0; index < tuiSettingsRowCount; index++ {
		if model.snapshot.SelectedTool != index {
			t.Fatalf("settings highlight = %d, want %d", model.snapshot.SelectedTool, index)
		}
		before := model.snapshot.Settings
		command := pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
		switch index {
		case tuiSettingsLanguageRow:
			if !model.languageSelectionOpen || before.AllowLAN != model.snapshot.Settings.AllowLAN {
				t.Fatal("language enter did not open language selection")
			}
		case tuiSettingsAllowLANRow:
			if !model.snapshot.Settings.AllowLAN || !strings.Contains(model.snapshot.Status, "staged") || command != nil {
				t.Fatalf("allow LAN enter status=%q", model.snapshot.Status)
			}
		case tuiSettingsIPv6Row:
			if !model.snapshot.Settings.IPv6 || model.snapshot.Settings.AllowLAN != before.AllowLAN {
				t.Fatal("IPv6 enter changed the wrong setting")
			}
		case tuiSettingsUnifiedDelayRow:
			if !model.snapshot.Settings.UnifiedDelay {
				t.Fatal("unified delay enter did not toggle")
			}
		case tuiSettingsTCPConcurrentRow:
			if !model.snapshot.Settings.TCPConcurrent {
				t.Fatal("TCP concurrent enter did not toggle")
			}
		case tuiSettingsLogLevelRow:
			if model.snapshot.Settings.LogLevel == before.LogLevel {
				t.Fatal("log level enter did not change the level")
			}
		case tuiSettingsTunScopeRow:
			if command == nil || model.snapshot.Status != "Working..." || model.snapshot.Settings.TunScope != before.TunScope {
				t.Fatalf("TUN scope enter status=%q scope=%q", model.snapshot.Status, model.snapshot.Settings.TunScope)
			}
		}
		finishRowEnter(model)
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	}
	if model.snapshot.SelectedTool != tuiSettingsLanguageRow {
		t.Fatalf("settings did not wrap: %d", model.snapshot.SelectedTool)
	}

	tools := newRowEnterModel(t)
	tools.client = controllerClient{options: controllerOptions{address: "127.0.0.1:1"}}
	tools.snapshot.Page = tuiPageMaintenance
	tools.snapshot.SelectedMaintenance = tuiMaintenanceEditConfigRow
	for index := 0; index < tuiMaintenanceRowCount; index++ {
		if tools.snapshot.SelectedMaintenance != index {
			t.Fatalf("maintenance highlight = %d, want %d", tools.snapshot.SelectedMaintenance, index)
		}
		command := pressRowEnterKey(tools, tea.KeyMsg{Type: tea.KeyEnter})
		switch index {
		case tuiMaintenanceEditConfigRow:
			if command != nil || !strings.Contains(tools.snapshot.Status, "managed backend") || tools.snapshot.Update.Loading {
				t.Fatalf("edit config enter status=%q", tools.snapshot.Status)
			}
		case tuiMaintenanceBackupRow, tuiMaintenanceRestoreRow, tuiMaintenanceGeoUpdateRow, tuiMaintenanceResetTrafficRow:
			if command == nil {
				t.Fatalf("maintenance row %d did not start", index)
			}
			message, ok := command().(tuiOperationResultMsg)
			if !ok {
				t.Fatal("maintenance enter did not return an operation")
			}
			status := message.state.snapshot.Status
			switch index {
			case tuiMaintenanceBackupRow:
				if !strings.Contains(status, "Backup requires") {
					t.Fatalf("backup status=%q", status)
				}
			case tuiMaintenanceRestoreRow:
				if !strings.Contains(status, "Restore requires") {
					t.Fatalf("restore status=%q", status)
				}
			case tuiMaintenanceGeoUpdateRow:
				if !strings.Contains(status, "Geo update") || strings.Contains(status, "Backup") {
					t.Fatalf("geo status=%q", status)
				}
			case tuiMaintenanceResetTrafficRow:
				if !strings.Contains(status, "Traffic reset") {
					t.Fatalf("reset status=%q", status)
				}
			}
		case tuiMaintenanceUpdateRow:
			if command == nil || !tools.snapshot.Update.Loading || tools.snapshot.Status != "Working..." {
				t.Fatalf("update enter loading=%t status=%q", tools.snapshot.Update.Loading, tools.snapshot.Status)
			}
		}
		finishRowEnter(tools)
		tools.snapshot.Update.Loading = false
		pressRowEnterKey(tools, tea.KeyMsg{Type: tea.KeyDown})
	}
	if tools.snapshot.SelectedMaintenance != tuiMaintenanceEditConfigRow {
		t.Fatalf("maintenance did not wrap: %d", tools.snapshot.SelectedMaintenance)
	}
}

func TestTUIRowEnterHighlightSkipsCharts(t *testing.T) {
	history := []trafficSnapshot{{Up: 1000, Down: 4000}, {Up: 8000, Down: 2000}}
	dashboard := populatedTUISnapshot(tuiPageDashboard)
	dashboard.FocusSidebar = false
	dashboard.Language = "en"
	dashboard.SelectedDashboard = tuiDashboardPublicIPRow
	dashboard.TrafficHistory = history
	rendered := renderTUIAtSize(dashboard, cliPaths{ConfigPath: "/tmp/flclash-row-enter.yaml"}, "unix:///tmp/flclash-row-enter.sock", true, false, 160, 60)
	if !tuiHasBraille(rendered) {
		t.Fatal("dashboard chart was not drawn")
	}
	selected := tuiSelectedPlainLines(rendered)
	if len(selected) == 0 || !strings.Contains(strings.Join(selected, "\n"), "Public IP") {
		t.Fatalf("dashboard highlight = %q", selected)
	}
	for _, line := range selected {
		if tuiHasBraille(line) || strings.Contains(line, "Live traffic") {
			t.Fatalf("dashboard highlight sat on the chart: %s", line)
		}
	}

	ssh := tuiSnapshot{
		Page:              tuiPageSSH,
		Language:          "en",
		FocusSidebar:      false,
		SSHDashboardFocus: true,
		SSHDetailName:     "row-enter",
		SSHDetailRow:      tuiSSHSpeedRow,
		SSHProfiles: []tuiSSHProfile{{
			Name: "row-enter", Connected: true, Ready: true, SocksPort: 1080,
		}},
		SSHTrafficHistory: history,
	}
	rendered = renderTUIAtSize(ssh, cliPaths{}, "unix:///tmp/flclash-row-enter.sock", true, false, 160, 60)
	if !tuiHasBraille(rendered) || !strings.Contains(stripTUIANSI(rendered), "Traffic history") {
		t.Fatal("SSH detail chart was not drawn")
	}
	selected = tuiSelectedPlainLines(rendered)
	if len(selected) == 0 || !strings.Contains(strings.Join(selected, "\n"), "Speed") {
		t.Fatalf("SSH highlight = %q", selected)
	}
	for _, line := range selected {
		if tuiHasBraille(line) || strings.Contains(line, "Traffic history") {
			t.Fatalf("SSH highlight sat on the chart: %s", line)
		}
	}
}

func TestTUIRowEnterPreservesSelection(t *testing.T) {
	current := tuiSnapshot{
		SelectedDashboard: tuiDashboardPublicIPRow,
		SSHDetailName:     "row-enter",
		SSHDashboardFocus: true,
		SSHDetailRow:      tuiSSHSpeedRow,
		SSHProfiles: []tuiSSHProfile{{
			Name: "row-enter", Connected: true, Ready: true, SocksPort: 1080,
		}},
	}
	updated := current
	updated.SSHProfiles = []tuiSSHProfile{{
		Name: "row-enter", Connected: true, Ready: true, SocksPort: 1080,
	}}
	merged := preserveTUIInteraction(current, updated)
	if merged.SelectedDashboard != tuiDashboardPublicIPRow || merged.SSHDetailRow != tuiSSHSpeedRow || merged.SSHDetailName != "row-enter" {
		t.Fatalf("refresh dropped the cursor: dashboard=%d ssh=%d", merged.SelectedDashboard, merged.SSHDetailRow)
	}

	updated.SSHProfiles = []tuiSSHProfile{{Name: "row-enter", Connected: false, Ready: false}}
	merged = preserveTUIInteraction(current, updated)
	if merged.SSHDetailName != "row-enter" || merged.SSHDetailRow != tuiSSHTunnelRow {
		t.Fatalf("disconnect kept a metric row: %+v", merged.SSHDetailRow)
	}

	updated.SSHProfiles = nil
	merged = preserveTUIInteraction(current, updated)
	if merged.SSHDetailName != "" || merged.SSHDashboardFocus || merged.SSHDetailRow != tuiSSHTunnelRow {
		t.Fatalf("missing profile kept the detail cursor: %+v", merged)
	}

	current.SelectedDashboard = 100
	updated = current
	updated.SSHProfiles = nil
	merged = preserveTUIInteraction(current, updated)
	if merged.SelectedDashboard != tuiDashboardServiceRow {
		t.Fatalf("missing dashboard row stayed selected: %d", merged.SelectedDashboard)
	}
}

func TestTUIDashboardSelectionStaysWithLabeledRows(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir(), ConfigPath: "/tmp/flclash-row-enter.yaml"}, nil, false)
	model.snapshot.FocusSidebar = false
	model.snapshot.Language = "en"
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.ManagedService = false
	model.snapshot.TrafficHistory = []trafficSnapshot{{Up: 1000, Down: 4000}, {Up: 8000, Down: 2000}}
	model.width = 141
	model.height = 46

	speedLine, memoryLine, chartLine, configLine := -1, -1, -1, -1
	for step := 0; step < 30; step++ {
		frame := renderTUIAtSize(model.snapshot, model.paths, "unix:///tmp/flclash-row-enter.sock", false, false, model.width, model.height)
		plain := strings.Split(stripTUIANSI(frame), "\n")
		for index, line := range plain {
			if strings.Contains(line, "Live traffic") {
				chartLine = index
			}
		}
		for index, line := range strings.Split(frame, "\n") {
			if !strings.Contains(line, tuiSelect) {
				continue
			}
			text := stripTUIANSI(line)
			switch {
			case strings.Contains(text, "Cloudflare DL"):
				speedLine = index
			case strings.Contains(text, "System memory"):
				memoryLine = index
			case strings.Contains(text, "Config"):
				configLine = index
			}
		}
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
		if model.snapshot.SelectedDashboard == tuiDashboardServiceRow {
			break
		}
	}
	if speedLine < 0 || memoryLine < 0 || chartLine < 0 || configLine < 0 {
		t.Fatalf("missing rows speed=%d memory=%d chart=%d config=%d", speedLine, memoryLine, chartLine, configLine)
	}
	if memoryLine-speedLine > 6 {
		t.Fatalf("Down jumped across the chart: speed line %d, memory line %d", speedLine, memoryLine)
	}
	if chartLine < configLine {
		t.Fatalf("chart is between cursor rows: chart %d config %d", chartLine, configLine)
	}
}

func TestTUIRowHighlightSurvivesEmbeddedReset(t *testing.T) {
	var b strings.Builder
	tuiRow(&b, "Speed  "+tuiTrafficChartDownload+"1 KB/s"+tuiReset+" · "+tuiTrafficChartUpload+"2 KB/s"+tuiReset, 80, true, "")
	line := strings.TrimSuffix(b.String(), "\n")
	final := strings.LastIndex(line, tuiReset)
	if final < 0 {
		t.Fatal("row has no reset")
	}
	body := line[:final]
	if !strings.Contains(body, tuiReset) {
		t.Fatal("template reset was dropped")
	}
	for index := 0; ; {
		next := strings.Index(body[index:], tuiReset)
		if next < 0 {
			break
		}
		next += index
		if !strings.HasPrefix(body[next+len(tuiReset):], tuiSelect) {
			t.Fatalf("highlight dropped after reset: %q", line)
		}
		index = next + len(tuiReset)
	}
}

func TestTUIRowEnterExternalCoreHighlightsConfig(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir(), ConfigPath: "/tmp/flclash-row-enter.yaml"}, nil, false)
	model.snapshot.FocusSidebar = false
	model.snapshot.Language = "en"
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.ManagedService = false
	model.snapshot.ExternalCore = false
	model.snapshot.SelectedDashboard = tuiDashboardServiceRow
	model.snapshot.TrafficHistory = []trafficSnapshot{{Up: 1000, Down: 4000}, {Up: 8000, Down: 2000}}
	model.width = 160
	model.height = 36

	overviewLabels := []string{
		"System memory",
		"TUI process",
		"External Core",
		"Go heap",
		"Network speed",
		"Traffic total",
		"Activity",
		"TUI frontends",
		"Config",
	}
	seen := map[string]int{}
	configIndex := -1
	for step := 0; step < 40; step++ {
		rendered := renderTUIAtSize(model.snapshot, model.paths, "unix:///tmp/flclash-row-enter.sock", false, false, model.width, model.height)
		selected := tuiSelectedPlainLines(rendered)
		if len(selected) == 0 {
			t.Fatalf("step %d index %d has no highlight", step, model.snapshot.SelectedDashboard)
		}
		joined := strings.Join(selected, "\n")
		for _, line := range selected {
			if tuiHasBraille(line) || strings.Contains(line, "Live traffic") {
				t.Fatalf("highlight sat on the chart: %s", line)
			}
		}
		for _, label := range overviewLabels {
			if strings.Contains(joined, label) {
				seen[label]++
				if label == "Config" {
					configIndex = model.snapshot.SelectedDashboard
				}
			}
		}
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
		if model.snapshot.SelectedDashboard == tuiDashboardServiceRow {
			break
		}
	}
	if model.snapshot.SelectedDashboard != tuiDashboardServiceRow {
		t.Fatalf("dashboard cursor did not wrap: %d", model.snapshot.SelectedDashboard)
	}
	for _, label := range overviewLabels {
		if seen[label] != 1 {
			t.Fatalf("%s highlighted %d times, want 1; seen=%v", label, seen[label], seen)
		}
	}
	if configIndex < 0 {
		t.Fatal("Config was not highlighted")
	}
	model.snapshot.SelectedDashboard = configIndex
	model.snapshot.ExternalCore = false
	model.snapshot.DashboardScroll = 0
	refreshed := tuiSnapshot{ExternalCore: false, ManagedService: false}
	_, command := model.update(tuiRefreshResultMsg{
		snapshot:   refreshed,
		sequence:   model.refreshSequence,
		generation: model.backendGeneration,
	})
	if command != nil {
		t.Fatal("refresh started another command")
	}
	if model.snapshot.SelectedDashboard != configIndex {
		t.Fatalf("refresh dropped Config: %d, want %d", model.snapshot.SelectedDashboard, configIndex)
	}
	rendered := renderTUIAtSize(model.snapshot, model.paths, "unix:///tmp/flclash-row-enter.sock", false, false, model.width, model.height)
	selected := tuiSelectedPlainLines(rendered)
	joined := strings.Join(selected, "\n")
	if !strings.Contains(joined, "Config") || strings.Contains(joined, "External Core") {
		t.Fatalf("refresh highlight = %q", selected)
	}
	for _, line := range selected {
		if tuiHasBraille(line) || strings.Contains(line, "Live traffic") {
			t.Fatalf("refresh highlight sat on the chart: %s", line)
		}
	}
}
