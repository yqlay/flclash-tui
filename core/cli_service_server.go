//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	logrus "github.com/sirupsen/logrus"
)

func serviceCommand(args []string) error {
	fs := flag.NewFlagSet("_service", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configArg := fs.String("config", "", "path to config.yaml")
	directoryArg := fs.String("directory", "", "FlClash data directory")
	testURLArg := fs.String(
		"test-url",
		"https://www.gstatic.com/generate_204",
		"URL used by proxy-group delay tests",
	)
	lockFDArg := fs.Int("lock-fd", -1, "inherited backend lock descriptor")
	createConfigArg := fs.Bool("create-config", false, "create the default profile when missing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	paths, err := resolvePaths(*configArg, *directoryArg)
	if err != nil {
		return err
	}
	var backendLock *cliFileLock
	if *lockFDArg >= 0 {
		lockFile := os.NewFile(
			uintptr(*lockFDArg),
			"flclash-backend-lock",
		)
		backendLock, err = adoptCLIBackendLock(
			lockFile,
			cliProcessOwner{
				Kind:       "service",
				HomeDir:    paths.HomeDir,
				ConfigPath: paths.ConfigPath,
			},
		)
		if err != nil {
			return err
		}
	}
	return runTUIService(paths, *testURLArg, backendLock, *createConfigArg)
}

func runTUIService(
	paths cliPaths,
	testURL string,
	backendLock *cliFileLock,
	allowCreate bool,
) error {
	if err := os.MkdirAll(paths.HomeDir, 0o700); err != nil {
		return err
	}
	logWriter, logErr := newTUIRotatingLogWriter(
		filepath.Join(paths.HomeDir, tuiServiceLogFilename),
	)
	if logErr == nil {
		originalLogOutput := logrus.StandardLogger().Out
		logrus.SetOutput(logWriter)
		defer func() {
			logrus.SetOutput(originalLogOutput)
			_ = logWriter.Close()
		}()
	}
	if err := ensureTUIConfig(paths, allowCreate); err != nil {
		return err
	}
	if err := rememberTUIActiveProfile(paths); err != nil {
		return fmt.Errorf("remember active profile: %w", err)
	}
	runtimeDirectory, err := ensureCLIRuntimeDirectory()
	if err != nil {
		return err
	}
	managerSocket := filepath.Join(
		runtimeDirectory,
		tuiServiceSocketFilename,
	)
	coreSocket := filepath.Join(paths.HomeDir, tuiCoreSocketFilename)
	if backendLock == nil {
		backendLock, err = acquireCLIBackendLock(cliProcessOwner{
			Kind:       "service",
			HomeDir:    paths.HomeDir,
			ConfigPath: paths.ConfigPath,
		})
		if err != nil {
			return err
		}
	} else if err := backendLock.setOwner(cliProcessOwner{
		Kind:       "service",
		HomeDir:    paths.HomeDir,
		ConfigPath: paths.ConfigPath,
	}); err != nil {
		backendLock.release()
		return err
	}
	defer backendLock.release()
	if err := ensureTUIFlClashDefaults(paths.ConfigPath); err != nil {
		return fmt.Errorf("apply FlClash defaults: %w", err)
	}
	if err := removeStaleTUIServiceSocket(
		runtimeDirectory,
		managerSocket,
	); err != nil {
		return err
	}
	if err := removeStaleTUIServiceSocket(
		paths.HomeDir,
		coreSocket,
	); err != nil {
		return err
	}
	configuredSettings := loadTUIConfiguredSettings(paths.ConfigPath, true)
	configuredPort := 0
	if configuredSettings != nil {
		configuredPort = configuredSettings.MixedPort
	}
	trafficMode := loadTUITrafficMode(paths.HomeDir, paths.ConfigPath)
	tunScope := loadTUITunScope(paths.HomeDir)
	tunEnabled := configuredSettings != nil && configuredSettings.TunEnabled && tunScope == tuiTunScopeUser
	actualPaths := paths
	runtimePort := configuredPort
	flcState := tuiFLCListenerState{Outbound: loadTUIFLCOutbound(paths.HomeDir)}
	cleanupTUISilentRuntimeConfigs(paths.HomeDir, "")
	if trafficMode == tuiSilentMode {
		tunEnabled = false
		runtimePort, err = chooseTUIProxyPort(configuredPort)
		if err != nil {
			return err
		}
		if flcState.Outbound != "" {
			flcState, err = newTUIFLCListenerStateAtPort(
				flcState.Outbound,
				runtimePort,
			)
			if err != nil {
				return err
			}
		}
		actualPaths.ConfigPath, err = writeTUISilentRuntimeConfig(paths, flcState)
		if err != nil {
			return err
		}
		defer cleanupTUISilentRuntimeConfigs(paths.HomeDir, "")
		if configuredPort > 0 && linuxSystemProxyMatches(configuredPort) {
			if err := setLinuxSystemProxy(configuredPort, false); err != nil {
				return fmt.Errorf("disable system proxy for silent mode: %w", err)
			}
		}
	} else {
		if configuredPort > 0 {
			runtimePort, err = chooseTUIProxyPort(configuredPort)
			if err != nil {
				return err
			}
		}
		actualPaths.ConfigPath, err = writeTUIManagedRuntimeConfig(
			paths,
			trafficMode,
			runtimePort,
			false,
			tunScope,
			0,
		)
		if err != nil {
			return err
		}
		defer cleanupTUISilentRuntimeConfigs(paths.HomeDir, "")
	}
	setupParams, err := initializeCore(
		actualPaths,
		testURL,
		"",
		coreSocket,
		"",
		false,
	)
	if err != nil {
		return err
	}
	defer handleShutdown()

	listener, err := net.Listen("unix", managerSocket)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(managerSocket)
	defer os.Remove(coreSocket)
	if err := os.Chmod(managerSocket, 0o600); err != nil {
		return err
	}
	_ = os.Chmod(coreSocket, 0o600)

	shutdown := make(chan struct{})
	var shutdownOnce sync.Once
	runtime := newTUIServiceRuntime(
		paths,
		testURL,
		coreSocket,
		setupParams,
		func() {
			shutdownOnce.Do(func() { close(shutdown) })
		},
	)
	defer runtime.closeCoreController()
	runtime.configureManagedRuntimePolicy(
		trafficMode,
		configuredPort,
		runtimePort,
		actualPaths.ConfigPath,
		flcState,
		tunScope,
		tunEnabled,
	)
	if err := runtime.restoreHistory(); err != nil {
		appendCLIApplicationLog(
			paths.HomeDir,
			"WARN",
			"history_restore",
			err.Error()+"; starting with empty History",
		)
	}
	historyCollectorDone := make(chan struct{})
	go func() {
		defer close(historyCollectorDone)
		collectTUIServiceHistory(runtime, shutdown)
	}()
	if settings := loadTUIConfiguredSettings(paths.ConfigPath, true); settings != nil {
		if trafficMode != tuiSilentMode {
			runtime.setSystemProxyState(
				linuxSystemProxyMatches(settings.MixedPort),
				settings.MixedPort,
			)
		}
	}
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(interrupt)
	serviceCleanupDone := make(chan struct{})
	go func() {
		defer close(serviceCleanupDone)
		select {
		case <-interrupt:
			runtime.handle(tuiServiceRequest{Action: "shutdown"})
			runtime.signalShutdown()
		case <-shutdown:
		}
		<-historyCollectorDone
		if err := runtime.persistHistory(true); err != nil {
			appendCLIApplicationLog(paths.HomeDir, "ERROR", "history_save", err.Error())
		}
		_ = listener.Close()
	}()
	defer func() {
		shutdownOnce.Do(func() { close(shutdown) })
		<-serviceCleanupDone
	}()

	var handlers sync.WaitGroup
	for {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			select {
			case <-shutdown:
				handlers.Wait()
				return nil
			default:
				return acceptErr
			}
		}
		handlers.Add(1)
		go func(connection net.Conn) {
			defer handlers.Done()
			serveTUIServiceConnection(runtime, connection)
		}(connection)
	}
}

func collectTUIServiceHistory(
	runtime *tuiServiceRuntime,
	shutdown <-chan struct{},
) {
	historyTicker := time.NewTicker(tuiHistoryCollectorInterval)
	persistTicker := time.NewTicker(historyPersistenceInterval())
	scavengeTicker := time.NewTicker(tuiCoreMemoryScavengeInterval)
	defer historyTicker.Stop()
	defer persistTicker.Stop()
	defer scavengeTicker.Stop()
	for {
		select {
		case <-historyTicker.C:
			_ = runtime.historyStatus("")
		case <-persistTicker.C:
			if err := runtime.persistHistory(false); err != nil {
				runtime.mu.RLock()
				homeDir := runtime.paths.HomeDir
				runtime.mu.RUnlock()
				appendCLIApplicationLog(homeDir, "ERROR", "history_save", err.Error())
			}
		case <-scavengeTicker.C:
			handleForceGC()
		case <-shutdown:
			return
		}
	}
}

func serveTUIServiceConnection(
	runtime *tuiServiceRuntime,
	connection net.Conn,
) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
	request, decodeErr := readTUIServiceRequest(connection)
	if decodeErr != nil {
		_ = json.NewEncoder(connection).Encode(tuiServiceStatus{
			OK:    false,
			Error: decodeErr.Error(),
		})
		return
	}
	if tuiServiceActionUsesReloadTimeout(request.Action) {
		_ = connection.SetDeadline(
			time.Now().Add(tuiServiceReloadTimeout + 5*time.Second),
		)
	} else if request.Action == "watch" {
		watchTimeout := time.Duration(request.WatchTimeoutMS) * time.Millisecond
		if watchTimeout <= 0 || watchTimeout > 30*time.Second {
			watchTimeout = 30 * time.Second
		}
		_ = connection.SetDeadline(time.Now().Add(watchTimeout + 2*time.Second))
	} else if request.Action == "speed_proxy" ||
		request.Action == "speed_route" ||
		request.Action == "delay_route" {
		_ = connection.SetDeadline(
			time.Now().Add(
				tuiSpeedConnectTimeout +
					tuiSpeedTestDuration +
					5*time.Second,
			),
		)
	}
	status := runtime.handle(request)
	encodeErr := json.NewEncoder(connection).Encode(status)
	if request.Action == "shutdown" && status.OK && encodeErr == nil {
		_ = connection.Close()
		runtime.signalShutdown()
	}
}

