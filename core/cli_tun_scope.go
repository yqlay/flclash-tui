//go:build linux && !cgo && cli

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	tuiTunScopeUser     = "user"
	tuiTunScopeSystem   = "system"
	tuiTunScopeFilename = ".flclash-tun-scope"
)

func normalizeTUITunScope(scope string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(scope)) {
	case "", tuiTunScopeUser:
		return tuiTunScopeUser, nil
	case tuiTunScopeSystem:
		return tuiTunScopeSystem, nil
	default:
		return "", errors.New("TUN scope must be user or system")
	}
}

func loadTUITunScope(homeDir string) string {
	data, err := os.ReadFile(filepath.Join(homeDir, tuiTunScopeFilename))
	if err != nil {
		return tuiTunScopeUser
	}
	scope, err := normalizeTUITunScope(string(data))
	if err != nil {
		return tuiTunScopeUser
	}
	return scope
}

func rememberTUITunScope(homeDir, scope string) error {
	normalized, err := normalizeTUITunScope(scope)
	if err != nil {
		return err
	}
	return writeTUIProfileAtomically(filepath.Join(homeDir, tuiTunScopeFilename), []byte(normalized+"\n"), 0o600)
}
