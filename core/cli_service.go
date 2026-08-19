//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"crypto/rand"
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
	tuiServiceProtocolVersion = 4
	tuiServiceSocketFilename  = ".flclash-cli-service.sock"
	tuiCoreSocketFilename     = ".flclash-cli-core.sock"
	tuiServiceLogFilename     = "flclash-cli-service.log"
	tuiServiceReloadTimeout   = 2 * time.Minute
	// ProfileData is JSON base64, so a 32 MiB subscription needs more than
	// 42 MiB on the wire. Keep the IPC bounded while accepting the documented
	// profile limit plus request metadata.
	tuiServiceRequestMaxBytes = 48 << 20
)

type tuiServiceRequest struct {
	ProtocolVersion  int          `json:"protocol_version,omitempty"`
	RequestID        string       `json:"request_id,omitempty"`
	ExpectedRevision *uint64      `json:"expected_revision,omitempty"`
	Action           string       `json:"action"`
	ConfigPath       string       `json:"config_path,omitempty"`
	ProxyGroup       string       `json:"proxy_group,omitempty"`
	ProxyName        string       `json:"proxy_name,omitempty"`
	Mode             string       `json:"mode,omitempty"`
	MixedPort        int          `json:"mixed_port,omitempty"`
	TunScope         string       `json:"tun_scope,omitempty"`
	TestURL          string       `json:"test_url,omitempty"`
	Settings         *tuiSettings `json:"settings,omitempty"`
	Enabled          *bool        `json:"enabled,omitempty"`
	ExpectedSHA256   string       `json:"expected_sha256,omitempty"`
	ProfileData      []byte       `json:"profile_data,omitempty"`
	CreateOnly       bool         `json:"create_only,omitempty"`
	SubscriptionURL  *string      `json:"subscription_url,omitempty"`
	NewName          string       `json:"new_name,omitempty"`
	ConnectionID     string       `json:"connection_id,omitempty"`
	AfterRevision    uint64       `json:"after_revision,omitempty"`
	WatchTimeoutMS   int          `json:"watch_timeout_ms,omitempty"`
}

type tuiServiceStatus struct {
	ProtocolVersion     int             `json:"protocol_version,omitempty"`
	RequestID           string          `json:"request_id,omitempty"`
	Revision            uint64          `json:"revision,omitempty"`
	OK                  bool            `json:"ok"`
	ErrorCode           string          `json:"error_code,omitempty"`
	Error               string          `json:"error,omitempty"`
	PID                 int             `json:"pid"`
	Version             string          `json:"version"`
	HomeDir             string          `json:"home_dir"`
	ConfigPath          string          `json:"config_path"`
	CoreSocket          string          `json:"core_socket"`
	Running             bool            `json:"running"`
	ShuttingDown        bool            `json:"shutting_down,omitempty"`
	SystemProxy         bool            `json:"system_proxy"`
	Mode                string          `json:"mode,omitempty"`
	ProxyPort           int             `json:"proxy_port,omitempty"`
	ConfiguredProxyPort int             `json:"configured_proxy_port,omitempty"`
	ActiveProxyPort     int             `json:"active_proxy_port,omitempty"`
	TunScope            string          `json:"tun_scope,omitempty"`
	TunState            string          `json:"tun_state,omitempty"`
	TunOwnerUID         uint32          `json:"tun_owner_uid,omitempty"`
	TunOwnerPID         int             `json:"tun_owner_pid,omitempty"`
	FLCEnabled          bool            `json:"flc_enabled,omitempty"`
	FLCOutbound         string          `json:"flc_outbound,omitempty"`
	FLCProxyURL         string          `json:"flc_proxy_url,omitempty"`
	ResultPath          string          `json:"result_path,omitempty"`
	History             []tuiRequest    `json:"history,omitempty"`
	Connections         []tuiConnection `json:"connections,omitempty"`
	FrontendCount       int             `json:"frontend_count"`
	Delay               int             `json:"delay,omitempty"`
	DelayJitter         int             `json:"delay_jitter,omitempty"`
	DelayMin            int             `json:"delay_min,omitempty"`
	DelayMax            int             `json:"delay_max,omitempty"`
	DelaySamples        int             `json:"delay_samples,omitempty"`
	Speed               *tuiSpeedResult `json:"speed,omitempty"`
}

