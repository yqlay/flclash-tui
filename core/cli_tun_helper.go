//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	tuiTunHelperSocket = "/run/flclash/tun-helper.sock"
	tuiTunPolkitAction = "org.flclash.tun.system"
)

var tuiTunHelperSocketOverride string

type tuiTunHelperRequest struct {
	Action string `json:"action"`
	Scope  string `json:"scope,omitempty"`
}

type tuiTunHelperResponse struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Scope    string `json:"scope,omitempty"`
	OwnerUID uint32 `json:"owner_uid,omitempty"`
	OwnerPID int    `json:"owner_pid,omitempty"`
	Device   string `json:"device,omitempty"`
}

type tuiTunLease struct {
	connection net.Conn
	scope      string
	file       *os.File
}

func tunHelperSocketPath() string {
	if tuiTunHelperSocketOverride != "" {
		return tuiTunHelperSocketOverride
	}
	return tuiTunHelperSocket
}

func acquireTUITunLease(scope string) (*tuiTunLease, tuiTunHelperResponse, error) {
	connection, err := net.Dial("unix", tunHelperSocketPath())
	if err != nil {
		return nil, tuiTunHelperResponse{}, fmt.Errorf(
			"TUN helper is unavailable: %w; install the Debian package and start flclash-tun-helper.service",
			err,
		)
	}
	request := tuiTunHelperRequest{Action: "acquire", Scope: scope}
	if err := json.NewEncoder(connection).Encode(request); err != nil {
		_ = connection.Close()
		return nil, tuiTunHelperResponse{}, err
	}
	var response tuiTunHelperResponse
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&response); err != nil {
		_ = connection.Close()
		return nil, response, err
	}
	if !response.OK {
		_ = connection.Close()
		if response.Error == "" {
			response.Error = "TUN lease was rejected"
		}
		return nil, response, errors.New(response.Error)
	}
	if _, err := connection.Write([]byte{1}); err != nil {
		_ = connection.Close()
		return nil, response, err
	}
	unixConnection := connection.(*net.UnixConn)
	data := make([]byte, 1)
	control := make([]byte, unix.CmsgSpace(4))
	_, controlLength, _, _, err := unixConnection.ReadMsgUnix(data, control)
	if err != nil {
		_ = connection.Close()
		return nil, response, err
	}
	messages, err := unix.ParseSocketControlMessage(control[:controlLength])
	if err != nil || len(messages) != 1 {
		_ = connection.Close()
		return nil, response, errors.New("TUN helper returned no file descriptor")
	}
	fds, err := unix.ParseUnixRights(&messages[0])
	if err != nil || len(fds) != 1 {
		_ = connection.Close()
		return nil, response, errors.New("TUN helper returned an invalid file descriptor")
	}
	file := os.NewFile(uintptr(fds[0]), response.Device)
	return &tuiTunLease{connection: connection, scope: scope, file: file}, response, nil
}

func (l *tuiTunLease) duplicateFD() (int, error) {
	if l == nil || l.file == nil {
		return 0, errors.New("TUN lease has no file descriptor")
	}
	fd, err := unix.Dup(int(l.file.Fd()))
	if err != nil {
		return 0, err
	}
	return fd, nil
}

func (l *tuiTunLease) release() {
	if l != nil && l.connection != nil {
		_ = l.connection.Close()
	}
	if l != nil && l.file != nil {
		_ = l.file.Close()
	}
}

type tuiTunHelperLeaseOwner struct {
	uid   uint32
	pid   int
	scope string
}

type tuiTunHelperState struct {
	mu     sync.Mutex
	owners map[uint32]tuiTunHelperLeaseOwner
	system *tuiTunHelperLeaseOwner
}

