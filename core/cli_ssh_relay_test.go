//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

func TestCLISSHRelayRejectsSOCKS5(t *testing.T) {
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	relay, publicPort, done := startCLITestRelay(t, upstream.Addr().(*net.TCPAddr).Port)
	connection, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(publicPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()
	if _, err := connection.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	_ = connection.SetDeadline(time.Now().Add(time.Second))
	reply := make([]byte, 2)
	n, err := connection.Read(reply)
	if n > 0 && reply[0] == 5 {
		t.Fatalf("public port answered SOCKS5: %v", reply[:n])
	}
	if err == nil && n > 0 && reply[0] == 5 {
		t.Fatal("public port still speaks SOCKS5")
	}
	if relay.upload.Load() != 0 || relay.download.Load() != 0 || len(relay.listFlows()) != 0 {
		t.Fatalf("rejected SOCKS5 was recorded ↑%d ↓%d flows=%+v", relay.upload.Load(), relay.download.Load(), relay.listFlows())
	}
	relay.stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCLISSHRelayMetersHTTPConnectTraffic(t *testing.T) {
	echo := listenCLITestTCP(t)
	defer echo.Close()
	go acceptCLITestEcho(echo)
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	go acceptCLITestSOCKS(upstream)
	relay, publicPort, done := startCLITestRelay(t, upstream.Addr().(*net.TCPAddr).Port)
	if err := probeCLIHTTPProxy(publicPort, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := probeCLISSHSOCKS(publicPort, time.Second); err == nil {
		t.Fatal("public HTTP proxy accepted a SOCKS5 greeting")
	}
	if relay.upload.Load() != 0 || relay.download.Load() != 0 {
		t.Fatalf("protocol probes were metered ↑%d ↓%d", relay.upload.Load(), relay.download.Load())
	}

	connection, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(publicPort)))
	if err != nil {
		t.Fatal(err)
	}
	target := echo.Addr().String()
	if _, err := fmt.Fprintf(connection, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(connection)
	status := readCLITestHTTPStatus(t, reader)
	if !strings.Contains(status, " 200 ") {
		t.Fatalf("CONNECT status = %q", status)
	}
	payload := bytes.Repeat([]byte("metered-over-http\n"), 4096)
	if _, err := connection.Write(payload); err != nil {
		t.Fatal(err)
	}
	received := make([]byte, len(payload))
	if _, err := io.ReadFull(reader, received); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(received, payload) {
		t.Fatal("HTTP relay changed the tunneled payload")
	}
	_ = connection.Close()
	waitCLITestRelayTotals(t, relay, len(payload))
	if !cliTestRelayHasHost(relay, target) {
		t.Fatalf("missing live HTTP flow for %s: %+v", target, relay.listFlows())
	}
	relay.stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestCLISSHRelayMetersAbsoluteHTTP(t *testing.T) {
	const payload = "metered-http-request"
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/meter" {
			t.Errorf("forwarded request URI = %q", r.URL.RequestURI())
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		if string(body) != payload {
			t.Errorf("forwarded body = %q", body)
		}
		_, _ = io.WriteString(w, payload)
	}))
	defer origin.Close()
	upstream := listenCLITestTCP(t)
	defer upstream.Close()
	go acceptCLITestSOCKS(upstream)
	relay, publicPort, done := startCLITestRelay(t, upstream.Addr().(*net.TCPAddr).Port)
	proxyURL, err := url.Parse("http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(publicPort)))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	response, err := client.Post(origin.URL+"/meter", "text/plain", strings.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	received, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(received) != payload {
		t.Fatalf("absolute HTTP response = %s %q", response.Status, received)
	}
	waitCLITestRelayTotals(t, relay, len(payload))
	if !cliTestRelayHasHost(relay, origin.Listener.Addr().String()) {
		t.Fatalf("missing absolute-form flow for %s: %+v", origin.Listener.Addr(), relay.listFlows())
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
	connection := dialCLITestHTTPConnect(t, publicPort, echo.Addr().String())
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
		t.Fatal("close did not end the HTTP tunnel")
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

func startCLITestRelay(t *testing.T, upstreamPort int) (*cliSSHRelay, int, chan error) {
	t.Helper()
	publicPort := availableCLITestPort(t)
	relay := &cliSSHRelay{
		listenPort:   publicPort,
		upstreamPort: upstreamPort,
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
	return relay, publicPort, done
}

func waitCLITestRelayTotals(t *testing.T, relay *cliSSHRelay, minimum int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	want := int64(minimum)
	for (relay.upload.Load() < want || relay.download.Load() < want) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if relay.upload.Load() < want || relay.download.Load() < want {
		t.Fatalf("metered totals = ↑%d ↓%d, want at least %d each", relay.upload.Load(), relay.download.Load(), minimum)
	}
}

func cliTestRelayHasHost(relay *cliSSHRelay, host string) bool {
	for _, flow := range relay.listFlows() {
		if flow.Host == host && strings.HasPrefix(flow.ID, cliSSHFlowIDPrefix) {
			return true
		}
	}
	return false
}

func dialCLITestHTTPConnect(t *testing.T, port int, target string) net.Conn {
	t.Helper()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(2 * time.Second)
	var connection net.Conn
	var err error
	for connection == nil && time.Now().Before(deadline) {
		connection, err = net.DialTimeout("tcp", address, 200*time.Millisecond)
		if connection == nil {
			time.Sleep(time.Millisecond)
		}
	}
	if connection == nil {
		t.Fatal(err)
	}
	_ = connection.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := fmt.Fprintf(connection, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		t.Fatal(err)
	}
	status := readCLITestHTTPStatus(t, bufio.NewReader(connection))
	if !strings.Contains(status, " 200 ") {
		t.Fatalf("CONNECT status = %q", status)
	}
	_ = connection.SetDeadline(time.Time{})
	return connection
}

func readCLITestHTTPStatus(t *testing.T, reader *bufio.Reader) string {
	t.Helper()
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" || line == "\n" {
			return status
		}
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
