//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestAudit2MergeKeepsCurrentControls(t *testing.T) {
	for _, merge := range []struct {
		name string
		fn   func(tuiSnapshot, tuiSnapshot) tuiSnapshot
	}{{"refresh", mergeTUIRefresh}, {"operation", mergeTUIOperation}} {
		for _, open := range []bool{false, true} {
			t.Run(merge.name+"/"+map[bool]string{false: "closed", true: "open"}[open], func(t *testing.T) {
				current := tuiSnapshot{
					Language: "zh-Hant", LogsQuery: "selected", LogsLevel: "ERROR", DashboardScroll: 7,
					Logs: []string{"2026-10-05 ERROR selected"}, SelectedLog: 0, LogDetailOpen: open,
					Connections: []tuiConnection{{ID: "selected"}}, SelectedConnection: 0, ConnectionsDetailOpen: open,
					Requests: []tuiRequest{{TuiConnection: tuiConnection{ID: "selected"}}}, SelectedRequest: 0, HistoryDetailOpen: open,
				}
				stale := cloneTUISnapshot(current)
				stale.Language, stale.LogsQuery, stale.LogsLevel, stale.DashboardScroll = "en", "", "ALL", 0
				stale.LogDetailOpen, stale.ConnectionsDetailOpen, stale.HistoryDetailOpen = !open, !open, !open
				merged := merge.fn(current, stale)
				if merged.Language != current.Language || merged.LogsQuery != current.LogsQuery || merged.LogsLevel != current.LogsLevel || merged.DashboardScroll != current.DashboardScroll ||
					merged.LogDetailOpen != open || merged.ConnectionsDetailOpen != open || merged.HistoryDetailOpen != open {
					t.Fatalf("background result restored stale controls: query=%q level=%q scroll=%d details=%t/%t/%t", merged.LogsQuery, merged.LogsLevel, merged.DashboardScroll, merged.LogDetailOpen, merged.ConnectionsDetailOpen, merged.HistoryDetailOpen)
				}
			})
		}
	}
}

func TestAudit2FilteredDetailsCannotRemainOpen(t *testing.T) {
	current := tuiSnapshot{
		Connections: []tuiConnection{{ID: "selected"}}, SelectedConnection: 0, ConnectionsDetailOpen: true, ConnectionsQuery: "missing",
		Requests: []tuiRequest{{TuiConnection: tuiConnection{ID: "selected"}}}, SelectedRequest: 0, HistoryDetailOpen: true, HistoryQuery: "missing",
	}
	merged := mergeTUIRefresh(current, cloneTUISnapshot(current))
	if merged.ConnectionsDetailOpen || merged.HistoryDetailOpen {
		t.Fatal("detail remained open for a row outside the current filter")
	}
}

