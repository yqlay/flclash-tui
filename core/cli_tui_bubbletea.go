//go:build linux && !cgo && cli

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/metacubex/mihomo/config"
	logrus "github.com/sirupsen/logrus"
)

const (
	tuiRefreshInterval = 2 * time.Second
	tuiProgramFPS      = 15
)

type tuiTickMsg time.Time

// tuiIdleTickPlan is the work a periodic tick may start. Public-IP / delay /
// speed probes are never part of a tick; they stay user- or event-triggered.
type tuiIdleTickPlan struct {
	RefreshSnapshot bool
	FetchHistory    bool
	FetchLogs       bool
	SampleMemory    bool
	PollSSH         bool
	CheckNetwork    bool
}

func tuiIdleTickPlanFor(page tuiPage) tuiIdleTickPlan {
	switch page {
	case tuiPageDashboard:
		return tuiIdleTickPlan{RefreshSnapshot: true, SampleMemory: true}
	case tuiPageProxies, tuiPageConnections:
		return tuiIdleTickPlan{RefreshSnapshot: true}
	case tuiPageRequests:
		return tuiIdleTickPlan{RefreshSnapshot: true, FetchHistory: true}
	case tuiPageLogs:
		return tuiIdleTickPlan{RefreshSnapshot: true, FetchLogs: true}
	case tuiPageSSH:
		return tuiIdleTickPlan{PollSSH: true}
	default:
		return tuiIdleTickPlan{}
	}
}

type tuiInterruptSignalMsg struct{}
type tuiTerminalExitSignalMsg struct{}

// tuiTerminalInput preserves the terminal file descriptor Bubble Tea needs
// for raw mode and cancellable reads while reporting EOF separately. Bubble
// Tea deliberately treats EOF as a completed input reader, not as a request
// to terminate the Program, which can otherwise leave a TUI alive after its
// PTY disappears.
type tuiTerminalInput struct {
	file        *os.File
	terminalEOF chan<- struct{}
	eofOnce     sync.Once
}

func newTUITerminalInput(
	file *os.File,
	terminalEOF chan<- struct{},
) *tuiTerminalInput {
	return &tuiTerminalInput{
		file:        file,
		terminalEOF: terminalEOF,
	}
}

func (input *tuiTerminalInput) Read(buffer []byte) (int, error) {
	count, err := input.file.Read(buffer)
	if errors.Is(err, io.EOF) {
		input.eofOnce.Do(func() {
			select {
			case input.terminalEOF <- struct{}{}:
			default:
			}
		})
	}
	return count, err
}

func (input *tuiTerminalInput) Write(buffer []byte) (int, error) {
	return input.file.Write(buffer)
}

func (input *tuiTerminalInput) Close() error {
	// Bubble Tea only requires this method to create its cancellable reader.
	// It must not close process-wide stdin during normal TUI cleanup.
	return nil
}

func (input *tuiTerminalInput) Fd() uintptr {
	return input.file.Fd()
}

func (input *tuiTerminalInput) Name() string {
	return input.file.Name()
}

type tuiServiceWatchMsg struct {
	status tuiServiceStatus
	err    error
}

type tuiShutdownResultMsg struct {
	err error
}

type tuiNetworkResultMsg struct {
	info  tuiNetworkInfo
	route string
}

type tuiMemoryResultMsg struct {
	info tuiMemoryInfo
}

type tuiCoreMemoryMsg struct {
	update tuiCoreMemoryUpdate
}

type tuiTrafficMsg struct {
	update tuiTrafficUpdate
}

type tuiRefreshResultMsg struct {
	sequence      uint64
	snapshot      tuiSnapshot
	serviceStatus *tuiServiceStatus
}

type tuiOperationState struct {
	snapshot           tuiSnapshot
	paths              cliPaths
	setupParams        []byte
	coreRunning        bool
	systemProxyManaged bool
	pendingMixedPort   *int
	stagedSettings     *tuiSettings
	settingsDirty      bool
	backendRevision    uint64
	profileSelection   string
	networkChanged     bool
}

type tuiOperationResultMsg struct {
	state tuiOperationState
}

type tuiProxyGroupSpeedResultMsg struct {
	groupName string
	node      string
	result    tuiSpeedResult
	remaining []string
	total     int
	successes int
}

type tuiEditorResultMsg struct {
	err error
}

