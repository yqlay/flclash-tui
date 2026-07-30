//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	tuiServiceSocketFilename = ".flclash-cli-service.sock"
	tuiCoreSocketFilename    = ".flclash-cli-core.sock"
	tuiServiceLogFilename    = "flclash-cli-service.log"
	tuiServiceReloadTimeout  = 2 * time.Minute
)

type tuiServiceRequest struct {
	Action     string `json:"action"`
	ConfigPath string `json:"config_path,omitempty"`
	ProxyName  string `json:"proxy_name,omitempty"`
	MixedPort  int    `json:"mixed_port,omitempty"`
	TestURL    string `json:"test_url,omitempty"`
}

type tuiServiceStatus struct {
	OK         bool            `json:"ok"`
	Error      string          `json:"error,omitempty"`
	PID        int             `json:"pid"`
	Version    string          `json:"version"`
	ConfigPath string          `json:"config_path"`
	CoreSocket string          `json:"core_socket"`
	Running    bool            `json:"running"`
	Delay      int             `json:"delay,omitempty"`
	Speed      *tuiSpeedResult `json:"speed,omitempty"`
}

type tuiServiceClient struct {
	homeDir       string
	timeout       time.Duration
	reloadTimeout time.Duration
}

func newTUIServiceClient(homeDir string) *tuiServiceClient {
	return &tuiServiceClient{
		homeDir:       homeDir,
		timeout:       3 * time.Second,
		reloadTimeout: tuiServiceReloadTimeout,
	}
}

func (c *tuiServiceClient) socketPath() string {
	return filepath.Join(c.homeDir, tuiServiceSocketFilename)
}

func (c *tuiServiceClient) request(action, configPath string) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:     action,
		ConfigPath: configPath,
	})
}

func (c *tuiServiceClient) requestPayload(
	request tuiServiceRequest,
) (tuiServiceStatus, error) {
	requestTimeout := c.timeout
	switch request.Action {
	case "reload":
		requestTimeout = c.reloadTimeout
	case "speed_proxy", "speed_route":
		requestTimeout = tuiSpeedConnectTimeout + tuiSpeedTestDuration + 2*time.Second
	case "delay_route":
		requestTimeout = tuiSpeedConnectTimeout + 2*time.Second
	}
	connection, err := net.DialTimeout("unix", c.socketPath(), requestTimeout)
	if err != nil {
		return tuiServiceStatus{}, err
	}
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(requestTimeout))
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		return tuiServiceStatus{}, err
	}
	var status tuiServiceStatus
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&status); err != nil {
		return tuiServiceStatus{}, err
	}
	if !status.OK {
		if status.Error == "" {
			status.Error = "background service rejected the request"
		}
		return status, errors.New(status.Error)
	}
	return status, nil
}

func (c *tuiServiceClient) status() (tuiServiceStatus, error) {
	return c.request("status", "")
}

func (c *tuiServiceClient) start() (tuiServiceStatus, error) {
	return c.request("start", "")
}

func (c *tuiServiceClient) stop() (tuiServiceStatus, error) {
	return c.request("stop", "")
}

func (c *tuiServiceClient) reload(configPath string) (tuiServiceStatus, error) {
	return c.request("reload", configPath)
}

func (c *tuiServiceClient) shutdown() error {
	_, err := c.request("shutdown", "")
	return err
}

func (c *tuiServiceClient) testProxySpeed(
	proxyName string,
) (tuiSpeedResult, error) {
	status, err := c.requestPayload(tuiServiceRequest{
		Action:    "speed_proxy",
		ProxyName: proxyName,
	})
	if err != nil {
		return tuiSpeedResult{}, err
	}
	if status.Speed == nil {
		return tuiSpeedResult{}, errors.New("background service returned no speed result")
	}
	return *status.Speed, nil
}

func (c *tuiServiceClient) testRouteSpeed(
	mixedPort int,
) (tuiSpeedResult, error) {
	status, err := c.requestPayload(tuiServiceRequest{
		Action:    "speed_route",
		MixedPort: mixedPort,
	})
	if err != nil {
		return tuiSpeedResult{}, err
	}
	if status.Speed == nil {
		return tuiSpeedResult{}, errors.New("background service returned no speed result")
	}
	return *status.Speed, nil
}

func (c *tuiServiceClient) testRouteDelay(
	mixedPort int,
	testURL string,
) (int, error) {
	status, err := c.requestPayload(tuiServiceRequest{
		Action:    "delay_route",
		MixedPort: mixedPort,
		TestURL:   testURL,
	})
	if err != nil {
		return 0, err
	}
	if status.Delay <= 0 {
		return 0, errors.New("background service returned no delay result")
	}
	return status.Delay, nil
}

