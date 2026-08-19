//go:build linux && !cgo && cli

package main

import (
	"fmt"
	"net"
	"strconv"
	"time"
)

func chooseTUIProxyPort(preferred int) (int, error) {
	if preferred > 0 && ensureTUIProxyPortFree(preferred) == nil {
		return preferred, nil
	}
	for attempt := 0; attempt < 64; attempt++ {
		listener, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			return 0, err
		}
		port := listener.Addr().(*net.TCPAddr).Port
		_ = listener.Close()
		if ensureTUIProxyPortFree(port) == nil {
			return port, nil
		}
	}
	return 0, fmt.Errorf("could not allocate a free proxy port")
}

const (
	tuiListenerDialTimeout       = 100 * time.Millisecond
	tuiListenerValidationTimeout = 2 * time.Second
)

func ensureTUIProxyPortFree(port int) error {
	address := net.JoinHostPort("0.0.0.0", strconv.Itoa(port))
	tcpProbe, err := net.Listen("tcp4", address)
	if err != nil {
		return fmt.Errorf("proxy TCP port %d is already in use", port)
	}
	defer tcpProbe.Close()
	udpAddress, err := net.ResolveUDPAddr("udp4", address)
	if err != nil {
		return err
	}
	udpProbe, err := net.ListenUDP("udp4", udpAddress)
	if err != nil {
		return fmt.Errorf("proxy UDP port %d is already in use", port)
	}
	return udpProbe.Close()
}

func waitForTUIProxyPortState(port int, open bool, timeout time.Duration) bool {
	if port <= 0 {
		return !open
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	deadline := time.Now().Add(timeout)
	for {
		connection, err := net.DialTimeout("tcp", address, tuiListenerDialTimeout)
		if err == nil {
			_ = connection.Close()
		}
		tcpOpen := err == nil
		udpAddress, resolveErr := net.ResolveUDPAddr("udp4", address)
		udpProbe, udpErr := net.ListenUDP("udp4", udpAddress)
		udpBound := resolveErr == nil && udpErr != nil
		if udpProbe != nil {
			_ = udpProbe.Close()
		}
		if open && tcpOpen && udpBound || !open && !tcpOpen && !udpBound {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(25 * time.Millisecond)
	}
}
