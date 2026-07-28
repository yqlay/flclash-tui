//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	nethttp "net/http"

	"github.com/metacubex/mihomo/config"
	logrus "github.com/sirupsen/logrus"
	"golang.org/x/term"
)

type tuiGroup struct {
	Name  string
	Type  string
	Now   string
	Nodes []string
}

type tuiPage int

const (
	tuiPageDashboard tuiPage = iota
	tuiPageProxies
	tuiPageConnections
	tuiPageLogs
	tuiPageSettings
	tuiPageProfiles
	tuiPageProviders
	tuiPageTools
)

type tuiConnection struct {
	ID       string
	Host     string
	Process  string
	Network  string
	Chain    string
	Upload   int64
	Download int64
}

type tuiSettings struct {
	Mode        string
	MixedPort   int
	AllowLAN    bool
	IPv6        bool
	LogLevel    string
	TunEnabled  bool
	SystemProxy bool
}

type tuiProvider struct {
	Name      string
	Type      string
	Vehicle   string
	Count     int
	UpdatedAt string
}

type tuiProfile struct {
	Name    string
	Path    string
	Current bool
}

type tuiSnapshot struct {
	Page               tuiPage
	Groups             []tuiGroup
	Traffic            trafficSnapshot
	TotalTraffic       trafficSnapshot
	Connections        []tuiConnection
	Logs               []string
	Profiles           []tuiProfile
	Providers          []tuiProvider
	Settings           tuiSettings
	UpdatedAt          time.Time
	Status             string
	SelectedGroup      int
	SelectedNode       int
	SelectedRow        int
	SelectedProvider   int
	SelectedConnection int
	ShowHelp           bool
}

type trafficSnapshot struct {
	Up        int64 `json:"up"`
	Down      int64 `json:"down"`
	UpTotal   int64 `json:"upTotal"`
	DownTotal int64 `json:"downTotal"`
}

type tuiProxyResponse struct {
	Proxies map[string]struct {
		Type string   `json:"type"`
		Now  string   `json:"now"`
		All  []string `json:"all"`
	} `json:"proxies"`
}

type tuiConfigResponse struct {
	Mode      string `json:"mode"`
	MixedPort int    `json:"mixed-port"`
	AllowLAN  bool   `json:"allow-lan"`
	IPv6      bool   `json:"ipv6"`
	LogLevel  string `json:"log-level"`
	Tun       struct {
		Enable bool `json:"enable"`
	} `json:"tun"`
}

type tuiProviderResponse struct {
	Providers map[string]struct {
		Name      string            `json:"name"`
		Type      string            `json:"type"`
		Vehicle   string            `json:"vehicle-type"`
		UpdatedAt string            `json:"updated-at"`
		Proxies   []json.RawMessage `json:"proxies"`
	} `json:"providers"`
}

const defaultTUIConfig = `mixed-port: 7890
allow-lan: false
mode: rule
log-level: info
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,PROXY
`

func tuiCommand(args []string) error {
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configArg := fs.String("config", "", "path to config.yaml")
	directoryArg := fs.String("directory", "", "FlClash data directory")
	controllerArg := fs.String("controller", "127.0.0.1:9090", "Mihomo external controller address")
	secretArg := fs.String("secret", "", "Mihomo external controller secret")
	testURLArg := fs.String("test-url", "https://www.gstatic.com/generate_204", "URL used by proxy-group delay tests")
	noStartArg := fs.Bool("no-start", false, "connect to an already running Mihomo core")
	if err := fs.Parse(args); err != nil {
		return err
	}

	paths, err := resolvePaths(*configArg, *directoryArg)
	if err != nil {
		return err
	}
	if !isInteractiveTUI() {
		return errors.New("TUI requires an interactive terminal; use run or proxy commands in non-interactive shells")
	}
	if err := ensureTUIConfig(paths, *configArg == ""); err != nil {
		return err
	}

	var setupParams []byte
	client := controllerClient{options: controllerOptions{
		address: *controllerArg,
		secret:  *secretArg,
	}}
	if !*noStartArg {
		if err := ensureControllerFree(*controllerArg); err != nil {
			return err
		}
		setupParams, err = startCore(paths, *testURLArg, *controllerArg, *secretArg)
		if err != nil {
			return err
		}
		var setup SetupParams
		if err := UnmarshalJson(setupParams, &setup); err == nil && setup.ExternalControllerSecret != nil {
			client.options.secret = *setup.ExternalControllerSecret
		}
		if err := waitForController(client, 3*time.Second); err != nil {
			handleShutdown()
			return err
		}
	}
	return runTUI(client, paths, setupParams, !*noStartArg)
}

