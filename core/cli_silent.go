//go:build linux && !cgo && cli

package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const (
	tuiSilentMode                 = "silent"
	tuiSilentRuntimeConfigPrefix  = ".flclash-silent-runtime-"
	tuiManagedRuntimeConfigPrefix = ".flclash-managed-runtime-"
	tuiFLCListenerName            = "flc-private"
)

func writeTUIManagedRuntimeConfig(
	paths cliPaths,
	mode string,
	port int,
	tunEnabled bool,
	tunScope string,
	tunFD int,
) (string, error) {
	data, err := os.ReadFile(paths.configPath)
	if err != nil {
		return "", err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return "", fmt.Errorf("parse profile for managed runtime: %w", err)
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return "", errors.New("configuration root must be a YAML mapping")
	}
	root := document.Content[0]
	for _, key := range []string{"port", "socks-port", "redir-port", "tproxy-port"} {
		setTUIYAMLScalar(root, key, "0", "!!int")
	}
	setTUIYAMLScalar(root, "mixed-port", strconv.Itoa(port), "!!int")
	setTUIYAMLScalar(root, "allow-lan", "false", "!!bool")
	setTUIYAMLScalar(root, "bind-address", "127.0.0.1", "!!str")
	// Keep the Core in rule mode so the per-UID admission rule is evaluated
	// even when FlClash presents direct/global as the selected outbound mode.
	setTUIYAMLScalar(root, "mode", "rule", "!!str")
	setTUIYAMLScalar(root, "find-process-mode", "always", "!!str")
	if mode == "direct" || mode == "global" {
		target := strings.ToUpper(mode)
		setTUIYAMLSequence(root, "rules", []string{"MATCH," + target}, "!!str")
	}
	prependTUIYAMLSequenceValue(
		root,
		"rules",
		fmt.Sprintf(
			"AND,((IN-NAME,DEFAULT-MIXED),(NOT,((UID,%d)))),REJECT",
			os.Getuid(),
		),
	)
	setTUIYAMLNode(root, "listeners", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
	setTUIYAMLNode(root, "tunnels", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})
	for _, key := range []string{"ss-config", "vmess-config", "external-controller", "external-controller-tls", "external-controller-pipe", "external-controller-unix", "external-ui", "external-ui-url", "external-doh-server"} {
		setTUIYAMLScalar(root, key, "", "!!str")
	}
	for _, key := range []string{"ntp", "iptables", "tuic-server"} {
		mapping := ensureTUIYAMLMapping(root, key)
		setTUIYAMLScalar(mapping, "enable", "false", "!!bool")
	}

	tun := ensureTUIYAMLMapping(root, "tun")
	setTUIYAMLScalar(tun, "enable", strconv.FormatBool(tunEnabled), "!!bool")
	if tunEnabled {
		if tunFD <= 0 {
			return "", errors.New("TUN helper did not provide a file descriptor")
		}
		setTUIYAMLScalar(tun, "file-descriptor", strconv.Itoa(tunFD), "!!int")
		setTUIYAMLScalar(tun, "auto-route", "false", "!!bool")
		setTUIYAMLScalar(tun, "auto-detect-interface", "true", "!!bool")
		octet := 1 + os.Getuid()%250
		setTUIYAMLSequence(tun, "inet4-address", []string{fmt.Sprintf("198.19.%d.1/30", octet)}, "!!str")
		if tunScope == tuiTunScopeUser {
			uid := os.Getuid()
			setTUIYAMLSequence(tun, "include-uid", []string{strconv.Itoa(uid)}, "!!int")
			deleteTUIYAMLKey(tun, "include-uid-range")
			setTUIYAMLScalar(tun, "device", "flc-u"+strconv.FormatInt(int64(uid), 36), "!!str")
			setTUIYAMLScalar(tun, "iproute2-table-index", strconv.Itoa(10000+uid), "!!int")
			setTUIYAMLScalar(tun, "iproute2-rule-index", strconv.Itoa(20000+uid), "!!int")
		} else {
			for _, key := range []string{"include-uid", "include-uid-range", "exclude-uid", "exclude-uid-range"} {
				deleteTUIYAMLKey(tun, key)
			}
			setTUIYAMLScalar(tun, "device", "flc-system", "!!str")
		}
	} else {
		deleteTUIYAMLKey(tun, "file-descriptor")
	}
	updated, err := yaml.Marshal(&document)
	if err != nil {
		return "", fmt.Errorf("encode managed runtime configuration: %w", err)
	}
	if message := validateConfigBytes(updated); message != "" {
		return "", errors.New(message)
	}
	runtimeID := make([]byte, 12)
	if _, err := rand.Read(runtimeID); err != nil {
		return "", err
	}
	runtimePath := filepath.Join(paths.homeDir, tuiManagedRuntimeConfigPrefix+hex.EncodeToString(runtimeID)+".yaml")
	if err := writeTUIProfileAtomically(runtimePath, updated, 0o600); err != nil {
		return "", err
	}
	return runtimePath, nil
}

