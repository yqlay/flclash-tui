//go:build linux && !cgo && cli

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

func linuxSystemProxyEnabled() bool {
	schema := linuxProxySchema()
	value, err := linuxGSettingsGet(schema, "mode")
	return err == nil && value == "'manual'"
}

func linuxSystemProxyMatches(port int) bool {
	if port <= 0 || !linuxSystemProxyEnabled() {
		return false
	}
	for _, suffix := range []string{".http", ".https", ".socks"} {
		schema := linuxProxySchema() + suffix
		host, err := linuxGSettingsGet(schema, "host")
		if err != nil {
			return false
		}
		configuredPort, err := linuxGSettingsGet(schema, "port")
		if err != nil {
			return false
		}
		proxyPort, err := strconv.Atoi(strings.TrimSpace(configuredPort))
		if err != nil {
			return false
		}
		host = strings.Trim(strings.TrimSpace(host), "'\"")
		if (host != "127.0.0.1" && host != "localhost" && host != "::1") ||
			proxyPort != port {
			return false
		}
	}
	return true
}

func linuxGSettingsGet(schema, key string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	output, err := exec.CommandContext(
		ctx,
		"gsettings",
		"get",
		schema,
		key,
	).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func setLinuxSystemProxy(port int, enable bool) error {
	schema := linuxProxySchema()
	commands := linuxSystemProxyCommands(schema, port, enable)
	for _, args := range commands {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		output, err := exec.CommandContext(ctx, "gsettings", append([]string{"set"}, args...)...).CombinedOutput()
		cancel()
		if err != nil {
			message := strings.TrimSpace(string(output))
			if message == "" {
				message = err.Error()
			}
			return fmt.Errorf("gsettings: %s", message)
		}
	}
	return nil
}

func linuxSystemProxyCommands(schema string, port int, enable bool) [][]string {
	commands := make([][]string, 0, 7)
	if enable {
		commands = append(commands,
			[]string{schema, "mode", "none"},
			[]string{schema, "ignore-hosts", "[]"},
			[]string{schema + ".http", "host", "127.0.0.1"},
			[]string{schema + ".http", "port", fmt.Sprintf("%d", port)},
			[]string{schema + ".https", "host", "127.0.0.1"},
			[]string{schema + ".https", "port", fmt.Sprintf("%d", port)},
			[]string{schema + ".socks", "host", "127.0.0.1"},
			[]string{schema + ".socks", "port", fmt.Sprintf("%d", port)},
		)
	}
	mode := "none"
	if enable {
		mode = "manual"
	}
	// Disable first and enable last so a failed host/port update cannot leave a
	// half-configured desktop proxy active.
	commands = append(commands, []string{schema, "mode", mode})
	return commands
}

func linuxProxySchema() string {
	if strings.Contains(strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP")), "MATE") {
		return "org.mate.system.proxy"
	}
	return "org.gnome.system.proxy"
}
