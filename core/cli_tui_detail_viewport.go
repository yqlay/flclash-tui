//go:build linux && !cgo && cli

package main

import "strings"

// Keep the existing outer panel intact while scrolling only its content.
func tuiDetailPanel(snapshot tuiSnapshot, width int, language ...string) string {
	var b strings.Builder
	switch snapshot.Page {
	case tuiPageRequests:
		drawTUIRequestDetail(&b, snapshot, width, language...)
	case tuiPageConnections:
		drawTUIConnectionDetail(&b, snapshot, width, language...)
	case tuiPageLogs:
		drawTUILogDetail(&b, snapshot, width, language...)
	}
	return b.String()
}

func tuiDetailScroll(snapshot *tuiSnapshot) *int {
	switch snapshot.Page {
	case tuiPageRequests:
		return &snapshot.HistoryDetailScroll
	case tuiPageConnections:
		return &snapshot.ConnectionsDetailScroll
	case tuiPageLogs:
		return &snapshot.LogDetailScroll
	default:
		return nil
	}
}

func tuiDetailOpen(snapshot tuiSnapshot) bool {
	switch snapshot.Page {
	case tuiPageRequests:
		return snapshot.HistoryDetailOpen
	case tuiPageConnections:
		return snapshot.ConnectionsDetailOpen
	case tuiPageLogs:
		return snapshot.LogDetailOpen
	default:
		return false
	}
}

func tuiDetailScrollLimit(snapshot tuiSnapshot, width, height int) int {
	lines := strings.Split(strings.TrimSuffix(tuiDetailPanel(snapshot, width, snapshot.Language), "\n"), "\n")
	return maxTUIIndex(len(lines) - height)
}

func drawTUIDetailViewport(b *strings.Builder, snapshot tuiSnapshot, width, height int, language ...string) {
	lines := strings.Split(strings.TrimSuffix(tuiDetailPanel(snapshot, width, language...), "\n"), "\n")
	if len(lines) < 3 || height < 4 {
		b.WriteString(strings.Join(lines, "\n") + "\n")
		return
	}
	limit := height - 3
	scroll := tuiDetailScroll(&snapshot)
	start := minTUI(maxTUIIndex(*scroll), maxTUIIndex(len(lines)-3-limit))
	b.WriteString(lines[0] + "\n" + lines[1] + "\n")
	end := minTUI(start+limit, len(lines)-3)
	for _, line := range lines[2+start : 2+end] {
		b.WriteString(line + "\n")
	}
	for row := end - start; row < limit; row++ {
		tuiRow(b, "", width, false, "")
	}
	b.WriteString(lines[len(lines)-1] + "\n")
}

func (m *tuiModel) scrollTUIDetail(delta int) bool {
	if !tuiDetailOpen(m.snapshot) {
		return false
	}
	layout := tuiLayoutAtSize(m.width, m.height, m.snapshot.Language)
	scroll := tuiDetailScroll(&m.snapshot)
	step := maxTUIWidth(layout.PageHeight-4, 1)
	*scroll = minTUI(maxTUIIndex(*scroll+delta*step), tuiDetailScrollLimit(m.snapshot, layout.ContentWidth, layout.PageHeight))
	return true
}

func (m *tuiModel) reflowTUIDetails() {
	layout := tuiLayoutAtSize(m.width, m.height, m.snapshot.Language)
	for _, page := range []tuiPage{tuiPageRequests, tuiPageConnections, tuiPageLogs} {
		snapshot := m.snapshot
		snapshot.Page = page
		scroll := tuiDetailScroll(&snapshot)
		if !tuiDetailOpen(snapshot) {
			*scroll = 0
		} else {
			*scroll = minTUI(maxTUIIndex(*scroll), tuiDetailScrollLimit(snapshot, layout.ContentWidth, layout.PageHeight))
		}
		switch page {
		case tuiPageRequests:
			m.snapshot.HistoryDetailScroll = *scroll
		case tuiPageConnections:
			m.snapshot.ConnectionsDetailScroll = *scroll
		case tuiPageLogs:
			m.snapshot.LogDetailScroll = *scroll
		}
	}
}
