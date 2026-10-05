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
		if m.settingsDraft == nil {
			m.settingsDraft = m.settingsDraftBase()
		}
		m.settingsDirty = true
		m.snapshot.appendStatus(newTUIMessage("ui.e029c971c1f2"))
		return
	}
	m.settingsDirty = true
	m.snapshot.appendStatus(newTUIMessage("ui.2bc8b93fe0a6"))
}

func applyTUIOperationSetting(
	state *tuiOperationState,
	service *tuiServiceClient,
	client controllerClient,
	key tuiKey,
) {
	if service == nil {
		state.snapshot.setStatus(newTUIMessage("ui.1a3c83b35cc5"))
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
		status, err := state.service.setTun(
			!state.snapshot.Settings.TunEnabled,
			scope,
			state.backendRevision,
		)
		if err != nil {
			state.snapshot.setStatus(newTUIMessage("ui.5d86b1ec6313", err.Error()))
			return
		}
		applyTUIOperationServiceStatus(state, status)
		state.snapshot.setStatus(newTUIMessage("ui.41842b752df0", strings.ToUpper(status.TunScope), strings.ToUpper(status.TunState)))
		state.networkChanged = true
		return
	}
	settings := state.snapshot.Settings
	if !changeTUISettingsValue(&settings, key) {
		state.snapshot.setStatus(newTUIMessage("ui.7c247b59e634"))
		return
	}
	commitTUIOperationSettings(state, service, client, settings)
}

