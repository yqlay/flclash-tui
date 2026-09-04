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
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type tuiTrafficUpdate struct {
	Traffic trafficSnapshot
	Closed  bool
}

const tuiTrafficHistoryLimit = 30

func appendTUITrafficHistory(
	history []trafficSnapshot,
	traffic trafficSnapshot,
) []trafficSnapshot {
	if len(history) >= tuiTrafficHistoryLimit {
		copy(history, history[len(history)-tuiTrafficHistoryLimit+1:])
		history = history[:tuiTrafficHistoryLimit-1]
	}
	return append(history, traffic)
}

func (m *tuiModel) startTrafficMonitor() {
	if m.stopTraffic != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	updates := make(chan tuiTrafficUpdate, 1)
	m.trafficUpdates = updates
	m.stopTraffic = cancel
	go monitorTUITraffic(ctx, m.client, updates)
}

func (m *tuiModel) stopTrafficMonitor() {
	if m.stopTraffic != nil {
		m.stopTraffic()
		m.stopTraffic = nil
	}
	m.trafficUpdates = nil
}

func (m *tuiModel) waitTrafficUpdate() tea.Cmd {
	if m.trafficUpdates == nil {
		return nil
	}
	updates := m.trafficUpdates
	return func() tea.Msg {
		update, open := <-updates
		if !open {
			update.Closed = true
		}
		return tuiTrafficMsg{update: update}
	}
}

func monitorTUITraffic(
	ctx context.Context,
	client controllerClient,
	updates chan tuiTrafficUpdate,
) {
	defer close(updates)
	send := func(update tuiTrafficUpdate) {
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
		_ = streamTUITraffic(ctx, client, func(traffic trafficSnapshot) {
			send(tuiTrafficUpdate{Traffic: traffic})
		})
		if ctx.Err() != nil {
			return
		}
		timer := time.NewTimer(tuiRefreshInterval)
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

func streamTUITraffic(
	ctx context.Context,
	client controllerClient,
	onTraffic func(trafficSnapshot),
) error {
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		strings.TrimRight(client.baseURL(), "/")+"/traffic",
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
		var traffic trafficSnapshot
		if json.Unmarshal(scanner.Bytes(), &traffic) != nil {
			continue
		}
		onTraffic(traffic)
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return errors.New("controller traffic stream closed")
}
