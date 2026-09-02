//go:build linux && !cgo && cli

package main

import (
	"bytes"
	"encoding/base64"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/metacubex/mihomo/adapter"
	"github.com/metacubex/mihomo/common/convert"
	"gopkg.in/yaml.v3"
)

const tuiSubscriptionMaxDecodeDepth = 2

type tuiSubscriptionPayload struct {
	Data   []byte
	Format string
	Nodes  int
}

func (p tuiSubscriptionPayload) summary() string {
	if p.Nodes > 0 {
		return fmt.Sprintf("%s · %d nodes", p.Format, p.Nodes)
	}
	return p.Format
}

func normalizeTUISubscription(data []byte) (tuiSubscriptionPayload, error) {
	return normalizeTUISubscriptionAtDepth(data, 0)
}

func normalizeTUISubscriptionAtDepth(
	data []byte,
	depth int,
) (tuiSubscriptionPayload, error) {
	original := append([]byte(nil), data...)
	data = bytes.TrimSpace(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")))
	if len(data) == 0 {
		return tuiSubscriptionPayload{}, errors.New("subscription content is empty")
	}
	if len(data) > tuiSubscriptionMaxBytes {
		return tuiSubscriptionPayload{}, fmt.Errorf(
			"subscription content exceeds %d MiB",
			tuiSubscriptionMaxBytes>>20,
		)
	}
	if !utf8.Valid(data) {
		return tuiSubscriptionPayload{}, errors.New(
			"subscription content is not UTF-8 text",
		)
	}

	if payload, matched, err := normalizeTUIJSONSubscription(data); matched {
		return payload, err
	}
	if payload, matched, err := normalizeTUINativeYAML(data); matched {
		if err == nil {
			payload.Data = original
		}
		return payload, err
	}
	if payload, matched, err := normalizeTUIURIList(data); matched {
		return payload, err
	}
	if payload, matched, err := normalizeTUILineSubscription(data); matched {
		return payload, err
	}

	if depth < tuiSubscriptionMaxDecodeDepth {
		if decoded, ok := decodeTUISubscriptionBase64(data); ok {
			payload, err := normalizeTUISubscriptionAtDepth(decoded, depth+1)
			if err != nil {
				return tuiSubscriptionPayload{}, fmt.Errorf(
					"decoded Base64 subscription is unsupported: %w",
					err,
				)
			}
			payload.Format = "Base64 " + payload.Format
			return payload, nil
		}
	}

	return tuiSubscriptionPayload{}, errors.New(
		"unsupported subscription format; expected Mihomo YAML, URI/Base64 URI, " +
			"SIP008, sing-box/Xray JSON, or Surge/Quantumult X/Loon proxy lines",
	)
}

func normalizeTUINativeYAML(
	data []byte,
) (tuiSubscriptionPayload, bool, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil ||
		len(document.Content) == 0 ||
		document.Content[0].Kind != yaml.MappingNode {
		return tuiSubscriptionPayload{}, false, nil
	}
	root := document.Content[0]
	if tuiYAMLMappingValue(root, "outbounds") != nil ||
		!hasTUIMihomoConfigKey(root) {
		return tuiSubscriptionPayload{}, false, nil
	}
	if message := validateConfigBytes(data); message != "" {
		return tuiSubscriptionPayload{}, true, errors.New(
			"Mihomo YAML is invalid: " + message,
		)
	}
	nodes := 0
	if proxies := tuiYAMLMappingValue(root, "proxies"); proxies != nil &&
		proxies.Kind == yaml.SequenceNode {
		nodes = len(proxies.Content)
	}
	return tuiSubscriptionPayload{
		Data:   append([]byte(nil), data...),
		Format: "Mihomo YAML",
		Nodes:  nodes,
	}, true, nil
}

func hasTUIMihomoConfigKey(root *yaml.Node) bool {
	known := map[string]struct{}{
		"allow-lan": {}, "authentication": {}, "bind-address": {},
		"clash-for-android": {}, "dns": {}, "experimental": {},
		"external-controller": {}, "external-controller-unix": {},
		"external-ui": {}, "find-process-mode": {}, "geo-auto-update": {},
		"geodata-loader": {}, "geodata-mode": {}, "geosite-matcher": {},
		"geox-url": {}, "global-client-fingerprint": {}, "global-ua": {},
		"hosts": {}, "interface-name": {}, "ipv6": {}, "iptables": {},
		"keep-alive-idle": {}, "keep-alive-interval": {}, "listeners": {},
		"log-level": {}, "mixed-port": {}, "mode": {}, "ntp": {},
		"port": {}, "profile": {}, "proxies": {}, "proxy-groups": {},
		"proxy-providers": {}, "redir-port": {}, "routing-mark": {},
		"rule-providers": {}, "rules": {}, "secret": {}, "skip-auth-prefixes": {},
		"sniffer": {}, "socks-port": {}, "ss-config": {}, "tcp-concurrent": {},
		"tls": {}, "tproxy-port": {}, "tun": {}, "tuic-server": {},
		"unified-delay": {}, "vmess-config": {},
	}
	for index := 0; index+1 < len(root.Content); index += 2 {
		if _, ok := known[root.Content[index].Value]; ok {
			return true
		}
	}
	return false
}

func normalizeTUIURIList(
	data []byte,
) (tuiSubscriptionPayload, bool, error) {
	lines := tuiSubscriptionURILines(data)
	if len(lines) == 0 {
		return tuiSubscriptionPayload{}, false, nil
	}
	proxies, err := convert.ConvertsV2Ray(data)
	if err != nil {
		return tuiSubscriptionPayload{}, true, fmt.Errorf(
			"parse URI subscription: %w",
			err,
		)
	}
	if len(proxies) != len(lines) {
		return tuiSubscriptionPayload{}, true, fmt.Errorf(
			"URI subscription contains %d nodes but Mihomo converted %d; "+
				"unsupported or malformed nodes were not imported",
			len(lines),
			len(proxies),
		)
	}
	payload, err := buildTUIProxyProfile(proxies, "URI list")
	return payload, true, err
}

func tuiSubscriptionURILines(data []byte) []string {
	lines := make([]string, 0)
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, ";") {
			continue
		}
		if scheme, _, ok := strings.Cut(line, "://"); ok &&
			isTUIURIStyleScheme(scheme) {
			lines = append(lines, line)
		}
	}
	return lines
}

