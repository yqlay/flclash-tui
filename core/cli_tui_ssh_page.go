//go:build linux && !cgo && cli

package main

import (
	"context"
	"errors"
	"fmt"
	nethttp "net/http"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *tuiModel) runSelectedSSHAction(action string) tea.Cmd {
	return m.runSelectedSSHActionWithCredentials(action, cliSSHCredentials{})
}

func (m *tuiModel) runSelectedSSHActionWithCredentials(
	action string,
	credentials cliSSHCredentials,
) tea.Cmd {
	if m.snapshot.SelectedSSH < 0 ||
		m.snapshot.SelectedSSH >= len(m.snapshot.SSHProfiles) {
		m.snapshot.Status = "Select an SSH profile first"
		return nil
	}
	if m.busy {
		m.snapshot.Status = "Another operation is still running"
		return nil
	}
	name := m.snapshot.SSHProfiles[m.snapshot.SelectedSSH].Name
	m.busy = true
	m.snapshot.Status = "SSH " + action + " " + name + "..."
	return func() tea.Msg {
		message := tuiSSHCommandResultMsg{action: action, selectedName: name}
		switch action {
		case "connect":
			state, alreadyConnected, err := connectCLISSHProfileWithCredentials(
				name,
				credentials,
			)
			message.err = err
			if err == nil {
				prefix := "connected"
				if alreadyConnected {
					prefix = "already connected"
				} else if state.Kind == cliSSHAttachedKind {
					prefix = "attached"
				}
				message.status = fmt.Sprintf(
					"SSH %s %s · SOCKS5 127.0.0.1:%d",
					state.Name,
					prefix,
					state.Port,
				)
			}
		case "attach":
			state, alreadyConnected, err := attachCLISSHProfile(name)
			message.err = err
			if err == nil {
				prefix := "attached"
				if alreadyConnected {
					prefix = "already connected"
				}
				message.status = fmt.Sprintf(
					"SSH %s %s · SOCKS5 127.0.0.1:%d",
					state.Name,
					prefix,
					state.Port,
				)
			}
		case "disconnect":
			state, disconnected, err := disconnectCLISSHProfile(name)
			message.err = err
			if err == nil {
				if disconnected {
					message.status = "SSH " + state.Name + " disconnected"
				} else {
					message.status = "No persistent SSH tunnel is open"
				}
			}
		case "test":
			state, latency, err := testCLISSHProfile(name)
			message.err = err
			if err == nil {
				message.status = fmt.Sprintf(
					"SSH %s ready · SOCKS5 127.0.0.1:%d · handshake %s",
					state.Name,
					state.Port,
					latency,
				)
			}
		default:
			message.err = fmt.Errorf("unsupported TUI SSH action %q", action)
		}
		return message
	}
}

func (m *tuiModel) beginSSHCapture() tea.Cmd {
	if m.snapshot.FocusSidebar {
		m.snapshot.Status = "Focus SSH profiles before capturing a live session"
		return nil
	}
	profiles := append([]tuiSSHProfile(nil), m.snapshot.SSHProfiles...)
	current := m.selectedSSHName()
	m.sshCaptureGeneration++
	generation := m.sshCaptureGeneration
	m.sshCaptureOpen = true
	m.sshCaptureNames = nil
	m.sshCaptureOptions = []string{"Checking existing SSH connections… · Esc cancel"}
	m.sshCaptureSelected = 0
	return func() tea.Msg {
		result := discoverTUISSHCapture(profiles, current)
		result.generation = generation
		return result
	}
}

func discoverTUISSHCapture(profiles []tuiSSHProfile, current string) tuiSSHCaptureResultMsg {
	names := make([]string, 0, len(profiles))
	options := make([]string, 0, len(profiles))
	selected := 0
	for _, profile := range profiles {
		if profile.NeedsUsername || (profile.Connected && profile.Ready) {
			continue
		}
		candidate := normalizeCLISSHProfile(cliSSHProfile{
			Name:     profile.Name,
			Username: profile.Username,
			Host:     profile.Host,
			Port:     profile.Port,
			Jump:     profile.Jump,
			Options:  append([]string(nil), profile.Options...),
		})
		path, ok := findCLILiveSSHMaster(candidate)
		if !ok {
			continue
		}
		if current != "" && strings.EqualFold(profile.Name, current) {
			selected = len(names)
		}
		label := fmt.Sprintf("%-16s %s", profile.Name, profile.Destination)
		if profile.Jump != "" {
			label += " · via " + profile.Jump
		}
		label += " · " + path
		names = append(names, profile.Name)
		options = append(options, label)
	}
	return tuiSSHCaptureResultMsg{names: names, options: options, selected: selected}
}

func (m *tuiModel) handleSSHCapture(message tea.KeyMsg) tea.Cmd {
	key, ok := tuiKeyFromTea(message)
	if !ok {
		return nil
	}
	switch key {
	case tuiKeyUp:
		m.sshCaptureSelected = wrapTUIIndex(
			m.sshCaptureSelected,
			-1,
			len(m.sshCaptureOptions),
		)
	case tuiKeyDown:
		m.sshCaptureSelected = wrapTUIIndex(
			m.sshCaptureSelected,
			1,
			len(m.sshCaptureOptions),
		)
	case tuiKeySelect:
		if m.sshCaptureNames == nil {
			return nil
		}
		if m.sshCaptureSelected < 0 ||
			m.sshCaptureSelected >= len(m.sshCaptureNames) {
			m.sshCaptureOpen = false
			m.snapshot.Status = "No live ControlMaster to capture"
			return nil
		}
		name := m.sshCaptureNames[m.sshCaptureSelected]
		m.sshCaptureOpen = false
		for index, profile := range m.snapshot.SSHProfiles {
			if strings.EqualFold(profile.Name, name) {
				m.snapshot.SelectedSSH = index
				break
			}
		}
		return m.runSelectedSSHAction("attach")
	case tuiKeyBack:
		m.sshCaptureOpen = false
		m.snapshot.Status = "SSH capture cancelled"
	case tuiKeyQuit, tuiKeyInterrupt:
		m.sshCaptureOpen = false
		return m.handleKey(key)
	}
	return nil
}

