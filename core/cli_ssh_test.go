//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestParseCLISSHProfileEdit(t *testing.T) {
	edit, interactive, err := parseCLISSHProfileEdit([]string{
		"school",
		"student@example.edu",
		"--port",
		"2222",
		"--local-port",
		"1080",
		"--identity",
		"/tmp/id_ed25519",
		"--option",
		"StrictHostKeyChecking=yes",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if interactive || edit.Name != "school" ||
		edit.Destination != "student@example.edu" || edit.Port != 2222 ||
		edit.Identity != "/tmp/id_ed25519" || edit.LocalPort != 1080 ||
		!edit.LocalPortSet || len(edit.Options) != 1 {
		t.Fatalf("unexpected parsed profile: %+v", edit)
	}
}

func TestCLISSHConfigIsPrivateAndViewsMaskPassword(t *testing.T) {
	configRoot := t.TempDir()
	runtimeRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = runtimeRoot
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previousRuntime })

	err := updateCLISSHConfig(func(config *cliSSHConfig) error {
		config.Profiles = []cliSSHProfile{{
			Name:        "school",
			Destination: "student@example.edu",
			Port:        22,
			LocalPort:   1080,
			Password:    "do-not-display",
		}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(configRoot, "flclash", cliSSHConfigFilename)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("SSH config mode = %o, want 0600", info.Mode().Perm())
	}
	views, err := loadCLISSHProfileViews()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(views)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || !views[0].PasswordSet ||
		views[0].LocalPort != 1080 ||
		strings.Contains(string(encoded), "do-not-display") {
		t.Fatalf("password leaked through view: %s", encoded)
	}
}

func TestTUISSHPageRendersMaskedAuthentication(t *testing.T) {
	snapshot := tuiSnapshot{
		Page:         tuiPageSSH,
		SelectedMenu: int(tuiPageSSH),
		SSHProfiles: []tuiSSHProfile{{
			Name:        "school",
			Destination: "student@example.edu",
			Port:        22,
			LocalPort:   1080,
			PasswordSet: true,
			Connected:   true,
			SocksPort:   45678,
		}},
	}
	output := stripTUIANSI(renderTUIAtSize(
		snapshot,
		cliPaths{},
		"private Unix socket",
		true,
		false,
		180,
		30,
	))
	for _, expected := range []string{
		"SSH",
		"school",
		"CONNECTED",
		"password ********",
		"SOCKS5 127.0.0.1:45678",
		"configured 1080",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("SSH page does not contain %q:\n%s", expected, output)
		}
	}
}

func TestCLISSHRejectsManagedForwardingOptions(t *testing.T) {
	for _, option := range []string{
		"ControlPath=/tmp/socket",
		"DynamicForward=127.0.0.1:9999",
		"LocalCommand=id",
	} {
		if err := validateCLISSHOption(option); err == nil {
			t.Fatalf("managed option %q was accepted", option)
		}
	}
}

func TestRunCLICommandWithSSHProxySetsEnvironmentAndExitCode(t *testing.T) {
	err := runCLICommandWithSSHProxy([]string{
		"sh",
		"-c",
		"test \"$ALL_PROXY\" = socks5h://127.0.0.1:45678 && exit 7",
	}, 45678)
	var exitError *cliExitCodeError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 7 {
		t.Fatalf("SSH wrapper exit = %v, want exit code 7", err)
	}
}