func isTUIURIStyleScheme(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || !isTUIASCIIAlpha(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if !isTUIASCIIAlpha(character) &&
			(character < '0' || character > '9') &&
			character != '+' && character != '-' && character != '.' {
			return false
		}
	}
	return true
}

func isTUIASCIIAlpha(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func decodeTUISubscriptionBase64(data []byte) ([]byte, bool) {
	compact := make([]byte, 0, len(data))
	for _, value := range data {
		switch value {
		case ' ', '\t', '\r', '\n':
			continue
		default:
			compact = append(compact, value)
		}
	}
	if len(compact) < 8 {
		return nil, false
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(string(compact))
		if err == nil && len(decoded) > 0 && utf8.Valid(decoded) {
			return decoded, true
		}
	}
	return nil, false
}

func buildTUIProxyProfile(
	proxies []map[string]any,
	format string,
) (tuiSubscriptionPayload, error) {
	if len(proxies) == 0 {
		return tuiSubscriptionPayload{}, errors.New("subscription contains no proxy nodes")
	}
	names := make([]string, 0, len(proxies)+1)
	names = append(names, "DIRECT")
	nameCounts := make(map[string]int, len(tuiReservedProxyNames))
	for _, name := range tuiReservedProxyNames {
		nameCounts[name] = 1
	}
	for index, proxy := range proxies {
		name, _ := proxy["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			return tuiSubscriptionPayload{}, fmt.Errorf(
				"proxy %d has no name",
				index+1,
			)
		}
		name = uniqueTUIProxyName(nameCounts, name)
		proxy["name"] = name
		if _, err := adapter.ParseProxy(proxy); err != nil {
			return tuiSubscriptionPayload{}, fmt.Errorf(
				"proxy %d (%s) is invalid: %w",
				index+1,
				name,
				err,
			)
		}
		names = append(names, name)
	}
	profile := map[string]any{
		"mixed-port":     7890,
		"allow-lan":      false,
		"mode":           "rule",
		"log-level":      "info",
		"ipv6":           false,
		"unified-delay":  true,
		"tcp-concurrent": true,
		"proxies":        proxies,
		"proxy-groups": []map[string]any{{
			"name":    "PROXY",
			"type":    "select",
			"proxies": names,
		}},
		"rules": []string{"MATCH,PROXY"},
	}
	normalized, err := yaml.Marshal(profile)
	if err != nil {
		return tuiSubscriptionPayload{}, fmt.Errorf("encode converted profile: %w", err)
	}
	if message := validateConfigBytes(normalized); message != "" {
		return tuiSubscriptionPayload{}, errors.New(
			"converted profile is invalid: " + message,
		)
	}
	return tuiSubscriptionPayload{
		Data:   normalized,
		Format: format,
		Nodes:  len(proxies),
	}, nil
}

// tuiReservedProxyNames are installed by Mihomo or generated by this profile.
// A subscription node with one of these names either collides with a built-in
// outbound at startup or prevents the generated proxy group from loading.
var tuiReservedProxyNames = []string{
	"COMPATIBLE",
	"DIRECT",
	"GLOBAL",
	"PASS",
	"PASS-RULE",
	"PROXY",
	"REJECT",
	"REJECT-DROP",
}

