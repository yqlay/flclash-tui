//go:build linux && !cgo && cli

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestTUIFormatting(t *testing.T) {
	if got := formatBytes(0); got != "0.0 B" {
		t.Fatalf("formatBytes(0) = %q", got)
	}
	if got := formatBytes(1024 * 1024); got != "1.0 MB" {
		t.Fatalf("formatBytes(1 MiB) = %q", got)
	}
	if got := truncateTUI("abcdef", 4); got != "abc…" {
		t.Fatalf("truncateTUI = %q", got)
	}
}

func TestTUIGroupAndSelectionMovement(t *testing.T) {
	if !isTUIGroup("Selector") || !isTUIGroup("urltest") || isTUIGroup("Direct") {
		t.Fatal("unexpected proxy group classification")
	}
	snapshot := tuiSnapshot{Groups: []tuiGroup{
		{Name: "A", Nodes: []string{"a", "b"}},
		{Name: "B", Nodes: []string{"c"}},
	}}
	moveTUIGroup(&snapshot, -1)
	if snapshot.SelectedGroup != 1 || snapshot.SelectedNode != 0 {
		t.Fatalf("group movement = (%d, %d)", snapshot.SelectedGroup, snapshot.SelectedNode)
	}
	moveTUINode(&snapshot, 1)
	if snapshot.SelectedNode != 0 {
		t.Fatalf("node movement = %d", snapshot.SelectedNode)
	}
}

func TestReadTUIKeys(t *testing.T) {
	keys := make(chan tuiKey, 5)
	go readTUIKeys(bytes.NewBufferString("rj\x1b[Aq"), keys)
	want := []tuiKey{tuiKeyRefresh, tuiKeyDown, tuiKeyUp, tuiKeyQuit}
	for _, expected := range want {
		if got := <-keys; got != expected {
			t.Fatalf("key = %v, want %v", got, expected)
		}
	}
}

func TestResolvePathsUsesDirectoryForDefaultConfig(t *testing.T) {
	directory := t.TempDir()
	paths, err := resolvePaths("", directory)
	if err != nil {
		t.Fatal(err)
	}
	wantHome, _ := filepath.Abs(directory)
	wantConfig := filepath.Join(wantHome, "config.yaml")
	if paths.homeDir != wantHome || paths.configPath != wantConfig {
		t.Fatalf("paths = (%q, %q), want (%q, %q)", paths.homeDir, paths.configPath, wantHome, wantConfig)
	}
}

func TestResolvePathsUsesRelativeDirectoryOnce(t *testing.T) {
	directory := filepath.Join("test-data", "instance")
	paths, err := resolvePaths("", directory)
	if err != nil {
		t.Fatal(err)
	}
	wantHome, _ := filepath.Abs(directory)
	wantConfig := filepath.Join(wantHome, "config.yaml")
	if paths.homeDir != wantHome || paths.configPath != wantConfig {
		t.Fatalf("paths = (%q, %q), want (%q, %q)", paths.homeDir, paths.configPath, wantHome, wantConfig)
	}
}

func TestResolvePathsDefaultConfigUsesUserConfigDirectory(t *testing.T) {
	configRoot, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	paths, err := resolvePaths("", "")
	if err != nil {
		t.Fatal(err)
	}
	wantHome, _ := filepath.Abs(filepath.Join(configRoot, "flclash"))
	wantConfig := filepath.Join(wantHome, "config.yaml")
	if paths.homeDir != wantHome || paths.configPath != wantConfig {
		t.Fatalf("paths = (%q, %q), want (%q, %q)", paths.homeDir, paths.configPath, wantHome, wantConfig)
	}
}

func TestEnsureTUIConfigCreatesMinimalConfig(t *testing.T) {
	directory := t.TempDir()
	paths := cliPaths{homeDir: directory, configPath: filepath.Join(directory, "config.yaml")}
	if err := ensureTUIConfig(paths, true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(paths.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if message := validateConfigBytes(data); message != "" {
		t.Fatalf("generated config is invalid: %s", message)
	}
	if err := ensureTUIConfig(paths, true); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(paths.configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != string(data) {
		t.Fatal("existing config was overwritten")
	}
}

func TestRestoreLatestTUIConfigDoesNotOverwriteWithInvalidBackup(t *testing.T) {
	directory := t.TempDir()
	configPath := filepath.Join(directory, "config.yaml")
	original := []byte("mixed-port: 17890\n")
	if err := os.WriteFile(configPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	backupPath := configPath + ".backup-999999999999999999"
	if err := os.WriteFile(backupPath, []byte("mixed-port: [invalid\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := restoreLatestTUIConfig(configPath); err == nil {
		t.Fatal("restore unexpectedly accepted invalid backup")
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(original) {
		t.Fatalf("config changed after invalid restore: %q", data)
	}
}
