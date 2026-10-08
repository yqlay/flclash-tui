//go:build linux && !cgo && cli

package main

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *tuiModel) shutdown() {
	if m.sshOperationCancel != nil {
		m.sshOperationCancel()
	}
	_ = waitCLISSHPendingOperations(6 * time.Second)
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
		cliHub.Shutdown()
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
		cmds = append(cmds, m.startSSHLocalRefresh(), m.pollSelectedSSHRelay())
	}
	return tea.Batch(cmds...)
}

func (m *tuiModel) waitServiceUpdate() tea.Cmd {
	if m.service == nil {
		return nil
	}
	service := m.service.forInstance(m.backendInstanceID)
	revision := m.backendRevision
	generation := m.backendGeneration
	return func() tea.Msg {
		status, err := service.watch(revision, 30*time.Second)
		return tuiServiceWatchMsg{status: status, err: err, generation: generation}
	}
}

func (m *tuiModel) startRefresh() tea.Cmd {
	if m.refreshInFlight || m.busy {
		return nil
	}
	m.refreshInFlight = true
	m.refreshSequence++
	sequence := m.refreshSequence
	generation := m.backendGeneration
	scope := tuiIdleTickPlanFor(m.snapshot.Page)
	m.refreshIncludesHistory = scope.FetchHistory
	m.refreshIncludesLogs = scope.FetchLogs
	snapshot := cloneTUISnapshot(m.snapshot)
	clearTUITransientRefreshStatus(&snapshot)
	previousLogs := append([]string(nil), m.snapshot.Logs...)
	client := m.client
	paths := m.paths
	service := m.service
	fetchHistory := scope.FetchHistory
	fetchLogs := scope.FetchLogs
	return func() tea.Msg {
		var serviceStatus *tuiServiceStatus
		var configuredSettings *tuiSettings
		original := cloneTUISnapshot(snapshot)
		initialPaths := paths
		unavailable := func(err error) tea.Msg {
			refreshTUILocalSnapshot(&original, initialPaths)
			applyTUISSHConnections(&original, filterConnectionsBySource(original.Connections, tuiTrafficSourceProxy), tuiSelectedConnectionID(original), tuiSelectedRequestID(original))
			original.setStatus(newTUIMessage("ui.3dbb8b44d1fa", "Status: "+err.Error()))
			return tuiRefreshResultMsg{sequence: sequence, generation: generation, snapshot: original, paths: initialPaths, backendUnavailable: true}
		}
		if service != nil {
			status, err := service.status()
			if err != nil {
				return unavailable(err)
			}
			serviceStatus = &status
			if status.HomeDir != "" {
				paths.HomeDir = status.HomeDir
			}
			if status.ConfigPath != "" {
				paths.ConfigPath = status.ConfigPath
			}
			if !status.Running {
				configuredSettings = loadTUIConfiguredSettings(paths.ConfigPath, true)
				if configuredSettings != nil {
					snapshot.Settings = *configuredSettings
				}
			}
			applyTUIBackendDisplay(&snapshot, status)
		}
		if paths.ConfigPath != "" {
			snapshot.GroupOrder = loadTUIProxyGroupOrder(paths.ConfigPath)
		}
		refreshTUISnapshot(&snapshot, client)
		refreshTUILocalSnapshot(&snapshot, paths)
		refreshIssues := make([]string, 0, 2)
		if service != nil {
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
					snapshot.BackendLogs = append([]string(nil), status.Logs...)
					snapshot.LogsInitialized = true
					mergeTUILogBuffers(&snapshot, cliLogSnapshot())
				} else {
					snapshot.Logs = previousLogs
					refreshIssues = append(refreshIssues, "Logs: "+err.Error())
				}
			} else {
				mergeTUILogBuffers(&snapshot, cliLogSnapshot())
			}
		}
		if service != nil {
			if status, err := service.status(); err == nil {
				if serviceStatus != nil && (status.InstanceID != serviceStatus.InstanceID || status.Revision != serviceStatus.Revision) {
					return tuiRefreshResultMsg{sequence: sequence, generation: generation, retry: true}
				}
				serviceStatus = &status
			} else {
				return unavailable(err)
			}
		}
		if configuredSettings != nil {
			snapshot.Settings = *configuredSettings
		}
		if serviceStatus != nil {
			applyTUIBackendDisplay(&snapshot, *serviceStatus)
		}
		if len(refreshIssues) > 0 && (snapshot.Status == "" || snapshot.Status == "Connected" || snapshot.Status == "Loading...") {
			snapshot.setStatus(newTUIMessage("ui.3dbb8b44d1fa", strings.Join(refreshIssues, " · ")))
		}
		return tuiRefreshResultMsg{
			generation:         generation,
			sequence:           sequence,
			snapshot:           snapshot,
			serviceStatus:      serviceStatus,
			configuredSettings: configuredSettings,
			paths:              paths,
		}
	}
}

