//go:build linux && !cgo && cli

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	tuiServiceErrorConflict       = "revision_conflict"
	tuiServiceErrorInvalidRequest = "invalid_request"
	tuiServiceErrorUnsupported    = "unsupported_protocol"
	tuiServiceErrorOperation      = "operation_failed"
	tuiServiceErrorShuttingDown   = "shutting_down"
	tuiServiceDedupLimit          = 256
)

var (
	startTUIServiceCoreListeners = func() bool { return cliHub.StartListener() }
	stopTUIServiceCoreListeners  = func() bool { return cliHub.StopListener() }
	waitTUIServiceProxyPortState = waitForTUIProxyPortState
)

type tuiServiceRuntime struct {
	mu                      sync.RWMutex
	mutationMu              sync.Mutex
	paths                   cliPaths
	testURL                 string
	coreSocket              string
	coreController          controllerClient
	setupParams             []byte
	running                 bool
	shuttingDown            bool
	systemProxy             bool
	proxyPort               int
	trafficMode             string
	configuredPort          int
	runtimePort             int
	activePort              int
	tunScope                string
	tunEnabled              bool
	tunLease                *tuiTunLease
	actualConfigPath        string
	flc                     tuiFLCListenerState
	history                 []tuiRequest
	historyUpdateMu         sync.Mutex
	historyVersion          uint64
	persistedHistoryVersion uint64
	historyPersistMu        sync.Mutex
	revision                uint64
	changed                 chan struct{}
	dedup                   map[string]tuiServiceStatus
	dedupOrder              []string
	shutdown                func()
	routeClient             func(string) (*http.Client, func(), error)
	routeSpeedTest          func(context.Context, *http.Client) (tuiSpeedResult, error)
	routeDelayTest          func(context.Context, *http.Client, string) (tuiDelayResult, error)
}

func newTUIServiceRuntime(
	paths cliPaths,
	testURL,
	coreSocket string,
	setupParams []byte,
	shutdown func(),
) *tuiServiceRuntime {
	controllerOptions := controllerOptions{unixSocket: coreSocket}
	return &tuiServiceRuntime{
		paths:      paths,
		testURL:    testURL,
		coreSocket: coreSocket,
		coreController: controllerClient{
			options: controllerOptions,
			client: controllerHTTPClientForOptions(
				controllerOptions,
				controllerRequestTimeout,
			),
		},
		setupParams:      append([]byte(nil), setupParams...),
		trafficMode:      tuiSilentMode,
		tunScope:         tuiTunScopeUser,
		actualConfigPath: paths.ConfigPath,
		revision:         1,
		changed:          make(chan struct{}),
		dedup:            map[string]tuiServiceStatus{},
		shutdown:         shutdown,
		routeClient:      newTUIRouteHTTPClient,
		routeSpeedTest:   runTUIDownloadSpeedTest,
		routeDelayTest:   runTUIRouteDelayTest,
	}
}

func (r *tuiServiceRuntime) closeCoreController() {
	r.coreController.closeIdleConnections()
}

func (r *tuiServiceRuntime) handle(
	request tuiServiceRequest,
) tuiServiceStatus {
	if request.ProtocolVersion != 0 &&
		request.ProtocolVersion != tuiServiceProtocolVersion {
		status := r.snapshot(request.RequestID)
		return failTUIServiceStatus(
			status,
			tuiServiceErrorUnsupported,
			fmt.Sprintf(
				"unsupported service protocol %d; expected %d",
				request.ProtocolVersion,
				tuiServiceProtocolVersion,
			),
		)
	}
	if request.RequestID != "" {
		if status, ok := r.cached(request.RequestID); ok {
			return status
		}
	}

	var status tuiServiceStatus
	switch request.Action {
	case "status":
		status = r.snapshot(request.RequestID)
	case "history":
		status = r.historyStatus(request.RequestID)
	case "connections":
		status = r.connectionsStatus(request.RequestID)
	case "logs":
		status = r.snapshot(request.RequestID)
		status.Logs = readTUIPersistentLogs(r.paths.HomeDir, request.LogLimit)
	case "watch":
		status = r.watch(request)
	case "speed_proxy":
		status = r.testProxySpeed(request)
	case "speed_route", "delay_route":
		status = r.testRoute(request)
	case "start", "stop", "reload", "apply_settings", "set_system_proxy", "set_tun", "flc_proxy",
		"close_connection", "close_all_connections",
		"set_mode", "set_flc_outbound", "select_proxy", "clear_history", "clear_logs", "put_profile",
		"rename_profile", "delete_profile", "link_profile", "backup_profile",
		"restore_profile", "shutdown":
		status = r.mutate(request)
	default:
		status = failTUIServiceStatus(
			r.snapshot(request.RequestID),
			tuiServiceErrorInvalidRequest,
			"unknown service action "+strconv.Quote(request.Action),
		)
	}
	if request.RequestID != "" && request.Action != "watch" {
		r.remember(request.RequestID, status)
	}
	return status
}

