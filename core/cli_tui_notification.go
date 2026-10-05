//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type tuiNotificationLevel string

const (
	tuiNotificationInfo    tuiNotificationLevel = "INFO"
	tuiNotificationSuccess tuiNotificationLevel = "SUCCESS"
	tuiNotificationWarning tuiNotificationLevel = "WARNING"
	tuiNotificationError   tuiNotificationLevel = "ERROR"
)

type tuiNotification struct {
	id           string
	level        tuiNotificationLevel
	title        string
	message      string
	text         tuiMessage
	progress     bool
	updatedAt    time.Time
	acknowledged bool
}

const tuiNotificationHistoryLimit = 50

func (m *tuiModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	previousStatus := m.snapshot.Status
	model, command := m.update(message)
	m.publishStatusNotification(message, previousStatus)
	return model, command
}

func (m *tuiModel) publishStatusNotification(message tea.Msg, previousStatus string) {
	status := strings.TrimSpace(m.snapshot.Status)
	text := m.snapshot.currentMessage()
	if status == "" || text.Info.Kind == "hint" {
		return
	}
	if status == previousStatus {
		return
	}
	if keyMessage, ok := message.(tea.KeyMsg); ok && tuiNotificationNavigationKey(keyMessage) {
		return
	}
	level := tuiNotificationLevel(text.Info.Level)
	if level == "" {
		level = tuiNotificationInfo
	}
	progress := m.busy || text.Info.Progress
	operationID := ""
	if progress || m.hasProgressNotification() {
		operationID = "operation"
	}
	m.enqueueNotification(tuiNotification{
		id:       operationID,
		level:    level,
		title:    tuiNotificationTitle(level, progress),
		message:  status,
		text:     text,
		progress: progress,
	})
}

func (m *tuiModel) hasProgressNotification() bool {
	for _, notification := range m.notifications {
		if notification.progress {
			return true
		}
	}
	return false
}

func tuiNotificationNavigationKey(message tea.KeyMsg) bool {
	key, ok := tuiKeyFromTea(message)
	if !ok {
		return true
	}
	switch key {
	case tuiKeyUp,
		tuiKeyDown,
		tuiKeyLeft,
		tuiKeyRight,
		tuiKeyFocusNext,
		tuiKeyFocusPrevious,
		tuiKeyViewPrevious,
		tuiKeyViewNext,
		tuiKeyPageUp,
		tuiKeyPageDown,
		tuiKeyDashboard,
		tuiKeyProxies,
		tuiKeyProfiles,
		tuiKeyRequests,
		tuiKeyConnections,
		tuiKeyLogs,
		tuiKeySettings,
		tuiKeyTools,
		tuiKeyMaintenance:
		return true
	default:
		return false
	}
}

func tuiNotificationTitle(level tuiNotificationLevel, progress bool) string {
	if progress {
		return "Operation in progress"
	}
	switch level {
	case tuiNotificationSuccess:
		return "Operation complete"
	case tuiNotificationWarning:
		return "Attention"
	case tuiNotificationError:
		return "Operation failed"
	default:
		return "Notification"
	}
}

