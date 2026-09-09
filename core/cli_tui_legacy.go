//go:build linux && !cgo && cli

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	logrus "github.com/sirupsen/logrus"
	"golang.org/x/term"
)

func runLegacyTUI(client controllerClient, paths cliPaths, setupParams []byte, ownsCore bool) error {
	if !isInteractiveTUI() {
		return errors.New("TUI requires an interactive terminal; use run or proxy commands in non-interactive shells")
	}

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("enable raw terminal mode: %w", err)
	}
	enterTUIScreen()
	defer func() {
		_ = leaveTUIMode(oldState)
	}()

	logrus.SetOutput(io.Discard)
	handleStartLog()
	defer handleStopLog()
	snapshot := tuiSnapshot{
		Status:            "Loading...",
		GroupOrder:        loadTUIProxyGroupOrder(paths.configPath),
		SelectedGroup:     0,
		SelectedNode:      0,
		SelectedRow:       tuiProfileImportSubscriptionRow,
		SelectedMenu:      int(tuiPageDashboard),
		SelectedDashboard: tuiDashboardServiceRow,
		FocusSidebar:      true,
	}
	refreshTUISnapshot(&snapshot, client)
	refreshTUIProfiles(&snapshot, paths)
	coreRunning := false
	systemProxyManaged := false
	screen := &tuiFrameWriter{writer: os.Stdout}
	draw := func() {
		drawTUI(screen, snapshot, paths, client.options.address, ownsCore, coreRunning)
	}
	shutdown := func() {
		if ownsCore && systemProxyManaged &&
			snapshot.Settings.SystemProxy &&
			linuxSystemProxyMatches(snapshot.Settings.MixedPort) {
			_ = setLinuxSystemProxy(snapshot.Settings.MixedPort, false)
			snapshot.Settings.SystemProxy = false
		}
		if ownsCore {
			handleShutdown()
		}
	}
	toggleCore := func() {
		if !ownsCore {
			snapshot.Status = "Core lifecycle is owned by the external process"
		} else if coreRunning {
			if handleStopListener() {
				coreRunning = false
				snapshot.Status = "Core listeners stopped"
				if systemProxyManaged && snapshot.Settings.SystemProxy {
					if !linuxSystemProxyMatches(snapshot.Settings.MixedPort) {
						snapshot.Settings.SystemProxy = false
						systemProxyManaged = false
						snapshot.Status += "; another instance owns the system proxy"
					} else if err := setLinuxSystemProxy(snapshot.Settings.MixedPort, false); err != nil {
						snapshot.Status += "; system proxy cleanup failed: " + err.Error()
					} else {
						snapshot.Settings.SystemProxy = false
						systemProxyManaged = false
					}
				}
			}
		} else if handleStartListener() {
			coreRunning = true
			snapshot.Status = "Core listeners started"
			refreshTUISnapshot(&snapshot, client)
		}
	}
	toggleSystemProxy := func() {
		autoStarted := false
		if ownsCore && !coreRunning {
			toggleCore()
			if !coreRunning {
				return
			}
			autoStarted = true
		}
		if toggleTUISystemProxy(&snapshot) {
			systemProxyManaged = snapshot.Settings.SystemProxy
			if autoStarted && snapshot.Settings.SystemProxy {
				snapshot.Status = fmt.Sprintf(
					"Core started on port %d; system proxy enabled",
					snapshot.Settings.MixedPort,
				)
			}
		}
	}
	draw()

	keys := make(chan tuiKey)
	keyHandled := make(chan struct{})
	go readTUIKeysSynchronized(os.Stdin, keys, keyHandled)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGWINCH)
	defer signal.Stop(signals)
	ticker := time.NewTicker(tuiRefreshInterval)
	defer ticker.Stop()

	for {
		select {
		case receivedSignal := <-signals:
			switch receivedSignal {
			case syscall.SIGWINCH:
				screen.invalidate()
				draw()
				continue
			case syscall.SIGHUP:
				if !ownsCore {
					snapshot.Status = "Reload requires a core started by this process"
				} else if message := handleSetupConfig(setupParams); message != "" {
					snapshot.Status = "Reload failed: " + message
				} else {
					snapshot.Status = "Configuration reloaded"
					refreshTUISnapshot(&snapshot, client)
				}
				draw()
				continue
			default:
				shutdown()
				return nil
			}
		case key, open := <-keys:
			if !open {
				shutdown()
				return nil
			}
			if snapshot.ShowHelp && key != tuiKeyHelp && key != tuiKeyQuit {
				snapshot.ShowHelp = false
				draw()
				keyHandled <- struct{}{}
				continue
			}
			if handleTUIFocusNavigation(&snapshot, key) {
				draw()
				keyHandled <- struct{}{}
				continue
			}
			switch key {
			case tuiKeyQuit:
				shutdown()
				return nil
			case tuiKeyRefresh:
				refreshTUISnapshot(&snapshot, client)
				refreshTUIProfiles(&snapshot, paths)
			case tuiKeyReload:
				if !ownsCore {
					snapshot.Status = "Reload requires a core started by this process"
					break
				}
				if message := handleSetupConfig(setupParams); message != "" {
					snapshot.Status = "Reload failed: " + message
				} else {
					snapshot.Status = "Configuration reloaded"
					refreshTUISnapshot(&snapshot, client)
				}
			case tuiKeyHelp:
				snapshot.ShowHelp = !snapshot.ShowHelp
			case tuiKeyCloseConnections:
				if snapshot.Page == tuiPageRequests {
					snapshot.Requests = nil
					snapshot.SelectedRequest = 0
					snapshot.Status = "History cleared"
					break
				}
				if snapshot.Page == tuiPageLogs {
					clearTUILogs()
					snapshot.Logs = nil
					snapshot.Status = "Logs cleared"
					break
				}
				if snapshot.Page != tuiPageConnections {
					break
				}
				if err := client.closeAllConnections(); err != nil {
					snapshot.Status = "Close connections failed: " + err.Error()
				} else {
					snapshot.Status = "All connections closed"
				}
			case tuiKeyCloseConnection:
				if snapshot.Page != tuiPageConnections || snapshot.SelectedConnection < 0 || snapshot.SelectedConnection >= len(snapshot.Connections) {
					break
				}
				connection := snapshot.Connections[snapshot.SelectedConnection]
				if err := client.closeConnection(connection.ID); err != nil {
					snapshot.Status = "Close connection failed: " + err.Error()
				} else {
					snapshot.Status = "Connection closed"
					refreshTUISnapshot(&snapshot, client)
				}
			case tuiKeyCoreToggle:
				toggleCore()
			case tuiKeyEdit:
				if snapshot.Page == tuiPageLogs {
					path, err := exportTUILogs(paths.homeDir, snapshot.Logs)
					if err != nil {
						snapshot.Status = "Export logs failed: " + err.Error()
					} else {
						snapshot.Status = "Logs exported: " + path
					}
					break
				}
				if snapshot.Page == tuiPageProfiles &&
					(snapshot.SelectedRow < 0 || snapshot.SelectedRow >= len(snapshot.Profiles)) {
					snapshot.Status = "Select a profile before editing its YAML"
					break
				}
				if snapshot.Page != tuiPageProfiles && snapshot.Page != tuiPageTools {
					snapshot.Status = "Edit YAML is available in Profiles and Maintenance"
					break
				}
				screen.invalidate()
				editPath := paths.configPath
				if snapshot.Page == tuiPageProfiles && snapshot.SelectedRow >= 0 && snapshot.SelectedRow < len(snapshot.Profiles) {
					editPath = snapshot.Profiles[snapshot.SelectedRow].Path
				}
				if err := runTUIEditor(editPath, &oldState); err != nil {
					snapshot.Status = "Editor failed: " + err.Error()
				} else if ownsCore {
					if message := handleSetupConfig(setupParams); message != "" {
						snapshot.Status = "Edited config is invalid: " + message
					} else {
						snapshot.Status = "Configuration applied"
					}
				} else {
					snapshot.Status = "Configuration saved; reload the external core to apply it"
				}
			case tuiKeyNewProfile:
				if snapshot.Page == tuiPageProfiles {
					screen.invalidate()
					if err := addTUIProfile(paths.homeDir, &oldState); err != nil {
						if errors.Is(err, errTUIActionCancelled) {
							snapshot.Status = "Profile download cancelled"
						} else {
							snapshot.Status = "Add profile failed: " + err.Error()
						}
					} else {
						snapshot.Status = "Profile downloaded"
						refreshTUIProfiles(&snapshot, paths)
					}
				}
			case tuiKeyProviders:
				snapshot.Page = tuiPageProxies
				snapshot.SelectedMenu = int(tuiPageProxies)
				snapshot.FocusSidebar = false
				snapshot.ProxyView = tuiProxyViewProviders
			case tuiKeyBackup:
				if snapshot.Page != tuiPageTools {
					break
				}
				executeTUITool(1, &snapshot, paths, setupParams, client, ownsCore, &oldState)
			case tuiKeyRestore:
				if snapshot.Page != tuiPageTools {
					break
				}
				executeTUITool(2, &snapshot, paths, setupParams, client, ownsCore, &oldState)
			case tuiKeyGeoUpdate:
				if snapshot.Page != tuiPageTools {
					break
				}
				executeTUITool(3, &snapshot, paths, setupParams, client, ownsCore, &oldState)
			case tuiKeyResetTraffic:
				if snapshot.Page != tuiPageTools {
					break
				}
				executeTUITool(4, &snapshot, paths, setupParams, client, ownsCore, &oldState)
			case tuiKeyUp:
				if snapshot.Page == tuiPageProfiles {
					moveTUIProfile(&snapshot, -1)
				} else if snapshot.Page == tuiPageRequests {
					snapshot.SelectedRequest = wrapTUIIndex(snapshot.SelectedRequest, -1, len(snapshot.Requests))
				} else if snapshot.Page == tuiPageConnections {
					moveTUIConnection(&snapshot, -1)
				} else if snapshot.Page == tuiPageProxies {
					if snapshot.ProxyView == tuiProxyViewProviders {
						moveTUIProvider(&snapshot, -1)
					} else {
						moveTUIGroup(&snapshot, -1)
					}
				} else if snapshot.Page == tuiPageDashboard {
					snapshot.SelectedDashboard = wrapTUIIndex(snapshot.SelectedDashboard, -1, tuiDashboardRowCount)
				} else if snapshot.Page == tuiPageTools {
					snapshot.SelectedTool = wrapTUIIndex(snapshot.SelectedTool, -1, tuiSettingsRowCount)
				}
			case tuiKeyDown:
				if snapshot.Page == tuiPageProfiles {
					moveTUIProfile(&snapshot, 1)
				} else if snapshot.Page == tuiPageRequests {
					snapshot.SelectedRequest = wrapTUIIndex(snapshot.SelectedRequest, 1, len(snapshot.Requests))
				} else if snapshot.Page == tuiPageConnections {
					moveTUIConnection(&snapshot, 1)
				} else if snapshot.Page == tuiPageProxies {
					if snapshot.ProxyView == tuiProxyViewProviders {
						moveTUIProvider(&snapshot, 1)
					} else {
						moveTUIGroup(&snapshot, 1)
					}
				} else if snapshot.Page == tuiPageDashboard {
					snapshot.SelectedDashboard = wrapTUIIndex(snapshot.SelectedDashboard, 1, tuiDashboardRowCount)
				} else if snapshot.Page == tuiPageTools {
					snapshot.SelectedTool = wrapTUIIndex(snapshot.SelectedTool, 1, tuiSettingsRowCount)
				}
			case tuiKeyDelayTest:
				if snapshot.Page == tuiPageProxies &&
					snapshot.ProxyView == tuiProxyViewGroups &&
					snapshot.SelectedGroup >= 0 &&
					snapshot.SelectedGroup < len(snapshot.Groups) {
					group := snapshot.Groups[snapshot.SelectedGroup]
					if snapshot.SelectedNode >= 0 &&
						snapshot.SelectedNode < len(group.Nodes) {
						node := group.Nodes[snapshot.SelectedNode]
						delay, err := client.testProxyDelay(
							node,
							"https://www.gstatic.com/generate_204",
						)
						if err != nil {
							snapshot.Status = "Delay test failed: " + err.Error()
						} else {
							snapshot.Status = fmt.Sprintf("%s delay: %d ms", node, delay)
						}
					}
				}
			case tuiKeyViewPrevious, tuiKeyViewNext:
				if !snapshot.FocusSidebar && snapshot.Page == tuiPageProxies {
					delta := 1
					if key == tuiKeyViewPrevious {
						delta = -1
					}
					snapshot.ProxyView = wrapTUIIndex(snapshot.ProxyView, delta, tuiProxyViewCount)
				}
			case tuiKeySelect:
				if snapshot.Page == tuiPageDashboard {
					switch snapshot.SelectedDashboard {
					case tuiDashboardServiceRow:
						toggleCore()
					case tuiDashboardSystemProxyRow:
						toggleSystemProxy()
					case tuiDashboardTunRow:
						updateTUISettings(&snapshot, client, tuiKeyTun)
					case tuiDashboardModeRow:
						updateTUISettings(&snapshot, client, tuiKeyMode)
					case tuiDashboardFLCOutboundRow:
						snapshot.Page = tuiPageProxies
						snapshot.SelectedMenu = int(tuiPageProxies)
						snapshot.FocusSidebar = false
						snapshot.Status = "Select a proxy node; flc follows that group"
					case tuiDashboardMixedPortRow:
						screen.invalidate()
						setTUIMixedPort(&snapshot, client, &oldState)
					case tuiDashboardDelayRow:
						snapshot.Status = "Dashboard route delay testing requires the managed TUI"
					case tuiDashboardSpeedRow:
						snapshot.Status = "Dashboard route speed testing requires the managed TUI"
					}
				} else if snapshot.Page == tuiPageProxies {
					if snapshot.ProxyView == tuiProxyViewProviders {
						updateTUIProvider(&snapshot, client)
					} else {
						selectTUIProxy(&snapshot, client, paths.homeDir)
					}
				} else if snapshot.Page == tuiPageProfiles {
					switchTUIProfile(&snapshot, &paths, &setupParams, client, ownsCore, coreRunning)
				} else if snapshot.Page == tuiPageTools {
					switch snapshot.SelectedTool {
					case tuiSettingsAllowLANRow:
						updateTUISettings(&snapshot, client, tuiKeyAllowLAN)
					case tuiSettingsIPv6Row:
						updateTUISettings(&snapshot, client, tuiKeyIPv6)
					case tuiSettingsUnifiedDelayRow:
						updateTUISettings(&snapshot, client, tuiKeyUnifiedDelay)
					case tuiSettingsTCPConcurrentRow:
						updateTUISettings(&snapshot, client, tuiKeyTCPConcurrent)
					case tuiSettingsLogLevelRow:
						updateTUISettings(&snapshot, client, tuiKeyLogLevel)
					case tuiSettingsTunScopeRow:
						snapshot.Status = "TUN scope changes require the managed Backend"
					}
				}
			case tuiKeyAllowLAN, tuiKeyIPv6, tuiKeyUnifiedDelay, tuiKeyTCPConcurrent,
				tuiKeyLogLevel:
				if snapshot.Page == tuiPageTools {
					updateTUISettings(&snapshot, client, key)
				}
			case tuiKeyTun, tuiKeyMode, tuiKeyPortUp, tuiKeyPortDown:
				if snapshot.Page == tuiPageDashboard {
					updateTUISettings(&snapshot, client, key)
				}
			case tuiKeySetPort:
				if snapshot.Page == tuiPageDashboard {
					screen.invalidate()
					setTUIMixedPort(&snapshot, client, &oldState)
				}
			case tuiKeySystemProxy:
				if snapshot.Page == tuiPageDashboard {
					toggleSystemProxy()
				}
			}
			draw()
			keyHandled <- struct{}{}
		case <-ticker.C:
			refreshTUISnapshot(&snapshot, client)
			refreshTUIProfiles(&snapshot, paths)
			draw()
		}
	}
}

