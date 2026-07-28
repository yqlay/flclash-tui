//go:build linux && !cgo && cli

package main

// The desktop build sends these events over its IPC transport. The CLI keeps
// the same core callbacks alive, but its foreground log is emitted by
// Mihomo's logger directly, so no second transport is required here.
func (result ActionResult) send() {}

func sendMessage(message Message) {}

func nextHandle(action *Action, result ActionResult) bool {
	return false
}
