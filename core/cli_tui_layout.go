//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"strings"
)

// Rendering and input/scroll calculations must use the same cell geometry.
type tuiLayout struct {
	Tiny, Compact                     bool
	Sidebar, ContentWidth, PageHeight int
}

var tuiSidebarKeys = []string{
	"ui.0b0f3184ac1e", "ui.dbab3e8ce4ef", "ui.f98e848ae0ab",
	"ui.81c96db54ca0", "ui.3a00ed8f7bcd", "ui.831a2c35bc4b",
	"ui.4146a84e969e", "ui.7a3930e93388", "ui.564047817ced",
}

func tuiLayoutAtSize(width, height int, language string) tuiLayout {
	layout := tuiLayout{Tiny: width < 40 || height < 10}
	if layout.Tiny {
		layout.ContentWidth, layout.PageHeight = maxTUIWidth(width, 1), maxTUIWidth(height-3, 1)
		return layout
	}
	layout.Sidebar = minTUI(maxTUIWidth(width/5, 22), 28)
	tr := tuiTranslator(language)
	for _, key := range tuiSidebarKeys {
		layout.Sidebar = maxTUIWidth(layout.Sidebar, tuiDisplayWidth(tr(key))+6)
	}
	layout.Compact = width < 88 || height < 18 || width-layout.Sidebar-1 < 56
	if layout.Compact {
		layout.Sidebar = 0
		layout.ContentWidth, layout.PageHeight = maxTUIWidth(width-2, 1), maxTUIWidth(height-2, 1)
	} else {
		layout.ContentWidth, layout.PageHeight = width-layout.Sidebar-3, height-4
	}
	return layout
}

type tuiField struct {
	label, value, color string
	selected            bool
	truncate            bool
}

func tuiLabelField(label, value string) tuiField {
	return tuiField{label: strings.TrimSpace(label), value: value}
}

// Only split trusted catalog templates, never user values or raw log messages.
func tuiTemplateField(template string, args ...any) tuiField {
	index := strings.IndexByte(template, '%')
	if index < 0 {
		return tuiLabelField(template, "")
	}
	field := tuiLabelField(template[:index], fmt.Sprintf(template[index:], args...))
	if strings.HasSuffix(field.label, "↑") {
		field.label = strings.TrimSpace(strings.TrimSuffix(field.label, "↑"))
		field.value = "↑ " + field.value
	}
	return field
}

func tuiFieldRows(fields []tuiField, width int) []tuiDashboardCompactRow {
	available := maxTUIWidth(width-4, 1)
	column := 14
	for _, field := range fields {
		if field.value != "" {
			column = maxTUIWidth(column, tuiDisplayWidth(field.label)+1)
		}
	}
	var rows []tuiDashboardCompactRow
	for _, field := range fields {
		if field.value == "" {
			for _, line := range tuiWrapText(field.label, available) {
				rows = append(rows, tuiDashboardCompactRow{value: line, color: field.color, selected: field.selected})
			}
			continue
		}
		prefix, budget := tuiPadRight(field.label, column), available-column
		if budget < 12 {
			rows = append(rows, tuiDashboardCompactRow{value: truncateTUI(field.label, available), color: field.color, selected: field.selected})
			prefix, budget = "  ", maxTUIWidth(available-2, 1)
		}
		var lines []string
		if field.truncate {
			lines = []string{truncateTUI(field.value, budget)}
		} else if tuiDisplayWidth(field.value) <= budget {
			lines = []string{field.value}
		} else {
			lines = tuiWrapText(field.value, budget)
		}
		for _, line := range lines {
			rows = append(rows, tuiDashboardCompactRow{value: prefix + line, color: field.color, selected: field.selected, ansi: strings.Contains(line, "\x1b")})
			prefix = strings.Repeat(" ", tuiDisplayWidth(prefix))
		}
	}
	return rows
}

func tuiFieldInputWidth(fields []tuiField, width int) int {
	column := 14
	for _, field := range fields {
		if field.value != "" {
			column = maxTUIWidth(column, tuiDisplayWidth(field.label)+1)
		}
	}
	available := maxTUIWidth(width-4, 1)
	if available-column < 12 {
		return maxTUIWidth(available-2, 1)
	}
	return available - column
}

func tuiShortFooter(value string, width int) string {
	if tuiDisplayWidth(value) <= width {
		return value
	}
	return tuiPanelHeading("←→ ↑↓", "Enter · Esc · ? · q · ^C", width)
}

