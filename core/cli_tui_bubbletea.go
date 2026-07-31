//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	nethttp "net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/metacubex/mihomo/config"
	logrus "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

const tuiRefreshInterval = time.Second
const tuiNetworkRefreshInterval = time.Minute

const (
	tuiSubscriptionUserAgent = "mihomo"
	tuiSubscriptionMaxBytes  = 32 << 20
)

type tuiTickMsg time.Time
type tuiReloadSignalMsg struct{}

type tuiServiceWatchMsg struct {
	status tuiServiceStatus
	err    error
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

type tuiInputMode byte

const (
	tuiInputNone tuiInputMode = iota
	tuiInputMixedPort
	tuiInputSubscription
	tuiInputProfileName
)

type tuiModel struct {
	snapshot            tuiSnapshot
	client              controllerClient
	service             *tuiServiceClient
	paths               cliPaths
	setupParams         []byte
	ownsCore            bool
	coreRunning         bool
	systemProxyManaged  bool
	width               int
	height              int
	refreshSequence     uint64
	refreshInFlight     bool
	busy                bool
	inputMode           tuiInputMode
	inputValue          []rune
	inputCursor         int
	inputSelectAll      bool
	renameProfilePath   string
	editorPath          string
	editorBackup        tuiProfileBackup
	pendingMixedPort    *int
	stagedSettings      *tuiSettings
	settingsDirty       bool
	backendRevision     uint64
	systemProxyToggle   func(*tuiSnapshot) bool
	networkCheckActive  bool
	memoryRefreshActive bool
	coreMemoryUpdates   <-chan tuiCoreMemoryUpdate
	stopCoreMemory      func()
	trafficUpdates      <-chan tuiTrafficUpdate
	stopTraffic         func()
	stopServiceOnExit   bool // Legacy test visibility; managed frontends always leave this false.
}

func newTUIModel(
	client controllerClient,
	paths cliPaths,
	setupParams []byte,
	ownsCore bool,
) *tuiModel {
	width, height := tuiTerminalSize()
	stagedSettings := loadTUIConfiguredSettings(paths.configPath, ownsCore)
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
			GroupOrder:        loadTUIProxyGroupOrder(paths.configPath),
			Settings:          settings,
			SelectedGroup:     0,
			SelectedNode:      0,
			SelectedRow:       -1,
			SelectedMenu:      int(tuiPageDashboard),
			SelectedDashboard: tuiDashboardSystemProxyRow,
			FocusSidebar:      true,
		},
		client:            client,
		paths:             paths,
		setupParams:       append([]byte(nil), setupParams...),
		ownsCore:          ownsCore,
		coreRunning:       false,
		width:             width,
		height:            height,
		pendingMixedPort:  pendingMixedPort,
		stagedSettings:    stagedSettings,
		systemProxyToggle: toggleTUISystemProxy,
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
) error {
	if !isInteractiveTUI() {
		return errors.New("TUI requires an interactive terminal; use run or proxy commands in non-interactive shells")
	}

	logrus.SetOutput(io.Discard)
	handleStartLog()
	defer handleStopLog()

	model := newTUIModel(client, paths, setupParams, ownsCore)
	model.service = service
	if service != nil {
		if status, statusErr := service.status(); statusErr == nil {
			model.backendRevision = status.Revision
			model.snapshot.Settings.SystemProxy = status.SystemProxy
		}
	}
	model.initializeCoreRuntime(coreRunning)
	model.snapshot.ManagedService = service != nil
	model.snapshot.Frontends, _ = listCLIFrontends()
	if startupNotice != "" {
		model.snapshot.StartupNotice = startupNotice
	}
	model.startCoreMemoryMonitor()
	model.startTrafficMonitor()
	defer model.stopCoreMemoryMonitor()
	defer model.stopTrafficMonitor()
	program := tea.NewProgram(model, tuiProgramOptions()...)
	sighup := make(chan os.Signal, 1)
	signal.Notify(sighup, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-sighup:
				program.Send(tuiReloadSignalMsg{})
			case <-done:
				return
			}
		}
	}()

	_, runErr := program.Run()
	close(done)
	signal.Stop(sighup)
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
		return
	}
	m.pendingMixedPort = nil
	m.stagedSettings = nil
	m.settingsDirty = false
	m.snapshot.Settings = tuiSettings{}
}

func tuiProgramOptions() []tea.ProgramOption {
	return []tea.ProgramOption{
		tea.WithAltScreen(),
		tea.WithFPS(30),
		tea.WithoutSignalHandler(),
	}
}

func (m *tuiModel) Init() tea.Cmd {
	return tea.Batch(
		tuiTickCommand(),
		m.startRefresh(),
		m.startNetworkCheck(true),
		m.startMemoryRefresh(),
		m.waitCoreMemoryUpdate(),
		m.waitTrafficUpdate(),
		m.waitServiceUpdate(),
	)
}

