//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	cliSSHAttachedSOCKSKind = "attached-socks"
	cliSSHCaptureMasterKind = "master"
	cliSSHCaptureSOCKSKind  = "socks"
)

type cliSSHCaptureCandidate struct {
	Name        string
	Username    string
	Host        string
	Port        int
	Jump        string
	Kind        string
	ControlPath string
	SocksPort   int
	Source      string
	Label       string
}

type cliSSHParsedCommand struct {
	User          string
	Host          string
	Port          int
	Jump          string
	DynamicPorts  []int
	ControlSocket string
	ConfigFile    string
	ControlOp     bool
	Master        bool
}

type sshGResolved struct {
	User          string
	Port          int
	Jump          string
	ControlSocket string
	Master        bool
	DynamicPorts  []int
}

var sshGResolveCache sync.Map

var listProcessLoopbackListenPorts = listProcessLoopbackListenPortsImpl
var readProcessCommandLine = readProcessCommandLineImpl
var readProcessComm = readProcessCommImpl
var readProcessParentComm = readProcessParentCommImpl

func cliSSHTunnelIsAttached(kind string) bool {
	return kind == cliSSHAttachedKind || kind == cliSSHAttachedSOCKSKind
}

func discoverCLICaptureCandidates() []cliSSHCaptureCandidate {
	config, _ := loadCLISSHConfig()
	return discoverCLICaptureCandidatesWithProfiles(config.Profiles)
}

func discoverCLICaptureCandidatesWithProfiles(profiles []cliSSHProfile) []cliSSHCaptureCandidate {
	seen := map[string]bool{}
	candidates := make([]cliSSHCaptureCandidate, 0)
	add := func(candidate cliSSHCaptureCandidate) {
		candidate.Port = normalizeCLISSHCapturePort(candidate.Port)
		candidate.Username = strings.TrimSpace(candidate.Username)
		candidate.Host = strings.TrimSpace(candidate.Host)
		if candidate.Host == "" {
			return
		}
		key := candidate.Kind + "|" + strings.ToLower(candidate.Username) + "|" +
			strings.ToLower(candidate.Host) + "|" + strconv.Itoa(candidate.Port) + "|" +
			candidate.ControlPath + "|" + strconv.Itoa(candidate.SocksPort)
		if seen[key] {
			return
		}
		seen[key] = true
		if candidate.Name == "" {
			candidate.Name = captureCLISSHNameHint(candidate)
		}
		candidate.Label = formatCLICaptureCandidate(candidate)
		candidates = append(candidates, candidate)
	}

	active, activeOK, _ := activeCLIPersistentSSHTunnel()
	for _, profile := range profiles {
		profile = normalizeCLISSHProfile(profile)
		if profile.Username == "" || profile.Host == "" {
			continue
		}
		if activeOK && strings.EqualFold(active.Name, profile.Name) && cliSSHTunnelReady(active) {
			continue
		}
		if path, ok := findCLILiveSSHMaster(profile); ok {
			add(cliSSHCaptureCandidate{
				Name:        profile.Name,
				Username:    profile.Username,
				Host:        profile.Host,
				Port:        profile.Port,
				Jump:        profile.Jump,
				Kind:        cliSSHCaptureMasterKind,
				ControlPath: path,
				Source:      "profile",
			})
		}
	}
	for _, candidate := range discoverOpenSSHConfigMasters() {
		add(candidate)
	}
	for _, candidate := range discoverProcessSSHMasters() {
		add(candidate)
	}
	for _, candidate := range discoverDiskSSHMasters() {
		add(candidate)
	}
	for _, candidate := range discoverLiveSSHSocksCandidates() {
		add(candidate)
	}
	return candidates
}

func discoverOpenSSHConfigMasters() []cliSSHCaptureCandidate {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	entries, err := parseOpenSSHConfigFile(filepath.Join(home, ".ssh", "config"), 0)
	if err != nil {
		return nil
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return nil
	}
	candidates := make([]cliSSHCaptureCandidate, 0)
	for _, entry := range entries {
		if entry.Name == "" || openSSHHostPattern(entry.Name) {
			continue
		}
		resolved, resolveErr := resolveCLIImportedSSHHost(
			filepath.Join(home, ".ssh", "config"),
			entry,
		)
		if resolveErr != nil {
			continue
		}
		profile, skip := cliSSHProfileFromOpenSSHHost(resolved)
		if skip != "" {
			continue
		}
		path := cliSSHConfigControlPath(sshPath, profile)
		if path == "" || !cliSSHControlPathOwned(path) {
			continue
		}
		if !cliSSHMasterCheck(sshPath, path, profile) {
			continue
		}
		candidates = append(candidates, cliSSHCaptureCandidate{
			Name:        profile.Name,
			Username:    profile.Username,
			Host:        profile.Host,
			Port:        profile.Port,
			Jump:        profile.Jump,
			Kind:        cliSSHCaptureMasterKind,
			ControlPath: path,
			Source:      "config",
		})
	}
	return candidates
}

