//go:build linux && !cgo && cli

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func ensureTUIService(
	paths cliPaths,
	testURL string,
	explicitConfig bool,
	explicitDirectory bool,
) (*tuiServiceClient, tuiServiceStatus, error) {
	client := newTUIServiceClient(paths.homeDir)
	if status, err := client.compatibleStatus(); err == nil {
		if status.Version == cliVersion &&
			status.ProtocolVersion == tuiServiceProtocolVersion {
			if err := validateTUIServiceTarget(
				paths,
				status,
				explicitConfig,
				explicitDirectory,
			); err != nil {
				return nil, tuiServiceStatus{}, err
			}
			return client, status, nil
		}
		if err := validateTUIServiceUpgradeCandidate(status); err != nil {
			return nil, tuiServiceStatus{}, err
		}
		wasRunning := status.Running
		if status.HomeDir != "" {
			paths.homeDir = status.HomeDir
		}
		if _, pathErr := tuiProfileStateKey(
			paths.homeDir,
			status.ConfigPath,
		); pathErr == nil {
			paths.configPath = status.ConfigPath
		}
		if err := client.shutdownPIDAndWait(
			status.PID,
			tuiServiceShutdownTimeout,
		); err != nil {
			return nil, tuiServiceStatus{}, fmt.Errorf(
				"stop outdated Backend %s: %w",
				status.Version,
				err,
			)
		}
		if err := spawnTUIService(paths, testURL, !explicitConfig); err != nil {
			return waitForCompetingTUIService(
				client,
				paths,
				explicitConfig,
				explicitDirectory,
				err,
			)
		}
		status, err = waitForTUIService(client, 30*time.Second)
		if err != nil {
			return nil, tuiServiceStatus{}, err
		}
		if wasRunning {
			status, err = client.startAtRevision(status.Revision)
			if err != nil {
				return nil, tuiServiceStatus{}, fmt.Errorf(
					"restart listeners after Backend upgrade: %w",
					err,
				)
			}
		}
		return client, status, nil
	}
	if legacyClient, legacyStatus, found := findLegacyTUIService(
		paths,
	); found {
		if legacyStatus.HomeDir == "" {
			legacyStatus.HomeDir = filepath.Dir(
				legacyStatus.ConfigPath,
			)
		}
		if err := validateTUIServiceTarget(
			paths,
			legacyStatus,
			explicitConfig,
			explicitDirectory,
		); err != nil {
			return nil, tuiServiceStatus{}, err
		}
		wasRunning := legacyStatus.Running
		if legacyStatus.ConfigPath != "" {
			paths.configPath = legacyStatus.ConfigPath
		}
		if legacyStatus.HomeDir != "" {
			paths.homeDir = legacyStatus.HomeDir
		}
		if err := legacyClient.shutdownPIDAndWait(
			legacyStatus.PID,
			tuiServiceShutdownTimeout,
		); err != nil {
			return nil, tuiServiceStatus{}, fmt.Errorf(
				"stop legacy background service: %w",
				err,
			)
		}
		if err := spawnTUIService(paths, testURL, !explicitConfig); err != nil {
			return waitForCompetingTUIService(
				client,
				paths,
				explicitConfig,
				explicitDirectory,
				err,
			)
		}
		status, err := waitForTUIService(client, 30*time.Second)
		if err != nil {
			return nil, tuiServiceStatus{}, err
		}
		if wasRunning {
			status, err = client.startAtRevision(status.Revision)
			if err != nil {
				return nil, tuiServiceStatus{}, fmt.Errorf(
					"restart listeners after Backend migration: %w",
					err,
				)
			}
		}
		return client, status, nil
	}
	if err := spawnTUIService(paths, testURL, !explicitConfig); err != nil {
		return waitForCompetingTUIService(
			client,
			paths,
			explicitConfig,
			explicitDirectory,
			err,
		)
	}
	status, err := waitForTUIService(client, 30*time.Second)
	if err != nil {
		return nil, tuiServiceStatus{}, err
	}
	if err := validateTUIServiceTarget(
		paths,
		status,
		explicitConfig,
		explicitDirectory,
	); err != nil {
		return nil, tuiServiceStatus{}, err
	}
	return client, status, nil
}

func validateTUIServiceUpgradeCandidate(status tuiServiceStatus) error {
	if status.ProtocolVersion > tuiServiceProtocolVersion ||
		isNewerCLIVersion(status.Version, cliVersion) {
		return fmt.Errorf(
			"backend %s uses protocol %d, newer than this client %s protocol %d; update flclash instead of replacing the backend",
			status.Version,
			status.ProtocolVersion,
			cliVersion,
			tuiServiceProtocolVersion,
		)
	}
	return nil
}

func findLegacyTUIService(
	paths cliPaths,
) (*tuiServiceClient, tuiServiceStatus, bool) {
	runtimeSocket, _ := cliServiceSocketPath()
	directories := []string{paths.homeDir}
	if configRoot, err := os.UserConfigDir(); err == nil {
		directories = append(
			directories,
			filepath.Join(configRoot, "flclash"),
		)
	}
	seen := map[string]bool{}
	for _, directory := range directories {
		directory = filepath.Clean(directory)
		socketPath := filepath.Join(
			directory,
			tuiServiceSocketFilename,
		)
		if seen[directory] ||
			filepath.Clean(socketPath) == filepath.Clean(runtimeSocket) {
			continue
		}
		seen[directory] = true
		client := newTUIServiceClientAt(directory)
		status, err := client.compatibleStatus()
		if err == nil {
			return client, status, true
		}
	}
	return nil, tuiServiceStatus{}, false
}

