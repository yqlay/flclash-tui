//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

func drawTUIProfiles(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	tuiTitle(
		b,
		"Profiles",
		fmt.Sprintf(
			"%d available · Enter activate · U refresh linked · e edit · F2/u rename · x delete",
			len(snapshot.Profiles),
		),
		width,
	)
	limit := maxTUIWidth(height-3, 1)
	selectedPosition := snapshot.SelectedRow + tuiProfileImportRowCount
	start, end := tuiVisibleRange(
		len(snapshot.Profiles)+tuiProfileImportRowCount,
		selectedPosition,
		limit,
	)
	for position := start; position < end; position++ {
		if position == 0 {
			tuiRow(
				b,
				"+ Import subscription URL",
				width,
				snapshot.SelectedRow == tuiProfileImportSubscriptionRow &&
					!snapshot.FocusSidebar,
				tuiCyan,
			)
			continue
		}
		if position == 1 {
			tuiRow(
				b,
				"+ Import local profile file",
				width,
				snapshot.SelectedRow == tuiProfileImportFileRow &&
					!snapshot.FocusSidebar,
				tuiCyan,
			)
			continue
		}
		index := position - tuiProfileImportRowCount
		profile := snapshot.Profiles[index]
		label := truncateTUI(profile.Name, width-42)
		if profile.Current {
			if index == snapshot.SelectedRow && !snapshot.FocusSidebar {
				if profile.SubscriptionURL != "" {
					label += "  [active · U refresh · e edit · x locked]"
				} else {
					label += "  [active · local · e edit · x locked]"
				}
			} else {
				label += "  [active]"
			}
		} else if index == snapshot.SelectedRow && !snapshot.FocusSidebar {
			if profile.SubscriptionURL != "" {
				label += "  [Enter activate · U refresh · e edit · F2 rename · x delete]"
			} else {
				label += "  [Enter activate · local · e edit · F2 rename · x delete]"
			}
		}
		tuiRow(
			b,
			label,
			width,
			index == snapshot.SelectedRow && !snapshot.FocusSidebar,
			tuiGreen,
		)
	}
	tuiEndPanel(b, width)
}

func drawTUISSH(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	profileLimit := 5
	if height < 18 {
		profileLimit = 2
	} else if height < 33 {
		profileLimit = 4
	}
	tuiTitle(
		b,
		fmt.Sprintf("SSH profiles · %d configured", len(snapshot.SSHProfiles)),
		"↑↓ select · Enter connect · a probe/capture live SSH · n add · e edit · u default",
		width,
	)
	listLen := len(snapshot.SSHProfiles) + 1
	visual := snapshot.SelectedSSH + 1
	if visual < 0 {
		visual = 0
	}
	start, end := tuiVisibleRange(
		listLen,
		visual,
		minTUI(profileLimit, listLen),
	)
	listFocused := !snapshot.FocusSidebar && !snapshot.SSHDashboardFocus
	for visualIndex := start; visualIndex < end; visualIndex++ {
		if visualIndex == 0 {
			tuiRow(
				b,
				"Capture existing SSH     Enter probes live ControlMaster",
				width,
				snapshot.SelectedSSH == tuiSSHCaptureRow && listFocused,
				tuiCyan,
			)
			continue
		}
		index := visualIndex - 1
		if index < 0 || index >= len(snapshot.SSHProfiles) {
			continue
		}
		profile := snapshot.SSHProfiles[index]
		status := "DISCONNECTED"
		endpoint := ""
		color := tuiDim
		if profile.NeedsUsername {
			status = "NEEDS USER"
			endpoint = " · edit profile before connecting"
			color = tuiYellow
		} else if profile.Connected && profile.Ready {
			status = "CONNECTED"
			if profile.Attached {
				status = "ATTACHED"
			}
			configured := "auto"
			if profile.LocalPort > 0 {
				configured = strconv.Itoa(profile.LocalPort)
			}
			endpoint = fmt.Sprintf(
				" · SOCKS5 127.0.0.1:%d · configured %s",
				profile.SocksPort,
				configured,
			)
			color = tuiGreen
		} else if profile.Connected {
			status = "BROKEN"
			endpoint = fmt.Sprintf(
				" · SOCKS5 127.0.0.1:%d unavailable",
				profile.SocksPort,
			)
			color = tuiRed
		} else if profile.LocalPort > 0 {
			endpoint = fmt.Sprintf(" · local 127.0.0.1:%d", profile.LocalPort)
		} else {
			endpoint = " · local auto"
		}
		if profile.Jump != "" {
			endpoint += " · via " + profile.Jump
		}
		if profile.LastError != "" && !(profile.Connected && profile.Ready) {
			endpoint += " · " + truncateTUI(profile.LastError, 48)
		}
		auth := cliSSHAuthenticationLabel(
			profile.Identity,
			profile.PassphraseSet,
			profile.PasswordSet,
		)
		name := truncateTUI(profile.Name, 16)
		if profile.Default {
			name = "*" + truncateTUI(profile.Name, 15)
		}
		row := fmt.Sprintf(
			"%-18s %-12s %s:%d · %s%s",
			name,
			status,
			truncateTUI(profile.Destination, 28),
			profile.Port,
			auth,
			endpoint,
		)
		tuiRow(
			b,
			row,
			width,
			index == snapshot.SelectedSSH && listFocused,
			color,
		)
	}
	tuiEndPanel(b, width)
	drawTUISSHDashboard(b, snapshot, width, height, end-start)
}

