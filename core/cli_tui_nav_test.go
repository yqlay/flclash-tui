//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

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
	model.snapshot.Page = tuiPageDashboard
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
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})

	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
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

	if model.snapshot.SelectedRow != tuiProfileImportSubscriptionRow {
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
	if model.snapshot.SelectedRow != tuiProfileImportFileRow {
		t.Fatalf("down from URL import selected row %d, want file import", model.snapshot.SelectedRow)
	}
	if command := model.selectCurrent(); command != nil {
		t.Fatal("opening the local file input unexpectedly returned a command")
	}
	if model.inputMode != tuiInputProfileFile {
		t.Fatalf("input mode = %d, want local file input", model.inputMode)
	}
	model.inputMode = tuiInputNone
	moveTUIProfile(&model.snapshot, 1)
	if model.snapshot.SelectedRow != 0 {
		t.Fatalf("down from import selected row %d, want first profile", model.snapshot.SelectedRow)
	}
	moveTUIProfile(&model.snapshot, -1)
	if model.snapshot.SelectedRow != tuiProfileImportFileRow {
		t.Fatalf("up from first profile selected row %d, want file import", model.snapshot.SelectedRow)
	}
}

func TestTUIProfilesHideManagedRuntimeFiles(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	for _, name := range []string{
		"config.yaml",
		"work.yml",
		tuiSilentRuntimeConfigPrefix + "0123456789abcdef01234567.yaml",
		tuiManagedRuntimeConfigPrefix + "89abcdef0123456789abcdef.yaml",
	} {
		if err := os.WriteFile(
			filepath.Join(directory, name),
			[]byte(defaultTUIConfig),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
	}
	paths := cliPaths{homeDir: directory, configPath: configPath}
	snapshot := tuiSnapshot{SelectedRow: tuiProfileImportSubscriptionRow}
	refreshTUIProfiles(&snapshot, paths)
	if len(snapshot.Profiles) != 2 {
		t.Fatalf("TUI profiles = %+v, want only user profiles", snapshot.Profiles)
	}
	profiles, err := listCLIProfiles(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(profiles) != 2 {
		t.Fatalf("CLI profiles = %+v, want only user profiles", profiles)
	}
	for _, profile := range append(snapshot.Profiles, profiles...) {
		if isTUIRuntimeProfileName(profile.Name) {
			t.Fatalf("runtime profile remained visible: %s", profile.Name)
		}
	}
}

func TestTUILocalProfileImportReadsAndAllocatesSafeName(t *testing.T) {
	sourceDirectory := t.TempDir()
	homeDir := t.TempDir()
	sourcePath := filepath.Join(sourceDirectory, "office.yaml")
	if err := os.WriteFile(sourcePath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(homeDir, "office.yaml"),
		[]byte(defaultTUIConfig),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	data, name, err := readTUILocalProfile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if name != "office.yaml" || string(data) != defaultTUIConfig {
		t.Fatalf("local import = %q %q", name, data)
	}
	destination, err := nextTUIImportedProfilePath(homeDir, name)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(destination) != "office-2.yaml" {
		t.Fatalf("collision destination = %s", destination)
	}
	if _, err := os.Stat(sourcePath); err != nil {
		t.Fatalf("reading local profile changed its source: %v", err)
	}
}

func TestTUIProxyPortShowsOneCurrentValue(t *testing.T) {
	snapshot := tuiSnapshot{
		Settings:        tuiSettings{Mode: tuiSilentMode, MixedPort: 7891},
		ActiveProxyPort: 45678,
		FLCEnabled:      true,
		FLCOutbound:     "PROXY",
	}
	if label := tuiProxyPortLabel(snapshot); label != "45678" {
		t.Fatalf("active unified port label = %q", label)
	}
	if label := tuiFLCOutboundLabel(snapshot); label != "PROXY · READY" {
		t.Fatalf("ready FLC label = %q", label)
	}
	snapshot.ActiveProxyPort = 0
	snapshot.FLCEnabled = false
	if label := tuiProxyPortLabel(snapshot); label != "7891" {
		t.Fatalf("stopped unified port label = %q", label)
	}
	if label := tuiFLCOutboundLabel(snapshot); label != "PROXY · WAITING FOR CORE" {
		t.Fatalf("waiting FLC label = %q", label)
	}
	snapshot.Groups = []tuiGroup{{Name: "PROXY", Now: "hk-1", Nodes: []string{"hk-1"}}}
	if label := tuiFLCOutboundLabel(snapshot); label != "PROXY → hk-1 · WAITING FOR CORE" {
		t.Fatalf("FLC label with selected node = %q", label)
	}
}

func TestTUIDashboardDefaultsToCoreRow(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	if model.snapshot.SelectedDashboard != tuiDashboardServiceRow {
		t.Fatalf(
			"default Dashboard row = %d, want Core",
			model.snapshot.SelectedDashboard,
		)
	}
}

func TestTUIDashboardFLCOutboundOpensProxies(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.FocusSidebar = false
	model.snapshot.FLCOutbound = "PROXY"
	model.snapshot.Groups = []tuiGroup{{
		Name:  "PROXY",
		Now:   "hk-1",
		Nodes: []string{"DIRECT", "hk-1"},
	}}
	model.snapshot.SelectedDashboard = tuiDashboardFLCOutboundRow
	if command := model.selectCurrent(); command != nil {
		t.Fatal("FLC outbound row unexpectedly scheduled a backend mutation")
	}
	if model.snapshot.Page != tuiPageProxies ||
		!model.snapshot.ProxyNodeFocus ||
		model.snapshot.SelectedGroup != 0 ||
		model.snapshot.Groups[model.snapshot.SelectedGroup].Nodes[model.snapshot.SelectedNode] != "hk-1" {
		t.Fatalf("FLC row did not open Proxies on the current node: %+v", model.snapshot)
	}
}

func TestBackendRenamesProfileRequestedByFrontend(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "profile-old.yaml")
	if err := os.WriteFile(sourcePath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := newTUIServiceRuntime(
		cliPaths{
			homeDir:    directory,
			configPath: filepath.Join(directory, "config.yaml"),
		},
		defaultCLITestURL,
		filepath.Join(directory, "core.sock"),
		nil,
		nil,
	)
	revision := uint64(1)
	status := runtime.handle(tuiServiceRequest{
		ProtocolVersion:  tuiServiceProtocolVersion,
		RequestID:        "rename-profile",
		ExpectedRevision: &revision,
		Action:           "rename_profile",
		ConfigPath:       sourcePath,
		NewName:          "office",
	})
	destinationPath := filepath.Join(directory, "office.yaml")
	if !status.OK || status.ResultPath != destinationPath || status.Revision != 2 {
		t.Fatalf("rename response = %+v", status)
	}
	if _, err := os.Stat(destinationPath); err != nil {
		t.Fatalf("renamed profile: %v", err)
	}
	if _, err := os.Stat(sourcePath); !os.IsNotExist(err) {
		t.Fatalf("old profile still exists: %v", err)
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
	deleteKey, ok := tuiKeyFromTea(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'x'},
	})
	if !ok || deleteKey != tuiKeyCloseConnections {
		t.Fatalf("x key = (%v, %v)", deleteKey, ok)
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
	for _, hint := range []string{
		"U refresh",
		"F2/u rename",
		"F2 rename",
		"x delete",
	} {
		if !strings.Contains(plain, hint) {
			t.Fatalf("profiles view does not contain %q:\n%s", hint, plain)
		}
	}
	snapshot.Profiles[0].Current = true
	output.Reset()
	drawTUIProfiles(&output, snapshot, 110, 24)
	plain = stripTUIANSI(output.String())
	if !strings.Contains(plain, "x locked") {
		t.Fatalf("active Profile does not explain deletion lock:\n%s", plain)
	}
}

func TestTUIQuitKeysUseGracefulShutdownPath(t *testing.T) {
	originalExit := completeCLIExitForTUI
	completeCLIExitForTUI = func(int) error { return nil }
	t.Cleanup(func() { completeCLIExitForTUI = originalExit })
	tests := []struct {
		name              string
		key               tea.KeyMsg
		expected          tuiKey
		stopServiceOnExit bool
	}{
		{
			name:     "q exits current TUI",
			key:      tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}},
			expected: tuiKeyQuit,
		},
		{
			name:              "ctrl+c shuts down managed Backend",
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
			if test.stopServiceOnExit {
				model.ownsCore = true
				model.service = &tuiServiceClient{}
			}
			command := model.handleTeaKey(test.key)
			if command == nil {
				t.Fatal("quit key did not return a command")
			}
			message := command()
			if test.stopServiceOnExit {
				if _, ok := message.(tuiShutdownResultMsg); !ok {
					t.Fatalf("Ctrl+C command returned %T", message)
				}
			} else if _, ok := message.(tea.QuitMsg); !ok {
				t.Fatal("quit key did not terminate the event loop")
			}
			if model.shutdownRequested != test.stopServiceOnExit {
				t.Fatalf(
					"shutdownRequested = %t, want %t",
					model.shutdownRequested,
					test.stopServiceOnExit,
				)
			}
			if model.frontendExitRequested != !test.stopServiceOnExit {
				t.Fatalf(
					"frontendExitRequested = %t, want %t",
					model.frontendExitRequested,
					!test.stopServiceOnExit,
				)
			}
		})
	}
}

