//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"strings"
)

// View preferences identify rows by profile name, independently of SSH runtime
// state. Restoring them never starts a tunnel or serializes credentials/IPs.
type tuiSSHViewState struct {
	SelectedProfile string `json:"selected_profile,omitempty"`
	OpenedProfile   string `json:"opened_profile,omitempty"`
	DetailFocus     bool   `json:"detail_focus,omitempty"`
	DetailRow       int    `json:"detail_row,omitempty"`
}

func (m *tuiModel) rememberSSHContentFocus() {
	if m.pendingSSHView == nil && m.snapshot.Page == tuiPageSSH && !m.snapshot.FocusSidebar {
		m.sshViewDetailFocus = m.snapshot.SSHDashboardFocus
	}
}

func (m *tuiModel) restoreSSHContentFocus() {
	if m.pendingSSHView == nil {
		m.snapshot.SSHDashboardFocus = m.sshViewDetailFocus && m.sshDetailProfileIndex() >= 0
	}
}

func (m *tuiModel) currentSSHView() tuiSSHViewState {
	// A page change before the first local refresh must not erase the saved
	// identity merely because startup has not loaded the profile list yet.
	if m.pendingSSHView != nil {
		return *m.pendingSSHView
	}
	focus := m.sshViewDetailFocus
	if m.snapshot.Page == tuiPageSSH && !m.snapshot.FocusSidebar {
		focus = m.snapshot.SSHDashboardFocus
	}
	view := tuiSSHViewState{
		SelectedProfile: m.selectedSSHName(),
		OpenedProfile:   m.snapshot.SSHDetailName,
		DetailFocus:     focus,
		DetailRow:       m.snapshot.SSHDetailRow,
	}
	if view.OpenedProfile == "" {
		view.DetailFocus = false
		view.DetailRow = tuiSSHTunnelRow
	}
	return view
}

func (m *tuiModel) restorePendingSSHView() {
	if m.pendingSSHView == nil || m.snapshot.SSHProfiles == nil {
		return
	}
	view := *m.pendingSSHView
	m.pendingSSHView = nil
	m.snapshot.SelectedSSH = findTUISSHProfile(m.snapshot.SSHProfiles, view.SelectedProfile)
	m.snapshot.SSHDetailName = ""
	m.snapshot.SSHDashboardFocus = false
	m.snapshot.SSHDetailRow = tuiSSHTunnelRow
	if index := findTUISSHProfile(m.snapshot.SSHProfiles, view.OpenedProfile); index >= 0 {
		m.snapshot.SSHDetailName = m.snapshot.SSHProfiles[index].Name
		m.snapshot.SSHDashboardFocus = view.DetailFocus
		if view.DetailRow >= 0 && view.DetailRow < tuiSSHDetailSelectableCount(m.snapshot.SSHProfiles[index]) {
			m.snapshot.SSHDetailRow = view.DetailRow
		}
	}
	m.sshViewDetailFocus = m.snapshot.SSHDashboardFocus
}

func (s *tuiPageSaver) saveView(homeDir, page string, view *tuiSSHViewState, seq uint64) error {
	// A frontend that did not change its SSH view only owns the page key.
	// Preserve a newer SSH view saved by another frontend in that case.
	if view == nil {
		return s.save(homeDir, page, seq)
	}
	if strings.TrimSpace(homeDir) == "" {
		return nil
	}
	if _, ok := tuiPageFromPreference(page); !ok {
		return errors.New("unsupported TUI page")
	}
	pageJSON, err := json.Marshal(page)
	if err != nil {
		return err
	}
	viewJSON, err := json.Marshal(view)
	if err != nil {
		return err
	}
	return writeTUIPreferenceKeys(homeDir, map[string]json.RawMessage{
		"page": pageJSON, "ssh_view": viewJSON,
	}, func() bool { return s.current() == seq })
}
