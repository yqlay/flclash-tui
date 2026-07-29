//go:build linux && !cgo && cli

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
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
}

func TestTUIRenderingFitsTerminalWidth(t *testing.T) {
	paths := cliPaths{configPath: "/tmp/flclash/config.yaml"}
	for _, size := range []struct {
		width  int
		height int
	}{
		{width: 40, height: 8},
		{width: 64, height: 14},
		{width: 72, height: 20},
		{width: 80, height: 24},
		{width: 120, height: 30},
	} {
		for page := tuiPageDashboard; page <= tuiPageTools; page++ {
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

func TestBubbleTeaRefreshDoesNotBlockNavigation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/proxies" {
			time.Sleep(200 * time.Millisecond)
			_, _ = io.WriteString(w, `{"proxies":{
				"A":{"type":"Selector","now":"A1","all":["A1"]},
				"B":{"type":"Selector","now":"B1","all":["B1"]}
			}}`)
			return
		}
		http.NotFound(w, request)
	}))
	defer server.Close()

	model := newTUIModel(
		controllerClient{
			options: controllerOptions{address: server.URL},
			client:  server.Client(),
		},
		cliPaths{homeDir: t.TempDir()},
		nil,
		false,
	)
	model.snapshot.Page = tuiPageProxies
	model.snapshot.FocusSidebar = false
	model.snapshot.Groups = []tuiGroup{
		{Name: "A", Nodes: []string{"A1"}},
		{Name: "B", Nodes: []string{"B1"}},
	}
	refresh := model.startRefresh()
	result := make(chan tea.Msg, 1)
	go func() {
		result <- refresh()
	}()

	started := time.Now()
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if command != nil {
		t.Fatal("navigation unexpectedly returned a command")
	}
	if elapsed := time.Since(started); elapsed > 50*time.Millisecond {
		t.Fatalf("navigation blocked for %s while refresh was running", elapsed)
	}
	if model.snapshot.SelectedGroup != 1 {
		t.Fatalf("selected group = %d, want 1", model.snapshot.SelectedGroup)
	}

	message := <-result
	_, _ = model.Update(message)
	if model.snapshot.SelectedGroup != 1 {
		t.Fatalf("background refresh overwrote the current selection: %d", model.snapshot.SelectedGroup)
	}
}

func TestBubbleTeaViewLeavesTerminalControlToRenderer(t *testing.T) {
	model := newTUIModel(
		controllerClient{options: controllerOptions{address: "127.0.0.1:9090"}},
		cliPaths{configPath: "/tmp/flclash/config.yaml"},
		nil,
		false,
	)
	model.width = 80
	model.height = 24
	view := model.View()
	for _, sequence := range []string{"\x1b[H", "\x1b[2J", "\x1b[?1049h"} {
		if strings.Contains(view, sequence) {
			t.Fatalf("view contains terminal lifecycle sequence %q", sequence)
		}
	}
}

func TestBubbleTeaInputOwnsTypedCharacters(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{},
		nil,
		false,
	)
	model.beginInput(tuiInputSubscription)
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("https://example.test/q")})
	if command != nil {
		t.Fatal("typing unexpectedly returned a command")
	}
	if got := string(model.inputValue); got != "https://example.test/q" {
		t.Fatalf("input = %q", got)
	}
}

func TestBubbleTeaInputSupportsReplacementAndCursorEditing(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{},
		nil,
		false,
	)
	model.snapshot.Settings.MixedPort = 7890
	model.beginInput(tuiInputMixedPort)

	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1")})
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("790")})
	if got := string(model.inputValue); got != "1790" {
		t.Fatalf("typing did not replace the selected port: %q", got)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyLeft})
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("8")})
	if got := string(model.inputValue); got != "1780" {
		t.Fatalf("cursor edit = %q, want 1780", got)
	}
}

func TestBubbleTeaBlocksMutatingActionsWhileBusy(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageTools
	model.snapshot.FocusSidebar = false
	model.snapshot.Settings.TunEnabled = false
	model.busy = true

	_, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if command != nil {
		t.Fatal("busy mutation unexpectedly returned a command")
	}
	if model.snapshot.Settings.TunEnabled {
		t.Fatal("busy mutation changed the staged settings")
	}
	if !strings.Contains(model.snapshot.Status, "Operation in progress") {
		t.Fatalf("busy status = %q", model.snapshot.Status)
	}

	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
	if model.snapshot.Page != tuiPageProfiles {
		t.Fatalf("navigation was blocked while busy: page %d", model.snapshot.Page)
	}
}

func TestTUIProfilesExposeSubscriptionImportAsSelectableRow(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{},
		nil,
		false,
	)
	model.snapshot.Page = tuiPageProfiles
	model.snapshot.FocusSidebar = false
	model.snapshot.Profiles = []tuiProfile{
		{Name: "config.yaml", Path: "/tmp/config.yaml"},
	}

	if model.snapshot.SelectedRow != -1 {
		t.Fatalf("initial profile selection = %d, want import row", model.snapshot.SelectedRow)
	}
	if command := model.selectCurrent(); command != nil {
		t.Fatal("opening the subscription input unexpectedly returned a command")
	}
	if model.inputMode != tuiInputSubscription {
		t.Fatalf("input mode = %d, want subscription input", model.inputMode)
	}

	model.inputMode = tuiInputNone
	model.inputValue = nil
	moveTUIProfile(&model.snapshot, 1)
	if model.snapshot.SelectedRow != 0 {
		t.Fatalf("down from import selected row %d, want first profile", model.snapshot.SelectedRow)
	}
	moveTUIProfile(&model.snapshot, -1)
	if model.snapshot.SelectedRow != -1 {
		t.Fatalf("up from first profile selected row %d, want import", model.snapshot.SelectedRow)
	}
}

func TestTUIRenamesSelectedProfileFromVisibleAction(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "profile-old.yaml")
	if err := os.WriteFile(sourcePath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(
		controllerClient{},
		cliPaths{
			homeDir:    directory,
			configPath: filepath.Join(directory, "config.yaml"),
		},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageProfiles
	model.snapshot.FocusSidebar = false
	model.snapshot.Profiles = []tuiProfile{{
		Name: "profile-old.yaml",
		Path: sourcePath,
	}}
	model.snapshot.SelectedRow = 0

	model.beginProfileRename()
	if model.inputMode != tuiInputProfileName {
		t.Fatalf("input mode = %d, want profile name", model.inputMode)
	}
	_, _ = model.Update(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("office"),
	})
	command := model.submitInput()
	if command == nil {
		t.Fatal("profile rename did not return an operation")
	}
	_, _ = model.Update(command())

	destinationPath := filepath.Join(directory, "office.yaml")
	if _, err := os.Stat(destinationPath); err != nil {
		t.Fatalf("renamed profile: %v", err)
	}
	if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
		t.Fatalf("old profile still exists: %v", err)
	}
	if model.snapshot.SelectedRow < 0 ||
		model.snapshot.SelectedRow >= len(model.snapshot.Profiles) ||
		model.snapshot.Profiles[model.snapshot.SelectedRow].Path != destinationPath {
		t.Fatalf("renamed profile was not selected: %+v", model.snapshot)
	}
	if model.snapshot.Status != "Renamed profile to office.yaml" {
		t.Fatalf("rename status = %q", model.snapshot.Status)
	}
}

func TestTUIProfileRenameRejectsUnsafeAndDuplicateNames(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "source.yaml")
	if err := os.WriteFile(sourcePath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, "existing.yaml"),
		[]byte(defaultTUIConfig),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"../outside",
		"nested/profile",
		"profile.json",
		"existing.yaml",
	} {
		if _, err := renameTUIProfile(directory, sourcePath, name); err == nil {
			t.Fatalf("unsafe or duplicate name %q was accepted", name)
		}
		if _, err := os.Stat(sourcePath); err != nil {
			t.Fatalf("failed rename removed source for %q: %v", name, err)
		}
	}
}

func TestTUIActiveProfileRenameShowsGuidance(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Page = tuiPageProfiles
	model.snapshot.FocusSidebar = false
	model.snapshot.Profiles = []tuiProfile{{
		Name:    "config.yaml",
		Path:    "/tmp/config.yaml",
		Current: true,
	}}
	model.snapshot.SelectedRow = 0

	model.beginProfileRename()
	if model.inputMode != tuiInputNone {
		t.Fatal("active profile opened the rename input")
	}
	if !strings.Contains(model.snapshot.Status, "Activate another profile") {
		t.Fatalf("active profile guidance = %q", model.snapshot.Status)
	}
}

func TestTUIProfileRenameKeyAndHintAreVisible(t *testing.T) {
	key, ok := tuiKeyFromTea(tea.KeyMsg{Type: tea.KeyF2})
	if !ok || key != tuiKeyRenameProfile {
		t.Fatalf("F2 key = (%v, %v)", key, ok)
	}
	updateKey, ok := tuiKeyFromTea(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'U'},
	})
	if !ok || updateKey != tuiKeyUpdateProfile {
		t.Fatalf("U key = (%v, %v)", updateKey, ok)
	}
	snapshot := tuiSnapshot{
		Page:         tuiPageProfiles,
		SelectedRow:  0,
		FocusSidebar: false,
		Profiles: []tuiProfile{{
			Name:            "work.yaml",
			Path:            "/tmp/work.yaml",
			SubscriptionURL: "https://example.test/subscription",
		}},
	}
	var output strings.Builder
	drawTUIProfiles(&output, snapshot, 110, 24)
	plain := stripTUIANSI(output.String())
	for _, hint := range []string{"U refresh", "F2/u rename", "F2 rename"} {
		if !strings.Contains(plain, hint) {
			t.Fatalf("profiles view does not contain %q:\n%s", hint, plain)
		}
	}
}

func TestTUIQuitKeysUseGracefulShutdownPath(t *testing.T) {
	tests := []struct {
		name              string
		key               tea.KeyMsg
		expected          tuiKey
		stopServiceOnExit bool
	}{
		{
			name:     "q detaches TUI",
			key:      tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}},
			expected: tuiKeyQuit,
		},
		{
			name:              "ctrl+c stops service",
			key:               tea.KeyMsg{Type: tea.KeyCtrlC},
			expected:          tuiKeyInterrupt,
			stopServiceOnExit: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key, ok := tuiKeyFromTea(test.key)
			if !ok || key != test.expected {
				t.Fatalf("quit key = (%v, %v)", key, ok)
			}
			model := newTUIModel(controllerClient{}, cliPaths{}, nil, false)
			command := model.handleTeaKey(test.key)
			if command == nil {
				t.Fatal("quit key did not return a command")
			}
			if _, ok := command().(tea.QuitMsg); !ok {
				t.Fatal("quit key did not terminate the event loop")
			}
			if model.stopServiceOnExit != test.stopServiceOnExit {
				t.Fatalf(
					"stopServiceOnExit = %t, want %t",
					model.stopServiceOnExit,
					test.stopServiceOnExit,
				)
			}
		})
	}
}

