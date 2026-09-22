//go:build linux && !cgo && cli

package main

import (
	"strings"
	"time"
)

const (
	tuiTrafficSourceMixed = "mixed"
	tuiTrafficSourceProxy = "proxy"
	tuiTrafficSourceSSH   = "ssh"
)

func normalizeTrafficSource(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case tuiTrafficSourceProxy:
		return tuiTrafficSourceProxy
	case tuiTrafficSourceSSH:
		return tuiTrafficSourceSSH
	default:
		return tuiTrafficSourceMixed
	}
}

func nextTrafficSource(value string) string {
	switch normalizeTrafficSource(value) {
	case tuiTrafficSourceMixed:
		return tuiTrafficSourceProxy
	case tuiTrafficSourceProxy:
		return tuiTrafficSourceSSH
	default:
		return tuiTrafficSourceMixed
	}
}

func isSSHConnectionID(id string) bool {
	return strings.HasPrefix(strings.TrimSpace(id), cliSSHFlowIDPrefix)
}

func tuiConnectionSource(connection tuiConnection) string {
	if strings.EqualFold(connection.Source, tuiTrafficSourceSSH) ||
		isSSHConnectionID(connection.ID) {
		return tuiTrafficSourceSSH
	}
	return tuiTrafficSourceProxy
}

func trafficSourceMatches(filter, source string) bool {
	filter = normalizeTrafficSource(filter)
	if filter == tuiTrafficSourceMixed {
		return true
	}
	return filter == source
}

func tagTUIConnectionSource(connection tuiConnection, source string) tuiConnection {
	if strings.TrimSpace(connection.Source) == "" {
		connection.Source = source
	}
	return connection
}

func tagTUIConnectionsSource(connections []tuiConnection, source string) []tuiConnection {
	tagged := make([]tuiConnection, 0, len(connections))
	for _, connection := range connections {
		tagged = append(tagged, tagTUIConnectionSource(connection, source))
	}
	return tagged
}

func mergeTUITrafficConnections(groups ...[]tuiConnection) []tuiConnection {
	total := 0
	for _, group := range groups {
		total += len(group)
	}
	merged := make([]tuiConnection, 0, total)
	seen := make(map[string]bool, total)
	for _, group := range groups {
		for _, connection := range group {
			if connection.ID == "" || seen[connection.ID] {
				continue
			}
			seen[connection.ID] = true
			merged = append(merged, connection)
		}
	}
	return merged
}

func filterConnectionsBySource(connections []tuiConnection, source string) []tuiConnection {
	source = normalizeTrafficSource(source)
	if source == tuiTrafficSourceMixed {
		return connections
	}
	filtered := make([]tuiConnection, 0, len(connections))
	for _, connection := range connections {
		if tuiConnectionSource(connection) == source {
			filtered = append(filtered, connection)
		}
	}
	return filtered
}

func filterRequestsBySource(requests []tuiRequest, source string) []tuiRequest {
	source = normalizeTrafficSource(source)
	if source == tuiTrafficSourceMixed {
		return requests
	}
	filtered := make([]tuiRequest, 0, len(requests))
	for _, request := range requests {
		if tuiConnectionSource(request.TuiConnection) == source {
			filtered = append(filtered, request)
		}
	}
	return filtered
}

func cliSSHRelayFlowConnection(flow cliSSHRelayFlow) tuiConnection {
	return tuiConnection{
		ID:       flow.ID,
		Host:     flow.Host,
		Network:  flow.Network,
		Chain:    flow.Chain,
		Source:   tuiTrafficSourceSSH,
		Upload:   flow.Upload,
		Download: flow.Download,
	}
}

func splitCLISSHRelayConnections(flows []cliSSHRelayFlow) (live, recent []tuiConnection) {
	live = make([]tuiConnection, 0, len(flows))
	recent = make([]tuiConnection, 0, len(flows))
	for _, flow := range flows {
		connection := cliSSHRelayFlowConnection(flow)
		if flow.Active {
			live = append(live, connection)
			continue
		}
		recent = append(recent, connection)
	}
	return live, recent
}

func loadCLISSHRelayConnections() (live, recent []tuiConnection) {
	return splitCLISSHRelayConnections(loadCLISSHRelayFlows())
}

func (m *tuiModel) cycleTrafficSource() {
	m.snapshot.TrafficSource = nextTrafficSource(m.snapshot.TrafficSource)
	m.snapshot.SelectedConnection = firstTUIConnectionMatch(m.snapshot)
	m.snapshot.SelectedRequest = firstTUIRequestMatch(m.snapshot)
	m.snapshot.ConnectionsDetailOpen = false
	m.snapshot.HistoryDetailOpen = false
	m.snapshot.Status = "Traffic source: " + normalizeTrafficSource(m.snapshot.TrafficSource)
}

func trafficClearConfirmMessage(source, kind string) string {
	switch normalizeTrafficSource(source) {
	case tuiTrafficSourceProxy:
		if kind == "history" {
			return "Delete persisted proxy History entries. SSH history is kept."
		}
		return "Close every visible proxy connection. SSH flows are left running."
	case tuiTrafficSourceSSH:
		if kind == "history" {
			return "Delete persisted SSH History entries. Proxy history is kept."
		}
		return "Close every visible SSH SOCKS flow. Proxy connections are left running."
	default:
		if kind == "history" {
			return "Delete all persisted active and completed History entries from the Backend."
		}
		return "Immediately close every active proxy and SSH connection visible on this page."
	}
}

func rememberClosedSSHHistory(
	history []tuiRequest,
	recent []tuiConnection,
	now time.Time,
) []tuiRequest {
	if len(recent) == 0 {
		return history
	}
	known := make(map[string]bool, len(history))
	for _, entry := range history {
		if entry.ID != "" {
			known[entry.ID] = true
		}
	}
	updated := append([]tuiRequest(nil), history...)
	for _, connection := range recent {
		if connection.ID == "" || known[connection.ID] {
			continue
		}
		known[connection.ID] = true
		updated = append(updated, tuiRequest{
			TuiConnection: connection,
			FirstSeen:     now,
			LastSeen:      now,
			Active:        false,
		})
	}
	if len(updated) > tuiRequestHistoryLimit {
		updated = updated[:tuiRequestHistoryLimit]
	}
	return updated
}
