//go:build linux && !cgo && cli

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

type notifyingCLIWriter struct {
	mu    sync.Mutex
	data  bytes.Buffer
	once  sync.Once
	wrote chan struct{}
}

func (w *notifyingCLIWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.once.Do(func() { close(w.wrote) })
	return w.data.Write(data)
}

func (w *notifyingCLIWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.data.String()
}

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
		"backend shutdown exit profile",
		"flc) words='status select test env ssh'",
		"complete -F _flc flc",
		"ssh) words='add edit delete list show connect disconnect status test --port --local-port --identity --passphrase --clear-passphrase --password --clear-password --option'",
		"words='-u --use'",
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

func TestReadManagedLogFollowSurvivesRotation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.log")
	if err := os.WriteFile(path, []byte("before rotation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	output := &notifyingCLIWriter{wrote: make(chan struct{})}
	interrupt := make(chan os.Signal, 1)
	done := make(chan error, 1)
	go func() {
		done <- readManagedLogTo(path, 100, true, output, interrupt)
	}()
	select {
	case <-output.wrote:
	case <-time.After(time.Second):
		t.Fatal("log follower did not print the existing log")
	}
	if err := os.Rename(path, path+".1"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("after rotation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(output.String(), "after rotation") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	interrupt <- syscall.SIGTERM
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("log follower did not stop")
	}
	text := output.String()
	if !strings.Contains(text, "before rotation") ||
		!strings.Contains(text, "after rotation") {
		t.Fatalf("rotated followed log = %q", text)
	}
}