func (m *tuiModel) attachSelectedSSH() tea.Cmd {
	return m.beginSSHCapture()
}

func (m *tuiModel) selectedSSHName() string {
	if m.snapshot.SelectedSSH < 0 || m.snapshot.SelectedSSH >= len(m.snapshot.SSHProfiles) {
		return ""
	}
	return m.snapshot.SSHProfiles[m.snapshot.SelectedSSH].Name
}

func (m *tuiModel) toggleSelectedSSHDefault() tea.Cmd {
	if m.snapshot.FocusSidebar || m.snapshot.SSHDashboardFocus {
		m.snapshot.Status = "Focus SSH profiles before setting a default"
		return nil
	}
	if m.snapshot.SelectedSSH < 0 || m.snapshot.SelectedSSH >= len(m.snapshot.SSHProfiles) {
		m.snapshot.Status = "Select an SSH profile first"
		return nil
	}
	profile := m.snapshot.SSHProfiles[m.snapshot.SelectedSSH]
	name := profile.Name
	clear := profile.Default
	m.busy = true
	m.snapshot.Status = "Updating default SSH profile..."
	return func() tea.Msg {
		message := tuiSSHCommandResultMsg{action: "default", selectedName: name}
		if clear {
			message.err = setCLISSHDefault("")
			if message.err == nil {
				message.status = "Default SSH profile cleared"
			}
			return message
		}
		message.err = setCLISSHDefault(name)
		if message.err == nil {
			message.status = "Default SSH profile " + name
		}
		return message
	}
}

func (m *tuiModel) resetSelectedSSHMetrics() {
	m.snapshot.SSHNetwork = tuiNetworkInfo{}
	m.snapshot.SSHDelay = tuiDelayResult{}
	m.snapshot.SSHSpeed = tuiSpeedResult{}
	m.snapshot.SSHDirectProbe = cliSSHRemoteProbe{}
	m.snapshot.SSHDirectNetwork = tuiNetworkInfo{}
	m.snapshot.SSHDirectDelay = tuiDelayResult{}
	m.snapshot.SSHDirectSpeed = tuiSpeedResult{}
	m.snapshot.SSHTraffic = trafficSnapshot{}
	m.snapshot.SSHTrafficHistory = nil
	m.snapshot.SSHTotalTraffic = trafficSnapshot{}
	m.snapshot.SSHConnections = 0
	m.sshLastStats = cliSSHRelayStats{}
	m.sshLastStatsAt = time.Time{}
	m.sshLastStatsName = ""
}

func (m *tuiModel) refreshSelectedSSHDashboard() tea.Cmd {
	if m.snapshot.Page != tuiPageSSH ||
		m.snapshot.SelectedSSH < 0 ||
		m.snapshot.SelectedSSH >= len(m.snapshot.SSHProfiles) {
		return nil
	}
	profile := m.snapshot.SSHProfiles[m.snapshot.SelectedSSH]
	if !profile.Connected || !profile.Ready {
		return nil
	}
	return tea.Batch(
		m.pollSelectedSSHRelay(),
		m.refreshSelectedSSHNetwork(),
		m.refreshSelectedSSHDirectProbe(),
	)
}

func (m *tuiModel) pollSelectedSSHRelay() tea.Cmd {
	if m.snapshot.Page != tuiPageSSH ||
		m.snapshot.SelectedSSH < 0 ||
		m.snapshot.SelectedSSH >= len(m.snapshot.SSHProfiles) {
		return nil
	}
	profile := m.snapshot.SSHProfiles[m.snapshot.SelectedSSH]
	if !profile.Connected || !profile.Ready {
		return nil
	}
	name := m.selectedSSHName()
	if name == "" {
		return nil
	}
	return func() tea.Msg {
		message := tuiSSHRelayStatsMsg{name: name, at: time.Now()}
		state, active, err := activeCLIPersistentSSHTunnel()
		if err != nil {
			message.err = err
			return message
		}
		if !active || !strings.EqualFold(state.Name, name) {
			message.err = errors.New("SSH tunnel is disconnected")
			return message
		}
		message.stats, message.err = queryCLISSHRelay(state, "status")
		return message
	}
}

func (m *tuiModel) selectedSSHHTTPClient() (
	string,
	*nethttp.Client,
	func(),
	error,
) {
	name := m.selectedSSHName()
	if name == "" {
		return "", nil, nil, errors.New("select an SSH profile first")
	}
	profile := m.snapshot.SSHProfiles[m.snapshot.SelectedSSH]
	if !profile.Connected || !profile.Ready || profile.SocksPort < 1 {
		return name, nil, nil, errors.New("connect this SSH profile first")
	}
	client, closeClient, err := newTUISOCKSHTTPClient(profile.SocksPort)
	return name, client, closeClient, err
}

