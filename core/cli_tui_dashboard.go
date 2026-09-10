//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"strings"
)

func drawTUIDashboard(b *strings.Builder, snapshot tuiSnapshot, paths cliPaths, width, height int) {
	serviceLabel := tuiServiceLabel(snapshot)
	systemProxyLabel := tuiSystemProxyLabel(snapshot)
	controls := []string{
		fmt.Sprintf("Core          %s", serviceLabel),
		fmt.Sprintf("System proxy  %s", systemProxyLabel),
		fmt.Sprintf("TUN           %s", tuiTUNLabel(snapshot)),
		fmt.Sprintf("Mode          %s", snapshot.Settings.Mode),
		fmt.Sprintf("flc           %s", tuiFLCOutboundLabel(snapshot)),
		fmt.Sprintf("Proxy port    %s", tuiProxyPortLabel(snapshot)),
	}
	tuiTitle(
		b,
		"Dashboard",
		"Current state shown first · Enter changes selected item",
		width,
	)
	for index, row := range controls {
		tuiRow(
			b,
			row,
			width,
			index == snapshot.SelectedDashboard && !snapshot.FocusSidebar,
			"",
		)
	}
	tuiEndPanel(b, width)

	publicIP := "Checking..."
	if snapshot.Network.PublicIP != "" {
		publicIP = snapshot.Network.PublicIP
		if snapshot.Network.Country != "" {
			publicIP += "  [" + snapshot.Network.Country + "]"
		}
		if snapshot.Network.Loading {
			publicIP += "  refreshing..."
		}
	} else if snapshot.Network.Error != "" && !snapshot.Network.Loading {
		publicIP = "Unavailable · press n to retry"
	}
	intranetIP := snapshot.Network.IntranetIP
	if intranetIP == "" {
		if snapshot.Network.Loading {
			intranetIP = "Detecting..."
		} else {
			intranetIP = "No active LAN address"
		}
	}
	networkSubtitle := ""
	if snapshot.Network.Route != "" {
		networkSubtitle = snapshot.Network.Route + " · "
	}
	networkSubtitle += "d " + strings.ToLower(tuiDashboardRouteName(snapshot)) +
		" RTT×5 · v CF speed · n refresh"
	if !snapshot.Network.CheckedAt.IsZero() {
		networkSubtitle += " · checked " + snapshot.Network.CheckedAt.Format("15:04:05")
	}
	tuiTitle(b, "Network detection", networkSubtitle, width)
	tuiRow(b, "Public IP     "+publicIP, width, false, tuiCyan)
	tuiRow(b, "Intranet IP   "+intranetIP, width, false, tuiGreen)
	tuiRow(
		b,
		tuiPadRight(tuiDashboardRouteName(snapshot), 15)+
			tuiDashboardDelayLabel(snapshot.DashboardDelay),
		width,
		snapshot.SelectedDashboard == tuiDashboardDelayRow &&
			!snapshot.FocusSidebar,
		tuiCyan,
	)
	tuiRow(
		b,
		"Cloudflare DL  "+tuiSpeedResultLabel(snapshot.DashboardSpeed),
		width,
		snapshot.SelectedDashboard == tuiDashboardSpeedRow &&
			!snapshot.FocusSidebar,
		tuiGreen,
	)
	tuiEndPanel(b, width)

	if height >= 33 {
		plotHeight := minTUI(maxTUIWidth(height-30, 3), 6)
		chart := buildTUITrafficChart(
			snapshot.TrafficHistory,
			maxTUIWidth(width-4, 1),
			plotHeight,
		)
		tuiTrafficTitle(b, snapshot.Traffic, chart.peak, width)
		for _, line := range chart.lines {
			writeTUIAnsiRow(b, line, width)
		}
		tuiEndPanel(b, width)
	}

	if height >= 17 {
		overview := tuiMemoryRows(snapshot)
		overview = append(overview,
			fmt.Sprintf(
				"Network speed ↑ %s/s   ↓ %s/s",
				formatBytes(snapshot.Traffic.Up),
				formatBytes(snapshot.Traffic.Down),
			),
			fmt.Sprintf(
				"Traffic total ↑ %s   ↓ %s",
				formatBytes(snapshot.TotalTraffic.Up),
				formatBytes(snapshot.TotalTraffic.Down),
			),
			fmt.Sprintf(
				"Activity      %d active · %d history entries",
				len(snapshot.Connections),
				len(snapshot.Requests),
			),
			"TUI frontends "+formatCLIFrontendSummary(snapshot.Frontends),
			fmt.Sprintf("Config        %s", paths.ConfigPath),
		)
		tuiTitle(
			b,
			"Overview",
			"memory refresh "+tuiMemoryRefreshInterval.String()+" · live status",
			width,
		)
		for _, row := range overview {
			tuiRow(b, row, width, false, "")
		}
		tuiEndPanel(b, width)
	}
}

