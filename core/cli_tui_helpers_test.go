//go:build linux && !cgo && cli

package main

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIInitializationDefersProxyListener(t *testing.T) {
	mixedPort := freeTUITestPort(t)
	controllerPort := freeTUITestPort(t)
	directory := t.TempDir()
	paths := cliPaths{
		homeDir:    directory,
		configPath: filepath.Join(directory, "config.yaml"),
	}
	configData := fmt.Appendf(nil, `mixed-port: %d
allow-lan: false
mode: rule
log-level: silent
unified-delay: true
tcp-concurrent: true
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,PROXY
`, mixedPort)
	if err := os.WriteFile(paths.configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}

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
	if canConnectTUITestPort(mixedPort) {
		t.Fatalf("mixed port %d was occupied before the TUI start action", mixedPort)
	}
	if isRunning {
		t.Fatal("core listener state is running before the TUI start action")
	}
	if !currentConfig.General.UnifiedDelay ||
		!currentConfig.General.TCPConcurrent {
		t.Fatalf(
			"FlClash delay defaults were not applied: %+v",
			currentConfig.General,
		)
	}

	model := newTUIModel(client, paths, setupParams, true)
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.FocusSidebar = false
	initialIPv6 := model.snapshot.Settings.IPv6
	_ = model.handleKey(tuiKeyTun)
	if message := stageTUICoreSettings(*model.stagedSettings); message != "" {
		t.Fatalf("stage TUN setting: %s", message)
	}
	if !currentConfig.General.Tun.Enable {
		t.Fatal("TUN setting was not staged in the core")
	}
	if canConnectTUITestPort(mixedPort) {
		t.Fatalf("staging TUN occupied mixed port %d", mixedPort)
	}
	_ = model.handleKey(tuiKeyTun)
	model.snapshot.Page = tuiPageTools
	_ = model.handleKey(tuiKeyAllowLAN)
	_ = model.handleKey(tuiKeyIPv6)
	_ = model.changeMode("global")
	startCommand := model.handleKey(tuiKeyCoreToggle)
	if startCommand == nil {
		t.Fatal("Core start did not return an operation")
	}
	_, _ = model.Update(startCommand())
	if !model.coreRunning {
		t.Fatalf("Core did not start: %s", model.snapshot.Status)
	}
	if model.snapshot.Settings.SystemProxy || model.systemProxyManaged {
		t.Fatalf("Core start changed system proxy: %+v", model.snapshot.Settings)
	}
	if model.pendingMixedPort != nil {
		t.Fatalf("pending mixed port was not applied: %d", *model.pendingMixedPort)
	}
	if model.stagedSettings != nil {
		t.Fatalf("staged settings were not applied: %+v", *model.stagedSettings)
	}
	if !currentConfig.General.AllowLan ||
		currentConfig.General.IPv6 == initialIPv6 ||
		currentConfig.General.Mode.String() != "global" ||
		!currentConfig.General.UnifiedDelay ||
		!currentConfig.General.TCPConcurrent {
		t.Fatalf("staged core settings were not applied: %+v", currentConfig.General)
	}
	deadline := time.Now().Add(time.Second)
	for !canConnectTUITestPort(mixedPort) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !canConnectTUITestPort(mixedPort) {
		t.Fatalf("mixed port %d did not open after automatic service startup", mixedPort)
	}

	stopCommand := model.handleKey(tuiKeyCoreToggle)
	if stopCommand == nil {
		t.Fatal("service stop did not return an operation")
	}
	_, _ = model.Update(stopCommand())
	deadline = time.Now().Add(time.Second)
	for canConnectTUITestPort(mixedPort) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if model.coreRunning || canConnectTUITestPort(mixedPort) {
		t.Fatalf("service did not stop before rollback test: %s", model.snapshot.Status)
	}

	model.snapshot.Page = tuiPageDashboard
	if command := model.handleKey(tuiKeySystemProxy); command != nil {
		t.Fatal("unmanaged TUI scheduled a system proxy mutation")
	}
	if !strings.Contains(model.snapshot.Status, "managed backend") {
		t.Fatalf("system proxy boundary status = %q", model.snapshot.Status)
	}
}