func ensureTUIService(
	paths cliPaths,
	testURL string,
) (*tuiServiceClient, tuiServiceStatus, error) {
	client := newTUIServiceClient(paths.homeDir)
	if status, err := client.status(); err == nil {
		if status.Version == cliVersion {
			return client, status, nil
		}
		wasRunning := status.Running
		if _, pathErr := tuiProfileStateKey(
			paths.homeDir,
			status.ConfigPath,
		); pathErr == nil {
			paths.configPath = status.ConfigPath
		}
		if err := client.shutdown(); err != nil {
			return nil, tuiServiceStatus{}, fmt.Errorf(
				"stop outdated background service %s: %w",
				status.Version,
				err,
			)
		}
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			if _, statusErr := client.status(); statusErr != nil {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if err := spawnTUIService(paths, testURL); err != nil {
			return nil, tuiServiceStatus{}, err
		}
		status, err = waitForTUIService(client, 30*time.Second)
		if err != nil {
			return nil, tuiServiceStatus{}, err
		}
		if wasRunning {
			status, err = client.start()
			if err != nil {
				return nil, tuiServiceStatus{}, fmt.Errorf(
					"restart listeners after service upgrade: %w",
					err,
				)
			}
		}
		return client, status, nil
	}
	if err := spawnTUIService(paths, testURL); err != nil {
		return nil, tuiServiceStatus{}, err
	}
	status, err := waitForTUIService(client, 30*time.Second)
	if err != nil {
		return nil, tuiServiceStatus{}, err
	}
	return client, status, nil
}

func waitForTUIService(
	client *tuiServiceClient,
	timeout time.Duration,
) (tuiServiceStatus, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		status, err := client.status()
		if err == nil {
			return status, nil
		}
		lastErr = err
		time.Sleep(50 * time.Millisecond)
	}
	return tuiServiceStatus{}, fmt.Errorf(
		"background service did not become ready: %w",
		lastErr,
	)
}

func spawnTUIService(paths cliPaths, testURL string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(paths.homeDir, 0o700); err != nil {
		return err
	}
	logPath := filepath.Join(paths.homeDir, tuiServiceLogFilename)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	command := exec.Command(
		executable,
		"_service",
		"--directory",
		paths.homeDir,
		"--config",
		paths.configPath,
		"--test-url",
		testURL,
	)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	_ = logFile.Close()
	return command.Process.Release()
}

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
	if err := fs.Parse(args); err != nil {
		return err
	}
	paths, err := resolvePaths(*configArg, *directoryArg)
	if err != nil {
		return err
	}
	return runTUIService(paths, *testURLArg)
}

