//go:build linux && !cgo && cli

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/component/proxydialer"
	"github.com/metacubex/mihomo/tunnel"
)

const (
	tuiSpeedTestBytes      int64 = 100_000_000
	tuiSpeedTestDuration         = 5 * time.Second
	tuiSpeedConnectTimeout       = 8 * time.Second
	tuiSpeedTestEndpoint         = "https://speed.cloudflare.com/__down"
)

func runTUIDownloadSpeedTest(
	ctx context.Context,
	client *http.Client,
) (tuiSpeedResult, error) {
	return runTUIDownloadSpeedTestWithOptions(
		ctx,
		client,
		tuiSpeedTestEndpoint,
		tuiSpeedTestBytes,
		tuiSpeedTestDuration,
	)
}

func runTUIDownloadSpeedTestWithOptions(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	byteLimit int64,
	testDuration time.Duration,
) (tuiSpeedResult, error) {
	if byteLimit <= 0 {
		return tuiSpeedResult{}, errors.New("speed test byte limit must be positive")
	}
	if testDuration <= 0 {
		return tuiSpeedResult{}, errors.New("speed test duration must be positive")
	}
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return tuiSpeedResult{}, err
	}
	query := requestURL.Query()
	query.Set("bytes", strconv.FormatInt(byteLimit, 10))
	query.Set("cache", strconv.FormatInt(time.Now().UnixNano(), 10))
	requestURL.RawQuery = query.Encode()

	requestContext, cancel := context.WithTimeout(
		ctx,
		tuiSpeedConnectTimeout+testDuration,
	)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestContext,
		http.MethodGet,
		requestURL.String(),
		nil,
	)
	if err != nil {
		return tuiSpeedResult{}, err
	}
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Cache-Control", "no-store")
	request.Header.Set("User-Agent", "flclash-cli/"+cliVersion)

	response, err := client.Do(request)
	if err != nil {
		return tuiSpeedResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return tuiSpeedResult{}, fmt.Errorf(
			"speed test server returned %s",
			response.Status,
		)
	}

	startedAt := time.Now()
	var windowExpired atomic.Bool
	timer := time.AfterFunc(testDuration, func() {
		windowExpired.Store(true)
		cancel()
	})
	defer timer.Stop()

	limited := io.LimitReader(response.Body, byteLimit)
	buffer := make([]byte, 64<<10)
	var downloaded int64
	for downloaded < byteLimit {
		count, readErr := limited.Read(buffer)
		if count > 0 {
			downloaded += int64(count)
		}
		if downloaded >= byteLimit {
			break
		}
		if readErr != nil {
			if windowExpired.Load() {
				break
			}
			if ctx.Err() != nil {
				return tuiSpeedResult{}, ctx.Err()
			}
			if errors.Is(readErr, io.EOF) {
				return tuiSpeedResult{}, fmt.Errorf(
					"speed test stream ended after %s",
					formatBytes(downloaded),
				)
			}
			return tuiSpeedResult{}, readErr
		}
	}

	completed := downloaded >= byteLimit
	duration := time.Since(startedAt)
	if !completed && windowExpired.Load() {
		duration = testDuration
	}
	if duration <= 0 {
		return tuiSpeedResult{}, errors.New("speed test duration is invalid")
	}
	return tuiSpeedResult{
		Bytes:          downloaded,
		DurationMillis: duration.Milliseconds(),
		BytesPerSecond: float64(downloaded) / duration.Seconds(),
		Complete:       completed,
	}, nil
}

func newTUIProxyNodeHTTPClient(proxyName string) (*http.Client, func(), error) {
	proxy := tunnel.AllProxies()[proxyName]
	if proxy == nil {
		return nil, nil, fmt.Errorf("proxy %q is unavailable", proxyName)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = proxydialer.New(proxy, true).DialContext
	transport.DisableCompression = true
	transport.ForceAttemptHTTP2 = true
	client := &http.Client{Transport: transport}
	return client, transport.CloseIdleConnections, nil
}

func newTUIRouteHTTPClient(mixedPort int) (*http.Client, func(), error) {
	if mixedPort <= 0 || mixedPort > 65535 {
		return nil, nil, errors.New("choose a valid mixed port before testing")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(&url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort("127.0.0.1", strconv.Itoa(mixedPort)),
	})
	transport.DisableCompression = true
	transport.ForceAttemptHTTP2 = true
	client := &http.Client{Transport: transport}
	return client, transport.CloseIdleConnections, nil
}

func runTUIRouteDelayTest(
	ctx context.Context,
	client *http.Client,
	testURL string,
) (int, error) {
	requestContext, cancel := context.WithTimeout(ctx, tuiSpeedConnectTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestContext,
		http.MethodGet,
		testURL,
		nil,
	)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Cache-Control", "no-store")
	request.Header.Set("User-Agent", "flclash-cli/"+cliVersion)
	startedAt := time.Now()
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	_ = response.Body.Close()
	if response.StatusCode < http.StatusOK ||
		response.StatusCode >= http.StatusMultipleChoices {
		return 0, fmt.Errorf(
			"delay test server returned %s",
			response.Status,
		)
	}
	delay := int(time.Since(startedAt).Milliseconds())
	if delay < 1 {
		delay = 1
	}
	return delay, nil
}
