//go:build linux && !cgo && cli

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIStagesMixedPortUntilCoreStart(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{ConfigPath: filepath.Join(t.TempDir(), "missing.yaml")},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.FocusSidebar = false
	model.snapshot.Settings.MixedPort = 7890
	model.inputMode = tuiInputMixedPort
	model.inputValue = []rune("17890")

	command := model.submitInput()
	if command != nil {
		t.Fatal("staging a stopped core port unexpectedly started an asynchronous operation")
	}
	if model.coreRunning {
		t.Fatal("core was marked running while staging a port")
	}
	if model.pendingMixedPort == nil || *model.pendingMixedPort != 17890 {
		t.Fatalf("pending mixed port = %v", model.pendingMixedPort)
	}
	if model.snapshot.Settings.MixedPort != 17890 {
		t.Fatalf("displayed mixed port = %d", model.snapshot.Settings.MixedPort)
	}

	model.refreshSequence = 1
	model.refreshInFlight = true
	_, _ = model.Update(tuiRefreshResultMsg{
		sequence: 1,
		snapshot: tuiSnapshot{
			Status:    "Connected",
			UpdatedAt: time.Now(),
			Settings:  tuiSettings{MixedPort: 0},
		},
	})
	if model.snapshot.Settings.MixedPort != 17890 {
		t.Fatalf("refresh discarded staged mixed port: %d", model.snapshot.Settings.MixedPort)
	}
}

func TestTUIStagesPreStartSettings(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{ConfigPath: filepath.Join(t.TempDir(), "missing.yaml")},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageTools
	model.snapshot.FocusSidebar = false
	model.snapshot.Settings = tuiSettings{
		Mode:      "rule",
		MixedPort: 17890,
		AllowLAN:  false,
		IPv6:      true,
		LogLevel:  "info",
	}

	for _, key := range []tuiKey{
		tuiKeyAllowLAN,
		tuiKeyIPv6,
		tuiKeyLogLevel,
	} {
		if command := model.handleKey(key); command != nil {
			t.Fatalf("staging key %v unexpectedly returned an operation", key)
		}
	}
	model.snapshot.Page = tuiPageDashboard
	if command := model.handleKey(tuiKeyTun); command != nil {
		t.Fatalf("staging key %v unexpectedly returned an operation", tuiKeyTun)
	}
	if command := model.changeMode("global"); command != nil {
		t.Fatal("staging a selected mode unexpectedly returned an operation")
	}
	if model.coreRunning {
		t.Fatal("staging settings started the core")
	}
	if model.stagedSettings == nil {
		t.Fatal("settings were not staged")
	}
	if !model.stagedSettings.TunEnabled ||
		!model.stagedSettings.AllowLAN ||
		model.stagedSettings.IPv6 ||
		model.stagedSettings.Mode != "global" ||
		model.stagedSettings.LogLevel != "debug" {
		t.Fatalf("unexpected staged settings: %+v", *model.stagedSettings)
	}

	model.refreshSequence = 1
	model.refreshInFlight = true
	_, _ = model.Update(tuiRefreshResultMsg{
		sequence: 1,
		snapshot: tuiSnapshot{
			Status:    "Connected",
			UpdatedAt: time.Now(),
			Settings: tuiSettings{
				Mode:       "direct",
				MixedPort:  0,
				AllowLAN:   false,
				IPv6:       true,
				LogLevel:   "silent",
				TunEnabled: false,
			},
		},
	})
	if model.snapshot.Settings != *model.stagedSettings {
		t.Fatalf("refresh discarded staged settings: %+v", model.snapshot.Settings)
	}
}

