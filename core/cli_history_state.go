//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

const (
	tuiHistoryFilename = ".flclash-cli-history.json"
	tuiHistoryVersion  = 1
)

type tuiPersistentHistory struct {
	Version         int          `json:"version"`
	Entries         []tuiRequest `json:"entries"`
	SSHClosedBefore time.Time    `json:"ssh_closed_before,omitzero"`
}

func loadTUIHistory(homeDir string) ([]tuiRequest, error) {
	saved, err := loadTUIPersistentHistory(homeDir)
	return saved.Entries, err
}

func loadTUIPersistentHistory(homeDir string) (tuiPersistentHistory, error) {
	path := filepath.Join(homeDir, tuiHistoryFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return tuiPersistentHistory{}, nil
		}
		return tuiPersistentHistory{}, err
	}
	var saved tuiPersistentHistory
	if err := json.Unmarshal(data, &saved); err != nil {
		return tuiPersistentHistory{}, fmt.Errorf("parse saved History: %w", err)
	}
	if saved.Version != tuiHistoryVersion {
		return tuiPersistentHistory{}, fmt.Errorf("unsupported History version %d", saved.Version)
	}
	entries := make([]tuiRequest, 0, minTUI(len(saved.Entries), tuiRequestHistoryLimit))
	for _, entry := range saved.Entries {
		if entry.ID == "" || entry.FirstSeen.IsZero() || entry.LastSeen.IsZero() {
			continue
		}
		// Restored entries become active again only after their source confirms
		// them. SSH can survive a Backend restart, unlike Mihomo connections.
		entry.Active = false
		entries = append(entries, entry)
		if len(entries) == tuiRequestHistoryLimit {
			break
		}
	}
	saved.Entries = entries
	return saved, nil
}

func saveTUIHistory(homeDir string, entries []tuiRequest, clearedBefore ...time.Time) error {
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		return err
	}
	if len(entries) > tuiRequestHistoryLimit {
		entries = entries[:tuiRequestHistoryLimit]
	}
	saved := tuiPersistentHistory{
		Version: tuiHistoryVersion,
		Entries: entries,
	}
	if len(clearedBefore) > 0 {
		saved.SSHClosedBefore = clearedBefore[0]
	}
	data, err := json.MarshalIndent(saved, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(homeDir, ".flclash-cli-history-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := file.Name()
	committed := false
	defer func() {
		_ = file.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
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
	if err := os.Rename(temporaryPath, filepath.Join(homeDir, tuiHistoryFilename)); err != nil {
		return err
	}
	committed = true
	return nil
}

func (r *tuiServiceRuntime) restoreHistory() error {
	r.mu.RLock()
	homeDir := r.paths.HomeDir
	r.mu.RUnlock()
	saved, err := loadTUIPersistentHistory(homeDir)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.history = saved.Entries
	r.sshHistoryClearedBefore = saved.SSHClosedBefore
	r.historyVersion = 1
	r.persistedHistoryVersion = 1
	r.mu.Unlock()
	return nil
}

func (r *tuiServiceRuntime) persistHistory(force bool) error {
	r.historyPersistMu.Lock()
	defer r.historyPersistMu.Unlock()
	r.mu.RLock()
	version := r.historyVersion
	if !force && version == r.persistedHistoryVersion {
		r.mu.RUnlock()
		return nil
	}
	homeDir := r.paths.HomeDir
	entries := append([]tuiRequest(nil), r.history...)
	clearedBefore := r.sshHistoryClearedBefore
	r.mu.RUnlock()
	if err := saveTUIHistory(homeDir, entries, clearedBefore); err != nil {
		return err
	}
	r.mu.Lock()
	if version > r.persistedHistoryVersion {
		r.persistedHistoryVersion = version
	}
	r.mu.Unlock()
	return nil
}

func (r *tuiServiceRuntime) clearPersistentHistory() (bool, error) {
	return r.clearPersistentHistoryForSource(tuiTrafficSourceMixed)
}

func (r *tuiServiceRuntime) clearPersistentHistoryForSource(source string) (bool, error) {
	source = normalizeTrafficSource(source)
	r.historyUpdateMu.Lock()
	defer r.historyUpdateMu.Unlock()
	r.mu.Lock()
	previous := append([]tuiRequest(nil), r.history...)
	previousCutoff := r.sshHistoryClearedBefore
	next := filterRequestsBySource(previous, inverseTrafficSource(source))
	if source == tuiTrafficSourceMixed {
		next = nil
	}
	changed := len(next) != len(previous)
	if trafficSourceMatches(source, tuiTrafficSourceSSH) {
		// Even an empty History can have completed flows pending in the relay.
		r.sshHistoryClearedBefore = time.Now().UTC()
		changed = true
	}
	if !changed {
		r.mu.Unlock()
		return false, nil
	}
	r.history = next
	r.historyVersion++
	r.mu.Unlock()
	if err := r.persistHistory(true); err != nil {
		r.mu.Lock()
		r.history = previous
		r.sshHistoryClearedBefore = previousCutoff
		r.historyVersion++
		r.mu.Unlock()
		return false, errors.New("persist cleared History: " + err.Error())
	}
	return true, nil
}

func inverseTrafficSource(source string) string {
	switch normalizeTrafficSource(source) {
	case tuiTrafficSourceProxy:
		return tuiTrafficSourceSSH
	case tuiTrafficSourceSSH:
		return tuiTrafficSourceProxy
	default:
		return tuiTrafficSourceMixed
	}
}

func (r *tuiServiceRuntime) recordHistoryUpdate(entries []tuiRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if slices.Equal(r.history, entries) {
		return
	}
	r.history = entries
	r.historyVersion++
}

const tuiHistoryCollectorInterval = 2 * time.Second

func historyPersistenceInterval() time.Duration {
	return 2 * time.Second
}
