//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	nethttp "net/http"

	"github.com/metacubex/mihomo/config"
	"golang.org/x/term"
)

func isInteractiveTUI() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
}

func waitForController(client controllerClient, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if _, err := client.request(nethttp.MethodGet, "/", nil); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = errors.New("controller did not become ready")
	}
	return fmt.Errorf("controller is not ready: %w", lastErr)
}

func refreshTUISnapshot(snapshot *tuiSnapshot, client controllerClient) {
	selectedGroupName := ""
	selectedNodeName := ""
	if snapshot.SelectedGroup >= 0 && snapshot.SelectedGroup < len(snapshot.Groups) {
		selectedGroup := snapshot.Groups[snapshot.SelectedGroup]
		selectedGroupName = selectedGroup.Name
		if snapshot.SelectedNode >= 0 && snapshot.SelectedNode < len(selectedGroup.Nodes) {
			selectedNodeName = selectedGroup.Nodes[snapshot.SelectedNode]
		}
	}
	selectedConnectionID := ""
	if snapshot.SelectedConnection >= 0 && snapshot.SelectedConnection < len(snapshot.Connections) {
		selectedConnectionID = snapshot.Connections[snapshot.SelectedConnection].ID
	}
	selectedRequestID := ""
	if snapshot.SelectedRequest >= 0 && snapshot.SelectedRequest < len(snapshot.Requests) {
		selectedRequestID = snapshot.Requests[snapshot.SelectedRequest].ID
	}
	selectedProviderName := ""
	if snapshot.SelectedProvider >= 0 && snapshot.SelectedProvider < len(snapshot.Providers) {
		selectedProviderName = snapshot.Providers[snapshot.SelectedProvider].Name
	}

	data, err := client.request("GET", "/proxies", nil)
	if err != nil {
		snapshot.Status = "Controller unavailable: " + err.Error()
		applyTUISSHConnections(snapshot, nil, selectedConnectionID, selectedRequestID)
		snapshot.UpdatedAt = time.Now()
		return
	}

	var response tuiProxyResponse
	if err := json.Unmarshal(data, &response); err != nil {
		snapshot.Status = "Invalid controller response: " + err.Error()
		return
	}

	groups := make([]tuiGroup, 0, len(response.Proxies))
	for name, proxy := range response.Proxies {
		if len(proxy.All) == 0 || !isTUIGroup(proxy.Type) {
			continue
		}
		delays := make(map[string]tuiDelayResult, len(proxy.All))
		for _, node := range proxy.All {
			nodeProxy, exists := response.Proxies[node]
			if !exists || len(nodeProxy.History) == 0 {
				continue
			}
			delay := nodeProxy.History[len(nodeProxy.History)-1].Delay
			if delay > 0 {
				delays[node] = tuiDelayResult{
					MedianMillis: delay,
					MinMillis:    delay,
					MaxMillis:    delay,
					Samples:      1,
				}
			}
		}
		groups = append(groups, tuiGroup{
			Name:   name,
			Type:   proxy.Type,
			Now:    proxy.Now,
			Nodes:  proxy.All,
			Delays: delays,
			Speeds: map[string]tuiSpeedResult{},
		})
	}
	orderTUIGroups(groups, snapshot.GroupOrder)
	snapshot.Groups = groups
	snapshot.SelectedGroup = findTUIGroup(groups, selectedGroupName)
	if len(groups) > 0 {
		group := groups[snapshot.SelectedGroup]
		if group.Name != selectedGroupName {
			selectedNodeName = ""
		}
		snapshot.SelectedNode = findTUIString(group.Nodes, selectedNodeName)
		if selectedNodeName == "" {
			snapshot.SelectedNode = findTUIString(group.Nodes, group.Now)
		}
	}

	if connections, err := client.request("GET", "/connections", nil); err == nil {
		var value struct {
			Connections []struct {
				ID       string `json:"id"`
				Metadata struct {
					Host            string `json:"host"`
					DestinationIP   string `json:"destinationIP"`
					DestinationPort string `json:"destinationPort"`
					Process         string `json:"process"`
					ProcessPath     string `json:"processPath"`
					UID             uint32 `json:"uid"`
					SourceIP        string `json:"sourceIP"`
					InboundName     string `json:"inboundName"`
					InboundUser     string `json:"inboundUser"`
					Network         string `json:"network"`
				} `json:"metadata"`
				Upload   int64    `json:"upload"`
				Download int64    `json:"download"`
				Chains   []string `json:"chains"`
			} `json:"connections"`
		}
		if json.Unmarshal(connections, &value) == nil {
			activeConnections := make([]tuiConnection, 0, len(value.Connections))
			systemTun := snapshot.Settings.TunEnabled && snapshot.Settings.TunScope == tuiTunScopeSystem
			for _, item := range value.Connections {
				chain := formatTUIProxyChain(item.Chains)
				host := item.Metadata.Host
				if host == "" {
					host = formatTUIDestination(
						item.Metadata.DestinationIP,
						item.Metadata.DestinationPort,
					)
				}
				connection := tuiConnection{
					ID: item.ID, Host: host, Process: item.Metadata.Process,
					ProcessPath: item.Metadata.ProcessPath, UID: item.Metadata.UID,
					SourceIP: item.Metadata.SourceIP, InboundName: item.Metadata.InboundName,
					InboundUser: item.Metadata.InboundUser,
					Network:     item.Metadata.Network, Chain: chain,
					Source: tuiTrafficSourceProxy,
					Upload: item.Upload, Download: item.Download,
				}
				if snapshot.ManagedService &&
					!isTUIOwnedConnection(connection, uint32(os.Getuid()), systemTun) {
					continue
				}
				activeConnections = append(activeConnections, connection)
			}
			applyTUISSHConnections(
				snapshot,
				activeConnections,
				selectedConnectionID,
				selectedRequestID,
			)
		} else if snapshot.Status == "" || snapshot.Status == "Connected" || snapshot.Status == "Loading..." {
			snapshot.Status = "Connections refresh failed: invalid controller response"
			applyTUISSHConnections(snapshot, nil, selectedConnectionID, selectedRequestID)
		}
	} else {
		if snapshot.Status == "" || snapshot.Status == "Connected" || snapshot.Status == "Loading..." {
			snapshot.Status = "Connections refresh failed: " + err.Error()
		}
		applyTUISSHConnections(snapshot, nil, selectedConnectionID, selectedRequestID)
	}
	systemProxyEnabled := snapshot.Settings.SystemProxy
	checkSystemProxy := snapshot.UpdatedAt.IsZero()
	if config, err := client.request("GET", "/configs", nil); err == nil {
		var value tuiConfigResponse
		if json.Unmarshal(config, &value) == nil {
			snapshot.Settings = tuiSettings{
				Mode: value.Mode, MixedPort: value.MixedPort, AllowLAN: value.AllowLAN,
				IPv6: value.IPv6, UnifiedDelay: value.UnifiedDelay,
				TCPConcurrent: value.TCPConcurrent, LogLevel: value.LogLevel,
				TunEnabled:  value.Tun.Enable,
				SystemProxy: systemProxyEnabled,
			}
		}
	}
	if checkSystemProxy {
		snapshot.Settings.SystemProxy = linuxSystemProxyMatches(
			snapshot.Settings.MixedPort,
		)
	}
	if providers, err := client.request("GET", "/providers/proxies", nil); err == nil {
		var value tuiProviderResponse
		if json.Unmarshal(providers, &value) == nil {
			snapshot.Providers = make([]tuiProvider, 0, len(value.Providers))
			for name, item := range value.Providers {
				if item.Name == "" {
					item.Name = name
				}
				snapshot.Providers = append(snapshot.Providers, tuiProvider{
					Name: item.Name, Type: item.Type, Vehicle: item.Vehicle,
					Count: len(item.Proxies), UpdatedAt: item.UpdatedAt,
				})
			}
			sort.Slice(snapshot.Providers, func(i, j int) bool { return snapshot.Providers[i].Name < snapshot.Providers[j].Name })
			snapshot.SelectedProvider = findTUIProvider(snapshot.Providers, selectedProviderName)
		}
	}
	if snapshot.Status == "" || snapshot.Status == "Loading..." ||
		snapshot.Status == "Connected" ||
		strings.HasPrefix(snapshot.Status, "Controller unavailable:") ||
		strings.HasPrefix(snapshot.Status, "Invalid controller response:") {
		snapshot.Status = "Connected"
	}
	snapshot.UpdatedAt = time.Now()
}

