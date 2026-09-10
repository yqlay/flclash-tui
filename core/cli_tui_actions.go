//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *tuiModel) selectCurrent() tea.Cmd {
	switch m.snapshot.Page {
	case tuiPageDashboard:
		switch m.snapshot.SelectedDashboard {
		case tuiDashboardServiceRow:
			return m.handleKey(tuiKeyCoreToggle)
		case tuiDashboardSystemProxyRow:
			return m.handleKey(tuiKeySystemProxy)
		case tuiDashboardTunRow:
			return m.handleKey(tuiKeyTun)
		case tuiDashboardModeRow:
			return m.handleKey(tuiKeyMode)
		case tuiDashboardFLCOutboundRow:
			return m.openProxiesForFLCOutbound()
		case tuiDashboardMixedPortRow:
			m.beginInput(tuiInputMixedPort)
		case tuiDashboardDelayRow:
			return m.testDashboardDelay()
		case tuiDashboardSpeedRow:
			return m.testDashboardSpeed()
		}
	case tuiPageProxies:
		if m.snapshot.ProxyView == tuiProxyViewProviders {
			return m.startOperation(func(state *tuiOperationState) {
				updateTUIProvider(&state.snapshot, m.client)
			})
		}
		if !m.snapshot.ProxyNodeFocus {
			if m.snapshot.SelectedGroup < 0 ||
				m.snapshot.SelectedGroup >= len(m.snapshot.Groups) {
				m.snapshot.Status = "Select a proxy group first"
				return nil
			}
			m.snapshot.ProxyNodeFocus = true
			group := m.snapshot.Groups[m.snapshot.SelectedGroup]
			m.snapshot.SelectedNode = findTUIString(group.Nodes, group.Now)
			m.snapshot.Status = "Nodes in " + group.Name +
				" · ↑↓/ws select · Enter apply · Esc back"
			return nil
		}
		group := m.snapshot.Groups[m.snapshot.SelectedGroup]
		if m.snapshot.SelectedNode < 0 ||
			m.snapshot.SelectedNode >= len(group.Nodes) {
			m.snapshot.Status = "Select a proxy node before applying it"
			return nil
		}
		return m.startOperation(func(state *tuiOperationState) {
			if m.service != nil {
				selectTUIServiceProxy(state, m.service, m.client)
			} else if selectTUIProxy(
				&state.snapshot,
				m.client,
				state.paths.HomeDir,
			) {
				state.networkChanged = true
			}
		})
	case tuiPageProfiles:
		if m.snapshot.SelectedRow == tuiProfileImportSubscriptionRow {
			m.beginInput(tuiInputSubscription)
			return nil
		}
		if m.snapshot.SelectedRow == tuiProfileImportFileRow {
			m.beginInput(tuiInputProfileFile)
			return nil
		}
		if m.snapshot.SelectedRow < 0 ||
			m.snapshot.SelectedRow >= len(m.snapshot.Profiles) {
			m.snapshot.Status = "Select a profile before activating it"
			return nil
		}
		if m.service == nil {
			m.snapshot.Status = "Profile activation requires the managed backend"
			return nil
		}
		return m.startOperation(func(state *tuiOperationState) {
			switchTUIServiceProfile(state, m.service, m.client)
			state.pendingMixedPort = nil
			state.stagedSettings = nil
			state.settingsDirty = false
			syncStoppedTUISettings(state)
		})
	case tuiPageSSH:
		if m.snapshot.SelectedSSH == tuiSSHCaptureRow {
			return m.beginSSHCapture()
		}
		if m.snapshot.SelectedSSH < 0 ||
			m.snapshot.SelectedSSH >= len(m.snapshot.SSHProfiles) {
			m.snapshot.Status = "Press n to add an SSH profile, or a to capture live SSH"
			return nil
		}
		profile := m.snapshot.SSHProfiles[m.snapshot.SelectedSSH]
		if profile.NeedsUsername {
			m.beginSSHForm(true)
			if m.sshFormOpen {
				m.sshFormSelected = tuiSSHFormUsernameRow
				m.snapshot.Status = "Legacy SSH profile · enter Username before connecting"
			}
			return nil
		}
		if !m.snapshot.SSHDashboardFocus {
			if profile.Connected && profile.Ready {
				m.snapshot.SSHDashboardFocus = true
				m.snapshot.Status = "SSH Dashboard focused · Enter controls selected row"
				return m.refreshSelectedSSHDashboard()
			}
			return m.runSelectedSSHAction("connect")
		}
		switch m.snapshot.SelectedSSHDetail {
		case tuiSSHDashboardTunnelRow:
			if profile.Connected && profile.Ready {
				return m.runSelectedSSHAction("disconnect")
			}
			return m.runSelectedSSHAction("connect")
		case tuiSSHDashboardDirectExitRow,
			tuiSSHDashboardManagedIPRow:
			return m.refreshSelectedSSHDashboard()
		case tuiSSHDashboardDirectRTTRow:
			return m.testSelectedSSHDelayFor(true)
		case tuiSSHDashboardDirectSpeedRow:
			return m.testSelectedSSHSpeedFor(true)
		case tuiSSHDashboardManagedRTTRow:
			return m.testSelectedSSHDelayFor(false)
		case tuiSSHDashboardManagedSpeedRow:
			return m.testSelectedSSHSpeedFor(false)
		}
	case tuiPageRequests:
		if len(matchedTUIRequestIndexes(m.snapshot)) == 0 {
			m.snapshot.Status = "No matching History entry"
			return nil
		}
		m.snapshot.HistoryDetailOpen = true
	case tuiPageConnections:
		if len(matchedTUIConnectionIndexes(m.snapshot)) == 0 {
			m.snapshot.Status = "No matching active connection"
			return nil
		}
		m.snapshot.ConnectionsDetailOpen = true
	case tuiPageLogs:
		if len(matchedTUILogIndexes(m.snapshot)) == 0 {
			m.snapshot.Status = "No matching log entry"
			return nil
		}
		m.snapshot.LogDetailOpen = true
	case tuiPageTools:
		return m.selectTUISetting(m.snapshot.SelectedTool)
	case tuiPageMaintenance:
		switch m.snapshot.SelectedMaintenance {
		case tuiMaintenanceEditConfigRow:
			return m.startEditor(m.paths.ConfigPath)
		case tuiMaintenanceBackupRow:
			return m.runTool(1)
		case tuiMaintenanceRestoreRow:
			return m.runTool(2)
		case tuiMaintenanceGeoUpdateRow:
			return m.runTool(3)
		case tuiMaintenanceResetTrafficRow:
			return m.runTool(4)
		case tuiMaintenanceUpdateRow:
			return m.runTool(5)
		}
	}
	return nil
}