func discoverLiveSSHSocksCandidates() []cliSSHCaptureCandidate {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	self := os.Getuid()
	candidates := make([]cliSSHCaptureCandidate, 0)
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 0 {
			continue
		}
		if processUID(pid) != self {
			continue
		}
		args := readProcessCommandLine(pid)
		candidates = append(candidates, captureSOCKSCandidatesFromSSHProcess(pid, args)...)
	}
	return candidates
}

func discoverProcessSSHMasters() []cliSSHCaptureCandidate {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	self := os.Getuid()
	candidates := make([]cliSSHCaptureCandidate, 0)
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 0 {
			continue
		}
		if processUID(pid) != self {
			continue
		}
		args := readProcessCommandLine(pid)
		candidates = append(candidates, captureMasterCandidatesFromSSHProcess(pid, args)...)
	}
	return candidates
}

func captureSOCKSCandidatesFromSSHProcess(pid int, args []string) []cliSSHCaptureCandidate {
	if len(args) == 0 || !isOpenSSHClientName(args[0]) {
		return nil
	}
	parsed := enrichSSHParsedCommand(parseSSHCommandLine(args))
	if parsed.ControlOp || parsed.Host == "" || strings.HasPrefix(parsed.Host, "-") {
		return nil
	}
	if len(parsed.DynamicPorts) == 0 {
		return nil
	}
	ports := make([]int, 0, len(parsed.DynamicPorts))
	needListen := false
	for _, port := range parsed.DynamicPorts {
		if port > 0 {
			ports = append(ports, port)
			continue
		}
		needListen = true
	}
	if needListen {
		ports = append(ports, listProcessLoopbackListenPorts(pid)...)
	}
	source := captureSourceFromCommand(pid, parsed)
	candidates := make([]cliSSHCaptureCandidate, 0, len(ports))
	for _, port := range uniquePositivePorts(ports) {
		if isFlClashManagedSOCKSPort(port) || probeCLISSHSOCKS(port, 200*time.Millisecond) != nil {
			continue
		}
		candidates = append(candidates, cliSSHCaptureCandidate{
			Name:      captureCLISSHNameHint(cliSSHCaptureCandidate{Host: parsed.Host, Source: source}),
			Username:  parsed.User,
			Host:      parsed.Host,
			Port:      parsed.Port,
			Jump:      parsed.Jump,
			Kind:      cliSSHCaptureSOCKSKind,
			SocksPort: port,
			Source:    source,
		})
	}
	return candidates
}

