//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"strings"
	"time"
)

func drawTUIProfiles(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	var selectedSubscription *tuiProfile
	if snapshot.SelectedRow >= 0 && snapshot.SelectedRow < len(snapshot.Profiles) {
		profile := &snapshot.Profiles[snapshot.SelectedRow]
		if profile.SubscriptionURL != "" && height >= 12 {
			selectedSubscription = profile
		}
	}
	detailHeight := 0
	if selectedSubscription != nil {
		detailHeight = 5
		if selectedSubscription.SubscriptionInfo != nil {
			detailHeight = 7
		}
	}
	tuiTitle(
		b,
		"Profiles",
		fmt.Sprintf(
			"%d available · Enter activate · U refresh linked · e edit · F2/u rename · x delete",
			len(snapshot.Profiles),
		),
		width,
	)
	limit := maxTUIWidth(height-3-detailHeight, 1)
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
		if index != snapshot.SelectedRow && profile.SubscriptionInfo != nil {
			label += "  [" + tuiSubscriptionCompact(profile.SubscriptionInfo) + "]"
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
	if selectedSubscription != nil {
		drawTUISubscriptionInfo(b, *selectedSubscription, width)
	}
}

func tuiSubscriptionCompact(info *tuiSubscriptionInfo) string {
	if info.Total != nil && *info.Total == 0 {
		return "unlimited"
	}
	if used, known := tuiSubscriptionUsed(info); known && info.Total != nil {
		return formatBytes(used) + "/" + formatBytes(*info.Total)
	}
	if used, known := tuiSubscriptionUsed(info); known {
		return "used " + formatBytes(used)
	}
	if info.Total != nil {
		return "limit " + formatBytes(*info.Total)
	}
	if info.Expire != nil {
		return "expires " + tuiSubscriptionExpiry(info)
	}
	return "subscription info"
}

func drawTUISubscriptionInfo(b *strings.Builder, profile tuiProfile, width int) {
	tuiTitle(b, "Subscription · "+profile.Name, "from provider response · U refresh", width)
	info := profile.SubscriptionInfo
	if info == nil {
		tuiRow(b, "Usage and expiry not provided by subscription server", width, false, tuiDim)
		tuiRow(b, "The YAML file alone cannot supply these account details", width, false, tuiDim)
		tuiEndPanel(b, width)
		return
	}
	used, usedKnown := tuiSubscriptionUsed(info)
	usedLabel := "not provided"
	if usedKnown {
		usedLabel = formatBytes(used)
	}
	if info.Upload != nil && info.Download != nil {
		usedLabel += " (up " + formatBytes(*info.Upload) + " · down " + formatBytes(*info.Download) + ")"
	}
	tuiRow(b, "Used      "+usedLabel, width, false, tuiCyan)
	quota := "not provided"
	quotaColor := tuiDim
	if info.Total != nil {
		quotaColor = tuiGreen
		if *info.Total == 0 {
			quota = "unlimited"
		} else {
			quota = formatBytes(*info.Total)
			if usedKnown {
				remaining := int64(0)
				if used < *info.Total {
					remaining = *info.Total - used
				}
				quota += " · " + formatBytes(remaining) + " left"
				if remaining == 0 {
					quotaColor = tuiRed
				}
			}
		}
	}
	tuiRow(b, "Quota     "+quota, width, false, quotaColor)
	expiry := tuiSubscriptionExpiry(info)
	expiryColor := tuiDim
	if info.Expire != nil {
		expiryColor = tuiGreen
		if *info.Expire > 0 && time.Now().After(time.Unix(*info.Expire, 0)) {
			expiryColor = tuiRed
		}
	}
	tuiRow(b, "Expires   "+expiry, width, false, expiryColor)
	checked := "not recorded"
	if !info.FetchedAt.IsZero() {
		checked = info.FetchedAt.Local().Format("2006-01-02 15:04")
	}
	tuiRow(b, "Checked   "+checked, width, false, tuiDim)
	tuiEndPanel(b, width)
}

func drawTUISSH(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	profileLimit := 5
	if height < 24 {
		profileLimit = 2
	} else if height < 33 {
		profileLimit = 4
	}
	tuiTitle(
		b,
		fmt.Sprintf("SSH profiles · %d", len(snapshot.SSHProfiles)),
		"↑↓ select · Enter open/connect · a Capture · n add · e edit · x delete",
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
			status := "—"
			color := tuiCyan
			if snapshot.SSHCaptureKnown {
				if snapshot.SSHCaptureFound == 0 {
					status = "NONE"
					color = tuiDim
				} else {
					status = fmt.Sprintf("%d LIVE", snapshot.SSHCaptureFound)
					color = tuiGreen
				}
			}
			row := fmt.Sprintf("%-18s %-12s Enter · SOCKS on THIS machine", "Capture", status)
			tuiRow(
				b,
				row,
				width,
				snapshot.SelectedSSH == tuiSSHCaptureRow && listFocused,
				color,
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
			endpoint = fmt.Sprintf(" · SOCKS5 127.0.0.1:%d", profile.SocksPort)
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
		if profile.LastError != "" && !(profile.Connected && profile.Ready) {
			endpoint += " · " + truncateTUI(profile.LastError, 36)
		}
		name := truncateTUI(profile.Name, 16)
		if profile.Default {
			name = "*" + truncateTUI(profile.Name, 15)
		}
		row := fmt.Sprintf(
			"%-18s %-12s %s:%d%s",
			name,
			status,
			truncateTUI(profile.Destination, 28),
			profile.Port,
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
	if snapshot.SSHDetailName == "" {
		return
	}
	index := findTUISSHProfile(snapshot.SSHProfiles, snapshot.SSHDetailName)
	if index < 0 {
		return
	}
	profile := snapshot.SSHProfiles[index]
	drawTUISSHDetails(b, snapshot, profile, width, height, profileRows)
}

func tuiSSHTunnelStatus(profile tuiSSHProfile) (string, string) {
	status := "DISCONNECTED · Enter connect · a Capture"
	statusColor := tuiDim
	if profile.NeedsUsername {
		status = "USERNAME REQUIRED · edit profile before connecting"
		statusColor = tuiYellow
	} else if profile.Connected && profile.Ready {
		status = fmt.Sprintf("CONNECTED · SOCKS5 127.0.0.1:%d · Enter to disconnect", profile.SocksPort)
		if profile.Attached {
			status = fmt.Sprintf("ATTACHED · exit via %s · SOCKS5 127.0.0.1:%d · Enter detach", tuiSSHExitHost(profile), profile.SocksPort)
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
	return status, statusColor
}

func drawTUISSHDetails(b *strings.Builder, snapshot tuiSnapshot, profile tuiSSHProfile, width, height, profileRows int) {
	status, statusColor := tuiSSHTunnelStatus(profile)
	focused := !snapshot.FocusSidebar && snapshot.SSHDashboardFocus
	subtitle := "Tab focus · Enter connect"
	if profile.Connected && profile.Ready {
		subtitle = "Tab focus · Enter tunnel · n refresh"
	}
	tuiTitle(b, "SSH · "+profile.Name, subtitle, width)
	tuiRow(b, "Tunnel        "+status, width, focused, statusColor)
	if !profile.Connected || !profile.Ready {
		tuiEndPanel(b, width)
		return
	}
	inetIP := tuiSSHRemoteIntranetLabel(snapshot.SSHDirectProbe)
	if profile.SocksOnly {
		inetIP = "Unavailable · captured SOCKS5"
	}
	tuiRow(b, "Proxy Inet IP  "+inetIP, width, false, tuiGreen)
	tuiRow(b, "Proxy IP       "+tuiSSHNetworkLabel(snapshot.SSHNetwork, "Not checked · press n"), width, false, tuiCyan)
	writeTUIAnsiRow(b, fmt.Sprintf(
		"Speed          %s↓ %s/s%s · %s↑ %s/s%s",
		tuiTrafficChartDownload,
		formatBytes(snapshot.SSHTraffic.Down),
		tuiReset,
		tuiTrafficChartUpload,
		formatBytes(snapshot.SSHTraffic.Up),
		tuiReset,
	), width)
	tuiEndPanel(b, width)

	chartHeight := height - (profileRows + 3) - 7 - 3
	if chartHeight < 1 {
		return
	}
	chart := buildTUITrafficChart(snapshot.SSHTrafficHistory, maxTUIWidth(width-4, 1), chartHeight)
	tuiTitle(b, "Traffic history", fmt.Sprintf("SSH relay only · peak %s/s · 30 samples", formatBytes(chart.peak)), width)
	for _, line := range chart.lines {
		writeTUIAnsiRow(b, line, width)
	}
	tuiEndPanel(b, width)
}

func tuiSSHExitHost(profile tuiSSHProfile) string {
	if profile.Reverse {
		return "SSH client (-R)"
	}
	return "SSH server"
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