func TestTUIQuitCancelsOnlyFrontendMonitors(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	coreMemoryStopped := false
	trafficStopped := false
	model.stopCoreMemory = func() { coreMemoryStopped = true }
	model.stopTraffic = func() { trafficStopped = true }

	command := model.handleKey(tuiKeyQuit)
	if command == nil {
		t.Fatal("q did not return a quit command")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatal("q did not terminate the TUI event loop")
	}
	if !coreMemoryStopped || !trafficStopped {
		t.Fatalf(
			"q left frontend monitors running: memory=%t traffic=%t",
			coreMemoryStopped,
			trafficStopped,
		)
	}
	if model.shutdownRequested {
		t.Fatal("q requested an owned Core shutdown")
	}
}

func TestTUIInterruptMarksOwnedLocalCoreForShutdown(t *testing.T) {
	originalExit := completeCLIExitForTUI
	completeCLIExitForTUI = func(int) error { return nil }
	t.Cleanup(func() { completeCLIExitForTUI = originalExit })
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	command := model.handleKey(tuiKeyInterrupt)
	if command == nil {
		t.Fatal("Ctrl+C did not return a quit command for an owned local Core")
	}
	if _, ok := command().(tuiShutdownResultMsg); !ok {
		t.Fatal("Ctrl+C did not run complete FlClash shutdown")
	}
	if !model.shutdownRequested || model.frontendExitRequested {
		t.Fatalf(
			"Ctrl+C lifecycle state is wrong: shutdown=%t frontendExit=%t",
			model.shutdownRequested,
			model.frontendExitRequested,
		)
	}
}