func backupTUIConfig(configPath string) (string, error) {
	lease, err := acquireTUIProfileLocks(filepath.Dir(configPath), configPath)
	if err != nil {
		return "", err
	}
	defer lease.release()
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", err
	}
	backupPath := fmt.Sprintf("%s.backup-%d", configPath, time.Now().UnixNano())
	if err := os.WriteFile(backupPath, data, 0o600); err != nil {
		return "", err
	}
	return backupPath, nil
}

func executeTUITool(
	index int,
	snapshot *tuiSnapshot,
	paths cliPaths,
	setupParams []byte,
	client controllerClient,
	ownsCore bool,
	oldState **term.State,
) {
	switch index {
	case 0:
		if err := runTUIEditor(paths.configPath, oldState); err != nil {
			snapshot.Status = "Editor failed: " + err.Error()
		} else if ownsCore {
			if message := handleSetupConfig(setupParams); message != "" {
				snapshot.Status = "Edited config is invalid: " + message
			} else {
				snapshot.Status = "Configuration applied"
			}
		} else {
			snapshot.Status = "Configuration saved; reload the external core to apply it"
		}
	case 1:
		if backupPath, err := backupTUIConfig(paths.configPath); err != nil {
			snapshot.Status = "Backup failed: " + err.Error()
		} else {
			snapshot.Status = "Backup created: " + filepath.Base(backupPath)
		}
	case 2:
		if backupPath, err := restoreLatestTUIConfig(paths.configPath); err != nil {
			snapshot.Status = "Restore failed: " + err.Error()
		} else if ownsCore {
			if message := handleSetupConfig(setupParams); message != "" {
				snapshot.Status = "Restore applied with errors: " + message
			} else {
				snapshot.Status = "Restored: " + filepath.Base(backupPath)
			}
		} else {
			snapshot.Status = "Restored: " + filepath.Base(backupPath) + "; reload the external core to apply it"
		}
	case 3:
		if err := client.updateGeo(); err != nil {
			snapshot.Status = "Geo update failed: " + err.Error()
		} else {
			snapshot.Status = "Geo databases update started"
		}
	case 4:
		if ownsCore {
			handleResetTraffic()
			snapshot.Status = "Traffic counters reset"
		} else {
			snapshot.Status = "Traffic reset requires a core started by this process"
		}
	case 5:
		checkTUIUpdate(snapshot)
	}
}

