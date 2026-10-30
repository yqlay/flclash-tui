//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestFullAuditBackendInspectionKeepsOneLockInode(t *testing.T) {
	for _, probe := range []string{"status", "exit cleanup"} {
		t.Run(probe, func(t *testing.T) {
			useTestCLIRuntimeDirectory(t)
			lock, err := acquireCLIBackendLock(cliProcessOwner{Kind: "service"})
			if err != nil {
				t.Fatal(err)
			}
			lock.release()
			// Another starter has opened the named lock, but has not flocked it
			// yet. Status/exit may run at exactly this point.
			contender, err := os.OpenFile(lock.path, os.O_RDWR, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			defer contender.Close()
			if probe == "status" {
				_, active, err := activeCLIBackendOwner()
				if err != nil || active {
					t.Fatalf("inactive status: %v %v", active, err)
				}
			} else if err := cleanupCLIExitArtifacts(0); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Flock(int(contender.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
				t.Fatal(err)
			}
			defer syscall.Flock(int(contender.Fd()), syscall.LOCK_UN)
			duplicate, err := acquireCLIBackendLock(cliProcessOwner{Kind: "service-starting"})
			if duplicate != nil {
				duplicate.release()
			}
			var busy *cliLockBusyError
			if !errors.As(err, &busy) {
				t.Fatalf("%s unlinked the lock and allowed a second Backend: %v", probe, err)
			}
		})
	}
}

func TestFullAuditProfileOptionsFollowNames(t *testing.T) {
	home := t.TempDir()
	config := filepath.Join(home, "active.yaml")
	target := filepath.Join(home, "next.yaml")
	if err := os.WriteFile(target, []byte("mode: rule\nrules: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var actions []string
	serveAlignService(t, func(request tuiServiceRequest) tuiServiceStatus {
		actions = append(actions, request.Action)
		return alignServiceStatus(1, config)
	})
	for _, args := range [][]string{
		{"use", "next.yaml", "--directory", home},
		{"use", "--directory", home, "next.yaml"},
	} {
		captureCLIOutput(t, func() error { return profileCommand(args) })
	}
	reloads := 0
	for _, action := range actions {
		if action == "reload" {
			reloads++
		}
	}
	if reloads != 2 {
		t.Fatalf("profile use did not reach the revisioned Backend: %v", actions)
	}
	current := captureCLIOutput(t, func() error { return profileCommand([]string{"current", "--directory", home}) })
	if strings.TrimSpace(current) != config {
		t.Fatalf("explicit active directory forgot its current profile: %q", current)
	}
}

func TestFullAuditProfileRejectsUnexpectedArguments(t *testing.T) {
	for _, command := range []string{"list", "current"} {
		t.Run(command, func(t *testing.T) {
			if err := profileCommand([]string{command, "extra"}); err == nil {
				t.Fatal("unexpected positional argument was silently ignored")
			}
		})
	}
}

func TestFullAuditHTTPProxyIPv6Targets(t *testing.T) {
	for _, tc := range []struct{ target, want string }{
		{"http://[2001:db8::1]/path", "[2001:db8::1]:80"},
		{"https://[2001:db8::1]/path", "[2001:db8::1]:443"},
		{"http://[::1]:8080/path", "[::1]:8080"},
		{"http://example.test/path", "example.test:80"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			u, err := url.Parse(tc.target)
			if err != nil {
				t.Fatal(err)
			}
			got, err := cliHTTPProxyTarget(&http.Request{Method: http.MethodGet, URL: u, Host: u.Host})
			if err != nil || got != tc.want {
				t.Fatalf("target %q, want %q: %v", got, tc.want, err)
			}
		})
	}
}

func TestFullAuditIPv6HTTPThroughSSHRelay(t *testing.T) {
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	done := make(chan error, 1)
	go func() {
		done <- func() error {
			connection, err := upstream.Accept()
			if err != nil {
				return err
			}
			defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
			target, err := readCLIInboundSOCKS5(connection)
			if err != nil {
				return err
			}
			if target != "[2001:db8::1]:80" {
				return fmt.Errorf("SOCKS target %q", target)
			}
			if err := writeCLISOCKS5Reply(connection, 0); err != nil {
				return err
			}
			_, err = io.WriteString(connection, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\nConnection: close\r\n\r\nok")
			return err
		}()
	}()
	relay, port, stopped := startCLITestRelay(t, upstream.Addr().(*net.TCPAddr).Port)
	defer func() { relay.stop(); <-stopped }()
	proxyURL, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	response, err := client.Get("http://[2001:db8::1]/")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != 200 || string(body) != "ok" {
		t.Fatalf("IPv6 HTTP response %s %q: %v", response.Status, body, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func FuzzFullAuditSubscriptionNeverPanics(f *testing.F) {
	for _, seed := range []string{"mode: rule\nrules: []\n", "[]", "null", "{\"outbounds\":[]}", "trojan://password@example.test:443#one", "c3M6Ly9iYWQ=", "[Proxy]\none = ss, example.test, 443, encrypt-method=aes-128-gcm, password=p"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 8192 {
			t.Skip()
		}
		payload, err := normalizeTUISubscription(data)
		if err == nil && (len(payload.Data) == 0 || validateConfigBytes(payload.Data) != "") {
			t.Fatal("subscription conversion returned an invalid profile without reporting an error")
		}
	})
}

func TestFullAuditLogRotatesBeforeCrossingLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backend.log")
	prefix := []byte(strings.Repeat("x", tuiServiceLogMaxBytes-10))
	if err := os.WriteFile(path, prefix, 0o600); err != nil {
		t.Fatal(err)
	}
	writer, err := newTUIRotatingLogWriter(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Write([]byte("12345678901234567890")); err != nil {
		t.Fatal(err)
	}
	if got := string(mustReadFile(t, path)); got != "12345678901234567890" {
		t.Fatalf("active log crossed its limit instead of rotating: %d bytes", len(got))
	}
	if got := len(mustReadFile(t, path+".1")); got != len(prefix) {
		t.Fatalf("rotated log has %d bytes", got)
	}
}

func TestFullAuditProfileReadOnlyJSON(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), []byte("mode: rule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := captureCLIOutput(t, func() error { return profileCommand([]string{"current", "--directory", home, "--json"}) })
	if !json.Valid([]byte(data)) {
		t.Fatalf("profile current --json returned plain text: %q", data)
	}
}

func TestFullAuditProfileImportCannotIgnoreExplicitDirectory(t *testing.T) {
	active := filepath.Join(t.TempDir(), "active.yaml")
	requestedHome := t.TempDir()
	source := filepath.Join(t.TempDir(), "local.yaml")
	if err := os.WriteFile(source, []byte("mode: rule\nrules: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var mutated bool
	serveAlignService(t, func(request tuiServiceRequest) tuiServiceStatus {
		if request.Action == "put_profile" {
			mutated = true
		}
		return alignServiceStatus(1, active)
	})
	err := profileCommand([]string{"import-file", "--directory", requestedHome, source})
	if err == nil || mutated {
		t.Fatalf("explicit directory was ignored and the active Backend was mutated: %v", err)
	}
}

func TestFullAuditProfileMutationJSON(t *testing.T) {
	for _, operation := range []string{"use", "rename", "delete", "link", "import", "import-file", "update"} {
		t.Run(operation, func(t *testing.T) {
			home := t.TempDir()
			active := filepath.Join(home, "active.yaml")
			target := filepath.Join(home, "next.yaml")
			data := []byte("mixed-port: 7890\nmode: rule\nrules: []\n")
			for _, path := range []string{active, target} {
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
			defer server.Close()
			if err := rememberTUISubscriptionSource(home, target, server.URL); err != nil {
				t.Fatal(err)
			}
			var requestAction string
			serveAlignService(t, func(request tuiServiceRequest) tuiServiceStatus {
				status := alignServiceStatus(2, active)
				status.ResultPath = request.ConfigPath
				if request.Action != "status" {
					requestAction = request.Action
					if request.ExpectedRevision == nil || *request.ExpectedRevision != 2 || request.ExpectedInstanceID != "align" {
						t.Error("profile mutation lost revision or Backend instance ownership")
					}
				}
				return status
			})
			args := []string{operation}
			switch operation {
			case "use", "update", "delete":
				args = append(args, "next.yaml")
			case "rename":
				args = append(args, "next.yaml", "renamed.yaml")
			case "import", "link":
				args = append(args, server.URL)
			case "import-file":
				args = append(args, target)
			}
			args = append(args, "--directory", home, "--json")
			output := captureCLIOutput(t, func() error { return profileCommand(args) })
			var result struct {
				Operation, Path string
				Revision        uint64
			}
			if err := json.Unmarshal([]byte(output), &result); err != nil || result.Operation != operation || result.Revision != 2 || result.Path == "" || requestAction == "" {
				t.Fatalf("profile %s lost its structured result: %q (%v)", operation, output, err)
			}
		})
	}
}

func TestFullAuditRefreshCannotOpenFilteredOutDetails(t *testing.T) {
	for _, page := range []tuiPage{tuiPageRequests, tuiPageConnections, tuiPageLogs} {
		t.Run(tuiPageName(page), func(t *testing.T) {
			model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
			t.Cleanup(model.sshOperationCancel)
			model.snapshot = tuiSnapshot{Page: page, FocusSidebar: false,
				TrafficSource: tuiTrafficSourceProxy, HistoryFilter: "active", SelectedRequest: 0, SelectedConnection: 0, SelectedLog: 0,
				Requests:    []tuiRequest{{TuiConnection: tuiConnection{ID: "selected"}, Active: true}, {TuiConnection: tuiConnection{ID: "other"}, Active: true}},
				Connections: []tuiConnection{{ID: "selected"}, {ID: "other"}},
				Logs:        []string{"INFO selected", "INFO other"},
			}
			updated := cloneTUISnapshot(model.snapshot)
			switch page {
			case tuiPageRequests:
				updated.Requests[0].Active = false // The selected request just completed.
			case tuiPageConnections:
				updated.Connections = updated.Connections[1:] // The selected flow just closed.
			case tuiPageLogs:
				updated.Logs = updated.Logs[1:] // Rotation removed the selected record.
			}
			model.snapshot = mergeTUIRefresh(model.snapshot, updated)
			model.Update(tea.KeyMsg{Type: tea.KeyEnter})
			var chosen string
			switch page {
			case tuiPageRequests:
				if model.snapshot.HistoryDetailOpen && model.snapshot.SelectedRequest >= 0 {
					chosen = model.snapshot.Requests[model.snapshot.SelectedRequest].ID
				}
			case tuiPageConnections:
				if model.snapshot.ConnectionsDetailOpen && model.snapshot.SelectedConnection >= 0 {
					chosen = model.snapshot.Connections[model.snapshot.SelectedConnection].ID
				}
			case tuiPageLogs:
				if model.snapshot.LogDetailOpen && model.snapshot.SelectedLog >= 0 {
					chosen = strings.TrimPrefix(model.snapshot.Logs[model.snapshot.SelectedLog], "INFO ")
				}
			}
			if chosen != "other" {
				t.Fatalf("Enter opened %q after the selected row left the visible list", chosen)
			}
		})
	}
}

func TestFullAuditLateTrafficDoesNotReviveStoppedCore(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	t.Cleanup(model.sshOperationCancel)
	model.coreRunning = false
	model.snapshot.Traffic = trafficSnapshot{}
	model.snapshot.TotalTraffic = trafficSnapshot{Up: 100, Down: 200}
	// A queued update from the old stream must not make a stopped Core look busy.
	model.Update(tuiTrafficMsg{update: tuiTrafficUpdate{Traffic: trafficSnapshot{Up: 50, Down: 60, UpTotal: 90, DownTotal: 180}}})
	if model.snapshot.Traffic.Up != 0 || model.snapshot.TotalTraffic.Up != 100 {
		t.Fatalf("stopped Core accepted a retired traffic sample: %+v %+v", model.snapshot.Traffic, model.snapshot.TotalTraffic)
	}
}

func TestFullAuditRetiredStreamsCannotOverwriteCurrentDashboard(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	t.Cleanup(model.sshOperationCancel)
	traffic := make(chan tuiTrafficUpdate, 1)
	memory := make(chan tuiCoreMemoryUpdate, 1)
	model.trafficUpdates, model.coreMemoryUpdates = traffic, memory
	oldTraffic, oldMemory := model.waitTrafficUpdate(), model.waitCoreMemoryUpdate()
	traffic <- tuiTrafficUpdate{Traffic: trafficSnapshot{Up: 999, UpTotal: 999}}
	memory <- tuiCoreMemoryUpdate{RSS: 999, Error: "retired stream error"}
	// Switching away cancels the old monitors; returning starts new channels.
	model.trafficUpdates = make(chan tuiTrafficUpdate)
	model.coreMemoryUpdates = make(chan tuiCoreMemoryUpdate)
	model.snapshot.Traffic.Up, model.snapshot.TotalTraffic.Up = 42, 50
	model.snapshot.Memory.CoreRSS = 100
	_, nextTraffic := model.Update(oldTraffic())
	_, nextMemory := model.Update(oldMemory())
	if nextTraffic != nil || nextMemory != nil || model.snapshot.Traffic.Up != 42 || model.snapshot.TotalTraffic.Up != 50 || model.snapshot.Memory.CoreRSS != 100 || model.snapshot.Memory.CoreError != "" {
		t.Fatal("a retired monitor overwrote a new sample or scheduled a duplicate reader")
	}
}

func TestFullAuditHiddenConnectionCannotBeClosedByOldSelection(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	t.Cleanup(model.sshOperationCancel)
	model.snapshot = tuiSnapshot{Page: tuiPageConnections, ConnectionsQuery: "match", SelectedConnection: 0,
		Connections: []tuiConnection{{ID: "selected", Host: "match-old"}, {ID: "other", Host: "match-other"}},
	}
	updated := cloneTUISnapshot(model.snapshot)
	updated.Connections[0].Host = "no-longer-in-filter"
	model.snapshot = mergeTUIRefresh(model.snapshot, updated)
	_, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if cmd != nil || model.dangerConfirmOpen {
		t.Fatal("the invisible previously selected connection was offered for closing")
	}
}