func TestAudit2StoppedSettingsFollowNewBackend(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(path, []byte("mixed-port: 7891\nmode: rule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: directory, ConfigPath: path}, nil, true)
	model.service = newTUIServiceClientAt(directory)
	model.update(tuiRefreshResultMsg{
		snapshot:      tuiSnapshot{Settings: tuiSettings{MixedPort: 7892, AllowLAN: true}},
		serviceStatus: &tuiServiceStatus{InstanceID: "backend", Revision: 2, ConfigPath: path, ConfiguredProxyPort: 7892, Mode: "rule", TunState: "off"},
	})
	if model.snapshot.Settings.MixedPort != 7892 || !model.snapshot.Settings.AllowLAN || (model.pendingMixedPort != nil && *model.pendingMixedPort != 7892) {
		t.Fatalf("initial clean cache overwrote newer settings: %+v, pending=%v", model.snapshot.Settings, model.pendingMixedPort)
	}
}

func TestAudit2ConflictingDraftKeepsInputAndRejectsSave(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(path, []byte("mixed-port: 7891\nmode: rule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: directory, ConfigPath: path}, nil, true)
	model.service = newTUIServiceClientAt(directory)
	model.backendInstanceID, model.backendRevision = "backend", 4
	model.stageTUISetting(tuiKeyAllowLAN)
	model.applyBackendStatus(tuiServiceStatus{InstanceID: "backend", Revision: 5, ConfigPath: path, ConfiguredProxyPort: 7892, Mode: "rule", TunState: "off"})
	if !model.settingsDirty || model.stagedSettings == nil || !model.stagedSettings.AllowLAN || model.snapshot.Settings.MixedPort != 7891 {
		t.Fatal("backend update discarded or partially replaced the settings draft")
	}
	message := model.startOperation(func(state *tuiOperationState) {
		commitTUIOperationSettings(state, state.service, controllerClient{}, *state.stagedSettings)
	})().(tuiOperationResultMsg)
	if !strings.Contains(strings.ToLower(message.state.snapshot.Status), "draft conflict") || !message.state.settingsDirty {
		t.Fatalf("conflicting draft attempted an ordinary save: %s", message.state.snapshot.Status)
	}
}

func audit2Backend(t *testing.T, directory string, status func(int) tuiServiceStatus) *tuiServiceClient {
	t.Helper()
	listener, err := net.Listen("unix", filepath.Join(directory, tuiServiceSocketFilename))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		count := 0
		for {
			connection, err := listener.Accept()
			if err != nil {
				return
			}
			var request tuiServiceRequest
			if err := json.NewDecoder(connection).Decode(&request); err == nil {
				count++
				response := status(count)
				response.ProtocolVersion = tuiServiceProtocolVersion
				_ = json.NewEncoder(connection).Encode(response)
			}
			_ = connection.Close()
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	return newTUIServiceClientAt(directory)
}

func audit2Controller(t *testing.T) controllerClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/proxies":
			_, _ = w.Write([]byte(`{"proxies":{"replacement":{"type":"Selector","all":["DIRECT"],"now":"DIRECT"}}}`))
		case "/connections":
			_ = json.NewEncoder(w).Encode(map[string]any{"connections": []any{map[string]any{"id": "other-user", "metadata": map[string]any{"uid": os.Getuid() + 1}}}})
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(server.Close)
	return controllerClient{options: controllerOptions{address: server.URL}, client: server.Client()}
}

func TestAudit2RefreshUsesAuthoritativeScopeAndConfig(t *testing.T) {
	useTestCLIRuntimeDirectory(t)
	directory := t.TempDir()
	newPath := filepath.Join(directory, "new.yaml")
	if err := os.WriteFile(newPath, []byte("mixed-port: 7892\nmode: rule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(audit2Controller(t), cliPaths{HomeDir: directory, ConfigPath: filepath.Join(directory, "old.yaml")}, nil, true)
	model.snapshot.ManagedService = true
	model.snapshot.Settings.TunEnabled, model.snapshot.Settings.TunScope = true, tuiTunScopeSystem
	model.service = audit2Backend(t, directory, func(int) tuiServiceStatus {
		return tuiServiceStatus{OK: true, InstanceID: "backend", Revision: 2, Running: true, HomeDir: directory, ConfigPath: newPath, TunState: "off", TunScope: tuiTunScopeUser}
	})
	message := model.startRefresh()().(tuiRefreshResultMsg)
	if len(message.snapshot.Connections) != 0 {
		t.Fatal("old system TUN scope exposed another user's connection")
	}
	if len(message.snapshot.Profiles) != 1 || !message.snapshot.Profiles[0].Current {
		t.Fatal("profile list used the pre-refresh configuration path")
	}
}

func TestAudit2UnavailableStatusKeepsLastValidBackendData(t *testing.T) {
	for _, failed := range []int{1, 2} {
		t.Run(map[int]string{1: "before", 2: "after"}[failed], func(t *testing.T) {
			useTestCLIRuntimeDirectory(t)
			directory := t.TempDir()
			model := newTUIModel(audit2Controller(t), cliPaths{HomeDir: directory}, nil, true)
			model.snapshot.ManagedService = true
			model.snapshot.Groups = []tuiGroup{{Name: "last-valid"}}
			var calls atomic.Int32
			model.service = audit2Backend(t, directory, func(index int) tuiServiceStatus {
				calls.Add(1)
				if index == failed {
					return tuiServiceStatus{OK: false, Error: "temporary status failure"}
				}
				return tuiServiceStatus{OK: true, InstanceID: "backend", Revision: 2, Running: true, Mode: "rule"}
			})
			message := model.startRefresh()().(tuiRefreshResultMsg)
			model.update(message)
			if len(model.snapshot.Groups) != 1 || model.snapshot.Groups[0].Name != "last-valid" {
				t.Fatalf("unverified data replaced last valid groups (status calls=%d)", calls.Load())
			}
			if model.refreshInFlight {
				t.Fatal("failed refresh left the frontend permanently busy")
			}
		})
	}
}

func TestAudit2PortInputRejectsBackendChangesWhileTyping(t *testing.T) {
	directory := t.TempDir()
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: directory, ConfigPath: filepath.Join(directory, "config.yaml")}, nil, true)
	model.service = newTUIServiceClientAt(directory)
	model.backendInstanceID, model.backendRevision = "backend", 4
	model.beginInput(tuiInputMixedPort)
	model.inputValue = []rune("7892")
	model.applyBackendStatus(tuiServiceStatus{InstanceID: "backend", Revision: 5, ConfigPath: model.paths.ConfigPath, ConfiguredProxyPort: 7893, Mode: "rule", TunState: "off"})
	command := model.submitInput()
	if command != nil {
		model.update(command())
	}
	if !strings.Contains(strings.ToLower(model.snapshot.Status), "draft conflict") || model.inputMode != tuiInputMixedPort || string(model.inputValue) != "7892" {
		t.Fatalf("port edit failed to retain input on conflict: %q mode=%d value=%q", model.snapshot.Status, model.inputMode, model.inputValue)
	}
	model.handleInput(tea.KeyMsg{Type: tea.KeyEsc})
}

func TestAudit2NewProfileOrderIsNotReplacedByOldProfile(t *testing.T) {
	current := tuiSnapshot{GroupOrder: []string{"old", "new"}}
	updated := tuiSnapshot{GroupOrder: []string{"new", "old"}, Groups: []tuiGroup{{Name: "new"}, {Name: "old"}}}
	merged := mergeTUIRefresh(current, updated)
	if len(merged.GroupOrder) != 2 || merged.GroupOrder[0] != "new" || merged.Groups[0].Name != "new" {
		t.Fatal("old profile group order survived a configuration refresh")
	}
}

func TestAudit2DraftRejectsInstancePathAndRevisionChanges(t *testing.T) {
	for _, kind := range []string{"instance", "path", "revision"} {
		t.Run(kind, func(t *testing.T) {
			base := &tuiSettingsDraftBase{InstanceID: "one", Revision: 4, ConfigPath: "/config/one.yaml"}
			state := tuiOperationState{backendInstanceID: base.InstanceID, backendRevision: base.Revision, paths: cliPaths{ConfigPath: base.ConfigPath}, settingsDraft: base, settingsDirty: true}
			if !validateTUISettingsDraft(&state) {
				t.Fatal("unchanged draft was rejected")
			}
			switch kind {
			case "instance":
				state.backendInstanceID = "replacement"
			case "path":
				state.paths.ConfigPath = "/config/two.yaml"
			case "revision":
				state.backendRevision++
			}
			if validateTUISettingsDraft(&state) || !state.settingsConflict || !state.settingsDirty {
				t.Fatal("draft was rebound or discarded")
			}
		})
	}
}

func TestAudit2PortEditRestoredAfterBackendRejectsRevision(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(path, []byte("mixed-port: 7891\nmode: rule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: directory, ConfigPath: path}, nil, true)
	model.service = audit2Backend(t, directory, func(int) tuiServiceStatus {
		return tuiServiceStatus{OK: false, InstanceID: "backend", Revision: 5, ErrorCode: tuiServiceErrorConflict, Error: "revision changed"}
	})
	model.backendInstanceID, model.backendRevision = "backend", 4
	model.beginInput(tuiInputMixedPort)
	model.inputValue = []rune("7892")
	command := model.submitInput()
	if command == nil {
		t.Fatal("valid edit did not issue a save")
	}
	model.update(command())
	if model.inputMode != tuiInputMixedPort || string(model.inputValue) != "7892" || model.inputSettingsDraft == nil || model.inputSettingsDraft.Revision != 4 {
		t.Fatal("late save conflict lost the original port edit")
	}
	model.handleInput(tea.KeyMsg{Type: tea.KeyEsc})
	if model.inputSettingsDraft != nil || model.settingsDraft != nil {
		t.Fatal("cancelled port edit contaminated subsequent settings operations")
	}
}

func TestAudit2UnavailableRefreshStillUpdatesIndependentSSH(t *testing.T) {
	current := tuiSnapshot{Groups: []tuiGroup{{Name: "latest"}}, BackendLogs: []string{"backend"}, LogsInitialized: true, Connections: []tuiConnection{{ID: "proxy", Source: tuiTrafficSourceProxy}, {ID: "ssh:old", Source: tuiTrafficSourceSSH}}}
	refreshed := tuiSnapshot{Groups: []tuiGroup{{Name: "stale"}}, SSHProfiles: []tuiSSHProfile{{Name: "ssh-new"}}, Connections: []tuiConnection{{ID: "ssh:new", Source: tuiTrafficSourceSSH}}}
	merged := mergeTUIUnavailableBackend(current, refreshed, cliPaths{}, cliPaths{})
	if merged.Groups[0].Name != "latest" || merged.BackendLogs[0] != "backend" || len(merged.SSHProfiles) != 1 || len(merged.Connections) != 2 || merged.Connections[0].ID != "proxy" || merged.Connections[1].ID != "ssh:new" {
		t.Fatal("Backend outage blocked SSH or rolled back current Backend data")
	}
}

func TestAudit2ExplicitReloadRecoversConflictingDraft(t *testing.T) {
	useTestCLIRuntimeDirectory(t)
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(path, []byte("mixed-port: 7891\nmode: rule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(audit2Controller(t), cliPaths{HomeDir: directory, ConfigPath: path}, nil, true)
	model.service = audit2Backend(t, directory, func(int) tuiServiceStatus {
		return tuiServiceStatus{OK: true, InstanceID: "backend", Revision: 6, ConfigPath: path, ConfiguredProxyPort: 7893, Mode: "rule", TunState: "off"}
	})
	model.backendInstanceID, model.backendRevision = "backend", 4
	model.stageTUIAdjustedPort(tuiKeyPortUp)
	if err := os.WriteFile(path, []byte("mixed-port: 7893\nmode: rule\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	model.applyBackendStatus(tuiServiceStatus{InstanceID: "backend", Revision: 5, ConfigPath: path, ConfiguredProxyPort: 7893, Mode: "rule", TunState: "off"})
	command := model.handleKey(tuiKeyReload)
	if command == nil {
		t.Fatal("explicit reload did not start")
	}
	model.update(command())
	if model.settingsDirty || model.settingsDraft != nil || model.snapshot.Settings.MixedPort != 7893 || model.stagedSettings == nil || model.stagedSettings.MixedPort != 7893 {
		t.Fatal("reload retained the obsolete draft or lost the latest profile settings")
	}
}
