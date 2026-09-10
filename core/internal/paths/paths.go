//go:build linux && !cgo && cli

package paths

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
	RuntimeLockFilename     = ".flclash-runtime.lock"
	FrontendDirectoryName   = ".flclash-frontends"
	SilentRuntimePrefix     = ".flclash-silent-runtime-"
	ManagedRuntimePrefix    = ".flclash-managed-runtime-"
	frontendSessionFileMode = 0o600
)

// DirectoryOverride, when set, replaces the per-user runtime directory.
var DirectoryOverride string

type Paths struct {
	HomeDir    string
	ConfigPath string
}

func Resolve(configArg, directoryArg string) (Paths, error) {
	var homeDir string
	var configPath string

	if directoryArg != "" {
		homeDir = directoryArg
		if configArg == "" {
			configArg = "config.yaml"
		}
		if !filepath.IsAbs(configArg) {
			configArg = filepath.Join(homeDir, configArg)
		}
	} else if configArg != "" {
		configPath = configArg
		homeDir = filepath.Dir(configArg)
	} else {
		configRoot, err := os.UserConfigDir()
		if err != nil {
			return Paths{}, fmt.Errorf("resolve user config directory: %w", err)
		}
		homeDir = filepath.Join(configRoot, "flclash")
		configPath = filepath.Join(homeDir, "config.yaml")
	}

	absoluteHome, err := filepath.Abs(homeDir)
	if err != nil {
		return Paths{}, err
	}
	if configPath == "" {
		configPath = configArg
	}
	absoluteConfig, err := filepath.Abs(configPath)
	if err != nil {
		return Paths{}, err
	}
	return Paths{HomeDir: absoluteHome, ConfigPath: absoluteConfig}, nil
}

type ProcessOwner struct {
	Kind       string    `json:"kind"`
	PID        int       `json:"pid"`
	TTY        string    `json:"tty,omitempty"`
	HomeDir    string    `json:"home_dir,omitempty"`
	ConfigPath string    `json:"config_path,omitempty"`
	StartedAt  time.Time `json:"started_at"`
}

type FileLock struct {
	File  *os.File
	Path  string
	Owner ProcessOwner
}

type LockBusyError struct {
	Path  string
	Owner ProcessOwner
}

func (e *LockBusyError) Error() string {
	description := "another FlClash backend is already running for this user"
	if e.Owner.PID > 0 {
		description += " (PID " + strconv.Itoa(e.Owner.PID)
		if e.Owner.Kind != "" {
			description += ", " + e.Owner.Kind
		}
		description += ")"
	}
	if e.Owner.ConfigPath != "" {
		description += "; active config: " + e.Owner.ConfigPath
	}
	return description
}

type FrontendSession struct {
	Lock *FileLock
}

func RuntimeDirectory() (string, error) {
	if DirectoryOverride != "" {
		return filepath.Abs(DirectoryOverride)
	}
	uid := os.Getuid()
	runUserDirectory := filepath.Join(
		"/run/user",
		strconv.Itoa(uid),
	)
	if info, err := os.Stat(runUserDirectory); err == nil &&
		info.IsDir() &&
		OwnedByCurrentUser(info) {
		return filepath.Join(runUserDirectory, "flclash"), nil
	}
	return filepath.Join(
		os.TempDir(),
		"flclash-runtime-"+strconv.Itoa(uid),
	), nil
}

func OwnedByCurrentUser(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Getuid()
}

func EnsureRuntimeDirectory() (string, error) {
	directory, err := RuntimeDirectory()
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
		!OwnedByCurrentUser(info) {
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

func RuntimeLockPath() (string, error) {
	directory, err := RuntimeDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, RuntimeLockFilename), nil
}

func SocketPath(filename string) (string, error) {
	directory, err := RuntimeDirectory()
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, filename), nil
}

func AcquireBackendLock(owner ProcessOwner) (*FileLock, error) {
	directory, err := EnsureRuntimeDirectory()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(directory, RuntimeLockFilename)
	return AcquireFileLock(path, owner)
}

func AcquireFileLock(path string, owner ProcessOwner) (*FileLock, error) {
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
			return nil, &LockBusyError{
				Path:  path,
				Owner: ReadProcessOwner(path),
			}
		}
		return nil, err
	}
	lock := &FileLock{File: file, Path: path}
	if err := lock.SetOwner(owner); err != nil {
		lock.Release()
		return nil, err
	}
	return lock, nil
}

func AdoptBackendLock(file *os.File, owner ProcessOwner) (*FileLock, error) {
	path, err := RuntimeLockPath()
	if err != nil {
		return nil, err
	}
	if file == nil {
		return nil, errors.New("inherited backend lock is unavailable")
	}
	lock := &FileLock{File: file, Path: path}
	if err := lock.SetOwner(owner); err != nil {
		_ = file.Close()
		return nil, err
	}
	return lock, nil
}

