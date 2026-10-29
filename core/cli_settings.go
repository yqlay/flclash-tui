//go:build linux && !cgo && cli

package main

import (
	"errors"
	"fmt"
	"strings"
)

func settingsCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		printSettingsUsage()
		return nil
	}
	switch args[0] {
	case "show":
		if len(args) != 1 {
			if cliSubcommandHelp(args[1:]) {
				printSettingsUsage()
				return nil
			}
			return errors.New("usage: flclash settings show")
		}
		return showManagedSettings()
	case "set":
		if len(args) > 1 && cliSubcommandHelp(args[1:]) {
			printSettingsUsage()
			return nil
		}
		if len(args) != 3 {
			return errors.New("usage: flclash settings set KEY VALUE")
		}
		if err := validateSettingsAssignment(args[1], args[2]); err != nil {
			return err
		}
		return setManagedSetting(args[1], args[2])
	default:
		return fmt.Errorf("unknown settings command %q; use `flclash settings -help`", args[0])
	}
}

func printSettingsUsage() {
	fmt.Println("Usage: flclash settings show")
	fmt.Println("       flclash settings set allow-lan|ipv6|unified-delay|tcp-concurrent on|off")
	fmt.Println("       flclash settings set log-level silent|error|warning|info|debug")
	fmt.Println("       flclash settings set tun-scope user|system")
	fmt.Println("TUN on/off, proxy port, mode, and System proxy stay on tun, port, mode, and sys.")
	fmt.Println("tun-scope is refused while TUN is on.")
}

func validateSettingsAssignment(key, value string) error {
	switch strings.ToLower(key) {
	case "allow-lan", "ipv6", "unified-delay", "tcp-concurrent":
		if _, err := cliOnOffValue(value); err != nil {
			return err
		}
	case "log-level":
		if _, err := cliLogLevelValue(value); err != nil {
			return err
		}
	case "tun-scope":
		if _, err := normalizeTUITunScope(value); err != nil {
			return err
		}
		if strings.TrimSpace(value) == "" {
			return errors.New("TUN scope must be user or system")
		}
	default:
		return fmt.Errorf("unknown setting %q", key)
	}
	return nil
}

func cliOnOffValue(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "on":
		return true, nil
	case "off":
		return false, nil
	default:
		return false, errors.New("value must be on or off")
	}
}

func cliLogLevelValue(value string) (string, error) {
	level := strings.ToLower(strings.TrimSpace(value))
	switch level {
	case "silent", "error", "warning", "info", "debug":
		return level, nil
	default:
		return "", errors.New("log-level must be silent, error, warning, info, or debug")
	}
}

func cliOnOffWord(value bool) string {
	if value {
		return "on"
	}
	return "off"
}

func showManagedSettings() error {
	_, status, settings, err := currentManagedSettings()
	if err != nil {
		return err
	}
	fmt.Printf("allow-lan: %s\n", cliOnOffWord(settings.AllowLAN))
	fmt.Printf("ipv6: %s\n", cliOnOffWord(settings.IPv6))
	fmt.Printf("unified-delay: %s\n", cliOnOffWord(settings.UnifiedDelay))
	fmt.Printf("tcp-concurrent: %s\n", cliOnOffWord(settings.TCPConcurrent))
	fmt.Printf("log-level: %s\n", settings.LogLevel)
	scope := status.TunScope
	if scope == "" {
		scope = tuiTunScopeUser
	}
	fmt.Printf("tun-scope: %s\n", scope)
	return nil
}

func setManagedSetting(key, value string) error {
	client, status, _, err := currentManagedSettings()
	if err != nil {
		return err
	}
	status, err = applyManagedSettingForCommand(
		client,
		status.ConfigPath,
		key,
		value,
		status.Revision,
		cliTunRequested(status),
	)
	if err != nil {
		return err
	}
	if strings.EqualFold(key, "tun-scope") {
		fmt.Printf("tun-scope %s (revision %d)\n", status.TunScope, status.Revision)
		return nil
	}
	fmt.Printf("%s %s (revision %d)\n", strings.ToLower(key), strings.ToLower(value), status.Revision)
	return nil
}

func cliTunRequested(status tuiServiceStatus) bool {
	if status.TunRequested != nil {
		return *status.TunRequested
	}
	return strings.EqualFold(status.TunState, "on")
}