func TestTUIProfileRenameUsesVisibleInputPanel(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.width = 80
	model.height = 24
	model.snapshot.Page = tuiPageProfiles
	model.snapshot.FocusSidebar = false
	model.snapshot.Profiles = []tuiProfile{{
		Name: "old-name.yaml",
		Path: "/tmp/old-name.yaml",
	}}
	model.snapshot.SelectedRow = 0
	model.beginProfileRename()

	plain := stripTUIANSI(model.View())
	for _, expected := range []string{
		"Rename profile",
		"old-name█",
		".yaml is added automatically",
		"Enter confirm",
		"Esc cancel",
	} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("rename input panel does not contain %q:\n%s", expected, plain)
		}
	}
}

func TestTUIInputViewportKeepsCursorVisible(t *testing.T) {
	value := []rune("https://example.test/a/very/long/subscription/token")
	output := tuiInputViewport(value, len(value), 20)
	if !strings.Contains(output, "█") {
		t.Fatalf("input viewport lost cursor: %q", output)
	}
	if tuiDisplayWidth(output) > 20 {
		t.Fatalf("input viewport width = %d, want <= 20: %q", tuiDisplayWidth(output), output)
	}
	if !strings.HasPrefix(output, "…") {
		t.Fatalf("long input viewport has no leading ellipsis: %q", output)
	}
}

func TestTUISettingsExposeAllInteractiveRows(t *testing.T) {
	snapshot := tuiSnapshot{
		Page: tuiPageTools,
		Settings: tuiSettings{
			Mode:       "rule",
			MixedPort:  17890,
			AllowLAN:   true,
			LogLevel:   "info",
			TunEnabled: true,
		},
	}
	var output strings.Builder
	drawTUISettings(&output, snapshot, 80, 24)
	plain := stripTUIANSI(output.String())
	for _, row := range []string{
		"Mode          rule",
		"Mixed port    17890",
		"Allow LAN     ON",
		"IPv6          OFF",
		"Log level     info",
		"TUN           ON",
		"Service       STOPPED · Enter to start",
		"System proxy  DISABLED · Enter to enable (starts Service)",
	} {
		if !strings.Contains(plain, row) {
			t.Fatalf("settings view does not contain %q:\n%s", row, plain)
		}
	}
	serviceIndex := strings.Index(plain, "Service       STOPPED · Enter to start")
	systemProxyIndex := strings.Index(
		plain,
		"System proxy  DISABLED · Enter to enable (starts Service)",
	)
	if serviceIndex < 0 || systemProxyIndex < 0 || serviceIndex >= systemProxyIndex {
		t.Fatalf("service row must appear before system proxy:\n%s", plain)
	}
}

func TestTUISettingsServiceRowUsesEnter(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageTools
	model.snapshot.FocusSidebar = false
	model.snapshot.SelectedTool = tuiSettingsServiceRow

	command := model.selectCurrent()
	if command == nil {
		t.Fatal("selecting the service row did not return a start operation")
	}
	if !model.busy {
		t.Fatal("selecting the service row did not mark the operation busy")
	}
}

func TestTUISettingsSystemProxyStartsStoppedService(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.FocusSidebar = false
	model.snapshot.SelectedDashboard = tuiDashboardSystemProxyRow

	if command := model.selectCurrent(); command == nil {
		t.Fatal("system proxy did not schedule automatic service startup")
	}
	if !model.busy {
		t.Fatal("automatic service startup did not mark the operation busy")
	}
}

func TestTUISettingsRunningServiceUnlocksSystemProxy(t *testing.T) {
	snapshot := tuiSnapshot{
		ServiceRunning: true,
		Settings: tuiSettings{
			SystemProxy: false,
		},
	}
	var output strings.Builder
	drawTUISettings(&output, snapshot, 80, 24)
	plain := stripTUIANSI(output.String())
	if !strings.Contains(plain, "Service       RUNNING · Enter to stop") {
		t.Fatalf("running settings view has no clear service state:\n%s", plain)
	}
	if !strings.Contains(plain, "System proxy  DISABLED · Enter to enable") {
		t.Fatalf("running settings view has no clear system proxy state:\n%s", plain)
	}
	if strings.Contains(plain, "(starts Service)") {
		t.Fatalf("running settings view kept automatic-start hint:\n%s", plain)
	}
}

func TestTUISidebarMatchesGraphicalInformationArchitecture(t *testing.T) {
	lines := tuiSidebar(
		tuiSnapshot{
			Page:         tuiPageDashboard,
			SelectedMenu: int(tuiPageDashboard),
			FocusSidebar: true,
		},
		26,
		20,
	)
	plain := stripTUIANSI(strings.Join(lines, "\n"))
	labels := []string{
		"Dashboard",
		"Proxies",
		"Profiles",
		"Requests",
		"Connections",
		"Logs",
		"Tools",
	}
	previous := -1
	for _, label := range labels {
		index := strings.Index(plain, label)
		if index < 0 {
			t.Fatalf("sidebar does not contain %q:\n%s", label, plain)
		}
		if index <= previous {
			t.Fatalf("sidebar order is wrong at %q:\n%s", label, plain)
		}
		previous = index
	}
	for _, removed := range []string{"Settings", "Providers"} {
		if strings.Contains(plain, removed) {
			t.Fatalf("sidebar still exposes standalone %s page:\n%s", removed, plain)
		}
	}
}

func TestTUIProxyViewsKeepProvidersInsideProxies(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, false)
	model.snapshot.Page = tuiPageProxies
	model.snapshot.SelectedMenu = int(tuiPageProxies)
	model.snapshot.FocusSidebar = false
	model.snapshot.Providers = []tuiProvider{
		{Name: "one"},
		{Name: "two"},
	}

	_, command := model.Update(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{']'},
	})
	if command != nil {
		t.Fatal("switching the proxy view unexpectedly returned a command")
	}
	if model.snapshot.Page != tuiPageProxies ||
		model.snapshot.ProxyView != tuiProxyViewProviders {
		t.Fatalf("provider view escaped Proxies: %+v", model.snapshot)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if model.snapshot.SelectedProvider != 1 {
		t.Fatalf("provider selection = %d, want 1", model.snapshot.SelectedProvider)
	}
	if command := model.selectCurrent(); command == nil {
		t.Fatal("provider Enter did not schedule an update")
	}
}

func TestTUIToolsExposeSettingsAndMaintenance(t *testing.T) {
	snapshot := tuiSnapshot{
		Page: tuiPageTools,
		Settings: tuiSettings{
			Mode:      "rule",
			MixedPort: 7891,
			LogLevel:  "info",
		},
	}
	var output strings.Builder
	drawTUITools(&output, snapshot, 100, 30)
	plain := stripTUIANSI(output.String())
	for _, expected := range []string{
		"Mode          rule",
		"Mixed port    7891",
		"Unified delay",
		"TCP concurrent",
		"System proxy",
		"Edit current YAML",
		"Update Mihomo Geo databases",
		"Reset traffic counters",
		"if stable, do not update lightly",
	} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("Tools does not contain %q:\n%s", expected, plain)
		}
	}
}

func TestTUIRequestHistoryTracksLifecycleAndLimit(t *testing.T) {
	start := time.Unix(100, 0)
	connection := tuiConnection{
		ID:      "request-1",
		Host:    "example.com",
		Process: "curl",
		Network: "tcp",
		Chain:   "PROXY",
	}
	history := updateTUIRequestHistory(nil, []tuiConnection{connection}, start)
	if len(history) != 1 || !history[0].Active {
		t.Fatalf("new request was not captured as active: %+v", history)
	}
	connection.Download = 1024
	history = updateTUIRequestHistory(
		history,
		[]tuiConnection{connection},
		start.Add(time.Second),
	)
	if len(history) != 1 || history[0].Download != 1024 {
		t.Fatalf("active request was duplicated or not updated: %+v", history)
	}
	history = updateTUIRequestHistory(
		history,
		nil,
		start.Add(2*time.Second),
	)
	if history[0].Active {
		t.Fatalf("completed request remained active: %+v", history[0])
	}

	oversized := make([]tuiRequest, tuiRequestHistoryLimit+20)
	for index := range oversized {
		oversized[index] = tuiRequest{
			tuiConnection: tuiConnection{ID: fmt.Sprintf("id-%d", index)},
			LastSeen:      start.Add(time.Duration(index) * time.Second),
		}
	}
	limited := updateTUIRequestHistory(oversized, nil, start)
	if len(limited) != tuiRequestHistoryLimit {
		t.Fatalf("request history size = %d, want %d", len(limited), tuiRequestHistoryLimit)
	}
}

func TestTUILogExportAndClear(t *testing.T) {
	clearTUILogs()
	sendMessage(Message{
		Type: LogMessage,
		Data: map[string]string{"Payload": "first event"},
	})
	sendMessage(Message{
		Type: LogMessage,
		Data: map[string]string{"Payload": "second event"},
	})
	logs := cliLogSnapshot()
	if len(logs) != 2 {
		t.Fatalf("captured logs = %v, want two entries", logs)
	}

	path, err := exportTUILogs(t.TempDir(), logs)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first event\nsecond event\n" {
		t.Fatalf("exported logs = %q", data)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("exported log mode = %o, want 600", info.Mode().Perm())
	}

	model := &tuiModel{
		snapshot: tuiSnapshot{
			Page: tuiPageLogs,
			Logs: logs,
		},
	}
	if command := model.handleKey(tuiKeyCloseConnections); command != nil {
		t.Fatal("clearing logs unexpectedly started an asynchronous command")
	}
	if len(model.snapshot.Logs) != 0 || len(cliLogSnapshot()) != 0 {
		t.Fatal("clearing logs left captured entries behind")
	}
}

func TestTUIEditKeyIsPageScoped(t *testing.T) {
	model := &tuiModel{
		snapshot: tuiSnapshot{Page: tuiPageDashboard},
		paths:    cliPaths{configPath: "/tmp/config.yaml"},
	}
	if command := model.handleKey(tuiKeyEdit); command != nil {
		t.Fatal("Dashboard edit key unexpectedly opened an editor")
	}
	if !strings.Contains(model.snapshot.Status, "Profiles and Tools") {
		t.Fatalf("Dashboard edit guidance = %q", model.snapshot.Status)
	}

	model.snapshot.Page = tuiPageProfiles
	model.snapshot.SelectedRow = -1
	if command := model.handleKey(tuiKeyEdit); command != nil {
		t.Fatal("profile import row unexpectedly opened an editor")
	}
	if !strings.Contains(model.snapshot.Status, "Select a profile") {
		t.Fatalf("Profiles edit guidance = %q", model.snapshot.Status)
	}
}

func TestFormatTUIDestination(t *testing.T) {
	for _, test := range []struct {
		host string
		port string
		want string
	}{
		{host: "1.1.1.1", port: "443", want: "1.1.1.1:443"},
		{host: "2001:db8::1", port: "53", want: "[2001:db8::1]:53"},
		{host: "example.com", want: "example.com"},
		{port: "443"},
	} {
		if got := formatTUIDestination(test.host, test.port); got != test.want {
			t.Fatalf(
				"formatTUIDestination(%q, %q) = %q, want %q",
				test.host,
				test.port,
				got,
				test.want,
			)
		}
	}
}

