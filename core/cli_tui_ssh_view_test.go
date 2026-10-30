//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func newSSHViewTestModel(t *testing.T) (*tuiModel, cliPaths) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := addCLISSHProfile(cliSSHProfile{Name: "alpha", Username: "user", Host: "example.test", Port: 22}); err != nil {
		t.Fatal(err)
	}
	paths, err := resolvePaths("", "")
	if err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(controllerClient{}, paths, nil, true)
	t.Cleanup(model.sshOperationCancel)
	model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	refreshTUISSH(&model.snapshot)
	model.snapshot.SSHProfiles[0].Connected = true
	model.snapshot.SSHProfiles[0].Ready = true
	model.snapshot.SSHProfiles[0].SocksPort = 12345
	model.update(tea.KeyMsg{Type: tea.KeyDown})
	model.update(tea.KeyMsg{Type: tea.KeyEnter})
	model.update(tea.KeyMsg{Type: tea.KeyDown})
	if model.snapshot.SSHDetailName != "alpha" || !model.snapshot.SSHDashboardFocus || model.snapshot.SSHDetailRow != tuiSSHProxyPortRow {
		t.Fatal("fixture did not open the SSH proxy-port row through Enter")
	}
	return model, paths
}

func TestSSHViewRestoresDetailAfterTabSidebarNavigation(t *testing.T) {
	model, _ := newSSHViewTestModel(t)
	model.update(tea.KeyMsg{Type: tea.KeyTab})
	if !model.snapshot.FocusSidebar {
		t.Fatal("Tab did not reach the sidebar")
	}
	for model.snapshot.SelectedMenu != int(tuiPageLogs) {
		model.update(tea.KeyMsg{Type: tea.KeyDown})
	}
	model.update(tea.KeyMsg{Type: tea.KeyEnter})
	model.update(tea.KeyMsg{Type: tea.KeyLeft})
	for model.snapshot.SelectedMenu != int(tuiPageSSH) {
		model.update(tea.KeyMsg{Type: tea.KeyUp})
	}
	model.update(tea.KeyMsg{Type: tea.KeyEnter})
	if model.snapshot.Page != tuiPageSSH || model.snapshot.FocusSidebar || !model.snapshot.SSHDashboardFocus ||
		model.snapshot.SSHDetailName != "alpha" || model.snapshot.SSHDetailRow != tuiSSHProxyPortRow {
		t.Fatalf("returning through the sidebar lost SSH detail state: page=%v sidebar=%t detail=%t name=%q row=%d", model.snapshot.Page, model.snapshot.FocusSidebar, model.snapshot.SSHDashboardFocus, model.snapshot.SSHDetailName, model.snapshot.SSHDetailRow)
	}
}

func TestSSHViewRestoresOpenedProfileAfterQuitAndRestart(t *testing.T) {
	model, paths := newSSHViewTestModel(t)
	model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model.shutdown()
	reopened := newTUIModel(controllerClient{}, paths, nil, true)
	t.Cleanup(reopened.sshOperationCancel)
	refresh := reopened.startSSHLocalRefresh()
	if refresh == nil {
		t.Fatal("restored SSH tab did not start its local refresh")
	}
	reopened.update(refresh())
	if reopened.snapshot.Page != tuiPageSSH || reopened.snapshot.FocusSidebar ||
		reopened.snapshot.SSHDetailName != "alpha" || !reopened.snapshot.SSHDashboardFocus ||
		reopened.snapshot.SSHDetailRow != tuiSSHProxyPortRow || reopened.snapshot.SelectedSSH != 0 {
		t.Fatalf("restart lost SSH view state: page=%v sidebar=%t detail=%t name=%q row=%d selected=%d", reopened.snapshot.Page, reopened.snapshot.FocusSidebar, reopened.snapshot.SSHDashboardFocus, reopened.snapshot.SSHDetailName, reopened.snapshot.SSHDetailRow, reopened.snapshot.SelectedSSH)
	}
	if reopened.sshBusy || reopened.snapshot.SSHProfiles[0].Connected {
		t.Fatal("restoring the SSH view started a connection")
	}
	reopened.update(tea.WindowSizeMsg{Width: 150, Height: 50})
	view := stripTUIANSI(reopened.View())
	if !strings.Contains(view, "SSH · alpha") || !strings.Contains(view, "HTTP/SOCKS") {
		t.Fatalf("restored SSH detail was not rendered:\n%s", view)
	}
}

