//go:build linux && !cgo && cli

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestUnsetEditorRefusesProfileAndMaintenanceEdits(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	const want = "set $VISUAL or $EDITOR before using config edit"

	profileHome := t.TempDir()
	profile := newTUIModel(controllerClient{}, cliPaths{HomeDir: profileHome}, nil, true)
	profile.service = &tuiServiceClient{}
	profile.snapshot.Page = tuiPageProfiles
	profile.snapshot.SelectedMenu = int(tuiPageProfiles)
	profile.snapshot.FocusSidebar = false
	profile.snapshot.Profiles = []tuiProfile{{Name: "alpha", Path: filepath.Join(profileHome, "alpha.yaml")}}
	profile.snapshot.SelectedRow = 0
	_, command := profile.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if command != nil || profile.editorBusy || profile.editorPath != "" || !strings.Contains(profile.snapshot.Status, want) {
		t.Fatalf("profile edit command %t busy %t path %q status %q", command != nil, profile.editorBusy, profile.editorPath, profile.snapshot.Status)
	}

	maintenanceHome := t.TempDir()
	maintenance := newTUIModel(controllerClient{}, cliPaths{HomeDir: maintenanceHome, ConfigPath: filepath.Join(maintenanceHome, "config.yaml")}, nil, true)
	maintenance.service = &tuiServiceClient{}
	maintenance.snapshot.Page = tuiPageMaintenance
	maintenance.snapshot.SelectedMenu = int(tuiPageMaintenance)
	maintenance.snapshot.FocusSidebar = false
	maintenance.snapshot.SelectedMaintenance = tuiMaintenanceEditConfigRow
	_, command = maintenance.update(tea.KeyMsg{Type: tea.KeyEnter})
	if command != nil || maintenance.editorBusy || maintenance.editorPath != "" || !strings.Contains(maintenance.snapshot.Status, want) {
		t.Fatalf("maintenance edit command %t busy %t path %q status %q", command != nil, maintenance.editorBusy, maintenance.editorPath, maintenance.snapshot.Status)
	}

	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("mixed-port: 7890\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := editManagedConfig(configPath); err == nil || err.Error() != want {
		t.Fatalf("config edit error = %v", err)
	}
}

func TestProxyPortZeroIsRejectedAndOffStaysDisabled(t *testing.T) {
	err := portCommand([]string{"0"})
	if err == nil || err.Error() != "proxy port must be a number from 1 to 65535, or off" {
		t.Fatalf("port 0 error = %v", err)
	}
	port, err := parseManagedProxyPort("off")
	if err != nil || port != 0 {
		t.Fatalf("off = %d, %v", port, err)
	}
	port, err = parseManagedProxyPort("7890")
	if err != nil || port != 7890 {
		t.Fatalf("7890 = %d, %v", port, err)
	}
}

func TestHistoryDoneAndCompletedSelectFinishedRows(t *testing.T) {
	history := []tuiRequest{
		{TuiConnection: tuiConnection{ID: "active", Host: "active.example"}, Active: true},
		{TuiConnection: tuiConnection{ID: "finished", Host: "finished.example"}, Active: false},
	}
	for _, state := range []string{"done", "completed", "DONE", "Completed"} {
		normalized, err := parseHistoryState(state)
		if err != nil {
			t.Fatalf("parse %q: %v", state, err)
		}
		filtered := filterCLIHistory(history, tuiTrafficSourceMixed, normalized, "", 0)
		if len(filtered) != 1 || filtered[0].ID != "finished" {
			t.Fatalf("cli %q = %+v", state, filtered)
		}
		snapshot := tuiSnapshot{Requests: history, HistoryFilter: normalized}
		indexes := matchedTUIRequestIndexes(snapshot)
		if len(indexes) != 1 || history[indexes[0]].ID != "finished" {
			t.Fatalf("tui %q indexes %v", state, indexes)
		}
	}
	if _, err := parseHistoryState("nope"); err == nil || err.Error() != "history state must be all, active, done, or completed" {
		t.Fatalf("invalid state error = %v", err)
	}

	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	model.snapshot.Page = tuiPageRequests
	model.snapshot.FocusSidebar = false
	model.snapshot.HistoryFilter = "all"
	seen := map[string]bool{}
	for index := 0; index < 4; index++ {
		model.handleKey(tuiKeyFilter)
		seen[model.snapshot.HistoryFilter] = true
	}
	for _, state := range []string{"active", "done", "completed", "all"} {
		if !seen[state] {
			t.Fatalf("filter cycle missed %q: %v", state, seen)
		}
	}

	t.Run("CLI", func(t *testing.T) {
		home := t.TempDir()
		serveRowAlignService(t, func(request tuiServiceRequest) tuiServiceStatus {
			status := alignServiceStatus(7, filepath.Join(home, "config.yaml"))
			if request.Action != "status" && request.Action != "history" {
				status.OK = false
				status.Error = "unexpected action: " + request.Action
			}
			status.History = history
			return status
		})
		for _, state := range []string{"done", "completed", "DONE", "Completed"} {
			output := captureStdout(t, func() error {
				return historyCommand([]string{"show", "--state", state, "--json"})
			})
			var got []tuiRequest
			if err := json.Unmarshal([]byte(output), &got); err != nil {
				t.Fatalf("history %q JSON: %v", state, err)
			}
			if len(got) != 1 || got[0].ID != "finished" {
				t.Fatalf("history command %q = %+v", state, got)
			}
		}
	})
}

