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

type tuiServiceRuntime struct {
	mu               sync.RWMutex
	mutationMu       sync.Mutex
	paths            cliPaths
	testURL          string
	coreSocket       string
	coreController   controllerClient
	setupParams      []byte
	running          bool
	shuttingDown     bool
	systemProxy      bool
	proxyPort        int
	trafficMode      string
	configuredPort   int
	actualConfigPath string
	flc              tuiFLCListenerState
	history          []tuiRequest
	revision         uint64
	changed          chan struct{}
	dedup            map[string]tuiServiceStatus
	dedupOrder       []string
	shutdown         func()
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
		trafficMode:      "rule",
		actualConfigPath: paths.configPath,
		revision:         1,
		changed:          make(chan struct{}),
		dedup:            map[string]tuiServiceStatus{},
		shutdown:         shutdown,
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
	case "flc_proxy":
		status = r.flcProxy(request.RequestID)
	case "history":
		status = r.historyStatus(request.RequestID)
	case "watch":
		status = r.watch(request)
	case "speed_proxy":
		status = r.testProxySpeed(request)
	case "speed_route", "delay_route":
		status = r.testRoute(request)
	case "start", "stop", "reload", "apply_settings", "set_system_proxy",
		"set_mode", "set_flc_outbound", "select_proxy", "clear_history", "put_profile",
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
		ShuttingDown:    r.shuttingDown,
		SystemProxy:     r.systemProxy,
		Mode:            r.trafficMode,
		ProxyPort:       r.configuredPort,
		FLCEnabled:      r.running && r.trafficMode == tuiSilentMode && r.flc.Port > 0,
		FLCOutbound:     r.flc.Outbound,
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
	case "set_mode":
		changed, err = r.applyTrafficMode(request.Mode)
	case "set_flc_outbound":
		changed, err = r.applyFLCOutbound(request.ProxyName)
	case "select_proxy":
		changed, err = r.selectProxy(request.ProxyGroup, request.ProxyName)
	case "clear_history":
		r.mu.Lock()
		changed = len(r.history) > 0
		r.history = nil
		r.mu.Unlock()
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
		_, err = r.stopCoreAndProxy(status)
		r.setRunning(false)
		r.setShuttingDown(true)
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
	status.ResultPath = resultPath
	return r.completeMutation(request, status)
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
	r.actualConfigPath = actualConfigPath
	r.flc = flc
	r.mu.Unlock()
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

func (r *tuiServiceRuntime) putProfile(
	request tuiServiceRequest,
) (bool, string, error) {
	r.mu.RLock()
	paths := r.paths
	r.mu.RUnlock()
	target := filepath.Clean(request.ConfigPath)
	if _, err := tuiProfileStateKey(paths.homeDir, target); err != nil {
		return false, "", err
	}
	if len(request.ProfileData) == 0 {
		return false, "", errors.New("profile content must not be empty")
	}
	if len(request.ProfileData) > tuiSubscriptionMaxBytes {
		return false, "", fmt.Errorf(
			"profile content exceeds %d MiB",
			tuiSubscriptionMaxBytes>>20,
		)
	}
	if message := validateConfigBytes(request.ProfileData); message != "" {
		return false, "", errors.New("profile is invalid: " + message)
	}
	if request.SubscriptionURL != nil {
		if _, err := newTUISubscriptionRequest(*request.SubscriptionURL); err != nil {
			return false, "", err
		}
	}

	lease, err := acquireTUIProfileLocks(paths.homeDir, target)
	if err != nil {
		return false, "", err
	}
	defer lease.release()
	var original []byte
	mode := os.FileMode(0o600)
	existed := false
	info, statErr := os.Lstat(target)
	switch {
	case statErr == nil:
		existed = true
		if request.CreateOnly {
			return false, "", fmt.Errorf("profile %q already exists", filepath.Base(target))
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, "", errors.New("profile must be a regular file, not a symlink")
		}
		original, err = os.ReadFile(target)
		if err != nil {
			return false, "", err
		}
		if request.ExpectedSHA256 == "" {
			return false, "", errors.New("an expected profile digest is required")
		}
		if actual := tuiBytesSHA256(original); actual != request.ExpectedSHA256 {
			return false, "", errors.New("profile changed after it was read; refresh and retry")
		}
		mode = info.Mode()
	case os.IsNotExist(statErr):
		if !request.CreateOnly {
			return false, "", errors.New("profile no longer exists; refresh and retry")
		}
	default:
		return false, "", statErr
	}
	if err := writeTUIProfileAtomically(target, request.ProfileData, mode); err != nil {
		return false, "", err
	}
	active := filepath.Clean(target) == filepath.Clean(paths.configPath)
	rollback := func(cause error) error {
		var restoreErr error
		if existed {
			restoreErr = writeTUIProfileAtomically(target, original, mode)
		} else {
			restoreErr = os.Remove(target)
			if os.IsNotExist(restoreErr) {
				restoreErr = nil
			}
		}
		if restoreErr != nil {
			return fmt.Errorf("%v; profile rollback failed: %w", cause, restoreErr)
		}
		if active {
			if _, reloadErr := r.reloadUnlocked(target, ""); reloadErr != nil {
				return fmt.Errorf("%v; profile restored but Core rollback failed: %w", cause, reloadErr)
			}
		}
		return cause
	}
	if active {
		if _, err := r.reloadUnlocked(target, tuiBytesSHA256(request.ProfileData)); err != nil {
			return false, "", rollback(fmt.Errorf("reload profile: %w", err))
		}
	}
	if request.SubscriptionURL != nil {
		if err := rememberTUISubscriptionSource(
			paths.homeDir,
			target,
			*request.SubscriptionURL,
		); err != nil {
			return false, "", rollback(fmt.Errorf("save subscription source: %w", err))
		}
	}
	return true, target, nil
}

func (r *tuiServiceRuntime) renameProfile(
	path,
	newName string,
) (bool, string, error) {
	r.mu.RLock()
	paths := r.paths
	r.mu.RUnlock()
	path = filepath.Clean(path)
	if _, err := tuiProfileStateKey(paths.homeDir, path); err != nil {
		return false, "", err
	}
	if path == filepath.Clean(paths.configPath) {
		return false, "", errors.New("activate another profile before renaming the current profile")
	}
	renamed, err := renameTUIProfile(paths.homeDir, path, newName)
	if err != nil {
		return false, "", err
	}
	return renamed != path, renamed, nil
}

func (r *tuiServiceRuntime) deleteProfile(path string) (bool, error) {
	r.mu.RLock()
	paths := r.paths
	r.mu.RUnlock()
	path = filepath.Clean(path)
	key, err := tuiProfileStateKey(paths.homeDir, path)
	if err != nil {
		return false, err
	}
	if path == filepath.Clean(paths.configPath) {
		return false, errors.New("cannot delete the active profile")
	}
	lease, err := acquireTUIProfileLocks(paths.homeDir, path)
	if err != nil {
		return false, err
	}
	defer lease.release()
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("profile must be a regular file, not a symlink")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	if err := updateTUIState(paths.homeDir, func(state *tuiPersistentState) {
		delete(state.SubscriptionSources, key)
	}); err != nil {
		if restoreErr := writeTUIProfileAtomically(path, data, info.Mode()); restoreErr != nil {
			return false, fmt.Errorf("update profile metadata: %v; file rollback failed: %w", err, restoreErr)
		}
		return false, fmt.Errorf("update profile metadata: %w; file restored", err)
	}
	return true, nil
}

func (r *tuiServiceRuntime) linkProfile(
	path string,
	subscriptionURL *string,
) (bool, error) {
	if subscriptionURL == nil {
		return false, errors.New("subscription URL is required")
	}
	if _, err := newTUISubscriptionRequest(*subscriptionURL); err != nil {
		return false, err
	}
	r.mu.RLock()
	paths := r.paths
	r.mu.RUnlock()
	path = filepath.Clean(path)
	if _, err := tuiProfileStateKey(paths.homeDir, path); err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("profile must be a regular file, not a symlink")
	}
	current, currentErr := loadTUISubscriptionSource(paths.homeDir, path)
	if currentErr == nil && current == strings.TrimSpace(*subscriptionURL) {
		return false, nil
	}
	if err := rememberTUISubscriptionSource(paths.homeDir, path, *subscriptionURL); err != nil {
		return false, err
	}
	return true, nil
}

