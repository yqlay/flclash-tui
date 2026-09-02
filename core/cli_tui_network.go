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
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const (
	tuiNetworkCheckTimeout  = 8 * time.Second
	tuiNetworkResponseLimit = 64 << 10
)

var tuiPublicIPSources = []string{
	"https://ipwho.is",
	"https://api.myip.com",
	"https://ipapi.co/json",
	"https://ident.me/json",
	"https://api.ip.sb/geoip",
	"https://ipinfo.io/json",
}

type tuiPublicIPResult struct {
	IP      string
	Country string
	Err     error
}

type tuiLocalIPCandidate struct {
	Interface string
	Address   net.IP
}

type tuiNetworkRoute struct {
	key      string
	label    string
	proxyURL string
	silent   bool
	outbound string
	service  *tuiServiceClient
}

func (m *tuiModel) startNetworkCheck(force bool) tea.Cmd {
	if m.networkCheckActive {
		return nil
	}
	if !force &&
		!m.snapshot.Network.CheckedAt.IsZero() &&
		time.Since(m.snapshot.Network.CheckedAt) < tuiNetworkRefreshInterval {
		return nil
	}
	m.networkCheckActive = true
	m.snapshot.Network.Loading = true
	route := m.networkRoute()
	coreRunning := m.coreRunning
	ownsCore := m.ownsCore
	return func() tea.Msg {
		resolved := route
		if resolved.silent {
			if ownsCore && !coreRunning {
				return tuiNetworkResultMsg{
					info: tuiNetworkInfo{
						IntranetIP: detectTUIIntranetIP(),
						Route:      "SILENT · Core stopped",
						CheckedAt:  time.Now(),
					},
					route: resolved.key,
				}
			}
			if resolved.service == nil {
				return tuiNetworkResultMsg{
					info: tuiNetworkInfo{
						IntranetIP: detectTUIIntranetIP(),
						Route:      "SILENT unavailable",
						Error:      "silent network detection requires the managed Backend",
						CheckedAt:  time.Now(),
					},
					route: resolved.key,
				}
			}
			status, err := resolved.service.flcProxy()
			if err != nil {
				return tuiNetworkResultMsg{
					info: tuiNetworkInfo{
						IntranetIP: detectTUIIntranetIP(),
						Route:      "SILENT unavailable",
						Error:      err.Error(),
						CheckedAt:  time.Now(),
					},
					route: resolved.key,
				}
			}
			resolved.proxyURL = status.FLCProxyURL
			resolved.outbound = status.FLCOutbound
			resolved.label = "SILENT · " + cliDisplayValue(status.FLCOutbound)
		}
		return tuiNetworkResultMsg{
			info:  detectTUINetworkRoute(resolved.proxyURL, resolved.label),
			route: resolved.key,
		}
	}
}

func (m *tuiModel) networkCheckProxyPort() int {
	if strings.EqualFold(m.snapshot.Settings.Mode, tuiSilentMode) {
		return 0
	}
	if m.coreRunning || !m.ownsCore {
		if m.snapshot.ActiveProxyPort > 0 {
			return m.snapshot.ActiveProxyPort
		}
		return m.snapshot.Settings.MixedPort
	}
	return 0
}

func (m *tuiModel) networkCheckRoute() string {
	return m.networkRoute().key
}

func (m *tuiModel) networkRoute() tuiNetworkRoute {
	if strings.EqualFold(m.snapshot.Settings.Mode, tuiSilentMode) {
		return tuiNetworkRoute{
			key: fmt.Sprintf(
				"silent:%d:%t:%t:%s",
				m.snapshot.ActiveProxyPort,
				m.coreRunning,
				m.snapshot.FLCEnabled,
				m.snapshot.FLCOutbound,
			),
			label:    "SILENT · " + cliDisplayValue(m.snapshot.FLCOutbound),
			silent:   true,
			outbound: m.snapshot.FLCOutbound,
			service:  m.service,
		}
	}
	if port := m.networkCheckProxyPort(); port > 0 {
		return tuiNetworkRoute{
			key:      "proxy:" + strconv.Itoa(port),
			label:    fmt.Sprintf("PROXY 127.0.0.1:%d", port),
			proxyURL: tuiLoopbackProxyURL(port),
		}
	}
	return tuiNetworkRoute{key: "direct", label: "DIRECT"}
}

func detectTUINetwork(proxyPort int) tuiNetworkInfo {
	proxyURL := tuiLoopbackProxyURL(proxyPort)
	label := "DIRECT"
	if proxyPort > 0 {
		label = fmt.Sprintf("PROXY 127.0.0.1:%d", proxyPort)
	}
	return detectTUINetworkRoute(proxyURL, label)
}

func detectTUINetworkRoute(proxyURL, routeLabel string) tuiNetworkInfo {
	client := newTUINetworkHTTPClient(proxyURL)
	defer client.CloseIdleConnections()
	return detectTUINetworkWithClient(client, routeLabel)
}