func refreshTUILocalSnapshot(snapshot *tuiSnapshot, paths cliPaths) {
	refreshTUIProfiles(snapshot, paths)
	refreshTUISSH(snapshot)
	snapshot.Frontends, _ = listCLIFrontends()
}

func tuiSelectedConnectionID(snapshot tuiSnapshot) string {
	if index := snapshot.SelectedConnection; index >= 0 && index < len(snapshot.Connections) {
		return snapshot.Connections[index].ID
	}
	return ""
}

func tuiSelectedRequestID(snapshot tuiSnapshot) string {
	if index := snapshot.SelectedRequest; index >= 0 && index < len(snapshot.Requests) {
		return snapshot.Requests[index].ID
	}
	return ""
}

func mergeTUIUnavailableBackend(current, refreshed tuiSnapshot, paths cliPaths, currentPaths cliPaths) tuiSnapshot {
	// Only independent/local data is trustworthy without a complete Backend
	// status bracket. Keep the latest live state, not the command's old copy.
	merged := cloneTUISnapshot(current)
	merged.SSHProfiles = refreshed.SSHProfiles
	merged.Frontends = refreshed.Frontends
	if paths.HomeDir == currentPaths.HomeDir {
		merged.Profiles = refreshed.Profiles
		for index := range merged.Profiles {
			merged.Profiles[index].Current = filepath.Clean(merged.Profiles[index].Path) == filepath.Clean(currentPaths.ConfigPath)
		}
	}
	merged.Connections = mergeTUITrafficConnections(filterConnectionsBySource(current.Connections, tuiTrafficSourceProxy), filterConnectionsBySource(refreshed.Connections, tuiTrafficSourceSSH))
	merged.setStatus(refreshed.currentMessage())
	return merged
}

func (m *tuiModel) startOperation(action func(*tuiOperationState)) tea.Cmd {
	if m.busy {
		m.snapshot.setStatus(newTUIMessage("ui.c90e0573178b"))
		return nil
	}
	m.busy = true
	m.syncNetworkExit()
	m.operationSequence++
	m.refreshInFlight = false
	m.refreshSequence++
	state := tuiOperationState{
		operationID:        m.operationSequence,
		networkGeneration:  m.networkExitGeneration,
		testingIndicators:  captureTUIOperationTestingIndicators(m.snapshot),
		service:            m.service.forInstance(m.backendInstanceID),
		backendInstanceID:  m.backendInstanceID,
		backendGeneration:  m.backendGeneration,
		snapshot:           cloneTUISnapshot(m.snapshot),
		paths:              m.paths,
		setupParams:        append([]byte(nil), m.setupParams...),
		coreRunning:        m.coreRunning,
		systemProxyManaged: m.systemProxyManaged,
		pendingMixedPort:   cloneTUIOptionalInt(m.pendingMixedPort),
		stagedSettings:     cloneTUISettings(m.stagedSettings),
		settingsDirty:      m.settingsDirty,
		settingsDraft:      cloneTUISettingsDraft(m.settingsDraft),
		backendRevision:    m.backendRevision,
	}
	m.snapshot.setStatus(newTUIMessage("ui.b93900bded31"))
	return func() tea.Msg {
		action(&state)
		return tuiOperationResultMsg{state: state}
	}
}