func parseSSHCommandLine(args []string) cliSSHParsedCommand {
	parsed := cliSSHParsedCommand{Port: 22}
	expanded := expandSSHCommandArguments(args)
loop:
	for index := 1; index < len(expanded); index++ {
		arg := expanded[index]
		if arg == "--" {
			if index+1 < len(expanded) && parsed.Host == "" && !strings.HasPrefix(expanded[index+1], "-") {
				assignSSHParsedDestination(&parsed, expanded[index+1])
			}
			break
		}
		if arg == "-O" || arg == "-G" {
			parsed.ControlOp = true
			break
		}
		switch {
		case arg == "-D" && index+1 < len(expanded):
			index++
			parsed.DynamicPorts = append(parsed.DynamicPorts, parseSSHDynamicPort(expanded[index]))
		case strings.HasPrefix(arg, "-D") && len(arg) > 2:
			parsed.DynamicPorts = append(parsed.DynamicPorts, parseSSHDynamicPort(arg[2:]))
		case arg == "-p" && index+1 < len(expanded):
			index++
			parsed.Port = parseSSHPortValue(expanded[index], parsed.Port)
		case strings.HasPrefix(arg, "-p") && len(arg) > 2:
			parsed.Port = parseSSHPortValue(arg[2:], parsed.Port)
		case arg == "-l" && index+1 < len(expanded):
			index++
			parsed.User = strings.TrimSpace(expanded[index])
		case strings.HasPrefix(arg, "-l") && len(arg) > 2:
			parsed.User = strings.TrimSpace(arg[2:])
		case arg == "-J" && index+1 < len(expanded):
			index++
			parsed.Jump = strings.TrimSpace(expanded[index])
		case strings.HasPrefix(arg, "-J") && len(arg) > 2:
			parsed.Jump = strings.TrimSpace(arg[2:])
		case arg == "-S" && index+1 < len(expanded):
			index++
			parsed.ControlSocket = expanded[index]
		case strings.HasPrefix(arg, "-S") && len(arg) > 2:
			parsed.ControlSocket = arg[2:]
		case arg == "-F" && index+1 < len(expanded):
			index++
			parsed.ConfigFile = expanded[index]
		case strings.HasPrefix(arg, "-F") && len(arg) > 2:
			parsed.ConfigFile = arg[2:]
		case arg == "-M":
			parsed.Master = true
		case arg == "-o" && index+1 < len(expanded):
			index++
			option := expanded[index]
			if !strings.Contains(option, "=") && index+1 < len(expanded) &&
				!strings.HasPrefix(expanded[index+1], "-") &&
				sshOptionTakesSeparateValue(option) {
				index++
				option += "=" + expanded[index]
			}
			applySSHCommandOption(&parsed, option)
		case strings.HasPrefix(arg, "-o") && len(arg) > 2:
			applySSHCommandOption(&parsed, arg[2:])
		case arg == "-B" || arg == "-E" || arg == "-I" || arg == "-i" ||
			arg == "-L" || arg == "-Q" || arg == "-R" || arg == "-W" ||
			arg == "-b" || arg == "-c" || arg == "-e" || arg == "-m" ||
			arg == "-w":
			index++
		case strings.HasPrefix(arg, "-"):
			continue
		default:
			if parsed.Host == "" {
				assignSSHParsedDestination(&parsed, arg)
				continue
			}
			break loop
		}
	}
	if parsed.ControlSocket != "" {
		parsed.ControlSocket = expandCLISSHIdentityPath(parsed.ControlSocket)
	}
	if parsed.ConfigFile != "" {
		parsed.ConfigFile = expandCLISSHIdentityPath(parsed.ConfigFile)
	}
	return parsed
}

func assignSSHParsedDestination(parsed *cliSSHParsedCommand, value string) {
	user, host := splitSSHDestination(value)
	if parsed.User == "" {
		parsed.User = user
	}
	parsed.Host = host
}

func expandSSHCommandArguments(args []string) []string {
	if len(args) == 0 {
		return args
	}
	expanded := make([]string, 0, len(args)+4)
	expanded = append(expanded, args[0])
	for _, arg := range args[1:] {
		expanded = append(expanded, expandSSHClusteredArgument(arg)...)
	}
	return expanded
}

func expandSSHClusteredArgument(arg string) []string {
	if !strings.HasPrefix(arg, "-") || strings.HasPrefix(arg, "--") || len(arg) < 3 {
		return []string{arg}
	}
	if arg[1] == 'D' || arg[1] == 'p' || arg[1] == 'l' || arg[1] == 'o' ||
		arg[1] == 'J' || arg[1] == 'S' || arg[1] == 'F' || arg[1] == 'i' {
		return []string{arg}
	}
	boolean := map[byte]bool{
		'4': true, '6': true, 'A': true, 'a': true, 'C': true, 'f': true,
		'g': true, 'K': true, 'k': true, 'M': true, 'N': true, 'n': true,
		'q': true, 's': true, 'T': true, 't': true, 'V': true, 'v': true,
		'X': true, 'x': true, 'Y': true, 'y': true,
	}
	valueFlag := map[byte]bool{
		'b': true, 'c': true, 'D': true, 'e': true, 'E': true, 'F': true,
		'I': true, 'i': true, 'J': true, 'L': true, 'l': true, 'm': true,
		'O': true, 'o': true, 'p': true, 'R': true, 'S': true, 'W': true,
		'w': true,
	}
	out := make([]string, 0, len(arg))
	for index := 1; index < len(arg); index++ {
		flag := arg[index]
		if valueFlag[flag] {
			out = append(out, "-"+string(flag))
			if index+1 < len(arg) {
				out = append(out, arg[index+1:])
			}
			return out
		}
		if !boolean[flag] {
			return []string{arg}
		}
		out = append(out, "-"+string(flag))
	}
	if len(out) == 0 {
		return []string{arg}
	}
	return out
}

