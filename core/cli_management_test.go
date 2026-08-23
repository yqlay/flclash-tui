//go:build linux && !cgo && cli

package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func captureCLIOutput(t *testing.T, run func() error) string {
	t.Helper()
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	defer func() { os.Stdout = original }()
	if err := run(); err != nil {
		_ = writer.Close()
		_ = reader.Close()
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		_ = reader.Close()
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return string(output)
}

func TestCompletionCoversPrimaryAndNestedCommands(t *testing.T) {
	bash := captureCLIOutput(t, func() error {
		return completionCommand([]string{"bash"})
	})
	for _, value := range []string{
		"start stop restart reload status",
		"config geo env doctor completion check update run version",
		"COMP_WORDS[2]} == close",
		"words='all'",
		"update) words='--check --download-only --yes'",
	} {
		if !strings.Contains(bash, value) {
			t.Fatalf("Bash completion does not contain %q:\n%s", value, bash)
		}
	}

	zsh := captureCLIOutput(t, func() error {
		return completionCommand([]string{"zsh"})
	})
	if !strings.Contains(zsh, "$words[3] == close") ||
		!strings.Contains(zsh, "_values 'argument' all") {
		t.Fatalf("Zsh completion has no `connections close all`: %s", zsh)
	}

	fish := captureCLIOutput(t, func() error {
		return completionCommand([]string{"fish"})
	})
	if !strings.Contains(
		fish,
		"__fish_seen_subcommand_from connections; and __fish_seen_subcommand_from close' -a all",
	) {
		t.Fatalf("Fish completion has no `connections close all`: %s", fish)
	}
}

func TestConnectionsArgs(t *testing.T) {
	for name, args := range map[string][]string{
		"show":      {"show", "unexpected"},
		"json":      {"show", "--json", "unexpected"},
		"close_all": {"close-all", "unexpected"},
	} {
		t.Run(name, func(t *testing.T) {
			setupCLICommandTestDirectories(t)
			serveCLICommandStatus(t, tuiServiceStatus{
				OK:              true,
				Version:         cliVersion,
				ProtocolVersion: tuiServiceProtocolVersion,
			})

			err := connectionsCommand(args)
			if err == nil || !strings.Contains(err.Error(), "usage:") {
				t.Fatalf("connections %v = %v", args, err)
			}
		})
	}
}