func normalizeTUIJSONSubscription(
	data []byte,
) (tuiSubscriptionPayload, bool, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || (trimmed[0] != '{' && trimmed[0] != '[') {
		return tuiSubscriptionPayload{}, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.UseNumber()
	var root any
	if err := decoder.Decode(&root); err != nil {
		return tuiSubscriptionPayload{}, false, nil
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return tuiSubscriptionPayload{}, true, errors.New(
				"JSON subscription must contain exactly one document",
			)
		}
		return tuiSubscriptionPayload{}, true, fmt.Errorf(
			"invalid trailing JSON subscription data: %w",
			err,
		)
	}
	object, ok := root.(map[string]any)
	if !ok {
		return tuiSubscriptionPayload{}, true, errors.New(
			"JSON subscription root must be an object",
		)
	}
	if outbounds, ok := tuiAnySlice(object["outbounds"]); ok {
		return normalizeTUIOutboundJSON(outbounds)
	}
	if servers, ok := tuiAnySlice(object["servers"]); ok {
		proxies, err := convertTUISIP008Servers(servers)
		if err != nil {
			return tuiSubscriptionPayload{}, true, err
		}
		payload, err := buildTUIProxyProfile(proxies, "SIP008 JSON")
		return payload, true, err
	}
	return tuiSubscriptionPayload{}, false, nil
}

func normalizeTUIOutboundJSON(
	outbounds []any,
) (tuiSubscriptionPayload, bool, error) {
	var hasSingBox bool
	var hasXray bool
	for _, value := range outbounds {
		outbound, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if tuiAnyString(outbound["type"]) != "" {
			hasSingBox = true
		}
		if tuiAnyString(outbound["protocol"]) != "" {
			hasXray = true
		}
	}
	if hasSingBox && hasXray {
		return tuiSubscriptionPayload{}, true, errors.New(
			"JSON mixes sing-box type and Xray protocol outbounds",
		)
	}
	var proxies []map[string]any
	var err error
	format := ""
	switch {
	case hasSingBox:
		proxies, err = convertTUISingBoxOutbounds(outbounds)
		format = "sing-box JSON"
	case hasXray:
		proxies, err = convertTUIXrayOutbounds(outbounds)
		format = "Xray JSON"
	default:
		return tuiSubscriptionPayload{}, true, errors.New(
			"JSON outbounds do not contain sing-box type or Xray protocol fields",
		)
	}
	if err != nil {
		return tuiSubscriptionPayload{}, true, err
	}
	payload, err := buildTUIProxyProfile(proxies, format)
	return payload, true, err
}

func convertTUISIP008Servers(servers []any) ([]map[string]any, error) {
	proxies := make([]map[string]any, 0, len(servers))
	names := map[string]int{}
	for index, value := range servers {
		server, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("SIP008 server %d is not an object", index+1)
		}
		name := firstTUISubscriptionString(server, "remarks", "name", "id")
		host := firstTUISubscriptionString(server, "server", "address")
		port := firstTUIValue(server, "server_port", "port")
		if name == "" {
			name = tuiEndpointName(host, port)
		}
		proxy := map[string]any{
			"name":     uniqueTUIProxyName(names, name),
			"type":     "ss",
			"server":   host,
			"port":     port,
			"cipher":   firstTUISubscriptionString(server, "method", "cipher"),
			"password": tuiAnyString(server["password"]),
			"udp":      true,
		}
		if err := applyTUISIP008PluginOptions(proxy, server); err != nil {
			return nil, fmt.Errorf("SIP008 server %d: %w", index+1, err)
		}
		proxies = append(proxies, proxy)
	}
	return proxies, nil
}

// applyTUISIP008PluginOptions keeps the standard plugin_opts string usable by
// Mihomo. Importing only the plugin name makes plugin-backed SS nodes look
// valid but connect with a different transport.
func applyTUISIP008PluginOptions(
	proxy,
	server map[string]any,
) error {
	plugin := strings.ToLower(strings.TrimSpace(tuiAnyString(server["plugin"])))
	options := strings.TrimSpace(tuiAnyString(server["plugin_opts"]))
	if plugin == "" {
		if options != "" {
			return errors.New("plugin_opts is set without a plugin")
		}
		return nil
	}
	switch plugin {
	case "obfs-local", "simple-obfs":
		plugin = "obfs"
	}
	proxy["plugin"] = plugin
	if options == "" {
		return nil
	}
	parsed, err := parseTUISIP008PluginOptions(plugin, options)
	if err != nil {
		return err
	}
	proxy["plugin-opts"] = parsed
	return nil
}

func parseTUISIP008PluginOptions(
	plugin,
	value string,
) (map[string]any, error) {
	options := make(map[string]any)
	for _, rawOption := range strings.Split(value, ";") {
		rawOption = strings.TrimSpace(rawOption)
		if rawOption == "" {
			continue
		}
		key, optionValue, found := strings.Cut(rawOption, "=")
		key = strings.ToLower(strings.TrimSpace(key))
		optionValue = strings.TrimSpace(optionValue)
		if key == "" {
			return nil, errors.New("plugin_opts contains an empty option name")
		}
		if !found {
			switch key {
			case "tls", "mux", "skip-cert-verify":
				optionValue = "true"
			default:
				return nil, fmt.Errorf(
					"plugin_opts option %q requires a value",
					key,
				)
			}
		}

		switch plugin {
		case "obfs":
			switch key {
			case "obfs", "mode":
				options["mode"] = optionValue
			case "obfs-host", "host":
				options["host"] = optionValue
			default:
				return nil, fmt.Errorf(
					"unsupported simple-obfs plugin_opts option %q",
					key,
				)
			}
		case "v2ray-plugin":
			switch key {
			case "mode", "obfs":
				options["mode"] = optionValue
			case "host", "obfs-host", "path":
				if key == "obfs-host" {
					key = "host"
				}
				options[key] = optionValue
			case "tls", "mux", "skip-cert-verify":
				options[key] = parseTUIBool(optionValue)
			default:
				return nil, fmt.Errorf(
					"unsupported v2ray-plugin plugin_opts option %q",
					key,
				)
			}
		default:
			return nil, fmt.Errorf(
				"unsupported SIP008 plugin %q",
				plugin,
			)
		}
	}
	if len(options) == 0 {
		return nil, errors.New("plugin_opts contains no usable options")
	}
	return options, nil
}

