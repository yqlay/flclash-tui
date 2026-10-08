//go:build linux && !cgo && cli

package main

import (
	tea "github.com/charmbracelet/bubbletea"
	"strings"
)

type tuiSSHLocalRefreshMsg struct {
	sequence uint64
	profiles []tuiSSHProfile
	status   tuiMessage
}

// SSH idle work only reads saved profiles and local tunnel/relay health. It
// must never discover processes, probe an exit IP, or query the Mihomo API.
func (m *tuiModel) startSSHLocalRefresh() tea.Cmd {
	if m.sshLocalRefreshActive || m.busy || m.snapshot.Page != tuiPageSSH {
		return nil
	}
	m.sshLocalRefreshActive = true
	m.sshLocalRefreshSequence++
	sequence := m.sshLocalRefreshSequence
	snapshot := cloneTUISnapshot(m.snapshot)
	return func() tea.Msg {
		previous := snapshot.Status
		refreshTUISSH(&snapshot)
		result := tuiSSHLocalRefreshMsg{sequence: sequence, profiles: snapshot.SSHProfiles}
		if previous != snapshot.Status {
			result.status = snapshot.currentMessage()
		}
		return result
	}
}

func (m *tuiModel) applySSHLocalRefresh(message tuiSSHLocalRefreshMsg) tea.Cmd {
	if message.sequence != m.sshLocalRefreshSequence {
		return nil
	}
	m.sshLocalRefreshActive = false
	if m.busy {
		return nil
	}
	previousIndex := m.sshDetailProfileIndex()
	var previous tuiSSHProfile
	if previousIndex >= 0 {
		previous = m.snapshot.SSHProfiles[previousIndex]
	}
	updated := cloneTUISnapshot(m.snapshot)
	updated.SSHProfiles = message.profiles
	m.snapshot = preserveTUIInteraction(m.snapshot, updated)
	if message.status.Key != "" || message.status.Raw != "" {
		m.snapshot.setStatus(message.status)
	}
	index := m.sshDetailProfileIndex()
	changed := previousIndex >= 0 && index < 0
	if previousIndex >= 0 && index >= 0 {
		current := m.snapshot.SSHProfiles[index]
		changed = !strings.EqualFold(previous.Name, current.Name) || previous.Connected != current.Connected || previous.Ready != current.Ready || previous.SocksPort != current.SocksPort || !previous.StartedAt.Equal(current.StartedAt)
	}
	if changed {
		m.resetSelectedSSHMetrics()
		return m.refreshSelectedSSHDashboard()
	}
	return nil
}