type tuiServiceError struct {
	Code     string
	Revision uint64
	Message  string
}

func (e *tuiServiceError) Error() string {
	return e.Message
}

type tuiServiceClient struct {
	homeDir       string
	timeout       time.Duration
	reloadTimeout time.Duration
}

func newTUIServiceClient(homeDir string) *tuiServiceClient {
	runtimeDirectory, err := cliRuntimeDirectory()
	if err == nil {
		homeDir = runtimeDirectory
	}
	return newTUIServiceClientAt(homeDir)
}

func newTUIServiceClientAt(socketDirectory string) *tuiServiceClient {
	return &tuiServiceClient{
		homeDir:       socketDirectory,
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
	if request.ProtocolVersion == 0 {
		request.ProtocolVersion = tuiServiceProtocolVersion
	}
	return c.sendRequest(request)
}

func (c *tuiServiceClient) requestPayloadUnversioned(
	request tuiServiceRequest,
) (tuiServiceStatus, error) {
	request.ProtocolVersion = 0
	return c.sendRequest(request)
}

func (c *tuiServiceClient) sendRequest(
	request tuiServiceRequest,
) (tuiServiceStatus, error) {
	if request.RequestID == "" {
		request.RequestID = newTUIServiceRequestID()
	}
	requestTimeout := c.timeout
	if tuiServiceActionUsesReloadTimeout(request.Action) {
		requestTimeout = c.reloadTimeout
	}
	switch request.Action {
	case "watch":
		requestTimeout = time.Duration(request.WatchTimeoutMS)*time.Millisecond + 2*time.Second
		if requestTimeout < c.timeout {
			requestTimeout = c.timeout
		}
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
	if status.ConfiguredProxyPort == 0 {
		status.ConfiguredProxyPort = status.ProxyPort
	}
	if status.ActiveProxyPort == 0 && status.Running {
		status.ActiveProxyPort = status.ProxyPort
	}
	if !status.OK {
		if status.Error == "" {
			status.Error = "Backend rejected the request"
		}
		return status, &tuiServiceError{
			Code:     status.ErrorCode,
			Revision: status.Revision,
			Message:  status.Error,
		}
	}
	return status, nil
}

func tuiServiceActionUsesReloadTimeout(action string) bool {
	switch action {
	case "reload", "apply_settings", "set_mode", "set_flc_outbound",
		"put_profile", "restore_profile":
		return true
	default:
		return false
	}
}

func newTUIServiceRequestID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err == nil {
		return fmt.Sprintf("%x", value[:])
	}
	return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
}

func (c *tuiServiceClient) status() (tuiServiceStatus, error) {
	return c.request("status", "")
}

func (c *tuiServiceClient) compatibleStatus() (tuiServiceStatus, error) {
	status, err := c.status()
	if !isUnsupportedTUIServiceProtocol(err) {
		return status, err
	}
	return c.requestPayloadUnversioned(tuiServiceRequest{Action: "status"})
}

func isUnsupportedTUIServiceProtocol(err error) bool {
	var serviceErr *tuiServiceError
	return errors.As(err, &serviceErr) &&
		serviceErr.Code == tuiServiceErrorUnsupported
}

func (c *tuiServiceClient) start() (tuiServiceStatus, error) {
	return c.request("start", "")
}

func (c *tuiServiceClient) startAtRevision(
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "start",
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) stop() (tuiServiceStatus, error) {
	return c.request("stop", "")
}

func (c *tuiServiceClient) stopAtRevision(
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "stop",
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) reload(configPath string) (tuiServiceStatus, error) {
	return c.request("reload", configPath)
}

func (c *tuiServiceClient) reloadAtRevision(
	configPath string,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "reload",
		ConfigPath:       configPath,
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) reloadAtRevisionWithDigest(
	configPath string,
	revision uint64,
	expectedSHA256 string,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "reload",
		ConfigPath:       configPath,
		ExpectedRevision: &revision,
		ExpectedSHA256:   expectedSHA256,
	})
}

