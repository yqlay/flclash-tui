//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func TestWriteTUISilentRuntimeConfigIsolatesInboundSurfaces(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	source := []byte(`port: 7892
socks-port: 7893
redir-port: 7894
tproxy-port: 7895
mixed-port: 7891
allow-lan: true
bind-address: '*'
mode: global
ss-config: 0.0.0.0:8388
vmess-config: 0.0.0.0:8389
external-controller: 0.0.0.0:9090
external-controller-tls: 0.0.0.0:9443
external-controller-pipe: user-pipe
external-controller-unix: /tmp/user-controller.sock
external-ui: ./ui
external-ui-url: https://example.invalid/ui.zip
external-doh-server: 0.0.0.0:8053
geo-auto-update: true
dns:
  enable: true
  listen: 0.0.0.0:1053
tun:
  enable: true
ntp:
  enable: true
iptables:
  enable: true
tuic-server:
  enable: true
listeners:
  - name: public-listener
    type: mixed
    listen: 0.0.0.0
    port: 9000
tunnels:
  - network: [tcp]
    address: 0.0.0.0:9001
    target: example.com:443
proxy-groups:
  - name: PROXY
    type: select
    proxies: [DIRECT]
rules:
  - MATCH,PROXY
`)
	if err := os.WriteFile(configPath, source, 0o640); err != nil {
		t.Fatal(err)
	}
	state := tuiFLCListenerState{
		Outbound: "PROXY",
		Port:     17891,
		Username: "flc",
		Password: "0123456789abcdef0123456789abcdef",
	}
	runtimePath, err := writeTUISilentRuntimeConfig(
		cliPaths{homeDir: directory, configPath: configPath},
		state,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupTUISilentRuntimeConfigs(directory, "") })

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(source) {
		t.Fatal("silent runtime generation modified the shared profile")
	}
	if !strings.HasPrefix(filepath.Base(runtimePath), tuiSilentRuntimeConfigPrefix) {
		t.Fatalf("runtime path = %q", runtimePath)
	}
	if strings.Contains(filepath.Base(runtimePath), state.Password[:12]) {
		t.Fatal("runtime path leaks a credential prefix")
	}
	info, err := os.Stat(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("runtime mode = %o, want 600", info.Mode().Perm())
	}

	data, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"port", "socks-port", "redir-port", "tproxy-port", "mixed-port",
	} {
		if value, ok := config[key].(int); !ok || value != 0 {
			t.Fatalf("%s = %#v, want 0", key, config[key])
		}
	}
	if config["allow-lan"] != false || config["bind-address"] != "127.0.0.1" {
		t.Fatalf("LAN isolation = allow %#v bind %#v", config["allow-lan"], config["bind-address"])
	}
	if config["mode"] != "rule" {
		t.Fatalf("native runtime mode = %#v, want rule", config["mode"])
	}
	for _, key := range []string{
		"ss-config",
		"vmess-config",
		"external-controller",
		"external-controller-tls",
		"external-controller-pipe",
		"external-controller-unix",
		"external-ui",
		"external-ui-url",
		"external-doh-server",
	} {
		if config[key] != "" {
			t.Fatalf("%s = %#v, want empty", key, config[key])
		}
	}
	for _, key := range []string{"tun", "ntp", "iptables", "tuic-server"} {
		mapping, ok := config[key].(map[string]any)
		if !ok || mapping["enable"] != false {
			t.Fatalf("%s = %#v, want enable false", key, config[key])
		}
	}
	if config["geo-auto-update"] != false {
		t.Fatalf("geo-auto-update = %#v, want false", config["geo-auto-update"])
	}
	dns, ok := config["dns"].(map[string]any)
	if !ok || dns["listen"] != "" {
		t.Fatalf("dns = %#v, want an empty listen address", config["dns"])
	}
	if tunnels, ok := config["tunnels"].([]any); !ok || len(tunnels) != 0 {
		t.Fatalf("tunnels = %#v, want an empty sequence", config["tunnels"])
	}
	listeners, ok := config["listeners"].([]any)
	if !ok || len(listeners) != 1 {
		t.Fatalf("listeners = %#v, want one private listener", config["listeners"])
	}
	listener, ok := listeners[0].(map[string]any)
	if !ok {
		t.Fatalf("listener = %#v", listeners[0])
	}
	for key, want := range map[string]any{
		"name":   tuiFLCListenerName,
		"type":   "mixed",
		"listen": "127.0.0.1",
		"port":   state.Port,
		"proxy":  state.Outbound,
	} {
		if listener[key] != want {
			t.Fatalf("listener %s = %#v, want %#v", key, listener[key], want)
		}
	}
	users, ok := listener["users"].([]any)
	if !ok || len(users) != 1 {
		t.Fatalf("listener users = %#v", listener["users"])
	}
	user, ok := users[0].(map[string]any)
	if !ok || user["username"] != state.Username || user["password"] != state.Password {
		t.Fatalf("listener user = %#v", users[0])
	}
}

func TestTUITrafficModeDefaultsToSilentUntilExplicitlyChanged(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(
		configPath,
		[]byte("mixed-port: 7891\nmode: global\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if mode := loadTUITrafficMode(directory, configPath); mode != tuiSilentMode {
		t.Fatalf("default traffic mode = %q, want silent", mode)
	}
	if err := rememberTUITrafficMode(directory, "global"); err != nil {
		t.Fatal(err)
	}
	if mode := loadTUITrafficMode(directory, configPath); mode != "global" {
		t.Fatalf("saved traffic mode = %q, want global", mode)
	}
}

func TestDefaultSilentBackendWaitsForFLCOutbound(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "flclash-default-silent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	previousRuntimeDirectory := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = directory
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previousRuntimeDirectory })

	proxyPort := freeTUITestPort(t)
	configPath := filepath.Join(directory, "config.yaml")
	source := fmt.Appendf(nil, `mixed-port: %d
