//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"strings"
	"time"
)

func drawTUIProfiles(b *strings.Builder, snapshot tuiSnapshot, width, height int, language ...string) {
	tr := tuiTranslator(language...)
	var selectedSubscription *tuiProfile
	if snapshot.SelectedRow >= 0 && snapshot.SelectedRow < len(snapshot.Profiles) {
		profile := &snapshot.Profiles[snapshot.SelectedRow]
		if profile.SubscriptionURL != "" && height >= 12 {
			selectedSubscription = profile
		}
	}
	detailHeight := 0
	var detail strings.Builder
	if selectedSubscription != nil {
		drawTUISubscriptionInfo(&detail, *selectedSubscription, width, language...)
		detailHeight = strings.Count(detail.String(), "\n")
		if detailHeight+4 > height {
			selectedSubscription = nil
			detailHeight = 0
		}
	}
	tuiTitle(
		b, tr("ui.535e52e4a261"), fmt.Sprintf(tr("ui.72464fb89303"), len(snapshot.Profiles)),
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
				b, tr("ui.43ae357e8aa7"), width,
				snapshot.SelectedRow == tuiProfileImportSubscriptionRow &&
					!snapshot.FocusSidebar,
				tuiCyan,
			)
			continue
		}
		if position == 1 {
			tuiRow(
				b, tr("ui.c3e1844dae7a"), width,
				snapshot.SelectedRow == tuiProfileImportFileRow &&
					!snapshot.FocusSidebar,
				tuiCyan,
			)
			continue
		}
		index := position - tuiProfileImportRowCount
		profile := snapshot.Profiles[index]
		label := ""
		if profile.Current {
			if index == snapshot.SelectedRow && !snapshot.FocusSidebar {
				if profile.SubscriptionURL != "" {
					label += tr("ui.2595c88775d7")
				} else {
					label += tr("ui.4d99215ed3e9")
				}
			} else {
				label += tr("ui.372ff0acb440")
			}
		} else if index == snapshot.SelectedRow && !snapshot.FocusSidebar {
			if profile.SubscriptionURL != "" {
				label += tr("ui.e0c618bce580")
			} else {
				label += tr("ui.7517b93565f6")
			}
		}
		if index != snapshot.SelectedRow && profile.SubscriptionInfo != nil {
			label += "  [" + tuiSubscriptionCompact(profile.SubscriptionInfo, language...) + "]"
		}
		label = tuiColumns(width-4, tuiColumn{value: profile.Name, width: maxTUIWidth(width-4-tuiDisplayWidth(label), 12), minimum: 12}, tuiColumn{value: strings.TrimSpace(label), width: tuiDisplayWidth(strings.TrimSpace(label)), minimum: 0, optional: true})
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
		b.WriteString(detail.String())
	}
}

func tuiSubscriptionCompact(info *tuiSubscriptionInfo, language ...string) string {
	tr := tuiTranslator(language...)
	if info.Total != nil && *info.Total == 0 {
		return tr("ui.2044fbdb55f9")
	}
	if used, known := tuiSubscriptionUsed(info); known && info.Total != nil {
		return formatBytes(used) + "/" + formatBytes(*info.Total)
	}
	if used, known := tuiSubscriptionUsed(info); known {
		return tr("ui.93b39383c7ed") + formatBytes(used)
	}
	if info.Total != nil {
		return tr("ui.70792dfe47ec") + formatBytes(*info.Total)
	}
	if info.Expire != nil {
		return tr("ui.22206a2566e5") + tuiSubscriptionExpiry(info)
	}
	return tr("ui.fe9870f4bb17")
}

