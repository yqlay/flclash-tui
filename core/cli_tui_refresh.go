//go:build linux && !cgo && cli

package main

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *tuiModel) shutdown() {
	m.editorBackup.release()
	if m.editorTempPath != "" {
		_ = os.Remove(m.editorTempPath)
	}
	if m.frontendExitRequested || !m.shutdownRequested || !m.ownsCore {
		return
	}
	if m.service != nil {
		return
	}
	if m.ownsCore {
		handleShutdown()
	}
}

func tuiTickCommand() tea.Cmd {
	return tea.Tick(tuiRefreshInterval, func(value time.Time) tea.Msg {
		return tuiTickMsg(value)
	})
}

func (m *tuiModel) idleTickCommand() tea.Cmd {
	plan := tuiIdleTickPlanFor(m.snapshot.Page)
	m.lastIdleTick = plan
	if !plan.RefreshSnapshot && !plan.FetchHistory && !plan.FetchLogs {
		m.refreshIncludesHistory = false
		m.refreshIncludesLogs = false
	}
	cmds := []tea.Cmd{tuiTickCommand()}
	if plan.RefreshSnapshot || plan.FetchHistory || plan.FetchLogs {
		cmds = append(cmds, m.startRefresh())
	}
	if plan.SampleMemory {
		cmds = append(cmds, m.startMemoryRefresh())
	}
	if plan.PollSSH {
		cmds = append(cmds, m.pollSelectedSSHRelay())
	}
	return tea.Batch(cmds...)
}

func (m *tuiModel) waitServiceUpdate() tea.Cmd {
	if m.service == nil {
		return nil
	}
	service := m.service
	revision := m.backendRevision
	return func() tea.Msg {
		status, err := service.watch(revision, 30*time.Second)
		return tuiServiceWatchMsg{status: status, err: err}
	}
}

func (m *tuiModel) startRefresh() tea.Cmd {
	if m.refreshInFlight || m.busy {
		return nil
	}
	m.refreshInFlight = true
	m.refreshSequence++
	sequence := m.refreshSequence
	scope := tuiIdleTickPlanFor(m.snapshot.Page)
	m.refreshIncludesHistory = scope.FetchHistory
	m.refreshIncludesLogs = scope.FetchLogs
	snapshot := m.snapshot
	clearTUITransientRefreshStatus(&snapshot)
	previousLogs := append([]string(nil), m.snapshot.Logs...)
	client := m.client
	paths := m.paths
	service := m.service
	fetchHistory := scope.FetchHistory
	fetchLogs := scope.FetchLogs
	return func() tea.Msg {
		refreshTUISnapshot(&snapshot, client)
		refreshTUIProfiles(&snapshot, paths)
		refreshTUISSH(&snapshot)
		snapshot.Frontends, _ = listCLIFrontends()
		refreshIssues := make([]string, 0, 2)
		var serviceStatus *tuiServiceStatus
		if service != nil {
			if status, err := service.status(); err == nil {
				serviceStatus = &status
			} else {
				refreshIssues = append(refreshIssues, "Status: "+err.Error())
			}
			if fetchHistory {
				if status, err := service.history(); err == nil {
					snapshot.Requests = append([]tuiRequest(nil), status.History...)
				} else {
					refreshIssues = append(refreshIssues, "History: "+err.Error())
				}
			}
		}
		if fetchLogs {
			if service != nil {
				if status, err := service.logs(1000); err == nil {
					localLogs := cliLogSnapshot()
					snapshot.Logs = append(append([]string(nil), status.Logs...), localLogs...)
					if len(snapshot.Logs) > 1500 {
						snapshot.Logs = snapshot.Logs[len(snapshot.Logs)-1500:]
					}
				} else {
					snapshot.Logs = previousLogs
					refreshIssues = append(refreshIssues, "Logs: "+err.Error())
				}
			} else {
				snapshot.Logs = cliLogSnapshot()
			}
		}
		if len(refreshIssues) > 0 && (snapshot.Status == "" || snapshot.Status == "Connected" || snapshot.Status == "Loading...") {
			snapshot.Status = "Refresh incomplete · " + strings.Join(refreshIssues, " · ")
		}
		return tuiRefreshResultMsg{
			sequence:      sequence,
			snapshot:      snapshot,
			serviceStatus: serviceStatus,
		}
	}
}

