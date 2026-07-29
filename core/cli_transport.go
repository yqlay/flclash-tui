//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"sync"
)

var (
	cliLogMu sync.Mutex
	cliLogs  []string
)

// The desktop build sends these events over its IPC transport. The CLI keeps
// the same core callbacks alive, but its foreground log is emitted by
// Mihomo's logger directly, so no second transport is required here.
func (result ActionResult) send() {}

func sendMessage(message Message) {
	if message.Type != LogMessage {
		return
	}
	data, err := json.Marshal(message.Data)
	if err != nil {
		return
	}
	var event struct {
		Payload string `json:"Payload"`
	}
	if json.Unmarshal(data, &event) != nil || event.Payload == "" {
		return
	}
	cliLogMu.Lock()
	defer cliLogMu.Unlock()
	cliLogs = append(cliLogs, event.Payload)
	if len(cliLogs) > 200 {
		cliLogs = cliLogs[len(cliLogs)-200:]
	}
}

func cliLogSnapshot() []string {
	cliLogMu.Lock()
	defer cliLogMu.Unlock()
	logs := make([]string, len(cliLogs))
	copy(logs, cliLogs)
	return logs
}

func clearTUILogs() {
	cliLogMu.Lock()
	defer cliLogMu.Unlock()
	cliLogs = nil
}

func nextHandle(action *Action, result ActionResult) bool {
	return false
}
