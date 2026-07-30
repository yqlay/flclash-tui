//go:build linux && !cgo && cli

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestTUIDownloadSpeedUsesCompletionTime(t *testing.T) {
	const byteLimit int64 = 256 << 10
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if got := request.URL.Query().Get("bytes"); got != strconv.FormatInt(byteLimit, 10) {
			http.Error(writer, "unexpected bytes query", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Length", strconv.FormatInt(byteLimit, 10))
		flusher := writer.(http.Flusher)
		chunk := make([]byte, byteLimit/4)
		for index := 0; index < 4; index++ {
			_, _ = writer.Write(chunk)
			flusher.Flush()
			if index < 3 {
				time.Sleep(10 * time.Millisecond)
			}
		}
	}))
	defer server.Close()

	result, err := runTUIDownloadSpeedTestWithOptions(
		context.Background(),
		server.Client(),
		server.URL,
		byteLimit,
		time.Second,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Bytes != byteLimit {
		t.Fatalf("completion result = %+v", result)
	}
	if result.DurationMillis <= 0 || result.DurationMillis >= 1000 {
		t.Fatalf("completion duration = %dms", result.DurationMillis)
	}
	expected := float64(result.Bytes) /
		(float64(result.DurationMillis) / float64(time.Second/time.Millisecond))
	ratio := result.BytesPerSecond / expected
	if ratio < 0.9 || ratio > 1.1 {
		t.Fatalf(
			"bytes/second = %f, expected approximately %f",
			result.BytesPerSecond,
			expected,
		)
	}
}

func TestTUIDownloadSpeedUsesFixedWindowWhenIncomplete(t *testing.T) {
	const (
		byteLimit    int64 = 10 << 20
		testDuration       = 80 * time.Millisecond
	)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		flusher, ok := writer.(http.Flusher)
		if !ok {
			http.Error(writer, "flushing unavailable", http.StatusInternalServerError)
			return
		}
		chunk := make([]byte, 8<<10)
		for {
			if _, err := writer.Write(chunk); err != nil {
				return
			}
			flusher.Flush()
			select {
			case <-request.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	}))
	defer server.Close()

	result, err := runTUIDownloadSpeedTestWithOptions(
		context.Background(),
		server.Client(),
		server.URL,
		byteLimit,
		testDuration,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Bytes <= 0 || result.Bytes >= byteLimit {
		t.Fatalf("incomplete result = %+v", result)
	}
	if result.DurationMillis != testDuration.Milliseconds() {
		t.Fatalf(
			"incomplete duration = %dms, want %dms",
			result.DurationMillis,
			testDuration.Milliseconds(),
		)
	}
	expected := float64(result.Bytes) / testDuration.Seconds()
	if result.BytesPerSecond != expected {
		t.Fatalf("bytes/second = %f, want %f", result.BytesPerSecond, expected)
	}
}

func TestTUIDownloadSpeedSplitsServerLimitedRequest(t *testing.T) {
	const (
		byteLimit       int64 = 10
		maxRequestBytes int64 = 9
	)
	var requested []int64
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		requestBytes, err := strconv.ParseInt(
			request.URL.Query().Get("bytes"),
			10,
			64,
		)
		if err != nil {
			http.Error(writer, "invalid bytes query", http.StatusBadRequest)
			return
		}
		if requestBytes > maxRequestBytes {
			http.Error(writer, "request too large", http.StatusForbidden)
			return
		}
		requested = append(requested, requestBytes)
		writer.Header().Set(
			"Content-Length",
			strconv.FormatInt(requestBytes, 10),
		)
		_, _ = writer.Write(make([]byte, requestBytes))
	}))
	defer server.Close()

	result, err := runTUIDownloadSpeedTestWithRequestLimit(
		context.Background(),
		server.Client(),
		server.URL,
		byteLimit,
		time.Second,
		maxRequestBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Bytes != byteLimit {
		t.Fatalf("split result = %+v", result)
	}
	if len(requested) != 2 ||
		requested[0] != maxRequestBytes ||
		requested[1] != byteLimit-maxRequestBytes {
		t.Fatalf("requested chunks = %v", requested)
	}
}

