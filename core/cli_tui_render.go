//go:build linux && !cgo && cli

package main

import (
	"core/internal/i18n"
	"fmt"
	"github.com/clipperhouse/uax29/v2/graphemes"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

func drawTUI(w io.Writer, snapshot tuiSnapshot, paths cliPaths, controllerAddress string, ownsCore, coreRunning bool) {
	width, height := tuiTerminalSize()
	drawTUIAtSize(w, snapshot, paths, controllerAddress, ownsCore, coreRunning, width, height)
}

func drawTUIAtSize(w io.Writer, snapshot tuiSnapshot, paths cliPaths, controllerAddress string, ownsCore, coreRunning bool, width, height int) {
	writeTUIFrame(w, renderTUIAtSize(snapshot, paths, controllerAddress, ownsCore, coreRunning, width, height))
}

func renderTUIAtSize(snapshot tuiSnapshot, paths cliPaths, controllerAddress string, ownsCore, coreRunning bool, width, height int) string {
	snapshot = safeTUISnapshotForRender(snapshot)
	paths.ConfigPath, paths.HomeDir = safeCLITerminalLine(paths.ConfigPath), safeCLITerminalLine(paths.HomeDir)
	controllerAddress = safeCLITerminalLine(controllerAddress)
	language := []string{snapshot.Language}
	tr := tuiTranslator(language...)
	layout := tuiLayoutAtSize(width, height, snapshot.Language)
	if width <= 0 || height <= 0 {
		return ""
	}
	if layout.Tiny {
		return renderTUITiny(snapshot, ownsCore, coreRunning, width, height, language...)
	}
	snapshot.ServiceRunning = coreRunning
	snapshot.ExternalCore = !ownsCore
	if ownsCore && coreRunning && snapshot.Settings.Mode != tuiSilentMode &&
		snapshot.Settings.MixedPort <= 0 &&
		(snapshot.Status == "" || snapshot.Status == "Connected" || snapshot.Status == "Core listeners started") {
		snapshot.setStatus(newTUIMessage("ui.73ece136b3ce"))
	}
	if layout.Compact {
		return renderTUICompact(
			snapshot,
			paths,
			controllerAddress,
			ownsCore,
			coreRunning,
			width,
			height, language...)
	}
	contentWidth := width - 2
	headerHeight := 3
	footerHeight := 1
	bodyHeight := height - headerHeight - footerHeight
	sidebarWidth := layout.Sidebar
	mainOuterWidth := width - sidebarWidth - 1
	mainContentWidth := mainOuterWidth - 2
	var b strings.Builder
	b.WriteString(tuiBoxTop(contentWidth))
	headerLeft := tr("ui.a07466a928d7")
	headerRight := "  " + tuiStatusDot(ownsCore, coreRunning) + " " + tuiCoreStatus(ownsCore, coreRunning, language...) + "  "
	if tuiDisplayWidth(headerLeft)+tuiDisplayWidth(headerRight)+tuiDisplayWidth(controllerAddress)+2 <= contentWidth {
		headerRight += controllerAddress + "  "
	} else if tuiDisplayWidth(headerLeft)+tuiDisplayWidth(headerRight) > contentWidth {
		headerLeft = "  FlClash"
	}
	b.WriteString(tuiBoxRow(headerLeft, headerRight, contentWidth, tuiCyan, tuiDim))
	b.WriteString(tuiBoxBottom(contentWidth))

	page := tuiRenderPage(snapshot, paths, mainContentWidth, bodyHeight, language...)
	sidebar := tuiSidebar(snapshot, sidebarWidth, bodyHeight, language...)
	pageLines := strings.Split(strings.TrimSuffix(page, "\n"), "\n")
	for row := 0; row < bodyHeight; row++ {
		left := ""
		if row < len(sidebar) {
			left = sidebar[row]
		}
		if left == "" {
			left = strings.Repeat(" ", sidebarWidth)
		}
		right := ""
		if row < len(pageLines) {
			right = pageLines[row]
		}
		left = tuiClampAnsiLine(left, sidebarWidth)
		right = tuiClampAnsiLine(right, mainOuterWidth)
		b.WriteString(left)
		b.WriteByte(' ')
		b.WriteString(right)
		b.WriteByte('\n')
	}

	footer := tr("ui.a9b34ecb8fd7")
	if width >= 110 {
		footer = tr("ui.f43c74fbfff4")
	}
	if snapshot.Page == tuiPageDashboard && bodyHeight < 33 {
		footer = tr("ui.e1db28b2e468")
	} else if snapshot.Page == tuiPageSSH {
		footer = tr("ui.cfc17a4264b8")
	}
	b.WriteString(tuiNotificationFooter(snapshot, footer, width, language...))
	return b.String()
}

func renderTUICompact(
	snapshot tuiSnapshot,
	paths cliPaths,
	controllerAddress string,
	ownsCore,
	coreRunning bool,
	width,
	height int, language ...string,
) string {
	tr := tuiTranslator(language...)
	pageName := tuiPageName(snapshot.Page, language...)
	focus := tr("ui.65f23e22a9bf")
	if snapshot.FocusSidebar {
		focus = tr("ui.59d29bfe4c87")
	} else if snapshot.Page == tuiPageSSH {
		focus = tr("ui.65c80c24b574")
		if snapshot.SSHDashboardFocus {
			focus = tr("ui.3197fdf77782")
		}
	}
	header := fmt.Sprintf(tr("ui.7c59d12a8362"), int(snapshot.Page)+1,
		pageName,
		tuiCoreStatus(ownsCore, coreRunning, language...),
		focus,
	)
	if width >= 72 {
		header += " · " + truncateTUI(controllerAddress, 24)
	}
	if tuiDisplayWidth(header) > width {
		header = tuiPanelHeading("FlClash · "+pageName, tuiCoreStatus(ownsCore, coreRunning, language...)+" · "+focus, width)
	}

	bodyHeight := height - 2
	contentWidth := width - 2
	var page string
	switch {
	case snapshot.LanguageSelectionOpen || snapshot.NotificationDetailOpen ||
		snapshot.DangerConfirmOpen ||
		snapshot.ProfileDelete.Open ||
		snapshot.SSHForm.DeleteConfirmOpen ||
		snapshot.SSHCredentialPrompt.Open ||
		snapshot.SSHForm.Open ||
		snapshot.SelectionTitle != "" ||
		snapshot.InputTitle != "":
		page = tuiRenderPage(
			snapshot,
			paths,
			contentWidth,
			bodyHeight, language...)
	case snapshot.FocusSidebar:
		page = renderTUICompactNavigation(
			snapshot,
			contentWidth,
			bodyHeight, language...)
	case snapshot.Page == tuiPageDashboard:
		page = renderTUICompactDashboard(
			snapshot,
			paths,
			contentWidth,
			bodyHeight, language...)
	default:
		page = tuiRenderPage(
			snapshot,
			paths,
			contentWidth,
			bodyHeight, language...)
	}

	var b strings.Builder
	b.WriteString(tuiClampAnsiLine(header, width))
	b.WriteByte('\n')
	pageLines := strings.Split(strings.TrimSuffix(page, "\n"), "\n")
	for row := 0; row < bodyHeight; row++ {
		line := ""
		if row < len(pageLines) {
			line = pageLines[row]
		}
		b.WriteString(tuiClampAnsiLine(line, width))
		b.WriteByte('\n')
	}
	footer := tr("ui.03473830b9e6")
	if snapshot.FocusSidebar {
		footer = tr("ui.441352eba67d")
	} else if snapshot.Page == tuiPageDashboard {
		footer = tr("ui.b813f1451df7")
	} else if snapshot.Page == tuiPageSSH {
		footer = tr("ui.e41981133fd7")
	}
	b.WriteString(tuiNotificationFooter(snapshot, footer, width, language...))
	return b.String()
}

func tuiPageName(page tuiPage, language ...string) string {
	tr := tuiTranslator(language...)
	switch page {
	case tuiPageDashboard:
		return tr("ui.67b696468610")
	case tuiPageProxies:
		return tr("ui.29c4349715f3")
	case tuiPageProfiles:
		return tr("ui.535e52e4a261")
	case tuiPageSSH:
		return "SSH"
	case tuiPageRequests:
		return tr("ui.0e7696009337")
	case tuiPageConnections:
		return tr("ui.dc273117482b")
	case tuiPageLogs:
		return tr("ui.ea2100dc89ae")
	case tuiPageTools:
		return tr("ui.74a883a037bc")
	case tuiPageMaintenance:
		return tr("ui.17ccfa5b681e")
	default:
		return tr("ui.b764cdc0eab7")
	}
}

func renderTUICompactNavigation(
	snapshot tuiSnapshot,
	width,
	height int, language ...string,
) string {
	tr := tuiTranslator(language...)
	var b strings.Builder
	tuiTitle(&b, tr("ui.3db65f8c2a7d"), tr("ui.40c05dff4899"), width)
	limit := maxTUIWidth(height-3, 1)
	start, end := tuiVisibleRange(
		int(tuiPageCount),
		snapshot.SelectedMenu,
		limit,
	)
	for index := start; index < end; index++ {
		tuiRow(
			&b,
			fmt.Sprintf("%d  %s", index+1, tuiPageName(tuiPage(index), language...)),
			width,
			index == snapshot.SelectedMenu,
			"",
		)
	}
	tuiEndPanel(&b, width)
	return b.String()
}

func tuiWrapText(value string, width int) []string {
	if width <= 0 {
		return nil
	}
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{""}
	}
	lines := make([]string, 0, len(words))
	line := ""
	for _, word := range words {
		candidate := word
		if line != "" {
			candidate = line + " " + word
		}
		if tuiDisplayWidth(candidate) <= width {
			line = candidate
			continue
		}
		if line != "" {
			lines = append(lines, line)
		}
		wordParts := tuiWrapWord(word, width)
		if len(wordParts) > 1 {
			lines = append(lines, wordParts[:len(wordParts)-1]...)
		}
		line = wordParts[len(wordParts)-1]
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

func tuiWrapWord(value string, width int) []string {
	if value == "" || width <= 0 {
		return []string{""}
	}
	parts := make([]string, 0, 1)
	var part strings.Builder
	partWidth := 0
	clusters := graphemes.FromString(strings.ToValidUTF8(value, "�"))
	clusters.AnsiEscapeSequences = true
	for clusters.Next() {
		cluster := clusters.Value()
		runeWidth := tuiDisplayWidth(cluster)
		if runeWidth > width {
			// An indivisible wide cluster cannot fit in a one-cell viewport.
			continue
		}
		if partWidth > 0 && partWidth+runeWidth > width {
			parts = append(parts, part.String())
			part.Reset()
			partWidth = 0
		}
		part.WriteString(cluster)
		partWidth += runeWidth
	}
	if part.Len() > 0 {
		parts = append(parts, part.String())
	}
	if len(parts) == 0 {
		return []string{""}
	}
	return parts
}

func drawTUITooSmall(w io.Writer, width, height int) {
	writeTUIFrame(w, renderTUITooSmall(width, height))
}

func renderTUITooSmall(width, height int, language ...string) string {
	tr := tuiTranslator(language...)
	lines := []string{
		"", tr("ui.1e9567a9dd43"), "",
		fmt.Sprintf(tr("ui.811807ed4d6e"), width, height), tr("ui.b9180024339b"), "", tr("ui.b0199ea270f3"),
	}
	var b strings.Builder
	for row := 0; row < height; row++ {
		line := ""
		if row < len(lines) {
			line = lines[row]
		}
		b.WriteString(tuiClampAnsiLine(line, width))
		if row < height-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func renderTUITiny(
	snapshot tuiSnapshot,
	ownsCore,
	coreRunning bool,
	width,
	height int, language ...string,
) string {
	tr := tuiTranslator(language...)
	lines := []string{tr("ui.f84639a4084d") +
		tuiCoreStatus(ownsCore, coreRunning, language...), fmt.Sprintf("%d %s", int(snapshot.Page)+1, tuiPageName(snapshot.Page, language...)),
	}
	if snapshot.LanguageSelectionOpen {
		items := i18n.Languages()
		selected := minTUI(maxTUIWidth(snapshot.SelectedLanguage, 0), len(items)-1)
		lines = []string{"Language", items[selected].Name + " [" + items[selected].Code + "]", i18n.Text(snapshot.Language, "language.choose")}
	} else if snapshot.NotificationDetailOpen {
		lines = append(lines, tuiNotificationTinyDetail(snapshot, width, language...)...)
	} else if snapshot.DangerConfirmOpen {
		lines = []string{snapshot.DangerConfirmTitle, snapshot.DangerConfirmMessage, "Enter · Esc"}
	} else if snapshot.ProfileDelete.Open {
		lines = []string{tr("ui.4e5fc6f52f59"), snapshot.ProfileDelete.Name, "Enter · Esc"}
	} else if snapshot.SSHForm.DeleteConfirmOpen {
		lines = []string{tr("ui.5cbce6c2671b"), snapshot.SSHForm.DeleteName, "Enter · Esc"}
	} else if snapshot.SSHCredentialPrompt.Open {
		lines = []string{tr("ui.5495b65bea00"), snapshot.SSHCredentialPrompt.Value, "Enter · Esc"}
	} else if snapshot.InputTitle != "" {
		lines = []string{snapshot.InputTitle, snapshot.InputValue, "Enter · Esc"}
	} else if snapshot.SelectionTitle != "" {
		lines = []string{snapshot.SelectionTitle}
		if len(snapshot.SelectionOptions) > 0 {
			index := minTUI(maxTUIIndex(snapshot.SelectedOption), len(snapshot.SelectionOptions)-1)
			lines = append(lines, snapshot.SelectionOptions[index], "Enter · Esc")
		}
	} else if snapshot.SSHForm.Open {
		rows := tuiFieldRows(tuiSSHFormFields(snapshot.SSHForm, language...), width+4)
		for _, row := range rows {
			if row.selected {
				lines = append([]string{"SSH"}, row.value, "Enter · Esc")
				break
			}
		}
	} else if notification, ok := tuiLatestUnreadNotification(snapshot.Notifications); ok {
		lines = append(lines, tuiNotificationTinySummary(notification, width, language...))
	}
	if height > 0 && len(lines) >= height {
		lines = lines[:height-1]
	}
	lines = append(lines, tr("ui.797be7a3e1f9"))
	var b strings.Builder
	for row := 0; row < height; row++ {
		line := ""
		if row < len(lines) {
			line = lines[row]
		}
		b.WriteString(tuiClampAnsiLine(line, width))
		if row < height-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func writeTUIFrame(w io.Writer, frame string) {
	// term.MakeRaw disables the terminal's output post-processing. A bare LF
	// therefore moves down without returning to column zero, which makes every
	// subsequent row drift to the right. Normalize complete frames to CRLF.
	frame = strings.ReplaceAll(frame, "\r\n", "\n")
	frame = strings.ReplaceAll(frame, "\n", "\r\n")
	_, _ = io.WriteString(w, frame)
}

type tuiFrameWriter struct {
	writer io.Writer
	last   string
}

func (w *tuiFrameWriter) Write(data []byte) (int, error) {
	frame := string(data)
	if frame == w.last {
		return len(data), nil
	}
	written, err := w.writer.Write(data)
	if err == nil && written == len(data) {
		w.last = frame
	}
	return written, err
}

func (w *tuiFrameWriter) invalidate() {
	w.last = ""
}

func tuiRenderPage(snapshot tuiSnapshot, paths cliPaths, width, height int, language ...string) string {
	tr := tuiTranslator(language...)
	var b strings.Builder
	if snapshot.LanguageSelectionOpen {
		drawTUILanguageSelection(&b, snapshot, width, height)
	} else if snapshot.NotificationDetailOpen {
		drawTUINotificationDetails(&b, snapshot, width, height, language...)
	} else if snapshot.DangerConfirmOpen {
		drawTUIDangerConfirm(&b, snapshot, width, height, language...)
	} else if snapshot.ProfileDelete.Open {
		drawTUIProfileDeleteConfirm(&b, snapshot.ProfileDelete, width, height, language...)
	} else if snapshot.SSHForm.DeleteConfirmOpen {
		drawTUISSHDeleteConfirm(&b, snapshot.SSHForm, width, height, language...)
	} else if snapshot.SSHCredentialPrompt.Open {
		drawTUISSHCredentialPrompt(&b, snapshot.SSHCredentialPrompt, width, height, language...)
	} else if snapshot.SSHForm.Open {
		drawTUISSHForm(&b, snapshot.SSHForm, width, height, language...)
	} else if snapshot.SelectionTitle != "" {
		drawTUISelection(&b, snapshot, width, height, language...)
	} else if snapshot.InputTitle != "" {
		drawTUIInput(&b, snapshot, width, height, language...)
	} else if snapshot.ShowHelp {
		drawTUIHelp(&b, width, height, language...)
	} else if snapshot.Page == tuiPageRequests {
		drawTUIRequests(&b, snapshot, width, height, language...)
	} else if snapshot.Page == tuiPageConnections {
		drawTUIConnections(&b, snapshot, width, height, language...)
	} else if snapshot.Page == tuiPageLogs {
		drawTUILogs(&b, snapshot, width, height, language...)
	} else if snapshot.Page == tuiPageProfiles {
		drawTUIProfiles(&b, snapshot, width, height, language...)
	} else if snapshot.Page == tuiPageSSH {
		drawTUISSH(&b, snapshot, width, height, language...)
	} else if snapshot.Page == tuiPageTools {
		drawTUITools(&b, snapshot, width, height, language...)
	} else if snapshot.Page == tuiPageMaintenance {
		drawTUIMaintenance(&b, snapshot, width, height, language...)
	} else if snapshot.Page == tuiPageDashboard {
		if height < 33 {
			return renderTUICompactDashboard(
				snapshot,
				paths,
				width,
				height, language...)
		}
		drawTUIDashboard(&b, snapshot, paths, width, height, language...)
	} else if snapshot.Page == tuiPageProxies &&
		snapshot.ProxyView == tuiProxyViewProviders {
		drawTUIProviders(&b, snapshot, width, height, language...)
	} else if snapshot.Page == tuiPageProxies && len(snapshot.Groups) == 0 {
		drawTUIEmpty(
			&b,
			width, tr("ui.b39dc0586e6b"), tr("ui.e06a1f3bf373"), language...)
	} else {
		drawTUIProxies(&b, snapshot, width, height, language...)
	}
	return b.String()
}

func drawTUISSHCredentialPrompt(
	b *strings.Builder,
	prompt tuiSSHCredentialPromptView,
	width, height int, language ...string,
) {
	tr := tuiTranslator(language...)
	tuiTitle(b, tr("ui.5495b65bea00"), tr("ui.c6ed2550e4a5"), width)
	fields := []tuiField{
		tuiLabelField(tr("ui.40f9380eb081"), prompt.Profile),
		tuiLabelField(tr("ui.5ca3303d2b97"), prompt.Identity),
		tuiLabelField(tr("ui.8cdf355377b7"), prompt.Value),
	}
	for index := range fields {
		fields[index].truncate = true
		fields[index].color = tuiCyan
	}
	fields[1].color, fields[2].selected = tuiDim, true
	rows := tuiFieldRows(fields, width)
	tuiWriteRows(b, tuiSelectedRows(rows, maxTUIWidth(height-4, 1)), width)
	if height >= len(rows)+5 {
		tuiRow(b, tr("ui.a3b01b249557"), width, false, tuiDim)
	}
	tuiRow(b, tuiOverlayHint(tr("ui.c6ed2550e4a5"), width-4), width, false, tuiDim)
	tuiEndPanel(b, width)
}

func drawTUIDangerConfirm(b *strings.Builder, snapshot tuiSnapshot, width, height int, language ...string) {
	tr := tuiTranslator(language...)
	tuiConfirmationPanel(b, snapshot.DangerConfirmTitle, snapshot.DangerConfirmMessage, tr("ui.566b258d2d58"), width, height, tuiYellow, tr("ui.2082f6d15946"))
}

func drawTUISelection(b *strings.Builder, snapshot tuiSnapshot, width, height int, language ...string) {
	tr := tuiTranslator(language...)
	tuiTitle(b, snapshot.SelectionTitle, tr("ui.566b258d2d58"), width)
	start, end := tuiVisibleRange(len(snapshot.SelectionOptions), snapshot.SelectedOption, maxTUIWidth(height-4, 1))
	for index := start; index < end; index++ {
		option := snapshot.SelectionOptions[index]
		label := option
		if strings.EqualFold(option, snapshot.Settings.Mode) {
			label += tr("ui.fdf010627cf3")
		}
		tuiRow(b, label, width, index == snapshot.SelectedOption, "")
	}
	tuiRow(b, tuiOverlayHint(tr("ui.566b258d2d58"), width-4), width, false, tuiDim)
	tuiEndPanel(b, width)
	if snapshot.SelectionHint != "" && height-(end-start+4) >= 4 {
		tuiEmptyPanel(b, tr("ui.76c528171b52"), snapshot.SelectionHint, width)
	}
}

func drawTUIInput(b *strings.Builder, snapshot tuiSnapshot, width, height int, language ...string) {
	tr := tuiTranslator(language...)
	tuiTitle(b, snapshot.InputTitle, tr("ui.566b258d2d58"), width)
	tuiRow(b, snapshot.InputValue, width, true, "")
	tuiRow(b, tuiOverlayHint(tr("ui.566b258d2d58"), width-4), width, false, tuiDim)
	tuiEndPanel(b, width)
	if snapshot.InputHint != "" && height >= 9 {
		tuiEmptyPanel(b, tr("ui.3046ec5fc894"), snapshot.InputHint, width)
	}
}

func drawTUISSHDeleteConfirm(
	b *strings.Builder,
	form tuiSSHFormView,
	width, height int, language ...string,
) {
	tr := tuiTranslator(language...)
	tuiConfirmationPanel(b, tr("ui.5cbce6c2671b"), tr("ui.85941fb9c992")+form.DeleteName+tr("ui.eff2e5d9ad9b"), tr("ui.566b258d2d58"), width, height, tuiRed)
}

func drawTUIProfileDeleteConfirm(
	b *strings.Builder,
	profile tuiProfileDeleteView,
	width, height int, language ...string,
) {
	tr := tuiTranslator(language...)
	tuiConfirmationPanel(b, tr("ui.4e5fc6f52f59"), tr("ui.85941fb9c992")+profile.Name+" ("+profile.Kind+tr("ui.83e91cd4a9a2"), tr("ui.566b258d2d58"), width, height, tuiRed)
}

func drawTUISSHForm(
	b *strings.Builder,
	form tuiSSHFormView,
	width,
	height int, language ...string,
) {
	tr := tuiTranslator(language...)
	title := tr("ui.4ad0afb38058")
	subtitle := tr("ui.8202eba653cf")
	if form.Existing {
		title = tr("ui.e5124b518aec")
	}
	if form.ReadOnly {
		title = tr("ui.d30fbba17a15")
		subtitle = tr("ui.305888507e97")
	}
	tuiTitle(
		b,
		title,
		subtitle,
		width,
	)
	rows := tuiFieldRows(tuiSSHFormFields(form, language...), width)
	tuiWriteRows(b, tuiSelectedRows(rows, maxTUIWidth(height-4, 1)), width)
	tuiRow(b, tuiOverlayHint(tr("ui.566b258d2d58"), width-4), width, false, tuiDim)
	tuiEndPanel(b, width)
}

func tuiSSHFormFields(form tuiSSHFormView, language ...string) []tuiField {
	tr := tuiTranslator(language...)
	rows := []tuiField{
		tuiLabelField(tr("ui.6f1f5571c8d7"), form.Name),
		tuiLabelField(tr("ui.dae627471c92"), form.Username),
		tuiLabelField(tr("ui.3399bee80cd4"), form.Host),
		tuiLabelField(tr("ui.fa98cd3e1313"), cliDisplayValue(form.Jump)),
		tuiTemplateField(tr("ui.cfe13ac844d1"), form.Port),
		tuiLabelField(tr("ui.580605e99507"), formatTUISSHLocalPort(form.LocalPort, language...)),
		tuiLabelField(tr("ui.b4771c58ec60"), cliDisplayValue(form.Identity)),
	}
	passphrase := tr("ui.be5192ae9afa")
	switch {
	case form.Identity == "":
		passphrase = tr("ui.4821b22cbe04")
	case form.IdentityError != "":
		passphrase = tr("ui.ad5596433ee6")
	case form.IdentityKind == cliSSHIdentityUnencrypted:
		passphrase = tr("ui.d3519f496eb7")
	case form.IdentityKind == cliSSHIdentityEncrypted && !form.PassphraseSet:
		passphrase = tr("ui.3e629128caf6")
	case form.PassphraseSet:
		passphrase = tr("ui.5ae755203e2e")
	}
	if form.ReadOnly && form.PassphraseSet {
		passphrase = tr("ui.b4ccf9453934")
	} else if form.ReadOnly {
		passphrase = tr("ui.7c999d8f7c2c")
	}
	if form.PassphraseChanged {
		passphrase = tr("ui.baaebcc4c8db")
	}
	if form.PassphraseCleared {
		passphrase = tr("ui.ab64806442ee")
	}
	rows = append(rows, tuiLabelField(tr("ui.d467413fb105"), passphrase))
	password := tr("ui.be5192ae9afa")
	if form.PasswordSet {
		password = tr("ui.5ae755203e2e")
	}
	if form.ReadOnly && form.PasswordSet {
		password = tr("ui.b4ccf9453934")
	} else if form.ReadOnly {
		password = tr("ui.7c999d8f7c2c")
	}
	if form.PasswordChanged {
		password = tr("ui.baaebcc4c8db")
	}
	if form.PasswordCleared {
		password = tr("ui.ab64806442ee")
	}
	rows = append(rows, tuiLabelField(tr("ui.89c8cb234afb"), password))
	for index, option := range form.Options {
		rows = append(rows, tuiLabelField(fmt.Sprintf(tr("ui.94fb36dd783a"), index+1, ""), option))
	}
	addOptionLabel := tr("ui.203a4ea6a7c1")
	addOptionColor := tuiCyan
	saveLabel := tr("ui.0c8209e72ec8")
	saveColor := tuiGreen
	if form.ReadOnly {
		addOptionLabel = tr("ui.979b8c7be4eb")
		addOptionColor = tuiDim
		saveLabel = tr("ui.fda7316f24a0")
		saveColor = tuiDim
	}
	rows = append(rows,
		tuiField{label: addOptionLabel, color: addOptionColor},
		tuiField{label: saveLabel, color: saveColor},
	)
	if form.Existing {
		rows = append(rows, tuiField{label: tr("ui.47311af432a7"), color: tuiRed})
	}
	cancelLabel := tr("ui.19766ed6ccb2")
	if form.ReadOnly {
		cancelLabel = tr("ui.edc82d69e76d")
	}
	rows = append(rows, tuiField{label: cancelLabel, color: tuiYellow})
	if form.FieldEditing && form.Selected >= 0 && form.Selected < len(rows) {
		rows[form.Selected] = tuiLabelField(tuiSSHFormEditingLabel(form, language...), form.FieldInput)
	}
	for index := range rows {
		rows[index].selected = index == form.Selected
	}
	return rows
}

func tuiSSHFormEditingLabel(form tuiSSHFormView, language ...string) string {
	tr := tuiTranslator(language...)
	if form.Selected == tuiSSHFormPassphraseRow {
		if form.PassphraseConfirm {
			return tr("ui.f4ab89f5dce3")
		}
		return tr("ui.d467413fb105")
	}
	if form.Selected == tuiSSHFormPasswordRow {
		if form.PasswordConfirm {
			return tr("ui.4522fb74ce00")
		}
		return tr("ui.89c8cb234afb")
	}
	if form.Selected == tuiSSHFormJumpRow {
		return tr("ui.fa98cd3e1313")
	}
	return tr("ui.e9e16253d5f9")
}

func formatTUISSHLocalPort(port int, language ...string) string {
	tr := tuiTranslator(language...)
	if port <= 0 {
		return tr("ui.929260ad9b9e")
	}
	return "127.0.0.1:" + strconv.Itoa(port)
}

const (
	tuiReset  = "\x1b[0m"
	tuiBold   = "\x1b[1m"
	tuiDim    = "\x1b[2m"
	tuiCyan   = "\x1b[36m"
	tuiGreen  = "\x1b[32m"
	tuiYellow = "\x1b[33m"
	tuiRed    = "\x1b[31m"
	tuiSelect = "\x1b[48;5;24m\x1b[97m"
)

func tuiTerminalSize() (int, int) {
	width, height := 0, 0
	for _, fd := range []uintptr{os.Stdin.Fd(), os.Stdout.Fd()} {
		candidateWidth, candidateHeight, err := term.GetSize(int(fd))
		if err == nil && candidateWidth > 0 && candidateHeight > 0 {
			if width == 0 || candidateWidth < width {
				width = candidateWidth
			}
			if height == 0 || candidateHeight < height {
				height = candidateHeight
			}
		}
	}
	if width == 0 {
		if columns, err := strconv.Atoi(os.Getenv("COLUMNS")); err == nil && columns > 0 {
			width = columns
		}
	}
	if height == 0 {
		if lines, err := strconv.Atoi(os.Getenv("LINES")); err == nil && lines > 0 {
			height = lines
		}
	}
	if width == 0 || height == 0 {
		return 96, 30
	}
	return width, height
}

func tuiBoxTop(width int) string {
	return "┌" + strings.Repeat("─", width) + "┐\n"
}

func tuiBoxBottom(width int) string {
	return "└" + strings.Repeat("─", width) + "┘\n"
}

func tuiBoxRow(left, right string, width int, leftColor, rightColor string) string {
	right = truncateTUI(right, maxTUIWidth(width-1, 0))
	leftWidth := width - tuiDisplayWidth(right)
	if leftWidth < 1 {
		leftWidth = 1
	}
	left = tuiPadRight(left, leftWidth)
	return "│" + leftColor + left + tuiReset + rightColor + right + tuiReset + "│\n"
}

func tuiSidebar(snapshot tuiSnapshot, width, height int, language ...string) []string {
	tr := tuiTranslator(language...)
	innerWidth := maxTUIWidth(width-2, 1)
	labels := make([]string, len(tuiSidebarKeys))
	for index, key := range tuiSidebarKeys {
		labels[index] = tr(key)
	}
	lines := make([]string, 0, height)
	lines = append(lines, tuiBoxTop(innerWidth))
	for index, label := range labels {
		if len(lines) >= height-1 {
			break
		}
		color := tuiDim
		prefix := "  "
		if tuiPage(index) == snapshot.Page {
			color = tuiCyan
		}
		if snapshot.FocusSidebar && index == snapshot.SelectedMenu {
			prefix = "> "
			color = tuiSelect + tuiCyan
			if tuiPage(index) == tuiPageRequests {
				// History uses a chevron as its page icon. Do not render it twice
				// when the sidebar cursor is also a chevron.
				label = tr("ui.42b4edf0c131")
			}
		}
		lines = append(lines, tuiBoxRow("  "+prefix+label, "", innerWidth, color, ""))
	}
	for len(lines) < height-1 {
		lines = append(lines, tuiBoxRow("", "", innerWidth, "", ""))
	}
	if height > 0 {
		lines = append(lines, tuiBoxBottom(innerWidth))
	}
	return lines
}

func tuiStatusDot(ownsCore, coreRunning bool) string {
	if !ownsCore {
		return "○"
	}
	if coreRunning {
		return "●"
	}
	return "○"
}

func tuiCoreStatus(ownsCore, coreRunning bool, language ...string) string {
	tr := tuiTranslator(language...)
	if !ownsCore {
		return tr("ui.08d241ad5168")
	}
	if coreRunning {
		return tr("ui.43fc9642f9a3")
	}
	return tr("ui.c06b7ed36d40")
}

func maxTUIWidth(width, minimum int) int {
	if width < minimum {
		return minimum
	}
	return width
}

func tuiPadRight(value string, width int) string {
	value = truncateTUI(value, width)
	return value + strings.Repeat(" ", maxTUIWidth(width-tuiDisplayWidth(value), 0))
}

func tuiClampAnsiLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\r"), "\n")
	line = tuiTruncateGraphemes(line, width, "")
	return line + strings.Repeat(" ", maxTUIWidth(width-tuiDisplayWidth(line), 0)) + tuiReset
}

func tuiRow(b *strings.Builder, value string, width int, selected bool, color string) {
	value = tuiPadRight(value, width-4)
	b.WriteString("│")
	if selected {
		b.WriteString(tuiSelect)
	}
	if color != "" {
		b.WriteString(color)
	}
	b.WriteString("  " + value + "  ")
	b.WriteString(tuiReset)
	b.WriteString("│\n")
}

func tuiTitle(b *strings.Builder, title, subtitle string, width int) {
	b.WriteString(tuiBoxTop(width))
	left := tuiPanelHeading(title, subtitle, width)
	b.WriteString(tuiBoxRow(left, "", width, tuiBold+tuiCyan, ""))
}

func tuiTrafficTitle(
	b *strings.Builder,
	traffic trafficSnapshot,
	peak int64,
	width int, language ...string,
) {
	tr := tuiTranslator(language...)
	b.WriteString(tuiBoxTop(width))
	line := "  " + tuiBold + tuiCyan + tr("ui.9fd866625ba6") + tuiReset +
		"  ·  " + formatTUITrafficLegend(traffic, peak, language...)
	b.WriteString("│")
	b.WriteString(tuiClampAnsiLine(line, width))
	b.WriteString("│\n")
}

func formatTUITrafficLegend(traffic trafficSnapshot, peak int64, language ...string) string {
	tr := tuiTranslator(language...)
	return fmt.Sprintf(tr("ui.41278a3a38a0"), tuiTrafficChartUpload,
		formatBytes(traffic.Up),
		tuiReset,
		tuiTrafficChartDownload,
		formatBytes(traffic.Down),
		tuiReset,
		tuiDim,
		formatBytes(peak),
		tuiReset,
	)
}

func tuiEndPanel(b *strings.Builder, width int) {
	b.WriteString(tuiBoxBottom(width))
}

func tuiEmptyPanel(b *strings.Builder, title, message string, width int) {
	tuiTitle(b, title, "", width)
	tuiRow(b, message, width, false, tuiDim)
	tuiEndPanel(b, width)
}

func drawTUIEmpty(b *strings.Builder, width int, title, message string, language ...string) {
	tuiEmptyPanel(b, title, message, width)
}
