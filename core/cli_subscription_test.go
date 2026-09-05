//go:build linux && !cgo && cli

package main

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/metacubex/mihomo/config"
)

func TestNormalizeTUISubscriptionPreservesNativeYAML(t *testing.T) {
	data := []byte(defaultTUIConfig)
	payload, err := normalizeTUISubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Format != "Mihomo YAML" || string(payload.Data) != string(data) {
		t.Fatalf("native payload = %+v, %q", payload, payload.Data)
	}
}

func TestNormalizeTUISubscriptionConvertsRawAndBase64URILists(t *testing.T) {
	uriList := strings.Join([]string{
		"anytls://secret@example.com:443/?sni=example.com#AnyTLS",
		"hysteria2://secret@example.net:8443/?sni=example.net#HY2",
		"vless://11111111-1111-1111-1111-111111111111@example.org:443?" +
			"encryption=none&security=tls&type=tcp&sni=example.org#VLESS",
	}, "\n")

	for name, data := range map[string][]byte{
		"raw":        []byte(uriList),
		"base64":     []byte(base64.StdEncoding.EncodeToString([]byte(uriList))),
		"base64-url": []byte(base64.RawURLEncoding.EncodeToString([]byte(uriList))),
	} {
		t.Run(name, func(t *testing.T) {
			payload, err := normalizeTUISubscription(data)
			if err != nil {
				t.Fatal(err)
			}
			if payload.Nodes != 3 || !strings.Contains(payload.Format, "URI list") {
				t.Fatalf("payload = %+v", payload)
			}
			raw, err := config.UnmarshalRawConfig(payload.Data)
			if err != nil {
				t.Fatal(err)
			}
			if len(raw.Proxy) != 3 || len(raw.ProxyGroup) != 1 ||
				len(raw.Rule) != 1 || raw.MixedPort != 7890 {
				t.Fatalf("converted config = %+v", raw)
			}
		})
	}
}

func TestNormalizeTUISubscriptionConvertsBase64YAML(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte(defaultTUIConfig))
	payload, err := normalizeTUISubscription([]byte(encoded))
	if err != nil {
		t.Fatal(err)
	}
	if payload.Format != "Base64 Mihomo YAML" || string(payload.Data) != defaultTUIConfig {
		t.Fatalf("payload = %+v, %q", payload, payload.Data)
	}
}

func TestNormalizeTUISubscriptionRejectsPartialURIConversion(t *testing.T) {
	data := []byte(
		"ss://YWVzLTEyOC1nY206c2VjcmV0@example.com:8388#SS\n" +
			"unknown-proxy://secret@example.net:443#Unknown\n",
	)
	_, err := normalizeTUISubscription(data)
	if err == nil || !strings.Contains(err.Error(), "unsupported or malformed nodes") {
		t.Fatalf("partial URI error = %v", err)
	}
}

func TestNormalizeTUISubscriptionRenamesDuplicateAndReservedNodeNames(t *testing.T) {
	payload, err := buildTUIProxyProfile([]map[string]any{
		{"name": "Same", "type": "direct"},
		{"name": "Same", "type": "direct"},
		{"name": "DIRECT", "type": "direct"},
		{"name": "REJECT", "type": "direct"},
		{"name": "PASS-RULE", "type": "direct"},
		{"name": "PROXY", "type": "direct"},
		{"name": "GLOBAL", "type": "direct"},
	}, "test")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := config.UnmarshalRawConfig(payload.Data)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(raw.Proxy))
	for _, proxy := range raw.Proxy {
		got = append(got, tuiAnyString(proxy["name"]))
	}
	want := []string{
		"Same",
		"Same 2",
		"DIRECT 2",
		"REJECT 2",
		"PASS-RULE 2",
		"PROXY 2",
		"GLOBAL 2",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("renamed proxy nodes = %v, want %v", got, want)
	}
	if _, err := config.Parse(payload.Data); err != nil {
		t.Fatalf("converted profile must start: %v", err)
	}
}