func runTUIService(paths cliPaths, testURL string) error {
	if err := os.MkdirAll(paths.homeDir, 0o700); err != nil {
		return err
	}
	managerSocket := filepath.Join(paths.homeDir, tuiServiceSocketFilename)
	coreSocket := filepath.Join(paths.homeDir, tuiCoreSocketFilename)
	if existing := newTUIServiceClient(paths.homeDir); serviceIsAvailable(existing) {
		return errors.New("background service is already running")
	}
	for _, socketPath := range []string{managerSocket, coreSocket} {
		if err := removeStaleTUIServiceSocket(paths.homeDir, socketPath); err != nil {
			return err
		}
	}
	setupParams, err := initializeCore(paths, testURL, "", coreSocket, "", false)
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

	var lock sync.Mutex
	running := false
	shutdown := make(chan struct{})
	var shutdownOnce sync.Once
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(interrupt)
	go func() {
		select {
		case <-interrupt:
			shutdownOnce.Do(func() { close(shutdown) })
		case <-shutdown:
		}
		_ = listener.Close()
	}()

	for {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			select {
			case <-shutdown:
				return nil
			default:
				return acceptErr
			}
		}
		go func(connection net.Conn) {
			defer connection.Close()
			_ = connection.SetDeadline(time.Now().Add(10 * time.Second))
			var request tuiServiceRequest
			if decodeErr := json.NewDecoder(bufio.NewReader(connection)).Decode(&request); decodeErr != nil {
				_ = json.NewEncoder(connection).Encode(tuiServiceStatus{
					OK:    false,
					Error: decodeErr.Error(),
				})
				return
			}
			if request.Action == "reload" {
				_ = connection.SetDeadline(
					time.Now().Add(tuiServiceReloadTimeout + 5*time.Second),
				)
			} else if request.Action == "speed_proxy" ||
				request.Action == "speed_route" {
				_ = connection.SetDeadline(
					time.Now().Add(
						tuiSpeedConnectTimeout +
							tuiSpeedTestDuration +
							5*time.Second,
					),
				)
			}
			lock.Lock()
			defer lock.Unlock()
			status := tuiServiceStatus{
				OK:         true,
				PID:        os.Getpid(),
				Version:    cliVersion,
				ConfigPath: paths.configPath,
				CoreSocket: coreSocket,
				Running:    running,
			}
			switch request.Action {
			case "status":
			case "start":
				if !running && !handleStartListener() {
					status.OK = false
					status.Error = "start proxy listeners failed"
				} else {
					running = true
				}
			case "stop":
				if running && !handleStopListener() {
					status.OK = false
					status.Error = "stop proxy listeners failed"
				} else {
					running = false
				}
			case "reload":
				reloadPath := request.ConfigPath
				if reloadPath == "" {
					reloadPath = paths.configPath
				}
				reloadedParams, reloadErr := reloadTUIServiceConfig(
					paths,
					reloadPath,
					testURL,
					coreSocket,
					setupParams,
					running,
				)
				if reloadErr != nil {
					status.OK = false
					status.Error = reloadErr.Error()
				} else {
					paths.configPath = reloadPath
					setupParams = reloadedParams
				}
			case "speed_proxy":
				client, closeClient, clientErr := newTUIProxyNodeHTTPClient(
					request.ProxyName,
				)
				if clientErr != nil {
					status.OK = false
					status.Error = clientErr.Error()
					break
				}
				result, speedErr := runTUIDownloadSpeedTest(
					context.Background(),
					client,
				)
				closeClient()
				if speedErr != nil {
					status.OK = false
					status.Error = speedErr.Error()
				} else {
					status.Speed = &result
				}
			case "speed_route", "delay_route":
				wasRunning := running
				if !running {
					reloadedParams, reloadErr := reloadTUIServiceConfig(
						paths,
						paths.configPath,
						testURL,
						coreSocket,
						setupParams,
						false,
					)
					if reloadErr != nil {
						status.OK = false
						status.Error = "prepare stopped Service for test: " +
							reloadErr.Error()
						break
					}
					setupParams = reloadedParams
					if !handleStartListener() {
						status.OK = false
						status.Error = "start proxy listeners for test failed"
						break
					}
				}
				running = true
				client, closeClient, clientErr := newTUIRouteHTTPClient(
					request.MixedPort,
				)
				if clientErr == nil {
					if request.Action == "speed_route" {
						result, speedErr := runTUIDownloadSpeedTest(
							context.Background(),
							client,
						)
						if speedErr != nil {
							clientErr = speedErr
						} else {
							status.Speed = &result
						}
					} else {
						delay, delayErr := runTUIRouteDelayTest(
							context.Background(),
							client,
							request.TestURL,
						)
						if delayErr != nil {
							clientErr = delayErr
						} else {
							status.Delay = delay
						}
					}
					closeClient()
				}
				if !wasRunning {
					if !handleStopListener() && clientErr == nil {
						clientErr = errors.New(
							"restore stopped Service state failed",
						)
					}
					running = false
				}
				if clientErr != nil {
					status.OK = false
					status.Error = clientErr.Error()
				}
			case "shutdown":
				running = false
				status.Running = false
				shutdownOnce.Do(func() { close(shutdown) })
			default:
				status.OK = false
				status.Error = "unknown service action " + strconv.Quote(request.Action)
			}
			status.ConfigPath = paths.configPath
			status.Running = running
			_ = json.NewEncoder(connection).Encode(status)
		}(connection)
	}
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
	if _, err := tuiProfileStateKey(paths.homeDir, configPath); err != nil {
		return nil, err
	}
	if err := ensureTUIBundledGeoData(paths.homeDir); err != nil {
		return nil, fmt.Errorf("prepare offline Geo data: %w", err)
	}
	if message := handleValidateConfig(configPath); message != "" {
		return nil, errors.New(message)
	}
	previousPath := paths.configPath
	rollback := func() error {
		initParams, marshalErr := json.Marshal(InitParams{
			HomeDir:    paths.homeDir,
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
		HomeDir:    paths.homeDir,
		ConfigPath: configPath,
		Version:    1,
	})
	if err != nil || !handleInitClash(string(initParams)) {
		return nil, errors.New("initialize updated profile failed")
	}
	setup := SetupParams{
		TestURL:                testURL,
		SelectedMap:            loadTUISelectedProxies(paths.homeDir),
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

func serviceIsAvailable(client *tuiServiceClient) bool {
	_, err := client.status()
	return err == nil
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
	client := newTUIServiceClient(paths.homeDir)
	status, err := client.status()
	if err != nil {
		return errors.New("no flclash background service is running")
	}
	settings := loadTUIConfiguredSettings(status.ConfigPath, true)
	if settings != nil && linuxSystemProxyMatches(settings.MixedPort) {
		if err := setLinuxSystemProxy(settings.MixedPort, false); err != nil {
			return fmt.Errorf("disable system proxy: %w", err)
		}
	}
	if err := client.shutdown(); err != nil {
		return err
	}
	fmt.Println("FlClash TUI background service stopped")
	return nil
}
