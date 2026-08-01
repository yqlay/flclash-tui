//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestTUIServiceRuntime(t *testing.T) *tuiServiceRuntime {
	t.Helper()
	directory := t.TempDir()
	return newTUIServiceRuntime(
		cliPaths{
			homeDir:    directory,
			configPath: filepath.Join(directory, "config.yaml"),
		},
		defaultCLITestURL,
		filepath.Join(directory, "core.sock"),
		nil,
		nil,
	)
}

func TestTUIServiceRuntimeRejectsStaleRevision(t *testing.T) {
	runtime := newTestTUIServiceRuntime(t)
	stale := uint64(0)
	status := runtime.handle(tuiServiceRequest{
		ProtocolVersion:  tuiServiceProtocolVersion,
		RequestID:        "stale-request",
		ExpectedRevision: &stale,
		Action:           "reload",
	})
	if status.OK || status.ErrorCode != tuiServiceErrorConflict || status.Revision != 1 {
		t.Fatalf("stale mutation response = %+v", status)
	}
	var serviceErr *tuiServiceError
	err := &tuiServiceError{
		Code:     status.ErrorCode,
		Revision: status.Revision,
		Message:  status.Error,
	}
	if !errors.As(err, &serviceErr) || serviceErr.Code != tuiServiceErrorConflict {
		t.Fatalf("structured conflict error = %#v", err)
	}
}

func TestTUIServiceRuntimeDeduplicatesRequestID(t *testing.T) {
	runtime := newTestTUIServiceRuntime(t)
	request := tuiServiceRequest{
		ProtocolVersion: tuiServiceProtocolVersion,
		RequestID:       "retry-id",
		Action:          "status",
	}
	first := runtime.handle(request)
	runtime.bumpRevision()
	second := runtime.handle(request)
	if first.Revision != 1 || second.Revision != first.Revision {
		t.Fatalf("deduplicated revisions = %d then %d", first.Revision, second.Revision)
	}
	fresh := runtime.handle(tuiServiceRequest{
		ProtocolVersion: tuiServiceProtocolVersion,
		RequestID:       "fresh-id",
		Action:          "status",
	})
	if fresh.Revision != 2 {
		t.Fatalf("fresh status revision = %d, want 2", fresh.Revision)
	}
}

func TestTUIServiceRuntimeDeduplicatesConcurrentMutation(t *testing.T) {
	directory := t.TempDir()
	var shutdownCount atomic.Int32
	runtime := newTUIServiceRuntime(
		cliPaths{
			homeDir:    directory,
			configPath: filepath.Join(directory, "config.yaml"),
		},
		defaultCLITestURL,
		filepath.Join(directory, "core.sock"),
		nil,
		func() { shutdownCount.Add(1) },
	)
	revision := uint64(1)
	request := tuiServiceRequest{
		ProtocolVersion:  tuiServiceProtocolVersion,
		RequestID:        "same-shutdown",
		ExpectedRevision: &revision,
		Action:           "shutdown",
	}
	const callers = 32
	var wait sync.WaitGroup
	wait.Add(callers)
	results := make(chan tuiServiceStatus, callers)
	for remaining := callers; remaining > 0; remaining-- {
		go func() {
			defer wait.Done()
			results <- runtime.handle(request)
		}()
	}
	wait.Wait()
	close(results)
	for status := range results {
		if !status.OK || status.Revision != 2 || !status.ShuttingDown {
			t.Fatalf("deduplicated mutation response = %+v", status)
		}
	}
	if shutdownCount.Load() != 0 {
		t.Fatalf("shutdown callback ran before ACK: %d", shutdownCount.Load())
	}
	runtime.signalShutdown()
	if shutdownCount.Load() != 1 {
		t.Fatalf("shutdown callback count = %d", shutdownCount.Load())
	}
}

func TestTUIServiceRuntimeWatchReportsMonotonicRevision(t *testing.T) {
	runtime := newTestTUIServiceRuntime(t)
	result := make(chan tuiServiceStatus, 1)
	go func() {
		result <- runtime.handle(tuiServiceRequest{
			ProtocolVersion: tuiServiceProtocolVersion,
			RequestID:       "watch-id",
			Action:          "watch",
			AfterRevision:   1,
			WatchTimeoutMS:  1000,
		})
	}()
	time.Sleep(20 * time.Millisecond)
	runtime.bumpRevision()
	select {
	case status := <-result:
		if !status.OK || status.Revision != 2 {
			t.Fatalf("watch response = %+v", status)
		}
	case <-time.After(time.Second):
		t.Fatal("watch did not wake after revision changed")
	}
}

