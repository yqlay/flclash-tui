//go:build linux && !cgo && cli

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/metacubex/mihomo/component/mmdb"
)

func TestEnsureTUIBundledGeoDataSeedsOfflineAssets(t *testing.T) {
	assetDirectory, err := filepath.Abs(filepath.Join("..", "assets", "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(tuiGeoDataDirectoryEnv, assetDirectory)
	homeDir := t.TempDir()

	if err := os.WriteFile(
		filepath.Join(homeDir, "geoip.metadb"),
		[]byte("incomplete download"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(homeDir, "GeoIP.dat"),
		[]byte("incomplete GeoIP download"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	if err := ensureTUIBundledGeoData(homeDir); err != nil {
		t.Fatal(err)
	}

	mmdbPath := filepath.Join(homeDir, "geoip.metadb")
	if !mmdb.Verify(mmdbPath) {
		t.Fatal("partial MMDB was not replaced by the bundled database")
	}
	info, err := os.Stat(mmdbPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("seeded MMDB mode = %v", info.Mode().Perm())
	}
	for _, name := range []string{"GEOSITE.dat", "ASN.mmdb"} {
		info, statErr := os.Stat(filepath.Join(homeDir, name))
		if statErr != nil || info.Size() == 0 {
			t.Fatalf("bundled %s was not seeded: info=%v err=%v", name, info, statErr)
		}
	}
	geoIPFile := tuiBundledGeoFiles[1]
	if !tuiGeoTargetIsUsable(filepath.Join(homeDir, "GeoIP.dat"), geoIPFile) {
		t.Fatal("partial GeoIP.dat was not replaced by the bundled database")
	}
}

func TestEnsureTUIBundledGeoDataPreservesValidMMDB(t *testing.T) {
	assetDirectory, err := filepath.Abs(filepath.Join("..", "assets", "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(tuiGeoDataDirectoryEnv, assetDirectory)
	homeDir := t.TempDir()
	sourcePath := filepath.Join(assetDirectory, "GEOIP.metadb")
	targetPath := filepath.Join(homeDir, "Country.mmdb")
	if err := copyTUIBundledGeoFile(sourcePath, targetPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(targetPath, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ensureTUIBundledGeoData(homeDir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatal("valid user MMDB was replaced")
	}
}

func TestEnsureTUIBundledGeoDataPreservesValidDAT(t *testing.T) {
	assetDirectory, err := filepath.Abs(filepath.Join("..", "assets", "data"))
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(tuiGeoDataDirectoryEnv, assetDirectory)
	homeDir := t.TempDir()
	sourcePath := filepath.Join(assetDirectory, "GEOIP.dat")
	targetPath := filepath.Join(homeDir, "GeoIP.dat")
	if err := copyTUIBundledGeoFile(sourcePath, targetPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(targetPath, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := ensureTUIBundledGeoData(homeDir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatal("valid user GeoIP.dat was replaced")
	}
}