func TestSSHViewRestoresWhenRestartedOnAnotherPage(t *testing.T) {
	model, paths := newSSHViewTestModel(t)
	model.update(tea.KeyMsg{Type: tea.KeyTab})
	model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}})
	model.shutdown()

	reopened := newTUIModel(controllerClient{}, paths, nil, true)
	t.Cleanup(reopened.sshOperationCancel)
	if reopened.snapshot.Page != tuiPageLogs {
		t.Fatal("restart did not restore Logs")
	}
	// Saving another tab before SSH's first local refresh must retain the
	// pending profile identities rather than replace them with the empty list.
	reopened.coreBusy = true
	_, command := reopened.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'6'}})
	applyTUIPageSave(t, reopened, command)
	reopened.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	command = reopened.startSSHLocalRefresh()
	if command == nil {
		t.Fatal("SSH local refresh was not available")
	}
	reopened.update(command())
	if !reopened.snapshot.SSHDashboardFocus || reopened.snapshot.SSHDetailName != "alpha" || reopened.snapshot.SSHDetailRow != tuiSSHProxyPortRow {
		t.Fatalf("another startup page lost SSH state: %+v", reopened.currentSSHView())
	}
}

func TestSSHViewKeepsHighlightSeparateFromOpenedProfile(t *testing.T) {
	model, paths := newSSHViewTestModel(t)
	model.snapshot.SSHProfiles = append(model.snapshot.SSHProfiles, tuiSSHProfile{Name: "beta"})
	model.update(tea.KeyMsg{Type: tea.KeyEsc})
	model.update(tea.KeyMsg{Type: tea.KeyDown})
	if model.selectedSSHName() != "beta" || model.snapshot.SSHDetailName != "alpha" || model.snapshot.SSHDashboardFocus {
		t.Fatal("moving the highlight changed the opened profile")
	}
	model.shutdown()

	reopened := newTUIModel(controllerClient{}, paths, nil, true)
	t.Cleanup(reopened.sshOperationCancel)
	// The local list order changed while the frontend was closed.
	reopened.update(tuiSSHLocalRefreshMsg{profiles: []tuiSSHProfile{{Name: "beta"}, {Name: "alpha"}}})
	if reopened.snapshot.SelectedSSH != 0 || reopened.snapshot.SSHDetailName != "alpha" || reopened.snapshot.SSHDashboardFocus {
		t.Fatalf("profile identities were replaced by row numbers: %+v", reopened.currentSSHView())
	}
	if reopened.sshBusy {
		t.Fatal("restoring the list highlight connected a profile")
	}
}

func TestSSHViewHandlesMissingProfilesAndDisconnectedRows(t *testing.T) {
	for _, tc := range []struct {
		name     string
		profiles []tuiSSHProfile
		opened   string
		selected int
	}{
		{name: "removed profile", profiles: []tuiSSHProfile{{Name: "other"}}, selected: tuiSSHCaptureRow},
		{name: "empty list", profiles: []tuiSSHProfile{}, selected: tuiSSHCaptureRow},
		{name: "disconnected", profiles: []tuiSSHProfile{{Name: "alpha"}}, opened: "alpha", selected: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model, paths := newSSHViewTestModel(t)
			for model.snapshot.SSHDetailRow != tuiSSHSpeedRow {
				model.update(tea.KeyMsg{Type: tea.KeyDown})
			}
			model.shutdown()
			reopened := newTUIModel(controllerClient{}, paths, nil, true)
			t.Cleanup(reopened.sshOperationCancel)
			reopened.update(tuiSSHLocalRefreshMsg{profiles: tc.profiles})
			if reopened.pendingSSHView != nil || reopened.snapshot.SSHDetailName != tc.opened || reopened.snapshot.SelectedSSH != tc.selected ||
				reopened.snapshot.SSHDetailRow != tuiSSHTunnelRow || reopened.snapshot.SSHDashboardFocus != (tc.opened != "") || reopened.sshBusy {
				t.Fatalf("invalid saved detail was not reset safely: %+v", reopened.currentSSHView())
			}
		})
	}
}

