//go:build linux && !cgo && cli

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	logrus "github.com/sirupsen/logrus"
	"golang.org/x/term"
)

func runTUIEditor(path string, oldState **term.State) error {
	return runTUICooked(oldState, func() error {
		editor := os.Getenv("VISUAL")
		if editor == "" {
			editor = os.Getenv("EDITOR")
		}
		if editor == "" {
			editor = "vi"
		}
		command := exec.Command("sh", "-c", editor+" -- \"$1\"", "flclash-tui-editor", path)
		command.Stdin = os.Stdin
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		return command.Run()
	})
}

var errTUIActionCancelled = errors.New("TUI action cancelled")

func readTUILine(reader io.Reader) (string, error) {
	var value strings.Builder
	buffer := make([]byte, 1)
	for {
		_, err := io.ReadFull(reader, buffer)
		if err != nil {
			if value.Len() > 0 && (errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
				return value.String(), nil
			}
			return value.String(), err
		}
		if buffer[0] == '\n' {
			return value.String(), nil
		}
		value.WriteByte(buffer[0])
		if value.Len() > 64*1024 {
			return "", errors.New("input is too long")
		}
	}
}

func addTUIProfile(homeDir string, oldState **term.State) error {
	return runTUICooked(oldState, func() error {
		_, _ = fmt.Fprint(os.Stdout, "Subscription URL (empty cancels): ")
		value, err := readTUILine(os.Stdin)
		if err != nil && len(value) == 0 {
			return err
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return errTUIActionCancelled
		}
		payload, err := fetchTUISubscriptionDetails(value)
		if err != nil {
			return err
		}
		path, err := tuiSubscriptionImportPath(homeDir, payload)
		if err != nil {
			return err
		}
		if err := writeTUIProfileAtomically(path, payload.Data, 0o600); err != nil {
			return err
		}
		return nil
	})
}

func runTUICooked(oldState **term.State, action func() error) error {
	if err := leaveTUIMode(*oldState); err != nil {
		return err
	}
	logrus.SetOutput(io.Discard)
	actionErr := action()
	reenterErr := reenterTUIMode(oldState)
	if actionErr != nil {
		if reenterErr != nil {
			return fmt.Errorf("%v (failed to restore TUI terminal: %w)", actionErr, reenterErr)
		}
		return actionErr
	}
	return reenterErr
}

func leaveTUIMode(state *term.State) error {
	restoreErr := term.Restore(int(os.Stdin.Fd()), state)
	logrus.SetOutput(os.Stdout)
	_, _ = fmt.Fprint(os.Stdout, "\x1b[?25h\x1b[?7h\x1b[0m\x1b[2J\x1b[H\x1b[?1049l")
	return restoreErr
}

func reenterTUIMode(oldState **term.State) error {
	state, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}
	*oldState = state
	logrus.SetOutput(io.Discard)
	enterTUIScreen()
	return nil
}

func enterTUIScreen() {
	_, _ = fmt.Fprint(os.Stdout, "\x1b[?1049h\x1b[?7l\x1b[?25l\x1b[H\x1b[2J")
}