func (m *tuiModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		m.width = message.Width
		m.height = message.Height
		return m, nil
	case tuiTickMsg:
		return m, tea.Batch(
			tuiTickCommand(),
			m.startRefresh(),
			m.startNetworkCheck(false),
			m.startMemoryRefresh(),
		)
	case tuiReloadSignalMsg:
		return m, m.handleKey(tuiKeyReload)
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
		m.backendRevision = message.status.Revision
		m.coreRunning = message.status.Running
		m.snapshot.Settings.SystemProxy = message.status.SystemProxy
		m.refreshInFlight = false
		return m, tea.Batch(m.startRefresh(), m.waitServiceUpdate())
	case tuiNetworkResultMsg:
		m.networkCheckActive = false
		if message.route != m.networkCheckRoute() {
			return m, m.startNetworkCheck(true)
		}
		m.snapshot.Network = message.info
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
			m.coreMemoryUpdates = nil
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
			m.trafficUpdates = nil
			return m, nil
		}
		m.snapshot.Traffic = message.update.Traffic
		m.snapshot.TotalTraffic = trafficSnapshot{
			Up:   message.update.Traffic.UpTotal,
			Down: message.update.Traffic.DownTotal,
		}
		return m, m.waitTrafficUpdate()
	case tuiRefreshResultMsg:
		if message.sequence != m.refreshSequence {
			return m, nil
		}
		m.refreshInFlight = false
		m.snapshot = mergeTUIRefresh(m.snapshot, message.snapshot)
		if message.serviceStatus != nil {
			m.backendRevision = message.serviceStatus.Revision
			m.coreRunning = message.serviceStatus.Running
			m.snapshot.Settings.SystemProxy = message.serviceStatus.SystemProxy
		}
		if !m.coreRunning && m.stagedSettings != nil {
			systemProxy := m.snapshot.Settings.SystemProxy
			m.snapshot.Settings = *m.stagedSettings
			m.snapshot.Settings.SystemProxy = systemProxy
		}
		if m.ownsCore && !m.coreRunning && m.snapshot.Status == "Connected" {
			m.snapshot.Status = "Ready; enable System proxy on Dashboard to start"
		}
		return m, nil
	case tuiOperationResultMsg:
		previousRoute := m.networkCheckRoute()
		m.busy = false
		m.snapshot = mergeTUIOperation(m.snapshot, message.state.snapshot)
		m.paths = message.state.paths
		m.setupParams = append(m.setupParams[:0], message.state.setupParams...)
		m.coreRunning = message.state.coreRunning
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
		if m.networkCheckRoute() != previousRoute {
			commands = append(commands, m.startNetworkCheck(true))
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
		editorBackup := m.editorBackup
		m.editorPath = ""
		m.editorBackup = tuiProfileBackup{}
		if message.err != nil {
			restoreErr := restoreTUISubscriptionProfile(editorPath, editorBackup)
			editorBackup.release()
			m.snapshot.Status = "Editor failed: " + message.err.Error()
			if restoreErr != nil {
				m.snapshot.Status += "; restore failed: " + restoreErr.Error()
			}
			return m, nil
		}
		edited, readErr := os.ReadFile(editorPath)
		if readErr != nil {
			editorBackup.release()
			m.snapshot.Status = "Edited configuration could not be read: " + readErr.Error()
			return m, nil
		}
		if validationMessage := validateConfigBytes(edited); validationMessage != "" {
			restoreErr := restoreTUISubscriptionProfile(editorPath, editorBackup)
			editorBackup.release()
			m.snapshot.Status = "Edited configuration is invalid: " + validationMessage
			if restoreErr == nil {
				m.snapshot.Status += "; original restored"
			} else {
				m.snapshot.Status += "; restore failed: " + restoreErr.Error()
			}
			return m, nil
		}
		editorBackup.updatedSHA256 = tuiBytesSHA256(edited)
		if filepath.Clean(editorPath) != filepath.Clean(m.paths.configPath) {
			editorBackup.release()
			m.snapshot.Status = "Configuration saved: " + filepath.Base(editorPath) +
				" · activate it to apply"
			return m, nil
		}
		editorBackup.release()
		return m, m.startOperation(func(state *tuiOperationState) {
			if reloadErr := reloadTUIOperationConfigExpected(
				state,
				m.service,
				m.client,
				m.ownsCore,
				editorBackup.updatedSHA256,
			); reloadErr != nil {
				restoreErr := restoreTUIProfileIfUnchanged(
					state.paths.homeDir,
					editorPath,
					editorBackup,
				)
				state.snapshot.Status = "Edited config could not be hot-reloaded: " +
					reloadErr.Error()
				if restoreErr == nil {
					originalReloadErr := reloadTUIOperationConfigExpected(
						state,
						m.service,
						m.client,
						m.ownsCore,
						tuiBytesSHA256(editorBackup.data),
					)
					if originalReloadErr == nil {
						state.snapshot.Status += "; original restored"
					} else {
						state.snapshot.Status += "; file restored but Core reload failed: " +
							originalReloadErr.Error()
					}
				} else {
					state.snapshot.Status += "; original not restored: " + restoreErr.Error()
				}
			} else {
				state.snapshot.Status = "Configuration saved and hot-reloaded"
				syncStoppedTUISettings(state)
			}
		})
	case tea.KeyMsg:
		if m.inputMode != tuiInputNone {
			return m, m.handleInput(message)
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

func (m *tuiModel) handleTeaKey(message tea.KeyMsg) tea.Cmd {
	key, ok := tuiKeyFromTea(message)
	if !ok {
		return nil
	}
	if message.String() == "v" &&
		(m.snapshot.Page == tuiPageDashboard ||
			m.snapshot.Page == tuiPageProxies) {
		key = tuiKeySpeedTest
	}
	if m.snapshot.StartupNotice != "" &&
		key != tuiKeyQuit &&
		key != tuiKeyInterrupt {
		m.snapshot.StartupNotice = ""
		m.snapshot.Status = "Connected to shared per-user backend"
		return nil
	}
	if m.snapshot.ShowHelp &&
		key != tuiKeyHelp &&
		key != tuiKeyQuit &&
		key != tuiKeyInterrupt {
		m.snapshot.ShowHelp = false
		return nil
	}
	if handleTUIFocusNavigation(&m.snapshot, key) {
		return nil
	}
	if m.busy && !tuiKeyAllowedWhileBusy(key) {
		m.snapshot.Status = "Operation in progress; navigation remains available"
		return nil
	}
	return m.handleKey(key)
}

func tuiKeyAllowedWhileBusy(key tuiKey) bool {
	switch key {
	case tuiKeyQuit,
		tuiKeyInterrupt,
		tuiKeyHelp,
		tuiKeyBack,
		tuiKeyUp,
		tuiKeyDown,
		tuiKeyLeft,
		tuiKeyRight,
		tuiKeyViewPrevious,
		tuiKeyViewNext,
		tuiKeyPageUp,
		tuiKeyPageDown:
		return true
	default:
		return false
	}
}

func (m *tuiModel) View() string {
	snapshot := m.snapshot
	if m.inputMode != tuiInputNone {
		cursor := m.inputCursor
		if cursor < 0 {
			cursor = 0
		}
		if cursor > len(m.inputValue) {
			cursor = len(m.inputValue)
		}
		snapshot.InputTitle, snapshot.InputHint = m.inputPresentation()
		snapshot.InputValue = tuiInputViewport(
			m.inputValue,
			cursor,
			maxTUIWidth(m.width-36, 20),
		)
		snapshot.Status = "Editing input · Enter confirm · Esc cancel"
	}
	return renderTUIAtSize(
		snapshot,
		m.paths,
		m.client.displayAddress(),
		m.ownsCore,
		m.coreRunning,
		m.width,
		m.height,
	)
}

func (m *tuiModel) inputPresentation() (string, string) {
	switch m.inputMode {
	case tuiInputMixedPort:
		return "Set mixed port", "Type 0-65535; typing replaces the current value"
	case tuiInputSubscription:
		return "Import subscription", "Paste a Clash/Mihomo subscription URL"
	case tuiInputProfileName:
		return "Rename profile", "Type a file name; .yaml is added automatically"
	default:
		return "Input", ""
	}
}

func tuiInputViewport(value []rune, cursor, width int) string {
	if width <= 0 {
		return ""
	}
	if cursor < 0 {
		cursor = 0
	}
	if cursor > len(value) {
		cursor = len(value)
	}
	start := cursor
	used := 1
	for start > 0 {
		runeWidth := tuiRuneWidth(value[start-1])
		reservedPrefix := 0
		if start-1 > 0 {
			reservedPrefix = 1
		}
		if used+runeWidth+reservedPrefix > width {
			break
		}
		start--
		used += runeWidth
	}
	used = 1 + tuiDisplayWidth(string(value[start:cursor]))
	if start > 0 {
		used++
	}
	end := cursor
	for end < len(value) {
		runeWidth := tuiRuneWidth(value[end])
		reservedSuffix := 0
		if end+1 < len(value) {
			reservedSuffix = 1
		}
		if used+runeWidth+reservedSuffix > width {
			break
		}
		end++
		used += runeWidth
	}
	var output strings.Builder
	if start > 0 {
		output.WriteRune('…')
	}
	output.WriteString(string(value[start:cursor]))
	output.WriteRune('█')
	output.WriteString(string(value[cursor:end]))
	if end < len(value) {
		output.WriteRune('…')
	}
	return output.String()
}

func (m *tuiModel) shutdown() {
	m.editorBackup.release()
	if m.ownsCore && m.service == nil && m.settingsDirty && m.stagedSettings != nil {
		_ = persistTUISettings(m.paths.configPath, *m.stagedSettings)
	}
	if !m.ownsCore {
		return
	}
	if m.service != nil {
		return
	}
	if m.ownsCore {
		handleShutdown()
	}
}

func tuiTickCommand() tea.Cmd {
	return tea.Tick(tuiRefreshInterval, func(value time.Time) tea.Msg {
		return tuiTickMsg(value)
	})
}

func (m *tuiModel) waitServiceUpdate() tea.Cmd {
	if m.service == nil {
		return nil
	}
	service := m.service
	revision := m.backendRevision
	return func() tea.Msg {
		status, err := service.watch(revision, 30*time.Second)
		return tuiServiceWatchMsg{status: status, err: err}
	}
}

func (m *tuiModel) startRefresh() tea.Cmd {
	if m.refreshInFlight || m.busy {
		return nil
	}
	m.refreshInFlight = true
	m.refreshSequence++
	sequence := m.refreshSequence
	snapshot := m.snapshot
	client := m.client
	paths := m.paths
	service := m.service
	return func() tea.Msg {
		refreshTUISnapshot(&snapshot, client)
		refreshTUIProfiles(&snapshot, paths)
		snapshot.Frontends, _ = listCLIFrontends()
		var serviceStatus *tuiServiceStatus
		if service != nil {
			if status, err := service.status(); err == nil {
				serviceStatus = &status
			}
		}
		return tuiRefreshResultMsg{
			sequence:      sequence,
			snapshot:      snapshot,
			serviceStatus: serviceStatus,
		}
	}
}

func (m *tuiModel) startOperation(action func(*tuiOperationState)) tea.Cmd {
	if m.busy {
		m.snapshot.Status = "Another operation is still running"
		return nil
	}
	m.busy = true
	m.refreshInFlight = false
	m.refreshSequence++
	state := tuiOperationState{
		snapshot:           m.snapshot,
		paths:              m.paths,
		setupParams:        append([]byte(nil), m.setupParams...),
		coreRunning:        m.coreRunning,
		systemProxyManaged: m.systemProxyManaged,
		pendingMixedPort:   cloneTUIOptionalInt(m.pendingMixedPort),
		stagedSettings:     cloneTUISettings(m.stagedSettings),
		settingsDirty:      m.settingsDirty,
		backendRevision:    m.backendRevision,
	}
	m.snapshot.Status = "Working..."
	return func() tea.Msg {
		action(&state)
		return tuiOperationResultMsg{state: state}
	}
}

func cloneTUIOptionalInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneTUISettings(value *tuiSettings) *tuiSettings {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func mergeTUIRefresh(current, refreshed tuiSnapshot) tuiSnapshot {
	refreshed = preserveTUIInteraction(current, refreshed)
	if !current.UpdatedAt.IsZero() {
		refreshed.Settings.SystemProxy = current.Settings.SystemProxy
	}
	if !tuiStatusIsControllerError(refreshed.Status) &&
		current.Status != "" &&
		current.Status != "Connected" &&
		current.Status != "Loading..." {
		refreshed.Status = current.Status
	}
	return refreshed
}

func tuiStatusIsControllerError(status string) bool {
	return strings.HasPrefix(status, "Controller unavailable:") ||
		strings.HasPrefix(status, "Invalid controller response:")
}

func mergeTUIOperation(current, result tuiSnapshot) tuiSnapshot {
	return preserveTUIInteraction(current, result)
}

func preserveTUIInteraction(current, updated tuiSnapshot) tuiSnapshot {
	selectedGroupName := ""
	selectedNodeName := ""
	if current.SelectedGroup >= 0 && current.SelectedGroup < len(current.Groups) {
		selectedGroup := current.Groups[current.SelectedGroup]
		selectedGroupName = selectedGroup.Name
		if current.SelectedNode >= 0 && current.SelectedNode < len(selectedGroup.Nodes) {
			selectedNodeName = selectedGroup.Nodes[current.SelectedNode]
		}
	}
	selectedConnectionID := ""
	if current.SelectedConnection >= 0 && current.SelectedConnection < len(current.Connections) {
		selectedConnectionID = current.Connections[current.SelectedConnection].ID
	}
	selectedRequestID := ""
	if current.SelectedRequest >= 0 && current.SelectedRequest < len(current.Requests) {
		selectedRequestID = current.Requests[current.SelectedRequest].ID
	}
	selectedProviderName := ""
	if current.SelectedProvider >= 0 && current.SelectedProvider < len(current.Providers) {
		selectedProviderName = current.Providers[current.SelectedProvider].Name
	}
	selectedProfilePath := ""
	importSelected := current.SelectedRow < 0
	if current.SelectedRow >= 0 && current.SelectedRow < len(current.Profiles) {
		selectedProfilePath = current.Profiles[current.SelectedRow].Path
	}

	updated.Page = current.Page
	updated.SelectedMenu = current.SelectedMenu
	updated.SelectedDashboard = current.SelectedDashboard
	updated.SelectedSetting = current.SelectedSetting
	updated.SelectedTool = current.SelectedTool
	updated.SelectedMaintenance = current.SelectedMaintenance
	updated.ProxyView = current.ProxyView
	updated.ProxyNodeFocus = current.ProxyNodeFocus
	updated.ManagedService = current.ManagedService
	updated.FocusSidebar = current.FocusSidebar
	updated.ShowHelp = current.ShowHelp
	updated.StartupNotice = current.StartupNotice
	if len(current.GroupOrder) > 0 {
		updated.GroupOrder = append([]string(nil), current.GroupOrder...)
		orderTUIGroups(updated.Groups, updated.GroupOrder)
	}
	if current.Network.Loading ||
		current.Network.CheckedAt.After(updated.Network.CheckedAt) {
		updated.Network = current.Network
	}
	if current.Memory.UpdatedAt.After(updated.Memory.UpdatedAt) ||
		current.Memory.CoreUpdated.After(updated.Memory.CoreUpdated) {
		updated.Memory = current.Memory
	}
	mergeTUIGroupDelays(current.Groups, updated.Groups)

	updated.SelectedGroup = findTUIGroup(updated.Groups, selectedGroupName)
	if selectedGroupName == "" {
		updated.SelectedGroup = clampTUISelection(current.SelectedGroup, len(updated.Groups))
	}
	if updated.SelectedGroup >= 0 && updated.SelectedGroup < len(updated.Groups) {
		updated.SelectedNode = findTUIString(
			updated.Groups[updated.SelectedGroup].Nodes,
			selectedNodeName,
		)
		if selectedNodeName == "" {
			updated.SelectedNode = clampTUISelection(
				current.SelectedNode,
				len(updated.Groups[updated.SelectedGroup].Nodes),
			)
		}
	}
	updated.SelectedConnection = findTUIConnection(updated.Connections, selectedConnectionID)
	if selectedConnectionID == "" {
		updated.SelectedConnection = clampTUISelection(
			current.SelectedConnection,
			len(updated.Connections),
		)
	}
	updated.SelectedRequest = findTUIRequest(updated.Requests, selectedRequestID)
	if selectedRequestID == "" {
		updated.SelectedRequest = clampTUISelection(
			current.SelectedRequest,
			len(updated.Requests),
		)
	}
	updated.SelectedProvider = findTUIProvider(updated.Providers, selectedProviderName)
	if selectedProviderName == "" {
		updated.SelectedProvider = clampTUISelection(
			current.SelectedProvider,
			len(updated.Providers),
		)
	}
	if importSelected {
		updated.SelectedRow = -1
	} else {
		updated.SelectedRow = findTUIProfile(updated.Profiles, selectedProfilePath)
	}
	if !importSelected && selectedProfilePath == "" {
		updated.SelectedRow = clampTUISelection(current.SelectedRow, len(updated.Profiles))
	}
	return updated
}

func mergeTUIGroupDelays(current, updated []tuiGroup) {
	currentByName := make(map[string]tuiGroup, len(current))
	for _, group := range current {
		currentByName[group.Name] = group
	}
	for index := range updated {
		previous, ok := currentByName[updated[index].Name]
		if !ok {
			continue
		}
		if len(previous.Delays) > 0 {
			delays := make(
				map[string]int,
				len(updated[index].Delays)+len(previous.Delays),
			)
			for node, delay := range updated[index].Delays {
				delays[node] = delay
			}
			for node, delay := range previous.Delays {
				if _, exists := delays[node]; !exists {
					delays[node] = delay
				}
			}
			updated[index].Delays = delays
		}
		if len(previous.Speeds) > 0 {
			speeds := make(
				map[string]tuiSpeedResult,
				len(updated[index].Speeds)+len(previous.Speeds),
			)
			for node, speed := range updated[index].Speeds {
				speeds[node] = speed
			}
			for node, speed := range previous.Speeds {
				if _, exists := speeds[node]; !exists {
					speeds[node] = speed
				}
			}
			updated[index].Speeds = speeds
		}
	}
}

func findTUIProfile(profiles []tuiProfile, path string) int {
	for index, profile := range profiles {
		if filepath.Clean(profile.Path) == filepath.Clean(path) {
			return index
		}
	}
	return 0
}

func clampTUISelection(index, total int) int {
	if total <= 0 {
		return 0
	}
	if index < 0 {
		return 0
	}
	if index >= total {
		return total - 1
	}
	return index
}

func (m *tuiModel) handleKey(key tuiKey) tea.Cmd {
	switch key {
	case tuiKeyQuit:
		return tea.Quit
	case tuiKeyInterrupt:
		return tea.Quit
	case tuiKeyBack:
		if !m.snapshot.FocusSidebar &&
			m.snapshot.Page == tuiPageProxies &&
			m.snapshot.ProxyView == tuiProxyViewGroups &&
			m.snapshot.ProxyNodeFocus {
			m.snapshot.ProxyNodeFocus = false
			m.snapshot.Status = "Proxy groups · Enter opens nodes · d delay · v speed"
		} else if !m.snapshot.FocusSidebar {
			m.snapshot.FocusSidebar = true
			m.snapshot.SelectedMenu = int(m.snapshot.Page)
		}
		return nil
	case tuiKeyRefresh:
		m.refreshInFlight = false
		m.refreshSequence++
		return m.startRefresh()
	case tuiKeyReload:
		return m.startOperation(func(state *tuiOperationState) {
			if reloadErr := reloadTUIOperationConfig(
				state,
				m.service,
				m.client,
				m.ownsCore,
			); reloadErr != nil {
				state.snapshot.Status = "Reload failed: " + reloadErr.Error()
			} else {
				state.snapshot.Status = "Configuration reloaded"
				syncStoppedTUISettings(state)
			}
		})
	case tuiKeyHelp:
		m.snapshot.ShowHelp = !m.snapshot.ShowHelp
	case tuiKeyCloseConnections:
		if m.snapshot.Page == tuiPageRequests {
			m.snapshot.Requests = nil
			m.snapshot.SelectedRequest = 0
			m.snapshot.Status = "Request history cleared"
		} else if m.snapshot.Page == tuiPageLogs {
			clearTUILogs()
			m.snapshot.Logs = nil
			m.snapshot.Status = "Logs cleared"
		} else if m.snapshot.Page == tuiPageConnections {
			return m.startOperation(func(state *tuiOperationState) {
				if err := m.client.closeAllConnections(); err != nil {
					state.snapshot.Status = "Close connections failed: " + err.Error()
				} else {
					state.snapshot.Status = "All connections closed"
				}
			})
		}
	case tuiKeyCloseConnection:
		if m.snapshot.Page == tuiPageDashboard {
			return m.testDashboardDelay()
		}
		if !m.snapshot.FocusSidebar &&
			m.snapshot.Page == tuiPageProxies &&
			m.snapshot.ProxyView == tuiProxyViewGroups {
			if m.snapshot.ProxyNodeFocus {
				return m.testSelectedProxyDelay()
			}
			return m.testSelectedProxyGroupDelays()
		}
		if m.snapshot.Page == tuiPageConnections &&
			m.snapshot.SelectedConnection >= 0 &&
			m.snapshot.SelectedConnection < len(m.snapshot.Connections) {
			connectionID := m.snapshot.Connections[m.snapshot.SelectedConnection].ID
			return m.startOperation(func(state *tuiOperationState) {
				if err := m.client.closeConnection(connectionID); err != nil {
					state.snapshot.Status = "Close connection failed: " + err.Error()
				} else {
					state.snapshot.Status = "Connection closed"
				}
			})
		}
	case tuiKeyCoreToggle:
		return m.startOperation(func(state *tuiOperationState) {
			if !m.ownsCore {
				state.snapshot.Status = "Core lifecycle is owned by the external process"
			} else if state.coreRunning {
				stopTUIManagedCore(state, m.service)
			} else {
				startTUIManagedCore(state, m.service)
			}
		})
	case tuiKeyEdit:
		switch m.snapshot.Page {
		case tuiPageProfiles:
			if m.snapshot.SelectedRow < 0 ||
				m.snapshot.SelectedRow >= len(m.snapshot.Profiles) {
				m.snapshot.Status = "Select a profile before editing its YAML"
				return nil
			}
			return m.startEditor(m.snapshot.Profiles[m.snapshot.SelectedRow].Path)
		case tuiPageLogs:
			return m.startOperation(func(state *tuiOperationState) {
				path, err := exportTUILogs(state.paths.homeDir, state.snapshot.Logs)
				if err != nil {
					state.snapshot.Status = "Export logs failed: " + err.Error()
				} else {
					state.snapshot.Status = "Logs exported: " + path
				}
			})
		case tuiPageMaintenance:
			return m.startEditor(m.paths.configPath)
		default:
			m.snapshot.Status = "Edit YAML is available in Profiles and Maintenance"
		}
	case tuiKeyNewProfile:
		if m.snapshot.Page == tuiPageProfiles {
			m.beginInput(tuiInputSubscription)
		} else if m.snapshot.Page == tuiPageDashboard {
			return m.startNetworkCheck(true)
		}
	case tuiKeyRenameProfile:
		if m.snapshot.Page == tuiPageProfiles {
			m.beginProfileRename()
		}
	case tuiKeyUpdateProfile:
		if m.snapshot.Page == tuiPageProfiles {
			return m.updateSelectedProfileSubscription()
		}
	case tuiKeyProviders:
		m.snapshot.Page = tuiPageProxies
		m.snapshot.SelectedMenu = int(tuiPageProxies)
		m.snapshot.FocusSidebar = false
		m.snapshot.ProxyView = tuiProxyViewProviders
		m.snapshot.ProxyNodeFocus = false
		m.snapshot.Status = "Providers view · Enter updates the selected provider"
	case tuiKeyBackup:
		if m.snapshot.Page == tuiPageMaintenance {
			return m.runTool(1)
		}
	case tuiKeyRestore:
		if m.snapshot.Page == tuiPageMaintenance {
			return m.runTool(2)
		}
	case tuiKeyGeoUpdate:
		if m.snapshot.Page == tuiPageMaintenance {
			return m.runTool(3)
		}
	case tuiKeyResetTraffic:
		if m.snapshot.Page == tuiPageMaintenance {
			return m.runTool(4)
		}
	case tuiKeyUp:
		m.moveSelection(-1)
	case tuiKeyDown:
		m.moveSelection(1)
	case tuiKeyDelayTest:
		if !m.snapshot.FocusSidebar &&
			m.snapshot.Page == tuiPageProxies &&
			m.snapshot.ProxyView == tuiProxyViewGroups {
			if m.snapshot.ProxyNodeFocus {
				return m.testSelectedProxyDelay()
			}
			return m.testSelectedProxyGroupDelays()
		}
	case tuiKeyDelayTestAll:
		if !m.snapshot.FocusSidebar &&
			m.snapshot.Page == tuiPageProxies &&
			m.snapshot.ProxyView == tuiProxyViewGroups {
			return m.testSelectedProxyGroupDelays()
		}
	case tuiKeySpeedTest:
		if m.snapshot.Page == tuiPageDashboard {
			return m.testDashboardSpeed()
		}
		if !m.snapshot.FocusSidebar &&
			m.snapshot.Page == tuiPageProxies &&
			m.snapshot.ProxyView == tuiProxyViewGroups {
			if m.snapshot.ProxyNodeFocus {
				return m.testSelectedProxySpeed()
			}
			return m.testSelectedProxyGroupSpeeds()
		}
	case tuiKeyViewPrevious, tuiKeyViewNext:
		if !m.snapshot.FocusSidebar && m.snapshot.Page == tuiPageProxies {
			delta := 1
			if key == tuiKeyViewPrevious {
				delta = -1
			}
			m.snapshot.ProxyView = wrapTUIIndex(
				m.snapshot.ProxyView,
				delta,
				tuiProxyViewCount,
			)
			if m.snapshot.ProxyView == tuiProxyViewProviders {
				m.snapshot.ProxyNodeFocus = false
				m.snapshot.Status = "Providers view · Enter updates the selected provider"
			} else {
				m.snapshot.ProxyNodeFocus = false
				m.snapshot.Status = "Proxy groups · Enter nodes · d delay group · v speed group"
			}
		}
	case tuiKeyPageUp:
		if m.snapshot.Page == tuiPageDashboard {
			step := maxTUIWidth(m.dashboardViewportLimit()-1, 1)
			m.snapshot.DashboardScroll = maxTUIIndex(
				m.snapshot.DashboardScroll - step,
			)
		}
	case tuiKeyPageDown:
		if m.snapshot.Page == tuiPageDashboard {
			limit := m.dashboardViewportLimit()
			maxScroll := maxTUIIndex(
				len(tuiCompactDashboardRows(m.snapshot, m.paths)) -
					limit,
			)
			m.snapshot.DashboardScroll = minTUI(
				m.snapshot.DashboardScroll+
					maxTUIWidth(limit-1, 1),
				maxScroll,
			)
		}
	case tuiKeySelect:
		return m.selectCurrent()
	case tuiKeyPortUp, tuiKeyPortDown:
		if m.snapshot.Page == tuiPageDashboard || m.snapshot.Page == tuiPageTools {
			if m.ownsCore && !m.coreRunning {
				m.stageTUIAdjustedPort(key)
				return nil
			}
			return m.startOperation(func(state *tuiOperationState) {
				applyTUIOperationSetting(state, m.service, m.client, key)
			})
		}
	case tuiKeyAllowLAN, tuiKeyIPv6, tuiKeyUnifiedDelay, tuiKeyTCPConcurrent,
		tuiKeyTun, tuiKeyMode, tuiKeyLogLevel:
		if m.snapshot.Page == tuiPageTools ||
			(m.snapshot.Page == tuiPageDashboard &&
				(key == tuiKeyTun || key == tuiKeyMode)) {
			if m.ownsCore && !m.coreRunning {
				m.stageTUISetting(key)
				return nil
			}
			return m.startOperation(func(state *tuiOperationState) {
				applyTUIOperationSetting(state, m.service, m.client, key)
			})
		}
	case tuiKeySetPort:
		if m.snapshot.Page == tuiPageDashboard || m.snapshot.Page == tuiPageTools {
			m.beginInput(tuiInputMixedPort)
		}
	case tuiKeySystemProxy:
		if m.snapshot.Page == tuiPageDashboard || m.snapshot.Page == tuiPageTools {
			return m.startOperation(func(state *tuiOperationState) {
				autoStarted := false
				if m.ownsCore && !state.coreRunning {
					if !startTUIManagedCore(state, m.service) {
						return
					}
					autoStarted = true
				}
				proxyUpdated := false
				if m.service != nil {
					if !prepareTUIBackendRevision(state, m.service) {
						return
					}
					enabled := !state.snapshot.Settings.SystemProxy
					status, err := m.service.setSystemProxy(
						enabled,
						state.backendRevision,
					)
					if err != nil {
						state.snapshot.Status = "System proxy update failed: " + err.Error()
					} else {
						state.backendRevision = status.Revision
						state.snapshot.Settings.SystemProxy = status.SystemProxy
						state.snapshot.Status = "System proxy " + cliOnOff(status.SystemProxy)
						proxyUpdated = true
					}
				} else {
					proxyUpdated = m.systemProxyToggle(&state.snapshot)
				}
				if proxyUpdated {
					state.systemProxyManaged = state.snapshot.Settings.SystemProxy
					if autoStarted && state.snapshot.Settings.SystemProxy {
						state.snapshot.Status = fmt.Sprintf(
							"Core started on port %d; system proxy enabled",
							state.snapshot.Settings.MixedPort,
						)
					}
				} else if autoStarted {
					proxyError := state.snapshot.Status
					if stopTUIManagedCore(state, m.service) {
						state.snapshot.Status = proxyError +
							"; automatic Core start rolled back"
					}
				}
			})
		}
	}
	return nil
}

func (m *tuiModel) dashboardViewportLimit() int {
	pageHeight := m.height - 4
	if m.width < 88 || m.height < 18 {
		pageHeight = m.height - 2
	}
	return maxTUIWidth(pageHeight-3, 1)
}

func (m *tuiModel) moveSelection(delta int) {
	switch m.snapshot.Page {
	case tuiPageProfiles:
		moveTUIProfile(&m.snapshot, delta)
		if m.snapshot.SelectedRow < 0 {
			m.snapshot.Status = "Enter to import a subscription URL"
		} else if m.snapshot.SelectedRow < len(m.snapshot.Profiles) {
			profile := m.snapshot.Profiles[m.snapshot.SelectedRow]
			if profile.Current {
				if profile.SubscriptionURL != "" {
					m.snapshot.Status = "Active subscription · U refreshes · e edits YAML"
				} else {
					m.snapshot.Status = "Active local profile · e edits YAML"
				}
			} else {
				if profile.SubscriptionURL != "" {
					m.snapshot.Status = "Enter activates · U refreshes · F2/u renames · e edits"
				} else {
					m.snapshot.Status = "Enter activates · F2/u renames · e edits"
				}
			}
		}
	case tuiPageConnections:
		moveTUIConnection(&m.snapshot, delta)
	case tuiPageRequests:
		m.snapshot.SelectedRequest = wrapTUIIndex(
			m.snapshot.SelectedRequest,
			delta,
			len(m.snapshot.Requests),
		)
	case tuiPageProxies:
		if m.snapshot.ProxyView == tuiProxyViewProviders {
			moveTUIProvider(&m.snapshot, delta)
		} else if m.snapshot.ProxyNodeFocus {
			moveTUINode(&m.snapshot, delta)
		} else {
			moveTUIGroup(&m.snapshot, delta)
		}
	case tuiPageDashboard:
		m.snapshot.SelectedDashboard = wrapTUIIndex(
			m.snapshot.SelectedDashboard,
			delta,
			tuiDashboardRowCount,
		)
		m.snapshot.DashboardScroll = 0
	case tuiPageTools:
		m.snapshot.SelectedTool = wrapTUIIndex(
			m.snapshot.SelectedTool,
			delta,
			tuiSettingsRowCount,
		)
	case tuiPageMaintenance:
		m.snapshot.SelectedMaintenance = wrapTUIIndex(
			m.snapshot.SelectedMaintenance,
			delta,
			tuiMaintenanceRowCount,
		)
	}
}

func (m *tuiModel) stageTUIAdjustedPort(key tuiKey) {
	port := m.snapshot.Settings.MixedPort
	switch key {
	case tuiKeyPortUp:
		if port >= 65535 {
			m.snapshot.Status = "Mixed port is already at 65535"
			return
		}
		port++
	case tuiKeyPortDown:
		if port > 0 {
			port--
		}
	}
	m.pendingMixedPort = &port
	m.snapshot.Settings.MixedPort = port
	m.stagedSettings = cloneTUISettings(&m.snapshot.Settings)
	m.settingsDirty = true
	m.snapshot.Status = fmt.Sprintf(
		"Mixed port %d staged; enable System proxy or start Core to apply",
		port,
	)
	m.persistStagedTUISettings()
}

func (m *tuiModel) stageTUISetting(key tuiKey) {
	switch key {
	case tuiKeyAllowLAN:
		m.snapshot.Settings.AllowLAN = !m.snapshot.Settings.AllowLAN
	case tuiKeyIPv6:
		m.snapshot.Settings.IPv6 = !m.snapshot.Settings.IPv6
	case tuiKeyUnifiedDelay:
		m.snapshot.Settings.UnifiedDelay = !m.snapshot.Settings.UnifiedDelay
	case tuiKeyTCPConcurrent:
		m.snapshot.Settings.TCPConcurrent = !m.snapshot.Settings.TCPConcurrent
	case tuiKeyTun:
		m.snapshot.Settings.TunEnabled = !m.snapshot.Settings.TunEnabled
	case tuiKeyMode:
		switch strings.ToLower(m.snapshot.Settings.Mode) {
		case "rule":
			m.snapshot.Settings.Mode = "global"
		case "global":
			m.snapshot.Settings.Mode = "direct"
		default:
			m.snapshot.Settings.Mode = "rule"
		}
	case tuiKeyLogLevel:
		levels := []string{"silent", "error", "warning", "info", "debug"}
		current := findTUIString(levels, strings.ToLower(m.snapshot.Settings.LogLevel))
		m.snapshot.Settings.LogLevel = levels[wrapTUIIndex(current, 1, len(levels))]
	default:
		return
	}
	port := m.snapshot.Settings.MixedPort
	m.pendingMixedPort = &port
	m.stagedSettings = cloneTUISettings(&m.snapshot.Settings)
	m.settingsDirty = true
	m.snapshot.Status = "Settings staged; enable System proxy or start Core to apply"
	m.persistStagedTUISettings()
}

func (m *tuiModel) persistStagedTUISettings() {
	if m.stagedSettings == nil {
		return
	}
	if m.service != nil {
		m.settingsDirty = true
		m.snapshot.Status += "; pending backend commit"
		return
	}
	if err := persistTUISettings(m.paths.configPath, *m.stagedSettings); err != nil {
		m.snapshot.Status += "; save failed: " + err.Error()
		m.settingsDirty = true
		return
	}
	m.settingsDirty = false
	m.snapshot.Status += "; saved for next launch"
}

func persistTUIOperationSettings(state *tuiOperationState) {
	if err := persistTUISettings(state.paths.configPath, state.snapshot.Settings); err != nil {
		state.stagedSettings = cloneTUISettings(&state.snapshot.Settings)
		state.settingsDirty = true
		state.snapshot.Status += "; save failed: " + err.Error()
		return
	}
	state.settingsDirty = false
}

func applyTUIOperationSetting(
	state *tuiOperationState,
	service *tuiServiceClient,
	client controllerClient,
	key tuiKey,
) {
	if service == nil {
		if updateTUISettings(&state.snapshot, client, key) {
			persistTUIOperationSettings(state)
		}
		return
	}
	settings := state.snapshot.Settings
	if !changeTUISettingsValue(&settings, key) {
		state.snapshot.Status = "Setting is already at its limit"
		return
	}
	commitTUIOperationSettings(state, service, client, settings)
}

func commitTUIOperationSettings(
	state *tuiOperationState,
	service *tuiServiceClient,
	client controllerClient,
	settings tuiSettings,
) {
	if !prepareTUIBackendRevision(state, service) {
		return
	}
	status, err := service.applySettings(settings, state.backendRevision)
	if err != nil {
		state.snapshot.Status = "Settings commit failed: " + err.Error()
		return
	}
	state.backendRevision = status.Revision
	state.snapshot.Settings = settings
	state.snapshot.Settings.SystemProxy = status.SystemProxy
	state.coreRunning = status.Running
	state.settingsDirty = false
	state.stagedSettings = nil
	state.pendingMixedPort = nil
	state.snapshot.Status = fmt.Sprintf(
		"Settings committed at revision %d",
		status.Revision,
	)
	refreshTUISnapshot(&state.snapshot, client)
}

func prepareTUIBackendRevision(
	state *tuiOperationState,
	service *tuiServiceClient,
) bool {
	if state.backendRevision > 0 {
		return true
	}
	status, err := service.status()
	if err != nil {
		state.snapshot.Status = "Cannot read backend state: " + err.Error()
		return false
	}
	state.backendRevision = status.Revision
	state.coreRunning = status.Running
	state.snapshot.Settings.SystemProxy = status.SystemProxy
	return true
}

func changeTUISettingsValue(settings *tuiSettings, key tuiKey) bool {
	switch key {
	case tuiKeyAllowLAN:
		settings.AllowLAN = !settings.AllowLAN
	case tuiKeyIPv6:
		settings.IPv6 = !settings.IPv6
	case tuiKeyUnifiedDelay:
		settings.UnifiedDelay = !settings.UnifiedDelay
	case tuiKeyTCPConcurrent:
		settings.TCPConcurrent = !settings.TCPConcurrent
	case tuiKeyTun:
		settings.TunEnabled = !settings.TunEnabled
	case tuiKeyMode:
		switch strings.ToLower(settings.Mode) {
		case "rule":
			settings.Mode = "global"
		case "global":
			settings.Mode = "direct"
		default:
			settings.Mode = "rule"
		}
	case tuiKeyLogLevel:
		levels := []string{"silent", "error", "warning", "info", "debug"}
		current := findTUIString(levels, strings.ToLower(settings.LogLevel))
		settings.LogLevel = levels[wrapTUIIndex(current, 1, len(levels))]
	case tuiKeyPortUp:
		if settings.MixedPort >= 65535 {
			return false
		}
		settings.MixedPort++
	case tuiKeyPortDown:
		if settings.MixedPort <= 0 {
			return false
		}
		settings.MixedPort--
	default:
		return false
	}
	return true
}

func syncStoppedTUISettings(state *tuiOperationState) {
	if state.coreRunning {
		return
	}
	settings := loadTUIConfiguredSettings(state.paths.configPath, true)
	if settings == nil {
		state.snapshot.Status += "; could not reload settings from YAML"
		return
	}
	settings.SystemProxy = state.snapshot.Settings.SystemProxy
	state.snapshot.Settings = *settings
	state.stagedSettings = cloneTUISettings(settings)
	state.settingsDirty = false
	port := settings.MixedPort
	state.pendingMixedPort = &port
}

func reloadTUIOperationConfig(
	state *tuiOperationState,
	service *tuiServiceClient,
	client controllerClient,
	ownsCore bool,
) error {
	return reloadTUIOperationConfigExpected(
		state,
		service,
		client,
		ownsCore,
		"",
	)
}

func reloadTUIOperationConfigExpected(
	state *tuiOperationState,
	service *tuiServiceClient,
	client controllerClient,
	ownsCore bool,
	expectedSHA256 string,
) error {
	if service != nil {
		if !prepareTUIBackendRevision(state, service) {
			return errors.New("backend revision is unavailable")
		}
		var status tuiServiceStatus
		var err error
		if expectedSHA256 == "" {
			status, err = service.reloadAtRevision(
				state.paths.configPath,
				state.backendRevision,
			)
		} else {
			status, err = service.reloadAtRevisionWithDigest(
				state.paths.configPath,
				state.backendRevision,
				expectedSHA256,
			)
		}
		if err != nil {
			return err
		}
		state.coreRunning = status.Running
		state.backendRevision = status.Revision
		state.snapshot.GroupOrder = loadTUIProxyGroupOrder(state.paths.configPath)
		return nil
	}
	if ownsCore {
		if message := handleSetupConfig(state.setupParams); message != "" {
			return errors.New(message)
		}
		state.snapshot.GroupOrder = loadTUIProxyGroupOrder(state.paths.configPath)
		return nil
	}
	data, err := os.ReadFile(state.paths.configPath)
	if err != nil {
		return err
	}
	if message := validateConfigBytes(data); message != "" {
		return errors.New(message)
	}
	if err := client.reloadConfigPayload(data); err != nil {
		return err
	}
	state.snapshot.GroupOrder = loadTUIProxyGroupOrder(state.paths.configPath)
	return nil
}

func persistTUISettings(path string, settings tuiSettings) error {
	writePath := path
	fileInfo, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fileInfo.Mode()&os.ModeSymlink != 0 {
		writePath, err = filepath.EvalSymlinks(path)
		if err != nil {
			return err
		}
	}
	fileInfo, err = os.Stat(writePath)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(writePath)
	if err != nil {
		return err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("parse YAML: %w", err)
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("configuration root must be a YAML mapping")
	}
	root := document.Content[0]
	setTUIYAMLScalar(root, "mode", settings.Mode, "!!str")
	setTUIYAMLScalar(root, "mixed-port", strconv.Itoa(settings.MixedPort), "!!int")
	setTUIYAMLScalar(root, "allow-lan", strconv.FormatBool(settings.AllowLAN), "!!bool")
	setTUIYAMLScalar(root, "ipv6", strconv.FormatBool(settings.IPv6), "!!bool")
	setTUIYAMLScalar(
		root,
		"unified-delay",
		strconv.FormatBool(settings.UnifiedDelay),
		"!!bool",
	)
	setTUIYAMLScalar(
		root,
		"tcp-concurrent",
		strconv.FormatBool(settings.TCPConcurrent),
		"!!bool",
	)
	setTUIYAMLScalar(root, "log-level", settings.LogLevel, "!!str")
	tun := tuiYAMLMappingValue(root, "tun")
	if tun == nil {
		root.Content = append(
			root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "tun"},
			&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"},
		)
		tun = root.Content[len(root.Content)-1]
	}
	if tun.Kind != yaml.MappingNode {
		return errors.New("TUN configuration must be a YAML mapping")
	}
	setTUIYAMLScalar(tun, "enable", strconv.FormatBool(settings.TunEnabled), "!!bool")

	updated, err := yaml.Marshal(&document)
	if err != nil {
		return fmt.Errorf("encode YAML: %w", err)
	}
	if message := validateConfigBytes(updated); message != "" {
		return errors.New(message)
	}
	temp, err := os.CreateTemp(
		filepath.Dir(writePath),
		"."+filepath.Base(writePath)+".tmp-*",
	)
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(fileInfo.Mode().Perm()); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(updated); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, writePath); err != nil {
		return err
	}
	return nil
}

func ensureTUIFlClashDefaults(path string) error {
	homeDir := filepath.Dir(path)
	lease, err := acquireTUIProfileLocks(homeDir, path)
	if err != nil {
		return err
	}
	defer lease.release()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return fmt.Errorf("parse YAML: %w", err)
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("configuration root must be a YAML mapping")
	}
	root := document.Content[0]
	missingIPv6 := tuiYAMLMappingValue(root, "ipv6") == nil
	missingUnifiedDelay := tuiYAMLMappingValue(root, "unified-delay") == nil
	missingTCPConcurrent := tuiYAMLMappingValue(root, "tcp-concurrent") == nil
	if !missingIPv6 && !missingUnifiedDelay && !missingTCPConcurrent {
		return nil
	}
	settings := loadTUIConfiguredSettings(path, true)
	if settings == nil {
		return errors.New("could not load current settings")
	}
	if missingIPv6 {
		settings.IPv6 = false
	}
	if missingUnifiedDelay {
		settings.UnifiedDelay = true
	}
	if missingTCPConcurrent {
		settings.TCPConcurrent = true
	}
	return persistTUISettings(path, *settings)
}

func tuiYAMLMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return mapping.Content[index+1]
		}
	}
	return nil
}

func setTUIYAMLScalar(mapping *yaml.Node, key, value, tag string) {
	node := tuiYAMLMappingValue(mapping, key)
	if node == nil {
		mapping.Content = append(
			mapping.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
			&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value},
		)
		return
	}
	node.Kind = yaml.ScalarNode
	node.Tag = tag
	node.Value = value
	node.Content = nil
}

func ensureTUIProxyPortFree(port int) error {
	address := fmt.Sprintf("0.0.0.0:%d", port)
	probe, err := net.Listen("tcp4", address)
	if err != nil {
		return fmt.Errorf("mixed port %d is already in use", port)
	}
	return probe.Close()
}

func stageTUICoreSettings(settings tuiSettings) string {
	data, err := json.Marshal(map[string]interface{}{
		"mode":           settings.Mode,
		"mixed-port":     settings.MixedPort,
		"allow-lan":      settings.AllowLAN,
		"ipv6":           settings.IPv6,
		"unified-delay":  settings.UnifiedDelay,
		"tcp-concurrent": settings.TCPConcurrent,
		"log-level":      settings.LogLevel,
		"tun": map[string]bool{
			"enable": settings.TunEnabled,
		},
	})
	if err != nil {
		return err.Error()
	}
	return handleUpdateConfig(data)
}

