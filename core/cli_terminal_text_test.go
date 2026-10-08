//go:build linux && !cgo && cli

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestCLITerminalTextRemovesUntrustedControls(t *testing.T) {
	for _, test := range []struct{ raw, want string }{
		{"safe\x1b[2Jname\x1b[31m!\x1b[0m", "safename!"},
		{"a\x1b]52;c;c2VjcmV0\ab", "ab"},
		{"a\x1b]0;title\x1b\\b", "ab"},
		{"a\x1bPpayload\x1b\\b", "ab"},
		{"a\u009b2Jb\u009d52;c;x\u009cc", "abc"},
		{"a\x00\x7f\r\bb", "ab"},
		{"a\x1b]unfinished", "a"},
		{"节点 / عربى / हिन्दी / 👩‍💻 / é", "节点 / عربى / हिन्दी / 👩‍💻 / é"},
		{"one\ntwo\tthree", "one\ntwo    three"},
	} {
		if got := safeCLITerminalText(test.raw); got != test.want {
			t.Errorf("safeCLITerminalText(%q) = %q, want %q", test.raw, got, test.want)
		}
	}
	if got := safeCLITerminalLine("node\nnext"); got != "node next" {
		t.Fatalf("single-line output: %q", got)
	}
}

func TestCLILogDisplaySanitizesWithoutChangingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "untrusted.log")
	raw := "INFO node=\x1b]52;c;c2VjcmV0\a节点\x1b[2J\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := readManagedLogTo(path, 100, false, &out, nil); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "INFO node=节点\n" {
		t.Fatalf("unsafe terminal log output: %q", got)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != raw {
		t.Fatalf("display sanitizer modified the original log: %q, %v", data, err)
	}
}
