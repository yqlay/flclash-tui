//go:build linux && !cgo && cli

package main

import "core/internal/hubapi"

type cliHubCore struct{}

func (cliHubCore) Init(paramsJSON string) bool { return handleInitClash(paramsJSON) }

func (cliHubCore) SetupConfig(data []byte) string { return handleSetupConfig(data) }

func (cliHubCore) ValidateConfig(path string) string { return handleValidateConfig(path) }

func (cliHubCore) StartListener() bool { return handleStartListener() }

func (cliHubCore) StopListener() bool { return handleStopListener() }

func (cliHubCore) Shutdown() bool { return handleShutdown() }

func (cliHubCore) StartLog() { handleStartLog() }

func (cliHubCore) StopLog() { handleStopLog() }

func (cliHubCore) ForceGC() { handleForceGC() }

func (cliHubCore) ResetTraffic() { handleResetTraffic() }

func (cliHubCore) UpdateConfig(data []byte) string { return handleUpdateConfig(data) }

var cliHub hubapi.Core = cliHubCore{}
