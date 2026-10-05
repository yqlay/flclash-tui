//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"core/internal/i18n"
	tea "github.com/charmbracelet/bubbletea"
)

func tuiTranslator(language ...string) func(string) string {
	code := tuiLanguageCode(language...)
	return func(key string) string { return i18n.Text(code, key) }
}

func tuiLanguageCode(language ...string) string {
	if len(language) != 0 {
		return i18n.Normalize(language[0])
	}
	return "en"
}

// tuiMessage carries translation-independent semantics and opaque data arguments.
// Status remains canonical English for diagnostics and backwards-compatible callers.
type tuiMessage struct {
	Key   string
	Args  []any
	Raw   string
	Parts []tuiMessage
	Info  i18n.SourceInfo
}

func newTUIMessage(key string, args ...any) tuiMessage {
	return tuiMessage{Key: key, Args: append([]any(nil), args...), Info: i18n.Info(key)}
}

func (message tuiMessage) text(language string) string {
	if len(message.Parts) != 0 {
		var b strings.Builder
		for _, part := range message.Parts {
			b.WriteString(part.text(language))
		}
		return b.String()
	}
	if message.Key == "" {
		return message.Raw
	}
	arguments := append([]any(nil), message.Args...)
	for index, argument := range arguments {
		if nested, ok := argument.(tuiMessage); ok {
			arguments[index] = nested.text(language)
		}
	}
	return i18n.Text(language, message.Key, arguments...)
}

func (snapshot *tuiSnapshot) setStatus(message tuiMessage) {
	snapshot.StatusMessage = message
	snapshot.Status = message.text("en")
}

func (snapshot *tuiSnapshot) appendStatus(message tuiMessage) {
	previous := snapshot.currentMessage()
	info := previous.Info
	if message.Info.Level == "ERROR" {
		info = message.Info
	}
	snapshot.setStatus(tuiMessage{Parts: []tuiMessage{previous, message}, Info: info})
}

func (snapshot tuiSnapshot) currentMessage() tuiMessage {
	if snapshot.StatusMessage.text("en") == snapshot.Status {
		return snapshot.StatusMessage
	}
	return tuiMessage{Raw: snapshot.Status}
}

func (snapshot tuiSnapshot) controllerError() bool {
	return snapshot.currentMessage().Info.Kind == "controller_error"
}

func (notification tuiNotification) displayMessage(language string) string {
	if strings.TrimSpace(notification.text.text("en")) == notification.message {
		return strings.TrimSpace(notification.text.text(language))
	}
	return notification.message
}

func (notification tuiNotification) displayTitle(language string) string {
	// Some notices have a specific title, not a generic operation title.
	if notification.title != "" && notification.title != tuiNotificationTitle(notification.level, notification.progress) {
		if notification.title == "Shared backend" {
			return i18n.Text(language, "notice.shared_backend")
		}
		return notification.title
	}
	key := "notification.info"
	if notification.progress {
		key = "notification.progress"
	} else {
		switch notification.level {
		case tuiNotificationSuccess:
			key = "notification.success"
		case tuiNotificationWarning:
			key = "notification.warning"
		case tuiNotificationError:
			key = "notification.error"
		}
	}
	return i18n.Text(language, key)
}

const tuiPreferencesFilename = ".flclash-tui-preferences.json"

type tuiPreferences struct {
	Language string `json:"language"`
}

func loadTUIPreferences(homeDir string) (tuiPreferences, error) {
	preferences := tuiPreferences{Language: "en"}
	data, err := os.ReadFile(filepath.Join(homeDir, tuiPreferencesFilename))
	if os.IsNotExist(err) {
		return preferences, nil
	}
	if err != nil {
		return preferences, err
	}
	if err := json.Unmarshal(data, &preferences); err != nil {
		return tuiPreferences{Language: "en"}, err
	}
	preferences.Language = i18n.Normalize(preferences.Language)
	return preferences, nil
}

