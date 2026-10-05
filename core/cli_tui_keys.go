//go:build linux && !cgo && cli

package main

import (
	"os"
	"strings"
	"time"

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
		m.snapshot.setStatus(newTUIMessage("ui.df28c9362434"))
		return func() tea.Msg {
			return tuiShutdownResultMsg{
				err: completeCLIExitForTUI(os.Getpid()),
			}
		}
	case tuiKeyBack:
		if m.snapshot.Page == tuiPageSSH && m.snapshot.SSHDashboardFocus {
			m.snapshot.SSHDashboardFocus = false
			m.snapshot.setStatus(newTUIMessage("ui.b2cb6c0f5d77"))
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
			m.snapshot.setStatus(newTUIMessage("ui.8d24f11c058c"))
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
				state.snapshot.setStatus(newTUIMessage("ui.0b3a51a19453", reloadErr.Error()))
			} else {
				clearTUISettingsDraft(state)
				state.snapshot.setStatus(newTUIMessage("ui.7b3279fdc1ff"))
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
			m.snapshot.setStatus(newTUIMessage("ui.bd778900ebc6", m.snapshot.HistoryFilter))
		case tuiPageLogs:
			levels := []string{"ALL", "ERROR", "WARN", "INFO", "DEBUG"}
			current := findTUIString(levels, tuiDefaultValue(m.snapshot.LogsLevel, "ALL"))
			m.snapshot.LogsLevel = levels[wrapTUIIndex(current, 1, len(levels))]
			m.snapshot.SelectedLog = firstTUILogMatch(m.snapshot)
			m.snapshot.LogDetailOpen = false
			m.snapshot.setStatus(newTUIMessage("ui.0c8ba04ef125", m.snapshot.LogsLevel))
		}
	case tuiKeySourceFilter:
		if m.snapshot.Page == tuiPageRequests || m.snapshot.Page == tuiPageConnections {
			m.cycleTrafficSource()
		}
	case tuiKeyCloseConnections:
		if m.snapshot.Page == tuiPageSSH {
			if m.snapshot.FocusSidebar || m.snapshot.SSHDashboardFocus {
				m.snapshot.setStatus(newTUIMessage("ui.e834cf942835"))
				return nil
			}
			m.beginSSHDeleteConfirm()
			return nil
		} else if m.snapshot.Page == tuiPageProfiles {
			m.beginProfileDeleteConfirm()
			return nil
		} else if m.snapshot.Page == tuiPageRequests {
			if !m.dangerConfirmed {
				m.beginLocalizedDangerConfirm(newTUIMessage("danger.history.title"), tuiTrafficConfirmMessage(m.snapshot.TrafficSource, "history"), key)
				m.dangerConfirmSource = normalizeTrafficSource(m.snapshot.TrafficSource)
				return nil
			}
			source := m.confirmedTrafficSource()
			if m.service != nil {
				return m.startOperation(func(state *tuiOperationState) {
					if !prepareTUIBackendRevision(state, m.service) {
						return
					}
					status, err := state.service.clearHistoryForSource(source, state.backendRevision)
					if err != nil {
						state.snapshot.setStatus(newTUIMessage("ui.1905760dba41", err.Error()))
						return
					}
					state.backendRevision = status.Revision
					state.snapshot.Requests = append([]tuiRequest(nil), status.History...)
					state.snapshot.SSHHistoryClearedBefore = status.SSHHistoryClearedBefore
					state.snapshot.SelectedRequest = -1
					state.snapshot.HistoryDetailOpen = false
					state.snapshot.setStatus(newTUIMessage("ui.a47de339e760"))
				})
			}
			m.snapshot.Requests = filterRequestsBySource(m.snapshot.Requests, inverseTrafficSource(source))
			if source == tuiTrafficSourceMixed {
				m.snapshot.Requests = nil
			}
			if trafficSourceMatches(source, tuiTrafficSourceSSH) {
				m.snapshot.SSHHistoryClearedBefore = time.Now().UTC()
			}
			m.snapshot.SelectedRequest = -1
			m.snapshot.HistoryDetailOpen = false
			m.snapshot.setStatus(newTUIMessage("ui.183e4443c02f"))
		} else if m.snapshot.Page == tuiPageLogs {
			if !m.dangerConfirmed {
				m.beginLocalizedDangerConfirm(newTUIMessage("danger.logs.title"), newTUIMessage("danger.logs.body"), key)
				return nil
			}
			if m.service != nil {
				return m.startOperation(func(state *tuiOperationState) {
					if !prepareTUIBackendRevision(state, m.service) {
						return
					}
					status, err := state.service.clearLogs(state.backendRevision)
					if err != nil {
						state.snapshot.setStatus(newTUIMessage("ui.a4c472c88838", err.Error()))
						return
					}
					state.backendRevision = status.Revision
					clearTUILogs()
					state.snapshot.Logs = nil
					state.snapshot.BackendLogs = nil
					state.snapshot.LocalLogs = nil
					state.snapshot.LogsInitialized = true
					state.snapshot.SelectedLog = -1
					state.snapshot.LogDetailOpen = false
					state.snapshot.setStatus(newTUIMessage("ui.195a05ddfebb"))
				})
			}
			clearTUILogs()
			m.snapshot.Logs = nil
			m.snapshot.BackendLogs = nil
			m.snapshot.LocalLogs = nil
			m.snapshot.LogsInitialized = true
			m.snapshot.SelectedLog = -1
			m.snapshot.LogDetailOpen = false
			m.snapshot.setStatus(newTUIMessage("ui.0cd1f5ed362f"))
		} else if m.snapshot.Page == tuiPageConnections {
			if !m.dangerConfirmed {
				m.beginLocalizedDangerConfirm(newTUIMessage("danger.connections.title"), tuiTrafficConfirmMessage(m.snapshot.TrafficSource, "connections"), key)
				m.dangerConfirmSource = normalizeTrafficSource(m.snapshot.TrafficSource)
				return nil
			}
			source := m.confirmedTrafficSource()
			return m.startOperation(func(state *tuiOperationState) {
				var err error
				if m.service != nil {
					if !prepareTUIBackendRevision(state, m.service) {
						return
					}
					var status tuiServiceStatus
					status, err = state.service.closeAllConnectionsManagedForSource(source, state.backendRevision)
					if err == nil {
						state.backendRevision = status.Revision
					}
				} else {
					err = closeTUIVisibleConnectionsForSource(m.client, uint32(os.Getuid()), state.snapshot.Settings.TunEnabled && state.snapshot.Settings.TunScope == tuiTunScopeSystem, "", source)
				}
				if err != nil {
					state.snapshot.setStatus(newTUIMessage("ui.a179d29a704a", err.Error()))
				} else {
					state.snapshot.setStatus(newTUIMessage("ui.837ad1848112"))
				}
			})
		}
	case tuiKeyCloseConnection:
		if m.snapshot.Page == tuiPageSSH {
			m.snapshot.setStatus(newTUIMessage("ui.e0d8c381bc7f"))
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
		if m.snapshot.Page == tuiPageConnections && (m.dangerConfirmed && m.dangerConfirmTarget != "" ||
			m.snapshot.SelectedConnection >= 0 && m.snapshot.SelectedConnection < len(m.snapshot.Connections)) {
			if !m.dangerConfirmed {
				connection := m.snapshot.Connections[m.snapshot.SelectedConnection]
				m.beginLocalizedDangerConfirm(newTUIMessage("danger.connection.title"), newTUIMessage("danger.connection.body", cliDisplayValue(connection.Host), connection.ID), key)
				m.dangerConfirmTarget = connection.ID
				m.dangerConfirmSource = normalizeTrafficSource(m.snapshot.TrafficSource)
				return nil
			}
			connectionID := m.dangerConfirmTarget
			source := m.confirmedTrafficSource()
			return m.startOperation(func(state *tuiOperationState) {
				var err error
				if m.service != nil {
					if !prepareTUIBackendRevision(state, m.service) {
						return
					}
					var status tuiServiceStatus
					status, err = state.service.closeConnectionManagedForSource(connectionID, source, state.backendRevision)
					if err == nil {
						state.backendRevision = status.Revision
					}
				} else {
					err = closeTUIVisibleConnectionsForSource(m.client, uint32(os.Getuid()), state.snapshot.Settings.TunEnabled && state.snapshot.Settings.TunScope == tuiTunScopeSystem, connectionID, source)
				}
				if err != nil {
					state.snapshot.setStatus(newTUIMessage("ui.a372309c919a", err.Error()))
				} else {
					state.snapshot.setStatus(newTUIMessage("ui.fdb770cf60c2"))
				}
			})
		}
		if m.snapshot.Page == tuiPageConnections {
			m.snapshot.setStatus(newTUIMessage("ui.e69b5e9624e1"))
		}
	case tuiKeyCoreToggle:
		return m.startOperation(func(state *tuiOperationState) {
			if !m.ownsCore {
				state.snapshot.setStatus(newTUIMessage("ui.cd919234613f"))
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
				m.snapshot.setStatus(newTUIMessage("ui.31adfd67cd2a"))
				return nil
			}
			m.beginSSHForm(true)
			return nil
		case tuiPageProfiles:
			if m.snapshot.SelectedRow < 0 ||
				m.snapshot.SelectedRow >= len(m.snapshot.Profiles) {
				m.snapshot.setStatus(newTUIMessage("ui.828aebbc21b9"))
				return nil
			}
			return m.startEditor(m.snapshot.Profiles[m.snapshot.SelectedRow].Path)
		case tuiPageLogs:
			return m.startOperation(func(state *tuiOperationState) {
				path, err := exportTUILogs(state.paths.HomeDir, state.snapshot.Logs)
				if err != nil {
					state.snapshot.setStatus(newTUIMessage("ui.a1978577d9c8", err.Error()))
				} else {
					state.snapshot.setStatus(newTUIMessage("ui.3c3165db18d2", path))
				}
			})
		case tuiPageMaintenance:
			return m.startEditor(m.paths.ConfigPath)
		default:
			m.snapshot.setStatus(newTUIMessage("ui.49c4e78c047b"))
		}
	case tuiKeyNewProfile:
		if m.snapshot.Page == tuiPageSSH {
			if !m.snapshot.FocusSidebar && m.snapshot.SSHDashboardFocus {
				return m.refreshSelectedSSHProxyIPs()
			}
			if m.snapshot.FocusSidebar {
				m.snapshot.setStatus(newTUIMessage("ui.665b18f29556"))
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
		m.snapshot.setStatus(newTUIMessage("ui.faccaeac76cf"))
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
				m.snapshot.setStatus(newTUIMessage("ui.faccaeac76cf"))
			} else {
				m.snapshot.ProxyNodeFocus = false
				m.snapshot.setStatus(newTUIMessage("ui.5aca75ca712d"))
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
					tuiLayoutAtSize(m.width, m.height, m.snapshot.Language).ContentWidth,
					m.dashboardPageHeight(),
					m.snapshot.Language,
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
				m.snapshot.setStatus(newTUIMessage("ui.4149ad38834a"))
				return nil
			}
			return m.startOperation(func(state *tuiOperationState) {
				enabled := !state.snapshot.Settings.SystemProxy
				if enabled && state.snapshot.Settings.Mode == tuiSilentMode {
					state.snapshot.setStatus(newTUIMessage("ui.0b2f57406e10"))
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
				status, err := state.service.setSystemProxy(
					enabled,
					state.backendRevision,
				)
				proxyUpdated := err == nil
				if err != nil {
					state.snapshot.setStatus(newTUIMessage("ui.3e34a530360a", err.Error()))
				} else {
					applyTUIOperationServiceStatus(state, status)
					state.snapshot.setStatus(newTUIMessage("ui.a6478a4de097", cliOnOff(status.SystemProxy)))
				}
				if proxyUpdated {
					state.systemProxyManaged = state.snapshot.Settings.SystemProxy
					if autoStarted && state.snapshot.Settings.SystemProxy {
						state.snapshot.setStatus(newTUIMessage("ui.648bc55423be", state.snapshot.Settings.MixedPort))

					}
				} else if autoStarted {
					proxyError := state.snapshot.Status
					if stopTUIManagedCore(state, m.service) {
						state.snapshot.setStatus(newTUIMessage("ui.0b82797bddcd", proxyError))

					}
				}
			})
		}
	}
	return nil
}

func (m *tuiModel) beginDangerConfirm(title, message string, key tuiKey) {
	m.dangerConfirmTarget = ""
	m.dangerConfirmSource = ""
	m.dangerConfirmTitleText = tuiMessage{}
	m.dangerConfirmBodyText = tuiMessage{}
	m.dangerConfirmOpen = true
	m.dangerConfirmTitle = title
	m.dangerConfirmMessage = message
	m.dangerConfirmKey = key
	m.snapshot.setStatus(newTUIMessage("ui.725a3903376d", title))
}

func (m *tuiModel) confirmedTrafficSource() string {
	if m.dangerConfirmed && m.dangerConfirmSource != "" {
		return normalizeTrafficSource(m.dangerConfirmSource)
	}
	return normalizeTrafficSource(m.snapshot.TrafficSource)
}

func (m *tuiModel) beginLocalizedDangerConfirm(title, body tuiMessage, key tuiKey) {
	m.beginDangerConfirm(title.text("en"), body.text("en"), key)
	m.dangerConfirmTitleText = title
	m.dangerConfirmBodyText = body
	m.snapshot.setStatus(newTUIMessage("ui.725a3903376d", title))
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
		m.dangerConfirmSource = ""
		m.snapshot.setStatus(newTUIMessage("ui.d98027398ae9"))
		return nil
	case tuiKeySelect:
		action := m.dangerConfirmKey
		m.dangerConfirmOpen = false
		m.dangerConfirmed = true
		command := m.handleKey(action)
		m.dangerConfirmed = false
		m.dangerConfirmTarget = ""
		m.dangerConfirmSource = ""
		return command
	default:
		return nil
	}
}

func (m *tuiModel) dashboardViewportLimit() int {
	return maxTUIWidth(m.dashboardPageHeight()-3, 1)
}

func (m *tuiModel) dashboardPageHeight() int {
	return tuiLayoutAtSize(m.width, m.height, m.snapshot.Language).PageHeight
}

func (m *tuiModel) revealDashboardSelection() {
	rows := tuiCompactDashboardRows(
		m.snapshot,
		m.paths,
		tuiLayoutAtSize(m.width, m.height, m.snapshot.Language).ContentWidth,
		m.dashboardPageHeight(),
		m.snapshot.Language,
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
			break
		} else {
			listLen := len(m.snapshot.SSHProfiles) + 1
			position := wrapTUIIndex(m.snapshot.SelectedSSH+1, delta, listLen)
			m.snapshot.SelectedSSH = position - 1
			if m.snapshot.SelectedSSH == tuiSSHCaptureRow {
				m.snapshot.setStatus(newTUIMessage("ui.f4ae67d05d76"))
			} else {
				m.snapshot.setStatus(newTUIMessage("ui.0c44df2190bd", m.selectedSSHName()))
			}
		}
	case tuiPageProfiles:
		moveTUIProfile(&m.snapshot, delta)
		if m.snapshot.SelectedRow == tuiProfileImportSubscriptionRow {
			m.snapshot.setStatus(newTUIMessage("ui.5564989ba8cc"))
		} else if m.snapshot.SelectedRow == tuiProfileImportFileRow {
			m.snapshot.setStatus(newTUIMessage("ui.d42c0a34419b"))
		} else if m.snapshot.SelectedRow < len(m.snapshot.Profiles) {
			profile := m.snapshot.Profiles[m.snapshot.SelectedRow]
			if profile.Current {
				if profile.SubscriptionURL != "" {
					m.snapshot.setStatus(newTUIMessage("ui.50692330c61b"))
				} else {
					m.snapshot.setStatus(newTUIMessage("ui.8aa2ec470969"))
				}
			} else {
				if profile.SubscriptionURL != "" {
					m.snapshot.setStatus(newTUIMessage("ui.db93c9e4cf26"))
				} else {
					m.snapshot.setStatus(newTUIMessage("ui.a1f28af8ebfd"))
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
			m.snapshot.setStatus(newTUIMessage("ui.191046cd2be2"))
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
	m.snapshot.setStatus(newTUIMessage("ui.0301590625fb", port))

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
	m.snapshot.setStatus(newTUIMessage("ui.807224a9beca"))
	m.persistStagedTUISettings()
}
