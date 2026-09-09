//go:build linux && !cgo && cli

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validateTUIProxyPortTransition(previousPort, targetPort int) error {
	if targetPort > 0 && !waitForTUIProxyPortState(
		targetPort,
		true,
		tuiListenerValidationTimeout,
	) {
		return fmt.Errorf(
			"proxy listener on 127.0.0.1:%d did not become ready",
			targetPort,
		)
	}
	if previousPort > 0 && previousPort != targetPort &&
		!waitForTUIProxyPortState(
			previousPort,
			false,
			tuiListenerValidationTimeout,
		) {
		return fmt.Errorf("previous proxy listener on 127.0.0.1:%d did not close", previousPort)
	}
	return nil
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
	writePath, info, original, err := readTUIWritableConfig(configPath)
	if err != nil {
		return false, err
	}
	if err := persistTUISettings(configPath, settings); err != nil {
		return false, err
	}
	if _, err := r.reloadUnlocked(configPath, ""); err == nil {
		r.mu.RLock()
		newActivePort := r.activePort
		r.mu.RUnlock()
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
		if systemProxy && proxyPort != newActivePort {
			if proxyPort > 0 && !linuxSystemProxyMatches(proxyPort) {
				r.setSystemProxyState(false, 0)
			} else if proxyErr := setLinuxSystemProxy(newActivePort, true); proxyErr != nil {
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
				var proxyRestoreErr error
				if proxyPort > 0 {
					proxyRestoreErr = setLinuxSystemProxy(proxyPort, true)
					if proxyRestoreErr != nil {
						r.setSystemProxyState(false, 0)
					}
				}
				if reloadErr != nil || proxyRestoreErr != nil {
					return false, fmt.Errorf(
						"update managed system proxy: %v; Core rollback: %v; System proxy rollback: %v",
						proxyErr,
						reloadErr,
						proxyRestoreErr,
					)
				}
				return false, fmt.Errorf(
					"update managed system proxy: %w; configuration restored",
					proxyErr,
				)
			} else {
				r.setSystemProxyState(true, newActivePort)
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
		if _, rollbackErr := r.reloadUnlocked(configPath, ""); rollbackErr != nil {
			return false, fmt.Errorf(
				"apply settings: %v; original profile restored but Core rollback failed: %w",
				reloadErr,
				rollbackErr,
			)
		}
		return false, fmt.Errorf(
			"apply settings: %w; original configuration and Core listener restored",
			reloadErr,
		)
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
	activePort := r.activePort
	r.mu.RUnlock()
	if proxyPort <= 0 {
		proxyPort = activePort
	}
	if proxyPort <= 0 {
		proxyPort = settings.MixedPort
	}
	if !enabled && !linuxSystemProxyMatches(proxyPort) {
		r.setSystemProxyState(false, 0)
		return true, nil
	}
	port := proxyPort
	if !enabled {
		port = proxyPort
	}
	if err := setLinuxSystemProxy(port, enabled); err != nil {
		return false, err
	}
	r.setSystemProxyState(enabled, proxyPort)
	return true, nil
}

func (r *tuiServiceRuntime) applyTun(enabled bool, requestedScope string) (bool, error) {
	scope, err := normalizeTUITunScope(requestedScope)
	if err != nil {
		return false, err
	}
	r.mu.RLock()
	previousScope := r.tunScope
	previousEnabled := r.tunEnabled
	mode := r.trafficMode
	running := r.running
	r.mu.RUnlock()
	if enabled && mode == tuiSilentMode {
		return false, errors.New("TUN cannot be enabled in silent mode")
	}
	if enabled == previousEnabled && scope == previousScope {
		return false, nil
	}
	if enabled && previousEnabled && scope != previousScope {
		return false, errors.New("turn TUN off before changing its scope")
	}
	settings := loadTUIConfiguredSettings(r.paths.configPath, true)
	if settings == nil {
		return false, errors.New("could not read the active configuration")
	}
	var replacementLease *tuiTunLease
	if enabled && running {
		replacementLease, _, err = acquireTUITunLease(scope)
		if err != nil {
			return false, err
		}
	}
	if err := rememberTUITunScope(r.paths.homeDir, scope); err != nil {
		replacementLease.release()
		return false, fmt.Errorf("remember TUN scope: %w", err)
	}
	r.mu.Lock()
	previousLease := r.tunLease
	r.tunScope = scope
	r.tunEnabled = enabled && running
	r.tunLease = replacementLease
	r.mu.Unlock()
	settings.TunEnabled = enabled && scope == tuiTunScopeUser
	if _, err := r.applySettings(*settings); err != nil {
		r.mu.Lock()
		r.tunScope = previousScope
		r.tunEnabled = previousEnabled
		r.tunLease = previousLease
		r.mu.Unlock()
		replacementLease.release()
		scopeRestoreErr := rememberTUITunScope(
			r.paths.homeDir,
			previousScope,
		)
		_, coreRestoreErr := r.reloadUnlocked(r.paths.configPath, "")
		if scopeRestoreErr != nil || coreRestoreErr != nil {
			var rollbackErrors []error
			if scopeRestoreErr != nil {
				rollbackErrors = append(
					rollbackErrors,
					fmt.Errorf("restore TUN scope: %w", scopeRestoreErr),
				)
			}
			if coreRestoreErr != nil {
				rollbackErrors = append(
					rollbackErrors,
					fmt.Errorf("restore Core TUN state: %w", coreRestoreErr),
				)
			}
			return false, fmt.Errorf(
				"apply TUN settings: %v; rollback failed: %w",
				err,
				errors.Join(rollbackErrors...),
			)
		}
		return false, err
	}
	r.mu.Lock()
	r.tunEnabled = enabled
	r.mu.Unlock()
	previousLease.release()
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
		if _, startErr := r.startCoreListeners(); startErr != nil {
			return failTUIServiceStatus(
				status,
				tuiServiceErrorOperation,
				"start proxy listeners for test: "+startErr.Error(),
			)
		}
	}
	activeStatus := r.snapshot(request.RequestID)
	activePort := activeStatus.ActiveProxyPort
	if activePort <= 0 {
		activePort = activeStatus.ProxyPort
	}
	proxyURL := tuiLoopbackProxyURL(activePort)
	if activeStatus.Mode == tuiSilentMode {
		r.mu.RLock()
		proxyURL = r.flc.proxyURL()
		r.mu.RUnlock()
	}
	var speed *tuiSpeedResult
	var delay tuiDelayResult
	client, closeClient, err := r.routeClient(proxyURL)
	if err == nil {
		if request.Action == "speed_route" {
			var result tuiSpeedResult
			result, err = r.routeSpeedTest(context.Background(), client)
			if err == nil {
				speed = &result
			}
		} else {
			delay, err = r.routeDelayTest(
				context.Background(),
				client,
				request.TestURL,
			)
		}
		closeClient()
	}
	if !wasRunning {
		cleanupErr := r.restoreStoppedRouteTestState(handleStopListener())
		if err == nil {
			err = cleanupErr
		}
	}
	status = r.snapshot(request.RequestID)
	if err != nil {
		return failTUIServiceStatus(status, tuiServiceErrorOperation, err.Error())
	}
	status.Speed = speed
	if delay.MedianMillis > 0 {
		status.Delay = delay.MedianMillis
		status.DelayJitter = delay.JitterMillis
		status.DelayMin = delay.MinMillis
		status.DelayMax = delay.MaxMillis
		status.DelaySamples = delay.Samples
	}
	return status
}

func (r *tuiServiceRuntime) restoreStoppedRouteTestState(
	listenerStopped bool,
) error {
	r.setRunning(false)
	r.releaseTunLease()
	if !listenerStopped {
		return errors.New("restore stopped Core state failed")
	}
	return nil
}

func (r *tuiServiceRuntime) setRunning(running bool) {
	r.mu.Lock()
	r.running = running
	if !running {
		r.activePort = 0
	}
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
