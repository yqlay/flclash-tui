//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const tuiMemoryRefreshInterval = time.Second

type tuiCoreMemoryUpdate struct {
	RSS       uint64
	Error     string
	UpdatedAt time.Time
	Closed    bool
}

func (m *tuiModel) startMemoryRefresh() tea.Cmd {
	if m.memoryRefreshActive {
		return nil
	}
	m.memoryRefreshActive = true
	current := m.snapshot.Memory
	externalCore := !m.ownsCore
	return func() tea.Msg {
		return tuiMemoryResultMsg{
			info: sampleTUIMemory(current, externalCore),
		}
	}
}

func sampleTUIMemory(current tuiMemoryInfo, externalCore bool) tuiMemoryInfo {
	info := tuiMemoryInfo{
		CoreRSS:      current.CoreRSS,
		CoreError:    current.CoreError,
		CoreUpdated:  current.CoreUpdated,
		ExternalCore: externalCore,
		UpdatedAt:    time.Now(),
	}
	var sampleErrors []string
	total, available, err := readTUISystemMemory("/proc/meminfo")
	if err != nil {
		sampleErrors = append(sampleErrors, "system: "+err.Error())
	} else {
		info.SystemTotal = total
		if available < total {
			info.SystemUsed = total - available
		}
	}
	processRSS, err := readTUIProcessRSS("/proc/self/statm", uint64(os.Getpagesize()))
	if err != nil {
		sampleErrors = append(sampleErrors, "process: "+err.Error())
	} else {
		info.ProcessRSS = processRSS
	}
	var memoryStats runtime.MemStats
	runtime.ReadMemStats(&memoryStats)
	info.GoHeap = memoryStats.HeapAlloc
	info.Error = strings.Join(sampleErrors, "; ")
	return info
}

func readTUISystemMemory(path string) (total, available uint64, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, 0, err
	}
	return parseTUISystemMemory(data)
}

func parseTUISystemMemory(data []byte) (total, available uint64, err error) {
	values := map[string]uint64{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		value, parseErr := strconv.ParseUint(fields[1], 10, 64)
		if parseErr != nil {
			continue
		}
		if len(fields) >= 3 && strings.EqualFold(fields[2], "kB") {
			value *= 1024
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return 0, 0, err
	}
	total = values["MemTotal"]
	if total == 0 {
		return 0, 0, errors.New("MemTotal is unavailable")
	}
	available, hasAvailable := values["MemAvailable"]
	if !hasAvailable {
		available = values["MemFree"] + values["Buffers"] + values["Cached"]
	}
	if available > total {
		available = total
	}
	return total, available, nil
}

func readTUIProcessRSS(path string, pageSize uint64) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return 0, errors.New("statm has no RSS field")
	}
	pages, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, err
	}
	return pages * pageSize, nil
}

func (m *tuiModel) startCoreMemoryMonitor() {
	if m.ownsCore && m.service == nil || m.coreMemoryUpdates != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan tuiCoreMemoryUpdate, 1)
	m.coreMemoryUpdates = updates
	m.stopCoreMemory = cancel
	go monitorTUICoreMemory(ctx, m.client, updates)
}

func (m *tuiModel) stopCoreMemoryMonitor() {
	if m.stopCoreMemory != nil {
		m.stopCoreMemory()
		m.stopCoreMemory = nil
	}
}

func (m *tuiModel) waitCoreMemoryUpdate() tea.Cmd {
	if m.coreMemoryUpdates == nil {
		return nil
	}
	updates := m.coreMemoryUpdates
	return func() tea.Msg {
		update, open := <-updates
		if !open {
			update.Closed = true
		}
		return tuiCoreMemoryMsg{update: update}
	}
}

func monitorTUICoreMemory(
	ctx context.Context,
	client controllerClient,
	updates chan tuiCoreMemoryUpdate,
) {
	defer close(updates)
	send := func(update tuiCoreMemoryUpdate) {
		select {
		case updates <- update:
		default:
			select {
			case <-updates:
			default:
			}
			select {
			case updates <- update:
			case <-ctx.Done():
			}
		}
	}
	for ctx.Err() == nil {
		err := streamTUICoreMemory(ctx, client, func(rss uint64) {
			send(tuiCoreMemoryUpdate{
				RSS:       rss,
				UpdatedAt: time.Now(),
			})
		})
		if ctx.Err() != nil {
			return
		}
		send(tuiCoreMemoryUpdate{
			Error:     err.Error(),
			UpdatedAt: time.Now(),
		})
		timer := time.NewTimer(tuiMemoryRefreshInterval)
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		}
	}
}

func streamTUICoreMemory(
	ctx context.Context,
	client controllerClient,
	onMemory func(uint64),
) error {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimRight(client.baseURL(), "/")+"/memory",
		nil,
	)
	if err != nil {
		return err
	}
	if client.options.secret != "" {
		request.Header.Set("Authorization", "Bearer "+client.options.secret)
	}
	httpClient := *client.httpClient()
	httpClient.Timeout = 0
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return fmt.Errorf(
			"controller returned %s: %s",
			response.Status,
			strings.TrimSpace(string(data)),
		)
	}
	scanner := bufio.NewScanner(response.Body)
	for scanner.Scan() {
		var payload struct {
			Inuse uint64 `json:"inuse"`
		}
		if json.Unmarshal(scanner.Bytes(), &payload) != nil || payload.Inuse == 0 {
			continue
		}
		onMemory(payload.Inuse)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("controller memory stream closed")
}
