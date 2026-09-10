//go:build linux && !cgo && cli

package main

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIFormatting(t *testing.T) {
	if got := formatBytes(0); got != "0.0 B" {
		t.Fatalf("formatBytes(0) = %q", got)
	}
	if got := formatBytes(1024 * 1024); got != "1.0 MB" {
		t.Fatalf("formatBytes(1 MiB) = %q", got)
	}
	if got := truncateTUI("abcdef", 4); got != "abc…" {
		t.Fatalf("truncateTUI = %q", got)
	}
	if got := tuiDisplayWidth("节点"); got != 4 {
		t.Fatalf("CJK display width = %d", got)
	}
	if got := tuiDisplayWidth("e\u0301"); got != 1 {
		t.Fatalf("combining display width = %d", got)
	}
	if got := tuiDisplayWidth("🚀"); got != 2 {
		t.Fatalf("emoji display width = %d", got)
	}
	longWord := "/run/user/1000/flclash/a-very-long-session-name"
	wrapped := tuiWrapText(longWord, 12)
	if strings.Join(wrapped, "") != longWord {
		t.Fatalf("wrapped long word lost content: %q", wrapped)
	}
	for _, line := range wrapped {
		if tuiDisplayWidth(line) > 12 {
			t.Fatalf("wrapped line is too wide: %q", line)
		}
	}
}

func TestTUIRenderingFitsTerminalWidth(t *testing.T) {
	paths := cliPaths{ConfigPath: "/tmp/flclash/config.yaml"}
	for _, size := range []struct {
		width  int
		height int
	}{
		{width: 40, height: 8},
		{width: 40, height: 10},
		{width: 44, height: 10},
		{width: 50, height: 12},
		{width: 64, height: 14},
		{width: 72, height: 20},
		{width: 87, height: 21},
		{width: 88, height: 22},
		{width: 80, height: 24},
		{width: 120, height: 30},
	} {
		for page := tuiPageDashboard; page < tuiPageCount; page++ {
			snapshot := populatedTUISnapshot(page)
			var output bytes.Buffer
			drawTUIAtSize(&output, snapshot, paths, "127.0.0.1:9090", true, true, size.width, size.height)
			if strings.HasSuffix(output.String(), "\n") {
				t.Fatalf("page %d at %dx%d ends with a newline", page, size.width, size.height)
			}
			assertTUIUsesCRLF(t, output.String())
			lines := strings.Split(output.String(), "\n")
			if len(lines) != size.height {
				t.Fatalf("page %d at %dx%d has %d lines, want %d", page, size.width, size.height, len(lines), size.height)
			}
			for lineNumber, line := range lines {
				if got := tuiDisplayWidth(stripTUIANSI(line)); got != size.width {
					t.Fatalf("page %d at %dx%d line %d has width %d, want %d: %q", page, size.width, size.height, lineNumber, got, size.width, line)
				}
			}
		}
	}
}

func TestTUIRenderingFitsEveryPositiveTerminalSize(t *testing.T) {
	paths := cliPaths{ConfigPath: "/tmp/flclash/config.yaml"}
	snapshots := make([]tuiSnapshot, 0, int(tuiPageCount))
	for page := tuiPageDashboard; page < tuiPageCount; page++ {
		snapshots = append(snapshots, populatedTUISnapshot(page))
	}
	for width := 1; width <= 160; width++ {
		for height := 1; height <= 60; height++ {
			for page, snapshot := range snapshots {
				output := renderTUIAtSize(
					snapshot,
					paths,
					"private Unix socket",
					true,
					true,
					width,
					height,
				)
				lines := strings.Split(output, "\n")
				if len(lines) != height {
					t.Fatalf(
						"page %d at %dx%d has %d lines, want %d",
						page,
						width,
						height,
						len(lines),
						height,
					)
				}
				for lineNumber, line := range lines {
					if got := tuiDisplayWidth(stripTUIANSI(line)); got != width {
						t.Fatalf(
							"page %d at %dx%d line %d has width %d, want %d",
							page,
							width,
							height,
							lineNumber,
							got,
							width,
						)
					}
				}
			}
		}
	}
}

