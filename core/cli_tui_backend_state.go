//go:build linux && !cgo && cli

package main

import tea "github.com/charmbracelet/bubbletea"

type tuiBackendHandshakeMsg struct {
	generation uint64
	status     tuiServiceStatus
	err        error
}

func (m *tuiModel) backendStatusCurrent(status tuiServiceStatus) bool {
	return (m.backendInstanceID == "" || status.InstanceID == m.backendInstanceID) &&
		status.Revision >= m.backendRevision
}

func (m *tuiModel) applyBackendStatus(status tuiServiceStatus) {
	m.backendInstanceID = status.InstanceID
	m.backendRevision = status.Revision
	m.coreRunning = status.Running
	if status.ConfigPath != "" {
		m.paths.ConfigPath = status.ConfigPath
	}
	if status.HomeDir != "" {
		m.paths.HomeDir = status.HomeDir
	}
	applyTUIBackendDisplay(&m.snapshot, status)
	if !m.settingsDirty && m.stagedSettings != nil {
		m.stagedSettings.MixedPort = status.ConfiguredProxyPort
		port := status.ConfiguredProxyPort
		m.pendingMixedPort = &port
	}
	m.preserveSettingsDraft()
	m.reconcileStoppedCoreState()
	m.syncNetworkExit()
}

func applyTUIBackendDisplay(snapshot *tuiSnapshot, status tuiServiceStatus) {
	snapshot.Settings.SystemProxy = status.SystemProxy
	if status.Mode != "" {
		snapshot.Settings.Mode = status.Mode
	}
	snapshot.Settings.MixedPort = status.ConfiguredProxyPort
	snapshot.ConfiguredProxyPort = status.ConfiguredProxyPort
	snapshot.ActiveProxyPort = status.ActiveProxyPort
	if status.TunState != "" {
		snapshot.Settings.TunEnabled = status.TunState == "on" && status.Mode != tuiSilentMode
	}
	if status.TunScope != "" {
		snapshot.Settings.TunScope = status.TunScope
	}
	if status.Mode == tuiSilentMode {
		snapshot.Settings.TunEnabled = false
	}
	snapshot.FLCEnabled = status.FLCEnabled
	snapshot.FLCOutbound = status.FLCOutbound
	snapshot.SSHHistoryClearedBefore = status.SSHHistoryClearedBefore
	snapshot.HistoryCount = cloneTUIOptionalInt(status.HistoryCount)
	snapshot.TunRequested = cloneTUIOptionalBool(status.TunRequested)
}

func (m *tuiModel) startBackendHandshake() tea.Cmd {
	if m.service == nil || m.backendHandshakeActive {
		return nil
	}
	m.backendHandshakeActive = true
	service := m.service
	generation := m.backendGeneration
	return func() tea.Msg {
		status, err := service.status()
		return tuiBackendHandshakeMsg{generation: generation, status: status, err: err}
	}
}
