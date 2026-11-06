//go:build linux && !cgo && cli

package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

func tuiSSHRouteRowAt(selected, profileCount, marked int) int {
	if selected < 0 {
		return 0
	}
	if profileCount == 0 {
		if marked == 1 && selected == 0 {
			return 1
		}
		if marked == 2 && selected == 1 {
			return 2
		}
		return 0
	}
	switch selected - profileCount {
	case 0:
		return 1
	case 1:
		return 2
	default:
		return 0
	}
}

func (m *tuiModel) openRouteList(filename, list string) {
	m.routeListOpen = true
	m.routeListStore = filename
	m.routeListName = list
	m.routeListCursor = 0
	m.reloadRouteListEntries()
	m.snapshot.setStatus(newTUIMessage("route.editor_hint"))
}

func (m *tuiModel) reloadRouteListEntries() {
	policy, err := loadRoutePolicy(m.paths.HomeDir, m.routeListStore)
	if err != nil {
		m.routeListEntries = nil
		m.snapshot.setStatus(routeListError(err))
		return
	}
	m.routeListEntries = append([]string(nil), policy.entries(m.routeListName)...)
	if len(m.routeListEntries) == 0 {
		m.routeListCursor = 0
		return
	}
	if m.routeListCursor >= len(m.routeListEntries) {
		m.routeListCursor = len(m.routeListEntries) - 1
	}
	if m.routeListCursor < 0 {
		m.routeListCursor = 0
	}
}

func (m *tuiModel) routeListTitle() string {
	tr := tuiTranslator(m.snapshot.Language)
	count := strconv.Itoa(len(m.routeListEntries))
	switch {
	case m.routeListStore == sshRoutePolicyFilename && m.routeListName == routeListBlacklist:
		return fmt.Sprintf(tr("route.ssh_blacklist"), count)
	case m.routeListStore == sshRoutePolicyFilename:
		return fmt.Sprintf(tr("route.ssh_whitelist"), count)
	case m.routeListName == routeListBlacklist:
		return fmt.Sprintf(tr("route.proxy_blacklist"), count)
	default:
		return fmt.Sprintf(tr("route.proxy_whitelist"), count)
	}
}

func (m *tuiModel) routeListNote() string {
	tr := tuiTranslator(m.snapshot.Language)
	if m.routeListStore == sshRoutePolicyFilename {
		return tr("route.ssh_note")
	}
	switch m.snapshot.Settings.Mode {
	case tuiSilentMode:
		return tr("route.proxy_note_silent")
	case "global":
		return tr("route.proxy_note_global")
	case "direct":
		return tr("route.proxy_note_direct")
	default:
		return tr("route.proxy_note_rule")
	}
}

func (m *tuiModel) handleRouteListKey(message tea.KeyMsg) tea.Cmd {
	key, ok := tuiKeyFromTea(message)
	if !ok {
		return nil
	}
	switch key {
	case tuiKeyQuit, tuiKeyInterrupt:
		return m.handleTeaKey(message)
	case tuiKeyUp:
		if len(m.routeListEntries) > 0 {
			m.routeListCursor = wrapTUIIndex(m.routeListCursor, -1, len(m.routeListEntries))
		}
	case tuiKeyDown:
		if len(m.routeListEntries) > 0 {
			m.routeListCursor = wrapTUIIndex(m.routeListCursor, 1, len(m.routeListEntries))
		}
	case tuiKeyPageUp:
		m.routeListCursor = moveRouteListCursor(m.routeListCursor, len(m.routeListEntries), -m.routeListWindow())
	case tuiKeyPageDown:
		m.routeListCursor = moveRouteListCursor(m.routeListCursor, len(m.routeListEntries), m.routeListWindow())
	case tuiKeyBack:
		m.routeListOpen = false
	case tuiKeyNewProfile:
		m.beginInput(tuiInputRouteEntry)
	case tuiKeyCloseConnections:
		return m.deleteSelectedRouteEntry()
	}
	return nil
}

func (m *tuiModel) commitRouteEntry(value string) tea.Cmd {
	if strings.TrimSpace(value) == "" {
		m.snapshot.setStatus(newTUIMessage("route.invalid", "empty"))
		return nil
	}
	added, err := addRouteEntry(m.paths.HomeDir, m.routeListStore, m.routeListName, value)
	if err != nil {
		m.snapshot.setStatus(routeListError(err))
		return nil
	}
	m.reloadRouteListEntries()
	m.routeListCursor = len(m.routeListEntries) - 1
	if m.routeListCursor < 0 {
		m.routeListCursor = 0
	}
	return m.finishRouteMutation(newTUIMessage("route.added", added))
}