func (r *tuiServiceRuntime) snapshot(requestID string) tuiServiceStatus {
	r.mu.RLock()
	status := tuiServiceStatus{
		ProtocolVersion:     tuiServiceProtocolVersion,
		RequestID:           requestID,
		Revision:            r.revision,
		OK:                  true,
		PID:                 os.Getpid(),
		Version:             cliVersion,
		HomeDir:             r.paths.HomeDir,
		ConfigPath:          r.paths.ConfigPath,
		CoreSocket:          r.coreSocket,
		Running:             r.running,
		ShuttingDown:        r.shuttingDown,
		SystemProxy:         r.systemProxy,
		Mode:                r.trafficMode,
		ProxyPort:           r.configuredPort,
		ConfiguredProxyPort: r.configuredPort,
		ActiveProxyPort:     r.activePort,
		FLCEnabled:          r.running && r.trafficMode == tuiSilentMode && r.flc.Port > 0,
		FLCOutbound:         r.flc.Outbound,
		TunScope:            r.tunScope,
	}
	if r.tunEnabled {
		status.TunState = "on"
		if r.tunLease != nil {
			status.TunOwnerUID = uint32(os.Getuid())
			status.TunOwnerPID = os.Getpid()
		}
	} else {
		status.TunState = "off"
	}
	if r.activePort > 0 {
		status.ProxyPort = r.activePort
	}
	r.mu.RUnlock()
	if frontends, err := listCLIFrontends(); err == nil {
		status.FrontendCount = len(frontends)
	}
	return status
}

func (r *tuiServiceRuntime) watch(request tuiServiceRequest) tuiServiceStatus {
	r.mu.RLock()
	if r.revision > request.AfterRevision {
		r.mu.RUnlock()
		return r.snapshot(request.RequestID)
	}
	changed := r.changed
	r.mu.RUnlock()

	timeout := time.Duration(request.WatchTimeoutMS) * time.Millisecond
	if timeout <= 0 || timeout > 30*time.Second {
		timeout = 30 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-changed:
	case <-timer.C:
	}
	return r.snapshot(request.RequestID)
}

