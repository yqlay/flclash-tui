//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *tuiModel) handleKey(key tuiKey) tea.Cmd {
	switch key {
	case tuiKeyQuit:
		m.frontendExitRequested = true
		m.stopCoreMemoryMonitor()
		m.stopTrafficMonitor()
		return tea.Quit
	case tuiKeyInterrupt:
		if m.shutdownRequested {
			return nil
		}
		m.shutdownRequested = true
		m.snapshot.Status = "Shutting down all frontends, Backend, and Core..."
		return func() tea.Msg {
			return tuiShutdownResultMsg{
				err: completeCLIExitForTUI(os.Getpid()),
			}
		}
	case tuiKeyBack:
		if m.snapshot.Page == tuiPageSSH && m.snapshot.SSHDashboardFocus {
			m.snapshot.SSHDashboardFocus = false
			m.snapshot.Status = "SSH profiles · Enter connects or focuses Dashboard"
		} else if m.snapshot.Page == tuiPageRequests && m.snapshot.HistoryDetailOpen {
			m.snapshot.HistoryDetailOpen = false
		} else if m.snapshot.Page == tuiPageConnections && m.snapshot.ConnectionsDetailOpen {
			m.snapshot.ConnectionsDetailOpen = false
		} else if m.snapshot.Page == tuiPageLogs && m.snapshot.LogDetailOpen {
			m.snapshot.LogDetailOpen = false
		} else if !m.snapshot.FocusSidebar &&
			m.snapshot.Page == tuiPageProxies &&
			m.snapshot.ProxyView == tuiProxyViewGroups &&
			m.snapshot.ProxyNodeFocus {
			m.snapshot.ProxyNodeFocus = false
			m.snapshot.Status = "Proxy groups · Enter opens nodes · d node RTT (5 samples) · v speed"
		} else if !m.snapshot.FocusSidebar {
			m.snapshot.FocusSidebar = true
			m.snapshot.SelectedMenu = int(m.snapshot.Page)
		}
		return nil
	case tuiKeyRefresh:
		m.refreshInFlight = false
		m.refreshSequence++
		return m.startRefresh()
	case tuiKeyReload:
		return m.startOperation(func(state *tuiOperationState) {
			if reloadErr := reloadTUIOperationConfig(
				state,
				m.service,
				m.client,
				m.ownsCore,
			); reloadErr != nil {
				state.snapshot.Status = "Reload failed: " + reloadErr.Error()
			} else {
				state.snapshot.Status = "Configuration reloaded"
				syncStoppedTUISettings(state)
			}
		})
	case tuiKeyHelp:
		m.snapshot.ShowHelp = !m.snapshot.ShowHelp
	case tuiKeySearch:
		switch m.snapshot.Page {
		case tuiPageRequests:
			m.beginInput(tuiInputHistorySearch)
		case tuiPageConnections:
			m.beginInput(tuiInputConnectionsSearch)
		case tuiPageLogs:
			m.beginInput(tuiInputLogsSearch)
		}
	case tuiKeyFilter:
		switch m.snapshot.Page {
		case tuiPageConnections:
			m.cycleTrafficSource()
		case tuiPageRequests:
			filters := []string{"all", "active", "completed"}
			current := findTUIString(filters, tuiDefaultValue(m.snapshot.HistoryFilter, "all"))
			m.snapshot.HistoryFilter = filters[wrapTUIIndex(current, 1, len(filters))]
			m.snapshot.SelectedRequest = firstTUIRequestMatch(m.snapshot)
			m.snapshot.HistoryDetailOpen = false
			m.snapshot.Status = "History filter: " + m.snapshot.HistoryFilter
		case tuiPageLogs:
			levels := []string{"ALL", "ERROR", "WARN", "INFO", "DEBUG"}
			current := findTUIString(levels, tuiDefaultValue(m.snapshot.LogsLevel, "ALL"))
			m.snapshot.LogsLevel = levels[wrapTUIIndex(current, 1, len(levels))]
			m.snapshot.SelectedLog = firstTUILogMatch(m.snapshot)
			m.snapshot.LogDetailOpen = false
			m.snapshot.Status = "Log level filter: " + m.snapshot.LogsLevel
		}
	case tuiKeySourceFilter:
		if m.snapshot.Page == tuiPageRequests || m.snapshot.Page == tuiPageConnections {
			m.cycleTrafficSource()
		}
	case tuiKeyCloseConnections:
		if m.snapshot.Page == tuiPageSSH {
			if m.snapshot.FocusSidebar || m.snapshot.SSHDashboardFocus {
				m.snapshot.Status = "Focus SSH profiles before deleting"
				return nil
			}
			m.beginSSHDeleteConfirm()
			return nil
		} else if m.snapshot.Page == tuiPageProfiles {
			m.beginProfileDeleteConfirm()
			return nil
		} else if m.snapshot.Page == tuiPageRequests {
			if !m.dangerConfirmed {
				m.beginDangerConfirm("Clear shared History?", trafficClearConfirmMessage(m.snapshot.TrafficSource, "history"), key)
				return nil
			}
			if m.service != nil {
				return m.startOperation(func(state *tuiOperationState) {
					if !prepareTUIBackendRevision(state, m.service) {
						return
					}
					status, err := m.service.clearHistoryForSource(m.snapshot.TrafficSource, state.backendRevision)
					if err != nil {
						state.snapshot.Status = "Clear History failed: " + err.Error()
						return
					}
					state.backendRevision = status.Revision
					state.snapshot.Requests = nil
					state.snapshot.SelectedRequest = -1
					state.snapshot.HistoryDetailOpen = false
					state.snapshot.Status = "Shared History cleared"
				})
			}
			m.snapshot.Requests = nil
			m.snapshot.SelectedRequest = -1
			m.snapshot.HistoryDetailOpen = false
			m.snapshot.Status = "History cleared"
		} else if m.snapshot.Page == tuiPageLogs {
			if !m.dangerConfirmed {
				m.beginDangerConfirm("Clear shared logs?", "Delete the Backend log and its rotated backup, plus this TUI's in-memory log entries.", key)
				return nil
			}
			if m.service != nil {
				return m.startOperation(func(state *tuiOperationState) {
					if !prepareTUIBackendRevision(state, m.service) {
						return
					}
					status, err := m.service.clearLogs(state.backendRevision)
					if err != nil {
						state.snapshot.Status = "Clear logs failed: " + err.Error()
						return
					}
					state.backendRevision = status.Revision
					clearTUILogs()
					state.snapshot.Logs = nil
					state.snapshot.SelectedLog = -1
					state.snapshot.LogDetailOpen = false
					state.snapshot.Status = "Shared logs cleared"
				})
			}
			clearTUILogs()
			m.snapshot.Logs = nil
			m.snapshot.SelectedLog = -1
			m.snapshot.LogDetailOpen = false
			m.snapshot.Status = "Logs cleared"
		} else if m.snapshot.Page == tuiPageConnections {
			if !m.dangerConfirmed {
				m.beginDangerConfirm("Close all connections?", trafficClearConfirmMessage(m.snapshot.TrafficSource, "connections"), key)
				return nil
			}
			return m.startOperation(func(state *tuiOperationState) {
				var err error
				if m.service != nil {
					if !prepareTUIBackendRevision(state, m.service) {
						return
					}
					var status tuiServiceStatus
					status, err = m.service.closeAllConnectionsManagedForSource(m.snapshot.TrafficSource, state.backendRevision)
					if err == nil {
						state.backendRevision = status.Revision
					}
				} else {
					err = closeTUIVisibleConnectionsForSource(m.client, uint32(os.Getuid()), state.snapshot.Settings.TunEnabled && state.snapshot.Settings.TunScope == tuiTunScopeSystem, "", m.snapshot.TrafficSource)
				}
				if err != nil {
					state.snapshot.Status = "Close connections failed: " + err.Error()
				} else {
					state.snapshot.Status = "All connections closed"
				}
			})
		}
	case tuiKeyCloseConnection:
		if m.snapshot.Page == tuiPageSSH {
			if !m.snapshot.FocusSidebar && m.snapshot.SSHDashboardFocus {
				return m.testSelectedSSHDelayFor(
					tuiSSHDashboardRowIsDirect(m.snapshot.SelectedSSHDetail),
				)
			}
			m.snapshot.Status = "Focus SSH Dashboard before testing route delay"
			return nil
		}
		if m.snapshot.Page == tuiPageDashboard {
			return m.testDashboardDelay()
		}
		if !m.snapshot.FocusSidebar &&
			m.snapshot.Page == tuiPageProxies &&
			m.snapshot.ProxyView == tuiProxyViewGroups {
			if m.snapshot.ProxyNodeFocus {
				return m.testSelectedProxyDelay()
			}
			return m.testSelectedProxyGroupDelays()
		}
		if m.snapshot.Page == tuiPageConnections &&
			m.snapshot.SelectedConnection >= 0 &&
			m.snapshot.SelectedConnection < len(m.snapshot.Connections) {
			if !m.dangerConfirmed {
				connection := m.snapshot.Connections[m.snapshot.SelectedConnection]
				m.beginDangerConfirm("Close selected connection?", "Close "+cliDisplayValue(connection.Host)+" ("+connection.ID+").", key)
				m.dangerConfirmTarget = connection.ID
				return nil
			}
			connectionID := m.snapshot.Connections[m.snapshot.SelectedConnection].ID
			if m.dangerConfirmTarget != "" {
				connectionID = m.dangerConfirmTarget
			}
			return m.startOperation(func(state *tuiOperationState) {
				var err error
				if m.service != nil {
					if !prepareTUIBackendRevision(state, m.service) {
						return
					}
					var status tuiServiceStatus
					status, err = m.service.closeConnectionManagedForSource(connectionID, m.snapshot.TrafficSource, state.backendRevision)
					if err == nil {
						state.backendRevision = status.Revision
					}
				} else {
					err = closeTUIVisibleConnectionsForSource(m.client, uint32(os.Getuid()), state.snapshot.Settings.TunEnabled && state.snapshot.Settings.TunScope == tuiTunScopeSystem, connectionID, m.snapshot.TrafficSource)
				}
				if err != nil {
					state.snapshot.Status = "Close connection failed: " + err.Error()
				} else {
					state.snapshot.Status = "Connection closed"
				}
			})
		}
		if m.snapshot.Page == tuiPageConnections {
			m.snapshot.Status = "Select an active connection before closing it"
		}
	case tuiKeyCoreToggle:
		return m.startOperation(func(state *tuiOperationState) {
			if !m.ownsCore {
				state.snapshot.Status = "Core lifecycle is owned by the external process"
			} else if state.coreRunning {
				stopTUIManagedCore(state, m.service)
			} else {
				startTUIManagedCore(state, m.service)
			}
		})
	case tuiKeyEdit:
		switch m.snapshot.Page {
		case tuiPageSSH:
			if m.snapshot.FocusSidebar || m.snapshot.SSHDashboardFocus {
				m.snapshot.Status = "Focus SSH profiles before editing"
				return nil
			}
			m.beginSSHForm(true)
			return nil
		case tuiPageProfiles:
			if m.snapshot.SelectedRow < 0 ||
				m.snapshot.SelectedRow >= len(m.snapshot.Profiles) {
				m.snapshot.Status = "Select a profile before editing its YAML"
				return nil
			}
			return m.startEditor(m.snapshot.Profiles[m.snapshot.SelectedRow].Path)
		case tuiPageLogs:
			return m.startOperation(func(state *tuiOperationState) {
				path, err := exportTUILogs(state.paths.HomeDir, state.snapshot.Logs)
				if err != nil {
					state.snapshot.Status = "Export logs failed: " + err.Error()
				} else {
					state.snapshot.Status = "Logs exported: " + path
				}
			})
		case tuiPageMaintenance:
			return m.startEditor(m.paths.ConfigPath)
		default:
			m.snapshot.Status = "Edit YAML is available in Profiles and Maintenance"
		}
	case tuiKeyNewProfile:
		if m.snapshot.Page == tuiPageSSH {
			if !m.snapshot.FocusSidebar && m.snapshot.SSHDashboardFocus {
				return m.refreshSelectedSSHDashboard()
			}
			if m.snapshot.FocusSidebar {
				m.snapshot.Status = "Focus SSH profiles before adding a profile"
				return nil
			}
			m.beginSSHForm(false)
			return nil
		} else if m.snapshot.Page == tuiPageProfiles {
			m.beginInput(tuiInputSubscription)
		} else if m.snapshot.Page == tuiPageDashboard {
			return m.startNetworkCheck()
		}
	case tuiKeyRenameProfile:
		if m.snapshot.Page == tuiPageSSH {
			return m.toggleSelectedSSHDefault()
		}
		if m.snapshot.Page == tuiPageProfiles {
			m.beginProfileRename()
		}
	case tuiKeyUpdateProfile:
		if m.snapshot.Page == tuiPageProfiles {
			return m.updateSelectedProfileSubscription()
		}
	case tuiKeyProviders:
		cmds := m.changeVisiblePage(tuiPageProxies)
		m.snapshot.SelectedMenu = int(tuiPageProxies)
		m.snapshot.FocusSidebar = false
		m.snapshot.ProxyView = tuiProxyViewProviders
		m.snapshot.ProxyNodeFocus = false
		m.snapshot.Status = "Providers view · Enter updates the selected provider"
		return tea.Batch(cmds...)
	case tuiKeyBackup:
		if m.snapshot.Page == tuiPageMaintenance {
			return m.runTool(1)
		}
	case tuiKeyRestore:
		if m.snapshot.Page == tuiPageMaintenance {
			return m.runTool(2)
		}
	case tuiKeyGeoUpdate:
		if m.snapshot.Page == tuiPageMaintenance {
			return m.runTool(3)
		}
	case tuiKeyResetTraffic:
		if m.snapshot.Page == tuiPageMaintenance {
			return m.runTool(4)
		}
	case tuiKeyUp:
		return m.moveSelection(-1)
	case tuiKeyDown:
		return m.moveSelection(1)
	case tuiKeyDelayTest:
		if !m.snapshot.FocusSidebar &&
			m.snapshot.Page == tuiPageProxies &&
			m.snapshot.ProxyView == tuiProxyViewGroups {
			if m.snapshot.ProxyNodeFocus {
				return m.testSelectedProxyDelay()
			}
			return m.testSelectedProxyGroupDelays()
		}
	case tuiKeyDelayTestAll:
		if !m.snapshot.FocusSidebar &&
			m.snapshot.Page == tuiPageProxies &&
			m.snapshot.ProxyView == tuiProxyViewGroups {
			return m.testSelectedProxyGroupDelays()
		}
	case tuiKeySpeedTest:
		if m.snapshot.Page == tuiPageDashboard {
			return m.testDashboardSpeed()
		}
		if m.snapshot.Page == tuiPageSSH &&
			!m.snapshot.FocusSidebar &&
			m.snapshot.SSHDashboardFocus {
			return m.testSelectedSSHSpeedFor(
				tuiSSHDashboardRowIsDirect(m.snapshot.SelectedSSHDetail),
			)
		}
		if !m.snapshot.FocusSidebar &&
			m.snapshot.Page == tuiPageProxies &&
			m.snapshot.ProxyView == tuiProxyViewGroups {
			if m.snapshot.ProxyNodeFocus {
				return m.testSelectedProxySpeed()
			}
			return m.testSelectedProxyGroupSpeeds()
		}
	case tuiKeyViewPrevious, tuiKeyViewNext:
		if !m.snapshot.FocusSidebar && m.snapshot.Page == tuiPageProxies {
			delta := 1
			if key == tuiKeyViewPrevious {
				delta = -1
			}
			m.snapshot.ProxyView = wrapTUIIndex(
				m.snapshot.ProxyView,
				delta,
				tuiProxyViewCount,
			)
			if m.snapshot.ProxyView == tuiProxyViewProviders {
				m.snapshot.ProxyNodeFocus = false
				m.snapshot.Status = "Providers view · Enter updates the selected provider"
			} else {
				m.snapshot.ProxyNodeFocus = false
				m.snapshot.Status = "Proxy groups · Enter nodes · d node RTT group · v speed group"
			}
		}
	case tuiKeyPageUp:
		if m.snapshot.Page == tuiPageDashboard {
			step := maxTUIWidth(m.dashboardViewportLimit()-1, 1)
			m.snapshot.DashboardScroll = maxTUIIndex(
				m.snapshot.DashboardScroll - step,
			)
		}
	case tuiKeyPageDown:
		if m.snapshot.Page == tuiPageDashboard {
			limit := m.dashboardViewportLimit()
			maxScroll := maxTUIIndex(
				len(tuiCompactDashboardRows(
					m.snapshot,
					m.paths,
					maxTUIWidth(m.width-2, 1),
					m.dashboardPageHeight(),
				)) -
					limit,
			)
			m.snapshot.DashboardScroll = minTUI(
				m.snapshot.DashboardScroll+
					maxTUIWidth(limit-1, 1),
				maxScroll,
			)
		}
	case tuiKeySelect:
		return m.selectCurrent()
	case tuiKeyPortUp, tuiKeyPortDown:
		if m.snapshot.Page == tuiPageDashboard {
			if m.service == nil && m.ownsCore && !m.coreRunning {
				m.stageTUIAdjustedPort(key)
				return nil
			}
			return m.startOperation(func(state *tuiOperationState) {
				applyTUIOperationSetting(state, m.service, m.client, key)
			})
		}
	case tuiKeyMode:
		if m.snapshot.Page == tuiPageDashboard {
			m.beginModeSelection()
		}
	case tuiKeyAllowLAN, tuiKeyIPv6, tuiKeyUnifiedDelay, tuiKeyTCPConcurrent,
		tuiKeyTun, tuiKeyLogLevel:
		if key == tuiKeyAllowLAN && m.snapshot.Page == tuiPageSSH {
			return m.attachSelectedSSH()
		}
		settingsKey := m.snapshot.Page == tuiPageTools && key != tuiKeyTun
		dashboardTun := m.snapshot.Page == tuiPageDashboard && key == tuiKeyTun
		if settingsKey || dashboardTun {
			if m.service == nil && m.ownsCore && !m.coreRunning {
				m.stageTUISetting(key)
				return nil
			}
			return m.startOperation(func(state *tuiOperationState) {
				applyTUIOperationSetting(state, m.service, m.client, key)
			})
		}
	case tuiKeyTunScope:
		if m.snapshot.Page == tuiPageTools {
			return m.startOperation(func(state *tuiOperationState) {
				applyTUITunScope(state, m.service)
			})
		}
	case tuiKeySetPort:
		if m.snapshot.Page == tuiPageDashboard {
			m.beginInput(tuiInputMixedPort)
		}
	case tuiKeySystemProxy:
		if m.snapshot.Page == tuiPageDashboard {
			if m.service == nil {
				m.snapshot.Status = "System proxy changes require the managed backend"
				return nil
			}
			return m.startOperation(func(state *tuiOperationState) {
				enabled := !state.snapshot.Settings.SystemProxy
				if enabled && state.snapshot.Settings.Mode == tuiSilentMode {
					state.snapshot.Status = "System proxy cannot be enabled in silent mode; switch mode first"
					return
				}
				autoStarted := false
				if m.ownsCore && !state.coreRunning {
					if !startTUIManagedCore(state, m.service) {
						return
					}
					autoStarted = true
				}
				if !prepareTUIBackendRevision(state, m.service) {
					return
				}
				status, err := m.service.setSystemProxy(
					enabled,
					state.backendRevision,
				)
				proxyUpdated := err == nil
				if err != nil {
					state.snapshot.Status = "System proxy update failed: " + err.Error()
				} else {
					applyTUIOperationServiceStatus(state, status)
					state.snapshot.Status = "System proxy " + cliOnOff(status.SystemProxy)
				}
				if proxyUpdated {
					state.systemProxyManaged = state.snapshot.Settings.SystemProxy
					if autoStarted && state.snapshot.Settings.SystemProxy {
						state.snapshot.Status = fmt.Sprintf(
							"Core started on port %d; system proxy enabled",
							state.snapshot.Settings.MixedPort,
						)
					}
				} else if autoStarted {
					proxyError := state.snapshot.Status
					if stopTUIManagedCore(state, m.service) {
						state.snapshot.Status = proxyError +
							"; automatic Core start rolled back"
					}
				}
			})
		}
	}
	return nil
}

