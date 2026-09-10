//go:build linux && !cgo && cli

package hubapi

type Core interface {
	Init(paramsJSON string) bool
	SetupConfig(data []byte) string
	ValidateConfig(path string) string
	StartListener() bool
	StopListener() bool
	Shutdown() bool
	StartLog()
	StopLog()
	ForceGC()
	ResetTraffic()
	UpdateConfig(data []byte) string
}
