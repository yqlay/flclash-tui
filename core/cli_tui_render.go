//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

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
	if width <= 0 || height <= 0 {
		return ""
	}
	if width < 40 || height < 10 {
		return renderTUITiny(snapshot, ownsCore, coreRunning, width, height)
	}
	snapshot.ServiceRunning = coreRunning
	snapshot.ExternalCore = !ownsCore
	if ownsCore && coreRunning && snapshot.Settings.Mode != tuiSilentMode &&
		snapshot.Settings.MixedPort <= 0 &&
		(snapshot.Status == "" || snapshot.Status == "Connected" || snapshot.Status == "Core listeners started") {
		snapshot.Status = "No proxy listener active; select Proxy port in Dashboard"
	}
	if width < 88 || height < 18 {
		return renderTUICompact(
			snapshot,
			paths,
			controllerAddress,
			ownsCore,
			coreRunning,
			width,
			height,
		)
	}
	contentWidth := width - 2
	headerHeight := 3
	footerHeight := 1
	bodyHeight := height - headerHeight - footerHeight
	sidebarWidth := minTUI(maxTUIWidth(width/5, 22), 28)
	mainOuterWidth := width - sidebarWidth - 1
	mainContentWidth := mainOuterWidth - 2
	var b strings.Builder
	b.WriteString(tuiBoxTop(contentWidth))
	headerLeft := "  FlClash  ·  terminal proxy manager"
	headerRight := "  " + tuiStatusDot(ownsCore, coreRunning) + " " + tuiCoreStatus(ownsCore, coreRunning) + "  " + truncateTUI(controllerAddress, 30) + "  "
	b.WriteString(tuiBoxRow(headerLeft, headerRight, contentWidth, tuiCyan, tuiDim))
	b.WriteString(tuiBoxBottom(contentWidth))

	page := tuiRenderPage(snapshot, paths, mainContentWidth, bodyHeight)
	sidebar := tuiSidebar(snapshot, sidebarWidth, bodyHeight)
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

	footer := "  ←→ panel  ↑↓/ws move  Enter apply  Esc nav  ? help  q exit TUI  ^C full exit"
	if width >= 110 {
		footer = "  ←→ panel  ↑↓/ws move  Enter open/apply  Esc back  d delay  ? help  q exit TUI  ^C full exit"
	}
	if snapshot.Page == tuiPageDashboard && bodyHeight < 33 {
		footer = "  ←→ panel  ↑↓/ws select  PgUp/PgDn scroll  Enter apply  q exit TUI  ^C full exit"
	}
	b.WriteString(tuiNotificationFooter(snapshot, footer, width))
	return b.String()
}

func renderTUICompact(
	snapshot tuiSnapshot,
	paths cliPaths,
	controllerAddress string,
	ownsCore,
	coreRunning bool,
	width,
	height int,
) string {
	pageName := tuiPageName(snapshot.Page)
	focus := "CONTENT"
	if snapshot.FocusSidebar {
		focus = "NAV"
	} else if snapshot.Page == tuiPageSSH {
		focus = "SSH PROFILES"
		if snapshot.SSHDashboardFocus {
			focus = "SSH DASHBOARD"
		}
	}
	header := fmt.Sprintf(
		"  FlClash · %d %s · %s · %s",
		int(snapshot.Page)+1,
		pageName,
		tuiCoreStatus(ownsCore, coreRunning),
		focus,
	)
	if width >= 72 {
		header += " · " + truncateTUI(controllerAddress, 24)
	}

	bodyHeight := height - 2
	contentWidth := width - 2
	var page string
	switch {
	case snapshot.NotificationDetailOpen ||
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
			bodyHeight,
		)
	case snapshot.FocusSidebar:
		page = renderTUICompactNavigation(
			snapshot,
			contentWidth,
			bodyHeight,
		)
	case snapshot.Page == tuiPageDashboard:
		page = renderTUICompactDashboard(
			snapshot,
			paths,
			contentWidth,
			bodyHeight,
		)
	default:
		page = tuiRenderPage(
			snapshot,
			paths,
			contentWidth,
			bodyHeight,
		)
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
	footer := "  1-9 page · ←/Esc nav · ↑↓/ws · Enter · q exit TUI · ^C full exit"
	if snapshot.FocusSidebar {
		footer = "  ↑↓/ws page · →/Enter open · 1-9 direct · q exit TUI · ^C full exit"
	} else if snapshot.Page == tuiPageDashboard {
		footer = "  ↑↓/ws select · Enter Core/flc · PgUp/PgDn · q exit TUI · ^C full exit"
	} else if snapshot.Page == tuiPageSSH {
		footer = "  Tab list/Dashboard · Enter connect · a capture · n add · q exit TUI · ^C full exit"
	}
	b.WriteString(tuiNotificationFooter(snapshot, footer, width))
	return b.String()
}