func validateTUIServiceTarget(
	paths cliPaths,
	status tuiServiceStatus,
	explicitConfig bool,
	explicitDirectory bool,
) error {
	if !explicitConfig && !explicitDirectory {
		return nil
	}
	sameHome := status.HomeDir == "" ||
		filepath.Clean(status.HomeDir) == filepath.Clean(paths.homeDir)
	sameConfig := status.ConfigPath == "" ||
		filepath.Clean(status.ConfigPath) == filepath.Clean(paths.configPath)
	if sameHome && (!explicitConfig || sameConfig) {
		return nil
	}
	return fmt.Errorf(
		"the per-user FlClash backend is already using %q; "+
			"stop it before opening explicit config %q",
		status.ConfigPath,
		paths.configPath,
	)
}

func waitForCompetingTUIService(
	client *tuiServiceClient,
	paths cliPaths,
	explicitConfig bool,
	explicitDirectory bool,
	spawnErr error,
) (*tuiServiceClient, tuiServiceStatus, error) {
	var busyErr *cliLockBusyError
	if !errors.As(spawnErr, &busyErr) ||
		(busyErr.owner.Kind != "service" &&
			busyErr.owner.Kind != "service-starting") {
		return nil, tuiServiceStatus{}, spawnErr
	}
	status, err := waitForTUIService(client, 30*time.Second)
	if err != nil {
		return nil, tuiServiceStatus{}, spawnErr
	}
	if err := validateTUIServiceTarget(
		paths,
		status,
		explicitConfig,
		explicitDirectory,
	); err != nil {
		return nil, tuiServiceStatus{}, err
	}
	return client, status, nil
}

func waitForTUIService(
	client *tuiServiceClient,
	timeout time.Duration,
) (tuiServiceStatus, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		status, err := client.status()
		if err == nil {
			return status, nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	return tuiServiceStatus{}, fmt.Errorf(
		"Backend did not become ready: %w",
		lastErr,
	)
}

func waitForTUIServiceExit(
	client *tuiServiceClient,
	pid int,
	timeout time.Duration,
) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, statusErr := client.compatibleStatus()
		if statusErr != nil && !cliProcessRunning(pid) {
			return true
		}
		time.Sleep(25 * time.Millisecond)
	}
	_, statusErr := client.compatibleStatus()
	return statusErr != nil && !cliProcessRunning(pid)
}

func cliProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func spawnTUIService(paths cliPaths, testURL string, allowCreate bool) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(paths.homeDir, 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(paths.homeDir, tuiServiceLogFilename)
	rotateTUIServiceLog(logPath)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	backendLock, err := acquireCLIBackendLock(cliProcessOwner{
		Kind:       "service-starting",
		HomeDir:    paths.homeDir,
		ConfigPath: paths.configPath,
	})
	if err != nil {
		_ = logFile.Close()
		return err
	}
	command := exec.Command(
		executable,
		"_service",
		"--directory",
		paths.homeDir,
		"--config",
		paths.configPath,
		"--test-url",
		testURL,
		"--create-config="+strconv.FormatBool(allowCreate),
		"--lock-fd",
		"3",
	)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.ExtraFiles = []*os.File{backendLock.file}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		backendLock.release()
		_ = logFile.Close()
		return err
	}
	backendLock.closeTransferredCopy()
	_ = logFile.Close()
	reapTUIServiceProcess(command)
	return nil
}

func rotateTUIServiceLog(path string) {
	cliPersistentLogMu.Lock()
	defer cliPersistentLogMu.Unlock()
	rotateTUIServiceLogUnlocked(path)
}

func rotateTUIServiceLogUnlocked(path string) {
	info, err := os.Stat(path)
	if err != nil || info.Size() <= tuiServiceLogMaxBytes {
		return
	}
	backupPath := path + ".1"
	_ = os.Remove(backupPath)
	_ = os.Rename(path, backupPath)
}

type tuiRotatingLogWriter struct {
	path string
	file *os.File
	size int64
}

func newTUIRotatingLogWriter(path string) (*tuiRotatingLogWriter, error) {
	rotateTUIServiceLog(path)
	writer := &tuiRotatingLogWriter{path: path}
	if err := writer.reopen(); err != nil {
		return nil, err
	}
	return writer, nil
}

func (w *tuiRotatingLogWriter) reopen() error {
	if w.file != nil {
		_ = w.file.Close()
	}
	file, err := os.OpenFile(w.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return err
	}
	w.file = file
	w.size = info.Size()
	return nil
}

func (w *tuiRotatingLogWriter) Write(data []byte) (int, error) {
	cliPersistentLogMu.Lock()
	defer cliPersistentLogMu.Unlock()
	if w.file == nil {
		if err := w.reopen(); err != nil {
			return 0, err
		}
	}
	pathInfo, pathErr := os.Stat(w.path)
	fileInfo, fileErr := w.file.Stat()
	if pathErr != nil || fileErr != nil || !os.SameFile(pathInfo, fileInfo) {
		if err := w.reopen(); err != nil {
			return 0, err
		}
	} else {
		w.size = pathInfo.Size()
	}
	if w.size+int64(len(data)) > tuiServiceLogMaxBytes {
		_ = w.file.Close()
		w.file = nil
		rotateTUIServiceLogUnlocked(w.path)
		if err := w.reopen(); err != nil {
			return 0, err
		}
	}
	written, err := w.file.Write(data)
	w.size += int64(written)
	return written, err
}

func (w *tuiRotatingLogWriter) Close() error {
	cliPersistentLogMu.Lock()
	defer cliPersistentLogMu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func reapTUIServiceProcess(command *exec.Cmd) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
		close(done)
	}()
	return done
}