func TestTUISSHAddAndEditStayInsideTUI(t *testing.T) {
	configRoot := t.TempDir()
	runtimeRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = runtimeRoot
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previousRuntime })

	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Page = tuiPageSSH
	model.snapshot.SelectedMenu = int(tuiPageSSH)
	model.snapshot.FocusSidebar = false
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if command != nil {
		t.Fatal("SSH add left the TUI through an external command")
	}
	if !model.sshFormOpen || model.sshFormExisting {
		t.Fatalf("SSH add form state = open %t existing %t", model.sshFormOpen, model.sshFormExisting)
	}
	model.sshForm = cliSSHProfile{
		Name:        "school",
		Destination: "student@example.edu",
		Port:        2222,
		LocalPort:   1080,
		Identity:    "/tmp/id_ed25519",
		Password:    "never-render-this",
		Options:     []string{"StrictHostKeyChecking=yes"},
	}
	model.sshFormPasswordChanged = true
	model.sshFormSelected = model.sshFormSaveRow()
	command = model.activateSSHFormRow()
	if command == nil {
		t.Fatal("SSH form save did not schedule a config write")
	}
	message, ok := command().(tuiSSHCommandResultMsg)
	if !ok || message.err != nil {
		t.Fatalf("SSH form save result = %#v", message)
	}
	_, _ = model.Update(message)
	profile, err := loadCLISSHProfile("school")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Destination != "student@example.edu" || profile.Port != 2222 ||
		profile.LocalPort != 1080 ||
		profile.Password != "never-render-this" || len(profile.Options) != 1 {
		t.Fatalf("saved SSH profile = %+v", profile)
	}
	if strings.Contains(model.View(), profile.Password) {
		t.Fatal("SSH password leaked through the TUI view")
	}

	model.beginSSHForm(true)
	if !model.sshFormOpen || !model.sshFormExisting ||
		model.sshForm.Password != "never-render-this" {
		t.Fatalf("SSH edit form did not load the selected profile: %+v", model.sshForm)
	}
	model.sshForm.Destination = "new@example.edu"
	model.sshFormSelected = model.sshFormSaveRow()
	command = model.activateSSHFormRow()
	message = command().(tuiSSHCommandResultMsg)
	if message.err != nil {
		t.Fatal(message.err)
	}
	profile, err = loadCLISSHProfile("school")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Destination != "new@example.edu" || profile.Password != "never-render-this" {
		t.Fatalf("SSH edit did not preserve password: %+v", profile)
	}
}

