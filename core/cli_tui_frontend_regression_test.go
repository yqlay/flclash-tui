//go:build linux && !cgo && cli

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIFrontendHighlightCaptureKeepsOpenedDashboard(t *testing.T) {
	model, _ := newSSHViewTestModel(t)
	model.update(tea.WindowSizeMsg{Width: 150, Height: 50})
	model.update(tea.KeyMsg{Type: tea.KeyEsc})
	model.update(tea.KeyMsg{Type: tea.KeyUp})
	if model.snapshot.SelectedSSH != tuiSSHCaptureRow || model.snapshot.SSHDetailName != "alpha" {
		t.Fatal("fixture did not highlight Capture without activating it")
	}
	if view := stripTUIANSI(model.View()); !strings.Contains(view, "SSH · alpha") {
		t.Fatal("highlighting Capture hid the opened alpha dashboard without Enter")
	}
	model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !model.sshCaptureOpen {
		t.Fatal("Enter on Capture did not open discovery")
	}
	model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if model.sshCaptureOpen || !strings.Contains(stripTUIANSI(model.View()), "SSH · alpha") {
		t.Fatal("cancelling Capture did not return to the opened dashboard")
	}
}

func TestTUIFrontendConcurrentOperationKeepsSSHError(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	t.Cleanup(model.sshOperationCancel)
	model.snapshot.SSHDetailName = "alpha"
	model.coreBusy = true
	model.syncLaneBusy()
	model.Update(tuiSSHDelayResultMsg{name: "alpha", err: errors.New("review SSH failure")})
	if len(model.notifications) != 1 {
		t.Fatal("SSH failure notification was not recorded")
	}
	if model.notifications[0].progress || model.notifications[0].level != tuiNotificationError {
		t.Fatal("another running task turned the SSH failure into progress")
	}
	state := tuiOperationState{
		snapshot: cloneTUISnapshot(model.snapshot),
		paths:    model.paths,
	}
	state.snapshot.setStatus(newTUIMessage("ui.7b3279fdc1ff"))
	model.Update(tuiOperationResultMsg{state: state})
	for _, notification := range model.notifications {
		if strings.Contains(notification.message, "review SSH failure") {
			return
		}
	}
	t.Fatal("Core completion erased the independent SSH failure from notification history")
}

func TestTUIFrontendPreferenceFailureIsVisible(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	t.Cleanup(model.sshOperationCancel)
	blocked := filepath.Join(t.TempDir(), "ordinary-file")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	model.paths.HomeDir = blocked
	model.snapshot.Page = tuiPageSSH
	saved := runTUIPageSave(t, model.persistVisiblePage())
	if saved.Err == nil {
		t.Fatal("fixture did not produce a preferences write error")
	}
	before := model.snapshot.Status
	model.Update(saved)
	if len(model.notifications) == 0 && model.snapshot.Status == before {
		t.Fatal("preferences save failed without any visible notification or status")
	}
}

func TestTUIFrontendCoalescedKeyBatchRespectsNewInput(t *testing.T) {
	for _, tc := range []struct {
		keys  string
		page  tuiPage
		mode  tuiInputMode
		value string
		paste bool
		exit  bool
	}{
		{keys: "p80", page: tuiPageDashboard, mode: tuiInputMixedPort, value: "80"},
		{keys: "p80q", page: tuiPageDashboard, mode: tuiInputMixedPort, value: "80"},
		{keys: "6/qs123", page: tuiPageRequests, mode: tuiInputHistorySearch, value: "qs123"},
		{keys: "6/👩‍💻e\u0301", page: tuiPageRequests, mode: tuiInputHistorySearch, value: "👩‍💻e\u0301"},
		{keys: "q8", page: tuiPageDashboard, exit: true},
		{keys: "mq8", page: tuiPageDashboard, exit: true},
		{keys: "p80", page: tuiPageDashboard, paste: true},
	} {
		t.Run(tc.keys+map[bool]string{true: " paste"}[tc.paste], func(t *testing.T) {
			model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
			t.Cleanup(model.sshOperationCancel)
			model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(tc.keys), Paste: tc.paste})
			if model.snapshot.Page != tc.page || model.inputMode != tc.mode || string(model.inputValue) != tc.value || model.frontendExitRequested != tc.exit {
				t.Fatalf("coalesced %q escaped its context: page=%v inputMode=%v value=%q exit=%t",
					tc.keys, model.snapshot.Page, model.inputMode, string(model.inputValue), model.frontendExitRequested)
			}
		})
	}
}

