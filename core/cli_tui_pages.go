//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"strings"
)

func drawTUIProxies(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	if snapshot.SelectedGroup < 0 || snapshot.SelectedGroup >= len(snapshot.Groups) {
		drawTUIEmpty(b, width, "Proxy groups", "No group selected")
		return
	}
	group := snapshot.Groups[snapshot.SelectedGroup]
	availableRows := maxTUIWidth(height-6, 2)
	groupLimit := minTUI(len(snapshot.Groups), maxTUIWidth(availableRows/3, 1))
	nodeLimit := maxTUIWidth(availableRows-groupLimit, 1)
	groupStart, groupEnd := tuiVisibleRange(len(snapshot.Groups), snapshot.SelectedGroup, groupLimit)
	groupHint := "↑↓/ws group · Enter nodes · d test group · v speed group · [/] view"
	if snapshot.ProxyNodeFocus {
		groupHint = "Esc returns to proxy groups"
	}
	tuiTitle(
		b,
		"Proxies  ·  Groups  [1/2]",
		groupHint,
		width,
	)
	for index := groupStart; index < groupEnd; index++ {
		item := snapshot.Groups[index]
		row := fmt.Sprintf("%-28s %-12s %s", truncateTUI(item.Name, 28), item.Type, truncateTUI(item.Now, maxTUIWidth(width-48, 10)))
		tuiRow(
			b,
			row,
			width,
			index == snapshot.SelectedGroup &&
				!snapshot.FocusSidebar &&
				!snapshot.ProxyNodeFocus,
			"",
		)
	}
	tuiEndPanel(b, width)

	nodeTitle := "Nodes in " + group.Name
	tuiTitle(
		b,
		nodeTitle,
		fmt.Sprintf(
			"%d nodes · ↑↓/ws select · Enter apply · d delay · v speed · Esc back",
			len(group.Nodes),
		),
		width,
	)
	nodeStart, nodeEnd := tuiVisibleRange(len(group.Nodes), snapshot.SelectedNode, nodeLimit)
	for index := nodeStart; index < nodeEnd; index++ {
		node := group.Nodes[index]
		label := truncateTUI(node, width-24)
		if node == group.Now {
			label += "  [current]"
		}
		color := tuiGreen
		delay, tested := group.Delays[node]
		switch {
		case delay.MedianMillis > 0:
			label += "  " + formatTUIDelay(delay)
		case delay.Error != "":
			label += "  Timeout · d retry"
			color = tuiDim
		case delay.Testing:
			label += "  Testing..."
			color = tuiCyan
		case !tested:
			label += "  [d test]"
			color = tuiDim
		}
		if speed, speedTested := group.Speeds[node]; speedTested {
			switch {
			case speed.Testing:
				label += "  Speed testing..."
				color = tuiCyan
			case speed.Error != "":
				label += "  Speed failed · v retry"
				color = tuiDim
			case speed.BytesPerSecond > 0:
				label += "  " + formatTUISpeed(speed)
			}
		} else {
			label += "  [v test]"
		}
		tuiRow(
			b,
			label,
			width,
			index == snapshot.SelectedNode &&
				!snapshot.FocusSidebar &&
				snapshot.ProxyNodeFocus,
			color,
		)
	}
	if len(group.Nodes) == 0 {
		tuiRow(b, "No nodes in this group", width, false, tuiDim)
	}
	tuiEndPanel(b, width)
}

func drawTUIProviders(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	tuiTitle(
		b,
		"Proxies  ·  Providers  [2/2]",
		"↑↓/ws provider · Enter update · [/] view",
		width,
	)
	if len(snapshot.Providers) == 0 {
		tuiRow(b, "No proxy providers configured", width, false, tuiDim)
		tuiEndPanel(b, width)
		return
	}
	limit := maxTUIWidth(height-3, 1)
	start, end := tuiVisibleRange(len(snapshot.Providers), snapshot.SelectedProvider, limit)
	for index := start; index < end; index++ {
		provider := snapshot.Providers[index]
		row := fmt.Sprintf("%-30s %-10s %4d proxies  %s", truncateTUI(provider.Name, 30), truncateTUI(provider.Type, 10), provider.Count, truncateTUI(provider.UpdatedAt, maxTUIWidth(width-60, 8)))
		tuiRow(b, row, width, index == snapshot.SelectedProvider && !snapshot.FocusSidebar, "")
	}
	tuiEndPanel(b, width)
}

