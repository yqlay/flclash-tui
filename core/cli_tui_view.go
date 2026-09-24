//go:build linux && !cgo && cli

package main

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *tuiModel) handleTeaKey(message tea.KeyMsg) tea.Cmd {
	key, ok := tuiKeyFromTea(message)
	if !ok {
		return nil
	}
	if message.String() == "v" &&
		(m.snapshot.Page == tuiPageDashboard ||
			m.snapshot.Page == tuiPageProxies ||
			m.snapshot.Page == tuiPageSSH && m.snapshot.SSHDashboardFocus) {
		key = tuiKeySpeedTest
	}
	if key == tuiKeyNotifications {
		m.toggleNotificationDetails()
		return nil
	}
	if m.notificationDetailOpen &&
		key != tuiKeyQuit &&
		key != tuiKeyInterrupt {
		m.handleNotificationDetailKey(key)
		return nil
	}
	if m.snapshot.ShowHelp &&
		key != tuiKeyHelp &&
		key != tuiKeyQuit &&
		key != tuiKeyInterrupt {
		m.snapshot.ShowHelp = false
		return nil
	}
	previousPage := m.snapshot.Page
	if handleTUIFocusNavigation(&m.snapshot, key) {
		var cmds []tea.Cmd
		if previousPage != tuiPageSSH && m.snapshot.Page == tuiPageSSH {
			m.resetSelectedSSHMetrics()
			cmds = append(cmds, m.refreshSelectedSSHDashboard())
		}
		if previousPage != m.snapshot.Page {
			m.refreshInFlight = false
			cmds = append(cmds, m.syncLiveMonitors()...)
			cmds = append(cmds, m.startRefresh())
		}
		return tea.Batch(cmds...)
	}
	if m.busy && !tuiKeyAllowedWhileBusy(key) {
		m.snapshot.Status = "Operation in progress; navigation remains available"
		return nil
	}
	return m.handleKey(key)
}

func tuiKeyAllowedWhileBusy(key tuiKey) bool {
	switch key {
	case tuiKeyQuit,
		tuiKeyInterrupt,
		tuiKeyNotifications,
		tuiKeyHelp,
		tuiKeyBack,
		tuiKeyUp,
		tuiKeyDown,
		tuiKeyLeft,
		tuiKeyRight,
		tuiKeyViewPrevious,
		tuiKeyViewNext,
		tuiKeyPageUp,
		tuiKeyPageDown:
		return true
	default:
		return false
	}
}

func (m *tuiModel) View() string {
	snapshot := m.snapshot
	snapshot.DangerConfirmOpen = m.dangerConfirmOpen
	snapshot.DangerConfirmTitle = m.dangerConfirmTitle
	snapshot.DangerConfirmMessage = m.dangerConfirmMessage
	snapshot.SSHForm = m.sshFormView()
	snapshot.SSHCredentialPrompt = tuiSSHCredentialPromptView{
		Open:     m.sshCredentialPromptOpen,
		Profile:  m.sshCredentialProfile,
		Identity: m.sshCredentialIdentity,
		Value:    strings.Repeat("•", len(m.sshCredentialInput)),
	}
	snapshot.ProfileDelete = tuiProfileDeleteView{
		Open: m.profileDeleteOpen,
		Name: m.profileDeleteName,
		Kind: m.profileDeleteKind,
	}
	snapshot.Notifications = append(
		[]tuiNotification(nil),
		m.notifications...,
	)
	snapshot.NotificationDetailOpen = m.notificationDetailOpen
	snapshot.NotificationSelected = m.notificationSelected
	snapshot.NotificationScroll = m.notificationScroll
	if m.notificationDetailOpen {
		snapshot.Status = "Notifications · ↑↓ select · PgUp/PgDn scroll · Enter confirm · Esc close"
	} else if m.sshCaptureOpen {
		snapshot.SelectionTitle = "Capture existing SSH"
		snapshot.SelectionOptions = append([]string(nil), m.sshCaptureOptions...)
		snapshot.SelectedOption = m.sshCaptureSelected
		snapshot.SelectionHint = "Reuses a live ssh client on this machine (ControlMaster or ssh -D), or inbound ssh -R reverse SOCKS. Sessions on another host without -R are invisible — connect a profile instead."
		snapshot.Status = "Capture existing SSH · ↑↓/ws choose · Enter attach · Esc cancel"
	} else if m.modeSelectionOpen {
		snapshot.SelectionTitle = "Select outbound mode"
		snapshot.SelectionOptions = append(
			[]string(nil),
			tuiTrafficModes...,
		)
		snapshot.SelectedOption = m.selectedMode
		snapshot.SelectionHint = "rule uses routing rules · silent proxies only flc commands · global proxies all traffic · direct bypasses proxies"
		snapshot.Status = "Selecting mode · ↑↓/ws choose · Enter confirm · Esc cancel"
	} else if m.inputMode != tuiInputNone {
		cursor := m.inputCursor
		if cursor < 0 {
			cursor = 0
		}
		if cursor > len(m.inputValue) {
			cursor = len(m.inputValue)
		}
		snapshot.InputTitle, snapshot.InputHint = m.inputPresentation()
		snapshot.InputValue = tuiInputViewport(
			m.inputValue,
			cursor,
			maxTUIWidth(m.width-36, 20),
		)
		snapshot.Status = "Editing input · Enter confirm · Esc cancel"
	}
	return renderTUIAtSize(
		snapshot,
		m.paths,
		m.client.displayAddress(),
		m.ownsCore,
		m.coreRunning,
		m.width,
		m.height,
	)
}