func maxTUIIndex(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

type tuiKey byte

const (
	tuiKeyQuit tuiKey = iota
	tuiKeyInterrupt
	tuiKeyRefresh
	tuiKeyReload
	tuiKeyHelp
	tuiKeyFocusNext
	tuiKeyFocusPrevious
	tuiKeyUp
	tuiKeyDown
	tuiKeyLeft
	tuiKeyRight
	tuiKeyBack
	tuiKeySelect
	tuiKeyDashboard
	tuiKeyProxies
	tuiKeyProfiles
	tuiKeySSH
	tuiKeyRequests
	tuiKeyConnections
	tuiKeyLogs
	tuiKeySettings
	tuiKeyProviders
	tuiKeyCloseConnections
	tuiKeyAllowLAN
	tuiKeyIPv6
	tuiKeyUnifiedDelay
	tuiKeyTCPConcurrent
	tuiKeyTunScope
	tuiKeyTun
	tuiKeyMode
	tuiKeyLogLevel
	tuiKeyPortUp
	tuiKeyPortDown
	tuiKeySetPort
	tuiKeySystemProxy
	tuiKeyCoreToggle
	tuiKeyEdit
	tuiKeyNewProfile
	tuiKeyRenameProfile
	tuiKeyUpdateProfile
	tuiKeyCloseConnection
	tuiKeyTools
	tuiKeyMaintenance
	tuiKeyBackup
	tuiKeyRestore
	tuiKeyGeoUpdate
	tuiKeyResetTraffic
	tuiKeyViewPrevious
	tuiKeyViewNext
	tuiKeyDelayTest
	tuiKeyDelayTestAll
	tuiKeySpeedTest
	tuiKeyPageUp
	tuiKeyPageDown
	tuiKeyNotifications
	tuiKeySearch
	tuiKeyFilter
	tuiKeySourceFilter
)

func readTUIKeys(reader io.Reader, keys chan<- tuiKey) {
	readTUIKeysSynchronized(reader, keys, nil)
}

func readTUIKeysSynchronized(reader io.Reader, keys chan<- tuiKey, handled <-chan struct{}) {
	defer close(keys)
	buffer := make([]byte, 1)
	for {
		if _, err := io.ReadFull(reader, buffer); err != nil {
			return
		}
		key := tuiKey(0xff)
		switch buffer[0] {
		case 'q', 'Q':
			key = tuiKeyQuit
		case 3:
			key = tuiKeyInterrupt
		case 14:
			key = tuiKeyNotifications
		case 'r':
			key = tuiKeyRefresh
		case 'R':
			key = tuiKeyReload
		case '?':
			key = tuiKeyHelp
		case '\t':
			key = tuiKeyFocusNext
		case '1':
			key = tuiKeyDashboard
		case '2':
			key = tuiKeySSH
		case '3':
			key = tuiKeyProxies
		case '4':
			key = tuiKeyProfiles
		case '5':
			key = tuiKeyRequests
		case '6':
			key = tuiKeyConnections
		case '7':
			key = tuiKeyLogs
		case '8':
			key = tuiKeyTools
		case '9':
			key = tuiKeyMaintenance
		case 'P':
			key = tuiKeyProviders
		case 'x', 'X':
			key = tuiKeyCloseConnections
		case 'a':
			key = tuiKeyAllowLAN
		case 'v':
			key = tuiKeyIPv6
		case 't':
			key = tuiKeyTun
		case 'm':
			key = tuiKeyMode
		case 'i':
			key = tuiKeyLogLevel
		case '+', '=':
			key = tuiKeyPortUp
		case '-':
			key = tuiKeyPortDown
		case 'p':
			key = tuiKeySetPort
		case 'S':
			key = tuiKeySystemProxy
		case 'c':
			key = tuiKeyCoreToggle
		case 'e':
			key = tuiKeyEdit
		case 'n':
			key = tuiKeyNewProfile
		case 'u':
			key = tuiKeyRenameProfile
		case 'U':
			key = tuiKeyUpdateProfile
		case 'd':
			key = tuiKeyCloseConnection
		case '[':
			key = tuiKeyViewPrevious
		case ']':
			key = tuiKeyViewNext
		case 'b':
			key = tuiKeyBackup
		case 'B':
			key = tuiKeyRestore
		case 'g':
			key = tuiKeyGeoUpdate
		case 'z':
			key = tuiKeyResetTraffic
		case 'D':
			key = tuiKeyDelayTest
		case 'A':
			key = tuiKeyDelayTestAll
		case '\r', '\n', ' ':
			key = tuiKeySelect
		case 'w':
			key = tuiKeyUp
		case 's':
			key = tuiKeyDown
		case 0x1b:
			key = readTUIEscape(reader)
		}
		if key != tuiKey(0xff) {
			keys <- key
			if handled != nil {
				if _, open := <-handled; !open {
					return
				}
			}
		}
	}
}

func readTUIEscape(reader io.Reader) tuiKey {
	prefix := make([]byte, 1)
	if _, err := io.ReadFull(reader, prefix); err != nil || (prefix[0] != '[' && prefix[0] != 'O') {
		return tuiKey(0xff)
	}
	number := byte(0)
	for count := 0; count < 16; count++ {
		value := make([]byte, 1)
		if _, err := io.ReadFull(reader, value); err != nil {
			return tuiKey(0xff)
		}
		switch value[0] {
		case 'A':
			return tuiKeyUp
		case 'B':
			return tuiKeyDown
		case 'C':
			return tuiKeyRight
		case 'D':
			return tuiKeyLeft
		case 'Z':
			return tuiKeyFocusPrevious
		case '~':
			switch number {
			case '5':
				return tuiKeyPageUp
			case '6':
				return tuiKeyPageDown
			}
			return tuiKey(0xff)
		}
		if value[0] >= '0' && value[0] <= '9' {
			number = value[0]
			continue
		}
		if (value[0] >= 'a' && value[0] <= 'z') ||
			(value[0] >= 'E' && value[0] <= 'Z') ||
			value[0] == '~' {
			return tuiKey(0xff)
		}
	}
	return tuiKey(0xff)
}

func handleTUIFocusNavigation(snapshot *tuiSnapshot, key tuiKey) bool {
	if page, ok := tuiPageForKey(key); ok {
		snapshot.Page = page
		snapshot.SelectedMenu = int(page)
		snapshot.FocusSidebar = false
		snapshot.ProxyNodeFocus = false
		snapshot.SSHDashboardFocus = false
		return true
	}
	switch key {
	case tuiKeyFocusNext, tuiKeyFocusPrevious:
		if snapshot.Page == tuiPageSSH {
			previous := key == tuiKeyFocusPrevious
			switch {
			case snapshot.FocusSidebar:
				snapshot.FocusSidebar = false
				snapshot.SSHDashboardFocus = previous
			case snapshot.SSHDashboardFocus:
				if previous {
					snapshot.SSHDashboardFocus = false
				} else {
					snapshot.FocusSidebar = true
					snapshot.SSHDashboardFocus = false
					snapshot.SelectedMenu = int(snapshot.Page)
				}
			default:
				if previous {
					snapshot.FocusSidebar = true
					snapshot.SelectedMenu = int(snapshot.Page)
				} else {
					snapshot.SSHDashboardFocus = true
				}
			}
			return true
		}
		snapshot.FocusSidebar = !snapshot.FocusSidebar
		if snapshot.FocusSidebar {
			snapshot.SelectedMenu = int(snapshot.Page)
		}
		return true
	}
	if snapshot.FocusSidebar {
		switch key {
		case tuiKeyUp:
			snapshot.SelectedMenu = wrapTUIIndex(snapshot.SelectedMenu, -1, int(tuiPageCount))
		case tuiKeyDown:
			snapshot.SelectedMenu = wrapTUIIndex(snapshot.SelectedMenu, 1, int(tuiPageCount))
		case tuiKeySelect, tuiKeyRight:
			snapshot.Page = tuiPage(wrapTUIIndex(snapshot.SelectedMenu, 0, int(tuiPageCount)))
			snapshot.SelectedMenu = int(snapshot.Page)
			snapshot.FocusSidebar = false
			snapshot.ProxyNodeFocus = false
			snapshot.SSHDashboardFocus = false
		case tuiKeyLeft:
		default:
			return false
		}
		return true
	}
	switch key {
	case tuiKeyLeft:
		snapshot.FocusSidebar = true
		snapshot.SSHDashboardFocus = false
		snapshot.SelectedMenu = int(snapshot.Page)
		return true
	case tuiKeyRight:
		return true
	}
	return false
}

func tuiPageForKey(key tuiKey) (tuiPage, bool) {
	switch key {
	case tuiKeyDashboard:
		return tuiPageDashboard, true
	case tuiKeyProxies:
		return tuiPageProxies, true
	case tuiKeyProfiles:
		return tuiPageProfiles, true
	case tuiKeySSH:
		return tuiPageSSH, true
	case tuiKeyRequests:
		return tuiPageRequests, true
	case tuiKeyConnections:
		return tuiPageConnections, true
	case tuiKeyLogs:
		return tuiPageLogs, true
	case tuiKeySettings:
		return tuiPageTools, true
	case tuiKeyTools:
		return tuiPageTools, true
	case tuiKeyMaintenance:
		return tuiPageMaintenance, true
	default:
		return tuiPageDashboard, false
	}
}