func (m *tuiModel) enqueueNotification(notification tuiNotification) {
	notification.message = strings.TrimSpace(notification.message)
	if notification.message == "" {
		return
	}
	if notification.title == "" {
		notification.title = tuiNotificationTitle(
			notification.level,
			notification.progress,
		)
	}
	if notification.updatedAt.IsZero() {
		notification.updatedAt = time.Now()
	}
	notification.acknowledged = false
	logMessage := sanitizeCLIApplicationLogDetail(
		m.paths.HomeDir,
		notification.message,
	)
	appendTUILogEvent(string(notification.level), logMessage)
	mergeTUILogBuffers(&m.snapshot, cliLogSnapshot())
	match := -1
	for index := range m.notifications {
		current := m.notifications[index]
		if current.message == notification.message {
			match = index
			break
		}
		if notification.id != "" &&
			(current.id == notification.id ||
				(notification.id == "operation" && current.progress)) {
			match = index
			break
		}
	}
	if match >= 0 {
		replacedSelected := m.notificationDetailOpen &&
			match == m.notificationSelected
		m.removeNotificationAt(match)
		if replacedSelected {
			m.notificationSelected = -1
		} else if m.notificationDetailOpen {
			m.notificationSelected++
		}
	} else if m.notificationDetailOpen && len(m.notifications) > 0 {
		m.notificationSelected++
	}
	if notification.id == "operation" && !notification.progress {
		notification.id = ""
	}
	m.notifications = append(
		[]tuiNotification{notification},
		m.notifications...,
	)
	if m.notificationSelected < 0 || !m.notificationDetailOpen {
		m.notificationSelected = 0
	}
	if len(m.notifications) > tuiNotificationHistoryLimit {
		m.notifications = m.notifications[:tuiNotificationHistoryLimit]
	}
	if m.notificationSelected >= len(m.notifications) {
		m.notificationSelected = len(m.notifications) - 1
	}
	if !m.notificationDetailOpen || m.notificationSelected == 0 {
		m.notificationScroll = 0
	}
}

func (m *tuiModel) removeNotificationAt(index int) {
	if index < 0 || index >= len(m.notifications) {
		return
	}
	m.notifications = append(
		m.notifications[:index],
		m.notifications[index+1:]...,
	)
	if !m.notificationDetailOpen || index == m.notificationSelected {
		return
	}
	if index < m.notificationSelected {
		m.notificationSelected--
	}
}

func (m *tuiModel) toggleNotificationDetails() {
	if m.notificationDetailOpen {
		m.notificationDetailOpen = false
		m.notificationScroll = 0
		return
	}
	m.notificationDetailOpen = true
	m.notificationSelected = 0
	for index, notification := range m.notifications {
		if !notification.acknowledged {
			m.notificationSelected = index
			break
		}
	}
	m.notificationScroll = 0
}

func (m *tuiModel) handleNotificationDetailKey(key tuiKey) {
	switch key {
	case tuiKeyBack:
		m.notificationDetailOpen = false
		m.notificationScroll = 0
	case tuiKeyUp:
		m.notificationSelected = maxTUIWidth(m.notificationSelected-1, 0)
		m.notificationScroll = 0
	case tuiKeyDown:
		m.notificationSelected = minTUI(
			m.notificationSelected+1,
			maxTUIWidth(len(m.notifications)-1, 0),
		)
		m.notificationScroll = 0
	case tuiKeyPageUp:
		m.notificationScroll = maxTUIWidth(
			m.notificationScroll-m.notificationDetailPageSize(),
			0,
		)
	case tuiKeyPageDown:
		m.notificationScroll = minTUI(
			m.notificationScroll+m.notificationDetailPageSize(),
			m.notificationScrollLimit(),
		)
	case tuiKeySelect:
		if m.notificationSelected >= 0 &&
			m.notificationSelected < len(m.notifications) {
			m.notifications[m.notificationSelected].acknowledged = true
		}
	}
}

func (m *tuiModel) notificationScrollLimit() int {
	if len(m.notifications) == 0 ||
		m.notificationSelected < 0 ||
		m.notificationSelected >= len(m.notifications) {
		return 0
	}
	width, height := m.notificationDetailContentSize()
	_, visible := tuiNotificationDetailRows(height)
	lines := tuiNotificationLines(
		m.notifications[m.notificationSelected].displayMessage(m.snapshot.Language),
		maxTUIWidth(width-4, 1),
	)
	return maxTUIWidth(len(lines)-visible, 0)
}

func (m *tuiModel) notificationDetailPageSize() int {
	_, height := m.notificationDetailContentSize()
	_, bodyRows := tuiNotificationDetailRows(height)
	return maxTUIWidth(bodyRows, 1)
}