func TestControllerClientUsesUnixSocketWithoutTCP(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "controller.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" {
			http.NotFound(w, request)
			return
		}
		_, _ = io.WriteString(w, `{"hello":"unix"}`)
	})}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
	})

	options := controllerOptions{unixSocket: socketPath}
	client := controllerClient{
		options: options,
		client:  controllerHTTPClientForOptions(options, time.Second),
	}
	data, err := client.request(http.MethodGet, "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != `{"hello":"unix"}` {
		t.Fatalf("Unix controller response = %q", got)
	}
}

func freeTUITestPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func canConnectTUITestPort(port int) bool {
	connection, err := net.DialTimeout(
		"tcp4",
		fmt.Sprintf("127.0.0.1:%d", port),
		50*time.Millisecond,
	)
	if err != nil {
		return false
	}
	_ = connection.Close()
	return true
}

func waitForTUITestPort(port int, open bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if canConnectTUITestPort(port) == open {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return canConnectTUITestPort(port) == open
}

func TestBubbleTeaProcessesBurstNavigationKeys(t *testing.T) {
	model := newTUIModel(
		controllerClient{},
		cliPaths{},
		nil,
		false,
	)
	_, command := model.Update(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune("sssss"),
	})
	if command != nil {
		t.Fatal("navigation burst unexpectedly returned a command")
	}
	if model.snapshot.SelectedMenu != int(tuiPageConnections) {
		t.Fatalf("selected menu = %d, want connections", model.snapshot.SelectedMenu)
	}
}

func assertTUIUsesCRLF(t *testing.T, output string) {
	t.Helper()
	for index, value := range []byte(output) {
		if value == '\n' && (index == 0 || output[index-1] != '\r') {
			t.Fatalf("TUI frame contains a bare LF at byte %d", index)
		}
	}
}

func populatedTUISnapshot(page tuiPage) tuiSnapshot {
	snapshot := tuiSnapshot{
		Page:      page,
		UpdatedAt: time.Now(),
		Settings: tuiSettings{
			Mode:      "rule",
			MixedPort: 7890,
			LogLevel:  "info",
		},
		Status: "Connected",
	}
	for index := 0; index < 24; index++ {
		nodes := make([]string, 24)
		for nodeIndex := range nodes {
			nodes[nodeIndex] = fmt.Sprintf("node-%02d-%02d", index, nodeIndex)
		}
		snapshot.Groups = append(snapshot.Groups, tuiGroup{
			Name:  fmt.Sprintf("group-%02d", index),
			Type:  "Selector",
			Now:   nodes[18],
			Nodes: nodes,
		})
		snapshot.Connections = append(snapshot.Connections, tuiConnection{
			ID:       fmt.Sprintf("connection-%02d", index),
			Host:     fmt.Sprintf("service-%02d.example.com", index),
			Process:  "example-process",
			Network:  "tcp",
			Chain:    "PROXY",
			Upload:   int64(index * 1024),
			Download: int64(index * 2048),
		})
		snapshot.Requests = append(snapshot.Requests, tuiRequest{
			tuiConnection: tuiConnection{
				ID:       fmt.Sprintf("request-%02d", index),
				Host:     fmt.Sprintf("request-%02d.example.com", index),
				Process:  "example-process",
				Network:  "tcp",
				Chain:    "PROXY",
				Upload:   int64(index * 1024),
				Download: int64(index * 2048),
			},
			LastSeen: time.Now().Add(-time.Duration(index) * time.Second),
			Active:   index%2 == 0,
		})
		snapshot.Profiles = append(snapshot.Profiles, tuiProfile{
			Name: fmt.Sprintf("profile-%02d.yaml", index),
			Path: fmt.Sprintf("/tmp/profile-%02d.yaml", index),
		})
		snapshot.Providers = append(snapshot.Providers, tuiProvider{
			Name:  fmt.Sprintf("provider-%02d", index),
			Type:  "HTTP",
			Count: index,
		})
		snapshot.Logs = append(snapshot.Logs, fmt.Sprintf("log line %02d", index))
	}
	snapshot.SelectedGroup = 18
	snapshot.SelectedNode = 18
	snapshot.SelectedConnection = 18
	snapshot.SelectedRow = 18
	snapshot.SelectedProvider = 18
	return snapshot
}