func TestTUINotificationRenderingFitsEveryPositiveTerminalSize(t *testing.T) {
	paths := cliPaths{ConfigPath: "/tmp/flclash/config.yaml"}
	notifications := []tuiNotification{
		{
			level:     tuiNotificationError,
			title:     "Operation failed",
			message:   strings.Repeat("Long notification 节点 error details ", 20),
			updatedAt: time.Date(2026, 8, 29, 12, 34, 56, 0, time.UTC),
		},
		{
			level:        tuiNotificationSuccess,
			title:        "Operation complete",
			message:      "Previous notification",
			updatedAt:    time.Date(2026, 8, 29, 12, 30, 0, 0, time.UTC),
			acknowledged: true,
		},
	}
	for width := 1; width <= 160; width++ {
		for height := 1; height <= 60; height++ {
			for _, detailsOpen := range []bool{false, true} {
				snapshot := populatedTUISnapshot(tuiPageDashboard)
				snapshot.Notifications = notifications
				snapshot.NotificationDetailOpen = detailsOpen
				snapshot.NotificationScroll = 3
				output := renderTUIAtSize(
					snapshot,
					paths,
					"private Unix socket",
					true,
					true,
					width,
					height,
				)
				lines := strings.Split(output, "\n")
				if len(lines) != height {
					t.Fatalf(
						"notification details=%t at %dx%d has %d lines, want %d",
						detailsOpen,
						width,
						height,
						len(lines),
						height,
					)
				}
				for lineNumber, line := range lines {
					if got := tuiDisplayWidth(stripTUIANSI(line)); got != width {
						t.Fatalf(
							"notification details=%t at %dx%d line %d has width %d, want %d: %q",
							detailsOpen,
							width,
							height,
							lineNumber,
							got,
							width,
							line,
						)
					}
				}
			}
		}
	}
}

func TestTUIProfileDeleteConfirmationOverridesCompactNavigation(t *testing.T) {
	paths := cliPaths{ConfigPath: "/tmp/flclash/config.yaml"}
	for _, size := range []struct {
		width  int
		height int
	}{
		{width: 40, height: 10},
		{width: 64, height: 14},
		{width: 87, height: 17},
		{width: 88, height: 18},
		{width: 120, height: 30},
	} {
		snapshot := populatedTUISnapshot(tuiPageProfiles)
		snapshot.FocusSidebar = true
		snapshot.ProfileDelete = tuiProfileDeleteView{
			Open: true,
			Name: "school.yaml",
			Kind: "subscription",
		}
		output := renderTUIAtSize(
			snapshot,
			paths,
			"private Unix socket",
			true,
			true,
			size.width,
			size.height,
		)
		plain := stripTUIANSI(output)
		if !strings.Contains(plain, "Delete Profile") ||
			!strings.Contains(plain, "Delete school.yaml") {
			t.Fatalf(
				"Profile confirmation was hidden at %dx%d:\n%s",
				size.width,
				size.height,
				plain,
			)
		}
		lines := strings.Split(output, "\n")
		if len(lines) != size.height {
			t.Fatalf(
				"Profile confirmation at %dx%d has %d lines, want %d",
				size.width,
				size.height,
				len(lines),
				size.height,
			)
		}
		for lineNumber, line := range lines {
			if got := tuiDisplayWidth(stripTUIANSI(line)); got != size.width {
				t.Fatalf(
					"Profile confirmation at %dx%d line %d has width %d, want %d",
					size.width,
					size.height,
					lineNumber,
					got,
					size.width,
				)
			}
		}
	}
}

type tuiImmediateQuitModel struct{}

func (tuiImmediateQuitModel) Init() tea.Cmd {
	return tea.Quit
}

func (model tuiImmediateQuitModel) Update(tea.Msg) (tea.Model, tea.Cmd) {
	return model, nil
}

func (tuiImmediateQuitModel) View() string {
	return ""
}

func TestTUIProgramDoesNotCaptureTerminalMouse(t *testing.T) {
	var output bytes.Buffer
	options := append(
		tuiProgramOptions(),
		tea.WithInput(nil),
		tea.WithOutput(&output),
	)
	program := tea.NewProgram(tuiImmediateQuitModel{}, options...)
	if _, err := program.Run(); err != nil {
		t.Fatal(err)
	}

	rendered := output.String()
	if !strings.Contains(rendered, "\x1b[?1049h") {
		t.Fatalf("test did not observe alternate-screen startup: %q", rendered)
	}
	for _, mouseEnableSequence := range []string{
		"\x1b[?1000h",
		"\x1b[?1002h",
		"\x1b[?1003h",
	} {
		if strings.Contains(rendered, mouseEnableSequence) {
			t.Fatalf(
				"TUI enabled terminal mouse capture with %q; native text selection would be blocked",
				mouseEnableSequence,
			)
		}
	}
}