func runTUITunHelper() error {
	if os.Geteuid() != 0 {
		return errors.New("tun-helper must run as root")
	}
	path := tunHelperSocketPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return fmt.Errorf("refusing to replace non-socket %s", path)
		}
		_ = os.Remove(path)
	} else if !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return err
	}
	defer listener.Close()
	defer os.Remove(path)
	if err := os.Chmod(path, 0o666); err != nil {
		return err
	}
	state := &tuiTunHelperState{owners: map[uint32]tuiTunHelperLeaseOwner{}}
	for {
		connection, err := listener.Accept()
		if err != nil {
			return err
		}
		go state.serve(connection)
	}
}

func (s *tuiTunHelperState) serve(connection net.Conn) {
	defer connection.Close()
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		return
	}
	peer, err := tuiTunPeerCredentials(unixConnection)
	if err != nil {
		_ = json.NewEncoder(connection).Encode(tuiTunHelperResponse{Error: err.Error()})
		return
	}
	var request tuiTunHelperRequest
	if err := json.NewDecoder(bufio.NewReader(connection)).Decode(&request); err != nil {
		return
	}
	if request.Action != "acquire" {
		_ = json.NewEncoder(connection).Encode(tuiTunHelperResponse{Error: "unsupported helper action"})
		return
	}
	scope, err := normalizeTUITunScope(request.Scope)
	if err != nil {
		_ = json.NewEncoder(connection).Encode(tuiTunHelperResponse{Error: err.Error()})
		return
	}
	owner := tuiTunHelperLeaseOwner{uid: peer.Uid, pid: int(peer.Pid), scope: scope}
	if scope == tuiTunScopeSystem {
		if err := authorizeTUISystemTun(owner); err != nil {
			_ = json.NewEncoder(connection).Encode(tuiTunHelperResponse{Error: err.Error()})
			return
		}
	}
	response, acquired := s.acquire(owner)
	if !acquired {
		_ = json.NewEncoder(connection).Encode(response)
		return
	}
	device, tunFile, cleanup, err := prepareTUITunKernel(owner)
	if err != nil {
		s.release(owner)
		_ = json.NewEncoder(connection).Encode(tuiTunHelperResponse{Error: err.Error()})
		return
	}
	defer tunFile.Close()
	defer cleanup()
	response.Device = device
	_ = json.NewEncoder(connection).Encode(response)
	ack := make([]byte, 1)
	if _, err := connection.Read(ack); err != nil {
		s.release(owner)
		return
	}
	rights := unix.UnixRights(int(tunFile.Fd()))
	if _, _, err := unixConnection.WriteMsgUnix([]byte{1}, rights, nil); err != nil {
		s.release(owner)
		return
	}
	_, _ = bufio.NewReader(connection).ReadByte()
	s.release(owner)
}

type tuiTunIfreq struct {
	Name  [unix.IFNAMSIZ]byte
	Flags uint16
	pad   [22]byte
}