func drawTUISSHDashboard(
	b *strings.Builder,
	snapshot tuiSnapshot,
	width,
	height,
	profileRows int,
) {
	if snapshot.SelectedSSH == tuiSSHCaptureRow {
		tuiTitle(
			b,
			"Capture existing SSH",
			"Enter probes now · a shortcut · Esc back",
			width,
		)
		tuiRow(b, "Probe runs only when you ask. Idle TUI refresh does not scan ControlMaster.", width, false, tuiCyan)
		tuiRow(b, "If a live multiplexed SSH exists, FlClash reuses it for SOCKS reverse proxy.", width, false, tuiDim)
		tuiRow(b, "Ordinary interactive ssh without ControlMaster cannot be captured.", width, false, tuiDim)
		tuiEndPanel(b, width)
		return
	}
	if snapshot.SelectedSSH < 0 || snapshot.SelectedSSH >= len(snapshot.SSHProfiles) {
		tuiEmptyPanel(b, "SSH Dashboard", "Add an SSH profile or capture a live ControlMaster", width)
		return
	}
	profile := snapshot.SSHProfiles[snapshot.SelectedSSH]
	status := "DISCONNECTED · Enter to connect · a probes for a live ControlMaster"
	statusColor := tuiDim
	if profile.NeedsUsername {
		status = "USERNAME REQUIRED · edit profile before connecting"
		statusColor = tuiYellow
	} else if profile.Connected && profile.Ready {
		status = fmt.Sprintf("CONNECTED · SOCKS5 127.0.0.1:%d · Enter to disconnect", profile.SocksPort)
		if profile.Attached {
			status = fmt.Sprintf("ATTACHED · SOCKS5 127.0.0.1:%d · Enter detaches FlClash only", profile.SocksPort)
		}
		statusColor = tuiGreen
	} else if profile.Connected {
		status = "BROKEN · Enter to reconnect"
		if profile.LastError != "" {
			status += " · " + profile.LastError
		}
		statusColor = tuiRed
	} else if profile.LastError != "" {
		status = "DISCONNECTED · Enter to connect · last error: " + profile.LastError
		statusColor = tuiYellow
	}
	if profile.Default && !profile.NeedsUsername {
		status = "DEFAULT · " + status
	}
	managedIP := tuiSSHNetworkLabel(snapshot.SSHNetwork, "Not checked · press n")
	directIP := tuiSSHNetworkLabel(snapshot.SSHDirectNetwork, "Not checked · press n")
	directState := tuiSSHDirectStateLabel(snapshot.SSHDirectProbe)
	if !snapshot.SSHDirectProbe.DirectAllowed {
		directIP = directState
	}
	focused := !snapshot.FocusSidebar && snapshot.SSHDashboardFocus
	if height < 18 {
		tuiTitle(
			b,
			"SSH Dashboard · "+profile.Name,
			"Tab focus · ↑↓ select · Enter run · n refresh · d RTT · v speed",
			width,
		)
		tuiRow(b, "Tunnel        "+status, width, focused && snapshot.SelectedSSHDetail == 0, statusColor)
		if snapshot.SelectedSSHDetail != 0 {
			label, color := tuiSSHSelectedDashboardRow(snapshot, managedIP, directIP, directState)
			tuiRow(b, label, width, focused, color)
		}
		writeTUIAnsiRow(b, "Traffic       "+formatTUITrafficLegend(snapshot.SSHTraffic, tuiTrafficPeak(snapshot.SSHTrafficHistory)), width)
		tuiEndPanel(b, width)
		return
	}

	tuiTitle(
		b,
		"SSH Dashboard · "+profile.Name,
		"Tab profiles/sidebar · ↑↓ select · Enter run · n refresh · d RTT · v speed",
		width,
	)
	tuiRow(b, "Tunnel        "+status, width, focused && snapshot.SelectedSSHDetail == 0, statusColor)
	tuiRow(b, "SSH host      "+profile.Destination+":"+strconv.Itoa(profile.Port), width, false, tuiCyan)
	if profile.Jump != "" {
		tuiRow(b, "Jump host     "+profile.Jump, width, false, tuiCyan)
	}
	tuiEndPanel(b, width)

	networkSubtitle := "B direct first · managed follows · direct requires B TUN off · d RTT×5 · v CF speed"
	if !snapshot.SSHNetwork.CheckedAt.IsZero() {
		networkSubtitle += " · checked " + snapshot.SSHNetwork.CheckedAt.Format("15:04:05")
	}
	tuiTitle(b, "Network detection", networkSubtitle, width)
	tuiRow(b, "A inet IP     "+tuiSSHIntranetLabel(snapshot.SSHNetwork.IntranetIP), width, false, tuiGreen)
	tuiRow(b, "B inet IP     "+tuiSSHRemoteIntranetLabel(snapshot.SSHDirectProbe), width, false, tuiGreen)
	tuiRow(b, "", width, false, "")
	tuiRow(b, "B direct exit "+directState, width, focused && snapshot.SelectedSSHDetail == tuiSSHDashboardDirectExitRow, tuiYellow)
	tuiRow(b, "B direct IP   "+directIP, width, false, tuiCyan)
	tuiRow(b, "Direct RTT    "+tuiDashboardDelayLabel(snapshot.SSHDirectDelay), width, focused && snapshot.SelectedSSHDetail == tuiSSHDashboardDirectRTTRow, tuiCyan)
	tuiRow(b, "Direct CF DL  "+tuiSpeedResultLabel(snapshot.SSHDirectSpeed), width, focused && snapshot.SelectedSSHDetail == tuiSSHDashboardDirectSpeedRow, tuiGreen)
	tuiRow(b, "", width, false, "")
	tuiRow(b, "B managed IP  "+managedIP, width, focused && snapshot.SelectedSSHDetail == tuiSSHDashboardManagedIPRow, tuiCyan)
	tuiRow(b, "Managed RTT   "+tuiDashboardDelayLabel(snapshot.SSHDelay), width, focused && snapshot.SelectedSSHDetail == tuiSSHDashboardManagedRTTRow, tuiCyan)
	tuiRow(b, "Managed CF DL "+tuiSpeedResultLabel(snapshot.SSHSpeed), width, focused && snapshot.SelectedSSHDetail == tuiSSHDashboardManagedSpeedRow, tuiGreen)
	tuiEndPanel(b, width)

	if height >= 27 {
		plotLimit := 6
		if height < 44 {
			plotLimit = 3
		}
		plotHeight := minTUI(maxTUIWidth(height-profileRows-22, 2), plotLimit)
		if height >= 36 {
			plotHeight = minTUI(maxTUIWidth(height-profileRows-23, 3), plotLimit)
		}
		chart := buildTUITrafficChart(snapshot.SSHTrafficHistory, maxTUIWidth(width-4, 1), plotHeight)
		tuiTrafficTitle(b, snapshot.SSHTraffic, chart.peak, width)
		for _, line := range chart.lines {
			writeTUIAnsiRow(b, line, width)
		}
		tuiEndPanel(b, width)
	}

	if height < 36 {
		return
	}
	uptime := "not connected"
	if profile.Connected && !profile.StartedAt.IsZero() {
		uptime = time.Since(profile.StartedAt).Round(time.Second).String()
	}
	tuiTitle(b, "Overview", "SSH relay metering · live status", width)
	tuiRow(b, fmt.Sprintf("Traffic total ↑ %s   ↓ %s", formatBytes(snapshot.SSHTotalTraffic.Up), formatBytes(snapshot.SSHTotalTraffic.Down)), width, false, "")
	tuiRow(b, fmt.Sprintf("Connections   %d active", snapshot.SSHConnections), width, false, "")
	tuiRow(b, "Uptime        "+uptime, width, false, "")
	tuiRow(b, "Scope         only traffic through this SSH SOCKS5 port", width, false, tuiDim)
	tuiEndPanel(b, width)
}