func (m *tuiModel) beginDangerConfirm(title, message string, key tuiKey) {
	m.dangerConfirmOpen = true
	m.dangerConfirmTitle = title
	m.dangerConfirmMessage = message
	m.dangerConfirmKey = key
	m.snapshot.Status = title + " · Enter confirm · Esc cancel"
}

func (m *tuiModel) handleDangerConfirm(message tea.KeyMsg) tea.Cmd {
	key, ok := tuiKeyFromTea(message)
	if !ok {
		return nil
	}
	switch key {
	case tuiKeyQuit, tuiKeyInterrupt:
		m.dangerConfirmOpen = false
		return m.handleKey(key)
	case tuiKeyBack:
		m.dangerConfirmOpen = false
		m.dangerConfirmTarget = ""
		m.snapshot.Status = "Operation cancelled"
		return nil
	case tuiKeySelect:
		action := m.dangerConfirmKey
		m.dangerConfirmOpen = false
		m.dangerConfirmed = true
		command := m.handleKey(action)
		m.dangerConfirmed = false
		m.dangerConfirmTarget = ""
		return command
	default:
		return nil
	}
}

func (m *tuiModel) dashboardViewportLimit() int {
	return maxTUIWidth(m.dashboardPageHeight()-3, 1)
}

func (m *tuiModel) dashboardPageHeight() int {
	pageHeight := m.height - 4
	if m.width < 88 || m.height < 18 {
		pageHeight = m.height - 2
	}
	return maxTUIWidth(pageHeight, 1)
}

