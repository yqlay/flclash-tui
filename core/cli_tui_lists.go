//go:build linux && !cgo && cli

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func drawTUIRequests(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	if snapshot.HistoryDetailOpen {
		drawTUIRequestDetail(b, snapshot, width)
		return
	}
	indexes := matchedTUIRequestIndexes(snapshot)
	active := 0
	var upload, download int64
	for _, request := range snapshot.Requests {
		if request.Active {
			active++
		}
		upload += request.Upload
		download += request.Download
	}
	tuiTitle(
		b,
		"History",
		fmt.Sprintf("%d/%d shown · %d active · ↑%s ↓%s · / search · f filter:%s · Enter detail · x clear", len(indexes), len(snapshot.Requests), active, formatBytes(upload), formatBytes(download), tuiDefaultValue(snapshot.HistoryFilter, "all")),
		width,
	)
	if len(indexes) == 0 {
		tuiRow(b, "No matching connection history", width, false, tuiDim)
		tuiEndPanel(b, width)
		return
	}
	limit := maxTUIWidth((height-3)/2, 1)
	start, end := tuiVisibleRange(
		len(indexes),
		findTUIInt(indexes, snapshot.SelectedRequest),
		limit,
	)
	rows := 0
	for index := start; index < end && rows < height-3; index++ {
		actualIndex := indexes[index]
		request := snapshot.Requests[actualIndex]
		host := request.Host
		if host == "" {
			host = request.ID
		}
		state := "done"
		color := tuiDim
		if request.Active {
			state = "active"
			color = tuiGreen
		}
		row := fmt.Sprintf(
			"%-7s %-30s %-7s %-18s %s",
			state,
			truncateTUI(host, 30),
			request.Network,
			truncateTUI(request.Chain, 18),
			request.LastSeen.Format("15:04:05"),
		)
		tuiRow(
			b,
			row,
			width,
			actualIndex == snapshot.SelectedRequest && !snapshot.FocusSidebar,
			color,
		)
		rows++
		if (request.Process != "" || request.UID != 0) && rows < height-3 {
			process := request.Process
			if request.UID != 0 {
				if process == "" {
					process = fmt.Sprintf("UID %d", request.UID)
				} else {
					process = fmt.Sprintf("%s · UID %d", process, request.UID)
				}
			}
			tuiRow(
				b,
				"process: "+strings.TrimSpace(process),
				width,
				false,
				tuiDim,
			)
			rows++
		}
	}
	tuiEndPanel(b, width)
}

func drawTUIConnections(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	if snapshot.ConnectionsDetailOpen {
		drawTUIConnectionDetail(b, snapshot, width)
		return
	}
	indexes := matchedTUIConnectionIndexes(snapshot)
	var upload, download int64
	for _, connection := range snapshot.Connections {
		upload += connection.Upload
		download += connection.Download
	}
	tuiTitle(b, "Connections", fmt.Sprintf("%d/%d active · ↑%s ↓%s · / search · Enter detail · d close · x close all", len(indexes), len(snapshot.Connections), formatBytes(upload), formatBytes(download)), width)
	if len(indexes) == 0 {
		tuiRow(b, "No matching active connections", width, false, tuiDim)
		tuiEndPanel(b, width)
		return
	}
	limit := maxTUIWidth((height-3)/2, 1)
	start, end := tuiVisibleRange(len(indexes), findTUIInt(indexes, snapshot.SelectedConnection), limit)
	rows := 0
	for index := start; index < end; index++ {
		actualIndex := indexes[index]
		connection := snapshot.Connections[actualIndex]
		if rows >= height-3 {
			break
		}
		label := connection.Host
		if label == "" {
			label = connection.ID
		}
		row := fmt.Sprintf("%-32s %-7s %-18s ↑%-9s ↓%-9s", truncateTUI(label, 32), connection.Network, truncateTUI(connection.Chain, 18), formatBytes(connection.Upload), formatBytes(connection.Download))
		tuiRow(b, row, width, actualIndex == snapshot.SelectedConnection && !snapshot.FocusSidebar, "")
		rows++
		if (connection.Process != "" || connection.UID != 0) && rows < height-3 {
			process := connection.Process
			if connection.UID != 0 {
				if process == "" {
					process = fmt.Sprintf("UID %d", connection.UID)
				} else {
					process = fmt.Sprintf("%s · UID %d", process, connection.UID)
				}
			}
			tuiRow(b, "process: "+strings.TrimSpace(process), width, false, tuiDim)
			rows++
		}
	}
	tuiEndPanel(b, width)
}