func (m *tuiModel) notificationDetailContentSize() (int, int) {
	layout := tuiLayoutAtSize(m.width, m.height, m.snapshot.Language)
	return layout.ContentWidth, layout.PageHeight
}

func tuiNotificationLines(message string, width int) []string {
	paragraphs := strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		lines = append(lines, tuiWrapText(paragraph, width)...)
	}
	return lines
}

func tuiNotificationLevelColor(level tuiNotificationLevel) string {
	switch level {
	case tuiNotificationSuccess:
		return tuiGreen
	case tuiNotificationWarning:
		return tuiYellow
	case tuiNotificationError:
		return tuiRed
	default:
		return tuiCyan
	}
}

func tuiLatestUnreadNotification(
	notifications []tuiNotification,
) (tuiNotification, bool) {
	for _, notification := range notifications {
		if !notification.acknowledged {
			return notification, true
		}
	}
	return tuiNotification{}, false
}

func tuiNotificationSummary(message string) string {
	return strings.Join(strings.Fields(message), " ")
}

func tuiNotificationFooter(snapshot tuiSnapshot, left string, width int, language ...string) string {
	tr := tuiTranslator(language...)
	notification, ok := tuiLatestUnreadNotification(snapshot.Notifications)
	if !ok || width < 30 {
		return tuiClampAnsiLine(tuiShortFooter(left, width), width)
	}
	rightWidth := minTUI(maxTUIWidth(width/2, 30), minTUI(width-2, 58))
	level := string(notification.level)
	hint := tr("ui.c9ca62ce1bf8")
	if tuiDisplayWidth(level)+tuiDisplayWidth(hint)+7 > width-1 {
		hint = "Ctrl+N"
	}
	fixedWidth := tuiDisplayWidth(level) + tuiDisplayWidth(hint) + 6
	rightWidth = maxTUIWidth(rightWidth, fixedWidth+1)
	summary := truncateTUI(
		tuiNotificationSummary(notification.displayMessage(tuiLanguageCode(language...))),
		maxTUIWidth(rightWidth-fixedWidth, 1),
	)
	right := tuiNotificationLevelColor(notification.level) + level + " · " +
		summary + tuiReset + tuiDim + " · " + hint + tuiReset
	rightWidth = tuiDisplayWidth(level) + tuiDisplayWidth(summary) +
		tuiDisplayWidth(hint) + 6
	leftWidth := maxTUIWidth(width-rightWidth-1, 0)
	left = truncateTUI(tuiShortFooter(left, leftWidth), leftWidth)
	line := left + strings.Repeat(
		" ",
		maxTUIWidth(width-tuiDisplayWidth(left)-rightWidth, 0),
	) + right
	return tuiClampAnsiLine(line, width)
}

func tuiNotificationTinySummary(notification tuiNotification, width int, language ...string) string {
	tr := tuiTranslator(language...)
	hint := tr("ui.8bf06050b8bb")
	message := truncateTUI(
		tuiNotificationSummary(notification.displayMessage(tuiLanguageCode(language...))),
		maxTUIWidth(width-tuiDisplayWidth(hint)-2, 1),
	)
	return tuiNotificationLevelColor(notification.level) + message +
		tuiReset + tuiDim + hint + tuiReset
}

func tuiNotificationTinyDetail(snapshot tuiSnapshot, width int, language ...string) []string {
	tr := tuiTranslator(language...)
	if len(snapshot.Notifications) == 0 {
		return []string{tr("ui.788011833a5a"), tr("ui.37ecf3603dbf"), tr("ui.0fdf605ea721")}
	}
	selected := minTUI(
		maxTUIWidth(snapshot.NotificationSelected, 0),
		len(snapshot.Notifications)-1,
	)
	notification := snapshot.Notifications[selected]
	lines := []string{fmt.Sprintf(tr("ui.948d9de89926"), notification.level,
		selected+1,
		len(snapshot.Notifications),
	)}
	messageLines := tuiNotificationLines(
		notification.displayMessage(tuiLanguageCode(language...)),
		maxTUIWidth(width, 1),
	)
	start := minTUI(
		maxTUIWidth(snapshot.NotificationScroll, 0),
		maxTUIWidth(len(messageLines)-1, 0),
	)
	lines = append(lines, messageLines[start:]...)
	lines = append(lines, tr("ui.ad2aa287d285"))
	return lines
}