type tuiSSHCommandResultMsg struct {
	action       string
	status       string
	selectedName string
	err          error
}

type tuiSSHCaptureResultMsg struct {
	generation uint64
	names      []string
	options    []string
	selected   int
	candidates []cliSSHCaptureCandidate
	hint       string
}

type tuiSSHRelayStatsMsg struct {
	name  string
	stats cliSSHRelayStats
	at    time.Time
	err   error
}

type tuiSSHNetworkResultMsg struct {
	name   string
	direct bool
	info   tuiNetworkInfo
}

type tuiSSHDelayResultMsg struct {
	name   string
	direct bool
	result tuiDelayResult
	err    error
}

type tuiSSHSpeedResultMsg struct {
	name   string
	direct bool
	result tuiSpeedResult
	err    error
}

type tuiSSHDirectProbeResultMsg struct {
	name  string
	probe cliSSHRemoteProbe
	err   error
}

type tuiInputMode byte

const (
	tuiInputNone tuiInputMode = iota
	tuiInputMixedPort
	tuiInputSubscription
	tuiInputProfileFile
	tuiInputProfileName
	tuiInputHistorySearch
	tuiInputConnectionsSearch
	tuiInputLogsSearch
)

var tuiTrafficModes = []string{
	"rule",
	tuiSilentMode,
	"global",
	"direct",
}

type tuiModel struct {
	snapshot                 tuiSnapshot
	client                   controllerClient
	service                  *tuiServiceClient
	paths                    cliPaths
	setupParams              []byte
	ownsCore                 bool
	coreRunning              bool
	systemProxyManaged       bool
	width                    int
	height                   int
	refreshSequence          uint64
	refreshInFlight          bool
	refreshIncludesHistory   bool
	refreshIncludesLogs      bool
	lastIdleTick             tuiIdleTickPlan
	busy                     bool
	inputMode                tuiInputMode
	inputValue               []rune
	inputCursor              int
	inputSelectAll           bool
	modeSelectionOpen        bool
	selectedMode             int
	renameProfilePath        string
	editorPath               string
	editorTempPath           string
	editorBackup             tuiProfileBackup
	pendingMixedPort         *int
	stagedSettings           *tuiSettings
	settingsDirty            bool
	backendRevision          uint64
	networkCheckActive       bool
	memoryRefreshActive      bool
	coreMemoryUpdates        <-chan tuiCoreMemoryUpdate
	stopCoreMemory           func()
	trafficUpdates           <-chan tuiTrafficUpdate
	stopTraffic              func()
	stopServiceOnExit        bool // Legacy test visibility; managed frontends always leave this false.
	frontendExitRequested    bool
	shutdownRequested        bool
	notifications            []tuiNotification
	notificationDetailOpen   bool
	notificationSelected     int
	notificationScroll       int
	sshFormOpen              bool
	sshFormExisting          bool
	sshFormReadOnly          bool
	sshFormOriginalName      string
	sshFormFingerprint       string
	sshForm                  cliSSHProfile
	sshFormSelected          int
	sshFormFieldEditing      bool
	sshFormInput             []rune
	sshFormCursor            int
	sshFormSelectAll         bool
	sshFormAddingOption      bool
	sshFormPassphraseChanged bool
	sshFormPassphraseCleared bool
	sshFormPassphraseConfirm bool
	sshFormPassphraseFirst   string
	sshFormPasswordChanged   bool
	sshFormPasswordCleared   bool
	sshFormPasswordConfirm   bool
	sshFormPasswordFirst     string
	sshFormIdentityKind      cliSSHIdentityKind
	sshFormIdentityError     string
	sshDeleteConfirmOpen     bool
	sshDeleteName            string
	sshCredentialPromptOpen  bool
	sshCredentialProfile     string
	sshCredentialIdentity    string
	sshCredentialInput       []rune
	sshCaptureOpen           bool
	sshCaptureGeneration     uint64
	sshCaptureNames          []string
	sshCaptureOptions        []string
	sshCaptureCandidates     []cliSSHCaptureCandidate
	sshCaptureSelected       int
	sshLastStats             cliSSHRelayStats
	sshLastStatsAt           time.Time
	sshLastStatsName         string
	profileDeleteOpen        bool
	profileDeletePath        string
	profileDeleteName        string
	profileDeleteKind        string
	dangerConfirmOpen        bool
	dangerConfirmTitle       string
	dangerConfirmMessage     string
	dangerConfirmKey         tuiKey
	dangerConfirmTarget      string
	dangerConfirmed          bool
}