func applySSHCommandOption(parsed *cliSSHParsedCommand, option string) {
	key, value, ok := strings.Cut(option, "=")
	if !ok {
		return
	}
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "dynamicforward":
		parsed.DynamicPorts = append(parsed.DynamicPorts, parseSSHDynamicPort(value))
	case "user":
		if parsed.User == "" {
			parsed.User = strings.TrimSpace(value)
		}
	case "port":
		parsed.Port = parseSSHPortValue(value, parsed.Port)
	case "proxyjump", "jumphost":
		if parsed.Jump == "" && !strings.EqualFold(strings.TrimSpace(value), "none") {
			parsed.Jump = strings.TrimSpace(value)
		}
	case "controlpath":
		if parsed.ControlSocket == "" {
			parsed.ControlSocket = expandCLISSHIdentityPath(strings.TrimSpace(value))
		}
	case "controlmaster":
		if cliSSHControlMasterEnabled(value) {
			parsed.Master = true
		}
	}
}

func sshOptionTakesSeparateValue(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "dynamicforward", "user", "port", "proxyjump", "jumphost",
		"controlpath", "controlmaster", "identityfile", "hostname",
		"proxycommand", "localforward", "remoteforward":
		return true
	default:
		return false
	}
}

func enrichSSHParsedCommand(parsed cliSSHParsedCommand) cliSSHParsedCommand {
	if parsed.Host == "" || parsed.ControlOp {
		return parsed
	}
	resolved, ok := resolveSSHGConfig(parsed)
	if !ok {
		return parsed
	}
	if parsed.User == "" {
		parsed.User = resolved.User
	}
	if (parsed.Port == 0 || parsed.Port == 22) && resolved.Port > 0 {
		parsed.Port = resolved.Port
	}
	if parsed.Jump == "" {
		parsed.Jump = resolved.Jump
	}
	if resolved.Master {
		parsed.Master = true
	}
	if parsed.ControlSocket == "" && parsed.Master {
		parsed.ControlSocket = resolved.ControlSocket
	}
	if len(parsed.DynamicPorts) == 0 {
		parsed.DynamicPorts = append(parsed.DynamicPorts, resolved.DynamicPorts...)
	}
	return parsed
}

func resolveSSHGConfig(parsed cliSSHParsedCommand) (sshGResolved, bool) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return sshGResolved{}, false
	}
	key := strings.Join([]string{
		sshPath,
		parsed.ConfigFile,
		parsed.User,
		strconv.Itoa(parsed.Port),
		parsed.Jump,
		parsed.Host,
	}, "\x00")
	if cached, ok := sshGResolveCache.Load(key); ok {
		return cached.(sshGResolved), true
	}
	args := []string{"-G"}
	if parsed.ConfigFile != "" {
		args = append(args, "-F", parsed.ConfigFile)
	}
	if parsed.User != "" {
		args = append(args, "-l", parsed.User)
	}
	if parsed.Port > 0 {
		args = append(args, "-p", strconv.Itoa(parsed.Port))
	}
	if parsed.Jump != "" {
		args = append(args, "-o", "ProxyJump="+parsed.Jump)
	}
	args = append(args, parsed.Host)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, sshPath, args...)
	command.WaitDelay = time.Second
	prepareCLISSHNonInteractiveCommand(command)
	output, err := command.Output()
	if err != nil {
		return sshGResolved{}, false
	}
	resolved := sshGResolved{}
	for _, line := range strings.Split(string(output), "\n") {
		lineKey, value, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.ToLower(lineKey) {
		case "user":
			resolved.User = value
		case "port":
			resolved.Port = parseSSHPortValue(value, 0)
		case "proxyjump":
			if value != "" && !strings.EqualFold(value, "none") {
				resolved.Jump = value
			}
		case "controlpath":
			if value != "" && !strings.EqualFold(value, "none") {
				resolved.ControlSocket = expandCLISSHIdentityPath(value)
			}
		case "controlmaster":
			if cliSSHControlMasterEnabled(value) {
				resolved.Master = true
			}
		case "dynamicforward":
			resolved.DynamicPorts = append(resolved.DynamicPorts, parseSSHDynamicPort(value))
		}
	}
	sshGResolveCache.Store(key, resolved)
	return resolved, true
}

