//go:build linux && !cgo && cli

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"gopkg.in/yaml.v3"
)

// refreshTUIManagedRuntimeTunFD prepares an existing generated runtime for
// another Core initialization. sing-tun owns (and closes) file-descriptor, so
// a YAML from a previously running Core cannot safely reuse that integer.
// Never change user profiles and never close the old integer: a different file
// may already occupy it. The returned duplicate belongs to the caller until
// SetupConfig transfers ownership to Core.
func refreshTUIManagedRuntimeTunFD(path string, lease *tuiTunLease) (int, error) {
	if !strings.HasPrefix(filepath.Base(path), tuiManagedRuntimeConfigPrefix) {
		return 0, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return 0, err
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return 0, errors.New("managed runtime root must be a YAML mapping")
	}
	tun := tuiYAMLMappingValue(document.Content[0], "tun")
	if tun == nil || tun.Kind != yaml.MappingNode {
		return 0, nil
	}
	enabled := tuiYAMLMappingValue(tun, "enable")
	if enabled == nil || !strings.EqualFold(enabled.Value, "true") {
		return 0, nil
	}
	fd, err := lease.duplicateFD()
	if err != nil {
		return 0, fmt.Errorf("duplicate rollback TUN lease: %w", err)
	}
	setTUIYAMLScalar(tun, "file-descriptor", strconv.Itoa(fd), "!!int")
	updated, err := yaml.Marshal(&document)
	if err == nil {
		err = writeTUIProfileAtomically(path, updated, 0o600)
	}
	if err != nil {
		_ = syscall.Close(fd)
		return 0, err
	}
	return fd, nil
}