func (m *tuiModel) revealDashboardSelection() {
	rows := tuiCompactDashboardRows(
		m.snapshot,
		m.paths,
		maxTUIWidth(m.width-2, 1),
		m.dashboardPageHeight(),
	)
	selectedRow := -1
	for index, row := range rows {
		if row.selected {
			selectedRow = index
			break
		}
	}
	if selectedRow < 0 {
		return
	}
	limit := m.dashboardViewportLimit()
	switch {
	case selectedRow < m.snapshot.DashboardScroll:
		m.snapshot.DashboardScroll = selectedRow
	case selectedRow >= m.snapshot.DashboardScroll+limit:
		m.snapshot.DashboardScroll = maxTUIIndex(selectedRow - limit + 1)
	}
}

func (m *tuiModel) moveSelection(delta int) tea.Cmd {
	switch m.snapshot.Page {
	case tuiPageSSH:
		if m.snapshot.SSHDashboardFocus {
			if m.snapshot.SelectedSSH == tuiSSHCaptureRow {
				m.snapshot.SSHDashboardFocus = false
				break
			}
			m.snapshot.SelectedSSHDetail = wrapTUIIndex(
				m.snapshot.SelectedSSHDetail,
				delta,
				tuiSSHDashboardRowCount,
			)
		} else {
			previousName := m.selectedSSHName()
			listLen := len(m.snapshot.SSHProfiles) + 1
			position := wrapTUIIndex(m.snapshot.SelectedSSH+1, delta, listLen)
			m.snapshot.SelectedSSH = position - 1
			if m.snapshot.SelectedSSH == tuiSSHCaptureRow {
				m.resetSelectedSSHMetrics()
				m.snapshot.Status = "Enter captures a live ControlMaster without a new SSH login"
				return nil
			}
			if m.selectedSSHName() != previousName {
				m.resetSelectedSSHMetrics()
				return m.refreshSelectedSSHDashboard()
			}
		}
	case tuiPageProfiles:
		moveTUIProfile(&m.snapshot, delta)
		if m.snapshot.SelectedRow == tuiProfileImportSubscriptionRow {
			m.snapshot.Status = "Enter to import a subscription URL"
		} else if m.snapshot.SelectedRow == tuiProfileImportFileRow {
			m.snapshot.Status = "Enter to convert and import a local profile file"
		} else if m.snapshot.SelectedRow < len(m.snapshot.Profiles) {
			profile := m.snapshot.Profiles[m.snapshot.SelectedRow]
			if profile.Current {
				if profile.SubscriptionURL != "" {
					m.snapshot.Status = "Active subscription · U refreshes · e edits YAML"
				} else {
					m.snapshot.Status = "Active local profile · e edits YAML"
				}
			} else {
				if profile.SubscriptionURL != "" {
					m.snapshot.Status = "Enter activates · U refreshes · F2/u renames · e edits"
				} else {
					m.snapshot.Status = "Enter activates · F2/u renames · e edits"
				}
			}
		}
	case tuiPageConnections:
		moveTUIConnectionMatch(&m.snapshot, delta)
	case tuiPageRequests:
		moveTUIRequestMatch(&m.snapshot, delta)
	case tuiPageLogs:
		moveTUILogMatch(&m.snapshot, delta)
	case tuiPageProxies:
		if m.snapshot.ProxyView == tuiProxyViewProviders {
			moveTUIProvider(&m.snapshot, delta)
		} else if m.snapshot.ProxyNodeFocus {
			moveTUINode(&m.snapshot, delta)
		} else {
			moveTUIGroup(&m.snapshot, delta)
		}
	case tuiPageDashboard:
		m.snapshot.SelectedDashboard = wrapTUIIndex(
			m.snapshot.SelectedDashboard,
			delta,
			tuiDashboardRowCount,
		)
		m.revealDashboardSelection()
	case tuiPageTools:
		m.snapshot.SelectedTool = wrapTUIIndex(
			m.snapshot.SelectedTool,
			delta,
			tuiSettingsRowCount,
		)
	case tuiPageMaintenance:
		m.snapshot.SelectedMaintenance = wrapTUIIndex(
			m.snapshot.SelectedMaintenance,
			delta,
			tuiMaintenanceRowCount,
		)
	}
	return nil
}