func TestCLISSHLocalPortPolicy(t *testing.T) {
	profile := cliSSHProfile{LocalPort: 1080}
	if port := configuredCLISSHLocalPort(profile, "persistent"); port != 1080 {
		t.Fatalf("persistent local port = %d, want 1080", port)
	}
	if port := configuredCLISSHLocalPort(profile, "transient"); port != 0 {
		t.Fatalf("transient local port = %d, want automatic", port)
	}
	for _, value := range []string{"auto", "0"} {
		if port, err := parseCLISSHLocalPort(value); err != nil || port != 0 {
			t.Fatalf("parse local port %q = %d, %v", value, port, err)
		}
	}
	for _, value := range []string{"-1", "65536", "invalid"} {
		if _, err := parseCLISSHLocalPort(value); err == nil {
			t.Fatalf("invalid local port %q was accepted", value)
		}
	}

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := checkCLISSHPortAvailable(port); err == nil ||
		!strings.Contains(err.Error(), "already in use") {
		t.Fatalf("occupied local port check = %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := checkCLISSHPortAvailable(port); err != nil {
		t.Fatalf("released local port was unavailable: %v", err)
	}
}

func TestTUISSHFormPasswordAndOptionEditing(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.sshFormOpen = true
	model.sshForm = cliSSHProfile{
		Name:        "school",
		Destination: "student@example.edu",
		Port:        22,
		Password:    "old-secret",
	}
	model.sshFormSelected = tuiSSHFormLocalPortRow
	model.beginSSHFormFieldEdit()
	model.sshFormInput = []rune("1080")
	model.sshFormCursor = len(model.sshFormInput)
	if !model.commitSSHFormField() || model.sshForm.LocalPort != 1080 {
		t.Fatalf("fixed local SOCKS5 port was not staged: %d", model.sshForm.LocalPort)
	}
	model.sshFormSelected = tuiSSHFormLocalPortRow
	model.beginSSHFormFieldEdit()
	model.sshFormInput = []rune("auto")
	model.sshFormCursor = len(model.sshFormInput)
	if !model.commitSSHFormField() || model.sshForm.LocalPort != 0 {
		t.Fatalf("automatic local SOCKS5 port was not staged: %d", model.sshForm.LocalPort)
	}
	model.sshFormSelected = tuiSSHFormPasswordRow
	model.beginSSHFormFieldEdit()
	model.sshFormInput = []rune("new-secret")
	model.sshFormCursor = len(model.sshFormInput)
	if model.commitSSHFormField() {
		t.Fatal("password was committed without confirmation")
	}
	if !model.sshFormPasswordConfirm {
		t.Fatal("password confirmation phase was not entered")
	}
	if strings.Contains(model.View(), "new-secret") || strings.Contains(model.View(), "old-secret") {
		t.Fatal("SSH password leaked while editing")
	}
	model.sshFormInput = []rune("new-secret")
	model.sshFormCursor = len(model.sshFormInput)
	if !model.commitSSHFormField() || model.sshForm.Password != "new-secret" {
		t.Fatal("confirmed password was not staged")
	}
	model.sshFormSelected = tuiSSHFormPasswordRow
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	if model.sshForm.Password != "" || !model.sshFormPasswordCleared {
		t.Fatal("password clear state was not staged")
	}

	model.sshFormSelected = model.sshFormAddOptionRow()
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !model.sshFormFieldEditing || len(model.sshForm.Options) != 1 {
		t.Fatal("add-option row did not open an in-TUI field")
	}
	model.sshFormInput = []rune("ControlPath=/tmp/unsafe")
	model.sshFormCursor = len(model.sshFormInput)
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !model.sshFormFieldEditing || !strings.Contains(model.snapshot.Status, "conflicts") {
		t.Fatalf("unsafe option was accepted: %q", model.snapshot.Status)
	}
	model.sshFormInput = []rune("ServerAliveInterval=30")
	model.sshFormCursor = len(model.sshFormInput)
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if model.sshFormFieldEditing || model.sshForm.Options[0] != "ServerAliveInterval=30" {
		t.Fatalf("valid option was not staged: %+v", model.sshForm.Options)
	}
}

func TestTUISSHDeleteRequiresConfirmation(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Page = tuiPageSSH
	model.snapshot.FocusSidebar = false
	model.snapshot.SSHProfiles = []tuiSSHProfile{{Name: "school"}}
	model.snapshot.SelectedSSH = 0
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if command != nil || !model.sshDeleteConfirmOpen {
		t.Fatalf("SSH delete did not open confirmation: command=%v open=%t", command, model.sshDeleteConfirmOpen)
	}
	if !strings.Contains(stripTUIANSI(model.View()), "Delete school?") {
		t.Fatal("SSH delete confirmation did not render its target")
	}
	_, command = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if command != nil || model.sshDeleteConfirmOpen {
		t.Fatal("SSH delete confirmation did not cancel in place")
	}
}

func TestTUISSHEditFormDeleteAction(t *testing.T) {
	configRoot := t.TempDir()
	runtimeRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = runtimeRoot
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previousRuntime })
	if err := addCLISSHProfile(cliSSHProfile{
		Name:        "school",
		Destination: "student@example.edu",
		Port:        22,
	}); err != nil {
		t.Fatal(err)
	}

	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Page = tuiPageSSH
	model.snapshot.FocusSidebar = false
	refreshTUISSH(&model.snapshot)
	model.beginSSHForm(true)
	if row := model.sshFormDeleteRow(); row < 0 || row >= model.sshFormRowCount() {
		t.Fatalf("SSH edit delete row = %d of %d", row, model.sshFormRowCount())
	}
	plain := stripTUIANSI(model.View())
	if !strings.Contains(plain, "Delete profile") {
		t.Fatalf("SSH edit form has no visible delete action:\n%s", plain)
	}
	model.sshFormSelected = model.sshFormDeleteRow()
	if command := model.activateSSHFormRow(); command != nil {
		t.Fatal("SSH edit delete action skipped confirmation")
	}
	if !model.sshDeleteConfirmOpen || !model.sshFormOpen {
		t.Fatal("SSH edit delete confirmation did not preserve the form")
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if model.sshDeleteConfirmOpen || !model.sshFormOpen {
		t.Fatal("cancelling SSH deletion did not return to the edit form")
	}

	model.sshFormSelected = model.sshFormDeleteRow()
	_ = model.activateSSHFormRow()
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil || model.sshFormOpen {
		t.Fatal("confirmed SSH deletion did not close the edit form")
	}
	message, ok := command().(tuiSSHCommandResultMsg)
	if !ok || message.err != nil {
		t.Fatalf("SSH deletion result = %#v", message)
	}
	if _, err := loadCLISSHProfile("school"); err == nil {
		t.Fatal("SSH profile still exists after confirmed form deletion")
	}

	addModel := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	addModel.sshFormOpen = true
	addModel.sshFormExisting = false
	if addModel.sshFormDeleteRow() != -1 ||
		strings.Contains(stripTUIANSI(addModel.View()), "Delete profile") {
		t.Fatal("SSH add form exposed a delete action")
	}
}

