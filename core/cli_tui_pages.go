//go:build linux && !cgo && cli

package main

import (
	"core/internal/i18n"
	"fmt"
	"strings"
)

func drawTUIProxies(b *strings.Builder, snapshot tuiSnapshot, width, height int, language ...string) {
	tr := tuiTranslator(language...)
	if snapshot.SelectedGroup < 0 || snapshot.SelectedGroup >= len(snapshot.Groups) {
		drawTUIEmpty(b, width, tr("ui.b39dc0586e6b"), tr("ui.d3ffc10982c9"), language...)
		return
	}
	group := snapshot.Groups[snapshot.SelectedGroup]
	availableRows := maxTUIWidth(height-6, 2)
	groupLimit := minTUI(len(snapshot.Groups), maxTUIWidth(availableRows/3, 1))
	nodeLimit := maxTUIWidth(availableRows-groupLimit, 1)
	groupStart, groupEnd := tuiVisibleRange(len(snapshot.Groups), snapshot.SelectedGroup, groupLimit)
	groupHint := tr("ui.42ce13a42874")
	if snapshot.ProxyNodeFocus {
		groupHint = tr("ui.edc5137b9179")
	}
	tuiTitle(
		b, tr("ui.5fc01e27a639"), groupHint,
		width,
	)
	for index := groupStart; index < groupEnd; index++ {
		item := snapshot.Groups[index]
		row := tuiColumns(width-4, tuiColumn{value: item.Name, width: 28, minimum: 8}, tuiColumn{value: item.Type, width: 12, minimum: 6, optional: true}, tuiColumn{value: item.Now, width: maxTUIWidth(width-48, 10), minimum: 8})
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

	nodeTitle := tr("ui.b228df86e81b") + group.Name
	tuiTitle(
		b,
		nodeTitle,
		fmt.Sprintf(tr("ui.4888c9c83bed"), len(group.Nodes)),
		width,
	)
	nodeStart, nodeEnd := tuiVisibleRange(len(group.Nodes), snapshot.SelectedNode, nodeLimit)
	for index := nodeStart; index < nodeEnd; index++ {
		node := group.Nodes[index]
		label := truncateTUI(node, width-24)
		if node == group.Now {
			label += tr("ui.7092b0d4d1b9")
		}
		color := tuiGreen
		delay, tested := group.Delays[node]
		switch {
		case delay.MedianMillis > 0:
			label += "  " + formatTUIDelay(delay, language...)
		case delay.Error != "":
			label += tr("ui.c5fbceb3cb76")
			color = tuiDim
		case delay.Testing:
			label += tr("ui.ffc70b13c202")
			color = tuiCyan
		case !tested:
			label += tr("ui.cfe0f02ae8da")
			color = tuiDim
		}
		if speed, speedTested := group.Speeds[node]; speedTested {
			switch {
			case speed.Testing:
				label += tr("ui.ea93eea5602a")
				color = tuiCyan
			case speed.Error != "":
				label += tr("ui.c17146077b24")
				color = tuiDim
			case speed.BytesPerSecond > 0:
				label += "  " + formatTUISpeed(speed)
			}
		} else {
			label += tr("ui.107cc2a4f52f")
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
		tuiRow(b, tr("ui.cef2c57fe2ed"), width, false, tuiDim)
	}
	tuiEndPanel(b, width)
}

func drawTUIProviders(b *strings.Builder, snapshot tuiSnapshot, width, height int, language ...string) {
	tr := tuiTranslator(language...)
	tuiTitle(
		b, tr("ui.47d771a56f18"), tr("ui.fbd7d7fc72fe"), width,
	)
	if len(snapshot.Providers) == 0 {
		tuiRow(b, tr("ui.0d41873790c3"), width, false, tuiDim)
		tuiEndPanel(b, width)
		return
	}
	limit := maxTUIWidth(height-3, 1)
	start, end := tuiVisibleRange(len(snapshot.Providers), snapshot.SelectedProvider, limit)
	for index := start; index < end; index++ {
		provider := snapshot.Providers[index]
		count := strings.TrimSpace(i18n.Count(tuiLanguageCode(language...), "providers.count", provider.Count, provider.Count))
		row := tuiColumns(width-4, tuiColumn{value: provider.Name, width: 30, minimum: 8}, tuiColumn{value: provider.Type, width: 10, minimum: 6, optional: true}, tuiColumn{value: count, width: tuiDisplayWidth(count), minimum: tuiDisplayWidth(count)}, tuiColumn{value: provider.UpdatedAt, width: maxTUIWidth(width-60, 8), minimum: 8, optional: true})
		tuiRow(b, row, width, index == snapshot.SelectedProvider && !snapshot.FocusSidebar, "")
	}
	tuiEndPanel(b, width)
}

func drawTUITools(b *strings.Builder, snapshot tuiSnapshot, width, height int, language ...string) {
	tr := tuiTranslator(language...)
	rows := tuiFieldRows(tuiSettingsFields(snapshot, language...), width)
	tuiTitle(
		b, tr("ui.74a883a037bc"), tr("ui.ab01bd8b9453"), width,
	)
	limit := maxTUIWidth(height-3, 1)
	tuiWriteRows(b, tuiSelectedRows(rows, limit), width)
	tuiEndPanel(b, width)
}

func drawTUIMaintenance(b *strings.Builder, snapshot tuiSnapshot, width, height int, language ...string) {
	tr := tuiTranslator(language...)
	rows := []string{tr("ui.c6d72737d4a1"), tr("ui.a656446a3979"), tr("ui.6861ed14e388"), tr("ui.e051747ccc5b"), tr("ui.207edd6d3339"), tuiUpdateRow(snapshot.Update, language...)}
	tuiTitle(
		b, tr("ui.17ccfa5b681e"), tr("ui.18d57e8c6aad"), width,
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

func tuiUpdateRow(info tuiUpdateInfo, language ...string) string {
	tr := tuiTranslator(language...)
	switch {
	case info.Loading:
		return tr("ui.0c33e6dba0ca")
	case info.Error != "":
		return tr("ui.1592dd3705a8")
	case info.LatestVersion != "" && info.Available:
		return fmt.Sprintf(tr("ui.5438c6aedc05"), info.LatestVersion)
	case info.LatestVersion != "":
		return fmt.Sprintf(tr("ui.e7751cd9894d"), cliVersion)
	default:
		return tr("ui.06905c634fe0")
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

func drawTUIHelp(b *strings.Builder, width, height int, language ...string) {
	tr := tuiTranslator(language...)
	rows := []string{tr("ui.c3f444889f5d"), tr("ui.54250fb97a2d"), tr("ui.f94a0d033f3a"), tr("ui.0de43a790d83"), tr("ui.45edd6ebf46d"), tr("ui.eb96c6ea8e4b"), tr("ui.7db9d1a5a715"), tr("ui.be571e2fec86"), tr("ui.f8a5661a2731"), tr("ui.da0b3c3a6c3a"), tr("ui.cdc5f54f1f26"), tr("ui.b6c5f9827d69"), tr("ui.8d0d06e21b22"), tr("ui.8c746c46c476")}
	if height < 28 {
		rows = rows[:minTUI(5, len(rows))]
	}
	rows = rows[:minTUI(len(rows), maxTUIWidth(height-3, 1))]
	tuiTitle(b, tr("ui.e9bef0b0f3c2"), tr("ui.aa107e7dba26"), width)
	for _, row := range rows {
		tuiRow(b, row, width, false, "")
	}
	tuiEndPanel(b, width)
}
