//go:build linux && !cgo && cli

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (r *tuiServiceRuntime) putProfile(
	request tuiServiceRequest,
) (bool, string, error) {
	r.mu.RLock()
	paths := r.paths
	r.mu.RUnlock()
	target := filepath.Clean(request.ConfigPath)
	if _, err := tuiProfileStateKey(paths.HomeDir, target); err != nil {
		return false, "", err
	}
	if len(request.ProfileData) == 0 {
		return false, "", errors.New("profile content must not be empty")
	}
	if len(request.ProfileData) > tuiSubscriptionMaxBytes {
		return false, "", fmt.Errorf(
			"profile content exceeds %d MiB",
			tuiSubscriptionMaxBytes>>20,
		)
	}
	if message := validateConfigBytes(request.ProfileData); message != "" {
		return false, "", errors.New("profile is invalid: " + message)
	}
	if request.SubscriptionURL != nil {
		if _, err := newTUISubscriptionRequest(*request.SubscriptionURL); err != nil {
			return false, "", err
		}
	}

	lease, err := acquireTUIProfileLocks(paths.HomeDir, target)
	if err != nil {
		return false, "", err
	}
	defer lease.release()
	var original []byte
	mode := os.FileMode(0o600)
	existed := false
	info, statErr := os.Lstat(target)
	switch {
	case statErr == nil:
		existed = true
		if request.CreateOnly {
			return false, "", fmt.Errorf("profile %q already exists", filepath.Base(target))
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return false, "", errors.New("profile must be a regular file, not a symlink")
		}
		original, err = os.ReadFile(target)
		if err != nil {
			return false, "", err
		}
		if request.ExpectedSHA256 == "" {
			return false, "", errors.New("an expected profile digest is required")
		}
		if actual := tuiBytesSHA256(original); actual != request.ExpectedSHA256 {
			return false, "", errors.New("profile changed after it was read; refresh and retry")
		}
		mode = info.Mode()
	case os.IsNotExist(statErr):
		if !request.CreateOnly {
			return false, "", errors.New("profile no longer exists; refresh and retry")
		}
	default:
		return false, "", statErr
	}
	if err := writeTUIProfileAtomically(target, request.ProfileData, mode); err != nil {
		return false, "", err
	}
	active := filepath.Clean(target) == filepath.Clean(paths.ConfigPath)
	rollback := func(cause error) error {
		var restoreErr error
		if existed {
			restoreErr = writeTUIProfileAtomically(target, original, mode)
		} else {
			restoreErr = os.Remove(target)
			if os.IsNotExist(restoreErr) {
				restoreErr = nil
			}
		}
		if restoreErr != nil {
			return fmt.Errorf("%v; profile rollback failed: %w", cause, restoreErr)
		}
		if active {
			if _, reloadErr := r.reloadUnlocked(target, ""); reloadErr != nil {
				return fmt.Errorf("%v; profile restored but Core rollback failed: %w", cause, reloadErr)
			}
		}
		return cause
	}
	if active {
		if _, err := r.reloadAndRepairFLCUnlocked(
			target,
			tuiBytesSHA256(request.ProfileData),
		); err != nil {
			return false, "", rollback(fmt.Errorf("reload profile: %w", err))
		}
	}
	if request.SubscriptionURL != nil {
		if err := rememberTUISubscriptionSource(
			paths.HomeDir,
			target,
			*request.SubscriptionURL,
		); err != nil {
			return false, "", rollback(fmt.Errorf("save subscription source: %w", err))
		}
	}
	return true, target, nil
}

func (r *tuiServiceRuntime) renameProfile(
	path,
	newName string,
) (bool, string, error) {
	r.mu.RLock()
	paths := r.paths
	r.mu.RUnlock()
	path = filepath.Clean(path)
	if _, err := tuiProfileStateKey(paths.HomeDir, path); err != nil {
		return false, "", err
	}
	if path == filepath.Clean(paths.ConfigPath) {
		return false, "", errors.New("activate another profile before renaming the current profile")
	}
	renamed, err := renameTUIProfile(paths.HomeDir, path, newName)
	if err != nil {
		return false, "", err
	}
	return renamed != path, renamed, nil
}

