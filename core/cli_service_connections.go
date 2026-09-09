//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

func (r *tuiServiceRuntime) historyStatus(requestID string) tuiServiceStatus {
	r.historyUpdateMu.Lock()
	defer r.historyUpdateMu.Unlock()
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
		connections = filterTUIConnections(
			connections,
			uint32(os.Getuid()),
			status.TunState == "on" && status.TunScope == tuiTunScopeSystem,
		)
		r.mu.RLock()
		previous := append([]tuiRequest(nil), r.history...)
		r.mu.RUnlock()
		updated := updateTUIRequestHistory(previous, connections, time.Now())
		r.recordHistoryUpdate(updated)
		status.History = append([]tuiRequest(nil), updated...)
	} else {
		r.mu.RLock()
		history := append([]tuiRequest(nil), r.history...)
		r.mu.RUnlock()
		updated, changed := markTUIRequestHistoryInactive(history)
		if changed {
			r.recordHistoryUpdate(updated)
		}
		status.History = updated
	}
	return status
}

func (r *tuiServiceRuntime) connectionsStatus(requestID string) tuiServiceStatus {
	status := r.snapshot(requestID)
	if !status.Running {
		return status
	}
	connections, err := loadTUIActiveConnections(r.coreController)
	if err != nil {
		return failTUIServiceStatus(status, tuiServiceErrorOperation, err.Error())
	}
	status.Connections = filterTUIConnections(
		connections,
		uint32(os.Getuid()),
		status.TunState == "on" && status.TunScope == tuiTunScopeSystem,
	)
	return status
}

func closeTUIVisibleConnections(controller controllerClient, uid uint32, systemTun bool, id string) error {
	if systemTun {
		if id == "" {
			return controller.closeAllConnections()
		}
		return controller.closeConnection(id)
	}
	connections, err := loadTUIActiveConnections(controller)
	if err != nil {
		return err
	}
	var failures []error
	for _, connection := range filterTUIConnections(connections, uid, false) {
		if id != "" && connection.ID != id {
			continue
		}
		if err := controller.closeConnection(connection.ID); err != nil {
			failures = append(failures, err)
		}
		if id != "" {
			return errors.Join(failures...)
		}
	}
	if id != "" {
		return errors.New("connection is no longer active or is outside the current user scope")
	}
	return errors.Join(failures...)
}

func filterTUIConnections(
	connections []tuiConnection,
	uid uint32,
	systemTun bool,
) []tuiConnection {
	if systemTun {
		return connections
	}
	filtered := connections[:0]
	for _, connection := range connections {
		if isTUIOwnedConnection(connection, uid, false) {
			filtered = append(filtered, connection)
		}
	}
	return filtered
}

func isTUIOwnedConnection(
	connection tuiConnection,
	uid uint32,
	systemTun bool,
) bool {
	if systemTun || connection.UID == uid {
		return true
	}
	if connection.UID != 0 || connection.InboundName != tuiFLCListenerName ||
		connection.InboundUser != "flc" {
		return false
	}
	address := net.ParseIP(strings.TrimSpace(connection.SourceIP))
	return address != nil && address.IsLoopback()
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
				ProcessPath     string `json:"processPath"`
				UID             uint32 `json:"uid"`
				SourceIP        string `json:"sourceIP"`
				InboundName     string `json:"inboundName"`
				InboundUser     string `json:"inboundUser"`
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
		chain := formatTUIProxyChain(item.Chains)
		host := item.Metadata.Host
		if host == "" {
			host = formatTUIDestination(
				item.Metadata.DestinationIP,
				item.Metadata.DestinationPort,
			)
		}
		connections = append(connections, tuiConnection{
			ID:          item.ID,
			Host:        host,
			Process:     item.Metadata.Process,
			ProcessPath: item.Metadata.ProcessPath,
			UID:         item.Metadata.UID,
			SourceIP:    item.Metadata.SourceIP,
			InboundName: item.Metadata.InboundName,
			InboundUser: item.Metadata.InboundUser,
			Network:     item.Metadata.Network,
			Chain:       chain,
			Upload:      item.Upload,
			Download:    item.Download,
		})
	}
	return connections, nil
}

func formatTUIProxyChain(chains []string) string {
	if len(chains) == 0 {
		return "DIRECT"
	}
	return strings.Join(chains, " → ")
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
		if !stopTUIServiceCoreListeners() {
			return changed, errors.New("stop proxy listeners failed")
		}
		if status.ActiveProxyPort > 0 && !waitTUIServiceProxyPortState(
			status.ActiveProxyPort,
			false,
			tuiListenerValidationTimeout,
		) {
			return changed, fmt.Errorf(
				"proxy listener on 127.0.0.1:%d did not stop; Core remains marked running",
				status.ActiveProxyPort,
			)
		}
		r.setRunning(false)
		changed = true
	}
	r.releaseTunLease()
	return changed, nil
}