func ensureTUIConfig(paths cliPaths, allowCreate bool) error {
	_, err := os.Stat(paths.configPath)
	if err == nil {
		return nil
	}
	if !os.IsNotExist(err) || !allowCreate {
		return fmt.Errorf("config file %q: %w", paths.configPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.configPath), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(paths.configPath, []byte(defaultTUIConfig), 0o600); err != nil {
		return fmt.Errorf("create default config: %w", err)
	}
	return nil
}

func startCore(paths cliPaths, testURL, controller, secret string) ([]byte, error) {
	configData, err := os.ReadFile(paths.configPath)
	if err != nil {
		return nil, fmt.Errorf("config file %q: %w", paths.configPath, err)
	}
	if err := os.MkdirAll(paths.homeDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	rawConfig, err := config.UnmarshalRawConfig(configData)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	effectiveSecret := secret
	if effectiveSecret == "" {
		effectiveSecret = rawConfig.Secret
	}

	initParams, err := json.Marshal(InitParams{
		HomeDir:    paths.homeDir,
		ConfigPath: paths.configPath,
		Version:    1,
	})
	if err != nil {
		return nil, err
	}
	if !handleInitClash(string(initParams)) {
		return nil, errors.New("initialize FlClash core failed")
	}

	setup := SetupParams{
		TestURL:     testURL,
		SelectedMap: map[string]string{},
	}
	if controller != "" {
		setup.ExternalController = &controller
		if effectiveSecret != "" {
			setup.ExternalControllerSecret = &effectiveSecret
		}
	}
	setupParams, err := json.Marshal(setup)
	if err != nil {
		return nil, err
	}
	if message := handleSetupConfig(setupParams); message != "" {
		return nil, fmt.Errorf("load config: %s", message)
	}
	if !handleStartListener() {
		return nil, errors.New("start proxy listeners failed")
	}
	return setupParams, nil
}

func runTUI(client controllerClient, paths cliPaths, setupParams []byte, ownsCore bool) error {
	if !isInteractiveTUI() {
		return errors.New("TUI requires an interactive terminal; use run or proxy commands in non-interactive shells")
	}

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("enable raw terminal mode: %w", err)
	}
	defer func() {
		_ = term.Restore(int(os.Stdin.Fd()), oldState)
		logrus.SetOutput(os.Stdout)
		_, _ = fmt.Fprint(os.Stdout, "\x1b[?25h\x1b[0m\n")
	}()

	logrus.SetOutput(io.Discard)
	_, _ = fmt.Fprint(os.Stdout, "\x1b[?25l\x1b[2J\x1b[H")
	handleStartLog()
	defer handleStopLog()
	snapshot := tuiSnapshot{Status: "Loading...", SelectedGroup: 0, SelectedNode: 0}
	refreshTUISnapshot(&snapshot, client)
	refreshTUIProfiles(&snapshot, paths)
	coreRunning := ownsCore
	drawTUI(os.Stdout, snapshot, paths, client.options.address, ownsCore, coreRunning)

	keys := make(chan tuiKey)
	go readTUIKeys(os.Stdin, keys)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Stop(signals)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-signals:
			if ownsCore {
				handleShutdown()
			}
			return nil
		case key := <-keys:
			switch key {
			case tuiKeyQuit:
				if ownsCore {
					handleShutdown()
				}
				return nil
			case tuiKeyRefresh:
				refreshTUISnapshot(&snapshot, client)
				refreshTUIProfiles(&snapshot, paths)
			case tuiKeyReload:
				if !ownsCore {
					snapshot.Status = "Reload requires a core started by this process"
					break
				}
				if message := handleSetupConfig(setupParams); message != "" {
					snapshot.Status = "Reload failed: " + message
				} else {
					snapshot.Status = "Configuration reloaded"
					refreshTUISnapshot(&snapshot, client)
				}
			case tuiKeyHelp:
				snapshot.ShowHelp = !snapshot.ShowHelp
			case tuiKeyDashboard:
				snapshot.Page = tuiPageDashboard
			case tuiKeyProxies:
				snapshot.Page = tuiPageProxies
			case tuiKeyConnections:
				snapshot.Page = tuiPageConnections
			case tuiKeyLogs:
				snapshot.Page = tuiPageLogs
			case tuiKeySettings:
				snapshot.Page = tuiPageSettings
			case tuiKeyProfiles:
				snapshot.Page = tuiPageProfiles
			case tuiKeyProviders:
				snapshot.Page = tuiPageProviders
			case tuiKeyTools:
				snapshot.Page = tuiPageTools
			case tuiKeyCloseConnections:
				if err := client.closeAllConnections(); err != nil {
					snapshot.Status = "Close connections failed: " + err.Error()
				} else {
					snapshot.Status = "All connections closed"
				}
			case tuiKeyCloseConnection:
				if snapshot.Page != tuiPageConnections || snapshot.SelectedConnection < 0 || snapshot.SelectedConnection >= len(snapshot.Connections) {
					break
				}
				connection := snapshot.Connections[snapshot.SelectedConnection]
				if err := client.closeConnection(connection.ID); err != nil {
					snapshot.Status = "Close connection failed: " + err.Error()
				} else {
					snapshot.Status = "Connection closed"
					refreshTUISnapshot(&snapshot, client)
				}
			case tuiKeyCoreToggle:
				if !ownsCore {
					snapshot.Status = "Core lifecycle is owned by the external process"
				} else if coreRunning {
					if handleStopListener() {
						coreRunning = false
						snapshot.Status = "Core listeners stopped"
					}
				} else if handleStartListener() {
					coreRunning = true
					snapshot.Status = "Core listeners started"
				}
			case tuiKeyEdit:
				editPath := paths.configPath
				if snapshot.Page == tuiPageProfiles && snapshot.SelectedRow >= 0 && snapshot.SelectedRow < len(snapshot.Profiles) {
					editPath = snapshot.Profiles[snapshot.SelectedRow].Path
				}
				if err := runTUIEditor(editPath, &oldState); err != nil {
					snapshot.Status = "Editor failed: " + err.Error()
				} else if ownsCore {
					if message := handleSetupConfig(setupParams); message != "" {
						snapshot.Status = "Edited config is invalid: " + message
					} else {
						snapshot.Status = "Configuration applied"
					}
				}
			case tuiKeyNewProfile:
				if snapshot.Page == tuiPageProfiles {
					if err := addTUIProfile(paths.homeDir, &oldState); err != nil {
						snapshot.Status = "Add profile failed: " + err.Error()
					} else {
						snapshot.Status = "Profile downloaded"
						refreshTUIProfiles(&snapshot, paths)
					}
				}
			case tuiKeyBackup:
				if backupPath, err := backupTUIConfig(paths.configPath); err != nil {
					snapshot.Status = "Backup failed: " + err.Error()
				} else {
					snapshot.Status = "Backup created: " + filepath.Base(backupPath)
				}
			case tuiKeyRestore:
				if backupPath, err := restoreLatestTUIConfig(paths.configPath); err != nil {
					snapshot.Status = "Restore failed: " + err.Error()
				} else if ownsCore {
					if message := handleSetupConfig(setupParams); message != "" {
						snapshot.Status = "Restore applied with errors: " + message
					} else {
						snapshot.Status = "Restored: " + filepath.Base(backupPath)
					}
				}
			case tuiKeyGeoUpdate:
				if err := client.updateGeo(); err != nil {
					snapshot.Status = "Geo update failed: " + err.Error()
				} else {
					snapshot.Status = "Geo databases update started"
				}
			case tuiKeyResetTraffic:
				if ownsCore {
					handleResetTraffic()
					snapshot.Status = "Traffic counters reset"
				} else {
					snapshot.Status = "Traffic reset requires a core started by this process"
				}
			case tuiKeyUp:
				if snapshot.Page == tuiPageProviders {
					moveTUIProvider(&snapshot, -1)
				} else if snapshot.Page == tuiPageProfiles {
					moveTUIProfile(&snapshot, -1)
				} else if snapshot.Page == tuiPageConnections {
					moveTUIConnection(&snapshot, -1)
				} else {
					moveTUIGroup(&snapshot, -1)
				}
			case tuiKeyDown:
				if snapshot.Page == tuiPageProviders {
					moveTUIProvider(&snapshot, 1)
				} else if snapshot.Page == tuiPageProfiles {
					moveTUIProfile(&snapshot, 1)
				} else if snapshot.Page == tuiPageConnections {
					moveTUIConnection(&snapshot, 1)
				} else {
					moveTUIGroup(&snapshot, 1)
				}
			case tuiKeyLeft:
				moveTUINode(&snapshot, -1)
			case tuiKeyRight:
				moveTUINode(&snapshot, 1)
			case tuiKeySelect:
				if snapshot.Page == tuiPageProxies || snapshot.Page == tuiPageDashboard {
					selectTUIProxy(&snapshot, client)
				} else if snapshot.Page == tuiPageProfiles {
					switchTUIProfile(&snapshot, &paths, &setupParams, client, ownsCore)
				} else if snapshot.Page == tuiPageProviders {
					updateTUIProvider(&snapshot, client)
				}
			case tuiKeyAllowLAN, tuiKeyIPv6, tuiKeyTun, tuiKeyMode, tuiKeyPortUp, tuiKeyPortDown:
				updateTUISettings(&snapshot, client, key)
			case tuiKeySystemProxy:
				toggleTUISystemProxy(&snapshot)
			}
			drawTUI(os.Stdout, snapshot, paths, client.options.address, ownsCore, coreRunning)
		case <-ticker.C:
			refreshTUISnapshot(&snapshot, client)
			refreshTUIProfiles(&snapshot, paths)
			drawTUI(os.Stdout, snapshot, paths, client.options.address, ownsCore, coreRunning)
		}
	}
}