func (m *tuiModel) refreshSelectedSSHNetwork() tea.Cmd {
	return m.refreshSelectedSSHNetworkFor(false)
}

func (m *tuiModel) refreshSelectedSSHNetworkFor(direct bool) tea.Cmd {
	name, client, closeClient, err := m.selectedSSHHTTPClient()
	if err != nil {
		info := tuiNetworkInfo{Error: err.Error(), CheckedAt: time.Now()}
		if direct {
			m.snapshot.SSHDirectNetwork = info
			m.snapshot.Status = "SSH direct network detection failed: " + err.Error()
		} else {
			m.snapshot.SSHNetwork = info
			m.snapshot.Status = "SSH network detection failed: " + err.Error()
		}
		return nil
	}
	if direct {
		m.snapshot.SSHDirectNetwork.Loading = true
	} else {
		m.snapshot.SSHNetwork.Loading = true
	}
	return func() tea.Msg {
		defer closeClient()
		return tuiSSHNetworkResultMsg{
			name:   name,
			direct: direct,
			info:   detectTUINetworkWithClient(client, "SSH · "+name),
		}
	}
}

func (m *tuiModel) testSelectedSSHDelay() tea.Cmd {
	return m.testSelectedSSHDelayFor(false)
}

func (m *tuiModel) testSelectedSSHDelayFor(direct bool) tea.Cmd {
	if direct && !m.snapshot.SSHDirectProbe.DirectAllowed {
		m.snapshot.Status = "SSH direct route unavailable: " + cliDisplayValue(m.snapshot.SSHDirectProbe.Reason)
		return nil
	}
	name, client, closeClient, err := m.selectedSSHHTTPClient()
	if err != nil {
		m.snapshot.Status = "SSH route delay failed: " + err.Error()
		return nil
	}
	if direct {
		m.snapshot.SSHDirectDelay = tuiDelayResult{Testing: true}
	} else {
		m.snapshot.SSHDelay = tuiDelayResult{Testing: true}
	}
	testURL := m.tuiDelayTestURL()
	return func() tea.Msg {
		defer closeClient()
		result, testErr := runTUIRouteDelayTest(context.Background(), client, testURL)
		return tuiSSHDelayResultMsg{name: name, direct: direct, result: result, err: testErr}
	}
}

func (m *tuiModel) testSelectedSSHSpeed() tea.Cmd {
	return m.testSelectedSSHSpeedFor(false)
}

func (m *tuiModel) testSelectedSSHSpeedFor(direct bool) tea.Cmd {
	if direct && !m.snapshot.SSHDirectProbe.DirectAllowed {
		m.snapshot.Status = "SSH direct route unavailable: " + cliDisplayValue(m.snapshot.SSHDirectProbe.Reason)
		return nil
	}
	name, client, closeClient, err := m.selectedSSHHTTPClient()
	if err != nil {
		m.snapshot.Status = "SSH route speed failed: " + err.Error()
		return nil
	}
	if direct {
		m.snapshot.SSHDirectSpeed = tuiSpeedResult{Testing: true}
	} else {
		m.snapshot.SSHSpeed = tuiSpeedResult{Testing: true}
	}
	return func() tea.Msg {
		defer closeClient()
		result, testErr := runTUIDownloadSpeedTest(context.Background(), client)
		return tuiSSHSpeedResultMsg{name: name, direct: direct, result: result, err: testErr}
	}
}

func (m *tuiModel) refreshSelectedSSHDirectProbe() tea.Cmd {
	name := m.selectedSSHName()
	if name == "" {
		return nil
	}
	profile := m.snapshot.SSHProfiles[m.snapshot.SelectedSSH]
	if !profile.Connected || !profile.Ready {
		return nil
	}
	return func() tea.Msg {
		message := tuiSSHDirectProbeResultMsg{name: name}
		state, active, err := activeCLIPersistentSSHTunnel()
		if err != nil {
			message.err = err
			return message
		}
		if !active || !strings.EqualFold(state.Name, name) {
			message.err = errors.New("SSH tunnel is disconnected")
			return message
		}
		message.probe, message.err = probeCLISSHRemote(state)
		return message
	}
}

func (m *tuiModel) beginSSHCredentialPrompt(profile, identity string) {
	m.sshCredentialPromptOpen = true
	m.sshCredentialProfile = profile
	m.sshCredentialIdentity = identity
	m.sshCredentialInput = nil
	m.snapshot.Status = "Encrypted SSH private key · enter its one-time passphrase"
}

func (m *tuiModel) resetSSHCredentialPrompt() {
	for index := range m.sshCredentialInput {
		m.sshCredentialInput[index] = 0
	}
	m.sshCredentialInput = nil
	m.sshCredentialPromptOpen = false
	m.sshCredentialProfile = ""
	m.sshCredentialIdentity = ""
}

