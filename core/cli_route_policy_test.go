//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"gopkg.in/yaml.v3"
)

func TestParseRouteEntryForms(t *testing.T) {
	suffix, err := parseRouteEntry("*.Example.COM")
	if err != nil || suffix.Display != "example.com" || suffix.clash("PROXY") != "DOMAIN-SUFFIX,example.com,PROXY" {
		t.Fatalf("suffix %#v %v", suffix, err)
	}
	plain, err := parseRouteEntry("example.com")
	if err != nil || plain.Key != suffix.Key {
		t.Fatalf("plain %#v %v", plain, err)
	}
	exact, err := parseRouteEntry("=API.Example.COM")
	if err != nil || exact.clash("PROXY") != "DOMAIN,api.example.com,PROXY" {
		t.Fatalf("exact %#v %v", exact, err)
	}
	host, err := parseRouteEntry("1.2.3.4")
	if err != nil || host.Display != "1.2.3.4/32" || host.clash("DIRECT") != "IP-CIDR,1.2.3.4/32,DIRECT,no-resolve" {
		t.Fatalf("host %#v %v", host, err)
	}
	prefix, err := parseRouteEntry("1.2.3.4/24")
	network, networkErr := parseRouteEntry("1.2.3.0/24")
	if err != nil || networkErr != nil || prefix.Key != network.Key || prefix.Display != "1.2.3.0/24" {
		t.Fatalf("prefix %#v %#v %v %v", prefix, network, err, networkErr)
	}
	v6, err := parseRouteEntry("2001:db8::1")
	if err != nil || v6.clash("DIRECT") != "IP-CIDR,2001:db8::1/128,DIRECT,no-resolve" {
		t.Fatalf("ipv6 %#v %v", v6, err)
	}
	for _, raw := range []string{"", "http://example.com", "example.com/a", "example.com:443", "bad host", "DOMAIN-KEYWORD,example.com", "=1.2.3.4"} {
		if _, err := parseRouteEntry(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestRouteListsRejectOverlapDuplicatesAndLimit(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, proxyRoutePolicyFilename)
	if err := os.WriteFile(path, []byte("{\n  \"note\": \"keep\",\n  \"whitelist\": [],\n  \"blacklist\": []\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	added, err := addRouteEntry(home, proxyRoutePolicyFilename, routeListWhitelist, "*.Example.COM")
	if err != nil || added != "example.com" {
		t.Fatalf("add %q %v", added, err)
	}
	if _, err := addRouteEntry(home, proxyRoutePolicyFilename, routeListWhitelist, "example.com"); !errors.Is(err, errRouteDuplicate) {
		t.Fatalf("duplicate %v", err)
	}
	if _, err := addRouteEntry(home, proxyRoutePolicyFilename, routeListBlacklist, "example.com"); !errors.Is(err, errRouteOverlap) {
		t.Fatalf("overlap %v", err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v %v", info, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"note": "keep"`) || !strings.Contains(string(data), "example.com") {
		t.Fatalf("stored %s %v", data, err)
	}
	for index := 0; index < routeListLimit-1; index++ {
		if _, err := addRouteEntry(home, proxyRoutePolicyFilename, routeListWhitelist, fmt.Sprintf("h%d.example", index)); err != nil {
			t.Fatalf("entry %d: %v", index, err)
		}
	}
	if _, err := addRouteEntry(home, proxyRoutePolicyFilename, routeListWhitelist, "overflow.example"); !errors.Is(err, errRouteFull) {
		t.Fatalf("limit %v", err)
	}
	if _, err := deleteRouteEntry(home, proxyRoutePolicyFilename, routeListWhitelist, "*.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := deleteRouteEntry(home, proxyRoutePolicyFilename, routeListWhitelist, "example.com"); !errors.Is(err, errRouteMissing) {
		t.Fatalf("second delete %v", err)
	}
}

func TestEmptyRouteHomeDoesNotWrite(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(cwd, proxyRoutePolicyFilename)
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cwd route file: %v", err)
	}
	if _, err := addRouteEntry("", proxyRoutePolicyFilename, routeListWhitelist, "example.com"); !errors.Is(err, errRouteEmptyHome) {
		t.Fatalf("empty home %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("empty home wrote %v", err)
	}
	rules, err := compileProxyRouteRules("", "rule", "")
	if err != nil || len(rules) != 0 {
		t.Fatalf("empty compile %#v %v", rules, err)
	}
}

func TestProxyRouteRulesFollowMode(t *testing.T) {
	home := t.TempDir()
	source := writeRouteTestProfile(t, home)
	if _, err := addRouteEntry(home, proxyRoutePolicyFilename, routeListWhitelist, "=api.example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := addRouteEntry(home, proxyRoutePolicyFilename, routeListWhitelist, "example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := addRouteEntry(home, proxyRoutePolicyFilename, routeListBlacklist, "203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	if _, err := addRouteEntry(home, sshRoutePolicyFilename, routeListBlacklist, "ssh-only.example"); err != nil {
		t.Fatal(err)
	}
	if err := rememberTUIFLCOutbound(home, "PROXY"); err != nil {
		t.Fatal(err)
	}
	paths := cliPaths{HomeDir: home, ConfigPath: filepath.Join(home, "config.yaml")}
	rulePath, err := writeTUIManagedRuntimeConfig(paths, "rule", 17891, false, tuiTunScopeUser, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupTUISilentRuntimeConfigs(home, "") })
	rules := routeTestRules(t, rulePath)
	uid := fmt.Sprintf("AND,((IN-NAME,DEFAULT-MIXED),(NOT,((UID,%d)))),REJECT", os.Getuid())
	want := []string{
		uid,
		"IP-CIDR,203.0.113.10/32,DIRECT,no-resolve",
		"DOMAIN,api.example.com,PROXY",
		"DOMAIN-SUFFIX,example.com,PROXY",
		"MATCH,PROXY",
	}
	if strings.Join(rules, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rule mode\n%s", strings.Join(rules, "\n"))
	}
	current, err := os.ReadFile(paths.ConfigPath)
	if err != nil || string(current) != string(source) {
		t.Fatal("profile bytes changed")
	}
	globalPath, err := writeTUIManagedRuntimeConfig(paths, "global", 17892, false, tuiTunScopeUser, 0)
	if err != nil {
		t.Fatal(err)
	}
	globalRules := routeTestRules(t, globalPath)
	if strings.Join(globalRules, "\n") != strings.Join([]string{uid, "IP-CIDR,203.0.113.10/32,DIRECT,no-resolve", "MATCH,GLOBAL"}, "\n") {
		t.Fatalf("global\n%s", strings.Join(globalRules, "\n"))
	}
	directPath, err := writeTUIManagedRuntimeConfig(paths, "direct", 17893, false, tuiTunScopeUser, 0)
	if err != nil {
		t.Fatal(err)
	}
	directRules := routeTestRules(t, directPath)
	if strings.Join(directRules, "\n") != strings.Join([]string{
		uid,
		"DOMAIN,api.example.com,PROXY",
		"DOMAIN-SUFFIX,example.com,PROXY",
		"MATCH,DIRECT",
	}, "\n") {
		t.Fatalf("direct\n%s", strings.Join(directRules, "\n"))
	}
	state := tuiFLCListenerState{Outbound: "PROXY", Port: 17894, Username: "flc", Password: "0123456789abcdef"}
	silentPath, err := writeTUISilentRuntimeConfig(paths, state)
	if err != nil {
		t.Fatal(err)
	}
	silent := routeTestDocument(t, silentPath)
	listeners, _ := silent["listeners"].([]any)
	listener, _ := listeners[0].(map[string]any)
	if _, ok := listener["proxy"]; ok {
		t.Fatalf("silent listener still forces proxy: %#v", listener)
	}
	silentRules := routeTestRuleList(t, silent)
	if strings.Join(silentRules[:2], "\n") != "IP-CIDR,203.0.113.10/32,DIRECT,no-resolve\nMATCH,PROXY" || strings.Contains(strings.Join(silentRules, "\n"), "DOMAIN") {
		t.Fatalf("silent\n%s", strings.Join(silentRules, "\n"))
	}
	if _, err := deleteRouteEntry(home, proxyRoutePolicyFilename, routeListBlacklist, "203.0.113.10"); err != nil {
		t.Fatal(err)
	}
	emptySilent, err := writeTUISilentRuntimeConfig(paths, state)
	if err != nil {
		t.Fatal(err)
	}
	emptyDoc := routeTestDocument(t, emptySilent)
	emptyListeners, _ := emptyDoc["listeners"].([]any)
	emptyListener, _ := emptyListeners[0].(map[string]any)
	if emptyListener["proxy"] != "PROXY" {
		t.Fatalf("empty blacklist listener %#v", emptyListener)
	}
}

func TestRouteListDoesNotReloadBackend(t *testing.T) {
	if routeCommandShouldReload(true, routeInvocation{action: "list"}) ||
		routeCommandShouldReload(true, routeInvocation{action: "list", explicitDirectory: true}) ||
		routeCommandShouldReload(false, routeInvocation{action: "add"}) ||
		routeCommandShouldReload(true, routeInvocation{action: "add", explicitDirectory: true}) {
		t.Fatal("reload decision accepted a read-only or explicit command")
	}
	if !routeCommandShouldReload(true, routeInvocation{action: "add"}) ||
		!routeCommandShouldReload(true, routeInvocation{action: "delete"}) {
		t.Fatal("proxy add and delete skipped reload")
	}
	for _, action := range []string{"list", "add", "delete"} {
		if routeCommandShouldReload(false, routeInvocation{action: action}) {
			t.Fatalf("ssh %s reloads", action)
		}
	}
}

func TestIPv4MappedPrefixMatchesIPv4Target(t *testing.T) {
	mapped, err := parseRouteEntry("::ffff:203.0.113.10/128")
	plain, plainErr := parseRouteEntry("203.0.113.10")
	if err != nil || plainErr != nil || mapped.Key != plain.Key || mapped.Display != "203.0.113.10/32" {
		t.Fatalf("mapped %#v plain %#v %v %v", mapped, plain, err, plainErr)
	}
	if mapped.clash("DIRECT") != "IP-CIDR,203.0.113.10/32,DIRECT,no-resolve" {
		t.Fatal(mapped.clash("DIRECT"))
	}
	prefix, err := parseRouteEntry("::ffff:203.0.113.9/120")
	if err != nil || prefix.Display != "203.0.113.0/24" {
		t.Fatalf("prefix %#v %v", prefix, err)
	}
	all, err := parseRouteEntry("::ffff:0:0/96")
	if err != nil || all.Display != "0.0.0.0/0" {
		t.Fatalf("mapped all %#v %v", all, err)
	}
	whole, err := parseRouteEntry("::/0")
	if err != nil || whole.Display != "::/0" {
		t.Fatalf("ipv6 all %#v %v", whole, err)
	}
	if !routeTargetMatches([]string{"::ffff:203.0.113.10/128"}, "203.0.113.10:443") {
		t.Fatal("mapped blacklist missed the ipv4 target")
	}
	if !routeTargetMatches([]string{"example.com"}, "example.com:443") ||
		routeTargetMatches([]string{"example.com"}, "other.example:443") {
		t.Fatal("domain blacklist match")
	}
	home := t.TempDir()
	if _, err := addRouteEntry(home, sshRoutePolicyFilename, routeListBlacklist, "::ffff:203.0.113.10/128"); err != nil {
		t.Fatal(err)
	}
	if _, err := addRouteEntry(home, sshRoutePolicyFilename, routeListBlacklist, "203.0.113.10"); !errors.Is(err, errRouteDuplicate) {
		t.Fatalf("duplicate mapped %v", err)
	}
	skip, err := sshRouteSkipsProxy(home, "203.0.113.10:443")
	if err != nil || !skip {
		t.Fatalf("skip %t %v", skip, err)
	}
	skip, err = sshRouteSkipsProxy(home, "example.com:443")
	if err != nil || skip {
		t.Fatalf("unlisted domain skip %t %v", skip, err)
	}
}

func TestRouteCommandsStayOnExplicitDirectory(t *testing.T) {
	home := t.TempDir()
	previous := reloadProxyRouteBackend
	reloadProxyRouteBackend = func() error {
		t.Fatal("explicit directory contacted the backend")
		return nil
	}
	t.Cleanup(func() { reloadProxyRouteBackend = previous })
	output := captureCLIOutput(t, func() error {
		return routeCommand([]string{"whitelist", "add", "=api.example.com", "--directory", home})
	})
	if !strings.Contains(output, "Added =api.example.com") {
		t.Fatalf("output %q", output)
	}
	if err := sshRouteCommand([]string{"blacklist", "add", "skip.example", "--directory", home}); err != nil {
		t.Fatal(err)
	}
	proxyData, err := os.ReadFile(filepath.Join(home, proxyRoutePolicyFilename))
	if err != nil || strings.Contains(string(proxyData), "skip.example") || !strings.Contains(string(proxyData), "=api.example.com") {
		t.Fatalf("proxy file %s %v", proxyData, err)
	}
	sshData, err := os.ReadFile(filepath.Join(home, sshRoutePolicyFilename))
	if err != nil || strings.Contains(string(sshData), "api.example.com") || !strings.Contains(string(sshData), "skip.example") {
		t.Fatalf("ssh file %s %v", sshData, err)
	}
	listed := captureCLIOutput(t, func() error {
		return routeCommand([]string{"--directory", home, "whitelist", "list"})
	})
	again := captureCLIOutput(t, func() error {
		return routeCommand([]string{"whitelist", "list", "--directory", home})
	})
	if listed != again || strings.TrimSpace(listed) != "=api.example.com" || strings.Contains(listed, "Reloaded") {
		t.Fatalf("list %q", listed)
	}
	sshListed := captureCLIOutput(t, func() error {
		return sshRouteCommand([]string{"blacklist", "list", "--directory", home})
	})
	if strings.TrimSpace(sshListed) != "skip.example" || strings.Contains(sshListed, "Reloaded") {
		t.Fatalf("ssh list %q", sshListed)
	}
}

func TestRouteListPagesByVisibleWindow(t *testing.T) {
	model := newRowEnterModel(t)
	model.width = 120
	model.height = 24
	home := model.paths.HomeDir
	const count = 40
	for index := 0; index < count; index++ {
		if _, err := addRouteEntry(home, proxyRoutePolicyFilename, routeListWhitelist, fmt.Sprintf("h%02d.example", index)); err != nil {
			t.Fatal(err)
		}
	}
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.FocusSidebar = false
	model.snapshot.SelectedDashboard = tuiDashboardProxyWhitelistRow
	if command := pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}); command != nil || !model.routeListOpen {
		t.Fatal("route list did not open")
	}
	window := model.routeListWindow()
	if window < 2 || window >= count {
		t.Fatalf("window %d", window)
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyPgDown})
	if model.routeListCursor != window {
		t.Fatalf("page down cursor %d window %d", model.routeListCursor, window)
	}
	plain := stripTUIANSI(model.View())
	selected := model.routeListEntries[model.routeListCursor]
	if !strings.Contains(plain, selected) || strings.Contains(plain, model.routeListEntries[0]) {
		t.Fatalf("selected %s not kept on screen:\n%s", selected, plain)
	}
	if !strings.Contains(plain, "PgUp") {
		t.Fatal("editor hint does not mention paging")
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyPgUp})
	if model.routeListCursor != 0 {
		t.Fatalf("page up cursor %d", model.routeListCursor)
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	if model.routeListCursor != 1 {
		t.Fatalf("down cursor %d", model.routeListCursor)
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyUp})
	if model.routeListCursor != 0 {
		t.Fatalf("up cursor %d", model.routeListCursor)
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyPgDown})
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyPgDown})
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyPgDown})
	if model.routeListCursor != count-1 {
		t.Fatalf("page down did not stop at the end: %d", model.routeListCursor)
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.routeListOpen {
		t.Fatal("esc left the list open")
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if model.inputMode != tuiInputRouteEntry {
		t.Fatal("n did not start add")
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("added.example")})
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if len(model.routeListEntries) != count+1 || model.routeListEntries[count] != "added.example" {
		t.Fatalf("add %#v", model.routeListEntries)
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if len(model.routeListEntries) != count || model.routeListEntries[count-1] == "added.example" {
		t.Fatalf("delete %#v", model.routeListEntries)
	}
	model.width = 80
	compact := stripTUIANSI(model.View())
	if !strings.Contains(compact, model.routeListEntries[model.routeListCursor]) || !strings.Contains(compact, "PgUp") {
		t.Fatalf("compact editor hid the selection:\n%s", compact)
	}
	if command := pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}); command == nil || !model.frontendExitRequested {
		t.Fatal("q did not request TUI exit")
	}
	tiny := newRowEnterModel(t)
	tiny.width = 30
	tiny.height = 24
	if !tuiLayoutAtSize(tiny.width, tiny.height, tiny.snapshot.Language).Tiny {
		t.Fatal("width 30 did not use the tiny layout")
	}
	for index := 0; index < 20; index++ {
		if _, err := addRouteEntry(tiny.paths.HomeDir, proxyRoutePolicyFilename, routeListWhitelist, fmt.Sprintf("t%02d.example", index)); err != nil {
			t.Fatal(err)
		}
	}
	tiny.snapshot.Page = tuiPageDashboard
	tiny.snapshot.FocusSidebar = false
	tiny.snapshot.SelectedDashboard = tuiDashboardProxyWhitelistRow
	if command := pressRowEnterKey(tiny, tea.KeyMsg{Type: tea.KeyEnter}); command != nil || !tiny.routeListOpen {
		t.Fatal("tiny route list did not open")
	}
	tinyPlain := stripTUIANSI(tiny.View())
	shown := 0
	for _, entry := range tiny.routeListEntries {
		if strings.Contains(tinyPlain, entry) {
			shown++
		}
	}
	if shown < 1 || tiny.routeListWindow() != shown {
		t.Fatalf("tiny window %d shown %d", tiny.routeListWindow(), shown)
	}
	before := tiny.routeListCursor
	pressRowEnterKey(tiny, tea.KeyMsg{Type: tea.KeyPgDown})
	if tiny.routeListCursor != before+shown {
		t.Fatalf("tiny page down cursor %d shown %d", tiny.routeListCursor, shown)
	}
	if !strings.Contains(stripTUIANSI(tiny.View()), tiny.routeListEntries[tiny.routeListCursor]) {
		t.Fatal("tiny page hid the selected entry")
	}
	interruptModel := newRowEnterModel(t)
	interruptModel.snapshot.Page = tuiPageDashboard
	interruptModel.snapshot.FocusSidebar = false
	interruptModel.snapshot.SelectedDashboard = tuiDashboardProxyWhitelistRow
	pressRowEnterKey(interruptModel, tea.KeyMsg{Type: tea.KeyEnter})
	if command := pressRowEnterKey(interruptModel, tea.KeyMsg{Type: tea.KeyCtrlC}); command == nil || !interruptModel.shutdownRequested || interruptModel.frontendExitRequested {
		t.Fatal("interrupt did not leave the TUI")
	}
}