func (l *FileLock) SetOwner(owner ProcessOwner) error {
	if owner.PID <= 0 {
		owner.PID = os.Getpid()
	}
	if owner.StartedAt.IsZero() {
		owner.StartedAt = time.Now()
	}
	if _, err := l.File.Seek(0, 0); err != nil {
		return err
	}
	if err := l.File.Truncate(0); err != nil {
		return err
	}
	if err := json.NewEncoder(l.File).Encode(owner); err != nil {
		return err
	}
	if err := l.File.Sync(); err != nil {
		return err
	}
	l.Owner = owner
	return nil
}

func (l *FileLock) Release() {
	if l == nil || l.File == nil {
		return
	}
	_ = syscall.Flock(int(l.File.Fd()), syscall.LOCK_UN)
	_ = l.File.Close()
	l.File = nil
}

func (l *FileLock) CloseTransferredCopy() {
	if l == nil || l.File == nil {
		return
	}
	_ = l.File.Close()
	l.File = nil
}

func ReadProcessOwner(path string) ProcessOwner {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProcessOwner{}
	}
	var owner ProcessOwner
	if json.Unmarshal(data, &owner) != nil {
		return ProcessOwner{}
	}
	return owner
}

func RegisterFrontend(homeDir, configPath string) (*FrontendSession, []ProcessOwner, error) {
	existing, err := ListFrontends()
	if err != nil {
		return nil, nil, err
	}
	runtimeDirectory, err := EnsureRuntimeDirectory()
	if err != nil {
		return nil, nil, err
	}
	sessionDirectory := filepath.Join(
		runtimeDirectory,
		FrontendDirectoryName,
	)
	if err := os.MkdirAll(sessionDirectory, 0o700); err != nil {
		return nil, nil, err
	}
	owner := ProcessOwner{
		Kind:       "tui",
		PID:        os.Getpid(),
		TTY:        TTYName(),
		HomeDir:    homeDir,
		ConfigPath: configPath,
		StartedAt:  time.Now(),
	}
	path := filepath.Join(
		sessionDirectory,
		fmt.Sprintf("%d-%d.lock", owner.PID, owner.StartedAt.UnixNano()),
	)
	lock, err := AcquireFileLock(path, owner)
	if err != nil {
		return nil, nil, err
	}
	return &FrontendSession{Lock: lock}, existing, nil
}

func (s *FrontendSession) Close() {
	if s == nil || s.Lock == nil {
		return
	}
	path := s.Lock.Path
	s.Lock.Release()
	_ = os.Remove(path)
	s.Lock = nil
}

func ListFrontends() ([]ProcessOwner, error) {
	runtimeDirectory, err := RuntimeDirectory()
	if err != nil {
		return nil, err
	}
	sessionDirectory := filepath.Join(
		runtimeDirectory,
		FrontendDirectoryName,
	)
	entries, err := os.ReadDir(sessionDirectory)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	owners := make([]ProcessOwner, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".lock") {
			continue
		}
		path := filepath.Join(sessionDirectory, entry.Name())
		file, openErr := os.OpenFile(path, os.O_RDWR, frontendSessionFileMode)
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
		owner := ReadProcessOwner(path)
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

func ActiveBackendOwner() (ProcessOwner, bool, error) {
	path, err := RuntimeLockPath()
	if err != nil {
		return ProcessOwner{}, false, err
	}
	file, err := os.OpenFile(path, os.O_RDWR, frontendSessionFileMode)
	if os.IsNotExist(err) {
		return ProcessOwner{}, false, nil
	}
	if err != nil {
		return ProcessOwner{}, false, err
	}
	defer file.Close()
	lockErr := syscall.Flock(
		int(file.Fd()),
		syscall.LOCK_EX|syscall.LOCK_NB,
	)
	if lockErr == nil {
		_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
		_ = os.Remove(path)
		return ProcessOwner{}, false, nil
	}
	if !errors.Is(lockErr, syscall.EWOULDBLOCK) &&
		!errors.Is(lockErr, syscall.EAGAIN) {
		return ProcessOwner{}, false, lockErr
	}
	owner := ReadProcessOwner(path)
	if owner.PID <= 0 {
		return ProcessOwner{}, false, errors.New(
			"active Backend lock has no valid owner PID",
		)
	}
	return owner, true, nil
}

func TTYName() string {
	path, err := os.Readlink("/proc/self/fd/0")
	if err != nil || !strings.HasPrefix(path, "/dev/") {
		return ""
	}
	return path
}

func FormatFrontendNotice(existing []ProcessOwner) string {
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

func FormatFrontendSummary(frontends []ProcessOwner) string {
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