func (m *tuiModel) handleSSHCredentialPrompt(message tea.KeyMsg) tea.Cmd {
	switch message.Type {
	case tea.KeyCtrlC:
		m.resetSSHCredentialPrompt()
		return m.handleKey(tuiKeyInterrupt)
	case tea.KeyEsc:
		m.resetSSHCredentialPrompt()
		m.snapshot.Status = "SSH connection cancelled"
		return nil
	case tea.KeyEnter:
		if len(m.sshCredentialInput) == 0 {
			m.snapshot.Status = "Private key passphrase must not be empty"
			return nil
		}
		profile := m.sshCredentialProfile
		passphrase := string(m.sshCredentialInput)
		m.resetSSHCredentialPrompt()
		for index, item := range m.snapshot.SSHProfiles {
			if strings.EqualFold(item.Name, profile) {
				m.snapshot.SelectedSSH = index
				break
			}
		}
		return m.runSelectedSSHActionWithCredentials(
			"connect",
			cliSSHCredentials{IdentityPassphrase: passphrase},
		)
	case tea.KeyBackspace, tea.KeyDelete:
		if len(m.sshCredentialInput) > 0 {
			m.sshCredentialInput[len(m.sshCredentialInput)-1] = 0
			m.sshCredentialInput = m.sshCredentialInput[:len(m.sshCredentialInput)-1]
		}
	case tea.KeyCtrlU:
		for index := range m.sshCredentialInput {
			m.sshCredentialInput[index] = 0
		}
		m.sshCredentialInput = nil
	case tea.KeyRunes:
		for _, value := range message.Runes {
			if len(m.sshCredentialInput) >= 1024 {
				break
			}
			m.sshCredentialInput = append(m.sshCredentialInput, value)
		}
	}
	return nil
}

func (m *tuiModel) beginSSHForm(existing bool) {
	profile := cliSSHProfile{Port: 22}
	originalName := ""
	readOnly := false
	fingerprint := ""
	if existing {
		if m.snapshot.SelectedSSH < 0 ||
			m.snapshot.SelectedSSH >= len(m.snapshot.SSHProfiles) {
			m.snapshot.Status = "Select an SSH profile first"
			return
		}
		selected := m.snapshot.SSHProfiles[m.snapshot.SelectedSSH]
		originalName = selected.Name
		loaded, err := loadCLISSHProfile(originalName)
		if err != nil {
			m.snapshot.Status = "SSH edit failed: " + err.Error()
			return
		}
		profile = loaded
		profile.Options = append([]string(nil), loaded.Options...)
		connected, err := cliSSHProfileConnected(originalName)
		if err != nil {
			m.snapshot.Status = "SSH edit failed: " + err.Error()
			return
		}
		readOnly = connected
		fingerprint, err = cliSSHProfileFingerprint(loaded)
		if err != nil {
			m.snapshot.Status = "SSH edit failed: " + err.Error()
			return
		}
	}
	m.sshFormOpen = true
	m.sshFormExisting = existing
	m.sshFormReadOnly = readOnly
	m.sshFormOriginalName = originalName
	m.sshFormFingerprint = fingerprint
	m.sshForm = profile
	m.sshFormSelected = tuiSSHFormNameRow
	m.sshFormFieldEditing = false
	m.sshFormPassphraseChanged = false
	m.sshFormPassphraseCleared = false
	m.sshFormPassphraseConfirm = false
	m.sshFormPassphraseFirst = ""
	m.sshFormPasswordChanged = false
	m.sshFormPasswordCleared = false
	m.sshFormPasswordConfirm = false
	m.sshFormPasswordFirst = ""
	m.sshFormAddingOption = false
	m.refreshSSHFormIdentityState()
	if readOnly {
		m.snapshot.Status = "CONNECTED · READ ONLY · disconnect this SSH profile before editing"
	} else {
		m.snapshot.Status = "Local traffic → SSH host exit · ↑↓/Tab select · Enter edit/confirm · Esc cancel"
	}
}

func (m *tuiModel) resetSSHForm() {
	m.sshFormOpen = false
	m.sshFormExisting = false
	m.sshFormReadOnly = false
	m.sshFormOriginalName = ""
	m.sshFormFingerprint = ""
	m.sshForm = cliSSHProfile{}
	m.sshFormSelected = 0
	m.sshFormFieldEditing = false
	m.sshFormInput = nil
	m.sshFormCursor = 0
	m.sshFormSelectAll = false
	m.sshFormAddingOption = false
	m.sshFormPassphraseChanged = false
	m.sshFormPassphraseCleared = false
	m.sshFormPassphraseConfirm = false
	m.sshFormPassphraseFirst = ""
	m.sshFormPasswordChanged = false
	m.sshFormPasswordCleared = false
	m.sshFormPasswordConfirm = false
	m.sshFormPasswordFirst = ""
	m.sshFormIdentityKind = cliSSHIdentityNone
	m.sshFormIdentityError = ""
}

func (m *tuiModel) refreshSSHFormIdentityState() {
	m.sshFormIdentityKind = cliSSHIdentityNone
	m.sshFormIdentityError = ""
	if strings.TrimSpace(m.sshForm.Identity) == "" {
		return
	}
	kind, err := inspectCLISSHIdentity(
		m.sshForm.Identity,
		m.sshForm.IdentityPassphrase,
	)
	m.sshFormIdentityKind = kind
	if err != nil {
		m.sshFormIdentityError = err.Error()
	}
}

func (m *tuiModel) sshFormAddOptionRow() int {
	return tuiSSHFormOptionStartRow + len(m.sshForm.Options)
}

func (m *tuiModel) sshFormSaveRow() int {
	return m.sshFormAddOptionRow() + 1
}

func (m *tuiModel) sshFormDeleteRow() int {
	if !m.sshFormExisting {
		return -1
	}
	return m.sshFormSaveRow() + 1
}

func (m *tuiModel) sshFormCancelRow() int {
	if m.sshFormExisting {
		return m.sshFormSaveRow() + 2
	}
	return m.sshFormSaveRow() + 1
}

func (m *tuiModel) sshFormRowCount() int {
	return m.sshFormCancelRow() + 1
}