func prepareTUITunKernel(owner tuiTunHelperLeaseOwner) (string, *os.File, func(), error) {
	suffix := strconv.FormatUint(uint64(owner.uid), 36)
	device := "flc-u" + suffix
	if owner.scope == tuiTunScopeSystem {
		device = "flc-system"
	}
	if len(device) >= unix.IFNAMSIZ {
		device = device[:unix.IFNAMSIZ-1]
	}
	file, err := os.OpenFile("/dev/net/tun", os.O_RDWR, 0)
	if err != nil {
		return "", nil, func() {}, fmt.Errorf("open /dev/net/tun: %w", err)
	}
	request := tuiTunIfreq{Flags: unix.IFF_TUN | unix.IFF_NO_PI}
	copy(request.Name[:], device)
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, file.Fd(), uintptr(unix.TUNSETIFF), uintptr(unsafe.Pointer(&request)))
	if errno != 0 {
		_ = file.Close()
		return "", nil, func() {}, fmt.Errorf("create TUN device: %w", errno)
	}
	device = strings.TrimRight(string(request.Name[:]), "\x00")
	table := 10000 + int(owner.uid%40000)
	priority := 20000 + int(owner.uid%30000)
	bypassPriority := 9000 + int(owner.uid%5000)
	mark := 0xf100 + int(owner.uid%0x0eff)
	if owner.scope == tuiTunScopeSystem {
		table = 9999
		priority = 15000
	}
	octet := 1 + int(owner.uid%250)
	address := fmt.Sprintf("198.19.%d.1/30", octet)
	cgroupPath, originalCgroup, err := moveTUITunBackendToCgroup(owner.pid)
	if err != nil {
		_ = file.Close()
		return "", nil, func() {}, err
	}
	cgroupMatch := strings.TrimPrefix(cgroupPath, "/sys/fs/cgroup/")
	iptablesRule := []string{"-w", "-t", "mangle", "-A", "OUTPUT", "-m", "cgroup", "--path", cgroupMatch, "-j", "MARK", "--set-mark", strconv.Itoa(mark)}
	if output, commandErr := exec.Command("iptables", iptablesRule...).CombinedOutput(); commandErr != nil {
		restoreTUITunBackendCgroup(owner.pid, originalCgroup, cgroupPath)
		_ = file.Close()
		return "", nil, func() {}, fmt.Errorf("install Core bypass mark: %s", strings.TrimSpace(string(output)))
	}
	commands := [][]string{
		{"ip", "link", "set", "dev", device, "up"},
		{"ip", "address", "replace", address, "dev", device},
		{"ip", "route", "replace", "default", "dev", device, "table", strconv.Itoa(table)},
		{"ip", "rule", "add", "fwmark", strconv.Itoa(mark), "priority", strconv.Itoa(bypassPriority), "table", "main"},
	}
	if owner.scope == tuiTunScopeUser {
		commands = append(commands, []string{"ip", "rule", "add", "uidrange", fmt.Sprintf("%d-%d", owner.uid, owner.uid), "priority", strconv.Itoa(priority), "table", strconv.Itoa(table)})
	} else {
		commands = append(commands, []string{"ip", "rule", "add", "priority", strconv.Itoa(priority), "table", strconv.Itoa(table)})
	}
	for _, command := range commands {
		if output, commandErr := exec.Command(command[0], command[1:]...).CombinedOutput(); commandErr != nil {
			_ = exec.Command("ip", "rule", "delete", "priority", strconv.Itoa(priority)).Run()
			_ = exec.Command("ip", "rule", "delete", "priority", strconv.Itoa(bypassPriority)).Run()
			_ = exec.Command("ip", "route", "flush", "table", strconv.Itoa(table)).Run()
			_ = exec.Command("ip", "link", "delete", "dev", device).Run()
			deleteRule := append([]string(nil), iptablesRule...)
			deleteRule[3] = "-D"
			_ = exec.Command("iptables", deleteRule...).Run()
			restoreTUITunBackendCgroup(owner.pid, originalCgroup, cgroupPath)
			_ = file.Close()
			return "", nil, func() {}, fmt.Errorf("%s: %s", strings.Join(command, " "), strings.TrimSpace(string(output)))
		}
	}
	cleanup := func() {
		_ = exec.Command("ip", "rule", "delete", "priority", strconv.Itoa(priority)).Run()
		_ = exec.Command("ip", "rule", "delete", "priority", strconv.Itoa(bypassPriority)).Run()
		_ = exec.Command("ip", "route", "flush", "table", strconv.Itoa(table)).Run()
		_ = exec.Command("ip", "link", "delete", "dev", device).Run()
		deleteRule := append([]string(nil), iptablesRule...)
		deleteRule[3] = "-D"
		_ = exec.Command("iptables", deleteRule...).Run()
		restoreTUITunBackendCgroup(owner.pid, originalCgroup, cgroupPath)
	}
	return device, file, cleanup, nil
}