func TestTUIRequestsCanBeClearedWithoutClosingConnections(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, false)
	model.snapshot.Page = tuiPageRequests
	model.snapshot.FocusSidebar = false
	model.snapshot.Requests = []tuiRequest{{
		tuiConnection: tuiConnection{ID: "request-1"},
	}}
	model.snapshot.Connections = []tuiConnection{{ID: "active-1"}}

	if command := model.handleKey(tuiKeyCloseConnections); command != nil {
		t.Fatal("clearing request history unexpectedly called the controller")
	}
	if len(model.snapshot.Requests) != 0 {
		t.Fatalf("request history was not cleared: %+v", model.snapshot.Requests)
	}
	if len(model.snapshot.Connections) != 1 {
		t.Fatal("clearing request history changed active connections")
	}
}

func TestControllerClientTestsSelectedProxyDelay(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/proxies/HK%20Node/delay" &&
			request.URL.Path != "/proxies/HK Node/delay" {
			t.Fatalf("delay path = %q", request.URL.Path)
		}
		if got := request.URL.Query().Get("url"); got != "https://example.test/204?a=1" {
			t.Fatalf("delay test URL = %q", got)
		}
		if got := request.URL.Query().Get("timeout"); got != "5000" {
			t.Fatalf("delay timeout = %q", got)
		}
		_, _ = io.WriteString(w, `{"delay":42}`)
	}))
	defer server.Close()
	client := controllerClient{
		options: controllerOptions{address: server.URL},
		client:  server.Client(),
	}
	delay, err := client.testProxyDelay(
		"HK Node",
		"https://example.test/204?a=1",
	)
	if err != nil {
		t.Fatal(err)
	}
	if delay != 42 {
		t.Fatalf("delay = %d, want 42", delay)
	}
}

func TestControllerClientDelayTestOverridesShortRefreshTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		_ *http.Request,
	) {
		time.Sleep(60 * time.Millisecond)
		_, _ = io.WriteString(w, `{"delay":35}`)
	}))
	defer server.Close()
	httpClient := server.Client()
	httpClient.Timeout = 10 * time.Millisecond
	client := controllerClient{
		options: controllerOptions{address: server.URL},
		client:  httpClient,
	}

	delay, err := client.testProxyDelay("Node", "https://example.test/204")
	if err != nil {
		t.Fatalf("delay test reused the short refresh timeout: %v", err)
	}
	if delay != 35 {
		t.Fatalf("delay = %d, want 35", delay)
	}
}

func TestTUIWholeGroupDelayTestCollectsReachableAndTimeoutNodes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		request *http.Request,
	) {
		node := strings.TrimSuffix(
			strings.TrimPrefix(request.URL.Path, "/proxies/"),
			"/delay",
		)
		switch node {
		case "fast":
			_, _ = io.WriteString(w, `{"delay":18}`)
		case "slow":
			_, _ = io.WriteString(w, `{"delay":240}`)
		default:
			http.Error(w, "unreachable", http.StatusGatewayTimeout)
		}
	}))
	defer server.Close()
	client := controllerClient{
		options: controllerOptions{address: server.URL},
		client:  server.Client(),
	}

	delays := testTUIProxyDelays(
		client,
		[]string{"fast", "slow", "dead"},
		"https://example.test/204",
	)
	if delays["fast"] != 18 || delays["slow"] != 240 || delays["dead"] != -1 {
		t.Fatalf("whole-group delays = %#v", delays)
	}
}

func TestTUIWholeGroupDelayKeyUpdatesVisibleNodeStates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		request *http.Request,
	) {
		if strings.Contains(request.URL.Path, "dead") {
			http.Error(w, "unreachable", http.StatusGatewayTimeout)
			return
		}
		_, _ = io.WriteString(w, `{"delay":27}`)
	}))
	defer server.Close()
	model := newTUIModel(
		controllerClient{
			options: controllerOptions{address: server.URL},
			client:  server.Client(),
		},
		cliPaths{},
		nil,
		false,
	)
	model.snapshot.Page = tuiPageProxies
	model.snapshot.ProxyView = tuiProxyViewGroups
	model.snapshot.FocusSidebar = false
	model.snapshot.Groups = []tuiGroup{{
		Name:   "Proxy",
		Nodes:  []string{"fast", "dead"},
		Delays: map[string]int{},
	}}

	command := model.handleKey(tuiKeyDelayTest)
	if command == nil {
		t.Fatal("d did not test all nodes from proxy-group mode")
	}
	for _, node := range model.snapshot.Groups[0].Nodes {
		if model.snapshot.Groups[0].Delays[node] != -2 {
			t.Fatalf("%s was not marked Testing", node)
		}
	}
	_, _ = model.Update(command())
	if model.snapshot.Groups[0].Delays["fast"] != 27 ||
		model.snapshot.Groups[0].Delays["dead"] != -1 {
		t.Fatalf("visible node delays = %#v", model.snapshot.Groups[0].Delays)
	}
	if !strings.Contains(model.snapshot.Status, "1/2 reachable") {
		t.Fatalf("whole-group status = %q", model.snapshot.Status)
	}
}

func TestTUINetworkDetectionUsesConfiguredMixedPortProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Host != "public-ip.invalid" {
			t.Fatalf("proxy target host = %q", request.URL.Host)
		}
		_, _ = io.WriteString(w, `{"ip":"203.0.113.8","country_code":"SG"}`)
	}))
	defer proxy.Close()
	proxyPort := proxy.Listener.Addr().(*net.TCPAddr).Port

	result := detectTUIPublicIP(
		newTUINetworkHTTPClient(proxyPort),
		[]string{"http://public-ip.invalid/json"},
	)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.IP != "203.0.113.8" || result.Country != "SG" {
		t.Fatalf("public IP result = %+v", result)
	}
}

func TestTUIPublicIPDetectionAcceptsOriginalFlClashResponseShapes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/invalid":
			http.Error(w, "failed", http.StatusServiceUnavailable)
		default:
			_, _ = io.WriteString(w, `{"query":"198.51.100.9","countryCode":"US"}`)
		}
	}))
	defer server.Close()

	result := detectTUIPublicIP(
		server.Client(),
		[]string{server.URL + "/invalid", server.URL + "/valid"},
	)
	if result.Err != nil {
		t.Fatal(result.Err)
	}
	if result.IP != "198.51.100.9" || result.Country != "US" {
		t.Fatalf("public IP result = %+v", result)
	}
}

func TestTUIIntranetIPPrefersPrivateWiFiIPv4(t *testing.T) {
	selected, ok := selectTUIIntranetIP([]tuiLocalIPCandidate{
		{Interface: "eth0", Address: net.ParseIP("2001:db8::8")},
		{Interface: "docker0", Address: net.ParseIP("172.17.0.1")},
		{Interface: "wlp2s0", Address: net.ParseIP("192.168.1.23")},
		{Interface: "eth0", Address: net.ParseIP("198.51.100.4")},
	})
	if !ok {
		t.Fatal("no intranet IP was selected")
	}
	if selected.Interface != "wlp2s0" ||
		selected.Address.String() != "192.168.1.23" {
		t.Fatalf("selected intranet IP = %+v", selected)
	}
}

func TestTUIDashboardRendersPublicAndIntranetIP(t *testing.T) {
	snapshot := populatedTUISnapshot(tuiPageDashboard)
	snapshot.Network = tuiNetworkInfo{
		PublicIP:   "203.0.113.8",
		Country:    "SG",
		IntranetIP: "192.168.1.23 (wlp2s0)",
		Route:      "PROXY 127.0.0.1:7890",
		CheckedAt:  time.Date(2026, 7, 29, 12, 34, 56, 0, time.Local),
	}
	var output strings.Builder
	drawTUIDashboard(
		&output,
		snapshot,
		cliPaths{configPath: "/tmp/config.yaml"},
		100,
		26,
	)
	plain := stripTUIANSI(output.String())
	for _, expected := range []string{
		"Network detection",
		"Public IP",
		"203.0.113.8",
		"[SG]",
		"Intranet IP",
		"192.168.1.23 (wlp2s0)",
		"PROXY 127.0.0.1:7890",
		"n refresh",
	} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("Dashboard does not contain %q:\n%s", expected, plain)
		}
	}
}

func TestTUINetworkCheckDiscardsResultFromOldRoute(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Settings.MixedPort = 7890
	model.coreRunning = true
	model.networkCheckActive = true

	_, command := model.Update(tuiNetworkResultMsg{
		route: "direct",
		info: tuiNetworkInfo{
			PublicIP:  "198.51.100.2",
			Route:     "DIRECT",
			CheckedAt: time.Now(),
		},
	})
	if command == nil {
		t.Fatal("route change did not schedule a fresh network check")
	}
	if !model.networkCheckActive {
		t.Fatal("fresh network check is not marked active")
	}
	if model.snapshot.Network.PublicIP != "" {
		t.Fatal("stale direct-route IP replaced the proxy-route result")
	}
}

func TestTUIMemoryRefreshIntervalIsOneSecond(t *testing.T) {
	if tuiMemoryRefreshInterval != time.Second {
		t.Fatalf("memory refresh interval = %s, want 1s", tuiMemoryRefreshInterval)
	}
	if tuiRefreshInterval != time.Second {
		t.Fatalf("TUI tick interval = %s, want 1s", tuiRefreshInterval)
	}
}

func TestTUIParsesLinuxSystemMemoryUsingMemAvailable(t *testing.T) {
	total, available, err := parseTUISystemMemory([]byte(`MemTotal:       8192000 kB
MemFree:        1000000 kB
MemAvailable:   3072000 kB
Buffers:         100000 kB
Cached:          500000 kB
`))
	if err != nil {
		t.Fatal(err)
	}
	if total != 8192000*1024 || available != 3072000*1024 {
		t.Fatalf("memory = total %d available %d", total, available)
	}
}

func TestTUIParsesLinuxSystemMemoryFallback(t *testing.T) {
	total, available, err := parseTUISystemMemory([]byte(`MemTotal: 4096 kB
MemFree: 512 kB
Buffers: 128 kB
Cached: 1024 kB
`))
	if err != nil {
		t.Fatal(err)
	}
	if total != 4096*1024 || available != (512+128+1024)*1024 {
		t.Fatalf("fallback memory = total %d available %d", total, available)
	}
}

func TestTUIReadsProcessRSSFromLinuxStatm(t *testing.T) {
	path := filepath.Join(t.TempDir(), "statm")
	if err := os.WriteFile(path, []byte("100 25 3 2 0 0 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	rss, err := readTUIProcessRSS(path, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if rss != 25*4096 {
		t.Fatalf("RSS = %d, want %d", rss, 25*4096)
	}
}

func TestTUILocalMemorySampleIncludesSystemProcessAndGoHeap(t *testing.T) {
	info := sampleTUIMemory(tuiMemoryInfo{}, false)
	if info.SystemTotal == 0 || info.SystemUsed == 0 {
		t.Fatalf("system memory was not sampled: %+v", info)
	}
	if info.ProcessRSS == 0 {
		t.Fatalf("process RSS was not sampled: %+v", info)
	}
	if info.GoHeap == 0 {
		t.Fatalf("Go heap was not sampled: %+v", info)
	}
	if info.ExternalCore {
		t.Fatal("embedded sample was marked as external")
	}
	if info.UpdatedAt.IsZero() {
		t.Fatal("memory sample has no timestamp")
	}
}

func TestTUIMemoryRefreshPreservesNewerExternalCoreSample(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, false)
	newer := time.Now()
	model.memoryRefreshActive = true
	model.snapshot.Memory = tuiMemoryInfo{
		CoreRSS:     222 << 20,
		CoreUpdated: newer,
	}
	_, _ = model.Update(tuiMemoryResultMsg{
		info: tuiMemoryInfo{
			SystemTotal: 8 << 30,
			SystemUsed:  4 << 30,
			ProcessRSS:  20 << 20,
			CoreRSS:     111 << 20,
			CoreUpdated: newer.Add(-time.Second),
			UpdatedAt:   newer,
		},
	})
	if model.memoryRefreshActive {
		t.Fatal("memory refresh remained active after receiving a sample")
	}
	if model.snapshot.Memory.CoreRSS != 222<<20 {
		t.Fatalf("newer external Core RSS was overwritten: %+v", model.snapshot.Memory)
	}
	if model.snapshot.Memory.SystemTotal != 8<<30 {
		t.Fatalf("local memory sample was not applied: %+v", model.snapshot.Memory)
	}
}