func newTUIModel(
	client controllerClient,
	paths cliPaths,
	setupParams []byte,
	ownsCore bool,
) *tuiModel {
	width, height := tuiTerminalSize()
	stagedSettings := loadTUIConfiguredSettings(paths.ConfigPath, ownsCore)
	settings := tuiSettings{}
	var pendingMixedPort *int
	if stagedSettings != nil {
		settings = *stagedSettings
		port := settings.MixedPort
		pendingMixedPort = &port
	}
	return &tuiModel{
		snapshot: tuiSnapshot{
			Status:            "Loading...",
			GroupOrder:        loadTUIProxyGroupOrder(paths.ConfigPath),
			Settings:          settings,
			SelectedGroup:     0,
			SelectedNode:      0,
			SelectedRow:       tuiProfileImportSubscriptionRow,
			SelectedMenu:      int(tuiPageDashboard),
			SelectedDashboard: tuiDashboardServiceRow,
			FocusSidebar:      true,
		},
		client:           client,
		paths:            paths,
		setupParams:      append([]byte(nil), setupParams...),
		ownsCore:         ownsCore,
		coreRunning:      false,
		width:            width,
		height:           height,
		pendingMixedPort: pendingMixedPort,
		stagedSettings:   stagedSettings,
	}
}

func loadTUIConfiguredSettings(path string, ownsCore bool) *tuiSettings {
	if !ownsCore {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	rawConfig, err := config.UnmarshalRawConfig(data)
	if err != nil {
		return nil
	}
	return &tuiSettings{
		Mode:          rawConfig.Mode.String(),
		MixedPort:     rawConfig.MixedPort,
		AllowLAN:      rawConfig.AllowLan,
		IPv6:          rawConfig.IPv6,
		UnifiedDelay:  rawConfig.UnifiedDelay,
		TCPConcurrent: rawConfig.TCPConcurrent,
		LogLevel:      rawConfig.LogLevel.String(),
		TunEnabled:    rawConfig.Tun.Enable,
	}
}

func runTUI(
	client controllerClient,
	paths cliPaths,
	setupParams []byte,
	ownsCore bool,
	service *tuiServiceClient,
	coreRunning bool,
	startupNotice string,
	interrupt <-chan os.Signal,
) error {
	if !isInteractiveTUI() {
		return errors.New("TUI requires an interactive terminal; use run or proxy commands in non-interactive shells")
	}

	logrus.SetOutput(io.Discard)
	cliHub.StartLog()
	defer cliHub.StopLog()

	model := newTUIModel(client, paths, setupParams, ownsCore)
	model.service = service
	if service != nil {
		if status, statusErr := service.status(); statusErr == nil {
			model.backendRevision = status.Revision
			model.snapshot.Settings.SystemProxy = status.SystemProxy
			model.snapshot.Settings.Mode = status.Mode
			model.snapshot.Settings.MixedPort = status.ConfiguredProxyPort
			model.snapshot.ConfiguredProxyPort = status.ConfiguredProxyPort
			model.snapshot.ActiveProxyPort = status.ActiveProxyPort
			model.snapshot.Settings.TunEnabled = status.TunState == "on"
			model.snapshot.Settings.TunScope = status.TunScope
			if status.Mode == tuiSilentMode {
				model.snapshot.Settings.TunEnabled = false
			}
			model.snapshot.FLCEnabled = status.FLCEnabled
			model.snapshot.FLCOutbound = status.FLCOutbound
		}
	}
	model.initializeCoreRuntime(coreRunning)
	model.snapshot.ManagedService = service != nil
	model.snapshot.Frontends, _ = listCLIFrontends()
	if startupNotice != "" {
		model.enqueueNotification(tuiNotification{
			level:   tuiNotificationInfo,
			title:   "Shared backend",
			message: startupNotice,
		})
	}
	defer model.stopCoreMemoryMonitor()
	defer model.stopTrafficMonitor()
	terminalEOF := make(chan struct{}, 1)
	programOptions := append(
		tuiProgramOptions(),
		tea.WithInput(newTUITerminalInput(os.Stdin, terminalEOF)),
	)
	program := tea.NewProgram(model, programOptions...)
	terminalExit := make(chan os.Signal, 1)
	signal.Notify(terminalExit, syscall.SIGHUP, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-interrupt:
				program.Send(tuiInterruptSignalMsg{})
			case <-terminalExit:
				program.Send(tuiTerminalExitSignalMsg{})
			case <-terminalEOF:
				program.Send(tuiTerminalExitSignalMsg{})
			case <-done:
				return
			}
		}
	}()

	_, runErr := program.Run()
	close(done)
	signal.Stop(terminalExit)
	model.shutdown()
	logrus.SetOutput(os.Stdout)
	if runErr != nil && !errors.Is(runErr, tea.ErrProgramKilled) {
		return fmt.Errorf("run TUI: %w", runErr)
	}
	return nil
}