func startTUIManagedCore(
	state *tuiOperationState,
	service *tuiServiceClient,
) bool {
	if state.coreRunning {
		return true
	}
	if state.snapshot.Settings.MixedPort <= 0 {
		state.snapshot.Status = "Choose a positive mixed port before starting"
		return false
	}
	if err := ensureTUIProxyPortFree(state.snapshot.Settings.MixedPort); err != nil {
		state.snapshot.Status = "Cannot start: " + err.Error()
		return false
	}
	port := state.snapshot.Settings.MixedPort
	if state.stagedSettings != nil {
		if service != nil {
			if !prepareTUIBackendRevision(state, service) {
				return false
			}
			status, err := service.applySettings(
				*state.stagedSettings,
				state.backendRevision,
			)
			if err != nil {
				state.snapshot.Status = "Cannot commit staged settings: " + err.Error()
				return false
			}
			state.backendRevision = status.Revision
			state.paths.configPath = status.ConfigPath
			state.snapshot.Settings.SystemProxy = status.SystemProxy
		} else if state.settingsDirty {
			if err := persistTUISettings(
				state.paths.configPath,
				*state.stagedSettings,
			); err != nil {
				state.snapshot.Status = "Cannot save staged settings: " + err.Error()
				return false
			}
		}
		if service == nil {
			if message := stageTUICoreSettings(*state.stagedSettings); message != "" {
				state.snapshot.Status = "Cannot apply staged settings: " + message
				return false
			}
		}
	}
	if service != nil {
		if !prepareTUIBackendRevision(state, service) {
			return false
		}
		status, err := service.startAtRevision(state.backendRevision)
		if err != nil {
			state.snapshot.Status = "Cannot start core listeners: " + err.Error()
			return false
		}
		state.backendRevision = status.Revision
	} else if !handleStartListener() {
		state.snapshot.Status = "Cannot start core listeners"
		return false
	}
	state.coreRunning = true
	state.pendingMixedPort = nil
	state.stagedSettings = nil
	state.settingsDirty = false
	state.snapshot.Status = fmt.Sprintf("Core listeners started on port %d", port)
	return true
}