func (m *tuiModel) beginSSHFormFieldEdit() {
	if m.sshFormReadOnly {
		m.snapshot.Status = "CONNECTED · READ ONLY · disconnect this SSH profile before editing"
		return
	}
	if m.sshFormSelected == tuiSSHFormPassphraseRow {
		switch {
		case strings.TrimSpace(m.sshForm.Identity) == "":
			m.snapshot.Status = "Select Identity(private key) before setting a key passphrase"
			return
		case m.sshFormIdentityKind == cliSSHIdentityUnencrypted:
			m.snapshot.Status = "This private key is not encrypted; no passphrase is required"
			return
		}
	}
	value := ""
	switch m.sshFormSelected {
	case tuiSSHFormNameRow:
		value = m.sshForm.Name
	case tuiSSHFormUsernameRow:
		value = m.sshForm.Username
	case tuiSSHFormHostRow:
		value = m.sshForm.Host
	case tuiSSHFormJumpRow:
		value = m.sshForm.Jump
	case tuiSSHFormPortRow:
		value = strconv.Itoa(m.sshForm.Port)
	case tuiSSHFormLocalPortRow:
		value = "auto"
		if m.sshForm.LocalPort > 0 {
			value = strconv.Itoa(m.sshForm.LocalPort)
		}
	case tuiSSHFormIdentityRow:
		value = m.sshForm.Identity
	case tuiSSHFormPassphraseRow:
		m.sshFormPassphraseConfirm = false
		m.sshFormPassphraseFirst = ""
	case tuiSSHFormPasswordRow:
		m.sshFormPasswordConfirm = false
		m.sshFormPasswordFirst = ""
	default:
		optionIndex := m.sshFormSelected - tuiSSHFormOptionStartRow
		if optionIndex < 0 || optionIndex >= len(m.sshForm.Options) {
			return
		}
		value = m.sshForm.Options[optionIndex]
	}
	m.sshFormFieldEditing = true
	m.sshFormInput = []rune(value)
	m.sshFormCursor = len(m.sshFormInput)
	m.sshFormSelectAll = value != ""
	m.snapshot.Status = "Editing SSH field · Enter confirm · Esc cancel"
}

func (m *tuiModel) cancelSSHFormFieldEdit() {
	if m.sshFormAddingOption {
		optionIndex := m.sshFormSelected - tuiSSHFormOptionStartRow
		if optionIndex >= 0 && optionIndex < len(m.sshForm.Options) {
			m.sshForm.Options = append(
				m.sshForm.Options[:optionIndex],
				m.sshForm.Options[optionIndex+1:]...,
			)
		}
	}
	m.sshFormFieldEditing = false
	m.sshFormInput = nil
	m.sshFormCursor = 0
	m.sshFormSelectAll = false
	m.sshFormAddingOption = false
	m.sshFormPassphraseConfirm = false
	m.sshFormPassphraseFirst = ""
	m.sshFormPasswordConfirm = false
	m.sshFormPasswordFirst = ""
}

func (m *tuiModel) commitSSHFormField() bool {
	value := string(m.sshFormInput)
	switch m.sshFormSelected {
	case tuiSSHFormNameRow:
		m.sshForm.Name = strings.TrimSpace(value)
	case tuiSSHFormUsernameRow:
		m.sshForm.Username = strings.TrimSpace(value)
	case tuiSSHFormHostRow:
		m.sshForm.Host = strings.TrimSpace(value)
	case tuiSSHFormJumpRow:
		m.sshForm.Jump = strings.TrimSpace(value)
		if err := validateCLISSHJump(m.sshForm.Jump); err != nil {
			m.snapshot.Status = err.Error()
			return false
		}
	case tuiSSHFormPortRow:
		port, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || port < 1 || port > 65535 {
			m.snapshot.Status = "SSH port must be between 1 and 65535"
			return false
		}
		m.sshForm.Port = port
	case tuiSSHFormLocalPortRow:
		port, err := parseCLISSHLocalPort(value)
		if err != nil {
			m.snapshot.Status = err.Error()
			return false
		}
		m.sshForm.LocalPort = port
	case tuiSSHFormIdentityRow:
		m.sshForm.Identity = strings.TrimSpace(value)
		m.refreshSSHFormIdentityState()
	case tuiSSHFormPassphraseRow:
		if value == "" {
			m.snapshot.Status = "Private key passphrase must not be empty; press c outside editing to clear it"
			return false
		}
		if !m.sshFormPassphraseConfirm {
			m.sshFormPassphraseFirst = value
			m.sshFormPassphraseConfirm = true
			m.sshFormInput = nil
			m.sshFormCursor = 0
			m.sshFormSelectAll = false
			m.snapshot.Status = "Confirm the private key passphrase · Enter confirm · Esc cancel"
			return false
		}
		if value != m.sshFormPassphraseFirst {
			m.sshFormPassphraseConfirm = false
			m.sshFormPassphraseFirst = ""
			m.sshFormInput = nil
			m.sshFormCursor = 0
			m.snapshot.Status = "Passphrases do not match; enter the private key passphrase again"
			return false
		}
		m.sshForm.IdentityPassphrase = value
		m.sshFormPassphraseChanged = true
		m.sshFormPassphraseCleared = false
		m.refreshSSHFormIdentityState()
	case tuiSSHFormPasswordRow:
		if value == "" {
			m.snapshot.Status = "SSH password must not be empty; press c outside editing to clear it"
			return false
		}
		if !m.sshFormPasswordConfirm {
			m.sshFormPasswordFirst = value
			m.sshFormPasswordConfirm = true
			m.sshFormInput = nil
			m.sshFormCursor = 0
			m.sshFormSelectAll = false
			m.snapshot.Status = "Confirm the new SSH password · Enter confirm · Esc cancel"
			return false
		}
		if value != m.sshFormPasswordFirst {
			m.sshFormPasswordConfirm = false
			m.sshFormPasswordFirst = ""
			m.sshFormInput = nil
			m.sshFormCursor = 0
			m.snapshot.Status = "SSH passwords do not match; enter the new password again"
			return false
		}
		m.sshForm.Password = value
		m.sshFormPasswordChanged = true
		m.sshFormPasswordCleared = false
	default:
		optionIndex := m.sshFormSelected - tuiSSHFormOptionStartRow
		value = strings.TrimSpace(value)
		if optionIndex < 0 || optionIndex >= len(m.sshForm.Options) {
			return false
		}
		if err := validateCLISSHOption(value); err != nil {
			m.snapshot.Status = "SSH option invalid: " + err.Error()
			return false
		}
		m.sshForm.Options[optionIndex] = value
		m.sshFormAddingOption = false
	}
	m.cancelSSHFormFieldEdit()
	m.snapshot.Status = "SSH profile form · select Save to commit"
	return true
}

