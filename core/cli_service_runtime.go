//go:build linux && !cgo && cli

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

const (
	tuiServiceErrorConflict       = "revision_conflict"
	tuiServiceErrorInvalidRequest = "invalid_request"
	tuiServiceErrorUnsupported    = "unsupported_protocol"
	tuiServiceErrorOperation      = "operation_failed"
	tuiServiceDedupLimit          = 256
)

type tuiServiceRuntime struct {
	mu          sync.RWMutex
	mutationMu  sync.Mutex
	paths       cliPaths
	testURL     string
	coreSocket  string
	setupParams []byte
	running     bool
	systemProxy bool
	proxyPort   int
	revision    uint64
	changed     chan struct{}
	dedup       map[string]tuiServiceStatus
	dedupOrder  []string
	shutdown    func()
}

func newTUIServiceRuntime(
	paths cliPaths,
	testURL,
	coreSocket string,
	setupParams []byte,
	shutdown func(),
) *tuiServiceRuntime {
	return &tuiServiceRuntime{
		paths:       paths,
		testURL:     testURL,
		coreSocket:  coreSocket,
		setupParams: append([]byte(nil), setupParams...),
		revision:    1,
		changed:     make(chan struct{}),
		dedup:       map[string]tuiServiceStatus{},
		shutdown:    shutdown,
	}
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
	case "watch":
		status = r.watch(request)
	case "speed_proxy":
		status = r.testProxySpeed(request)
	case "speed_route", "delay_route":
		status = r.testRoute(request)
	case "start", "stop", "reload", "apply_settings", "set_system_proxy", "shutdown":
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
		ProtocolVersion: tuiServiceProtocolVersion,
		RequestID:       requestID,
		Revision:        r.revision,
		OK:              true,
		PID:             os.Getpid(),
		Version:         cliVersion,
		HomeDir:         r.paths.homeDir,
		ConfigPath:      r.paths.configPath,
		CoreSocket:      r.coreSocket,
		Running:         r.running,
		SystemProxy:     r.systemProxy,
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
	var err error
	switch request.Action {
	case "start":
		if !status.Running {
			if !handleStartListener() {
				err = errors.New("start proxy listeners failed")
			} else {
				r.setRunning(true)
				changed = true
			}
		}
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
	case "shutdown":
		_, err = r.stopCoreAndProxy(status)
		r.setRunning(false)
		changed = true
	}
	if err != nil {
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
	if request.Action == "shutdown" && r.shutdown != nil {
		r.shutdown()
	}
	return r.completeMutation(request, status)
}

func (r *tuiServiceRuntime) completeMutation(
	request tuiServiceRequest,
	status tuiServiceStatus,
) tuiServiceStatus {
	if request.RequestID != "" {
		r.remember(request.RequestID, status)
	}
	r.mutationMu.Unlock()
	return status
}

func (r *tuiServiceRuntime) stopCoreAndProxy(
	status tuiServiceStatus,
) (bool, error) {
	changed := false
	if status.SystemProxy {
		r.mu.RLock()
		proxyPort := r.proxyPort
		r.mu.RUnlock()
		if proxyPort <= 0 {
			settings := loadTUIConfiguredSettings(status.ConfigPath, true)
			if settings == nil {
				return false, errors.New("read active Mixed Port for system proxy cleanup")
			}
			proxyPort = settings.MixedPort
		}
		if linuxSystemProxyMatches(proxyPort) {
			if err := setLinuxSystemProxy(proxyPort, false); err != nil {
				return false, fmt.Errorf("disable managed system proxy: %w", err)
			}
		}
		r.setSystemProxyState(false, 0)
		changed = true
	}
	if status.Running {
		if !handleStopListener() {
			return changed, errors.New("stop proxy listeners failed")
		}
		r.setRunning(false)
		changed = true
	}
	return changed, nil
}

func (r *tuiServiceRuntime) reload(configPath string) (bool, error) {
	return r.reloadExpected(configPath, "")
}

func (r *tuiServiceRuntime) reloadExpected(
	configPath,
	expectedSHA256 string,
) (bool, error) {
	r.mu.RLock()
	paths := r.paths
	systemProxy := r.systemProxy
	proxyPort := r.proxyPort
	r.mu.RUnlock()
	if configPath == "" {
		configPath = paths.configPath
	}
	lease, err := acquireTUIProfileLocks(
		paths.homeDir,
		paths.configPath,
		configPath,
	)
	if err != nil {
		return false, err
	}
	defer lease.release()
	changed, err := r.reloadUnlocked(configPath, expectedSHA256)
	if err != nil || !systemProxy {
		return changed, err
	}
	settings := loadTUIConfiguredSettings(configPath, true)
	if settings == nil {
		return r.rollbackReloadProxy(
			paths.configPath,
			proxyPort,
			errors.New("reloaded profile has no usable Mixed Port"),
		)
	}
	if proxyPort > 0 && !linuxSystemProxyMatches(proxyPort) {
		r.setSystemProxyState(false, 0)
		return changed, nil
	}
	if settings.MixedPort <= 0 {
		if err := setLinuxSystemProxy(proxyPort, false); err != nil {
			return r.rollbackReloadProxy(paths.configPath, proxyPort, err)
		}
		r.setSystemProxyState(false, 0)
		return changed, nil
	}
	if settings.MixedPort == proxyPort {
		return changed, nil
	}
	if err := setLinuxSystemProxy(settings.MixedPort, true); err != nil {
		return r.rollbackReloadProxy(paths.configPath, proxyPort, err)
	}
	r.setSystemProxyState(true, settings.MixedPort)
	return changed, nil
}

func (r *tuiServiceRuntime) rollbackReloadProxy(
	previousPath string,
	proxyPort int,
	cause error,
) (bool, error) {
	r.mu.RLock()
	currentPath := r.paths.configPath
	r.mu.RUnlock()
	if filepath.Clean(currentPath) == filepath.Clean(previousPath) {
		if proxyPort > 0 {
			if proxyErr := setLinuxSystemProxy(proxyPort, true); proxyErr != nil {
				r.setSystemProxyState(false, 0)
				return false, fmt.Errorf(
					"update managed system proxy: %v; proxy rollback failed: %w",
					cause,
					proxyErr,
				)
			}
		}
		return false, fmt.Errorf(
			"update managed system proxy: %w; restore the profile content and reload",
			cause,
		)
	}
	_, rollbackErr := r.reloadUnlocked(previousPath, "")
	if rollbackErr != nil {
		return false, fmt.Errorf(
			"update managed system proxy: %v; profile rollback failed: %w",
			cause,
			rollbackErr,
		)
	}
	if proxyPort > 0 {
		if proxyErr := setLinuxSystemProxy(proxyPort, true); proxyErr != nil {
			r.setSystemProxyState(false, 0)
			return false, fmt.Errorf(
				"update managed system proxy: %v; profile restored but proxy rollback failed: %w",
				cause,
				proxyErr,
			)
		}
	}
	r.setSystemProxyState(proxyPort > 0, proxyPort)
	return false, fmt.Errorf(
		"update managed system proxy: %w; previous profile restored",
		cause,
	)
}

func (r *tuiServiceRuntime) reloadUnlocked(
	configPath,
	expectedSHA256 string,
) (bool, error) {
	r.mu.RLock()
	paths := r.paths
	setupParams := append([]byte(nil), r.setupParams...)
	running := r.running
	r.mu.RUnlock()
	if configPath == "" {
		configPath = paths.configPath
	}
	configPath = filepath.Clean(configPath)
	if expectedSHA256 != "" {
		actualSHA256, err := tuiFileSHA256(configPath)
		if err != nil {
			return false, err
		}
		if actualSHA256 != expectedSHA256 {
			return false, errors.New(
				"profile changed after editing; refresh and retry",
			)
		}
	}
	reloaded, err := reloadTUIServiceConfig(
		paths,
		configPath,
		r.testURL,
		r.coreSocket,
		setupParams,
		running,
	)
	if err != nil {
		return false, err
	}
	updatedPaths := paths
	updatedPaths.configPath = configPath
	if configPath != paths.configPath {
		if err := rememberTUIActiveProfile(updatedPaths); err != nil {
			_, rollbackErr := reloadTUIServiceConfig(
				paths,
				paths.configPath,
				r.testURL,
				r.coreSocket,
				setupParams,
				running,
			)
			if rollbackErr != nil {
				return false, fmt.Errorf(
					"remember active profile: %v; Core rollback failed: %w",
					err,
					rollbackErr,
				)
			}
			return false, fmt.Errorf("remember active profile: %w", err)
		}
	}
	r.mu.Lock()
	r.paths.configPath = configPath
	r.setupParams = append([]byte(nil), reloaded...)
	r.mu.Unlock()
	return true, nil
}

func (r *tuiServiceRuntime) applySettings(
	settings tuiSettings,
) (bool, error) {
	r.mu.RLock()
	homeDir := r.paths.homeDir
	configPath := r.paths.configPath
	systemProxy := r.systemProxy
	proxyPort := r.proxyPort
	r.mu.RUnlock()
	lease, err := acquireTUIProfileLocks(homeDir, configPath)
	if err != nil {
		return false, err
	}
	defer lease.release()
	previousSettings := loadTUIConfiguredSettings(configPath, true)
	writePath, info, original, err := readTUIWritableConfig(configPath)
	if err != nil {
		return false, err
	}
	if err := persistTUISettings(configPath, settings); err != nil {
		return false, err
	}
	if _, err := r.reloadUnlocked(configPath, ""); err == nil {
		if systemProxy && settings.MixedPort <= 0 {
			if proxyPort > 0 && linuxSystemProxyMatches(proxyPort) {
				if proxyErr := setLinuxSystemProxy(proxyPort, false); proxyErr != nil {
					return false, fmt.Errorf(
						"disable managed system proxy: %w",
						proxyErr,
					)
				}
			}
			r.setSystemProxyState(false, 0)
			return true, nil
		}
		if systemProxy &&
			(previousSettings == nil || previousSettings.MixedPort != settings.MixedPort) {
			if proxyPort > 0 && !linuxSystemProxyMatches(proxyPort) {
				r.setSystemProxyState(false, 0)
			} else if proxyErr := setLinuxSystemProxy(settings.MixedPort, true); proxyErr != nil {
				if restoreErr := writeTUIProfileAtomically(
					writePath,
					original,
					info.Mode(),
				); restoreErr != nil {
					return false, fmt.Errorf(
						"update managed system proxy: %v; rollback write failed: %w",
						proxyErr,
						restoreErr,
					)
				}
				_, reloadErr := r.reloadUnlocked(configPath, "")
				if proxyPort > 0 {
					_ = setLinuxSystemProxy(proxyPort, true)
				}
				if reloadErr != nil {
					return false, fmt.Errorf(
						"update managed system proxy: %v; rollback reload failed: %w",
						proxyErr,
						reloadErr,
					)
				}
				return false, fmt.Errorf(
					"update managed system proxy: %w; configuration restored",
					proxyErr,
				)
			} else {
				r.setSystemProxyState(true, settings.MixedPort)
			}
		}
		return true, nil
	} else {
		reloadErr := err
		if restoreErr := writeTUIProfileAtomically(
			writePath,
			original,
			info.Mode(),
		); restoreErr != nil {
			return false, fmt.Errorf(
				"apply settings: %v; rollback write failed: %w",
				reloadErr,
				restoreErr,
			)
		}
		return false, fmt.Errorf("apply settings: %w; original configuration restored", reloadErr)
	}
}

func readTUIWritableConfig(
	path string,
) (string, os.FileInfo, []byte, error) {
	writePath := path
	info, err := os.Lstat(path)
	if err != nil {
		return "", nil, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		writePath, err = filepath.EvalSymlinks(path)
		if err != nil {
			return "", nil, nil, err
		}
	}
	info, err = os.Stat(writePath)
	if err != nil {
		return "", nil, nil, err
	}
	data, err := os.ReadFile(writePath)
	if err != nil {
		return "", nil, nil, err
	}
	return writePath, info, data, nil
}

func (r *tuiServiceRuntime) applySystemProxy(enabled bool) (bool, error) {
	status := r.snapshot("")
	if status.SystemProxy == enabled {
		return false, nil
	}
	if enabled && !status.Running {
		return false, errors.New("start the Core before enabling system proxy")
	}
	settings := loadTUIConfiguredSettings(status.ConfigPath, true)
	if settings == nil {
		return false, errors.New("could not read the active configuration")
	}
	if enabled && settings.MixedPort <= 0 {
		return false, errors.New("active configuration has no usable Mixed Port")
	}
	r.mu.RLock()
	proxyPort := r.proxyPort
	r.mu.RUnlock()
	if proxyPort <= 0 {
		proxyPort = settings.MixedPort
	}
	if !enabled && !linuxSystemProxyMatches(proxyPort) {
		r.setSystemProxyState(false, 0)
		return true, nil
	}
	port := settings.MixedPort
	if !enabled {
		port = proxyPort
	}
	if err := setLinuxSystemProxy(port, enabled); err != nil {
		return false, err
	}
	r.setSystemProxyState(enabled, settings.MixedPort)
	return true, nil
}

func (r *tuiServiceRuntime) testProxySpeed(
	request tuiServiceRequest,
) tuiServiceStatus {
	status := r.snapshot(request.RequestID)
	client, closeClient, err := newTUIProxyNodeHTTPClient(request.ProxyName)
	if err == nil {
		var result tuiSpeedResult
		result, err = runTUIDownloadSpeedTest(context.Background(), client)
		if err == nil {
			status.Speed = &result
		}
		closeClient()
	}
	if err != nil {
		return failTUIServiceStatus(status, tuiServiceErrorOperation, err.Error())
	}
	return status
}

func (r *tuiServiceRuntime) testRoute(
	request tuiServiceRequest,
) tuiServiceStatus {
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	status := r.snapshot(request.RequestID)
	wasRunning := status.Running
	if !wasRunning {
		if _, err := r.reload(""); err != nil {
			return failTUIServiceStatus(
				status,
				tuiServiceErrorOperation,
				"prepare stopped Core for test: "+err.Error(),
			)
		}
		if !handleStartListener() {
			return failTUIServiceStatus(
				status,
				tuiServiceErrorOperation,
				"start proxy listeners for test failed",
			)
		}
		r.setRunning(true)
	}
	client, closeClient, err := newTUIRouteHTTPClient(request.MixedPort)
	if err == nil {
		if request.Action == "speed_route" {
			var result tuiSpeedResult
			result, err = runTUIDownloadSpeedTest(context.Background(), client)
			if err == nil {
				status.Speed = &result
			}
		} else {
			status.Delay, err = runTUIRouteDelayTest(
				context.Background(),
				client,
				request.TestURL,
			)
		}
		closeClient()
	}
	if !wasRunning {
		if !handleStopListener() && err == nil {
			err = errors.New("restore stopped Core state failed")
		}
		r.setRunning(false)
	}
	status = r.snapshot(request.RequestID)
	if err != nil {
		return failTUIServiceStatus(status, tuiServiceErrorOperation, err.Error())
	}
	return status
}

func (r *tuiServiceRuntime) setRunning(running bool) {
	r.mu.Lock()
	r.running = running
	r.mu.Unlock()
}

func (r *tuiServiceRuntime) setSystemProxyState(enabled bool, port int) {
	r.mu.Lock()
	r.systemProxy = enabled
	if enabled {
		r.proxyPort = port
	} else {
		r.proxyPort = 0
	}
	r.mu.Unlock()
}

func (r *tuiServiceRuntime) bumpRevision() {
	r.mu.Lock()
	r.revision++
	close(r.changed)
	r.changed = make(chan struct{})
	r.mu.Unlock()
}

func (r *tuiServiceRuntime) cached(
	requestID string,
) (tuiServiceStatus, bool) {
	r.mu.RLock()
	status, ok := r.dedup[requestID]
	r.mu.RUnlock()
	return status, ok
}

func (r *tuiServiceRuntime) remember(
	requestID string,
	status tuiServiceStatus,
) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.dedup[requestID]; exists {
		return
	}
	r.dedup[requestID] = status
	r.dedupOrder = append(r.dedupOrder, requestID)
	if len(r.dedupOrder) > tuiServiceDedupLimit {
		delete(r.dedup, r.dedupOrder[0])
		r.dedupOrder = r.dedupOrder[1:]
	}
}

func failTUIServiceStatus(
	status tuiServiceStatus,
	code,
	message string,
) tuiServiceStatus {
	status.OK = false
	status.ErrorCode = code
	status.Error = message
	return status
}