func TestTUICompactDashboardCanScrollEverySection(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{
			HomeDir:    "/tmp/flclash",
			ConfigPath: "/tmp/flclash/config.yaml",
		},
		nil,
		true,
	)
	model.width = 50
	model.height = 12
	model.snapshot = populatedTUISnapshot(tuiPageDashboard)
	model.snapshot.FocusSidebar = false
	model.snapshot.Frontends = []cliProcessOwner{{PID: 101}, {PID: 202}}

	first := stripTUIANSI(model.View())
	if !strings.Contains(first, "Core") ||
		!strings.Contains(first, "Live traffic") ||
		strings.Contains(first, "TUI frontends") {
		t.Fatalf("first compact viewport is wrong:\n%s", first)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	network := stripTUIANSI(model.View())
	if !strings.Contains(network, "Public IP") ||
		!strings.Contains(network, "Rule route") {
		t.Fatalf("PageDown did not reveal network section:\n%s", network)
	}
	viewports := []string{first, network}
	for count := 0; count < 6; count++ {
		_, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
		viewports = append(viewports, stripTUIANSI(model.View()))
	}
	all := strings.Join(viewports, "\n")
	for _, expected := range []string{
		"Go heap",
		"Network speed",
		"TUI frontends",
		"Config",
	} {
		if !strings.Contains(all, expected) {
			t.Fatalf("compact Dashboard never revealed %q:\n%s", expected, all)
		}
	}
	last := stripTUIANSI(model.View())
	if !strings.Contains(last, "TUI frontends") ||
		!strings.Contains(last, "Config") {
		t.Fatalf("last compact viewport is inaccessible:\n%s", last)
	}

	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if model.snapshot.DashboardScroll != 1 {
		t.Fatalf(
			"dashboard selection was not revealed near the controls: %d",
			model.snapshot.DashboardScroll,
		)
	}
}

func TestTUICompactNavigationTracksSelectedPage(t *testing.T) {
	snapshot := populatedTUISnapshot(tuiPageTools)
	snapshot.FocusSidebar = true
	snapshot.SelectedMenu = int(tuiPageTools)
	output := stripTUIANSI(renderTUIAtSize(
		snapshot,
		cliPaths{},
		"private Unix socket",
		true,
		true,
		44,
		10,
	))
	if !strings.Contains(output, "Navigation") ||
		!strings.Contains(output, "8  Settings") ||
		!strings.Contains(output, "9  Maintenance") {
		t.Fatalf("compact navigation hid selected page:\n%s", output)
	}
}

func TestTUIEscReturnsFromEveryPageToNavigationAtEveryLayout(t *testing.T) {
	sizes := []struct {
		width  int
		height int
	}{
		{width: 40, height: 10},
		{width: 44, height: 10},
		{width: 64, height: 14},
		{width: 87, height: 17},
		{width: 88, height: 18},
		{width: 120, height: 30},
	}
	for _, size := range sizes {
		for page := tuiPageDashboard; page < tuiPageCount; page++ {
			t.Run(fmt.Sprintf("%dx%d/%s", size.width, size.height, tuiPageName(page)), func(t *testing.T) {
				model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
				model.width = size.width
				model.height = size.height
				model.snapshot = populatedTUISnapshot(page)
				model.snapshot.SelectedMenu = int(page)
				model.snapshot.FocusSidebar = false
				model.snapshot.ProxyNodeFocus = false

				_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})

				if !model.snapshot.FocusSidebar {
					t.Fatalf("Esc did not return %s to navigation: %+v", tuiPageName(page), model.snapshot)
				}
				if model.snapshot.SelectedMenu != int(page) {
					t.Fatalf(
						"navigation selection = %d after leaving %s, want %d",
						model.snapshot.SelectedMenu,
						tuiPageName(page),
						page,
					)
				}
				if size.width >= 40 &&
					size.height >= 10 &&
					(size.width < 88 || size.height < 18) {
					output := stripTUIANSI(model.View())
					if !strings.Contains(output, "Navigation") {
						t.Fatalf(
							"navigation is not visible after Esc at %dx%d:\n%s",
							size.width,
							size.height,
							output,
						)
					}
				}
			})
		}
	}
}