func captureMasterCandidatesFromSSHProcess(pid int, args []string) []cliSSHCaptureCandidate {
	if len(args) == 0 || !isOpenSSHClientName(args[0]) {
		return nil
	}
	parsed := enrichSSHParsedCommand(parseSSHCommandLine(args))
	if parsed.ControlOp || parsed.Host == "" || strings.HasPrefix(parsed.Host, "-") {
		return nil
	}
	path := expandCLISSHIdentityPath(strings.TrimSpace(parsed.ControlSocket))
	if path == "" || strings.Contains(path, "%") ||
		isFlClashManagedSSHPath(path) || !cliSSHControlPathOwned(path) {
		return nil
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil || !cliSSHMasterCheckSocket(sshPath, path, parsed) {
		return nil
	}
	source := captureSourceFromCommand(pid, parsed)
	return []cliSSHCaptureCandidate{{
		Name:        captureCLISSHNameHint(cliSSHCaptureCandidate{Host: parsed.Host, Source: source}),
		Username:    parsed.User,
		Host:        parsed.Host,
		Port:        parsed.Port,
		Jump:        parsed.Jump,
		Kind:        cliSSHCaptureMasterKind,
		ControlPath: path,
		Source:      source,
	}}
}

func cliSSHMasterCheckSocket(sshPath, controlPath string, parsed cliSSHParsedCommand) bool {
	destination := formatCLISSHDestination(parsed.User, parsed.Host)
	if destination == "" || destination == "@" {
		destination = "flclash-capture"
	}
	return inspectCLISSHMaster(sshPath, cliSSHTunnelState{
		ControlPath: controlPath,
		Destination: destination,
	}) == cliSSHMasterAliveStatus
}

func discoverDiskSSHMasters() []cliSSHCaptureCandidate {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return nil
	}
	candidates := make([]cliSSHCaptureCandidate, 0)
	seen := map[string]bool{}
	for _, root := range controlSocketSearchRoots() {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() {
				if path != root && (strings.HasPrefix(entry.Name(), ".") || depthFromRoot(root, path) > 2) {
					return filepath.SkipDir
				}
				return nil
			}
			info, infoErr := entry.Info()
			if infoErr != nil || !looksLikeSSHControlSocket(path, info.Mode()) ||
				isFlClashManagedSSHPath(path) || seen[path] || !cliSSHControlPathOwned(path) {
				return nil
			}
			if !cliSSHMasterCheckSocket(sshPath, path, cliSSHParsedCommand{Host: "flclash-capture"}) {
				return nil
			}
			seen[path] = true
			host := hostHintFromControlPath(path)
			candidates = append(candidates, cliSSHCaptureCandidate{
				Name:        captureCLISSHNameHint(cliSSHCaptureCandidate{Host: host, Source: "ssh"}),
				Username:    "",
				Host:        firstNonEmpty(host, "captured"),
				Port:        22,
				Kind:        cliSSHCaptureMasterKind,
				ControlPath: path,
				Source:      "socket",
			})
			return nil
		})
	}
	return candidates
}

func controlSocketSearchRoots() []string {
	roots := make([]string, 0, 8)
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots,
			filepath.Join(home, ".ssh"),
			filepath.Join(home, ".ssh", "sockets"),
			filepath.Join(home, ".ssh", "cm"),
			filepath.Join(home, ".ssh", "masters"),
			filepath.Join(home, ".ssh", "controlmasters"),
		)
	}
	if dir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); dir != "" {
		roots = append(roots, filepath.Join(dir, "ssh"))
		runtimeMatches, _ := filepath.Glob(filepath.Join(dir, "ssh-*"))
		roots = append(roots, runtimeMatches...)
	}
	matches, _ := filepath.Glob("/tmp/ssh-*")
	roots = append(roots, matches...)
	existing := make([]string, 0, len(roots))
	for _, root := range roots {
		if info, err := os.Stat(root); err == nil && info.IsDir() {
			existing = append(existing, root)
		}
	}
	return existing
}

func depthFromRoot(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return 99
	}
	if rel == "." {
		return 0
	}
	return strings.Count(rel, string(os.PathSeparator))
}

func hostHintFromControlPath(path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".sock")
	base = strings.TrimPrefix(base, "cm-")
	if user, host, ok := strings.Cut(base, "@"); ok {
		_ = user
		host = strings.Split(host, "-")[0]
		host = strings.Split(host, ":")[0]
		return host
	}
	return sanitizeCLISSHImportedName(base)
}