func (m *tuiModel) startOperation(action func(*tuiOperationState)) tea.Cmd {
	if m.busy {
		m.snapshot.Status = "Another operation is still running"
		return nil
	}
	m.busy = true
	m.refreshInFlight = false
	m.refreshSequence++
	state := tuiOperationState{
		snapshot:           m.snapshot,
		paths:              m.paths,
		setupParams:        append([]byte(nil), m.setupParams...),
		coreRunning:        m.coreRunning,
		systemProxyManaged: m.systemProxyManaged,
		pendingMixedPort:   cloneTUIOptionalInt(m.pendingMixedPort),
		stagedSettings:     cloneTUISettings(m.stagedSettings),
		settingsDirty:      m.settingsDirty,
		backendRevision:    m.backendRevision,
	}
	m.snapshot.Status = "Working..."
	return func() tea.Msg {
		action(&state)
		return tuiOperationResultMsg{state: state}
	}
}

func cloneTUIOptionalInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTUISettings(value *tuiSettings) *tuiSettings {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func mergeTUIRefresh(current, refreshed tuiSnapshot) tuiSnapshot {
	refreshed.Traffic = current.Traffic
	refreshed.TrafficHistory = append(
		[]trafficSnapshot(nil),
		current.TrafficHistory...,
	)
	refreshed.TotalTraffic = current.TotalTraffic
	refreshed = preserveTUIInteraction(current, refreshed)
	if !current.UpdatedAt.IsZero() {
		refreshed.Settings.SystemProxy = current.Settings.SystemProxy
	}
	if !tuiStatusIsControllerError(refreshed.Status) &&
		!tuiStatusIsControllerError(current.Status) &&
		current.Status != "" &&
		current.Status != "Connected" &&
		current.Status != "Loading..." {
		refreshed.Status = current.Status
	}
	return refreshed
}

func tuiStatusIsControllerError(status string) bool {
	return strings.HasPrefix(status, "Controller unavailable:") ||
		strings.HasPrefix(status, "Invalid controller response:") ||
		strings.HasPrefix(status, "Connections refresh failed:") ||
		strings.HasPrefix(status, "Refresh incomplete ·") ||
		strings.HasPrefix(status, "SSH profiles unavailable:")
}

func clearTUITransientRefreshStatus(snapshot *tuiSnapshot) {
	if tuiStatusIsControllerError(snapshot.Status) {
		snapshot.Status = "Loading..."
	}
}

func mergeTUIOperation(current, result tuiSnapshot) tuiSnapshot {
	// Traffic updates arrive independently while an operation is running. The
	// operation snapshot was captured before those updates, so never replace the
	// live counters with its stale copy when the result is applied.
	result.Traffic = current.Traffic
	result.TrafficHistory = append(
		[]trafficSnapshot(nil),
		current.TrafficHistory...,
	)
	result.TotalTraffic = current.TotalTraffic
	return preserveTUIInteraction(current, result)
}

func preserveTUIInteraction(current, updated tuiSnapshot) tuiSnapshot {
	selectedGroupName := ""
	selectedNodeName := ""
	if current.SelectedGroup >= 0 && current.SelectedGroup < len(current.Groups) {
		selectedGroup := current.Groups[current.SelectedGroup]
		selectedGroupName = selectedGroup.Name
		if current.SelectedNode >= 0 && current.SelectedNode < len(selectedGroup.Nodes) {
			selectedNodeName = selectedGroup.Nodes[current.SelectedNode]
		}
	}
	selectedConnectionID := ""
	if current.SelectedConnection >= 0 && current.SelectedConnection < len(current.Connections) {
		selectedConnectionID = current.Connections[current.SelectedConnection].ID
	}
	selectedRequestID := ""
	if current.SelectedRequest >= 0 && current.SelectedRequest < len(current.Requests) {
		selectedRequestID = current.Requests[current.SelectedRequest].ID
	}
	selectedLog := ""
	if current.SelectedLog >= 0 && current.SelectedLog < len(current.Logs) {
		selectedLog = current.Logs[current.SelectedLog]
	}
	selectedProviderName := ""
	if current.SelectedProvider >= 0 && current.SelectedProvider < len(current.Providers) {
		selectedProviderName = current.Providers[current.SelectedProvider].Name
	}
	selectedProfilePath := ""
	importSelected := current.SelectedRow < 0
	if current.SelectedRow >= 0 && current.SelectedRow < len(current.Profiles) {
		selectedProfilePath = current.Profiles[current.SelectedRow].Path
	}
	selectedSSHName := ""
	captureSelected := current.SelectedSSH == tuiSSHCaptureRow
	if current.SelectedSSH >= 0 && current.SelectedSSH < len(current.SSHProfiles) {
		selectedSSHName = current.SSHProfiles[current.SelectedSSH].Name
	}

	updated.Page = current.Page
	updated.SelectedMenu = current.SelectedMenu
	updated.SelectedDashboard = current.SelectedDashboard
	updated.SelectedSetting = current.SelectedSetting
	updated.SelectedTool = current.SelectedTool
	updated.SelectedMaintenance = current.SelectedMaintenance
	updated.ProxyView = current.ProxyView
	updated.ProxyNodeFocus = current.ProxyNodeFocus
	updated.SSHDashboardFocus = current.SSHDashboardFocus
	updated.SelectedSSHDetail = current.SelectedSSHDetail
	updated.ManagedService = current.ManagedService
	updated.FocusSidebar = current.FocusSidebar
	updated.ShowHelp = current.ShowHelp
	updated.SSHNetwork = current.SSHNetwork
	updated.SSHDelay = current.SSHDelay
	updated.SSHSpeed = current.SSHSpeed
	updated.SSHDirectProbe = current.SSHDirectProbe
	updated.SSHDirectNetwork = current.SSHDirectNetwork
	updated.SSHDirectDelay = current.SSHDirectDelay
	updated.SSHDirectSpeed = current.SSHDirectSpeed
	updated.SSHTraffic = current.SSHTraffic
	updated.SSHTrafficHistory = append(
		[]trafficSnapshot(nil),
		current.SSHTrafficHistory...,
	)
	updated.SSHTotalTraffic = current.SSHTotalTraffic
	updated.SSHConnections = current.SSHConnections
	if len(current.GroupOrder) > 0 {
		updated.GroupOrder = append([]string(nil), current.GroupOrder...)
		orderTUIGroups(updated.Groups, updated.GroupOrder)
	}
	if current.Network.Loading ||
		current.Network.CheckedAt.After(updated.Network.CheckedAt) {
		updated.Network = current.Network
	}
	if current.Memory.UpdatedAt.After(updated.Memory.UpdatedAt) ||
		current.Memory.CoreUpdated.After(updated.Memory.CoreUpdated) {
		updated.Memory = current.Memory
	}
	mergeTUIGroupDelays(current.Groups, updated.Groups)

	updated.SelectedGroup = findTUIGroupExact(updated.Groups, selectedGroupName)
	if selectedGroupName == "" {
		updated.SelectedGroup = clampTUISelection(current.SelectedGroup, len(updated.Groups))
	}
	if updated.SelectedGroup >= 0 && updated.SelectedGroup < len(updated.Groups) {
		updated.SelectedNode = findTUIStringExact(
			updated.Groups[updated.SelectedGroup].Nodes,
			selectedNodeName,
		)
		if selectedNodeName == "" {
			updated.SelectedNode = clampTUISelection(
				current.SelectedNode,
				len(updated.Groups[updated.SelectedGroup].Nodes),
			)
		}
		if updated.SelectedNode < 0 {
			updated.ProxyNodeFocus = false
		}
	} else {
		updated.SelectedNode = -1
		updated.ProxyNodeFocus = false
	}
	updated.SelectedConnection = findTUIConnection(updated.Connections, selectedConnectionID)
	if selectedConnectionID == "" {
		updated.SelectedConnection = clampTUISelection(
			current.SelectedConnection,
			len(updated.Connections),
		)
		if current.ConnectionsDetailOpen {
			updated.ConnectionsDetailOpen = false
		}
	}
	if updated.SelectedConnection < 0 {
		updated.ConnectionsDetailOpen = false
	}
	updated.SelectedRequest = findTUIRequest(updated.Requests, selectedRequestID)
	if selectedRequestID == "" {
		updated.SelectedRequest = clampTUISelection(
			current.SelectedRequest,
			len(updated.Requests),
		)
		if current.HistoryDetailOpen {
			updated.HistoryDetailOpen = false
		}
	}
	if updated.SelectedRequest < 0 {
		updated.HistoryDetailOpen = false
	}
	updated.SelectedLog = findTUILog(updated.Logs, selectedLog)
	selectedLogFound := updated.SelectedLog >= 0
	if selectedLog == "" || updated.SelectedLog < 0 {
		updated.SelectedLog = firstTUILogMatch(updated)
	}
	if current.LogDetailOpen && !selectedLogFound ||
		updated.SelectedLog < 0 ||
		findTUIInt(matchedTUILogIndexes(updated), updated.SelectedLog) < 0 {
		updated.LogDetailOpen = false
	}
	updated.SelectedProvider = findTUIProvider(updated.Providers, selectedProviderName)
	if selectedProviderName == "" {
		updated.SelectedProvider = clampTUISelection(
			current.SelectedProvider,
			len(updated.Providers),
		)
	}
	if importSelected {
		updated.SelectedRow = current.SelectedRow
	} else if selectedProfilePath != "" {
		updated.SelectedRow = findTUIProfileExact(
			updated.Profiles,
			selectedProfilePath,
		)
		if updated.SelectedRow < 0 {
			updated.SelectedRow = tuiProfileImportSubscriptionRow
		}
	} else {
		updated.SelectedRow = clampTUISelection(current.SelectedRow, len(updated.Profiles))
	}
	if !importSelected && len(updated.Profiles) == 0 {
		updated.SelectedRow = tuiProfileImportSubscriptionRow
	}
	if captureSelected || len(updated.SSHProfiles) == 0 {
		updated.SelectedSSH = tuiSSHCaptureRow
	} else {
		updated.SelectedSSH = findTUISSHProfile(updated.SSHProfiles, selectedSSHName)
		if selectedSSHName == "" {
			updated.SelectedSSH = clampTUISelection(
				current.SelectedSSH,
				len(updated.SSHProfiles),
			)
		}
	}
	return updated
}

func findTUISSHProfile(profiles []tuiSSHProfile, name string) int {
	for index, profile := range profiles {
		if strings.EqualFold(profile.Name, name) {
			return index
		}
	}
	return -1
}

func findTUIGroupExact(groups []tuiGroup, name string) int {
	if name == "" {
		return -1
	}
	for index, group := range groups {
		if group.Name == name {
			return index
		}
	}
	return -1
}

func findTUIStringExact(values []string, value string) int {
	if value == "" {
		return -1
	}
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return -1
}

func findTUIProfileExact(profiles []tuiProfile, path string) int {
	if path == "" {
		return -1
	}
	for index, profile := range profiles {
		if filepath.Clean(profile.Path) == filepath.Clean(path) {
			return index
		}
	}
	return -1
}

func findTUILog(logs []string, line string) int {
	if line == "" {
		return -1
	}
	for index := len(logs) - 1; index >= 0; index-- {
		if logs[index] == line {
			return index
		}
	}
	return -1
}

func mergeTUIGroupDelays(current, updated []tuiGroup) {
	currentByName := make(map[string]tuiGroup, len(current))
	for _, group := range current {
		currentByName[group.Name] = group
	}
	for index := range updated {
		previous, ok := currentByName[updated[index].Name]
		if !ok {
			continue
		}
		if len(previous.Delays) > 0 {
			delays := make(
				map[string]tuiDelayResult,
				len(updated[index].Delays)+len(previous.Delays),
			)
			for node, delay := range updated[index].Delays {
				delays[node] = delay
			}
			for node, delay := range previous.Delays {
				if _, exists := delays[node]; !exists {
					delays[node] = delay
				}
			}
			updated[index].Delays = delays
		}
		if len(previous.Speeds) > 0 {
			speeds := make(
				map[string]tuiSpeedResult,
				len(updated[index].Speeds)+len(previous.Speeds),
			)
			for node, speed := range updated[index].Speeds {
				speeds[node] = speed
			}
			for node, speed := range previous.Speeds {
				if _, exists := speeds[node]; !exists {
					speeds[node] = speed
				}
			}
			updated[index].Speeds = speeds
		}
	}
}

func findTUIProfile(profiles []tuiProfile, path string) int {
	for index, profile := range profiles {
		if filepath.Clean(profile.Path) == filepath.Clean(path) {
			return index
		}
	}
	return 0
}

func clampTUISelection(index, total int) int {
	if total <= 0 {
		return 0
	}
	if index < 0 {
		return 0
	}
	if index >= total {
		return total - 1
	}
	return index
}
