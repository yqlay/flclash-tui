//go:build linux && !cgo && cli

package main

import (
	"bytes"
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