func applyTUISSHConnections(
	snapshot *tuiSnapshot,
	proxy []tuiConnection,
	selectedConnectionID,
	selectedRequestID string,
) {
	liveSSH, recentSSH := loadCLISSHRelayConnections()
	now := time.Now()
	live := mergeTUITrafficConnections(proxy, liveSSH)
	snapshot.Requests = rememberClosedSSHHistory(
		updateTUIRequestHistory(snapshot.Requests, live, now),
		recentSSH,
		now,
	)
	snapshot.Connections = live
	if selectedConnectionID == "" {
		snapshot.SelectedConnection = clampTUISelection(
			snapshot.SelectedConnection,
			len(snapshot.Connections),
		)
	} else {
		snapshot.SelectedConnection = findTUIConnection(
			snapshot.Connections,
			selectedConnectionID,
		)
	}
	if selectedRequestID == "" {
		snapshot.SelectedRequest = clampTUISelection(
			snapshot.SelectedRequest,
			len(snapshot.Requests),
		)
	} else {
		snapshot.SelectedRequest = findTUIRequest(
			snapshot.Requests,
			selectedRequestID,
		)
	}
}

const tuiRequestHistoryLimit = 500

func updateTUIRequestHistory(
	history []tuiRequest,
	active []tuiConnection,
	now time.Time,
) []tuiRequest {
	updated := append([]tuiRequest(nil), history...)
	indexByID := make(map[string]int, len(updated))
	for index := range updated {
		updated[index].Active = false
		if updated[index].ID != "" {
			indexByID[updated[index].ID] = index
		}
	}
	for _, connection := range active {
		if strings.TrimSpace(connection.ID) == "" {
			continue
		}
		index, exists := indexByID[connection.ID]
		if exists {
			updated[index].TuiConnection = connection
			updated[index].LastSeen = now
			updated[index].Active = true
			continue
		}
		updated = append(updated, tuiRequest{
			TuiConnection: connection,
			FirstSeen:     now,
			LastSeen:      now,
			Active:        true,
		})
		indexByID[connection.ID] = len(updated) - 1
	}
	sort.SliceStable(updated, func(i, j int) bool {
		return updated[i].LastSeen.After(updated[j].LastSeen)
	})
	if len(updated) > tuiRequestHistoryLimit {
		updated = updated[:tuiRequestHistoryLimit]
	}
	return updated
}

