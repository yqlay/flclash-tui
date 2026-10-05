//go:build linux && !cgo && cli

package main

// Keep persisted Backend logs and process-local TUI events separate: appending
// a notification must never replace a page populated by the Backend.
func mergeTUILogBuffers(snapshot *tuiSnapshot, local []string) {
	selected := ""
	if index := snapshot.SelectedLog; index >= 0 && index < len(snapshot.Logs) {
		selected = snapshot.Logs[index]
	}
	if !snapshot.LogsInitialized {
		snapshot.BackendLogs = append([]string(nil), snapshot.Logs...)
		snapshot.LogsInitialized = true
	}
	snapshot.LocalLogs = append([]string(nil), local...)
	snapshot.Logs = append(append([]string(nil), snapshot.BackendLogs...), snapshot.LocalLogs...)
	if len(snapshot.Logs) > 1500 {
		snapshot.Logs = snapshot.Logs[len(snapshot.Logs)-1500:]
	}
	snapshot.SelectedLog = findTUILog(snapshot.Logs, selected)
	if snapshot.SelectedLog < 0 {
		snapshot.SelectedLog = firstTUILogMatch(*snapshot)
		snapshot.LogDetailOpen = false
	}
}
