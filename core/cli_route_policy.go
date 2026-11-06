//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	proxyRoutePolicyFilename = ".flclash-route-policy.json"
	sshRoutePolicyFilename   = ".flclash-ssh-route.json"
	routeListWhitelist       = "whitelist"
	routeListBlacklist       = "blacklist"
	routeListLimit           = 200
	routeKindSuffix          = "suffix"
	routeKindExact           = "exact"
	routeKindCIDR            = "cidr"
)

var (
	errRouteDuplicate = errors.New("entry is already on this list")
	errRouteOverlap   = errors.New("entry is on the other list")
	errRouteFull      = errors.New("this list already has 200 entries")
	errRouteMissing   = errors.New("entry was not found")
	errRouteEmptyHome = errors.New("route list directory is empty")
)

type routeInvalidError struct {
	Reason string
}

func (e routeInvalidError) Error() string {
	return "invalid route entry: " + e.Reason
}

type routeEntry struct {
	Display string
	Kind    string
	Key     string
}

type routePolicy struct {
	Whitelist []string
	Blacklist []string
	extra     map[string]json.RawMessage
}

// routePolicyReadHook is a test probe. Production leaves it nil.
var routePolicyReadHook func(filename string)

func routePolicyPath(home, filename string) (string, bool) {
	home = strings.TrimSpace(home)
	if home == "" {
		return "", false
	}
	return filepath.Join(home, filename), true
}

func loadRoutePolicy(home, filename string) (routePolicy, error) {
	if routePolicyReadHook != nil {
		routePolicyReadHook(filename)
	}
	policy := routePolicy{Whitelist: []string{}, Blacklist: []string{}}
	path, ok := routePolicyPath(home, filename)
	if !ok {
		return policy, nil
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return policy, nil
	}
	if err != nil {
		return policy, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return policy, fmt.Errorf("read route list: %w", err)
	}
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	policy.extra = raw
	if err := decodeRouteList(raw["whitelist"], &policy.Whitelist); err != nil {
		return policy, err
	}
	if err := decodeRouteList(raw["blacklist"], &policy.Blacklist); err != nil {
		return policy, err
	}
	return policy, nil
}

func decodeRouteList(raw json.RawMessage, dest *[]string) error {
	if len(bytesTrimSpace(raw)) == 0 || string(raw) == "null" {
		*dest = []string{}
		return nil
	}
	var entries []string
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("read route list: %w", err)
	}
	if entries == nil {
		entries = []string{}
	}
	*dest = entries
	return nil
}

func bytesTrimSpace(raw []byte) []byte {
	return []byte(strings.TrimSpace(string(raw)))
}

func (policy *routePolicy) entries(list string) []string {
	if list == routeListBlacklist {
		return policy.Blacklist
	}
	return policy.Whitelist
}

func (policy *routePolicy) setEntries(list string, entries []string) {
	if entries == nil {
		entries = []string{}
	}
	if list == routeListBlacklist {
		policy.Blacklist = entries
		return
	}
	policy.Whitelist = entries
}