mode: rule
log-level: silent
proxy-groups:
  - name: PROXY
    type: select
    proxies: [DIRECT]
rules:
  - MATCH,PROXY
`, proxyPort)
	if err := os.WriteFile(configPath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	serviceDone := make(chan error, 1)
	go func() {
		serviceDone <- runTUIService(
			cliPaths{homeDir: directory, configPath: configPath},
			defaultCLITestURL,
			nil,
			false,
		)
	}()
	service := newTUIServiceClient(directory)
	shutdown := true
	t.Cleanup(func() {
		if shutdown {
			_ = service.shutdown()
		}
	})
	status := waitForTUIServiceStatus(t, service)
	baseline, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if status.Mode != tuiSilentMode || status.Running || status.FLCEnabled ||
		status.SystemProxy {
		t.Fatalf("default silent status = %+v", status)
	}
	if waitForTUIProxyPortState(proxyPort, true, 150*time.Millisecond) {
		t.Fatal("default silent mode opened the normal proxy port")
	}
	failedStatus, err := service.startAtRevision(status.Revision)
	if err == nil || !strings.Contains(err.Error(), "requires an FLC outbound") {
		t.Fatalf("start without FLC outbound = %+v, %v", failedStatus, err)
	}
	if failedStatus.Running {
		t.Fatal("failed default-silent start marked Core running")
	}

	status, err = service.setFLCOutbound("PROXY", status.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if status.Running || status.FLCEnabled || status.FLCOutbound != "PROXY" {
		t.Fatalf("stopped FLC selection status = %+v", status)
	}
	status, err = service.startAtRevision(status.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !status.Running || !status.FLCEnabled || status.SystemProxy {
		t.Fatalf("started default silent status = %+v", status)
	}
	if waitForTUIProxyPortState(proxyPort, true, 150*time.Millisecond) {
		t.Fatal("normal proxy port opened after starting default silent mode")
	}
	privateStatus, err := service.flcProxy()
	if err != nil {
		t.Fatal(err)
	}
	privateURL, err := url.Parse(privateStatus.FLCProxyURL)
	if err != nil || privateURL.User == nil || privateURL.Hostname() != "127.0.0.1" {
		t.Fatalf("private FLC URL = %q, %v", privateStatus.FLCProxyURL, err)
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(baseline) {
		t.Fatal("default silent mode modified the shared profile")
	}
	if err := service.shutdown(); err != nil {
		t.Fatal(err)
	}
	shutdown = false
	select {
	case err := <-serviceDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Backend did not exit after shutdown ACK")
	}
}

func TestNewTUIFLCListenerStateCreatesAuthenticatedLoopbackURL(t *testing.T) {
	state, err := newTUIFLCListenerState("PROXY")
	if err != nil {
		t.Fatal(err)
	}
	if state.Port <= 0 || state.Username != "flc" || len(state.Password) != 48 {
		t.Fatalf("FLC listener state = %+v", state)
	}
	proxyURL, err := url.Parse(state.proxyURL())
	if err != nil {
		t.Fatal(err)
	}
	password, hasPassword := proxyURL.User.Password()
	if proxyURL.Scheme != "http" || proxyURL.Hostname() != "127.0.0.1" ||
		proxyURL.User.Username() != state.Username || !hasPassword || password != state.Password {
		t.Fatalf("private proxy URL = %q", proxyURL.String())
	}
}

func TestWriteTUISilentRuntimeConfigRejectsIncompleteCredentials(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(configPath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := writeTUISilentRuntimeConfig(
		cliPaths{homeDir: directory, configPath: configPath},
		tuiFLCListenerState{Outbound: "PROXY", Port: 17891},
	)
	if err == nil {
		t.Fatal("incomplete private-listener credentials were accepted")
	}
}

func TestWriteTUISilentRuntimeConfigWithoutOutboundHasNoInbound(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	if err := os.WriteFile(configPath, []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	runtimePath, err := writeTUISilentRuntimeConfig(
		cliPaths{homeDir: directory, configPath: configPath},
		tuiFLCListenerState{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupTUISilentRuntimeConfigs(directory, "") })
	data, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	listeners, ok := config["listeners"].([]any)
	if !ok || len(listeners) != 0 {
		t.Fatalf("silent listeners without outbound = %#v, want empty", config["listeners"])
	}
	if mixedPort, ok := config["mixed-port"].(int); !ok || mixedPort != 0 {
		t.Fatalf("silent mixed-port without outbound = %#v, want 0", config["mixed-port"])
	}
	if tun, ok := config["tun"].(map[string]any); !ok || tun["enable"] != false {
		t.Fatalf("silent TUN without outbound = %#v, want disabled", config["tun"])
	}
}

func TestWriteTUIManagedRuntimeConfigScopesUserTun(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	source := []byte(`mixed-port: 7891
allow-lan: true
mode: global
listeners:
  - name: public
    type: mixed
    listen: 0.0.0.0
    port: 9999
