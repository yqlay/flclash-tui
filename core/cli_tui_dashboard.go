//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"strings"
)

func tuiDashboardControlFields(snapshot tuiSnapshot, language ...string) []tuiField {
	tr := tuiTranslator(language...)
	fields := []tuiField{
		tuiTemplateField(tr("ui.f18cab8abac0"), tuiServiceLabel(snapshot, language...)),
		tuiTemplateField(tr("ui.e03730a5c7f3"), tuiSystemProxyLabel(snapshot, language...)),
		tuiTemplateField(tr("ui.a4d79613e35b"), tuiTUNLabel(snapshot, language...)),
		tuiTemplateField(tr("ui.9a0dbc8998df"), snapshot.Settings.Mode),
		tuiTemplateField(tr("ui.1b54c40c04fc"), tuiFLCOutboundLabel(snapshot, language...)),
		tuiTemplateField(tr("ui.4e7d696cd5d1"), tuiProxyPortLabel(snapshot, language...)),
	}
	for index := range fields {
		fields[index].selected = index == snapshot.SelectedDashboard && !snapshot.FocusSidebar
	}
	return fields
}

func tuiDashboardNetworkFields(snapshot tuiSnapshot, language ...string) []tuiField {
	tr := tuiTranslator(language...)
	publicIP := tr("ui.2e5f79bb94a8")
	if snapshot.Network.PublicIP != "" {
		publicIP = snapshot.Network.PublicIP
		if snapshot.Network.Country != "" {
			publicIP += "  [" + snapshot.Network.Country + "]"
		}
		if snapshot.Network.Loading {
			publicIP += tr("ui.0651d028ba72")
		}
	} else if snapshot.Network.Error != "" && !snapshot.Network.Loading {
		publicIP = tr("ui.46e48f8fe395")
	}
	intranetIP := snapshot.Network.IntranetIP
	if intranetIP == "" {
		intranetIP = tr("ui.32bb0437abc1")
		if snapshot.Network.Loading {
			intranetIP = tr("ui.5ac197f8103a")
		}
	}
	fields := []tuiField{
		tuiLabelField(tr("ui.5e24a67e69b7"), publicIP),
		tuiLabelField(tr("ui.fc5dbbf8f80a"), intranetIP),
		tuiLabelField(tuiDashboardRouteName(snapshot, language...), tuiDashboardDelayLabel(snapshot.DashboardDelay, language...)),
		tuiLabelField(tr("ui.2448d6d56590"), tuiSpeedResultLabel(snapshot.DashboardSpeed, language...)),
	}
	fields[0].color, fields[1].color = tuiCyan, tuiGreen
	fields[2].color, fields[3].color = tuiCyan, tuiGreen
	fields[2].selected = snapshot.SelectedDashboard == tuiDashboardDelayRow && !snapshot.FocusSidebar
	fields[3].selected = snapshot.SelectedDashboard == tuiDashboardSpeedRow && !snapshot.FocusSidebar
	return fields
}

func tuiDashboardOverviewFields(snapshot tuiSnapshot, paths cliPaths, language ...string) []tuiField {
	tr := tuiTranslator(language...)
	fields := tuiMemoryFields(snapshot, language...)
	fields = append(fields,
		tuiTemplateField(tr("ui.15ae14e3c674"), formatBytes(snapshot.Traffic.Up), formatBytes(snapshot.Traffic.Down)),
		tuiTemplateField(tr("ui.15e659c979fb"), formatBytes(snapshot.TotalTraffic.Up), formatBytes(snapshot.TotalTraffic.Down)),
		tuiTemplateField(tr("ui.92f24da40aae"), len(snapshot.Connections), len(snapshot.Requests)),
		tuiLabelField(tr("ui.41efe1c5e221"), formatCLIFrontendSummary(snapshot.Frontends)),
		tuiTemplateField(tr("ui.e098301423c6"), paths.ConfigPath),
	)
	// Long paths are opaque data, not a reason to consume the entire chart.
	fields[len(fields)-1].truncate = true
	return fields
}