func TestTUIVKeyIsScopedToDashboardAndProxies(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.FocusSidebar = false
	_ = model.handleTeaKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'v'},
	})
	if model.snapshot.Status != "Dashboard speed test requires the managed Service" {
		t.Fatalf("Dashboard v status = %q", model.snapshot.Status)
	}

	model.snapshot.Page = tuiPageProxies
	model.snapshot.Groups = []tuiGroup{{
		Name:  "Proxy",
		Nodes: []string{"Node"},
	}}
	model.snapshot.ProxyNodeFocus = false
	_ = model.handleTeaKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'v'},
	})
	if model.snapshot.Status != "Proxy speed testing requires the managed Service" {
		t.Fatalf("Proxies v status = %q", model.snapshot.Status)
	}

	model.snapshot.Page = tuiPageTools
	model.snapshot.Settings.IPv6 = false
	_ = model.handleTeaKey(tea.KeyMsg{
		Type:  tea.KeyRunes,
		Runes: []rune{'v'},
	})
	if !model.snapshot.Settings.IPv6 {
		t.Fatal("Tools v did not keep the IPv6 action")
	}
}

func TestTUIDashboardAndProxySpeedResultsRender(t *testing.T) {
	snapshot := populatedTUISnapshot(tuiPageDashboard)
	snapshot.DashboardDelay = 42
	snapshot.DashboardSpeed = tuiSpeedResult{
		Bytes:          100_000_000,
		DurationMillis: 4000,
		BytesPerSecond: 25_000_000,
		Complete:       true,
	}
	var dashboard strings.Builder
	drawTUIDashboard(
		&dashboard,
		snapshot,
		cliPaths{configPath: "/tmp/config.yaml"},
		110,
		30,
	)
	dashboardText := stripTUIANSI(dashboard.String())
	for _, expected := range []string{
		"Route latency  42 ms",
		"Download test  25.00 MB/s · 200.0 Mbps",
		"100.0 MB in 4.00s",
	} {
		if !strings.Contains(dashboardText, expected) {
			t.Fatalf("Dashboard missing %q:\n%s", expected, dashboardText)
		}
	}

	snapshot.Page = tuiPageProxies
	snapshot.Groups = []tuiGroup{{
		Name:   "Proxy",
		Type:   "Selector",
		Now:    "Node",
		Nodes:  []string{"Node"},
		Delays: map[string]int{"Node": 42},
		Speeds: map[string]tuiSpeedResult{
			"Node": {
				Bytes:          50_000_000,
				DurationMillis: 5000,
				BytesPerSecond: 10_000_000,
			},
		},
	}}
	snapshot.SelectedGroup = 0
	snapshot.SelectedNode = 0
	var proxies strings.Builder
	drawTUIProxies(&proxies, snapshot, 110, 24)
	proxyText := stripTUIANSI(proxies.String())
	for _, expected := range []string{
		"d test group",
		"v speed group",
		"42 ms",
		"10.00 MB/s · 80.0 Mbps",
	} {
		if !strings.Contains(proxyText, expected) {
			t.Fatalf("Proxies missing %q:\n%s", expected, proxyText)
		}
	}
}

func TestTUIProxyGroupSpeedResultUpdatesOneNodeAtATime(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.busy = true
	model.snapshot.Groups = []tuiGroup{{
		Name:   "Proxy",
		Nodes:  []string{"Node"},
		Speeds: map[string]tuiSpeedResult{"Node": {Testing: true}},
	}}
	_, _ = model.Update(tuiProxyGroupSpeedResultMsg{
		groupName: "Proxy",
		node:      "Node",
		result: tuiSpeedResult{
			Bytes:          50_000_000,
			DurationMillis: 5000,
			BytesPerSecond: 10_000_000,
		},
		total: 1,
	})
	result := model.snapshot.Groups[0].Speeds["Node"]
	if result.Testing || result.BytesPerSecond != 10_000_000 {
		t.Fatalf("node speed result = %+v", result)
	}
	if model.busy {
		t.Fatal("group speed operation remained busy after the last node")
	}
	if model.snapshot.Status != "Proxy speed tests complete: 1/1 succeeded" {
		t.Fatalf("group speed status = %q", model.snapshot.Status)
	}
}

func Example_speedFormula() {
	result := tuiSpeedResult{
		Bytes:          100_000_000,
		DurationMillis: 4000,
		BytesPerSecond: 25_000_000,
		Complete:       true,
	}
	fmt.Println(formatTUISpeed(result))
	// Output: 25.00 MB/s · 200.0 Mbps
}
