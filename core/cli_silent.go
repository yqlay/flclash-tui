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
	tuiSilentMode                = "silent"
	tuiSilentRuntimeConfigPrefix = ".flclash-silent-runtime-"
	tuiFLCListenerName           = "flc-private"
)

type tuiFLCListenerState struct {
	Outbound string
	Port     int
	Username string
	Password string
}

func newTUIFLCListenerState(outbound string) (tuiFLCListenerState, error) {
	outbound = strings.TrimSpace(outbound)
	if outbound == "" {
		return tuiFLCListenerState{}, errors.New(
			"silent mode requires an FLC outbound; run `flclash flc select NAME` first",
		)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return tuiFLCListenerState{}, fmt.Errorf("allocate private FLC port: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		return tuiFLCListenerState{}, fmt.Errorf("release private FLC port reservation: %w", err)
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
	if strings.TrimSpace(state.Outbound) == "" {
		return "", errors.New("silent runtime outbound must not be empty")
	}
	if state.Port <= 0 || state.Port > 65535 {
		return "", errors.New("silent runtime port must be between 1 and 65535")
	}
	if state.Username == "" || len(state.Password) < 12 {
		return "", errors.New("silent runtime credentials are incomplete")
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

	tun := ensureTUIYAMLMapping(root, "tun")
	setTUIYAMLScalar(tun, "enable", "false", "!!bool")
	iptables := ensureTUIYAMLMapping(root, "iptables")
	setTUIYAMLScalar(iptables, "enable", "false", "!!bool")
	tuicServer := ensureTUIYAMLMapping(root, "tuic-server")
	setTUIYAMLScalar(tuicServer, "enable", "false", "!!bool")
	if dns := tuiYAMLMappingValue(root, "dns"); dns != nil && dns.Kind == yaml.MappingNode {
		setTUIYAMLScalar(dns, "listen", "", "!!str")
	}

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
	listeners := &yaml.Node{
		Kind:    yaml.SequenceNode,
		Tag:     "!!seq",
		Content: []*yaml.Node{listener},
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
	matches, _ := filepath.Glob(
		filepath.Join(homeDir, tuiSilentRuntimeConfigPrefix+"*.yaml"),
	)
	for _, match := range matches {
		if keep != "" && filepath.Clean(match) == filepath.Clean(keep) {
			continue
		}
		_ = os.Remove(match)
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
