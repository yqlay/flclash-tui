//go:build linux && !cgo && cli

package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTUIHistoryPersistsRestoresAndClears(t *testing.T) {
	directory := t.TempDir()
	now := time.Now().UTC().Truncate(time.Second)
	entries := []tuiRequest{{
		tuiConnection: tuiConnection{
			ID:      "connection-1",
			Host:    "example.test",
			Network: "tcp",
			Chain:   "PROXY",
		},
		FirstSeen: now.Add(-time.Minute),
		LastSeen:  now,
		Active:    true,
	}}
	if err := saveTUIHistory(directory, entries); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(directory, tuiHistoryFilename))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("History mode = %o, want 600", info.Mode().Perm())
	}
	restored, err := loadTUIHistory(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0].ID != entries[0].ID || restored[0].Active {
		t.Fatalf("restored History = %+v", restored)
	}
	runtime := newTUIServiceRuntime(
		cliPaths{homeDir: directory, configPath: filepath.Join(directory, "config.yaml")},
		defaultCLITestURL,
		filepath.Join(directory, "core.sock"),
		nil,
		nil,
	)
	if err := runtime.restoreHistory(); err != nil {
		t.Fatal(err)
	}
	changed, err := runtime.clearPersistentHistory()
	if err != nil || !changed {
		t.Fatalf("clear History = %t, %v", changed, err)
	}
	restored, err = loadTUIHistory(directory)
	if err != nil || len(restored) != 0 {
		t.Fatalf("cleared persisted History = %+v, %v", restored, err)
	}
}

func TestFilterCLIHistoryCombinesStateSearchAndLimit(t *testing.T) {
	history := []tuiRequest{
		{tuiConnection: tuiConnection{ID: "1", Host: "api.example", Process: "curl", Network: "tcp", Chain: "PROXY"}, Active: true},
		{tuiConnection: tuiConnection{ID: "2", Host: "other.example", Process: "browser", Network: "tcp", Chain: "DIRECT"}},
		{tuiConnection: tuiConnection{ID: "3", Host: "cdn.example", Process: "curl", Network: "udp", Chain: "PROXY"}},
	}
	filtered := filterCLIHistory(history, "done", "curl", 1)
	if len(filtered) != 1 || filtered[0].ID != "3" {
		t.Fatalf("filtered History = %+v", filtered)
	}
}

func TestLoadTUIHistoryRejectsCorruptState(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(directory, tuiHistoryFilename),
		[]byte("not-json"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := loadTUIHistory(directory); err == nil {
		t.Fatal("corrupt History was accepted")
	}
}