func (m *tuiModel) openProxiesForFLCOutbound() tea.Cmd {
	cmds := m.changeVisiblePage(tuiPageProxies)
	m.snapshot.SelectedMenu = int(tuiPageProxies)
	m.snapshot.FocusSidebar = false
	m.snapshot.ProxyView = tuiProxyViewGroups
	m.snapshot.SSHDashboardFocus = false
	if name := strings.TrimSpace(m.snapshot.FLCOutbound); name != "" {
		m.snapshot.SelectedGroup = findTUIGroup(m.snapshot.Groups, name)
	}
	if m.snapshot.SelectedGroup >= 0 &&
		m.snapshot.SelectedGroup < len(m.snapshot.Groups) {
		group := m.snapshot.Groups[m.snapshot.SelectedGroup]
		m.snapshot.ProxyNodeFocus = true
		m.snapshot.SelectedNode = findTUIString(group.Nodes, group.Now)
		m.snapshot.Status = "flc uses " + group.Name + " · select a node"
		return tea.Batch(cmds...)
	}
	m.snapshot.ProxyNodeFocus = false
	m.snapshot.Status = "Select a proxy node; flc follows that group"
	return tea.Batch(cmds...)
}

func selectTUIServiceProxy(
	state *tuiOperationState,
	service *tuiServiceClient,
	client controllerClient,
) {
	if state.snapshot.SelectedGroup < 0 ||
		state.snapshot.SelectedGroup >= len(state.snapshot.Groups) {
		state.snapshot.Status = "Select a proxy group before applying it"
		return
	}
	group := state.snapshot.Groups[state.snapshot.SelectedGroup]
	if state.snapshot.SelectedNode < 0 ||
		state.snapshot.SelectedNode >= len(group.Nodes) {
		state.snapshot.Status = "Select a proxy node before applying it"
		return
	}
	node := group.Nodes[state.snapshot.SelectedNode]
	if !prepareTUIBackendRevision(state, service) {
		return
	}
	status, err := service.selectProxy(group.Name, node, state.backendRevision)
	if err != nil {
		state.snapshot.Status = "Switch failed: " + err.Error()
		return
	}
	state.snapshot.Status = fmt.Sprintf(
		"Switched %s to %s · silent flc follows this group",
		group.Name,
		node,
	)
	refreshTUISnapshot(&state.snapshot, client)
	applyTUIOperationServiceStatus(state, status)
	state.networkChanged = true
}