func TestTUISSHSaveFailureKeepsFormOpen(t *testing.T) {
	configRoot := t.TempDir()
	runtimeRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = runtimeRoot
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previousRuntime })
	if err := addCLISSHProfile(cliSSHProfile{
		Name:        "school",
		Destination: "first@example.edu",
		Port:        22,
	}); err != nil {
		t.Fatal(err)
	}

	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.sshFormOpen = true
	model.sshForm = cliSSHProfile{
		Name:        "school",
		Destination: "duplicate@example.edu",
		Port:        22,
	}
	model.sshFormSelected = model.sshFormSaveRow()
	command := model.saveSSHForm()
	message := command().(tuiSSHCommandResultMsg)
	if message.err == nil {
		t.Fatal("duplicate SSH profile was accepted")
	}
	_, _ = model.Update(message)
	if !model.sshFormOpen || model.sshForm.Destination != "duplicate@example.edu" {
		t.Fatalf("failed save discarded the SSH form: %+v", model.sshForm)
	}
	if !strings.Contains(model.snapshot.Status, "already exists") {
		t.Fatalf("failed save status = %q", model.snapshot.Status)
	}
}

func TestTUISSHFormPreservesQuitSemantics(t *testing.T) {
	originalExit := completeCLIExitForTUI
	completeCLIExitForTUI = func(int) error { return nil }
	t.Cleanup(func() { completeCLIExitForTUI = originalExit })
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.sshFormOpen = true
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if command == nil || !model.frontendExitRequested || model.sshFormOpen {
		t.Fatalf(
			"q did not exit from SSH form: command=%v exit=%t form=%t",
			command,
			model.frontendExitRequested,
			model.sshFormOpen,
		)
	}
	busyModel := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	busyModel.sshFormOpen = true
	busyModel.busy = true
	_, command = busyModel.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if command == nil || !busyModel.frontendExitRequested || busyModel.sshFormOpen {
		t.Fatalf(
			"q did not exit during SSH save: command=%v exit=%t form=%t",
			command,
			busyModel.frontendExitRequested,
			busyModel.sshFormOpen,
		)
	}
	interruptModel := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	interruptModel.sshFormOpen = true
	interruptModel.sshFormReadOnly = true
	_, command = interruptModel.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if command == nil || !interruptModel.shutdownRequested ||
		interruptModel.frontendExitRequested || interruptModel.sshFormOpen {
		t.Fatalf(
			"Ctrl+C did not fully shut down from SSH details: command=%v shutdown=%t frontend=%t form=%t",
			command,
			interruptModel.shutdownRequested,
			interruptModel.frontendExitRequested,
			interruptModel.sshFormOpen,
		)
	}
}

