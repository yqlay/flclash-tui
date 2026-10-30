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

func TestTUIRestoresLastVisitedPage(t *testing.T) {
	for page := tuiPageDashboard; page < tuiPageCount; page++ {
		name, ok := tuiPreferencePageName(page)
		if !ok || name == "" {
			t.Fatalf("page %d has no preference name", page)
		}
		got, ok := tuiPageFromPreference(name)
		if !ok || got != page {
			t.Fatalf("preference %q restored %v, %t", name, got, ok)
		}
		got, ok = tuiPageFromPreference("  " + strings.ToUpper(name) + " ")
		if !ok || got != page {
			t.Fatalf("preference %q restored %v, %t", strings.ToUpper(name), got, ok)
		}
	}
	if _, ok := tuiPageFromPreference("requests"); ok {
		t.Fatal("internal page name was accepted")
	}
	if _, ok := tuiPageFromPreference(""); ok {
		t.Fatal("blank page was accepted")
	}

	t.Run("every page key is saved and restored", func(t *testing.T) {
		home := t.TempDir()
		model := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
		model.coreBusy = true
		digits := []rune{'1', '2', '3', '4', '5', '6', '7', '8', '9'}
		for index, digit := range digits {
			page := tuiPage(index)
			_, command := model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{digit}})
			if model.snapshot.Page != page || model.snapshot.SelectedMenu != index {
				t.Fatalf("%c opened page %v menu %d", digit, model.snapshot.Page, model.snapshot.SelectedMenu)
			}
			name, ok := tuiPreferencePageName(page)
			if !ok {
				t.Fatalf("%c has no preference name", digit)
			}
			if page == tuiPageDashboard && command == nil {
				continue
			}
			applyTUIPageSave(t, model, command)
			if got := storedTUIPreferencePage(t, home); got != name {
				t.Fatalf("%c stored %q, want %q", digit, got, name)
			}
			reloaded := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
			if reloaded.snapshot.Page != page || reloaded.snapshot.SelectedMenu != index || reloaded.snapshot.FocusSidebar {
				t.Fatalf("%c reloaded page %v menu %d sidebar %t", digit, reloaded.snapshot.Page, reloaded.snapshot.SelectedMenu, reloaded.snapshot.FocusSidebar)
			}
		}
		model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
		if model.snapshot.Page != tuiPageDashboard {
			t.Fatalf("digit 1 returned to %v", model.snapshot.Page)
		}
		model.stopTrafficMonitor()
		model.stopCoreMemoryMonitor()
		model.shutdown()
		if got := storedTUIPreferencePage(t, home); got != "dashboard" {
			t.Fatalf("return stored %q", got)
		}

		sidebar := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
		sidebar.coreBusy = true
		var command tea.Cmd
		for index := 0; index < int(tuiPageCount); index++ {
			if !sidebar.snapshot.FocusSidebar {
				_, command = sidebar.update(tea.KeyMsg{Type: tea.KeyLeft})
				if command != nil || !sidebar.snapshot.FocusSidebar {
					t.Fatalf("left command %t sidebar %t", command != nil, sidebar.snapshot.FocusSidebar)
				}
			}
			for sidebar.snapshot.SelectedMenu != index {
				_, command = sidebar.update(tea.KeyMsg{Type: tea.KeyDown})
				if command != nil {
					t.Fatal("sidebar move scheduled a save")
				}
			}
			_, command = sidebar.update(tea.KeyMsg{Type: tea.KeyEnter})
			if sidebar.snapshot.Page != tuiPage(index) || sidebar.snapshot.SelectedMenu != index {
				t.Fatalf("sidebar %d opened page %v menu %d", index, sidebar.snapshot.Page, sidebar.snapshot.SelectedMenu)
			}
			name, _ := tuiPreferencePageName(tuiPage(index))
			if index == 0 {
				if command != nil {
					t.Fatal("opening the current dashboard scheduled a save")
				}
				continue
			}
			applyTUIPageSave(t, sidebar, command)
			if got := storedTUIPreferencePage(t, sidebar.paths.HomeDir); got != name {
				t.Fatalf("sidebar %d stored %q, want %q", index, got, name)
			}
			reloaded := newTUIModel(controllerClient{}, cliPaths{HomeDir: sidebar.paths.HomeDir}, nil, true)
			if reloaded.snapshot.Page != tuiPage(index) || reloaded.snapshot.FocusSidebar {
				t.Fatalf("sidebar %d reloaded page %v sidebar %t", index, reloaded.snapshot.Page, reloaded.snapshot.FocusSidebar)
			}
		}
	})

	t.Run("missing file opens dashboard", func(t *testing.T) {
		home := t.TempDir()
		model := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
		if model.snapshot.Page != tuiPageDashboard || model.snapshot.SelectedMenu != int(tuiPageDashboard) || model.snapshot.FocusSidebar {
			t.Fatalf("startup = page %v menu %d sidebar %t", model.snapshot.Page, model.snapshot.SelectedMenu, model.snapshot.FocusSidebar)
		}
		model.shutdown()
		if _, err := os.Stat(filepath.Join(home, tuiPreferencesFilename)); !os.IsNotExist(err) {
			t.Fatal("dashboard startup wrote preferences")
		}
	})

	t.Run("stored page is shown on the next open", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, tuiPreferencesFilename)
		original := []byte("{\"language\":\"zh-Hans\",\"page\":\"connections\",\"future\":{\"value\":1}}\n")
		if err := os.WriteFile(path, original, 0o644); err != nil {
			t.Fatal(err)
		}
		model := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
		if model.snapshot.Page != tuiPageConnections ||
			model.snapshot.SelectedMenu != int(tuiPageConnections) ||
			model.snapshot.FocusSidebar ||
			model.snapshot.Language != "zh-Hans" {
			t.Fatalf("restored page %v menu %d sidebar %t language %s", model.snapshot.Page, model.snapshot.SelectedMenu, model.snapshot.FocusSidebar, model.snapshot.Language)
		}
		view := stripTUIANSI(renderTUIAtSize(model.snapshot, model.paths, "", true, false, 120, 40))
		if !strings.Contains(view, tuiPageName(tuiPageConnections, "zh-Hans")) {
			t.Fatal("restored connections page was not visible")
		}
		if commands := model.syncLiveMonitors(); len(commands) != 0 || model.stopTraffic != nil || model.stopCoreMemory != nil {
			t.Fatal("restored connections page started dashboard monitors")
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o644 {
			t.Fatalf("startup rewrote preferences: %v %v", info, err)
		}
		if string(mustReadFile(t, path)) != string(original) {
			t.Fatal("startup rewrote preferences")
		}

		model.coreBusy = true
		_, command := model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
		if command != nil || model.snapshot.Page != tuiPageConnections {
			t.Fatal("repeating the current page scheduled a save")
		}
		if string(mustReadFile(t, path)) != string(original) {
			t.Fatal("repeating the current page rewrote preferences")
		}

		_, command = model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'6'}})
		if model.snapshot.Page != tuiPageRequests || model.snapshot.SelectedMenu != int(tuiPageRequests) {
			t.Fatalf("digit 6 opened page %v menu %d", model.snapshot.Page, model.snapshot.SelectedMenu)
		}
		saved := runTUIPageSave(t, command)
		if saved.Err != nil || saved.Page != tuiPageRequests {
			t.Fatalf("page save = %+v", saved)
		}
		_, _ = model.update(saved)
		assertTUIPreferencePage(t, home, "history", "zh-Hans", 1)

		if err := saveTUILanguage(home, "ja"); err != nil {
			t.Fatal(err)
		}
		reloaded := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
		if reloaded.snapshot.Page != tuiPageRequests ||
			reloaded.snapshot.SelectedMenu != int(tuiPageRequests) ||
			reloaded.snapshot.FocusSidebar ||
			reloaded.snapshot.Language != "ja" {
			t.Fatalf("reloaded page %v menu %d sidebar %t language %s", reloaded.snapshot.Page, reloaded.snapshot.SelectedMenu, reloaded.snapshot.FocusSidebar, reloaded.snapshot.Language)
		}
	})

	t.Run("unknown or blank page falls back to dashboard", func(t *testing.T) {
		for _, stored := range []string{
			`{"language":"ja","page":"not-a-page"}`,
			`{"language":"ja","page":""}`,
			`{"language":"ja","page":"   "}`,
		} {
			home := t.TempDir()
			if err := os.WriteFile(filepath.Join(home, tuiPreferencesFilename), []byte(stored), 0o600); err != nil {
				t.Fatal(err)
			}
			model := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
			if model.snapshot.Page != tuiPageDashboard ||
				model.snapshot.SelectedMenu != int(tuiPageDashboard) ||
				model.snapshot.Language != "ja" ||
				model.snapshot.FocusSidebar {
				t.Fatalf("fallback for %s: page %v menu %d language %s sidebar %t", stored, model.snapshot.Page, model.snapshot.SelectedMenu, model.snapshot.Language, model.snapshot.FocusSidebar)
			}
		}
	})

	t.Run("sidebar enter and quit keep the page", func(t *testing.T) {
		home := t.TempDir()
		model := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
		model.coreBusy = true
		_, command := model.update(tea.KeyMsg{Type: tea.KeyLeft})
		if command != nil || !model.snapshot.FocusSidebar || model.snapshot.Page != tuiPageDashboard {
			t.Fatalf("left command %t sidebar %t page %v", command != nil, model.snapshot.FocusSidebar, model.snapshot.Page)
		}
		_, command = model.update(tea.KeyMsg{Type: tea.KeyDown})
		if command != nil || model.snapshot.Page != tuiPageDashboard || model.snapshot.SelectedMenu != int(tuiPageSSH) {
			t.Fatalf("down command %t page %v menu %d", command != nil, model.snapshot.Page, model.snapshot.SelectedMenu)
		}
		_, command = model.update(tea.KeyMsg{Type: tea.KeyEnter})
		if model.snapshot.Page != tuiPageSSH || model.snapshot.SelectedMenu != int(tuiPageSSH) {
			t.Fatalf("enter opened page %v menu %d", model.snapshot.Page, model.snapshot.SelectedMenu)
		}
		saved := runTUIPageSave(t, command)
		if saved.Err != nil {
			t.Fatal(saved.Err)
		}
		_, _ = model.update(saved)
		model.shutdown()
		reloaded := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
		if reloaded.snapshot.Page != tuiPageSSH || reloaded.snapshot.FocusSidebar {
			t.Fatalf("reloaded sidebar page %v sidebar %t", reloaded.snapshot.Page, reloaded.snapshot.FocusSidebar)
		}
	})

	t.Run("return to dashboard retires the page just left", func(t *testing.T) {
		home := t.TempDir()
		model := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
		model.coreBusy = true
		_, proxies := model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'3'}})
		if model.snapshot.Page != tuiPageProxies || proxies == nil {
			t.Fatalf("proxies page %v command %t", model.snapshot.Page, proxies != nil)
		}
		model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
		if model.snapshot.Page != tuiPageDashboard {
			t.Fatalf("returned to %v", model.snapshot.Page)
		}
		stale := runTUIPageSave(t, proxies)
		if !errors.Is(stale.Err, errTUIPreferenceSuperseded) {
			t.Fatalf("in-flight proxies save = %+v", stale)
		}
		_, _ = model.update(stale)
		model.stopTrafficMonitor()
		model.stopCoreMemoryMonitor()
		model.shutdown()
		if got := storedTUIPreferencePage(t, home); got != "dashboard" {
			t.Fatalf("stored page = %q", got)
		}
		info, err := os.Stat(filepath.Join(home, tuiPreferencesFilename))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("preferences mode = %v %v", info, err)
		}
		reloaded := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
		if reloaded.snapshot.Page != tuiPageDashboard ||
			reloaded.snapshot.SelectedMenu != int(tuiPageDashboard) ||
			reloaded.snapshot.FocusSidebar {
			t.Fatalf("reloaded page %v menu %d sidebar %t", reloaded.snapshot.Page, reloaded.snapshot.SelectedMenu, reloaded.snapshot.FocusSidebar)
		}
	})

	t.Run("quit writes the page when the save command has not run", func(t *testing.T) {
		home := t.TempDir()
		model := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
		_, command := model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'P'}})
		if model.snapshot.Page != tuiPageProxies || model.snapshot.ProxyView != tuiProxyViewProviders || command == nil {
			t.Fatalf("providers page %v view %d command %t", model.snapshot.Page, model.snapshot.ProxyView, command != nil)
		}
		model.shutdown()
		reloaded := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
		if reloaded.snapshot.Page != tuiPageProxies || reloaded.snapshot.ProxyView != tuiProxyViewGroups || reloaded.snapshot.FocusSidebar {
			t.Fatalf("reloaded proxies page %v view %d sidebar %t", reloaded.snapshot.Page, reloaded.snapshot.ProxyView, reloaded.snapshot.FocusSidebar)
		}
	})

	t.Run("stale save does not replace the newer page", func(t *testing.T) {
		home := t.TempDir()
		if err := os.WriteFile(filepath.Join(home, tuiPreferencesFilename), []byte(`{"language":"en","future":{"value":1}}`), 0o600); err != nil {
			t.Fatal(err)
		}
		model := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
		model.coreBusy = true
		_, first := model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
		_, second := model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}})
		if model.snapshot.Page != tuiPageLogs {
			t.Fatalf("page = %v", model.snapshot.Page)
		}
		saved := runTUIPageSave(t, second)
		if saved.Err != nil || saved.Page != tuiPageLogs {
			t.Fatalf("newer save = %+v", saved)
		}
		_, _ = model.update(saved)
		stale := runTUIPageSave(t, first)
		if !errors.Is(stale.Err, errTUIPreferenceSuperseded) {
			t.Fatalf("stale save = %+v", stale)
		}
		_, _ = model.update(stale)
		if model.persistedPage != tuiPageLogs {
			t.Fatalf("persisted page = %v", model.persistedPage)
		}
		assertTUIPreferencePage(t, home, "logs", "en", 1)
	})

	t.Run("corrupt preferences are left untouched", func(t *testing.T) {
		home := t.TempDir()
		path := filepath.Join(home, tuiPreferencesFilename)
		broken := []byte(`{broken`)
		if err := os.WriteFile(path, broken, 0o600); err != nil {
			t.Fatal(err)
		}
		model := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
		if model.snapshot.Page != tuiPageDashboard || model.snapshot.FocusSidebar {
			t.Fatalf("corrupt preferences opened page %v sidebar %t", model.snapshot.Page, model.snapshot.FocusSidebar)
		}
		model.coreBusy = true
		_, command := model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
		saved := runTUIPageSave(t, command)
		if saved.Err == nil {
			t.Fatal("corrupt preferences were overwritten")
		}
		if string(mustReadFile(t, path)) != string(broken) {
			t.Fatal("corrupt preferences changed")
		}
	})

	t.Run("empty home does not write a preferences file", func(t *testing.T) {
		directory := t.TempDir()
		t.Chdir(directory)
		model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
		model.coreBusy = true
		_, command := model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'5'}})
		if command != nil || model.snapshot.Page != tuiPageConnections {
			t.Fatalf("empty home command %t page %v", command != nil, model.snapshot.Page)
		}
		model.shutdown()
		if _, err := os.Stat(filepath.Join(directory, tuiPreferencesFilename)); !os.IsNotExist(err) {
			t.Fatal("empty home wrote preferences")
		}
	})

	t.Run("restored pages execute the initial row without moving", func(t *testing.T) {
		t.Setenv("VISUAL", "")
		t.Setenv("EDITOR", "")
		for page := tuiPageDashboard; page < tuiPageCount; page++ {
			name, ok := tuiPreferencePageName(page)
			if !ok {
				t.Fatalf("page %v has no preference name", page)
			}
			t.Run(name, func(t *testing.T) {
				home := t.TempDir()
				payload := []byte(`{"language":"en","page":"` + name + `"}` + "\n")
				if err := os.WriteFile(filepath.Join(home, tuiPreferencesFilename), payload, 0o600); err != nil {
					t.Fatal(err)
				}
				model := newTUIModel(controllerClient{}, cliPaths{HomeDir: home, ConfigPath: filepath.Join(home, "config.yaml")}, nil, true)
				t.Cleanup(model.sshOperationCancel)
				wantCursor := 0
				switch page {
				case tuiPageDashboard:
					wantCursor = tuiDashboardServiceRow
				case tuiPageSSH:
					wantCursor = tuiSSHCaptureRow
				case tuiPageProxies:
					model.snapshot.Groups = []tuiGroup{{Name: "ONE", Now: "a", Nodes: []string{"a"}}}
				case tuiPageProfiles:
					wantCursor = tuiProfileImportSubscriptionRow
				case tuiPageConnections:
					model.snapshot.Connections = []tuiConnection{{ID: "c1", Host: "one.example"}}
				case tuiPageRequests:
					model.snapshot.Requests = []tuiRequest{{TuiConnection: tuiConnection{ID: "h1", Host: "one.example"}}}
				case tuiPageLogs:
					model.snapshot.Logs = []string{"one"}
				case tuiPageTools:
					wantCursor = tuiSettingsLanguageRow
				case tuiPageMaintenance:
					wantCursor = tuiMaintenanceEditConfigRow
					model.service = &tuiServiceClient{}
				}
				if model.snapshot.Page != page || model.snapshot.SelectedMenu != int(page) || model.snapshot.FocusSidebar || restoredContentCursor(model) != wantCursor {
					t.Fatalf("startup page %v menu %d sidebar %t cursor %d, want page %v cursor %d", model.snapshot.Page, model.snapshot.SelectedMenu, model.snapshot.FocusSidebar, restoredContentCursor(model), page, wantCursor)
				}
				// Do not set a cursor/focus or press an arrow first: this must
				// exercise the row selected by the shipped startup path.
				_, command := model.update(tea.KeyMsg{Type: tea.KeyEnter})
				if model.snapshot.Page != page || model.snapshot.SelectedMenu != int(page) || model.snapshot.FocusSidebar || restoredContentCursor(model) != wantCursor {
					t.Fatalf("enter changed the restored page/focus/cursor: page %v menu %d sidebar %t cursor %d", model.snapshot.Page, model.snapshot.SelectedMenu, model.snapshot.FocusSidebar, restoredContentCursor(model))
				}
				// Inspect the real key-handler result without executing lazy
				// commands that would start Core or discover external SSH.
				switch page {
				case tuiPageDashboard:
					if command == nil || !model.coreBusy || model.snapshot.Status != "Working..." {
						t.Fatalf("initial Core row: command %t busy %t status %q", command != nil, model.coreBusy, model.snapshot.Status)
					}
				case tuiPageSSH:
					if command == nil || !model.sshCaptureOpen {
						t.Fatal("initial Capture row did not open Capture")
					}
				case tuiPageProxies:
					if command != nil || !model.snapshot.ProxyNodeFocus {
						t.Fatal("initial group row did not open its nodes")
					}
				case tuiPageProfiles:
					if command != nil || model.inputMode != tuiInputSubscription {
						t.Fatalf("initial subscription row opened input %v", model.inputMode)
					}
				case tuiPageConnections:
					if command != nil || !model.snapshot.ConnectionsDetailOpen {
						t.Fatal("initial connection row did not open details")
					}
				case tuiPageRequests:
					if command != nil || !model.snapshot.HistoryDetailOpen {
						t.Fatal("initial history row did not open details")
					}
				case tuiPageLogs:
					if command != nil || !model.snapshot.LogDetailOpen {
						t.Fatal("initial log row did not open details")
					}
				case tuiPageTools:
					if command != nil || !model.languageSelectionOpen {
						t.Fatal("initial Language row did not open language selection")
					}
				case tuiPageMaintenance:
					if command != nil || model.editorBusy || model.coreBusy || model.snapshot.Status != "set $VISUAL or $EDITOR before using config edit" {
						t.Fatalf("initial edit-config row: command %t editor %t busy %t status %q", command != nil, model.editorBusy, model.coreBusy, model.snapshot.Status)
					}
				}
			})
		}
	})

	t.Run("restored pages start on content rows", func(t *testing.T) {
		for page := tuiPageDashboard; page < tuiPageCount; page++ {
			name, ok := tuiPreferencePageName(page)
			if !ok {
				t.Fatalf("page %v has no preference name", page)
			}
			home := t.TempDir()
			payload := []byte(`{"language":"en","page":"` + name + `"}` + "\n")
			if err := os.WriteFile(filepath.Join(home, tuiPreferencesFilename), payload, 0o600); err != nil {
				t.Fatal(err)
			}
			model := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
			if model.snapshot.Page != page || model.snapshot.SelectedMenu != int(page) || model.snapshot.FocusSidebar {
				t.Fatalf("%s opened page %v menu %d sidebar %t", name, model.snapshot.Page, model.snapshot.SelectedMenu, model.snapshot.FocusSidebar)
			}
			switch page {
			case tuiPageProxies:
				model.snapshot.Groups = []tuiGroup{
					{Name: "ONE", Now: "a", Nodes: []string{"a"}},
					{Name: "TWO", Now: "b", Nodes: []string{"b"}},
				}
			case tuiPageConnections:
				model.snapshot.Connections = []tuiConnection{{ID: "c1", Host: "one.example"}, {ID: "c2", Host: "two.example"}}
			case tuiPageRequests:
				model.snapshot.Requests = []tuiRequest{
					{TuiConnection: tuiConnection{ID: "h1", Host: "one.example"}},
					{TuiConnection: tuiConnection{ID: "h2", Host: "two.example"}},
				}
			case tuiPageLogs:
				model.snapshot.Logs = []string{"one", "two"}
			}
			if page == tuiPageSSH {
				if model.snapshot.SelectedSSH != tuiSSHCaptureRow {
					t.Fatalf("%s opened on cursor %d, want capture", name, model.snapshot.SelectedSSH)
				}
				menu := model.snapshot.SelectedMenu
				_, command := model.update(tea.KeyMsg{Type: tea.KeyEnter})
				if model.snapshot.Page != page || model.snapshot.SelectedMenu != menu || model.snapshot.FocusSidebar {
					t.Fatalf("%s enter left page %v menu %d sidebar %t", name, model.snapshot.Page, model.snapshot.SelectedMenu, model.snapshot.FocusSidebar)
				}
				if !restoredContentEnterRan(page, model, command) {
					t.Fatalf("%s enter did not open capture", name)
				}
				continue
			}
			menu := model.snapshot.SelectedMenu
			before := restoredContentCursor(model)
			_, command := model.update(tea.KeyMsg{Type: tea.KeyDown})
			if command != nil || model.snapshot.Page != page || model.snapshot.SelectedMenu != menu || model.snapshot.FocusSidebar {
				t.Fatalf("%s down left page %v menu %d sidebar %t", name, model.snapshot.Page, model.snapshot.SelectedMenu, model.snapshot.FocusSidebar)
			}
			if restoredContentCursor(model) == before {
				t.Fatalf("%s down left the content cursor at %d", name, before)
			}
			_, command = model.update(tea.KeyMsg{Type: tea.KeyEnter})
			if model.snapshot.Page != page || model.snapshot.SelectedMenu != menu || model.snapshot.FocusSidebar {
				t.Fatalf("%s enter left page %v menu %d sidebar %t", name, model.snapshot.Page, model.snapshot.SelectedMenu, model.snapshot.FocusSidebar)
			}
			if !restoredContentEnterRan(page, model, command) {
				t.Fatalf("%s enter did not run the labeled row", name)
			}
		}
	})
}