func looksLikeSSHControlSocket(path string, mode os.FileMode) bool {
	path = filepath.Clean(path)
	if auth := strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK")); auth != "" && filepath.Clean(auth) == path {
		return false
	}
	base := strings.ToLower(filepath.Base(path))
	switch {
	case strings.Contains(base, "agent"),
		strings.HasPrefix(base, "id_"),
		strings.HasSuffix(base, ".pub"),
		strings.HasSuffix(base, ".pem"),
		strings.HasSuffix(base, ".ppk"),
		base == "config",
		base == "rc",
		base == "environment",
		strings.Contains(base, "known_hosts"),
		base == "authorized_keys":
		return false
	}
	if mode&os.ModeSocket != 0 {
		return true
	}
	if !mode.IsRegular() {
		return false
	}
	dir := strings.ToLower(filepath.Base(filepath.Dir(path)))
	return strings.Contains(base, "sock") ||
		strings.Contains(base, "cm-") ||
		strings.Contains(base, "master") ||
		strings.Contains(base, "control") ||
		dir == "sockets" || dir == "cm" || dir == "masters" || dir == "controlmasters" ||
		strings.HasPrefix(dir, "ssh-")
}

func isFlClashManagedSSHPath(path string) bool {
	path = filepath.Clean(path)
	directory, err := ensureCLISSHRuntimeDirectory()
	if err != nil {
		return false
	}
	prefix := filepath.Clean(directory) + string(os.PathSeparator)
	return strings.HasPrefix(path, prefix)
}

func isFlClashManagedSOCKSPort(port int) bool {
	if port < 1 {
		return false
	}
	state, active, err := activeCLIPersistentSSHTunnel()
	if err != nil || !active {
		return false
	}
	return port == state.Port || port == cliSSHUpstreamPort(state)
}

func parseSSHDynamicPort(value string) int {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if strings.HasPrefix(value, "[") {
		_, port, ok := strings.Cut(value, "]:")
		if ok {
			return parseSSHPortValue(port, 0)
		}
	}
	if _, port, ok := strings.Cut(value, ":"); ok {
		return parseSSHPortValue(port, 0)
	}
	return parseSSHPortValue(value, 0)
}

func parseSSHPortValue(value string, fallback int) int {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || port < 0 || port > 65535 {
		return fallback
	}
	return port
}

func splitSSHDestination(value string) (string, string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	if user, host, ok := strings.Cut(value, "@"); ok {
		return user, strings.Trim(host, "[]")
	}
	return "", strings.Trim(value, "[]")
}

func isOpenSSHClientName(path string) bool {
	base := strings.ToLower(filepath.Base(path))
	return base == "ssh" || base == "ssh.bin"
}

func captureSourceFromCommand(pid int, parsed cliSSHParsedCommand) string {
	lower := strings.ToLower(parsed.ConfigFile)
	switch {
	case strings.Contains(lower, "cursor"):
		return "cursor"
	case strings.Contains(lower, "windsurf"):
		return "windsurf"
	case strings.Contains(lower, "jetbrains") || strings.Contains(lower, "gateway"):
		return "jetbrains"
	case strings.Contains(lower, "vscode") || strings.Contains(lower, "code-oss") ||
		strings.Contains(lower, "codium") || strings.Contains(lower, "code-server"):
		return "vscode"
	}
	return captureSourceFromProcess(pid)
}

func captureSourceFromProcess(pid int) string {
	for _, comm := range []string{readProcessParentComm(pid), readProcessComm(pid)} {
		switch strings.ToLower(strings.TrimSpace(comm)) {
		case "code", "code-oss", "codium", "code-server":
			return "vscode"
		case "cursor", "cursor-bin":
			return "cursor"
		case "windsurf":
			return "windsurf"
		case "idea", "idea64", "goland", "pycharm", "webstorm", "phpstorm", "clion",
			"jetbrains_client", "gateway", "gateway64":
			return "jetbrains"
		}
	}
	return "ssh"
}

func captureCLISSHNameHint(candidate cliSSHCaptureCandidate) string {
	host := sanitizeCLISSHImportedName(candidate.Host)
	switch candidate.Source {
	case "vscode":
		return firstNonEmpty(sanitizeCLISSHImportedName("vscode-"+host), "vscode")
	case "cursor":
		return firstNonEmpty(sanitizeCLISSHImportedName("cursor-"+host), "cursor")
	case "windsurf":
		return firstNonEmpty(sanitizeCLISSHImportedName("windsurf-"+host), "windsurf")
	case "jetbrains":
		return firstNonEmpty(sanitizeCLISSHImportedName("jetbrains-"+host), "jetbrains")
	default:
		return firstNonEmpty(host, "ssh")
	}
}