func TestConnectedSSHProfileIsReadOnlyInTUIAndCLI(t *testing.T) {
	configRoot := t.TempDir()
	runtimeRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = runtimeRoot
	previousActive := activeCLIPersistentSSHTunnelForOperation
	connected := true
	activeCLIPersistentSSHTunnelForOperation = func() (cliSSHTunnelState, bool, error) {
		if !connected {
			return cliSSHTunnelState{}, false, nil
		}
		return cliSSHTunnelState{Name: "school", Port: 1080}, true, nil
	}
	t.Cleanup(func() {
		cliRuntimeDirectoryOverride = previousRuntime
		activeCLIPersistentSSHTunnelForOperation = previousActive
	})
	if err := addCLISSHProfile(cliSSHProfile{
		Name:        "school",
		Destination: "student@example.edu",
		Port:        22,
		LocalPort:   1080,
		Password:    "never-render-this",
		Options:     []string{"Compression=yes"},
	}); err != nil {
		t.Fatal(err)
	}

	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Page = tuiPageSSH
	model.snapshot.FocusSidebar = false
	model.snapshot.SSHProfiles = []tuiSSHProfile{{
		Name:      "school",
		Connected: true,
	}}
	model.beginSSHForm(true)
	if !model.sshFormOpen || !model.sshFormReadOnly {
		t.Fatalf("connected SSH form state = open:%t read-only:%t", model.sshFormOpen, model.sshFormReadOnly)
	}
	plain := stripTUIANSI(model.View())
	for _, expected := range []string{
		"CONNECTED · READ ONLY",
		"Save unavailable",
		"disconnect before editing",
		"Compression=yes",
	} {
		if !strings.Contains(plain, expected) {
			t.Fatalf("read-only SSH details do not contain %q:\n%s", expected, plain)
		}
	}
	if strings.Contains(plain, "never-render-this") {
		t.Fatal("connected SSH details leaked the password")
	}
	model.sshFormSelected = tuiSSHFormDestinationRow
	if command := model.activateSSHFormRow(); command != nil || model.sshFormFieldEditing {
		t.Fatal("connected SSH details allowed field editing")
	}
	model.sshForm.Destination = "staged@example.edu"
	if command := model.saveSSHForm(); command != nil {
		t.Fatal("connected SSH details scheduled a save")
	}
	saved, err := loadCLISSHProfile("school")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Destination != "student@example.edu" {
		t.Fatalf("read-only SSH details changed disk config: %+v", saved)
	}
	if err := cliSSHEditCommand([]string{"school", "new@example.edu"}); err == nil ||
		!strings.Contains(err.Error(), "disconnect it before editing") {
		t.Fatalf("connected CLI edit error = %v", err)
	}

	connected = false
	model = newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.SSHProfiles = []tuiSSHProfile{{Name: "school"}}
	model.beginSSHForm(true)
	if !model.sshFormOpen || model.sshFormReadOnly {
		t.Fatal("disconnected SSH profile did not become editable after reopening")
	}
}