func TestSSHViewNewSelectionSupersedesPendingRestore(t *testing.T) {
	model, paths := newSSHViewTestModel(t)
	model.shutdown()
	reopened := newTUIModel(controllerClient{}, paths, nil, true)
	t.Cleanup(reopened.sshOperationCancel)
	// The user acts before the asynchronous startup refresh completes.
	reopened.snapshot.SSHProfiles = []tuiSSHProfile{{Name: "beta", Connected: true, Ready: true}}
	reopened.update(tea.KeyMsg{Type: tea.KeyDown})
	reopened.update(tea.KeyMsg{Type: tea.KeyEnter})
	reopened.update(tuiSSHLocalRefreshMsg{profiles: []tuiSSHProfile{{Name: "alpha"}, {Name: "beta"}}})
	if reopened.snapshot.SSHDetailName != "beta" || reopened.selectedSSHName() != "beta" || !reopened.snapshot.SSHDashboardFocus {
		t.Fatalf("startup restore overwrote the user's selection: %+v", reopened.currentSSHView())
	}
}

func TestSSHViewSavesWithoutDiscardingOtherPreferences(t *testing.T) {
	model, paths := newSSHViewTestModel(t)
	path := filepath.Join(paths.HomeDir, tuiPreferencesFilename)
	if err := os.WriteFile(path, []byte(`{"language":"zh-Hant","future":{"value":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	model.shutdown()
	assertTUIPreferencePage(t, paths.HomeDir, "ssh", "zh-Hant", 1)
	want := model.currentSSHView()
	if err := saveTUILanguage(paths.HomeDir, "ja"); err != nil {
		t.Fatal(err)
	}
	preferences, err := loadTUIPreferences(paths.HomeDir)
	if err != nil || preferences.SSHView == nil || *preferences.SSHView != want || preferences.Language != "ja" {
		t.Fatalf("language save lost SSH state: %+v %v", preferences, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(mustReadFile(t, path), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 {
		t.Fatalf("view preferences contain unexpected fields: %v", fields)
	}
	// A second frontend that did not change its SSH view owns only the page
	// key, even if a first frontend has since saved another detail row.
	other := newTUIModel(controllerClient{}, paths, nil, true)
	t.Cleanup(other.sshOperationCancel)
	model.snapshot.SSHDetailRow = tuiSSHTunnelRow
	model.flushVisiblePage()
	other.coreBusy = true
	_, command := other.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}})
	applyTUIPageSave(t, other, command)
	preferences, err = loadTUIPreferences(paths.HomeDir)
	if err != nil || preferences.SSHView == nil || preferences.SSHView.DetailRow != tuiSSHTunnelRow {
		t.Fatalf("another frontend's tab save replaced the newer SSH view: %+v %v", preferences, err)
	}
}

func TestSSHViewQuitRetiresPendingSaveEvenAfterReturningToSavedView(t *testing.T) {
	model, paths := newSSHViewTestModel(t)
	model.flushVisiblePage()
	want := model.currentSSHView()
	model.update(tea.KeyMsg{Type: tea.KeyDown})
	command := model.persistVisiblePage()
	// The asynchronous save reached disk, but its reply is still queued when
	// the user returns to the previously persisted row and quits.
	saved := runTUIPageSave(t, command)
	if saved.Err != nil {
		t.Fatal(saved.Err)
	}
	model.update(tea.KeyMsg{Type: tea.KeyUp})
	model.shutdown()
	preferences, err := loadTUIPreferences(paths.HomeDir)
	if err != nil || preferences.SSHView == nil || *preferences.SSHView != want {
		t.Fatalf("quit retained the in-flight view instead of the visible view: %+v %v", preferences, err)
	}
	stale := runTUIPageSave(t, command)
	if !errors.Is(stale.Err, errTUIPreferenceSuperseded) {
		t.Fatalf("queued view save remained current after quit: %v", stale.Err)
	}
}