func tuiSSHNetworkLabel(info tuiNetworkInfo, empty string) string {
	if info.Loading {
		return "Checking through SSH exit..."
	}
	if info.PublicIP != "" {
		value := info.PublicIP
		if info.Country != "" {
			value += "  [" + info.Country + "]"
		}
		return value
	}
	if info.Error != "" {
		return "Unavailable · " + info.Error
	}
	return empty
}

func tuiSSHDirectStateLabel(probe cliSSHRemoteProbe) string {
	switch {
	case probe.DirectAllowed:
		return "READY · remote FlClash TUN off"
	case probe.ProtocolVersion == 0 && probe.Reason == "":
		return "Not checked · press n"
	case probe.Reason != "":
		return "BLOCKED · " + probe.Reason
	default:
		return "BLOCKED · remote direct state unavailable"
	}
}

func tuiSSHSelectedDashboardRow(
	snapshot tuiSnapshot,
	managedIP,
	directIP,
	directState string,
) (string, string) {
	switch snapshot.SelectedSSHDetail {
	case tuiSSHDashboardDirectExitRow:
		return "B direct exit " + directState, tuiYellow
	case tuiSSHDashboardDirectRTTRow:
		return "Direct RTT    " + tuiDashboardDelayLabel(snapshot.SSHDirectDelay), tuiCyan
	case tuiSSHDashboardDirectSpeedRow:
		return "Direct CF DL  " + tuiSpeedResultLabel(snapshot.SSHDirectSpeed), tuiGreen
	case tuiSSHDashboardManagedIPRow:
		return "B managed IP  " + managedIP, tuiCyan
	case tuiSSHDashboardManagedRTTRow:
		return "Managed RTT   " + tuiDashboardDelayLabel(snapshot.SSHDelay), tuiCyan
	case tuiSSHDashboardManagedSpeedRow:
		return "Managed CF DL " + tuiSpeedResultLabel(snapshot.SSHSpeed), tuiGreen
	default:
		return "A inet IP     " + tuiSSHIntranetLabel(snapshot.SSHNetwork.IntranetIP), tuiGreen
	}
}