func TestTUIEscFollowsProxyNavigationHierarchy(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.width = 44
	model.height = 10
	model.snapshot = populatedTUISnapshot(tuiPageProxies)
	model.snapshot.SelectedMenu = int(tuiPageProxies)
	model.snapshot.FocusSidebar = false
	model.snapshot.ProxyView = tuiProxyViewGroups
	model.snapshot.ProxyNodeFocus = true

	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if model.snapshot.ProxyNodeFocus || model.snapshot.FocusSidebar {
		t.Fatalf("first Esc did not return only to proxy groups: %+v", model.snapshot)
	}

	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !model.snapshot.FocusSidebar {
		t.Fatalf("second Esc did not return to navigation: %+v", model.snapshot)
	}
	if output := stripTUIANSI(model.View()); !strings.Contains(output, "Navigation") {
		t.Fatalf("compact navigation is not visible after second Esc:\n%s", output)
	}
}

func TestTUIExistingFrontendNoticeIsNonBlockingAndOpensDetails(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{},
		nil,
		true,
	)
	model.width = 100
	model.height = 30
	model.enqueueNotification(tuiNotification{
		level:   tuiNotificationInfo,
		title:   "Shared backend",
		message: "Attached to shared backend · 1 other TUI frontend: PID 123 /dev/pts/2",
	})
	output := stripTUIANSI(model.View())
	if !strings.Contains(output, "Ctrl+N details") ||
		!strings.Contains(output, "INFO") {
		t.Fatalf("frontend startup notice summary is not visible:\n%s", output)
	}
	_, _ = model.Update(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'2'},
	})
	if model.snapshot.Page != tuiPageSSH || len(model.notifications) != 1 {
		t.Fatalf("notification blocked normal navigation: %+v", model.snapshot)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	details := stripTUIANSI(model.View())
	for _, expected := range []string{
		"FlClash  ·  terminal proxy manager",
		"Dashboard",
		"Notifications",
		"Shared backend",
		"PID 123",
	} {
		if !strings.Contains(details, expected) {
			t.Fatalf("framed notification details do not contain %q:\n%s", expected, details)
		}
	}
}