type tuiDashboardCompactRow struct {
	value    string
	color    string
	selected bool
	ansi     bool
}

func renderTUICompactDashboard(
	snapshot tuiSnapshot,
	paths cliPaths,
	width,
	height int,
) string {
	rows := tuiCompactDashboardRows(snapshot, paths, width, height)
	limit := maxTUIWidth(height-3, 1)
	maxStart := maxTUIIndex(len(rows) - limit)
	start := snapshot.DashboardScroll
	if start > maxStart {
		start = maxStart
	}
	if start < 0 {
		start = 0
	}
	end := minTUI(start+limit, len(rows))
	var b strings.Builder
	tuiTitle(
		&b,
		"Dashboard",
		fmt.Sprintf(
			"rows %d-%d/%d · PgUp/PgDn scroll",
			start+1,
			end,
			len(rows),
		),
		width,
	)
	for _, row := range rows[start:end] {
		if row.ansi {
			writeTUIAnsiRow(&b, row.value, width)
			continue
		}
		tuiRow(
			&b,
			row.value,
			width,
			row.selected,
			row.color,
		)
	}
	tuiEndPanel(&b, width)
	return b.String()
}

func tuiCompactDashboardRows(
	snapshot tuiSnapshot,
	paths cliPaths,
	width int,
	height int,
) []tuiDashboardCompactRow {
	controls := []string{
		fmt.Sprintf("Core          %s", tuiServiceLabel(snapshot)),
		fmt.Sprintf(
			"System proxy  %s",
			tuiSystemProxyLabel(snapshot),
		),
		fmt.Sprintf("TUN           %s", tuiTUNLabel(snapshot)),
		fmt.Sprintf("Mode          %s", snapshot.Settings.Mode),
		fmt.Sprintf("flc           %s", tuiFLCOutboundLabel(snapshot)),
		fmt.Sprintf("Proxy port    %s", tuiProxyPortLabel(snapshot)),
	}
	rows := make([]tuiDashboardCompactRow, 0, 28)
	for index, control := range controls {
		rows = append(rows, tuiDashboardCompactRow{
			value: control,
			selected: index == snapshot.SelectedDashboard &&
				!snapshot.FocusSidebar,
		})
	}
	chart := buildTUITrafficChart(
		snapshot.TrafficHistory,
		maxTUIWidth(width-4, 1),
		tuiCompactTrafficChartHeight(height),
	)
	rows = append(rows, tuiDashboardCompactRow{
		value: tuiCyan + "── Live traffic" + tuiReset + " · " +
			formatTUITrafficLegend(snapshot.Traffic, chart.peak),
		ansi: true,
	})
	for _, line := range chart.lines {
		rows = append(rows, tuiDashboardCompactRow{value: line, ansi: true})
	}

	publicIP := "Checking..."
	if snapshot.Network.PublicIP != "" {
		publicIP = snapshot.Network.PublicIP
		if snapshot.Network.Country != "" {
			publicIP += " [" + snapshot.Network.Country + "]"
		}
	} else if snapshot.Network.Error != "" &&
		!snapshot.Network.Loading {
		publicIP = "Unavailable · n retry"
	}
	intranetIP := snapshot.Network.IntranetIP
	if intranetIP == "" {
		if snapshot.Network.Loading {
			intranetIP = "Detecting..."
		} else {
			intranetIP = "No active LAN address"
		}
	}
	rows = append(
		rows,
		tuiDashboardCompactRow{
			value: "── Network · d " +
				strings.ToLower(tuiDashboardRouteName(snapshot)) +
				" latency · v Cloudflare speed · n refresh",
			color: tuiCyan,
		},
		tuiDashboardCompactRow{
			value: "Public IP     " + publicIP,
			color: tuiCyan,
		},
		tuiDashboardCompactRow{
			value: "Intranet IP   " + intranetIP,
			color: tuiGreen,
		},
		tuiDashboardCompactRow{
			value: tuiPadRight(tuiDashboardRouteName(snapshot), 15) +
				tuiDashboardDelayLabel(snapshot.DashboardDelay),
			color: tuiCyan,
			selected: snapshot.SelectedDashboard == tuiDashboardDelayRow &&
				!snapshot.FocusSidebar,
		},
		tuiDashboardCompactRow{
			value: "Cloudflare DL  " +
				tuiSpeedResultLabel(snapshot.DashboardSpeed),
			color: tuiGreen,
			selected: snapshot.SelectedDashboard == tuiDashboardSpeedRow &&
				!snapshot.FocusSidebar,
		},
		tuiDashboardCompactRow{
			value: "── Overview · live status",
			color: tuiCyan,
		},
	)
	for _, row := range tuiMemoryRows(snapshot) {
		rows = append(rows, tuiDashboardCompactRow{value: row})
	}
	rows = append(
		rows,
		tuiDashboardCompactRow{
			value: fmt.Sprintf(
				"Network speed ↑ %s/s ↓ %s/s",
				formatBytes(snapshot.Traffic.Up),
				formatBytes(snapshot.Traffic.Down),
			),
		},
		tuiDashboardCompactRow{
			value: fmt.Sprintf(
				"Traffic total ↑ %s ↓ %s",
				formatBytes(snapshot.TotalTraffic.Up),
				formatBytes(snapshot.TotalTraffic.Down),
			),
		},
		tuiDashboardCompactRow{
			value: fmt.Sprintf(
				"Activity      %d active · %d history entries",
				len(snapshot.Connections),
				len(snapshot.Requests),
			),
		},
		tuiDashboardCompactRow{
			value: "TUI frontends " +
				formatCLIFrontendSummary(snapshot.Frontends),
		},
		tuiDashboardCompactRow{
			value: "Config        " + paths.ConfigPath,
		},
	)
	return rows
}

