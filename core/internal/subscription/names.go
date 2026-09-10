//go:build linux && !cgo && cli

package subscription

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	silentRuntimePrefix  = ".flclash-silent-runtime-"
	managedRuntimePrefix = ".flclash-managed-runtime-"
)

func tuiImportedProfileName(sourceName string) string {
	base := filepath.Base(strings.TrimSpace(sourceName))
	extension := strings.ToLower(filepath.Ext(base))
	if extension == ".yaml" || extension == ".yml" {
		return base
	}
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if stem == "" || stem == "." || stem == ".." {
		stem = "imported-profile"
	}
	return stem + ".yaml"
}

func nextTUIImportedProfilePath(homeDir, sourceName string) (string, error) {
	extension := strings.ToLower(filepath.Ext(sourceName))
	stem := strings.TrimSuffix(filepath.Base(sourceName), filepath.Ext(sourceName))
	if extension != ".yaml" && extension != ".yml" || stem == "" {
		return "", errors.New("profile file must end in .yaml or .yml")
	}
	if isRuntimeProfileName(sourceName) {
		stem = "imported-" + strings.TrimLeft(stem, ".")
	}
	for suffix := 1; suffix <= 10000; suffix++ {
		name := stem + extension
		if suffix > 1 {
			name = fmt.Sprintf("%s-%d%s", stem, suffix, extension)
		}
		path := filepath.Join(homeDir, name)
		if _, err := os.Lstat(path); os.IsNotExist(err) {
			return path, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate a unique profile name")
}

func isRuntimeProfileName(name string) bool {
	extension := strings.ToLower(filepath.Ext(name))
	if extension != ".yaml" && extension != ".yml" {
		return false
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	return strings.HasPrefix(base, silentRuntimePrefix) ||
		strings.HasPrefix(base, managedRuntimePrefix)
}