func checkTUIUpdate(snapshot *tuiSnapshot) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	release, err := fetchLatestCLIRelease(
		ctx,
		cliUpdateHTTPClient,
		cliLatestReleaseAPIURL,
	)
	snapshot.Update.Loading = false
	snapshot.Update.CheckedAt = time.Now()
	if err != nil {
		snapshot.Update.Error = err.Error()
		snapshot.Status = "Update check failed: " + err.Error()
		return
	}
	latestVersion := normalizeCLIVersion(release.TagName)
	if latestVersion == "" {
		snapshot.Update.Error = "invalid release version " + release.TagName
		snapshot.Status = "Update check failed: invalid release version"
		return
	}
	snapshot.Update.Error = ""
	snapshot.Update.LatestVersion = latestVersion
	snapshot.Update.ReleaseURL = release.HTMLURL
	snapshot.Update.Available = isNewerCLIVersion(latestVersion, cliVersion)
	if snapshot.Update.Available {
		snapshot.Status = fmt.Sprintf(
			"v%s available · %s · quit and run: flclash update",
			latestVersion,
			cliUpdateWarning,
		)
		return
	}
	snapshot.Status = fmt.Sprintf(
		"v%s is current · %s",
		cliVersion,
		cliUpdateWarning,
	)
}

func restoreLatestTUIConfig(configPath string) (string, error) {
	backupPath, backup, err := restoreLatestTUIConfigLocked(
		filepath.Dir(configPath),
		configPath,
	)
	backup.release()
	return backupPath, err
}