func (c *tuiServiceClient) putProfile(
	path string,
	data []byte,
	expectedSHA256 string,
	createOnly bool,
	subscriptionURL *string,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "put_profile",
		ConfigPath:       path,
		ProfileData:      data,
		ExpectedSHA256:   expectedSHA256,
		CreateOnly:       createOnly,
		SubscriptionURL:  subscriptionURL,
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) renameProfile(
	path,
	newName string,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "rename_profile",
		ConfigPath:       path,
		NewName:          newName,
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) deleteProfile(
	path string,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "delete_profile",
		ConfigPath:       path,
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) linkProfile(
	path,
	subscriptionURL string,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "link_profile",
		ConfigPath:       path,
		SubscriptionURL:  &subscriptionURL,
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) backupProfile(
	path string,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "backup_profile",
		ConfigPath:       path,
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) restoreProfile(
	path string,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "restore_profile",
		ConfigPath:       path,
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) applySettings(
	settings tuiSettings,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "apply_settings",
		Settings:         &settings,
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) setSystemProxy(
	enabled bool,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "set_system_proxy",
		Enabled:          &enabled,
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) setTun(
	enabled bool,
	scope string,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "set_tun",
		Enabled:          &enabled,
		TunScope:         scope,
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) connections() (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{Action: "connections"})
}

func (c *tuiServiceClient) closeConnectionManaged(
	id string,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "close_connection",
		ConnectionID:     id,
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) closeAllConnectionsManaged(
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "close_all_connections",
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) setMode(
	mode string,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "set_mode",
		Mode:             mode,
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) setFLCOutbound(
	outbound string,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "set_flc_outbound",
		ProxyName:        outbound,
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) selectProxy(
	group,
	proxy string,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "select_proxy",
		ProxyGroup:       group,
		ProxyName:        proxy,
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) flcProxy() (tuiServiceStatus, error) {
	return c.request("flc_proxy", "")
}

func (c *tuiServiceClient) history() (tuiServiceStatus, error) {
	return c.request("history", "")
}

func (c *tuiServiceClient) clearHistory(
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "clear_history",
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) watch(
	afterRevision uint64,
	timeout time.Duration,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:         "watch",
		AfterRevision:  afterRevision,
		WatchTimeoutMS: int(timeout / time.Millisecond),
	})
}

func (c *tuiServiceClient) shutdown() error {
	_, err := c.request("shutdown", "")
	if isUnsupportedTUIServiceProtocol(err) {
		_, err = c.requestPayloadUnversioned(tuiServiceRequest{
			Action: "shutdown",
		})
	}
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
		return tuiSpeedResult{}, errors.New("Backend returned no speed result")
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
		return tuiSpeedResult{}, errors.New("Backend returned no speed result")
	}
	return *status.Speed, nil
}

func (c *tuiServiceClient) testRouteDelay(
	mixedPort int,
	testURL string,
) (tuiDelayResult, error) {
	status, err := c.requestPayload(tuiServiceRequest{
		Action:    "delay_route",
		MixedPort: mixedPort,
		TestURL:   testURL,
	})
	if err != nil {
		return tuiDelayResult{}, err
	}
	if status.Delay <= 0 {
		return tuiDelayResult{}, errors.New("Backend returned no delay result")
	}
	return tuiDelayResult{
		MedianMillis: status.Delay,
		JitterMillis: status.DelayJitter,
		MinMillis:    status.DelayMin,
		MaxMillis:    status.DelayMax,
		Samples:      status.DelaySamples,
	}, nil
}