func switchTUIServiceProfile(
	state *tuiOperationState,
	service *tuiServiceClient,
	client controllerClient,
) {
	if state.snapshot.SelectedRow < 0 ||
		state.snapshot.SelectedRow >= len(state.snapshot.Profiles) {
		state.snapshot.Status = "Select a profile before activating it"
		return
	}
	profile := state.snapshot.Profiles[state.snapshot.SelectedRow]
	if profile.Current {
		state.snapshot.Status = "Profile is already active"
		return
	}
	if message := handleValidateConfig(profile.Path); message != "" {
		state.snapshot.Status = "Profile invalid: " + message
		return
	}
	expectedSHA256, err := tuiFileSHA256(profile.Path)
	if err != nil {
		state.snapshot.Status = "Profile read failed: " + err.Error()
		return
	}
	if !prepareTUIBackendRevision(state, service) {
		return
	}
	status, err := service.reloadAtRevisionWithDigest(
		profile.Path,
		state.backendRevision,
		expectedSHA256,
	)
	if err != nil {
		state.snapshot.Status = "Profile hot-reload failed: " + err.Error()
		return
	}
	state.paths.ConfigPath = profile.Path
	state.snapshot.GroupOrder = loadTUIProxyGroupOrder(profile.Path)
	state.snapshot.ProxyNodeFocus = false
	state.snapshot.Status = "Active profile: " + profile.Name
	refreshTUISnapshot(&state.snapshot, client)
	applyTUIOperationServiceStatus(state, status)
	state.networkChanged = true
	refreshTUIProfiles(&state.snapshot, state.paths)
}

func (m *tuiModel) selectTUISetting(index int) tea.Cmd {
	switch index {
	case tuiSettingsAllowLANRow:
		return m.handleKey(tuiKeyAllowLAN)
	case tuiSettingsIPv6Row:
		return m.handleKey(tuiKeyIPv6)
	case tuiSettingsUnifiedDelayRow:
		return m.handleKey(tuiKeyUnifiedDelay)
	case tuiSettingsTCPConcurrentRow:
		return m.handleKey(tuiKeyTCPConcurrent)
	case tuiSettingsLogLevelRow:
		return m.handleKey(tuiKeyLogLevel)
	case tuiSettingsTunScopeRow:
		return m.handleKey(tuiKeyTunScope)
	}
	return nil
}

func (m *tuiModel) runTool(index int) tea.Cmd {
	if index == 5 {
		m.snapshot.Update.Loading = true
		m.snapshot.Update.Error = ""
	}
	return m.startOperation(func(state *tuiOperationState) {
		switch index {
		case 1:
			if m.service == nil {
				state.snapshot.Status = "Backup requires the managed backend"
				return
			}
			if !prepareTUIBackendRevision(state, m.service) {
				return
			}
			status, err := m.service.backupProfile(
				state.paths.ConfigPath,
				state.backendRevision,
			)
			if err != nil {
				state.snapshot.Status = "Backup failed: " + err.Error()
			} else {
				state.backendRevision = status.Revision
				state.snapshot.Status = "Backup created: " + filepath.Base(status.ResultPath)
			}
		case 2:
			if m.service == nil {
				state.snapshot.Status = "Restore requires the managed backend"
				return
			}
			if !prepareTUIBackendRevision(state, m.service) {
				return
			}
			status, err := m.service.restoreProfile(
				state.paths.ConfigPath,
				state.backendRevision,
			)
			if err != nil {
				state.snapshot.Status = "Restore failed: " + err.Error()
			} else {
				applyTUIOperationServiceStatus(state, status)
				state.snapshot.Status = "Restored and hot-reloaded: " +
					filepath.Base(status.ResultPath)
				syncStoppedTUISettings(state)
			}
		case 3:
			if err := m.client.updateGeo(); err != nil {
				state.snapshot.Status = "Geo update failed: " + err.Error()
			} else {
				state.snapshot.Status = "Geo databases update started"
			}
		case 4:
			if m.ownsCore {
				handleResetTraffic()
				state.snapshot.Status = "Traffic counters reset"
			} else {
				state.snapshot.Status = "Traffic reset requires a core started by this process"
			}
		case 5:
			checkTUIUpdate(&state.snapshot)
		}
	})
}

