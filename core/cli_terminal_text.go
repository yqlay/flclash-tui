//go:build linux && !cgo && cli

package main

import (
	"strings"
	"unicode"
)

// Only use these helpers at display boundaries. Profile names and API payloads
// must retain their original bytes; terminal control sequences must not.
func safeCLITerminalText(raw string) string {
	var out strings.Builder
	state := byte(0) // normal, escape, CSI, string, string escape, intermediate
	osc := false
	for _, r := range raw {
		switch state {
		case 1:
			switch r {
			case '[':
				state = 2
			case ']', 'P', 'X', '^', '_':
				state, osc = 3, r == ']'
			default:
				if r >= 0x20 && r <= 0x2f {
					state = 5
				} else {
					state = 0
				}
			}
			continue
		case 2:
			if r >= 0x40 && r <= 0x7e {
				state = 0
			}
			continue
		case 3:
			if r == 0x9c || osc && r == '\a' {
				state = 0
			} else if r == 0x1b {
				state = 4
			}
			continue
		case 4:
			if r == '\\' || r == 0x9c || osc && r == '\a' {
				state = 0
			} else if r != 0x1b {
				state = 3
			}
			continue
		case 5:
			if r < 0x20 || r > 0x2f {
				state = 0
			}
			continue
		}
		switch r {
		case 0x1b:
			state = 1
		case 0x9b:
			state = 2
		case 0x90, 0x98, 0x9d, 0x9e, 0x9f:
			state, osc = 3, r == 0x9d
		case '\n':
			out.WriteByte('\n')
		case '\t':
			out.WriteString("    ")
		default:
			if !unicode.IsControl(r) {
				out.WriteRune(r)
			}
		}
	}
	return out.String()
}

func safeCLITerminalLine(raw string) string {
	return strings.ReplaceAll(safeCLITerminalText(raw), "\n", " ")
}