func saveTUILanguage(homeDir, language string) error {
	if i18n.Normalize(language) != language {
		return errors.New("unsupported TUI language")
	}
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		return err
	}
	lockPath := filepath.Join(homeDir, ".flclash-tui-preferences.lock")
	owner := cliProcessOwner{Kind: "tui-preferences", PID: os.Getpid(), HomeDir: homeDir, StartedAt: time.Now()}
	var lock *cliFileLock
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for {
		lock, err = acquireCLIFileLock(lockPath, owner)
		var busy *cliLockBusyError
		if err == nil || !errors.As(err, &busy) || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	defer lock.release()
	values := map[string]json.RawMessage{}
	path := filepath.Join(homeDir, tuiPreferencesFilename)
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &values); err != nil {
			return fmt.Errorf("read TUI preferences: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if values == nil {
		values = map[string]json.RawMessage{}
	}
	value, _ := json.Marshal(language)
	values["language"] = value
	data, err := json.MarshalIndent(values, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(homeDir, ".flclash-tui-preferences-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

type tuiLanguageSavedMsg struct {
	Language string
	Err      error
}

func (m *tuiModel) beginLanguageSelection() {
	m.languageSelectionOpen = true
	m.selectedLanguage = 0
	for index, item := range i18n.Languages() {
		if item.Code == i18n.Normalize(m.snapshot.Language) {
			m.selectedLanguage = index
		}
	}
}

func (m *tuiModel) handleLanguageSelection(message tea.KeyMsg) tea.Cmd {
	key, ok := tuiKeyFromTea(message)
	if !ok {
		return nil
	}
	switch key {
	case tuiKeyUp:
		m.selectedLanguage = wrapTUIIndex(m.selectedLanguage, -1, len(i18n.Languages()))
	case tuiKeyDown:
		m.selectedLanguage = wrapTUIIndex(m.selectedLanguage, 1, len(i18n.Languages()))
	case tuiKeyBack:
		m.languageSelectionOpen = false
	case tuiKeySelect:
		if m.languageSaving {
			return nil
		}
		language := i18n.Languages()[m.selectedLanguage].Code
		m.languageSelectionOpen = false
		m.languageSaving = true
		homeDir := m.paths.HomeDir
		return func() tea.Msg {
			return tuiLanguageSavedMsg{Language: language, Err: saveTUILanguage(homeDir, language)}
		}
	case tuiKeyQuit, tuiKeyInterrupt:
		m.languageSelectionOpen = false
		return m.handleKey(key)
	}
	return nil
}

func tuiLanguageLabel(language string) string {
	item := i18n.Lookup(language)
	label := "Language"
	if item.Code != "en" {
		label += " / " + i18n.Text(item.Code, "language.label")
	}
	return label + "  " + item.Name + " [" + item.Code + "]"
}

func tuiTrafficConfirmMessage(source, kind string) tuiMessage {
	return newTUIMessage("danger." + kind + "." + normalizeTrafficSource(source))
}

func drawTUILanguageSelection(b *strings.Builder, snapshot tuiSnapshot, width, height int) {
	title := "Language"
	if label := i18n.Text(snapshot.Language, "language.label"); label != title {
		title += " / " + label
	}
	tuiTitle(b, title, i18n.Text(snapshot.Language, "language.choose"), width)
	items := i18n.Languages()
	compatibility := tuiWrapText(i18n.Text(snapshot.Language, "language.terminal"), maxTUIWidth(width-4, 1))
	if len(compatibility) > 3 {
		compatibility = compatibility[:3]
	}
	if height < 12 {
		compatibility = nil
	}
	limit := height - 4
	if len(compatibility) > 0 {
		limit -= 3 + len(compatibility)
	}
	limit = maxTUIWidth(limit, 1)
	start, end := tuiVisibleRange(len(items), snapshot.SelectedLanguage, limit)
	for index := start; index < end; index++ {
		item := items[index]
		label := tuiLanguageOptionLabel(item, snapshot.Language, width-4)
		tuiRow(b, label, width, index == snapshot.SelectedLanguage, "")
	}
	tuiRow(b, tuiOverlayHint(i18n.Text(snapshot.Language, "language.choose"), width-4), width, false, tuiDim)
	tuiEndPanel(b, width)
	if len(compatibility) > 0 {
		tuiTitle(b, i18n.Text(snapshot.Language, "language.compatibility"), "", width)
		for _, line := range compatibility {
			tuiRow(b, line, width, false, tuiDim)
		}
		tuiEndPanel(b, width)
	}
}

func tuiLanguageOptionLabel(item i18n.Language, language string, width int) string {
	suffix := " [" + item.Code + "]"
	if item.Code == i18n.Normalize(language) {
		suffix += " ✓"
	}
	label := item.Name
	if item.English != item.Name {
		label += " · " + item.English
	}
	if tuiDisplayWidth(label+suffix) <= width {
		return label + suffix
	}
	// English descriptions yield space before the native name or language code.
	budget := width - tuiDisplayWidth(suffix)
	if budget <= 0 {
		return truncateTUI(strings.TrimSpace(suffix), width)
	}
	return truncateTUI(item.Name, budget) + suffix
}
