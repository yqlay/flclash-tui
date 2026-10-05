//go:build linux && !cgo && cli

package main

import (
	"errors"
	"path/filepath"
)

// A draft belongs to the exact configuration transaction that was shown when
// editing began. Watch/refresh updates must not silently rebase it.
type tuiSettingsDraftBase struct {
	InstanceID string
	Revision   uint64
	ConfigPath string
}

type tuiPortEditDraft struct {
	Value string
	Base  *tuiSettingsDraftBase
}

func (m *tuiModel) settingsDraftBase() *tuiSettingsDraftBase {
	return &tuiSettingsDraftBase{InstanceID: m.backendInstanceID, Revision: m.backendRevision, ConfigPath: m.paths.ConfigPath}
}

func cloneTUISettingsDraft(base *tuiSettingsDraftBase) *tuiSettingsDraftBase {
	if base == nil {
		return nil
	}
	copied := *base
	return &copied
}

func (base *tuiSettingsDraftBase) matches(instance string, revision uint64, path string) bool {
	return base != nil && base.Revision > 0 && base.InstanceID == instance && base.Revision == revision && filepath.Clean(base.ConfigPath) == filepath.Clean(path)
}

func validateTUISettingsDraft(state *tuiOperationState) bool {
	if state.settingsDraft == nil && !state.settingsDirty {
		return true
	}
	if state.settingsDraft.matches(state.backendInstanceID, state.backendRevision, state.paths.ConfigPath) {
		return true
	}
	state.snapshot.setStatus(newTUIMessage("settings.draft_conflict"))
	state.settingsConflict = true
	return false
}

func reportTUISettingsSaveError(state *tuiOperationState, err error, fallback string) {
	var failure *tuiServiceError
	if errors.As(err, &failure) && failure.Code == tuiServiceErrorConflict {
		state.settingsConflict = true
		state.snapshot.setStatus(newTUIMessage("settings.draft_conflict"))
		return
	}
	state.snapshot.setStatus(newTUIMessage(fallback, err.Error()))
}

func clearTUISettingsDraft(state *tuiOperationState) {
	state.settingsDraft = nil
	state.stagedSettings = nil
	state.pendingMixedPort = nil
	state.settingsDirty = false
}

func (m *tuiModel) preserveSettingsDraft() {
	if !m.settingsDirty || m.stagedSettings == nil {
		return
	}
	runtime := m.snapshot.Settings
	m.snapshot.Settings = *m.stagedSettings
	if m.service != nil {
		// These controls describe the Backend, not editable profile values.
		m.snapshot.Settings.Mode = runtime.Mode
		m.snapshot.Settings.TunEnabled = runtime.TunEnabled
		m.snapshot.Settings.TunScope = runtime.TunScope
		m.snapshot.Settings.SystemProxy = runtime.SystemProxy
		if !m.settingsDraft.matches(m.backendInstanceID, m.backendRevision, m.paths.ConfigPath) {
			m.snapshot.setStatus(newTUIMessage("settings.draft_conflict"))
		}
	}
}
