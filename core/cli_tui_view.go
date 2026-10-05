//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *tuiModel) handleTeaKey(message tea.KeyMsg) tea.Cmd {
	if m.languageSelectionOpen {
		return m.handleLanguageSelection(message)
	}
	key, ok := tuiKeyFromTea(message)
	if !ok {
		return nil
	}
	if message.String() == "v" &&
		(m.snapshot.Page == tuiPageDashboard ||
			m.snapshot.Page == tuiPageProxies) {
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
		if previousPage != m.snapshot.Page {
			if previousPage == tuiPageSSH || m.snapshot.Page == tuiPageSSH {
				m.snapshot.SSHDetailName = ""
				m.snapshot.SSHDashboardFocus = false
				m.resetSelectedSSHMetrics()
			}
			m.refreshInFlight = false
			cmds = append(cmds, m.syncLiveMonitors()...)
			cmds = append(cmds, m.startRefresh())
		}
		return tea.Batch(cmds...)
	}
	if key == tuiKeySelect && m.snapshot.Page == tuiPageTools && !m.snapshot.FocusSidebar && m.snapshot.SelectedTool == tuiSettingsLanguageRow {
		m.beginLanguageSelection()
		return nil
	}
	if m.busy && !tuiKeyAllowedWhileBusy(key) {
		m.snapshot.setStatus(newTUIMessage("ui.03e6bb796eac"))
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
	tr := tuiTranslator(m.snapshot.Language)
	snapshot := m.snapshot
	snapshot.LanguageSelectionOpen = m.languageSelectionOpen
	snapshot.SelectedLanguage = m.selectedLanguage
	snapshot.DangerConfirmOpen = m.dangerConfirmOpen
	snapshot.DangerConfirmTitle = m.dangerConfirmTitle
	snapshot.DangerConfirmMessage = m.dangerConfirmMessage
	if m.dangerConfirmTitleText.Key != "" {
		snapshot.DangerConfirmTitle = m.dangerConfirmTitleText.text(snapshot.Language)
	}
	if m.dangerConfirmBodyText.Key != "" {
		snapshot.DangerConfirmMessage = m.dangerConfirmBodyText.text(snapshot.Language)
	}
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
		snapshot.Status = tr("ui.7cb3e7ed7ef0")
	} else if m.sshCaptureOpen {
		snapshot.SelectionTitle = fmt.Sprintf(tr("ui.f4d5ed40e313"), len(m.sshCaptureCandidates))
		if m.sshCaptureNames == nil {
			snapshot.SelectionTitle = tr("ui.1900b478a586")
		}
		snapshot.SelectionOptions = append([]string(nil), m.sshCaptureOptions...)
		snapshot.SelectedOption = m.sshCaptureSelected
		snapshot.SelectionHint = ""
		snapshot.Status = tr("ui.54346e32fec8")
	} else if m.modeSelectionOpen {
		snapshot.SelectionTitle = tr("ui.d3f86ef363b3")
		snapshot.SelectionOptions = append(
			[]string(nil),
			tuiTrafficModes...,
		)
		snapshot.SelectedOption = m.selectedMode
		snapshot.SelectionHint = tr("ui.35294cbec8a7")
		snapshot.Status = tr("ui.304481cef0bc")
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
			maxTUIWidth(tuiLayoutAtSize(m.width, m.height, snapshot.Language).ContentWidth-4, 1),
		)
		snapshot.Status = tr("ui.93388fbb005d")
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
		// Measure the same labels that the form renderer uses. A placeholder
		// keeps an empty editing field in the label/value column calculation.
		view.FieldInput = " "
		budget := tuiFieldInputWidth(tuiSSHFormFields(view, m.snapshot.Language), tuiLayoutAtSize(m.width, m.height, m.snapshot.Language).ContentWidth)
		view.FieldInput = tuiInputViewport(
			value,
			m.sshFormCursor,
			budget,
		)
	}
	return view
}

func (m *tuiModel) inputPresentation() (string, string) {
	tr := tuiTranslator(m.snapshot.Language)
	switch m.inputMode {
	case tuiInputMixedPort:
		return tr("ui.0ecd20bdfce8"), tr("ui.b14cdbf61b08")
	case tuiInputSubscription:
		return tr("ui.59f37e6883b8"), tr("ui.9d6a7e5ff358")
	case tuiInputProfileFile:
		return tr("ui.15445e4510f5"), tr("ui.4e378981f377")
	case tuiInputProfileName:
		return tr("ui.c9aaa25628eb"), tr("profiles.rename_hint")
	case tuiInputHistorySearch:
		return tr("ui.66d7e4a1c738"), tr("ui.0adcb8727d46")
	case tuiInputConnectionsSearch:
		return tr("ui.902ce24cde98"), tr("ui.0adcb8727d46")
	case tuiInputLogsSearch:
		return tr("ui.a12157d01bea"), tr("ui.20a730d8e192")
	default:
		return tr("ui.36ecb4f86691"), ""
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
	boundaries := tuiGraphemeBoundaries(value)
	cursorIndex := sort.SearchInts(boundaries, cursor)
	cursor = boundaries[cursorIndex]
	start := cursor
	startIndex := cursorIndex
	used := 1
	for startIndex > 0 {
		previous := boundaries[startIndex-1]
		runeWidth := tuiDisplayWidth(string(value[previous:start]))
		reservedPrefix := 0
		if previous > 0 {
			reservedPrefix = 1
		}
		if used+runeWidth+reservedPrefix > width {
			break
		}
		start = previous
		startIndex--
		used += runeWidth
	}
	used = 1 + tuiDisplayWidth(string(value[start:cursor]))
	if start > 0 {
		used++
	}
	end := cursor
	endIndex := cursorIndex
	for endIndex+1 < len(boundaries) {
		next := boundaries[endIndex+1]
		runeWidth := tuiDisplayWidth(string(value[end:next]))
		reservedSuffix := 0
		if next < len(value) {
			reservedSuffix = 1
		}
		if used+runeWidth+reservedSuffix > width {
			break
		}
		end = next
		endIndex++
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
	return truncateTUI(output.String(), width)
}