func tuiSSHDashboardRowIsDirect(row int) bool {
	return row >= tuiSSHDashboardDirectExitRow &&
		row <= tuiSSHDashboardDirectSpeedRow
}

func tuiSSHIntranetLabel(value string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return "Not checked · press n"
}

func tuiSSHRemoteIntranetLabel(probe cliSSHRemoteProbe) string {
	if strings.TrimSpace(probe.IntranetIP) != "" {
		return probe.IntranetIP
	}
	if probe.ProtocolVersion == 0 && probe.Reason == "" {
		return "Not checked · press n"
	}
	if probe.Reason != "" {
		return "Unavailable · " + probe.Reason
	}
	return "Unavailable · remote FlClash did not report an inet IP"
}

func tuiTrafficPeak(history []trafficSnapshot) int64 {
	var peak int64
	for _, sample := range history {
		peak = maxTUIInt64(peak, maxTUIInt64(sample.Up, sample.Down))
	}
	return peak
}

func truncateTUI(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if tuiDisplayWidth(value) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	var b strings.Builder
	visibleWidth := 0
	for _, runeValue := range value {
		runeWidth := tuiRuneWidth(runeValue)
		if visibleWidth+runeWidth > width-1 {
			break
		}
		b.WriteRune(runeValue)
		visibleWidth += runeWidth
	}
	b.WriteRune('…')
	return b.String()
}

func formatBytes(value int64) string {
	units := []string{"B", "KB", "MB", "GB"}
	amount := float64(value)
	unit := 0
	for amount >= 1024 && unit < len(units)-1 {
		amount /= 1024
		unit++
	}
	return fmt.Sprintf("%.1f %s", amount, units[unit])
}