func backupTUIConfig(configPath string) (string, error) {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	backupPath := fmt.Sprintf("%s.backup-%d", configPath, time.Now().UnixNano())
	if err := os.WriteFile(backupPath, data, 0o600); err != nil {
		return "", err
	}
	return backupPath, nil
}

func restoreLatestTUIConfig(configPath string) (string, error) {
	entries, err := os.ReadDir(filepath.Dir(configPath))
	if err != nil {
		return "", err
	}
	prefix := filepath.Base(configPath) + ".backup-"
	var latest string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		candidate := filepath.Join(filepath.Dir(configPath), entry.Name())
		if latest == "" || entry.Name() > filepath.Base(latest) {
			latest = candidate
		}
	}
	if latest == "" {
		return "", errors.New("no config backup found")
	}
	data, err := os.ReadFile(latest)
	if err != nil {
		return "", err
	}
	if message := validateConfigBytes(data); message != "" {
		return "", fmt.Errorf("restored config is invalid: %s", message)
	}
	if err := os.WriteFile(configPath, data, 0o600); err != nil {
		return "", err
	}
	return latest, nil
}

func isInteractiveTUI() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func waitForController(client controllerClient, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, err := client.request(nethttp.MethodGet, "/", nil); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = errors.New("controller did not become ready")
	}
	return fmt.Errorf("controller is not ready: %w", lastErr)
}