func drawTUILogs(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	if snapshot.LogDetailOpen {
		drawTUILogDetail(b, snapshot, width)
		return
	}
	indexes := matchedTUILogIndexes(snapshot)
	tuiTitle(b, "Logs", fmt.Sprintf("%d/%d shown · backend + TUI · / search · f level:%s · Enter detail · e export · x clear", len(indexes), len(snapshot.Logs), tuiDefaultValue(snapshot.LogsLevel, "ALL")), width)
	limit := maxTUIWidth(height-3, 1)
	selectedPosition := findTUIInt(indexes, snapshot.SelectedLog)
	if selectedPosition < 0 && len(indexes) > 0 {
		selectedPosition = len(indexes) - 1
	}
	start, end := tuiVisibleRange(len(indexes), selectedPosition, limit)
	for position := start; position < end; position++ {
		index := indexes[position]
		tuiRow(b, snapshot.Logs[index], width, index == snapshot.SelectedLog && !snapshot.FocusSidebar, tuiLogLineColor(snapshot.Logs[index]))
	}
	if len(indexes) == 0 {
		tuiRow(b, "No matching logs captured", width, false, tuiDim)
	}
	tuiEndPanel(b, width)
}

func tuiDefaultValue(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func findTUIInt(values []int, wanted int) int {
	for index, value := range values {
		if value == wanted {
			return index
		}
	}
	return -1
}

func matchedTUIRequestIndexes(snapshot tuiSnapshot) []int {
	query := strings.ToLower(strings.TrimSpace(snapshot.HistoryQuery))
	filter := strings.ToLower(tuiDefaultValue(snapshot.HistoryFilter, "all"))
	indexes := make([]int, 0, len(snapshot.Requests))
	for index, request := range snapshot.Requests {
		if filter == "active" && !request.Active || filter == "completed" && request.Active {
			continue
		}
		haystack := strings.ToLower(strings.Join([]string{
			request.ID, request.Host, request.Process, request.ProcessPath,
			request.Network, request.Chain,
		}, " "))
		if query == "" || strings.Contains(haystack, query) {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func matchedTUIConnectionIndexes(snapshot tuiSnapshot) []int {
	query := strings.ToLower(strings.TrimSpace(snapshot.ConnectionsQuery))
	indexes := make([]int, 0, len(snapshot.Connections))
	for index, connection := range snapshot.Connections {
		haystack := strings.ToLower(strings.Join([]string{
			connection.ID, connection.Host, connection.Process, connection.ProcessPath,
			connection.Network, connection.Chain,
		}, " "))
		if query == "" || strings.Contains(haystack, query) {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func matchedTUILogIndexes(snapshot tuiSnapshot) []int {
	query := strings.ToLower(strings.TrimSpace(snapshot.LogsQuery))
	level := strings.ToUpper(tuiDefaultValue(snapshot.LogsLevel, "ALL"))
	indexes := make([]int, 0, len(snapshot.Logs))
	for index, line := range snapshot.Logs {
		upperLine := strings.ToUpper(line)
		if level != "ALL" &&
			!strings.Contains(upperLine, " "+level+" ") &&
			!strings.Contains(upperLine, "LEVEL="+level) {
			continue
		}
		if query == "" || strings.Contains(strings.ToLower(line), query) {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func firstTUIRequestMatch(snapshot tuiSnapshot) int {
	indexes := matchedTUIRequestIndexes(snapshot)
	if len(indexes) == 0 {
		return -1
	}
	return indexes[0]
}

func firstTUIConnectionMatch(snapshot tuiSnapshot) int {
	indexes := matchedTUIConnectionIndexes(snapshot)
	if len(indexes) == 0 {
		return -1
	}
	return indexes[0]
}

func firstTUILogMatch(snapshot tuiSnapshot) int {
	indexes := matchedTUILogIndexes(snapshot)
	if len(indexes) == 0 {
		return -1
	}
	return indexes[len(indexes)-1]
}

func moveTUIRequestMatch(snapshot *tuiSnapshot, delta int) {
	indexes := matchedTUIRequestIndexes(*snapshot)
	if len(indexes) == 0 {
		return
	}
	position := findTUIInt(indexes, snapshot.SelectedRequest)
	if position < 0 {
		if delta < 0 {
			position = 0
		} else {
			position = -1
		}
	}
	snapshot.SelectedRequest = indexes[wrapTUIIndex(position, delta, len(indexes))]
}

func moveTUIConnectionMatch(snapshot *tuiSnapshot, delta int) {
	indexes := matchedTUIConnectionIndexes(*snapshot)
	if len(indexes) == 0 {
		return
	}
	position := findTUIInt(indexes, snapshot.SelectedConnection)
	if position < 0 {
		if delta < 0 {
			position = 0
		} else {
			position = -1
		}
	}
	snapshot.SelectedConnection = indexes[wrapTUIIndex(position, delta, len(indexes))]
}

func moveTUILogMatch(snapshot *tuiSnapshot, delta int) {
	indexes := matchedTUILogIndexes(*snapshot)
	if len(indexes) == 0 {
		return
	}
	position := findTUIInt(indexes, snapshot.SelectedLog)
	if position < 0 {
		position = len(indexes) - 1
	}
	snapshot.SelectedLog = indexes[wrapTUIIndex(position, delta, len(indexes))]
}

func tuiLogLineColor(line string) string {
	upper := strings.ToUpper(line)
	switch {
	case strings.Contains(upper, " ERROR ") || strings.Contains(upper, "LEVEL=ERROR"):
		return tuiRed
	case strings.Contains(upper, " WARN ") || strings.Contains(upper, "LEVEL=WARNING") || strings.Contains(upper, "LEVEL=WARN"):
		return tuiYellow
	case strings.Contains(upper, " INFO ") || strings.Contains(upper, "LEVEL=INFO"):
		return tuiCyan
	default:
		return tuiDim
	}
}

func drawTUIRequestDetail(b *strings.Builder, snapshot tuiSnapshot, width int) {
	if snapshot.SelectedRequest < 0 || snapshot.SelectedRequest >= len(snapshot.Requests) {
		tuiEmptyPanel(b, "History detail", "Entry is no longer available · Esc to return", width)
		return
	}
	request := snapshot.Requests[snapshot.SelectedRequest]
	tuiTitle(b, "History detail", "Esc returns to list", width)
	tuiRow(b, "State         "+map[bool]string{true: "ACTIVE", false: "COMPLETED"}[request.Active], width, false, tuiGreen)
	drawTUIConnectionFields(b, request.tuiConnection, width)
	tuiRow(b, "First seen    "+request.FirstSeen.Format(time.RFC3339), width, false, "")
	tuiRow(b, "Last seen     "+request.LastSeen.Format(time.RFC3339), width, false, "")
	tuiEndPanel(b, width)
}

func drawTUIConnectionDetail(b *strings.Builder, snapshot tuiSnapshot, width int) {
	if snapshot.SelectedConnection < 0 || snapshot.SelectedConnection >= len(snapshot.Connections) {
		tuiEmptyPanel(b, "Connection detail", "Connection has closed · Esc to return", width)
		return
	}
	tuiTitle(b, "Connection detail", "d close selected · Esc returns to list", width)
	drawTUIConnectionFields(b, snapshot.Connections[snapshot.SelectedConnection], width)
	tuiEndPanel(b, width)
}

func drawTUIConnectionFields(b *strings.Builder, connection tuiConnection, width int) {
	tuiRow(b, "Host          "+cliDisplayValue(connection.Host), width, false, tuiCyan)
	tuiRow(b, "Network       "+cliDisplayValue(connection.Network), width, false, "")
	tuiRow(b, "Route         "+cliDisplayValue(connection.Chain), width, false, "")
	tuiRow(b, "Process       "+cliDisplayValue(connection.Process), width, false, "")
	tuiRow(b, "Process path  "+cliDisplayValue(connection.ProcessPath), width, false, tuiDim)
	tuiRow(b, fmt.Sprintf("UID           %d", connection.UID), width, false, "")
	tuiRow(b, fmt.Sprintf("Traffic       ↑ %s   ↓ %s", formatBytes(connection.Upload), formatBytes(connection.Download)), width, false, "")
	tuiRow(b, "ID            "+connection.ID, width, false, tuiDim)
}

func drawTUILogDetail(b *strings.Builder, snapshot tuiSnapshot, width int) {
	if snapshot.SelectedLog < 0 || snapshot.SelectedLog >= len(snapshot.Logs) {
		tuiEmptyPanel(b, "Log detail", "Entry is no longer available · Esc to return", width)
		return
	}
	line := snapshot.Logs[snapshot.SelectedLog]
	tuiTitle(b, "Log detail", "full entry · Esc returns to list", width)
	for _, wrapped := range tuiWrapText(line, maxTUIWidth(width-4, 1)) {
		tuiRow(b, wrapped, width, false, tuiLogLineColor(line))
	}
	tuiEndPanel(b, width)
}

func formatTUIDestination(host, port string) string {
	if host == "" {
		return ""
	}
	if port == "" {
		return host
	}
	return net.JoinHostPort(host, port)
}

func exportTUILogs(homeDir string, logs []string) (string, error) {
	if len(logs) == 0 {
		return "", errors.New("no logs captured yet")
	}
	logDir := filepath.Join(homeDir, "logs")
	if err := os.MkdirAll(logDir, 0o700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(logDir, "flclash-"+time.Now().Format("20060102-150405")+"-*.log")
	if err != nil {
		return "", err
	}
	path := file.Name()
	_, writeErr := file.WriteString(strings.Join(logs, "\n") + "\n")
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func drawTUISettings(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	rows := tuiSettingsRows(snapshot)
	subtitle := "Core is stopped · changes are staged until it starts"
	if snapshot.ExternalCore {
		subtitle = "↑/↓ select · Enter change · external core configuration"
	} else if snapshot.ServiceRunning {
		subtitle = "Core is running · changes apply immediately"
	}
	tuiTitle(b, "Settings", subtitle, width)
	rowLimit := minTUI(len(rows), maxTUIWidth(height-3, 1))
	if height >= len(rows)+7 {
		rowLimit = len(rows)
	}
	for index, row := range rows[:rowLimit] {
		tuiRow(b, row, width, index == snapshot.SelectedTool && !snapshot.FocusSidebar, "")
	}
	tuiEndPanel(b, width)
	if height >= len(rows)+7 {
		tuiEmptyPanel(b, "Optional keys", "a LAN · v IPv6 · i log level · Dashboard: c Core · m mode · t TUN · p port · S system proxy", width)
	}
}

func tuiSettingsRows(snapshot tuiSnapshot) []string {
	return []string{
		fmt.Sprintf("Allow LAN     %s", tuiOnOff(snapshot.Settings.AllowLAN)),
		fmt.Sprintf("IPv6          %s", tuiOnOff(snapshot.Settings.IPv6)),
		fmt.Sprintf(
			"Unified delay %s · warm connection, matches FlClash",
			tuiOnOff(snapshot.Settings.UnifiedDelay),
		),
		fmt.Sprintf(
			"TCP concurrent %s · race available addresses",
			tuiOnOff(snapshot.Settings.TCPConcurrent),
		),
		fmt.Sprintf("Log level     %s", snapshot.Settings.LogLevel),
		fmt.Sprintf("TUN scope     %s", strings.ToUpper(tuiEffectiveTunScope(snapshot.Settings.TunScope))),
	}
}

func tuiFLCOutboundLabel(snapshot tuiSnapshot) string {
	group := strings.TrimSpace(snapshot.FLCOutbound)
	if group == "" {
		return "pick a node in Proxies"
	}
	value := group
	for _, candidate := range snapshot.Groups {
		if strings.EqualFold(candidate.Name, group) && strings.TrimSpace(candidate.Now) != "" {
			value = group + " → " + candidate.Now
			break
		}
	}
	if snapshot.Settings.Mode != tuiSilentMode {
		return value
	}
	if snapshot.FLCEnabled {
		return value + " · READY"
	}
	return value + " · WAITING FOR CORE"
}

func tuiServiceLabel(snapshot tuiSnapshot) string {
	if snapshot.ExternalCore {
		return "EXTERNAL · managed by another process"
	}
	if snapshot.ServiceRunning {
		return "RUNNING · Enter to stop"
	}
	return "STOPPED · Enter to start"
}

func tuiSystemProxyLabel(snapshot tuiSnapshot) string {
	if snapshot.Settings.Mode == tuiSilentMode {
		return "DISABLED · locked by silent mode"
	}
	if snapshot.Settings.SystemProxy {
		return "ENABLED · Enter to disable"
	}
	if !snapshot.ExternalCore && !snapshot.ServiceRunning {
		return "DISABLED · Enter to enable (starts Core)"
	}
	return "DISABLED · Enter to enable"
}

func tuiTUNLabel(snapshot tuiSnapshot) string {
	if snapshot.Settings.Mode == tuiSilentMode {
		return "OFF · locked by silent mode"
	}
	scope := snapshot.Settings.TunScope
	if scope == "" {
		scope = tuiTunScopeUser
	}
	if snapshot.Settings.TunEnabled {
		return strings.ToUpper(scope) + " ON"
	}
	return "OFF · " + scope
}

func tuiEffectiveTunScope(scope string) string {
	if scope == "" {
		return tuiTunScopeUser
	}
	return scope
}

func tuiProxyPortLabel(snapshot tuiSnapshot) string {
	if snapshot.ActiveProxyPort > 0 {
		return strconv.Itoa(snapshot.ActiveProxyPort)
	}
	if snapshot.Settings.MixedPort > 0 {
		return strconv.Itoa(snapshot.Settings.MixedPort)
	}
	return "NOT READY"
}

func tuiOnOff(enabled bool) string {
	if enabled {
		return "ON"
	}
	return "OFF"
}