// markTUIRequestHistoryInactive closes the local view of every request when
// the Core is no longer running. Mihomo cannot keep a connection alive after
// its listeners stop, so retaining ACTIVE here would misrepresent persisted
// History after a stop or Backend restart.
func markTUIRequestHistoryInactive(history []tuiRequest) ([]tuiRequest, bool) {
	updated := append([]tuiRequest(nil), history...)
	changed := false
	for index := range updated {
		if updated[index].Active {
			updated[index].Active = false
			changed = true
		}
	}
	return updated, changed
}

func refreshTUIProfiles(snapshot *tuiSnapshot, paths cliPaths) {
	selectedImportRow := snapshot.SelectedRow
	importSelected := selectedImportRow < 0
	selectedProfilePath := ""
	if snapshot.SelectedRow >= 0 && snapshot.SelectedRow < len(snapshot.Profiles) {
		selectedProfilePath = snapshot.Profiles[snapshot.SelectedRow].Path
	}
	entries, err := os.ReadDir(paths.HomeDir)
	if err != nil {
		snapshot.Profiles = nil
		return
	}
	profiles := make([]tuiProfile, 0, len(entries)+1)
	subscriptionSources := loadTUISubscriptionSources(paths.HomeDir)
	currentFound := false
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if isTUIRuntimeProfileName(entry.Name()) {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(paths.HomeDir, entry.Name())
		current := filepath.Clean(path) == filepath.Clean(paths.ConfigPath)
		currentFound = currentFound || current
		subscriptionURL := ""
		if stateKey, err := tuiProfileStateKey(paths.HomeDir, path); err == nil {
			subscriptionURL = subscriptionSources[stateKey]
		}
		profiles = append(profiles, tuiProfile{
			Name:            entry.Name(),
			Path:            path,
			Current:         current,
			SubscriptionURL: subscriptionURL,
		})
	}
	if !currentFound {
		subscriptionURL := ""
		if stateKey, err := tuiProfileStateKey(
			paths.HomeDir,
			paths.ConfigPath,
		); err == nil {
			subscriptionURL = subscriptionSources[stateKey]
		}
		profiles = append(profiles, tuiProfile{
			Name:            filepath.Base(paths.ConfigPath),
			Path:            paths.ConfigPath,
			Current:         true,
			SubscriptionURL: subscriptionURL,
		})
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	snapshot.Profiles = profiles
	if importSelected {
		snapshot.SelectedRow = selectedImportRow
		if snapshot.SelectedRow < tuiProfileImportSubscriptionRow ||
			snapshot.SelectedRow > tuiProfileImportFileRow {
			snapshot.SelectedRow = tuiProfileImportSubscriptionRow
		}
		return
	}
	snapshot.SelectedRow = 0
	for index, profile := range profiles {
		if filepath.Clean(profile.Path) == filepath.Clean(selectedProfilePath) {
			snapshot.SelectedRow = index
			break
		}
	}
}

func refreshTUISSH(snapshot *tuiSnapshot) {
	selectedName := ""
	captureSelected := snapshot.SelectedSSH == tuiSSHCaptureRow
	if snapshot.SelectedSSH >= 0 && snapshot.SelectedSSH < len(snapshot.SSHProfiles) {
		selectedName = snapshot.SSHProfiles[snapshot.SelectedSSH].Name
	}
	views, err := loadCLISSHProfileViews()
	if err != nil {
		if snapshot.Page == tuiPageSSH {
			snapshot.Status = "SSH profiles unavailable: " + err.Error()
		}
		return
	}
	snapshot.SSHProfiles = make([]tuiSSHProfile, 0, len(views))
	for _, view := range views {
		snapshot.SSHProfiles = append(snapshot.SSHProfiles, tuiSSHProfile{
			Name:          view.Name,
			Username:      view.Username,
			Host:          view.Host,
			Destination:   view.Destination,
			Port:          view.Port,
			LocalPort:     view.LocalPort,
			Jump:          view.Jump,
			Identity:      view.Identity,
			PassphraseSet: view.PassphraseSet,
			PasswordSet:   view.PasswordSet,
			NeedsUsername: view.NeedsUsername,
			Default:       view.Default,
			LastError:     view.LastError,
			Connected:     view.Connected,
			Attached:      view.Attached,
			Attachable:    view.Attachable,
			Ready:         view.Ready,
			SocksPort:     view.SocksPort,
			StartedAt:     view.StartedAt,
			Options:       append([]string(nil), view.Options...),
		})
	}
	if captureSelected || len(snapshot.SSHProfiles) == 0 {
		snapshot.SelectedSSH = tuiSSHCaptureRow
		return
	}
	snapshot.SelectedSSH = 0
	for index, profile := range snapshot.SSHProfiles {
		if strings.EqualFold(profile.Name, selectedName) {
			snapshot.SelectedSSH = index
			break
		}
	}
}

func findTUIGroup(groups []tuiGroup, name string) int {
	for index, group := range groups {
		if group.Name == name {
			return index
		}
	}
	return 0
}

func findTUIString(values []string, value string) int {
	for index, candidate := range values {
		if candidate == value {
			return index
		}
	}
	return 0
}

func findTUIConnection(connections []tuiConnection, id string) int {
	for index, connection := range connections {
		if connection.ID == id {
			return index
		}
	}
	return -1
}

func findTUIRequest(requests []tuiRequest, id string) int {
	for index, request := range requests {
		if request.ID == id {
			return index
		}
	}
	return -1
}

func findTUIProvider(providers []tuiProvider, name string) int {
	for index, provider := range providers {
		if provider.Name == name {
			return index
		}
	}
	return 0
}

func switchTUIProfile(
	snapshot *tuiSnapshot,
	paths *cliPaths,
	setupParams *[]byte,
	client controllerClient,
	ownsCore,
	startListeners bool,
) {
	if !ownsCore {
		snapshot.Status = "Profile switching requires a core started by this process"
		return
	}
	if snapshot.SelectedRow < 0 || snapshot.SelectedRow >= len(snapshot.Profiles) {
		return
	}
	profile := snapshot.Profiles[snapshot.SelectedRow]
	if profile.Current {
		snapshot.Status = "Profile is already active"
		return
	}
	if message := cliHub.ValidateConfig(profile.Path); message != "" {
		snapshot.Status = "Profile invalid: " + message
		return
	}
	if err := ensureTUIFlClashDefaults(profile.Path); err != nil {
		snapshot.Status = "Profile defaults failed: " + err.Error()
		return
	}
	previousPaths := *paths
	previousSetupParams := append([]byte(nil), (*setupParams)...)
	systemProxyEnabled := snapshot.Settings.SystemProxy
	rollback := func() string {
		initParams, err := json.Marshal(InitParams{
			HomeDir: previousPaths.HomeDir, ConfigPath: previousPaths.ConfigPath, Version: 1,
		})
		if err != nil || !cliHub.Init(string(initParams)) {
			return "previous profile initialization failed"
		}
		if message := cliHub.SetupConfig(previousSetupParams); message != "" {
			return "previous profile reload failed: " + message
		}
		if startListeners && !cliHub.StartListener() {
			return "previous profile listener restart failed"
		}
		return ""
	}
	controller := client.options.address
	controllerUnix := client.options.unixSocket
	secret := client.options.secret
	params := defaultSetupParams()
	if len(*setupParams) > 0 {
		if err := UnmarshalJson(*setupParams, params); err != nil {
			snapshot.Status = "Profile setup failed: " + err.Error()
			return
		}
	}
	params.SelectedMap = loadTUISelectedProxies(paths.HomeDir)
	if controllerUnix != "" {
		params.ExternalController = nil
		params.ExternalControllerUnix = &controllerUnix
	} else {
		params.ExternalController = &controller
		params.ExternalControllerUnix = nil
	}
	params.ExternalControllerSecret = &secret
	newSetupParams, err := json.Marshal(params)
	if err != nil {
		snapshot.Status = "Profile setup failed: " + err.Error()
		return
	}
	initParams, err := json.Marshal(InitParams{
		HomeDir: paths.HomeDir, ConfigPath: profile.Path, Version: 1,
	})
	if err != nil || !cliHub.Init(string(initParams)) {
		snapshot.Status = "Profile initialization failed"
		return
	}
	if message := cliHub.SetupConfig(newSetupParams); message != "" {
		snapshot.Status = "Profile load failed: " + message
		if rollbackMessage := rollback(); rollbackMessage != "" {
			snapshot.Status += "; rollback failed: " + rollbackMessage
		}
		return
	}
	if startListeners && !cliHub.StartListener() {
		snapshot.Status = "Profile listener start failed"
		if rollbackMessage := rollback(); rollbackMessage != "" {
			snapshot.Status += "; rollback failed: " + rollbackMessage
		}
		return
	}
	paths.ConfigPath = profile.Path
	*setupParams = newSetupParams
	snapshot.GroupOrder = loadTUIProxyGroupOrder(profile.Path)
	snapshot.ProxyNodeFocus = false
	snapshot.Status = "Active profile: " + profile.Name
	if err := rememberTUIActiveProfile(*paths); err != nil {
		snapshot.Status += "; could not remember profile: " + err.Error()
	}
	refreshTUISnapshot(snapshot, client)
	if systemProxyEnabled {
		enableSystemProxy := snapshot.Settings.MixedPort > 0
		if err := setLinuxSystemProxy(snapshot.Settings.MixedPort, enableSystemProxy); err != nil {
			snapshot.Status += "; system proxy update failed: " + err.Error()
		} else {
			snapshot.Settings.SystemProxy = enableSystemProxy
			if !enableSystemProxy {
				snapshot.Status += "; System proxy disabled because the profile has no Proxy port"
			}
		}
	}
	refreshTUIProfiles(snapshot, *paths)
}

func tuiProxyGroupNow(controller controllerClient, group string) string {
	group = strings.TrimSpace(group)
	if group == "" {
		return ""
	}
	data, err := controller.request(nethttp.MethodGet, "/proxies", nil)
	if err != nil {
		return ""
	}
	var response tuiProxyResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return ""
	}
	proxy, ok := response.Proxies[group]
	if !ok {
		return ""
	}
	return strings.TrimSpace(proxy.Now)
}

