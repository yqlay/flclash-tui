//go:build linux && !cgo && cli

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSummarizeTUIDelaysUsesMedianAndMeanSuccessiveJitter(t *testing.T) {
	result, err := summarizeTUIDelays([]int{10, 14, 12, 80, 11})
	if err != nil {
		t.Fatal(err)
	}
	if result.MedianMillis != 12 || result.JitterMillis != 36 ||
		result.MinMillis != 10 || result.MaxMillis != 80 || result.Samples != 5 {
		t.Fatalf("delay summary = %+v", result)
	}
}

func TestTUIParallelDownloadSpeedUsesConcurrentStreamsAndExactBudget(t *testing.T) {
	const (
		byteLimit int64 = 400 << 10
		streams         = 4
	)
	var active atomic.Int32
	var maximum atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		requestBytes, err := strconv.ParseInt(request.URL.Query().Get("bytes"), 10, 64)
		if err != nil {
			http.Error(writer, "invalid byte count", http.StatusBadRequest)
			return
		}
		if requestBytes > 1 {
			current := active.Add(1)
			defer active.Add(-1)
			for {
				observed := maximum.Load()
				if current <= observed || maximum.CompareAndSwap(observed, current) {
					break
				}
			}
			time.Sleep(25 * time.Millisecond)
		}
		writer.Header().Set("Content-Length", strconv.FormatInt(requestBytes, 10))
		_, _ = writer.Write(make([]byte, requestBytes))
	}))
	defer server.Close()

	result, err := runTUIParallelDownloadSpeedTestWithOptions(
		context.Background(),
		server.Client(),
		server.URL,
		byteLimit,
		time.Second,
		streams,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.Bytes != byteLimit {
		t.Fatalf("parallel speed result = %+v", result)
	}
	if maximum.Load() < streams {
		t.Fatalf("maximum concurrent streams = %d, want %d", maximum.Load(), streams)
	}
}