func TestTUINotificationReplacesProgressAndPreservesPage(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Page = tuiPageProfiles
	model.snapshot.FocusSidebar = false
	model.enqueueNotification(tuiNotification{
		id:       "operation",
		level:    tuiNotificationInfo,
		message:  "Working...",
		progress: true,
	})
	model.enqueueNotification(tuiNotification{
		id:      "operation",
		level:   tuiNotificationSuccess,
		message: "Configuration saved and hot-reloaded",
	})
	if len(model.notifications) != 1 ||
		model.notifications[0].message != "Configuration saved and hot-reloaded" {
		t.Fatalf("progress notification was not replaced: %+v", model.notifications)
	}
	plain := stripTUIANSI(model.View())
	if !strings.Contains(plain, "Ctrl+N details") ||
		!strings.Contains(plain, "Configuration saved") {
		t.Fatalf("notification footer summary is incomplete:\n%s", plain)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	plain = stripTUIANSI(model.View())
	if !strings.Contains(plain, "Operation complete") ||
		!strings.Contains(plain, "Enter confirm · Esc close") ||
		!strings.Contains(plain, "Profiles") {
		t.Fatalf("notification details are incomplete:\n%s", plain)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !model.notifications[0].acknowledged {
		t.Fatal("Enter did not confirm the selected notification")
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if len(model.notifications) != 1 || model.notificationDetailOpen ||
		model.snapshot.Page != tuiPageProfiles ||
		model.snapshot.FocusSidebar {
		t.Fatalf("notification details changed the underlying page: %+v", model.snapshot)
	}
	if strings.Contains(stripTUIANSI(model.View()), "Ctrl+N details") {
		t.Fatal("confirmed notification remained in the footer")
	}
}

func TestTUINotificationHistoryIsBoundedAndKeepsNewestUnread(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	for index := 0; index < tuiNotificationHistoryLimit+5; index++ {
		model.enqueueNotification(tuiNotification{
			level:   tuiNotificationInfo,
			message: fmt.Sprintf("Notice %02d", index),
		})
	}
	if len(model.notifications) != tuiNotificationHistoryLimit ||
		model.notifications[0].message != "Notice 54" ||
		model.notifications[len(model.notifications)-1].message != "Notice 05" {
		t.Fatalf("notification history was not bounded newest-first: %+v", model.notifications)
	}
	model.notifications[0].acknowledged = true
	model.enqueueNotification(tuiNotification{
		level:   tuiNotificationWarning,
		message: "Notice 53",
	})
	if model.notifications[0].message != "Notice 53" ||
		model.notifications[0].acknowledged {
		t.Fatalf("duplicate notification was not refreshed as unread: %+v", model.notifications[0])
	}
}

func TestTUINotificationKeepsCompletedOperationsAndDetailScroll(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.enqueueNotification(tuiNotification{
		id:       "operation",
		level:    tuiNotificationInfo,
		message:  "First operation working",
		progress: true,
	})
	model.enqueueNotification(tuiNotification{
		id:      "operation",
		level:   tuiNotificationSuccess,
		message: "First operation complete",
	})
	if len(model.notifications) != 1 || model.notifications[0].id != "" {
		t.Fatalf("completed operation remained replaceable: %+v", model.notifications)
	}
	model.enqueueNotification(tuiNotification{
		id:       "operation",
		level:    tuiNotificationInfo,
		message:  "Second operation working",
		progress: true,
	})
	model.enqueueNotification(tuiNotification{
		id:      "operation",
		level:   tuiNotificationSuccess,
		message: "Second operation complete",
	})
	if len(model.notifications) != 2 ||
		model.notifications[0].message != "Second operation complete" ||
		model.notifications[1].message != "First operation complete" {
		t.Fatalf("successive operations did not keep separate history: %+v", model.notifications)
	}

	model.notificationDetailOpen = true
	model.notificationSelected = 1
	model.notificationScroll = 7
	model.enqueueNotification(tuiNotification{
		level:   tuiNotificationWarning,
		message: "Unrelated background warning",
	})
	if model.notificationSelected != 2 || model.notificationScroll != 7 {
		t.Fatalf(
			"background notification disturbed selected details: selected=%d scroll=%d",
			model.notificationSelected,
			model.notificationScroll,
		)
	}
}

func TestTUINotificationFooterAdaptsWithoutColorBleed(t *testing.T) {
	snapshot := tuiSnapshot{
		Notifications: []tuiNotification{{
			level:   tuiNotificationError,
			message: "A deliberately long notification message that must be truncated safely",
		}},
	}
	for _, width := range []int{40, 72, 120} {
		line := tuiNotificationFooter(
			snapshot,
			"  ←→ panel  ↑↓ move  Enter apply  q exit",
			width,
		)
		plain := stripTUIANSI(line)
		if !strings.Contains(plain, "ERROR") ||
			!strings.Contains(plain, "Ctrl+N details") {
			t.Fatalf("width %d footer notification is incomplete: %q", width, plain)
		}
		if got := tuiDisplayWidth(plain); got != width {
			t.Fatalf("width %d footer rendered at %d cells: %q", width, got, plain)
		}
		if !strings.HasSuffix(line, tuiReset) {
			t.Fatalf("width %d footer did not reset ANSI color: %q", width, line)
		}
	}
}

func TestTUINotificationDetailsSelectScrollAndRestoreInput(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.width = 70
	model.height = 16
	model.snapshot.Page = tuiPageProfiles
	model.snapshot.FocusSidebar = false
	model.inputMode = tuiInputSubscription
	model.inputValue = []rune("https://example.test/subscription")
	model.enqueueNotification(tuiNotification{
		level:   tuiNotificationWarning,
		message: strings.Repeat("older message section ", 40),
	})
	model.enqueueNotification(tuiNotification{
		level:   tuiNotificationSuccess,
		message: "newest message",
	})

	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyCtrlN})
	if !model.notificationDetailOpen || model.notificationSelected != 0 {
		t.Fatalf("Ctrl+N did not select newest unread notification: %+v", model)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if model.notificationSelected != 1 || model.notificationScroll == 0 {
		t.Fatalf("notification selection or detail scrolling failed: %+v", model)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if model.notificationDetailOpen || model.inputMode != tuiInputSubscription {
		t.Fatalf("closing details did not restore input state: %+v", model)
	}
	plain := stripTUIANSI(model.View())
	if !strings.Contains(plain, "Import subscription") ||
		!strings.Contains(plain, "example.test") {
		t.Fatalf("input view was not restored after notification details:\n%s", plain)
	}
	key, ok := tuiKeyFromTea(tea.KeyMsg{Type: tea.KeyCtrlN})
	if !ok || key != tuiKeyNotifications {
		t.Fatalf("Ctrl+N key = (%v, %v)", key, ok)
	}
}

func TestTUINotificationLogsRedactSensitiveURLs(t *testing.T) {
	clearTUILogs()
	model := newTUIModel(
		controllerClient{},
		cliPaths{HomeDir: "/tmp/private-flclash"},
		nil,
		true,
	)
	model.enqueueNotification(tuiNotification{
		level: tuiNotificationError,
		message: "Add profile failed: https://subscription.example/token " +
			"/tmp/private-flclash/config.yaml",
	})
	logs := cliLogSnapshot()
	if len(logs) == 0 {
		t.Fatal("notification feedback was not written to Logs")
	}
	last := logs[len(logs)-1]
	if strings.Contains(last, "subscription.example") ||
		strings.Contains(last, "/tmp/private-flclash") ||
		!strings.Contains(last, "[redacted-url]") ||
		!strings.Contains(last, "$DATA") {
		t.Fatalf("notification log was not redacted: %q", last)
	}
}

func TestTUIQuitClosesFrontendFromNotificationDetails(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.enqueueNotification(tuiNotification{
		level:   tuiNotificationInfo,
		message: "A notification is open",
	})
	model.toggleNotificationDetails()
	command := model.handleTeaKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'q'},
	})
	if command == nil || !model.frontendExitRequested {
		t.Fatal("q did not request frontend exit from notification details")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatal("q did not return Bubble Tea's quit message")
	}
}