func TestTUIInterruptFromInputUsesManagedShutdownPath(t *testing.T) {
	originalExit := completeCLIExitForTUI
	completeCLIExitForTUI = func(int) error { return nil }
	t.Cleanup(func() { completeCLIExitForTUI = originalExit })
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.service = newTUIServiceClientAt(t.TempDir())
	model.beginInput(tuiInputMixedPort)
	command := model.handleInput(tea.KeyMsg{Type: tea.KeyCtrlC})
	if command == nil {
		t.Fatal("Ctrl+C in input mode did not request shutdown")
	}
	if !model.shutdownRequested || model.frontendExitRequested {
		t.Fatalf(
			"Ctrl+C in input mode used the wrong lifecycle: shutdown=%t frontend=%t",
			model.shutdownRequested,
			model.frontendExitRequested,
		)
	}
	if model.inputMode != tuiInputNone {
		t.Fatalf("Ctrl+C left input mode active: %v", model.inputMode)
	}
	if _, ok := command().(tuiShutdownResultMsg); !ok {
		t.Fatal("Ctrl+C in input mode bypassed managed Backend shutdown")
	}
}

func TestTUIInterruptSignalUsesManagedShutdownPath(t *testing.T) {
	originalExit := completeCLIExitForTUI
	completeCLIExitForTUI = func(int) error { return nil }
	t.Cleanup(func() { completeCLIExitForTUI = originalExit })
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.service = newTUIServiceClientAt(t.TempDir())
	_, command := model.Update(tuiInterruptSignalMsg{})
	if command == nil || !model.shutdownRequested {
		t.Fatal("SIGINT did not request managed Backend shutdown")
	}
	if _, ok := command().(tuiShutdownResultMsg); !ok {
		t.Fatal("SIGINT bypassed managed Backend shutdown")
	}
}