func tuiNotificationDetailRows(height int) (int, int) {
	available := maxTUIWidth(height-6, 2)
	historyRows := minTUI(maxTUIWidth(available/3, 1), 6)
	return historyRows, maxTUIWidth(available-historyRows, 1)
}

func drawTUINotificationDetails(
	b *strings.Builder,
	snapshot tuiSnapshot,
	width,
	height int, language ...string,
) {
	tr := tuiTranslator(language...)
	historyRows, bodyRows := tuiNotificationDetailRows(height)
	tuiTitle(
		b, tr("ui.788011833a5a"), tr("ui.9a1bd2494b83"), width,
	)
	if len(snapshot.Notifications) == 0 {
		tuiRow(b, tr("ui.37ecf3603dbf"), width, false, tuiDim)
		for row := 1; row < historyRows; row++ {
			tuiRow(b, "", width, false, "")
		}
		tuiEndPanel(b, width)
		tuiTitle(b, tr("ui.45989de49fb7"), tr("ui.4a96199166fd"), width)
		for row := 0; row < bodyRows; row++ {
			tuiRow(b, "", width, false, "")
		}
		tuiEndPanel(b, width)
		return
	}

	selected := minTUI(
		maxTUIWidth(snapshot.NotificationSelected, 0),
		len(snapshot.Notifications)-1,
	)
	start := selected - historyRows/2
	start = minTUI(
		maxTUIWidth(start, 0),
		maxTUIWidth(len(snapshot.Notifications)-historyRows, 0),
	)
	end := minTUI(start+historyRows, len(snapshot.Notifications))
	for index := start; index < end; index++ {
		notification := snapshot.Notifications[index]
		state := "●"
		if notification.acknowledged {
			state = "✓"
		}
		timestamp := "--:--:--"
		if !notification.updatedAt.IsZero() {
			timestamp = notification.updatedAt.Format("15:04:05")
		}
		row := fmt.Sprintf(
			"%s %s  %-7s  %s",
			state,
			timestamp,
			notification.level,
			tuiNotificationSummary(notification.displayMessage(tuiLanguageCode(language...))),
		)
		tuiRow(
			b,
			row,
			width,
			index == selected,
			tuiNotificationLevelColor(notification.level),
		)
	}
	for row := end - start; row < historyRows; row++ {
		tuiRow(b, "", width, false, "")
	}
	tuiEndPanel(b, width)

	notification := snapshot.Notifications[selected]
	position := ""
	messageLines := tuiNotificationLines(
		notification.displayMessage(tuiLanguageCode(language...)),
		maxTUIWidth(width-4, 1),
	)
	startLine := minTUI(
		maxTUIWidth(snapshot.NotificationScroll, 0),
		maxTUIWidth(len(messageLines)-bodyRows, 0),
	)
	if len(messageLines) > bodyRows {
		position = fmt.Sprintf(
			" · %d/%d",
			startLine+1,
			len(messageLines),
		)
	}
	tuiTitle(
		b,
		notification.displayTitle(tuiLanguageCode(language...)),
		string(notification.level)+tr("ui.071e96130496")+position,
		width,
	)
	endLine := minTUI(startLine+bodyRows, len(messageLines))
	for index := startLine; index < endLine; index++ {
		tuiRow(b, messageLines[index], width, false, "")
	}
	for row := endLine - startLine; row < bodyRows; row++ {
		tuiRow(b, "", width, false, "")
	}
	tuiEndPanel(b, width)
}