tun:
  enable: false
proxy-groups:
  - name: PROXY
    type: select
    proxies: [DIRECT]
rules:
  - MATCH,PROXY
`)
	if err := os.WriteFile(configPath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	runtimePath, err := writeTUIManagedRuntimeConfig(
		cliPaths{homeDir: directory, configPath: configPath},
		"rule",
		17891,
		true,
		tuiTunScopeUser,
		123,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupTUISilentRuntimeConfigs(directory, "") })
	if current, err := os.ReadFile(configPath); err != nil || string(current) != string(source) {
		t.Fatalf("logical profile changed: %v", err)
	}
	data, err := os.ReadFile(runtimePath)
	if err != nil {
		t.Fatal(err)
	}
	var config map[string]any
	if err := yaml.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	if config["mixed-port"] != 17891 || config["allow-lan"] != false || config["find-process-mode"] != "always" {
		t.Fatalf("managed listener policy = %#v", config)
	}
	rules, ok := config["rules"].([]any)
	if !ok || len(rules) < 1 || rules[0] != fmt.Sprintf(
		"AND,((IN-NAME,DEFAULT-MIXED),(NOT,((UID,%d)))),REJECT",
		os.Getuid(),
	) {
		t.Fatalf("managed UID guard rule = %#v", config["rules"])
	}
	if listeners, ok := config["listeners"].([]any); !ok || len(listeners) != 0 {
		t.Fatalf("managed listeners = %#v", config["listeners"])
	}
	tun, ok := config["tun"].(map[string]any)
	if !ok || tun["enable"] != true || tun["file-descriptor"] != 123 || tun["auto-route"] != false {
		t.Fatalf("managed TUN = %#v", config["tun"])
	}
	uidList, ok := tun["include-uid"].([]any)
	if !ok || len(uidList) != 1 || uidList[0] != os.Getuid() {
		t.Fatalf("managed TUN UID = %#v", tun["include-uid"])
	}
	directPath, err := writeTUIManagedRuntimeConfig(
		cliPaths{homeDir: directory, configPath: configPath},
		"direct",
		17892,
		false,
		tuiTunScopeUser,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	directData, err := os.ReadFile(directPath)
	if err != nil {
		t.Fatal(err)
	}
	var directConfig map[string]any
	if err := yaml.Unmarshal(directData, &directConfig); err != nil {
		t.Fatal(err)
	}
	directRules, ok := directConfig["rules"].([]any)
	if directConfig["mode"] != "rule" || !ok || len(directRules) != 2 || directRules[1] != "MATCH,DIRECT" {
		t.Fatalf("managed direct mode = %#v", directConfig)
	}
}

func TestEnteringSilentWithoutOutboundClosesNormalListener(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "flclash-enter-silent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	previousRuntimeDirectory := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = directory
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previousRuntimeDirectory })

	proxyPort := freeTUITestPort(t)
	configPath := filepath.Join(directory, "config.yaml")
	source := fmt.Appendf(nil, `mixed-port: %d