func drawTUISubscriptionInfo(b *strings.Builder, profile tuiProfile, width int, language ...string) {
	tr := tuiTranslator(language...)
	tuiTitle(b, tr("ui.429bd45afeeb")+profile.Name, tr("ui.b7c88ba2ac64"), width)
	info := profile.SubscriptionInfo
	if info == nil {
		tuiRow(b, tr("ui.b62aa03d125c"), width, false, tuiDim)
		tuiRow(b, tr("ui.33c04a8d8365"), width, false, tuiDim)
		tuiEndPanel(b, width)
		return
	}
	used, usedKnown := tuiSubscriptionUsed(info)
	usedLabel := tr("ui.1c9e6f54a903")
	if usedKnown {
		usedLabel = formatBytes(used)
	}
	if info.Upload != nil && info.Download != nil {
		usedLabel += tr("ui.45122f2b66c1") + formatBytes(*info.Upload) + tr("ui.dc5b03f829cd") + formatBytes(*info.Download) + ")"
	}
	fields := []tuiField{tuiLabelField(tr("ui.ef85e6bc5f5b"), usedLabel)}
	fields[0].color = tuiCyan
	quota := tr("ui.1c9e6f54a903")
	quotaColor := tuiDim
	if info.Total != nil {
		quotaColor = tuiGreen
		if *info.Total == 0 {
			quota = tr("ui.2044fbdb55f9")
		} else {
			quota = formatBytes(*info.Total)
			if usedKnown {
				remaining := int64(0)
				if used < *info.Total {
					remaining = *info.Total - used
				}
				quota += " · " + formatBytes(remaining) + tr("ui.bce319cfa7b7")
				if remaining == 0 {
					quotaColor = tuiRed
				}
			}
		}
	}
	fields = append(fields, tuiLabelField(tr("ui.1caf9b0b1b33"), quota))
	fields[1].color = quotaColor
	expiry := tuiSubscriptionExpiry(info)
	expiryColor := tuiDim
	if info.Expire != nil {
		expiryColor = tuiGreen
		if *info.Expire > 0 && time.Now().After(time.Unix(*info.Expire, 0)) {
			expiryColor = tuiRed
		}
	}
	fields = append(fields, tuiLabelField(tr("ui.ba568fcde4b2"), expiry))
	fields[2].color = expiryColor
	checked := tr("ui.ea80f83bbd64")
	if !info.FetchedAt.IsZero() {
		checked = info.FetchedAt.Local().Format("2006-01-02 15:04")
	}
	fields = append(fields, tuiLabelField(tr("ui.c8b88d0ec5d9"), checked))
	fields[3].color = tuiDim
	tuiWriteRows(b, tuiFieldRows(fields, width), width)
	tuiEndPanel(b, width)
}