func TestTUITerminalExitSignalClosesOnlyCurrentFrontend(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	coreMemoryStopped := false
	trafficStopped := false
	model.stopCoreMemory = func() { coreMemoryStopped = true }
	model.stopTraffic = func() { trafficStopped = true }

	_, command := model.Update(tuiTerminalExitSignalMsg{})
	if command == nil {
		t.Fatal("terminal exit signal did not return a quit command")
	}
	if _, ok := command().(tea.QuitMsg); !ok {
		t.Fatal("terminal exit signal did not quit the TUI event loop")
	}
	if !model.frontendExitRequested || model.shutdownRequested {
		t.Fatalf(
			"terminal exit used wrong lifecycle: frontend=%t shutdown=%t",
			model.frontendExitRequested,
			model.shutdownRequested,
		)
	}
	if !coreMemoryStopped || !trafficStopped {
		t.Fatalf(
			"terminal exit left frontend monitors running: memory=%t traffic=%t",
			coreMemoryStopped,
			trafficStopped,
		)
	}
}

func TestTUITerminalInputReportsEOFOnlyOnce(t *testing.T) {
	readEnd, writeEnd, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer readEnd.Close()
	if err := writeEnd.Close(); err != nil {
		t.Fatal(err)
	}

	terminalEOF := make(chan struct{}, 2)
	input := newTUITerminalInput(readEnd, terminalEOF)
	buffer := make([]byte, 1)
	for range 2 {
		if _, err := input.Read(buffer); err != io.EOF {
			t.Fatalf("terminal input error = %v, want EOF", err)
		}
	}

	select {
	case <-terminalEOF:
	default:
		t.Fatal("terminal input did not report EOF")
	}
	select {
	case <-terminalEOF:
		t.Fatal("terminal input reported EOF more than once")
	default:
	}
}

func TestTUIParentExitSignalChecksForStartRace(t *testing.T) {
	originalParentPID := tuiParentPID
	originalSetSignal := setTUIParentExitSignal
	t.Cleanup(func() {
		tuiParentPID = originalParentPID
		setTUIParentExitSignal = originalSetSignal
	})

	signalCalls := 0
	setTUIParentExitSignal = func() error {
		signalCalls++
		return nil
	}
	tuiParentPID = func() int { return 42 }
	if err := armTUIParentExitSignal(); err != nil {
		t.Fatalf("arm TUI parent exit signal: %v", err)
	}
	if signalCalls != 1 {
		t.Fatalf("parent exit signal calls = %d, want 1", signalCalls)
	}

	parentCalls := 0
	tuiParentPID = func() int {
		parentCalls++
		if parentCalls == 1 {
			return 42
		}
		return 1
	}
	err := armTUIParentExitSignal()
	if err == nil || !strings.Contains(err.Error(), "parent exited") {
		t.Fatalf("parent exit race error = %v", err)
	}
}