func restoreLatestTUIConfigLocked(
	homeDir,
	configPath string,
) (string, tuiProfileBackup, error) {
	lease, err := acquireTUIProfileLocks(homeDir, configPath)
	if err != nil {
		return "", tuiProfileBackup{}, err
	}
	releaseOnError := true
	defer func() {
		if releaseOnError {
			lease.release()
		}
	}()
	original, err := os.ReadFile(configPath)
	if err != nil {
		return "", tuiProfileBackup{}, err
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return "", tuiProfileBackup{}, err
	}
	entries, err := os.ReadDir(filepath.Dir(configPath))
	if err != nil {
		return "", tuiProfileBackup{}, err
	}
	prefix := filepath.Base(configPath) + ".backup-"
	var latest string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		candidate := filepath.Join(filepath.Dir(configPath), entry.Name())
		if latest == "" || entry.Name() > filepath.Base(latest) {
			latest = candidate
		}
	}
	if latest == "" {
		return "", tuiProfileBackup{}, errors.New("no config backup found")
	}
	data, err := os.ReadFile(latest)
	if err != nil {
		return "", tuiProfileBackup{}, err
	}
	if message := validateConfigBytes(data); message != "" {
		return "", tuiProfileBackup{}, fmt.Errorf("restored config is invalid: %s", message)
	}
	if err := writeTUIProfileAtomically(configPath, data, info.Mode()); err != nil {
		return "", tuiProfileBackup{}, err
	}
	backup := tuiProfileBackup{
		data:          original,
		mode:          info.Mode(),
		updatedSHA256: tuiBytesSHA256(data),
		lock:          lease,
	}
	releaseOnError = false
	return latest, backup, nil
}
