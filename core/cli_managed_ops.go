//go:build linux && !cgo && cli

package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Dashboard rows and the matching flclash commands call these operations.
// The command prints the result; the TUI keeps its own status line.

func managedListenerPort(reachable bool, mode string, activePort, proxyPort, configuredPort int) int {
	if !reachable || strings.EqualFold(mode, tuiSilentMode) {
		return 0
	}
	if activePort > 0 {
		return activePort
	}
	if proxyPort > 0 {
		return proxyPort
	}
	if configuredPort > 0 {
		return configuredPort
	}
	return 0
}

func detectManagedNetwork(
	client *tuiServiceClient,
	coreRunning bool,
	probeWhenStopped bool,
	mode string,
	activePort, proxyPort, configuredPort int,
	flcOutbound string,
) tuiNetworkInfo {
	if strings.EqualFold(mode, tuiSilentMode) {
		if !coreRunning {
			return tuiNetworkInfo{
				IntranetIP: detectTUIIntranetIP(),
				Route:      "SILENT · Core stopped",
				CheckedAt:  time.Now(),
			}
		}
		if client == nil {
			return tuiNetworkInfo{
				IntranetIP: detectTUIIntranetIP(),
				Route:      "SILENT unavailable",
				Error:      "silent network detection requires the managed Backend",
				CheckedAt:  time.Now(),
			}
		}
		status, err := client.flcProxy()
		if err != nil {
			return tuiNetworkInfo{
				IntranetIP: detectTUIIntranetIP(),
				Route:      "SILENT unavailable",
				Error:      err.Error(),
				CheckedAt:  time.Now(),
			}
		}
		outbound := status.FLCOutbound
		if strings.TrimSpace(outbound) == "" {
			outbound = flcOutbound
		}
		return detectTUINetworkRouteForCommand(status.FLCProxyURL, "SILENT · "+cliDisplayValue(outbound))
	}
	port := managedListenerPort(coreRunning || probeWhenStopped, mode, activePort, proxyPort, configuredPort)
	proxyURL := tuiLoopbackProxyURL(port)
	label := "DIRECT"
	if port > 0 {
		label = fmt.Sprintf("PROXY 127.0.0.1:%d", port)
	}
	return detectTUINetworkRouteForCommand(proxyURL, label)
}

func managedRouteDelay(
	client *tuiServiceClient,
	coreRunning bool,
	probeWhenStopped bool,
	mode string,
	activePort, proxyPort, configuredPort int,
) (tuiDelayResult, error) {
	port := managedListenerPort(coreRunning || probeWhenStopped, mode, activePort, proxyPort, configuredPort)
	if port <= 0 {
		return tuiDelayResult{}, errors.New("normal Proxy port is not active; use `flclash flc test` in silent mode")
	}
	if client == nil {
		return tuiDelayResult{}, errors.New("Dashboard delay test requires the managed backend")
	}
	return testManagedRouteDelayForCommand(client, port, defaultCLITestURL)
}

func managedRouteSpeed(
	client *tuiServiceClient,
	coreRunning bool,
	probeWhenStopped bool,
	mode string,
	activePort, proxyPort, configuredPort int,
) (tuiSpeedResult, error) {
	port := managedListenerPort(coreRunning || probeWhenStopped, mode, activePort, proxyPort, configuredPort)
	if port <= 0 {
		return tuiSpeedResult{}, errors.New("normal Proxy port is not active; use `flclash flc test` in silent mode")
	}
	if client == nil {
		return tuiSpeedResult{}, errors.New("Dashboard speed test requires the managed backend")
	}
	return testManagedRouteSpeedForCommand(client, port)
}

func applyManagedSystemProxy(
	client *tuiServiceClient,
	status tuiServiceStatus,
	enabled bool,
) (tuiServiceStatus, bool, error) {
	if enabled && status.Mode == tuiSilentMode {
		return status, false, errors.New("System proxy cannot be enabled in silent mode; switch mode first")
	}
	autoStarted := false
	if enabled && !status.Running {
		next, err := client.startAtRevision(status.Revision)
		if err != nil {
			return status, false, fmt.Errorf("start Core for System proxy: %w", err)
		}
		status = next
		autoStarted = true
	}
	next, err := client.setSystemProxy(enabled, status.Revision)
	if err != nil {
		if autoStarted {
			rolled, rollbackErr := client.stopAtRevision(status.Revision)
			if rollbackErr != nil {
				return status, true, fmt.Errorf(
					"update System proxy: %v; automatic Core rollback failed: %w",
					err,
					rollbackErr,
				)
			}
			status = rolled
		}
		return status, autoStarted, fmt.Errorf("update System proxy: %w", err)
	}
	return next, autoStarted, nil
}

func applyManagedTun(
	client *tuiServiceClient,
	enabled bool,
	scope string,
	revision uint64,
) (tuiServiceStatus, error) {
	if scope == "" {
		scope = tuiTunScopeUser
	}
	return client.setTun(enabled, scope, revision)
}