func TestDashboardRouteRowEditsProxyList(t *testing.T) {
	model := newRowEnterModel(t)
	home := model.paths.HomeDir
	writeRouteTestProfile(t, home)
	model.paths.ConfigPath = filepath.Join(home, "config.yaml")
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.FocusSidebar = false
	model.snapshot.SelectedDashboard = tuiDashboardProxyWhitelistRow
	model.snapshot.Settings.Mode = "rule"
	if command := pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}); command != nil || !model.routeListOpen || model.routeListStore != proxyRoutePolicyFilename {
		t.Fatalf("settings enter open=%t store=%s", model.routeListOpen, model.routeListStore)
	}
	if !strings.Contains(stripTUIANSI(model.View()), "Rule: blacklist is DIRECT") {
		t.Fatalf("settings list note missing:\n%s", stripTUIANSI(model.View()))
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if model.inputMode != tuiInputRouteEntry {
		t.Fatal("add input did not open")
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("example.com")})
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if !model.routeListOpen || len(model.routeListEntries) != 1 || model.routeListEntries[0] != "example.com" {
		t.Fatalf("entries %#v open=%t status=%s", model.routeListEntries, model.routeListOpen, model.snapshot.Status)
	}
	if err := rememberTUIFLCOutbound(home, "PROXY"); err != nil {
		t.Fatal(err)
	}
	runtime, err := writeTUIManagedRuntimeConfig(model.paths, "rule", 17891, false, tuiTunScopeUser, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cleanupTUISilentRuntimeConfigs(home, "") })
	if !strings.Contains(strings.Join(routeTestRules(t, runtime), "\n"), "DOMAIN-SUFFIX,example.com,PROXY") {
		t.Fatal(routeTestRules(t, runtime))
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.routeListOpen {
		t.Fatal("esc left the dashboard list open")
	}
	plain := stripTUIANSI(model.View())
	if !strings.Contains(plain, "Whitelist") || !strings.Contains(plain, "Blacklist") {
		t.Fatalf("dashboard rows missing:\n%s", plain)
	}
	if strings.Contains(plain, "Allow LAN") {
		t.Fatalf("proxy lists stayed on Settings:\n%s", plain)
	}
}