func drawTUIDashboard(b *strings.Builder, snapshot tuiSnapshot, paths cliPaths, width, height int, language ...string) {
	tr := tuiTranslator(language...)
	controls := tuiFieldRows(tuiDashboardControlFields(snapshot, language...), width)
	network := tuiFieldRows(tuiDashboardNetworkFields(snapshot, language...), width)
	overview := tuiFieldRows(tuiDashboardOverviewFields(snapshot, paths, language...), width)
	fixedRows := len(controls) + len(network) + len(overview) + 12
	if height >= 33 && height < fixedRows+1 {
		b.WriteString(renderTUICompactDashboard(snapshot, paths, width, height, language...))
		return
	}
	tuiTitle(b, tr("ui.67b696468610"), tr("ui.27c478967b20"), width)
	tuiWriteRows(b, controls, width)
	tuiEndPanel(b, width)
	networkSubtitle := ""
	if snapshot.Network.Route != "" {
		networkSubtitle = snapshot.Network.Route + " · "
	}
	networkSubtitle += "d " + strings.ToLower(tuiDashboardRouteName(snapshot, language...)) + tr("ui.1a476d7f86fc")
	if !snapshot.Network.CheckedAt.IsZero() {
		networkSubtitle += tr("ui.0727957e1df3") + snapshot.Network.CheckedAt.Format("15:04:05")
	}
	tuiTitle(b, tr("ui.3aae919b1bc0"), networkSubtitle, width)
	tuiWriteRows(b, network, width)
	tuiEndPanel(b, width)
	if height >= 33 && height > fixedRows {
		chart := buildTUITrafficChart(snapshot.TrafficHistory, maxTUIWidth(width-4, 1), height-fixedRows)
		tuiTrafficTitle(b, snapshot.Traffic, chart.peak, width, language...)
		for _, line := range chart.lines {
			writeTUIAnsiRow(b, line, width)
		}
		tuiEndPanel(b, width)
	}
	if height >= 17 {
		tuiTitle(b, tr("ui.d4b1ea5708dd"), tr("ui.4abac76b607e")+tuiMemoryRefreshInterval.String()+tr("ui.b3db8b20fba3"), width)
		tuiWriteRows(b, overview, width)
		tuiEndPanel(b, width)
	}
}

type tuiDashboardCompactRow struct {
	value    string
	color    string
	selected bool
	ansi     bool
}

func renderTUICompactDashboard(snapshot tuiSnapshot, paths cliPaths, width, height int, language ...string) string {
	tr := tuiTranslator(language...)
	rows := tuiCompactDashboardRows(snapshot, paths, width, height, language...)
	limit := maxTUIWidth(height-3, 1)
	start := minTUI(maxTUIIndex(snapshot.DashboardScroll), maxTUIIndex(len(rows)-limit))
	end := minTUI(start+limit, len(rows))
	var b strings.Builder
	tuiTitle(&b, tr("ui.67b696468610"), fmt.Sprintf(tr("ui.ed763ad9f535"), start+1, end, len(rows)), width)
	tuiWriteRows(&b, rows[start:end], width)
	tuiEndPanel(&b, width)
	return b.String()
}

func tuiCompactDashboardRows(snapshot tuiSnapshot, paths cliPaths, width, height int, language ...string) []tuiDashboardCompactRow {
	tr := tuiTranslator(language...)
	controlFields := tuiDashboardControlFields(snapshot, language...)
	for index := range controlFields {
		controlFields[index].truncate = true
	}
	controls := tuiFieldRows(controlFields, width)
	network := tuiFieldRows(tuiDashboardNetworkFields(snapshot, language...), width)
	overview := tuiFieldRows(tuiDashboardOverviewFields(snapshot, paths, language...), width)
	fixedRows := len(controls) + len(network) + len(overview) + 3
	chartHeight := maxTUIWidth(tuiCompactTrafficChartHeight(height), height-3-fixedRows)
	chart := buildTUITrafficChart(snapshot.TrafficHistory, maxTUIWidth(width-4, 1), chartHeight)
	rows := append([]tuiDashboardCompactRow(nil), controls...)
	rows = append(rows, tuiDashboardCompactRow{
		value: tuiCyan + tr("ui.43e907bfbc87") + tuiReset + " · " + formatTUITrafficLegend(snapshot.Traffic, chart.peak, language...), ansi: true,
	})
	for _, line := range chart.lines {
		rows = append(rows, tuiDashboardCompactRow{value: line, ansi: true})
	}
	rows = append(rows, tuiDashboardCompactRow{value: tr("ui.a8024f5e9c32") + strings.ToLower(tuiDashboardRouteName(snapshot, language...)) + tr("ui.e8595567c19b"), color: tuiCyan})
	rows = append(rows, network...)
	rows = append(rows, tuiDashboardCompactRow{value: tr("ui.7f2565a40efe"), color: tuiCyan})
	return append(rows, overview...)
}

func tuiDashboardRouteName(snapshot tuiSnapshot, language ...string) string {
	tr := tuiTranslator(language...)
	if strings.EqualFold(snapshot.Settings.Mode, tuiSilentMode) {
		return tr("ui.148be5b3761c")
	}
	return tr("ui.9f4d9e11fced")
}

