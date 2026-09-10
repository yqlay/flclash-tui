//go:build linux && !cgo && cli

package main

import (
	"errors"
	"os"

	clipaths "core/internal/paths"
)

const (
	cliRuntimeLockFilename   = clipaths.RuntimeLockFilename
	cliFrontendDirectoryName = clipaths.FrontendDirectoryName
)

var cliRuntimeDirectoryOverride string

type cliPaths = clipaths.Paths

type cliProcessOwner = clipaths.ProcessOwner

type cliFileLock struct {
	file  *os.File
	path  string
	owner cliProcessOwner
	inner *clipaths.FileLock
}

type cliLockBusyError struct {
	path  string
	owner cliProcessOwner
}

func (e *cliLockBusyError) Error() string {
	return (&clipaths.LockBusyError{Path: e.path, Owner: e.owner}).Error()
}

type cliFrontendSession struct {
	lock *cliFileLock
}

func wrapCLIFileLock(inner *clipaths.FileLock) *cliFileLock {
	if inner == nil {
		return nil
	}
	return &cliFileLock{
		file:  inner.File,
		path:  inner.Path,
		owner: inner.Owner,
		inner: inner,
	}
}

func syncCLIRuntimeOverride() {
	clipaths.DirectoryOverride = cliRuntimeDirectoryOverride
}

func resolvePaths(configArg, directoryArg string) (cliPaths, error) {
	return clipaths.Resolve(configArg, directoryArg)
}

func cliRuntimeDirectory() (string, error) {
	syncCLIRuntimeOverride()
	return clipaths.RuntimeDirectory()
}

func cliPathOwnedByCurrentUser(info os.FileInfo) bool {
	return clipaths.OwnedByCurrentUser(info)
}

func ensureCLIRuntimeDirectory() (string, error) {
	syncCLIRuntimeOverride()
	return clipaths.EnsureRuntimeDirectory()
}

func cliRuntimeLockPath() (string, error) {
	syncCLIRuntimeOverride()
	return clipaths.RuntimeLockPath()
}

func cliServiceSocketPath() (string, error) {
	syncCLIRuntimeOverride()
	return clipaths.SocketPath(tuiServiceSocketFilename)
}

func acquireCLIBackendLock(owner cliProcessOwner) (*cliFileLock, error) {
	syncCLIRuntimeOverride()
	lock, err := clipaths.AcquireBackendLock(owner)
	if err != nil {
		return nil, wrapCLILockError(err)
	}
	return wrapCLIFileLock(lock), nil
}

func acquireCLIFileLock(path string, owner cliProcessOwner) (*cliFileLock, error) {
	syncCLIRuntimeOverride()
	lock, err := clipaths.AcquireFileLock(path, owner)
	if err != nil {
		return nil, wrapCLILockError(err)
	}
	return wrapCLIFileLock(lock), nil
}

func adoptCLIBackendLock(file *os.File, owner cliProcessOwner) (*cliFileLock, error) {
	syncCLIRuntimeOverride()
	lock, err := clipaths.AdoptBackendLock(file, owner)
	if err != nil {
		return nil, wrapCLILockError(err)
	}
	return wrapCLIFileLock(lock), nil
}

func wrapCLILockError(err error) error {
	if err == nil {
		return nil
	}
	var busy *clipaths.LockBusyError
	if errors.As(err, &busy) {
		return &cliLockBusyError{path: busy.Path, owner: busy.Owner}
	}
	return err
}

func (l *cliFileLock) setOwner(owner cliProcessOwner) error {
	if l == nil || l.inner == nil {
		return nil
	}
	if err := l.inner.SetOwner(owner); err != nil {
		return err
	}
	l.file = l.inner.File
	l.path = l.inner.Path
	l.owner = l.inner.Owner
	return nil
}

func (l *cliFileLock) release() {
	if l == nil || l.inner == nil {
		return
	}
	l.inner.Release()
	l.file = l.inner.File
}

func (l *cliFileLock) closeTransferredCopy() {
	if l == nil || l.inner == nil {
		return
	}
	l.inner.CloseTransferredCopy()
	l.file = l.inner.File
}

func readCLIProcessOwner(path string) cliProcessOwner {
	return clipaths.ReadProcessOwner(path)
}

func registerCLIFrontend(homeDir, configPath string) (*cliFrontendSession, []cliProcessOwner, error) {
	syncCLIRuntimeOverride()
	session, existing, err := clipaths.RegisterFrontend(homeDir, configPath)
	if err != nil {
		return nil, nil, wrapCLILockError(err)
	}
	return &cliFrontendSession{lock: wrapCLIFileLock(session.Lock)}, existing, nil
}

func (s *cliFrontendSession) close() {
	if s == nil || s.lock == nil {
		return
	}
	if s.lock.inner != nil {
		session := clipaths.FrontendSession{Lock: s.lock.inner}
		session.Close()
		s.lock.file = s.lock.inner.File
	}
	s.lock = nil
}

func listCLIFrontends() ([]cliProcessOwner, error) {
	syncCLIRuntimeOverride()
	return clipaths.ListFrontends()
}

func activeCLIBackendOwner() (cliProcessOwner, bool, error) {
	syncCLIRuntimeOverride()
	return clipaths.ActiveBackendOwner()
}

func cliTTYName() string {
	return clipaths.TTYName()
}

func formatCLIFrontendNotice(existing []cliProcessOwner) string {
	return clipaths.FormatFrontendNotice(existing)
}

func formatCLIFrontendSummary(frontends []cliProcessOwner) string {
	return clipaths.FormatFrontendSummary(frontends)
}