func TestTUIExternalCoreMemoryErrorKeepsLastRSS(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, false)
	model.snapshot.Memory.CoreRSS = 256 << 20
	_, command := model.Update(tuiCoreMemoryMsg{
		update: tuiCoreMemoryUpdate{
			Error:     "temporary disconnect",
			UpdatedAt: time.Now(),
		},
	})
	if command != nil {
		t.Fatal("model without a monitor scheduled another monitor read")
	}
	if model.snapshot.Memory.CoreRSS != 256<<20 {
		t.Fatalf("temporary error erased the last Core RSS: %+v", model.snapshot.Memory)
	}
	if model.snapshot.Memory.CoreError != "temporary disconnect" {
		t.Fatalf("Core memory error = %q", model.snapshot.Memory.CoreError)
	}
}

func TestTUIExternalCoreMemoryStreamIgnoresInitialZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/memory" {
			t.Fatalf("memory path = %q", request.URL.Path)
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("test response does not support flushing")
		}
		_, _ = io.WriteString(w, "{\"inuse\":0,\"oslimit\":0}\n")
		flusher.Flush()
		_, _ = io.WriteString(w, "{\"inuse\":98765432,\"oslimit\":0}\n")
		flusher.Flush()
		<-request.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	values := make(chan uint64, 1)
	errors := make(chan error, 1)
	go func() {
		errors <- streamTUICoreMemory(
			ctx,
			controllerClient{
				options: controllerOptions{address: server.URL},
				client:  server.Client(),
			},
			func(value uint64) {
				values <- value
				cancel()
			},
		)
	}()
	select {
	case value := <-values:
		if value != 98765432 {
			t.Fatalf("external Core RSS = %d", value)
		}
	case <-time.After(time.Second):
		t.Fatal("external Core memory stream did not produce a value")
	}
	select {
	case <-errors:
	case <-time.After(time.Second):
		t.Fatal("external Core memory stream did not stop after cancellation")
	}
}

func TestTUIDashboardRendersEmbeddedAndExternalMemory(t *testing.T) {
	base := populatedTUISnapshot(tuiPageDashboard)
	base.Memory = tuiMemoryInfo{
		SystemTotal: 8 << 30,
		SystemUsed:  4 << 30,
		ProcessRSS:  120 << 20,
		GoHeap:      48 << 20,
		UpdatedAt:   time.Now(),
	}
	var embedded strings.Builder
	drawTUIDashboard(
		&embedded,
		base,
		cliPaths{configPath: "/tmp/config.yaml"},
		100,
		26,
	)
	embeddedPlain := stripTUIANSI(embedded.String())
	for _, expected := range []string{
		"memory refresh 1s",
		"System memory 4.0 GB / 8.0 GB  50.0%",
		"CLI + Mihomo  120.0 MB RSS · shared process",
		"Go heap       48.0 MB",
	} {
		if !strings.Contains(embeddedPlain, expected) {
			t.Fatalf("embedded Dashboard does not contain %q:\n%s", expected, embeddedPlain)
		}
	}

	externalSnapshot := base
	externalSnapshot.ExternalCore = true
	externalSnapshot.Memory.ExternalCore = true
	externalSnapshot.Memory.CoreRSS = 256 << 20
	externalSnapshot.Memory.CoreUpdated = time.Now()
	var external strings.Builder
	drawTUIDashboard(
		&external,
		externalSnapshot,
		cliPaths{configPath: "/tmp/config.yaml"},
		100,
		26,
	)
	externalPlain := stripTUIANSI(external.String())
	for _, expected := range []string{
		"TUI process   120.0 MB",
		"External Core 256.0 MB",
	} {
		if !strings.Contains(externalPlain, expected) {
			t.Fatalf("external Dashboard does not contain %q:\n%s", expected, externalPlain)
		}
	}
}

func TestTUIProxiesExposeSelectedAndWholeGroupDelayTests(t *testing.T) {
	snapshot := populatedTUISnapshot(tuiPageProxies)
	snapshot.FocusSidebar = false
	snapshot.SelectedGroup = 0
	snapshot.SelectedNode = 0
	snapshot.Groups = []tuiGroup{{
		Name:  "Proxy",
		Type:  "Selector",
		Now:   "fast",
		Nodes: []string{"fast", "slow", "dead", "new"},
		Delays: map[string]int{
			"fast": 18,
			"slow": -2,
			"dead": -1,
		},
	}}
	var output strings.Builder
	drawTUIProxies(&output, snapshot, 100, 24)
	plain := stripTUIANSI(output.String())
	for _, expected := range []string{
		"↑↓/ws group",
		"d test group",
		"Enter apply",
		"18 ms",
		"Testing...",
		"Timeout · d retry",
		"[d test]",
	} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("Proxies does not contain %q:\n%s", expected, plain)
		}
	}
	key, ok := tuiKeyFromTea(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'A'},
	})
	if !ok || key != tuiKeyDelayTestAll {
		t.Fatalf("A key = (%v, %v)", key, ok)
	}
}

func TestTUIStagesMixedPortUntilCoreStart(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{configPath: filepath.Join(t.TempDir(), "missing.yaml")},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.FocusSidebar = false
	model.snapshot.Settings.MixedPort = 7890
	model.inputMode = tuiInputMixedPort
	model.inputValue = []rune("17890")

	command := model.submitInput()
	if command != nil {
		t.Fatal("staging a stopped core port unexpectedly started an asynchronous operation")
	}
	if model.coreRunning {
		t.Fatal("core was marked running while staging a port")
	}
	if model.pendingMixedPort == nil || *model.pendingMixedPort != 17890 {
		t.Fatalf("pending mixed port = %v", model.pendingMixedPort)
	}
	if model.snapshot.Settings.MixedPort != 17890 {
		t.Fatalf("displayed mixed port = %d", model.snapshot.Settings.MixedPort)
	}

	model.refreshSequence = 1
	model.refreshInFlight = true
	_, _ = model.Update(tuiRefreshResultMsg{
		sequence: 1,
		snapshot: tuiSnapshot{
			Status:    "Connected",
			UpdatedAt: time.Now(),
			Settings:  tuiSettings{MixedPort: 0},
		},
	})
	if model.snapshot.Settings.MixedPort != 17890 {
		t.Fatalf("refresh discarded staged mixed port: %d", model.snapshot.Settings.MixedPort)
	}
}

func TestTUIStagesPreStartSettings(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{configPath: filepath.Join(t.TempDir(), "missing.yaml")},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageTools
	model.snapshot.FocusSidebar = false
	model.snapshot.Settings = tuiSettings{
		Mode:      "rule",
		MixedPort: 17890,
		AllowLAN:  false,
		IPv6:      true,
		LogLevel:  "info",
	}

	for _, key := range []tuiKey{
		tuiKeyTun,
		tuiKeyAllowLAN,
		tuiKeyIPv6,
		tuiKeyMode,
		tuiKeyLogLevel,
	} {
		if command := model.handleKey(key); command != nil {
			t.Fatalf("staging key %v unexpectedly returned an operation", key)
		}
	}
	if model.coreRunning {
		t.Fatal("staging settings started the core")
	}
	if model.stagedSettings == nil {
		t.Fatal("settings were not staged")
	}
	if !model.stagedSettings.TunEnabled ||
		!model.stagedSettings.AllowLAN ||
		model.stagedSettings.IPv6 ||
		model.stagedSettings.Mode != "global" ||
		model.stagedSettings.LogLevel != "debug" {
		t.Fatalf("unexpected staged settings: %+v", *model.stagedSettings)
	}

	model.refreshSequence = 1
	model.refreshInFlight = true
	_, _ = model.Update(tuiRefreshResultMsg{
		sequence: 1,
		snapshot: tuiSnapshot{
			Status:    "Connected",
			UpdatedAt: time.Now(),
			Settings: tuiSettings{
				Mode:       "direct",
				MixedPort:  0,
				AllowLAN:   false,
				IPv6:       true,
				LogLevel:   "silent",
				TunEnabled: false,
			},
		},
	})
	if model.snapshot.Settings != *model.stagedSettings {
		t.Fatalf("refresh discarded staged settings: %+v", model.snapshot.Settings)
	}
}

