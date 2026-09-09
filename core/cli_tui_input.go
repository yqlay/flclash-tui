//go:build linux && !cgo && cli

package main

import (
	"errors"
	"fmt"
	"io"
	nethttp "net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

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
	} else if mode == tuiInputHistorySearch {
		m.inputValue = []rune(m.snapshot.HistoryQuery)
		m.inputCursor = len(m.inputValue)
		m.inputSelectAll = true
	} else if mode == tuiInputConnectionsSearch {
		m.inputValue = []rune(m.snapshot.ConnectionsQuery)
		m.inputCursor = len(m.inputValue)
		m.inputSelectAll = true
	} else if mode == tuiInputLogsSearch {
		m.inputValue = []rune(m.snapshot.LogsQuery)
		m.inputCursor = len(m.inputValue)
		m.inputSelectAll = true
	}
}

func (m *tuiModel) beginModeSelection() {
	m.modeSelectionOpen = true
	m.selectedMode = findTUIString(
		tuiTrafficModes,
		strings.ToLower(m.snapshot.Settings.Mode),
	)
	if m.selectedMode < 0 {
		m.selectedMode = 0
	}
	m.snapshot.Status = "Select an outbound mode"
}

func (m *tuiModel) handleModeSelection(message tea.KeyMsg) tea.Cmd {
	key, ok := tuiKeyFromTea(message)
	if !ok {
		return nil
	}
	switch key {
	case tuiKeyUp:
		m.selectedMode = wrapTUIIndex(
			m.selectedMode,
			-1,
			len(tuiTrafficModes),
		)
	case tuiKeyDown:
		m.selectedMode = wrapTUIIndex(
			m.selectedMode,
			1,
			len(tuiTrafficModes),
		)
	case tuiKeySelect:
		mode := tuiTrafficModes[m.selectedMode]
		m.modeSelectionOpen = false
		return m.changeMode(mode)
	case tuiKeyBack:
		m.modeSelectionOpen = false
		m.snapshot.Status = "Mode selection cancelled"
	case tuiKeyQuit, tuiKeyInterrupt:
		m.modeSelectionOpen = false
		return m.handleKey(key)
	}
	return nil
}

func (m *tuiModel) changeMode(mode string) tea.Cmd {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if strings.EqualFold(mode, m.snapshot.Settings.Mode) {
		m.snapshot.Status = "Mode unchanged: " + mode
		return nil
	}
	if m.service == nil {
		if !m.ownsCore || m.coreRunning {
			m.snapshot.Status = "Mode changes require the managed backend"
			return nil
		}
		if mode == tuiSilentMode {
			m.snapshot.Status = "Silent mode requires the managed backend"
			return nil
		}
		m.stageTUIMode(mode)
		return nil
	}
	service := m.service
	command := m.startOperation(func(state *tuiOperationState) {
		if !prepareTUIBackendRevision(state, service) {
			return
		}
		status, err := service.setMode(mode, state.backendRevision)
		if err != nil {
			state.snapshot.Status = "Mode change failed: " + err.Error()
			return
		}
		applyTUIOperationServiceStatus(state, status)
		if status.Mode == tuiSilentMode {
			if outbound := strings.TrimSpace(status.FLCOutbound); outbound != "" {
				state.snapshot.Status = "Mode silent · flc follows " + outbound + " · pick nodes in Proxies"
			} else {
				state.snapshot.Status = "Mode silent · pick a node in Proxies for flc"
			}
		} else {
			state.snapshot.Status = "Mode changed to " + status.Mode
		}
		state.networkChanged = true
	})
	if command != nil {
		m.snapshot.Status = "Changing mode to " + mode + "..."
	}
	return command
}

