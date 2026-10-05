//go:build linux && !cgo && cli

package main

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
	"github.com/clipperhouse/uax29/v2/graphemes"
)

// Editing keeps rune offsets, but all movement and removal uses grapheme
// boundaries. Data values are never reversed or reshaped for RTL display.
func tuiGraphemeBoundaries(value []rune) []int {
	boundaries := []int{0}
	position := 0
	clusters := graphemes.FromString(string(value))
	for clusters.Next() {
		position += utf8.RuneCountInString(clusters.Value())
		boundaries = append(boundaries, position)
	}
	return boundaries
}

// Grapheme properties describe editing boundaries, not terminal cells. Some
// spacing marks (notably Bengali vowel signs) have the Extend property and are
// counted as zero-width by ANSI's grapheme width implementation, but terminals
// such as xterm still advance one cell for them. Keep the existing CJK/emoji
// widths and account for these marks in every layout and clipping calculation.
func tuiDisplayWidth(value string) int {
	width := ansi.StringWidth(value)
	for _, character := range value {
		if character >= 0x0900 && unicode.Is(unicode.Mc, character) {
			return width + tuiSpacingMarkCellCorrection(value)
		}
	}
	return width
}

func tuiSpacingMarkCellCorrection(value string) int {
	cells := 0
	// Do not count text inside OSC/DCS sequences, for example hyperlink URLs.
	for _, character := range ansi.Strip(value) {
		if unicode.Is(unicode.Mc, character) && ansi.StringWidth(string(character)) == 0 {
			cells++
		}
	}
	return cells
}

func tuiRuneWidth(value rune) int {
	return tuiDisplayWidth(string(value))
}

// Width and clipping share the terminal-cell rules above. Editing/clipping
// still uses current UAX #29 boundaries, including Indic conjuncts.
func tuiTruncateGraphemes(value string, width int, tail string) string {
	if width <= 0 {
		return ""
	}
	value = strings.ToValidUTF8(value, "�")
	if tuiDisplayWidth(value) <= width {
		return value
	}
	limit := maxTUIWidth(width-tuiDisplayWidth(tail), 0)
	clusters := graphemes.FromString(value)
	clusters.AnsiEscapeSequences = true
	var result strings.Builder
	used := 0
	for clusters.Next() {
		cluster := clusters.Value()
		next := tuiDisplayWidth(cluster)
		if used+next > limit {
			break
		}
		result.WriteString(cluster)
		used += next
	}
	result.WriteString(tail)
	if strings.Contains(value, "\x1b") {
		result.WriteString(tuiReset)
	}
	return result.String()
}

func tuiPreviousGrapheme(value []rune, cursor int) int {
	previous := 0
	for _, boundary := range tuiGraphemeBoundaries(value) {
		if boundary >= cursor {
			break
		}
		previous = boundary
	}
	return previous
}

func tuiNextGrapheme(value []rune, cursor int) int {
	for _, boundary := range tuiGraphemeBoundaries(value) {
		if boundary > cursor {
			return boundary
		}
	}
	return len(value)
}

func tuiGraphemeCursor(value []rune, cursor int) int {
	if cursor <= 0 {
		return 0
	}
	if cursor >= len(value) {
		return len(value)
	}
	for _, boundary := range tuiGraphemeBoundaries(value) {
		if boundary >= cursor {
			return boundary
		}
	}
	return len(value)
}