func readTUIServiceRequest(reader io.Reader) (tuiServiceRequest, error) {
	limited := &io.LimitedReader{
		R: reader,
		N: tuiServiceRequestMaxBytes + 1,
	}
	data, err := bufio.NewReader(limited).ReadBytes('\n')
	if int64(len(data)) > tuiServiceRequestMaxBytes || limited.N == 0 {
		return tuiServiceRequest{}, fmt.Errorf(
			"Backend request exceeds %d bytes",
			tuiServiceRequestMaxBytes,
		)
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return tuiServiceRequest{}, err
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return tuiServiceRequest{}, io.ErrUnexpectedEOF
	}
	var request tuiServiceRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return tuiServiceRequest{}, err
	}
	return request, nil
}

func reloadTUIServiceConfig(
	paths cliPaths,
	configPath,
	testURL,
	coreSocket string,
	previousSetup []byte,
	running bool,
) ([]byte, error) {
	configPath = filepath.Clean(configPath)
	if _, err := tuiProfileStateKey(paths.HomeDir, configPath); err != nil {
		return nil, err
	}
	return reloadTUIActualConfig(
		paths.HomeDir,
		paths.ConfigPath,
		configPath,
		testURL,
		coreSocket,
		previousSetup,
		running,
	)
}

func reloadTUIActualConfig(
	homeDir,
	previousPath,
	configPath,
	testURL,
	coreSocket string,
	previousSetup []byte,
	running bool,
) ([]byte, error) {
	previousPath = filepath.Clean(previousPath)
	configPath = filepath.Clean(configPath)
	if _, err := tuiProfileStateKey(homeDir, previousPath); err != nil {
		return nil, err
	}
	if _, err := tuiProfileStateKey(homeDir, configPath); err != nil {
		return nil, err
	}
	if err := ensureTUIBundledGeoData(homeDir); err != nil {
		return nil, fmt.Errorf("prepare offline Geo data: %w", err)
	}
	if message := handleValidateConfig(configPath); message != "" {
		return nil, errors.New(message)
	}
	rollback := func() error {
		initParams, marshalErr := json.Marshal(InitParams{
			HomeDir:    homeDir,
			ConfigPath: previousPath,
			Version:    1,
		})
		if marshalErr != nil || !handleInitClash(string(initParams)) {
			return errors.New("restore previous profile initialization failed")
		}
		if message := handleSetupConfig(previousSetup); message != "" {
			return errors.New("restore previous profile failed: " + message)
		}
		if running {
			handleStartListener()
		} else {
			handleStopListener()
		}
		return nil
	}
	initParams, err := json.Marshal(InitParams{
		HomeDir:    homeDir,
		ConfigPath: configPath,
		Version:    1,
	})
	if err != nil || !handleInitClash(string(initParams)) {
		return nil, errors.New("initialize updated profile failed")
	}
	setup := SetupParams{
		TestURL:                testURL,
		SelectedMap:            loadTUISelectedProxies(homeDir),
		ExternalControllerUnix: &coreSocket,
	}
	setupParams, err := json.Marshal(setup)
	if err != nil {
		_ = rollback()
		return nil, err
	}
	if message := handleSetupConfig(setupParams); message != "" {
		rollbackErr := rollback()
		if rollbackErr != nil {
			return nil, fmt.Errorf("%s; rollback failed: %w", message, rollbackErr)
		}
		return nil, errors.New(message)
	}
	if running {
		handleStartListener()
	} else {
		handleStopListener()
	}
	options := controllerOptions{unixSocket: coreSocket}
	controller := controllerClient{
		options: options,
		client: controllerHTTPClientForOptions(
			options,
			750*time.Millisecond,
		),
	}
	if err := waitForController(controller, 3*time.Second); err != nil {
		rollbackErr := rollback()
		if rollbackErr != nil {
			return nil, fmt.Errorf("%v; rollback failed: %w", err, rollbackErr)
		}
		return nil, err
	}
	_ = os.Chmod(coreSocket, 0o600)
	return setupParams, nil
}

func removeStaleTUIServiceSocket(homeDir, socketPath string) error {
	relative, err := filepath.Rel(filepath.Clean(homeDir), filepath.Clean(socketPath))
	if err != nil || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("service socket must stay inside the data directory")
	}
	info, err := os.Lstat(socketPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to replace non-socket path %q", socketPath)
	}
	return os.Remove(socketPath)
}

func stopCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash stop")
		fmt.Println("Stop Core listeners and the managed system proxy; keep the backend running.")
		return nil
	}
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	directoryArg := fs.String("directory", "", "FlClash data directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	paths, err := resolvePaths("", *directoryArg)
	if err != nil {
		return err
	}
	client := newTUIServiceClient(paths.HomeDir)
	status, err := client.status()
	if err != nil {
		return errors.New("no FlClash Backend is running")
	}
	if err := validateCurrentTUIService(status); err != nil {
		return err
	}
	status, err = client.stopAtRevision(status.Revision)
	if err != nil {
		return err
	}
	fmt.Printf("Core stopped; backend remains available (revision %d)\n", status.Revision)
	return nil
}
