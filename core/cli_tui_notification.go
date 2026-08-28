//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"strings"

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
	id       string
	level    tuiNotificationLevel
	title    string
	message  string
	progress bool
}

func (m *tuiModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	previousStatus := m.snapshot.Status
	model, command := m.update(message)
	m.publishStatusNotification(message, previousStatus)
	return model, command
}

func (m *tuiModel) publishStatusNotification(message tea.Msg, previousStatus string) {
	status := strings.TrimSpace(m.snapshot.Status)
	if status == "" || status == "Loading..." || status == "Connected" ||
		strings.HasPrefix(status, "Ready; ") ||
		strings.HasPrefix(status, "Selecting mode ") ||
		strings.HasPrefix(status, "Editing input ") ||
		tuiStatusIsContextHint(status) {
		return
	}
	if status == previousStatus {
		return
	}
	if keyMessage, ok := message.(tea.KeyMsg); ok && tuiNotificationNavigationKey(keyMessage) {
		return
	}
	level := tuiNotificationLevelForStatus(status)
	progress := m.busy || status == "Working..." ||
		strings.Contains(strings.ToLower(status), "testing") ||
		strings.Contains(strings.ToLower(status), "in progress")
	operationID := ""
	if progress || m.hasProgressNotification() {
		operationID = "operation"
	}
	m.enqueueNotification(tuiNotification{
		id:       operationID,
		level:    level,
		title:    tuiNotificationTitle(level, progress),
		message:  status,
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

func tuiStatusIsContextHint(status string) bool {
	for _, prefix := range []string{
		"Proxy groups ·",
		"Nodes in ",
		"Providers view ·",
		"Enter to import ",
		"Enter to copy ",
		"Enter activates ·",
		"Active subscription ·",
		"Active local profile ·",
		"Select an outbound mode",
	} {
		if strings.HasPrefix(status, prefix) {
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

func tuiNotificationLevelForStatus(status string) tuiNotificationLevel {
	lower := strings.ToLower(status)
	switch {
	case strings.Contains(lower, "failed"),
		strings.Contains(lower, "error"),
		strings.Contains(lower, "invalid"),
		strings.Contains(lower, "unavailable"),
		strings.Contains(lower, "could not"),
		strings.Contains(lower, "cannot"),
		strings.Contains(lower, "timeout"),
		strings.Contains(lower, "interrupted"):
		return tuiNotificationError
	case strings.Contains(lower, "cancel"),
		strings.Contains(lower, "requires"),
		strings.Contains(lower, "disabled"),
		strings.Contains(lower, "stopped"):
		return tuiNotificationWarning
	case strings.Contains(lower, "complete"),
		strings.Contains(lower, "succeeded"),
		strings.Contains(lower, "saved"),
		strings.Contains(lower, "updated"),
		strings.Contains(lower, "enabled"),
		strings.Contains(lower, "started"),
		strings.Contains(lower, "switched"),
		strings.Contains(lower, "reloaded"):
		return tuiNotificationSuccess
	default:
		return tuiNotificationInfo
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
	logMessage := sanitizeCLIApplicationLogDetail(
		m.paths.homeDir,
		notification.message,
	)
	appendTUILogEvent(string(notification.level), logMessage)
	m.snapshot.Logs = cliLogSnapshot()
	for index := range m.notifications {
		current := m.notifications[index]
		if current.message == notification.message {
			return
		}
		if notification.id != "" &&
			(current.id == notification.id ||
				(notification.id == "operation" && current.progress)) {
			m.notifications[index] = notification
			if index == 0 {
				m.notificationScroll = 0
			}
			return
		}
	}
	m.notifications = append(m.notifications, notification)
}

func (m *tuiModel) handleNotificationKey(key tuiKey) {
	switch key {
	case tuiKeySelect, tuiKeyBack:
		m.notifications = m.notifications[1:]
		m.notificationScroll = 0
	case tuiKeyUp:
		m.notificationScroll = maxTUIWidth(m.notificationScroll-1, 0)
	case tuiKeyDown:
		m.notificationScroll = minTUI(
			m.notificationScroll+1,
			m.notificationScrollLimit(),
		)
	case tuiKeyPageUp:
		m.notificationScroll = maxTUIWidth(
			m.notificationScroll-maxTUIWidth(m.height-2, 1),
			0,
		)
	case tuiKeyPageDown:
		m.notificationScroll = minTUI(
			m.notificationScroll+maxTUIWidth(m.height-2, 1),
			m.notificationScrollLimit(),
		)
	}
}

func (m *tuiModel) notificationScrollLimit() int {
	if len(m.notifications) == 0 {
		return 0
	}
	lineWidth := maxTUIWidth(m.width-6, 1)
	visible := maxTUIWidth(m.height-2, 1)
	lines := tuiNotificationLines(m.notifications[0].message, lineWidth)
	return maxTUIWidth(len(lines)-visible, 0)
}

func tuiNotificationLines(message string, width int) []string {
	paragraphs := strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n")
	lines := make([]string, 0, len(paragraphs))
	for _, paragraph := range paragraphs {
		lines = append(lines, tuiWrapText(paragraph, width)...)
	}
	return lines
}

func renderTUINotification(snapshot tuiSnapshot, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	title := snapshot.NotificationTitle
	if title == "" {
		title = "Notification"
	}
	header := fmt.Sprintf(
		"FlClash · %s · %s",
		title,
		snapshot.NotificationLevel,
	)
	hint := "Enter confirm · Esc close"
	if snapshot.NotificationProgress {
		hint = "Enter hide · Esc hide · operation continues"
	}
	if width < 12 || height < 5 {
		return renderTUINotificationTiny(
			header,
			snapshot.NotificationMessage,
			hint,
			width,
			height,
		)
	}
	contentWidth := maxTUIWidth(width-6, 1)
	bodyHeight := maxTUIWidth(height-2, 1)
	lines := tuiNotificationLines(snapshot.NotificationMessage, contentWidth)
	start := minTUI(
		maxTUIWidth(snapshot.NotificationScroll, 0),
		maxTUIWidth(len(lines)-bodyHeight, 0),
	)
	end := minTUI(start+bodyHeight, len(lines))
	visible := lines[start:end]

	var b strings.Builder
	b.WriteString(tuiClampAnsiLine("  "+header, width))
	b.WriteByte('\n')
	for row := 0; row < bodyHeight; row++ {
		line := ""
		if row < len(visible) {
			line = "  " + visible[row]
		}
		b.WriteString(tuiClampAnsiLine(line, width))
		b.WriteByte('\n')
	}
	position := ""
	if len(lines) > bodyHeight {
		position = fmt.Sprintf(" · %d/%d", start+1, len(lines))
	}
	b.WriteString(tuiClampAnsiLine("  "+hint+position, width))
	return b.String()
}

func renderTUINotificationTiny(
	header,
	message,
	hint string,
	width,
	height int,
) string {
	lines := []string{header}
	lines = append(
		lines,
		tuiNotificationLines(message, maxTUIWidth(width, 1))...,
	)
	lines = append(lines, hint)
	var b strings.Builder
	for row := 0; row < height; row++ {
		line := ""
		if row < len(lines) {
			line = lines[row]
		}
		b.WriteString(tuiClampAnsiLine(line, width))
		if row < height-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}