func TestTUIParallelDownloadSpeedUsesSharedFixedWindow(t *testing.T) {
	const (
		byteLimit    int64 = 10 << 20
		testDuration       = 80 * time.Millisecond
		streams            = 4
	)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		requestBytes, err := strconv.ParseInt(request.URL.Query().Get("bytes"), 10, 64)
		if err != nil {
			http.Error(writer, "invalid byte count", http.StatusBadRequest)
			return
		}
		if requestBytes == 1 {
			writer.Header().Set("Content-Length", "1")
			_, _ = writer.Write([]byte{0})
			return
		}
		flusher := writer.(http.Flusher)
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

	result, err := runTUIParallelDownloadSpeedTestWithOptions(
		context.Background(),
		server.Client(),
		server.URL,
		byteLimit,
		testDuration,
		streams,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Complete || result.Bytes <= 0 || result.Bytes >= byteLimit {
		t.Fatalf("parallel incomplete result = %+v", result)
	}
	if result.DurationMillis != testDuration.Milliseconds() {
		t.Fatalf(
			"parallel incomplete duration = %dms, want %dms",
			result.DurationMillis,
			testDuration.Milliseconds(),
		)
	}
	expected := float64(result.Bytes) / testDuration.Seconds()
	if result.BytesPerSecond != expected {
		t.Fatalf("bytes/second = %f, want %f", result.BytesPerSecond, expected)
	}
}

func TestTUIRouteDelayUsesActiveFallbackPortAndReturnsSamples(t *testing.T) {
	const (
		configuredPort = 7890
		activePort     = 39123
	)
	runtime := newTestTUIServiceRuntime(t)
	runtime.configureManagedRuntimePolicy(
		"rule",
		configuredPort,
		activePort,
		"config.yaml",
		tuiFLCListenerState{},
		tuiTunScopeUser,
		false,
	)
	runtime.setRunning(true)
	runtime.mu.Lock()
	runtime.activePort = activePort
	runtime.mu.Unlock()
	usedProxyURL := ""
	runtime.routeClient = func(proxyURL string) (*http.Client, func(), error) {
		usedProxyURL = proxyURL
		return &http.Client{}, func() {}, nil
	}
	runtime.routeDelayTest = func(
		context.Context,
		*http.Client,
		string,
	) (tuiDelayResult, error) {
		return tuiDelayResult{
			MedianMillis: 42,
			JitterMillis: 3,
			MinMillis:    38,
			MaxMillis:    47,
			Samples:      tuiDelayTestSamples,
		}, nil
	}
	status := runtime.testRoute(tuiServiceRequest{
		Action:    "delay_route",
		MixedPort: configuredPort,
		TestURL:   "https://example.com/generate_204",
	})
	if !status.OK {
		t.Fatalf("route delay failed: %+v", status)
	}
	if usedProxyURL != tuiLoopbackProxyURL(activePort) {
		t.Fatalf(
			"route test proxy = %q, want %q",
			usedProxyURL,
			tuiLoopbackProxyURL(activePort),
		)
	}
	if status.Delay != 42 || status.DelayJitter != 3 ||
		status.DelayMin != 38 || status.DelayMax != 47 ||
		status.DelaySamples != tuiDelayTestSamples {
		t.Fatalf("route delay status = %+v", status)
	}
}

func TestTUIRouteDelayUsesAuthenticatedSilentListener(t *testing.T) {
	flc := tuiFLCListenerState{
		Outbound: "Auto",
		Port:     39124,
		Username: "flc",
		Password: "private-secret",
	}
	runtime := newTestTUIServiceRuntime(t)
	runtime.configureManagedRuntimePolicy(
		tuiSilentMode,
		7890,
		flc.Port,
		"config.yaml",
		flc,
		tuiTunScopeUser,
		false,
	)
	runtime.setRunning(true)
	runtime.mu.Lock()
	runtime.activePort = flc.Port
	runtime.mu.Unlock()
	usedProxyURL := ""
	runtime.routeClient = func(proxyURL string) (*http.Client, func(), error) {
		usedProxyURL = proxyURL
		return &http.Client{}, func() {}, nil
	}
	runtime.routeDelayTest = func(
		context.Context,
		*http.Client,
		string,
	) (tuiDelayResult, error) {
		return tuiDelayResult{MedianMillis: 24, Samples: tuiDelayTestSamples}, nil
	}

	status := runtime.testRoute(tuiServiceRequest{
		Action:  "delay_route",
		TestURL: "https://example.com/generate_204",
	})
	if !status.OK || status.Delay != 24 {
		t.Fatalf("silent route delay failed: %+v", status)
	}
	if usedProxyURL != flc.proxyURL() {
		t.Fatalf("silent route proxy = %q, want authenticated FLC URL", usedProxyURL)
	}
}

func TestConfigureTUIHTTP1TransportNegotiatesHTTP1OverTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.ProtoMajor != 1 {
			t.Errorf("request protocol = %s, want HTTP/1.x", request.Proto)
		}
		writer.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	transport := server.Client().Transport.(*http.Transport).Clone()
	configureTUIHTTP1Transport(transport)
	client := &http.Client{Transport: transport}
	request, err := http.NewRequest(http.MethodHead, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.ProtoMajor != 1 {
		t.Fatalf("response protocol = %s, want HTTP/1.x", response.Proto)
	}
}

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
	if model.snapshot.Status != "Dashboard speed test requires the managed backend" {
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
	if model.snapshot.Status != "Proxy speed testing requires the managed backend" {
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
	snapshot.DashboardDelay = tuiDelayResult{
		MedianMillis: 42,
		JitterMillis: 3,
		MinMillis:    39,
		MaxMillis:    46,
		Samples:      5,
	}
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
		"Rule route     42 ms · jitter 3 ms · 5 samples",
		"Cloudflare DL  25.00 MB/s · 200.0 Mbps",
		"100.0 MB in 4.00s",
	} {
		if !strings.Contains(dashboardText, expected) {
			t.Fatalf("Dashboard missing %q:\n%s", expected, dashboardText)
		}
	}

	snapshot.Page = tuiPageProxies
	snapshot.Groups = []tuiGroup{{
		Name:  "Proxy",
		Type:  "Selector",
		Now:   "Node",
		Nodes: []string{"Node"},
		Delays: map[string]tuiDelayResult{
			"Node": {MedianMillis: 42, JitterMillis: 2, Samples: 5},
		},
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