func TestSSHRouteRowIsSeparateFromProxy(t *testing.T) {
	model := newRowEnterModel(t)
	model.snapshot.Page = tuiPageSSH
	model.snapshot.FocusSidebar = false
	model.snapshot.SelectedSSH = tuiSSHCaptureRow
	model.snapshot.SSHProfiles = []tuiSSHProfile{{Name: "alpha"}, {Name: "beta"}}
	for _, want := range []int{0, 1, 2} {
		pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
		if model.snapshot.SelectedSSH != want {
			t.Fatalf("selected %d want %d", model.snapshot.SelectedSSH, want)
		}
	}
	if command := pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter}); command != nil || model.sshBusy || !model.routeListOpen || model.routeListStore != sshRoutePolicyFilename || model.routeListName != routeListWhitelist {
		t.Fatalf("ssh enter open=%t store=%s list=%s busy=%t", model.routeListOpen, model.routeListStore, model.routeListName, model.sshBusy)
	}
	plain := stripTUIANSI(model.View())
	if !strings.Contains(plain, "flc ssh uses the blacklist only") || !strings.Contains(plain, "Whitelist") {
		t.Fatalf("ssh list copy missing:\n%s", plain)
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.routeListOpen {
		t.Fatal("esc left the ssh list open")
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyDown})
	if model.snapshot.SelectedSSH != 3 {
		t.Fatalf("blacklist row %d", model.snapshot.SelectedSSH)
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.routeListName != routeListBlacklist {
		t.Fatalf("list %s", model.routeListName)
	}
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("skip.example")})
	pressRowEnterKey(model, tea.KeyMsg{Type: tea.KeyEnter})
	data, err := os.ReadFile(filepath.Join(model.paths.HomeDir, sshRoutePolicyFilename))
	if err != nil || !strings.Contains(string(data), "skip.example") {
		t.Fatalf("ssh file %s %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(model.paths.HomeDir, proxyRoutePolicyFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("proxy file appeared: %v", err)
	}
	if strings.Contains(stripTUIANSI(model.View()), "Blacklist") == false {
		t.Fatal("skip ssh row missing after close path")
	}
}