func TestReplaceCLISSHProfileRejectsStaleFrontend(t *testing.T) {
	configRoot := t.TempDir()
	runtimeRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = runtimeRoot
	previousActive := activeCLIPersistentSSHTunnelForOperation
	activeCLIPersistentSSHTunnelForOperation = func() (cliSSHTunnelState, bool, error) {
		return cliSSHTunnelState{}, false, nil
	}
	t.Cleanup(func() {
		cliRuntimeDirectoryOverride = previousRuntime
		activeCLIPersistentSSHTunnelForOperation = previousActive
	})
	original := cliSSHProfile{
		Name:        "school",
		Destination: "first@example.edu",
		Port:        22,
	}
	if err := addCLISSHProfile(original); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := cliSSHProfileFingerprint(original)
	if err != nil {
		t.Fatal(err)
	}
	if err := updateCLISSHConfig(func(config *cliSSHConfig) error {
		config.Profiles[0].Destination = "external@example.edu"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stale := original
	stale.Destination = "stale@example.edu"
	err = replaceCLISSHProfile("school", fingerprint, stale)
	if err == nil || !strings.Contains(err.Error(), "changed in another frontend") {
		t.Fatalf("stale SSH edit error = %v", err)
	}
	saved, err := loadCLISSHProfile("school")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Destination != "external@example.edu" {
		t.Fatalf("stale SSH edit overwrote external change: %+v", saved)
	}
}

func TestConnectCLISSHProfileRestoresPreviousTunnel(t *testing.T) {
	for _, restoreFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "restored", true: "restore_failed"}[restoreFails], func(t *testing.T) {
			configRoot := t.TempDir()
			runtimeRoot := t.TempDir()
			t.Setenv("XDG_CONFIG_HOME", configRoot)
			previousRuntime := cliRuntimeDirectoryOverride
			cliRuntimeDirectoryOverride = runtimeRoot
			previousActive := activeCLIPersistentSSHTunnelForOperation
			previousStart := startCLIPersistentSSHTunnelForOperation
			previousStop := stopCLIStateTunnelForOperation
			t.Cleanup(func() {
				cliRuntimeDirectoryOverride = previousRuntime
				activeCLIPersistentSSHTunnelForOperation = previousActive
				startCLIPersistentSSHTunnelForOperation = previousStart
				stopCLIStateTunnelForOperation = previousStop
			})
			for _, profile := range []cliSSHProfile{
				{Name: "old", Destination: "old@example.edu", Port: 22, LocalPort: 1080},
				{Name: "new", Destination: "new@example.edu", Port: 22, LocalPort: 1080},
			} {
				if err := addCLISSHProfile(profile); err != nil {
					t.Fatal(err)
				}
			}
			activeCLIPersistentSSHTunnelForOperation = func() (cliSSHTunnelState, bool, error) {
				return cliSSHTunnelState{Name: "old", Port: 1080}, true, nil
			}
			var operations []string
			stopCLIStateTunnelForOperation = func(state cliSSHTunnelState) error {
				operations = append(operations, "stop:"+state.Name)
				return nil
			}
			startCLIPersistentSSHTunnelForOperation = func(profile cliSSHProfile) (cliSSHTunnelState, error) {
				operations = append(operations, "start:"+profile.Name)
				if profile.Name == "new" || restoreFails {
					return cliSSHTunnelState{}, errors.New("simulated start failure")
				}
				return cliSSHTunnelState{Name: profile.Name, Port: profile.LocalPort}, nil
			}
			_, _, err := connectCLISSHProfile("new")
			if err == nil {
				t.Fatal("failed SSH switch unexpectedly succeeded")
			}
			expected := []string{"stop:old", "start:new", "start:old"}
			if strings.Join(operations, ",") != strings.Join(expected, ",") {
				t.Fatalf("SSH switch operations = %v, want %v", operations, expected)
			}
			if restoreFails {
				if !strings.Contains(err.Error(), "restore previous tunnel") {
					t.Fatalf("double SSH failure error = %v", err)
				}
			} else if !strings.Contains(err.Error(), "previous tunnel \"old\" restored") {
				t.Fatalf("restored SSH switch error = %v", err)
			}
		})
	}
}

func TestConnectCLISSHProfileSwitchesSharedFixedPort(t *testing.T) {
	configRoot := t.TempDir()
	runtimeRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = runtimeRoot
	previousActive := activeCLIPersistentSSHTunnelForOperation
	previousStart := startCLIPersistentSSHTunnelForOperation
	previousStop := stopCLIStateTunnelForOperation
	t.Cleanup(func() {
		cliRuntimeDirectoryOverride = previousRuntime
		activeCLIPersistentSSHTunnelForOperation = previousActive
		startCLIPersistentSSHTunnelForOperation = previousStart
		stopCLIStateTunnelForOperation = previousStop
	})
	for _, profile := range []cliSSHProfile{
		{Name: "old", Destination: "old@example.edu", Port: 22, LocalPort: 1080},
		{Name: "new", Destination: "new@example.edu", Port: 22, LocalPort: 1080},
	} {
		if err := addCLISSHProfile(profile); err != nil {
			t.Fatal(err)
		}
	}
	activeCLIPersistentSSHTunnelForOperation = func() (cliSSHTunnelState, bool, error) {
		return cliSSHTunnelState{Name: "old", Port: 1080}, true, nil
	}
	var operations []string
	stopCLIStateTunnelForOperation = func(state cliSSHTunnelState) error {
		operations = append(operations, "stop:"+state.Name)
		return nil
	}
	startCLIPersistentSSHTunnelForOperation = func(profile cliSSHProfile) (cliSSHTunnelState, error) {
		operations = append(operations, "start:"+profile.Name)
		return cliSSHTunnelState{Name: profile.Name, Port: profile.LocalPort}, nil
	}
	state, alreadyConnected, err := connectCLISSHProfile("new")
	if err != nil || alreadyConnected || state.Name != "new" || state.Port != 1080 {
		t.Fatalf("shared-port SSH switch = state:%+v already:%t err:%v", state, alreadyConnected, err)
	}
	expected := []string{"stop:old", "start:new"}
	if strings.Join(operations, ",") != strings.Join(expected, ",") {
		t.Fatalf("shared-port SSH switch operations = %v, want %v", operations, expected)
	}
}