func (r *tuiServiceRuntime) backupProfile(path string) (bool, string, error) {
	r.mu.RLock()
	paths := r.paths
	r.mu.RUnlock()
	path = filepath.Clean(path)
	if _, err := tuiProfileStateKey(paths.homeDir, path); err != nil {
		return false, "", err
	}
	lease, err := acquireTUIProfileLocks(paths.homeDir, path)
	if err != nil {
		return false, "", err
	}
	defer lease.release()
	info, err := os.Lstat(path)
	if err != nil {
		return false, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, "", errors.New("profile must be a regular file, not a symlink")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, "", err
	}
	backupPath := fmt.Sprintf("%s.backup-%d", path, time.Now().UnixNano())
	backup, err := os.OpenFile(
		backupPath,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return false, "", err
	}
	if _, err := backup.Write(data); err != nil {
		_ = backup.Close()
		_ = os.Remove(backupPath)
		return false, "", err
	}
	if err := backup.Sync(); err != nil {
		_ = backup.Close()
		_ = os.Remove(backupPath)
		return false, "", err
	}
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupPath)
		return false, "", err
	}
	return true, backupPath, nil
}

func (r *tuiServiceRuntime) restoreProfile(path string) (bool, string, error) {
	r.mu.RLock()
	paths := r.paths
	r.mu.RUnlock()
	path = filepath.Clean(path)
	if _, err := tuiProfileStateKey(paths.homeDir, path); err != nil {
		return false, "", err
	}
	backupPath, backup, err := restoreLatestTUIConfigLocked(paths.homeDir, path)
	if err != nil {
		return false, "", err
	}
	defer backup.release()
	if path != filepath.Clean(paths.configPath) {
		return true, backupPath, nil
	}
	if _, err := r.reloadUnlocked(path, backup.updatedSHA256); err != nil {
		if restoreErr := restoreTUISubscriptionProfile(path, backup); restoreErr != nil {
			return false, "", fmt.Errorf("reload restored profile: %v; file rollback failed: %w", err, restoreErr)
		}
		if _, rollbackErr := r.reloadUnlocked(path, tuiBytesSHA256(backup.data)); rollbackErr != nil {
			return false, "", fmt.Errorf("reload restored profile: %v; Core rollback failed: %w", err, rollbackErr)
		}
		return false, "", fmt.Errorf("reload restored profile: %w; original restored", err)
	}
	return true, backupPath, nil
}