func TestPreserveSSHRouteRowAcrossProfileCount(t *testing.T) {
	current := tuiSnapshot{
		SelectedSSH: 2,
		SSHRouteRow: 1,
		SSHProfiles: []tuiSSHProfile{{Name: "a"}, {Name: "b"}},
	}
	merged := preserveTUIInteraction(current, tuiSnapshot{
		SSHProfiles: []tuiSSHProfile{{Name: "a"}, {Name: "b"}, {Name: "c"}},
	})
	if merged.SelectedSSH != 3 || merged.SSHRouteRow != 1 {
		t.Fatalf("preserved %#v", merged.SelectedSSH)
	}
	empty := preserveTUIInteraction(tuiSnapshot{SelectedSSH: 0}, tuiSnapshot{})
	if empty.SelectedSSH != tuiSSHCaptureRow || empty.SSHRouteRow != 0 {
		t.Fatalf("startup selection %#v row %d", empty.SelectedSSH, empty.SSHRouteRow)
	}
	kept := preserveTUIInteraction(tuiSnapshot{SelectedSSH: 0, SSHRouteRow: 1}, tuiSnapshot{})
	if kept.SelectedSSH != 0 || kept.SSHRouteRow != 1 {
		t.Fatalf("empty whitelist row %#v %d", kept.SelectedSSH, kept.SSHRouteRow)
	}
}