func detectTUINetworkWithClient(client *http.Client, routeLabel string) tuiNetworkInfo {
	info := tuiNetworkInfo{
		IntranetIP: detectTUIIntranetIP(),
		Route:      routeLabel,
		CheckedAt:  time.Now(),
	}
	result := detectTUIPublicIP(
		client,
		tuiPublicIPSources,
	)
	if result.Err != nil {
		info.Error = result.Err.Error()
		return info
	}
	info.PublicIP = result.IP
	info.Country = result.Country
	return info
}

func newTUINetworkHTTPClient(proxyURL string) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if parsedProxyURL, err := url.Parse(proxyURL); err == nil &&
		parsedProxyURL.Scheme == "http" && parsedProxyURL.Host != "" {
		transport.Proxy = http.ProxyURL(parsedProxyURL)
	}
	return &http.Client{
		Transport: transport,
		Timeout:   tuiNetworkCheckTimeout,
	}
}

func detectTUIPublicIP(client *http.Client, sources []string) tuiPublicIPResult {
	if len(sources) == 0 {
		return tuiPublicIPResult{Err: errors.New("no public IP service configured")}
	}
	ctx, cancel := context.WithTimeout(context.Background(), tuiNetworkCheckTimeout)
	defer cancel()
	results := make(chan tuiPublicIPResult, len(sources))
	for _, source := range sources {
		source := source
		go func() {
			results <- requestTUIPublicIP(ctx, client, source)
		}()
	}
	var lastErr error
	for range sources {
		result := <-results
		if result.Err == nil {
			cancel()
			return result
		}
		lastErr = result.Err
	}
	if lastErr == nil {
		lastErr = errors.New("public IP detection failed")
	}
	return tuiPublicIPResult{
		Err: fmt.Errorf("public IP detection failed: %w", lastErr),
	}
}

func requestTUIPublicIP(
	ctx context.Context,
	client *http.Client,
	source string,
) tuiPublicIPResult {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
	if err != nil {
		return tuiPublicIPResult{Err: err}
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "flclash/"+cliVersion)
	response, err := client.Do(request)
	if err != nil {
		return tuiPublicIPResult{Err: err}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return tuiPublicIPResult{
			Err: fmt.Errorf("%s returned %s", source, response.Status),
		}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, tuiNetworkResponseLimit+1))
	if err != nil {
		return tuiPublicIPResult{Err: err}
	}
	if len(data) > tuiNetworkResponseLimit {
		return tuiPublicIPResult{Err: errors.New("public IP response is too large")}
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(data, &payload); err != nil {
		return tuiPublicIPResult{Err: err}
	}
	ip := firstTUIString(payload, "ip", "query")
	if net.ParseIP(ip) == nil {
		return tuiPublicIPResult{Err: errors.New("public IP response has no valid IP")}
	}
	country := strings.ToUpper(firstTUIString(
		payload,
		"country_code",
		"countryCode",
		"cc",
		"country",
	))
	if len(country) > 2 {
		country = ""
	}
	return tuiPublicIPResult{IP: ip, Country: country}
}

func firstTUIString(payload map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func detectTUIIntranetIP() string {
	interfaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	candidates := make([]tuiLocalIPCandidate, 0)
	for _, networkInterface := range interfaces {
		if networkInterface.Flags&net.FlagUp == 0 ||
			networkInterface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addresses, addressErr := networkInterface.Addrs()
		if addressErr != nil {
			continue
		}
		for _, address := range addresses {
			ip, _, parseErr := net.ParseCIDR(address.String())
			if parseErr != nil ||
				ip.IsLoopback() ||
				ip.IsLinkLocalUnicast() ||
				!ip.IsGlobalUnicast() {
				continue
			}
			candidates = append(candidates, tuiLocalIPCandidate{
				Interface: networkInterface.Name,
				Address:   ip,
			})
		}
	}
	selected, ok := selectTUIIntranetIP(candidates)
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s (%s)", selected.Address.String(), selected.Interface)
}

func selectTUIIntranetIP(
	candidates []tuiLocalIPCandidate,
) (tuiLocalIPCandidate, bool) {
	if len(candidates) == 0 {
		return tuiLocalIPCandidate{}, false
	}
	sorted := append([]tuiLocalIPCandidate(nil), candidates...)
	sort.SliceStable(sorted, func(left, right int) bool {
		leftScore := scoreTUIIntranetIP(sorted[left])
		rightScore := scoreTUIIntranetIP(sorted[right])
		if leftScore != rightScore {
			return leftScore < rightScore
		}
		return sorted[left].Interface < sorted[right].Interface
	})
	return sorted[0], true
}

func scoreTUIIntranetIP(candidate tuiLocalIPCandidate) int {
	score := 30
	if candidate.Address.To4() != nil {
		score = 10
	}
	if candidate.Address.IsPrivate() {
		score -= 5
	}
	name := strings.ToLower(candidate.Interface)
	if strings.Contains(name, "wlan") ||
		strings.HasPrefix(name, "wlp") ||
		name == "en0" {
		score -= 2
	} else if strings.HasPrefix(name, "eth") ||
		strings.HasPrefix(name, "enp") ||
		strings.HasPrefix(name, "eno") {
		score--
	}
	return score
}