func TestTUIStartupInterruptIsConsumedWithoutBackendResidue(t *testing.T) {
	originalExit := completeCLIExitForTUI
	completeCLIExitForTUI = func(int) error { return nil }
	t.Cleanup(func() { completeCLIExitForTUI = originalExit })
	interrupt := make(chan os.Signal, 1)
	interrupt <- os.Interrupt
	interrupted, err := shutdownTUIServiceOnInterrupt(
		interrupt,
		nil,
		cliPaths{homeDir: t.TempDir()},
	)
	if err != nil || !interrupted {
		t.Fatalf("startup interrupt result = %t, %v", interrupted, err)
	}
	interrupted, err = shutdownTUIServiceOnInterrupt(
		interrupt,
		nil,
		cliPaths{homeDir: t.TempDir()},
	)
	if err != nil || interrupted {
		t.Fatalf("drained startup interrupt result = %t, %v", interrupted, err)
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

func TestTUIModeSelectionShowsAllModesBeforeChanging(t *testing.T) {
	if got := strings.Join(tuiTrafficModes, ","); got != "rule,silent,global,direct" {
		t.Fatalf("mode list order = %q", got)
	}
	model := newTUIModel(
		controllerClient{},
		cliPaths{configPath: filepath.Join(t.TempDir(), "missing.yaml")},
		nil,
		true,
	)
	model.width = 100
	model.height = 24
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.FocusSidebar = false
	model.snapshot.SelectedDashboard = tuiDashboardModeRow
	model.snapshot.Settings = tuiSettings{Mode: "rule", MixedPort: 7891}

	if command := model.selectCurrent(); command != nil {
		t.Fatal("opening the mode list unexpectedly started an operation")
	}
	if !model.modeSelectionOpen || model.busy {
		t.Fatalf(
			"mode selection state = open:%t busy:%t",
			model.modeSelectionOpen,
			model.busy,
		)
	}
	if model.snapshot.Settings.Mode != "rule" {
		t.Fatalf("opening the list changed mode to %q", model.snapshot.Settings.Mode)
	}
	plain := stripTUIANSI(model.View())
	for _, expected := range []string{
		"Select outbound mode",
		"rule  (current)",
		"silent",
		"global",
		"direct",
		"Enter confirm",
		"Esc cancel",
	} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("mode list does not contain %q:\n%s", expected, plain)
		}
	}

	_, command := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if command != nil || model.selectedMode != 1 {
		t.Fatalf(
			"mode list down = selection:%d command:%v",
			model.selectedMode,
			command,
		)
	}
	if model.snapshot.Settings.Mode != "rule" {
		t.Fatalf("moving in the list changed mode to %q", model.snapshot.Settings.Mode)
	}
}

func TestTUIModeSelectionStagesExactChoice(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{configPath: filepath.Join(t.TempDir(), "missing.yaml")},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageTools
	model.snapshot.FocusSidebar = false
	model.snapshot.Settings = tuiSettings{Mode: "rule", MixedPort: 7891}

	model.beginModeSelection()
	model.selectedMode = findTUIString(tuiTrafficModes, "global")
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if command != nil {
		t.Fatal("staging the selected mode unexpectedly started an operation")
	}
	if model.modeSelectionOpen {
		t.Fatal("mode list remained open after confirmation")
	}
	if model.snapshot.Settings.Mode != "global" || model.stagedSettings == nil ||
		model.stagedSettings.Mode != "global" {
		t.Fatalf(
			"selected mode was not staged exactly: snapshot=%+v staged=%+v",
			model.snapshot.Settings,
			model.stagedSettings,
		)
	}
}

