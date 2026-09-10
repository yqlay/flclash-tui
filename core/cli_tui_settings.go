//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

func (m *tuiModel) persistStagedTUISettings() {
	if m.stagedSettings == nil {
		return
	}
	if m.service != nil {
		m.settingsDirty = true
		m.snapshot.Status += "; pending backend commit"
		return
	}
	m.settingsDirty = true
	m.snapshot.Status += "; not saved without Backend"
}

func applyTUIOperationSetting(
	state *tuiOperationState,
	service *tuiServiceClient,
	client controllerClient,
	key tuiKey,
) {
	if service == nil {
		state.snapshot.Status = "Changing shared settings requires the managed backend"
		return
	}
	if key == tuiKeyTun {
		if !prepareTUIBackendRevision(state, service) {
			return
		}
		scope := state.snapshot.Settings.TunScope
		if scope == "" {
			scope = tuiTunScopeUser
		}
		status, err := service.setTun(
			!state.snapshot.Settings.TunEnabled,
			scope,
			state.backendRevision,
		)
		if err != nil {
			state.snapshot.Status = "TUN update failed: " + err.Error()
			return
		}
		applyTUIOperationServiceStatus(state, status)
		state.snapshot.Status = "TUN " + strings.ToUpper(status.TunScope) + " " + strings.ToUpper(status.TunState)
		state.networkChanged = true
		return
	}
	settings := state.snapshot.Settings
	if !changeTUISettingsValue(&settings, key) {
		state.snapshot.Status = "Setting is already at its limit"
		return
	}
	commitTUIOperationSettings(state, service, client, settings)
}

func applyTUITunScope(state *tuiOperationState, service *tuiServiceClient) {
	if service == nil {
		state.snapshot.Status = "Changing TUN scope requires the managed Backend"
		return
	}
	if state.snapshot.Settings.TunEnabled {
		state.snapshot.Status = "Turn TUN off before changing its scope"
		return
	}
	if !prepareTUIBackendRevision(state, service) {
		return
	}
	scope := tuiEffectiveTunScope(state.snapshot.Settings.TunScope)
	if scope == tuiTunScopeUser {
		scope = tuiTunScopeSystem
	} else {
		scope = tuiTunScopeUser
	}
	status, err := service.setTun(false, scope, state.backendRevision)
	if err != nil {
		state.snapshot.Status = "TUN scope update failed: " + err.Error()
		return
	}
	applyTUIOperationServiceStatus(state, status)
	state.snapshot.Status = "TUN scope " + strings.ToUpper(status.TunScope)
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
	profileSettings, err := tuiProfileSettingsForCommit(state, settings)
	if err != nil {
		state.snapshot.Status = "Settings commit failed: " + err.Error()
		return
	}
	status, err := service.applySettings(profileSettings, state.backendRevision)
	if err != nil {
		state.snapshot.Status = "Settings commit failed: " + err.Error()
		return
	}
	state.snapshot.Settings = profileSettings
	state.settingsDirty = false
	state.stagedSettings = nil
	state.pendingMixedPort = nil
	state.snapshot.Status = fmt.Sprintf(
		"Settings committed at revision %d",
		status.Revision,
	)
	refreshTUISnapshot(&state.snapshot, client)
	applyTUIOperationServiceStatus(state, status)
	state.networkChanged = true
}

func tuiProfileSettingsForCommit(
	state *tuiOperationState,
	settings tuiSettings,
) (tuiSettings, error) {
	configured := loadTUIConfiguredSettings(state.paths.ConfigPath, true)
	if configured == nil || strings.EqualFold(configured.Mode, tuiSilentMode) {
		return tuiSettings{}, errors.New(
			"could not load native mode and TUN settings from the active YAML profile",
		)
	}
	// Mode and TUN are Backend-owned controls. The snapshot contains their
	// effective display state (including FlClash-only silent mode and system
	// TUN), which must never be serialized back into the shared YAML by an
	// unrelated settings or port edit.
	settings.Mode = configured.Mode
	settings.TunEnabled = configured.TunEnabled
	return settings, nil
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
	applyTUIOperationServiceStatus(state, status)
	return true
}