func setTUIYAMLSequence(root *yaml.Node, key string, values []string, tag string) {
	sequence := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	for _, value := range values {
		sequence.Content = append(sequence.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
	}
	setTUIYAMLNode(root, key, sequence)
}

func prependTUIYAMLSequenceValue(root *yaml.Node, key, value string) {
	sequence := tuiYAMLMappingValue(root, key)
	if sequence == nil || sequence.Kind != yaml.SequenceNode {
		sequence = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		setTUIYAMLNode(root, key, sequence)
	}
	sequence.Content = append(
		[]*yaml.Node{{Kind: yaml.ScalarNode, Tag: "!!str", Value: value}},
		sequence.Content...,
	)
}

func deleteTUIYAMLKey(root *yaml.Node, key string) {
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Value == key {
			root.Content = append(root.Content[:index], root.Content[index+2:]...)
			return
		}
	}
}

type tuiFLCListenerState struct {
	Outbound string
	Port     int
	Username string
	Password string
}

func newTUIFLCListenerState(outbound string) (tuiFLCListenerState, error) {
	return newTUIFLCListenerStateAtPort(outbound, 0)
}

func newTUIFLCListenerStateAtPort(
	outbound string,
	port int,
) (tuiFLCListenerState, error) {
	outbound = strings.TrimSpace(outbound)
	if outbound == "" {
		return tuiFLCListenerState{}, errors.New(
			"silent mode has no flc group yet; select a node in Proxies, or run `flclash proxy select GROUP NODE`",
		)
	}
	if port <= 0 {
		var err error
		port, err = chooseTUIProxyPort(port)
		if err != nil {
			return tuiFLCListenerState{}, fmt.Errorf("allocate private FLC port: %w", err)
		}
	}
	if port > 65535 {
		return tuiFLCListenerState{}, errors.New(
			"private FLC port must be between 1 and 65535",
		)
	}
	password := make([]byte, 24)
	if _, err := rand.Read(password); err != nil {
		return tuiFLCListenerState{}, fmt.Errorf("generate FLC credentials: %w", err)
	}
	return tuiFLCListenerState{
		Outbound: outbound,
		Port:     port,
		Username: "flc",
		Password: hex.EncodeToString(password),
	}, nil
}

func isTUIRuntimeProfileName(name string) bool {
	extension := strings.ToLower(filepath.Ext(name))
	if extension != ".yaml" && extension != ".yml" {
		return false
	}
	base := strings.TrimSuffix(name, filepath.Ext(name))
	return strings.HasPrefix(base, tuiSilentRuntimeConfigPrefix) ||
		strings.HasPrefix(base, tuiManagedRuntimeConfigPrefix)
}

func (state tuiFLCListenerState) proxyURL() string {
	if state.Port <= 0 || state.Username == "" || state.Password == "" {
		return ""
	}
	return (&url.URL{
		Scheme: "http",
		User:   url.UserPassword(state.Username, state.Password),
		Host:   net.JoinHostPort("127.0.0.1", strconv.Itoa(state.Port)),
	}).String()
}