func tuiDashboardRouteName(snapshot tuiSnapshot) string {
	if strings.EqualFold(snapshot.Settings.Mode, tuiSilentMode) {
		return "Silent route"
	}
	return "Rule route"
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

func tuiDashboardDelayLabel(delay tuiDelayResult) string {
	switch {
	case delay.MedianMillis > 0:
		return formatTUIDelay(delay) + " · d retest"
	case delay.Error != "":
		return "Timeout · d retry"
	case delay.Testing:
		return "Testing..."
	default:
		return "Not tested · press d"
	}
}

func formatTUIDelay(result tuiDelayResult) string {
	if result.Samples <= 1 {
		return fmt.Sprintf("%d ms", result.MedianMillis)
	}
	return fmt.Sprintf(
		"%d ms · jitter %d ms · %d samples",
		result.MedianMillis,
		result.JitterMillis,
		result.Samples,
	)
}

func tuiSpeedResultLabel(result tuiSpeedResult) string {
	switch {
	case result.Testing:
		return "Testing 4 streams · up to 100 MB/5s..."
	case result.Error != "":
		return "Failed · v retry"
	case result.BytesPerSecond > 0:
		downloaded := float64(result.Bytes) / 1_000_000
		duration := float64(result.DurationMillis) / 1000
		return fmt.Sprintf(
			"%s · %.1f MB in %.2fs · v retest",
			formatTUISpeed(result),
			downloaded,
			duration,
		)
	default:
		return "Not tested · press v"
	}
}

func tuiMemoryRows(snapshot tuiSnapshot) []string {
	systemMemory := "Unavailable"
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
	rows := []string{"System memory " + systemMemory}
	if snapshot.ExternalCore || snapshot.ManagedService {
		processMemory := "Measuring..."
		if snapshot.Memory.ProcessRSS > 0 {
			processMemory = formatTUIUintBytes(snapshot.Memory.ProcessRSS)
		}
		coreMemory := "Measuring..."
		if snapshot.Memory.CoreRSS > 0 {
			coreMemory = formatTUIUintBytes(snapshot.Memory.CoreRSS)
		} else if snapshot.Memory.CoreError != "" {
			coreMemory = "Unavailable · retrying"
		}
		rows = append(
			rows,
			"TUI process   "+processMemory,
			func() string {
				if snapshot.ManagedService {
					return "Managed Core  " + coreMemory
				}
				return "External Core " + coreMemory
			}(),
		)
	} else {
		processMemory := "Measuring..."
		if snapshot.Memory.ProcessRSS > 0 {
			processMemory = formatTUIUintBytes(snapshot.Memory.ProcessRSS)
		}
		rows = append(
			rows,
			"CLI + Mihomo  "+processMemory+" RSS · shared process",
		)
	}
	goHeap := "Measuring..."
	if snapshot.Memory.GoHeap > 0 {
		goHeap = formatTUIUintBytes(snapshot.Memory.GoHeap)
	}
	return append(rows, "Go heap       "+goHeap)
}

func formatTUIUintBytes(value uint64) string {
	const maxInt64 = uint64(^uint64(0) >> 1)
	if value > maxInt64 {
		value = maxInt64
	}
	return formatBytes(int64(value))
}