func (m *tuiModel) handleSSHForm(message tea.KeyMsg) tea.Cmd {
	if m.sshFormFieldEditing {
		return m.handleSSHFormFieldInput(message)
	}
	if message.Type == tea.KeyRunes && len(message.Runes) == 1 && message.Runes[0] == 'q' {
		m.resetSSHForm()
		return m.handleKey(tuiKeyQuit)
	}
	if m.busy {
		if message.Type == tea.KeyCtrlC {
			m.resetSSHForm()
			return m.handleKey(tuiKeyInterrupt)
		}
		m.snapshot.Status = "SSH profile save is still running"
		return nil
	}
	switch message.Type {
	case tea.KeyCtrlC:
		m.resetSSHForm()
		return m.handleKey(tuiKeyInterrupt)
	case tea.KeyEsc:
		m.resetSSHForm()
		m.snapshot.Status = "SSH profile changes cancelled"
		return nil
	case tea.KeyUp, tea.KeyShiftTab:
		m.sshFormSelected = wrapTUIIndex(
			m.sshFormSelected,
			-1,
			m.sshFormRowCount(),
		)
		return nil
	case tea.KeyDown, tea.KeyTab:
		m.sshFormSelected = wrapTUIIndex(
			m.sshFormSelected,
			1,
			m.sshFormRowCount(),
		)
		return nil
	case tea.KeyEnter:
		return m.activateSSHFormRow()
	case tea.KeyDelete:
		return m.deleteSelectedSSHFormOption()
	case tea.KeyRunes:
		if len(message.Runes) == 1 {
			switch message.Runes[0] {
			case 'c':
				if m.sshFormSelected == tuiSSHFormPassphraseRow ||
					m.sshFormSelected == tuiSSHFormPasswordRow {
					if m.sshFormReadOnly {
						m.snapshot.Status = "CONNECTED · READ ONLY · disconnect this SSH profile before editing"
						return nil
					}
					if m.sshFormSelected == tuiSSHFormPassphraseRow {
						m.sshForm.IdentityPassphrase = ""
						m.sshFormPassphraseChanged = false
						m.sshFormPassphraseCleared = true
						m.refreshSSHFormIdentityState()
						m.snapshot.Status = "Saved private key passphrase will be cleared when the form is saved"
					} else {
						m.sshForm.Password = ""
						m.sshFormPasswordChanged = false
						m.sshFormPasswordCleared = true
						m.snapshot.Status = "Saved SSH password will be cleared when the form is saved"
					}
				}
			case 'x':
				return m.deleteSelectedSSHFormOption()
			}
		}
	}
	return nil
}

func (m *tuiModel) activateSSHFormRow() tea.Cmd {
	if m.sshFormReadOnly {
		switch m.sshFormSelected {
		case m.sshFormDeleteRow():
			m.beginSSHDeleteConfirmForName(m.sshFormOriginalName)
		case m.sshFormCancelRow():
			m.resetSSHForm()
			m.snapshot.Status = "SSH profile details closed"
		default:
			m.snapshot.Status = "CONNECTED · READ ONLY · disconnect this SSH profile before editing"
		}
		return nil
	}
	switch m.sshFormSelected {
	case m.sshFormAddOptionRow():
		m.sshForm.Options = append(m.sshForm.Options, "")
		m.sshFormSelected = tuiSSHFormOptionStartRow + len(m.sshForm.Options) - 1
		m.sshFormAddingOption = true
		m.beginSSHFormFieldEdit()
		return nil
	case m.sshFormSaveRow():
		return m.saveSSHForm()
	case m.sshFormDeleteRow():
		m.beginSSHDeleteConfirmForName(m.sshFormOriginalName)
		return nil
	case m.sshFormCancelRow():
		m.resetSSHForm()
		m.snapshot.Status = "SSH profile changes cancelled"
		return nil
	default:
		m.beginSSHFormFieldEdit()
		return nil
	}
}