func stopTUIManagedCore(
	state *tuiOperationState,
	service *tuiServiceClient,
) bool {
	if !state.coreRunning {
		return true
	}
	if service != nil {
		if !prepareTUIBackendRevision(state, service) {
			return false
		}
		status, err := service.stopAtRevision(state.backendRevision)
		if err != nil {
			state.snapshot.Status = "Cannot stop core listeners: " + err.Error()
			return false
		}
		state.backendRevision = status.Revision
		state.snapshot.Settings.SystemProxy = status.SystemProxy
	} else if !handleStopListener() {
		state.snapshot.Status = "Cannot stop core listeners"
		return false
	}
	port := state.snapshot.Settings.MixedPort
	state.coreRunning = false
	state.pendingMixedPort = &port
	state.stagedSettings = cloneTUISettings(&state.snapshot.Settings)
	state.settingsDirty = false
	state.snapshot.Status = "Core listeners stopped"
	if service == nil && state.systemProxyManaged && state.snapshot.Settings.SystemProxy {
		if !linuxSystemProxyMatches(state.snapshot.Settings.MixedPort) {
			state.snapshot.Settings.SystemProxy = false
			state.systemProxyManaged = false
			state.snapshot.Status += "; another instance owns the system proxy"
		} else if err := setLinuxSystemProxy(
			state.snapshot.Settings.MixedPort,
			false,
		); err != nil {
			state.snapshot.Status += "; system proxy cleanup failed: " + err.Error()
		} else {
			state.snapshot.Settings.SystemProxy = false
			state.systemProxyManaged = false
		}
	}
	return true
}

func (m *tuiModel) selectCurrent() tea.Cmd {
	switch m.snapshot.Page {
	case tuiPageDashboard:
		switch m.snapshot.SelectedDashboard {
		case tuiDashboardServiceRow:
			return m.handleKey(tuiKeyCoreToggle)
		case tuiDashboardSystemProxyRow:
			return m.handleKey(tuiKeySystemProxy)
		case tuiDashboardTunRow:
			return m.handleKey(tuiKeyTun)
		case tuiDashboardModeRow:
			return m.handleKey(tuiKeyMode)
		case tuiDashboardMixedPortRow:
			m.beginInput(tuiInputMixedPort)
		}
	case tuiPageProxies:
		if m.snapshot.ProxyView == tuiProxyViewProviders {
			return m.startOperation(func(state *tuiOperationState) {
				updateTUIProvider(&state.snapshot, m.client)
			})
		}
		if !m.snapshot.ProxyNodeFocus {
			if m.snapshot.SelectedGroup < 0 ||
				m.snapshot.SelectedGroup >= len(m.snapshot.Groups) {
				m.snapshot.Status = "Select a proxy group first"
				return nil
			}
			m.snapshot.ProxyNodeFocus = true
			group := m.snapshot.Groups[m.snapshot.SelectedGroup]
			m.snapshot.SelectedNode = findTUIString(group.Nodes, group.Now)
			m.snapshot.Status = "Nodes in " + group.Name +
				" · ↑↓/ws select · Enter apply · Esc back"
			return nil
		}
		return m.startOperation(func(state *tuiOperationState) {
			selectTUIProxy(&state.snapshot, m.client, state.paths.homeDir)
		})
	case tuiPageProfiles:
		if m.snapshot.SelectedRow < 0 {
			m.beginInput(tuiInputSubscription)
			return nil
		}
		return m.startOperation(func(state *tuiOperationState) {
			if m.service != nil {
				switchTUIServiceProfile(state, m.service, m.client)
			} else {
				switchTUIProfile(
					&state.snapshot,
					&state.paths,
					&state.setupParams,
					m.client,
					m.ownsCore,
					state.coreRunning,
				)
			}
			state.pendingMixedPort = nil
			state.stagedSettings = nil
			state.settingsDirty = false
			if !state.coreRunning {
				state.stagedSettings = loadTUIConfiguredSettings(state.paths.configPath, true)
				if state.stagedSettings != nil {
					state.snapshot.Settings = *state.stagedSettings
					port := state.stagedSettings.MixedPort
					state.pendingMixedPort = &port
				}
			}
		})
	case tuiPageTools:
		return m.selectTUISetting(m.snapshot.SelectedTool)
	case tuiPageMaintenance:
		switch m.snapshot.SelectedMaintenance {
		case tuiMaintenanceEditConfigRow:
			return m.startEditor(m.paths.configPath)
		case tuiMaintenanceBackupRow:
			return m.runTool(1)
		case tuiMaintenanceRestoreRow:
			return m.runTool(2)
		case tuiMaintenanceGeoUpdateRow:
			return m.runTool(3)
		case tuiMaintenanceResetTrafficRow:
			return m.runTool(4)
		case tuiMaintenanceUpdateRow:
			return m.runTool(5)
		}
	}
	return nil
}

func switchTUIServiceProfile(
	state *tuiOperationState,
	service *tuiServiceClient,
	client controllerClient,
) {
	if state.snapshot.SelectedRow < 0 ||
		state.snapshot.SelectedRow >= len(state.snapshot.Profiles) {
		return
	}
	profile := state.snapshot.Profiles[state.snapshot.SelectedRow]
	if profile.Current {
		state.snapshot.Status = "Profile is already active"
		return
	}
	if message := handleValidateConfig(profile.Path); message != "" {
		state.snapshot.Status = "Profile invalid: " + message
		return
	}
	if err := ensureTUIFlClashDefaults(profile.Path); err != nil {
		state.snapshot.Status = "Profile defaults failed: " + err.Error()
		return
	}
	expectedSHA256, err := tuiFileSHA256(profile.Path)
	if err != nil {
		state.snapshot.Status = "Profile read failed: " + err.Error()
		return
	}
	if !prepareTUIBackendRevision(state, service) {
		return
	}
	status, err := service.reloadAtRevisionWithDigest(
		profile.Path,
		state.backendRevision,
		expectedSHA256,
	)
	if err != nil {
		state.snapshot.Status = "Profile hot-reload failed: " + err.Error()
		return
	}
	state.paths.configPath = profile.Path
	state.coreRunning = status.Running
	state.backendRevision = status.Revision
	state.snapshot.GroupOrder = loadTUIProxyGroupOrder(profile.Path)
	state.snapshot.ProxyNodeFocus = false
	state.snapshot.Status = "Active profile: " + profile.Name
	if err := rememberTUIActiveProfile(state.paths); err != nil {
		state.snapshot.Status += "; could not remember profile: " + err.Error()
	}
	refreshTUISnapshot(&state.snapshot, client)
	refreshTUIProfiles(&state.snapshot, state.paths)
}

func (m *tuiModel) selectTUISetting(index int) tea.Cmd {
	switch index {
	case tuiSettingsModeRow:
		return m.handleKey(tuiKeyMode)
	case tuiSettingsMixedPortRow:
		m.beginInput(tuiInputMixedPort)
	case tuiSettingsAllowLANRow:
		return m.handleKey(tuiKeyAllowLAN)
	case tuiSettingsIPv6Row:
		return m.handleKey(tuiKeyIPv6)
	case tuiSettingsUnifiedDelayRow:
		return m.handleKey(tuiKeyUnifiedDelay)
	case tuiSettingsTCPConcurrentRow:
		return m.handleKey(tuiKeyTCPConcurrent)
	case tuiSettingsLogLevelRow:
		return m.handleKey(tuiKeyLogLevel)
	case tuiSettingsTunRow:
		return m.handleKey(tuiKeyTun)
	case tuiSettingsServiceRow:
		return m.handleKey(tuiKeyCoreToggle)
	case tuiSettingsSystemProxyRow:
		return m.handleKey(tuiKeySystemProxy)
	}
	return nil
}