func (m *tuiModel) initializeCoreRuntime(coreRunning bool) {
	m.coreRunning = coreRunning
	if !coreRunning {
		m.reconcileStoppedCoreState()
		return
	}
	m.pendingMixedPort = nil
	m.stagedSettings = nil
	m.settingsDirty = false
	m.snapshot.Settings = tuiSettings{}
}

// reconcileStoppedCoreState prevents a stopped Core from leaving stale active
// connections or ACTIVE History rows on screen while an asynchronous refresh
// is pending. Persistent History remains available, but every entry is closed.
func (m *tuiModel) reconcileStoppedCoreState() {
	if m.coreRunning {
		return
	}
	m.snapshot.Connections = nil
	m.snapshot.SelectedConnection = -1
	m.snapshot.ConnectionsDetailOpen = false
	m.snapshot.Requests, _ = markTUIRequestHistoryInactive(m.snapshot.Requests)
}

func tuiProgramOptions() []tea.ProgramOption {
	return []tea.ProgramOption{
		tea.WithAltScreen(),
		tea.WithFPS(tuiProgramFPS),
		tea.WithoutSignalHandler(),
	}
}

func tuiPageShowsLiveCoreStats(page tuiPage) bool {
	return page == tuiPageDashboard
}

func (m *tuiModel) syncLiveMonitors() []tea.Cmd {
	if !tuiPageShowsLiveCoreStats(m.snapshot.Page) {
		m.stopTrafficMonitor()
		m.stopCoreMemoryMonitor()
		return nil
	}
	startedTraffic := m.stopTraffic == nil
	startedMemory := m.stopCoreMemory == nil
	m.startTrafficMonitor()
	m.startCoreMemoryMonitor()
	var cmds []tea.Cmd
	if startedTraffic && m.stopTraffic != nil {
		cmds = append(cmds, m.waitTrafficUpdate())
	}
	if startedMemory && m.stopCoreMemory != nil {
		cmds = append(cmds, m.waitCoreMemoryUpdate())
	}
	return cmds
}

func (m *tuiModel) changeVisiblePage(page tuiPage) []tea.Cmd {
	m.snapshot.Page = page
	return m.syncLiveMonitors()
}

func (m *tuiModel) Init() tea.Cmd {
	cmds := []tea.Cmd{
		tuiTickCommand(),
		m.startRefresh(),
		m.startNetworkCheck(),
		m.startMemoryRefresh(),
		m.waitServiceUpdate(),
	}
	cmds = append(cmds, m.syncLiveMonitors()...)
	return tea.Batch(cmds...)
}

