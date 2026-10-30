//go:build linux && !cgo && cli

package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestBugAuditOldSSHLocalRefreshCannotUndoProfileEdit(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := updateCLISSHConfig(func(config *cliSSHConfig) error {
		config.Profiles = []cliSSHProfile{{Name: "test", Username: "user", Host: "example.test", Port: 22, LocalPort: 23456}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	paths, err := resolvePaths("", "")
	if err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(controllerClient{}, paths, nil, true)
	t.Cleanup(model.sshOperationCancel)
	model.snapshot.Page = tuiPageSSH
	refreshTUISSH(&model.snapshot)
	command := model.startSSHLocalRefresh()
	if command == nil {
		t.Fatal("SSH refresh did not start")
	}
	stale := command()
	sequence, ok := model.beginSSHWrite()
	if !ok {
		t.Fatal("SSH edit did not start")
	}
	if err := updateCLISSHConfig(func(config *cliSSHConfig) error {
		config.Profiles[0].LocalPort = 34567
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	model.update(tuiSSHCommandResultMsg{action: "edit", sequence: sequence, selectedName: "test"})
	if model.snapshot.SSHProfiles[0].LocalPort != 34567 {
		t.Fatal("edit result did not refresh the saved port")
	}
	model.update(stale)
	if model.snapshot.SSHProfiles[0].LocalPort != 34567 || model.sshLocalRefreshActive {
		t.Fatalf("old local refresh undid SSH edit: %+v", model.snapshot.SSHProfiles)
	}
	current := model.startSSHLocalRefresh()
	if current == nil {
		t.Fatal("discarding stale data left the local refresh stuck")
	}
	model.update(current())
	if model.sshLocalRefreshActive || model.snapshot.SSHProfiles[0].LocalPort != 34567 {
		t.Fatal("a current SSH refresh was not accepted")
	}
}

func TestBugAuditOldFullRefreshPreservesFreshSSHState(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/proxies":
			_, _ = w.Write([]byte(`{"proxies":{}}`))
		case "/connections":
			_, _ = w.Write([]byte(`{"connections":[]}`))
		case "/configs":
			_, _ = w.Write([]byte(`{"mode":"rule","allow-lan":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	model := newTUIModel(controllerClient{options: controllerOptions{address: server.URL}}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	t.Cleanup(model.sshOperationCancel)
	model.snapshot.Page = tuiPageSSH
	full := model.startRefresh()
	if full == nil {
		t.Fatal("full refresh did not start")
	}
	stale := full()
	if model.startSSHLocalRefresh() == nil {
		t.Fatal("local refresh did not start")
	}
	model.update(tuiSSHLocalRefreshMsg{sequence: model.sshLocalRefreshSequence, profiles: []tuiSSHProfile{{Name: "fresh"}}})
	model.update(stale)
	if len(model.snapshot.SSHProfiles) != 1 || model.snapshot.SSHProfiles[0].Name != "fresh" {
		t.Fatalf("old full refresh undid newer SSH data: %+v", model.snapshot.SSHProfiles)
	}
	if !model.snapshot.Settings.AllowLAN {
		t.Fatal("preserving fresh SSH data discarded an independent Core update")
	}
}

func TestBugAuditMalformedProxyResponseClearsPhantomConnections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"proxies":`))
	}))
	defer server.Close()
	model := newTUIModel(controllerClient{options: controllerOptions{address: server.URL}}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	t.Cleanup(model.sshOperationCancel)
	model.coreRunning = true
	model.snapshot.Connections = []tuiConnection{{ID: "proxy-old", Host: "example.test"}}
	model.snapshot.Requests = []tuiRequest{{TuiConnection: model.snapshot.Connections[0], Active: true}}
	refresh := model.startRefresh()
	if refresh == nil {
		t.Fatal("full refresh did not start")
	}
	model.update(refresh())
	if len(model.snapshot.Connections) != 0 || len(model.snapshot.Requests) != 1 || model.snapshot.Requests[0].Active {
		t.Fatalf("malformed controller response left phantom proxy flows: connections=%+v history=%+v", model.snapshot.Connections, model.snapshot.Requests)
	}
}

func TestBugAuditCoreOperationPreservesNewSSHState(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, tuiServiceLogFilename), []byte("test log\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: home}, nil, true)
	t.Cleanup(model.sshOperationCancel)
	model.snapshot.Page = tuiPageSSH
	model.snapshot.SSHProfiles = []tuiSSHProfile{{Name: "test", Host: "example.test", Port: 22}}
	model.snapshot.SelectedSSH = 0
	model.snapshot.SSHDetailName = "test"
	if model.startSSHLocalRefresh() == nil {
		t.Fatal("SSH local refresh did not start")
	}
	sequence := model.sshLocalRefreshSequence
	model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'7'}})
	_, export := model.update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'e'}})
	if export == nil || !model.coreBusy {
		t.Fatal("log export did not acquire the core operation lane")
	}
	model.update(tuiSSHLocalRefreshMsg{sequence: sequence, profiles: []tuiSSHProfile{{
		Name: "test", Host: "example.test", Port: 22, Connected: true, Ready: true, SocksPort: 23456,
	}}})
	if !model.snapshot.SSHProfiles[0].Connected {
		t.Fatal("SSH refresh did not update the live snapshot")
	}
	model.snapshot.SSHCaptureKnown = true
	model.snapshot.SSHCaptureFound = 2
	model.snapshot.SSHCaptureHint = "new capture result"
	model.update(export())
	if len(model.snapshot.SSHProfiles) != 1 || !model.snapshot.SSHProfiles[0].Connected ||
		!model.snapshot.SSHProfiles[0].Ready || model.snapshot.SSHProfiles[0].SocksPort != 23456 {
		t.Fatalf("late core operation rolled back SSH state: %+v", model.snapshot.SSHProfiles)
	}
	if !model.snapshot.SSHCaptureKnown || model.snapshot.SSHCaptureFound != 2 || model.snapshot.SSHCaptureHint != "new capture result" {
		t.Fatal("late core operation rolled back explicit Capture results")
	}
}

func TestBugAuditInvalidCommandsDoNotQueryBackend(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func() error
		want string
	}{
		{"net", func() error { return networkCommand([]string{"bogus"}) }, "unknown net command"},
		{"flc", func() error { return flcManagementCommand([]string{"bogus"}) }, "unknown flc command"},
		{"select", func() error { return flcManagementCommand([]string{"select"}) }, "usage: flclash flc select NAME"},
		{"status", func() error { return flcManagementCommand([]string{"status", "extra"}) }, "usage: flclash flc status"},
		{"test", func() error { return flcManagementCommand([]string{"test", "extra"}) }, "usage: flclash flc test"},
		{"env", func() error { return flcManagementCommand([]string{"env", "extra"}) }, "usage: flclash env"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan string, 4)
			home := t.TempDir()
			serveRowAlignService(t, func(request tuiServiceRequest) tuiServiceStatus {
				requests <- request.Action
				status := alignServiceStatus(1, filepath.Join(home, "config.yaml"))
				status.OK = false
				status.Error = "unexpected Backend lookup"
				return status
			})
			err := tc.run()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("invalid command error = %v, want %q before Backend access", err, tc.want)
			}
			select {
			case action := <-requests:
				t.Fatalf("invalid command contacted Backend: %s", action)
			default:
			}
		})
	}
}