func stripTUIANSI(value string) string {
	var output strings.Builder
	for index := 0; index < len(value); {
		if value[index] != '\x1b' {
			output.WriteByte(value[index])
			index++
			continue
		}
		index++
		if index < len(value) && value[index] == '[' {
			index++
			for index < len(value) {
				code := value[index]
				index++
				if code >= '@' && code <= '~' {
					break
				}
			}
		}
	}
	return valueWithoutNewline(output.String())
}

func valueWithoutNewline(value string) string {
	return strings.TrimRight(value, "\r\n")
}

func TestTUIGroupAndSelectionMovement(t *testing.T) {
	if !isTUIGroup("Selector") || !isTUIGroup("urltest") || isTUIGroup("Direct") {
		t.Fatal("unexpected proxy group classification")
	}
	snapshot := tuiSnapshot{Groups: []tuiGroup{
		{Name: "A", Nodes: []string{"a", "b"}},
		{Name: "B", Now: "d", Nodes: []string{"c", "d"}},
	}}
	moveTUIGroup(&snapshot, -1)
	if snapshot.SelectedGroup != 1 || snapshot.SelectedNode != 1 {
		t.Fatalf("group movement = (%d, %d)", snapshot.SelectedGroup, snapshot.SelectedNode)
	}
	moveTUINode(&snapshot, 1)
	if snapshot.SelectedNode != 0 {
		t.Fatalf("node movement = %d", snapshot.SelectedNode)
	}
	snapshot.SelectedGroup = 99
	moveTUINode(&snapshot, 1)
	snapshot.SelectedGroup = -99
	moveTUIGroup(&snapshot, -1)
	if snapshot.SelectedGroup != 0 {
		t.Fatalf("out-of-range group movement = %d", snapshot.SelectedGroup)
	}
}

func TestTUIProxyGroupsFollowConfigurationOrder(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	configData := []byte(`proxy-groups:
  - name: Z-LAST-ALPHABETICALLY
    type: select
    proxies: [DIRECT]
  - name: A-FIRST-ALPHABETICALLY
    type: select
    proxies: [DIRECT]
  - name: M-MIDDLE
    type: select
    proxies: [DIRECT]
rules:
  - MATCH,DIRECT
`)
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatal(err)
	}
	order := loadTUIProxyGroupOrder(configPath)
	groups := []tuiGroup{
		{Name: "A-FIRST-ALPHABETICALLY"},
		{Name: "M-MIDDLE"},
		{Name: "Z-LAST-ALPHABETICALLY"},
	}
	orderTUIGroups(groups, order)
	got := []string{groups[0].Name, groups[1].Name, groups[2].Name}
	want := []string{
		"Z-LAST-ALPHABETICALLY",
		"A-FIRST-ALPHABETICALLY",
		"M-MIDDLE",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("proxy group order = %v, want configuration order %v", got, want)
	}
}

func TestTUIVisibleRangeKeepsSelectionOnScreen(t *testing.T) {
	tests := []struct {
		total    int
		selected int
		limit    int
		start    int
		end      int
	}{
		{total: 100, selected: 0, limit: 10, start: 0, end: 10},
		{total: 100, selected: 50, limit: 10, start: 45, end: 55},
		{total: 100, selected: 99, limit: 10, start: 90, end: 100},
		{total: 4, selected: 3, limit: 10, start: 0, end: 4},
	}
	for _, test := range tests {
		start, end := tuiVisibleRange(test.total, test.selected, test.limit)
		if start != test.start || end != test.end {
			t.Fatalf("tuiVisibleRange(%d, %d, %d) = (%d, %d), want (%d, %d)",
				test.total, test.selected, test.limit, start, end, test.start, test.end)
		}
	}
}

func TestWrapTUIIndexNormalizesStaleSelections(t *testing.T) {
	tests := []struct {
		current int
		delta   int
		total   int
		want    int
	}{
		{current: 99, delta: 1, total: 2, want: 0},
		{current: -99, delta: -1, total: 2, want: 0},
		{current: 0, delta: -1, total: 4, want: 3},
		{current: 3, delta: 1, total: 4, want: 0},
	}
	for _, test := range tests {
		if got := wrapTUIIndex(test.current, test.delta, test.total); got != test.want {
			t.Fatalf("wrapTUIIndex(%d, %d, %d) = %d, want %d",
				test.current, test.delta, test.total, got, test.want)
		}
	}
}