func applyTUIOperationServiceStatus(
	state *tuiOperationState,
	status tuiServiceStatus,
) {
	state.backendRevision = status.Revision
	state.coreRunning = status.Running
	if status.ConfigPath != "" {
		state.paths.ConfigPath = status.ConfigPath
	}
	state.snapshot.Settings.SystemProxy = status.SystemProxy
	if status.Mode != "" {
		state.snapshot.Settings.Mode = status.Mode
	}
	state.snapshot.Settings.MixedPort = status.ConfiguredProxyPort
	state.snapshot.ConfiguredProxyPort = status.ConfiguredProxyPort
	state.snapshot.ActiveProxyPort = status.ActiveProxyPort
	if status.TunState != "" {
		state.snapshot.Settings.TunEnabled = status.TunState == "on"
	}
	if status.TunScope != "" {
		state.snapshot.Settings.TunScope = status.TunScope
	}
	if status.Mode == tuiSilentMode {
		state.snapshot.Settings.TunEnabled = false
	}
	state.snapshot.FLCEnabled = status.FLCEnabled
	state.snapshot.FLCOutbound = status.FLCOutbound
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
	settings := loadTUIConfiguredSettings(state.paths.ConfigPath, true)
	if settings == nil {
		state.snapshot.Status += "; could not reload settings from YAML"
		return
	}
	backendMode := state.snapshot.Settings.Mode
	backendTunEnabled := state.snapshot.Settings.TunEnabled
	backendTunScope := state.snapshot.Settings.TunScope
	settings.SystemProxy = state.snapshot.Settings.SystemProxy
	state.stagedSettings = cloneTUISettings(settings)
	state.snapshot.Settings = *settings
	if strings.EqualFold(backendMode, tuiSilentMode) {
		state.snapshot.Settings.Mode = tuiSilentMode
	}
	state.snapshot.Settings.TunEnabled = backendTunEnabled
	state.snapshot.Settings.TunScope = backendTunScope
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
				state.paths.ConfigPath,
				state.backendRevision,
			)
		} else {
			status, err = service.reloadAtRevisionWithDigest(
				state.paths.ConfigPath,
				state.backendRevision,
				expectedSHA256,
			)
		}
		if err != nil {
			return err
		}
		applyTUIOperationServiceStatus(state, status)
		state.snapshot.GroupOrder = loadTUIProxyGroupOrder(state.paths.ConfigPath)
		return nil
	}
	if ownsCore {
		if message := cliHub.SetupConfig(state.setupParams); message != "" {
			return errors.New(message)
		}
		state.snapshot.GroupOrder = loadTUIProxyGroupOrder(state.paths.ConfigPath)
		return nil
	}
	data, err := os.ReadFile(state.paths.ConfigPath)
	if err != nil {
		return err
	}
	if message := validateConfigBytes(data); message != "" {
		return errors.New(message)
	}
	if err := client.reloadConfigPayload(data); err != nil {
		return err
	}
	state.snapshot.GroupOrder = loadTUIProxyGroupOrder(state.paths.ConfigPath)
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
	updated, err := applyTUISettingsToConfig(data, settings)
	if err != nil {
		return err
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

func applyTUISettingsToConfig(
	data []byte,
	settings tuiSettings,
) ([]byte, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, fmt.Errorf("parse YAML: %w", err)
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("configuration root must be a YAML mapping")
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
		return nil, errors.New("TUN configuration must be a YAML mapping")
	}
	setTUIYAMLScalar(tun, "enable", strconv.FormatBool(settings.TunEnabled), "!!bool")

	updated, err := yaml.Marshal(&document)
	if err != nil {
		return nil, fmt.Errorf("encode YAML: %w", err)
	}
	if message := validateConfigBytes(updated); message != "" {
		return nil, errors.New(message)
	}
	return updated, nil
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
	return cliHub.UpdateConfig(data)
}

func startTUIManagedCore(
	state *tuiOperationState,
	service *tuiServiceClient,
) bool {
	if state.coreRunning {
		return true
	}
	mode := strings.ToLower(state.snapshot.Settings.Mode)
	if service == nil {
		if mode == tuiSilentMode {
			state.snapshot.Status = "Silent mode requires the managed backend"
			return false
		}
		if state.snapshot.Settings.MixedPort <= 0 {
			state.snapshot.Status = "Choose a positive Proxy port before starting"
			return false
		}
		if err := ensureTUIProxyPortFree(state.snapshot.Settings.MixedPort); err != nil {
			state.snapshot.Status = "Cannot start: " + err.Error()
			return false
		}
	}
	port := state.snapshot.Settings.MixedPort
	if state.stagedSettings != nil && state.settingsDirty {
		if service != nil {
			if !prepareTUIBackendRevision(state, service) {
				return false
			}
			profileSettings, profileErr := tuiProfileSettingsForCommit(
				state,
				*state.stagedSettings,
			)
			if profileErr != nil {
				state.snapshot.Status = "Cannot commit staged settings: " + profileErr.Error()
				return false
			}
			status, err := service.applySettings(
				profileSettings,
				state.backendRevision,
			)
			if err != nil {
				state.snapshot.Status = "Cannot commit staged settings: " + err.Error()
				return false
			}
			applyTUIOperationServiceStatus(state, status)
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
		applyTUIOperationServiceStatus(state, status)
	} else if !cliHub.StartListener() {
		state.snapshot.Status = "Cannot start core listeners"
		return false
	}
	state.coreRunning = true
	state.pendingMixedPort = nil
	state.stagedSettings = nil
	state.settingsDirty = false
	if mode == tuiSilentMode {
		if outbound := strings.TrimSpace(state.snapshot.FLCOutbound); outbound != "" {
			state.snapshot.Status = "Core started in silent mode · FLC " + outbound + " · only flc uses the private listener"
		} else {
			state.snapshot.Status = "Core started in silent mode; only flc uses the private listener"
		}
	} else {
		state.snapshot.Status = fmt.Sprintf("Core listeners started on port %d", port)
	}
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
		applyTUIOperationServiceStatus(state, status)
	} else if !cliHub.StopListener() {
		state.snapshot.Status = "Cannot stop core listeners"
		return false
	}
	state.coreRunning = false
	state.snapshot.Status = "Core listeners stopped"
	syncStoppedTUISettings(state)
	return true
}