func TestTUIModeSelectionSubmitsExactChoiceAsynchronously(t *testing.T) {
	directory := t.TempDir()
	socketPath := filepath.Join(directory, tuiServiceSocketFilename)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requests := make(chan tuiServiceRequest, 1)
	serverDone := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer connection.Close()
		var request tuiServiceRequest
		if decodeErr := json.NewDecoder(connection).Decode(&request); decodeErr != nil {
			serverDone <- decodeErr
			return
		}
		requests <- request
		serverDone <- json.NewEncoder(connection).Encode(tuiServiceStatus{
			ProtocolVersion: tuiServiceProtocolVersion,
			RequestID:       request.RequestID,
			Revision:        8,
			OK:              true,
			Running:         true,
			Mode:            request.Mode,
			ProxyPort:       7891,
		})
	}()

	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.service = newTUIServiceClientAt(directory)
	model.backendRevision = 7
	model.coreRunning = true
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.FocusSidebar = false
	model.snapshot.Settings = tuiSettings{Mode: "rule", MixedPort: 7891}
	model.beginModeSelection()
	model.selectedMode = findTUIString(tuiTrafficModes, "direct")
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil || !model.busy {
		t.Fatalf("confirmed mode change = command:%v busy:%t", command, model.busy)
	}
	if model.snapshot.Settings.Mode != "rule" ||
		!strings.Contains(model.snapshot.Status, "Changing mode to direct") {
		t.Fatalf("mode changed before Backend result: %+v", model.snapshot)
	}

	_, _ = model.Update(command())
	if model.busy || model.snapshot.Settings.Mode != "direct" ||
		model.backendRevision != 8 {
		t.Fatalf(
			"confirmed mode result = busy:%t mode:%q revision:%d",
			model.busy,
			model.snapshot.Settings.Mode,
			model.backendRevision,
		)
	}
	request := <-requests
	if request.Action != "set_mode" || request.Mode != "direct" ||
		request.ExpectedRevision == nil || *request.ExpectedRevision != 7 {
		t.Fatalf("mode request = %+v", request)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
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
			TunScope:   tuiTunScopeUser,
		},
	}
	var output strings.Builder
	drawTUISettings(&output, snapshot, 80, 24)
	plain := stripTUIANSI(output.String())
	for _, row := range []string{
		"Allow LAN     ON",
		"IPv6          OFF",
		"Unified delay",
		"TCP concurrent",
		"Log level     info",
		"TUN scope     USER",
	} {
		if !strings.Contains(plain, row) {
			t.Fatalf("settings view does not contain %q:\n%s", row, plain)
		}
	}
	for _, daily := range []string{
		"Mode          rule",
		"flc           pick a node in Proxies",
		"Proxy port    17890",
		"Core          STOPPED · Enter to start",
		"System proxy  DISABLED · Enter to enable (starts Core)",
		"TUN           USER ON",
	} {
		if strings.Contains(plain, daily) {
			t.Fatalf("settings still repeats daily control %q:\n%s", daily, plain)
		}
	}
}

func TestTUISettingsServiceRowUsesEnter(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.FocusSidebar = false
	model.snapshot.SelectedDashboard = tuiDashboardServiceRow

	command := model.selectCurrent()
	if command == nil {
		t.Fatal("selecting the Dashboard Core row did not return a start operation")
	}
	if !model.busy {
		t.Fatal("selecting the Dashboard Core row did not mark the operation busy")
	}
}

