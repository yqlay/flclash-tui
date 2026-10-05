//go:build linux && !cgo && cli

package main

import (
	"core/internal/i18n"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUILocalizedSidebarLabelsRemainVisible(t *testing.T) {
	for _, language := range i18n.Languages() {
		for _, width := range []int{88, 100, 120, 180} {
			layout := tuiLayoutAtSize(width, 40, language.Code)
			if layout.Compact {
				continue
			}
			if layout.ContentWidth+2 < 56 {
				t.Fatal("sidebar consumed the content viewport")
			}
			rows := tuiSidebar(tuiSnapshot{Language: language.Code}, layout.Sidebar, 20, language.Code)
			for index, key := range tuiSidebarKeys {
				line := stripTUIANSI(rows[index+1])
				if !strings.Contains(line, i18n.Text(language.Code, key)) || tuiDisplayWidth(line) != layout.Sidebar || !strings.HasSuffix(strings.TrimSpace(line), "│") {
					t.Fatalf("%s %d: clipped navigation %q", language.Code, width, line)
				}
			}
		}
	}
}

func TestTUILocalizedFieldsAlignInCells(t *testing.T) {
	for _, language := range i18n.Languages() {
		snapshot := populatedTUISnapshot(tuiPageDashboard)
		snapshot.Language = language.Code
		groups := [][]tuiField{
			tuiDashboardControlFields(snapshot, language.Code),
			tuiDashboardNetworkFields(snapshot, language.Code),
			tuiDashboardOverviewFields(snapshot, cliPaths{}, language.Code),
			tuiSettingsFields(snapshot, language.Code),
			tuiSSHFormFields(tuiSSHFormView{Port: 22}, language.Code)[:9],
		}
		for group, fields := range groups {
			for index := range fields {
				fields[index].value = fmt.Sprintf("VALUE_%d", index)
			}
			rows := tuiFieldRows(fields, 180)
			if len(rows) != len(fields) {
				t.Fatalf("%s group %d unexpectedly wrapped", language.Code, group)
			}
			column := -1
			for index, row := range rows {
				position := strings.Index(row.value, fields[index].value)
				if position < 0 || !strings.Contains(row.value, fields[index].label) {
					t.Fatalf("lost field: %q", row.value)
				}
				cells := tuiDisplayWidth(row.value[:position])
				if column >= 0 && column != cells {
					t.Fatalf("%s group %d: value columns %d/%d", language.Code, group, column, cells)
				}
				column = cells
			}
		}
	}
}

func TestTUIResponsiveColumnsPreserveWideNamesAndStatus(t *testing.T) {
	for _, name := range []string{"节点", "👩‍💻é", "क्ष"} {
		line := tuiColumns(60, tuiColumn{value: name, width: 18, minimum: 8}, tuiColumn{value: "CONNECTED", width: 12, minimum: 9}, tuiColumn{value: "host:22", width: 20, minimum: 8})
		position := strings.Index(line, "CONNECTED")
		if position < 0 || tuiDisplayWidth(line[:position]) != 19 {
			t.Fatalf("wide name shifted column: %q", line)
		}
		line = tuiColumns(30, tuiColumn{value: name, width: 18, minimum: 8}, tuiColumn{value: "CONNECTED", width: 12, minimum: 9}, tuiColumn{value: "optional host", width: 20, minimum: 8, optional: true})
		if !strings.Contains(line, name) || !strings.Contains(line, "CONNECTED") || tuiDisplayWidth(line) > 30 {
			t.Fatalf("narrow columns lost important data: %q", line)
		}
	}
}

func TestTUILocalizedOverlayBudgetsKeepControlsAndBorders(t *testing.T) {
	for _, language := range i18n.Languages() {
		for _, size := range [][2]int{{40, 10}, {64, 14}, {88, 18}, {120, 40}} {
			for overlay := 0; overlay < 8; overlay++ {
				snapshot := populatedTUISnapshot(tuiPageSSH)
				snapshot.Language, snapshot.FocusSidebar = language.Code, true
				switch overlay {
				case 0:
					snapshot.LanguageSelectionOpen = true
					snapshot.SelectedLanguage = 17
				case 1:
					snapshot.DangerConfirmOpen = true
					snapshot.DangerConfirmTitle = "CONFIRM"
					snapshot.DangerConfirmMessage = strings.Repeat("Long warning 节点 ", 20)
				case 2:
					snapshot.ProfileDelete = tuiProfileDeleteView{Open: true, Name: "节点.yaml"}
				case 3:
					snapshot.SSHForm = tuiSSHFormView{DeleteConfirmOpen: true, DeleteName: "节点"}
				case 4:
					snapshot.SSHCredentialPrompt = tuiSSHCredentialPromptView{Open: true, Profile: "school", Identity: "/tmp/key", Value: "•••"}
				case 5:
					snapshot.InputTitle = "INPUT"
					snapshot.InputValue = "typed value_"
				case 6:
					snapshot.SelectionTitle = "SELECT"
					snapshot.SelectionOptions = []string{"first", "second", "last"}
					snapshot.SelectedOption = 2
				case 7:
					snapshot.SSHForm = tuiSSHFormView{Open: true, Port: 22, Selected: tuiSSHFormPasswordRow, FieldEditing: true, FieldInput: "masked_"}
				}
				layout := tuiLayoutAtSize(size[0], size[1], language.Code)
				page := stripTUIANSI(tuiRenderPage(snapshot, cliPaths{}, layout.ContentWidth, layout.PageHeight, language.Code))
				lines := strings.Split(strings.TrimSuffix(page, "\n"), "\n")
				if len(lines) > layout.PageHeight || !strings.HasPrefix(lines[len(lines)-1], "└") {
					t.Fatalf("%s %v overlay %d: missing bottom border:\n%s", language.Code, size, overlay, page)
				}
				if !strings.Contains(page, "Enter") || !strings.Contains(page, "Esc") {
					t.Fatalf("%s %v overlay %d: missing controls:\n%s", language.Code, size, overlay, page)
				}
				if overlay == 0 && !strings.Contains(page, "[pcm]") {
					t.Fatalf("language picker clipped the selected code:\n%s", page)
				}
				if overlay == 1 {
					view := stripTUIANSI(renderTUIAtSize(snapshot, cliPaths{}, "socket", true, false, size[0], size[1]))
					if !strings.Contains(view, "CONFIRM") {
						t.Fatal("compact navigation hid the confirmation")
					}
				}
			}
		}
	}
}

func TestTUILanguageAndResizeKeepInputCursorWithoutBackendCommands(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	model.snapshot = populatedTUISnapshot(tuiPageTools)
	model.snapshot.FocusSidebar = false
	model.snapshot.SelectedTool = tuiSettingsLanguageRow
	model.busy = true
	model.inputMode = tuiInputProfileName
	model.inputValue = []rune(strings.Repeat("节点👩‍💻", 40) + "TAIL")
	model.inputCursor = len(model.inputValue)
	for _, language := range i18n.Languages() {
		_, cmd := model.Update(tuiLanguageSavedMsg{Language: language.Code})
		if cmd != nil || !model.busy || model.coreRunning || model.refreshInFlight || model.settingsDirty {
			t.Fatal("locale changed backend state")
		}
		for _, size := range [][2]int{{120, 40}, {88, 18}, {40, 10}, {180, 60}} {
			_, cmd = model.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			view := stripTUIANSI(model.View())
			if cmd != nil || !utf8.ValidString(view) || !strings.Contains(view, "TAIL█") || model.inputCursor != len(model.inputValue) {
				t.Fatalf("%s %v: resize lost input cursor:\n%s", language.Code, size, view)
			}
			if model.snapshot.SelectedTool != tuiSettingsLanguageRow || model.snapshot.FocusSidebar {
				t.Fatal("reflow changed selection/focus")
			}
		}
	}
}

func TestTUILocalizedTrafficChartsUseMeasuredRows(t *testing.T) {
	for _, language := range i18n.Languages() {
		snapshot := populatedTUISnapshot(tuiPageDashboard)
		snapshot.Language = language.Code
		snapshot.ServiceRunning = true
		snapshot.ManagedService = true
		snapshot.Network.PublicIP = "203.0.113.123"
		snapshot.Network.IntranetIP = "192.168.100.123 (eth0)"
		for _, size := range [][2]int{{90, 60}, {120, 80}, {180, 100}} {
			var output strings.Builder
			drawTUIDashboard(&output, snapshot, cliPaths{ConfigPath: "/tmp/config.yaml"}, size[0], size[1], language.Code)
			plain := stripTUIANSI(output.String())
			if strings.Count(plain, "\n")+1 != size[1] || !strings.Contains(plain, snapshot.Network.PublicIP) || !strings.Contains(plain, snapshot.Network.IntranetIP) || !strings.Contains(plain, "└") {
				t.Fatalf("%s %v: rows=%d public=%t intranet=%t: bad measured layout:\n%s", language.Code, size, strings.Count(plain, "\n"), strings.Contains(plain, snapshot.Network.PublicIP), strings.Contains(plain, snapshot.Network.IntranetIP), plain)
			}
		}
	}
}

func TestTUINotificationDetailBodyUsesCurrentLanguage(t *testing.T) {
	snapshot := tuiSnapshot{Language: "zh-Hans", NotificationDetailOpen: true, Notifications: []tuiNotification{{level: tuiNotificationError, message: "Language save failed: sample", text: newTUIMessage("language.save_failed", "sample")}}}
	var output strings.Builder
	drawTUINotificationDetails(&output, snapshot, 80, 20, snapshot.Language)
	if strings.Contains(output.String(), "Language save failed") || strings.Count(output.String(), newTUIMessage("language.save_failed", "sample").text(snapshot.Language)) < 2 {
		t.Fatal("notification list and body used different locales")
	}
}

func TestTUILocalizedSSHFormCursorRemainsVisible(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	model.snapshot.Page = tuiPageSSH
	model.sshFormOpen, model.sshFormFieldEditing = true, true
	model.sshFormSelected = tuiSSHFormHostRow
	model.sshFormInput = []rune(strings.Repeat("节点👩‍💻", 30) + "TAIL")
	model.sshFormCursor = len(model.sshFormInput)
	for _, language := range i18n.Languages() {
		model.snapshot.Language = language.Code
		for _, size := range [][2]int{{40, 10}, {64, 14}, {88, 18}, {180, 60}} {
			_, cmd := model.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			view := stripTUIANSI(model.View())
			if cmd != nil || !strings.Contains(view, "TAIL█") || model.sshFormCursor != len(model.sshFormInput) {
				t.Fatalf("%s %v: clipped SSH form cursor:\n%s", language.Code, size, view)
			}
		}
	}
}

func TestTUILocalizedSSHDashboardUsesMeasuredHeight(t *testing.T) {
	for _, language := range i18n.Languages() {
		snapshot := populatedTUISnapshot(tuiPageSSH)
		snapshot.Language, snapshot.SSHDetailName = language.Code, "school节点"
		snapshot.SSHProfiles = []tuiSSHProfile{{Name: snapshot.SSHDetailName, Destination: "student@192.0.2.5", Port: 22, SocksPort: 10808, Connected: true, Ready: true, Default: true}}
		snapshot.SSHNetwork = tuiNetworkInfo{PublicIP: "203.0.113.123", Country: "HK"}
		snapshot.SSHTraffic = trafficSnapshot{Up: 4096, Down: 32768}
		for _, size := range [][2]int{{64, 40}, {90, 60}, {120, 80}} {
			var output strings.Builder
			drawTUISSH(&output, snapshot, size[0], size[1], language.Code)
			plain := stripTUIANSI(output.String())
			if strings.Count(plain, "\n")+1 != size[1] || !strings.Contains(plain, snapshot.SSHNetwork.PublicIP) || !strings.Contains(plain, "↓ 32.0 KB/s") || !strings.Contains(plain, "↑ 4.0 KB/s") {
				t.Fatalf("%s %v: invalid SSH chart layout:\n%s", language.Code, size, plain)
			}
			for _, line := range strings.Split(plain, "\n") {
				if tuiDisplayWidth(line) != size[0]+2 {
					t.Fatalf("%s: broken SSH border: %q", language.Code, line)
				}
			}
			if testing.Verbose() && size[0] == 64 && (language.Code == "zh-Hans" || language.Code == "fr" || language.Code == "ar" || language.Code == "my") {
				t.Logf("%s SSH preview:\n%s", language.Code, plain)
			}
		}
	}
}

func TestTUINotificationScrollReflowsWithLocaleAndResize(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	model.notificationDetailOpen = true
	model.notifications = []tuiNotification{{level: tuiNotificationError, message: "Language save failed: sample", text: newTUIMessage("language.save_failed", strings.Repeat("节点 example ", 30))}}
	for _, language := range i18n.Languages() {
		model.notificationScroll = 10000
		_, cmd := model.Update(tuiLanguageSavedMsg{Language: language.Code})
		if cmd != nil || model.notificationScroll > model.notificationScrollLimit() {
			t.Fatal("language change left invalid notification scrolling")
		}
		for _, size := range [][2]int{{40, 10}, {88, 18}, {180, 60}} {
			model.notificationScroll = 10000
			_, cmd = model.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
			layout := tuiLayoutAtSize(size[0], size[1], language.Code)
			width, height := model.notificationDetailContentSize()
			if cmd != nil || width != layout.ContentWidth || height != layout.PageHeight || model.notificationScroll > model.notificationScrollLimit() || model.notifications[model.notificationSelected].message != "Language save failed: sample" {
				t.Fatal("notification viewport disagreed with rendering")
			}
		}
	}
}