func formatCLICaptureCandidate(candidate cliSSHCaptureCandidate) string {
	dest := formatCLISSHDestination(candidate.Username, candidate.Host)
	if candidate.Kind == cliSSHCaptureSOCKSKind {
		return fmt.Sprintf(
			"%-16s %-28s SOCKS 127.0.0.1:%d · %s",
			candidate.Name,
			dest,
			candidate.SocksPort,
			candidate.Source,
		)
	}
	return fmt.Sprintf(
		"%-16s %-28s ControlMaster %s",
		candidate.Name,
		dest,
		candidate.ControlPath,
	)
}

func formatCLICaptureEmptyHint() string {
	if captureHasRemoteSSHProcess() {
		return "VS Code/Cursor SSH is running but has no ControlMaster or -D SOCKS. Add ControlMaster auto and ControlPath ~/.ssh/cm-%C to ~/.ssh/config, reconnect, then capture again."
	}
	return "No live ControlMaster or ssh -D SOCKS matches. Ordinary ssh without multiplexing cannot be captured."
}

func captureHasRemoteSSHProcess() bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	self := os.Getuid()
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil {
			continue
		}
		if processUID(pid) != self {
			continue
		}
		args := readProcessCommandLine(pid)
		if len(args) == 0 || !isOpenSSHClientName(args[0]) {
			continue
		}
		source := captureSourceFromCommand(pid, parseSSHCommandLine(args))
		if source == "vscode" || source == "cursor" || source == "windsurf" {
			return true
		}
	}
	return false
}

func ensureCLISSHProfileForCapture(candidate cliSSHCaptureCandidate) (cliSSHProfile, error) {
	config, err := loadCLISSHConfig()
	if err != nil {
		return cliSSHProfile{}, err
	}
	for _, profile := range config.Profiles {
		profile = normalizeCLISSHProfile(profile)
		if sshCaptureProfileMatches(profile, candidate) {
			return profile, nil
		}
	}
	name := uniqueCLISSHProfileName(firstNonEmpty(candidate.Name, captureCLISSHNameHint(candidate)))
	parsed := enrichSSHParsedCommand(cliSSHParsedCommand{
		User: candidate.Username,
		Host: candidate.Host,
		Port: candidate.Port,
		Jump: candidate.Jump,
	})
	profile := normalizeCLISSHProfile(cliSSHProfile{
		Name:     name,
		Username: firstNonEmpty(parsed.User, candidate.Username, currentUserName()),
		Host:     candidate.Host,
		Port:     normalizeCLISSHCapturePort(parsed.Port),
		Jump:     firstNonEmpty(parsed.Jump, candidate.Jump),
	})
	if err := addCLISSHProfile(profile); err != nil {
		return cliSSHProfile{}, err
	}
	return profile, nil
}