func convertTUISingBoxOutbounds(outbounds []any) ([]map[string]any, error) {
	proxies := make([]map[string]any, 0, len(outbounds))
	names := map[string]int{}
	for index, value := range outbounds {
		outbound, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("sing-box outbound %d is not an object", index+1)
		}
		kind := strings.ToLower(tuiAnyString(outbound["type"]))
		if isTUILocalOutbound(kind) {
			continue
		}
		proxy, err := convertTUISingBoxOutbound(outbound, names)
		if err != nil {
			return nil, fmt.Errorf("sing-box outbound %d: %w", index+1, err)
		}
		proxies = append(proxies, proxy)
	}
	return proxies, nil
}

func convertTUISingBoxOutbound(
	outbound map[string]any,
	names map[string]int,
) (map[string]any, error) {
	kind := strings.ToLower(tuiAnyString(outbound["type"]))
	host := tuiAnyString(outbound["server"])
	port := firstTUIValue(outbound, "server_port", "port")
	if host == "" || tuiAnyString(port) == "" {
		return nil, fmt.Errorf("%s outbound has no server or server_port", kind)
	}
	name := tuiAnyString(outbound["tag"])
	if name == "" {
		name = tuiEndpointName(host, port)
	}
	proxy := map[string]any{
		"name":   uniqueTUIProxyName(names, name),
		"server": host,
		"port":   port,
		"udp":    true,
	}
	switch kind {
	case "shadowsocks":
		proxy["type"] = "ss"
		proxy["cipher"] = tuiAnyString(outbound["method"])
		proxy["password"] = tuiAnyString(outbound["password"])
	case "vmess":
		proxy["type"] = "vmess"
		proxy["uuid"] = tuiAnyString(outbound["uuid"])
		proxy["alterId"] = firstTUIValue(outbound, "alter_id", "alterId")
		proxy["cipher"] = firstTUISubscriptionString(outbound, "security", "cipher")
		if proxy["cipher"] == "" {
			proxy["cipher"] = "auto"
		}
	case "vless":
		proxy["type"] = "vless"
		proxy["uuid"] = tuiAnyString(outbound["uuid"])
		proxy["encryption"] = tuiAnyString(outbound["encryption"])
		if proxy["encryption"] == "" {
			proxy["encryption"] = "none"
		}
		copyTUIString(proxy, outbound, "flow", "flow")
	case "trojan":
		proxy["type"] = "trojan"
		proxy["password"] = tuiAnyString(outbound["password"])
	case "hysteria":
		proxy["type"] = "hysteria"
		proxy["auth-str"] = firstTUISubscriptionString(outbound, "auth_str", "auth")
		copyTUIValue(proxy, outbound, "up", "up_mbps", "up")
		copyTUIValue(proxy, outbound, "down", "down_mbps", "down")
	case "hysteria2":
		proxy["type"] = "hysteria2"
		proxy["password"] = tuiAnyString(outbound["password"])
		copyTUIValue(proxy, outbound, "up", "up_mbps", "up")
		copyTUIValue(proxy, outbound, "down", "down_mbps", "down")
		if obfs, ok := tuiAnyMap(outbound["obfs"]); ok {
			copyTUIString(proxy, obfs, "obfs", "type")
			copyTUIString(proxy, obfs, "obfs-password", "password")
		}
	case "tuic":
		proxy["type"] = "tuic"
		proxy["uuid"] = tuiAnyString(outbound["uuid"])
		proxy["password"] = tuiAnyString(outbound["password"])
		copyTUIString(proxy, outbound, "congestion-controller", "congestion_control")
	case "anytls":
		proxy["type"] = "anytls"
		proxy["password"] = tuiAnyString(outbound["password"])
	case "socks", "socks5":
		proxy["type"] = "socks5"
		copyTUIString(proxy, outbound, "username", "username")
		copyTUIString(proxy, outbound, "password", "password")
	case "http", "https":
		proxy["type"] = "http"
		copyTUIString(proxy, outbound, "username", "username")
		copyTUIString(proxy, outbound, "password", "password")
		if kind == "https" {
			proxy["tls"] = true
		}
	default:
		return nil, fmt.Errorf("unsupported outbound type %q", kind)
	}
	applyTUISingBoxTLS(proxy, outbound)
	applyTUISingBoxTransport(proxy, outbound)
	return proxy, nil
}