func drawTUISSH(b *strings.Builder, snapshot tuiSnapshot, width, height int, language ...string) {
	tr := tuiTranslator(language...)
	profileLimit := 5
	if height < 24 {
		profileLimit = 2
	} else if height < 33 {
		profileLimit = 4
	}
	if snapshot.SSHDetailName != "" {
		profileLimit = minTUI(profileLimit, maxTUIWidth(height-7, 1))
	}
	tuiTitle(
		b,
		fmt.Sprintf(tr("ui.e0a7eec09c2f"), len(snapshot.SSHProfiles)), tr("ui.79403c9f1ae4"), width,
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
	statusWidth := 12
	for _, profile := range snapshot.SSHProfiles {
		statusWidth = maxTUIWidth(statusWidth, tuiDisplayWidth(tuiSSHListStatus(profile, language...)))
	}
	if snapshot.SSHCaptureKnown {
		captureStatus := tr("ui.c627c09c14e5")
		if snapshot.SSHCaptureFound > 0 {
			captureStatus = fmt.Sprintf(tr("ui.d0782e498713"), snapshot.SSHCaptureFound)
		}
		statusWidth = maxTUIWidth(statusWidth, tuiDisplayWidth(captureStatus))
	}
	for visualIndex := start; visualIndex < end; visualIndex++ {
		if visualIndex == 0 {
			status := "—"
			color := tuiCyan
			if snapshot.SSHCaptureKnown {
				if snapshot.SSHCaptureFound == 0 {
					status = tr("ui.c627c09c14e5")
					color = tuiDim
				} else {
					status = fmt.Sprintf(tr("ui.d0782e498713"), snapshot.SSHCaptureFound)
					color = tuiGreen
				}
			}
			row := tuiColumns(width-4,
				tuiColumn{value: tr("ui.1900b478a586"), width: 18, minimum: 8},
				tuiColumn{value: status, width: statusWidth, minimum: statusWidth},
				tuiColumn{value: tr("layout.capture_hint"), width: 32, minimum: 8, optional: true},
			)
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
		status := tr("ui.5541de951552")
		endpoint := ""
		color := tuiDim
		if profile.NeedsUsername {
			status = tr("ui.58be0d89a72e")
			endpoint = tr("ui.80cdfd3e3379")
			color = tuiYellow
		} else if profile.Connected && profile.Ready {
			status = tr("ui.1f914c4386c0")
			if profile.Attached {
				status = tr("ui.9448242431c8")
			}
			endpoint = fmt.Sprintf(tr("ui.89f09a77f737"), profile.SocksPort)
			color = tuiGreen
		} else if profile.Connected {
			status = tr("ui.d84771511dbe")
			endpoint = fmt.Sprintf(tr("ui.5eec4815da1c"), profile.SocksPort)
			color = tuiRed
		} else if profile.LocalPort > 0 {
			endpoint = fmt.Sprintf(tr("ui.4b8293b7a463"), profile.LocalPort)
		} else {
			endpoint = tr("ui.d9b076d2ba45")
		}
		if profile.LastError != "" && !(profile.Connected && profile.Ready) {
			endpoint += " · " + truncateTUI(profile.LastError, 36)
		}
		name := truncateTUI(profile.Name, 16)
		if profile.Default {
			name = "*" + truncateTUI(profile.Name, 15)
		}
		row := tuiColumns(width-4,
			tuiColumn{value: name, width: 18, minimum: 8},
			tuiColumn{value: status, width: statusWidth, minimum: statusWidth},
			tuiColumn{value: fmt.Sprintf("%s:%d%s", profile.Destination, profile.Port, endpoint), width: maxTUIWidth(width-24-statusWidth, 8), minimum: 8, optional: true},
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
	drawTUISSHDashboard(b, snapshot, width, height, end-start, language...)
}

func tuiSSHListStatus(profile tuiSSHProfile, language ...string) string {
	tr := tuiTranslator(language...)
	if profile.NeedsUsername {
		return tr("ui.58be0d89a72e")
	}
	if profile.Connected && profile.Ready {
		if profile.Attached {
			return tr("ui.9448242431c8")
		}
		return tr("ui.1f914c4386c0")
	}
	if profile.Connected {
		return tr("ui.d84771511dbe")
	}
	return tr("ui.5541de951552")
}

func drawTUISSHDashboard(
	b *strings.Builder,
	snapshot tuiSnapshot,
	width,
	height,
	profileRows int, language ...string,
) {
	if snapshot.SSHDetailName == "" {
		return
	}
	index := findTUISSHProfile(snapshot.SSHProfiles, snapshot.SSHDetailName)
	if index < 0 {
		return
	}
	profile := snapshot.SSHProfiles[index]
	drawTUISSHDetails(b, snapshot, profile, width, height, profileRows, language...)
}

func tuiSSHTunnelStatus(profile tuiSSHProfile, language ...string) (string, string) {
	tr := tuiTranslator(language...)
	status := tr("ui.3b8f2f48332d")
	statusColor := tuiDim
	if profile.NeedsUsername {
		status = tr("ui.c72289881d00")
		statusColor = tuiYellow
	} else if profile.Connected && profile.Ready {
		status = fmt.Sprintf(tr("ui.fdc61cb25073"), profile.SocksPort)
		if profile.Attached {
			status = fmt.Sprintf(tr("ui.df84bd191b6e"), tuiSSHExitHost(profile, language...), profile.SocksPort)
		}
		statusColor = tuiGreen
	} else if profile.Connected {
		status = tr("ui.1f1ff003e81e")
		if profile.LastError != "" {
			status += " · " + profile.LastError
		}
		statusColor = tuiRed
	} else if profile.LastError != "" {
		status = tr("ui.b6767700771c") + profile.LastError
		statusColor = tuiYellow
	}
	if profile.Default && !profile.NeedsUsername {
		status = tr("ui.ef08eaa198ce") + status
	}
	return status, statusColor
}

func drawTUISSHDetails(b *strings.Builder, snapshot tuiSnapshot, profile tuiSSHProfile, width, height, profileRows int, language ...string) {
	tr := tuiTranslator(language...)
	status, statusColor := tuiSSHTunnelStatus(profile, language...)
	focused := !snapshot.FocusSidebar && snapshot.SSHDashboardFocus
	subtitle := tr("ui.ca79eec78149")
	if profile.Connected && profile.Ready {
		subtitle = tr("ui.1f7f4087d744")
	}
	tuiTitle(b, tr("ui.7be2736a551d")+profile.Name, subtitle, width)
	fields := []tuiField{tuiLabelField(tr("ui.ead00218114c"), status)}
	fields[0].selected, fields[0].color = focused, statusColor
	if !profile.Connected || !profile.Ready {
		fields[0].truncate = true
		fields[0].selected = true
		rows := tuiSelectedRows(tuiFieldRows(fields, width), maxTUIWidth(height-(profileRows+3)-3, 1))
		for index := range rows {
			rows[index].selected = focused
		}
		tuiWriteRows(b, rows, width)
		tuiEndPanel(b, width)
		return
	}
	inetIP := tuiSSHRemoteIntranetLabel(snapshot.SSHDirectProbe, language...)
	if profile.SocksOnly {
		inetIP = tr("ui.b2172a8fc3b3")
	}
	fields = append(fields,
		tuiLabelField(tr("ui.d0e0f92fa371"), inetIP),
		tuiLabelField(tr("ui.b5a2eb00c061"), tuiSSHNetworkLabel(snapshot.SSHNetwork, tr("ui.309ee924bee5"), language...)),
		tuiTemplateField(tr("ui.205b4200a20d"), tuiTrafficChartDownload,
			formatBytes(snapshot.SSHTraffic.Down),
			tuiReset,
			tuiTrafficChartUpload,
			formatBytes(snapshot.SSHTraffic.Up),
			tuiReset,
		))
	fields[1].color, fields[2].color = tuiGreen, tuiCyan
	rows := tuiFieldRows(fields, width)
	limit := maxTUIWidth(height-(profileRows+3)-3, 1)
	visible := tuiSelectedRows(rows, limit)
	tuiWriteRows(b, visible, width)
	tuiEndPanel(b, width)

	chartHeight := height - (profileRows + 3) - (len(visible) + 3) - 3
	if chartHeight < 1 {
		return
	}
	chart := buildTUITrafficChart(snapshot.SSHTrafficHistory, maxTUIWidth(width-4, 1), chartHeight)
	tuiTitle(b, tr("ui.7fa028314fdd"), fmt.Sprintf(tr("ui.3a371c0c2d12"), formatBytes(chart.peak)), width)
	for _, line := range chart.lines {
		writeTUIAnsiRow(b, line, width)
	}
	tuiEndPanel(b, width)
}

func tuiSSHExitHost(profile tuiSSHProfile, language ...string) string {
	if profile.Reverse {
		return tuiTranslator(language...)("ssh.client")
	}
	return tuiTranslator(language...)("ssh.server")
}

func tuiSSHNetworkLabel(info tuiNetworkInfo, empty string, language ...string) string {
	tr := tuiTranslator(language...)
	if info.Loading {
		return tr("ui.d2387aecbed3")
	}
	if info.PublicIP != "" {
		value := info.PublicIP
		if info.Country != "" {
			value += "  [" + info.Country + "]"
		}
		return value
	}
	if info.Error != "" {
		return tr("ui.9203d7df0b44") + info.Error
	}
	return empty
}

func tuiSSHRemoteIntranetLabel(probe cliSSHRemoteProbe, language ...string) string {
	tr := tuiTranslator(language...)
	if strings.TrimSpace(probe.IntranetIP) != "" {
		return probe.IntranetIP
	}
	if probe.ProtocolVersion == 0 && probe.Reason == "" {
		return tr("ui.309ee924bee5")
	}
	if probe.Reason != "" {
		return tr("ui.9203d7df0b44") + probe.Reason
	}
	return tr("ui.dce5b6384f60")
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
	return tuiTruncateGraphemes(value, width, "…")
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