func (m *tuiModel) update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
		return m, nil
	case tuiTickMsg:
		return m, m.idleTickCommand()
	case tuiInterruptSignalMsg:
		return m, m.handleKey(tuiKeyInterrupt)
	case tuiTerminalExitSignalMsg:
		return m, m.handleKey(tuiKeyQuit)
	case tuiServiceWatchMsg:
		if message.err != nil {
			m.snapshot.Status = "Backend watch interrupted: " + message.err.Error()
			retry := m.waitServiceUpdate()
			return m, tea.Tick(time.Second, func(time.Time) tea.Msg {
				if retry == nil {
					return tuiServiceWatchMsg{}
				}
				return retry()
			})
		}
		if message.status.ShuttingDown {
			m.snapshot.Status = "Backend is shutting down"
			return m, tea.Quit
		}
		m.backendRevision = message.status.Revision
		m.coreRunning = message.status.Running
		m.reconcileStoppedCoreState()
		m.snapshot.Settings.SystemProxy = message.status.SystemProxy
		m.snapshot.Settings.Mode = message.status.Mode
		m.snapshot.Settings.MixedPort = message.status.ConfiguredProxyPort
		m.snapshot.ConfiguredProxyPort = message.status.ConfiguredProxyPort
		m.snapshot.ActiveProxyPort = message.status.ActiveProxyPort
		m.snapshot.Settings.TunEnabled = message.status.TunState == "on"
		m.snapshot.Settings.TunScope = message.status.TunScope
		if message.status.Mode == tuiSilentMode {
			m.snapshot.Settings.TunEnabled = false
		}
		m.snapshot.FLCEnabled = message.status.FLCEnabled
		m.snapshot.FLCOutbound = message.status.FLCOutbound
		m.refreshInFlight = false
		return m, tea.Batch(m.startRefresh(), m.waitServiceUpdate())
	case tuiShutdownResultMsg:
		if message.err != nil {
			m.shutdownRequested = false
			m.snapshot.Status = "Backend shutdown failed: " + message.err.Error()
			return m, nil
		}
		m.snapshot.Status = "Backend stopped"
		return m, tea.Quit
	case tuiNetworkResultMsg:
		m.networkCheckActive = false
		if message.route != m.networkCheckRoute() {
			return m, m.startNetworkCheck()
		}
		m.snapshot.Network = message.info
		if message.info.Error != "" {
			m.snapshot.Status = "Network detection failed: " + message.info.Error
		} else if strings.HasPrefix(m.snapshot.Status, "Network detection failed: ") {
			m.snapshot.Status = "Connected"
		}
		return m, nil
	case tuiMemoryResultMsg:
		m.memoryRefreshActive = false
		if m.snapshot.Memory.CoreUpdated.After(message.info.CoreUpdated) {
			message.info.CoreRSS = m.snapshot.Memory.CoreRSS
			message.info.CoreError = m.snapshot.Memory.CoreError
			message.info.CoreUpdated = m.snapshot.Memory.CoreUpdated
		}
		m.snapshot.Memory = message.info
		return m, nil
	case tuiCoreMemoryMsg:
		if message.update.Closed {
			return m, nil
		}
		m.snapshot.Memory.ExternalCore = !m.ownsCore
		if message.update.RSS > 0 {
			m.snapshot.Memory.CoreRSS = message.update.RSS
			m.snapshot.Memory.CoreError = ""
		}
		if message.update.Error != "" {
			m.snapshot.Memory.CoreError = message.update.Error
		}
		m.snapshot.Memory.CoreUpdated = message.update.UpdatedAt
		return m, m.waitCoreMemoryUpdate()
	case tuiTrafficMsg:
		if message.update.Closed {
			return m, nil
		}
		m.snapshot.Traffic = message.update.Traffic
		m.snapshot.TrafficHistory = appendTUITrafficHistory(
			m.snapshot.TrafficHistory,
			message.update.Traffic,
		)
		m.snapshot.TotalTraffic = trafficSnapshot{
			Up:   message.update.Traffic.UpTotal,
			Down: message.update.Traffic.DownTotal,
		}
		return m, m.waitTrafficUpdate()
	case tuiSSHCaptureResultMsg:
		if !m.sshCaptureOpen || message.generation != m.sshCaptureGeneration {
			return m, nil
		}
		m.sshCaptureNames = message.names
		m.sshCaptureOptions = message.options
		m.sshCaptureCandidates = message.candidates
		m.sshCaptureSelected = message.selected
		if len(message.options) == 0 {
			hint := message.hint
			if hint == "" {
				hint = formatCLICaptureEmptyHint()
			}
			m.sshCaptureOptions = []string{hint}
		}
		return m, nil
	case tuiSSHRelayStatsMsg:
		if m.selectedSSHName() != message.name {
			return m, nil
		}
		if message.err != nil {
			m.snapshot.SSHTraffic = trafficSnapshot{}
			m.snapshot.SSHConnections = 0
			return m, nil
		}
		elapsed := message.at.Sub(m.sshLastStatsAt).Seconds()
		traffic := trafficSnapshot{
			UpTotal:   message.stats.Upload,
			DownTotal: message.stats.Download,
		}
		if m.sshLastStatsName == message.name && elapsed > 0 {
			traffic.Up = maxTUIInt64(0, int64(float64(message.stats.Upload-m.sshLastStats.Upload)/elapsed))
			traffic.Down = maxTUIInt64(0, int64(float64(message.stats.Download-m.sshLastStats.Download)/elapsed))
		}
		m.sshLastStats = message.stats
		m.sshLastStatsAt = message.at
		m.sshLastStatsName = message.name
		m.snapshot.SSHTraffic = traffic
		m.snapshot.SSHTotalTraffic = trafficSnapshot{Up: message.stats.Upload, Down: message.stats.Download}
		m.snapshot.SSHConnections = message.stats.Connections
		m.snapshot.SSHTrafficHistory = appendTUITrafficHistory(m.snapshot.SSHTrafficHistory, traffic)
		return m, nil
	case tuiSSHNetworkResultMsg:
		if m.selectedSSHName() == message.name {
			if message.direct {
				m.snapshot.SSHDirectNetwork = message.info
			} else {
				m.snapshot.SSHNetwork = message.info
			}
			if message.info.Error != "" {
				prefix := "SSH network detection"
				if message.direct {
					prefix = "SSH direct network detection"
				}
				m.snapshot.Status = prefix + " failed: " + message.info.Error
			} else {
				if message.direct {
					m.snapshot.Status = "SSH direct exit network refreshed"
				} else {
					m.snapshot.Status = "SSH managed exit network refreshed"
				}
			}
		}
		return m, nil
	case tuiSSHDirectProbeResultMsg:
		if m.selectedSSHName() != message.name {
			return m, nil
		}
		if message.err != nil {
			m.snapshot.SSHDirectProbe = cliSSHRemoteProbe{Reason: message.err.Error()}
			m.snapshot.SSHDirectNetwork = tuiNetworkInfo{Error: message.err.Error(), CheckedAt: time.Now()}
			m.snapshot.Status = "SSH direct exit unavailable: " + message.err.Error()
			return m, nil
		}
		m.snapshot.SSHDirectProbe = message.probe
		if !message.probe.DirectAllowed {
			reason := cliDisplayValue(message.probe.Reason)
			m.snapshot.SSHDirectNetwork = tuiNetworkInfo{Error: reason, CheckedAt: time.Now()}
			m.snapshot.Status = "SSH direct exit unavailable: " + reason
			return m, nil
		}
		m.snapshot.Status = "SSH direct exit verified · testing its network path"
		return m, m.refreshSelectedSSHNetworkFor(true)
	case tuiSSHDelayResultMsg:
		if m.selectedSSHName() == message.name {
			if message.err != nil {
				if message.direct {
					m.snapshot.SSHDirectDelay = tuiDelayResult{Error: message.err.Error()}
					m.snapshot.Status = "SSH direct route delay failed: " + message.err.Error()
				} else {
					m.snapshot.SSHDelay = tuiDelayResult{Error: message.err.Error()}
					m.snapshot.Status = "SSH managed route delay failed: " + message.err.Error()
				}
			} else {
				if message.direct {
					m.snapshot.SSHDirectDelay = message.result
					m.snapshot.Status = "SSH direct route delay: " + formatTUIDelay(message.result)
				} else {
					m.snapshot.SSHDelay = message.result
					m.snapshot.Status = "SSH managed route delay: " + formatTUIDelay(message.result)
				}
			}
		}
		return m, nil
	case tuiSSHSpeedResultMsg:
		if m.selectedSSHName() == message.name {
			if message.err != nil {
				if message.direct {
					m.snapshot.SSHDirectSpeed = tuiSpeedResult{Error: message.err.Error()}
					m.snapshot.Status = "SSH direct route speed failed: " + message.err.Error()
				} else {
					m.snapshot.SSHSpeed = tuiSpeedResult{Error: message.err.Error()}
					m.snapshot.Status = "SSH managed route speed failed: " + message.err.Error()
				}
			} else {
				if message.direct {
					m.snapshot.SSHDirectSpeed = message.result
					m.snapshot.Status = "SSH direct route speed: " + formatTUISpeed(message.result)
				} else {
					m.snapshot.SSHSpeed = message.result
					m.snapshot.Status = "SSH managed route speed: " + formatTUISpeed(message.result)
				}
			}
		}
		return m, nil
	case tuiRefreshResultMsg:
		if message.sequence != m.refreshSequence {
			return m, nil
		}
		m.refreshInFlight = false
		m.snapshot = mergeTUIRefresh(m.snapshot, message.snapshot)
		if message.serviceStatus != nil {
			m.backendRevision = message.serviceStatus.Revision
			m.coreRunning = message.serviceStatus.Running
			m.reconcileStoppedCoreState()
			m.snapshot.Settings.SystemProxy = message.serviceStatus.SystemProxy
			m.snapshot.Settings.Mode = message.serviceStatus.Mode
			m.snapshot.Settings.MixedPort = message.serviceStatus.ConfiguredProxyPort
			m.snapshot.ConfiguredProxyPort = message.serviceStatus.ConfiguredProxyPort
			m.snapshot.ActiveProxyPort = message.serviceStatus.ActiveProxyPort
			m.snapshot.Settings.TunEnabled = message.serviceStatus.TunState == "on"
			m.snapshot.Settings.TunScope = message.serviceStatus.TunScope
			if message.serviceStatus.Mode == tuiSilentMode {
				m.snapshot.Settings.TunEnabled = false
			}
			m.snapshot.FLCEnabled = message.serviceStatus.FLCEnabled
			m.snapshot.FLCOutbound = message.serviceStatus.FLCOutbound
		}
		if !m.coreRunning && m.stagedSettings != nil {
			systemProxy := m.snapshot.Settings.SystemProxy
			m.snapshot.Settings = *m.stagedSettings
			m.snapshot.Settings.SystemProxy = systemProxy
			if message.serviceStatus != nil {
				m.snapshot.Settings.Mode = message.serviceStatus.Mode
				m.snapshot.Settings.TunEnabled =
					message.serviceStatus.TunState == "on"
				m.snapshot.Settings.TunScope = message.serviceStatus.TunScope
				if message.serviceStatus.Mode == tuiSilentMode {
					m.snapshot.Settings.TunEnabled = false
				}
			}
		}
		if m.ownsCore && !m.coreRunning && m.snapshot.Status == "Connected" {
			m.snapshot.Status = "Ready; start Core or enable System proxy on Dashboard"
		}
		return m, nil
	case tuiOperationResultMsg:
		previousRoute := m.networkCheckRoute()
		m.busy = false
		m.snapshot = mergeTUIOperation(m.snapshot, message.state.snapshot)
		m.paths = message.state.paths
		m.setupParams = append(m.setupParams[:0], message.state.setupParams...)
		m.coreRunning = message.state.coreRunning
		m.reconcileStoppedCoreState()
		m.systemProxyManaged = message.state.systemProxyManaged
		m.pendingMixedPort = cloneTUIOptionalInt(message.state.pendingMixedPort)
		m.stagedSettings = cloneTUISettings(message.state.stagedSettings)
		m.settingsDirty = message.state.settingsDirty
		m.backendRevision = message.state.backendRevision
		if message.state.profileSelection != "" {
			m.snapshot.SelectedRow = findTUIProfile(
				m.snapshot.Profiles,
				message.state.profileSelection,
			)
		}
		commands := []tea.Cmd{m.startRefresh()}
		if message.state.networkChanged ||
			m.networkCheckRoute() != previousRoute {
			commands = append(commands, m.startNetworkCheck())
		}
		return m, tea.Batch(commands...)
	case tuiProxyGroupSpeedResultMsg:
		if message.result.Error == "" {
			message.successes++
		}
		setTUIGroupSpeed(
			&m.snapshot,
			message.groupName,
			message.node,
			message.result,
		)
		completed := message.total - len(message.remaining)
		if len(message.remaining) > 0 {
			m.snapshot.Status = fmt.Sprintf(
				"%s speed tests: %d/%d complete · testing %s next",
				message.groupName,
				completed,
				message.total,
				message.remaining[0],
			)
			return m, m.testNextProxyGroupSpeed(
				message.groupName,
				message.remaining,
				message.total,
				message.successes,
			)
		}
		m.busy = false
		m.snapshot.Status = fmt.Sprintf(
			"%s speed tests complete: %d/%d succeeded",
			message.groupName,
			message.successes,
			message.total,
		)
		return m, m.startRefresh()
	case tuiEditorResultMsg:
		m.busy = false
		editorPath := m.editorPath
		editorTempPath := m.editorTempPath
		editorBackup := m.editorBackup
		m.editorPath = ""
		m.editorTempPath = ""
		m.editorBackup = tuiProfileBackup{}
		defer os.Remove(editorTempPath)
		if message.err != nil {
			m.snapshot.Status = "Editor failed: " + message.err.Error()
			return m, nil
		}
		edited, readErr := os.ReadFile(editorTempPath)
		if readErr != nil {
			m.snapshot.Status = "Edited configuration could not be read: " + readErr.Error()
			return m, nil
		}
		if validationMessage := validateConfigBytes(edited); validationMessage != "" {
			m.snapshot.Status = "Edited configuration is invalid: " + validationMessage
			return m, nil
		}
		return m, m.startOperation(func(state *tuiOperationState) {
			if !prepareTUIBackendRevision(state, m.service) {
				return
			}
			status, err := m.service.putProfile(
				editorPath,
				edited,
				tuiBytesSHA256(editorBackup.data),
				false,
				nil,
				state.backendRevision,
			)
			if err != nil {
				state.snapshot.Status = "Edited configuration was not committed: " + err.Error()
				return
			}
			applyTUIOperationServiceStatus(state, status)
			if filepath.Clean(editorPath) == filepath.Clean(state.paths.ConfigPath) {
				state.snapshot.Status = "Configuration saved and hot-reloaded"
				syncStoppedTUISettings(state)
				state.networkChanged = true
			} else {
				state.snapshot.Status = "Configuration saved: " + filepath.Base(editorPath) +
					" · activate it to apply"
			}
			refreshTUIProfiles(&state.snapshot, state.paths)
		})
	case tuiSSHCommandResultMsg:
		m.busy = false
		var credentialRequired *cliSSHCredentialRequiredError
		if message.action == "connect" && errors.As(message.err, &credentialRequired) {
			m.beginSSHCredentialPrompt(
				credentialRequired.Profile,
				credentialRequired.Identity,
			)
			return m, nil
		}
		if message.err == nil && (message.action == "add" || message.action == "edit") {
			m.resetSSHForm()
		}
		refreshTUISSH(&m.snapshot)
		if message.selectedName != "" {
			for index, profile := range m.snapshot.SSHProfiles {
				if strings.EqualFold(profile.Name, message.selectedName) {
					m.snapshot.SelectedSSH = index
					break
				}
			}
		}
		if message.err != nil {
			m.snapshot.Status = "SSH " + message.action + " failed: " + message.err.Error()
		} else if message.status != "" {
			m.snapshot.Status = message.status
		} else {
			m.snapshot.Status = "SSH " + message.action + " complete"
		}
		commands := []tea.Cmd{m.startRefresh()}
		if message.err == nil {
			switch message.action {
			case "connect":
				m.resetSelectedSSHMetrics()
				commands = append(commands, m.refreshSelectedSSHDashboard())
			case "disconnect":
				m.resetSelectedSSHMetrics()
			}
		}
		return m, tea.Batch(commands...)
	case tea.KeyMsg:
		if message.String() == "ctrl+n" || m.notificationDetailOpen {
			return m, m.handleTeaKey(message)
		}
		if m.profileDeleteOpen {
			return m, m.handleProfileDeleteConfirm(message)
		}
		if m.dangerConfirmOpen {
			return m, m.handleDangerConfirm(message)
		}
		if m.inputMode != tuiInputNone {
			return m, m.handleInput(message)
		}
		if m.sshCredentialPromptOpen {
			return m, m.handleSSHCredentialPrompt(message)
		}
		if m.sshDeleteConfirmOpen {
			return m, m.handleSSHDeleteConfirm(message)
		}
		if m.sshFormOpen {
			return m, m.handleSSHForm(message)
		}
		if m.sshCaptureOpen {
			return m, m.handleSSHCapture(message)
		}
		if m.modeSelectionOpen {
			return m, m.handleModeSelection(message)
		}
		if message.Type == tea.KeyRunes && len(message.Runes) > 1 && !message.Paste {
			commands := make([]tea.Cmd, 0, len(message.Runes))
			for _, value := range message.Runes {
				command := m.handleTeaKey(tea.KeyMsg{
					Type:  tea.KeyRunes,
					Runes: []rune{value},
				})
				if command != nil {
					commands = append(commands, command)
				}
			}
			return m, tea.Batch(commands...)
		}
		return m, m.handleTeaKey(message)
	default:
		return m, nil
	}
}
