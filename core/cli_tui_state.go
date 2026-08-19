//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	tuiStateFilename = ".flclash-cli-state.json"
	tuiStateLockName = ".flclash-cli-state.lock"
	tuiStateVersion  = 2
)

type tuiPersistentState struct {
	Version             int               `json:"version"`
	ActiveProfile       string            `json:"active_profile,omitempty"`
	SelectedProxies     map[string]string `json:"selected_proxies,omitempty"`
	SubscriptionSources map[string]string `json:"subscription_sources,omitempty"`
	TrafficMode         string            `json:"traffic_mode,omitempty"`
	FLCOutbound         string            `json:"flc_outbound,omitempty"`
}

func loadTUIState(homeDir string) (tuiPersistentState, error) {
	path := filepath.Join(homeDir, tuiStateFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return tuiPersistentState{
				Version:             tuiStateVersion,
				SelectedProxies:     map[string]string{},
				SubscriptionSources: map[string]string{},
			}, nil
		}
		return tuiPersistentState{}, err
	}
	var state tuiPersistentState
	if err := json.Unmarshal(data, &state); err != nil {
		return tuiPersistentState{}, fmt.Errorf("parse saved TUI state: %w", err)
	}
	if state.Version != 1 && state.Version != tuiStateVersion {
		return tuiPersistentState{}, fmt.Errorf(
			"unsupported saved TUI state version %d",
			state.Version,
		)
	}
	state.Version = tuiStateVersion
	if state.SelectedProxies == nil {
		state.SelectedProxies = map[string]string{}
	}
	if state.SubscriptionSources == nil {
		state.SubscriptionSources = map[string]string{}
	}
	return state, nil
}

func loadTUITrafficMode(homeDir, _ string) string {
	state, err := loadTUIState(homeDir)
	if err == nil {
		mode := strings.ToLower(strings.TrimSpace(state.TrafficMode))
		if mode == "silent" || mode == "rule" || mode == "global" || mode == "direct" {
			return mode
		}
	}
	return tuiSilentMode
}

func rememberTUITrafficMode(homeDir, mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "silent" && mode != "rule" && mode != "global" && mode != "direct" {
		return fmt.Errorf("unsupported traffic mode %q", mode)
	}
	return updateTUIState(homeDir, func(state *tuiPersistentState) {
		state.TrafficMode = mode
	})
}

func loadTUIFLCOutbound(homeDir string) string {
	state, err := loadTUIState(homeDir)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(state.FLCOutbound)
}

func rememberTUIFLCOutbound(homeDir, outbound string) error {
	outbound = strings.TrimSpace(outbound)
	if outbound == "" {
		return errors.New("FLC outbound must not be empty")
	}
	return updateTUIState(homeDir, func(state *tuiPersistentState) {
		state.FLCOutbound = outbound
	})
}

func saveTUIState(homeDir string, state tuiPersistentState) error {
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		return err
	}
	state.Version = tuiStateVersion
	if state.SelectedProxies == nil {
		state.SelectedProxies = map[string]string{}
	}
	if state.SubscriptionSources == nil {
		state.SubscriptionSources = map[string]string{}
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(homeDir, ".flclash-cli-state-*.tmp")
	if err != nil {
		return err
	}
	tempPath := file.Name()
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, filepath.Join(homeDir, tuiStateFilename)); err != nil {
		return err
	}
	committed = true
	return nil
}

func updateTUIState(
	homeDir string,
	update func(*tuiPersistentState),
) error {
	lockPath := filepath.Join(homeDir, tuiStateLockName)
	owner := cliProcessOwner{
		Kind:      "state-update",
		PID:       os.Getpid(),
		HomeDir:   homeDir,
		StartedAt: time.Now(),
	}
	var lock *cliFileLock
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for {
		lock, err = acquireCLIFileLock(lockPath, owner)
		var busy *cliLockBusyError
		if err == nil || !errors.As(err, &busy) || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		return fmt.Errorf("lock shared state: %w", err)
	}
	defer func() {
		lock.release()
	}()
	state, err := loadTUIState(homeDir)
	if err != nil {
		state = tuiPersistentState{
			Version:             tuiStateVersion,
			SelectedProxies:     map[string]string{},
			SubscriptionSources: map[string]string{},
		}
	}
	update(&state)
	return saveTUIState(homeDir, state)
}

func rememberTUIActiveProfile(paths cliPaths) error {
	profile, err := filepath.Rel(paths.homeDir, paths.configPath)
	if err != nil {
		return err
	}
	if profile == "." ||
		profile == ".." ||
		strings.HasPrefix(profile, ".."+string(filepath.Separator)) {
		return errors.New("active profile must be inside the TUI data directory")
	}
	return updateTUIState(paths.homeDir, func(state *tuiPersistentState) {
		state.ActiveProfile = filepath.Clean(profile)
	})
}

