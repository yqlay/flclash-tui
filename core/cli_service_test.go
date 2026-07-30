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
	previousRuntimeDirectory := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = homeDir
	t.Cleanup(func() {
		cliRuntimeDirectoryOverride = previousRuntimeDirectory
	})
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

func TestFindLegacyTUIServiceOutsidePerUserRuntime(t *testing.T) {
	runtimeDirectory := t.TempDir()
	legacyDirectory := t.TempDir()
	previousRuntimeDirectory := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = runtimeDirectory
	t.Cleanup(func() {
		cliRuntimeDirectoryOverride = previousRuntimeDirectory
	})
	socketPath := filepath.Join(
		legacyDirectory,
		tuiServiceSocketFilename,
	)
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
		if request.Action != "status" {
			serverDone <- json.NewEncoder(connection).Encode(
				tuiServiceStatus{
					OK:    false,
					Error: "unexpected action " + request.Action,
				},
			)
			return
		}
		serverDone <- json.NewEncoder(connection).Encode(
			tuiServiceStatus{
				OK:      true,
				PID:     1234,
				Version: "0.3.11",
				ConfigPath: filepath.Join(
					legacyDirectory,
					"config.yaml",
				),
			},
		)
	}()

	client, status, found := findLegacyTUIService(cliPaths{
		homeDir:    legacyDirectory,
		configPath: filepath.Join(legacyDirectory, "config.yaml"),
	})
	if !found || client == nil {
		t.Fatal("legacy service was not discovered")
	}
	if client.socketPath() != socketPath ||
		status.Version != "0.3.11" ||
		status.PID != 1234 {
		t.Fatalf(
			"legacy service = %q, %+v",
			client.socketPath(),
			status,
		)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