func moveTUITunBackendToCgroup(pid int) (string, string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		return "", "", err
	}
	original := ""
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "0::") {
			original = strings.TrimPrefix(line, "0::")
			break
		}
	}
	if original == "" {
		return "", "", errors.New("cgroup v2 is required for TUN loop prevention")
	}
	root := "/sys/fs/cgroup"
	group := filepath.Join(root, "flclash-tun", "pid-"+strconv.Itoa(pid))
	if err := os.MkdirAll(group, 0o755); err != nil {
		return "", "", fmt.Errorf("create TUN cgroup: %w", err)
	}
	if err := os.WriteFile(filepath.Join(group, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0o600); err != nil {
		_ = os.Remove(group)
		return "", "", fmt.Errorf("move Backend into TUN cgroup: %w", err)
	}
	relativeOriginal := strings.TrimPrefix(filepath.Clean("/"+original), "/")
	return group, filepath.Join(root, relativeOriginal), nil
}

func restoreTUITunBackendCgroup(pid int, original, temporary string) {
	if original != "" {
		_ = os.WriteFile(filepath.Join(original, "cgroup.procs"), []byte(strconv.Itoa(pid)), 0o600)
	}
	_ = os.Remove(temporary)
}

func (s *tuiTunHelperState) acquire(owner tuiTunHelperLeaseOwner) (tuiTunHelperResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner.scope == tuiTunScopeSystem {
		if s.system != nil {
			return occupiedTUITunResponse(*s.system), false
		}
		for _, active := range s.owners {
			if active.uid != owner.uid || active.pid != owner.pid {
				return occupiedTUITunResponse(active), false
			}
		}
		delete(s.owners, owner.uid)
		s.system = &owner
	} else {
		if s.system != nil {
			if s.system.uid != owner.uid || s.system.pid != owner.pid {
				return occupiedTUITunResponse(*s.system), false
			}
			s.system = nil
		}
		if active, ok := s.owners[owner.uid]; ok && active.pid != owner.pid {
			return occupiedTUITunResponse(active), false
		}
		s.owners[owner.uid] = owner
	}
	return tuiTunHelperResponse{OK: true, Scope: owner.scope, OwnerUID: owner.uid, OwnerPID: owner.pid}, true
}

func (s *tuiTunHelperState) release(owner tuiTunHelperLeaseOwner) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner.scope == tuiTunScopeSystem {
		if s.system != nil && s.system.uid == owner.uid && s.system.pid == owner.pid {
			s.system = nil
		}
		return
	}
	if active, ok := s.owners[owner.uid]; ok && active.pid == owner.pid {
		delete(s.owners, owner.uid)
	}
}

func occupiedTUITunResponse(owner tuiTunHelperLeaseOwner) tuiTunHelperResponse {
	return tuiTunHelperResponse{
		Error:    fmt.Sprintf("TUN is held by %s lease (UID %d, PID %d)", owner.scope, owner.uid, owner.pid),
		Scope:    owner.scope,
		OwnerUID: owner.uid,
		OwnerPID: owner.pid,
	}
}

func tuiTunPeerCredentials(connection *net.UnixConn) (*syscall.Ucred, error) {
	raw, err := connection.SyscallConn()
	if err != nil {
		return nil, err
	}
	var peer *syscall.Ucred
	var socketErr error
	if err := raw.Control(func(fd uintptr) {
		peer, socketErr = syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
	}); err != nil {
		return nil, err
	}
	return peer, socketErr
}

func authorizeTUISystemTun(owner tuiTunHelperLeaseOwner) error {
	start, err := linuxProcessStartTime(owner.pid)
	if err != nil {
		return err
	}
	process := fmt.Sprintf("%d,%s,%d", owner.pid, start, owner.uid)
	command := exec.Command(
		"pkcheck",
		"--action-id", tuiTunPolkitAction,
		"--process", process,
		"--allow-user-interaction",
	)
	if output, err := command.CombinedOutput(); err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("system TUN authorization denied: %s", message)
	}
	return nil
}

func linuxProcessStartTime(pid int) (string, error) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", err
	}
	closeIndex := strings.LastIndexByte(string(data), ')')
	if closeIndex < 0 {
		return "", errors.New("invalid process stat")
	}
	fields := strings.Fields(string(data[closeIndex+1:]))
	if len(fields) <= 19 {
		return "", errors.New("process stat has no start time")
	}
	return fields[19], nil
}