func TestTUIServiceRuntimeStatusDoesNotWaitForMutationQueue(t *testing.T) {
	runtime := newTestTUIServiceRuntime(t)
	runtime.mutationMu.Lock()
	defer runtime.mutationMu.Unlock()

	result := make(chan tuiServiceStatus, 1)
	go func() {
		result <- runtime.handle(tuiServiceRequest{
			ProtocolVersion: tuiServiceProtocolVersion,
			RequestID:       "status-during-mutation",
			Action:          "status",
		})
	}()
	select {
	case status := <-result:
		if !status.OK || status.Revision != 1 {
			t.Fatalf("status response = %+v", status)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("status waited for the serialized mutation queue")
	}
}

func TestTUIServiceRuntimeRejectsUnsupportedProtocol(t *testing.T) {
	runtime := newTestTUIServiceRuntime(t)
	status := runtime.handle(tuiServiceRequest{
		ProtocolVersion: tuiServiceProtocolVersion + 1,
		RequestID:       "future-client",
		Action:          "status",
	})
	if status.OK || status.ErrorCode != tuiServiceErrorUnsupported {
		t.Fatalf("unsupported protocol response = %+v", status)
	}
}

func TestTUIServiceRuntimeSelectProxyValidatesAndRollsBack(t *testing.T) {
	directory := t.TempDir()
	socketPath := filepath.Join(directory, "core.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	var selectionMu sync.Mutex
	selection := "DIRECT"
	selections := make([]string, 0, 3)
	server := &http.Server{Handler: http.HandlerFunc(func(
		w http.ResponseWriter,
		request *http.Request,
	) {
		selectionMu.Lock()
		defer selectionMu.Unlock()
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/proxies":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"proxies": map[string]any{
					"PROXY": map[string]any{
						"type": "Selector",
						"now":  selection,
						"all":  []string{"DIRECT", "Node A"},
					},
				},
			})
		case request.Method == http.MethodPut && request.URL.Path == "/proxies/PROXY":
			var body struct {
				Name string `json:"name"`
			}
			if decodeErr := json.NewDecoder(request.Body).Decode(&body); decodeErr != nil {
				http.Error(w, decodeErr.Error(), http.StatusBadRequest)
				return
			}
			selection = body.Name
			selections = append(selections, body.Name)
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, request)
		}
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })

	runtime := newTUIServiceRuntime(
		cliPaths{
			homeDir:    directory,
			configPath: filepath.Join(directory, "config.yaml"),
		},
		defaultCLITestURL,
		socketPath,
		nil,
		nil,
	)
	if _, err := runtime.selectProxy("PROXY", "missing"); err == nil {
		t.Fatal("proxy outside the selected group was accepted")
	}
	selectionMu.Lock()
	if len(selections) != 0 {
		selectionMu.Unlock()
		t.Fatalf("invalid selection changed Core: %v", selections)
	}
	selectionMu.Unlock()
	changed, err := runtime.selectProxy("PROXY", "Node A")
	if err != nil || !changed {
		t.Fatalf("valid selection = %v, %v", changed, err)
	}
	if saved := loadTUISelectedProxies(directory)["PROXY"]; saved != "Node A" {
		t.Fatalf("saved selection = %q, want Node A", saved)
	}

	statePath := filepath.Join(directory, tuiStateFilename)
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(statePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.selectProxy("PROXY", "DIRECT"); err == nil {
		t.Fatal("selection succeeded when its state could not be saved")
	}
	selectionMu.Lock()
	defer selectionMu.Unlock()
	if selection != "Node A" {
		t.Fatalf("Core rollback selection = %q, want Node A", selection)
	}
	if len(selections) != 3 || selections[0] != "Node A" ||
		selections[1] != "DIRECT" || selections[2] != "Node A" {
		t.Fatalf("Core selection transaction = %v", selections)
	}
}

func TestTUIServiceRuntimeBacksUpNestedProfileThroughBackend(t *testing.T) {
	runtime := newTestTUIServiceRuntime(t)
	nested := filepath.Join(runtime.paths.homeDir, "profiles")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(nested, "work.yaml")
	content := []byte("mixed-port: 7891\nmode: rule\n")
	if err := os.WriteFile(profilePath, content, 0o640); err != nil {
		t.Fatal(err)
	}
	changed, backupPath, err := runtime.backupProfile(profilePath)
	if err != nil || !changed {
		t.Fatalf("backup result = %t, %q, %v", changed, backupPath, err)
	}
	backup, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(backup) != string(content) {
		t.Fatalf("backup content = %q, want %q", backup, content)
	}
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %o, want 600", info.Mode().Perm())
	}

	symlinkPath := filepath.Join(nested, "linked.yaml")
	if err := os.Symlink(profilePath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runtime.backupProfile(symlinkPath); err == nil {
		t.Fatal("Backend accepted a symlink profile backup target")
	}
}