func (m *tuiModel) testSelectedProxyDelay() tea.Cmd {
	if m.snapshot.SelectedGroup < 0 ||
		m.snapshot.SelectedGroup >= len(m.snapshot.Groups) {
		m.snapshot.Status = "Select a proxy group first"
		return nil
	}
	group := m.snapshot.Groups[m.snapshot.SelectedGroup]
	if m.snapshot.SelectedNode < 0 ||
		m.snapshot.SelectedNode >= len(group.Nodes) {
		m.snapshot.Status = "Select a proxy node first"
		return nil
	}
	groupName := group.Name
	node := group.Nodes[m.snapshot.SelectedNode]
	testURL := m.tuiDelayTestURL()
	setTUIGroupDelay(&m.snapshot, groupName, node, tuiDelayResult{Testing: true})
	return m.startOperation(func(state *tuiOperationState) {
		delay, err := testTUIProxyDelaySamples(m.client, node, testURL)
		if err != nil {
			setTUIGroupDelay(&state.snapshot, groupName, node, tuiDelayResult{Error: err.Error()})
			state.snapshot.Status = node + " delay: Timeout · " + err.Error()
			return
		}
		setTUIGroupDelay(&state.snapshot, groupName, node, delay)
		state.snapshot.Status = node + " delay: " + formatTUIDelay(delay)
	})
}

func (m *tuiModel) testSelectedProxyGroupDelays() tea.Cmd {
	if m.snapshot.SelectedGroup < 0 ||
		m.snapshot.SelectedGroup >= len(m.snapshot.Groups) {
		m.snapshot.Status = "Select a proxy group first"
		return nil
	}
	group := m.snapshot.Groups[m.snapshot.SelectedGroup]
	if len(group.Nodes) == 0 {
		m.snapshot.Status = "Selected proxy group has no nodes"
		return nil
	}
	nodes := append([]string(nil), group.Nodes...)
	testingDelays := make(map[string]tuiDelayResult, len(nodes))
	for _, node := range nodes {
		testingDelays[node] = tuiDelayResult{Testing: true}
	}
	setTUIGroupDelays(&m.snapshot, group.Name, testingDelays)
	testURL := m.tuiDelayTestURL()
	return m.startOperation(func(state *tuiOperationState) {
		delays := testTUIProxyDelays(m.client, nodes, testURL)
		successes := 0
		for _, delay := range delays {
			if delay.MedianMillis > 0 {
				successes++
			}
		}
		setTUIGroupDelays(&state.snapshot, group.Name, delays)
		state.snapshot.Status = fmt.Sprintf(
			"%s delays complete: %d/%d reachable",
			group.Name,
			successes,
			len(nodes),
		)
	})
}

func (m *tuiModel) testDashboardDelay() tea.Cmd {
	if m.service == nil {
		m.snapshot.Status = "Dashboard delay test requires the managed backend"
		return nil
	}
	m.snapshot.DashboardDelay = tuiDelayResult{Testing: true}
	mixedPort := m.snapshot.ActiveProxyPort
	testURL := m.tuiDelayTestURL()
	return m.startOperation(func(state *tuiOperationState) {
		delay, err := m.service.testRouteDelay(mixedPort, testURL)
		if err != nil {
			state.snapshot.DashboardDelay = tuiDelayResult{Error: err.Error()}
			state.snapshot.Status = "Current route delay: Timeout · " + err.Error()
			return
		}
		state.snapshot.DashboardDelay = delay
		state.snapshot.Status = "Current route delay: " + formatTUIDelay(delay)
	})
}

func (m *tuiModel) testDashboardSpeed() tea.Cmd {
	if m.service == nil {
		m.snapshot.Status = "Dashboard speed test requires the managed backend"
		return nil
	}
	m.snapshot.DashboardSpeed = tuiSpeedResult{Testing: true}
	mixedPort := m.snapshot.ActiveProxyPort
	return m.startOperation(func(state *tuiOperationState) {
		result, err := m.service.testRouteSpeed(mixedPort)
		if err != nil {
			state.snapshot.DashboardSpeed = tuiSpeedResult{Error: err.Error()}
			state.snapshot.Status = "Current route speed failed: " + err.Error()
			return
		}
		state.snapshot.DashboardSpeed = result
		state.snapshot.Status = "Current route speed: " + formatTUISpeed(result)
	})
}

