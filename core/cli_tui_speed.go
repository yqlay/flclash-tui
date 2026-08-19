//go:build linux && !cgo && cli

package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/metacubex/mihomo/component/proxydialer"
	"github.com/metacubex/mihomo/tunnel"
)

const (
	tuiSpeedTestBytes       int64 = 99_999_999
	tuiSpeedMaxRequestBytes int64 = 99_999_999
	tuiSpeedTestStreams           = 4
	tuiSpeedTestDuration          = 5 * time.Second
	tuiSpeedConnectTimeout        = 8 * time.Second
	tuiSpeedTestEndpoint          = "https://speed.cloudflare.com/__down"
	tuiDelayTestSamples           = 5
)

func runTUIDownloadSpeedTest(
	ctx context.Context,
	client *http.Client,
) (tuiSpeedResult, error) {
	return runTUIParallelDownloadSpeedTestWithOptions(
		ctx,
		client,
		tuiSpeedTestEndpoint,
		tuiSpeedTestBytes,
		tuiSpeedTestDuration,
		tuiSpeedTestStreams,
	)
}

func runTUIParallelDownloadSpeedTestWithOptions(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	byteLimit int64,
	testDuration time.Duration,
	streams int,
) (tuiSpeedResult, error) {
	if byteLimit <= 0 {
		return tuiSpeedResult{}, errors.New("speed test byte limit must be positive")
	}
	if testDuration <= 0 {
		return tuiSpeedResult{}, errors.New("speed test duration must be positive")
	}
	if streams <= 0 {
		return tuiSpeedResult{}, errors.New("speed test stream count must be positive")
	}
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return tuiSpeedResult{}, err
	}
	warmContext, cancelWarm := context.WithTimeout(ctx, tuiSpeedConnectTimeout)
	err = warmTUIDownloadStreams(warmContext, client, requestURL, streams)
	cancelWarm()
	if err != nil {
		return tuiSpeedResult{}, fmt.Errorf("warm speed test connections: %w", err)
	}

	downloadContext, cancelDownload := context.WithTimeout(ctx, testDuration)
	defer cancelDownload()
	startedAt := time.Now()
	results := make(chan tuiSpeedStreamResult, streams)
	base := byteLimit / int64(streams)
	remainder := byteLimit % int64(streams)
	for stream := 0; stream < streams; stream++ {
		requestBytes := base
		if int64(stream) < remainder {
			requestBytes++
		}
		go func(index int, limit int64) {
			bytes, streamErr := readTUISpeedStream(
				downloadContext,
				client,
				requestURL,
				limit,
				index,
			)
			results <- tuiSpeedStreamResult{bytes: bytes, err: streamErr}
		}(stream, requestBytes)
	}
	var downloaded int64
	for index := 0; index < streams; index++ {
		result := <-results
		downloaded += result.bytes
		if result.err != nil && !errors.Is(result.err, context.DeadlineExceeded) &&
			!errors.Is(result.err, context.Canceled) {
			return tuiSpeedResult{}, result.err
		}
	}
	duration := time.Since(startedAt)
	completed := downloaded >= byteLimit
	if !completed && errors.Is(downloadContext.Err(), context.DeadlineExceeded) {
		duration = testDuration
	}
	if downloaded <= 0 {
		return tuiSpeedResult{}, errors.New("speed test did not receive data")
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

type tuiSpeedStreamResult struct {
	bytes int64
	err   error
}

func warmTUIDownloadStreams(
	ctx context.Context,
	client *http.Client,
	endpoint *url.URL,
	streams int,
) error {
	errorsByStream := make(chan error, streams)
	for stream := 0; stream < streams; stream++ {
		go func(index int) {
			_, err := readTUISpeedStream(ctx, client, endpoint, 1, index)
			errorsByStream <- err
		}(stream)
	}
	for index := 0; index < streams; index++ {
		if err := <-errorsByStream; err != nil {
			return err
		}
	}
	return nil
}

func readTUISpeedStream(
	ctx context.Context,
	client *http.Client,
	endpoint *url.URL,
	requestBytes int64,
	stream int,
) (int64, error) {
	currentURL := *endpoint
	query := currentURL.Query()
	query.Set("bytes", strconv.FormatInt(requestBytes, 10))
	query.Set("cache", fmt.Sprintf("%d-%d", time.Now().UnixNano(), stream))
	currentURL.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, currentURL.String(), nil)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Cache-Control", "no-store")
	request.Header.Set("User-Agent", "flclash/"+cliVersion)
	response, err := client.Do(request)
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("speed test server returned %s", response.Status)
	}
	downloaded, err := io.Copy(io.Discard, io.LimitReader(response.Body, requestBytes))
	if ctx.Err() != nil {
		return downloaded, ctx.Err()
	}
	if err != nil {
		return downloaded, err
	}
	if downloaded != requestBytes {
		return downloaded, io.ErrUnexpectedEOF
	}
	return downloaded, nil
}

