//go:build linux && !cgo && cli

package main

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const tuiProfileLockDirectory = ".flclash-profile-locks"

type tuiProfileLockLease struct {
	locks []*cliFileLock
}

func acquireTUIProfileLocks(
	homeDir string,
	paths ...string,
) (*tuiProfileLockLease, error) {
	type lockTarget struct {
		path     string
		lockPath string
	}
	targets := make([]lockTarget, 0, len(paths))
	seen := map[string]bool{}
	for _, path := range paths {
		absolutePath, err := filepath.Abs(path)
		if err != nil {
			return nil, err
		}
		absolutePath = filepath.Clean(absolutePath)
		if _, err := tuiProfileStateKey(homeDir, absolutePath); err != nil {
			return nil, err
		}
		digest := sha256.Sum256([]byte(absolutePath))
		lockPath := filepath.Join(
			homeDir,
			tuiProfileLockDirectory,
			fmt.Sprintf("%x.lock", digest),
		)
		if seen[lockPath] {
			continue
		}
		seen[lockPath] = true
		targets = append(targets, lockTarget{path: absolutePath, lockPath: lockPath})
	}
	sort.Slice(targets, func(left, right int) bool {
		return targets[left].lockPath < targets[right].lockPath
	})
	lease := &tuiProfileLockLease{}
	for _, target := range targets {
		owner := cliProcessOwner{
			Kind:       "profile-update",
			PID:        os.Getpid(),
			HomeDir:    homeDir,
			ConfigPath: target.path,
			StartedAt:  time.Now(),
		}
		deadline := time.Now().Add(2 * time.Second)
		for {
			lock, err := acquireCLIFileLock(target.lockPath, owner)
			if err == nil {
				lease.locks = append(lease.locks, lock)
				break
			}
			var busy *cliLockBusyError
			if !errors.As(err, &busy) || time.Now().After(deadline) {
				lease.release()
				if errors.As(err, &busy) {
					return nil, fmt.Errorf(
						"profile %q is being modified by another frontend",
						filepath.Base(target.path),
					)
				}
				return nil, err
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	return lease, nil
}

func (l *tuiProfileLockLease) release() {
	if l == nil {
		return
	}
	for index := len(l.locks) - 1; index >= 0; index-- {
		l.locks[index].release()
	}
	l.locks = nil
}

func tuiBytesSHA256(data []byte) string {
	digest := sha256.Sum256(data)
	return fmt.Sprintf("%x", digest)
}

func tuiFileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return tuiBytesSHA256(data), nil
}