func TestReadTUIKeys(t *testing.T) {
	keys := make(chan tuiKey, 8)
	go readTUIKeys(bytes.NewBufferString("rsp\t\x0e\x1b[Z\x1b[1;5Aq"), keys)
	want := []tuiKey{
		tuiKeyRefresh,
		tuiKeyDown,
		tuiKeySetPort,
		tuiKeyFocusNext,
		tuiKeyNotifications,
		tuiKeyFocusPrevious,
		tuiKeyUp,
		tuiKeyQuit,
	}
	for _, expected := range want {
		if got := <-keys; got != expected {
			t.Fatalf("key = %v, want %v", got, expected)
		}
	}
	if _, open := <-keys; open {
		t.Fatal("key channel was not closed after input ended")
	}
}

func TestTUIFocusNavigationMakesSidebarOperable(t *testing.T) {
	snapshot := tuiSnapshot{
		Page:         tuiPageDashboard,
		SelectedMenu: int(tuiPageDashboard),
		FocusSidebar: true,
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeyDown) {
		t.Fatal("sidebar down key was not handled")
	}
	if snapshot.SelectedMenu != int(tuiPageSSH) || snapshot.Page != tuiPageDashboard {
		t.Fatalf("sidebar movement changed wrong state: %+v", snapshot)
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeySelect) {
		t.Fatal("sidebar Enter was not handled")
	}
	if snapshot.Page != tuiPageSSH || snapshot.FocusSidebar {
		t.Fatalf("sidebar Enter did not open content: %+v", snapshot)
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeyLeft) || !snapshot.FocusSidebar {
		t.Fatalf("left did not return to sidebar from SSH: %+v", snapshot)
	}
	if snapshot.SelectedMenu != int(tuiPageSSH) {
		t.Fatalf("sidebar cursor did not follow active page: %+v", snapshot)
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeySettings) {
		t.Fatal("numeric page shortcut was not handled")
	}
	if snapshot.Page != tuiPageTools || snapshot.FocusSidebar {
		t.Fatalf("numeric page shortcut did not open content: %+v", snapshot)
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeyLeft) || !snapshot.FocusSidebar {
		t.Fatalf("left did not return to sidebar: %+v", snapshot)
	}
}

func TestTUIFocusNavigationCyclesThroughSSHProfilesAndDashboard(t *testing.T) {
	snapshot := tuiSnapshot{
		Page:         tuiPageSSH,
		SelectedMenu: int(tuiPageSSH),
		FocusSidebar: true,
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeyFocusNext) ||
		snapshot.FocusSidebar || snapshot.SSHDashboardFocus {
		t.Fatalf("Tab sidebar -> profiles = %+v", snapshot)
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeyFocusNext) ||
		!snapshot.SSHDashboardFocus {
		t.Fatalf("Tab profiles -> Dashboard = %+v", snapshot)
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeyFocusNext) ||
		!snapshot.FocusSidebar || snapshot.SSHDashboardFocus {
		t.Fatalf("Tab Dashboard -> sidebar = %+v", snapshot)
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeyFocusPrevious) ||
		snapshot.FocusSidebar || !snapshot.SSHDashboardFocus {
		t.Fatalf("Shift+Tab sidebar -> Dashboard = %+v", snapshot)
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeyFocusPrevious) ||
		snapshot.FocusSidebar || snapshot.SSHDashboardFocus {
		t.Fatalf("Shift+Tab Dashboard -> profiles = %+v", snapshot)
	}
	if !handleTUIFocusNavigation(&snapshot, tuiKeyFocusPrevious) ||
		!snapshot.FocusSidebar {
		t.Fatalf("Shift+Tab profiles -> sidebar = %+v", snapshot)
	}
}

func TestPreserveTUIInteractionKeepsSSHSelectionDashboardAndMetrics(t *testing.T) {
	current := tuiSnapshot{
		SSHDashboardFocus: true,
		SelectedSSH:       1,
		SelectedSSHDetail: 3,
		SSHNetwork:        tuiNetworkInfo{PublicIP: "203.0.113.8"},
		SSHTrafficHistory: []trafficSnapshot{{Up: 10, Down: 20}},
		SSHProfiles: []tuiSSHProfile{
			{Name: "first"},
			{Name: "second"},
		},
	}
	updated := tuiSnapshot{
		SSHProfiles: []tuiSSHProfile{
			{Name: "second"},
			{Name: "first"},
		},
	}
	merged := preserveTUIInteraction(current, updated)
	if merged.SelectedSSH != 0 ||
		!merged.SSHDashboardFocus ||
		merged.SelectedSSHDetail != 3 ||
		merged.SSHNetwork.PublicIP != "203.0.113.8" ||
		len(merged.SSHTrafficHistory) != 1 {
		t.Fatalf("SSH interaction was not preserved: %+v", merged)
	}
}