func runTUIDownloadSpeedTestWithOptions(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	byteLimit int64,
	testDuration time.Duration,
) (tuiSpeedResult, error) {
	return runTUIDownloadSpeedTestWithRequestLimit(
		ctx,
		client,
		endpoint,
		byteLimit,
		testDuration,
		byteLimit,
	)
}

func runTUIDownloadSpeedTestWithRequestLimit(
	ctx context.Context,
	client *http.Client,
	endpoint string,
	byteLimit int64,
	testDuration time.Duration,
	maxRequestBytes int64,
) (tuiSpeedResult, error) {
	if byteLimit <= 0 {
		return tuiSpeedResult{}, errors.New("speed test byte limit must be positive")
	}
	if testDuration <= 0 {
		return tuiSpeedResult{}, errors.New("speed test duration must be positive")
	}
	if maxRequestBytes <= 0 {
		return tuiSpeedResult{}, errors.New("speed test request limit must be positive")
	}
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return tuiSpeedResult{}, err
	}

	requestContext, cancel := context.WithTimeout(
		ctx,
		tuiSpeedConnectTimeout+testDuration,
	)
	defer cancel()

	var windowExpired atomic.Bool
	var startedAt time.Time
	var timer *time.Timer
	defer func() {
		if timer != nil {
			timer.Stop()
		}
	}()

	buffer := make([]byte, 64<<10)
	var downloaded int64