func TestDeleteConnectedCLISSHProfileRestoresTunnelOnConfigFailure(t *testing.T) {
	configRoot := t.TempDir()
	runtimeRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = runtimeRoot
	previousActive := activeCLIPersistentSSHTunnelForOperation
	previousStart := startCLIPersistentSSHTunnelForOperation
	previousStop := stopCLIStateTunnelForOperation
	previousUpdate := updateCLISSHConfigForOperation
	t.Cleanup(func() {
		cliRuntimeDirectoryOverride = previousRuntime
		activeCLIPersistentSSHTunnelForOperation = previousActive
		startCLIPersistentSSHTunnelForOperation = previousStart
		stopCLIStateTunnelForOperation = previousStop
		updateCLISSHConfigForOperation = previousUpdate
	})
	profile := cliSSHProfile{Name: "school", Destination: "student@example.edu", Port: 22}
	if err := addCLISSHProfile(profile); err != nil {
		t.Fatal(err)
	}
	activeCLIPersistentSSHTunnelForOperation = func() (cliSSHTunnelState, bool, error) {
		return cliSSHTunnelState{Name: "school", Port: 1080}, true, nil
	}
	stopped, restored := false, false
	stopCLIStateTunnelForOperation = func(cliSSHTunnelState) error {
		stopped = true
		return nil
	}
	startCLIPersistentSSHTunnelForOperation = func(restoredProfile cliSSHProfile) (cliSSHTunnelState, error) {
		restored = restoredProfile.Name == profile.Name
		return cliSSHTunnelState{Name: restoredProfile.Name}, nil
	}
	updateCLISSHConfigForOperation = func(func(*cliSSHConfig) error) error {
		return errors.New("simulated config write failure")
	}
	err := deleteCLISSHProfile("school")
	if err == nil || !strings.Contains(err.Error(), "previous tunnel restored") ||
		!stopped || !restored {
		t.Fatalf("connected SSH delete rollback = stopped:%t restored:%t err:%v", stopped, restored, err)
	}
	if _, err := loadCLISSHProfile("school"); err != nil {
		t.Fatalf("failed delete removed SSH profile: %v", err)
	}
}

