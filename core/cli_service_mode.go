//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

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
	oldTunEnabled := r.tunEnabled
	oldTunScope := r.tunScope
	oldTunLease := r.tunLease
	running := r.running
	runtimePort := r.runtimePort
	activePort := r.activePort
	r.mu.RUnlock()
	if mode == currentMode {
		return false, nil
	}
	oldSettings := loadTUIConfiguredSettings(paths.ConfigPath, true)
	if oldSettings == nil {
		return false, errors.New("could not load active settings")
	}
	if mode == tuiSilentMode {
		outbound := loadTUIFLCOutbound(paths.HomeDir)
		flc := tuiFLCListenerState{Outbound: outbound}
		if outbound != "" {
			var err error
			port := activePort
			if port <= 0 {
				port = runtimePort
			}
			if port <= 0 {
				port = oldSettings.MixedPort
			}
			flc, err = newTUIFLCListenerStateAtPort(outbound, port)
			if err != nil {
				return false, err
			}
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
		r.tunEnabled = false
		r.tunLease = nil
		r.mu.Unlock()
		if _, err := r.reloadUnlocked(paths.ConfigPath, ""); err != nil {
			r.mu.Lock()
			r.trafficMode = currentMode
			r.flc = oldFLC
			r.tunEnabled = oldTunEnabled
			r.tunLease = oldTunLease
			r.mu.Unlock()
			if systemProxy {
				if _, proxyRollbackErr := r.applySystemProxy(true); proxyRollbackErr != nil {
					return false, fmt.Errorf(
						"switch to silent mode: %v; System proxy rollback failed: %w",
						err,
						proxyRollbackErr,
					)
				}
			}
			return false, err
		}
		if err := rememberTUITrafficMode(paths.HomeDir, mode); err != nil {
			r.mu.Lock()
			r.trafficMode = currentMode
			r.flc = oldFLC
			r.tunEnabled = oldTunEnabled
			r.tunLease = oldTunLease
			r.mu.Unlock()
			_, rollbackErr := r.reloadUnlocked(paths.ConfigPath, "")
			var proxyRollbackErr error
			if systemProxy {
				_, proxyRollbackErr = r.applySystemProxy(true)
			}
			if rollbackErr != nil || proxyRollbackErr != nil {
				return false, fmt.Errorf(
					"save silent mode: %v; Core rollback: %v; System proxy rollback: %v",
					err,
					rollbackErr,
					proxyRollbackErr,
				)
			}
			return false, fmt.Errorf("save silent mode: %w", err)
		}
		oldTunLease.release()
		_, _ = r.repairFLCOutbound()
		return true, nil
	}
	if currentMode != tuiSilentMode {
		return r.applyNativeTrafficMode(
			mode,
			currentMode,
			paths,
			oldFLC,
			*oldSettings,
			running,
		)
	}

	updated := *oldSettings
	updated.Mode = mode
	profileChanged := !strings.EqualFold(oldSettings.Mode, mode)
	writePath, profileInfo, originalProfile, err := readTUIWritableConfig(
		paths.ConfigPath,
	)
	if err != nil {
		return false, err
	}
	r.mu.Lock()
	r.trafficMode = mode
	r.flc = tuiFLCListenerState{Outbound: oldFLC.Outbound}
	restoreUserTun := oldTunScope == tuiTunScopeUser && oldSettings.TunEnabled
	r.tunEnabled = restoreUserTun
	r.mu.Unlock()
	var restoredTunLease *tuiTunLease
	if restoreUserTun && running {
		restoredTunLease, _, err = acquireTUITunLease(tuiTunScopeUser)
		if err != nil {
			r.mu.Lock()
			r.trafficMode = currentMode
			r.flc = oldFLC
			r.tunEnabled = false
			r.mu.Unlock()
			return false, err
		}
		r.mu.Lock()
		r.tunLease = restoredTunLease
		r.mu.Unlock()
	}
	if profileChanged {
		_, err = r.applySettings(updated)
	} else {
		_, err = r.reloadUnlocked(paths.ConfigPath, "")
	}
	if err != nil {
		restoredTunLease.release()
		r.mu.Lock()
		r.trafficMode = currentMode
		r.flc = oldFLC
		r.tunEnabled = false
		r.tunLease = nil
		r.mu.Unlock()
		return false, err
	}
	if err := rememberTUITrafficMode(paths.HomeDir, mode); err != nil {
		var profileRollbackErr error
		if profileChanged {
			lease, lockErr := acquireTUIProfileLocks(paths.HomeDir, paths.ConfigPath)
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
		r.tunEnabled = false
		r.tunLease = nil
		r.mu.Unlock()
		restoredTunLease.release()
		_, coreRollbackErr := r.reloadUnlocked(paths.ConfigPath, "")
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
	running bool,
) (bool, error) {
	lease, err := acquireTUIProfileLocks(paths.HomeDir, paths.ConfigPath)
	if err != nil {
		return false, err
	}
	defer lease.release()

	writePath, profileInfo, originalProfile, err := readTUIWritableConfig(
		paths.ConfigPath,
	)
	if err != nil {
		return false, err
	}
	profileChanged := !strings.EqualFold(oldSettings.Mode, mode)
	if profileChanged {
		updated := oldSettings
		updated.Mode = mode
		if err := persistTUISettings(paths.ConfigPath, updated); err != nil {
			return false, err
		}
	}

	controller := r.coreController
	if running {
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
	}

	if err := rememberTUITrafficMode(paths.HomeDir, mode); err != nil {
		var profileRollbackErr error
		if profileChanged {
			profileRollbackErr = writeTUIProfileAtomically(
				writePath,
				originalProfile,
				profileInfo.Mode(),
			)
		}
		var coreRollbackErr error
		if running {
			coreRollbackErr = controller.patchConfig(
				map[string]interface{}{"mode": currentMode},
			)
		}
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
	runtimePort := r.runtimePort
	activePort := r.activePort
	r.mu.RUnlock()
	if outbound == previous.Outbound &&
		(mode != tuiSilentMode || previous.proxyURL() != "") {
		return false, nil
	}
	if err := validateTUIFLCOutbound(r.coreController, outbound); err != nil {
		return false, err
	}
	next := tuiFLCListenerState{Outbound: outbound}
	if mode == tuiSilentMode {
		var err error
		port := activePort
		if port <= 0 {
			port = runtimePort
		}
		next, err = newTUIFLCListenerStateAtPort(outbound, port)
		if err != nil {
			return false, err
		}
		r.mu.Lock()
		r.flc = next
		r.mu.Unlock()
		if _, err := r.reloadUnlocked(paths.ConfigPath, ""); err != nil {
			r.mu.Lock()
			r.flc = previous
			r.mu.Unlock()
			return false, err
		}
	}
	if err := rememberTUIFLCOutbound(paths.HomeDir, outbound); err != nil {
		if mode == tuiSilentMode {
			r.mu.Lock()
			r.flc = previous
			r.mu.Unlock()
			if _, rollbackErr := r.reloadUnlocked(paths.ConfigPath, ""); rollbackErr != nil {
				return false, fmt.Errorf(
					"save FLC outbound: %v; Core rollback failed: %w",
					err,
					rollbackErr,
				)
			}
		}
		return false, fmt.Errorf("save FLC outbound: %w", err)
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
	homeDir := r.paths.HomeDir
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
	changed := proxy != current.Now
	followed, followErr := r.followFLCOutboundGroup(group)
	if followErr != nil {
		return changed || followed, fmt.Errorf(
			"selected %s in %s, but silent flc was not pointed at that group: %w",
			proxy,
			group,
			followErr,
		)
	}
	return changed || followed, nil
}

func (r *tuiServiceRuntime) followFLCOutboundGroup(group string) (bool, error) {
	group = strings.TrimSpace(group)
	if group == "" {
		return false, nil
	}
	r.mu.RLock()
	current := strings.TrimSpace(r.flc.Outbound)
	r.mu.RUnlock()
	if strings.EqualFold(current, group) {
		return false, nil
	}
	return r.applyFLCOutbound(group)
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
	proxy, ok := response.Proxies[outbound]
	if !ok {
		return fmt.Errorf("proxy or group %q was not found", outbound)
	}
	if isTUIGroup(proxy.Type) && len(proxy.All) == 0 {
		return fmt.Errorf("proxy group %q has no nodes", outbound)
	}
	return nil
}