func (m *tuiModel) deleteSelectedSSHFormOption() tea.Cmd {
	if m.sshFormReadOnly {
		m.snapshot.Status = "CONNECTED · READ ONLY · disconnect this SSH profile before editing"
		return nil
	}
	optionIndex := m.sshFormSelected - tuiSSHFormOptionStartRow
	if optionIndex < 0 || optionIndex >= len(m.sshForm.Options) {
		return nil
	}
	m.sshForm.Options = append(
		m.sshForm.Options[:optionIndex],
		m.sshForm.Options[optionIndex+1:]...,
	)
	if m.sshFormSelected >= m.sshFormRowCount() {
		m.sshFormSelected = m.sshFormRowCount() - 1
	}
	m.snapshot.Status = "SSH option removed from the form; select Save to commit"
	return nil
}

func (m *tuiModel) saveSSHForm() tea.Cmd {
	if m.sshFormReadOnly {
		m.snapshot.Status = "CONNECTED · READ ONLY · disconnect this SSH profile before editing"
		return nil
	}
	profile := m.sshForm
	profile.Name = strings.TrimSpace(profile.Name)
	profile.Username = strings.TrimSpace(profile.Username)
	profile.Host = strings.TrimSpace(profile.Host)
	profile.Jump = strings.TrimSpace(profile.Jump)
	profile.Identity = strings.TrimSpace(profile.Identity)
	profile = normalizeCLISSHProfile(profile)
	if profile.Identity != "" {
		kind, err := inspectCLISSHIdentity(
			profile.Identity,
			profile.IdentityPassphrase,
		)
		if err != nil {
			m.snapshot.Status = "SSH private key invalid: " + err.Error()
			return nil
		}
		if kind == cliSSHIdentityUnencrypted {
			profile.IdentityPassphrase = ""
		}
	}
	if err := validateCLISSHProfile(profile); err != nil {
		m.snapshot.Status = "SSH profile invalid: " + err.Error()
		return nil
	}
	existing := m.sshFormExisting
	originalName := m.sshFormOriginalName
	expectedFingerprint := m.sshFormFingerprint
	m.busy = true
	m.snapshot.Status = "Saving SSH profile " + profile.Name + "..."
	return func() tea.Msg {
		var err error
		action := "add"
		if existing {
			action = "edit"
			err = replaceCLISSHProfile(
				originalName,
				expectedFingerprint,
				profile,
			)
		} else {
			err = addCLISSHProfile(profile)
		}
		status := ""
		if err == nil {
			status = "SSH profile " + profile.Name + " saved"
		}
		return tuiSSHCommandResultMsg{
			action:       action,
			status:       status,
			selectedName: profile.Name,
			err:          err,
		}
	}
}

func (m *tuiModel) handleSSHFormFieldInput(message tea.KeyMsg) tea.Cmd {
	switch message.Type {
	case tea.KeyCtrlC:
		m.resetSSHForm()
		return m.handleKey(tuiKeyInterrupt)
	case tea.KeyEsc:
		m.cancelSSHFormFieldEdit()
		m.snapshot.Status = "SSH field edit cancelled"
	case tea.KeyEnter:
		m.commitSSHFormField()
	case tea.KeyBackspace, tea.KeyCtrlH:
		if m.sshFormSelectAll {
			m.clearSSHFormInput()
		} else if m.sshFormCursor > 0 {
			m.sshFormInput = append(
				m.sshFormInput[:m.sshFormCursor-1],
				m.sshFormInput[m.sshFormCursor:]...,
			)
			m.sshFormCursor--
		}
	case tea.KeyDelete:
		if m.sshFormSelectAll {
			m.clearSSHFormInput()
		} else if m.sshFormCursor < len(m.sshFormInput) {
			m.sshFormInput = append(
				m.sshFormInput[:m.sshFormCursor],
				m.sshFormInput[m.sshFormCursor+1:]...,
			)
		}
	case tea.KeyLeft:
		if m.sshFormSelectAll {
			m.sshFormCursor = 0
			m.sshFormSelectAll = false
		} else if m.sshFormCursor > 0 {
			m.sshFormCursor--
		}
	case tea.KeyRight:
		if m.sshFormSelectAll {
			m.sshFormCursor = len(m.sshFormInput)
			m.sshFormSelectAll = false
		} else if m.sshFormCursor < len(m.sshFormInput) {
			m.sshFormCursor++
		}
	case tea.KeyHome, tea.KeyCtrlA:
		m.sshFormCursor = 0
		m.sshFormSelectAll = false
	case tea.KeyEnd, tea.KeyCtrlE:
		m.sshFormCursor = len(m.sshFormInput)
		m.sshFormSelectAll = false
	case tea.KeyCtrlU:
		m.clearSSHFormInput()
	case tea.KeyRunes:
		limit := 4096
		if m.sshFormSelected == tuiSSHFormNameRow {
			limit = 64
		} else if m.sshFormSelected == tuiSSHFormUsernameRow {
			limit = 128
		} else if m.sshFormSelected == tuiSSHFormHostRow {
			limit = 512
		} else if m.sshFormSelected == tuiSSHFormPortRow {
			limit = 5
		} else if m.sshFormSelected == tuiSSHFormLocalPortRow {
			limit = 5
		}
		if m.sshFormSelectAll {
			m.clearSSHFormInput()
		}
		for _, value := range message.Runes {
			if len(m.sshFormInput) >= limit {
				break
			}
			if m.sshFormSelected == tuiSSHFormPortRow && (value < '0' || value > '9') {
				continue
			}
			if m.sshFormSelected == tuiSSHFormLocalPortRow &&
				!((value >= '0' && value <= '9') ||
					(value >= 'a' && value <= 'z') ||
					(value >= 'A' && value <= 'Z')) {
				continue
			}
			m.sshFormInput = append(m.sshFormInput, 0)
			copy(
				m.sshFormInput[m.sshFormCursor+1:],
				m.sshFormInput[m.sshFormCursor:],
			)
			m.sshFormInput[m.sshFormCursor] = value
			m.sshFormCursor++
		}
	}
	return nil
}

