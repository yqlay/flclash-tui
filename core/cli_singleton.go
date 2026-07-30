//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	cliRuntimeLockFilename     = ".flclash-runtime.lock"
	cliFrontendDirectoryName   = ".flclash-frontends"
	cliFrontendSessionFileMode = 0o600
)

var cliRuntimeDirectoryOverride string

type cliProcessOwner struct {
	Kind       string    `json:"kind"`
	PID        int       `json:"pid"`
	TTY        string    `json:"tty,omitempty"`
	HomeDir    string    `json:"home_dir,omitempty"`
	ConfigPath string    `json:"config_path,omitempty"`
	StartedAt  time.Time `json:"started_at"`
}

type cliFileLock struct {
	file  *os.File
	path  string
	owner cliProcessOwner
}

type cliLockBusyError struct {
	path  string
	owner cliProcessOwner
}

func (e *cliLockBusyError) Error() string {
	description := "another FlClash backend is already running for this user"
	if e.owner.PID > 0 {
		description += " (PID " + strconv.Itoa(e.owner.PID)
		if e.owner.Kind != "" {
			description += ", " + e.owner.Kind
		}
		description += ")"
	}
	if e.owner.ConfigPath != "" {
		description += "; active config: " + e.owner.ConfigPath
	}
	return description
}

type cliFrontendSession struct {
	lock *cliFileLock
}

func cliRuntimeDirectory() (string, error) {
	if cliRuntimeDirectoryOverride != "" {
		return filepath.Abs(cliRuntimeDirectoryOverride)
	}
	uid := os.Getuid()
	runUserDirectory := filepath.Join(
		"/run/user",
		strconv.Itoa(uid),
	)
	if info, err := os.Stat(runUserDirectory); err == nil &&
		info.IsDir() &&
		cliPathOwnedByCurrentUser(info) {
		return filepath.Join(runUserDirectory, "flclash"), nil
	}
	return filepath.Join(
		os.TempDir(),
		"flclash-runtime-"+strconv.Itoa(uid),
	), nil
}

func cliPathOwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

func ensureCLIRuntimeDirectory() (string, error) {
	directory, err := cliRuntimeDirectory()
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(directory)
	if os.IsNotExist(err) {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return "", err
		}
		return directory, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 ||
		!info.IsDir() ||
		!cliPathOwnedByCurrentUser(info) {
		return "", fmt.Errorf(
			"unsafe FlClash runtime directory %q",
			directory,
		)
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(directory, 0o700); err != nil {
			return "", err
		}
	}
	return directory, nil
}

func cliRuntimeLockPath() (string, error) {
	directory, err := cliRuntimeDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, cliRuntimeLockFilename), nil
}

func cliServiceSocketPath() (string, error) {
	directory, err := cliRuntimeDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, tuiServiceSocketFilename), nil
}

func acquireCLIBackendLock(owner cliProcessOwner) (*cliFileLock, error) {
	directory, err := ensureCLIRuntimeDirectory()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(directory, cliRuntimeLockFilename)
	return acquireCLIFileLock(path, owner)
}

func acquireCLIFileLock(
	path string,
	owner cliProcessOwner,
) (*cliFileLock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(
		int(file.Fd()),
		syscall.LOCK_EX|syscall.LOCK_NB,
	); err != nil {
		_ = file.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) ||
			errors.Is(err, syscall.EAGAIN) {
			return nil, &cliLockBusyError{
				path:  path,
				owner: readCLIProcessOwner(path),
			}
		}
		return nil, err
	}
	lock := &cliFileLock{file: file, path: path}
	if err := lock.setOwner(owner); err != nil {
		lock.release()
		return nil, err
	}
	return lock, nil
}

func adoptCLIBackendLock(
	file *os.File,
	owner cliProcessOwner,
) (*cliFileLock, error) {
	path, err := cliRuntimeLockPath()
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, errors.New("inherited backend lock is unavailable")
	}
	lock := &cliFileLock{file: file, path: path}
	if err := lock.setOwner(owner); err != nil {
		_ = file.Close()
		return nil, err
	}
	return lock, nil
}

func (l *cliFileLock) setOwner(owner cliProcessOwner) error {
	if owner.PID <= 0 {
		owner.PID = os.Getpid()
	}
	if owner.StartedAt.IsZero() {
		owner.StartedAt = time.Now()
	}
	if _, err := l.file.Seek(0, 0); err != nil {
		return err
	}
	if err := l.file.Truncate(0); err != nil {
		return err
	}
	if err := json.NewEncoder(l.file).Encode(owner); err != nil {
		return err
	}
	if err := l.file.Sync(); err != nil {
		return err
	}
	l.owner = owner
	return nil
}