func writeTUISilentRuntimeConfig(
	paths cliPaths,
	state tuiFLCListenerState,
) (string, error) {
	hasOutbound := strings.TrimSpace(state.Outbound) != ""
	hasPrivateListener := state.Port != 0 || state.Username != "" || state.Password != ""
	if hasOutbound != hasPrivateListener {
		return "", errors.New("silent runtime listener state is incomplete")
	}
	if hasPrivateListener {
		if state.Port <= 0 || state.Port > 65535 {
			return "", errors.New("silent runtime port must be between 1 and 65535")
		}
		if state.Username == "" || len(state.Password) < 12 {
			return "", errors.New("silent runtime credentials are incomplete")
		}
	}
	data, err := os.ReadFile(paths.configPath)
	if err != nil {
		return "", err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return "", fmt.Errorf("parse profile for silent mode: %w", err)
	}
	if len(document.Content) == 0 || document.Content[0].Kind != yaml.MappingNode {
		return "", errors.New("configuration root must be a YAML mapping")
	}
	root := document.Content[0]
	for _, key := range []string{
		"port",
		"socks-port",
		"redir-port",
		"tproxy-port",
		"mixed-port",
	} {
		setTUIYAMLScalar(root, key, "0", "!!int")
	}
	setTUIYAMLScalar(root, "allow-lan", "false", "!!bool")
	setTUIYAMLScalar(root, "bind-address", "127.0.0.1", "!!str")
	setTUIYAMLScalar(root, "mode", "rule", "!!str")
	setTUIYAMLScalar(root, "ss-config", "", "!!str")
	setTUIYAMLScalar(root, "vmess-config", "", "!!str")
	setTUIYAMLScalar(root, "external-controller", "", "!!str")
	setTUIYAMLScalar(root, "external-controller-tls", "", "!!str")
	setTUIYAMLScalar(root, "external-controller-pipe", "", "!!str")
	setTUIYAMLScalar(root, "external-controller-unix", "", "!!str")
	setTUIYAMLScalar(root, "external-ui", "", "!!str")
	setTUIYAMLScalar(root, "external-ui-url", "", "!!str")
	setTUIYAMLScalar(root, "external-doh-server", "", "!!str")
	setTUIYAMLScalar(root, "geo-auto-update", "false", "!!bool")
	setTUIYAMLScalar(root, "geodata-loader", "memconservative", "!!str")
	setTUIYAMLScalar(root, "geodata-mode", "false", "!!bool")

	tun := ensureTUIYAMLMapping(root, "tun")
	setTUIYAMLScalar(tun, "enable", "false", "!!bool")
	ntp := ensureTUIYAMLMapping(root, "ntp")
	setTUIYAMLScalar(ntp, "enable", "false", "!!bool")
	iptables := ensureTUIYAMLMapping(root, "iptables")
	setTUIYAMLScalar(iptables, "enable", "false", "!!bool")
	tuicServer := ensureTUIYAMLMapping(root, "tuic-server")
	setTUIYAMLScalar(tuicServer, "enable", "false", "!!bool")
	if dns := tuiYAMLMappingValue(root, "dns"); dns != nil && dns.Kind == yaml.MappingNode {
		setTUIYAMLScalar(dns, "listen", "", "!!str")
	}

	listeners := &yaml.Node{
		Kind: yaml.SequenceNode,
		Tag:  "!!seq",
	}
	if hasPrivateListener {
		listener := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setTUIYAMLScalar(listener, "name", tuiFLCListenerName, "!!str")
		setTUIYAMLScalar(listener, "type", "mixed", "!!str")
		setTUIYAMLScalar(listener, "listen", "127.0.0.1", "!!str")
		setTUIYAMLScalar(listener, "port", strconv.Itoa(state.Port), "!!int")
		setTUIYAMLScalar(listener, "proxy", state.Outbound, "!!str")
		user := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		setTUIYAMLScalar(user, "username", state.Username, "!!str")
		setTUIYAMLScalar(user, "password", state.Password, "!!str")
		users := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{user}}
		setTUIYAMLNode(listener, "users", users)
		listeners.Content = []*yaml.Node{listener}
	}
	setTUIYAMLNode(root, "listeners", listeners)
	setTUIYAMLNode(root, "tunnels", &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"})

	updated, err := yaml.Marshal(&document)
	if err != nil {
		return "", fmt.Errorf("encode silent runtime configuration: %w", err)
	}
	if message := validateConfigBytes(updated); message != "" {
		return "", errors.New(message)
	}
	runtimeID := make([]byte, 12)
	if _, err := rand.Read(runtimeID); err != nil {
		return "", fmt.Errorf("generate silent runtime path: %w", err)
	}
	runtimePath := filepath.Join(
		paths.homeDir,
		tuiSilentRuntimeConfigPrefix+hex.EncodeToString(runtimeID)+".yaml",
	)
	if err := writeTUIProfileAtomically(runtimePath, updated, 0o600); err != nil {
		return "", fmt.Errorf("write silent runtime configuration: %w", err)
	}
	return runtimePath, nil
}

func cleanupTUISilentRuntimeConfigs(homeDir, keep string) {
	for _, prefix := range []string{tuiSilentRuntimeConfigPrefix, tuiManagedRuntimeConfigPrefix} {
		matches, _ := filepath.Glob(filepath.Join(homeDir, prefix+"*.yaml"))
		for _, match := range matches {
			if keep != "" && filepath.Clean(match) == filepath.Clean(keep) {
				continue
			}
			_ = os.Remove(match)
		}
	}
}

func ensureTUIYAMLMapping(root *yaml.Node, key string) *yaml.Node {
	if existing := tuiYAMLMappingValue(root, key); existing != nil && existing.Kind == yaml.MappingNode {
		return existing
	}
	mapping := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	setTUIYAMLNode(root, key, mapping)
	return mapping
}

func setTUIYAMLNode(root *yaml.Node, key string, value *yaml.Node) {
	for index := 0; index+1 < len(root.Content); index += 2 {
		if root.Content[index].Value == key {
			root.Content[index+1] = value
			return
		}
	}
	root.Content = append(
		root.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		value,
	)
}