// Commands execute outside Bubble Tea's update loop. Never share writable
// slices/maps with the live model or a concurrent refresh/traffic result.
func cloneTUISnapshot(source tuiSnapshot) tuiSnapshot {
	cloned := source
	cloned.HistoryCount = cloneTUIOptionalInt(source.HistoryCount)
	cloned.TunRequested = cloneTUIOptionalBool(source.TunRequested)
	cloned.Groups = append([]tuiGroup(nil), source.Groups...)
	for index := range cloned.Groups {
		cloned.Groups[index].Nodes = append([]string(nil), source.Groups[index].Nodes...)
		cloned.Groups[index].Delays = maps.Clone(source.Groups[index].Delays)
		cloned.Groups[index].Speeds = maps.Clone(source.Groups[index].Speeds)
	}
	cloned.Profiles = append([]tuiProfile(nil), source.Profiles...)
	for index := range cloned.Profiles {
		if info := source.Profiles[index].SubscriptionInfo; info != nil {
			copied := *info
			cloned.Profiles[index].SubscriptionInfo = &copied
		}
	}
	cloned.SSHProfiles = append([]tuiSSHProfile(nil), source.SSHProfiles...)
	for index := range cloned.SSHProfiles {
		cloned.SSHProfiles[index].Options = append([]string(nil), source.SSHProfiles[index].Options...)
	}
	cloned.Connections = append([]tuiConnection(nil), source.Connections...)
	cloned.Requests = append([]tuiRequest(nil), source.Requests...)
	cloned.Providers = append([]tuiProvider(nil), source.Providers...)
	cloned.GroupOrder = append([]string(nil), source.GroupOrder...)
	cloned.TrafficHistory = append([]trafficSnapshot(nil), source.TrafficHistory...)
	cloned.SSHTrafficHistory = append([]trafficSnapshot(nil), source.SSHTrafficHistory...)
	cloned.Logs = append([]string(nil), source.Logs...)
	cloned.BackendLogs = append([]string(nil), source.BackendLogs...)
	cloned.LocalLogs = append([]string(nil), source.LocalLogs...)
	cloned.Frontends = append([]cliProcessOwner(nil), source.Frontends...)
	return cloned
}

func cloneTUIOptionalInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTUIOptionalBool(value *bool) *bool {
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
	if !refreshed.controllerError() &&
		!current.controllerError() &&
		current.Status != "" &&
		current.Status != "Connected" &&
		current.Status != "Loading..." {
		refreshed.setStatus(current.currentMessage())
	}
	return refreshed
}

func clearTUITransientRefreshStatus(snapshot *tuiSnapshot) {
	if snapshot.controllerError() {
		snapshot.setStatus(newTUIMessage("ui.47d2a515ef2f"))
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
	updated.Language = current.Language
	updated.SelectedMenu = current.SelectedMenu
	updated.SelectedDashboard = current.SelectedDashboard
	updated.SelectedSetting = current.SelectedSetting
	updated.SelectedTool = current.SelectedTool
	updated.SelectedMaintenance = current.SelectedMaintenance
	updated.ProxyView = current.ProxyView
	updated.ProxyNodeFocus = current.ProxyNodeFocus
	updated.SSHDashboardFocus = current.SSHDashboardFocus
	updated.SSHDetailName = current.SSHDetailName
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
	updated.TrafficSource = current.TrafficSource
	updated.HistoryFilter = current.HistoryFilter
	updated.HistoryQuery = current.HistoryQuery
	updated.HistoryDetailOpen = current.HistoryDetailOpen
	updated.HistoryDetailScroll = current.HistoryDetailScroll
	updated.ConnectionsQuery = current.ConnectionsQuery
	updated.ConnectionsDetailOpen = current.ConnectionsDetailOpen
	updated.ConnectionsDetailScroll = current.ConnectionsDetailScroll
	updated.LogsQuery = current.LogsQuery
	updated.LogsLevel = current.LogsLevel
	updated.LogDetailOpen = current.LogDetailOpen
	updated.LogDetailScroll = current.LogDetailScroll
	updated.DashboardScroll = current.DashboardScroll
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
	if updated.SelectedConnection < 0 || findTUIInt(matchedTUIConnectionIndexes(updated), updated.SelectedConnection) < 0 {
		updated.ConnectionsDetailOpen = false
	}
	if !updated.ConnectionsDetailOpen {
		updated.ConnectionsDetailScroll = 0
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
	if updated.SelectedRequest < 0 || findTUIInt(matchedTUIRequestIndexes(updated), updated.SelectedRequest) < 0 {
		updated.HistoryDetailOpen = false
	}
	if !updated.HistoryDetailOpen {
		updated.HistoryDetailScroll = 0
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
	if !updated.LogDetailOpen {
		updated.LogDetailScroll = 0
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
	if updated.SSHDetailName != "" &&
		findTUISSHProfile(updated.SSHProfiles, updated.SSHDetailName) < 0 {
		updated.SSHDetailName = ""
		updated.SSHDashboardFocus = false
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
				if findTUIStringExact(updated[index].Nodes, node) < 0 {
					continue
				}
				if next, exists := delays[node]; !exists || !next.Manual && (delay.Manual || delay.Testing || delay.Samples > 1 || delay.Error != "") {
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
				if findTUIStringExact(updated[index].Nodes, node) < 0 {
					continue
				}
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