func tuiOverlayHint(value string, width int) string {
	if tuiDisplayWidth(value) <= width {
		return value
	}
	return "Enter · Esc"
}

func tuiConfirmationPanel(b *strings.Builder, title, body, hint string, width, height int, color string, warning ...string) {
	tuiTitle(b, title, hint, width)
	lines := tuiNotificationLines(body, maxTUIWidth(width-4, 1))
	limit := maxTUIWidth(height-4-len(warning), 1)
	if len(lines) > limit {
		lines = lines[:limit]
		lines[limit-1] = truncateTUI(lines[limit-1]+"…", width-4)
	}
	for _, line := range lines {
		tuiRow(b, line, width, false, color)
	}
	for _, line := range warning {
		tuiRow(b, line, width, false, tuiRed)
	}
	tuiRow(b, tuiOverlayHint(hint, width-4), width, false, tuiDim)
	tuiEndPanel(b, width)
}

func tuiWriteRows(b *strings.Builder, rows []tuiDashboardCompactRow, width int) {
	for _, row := range rows {
		// tuiRow preserves ANSI and also highlights selected wrapped fields.
		tuiRow(b, row.value, width, row.selected, row.color)
	}
}

func tuiSelectedRows(rows []tuiDashboardCompactRow, limit int) []tuiDashboardCompactRow {
	selected := 0
	selectedEnd := 0
	for index, row := range rows {
		if row.selected {
			selected = index
			break
		}
	}
	selectedEnd = selected
	for selectedEnd+1 < len(rows) && rows[selectedEnd+1].selected {
		selectedEnd++
	}
	start, end := tuiVisibleRange(len(rows), selected, maxTUIWidth(limit, 1))
	if selectedEnd >= end {
		end = selectedEnd + 1
		start = maxTUIIndex(end - maxTUIWidth(limit, 1))
	}
	return rows[start:end]
}

type tuiColumn struct {
	value          string
	width, minimum int
	optional       bool
}

// Optional columns yield space first; padding is in cells, not Go fmt runes.
func tuiColumns(width int, columns ...tuiColumn) string {
	width = maxTUIWidth(width, 1)
	columns = append([]tuiColumn(nil), columns...)
	total := func() int {
		result := maxTUIIndex(len(columns) - 1)
		for _, column := range columns {
			result += column.width
		}
		return result
	}
	for index := len(columns) - 1; index >= 0 && total() > width; index-- {
		if columns[index].optional {
			columns = append(columns[:index], columns[index+1:]...)
		}
	}
	for index := len(columns) - 1; index >= 0 && total() > width; index-- {
		columns[index].width -= minTUI(total()-width, maxTUIIndex(columns[index].width-columns[index].minimum))
	}
	var values []string
	for _, column := range columns {
		values = append(values, tuiPadRight(column.value, column.width))
	}
	return truncateTUI(strings.TrimRight(strings.Join(values, " "), " "), width)
}

// Keep the title intact; hints are optional, whole clauses rather than slices.
func tuiPanelHeading(title, subtitle string, width int) string {
	left := "  " + title
	if subtitle != "" && tuiDisplayWidth(left+"  ·  "+subtitle) <= width {
		return left + "  ·  " + subtitle
	}
	for _, hint := range strings.Split(subtitle, "·") {
		hint = strings.TrimSpace(hint)
		if hint == "" {
			continue
		}
		separator := " · "
		if left == "  "+title {
			separator = "  ·  "
		}
		candidate := left + separator + hint
		if tuiDisplayWidth(candidate) <= width {
			left = candidate
		}
	}
	return left
}

func (m *tuiModel) reflowTUI() {
	m.reflowTUIDetails()
	layout := tuiLayoutAtSize(m.width, m.height, m.snapshot.Language)
	if m.snapshot.Page == tuiPageDashboard && !layout.Tiny {
		rows := tuiCompactDashboardRows(m.snapshot, m.paths, layout.ContentWidth, layout.PageHeight, m.snapshot.Language)
		m.snapshot.DashboardScroll = minTUI(maxTUIIndex(m.snapshot.DashboardScroll), maxTUIIndex(len(rows)-maxTUIWidth(layout.PageHeight-3, 1)))
		m.revealDashboardSelection()
	}
	if m.notificationDetailOpen {
		m.notificationScroll = minTUI(maxTUIIndex(m.notificationScroll), m.notificationScrollLimit())
	}
}