func applyTUISingBoxTLS(proxy, outbound map[string]any) {
	tls, ok := tuiAnyMap(outbound["tls"])
	if !ok || !tuiAnyBool(tls["enabled"]) {
		return
	}
	kind := tuiAnyString(proxy["type"])
	if kind == "vmess" || kind == "vless" {
		proxy["tls"] = true
	}
	serverName := firstTUISubscriptionString(tls, "server_name", "server-name")
	if serverName != "" {
		if kind == "hysteria" || kind == "hysteria2" ||
			kind == "trojan" || kind == "anytls" || kind == "tuic" {
			proxy["sni"] = serverName
		} else {
			proxy["servername"] = serverName
		}
	}
	proxy["skip-cert-verify"] = tuiAnyBool(tls["insecure"])
	if alpn, ok := tuiStringSlice(tls["alpn"]); ok && len(alpn) > 0 {
		proxy["alpn"] = alpn
	}
	if utls, ok := tuiAnyMap(tls["utls"]); ok && tuiAnyBool(utls["enabled"]) {
		copyTUIString(proxy, utls, "client-fingerprint", "fingerprint")
	}
	if reality, ok := tuiAnyMap(tls["reality"]); ok && tuiAnyBool(reality["enabled"]) {
		proxy["reality-opts"] = map[string]any{
			"public-key": tuiAnyString(reality["public_key"]),
			"short-id":   tuiAnyString(reality["short_id"]),
		}
	}
}

func applyTUISingBoxTransport(proxy, outbound map[string]any) {
	transport, ok := tuiAnyMap(outbound["transport"])
	if !ok {
		return
	}
	kind := strings.ToLower(tuiAnyString(transport["type"]))
	switch kind {
	case "ws", "websocket":
		proxy["network"] = "ws"
		opts := map[string]any{}
		copyTUIString(opts, transport, "path", "path")
		if headers, ok := tuiAnyMap(transport["headers"]); ok {
			opts["headers"] = headers
		}
		proxy["ws-opts"] = opts
	case "grpc":
		proxy["network"] = "grpc"
		proxy["grpc-opts"] = map[string]any{
			"grpc-service-name": firstTUISubscriptionString(transport, "service_name", "serviceName"),
		}
	case "http", "http2":
		proxy["network"] = "h2"
		proxy["h2-opts"] = map[string]any{
			"path": tuiAnyString(transport["path"]),
			"host": tuiStringSliceOrEmpty(transport["host"]),
		}
	case "httpupgrade":
		proxy["network"] = "httpupgrade"
		proxy["http-upgrade-opts"] = map[string]any{
			"path": tuiAnyString(transport["path"]),
			"host": tuiAnyString(transport["host"]),
		}
	}
}

func convertTUIXrayOutbounds(outbounds []any) ([]map[string]any, error) {
	proxies := make([]map[string]any, 0, len(outbounds))
	names := map[string]int{}
	for index, value := range outbounds {
		outbound, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("Xray outbound %d is not an object", index+1)
		}
		protocol := strings.ToLower(tuiAnyString(outbound["protocol"]))
		if isTUILocalOutbound(protocol) {
			continue
		}
		proxy, err := convertTUIXrayOutbound(outbound, names)
		if err != nil {
			return nil, fmt.Errorf("Xray outbound %d: %w", index+1, err)
		}
		proxies = append(proxies, proxy)
	}
	return proxies, nil
}

func convertTUIXrayOutbound(
	outbound map[string]any,
	names map[string]int,
) (map[string]any, error) {
	protocol := strings.ToLower(tuiAnyString(outbound["protocol"]))
	settings, ok := tuiAnyMap(outbound["settings"])
	if !ok {
		return nil, fmt.Errorf("%s outbound has no settings object", protocol)
	}
	name := tuiAnyString(outbound["tag"])
	proxy := map[string]any{"udp": true}
	switch protocol {
	case "vmess", "vless":
		vnext, ok := tuiAnySlice(settings["vnext"])
		if !ok || len(vnext) == 0 {
			return nil, fmt.Errorf("%s settings have no vnext server", protocol)
		}
		server, ok := vnext[0].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s vnext server is invalid", protocol)
		}
		users, ok := tuiAnySlice(server["users"])
		if !ok || len(users) == 0 {
			return nil, fmt.Errorf("%s vnext server has no user", protocol)
		}
		user, ok := users[0].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s user is invalid", protocol)
		}
		proxy["type"] = protocol
		proxy["server"] = firstTUISubscriptionString(server, "address", "server")
		proxy["port"] = firstTUIValue(server, "port", "server_port")
		proxy["uuid"] = firstTUISubscriptionString(user, "id", "uuid")
		if protocol == "vmess" {
			proxy["alterId"] = firstTUIValue(user, "alterId", "alter_id")
			proxy["cipher"] = firstTUISubscriptionString(user, "security", "cipher")
			if proxy["cipher"] == "" {
				proxy["cipher"] = "auto"
			}
		} else {
			proxy["encryption"] = firstTUISubscriptionString(user, "encryption")
			if proxy["encryption"] == "" {
				proxy["encryption"] = "none"
			}
			copyTUIString(proxy, user, "flow", "flow")
		}
	case "trojan", "shadowsocks", "socks", "http":
		servers, ok := tuiAnySlice(settings["servers"])
		if !ok || len(servers) == 0 {
			return nil, fmt.Errorf("%s settings have no server", protocol)
		}
		server, ok := servers[0].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s server is invalid", protocol)
		}
		proxy["server"] = firstTUISubscriptionString(server, "address", "server")
		proxy["port"] = firstTUIValue(server, "port", "server_port")
		switch protocol {
		case "trojan":
			proxy["type"] = "trojan"
			proxy["password"] = tuiAnyString(server["password"])
		case "shadowsocks":
			proxy["type"] = "ss"
			proxy["cipher"] = firstTUISubscriptionString(server, "method", "cipher")
			proxy["password"] = tuiAnyString(server["password"])
		case "socks":
			proxy["type"] = "socks5"
			applyTUIXrayServerUser(proxy, server)
		case "http":
			proxy["type"] = "http"
			applyTUIXrayServerUser(proxy, server)
		}
	default:
		return nil, fmt.Errorf("unsupported protocol %q", protocol)
	}
	if name == "" {
		name = tuiEndpointName(tuiAnyString(proxy["server"]), proxy["port"])
	}
	proxy["name"] = uniqueTUIProxyName(names, name)
	if stream, ok := tuiAnyMap(outbound["streamSettings"]); ok {
		applyTUIXrayStream(proxy, stream)
	}
	return proxy, nil
}