func (m *tuiModel) stageTUIAdjustedPort(key tuiKey) {
	port := m.snapshot.Settings.MixedPort
	switch key {
	case tuiKeyPortUp:
		if port >= 65535 {
			m.snapshot.Status = "Proxy port is already at 65535"
			return
		}
		port++
	case tuiKeyPortDown:
		if port > 0 {
			port--
		}
	}
	m.pendingMixedPort = &port
	m.snapshot.Settings.MixedPort = port
	m.stagedSettings = cloneTUISettings(&m.snapshot.Settings)
	m.settingsDirty = true
	m.snapshot.Status = fmt.Sprintf(
		"Proxy port %d staged; enable System proxy or start Core to apply",
		port,
	)
	m.persistStagedTUISettings()
}

func (m *tuiModel) stageTUISetting(key tuiKey) {
	switch key {
	case tuiKeyAllowLAN:
		m.snapshot.Settings.AllowLAN = !m.snapshot.Settings.AllowLAN
	case tuiKeyIPv6:
		m.snapshot.Settings.IPv6 = !m.snapshot.Settings.IPv6
	case tuiKeyUnifiedDelay:
		m.snapshot.Settings.UnifiedDelay = !m.snapshot.Settings.UnifiedDelay
	case tuiKeyTCPConcurrent:
		m.snapshot.Settings.TCPConcurrent = !m.snapshot.Settings.TCPConcurrent
	case tuiKeyTun:
		m.snapshot.Settings.TunEnabled = !m.snapshot.Settings.TunEnabled
	case tuiKeyLogLevel:
		levels := []string{"silent", "error", "warning", "info", "debug"}
		current := findTUIString(levels, strings.ToLower(m.snapshot.Settings.LogLevel))
		m.snapshot.Settings.LogLevel = levels[wrapTUIIndex(current, 1, len(levels))]
	default:
		return
	}
	port := m.snapshot.Settings.MixedPort
	m.pendingMixedPort = &port
	m.stagedSettings = cloneTUISettings(&m.snapshot.Settings)
	m.settingsDirty = true
	m.snapshot.Status = "Settings staged; enable System proxy or start Core to apply"
	m.persistStagedTUISettings()
}
