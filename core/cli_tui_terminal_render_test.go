//go:build linux && !cgo && cli

package main

import (
	"bytes"
	"core/internal/i18n"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

type tuiTerminalRenderFrame struct {
	Name   string   `json:"name"`
	Width  int      `json:"width"`
	Height int      `json:"height"`
	Data   string   `json:"data"`
	Lines  []string `json:"lines"`
	marker string
}

func tuiTerminalTestFrame(name, view string, width, height int) tuiTerminalRenderFrame {
	var output bytes.Buffer
	writeTUIFrame(&output, view)
	return tuiTerminalRenderFrame{
		Name: name, Width: width, Height: height, Data: output.String(),
		Lines: strings.Split(ansi.Strip(view), "\n"),
	}
}

// A real Bubble Tea renderer with an inert Init exercises repaint behavior
// without starting Core, SSH, background refreshes, or network probes.
type tuiTerminalRenderModel struct {
	model    *tuiModel
	states   chan tuiTerminalRenderFrame
	revision int
}

func (*tuiTerminalRenderModel) Init() tea.Cmd { return nil }

func (model *tuiTerminalRenderModel) marker() string {
	return fmt.Sprintf("\x1b]999;flclash-render-test-%d\x07", model.revision)
}

func (model *tuiTerminalRenderModel) View() string {
	// An ignored OSC marker lets the driver wait for this exact renderer
	// flush, not a timer or an older startup/resize frame. It occupies no
	// terminal cells and leaves the application's visible view unchanged.
	return model.model.View() + model.marker()
}

func (model *tuiTerminalRenderModel) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	model.revision++
	_, command := model.model.Update(message)
	if command != nil {
		// The only commands our driver requests are language preference saves
		// into t.TempDir(). Apply the result before collecting the checkpoint.
		_, _ = model.model.Update(command())
	}
	frame := tuiTerminalTestFrame("", model.View(), model.model.width, model.model.height)
	frame.marker = model.marker()
	model.states <- frame
	return model, nil
}

type tuiTerminalRenderOutput struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	frames chan string
}

func (output *tuiTerminalRenderOutput) Write(data []byte) (int, error) {
	output.mu.Lock()
	count, err := output.buffer.Write(data)
	output.mu.Unlock()
	if bytes.Contains(data, []byte("\x1b]999;flclash-render-test-")) {
		select {
		case output.frames <- string(data):
		default:
		}
	}
	return count, err
}

func (output *tuiTerminalRenderOutput) take() string {
	output.mu.Lock()
	defer output.mu.Unlock()
	result := output.buffer.String()
	output.buffer.Reset()
	return result
}