func applyManagedMode(
	client *tuiServiceClient,
	mode string,
	revision uint64,
) (tuiServiceStatus, error) {
	return client.setMode(mode, revision)
}

func applyManagedMixedPort(
	client *tuiServiceClient,
	configPath string,
	port int,
	revision uint64,
) (tuiServiceStatus, error) {
	settings := loadTUIConfiguredSettings(configPath, true)
	if settings == nil {
		return tuiServiceStatus{}, errors.New("could not load active settings")
	}
	settings.MixedPort = port
	return client.applySettings(*settings, revision)
}

func applyManagedSetting(
	client *tuiServiceClient,
	configPath, key, value string,
	revision uint64,
	tunRequested bool,
) (tuiServiceStatus, error) {
	switch strings.ToLower(key) {
	case "tun-scope":
		if tunRequested {
			return tuiServiceStatus{}, errors.New("Turn TUN off before changing its scope")
		}
		scope, err := normalizeTUITunScope(value)
		if err != nil {
			return tuiServiceStatus{}, err
		}
		return applyManagedTunForCommand(client, false, scope, revision)
	}
	settings := loadTUIConfiguredSettings(configPath, true)
	if settings == nil {
		return tuiServiceStatus{}, errors.New("could not load active settings")
	}
	switch strings.ToLower(key) {
	case "allow-lan":
		enabled, err := cliOnOffValue(value)
		if err != nil {
			return tuiServiceStatus{}, err
		}
		settings.AllowLAN = enabled
	case "ipv6":
		enabled, err := cliOnOffValue(value)
		if err != nil {
			return tuiServiceStatus{}, err
		}
		settings.IPv6 = enabled
	case "unified-delay":
		enabled, err := cliOnOffValue(value)
		if err != nil {
			return tuiServiceStatus{}, err
		}
		settings.UnifiedDelay = enabled
	case "tcp-concurrent":
		enabled, err := cliOnOffValue(value)
		if err != nil {
			return tuiServiceStatus{}, err
		}
		settings.TCPConcurrent = enabled
	case "log-level":
		level, err := cliLogLevelValue(value)
		if err != nil {
			return tuiServiceStatus{}, err
		}
		settings.LogLevel = level
	default:
		return tuiServiceStatus{}, fmt.Errorf("unknown setting %q", key)
	}
	return client.applySettings(*settings, revision)
}

func activateManagedProfile(
	client *tuiServiceClient,
	path string,
	revision uint64,
) (tuiServiceStatus, error) {
	if message := cliHub.ValidateConfig(path); message != "" {
		return tuiServiceStatus{}, errors.New(message)
	}
	digest, err := tuiFileSHA256(path)
	if err != nil {
		return tuiServiceStatus{}, err
	}
	return client.reloadAtRevisionWithDigest(path, revision, digest)
}

func selectManagedProxy(
	client *tuiServiceClient,
	group, node string,
	revision uint64,
) (tuiServiceStatus, error) {
	return client.selectProxy(group, node, revision)
}

func tuiSettingsAssignment(key tuiKey, settings tuiSettings) (string, string, bool) {
	switch key {
	case tuiKeyAllowLAN:
		return "allow-lan", cliOnOffWord(settings.AllowLAN), true
	case tuiKeyIPv6:
		return "ipv6", cliOnOffWord(settings.IPv6), true
	case tuiKeyUnifiedDelay:
		return "unified-delay", cliOnOffWord(settings.UnifiedDelay), true
	case tuiKeyTCPConcurrent:
		return "tcp-concurrent", cliOnOffWord(settings.TCPConcurrent), true
	case tuiKeyLogLevel:
		return "log-level", strings.ToLower(settings.LogLevel), true
	default:
		return "", "", false
	}
}

var (
	detectTUINetworkRouteForCommand = detectTUINetworkRoute
	detectManagedNetworkForCommand  = detectManagedNetwork
	testManagedRouteDelayForCommand = func(client *tuiServiceClient, port int, testURL string) (tuiDelayResult, error) {
		return client.testRouteDelay(port, testURL)
	}
	testManagedRouteSpeedForCommand = func(client *tuiServiceClient, port int) (tuiSpeedResult, error) {
		return client.testRouteSpeed(port)
	}
	managedRouteDelayForCommand               = managedRouteDelay
	managedRouteSpeedForCommand               = managedRouteSpeed
	applyManagedSystemProxyForCommand         = applyManagedSystemProxy
	applyManagedTunForCommand                 = applyManagedTun
	applyManagedModeForCommand                = applyManagedMode
	applyManagedMixedPortForCommand           = applyManagedMixedPort
	applyManagedSettingForCommand             = applyManagedSetting
	activateManagedProfileForCommand          = activateManagedProfile
	selectManagedProxyForCommand              = selectManagedProxy
	updateGeoForCommand                       = func(client controllerClient) error { return client.updateGeo() }
	connectCLISSHProfileWithContextForCommand = connectCLISSHProfileWithCredentialsContext
	disconnectCLISSHProfileForCommand         = disconnectCLISSHProfile
	attachCLISSHProfileContextForCommand      = attachCLISSHProfileContext
)
