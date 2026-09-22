//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"
)

const (
	tuiServiceProtocolVersion = 5
	tuiServiceSocketFilename  = ".flclash-cli-service.sock"
	tuiCoreSocketFilename     = ".flclash-cli-core.sock"
	tuiServiceLogFilename     = "flclash-cli-service.log"
	tuiServiceReloadTimeout   = 2 * time.Minute
	tuiServiceShutdownTimeout = 5 * time.Second
	tuiServiceLogMaxBytes     = 5 << 20
	// ProfileData is JSON base64, so a 32 MiB subscription needs more than
	// 42 MiB on the wire. Keep the IPC bounded while accepting the documented
	// profile limit plus request metadata.
	tuiServiceRequestMaxBytes = 48 << 20
)

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
	applyTUIServiceStatusCompatibility(&status)
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

func applyTUIServiceStatusCompatibility(status *tuiServiceStatus) {
	if status.ConfiguredProxyPort == 0 &&
		status.Mode != tuiSilentMode &&
		!status.FLCEnabled {
		status.ConfiguredProxyPort = status.ProxyPort
	}
	if status.ActiveProxyPort == 0 && status.Running {
		status.ActiveProxyPort = status.ProxyPort
	}
}

func tuiServiceActionUsesReloadTimeout(action string) bool {
	switch action {
	case "reload", "apply_settings", "set_mode", "set_flc_outbound",
		"put_profile", "restore_profile",
		"select_proxy", "start", "stop", "set_tun", "flc_proxy":
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
	return c.closeConnectionManagedForSource(id, tuiTrafficSourceMixed, revision)
}

func (c *tuiServiceClient) closeConnectionManagedForSource(
	id,
	source string,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "close_connection",
		ConnectionID:     id,
		Source:           normalizeTrafficSource(source),
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) closeAllConnectionsManaged(
	revision uint64,
) (tuiServiceStatus, error) {
	return c.closeAllConnectionsManagedForSource(tuiTrafficSourceMixed, revision)
}

func (c *tuiServiceClient) closeAllConnectionsManagedForSource(
	source string,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "close_all_connections",
		Source:           normalizeTrafficSource(source),
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

func (c *tuiServiceClient) logs(limit int) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{Action: "logs", LogLimit: limit})
}

func (c *tuiServiceClient) clearLogs(revision uint64) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "clear_logs",
		ExpectedRevision: &revision,
	})
}

func (c *tuiServiceClient) clearHistory(
	revision uint64,
) (tuiServiceStatus, error) {
	return c.clearHistoryForSource(tuiTrafficSourceMixed, revision)
}

func (c *tuiServiceClient) clearHistoryForSource(
	source string,
	revision uint64,
) (tuiServiceStatus, error) {
	return c.requestPayload(tuiServiceRequest{
		Action:           "clear_history",
		Source:           normalizeTrafficSource(source),
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

func (c *tuiServiceClient) shutdownAndWait(timeout time.Duration) error {
	status, err := c.compatibleStatus()
	if err != nil {
		return err
	}
	return c.shutdownPIDAndWait(status.PID, timeout)
}

func (c *tuiServiceClient) shutdownPIDAndWait(
	pid int,
	timeout time.Duration,
) error {
	if err := c.shutdown(); err != nil {
		return err
	}
	if waitForTUIServiceExit(c, pid, timeout) {
		return nil
	}
	return fmt.Errorf(
		"Backend PID %d did not exit within %s",
		pid,
		timeout,
	)
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