func TestDirectSSHCommandIgnoresRouteList(t *testing.T) {
	previousProbe := probeCLISSHRemoteForCommand
	previousRun := runCLICommandWithSSHProxyForCommand
	previousHook := routePolicyReadHook
	t.Cleanup(func() {
		probeCLISSHRemoteForCommand = previousProbe
		runCLICommandWithSSHProxyForCommand = previousRun
		routePolicyReadHook = previousHook
	})
	routePolicyReadHook = func(filename string) {
		t.Fatalf("direct exit read %s", filename)
	}
	probeCLISSHRemoteForCommand = func(cliSSHTunnelState) (cliSSHRemoteProbe, error) {
		return cliSSHRemoteProbe{
			ProtocolVersion: cliSSHRemoteProbeVersion,
			Available:       true,
			DirectAllowed:   true,
		}, nil
	}
	got := 0
	runCLICommandWithSSHProxyForCommand = func(args []string, port int) error {
		got = port
		return nil
	}
	if err := runCLICommandThroughSSHForCommand([]string{"true"}, cliSSHTunnelState{Port: 11, DirectPort: 22, Name: "lab"}, true); err != nil || got != 22 {
		t.Fatalf("direct port %d err %v", got, err)
	}
	if err := runCLICommandThroughSSHForCommand([]string{"true"}, cliSSHTunnelState{Port: 11, DirectPort: 22}, false); err != nil || got != 11 {
		t.Fatalf("normal port %d err %v", got, err)
	}
	if err := runCLICommandThroughSSHForCommand([]string{"true"}, cliSSHTunnelState{Port: 11}, true); err != nil || got != 11 {
		t.Fatalf("fallback port %d err %v", got, err)
	}
}

