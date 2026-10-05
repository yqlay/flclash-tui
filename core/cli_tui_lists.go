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

func drawTUIRequests(b *strings.Builder, snapshot tuiSnapshot, width, height int, language ...string) {
	tr := tuiTranslator(language...)
	if snapshot.HistoryDetailOpen {
		drawTUIRequestDetail(b, snapshot, width, language...)
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
		b, tr("ui.0e7696009337"), fmt.Sprintf(tr("ui.865df88c5fea"), len(indexes), len(snapshot.Requests), active, formatBytes(upload), formatBytes(download), tuiDefaultValue(snapshot.TrafficSource, tuiTrafficSourceMixed), tuiDefaultValue(snapshot.HistoryFilter, "all")),
		width,
	)
	if len(indexes) == 0 {
		tuiRow(b, tr("ui.6b79118cd840"), width, false, tuiDim)
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
	stateWidth := maxTUIWidth(7, maxTUIWidth(tuiDisplayWidth(tr("history.done")), tuiDisplayWidth(tr("history.active"))))
	for index := start; index < end && rows < height-3; index++ {
		actualIndex := indexes[index]
		request := snapshot.Requests[actualIndex]
		host := request.Host
		if host == "" {
			host = request.ID
		}
		state := tr("history.done")
		color := tuiDim
		if request.Active {
			state = tr("history.active")
			color = tuiGreen
		}
		row := tuiColumns(width-4,
			tuiColumn{value: tuiConnectionSource(request.TuiConnection), width: 5, minimum: 3, optional: true},
			tuiColumn{value: state, width: stateWidth, minimum: stateWidth},
			tuiColumn{value: host, width: 28, minimum: 8},
			tuiColumn{value: request.Network, width: 7, minimum: 3, optional: true},
			tuiColumn{value: request.Chain, width: 16, minimum: 8, optional: true},
			tuiColumn{value: request.LastSeen.Format("15:04:05"), width: 8, minimum: 8, optional: true},
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
					process = fmt.Sprintf(tr("ui.a8d91f87fd91"), request.UID)
				} else {
					process = fmt.Sprintf(tr("ui.3ccec61ba395"), process, request.UID)
				}
			}
			tuiRow(
				b, tr("ui.8a9528d3cad5")+
					strings.TrimSpace(process), width,
				false,
				tuiDim,
			)
			rows++
		}
	}
	tuiEndPanel(b, width)
}

func drawTUIConnections(b *strings.Builder, snapshot tuiSnapshot, width, height int, language ...string) {
	tr := tuiTranslator(language...)
	if snapshot.ConnectionsDetailOpen {
		drawTUIConnectionDetail(b, snapshot, width, language...)
		return
	}
	indexes := matchedTUIConnectionIndexes(snapshot)
	var upload, download int64
	for _, connection := range snapshot.Connections {
		upload += connection.Upload
		download += connection.Download
	}
	tuiTitle(b, tr("ui.dc273117482b"), fmt.Sprintf(tr("ui.acf6b342bb62"), len(indexes), len(snapshot.Connections), formatBytes(upload), formatBytes(download), tuiDefaultValue(snapshot.TrafficSource, tuiTrafficSourceMixed)), width)
	if len(indexes) == 0 {
		tuiRow(b, tr("ui.d87808a112a9"), width, false, tuiDim)
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
		row := tuiColumns(width-4,
			tuiColumn{value: tuiConnectionSource(connection), width: 5, minimum: 3, optional: true},
			tuiColumn{value: label, width: 30, minimum: 8},
			tuiColumn{value: connection.Network, width: 7, minimum: 3, optional: true},
			tuiColumn{value: connection.Chain, width: 16, minimum: 8, optional: true},
			tuiColumn{value: "↑" + formatBytes(connection.Upload), width: 10, minimum: tuiDisplayWidth("↑" + formatBytes(connection.Upload))},
			tuiColumn{value: "↓" + formatBytes(connection.Download), width: 10, minimum: tuiDisplayWidth("↓" + formatBytes(connection.Download))},
		)
		tuiRow(b, row, width, actualIndex == snapshot.SelectedConnection && !snapshot.FocusSidebar, "")
		rows++
		if (connection.Process != "" || connection.UID != 0) && rows < height-3 {
			process := connection.Process
			if connection.UID != 0 {
				if process == "" {
					process = fmt.Sprintf(tr("ui.a8d91f87fd91"), connection.UID)
				} else {
					process = fmt.Sprintf(tr("ui.3ccec61ba395"), process, connection.UID)
				}
			}
			tuiRow(b, tr("ui.8a9528d3cad5")+strings.TrimSpace(process), width, false, tuiDim)
			rows++
		}
	}
	tuiEndPanel(b, width)
}