func TestCompletionOffersSSHAttach(t *testing.T) {
	for _, shell := range []string{"bash", "zsh", "fish"} {
		output := captureStdout(t, func() error {
			return completionCommand([]string{shell})
		})
		if !completionOffersSSHAttach(shell, output) {
			t.Fatalf("%s completion has no ssh attach:\n%s", shell, output)
		}
	}
}

func completionOffersSSHAttach(shell, output string) bool {
	switch shell {
	case "bash":
		return strings.Contains(output, "ssh) words='") && strings.Contains(output, "connect attach disconnect")
	case "zsh":
		return strings.Contains(output, "ssh) _values 'argument'") && strings.Contains(output, "connect attach disconnect")
	case "fish":
		return strings.Contains(output, "complete -c flclash -f -n '__fish_seen_subcommand_from ssh' -a attach\n")
	default:
		return false
	}
}

func TestLogExportsShareThePersistentLog(t *testing.T) {
	home := t.TempDir()
	const rotatedLines, currentLines, keepLines = 1205, 1200, 2000
	lines := make([]string, rotatedLines+currentLines)
	for index := range lines {
		lines[index] = fmt.Sprintf("persist-%04d", index)
	}
	path := filepath.Join(home, tuiServiceLogFilename)
	if err := os.WriteFile(path+".1", []byte(strings.Join(lines[:rotatedLines], "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines[rotatedLines:], "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	serveRowAlignService(t, func(request tuiServiceRequest) tuiServiceStatus {
		status := alignServiceStatus(7, filepath.Join(home, "config.yaml"))
		if request.Action != "status" {
			status.OK = false
			status.Error = "unexpected action: " + request.Action
		}
		return status
	})
	cliOutput := captureStdout(t, func() error {
		return logsManagedCommand([]string{"export"})
	})
	cliPath := strings.TrimSpace(cliOutput)
	if filepath.Dir(cliPath) != filepath.Join(home, "logs") {
		t.Fatalf("CLI export did not use Backend's isolated home: %q", cliPath)
	}
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
	model.snapshot.Page = tuiPageLogs
	model.snapshot.FocusSidebar = false
	model.snapshot.Logs = []string{"SCREEN ONLY"}
	_, command := model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if command == nil {
		t.Fatal("logs export did not start")
	}
	result := command()
	message, ok := result.(tuiOperationResultMsg)
	if !ok {
		t.Fatalf("logs export returned %T", result)
	}
	if message.state.snapshot.Status == "" || strings.Contains(message.state.snapshot.Status, "SCREEN ONLY") {
		t.Fatalf("tui export status %q", message.state.snapshot.Status)
	}
	cliBody := mustReadFile(t, cliPath)
	matches, err := filepath.Glob(filepath.Join(home, "logs", "flclash-*.log"))
	if err != nil || len(matches) != 2 {
		t.Fatalf("export files %v %v", matches, err)
	}
	var tuiBody []byte
	for _, match := range matches {
		if match == cliPath {
			continue
		}
		tuiBody = mustReadFile(t, match)
	}
	want := strings.Join(lines[len(lines)-keepLines:], "\n") + "\n"
	for source, body := range map[string][]byte{"CLI": cliBody, "TUI": tuiBody} {
		if string(body) != want {
			t.Fatalf("%s export differs from the last %d persistent lines (got %d lines)", source, keepLines, bytes.Count(body, []byte{'\n'}))
		}
		if strings.Contains(string(body), "SCREEN ONLY") {
			t.Fatalf("%s export included the on-screen log buffer", source)
		}
	}
}

func TestStoppedCoreNetworkPortAgrees(t *testing.T) {
	original := detectTUINetworkRouteForCommand
	var probes []string
	detectTUINetworkRouteForCommand = func(proxyURL, label string) tuiNetworkInfo {
		probes = append(probes, proxyURL+"|"+label)
		return tuiNetworkInfo{Route: label}
	}
	t.Cleanup(func() { detectTUINetworkRouteForCommand = original })

	const port = 7890
	want := fmt.Sprintf("PROXY 127.0.0.1:%d", port)
	if got := managedNetworkListenerPort(false, "rule", 0, 0, port); got != port {
		t.Fatalf("listener = %d", got)
	}
	home := t.TempDir()
	serveRowAlignService(t, func(request tuiServiceRequest) tuiServiceStatus {
		status := alignServiceStatus(7, filepath.Join(home, "config.yaml"))
		status.Mode = "rule"
		status.Running = false
		status.ConfiguredProxyPort = port
		if request.Action != "status" {
			status.OK = false
			status.Error = "unexpected action: " + request.Action
		}
		return status
	})
	for _, ownsCore := range []bool{true, false} {
		model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, ownsCore)
		model.coreRunning = false
		model.snapshot.Settings.Mode = "rule"
		model.snapshot.Settings.MixedPort = port
		model.snapshot.ActiveProxyPort = 0
		if model.networkCheckProxyPort() != port {
			t.Fatalf("owns %t preview port %d", ownsCore, model.networkCheckProxyPort())
		}
		command := model.startNetworkCheck()
		if command == nil {
			t.Fatal("network check did not start")
		}
		message, ok := command().(tuiNetworkResultMsg)
		if !ok || message.info.Route != want {
			t.Fatalf("owns %t route %#v", ownsCore, message)
		}
	}
	for _, action := range []string{"show", "refresh"} {
		output := captureStdout(t, func() error { return networkCommand([]string{action}) })
		if !strings.Contains(output, "Route:       "+want+"\n") {
			t.Fatalf("net %s route output %q", action, output)
		}
	}
	if len(probes) != 4 {
		t.Fatalf("network probes = %v, want owner TUI, non-owner TUI, net show, and net refresh", probes)
	}
	for index, probe := range probes {
		if probe != fmt.Sprintf("http://127.0.0.1:%d|%s", port, want) {
			t.Fatalf("probe %d = %q", index, probe)
		}
	}
}

func TestShortDashboardFooterKeepsDelayHint(t *testing.T) {
	snapshot := tuiSnapshot{Page: tuiPageDashboard, Language: "en", FocusSidebar: false}
	output := renderTUIAtSize(snapshot, cliPaths{}, "socket", true, false, 120, 36)
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	footer := stripTUIANSI(lines[len(lines)-1])
	if !strings.Contains(footer, "d delay") {
		t.Fatalf("short dashboard footer = %q", footer)
	}
	profiles := snapshot
	profiles.Page = tuiPageProfiles
	profileOutput := renderTUIAtSize(profiles, cliPaths{}, "socket", true, false, 120, 36)
	profileLines := strings.Split(strings.TrimRight(profileOutput, "\n"), "\n")
	profileFooter := stripTUIANSI(profileLines[len(profileLines)-1])
	if strings.Contains(profileFooter, "d delay") {
		t.Fatalf("profiles footer = %q", profileFooter)
	}

	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	model.service = &tuiServiceClient{}
	model.coreRunning = true
	model.width = 120
	model.height = 36
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.Language = "en"
	model.snapshot.FocusSidebar = false
	model.snapshot.Settings.Mode = "rule"
	model.snapshot.Settings.MixedPort = 7890
	_, command := model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if !model.snapshot.DashboardDelay.Testing {
		t.Fatalf("d status %q testing %t command %t", model.snapshot.Status, model.snapshot.DashboardDelay.Testing, command != nil)
	}
}

func captureStdout(t *testing.T, run func() error) string {
	t.Helper()
	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	runErr := run()
	_ = writer.Close()
	os.Stdout = original
	var buffer bytes.Buffer
	_, copyErr := io.Copy(&buffer, reader)
	_ = reader.Close()
	if runErr != nil {
		t.Fatal(runErr)
	}
	if copyErr != nil {
		t.Fatal(copyErr)
	}
	return buffer.String()
}