func (r *tuiServiceRuntime) applyTrafficMode(mode string) (bool, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != tuiSilentMode && mode != "rule" && mode != "global" && mode != "direct" {
		return false, errors.New("mode must be rule, global, direct, or silent")
	}
	r.mu.RLock()
	currentMode := r.trafficMode
	paths := r.paths
	oldFLC := r.flc
	systemProxy := r.systemProxy
	r.mu.RUnlock()
	if mode == currentMode {
		return false, nil
	}
	oldSettings := loadTUIConfiguredSettings(paths.configPath, true)
	if oldSettings == nil {
		return false, errors.New("could not load active settings")
	}
	if mode == tuiSilentMode {
		outbound := loadTUIFLCOutbound(paths.homeDir)
		flc, err := newTUIFLCListenerState(outbound)
		if err != nil {
			return false, err
		}
		if systemProxy {
			if _, err := r.applySystemProxy(false); err != nil {
				return false, fmt.Errorf("disable System proxy for silent mode: %w", err)
			}
		}
		_ = r.coreController.closeAllConnections()
		r.mu.Lock()
		r.trafficMode = mode
		r.flc = flc
		r.mu.Unlock()
		if _, err := r.reloadUnlocked(paths.configPath, ""); err != nil {
			r.mu.Lock()
			r.trafficMode = currentMode
			r.flc = oldFLC
			r.mu.Unlock()
			if systemProxy {
				_, _ = r.applySystemProxy(true)
			}
			return false, err
		}
		if err := rememberTUITrafficMode(paths.homeDir, mode); err != nil {
			r.mu.Lock()
			r.trafficMode = currentMode
			r.flc = oldFLC
			r.mu.Unlock()
			_, rollbackErr := r.reloadUnlocked(paths.configPath, "")
			if systemProxy {
				_, _ = r.applySystemProxy(true)
			}
			if rollbackErr != nil {
				return false, fmt.Errorf(
					"save silent mode: %v; Core rollback failed: %w",
					err,
					rollbackErr,
				)
			}
			return false, fmt.Errorf("save silent mode: %w", err)
		}
		return true, nil
	}
	if currentMode != tuiSilentMode {
		return r.applyNativeTrafficMode(
			mode,
			currentMode,
			paths,
			oldFLC,
			*oldSettings,
		)
	}

	updated := *oldSettings
	updated.Mode = mode
	profileChanged := !strings.EqualFold(oldSettings.Mode, mode)
	writePath, profileInfo, originalProfile, err := readTUIWritableConfig(
		paths.configPath,
	)
	if err != nil {
		return false, err
	}
	r.mu.Lock()
	r.trafficMode = mode
	r.flc = tuiFLCListenerState{Outbound: oldFLC.Outbound}
	r.mu.Unlock()
	if profileChanged {
		_, err = r.applySettings(updated)
	} else {
		_, err = r.reloadUnlocked(paths.configPath, "")
	}
	if err != nil {
		r.mu.Lock()
		r.trafficMode = currentMode
		r.flc = oldFLC
		r.mu.Unlock()
		return false, err
	}
	if err := rememberTUITrafficMode(paths.homeDir, mode); err != nil {
		var profileRollbackErr error
		if profileChanged {
			lease, lockErr := acquireTUIProfileLocks(paths.homeDir, paths.configPath)
			if lockErr != nil {
				profileRollbackErr = lockErr
			} else {
				profileRollbackErr = writeTUIProfileAtomically(
					writePath,
					originalProfile,
					profileInfo.Mode(),
				)
				lease.release()
			}
		}
		r.mu.Lock()
		r.trafficMode = currentMode
		r.flc = oldFLC
		r.mu.Unlock()
		_, coreRollbackErr := r.reloadUnlocked(paths.configPath, "")
		if profileRollbackErr != nil || coreRollbackErr != nil {
			return false, fmt.Errorf(
				"save mode: %v; profile rollback: %v; Core silent-mode rollback: %v",
				err,
				profileRollbackErr,
				coreRollbackErr,
			)
		}
		return false, fmt.Errorf("save mode: %w", err)
	}
	return true, nil
}