func tuiPageName(page tuiPage) string {
	switch page {
	case tuiPageDashboard:
		return "Dashboard"
	case tuiPageProxies:
		return "Proxies"
	case tuiPageProfiles:
		return "Profiles"
	case tuiPageSSH:
		return "SSH"
	case tuiPageRequests:
		return "History"
	case tuiPageConnections:
		return "Connections"
	case tuiPageLogs:
		return "Logs"
	case tuiPageTools:
		return "Settings"
	case tuiPageMaintenance:
		return "Maintenance"
	default:
		return "Unknown"
	}
}

func renderTUICompactNavigation(
	snapshot tuiSnapshot,
	width,
	height int,
) string {
	var b strings.Builder
	tuiTitle(&b, "Navigation", "↑↓/ws select · →/Enter open", width)
	limit := maxTUIWidth(height-3, 1)
	start, end := tuiVisibleRange(
		int(tuiPageCount),
		snapshot.SelectedMenu,
		limit,
	)
	for index := start; index < end; index++ {
		tuiRow(
			&b,
			fmt.Sprintf("%d  %s", index+1, tuiPageName(tuiPage(index))),
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
	for _, valueRune := range value {
		runeWidth := tuiRuneWidth(valueRune)
		if partWidth > 0 && partWidth+runeWidth > width {
			parts = append(parts, part.String())
			part.Reset()
			partWidth = 0
		}
		part.WriteRune(valueRune)
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

func renderTUITooSmall(width, height int) string {
	lines := []string{
		"",
		"  FlClash TUI",
		"",
		fmt.Sprintf("  Terminal: %dx%d", width, height),
		"  Resize to at least 40x10",
		"",
		"  q exit TUI · Ctrl+C full exit",
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
	height int,
) string {
	lines := []string{
		"FlClash " + tuiCoreStatus(ownsCore, coreRunning),
		fmt.Sprintf("%d %s", int(snapshot.Page)+1, tuiPageName(snapshot.Page)),
	}
	if snapshot.NotificationDetailOpen {
		lines = append(lines, tuiNotificationTinyDetail(snapshot, width)...)
	} else if notification, ok := tuiLatestUnreadNotification(snapshot.Notifications); ok {
		lines = append(lines, tuiNotificationTinySummary(notification, width))
	}
	if height > 0 && len(lines) >= height {
		lines = lines[:height-1]
	}
	lines = append(lines, "q exit TUI · ^C full exit")
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

func tuiRenderPage(snapshot tuiSnapshot, paths cliPaths, width, height int) string {
	var b strings.Builder
	if snapshot.NotificationDetailOpen {
		drawTUINotificationDetails(&b, snapshot, width, height)
	} else if snapshot.DangerConfirmOpen {
		drawTUIDangerConfirm(&b, snapshot, width)
	} else if snapshot.ProfileDelete.Open {
		drawTUIProfileDeleteConfirm(&b, snapshot.ProfileDelete, width)
	} else if snapshot.SSHForm.DeleteConfirmOpen {
		drawTUISSHDeleteConfirm(&b, snapshot.SSHForm, width)
	} else if snapshot.SSHCredentialPrompt.Open {
		drawTUISSHCredentialPrompt(&b, snapshot.SSHCredentialPrompt, width)
	} else if snapshot.SSHForm.Open {
		drawTUISSHForm(&b, snapshot.SSHForm, width, height)
	} else if snapshot.SelectionTitle != "" {
		drawTUISelection(&b, snapshot, width)
	} else if snapshot.InputTitle != "" {
		drawTUIInput(&b, snapshot, width)
	} else if snapshot.ShowHelp {
		drawTUIHelp(&b, width, height)
	} else if snapshot.Page == tuiPageRequests {
		drawTUIRequests(&b, snapshot, width, height)
	} else if snapshot.Page == tuiPageConnections {
		drawTUIConnections(&b, snapshot, width, height)
	} else if snapshot.Page == tuiPageLogs {
		drawTUILogs(&b, snapshot, width, height)
	} else if snapshot.Page == tuiPageProfiles {
		drawTUIProfiles(&b, snapshot, width, height)
	} else if snapshot.Page == tuiPageSSH {
		drawTUISSH(&b, snapshot, width, height)
	} else if snapshot.Page == tuiPageTools {
		drawTUITools(&b, snapshot, width, height)
	} else if snapshot.Page == tuiPageMaintenance {
		drawTUIMaintenance(&b, snapshot, width, height)
	} else if snapshot.Page == tuiPageDashboard {
		if height < 33 {
			return renderTUICompactDashboard(
				snapshot,
				paths,
				width,
				height,
			)
		}
		drawTUIDashboard(&b, snapshot, paths, width, height)
	} else if snapshot.Page == tuiPageProxies &&
		snapshot.ProxyView == tuiProxyViewProviders {
		drawTUIProviders(&b, snapshot, width, height)
	} else if snapshot.Page == tuiPageProxies && len(snapshot.Groups) == 0 {
		drawTUIEmpty(
			&b,
			width,
			"Proxy groups",
			"No selectable groups · press ] for Providers or r to refresh",
		)
	} else {
		drawTUIProxies(&b, snapshot, width, height)
	}
	return b.String()
}

func drawTUISSHCredentialPrompt(
	b *strings.Builder,
	prompt tuiSSHCredentialPromptView,
	width int,
) {
	tuiTitle(b, "Unlock SSH private key", "Enter connect · Esc cancel", width)
	tuiRow(b, "Profile       "+prompt.Profile, width, false, tuiCyan)
	tuiRow(b, "Identity      "+prompt.Identity, width, false, tuiDim)
	tuiRow(b, "Passphrase    "+prompt.Value, width, true, tuiCyan)
	tuiRow(b, "One-time credential · it will not be saved", width, false, tuiDim)
	tuiEndPanel(b, width)
}

func drawTUIDangerConfirm(b *strings.Builder, snapshot tuiSnapshot, width int) {
	tuiTitle(b, snapshot.DangerConfirmTitle, "Enter confirm · Esc cancel", width)
	for _, line := range tuiWrapText(snapshot.DangerConfirmMessage, maxTUIWidth(width-4, 1)) {
		tuiRow(b, line, width, false, tuiYellow)
	}
	tuiRow(b, "This operation cannot be undone in the current session.", width, false, tuiRed)
	tuiEndPanel(b, width)
}

func drawTUISelection(b *strings.Builder, snapshot tuiSnapshot, width int) {
	tuiTitle(b, snapshot.SelectionTitle, "Enter confirm · Esc cancel", width)
	for index, option := range snapshot.SelectionOptions {
		label := option
		if strings.EqualFold(option, snapshot.Settings.Mode) {
			label += "  (current)"
		}
		tuiRow(b, label, width, index == snapshot.SelectedOption, "")
	}
	tuiEndPanel(b, width)
	if snapshot.SelectionHint != "" {
		tuiEmptyPanel(b, "Selection help", snapshot.SelectionHint, width)
	}
}

func drawTUIInput(b *strings.Builder, snapshot tuiSnapshot, width int) {
	tuiTitle(b, snapshot.InputTitle, "Enter confirm · Esc cancel", width)
	tuiRow(b, snapshot.InputValue, width, true, "")
	tuiEndPanel(b, width)
	if snapshot.InputHint != "" {
		tuiEmptyPanel(b, "Input help", snapshot.InputHint, width)
	}
}

func drawTUISSHDeleteConfirm(
	b *strings.Builder,
	form tuiSSHFormView,
	width int,
) {
	tuiTitle(b, "Delete SSH profile", "Enter confirm · Esc cancel", width)
	tuiRow(
		b,
		"Delete "+form.DeleteName+"? This also disconnects its active tunnel.",
		width,
		true,
		tuiRed,
	)
	tuiEndPanel(b, width)
}

func drawTUIProfileDeleteConfirm(
	b *strings.Builder,
	profile tuiProfileDeleteView,
	width int,
) {
	tuiTitle(b, "Delete Profile", "Enter confirm · Esc cancel", width)
	tuiRow(
		b,
		"Delete "+profile.Name+" ("+profile.Kind+")? The saved YAML and linked metadata will be removed.",
		width,
		true,
		tuiRed,
	)
	tuiEndPanel(b, width)
}

func drawTUISSHForm(
	b *strings.Builder,
	form tuiSSHFormView,
	width,
	height int,
) {
	title := "Add SSH profile"
	subtitle := "↑↓/Tab select · Enter edit/confirm · x remove option · Esc cancel"
	if form.Existing {
		title = "Edit SSH profile"
	}
	if form.ReadOnly {
		title = "SSH profile details · CONNECTED · READ ONLY"
		subtitle = "↑↓ select · disconnect before editing · Esc close"
	}
	tuiTitle(
		b,
		title,
		subtitle,
		width,
	)
	rows := make([]struct {
		label string
		color string
	}, 0, len(form.Options)+12)
	rows = append(rows,
		struct {
			label string
			color string
		}{label: "Name                   " + form.Name},
		struct {
			label string
			color string
		}{label: "SSH username           " + form.Username},
		struct {
			label string
			color string
		}{label: "SSH host               " + form.Host},
		struct {
			label string
			color string
		}{label: "Jump host              " + cliDisplayValue(form.Jump)},
		struct {
			label string
			color string
		}{label: fmt.Sprintf("SSH port               %d", form.Port)},
		struct {
			label string
			color string
		}{label: "Local SOCKS            " + formatTUISSHLocalPort(form.LocalPort)},
		struct {
			label string
			color string
		}{label: "Identity(private key)  " + cliDisplayValue(form.Identity)},
	)
	passphrase := "not saved · Enter set"
	switch {
	case form.Identity == "":
		passphrase = "not applicable · select Identity first"
	case form.IdentityError != "":
		passphrase = "key unavailable/invalid · check Identity"
	case form.IdentityKind == cliSSHIdentityUnencrypted:
		passphrase = "not required · private key is unencrypted"
	case form.IdentityKind == cliSSHIdentityEncrypted && !form.PassphraseSet:
		passphrase = "ask once when connecting · not saved"
	case form.PassphraseSet:
		passphrase = "******** · Enter replace · c clear"
	}
	if form.ReadOnly && form.PassphraseSet {
		passphrase = "******** · set · read only"
	} else if form.ReadOnly {
		passphrase = "not saved · read only"
	}
	if form.PassphraseChanged {
		passphrase = "******** · replacement staged · c clear"
	}
	if form.PassphraseCleared {
		passphrase = "clear on save · Enter set"
	}
	rows = append(rows, struct {
		label string
		color string
	}{label: "Key passphrase         " + passphrase})
	password := "not saved · Enter set"
	if form.PasswordSet {
		password = "******** · Enter replace · c clear"
	}
	if form.ReadOnly && form.PasswordSet {
		password = "******** · set · read only"
	} else if form.ReadOnly {
		password = "not saved · read only"
	}
	if form.PasswordChanged {
		password = "******** · replacement staged · c clear"
	}
	if form.PasswordCleared {
		password = "clear on save · Enter set"
	}
	rows = append(rows, struct {
		label string
		color string
	}{label: "SSH password           " + password})
	for index, option := range form.Options {
		rows = append(rows, struct {
			label string
			color string
		}{label: fmt.Sprintf("Option %-3d   %s", index+1, option)})
	}
	addOptionLabel := "+ Add OpenSSH option (KEY=VALUE)"
	addOptionColor := tuiCyan
	saveLabel := "Save profile"
	saveColor := tuiGreen
	if form.ReadOnly {
		addOptionLabel = "Options are read only while connected"
		addOptionColor = tuiDim
		saveLabel = "Save unavailable · disconnect before editing"
		saveColor = tuiDim
	}
	rows = append(rows,
		struct {
			label string
			color string
		}{label: addOptionLabel, color: addOptionColor},
		struct {
			label string
			color string
		}{label: saveLabel, color: saveColor},
	)
	if form.Existing {
		rows = append(rows, struct {
			label string
			color string
		}{label: "Delete profile", color: tuiRed})
	}
	cancelLabel := "Cancel"
	if form.ReadOnly {
		cancelLabel = "Close details"
	}
	rows = append(rows, struct {
		label string
		color string
	}{label: cancelLabel, color: tuiYellow})
	if form.FieldEditing && form.Selected >= 0 && form.Selected < len(rows) {
		label := "Value        " + form.FieldInput
		if form.Selected == tuiSSHFormPassphraseRow {
			label = "Key passphrase         " + form.FieldInput
			if form.PassphraseConfirm {
				label = "Confirm passphrase     " + form.FieldInput
			}
		} else if form.Selected == tuiSSHFormPasswordRow {
			label = "SSH password           " + form.FieldInput
			if form.PasswordConfirm {
				label = "Confirm password       " + form.FieldInput
			}
		} else if form.Selected == tuiSSHFormJumpRow {
			label = "Jump host              " + form.FieldInput
		}
		rows[form.Selected].label = label
	}
	limit := maxTUIWidth(height-4, 1)
	start, end := tuiVisibleRange(len(rows), form.Selected, limit)
	for index := start; index < end; index++ {
		tuiRow(
			b,
			rows[index].label,
			width,
			index == form.Selected,
			rows[index].color,
		)
	}
	tuiEndPanel(b, width)
}

func formatTUISSHLocalPort(port int) string {
	if port <= 0 {
		return "auto"
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

func tuiSidebar(snapshot tuiSnapshot, width, height int) []string {
	innerWidth := maxTUIWidth(width-2, 1)
	labels := []string{
		"@  Dashboard",
		"#  SSH",
		"*  Proxies",
		"+  Profiles",
		">  History",
		"~  Connections",
		"=  Logs",
		":  Settings",
		"!  Maintenance",
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
			if label == ">  History" {
				// History uses a chevron as its page icon. Do not render it twice
				// when the sidebar cursor is also a chevron.
				label = "   History"
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

func tuiCoreStatus(ownsCore, coreRunning bool) string {
	if !ownsCore {
		return "EXTERNAL CORE"
	}
	if coreRunning {
		return "CORE RUNNING"
	}
	return "CORE STOPPED"
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

func tuiDisplayWidth(value string) int {
	width := 0
	for _, r := range value {
		width += tuiRuneWidth(r)
	}
	return width
}

func tuiRuneWidth(value rune) int {
	if value == 0 || value < 32 {
		return 0
	}
	if value == '\u200d' || value == '\ufe0e' || value == '\ufe0f' ||
		unicode.Is(unicode.Mn, value) || unicode.Is(unicode.Me, value) || unicode.Is(unicode.Cf, value) {
		return 0
	}
	if value >= 0x1100 && (value <= 0x115f || value == 0x2329 || value == 0x232a ||
		(value >= 0x2e80 && value <= 0xa4cf) || (value >= 0xac00 && value <= 0xd7a3) ||
		(value >= 0xf900 && value <= 0xfaff) || (value >= 0xfe10 && value <= 0xfe6f) ||
		(value >= 0xff00 && value <= 0xff60) || (value >= 0xffe0 && value <= 0xffe6) ||
		(value >= 0x1f1e6 && value <= 0x1f1ff) || (value >= 0x1f300 && value <= 0x1faff)) {
		return 2
	}
	return 1
}

func tuiClampAnsiLine(line string, width int) string {
	if width <= 0 {
		return ""
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\r"), "\n")
	var b strings.Builder
	visibleWidth := 0
	for index := 0; index < len(line); {
		if line[index] == '\x1b' {
			start := index
			index++
			if index < len(line) && line[index] == '[' {
				index++
				for index < len(line) {
					value := line[index]
					index++
					if value >= '@' && value <= '~' {
						break
					}
				}
			}
			b.WriteString(line[start:index])
			continue
		}
		value, size := utf8.DecodeRuneInString(line[index:])
		runeWidth := tuiRuneWidth(value)
		if visibleWidth+runeWidth > width {
			break
		}
		b.WriteString(line[index : index+size])
		visibleWidth += runeWidth
		index += size
	}
	if visibleWidth < width {
		b.WriteString(strings.Repeat(" ", width-visibleWidth))
	}
	b.WriteString(tuiReset)
	return b.String()
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
	left := "  " + title
	if subtitle != "" {
		left += "  ·  " + subtitle
	}
	b.WriteString(tuiBoxRow(left, "", width, tuiBold+tuiCyan, ""))
}

func tuiTrafficTitle(
	b *strings.Builder,
	traffic trafficSnapshot,
	peak int64,
	width int,
) {
	b.WriteString(tuiBoxTop(width))
	line := "  " + tuiBold + tuiCyan + "Live traffic" + tuiReset +
		"  ·  " + formatTUITrafficLegend(traffic, peak)
	b.WriteString("│")
	b.WriteString(tuiClampAnsiLine(line, width))
	b.WriteString("│\n")
}

func formatTUITrafficLegend(traffic trafficSnapshot, peak int64) string {
	return fmt.Sprintf(
		"%s↑ %s/s%s · %s↓ %s/s%s%s · peak %s/s · 30 samples%s",
		tuiTrafficChartUpload,
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

func drawTUIEmpty(b *strings.Builder, width int, title, message string) {
	tuiEmptyPanel(b, title, message, width)
}
