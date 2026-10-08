//go:build linux && !cgo && cli

package main

import (
	"reflect"
	"testing"
)

func TestCLIFlagsInterspersedAndTerminator(t *testing.T) {
	for _, args := range [][]string{
		{"first", "--source=ssh", "second", "--json", "--", "--node"},
		{"--json", "--source", "ssh", "first", "second", "--", "--node"},
	} {
		fs := newCLIFlagSet("audit")
		source := fs.String("source", "", "")
		json := fs.Bool("json", false, "")
		if err := parseCLIFlags(fs, args); err != nil {
			t.Fatal(err)
		}
		if *source != "ssh" || !*json || !reflect.DeepEqual(fs.Args(), []string{"first", "second", "--node"}) {
			t.Fatalf("options/positionals lost: source=%q json=%v args=%q", *source, *json, fs.Args())
		}
	}
	for _, args := range [][]string{{"--source"}, {"--unknown"}, {"node", "--source", "--json"}, {"--source", "-json"}, {"--source", "-help"}} {
		fs := newCLIFlagSet("audit")
		fs.String("source", "", "")
		fs.Bool("json", false, "")
		if err := parseCLIFlags(fs, args); err == nil {
			t.Fatalf("accepted malformed arguments %q", args)
		}
	}
}
