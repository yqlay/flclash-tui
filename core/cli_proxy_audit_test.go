//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestProxyCLIInterspersedJSONAndSelection(t *testing.T) {
	const group, node = "选择 组", "节点/一"
	var selected atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut:
			var body struct {
				Name string `json:"name"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			selected.Store(body.Name)
			w.WriteHeader(http.StatusNoContent)
		case strings.HasSuffix(r.URL.Path, "/delay"):
			if r.URL.Query().Get("url") != "https://example.test/delay" {
				t.Errorf("test-url not forwarded: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"delay":42}`))
		default:
			_, _ = w.Write([]byte(`{"proxies":{"选择 组":{"type":"Selector","now":"节点/一","all":["节点/一","other"]}}}`))
		}
	}))
	defer server.Close()
	for _, args := range [][]string{
		{"nodes", group, "--json", "--controller=" + server.URL},
		{"select", group, node, "--controller", server.URL, "--json"},
		{"delay", node, "--json", "--controller", server.URL, "--test-url=https://example.test/delay"},
	} {
		t.Run(args[0], func(t *testing.T) {
			output := captureCLIOutput(t, func() error { return proxyCommand(args) })
			var result map[string]any
			if err := json.Unmarshal([]byte(output), &result); err != nil {
				t.Fatalf("stdout is not one JSON object: %q: %v", output, err)
			}
			switch args[0] {
			case "nodes":
				if result["group"] != group || result["now"] != node {
					t.Fatalf("nodes schema changed: %#v", result)
				}
			case "select":
				if result["group"] != group || result["node"] != node || selected.Load() != node {
					t.Fatalf("selection lost raw Unicode identity: %#v / %q", result, selected.Load())
				}
			case "delay":
				if result["node"] != node || result["samples"] != float64(5) || result["median_millis"] != float64(42) {
					t.Fatalf("delay schema/result: %#v", result)
				}
			}
		})
	}
}

func TestProxyCLIValidatesBeforeResolvingController(t *testing.T) {
	setupCLICommandTestDirectories(t)
	for _, args := range [][]string{
		{"typo"}, {"typo", "--help"}, {"groups", "extra"}, {"nodes"},
		{"select", "one"}, {"delay", "one", "two"}, {"speed"},
		{"nodes", "group", "--unknown"}, {"nodes", "group", "--controller"},
	} {
		if err := proxyCommand(args); err == nil || strings.Contains(err.Error(), "backend is running") {
			t.Errorf("%q should fail argument validation before service resolution, got %v", args, err)
		}
	}
	for _, command := range []string{"groups", "nodes", "select", "delay", "speed"} {
		output := captureCLIOutput(t, func() error { return proxyCommand([]string{command, "--help"}) })
		if !strings.Contains(output, "Usage: flclash proxy "+command) {
			t.Errorf("missing subcommand usage: %q", output)
		}
	}
}

func TestProxyCLIRejectsHTMLForJSONAndMissingSingleDashValue(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("<html>not a controller</html>"))
	}))
	defer server.Close()
	for _, command := range []string{"groups", "list"} {
		if err := proxyCommand([]string{command, "--json", "--controller", server.URL}); err == nil {
			t.Fatalf("%s --json accepted a successful HTML response", command)
		}
	}
	requests.Store(0)
	if err := proxyCommand([]string{"groups", "--controller", server.URL, "--secret", "-json"}); err == nil || requests.Load() != 0 {
		t.Fatalf("missing secret value contacted the controller: error=%v requests=%d", err, requests.Load())
	}
}

func TestManagementCLIRejectsUnexpectedArgumentsBeforeServiceLookup(t *testing.T) {
	setupCLICommandTestDirectories(t)
	for _, run := range []func() error{
		func() error { return restartManagedCommand([]string{"extra"}) },
		func() error { return reloadManagedCommand([]string{"extra"}) },
		func() error { return serviceManagementCommand([]string{"stop", "extra"}) },
		func() error { return connectionsCommand([]string{"close", "all", "--source", "bad"}) },
		func() error { return connectionsCommand([]string{"unknown"}) },
		func() error { return historyCommand([]string{"clear", "--source", "bad"}) },
		func() error { return configCommand([]string{"restore", "extra"}) },
		func() error { return geoCommand([]string{"update", "extra"}) },
		func() error { return envCommand([]string{"extra"}) },
		func() error { return doctorCommand([]string{"extra"}) },
		func() error { return logsManagedCommand([]string{"extra"}) },
	} {
		if err := run(); err == nil || strings.Contains(err.Error(), "backend is running") {
			t.Errorf("expected pre-service validation error, got %v", err)
		}
	}
}

func TestProxyCLIManagedJSONAndSourceClose(t *testing.T) {
	setupCLICommandTestDirectories(t)
	runtimeDir, err := cliRuntimeDirectory()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(runtimeDir, tuiServiceSocketFilename))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	requests := make(chan tuiServiceRequest, 16)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			var request tuiServiceRequest
			if json.NewDecoder(conn).Decode(&request) != nil {
				_ = conn.Close()
				continue
			}
			requests <- request
			status := tuiServiceStatus{OK: true, Version: cliVersion, ProtocolVersion: tuiServiceProtocolVersion,
				Revision: 7, InstanceID: "audit-instance", Mode: tuiSilentMode, FLCOutbound: "group"}
			if request.Action == "speed_proxy" {
				status.Speed = &tuiSpeedResult{Bytes: 2048, DurationMillis: 1000, BytesPerSecond: 2048, Complete: true}
			}
			_ = json.NewEncoder(conn).Encode(status)
			_ = conn.Close()
		}
	}()
	for _, command := range []string{"select", "speed"} {
		args := []string{command, "node", "--json"}
		if command == "select" {
			args = []string{command, "group", "node", "--json"}
		}
		output := captureCLIOutput(t, func() error { return proxyCommand(args) })
		var value map[string]any
		if err := json.Unmarshal([]byte(output), &value); err != nil {
			t.Fatalf("managed %s stdout not JSON: %q", command, output)
		}
		if value["node"] != "node" {
			t.Fatalf("missing node in %s: %#v", command, value)
		}
		if command == "select" && (value["revision"] != float64(7) || value["flc_outbound"] != "group") {
			t.Fatalf("selection revision/outbound missing: %#v", value)
		}
		if command == "speed" && (value["bytes"] != float64(2048) || value["complete"] != true) {
			t.Fatalf("speed data missing: %#v", value)
		}
	}
	if err := connectionsCommand([]string{"close", "all", "--source", "ssh"}); err != nil {
		t.Fatal(err)
	}
	close(requests) // all synchronous requests have been recorded
	var selected, closed bool
	for request := range requests {
		switch request.Action {
		case "select_proxy":
			selected = request.ProxyGroup == "group" && request.ProxyName == "node" && request.ExpectedRevision != nil && *request.ExpectedRevision == 7 && request.ExpectedInstanceID == "audit-instance"
		case "close_all_connections":
			closed = request.Source == "ssh" && request.ExpectedRevision != nil
		}
	}
	if !selected || !closed {
		t.Fatalf("selection bypassed transaction or source filter was lost: selected=%v closed=%v", selected, closed)
	}
}