func TestTUIDashboardRefreshKeepsSilentTUNAndFLCOverlay(t *testing.T) {
	directory := t.TempDir()
	socketPath := filepath.Join(directory, tuiServiceSocketFilename)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	actions := make(chan string, 8)
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			var request tuiServiceRequest
			if decodeErr := json.NewDecoder(connection).Decode(&request); decodeErr != nil {
				_ = connection.Close()
				continue
			}
			select {
			case actions <- request.Action:
			default:
			}
			_ = json.NewEncoder(connection).Encode(tuiServiceStatus{
				ProtocolVersion:     tuiServiceProtocolVersion,
				RequestID:           request.RequestID,
				Revision:            3,
				OK:                  true,
				Running:             true,
				Mode:                tuiSilentMode,
				ProxyPort:           17890,
				ConfiguredProxyPort: 17890,
				ActiveProxyPort:     17890,
				TunState:            "off",
				TunScope:            tuiTunScopeSystem,
				FLCEnabled:          true,
				FLCOutbound:         "PROXY",
			})
			_ = connection.Close()
		}
	}()

	controller := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/proxies":
			_, _ = io.WriteString(w, `{"proxies":{"PROXY":{"type":"Selector","now":"hk-1","all":["DIRECT","hk-1"]}}}`)
		case "/connections":
			_, _ = io.WriteString(w, `{"connections":[]}`)
		case "/configs":
			_, _ = io.WriteString(w, `{
				"mode":"rule",
				"mixed-port":17890,
				"allow-lan":false,
				"ipv6":false,
				"unified-delay":true,
				"tcp-concurrent":true,
				"log-level":"info",
				"tun":{"enable":true}
			}`)
		case "/providers/proxies":
			_, _ = io.WriteString(w, `{"providers":{}}`)
		default:
			http.NotFound(w, request)
		}
	}))
	defer controller.Close()

	model := newTUIModel(
		controllerClient{
			options: controllerOptions{address: controller.URL},
			client:  controller.Client(),
		},
		cliPaths{},
		nil,
		true,
	)
	model.service = newTUIServiceClientAt(directory)
	model.coreRunning = true
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.Settings = tuiSettings{
		Mode:       tuiSilentMode,
		MixedPort:  17890,
		TunEnabled: false,
		TunScope:   tuiTunScopeSystem,
	}
	model.snapshot.FLCEnabled = true
	model.snapshot.FLCOutbound = "PROXY"
	model.snapshot.Groups = []tuiGroup{{
		Name:  "PROXY",
		Now:   "hk-1",
		Nodes: []string{"DIRECT", "hk-1"},
	}}

	refresh := model.startRefresh()
	if refresh == nil {
		t.Fatal("Dashboard refresh did not start")
	}
	if model.refreshIncludesHistory || model.refreshIncludesLogs {
		t.Fatalf(
			"Dashboard refresh scheduled History/Logs bulk fetch: history=%t logs=%t",
			model.refreshIncludesHistory,
			model.refreshIncludesLogs,
		)
	}
	_, _ = model.Update(refresh())

	if model.snapshot.Settings.Mode != tuiSilentMode {
		t.Fatalf("Dashboard refresh dropped silent mode: %+v", model.snapshot.Settings)
	}
	if model.snapshot.Settings.TunEnabled ||
		model.snapshot.Settings.TunScope != tuiTunScopeSystem {
		t.Fatalf("Dashboard refresh dropped SYSTEM TUN overlay: %+v", model.snapshot.Settings)
	}
	if label := tuiTUNLabel(model.snapshot); label != "OFF · locked by silent mode" {
		t.Fatalf("TUN label = %q", label)
	}
	if label := tuiFLCOutboundLabel(model.snapshot); !strings.Contains(label, "READY") &&
		!strings.Contains(label, "WAITING FOR CORE") {
		t.Fatalf("flc lost silent suffix: %q", label)
	}
	if !strings.Contains(tuiFLCOutboundLabel(model.snapshot), "PROXY") {
		t.Fatalf("flc outbound was lost: %q", tuiFLCOutboundLabel(model.snapshot))
	}

	select {
	case action := <-actions:
		if action != "status" {
			t.Fatalf("Dashboard refresh called %q, want status", action)
		}
	case <-time.After(time.Second):
		t.Fatal("Dashboard refresh did not call Backend status")
	}
	select {
	case action := <-actions:
		t.Fatalf("Dashboard refresh made extra Backend call %q", action)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestTUIStoppedRefreshKeepsBackendModeAndTunState(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.stagedSettings = &tuiSettings{
		Mode:       "rule",
		MixedPort:  7890,
		TunEnabled: true,
		TunScope:   tuiTunScopeUser,
	}
	model.refreshSequence = 1
	model.refreshInFlight = true
	serviceStatus := &tuiServiceStatus{
		Running:             false,
		Mode:                tuiSilentMode,
		ConfiguredProxyPort: 7890,
		TunState:            "off",
		TunScope:            tuiTunScopeSystem,
	}

	_, _ = model.Update(tuiRefreshResultMsg{
		sequence: 1,
		snapshot: tuiSnapshot{
			Status:   "Connected",
			Settings: tuiSettings{Mode: "rule", TunEnabled: true},
		},
		serviceStatus: serviceStatus,
	})

	if model.snapshot.Settings.Mode != tuiSilentMode ||
		model.snapshot.Settings.TunEnabled ||
		model.snapshot.Settings.TunScope != tuiTunScopeSystem {
		t.Fatalf(
			"staged YAML hid Backend mode/TUN state: %+v",
			model.snapshot.Settings,
		)
	}
	if !strings.Contains(model.snapshot.Status, "start Core") {
		t.Fatalf("stopped Dashboard guidance = %q", model.snapshot.Status)
	}
}

func TestSyncStoppedSettingsSeparatesBackendAndProfileState(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	profileSettings := tuiSettings{
		Mode:       "global",
		MixedPort:  12345,
		LogLevel:   "info",
		TunEnabled: true,
	}
	updated, err := applyTUISettingsToConfig(
		[]byte(defaultTUIConfig),
		profileSettings,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, updated, 0o600); err != nil {
		t.Fatal(err)
	}
	state := tuiOperationState{
		paths: cliPaths{HomeDir: directory, ConfigPath: configPath},
		snapshot: tuiSnapshot{Settings: tuiSettings{
			Mode:        tuiSilentMode,
			SystemProxy: false,
			TunEnabled:  false,
			TunScope:    tuiTunScopeSystem,
		}},
	}

	syncStoppedTUISettings(&state)
	if state.stagedSettings == nil ||
		state.stagedSettings.Mode != "global" ||
		!state.stagedSettings.TunEnabled {
		t.Fatalf("profile staged settings = %+v", state.stagedSettings)
	}
	if state.snapshot.Settings.Mode != tuiSilentMode ||
		state.snapshot.Settings.TunEnabled ||
		state.snapshot.Settings.TunScope != tuiTunScopeSystem {
		t.Fatalf("Backend display state was overwritten: %+v", state.snapshot.Settings)
	}
}

func TestTUISilentStopStartDoesNotCommitDisplayMode(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(configPath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen(
		"unix",
		filepath.Join(directory, tuiServiceSocketFilename),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requests := make(chan tuiServiceRequest, 2)
	serverDone := make(chan error, 1)
	go func() {
		for index := 0; index < 2; index++ {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				serverDone <- acceptErr
				return
			}
			var request tuiServiceRequest
			decodeErr := json.NewDecoder(connection).Decode(&request)
			if decodeErr == nil {
				requests <- request
				decodeErr = json.NewEncoder(connection).Encode(tuiServiceStatus{
					OK:                  true,
					Revision:            uint64(8 + index),
					Running:             index == 1,
					Mode:                tuiSilentMode,
					ConfigPath:          configPath,
					ConfiguredProxyPort: 12345,
					TunState:            "off",
					TunScope:            tuiTunScopeUser,
					FLCEnabled:          index == 1,
					FLCOutbound:         "PROXY",
				})
			}
			_ = connection.Close()
			if decodeErr != nil {
				serverDone <- decodeErr
				return
			}
		}
		serverDone <- nil
	}()

	state := tuiOperationState{
		paths:           cliPaths{HomeDir: directory, ConfigPath: configPath},
		coreRunning:     true,
		backendRevision: 7,
		snapshot: tuiSnapshot{Settings: tuiSettings{
			Mode:      tuiSilentMode,
			MixedPort: 12345,
		}},
	}
	service := newTUIServiceClientAt(directory)
	if !stopTUIManagedCore(&state, service) {
		t.Fatalf("silent Core stop failed: %s", state.snapshot.Status)
	}
	if state.stagedSettings == nil || state.stagedSettings.Mode != "rule" ||
		state.settingsDirty || state.snapshot.Settings.Mode != tuiSilentMode {
		t.Fatalf("stopped silent state = %+v", state)
	}
	if !startTUIManagedCore(&state, service) {
		t.Fatalf("silent Core restart failed: %s", state.snapshot.Status)
	}
	first := <-requests
	second := <-requests
	if first.Action != "stop" || second.Action != "start" {
		t.Fatalf("silent restart requests = %q, %q", first.Action, second.Action)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if !state.coreRunning || state.snapshot.Settings.Mode != tuiSilentMode ||
		!state.snapshot.FLCEnabled {
		t.Fatalf("restarted silent state = %+v", state)
	}
}

func TestTUISilentDirtyStagedSettingsRecoverNativeProfileMode(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	profileSettings := tuiSettings{
		Mode:       "global",
		MixedPort:  12345,
		LogLevel:   "info",
		TunEnabled: true,
	}
	updated, err := applyTUISettingsToConfig(
		[]byte(defaultTUIConfig),
		profileSettings,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, updated, 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen(
		"unix",
		filepath.Join(directory, tuiServiceSocketFilename),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requests := make(chan tuiServiceRequest, 3)
	serverDone := make(chan error, 1)
	go func() {
		for index := 0; index < 3; index++ {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				serverDone <- acceptErr
				return
			}
			var request tuiServiceRequest
			decodeErr := json.NewDecoder(connection).Decode(&request)
			if decodeErr == nil {
				requests <- request
				decodeErr = json.NewEncoder(connection).Encode(tuiServiceStatus{
					OK:                  true,
					Revision:            uint64(20 + index),
					Running:             index == 2,
					Mode:                tuiSilentMode,
					ConfigPath:          configPath,
					ConfiguredProxyPort: 12346,
					TunState:            "off",
					TunScope:            tuiTunScopeUser,
					FLCEnabled:          index == 2,
					FLCOutbound:         "PROXY",
				})
			}
			_ = connection.Close()
			if decodeErr != nil {
				serverDone <- decodeErr
				return
			}
		}
		serverDone <- nil
	}()

	state := tuiOperationState{
		paths:         cliPaths{HomeDir: directory, ConfigPath: configPath},
		settingsDirty: true,
		stagedSettings: &tuiSettings{
			Mode:       tuiSilentMode,
			MixedPort:  12346,
			LogLevel:   "debug",
			TunEnabled: false,
		},
		snapshot: tuiSnapshot{Settings: tuiSettings{
			Mode:      tuiSilentMode,
			MixedPort: 12346,
		}},
	}
	if !startTUIManagedCore(&state, newTUIServiceClientAt(directory)) {
		t.Fatalf("dirty silent Core start failed: %s", state.snapshot.Status)
	}
	statusRequest := <-requests
	applyRequest := <-requests
	startRequest := <-requests
	if statusRequest.Action != "status" || applyRequest.Action != "apply_settings" ||
		startRequest.Action != "start" {
		t.Fatalf(
			"dirty silent start requests = %q, %q, %q",
			statusRequest.Action,
			applyRequest.Action,
			startRequest.Action,
		)
	}
	if applyRequest.Settings == nil ||
		applyRequest.Settings.Mode != "global" ||
		!applyRequest.Settings.TunEnabled ||
		applyRequest.Settings.MixedPort != 12346 ||
		applyRequest.Settings.LogLevel != "debug" {
		t.Fatalf("recovered profile settings = %+v", applyRequest.Settings)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if !state.coreRunning || state.snapshot.Settings.Mode != tuiSilentMode ||
		!state.snapshot.FLCEnabled {
		t.Fatalf("recovered silent state = %+v", state)
	}
}

func TestTUISilentSettingsCommitPreservesNativeModeAndTun(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	profileSettings := tuiSettings{
		Mode:       "global",
		MixedPort:  12345,
		LogLevel:   "info",
		TunEnabled: true,
	}
	updated, err := applyTUISettingsToConfig(
		[]byte(defaultTUIConfig),
		profileSettings,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, updated, 0o600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen(
		"unix",
		filepath.Join(directory, tuiServiceSocketFilename),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requestReceived := make(chan tuiServiceRequest, 1)
	serverDone := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer connection.Close()
		var request tuiServiceRequest
		if decodeErr := json.NewDecoder(connection).Decode(&request); decodeErr != nil {
			serverDone <- decodeErr
			return
		}
		requestReceived <- request
		serverDone <- json.NewEncoder(connection).Encode(tuiServiceStatus{
			OK:                  true,
			Revision:            12,
			Mode:                tuiSilentMode,
			ConfigPath:          configPath,
			ConfiguredProxyPort: 12346,
			TunState:            "off",
			TunScope:            tuiTunScopeUser,
		})
	}()

	state := tuiOperationState{
		paths:           cliPaths{HomeDir: directory, ConfigPath: configPath},
		backendRevision: 11,
		snapshot: tuiSnapshot{Settings: tuiSettings{
			Mode:      tuiSilentMode,
			MixedPort: 12345,
		}},
	}
	desired := state.snapshot.Settings
	desired.MixedPort = 12346
	desired.AllowLAN = true
	coreServer := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path != "/configs" {
			http.NotFound(w, request)
			return
		}
		_, _ = io.WriteString(w, `{
  "mode": "global",
  "mixed-port": 55555,
  "allow-lan": true,
  "log-level": "info",
  "tun": {"enable": true}
}`)
	}))
	defer coreServer.Close()
	commitTUIOperationSettings(
		&state,
		newTUIServiceClientAt(directory),
		controllerClient{
			options: controllerOptions{address: coreServer.URL},
			client:  coreServer.Client(),
		},
		desired,
	)
	request := <-requestReceived
	if request.Action != "apply_settings" || request.Settings == nil ||
		request.Settings.Mode != "global" ||
		!request.Settings.TunEnabled ||
		request.Settings.MixedPort != 12346 {
		t.Fatalf("profile settings request = %+v", request)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	if state.snapshot.Settings.Mode != tuiSilentMode ||
		state.snapshot.Settings.TunEnabled ||
		state.snapshot.Settings.MixedPort != 12346 {
		t.Fatalf("post-commit display state = %+v", state.snapshot.Settings)
	}
}

func TestTUIRunningCoreUsesLiveSettingsInsteadOfStagedYAML(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Settings = tuiSettings{MixedPort: 12345}
	model.stagedSettings = &tuiSettings{MixedPort: 12345}
	stagedPort := 12345
	model.pendingMixedPort = &stagedPort
	model.settingsDirty = true

	model.initializeCoreRuntime(true)
	if model.stagedSettings != nil || model.pendingMixedPort != nil || model.settingsDirty {
		t.Fatalf(
			"running Core retained staged settings: staged=%+v pending=%v dirty=%v",
			model.stagedSettings,
			model.pendingMixedPort,
			model.settingsDirty,
		)
	}

	model.refreshSequence = 1
	model.refreshInFlight = true
	_, _ = model.Update(tuiRefreshResultMsg{
		sequence: 1,
		snapshot: tuiSnapshot{
			Status:    "Connected",
			UpdatedAt: time.Now(),
			Settings:  tuiSettings{MixedPort: 8001},
		},
	})
	if model.snapshot.Settings.MixedPort != 8001 {
		t.Fatalf(
			"Dashboard mixed port = %d, want live Core port 8001",
			model.snapshot.Settings.MixedPort,
		)
	}
}

func TestTUIOperationResultKeepsLiveTrafficUpdates(t *testing.T) {
	current := tuiSnapshot{
		Traffic:        trafficSnapshot{Up: 11, Down: 22},
		TrafficHistory: []trafficSnapshot{{Up: 11, Down: 22}},
		TotalTraffic:   trafficSnapshot{Up: 33, Down: 44},
	}
	staleResult := tuiSnapshot{
		Traffic:        trafficSnapshot{Up: 1, Down: 2},
		TrafficHistory: []trafficSnapshot{{Up: 1, Down: 2}},
		TotalTraffic:   trafficSnapshot{Up: 3, Down: 4},
	}

	merged := mergeTUIOperation(current, staleResult)
	if merged.Traffic != current.Traffic ||
		merged.TotalTraffic != current.TotalTraffic ||
		!slices.Equal(merged.TrafficHistory, current.TrafficHistory) {
		t.Fatalf(
			"operation result restored stale traffic: current=%+v/%+v merged=%+v/%+v",
			current.Traffic,
			current.TotalTraffic,
			merged.Traffic,
			merged.TotalTraffic,
		)
	}
}

func TestBackendImportsSubscriptionWithoutStartingCore(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.UserAgent() != tuiSubscriptionUserAgent {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if !strings.Contains(request.Header.Get("Accept"), "application/yaml") {
			http.Error(w, "missing YAML accept header", http.StatusNotAcceptable)
			return
		}
		_, _ = io.WriteString(w, `mixed-port: 17891
mode: rule
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,PROXY
`)
	}))
	defer server.Close()

	directory := t.TempDir()
	runtime := newTUIServiceRuntime(
		cliPaths{
			HomeDir:    directory,
			ConfigPath: filepath.Join(directory, "config.yaml"),
		},
		defaultCLITestURL,
		filepath.Join(directory, "core.sock"),
		nil,
		nil,
	)
	data, err := fetchTUISubscription(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "profile-imported.yaml")
	revision := uint64(1)
	sourceURL := server.URL
	status := runtime.handle(tuiServiceRequest{
		ProtocolVersion:  tuiServiceProtocolVersion,
		RequestID:        "import-profile",
		ExpectedRevision: &revision,
		Action:           "put_profile",
		ConfigPath:       path,
		ProfileData:      data,
		CreateOnly:       true,
		SubscriptionURL:  &sourceURL,
	})
	if !status.OK {
		t.Fatalf("import response = %+v", status)
	}
	if status.Running {
		t.Fatal("subscription import started the core")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	linkedURL, err := loadTUISubscriptionSource(directory, path)
	if err != nil || linkedURL != server.URL {
		t.Fatalf("subscription source = %q, %v", linkedURL, err)
	}
}

func TestTUIUpdatesSubscriptionAndPreservesLocalSettings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `mixed-port: 9999
mode: rule
allow-lan: false
ipv6: false
unified-delay: true
tcp-concurrent: true
log-level: info
tun:
  enable: false
proxies:
  - name: NEW-NODE
    type: direct
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - NEW-NODE
rules:
  - MATCH,PROXY
`)
	}))
	defer server.Close()

	directory := t.TempDir()
	activePath := filepath.Join(directory, "config.yaml")
	profilePath := filepath.Join(directory, "work.yaml")
	if err := os.WriteFile(activePath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	original := `mixed-port: 17890
mode: global
allow-lan: true
ipv6: true
unified-delay: false
tcp-concurrent: false
log-level: debug
tun:
  enable: true
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,PROXY
`
	if err := os.WriteFile(profilePath, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := rememberTUISubscriptionSource(directory, profilePath, server.URL); err != nil {
		t.Fatal(err)
	}

	runtime := newTUIServiceRuntime(
		cliPaths{HomeDir: directory, ConfigPath: activePath},
		defaultCLITestURL,
		filepath.Join(directory, "core.sock"),
		nil,
		nil,
	)
	updated, err := fetchTUISubscription(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	settings := loadTUIConfiguredSettings(profilePath, true)
	if settings == nil {
		t.Fatal("could not read original local settings")
	}
	updated, err = applyTUISettingsToConfig(updated, *settings)
	if err != nil {
		t.Fatal(err)
	}
	revision := uint64(1)
	sourceURL := server.URL
	status := runtime.handle(tuiServiceRequest{
		ProtocolVersion:  tuiServiceProtocolVersion,
		RequestID:        "update-profile",
		ExpectedRevision: &revision,
		Action:           "put_profile",
		ConfigPath:       profilePath,
		ProfileData:      updated,
		ExpectedSHA256:   tuiBytesSHA256([]byte(original)),
		SubscriptionURL:  &sourceURL,
	})
	if !status.OK {
		t.Fatalf("update response = %+v", status)
	}

	data, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	for _, expected := range []string{
		"name: NEW-NODE",
		"mixed-port: 17890",
		"mode: global",
		"allow-lan: true",
		"ipv6: true",
		"unified-delay: false",
		"tcp-concurrent: false",
		"log-level: debug",
		"enable: true",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("updated profile does not contain %q:\n%s", expected, output)
		}
	}
	info, err := os.Stat(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("updated profile mode = %o, want 640", info.Mode().Perm())
	}
	if status.ResultPath != profilePath || status.Revision != 2 {
		t.Fatalf("subscription update status = %+v", status)
	}
}

func TestTUIUnlinkedProfileDoesNotPretendToRefreshSubscription(t *testing.T) {
	directory := t.TempDir()
	activePath := filepath.Join(directory, "config.yaml")
	profilePath := filepath.Join(directory, "legacy.yaml")
	for _, path := range []string{activePath, profilePath} {
		if err := os.WriteFile(path, []byte(defaultTUIConfig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	model := newTUIModel(
		controllerClient{},
		cliPaths{HomeDir: directory, ConfigPath: activePath},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageProfiles
	model.snapshot.FocusSidebar = false
	model.snapshot.Profiles = []tuiProfile{{
		Name: "legacy.yaml",
		Path: profilePath,
	}}
	model.snapshot.SelectedRow = 0

	if command := model.handleKey(tuiKeyUpdateProfile); command != nil {
		t.Fatal("unlinked profile unexpectedly started a subscription refresh")
	}
	if model.inputMode != tuiInputNone {
		t.Fatalf("unlinked profile opened manual URL input: %d", model.inputMode)
	}
	if !strings.Contains(model.snapshot.Status, "not linked to a subscription") {
		t.Fatalf("unlinked profile status = %q", model.snapshot.Status)
	}
}

func TestTUIProfileDeleteValidationAndTransaction(t *testing.T) {
	directory := t.TempDir()
	activePath := filepath.Join(directory, "config.yaml")
	targetPath := filepath.Join(directory, "school.yaml")
	for _, path := range []string{activePath, targetPath} {
		if err := os.WriteFile(path, []byte(defaultTUIConfig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	model := newTUIModel(
		controllerClient{},
		cliPaths{HomeDir: directory, ConfigPath: activePath},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageProfiles
	model.snapshot.FocusSidebar = false
	model.snapshot.SelectedRow = tuiProfileImportSubscriptionRow
	model.beginProfileDeleteConfirm()
	if model.profileDeleteOpen || !strings.Contains(model.snapshot.Status, "saved Profile") {
		t.Fatalf("import row deletion status = %q", model.snapshot.Status)
	}
	model.snapshot.Profiles = []tuiProfile{{
		Name:    "config.yaml",
		Path:    activePath,
		Current: true,
	}}
	model.snapshot.SelectedRow = 0
	model.beginProfileDeleteConfirm()
	if model.profileDeleteOpen || !strings.Contains(model.snapshot.Status, "active Profile") {
		t.Fatalf("active Profile deletion status = %q", model.snapshot.Status)
	}
	model.snapshot.Profiles = []tuiProfile{{
		Name:            "school.yaml",
		Path:            targetPath,
		SubscriptionURL: "https://secret.example/subscription-token",
	}}
	model.beginProfileDeleteConfirm()
	if model.profileDeleteOpen || !strings.Contains(model.snapshot.Status, "managed Backend") {
		t.Fatalf("unmanaged Profile deletion status = %q", model.snapshot.Status)
	}

	socketDirectory := t.TempDir()
	socketPath := filepath.Join(socketDirectory, tuiServiceSocketFilename)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requests := make(chan tuiServiceRequest, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		var request tuiServiceRequest
		if decodeErr := json.NewDecoder(connection).Decode(&request); decodeErr != nil {
			return
		}
		requests <- request
		if request.Action == "delete_profile" {
			_ = os.Remove(request.ConfigPath)
		}
		_ = json.NewEncoder(connection).Encode(tuiServiceStatus{
			OK:         true,
			Revision:   2,
			ConfigPath: activePath,
		})
	}()
	model.service = newTUIServiceClientAt(socketDirectory)
	model.backendRevision = 1
	model.beginProfileDeleteConfirm()
	if !model.profileDeleteOpen || model.profileDeleteKind != "subscription" {
		t.Fatalf("Profile confirmation state = %+v", model.snapshot.ProfileDelete)
	}
	confirmation := stripTUIANSI(model.View())
	if !strings.Contains(confirmation, "Delete school.yaml (subscription)?") ||
		strings.Contains(confirmation, "subscription-token") {
		t.Fatalf("unsafe or incomplete Profile confirmation:\n%s", confirmation)
	}
	_, cancelCommand := model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cancelCommand != nil || model.profileDeleteOpen {
		t.Fatal("Profile deletion confirmation did not cancel in place")
	}
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatalf("cancelled Profile deletion changed the file: %v", err)
	}
	model.beginProfileDeleteConfirm()
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if command == nil || model.profileDeleteOpen {
		t.Fatal("Profile confirmation did not schedule deletion")
	}
	rawMessage := command()
	message, ok := rawMessage.(tuiOperationResultMsg)
	if !ok {
		t.Fatalf("Profile deletion result type = %T", rawMessage)
	}
	_, _ = model.Update(message)
	request := <-requests
	if request.Action != "delete_profile" || request.ConfigPath != targetPath ||
		request.ExpectedRevision == nil || *request.ExpectedRevision != 1 {
		t.Fatalf("Profile deletion request = %+v", request)
	}
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Fatalf("deleted Profile still exists: %v", err)
	}
	if !strings.Contains(model.snapshot.Status, "Profile deleted: school.yaml") {
		t.Fatalf("Profile deletion status = %q", model.snapshot.Status)
	}
}

func TestTUIActiveSubscriptionUpdateReloadsWithoutStartingListeners(t *testing.T) {
	mixedPort := freeTUITestPort(t)
	controllerPort := freeTUITestPort(t)
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	original := fmt.Appendf(nil, `mixed-port: %d
mode: rule
log-level: silent
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,PROXY
`, mixedPort)
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `mixed-port: 9999
mode: global
log-level: info
proxies:
  - name: UPDATED-DIRECT
    type: direct
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - UPDATED-DIRECT
rules:
  - MATCH,PROXY
`)
	}))
	defer server.Close()

	paths := cliPaths{HomeDir: directory, ConfigPath: configPath}
	controllerAddress := fmt.Sprintf("127.0.0.1:%d", controllerPort)
	setupParams, err := initializeCore(
		paths,
		"https://www.gstatic.com/generate_204",
		controllerAddress,
		"",
		"",
		false,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		handleShutdown()
	})
	client := controllerClient{
		options: controllerOptions{address: controllerAddress},
		client:  &http.Client{Timeout: time.Second},
	}
	if err := waitForController(client, 2*time.Second); err != nil {
		t.Fatal(err)
	}
	if err := rememberTUISubscriptionSource(directory, configPath, server.URL); err != nil {
		t.Fatal(err)
	}

	model := newTUIModel(client, paths, setupParams, true)
	model.snapshot.Page = tuiPageProfiles
	model.snapshot.FocusSidebar = false
	model.snapshot.Profiles = []tuiProfile{{
		Name:            "config.yaml",
		Path:            configPath,
		Current:         true,
		SubscriptionURL: server.URL,
	}}
	model.snapshot.SelectedRow = 0
	command := model.handleKey(tuiKeyUpdateProfile)
	if command != nil {
		t.Fatal("TUI without a backend scheduled a shared-profile mutation")
	}
	if !strings.Contains(model.snapshot.Status, "managed backend") {
		t.Fatalf("unmanaged update status = %q", model.snapshot.Status)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, original) {
		t.Fatal("unmanaged TUI modified the active profile")
	}
}

func TestTUIBackgroundServiceHotReloadsSubscriptionWithoutStoppingListeners(
	t *testing.T,
) {
	mixedPort := freeTUITestPort(t)
	directory, err := os.MkdirTemp("/tmp", "flclash-service-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(directory)
	})
	previousRuntimeDirectory := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = directory
	t.Cleanup(func() {
		cliRuntimeDirectoryOverride = previousRuntimeDirectory
	})
	configPath := filepath.Join(directory, "config.yaml")
	original := fmt.Appendf(nil, `mixed-port: %d
mode: rule
log-level: silent
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,PROXY
`, mixedPort)
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rememberTUITrafficMode(directory, "rule"); err != nil {
		t.Fatal(err)
	}
	subscription := httptest.NewServer(http.HandlerFunc(
		func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, `mixed-port: 9999
mode: global
log-level: info
proxies:
  - name: UPDATED-DIRECT
    type: direct
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - UPDATED-DIRECT
rules:
  - MATCH,PROXY
`)
		},
	))
	defer subscription.Close()
	if err := rememberTUISubscriptionSource(
		directory,
		configPath,
		subscription.URL,
	); err != nil {
		t.Fatal(err)
	}

	paths := cliPaths{HomeDir: directory, ConfigPath: configPath}
	serviceDone := make(chan error, 1)
	go func() {
		serviceDone <- runTUIService(
			paths,
			"https://www.gstatic.com/generate_204",
			nil,
			false,
		)
	}()
	service := newTUIServiceClient(directory)
	var status tuiServiceStatus
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		status, err = service.status()
		if err == nil {
			break
		}
		select {
		case serviceErr := <-serviceDone:
			t.Fatalf("background service exited before ready: %v", serviceErr)
		default:
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !status.OK {
		select {
		case serviceErr := <-serviceDone:
			t.Fatalf("background service did not become ready: %v", serviceErr)
		default:
			t.Fatal("background service did not become ready")
		}
	}
	t.Cleanup(func() {
		_ = service.shutdown()
		select {
		case <-serviceDone:
		case <-time.After(2 * time.Second):
		}
	})
	if status.Running {
		t.Fatal("new background service unexpectedly started listeners")
	}
	stoppedModel := newTUIModel(
		controllerClient{},
		paths,
		nil,
		true,
	)
	stoppedModel.service = service
	stoppedModel.coreRunning = false
	stoppedModel.shutdown()
	stoppedStatus, err := service.status()
	if err != nil || stoppedStatus.Running {
		t.Fatalf(
			"q-style detach stopped an idle shared service: %+v, %v",
			stoppedStatus,
			err,
		)
	}
	status, err = service.start()
	if err != nil {
		t.Fatal(err)
	}
	if !status.Running || !waitForTUITestPort(mixedPort, true, 2*time.Second) {
		t.Fatal("background service did not start the mixed listener")
	}
	client := controllerClient{
		options: controllerOptions{unixSocket: status.CoreSocket},
		client: controllerHTTPClientForOptions(
			controllerOptions{unixSocket: status.CoreSocket},
			time.Second,
		),
	}
	model := newTUIModel(client, paths, nil, true)
	model.service = service
	model.coreRunning = true
	model.shutdown()
	detachedStatus, err := service.status()
	if err != nil || !detachedStatus.Running {
		t.Fatalf("q-style TUI detach stopped the background service: %+v, %v", detachedStatus, err)
	}
	model.snapshot.Page = tuiPageProfiles
	model.snapshot.FocusSidebar = false
	model.snapshot.Profiles = []tuiProfile{{
		Name:            "config.yaml",
		Path:            configPath,
		Current:         true,
		SubscriptionURL: subscription.URL,
	}}
	model.snapshot.SelectedRow = 0
	command := model.handleKey(tuiKeyUpdateProfile)
	if command == nil {
		t.Fatal("linked subscription did not start a refresh")
	}
	_, _ = model.Update(command())
	if model.snapshot.Status !=
		"Subscription refreshed and hot-reloaded: config.yaml" {
		t.Fatalf("refresh status = %q", model.snapshot.Status)
	}
	if !model.coreRunning || !waitForTUITestPort(mixedPort, true, time.Second) {
		t.Fatal("subscription hot-reload stopped the running listener")
	}
	proxyData, err := client.request(http.MethodGet, "/proxies", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(proxyData, []byte("UPDATED-DIRECT")) {
		t.Fatalf("background core did not load refreshed nodes: %s", proxyData)
	}
	beforeEdit, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	temporary, err := os.CreateTemp("", "flclash-editor-test-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	temporaryPath := temporary.Name()
	t.Cleanup(func() { _ = os.Remove(temporaryPath) })
	edited := fmt.Appendf(nil, `mixed-port: %d
mode: rule
log-level: silent
proxies:
  - name: EDITED-DIRECT
    type: direct
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - EDITED-DIRECT
rules:
  - MATCH,PROXY
`, mixedPort)
	if _, err := temporary.Write(edited); err != nil {
		t.Fatal(err)
	}
	if err := temporary.Close(); err != nil {
		t.Fatal(err)
	}
	model.editorPath = configPath
	model.editorTempPath = temporaryPath
	model.editorBackup = tuiProfileBackup{data: beforeEdit, mode: 0o600}
	_, editorCommand := model.Update(tuiEditorResultMsg{})
	if editorCommand == nil {
		t.Fatal("active config edit did not schedule a hot-reload")
	}
	_, _ = model.Update(editorCommand())
	if model.snapshot.Status != "Configuration saved and hot-reloaded" {
		t.Fatalf("editor hot-reload status = %q", model.snapshot.Status)
	}
	proxyData, err = client.request(http.MethodGet, "/proxies", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(proxyData, []byte("EDITED-DIRECT")) {
		t.Fatalf("background core did not hot-reload edited config: %s", proxyData)
	}
	if !waitForTUITestPort(mixedPort, true, time.Second) {
		t.Fatal("configuration edit interrupted the mixed listener")
	}
	model.shutdown()
	if !waitForTUITestPort(mixedPort, true, time.Second) {
		t.Fatal("Ctrl+C-style detach stopped the mixed listener")
	}
	if status, err := service.status(); err != nil || !status.Running {
		t.Fatalf("Ctrl+C-style detach stopped the backend: %+v, %v", status, err)
	}
}

func TestTUISubscriptionUpdateRejectsInvalidResponseWithoutChangingProfile(
	t *testing.T,
) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "this is not a Mihomo configuration")
	}))
	defer server.Close()

	directory := t.TempDir()
	profilePath := filepath.Join(directory, "work.yaml")
	original := []byte(defaultTUIConfig)
	if err := os.WriteFile(profilePath, original, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := updateTUISubscriptionProfile(
		directory,
		profilePath,
		server.URL,
	); err == nil {
		t.Fatal("invalid subscription response was accepted")
	}
	after, err := os.ReadFile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, original) {
		t.Fatalf("invalid update changed profile:\n%s", after)
	}
}

