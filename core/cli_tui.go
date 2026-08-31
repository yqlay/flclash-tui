//go:build linux && !cgo && cli

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"
	"unicode/utf8"

	nethttp "net/http"

	"github.com/metacubex/mihomo/config"
	logrus "github.com/sirupsen/logrus"
	"golang.org/x/term"
)

type tuiGroup struct {
	Name   string
	Type   string
	Now    string
	Nodes  []string
	Delays map[string]tuiDelayResult
	Speeds map[string]tuiSpeedResult
}

type tuiPage int

const (
	tuiPageDashboard tuiPage = iota
	tuiPageProxies
	tuiPageProfiles
	tuiPageSSH
	tuiPageRequests
	tuiPageConnections
	tuiPageLogs
	tuiPageTools
	tuiPageMaintenance
	tuiPageCount
)

type tuiConnection struct {
	ID          string `json:"id"`
	Host        string `json:"host"`
	Process     string `json:"process,omitempty"`
	ProcessPath string `json:"process_path,omitempty"`
	UID         uint32 `json:"uid,omitempty"`
	Network     string `json:"network,omitempty"`
	Chain       string `json:"chain,omitempty"`
	Upload      int64  `json:"upload"`
	Download    int64  `json:"download"`
}

type tuiRequest struct {
	tuiConnection
	FirstSeen time.Time
	LastSeen  time.Time
	Active    bool
}

type tuiSettings struct {
	Mode          string
	MixedPort     int
	AllowLAN      bool
	IPv6          bool
	UnifiedDelay  bool
	TCPConcurrent bool
	LogLevel      string
	TunEnabled    bool
	TunScope      string
	SystemProxy   bool
}

type tuiNetworkInfo struct {
	PublicIP   string
	Country    string
	IntranetIP string
	Route      string
	Error      string
	Loading    bool
	CheckedAt  time.Time
}

type tuiMemoryInfo struct {
	SystemTotal  uint64
	SystemUsed   uint64
	ProcessRSS   uint64
	GoHeap       uint64
	CoreRSS      uint64
	ExternalCore bool
	Error        string
	CoreError    string
	UpdatedAt    time.Time
	CoreUpdated  time.Time
}

type tuiSpeedResult struct {
	Bytes          int64   `json:"bytes"`
	DurationMillis int64   `json:"duration_millis"`
	BytesPerSecond float64 `json:"bytes_per_second"`
	Complete       bool    `json:"complete"`
	Testing        bool    `json:"-"`
	Error          string  `json:"-"`
}

type tuiDelayResult struct {
	MedianMillis int    `json:"median_millis"`
	JitterMillis int    `json:"jitter_millis"`
	MinMillis    int    `json:"min_millis"`
	MaxMillis    int    `json:"max_millis"`
	Samples      int    `json:"samples"`
	Testing      bool   `json:"-"`
	Error        string `json:"-"`
}

type tuiUpdateInfo struct {
	LatestVersion string
	ReleaseURL    string
	Available     bool
	Loading       bool
	Error         string
	CheckedAt     time.Time
}

const (
	tuiSettingsModeRow = iota
	tuiSettingsFLCOutboundRow
	tuiSettingsMixedPortRow
	tuiSettingsAllowLANRow
	tuiSettingsIPv6Row
	tuiSettingsUnifiedDelayRow
	tuiSettingsTCPConcurrentRow
	tuiSettingsLogLevelRow
	tuiSettingsTunScopeRow
	tuiSettingsTunRow
	tuiSettingsServiceRow
	tuiSettingsSystemProxyRow
	tuiSettingsRowCount
)

const (
	tuiDashboardServiceRow = iota
	tuiDashboardSystemProxyRow
	tuiDashboardTunRow
	tuiDashboardModeRow
	tuiDashboardMixedPortRow
	tuiDashboardDelayRow
	tuiDashboardSpeedRow
	tuiDashboardRowCount
)

const (
	tuiToolsEditConfigRow = tuiSettingsRowCount + iota
	tuiToolsBackupRow
	tuiToolsRestoreRow
	tuiToolsGeoUpdateRow
	tuiToolsResetTrafficRow
	tuiToolsUpdateRow
	tuiToolsRowCount
)

const (
	tuiMaintenanceEditConfigRow = iota
	tuiMaintenanceBackupRow
	tuiMaintenanceRestoreRow
	tuiMaintenanceGeoUpdateRow
	tuiMaintenanceResetTrafficRow
	tuiMaintenanceUpdateRow
	tuiMaintenanceRowCount
)

const (
	tuiProxyViewGroups = iota
	tuiProxyViewProviders
	tuiProxyViewCount
)

type tuiProvider struct {
	Name      string
	Type      string
	Vehicle   string
	Count     int
	UpdatedAt string
}

type tuiProfile struct {
	Name            string
	Path            string
	Current         bool
	SubscriptionURL string
}

type tuiSSHProfile struct {
	Name        string
	Destination string
	Port        int
	LocalPort   int
	Identity    string
	PasswordSet bool
	Options     []string
	Connected   bool
	SocksPort   int
}

const (
	tuiSSHFormNameRow = iota
	tuiSSHFormDestinationRow
	tuiSSHFormPortRow
	tuiSSHFormLocalPortRow
	tuiSSHFormIdentityRow
	tuiSSHFormPasswordRow
	tuiSSHFormOptionStartRow
)

type tuiSSHFormView struct {
	Open              bool
	Existing          bool
	ReadOnly          bool
	Name              string
	Destination       string
	Port              int
	LocalPort         int
	Identity          string
	PasswordSet       bool
	PasswordChanged   bool
	PasswordCleared   bool
	Options           []string
	Selected          int
	FieldEditing      bool
	FieldInput        string
	PasswordConfirm   bool
	DeleteConfirmOpen bool
	DeleteName        string
}

type tuiProfileDeleteView struct {
	Open bool
	Name string
	Kind string
}

const (
	tuiProfileImportSubscriptionRow = -2
	tuiProfileImportFileRow         = -1
	tuiProfileImportRowCount        = 2
)

type tuiSnapshot struct {
	Page                   tuiPage
	Groups                 []tuiGroup
	GroupOrder             []string
	Traffic                trafficSnapshot
	TrafficHistory         []trafficSnapshot
	TotalTraffic           trafficSnapshot
	Connections            []tuiConnection
	Requests               []tuiRequest
	Logs                   []string
	Profiles               []tuiProfile
	SSHProfiles            []tuiSSHProfile
	Providers              []tuiProvider
	Settings               tuiSettings
	Network                tuiNetworkInfo
	Memory                 tuiMemoryInfo
	DashboardDelay         tuiDelayResult
	DashboardSpeed         tuiSpeedResult
	Update                 tuiUpdateInfo
	UpdatedAt              time.Time
	Status                 string
	SelectedGroup          int
	SelectedNode           int
	SelectedRow            int
	SelectedSSH            int
	SelectedProvider       int
	SelectedConnection     int
	SelectedRequest        int
	SelectedMenu           int
	SelectedDashboard      int
	SelectedSetting        int
	SelectedTool           int
	SelectedMaintenance    int
	DashboardScroll        int
	ProxyView              int
	ProxyNodeFocus         bool
	FocusSidebar           bool
	ShowHelp               bool
	ServiceRunning         bool
	ExternalCore           bool
	ManagedService         bool
	FLCEnabled             bool
	FLCOutbound            string
	ConfiguredProxyPort    int
	ActiveProxyPort        int
	Frontends              []cliProcessOwner
	InputTitle             string
	InputValue             string
	InputHint              string
	SelectionTitle         string
	SelectionOptions       []string
	SelectedOption         int
	SelectionHint          string
	Notifications          []tuiNotification
	NotificationDetailOpen bool
	NotificationSelected   int
	NotificationScroll     int
	SSHForm                tuiSSHFormView
	ProfileDelete          tuiProfileDeleteView
}

type trafficSnapshot struct {
	Up        int64 `json:"up"`
	Down      int64 `json:"down"`
	UpTotal   int64 `json:"upTotal"`
	DownTotal int64 `json:"downTotal"`
}

type tuiProxyResponse struct {
	Proxies map[string]struct {
		Type    string   `json:"type"`
		Now     string   `json:"now"`
		All     []string `json:"all"`
		History []struct {
			Delay int `json:"delay"`
		} `json:"history"`
	} `json:"proxies"`
}