func refreshTUISnapshot(snapshot *tuiSnapshot, client controllerClient) {
	data, err := client.request("GET", "/proxies", nil)
	if err != nil {
		snapshot.Status = "Controller unavailable: " + err.Error()
		snapshot.UpdatedAt = time.Now()
		return
	}

	var response tuiProxyResponse
	if err := json.Unmarshal(data, &response); err != nil {
		snapshot.Status = "Invalid controller response: " + err.Error()
		return
	}

	groups := make([]tuiGroup, 0, len(response.Proxies))
	for name, proxy := range response.Proxies {
		if len(proxy.All) == 0 || !isTUIGroup(proxy.Type) {
			continue
		}
		groups = append(groups, tuiGroup{Name: name, Type: proxy.Type, Now: proxy.Now, Nodes: proxy.All})
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].Name < groups[j].Name })
	snapshot.Groups = groups
	if snapshot.SelectedGroup >= len(groups) {
		snapshot.SelectedGroup = maxTUIIndex(len(groups) - 1)
	}
	if len(groups) > 0 && snapshot.SelectedNode >= len(groups[snapshot.SelectedGroup].Nodes) {
		snapshot.SelectedNode = maxTUIIndex(len(groups[snapshot.SelectedGroup].Nodes) - 1)
	}

	if traffic, err := client.requestStreamFirst("/traffic"); err == nil {
		var value trafficSnapshot
		if json.Unmarshal(traffic, &value) == nil {
			snapshot.Traffic = value
			snapshot.TotalTraffic = trafficSnapshot{Up: value.UpTotal, Down: value.DownTotal}
		}
	}
	if connections, err := client.request("GET", "/connections", nil); err == nil {
		var value struct {
			Connections []struct {
				ID       string `json:"id"`
				Metadata struct {
					Host    string `json:"host"`
					Process string `json:"process"`
					Network string `json:"network"`
				} `json:"metadata"`
				Upload   int64    `json:"upload"`
				Download int64    `json:"download"`
				Chains   []string `json:"chains"`
			} `json:"connections"`
		}
		if json.Unmarshal(connections, &value) == nil {
			snapshot.Connections = make([]tuiConnection, 0, len(value.Connections))
			for _, item := range value.Connections {
				chain := "DIRECT"
				if len(item.Chains) > 0 {
					chain = item.Chains[len(item.Chains)-1]
				}
				snapshot.Connections = append(snapshot.Connections, tuiConnection{
					ID: item.ID, Host: item.Metadata.Host, Process: item.Metadata.Process,
					Network: item.Metadata.Network, Chain: chain,
					Upload: item.Upload, Download: item.Download,
				})
			}
			if snapshot.SelectedConnection >= len(snapshot.Connections) {
				snapshot.SelectedConnection = maxTUIIndex(len(snapshot.Connections) - 1)
			}
		}
	}
	if config, err := client.request("GET", "/configs", nil); err == nil {
		var value tuiConfigResponse
		if json.Unmarshal(config, &value) == nil {
			snapshot.Settings = tuiSettings{
				Mode: value.Mode, MixedPort: value.MixedPort, AllowLAN: value.AllowLAN,
				IPv6: value.IPv6, LogLevel: value.LogLevel, TunEnabled: value.Tun.Enable,
			}
		}
	}
	snapshot.Settings.SystemProxy = linuxSystemProxyEnabled()
	if providers, err := client.request("GET", "/providers/proxies", nil); err == nil {
		var value tuiProviderResponse
		if json.Unmarshal(providers, &value) == nil {
			snapshot.Providers = make([]tuiProvider, 0, len(value.Providers))
			for name, item := range value.Providers {
				if item.Name == "" {
					item.Name = name
				}
				snapshot.Providers = append(snapshot.Providers, tuiProvider{
					Name: item.Name, Type: item.Type, Vehicle: item.Vehicle,
					Count: len(item.Proxies), UpdatedAt: item.UpdatedAt,
				})
			}
			sort.Slice(snapshot.Providers, func(i, j int) bool { return snapshot.Providers[i].Name < snapshot.Providers[j].Name })
			if snapshot.SelectedProvider >= len(snapshot.Providers) {
				snapshot.SelectedProvider = maxTUIIndex(len(snapshot.Providers) - 1)
			}
		}
	}
	snapshot.Logs = cliLogSnapshot()
	snapshot.Status = "Connected"
	snapshot.UpdatedAt = time.Now()
}

func refreshTUIProfiles(snapshot *tuiSnapshot, paths cliPaths) {
	entries, err := os.ReadDir(paths.homeDir)
	if err != nil {
		snapshot.Profiles = nil
		return
	}
	profiles := make([]tuiProfile, 0, len(entries)+1)
	currentFound := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(paths.homeDir, entry.Name())
		current := filepath.Clean(path) == filepath.Clean(paths.configPath)
		currentFound = currentFound || current
		profiles = append(profiles, tuiProfile{
			Name: entry.Name(), Path: path, Current: current,
		})
	}
	if !currentFound {
		profiles = append(profiles, tuiProfile{
			Name: filepath.Base(paths.configPath), Path: paths.configPath, Current: true,
		})
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	snapshot.Profiles = profiles
	if snapshot.SelectedRow >= len(profiles) {
		snapshot.SelectedRow = maxTUIIndex(len(profiles) - 1)
	}
}

func switchTUIProfile(snapshot *tuiSnapshot, paths *cliPaths, setupParams *[]byte, client controllerClient, ownsCore bool) {
	if !ownsCore {
		snapshot.Status = "Profile switching requires a core started by this process"
		return
	}
	if len(snapshot.Profiles) == 0 {
		return
	}
	profile := snapshot.Profiles[snapshot.SelectedRow]
	if profile.Current {
		return
	}
	if message := handleValidateConfig(profile.Path); message != "" {
		snapshot.Status = "Profile invalid: " + message
		return
	}
	controller := client.options.address
	secret := client.options.secret
	params := defaultSetupParams()
	if len(*setupParams) > 0 {
		if err := UnmarshalJson(*setupParams, params); err != nil {
			snapshot.Status = "Profile setup failed: " + err.Error()
			return
		}
	}
	params.ExternalController = &controller
	params.ExternalControllerSecret = &secret
	newSetupParams, err := json.Marshal(params)
	if err != nil {
		snapshot.Status = "Profile setup failed: " + err.Error()
		return
	}
	initParams, err := json.Marshal(InitParams{
		HomeDir: paths.homeDir, ConfigPath: profile.Path, Version: 1,
	})
	if err != nil || !handleInitClash(string(initParams)) {
		snapshot.Status = "Profile initialization failed"
		return
	}
	if message := handleSetupConfig(newSetupParams); message != "" {
		snapshot.Status = "Profile load failed: " + message
		return
	}
	if !handleStartListener() {
		snapshot.Status = "Profile listener start failed"
		return
	}
	paths.configPath = profile.Path
	*setupParams = newSetupParams
	snapshot.Status = "Active profile: " + profile.Name
	refreshTUISnapshot(snapshot, client)
	refreshTUIProfiles(snapshot, *paths)
}