func TestTUIProxyGroupsAndNodesUseArrowOrWSNavigation(t *testing.T) {
	left, ok := tuiKeyFromTea(tea.KeyMsg{Type: tea.KeyLeft})
	if !ok || left != tuiKeyLeft {
		t.Fatalf("left arrow = (%v, %v)", left, ok)
	}
	right, ok := tuiKeyFromTea(tea.KeyMsg{Type: tea.KeyRight})
	if !ok || right != tuiKeyRight {
		t.Fatalf("right arrow = (%v, %v)", right, ok)
	}
	up, ok := tuiKeyFromTea(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'w'}})
	if !ok || up != tuiKeyUp {
		t.Fatalf("w = (%v, %v)", up, ok)
	}
	down, ok := tuiKeyFromTea(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if !ok || down != tuiKeyDown {
		t.Fatalf("s = (%v, %v)", down, ok)
	}
	for _, removed := range []rune{'j', 'k', 'h', 'l'} {
		if key, mapped := tuiKeyFromTea(tea.KeyMsg{
			Type:  tea.KeyRunes,
			Runes: []rune{removed},
		}); mapped {
			t.Fatalf("removed key %q still maps to %v", removed, key)
		}
	}

	model := newTUIModel(controllerClient{}, cliPaths{}, nil, false)
	model.snapshot.Page = tuiPageProxies
	model.snapshot.SelectedMenu = int(tuiPageProxies)
	model.snapshot.FocusSidebar = false
	model.snapshot.Groups = []tuiGroup{
		{Name: "FIRST", Now: "A", Nodes: []string{"A", "B"}},
		{Name: "SECOND", Now: "C", Nodes: []string{"C", "D"}},
	}
	model.snapshot.SelectedNode = 0
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if !model.snapshot.FocusSidebar || model.snapshot.SelectedNode != 0 {
		t.Fatalf("left arrow changed proxy state instead of focus: %+v", model.snapshot)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRight})
	if model.snapshot.FocusSidebar {
		t.Fatalf("right arrow did not focus content: %+v", model.snapshot)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if model.snapshot.SelectedGroup != 1 || model.snapshot.ProxyNodeFocus {
		t.Fatalf("s did not move the proxy group: %+v", model.snapshot)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !model.snapshot.ProxyNodeFocus {
		t.Fatalf("Enter did not open node selection: %+v", model.snapshot)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if model.snapshot.SelectedNode != 1 {
		t.Fatalf("s did not move the selected node: %+v", model.snapshot)
	}
	_, _ = model.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if model.snapshot.ProxyNodeFocus {
		t.Fatalf("Esc did not return to proxy groups: %+v", model.snapshot)
	}
}

func TestReadTUIKeysWaitsUntilPreviousKeyIsHandled(t *testing.T) {
	keys := make(chan tuiKey)
	handled := make(chan struct{})
	go readTUIKeysSynchronized(bytes.NewBufferString("1234"), keys, handled)

	want := []tuiKey{
		tuiKeyDashboard,
		tuiKeySSH,
		tuiKeyProxies,
		tuiKeyProfiles,
	}
	for index, expected := range want {
		if got := <-keys; got != expected {
			t.Fatalf("key %d = %v, want %v", index+1, got, expected)
		}
		if index == 0 {
			select {
			case unexpected := <-keys:
				t.Fatalf("read next key before acknowledgement: %v", unexpected)
			case <-time.After(20 * time.Millisecond):
			}
		}
		handled <- struct{}{}
	}
	if _, open := <-keys; open {
		t.Fatal("key channel was not closed after synchronized input ended")
	}
}

func TestReadTUILineDoesNotConsumeFollowingKeys(t *testing.T) {
	input := bytes.NewBufferString("17892\n?q")
	line, err := readTUILine(input)
	if err != nil {
		t.Fatal(err)
	}
	if line != "17892" {
		t.Fatalf("line = %q, want 17892", line)
	}
	remaining, err := io.ReadAll(input)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(remaining); got != "?q" {
		t.Fatalf("remaining input = %q, want ?q", got)
	}
}

func TestSetProxyDoesNotWriteToTerminal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut || request.URL.Path != "/proxies/PROXY" {
			t.Fatalf("unexpected request: %s %s", request.Method, request.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := controllerClient{
		options: controllerOptions{address: server.URL},
		client:  server.Client(),
	}
	readPipe, writePipe, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	originalStdout := os.Stdout
	os.Stdout = writePipe
	defer func() {
		os.Stdout = originalStdout
	}()

	if err := client.setProxy("PROXY", "DIRECT"); err != nil {
		t.Fatal(err)
	}
	if err := writePipe.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(readPipe)
	if err != nil {
		t.Fatal(err)
	}
	if len(output) != 0 {
		t.Fatalf("setProxy wrote to terminal: %q", output)
	}
}

func TestRefreshTUISnapshotPreservesSelectionsAndActionStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/proxies":
			_, _ = io.WriteString(w, `{"proxies":{
				"A":{"type":"Selector","now":"A1","all":["A1"]},
				"B":{"type":"Selector","now":"B2","all":["B1","B2"]}
			}}`)
		case "/traffic":
			_, _ = io.WriteString(w, `{"up":1,"down":2,"upTotal":3,"downTotal":4}`+"\n")
		case "/connections":
			_, _ = io.WriteString(w, `{"connections":[
				{"id":"id-1","metadata":{"host":"one","network":"tcp"},"chains":["DIRECT"]},
				{"id":"id-2","metadata":{"destinationIP":"2001:db8::2","destinationPort":"443","network":"tcp"},"chains":["PROXY"]}
			]}`)
		case "/configs":
			_, _ = io.WriteString(w, `{"mode":"rule","mixed-port":17890,"log-level":"info"}`)
		case "/providers/proxies":
			_, _ = io.WriteString(w, `{"providers":{
				"p1":{"name":"p1","type":"Proxy","proxies":[]},
				"p2":{"name":"p2","type":"Proxy","proxies":[]}
			}}`)
		default:
			http.NotFound(w, request)
		}
	}))
	defer server.Close()

	snapshot := tuiSnapshot{
		Groups: []tuiGroup{
			{Name: "B", Now: "B2", Nodes: []string{"B1", "B2"}},
		},
		Connections:        []tuiConnection{{ID: "id-2"}},
		Providers:          []tuiProvider{{Name: "p2"}},
		SelectedNode:       0,
		SelectedConnection: 0,
		SelectedProvider:   0,
		Status:             "Switched B to B1",
		UpdatedAt:          time.Now(),
	}
	client := controllerClient{
		options: controllerOptions{address: server.URL},
		client:  server.Client(),
	}
	refreshTUISnapshot(&snapshot, client)

	if snapshot.Groups[snapshot.SelectedGroup].Name != "B" ||
		snapshot.Groups[snapshot.SelectedGroup].Nodes[snapshot.SelectedNode] != "B1" {
		t.Fatalf("proxy selection was not preserved: group=%d node=%d", snapshot.SelectedGroup, snapshot.SelectedNode)
	}
	if snapshot.Connections[snapshot.SelectedConnection].ID != "id-2" {
		t.Fatalf("connection selection = %q", snapshot.Connections[snapshot.SelectedConnection].ID)
	}
	if snapshot.Connections[snapshot.SelectedConnection].Host != "[2001:db8::2]:443" {
		t.Fatalf(
			"connection destination = %q",
			snapshot.Connections[snapshot.SelectedConnection].Host,
		)
	}
	if snapshot.Providers[snapshot.SelectedProvider].Name != "p2" {
		t.Fatalf("provider selection = %q", snapshot.Providers[snapshot.SelectedProvider].Name)
	}
	if snapshot.Status != "Switched B to B1" {
		t.Fatalf("action status was overwritten: %q", snapshot.Status)
	}
	if snapshot.Settings.MixedPort != 17890 {
		t.Fatalf("snapshot values were not refreshed: %+v", snapshot)
	}
}