func (r *tuiServiceRuntime) mutate(
	request tuiServiceRequest,
) tuiServiceStatus {
	r.mutationMu.Lock()
	if request.RequestID != "" {
		if status, ok := r.cached(request.RequestID); ok {
			r.mutationMu.Unlock()
			return status
		}
	}
	status := r.snapshot(request.RequestID)
	if status.ShuttingDown {
		if request.Action == "shutdown" {
			return r.completeMutation(request, status)
		}
		return r.completeMutation(request, failTUIServiceStatus(
			status,
			tuiServiceErrorShuttingDown,
			"backend is shutting down",
		))
	}
	if request.ExpectedRevision != nil &&
		*request.ExpectedRevision != status.Revision {
		return r.completeMutation(request, failTUIServiceStatus(
			status,
			tuiServiceErrorConflict,
			fmt.Sprintf(
				"backend changed from revision %d to %d; refresh and retry",
				*request.ExpectedRevision,
				status.Revision,
			),
		))
	}

	changed := false
	resultPath := ""
	var err error
	switch request.Action {
	case "start":
		changed, err = r.startCoreListeners()
	case "stop":
		changed, err = r.stopCoreAndProxy(status)
	case "reload":
		changed, err = r.reloadExpected(
			request.ConfigPath,
			request.ExpectedSHA256,
		)
	case "apply_settings":
		if request.Settings == nil {
			err = errors.New("settings payload is required")
		} else {
			changed, err = r.applySettings(*request.Settings)
		}
	case "set_system_proxy":
		if request.Enabled == nil {
			err = errors.New("enabled must be explicitly true or false")
		} else {
			changed, err = r.applySystemProxy(*request.Enabled)
		}
	case "set_tun":
		if request.Enabled == nil {
			err = errors.New("enabled must be explicitly true or false")
		} else {
			changed, err = r.applyTun(*request.Enabled, request.TunScope)
		}
	case "set_mode":
		changed, err = r.applyTrafficMode(request.Mode)
	case "set_flc_outbound":
		changed, err = r.applyFLCOutbound(request.ProxyName)
	case "flc_proxy":
		changed, err = r.ensureFLCProxy()
	case "select_proxy":
		changed, err = r.selectProxy(request.ProxyGroup, request.ProxyName)
	case "clear_history":
		changed, err = r.clearPersistentHistory()
	case "clear_logs":
		err = clearTUIPersistentLogs(r.paths.HomeDir)
		changed = err == nil
	case "close_connection":
		if strings.TrimSpace(request.ConnectionID) == "" {
			err = errors.New("connection ID is required")
		} else {
			err = closeTUIVisibleConnections(r.coreController, uint32(os.Getuid()), status.TunState == "on" && status.TunScope == tuiTunScopeSystem, request.ConnectionID)
			changed = err == nil
		}
	case "close_all_connections":
		err = closeTUIVisibleConnections(r.coreController, uint32(os.Getuid()), status.TunState == "on" && status.TunScope == tuiTunScopeSystem, "")
		changed = err == nil
	case "put_profile":
		changed, resultPath, err = r.putProfile(request)
	case "rename_profile":
		changed, resultPath, err = r.renameProfile(request.ConfigPath, request.NewName)
	case "delete_profile":
		changed, err = r.deleteProfile(request.ConfigPath)
	case "link_profile":
		changed, err = r.linkProfile(request.ConfigPath, request.SubscriptionURL)
	case "backup_profile":
		changed, resultPath, err = r.backupProfile(request.ConfigPath)
	case "restore_profile":
		changed, resultPath, err = r.restoreProfile(request.ConfigPath)
	case "shutdown":
		changed, err = r.stopCoreAndProxy(status)
		if err == nil {
			r.setShuttingDown(true)
			changed = true
		}
	}
	if err != nil {
		r.logMutation(request, false, err)
		return r.completeMutation(request, failTUIServiceStatus(
			r.snapshot(request.RequestID),
			tuiServiceErrorOperation,
			err.Error(),
		))
	}
	if changed {
		r.bumpRevision()
	}
	status = r.snapshot(request.RequestID)
	if request.Action == "flc_proxy" {
		r.mu.RLock()
		status.FLCProxyURL = r.flc.proxyURL()
		r.mu.RUnlock()
	}
	status.ResultPath = resultPath
	r.logMutation(request, true, nil)
	return r.completeMutation(request, status)
}

func (r *tuiServiceRuntime) logMutation(
	request tuiServiceRequest,
	succeeded bool,
	err error,
) {
	r.mu.RLock()
	homeDir := r.paths.HomeDir
	r.mu.RUnlock()
	level := "INFO"
	result := "succeeded"
	if !succeeded {
		level = "ERROR"
		result = "failed"
	}
	detail := result
	if request.ConfigPath != "" {
		detail += " profile=" + filepath.Base(request.ConfigPath)
	}
	if request.Mode != "" {
		detail += " mode=" + request.Mode
	}
	if request.NewName != "" {
		detail += " name=" + filepath.Base(request.NewName)
	}
	if err != nil {
		detail += " error=" + err.Error()
	}
	appendCLIApplicationLog(homeDir, level, request.Action, detail)
}

func (r *tuiServiceRuntime) signalShutdown() {
	if r.shutdown != nil {
		r.shutdown()
	}
}

func (r *tuiServiceRuntime) configureRuntimePolicy(
	mode string,
	configuredPort int,
	actualConfigPath string,
	flc tuiFLCListenerState,
) {
	r.mu.Lock()
	r.trafficMode = mode
	r.configuredPort = configuredPort
	r.runtimePort = configuredPort
	r.actualConfigPath = actualConfigPath
	r.flc = flc
	r.mu.Unlock()
}

