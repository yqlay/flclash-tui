//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

type tuiServiceMemoryConnection struct {
	reader   *bytes.Reader
	written  bytes.Buffer
	writeErr error
}

func (connection *tuiServiceMemoryConnection) Read(data []byte) (int, error) {
	return connection.reader.Read(data)
}

func (connection *tuiServiceMemoryConnection) Write(data []byte) (int, error) {
	if connection.writeErr != nil {
		return 0, connection.writeErr
	}
	return connection.written.Write(data)
}

func (connection *tuiServiceMemoryConnection) Close() error { return nil }

func (connection *tuiServiceMemoryConnection) LocalAddr() net.Addr { return nil }

func (connection *tuiServiceMemoryConnection) RemoteAddr() net.Addr { return nil }

func (connection *tuiServiceMemoryConnection) SetDeadline(time.Time) error { return nil }

func (connection *tuiServiceMemoryConnection) SetReadDeadline(time.Time) error { return nil }

func (connection *tuiServiceMemoryConnection) SetWriteDeadline(time.Time) error { return nil }

func TestReapTUIServiceProcessWaitsForChild(t *testing.T) {
	command := exec.Command("/bin/sh", "-c", "exit 0")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	pid := command.Process.Pid
	select {
	case err := <-reapTUIServiceProcess(command):
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("child process was not reaped")
	}
	if command.ProcessState == nil || !command.ProcessState.Exited() {
		t.Fatalf("child process state = %#v", command.ProcessState)
	}
	var status syscall.WaitStatus
	if waited, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil); waited != -1 || !errors.Is(err, syscall.ECHILD) {
		t.Fatalf("child remained waitable: pid=%d err=%v", waited, err)
	}
}

func TestTUIServiceShutdownWritesACKBeforeSignallingExit(t *testing.T) {
	directory := t.TempDir()
	var connection *tuiServiceMemoryConnection
	shutdownCalled := false
	runtime := newTUIServiceRuntime(
		cliPaths{
			homeDir:    directory,
			configPath: filepath.Join(directory, "config.yaml"),
		},
		defaultCLITestURL,
		filepath.Join(directory, "core.sock"),
		nil,
		func() {
			var status tuiServiceStatus
			if err := json.Unmarshal(bytes.TrimSpace(connection.written.Bytes()), &status); err != nil {
				t.Errorf("shutdown signalled before a complete ACK was written: %v", err)
			} else if !status.OK || !status.ShuttingDown {
				t.Errorf("shutdown ACK = %+v", status)
			}
			shutdownCalled = true
		},
	)
	request, err := json.Marshal(tuiServiceRequest{
		ProtocolVersion: tuiServiceProtocolVersion,
		RequestID:       "shutdown-with-ack",
		Action:          "shutdown",
	})
	if err != nil {
		t.Fatal(err)
	}
	connection = &tuiServiceMemoryConnection{reader: bytes.NewReader(request)}
	serveTUIServiceConnection(runtime, connection)
	if !shutdownCalled {
		t.Fatal("backend exit was not signalled after the shutdown ACK")
	}
}

func TestTUIServiceShutdownDoesNotExitWhenACKWriteFails(t *testing.T) {
	directory := t.TempDir()
	shutdownCalled := false
	runtime := newTUIServiceRuntime(
		cliPaths{
			homeDir:    directory,
			configPath: filepath.Join(directory, "config.yaml"),
		},
		defaultCLITestURL,
		filepath.Join(directory, "core.sock"),
		nil,
		func() { shutdownCalled = true },
	)
	request, err := json.Marshal(tuiServiceRequest{
		ProtocolVersion: tuiServiceProtocolVersion,
		RequestID:       "shutdown-write-failure",
		Action:          "shutdown",
	})
	if err != nil {
		t.Fatal(err)
	}
	connection := &tuiServiceMemoryConnection{
		reader:   bytes.NewReader(request),
		writeErr: io.ErrClosedPipe,
	}
	serveTUIServiceConnection(runtime, connection)
	if shutdownCalled {
		t.Fatal("backend exited even though the shutdown ACK was not delivered")
	}
}

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
		if request.ProtocolVersion != tuiServiceProtocolVersion || request.RequestID == "" {
			serverDone <- fmt.Errorf(
				"request identity = protocol %d, id %q",
				request.ProtocolVersion,
				request.RequestID,
			)
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

func TestValidateCurrentTUIServiceRequiresVersionedProtocol(t *testing.T) {
	if err := validateCurrentTUIService(tuiServiceStatus{
		Version:         cliVersion,
		ProtocolVersion: tuiServiceProtocolVersion,
	}); err != nil {
		t.Fatalf("current backend was rejected: %v", err)
	}
	for _, status := range []tuiServiceStatus{
		{Version: "0.3.15", ProtocolVersion: 0},
		{Version: cliVersion, ProtocolVersion: tuiServiceProtocolVersion - 1},
	} {
		if err := validateCurrentTUIService(status); err == nil {
			t.Fatalf("outdated backend was accepted: %+v", status)
		}
	}
}