func TestTUIImportsSubscriptionBeforeCoreStart(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.UserAgent() != tuiSubscriptionUserAgent {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !strings.Contains(request.Header.Get("Accept"), "application/yaml") {
			http.Error(w, "missing YAML accept header", http.StatusNotAcceptable)
			return
		}
		_, _ = io.WriteString(w, `mixed-port: 17891
mode: rule
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,PROXY
`)
	}))
	defer server.Close()

	directory := t.TempDir()
	model := newTUIModel(
		controllerClient{},
		cliPaths{
			homeDir:    directory,
			configPath: filepath.Join(directory, "config.yaml"),
		},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageProfiles
	model.inputMode = tuiInputSubscription
	model.inputValue = []rune(server.URL)
	command := model.submitInput()
	if command == nil {
		t.Fatal("subscription import did not return an operation")
	}
	_, _ = model.Update(command())
	if model.coreRunning {
		t.Fatal("subscription import started the core")
	}
	if model.snapshot.Status !=
		"Subscription profile linked; U refreshes from the saved URL" {
		t.Fatalf("subscription status = %q", model.snapshot.Status)
	}
	found := false
	for _, profile := range model.snapshot.Profiles {
		if strings.HasPrefix(profile.Name, "profile-") {
			if profile.SubscriptionURL != server.URL {
				t.Fatalf(
					"downloaded profile source = %q, want %q",
					profile.SubscriptionURL,
					server.URL,
				)
			}
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("downloaded profile missing: %+v", model.snapshot.Profiles)
	}
}

func TestTUIUpdatesSubscriptionAndPreservesLocalSettings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `mixed-port: 9999
mode: rule
allow-lan: false
ipv6: false
unified-delay: true
tcp-concurrent: true
log-level: info
tun:
  enable: false
proxies:
  - name: NEW-NODE
    type: direct
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - NEW-NODE
rules:
  - MATCH,PROXY
`)
	}))
	defer server.Close()

	directory := t.TempDir()
	activePath := filepath.Join(directory, "config.yaml")
	profilePath := filepath.Join(directory, "work.yaml")
	if err := os.WriteFile(activePath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	original := `mixed-port: 17890
mode: global
allow-lan: true
ipv6: true
unified-delay: false
tcp-concurrent: false
log-level: debug
tun:
  enable: true
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,PROXY
`
	if err := os.WriteFile(profilePath, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := rememberTUISubscriptionSource(directory, profilePath, server.URL); err != nil {
		t.Fatal(err)
	}

	model := newTUIModel(
		controllerClient{},
		cliPaths{homeDir: directory, configPath: activePath},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageProfiles
	model.snapshot.FocusSidebar = false
	model.snapshot.Profiles = []tuiProfile{{
		Name:            "work.yaml",
		Path:            profilePath,
		SubscriptionURL: server.URL,
	}}
	model.snapshot.SelectedRow = 0

	command := model.handleKey(tuiKeyUpdateProfile)
	if command == nil {
		t.Fatal("subscription update did not return an operation")
	}
	_, _ = model.Update(command())

	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	for _, expected := range []string{
		"name: NEW-NODE",
		"mixed-port: 17890",
		"mode: global",
		"allow-lan: true",
		"ipv6: true",
		"unified-delay: false",
		"tcp-concurrent: false",
		"log-level: debug",
		"enable: true",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("updated profile does not contain %q:\n%s", expected, output)
		}
	}
	info, err := os.Stat(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("updated profile mode = %o, want 640", info.Mode().Perm())
	}
	if model.snapshot.Status != "Subscription refreshed: work.yaml" {
		t.Fatalf("subscription update status = %q", model.snapshot.Status)
	}
}

func TestTUIUnlinkedProfileDoesNotPretendToRefreshSubscription(t *testing.T) {
	directory := t.TempDir()
	activePath := filepath.Join(directory, "config.yaml")
	profilePath := filepath.Join(directory, "legacy.yaml")
	for _, path := range []string{activePath, profilePath} {
		if err := os.WriteFile(path, []byte(defaultTUIConfig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	model := newTUIModel(
		controllerClient{},
		cliPaths{homeDir: directory, configPath: activePath},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageProfiles
	model.snapshot.FocusSidebar = false
	model.snapshot.Profiles = []tuiProfile{{
		Name: "legacy.yaml",
		Path: profilePath,
	}}
	model.snapshot.SelectedRow = 0

	if command := model.handleKey(tuiKeyUpdateProfile); command != nil {
		t.Fatal("unlinked profile unexpectedly started a subscription refresh")
	}
	if model.inputMode != tuiInputNone {
		t.Fatalf("unlinked profile opened manual URL input: %d", model.inputMode)
	}
	if !strings.Contains(model.snapshot.Status, "not linked to a subscription") {
		t.Fatalf("unlinked profile status = %q", model.snapshot.Status)
	}
}

func TestTUIActiveSubscriptionUpdateReloadsWithoutStartingListeners(t *testing.T) {
	mixedPort := freeTUITestPort(t)
	controllerPort := freeTUITestPort(t)
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	original := fmt.Appendf(nil, `mixed-port: %d
mode: rule
log-level: silent
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,PROXY
`, mixedPort)
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `mixed-port: 9999
mode: global
log-level: info
proxies:
  - name: UPDATED-DIRECT
    type: direct
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - UPDATED-DIRECT
rules:
  - MATCH,PROXY
`)
	}))
	defer server.Close()

	paths := cliPaths{homeDir: directory, configPath: configPath}
	controllerAddress := fmt.Sprintf("127.0.0.1:%d", controllerPort)
	setupParams, err := initializeCore(
		paths,
		"https://www.gstatic.com/generate_204",
		controllerAddress,
		"",
		"",
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		handleShutdown()
	})
	client := controllerClient{
		options: controllerOptions{address: controllerAddress},
		client:  &http.Client{Timeout: time.Second},
	}
	if err := waitForController(client, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := rememberTUISubscriptionSource(directory, configPath, server.URL); err != nil {
		t.Fatal(err)
	}

	model := newTUIModel(client, paths, setupParams, true)
	model.snapshot.Page = tuiPageProfiles
	model.snapshot.FocusSidebar = false
	model.snapshot.Profiles = []tuiProfile{{
		Name:            "config.yaml",
		Path:            configPath,
		Current:         true,
		SubscriptionURL: server.URL,
	}}
	model.snapshot.SelectedRow = 0
	command := model.handleKey(tuiKeyUpdateProfile)
	if command == nil {
		t.Fatal("active subscription update did not return an operation")
	}
	_, _ = model.Update(command())

	if model.snapshot.Status !=
		"Subscription refreshed and hot-reloaded: config.yaml" {
		t.Fatalf("active update status = %q", model.snapshot.Status)
	}
	if model.coreRunning || canConnectTUITestPort(mixedPort) {
		t.Fatal("active subscription update unexpectedly started proxy listeners")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	if !strings.Contains(output, "name: UPDATED-DIRECT") ||
		!strings.Contains(output, fmt.Sprintf("mixed-port: %d", mixedPort)) ||
		!strings.Contains(output, "mode: rule") {
		t.Fatalf("active updated profile did not preserve settings:\n%s", output)
	}
	if err := waitForController(client, 2*time.Second); err != nil {
		t.Fatalf("controller did not recover after active profile reload: %v", err)
	}
	proxyData, err := client.request(http.MethodGet, "/proxies", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(proxyData, []byte("UPDATED-DIRECT")) {
		t.Fatalf("active core did not reload updated proxies: %s", proxyData)
	}
}

func TestTUIBackgroundServiceHotReloadsSubscriptionWithoutStoppingListeners(
	t *testing.T,
) {
	mixedPort := freeTUITestPort(t)
	directory, err := os.MkdirTemp("/tmp", "flclash-service-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(directory)
	})
	configPath := filepath.Join(directory, "config.yaml")
	original := fmt.Appendf(nil, `mixed-port: %d
mode: rule
log-level: silent
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,PROXY
`, mixedPort)
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	subscription := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, `mixed-port: 9999
mode: global
log-level: info
proxies:
  - name: UPDATED-DIRECT
    type: direct
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - UPDATED-DIRECT
rules:
  - MATCH,PROXY
`)
		},
	))
	defer subscription.Close()
	if err := rememberTUISubscriptionSource(
		directory,
		configPath,
		subscription.URL,
	); err != nil {
		t.Fatal(err)
	}

	paths := cliPaths{homeDir: directory, configPath: configPath}
	serviceDone := make(chan error, 1)
	go func() {
		serviceDone <- runTUIService(
			paths,
			"https://www.gstatic.com/generate_204",
		)
	}()
	service := newTUIServiceClient(directory)
	var status tuiServiceStatus
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		status, err = service.status()
		if err == nil {
			break
		}
		select {
		case serviceErr := <-serviceDone:
			t.Fatalf("background service exited before ready: %v", serviceErr)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !status.OK {
		select {
		case serviceErr := <-serviceDone:
			t.Fatalf("background service did not become ready: %v", serviceErr)
		default:
			t.Fatal("background service did not become ready")
		}
	}
	t.Cleanup(func() {
		_ = service.shutdown()
		select {
		case <-serviceDone:
		case <-time.After(2 * time.Second):
		}
	})
	if status.Running {
		t.Fatal("new background service unexpectedly started listeners")
	}
	status, err = service.start()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Running || !waitForTUITestPort(mixedPort, true, 2*time.Second) {
		t.Fatal("background service did not start the mixed listener")
	}
	client := controllerClient{
		options: controllerOptions{unixSocket: status.CoreSocket},
		client: controllerHTTPClientForOptions(
			controllerOptions{unixSocket: status.CoreSocket},
			time.Second,
		),
	}
	model := newTUIModel(client, paths, nil, true)
	model.service = service
	model.coreRunning = true
	model.shutdown()
	detachedStatus, err := service.status()
	if err != nil || !detachedStatus.Running {
		t.Fatalf("q-style TUI detach stopped the background service: %+v, %v", detachedStatus, err)
	}
	model.snapshot.Page = tuiPageProfiles
	model.snapshot.FocusSidebar = false
	model.snapshot.Profiles = []tuiProfile{{
		Name:            "config.yaml",
		Path:            configPath,
		Current:         true,
		SubscriptionURL: subscription.URL,
	}}
	model.snapshot.SelectedRow = 0
	command := model.handleKey(tuiKeyUpdateProfile)
	if command == nil {
		t.Fatal("linked subscription did not start a refresh")
	}
	_, _ = model.Update(command())
	if model.snapshot.Status !=
		"Subscription refreshed and hot-reloaded: config.yaml" {
		t.Fatalf("refresh status = %q", model.snapshot.Status)
	}
	if !model.coreRunning || !waitForTUITestPort(mixedPort, true, time.Second) {
		t.Fatal("subscription hot-reload stopped the running listener")
	}
	proxyData, err := client.request(http.MethodGet, "/proxies", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(proxyData, []byte("UPDATED-DIRECT")) {
		t.Fatalf("background core did not load refreshed nodes: %s", proxyData)
	}
	beforeEdit, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	edited := fmt.Appendf(nil, `mixed-port: %d
mode: rule
log-level: silent
proxies:
  - name: EDITED-DIRECT
    type: direct
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - EDITED-DIRECT
rules:
  - MATCH,PROXY
`, mixedPort)
	if err := os.WriteFile(configPath, edited, info.Mode()); err != nil {
		t.Fatal(err)
	}
	model.editorPath = configPath
	model.editorBackup = tuiProfileBackup{data: beforeEdit, mode: info.Mode()}
	_, editorCommand := model.Update(tuiEditorResultMsg{})
	if editorCommand == nil {
		t.Fatal("active config edit did not schedule a hot-reload")
	}
	_, _ = model.Update(editorCommand())
	if model.snapshot.Status != "Configuration saved and hot-reloaded" {
		t.Fatalf("editor hot-reload status = %q", model.snapshot.Status)
	}
	proxyData, err = client.request(http.MethodGet, "/proxies", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(proxyData, []byte("EDITED-DIRECT")) {
		t.Fatalf("background core did not hot-reload edited config: %s", proxyData)
	}
	if !waitForTUITestPort(mixedPort, true, time.Second) {
		t.Fatal("configuration edit interrupted the mixed listener")
	}
	model.stopServiceOnExit = true
	model.shutdown()
	if !waitForTUITestPort(mixedPort, false, 2*time.Second) {
		t.Fatal("Ctrl+C-style shutdown did not stop the mixed listener")
	}
	serviceStopped := false
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := service.status(); err != nil {
			serviceStopped = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !serviceStopped {
		t.Fatal("Ctrl+C-style shutdown left the background service running")
	}
}

func TestTUISubscriptionUpdateRejectsInvalidResponseWithoutChangingProfile(
	t *testing.T,
) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "this is not a Mihomo configuration")
	}))
	defer server.Close()

	directory := t.TempDir()
	profilePath := filepath.Join(directory, "work.yaml")
	original := []byte(defaultTUIConfig)
	if err := os.WriteFile(profilePath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := updateTUISubscriptionProfile(
		directory,
		profilePath,
		server.URL,
	); err == nil {
		t.Fatal("invalid subscription response was accepted")
	}
	after, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("invalid update changed profile:\n%s", after)
	}
}