func TestTUISettingsSystemProxyRequiresBackend(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.FocusSidebar = false
	model.snapshot.SelectedDashboard = tuiDashboardSystemProxyRow

	if command := model.selectCurrent(); command != nil {
		t.Fatal("system proxy without Backend scheduled a mutation")
	}
	if !strings.Contains(model.snapshot.Status, "managed backend") {
		t.Fatalf("system proxy status = %q", model.snapshot.Status)
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
	drawTUIDashboard(
		&output,
		snapshot,
		cliPaths{configPath: "/tmp/config.yaml"},
		80,
		24,
	)
	plain := stripTUIANSI(output.String())
	if !strings.Contains(plain, "Core          RUNNING · Enter to stop") {
		t.Fatalf("running Dashboard has no clear service state:\n%s", plain)
	}
	if !strings.Contains(plain, "System proxy  DISABLED · Enter to enable") {
		t.Fatalf("running Dashboard has no clear system proxy state:\n%s", plain)
	}
	if strings.Contains(plain, "(starts Core)") {
		t.Fatalf("running Dashboard kept automatic-start hint:\n%s", plain)
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
		"SSH",
		"Proxies",
		"Profiles",
		"History",
		"Connections",
		"Logs",
		"Settings",
		"Maintenance",
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
	for _, removed := range []string{"Providers"} {
		if strings.Contains(plain, removed) {
			t.Fatalf("sidebar still exposes standalone %s page:\n%s", removed, plain)
		}
	}
	historySelected := tuiSidebar(
		tuiSnapshot{
			Page:         tuiPageRequests,
			SelectedMenu: int(tuiPageRequests),
			FocusSidebar: true,
		},
		26,
		20,
	)
	if selected := stripTUIANSI(strings.Join(historySelected, "\n")); strings.Contains(selected, "> >  History") {
		t.Fatalf("selected History rendered two cursor chevrons:\n%s", selected)
	}
}

func TestTUISidebarOrderAndNumericShortcutsStayAligned(t *testing.T) {
	pages := []struct {
		digit string
		key   tuiKey
		page  tuiPage
	}{
		{"1", tuiKeyDashboard, tuiPageDashboard},
		{"2", tuiKeySSH, tuiPageSSH},
		{"3", tuiKeyProxies, tuiPageProxies},
		{"4", tuiKeyProfiles, tuiPageProfiles},
		{"5", tuiKeyRequests, tuiPageRequests},
		{"6", tuiKeyConnections, tuiPageConnections},
		{"7", tuiKeyLogs, tuiPageLogs},
		{"8", tuiKeyTools, tuiPageTools},
		{"9", tuiKeyMaintenance, tuiPageMaintenance},
	}
	for index, test := range pages {
		key, ok := tuiKeyFromTea(tea.KeyMsg{
			Type:  tea.KeyRunes,
			Runes: []rune(test.digit),
		})
		if !ok || key != test.key {
			t.Fatalf("%s mapped to (%v, %t), want (%v, true)", test.digit, key, ok, test.key)
		}
		page, ok := tuiPageForKey(key)
		if !ok || page != test.page {
			t.Fatalf("%s opens (%v, %t), want (%v, true)", test.digit, page, ok, test.page)
		}

		snapshot := tuiSnapshot{
			Page:         tuiPageDashboard,
			SelectedMenu: int(tuiPageDashboard),
			FocusSidebar: true,
		}
		for moved := 0; moved < index; moved++ {
			if !handleTUIFocusNavigation(&snapshot, tuiKeyDown) {
				t.Fatalf("sidebar did not move down to position %d", index)
			}
		}
		if snapshot.SelectedMenu != index {
			t.Fatalf("sidebar position = %d, want %d", snapshot.SelectedMenu, index)
		}
		if !handleTUIFocusNavigation(&snapshot, tuiKeySelect) ||
			snapshot.Page != test.page || snapshot.FocusSidebar {
			t.Fatalf("sidebar position %d did not open %s: %+v", index, tuiPageName(test.page), snapshot)
		}
	}
}

func TestSilentNetworkCheckDoesNotCreateListenerWhileCoreStopped(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.coreRunning = false
	model.snapshot.Settings.Mode = tuiSilentMode
	command := model.startNetworkCheck()
	if command == nil {
		t.Fatal("silent stopped-Core check did not return a result command")
	}
	message, ok := command().(tuiNetworkResultMsg)
	if !ok {
		t.Fatalf("network result type = %T", command())
	}
	if message.info.Error != "" || message.info.Route != "SILENT · Core stopped" {
		t.Fatalf("stopped silent network result = %+v", message.info)
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

func TestTUISeparatesSettingsAndMaintenance(t *testing.T) {
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
		"Allow LAN",
		"IPv6",
		"Unified delay",
		"TCP concurrent",
		"Log level     info",
		"TUN scope",
	} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("Settings does not contain %q:\n%s", expected, plain)
		}
	}
	for _, daily := range []string{
		"Mode          rule",
		"flc           pick a node in Proxies",
		"Proxy port    7891",
		"System proxy",
	} {
		if strings.Contains(plain, daily) {
			t.Fatalf("Settings still repeats daily control %q:\n%s", daily, plain)
		}
	}
	output.Reset()
	snapshot.Page = tuiPageMaintenance
	drawTUIMaintenance(&output, snapshot, 100, 30)
	plain = stripTUIANSI(output.String())
	for _, expected := range []string{
		"Edit current YAML",
		"Update Mihomo Geo databases",
		"Reset traffic counters",
		"if stable, do not update lightly",
	} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("Maintenance does not contain %q:\n%s", expected, plain)
		}
	}
}