func (r *tuiServiceRuntime) applyNativeTrafficMode(
	mode,
	currentMode string,
	paths cliPaths,
	oldFLC tuiFLCListenerState,
	oldSettings tuiSettings,
) (bool, error) {
	lease, err := acquireTUIProfileLocks(paths.homeDir, paths.configPath)
	if err != nil {
		return false, err
	}
	defer lease.release()

	writePath, profileInfo, originalProfile, err := readTUIWritableConfig(
		paths.configPath,
	)
	if err != nil {
		return false, err
	}
	profileChanged := !strings.EqualFold(oldSettings.Mode, mode)
	if profileChanged {
		updated := oldSettings
		updated.Mode = mode
		if err := persistTUISettings(paths.configPath, updated); err != nil {
			return false, err
		}
	}

	controller := r.coreController
	if err := controller.patchConfig(map[string]interface{}{"mode": mode}); err != nil {
		if !profileChanged {
			return false, fmt.Errorf("switch Core mode: %w", err)
		}
		if rollbackErr := writeTUIProfileAtomically(
			writePath,
			originalProfile,
			profileInfo.Mode(),
		); rollbackErr != nil {
			return false, fmt.Errorf(
				"switch Core mode: %v; profile rollback failed: %w",
				err,
				rollbackErr,
			)
		}
		return false, fmt.Errorf("switch Core mode: %w; profile restored", err)
	}

	if err := rememberTUITrafficMode(paths.homeDir, mode); err != nil {
		var profileRollbackErr error
		if profileChanged {
			profileRollbackErr = writeTUIProfileAtomically(
				writePath,
				originalProfile,
				profileInfo.Mode(),
			)
		}
		coreRollbackErr := controller.patchConfig(
			map[string]interface{}{"mode": currentMode},
		)
		if profileRollbackErr != nil || coreRollbackErr != nil {
			return false, fmt.Errorf(
				"save mode: %v; profile rollback: %v; Core rollback: %v",
				err,
				profileRollbackErr,
				coreRollbackErr,
			)
		}
		return false, fmt.Errorf("save mode: %w; previous mode restored", err)
	}

	r.mu.Lock()
	r.trafficMode = mode
	r.flc = tuiFLCListenerState{Outbound: oldFLC.Outbound}
	r.mu.Unlock()
	return true, nil
}

