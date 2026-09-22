//go:build linux && !cgo && cli

package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/proxy"
)

func TestSSHRelayUpstreamHandshakeHonorsCancellation(t *testing.T) {
	listener := listenCLITestTCP(t)
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, err := listener.Accept()
		if err == nil {
			accepted <- connection
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		connection, err := dialCLISSHRelayUpstream(ctx, listener.Addr().(*net.TCPAddr).Port, "example.test:443")
		if connection != nil {
			connection.Close()
		}
		done <- err
	}()
	var upstream net.Conn
	select {
	case upstream = <-accepted:
		defer upstream.Close()
	case <-time.After(2 * time.Second):
		t.Fatal("upstream not contacted")
	}
	// The server accepts TCP but never responds to the SOCKS greeting.
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("stalled SOCKS handshake unexpectedly succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not release stalled handshake")
	}
	upstream.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := io.Copy(io.Discard, upstream); err != nil {
		t.Fatalf("cancelled handshake did not close upstream: %v", err)
	}
}

func TestCLISSHRelayMetersSOCKS5Traffic(t *testing.T) {
	echo := listenCLITestTCP(t)
	defer echo.Close()
	go acceptCLITestEcho(echo)

	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	go acceptCLITestSOCKS(upstream)

	publicPort := availableCLITestPort(t)
	relay := &cliSSHRelay{
		listenPort:   publicPort,
		upstreamPort: upstream.Addr().(*net.TCPAddr).Port,
		controlPath:  filepath.Join(t.TempDir(), "relay.sock"),
		startedAt:    time.Now(),
		shutdown:     make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() { done <- relay.run() }()
	deadline := time.Now().Add(2 * time.Second)
	for relay.listener == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if relay.listener == nil {
		t.Fatal("relay listener did not become ready")
	}

	dialer, err := proxy.SOCKS5(
		"tcp",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(publicPort)),
		nil,
		proxy.Direct,
	)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := dialer.Dial("tcp", echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("metered-over-ssh\n"), 4096)
	if _, err := connection.Write(payload); err != nil {
		t.Fatal(err)
	}
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(connection, received); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, payload) {
		t.Fatal("relay changed the proxied payload")
	}
	_ = connection.Close()

	deadline = time.Now().Add(time.Second)
	for (relay.upload.Load() < int64(len(payload)) || relay.download.Load() < int64(len(payload))) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if relay.upload.Load() < int64(len(payload)) || relay.download.Load() < int64(len(payload)) {
		t.Fatalf("metered totals = ↑%d ↓%d, want at least %d each", relay.upload.Load(), relay.download.Load(), len(payload))
	}
	flows := relay.listFlows()
	if len(flows) == 0 {
		t.Fatal("relay did not record the SOCKS flow")
	}
	foundLive := false
	for _, flow := range flows {
		if flow.Active && flow.Host == echo.Addr().String() {
			foundLive = true
			if !strings.HasPrefix(flow.ID, cliSSHFlowIDPrefix) {
				t.Fatalf("flow id = %q", flow.ID)
			}
		}
	}
	if !foundLive {
		t.Fatalf("missing live flow for %s: %+v", echo.Addr(), flows)
	}
	relay.stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCLISSHRelayCloseStopsActiveFlow(t *testing.T) {
	echo := listenCLITestTCP(t)
	defer echo.Close()
	go acceptCLITestEcho(echo)
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	go acceptCLITestSOCKS(upstream)
	publicPort := availableCLITestPort(t)
	relay := &cliSSHRelay{
		listenPort:   publicPort,
		upstreamPort: upstream.Addr().(*net.TCPAddr).Port,
		controlPath:  filepath.Join(t.TempDir(), "relay.sock"),
		profile:      "home",
		startedAt:    time.Now(),
		shutdown:     make(chan struct{}),
		active:       map[string]*cliSSHRelayFlowState{},
	}
	done := make(chan error, 1)
	go func() { done <- relay.run() }()
	deadline := time.Now().Add(2 * time.Second)
	for relay.listener == nil && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if relay.listener == nil {
		t.Fatal("relay listener did not become ready")
	}
	dialer, err := proxy.SOCKS5(
		"tcp",
		net.JoinHostPort("127.0.0.1", strconv.Itoa(publicPort)),
		nil,
		proxy.Direct,
	)
	if err != nil {
		t.Fatal(err)
	}
	connection, err := dialer.Dial("tcp", echo.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	var id string
	for time.Now().Before(deadline) && id == "" {
		for _, flow := range relay.listFlows() {
			if flow.Active {
				id = flow.ID
				if flow.Chain != "SSH · home" {
					t.Fatalf("chain = %q", flow.Chain)
				}
				break
			}
		}
		time.Sleep(time.Millisecond)
	}
	if id == "" {
		t.Fatal("no active flow to close")
	}
	if err := relay.closeFlows(id); err != nil {
		t.Fatal(err)
	}
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		_, _ = io.Copy(io.Discard, connection)
	}()
	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		t.Fatal("close did not end the SOCKS session")
	}
	_ = connection.Close()
	relay.stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCLISSHDynamicForwardUsesHiddenUpstreamPort(t *testing.T) {
	arguments := cliSSHDynamicForwardArguments(cliSSHTunnelState{
		Destination:  "student@example.edu",
		Port:         1080,
		UpstreamPort: 49152,
		ControlPath:  "/tmp/control.sock",
	})
	joined := " " + strings.Join(arguments, " ") + " "
	if !strings.Contains(joined, " -D 127.0.0.1:49152 ") || strings.Contains(joined, " -D 127.0.0.1:1080 ") {
		t.Fatalf("dynamic forward exposed the wrong port: %v", arguments)
	}
}

func listenCLITestTCP(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return listener
}

func availableCLITestPort(t *testing.T) int {
	t.Helper()
	listener := listenCLITestTCP(t)
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func acceptCLITestEcho(listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer connection.Close()
			_, _ = io.Copy(connection, connection)
		}()
	}
}

func acceptCLITestSOCKS(listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		go func() {
			defer connection.Close()
			target, err := readCLIInboundSOCKS5(connection)
			if err != nil {
				return
			}
			upstream, err := net.Dial("tcp", target)
			if err != nil {
				_ = writeCLISOCKS5Reply(connection, 0x05)
				return
			}
			defer upstream.Close()
			if writeCLISOCKS5Reply(connection, 0) != nil {
				return
			}
			go io.Copy(upstream, connection)
			_, _ = io.Copy(connection, upstream)
		}()
	}
}
