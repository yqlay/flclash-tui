//go:build linux && !cgo && cli

package main

import "testing"

func TestTUITunHelperLeaseRules(t *testing.T) {
	state := &tuiTunHelperState{owners: map[uint32]tuiTunHelperLeaseOwner{}}
	userA := tuiTunHelperLeaseOwner{uid: 1000, pid: 10, scope: tuiTunScopeUser}
	userB := tuiTunHelperLeaseOwner{uid: 1001, pid: 11, scope: tuiTunScopeUser}
	system := tuiTunHelperLeaseOwner{uid: 1000, pid: 10, scope: tuiTunScopeSystem}
	if response, ok := state.acquire(userA); !ok || !response.OK {
		t.Fatalf("acquire user A = %+v, %v", response, ok)
	}
	if response, ok := state.acquire(userB); !ok || !response.OK {
		t.Fatalf("acquire user B = %+v, %v", response, ok)
	}
	if response, ok := state.acquire(system); ok || response.OwnerUID != userB.uid {
		t.Fatalf("system lease with another user active = %+v, %v", response, ok)
	}
	state.release(userB)
	if response, ok := state.acquire(system); !ok || !response.OK {
		t.Fatalf("upgrade own user lease to system = %+v, %v", response, ok)
	}
	if response, ok := state.acquire(userB); ok || response.OwnerUID != system.uid {
		t.Fatalf("user lease while system active = %+v, %v", response, ok)
	}
	if response, ok := state.acquire(userA); !ok || !response.OK {
		t.Fatalf("downgrade own system lease to user = %+v, %v", response, ok)
	}
}

func TestNormalizeTUITunScopeDefaultsToUser(t *testing.T) {
	if scope, err := normalizeTUITunScope(""); err != nil || scope != tuiTunScopeUser {
		t.Fatalf("normalize empty scope = %q, %v", scope, err)
	}
	if _, err := normalizeTUITunScope("machine"); err == nil {
		t.Fatal("invalid TUN scope was accepted")
	}
}

func TestFilterTUIConnectionsHonorsUserAndSystemScope(t *testing.T) {
	connections := []tuiConnection{{ID: "mine", UID: 1000}, {ID: "other", UID: 1001}, {ID: "unknown"}}
	filtered := filterTUIConnections(append([]tuiConnection(nil), connections...), 1000, false)
	if len(filtered) != 1 || filtered[0].ID != "mine" {
		t.Fatalf("user connections = %+v", filtered)
	}
	all := filterTUIConnections(append([]tuiConnection(nil), connections...), 1000, true)
	if len(all) != len(connections) {
		t.Fatalf("system connections = %+v", all)
	}
}