func (m *tuiModel) testSelectedProxySpeed() tea.Cmd {
	if m.service == nil {
		m.snapshot.Status = "Proxy speed testing requires the managed backend"
		return nil
	}
	if m.snapshot.SelectedGroup < 0 ||
		m.snapshot.SelectedGroup >= len(m.snapshot.Groups) {
		m.snapshot.Status = "Select a proxy group first"
		return nil
	}
	group := m.snapshot.Groups[m.snapshot.SelectedGroup]
	if m.snapshot.SelectedNode < 0 ||
		m.snapshot.SelectedNode >= len(group.Nodes) {
		m.snapshot.Status = "Select a proxy node first"
		return nil
	}
	groupName := group.Name
	node := group.Nodes[m.snapshot.SelectedNode]
	setTUIGroupSpeed(
		&m.snapshot,
		groupName,
		node,
		tuiSpeedResult{Testing: true},
	)
	return m.startOperation(func(state *tuiOperationState) {
		result, err := m.service.testProxySpeed(node)
		if err != nil {
			setTUIGroupSpeed(
				&state.snapshot,
				groupName,
				node,
				tuiSpeedResult{Error: err.Error()},
			)
			state.snapshot.Status = node + " speed failed: " + err.Error()
			return
		}
		setTUIGroupSpeed(&state.snapshot, groupName, node, result)
		state.snapshot.Status = node + " speed: " + formatTUISpeed(result)
	})
}

func (m *tuiModel) testSelectedProxyGroupSpeeds() tea.Cmd {
	if m.service == nil {
		m.snapshot.Status = "Proxy speed testing requires the managed backend"
		return nil
	}
	if m.snapshot.SelectedGroup < 0 ||
		m.snapshot.SelectedGroup >= len(m.snapshot.Groups) {
		m.snapshot.Status = "Select a proxy group first"
		return nil
	}
	group := m.snapshot.Groups[m.snapshot.SelectedGroup]
	if len(group.Nodes) == 0 {
		m.snapshot.Status = "Selected proxy group has no nodes"
		return nil
	}
	nodes := append([]string(nil), group.Nodes...)
	testingSpeeds := make(map[string]tuiSpeedResult, len(nodes))
	for _, node := range nodes {
		testingSpeeds[node] = tuiSpeedResult{Testing: true}
	}
	setTUIGroupSpeeds(&m.snapshot, group.Name, testingSpeeds)
	if m.busy {
		m.snapshot.Status = "Another operation is still running"
		return nil
	}
	m.busy = true
	m.refreshInFlight = false
	m.refreshSequence++
	m.snapshot.Status = fmt.Sprintf(
		"%s speed tests: 0/%d complete · up to %d MB total",
		group.Name,
		len(nodes),
		len(nodes)*100,
	)
	return m.testNextProxyGroupSpeed(group.Name, nodes, len(nodes), 0)
}

func (m *tuiModel) testNextProxyGroupSpeed(
	groupName string,
	nodes []string,
	total,
	successes int,
) tea.Cmd {
	if len(nodes) == 0 {
		return nil
	}
	node := nodes[0]
	remaining := append([]string(nil), nodes[1:]...)
	service := m.service
	return func() tea.Msg {
		result, err := service.testProxySpeed(node)
		if err != nil {
			result = tuiSpeedResult{Error: err.Error()}
		}
		return tuiProxyGroupSpeedResultMsg{
			groupName: groupName,
			node:      node,
			result:    result,
			remaining: remaining,
			total:     total,
			successes: successes,
		}
	}
}

func (m *tuiModel) tuiDelayTestURL() string {
	testURL := "https://www.gstatic.com/generate_204"
	params := defaultSetupParams()
	if len(m.setupParams) > 0 &&
		UnmarshalJson(m.setupParams, params) == nil &&
		params.TestURL != "" {
		testURL = params.TestURL
	}
	return testURL
}

func testTUIProxyDelays(
	client controllerClient,
	nodes []string,
	testURL string,
) map[string]tuiDelayResult {
	const parallelism = 4
	delays := make(map[string]tuiDelayResult, len(nodes))
	var mutex sync.Mutex
	var waitGroup sync.WaitGroup
	limit := make(chan struct{}, parallelism)
	for _, node := range nodes {
		node := node
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			limit <- struct{}{}
			delay, err := testTUIProxyDelaySamples(client, node, testURL)
			<-limit
			if err != nil {
				delay = tuiDelayResult{Error: err.Error()}
			}
			mutex.Lock()
			delays[node] = delay
			mutex.Unlock()
		}()
	}
	waitGroup.Wait()
	return delays
}

