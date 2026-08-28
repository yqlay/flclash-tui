//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var (
	cliLogMu           sync.Mutex
	cliPersistentLogMu sync.Mutex
	cliLogs            []string
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
	if len(cliLogs) > 500 {
		cliLogs = cliLogs[len(cliLogs)-500:]
	}
}

func appendTUILogEvent(level, message string) {
	line := formatCLIApplicationLog(level, "tui", message)
	cliLogMu.Lock()
	defer cliLogMu.Unlock()
	cliLogs = append(cliLogs, line)
	if len(cliLogs) > 500 {
		cliLogs = cliLogs[len(cliLogs)-500:]
	}
}

func appendCLIApplicationLog(homeDir, level, event, detail string) {
	detail = sanitizeCLIApplicationLogDetail(homeDir, detail)
	line := formatCLIApplicationLog(level, event, detail)
	cliLogMu.Lock()
	cliLogs = append(cliLogs, line)
	if len(cliLogs) > 500 {
		cliLogs = cliLogs[len(cliLogs)-500:]
	}
	cliLogMu.Unlock()
	if strings.TrimSpace(homeDir) == "" {
		return
	}
	path := filepath.Join(homeDir, tuiServiceLogFilename)
	cliPersistentLogMu.Lock()
	defer cliPersistentLogMu.Unlock()
	rotateTUIServiceLogUnlocked(path)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = file.WriteString(line + "\n")
	_ = file.Close()
}

func formatCLIApplicationLog(level, event, detail string) string {
	level = strings.ToUpper(strings.TrimSpace(level))
	if level == "" {
		level = "INFO"
	}
	detail = strings.Join(strings.Fields(detail), " ")
	return fmt.Sprintf(
		"%s %-5s %-24s %s",
		time.Now().Format(time.RFC3339),
		level,
		event,
		detail,
	)
}

func sanitizeCLIApplicationLogDetail(homeDir, detail string) string {
	detail = strings.ReplaceAll(detail, "\r", " ")
	detail = strings.ReplaceAll(detail, "\n", " ")
	if homeDir != "" {
		detail = strings.ReplaceAll(detail, filepath.Clean(homeDir), "$DATA")
	}
	fields := strings.Fields(detail)
	for index, field := range fields {
		if strings.Contains(field, "://") {
			fields[index] = "[redacted-url]"
		}
	}
	return strings.Join(fields, " ")
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