func TestNormalizeTUISubscriptionConvertsSIP008JSON(t *testing.T) {
	data := []byte(`{
  "version": 1,
  "servers": [{
    "remarks": "SIP008",
    "server": "example.com",
    "server_port": 8388,
    "method": "aes-128-gcm",
    "password": "secret"
  }]
}`)
	payload, err := normalizeTUISubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Format != "SIP008 JSON" || payload.Nodes != 1 {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestNormalizeTUISubscriptionPreservesSIP008PluginOptions(t *testing.T) {
	data := []byte(`{
  "version": 1,
  "servers": [{
    "remarks": "WebSocket SS",
    "server": "example.com",
    "server_port": 443,
    "method": "aes-128-gcm",
    "password": "secret",
    "plugin": "v2ray-plugin",
    "plugin_opts": "mode=websocket;host=cdn.example.com;path=/ws;tls;mux=false"
  }, {
    "remarks": "Simple obfs",
    "server": "example.net",
    "server_port": 443,
    "method": "aes-128-gcm",
    "password": "secret",
    "plugin": "obfs-local",
    "plugin_opts": "obfs=tls;obfs-host=cover.example.net"
  }]
}`)
	payload, err := normalizeTUISubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := config.UnmarshalRawConfig(payload.Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.Proxy) != 2 {
		t.Fatalf("converted proxies = %#v", raw.Proxy)
	}
	firstOptions, ok := tuiAnyMap(raw.Proxy[0]["plugin-opts"])
	if !ok || firstOptions["mode"] != "websocket" ||
		firstOptions["host"] != "cdn.example.com" ||
		firstOptions["path"] != "/ws" ||
		firstOptions["tls"] != true || firstOptions["mux"] != false {
		t.Fatalf("v2ray plugin options = %#v", firstOptions)
	}
	if raw.Proxy[1]["plugin"] != "obfs" {
		t.Fatalf("simple-obfs plugin = %#v", raw.Proxy[1]["plugin"])
	}
	secondOptions, ok := tuiAnyMap(raw.Proxy[1]["plugin-opts"])
	if !ok || secondOptions["mode"] != "tls" ||
		secondOptions["host"] != "cover.example.net" {
		t.Fatalf("simple-obfs plugin options = %#v", secondOptions)
	}
}

func TestNormalizeTUISubscriptionRejectsTrailingJSONData(t *testing.T) {
	data := []byte(`{"servers": []}{"ignored": true}`)
	_, err := normalizeTUISubscription(data)
	if err == nil || !strings.Contains(err.Error(), "exactly one document") {
		t.Fatalf("trailing JSON error = %v", err)
	}
}

func TestNormalizeTUISubscriptionConvertsSingBoxJSON(t *testing.T) {
	data := []byte(`{
  "outbounds": [
    {"type": "direct", "tag": "direct"},
    {
      "type": "vless",
      "tag": "sing-vless",
      "server": "example.com",
      "server_port": 443,
      "uuid": "11111111-1111-1111-1111-111111111111",
      "tls": {"enabled": true, "server_name": "example.com"},
      "transport": {"type": "ws", "path": "/ws", "headers": {"Host": "example.com"}}
    }
  ]
}`)
	payload, err := normalizeTUISubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Format != "sing-box JSON" || payload.Nodes != 1 {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestNormalizeTUISubscriptionConvertsXrayJSON(t *testing.T) {
	data := []byte(`{
  "outbounds": [{
    "tag": "xray-vless",
    "protocol": "vless",
    "settings": {
      "vnext": [{
        "address": "example.com",
        "port": 443,
        "users": [{
          "id": "11111111-1111-1111-1111-111111111111",
          "encryption": "none"
        }]
      }]
    },
    "streamSettings": {
      "network": "grpc",
      "security": "tls",
      "tlsSettings": {"serverName": "example.com"},
      "grpcSettings": {"serviceName": "proxy"}
    }
  }]
}`)
	payload, err := normalizeTUISubscription(data)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Format != "Xray JSON" || payload.Nodes != 1 {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestNormalizeTUISubscriptionConvertsLegacyClientLines(t *testing.T) {
	for name, data := range map[string]string{
		"surge": `[Proxy]
Node = trojan, example.com, 443, password=secret, sni=example.com
`,
		"quantumult-x": `shadowsocks=example.com:8388, method=aes-128-gcm, password=secret, tag=SS
`,
	} {
		t.Run(name, func(t *testing.T) {
			payload, err := normalizeTUISubscription([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			if payload.Format != "Surge/Quantumult X/Loon lines" || payload.Nodes != 1 {
				t.Fatalf("payload = %+v", payload)
			}
		})
	}
}

func TestReadTUILocalProfileAcceptsNonYAMLExtension(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "nodes.txt")
	data := "hysteria2://secret@example.com:443/?sni=example.com#HY2\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	payload, name, err := readTUILocalProfileDetails(path)
	if err != nil {
		t.Fatal(err)
	}
	if name != "nodes.yaml" || payload.Nodes != 1 {
		t.Fatalf("local import = %q %+v", name, payload)
	}
}

func TestTUISubscriptionFileNameFollowsOriginalFlClash(t *testing.T) {
	if name := tuiNewSubscriptionFileName(`attachment; filename="Singapore.yaml"`); name != "Singapore.yaml" {
		t.Fatalf("quoted filename = %q", name)
	}
	if name := tuiNewSubscriptionFileName(`attachment; filename*=UTF-8''%E6%96%B0%E5%8A%A0%E5%9D%A1.yaml`); name != "新加坡.yaml" {
		t.Fatalf("RFC 5987 filename = %q", name)
	}
	if name := tuiNewSubscriptionFileName(`attachment; filename="../etc/passwd"`); name != "passwd.yaml" {
		t.Fatalf("path traversal filename = %q", name)
	}
	if name := tuiNewSubscriptionFileName(`attachment; filename="nodes.txt"`); name != "nodes.yaml" {
		t.Fatalf("non-yaml filename = %q", name)
	}
	fallback := tuiNewSubscriptionFileName("")
	if matched, _ := regexp.MatchString(`^[1-9][0-9]+\.yaml$`, fallback); !matched {
		t.Fatalf("missing disposition should use original-style id.yaml, got %q", fallback)
	}
	second := tuiNewSubscriptionFileName("   ")
	if second == fallback {
		t.Fatalf("snowflake fallback reused %q", second)
	}
}

func TestFetchTUISubscriptionUsesContentDispositionFileName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="office.yaml"`)
		_, _ = fmt.Fprint(w, defaultTUIConfig)
	}))
	defer server.Close()

	payload, err := fetchTUISubscriptionDetails(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if payload.FileName != "office.yaml" {
		t.Fatalf("downloaded file name = %q", payload.FileName)
	}
	homeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(homeDir, "office.yaml"), []byte(defaultTUIConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	path, err := tuiSubscriptionImportPath(homeDir, payload)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "office-2.yaml" {
		t.Fatalf("collision path = %s", path)
	}
}

func TestFetchTUISubscriptionFallsBackToNumericFileName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, defaultTUIConfig)
	}))
	defer server.Close()

	payload, err := fetchTUISubscriptionDetails(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if matched, _ := regexp.MatchString(`^[1-9][0-9]+\.yaml$`, payload.FileName); !matched {
		t.Fatalf("fallback file name = %q", payload.FileName)
	}
	path, err := tuiSubscriptionImportPath(t.TempDir(), payload)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != payload.FileName {
		t.Fatalf("import path = %s, want %s", path, payload.FileName)
	}
}

func TestFetchTUISubscriptionExternalFixture(t *testing.T) {
	fixtureURL := os.Getenv("FLCLASH_TEST_SUBSCRIPTION_URL")
	if fixtureURL == "" {
		t.Skip("FLCLASH_TEST_SUBSCRIPTION_URL is not set")
	}
	payload, err := fetchTUISubscriptionDetails(fixtureURL)
	if err != nil {
		t.Fatal(err)
	}
	if payload.Nodes == 0 || !strings.Contains(payload.Format, "URI list") {
		t.Fatalf("external subscription was not converted: %+v", payload)
	}
}