func isTUIGroup(proxyType string) bool {
	switch strings.ToLower(proxyType) {
	case "selector", "urltest", "fallback", "loadbalance", "relay":
		return true
	default:
		return false
	}
}

func moveTUIGroup(snapshot *tuiSnapshot, delta int) {
	if len(snapshot.Groups) == 0 {
		return
	}
	snapshot.SelectedGroup = (snapshot.SelectedGroup + delta + len(snapshot.Groups)) % len(snapshot.Groups)
	snapshot.SelectedNode = 0
}

func moveTUINode(snapshot *tuiSnapshot, delta int) {
	if len(snapshot.Groups) == 0 {
		return
	}
	nodes := snapshot.Groups[snapshot.SelectedGroup].Nodes
	if len(nodes) == 0 {
		return
	}
	snapshot.SelectedNode = (snapshot.SelectedNode + delta + len(nodes)) % len(nodes)
}

func moveTUIProvider(snapshot *tuiSnapshot, delta int) {
	if len(snapshot.Providers) == 0 {
		return
	}
	snapshot.SelectedProvider = (snapshot.SelectedProvider + delta + len(snapshot.Providers)) % len(snapshot.Providers)
}

func moveTUIProfile(snapshot *tuiSnapshot, delta int) {
	if len(snapshot.Profiles) == 0 {
		return
	}
	snapshot.SelectedRow = (snapshot.SelectedRow + delta + len(snapshot.Profiles)) % len(snapshot.Profiles)
}

func moveTUIConnection(snapshot *tuiSnapshot, delta int) {
	if len(snapshot.Connections) == 0 {
		return
	}
	snapshot.SelectedConnection = (snapshot.SelectedConnection + delta + len(snapshot.Connections)) % len(snapshot.Connections)
}

func selectTUIProxy(snapshot *tuiSnapshot, client controllerClient) {
	if snapshot.SelectedGroup < 0 || snapshot.SelectedGroup >= len(snapshot.Groups) {
		return
	}
	group := snapshot.Groups[snapshot.SelectedGroup]
	if snapshot.SelectedNode < 0 || snapshot.SelectedNode >= len(group.Nodes) {
		return
	}
	if err := client.selectProxy(group.Name, group.Nodes[snapshot.SelectedNode]); err != nil {
		snapshot.Status = "Switch failed: " + err.Error()
		return
	}
	snapshot.Status = fmt.Sprintf("Switched %s to %s", group.Name, group.Nodes[snapshot.SelectedNode])
	refreshTUISnapshot(snapshot, client)
}

func updateTUIProvider(snapshot *tuiSnapshot, client controllerClient) {
	if snapshot.SelectedProvider < 0 || snapshot.SelectedProvider >= len(snapshot.Providers) {
		return
	}
	provider := snapshot.Providers[snapshot.SelectedProvider]
	if err := client.updateProvider(provider.Name); err != nil {
		snapshot.Status = "Provider update failed: " + err.Error()
		return
	}
	snapshot.Status = "Updated provider " + provider.Name
	refreshTUISnapshot(snapshot, client)
}

func updateTUISettings(snapshot *tuiSnapshot, client controllerClient, key tuiKey) {
	patch := map[string]interface{}{}
	switch key {
	case tuiKeyAllowLAN:
		patch["allow-lan"] = !snapshot.Settings.AllowLAN
	case tuiKeyIPv6:
		patch["ipv6"] = !snapshot.Settings.IPv6
	case tuiKeyTun:
		patch["tun"] = map[string]bool{"enable": !snapshot.Settings.TunEnabled}
	case tuiKeyMode:
		mode := "rule"
		if strings.EqualFold(snapshot.Settings.Mode, "rule") {
			mode = "global"
		} else if strings.EqualFold(snapshot.Settings.Mode, "global") {
			mode = "direct"
		}
		patch["mode"] = mode
	case tuiKeyPortUp:
		patch["mixed-port"] = snapshot.Settings.MixedPort + 1
	case tuiKeyPortDown:
		if snapshot.Settings.MixedPort > 0 {
			patch["mixed-port"] = snapshot.Settings.MixedPort - 1
		}
	default:
		return
	}
	if err := client.patchConfig(patch); err != nil {
		snapshot.Status = "Settings update failed: " + err.Error()
		return
	}
	snapshot.Status = "Settings updated"
	refreshTUISnapshot(snapshot, client)
}

func toggleTUISystemProxy(snapshot *tuiSnapshot) {
	port := snapshot.Settings.MixedPort
	if port <= 0 {
		snapshot.Status = "System proxy requires a positive mixed port"
		return
	}
	enable := !snapshot.Settings.SystemProxy
	if err := setLinuxSystemProxy(port, enable); err != nil {
		snapshot.Status = "System proxy update failed: " + err.Error()
		return
	}
	snapshot.Settings.SystemProxy = enable
	snapshot.Status = "System proxy enabled"
	if !enable {
		snapshot.Status = "System proxy disabled"
	}
}

func linuxSystemProxyEnabled() bool {
	schema := linuxProxySchema()
	output, err := exec.Command("gsettings", "get", schema, "mode").Output()
	return err == nil && strings.TrimSpace(string(output)) == "'manual'"
}