mode: rule
log-level: silent
proxy-groups:
  - name: PROXY
    type: select
    proxies: [DIRECT]
rules:
  - MATCH,PROXY
`, proxyPort)
	if err := os.WriteFile(configPath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rememberTUITrafficMode(directory, "rule"); err != nil {
		t.Fatal(err)
	}
	serviceDone := make(chan error, 1)
	go func() {
		serviceDone <- runTUIService(
			cliPaths{homeDir: directory, configPath: configPath},
			defaultCLITestURL,
			nil,
			false,
		)
	}()
	service := newTUIServiceClient(directory)
	shutdown := true
	t.Cleanup(func() {
		if shutdown {
			_ = service.shutdown()
		}
	})
	status := waitForTUIServiceStatus(t, service)
	baseline, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	status, err = service.startAtRevision(status.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !waitForTUIProxyPortState(proxyPort, true, tuiListenerValidationTimeout) {
		t.Fatal("normal proxy listener did not start")
	}
	status, err = service.setMode(tuiSilentMode, status.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if status.Mode != tuiSilentMode || !status.Running || status.FLCEnabled ||
		status.SystemProxy || status.FLCOutbound != "" {
		t.Fatalf("silent-without-outbound status = %+v", status)
	}
	if !waitForTUIProxyPortState(proxyPort, false, tuiListenerValidationTimeout) {
		t.Fatal("entering silent without outbound left the normal proxy listener open")
	}
	if _, err := service.flcProxy(); err == nil {
		t.Fatal("silent without outbound exposed FLC credentials")
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(baseline) {
		t.Fatal("entering silent without outbound modified the shared profile")
	}
	if err := service.shutdown(); err != nil {
		t.Fatal(err)
	}
	shutdown = false
	select {
	case err := <-serviceDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Backend did not exit after shutdown ACK")
	}
}

func TestSilentModeRunsOnlyAuthenticatedFLCListener(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "flclash-silent-integration-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	previousRuntimeDirectory := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = directory
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previousRuntimeDirectory })

	proxyPort := freeTUITestPort(t)
	configPath := filepath.Join(directory, "config.yaml")
	source := fmt.Appendf(nil, `mixed-port: %d
mode: rule
log-level: silent
ipv6: false
unified-delay: true
tcp-concurrent: true
proxy-groups:
  - name: PROXY
    type: select
    proxies: [DIRECT]
rules:
  - MATCH,PROXY