func TestTUIStatusDoesNotPolluteFooter(t *testing.T) {
	snapshot := populatedTUISnapshot(tuiPageDashboard)
	snapshot.Status = "This operation failed and must not appear in the footer"
	plain := stripTUIANSI(renderTUIAtSize(
		snapshot,
		cliPaths{},
		"unix:///tmp/core.sock",
		true,
		true,
		120,
		36,
	))
	if strings.Contains(plain, snapshot.Status) {
		t.Fatalf("dynamic status leaked into the Dashboard footer:\n%s", plain)
	}
}

func TestTUIDashboardRouteTestsAreSelectable(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.FocusSidebar = false
	model.snapshot.SelectedDashboard = tuiDashboardMixedPortRow

	model.moveSelection(1)
	if model.snapshot.SelectedDashboard != tuiDashboardDelayRow {
		t.Fatalf("Dashboard down selected %d, want route delay", model.snapshot.SelectedDashboard)
	}
	if command := model.selectCurrent(); command != nil ||
		!strings.Contains(model.snapshot.Status, "delay test") {
		t.Fatalf("Dashboard Enter did not invoke route delay: %q", model.snapshot.Status)
	}
	model.moveSelection(1)
	if model.snapshot.SelectedDashboard != tuiDashboardSpeedRow {
		t.Fatalf("Dashboard down selected %d, want route speed", model.snapshot.SelectedDashboard)
	}
	if command := model.selectCurrent(); command != nil ||
		!strings.Contains(model.snapshot.Status, "speed test") {
		t.Fatalf("Dashboard Enter did not invoke route speed: %q", model.snapshot.Status)
	}
}

func TestTUIRowStylesDoNotColorPanelBorders(t *testing.T) {
	var output strings.Builder
	tuiRow(&output, "Network", 30, true, tuiCyan)
	row := output.String()
	if !strings.HasPrefix(row, "│"+tuiSelect) ||
		!strings.HasSuffix(row, tuiReset+"│\n") {
		t.Fatalf("row styling polluted its panel borders: %q", row)
	}
}

func TestTUIFrameWriterSkipsUnchangedFrames(t *testing.T) {
	var output bytes.Buffer
	writer := &tuiFrameWriter{writer: &output}
	if _, err := writer.Write([]byte("frame")); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("frame")); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "frame" {
		t.Fatalf("duplicate frame output = %q", got)
	}
	writer.invalidate()
	if _, err := writer.Write([]byte("frame")); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "frameframe" {
		t.Fatalf("invalidated frame output = %q", got)
	}
}