func setLinuxSystemProxy(port int, enable bool) error {
	schema := linuxProxySchema()
	commands := make([][]string, 0, 7)
	if enable {
		commands = append(commands,
			[]string{schema, "ignore-hosts", "[]"},
			[]string{schema + ".http", "host", "127.0.0.1"},
			[]string{schema + ".http", "port", fmt.Sprintf("%d", port)},
			[]string{schema + ".https", "host", "127.0.0.1"},
			[]string{schema + ".https", "port", fmt.Sprintf("%d", port)},
			[]string{schema + ".socks", "host", "127.0.0.1"},
			[]string{schema + ".socks", "port", fmt.Sprintf("%d", port)},
		)
	}
	mode := "none"
	if enable {
		mode = "manual"
	}
	// Set mode last so a failed host/port update cannot leave a half-configured
	// desktop proxy enabled.
	commands = append(commands, []string{schema, "mode", mode})
	for _, args := range commands {
		if output, err := exec.Command("gsettings", append([]string{"set"}, args...)...).CombinedOutput(); err != nil {
			return fmt.Errorf("gsettings: %s", strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func linuxProxySchema() string {
	if strings.Contains(strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP")), "MATE") {
		return "org.mate.system.proxy"
	}
	return "org.gnome.system.proxy"
}

func runTUIEditor(path string, oldState **term.State) error {
	return runTUICooked(oldState, func() error {
		editor := os.Getenv("VISUAL")
		if editor == "" {
			editor = os.Getenv("EDITOR")
		}
		if editor == "" {
			editor = "vi"
		}
		command := exec.Command("sh", "-c", editor+" -- \"$1\"", "flclash-tui-editor", path)
		command.Stdin = os.Stdin
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		return command.Run()
	})
}

func addTUIProfile(homeDir string, oldState **term.State) error {
	return runTUICooked(oldState, func() error {
		_, _ = fmt.Fprint(os.Stdout, "Subscription URL (empty cancels): ")
		value, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && len(value) == 0 {
			return err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return nil
		}
		parsed, err := nethttp.NewRequest(nethttp.MethodGet, value, nil)
		if err != nil || (parsed.URL.Scheme != "http" && parsed.URL.Scheme != "https") {
			return errors.New("subscription URL must use http or https")
		}
		client := &nethttp.Client{Timeout: 30 * time.Second}
		response, err := client.Do(parsed)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("subscription returned %s", response.Status)
		}
		data, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return errors.New("subscription response is empty")
		}
		if err := os.MkdirAll(homeDir, 0o700); err != nil {
			return err
		}
		path := filepath.Join(homeDir, fmt.Sprintf("profile-%d.yaml", time.Now().UnixNano()))
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
		if message := handleValidateConfig(path); message != "" {
			_ = os.Remove(path)
			return fmt.Errorf("downloaded profile is invalid: %s", message)
		}
		return nil
	})
}

func runTUICooked(oldState **term.State, action func() error) error {
	if err := leaveTUIMode(*oldState); err != nil {
		return err
	}
	actionErr := action()
	reenterErr := reenterTUIMode(oldState)
	if actionErr != nil {
		if reenterErr != nil {
			return fmt.Errorf("%v (failed to restore TUI terminal: %w)", actionErr, reenterErr)
		}
		return actionErr
	}
	return reenterErr
}

func leaveTUIMode(state *term.State) error {
	if err := term.Restore(int(os.Stdin.Fd()), state); err != nil {
		return err
	}
	logrus.SetOutput(os.Stdout)
	_, _ = fmt.Fprint(os.Stdout, "\x1b[?25h\x1b[0m")
	return nil
}

func reenterTUIMode(oldState **term.State) error {
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	*oldState = state
	logrus.SetOutput(io.Discard)
	_, _ = fmt.Fprint(os.Stdout, "\x1b[?25l")
	return nil
}

func maxTUIIndex(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

type tuiKey byte

const (
	tuiKeyQuit tuiKey = iota
	tuiKeyRefresh
	tuiKeyReload
	tuiKeyHelp
	tuiKeyUp
	tuiKeyDown
	tuiKeyLeft
	tuiKeyRight
	tuiKeySelect
	tuiKeyDashboard
	tuiKeyProxies
	tuiKeyConnections
	tuiKeyLogs
	tuiKeySettings
	tuiKeyProfiles
	tuiKeyProviders
	tuiKeyCloseConnections
	tuiKeyAllowLAN
	tuiKeyIPv6
	tuiKeyTun
	tuiKeyMode
	tuiKeyPortUp
	tuiKeyPortDown
	tuiKeySystemProxy
	tuiKeyCoreToggle
	tuiKeyEdit
	tuiKeyNewProfile
	tuiKeyCloseConnection
	tuiKeyTools
	tuiKeyBackup
	tuiKeyRestore
	tuiKeyGeoUpdate
	tuiKeyResetTraffic
)

func readTUIKeys(reader io.Reader, keys chan<- tuiKey) {
	buffer := make([]byte, 1)
	for {
		if _, err := io.ReadFull(reader, buffer); err != nil {
			return
		}
		key := tuiKey(0xff)
		switch buffer[0] {
		case 'q', 'Q', 3:
			key = tuiKeyQuit
		case 'r':
			key = tuiKeyRefresh
		case 'R':
			key = tuiKeyReload
		case '?':
			key = tuiKeyHelp
		case '1':
			key = tuiKeyDashboard
		case '2':
			key = tuiKeyProxies
		case '3':
			key = tuiKeyConnections
		case '4':
			key = tuiKeyLogs
		case '5':
			key = tuiKeySettings
		case '6':
			key = tuiKeyProfiles
		case '7':
			key = tuiKeyProviders
		case 'x', 'X':
			key = tuiKeyCloseConnections
		case 'a':
			key = tuiKeyAllowLAN
		case 'v':
			key = tuiKeyIPv6
		case 't':
			key = tuiKeyTun
		case 'm':
			key = tuiKeyMode
		case '+', '=':
			key = tuiKeyPortUp
		case '-':
			key = tuiKeyPortDown
		case 's':
			key = tuiKeySystemProxy
		case 'c':
			key = tuiKeyCoreToggle
		case 'e':
			key = tuiKeyEdit
		case 'n':
			key = tuiKeyNewProfile
		case 'd':
			key = tuiKeyCloseConnection
		case '8':
			key = tuiKeyTools
		case 'b':
			key = tuiKeyBackup
		case 'B':
			key = tuiKeyRestore
		case 'g':
			key = tuiKeyGeoUpdate
		case 'z':
			key = tuiKeyResetTraffic
		case '\r', '\n', ' ':
			key = tuiKeySelect
		case 'k':
			key = tuiKeyUp
		case 'j':
			key = tuiKeyDown
		case 'h':
			key = tuiKeyLeft
		case 'l':
			key = tuiKeyRight
		case 0x1b:
			key = readTUIEscape(reader)
		}
		if key != tuiKey(0xff) {
			keys <- key
		}
	}
}

func readTUIEscape(reader io.Reader) tuiKey {
	buffer := make([]byte, 2)
	if _, err := io.ReadFull(reader, buffer); err != nil || buffer[0] != '[' {
		return tuiKey(0xff)
	}
	switch buffer[1] {
	case 'A':
		return tuiKeyUp
	case 'B':
		return tuiKeyDown
	case 'C':
		return tuiKeyRight
	case 'D':
		return tuiKeyLeft
	default:
		return tuiKey(0xff)
	}
}

func drawTUI(w io.Writer, snapshot tuiSnapshot, paths cliPaths, controllerAddress string, ownsCore, coreRunning bool) {
	var b strings.Builder
	b.WriteString("\x1b[2J\x1b[H")
	b.WriteString("\x1b[1;36mFlClash TUI\x1b[0m  \x1b[2mMihomo core\x1b[0m\n")
	b.WriteString("────────────────────────────────────────────────────────────────────────\n")
	fmt.Fprintf(&b, " Status: %-56s\n", truncateTUI(snapshot.Status, 56))
	fmt.Fprintf(&b, " Traffic: ↑ %-10s  ↓ %-10s  Connections: %-5d  Updated: %s\n",
		formatBytes(snapshot.Traffic.Up), formatBytes(snapshot.Traffic.Down), len(snapshot.Connections),
		snapshot.UpdatedAt.Format("15:04:05"))
	coreStatus := "external"
	if ownsCore {
		coreStatus = "stopped"
		if coreRunning {
			coreStatus = "running"
		}
	}
	fmt.Fprintf(&b, " Core: %-8s Controller: %-24s\n", coreStatus, controllerAddress)
	b.WriteString("────────────────────────────────────────────────────────────────────────\n")

	if snapshot.ShowHelp {
		b.WriteString("\n  Keyboard\n\n")
		b.WriteString("  ↑/k ↓/j     select proxy group\n")
		b.WriteString("  ←/h →/l     select node\n")
		b.WriteString("  Enter/Space  switch node\n")
		b.WriteString("  r            refresh\n")
		b.WriteString("  R            reload configuration\n")
		b.WriteString("  a/v/t        toggle LAN, IPv6, TUN on settings page\n")
		b.WriteString("  m +/-         change mode or mixed port on settings page\n")
		b.WriteString("  s            toggle Linux desktop system proxy\n")
		b.WriteString("  c            start/stop core listeners\n")
		b.WriteString("  e            edit selected/current YAML in $EDITOR\n")
		b.WriteString("  n            download a subscription profile\n")
		b.WriteString("  6            profiles  7 providers\n")
		b.WriteString("  8            tools (backup, restore, Geo, editor)\n")
		b.WriteString("  ?            toggle help\n")
		b.WriteString("  q/Ctrl-C     quit\n")
	} else if snapshot.Page == tuiPageConnections {
		drawTUIConnections(&b, snapshot)
	} else if snapshot.Page == tuiPageLogs {
		drawTUILogs(&b, snapshot)
	} else if snapshot.Page == tuiPageSettings {
		drawTUISettings(&b, snapshot)
	} else if snapshot.Page == tuiPageProfiles {
		drawTUIProfiles(&b, snapshot)
	} else if snapshot.Page == tuiPageProviders {
		b.WriteString("\n  \x1b[1mProviders\x1b[0m\n\n")
		if len(snapshot.Providers) == 0 {
			b.WriteString("  No proxy providers configured.\n")
		} else {
			for i, provider := range snapshot.Providers {
				marker := "  "
				if i == snapshot.SelectedProvider {
					marker = "➜ "
				}
				fmt.Fprintf(&b, " %s%-28s %-12s %-8d %s\n", marker,
					truncateTUI(provider.Name, 28), truncateTUI(provider.Type, 12),
					provider.Count, truncateTUI(provider.UpdatedAt, 24))
			}
			b.WriteString("\n  Enter updates the selected proxy provider.\n")
		}
	} else if snapshot.Page == tuiPageTools {
		b.WriteString("\n  \x1b[1mTools\x1b[0m\n\n")
		b.WriteString("  e  edit the current YAML configuration in $EDITOR\n")
		b.WriteString("  b  create a timestamped configuration backup\n")
		b.WriteString("  B  restore the newest configuration backup\n")
		b.WriteString("  g  update Mihomo Geo databases\n")
		b.WriteString("  z  reset traffic counters\n")
		b.WriteString("\n  Advanced DNS, network, rules, scripts, and profile overrides remain\n")
		b.WriteString("  available through the same YAML editor and core configuration path.\n")
	} else if snapshot.Page == tuiPageDashboard {
		drawTUIDashboard(&b, snapshot, paths)
	} else if len(snapshot.Groups) == 0 {
		b.WriteString("\n  No selectable proxy groups found.\n")
		b.WriteString("  Check the configuration and press r to refresh.\n")
	} else {
		group := snapshot.Groups[snapshot.SelectedGroup]
		b.WriteString("\n  \x1b[1mProxy groups\x1b[0m\n\n")
		for i, item := range snapshot.Groups {
			marker := "  "
			if i == snapshot.SelectedGroup {
				marker = "➜ "
			}
			fmt.Fprintf(&b, " %s%-28s %-12s %s\n", marker, truncateTUI(item.Name, 28), item.Type, truncateTUI(item.Now, 30))
		}
		b.WriteString("\n  \x1b[1mNodes in ")
		b.WriteString(group.Name)
		b.WriteString("\x1b[0m\n\n")
		for i, node := range group.Nodes {
			marker := "  "
			if i == snapshot.SelectedNode {
				marker = "➜ "
			}
			current := ""
			if node == group.Now {
				current = " \x1b[32m(current)\x1b[0m"
			}
			fmt.Fprintf(&b, " %s%s%s\n", marker, truncateTUI(node, 54), current)
		}
	}

	b.WriteString("\n────────────────────────────────────────────────────────────────────────\n")
	b.WriteString(" 1 dashboard  2 proxies  3 connections  4 logs  5 settings  6 profiles  7 providers  8 tools\n")
	b.WriteString(" ↑/↓ select  ←/→ node  Enter switch  r refresh  R reload  e edit  n new profile\n")
	b.WriteString(" d close selected  x close all  ? help  q quit\n")
	_, _ = io.WriteString(w, b.String())
}

func drawTUIDashboard(b *strings.Builder, snapshot tuiSnapshot, paths cliPaths) {
	b.WriteString("\n  \x1b[1mDashboard\x1b[0m\n\n")
	fmt.Fprintf(b, "  Config       %s\n", paths.configPath)
	fmt.Fprintf(b, "  Mode         %s\n", snapshot.Settings.Mode)
	fmt.Fprintf(b, "  Mixed port   %d\n", snapshot.Settings.MixedPort)
	fmt.Fprintf(b, "  Total up     %s\n", formatBytes(snapshot.TotalTraffic.Up))
	fmt.Fprintf(b, "  Total down   %s\n", formatBytes(snapshot.TotalTraffic.Down))
	fmt.Fprintf(b, "  Tun          %t\n", snapshot.Settings.TunEnabled)
	b.WriteString("\n  Use 2 to manage proxy groups and 3/4 for live connections and logs.\n")
}

func drawTUIConnections(b *strings.Builder, snapshot tuiSnapshot) {
	b.WriteString("\n  \x1b[1mConnections\x1b[0m\n\n")
	if len(snapshot.Connections) == 0 {
		b.WriteString("  No active connections.\n")
		return
	}
	for index, connection := range snapshot.Connections {
		marker := "  "
		if index == snapshot.SelectedConnection {
			marker = "➜ "
		}
		label := connection.Host
		if label == "" {
			label = connection.ID
		}
		fmt.Fprintf(b, " %s%-28s %-8s %-18s ↑%-8s ↓%-8s\n", marker,
			truncateTUI(label, 28), connection.Network, truncateTUI(connection.Chain, 18),
			formatBytes(connection.Upload), formatBytes(connection.Download))
		if connection.Process != "" {
			fmt.Fprintf(b, "    process: %s\n", truncateTUI(connection.Process, 70))
		}
	}
	b.WriteString("\n  d closes the selected connection; x closes all connections.\n")
}

func drawTUILogs(b *strings.Builder, snapshot tuiSnapshot) {
	b.WriteString("\n  \x1b[1mLogs\x1b[0m\n\n")
	start := 0
	if len(snapshot.Logs) > 18 {
		start = len(snapshot.Logs) - 18
	}
	for _, line := range snapshot.Logs[start:] {
		fmt.Fprintf(b, "  %s\n", truncateTUI(line, 74))
	}
	if len(snapshot.Logs) == 0 {
		b.WriteString("  No logs captured yet.\n")
	}
}

func drawTUISettings(b *strings.Builder, snapshot tuiSnapshot) {
	b.WriteString("\n  \x1b[1mCore settings\x1b[0m\n\n")
	fmt.Fprintf(b, "  Mode         %s\n", snapshot.Settings.Mode)
	fmt.Fprintf(b, "  Mixed port   %d\n", snapshot.Settings.MixedPort)
	fmt.Fprintf(b, "  Allow LAN    %t\n", snapshot.Settings.AllowLAN)
	fmt.Fprintf(b, "  IPv6         %t\n", snapshot.Settings.IPv6)
	fmt.Fprintf(b, "  Log level    %s\n", snapshot.Settings.LogLevel)
	fmt.Fprintf(b, "  TUN          %t\n", snapshot.Settings.TunEnabled)
	fmt.Fprintf(b, "  System proxy %t\n", snapshot.Settings.SystemProxy)
	b.WriteString("\n  Settings are updated through the same core update path used by FlClash.\n")
}

func drawTUIProfiles(b *strings.Builder, snapshot tuiSnapshot) {
	b.WriteString("\n  \x1b[1mProfiles\x1b[0m\n\n")
	if len(snapshot.Profiles) == 0 {
		b.WriteString("  No YAML profiles found in the data directory.\n")
		return
	}
	for i, profile := range snapshot.Profiles {
		marker := "  "
		if i == snapshot.SelectedRow {
			marker = "➜ "
		}
		current := ""
		if profile.Current {
			current = " \x1b[32m(active)\x1b[0m"
		}
		fmt.Fprintf(b, " %s%-32s%s\n", marker, truncateTUI(profile.Name, 32), current)
	}
	b.WriteString("\n  Enter activates; e edits; n downloads a subscription profile.\n")
}

func truncateTUI(value string, width int) string {
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	return string(runes[:width-1]) + "…"
}

func formatBytes(value int64) string {
	units := []string{"B", "KB", "MB", "GB"}
	amount := float64(value)
	unit := 0
	for amount >= 1024 && unit < len(units)-1 {
		amount /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", amount, units[unit])
}
