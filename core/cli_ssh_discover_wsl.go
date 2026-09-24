//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	cachedLoopbackListenOwners   map[int]string
	cachedLoopbackListenOwnersMu sync.Mutex
)

func resetCachedLoopbackListenOwners() {
	cachedLoopbackListenOwnersMu.Lock()
	cachedLoopbackListenOwners = nil
	cachedLoopbackListenOwnersMu.Unlock()
}

type windowsSSHSnapshot struct {
	ConfigPath   string
	ProcessCount int
	HadSOCKS     bool
	Processes    []windowsSSHProcess
}

type windowsSSHProcess struct {
	PID         int
	CommandLine string
	ListenPorts []int
}

type windowsSSHSnapshotJSON struct {
	UserProfile string          `json:"UserProfile"`
	Config      string          `json:"Config"`
	Processes   json.RawMessage `json:"Processes"`
}

type windowsSSHProcessJSON struct {
	Pid         int    `json:"Pid"`
	CommandLine string `json:"CommandLine"`
	ListenPorts any    `json:"ListenPorts"`
}

var lastWindowsSSHSnapshot windowsSSHSnapshot

func runningOnWSLImpl() bool {
	if strings.TrimSpace(os.Getenv("WSL_DISTRO_NAME")) != "" {
		return true
	}
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(data)), "microsoft")
}

func discoverWindowsSSHSocksCandidates() []cliSSHCaptureCandidate {
	lastWindowsSSHSnapshot = windowsSSHSnapshot{}
	if !cliRunningOnWSL() {
		return nil
	}
	snapshot, err := listWindowsSSHSnapshot()
	if err != nil {
		return nil
	}
	lastWindowsSSHSnapshot = snapshot
	candidates := make([]cliSSHCaptureCandidate, 0)
	for _, proc := range snapshot.Processes {
		args := splitWindowsCommandLine(proc.CommandLine)
		if len(args) == 0 {
			continue
		}
		parsed := parseSSHCommandLine(args)
		if parsed.ConfigFile == "" && snapshot.ConfigPath != "" {
			parsed.ConfigFile = snapshot.ConfigPath
		}
		parsed = enrichSSHParsedCommand(parsed)
		if parsed.ControlOp || parsed.Host == "" || strings.HasPrefix(parsed.Host, "-") {
			continue
		}
		ports := append([]int(nil), parsed.DynamicPorts...)
		needListen := false
		for _, port := range parsed.DynamicPorts {
			if port <= 0 {
				needListen = true
				break
			}
		}
		if needListen || len(parsed.DynamicPorts) == 0 {
			ports = append(ports, proc.ListenPorts...)
		}
		if len(parsed.DynamicPorts) == 0 && len(proc.ListenPorts) == 0 {
			continue
		}
		source := "vscode"
		if captureSourceFromCommand(0, parsed) != "ssh" {
			source = captureSourceFromCommand(0, parsed)
		}
		for _, port := range uniquePositivePorts(ports) {
			if isFlClashManagedSOCKSPort(port) || isProxyEngineListenPort(port) ||
				probeCLISSHSOCKS(port, 200*time.Millisecond) != nil {
				continue
			}
			lastWindowsSSHSnapshot.HadSOCKS = true
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
	}
	lastWindowsSSHSnapshot.ProcessCount = len(snapshot.Processes)
	return candidates
}

func listWindowsSSHSnapshotImpl() (windowsSSHSnapshot, error) {
	powershell := windowsPowerShellPath()
	if powershell == "" {
		return windowsSSHSnapshot{}, os.ErrNotExist
	}
	script := strings.Join([]string{
		"$ErrorActionPreference = 'SilentlyContinue'",
		"[Console]::OutputEncoding = [Text.UTF8Encoding]::new()",
		"$profilePath = Join-Path $env:USERPROFILE '.ssh\\config'",
		"$procs = @(Get-CimInstance Win32_Process -Filter \"Name='ssh.exe'\" | ForEach-Object {",
		"  [pscustomobject]@{ Pid = $_.ProcessId; CommandLine = $_.CommandLine; ListenPorts = @() }",
		"})",
		"[pscustomobject]@{ UserProfile = $env:USERPROFILE; Config = $profilePath; Processes = @($procs) } | ConvertTo-Json -Compress -Depth 4",
	}, "; ")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, powershell, "-NoProfile", "-NonInteractive", "-Command", script)
	command.WaitDelay = time.Second
	prepareCLISSHNonInteractiveCommand(command)
	output, err := command.Output()
	if err != nil {
		return windowsSSHSnapshot{}, err
	}
	output = bytes.TrimPrefix(bytes.TrimSpace(output), []byte{0xEF, 0xBB, 0xBF})
	if len(output) == 0 {
		return windowsSSHSnapshot{}, nil
	}
	var decoded windowsSSHSnapshotJSON
	if err := json.Unmarshal(output, &decoded); err != nil {
		return windowsSSHSnapshot{}, err
	}
	config := decoded.Config
	if config == "" && decoded.UserProfile != "" {
		config = strings.TrimRight(decoded.UserProfile, `\/`) + `\.ssh\config`
	}
	decodedProcesses := decodeWindowsSSHProcesses(decoded.Processes)
	snapshot := windowsSSHSnapshot{
		ConfigPath: windowsPathToWSL(config),
		Processes:  make([]windowsSSHProcess, 0, len(decodedProcesses)),
	}
	if snapshot.ConfigPath != "" {
		if info, err := os.Lstat(snapshot.ConfigPath); err != nil || info.IsDir() {
			snapshot.ConfigPath = ""
		}
	}
	for _, proc := range decodedProcesses {
		snapshot.Processes = append(snapshot.Processes, windowsSSHProcess{
			PID:         proc.Pid,
			CommandLine: strings.TrimSpace(proc.CommandLine),
			ListenPorts: jsonIntList(proc.ListenPorts),
		})
	}
	snapshot.ProcessCount = len(snapshot.Processes)
	return snapshot, nil
}