func TestTUIFrontendConcurrentWritesKeepTheirOwnNotifications(t *testing.T) {
	model, _ := newSSHViewTestModel(t)
	model.Update(tea.KeyMsg{Type: tea.KeyUp})
	model.Update(tea.KeyMsg{Type: tea.KeyEnter})                     // Begin SSH disconnect; do not execute it.
	model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'R'}}) // Begin Core reload.
	if !model.sshBusy || !model.coreBusy || len(model.notifications) != 2 ||
		!model.notifications[0].progress || !model.notifications[1].progress {
		t.Fatal("parallel writes did not have independent progress notices")
	}
	model.Update(tuiSSHCommandResultMsg{
		action: "disconnect", sequence: model.sshWriteSequence,
		selectedName: "alpha", err: errors.New("independent SSH write failed"),
	})
	if len(model.notifications) != 2 || model.notifications[0].progress ||
		model.notifications[0].level != tuiNotificationError || !model.notifications[1].progress {
		t.Fatal("SSH completion consumed or inherited Core progress")
	}
	state := tuiOperationState{operationID: model.operationSequence, snapshot: cloneTUISnapshot(model.snapshot), paths: model.paths}
	state.snapshot.setStatus(newTUIMessage("ui.7b3279fdc1ff"))
	model.Update(tuiOperationResultMsg{state: state})
	if len(model.notifications) != 2 || model.notifications[0].progress || model.notifications[1].progress ||
		!strings.Contains(model.notifications[1].message, "independent SSH write failed") {
		t.Fatal("Core completion erased SSH's completed failure")
	}
}

func TestTUIFrontendIdenticalProgressDoesNotMergeDifferentTasks(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	t.Cleanup(model.sshOperationCancel)
	for _, task := range []string{"core", "ssh-write"} {
		model.enqueueNotification(tuiNotification{id: task, level: tuiNotificationInfo, message: "Working...", progress: true})
	}
	if len(model.notifications) != 2 {
		t.Fatal("identical text merged independently owned progress")
	}
	model.enqueueNotification(tuiNotification{id: "core", level: tuiNotificationSuccess, message: "Core complete"})
	if len(model.notifications) != 2 || model.notifications[0].progress || !model.notifications[1].progress {
		t.Fatal("a task completion replaced the other task's progress")
	}
	model.enqueueNotification(tuiNotification{id: "core", level: tuiNotificationInfo, message: "Working...", progress: true})
	if len(model.notifications) != 3 || model.notifications[1].message != "Core complete" {
		t.Fatal("a new task overwrote the previously completed task")
	}
}

func TestTUIFrontendPreferenceWarningRetriesWithoutFlooding(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	t.Cleanup(model.sshOperationCancel)
	blocked := filepath.Join(t.TempDir(), "ordinary-file")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	model.paths.HomeDir = blocked
	// Even a failed rewrite of the last saved tab must retry: an older
	// asynchronous save may already have put another tab on disk.
	model.snapshot.Page = tuiPageDashboard
	model.Update(runTUIPageSave(t, model.persistVisiblePage()))
	if len(model.notifications) != 1 || model.notifications[0].level != tuiNotificationWarning || model.notifications[0].progress {
		t.Fatal("preference failure was not a standalone warning")
	}
	model.notifications[0].acknowledged = true
	model.Update(runTUIPageSave(t, model.persistVisiblePage()))
	if len(model.notifications) != 1 || !model.notifications[0].acknowledged {
		t.Fatal("repeated persistence failure flooded or reopened the warning")
	}
	model.paths.HomeDir = t.TempDir()
	model.coreBusy = true // Ignore Backend work; execute only the preference command.
	batch, ok := model.idleTickCommand()().(tea.BatchMsg)
	if !ok {
		t.Fatal("idle retry did not schedule its command batch")
	}
	retried := false
	for _, command := range batch {
		if saved, ok := command().(tuiPageSavedMsg); ok {
			if saved.Err != nil {
				t.Fatal(saved.Err)
			}
			model.Update(saved)
			retried = true
		}
	}
	if !retried || model.persistedPage != tuiPageDashboard || model.viewSaveFailed {
		t.Fatal("recovery did not retry the failed page save on idle")
	}
	model.paths.HomeDir = blocked
	model.snapshot.Page = tuiPageSSH
	model.Update(runTUIPageSave(t, model.persistVisiblePage()))
	if len(model.notifications) != 1 || model.notifications[0].acknowledged {
		t.Fatal("a new failure after recovery did not refresh the warning")
	}
	model.snapshot.Language = "zh-Hans"
	if !strings.Contains(model.notifications[0].displayMessage(model.snapshot.Language), "无法保存界面状态") {
		t.Fatal("preference warning did not follow the current UI language")
	}
	if err := model.shutdown(); err == nil {
		t.Fatal("shutdown hid the final unsaved-state error")
	}
}