func (r *tuiServiceRuntime) deleteProfile(path string) (bool, error) {
	r.mu.RLock()
	paths := r.paths
	r.mu.RUnlock()
	path = filepath.Clean(path)
	key, err := tuiProfileStateKey(paths.HomeDir, path)
	if err != nil {
		return false, err
	}
	if path == filepath.Clean(paths.ConfigPath) {
		return false, errors.New("cannot delete the active profile")
	}
	lease, err := acquireTUIProfileLocks(paths.HomeDir, path)
	if err != nil {
		return false, err
	}
	defer lease.release()
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("profile must be a regular file, not a symlink")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		return false, err
	}
	if err := updateTUIState(paths.HomeDir, func(state *tuiPersistentState) {
		delete(state.SubscriptionSources, key)
	}); err != nil {
		if restoreErr := writeTUIProfileAtomically(path, data, info.Mode()); restoreErr != nil {
			return false, fmt.Errorf("update profile metadata: %v; file rollback failed: %w", err, restoreErr)
		}
		return false, fmt.Errorf("update profile metadata: %w; file restored", err)
	}
	return true, nil
}

func (r *tuiServiceRuntime) linkProfile(
	path string,
	subscriptionURL *string,
) (bool, error) {
	if subscriptionURL == nil {
		return false, errors.New("subscription URL is required")
	}
	if _, err := newTUISubscriptionRequest(*subscriptionURL); err != nil {
		return false, err
	}
	r.mu.RLock()
	paths := r.paths
	r.mu.RUnlock()
	path = filepath.Clean(path)
	if _, err := tuiProfileStateKey(paths.HomeDir, path); err != nil {
		return false, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, errors.New("profile must be a regular file, not a symlink")
	}
	current, currentErr := loadTUISubscriptionSource(paths.HomeDir, path)
	if currentErr == nil && current == strings.TrimSpace(*subscriptionURL) {
		return false, nil
	}
	if err := rememberTUISubscriptionSource(paths.HomeDir, path, *subscriptionURL); err != nil {
		return false, err
	}
	return true, nil
}

func (r *tuiServiceRuntime) backupProfile(path string) (bool, string, error) {
	r.mu.RLock()
	paths := r.paths
	r.mu.RUnlock()
	path = filepath.Clean(path)
	if _, err := tuiProfileStateKey(paths.HomeDir, path); err != nil {
		return false, "", err
	}
	lease, err := acquireTUIProfileLocks(paths.HomeDir, path)
	if err != nil {
		return false, "", err
	}
	defer lease.release()
	info, err := os.Lstat(path)
	if err != nil {
		return false, "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, "", errors.New("profile must be a regular file, not a symlink")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return false, "", err
	}
	backupPath := fmt.Sprintf("%s.backup-%d", path, time.Now().UnixNano())
	backup, err := os.OpenFile(
		backupPath,
		os.O_CREATE|os.O_EXCL|os.O_WRONLY,
		0o600,
	)
	if err != nil {
		return false, "", err
	}
	if _, err := backup.Write(data); err != nil {
		_ = backup.Close()
		_ = os.Remove(backupPath)
		return false, "", err
	}
	if err := backup.Sync(); err != nil {
		_ = backup.Close()
		_ = os.Remove(backupPath)
		return false, "", err
	}
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupPath)
		return false, "", err
	}
	return true, backupPath, nil
}

func (r *tuiServiceRuntime) restoreProfile(path string) (bool, string, error) {
	r.mu.RLock()
	paths := r.paths
	r.mu.RUnlock()
	path = filepath.Clean(path)
	if _, err := tuiProfileStateKey(paths.HomeDir, path); err != nil {
		return false, "", err
	}
	backupPath, backup, err := restoreLatestTUIConfigLocked(paths.HomeDir, path)
	if err != nil {
		return false, "", err
	}
	defer backup.release()
	if path != filepath.Clean(paths.ConfigPath) {
		return true, backupPath, nil
	}
	if _, err := r.reloadAndRepairFLCUnlocked(path, backup.updatedSHA256); err != nil {
		if restoreErr := restoreTUISubscriptionProfile(path, backup); restoreErr != nil {
			return false, "", fmt.Errorf("reload restored profile: %v; file rollback failed: %w", err, restoreErr)
		}
		if _, rollbackErr := r.reloadUnlocked(path, tuiBytesSHA256(backup.data)); rollbackErr != nil {
			return false, "", fmt.Errorf("reload restored profile: %v; Core rollback failed: %w", err, rollbackErr)
		}
		return false, "", fmt.Errorf("reload restored profile: %w; original restored", err)
	}
	return true, backupPath, nil
}
