//go:build linux && !cgo && cli

package main

import (
	"core/internal/i18n"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestTUINativeLanguageCellWidths(t *testing.T) {
	// These expectations come from xterm's Unicode 11 cell buffer, not from
	// the same width implementation used to render the page.
	widths := map[string]int{
		"en": 7, "zh-Hans": 8, "zh-Hant": 8, "ru": 7, "fa": 5,
		"my": 5, "tk": 9, "ur": 4, "hi": 5, "es": 7, "ar": 7,
		"fr": 8, "bn": 5, "pt": 9, "id": 16, "de": 7, "ja": 6, "pcm": 5,
	}
	for _, item := range i18n.Languages() {
		t.Run(item.Code, func(t *testing.T) {
			for _, value := range []string{item.Name, tuiCyan + item.Name + tuiReset} {
				if got := tuiDisplayWidth(value); got != widths[item.Code] {
					t.Fatalf("%q occupies %d cells, want %d", value, got, widths[item.Code])
				}
			}
		})
	}
}

func TestTUISpacingMarksAndANSIWidths(t *testing.T) {
	for _, test := range []struct {
		value string
		width int
	}{
		{"বাংলা", 5},
		{"কা", 2},
		{"ক্ষ", 2},
		{"कि", 2},
		{"क्ष", 2},
		{"ကိ", 1},
		{"\u09be", 1},
		{"\u09cd", 0},
		{"e\u0301", 1},
		{"节点", 4},
		{"👩‍💻", 2},
		{"🇨🇳", 2},
		{"\x1b]8;;https://example.invalid/বাংলা\x1b\\link\x1b]8;;\x1b\\", 4},
		{"\x1bP1;2qবাংলা\x1b\\text", 4},
	} {
		if got := tuiDisplayWidth(test.value); got != test.width {
			t.Errorf("width(%q) = %d, want %d", test.value, got, test.width)
		}
	}
	if tuiRuneWidth('\u09be') != 1 || tuiRuneWidth('\u09cd') != 0 {
		t.Fatal("rune and string measurements disagree")
	}
}

func TestTUIIndicCellClippingAndWrapping(t *testing.T) {
	for _, cluster := range []string{"কা", "ক্ষ", "कि", "क्ष", "e\u0301", "👩‍💻"} {
		cells := tuiDisplayWidth(cluster)
		for width := 1; width <= 12; width++ {
			line := tuiClampAnsiLine(tuiRed+strings.Repeat(cluster, 6)+tuiReset, width)
			plain := strings.TrimRight(ansi.Strip(line), " ")
			want := strings.Repeat(cluster, minTUI(width/cells, 6))
			if plain != want || tuiDisplayWidth(line) != width || !strings.HasSuffix(line, tuiReset) {
				t.Fatalf("cluster %q at %d cells: %q, want %q", cluster, width, line, want)
			}
			parts := tuiWrapWord(strings.Repeat(cluster, 6), width)
			for _, part := range parts {
				if tuiDisplayWidth(part) > width {
					t.Fatalf("wrapped cluster overflow: %q", part)
				}
			}
			if width >= cells && strings.Join(parts, "") != strings.Repeat(cluster, 6) {
				t.Fatal("wrapping lost a complete cluster")
			}
		}
	}
	if got := truncateTUI("বাংলা", 4); got != "বাং…" {
		t.Fatalf("spacing-mark clipping = %q, want %q", got, "বাং…")
	}
}

func TestTUILanguageOptionKeepsNativeNameAndCode(t *testing.T) {
	for _, item := range i18n.Languages() {
		for _, width := range []int{34, 54, 80} {
			label := tuiLanguageOptionLabel(item, item.Code, width)
			if !strings.Contains(label, item.Name) || !strings.Contains(label, "["+item.Code+"] ✓") || tuiDisplayWidth(label) > width {
				t.Fatalf("%s at %d cells lost native name/code: %q", item.Code, width, label)
			}
		}
		for width := 1; width <= 32; width++ {
			if got := tuiLanguageOptionLabel(item, item.Code, width); tuiDisplayWidth(got) > width {
				t.Fatalf("%s option overflows %d cells: %q", item.Code, width, got)
			}
		}
	}
	if got := tuiLanguageOptionLabel(i18n.Lookup("en"), "en", 80); strings.Contains(got, "English · English") {
		t.Fatal("English label was duplicated")
	}
	if got := tuiLanguageOptionLabel(i18n.Lookup("zh-Hant"), "en", 25); got != "繁體中文 [zh-Hant]" {
		t.Fatalf("description did not yield space first: %q", got)
	}
}

func TestTUILanguagePickerAllSelectionsFit(t *testing.T) {
	for _, language := range i18n.Languages() {
		for selected, item := range i18n.Languages() {
			for _, size := range [][2]int{{40, 10}, {64, 14}, {87, 21}, {88, 18}, {120, 40}, {180, 60}} {
				snapshot := tuiSnapshot{Page: tuiPageTools, Language: language.Code, LanguageSelectionOpen: true, SelectedLanguage: selected}
				layout := tuiLayoutAtSize(size[0], size[1], language.Code)
				var output strings.Builder
				drawTUILanguageSelection(&output, snapshot, layout.ContentWidth, layout.PageHeight)
				lines := strings.Split(strings.TrimSuffix(output.String(), "\n"), "\n")
				if len(lines) > layout.PageHeight || !strings.HasPrefix(lines[len(lines)-1], "└") {
					t.Fatalf("%s selection %s at %v lost bottom border", language.Code, item.Code, size)
				}
				selectedVisible := false
				for _, line := range lines {
					if tuiDisplayWidth(line) != layout.ContentWidth+2 {
						t.Fatalf("%s selection %s: wrong row width %q", language.Code, item.Code, line)
					}
					if strings.Contains(line, tuiSelect) {
						selectedVisible = strings.Contains(line, item.Name) && strings.Contains(line, "["+item.Code+"]")
					}
				}
				if !selectedVisible || !strings.Contains(output.String(), "Enter") || !strings.Contains(output.String(), "Esc") {
					t.Fatalf("%s selection %s at %v lost selection/controls", language.Code, item.Code, size)
				}
			}
		}
	}
}