func formatCLIFLCOutbound(status tuiServiceStatus) string {
	group := strings.TrimSpace(status.FLCOutbound)
	if group == "" {
		return cliDisplayValue(group)
	}
	if node := tuiProxyGroupNow(managedController(status), group); node != "" {
		return group + " → " + node
	}
	return group
}

func isTUIGroup(proxyType string) bool {
	switch strings.ToLower(proxyType) {
	case "selector", "urltest", "fallback", "loadbalance", "relay":
		return true
	default:
		return false
	}
}

func loadTUIProxyGroupOrder(path string) []string {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	rawConfig, err := config.UnmarshalRawConfig(data)
	if err != nil {
		return nil
	}
	order := make([]string, 0, len(rawConfig.ProxyGroup))
	for _, group := range rawConfig.ProxyGroup {
		name, ok := group["name"].(string)
		if ok && name != "" {
			order = append(order, name)
		}
	}
	return order
}

func orderTUIGroups(groups []tuiGroup, order []string) {
	positions := make(map[string]int, len(order))
	for index, name := range order {
		positions[name] = index
	}
	sort.SliceStable(groups, func(i, j int) bool {
		left, leftKnown := positions[groups[i].Name]
		right, rightKnown := positions[groups[j].Name]
		switch {
		case leftKnown && rightKnown:
			return left < right
		case leftKnown:
			return true
		case rightKnown:
			return false
		default:
			return groups[i].Name < groups[j].Name
		}
	})
}