func restoredContentCursor(model *tuiModel) int {
	switch model.snapshot.Page {
	case tuiPageDashboard:
		return model.snapshot.SelectedDashboard
	case tuiPageSSH:
		return model.snapshot.SelectedSSH
	case tuiPageProxies:
		return model.snapshot.SelectedGroup
	case tuiPageProfiles:
		return model.snapshot.SelectedRow
	case tuiPageConnections:
		return model.snapshot.SelectedConnection
	case tuiPageRequests:
		return model.snapshot.SelectedRequest
	case tuiPageLogs:
		return model.snapshot.SelectedLog
	case tuiPageTools:
		return model.snapshot.SelectedTool
	case tuiPageMaintenance:
		return model.snapshot.SelectedMaintenance
	default:
		return -1
	}
}

func restoredContentEnterRan(page tuiPage, model *tuiModel, command tea.Cmd) bool {
	switch page {
	case tuiPageDashboard:
		return command == nil && model.snapshot.Status != "Loading..."
	case tuiPageSSH:
		return command != nil && model.sshCaptureOpen
	case tuiPageProxies:
		return command == nil && model.snapshot.ProxyNodeFocus
	case tuiPageProfiles:
		return command == nil && model.inputMode == tuiInputProfileFile
	case tuiPageConnections:
		return command == nil && model.snapshot.ConnectionsDetailOpen
	case tuiPageRequests:
		return command == nil && model.snapshot.HistoryDetailOpen
	case tuiPageLogs:
		return command == nil && model.snapshot.LogDetailOpen
	case tuiPageTools:
		return command == nil && model.snapshot.Settings.AllowLAN
	default:
		return command != nil && model.coreBusy && model.snapshot.SelectedMaintenance == tuiMaintenanceBackupRow
	}
}