func TestResolvePathsUsesDirectoryForDefaultConfig(t *testing.T) {
	directory := t.TempDir()
	paths, err := resolvePaths("", directory)
	if err != nil {
		t.Fatal(err)
	}
	wantHome, _ := filepath.Abs(directory)
	wantConfig := filepath.Join(wantHome, "config.yaml")
	if paths.homeDir != wantHome || paths.configPath != wantConfig {
		t.Fatalf("paths = (%q, %q), want (%q, %q)", paths.homeDir, paths.configPath, wantHome, wantConfig)
	}
}

func TestResolvePathsUsesRelativeDirectoryOnce(t *testing.T) {
	directory := filepath.Join("test-data", "instance")
	paths, err := resolvePaths("", directory)
	if err != nil {
		t.Fatal(err)
	}
	wantHome, _ := filepath.Abs(directory)
	wantConfig := filepath.Join(wantHome, "config.yaml")
	if paths.homeDir != wantHome || paths.configPath != wantConfig {
		t.Fatalf("paths = (%q, %q), want (%q, %q)", paths.homeDir, paths.configPath, wantHome, wantConfig)
	}
}

func TestResolvePathsDefaultConfigUsesUserConfigDirectory(t *testing.T) {
	configRoot, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := resolvePaths("", "")
	if err != nil {
		t.Fatal(err)
	}
	wantHome, _ := filepath.Abs(filepath.Join(configRoot, "flclash"))
	wantConfig := filepath.Join(wantHome, "config.yaml")
	if paths.homeDir != wantHome || paths.configPath != wantConfig {
		t.Fatalf("paths = (%q, %q), want (%q, %q)", paths.homeDir, paths.configPath, wantHome, wantConfig)
	}
}