func (m *tuiModel) runTool(index int) tea.Cmd {
	if index == 5 {
		m.snapshot.Update.Loading = true
		m.snapshot.Update.Error = ""
	}
	return m.startOperation(func(state *tuiOperationState) {
		switch index {
		case 1:
			if backupPath, err := backupTUIConfig(state.paths.configPath); err != nil {
				state.snapshot.Status = "Backup failed: " + err.Error()
			} else {
				state.snapshot.Status = "Backup created: " + filepath.Base(backupPath)
			}
		case 2:
			backupPath, backup, err := restoreLatestTUIConfigLocked(
				state.paths.homeDir,
				state.paths.configPath,
			)
			if err != nil {
				state.snapshot.Status = "Restore failed: " + err.Error()
			} else {
				defer backup.release()
				if m.service != nil {
					backup.release()
				}
				if reloadErr := reloadTUIOperationConfigExpected(
					state,
					m.service,
					m.client,
					m.ownsCore,
					backup.updatedSHA256,
				); reloadErr != nil {
					var rollbackErr error
					if backup.lock != nil {
						rollbackErr = restoreTUISubscriptionProfile(
							state.paths.configPath,
							backup,
						)
					} else {
						rollbackErr = restoreTUIProfileIfUnchanged(
							state.paths.homeDir,
							state.paths.configPath,
							backup,
						)
					}
					state.snapshot.Status = "Restore hot-reload failed: " + reloadErr.Error()
					if rollbackErr == nil {
						originalReloadErr := reloadTUIOperationConfigExpected(
							state,
							m.service,
							m.client,
							m.ownsCore,
							tuiBytesSHA256(backup.data),
						)
						if originalReloadErr == nil {
							state.snapshot.Status += "; original restored"
						} else {
							state.snapshot.Status += "; file restored but Core reload failed: " +
								originalReloadErr.Error()
						}
					} else {
						state.snapshot.Status += "; rollback skipped: " + rollbackErr.Error()
					}
				} else {
					state.snapshot.Status = "Restored and hot-reloaded: " +
						filepath.Base(backupPath)
					syncStoppedTUISettings(state)
				}
			}
		case 3:
			if err := m.client.updateGeo(); err != nil {
				state.snapshot.Status = "Geo update failed: " + err.Error()
			} else {
				state.snapshot.Status = "Geo databases update started"
			}
		case 4:
			if m.ownsCore {
				handleResetTraffic()
				state.snapshot.Status = "Traffic counters reset"
			} else {
				state.snapshot.Status = "Traffic reset requires a core started by this process"
			}
		case 5:
			checkTUIUpdate(&state.snapshot)
		}
	})
}

func (m *tuiModel) testSelectedProxyDelay() tea.Cmd {
	if m.snapshot.SelectedGroup < 0 ||
		m.snapshot.SelectedGroup >= len(m.snapshot.Groups) {
		m.snapshot.Status = "Select a proxy group first"
		return nil
	}
	group := m.snapshot.Groups[m.snapshot.SelectedGroup]
	if m.snapshot.SelectedNode < 0 ||
		m.snapshot.SelectedNode >= len(group.Nodes) {
		m.snapshot.Status = "Select a proxy node first"
		return nil
	}
	groupName := group.Name
	node := group.Nodes[m.snapshot.SelectedNode]
	testURL := m.tuiDelayTestURL()
	setTUIGroupDelay(&m.snapshot, groupName, node, -2)
	return m.startOperation(func(state *tuiOperationState) {
		delay, err := m.client.testProxyDelay(node, testURL)
		if err != nil {
			setTUIGroupDelay(&state.snapshot, groupName, node, -1)
			state.snapshot.Status = node + " delay: Timeout · " + err.Error()
			return
		}
		setTUIGroupDelay(&state.snapshot, groupName, node, delay)
		state.snapshot.Status = fmt.Sprintf("%s delay: %d ms", node, delay)
	})
}

func (m *tuiModel) testSelectedProxyGroupDelays() tea.Cmd {
	if m.snapshot.SelectedGroup < 0 ||
		m.snapshot.SelectedGroup >= len(m.snapshot.Groups) {
		m.snapshot.Status = "Select a proxy group first"
		return nil
	}
	group := m.snapshot.Groups[m.snapshot.SelectedGroup]
	if len(group.Nodes) == 0 {
		m.snapshot.Status = "Selected proxy group has no nodes"
		return nil
	}
	nodes := append([]string(nil), group.Nodes...)
	testingDelays := make(map[string]int, len(nodes))
	for _, node := range nodes {
		testingDelays[node] = -2
	}
	setTUIGroupDelays(&m.snapshot, group.Name, testingDelays)
	testURL := m.tuiDelayTestURL()
	return m.startOperation(func(state *tuiOperationState) {
		delays := testTUIProxyDelays(m.client, nodes, testURL)
		successes := 0
		for _, delay := range delays {
			if delay > 0 {
				successes++
			}
		}
		setTUIGroupDelays(&state.snapshot, group.Name, delays)
		state.snapshot.Status = fmt.Sprintf(
			"%s delays complete: %d/%d reachable",
			group.Name,
			successes,
			len(nodes),
		)
	})
}

func (m *tuiModel) testDashboardDelay() tea.Cmd {
	if m.service == nil {
		m.snapshot.Status = "Dashboard delay test requires the managed backend"
		return nil
	}
	m.snapshot.DashboardDelay = -2
	mixedPort := m.snapshot.Settings.MixedPort
	testURL := m.tuiDelayTestURL()
	return m.startOperation(func(state *tuiOperationState) {
		delay, err := m.service.testRouteDelay(mixedPort, testURL)
		if err != nil {
			state.snapshot.DashboardDelay = -1
			state.snapshot.Status = "Current route delay: Timeout · " + err.Error()
			return
		}
		state.snapshot.DashboardDelay = delay
		state.snapshot.Status = fmt.Sprintf(
			"Current route delay: %d ms",
			delay,
		)
	})
}

func (m *tuiModel) testDashboardSpeed() tea.Cmd {
	if m.service == nil {
		m.snapshot.Status = "Dashboard speed test requires the managed backend"
		return nil
	}
	m.snapshot.DashboardSpeed = tuiSpeedResult{Testing: true}
	mixedPort := m.snapshot.Settings.MixedPort
	return m.startOperation(func(state *tuiOperationState) {
		result, err := m.service.testRouteSpeed(mixedPort)
		if err != nil {
			state.snapshot.DashboardSpeed = tuiSpeedResult{Error: err.Error()}
			state.snapshot.Status = "Current route speed failed: " + err.Error()
			return
		}
		state.snapshot.DashboardSpeed = result
		state.snapshot.Status = "Current route speed: " + formatTUISpeed(result)
	})
}

func (m *tuiModel) testSelectedProxySpeed() tea.Cmd {
	if m.service == nil {
		m.snapshot.Status = "Proxy speed testing requires the managed backend"
		return nil
	}
	if m.snapshot.SelectedGroup < 0 ||
		m.snapshot.SelectedGroup >= len(m.snapshot.Groups) {
		m.snapshot.Status = "Select a proxy group first"
		return nil
	}
	group := m.snapshot.Groups[m.snapshot.SelectedGroup]
	if m.snapshot.SelectedNode < 0 ||
		m.snapshot.SelectedNode >= len(group.Nodes) {
		m.snapshot.Status = "Select a proxy node first"
		return nil
	}
	groupName := group.Name
	node := group.Nodes[m.snapshot.SelectedNode]
	setTUIGroupSpeed(
		&m.snapshot,
		groupName,
		node,
		tuiSpeedResult{Testing: true},
	)
	return m.startOperation(func(state *tuiOperationState) {
		result, err := m.service.testProxySpeed(node)
		if err != nil {
			setTUIGroupSpeed(
				&state.snapshot,
				groupName,
				node,
				tuiSpeedResult{Error: err.Error()},
			)
			state.snapshot.Status = node + " speed failed: " + err.Error()
			return
		}
		setTUIGroupSpeed(&state.snapshot, groupName, node, result)
		state.snapshot.Status = node + " speed: " + formatTUISpeed(result)
	})
}

func (m *tuiModel) testSelectedProxyGroupSpeeds() tea.Cmd {
	if m.service == nil {
		m.snapshot.Status = "Proxy speed testing requires the managed backend"
		return nil
	}
	if m.snapshot.SelectedGroup < 0 ||
		m.snapshot.SelectedGroup >= len(m.snapshot.Groups) {
		m.snapshot.Status = "Select a proxy group first"
		return nil
	}
	group := m.snapshot.Groups[m.snapshot.SelectedGroup]
	if len(group.Nodes) == 0 {
		m.snapshot.Status = "Selected proxy group has no nodes"
		return nil
	}
	nodes := append([]string(nil), group.Nodes...)
	testingSpeeds := make(map[string]tuiSpeedResult, len(nodes))
	for _, node := range nodes {
		testingSpeeds[node] = tuiSpeedResult{Testing: true}
	}
	setTUIGroupSpeeds(&m.snapshot, group.Name, testingSpeeds)
	if m.busy {
		m.snapshot.Status = "Another operation is still running"
		return nil
	}
	m.busy = true
	m.refreshInFlight = false
	m.refreshSequence++
	m.snapshot.Status = fmt.Sprintf(
		"%s speed tests: 0/%d complete · up to %d MB total",
		group.Name,
		len(nodes),
		len(nodes)*100,
	)
	return m.testNextProxyGroupSpeed(group.Name, nodes, len(nodes), 0)
}

func (m *tuiModel) testNextProxyGroupSpeed(
	groupName string,
	nodes []string,
	total,
	successes int,
) tea.Cmd {
	if len(nodes) == 0 {
		return nil
	}
	node := nodes[0]
	remaining := append([]string(nil), nodes[1:]...)
	service := m.service
	return func() tea.Msg {
		result, err := service.testProxySpeed(node)
		if err != nil {
			result = tuiSpeedResult{Error: err.Error()}
		}
		return tuiProxyGroupSpeedResultMsg{
			groupName: groupName,
			node:      node,
			result:    result,
			remaining: remaining,
			total:     total,
			successes: successes,
		}
	}
}

func (m *tuiModel) tuiDelayTestURL() string {
	testURL := "https://www.gstatic.com/generate_204"
	params := defaultSetupParams()
	if len(m.setupParams) > 0 &&
		UnmarshalJson(m.setupParams, params) == nil &&
		params.TestURL != "" {
		testURL = params.TestURL
	}
	return testURL
}

func testTUIProxyDelays(
	client controllerClient,
	nodes []string,
	testURL string,
) map[string]int {
	const parallelism = 16
	delays := make(map[string]int, len(nodes))
	var mutex sync.Mutex
	var waitGroup sync.WaitGroup
	limit := make(chan struct{}, parallelism)
	for _, node := range nodes {
		node := node
		waitGroup.Add(1)
		go func() {
			defer waitGroup.Done()
			limit <- struct{}{}
			delay, err := client.testProxyDelay(node, testURL)
			<-limit
			if err != nil {
				delay = -1
			}
			mutex.Lock()
			delays[node] = delay
			mutex.Unlock()
		}()
	}
	waitGroup.Wait()
	return delays
}

func setTUIGroupDelay(
	snapshot *tuiSnapshot,
	groupName,
	node string,
	delay int,
) {
	setTUIGroupDelays(snapshot, groupName, map[string]int{node: delay})
}

func setTUIGroupDelays(
	snapshot *tuiSnapshot,
	groupName string,
	updates map[string]int,
) {
	groupIndex := -1
	for index, group := range snapshot.Groups {
		if group.Name == groupName {
			groupIndex = index
			break
		}
	}
	if groupIndex < 0 {
		return
	}
	groups := append([]tuiGroup(nil), snapshot.Groups...)
	group := groups[groupIndex]
	delays := make(map[string]int, len(group.Delays)+len(updates))
	for name, value := range group.Delays {
		delays[name] = value
	}
	for node, delay := range updates {
		delays[node] = delay
	}
	group.Delays = delays
	groups[groupIndex] = group
	snapshot.Groups = groups
}

func setTUIGroupSpeed(
	snapshot *tuiSnapshot,
	groupName,
	node string,
	speed tuiSpeedResult,
) {
	setTUIGroupSpeeds(
		snapshot,
		groupName,
		map[string]tuiSpeedResult{node: speed},
	)
}