func moveTUIGroup(snapshot *tuiSnapshot, delta int) {
	if len(snapshot.Groups) == 0 {
		return
	}
	snapshot.SelectedGroup = wrapTUIIndex(snapshot.SelectedGroup, delta, len(snapshot.Groups))
	snapshot.SelectedNode = findTUIString(
		snapshot.Groups[snapshot.SelectedGroup].Nodes,
		snapshot.Groups[snapshot.SelectedGroup].Now,
	)
}

func moveTUINode(snapshot *tuiSnapshot, delta int) {
	if snapshot.SelectedGroup < 0 || snapshot.SelectedGroup >= len(snapshot.Groups) {
		return
	}
	nodes := snapshot.Groups[snapshot.SelectedGroup].Nodes
	if len(nodes) == 0 {
		return
	}
	snapshot.SelectedNode = wrapTUIIndex(snapshot.SelectedNode, delta, len(nodes))
}

func moveTUIProvider(snapshot *tuiSnapshot, delta int) {
	if len(snapshot.Providers) == 0 {
		return
	}
	snapshot.SelectedProvider = wrapTUIIndex(snapshot.SelectedProvider, delta, len(snapshot.Providers))
}

func moveTUIProfile(snapshot *tuiSnapshot, delta int) {
	position := snapshot.SelectedRow + tuiProfileImportRowCount
	position = wrapTUIIndex(
		position,
		delta,
		len(snapshot.Profiles)+tuiProfileImportRowCount,
	)
	snapshot.SelectedRow = position - tuiProfileImportRowCount
}