func windowsPowerShellPath() string {
	for _, path := range []string{
		"/mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe",
		"/mnt/c/WINDOWS/System32/WindowsPowerShell/v1.0/powershell.exe",
	} {
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() {
			return path
		}
	}
	if path, err := exec.LookPath("powershell.exe"); err == nil {
		return path
	}
	return ""
}

func windowsPathToWSL(path string) string {
	path = strings.TrimSpace(path)
	path = strings.Trim(path, `"'`)
	if path == "" {
		return ""
	}
	path = strings.ReplaceAll(path, "/", "\\")
	if len(path) >= 2 && path[1] == ':' {
		drive := strings.ToLower(string(path[0]))
		rest := strings.ReplaceAll(path[2:], "\\", "/")
		return "/mnt/" + drive + rest
	}
	return strings.ReplaceAll(path, "\\", "/")
}

func splitWindowsCommandLine(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	args := make([]string, 0, 8)
	var current strings.Builder
	inQuote := false
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if ch == '"' {
			inQuote = !inQuote
			continue
		}
		if (ch == ' ' || ch == '\t') && !inQuote {
			if current.Len() > 0 {
				args = append(args, current.String())
				current.Reset()
			}
			continue
		}
		current.WriteByte(ch)
	}
	if current.Len() > 0 {
		args = append(args, current.String())
	}
	return args
}

func decodeWindowsSSHProcesses(raw json.RawMessage) []windowsSSHProcessJSON {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var many []windowsSSHProcessJSON
	if err := json.Unmarshal(raw, &many); err == nil {
		return many
	}
	var one windowsSSHProcessJSON
	if err := json.Unmarshal(raw, &one); err == nil {
		return []windowsSSHProcessJSON{one}
	}
	return nil
}

func jsonIntList(value any) []int {
	switch typed := value.(type) {
	case nil:
		return nil
	case float64:
		return []int{int(typed)}
	case int:
		return []int{typed}
	case []any:
		ports := make([]int, 0, len(typed))
		for _, item := range typed {
			switch n := item.(type) {
			case float64:
				ports = append(ports, int(n))
			case int:
				ports = append(ports, n)
			case json.Number:
				parsed, err := n.Int64()
				if err == nil {
					ports = append(ports, int(parsed))
				}
			}
		}
		return ports
	default:
		return nil
	}
}

func isProxyEngineComm(comm string) bool {
	switch strings.ToLower(strings.TrimSpace(comm)) {
	case "mihomo", "clash", "clash-meta", "clash-verge", "flclash", "flc",
		"sing-box", "singbox", "xray", "v2ray", "hysteria", "hysteria2":
		return true
	default:
		return false
	}
}

func isProxyEngineListenPort(port int) bool {
	if port < 1 {
		return false
	}
	cachedLoopbackListenOwnersMu.Lock()
	defer cachedLoopbackListenOwnersMu.Unlock()
	if cachedLoopbackListenOwners == nil {
		cachedLoopbackListenOwners = linuxLoopbackListenOwners()
		if cachedLoopbackListenOwners == nil {
			cachedLoopbackListenOwners = map[int]string{}
		}
	}
	return isProxyEngineComm(cachedLoopbackListenOwners[port])
}

func linuxLoopbackListenOwnersImpl() map[int]string {
	inodePorts := map[uint64][]int{}
	for _, item := range []struct {
		path   string
		family int
	}{
		{"/proc/net/tcp", 4},
		{"/proc/net/tcp6", 6},
	} {
		for _, port := range listenPortsWithInodes(item.path, item.family) {
			inodePorts[port.inode] = append(inodePorts[port.inode], port.port)
		}
	}
	if len(inodePorts) == 0 {
		return nil
	}
	owners := map[int]string{}
	self := os.Getuid()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return owners
	}
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 0 || processUID(pid) != self {
			continue
		}
		comm := readProcessComm(pid)
		for inode := range processSocketInodes(pid) {
			for _, port := range inodePorts[inode] {
				if _, exists := owners[port]; !exists {
					owners[port] = comm
				}
			}
		}
	}
	return owners
}

type procListenPort struct {
	port  int
	inode uint64
}

func listenPortsWithInodes(path string, family int) []procListenPort {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		return nil
	}
	ports := make([]procListenPort, 0)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 10 || fields[3] != "0A" {
			continue
		}
		inode, err := strconv.ParseUint(fields[9], 10, 64)
		if err != nil {
			continue
		}
		host, port, ok := parseProcNetAddress(fields[1], family)
		if !ok || port < 1 || !procNetListenAddressOK(host) {
			continue
		}
		ports = append(ports, procListenPort{port: port, inode: inode})
	}
	return ports
}

func captureHasRemoteWSLProcess() bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	self := os.Getuid()
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 0 || processUID(pid) != self {
			continue
		}
		comm := strings.ToLower(readProcessComm(pid))
		if comm == "code-server" {
			return true
		}
		args := readProcessCommandLine(pid)
		joined := strings.ToLower(strings.Join(args, " "))
		if strings.Contains(joined, "vscode-server") || strings.Contains(joined, "wslserver.sh") ||
			strings.Contains(joined, "remote-wsl") {
			return true
		}
	}
	return false
}