func (m *tuiModel) deleteSelectedRouteEntry() tea.Cmd {
	if len(m.routeListEntries) == 0 || m.routeListCursor < 0 || m.routeListCursor >= len(m.routeListEntries) {
		m.snapshot.setStatus(newTUIMessage("route.empty"))
		return nil
	}
	removed, err := deleteRouteEntry(
		m.paths.HomeDir,
		m.routeListStore,
		m.routeListName,
		m.routeListEntries[m.routeListCursor],
	)
	if err != nil {
		m.snapshot.setStatus(routeListError(err))
		return nil
	}
	m.reloadRouteListEntries()
	return m.finishRouteMutation(newTUIMessage("route.removed", removed))
}

func (m *tuiModel) finishRouteMutation(message tuiMessage) tea.Cmd {
	if m.routeListStore != proxyRoutePolicyFilename || m.service == nil {
		m.snapshot.setStatus(message)
		return nil
	}
	return m.startOperation(func(state *tuiOperationState) {
		if err := reloadTUIOperationConfig(state, m.service, m.client, m.ownsCore); err != nil {
			state.snapshot.setStatus(newTUIMessage("route.saved_reload_failed", err.Error()))
			return
		}
		state.snapshot.setStatus(message)
	})
}

func routeListError(err error) tuiMessage {
	var invalid routeInvalidError
	switch {
	case errors.As(err, &invalid):
		return newTUIMessage("route.invalid", invalid.Reason)
	case errors.Is(err, errRouteDuplicate):
		return newTUIMessage("route.duplicate")
	case errors.Is(err, errRouteOverlap):
		return newTUIMessage("route.overlap")
	case errors.Is(err, errRouteFull):
		return newTUIMessage("route.full")
	case errors.Is(err, errRouteMissing):
		return newTUIMessage("route.missing")
	default:
		return newTUIMessage("route.invalid", err.Error())
	}
}

func annotateRouteCounts(snapshot *tuiSnapshot, home string) {
	proxy, err := loadRoutePolicy(home, proxyRoutePolicyFilename)
	if err != nil {
		proxy = routePolicy{}
	}
	sshPolicy, err := loadRoutePolicy(home, sshRoutePolicyFilename)
	if err != nil {
		sshPolicy = routePolicy{}
	}
	snapshot.ProxyWhitelistCount = len(proxy.Whitelist)
	snapshot.ProxyBlacklistCount = len(proxy.Blacklist)
	snapshot.SSHWhitelistCount = len(sshPolicy.Whitelist)
	snapshot.SSHBlacklistCount = len(sshPolicy.Blacklist)
}

func tuiRouteListWindow(height int) int {
	return maxTUIWidth(height-5, 1)
}

func (m *tuiModel) routeListWindow() int {
	layout := tuiLayoutAtSize(m.width, m.height, m.snapshot.Language)
	// A tiny terminal draws only the selected entry, so a page is one row.
	if layout.Tiny {
		return 1
	}
	return tuiRouteListWindow(layout.PageHeight)
}

func moveRouteListCursor(cursor, total, delta int) int {
	if total <= 0 {
		return 0
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= total {
		cursor = total - 1
	}
	next := cursor + delta
	if next < 0 {
		return 0
	}
	if next >= total {
		return total - 1
	}
	return next
}

func drawTUIRouteList(b *strings.Builder, snapshot tuiSnapshot, width, height int, language ...string) {
	tr := tuiTranslator(language...)
	tuiTitle(b, snapshot.RouteListTitle, tr("route.editor_hint"), width)
	if snapshot.RouteListNote != "" {
		tuiRow(b, snapshot.RouteListNote, width, false, tuiDim)
	}
	if len(snapshot.RouteListEntries) == 0 {
		tuiRow(b, tr("route.empty"), width, false, tuiDim)
		tuiEndPanel(b, width)
		return
	}
	limit := tuiRouteListWindow(height)
	start, end := tuiVisibleRange(len(snapshot.RouteListEntries), snapshot.RouteListCursor, limit)
	for index := start; index < end; index++ {
		tuiRow(b, snapshot.RouteListEntries[index], width, index == snapshot.RouteListCursor, "")
	}
	tuiEndPanel(b, width)
}