func (m *tuiModel) sshFormView() tuiSSHFormView {
	profile := normalizeCLISSHProfile(m.sshForm)
	view := tuiSSHFormView{
		Open:              m.sshFormOpen,
		Existing:          m.sshFormExisting,
		ReadOnly:          m.sshFormReadOnly,
		Name:              profile.Name,
		Username:          profile.Username,
		Host:              profile.Host,
		Destination:       profile.Destination,
		Jump:              profile.Jump,
		Port:              m.sshForm.Port,
		LocalPort:         m.sshForm.LocalPort,
		Identity:          m.sshForm.Identity,
		IdentityKind:      m.sshFormIdentityKind,
		IdentityError:     m.sshFormIdentityError,
		PassphraseSet:     m.sshForm.IdentityPassphrase != "",
		PassphraseChanged: m.sshFormPassphraseChanged,
		PassphraseCleared: m.sshFormPassphraseCleared,
		PasswordSet:       m.sshForm.Password != "",
		PasswordChanged:   m.sshFormPasswordChanged,
		PasswordCleared:   m.sshFormPasswordCleared,
		Options:           append([]string(nil), m.sshForm.Options...),
		Selected:          m.sshFormSelected,
		FieldEditing:      m.sshFormFieldEditing,
		PassphraseConfirm: m.sshFormPassphraseConfirm,
		PasswordConfirm:   m.sshFormPasswordConfirm,
		DeleteConfirmOpen: m.sshDeleteConfirmOpen,
		DeleteName:        m.sshDeleteName,
	}
	if m.sshFormFieldEditing {
		value := m.sshFormInput
		if m.sshFormSelected == tuiSSHFormPassphraseRow ||
			m.sshFormSelected == tuiSSHFormPasswordRow {
			value = []rune(strings.Repeat("•", len(m.sshFormInput)))
		}
		view.FieldInput = tuiInputViewport(
			value,
			m.sshFormCursor,
			maxTUIWidth(m.width-48, 16),
		)
	}
	return view
}

func (m *tuiModel) inputPresentation() (string, string) {
	switch m.inputMode {
	case tuiInputMixedPort:
		return "Set proxy port", "Type 0-65535; silent mode keeps it closed"
	case tuiInputSubscription:
		return "Import subscription", "Paste a YAML, URI, Base64, JSON, or client-format URL"
	case tuiInputProfileFile:
		return "Import local profile", "Type a YAML, URI, Base64, JSON, or client-format file path"
	case tuiInputProfileName:
		return "Rename profile", "Type a file name; .yaml is added automatically"
	case tuiInputHistorySearch:
		return "Search History", "Match host, process, network, route, or connection ID"
	case tuiInputConnectionsSearch:
		return "Search Connections", "Match host, process, network, route, or connection ID"
	case tuiInputLogsSearch:
		return "Search Logs", "Case-insensitive text search; empty clears the search"
	default:
		return "Input", ""
	}
}

func tuiInputViewport(value []rune, cursor, width int) string {
	if width <= 0 {
		return ""
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(value) {
		cursor = len(value)
	}
	start := cursor
	used := 1
	for start > 0 {
		runeWidth := tuiRuneWidth(value[start-1])
		reservedPrefix := 0
		if start-1 > 0 {
			reservedPrefix = 1
		}
		if used+runeWidth+reservedPrefix > width {
			break
		}
		start--
		used += runeWidth
	}
	used = 1 + tuiDisplayWidth(string(value[start:cursor]))
	if start > 0 {
		used++
	}
	end := cursor
	for end < len(value) {
		runeWidth := tuiRuneWidth(value[end])
		reservedSuffix := 0
		if end+1 < len(value) {
			reservedSuffix = 1
		}
		if used+runeWidth+reservedSuffix > width {
			break
		}
		end++
		used += runeWidth
	}
	var output strings.Builder
	if start > 0 {
		output.WriteRune('…')
	}
	output.WriteString(string(value[start:cursor]))
	output.WriteRune('█')
	output.WriteString(string(value[cursor:end]))
	if end < len(value) {
		output.WriteRune('…')
	}
	return output.String()
}