func (r *tuiServiceRuntime) applyFLCOutbound(outbound string) (bool, error) {
	outbound = strings.TrimSpace(outbound)
	if outbound == "" {
		return false, errors.New("FLC outbound must not be empty")
	}
	r.mu.RLock()
	paths := r.paths
	mode := r.trafficMode
	previous := r.flc
	r.mu.RUnlock()
	if outbound == previous.Outbound {
		return false, nil
	}
	if err := validateTUIFLCOutbound(r.coreController, outbound); err != nil {
		return false, err
	}
	next := tuiFLCListenerState{Outbound: outbound}
	if mode == tuiSilentMode {
		var err error
		next, err = newTUIFLCListenerState(outbound)
		if err != nil {
			return false, err
		}
		r.mu.Lock()
		r.flc = next
		r.mu.Unlock()
		if _, err := r.reloadUnlocked(paths.configPath, ""); err != nil {
			r.mu.Lock()
			r.flc = previous
			r.mu.Unlock()
			return false, err
		}
	}
	if err := rememberTUIFLCOutbound(paths.homeDir, outbound); err != nil {
		if mode == tuiSilentMode {
			r.mu.Lock()
			r.flc = previous
			r.mu.Unlock()
			_, _ = r.reloadUnlocked(paths.configPath, "")
		}
		return false, err
	}
	if mode != tuiSilentMode {
		r.mu.Lock()
		r.flc = next
		r.mu.Unlock()
	}
	return true, nil
}

func (r *tuiServiceRuntime) selectProxy(group, proxy string) (bool, error) {
	group = strings.TrimSpace(group)
	proxy = strings.TrimSpace(proxy)
	if group == "" || proxy == "" {
		return false, errors.New("proxy group and node must not be empty")
	}
	r.mu.RLock()
	homeDir := r.paths.homeDir
	r.mu.RUnlock()
	controller := r.coreController
	data, err := controller.request(http.MethodGet, "/proxies", nil)
	if err != nil {
		return false, fmt.Errorf("read current proxy selection: %w", err)
	}
	var response tuiProxyResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return false, fmt.Errorf("parse current proxy selection: %w", err)
	}
	current, ok := response.Proxies[group]
	if !ok || len(current.All) == 0 {
		return false, fmt.Errorf("proxy group %q was not found", group)
	}
	found := false
	for _, candidate := range current.All {
		if candidate == proxy {
			found = true
			break
		}
	}
	if !found {
		return false, fmt.Errorf("proxy %q is not in group %q", proxy, group)
	}
	if err := controller.setProxy(
		group,
		proxy,
	); err != nil {
		return false, err
	}
	if err := rememberTUIProxySelection(homeDir, group, proxy); err != nil {
		if rollbackErr := controller.setProxy(group, current.Now); rollbackErr != nil {
			return false, fmt.Errorf(
				"save proxy selection: %v; Core rollback failed: %w",
				err,
				rollbackErr,
			)
		}
		return false, fmt.Errorf("save proxy selection: %w; Core selection restored", err)
	}
	return proxy != current.Now, nil
}

func validateTUIFLCOutbound(controller controllerClient, outbound string) error {
	data, err := controller.request(http.MethodGet, "/proxies", nil)
	if err != nil {
		return fmt.Errorf("read proxy list: %w", err)
	}
	var response tuiProxyResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return fmt.Errorf("parse proxy list: %w", err)
	}
	if _, ok := response.Proxies[outbound]; !ok {
		return fmt.Errorf("proxy or group %q was not found", outbound)
	}
	return nil
}

func (r *tuiServiceRuntime) historyStatus(requestID string) tuiServiceStatus {
	status := r.snapshot(requestID)
	if status.Running {
		connections, err := loadTUIActiveConnections(r.coreController)
		if err != nil {
			return failTUIServiceStatus(
				status,
				tuiServiceErrorOperation,
				err.Error(),
			)
		}
		r.mu.Lock()
		r.history = updateTUIRequestHistory(r.history, connections, time.Now())
		status.History = append([]tuiRequest(nil), r.history...)
		r.mu.Unlock()
	} else {
		r.mu.RLock()
		status.History = append([]tuiRequest(nil), r.history...)
		r.mu.RUnlock()
	}
	return status
}