func TestTUIProfileRenameMovesSavedSubscriptionSource(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "old.yaml")
	if err := os.WriteFile(sourcePath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	const sourceURL = "https://example.test/subscription"
	if err := rememberTUISubscriptionSource(directory, sourcePath, sourceURL); err != nil {
		t.Fatal(err)
	}

	newPath, err := renameTUIProfile(directory, sourcePath, "new.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sources := loadTUISubscriptionSources(directory)
	if sources["old.yaml"] != "" || sources["new.yaml"] != sourceURL {
		t.Fatalf("renamed subscription sources = %+v", sources)
	}
	if filepath.Base(newPath) != "new.yaml" {
		t.Fatalf("renamed path = %q", newPath)
	}
}

func TestTUIInvalidProfileEditRestoresOriginalFile(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	original := []byte(defaultTUIConfig)
	if err := os.WriteFile(configPath, original, 0o640); err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(
		controllerClient{},
		cliPaths{homeDir: directory, configPath: configPath},
		nil,
		true,
	)
	model.editorPath = configPath
	model.editorBackup = tuiProfileBackup{data: original, mode: 0o640}
	if err := os.WriteFile(configPath, []byte("not a mihomo profile"), 0o640); err != nil {
		t.Fatal(err)
	}
	_, command := model.Update(tuiEditorResultMsg{})
	if command != nil {
		t.Fatal("invalid edit unexpectedly scheduled a hot-reload")
	}
	restored, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("invalid edit was not rolled back:\n%s", restored)
	}
	if !strings.Contains(model.snapshot.Status, "original restored") {
		t.Fatalf("invalid edit status = %q", model.snapshot.Status)
	}
}

func TestTUISubscriptionSourceRejectsProfileOutsideDataDirectory(t *testing.T) {
	directory := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "outside.yaml")
	if err := rememberTUISubscriptionSource(
		directory,
		outsidePath,
		"https://example.test/subscription",
	); err == nil {
		t.Fatal("subscription source accepted a profile outside the data directory")
	}
}

func TestPersistTUISettingsUpdatesYAMLWithoutDroppingProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := `# keep this profile comment
mixed-port: 7890
mode: rule
log-level: info
proxies:
  - name: example
    type: direct
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - example
rules:
  - MATCH,PROXY
`
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	settings := tuiSettings{
		Mode:          "global",
		MixedPort:     17890,
		AllowLAN:      true,
		IPv6:          false,
		UnifiedDelay:  true,
		TCPConcurrent: true,
		LogLevel:      "debug",
		TunEnabled:    true,
	}
	if err := persistTUISettings(path, settings); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	for _, expected := range []string{
		"# keep this profile comment",
		"mixed-port: 17890",
		"mode: global",
		"allow-lan: true",
		"ipv6: false",
		"unified-delay: true",
		"tcp-concurrent: true",
		"log-level: debug",
		"enable: true",
		"name: example",
		"MATCH,PROXY",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("persisted YAML does not contain %q:\n%s", expected, output)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("persisted mode = %o, want 640", got)
	}
}