func TestTUIProfileRenameMovesSavedSubscriptionSource(t *testing.T) {
	directory := t.TempDir()
	sourcePath := filepath.Join(directory, "old.yaml")
	if err := os.WriteFile(sourcePath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	const sourceURL = "https://example.test/subscription"
	if err := rememberTUISubscriptionSource(directory, sourcePath, sourceURL); err != nil {
		t.Fatal(err)
	}

	newPath, err := renameTUIProfile(directory, sourcePath, "new.yaml")
	if err != nil {
		t.Fatal(err)
	}
	sources := loadTUISubscriptionSources(directory)
	if sources["old.yaml"] != "" || sources["new.yaml"] != sourceURL {
		t.Fatalf("renamed subscription sources = %+v", sources)
	}
	if filepath.Base(newPath) != "new.yaml" {
		t.Fatalf("renamed path = %q", newPath)
	}
}

func TestTUIInvalidProfileEditNeverTouchesOriginalFile(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	original := []byte(defaultTUIConfig)
	if err := os.WriteFile(configPath, original, 0o640); err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(
		controllerClient{},
		cliPaths{HomeDir: directory, ConfigPath: configPath},
		nil,
		true,
	)
	model.editorPath = configPath
	model.editorBackup = tuiProfileBackup{data: original, mode: 0o640}
	temporary, err := os.CreateTemp("", "flclash-invalid-editor-test-*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	temporaryPath := temporary.Name()
	if _, err := temporary.Write([]byte("not a mihomo profile")); err != nil {
		t.Fatal(err)
	}
	if err := temporary.Close(); err != nil {
		t.Fatal(err)
	}
	model.editorTempPath = temporaryPath
	_, command := model.Update(tuiEditorResultMsg{})
	if command != nil {
		t.Fatal("invalid edit unexpectedly scheduled a hot-reload")
	}
	restored, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, original) {
		t.Fatalf("invalid edit was not rolled back:\n%s", restored)
	}
	if !strings.Contains(model.snapshot.Status, "invalid") {
		t.Fatalf("invalid edit status = %q", model.snapshot.Status)
	}
}

func TestTUISubscriptionSourceRejectsProfileOutsideDataDirectory(t *testing.T) {
	directory := t.TempDir()
	outsidePath := filepath.Join(t.TempDir(), "outside.yaml")
	if err := rememberTUISubscriptionSource(
		directory,
		outsidePath,
		"https://example.test/subscription",
	); err == nil {
		t.Fatal("subscription source accepted a profile outside the data directory")
	}
}

func TestPersistTUISettingsUpdatesYAMLWithoutDroppingProfile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	original := `# keep this profile comment
mixed-port: 7890
mode: rule
log-level: info
proxies:
  - name: example
    type: direct
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - example
rules:
  - MATCH,PROXY
`
	if err := os.WriteFile(path, []byte(original), 0o640); err != nil {
		t.Fatal(err)
	}
	settings := tuiSettings{
		Mode:          "global",
		MixedPort:     17890,
		AllowLAN:      true,
		IPv6:          false,
		UnifiedDelay:  true,
		TCPConcurrent: true,
		LogLevel:      "debug",
		TunEnabled:    true,
	}
	if err := persistTUISettings(path, settings); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	for _, expected := range []string{
		"# keep this profile comment",
		"mixed-port: 17890",
		"mode: global",
		"allow-lan: true",
		"ipv6: false",
		"unified-delay: true",
		"tcp-concurrent: true",
		"log-level: debug",
		"enable: true",
		"name: example",
		"MATCH,PROXY",
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("persisted YAML does not contain %q:\n%s", expected, output)
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o640 {
		t.Fatalf("persisted mode = %o, want 640", got)
	}
}

func TestTUIStateRestoresActiveProfileAndProxySelections(t *testing.T) {
	directory := t.TempDir()
	defaultPath := filepath.Join(directory, "config.yaml")
	activePath := filepath.Join(directory, "work.yaml")
	for _, path := range []string{defaultPath, activePath} {
		if err := os.WriteFile(path, []byte(defaultTUIConfig), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	activePaths := cliPaths{
		HomeDir:    directory,
		ConfigPath: activePath,
	}
	if err := rememberTUIActiveProfile(activePaths); err != nil {
		t.Fatal(err)
	}
	if err := rememberTUIProxySelection(directory, "PROXY", "Tokyo"); err != nil {
		t.Fatal(err)
	}
	if err := rememberTUIProxySelection(directory, "AUTO", "Singapore"); err != nil {
		t.Fatal(err)
	}

	restored, err := restoreTUIActiveProfile(cliPaths{
		HomeDir:    directory,
		ConfigPath: defaultPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if restored.ConfigPath != activePath {
		t.Fatalf("restored profile = %q, want %q", restored.ConfigPath, activePath)
	}
	selected := loadTUISelectedProxies(directory)
	if selected["PROXY"] != "Tokyo" || selected["AUTO"] != "Singapore" {
		t.Fatalf("restored proxy selections = %+v", selected)
	}
	info, err := os.Stat(filepath.Join(directory, tuiStateFilename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("saved state mode = %o, want 600", info.Mode().Perm())
	}
}

func TestTUIStateConcurrentUpdatesKeepEveryMutation(t *testing.T) {
	directory := t.TempDir()
	const updates = 32
	var wait sync.WaitGroup
	wait.Add(updates)
	errors := make(chan error, updates)
	for index := 0; index < updates; index++ {
		index := index
		go func() {
			defer wait.Done()
			errors <- rememberTUIProxySelection(
				directory,
				fmt.Sprintf("group-%02d", index),
				fmt.Sprintf("proxy-%02d", index),
			)
		}()
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	selected := loadTUISelectedProxies(directory)
	if len(selected) != updates {
		t.Fatalf("saved %d concurrent updates, want %d", len(selected), updates)
	}
	if _, err := os.Stat(filepath.Join(directory, tuiStateLockName)); err != nil {
		t.Fatalf("stable state lock file is missing: %v", err)
	}
}

func TestTUIProfileRollbackDoesNotOverwriteConcurrentChange(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	original := []byte("mode: rule\n")
	edited := []byte("mode: global\n")
	concurrent := []byte("mode: direct\n")
	if err := os.WriteFile(path, concurrent, 0o600); err != nil {
		t.Fatal(err)
	}
	backup := tuiProfileBackup{
		data:          original,
		mode:          0o600,
		updatedSHA256: tuiBytesSHA256(edited),
	}
	if err := restoreTUIProfileIfUnchanged(directory, path, backup); err == nil {
		t.Fatal("concurrent profile change was overwritten")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, concurrent) {
		t.Fatalf("concurrent profile became %q", data)
	}
}

func TestTUIProfileLockSerializesFrontends(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "config.yaml")
	first, err := acquireTUIProfileLocks(directory, path)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		second, lockErr := acquireTUIProfileLocks(directory, path)
		if lockErr == nil {
			second.release()
		}
		result <- lockErr
	}()
	select {
	case err := <-result:
		first.release()
		t.Fatalf("second frontend bypassed profile lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	first.release()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second frontend did not acquire released profile lock")
	}
}

func TestTUIStateRejectsInvalidSavedProfileAndRecovers(t *testing.T) {
	directory := t.TempDir()
	defaultPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(defaultPath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(directory, tuiStateFilename)
	if err := os.WriteFile(
		statePath,
		[]byte(`{"version":1,"active_profile":"../outside.yaml"}`),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	paths := cliPaths{HomeDir: directory, ConfigPath: defaultPath}
	restored, err := restoreTUIActiveProfile(paths)
	if err == nil {
		t.Fatal("unsafe saved profile was accepted")
	}
	if restored != paths {
		t.Fatalf("invalid state changed paths: %+v", restored)
	}
	if err := rememberTUIActiveProfile(paths); err != nil {
		t.Fatal(err)
	}
	recovered, err := restoreTUIActiveProfile(paths)
	if err != nil || recovered.ConfigPath != defaultPath {
		t.Fatalf("state did not recover: paths=%+v err=%v", recovered, err)
	}
}

func TestTUIStateUpdateDoesNotOverwriteCorruptState(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, tuiStateFilename)
	corrupt := []byte("not-json\n")
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rememberTUIProxySelection(directory, "PROXY", "Tokyo"); err == nil ||
		!strings.Contains(err.Error(), "load shared state") {
		t.Fatalf("corrupt shared state update error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, corrupt) {
		t.Fatalf("corrupt shared state was overwritten: %q", data)
	}
}

func TestTUIStoppedSettingsStayStagedWithoutBackend(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(configPath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	model := newTUIModel(
		controllerClient{},
		cliPaths{HomeDir: directory, ConfigPath: configPath},
		nil,
		true,
	)
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.FocusSidebar = false

	if command := model.handleKey(tuiKeyMode); command != nil {
		t.Fatal("opening the mode list unexpectedly started an asynchronous operation")
	}
	model.selectedMode = findTUIString(tuiTrafficModes, "global")
	if command := model.handleModeSelection(tea.KeyMsg{Type: tea.KeyEnter}); command != nil {
		t.Fatal("stopped mode selection unexpectedly started an asynchronous operation")
	}
	if !model.settingsDirty {
		t.Fatal("uncommitted stopped setting was marked clean")
	}
	if !strings.Contains(model.snapshot.Status, "not saved without Backend") {
		t.Fatalf("Backend boundary missing: %q", model.snapshot.Status)
	}
	reloaded := loadTUIConfiguredSettings(configPath, true)
	if reloaded == nil || reloaded.Mode != "rule" {
		t.Fatalf("frontend changed shared YAML without Backend: %+v", reloaded)
	}
}

func TestMergeTUIRefreshSurfacesControllerErrors(t *testing.T) {
	current := tuiSnapshot{
		Status:    "Settings staged",
		UpdatedAt: time.Now(),
	}
	refreshed := tuiSnapshot{
		Status:    "Controller unavailable: connection refused",
		UpdatedAt: time.Now(),
	}
	merged := mergeTUIRefresh(current, refreshed)
	if merged.Status != refreshed.Status {
		t.Fatalf("controller error was hidden by stale action status: %q", merged.Status)
	}
}