func (r *tuiServiceRuntime) releaseTunLease() {
	r.mu.Lock()
	lease := r.tunLease
	r.tunLease = nil
	r.mu.Unlock()
	lease.release()
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
	changed, err := r.reloadAndRepairFLCUnlocked(configPath, expectedSHA256)
	if err != nil || !systemProxy {
		return changed, err
	}
	if loadTUIConfiguredSettings(configPath, true) == nil {
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
	r.mu.RLock()
	activePort := r.activePort
	r.mu.RUnlock()
	if activePort <= 0 {
		if err := setLinuxSystemProxy(proxyPort, false); err != nil {
			return r.rollbackReloadProxy(paths.configPath, proxyPort, err)
		}
		r.setSystemProxyState(false, 0)
		return changed, nil
	}
	if activePort == proxyPort {
		return changed, nil
	}
	if err := setLinuxSystemProxy(activePort, true); err != nil {
		return r.rollbackReloadProxy(paths.configPath, proxyPort, err)
	}
	r.setSystemProxyState(true, activePort)
	return changed, nil
}

func (r *tuiServiceRuntime) reloadAndRepairFLCUnlocked(
	configPath,
	expectedSHA256 string,
) (bool, error) {
	changed, err := r.reloadUnlocked(configPath, expectedSHA256)
	if err != nil {
		return changed, err
	}
	repaired, err := r.repairFLCOutbound()
	return changed || repaired, err
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
	tunScope := r.tunScope
	tunEnabled := r.tunEnabled
	tunLease := r.tunLease
	activePort := r.activePort
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
	settings := loadTUIConfiguredSettings(configPath, true)
	if settings == nil {
		return false, errors.New("could not load active settings")
	}
	actualConfigPath := configPath
	targetPort := settings.MixedPort
	tunFD := 0
	if mode == tuiSilentMode {
		logicalPaths := paths
		logicalPaths.configPath = configPath
		var err error
		actualConfigPath, err = writeTUISilentRuntimeConfig(logicalPaths, flc)
		if err != nil {
			return false, err
		}
		if flc.proxyURL() == "" {
			targetPort = 0
		} else {
			targetPort = flc.Port
		}
	} else {
		if targetPort > 0 && targetPort != activePort {
			var err error
			targetPort, err = chooseTUIProxyPort(targetPort)
			if err != nil {
				return false, err
			}
		}
		if tunEnabled {
			var err error
			tunFD, err = tunLease.duplicateFD()
			if err != nil {
				return false, err
			}
		}
		logicalPaths := paths
		logicalPaths.configPath = configPath
		var err error
		actualConfigPath, err = writeTUIManagedRuntimeConfig(
			logicalPaths,
			mode,
			targetPort,
			tunEnabled,
			tunScope,
			tunFD,
		)
		if err != nil {
			if tunFD > 0 {
				_ = syscall.Close(tunFD)
			}
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
		if tunFD > 0 {
			_ = syscall.Close(tunFD)
		}
		if actualConfigPath != configPath {
			_ = os.Remove(actualConfigPath)
		}
		return false, err
	}
	if running {
		if err := validateTUIProxyPortTransition(activePort, targetPort); err != nil {
			validationErr := err
			if filepath.Clean(actualConfigPath) != filepath.Clean(previousActualPath) {
				_, rollbackErr := reloadTUIActualConfig(
					paths.homeDir,
					actualConfigPath,
					previousActualPath,
					r.testURL,
					r.coreSocket,
					reloaded,
					running,
				)
				if rollbackErr == nil {
					rollbackErr = validateTUIProxyPortTransition(targetPort, activePort)
				}
				if rollbackErr != nil {
					return false, fmt.Errorf(
						"validate proxy port switch: %v; Core rollback failed: %w",
						validationErr,
						rollbackErr,
					)
				}
			}
			if actualConfigPath != configPath {
				_ = os.Remove(actualConfigPath)
			}
			return false, fmt.Errorf("validate proxy port switch: %w", validationErr)
		}
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
			if actualConfigPath != configPath {
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
	r.mu.Lock()
	r.paths.configPath = configPath
	r.setupParams = append([]byte(nil), reloaded...)
	r.actualConfigPath = actualConfigPath
	r.runtimePort = targetPort
	if settings != nil {
		r.configuredPort = settings.MixedPort
		if mode != tuiSilentMode {
			r.trafficMode = strings.ToLower(settings.Mode)
		}
	}
	if running {
		r.activePort = targetPort
	}
	r.mu.Unlock()
	if mode != tuiSilentMode && settings != nil {
		_ = rememberTUITrafficMode(paths.homeDir, settings.Mode)
	}
	if previousActualPath != actualConfigPath &&
		(strings.Contains(filepath.Base(previousActualPath), tuiSilentRuntimeConfigPrefix) ||
			strings.Contains(filepath.Base(previousActualPath), tuiManagedRuntimeConfigPrefix)) {
		_ = os.Remove(previousActualPath)
	}
	cleanupTUISilentRuntimeConfigs(paths.homeDir, actualConfigPath)
	return true, nil
}