func moveTUIConnection(snapshot *tuiSnapshot, delta int) {
	if len(snapshot.Connections) == 0 {
		return
	}
	snapshot.SelectedConnection = wrapTUIIndex(snapshot.SelectedConnection, delta, len(snapshot.Connections))
}

func wrapTUIIndex(current, delta, total int) int {
	if total <= 0 {
		return 0
	}
	current %= total
	if current < 0 {
		current += total
	}
	next := (current + delta) % total
	if next < 0 {
		next += total
	}
	return next
}

func selectTUIProxy(
	snapshot *tuiSnapshot,
	client controllerClient,
	homeDir string,
) bool {
	if snapshot.SelectedGroup < 0 || snapshot.SelectedGroup >= len(snapshot.Groups) {
		snapshot.Status = "Select a proxy group before applying it"
		return false
	}
	group := snapshot.Groups[snapshot.SelectedGroup]
	if snapshot.SelectedNode < 0 || snapshot.SelectedNode >= len(group.Nodes) {
		snapshot.Status = "Select a proxy node before applying it"
		return false
	}
	if err := client.setProxy(group.Name, group.Nodes[snapshot.SelectedNode]); err != nil {
		snapshot.Status = "Switch failed: " + err.Error()
		return false
	}
	snapshot.Status = fmt.Sprintf("Switched %s to %s", group.Name, group.Nodes[snapshot.SelectedNode])
	if err := rememberTUIProxySelection(
		homeDir,
		group.Name,
		group.Nodes[snapshot.SelectedNode],
	); err != nil {
		snapshot.Status += "; selection save failed: " + err.Error()
	}
	refreshTUISnapshot(snapshot, client)
	return true
}

