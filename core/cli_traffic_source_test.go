//go:build linux && !cgo && cli

package main

import (
	"strings"
	"testing"
	"time"
)

func TestTrafficSourceFilterAndMerge(t *testing.T) {
	proxy := []tuiConnection{{
		ID:     "p1",
		Host:   "api.example",
		Source: tuiTrafficSourceProxy,
		Chain:  "PROXY",
	}}
	ssh := []tuiConnection{{
		ID:     "ssh:1-1",
		Host:   "example.test:443",
		Source: tuiTrafficSourceSSH,
		Chain:  "SSH · home",
	}}
	merged := mergeTUITrafficConnections(proxy, ssh)
	if len(merged) != 2 {
		t.Fatalf("merged = %+v", merged)
	}
	if got := filterConnectionsBySource(merged, tuiTrafficSourceSSH); len(got) != 1 || got[0].ID != "ssh:1-1" {
		t.Fatalf("ssh filter = %+v", got)
	}
	if got := filterConnectionsBySource(merged, tuiTrafficSourceProxy); len(got) != 1 || got[0].ID != "p1" {
		t.Fatalf("proxy filter = %+v", got)
	}
	if got := filterConnectionsBySource(merged, tuiTrafficSourceMixed); len(got) != 2 {
		t.Fatalf("mixed filter = %+v", got)
	}
}

func TestLegacyHistoryEntriesDefaultToProxy(t *testing.T) {
	request := tuiRequest{TuiConnection: tuiConnection{ID: "old", Host: "a.example"}}
	if tuiConnectionSource(request.TuiConnection) != tuiTrafficSourceProxy {
		t.Fatalf("legacy source = %q", tuiConnectionSource(request.TuiConnection))
	}
	if !trafficSourceMatches(tuiTrafficSourceProxy, tuiConnectionSource(request.TuiConnection)) {
		t.Fatal("legacy entry should match proxy filter")
	}
	if trafficSourceMatches(tuiTrafficSourceSSH, tuiConnectionSource(request.TuiConnection)) {
		t.Fatal("legacy entry should not match ssh filter")
	}
}

func TestRememberClosedSSHHistoryInsertsUnseenFlows(t *testing.T) {
	now := time.Now()
	history := []tuiRequest{{
		TuiConnection: tuiConnection{ID: "ssh:keep", Source: tuiTrafficSourceSSH},
		FirstSeen:     now,
		LastSeen:      now,
		Active:        false,
	}}
	updated := rememberClosedSSHHistory(history, []tuiConnection{
		{ID: "ssh:keep", Source: tuiTrafficSourceSSH},
		{ID: "ssh:new", Host: "closed.example:443", Source: tuiTrafficSourceSSH},
	}, now)
	if len(updated) != 2 {
		t.Fatalf("history = %+v", updated)
	}
	if updated[1].ID != "ssh:new" || updated[1].Active {
		t.Fatalf("closed ssh entry = %+v", updated[1])
	}
}

func TestTUITrafficSourceKeyCyclesOnConnectionsAndHistory(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{}, nil, true)
	model.snapshot.Page = tuiPageConnections
	model.snapshot.Connections = []tuiConnection{
		{ID: "p1", Host: "proxy.example", Source: tuiTrafficSourceProxy},
		{ID: "ssh:1", Host: "ssh.example", Source: tuiTrafficSourceSSH},
	}
	model.handleKey(tuiKeyFilter)
	if normalizeTrafficSource(model.snapshot.TrafficSource) != tuiTrafficSourceProxy {
		t.Fatalf("connections f = %q", model.snapshot.TrafficSource)
	}
	if model.snapshot.SelectedConnection < 0 ||
		tuiConnectionSource(model.snapshot.Connections[model.snapshot.SelectedConnection]) != tuiTrafficSourceProxy {
		t.Fatalf("proxy filter selected %+v", model.snapshot.Connections)
	}
	model.snapshot.Page = tuiPageRequests
	model.snapshot.Requests = []tuiRequest{
		{TuiConnection: tuiConnection{ID: "p1", Source: tuiTrafficSourceProxy}, Active: true},
		{TuiConnection: tuiConnection{ID: "ssh:1", Source: tuiTrafficSourceSSH}, Active: true},
	}
	model.handleKey(tuiKeySourceFilter)
	if normalizeTrafficSource(model.snapshot.TrafficSource) != tuiTrafficSourceSSH {
		t.Fatalf("history o = %q", model.snapshot.TrafficSource)
	}
	if !strings.Contains(model.snapshot.Status, "ssh") {
		t.Fatalf("status = %q", model.snapshot.Status)
	}
}
