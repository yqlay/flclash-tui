//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/metacubex/mihomo/config"
	logrus "github.com/sirupsen/logrus"
	"golang.org/x/sys/unix"
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
	tuiPageSSH
	tuiPageProxies
	tuiPageProfiles
	tuiPageRequests
	tuiPageConnections
	tuiPageLogs
	tuiPageTools
	tuiPageMaintenance
	tuiPageCount
)

const (
	tuiSSHDashboardTunnelRow = iota
	tuiSSHDashboardDirectExitRow
	tuiSSHDashboardDirectRTTRow
	tuiSSHDashboardDirectSpeedRow
	tuiSSHDashboardManagedIPRow
	tuiSSHDashboardManagedRTTRow
	tuiSSHDashboardManagedSpeedRow
	tuiSSHDashboardRowCount
)

type tuiConnection struct {
	ID          string `json:"id"`
	Host        string `json:"host"`
	Process     string `json:"process,omitempty"`
	ProcessPath string `json:"process_path,omitempty"`
	UID         uint32 `json:"uid,omitempty"`
	SourceIP    string `json:"source_ip,omitempty"`
	InboundName string `json:"inbound_name,omitempty"`
	InboundUser string `json:"inbound_user,omitempty"`
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
	tuiSettingsAllowLANRow = iota
	tuiSettingsIPv6Row
	tuiSettingsUnifiedDelayRow
	tuiSettingsTCPConcurrentRow
	tuiSettingsLogLevelRow
	tuiSettingsTunScopeRow
	tuiSettingsRowCount
)

const (
	tuiDashboardServiceRow = iota
	tuiDashboardSystemProxyRow
	tuiDashboardTunRow
	tuiDashboardModeRow
	tuiDashboardFLCOutboundRow
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
	Name          string
	Username      string
	Host          string
	Destination   string
	Port          int
	LocalPort     int
	Jump          string
	Identity      string
	PassphraseSet bool
	PasswordSet   bool
	NeedsUsername bool
	Default       bool
	LastError     string
	Options       []string
	Connected     bool
	Attached      bool
	Attachable    bool
	Ready         bool
	SocksPort     int
	StartedAt     time.Time
}

const (
	tuiSSHFormNameRow = iota
	tuiSSHFormUsernameRow
	tuiSSHFormHostRow
	tuiSSHFormJumpRow
	tuiSSHFormPortRow
	tuiSSHFormLocalPortRow
	tuiSSHFormIdentityRow
	tuiSSHFormPassphraseRow
	tuiSSHFormPasswordRow
	tuiSSHFormOptionStartRow
)

const tuiSSHFormDestinationRow = tuiSSHFormHostRow

type tuiSSHFormView struct {
	Open              bool
	Existing          bool
	ReadOnly          bool
	Name              string
	Username          string
	Host              string
	Destination       string
	Jump              string
	Port              int
	LocalPort         int
	Identity          string
	IdentityKind      cliSSHIdentityKind
	IdentityError     string
	PassphraseSet     bool
	PassphraseChanged bool
	PassphraseCleared bool
	PasswordSet       bool
	PasswordChanged   bool
	PasswordCleared   bool
	Options           []string
	Selected          int
	FieldEditing      bool
	FieldInput        string
	PassphraseConfirm bool
	PasswordConfirm   bool
	DeleteConfirmOpen bool
	DeleteName        string
}

type tuiSSHCredentialPromptView struct {
	Open     bool
	Profile  string
	Identity string
	Value    string
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
	tuiSSHCaptureRow                = -1
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
	SSHDashboardFocus      bool
	SelectedSSHDetail      int
	SSHNetwork             tuiNetworkInfo
	SSHDelay               tuiDelayResult
	SSHSpeed               tuiSpeedResult
	SSHDirectProbe         cliSSHRemoteProbe
	SSHDirectNetwork       tuiNetworkInfo
	SSHDirectDelay         tuiDelayResult
	SSHDirectSpeed         tuiSpeedResult
	SSHTraffic             trafficSnapshot
	SSHTrafficHistory      []trafficSnapshot
	SSHTotalTraffic        trafficSnapshot
	SSHConnections         int64
	HistoryQuery           string
	HistoryFilter          string
	HistoryDetailOpen      bool
	ConnectionsQuery       string
	ConnectionsDetailOpen  bool
	LogsQuery              string
	LogsLevel              string
	LogDetailOpen          bool
	SelectedLog            int
	DangerConfirmOpen      bool
	DangerConfirmTitle     string
	DangerConfirmMessage   string
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
	SSHCredentialPrompt    tuiSSHCredentialPromptView
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
geodata-loader: memconservative
geodata-mode: false
proxy-groups:
  - name: PROXY
    type: select
    proxies:
      - DIRECT
rules:
  - MATCH,PROXY
`

var tuiParentPID = os.Getppid

var setTUIParentExitSignal = func() error {
	return unix.Prctl(
		unix.PR_SET_PDEATHSIG,
		uintptr(syscall.SIGHUP),
		0,
		0,
		0,
	)
}

// armTUIParentExitSignal covers terminal hosts that terminate the shell
// directly without delivering SIGHUP to the foreground process group. The
// parent is checked again because it can exit in the small window before
// PR_SET_PDEATHSIG is installed.
func armTUIParentExitSignal() error {
	parentPID := tuiParentPID()
	if parentPID <= 1 {
		return nil
	}
	if err := setTUIParentExitSignal(); err != nil {
		return fmt.Errorf("arm TUI parent exit signal: %w", err)
	}
	if tuiParentPID() != parentPID {
		return errors.New("terminal parent exited while the TUI was starting")
	}
	return nil
}

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
	if err := armTUIParentExitSignal(); err != nil {
		return err
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
		if _, pathErr := tuiProfileStateKey(paths.HomeDir, status.ConfigPath); pathErr != nil {
			return fmt.Errorf("background service returned an invalid profile path: %w", pathErr)
		}
		if status.HomeDir != "" {
			paths.HomeDir = status.HomeDir
		}
		paths.ConfigPath = status.ConfigPath
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
		paths.HomeDir,
		paths.ConfigPath,
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
	_, err := os.Stat(paths.ConfigPath)
	if err == nil {
		return nil
	}
	if !os.IsNotExist(err) || !allowCreate {
		return fmt.Errorf("config file %q: %w", paths.ConfigPath, err)
	}
	if err := os.MkdirAll(filepath.Dir(paths.ConfigPath), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(paths.ConfigPath, []byte(defaultTUIConfig), 0o600); err != nil {
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
	configData, err := os.ReadFile(paths.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("config file %q: %w", paths.ConfigPath, err)
	}
	if err := os.MkdirAll(paths.HomeDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	if err := ensureTUIBundledGeoData(paths.HomeDir); err != nil {
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
		HomeDir:    paths.HomeDir,
		ConfigPath: paths.ConfigPath,
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
		SelectedMap: loadTUISelectedProxies(paths.HomeDir),
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
