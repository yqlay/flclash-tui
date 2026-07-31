//go:build linux && !cgo && cli

package main

import (
	"errors"
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
		if !status.OK || status.Revision != 2 {
			t.Fatalf("deduplicated mutation response = %+v", status)
		}
	}
	if shutdownCount.Load() != 1 {
		t.Fatalf("shutdown callback ran %d times", shutdownCount.Load())
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