func rememberTUIProxySelection(homeDir, group, proxy string) error {
	if group == "" || proxy == "" {
		return errors.New("proxy group and selection must not be empty")
	}
	return updateTUIState(homeDir, func(state *tuiPersistentState) {
		if state.SelectedProxies == nil {
			state.SelectedProxies = map[string]string{}
		}
		state.SelectedProxies[group] = proxy
	})
}

func tuiProfileStateKey(homeDir, profilePath string) (string, error) {
	homeDir = filepath.Clean(homeDir)
	profilePath = filepath.Clean(profilePath)
	relative, err := filepath.Rel(homeDir, profilePath)
	if err != nil ||
		relative == "." ||
		relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("profile must be inside the TUI data directory")
	}
	extension := strings.ToLower(filepath.Ext(relative))
	if extension != ".yaml" && extension != ".yml" {
		return "", errors.New("profile must be a YAML file")
	}
	return filepath.Clean(relative), nil
}

func rememberTUISubscriptionSource(homeDir, profilePath, sourceURL string) error {
	key, err := tuiProfileStateKey(homeDir, profilePath)
	if err != nil {
		return err
	}
	if strings.TrimSpace(sourceURL) == "" {
		return errors.New("subscription URL must not be empty")
	}
	return updateTUIState(homeDir, func(state *tuiPersistentState) {
		if state.SubscriptionSources == nil {
			state.SubscriptionSources = map[string]string{}
		}
		state.SubscriptionSources[key] = sourceURL
	})
}

func loadTUISubscriptionSources(homeDir string) map[string]string {
	state, err := loadTUIState(homeDir)
	if err != nil {
		return map[string]string{}
	}
	sources := make(map[string]string, len(state.SubscriptionSources))
	for profile, sourceURL := range state.SubscriptionSources {
		sources[filepath.Clean(profile)] = sourceURL
	}
	return sources
}

func loadTUISubscriptionSource(homeDir, profilePath string) (string, error) {
	key, err := tuiProfileStateKey(homeDir, profilePath)
	if err != nil {
		return "", err
	}
	state, err := loadTUIState(homeDir)
	if err != nil {
		return "", fmt.Errorf("load subscription source: %w", err)
	}
	sourceURL := strings.TrimSpace(state.SubscriptionSources[key])
	if sourceURL == "" {
		return "", errors.New(
			"profile is not linked to a subscription; import its URL once to create a linked profile",
		)
	}
	return sourceURL, nil
}

func renameTUISubscriptionSource(homeDir, oldPath, newPath string) error {
	oldKey, err := tuiProfileStateKey(homeDir, oldPath)
	if err != nil {
		return err
	}
	newKey, err := tuiProfileStateKey(homeDir, newPath)
	if err != nil {
		return err
	}
	return updateTUIState(homeDir, func(state *tuiPersistentState) {
		sourceURL, ok := state.SubscriptionSources[oldKey]
		if !ok {
			return
		}
		delete(state.SubscriptionSources, oldKey)
		state.SubscriptionSources[newKey] = sourceURL
	})
}

func restoreTUIActiveProfile(paths cliPaths) (cliPaths, error) {
	state, err := loadTUIState(paths.homeDir)
	if err != nil || state.ActiveProfile == "" {
		return paths, err
	}
	if filepath.IsAbs(state.ActiveProfile) {
		return paths, errors.New("saved active profile must be a relative path")
	}
	profilePath := filepath.Clean(filepath.Join(paths.homeDir, state.ActiveProfile))
	relative, err := filepath.Rel(paths.homeDir, profilePath)
	if err != nil ||
		relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return paths, errors.New("saved active profile is outside the TUI data directory")
	}
	extension := strings.ToLower(filepath.Ext(profilePath))
	if extension != ".yaml" && extension != ".yml" {
		return paths, errors.New("saved active profile is not a YAML file")
	}
	info, err := os.Stat(profilePath)
	if err != nil {
		return paths, fmt.Errorf("saved active profile: %w", err)
	}
	if !info.Mode().IsRegular() {
		return paths, errors.New("saved active profile is not a regular file")
	}
	data, err := os.ReadFile(profilePath)
	if err != nil {
		return paths, err
	}
	if message := validateConfigBytes(data); message != "" {
		return paths, errors.New("saved active profile is invalid: " + message)
	}
	paths.configPath = profilePath
	return paths, nil
}

func loadTUISelectedProxies(homeDir string) map[string]string {
	state, err := loadTUIState(homeDir)
	if err != nil {
		return map[string]string{}
	}
	selected := make(map[string]string, len(state.SelectedProxies))
	for group, proxy := range state.SelectedProxies {
		selected[group] = proxy
	}
	return selected
}