func drawTUILogs(b *strings.Builder, snapshot tuiSnapshot, width, height int, language ...string) {
	tr := tuiTranslator(language...)
	if snapshot.LogDetailOpen {
		drawTUILogDetail(b, snapshot, width, language...)
		return
	}
	indexes := matchedTUILogIndexes(snapshot)
	tuiTitle(b, tr("ui.ea2100dc89ae"), fmt.Sprintf(tr("ui.ebd20c8948af"), len(indexes), len(snapshot.Logs), tuiDefaultValue(snapshot.LogsLevel, tr("ui.b5c7aed7cd2a"))), width)
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
		tuiRow(b, tr("ui.f474c18dfd1d"), width, false, tuiDim)
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
		if !trafficSourceMatches(snapshot.TrafficSource, tuiConnectionSource(request.TuiConnection)) {
			continue
		}
		haystack := strings.ToLower(strings.Join([]string{
			request.ID, request.Host, request.Process, request.ProcessPath,
			request.Network, request.Chain, tuiConnectionSource(request.TuiConnection),
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
		if !trafficSourceMatches(snapshot.TrafficSource, tuiConnectionSource(connection)) {
			continue
		}
		haystack := strings.ToLower(strings.Join([]string{
			connection.ID, connection.Host, connection.Process, connection.ProcessPath,
			connection.Network, connection.Chain, tuiConnectionSource(connection),
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

func drawTUIRequestDetail(b *strings.Builder, snapshot tuiSnapshot, width int, language ...string) {
	tr := tuiTranslator(language...)
	if snapshot.SelectedRequest < 0 || snapshot.SelectedRequest >= len(snapshot.Requests) {
		tuiEmptyPanel(b, tr("ui.99a49fb3ed65"), tr("ui.c92742be0707"), width)
		return
	}
	request := snapshot.Requests[snapshot.SelectedRequest]
	tuiTitle(b, tr("ui.99a49fb3ed65"), tr("ui.f0f4adc085cf"), width)
	state := tuiLabelField(tr("ui.629d8f58caed"), map[bool]string{true: tr("ui.630c2f1c0ee1"), false: tr("ui.c48179f5246e")}[request.Active])
	state.color = tuiGreen
	tuiWriteRows(b, tuiFieldRows([]tuiField{state}, width), width)
	drawTUIConnectionFields(b, request.TuiConnection, width, language...)
	tuiWriteRows(b, tuiFieldRows([]tuiField{
		tuiLabelField(tr("ui.7e9061d1952f"), request.FirstSeen.Format(time.RFC3339)),
		tuiLabelField(tr("ui.6a4407c1cc7e"), request.LastSeen.Format(time.RFC3339)),
	}, width), width)
	tuiEndPanel(b, width)
}

func drawTUIConnectionDetail(b *strings.Builder, snapshot tuiSnapshot, width int, language ...string) {
	tr := tuiTranslator(language...)
	if snapshot.SelectedConnection < 0 || snapshot.SelectedConnection >= len(snapshot.Connections) {
		tuiEmptyPanel(b, tr("ui.47f3295b13f9"), tr("ui.4ff6aca91db8"), width)
		return
	}
	tuiTitle(b, tr("ui.47f3295b13f9"), tr("ui.0cfe8e56a018"), width)
	drawTUIConnectionFields(b, snapshot.Connections[snapshot.SelectedConnection], width, language...)
	tuiEndPanel(b, width)
}

func drawTUIConnectionFields(b *strings.Builder, connection tuiConnection, width int, language ...string) {
	tr := tuiTranslator(language...)
	fields := []tuiField{
		tuiLabelField(tr("ui.1f4de645809d"), tuiConnectionSource(connection)),
		tuiLabelField(tr("ui.62b30a8c214b"), cliDisplayValue(connection.Host)),
		tuiLabelField(tr("ui.c723789f30c6"), cliDisplayValue(connection.Network)),
		tuiLabelField(tr("ui.231c08290fb4"), cliDisplayValue(connection.Chain)),
		tuiLabelField(tr("ui.d7f01d8f04bb"), cliDisplayValue(connection.Process)),
		tuiLabelField(tr("ui.31c23451958c"), cliDisplayValue(connection.ProcessPath)),
		tuiTemplateField(tr("ui.66c2cd3e4003"), connection.UID),
		tuiTemplateField(tr("ui.b8c84081aa9a"), formatBytes(connection.Upload), formatBytes(connection.Download)),
		tuiLabelField(tr("ui.660ef24fb34c"), connection.ID),
	}
	fields[0].color, fields[1].color = tuiCyan, tuiCyan
	fields[5].color, fields[8].color = tuiDim, tuiDim
	fields[5].truncate, fields[8].truncate = true, true
	tuiWriteRows(b, tuiFieldRows(fields, width), width)
}

func drawTUILogDetail(b *strings.Builder, snapshot tuiSnapshot, width int, language ...string) {
	tr := tuiTranslator(language...)
	if snapshot.SelectedLog < 0 || snapshot.SelectedLog >= len(snapshot.Logs) {
		tuiEmptyPanel(b, tr("ui.b6efa3086da6"), tr("ui.c92742be0707"), width)
		return
	}
	line := snapshot.Logs[snapshot.SelectedLog]
	tuiTitle(b, tr("ui.b6efa3086da6"), tr("ui.4063b5fbde39"), width)
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

func drawTUISettings(b *strings.Builder, snapshot tuiSnapshot, width, height int, language ...string) {
	tr := tuiTranslator(language...)
	rows := tuiFieldRows(tuiSettingsFields(snapshot, language...), width)
	subtitle := tr("ui.5057b2d12ef2")
	if snapshot.ExternalCore {
		subtitle = tr("ui.db54e959c852")
	} else if snapshot.ServiceRunning {
		subtitle = tr("ui.31ecd28d6667")
	}
	tuiTitle(b, tr("ui.74a883a037bc"), subtitle, width)
	rowLimit := minTUI(len(rows), maxTUIWidth(height-3, 1))
	if height >= len(rows)+7 {
		rowLimit = len(rows)
	}
	tuiWriteRows(b, tuiSelectedRows(rows, rowLimit), width)
	tuiEndPanel(b, width)
	if height >= len(rows)+7 {
		tuiEmptyPanel(b, tr("ui.20a35e6d0645"), tr("ui.980ac5e5c777"), width)
	}
}

func tuiSettingsRows(snapshot tuiSnapshot, language ...string) []string {
	rows := tuiFieldRows(tuiSettingsFields(snapshot, language...), 4096)
	values := make([]string, len(rows))
	for index, row := range rows {
		values[index] = row.value
	}
	return values
}

func tuiSettingsFields(snapshot tuiSnapshot, language ...string) []tuiField {
	tr := tuiTranslator(language...)
	fields := []tuiField{
		tuiLabelField("Language", strings.TrimSpace(strings.TrimPrefix(tuiLanguageLabel(snapshot.Language), "Language"))),
		tuiTemplateField(tr("ui.c63700c619e9"), tuiOnOff(snapshot.Settings.AllowLAN, language...)),
		tuiTemplateField(tr("ui.68724b68057a"), tuiOnOff(snapshot.Settings.IPv6, language...)),
		tuiTemplateField(tr("ui.ff86fccccb37"), tuiOnOff(snapshot.Settings.UnifiedDelay, language...)),
		tuiTemplateField(tr("ui.1aea042bd24f"), tuiOnOff(snapshot.Settings.TCPConcurrent, language...)),
		tuiTemplateField(tr("ui.b8bf7e9135ba"), snapshot.Settings.LogLevel),
		tuiTemplateField(tr("ui.8ab6f53189fb"), strings.ToUpper(tuiEffectiveTunScope(snapshot.Settings.TunScope))),
	}
	for index := range fields {
		fields[index].selected = index == snapshot.SelectedTool && !snapshot.FocusSidebar
	}
	return fields
}

func tuiFLCOutboundLabel(snapshot tuiSnapshot, language ...string) string {
	tr := tuiTranslator(language...)
	group := strings.TrimSpace(snapshot.FLCOutbound)
	if group == "" {
		return tr("ui.5061e6308a55")
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
		return value + tr("ui.0aa7e0c92995")
	}
	return value + tr("ui.ab2a95485594")
}

func tuiServiceLabel(snapshot tuiSnapshot, language ...string) string {
	tr := tuiTranslator(language...)
	if snapshot.ExternalCore {
		return tr("ui.2aa170dd5d02")
	}
	if snapshot.ServiceRunning {
		return tr("ui.c822fa1222e5")
	}
	return tr("ui.c0168e5ebe9d")
}

func tuiSystemProxyLabel(snapshot tuiSnapshot, language ...string) string {
	tr := tuiTranslator(language...)
	if snapshot.Settings.Mode == tuiSilentMode {
		return tr("ui.e2575e46d735")
	}
	if snapshot.Settings.SystemProxy {
		return tr("ui.cd4670734e84")
	}
	if !snapshot.ExternalCore && !snapshot.ServiceRunning {
		return tr("ui.2bae69d8c9f4")
	}
	return tr("ui.bc48a7d78201")
}

func tuiTUNLabel(snapshot tuiSnapshot, language ...string) string {
	tr := tuiTranslator(language...)
	if snapshot.Settings.Mode == tuiSilentMode {
		return tr("ui.8f2d54b1125c")
	}
	scope := snapshot.Settings.TunScope
	if scope == "" {
		scope = tuiTunScopeUser
	}
	if snapshot.Settings.TunEnabled {
		return strings.ToUpper(scope) + tr("ui.fa1f35b4348f")
	}
	return tr("ui.b7ba1ba85dd1") + scope
}

func tuiEffectiveTunScope(scope string) string {
	if scope == "" {
		return tuiTunScopeUser
	}
	return scope
}

func tuiProxyPortLabel(snapshot tuiSnapshot, language ...string) string {
	if snapshot.ActiveProxyPort > 0 {
		return strconv.Itoa(snapshot.ActiveProxyPort)
	}
	if snapshot.Settings.MixedPort > 0 {
		return strconv.Itoa(snapshot.Settings.MixedPort)
	}
	return tuiTranslator(language...)("proxy.not_ready")
}

func tuiOnOff(enabled bool, language ...string) string {
	tr := tuiTranslator(language...)
	if enabled {
		return tr("ui.e8a01133b135")
	}
	return tr("ui.38cca6bea010")
}
