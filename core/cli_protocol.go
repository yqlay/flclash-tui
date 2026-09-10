//go:build linux && !cgo && cli

package main

import "core/internal/protocol"

type tuiConnection = protocol.TuiConnection
type tuiRequest = protocol.TuiRequest
type tuiSettings = protocol.TuiSettings
type tuiSpeedResult = protocol.TuiSpeedResult
type tuiServiceRequest = protocol.TuiServiceRequest
type tuiServiceStatus = protocol.TuiServiceStatus
type tuiServiceError = protocol.TuiServiceError