func collectTUITerminalTransitions(t *testing.T) []tuiTerminalRenderFrame {
	t.Helper()
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, true)
	model.snapshot.Page = tuiPageTools
	model.snapshot.SelectedTool = tuiSettingsLanguageRow
	model.snapshot.FocusSidebar = false
	model.width, model.height = 120, 40
	model.busy = true
	wrapper := &tuiTerminalRenderModel{model: model, states: make(chan tuiTerminalRenderFrame, 4)}
	output := &tuiTerminalRenderOutput{frames: make(chan string, 4)}
	program := tea.NewProgram(wrapper, tea.WithInput(nil), tea.WithOutput(output), tea.WithAltScreen(), tea.WithoutSignalHandler(), tea.WithFPS(120))
	done := make(chan error, 1)
	go func() { _, err := program.Run(); done <- err }()
	t.Cleanup(func() {
		program.Quit()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("terminal fixture renderer: %v", err)
			}
		case <-time.After(5 * time.Second):
			program.Kill()
			t.Error("terminal fixture renderer did not stop")
		}
	})
	var frames []tuiTerminalRenderFrame
	step := func(name string, message tea.Msg) {
		t.Helper()
		program.Send(message)
		var frame tuiTerminalRenderFrame
		select {
		case frame = <-wrapper.states:
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: model did not reach checkpoint", name)
		}
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for rendered := false; !rendered; {
			select {
			case data := <-output.frames:
				rendered = strings.Contains(data, frame.marker)
			case <-timer.C:
				t.Fatalf("%s: renderer did not emit a frame", name)
			}
		}
		frame.Name, frame.Data = name, output.take()
		frames = append(frames, frame)
	}
	step("settings", tea.WindowSizeMsg{Width: 120, Height: 40})
	step("open language picker", tea.KeyMsg{Type: tea.KeyEnter})
	for index := 0; index < len(i18n.Languages()); index++ {
		step(fmt.Sprintf("move down %d", index), tea.KeyMsg{Type: tea.KeyDown})
	}
	for index := 0; index < len(i18n.Languages()); index++ {
		step(fmt.Sprintf("move up %d", index), tea.KeyMsg{Type: tea.KeyUp})
	}
	for _, size := range [][2]int{{40, 10}, {64, 14}, {88, 18}, {180, 60}, {120, 40}} {
		step(fmt.Sprintf("resize %v", size), tea.WindowSizeMsg{Width: size[0], Height: size[1]})
	}
	step("cancel picker", tea.KeyMsg{Type: tea.KeyEsc})
	step("reopen picker", tea.KeyMsg{Type: tea.KeyEnter})
	step("select Simplified Chinese", tea.KeyMsg{Type: tea.KeyDown})
	step("save language", tea.KeyMsg{Type: tea.KeyEnter})
	step("failed save keeps language", tuiLanguageSavedMsg{Language: "bn", Err: errors.New("test disk full")})
	for _, item := range i18n.Languages() {
		step("apply "+item.Code, tuiLanguageSavedMsg{Language: item.Code})
		step("open in "+item.Code, tea.KeyMsg{Type: tea.KeyEnter})
		step("close in "+item.Code, tea.KeyMsg{Type: tea.KeyEsc})
	}
	return frames
}

func TestTUILanguageTerminalEmulator(t *testing.T) {
	required := os.Getenv("FLCLASH_TUI_REQUIRE_TERMINAL_TESTS") == "1" || os.Getenv("FLCLASH_TUI_XTERM_MODULES") != ""
	node, err := exec.LookPath("node")
	if err != nil {
		if required {
			t.Fatal("Node.js is required for terminal regression tests")
		}
		t.Skip("Node.js unavailable; see internal/i18n/README.md for terminal regression tests")
	}
	script, err := filepath.Abs(filepath.Join("..", "packaging", "terminal-tests", "language-render.cjs"))
	if err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(node, script, "--check").CombinedOutput(); err != nil {
		if !required {
			if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 78 {
				t.Skip(strings.TrimSpace(string(output)))
			}
		}
		t.Fatalf("terminal regression dependencies: %v: %s", err, output)
	}
	fixtures := struct {
		Frames []tuiTerminalRenderFrame `json:"frames"`
		Stream []tuiTerminalRenderFrame `json:"stream"`
	}{}
	for _, language := range i18n.Languages() {
		for selected, item := range i18n.Languages() {
			for _, size := range [][2]int{{40, 10}, {88, 18}, {120, 40}, {180, 60}} {
				snapshot := tuiSnapshot{Page: tuiPageTools, Language: language.Code, LanguageSelectionOpen: true, SelectedLanguage: selected}
				view := renderTUIAtSize(snapshot, cliPaths{}, "private Unix socket", true, false, size[0], size[1])
				name := fmt.Sprintf("%s picker %s %dx%d", language.Code, item.Code, size[0], size[1])
				fixtures.Frames = append(fixtures.Frames, tuiTerminalTestFrame(name, view, size[0], size[1]))
			}
		}
	}
	fixtures.Stream = collectTUITerminalTransitions(t)
	data, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "frames.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(node, script, path).CombinedOutput()
	if err != nil {
		t.Fatalf("terminal regression: %v\n%s", err, output)
	}
	t.Log(strings.TrimSpace(string(output)))
}