func sshCaptureProfileMatches(profile cliSSHProfile, candidate cliSSHCaptureCandidate) bool {
	if candidate.Username != "" && profile.Username != "" &&
		!strings.EqualFold(profile.Username, candidate.Username) {
		return false
	}
	if profile.Port != normalizeCLISSHCapturePort(candidate.Port) {
		return false
	}
	if strings.EqualFold(profile.Host, candidate.Host) {
		return true
	}
	for _, option := range profile.Options {
		key, value, ok := strings.Cut(option, "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "HostName") &&
			strings.EqualFold(strings.TrimSpace(value), candidate.Host) {
			return true
		}
	}
	return false
}

func uniqueCLISSHProfileName(base string) string {
	base = sanitizeCLISSHImportedName(base)
	if base == "" {
		base = "ssh"
	}
	config, err := loadCLISSHConfig()
	if err != nil {
		return base
	}
	if _, found := findCLISSHProfile(config.Profiles, base); !found {
		return base
	}
	for index := 2; index < 100; index++ {
		name := fmt.Sprintf("%s-%d", base, index)
		if _, found := findCLISSHProfile(config.Profiles, name); !found {
			return name
		}
	}
	return fmt.Sprintf("%s-%d", base, os.Getpid())
}

func captureCLISSHCandidate(candidate cliSSHCaptureCandidate) (cliSSHTunnelState, bool, error) {
	profile, err := ensureCLISSHProfileForCapture(candidate)
	if err != nil {
		return cliSSHTunnelState{}, false, err
	}
	switch candidate.Kind {
	case cliSSHCaptureSOCKSKind:
		state, already, attachErr := attachCLISSHSocksProfile(profile, candidate.SocksPort)
		return state, already, attachErr
	default:
		if candidate.ControlPath != "" {
			return attachCLISSHProfileAtPath(profile.Name, candidate.ControlPath)
		}
		return attachCLISSHProfile(profile.Name)
	}
}

func normalizeCLISSHCapturePort(port int) int {
	if port <= 0 {
		return 22
	}
	return port
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func currentUserName() string {
	if user := strings.TrimSpace(os.Getenv("USER")); user != "" {
		return user
	}
	if user := strings.TrimSpace(os.Getenv("LOGNAME")); user != "" {
		return user
	}
	return ""
}

func uniquePositivePorts(ports []int) []int {
	seen := map[int]bool{}
	result := make([]int, 0, len(ports))
	for _, port := range ports {
		if port < 1 || port > 65535 || seen[port] {
			continue
		}
		seen[port] = true
		result = append(result, port)
	}
	return result
}

func processUID(pid int) int {
	info, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	if err != nil {
		return -1
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1
	}
	return int(stat.Uid)
}

func readProcessCommandLineImpl(pid int) []string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	if err != nil || len(data) == 0 {
		return nil
	}
	parts := strings.Split(strings.TrimRight(string(data), "\x00"), "\x00")
	args := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			args = append(args, part)
		}
	}
	return args
}

func readProcessCommImpl(pid int) string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func readProcessParentCommImpl(pid int) string {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "status"))
	if err != nil {
		return ""
	}
	ppid := 0
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "PPid:") {
			ppid, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:")))
			break
		}
	}
	if ppid <= 0 {
		return ""
	}
	return readProcessComm(ppid)
}

func listProcessLoopbackListenPortsImpl(pid int) []int {
	inodes := processSocketInodes(pid)
	if len(inodes) == 0 {
		return nil
	}
	ports := append(
		listenPortsFromProcNet("/proc/net/tcp", inodes, 4),
		listenPortsFromProcNet("/proc/net/tcp6", inodes, 6)...,
	)
	return uniquePositivePorts(ports)
}

func processSocketInodes(pid int) map[uint64]bool {
	entries, err := os.ReadDir(filepath.Join("/proc", strconv.Itoa(pid), "fd"))
	if err != nil {
		return nil
	}
	inodes := map[uint64]bool{}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "fd", entry.Name()))
		if err != nil || !strings.HasPrefix(target, "socket:[") {
			continue
		}
		raw := strings.TrimSuffix(strings.TrimPrefix(target, "socket:["), "]")
		inode, convErr := strconv.ParseUint(raw, 10, 64)
		if convErr != nil {
			continue
		}
		inodes[inode] = true
	}
	return inodes
}

func listenPortsFromProcNet(path string, inodes map[uint64]bool, family int) []int {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return nil
	}
	ports := make([]int, 0)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[3] != "0A" {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil || !inodes[inode] {
			continue
		}
		host, port, ok := parseProcNetAddress(fields[1], family)
		if !ok || port < 1 {
			continue
		}
		if !procNetListenAddressOK(host) {
			continue
		}
		ports = append(ports, port)
	}
	return ports
}

func parseProcNetAddress(value string, family int) (string, int, bool) {
	ipText, portText, ok := strings.Cut(value, ":")
	if !ok {
		return "", 0, false
	}
	portValue, err := strconv.ParseUint(portText, 16, 16)
	if err != nil {
		return "", 0, false
	}
	decoded, err := hex.DecodeString(ipText)
	if err != nil {
		return "", 0, false
	}
	if family == 4 && len(decoded) == 4 {
		return net.IPv4(decoded[3], decoded[2], decoded[1], decoded[0]).String(), int(portValue), true
	}
	if family == 6 && len(decoded) == 16 {
		ip := make(net.IP, 16)
		for index := 0; index < 16; index += 4 {
			ip[index] = decoded[index+3]
			ip[index+1] = decoded[index+2]
			ip[index+2] = decoded[index+1]
			ip[index+3] = decoded[index]
		}
		return ip.String(), int(portValue), true
	}
	return "", 0, false
}

func procNetListenAddressOK(host string) bool {
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsUnspecified()
}