func TestStopCLIStateTunnelKeepsStateWhenOpenSSHExitFails(t *testing.T) {
	binDirectory := t.TempDir()
	sshPath := filepath.Join(binDirectory, "ssh")
	if err := os.WriteFile(sshPath, []byte("#!/bin/sh\nif [ \"$4\" = \"check\" ]; then test -f \"$2\"; exit $?; fi\nif [ \"$4\" = \"exit\" ]; then exit 1; fi\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	controlPath := filepath.Join(t.TempDir(), "control.sock")
	statePath := filepath.Join(t.TempDir(), "persistent.json")
	for _, path := range []string{controlPath, statePath} {
		if err := os.WriteFile(path, []byte("state"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	state := cliSSHTunnelState{
		Name:        "school",
		Destination: "student@example.edu",
		Port:        listener.Addr().(*net.TCPAddr).Port,
		ControlPath: controlPath,
		StatePath:   statePath,
	}
	if err := stopCLIStateTunnel(state); err == nil {
		t.Fatal("failed OpenSSH exit was reported as success")
	}
	for _, path := range []string{controlPath, statePath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("failed OpenSSH exit removed %s: %v", path, err)
		}
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(controlPath); err != nil {
		t.Fatal(err)
	}
	if err := stopCLIStateTunnel(state); err != nil {
		t.Fatalf("stale SSH state cleanup failed: %v", err)
	}
	for _, path := range []string{controlPath, statePath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("stale SSH state remains at %s: %v", path, err)
		}
	}
}

func TestActiveCLISSHTunnelKeepsBrokenForwardManageable(t *testing.T) {
	binDirectory := t.TempDir()
	sshPath := filepath.Join(binDirectory, "ssh")
	script := "#!/bin/sh\n" +
		"if [ \"$4\" = \"check\" ]; then test -f \"$2.master\"; exit $?; fi\n" +
		"if [ \"$4\" = \"exit\" ]; then rm -f \"$2.master\"; exit 0; fi\n" +
		"exit 1\n"
	if err := os.WriteFile(sshPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDirectory+string(os.PathListSeparator)+os.Getenv("PATH"))
	runtimeRoot := t.TempDir()
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = runtimeRoot
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previousRuntime })
	directory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	controlPath := filepath.Join(directory, "control.sock")
	masterMarker := controlPath + ".master"
	if err := os.WriteFile(masterMarker, []byte("alive"), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(directory, cliSSHPersistentStateFile)
	state := cliSSHTunnelState{
		Name:        "school",
		Destination: "student@example.edu",
		Port:        port,
		ControlPath: controlPath,
		Kind:        "persistent",
		StartedAt:   time.Now(),
		StatePath:   statePath,
	}
	if err := saveCLISSHTunnelState(state); err != nil {
		t.Fatal(err)
	}
	loaded, active, err := activeCLIPersistentSSHTunnel()
	if err != nil || !active || loaded.Name != "school" {
		t.Fatalf("broken SOCKS listener state = state:%+v active:%t err:%v", loaded, active, err)
	}
	for _, path := range []string{masterMarker, statePath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("broken SSH tunnel lost manageable state at %s: %v", path, err)
		}
	}
	if err := stopCLIStateTunnel(loaded); err != nil {
		t.Fatalf("broken SSH forward could not be disconnected: %v", err)
	}
	for _, path := range []string{masterMarker, statePath} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("disconnected broken SSH tunnel artifact remains at %s: %v", path, err)
		}
	}
}

func TestStopAllCLISSHTunnelsPreservesInvalidRuntimeState(t *testing.T) {
	runtimeRoot := t.TempDir()
	previousRuntime := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = runtimeRoot
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previousRuntime })
	directory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(directory, "broken.json")
	if err := os.WriteFile(statePath, []byte("not-json"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = stopAllCLISSHTunnels()
	if err == nil || !strings.Contains(err.Error(), "inspect SSH runtime state") {
		t.Fatalf("invalid SSH runtime state error = %v", err)
	}
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("invalid SSH runtime state was hidden by deletion: %v", err)
	}
}

func TestTUISSHFormRenderingFitsTerminal(t *testing.T) {
	for width := 40; width <= 140; width++ {
		for height := 10; height <= 40; height++ {
			for _, readOnly := range []bool{false, true} {
				snapshot := tuiSnapshot{
					Page: tuiPageSSH,
					SSHForm: tuiSSHFormView{
						Open:        true,
						Existing:    true,
						ReadOnly:    readOnly,
						Name:        "school",
						Destination: "student@example.edu",
						Port:        22,
						LocalPort:   1080,
						PasswordSet: true,
						Options:     []string{"ServerAliveInterval=30", "Compression=yes"},
						Selected:    tuiSSHFormOptionStartRow + 1,
					},
				}
				output := renderTUIAtSize(
					snapshot,
					cliPaths{},
					"private Unix socket",
					true,
					false,
					width,
					height,
				)
				lines := strings.Split(output, "\n")
				if len(lines) != height {
					t.Fatalf("SSH form read-only=%t at %dx%d has %d lines", readOnly, width, height, len(lines))
				}
				for lineNumber, line := range lines {
					if got := tuiDisplayWidth(stripTUIANSI(line)); got != width {
						t.Fatalf("SSH form read-only=%t at %dx%d line %d width = %d", readOnly, width, height, lineNumber, got)
					}
				}
			}
		}
	}
}
