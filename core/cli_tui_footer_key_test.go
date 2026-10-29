//go:build linux && !cgo && cli

package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestWideFooterDelayClaimMatchesTheKey(t *testing.T) {
	delayClause := wideFooterDelayClause(t)
	closeClause := tuiConnectionCloseFooterClause("en")
	if closeClause == "" || closeClause == delayClause {
		t.Fatalf("close clause = %q, delay clause = %q", closeClause, delayClause)
	}

	connections := wideFooterSnapshot(tuiPageConnections)
	footer := renderedWideFooter(t, connections)
	if strings.Contains(footer, delayClause) || !strings.Contains(footer, closeClause) {
		t.Fatalf("connections footer = %q", footer)
	}
	if !strings.Contains(footer, "q ") || !strings.Contains(footer, "^C") {
		t.Fatalf("connections footer dropped exit keys: %q", footer)
	}

	model := newRowEnterModel(t)
	model.snapshot = connections
	model.snapshot.Language = "en"
	model.snapshot.FocusSidebar = false
	model.snapshot.SelectedConnection = 0
	model.width = 120
	model.height = 40
	_, command := model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if command != nil || !model.dangerConfirmOpen || model.dangerConfirmKey != tuiKeyCloseConnection {
		t.Fatalf("d confirm open=%t key=%v command=%t", model.dangerConfirmOpen, model.dangerConfirmKey, command != nil)
	}
	if model.dangerConfirmTarget != connections.Connections[0].ID || model.snapshot.DashboardDelay.Testing {
		t.Fatalf("d target=%q delay=%t", model.dangerConfirmTarget, model.snapshot.DashboardDelay.Testing)
	}

	for _, page := range []tuiPage{
		tuiPageProfiles,
		tuiPageRequests,
		tuiPageLogs,
		tuiPageTools,
		tuiPageMaintenance,
	} {
		snapshot := wideFooterSnapshot(page)
		line := renderedWideFooter(t, snapshot)
		if strings.Contains(line, delayClause) || strings.Contains(line, closeClause) {
			t.Fatalf("%v footer = %q", page, line)
		}
		pageModel := newRowEnterModel(t)
		pageModel.snapshot = snapshot
		pageModel.width = 120
		pageModel.height = 40
		_, pageCommand := pageModel.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
		if pageCommand != nil || pageModel.dangerConfirmOpen || pageModel.snapshot.DashboardDelay.Testing {
			t.Fatalf("%v d command=%t confirm=%t delay=%t", page, pageCommand != nil, pageModel.dangerConfirmOpen, pageModel.snapshot.DashboardDelay.Testing)
		}
	}

	providers := wideFooterSnapshot(tuiPageProxies)
	providers.ProxyView = tuiProxyViewProviders
	providers.ProxyNodeFocus = false
	if strings.Contains(renderedWideFooter(t, providers), delayClause) {
		t.Fatal("proxy providers footer advertises delay")
	}
	providerModel := newRowEnterModel(t)
	providerModel.snapshot = providers
	providerModel.width = 120
	providerModel.height = 40
	_, providerCommand := providerModel.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if providerCommand != nil || providerModel.dangerConfirmOpen || proxyDelayStarted(providerModel.snapshot) {
		t.Fatal("d on proxy providers started a delay or a close confirm")
	}

	if !strings.Contains(renderedWideFooter(t, wideFooterSnapshot(tuiPageDashboard)), delayClause) {
		t.Fatal("dashboard footer dropped the delay claim")
	}
	groups := wideFooterSnapshot(tuiPageProxies)
	groups.ProxyView = tuiProxyViewGroups
	groups.ProxyNodeFocus = false
	if !strings.Contains(renderedWideFooter(t, groups), delayClause) {
		t.Fatal("proxy groups footer dropped the delay claim")
	}
}

func wideFooterSnapshot(page tuiPage) tuiSnapshot {
	snapshot := populatedTUISnapshot(page)
	snapshot.Language = "en"
	snapshot.FocusSidebar = false
	return snapshot
}

func renderedWideFooter(t *testing.T, snapshot tuiSnapshot) string {
	t.Helper()
	output := renderTUIAtSize(snapshot, cliPaths{}, "socket", true, true, 120, 40)
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) != 40 {
		t.Fatalf("frame lines = %d", len(lines))
	}
	footer := stripTUIANSI(lines[len(lines)-1])
	if tuiDisplayWidth(footer) != 120 {
		t.Fatalf("footer width = %d: %q", tuiDisplayWidth(footer), footer)
	}
	return footer
}

func wideFooterDelayClause(t *testing.T) string {
	t.Helper()
	segments, _ := tuiSplitFooterSegments(tuiTranslator("en")("ui.f43c74fbfff4"))
	for _, segment := range segments {
		if tuiFooterNamesDKey(segment) {
			return segment
		}
	}
	t.Fatal("wide footer has no d clause")
	return ""
}

func proxyDelayStarted(snapshot tuiSnapshot) bool {
	for _, group := range snapshot.Groups {
		for _, delay := range group.Delays {
			if delay.Testing {
				return true
			}
		}
	}
	return false
}
