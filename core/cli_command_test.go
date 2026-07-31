//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestCLIProxyEnvironmentReplacesProxyVariables(t *testing.T) {
	environment := cliProxyEnvironment(
		[]string{
			"PATH=/usr/bin",
			"HTTP_PROXY=http://old.example:1",
			"https_proxy=http://old.example:2",
			"NO_PROXY=localhost",
		},
		"http://127.0.0.1:17890",
	)
	values := cliCommandEnvironmentMap(environment)
	for _, key := range []string{
		"HTTP_PROXY",
		"HTTPS_PROXY",
		"ALL_PROXY",
		"http_proxy",
		"https_proxy",
		"all_proxy",
	} {
		if values[key] != "http://127.0.0.1:17890" {
			t.Fatalf("%s = %q", key, values[key])
		}
	}
	if values["PATH"] != "/usr/bin" || values["NO_PROXY"] != "localhost" {
		t.Fatalf("unrelated environment was changed: %#v", values)
	}
}

func TestCLIWrappedCommandDisablesWgetConfig(t *testing.T) {
	tests := []struct {
		name       string
		executable string
		arguments  []string
		want       []string
	}{
		{
			name:       "GNU Wget ignores stale user proxy config",
			executable: "/usr/bin/wget",
			arguments:  []string{"wget", "-O", "package.deb", "https://example.com/package.deb"},
			want:       []string{"wget", "--no-config", "-O", "package.deb", "https://example.com/package.deb"},
		},
		{
			name:       "existing no-config is not duplicated",
			executable: "/usr/bin/wget",
			arguments:  []string{"wget", "--no-config", "https://example.com"},
			want:       []string{"wget", "--no-config", "https://example.com"},
		},
		{
			name:       "other commands are unchanged",
			executable: "/usr/bin/curl",
			arguments:  []string{"curl", "https://example.com"},
			want:       []string{"curl", "https://example.com"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := cliWrappedCommandArguments(test.executable, test.arguments)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("arguments = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestWrappedCommandRequiresCommand(t *testing.T) {
	err := wrappedCommand(nil)
	if err == nil ||
		!strings.Contains(err.Error(), "no command specified") ||
		!strings.Contains(err.Error(), "未指定要执行的命令") {
		t.Fatalf("error = %v", err)
	}
}

func TestWrappedCommandReportsStoppedServiceBilingually(t *testing.T) {
	setupCLICommandTestDirectories(t)
	serveCLICommandStatus(t, tuiServiceStatus{
		OK:      true,
		Running: false,
	})

	err := wrappedCommand([]string{"curl", "https://example.com"})
	if err == nil ||
		!strings.Contains(err.Error(), "Service/Core is stopped") ||
		!strings.Contains(err.Error(), "Service/Core 已停止") {
		t.Fatalf("error = %v", err)
	}
}

func TestWrappedCommandReportsMissingServiceBilingually(t *testing.T) {
	setupCLICommandTestDirectories(t)

	err := wrappedCommand([]string{"curl", "https://example.com"})
	if err == nil ||
		!strings.Contains(err.Error(), "FlClash is not running") ||
		!strings.Contains(err.Error(), "FlClash 未运行") {
		t.Fatalf("error = %v", err)
	}
}

func TestWrappedCommandUsesActiveMixedPort(t *testing.T) {
	setupCLICommandTestDirectories(t)
	mixedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer mixedListener.Close()
	_, portValue, err := net.SplitHostPort(mixedListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	mixedPort, err := strconv.Atoi(portValue)
	if err != nil {
		t.Fatal(err)
	}
	coreSocket := serveCLICommandCoreConfig(t, mixedPort)
	serveCLICommandStatus(t, tuiServiceStatus{
		OK:         true,
		Running:    true,
		CoreSocket: coreSocket,
	})

	previousLookPath := cliCommandLookPath
	previousExec := cliCommandExec
	t.Cleanup(func() {
		cliCommandLookPath = previousLookPath
		cliCommandExec = previousExec
	})
	cliCommandLookPath = func(file string) (string, error) {
		if file != "probe-command" {
			return "", errors.New("unexpected command")
		}
		return "/usr/bin/probe-command", nil
	}
	var executable string
	var arguments []string
	var environment []string
	cliCommandExec = func(
		path string,
		args []string,
		env []string,
	) error {
		executable = path
		arguments = append([]string(nil), args...)
		environment = append([]string(nil), env...)
		return nil
	}

	commandArgs := []string{"probe-command", "--value", "with spaces"}
	if err := wrappedCommand(commandArgs); err != nil {
		t.Fatal(err)
	}
	if executable != "/usr/bin/probe-command" ||
		!reflect.DeepEqual(arguments, commandArgs) {
		t.Fatalf("exec = %q %#v", executable, arguments)
	}
	wantProxy := "http://127.0.0.1:" + strconv.Itoa(mixedPort)
	values := cliCommandEnvironmentMap(environment)
	for _, key := range []string{
		"HTTP_PROXY",
		"HTTPS_PROXY",
		"ALL_PROXY",
		"http_proxy",
		"https_proxy",
		"all_proxy",
	} {
		if values[key] != wantProxy {
			t.Fatalf("%s = %q, want %q", key, values[key], wantProxy)
		}
	}
}

func TestWrappedCommandReportsClosedMixedPortBilingually(t *testing.T) {
	setupCLICommandTestDirectories(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portValue, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	mixedPort, err := strconv.Atoi(portValue)
	if err != nil {
		t.Fatal(err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	coreSocket := serveCLICommandCoreConfig(t, mixedPort)
	serveCLICommandStatus(t, tuiServiceStatus{
		OK:         true,
		Running:    true,
		CoreSocket: coreSocket,
	})

	err = wrappedCommand([]string{"curl", "https://example.com"})
	if err == nil ||
		!strings.Contains(err.Error(), "is not accepting connections") ||
		!strings.Contains(err.Error(), "无法连接") {
		t.Fatalf("error = %v", err)
	}
}

func TestWrappedCommandReportsMissingCommandBilingually(t *testing.T) {
	setupCLICommandTestDirectories(t)
	mixedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer mixedListener.Close()
	_, portValue, err := net.SplitHostPort(mixedListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	mixedPort, err := strconv.Atoi(portValue)
	if err != nil {
		t.Fatal(err)
	}
	coreSocket := serveCLICommandCoreConfig(t, mixedPort)
	serveCLICommandStatus(t, tuiServiceStatus{
		OK:         true,
		Running:    true,
		CoreSocket: coreSocket,
	})

	previousLookPath := cliCommandLookPath
	t.Cleanup(func() {
		cliCommandLookPath = previousLookPath
	})
	cliCommandLookPath = func(string) (string, error) {
		return "", errors.New("not found")
	}
	err = wrappedCommand([]string{"missing-command"})
	if err == nil ||
		!strings.Contains(err.Error(), "command not found") ||
		!strings.Contains(err.Error(), "找不到命令") {
		t.Fatalf("error = %v", err)
	}
}

func setupCLICommandTestDirectories(t *testing.T) {
	t.Helper()
	runtimeDirectory := t.TempDir()
	previousRuntimeDirectory := cliRuntimeDirectoryOverride
	cliRuntimeDirectoryOverride = runtimeDirectory
	t.Cleanup(func() {
		cliRuntimeDirectoryOverride = previousRuntimeDirectory
	})
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

func serveCLICommandCoreConfig(t *testing.T, mixedPort int) string {
	t.Helper()
	coreSocket := filepath.Join(t.TempDir(), "core.sock")
	coreListener, err := net.Listen("unix", coreSocket)
	if err != nil {
		t.Fatal(err)
	}
	coreServer := &http.Server{Handler: http.HandlerFunc(
		func(w http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/configs" {
				http.NotFound(w, request)
				return
			}
			_, _ = w.Write([]byte(
				`{"mode":"rule","mixed-port":` +
					strconv.Itoa(mixedPort) +
					`}`,
			))
		},
	)}
	go func() {
		_ = coreServer.Serve(coreListener)
	}()
	t.Cleanup(func() {
		_ = coreServer.Close()
	})
	return coreSocket
}

func serveCLICommandStatus(t *testing.T, status tuiServiceStatus) {
	t.Helper()
	runtimeDirectory, err := cliRuntimeDirectory()
	if err != nil {
		t.Fatal(err)
	}
	socketPath := filepath.Join(
		runtimeDirectory,
		tuiServiceSocketFilename,
	)
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		var request tuiServiceRequest
		if json.NewDecoder(bufio.NewReader(connection)).Decode(&request) != nil {
			return
		}
		if request.Action != "status" {
			status.OK = false
			status.Error = "unexpected action " + request.Action
		}
		_ = json.NewEncoder(connection).Encode(status)
	}()
}

func cliCommandEnvironmentMap(environment []string) map[string]string {
	result := make(map[string]string, len(environment))
	for _, item := range environment {
		key, value, found := strings.Cut(item, "=")
		if found {
			result[key] = value
		}
	}
	return result
}