`, proxyPort)
	if err := os.WriteFile(configPath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	paths := cliPaths{homeDir: directory, configPath: configPath}
	if err := rememberTUITrafficMode(directory, "rule"); err != nil {
		t.Fatal(err)
	}
	serviceDone := make(chan error, 1)
	go func() {
		serviceDone <- runTUIService(paths, defaultCLITestURL, nil, false)
	}()
	service := newTUIServiceClient(directory)
	shutdown := true
	t.Cleanup(func() {
		if shutdown {
			_ = service.shutdown()
		}
	})
	var status tuiServiceStatus
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		status, err = service.status()
		if err == nil {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("Backend did not start: %v", err)
	}
	status, err = service.startAtRevision(status.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !waitForTUITestPort(proxyPort, true, 2*time.Second) {
		t.Fatal("normal Proxy port did not start before silent transition")
	}
	status, err = service.setFLCOutbound("PROXY", status.Revision)
	if err != nil {
		t.Fatal(err)
	}
	status, err = service.setMode(tuiSilentMode, status.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if status.Mode != tuiSilentMode || !status.Running || !status.FLCEnabled ||
		status.SystemProxy || status.FLCOutbound != "PROXY" {
		t.Fatalf("silent status = %+v", status)
	}
	if !waitForTUITestPort(proxyPort, false, 2*time.Second) {
		t.Fatal("normal Proxy port remained open in silent mode")
	}
	privateStatus, err := service.flcProxy()
	if err != nil {
		t.Fatal(err)
	}
	privateURL, err := url.Parse(privateStatus.FLCProxyURL)
	if err != nil || privateURL.User == nil || privateURL.Hostname() != "127.0.0.1" {
		t.Fatalf("private FLC URL = %q, %v", privateStatus.FLCProxyURL, err)
	}

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "through-flc")
	}))
	defer target.Close()
	authenticatedTransport := http.DefaultTransport.(*http.Transport).Clone()
	authenticatedTransport.Proxy = http.ProxyURL(privateURL)
	defer authenticatedTransport.CloseIdleConnections()
	response, err := (&http.Client{
		Transport: authenticatedTransport,
		Timeout:   3 * time.Second,
	}).Get(target.URL)
	if err != nil {
		t.Fatalf("authenticated FLC request: %v", err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil || response.StatusCode != http.StatusOK || string(body) != "through-flc" {
		t.Fatalf("authenticated response = %s %q, %v", response.Status, body, readErr)
	}

	unauthenticatedURL := *privateURL
	unauthenticatedURL.User = nil
	unauthenticatedTransport := http.DefaultTransport.(*http.Transport).Clone()
	unauthenticatedTransport.Proxy = http.ProxyURL(&unauthenticatedURL)
	defer unauthenticatedTransport.CloseIdleConnections()
	response, err = (&http.Client{
		Transport: unauthenticatedTransport,
		Timeout:   3 * time.Second,
	}).Get(target.URL)
	if err != nil {
		t.Fatalf("unauthenticated FLC request did not return an HTTP rejection: %v", err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusProxyAuthRequired {
		t.Fatalf("unauthenticated FLC status = %s, want 407", response.Status)
	}

	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(source) {
		t.Fatal("silent mode modified the shared profile")
	}
	statePath := filepath.Join(directory, tuiStateFilename)
	savedState, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	failedStatus, err := service.setMode("rule", status.Revision)
	if err == nil {
		t.Fatal("leaving silent mode succeeded when its state could not be saved")
	}
	if failedStatus.Mode != tuiSilentMode || !failedStatus.FLCEnabled ||
		failedStatus.Revision != status.Revision {
		t.Fatalf("failed mode transaction status = %+v", failedStatus)
	}
	if !waitForTUITestPort(proxyPort, false, 2*time.Second) {
		t.Fatal("normal Proxy port opened after a failed silent-mode transaction")
	}
	afterRollback, readErr := os.ReadFile(configPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(afterRollback) != string(source) {
		t.Fatal("failed silent-mode transaction did not restore the shared profile")
	}
	if _, proxyErr := service.flcProxy(); proxyErr != nil {
		t.Fatalf("private FLC listener was lost after mode rollback: %v", proxyErr)
	}
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, savedState, 0o600); err != nil {
		t.Fatal(err)
	}
	status = failedStatus
	status, err = service.setMode("rule", status.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if status.Mode != "rule" || status.FLCEnabled {
		t.Fatalf("rule status after silent = %+v", status)
	}
	afterExit, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterExit) != string(source) {
		t.Fatal("leaving silent mode rewrote an unchanged native mode profile")
	}
	if !waitForTUITestPort(proxyPort, true, 2*time.Second) {
		t.Fatal("normal Proxy port did not return after leaving silent mode")
	}
	if _, err := service.flcProxy(); err == nil {
		t.Fatal("private FLC credentials remained available outside silent mode")
	}
	if err := service.shutdown(); err != nil {
		t.Fatal(err)
	}
	shutdown = false
	select {
	case err := <-serviceDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Backend did not exit after shutdown ACK")
	}
	if matches, _ := filepath.Glob(
		filepath.Join(directory, tuiSilentRuntimeConfigPrefix+"*.yaml"),
	); len(matches) != 0 {
		t.Fatalf("silent runtime profiles were not cleaned up: %v", matches)
	}
}