func TestSSHRelaySkipUsesDirectAndLeavesDashDAlone(t *testing.T) {
	home := t.TempDir()
	listener := listenCLITestTCP(t)
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	if _, err := addRouteEntry(home, sshRoutePolicyFilename, routeListBlacklist, "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := addRouteEntry(home, sshRoutePolicyFilename, routeListWhitelist, "keep.example"); err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{}, 4)
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
			select {
			case accepted <- struct{}{}:
			default:
			}
		}
	}()
	called := 0
	relay := routeTestRelay(home, func(target string) {
		called++
	})
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if status := routeTestHTTPConnect(t, relay, target, true); status != 200 || called != 0 {
		t.Fatalf("blacklist status %d calls %d", status, called)
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("blacklist did not dial directly")
	}
	if status := routeTestHTTPConnect(t, relay, "example.com:443", true); status != 200 || called != 1 {
		t.Fatalf("other status %d calls %d", status, called)
	}
	if status := routeTestHTTPConnect(t, relay, target, false); status != 200 || called != 2 {
		t.Fatalf("policy off status %d calls %d", status, called)
	}
	if code := routeTestSOCKSConnect(t, relay, port, true); code != 0 || called != 2 {
		t.Fatalf("socks code %d calls %d", code, called)
	}
}