func (r *tuiServiceRuntime) configureManagedRuntimePolicy(
	mode string,
	configuredPort int,
	runtimePort int,
	actualConfigPath string,
	flc tuiFLCListenerState,
	tunScope string,
	tunEnabled bool,
) {
	r.configureRuntimePolicy(mode, configuredPort, actualConfigPath, flc)
	r.mu.Lock()
	r.runtimePort = runtimePort
	r.tunScope = tunScope
	r.tunEnabled = tunEnabled
	r.mu.Unlock()
}

func tuiRuntimeProxyPort(
	mode string,
	configuredPort int,
	flc tuiFLCListenerState,
) (int, error) {
	if mode != tuiSilentMode {
		return configuredPort, nil
	}
	if flc.proxyURL() == "" {
		return 0, errors.New(
			"silent mode has no flc group yet; select a node in Proxies, or run `flclash proxy select GROUP NODE`",
		)
	}
	return flc.Port, nil
}

func (r *tuiServiceRuntime) startCoreListeners() (bool, error) {
	if _, err := r.repairFLCOutbound(); err != nil {
		return false, err
	}
	r.mu.RLock()
	if r.running {
		r.mu.RUnlock()
		return false, nil
	}
	mode := r.trafficMode
	runtimePort := r.runtimePort
	flc := r.flc
	tunEnabled := r.tunEnabled
	tunLease := r.tunLease
	tunScope := r.tunScope
	systemProxy := r.systemProxy
	systemProxyPort := r.proxyPort
	r.mu.RUnlock()
	if tunEnabled && tunLease == nil {
		lease, _, leaseErr := acquireTUITunLease(tunScope)
		if leaseErr != nil {
			return false, leaseErr
		}
		r.mu.Lock()
		r.tunLease = lease
		r.mu.Unlock()
		if _, reloadErr := r.reloadUnlocked("", ""); reloadErr != nil {
			r.releaseTunLease()
			return false, fmt.Errorf("prepare TUN runtime: %w", reloadErr)
		}
		r.mu.RLock()
		runtimePort = r.runtimePort
		r.mu.RUnlock()
		defer func() {
			r.mu.RLock()
			running := r.running
			r.mu.RUnlock()
			if !running {
				r.releaseTunLease()
			}
		}()
	}

	port, err := tuiRuntimeProxyPort(mode, runtimePort, flc)
	if err != nil {
		return false, err
	}
	if port > 0 {
		if err := ensureTUIProxyPortFree(port); err != nil {
			if mode == tuiSilentMode {
				return false, err
			}
			if _, reloadErr := r.reloadUnlocked("", ""); reloadErr != nil {
				return false, fmt.Errorf("reallocate occupied proxy port: %w", reloadErr)
			}
			r.mu.RLock()
			port = r.runtimePort
			r.mu.RUnlock()
			if err := ensureTUIProxyPortFree(port); err != nil {
				return false, err
			}
		}
	}
	if !startTUIServiceCoreListeners() {
		return false, errors.New("start proxy listeners failed")
	}
	if port > 0 && !waitTUIServiceProxyPortState(
		port,
		true,
		tuiListenerValidationTimeout,
	) {
		return false, r.rollbackStartedCore(
			port,
			fmt.Errorf(
				"proxy listener on 127.0.0.1:%d did not become ready",
				port,
			),
		)
	}
	if systemProxy && systemProxyPort != port {
		if err := setLinuxSystemProxy(port, true); err != nil {
			return false, r.rollbackStartedCore(
				port,
				fmt.Errorf("update System proxy to active port: %w", err),
			)
		}
		r.setSystemProxyState(true, port)
	}
	r.mu.Lock()
	r.running = true
	r.activePort = port
	r.mu.Unlock()
	return true, nil
}