func (l *cliFileLock) release() {
	if l == nil || l.file == nil {
		return
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	_ = l.file.Close()
	l.file = nil
}

func (l *cliFileLock) closeTransferredCopy() {
	if l == nil || l.file == nil {
		return
	}
	_ = l.file.Close()
	l.file = nil
}

func readCLIProcessOwner(path string) cliProcessOwner {
	data, err := os.ReadFile(path)
	if err != nil {
		return cliProcessOwner{}
	}
	var owner cliProcessOwner
	if json.Unmarshal(data, &owner) != nil {
		return cliProcessOwner{}
	}
	return owner
}

func registerCLIFrontend(
	homeDir,
	configPath string,
) (*cliFrontendSession, []cliProcessOwner, error) {
	existing, err := listCLIFrontends()
	if err != nil {
		return nil, nil, err
	}
	runtimeDirectory, err := ensureCLIRuntimeDirectory()
	if err != nil {
		return nil, nil, err
	}
	sessionDirectory := filepath.Join(
		runtimeDirectory,
		cliFrontendDirectoryName,
	)
	if err := os.MkdirAll(sessionDirectory, 0o700); err != nil {
		return nil, nil, err
	}
	owner := cliProcessOwner{
		Kind:       "tui",
		PID:        os.Getpid(),
		TTY:        cliTTYName(),
		HomeDir:    homeDir,
		ConfigPath: configPath,
		StartedAt:  time.Now(),
	}
	path := filepath.Join(
		sessionDirectory,
		fmt.Sprintf("%d-%d.lock", owner.PID, owner.StartedAt.UnixNano()),
	)
	lock, err := acquireCLIFileLock(path, owner)
	if err != nil {
		return nil, nil, err
	}
	return &cliFrontendSession{lock: lock}, existing, nil
}

func (s *cliFrontendSession) close() {
	if s == nil || s.lock == nil {
		return
	}
	path := s.lock.path
	s.lock.release()
	_ = os.Remove(path)
	s.lock = nil
}

func listCLIFrontends() ([]cliProcessOwner, error) {
	runtimeDirectory, err := cliRuntimeDirectory()
	if err != nil {
		return nil, err
	}
	sessionDirectory := filepath.Join(
		runtimeDirectory,
		cliFrontendDirectoryName,
	)
	entries, err := os.ReadDir(sessionDirectory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	owners := make([]cliProcessOwner, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		path := filepath.Join(sessionDirectory, entry.Name())
		file, openErr := os.OpenFile(path, os.O_RDWR, cliFrontendSessionFileMode)
		if openErr != nil {
			continue
		}
		lockErr := syscall.Flock(
			int(file.Fd()),
			syscall.LOCK_EX|syscall.LOCK_NB,
		)
		if lockErr == nil {
			_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
			_ = file.Close()
			_ = os.Remove(path)
			continue
		}
		_ = file.Close()
		if !errors.Is(lockErr, syscall.EWOULDBLOCK) &&
			!errors.Is(lockErr, syscall.EAGAIN) {
			continue
		}
		owner := readCLIProcessOwner(path)
		if owner.PID > 0 {
			owners = append(owners, owner)
		}
	}
	sort.Slice(owners, func(left, right int) bool {
		if owners[left].StartedAt.Equal(owners[right].StartedAt) {
			return owners[left].PID < owners[right].PID
		}
		return owners[left].StartedAt.Before(owners[right].StartedAt)
	})
	return owners, nil
}

func cliTTYName() string {
	path, err := os.Readlink("/proc/self/fd/0")
	if err != nil || !strings.HasPrefix(path, "/dev/") {
		return ""
	}
	return path
}

func formatCLIFrontendNotice(existing []cliProcessOwner) string {
	if len(existing) == 0 {
		return ""
	}
	parts := make([]string, 0, len(existing))
	for _, owner := range existing {
		value := "PID " + strconv.Itoa(owner.PID)
		if owner.TTY != "" {
			value += " " + owner.TTY
		}
		parts = append(parts, value)
	}
	return fmt.Sprintf(
		"Attached to shared backend · %d other TUI frontend(s): %s",
		len(existing),
		strings.Join(parts, ", "),
	)
}

func formatCLIFrontendSummary(frontends []cliProcessOwner) string {
	if len(frontends) == 0 {
		return "1 active"
	}
	pids := make([]string, 0, len(frontends))
	for _, frontend := range frontends {
		pids = append(pids, strconv.Itoa(frontend.PID))
	}
	return fmt.Sprintf(
		"%d active · PID %s",
		len(frontends),
		strings.Join(pids, ", "),
	)
}