func applyTUIXrayServerUser(proxy, server map[string]any) {
	users, ok := tuiAnySlice(server["users"])
	if !ok || len(users) == 0 {
		return
	}
	user, ok := users[0].(map[string]any)
	if !ok {
		return
	}
	copyTUIString(proxy, user, "username", "user", "username")
	copyTUIString(proxy, user, "password", "pass", "password")
}

func applyTUIXrayStream(proxy, stream map[string]any) {
	network := strings.ToLower(tuiAnyString(stream["network"]))
	if network != "" && network != "tcp" {
		proxy["network"] = network
	}
	security := strings.ToLower(tuiAnyString(stream["security"]))
	if security == "tls" || security == "reality" {
		kind := tuiAnyString(proxy["type"])
		if kind == "vmess" || kind == "vless" {
			proxy["tls"] = true
		}
	}
	if tls, ok := tuiAnyMap(stream["tlsSettings"]); ok {
		applyTUIXrayTLS(proxy, tls)
	}
	if reality, ok := tuiAnyMap(stream["realitySettings"]); ok {
		applyTUIXrayTLS(proxy, reality)
		proxy["reality-opts"] = map[string]any{
			"public-key": firstTUISubscriptionString(reality, "publicKey", "public_key"),
			"short-id":   firstTUISubscriptionString(reality, "shortId", "short_id"),
		}
	}
	switch network {
	case "ws":
		if ws, ok := tuiAnyMap(stream["wsSettings"]); ok {
			proxy["ws-opts"] = map[string]any{
				"path":    tuiAnyString(ws["path"]),
				"headers": tuiAnyMapOrEmpty(ws["headers"]),
			}
		}
	case "grpc":
		if grpc, ok := tuiAnyMap(stream["grpcSettings"]); ok {
			proxy["grpc-opts"] = map[string]any{
				"grpc-service-name": firstTUISubscriptionString(grpc, "serviceName", "service_name"),
			}
		}
	case "h2", "http":
		if httpSettings, ok := tuiAnyMap(stream["httpSettings"]); ok {
			proxy["network"] = "h2"
			proxy["h2-opts"] = map[string]any{
				"path": tuiAnyString(httpSettings["path"]),
				"host": tuiStringSliceOrEmpty(httpSettings["host"]),
			}
		}
	}
}

func applyTUIXrayTLS(proxy, tls map[string]any) {
	serverName := firstTUISubscriptionString(tls, "serverName", "server_name")
	if serverName != "" {
		kind := tuiAnyString(proxy["type"])
		if kind == "trojan" {
			proxy["sni"] = serverName
		} else {
			proxy["servername"] = serverName
		}
	}
	proxy["skip-cert-verify"] = tuiAnyBool(firstTUIValue(tls, "allowInsecure", "insecure"))
	if alpn, ok := tuiStringSlice(tls["alpn"]); ok && len(alpn) > 0 {
		proxy["alpn"] = alpn
	}
	copyTUIString(proxy, tls, "client-fingerprint", "fingerprint")
}