func drawTUITools(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	rows := tuiSettingsRows(snapshot)
	tuiTitle(
		b,
		"Settings",
		"Allow LAN, IPv6, delay, log, TUN scope · daily controls are on Dashboard",
		width,
	)
	limit := maxTUIWidth(height-3, 1)
	start, end := tuiVisibleRange(len(rows), snapshot.SelectedTool, limit)
	for index := start; index < end; index++ {
		tuiRow(
			b,
			rows[index],
			width,
			index == snapshot.SelectedTool && !snapshot.FocusSidebar,
			"",
		)
	}
	tuiEndPanel(b, width)
}

func drawTUIMaintenance(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	rows := []string{
		"Config        Edit current YAML in $EDITOR",
		"Backup        Create timestamped configuration backup",
		"Restore       Restore newest configuration backup",
		"Resources     Update Mihomo Geo databases",
		"Traffic       Reset traffic counters",
		tuiUpdateRow(snapshot.Update),
	}
	tuiTitle(
		b,
		"Maintenance",
		"Configuration · resources · diagnostics",
		width,
	)
	limit := maxTUIWidth(height-3, 1)
	start, end := tuiVisibleRange(len(rows), snapshot.SelectedMaintenance, limit)
	for index := start; index < end; index++ {
		row := rows[index]
		tuiRow(
			b,
			row,
			width,
			index == snapshot.SelectedMaintenance && !snapshot.FocusSidebar,
			"",
		)
	}
	tuiEndPanel(b, width)
}

func tuiUpdateRow(info tuiUpdateInfo) string {
	switch {
	case info.Loading:
		return "Update        Checking GitHub Releases..."
	case info.Error != "":
		return "Update        Check failed · Enter to retry"
	case info.LatestVersion != "" && info.Available:
		return fmt.Sprintf(
			"Update        v%s available · run flclash update",
			info.LatestVersion,
		)
	case info.LatestVersion != "":
		return fmt.Sprintf("Update        v%s is latest · keep it if stable", cliVersion)
	default:
		return "Update        Enter checks GitHub · if stable, do not update lightly"
	}
}

func minTUI(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func tuiVisibleRange(total, selected, limit int) (int, int) {
	if total <= 0 || limit <= 0 {
		return 0, 0
	}
	if limit >= total {
		return 0, total
	}
	if selected < 0 {
		selected = 0
	}
	if selected >= total {
		selected = total - 1
	}
	start := selected - limit/2
	if start < 0 {
		start = 0
	}
	if start+limit > total {
		start = total - limit
	}
	return start, start + limit
}

func drawTUIHelp(b *strings.Builder, width, height int) {
	rows := []string{
		"Navigation     ← sidebar · → content · ↑↓/ws move · Enter opens/applies · Esc back",
		"Sections       1-9 open directly · Tab changes focus · [/] changes proxy view",
		"Dashboard      flc Enter opens Proxies · d latency · v speed · n refresh",
		"Proxies        Enter nodes · d node RTT (5 samples) · v node speed · Esc groups",
		"Profiles       Enter activate · U refresh · u/F2 rename · e edit · n import · x delete",
		"SSH            Tab list/Dashboard · n add · a probe/capture live SSH · e edit · u default · Enter connect",
		"History        x clears shared history · Connections: x close all · d close selected",
		"Logs           e exports captured logs · x clears captured logs",
		"Core           S system proxy (auto-start) · c start/stop · t TUN · m mode",
		"Settings       Enter applies row · a LAN · v IPv6 · i log · Dashboard has Core/mode/flc/port",
		"Maintenance    Edit, backup, restore, Geo, traffic reset, and updates",
		"Notifications  Ctrl+N opens history/details · Enter confirms · Esc closes",
		"Exit           q exits this TUI only · Ctrl+C shuts down Backend, Core, and SSH",
	}
	if height < 28 {
		rows = rows[:minTUI(5, len(rows))]
	}
	rows = rows[:minTUI(len(rows), maxTUIWidth(height-3, 1))]
	tuiTitle(b, "Keyboard shortcuts", "press ? to close", width)
	for _, row := range rows {
		tuiRow(b, row, width, false, "")
	}
	tuiEndPanel(b, width)
}