func setTUIGroupSpeeds(
	snapshot *tuiSnapshot,
	groupName string,
	updates map[string]tuiSpeedResult,
) {
	groupIndex := -1
	for index, group := range snapshot.Groups {
		if group.Name == groupName {
			groupIndex = index
			break
		}
	}
	if groupIndex < 0 {
		return
	}
	groups := append([]tuiGroup(nil), snapshot.Groups...)
	group := groups[groupIndex]
	speeds := make(
		map[string]tuiSpeedResult,
		len(group.Speeds)+len(updates),
	)
	for name, value := range group.Speeds {
		speeds[name] = value
	}
	for node, speed := range updates {
		speeds[node] = speed
	}
	group.Speeds = speeds
	groups[groupIndex] = group
	snapshot.Groups = groups
}

func formatTUISpeed(result tuiSpeedResult) string {
	megabytesPerSecond := result.BytesPerSecond / 1_000_000
	megabitsPerSecond := result.BytesPerSecond * 8 / 1_000_000
	return fmt.Sprintf(
		"%.2f MB/s · %.1f Mbps",
		megabytesPerSecond,
		megabitsPerSecond,
	)
}

func (m *tuiModel) startEditor(path string) tea.Cmd {
	if m.busy {
		m.snapshot.Status = "Another operation is still running"
		return nil
	}
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	lease, err := acquireTUIProfileLocks(m.paths.homeDir, path)
	if err != nil {
		m.snapshot.Status = "Editor failed: " + err.Error()
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		lease.release()
		m.snapshot.Status = "Editor failed: " + err.Error()
		return nil
	}
	if !info.Mode().IsRegular() {
		lease.release()
		m.snapshot.Status = "Editor failed: configuration must be a regular file"
		return nil
	}
	backup, err := os.ReadFile(path)
	if err != nil {
		lease.release()
		m.snapshot.Status = "Editor failed: " + err.Error()
		return nil
	}
	command := exec.Command("sh", "-c", editor+" -- \"$1\"", "flclash-tui-editor", path)
	m.editorPath = path
	m.editorBackup = tuiProfileBackup{
		data: backup,
		mode: info.Mode(),
		lock: lease,
	}
	m.busy = true
	m.snapshot.Status = "Editor open"
	return tea.ExecProcess(command, func(err error) tea.Msg {
		return tuiEditorResultMsg{err: err}
	})
}

func (m *tuiModel) beginInput(mode tuiInputMode) {
	m.inputMode = mode
	m.inputValue = m.inputValue[:0]
	m.inputCursor = 0
	m.inputSelectAll = false
	if mode == tuiInputMixedPort {
		m.inputValue = []rune(strconv.Itoa(m.snapshot.Settings.MixedPort))
		m.inputCursor = len(m.inputValue)
		m.inputSelectAll = true
	} else if mode == tuiInputProfileName {
		name := strings.TrimSuffix(
			filepath.Base(m.renameProfilePath),
			filepath.Ext(m.renameProfilePath),
		)
		m.inputValue = []rune(name)
		m.inputCursor = len(m.inputValue)
		m.inputSelectAll = true
	}
}

func (m *tuiModel) beginProfileRename() {
	if m.snapshot.SelectedRow < 0 {
		m.snapshot.Status = "Select a profile file before renaming"
		return
	}
	if m.snapshot.SelectedRow >= len(m.snapshot.Profiles) {
		m.snapshot.Status = "Selected profile is no longer available"
		return
	}
	profile := m.snapshot.Profiles[m.snapshot.SelectedRow]
	if profile.Current {
		m.snapshot.Status = "Activate another profile before renaming the current one"
		return
	}
	m.renameProfilePath = profile.Path
	m.beginInput(tuiInputProfileName)
}

func (m *tuiModel) updateSelectedProfileSubscription() tea.Cmd {
	if m.snapshot.SelectedRow < 0 {
		m.snapshot.Status = "Select a profile before updating its subscription"
		return nil
	}
	if m.snapshot.SelectedRow >= len(m.snapshot.Profiles) {
		m.snapshot.Status = "Selected profile is no longer available"
		return nil
	}
	profile := m.snapshot.Profiles[m.snapshot.SelectedRow]
	sourceURL, err := loadTUISubscriptionSource(m.paths.homeDir, profile.Path)
	if err != nil {
		m.snapshot.Status = "Subscription refresh unavailable: " + err.Error()
		return nil
	}
	return m.startProfileSubscriptionUpdate(profile.Path, sourceURL)
}

func (m *tuiModel) startProfileSubscriptionUpdate(
	profilePath,
	sourceURL string,
) tea.Cmd {
	if profilePath == "" {
		m.snapshot.Status = "Update failed: selected profile path is empty"
		return nil
	}
	return m.startOperation(func(state *tuiOperationState) {
		isActive := filepath.Clean(profilePath) == filepath.Clean(state.paths.configPath)
		backup, err := updateTUISubscriptionProfile(
			state.paths.homeDir,
			profilePath,
			sourceURL,
		)
		if err != nil {
			state.snapshot.Status = "Subscription update failed: " + err.Error()
			return
		}
		defer backup.release()
		if err := rememberTUISubscriptionSource(
			state.paths.homeDir,
			profilePath,
			sourceURL,
		); err != nil {
			rollbackErr := restoreTUISubscriptionProfile(profilePath, backup)
			state.snapshot.Status = "Subscription update failed: could not save source: " +
				err.Error()
			if rollbackErr != nil {
				state.snapshot.Status += "; profile rollback failed: " + rollbackErr.Error()
			}
			return
		}

		if isActive {
			if m.service != nil {
				backup.release()
			}
			if reloadErr := reloadTUIOperationConfigExpected(
				state,
				m.service,
				m.client,
				m.ownsCore,
				backup.updatedSHA256,
			); reloadErr != nil {
				var rollbackErr error
				if backup.lock != nil {
					rollbackErr = restoreTUISubscriptionProfile(profilePath, backup)
				} else {
					rollbackErr = restoreTUIProfileIfUnchanged(
						state.paths.homeDir,
						profilePath,
						backup,
					)
				}
				state.snapshot.Status = "Subscription refresh failed to hot-reload: " +
					reloadErr.Error()
				switch {
				case rollbackErr != nil:
					state.snapshot.Status += "; profile rollback failed: " +
						rollbackErr.Error()
				default:
					originalReloadErr := reloadTUIOperationConfigExpected(
						state,
						m.service,
						m.client,
						m.ownsCore,
						tuiBytesSHA256(backup.data),
					)
					if originalReloadErr == nil {
						state.snapshot.Status += "; original profile restored"
					} else {
						state.snapshot.Status += "; file restored but Core reload failed: " +
							originalReloadErr.Error()
					}
				}
				return
			}
			state.snapshot.Status = "Subscription refreshed and hot-reloaded: " +
				filepath.Base(profilePath)
			syncStoppedTUISettings(state)
		} else {
			state.snapshot.Status = "Subscription refreshed: " + filepath.Base(profilePath)
		}
		refreshTUIProfiles(&state.snapshot, state.paths)
		state.snapshot.SelectedRow = findTUIProfile(
			state.snapshot.Profiles,
			profilePath,
		)
		state.profileSelection = profilePath
	})
}

func (m *tuiModel) handleInput(message tea.KeyMsg) tea.Cmd {
	switch message.Type {
	case tea.KeyCtrlC:
		return tea.Quit
	case tea.KeyEsc:
		m.resetInput()
		m.snapshot.Status = "Input cancelled"
		return nil
	case tea.KeyEnter:
		return m.submitInput()
	case tea.KeyBackspace, tea.KeyCtrlH:
		if m.inputSelectAll {
			m.clearInputSelection()
			return nil
		}
		if m.inputCursor > 0 {
			m.inputValue = append(
				m.inputValue[:m.inputCursor-1],
				m.inputValue[m.inputCursor:]...,
			)
			m.inputCursor--
		}
		return nil
	case tea.KeyDelete:
		if m.inputSelectAll {
			m.clearInputSelection()
			return nil
		}
		if m.inputCursor < len(m.inputValue) {
			m.inputValue = append(
				m.inputValue[:m.inputCursor],
				m.inputValue[m.inputCursor+1:]...,
			)
		}
		return nil
	case tea.KeyLeft:
		if m.inputSelectAll {
			m.inputCursor = 0
			m.inputSelectAll = false
		} else if m.inputCursor > 0 {
			m.inputCursor--
		}
		return nil
	case tea.KeyRight:
		if m.inputSelectAll {
			m.inputCursor = len(m.inputValue)
			m.inputSelectAll = false
		} else if m.inputCursor < len(m.inputValue) {
			m.inputCursor++
		}
		return nil
	case tea.KeyHome, tea.KeyCtrlA:
		m.inputCursor = 0
		m.inputSelectAll = false
		return nil
	case tea.KeyEnd, tea.KeyCtrlE:
		m.inputCursor = len(m.inputValue)
		m.inputSelectAll = false
		return nil
	case tea.KeyCtrlU:
		m.clearInputSelection()
		return nil
	case tea.KeyCtrlW:
		if m.inputSelectAll {
			m.clearInputSelection()
			return nil
		}
		if len(m.inputValue) > 0 {
			start := m.inputCursor
			for start > 0 && m.inputValue[start-1] == ' ' {
				start--
			}
			for start > 0 && m.inputValue[start-1] != ' ' {
				start--
			}
			m.inputValue = append(m.inputValue[:start], m.inputValue[m.inputCursor:]...)
			m.inputCursor = start
		}
		return nil
	case tea.KeyRunes:
		limit := 4096
		if m.inputMode == tuiInputMixedPort {
			limit = 5
		} else if m.inputMode == tuiInputProfileName {
			limit = 128
		}
		if m.inputSelectAll {
			m.clearInputSelection()
		}
		for _, value := range message.Runes {
			if len(m.inputValue) >= limit {
				break
			}
			if m.inputMode != tuiInputMixedPort || value >= '0' && value <= '9' {
				m.inputValue = append(m.inputValue, 0)
				copy(m.inputValue[m.inputCursor+1:], m.inputValue[m.inputCursor:])
				m.inputValue[m.inputCursor] = value
				m.inputCursor++
			}
		}
	}
	return nil
}

func (m *tuiModel) clearInputSelection() {
	m.inputValue = m.inputValue[:0]
	m.inputCursor = 0
	m.inputSelectAll = false
}

func (m *tuiModel) resetInput() {
	m.inputMode = tuiInputNone
	m.clearInputSelection()
	m.renameProfilePath = ""
}

func (m *tuiModel) submitInput() tea.Cmd {
	value := strings.TrimSpace(string(m.inputValue))
	mode := m.inputMode
	renameProfilePath := m.renameProfilePath
	m.resetInput()
	switch mode {
	case tuiInputMixedPort:
		port, err := strconv.Atoi(value)
		if err != nil || port < 0 || port > 65535 {
			m.snapshot.Status = "Port change failed: mixed port must be a number from 0 to 65535"
			return nil
		}
		if port == m.snapshot.Settings.MixedPort {
			m.snapshot.Status = "Port unchanged"
			return nil
		}
		if m.ownsCore && !m.coreRunning {
			m.pendingMixedPort = &port
			m.snapshot.Settings.MixedPort = port
			m.stagedSettings = cloneTUISettings(&m.snapshot.Settings)
			m.settingsDirty = true
			m.snapshot.Status = fmt.Sprintf(
				"Mixed port %d staged; enable System proxy or start Core to apply",
				port,
			)
			m.persistStagedTUISettings()
			return nil
		}
		return m.startOperation(func(state *tuiOperationState) {
			if m.service != nil {
				settings := state.snapshot.Settings
				settings.MixedPort = port
				commitTUIOperationSettings(state, m.service, m.client, settings)
				return
			}
			if applyTUIMixedPort(&state.snapshot, m.client, port) {
				persistTUIOperationSettings(state)
			}
		})
	case tuiInputSubscription:
		if value == "" {
			m.snapshot.Status = "Profile download cancelled"
			return nil
		}
		if _, err := newTUISubscriptionRequest(value); err != nil {
			m.snapshot.Status = "Add profile failed: subscription URL must use http or https"
			return nil
		}
		return m.startOperation(func(state *tuiOperationState) {
			path, err := downloadTUIProfile(state.paths.homeDir, value)
			if err != nil {
				state.snapshot.Status = "Add profile failed: " + err.Error()
				return
			}
			if err := rememberTUISubscriptionSource(
				state.paths.homeDir,
				path,
				value,
			); err != nil {
				_ = os.Remove(path)
				state.snapshot.Status = "Add profile failed: could not save subscription source: " +
					err.Error()
				return
			}
			state.snapshot.Status = "Subscription profile linked; U refreshes from the saved URL"
			refreshTUIProfiles(&state.snapshot, state.paths)
			state.snapshot.SelectedRow = findTUIProfile(state.snapshot.Profiles, path)
			state.profileSelection = path
		})
	case tuiInputProfileName:
		if value == "" {
			m.snapshot.Status = "Profile rename cancelled"
			return nil
		}
		return m.startOperation(func(state *tuiOperationState) {
			newPath, err := renameTUIProfile(
				state.paths.homeDir,
				renameProfilePath,
				value,
			)
			if err != nil {
				state.snapshot.Status = "Rename failed: " + err.Error()
				return
			}
			refreshTUIProfiles(&state.snapshot, state.paths)
			state.snapshot.SelectedRow = findTUIProfile(
				state.snapshot.Profiles,
				newPath,
			)
			state.profileSelection = newPath
			state.snapshot.Status = "Renamed profile to " + filepath.Base(newPath)
		})
	}
	return nil
}