func normalizeTUILineSubscription(
	data []byte,
) (tuiSubscriptionPayload, bool, error) {
	proxies := make([]map[string]any, 0)
	names := map[string]int{}
	inProxySection := false
	matched := false
	for lineNumber, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(strings.TrimSuffix(rawLine, "\r"))
		if line == "" || strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, ";") || strings.HasPrefix(line, "//") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			inProxySection = strings.EqualFold(strings.TrimSpace(line[1:len(line)-1]), "Proxy")
			continue
		}
		proxy, recognized, err := parseTUILegacyProxyLine(line, inProxySection, names)
		if !recognized {
			continue
		}
		matched = true
		if err != nil {
			return tuiSubscriptionPayload{}, true, fmt.Errorf(
				"proxy line %d: %w",
				lineNumber+1,
				err,
			)
		}
		if proxy != nil {
			proxies = append(proxies, proxy)
		}
	}
	if !matched {
		return tuiSubscriptionPayload{}, false, nil
	}
	payload, err := buildTUIProxyProfile(proxies, "Surge/Quantumult X/Loon lines")
	return payload, true, err
}

func parseTUILegacyProxyLine(
	line string,
	inProxySection bool,
	names map[string]int,
) (map[string]any, bool, error) {
	left, right, ok := strings.Cut(line, "=")
	if !ok {
		return nil, false, nil
	}
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	reader := csv.NewReader(strings.NewReader(right))
	reader.TrimLeadingSpace = true
	reader.FieldsPerRecord = -1
	fields, err := reader.Read()
	if err != nil || len(fields) == 0 {
		return nil, inProxySection, errors.New("invalid comma-separated proxy entry")
	}
	for index := range fields {
		fields[index] = strings.TrimSpace(fields[index])
	}

	name := ""
	kind := ""
	endpointIndex := 0
	if inProxySection && isTUILegacyProxyType(fields[0]) {
		name = left
		kind = fields[0]
		endpointIndex = 1
	} else if isTUILegacyProxyType(left) {
		kind = left
	} else {
		return nil, false, nil
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind == "direct" || kind == "reject" {
		return nil, true, nil
	}
	if endpointIndex >= len(fields) {
		return nil, true, errors.New("proxy entry has no server")
	}

	options := map[string]string{}
	var host string
	var port any
	if endpointIndex+1 < len(fields) && !strings.Contains(fields[endpointIndex], ":") &&
		!strings.Contains(fields[endpointIndex+1], "=") {
		host = fields[endpointIndex]
		port = fields[endpointIndex+1]
		endpointIndex += 2
	} else {
		var parseErr error
		host, port, parseErr = splitTUIEndpoint(fields[endpointIndex])
		if parseErr != nil {
			return nil, true, parseErr
		}
		endpointIndex++
	}
	for _, field := range fields[endpointIndex:] {
		key, value, found := strings.Cut(field, "=")
		if !found {
			continue
		}
		options[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	if name == "" {
		name = firstTUIOption(options, "tag", "remarks", "name")
	}
	if name == "" {
		name = tuiEndpointName(host, port)
	}
	proxy := map[string]any{
		"name":   uniqueTUIProxyName(names, name),
		"server": host,
		"port":   port,
		"udp":    true,
	}
	switch kind {
	case "ss", "shadowsocks":
		proxy["type"] = "ss"
		proxy["cipher"] = firstTUIOption(options, "encrypt-method", "method", "cipher")
		proxy["password"] = options["password"]
	case "ssr", "shadowsocksr":
		proxy["type"] = "ssr"
		proxy["cipher"] = firstTUIOption(options, "encrypt-method", "method", "cipher")
		proxy["password"] = options["password"]
		proxy["protocol"] = options["protocol"]
		proxy["obfs"] = options["obfs"]
	case "vmess":
		proxy["type"] = "vmess"
		proxy["uuid"] = firstTUIOption(options, "username", "uuid", "id")
		proxy["alterId"] = firstTUIOption(options, "alterid", "alter-id")
		proxy["cipher"] = firstTUIOption(options, "encrypt-method", "method", "cipher")
		if proxy["cipher"] == "" {
			proxy["cipher"] = "auto"
		}
	case "vless":
		proxy["type"] = "vless"
		proxy["uuid"] = firstTUIOption(options, "username", "uuid", "id")
		proxy["encryption"] = firstTUIOption(options, "encryption")
		if proxy["encryption"] == "" {
			proxy["encryption"] = "none"
		}
		proxy["flow"] = options["flow"]
	case "trojan":
		proxy["type"] = "trojan"
		proxy["password"] = options["password"]
	case "hysteria", "hysteria2", "hy2":
		if kind == "hysteria" {
			proxy["type"] = "hysteria"
			proxy["auth-str"] = firstTUIOption(options, "auth", "password")
		} else {
			proxy["type"] = "hysteria2"
			proxy["password"] = firstTUIOption(options, "password", "auth")
		}
		proxy["up"] = firstTUIOption(options, "up", "upmbps")
		proxy["down"] = firstTUIOption(options, "down", "downmbps")
	case "tuic":
		proxy["type"] = "tuic"
		proxy["uuid"] = firstTUIOption(options, "username", "uuid")
		proxy["password"] = options["password"]
	case "anytls":
		proxy["type"] = "anytls"
		proxy["password"] = options["password"]
	case "socks", "socks5":
		proxy["type"] = "socks5"
		proxy["username"] = options["username"]
		proxy["password"] = options["password"]
	case "http", "https":
		proxy["type"] = "http"
		proxy["username"] = options["username"]
		proxy["password"] = options["password"]
		if kind == "https" {
			proxy["tls"] = true
		}
	default:
		return nil, true, fmt.Errorf("unsupported proxy type %q", kind)
	}
	applyTUILegacyOptions(proxy, options)
	return proxy, true, nil
}

func applyTUILegacyOptions(proxy map[string]any, options map[string]string) {
	if sni := firstTUIOption(options, "sni", "server-name", "tls-host"); sni != "" {
		kind := tuiAnyString(proxy["type"])
		if kind == "vmess" || kind == "vless" {
			proxy["servername"] = sni
		} else {
			proxy["sni"] = sni
		}
	}
	if insecure := firstTUIOption(options, "skip-cert-verify", "allowinsecure"); insecure != "" {
		proxy["skip-cert-verify"] = parseTUIBool(insecure)
	}
	if parseTUIBool(firstTUIOption(options, "tls", "over-tls")) {
		proxy["tls"] = true
	}
	if path := firstTUIOption(options, "ws-path", "path"); path != "" ||
		parseTUIBool(options["ws"]) {
		proxy["network"] = "ws"
		opts := map[string]any{"path": path}
		if host := firstTUIOption(options, "ws-headers", "ws-host", "host"); host != "" {
			opts["headers"] = map[string]any{"Host": host}
		}
		proxy["ws-opts"] = opts
	}
	if service := firstTUIOption(options, "grpc-service-name", "service-name"); service != "" {
		proxy["network"] = "grpc"
		proxy["grpc-opts"] = map[string]any{"grpc-service-name": service}
	}
}

func isTUILegacyProxyType(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "anytls", "direct", "http", "https", "hy2", "hysteria", "hysteria2",
		"reject", "shadowsocks", "shadowsocksr", "socks", "socks5", "ss", "ssr",
		"trojan", "tuic", "vless", "vmess":
		return true
	default:
		return false
	}
}

func splitTUIEndpoint(value string) (string, any, error) {
	value = strings.TrimSpace(value)
	if host, port, err := net.SplitHostPort(value); err == nil {
		return host, port, nil
	}
	index := strings.LastIndex(value, ":")
	if index <= 0 || index == len(value)-1 {
		return "", nil, errors.New("proxy endpoint must include host:port")
	}
	return strings.Trim(value[:index], "[]"), value[index+1:], nil
}

func isTUILocalOutbound(kind string) bool {
	switch kind {
	case "", "block", "bridge", "direct", "dns", "freedom", "blackhole",
		"loopback", "selector", "urltest":
		return true
	default:
		return false
	}
}

func uniqueTUIProxyName(names map[string]int, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "proxy"
	}
	names[value]++
	if names[value] == 1 {
		return value
	}
	return fmt.Sprintf("%s %d", value, names[value])
}

func tuiEndpointName(host string, port any) string {
	if host == "" {
		return "proxy"
	}
	if value := tuiAnyString(port); value != "" {
		return net.JoinHostPort(host, value)
	}
	return host
}

func tuiAnyMap(value any) (map[string]any, bool) {
	mapping, ok := value.(map[string]any)
	return mapping, ok
}

func tuiAnyMapOrEmpty(value any) map[string]any {
	mapping, _ := tuiAnyMap(value)
	if mapping == nil {
		return map[string]any{}
	}
	return mapping
}

func tuiAnySlice(value any) ([]any, bool) {
	values, ok := value.([]any)
	return values, ok
}

func tuiAnyString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 32)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case uint64:
		return strconv.FormatUint(typed, 10)
	default:
		return ""
	}
}