func tuiCompactTrafficChartHeight(height int) int {
	switch {
	case height <= 10:
		return 1
	case height <= 14:
		return 2
	default:
		return 3
	}
}

func tuiDashboardDelayLabel(delay tuiDelayResult, language ...string) string {
	tr := tuiTranslator(language...)
	switch {
	case delay.MedianMillis > 0:
		return formatTUIDelay(delay, language...) + tr("ui.e5e2fc4ce8e2")
	case delay.Error != "":
		return tr("ui.e2943f0d5872")
	case delay.Testing:
		return tr("ui.6c02a28421f8")
	default:
		return tr("ui.e7f06d8fb8b9")
	}
}

func formatTUIDelay(result tuiDelayResult, language ...string) string {
	tr := tuiTranslator(language...)
	if result.Samples <= 1 {
		return fmt.Sprintf("%d ms", result.MedianMillis)
	}
	return fmt.Sprintf(tr("ui.6bdb453bc521"), result.MedianMillis,
		result.JitterMillis,
		result.Samples,
	)
}

func tuiSpeedResultLabel(result tuiSpeedResult, language ...string) string {
	tr := tuiTranslator(language...)
	switch {
	case result.Testing:
		return tr("ui.b1efed1838b0")
	case result.Error != "":
		return tr("ui.2683b48bf94c")
	case result.BytesPerSecond > 0:
		downloaded := float64(result.Bytes) / 1_000_000
		duration := float64(result.DurationMillis) / 1000
		return fmt.Sprintf(tr("ui.f1058f3fb043"), formatTUISpeed(result),
			downloaded,
			duration,
		)
	default:
		return tr("ui.e26c6a34cb5a")
	}
}

func tuiMemoryRows(snapshot tuiSnapshot, language ...string) []string {
	rows := tuiFieldRows(tuiMemoryFields(snapshot, language...), 4096)
	values := make([]string, len(rows))
	for index, row := range rows {
		values[index] = row.value
	}
	return values
}

func tuiMemoryFields(snapshot tuiSnapshot, language ...string) []tuiField {
	tr := tuiTranslator(language...)
	systemMemory := tr("ui.ca1844969742")
	if snapshot.Memory.SystemTotal > 0 {
		percentage := float64(snapshot.Memory.SystemUsed) /
			float64(snapshot.Memory.SystemTotal) * 100
		systemMemory = fmt.Sprintf(
			"%s / %s  %.1f%%",
			formatTUIUintBytes(snapshot.Memory.SystemUsed),
			formatTUIUintBytes(snapshot.Memory.SystemTotal),
			percentage,
		)
	}
	rows := []tuiField{tuiLabelField(tr("ui.bc59b24ffe23"), systemMemory)}
	if snapshot.ExternalCore || snapshot.ManagedService {
		processMemory := tr("ui.e7c955a5ec17")
		if snapshot.Memory.ProcessRSS > 0 {
			processMemory = formatTUIUintBytes(snapshot.Memory.ProcessRSS)
		}
		coreMemory := tr("ui.e7c955a5ec17")
		if snapshot.Memory.CoreRSS > 0 {
			coreMemory = formatTUIUintBytes(snapshot.Memory.CoreRSS)
		} else if snapshot.Memory.CoreError != "" {
			coreMemory = tr("ui.88b0f6cc37a2")
		}
		rows = append(
			rows, tuiLabelField(tr("ui.ed62a0b483ae"), processMemory), func() tuiField {
				if snapshot.ManagedService {
					return tuiLabelField(tr("ui.8f9fba6af37b"), coreMemory)
				}
				return tuiLabelField(tr("ui.02422146f8b3"), coreMemory)
			}(),
		)
	} else {
		processMemory := tr("ui.e7c955a5ec17")
		if snapshot.Memory.ProcessRSS > 0 {
			processMemory = formatTUIUintBytes(snapshot.Memory.ProcessRSS)
		}
		rows = append(
			rows, tuiLabelField(tr("ui.ff7b6ecc43a2"), processMemory+tr("ui.31f57e0b9ad5")),
		)
	}
	goHeap := tr("ui.e7c955a5ec17")
	if snapshot.Memory.GoHeap > 0 {
		goHeap = formatTUIUintBytes(snapshot.Memory.GoHeap)
	}
	return append(rows, tuiLabelField(tr("ui.34dc50a0cfa2"), goHeap))
}

func formatTUIUintBytes(value uint64) string {
	const maxInt64 = uint64(^uint64(0) >> 1)
	if value > maxInt64 {
		value = maxInt64
	}
	return formatBytes(int64(value))
}
