//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"
)

func TestTUIServiceReloadUsesExtendedTimeout(t *testing.T) {
	homeDir := t.TempDir()
	socketPath := filepath.Join(homeDir, tuiServiceSocketFilename)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serverDone := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer connection.Close()
		var request tuiServiceRequest
		if decodeErr := json.NewDecoder(
			bufio.NewReader(connection),
		).Decode(&request); decodeErr != nil {
			serverDone <- decodeErr
			return
		}
		time.Sleep(50 * time.Millisecond)
		serverDone <- json.NewEncoder(connection).Encode(tuiServiceStatus{
			OK:      true,
			Running: true,
		})
	}()

	client := &tuiServiceClient{
		homeDir:       homeDir,
		timeout:       10 * time.Millisecond,
		reloadTimeout: 250 * time.Millisecond,
	}
	status, err := client.reload(filepath.Join(homeDir, "profile.yaml"))
	if err != nil {
		t.Fatalf("reload used the ordinary short timeout: %v", err)
	}
	if !status.Running {
		t.Fatal("reload response was not decoded")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