func setTUIGroupDelay(
	snapshot *tuiSnapshot,
	groupName,
	node string,
	delay tuiDelayResult,
) {
	setTUIGroupDelays(snapshot, groupName, map[string]tuiDelayResult{node: delay})
}

func setTUIGroupDelays(
	snapshot *tuiSnapshot,
	groupName string,
	updates map[string]tuiDelayResult,
) {
	groupIndex := -1
	for index, group := range snapshot.Groups {
		if group.Name == groupName {
			groupIndex = index
			break
		}
	}
	if groupIndex < 0 {
		return
	}
	groups := append([]tuiGroup(nil), snapshot.Groups...)
	group := groups[groupIndex]
	delays := make(map[string]tuiDelayResult, len(group.Delays)+len(updates))
	for name, value := range group.Delays {
		delays[name] = value
	}
	for node, delay := range updates {
		delays[node] = delay
	}
	group.Delays = delays
	groups[groupIndex] = group
	snapshot.Groups = groups
}

func setTUIGroupSpeed(
	snapshot *tuiSnapshot,
	groupName,
	node string,
	speed tuiSpeedResult,
) {
	setTUIGroupSpeeds(
		snapshot,
		groupName,
		map[string]tuiSpeedResult{node: speed},
	)
}

func setTUIGroupSpeeds(
	snapshot *tuiSnapshot,
	groupName string,
	updates map[string]tuiSpeedResult,
) {
	groupIndex := -1
	for index, group := range snapshot.Groups {
		if group.Name == groupName {
			groupIndex = index
			break
		}
	}
	if groupIndex < 0 {
		return
	}
	groups := append([]tuiGroup(nil), snapshot.Groups...)
	group := groups[groupIndex]
	speeds := make(
		map[string]tuiSpeedResult,
		len(group.Speeds)+len(updates),
	)
	for name, value := range group.Speeds {
		speeds[name] = value
	}
	for node, speed := range updates {
		speeds[node] = speed
	}
	group.Speeds = speeds
	groups[groupIndex] = group
	snapshot.Groups = groups
}

func formatTUISpeed(result tuiSpeedResult) string {
	megabytesPerSecond := result.BytesPerSecond / 1_000_000
	megabitsPerSecond := result.BytesPerSecond * 8 / 1_000_000
	return fmt.Sprintf(
		"%.2f MB/s · %.1f Mbps",
		megabytesPerSecond,
		megabitsPerSecond,
	)
}

func (m *tuiModel) startEditor(path string) tea.Cmd {
	if m.busy {
		m.snapshot.Status = "Another operation is still running"
		return nil
	}
	if m.service == nil {
		m.snapshot.Status = "Editing shared YAML requires the managed backend"
		return nil
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	info, err := os.Lstat(path)
	if err != nil {
		m.snapshot.Status = "Editor failed: " + err.Error()
		return nil
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		m.snapshot.Status = "Editor failed: configuration must be a regular file"
		return nil
	}
	backup, err := os.ReadFile(path)
	if err != nil {
		m.snapshot.Status = "Editor failed: " + err.Error()
		return nil
	}
	temporary, err := os.CreateTemp("", "flclash-tui-edit-*.yaml")
	if err != nil {
		m.snapshot.Status = "Editor failed: " + err.Error()
		return nil
	}
	temporaryPath := temporary.Name()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
		m.snapshot.Status = "Editor failed: " + err.Error()
		return nil
	}
	if _, err := temporary.Write(backup); err != nil {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
		m.snapshot.Status = "Editor failed: " + err.Error()
		return nil
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		m.snapshot.Status = "Editor failed: " + err.Error()
		return nil
	}
	command := exec.Command(
		"sh",
		"-c",
		editor+" -- \"$1\"",
		"flclash-tui-editor",
		temporaryPath,
	)
	m.editorPath = path
	m.editorTempPath = temporaryPath
	m.editorBackup = tuiProfileBackup{
		data: backup,
		mode: info.Mode(),
	}
	m.busy = true
	m.snapshot.Status = "Editor open"
	return tea.ExecProcess(command, func(err error) tea.Msg {
		return tuiEditorResultMsg{err: err}
	})
}