func renameTUIProfile(homeDir, sourcePath, requestedName string) (string, error) {
	sourcePath = filepath.Clean(sourcePath)
	homeDir = filepath.Clean(homeDir)
	if filepath.Dir(sourcePath) != homeDir {
		return "", errors.New("profile must be in the FlClash data directory")
	}
	name := strings.TrimSpace(requestedName)
	if name == "" || name == "." || name == ".." {
		return "", errors.New("profile name cannot be empty")
	}
	if filepath.Base(name) != name || strings.ContainsAny(name, `/\`) {
		return "", errors.New("profile name cannot contain a path")
	}
	for _, value := range name {
		if value < 32 || value == 127 {
			return "", errors.New("profile name contains control characters")
		}
	}
	extension := strings.ToLower(filepath.Ext(name))
	stem := strings.TrimSuffix(name, filepath.Ext(name))
	if stem == "" || stem == "." || stem == ".." {
		return "", errors.New("profile name cannot be empty")
	}
	if extension == "" {
		extension = strings.ToLower(filepath.Ext(sourcePath))
		if extension != ".yaml" && extension != ".yml" {
			extension = ".yaml"
		}
		name += extension
	} else if extension != ".yaml" && extension != ".yml" {
		return "", errors.New("profile name must end in .yaml or .yml")
	}
	destinationPath := filepath.Join(homeDir, name)
	if destinationPath == sourcePath {
		return sourcePath, nil
	}
	lease, err := acquireTUIProfileLocks(homeDir, sourcePath, destinationPath)
	if err != nil {
		return "", err
	}
	defer lease.release()
	if _, err := os.Lstat(destinationPath); err == nil {
		return "", fmt.Errorf("%s already exists", name)
	} else if !os.IsNotExist(err) {
		return "", err
	}
	if err := os.Rename(sourcePath, destinationPath); err != nil {
		return "", err
	}
	if err := renameTUISubscriptionSource(homeDir, sourcePath, destinationPath); err != nil {
		if rollbackErr := os.Rename(destinationPath, sourcePath); rollbackErr != nil {
			return "", fmt.Errorf(
				"save renamed subscription source: %v; file rollback failed: %w",
				err,
				rollbackErr,
			)
		}
		return "", fmt.Errorf("save renamed subscription source: %w", err)
	}
	return destinationPath, nil
}

func applyTUIMixedPort(snapshot *tuiSnapshot, client controllerClient, selectedPort int) bool {
	systemProxyEnabled := snapshot.Settings.SystemProxy
	if err := client.patchConfig(map[string]interface{}{"mixed-port": selectedPort}); err != nil {
		snapshot.Status = "Port change failed: " + err.Error()
		return false
	}
	refreshTUISnapshot(snapshot, client)
	if systemProxyEnabled {
		enableSystemProxy := snapshot.Settings.MixedPort > 0
		if err := setLinuxSystemProxy(snapshot.Settings.MixedPort, enableSystemProxy); err != nil {
			snapshot.Status = "Port changed, but system proxy update failed: " + err.Error()
			return true
		}
		snapshot.Settings.SystemProxy = enableSystemProxy
		if !enableSystemProxy {
			snapshot.Status = "Mixed port disabled; system proxy disabled"
			return true
		}
	}
	snapshot.Status = fmt.Sprintf("Mixed port changed to %d", snapshot.Settings.MixedPort)
	return true
}

func downloadTUIProfile(homeDir, value string) (string, error) {
	data, err := fetchTUISubscription(value)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		return "", err
	}
	path := filepath.Join(homeDir, fmt.Sprintf("profile-%d.yaml", time.Now().UnixNano()))
	if err := writeTUIProfileAtomically(path, data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func fetchTUISubscription(value string) ([]byte, error) {
	request, err := newTUISubscriptionRequest(value)
	if err != nil {
		return nil, err
	}
	client := &nethttp.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("subscription returned %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, tuiSubscriptionMaxBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, errors.New("subscription response is empty")
	}
	if len(data) > tuiSubscriptionMaxBytes {
		return nil, fmt.Errorf(
			"subscription response exceeds %d MiB",
			tuiSubscriptionMaxBytes>>20,
		)
	}
	if message := validateConfigBytes(data); message != "" {
		return nil, fmt.Errorf("downloaded profile is invalid: %s", message)
	}
	return data, nil
}

func writeTUIProfileAtomically(path string, data []byte, mode os.FileMode) error {
	temp, err := os.CreateTemp(
		filepath.Dir(path),
		"."+filepath.Base(path)+".tmp-*",
	)
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(mode.Perm()); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}

type tuiProfileBackup struct {
	data          []byte
	mode          os.FileMode
	updatedSHA256 string
	lock          *tuiProfileLockLease
}

func (b *tuiProfileBackup) release() {
	if b == nil || b.lock == nil {
		return
	}
	b.lock.release()
	b.lock = nil
}

func updateTUISubscriptionProfile(
	homeDir,
	path,
	sourceURL string,
) (tuiProfileBackup, error) {
	if _, err := tuiProfileStateKey(homeDir, path); err != nil {
		return tuiProfileBackup{}, err
	}
	lease, err := acquireTUIProfileLocks(homeDir, path)
	if err != nil {
		return tuiProfileBackup{}, err
	}
	releaseOnError := true
	defer func() {
		if releaseOnError {
			lease.release()
		}
	}()
	info, err := os.Lstat(path)
	if err != nil {
		return tuiProfileBackup{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return tuiProfileBackup{}, errors.New("profile must be a regular file")
	}
	previous, err := os.ReadFile(path)
	if err != nil {
		return tuiProfileBackup{}, err
	}
	previousSettings := loadTUIConfiguredSettings(path, true)
	updated, err := fetchTUISubscription(sourceURL)
	if err != nil {
		return tuiProfileBackup{}, err
	}
	if err := writeTUIProfileAtomically(path, updated, info.Mode()); err != nil {
		return tuiProfileBackup{}, err
	}
	backup := tuiProfileBackup{data: previous, mode: info.Mode(), lock: lease}
	if previousSettings != nil {
		if err := persistTUISettings(path, *previousSettings); err != nil {
			rollbackErr := restoreTUISubscriptionProfile(path, backup)
			if rollbackErr != nil {
				return tuiProfileBackup{}, fmt.Errorf(
					"preserve local settings: %v; profile rollback failed: %w",
					err,
					rollbackErr,
				)
			}
			return tuiProfileBackup{}, fmt.Errorf("preserve local settings: %w", err)
		}
	}
	updatedData, err := os.ReadFile(path)
	if err != nil {
		_ = restoreTUISubscriptionProfile(path, backup)
		return tuiProfileBackup{}, err
	}
	backup.updatedSHA256 = tuiBytesSHA256(updatedData)
	releaseOnError = false
	return backup, nil
}

func restoreTUISubscriptionProfile(path string, backup tuiProfileBackup) error {
	return writeTUIProfileAtomically(path, backup.data, backup.mode)
}

func restoreTUIProfileIfUnchanged(
	homeDir,
	path string,
	backup tuiProfileBackup,
) error {
	lease, err := acquireTUIProfileLocks(homeDir, path)
	if err != nil {
		return err
	}
	defer lease.release()
	actualSHA256, err := tuiFileSHA256(path)
	if err != nil {
		return err
	}
	if backup.updatedSHA256 == "" || actualSHA256 != backup.updatedSHA256 {
		return errors.New("profile changed concurrently")
	}
	return restoreTUISubscriptionProfile(path, backup)
}

func newTUISubscriptionRequest(value string) (*nethttp.Request, error) {
	request, err := nethttp.NewRequest(nethttp.MethodGet, value, nil)
	if err != nil ||
		(request.URL.Scheme != "http" && request.URL.Scheme != "https") {
		return nil, errors.New("subscription URL must use http or https")
	}
	request.Header.Set("User-Agent", tuiSubscriptionUserAgent)
	request.Header.Set("Accept", "application/yaml, text/yaml, text/plain, */*")
	return request, nil
}

func tuiKeyFromTea(message tea.KeyMsg) (tuiKey, bool) {
	switch message.String() {
	case "q", "Q":
		return tuiKeyQuit, true
	case "ctrl+c":
		return tuiKeyInterrupt, true
	case "r":
		return tuiKeyRefresh, true
	case "R":
		return tuiKeyReload, true
	case "?":
		return tuiKeyHelp, true
	case "tab":
		return tuiKeyFocusNext, true
	case "shift+tab":
		return tuiKeyFocusPrevious, true
	case "up", "w":
		return tuiKeyUp, true
	case "down", "s":
		return tuiKeyDown, true
	case "pgup":
		return tuiKeyPageUp, true
	case "pgdown":
		return tuiKeyPageDown, true
	case "left":
		return tuiKeyLeft, true
	case "right":
		return tuiKeyRight, true
	case "esc":
		return tuiKeyBack, true
	case "[":
		return tuiKeyViewPrevious, true
	case "]":
		return tuiKeyViewNext, true
	case "D":
		return tuiKeyDelayTest, true
	case "A":
		return tuiKeyDelayTestAll, true
	case "enter", " ":
		return tuiKeySelect, true
	case "1":
		return tuiKeyDashboard, true
	case "2":
		return tuiKeyProxies, true
	case "3":
		return tuiKeyProfiles, true
	case "4":
		return tuiKeyRequests, true
	case "5":
		return tuiKeyConnections, true
	case "6":
		return tuiKeyLogs, true
	case "7":
		return tuiKeyTools, true
	case "8":
		return tuiKeyMaintenance, true
	case "P":
		return tuiKeyProviders, true
	case "x", "X":
		return tuiKeyCloseConnections, true
	case "a":
		return tuiKeyAllowLAN, true
	case "v":
		return tuiKeyIPv6, true
	case "t":
		return tuiKeyTun, true
	case "m":
		return tuiKeyMode, true
	case "i":
		return tuiKeyLogLevel, true
	case "+", "=":
		return tuiKeyPortUp, true
	case "-":
		return tuiKeyPortDown, true
	case "p":
		return tuiKeySetPort, true
	case "S":
		return tuiKeySystemProxy, true
	case "c":
		return tuiKeyCoreToggle, true
	case "e":
		return tuiKeyEdit, true
	case "n":
		return tuiKeyNewProfile, true
	case "f2", "u":
		return tuiKeyRenameProfile, true
	case "U":
		return tuiKeyUpdateProfile, true
	case "d":
		return tuiKeyCloseConnection, true
	case "b":
		return tuiKeyBackup, true
	case "B":
		return tuiKeyRestore, true
	case "g":
		return tuiKeyGeoUpdate, true
	case "z":
		return tuiKeyResetTraffic, true
	default:
		return 0, false
	}
}