func applyTUITunScope(state *tuiOperationState, service *tuiServiceClient) {
	if service == nil {
		state.snapshot.setStatus(newTUIMessage("ui.803f9a0ac096"))
		return
	}
	if state.snapshot.Settings.TunEnabled {
		state.snapshot.setStatus(newTUIMessage("ui.b58d11c6d993"))
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
	status, err := state.service.setTun(false, scope, state.backendRevision)
	if err != nil {
		state.snapshot.setStatus(newTUIMessage("ui.b9d29efd3c14", err.Error()))
		return
	}
	applyTUIOperationServiceStatus(state, status)
	state.snapshot.setStatus(newTUIMessage("ui.f4894f303d4e", strings.ToUpper(status.TunScope)))
}

func commitTUIOperationSettings(
	state *tuiOperationState,
	service *tuiServiceClient,
	client controllerClient,
	settings tuiSettings,
) {
	if !prepareTUIBackendRevision(state, service) || !validateTUISettingsDraft(state) {
		return
	}
	profileSettings, err := tuiProfileSettingsForCommit(state, settings)
	if err != nil {
		state.snapshot.setStatus(newTUIMessage("ui.13f4425a0649", err.Error()))
		return
	}
	status, err := state.service.applySettings(profileSettings, state.backendRevision)
	if err != nil {
		reportTUISettingsSaveError(state, err, "ui.13f4425a0649")
		return
	}
	state.snapshot.Settings = profileSettings
	clearTUISettingsDraft(state)
	state.snapshot.setStatus(newTUIMessage("ui.09838b9053bc", status.Revision))

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
	if state.service == nil {
		state.service = service.forInstance(state.backendInstanceID)
	}
	if state.backendRevision > 0 {
		return true
	}
	status, err := service.status()
	if err != nil {
		state.snapshot.setStatus(newTUIMessage("ui.d4946a9140c6", err.Error()))
		return false
	}
	if state.backendInstanceID != "" && state.backendInstanceID != status.InstanceID {
		state.snapshot.setStatus(newTUIMessage("ui.d4946a9140c6", "backend instance changed; refresh and retry"))
		return false
	}
	applyTUIOperationServiceStatus(state, status)
	state.service = service.forInstance(status.InstanceID)
	return true
}

func applyTUIOperationServiceStatus(
	state *tuiOperationState,
	status tuiServiceStatus,
) {
	state.backendRevision = status.Revision
	state.backendInstanceID = status.InstanceID
	state.coreRunning = status.Running
	if status.ConfigPath != "" {
		state.paths.ConfigPath = status.ConfigPath
	}
	applyTUIBackendDisplay(&state.snapshot, status)
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
	if state.coreRunning || state.settingsDirty {
		return
	}
	settings := loadTUIConfiguredSettings(state.paths.ConfigPath, true)
	if settings == nil {
		state.snapshot.appendStatus(newTUIMessage("ui.356067a4895f"))
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
	state.settingsDraft = nil
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
			status, err = state.service.reloadAtRevision(
				state.paths.ConfigPath,
				state.backendRevision,
			)
		} else {
			status, err = state.service.reloadAtRevisionWithDigest(
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
			state.snapshot.setStatus(newTUIMessage("ui.1b69b68b5801"))
			return false
		}
		if state.snapshot.Settings.MixedPort <= 0 {
			state.snapshot.setStatus(newTUIMessage("ui.6da98ca12dfd"))
			return false
		}
		if err := ensureTUIProxyPortFree(state.snapshot.Settings.MixedPort); err != nil {
			state.snapshot.setStatus(newTUIMessage("ui.9ec86520b9dc", err.Error()))
			return false
		}
	}
	port := state.snapshot.Settings.MixedPort
	if state.stagedSettings != nil && state.settingsDirty {
		if service != nil {
			if !prepareTUIBackendRevision(state, service) || !validateTUISettingsDraft(state) {
				return false
			}
			profileSettings, profileErr := tuiProfileSettingsForCommit(
				state,
				*state.stagedSettings,
			)
			if profileErr != nil {
				state.snapshot.setStatus(newTUIMessage("ui.3b3a7b6817d8", profileErr.Error()))
				return false
			}
			status, err := state.service.applySettings(
				profileSettings,
				state.backendRevision,
			)
			if err != nil {
				reportTUISettingsSaveError(state, err, "ui.3b3a7b6817d8")
				return false
			}
			applyTUIOperationServiceStatus(state, status)
			clearTUISettingsDraft(state)
		}
		if service == nil {
			if message := stageTUICoreSettings(*state.stagedSettings); message != "" {
				state.snapshot.setStatus(newTUIMessage("ui.d660ad14fd2f", message))
				return false
			}
		}
	}
	if service != nil {
		if !prepareTUIBackendRevision(state, service) {
			return false
		}
		status, err := state.service.startAtRevision(state.backendRevision)
		if err != nil {
			state.snapshot.setStatus(newTUIMessage("ui.1e770c3711f0", err.Error()))
			return false
		}
		applyTUIOperationServiceStatus(state, status)
	} else if !cliHub.StartListener() {
		state.snapshot.setStatus(newTUIMessage("ui.16bf361c146f"))
		return false
	}
	state.coreRunning = true
	clearTUISettingsDraft(state)
	if mode == tuiSilentMode {
		if outbound := strings.TrimSpace(state.snapshot.FLCOutbound); outbound != "" {
			state.snapshot.setStatus(newTUIMessage("ui.6c3f66b449da", outbound))
		} else {
			state.snapshot.setStatus(newTUIMessage("ui.920f28d000fb"))
		}
	} else {
		state.snapshot.setStatus(newTUIMessage("ui.54b76d0e32bc", port))
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
		status, err := state.service.stopAtRevision(state.backendRevision)
		if err != nil {
			state.snapshot.setStatus(newTUIMessage("ui.a580272ceef6", err.Error()))
			return false
		}
		applyTUIOperationServiceStatus(state, status)
	} else if !cliHub.StopListener() {
		state.snapshot.setStatus(newTUIMessage("ui.ee456006f3a7"))
		return false
	}
	state.coreRunning = false
	state.snapshot.setStatus(newTUIMessage("ui.d7a2587ef8f5"))
	syncStoppedTUISettings(state)
	return true
}