func ensureTUIService(
	paths cliPaths,
	testURL string,
	explicitConfig bool,
	explicitDirectory bool,
) (*tuiServiceClient, tuiServiceStatus, error) {
	client := newTUIServiceClient(paths.homeDir)
	if status, err := client.compatibleStatus(); err == nil {
		if status.Version == cliVersion &&
			status.ProtocolVersion == tuiServiceProtocolVersion {
			if err := validateTUIServiceTarget(
				paths,
				status,
				explicitConfig,
				explicitDirectory,
			); err != nil {
				return nil, tuiServiceStatus{}, err
			}
			return client, status, nil
		}
		if err := validateTUIServiceUpgradeCandidate(status); err != nil {
			return nil, tuiServiceStatus{}, err
		}
		wasRunning := status.Running
		if status.HomeDir != "" {
			paths.homeDir = status.HomeDir
		}
		if _, pathErr := tuiProfileStateKey(
			paths.homeDir,
			status.ConfigPath,
		); pathErr == nil {
			paths.configPath = status.ConfigPath
		}
		if err := client.shutdown(); err != nil {
			return nil, tuiServiceStatus{}, fmt.Errorf(
				"stop outdated Backend %s: %w",
				status.Version,
				err,
			)
		}
		waitForTUIServiceExit(client, status.PID, 3*time.Second)
		if err := spawnTUIService(paths, testURL, !explicitConfig); err != nil {
			return waitForCompetingTUIService(
				client,
				paths,
				explicitConfig,
				explicitDirectory,
				err,
			)
		}
		status, err = waitForTUIService(client, 30*time.Second)
		if err != nil {
			return nil, tuiServiceStatus{}, err
		}
		if wasRunning {
			status, err = client.startAtRevision(status.Revision)
			if err != nil {
				return nil, tuiServiceStatus{}, fmt.Errorf(
					"restart listeners after Backend upgrade: %w",
					err,
				)
			}
		}
		return client, status, nil
	}
	if legacyClient, legacyStatus, found := findLegacyTUIService(
		paths,
	); found {
		if legacyStatus.HomeDir == "" {
			legacyStatus.HomeDir = filepath.Dir(
				legacyStatus.ConfigPath,
			)
		}
		if err := validateTUIServiceTarget(
			paths,
			legacyStatus,
			explicitConfig,
			explicitDirectory,
		); err != nil {
			return nil, tuiServiceStatus{}, err
		}
		wasRunning := legacyStatus.Running
		if legacyStatus.ConfigPath != "" {
			paths.configPath = legacyStatus.ConfigPath
		}
		if legacyStatus.HomeDir != "" {
			paths.homeDir = legacyStatus.HomeDir
		}
		if err := legacyClient.shutdown(); err != nil {
			return nil, tuiServiceStatus{}, fmt.Errorf(
				"stop legacy background service: %w",
				err,
			)
		}
		waitForTUIServiceExit(
			legacyClient,
			legacyStatus.PID,
			3*time.Second,
		)
		if err := spawnTUIService(paths, testURL, !explicitConfig); err != nil {
			return waitForCompetingTUIService(
				client,
				paths,
				explicitConfig,
				explicitDirectory,
				err,
			)
		}
		status, err := waitForTUIService(client, 30*time.Second)
		if err != nil {
			return nil, tuiServiceStatus{}, err
		}
		if wasRunning {
			status, err = client.startAtRevision(status.Revision)
			if err != nil {
				return nil, tuiServiceStatus{}, fmt.Errorf(
					"restart listeners after Backend migration: %w",
					err,
				)
			}
		}
		return client, status, nil
	}
	if err := spawnTUIService(paths, testURL, !explicitConfig); err != nil {
		return waitForCompetingTUIService(
			client,
			paths,
			explicitConfig,
			explicitDirectory,
			err,
		)
	}
	status, err := waitForTUIService(client, 30*time.Second)
	if err != nil {
		return nil, tuiServiceStatus{}, err
	}
	if err := validateTUIServiceTarget(
		paths,
		status,
		explicitConfig,
		explicitDirectory,
	); err != nil {
		return nil, tuiServiceStatus{}, err
	}
	return client, status, nil
}