func updateTUIProvider(snapshot *tuiSnapshot, client controllerClient) {
	if snapshot.SelectedProvider < 0 || snapshot.SelectedProvider >= len(snapshot.Providers) {
		snapshot.Status = "Select a provider before updating it"
		return
	}
	provider := snapshot.Providers[snapshot.SelectedProvider]
	if err := client.updateProvider(provider.Name); err != nil {
		snapshot.Status = "Provider update failed: " + err.Error()
		return
	}
	snapshot.Status = "Updated provider " + provider.Name
	refreshTUISnapshot(snapshot, client)
}

func updateTUISettings(snapshot *tuiSnapshot, client controllerClient, key tuiKey) bool {
	systemProxyEnabled := snapshot.Settings.SystemProxy
	patch := map[string]interface{}{}
	switch key {
	case tuiKeyAllowLAN:
		patch["allow-lan"] = !snapshot.Settings.AllowLAN
	case tuiKeyIPv6:
		patch["ipv6"] = !snapshot.Settings.IPv6
	case tuiKeyUnifiedDelay:
		patch["unified-delay"] = !snapshot.Settings.UnifiedDelay
	case tuiKeyTCPConcurrent:
		patch["tcp-concurrent"] = !snapshot.Settings.TCPConcurrent
	case tuiKeyTun:
		patch["tun"] = map[string]bool{"enable": !snapshot.Settings.TunEnabled}
	case tuiKeyLogLevel:
		levels := []string{"silent", "error", "warning", "info", "debug"}
		current := findTUIString(levels, strings.ToLower(snapshot.Settings.LogLevel))
		patch["log-level"] = levels[wrapTUIIndex(current, 1, len(levels))]
	case tuiKeyPortUp:
		if snapshot.Settings.MixedPort >= 65535 {
			snapshot.Status = "Proxy port is already at 65535"
			return false
		}
		patch["mixed-port"] = snapshot.Settings.MixedPort + 1
	case tuiKeyPortDown:
		if snapshot.Settings.MixedPort <= 0 {
			snapshot.Status = "Proxy port is already at 0"
			return false
		}
		patch["mixed-port"] = snapshot.Settings.MixedPort - 1
	default:
		return false
	}
	if err := client.patchConfig(patch); err != nil {
		snapshot.Status = "Settings update failed: " + err.Error()
		return false
	}
	snapshot.Status = "Settings updated"
	refreshTUISnapshot(snapshot, client)
	if (key == tuiKeyPortUp || key == tuiKeyPortDown) && systemProxyEnabled {
		enableSystemProxy := snapshot.Settings.MixedPort > 0
		if err := setLinuxSystemProxy(snapshot.Settings.MixedPort, enableSystemProxy); err != nil {
			snapshot.Status = "Port changed, but system proxy update failed: " + err.Error()
		} else if !enableSystemProxy {
			snapshot.Settings.SystemProxy = false
			snapshot.Status = "Proxy port disabled; System proxy disabled"
		} else {
			snapshot.Settings.SystemProxy = true
			snapshot.Status = fmt.Sprintf("Proxy port changed to %d", snapshot.Settings.MixedPort)
		}
	}
	return true
}