func (m *tuiModel) clearSSHFormInput() {
	m.sshFormInput = nil
	m.sshFormCursor = 0
	m.sshFormSelectAll = false
}

func (m *tuiModel) beginSSHDeleteConfirm() {
	if m.snapshot.SelectedSSH < 0 ||
		m.snapshot.SelectedSSH >= len(m.snapshot.SSHProfiles) {
		m.snapshot.Status = "Select an SSH profile first"
		return
	}
	m.beginSSHDeleteConfirmForName(
		m.snapshot.SSHProfiles[m.snapshot.SelectedSSH].Name,
	)
}

func (m *tuiModel) beginSSHDeleteConfirmForName(name string) {
	m.sshDeleteName = name
	m.sshDeleteConfirmOpen = true
	m.snapshot.Status = "Confirm SSH profile deletion · Enter confirm · Esc cancel"
}

func (m *tuiModel) handleSSHDeleteConfirm(message tea.KeyMsg) tea.Cmd {
	switch message.Type {
	case tea.KeyCtrlC:
		m.sshDeleteConfirmOpen = false
		m.sshDeleteName = ""
		return m.handleKey(tuiKeyInterrupt)
	case tea.KeyEsc:
		m.sshDeleteConfirmOpen = false
		m.sshDeleteName = ""
		m.snapshot.Status = "SSH profile deletion cancelled"
	case tea.KeyEnter:
		name := m.sshDeleteName
		m.sshDeleteConfirmOpen = false
		m.sshDeleteName = ""
		if m.sshFormOpen {
			m.resetSSHForm()
		}
		m.busy = true
		m.snapshot.Status = "Deleting SSH profile " + name + "..."
		return func() tea.Msg {
			err := deleteCLISSHProfile(name)
			status := ""
			if err == nil {
				status = "SSH profile " + name + " deleted"
			}
			return tuiSSHCommandResultMsg{
				action: "delete",
				status: status,
				err:    err,
			}
		}
	case tea.KeyRunes:
		if len(message.Runes) == 1 && message.Runes[0] == 'q' {
			m.sshDeleteConfirmOpen = false
			m.sshDeleteName = ""
			return m.handleKey(tuiKeyQuit)
		}
	}
	return nil
}

func (m *tuiModel) beginProfileDeleteConfirm() {
	if m.snapshot.SelectedRow < 0 ||
		m.snapshot.SelectedRow >= len(m.snapshot.Profiles) {
		m.snapshot.Status = "Select a saved Profile before deleting"
		return
	}
	profile := m.snapshot.Profiles[m.snapshot.SelectedRow]
	if profile.Current {
		m.snapshot.Status = "Cannot delete the active Profile; activate another one first"
		return
	}
	if m.service == nil {
		m.snapshot.Status = "Profile deletion requires the managed Backend"
		return
	}
	m.profileDeleteOpen = true
	m.profileDeletePath = profile.Path
	m.profileDeleteName = profile.Name
	m.profileDeleteKind = "local"
	if profile.SubscriptionURL != "" {
		m.profileDeleteKind = "subscription"
	}
	m.snapshot.Status = "Confirm Profile deletion · Enter confirm · Esc cancel"
}

func (m *tuiModel) resetProfileDeleteConfirm() {
	m.profileDeleteOpen = false
	m.profileDeletePath = ""
	m.profileDeleteName = ""
	m.profileDeleteKind = ""
}

func (m *tuiModel) handleProfileDeleteConfirm(message tea.KeyMsg) tea.Cmd {
	switch message.Type {
	case tea.KeyCtrlC:
		m.resetProfileDeleteConfirm()
		return m.handleKey(tuiKeyInterrupt)
	case tea.KeyEsc:
		m.resetProfileDeleteConfirm()
		m.snapshot.Status = "Profile deletion cancelled"
	case tea.KeyEnter:
		path := m.profileDeletePath
		name := m.profileDeleteName
		selected := m.snapshot.SelectedRow
		service := m.service
		m.resetProfileDeleteConfirm()
		return m.startOperation(func(state *tuiOperationState) {
			if service == nil {
				state.snapshot.Status = "Profile deletion requires the managed Backend"
				return
			}
			if !prepareTUIBackendRevision(state, service) {
				return
			}
			status, err := service.deleteProfile(path, state.backendRevision)
			if err != nil {
				state.snapshot.Status = "Profile deletion failed: " + err.Error()
				return
			}
			applyTUIOperationServiceStatus(state, status)
			refreshTUIProfiles(&state.snapshot, state.paths)
			if len(state.snapshot.Profiles) == 0 {
				state.snapshot.SelectedRow = tuiProfileImportSubscriptionRow
			} else {
				state.snapshot.SelectedRow = clampTUISelection(
					selected,
					len(state.snapshot.Profiles),
				)
			}
			state.snapshot.Status = "Profile deleted: " + name
		})
	case tea.KeyRunes:
		if len(message.Runes) == 1 && message.Runes[0] == 'q' {
			m.resetProfileDeleteConfirm()
			return m.handleKey(tuiKeyQuit)
		}
	}
	return nil
}