func tuiAnyBool(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return parseTUIBool(typed)
	default:
		return false
	}
}

func parseTUIBool(value string) bool {
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	return err == nil && parsed
}

func tuiStringSlice(value any) ([]string, bool) {
	values, ok := tuiAnySlice(value)
	if !ok {
		if text := tuiAnyString(value); text != "" {
			return []string{text}, true
		}
		return nil, false
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		if text := tuiAnyString(value); text != "" {
			result = append(result, text)
		}
	}
	return result, true
}

func tuiStringSliceOrEmpty(value any) []string {
	values, _ := tuiStringSlice(value)
	if values == nil {
		return []string{}
	}
	return values
}

func firstTUISubscriptionString(mapping map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := tuiAnyString(mapping[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstTUIValue(mapping map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := mapping[key]; ok && tuiAnyString(value) != "" {
			return value
		}
	}
	return ""
}

func copyTUIString(
	destination map[string]any,
	source map[string]any,
	destinationKey string,
	sourceKeys ...string,
) {
	if value := firstTUISubscriptionString(source, sourceKeys...); value != "" {
		destination[destinationKey] = value
	}
}

func copyTUIValue(
	destination map[string]any,
	source map[string]any,
	destinationKey string,
	sourceKeys ...string,
) {
	if value := firstTUIValue(source, sourceKeys...); tuiAnyString(value) != "" {
		destination[destinationKey] = value
	}
}

func firstTUIOption(options map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(options[strings.ToLower(key)]); value != "" {
			return value
		}
	}
	return ""
}
