//go:build linux && !cgo && cli

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/metacubex/mihomo/component/geodata/memconservative"
	"github.com/metacubex/mihomo/component/mmdb"
)

const (
	tuiGeoDataDirectoryEnv = "FLCLASH_CLI_DATA_DIR"
	tuiSystemGeoDataDir    = "/usr/share/flclash-cli/data"
)

type tuiBundledGeoFile struct {
	name           string
	aliases        []string
	isMMDB         bool
	validationCode string
}

var tuiBundledGeoFiles = []tuiBundledGeoFile{
	{
		name:    "GEOIP.metadb",
		aliases: []string{"Country.mmdb", "geoip.db", "geoip.metadb"},
		isMMDB:  true,
	},
	{
		name:           "GEOIP.dat",
		aliases:        []string{"GeoIP.dat"},
		validationCode: "CN",
	},
	{
		name:           "GEOSITE.dat",
		aliases:        []string{"GeoSite.dat"},
		validationCode: "CN",
	},
	{
		name:    "ASN.mmdb",
		aliases: []string{"ASN.mmdb"},
		isMMDB:  true,
	},
}

func ensureTUIBundledGeoData(homeDir string) error {
	directories := tuiBundledGeoDataDirectories()
	for _, geoFile := range tuiBundledGeoFiles {
		targetPath, targetExists, err := findTUIExistingGeoTarget(
			homeDir,
			geoFile,
		)
		if err != nil {
			return err
		}
		if targetExists && tuiGeoTargetIsUsable(targetPath, geoFile) {
			continue
		}
		sourcePath := findTUIBundledGeoSource(directories, geoFile.name)
		if sourcePath == "" {
			continue
		}
		if !tuiGeoTargetIsUsable(sourcePath, geoFile) {
			return fmt.Errorf("bundled Geo database %q is invalid", sourcePath)
		}
		if err := copyTUIBundledGeoFile(sourcePath, targetPath); err != nil {
			return fmt.Errorf("install bundled Geo data %s: %w", geoFile.name, err)
		}
	}
	return nil
}

func tuiBundledGeoDataDirectories() []string {
	directories := make([]string, 0, 3)
	if configured := strings.TrimSpace(os.Getenv(tuiGeoDataDirectoryEnv)); configured != "" {
		directories = append(directories, configured)
	}
	if executable, err := os.Executable(); err == nil {
		directories = append(
			directories,
			filepath.Join(filepath.Dir(executable), "data"),
		)
	}
	directories = append(directories, tuiSystemGeoDataDir)
	return directories
}

func findTUIBundledGeoSource(directories []string, name string) string {
	for _, directory := range directories {
		sourcePath := filepath.Join(directory, name)
		if info, err := os.Stat(sourcePath); err == nil &&
			info.Mode().IsRegular() &&
			info.Size() > 0 {
			return sourcePath
		}
	}
	return ""
}

func findTUIExistingGeoTarget(
	homeDir string,
	geoFile tuiBundledGeoFile,
) (string, bool, error) {
	entries, err := os.ReadDir(homeDir)
	if err != nil {
		return "", false, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		for _, alias := range geoFile.aliases {
			if strings.EqualFold(entry.Name(), alias) {
				return filepath.Join(homeDir, entry.Name()), true, nil
			}
		}
	}
	return filepath.Join(homeDir, geoFile.name), false, nil
}

func tuiGeoTargetIsUsable(path string, geoFile tuiBundledGeoFile) bool {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return false
	}
	if geoFile.isMMDB {
		return mmdb.Verify(path)
	}
	if geoFile.validationCode != "" {
		data, decodeErr := memconservative.Decode(
			path,
			geoFile.validationCode,
		)
		return decodeErr == nil && len(data) > 0
	}
	return true
}

func copyTUIBundledGeoFile(sourcePath, targetPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()

	targetDirectory := filepath.Dir(targetPath)
	temporary, err := os.CreateTemp(
		targetDirectory,
		"."+filepath.Base(targetPath)+".tmp-*",
	)
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return err
	}
	_, copyErr := io.Copy(temporary, source)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(temporaryPath, targetPath)
}