func TestEnsureTUIConfigCreatesMinimalConfig(t *testing.T) {
	directory := t.TempDir()
	paths := cliPaths{homeDir: directory, configPath: filepath.Join(directory, "config.yaml")}
	if err := ensureTUIConfig(paths, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if message := validateConfigBytes(data); message != "" {
		t.Fatalf("generated config is invalid: %s", message)
	}
	for _, expected := range []string{
		"ipv6: false",
		"unified-delay: true",
		"tcp-concurrent: true",
		"geodata-loader: memconservative",
		"geodata-mode: false",
	} {
		if !strings.Contains(string(data), expected) {
			t.Fatalf("generated config does not contain %q:\n%s", expected, data)
		}
	}
	if err := ensureTUIConfig(paths, true); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(paths.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(data) {
		t.Fatal("existing config was overwritten")
	}
}

func TestEnsureTUIFlClashDefaultsMigratesOnlyMissingSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	configData := `mixed-port: 7891
mode: rule
ipv6: true
unified-delay: false
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,PROXY
`
	if err := os.WriteFile(path, []byte(configData), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureTUIFlClashDefaults(path); err != nil {
		t.Fatal(err)
	}
	settings := loadTUIConfiguredSettings(path, true)
	if settings == nil {
		t.Fatal("migrated settings could not be loaded")
	}
	if !settings.IPv6 {
		t.Fatal("explicit IPv6 setting was overwritten")
	}
	if settings.UnifiedDelay {
		t.Fatal("explicit unified-delay setting was overwritten")
	}
	if !settings.TCPConcurrent {
		t.Fatal("missing tcp-concurrent did not receive FlClash default")
	}
}

func TestRestoreLatestTUIConfigDoesNotOverwriteWithInvalidBackup(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	original := []byte("mixed-port: 17890\n")
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	backupPath := configPath + ".backup-999999999999999999"
	if err := os.WriteFile(backupPath, []byte("mixed-port: [invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := restoreLatestTUIConfig(configPath); err == nil {
		t.Fatal("restore unexpectedly accepted invalid backup")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Fatalf("config changed after invalid restore: %q", data)
	}
}

func TestReadTUILocalProfileAcceptsNonYAMLExtension(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "nodes.txt")
	data := "hysteria2://secret@example.com:443/?sni=example.com#HY2\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, name, err := readTUILocalProfileDetails(path)
	if err != nil {
		t.Fatal(err)
	}
	if name != "nodes.yaml" || payload.Nodes != 1 {
		t.Fatalf("local import = %q %+v", name, payload)
	}
}