func validateTUIServiceUpgradeCandidate(status tuiServiceStatus) error {
	if status.ProtocolVersion > tuiServiceProtocolVersion ||
		isNewerCLIVersion(status.Version, cliVersion) {
		return fmt.Errorf(
			"backend %s uses protocol %d, newer than this client %s protocol %d; update flclash instead of replacing the backend",
			status.Version,
			status.ProtocolVersion,
			cliVersion,
			tuiServiceProtocolVersion,
		)
	}
	return nil
}

func findLegacyTUIService(
	paths cliPaths,
) (*tuiServiceClient, tuiServiceStatus, bool) {
	runtimeSocket, _ := cliServiceSocketPath()
	directories := []string{paths.homeDir}
	if configRoot, err := os.UserConfigDir(); err == nil {
		directories = append(
			directories,
			filepath.Join(configRoot, "flclash"),
		)
	}
	seen := map[string]bool{}
	for _, directory := range directories {
		directory = filepath.Clean(directory)
		socketPath := filepath.Join(
			directory,
			tuiServiceSocketFilename,
		)
		if seen[directory] ||
			filepath.Clean(socketPath) == filepath.Clean(runtimeSocket) {
			continue
		}
		seen[directory] = true
		client := newTUIServiceClientAt(directory)
		status, err := client.compatibleStatus()
		if err == nil {
			return client, status, true
		}
	}
	return nil, tuiServiceStatus{}, false
}

func validateTUIServiceTarget(
	paths cliPaths,
	status tuiServiceStatus,
	explicitConfig bool,
	explicitDirectory bool,
) error {
	if !explicitConfig && !explicitDirectory {
		return nil
	}
	sameHome := status.HomeDir == "" ||
		filepath.Clean(status.HomeDir) == filepath.Clean(paths.homeDir)
	sameConfig := status.ConfigPath == "" ||
		filepath.Clean(status.ConfigPath) == filepath.Clean(paths.configPath)
	if sameHome && (!explicitConfig || sameConfig) {
		return nil
	}
	return fmt.Errorf(
		"the per-user FlClash backend is already using %q; "+
			"stop it before opening explicit config %q",
		status.ConfigPath,
		paths.configPath,
	)
}

func waitForCompetingTUIService(
	client *tuiServiceClient,
	paths cliPaths,
	explicitConfig bool,
	explicitDirectory bool,
	spawnErr error,
) (*tuiServiceClient, tuiServiceStatus, error) {
	var busyErr *cliLockBusyError
	if !errors.As(spawnErr, &busyErr) ||
		(busyErr.owner.Kind != "service" &&
			busyErr.owner.Kind != "service-starting") {
		return nil, tuiServiceStatus{}, spawnErr
	}
	status, err := waitForTUIService(client, 30*time.Second)
	if err != nil {
		return nil, tuiServiceStatus{}, spawnErr
	}
	if err := validateTUIServiceTarget(
		paths,
		status,
		explicitConfig,
		explicitDirectory,
	); err != nil {
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
		"Backend did not become ready: %w",
		lastErr,
	)
}

func waitForTUIServiceExit(
	client *tuiServiceClient,
	pid int,
	timeout time.Duration,
) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, statusErr := client.compatibleStatus()
		if statusErr != nil && !cliProcessRunning(pid) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func cliProcessRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

func spawnTUIService(paths cliPaths, testURL string, allowCreate bool) error {
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
	backendLock, err := acquireCLIBackendLock(cliProcessOwner{
		Kind:       "service-starting",
		HomeDir:    paths.homeDir,
		ConfigPath: paths.configPath,
	})
	if err != nil {
		_ = logFile.Close()
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
		"--create-config="+strconv.FormatBool(allowCreate),
		"--lock-fd",
		"3",
	)
	command.Stdin = nil
	command.Stdout = logFile
	command.Stderr = logFile
	command.ExtraFiles = []*os.File{backendLock.file}
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		backendLock.release()
		_ = logFile.Close()
		return err
	}
	backendLock.closeTransferredCopy()
	_ = logFile.Close()
	reapTUIServiceProcess(command)
	return nil
}