func (m *tuiModel) stageTUIMode(mode string) {
	m.snapshot.Settings.Mode = mode
	port := m.snapshot.Settings.MixedPort
	m.pendingMixedPort = &port
	m.stagedSettings = cloneTUISettings(&m.snapshot.Settings)
	m.settingsDirty = true
	m.snapshot.Status = "Mode " + mode +
		" staged; enable System proxy or start Core to apply"
	m.persistStagedTUISettings()
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
	if m.service == nil {
		m.snapshot.Status = "Subscription updates require the managed backend"
		return nil
	}
	return m.startOperation(func(state *tuiOperationState) {
		isActive := filepath.Clean(profilePath) == filepath.Clean(state.paths.configPath)
		previous, err := os.ReadFile(profilePath)
		if err != nil {
			state.snapshot.Status = "Subscription update failed: " + err.Error()
			return
		}
		updated, err := fetchTUISubscription(sourceURL)
		if err != nil {
			state.snapshot.Status = "Subscription update failed: " + err.Error()
			return
		}
		if previousSettings := loadTUIConfiguredSettings(profilePath, true); previousSettings != nil {
			updated, err = applyTUISettingsToConfig(updated, *previousSettings)
			if err != nil {
				state.snapshot.Status = "Subscription update failed: preserve local settings: " +
					err.Error()
				return
			}
		}
		if !prepareTUIBackendRevision(state, m.service) {
			return
		}
		status, err := m.service.putProfile(
			profilePath,
			updated,
			tuiBytesSHA256(previous),
			false,
			&sourceURL,
			state.backendRevision,
		)
		if err != nil {
			state.snapshot.Status = "Subscription update failed: " + err.Error()
			return
		}
		applyTUIOperationServiceStatus(state, status)
		if isActive {
			state.snapshot.Status = "Subscription refreshed and hot-reloaded: " +
				filepath.Base(profilePath)
			syncStoppedTUISettings(state)
			state.networkChanged = true
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
		m.resetInput()
		return m.handleKey(tuiKeyInterrupt)
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
	case tuiInputHistorySearch:
		m.snapshot.HistoryQuery = value
		m.snapshot.SelectedRequest = firstTUIRequestMatch(m.snapshot)
		m.snapshot.HistoryDetailOpen = false
		m.snapshot.Status = "History search updated"
		return nil
	case tuiInputConnectionsSearch:
		m.snapshot.ConnectionsQuery = value
		m.snapshot.SelectedConnection = firstTUIConnectionMatch(m.snapshot)
		m.snapshot.ConnectionsDetailOpen = false
		m.snapshot.Status = "Connections search updated"
		return nil
	case tuiInputLogsSearch:
		m.snapshot.LogsQuery = value
		m.snapshot.SelectedLog = firstTUILogMatch(m.snapshot)
		m.snapshot.LogDetailOpen = false
		m.snapshot.Status = "Log search updated"
		return nil
	case tuiInputMixedPort:
		port, err := strconv.Atoi(value)
		if err != nil || port < 0 || port > 65535 {
			m.snapshot.Status = "Port change failed: Proxy port must be a number from 0 to 65535"
			return nil
		}
		if port == m.snapshot.Settings.MixedPort {
			m.snapshot.Status = "Port unchanged"
			return nil
		}
		if m.service == nil && m.ownsCore && !m.coreRunning {
			m.pendingMixedPort = &port
			m.snapshot.Settings.MixedPort = port
			m.stagedSettings = cloneTUISettings(&m.snapshot.Settings)
			m.settingsDirty = true
			m.snapshot.Status = fmt.Sprintf(
				"Proxy port %d staged; enable System proxy or start Core to apply",
				port,
			)
			m.persistStagedTUISettings()
			return nil
		}
		if m.service == nil {
			m.snapshot.Status = "Changing the proxy port requires the managed backend"
			return nil
		}
		return m.startOperation(func(state *tuiOperationState) {
			settings := state.snapshot.Settings
			settings.MixedPort = port
			commitTUIOperationSettings(state, m.service, m.client, settings)
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
		if m.service == nil {
			m.snapshot.Status = "Subscription import requires the managed backend"
			return nil
		}
		return m.startOperation(func(state *tuiOperationState) {
			payload, err := fetchTUISubscriptionDetails(value)
			if err != nil {
				state.snapshot.Status = "Add profile failed: " + err.Error()
				return
			}
			if !prepareTUIBackendRevision(state, m.service) {
				return
			}
			path, err := tuiSubscriptionImportPath(state.paths.homeDir, payload)
			if err != nil {
				state.snapshot.Status = "Add profile failed: " + err.Error()
				return
			}
			status, err := m.service.putProfile(
				path,
				payload.Data,
				"",
				true,
				&value,
				state.backendRevision,
			)
			if err != nil {
				state.snapshot.Status = "Add profile failed: " + err.Error()
				return
			}
			state.backendRevision = status.Revision
			path = status.ResultPath
			state.snapshot.Status = "Subscription linked: " + payload.summary() +
				" · U refreshes from the saved URL"
			refreshTUIProfiles(&state.snapshot, state.paths)
			state.snapshot.SelectedRow = findTUIProfile(state.snapshot.Profiles, path)
			state.profileSelection = path
		})
	case tuiInputProfileFile:
		if value == "" {
			m.snapshot.Status = "Local profile import cancelled"
			return nil
		}
		if m.service == nil {
			m.snapshot.Status = "Local profile import requires the managed backend"
			return nil
		}
		payload, name, err := readTUILocalProfileDetails(value)
		if err != nil {
			m.snapshot.Status = "Import local profile failed: " + err.Error()
			appendTUILogEvent("ERROR", m.snapshot.Status)
			return nil
		}
		return m.startOperation(func(state *tuiOperationState) {
			if !prepareTUIBackendRevision(state, m.service) {
				return
			}
			path, err := nextTUIImportedProfilePath(state.paths.homeDir, name)
			if err != nil {
				state.snapshot.Status = "Import local profile failed: " + err.Error()
				return
			}
			status, err := m.service.putProfile(
				path,
				payload.Data,
				"",
				true,
				nil,
				state.backendRevision,
			)
			if err != nil {
				state.snapshot.Status = "Import local profile failed: " + err.Error()
				return
			}
			state.backendRevision = status.Revision
			path = status.ResultPath
			state.snapshot.Status = "Local profile imported: " + filepath.Base(path) +
				" · " + payload.summary()
			refreshTUIProfiles(&state.snapshot, state.paths)
			state.snapshot.SelectedRow = findTUIProfile(state.snapshot.Profiles, path)
			state.profileSelection = path
		})
	case tuiInputProfileName:
		if value == "" {
			m.snapshot.Status = "Profile rename cancelled"
			return nil
		}
		if m.service == nil {
			m.snapshot.Status = "Profile rename requires the managed backend"
			return nil
		}
		return m.startOperation(func(state *tuiOperationState) {
			if !prepareTUIBackendRevision(state, m.service) {
				return
			}
			status, err := m.service.renameProfile(
				renameProfilePath,
				value,
				state.backendRevision,
			)
			if err != nil {
				state.snapshot.Status = "Rename failed: " + err.Error()
				return
			}
			state.backendRevision = status.Revision
			newPath := status.ResultPath
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
			snapshot.Status = "Proxy port disabled; System proxy disabled"
			return true
		}
	}
	snapshot.Status = fmt.Sprintf("Proxy port changed to %d", snapshot.Settings.MixedPort)
	return true
}

func downloadTUIProfile(homeDir, value string) (string, error) {
	payload, err := fetchTUISubscriptionDetails(value)
	if err != nil {
		return "", err
	}
	path, err := tuiSubscriptionImportPath(homeDir, payload)
	if err != nil {
		return "", err
	}
	if err := writeTUIProfileAtomically(path, payload.Data, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func readTUILocalProfile(value string) ([]byte, string, error) {
	payload, name, err := readTUILocalProfileDetails(value)
	return payload.Data, name, err
}

func readTUILocalProfileDetails(
	value string,
) (tuiSubscriptionPayload, string, error) {
	path := strings.TrimSpace(value)
	if path == "" {
		return tuiSubscriptionPayload{}, "", errors.New("profile path must not be empty")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return tuiSubscriptionPayload{}, "", fmt.Errorf("resolve home directory: %w", err)
		}
		if path == "~" {
			path = homeDir
		} else {
			path = filepath.Join(homeDir, strings.TrimPrefix(path, "~/"))
		}
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return tuiSubscriptionPayload{}, "", err
	}
	info, err := os.Lstat(absolutePath)
	if err != nil {
		return tuiSubscriptionPayload{}, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return tuiSubscriptionPayload{}, "", errors.New("profile must be a regular file, not a symlink")
	}
	name := filepath.Base(absolutePath)
	if info.Size() > tuiSubscriptionMaxBytes {
		return tuiSubscriptionPayload{}, "", fmt.Errorf(
			"profile content exceeds %d MiB",
			tuiSubscriptionMaxBytes>>20,
		)
	}
	data, err := os.ReadFile(absolutePath)
	if err != nil {
		return tuiSubscriptionPayload{}, "", err
	}
	if len(data) == 0 {
		return tuiSubscriptionPayload{}, "", errors.New("profile content must not be empty")
	}
	payload, err := normalizeTUISubscription(data)
	if err != nil {
		return tuiSubscriptionPayload{}, "", errors.New("profile is invalid: " + err.Error())
	}
	return payload, tuiImportedProfileName(name), nil
}

func tuiImportedProfileName(sourceName string) string {
	base := filepath.Base(strings.TrimSpace(sourceName))
	extension := strings.ToLower(filepath.Ext(base))
	if extension == ".yaml" || extension == ".yml" {
		return base
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if stem == "" || stem == "." || stem == ".." {
		stem = "imported-profile"
	}
	return stem + ".yaml"
}

func nextTUIImportedProfilePath(homeDir, sourceName string) (string, error) {
	extension := strings.ToLower(filepath.Ext(sourceName))
	stem := strings.TrimSuffix(filepath.Base(sourceName), filepath.Ext(sourceName))
	if extension != ".yaml" && extension != ".yml" || stem == "" {
		return "", errors.New("profile file must end in .yaml or .yml")
	}
	if isTUIRuntimeProfileName(sourceName) {
		stem = "imported-" + strings.TrimLeft(stem, ".")
	}
	for suffix := 1; suffix <= 10000; suffix++ {
		name := stem + extension
		if suffix > 1 {
			name = fmt.Sprintf("%s-%d%s", stem, suffix, extension)
		}
		path := filepath.Join(homeDir, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate a unique profile name")
}

func fetchTUISubscription(value string) ([]byte, error) {
	payload, err := fetchTUISubscriptionDetails(value)
	return payload.Data, err
}

func fetchTUISubscriptionDetails(
	value string,
) (tuiSubscriptionPayload, error) {
	request, err := newTUISubscriptionRequest(value)
	if err != nil {
		return tuiSubscriptionPayload{}, err
	}
	client := &nethttp.Client{Timeout: 30 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return tuiSubscriptionPayload{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return tuiSubscriptionPayload{}, fmt.Errorf("subscription returned %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, tuiSubscriptionMaxBytes+1))
	if err != nil {
		return tuiSubscriptionPayload{}, err
	}
	if len(data) == 0 {
		return tuiSubscriptionPayload{}, errors.New("subscription response is empty")
	}
	if len(data) > tuiSubscriptionMaxBytes {
		return tuiSubscriptionPayload{}, fmt.Errorf(
			"subscription response exceeds %d MiB",
			tuiSubscriptionMaxBytes>>20,
		)
	}
	payload, err := normalizeTUISubscription(data)
	if err != nil {
		return tuiSubscriptionPayload{}, fmt.Errorf(
			"downloaded subscription is invalid: %w",
			err,
		)
	}
	payload.FileName = tuiNewSubscriptionFileName(
		response.Header.Get("Content-Disposition"),
	)
	return payload, nil
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
		if rollbackErr := restoreTUISubscriptionProfile(path, backup); rollbackErr != nil {
			return tuiProfileBackup{}, fmt.Errorf(
				"read updated subscription profile: %v; profile rollback failed: %w",
				err,
				rollbackErr,
			)
		}
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
	request.Header.Set(
		"Accept",
		"application/yaml, application/x-yaml, application/json, text/yaml, text/plain, */*",
	)
	return request, nil
}

func tuiKeyFromTea(message tea.KeyMsg) (tuiKey, bool) {
	switch message.String() {
	case "q", "Q":
		return tuiKeyQuit, true
	case "ctrl+c":
		return tuiKeyInterrupt, true
	case "ctrl+n":
		return tuiKeyNotifications, true
	case "/":
		return tuiKeySearch, true
	case "f":
		return tuiKeyFilter, true
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
		return tuiKeySSH, true
	case "3":
		return tuiKeyProxies, true
	case "4":
		return tuiKeyProfiles, true
	case "5":
		return tuiKeyRequests, true
	case "6":
		return tuiKeyConnections, true
	case "7":
		return tuiKeyLogs, true
	case "8":
		return tuiKeyTools, true
	case "9":
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