func loadTUIActiveConnections(controller controllerClient) ([]tuiConnection, error) {
	data, err := controller.request(
		http.MethodGet,
		"/connections",
		nil,
	)
	if err != nil {
		return nil, err
	}
	var value struct {
		Connections []struct {
			ID       string `json:"id"`
			Metadata struct {
				Host            string `json:"host"`
				DestinationIP   string `json:"destinationIP"`
				DestinationPort string `json:"destinationPort"`
				Process         string `json:"process"`
				Network         string `json:"network"`
			} `json:"metadata"`
			Upload   int64    `json:"upload"`
			Download int64    `json:"download"`
			Chains   []string `json:"chains"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, err
	}
	connections := make([]tuiConnection, 0, len(value.Connections))
	for _, item := range value.Connections {
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
		connections = append(connections, tuiConnection{
			ID:       item.ID,
			Host:     host,
			Process:  item.Metadata.Process,
			Network:  item.Metadata.Network,
			Chain:    chain,
			Upload:   item.Upload,
			Download: item.Download,
		})
	}
	return connections, nil
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
				return false, errors.New("read active Proxy port for System proxy cleanup")
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
			errors.New("reloaded profile has no usable Proxy port (Mihomo mixed-port)"),
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
	mode := r.trafficMode
	flc := r.flc
	previousActualPath := r.actualConfigPath
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
	actualConfigPath := configPath
	if mode == tuiSilentMode {
		logicalPaths := paths
		logicalPaths.configPath = configPath
		var err error
		actualConfigPath, err = writeTUISilentRuntimeConfig(logicalPaths, flc)
		if err != nil {
			return false, err
		}
	}
	reloaded, err := reloadTUIActualConfig(
		paths.homeDir,
		previousActualPath,
		actualConfigPath,
		r.testURL,
		r.coreSocket,
		setupParams,
		running,
	)
	if err != nil {
		if mode == tuiSilentMode {
			_ = os.Remove(actualConfigPath)
		}
		return false, err
	}
	updatedPaths := paths
	updatedPaths.configPath = configPath
	if configPath != paths.configPath {
		if err := rememberTUIActiveProfile(updatedPaths); err != nil {
			_, rollbackErr := reloadTUIActualConfig(
				paths.homeDir,
				actualConfigPath,
				previousActualPath,
				r.testURL,
				r.coreSocket,
				reloaded,
				running,
			)
			if mode == tuiSilentMode {
				_ = os.Remove(actualConfigPath)
			}
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
	settings := loadTUIConfiguredSettings(configPath, true)
	r.mu.Lock()
	r.paths.configPath = configPath
	r.setupParams = append([]byte(nil), reloaded...)
	r.actualConfigPath = actualConfigPath
	if settings != nil {
		r.configuredPort = settings.MixedPort
		if mode != tuiSilentMode {
			r.trafficMode = strings.ToLower(settings.Mode)
		}
	}
	r.mu.Unlock()
	if mode != tuiSilentMode && settings != nil {
		_ = rememberTUITrafficMode(paths.homeDir, settings.Mode)
	}
	if previousActualPath != actualConfigPath &&
		strings.Contains(filepath.Base(previousActualPath), tuiSilentRuntimeConfigPrefix) {
		_ = os.Remove(previousActualPath)
	}
	if mode == tuiSilentMode {
		cleanupTUISilentRuntimeConfigs(paths.homeDir, actualConfigPath)
	}
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
	mode := r.trafficMode
	r.mu.RUnlock()
	if mode == tuiSilentMode && settings.TunEnabled {
		return false, errors.New("TUN cannot be enabled in silent mode")
	}
	if strings.EqualFold(settings.Mode, tuiSilentMode) {
		return false, errors.New("silent is a FlClash mode and cannot be written to the profile")
	}
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
	if enabled && status.Mode == tuiSilentMode {
		return false, errors.New(
			"System proxy cannot be enabled in silent mode; switch mode first",
		)
	}
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
		return false, errors.New("active configuration has no usable Proxy port (Mihomo mixed-port)")
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

func (r *tuiServiceRuntime) setShuttingDown(shuttingDown bool) {
	r.mu.Lock()
	r.shuttingDown = shuttingDown
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