func reapTUIServiceProcess(command *exec.Cmd) <-chan error {
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
		close(done)
	}()
	return done
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
				HomeDir:    paths.homeDir,
				ConfigPath: paths.configPath,
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
	if err := os.MkdirAll(paths.homeDir, 0o700); err != nil {
		return err
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
	coreSocket := filepath.Join(paths.homeDir, tuiCoreSocketFilename)
	if backendLock == nil {
		backendLock, err = acquireCLIBackendLock(cliProcessOwner{
			Kind:       "service",
			HomeDir:    paths.homeDir,
			ConfigPath: paths.configPath,
		})
		if err != nil {
			return err
		}
	} else if err := backendLock.setOwner(cliProcessOwner{
		Kind:       "service",
		HomeDir:    paths.homeDir,
		ConfigPath: paths.configPath,
	}); err != nil {
		backendLock.release()
		return err
	}
	defer backendLock.release()
	if err := ensureTUIFlClashDefaults(paths.configPath); err != nil {
		return fmt.Errorf("apply FlClash defaults: %w", err)
	}
	if err := removeStaleTUIServiceSocket(
		runtimeDirectory,
		managerSocket,
	); err != nil {
		return err
	}
	if err := removeStaleTUIServiceSocket(
		paths.homeDir,
		coreSocket,
	); err != nil {
		return err
	}
	configuredSettings := loadTUIConfiguredSettings(paths.configPath, true)
	configuredPort := 0
	if configuredSettings != nil {
		configuredPort = configuredSettings.MixedPort
	}
	trafficMode := loadTUITrafficMode(paths.homeDir, paths.configPath)
	tunScope := loadTUITunScope(paths.homeDir)
	tunEnabled := configuredSettings != nil && configuredSettings.TunEnabled && tunScope == tuiTunScopeUser
	actualPaths := paths
	runtimePort := configuredPort
	flcState := tuiFLCListenerState{Outbound: loadTUIFLCOutbound(paths.homeDir)}
	cleanupTUISilentRuntimeConfigs(paths.homeDir, "")
	if trafficMode == tuiSilentMode {
		tunEnabled = false
		if flcState.Outbound != "" {
			flcState, err = newTUIFLCListenerState(flcState.Outbound)
			if err != nil {
				return err
			}
		}
		actualPaths.configPath, err = writeTUISilentRuntimeConfig(paths, flcState)
		if err != nil {
			return err
		}
		defer cleanupTUISilentRuntimeConfigs(paths.homeDir, "")
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
		actualPaths.configPath, err = writeTUIManagedRuntimeConfig(
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
		defer cleanupTUISilentRuntimeConfigs(paths.homeDir, "")
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
		actualPaths.configPath,
		flcState,
		tunScope,
		tunEnabled,
	)
	go collectTUIServiceHistory(runtime, shutdown)
	if settings := loadTUIConfiguredSettings(paths.configPath, true); settings != nil {
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
	go func() {
		select {
		case <-interrupt:
			runtime.handle(tuiServiceRequest{Action: "shutdown"})
			runtime.signalShutdown()
		case <-shutdown:
		}
		_ = listener.Close()
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
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = runtime.historyStatus("")
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
	var request tuiServiceRequest
	limited := io.LimitReader(connection, tuiServiceRequestMaxBytes+1)
	if decodeErr := json.NewDecoder(bufio.NewReader(limited)).Decode(&request); decodeErr != nil {
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
	return reloadTUIActualConfig(
		paths.homeDir,
		paths.configPath,
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
	client := newTUIServiceClient(paths.homeDir)
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
