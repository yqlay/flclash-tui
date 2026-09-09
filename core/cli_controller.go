//go:build linux && !cgo && cli

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

func (c controllerClient) closeIdleConnections() {
	if c.client != nil {
		c.client.CloseIdleConnections()
	}
}

func (c controllerClient) httpClient() *http.Client {
	if c.client != nil {
		return c.client
	}
	if c.options.unixSocket != "" {
		return controllerHTTPClientForOptions(c.options, controllerRequestTimeout)
	}
	return controllerHTTPClient
}

func (c controllerClient) request(method, path string, body io.Reader) ([]byte, error) {
	return c.requestWithClient(c.httpClient(), method, path, body)
}

func (c controllerClient) requestWithTimeout(
	timeout time.Duration,
	method,
	path string,
	body io.Reader,
) ([]byte, error) {
	client := *c.httpClient()
	client.Timeout = timeout
	return c.requestWithClient(&client, method, path, body)
}

func (c controllerClient) requestWithClient(
	client *http.Client,
	method,
	path string,
	body io.Reader,
) ([]byte, error) {
	base := c.baseURL()
	req, err := http.NewRequest(method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if c.options.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.options.secret)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("controller returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

const controllerRequestTimeout = 5 * time.Second

var controllerHTTPClient = &http.Client{Timeout: controllerRequestTimeout}

func (c controllerClient) baseURL() string {
	if c.options.unixSocket != "" {
		return "http://unix"
	}
	base := c.options.address
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	return strings.TrimRight(base, "/")
}

func (c controllerClient) displayAddress() string {
	if c.options.unixSocket != "" {
		return "private Unix socket"
	}
	return c.options.address
}

func controllerHTTPClientForOptions(options controllerOptions, timeout time.Duration) *http.Client {
	if options.unixSocket == "" {
		return &http.Client{Timeout: timeout}
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			dialer := &net.Dialer{Timeout: timeout}
			return dialer.DialContext(ctx, "unix", options.unixSocket)
		},
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
	}
}

func controllerDialAddress(address string) (string, bool) {
	base := address
	if !strings.Contains(base, "://") {
		base = "http://" + base
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" {
		return "", false
	}
	return parsed.Host, true
}

func ensureControllerFree(address string) error {
	dialAddress, ok := controllerDialAddress(address)
	if !ok {
		return nil
	}
	connection, err := net.DialTimeout("tcp", dialAddress, 100*time.Millisecond)
	if err != nil {
		return nil
	}
	_ = connection.Close()
	return fmt.Errorf("controller address %q is already in use; use --no-start to connect to an existing core or choose another port", address)
}

func (c controllerClient) listProxies() error {
	data, err := c.request(http.MethodGet, "/proxies", nil)
	if err != nil {
		return err
	}
	var response struct {
		Proxies map[string]struct {
			Type string   `json:"type"`
			Now  string   `json:"now"`
			All  []string `json:"all"`
		} `json:"proxies"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return err
	}
	groups := make([]string, 0, len(response.Proxies))
	for name := range response.Proxies {
		groups = append(groups, name)
	}
	sort.Strings(groups)
	for _, name := range groups {
		proxy := response.Proxies[name]
		if len(proxy.All) == 0 {
			continue
		}
		fmt.Printf("%s (%s) -> %s\n", name, proxy.Type, proxy.Now)
		for _, item := range proxy.All {
			fmt.Printf("  - %s\n", item)
		}
	}
	return nil
}

func (c controllerClient) selectProxy(group, proxy string) error {
	if err := c.setProxy(group, proxy); err != nil {
		return err
	}
	fmt.Printf("selected %q in %q\n", proxy, group)
	return nil
}

func (c controllerClient) setProxy(group, proxy string) error {
	body, err := json.Marshal(map[string]string{"name": proxy})
	if err != nil {
		return err
	}
	path := "/proxies/" + url.PathEscape(group)
	if _, err := c.request(http.MethodPut, path, strings.NewReader(string(body))); err != nil {
		return err
	}
	return nil
}

func (c controllerClient) testProxyDelay(
	proxy,
	testURL string,
) (int, error) {
	query := url.Values{}
	query.Set("timeout", "5000")
	query.Set("url", testURL)
	path := "/proxies/" + url.PathEscape(proxy) + "/delay?" + query.Encode()
	data, err := c.requestWithTimeout(
		6*time.Second,
		http.MethodGet,
		path,
		nil,
	)
	if err != nil {
		return 0, err
	}
	var result struct {
		Delay int `json:"delay"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return 0, err
	}
	if result.Delay <= 0 {
		return 0, errors.New("delay test returned no usable result")
	}
	return result.Delay, nil
}

func (c controllerClient) closeAllConnections() error {
	_, err := c.request(http.MethodDelete, "/connections", nil)
	return err
}

func (c controllerClient) closeConnection(id string) error {
	_, err := c.request(http.MethodDelete, "/connections/"+url.PathEscape(id), nil)
	return err
}

func (c controllerClient) patchConfig(values map[string]interface{}) error {
	body, err := json.Marshal(values)
	if err != nil {
		return err
	}
	_, err = c.request(http.MethodPatch, "/configs", strings.NewReader(string(body)))
	return err
}

func (c controllerClient) reloadConfigPayload(data []byte) error {
	body, err := json.Marshal(map[string]string{"payload": string(data)})
	if err != nil {
		return err
	}
	_, err = c.request(
		http.MethodPut,
		"/configs?force=true",
		strings.NewReader(string(body)),
	)
	return err
}

func (c controllerClient) updateProvider(name string) error {
	path := "/providers/proxies/" + url.PathEscape(name)
	_, err := c.request(http.MethodPut, path, nil)
	return err
}

func (c controllerClient) updateGeo() error {
	_, err := c.request(http.MethodPost, "/configs/geo", nil)
	return err
}