type tuiConfigResponse struct {
	Mode          string `json:"mode"`
	MixedPort     int    `json:"mixed-port"`
	AllowLAN      bool   `json:"allow-lan"`
	IPv6          bool   `json:"ipv6"`
	UnifiedDelay  bool   `json:"unified-delay"`
	TCPConcurrent bool   `json:"tcp-concurrent"`
	LogLevel      string `json:"log-level"`
	Tun           struct {
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
ipv6: false
unified-delay: true
tcp-concurrent: true
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,PROXY
`

func tuiCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash [tui] [--config PATH] [--directory PATH]")
		fmt.Println("Open an interactive frontend connected to the per-user backend.")
		return nil
	}
	fs := flag.NewFlagSet("tui", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configArg := fs.String("config", "", "path to config.yaml")
	directoryArg := fs.String("directory", "", "FlClash data directory")
	controllerArg := fs.String("controller", "", "Mihomo external controller address (default: private Unix socket)")
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
	if *configArg == "" {
		if restoredPaths, restoreErr := restoreTUIActiveProfile(paths); restoreErr == nil {
			paths = restoredPaths
		}
	}
	if !isInteractiveTUI() {
		return errors.New("TUI requires an interactive terminal; use run or proxy commands in non-interactive shells")
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT)
	defer signal.Stop(interrupt)
	if *configArg != "" || *noStartArg {
		if err := ensureTUIConfig(paths, false); err != nil {
			return err
		}
	}
	originalLogOutput := logrus.StandardLogger().Out
	logrus.SetOutput(io.Discard)
	defer logrus.SetOutput(originalLogOutput)

	controllerAddress := *controllerArg
	controllerUnix := ""
	var service *tuiServiceClient
	coreRunning := false
	if *noStartArg && controllerAddress == "" {
		controllerAddress = "127.0.0.1:9090"
	}
	if !*noStartArg {
		var status tuiServiceStatus
		service, status, err = ensureTUIService(
			paths,
			*testURLArg,
			*configArg != "",
			*directoryArg != "",
		)
		if err != nil {
			if interrupted, shutdownErr := shutdownTUIServiceOnInterrupt(
				interrupt,
				service,
				paths,
			); interrupted {
				return shutdownErr
			}
			return err
		}
		if _, pathErr := tuiProfileStateKey(paths.homeDir, status.ConfigPath); pathErr != nil {
			return fmt.Errorf("background service returned an invalid profile path: %w", pathErr)
		}
		if status.HomeDir != "" {
			paths.homeDir = status.HomeDir
		}
		paths.configPath = status.ConfigPath
		controllerAddress = ""
		controllerUnix = status.CoreSocket
		coreRunning = status.Running
	}
	options := controllerOptions{
		address:    controllerAddress,
		unixSocket: controllerUnix,
		secret:     *secretArg,
	}
	client := controllerClient{
		options: options,
		client:  controllerHTTPClientForOptions(options, 750*time.Millisecond),
	}

	if !*noStartArg {
		if err := waitForController(client, 3*time.Second); err != nil {
			if interrupted, shutdownErr := shutdownTUIServiceOnInterrupt(
				interrupt,
				service,
				paths,
			); interrupted {
				return shutdownErr
			}
			return err
		}
	}
	if interrupted, shutdownErr := shutdownTUIServiceOnInterrupt(
		interrupt,
		service,
		paths,
	); interrupted {
		return shutdownErr
	}
	frontendSession, existingFrontends, err := registerCLIFrontend(
		paths.homeDir,
		paths.configPath,
	)
	if err != nil {
		return fmt.Errorf("register TUI frontend: %w", err)
	}
	defer frontendSession.close()
	runErr := runTUI(
		client,
		paths,
		nil,
		!*noStartArg,
		service,
		coreRunning,
		formatCLIFrontendNotice(existingFrontends),
		interrupt,
	)
	// Release the current frontend synchronously before tuiCommand returns.
	// close is idempotent, so the defer remains as an error-path safeguard.
	frontendSession.close()
	return runErr
}

func shutdownTUIServiceOnInterrupt(
	interrupt <-chan os.Signal,
	_ *tuiServiceClient,
	_ cliPaths,
) (bool, error) {
	select {
	case <-interrupt:
	default:
		return false, nil
	}
	return true, completeCLIExitForTUI(os.Getpid())
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
	return initializeCore(paths, testURL, controller, "", secret, true)
}

func initializeCore(
	paths cliPaths,
	testURL,
	controller,
	controllerUnix,
	secret string,
	startListeners bool,
) ([]byte, error) {
	configData, err := os.ReadFile(paths.configPath)
	if err != nil {
		return nil, fmt.Errorf("config file %q: %w", paths.configPath, err)
	}
	if err := os.MkdirAll(paths.homeDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	if err := ensureTUIBundledGeoData(paths.homeDir); err != nil {
		return nil, fmt.Errorf("prepare offline Geo data: %w", err)
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
	if !startListeners {
		handleStopListener()
	}

	setup := SetupParams{
		TestURL:     testURL,
		SelectedMap: loadTUISelectedProxies(paths.homeDir),
	}
	if controller != "" {
		setup.ExternalController = &controller
		if effectiveSecret != "" {
			setup.ExternalControllerSecret = &effectiveSecret
		}
	}
	if controllerUnix != "" {
		setup.ExternalControllerUnix = &controllerUnix
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
	if startListeners && !handleStartListener() {
		return nil, errors.New("start proxy listeners failed")
	}
	return setupParams, nil
}

func runLegacyTUI(client controllerClient, paths cliPaths, setupParams []byte, ownsCore bool) error {
	if !isInteractiveTUI() {
		return errors.New("TUI requires an interactive terminal; use run or proxy commands in non-interactive shells")
	}

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("enable raw terminal mode: %w", err)
	}
	enterTUIScreen()
	defer func() {
		_ = leaveTUIMode(oldState)
	}()

	logrus.SetOutput(io.Discard)
	handleStartLog()
	defer handleStopLog()
	snapshot := tuiSnapshot{
		Status:            "Loading...",
		GroupOrder:        loadTUIProxyGroupOrder(paths.configPath),
		SelectedGroup:     0,
		SelectedNode:      0,
		SelectedRow:       tuiProfileImportSubscriptionRow,
		SelectedMenu:      int(tuiPageDashboard),
		SelectedDashboard: tuiDashboardSystemProxyRow,
		FocusSidebar:      true,
	}
	refreshTUISnapshot(&snapshot, client)
	refreshTUIProfiles(&snapshot, paths)
	coreRunning := false
	systemProxyManaged := false
	screen := &tuiFrameWriter{writer: os.Stdout}
	draw := func() {
		drawTUI(screen, snapshot, paths, client.options.address, ownsCore, coreRunning)
	}
	shutdown := func() {
		if ownsCore && systemProxyManaged &&
			snapshot.Settings.SystemProxy &&
			linuxSystemProxyMatches(snapshot.Settings.MixedPort) {
			_ = setLinuxSystemProxy(snapshot.Settings.MixedPort, false)
			snapshot.Settings.SystemProxy = false
		}
		if ownsCore {
			handleShutdown()
		}
	}
	toggleCore := func() {
		if !ownsCore {
			snapshot.Status = "Core lifecycle is owned by the external process"
		} else if coreRunning {
			if handleStopListener() {
				coreRunning = false
				snapshot.Status = "Core listeners stopped"
				if systemProxyManaged && snapshot.Settings.SystemProxy {
					if !linuxSystemProxyMatches(snapshot.Settings.MixedPort) {
						snapshot.Settings.SystemProxy = false
						systemProxyManaged = false
						snapshot.Status += "; another instance owns the system proxy"
					} else if err := setLinuxSystemProxy(snapshot.Settings.MixedPort, false); err != nil {
						snapshot.Status += "; system proxy cleanup failed: " + err.Error()
					} else {
						snapshot.Settings.SystemProxy = false
						systemProxyManaged = false
					}
				}
			}
		} else if handleStartListener() {
			coreRunning = true
			snapshot.Status = "Core listeners started"
			refreshTUISnapshot(&snapshot, client)
		}
	}
	toggleSystemProxy := func() {
		autoStarted := false
		if ownsCore && !coreRunning {
			toggleCore()
			if !coreRunning {
				return
			}
			autoStarted = true
		}
		if toggleTUISystemProxy(&snapshot) {
			systemProxyManaged = snapshot.Settings.SystemProxy
			if autoStarted && snapshot.Settings.SystemProxy {
				snapshot.Status = fmt.Sprintf(
					"Core started on port %d; system proxy enabled",
					snapshot.Settings.MixedPort,
				)
			}
		}
	}
	draw()

	keys := make(chan tuiKey)
	keyHandled := make(chan struct{})
	go readTUIKeysSynchronized(os.Stdin, keys, keyHandled)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGWINCH)
	defer signal.Stop(signals)
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case receivedSignal := <-signals:
			switch receivedSignal {
			case syscall.SIGWINCH:
				screen.invalidate()
				draw()
				continue
			case syscall.SIGHUP:
				if !ownsCore {
					snapshot.Status = "Reload requires a core started by this process"
				} else if message := handleSetupConfig(setupParams); message != "" {
					snapshot.Status = "Reload failed: " + message
				} else {
					snapshot.Status = "Configuration reloaded"
					refreshTUISnapshot(&snapshot, client)
				}
				draw()
				continue
			default:
				shutdown()
				return nil
			}
		case key, open := <-keys:
			if !open {
				shutdown()
				return nil
			}
			if snapshot.ShowHelp && key != tuiKeyHelp && key != tuiKeyQuit {
				snapshot.ShowHelp = false
				draw()
				keyHandled <- struct{}{}
				continue
			}
			if handleTUIFocusNavigation(&snapshot, key) {
				draw()
				keyHandled <- struct{}{}
				continue
			}
			switch key {
			case tuiKeyQuit:
				shutdown()
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
			case tuiKeyCloseConnections:
				if snapshot.Page == tuiPageRequests {
					snapshot.Requests = nil
					snapshot.SelectedRequest = 0
					snapshot.Status = "History cleared"
					break
				}
				if snapshot.Page == tuiPageLogs {
					clearTUILogs()
					snapshot.Logs = nil
					snapshot.Status = "Logs cleared"
					break
				}
				if snapshot.Page != tuiPageConnections {
					break
				}
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
				toggleCore()
			case tuiKeyEdit:
				if snapshot.Page == tuiPageLogs {
					path, err := exportTUILogs(paths.homeDir, snapshot.Logs)
					if err != nil {
						snapshot.Status = "Export logs failed: " + err.Error()
					} else {
						snapshot.Status = "Logs exported: " + path
					}
					break
				}
				if snapshot.Page == tuiPageProfiles &&
					(snapshot.SelectedRow < 0 || snapshot.SelectedRow >= len(snapshot.Profiles)) {
					snapshot.Status = "Select a profile before editing its YAML"
					break
				}
				if snapshot.Page != tuiPageProfiles && snapshot.Page != tuiPageTools {
					snapshot.Status = "Edit YAML is available in Profiles and Maintenance"
					break
				}
				screen.invalidate()
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
				} else {
					snapshot.Status = "Configuration saved; reload the external core to apply it"
				}
			case tuiKeyNewProfile:
				if snapshot.Page == tuiPageProfiles {
					screen.invalidate()
					if err := addTUIProfile(paths.homeDir, &oldState); err != nil {
						if errors.Is(err, errTUIActionCancelled) {
							snapshot.Status = "Profile download cancelled"
						} else {
							snapshot.Status = "Add profile failed: " + err.Error()
						}
					} else {
						snapshot.Status = "Profile downloaded"
						refreshTUIProfiles(&snapshot, paths)
					}
				}
			case tuiKeyProviders:
				snapshot.Page = tuiPageProxies
				snapshot.SelectedMenu = int(tuiPageProxies)
				snapshot.FocusSidebar = false
				snapshot.ProxyView = tuiProxyViewProviders
			case tuiKeyBackup:
				if snapshot.Page != tuiPageTools {
					break
				}
				executeTUITool(1, &snapshot, paths, setupParams, client, ownsCore, &oldState)
			case tuiKeyRestore:
				if snapshot.Page != tuiPageTools {
					break
				}
				executeTUITool(2, &snapshot, paths, setupParams, client, ownsCore, &oldState)
			case tuiKeyGeoUpdate:
				if snapshot.Page != tuiPageTools {
					break
				}
				executeTUITool(3, &snapshot, paths, setupParams, client, ownsCore, &oldState)
			case tuiKeyResetTraffic:
				if snapshot.Page != tuiPageTools {
					break
				}
				executeTUITool(4, &snapshot, paths, setupParams, client, ownsCore, &oldState)
			case tuiKeyUp:
				if snapshot.Page == tuiPageProfiles {
					moveTUIProfile(&snapshot, -1)
				} else if snapshot.Page == tuiPageRequests {
					snapshot.SelectedRequest = wrapTUIIndex(snapshot.SelectedRequest, -1, len(snapshot.Requests))
				} else if snapshot.Page == tuiPageConnections {
					moveTUIConnection(&snapshot, -1)
				} else if snapshot.Page == tuiPageProxies {
					if snapshot.ProxyView == tuiProxyViewProviders {
						moveTUIProvider(&snapshot, -1)
					} else {
						moveTUIGroup(&snapshot, -1)
					}
				} else if snapshot.Page == tuiPageDashboard {
					snapshot.SelectedDashboard = wrapTUIIndex(snapshot.SelectedDashboard, -1, tuiDashboardRowCount)
				} else if snapshot.Page == tuiPageTools {
					snapshot.SelectedTool = wrapTUIIndex(snapshot.SelectedTool, -1, tuiToolsRowCount)
				}
			case tuiKeyDown:
				if snapshot.Page == tuiPageProfiles {
					moveTUIProfile(&snapshot, 1)
				} else if snapshot.Page == tuiPageRequests {
					snapshot.SelectedRequest = wrapTUIIndex(snapshot.SelectedRequest, 1, len(snapshot.Requests))
				} else if snapshot.Page == tuiPageConnections {
					moveTUIConnection(&snapshot, 1)
				} else if snapshot.Page == tuiPageProxies {
					if snapshot.ProxyView == tuiProxyViewProviders {
						moveTUIProvider(&snapshot, 1)
					} else {
						moveTUIGroup(&snapshot, 1)
					}
				} else if snapshot.Page == tuiPageDashboard {
					snapshot.SelectedDashboard = wrapTUIIndex(snapshot.SelectedDashboard, 1, tuiDashboardRowCount)
				} else if snapshot.Page == tuiPageTools {
					snapshot.SelectedTool = wrapTUIIndex(snapshot.SelectedTool, 1, tuiToolsRowCount)
				}
			case tuiKeyDelayTest:
				if snapshot.Page == tuiPageProxies &&
					snapshot.ProxyView == tuiProxyViewGroups &&
					snapshot.SelectedGroup >= 0 &&
					snapshot.SelectedGroup < len(snapshot.Groups) {
					group := snapshot.Groups[snapshot.SelectedGroup]
					if snapshot.SelectedNode >= 0 &&
						snapshot.SelectedNode < len(group.Nodes) {
						node := group.Nodes[snapshot.SelectedNode]
						delay, err := client.testProxyDelay(
							node,
							"https://www.gstatic.com/generate_204",
						)
						if err != nil {
							snapshot.Status = "Delay test failed: " + err.Error()
						} else {
							snapshot.Status = fmt.Sprintf("%s delay: %d ms", node, delay)
						}
					}
				}
			case tuiKeyViewPrevious, tuiKeyViewNext:
				if !snapshot.FocusSidebar && snapshot.Page == tuiPageProxies {
					delta := 1
					if key == tuiKeyViewPrevious {
						delta = -1
					}
					snapshot.ProxyView = wrapTUIIndex(snapshot.ProxyView, delta, tuiProxyViewCount)
				}
			case tuiKeySelect:
				if snapshot.Page == tuiPageDashboard {
					switch snapshot.SelectedDashboard {
					case tuiDashboardServiceRow:
						toggleCore()
					case tuiDashboardSystemProxyRow:
						toggleSystemProxy()
					case tuiDashboardTunRow:
						updateTUISettings(&snapshot, client, tuiKeyTun)
					case tuiDashboardModeRow:
						updateTUISettings(&snapshot, client, tuiKeyMode)
					case tuiDashboardMixedPortRow:
						screen.invalidate()
						setTUIMixedPort(&snapshot, client, &oldState)
					case tuiDashboardDelayRow:
						snapshot.Status = "Dashboard route delay testing requires the managed TUI"
					case tuiDashboardSpeedRow:
						snapshot.Status = "Dashboard route speed testing requires the managed TUI"
					}
				} else if snapshot.Page == tuiPageProxies {
					if snapshot.ProxyView == tuiProxyViewProviders {
						updateTUIProvider(&snapshot, client)
					} else {
						selectTUIProxy(&snapshot, client, paths.homeDir)
					}
				} else if snapshot.Page == tuiPageProfiles {
					switchTUIProfile(&snapshot, &paths, &setupParams, client, ownsCore, coreRunning)
				} else if snapshot.Page == tuiPageTools {
					switch snapshot.SelectedTool {
					case tuiSettingsModeRow:
						updateTUISettings(&snapshot, client, tuiKeyMode)
					case tuiSettingsMixedPortRow:
						screen.invalidate()
						setTUIMixedPort(&snapshot, client, &oldState)
					case tuiSettingsAllowLANRow:
						updateTUISettings(&snapshot, client, tuiKeyAllowLAN)
					case tuiSettingsIPv6Row:
						updateTUISettings(&snapshot, client, tuiKeyIPv6)
					case tuiSettingsUnifiedDelayRow:
						updateTUISettings(&snapshot, client, tuiKeyUnifiedDelay)
					case tuiSettingsTCPConcurrentRow:
						updateTUISettings(&snapshot, client, tuiKeyTCPConcurrent)
					case tuiSettingsLogLevelRow:
						updateTUISettings(&snapshot, client, tuiKeyLogLevel)
					case tuiSettingsTunScopeRow:
						snapshot.Status = "TUN scope changes require the managed Backend"
					case tuiSettingsTunRow:
						updateTUISettings(&snapshot, client, tuiKeyTun)
					case tuiSettingsServiceRow:
						toggleCore()
					case tuiSettingsSystemProxyRow:
						toggleSystemProxy()
					case tuiToolsEditConfigRow:
						screen.invalidate()
						executeTUITool(0, &snapshot, paths, setupParams, client, ownsCore, &oldState)
					case tuiToolsBackupRow:
						executeTUITool(1, &snapshot, paths, setupParams, client, ownsCore, &oldState)
					case tuiToolsRestoreRow:
						executeTUITool(2, &snapshot, paths, setupParams, client, ownsCore, &oldState)
					case tuiToolsGeoUpdateRow:
						executeTUITool(3, &snapshot, paths, setupParams, client, ownsCore, &oldState)
					case tuiToolsResetTrafficRow:
						executeTUITool(4, &snapshot, paths, setupParams, client, ownsCore, &oldState)
					case tuiToolsUpdateRow:
						executeTUITool(5, &snapshot, paths, setupParams, client, ownsCore, &oldState)
					}
				}
			case tuiKeyAllowLAN, tuiKeyIPv6, tuiKeyUnifiedDelay, tuiKeyTCPConcurrent,
				tuiKeyTun, tuiKeyMode, tuiKeyLogLevel, tuiKeyPortUp, tuiKeyPortDown:
				if snapshot.Page == tuiPageTools || snapshot.Page == tuiPageDashboard {
					updateTUISettings(&snapshot, client, key)
				}
			case tuiKeySetPort:
				if snapshot.Page == tuiPageTools || snapshot.Page == tuiPageDashboard {
					screen.invalidate()
					setTUIMixedPort(&snapshot, client, &oldState)
				}
			case tuiKeySystemProxy:
				if snapshot.Page == tuiPageTools || snapshot.Page == tuiPageDashboard {
					toggleSystemProxy()
				}
			}
			draw()
			keyHandled <- struct{}{}
		case <-ticker.C:
			refreshTUISnapshot(&snapshot, client)
			refreshTUIProfiles(&snapshot, paths)
			draw()
		}
	}
}

func backupTUIConfig(configPath string) (string, error) {
	lease, err := acquireTUIProfileLocks(filepath.Dir(configPath), configPath)
	if err != nil {
		return "", err
	}
	defer lease.release()
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

func executeTUITool(
	index int,
	snapshot *tuiSnapshot,
	paths cliPaths,
	setupParams []byte,
	client controllerClient,
	ownsCore bool,
	oldState **term.State,
) {
	switch index {
	case 0:
		if err := runTUIEditor(paths.configPath, oldState); err != nil {
			snapshot.Status = "Editor failed: " + err.Error()
		} else if ownsCore {
			if message := handleSetupConfig(setupParams); message != "" {
				snapshot.Status = "Edited config is invalid: " + message
			} else {
				snapshot.Status = "Configuration applied"
			}
		} else {
			snapshot.Status = "Configuration saved; reload the external core to apply it"
		}
	case 1:
		if backupPath, err := backupTUIConfig(paths.configPath); err != nil {
			snapshot.Status = "Backup failed: " + err.Error()
		} else {
			snapshot.Status = "Backup created: " + filepath.Base(backupPath)
		}
	case 2:
		if backupPath, err := restoreLatestTUIConfig(paths.configPath); err != nil {
			snapshot.Status = "Restore failed: " + err.Error()
		} else if ownsCore {
			if message := handleSetupConfig(setupParams); message != "" {
				snapshot.Status = "Restore applied with errors: " + message
			} else {
				snapshot.Status = "Restored: " + filepath.Base(backupPath)
			}
		} else {
			snapshot.Status = "Restored: " + filepath.Base(backupPath) + "; reload the external core to apply it"
		}
	case 3:
		if err := client.updateGeo(); err != nil {
			snapshot.Status = "Geo update failed: " + err.Error()
		} else {
			snapshot.Status = "Geo databases update started"
		}
	case 4:
		if ownsCore {
			handleResetTraffic()
			snapshot.Status = "Traffic counters reset"
		} else {
			snapshot.Status = "Traffic reset requires a core started by this process"
		}
	case 5:
		checkTUIUpdate(snapshot)
	}
}

func checkTUIUpdate(snapshot *tuiSnapshot) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	release, err := fetchLatestCLIRelease(
		ctx,
		cliUpdateHTTPClient,
		cliLatestReleaseAPIURL,
	)
	snapshot.Update.Loading = false
	snapshot.Update.CheckedAt = time.Now()
	if err != nil {
		snapshot.Update.Error = err.Error()
		snapshot.Status = "Update check failed: " + err.Error()
		return
	}
	latestVersion := normalizeCLIVersion(release.TagName)
	if latestVersion == "" {
		snapshot.Update.Error = "invalid release version " + release.TagName
		snapshot.Status = "Update check failed: invalid release version"
		return
	}
	snapshot.Update.Error = ""
	snapshot.Update.LatestVersion = latestVersion
	snapshot.Update.ReleaseURL = release.HTMLURL
	snapshot.Update.Available = isNewerCLIVersion(latestVersion, cliVersion)
	if snapshot.Update.Available {
		snapshot.Status = fmt.Sprintf(
			"v%s available · %s · quit and run: flclash update",
			latestVersion,
			cliUpdateWarning,
		)
		return
	}
	snapshot.Status = fmt.Sprintf(
		"v%s is current · %s",
		cliVersion,
		cliUpdateWarning,
	)
}

func restoreLatestTUIConfig(configPath string) (string, error) {
	backupPath, backup, err := restoreLatestTUIConfigLocked(
		filepath.Dir(configPath),
		configPath,
	)
	backup.release()
	return backupPath, err
}

func restoreLatestTUIConfigLocked(
	homeDir,
	configPath string,
) (string, tuiProfileBackup, error) {
	lease, err := acquireTUIProfileLocks(homeDir, configPath)
	if err != nil {
		return "", tuiProfileBackup{}, err
	}
	releaseOnError := true
	defer func() {
		if releaseOnError {
			lease.release()
		}
	}()
	original, err := os.ReadFile(configPath)
	if err != nil {
		return "", tuiProfileBackup{}, err
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return "", tuiProfileBackup{}, err
	}
	entries, err := os.ReadDir(filepath.Dir(configPath))
	if err != nil {
		return "", tuiProfileBackup{}, err
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
		return "", tuiProfileBackup{}, errors.New("no config backup found")
	}
	data, err := os.ReadFile(latest)
	if err != nil {
		return "", tuiProfileBackup{}, err
	}
	if message := validateConfigBytes(data); message != "" {
		return "", tuiProfileBackup{}, fmt.Errorf("restored config is invalid: %s", message)
	}
	if err := writeTUIProfileAtomically(configPath, data, info.Mode()); err != nil {
		return "", tuiProfileBackup{}, err
	}
	backup := tuiProfileBackup{
		data:          original,
		mode:          info.Mode(),
		updatedSHA256: tuiBytesSHA256(data),
		lock:          lease,
	}
	releaseOnError = false
	return latest, backup, nil
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
	selectedGroupName := ""
	selectedNodeName := ""
	if snapshot.SelectedGroup >= 0 && snapshot.SelectedGroup < len(snapshot.Groups) {
		selectedGroup := snapshot.Groups[snapshot.SelectedGroup]
		selectedGroupName = selectedGroup.Name
		if snapshot.SelectedNode >= 0 && snapshot.SelectedNode < len(selectedGroup.Nodes) {
			selectedNodeName = selectedGroup.Nodes[snapshot.SelectedNode]
		}
	}
	selectedConnectionID := ""
	if snapshot.SelectedConnection >= 0 && snapshot.SelectedConnection < len(snapshot.Connections) {
		selectedConnectionID = snapshot.Connections[snapshot.SelectedConnection].ID
	}
	selectedRequestID := ""
	if snapshot.SelectedRequest >= 0 && snapshot.SelectedRequest < len(snapshot.Requests) {
		selectedRequestID = snapshot.Requests[snapshot.SelectedRequest].ID
	}
	selectedProviderName := ""
	if snapshot.SelectedProvider >= 0 && snapshot.SelectedProvider < len(snapshot.Providers) {
		selectedProviderName = snapshot.Providers[snapshot.SelectedProvider].Name
	}

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
		delays := make(map[string]tuiDelayResult, len(proxy.All))
		for _, node := range proxy.All {
			nodeProxy, exists := response.Proxies[node]
			if !exists || len(nodeProxy.History) == 0 {
				continue
			}
			delay := nodeProxy.History[len(nodeProxy.History)-1].Delay
			if delay > 0 {
				delays[node] = tuiDelayResult{
					MedianMillis: delay,
					MinMillis:    delay,
					MaxMillis:    delay,
					Samples:      1,
				}
			}
		}
		groups = append(groups, tuiGroup{
			Name:   name,
			Type:   proxy.Type,
			Now:    proxy.Now,
			Nodes:  proxy.All,
			Delays: delays,
			Speeds: map[string]tuiSpeedResult{},
		})
	}
	orderTUIGroups(groups, snapshot.GroupOrder)
	snapshot.Groups = groups
	snapshot.SelectedGroup = findTUIGroup(groups, selectedGroupName)
	if len(groups) > 0 {
		group := groups[snapshot.SelectedGroup]
		if group.Name != selectedGroupName {
			selectedNodeName = ""
		}
		snapshot.SelectedNode = findTUIString(group.Nodes, selectedNodeName)
		if selectedNodeName == "" {
			snapshot.SelectedNode = findTUIString(group.Nodes, group.Now)
		}
	}

	if connections, err := client.request("GET", "/connections", nil); err == nil {
		var value struct {
			Connections []struct {
				ID       string `json:"id"`
				Metadata struct {
					Host            string `json:"host"`
					DestinationIP   string `json:"destinationIP"`
					DestinationPort string `json:"destinationPort"`
					Process         string `json:"process"`
					ProcessPath     string `json:"processPath"`
					UID             uint32 `json:"uid"`
					Network         string `json:"network"`
				} `json:"metadata"`
				Upload   int64    `json:"upload"`
				Download int64    `json:"download"`
				Chains   []string `json:"chains"`
			} `json:"connections"`
		}
		if json.Unmarshal(connections, &value) == nil {
			activeConnections := make([]tuiConnection, 0, len(value.Connections))
			systemTun := snapshot.Settings.TunEnabled && snapshot.Settings.TunScope == tuiTunScopeSystem
			for _, item := range value.Connections {
				if snapshot.ManagedService && !systemTun && item.Metadata.UID != uint32(os.Getuid()) {
					continue
				}
				chain := "DIRECT"
				if len(item.Chains) > 0 {
					chain = item.Chains[len(item.Chains)-1]
				}
				host := item.Metadata.Host
				if host == "" {
					host = formatTUIDestination(
						item.Metadata.DestinationIP,
						item.Metadata.DestinationPort,
					)
				}
				activeConnections = append(activeConnections, tuiConnection{
					ID: item.ID, Host: host, Process: item.Metadata.Process,
					ProcessPath: item.Metadata.ProcessPath, UID: item.Metadata.UID,
					Network: item.Metadata.Network, Chain: chain,
					Upload: item.Upload, Download: item.Download,
				})
			}
			snapshot.Requests = updateTUIRequestHistory(
				snapshot.Requests,
				activeConnections,
				time.Now(),
			)
			snapshot.Connections = activeConnections
			snapshot.SelectedConnection = findTUIConnection(snapshot.Connections, selectedConnectionID)
			snapshot.SelectedRequest = findTUIRequest(snapshot.Requests, selectedRequestID)
		}
	}
	systemProxyEnabled := snapshot.Settings.SystemProxy
	checkSystemProxy := snapshot.UpdatedAt.IsZero()
	if config, err := client.request("GET", "/configs", nil); err == nil {
		var value tuiConfigResponse
		if json.Unmarshal(config, &value) == nil {
			snapshot.Settings = tuiSettings{
				Mode: value.Mode, MixedPort: value.MixedPort, AllowLAN: value.AllowLAN,
				IPv6: value.IPv6, UnifiedDelay: value.UnifiedDelay,
				TCPConcurrent: value.TCPConcurrent, LogLevel: value.LogLevel,
				TunEnabled:  value.Tun.Enable,
				SystemProxy: systemProxyEnabled,
			}
		}
	}
	if checkSystemProxy {
		snapshot.Settings.SystemProxy = linuxSystemProxyMatches(
			snapshot.Settings.MixedPort,
		)
	}
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
			snapshot.SelectedProvider = findTUIProvider(snapshot.Providers, selectedProviderName)
		}
	}
	snapshot.Logs = cliLogSnapshot()
	if snapshot.Status == "" || snapshot.Status == "Loading..." ||
		snapshot.Status == "Connected" ||
		strings.HasPrefix(snapshot.Status, "Controller unavailable:") ||
		strings.HasPrefix(snapshot.Status, "Invalid controller response:") {
		snapshot.Status = "Connected"
	}
	snapshot.UpdatedAt = time.Now()
}

const tuiRequestHistoryLimit = 500

func updateTUIRequestHistory(
	history []tuiRequest,
	active []tuiConnection,
	now time.Time,
) []tuiRequest {
	updated := append([]tuiRequest(nil), history...)
	indexByID := make(map[string]int, len(updated))
	for index := range updated {
		updated[index].Active = false
		if updated[index].ID != "" {
			indexByID[updated[index].ID] = index
		}
	}
	for _, connection := range active {
		index, exists := indexByID[connection.ID]
		if exists && connection.ID != "" {
			updated[index].tuiConnection = connection
			updated[index].LastSeen = now
			updated[index].Active = true
			continue
		}
		updated = append(updated, tuiRequest{
			tuiConnection: connection,
			FirstSeen:     now,
			LastSeen:      now,
			Active:        true,
		})
		if connection.ID != "" {
			indexByID[connection.ID] = len(updated) - 1
		}
	}
	sort.SliceStable(updated, func(i, j int) bool {
		return updated[i].LastSeen.After(updated[j].LastSeen)
	})
	if len(updated) > tuiRequestHistoryLimit {
		updated = updated[:tuiRequestHistoryLimit]
	}
	return updated
}

func refreshTUIProfiles(snapshot *tuiSnapshot, paths cliPaths) {
	selectedImportRow := snapshot.SelectedRow
	importSelected := selectedImportRow < 0
	selectedProfilePath := ""
	if snapshot.SelectedRow >= 0 && snapshot.SelectedRow < len(snapshot.Profiles) {
		selectedProfilePath = snapshot.Profiles[snapshot.SelectedRow].Path
	}
	entries, err := os.ReadDir(paths.homeDir)
	if err != nil {
		snapshot.Profiles = nil
		return
	}
	profiles := make([]tuiProfile, 0, len(entries)+1)
	subscriptionSources := loadTUISubscriptionSources(paths.homeDir)
	currentFound := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if isTUIRuntimeProfileName(entry.Name()) {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(paths.homeDir, entry.Name())
		current := filepath.Clean(path) == filepath.Clean(paths.configPath)
		currentFound = currentFound || current
		subscriptionURL := ""
		if stateKey, err := tuiProfileStateKey(paths.homeDir, path); err == nil {
			subscriptionURL = subscriptionSources[stateKey]
		}
		profiles = append(profiles, tuiProfile{
			Name:            entry.Name(),
			Path:            path,
			Current:         current,
			SubscriptionURL: subscriptionURL,
		})
	}
	if !currentFound {
		subscriptionURL := ""
		if stateKey, err := tuiProfileStateKey(
			paths.homeDir,
			paths.configPath,
		); err == nil {
			subscriptionURL = subscriptionSources[stateKey]
		}
		profiles = append(profiles, tuiProfile{
			Name:            filepath.Base(paths.configPath),
			Path:            paths.configPath,
			Current:         true,
			SubscriptionURL: subscriptionURL,
		})
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	snapshot.Profiles = profiles
	if importSelected {
		snapshot.SelectedRow = selectedImportRow
		if snapshot.SelectedRow < tuiProfileImportSubscriptionRow ||
			snapshot.SelectedRow > tuiProfileImportFileRow {
			snapshot.SelectedRow = tuiProfileImportSubscriptionRow
		}
		return
	}
	snapshot.SelectedRow = 0
	for index, profile := range profiles {
		if filepath.Clean(profile.Path) == filepath.Clean(selectedProfilePath) {
			snapshot.SelectedRow = index
			break
		}
	}
}

func refreshTUISSH(snapshot *tuiSnapshot) {
	selectedName := ""
	if snapshot.SelectedSSH >= 0 && snapshot.SelectedSSH < len(snapshot.SSHProfiles) {
		selectedName = snapshot.SSHProfiles[snapshot.SelectedSSH].Name
	}
	views, err := loadCLISSHProfileViews()
	if err != nil {
		if snapshot.Page == tuiPageSSH {
			snapshot.Status = "SSH profiles unavailable: " + err.Error()
		}
		return
	}
	snapshot.SSHProfiles = make([]tuiSSHProfile, 0, len(views))
	for _, view := range views {
		snapshot.SSHProfiles = append(snapshot.SSHProfiles, tuiSSHProfile{
			Name:        view.Name,
			Destination: view.Destination,
			Port:        view.Port,
			LocalPort:   view.LocalPort,
			Identity:    view.Identity,
			PasswordSet: view.PasswordSet,
			Connected:   view.Connected,
			SocksPort:   view.SocksPort,
			Options:     append([]string(nil), view.Options...),
		})
	}
	snapshot.SelectedSSH = 0
	for index, profile := range snapshot.SSHProfiles {
		if strings.EqualFold(profile.Name, selectedName) {
			snapshot.SelectedSSH = index
			break
		}
	}
}

func findTUIGroup(groups []tuiGroup, name string) int {
	for index, group := range groups {
		if group.Name == name {
			return index
		}
	}
	return 0
}

func findTUIString(values []string, value string) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 0
}

func findTUIConnection(connections []tuiConnection, id string) int {
	for index, connection := range connections {
		if connection.ID == id {
			return index
		}
	}
	return 0
}

func findTUIRequest(requests []tuiRequest, id string) int {
	for index, request := range requests {
		if request.ID == id {
			return index
		}
	}
	return 0
}

func findTUIProvider(providers []tuiProvider, name string) int {
	for index, provider := range providers {
		if provider.Name == name {
			return index
		}
	}
	return 0
}

func switchTUIProfile(
	snapshot *tuiSnapshot,
	paths *cliPaths,
	setupParams *[]byte,
	client controllerClient,
	ownsCore,
	startListeners bool,
) {
	if !ownsCore {
		snapshot.Status = "Profile switching requires a core started by this process"
		return
	}
	if snapshot.SelectedRow < 0 || snapshot.SelectedRow >= len(snapshot.Profiles) {
		return
	}
	profile := snapshot.Profiles[snapshot.SelectedRow]
	if profile.Current {
		snapshot.Status = "Profile is already active"
		return
	}
	if message := handleValidateConfig(profile.Path); message != "" {
		snapshot.Status = "Profile invalid: " + message
		return
	}
	if err := ensureTUIFlClashDefaults(profile.Path); err != nil {
		snapshot.Status = "Profile defaults failed: " + err.Error()
		return
	}
	previousPaths := *paths
	previousSetupParams := append([]byte(nil), (*setupParams)...)
	systemProxyEnabled := snapshot.Settings.SystemProxy
	rollback := func() string {
		initParams, err := json.Marshal(InitParams{
			HomeDir: previousPaths.homeDir, ConfigPath: previousPaths.configPath, Version: 1,
		})
		if err != nil || !handleInitClash(string(initParams)) {
			return "previous profile initialization failed"
		}
		if message := handleSetupConfig(previousSetupParams); message != "" {
			return "previous profile reload failed: " + message
		}
		if startListeners && !handleStartListener() {
			return "previous profile listener restart failed"
		}
		return ""
	}
	controller := client.options.address
	controllerUnix := client.options.unixSocket
	secret := client.options.secret
	params := defaultSetupParams()
	if len(*setupParams) > 0 {
		if err := UnmarshalJson(*setupParams, params); err != nil {
			snapshot.Status = "Profile setup failed: " + err.Error()
			return
		}
	}
	params.SelectedMap = loadTUISelectedProxies(paths.homeDir)
	if controllerUnix != "" {
		params.ExternalController = nil
		params.ExternalControllerUnix = &controllerUnix
	} else {
		params.ExternalController = &controller
		params.ExternalControllerUnix = nil
	}
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
		if rollbackMessage := rollback(); rollbackMessage != "" {
			snapshot.Status += "; rollback failed: " + rollbackMessage
		}
		return
	}
	if startListeners && !handleStartListener() {
		snapshot.Status = "Profile listener start failed"
		if rollbackMessage := rollback(); rollbackMessage != "" {
			snapshot.Status += "; rollback failed: " + rollbackMessage
		}
		return
	}
	paths.configPath = profile.Path
	*setupParams = newSetupParams
	snapshot.GroupOrder = loadTUIProxyGroupOrder(profile.Path)
	snapshot.ProxyNodeFocus = false
	snapshot.Status = "Active profile: " + profile.Name
	if err := rememberTUIActiveProfile(*paths); err != nil {
		snapshot.Status += "; could not remember profile: " + err.Error()
	}
	refreshTUISnapshot(snapshot, client)
	if systemProxyEnabled {
		enableSystemProxy := snapshot.Settings.MixedPort > 0
		if err := setLinuxSystemProxy(snapshot.Settings.MixedPort, enableSystemProxy); err != nil {
			snapshot.Status += "; system proxy update failed: " + err.Error()
		} else {
			snapshot.Settings.SystemProxy = enableSystemProxy
			if !enableSystemProxy {
				snapshot.Status += "; System proxy disabled because the profile has no Proxy port"
			}
		}
	}
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

func loadTUIProxyGroupOrder(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	rawConfig, err := config.UnmarshalRawConfig(data)
	if err != nil {
		return nil
	}
	order := make([]string, 0, len(rawConfig.ProxyGroup))
	for _, group := range rawConfig.ProxyGroup {
		name, ok := group["name"].(string)
		if ok && name != "" {
			order = append(order, name)
		}
	}
	return order
}

func orderTUIGroups(groups []tuiGroup, order []string) {
	positions := make(map[string]int, len(order))
	for index, name := range order {
		positions[name] = index
	}
	sort.SliceStable(groups, func(i, j int) bool {
		left, leftKnown := positions[groups[i].Name]
		right, rightKnown := positions[groups[j].Name]
		switch {
		case leftKnown && rightKnown:
			return left < right
		case leftKnown:
			return true
		case rightKnown:
			return false
		default:
			return groups[i].Name < groups[j].Name
		}
	})
}

func moveTUIGroup(snapshot *tuiSnapshot, delta int) {
	if len(snapshot.Groups) == 0 {
		return
	}
	snapshot.SelectedGroup = wrapTUIIndex(snapshot.SelectedGroup, delta, len(snapshot.Groups))
	snapshot.SelectedNode = findTUIString(
		snapshot.Groups[snapshot.SelectedGroup].Nodes,
		snapshot.Groups[snapshot.SelectedGroup].Now,
	)
}

func moveTUINode(snapshot *tuiSnapshot, delta int) {
	if snapshot.SelectedGroup < 0 || snapshot.SelectedGroup >= len(snapshot.Groups) {
		return
	}
	nodes := snapshot.Groups[snapshot.SelectedGroup].Nodes
	if len(nodes) == 0 {
		return
	}
	snapshot.SelectedNode = wrapTUIIndex(snapshot.SelectedNode, delta, len(nodes))
}

func moveTUIProvider(snapshot *tuiSnapshot, delta int) {
	if len(snapshot.Providers) == 0 {
		return
	}
	snapshot.SelectedProvider = wrapTUIIndex(snapshot.SelectedProvider, delta, len(snapshot.Providers))
}

func moveTUIProfile(snapshot *tuiSnapshot, delta int) {
	position := snapshot.SelectedRow + tuiProfileImportRowCount
	position = wrapTUIIndex(
		position,
		delta,
		len(snapshot.Profiles)+tuiProfileImportRowCount,
	)
	snapshot.SelectedRow = position - tuiProfileImportRowCount
}

func moveTUIConnection(snapshot *tuiSnapshot, delta int) {
	if len(snapshot.Connections) == 0 {
		return
	}
	snapshot.SelectedConnection = wrapTUIIndex(snapshot.SelectedConnection, delta, len(snapshot.Connections))
}

func wrapTUIIndex(current, delta, total int) int {
	if total <= 0 {
		return 0
	}
	current %= total
	if current < 0 {
		current += total
	}
	next := (current + delta) % total
	if next < 0 {
		next += total
	}
	return next
}

func selectTUIProxy(
	snapshot *tuiSnapshot,
	client controllerClient,
	homeDir string,
) {
	if snapshot.SelectedGroup < 0 || snapshot.SelectedGroup >= len(snapshot.Groups) {
		return
	}
	group := snapshot.Groups[snapshot.SelectedGroup]
	if snapshot.SelectedNode < 0 || snapshot.SelectedNode >= len(group.Nodes) {
		return
	}
	if err := client.setProxy(group.Name, group.Nodes[snapshot.SelectedNode]); err != nil {
		snapshot.Status = "Switch failed: " + err.Error()
		return
	}
	snapshot.Status = fmt.Sprintf("Switched %s to %s", group.Name, group.Nodes[snapshot.SelectedNode])
	if err := rememberTUIProxySelection(
		homeDir,
		group.Name,
		group.Nodes[snapshot.SelectedNode],
	); err != nil {
		snapshot.Status += "; selection save failed: " + err.Error()
	}
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

func updateTUISettings(snapshot *tuiSnapshot, client controllerClient, key tuiKey) bool {
	systemProxyEnabled := snapshot.Settings.SystemProxy
	patch := map[string]interface{}{}
	switch key {
	case tuiKeyAllowLAN:
		patch["allow-lan"] = !snapshot.Settings.AllowLAN
	case tuiKeyIPv6:
		patch["ipv6"] = !snapshot.Settings.IPv6
	case tuiKeyUnifiedDelay:
		patch["unified-delay"] = !snapshot.Settings.UnifiedDelay
	case tuiKeyTCPConcurrent:
		patch["tcp-concurrent"] = !snapshot.Settings.TCPConcurrent
	case tuiKeyTun:
		patch["tun"] = map[string]bool{"enable": !snapshot.Settings.TunEnabled}
	case tuiKeyLogLevel:
		levels := []string{"silent", "error", "warning", "info", "debug"}
		current := findTUIString(levels, strings.ToLower(snapshot.Settings.LogLevel))
		patch["log-level"] = levels[wrapTUIIndex(current, 1, len(levels))]
	case tuiKeyPortUp:
		if snapshot.Settings.MixedPort >= 65535 {
			snapshot.Status = "Proxy port is already at 65535"
			return false
		}
		patch["mixed-port"] = snapshot.Settings.MixedPort + 1
	case tuiKeyPortDown:
		if snapshot.Settings.MixedPort <= 0 {
			snapshot.Status = "Proxy port is already at 0"
			return false
		}
		patch["mixed-port"] = snapshot.Settings.MixedPort - 1
	default:
		return false
	}
	if err := client.patchConfig(patch); err != nil {
		snapshot.Status = "Settings update failed: " + err.Error()
		return false
	}
	snapshot.Status = "Settings updated"
	refreshTUISnapshot(snapshot, client)
	if (key == tuiKeyPortUp || key == tuiKeyPortDown) && systemProxyEnabled {
		enableSystemProxy := snapshot.Settings.MixedPort > 0
		if err := setLinuxSystemProxy(snapshot.Settings.MixedPort, enableSystemProxy); err != nil {
			snapshot.Status = "Port changed, but system proxy update failed: " + err.Error()
		} else if !enableSystemProxy {
			snapshot.Settings.SystemProxy = false
			snapshot.Status = "Proxy port disabled; System proxy disabled"
		} else {
			snapshot.Settings.SystemProxy = true
			snapshot.Status = fmt.Sprintf("Proxy port changed to %d", snapshot.Settings.MixedPort)
		}
	}
	return true
}

func toggleTUISystemProxy(snapshot *tuiSnapshot) bool {
	port := snapshot.Settings.MixedPort
	if port <= 0 {
		snapshot.Status = "System proxy requires a positive Proxy port"
		return false
	}
	enable := !snapshot.Settings.SystemProxy
	if err := setLinuxSystemProxy(port, enable); err != nil {
		snapshot.Status = "System proxy update failed: " + err.Error()
		return false
	}
	snapshot.Settings.SystemProxy = enable
	snapshot.Status = "System proxy enabled"
	if !enable {
		snapshot.Status = "System proxy disabled"
	}
	return true
}

func setTUIMixedPort(snapshot *tuiSnapshot, client controllerClient, oldState **term.State) {
	currentPort := snapshot.Settings.MixedPort
	selectedPort := currentPort
	changed := false
	err := runTUICooked(oldState, func() error {
		_, _ = fmt.Fprintf(os.Stdout, "Proxy port [0-65535] (current %d, empty cancels): ", currentPort)
		value, readErr := readTUILine(os.Stdin)
		if readErr != nil && len(value) == 0 {
			return readErr
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return nil
		}
		port, parseErr := strconv.Atoi(value)
		if parseErr != nil || port < 0 || port > 65535 {
			return errors.New("Proxy port must be a number from 0 to 65535")
		}
		selectedPort = port
		changed = selectedPort != currentPort
		return nil
	})
	if err != nil {
		snapshot.Status = "Port change failed: " + err.Error()
		return
	}
	if !changed {
		snapshot.Status = "Port unchanged"
		return
	}
	if err := client.patchConfig(map[string]interface{}{"mixed-port": selectedPort}); err != nil {
		snapshot.Status = "Port change failed: " + err.Error()
		return
	}
	systemProxyEnabled := snapshot.Settings.SystemProxy
	refreshTUISnapshot(snapshot, client)
	if systemProxyEnabled {
		enableSystemProxy := snapshot.Settings.MixedPort > 0
		if err := setLinuxSystemProxy(snapshot.Settings.MixedPort, enableSystemProxy); err != nil {
			snapshot.Status = "Port changed, but system proxy update failed: " + err.Error()
			return
		}
		snapshot.Settings.SystemProxy = enableSystemProxy
		if !enableSystemProxy {
			snapshot.Status = "Proxy port disabled; System proxy disabled"
			return
		}
	}
	snapshot.Status = fmt.Sprintf("Proxy port changed to %d", snapshot.Settings.MixedPort)
}

func linuxSystemProxyEnabled() bool {
	schema := linuxProxySchema()
	value, err := linuxGSettingsGet(schema, "mode")
	return err == nil && value == "'manual'"
}

func linuxSystemProxyMatches(port int) bool {
	if port <= 0 || !linuxSystemProxyEnabled() {
		return false
	}
	for _, suffix := range []string{".http", ".https", ".socks"} {
		schema := linuxProxySchema() + suffix
		host, err := linuxGSettingsGet(schema, "host")
		if err != nil {
			return false
		}
		configuredPort, err := linuxGSettingsGet(schema, "port")
		if err != nil {
			return false
		}
		proxyPort, err := strconv.Atoi(strings.TrimSpace(configuredPort))
		if err != nil {
			return false
		}
		host = strings.Trim(strings.TrimSpace(host), "'\"")
		if (host != "127.0.0.1" && host != "localhost" && host != "::1") ||
			proxyPort != port {
			return false
		}
	}
	return true
}

func linuxGSettingsGet(schema, key string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	output, err := exec.CommandContext(
		ctx,
		"gsettings",
		"get",
		schema,
		key,
	).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func setLinuxSystemProxy(port int, enable bool) error {
	schema := linuxProxySchema()
	commands := linuxSystemProxyCommands(schema, port, enable)
	for _, args := range commands {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		output, err := exec.CommandContext(ctx, "gsettings", append([]string{"set"}, args...)...).CombinedOutput()
		cancel()
		if err != nil {
			message := strings.TrimSpace(string(output))
			if message == "" {
				message = err.Error()
			}
			return fmt.Errorf("gsettings: %s", message)
		}
	}
	return nil
}

func linuxSystemProxyCommands(schema string, port int, enable bool) [][]string {
	commands := make([][]string, 0, 7)
	if enable {
		commands = append(commands,
			[]string{schema, "mode", "none"},
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
	// Disable first and enable last so a failed host/port update cannot leave a
	// half-configured desktop proxy active.
	commands = append(commands, []string{schema, "mode", mode})
	return commands
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

var errTUIActionCancelled = errors.New("TUI action cancelled")

func readTUILine(reader io.Reader) (string, error) {
	var value strings.Builder
	buffer := make([]byte, 1)
	for {
		_, err := io.ReadFull(reader, buffer)
		if err != nil {
			if value.Len() > 0 && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
				return value.String(), nil
			}
			return value.String(), err
		}
		if buffer[0] == '\n' {
			return value.String(), nil
		}
		value.WriteByte(buffer[0])
		if value.Len() > 64*1024 {
			return "", errors.New("input is too long")
		}
	}
}

func addTUIProfile(homeDir string, oldState **term.State) error {
	return runTUICooked(oldState, func() error {
		_, _ = fmt.Fprint(os.Stdout, "Subscription URL (empty cancels): ")
		value, err := readTUILine(os.Stdin)
		if err != nil && len(value) == 0 {
			return err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return errTUIActionCancelled
		}
		request, err := newTUISubscriptionRequest(value)
		if err != nil {
			return err
		}
		client := &nethttp.Client{Timeout: 30 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("subscription returned %s", response.Status)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, tuiSubscriptionMaxBytes+1))
		if err != nil {
			return err
		}
		if len(data) == 0 {
			return errors.New("subscription response is empty")
		}
		if len(data) > tuiSubscriptionMaxBytes {
			return fmt.Errorf("subscription response exceeds %d MiB", tuiSubscriptionMaxBytes>>20)
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
	logrus.SetOutput(io.Discard)
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
	restoreErr := term.Restore(int(os.Stdin.Fd()), state)
	logrus.SetOutput(os.Stdout)
	_, _ = fmt.Fprint(os.Stdout, "\x1b[?25h\x1b[?7h\x1b[0m\x1b[2J\x1b[H\x1b[?1049l")
	return restoreErr
}

func reenterTUIMode(oldState **term.State) error {
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	*oldState = state
	logrus.SetOutput(io.Discard)
	enterTUIScreen()
	return nil
}

func enterTUIScreen() {
	_, _ = fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[?7l\x1b[?25l\x1b[H\x1b[2J")
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
	tuiKeyInterrupt
	tuiKeyRefresh
	tuiKeyReload
	tuiKeyHelp
	tuiKeyFocusNext
	tuiKeyFocusPrevious
	tuiKeyUp
	tuiKeyDown
	tuiKeyLeft
	tuiKeyRight
	tuiKeyBack
	tuiKeySelect
	tuiKeyDashboard
	tuiKeyProxies
	tuiKeyProfiles
	tuiKeySSH
	tuiKeyRequests
	tuiKeyConnections
	tuiKeyLogs
	tuiKeySettings
	tuiKeyProviders
	tuiKeyCloseConnections
	tuiKeyAllowLAN
	tuiKeyIPv6
	tuiKeyUnifiedDelay
	tuiKeyTCPConcurrent
	tuiKeyTunScope
	tuiKeyTun
	tuiKeyMode
	tuiKeyLogLevel
	tuiKeyPortUp
	tuiKeyPortDown
	tuiKeySetPort
	tuiKeySystemProxy
	tuiKeyCoreToggle
	tuiKeyEdit
	tuiKeyNewProfile
	tuiKeyRenameProfile
	tuiKeyUpdateProfile
	tuiKeyCloseConnection
	tuiKeyTools
	tuiKeyMaintenance
	tuiKeyBackup
	tuiKeyRestore
	tuiKeyGeoUpdate
	tuiKeyResetTraffic
	tuiKeyViewPrevious
	tuiKeyViewNext
	tuiKeyDelayTest
	tuiKeyDelayTestAll
	tuiKeySpeedTest
	tuiKeyPageUp
	tuiKeyPageDown
	tuiKeyNotifications
)

func readTUIKeys(reader io.Reader, keys chan<- tuiKey) {
	readTUIKeysSynchronized(reader, keys, nil)
}

func readTUIKeysSynchronized(reader io.Reader, keys chan<- tuiKey, handled <-chan struct{}) {
	defer close(keys)
	buffer := make([]byte, 1)
	for {
		if _, err := io.ReadFull(reader, buffer); err != nil {
			return
		}
		key := tuiKey(0xff)
		switch buffer[0] {
		case 'q', 'Q':
			key = tuiKeyQuit
		case 3:
			key = tuiKeyInterrupt
		case 14:
			key = tuiKeyNotifications
		case 'r':
			key = tuiKeyRefresh
		case 'R':
			key = tuiKeyReload
		case '?':
			key = tuiKeyHelp
		case '\t':
			key = tuiKeyFocusNext
		case '1':
			key = tuiKeyDashboard
		case '2':
			key = tuiKeyProxies
		case '3':
			key = tuiKeyProfiles
		case '4':
			key = tuiKeySSH
		case '5':
			key = tuiKeyRequests
		case '6':
			key = tuiKeyConnections
		case '7':
			key = tuiKeyLogs
		case '8':
			key = tuiKeyTools
		case '9':
			key = tuiKeyMaintenance
		case 'P':
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
		case 'i':
			key = tuiKeyLogLevel
		case '+', '=':
			key = tuiKeyPortUp
		case '-':
			key = tuiKeyPortDown
		case 'p':
			key = tuiKeySetPort
		case 'S':
			key = tuiKeySystemProxy
		case 'c':
			key = tuiKeyCoreToggle
		case 'e':
			key = tuiKeyEdit
		case 'n':
			key = tuiKeyNewProfile
		case 'u':
			key = tuiKeyRenameProfile
		case 'U':
			key = tuiKeyUpdateProfile
		case 'd':
			key = tuiKeyCloseConnection
		case '[':
			key = tuiKeyViewPrevious
		case ']':
			key = tuiKeyViewNext
		case 'b':
			key = tuiKeyBackup
		case 'B':
			key = tuiKeyRestore
		case 'g':
			key = tuiKeyGeoUpdate
		case 'z':
			key = tuiKeyResetTraffic
		case 'D':
			key = tuiKeyDelayTest
		case 'A':
			key = tuiKeyDelayTestAll
		case '\r', '\n', ' ':
			key = tuiKeySelect
		case 'w':
			key = tuiKeyUp
		case 's':
			key = tuiKeyDown
		case 0x1b:
			key = readTUIEscape(reader)
		}
		if key != tuiKey(0xff) {
			keys <- key
			if handled != nil {
				if _, open := <-handled; !open {
					return
				}
			}
		}
	}
}

func readTUIEscape(reader io.Reader) tuiKey {
	prefix := make([]byte, 1)
	if _, err := io.ReadFull(reader, prefix); err != nil || (prefix[0] != '[' && prefix[0] != 'O') {
		return tuiKey(0xff)
	}
	number := byte(0)
	for count := 0; count < 16; count++ {
		value := make([]byte, 1)
		if _, err := io.ReadFull(reader, value); err != nil {
			return tuiKey(0xff)
		}
		switch value[0] {
		case 'A':
			return tuiKeyUp
		case 'B':
			return tuiKeyDown
		case 'C':
			return tuiKeyRight
		case 'D':
			return tuiKeyLeft
		case 'Z':
			return tuiKeyFocusPrevious
		case '~':
			switch number {
			case '5':
				return tuiKeyPageUp
			case '6':
				return tuiKeyPageDown
			}
			return tuiKey(0xff)
		}
		if value[0] >= '0' && value[0] <= '9' {
			number = value[0]
			continue
		}
		if (value[0] >= 'a' && value[0] <= 'z') ||
			(value[0] >= 'E' && value[0] <= 'Z') ||
			value[0] == '~' {
			return tuiKey(0xff)
		}
	}
	return tuiKey(0xff)
}

func handleTUIFocusNavigation(snapshot *tuiSnapshot, key tuiKey) bool {
	if page, ok := tuiPageForKey(key); ok {
		snapshot.Page = page
		snapshot.SelectedMenu = int(page)
		snapshot.FocusSidebar = false
		snapshot.ProxyNodeFocus = false
		return true
	}
	switch key {
	case tuiKeyFocusNext, tuiKeyFocusPrevious:
		snapshot.FocusSidebar = !snapshot.FocusSidebar
		if snapshot.FocusSidebar {
			snapshot.SelectedMenu = int(snapshot.Page)
		}
		return true
	}
	if snapshot.FocusSidebar {
		switch key {
		case tuiKeyUp:
			snapshot.SelectedMenu = wrapTUIIndex(snapshot.SelectedMenu, -1, int(tuiPageCount))
		case tuiKeyDown:
			snapshot.SelectedMenu = wrapTUIIndex(snapshot.SelectedMenu, 1, int(tuiPageCount))
		case tuiKeySelect, tuiKeyRight:
			snapshot.Page = tuiPage(wrapTUIIndex(snapshot.SelectedMenu, 0, int(tuiPageCount)))
			snapshot.SelectedMenu = int(snapshot.Page)
			snapshot.FocusSidebar = false
			snapshot.ProxyNodeFocus = false
		case tuiKeyLeft:
		default:
			return false
		}
		return true
	}
	switch key {
	case tuiKeyLeft:
		snapshot.FocusSidebar = true
		snapshot.SelectedMenu = int(snapshot.Page)
		return true
	case tuiKeyRight:
		return true
	}
	return false
}

func tuiPageForKey(key tuiKey) (tuiPage, bool) {
	switch key {
	case tuiKeyDashboard:
		return tuiPageDashboard, true
	case tuiKeyProxies:
		return tuiPageProxies, true
	case tuiKeyProfiles:
		return tuiPageProfiles, true
	case tuiKeySSH:
		return tuiPageSSH, true
	case tuiKeyRequests:
		return tuiPageRequests, true
	case tuiKeyConnections:
		return tuiPageConnections, true
	case tuiKeyLogs:
		return tuiPageLogs, true
	case tuiKeySettings:
		return tuiPageTools, true
	case tuiKeyTools:
		return tuiPageTools, true
	case tuiKeyMaintenance:
		return tuiPageMaintenance, true
	default:
		return tuiPageDashboard, false
	}
}

func drawTUI(w io.Writer, snapshot tuiSnapshot, paths cliPaths, controllerAddress string, ownsCore, coreRunning bool) {
	width, height := tuiTerminalSize()
	drawTUIAtSize(w, snapshot, paths, controllerAddress, ownsCore, coreRunning, width, height)
}

func drawTUIAtSize(w io.Writer, snapshot tuiSnapshot, paths cliPaths, controllerAddress string, ownsCore, coreRunning bool, width, height int) {
	writeTUIFrame(w, renderTUIAtSize(snapshot, paths, controllerAddress, ownsCore, coreRunning, width, height))
}

func renderTUIAtSize(snapshot tuiSnapshot, paths cliPaths, controllerAddress string, ownsCore, coreRunning bool, width, height int) string {
	if width <= 0 || height <= 0 {
		return ""
	}
	if width < 40 || height < 10 {
		return renderTUITiny(snapshot, ownsCore, coreRunning, width, height)
	}
	snapshot.ServiceRunning = coreRunning
	snapshot.ExternalCore = !ownsCore
	if ownsCore && coreRunning && snapshot.Settings.Mode != tuiSilentMode &&
		snapshot.Settings.MixedPort <= 0 &&
		(snapshot.Status == "" || snapshot.Status == "Connected" || snapshot.Status == "Core listeners started") {
		snapshot.Status = "No proxy listener active; select Proxy port in Dashboard or Settings"
	}
	if width < 88 || height < 18 {
		return renderTUICompact(
			snapshot,
			paths,
			controllerAddress,
			ownsCore,
			coreRunning,
			width,
			height,
		)
	}
	contentWidth := width - 2
	headerHeight := 3
	footerHeight := 1
	bodyHeight := height - headerHeight - footerHeight
	sidebarWidth := minTUI(maxTUIWidth(width/5, 22), 28)
	mainOuterWidth := width - sidebarWidth - 1
	mainContentWidth := mainOuterWidth - 2
	var b strings.Builder
	b.WriteString(tuiBoxTop(contentWidth))
	headerLeft := "  FlClash  ·  terminal proxy manager"
	headerRight := "  " + tuiStatusDot(ownsCore, coreRunning) + " " + tuiCoreStatus(ownsCore, coreRunning) + "  " + truncateTUI(controllerAddress, 30) + "  "
	b.WriteString(tuiBoxRow(headerLeft, headerRight, contentWidth, tuiCyan, tuiDim))
	b.WriteString(tuiBoxBottom(contentWidth))

	page := tuiRenderPage(snapshot, paths, mainContentWidth, bodyHeight)
	sidebar := tuiSidebar(snapshot, sidebarWidth, bodyHeight)
	pageLines := strings.Split(strings.TrimSuffix(page, "\n"), "\n")
	for row := 0; row < bodyHeight; row++ {
		left := ""
		if row < len(sidebar) {
			left = sidebar[row]
		}
		if left == "" {
			left = strings.Repeat(" ", sidebarWidth)
		}
		right := ""
		if row < len(pageLines) {
			right = pageLines[row]
		}
		left = tuiClampAnsiLine(left, sidebarWidth)
		right = tuiClampAnsiLine(right, mainOuterWidth)
		b.WriteString(left)
		b.WriteByte(' ')
		b.WriteString(right)
		b.WriteByte('\n')
	}

	footer := "  ←→ panel  ↑↓/ws move  Enter apply  Esc nav  ? help  q exit TUI  ^C shutdown"
	if width >= 110 {
		footer = "  ←→ panel  ↑↓/ws move  Enter open/apply  Esc back  d delay  ? help  q exit TUI  ^C shutdown"
	}
	if snapshot.Page == tuiPageDashboard && bodyHeight < 33 {
		footer = "  ←→ panel  ↑↓/ws select  PgUp/PgDn scroll  Enter apply  q exit TUI  ^C shutdown"
	}
	b.WriteString(tuiNotificationFooter(snapshot, footer, width))
	return b.String()
}

func renderTUICompact(
	snapshot tuiSnapshot,
	paths cliPaths,
	controllerAddress string,
	ownsCore,
	coreRunning bool,
	width,
	height int,
) string {
	pageName := tuiPageName(snapshot.Page)
	focus := "CONTENT"
	if snapshot.FocusSidebar {
		focus = "NAV"
	}
	header := fmt.Sprintf(
		"  FlClash · %d %s · %s · %s",
		int(snapshot.Page)+1,
		pageName,
		tuiCoreStatus(ownsCore, coreRunning),
		focus,
	)
	if width >= 72 {
		header += " · " + truncateTUI(controllerAddress, 24)
	}

	bodyHeight := height - 2
	contentWidth := width - 2
	var page string
	switch {
	case snapshot.NotificationDetailOpen ||
		snapshot.ProfileDelete.Open ||
		snapshot.SSHForm.DeleteConfirmOpen ||
		snapshot.SSHForm.Open ||
		snapshot.SelectionTitle != "" ||
		snapshot.InputTitle != "":
		page = tuiRenderPage(
			snapshot,
			paths,
			contentWidth,
			bodyHeight,
		)
	case snapshot.FocusSidebar:
		page = renderTUICompactNavigation(
			snapshot,
			contentWidth,
			bodyHeight,
		)
	case snapshot.Page == tuiPageDashboard:
		page = renderTUICompactDashboard(
			snapshot,
			paths,
			contentWidth,
			bodyHeight,
		)
	default:
		page = tuiRenderPage(
			snapshot,
			paths,
			contentWidth,
			bodyHeight,
		)
	}

	var b strings.Builder
	b.WriteString(tuiClampAnsiLine(header, width))
	b.WriteByte('\n')
	pageLines := strings.Split(strings.TrimSuffix(page, "\n"), "\n")
	for row := 0; row < bodyHeight; row++ {
		line := ""
		if row < len(pageLines) {
			line = pageLines[row]
		}
		b.WriteString(tuiClampAnsiLine(line, width))
		b.WriteByte('\n')
	}
	footer := "  1-9 page · ←/Esc nav · ↑↓/ws · Enter · q"
	if snapshot.FocusSidebar {
		footer = "  ↑↓/ws page · →/Enter open · 1-9 direct · q"
	} else if snapshot.Page == tuiPageDashboard {
		footer = "  ↑↓/ws select · PgUp/PgDn scroll · Esc nav · q"
	}
	b.WriteString(tuiNotificationFooter(snapshot, footer, width))
	return b.String()
}

func tuiPageName(page tuiPage) string {
	switch page {
	case tuiPageDashboard:
		return "Dashboard"
	case tuiPageProxies:
		return "Proxies"
	case tuiPageProfiles:
		return "Profiles"
	case tuiPageSSH:
		return "SSH"
	case tuiPageRequests:
		return "History"
	case tuiPageConnections:
		return "Connections"
	case tuiPageLogs:
		return "Logs"
	case tuiPageTools:
		return "Settings"
	case tuiPageMaintenance:
		return "Maintenance"
	default:
		return "Unknown"
	}
}

func renderTUICompactNavigation(
	snapshot tuiSnapshot,
	width,
	height int,
) string {
	var b strings.Builder
	tuiTitle(&b, "Navigation", "↑↓/ws select · →/Enter open", width)
	limit := maxTUIWidth(height-3, 1)
	start, end := tuiVisibleRange(
		int(tuiPageCount),
		snapshot.SelectedMenu,
		limit,
	)
	for index := start; index < end; index++ {
		tuiRow(
			&b,
			fmt.Sprintf("%d  %s", index+1, tuiPageName(tuiPage(index))),
			width,
			index == snapshot.SelectedMenu,
			"",
		)
	}
	tuiEndPanel(&b, width)
	return b.String()
}

func tuiWrapText(value string, width int) []string {
	if width <= 0 {
		return nil
	}
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{""}
	}
	lines := make([]string, 0, len(words))
	line := ""
	for _, word := range words {
		candidate := word
		if line != "" {
			candidate = line + " " + word
		}
		if tuiDisplayWidth(candidate) <= width {
			line = candidate
			continue
		}
		if line != "" {
			lines = append(lines, line)
		}
		wordParts := tuiWrapWord(word, width)
		if len(wordParts) > 1 {
			lines = append(lines, wordParts[:len(wordParts)-1]...)
		}
		line = wordParts[len(wordParts)-1]
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

func tuiWrapWord(value string, width int) []string {
	if value == "" || width <= 0 {
		return []string{""}
	}
	parts := make([]string, 0, 1)
	var part strings.Builder
	partWidth := 0
	for _, valueRune := range value {
		runeWidth := tuiRuneWidth(valueRune)
		if partWidth > 0 && partWidth+runeWidth > width {
			parts = append(parts, part.String())
			part.Reset()
			partWidth = 0
		}
		part.WriteRune(valueRune)
		partWidth += runeWidth
	}
	if part.Len() > 0 {
		parts = append(parts, part.String())
	}
	if len(parts) == 0 {
		return []string{""}
	}
	return parts
}

func drawTUITooSmall(w io.Writer, width, height int) {
	writeTUIFrame(w, renderTUITooSmall(width, height))
}

func renderTUITooSmall(width, height int) string {
	lines := []string{
		"",
		"  FlClash TUI",
		"",
		fmt.Sprintf("  Terminal: %dx%d", width, height),
		"  Resize to at least 40x10",
		"",
		"  q exit TUI · Ctrl+C shutdown",
	}
	var b strings.Builder
	for row := 0; row < height; row++ {
		line := ""
		if row < len(lines) {
			line = lines[row]
		}
		b.WriteString(tuiClampAnsiLine(line, width))
		if row < height-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func renderTUITiny(
	snapshot tuiSnapshot,
	ownsCore,
	coreRunning bool,
	width,
	height int,
) string {
	lines := []string{
		"FlClash " + tuiCoreStatus(ownsCore, coreRunning),
		fmt.Sprintf("%d %s", int(snapshot.Page)+1, tuiPageName(snapshot.Page)),
	}
	if snapshot.NotificationDetailOpen {
		lines = append(lines, tuiNotificationTinyDetail(snapshot, width)...)
	} else if notification, ok := tuiLatestUnreadNotification(snapshot.Notifications); ok {
		lines = append(lines, tuiNotificationTinySummary(notification, width))
	}
	if height > 0 && len(lines) >= height {
		lines = lines[:height-1]
	}
	lines = append(lines, "q exit TUI · ^C shutdown")
	var b strings.Builder
	for row := 0; row < height; row++ {
		line := ""
		if row < len(lines) {
			line = lines[row]
		}
		b.WriteString(tuiClampAnsiLine(line, width))
		if row < height-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func writeTUIFrame(w io.Writer, frame string) {
	// term.MakeRaw disables the terminal's output post-processing. A bare LF
	// therefore moves down without returning to column zero, which makes every
	// subsequent row drift to the right. Normalize complete frames to CRLF.
	frame = strings.ReplaceAll(frame, "\r\n", "\n")
	frame = strings.ReplaceAll(frame, "\n", "\r\n")
	_, _ = io.WriteString(w, frame)
}

type tuiFrameWriter struct {
	writer io.Writer
	last   string
}

func (w *tuiFrameWriter) Write(data []byte) (int, error) {
	frame := string(data)
	if frame == w.last {
		return len(data), nil
	}
	written, err := w.writer.Write(data)
	if err == nil && written == len(data) {
		w.last = frame
	}
	return written, err
}

func (w *tuiFrameWriter) invalidate() {
	w.last = ""
}

func tuiRenderPage(snapshot tuiSnapshot, paths cliPaths, width, height int) string {
	var b strings.Builder
	if snapshot.NotificationDetailOpen {
		drawTUINotificationDetails(&b, snapshot, width, height)
	} else if snapshot.ProfileDelete.Open {
		drawTUIProfileDeleteConfirm(&b, snapshot.ProfileDelete, width)
	} else if snapshot.SSHForm.DeleteConfirmOpen {
		drawTUISSHDeleteConfirm(&b, snapshot.SSHForm, width)
	} else if snapshot.SSHForm.Open {
		drawTUISSHForm(&b, snapshot.SSHForm, width, height)
	} else if snapshot.SelectionTitle != "" {
		drawTUISelection(&b, snapshot, width)
	} else if snapshot.InputTitle != "" {
		drawTUIInput(&b, snapshot, width)
	} else if snapshot.ShowHelp {
		drawTUIHelp(&b, width, height)
	} else if snapshot.Page == tuiPageRequests {
		drawTUIRequests(&b, snapshot, width, height)
	} else if snapshot.Page == tuiPageConnections {
		drawTUIConnections(&b, snapshot, width, height)
	} else if snapshot.Page == tuiPageLogs {
		drawTUILogs(&b, snapshot, width, height)
	} else if snapshot.Page == tuiPageProfiles {
		drawTUIProfiles(&b, snapshot, width, height)
	} else if snapshot.Page == tuiPageSSH {
		drawTUISSH(&b, snapshot, width, height)
	} else if snapshot.Page == tuiPageTools {
		drawTUITools(&b, snapshot, width, height)
	} else if snapshot.Page == tuiPageMaintenance {
		drawTUIMaintenance(&b, snapshot, width, height)
	} else if snapshot.Page == tuiPageDashboard {
		if height < 33 {
			return renderTUICompactDashboard(
				snapshot,
				paths,
				width,
				height,
			)
		}
		drawTUIDashboard(&b, snapshot, paths, width, height)
	} else if snapshot.Page == tuiPageProxies &&
		snapshot.ProxyView == tuiProxyViewProviders {
		drawTUIProviders(&b, snapshot, width, height)
	} else if snapshot.Page == tuiPageProxies && len(snapshot.Groups) == 0 {
		drawTUIEmpty(
			&b,
			width,
			"Proxy groups",
			"No selectable groups · press ] for Providers or r to refresh",
		)
	} else {
		drawTUIProxies(&b, snapshot, width, height)
	}
	return b.String()
}

func drawTUISelection(b *strings.Builder, snapshot tuiSnapshot, width int) {
	tuiTitle(b, snapshot.SelectionTitle, "Enter confirm · Esc cancel", width)
	for index, option := range snapshot.SelectionOptions {
		label := option
		if strings.EqualFold(option, snapshot.Settings.Mode) {
			label += "  (current)"
		}
		tuiRow(b, label, width, index == snapshot.SelectedOption, "")
	}
	tuiEndPanel(b, width)
	if snapshot.SelectionHint != "" {
		tuiEmptyPanel(b, "Selection help", snapshot.SelectionHint, width)
	}
}

func drawTUIInput(b *strings.Builder, snapshot tuiSnapshot, width int) {
	tuiTitle(b, snapshot.InputTitle, "Enter confirm · Esc cancel", width)
	tuiRow(b, snapshot.InputValue, width, true, "")
	tuiEndPanel(b, width)
	if snapshot.InputHint != "" {
		tuiEmptyPanel(b, "Input help", snapshot.InputHint, width)
	}
}

func drawTUISSHDeleteConfirm(
	b *strings.Builder,
	form tuiSSHFormView,
	width int,
) {
	tuiTitle(b, "Delete SSH profile", "Enter confirm · Esc cancel", width)
	tuiRow(
		b,
		"Delete "+form.DeleteName+"? This also disconnects its active tunnel.",
		width,
		true,
		tuiRed,
	)
	tuiEndPanel(b, width)
}

func drawTUIProfileDeleteConfirm(
	b *strings.Builder,
	profile tuiProfileDeleteView,
	width int,
) {
	tuiTitle(b, "Delete Profile", "Enter confirm · Esc cancel", width)
	tuiRow(
		b,
		"Delete "+profile.Name+" ("+profile.Kind+")? The saved YAML and linked metadata will be removed.",
		width,
		true,
		tuiRed,
	)
	tuiEndPanel(b, width)
}

func drawTUISSHForm(
	b *strings.Builder,
	form tuiSSHFormView,
	width,
	height int,
) {
	title := "Add SSH profile"
	subtitle := "↑↓/Tab select · Enter edit/confirm · x remove option · Esc cancel"
	if form.Existing {
		title = "Edit SSH profile"
	}
	if form.ReadOnly {
		title = "SSH profile details · CONNECTED · READ ONLY"
		subtitle = "↑↓ select · disconnect before editing · Esc close"
	}
	tuiTitle(
		b,
		title,
		subtitle,
		width,
	)
	rows := make([]struct {
		label string
		color string
	}, 0, len(form.Options)+10)
	rows = append(rows,
		struct {
			label string
			color string
		}{label: "Name         " + form.Name},
		struct {
			label string
			color string
		}{label: "SSH exit     " + form.Destination},
		struct {
			label string
			color string
		}{label: fmt.Sprintf("SSH port     %d", form.Port)},
		struct {
			label string
			color string
		}{label: "Local SOCKS  " + formatTUISSHLocalPort(form.LocalPort)},
		struct {
			label string
			color string
		}{label: "Identity     " + cliDisplayValue(form.Identity)},
	)
	password := "not saved · Enter set"
	if form.PasswordSet {
		password = "******** · Enter replace · c clear"
	}
	if form.ReadOnly && form.PasswordSet {
		password = "******** · set · read only"
	} else if form.ReadOnly {
		password = "not saved · read only"
	}
	if form.PasswordChanged {
		password = "******** · replacement staged · c clear"
	}
	if form.PasswordCleared {
		password = "clear on save · Enter set"
	}
	rows = append(rows, struct {
		label string
		color string
	}{label: "Password     " + password})
	for index, option := range form.Options {
		rows = append(rows, struct {
			label string
			color string
		}{label: fmt.Sprintf("Option %-3d   %s", index+1, option)})
	}
	addOptionLabel := "+ Add OpenSSH option (KEY=VALUE)"
	addOptionColor := tuiCyan
	saveLabel := "Save profile"
	saveColor := tuiGreen
	if form.ReadOnly {
		addOptionLabel = "Options are read only while connected"
		addOptionColor = tuiDim
		saveLabel = "Save unavailable · disconnect before editing"
		saveColor = tuiDim
	}
	rows = append(rows,
		struct {
			label string
			color string
		}{label: addOptionLabel, color: addOptionColor},
		struct {
			label string
			color string
		}{label: saveLabel, color: saveColor},
	)
	if form.Existing {
		rows = append(rows, struct {
			label string
			color string
		}{label: "Delete profile", color: tuiRed})
	}
	cancelLabel := "Cancel"
	if form.ReadOnly {
		cancelLabel = "Close details"
	}
	rows = append(rows, struct {
		label string
		color string
	}{label: cancelLabel, color: tuiYellow})
	if form.FieldEditing && form.Selected >= 0 && form.Selected < len(rows) {
		label := "Value        " + form.FieldInput
		if form.Selected == tuiSSHFormPasswordRow {
			label = "Password     " + form.FieldInput
			if form.PasswordConfirm {
				label = "Confirm      " + form.FieldInput
			}
		}
		rows[form.Selected].label = label
	}
	limit := maxTUIWidth(height-4, 1)
	start, end := tuiVisibleRange(len(rows), form.Selected, limit)
	for index := start; index < end; index++ {
		tuiRow(
			b,
			rows[index].label,
			width,
			index == form.Selected,
			rows[index].color,
		)
	}
	tuiEndPanel(b, width)
}

func formatTUISSHLocalPort(port int) string {
	if port <= 0 {
		return "auto"
	}
	return "127.0.0.1:" + strconv.Itoa(port)
}

const (
	tuiReset  = "\x1b[0m"
	tuiBold   = "\x1b[1m"
	tuiDim    = "\x1b[2m"
	tuiCyan   = "\x1b[36m"
	tuiGreen  = "\x1b[32m"
	tuiYellow = "\x1b[33m"
	tuiRed    = "\x1b[31m"
	tuiSelect = "\x1b[48;5;24m\x1b[97m"
)

func tuiTerminalSize() (int, int) {
	width, height := 0, 0
	for _, fd := range []uintptr{os.Stdin.Fd(), os.Stdout.Fd()} {
		candidateWidth, candidateHeight, err := term.GetSize(int(fd))
		if err == nil && candidateWidth > 0 && candidateHeight > 0 {
			if width == 0 || candidateWidth < width {
				width = candidateWidth
			}
			if height == 0 || candidateHeight < height {
				height = candidateHeight
			}
		}
	}
	if width == 0 {
		if columns, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && columns > 0 {
			width = columns
		}
	}
	if height == 0 {
		if lines, err := strconv.Atoi(os.Getenv("LINES")); err == nil && lines > 0 {
			height = lines
		}
	}
	if width == 0 || height == 0 {
		return 96, 30
	}
	return width, height
}

func tuiBoxTop(width int) string {
	return "┌" + strings.Repeat("─", width) + "┐\n"
}

func tuiBoxBottom(width int) string {
	return "└" + strings.Repeat("─", width) + "┘\n"
}

func tuiBoxRow(left, right string, width int, leftColor, rightColor string) string {
	right = truncateTUI(right, maxTUIWidth(width-1, 0))
	leftWidth := width - tuiDisplayWidth(right)
	if leftWidth < 1 {
		leftWidth = 1
	}
	left = tuiPadRight(left, leftWidth)
	return "│" + leftColor + left + tuiReset + rightColor + right + tuiReset + "│\n"
}

func tuiSidebar(snapshot tuiSnapshot, width, height int) []string {
	innerWidth := maxTUIWidth(width-2, 1)
	labels := []string{
		"@  Dashboard",
		"*  Proxies",
		"+  Profiles",
		"#  SSH",
		">  History",
		"~  Connections",
		"=  Logs",
		":  Settings",
		"!  Maintenance",
	}
	lines := make([]string, 0, height)
	lines = append(lines, tuiBoxTop(innerWidth))
	for index, label := range labels {
		if len(lines) >= height-1 {
			break
		}
		color := tuiDim
		prefix := "  "
		if tuiPage(index) == snapshot.Page {
			color = tuiCyan
		}
		if snapshot.FocusSidebar && index == snapshot.SelectedMenu {
			prefix = "> "
			color = tuiSelect + tuiCyan
		}
		lines = append(lines, tuiBoxRow("  "+prefix+label, "", innerWidth, color, ""))
	}
	for len(lines) < height-1 {
		lines = append(lines, tuiBoxRow("", "", innerWidth, "", ""))
	}
	if height > 0 {
		lines = append(lines, tuiBoxBottom(innerWidth))
	}
	return lines
}

func tuiStatusDot(ownsCore, coreRunning bool) string {
	if !ownsCore {
		return "○"
	}
	if coreRunning {
		return "●"
	}
	return "○"
}

func tuiCoreStatus(ownsCore, coreRunning bool) string {
	if !ownsCore {
		return "EXTERNAL CORE"
	}
	if coreRunning {
		return "CORE RUNNING"
	}
	return "CORE STOPPED"
}

func maxTUIWidth(width, minimum int) int {
	if width < minimum {
		return minimum
	}
	return width
}

func tuiPadRight(value string, width int) string {
	value = truncateTUI(value, width)
	return value + strings.Repeat(" ", maxTUIWidth(width-tuiDisplayWidth(value), 0))
}

func tuiDisplayWidth(value string) int {
	width := 0
	for _, r := range value {
		width += tuiRuneWidth(r)
	}
	return width
}

func tuiRuneWidth(value rune) int {
	if value == 0 || value < 32 {
		return 0
	}
	if value == '\u200d' || value == '\ufe0e' || value == '\ufe0f' ||
		unicode.Is(unicode.Mn, value) || unicode.Is(unicode.Me, value) || unicode.Is(unicode.Cf, value) {
		return 0
	}
	if value >= 0x1100 && (value <= 0x115f || value == 0x2329 || value == 0x232a ||
		(value >= 0x2e80 && value <= 0xa4cf) || (value >= 0xac00 && value <= 0xd7a3) ||
		(value >= 0xf900 && value <= 0xfaff) || (value >= 0xfe10 && value <= 0xfe6f) ||
		(value >= 0xff00 && value <= 0xff60) || (value >= 0xffe0 && value <= 0xffe6) ||
		(value >= 0x1f1e6 && value <= 0x1f1ff) || (value >= 0x1f300 && value <= 0x1faff)) {
		return 2
	}
	return 1
}

func tuiClampAnsiLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\r"), "\n")
	var b strings.Builder
	visibleWidth := 0
	for index := 0; index < len(line); {
		if line[index] == '\x1b' {
			start := index
			index++
			if index < len(line) && line[index] == '[' {
				index++
				for index < len(line) {
					value := line[index]
					index++
					if value >= '@' && value <= '~' {
						break
					}
				}
			}
			b.WriteString(line[start:index])
			continue
		}
		value, size := utf8.DecodeRuneInString(line[index:])
		runeWidth := tuiRuneWidth(value)
		if visibleWidth+runeWidth > width {
			break
		}
		b.WriteString(line[index : index+size])
		visibleWidth += runeWidth
		index += size
	}
	if visibleWidth < width {
		b.WriteString(strings.Repeat(" ", width-visibleWidth))
	}
	b.WriteString(tuiReset)
	return b.String()
}

func tuiRow(b *strings.Builder, value string, width int, selected bool, color string) {
	value = tuiPadRight(value, width-4)
	b.WriteString("│")
	if selected {
		b.WriteString(tuiSelect)
	}
	if color != "" {
		b.WriteString(color)
	}
	b.WriteString("  " + value + "  ")
	b.WriteString(tuiReset)
	b.WriteString("│\n")
}

func tuiTitle(b *strings.Builder, title, subtitle string, width int) {
	b.WriteString(tuiBoxTop(width))
	left := "  " + title
	if subtitle != "" {
		left += "  ·  " + subtitle
	}
	b.WriteString(tuiBoxRow(left, "", width, tuiBold+tuiCyan, ""))
}

func tuiTrafficTitle(
	b *strings.Builder,
	traffic trafficSnapshot,
	peak int64,
	width int,
) {
	b.WriteString(tuiBoxTop(width))
	line := "  " + tuiBold + tuiCyan + "Live traffic" + tuiReset +
		"  ·  " + formatTUITrafficLegend(traffic, peak)
	b.WriteString("│")
	b.WriteString(tuiClampAnsiLine(line, width))
	b.WriteString("│\n")
}

func formatTUITrafficLegend(traffic trafficSnapshot, peak int64) string {
	return fmt.Sprintf(
		"%s↑ %s/s%s · %s↓ %s/s%s%s · peak %s/s · 30 samples%s",
		tuiTrafficChartUpload,
		formatBytes(traffic.Up),
		tuiReset,
		tuiTrafficChartDownload,
		formatBytes(traffic.Down),
		tuiReset,
		tuiDim,
		formatBytes(peak),
		tuiReset,
	)
}

func tuiEndPanel(b *strings.Builder, width int) {
	b.WriteString(tuiBoxBottom(width))
}

func tuiEmptyPanel(b *strings.Builder, title, message string, width int) {
	tuiTitle(b, title, "", width)
	tuiRow(b, message, width, false, tuiDim)
	tuiEndPanel(b, width)
}

func drawTUIEmpty(b *strings.Builder, width int, title, message string) {
	tuiEmptyPanel(b, title, message, width)
}

func drawTUIProxies(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	if snapshot.SelectedGroup < 0 || snapshot.SelectedGroup >= len(snapshot.Groups) {
		drawTUIEmpty(b, width, "Proxy groups", "No group selected")
		return
	}
	group := snapshot.Groups[snapshot.SelectedGroup]
	availableRows := maxTUIWidth(height-6, 2)
	groupLimit := minTUI(len(snapshot.Groups), maxTUIWidth(availableRows/3, 1))
	nodeLimit := maxTUIWidth(availableRows-groupLimit, 1)
	groupStart, groupEnd := tuiVisibleRange(len(snapshot.Groups), snapshot.SelectedGroup, groupLimit)
	groupHint := "↑↓/ws group · Enter nodes · d test group · v speed group · [/] view"
	if snapshot.ProxyNodeFocus {
		groupHint = "Esc returns to proxy groups"
	}
	tuiTitle(
		b,
		"Proxies  ·  Groups  [1/2]",
		groupHint,
		width,
	)
	for index := groupStart; index < groupEnd; index++ {
		item := snapshot.Groups[index]
		row := fmt.Sprintf("%-28s %-12s %s", truncateTUI(item.Name, 28), item.Type, truncateTUI(item.Now, maxTUIWidth(width-48, 10)))
		tuiRow(
			b,
			row,
			width,
			index == snapshot.SelectedGroup &&
				!snapshot.FocusSidebar &&
				!snapshot.ProxyNodeFocus,
			"",
		)
	}
	tuiEndPanel(b, width)

	nodeTitle := "Nodes in " + group.Name
	tuiTitle(
		b,
		nodeTitle,
		fmt.Sprintf(
			"%d nodes · ↑↓/ws select · Enter apply · d delay · v speed · Esc back",
			len(group.Nodes),
		),
		width,
	)
	nodeStart, nodeEnd := tuiVisibleRange(len(group.Nodes), snapshot.SelectedNode, nodeLimit)
	for index := nodeStart; index < nodeEnd; index++ {
		node := group.Nodes[index]
		label := truncateTUI(node, width-24)
		if node == group.Now {
			label += "  [current]"
		}
		color := tuiGreen
		delay, tested := group.Delays[node]
		switch {
		case delay.MedianMillis > 0:
			label += "  " + formatTUIDelay(delay)
		case delay.Error != "":
			label += "  Timeout · d retry"
			color = tuiDim
		case delay.Testing:
			label += "  Testing..."
			color = tuiCyan
		case !tested:
			label += "  [d test]"
			color = tuiDim
		}
		if speed, speedTested := group.Speeds[node]; speedTested {
			switch {
			case speed.Testing:
				label += "  Speed testing..."
				color = tuiCyan
			case speed.Error != "":
				label += "  Speed failed · v retry"
				color = tuiDim
			case speed.BytesPerSecond > 0:
				label += "  " + formatTUISpeed(speed)
			}
		} else {
			label += "  [v test]"
		}
		tuiRow(
			b,
			label,
			width,
			index == snapshot.SelectedNode &&
				!snapshot.FocusSidebar &&
				snapshot.ProxyNodeFocus,
			color,
		)
	}
	if len(group.Nodes) == 0 {
		tuiRow(b, "No nodes in this group", width, false, tuiDim)
	}
	tuiEndPanel(b, width)
}

func drawTUIProviders(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	tuiTitle(
		b,
		"Proxies  ·  Providers  [2/2]",
		"↑↓/ws provider · Enter update · [/] view",
		width,
	)
	if len(snapshot.Providers) == 0 {
		tuiRow(b, "No proxy providers configured", width, false, tuiDim)
		tuiEndPanel(b, width)
		return
	}
	limit := maxTUIWidth(height-3, 1)
	start, end := tuiVisibleRange(len(snapshot.Providers), snapshot.SelectedProvider, limit)
	for index := start; index < end; index++ {
		provider := snapshot.Providers[index]
		row := fmt.Sprintf("%-30s %-10s %4d proxies  %s", truncateTUI(provider.Name, 30), truncateTUI(provider.Type, 10), provider.Count, truncateTUI(provider.UpdatedAt, maxTUIWidth(width-60, 8)))
		tuiRow(b, row, width, index == snapshot.SelectedProvider && !snapshot.FocusSidebar, "")
	}
	tuiEndPanel(b, width)
}

func drawTUITools(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	rows := tuiSettingsRows(snapshot)
	tuiTitle(
		b,
		"Settings",
		"Core · network · lifecycle",
		width,
	)
	limit := maxTUIWidth(height-3, 1)
	start, end := tuiVisibleRange(len(rows), snapshot.SelectedTool, limit)
	for index := start; index < end; index++ {
		tuiRow(
			b,
			rows[index],
			width,
			index == snapshot.SelectedTool && !snapshot.FocusSidebar,
			"",
		)
	}
	tuiEndPanel(b, width)
}

func drawTUIMaintenance(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	rows := []string{
		"Config        Edit current YAML in $EDITOR",
		"Backup        Create timestamped configuration backup",
		"Restore       Restore newest configuration backup",
		"Resources     Update Mihomo Geo databases",
		"Traffic       Reset traffic counters",
		tuiUpdateRow(snapshot.Update),
	}
	tuiTitle(
		b,
		"Maintenance",
		"Configuration · resources · diagnostics",
		width,
	)
	limit := maxTUIWidth(height-3, 1)
	start, end := tuiVisibleRange(len(rows), snapshot.SelectedMaintenance, limit)
	for index := start; index < end; index++ {
		row := rows[index]
		tuiRow(
			b,
			row,
			width,
			index == snapshot.SelectedMaintenance && !snapshot.FocusSidebar,
			"",
		)
	}
	tuiEndPanel(b, width)
}

func tuiUpdateRow(info tuiUpdateInfo) string {
	switch {
	case info.Loading:
		return "Update        Checking GitHub Releases..."
	case info.Error != "":
		return "Update        Check failed · Enter to retry"
	case info.LatestVersion != "" && info.Available:
		return fmt.Sprintf(
			"Update        v%s available · run flclash update",
			info.LatestVersion,
		)
	case info.LatestVersion != "":
		return fmt.Sprintf("Update        v%s is latest · keep it if stable", cliVersion)
	default:
		return "Update        Enter checks GitHub · if stable, do not update lightly"
	}
}

func minTUI(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func tuiVisibleRange(total, selected, limit int) (int, int) {
	if total <= 0 || limit <= 0 {
		return 0, 0
	}
	if limit >= total {
		return 0, total
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= total {
		selected = total - 1
	}
	start := selected - limit/2
	if start < 0 {
		start = 0
	}
	if start+limit > total {
		start = total - limit
	}
	return start, start + limit
}

func drawTUIHelp(b *strings.Builder, width, height int) {
	rows := []string{
		"Navigation     ← sidebar · → content · ↑↓/ws move · Enter opens/applies · Esc back",
		"Sections       1-9 open directly · Tab changes focus · [/] changes proxy view",
		"Dashboard      d rule-route latency (5 samples) · v Cloudflare 4-stream speed · n refresh",
		"Proxies        Enter nodes · d node RTT (5 samples) · v node speed · Esc groups",
		"Profiles       Enter activate · U refresh · F2/u rename · e edit · n import · x delete",
		"SSH            Enter connect/disconnect · n add · e edit · x delete · d test",
		"History        x clears shared history · Connections: x close all · d close selected",
		"Logs           e exports captured logs · x clears captured logs",
		"Core           S system proxy (auto-start) · c start/stop · t TUN · m mode",
		"Settings       Enter applies row · a LAN · v IPv6 · p port",
		"Maintenance    Edit, backup, restore, Geo, traffic reset, and updates",
		"Notifications  Ctrl+N opens history/details · Enter confirms · Esc closes",
		"Exit           q exits this TUI · Ctrl+C shuts down Backend and Core",
	}
	if height < 28 {
		rows = rows[:minTUI(5, len(rows))]
	}
	rows = rows[:minTUI(len(rows), maxTUIWidth(height-3, 1))]
	tuiTitle(b, "Keyboard shortcuts", "press ? to close", width)
	for _, row := range rows {
		tuiRow(b, row, width, false, "")
	}
	tuiEndPanel(b, width)
}

func drawTUIDashboard(b *strings.Builder, snapshot tuiSnapshot, paths cliPaths, width, height int) {
	serviceLabel := tuiServiceLabel(snapshot)
	systemProxyLabel := tuiSystemProxyLabel(snapshot)
	controls := []string{
		fmt.Sprintf("Core          %s", serviceLabel),
		fmt.Sprintf("System proxy  %s", systemProxyLabel),
		fmt.Sprintf("TUN           %s", tuiTUNLabel(snapshot)),
		fmt.Sprintf("Mode          %s", snapshot.Settings.Mode),
		fmt.Sprintf("Proxy port    %s", tuiProxyPortLabel(snapshot)),
	}
	tuiTitle(
		b,
		"Dashboard",
		"Current state shown first · Enter changes selected item",
		width,
	)
	for index, row := range controls {
		tuiRow(
			b,
			row,
			width,
			index == snapshot.SelectedDashboard && !snapshot.FocusSidebar,
			"",
		)
	}
	tuiEndPanel(b, width)

	publicIP := "Checking..."
	if snapshot.Network.PublicIP != "" {
		publicIP = snapshot.Network.PublicIP
		if snapshot.Network.Country != "" {
			publicIP += "  [" + snapshot.Network.Country + "]"
		}
		if snapshot.Network.Loading {
			publicIP += "  refreshing..."
		}
	} else if snapshot.Network.Error != "" && !snapshot.Network.Loading {
		publicIP = "Unavailable · press n to retry"
	}
	intranetIP := snapshot.Network.IntranetIP
	if intranetIP == "" {
		if snapshot.Network.Loading {
			intranetIP = "Detecting..."
		} else {
			intranetIP = "No active LAN address"
		}
	}
	networkSubtitle := ""
	if snapshot.Network.Route != "" {
		networkSubtitle = snapshot.Network.Route + " · "
	}
	networkSubtitle += "d " + strings.ToLower(tuiDashboardRouteName(snapshot)) +
		" RTT×5 · v CF speed · n refresh"
	if !snapshot.Network.CheckedAt.IsZero() {
		networkSubtitle += " · checked " + snapshot.Network.CheckedAt.Format("15:04:05")
	}
	tuiTitle(b, "Network detection", networkSubtitle, width)
	tuiRow(b, "Public IP     "+publicIP, width, false, tuiCyan)
	tuiRow(b, "Intranet IP   "+intranetIP, width, false, tuiGreen)
	tuiRow(
		b,
		tuiPadRight(tuiDashboardRouteName(snapshot), 15)+
			tuiDashboardDelayLabel(snapshot.DashboardDelay),
		width,
		snapshot.SelectedDashboard == tuiDashboardDelayRow &&
			!snapshot.FocusSidebar,
		tuiCyan,
	)
	tuiRow(
		b,
		"Cloudflare DL  "+tuiSpeedResultLabel(snapshot.DashboardSpeed),
		width,
		snapshot.SelectedDashboard == tuiDashboardSpeedRow &&
			!snapshot.FocusSidebar,
		tuiGreen,
	)
	tuiEndPanel(b, width)

	if height >= 33 {
		plotHeight := minTUI(maxTUIWidth(height-30, 3), 6)
		chart := buildTUITrafficChart(
			snapshot.TrafficHistory,
			maxTUIWidth(width-4, 1),
			plotHeight,
		)
		tuiTrafficTitle(b, snapshot.Traffic, chart.peak, width)
		for _, line := range chart.lines {
			writeTUIAnsiRow(b, line, width)
		}
		tuiEndPanel(b, width)
	}

	if height >= 17 {
		overview := tuiMemoryRows(snapshot)
		overview = append(overview,
			fmt.Sprintf(
				"Network speed ↑ %s/s   ↓ %s/s",
				formatBytes(snapshot.Traffic.Up),
				formatBytes(snapshot.Traffic.Down),
			),
			fmt.Sprintf(
				"Traffic total ↑ %s   ↓ %s",
				formatBytes(snapshot.TotalTraffic.Up),
				formatBytes(snapshot.TotalTraffic.Down),
			),
			fmt.Sprintf(
				"Activity      %d active · %d history entries",
				len(snapshot.Connections),
				len(snapshot.Requests),
			),
			"TUI frontends "+formatCLIFrontendSummary(snapshot.Frontends),
			fmt.Sprintf("Config        %s", paths.configPath),
		)
		tuiTitle(b, "Overview", "memory refresh 1s · live status", width)
		for _, row := range overview {
			tuiRow(b, row, width, false, "")
		}
		tuiEndPanel(b, width)
	}
}

type tuiDashboardCompactRow struct {
	value    string
	color    string
	selected bool
	ansi     bool
}

func renderTUICompactDashboard(
	snapshot tuiSnapshot,
	paths cliPaths,
	width,
	height int,
) string {
	rows := tuiCompactDashboardRows(snapshot, paths, width, height)
	limit := maxTUIWidth(height-3, 1)
	maxStart := maxTUIIndex(len(rows) - limit)
	start := snapshot.DashboardScroll
	if start > maxStart {
		start = maxStart
	}
	if start < 0 {
		start = 0
	}
	end := minTUI(start+limit, len(rows))
	var b strings.Builder
	tuiTitle(
		&b,
		"Dashboard",
		fmt.Sprintf(
			"rows %d-%d/%d · PgUp/PgDn scroll",
			start+1,
			end,
			len(rows),
		),
		width,
	)
	for _, row := range rows[start:end] {
		if row.ansi {
			writeTUIAnsiRow(&b, row.value, width)
			continue
		}
		tuiRow(
			&b,
			row.value,
			width,
			row.selected,
			row.color,
		)
	}
	tuiEndPanel(&b, width)
	return b.String()
}

func tuiCompactDashboardRows(
	snapshot tuiSnapshot,
	paths cliPaths,
	width int,
	height int,
) []tuiDashboardCompactRow {
	controls := []string{
		fmt.Sprintf("Core          %s", tuiServiceLabel(snapshot)),
		fmt.Sprintf(
			"System proxy  %s",
			tuiSystemProxyLabel(snapshot),
		),
		fmt.Sprintf("TUN           %s", tuiTUNLabel(snapshot)),
		fmt.Sprintf("Mode          %s", snapshot.Settings.Mode),
		fmt.Sprintf("Proxy port    %s", tuiProxyPortLabel(snapshot)),
	}
	rows := make([]tuiDashboardCompactRow, 0, 28)
	for index, control := range controls {
		rows = append(rows, tuiDashboardCompactRow{
			value: control,
			selected: index == snapshot.SelectedDashboard &&
				!snapshot.FocusSidebar,
		})
	}
	chart := buildTUITrafficChart(
		snapshot.TrafficHistory,
		maxTUIWidth(width-4, 1),
		tuiCompactTrafficChartHeight(height),
	)
	rows = append(rows, tuiDashboardCompactRow{
		value: tuiCyan + "── Live traffic" + tuiReset + " · " +
			formatTUITrafficLegend(snapshot.Traffic, chart.peak),
		ansi: true,
	})
	for _, line := range chart.lines {
		rows = append(rows, tuiDashboardCompactRow{value: line, ansi: true})
	}

	publicIP := "Checking..."
	if snapshot.Network.PublicIP != "" {
		publicIP = snapshot.Network.PublicIP
		if snapshot.Network.Country != "" {
			publicIP += " [" + snapshot.Network.Country + "]"
		}
	} else if snapshot.Network.Error != "" &&
		!snapshot.Network.Loading {
		publicIP = "Unavailable · n retry"
	}
	intranetIP := snapshot.Network.IntranetIP
	if intranetIP == "" {
		if snapshot.Network.Loading {
			intranetIP = "Detecting..."
		} else {
			intranetIP = "No active LAN address"
		}
	}
	rows = append(
		rows,
		tuiDashboardCompactRow{
			value: "── Network · d " +
				strings.ToLower(tuiDashboardRouteName(snapshot)) +
				" latency · v Cloudflare speed · n refresh",
			color: tuiCyan,
		},
		tuiDashboardCompactRow{
			value: "Public IP     " + publicIP,
			color: tuiCyan,
		},
		tuiDashboardCompactRow{
			value: "Intranet IP   " + intranetIP,
			color: tuiGreen,
		},
		tuiDashboardCompactRow{
			value: tuiPadRight(tuiDashboardRouteName(snapshot), 15) +
				tuiDashboardDelayLabel(snapshot.DashboardDelay),
			color: tuiCyan,
			selected: snapshot.SelectedDashboard == tuiDashboardDelayRow &&
				!snapshot.FocusSidebar,
		},
		tuiDashboardCompactRow{
			value: "Cloudflare DL  " +
				tuiSpeedResultLabel(snapshot.DashboardSpeed),
			color: tuiGreen,
			selected: snapshot.SelectedDashboard == tuiDashboardSpeedRow &&
				!snapshot.FocusSidebar,
		},
		tuiDashboardCompactRow{
			value: "── Overview · live status",
			color: tuiCyan,
		},
	)
	for _, row := range tuiMemoryRows(snapshot) {
		rows = append(rows, tuiDashboardCompactRow{value: row})
	}
	rows = append(
		rows,
		tuiDashboardCompactRow{
			value: fmt.Sprintf(
				"Network speed ↑ %s/s ↓ %s/s",
				formatBytes(snapshot.Traffic.Up),
				formatBytes(snapshot.Traffic.Down),
			),
		},
		tuiDashboardCompactRow{
			value: fmt.Sprintf(
				"Traffic total ↑ %s ↓ %s",
				formatBytes(snapshot.TotalTraffic.Up),
				formatBytes(snapshot.TotalTraffic.Down),
			),
		},
		tuiDashboardCompactRow{
			value: fmt.Sprintf(
				"Activity      %d active · %d history entries",
				len(snapshot.Connections),
				len(snapshot.Requests),
			),
		},
		tuiDashboardCompactRow{
			value: "TUI frontends " +
				formatCLIFrontendSummary(snapshot.Frontends),
		},
		tuiDashboardCompactRow{
			value: "Config        " + paths.configPath,
		},
	)
	return rows
}

func tuiDashboardRouteName(snapshot tuiSnapshot) string {
	if strings.EqualFold(snapshot.Settings.Mode, tuiSilentMode) {
		return "Silent route"
	}
	return "Rule route"
}

func tuiCompactTrafficChartHeight(height int) int {
	switch {
	case height <= 10:
		return 1
	case height <= 14:
		return 2
	default:
		return 3
	}
}

func tuiDashboardDelayLabel(delay tuiDelayResult) string {
	switch {
	case delay.MedianMillis > 0:
		return formatTUIDelay(delay) + " · d retest"
	case delay.Error != "":
		return "Timeout · d retry"
	case delay.Testing:
		return "Testing..."
	default:
		return "Not tested · press d"
	}
}

func formatTUIDelay(result tuiDelayResult) string {
	if result.Samples <= 1 {
		return fmt.Sprintf("%d ms", result.MedianMillis)
	}
	return fmt.Sprintf(
		"%d ms · jitter %d ms · %d samples",
		result.MedianMillis,
		result.JitterMillis,
		result.Samples,
	)
}

func tuiSpeedResultLabel(result tuiSpeedResult) string {
	switch {
	case result.Testing:
		return "Testing 4 streams · up to 100 MB/5s..."
	case result.Error != "":
		return "Failed · v retry"
	case result.BytesPerSecond > 0:
		downloaded := float64(result.Bytes) / 1_000_000
		duration := float64(result.DurationMillis) / 1000
		return fmt.Sprintf(
			"%s · %.1f MB in %.2fs · v retest",
			formatTUISpeed(result),
			downloaded,
			duration,
		)
	default:
		return "Not tested · press v"
	}
}

func tuiMemoryRows(snapshot tuiSnapshot) []string {
	systemMemory := "Unavailable"
	if snapshot.Memory.SystemTotal > 0 {
		percentage := float64(snapshot.Memory.SystemUsed) /
			float64(snapshot.Memory.SystemTotal) * 100
		systemMemory = fmt.Sprintf(
			"%s / %s  %.1f%%",
			formatTUIUintBytes(snapshot.Memory.SystemUsed),
			formatTUIUintBytes(snapshot.Memory.SystemTotal),
			percentage,
		)
	}
	rows := []string{"System memory " + systemMemory}
	if snapshot.ExternalCore || snapshot.ManagedService {
		processMemory := "Measuring..."
		if snapshot.Memory.ProcessRSS > 0 {
			processMemory = formatTUIUintBytes(snapshot.Memory.ProcessRSS)
		}
		coreMemory := "Measuring..."
		if snapshot.Memory.CoreRSS > 0 {
			coreMemory = formatTUIUintBytes(snapshot.Memory.CoreRSS)
		} else if snapshot.Memory.CoreError != "" {
			coreMemory = "Unavailable · retrying"
		}
		rows = append(
			rows,
			"TUI process   "+processMemory,
			func() string {
				if snapshot.ManagedService {
					return "Managed Core  " + coreMemory
				}
				return "External Core " + coreMemory
			}(),
		)
	} else {
		processMemory := "Measuring..."
		if snapshot.Memory.ProcessRSS > 0 {
			processMemory = formatTUIUintBytes(snapshot.Memory.ProcessRSS)
		}
		rows = append(
			rows,
			"CLI + Mihomo  "+processMemory+" RSS · shared process",
		)
	}
	goHeap := "Measuring..."
	if snapshot.Memory.GoHeap > 0 {
		goHeap = formatTUIUintBytes(snapshot.Memory.GoHeap)
	}
	return append(rows, "Go heap       "+goHeap)
}

func formatTUIUintBytes(value uint64) string {
	const maxInt64 = uint64(^uint64(0) >> 1)
	if value > maxInt64 {
		value = maxInt64
	}
	return formatBytes(int64(value))
}

func drawTUIRequests(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	tuiTitle(
		b,
		"History",
		fmt.Sprintf("%d shared entries · active and completed · x clear", len(snapshot.Requests)),
		width,
	)
	if len(snapshot.Requests) == 0 {
		tuiRow(b, "No connection history captured by Backend", width, false, tuiDim)
		tuiEndPanel(b, width)
		return
	}
	limit := maxTUIWidth((height-3)/2, 1)
	start, end := tuiVisibleRange(
		len(snapshot.Requests),
		snapshot.SelectedRequest,
		limit,
	)
	rows := 0
	for index := start; index < end && rows < height-3; index++ {
		request := snapshot.Requests[index]
		host := request.Host
		if host == "" {
			host = request.ID
		}
		state := "done"
		color := tuiDim
		if request.Active {
			state = "active"
			color = tuiGreen
		}
		row := fmt.Sprintf(
			"%-7s %-30s %-7s %-18s %s",
			state,
			truncateTUI(host, 30),
			request.Network,
			truncateTUI(request.Chain, 18),
			request.LastSeen.Format("15:04:05"),
		)
		tuiRow(
			b,
			row,
			width,
			index == snapshot.SelectedRequest && !snapshot.FocusSidebar,
			color,
		)
		rows++
		if (request.Process != "" || request.UID != 0) && rows < height-3 {
			process := request.Process
			if request.UID != 0 {
				if process == "" {
					process = fmt.Sprintf("UID %d", request.UID)
				} else {
					process = fmt.Sprintf("%s · UID %d", process, request.UID)
				}
			}
			tuiRow(
				b,
				"process: "+strings.TrimSpace(process),
				width,
				false,
				tuiDim,
			)
			rows++
		}
	}
	tuiEndPanel(b, width)
}

func drawTUIConnections(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	tuiTitle(b, "Connections", fmt.Sprintf("%d active", len(snapshot.Connections)), width)
	if len(snapshot.Connections) == 0 {
		tuiRow(b, "No active connections", width, false, tuiDim)
		tuiEndPanel(b, width)
		return
	}
	limit := maxTUIWidth((height-3)/2, 1)
	start, end := tuiVisibleRange(len(snapshot.Connections), snapshot.SelectedConnection, limit)
	rows := 0
	for index := start; index < end; index++ {
		connection := snapshot.Connections[index]
		if rows >= height-3 {
			break
		}
		label := connection.Host
		if label == "" {
			label = connection.ID
		}
		row := fmt.Sprintf("%-32s %-7s %-18s ↑%-9s ↓%-9s", truncateTUI(label, 32), connection.Network, truncateTUI(connection.Chain, 18), formatBytes(connection.Upload), formatBytes(connection.Download))
		tuiRow(b, row, width, index == snapshot.SelectedConnection && !snapshot.FocusSidebar, "")
		rows++
		if (connection.Process != "" || connection.UID != 0) && rows < height-3 {
			process := connection.Process
			if connection.UID != 0 {
				if process == "" {
					process = fmt.Sprintf("UID %d", connection.UID)
				} else {
					process = fmt.Sprintf("%s · UID %d", process, connection.UID)
				}
			}
			tuiRow(b, "process: "+strings.TrimSpace(process), width, false, tuiDim)
			rows++
		}
	}
	tuiEndPanel(b, width)
}

func drawTUILogs(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	tuiTitle(b, "Logs", "latest events · e export · x clear", width)
	limit := maxTUIWidth(height-3, 1)
	start := maxTUIIndex(len(snapshot.Logs) - limit)
	for _, line := range snapshot.Logs[start:] {
		tuiRow(b, line, width, false, tuiDim)
	}
	if len(snapshot.Logs) == 0 {
		tuiRow(b, "No logs captured yet", width, false, tuiDim)
	}
	tuiEndPanel(b, width)
}

func formatTUIDestination(host, port string) string {
	if host == "" {
		return ""
	}
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}

func exportTUILogs(homeDir string, logs []string) (string, error) {
	if len(logs) == 0 {
		return "", errors.New("no logs captured yet")
	}
	logDir := filepath.Join(homeDir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(
		logDir,
		"flclash-"+time.Now().Format("20060102-150405")+".log",
	)
	return path, os.WriteFile(path, []byte(strings.Join(logs, "\n")+"\n"), 0o600)
}

func drawTUISettings(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	rows := tuiSettingsRows(snapshot)
	subtitle := "Core is stopped · changes are staged until it starts"
	if snapshot.ExternalCore {
		subtitle = "↑/↓ select · Enter change · external core configuration"
	} else if snapshot.ServiceRunning {
		subtitle = "Core is running · changes apply immediately"
	}
	tuiTitle(b, "Settings", subtitle, width)
	rowLimit := minTUI(len(rows), maxTUIWidth(height-3, 1))
	if height >= len(rows)+7 {
		rowLimit = len(rows)
	}
	for index, row := range rows[:rowLimit] {
		tuiRow(b, row, width, index == snapshot.SelectedSetting && !snapshot.FocusSidebar, "")
	}
	tuiEndPanel(b, width)
	if height >= len(rows)+7 {
		tuiEmptyPanel(b, "Optional keys", "a LAN · v IPv6 · t TUN · m mode · p port · +/- adjust · S system proxy · c Core", width)
	}
}

func tuiSettingsRows(snapshot tuiSnapshot) []string {
	return []string{
		fmt.Sprintf("Mode          %s", snapshot.Settings.Mode),
		fmt.Sprintf("FLC outbound  %s", tuiFLCOutboundLabel(snapshot)),
		fmt.Sprintf("Proxy port    %s", tuiProxyPortLabel(snapshot)),
		fmt.Sprintf("Allow LAN     %s", tuiOnOff(snapshot.Settings.AllowLAN)),
		fmt.Sprintf("IPv6          %s", tuiOnOff(snapshot.Settings.IPv6)),
		fmt.Sprintf(
			"Unified delay %s · warm connection, matches FlClash",
			tuiOnOff(snapshot.Settings.UnifiedDelay),
		),
		fmt.Sprintf(
			"TCP concurrent %s · race available addresses",
			tuiOnOff(snapshot.Settings.TCPConcurrent),
		),
		fmt.Sprintf("Log level     %s", snapshot.Settings.LogLevel),
		fmt.Sprintf("TUN scope     %s", strings.ToUpper(tuiEffectiveTunScope(snapshot.Settings.TunScope))),
		fmt.Sprintf("TUN           %s", tuiTUNLabel(snapshot)),
		fmt.Sprintf("Core          %s", tuiServiceLabel(snapshot)),
		fmt.Sprintf("System proxy  %s", tuiSystemProxyLabel(snapshot)),
	}
}

func tuiFLCOutboundLabel(snapshot tuiSnapshot) string {
	value := cliDisplayValue(snapshot.FLCOutbound)
	if snapshot.Settings.Mode != tuiSilentMode || snapshot.FLCOutbound == "" {
		return value
	}
	if snapshot.FLCEnabled {
		return value + " · READY"
	}
	return value + " · WAITING FOR CORE"
}

func tuiServiceLabel(snapshot tuiSnapshot) string {
	if snapshot.ExternalCore {
		return "EXTERNAL · managed by another process"
	}
	if snapshot.ServiceRunning {
		return "RUNNING · Enter to stop"
	}
	return "STOPPED · Enter to start"
}

func tuiSystemProxyLabel(snapshot tuiSnapshot) string {
	if snapshot.Settings.Mode == tuiSilentMode {
		return "DISABLED · locked by silent mode"
	}
	if snapshot.Settings.SystemProxy {
		return "ENABLED · Enter to disable"
	}
	if !snapshot.ExternalCore && !snapshot.ServiceRunning {
		return "DISABLED · Enter to enable (starts Core)"
	}
	return "DISABLED · Enter to enable"
}

func tuiTUNLabel(snapshot tuiSnapshot) string {
	if snapshot.Settings.Mode == tuiSilentMode {
		return "OFF · locked by silent mode"
	}
	scope := snapshot.Settings.TunScope
	if scope == "" {
		scope = tuiTunScopeUser
	}
	if snapshot.Settings.TunEnabled {
		return strings.ToUpper(scope) + " ON"
	}
	return "OFF · " + scope
}

func tuiEffectiveTunScope(scope string) string {
	if scope == "" {
		return tuiTunScopeUser
	}
	return scope
}

func tuiProxyPortLabel(snapshot tuiSnapshot) string {
	if snapshot.ActiveProxyPort > 0 {
		return strconv.Itoa(snapshot.ActiveProxyPort)
	}
	if snapshot.Settings.MixedPort > 0 {
		return strconv.Itoa(snapshot.Settings.MixedPort)
	}
	return "NOT READY"
}

func tuiOnOff(enabled bool) string {
	if enabled {
		return "ON"
	}
	return "OFF"
}

func drawTUIProfiles(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	tuiTitle(
		b,
		"Profiles",
		fmt.Sprintf(
			"%d available · Enter activate · U refresh linked · e edit · F2/u rename · x delete",
			len(snapshot.Profiles),
		),
		width,
	)
	limit := maxTUIWidth(height-3, 1)
	selectedPosition := snapshot.SelectedRow + tuiProfileImportRowCount
	start, end := tuiVisibleRange(
		len(snapshot.Profiles)+tuiProfileImportRowCount,
		selectedPosition,
		limit,
	)
	for position := start; position < end; position++ {
		if position == 0 {
			tuiRow(
				b,
				"+ Import subscription URL",
				width,
				snapshot.SelectedRow == tuiProfileImportSubscriptionRow &&
					!snapshot.FocusSidebar,
				tuiCyan,
			)
			continue
		}
		if position == 1 {
			tuiRow(
				b,
				"+ Import local YAML file",
				width,
				snapshot.SelectedRow == tuiProfileImportFileRow &&
					!snapshot.FocusSidebar,
				tuiCyan,
			)
			continue
		}
		index := position - tuiProfileImportRowCount
		profile := snapshot.Profiles[index]
		label := truncateTUI(profile.Name, width-42)
		if profile.Current {
			if index == snapshot.SelectedRow && !snapshot.FocusSidebar {
				if profile.SubscriptionURL != "" {
					label += "  [active · U refresh · e edit · x locked]"
				} else {
					label += "  [active · local · e edit · x locked]"
				}
			} else {
				label += "  [active]"
			}
		} else if index == snapshot.SelectedRow && !snapshot.FocusSidebar {
			if profile.SubscriptionURL != "" {
				label += "  [Enter activate · U refresh · e edit · F2 rename · x delete]"
			} else {
				label += "  [Enter activate · local · e edit · F2 rename · x delete]"
			}
		}
		tuiRow(
			b,
			label,
			width,
			index == snapshot.SelectedRow && !snapshot.FocusSidebar,
			tuiGreen,
		)
	}
	tuiEndPanel(b, width)
}

func drawTUISSH(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	tuiTitle(
		b,
		"SSH",
		"Local traffic → SSH host exit · Enter connect/disconnect · n add · e edit · x delete · d test",
		width,
	)
	if len(snapshot.SSHProfiles) == 0 {
		tuiRow(b, "No SSH profiles · press n to add one", width, false, tuiDim)
		tuiEndPanel(b, width)
		return
	}
	limit := maxTUIWidth(height-3, 1)
	start, end := tuiVisibleRange(
		len(snapshot.SSHProfiles),
		snapshot.SelectedSSH,
		limit,
	)
	for index := start; index < end; index++ {
		profile := snapshot.SSHProfiles[index]
		status := "DISCONNECTED"
		endpoint := ""
		color := tuiDim
		if profile.Connected {
			status = "CONNECTED"
			configured := "auto"
			if profile.LocalPort > 0 {
				configured = strconv.Itoa(profile.LocalPort)
			}
			endpoint = fmt.Sprintf(
				" · SOCKS5 127.0.0.1:%d · configured %s",
				profile.SocksPort,
				configured,
			)
			color = tuiGreen
		} else if profile.LocalPort > 0 {
			endpoint = fmt.Sprintf(" · local 127.0.0.1:%d", profile.LocalPort)
		} else {
			endpoint = " · local auto"
		}
		auth := "agent/key"
		if profile.PasswordSet {
			auth = "password ********"
		} else if profile.Identity != "" {
			auth = "key " + profile.Identity
		}
		row := fmt.Sprintf(
			"%-18s %-12s %s:%d · %s%s",
			truncateTUI(profile.Name, 18),
			status,
			truncateTUI(profile.Destination, 28),
			profile.Port,
			auth,
			endpoint,
		)
		tuiRow(
			b,
			row,
			width,
			index == snapshot.SelectedSSH && !snapshot.FocusSidebar,
			color,
		)
	}
	tuiEndPanel(b, width)
}

func truncateTUI(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if tuiDisplayWidth(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	var b strings.Builder
	visibleWidth := 0
	for _, runeValue := range value {
		runeWidth := tuiRuneWidth(runeValue)
		if visibleWidth+runeWidth > width-1 {
			break
		}
		b.WriteRune(runeValue)
		visibleWidth += runeWidth
	}
	b.WriteRune('…')
	return b.String()
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