func applyTUIPageSave(t *testing.T, model *tuiModel, command tea.Cmd) {
	t.Helper()
	saved := runTUIPageSave(t, command)
	if saved.Err != nil {
		t.Fatal(saved.Err)
	}
	_, _ = model.update(saved)
}

func storedTUIPreferencePage(t *testing.T, home string) string {
	t.Helper()
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(mustReadFile(t, filepath.Join(home, tuiPreferencesFilename)), &fields); err != nil {
		t.Fatal(err)
	}
	var page string
	if err := json.Unmarshal(fields["page"], &page); err != nil {
		t.Fatalf("page = %s %v", fields["page"], err)
	}
	return page
}

func runTUIPageSave(t *testing.T, command tea.Cmd) tuiPageSavedMsg {
	t.Helper()
	if command == nil {
		t.Fatal("missing page save")
	}
	message := command()
	saved, ok := message.(tuiPageSavedMsg)
	if !ok {
		t.Fatalf("page save returned %T", message)
	}
	return saved
}

func assertTUIPreferencePage(t *testing.T, home, page, language string, future int) {
	t.Helper()
	path := filepath.Join(home, tuiPreferencesFilename)
	fields := map[string]json.RawMessage{}
	if err := json.Unmarshal(mustReadFile(t, path), &fields); err != nil {
		t.Fatal(err)
	}
	var gotPage, gotLanguage string
	var gotFuture struct {
		Value int `json:"value"`
	}
	if err := json.Unmarshal(fields["page"], &gotPage); err != nil || gotPage != page {
		t.Fatalf("page = %s %v", fields["page"], err)
	}
	if err := json.Unmarshal(fields["language"], &gotLanguage); err != nil || gotLanguage != language {
		t.Fatalf("language = %s %v", fields["language"], err)
	}
	if err := json.Unmarshal(fields["future"], &gotFuture); err != nil || gotFuture.Value != future {
		t.Fatalf("future = %s %v", fields["future"], err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("preferences mode = %v %v", info, err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