func (r *tuiServiceRuntime) rollbackStartedCore(port int, cause error) error {
	stopped := stopTUIServiceCoreListeners()
	if stopped && waitTUIServiceProxyPortState(
		port,
		false,
		tuiListenerValidationTimeout,
	) {
		return fmt.Errorf("%w; Core listeners stopped", cause)
	}
	r.mu.Lock()
	r.running = true
	r.activePort = port
	r.mu.Unlock()
	return fmt.Errorf(
		"%v; Core listener cleanup failed and the Backend still marks Core as running",
		cause,
	)
}

func (r *tuiServiceRuntime) flcProxy(requestID string) tuiServiceStatus {
	status := r.snapshot(requestID)
	if !status.Running {
		return failTUIServiceStatus(
			status,
			tuiServiceErrorOperation,
			"FlClash Core is stopped; run `flclash core start` first",
		)
	}
	if status.Mode != tuiSilentMode {
		return failTUIServiceStatus(
			status,
			tuiServiceErrorOperation,
			"private FLC listener is only active in silent mode",
		)
	}
	r.mu.RLock()
	proxyURL := r.flc.proxyURL()
	r.mu.RUnlock()
	if proxyURL == "" {
		return failTUIServiceStatus(
			status,
			tuiServiceErrorOperation,
			"private FLC listener is unavailable",
		)
	}
	status.FLCProxyURL = proxyURL
	return status
}

func (r *tuiServiceRuntime) ensureFLCProxy() (bool, error) {
	status := r.snapshot("")
	if status.Mode != tuiSilentMode {
		return false, errors.New("private FLC listener is only active in silent mode")
	}
	changed, err := r.repairFLCOutbound()
	if err != nil {
		return false, err
	}
	if !status.Running {
		started, err := r.startCoreListeners()
		if err != nil {
			return changed, err
		}
		changed = changed || started
	}
	return changed, nil
}

func (r *tuiServiceRuntime) repairFLCOutbound() (bool, error) {
	r.mu.RLock()
	mode := r.trafficMode
	outbound := strings.TrimSpace(r.flc.Outbound)
	incomplete := r.flc.proxyURL() == ""
	configPath := r.paths.ConfigPath
	r.mu.RUnlock()
	if mode != tuiSilentMode {
		return false, nil
	}
	if outbound != "" {
		if err := validateTUIFLCOutbound(r.coreController, outbound); err == nil {
			if !incomplete {
				return false, nil
			}
			changed, err := r.applyFLCOutbound(outbound)
			if err != nil {
				return false, fmt.Errorf("restore FLC outbound %q: %w", outbound, err)
			}
			return changed, nil
		}
	}
	replacement, err := chooseDefaultTUIFLCOutbound(
		r.coreController,
		configPath,
	)
	if err != nil {
		if outbound == "" {
			return false, err
		}
		return false, fmt.Errorf(
			"saved FLC outbound %q is unavailable and no replacement was found: %w",
			outbound,
			err,
		)
	}
	changed, err := r.applyFLCOutbound(replacement)
	if err != nil {
		if outbound == "" {
			return false, fmt.Errorf("auto-select FLC outbound %q: %w", replacement, err)
		}
		return false, fmt.Errorf(
			"replace unavailable FLC outbound %q with %q: %w",
			outbound,
			replacement,
			err,
		)
	}
	return changed, nil
}

func chooseDefaultTUIFLCOutbound(controller controllerClient, configPath string) (string, error) {
	data, err := controller.request(http.MethodGet, "/proxies", nil)
	if err != nil {
		return "", fmt.Errorf("read proxy list for FLC auto-selection: %w", err)
	}
	var response tuiProxyResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return "", fmt.Errorf("parse proxy list for FLC auto-selection: %w", err)
	}
	usable := func(name string) bool {
		proxy, ok := response.Proxies[name]
		return ok && len(proxy.All) > 0 && isTUIGroup(proxy.Type)
	}
	for _, name := range loadTUIProxyGroupOrder(configPath) {
		if usable(name) {
			return name, nil
		}
	}
	for _, preferred := range []string{"PROXY", "GLOBAL"} {
		if usable(preferred) {
			return preferred, nil
		}
	}
	names := make([]string, 0, len(response.Proxies))
	for name := range response.Proxies {
		if usable(name) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	if len(names) > 0 {
		return names[0], nil
	}
	return "", errors.New(
		"silent mode has no usable proxy group; import a Profile, then select a node in Proxies",
	)
}