downloadLoop:
	for downloaded < byteLimit {
		requestBytes := byteLimit - downloaded
		if requestBytes > maxRequestBytes {
			requestBytes = maxRequestBytes
		}
		currentURL := *requestURL
		query := currentURL.Query()
		query.Set("bytes", strconv.FormatInt(requestBytes, 10))
		query.Set("cache", strconv.FormatInt(time.Now().UnixNano(), 10))
		currentURL.RawQuery = query.Encode()

		request, requestErr := http.NewRequestWithContext(
			requestContext,
			http.MethodGet,
			currentURL.String(),
			nil,
		)
		if requestErr != nil {
			return tuiSpeedResult{}, requestErr
		}
		request.Header.Set("Accept-Encoding", "identity")
		request.Header.Set("Cache-Control", "no-store")
		request.Header.Set("User-Agent", "flclash/"+cliVersion)

		response, responseErr := client.Do(request)
		if responseErr != nil {
			if windowExpired.Load() {
				break
			}
			if ctx.Err() != nil {
				return tuiSpeedResult{}, ctx.Err()
			}
			return tuiSpeedResult{}, responseErr
		}
		if response.StatusCode != http.StatusOK {
			_ = response.Body.Close()
			return tuiSpeedResult{}, fmt.Errorf(
				"speed test server returned %s",
				response.Status,
			)
		}
		if startedAt.IsZero() {
			startedAt = time.Now()
			timer = time.AfterFunc(testDuration, func() {
				windowExpired.Store(true)
				cancel()
			})
		}

		limited := io.LimitReader(response.Body, requestBytes)
		var requestDownloaded int64
		for requestDownloaded < requestBytes {
			count, readErr := limited.Read(buffer)
			if count > 0 {
				count64 := int64(count)
				requestDownloaded += count64
				downloaded += count64
			}
			if requestDownloaded >= requestBytes {
				break
			}
			if readErr != nil {
				_ = response.Body.Close()
				if windowExpired.Load() {
					break downloadLoop
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
		_ = response.Body.Close()
	}

	completed := downloaded >= byteLimit
	if startedAt.IsZero() {
		return tuiSpeedResult{}, errors.New("speed test did not receive a response")
	}
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
	configureTUIHTTP1Transport(transport)
	client := &http.Client{Transport: transport}
	return client, transport.CloseIdleConnections, nil
}

func newTUIRouteHTTPClient(mixedPort int) (*http.Client, func(), error) {
	if mixedPort <= 0 || mixedPort > 65535 {
		return nil, nil, errors.New("choose a valid Proxy port before testing")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyURL(&url.URL{
		Scheme: "http",
		Host:   net.JoinHostPort("127.0.0.1", strconv.Itoa(mixedPort)),
	})
	configureTUIHTTP1Transport(transport)
	client := &http.Client{Transport: transport}
	return client, transport.CloseIdleConnections, nil
}

func configureTUIHTTP1Transport(transport *http.Transport) {
	transport.DisableCompression = true
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
}

func runTUIRouteDelayTest(
	ctx context.Context,
	client *http.Client,
	testURL string,
) (tuiDelayResult, error) {
	requestContext, cancel := context.WithTimeout(ctx, tuiSpeedConnectTimeout)
	defer cancel()
	if _, err := runTUIRouteDelaySample(requestContext, client, testURL); err != nil {
		return tuiDelayResult{}, err
	}
	samples := make([]int, 0, tuiDelayTestSamples)
	for index := 0; index < tuiDelayTestSamples; index++ {
		delay, err := runTUIRouteDelaySample(requestContext, client, testURL)
		if err != nil {
			return tuiDelayResult{}, err
		}
		samples = append(samples, delay)
	}
	return summarizeTUIDelays(samples)
}

func runTUIRouteDelaySample(
	ctx context.Context,
	client *http.Client,
	testURL string,
) (int, error) {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodHead,
		testURL,
		nil,
	)
	if err != nil {
		return 0, err
	}
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Cache-Control", "no-store")
	request.Header.Set("User-Agent", "flclash/"+cliVersion)
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

func testTUIProxyDelaySamples(
	client controllerClient,
	proxy,
	testURL string,
) (tuiDelayResult, error) {
	samples := make([]int, 0, tuiDelayTestSamples)
	for index := 0; index < tuiDelayTestSamples; index++ {
		delay, err := client.testProxyDelay(proxy, testURL)
		if err != nil {
			return tuiDelayResult{}, err
		}
		samples = append(samples, delay)
	}
	return summarizeTUIDelays(samples)
}

func summarizeTUIDelays(samples []int) (tuiDelayResult, error) {
	if len(samples) == 0 {
		return tuiDelayResult{}, errors.New("delay test returned no samples")
	}
	sorted := append([]int(nil), samples...)
	sort.Ints(sorted)
	median := sorted[len(sorted)/2]
	if len(sorted)%2 == 0 {
		median = int(math.Round(float64(sorted[len(sorted)/2-1]+sorted[len(sorted)/2]) / 2))
	}
	jitter := 0
	if len(samples) > 1 {
		var differences int
		for index := 1; index < len(samples); index++ {
			differences += absTUIInt(samples[index] - samples[index-1])
		}
		jitter = int(math.Round(float64(differences) / float64(len(samples)-1)))
	}
	return tuiDelayResult{
		MedianMillis: median,
		JitterMillis: jitter,
		MinMillis:    sorted[0],
		MaxMillis:    sorted[len(sorted)-1],
		Samples:      len(samples),
	}, nil
}

func absTUIInt(value int) int {
	if value < 0 {
		return -value
	}
	return value
}
