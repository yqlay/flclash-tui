//go:build linux && !cgo && cli

package main

import (
	"core/internal/i18n"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func TestTUIPreferencesIndependentAndAtomic(t *testing.T) {
	home := t.TempDir()
	preferences, err := loadTUIPreferences(home)
	if err != nil || preferences.Language != "en" {
		t.Fatalf("missing preferences: %+v %v", preferences, err)
	}
	path := filepath.Join(home, tuiPreferencesFilename)
	if err := os.WriteFile(path, []byte(`{"language":"en","future":{"value":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := saveTUILanguage(home, "zh-Hant"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	var future struct {
		Value int `json:"value"`
	}
	if err := json.Unmarshal(fields["future"], &future); err != nil || future.Value != 1 {
		t.Fatalf("lost unknown settings: %s %v", data, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("insecure preferences: %v %v", info, err)
	}
	preferences, err = loadTUIPreferences(home)
	if err != nil || preferences.Language != "zh-Hant" {
		t.Fatalf("did not reload: %+v %v", preferences, err)
	}
	if err := saveTUILanguage(home, "invalid"); err == nil {
		t.Fatal("invalid language accepted")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(data) {
		t.Fatal("failed save changed the file")
	}
	for _, value := range []string{`{"language":"unknown"}`, `{}`, `null`} {
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		preferences, err = loadTUIPreferences(home)
		if err != nil || preferences.Language != "en" {
			t.Fatalf("safe fallback for %q: %+v %v", value, preferences, err)
		}
	}
	if err := saveTUILanguage(home, "ja"); err != nil {
		t.Fatalf("null preferences should be repairable: %v", err)
	}
	bad := []byte(`{broken`)
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	preferences, err = loadTUIPreferences(home)
	if err == nil || preferences.Language != "en" {
		t.Fatal("malformed preferences did not report failure")
	}
	if err := saveTUILanguage(home, "ja"); err == nil {
		t.Fatal("silently overwrote malformed preferences")
	}
	after, _ = os.ReadFile(path)
	if string(after) != string(bad) {
		t.Fatal("corrupt preferences destroyed")
	}
}

func TestTUIPreferencesConcurrentWriters(t *testing.T) {
	home := t.TempDir()
	var wg sync.WaitGroup
	errors := make(chan error, 18)
	for _, language := range i18n.Languages() {
		wg.Add(1)
		go func(code string) { defer wg.Done(); errors <- saveTUILanguage(home, code) }(language.Code)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := loadTUIPreferences(home); err != nil {
		t.Fatal(err)
	}
}

func TestTUILanguagePickerDoesNotMutateBackend(t *testing.T) {
	home := t.TempDir()
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
	model.snapshot.Page = tuiPageTools
	model.snapshot.FocusSidebar = false
	model.snapshot.SelectedTool = tuiSettingsLanguageRow
	model.busy = true // A separate Core/SSH operation may still be running.
	model.snapshot.setStatus(newTUIMessage("ui.b93900bded31"))
	originalStatus := model.snapshot.Status
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || !model.languageSelectionOpen {
		t.Fatal("language selection requires a backend")
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	if model.snapshot.Language != "en" {
		t.Fatal("highlight changed language")
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if model.languageSelectionOpen {
		t.Fatal("Esc did not close picker")
	}
	if _, err := os.Stat(filepath.Join(home, tuiPreferencesFilename)); !os.IsNotExist(err) {
		t.Fatal("cancel wrote preferences")
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || model.snapshot.Language != "en" {
		t.Fatal("must wait for successful save")
	}
	_, _ = model.Update(cmd())
	if model.snapshot.Language != "zh-Hans" || !model.busy || model.snapshot.Status != originalStatus {
		t.Fatal("language change disturbed in-flight operation")
	}
	if model.coreRunning || model.settingsDirty || model.stopTraffic != nil || model.refreshInFlight {
		t.Fatal("language change started backend work")
	}
	second := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
	if second.snapshot.Language != "zh-Hans" {
		t.Fatal("new frontend did not load preferences")
	}
	_, _ = model.Update(tuiLanguageSavedMsg{Language: "ja", Err: errors.New("disk full")})
	if model.snapshot.Language != "zh-Hans" {
		t.Fatal("failed save changed language")
	}
	if !strings.Contains(stripTUIANSI(model.View()), "Language") {
		t.Fatal("English recovery label missing")
	}
}

func TestTUILocalizedNotificationsRemainStructured(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	data := "ERROR failed /tmp/Language saved.yaml 192.0.2.1"
	message := newTUIMessage("ssh.saved", data)
	model.snapshot.setStatus(message)
	model.publishStatusNotification(tuiTickMsg{}, "")
	if len(model.notifications) != 1 || model.notifications[0].level != tuiNotificationSuccess {
		t.Fatal("classified data as an error")
	}
	for _, language := range i18n.Languages() {
		if got := model.notifications[0].displayMessage(language.Code); !strings.Contains(got, data) {
			t.Fatalf("%s altered data: %q", language.Code, got)
		}
	}
	current := model.snapshot
	current.Language = "ja"
	result := tuiSnapshot{}
	result.setStatus(newTUIMessage("language.save_failed", data))
	merged := mergeTUIOperation(current, result)
	if merged.Language != "ja" || merged.currentMessage().Info.Level != "ERROR" || merged.currentMessage().text("ja") == merged.Status {
		t.Fatal("asynchronous operation lost locale/message")
	}
}

func TestTUIAllLanguagesPagesAndOverlaysFit(t *testing.T) {
	sizes := [][2]int{{1, 1}, {15, 6}, {39, 9}, {40, 10}, {64, 14}, {87, 21}, {88, 22}, {120, 40}, {180, 60}}
	for _, language := range i18n.Languages() {
		for page := tuiPageDashboard; page < tuiPageCount; page++ {
			for overlay := 0; overlay < 10; overlay++ {
				snapshot := populatedTUISnapshot(page)
				snapshot.Language = language.Code
				switch overlay {
				case 1:
					snapshot.LanguageSelectionOpen = true
					snapshot.SelectedLanguage = 17
				case 2:
					snapshot.NotificationDetailOpen = true
					snapshot.Notifications = []tuiNotification{{level: tuiNotificationError, text: newTUIMessage("language.save_failed", "数据 %s"), message: "Language save failed: 数据 %s"}}
				case 3:
					snapshot.ShowHelp = true
				case 4:
					snapshot.SSHForm = tuiSSHFormView{Open: true, Name: "节点 👩‍💻", Host: "192.0.2.1", Port: 22}
				case 5:
					snapshot.ProfileDelete = tuiProfileDeleteView{Open: true, Name: "节点.yaml"}
				case 6:
					snapshot.SSHCredentialPrompt = tuiSSHCredentialPromptView{Open: true, Profile: "é", Identity: "/tmp/密钥", Value: "•••"}
				case 7:
					snapshot.DangerConfirmOpen = true
					snapshot.DangerConfirmTitle = newTUIMessage("danger.connection.title").text(language.Code)
					snapshot.DangerConfirmMessage = newTUIMessage("danger.connection.body", "数据 👩‍💻", "123").text(language.Code)
				case 8:
					snapshot.InputTitle = i18n.Text(language.Code, "language.label")
					snapshot.InputValue = tuiInputViewport([]rune("क्ष 👩‍💻"), 1, 20)
				case 9:
					snapshot.SelectionTitle = "SSH"
					snapshot.SelectionOptions = []string{"节点", "👩‍💻"}
				}
				for _, size := range sizes {
					output := renderTUIAtSize(snapshot, cliPaths{}, "private Unix socket", true, true, size[0], size[1])
					lines := strings.Split(output, "\n")
					if len(lines) != size[1] {
						t.Fatalf("%s page %d overlay %d: wrong height", language.Code, page, overlay)
					}
					for _, line := range lines {
						if !utf8.ValidString(line) || tuiDisplayWidth(line) != size[0] || strings.Contains(line, "%!") {
							t.Fatalf("%s %dx%d page %d overlay %d: invalid row %q", language.Code, size[0], size[1], page, overlay, line)
						}
					}
				}
			}
		}
	}
}

func TestTUIUnicodeClusterEditingAndClipping(t *testing.T) {
	for _, cluster := range []string{"e\u0301", "👩‍💻", "🇨🇳", "ကိ", "की", "ক্ষ", "क्ष", "কি", "یٔ"} {
		t.Run(fmt.Sprintf("%x", []rune(cluster)), func(t *testing.T) {
			model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
			model.inputMode = tuiInputProfileName
			model.inputValue = []rune("a" + cluster + "z")
			model.inputCursor = len([]rune("a" + cluster))
			model.handleInput(tea.KeyMsg{Type: tea.KeyLeft})
			if model.inputCursor != 1 {
				t.Fatalf("cursor split cluster: %d", model.inputCursor)
			}
			model.handleInput(tea.KeyMsg{Type: tea.KeyDelete})
			if string(model.inputValue) != "az" {
				t.Fatalf("partial deletion: %q", model.inputValue)
			}
			model.inputValue = []rune("a" + cluster)
			model.inputCursor = len(model.inputValue)
			model.handleInput(tea.KeyMsg{Type: tea.KeyBackspace})
			if string(model.inputValue) != "a" {
				t.Fatal("partial backspace")
			}
			for width := 1; width < 12; width++ {
				line := tuiClampAnsiLine(tuiRed+cluster+cluster+tuiReset, width)
				if tuiDisplayWidth(line) != width || !strings.HasSuffix(line, tuiReset) {
					t.Fatalf("broken ANSI clipping: %q", line)
				}
				viewport := tuiInputViewport([]rune(cluster+cluster), 1, width)
				if tuiDisplayWidth(viewport) > width || !utf8.ValidString(viewport) {
					t.Fatal(viewport)
				}
				plain := ansi.Strip(line)
				plain = strings.TrimRight(plain, " ")
				if plain != "" && plain != cluster && plain != cluster+cluster {
					t.Fatalf("clipped inside cluster: %q", plain)
				}
			}
		})
	}
}

func TestTUIUnicodeInputJoinedPasteAndInvalidDisplay(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.inputMode = tuiInputProfileName
	model.inputValue = []rune("💻")
	model.handleInput(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("👩‍")})
	if model.inputCursor != len(model.inputValue) {
		t.Fatal("cursor stopped inside joined cluster")
	}
	model.handleInput(tea.KeyMsg{Type: tea.KeyBackspace})
	if len(model.inputValue) != 0 {
		t.Fatal("joined paste was partially deleted")
	}
	if got := tuiClampAnsiLine("\xff\xfd data", 3); !utf8.ValidString(got) || tuiDisplayWidth(got) != 3 {
		t.Fatal("invalid UTF-8 crashed/broke display")
	}
}