func toggleTUISystemProxy(snapshot *tuiSnapshot) bool {
	port := snapshot.Settings.MixedPort
	if port <= 0 {
		snapshot.Status = "System proxy requires a positive Proxy port"
		return false
	}
	enable := !snapshot.Settings.SystemProxy
	if err := setLinuxSystemProxy(port, enable); err != nil {
		snapshot.Status = "System proxy update failed: " + err.Error()
		return false
	}
	snapshot.Settings.SystemProxy = enable
	snapshot.Status = "System proxy enabled"
	if !enable {
		snapshot.Status = "System proxy disabled"
	}
	return true
}

func setTUIMixedPort(snapshot *tuiSnapshot, client controllerClient, oldState **term.State) {
	currentPort := snapshot.Settings.MixedPort
	selectedPort := currentPort
	changed := false
	err := runTUICooked(oldState, func() error {
		_, _ = fmt.Fprintf(os.Stdout, "Proxy port [0-65535] (current %d, empty cancels): ", currentPort)
		value, readErr := readTUILine(os.Stdin)
		if readErr != nil && len(value) == 0 {
			return readErr
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return nil
		}
		port, parseErr := strconv.Atoi(value)
		if parseErr != nil || port < 0 || port > 65535 {
			return errors.New("Proxy port must be a number from 0 to 65535")
		}
		selectedPort = port
		changed = selectedPort != currentPort
		return nil
	})
	if err != nil {
		snapshot.Status = "Port change failed: " + err.Error()
		return
	}
	if !changed {
		snapshot.Status = "Port unchanged"
		return
	}
	if err := client.patchConfig(map[string]interface{}{"mixed-port": selectedPort}); err != nil {
		snapshot.Status = "Port change failed: " + err.Error()
		return
	}
	systemProxyEnabled := snapshot.Settings.SystemProxy
	refreshTUISnapshot(snapshot, client)
	if systemProxyEnabled {
		enableSystemProxy := snapshot.Settings.MixedPort > 0
		if err := setLinuxSystemProxy(snapshot.Settings.MixedPort, enableSystemProxy); err != nil {
			snapshot.Status = "Port changed, but system proxy update failed: " + err.Error()
			return
		}
		snapshot.Settings.SystemProxy = enableSystemProxy
		if !enableSystemProxy {
			snapshot.Status = "Proxy port disabled; System proxy disabled"
			return
		}
	}
	snapshot.Status = fmt.Sprintf("Proxy port changed to %d", snapshot.Settings.MixedPort)
}