func TestRelayOpensPolicyOffListener(t *testing.T) {
	relay, port, done := startCLITestRelay(t, 1)
	if relay.directListenPort == 0 || relay.directListenPort == port {
		t.Fatalf("direct %d public %d", relay.directListenPort, port)
	}
	relay.stop()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func writeRouteTestProfile(t *testing.T, home string) []byte {
	t.Helper()
	source := []byte(`mixed-port: 7890
allow-lan: false
mode: rule
proxy-groups:
  - name: PROXY
    type: select
    proxies: [DIRECT]
rules:
  - MATCH,PROXY
`)
	if err := os.WriteFile(filepath.Join(home, "config.yaml"), source, 0o600); err != nil {
		t.Fatal(err)
	}
	return source
}

func routeTestDocument(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

func routeTestRules(t *testing.T, path string) []string {
	t.Helper()
	return routeTestRuleList(t, routeTestDocument(t, path))
}

func routeTestRuleList(t *testing.T, document map[string]any) []string {
	t.Helper()
	raw, ok := document["rules"].([]any)
	if !ok {
		t.Fatalf("rules %#v", document["rules"])
	}
	rules := make([]string, 0, len(raw))
	for _, rule := range raw {
		text, ok := rule.(string)
		if !ok {
			t.Fatalf("rule %#v", rule)
		}
		rules = append(rules, text)
	}
	return rules
}

func routeTestRelay(home string, record func(string)) *cliSSHRelay {
	return &cliSSHRelay{
		policyHome:   home,
		upstreamPort: 1,
		active:       map[string]*cliSSHRelayFlowState{},
		dialUpstream: func(ctx context.Context, port int, target string) (net.Conn, error) {
			record(target)
			left, right := net.Pipe()
			_ = right.Close()
			return left, nil
		},
	}
}

func routeTestHTTPConnect(t *testing.T, relay *cliSSHRelay, target string, apply bool) int {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.serveHTTP(&cliRelayBufferedConn{Conn: server, reader: bufio.NewReader(server)}, apply)
	}()
	if _, err := fmt.Fprintf(client, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target); err != nil {
		t.Fatal(err)
	}
	_ = client.SetDeadline(time.Now().Add(2 * time.Second))
	reader := bufio.NewReader(client)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	code := 0
	if _, err := fmt.Sscanf(status, "HTTP/1.1 %d", &code); err != nil {
		t.Fatalf("status %q %v", status, err)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("http relay did not finish")
	}
	return code
}

func routeTestSOCKSConnect(t *testing.T, relay *cliSSHRelay, port int, apply bool) byte {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		relay.serveSOCKS(&cliRelayBufferedConn{Conn: server, reader: bufio.NewReader(server)}, apply)
	}()
	if _, err := client.Write([]byte{5, 1, 0}); err != nil {
		t.Fatal(err)
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(client, method); err != nil {
		t.Fatal(err)
	}
	request := []byte{5, 1, 0, 1, 127, 0, 0, 1, byte(port >> 8), byte(port)}
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(client, reply); err != nil {
		t.Fatal(err)
	}
	_ = client.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("socks relay did not finish")
	}
	return reply[1]
}
