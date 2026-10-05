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
	cliHub.StartLog()
	defer cliHub.StopLog()
	snapshot := tuiSnapshot{
		Status:            "Loading...",
		GroupOrder:        loadTUIProxyGroupOrder(paths.ConfigPath),
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
			cliHub.Shutdown()
		}
	}
	toggleCore := func() {
		if !ownsCore {
			snapshot.setStatus(newTUIMessage("ui.cd919234613f"))
		} else if coreRunning {
			if cliHub.StopListener() {
				coreRunning = false
				snapshot.setStatus(newTUIMessage("ui.d7a2587ef8f5"))
				if systemProxyManaged && snapshot.Settings.SystemProxy {
					if !linuxSystemProxyMatches(snapshot.Settings.MixedPort) {
						snapshot.Settings.SystemProxy = false
						systemProxyManaged = false
						snapshot.appendStatus(newTUIMessage("ui.401493c0a318"))
					} else if err := setLinuxSystemProxy(snapshot.Settings.MixedPort, false); err != nil {
						snapshot.appendStatus(newTUIMessage("ui.42dbf63e8261", err.Error()))
					} else {
						snapshot.Settings.SystemProxy = false
						systemProxyManaged = false
					}
				}
			}
		} else if cliHub.StartListener() {
			coreRunning = true
			snapshot.setStatus(newTUIMessage("ui.95855b291942"))
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
				snapshot.setStatus(newTUIMessage("ui.648bc55423be", snapshot.Settings.MixedPort))

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
					snapshot.setStatus(newTUIMessage("ui.7602af8e4854"))
				} else if message := cliHub.SetupConfig(setupParams); message != "" {
					snapshot.setStatus(newTUIMessage("ui.0b3a51a19453", message))
				} else {
					snapshot.setStatus(newTUIMessage("ui.7b3279fdc1ff"))
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
					snapshot.setStatus(newTUIMessage("ui.7602af8e4854"))
					break
				}
				if message := cliHub.SetupConfig(setupParams); message != "" {
					snapshot.setStatus(newTUIMessage("ui.0b3a51a19453", message))
				} else {
					snapshot.setStatus(newTUIMessage("ui.7b3279fdc1ff"))
					refreshTUISnapshot(&snapshot, client)
				}
			case tuiKeyHelp:
				snapshot.ShowHelp = !snapshot.ShowHelp
			case tuiKeyCloseConnections:
				if snapshot.Page == tuiPageRequests {
					snapshot.Requests = nil
					snapshot.SelectedRequest = 0
					snapshot.setStatus(newTUIMessage("ui.183e4443c02f"))
					break
				}
				if snapshot.Page == tuiPageLogs {
					clearTUILogs()
					snapshot.Logs = nil
					snapshot.setStatus(newTUIMessage("ui.0cd1f5ed362f"))
					break
				}
				if snapshot.Page != tuiPageConnections {
					break
				}
				if err := client.closeAllConnections(); err != nil {
					snapshot.setStatus(newTUIMessage("ui.a179d29a704a", err.Error()))
				} else {
					snapshot.setStatus(newTUIMessage("ui.837ad1848112"))
				}
			case tuiKeyCloseConnection:
				if snapshot.Page != tuiPageConnections || snapshot.SelectedConnection < 0 || snapshot.SelectedConnection >= len(snapshot.Connections) {
					break
				}
				connection := snapshot.Connections[snapshot.SelectedConnection]
				if err := client.closeConnection(connection.ID); err != nil {
					snapshot.setStatus(newTUIMessage("ui.a372309c919a", err.Error()))
				} else {
					snapshot.setStatus(newTUIMessage("ui.fdb770cf60c2"))
					refreshTUISnapshot(&snapshot, client)
				}
			case tuiKeyCoreToggle:
				toggleCore()
			case tuiKeyEdit:
				if snapshot.Page == tuiPageLogs {
					path, err := exportTUILogs(paths.HomeDir, snapshot.Logs)
					if err != nil {
						snapshot.setStatus(newTUIMessage("ui.a1978577d9c8", err.Error()))
					} else {
						snapshot.setStatus(newTUIMessage("ui.3c3165db18d2", path))
					}
					break
				}
				if snapshot.Page == tuiPageProfiles &&
					(snapshot.SelectedRow < 0 || snapshot.SelectedRow >= len(snapshot.Profiles)) {
					snapshot.setStatus(newTUIMessage("ui.828aebbc21b9"))
					break
				}
				if snapshot.Page != tuiPageProfiles && snapshot.Page != tuiPageTools {
					snapshot.setStatus(newTUIMessage("ui.49c4e78c047b"))
					break
				}
				screen.invalidate()
				editPath := paths.ConfigPath
				if snapshot.Page == tuiPageProfiles && snapshot.SelectedRow >= 0 && snapshot.SelectedRow < len(snapshot.Profiles) {
					editPath = snapshot.Profiles[snapshot.SelectedRow].Path
				}
				if err := runTUIEditor(editPath, &oldState); err != nil {
					snapshot.setStatus(newTUIMessage("ui.d06cf3587589", err.Error()))
				} else if ownsCore {
					if message := cliHub.SetupConfig(setupParams); message != "" {
						snapshot.setStatus(newTUIMessage("ui.4795efe5b117", message))
					} else {
						snapshot.setStatus(newTUIMessage("ui.455f8f165480"))
					}
				} else {
					snapshot.setStatus(newTUIMessage("ui.28da573f2104"))
				}
			case tuiKeyNewProfile:
				if snapshot.Page == tuiPageProfiles {
					screen.invalidate()
					if err := addTUIProfile(paths.HomeDir, &oldState); err != nil {
						if errors.Is(err, errTUIActionCancelled) {
							snapshot.setStatus(newTUIMessage("ui.c1fbc05c228e"))
						} else {
							snapshot.setStatus(newTUIMessage("ui.4f89b986ab7e", err.Error()))
						}
					} else {
						snapshot.setStatus(newTUIMessage("ui.13929d79cbed"))
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
							snapshot.setStatus(newTUIMessage("ui.0ebfa5742b16", err.Error()))
						} else {
							snapshot.setStatus(newTUIMessage("ui.49e9969582e8", node, delay))
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
						snapshot.setStatus(newTUIMessage("ui.5c535d92dda4"))
					case tuiDashboardMixedPortRow:
						screen.invalidate()
						setTUIMixedPort(&snapshot, client, &oldState)
					case tuiDashboardDelayRow:
						snapshot.setStatus(newTUIMessage("ui.38480b0a5d4c"))
					case tuiDashboardSpeedRow:
						snapshot.setStatus(newTUIMessage("ui.929eb303f25a"))
					}
				} else if snapshot.Page == tuiPageProxies {
					if snapshot.ProxyView == tuiProxyViewProviders {
						updateTUIProvider(&snapshot, client)
					} else {
						selectTUIProxy(&snapshot, client, paths.HomeDir)
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
						snapshot.setStatus(newTUIMessage("ui.604f5593583b"))
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
		if err := runTUIEditor(paths.ConfigPath, oldState); err != nil {
			snapshot.setStatus(newTUIMessage("ui.d06cf3587589", err.Error()))
		} else if ownsCore {
			if message := cliHub.SetupConfig(setupParams); message != "" {
				snapshot.setStatus(newTUIMessage("ui.4795efe5b117", message))
			} else {
				snapshot.setStatus(newTUIMessage("ui.455f8f165480"))
			}
		} else {
			snapshot.setStatus(newTUIMessage("ui.28da573f2104"))
		}
	case 1:
		if backupPath, err := backupTUIConfig(paths.ConfigPath); err != nil {
			snapshot.setStatus(newTUIMessage("ui.87ab982de4ef", err.Error()))
		} else {
			snapshot.setStatus(newTUIMessage("ui.926013bbeabf", filepath.Base(backupPath)))
		}
	case 2:
		if backupPath, err := restoreLatestTUIConfig(paths.ConfigPath); err != nil {
			snapshot.setStatus(newTUIMessage("ui.84bb92888280", err.Error()))
		} else if ownsCore {
			if message := cliHub.SetupConfig(setupParams); message != "" {
				snapshot.setStatus(newTUIMessage("ui.e247228cf04a", message))
			} else {
				snapshot.setStatus(newTUIMessage("ui.0376075ef9a4", filepath.Base(backupPath)))
			}
		} else {
			snapshot.setStatus(newTUIMessage("ui.c58a91b16bd5", filepath.Base(backupPath)))
		}
	case 3:
		if err := client.updateGeo(); err != nil {
			snapshot.setStatus(newTUIMessage("ui.a0a5ec4d845b", err.Error()))
		} else {
			snapshot.setStatus(newTUIMessage("ui.532856d3bd98"))
		}
	case 4:
		if ownsCore {
			cliHub.ResetTraffic()
			snapshot.setStatus(newTUIMessage("ui.984eb5b20446"))
		} else {
			snapshot.setStatus(newTUIMessage("ui.e351b8fb7841"))
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
		snapshot.setStatus(newTUIMessage("ui.c60759d564bd", err.Error()))
		return
	}
	latestVersion := normalizeCLIVersion(release.TagName)
	if latestVersion == "" {
		snapshot.Update.Error = "invalid release version " + release.TagName
		snapshot.setStatus(newTUIMessage("ui.57fcdb1bed64"))
		return
	}
	snapshot.Update.Error = ""
	snapshot.Update.LatestVersion = latestVersion
	snapshot.Update.ReleaseURL = release.HTMLURL
	snapshot.Update.Available = isNewerCLIVersion(latestVersion, cliVersion)
	if snapshot.Update.Available {
		snapshot.setStatus(newTUIMessage("ui.bdee1acc5021", latestVersion,
			cliUpdateWarning))

		return
	}
	snapshot.setStatus(newTUIMessage("ui.cccf36fbdaf8", cliVersion,
		cliUpdateWarning))

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