func mutateRoutePolicy(home, filename string, update func(*routePolicy) error) error {
	home = strings.TrimSpace(home)
	if home == "" {
		return errRouteEmptyHome
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		return err
	}
	lockPath := filepath.Join(home, filename+".lock")
	owner := cliProcessOwner{Kind: "route-policy", PID: os.Getpid(), HomeDir: home, StartedAt: time.Now()}
	var lock *cliFileLock
	var err error
	deadline := time.Now().Add(2 * time.Second)
	for {
		lock, err = acquireCLIFileLock(lockPath, owner)
		var busy *cliLockBusyError
		if err == nil || !errors.As(err, &busy) || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	defer lock.release()
	policy, err := loadRoutePolicy(home, filename)
	if err != nil {
		return err
	}
	if err := update(&policy); err != nil {
		return err
	}
	return writeRoutePolicy(home, filename, policy)
}

func writeRoutePolicy(home, filename string, policy routePolicy) error {
	path, ok := routePolicyPath(home, filename)
	if !ok {
		return errRouteEmptyHome
	}
	raw := policy.extra
	if raw == nil {
		raw = map[string]json.RawMessage{}
	}
	whitelist, err := json.Marshal(policy.Whitelist)
	if err != nil {
		return err
	}
	blacklist, err := json.Marshal(policy.Blacklist)
	if err != nil {
		return err
	}
	raw["whitelist"] = whitelist
	raw["blacklist"] = blacklist
	data, err := json.MarshalIndent(raw, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(home, filename+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if err := file.Chmod(0o600); err != nil {
		return err
	}
	if _, err := file.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

func addRouteEntry(home, filename, list, raw string) (string, error) {
	entry, err := parseRouteEntry(raw)
	if err != nil {
		return "", err
	}
	err = mutateRoutePolicy(home, filename, func(policy *routePolicy) error {
		current := append([]string(nil), policy.entries(list)...)
		otherName := routeListWhitelist
		if list == routeListWhitelist {
			otherName = routeListBlacklist
		}
		if routeEntriesContain(current, entry.Key) {
			return errRouteDuplicate
		}
		if routeEntriesContain(policy.entries(otherName), entry.Key) {
			return errRouteOverlap
		}
		if len(current) >= routeListLimit {
			return errRouteFull
		}
		policy.setEntries(list, append(current, entry.Display))
		return nil
	})
	if err != nil {
		return "", err
	}
	return entry.Display, nil
}

func deleteRouteEntry(home, filename, list, raw string) (string, error) {
	entry, err := parseRouteEntry(raw)
	if err != nil {
		return "", err
	}
	err = mutateRoutePolicy(home, filename, func(policy *routePolicy) error {
		current := policy.entries(list)
		next := make([]string, 0, len(current))
		removed := false
		for _, stored := range current {
			parsed, parseErr := parseRouteEntry(stored)
			if !removed && parseErr == nil && parsed.Key == entry.Key {
				removed = true
				continue
			}
			next = append(next, stored)
		}
		if !removed {
			return errRouteMissing
		}
		policy.setEntries(list, next)
		return nil
	})
	if err != nil {
		return "", err
	}
	return entry.Display, nil
}

func routeEntriesContain(entries []string, key string) bool {
	for _, stored := range entries {
		parsed, err := parseRouteEntry(stored)
		if err == nil && parsed.Key == key {
			return true
		}
	}
	return false
}

func parseRouteEntry(raw string) (routeEntry, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return routeEntry{}, routeInvalidError{Reason: "empty"}
	}
	if strings.Contains(raw, "://") || strings.ContainsAny(raw, " \t\r\n\\?#@,") {
		return routeEntry{}, routeInvalidError{Reason: "unsupported syntax"}
	}
	if strings.HasPrefix(raw, "=") {
		domain, err := canonicalRouteDomain(strings.TrimPrefix(raw, "="))
		if err != nil || net.ParseIP(domain) != nil {
			return routeEntry{}, routeInvalidError{Reason: "exact host must be a domain"}
		}
		return routeEntry{Display: "=" + domain, Kind: routeKindExact, Key: routeKindExact + ":" + domain}, nil
	}
	suffix := false
	if strings.HasPrefix(raw, "*.") {
		suffix = true
		raw = strings.TrimPrefix(raw, "*.")
	}
	if !suffix && (strings.Contains(raw, "/") || net.ParseIP(raw) != nil) {
		return parseRouteIP(raw)
	}
	if strings.Contains(raw, ":") || strings.Contains(raw, "/") {
		return routeEntry{}, routeInvalidError{Reason: "unsupported syntax"}
	}
	domain, err := canonicalRouteDomain(raw)
	if err != nil {
		return routeEntry{}, err
	}
	return routeEntry{Display: domain, Kind: routeKindSuffix, Key: routeKindSuffix + ":" + domain}, nil
}

func canonicalRouteDomain(domain string) (string, error) {
	domain = strings.TrimSuffix(domain, ".")
	if strings.Contains(domain, "*") || !validRouteDomain(domain) {
		return "", routeInvalidError{Reason: "not a domain"}
	}
	return strings.ToLower(domain), nil
}

func validRouteDomain(domain string) bool {
	if domain == "" || len(domain) > 253 || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return false
	}
	for _, label := range strings.Split(domain, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for index := 0; index < len(label); index++ {
			value := label[index]
			if value > 127 || !((value >= 'a' && value <= 'z') || (value >= 'A' && value <= 'Z') || (value >= '0' && value <= '9') || value == '-') {
				return false
			}
		}
	}
	return true
}

func parseRouteIP(raw string) (routeEntry, error) {
	if strings.Contains(raw, "%") || strings.HasPrefix(raw, "[") {
		return routeEntry{}, routeInvalidError{Reason: "unsupported IP"}
	}
	if !strings.Contains(raw, "/") {
		ip := net.ParseIP(raw)
		if ip == nil {
			return routeEntry{}, routeInvalidError{Reason: "not an IP"}
		}
		bits := 128
		if ipv4 := ip.To4(); ipv4 != nil {
			ip = ipv4
			bits = 32
		}
		raw = ip.String() + "/" + fmt.Sprintf("%d", bits)
	}
	_, network, err := net.ParseCIDR(raw)
	if err != nil || network == nil {
		return routeEntry{}, routeInvalidError{Reason: "not an IP prefix"}
	}
	if canonical, ok := canonicalIPv4MappedPrefix(network); ok {
		network = canonical
	}
	display := network.String()
	return routeEntry{Display: display, Kind: routeKindCIDR, Key: routeKindCIDR + ":" + display}, nil
}

// canonicalIPv4MappedPrefix turns ::ffff:0:0/96 and longer into an IPv4
// prefix. Go's IPNet.Contains compares address lengths after To4, so a
// 16-byte mapped network never matches an IPv4 target.
func canonicalIPv4MappedPrefix(network *net.IPNet) (*net.IPNet, bool) {
	if network == nil {
		return nil, false
	}
	ones, bits := network.Mask.Size()
	if bits != 128 || ones < 96 {
		return nil, false
	}
	ipv4 := network.IP.To4()
	if ipv4 == nil {
		return nil, false
	}
	mask := net.CIDRMask(ones-96, 32)
	return &net.IPNet{IP: ipv4.Mask(mask), Mask: mask}, true
}

func (entry routeEntry) clash(target string) string {
	switch entry.Kind {
	case routeKindExact:
		return fmt.Sprintf("DOMAIN,%s,%s", strings.TrimPrefix(entry.Display, "="), target)
	case routeKindCIDR:
		return fmt.Sprintf("IP-CIDR,%s,%s,no-resolve", entry.Display, target)
	default:
		return fmt.Sprintf("DOMAIN-SUFFIX,%s,%s", entry.Display, target)
	}
}

func routeClashRules(stored []string, target string) ([]string, error) {
	rules := make([]string, 0, len(stored))
	for _, raw := range stored {
		entry, err := parseRouteEntry(raw)
		if err != nil {
			return nil, err
		}
		rules = append(rules, entry.clash(target))
	}
	return rules, nil
}

// compileProxyRouteRules reads only the proxy list. Silent and global already
// send unnamed traffic to the proxy, so they emit the blacklist only. Direct
// already sends unnamed traffic direct, so it emits the whitelist only.
func compileProxyRouteRules(home, mode, outbound string) ([]string, error) {
	policy, err := loadRoutePolicy(home, proxyRoutePolicyFilename)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(outbound) == "" {
		outbound = "GLOBAL"
	}
	switch mode {
	case "direct":
		return routeClashRules(policy.Whitelist, outbound)
	case "global", tuiSilentMode:
		return routeClashRules(policy.Blacklist, "DIRECT")
	default:
		blacklist, err := routeClashRules(policy.Blacklist, "DIRECT")
		if err != nil {
			return nil, err
		}
		whitelist, err := routeClashRules(policy.Whitelist, outbound)
		if err != nil {
			return nil, err
		}
		return append(blacklist, whitelist...), nil
	}
}

func sshRouteSkipsProxy(home, target string) (bool, error) {
	policy, err := loadRoutePolicy(home, sshRoutePolicyFilename)
	if err != nil {
		return false, err
	}
	return routeTargetMatches(policy.Blacklist, target), nil
}

func routeTargetHost(target string) string {
	host, _, err := net.SplitHostPort(target)
	if err != nil {
		return strings.Trim(strings.TrimSpace(target), "[]")
	}
	return host
}

func routeTargetMatches(stored []string, target string) bool {
	host := routeTargetHost(target)
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	for _, raw := range stored {
		entry, err := parseRouteEntry(raw)
		if err != nil {
			continue
		}
		if entry.matches(host, ip) {
			return true
		}
	}
	return false
}

func (entry routeEntry) matches(host string, ip net.IP) bool {
	switch entry.Kind {
	case routeKindCIDR:
		if ip == nil {
			return false
		}
		_, network, err := net.ParseCIDR(entry.Display)
		return err == nil && network.Contains(ip)
	case routeKindExact:
		if ip != nil {
			return false
		}
		return strings.EqualFold(strings.TrimSuffix(host, "."), strings.TrimPrefix(entry.Display, "="))
	case routeKindSuffix:
		if ip != nil {
			return false
		}
		name := strings.ToLower(strings.TrimSuffix(host, "."))
		domain := strings.ToLower(entry.Display)
		return name == domain || strings.HasSuffix(name, "."+domain)
	default:
		return false
	}
}
