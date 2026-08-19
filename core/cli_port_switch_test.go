//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestLinuxSystemProxyUpdateDisablesBeforeChangingPorts(t *testing.T) {
	schema := "org.gnome.system.proxy"
	commands := linuxSystemProxyCommands(schema, 17890, true)
	if len(commands) < 3 {
		t.Fatalf("system proxy commands = %#v", commands)
	}
	if want := []string{schema, "mode", "none"}; !reflect.DeepEqual(commands[0], want) {
		t.Fatalf("first system proxy command = %#v, want %#v", commands[0], want)
	}
	if want := []string{schema, "mode", "manual"}; !reflect.DeepEqual(commands[len(commands)-1], want) {
		t.Fatalf(
			"last system proxy command = %#v, want %#v",
			commands[len(commands)-1],
			want,
		)
	}
	for _, suffix := range []string{".http", ".https", ".socks"} {
		want := []string{schema + suffix, "port", "17890"}
		found := false
		for _, command := range commands {
			if reflect.DeepEqual(command, want) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("system proxy commands have no %#v: %#v", want, commands)
		}
	}
}

func TestEnsureTUIProxyPortFreeChecksUDP(t *testing.T) {
	port := freeTUITestPort(t)
	address, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.ListenUDP("udp4", address)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err := ensureTUIProxyPortFree(port); err == nil {
		t.Fatal("UDP-only proxy port conflict was accepted")
	}
}

func TestTUIServicePortSwitchFallsBackWhenConfiguredPortIsOccupied(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "flclash-port-switch-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	previousRuntimeDirectory := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = directory
	t.Cleanup(func() { cliRuntimeDirectoryOverride = previousRuntimeDirectory })

	oldPort := freeTUITestPort(t)
	targetPort := freeTUITestPort(t)
	for targetPort == oldPort {
		targetPort = freeTUITestPort(t)
	}
	configPath := filepath.Join(directory, "config.yaml")
	source := fmt.Appendf(nil, `mixed-port: %d
mode: rule
log-level: silent
proxy-groups:
  - name: PROXY
    type: select
    proxies: [DIRECT]
rules:
  - MATCH,PROXY
`, oldPort)
	if err := os.WriteFile(configPath, source, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rememberTUITrafficMode(directory, "rule"); err != nil {
		t.Fatal(err)
	}

	serviceDone := make(chan error, 1)
	go func() {
		serviceDone <- runTUIService(
			cliPaths{homeDir: directory, configPath: configPath},
			defaultCLITestURL,
			nil,
			false,
		)
	}()
	service := newTUIServiceClient(directory)
	shutdown := true
	t.Cleanup(func() {
		if shutdown {
			_ = service.shutdown()
		}
	})
	status := waitForTUIServiceStatus(t, service)
	status, err = service.startAtRevision(status.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !waitForTUIProxyPortState(oldPort, true, tuiListenerValidationTimeout) {
		t.Fatal("old proxy listener did not start")
	}

	blocker, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", targetPort))
	if err != nil {
		t.Fatal(err)
	}
	settings := loadTUIConfiguredSettings(configPath, true)
	if settings == nil {
		t.Fatal("could not load active settings")
	}
	settings.MixedPort = targetPort
	fallbackStatus, err := service.applySettings(*settings, status.Revision)
	if err != nil {
		_ = blocker.Close()
		t.Fatal(err)
	}
	if !fallbackStatus.Running || fallbackStatus.ConfiguredProxyPort != targetPort ||
		fallbackStatus.ActiveProxyPort == targetPort || fallbackStatus.ActiveProxyPort == oldPort {
		_ = blocker.Close()
		t.Fatalf("fallback switch status = %+v", fallbackStatus)
	}
	if current := loadTUIConfiguredSettings(configPath, true); current == nil ||
		current.MixedPort != targetPort {
		_ = blocker.Close()
		t.Fatalf("fallback switch did not persist preferred port: %+v", current)
	}
	if !waitForTUIProxyPortState(oldPort, false, tuiListenerValidationTimeout) {
		_ = blocker.Close()
		t.Fatal("fallback switch left the old proxy listener open")
	}
	if err := blocker.Close(); err != nil {
		t.Fatal(err)
	}

	status = fallbackStatus
	status, err = service.applySettings(*settings, status.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if status.ProxyPort != targetPort || !status.Running {
		t.Fatalf("successful switch status = %+v", status)
	}
	if !waitForTUIProxyPortState(targetPort, true, tuiListenerValidationTimeout) {
		t.Fatal("target proxy listener did not become ready")
	}
	if !waitForTUIProxyPortState(oldPort, false, tuiListenerValidationTimeout) {
		t.Fatal("old proxy listener remained open after the switch")
	}
	settings.MixedPort = 0
	status, err = service.applySettings(*settings, status.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if status.ProxyPort != 0 || !status.Running {
		t.Fatalf("disabled-port status = %+v", status)
	}
	if !waitForTUIProxyPortState(targetPort, false, tuiListenerValidationTimeout) {
		t.Fatal("target proxy listener remained open after disabling the port")
	}

	if err := service.shutdown(); err != nil {
		t.Fatal(err)
	}
	shutdown = false
	select {
	case err := <-serviceDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Backend did not exit after shutdown ACK")
	}
}

func waitForTUIServiceStatus(
	t *testing.T,
	service *tuiServiceClient,
) tuiServiceStatus {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var status tuiServiceStatus
	var err error
	for time.Now().Before(deadline) {
		status, err = service.status()
		if err == nil {
			return status
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("Backend did not start: %v", err)
	return tuiServiceStatus{}
}