func TestTUIStateRestoresActiveProfileAndProxySelections(t *testing.T) {
	directory := t.TempDir()
	defaultPath := filepath.Join(directory, "config.yaml")
	activePath := filepath.Join(directory, "work.yaml")
	for _, path := range []string{defaultPath, activePath} {
		if err := os.WriteFile(path, []byte(defaultTUIConfig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	activePaths := cliPaths{
		homeDir:    directory,
		configPath: activePath,
	}
	if err := rememberTUIActiveProfile(activePaths); err != nil {
		t.Fatal(err)
	}
	if err := rememberTUIProxySelection(directory, "PROXY", "Tokyo"); err != nil {
		t.Fatal(err)
	}
	if err := rememberTUIProxySelection(directory, "AUTO", "Singapore"); err != nil {
		t.Fatal(err)
	}

	restored, err := restoreTUIActiveProfile(cliPaths{
		homeDir:    directory,
		configPath: defaultPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.configPath != activePath {
		t.Fatalf("restored profile = %q, want %q", restored.configPath, activePath)
	}
	selected := loadTUISelectedProxies(directory)
	if selected["PROXY"] != "Tokyo" || selected["AUTO"] != "Singapore" {
		t.Fatalf("restored proxy selections = %+v", selected)
	}
	info, err := os.Stat(filepath.Join(directory, tuiStateFilename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("saved state mode = %o, want 600", info.Mode().Perm())
	}
}

func TestTUIStateRejectsInvalidSavedProfileAndRecovers(t *testing.T) {
	directory := t.TempDir()
	defaultPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(defaultPath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(directory, tuiStateFilename)
	if err := os.WriteFile(
		statePath,
		[]byte(`{"version":1,"active_profile":"../outside.yaml"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	paths := cliPaths{homeDir: directory, configPath: defaultPath}
	restored, err := restoreTUIActiveProfile(paths)
	if err == nil {
		t.Fatal("unsafe saved profile was accepted")
	}
	if restored != paths {
		t.Fatalf("invalid state changed paths: %+v", restored)
	}
	if err := rememberTUIActiveProfile(paths); err != nil {
		t.Fatal(err)
	}
	recovered, err := restoreTUIActiveProfile(paths)
	if err != nil || recovered.configPath != defaultPath {
		t.Fatalf("state did not recover: paths=%+v err=%v", recovered, err)
	}
}

func TestTUIStoppedSettingsAreSavedImmediately(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(configPath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(
		controllerClient{},
		cliPaths{homeDir: directory, configPath: configPath},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageTools
	model.snapshot.FocusSidebar = false

	if command := model.handleKey(tuiKeyMode); command != nil {
		t.Fatal("stopped setting unexpectedly started an asynchronous operation")
	}
	if model.settingsDirty {
		t.Fatal("successfully saved stopped setting remained dirty")
	}
	if !strings.Contains(model.snapshot.Status, "saved for next launch") {
		t.Fatalf("save confirmation missing: %q", model.snapshot.Status)
	}
	reloaded := loadTUIConfiguredSettings(configPath, true)
	if reloaded == nil || reloaded.Mode != "global" {
		t.Fatalf("saved mode was not restored: %+v", reloaded)
	}
}

func TestMergeTUIRefreshSurfacesControllerErrors(t *testing.T) {
	current := tuiSnapshot{
		Status:    "Settings staged",
		UpdatedAt: time.Now(),
	}
	refreshed := tuiSnapshot{
		Status:    "Controller unavailable: connection refused",
		UpdatedAt: time.Now(),
	}
	merged := mergeTUIRefresh(current, refreshed)
	if merged.Status != refreshed.Status {
		t.Fatalf("controller error was hidden by stale action status: %q", merged.Status)
	}
}

func TestTUIInitializationDefersProxyListener(t *testing.T) {
	mixedPort := freeTUITestPort(t)
	controllerPort := freeTUITestPort(t)
	directory := t.TempDir()
	paths := cliPaths{
		homeDir:    directory,
		configPath: filepath.Join(directory, "config.yaml"),
	}
	configData := fmt.Appendf(nil, `mixed-port: %d
allow-lan: false
mode: rule
log-level: silent
unified-delay: true
tcp-concurrent: true
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,PROXY
`, mixedPort)
	if err := os.WriteFile(paths.configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}

	controllerAddress := fmt.Sprintf("127.0.0.1:%d", controllerPort)
	setupParams, err := initializeCore(
		paths,
		"https://www.gstatic.com/generate_204",
		controllerAddress,
		"",
		"",
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		handleShutdown()
	})
	client := controllerClient{
		options: controllerOptions{address: controllerAddress},
		client:  &http.Client{Timeout: time.Second},
	}
	if err := waitForController(client, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if canConnectTUITestPort(mixedPort) {
		t.Fatalf("mixed port %d was occupied before the TUI start action", mixedPort)
	}
	if isRunning {
		t.Fatal("core listener state is running before the TUI start action")
	}
	if !currentConfig.General.UnifiedDelay ||
		!currentConfig.General.TCPConcurrent {
		t.Fatalf(
			"FlClash delay defaults were not applied: %+v",
			currentConfig.General,
		)
	}

	model := newTUIModel(client, paths, setupParams, true)
	model.snapshot.Page = tuiPageTools
	model.snapshot.FocusSidebar = false
	initialIPv6 := model.snapshot.Settings.IPv6
	_ = model.handleKey(tuiKeyTun)
	if message := stageTUICoreSettings(*model.stagedSettings); message != "" {
		t.Fatalf("stage TUN setting: %s", message)
	}
	if !currentConfig.General.Tun.Enable {
		t.Fatal("TUN setting was not staged in the core")
	}
	if canConnectTUITestPort(mixedPort) {
		t.Fatalf("staging TUN occupied mixed port %d", mixedPort)
	}
	_ = model.handleKey(tuiKeyTun)
	_ = model.handleKey(tuiKeyAllowLAN)
	_ = model.handleKey(tuiKeyIPv6)
	_ = model.handleKey(tuiKeyMode)
	model.systemProxyToggle = func(snapshot *tuiSnapshot) bool {
		snapshot.Settings.SystemProxy = true
		snapshot.Status = "System proxy enabled"
		return true
	}
	startCommand := model.handleKey(tuiKeySystemProxy)
	if startCommand == nil {
		t.Fatal("system proxy did not return an automatic start operation")
	}
	_, _ = model.Update(startCommand())
	if !model.coreRunning {
		t.Fatalf("system proxy did not start the core: %s", model.snapshot.Status)
	}
	if !model.snapshot.Settings.SystemProxy || !model.systemProxyManaged {
		t.Fatalf("system proxy was not enabled after automatic start: %+v", model.snapshot.Settings)
	}
	if model.pendingMixedPort != nil {
		t.Fatalf("pending mixed port was not applied: %d", *model.pendingMixedPort)
	}
	if model.stagedSettings != nil {
		t.Fatalf("staged settings were not applied: %+v", *model.stagedSettings)
	}
	if !currentConfig.General.AllowLan ||
		currentConfig.General.IPv6 == initialIPv6 ||
		currentConfig.General.Mode.String() != "global" ||
		!currentConfig.General.UnifiedDelay ||
		!currentConfig.General.TCPConcurrent {
		t.Fatalf("staged core settings were not applied: %+v", currentConfig.General)
	}
	deadline := time.Now().Add(time.Second)
	for !canConnectTUITestPort(mixedPort) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !canConnectTUITestPort(mixedPort) {
		t.Fatalf("mixed port %d did not open after automatic service startup", mixedPort)
	}

	stopCommand := model.handleKey(tuiKeyCoreToggle)
	if stopCommand == nil {
		t.Fatal("service stop did not return an operation")
	}
	_, _ = model.Update(stopCommand())
	deadline = time.Now().Add(time.Second)
	for canConnectTUITestPort(mixedPort) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if model.coreRunning || canConnectTUITestPort(mixedPort) {
		t.Fatalf("service did not stop before rollback test: %s", model.snapshot.Status)
	}

	model.systemProxyToggle = func(snapshot *tuiSnapshot) bool {
		snapshot.Status = "System proxy update failed: simulated"
		return false
	}
	failedCommand := model.handleKey(tuiKeySystemProxy)
	if failedCommand == nil {
		t.Fatal("failed system proxy action did not start an operation")
	}
	_, _ = model.Update(failedCommand())
	if model.coreRunning || canConnectTUITestPort(mixedPort) {
		t.Fatalf("failed system proxy action left Service running: %s", model.snapshot.Status)
	}
	if !strings.Contains(model.snapshot.Status, "rolled back") {
		t.Fatalf("rollback status = %q", model.snapshot.Status)
	}
}

func TestControllerClientUsesUnixSocketWithoutTCP(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "controller.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" {
			http.NotFound(w, request)
			return
		}
		_, _ = io.WriteString(w, `{"hello":"unix"}`)
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})

	options := controllerOptions{unixSocket: socketPath}
	client := controllerClient{
		options: options,
		client:  controllerHTTPClientForOptions(options, time.Second),
	}
	data, err := client.request(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != `{"hello":"unix"}` {
		t.Fatalf("Unix controller response = %q", got)
	}
}

func freeTUITestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func canConnectTUITestPort(port int) bool {
	connection, err := net.DialTimeout(
		"tcp4",
		fmt.Sprintf("127.0.0.1:%d", port),
		50*time.Millisecond,
	)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func waitForTUITestPort(port int, open bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if canConnectTUITestPort(port) == open {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return canConnectTUITestPort(port) == open
}

func TestBubbleTeaProcessesBurstNavigationKeys(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{},
		nil,
		false,
	)
	_, command := model.Update(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("ssss"),
	})
	if command != nil {
		t.Fatal("navigation burst unexpectedly returned a command")
	}
	if model.snapshot.SelectedMenu != int(tuiPageConnections) {
		t.Fatalf("selected menu = %d, want connections", model.snapshot.SelectedMenu)
	}
}

func assertTUIUsesCRLF(t *testing.T, output string) {
	t.Helper()
	for index, value := range []byte(output) {
		if value == '\n' && (index == 0 || output[index-1] != '\r') {
			t.Fatalf("TUI frame contains a bare LF at byte %d", index)
		}
	}
}

func populatedTUISnapshot(page tuiPage) tuiSnapshot {
	snapshot := tuiSnapshot{
		Page:      page,
		UpdatedAt: time.Now(),
		Settings: tuiSettings{
			Mode:      "rule",
			MixedPort: 7890,
			LogLevel:  "info",
		},
		Status: "Connected",
	}
	for index := 0; index < 24; index++ {
		nodes := make([]string, 24)
		for nodeIndex := range nodes {
			nodes[nodeIndex] = fmt.Sprintf("node-%02d-%02d", index, nodeIndex)
		}
		snapshot.Groups = append(snapshot.Groups, tuiGroup{
			Name:  fmt.Sprintf("group-%02d", index),
			Type:  "Selector",
			Now:   nodes[18],
			Nodes: nodes,
		})
		snapshot.Connections = append(snapshot.Connections, tuiConnection{
			ID:       fmt.Sprintf("connection-%02d", index),
			Host:     fmt.Sprintf("service-%02d.example.com", index),
			Process:  "example-process",
			Network:  "tcp",
			Chain:    "PROXY",
			Upload:   int64(index * 1024),
			Download: int64(index * 2048),
		})
		snapshot.Requests = append(snapshot.Requests, tuiRequest{
			tuiConnection: tuiConnection{
				ID:       fmt.Sprintf("request-%02d", index),
				Host:     fmt.Sprintf("request-%02d.example.com", index),
				Process:  "example-process",
				Network:  "tcp",
				Chain:    "PROXY",
				Upload:   int64(index * 1024),
				Download: int64(index * 2048),
			},
			LastSeen: time.Now().Add(-time.Duration(index) * time.Second),
			Active:   index%2 == 0,
		})
		snapshot.Profiles = append(snapshot.Profiles, tuiProfile{
			Name: fmt.Sprintf("profile-%02d.yaml", index),
			Path: fmt.Sprintf("/tmp/profile-%02d.yaml", index),
		})
		snapshot.Providers = append(snapshot.Providers, tuiProvider{
			Name:  fmt.Sprintf("provider-%02d", index),
			Type:  "HTTP",
			Count: index,
		})
		snapshot.Logs = append(snapshot.Logs, fmt.Sprintf("log line %02d", index))
	}
	snapshot.SelectedGroup = 18
	snapshot.SelectedNode = 18
	snapshot.SelectedConnection = 18
	snapshot.SelectedRow = 18
	snapshot.SelectedProvider = 18
	return snapshot
}

func stripTUIANSI(value string) string {
	var output strings.Builder
	for index := 0; index < len(value); {
		if value[index] != '\x1b' {
			output.WriteByte(value[index])
			index++
			continue
		}
		index++
		if index < len(value) && value[index] == '[' {
			index++
			for index < len(value) {
				code := value[index]
				index++
				if code >= '@' && code <= '~' {
					break
				}
			}
		}
	}
	return valueWithoutNewline(output.String())
}

func valueWithoutNewline(value string) string {
	return strings.TrimRight(value, "\r\n")
}

func TestTUIGroupAndSelectionMovement(t *testing.T) {
	if !isTUIGroup("Selector") || !isTUIGroup("urltest") || isTUIGroup("Direct") {
		t.Fatal("unexpected proxy group classification")
	}
	snapshot := tuiSnapshot{Groups: []tuiGroup{
		{Name: "A", Nodes: []string{"a", "b"}},
		{Name: "B", Now: "d", Nodes: []string{"c", "d"}},
	}}
	moveTUIGroup(&snapshot, -1)
	if snapshot.SelectedGroup != 1 || snapshot.SelectedNode != 1 {
		t.Fatalf("group movement = (%d, %d)", snapshot.SelectedGroup, snapshot.SelectedNode)
	}
	moveTUINode(&snapshot, 1)
	if snapshot.SelectedNode != 0 {
		t.Fatalf("node movement = %d", snapshot.SelectedNode)
	}
	snapshot.SelectedGroup = 99
	moveTUINode(&snapshot, 1)
	snapshot.SelectedGroup = -99
	moveTUIGroup(&snapshot, -1)
	if snapshot.SelectedGroup != 0 {
		t.Fatalf("out-of-range group movement = %d", snapshot.SelectedGroup)
	}
}

func TestTUIProxyGroupsFollowConfigurationOrder(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	configData := []byte(`proxy-groups:
  - name: Z-LAST-ALPHABETICALLY
    type: select
    proxies: [DIRECT]
  - name: A-FIRST-ALPHABETICALLY
    type: select
    proxies: [DIRECT]
  - name: M-MIDDLE
    type: select
    proxies: [DIRECT]
rules:
  - MATCH,DIRECT
`)
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	order := loadTUIProxyGroupOrder(configPath)
	groups := []tuiGroup{
		{Name: "A-FIRST-ALPHABETICALLY"},
		{Name: "M-MIDDLE"},
		{Name: "Z-LAST-ALPHABETICALLY"},
	}
	orderTUIGroups(groups, order)
	got := []string{groups[0].Name, groups[1].Name, groups[2].Name}
	want := []string{
		"Z-LAST-ALPHABETICALLY",
		"A-FIRST-ALPHABETICALLY",
		"M-MIDDLE",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("proxy group order = %v, want configuration order %v", got, want)
	}
}

func TestTUIVisibleRangeKeepsSelectionOnScreen(t *testing.T) {
	tests := []struct {
		total    int
		selected int
		limit    int
		start    int
		end      int
	}{
		{total: 100, selected: 0, limit: 10, start: 0, end: 10},
		{total: 100, selected: 50, limit: 10, start: 45, end: 55},
		{total: 100, selected: 99, limit: 10, start: 90, end: 100},
		{total: 4, selected: 3, limit: 10, start: 0, end: 4},
	}
	for _, test := range tests {
		start, end := tuiVisibleRange(test.total, test.selected, test.limit)
		if start != test.start || end != test.end {
			t.Fatalf("tuiVisibleRange(%d, %d, %d) = (%d, %d), want (%d, %d)",
				test.total, test.selected, test.limit, start, end, test.start, test.end)
		}
	}
}

func TestWrapTUIIndexNormalizesStaleSelections(t *testing.T) {
	tests := []struct {
		current int
		delta   int
		total   int
		want    int
	}{
		{current: 99, delta: 1, total: 2, want: 0},
		{current: -99, delta: -1, total: 2, want: 0},
		{current: 0, delta: -1, total: 4, want: 3},
		{current: 3, delta: 1, total: 4, want: 0},
	}
	for _, test := range tests {
		if got := wrapTUIIndex(test.current, test.delta, test.total); got != test.want {
			t.Fatalf("wrapTUIIndex(%d, %d, %d) = %d, want %d",
				test.current, test.delta, test.total, got, test.want)
		}
	}
}

func TestReadTUIKeys(t *testing.T) {
	keys := make(chan tuiKey, 7)
	go readTUIKeys(bytes.NewBufferString("rsp\t\x1b[Z\x1b[1;5Aq"), keys)
	want := []tuiKey{
		tuiKeyRefresh,
		tuiKeyDown,
		tuiKeySetPort,
		tuiKeyFocusNext,
		tuiKeyFocusPrevious,
		tuiKeyUp,
		tuiKeyQuit,
	}
	for _, expected := range want {
		if got := <-keys; got != expected {
			t.Fatalf("key = %v, want %v", got, expected)
		}
	}
	if _, open := <-keys; open {
		t.Fatal("key channel was not closed after input ended")
	}
}

func TestTUIFocusNavigationMakesSidebarOperable(t *testing.T) {
	snapshot := tuiSnapshot{
		Page:         tuiPageDashboard,
		SelectedMenu: int(tuiPageDashboard),
		FocusSidebar: true,
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeyDown) {
		t.Fatal("sidebar down key was not handled")
	}
	if snapshot.SelectedMenu != int(tuiPageProxies) || snapshot.Page != tuiPageDashboard {
		t.Fatalf("sidebar movement changed wrong state: %+v", snapshot)
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeySelect) {
		t.Fatal("sidebar Enter was not handled")
	}
	if snapshot.Page != tuiPageProxies || snapshot.FocusSidebar {
		t.Fatalf("sidebar Enter did not open content: %+v", snapshot)
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeyLeft) || !snapshot.FocusSidebar {
		t.Fatalf("left did not return to sidebar from proxies: %+v", snapshot)
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeyRight) || snapshot.FocusSidebar {
		t.Fatalf("right did not return to proxy content: %+v", snapshot)
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeyFocusNext) || !snapshot.FocusSidebar {
		t.Fatalf("Tab did not focus sidebar: %+v", snapshot)
	}
	if snapshot.SelectedMenu != int(tuiPageProxies) {
		t.Fatalf("sidebar cursor did not follow active page: %+v", snapshot)
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeySettings) {
		t.Fatal("numeric page shortcut was not handled")
	}
	if snapshot.Page != tuiPageTools || snapshot.FocusSidebar {
		t.Fatalf("numeric page shortcut did not open content: %+v", snapshot)
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeyLeft) || !snapshot.FocusSidebar {
		t.Fatalf("left did not return to sidebar: %+v", snapshot)
	}
}

func TestTUIProxyGroupsAndNodesUseArrowOrWSNavigation(t *testing.T) {
	left, ok := tuiKeyFromTea(tea.KeyMsg{Type: tea.KeyLeft})
	if !ok || left != tuiKeyLeft {
		t.Fatalf("left arrow = (%v, %v)", left, ok)
	}
	right, ok := tuiKeyFromTea(tea.KeyMsg{Type: tea.KeyRight})
	if !ok || right != tuiKeyRight {
		t.Fatalf("right arrow = (%v, %v)", right, ok)
	}
	up, ok := tuiKeyFromTea(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if !ok || up != tuiKeyUp {
		t.Fatalf("w = (%v, %v)", up, ok)
	}
	down, ok := tuiKeyFromTea(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if !ok || down != tuiKeyDown {
		t.Fatalf("s = (%v, %v)", down, ok)
	}
	for _, removed := range []rune{'j', 'k', 'h', 'l'} {
		if key, mapped := tuiKeyFromTea(tea.KeyMsg{
			Type:  tea.KeyRunes,
			Runes: []rune{removed},
		}); mapped {
			t.Fatalf("removed key %q still maps to %v", removed, key)
		}
	}

	model := newTUIModel(controllerClient{}, cliPaths{}, nil, false)
	model.snapshot.Page = tuiPageProxies
	model.snapshot.SelectedMenu = int(tuiPageProxies)
	model.snapshot.FocusSidebar = false
	model.snapshot.Groups = []tuiGroup{
		{Name: "FIRST", Now: "A", Nodes: []string{"A", "B"}},
		{Name: "SECOND", Now: "C", Nodes: []string{"C", "D"}},
	}
	model.snapshot.SelectedNode = 0
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if !model.snapshot.FocusSidebar || model.snapshot.SelectedNode != 0 {
		t.Fatalf("left arrow changed proxy state instead of focus: %+v", model.snapshot)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRight})
	if model.snapshot.FocusSidebar {
		t.Fatalf("right arrow did not focus content: %+v", model.snapshot)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if model.snapshot.SelectedGroup != 1 || model.snapshot.ProxyNodeFocus {
		t.Fatalf("s did not move the proxy group: %+v", model.snapshot)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !model.snapshot.ProxyNodeFocus {
		t.Fatalf("Enter did not open node selection: %+v", model.snapshot)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if model.snapshot.SelectedNode != 1 {
		t.Fatalf("s did not move the selected node: %+v", model.snapshot)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if model.snapshot.ProxyNodeFocus {
		t.Fatalf("Esc did not return to proxy groups: %+v", model.snapshot)
	}
}

func TestReadTUIKeysWaitsUntilPreviousKeyIsHandled(t *testing.T) {
	keys := make(chan tuiKey)
	handled := make(chan struct{})
	go readTUIKeysSynchronized(bytes.NewBufferString("12"), keys, handled)

	if got := <-keys; got != tuiKeyDashboard {
		t.Fatalf("first key = %v, want dashboard", got)
	}
	select {
	case unexpected := <-keys:
		t.Fatalf("read next key before acknowledgement: %v", unexpected)
	case <-time.After(20 * time.Millisecond):
	}
	handled <- struct{}{}
	if got := <-keys; got != tuiKeyProxies {
		t.Fatalf("second key = %v, want proxies", got)
	}
	handled <- struct{}{}
	if _, open := <-keys; open {
		t.Fatal("key channel was not closed after synchronized input ended")
	}
}

func TestReadTUILineDoesNotConsumeFollowingKeys(t *testing.T) {
	input := bytes.NewBufferString("17892\n?q")
	line, err := readTUILine(input)
	if err != nil {
		t.Fatal(err)
	}
	if line != "17892" {
		t.Fatalf("line = %q, want 17892", line)
	}
	remaining, err := io.ReadAll(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(remaining); got != "?q" {
		t.Fatalf("remaining input = %q, want ?q", got)
	}
}

func TestSetProxyDoesNotWriteToTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut || request.URL.Path != "/proxies/PROXY" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := controllerClient{
		options: controllerOptions{address: server.URL},
		client:  server.Client(),
	}
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdout := os.Stdout
	os.Stdout = writePipe
	defer func() {
		os.Stdout = originalStdout
	}()

	if err := client.setProxy("PROXY", "DIRECT"); err != nil {
		t.Fatal(err)
	}
	if err := writePipe.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 0 {
		t.Fatalf("setProxy wrote to terminal: %q", output)
	}
}

func TestRefreshTUISnapshotPreservesSelectionsAndActionStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/proxies":
			_, _ = io.WriteString(w, `{"proxies":{
				"A":{"type":"Selector","now":"A1","all":["A1"]},
				"B":{"type":"Selector","now":"B2","all":["B1","B2"]}
			}}`)
		case "/traffic":
			_, _ = io.WriteString(w, `{"up":1,"down":2,"upTotal":3,"downTotal":4}`+"\n")
		case "/connections":
			_, _ = io.WriteString(w, `{"connections":[
				{"id":"id-1","metadata":{"host":"one","network":"tcp"},"chains":["DIRECT"]},
				{"id":"id-2","metadata":{"destinationIP":"2001:db8::2","destinationPort":"443","network":"tcp"},"chains":["PROXY"]}
			]}`)
		case "/configs":
			_, _ = io.WriteString(w, `{"mode":"rule","mixed-port":17890,"log-level":"info"}`)
		case "/providers/proxies":
			_, _ = io.WriteString(w, `{"providers":{
				"p1":{"name":"p1","type":"Proxy","proxies":[]},
				"p2":{"name":"p2","type":"Proxy","proxies":[]}
			}}`)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	snapshot := tuiSnapshot{
		Groups: []tuiGroup{
			{Name: "B", Now: "B2", Nodes: []string{"B1", "B2"}},
		},
		Connections:        []tuiConnection{{ID: "id-2"}},
		Providers:          []tuiProvider{{Name: "p2"}},
		SelectedNode:       0,
		SelectedConnection: 0,
		SelectedProvider:   0,
		Status:             "Switched B to B1",
		UpdatedAt:          time.Now(),
	}
	client := controllerClient{
		options: controllerOptions{address: server.URL},
		client:  server.Client(),
	}
	refreshTUISnapshot(&snapshot, client)

	if snapshot.Groups[snapshot.SelectedGroup].Name != "B" ||
		snapshot.Groups[snapshot.SelectedGroup].Nodes[snapshot.SelectedNode] != "B1" {
		t.Fatalf("proxy selection was not preserved: group=%d node=%d", snapshot.SelectedGroup, snapshot.SelectedNode)
	}
	if snapshot.Connections[snapshot.SelectedConnection].ID != "id-2" {
		t.Fatalf("connection selection = %q", snapshot.Connections[snapshot.SelectedConnection].ID)
	}
	if snapshot.Connections[snapshot.SelectedConnection].Host != "[2001:db8::2]:443" {
		t.Fatalf(
			"connection destination = %q",
			snapshot.Connections[snapshot.SelectedConnection].Host,
		)
	}
	if snapshot.Providers[snapshot.SelectedProvider].Name != "p2" {
		t.Fatalf("provider selection = %q", snapshot.Providers[snapshot.SelectedProvider].Name)
	}
	if snapshot.Status != "Switched B to B1" {
		t.Fatalf("action status was overwritten: %q", snapshot.Status)
	}
	if snapshot.Settings.MixedPort != 17890 || snapshot.Traffic.Up != 1 || snapshot.TotalTraffic.Down != 4 {
		t.Fatalf("snapshot values were not refreshed: %+v", snapshot)
	}
}

func TestResolvePathsUsesDirectoryForDefaultConfig(t *testing.T) {
	directory := t.TempDir()
	paths, err := resolvePaths("", directory)
	if err != nil {
		t.Fatal(err)
	}
	wantHome, _ := filepath.Abs(directory)
	wantConfig := filepath.Join(wantHome, "config.yaml")
	if paths.homeDir != wantHome || paths.configPath != wantConfig {
		t.Fatalf("paths = (%q, %q), want (%q, %q)", paths.homeDir, paths.configPath, wantHome, wantConfig)
	}
}

func TestResolvePathsUsesRelativeDirectoryOnce(t *testing.T) {
	directory := filepath.Join("test-data", "instance")
	paths, err := resolvePaths("", directory)
	if err != nil {
		t.Fatal(err)
	}
	wantHome, _ := filepath.Abs(directory)
	wantConfig := filepath.Join(wantHome, "config.yaml")
	if paths.homeDir != wantHome || paths.configPath != wantConfig {
		t.Fatalf("paths = (%q, %q), want (%q, %q)", paths.homeDir, paths.configPath, wantHome, wantConfig)
	}
}

func TestResolvePathsDefaultConfigUsesUserConfigDirectory(t *testing.T) {
	configRoot, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := resolvePaths("", "")
	if err != nil {
		t.Fatal(err)
	}
	wantHome, _ := filepath.Abs(filepath.Join(configRoot, "flclash"))
	wantConfig := filepath.Join(wantHome, "config.yaml")
	if paths.homeDir != wantHome || paths.configPath != wantConfig {
		t.Fatalf("paths = (%q, %q), want (%q, %q)", paths.homeDir, paths.configPath, wantHome, wantConfig)
	}
}

func TestEnsureTUIConfigCreatesMinimalConfig(t *testing.T) {
	directory := t.TempDir()
	paths := cliPaths{homeDir: directory, configPath: filepath.Join(directory, "config.yaml")}
	if err := ensureTUIConfig(paths, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if message := validateConfigBytes(data); message != "" {
		t.Fatalf("generated config is invalid: %s", message)
	}
	for _, expected := range []string{
		"ipv6: false",
		"unified-delay: true",
		"tcp-concurrent: true",
	} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("generated config does not contain %q:\n%s", expected, data)
		}
	}
	if err := ensureTUIConfig(paths, true); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(paths.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(data) {
		t.Fatal("existing config was overwritten")
	}
}

func TestEnsureTUIFlClashDefaultsMigratesOnlyMissingSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	configData := `mixed-port: 7891
mode: rule
ipv6: true
unified-delay: false
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,PROXY
`
	if err := os.WriteFile(path, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureTUIFlClashDefaults(path); err != nil {
		t.Fatal(err)
	}
	settings := loadTUIConfiguredSettings(path, true)
	if settings == nil {
		t.Fatal("migrated settings could not be loaded")
	}
	if !settings.IPv6 {
		t.Fatal("explicit IPv6 setting was overwritten")
	}
	if settings.UnifiedDelay {
		t.Fatal("explicit unified-delay setting was overwritten")
	}
	if !settings.TCPConcurrent {
		t.Fatal("missing tcp-concurrent did not receive FlClash default")
	}
}

func TestRestoreLatestTUIConfigDoesNotOverwriteWithInvalidBackup(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	original := []byte("mixed-port: 17890\n")
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	backupPath := configPath + ".backup-999999999999999999"
	if err := os.WriteFile(backupPath, []byte("mixed-port: [invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := restoreLatestTUIConfig(configPath); err == nil {
		t.Fatal("restore unexpectedly accepted invalid backup")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Fatalf("config changed after invalid restore: %q", data)
	}
}
