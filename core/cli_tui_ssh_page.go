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
	index := m.snapshot.SelectedSSH
	if m.snapshot.SSHDashboardFocus {
		index = m.sshDetailProfileIndex()
	}
	if index < 0 || index >= len(m.snapshot.SSHProfiles) {
		m.snapshot.setStatus(newTUIMessage("ui.d7d54ef0ad99"))
		return nil
	}
	if m.busy {
		m.snapshot.setStatus(newTUIMessage("ui.c90e0573178b"))
		return nil
	}
	name := m.snapshot.SSHProfiles[index].Name
	operationContext := m.sshOperationContext
	if operationContext == nil {
		operationContext = context.Background()
	}
	m.busy = true
	m.snapshot.setStatus(newTUIMessage("ui.013120ffa8f1", action, name))
	return func() tea.Msg {
		message := tuiSSHCommandResultMsg{action: action, selectedName: name}
		switch action {
		case "connect":
			state, alreadyConnected, err := connectCLISSHProfileWithCredentialsContext(
				operationContext,
				name,
				credentials,
			)
			message.err = err
			if err == nil {
				prefix := "connected"
				if alreadyConnected {
					prefix = "already connected"
				} else if cliSSHTunnelIsAttached(state.Kind) {
					prefix = "attached"
				}
				message.status = fmt.Sprintf(
					"SSH %s %s · SOCKS5 127.0.0.1:%d",
					state.Name,
					prefix,
					state.Port,
				)
				key := "ssh.connected"
				if alreadyConnected {
					key = "ssh.already_connected"
				} else if cliSSHTunnelIsAttached(state.Kind) {
					key = "ssh.attached"
				}
				message.text = newTUIMessage(key, state.Name, state.Port)
			}
		case "attach":
			state, alreadyConnected, err := attachCLISSHProfileContext(operationContext, name)
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
				key := "ssh.attached"
				if alreadyConnected {
					key = "ssh.already_connected"
				}
				message.text = newTUIMessage(key, state.Name, state.Port)
			}
		case "disconnect":
			state, disconnected, err := disconnectCLISSHProfile(name)
			message.err = err
			if err == nil {
				if disconnected {
					message.status = "SSH " + state.Name + " disconnected"
					message.text = newTUIMessage("ssh.disconnected", state.Name)
				} else {
					message.status = "No persistent SSH tunnel is open"
					message.text = newTUIMessage("ssh.no_tunnel")
				}
			}
		case "test":
			state, latency, err := testCLISSHProfileContext(operationContext, name)
			message.err = err
			if err == nil {
				message.status = fmt.Sprintf(
					"SSH %s ready · SOCKS5 127.0.0.1:%d · handshake %s",
					state.Name,
					state.Port,
					latency,
				)
				message.text = newTUIMessage("ssh.ready", state.Name, state.Port, latency)
			}
		default:
			message.err = fmt.Errorf("unsupported TUI SSH action %q", action)
		}
		return message
	}
}

func (m *tuiModel) beginSSHCapture() tea.Cmd {
	if m.snapshot.FocusSidebar {
		m.snapshot.setStatus(newTUIMessage("ui.f08d600f6d61"))
		return nil
	}
	profiles := append([]tuiSSHProfile(nil), m.snapshot.SSHProfiles...)
	current := m.selectedSSHName()
	m.sshCaptureGeneration++
	generation := m.sshCaptureGeneration
	m.sshCaptureOpen = true
	m.sshCaptureNames = nil
	m.sshCaptureCandidates = nil
	m.sshCaptureOptions = []string{"Scanning…"}
	m.sshCaptureSelected = 0
	return func() tea.Msg {
		result := discoverTUISSHCapture(profiles, current)
		result.generation = generation
		return result
	}
}

func applyTUISSHCaptureResult(snapshot *tuiSnapshot, candidates []cliSSHCaptureCandidate) {
	snapshot.SSHCaptureKnown = true
	snapshot.SSHCaptureFound = len(candidates)
	inbound, local := 0, 0
	for _, candidate := range candidates {
		if candidate.Kind == cliSSHCaptureReverseSOCKSKind {
			inbound++
			continue
		}
		local++
	}
	snapshot.SSHCaptureInbound = inbound
	snapshot.SSHCaptureLocal = local
}

func discoverTUISSHCapture(profiles []tuiSSHProfile, current string) tuiSSHCaptureResultMsg {
	converted := make([]cliSSHProfile, 0, len(profiles))
	for _, profile := range profiles {
		converted = append(converted, normalizeCLISSHProfile(cliSSHProfile{
			Name:     profile.Name,
			Username: profile.Username,
			Host:     profile.Host,
			Port:     profile.Port,
			Jump:     profile.Jump,
			Options:  append([]string(nil), profile.Options...),
		}))
	}
	candidates := discoverCLICaptureCandidatesWithProfiles(converted)
	names := make([]string, 0, len(candidates))
	options := make([]string, 0, len(candidates))
	selected := 0
	for _, candidate := range candidates {
		if current != "" && strings.EqualFold(candidate.Name, current) {
			selected = len(names)
		}
		names = append(names, candidate.Name)
		options = append(options, candidate.Label)
	}
	return tuiSSHCaptureResultMsg{
		names:      names,
		options:    options,
		selected:   selected,
		candidates: candidates,
		hint:       formatCLICaptureEmptyHint(),
	}
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
			m.sshCaptureSelected >= len(m.sshCaptureCandidates) {
			m.sshCaptureOpen = false
			m.snapshot.setStatus(newTUIMessage("ui.f684c73cba75", formatCLICaptureEmptyHint()))
			return nil
		}
		return m.attachSSHCaptureCandidate(m.sshCaptureCandidates[m.sshCaptureSelected])
	case tuiKeyBack:
		m.sshCaptureOpen = false
		m.snapshot.setStatus(newTUIMessage("ui.2e529a253060"))
	case tuiKeyQuit, tuiKeyInterrupt:
		m.sshCaptureOpen = false
		return m.handleKey(key)
	}
	return nil
}

func (m *tuiModel) attachSSHCaptureCandidate(candidate cliSSHCaptureCandidate) tea.Cmd {
	m.sshCaptureOpen = false
	m.busy = true
	m.snapshot.setStatus(newTUIMessage("ui.f34a03e6fb1c", candidate.Name))
	operationContext := m.sshOperationContext
	if operationContext == nil {
		operationContext = context.Background()
	}
	return func() tea.Msg {
		state, already, err := captureCLISSHCandidateContext(operationContext, candidate)
		message := tuiSSHCommandResultMsg{
			action:       "attach",
			selectedName: candidate.Name,
			err:          err,
		}
		if err == nil {
			message.selectedName = state.Name
			label := "attached"
			if already {
				label = "already attached"
			}
			message.status = fmt.Sprintf(
				"SSH %s %s · SOCKS5 127.0.0.1:%d",
				state.Name,
				label,
				state.Port,
			)
			key := "ssh.attached"
			if already {
				key = "ssh.already_attached"
			}
			message.text = newTUIMessage(key, state.Name, state.Port)
		}
		return message
	}
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

func (m *tuiModel) sshDetailProfileIndex() int {
	if m.snapshot.SSHDetailName == "" {
		return -1
	}
	return findTUISSHProfile(m.snapshot.SSHProfiles, m.snapshot.SSHDetailName)
}

func (m *tuiModel) toggleSelectedSSHDefault() tea.Cmd {
	if m.snapshot.FocusSidebar || m.snapshot.SSHDashboardFocus {
		m.snapshot.setStatus(newTUIMessage("ui.22ee1096c976"))
		return nil
	}
	if m.snapshot.SelectedSSH < 0 || m.snapshot.SelectedSSH >= len(m.snapshot.SSHProfiles) {
		m.snapshot.setStatus(newTUIMessage("ui.d7d54ef0ad99"))
		return nil
	}
	profile := m.snapshot.SSHProfiles[m.snapshot.SelectedSSH]
	name := profile.Name
	clear := profile.Default
	m.busy = true
	m.snapshot.setStatus(newTUIMessage("ui.c88cd9071375"))
	return func() tea.Msg {
		message := tuiSSHCommandResultMsg{action: "default", selectedName: name}
		if clear {
			message.err = setCLISSHDefault("")
			if message.err == nil {
				message.status = "Default SSH profile cleared"
				message.text = newTUIMessage("ssh.default_cleared")
			}
			return message
		}
		message.err = setCLISSHDefault(name)
		if message.err == nil {
			message.status = "Default SSH profile " + name
			message.text = newTUIMessage("ssh.default", name)
		}
		return message
	}
}

func (m *tuiModel) resetSelectedSSHMetrics() {
	m.sshDetailGeneration++
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

func (m *tuiModel) isCurrentSSHResult(name string, generation uint64) bool {
	return generation == m.sshDetailGeneration &&
		strings.EqualFold(m.snapshot.SSHDetailName, name)
}

func (m *tuiModel) refreshSelectedSSHDashboard() tea.Cmd {
	if m.snapshot.Page != tuiPageSSH || m.sshDetailProfileIndex() < 0 {
		return nil
	}
	profile := m.snapshot.SSHProfiles[m.sshDetailProfileIndex()]
	if !profile.Connected || !profile.Ready {
		return nil
	}
	return m.pollSelectedSSHRelay()
}

func (m *tuiModel) refreshSelectedSSHProxyIPs() tea.Cmd {
	if m.snapshot.Page != tuiPageSSH || m.sshDetailProfileIndex() < 0 {
		return nil
	}
	profile := m.snapshot.SSHProfiles[m.sshDetailProfileIndex()]
	if !profile.Connected || !profile.Ready {
		m.snapshot.setStatus(newTUIMessage("ui.f221ae231ed3"))
		return nil
	}
	m.sshProxyRefreshSequence++
	if profile.SocksOnly {
		return m.refreshSelectedSSHNetwork()
	}
	return tea.Batch(m.refreshSelectedSSHNetwork(), m.refreshSelectedSSHDirectProbe())
}

func (m *tuiModel) pollSelectedSSHRelay() tea.Cmd {
	if m.snapshot.Page != tuiPageSSH || m.sshDetailProfileIndex() < 0 {
		return nil
	}
	profile := m.snapshot.SSHProfiles[m.sshDetailProfileIndex()]
	if !profile.Connected || !profile.Ready {
		return nil
	}
	name := profile.Name
	generation := m.sshDetailGeneration
	if name == "" {
		return nil
	}
	return func() tea.Msg {
		message := tuiSSHRelayStatsMsg{name: name, generation: generation, at: time.Now()}
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
	index := m.sshDetailProfileIndex()
	if index < 0 {
		return "", nil, nil, errors.New("select an SSH profile first")
	}
	profile := m.snapshot.SSHProfiles[index]
	name := profile.Name
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
			m.snapshot.setStatus(newTUIMessage("ui.6a9f7c4234be", err.Error()))
		} else {
			m.snapshot.SSHNetwork = info
			m.snapshot.setStatus(newTUIMessage("ui.aef5f47e988a", err.Error()))
		}
		return nil
	}
	if direct {
		m.snapshot.SSHDirectNetwork.Loading = true
	} else {
		m.snapshot.SSHNetwork.Loading = true
	}
	generation := m.sshDetailGeneration
	sequence := m.sshProxyRefreshSequence
	return func() tea.Msg {
		defer closeClient()
		return tuiSSHNetworkResultMsg{
			name:       name,
			generation: generation,
			sequence:   sequence,
			direct:     direct,
			info:       detectTUINetworkWithClient(client, "SSH · "+name),
		}
	}
}

func (m *tuiModel) testSelectedSSHDelay() tea.Cmd {
	return m.testSelectedSSHDelayFor(false)
}

func (m *tuiModel) testSelectedSSHDelayFor(direct bool) tea.Cmd {
	if direct && !m.snapshot.SSHDirectProbe.DirectAllowed {
		m.snapshot.setStatus(newTUIMessage("ui.f65b91e54bf3", cliDisplayValue(m.snapshot.SSHDirectProbe.Reason)))
		return nil
	}
	name, client, closeClient, err := m.selectedSSHHTTPClient()
	if err != nil {
		m.snapshot.setStatus(newTUIMessage("ui.3a60dd9f3513", err.Error()))
		return nil
	}
	if direct {
		m.snapshot.SSHDirectDelay = tuiDelayResult{Testing: true}
	} else {
		m.snapshot.SSHDelay = tuiDelayResult{Testing: true}
	}
	testURL := m.tuiDelayTestURL()
	generation := m.sshDetailGeneration
	index := tuiSSHProbeIndex(direct)
	m.sshDelaySequence[index]++
	sequence := m.sshDelaySequence[index]
	return func() tea.Msg {
		defer closeClient()
		result, testErr := runTUIRouteDelayTest(context.Background(), client, testURL)
		return tuiSSHDelayResultMsg{name: name, generation: generation, direct: direct, sequence: sequence, result: result, err: testErr}
	}
}

func (m *tuiModel) testSelectedSSHSpeed() tea.Cmd {
	return m.testSelectedSSHSpeedFor(false)
}

func (m *tuiModel) testSelectedSSHSpeedFor(direct bool) tea.Cmd {
	if direct && !m.snapshot.SSHDirectProbe.DirectAllowed {
		m.snapshot.setStatus(newTUIMessage("ui.f65b91e54bf3", cliDisplayValue(m.snapshot.SSHDirectProbe.Reason)))
		return nil
	}
	name, client, closeClient, err := m.selectedSSHHTTPClient()
	if err != nil {
		m.snapshot.setStatus(newTUIMessage("ui.52728cefb85d", err.Error()))
		return nil
	}
	if direct {
		m.snapshot.SSHDirectSpeed = tuiSpeedResult{Testing: true}
	} else {
		m.snapshot.SSHSpeed = tuiSpeedResult{Testing: true}
	}
	generation := m.sshDetailGeneration
	index := tuiSSHProbeIndex(direct)
	m.sshSpeedSequence[index]++
	sequence := m.sshSpeedSequence[index]
	return func() tea.Msg {
		defer closeClient()
		result, testErr := runTUIDownloadSpeedTest(context.Background(), client)
		return tuiSSHSpeedResultMsg{name: name, generation: generation, direct: direct, sequence: sequence, result: result, err: testErr}
	}
}

func tuiSSHProbeIndex(direct bool) int {
	if direct {
		return 1
	}
	return 0
}

func (m *tuiModel) refreshSelectedSSHDirectProbe() tea.Cmd {
	index := m.sshDetailProfileIndex()
	if index < 0 {
		return nil
	}
	profile := m.snapshot.SSHProfiles[index]
	name := profile.Name
	generation := m.sshDetailGeneration
	sequence := m.sshProxyRefreshSequence
	if !profile.Connected || !profile.Ready {
		return nil
	}
	return func() tea.Msg {
		message := tuiSSHDirectProbeResultMsg{name: name, generation: generation, sequence: sequence}
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
	m.snapshot.setStatus(newTUIMessage("ui.e0a5e9c91b35"))
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
		m.snapshot.setStatus(newTUIMessage("ui.c659ad238be8"))
		return nil
	case tea.KeyEnter:
		if len(m.sshCredentialInput) == 0 {
			m.snapshot.setStatus(newTUIMessage("ui.add06c08c882"))
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
			m.sshCredentialInput = m.sshCredentialInput[:tuiPreviousGrapheme(m.sshCredentialInput, len(m.sshCredentialInput))]
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
			m.snapshot.setStatus(newTUIMessage("ui.d7d54ef0ad99"))
			return
		}
		selected := m.snapshot.SSHProfiles[m.snapshot.SelectedSSH]
		originalName = selected.Name
		loaded, err := loadCLISSHProfile(originalName)
		if err != nil {
			m.snapshot.setStatus(newTUIMessage("ui.2b046c8d929c", err.Error()))
			return
		}
		profile = loaded
		profile.Options = append([]string(nil), loaded.Options...)
		connected, err := cliSSHProfileConnected(originalName)
		if err != nil {
			m.snapshot.setStatus(newTUIMessage("ui.2b046c8d929c", err.Error()))
			return
		}
		readOnly = connected
		fingerprint, err = cliSSHProfileFingerprint(loaded)
		if err != nil {
			m.snapshot.setStatus(newTUIMessage("ui.2b046c8d929c", err.Error()))
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
		m.snapshot.setStatus(newTUIMessage("ui.7239b70a44b1"))
	} else {
		m.snapshot.setStatus(newTUIMessage("ui.f2adb4b7a95d"))
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
		m.snapshot.setStatus(newTUIMessage("ui.7239b70a44b1"))
		return
	}
	if m.sshFormSelected == tuiSSHFormPassphraseRow {
		switch {
		case strings.TrimSpace(m.sshForm.Identity) == "":
			m.snapshot.setStatus(newTUIMessage("ui.28955a95e445"))
			return
		case m.sshFormIdentityKind == cliSSHIdentityUnencrypted:
			m.snapshot.setStatus(newTUIMessage("ui.c761569bbb5b"))
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
	m.snapshot.setStatus(newTUIMessage("ui.e107e1f4c746"))
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
			m.snapshot.setStatus(newTUIMessage("ui.ff400816f107", err.Error()))
			return false
		}
	case tuiSSHFormPortRow:
		port, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || port < 1 || port > 65535 {
			m.snapshot.setStatus(newTUIMessage("ui.1076ae969ced"))
			return false
		}
		m.sshForm.Port = port
	case tuiSSHFormLocalPortRow:
		port, err := parseCLISSHLocalPort(value)
		if err != nil {
			m.snapshot.setStatus(newTUIMessage("ui.ff400816f107", err.Error()))
			return false
		}
		m.sshForm.LocalPort = port
	case tuiSSHFormIdentityRow:
		m.sshForm.Identity = strings.TrimSpace(value)
		m.refreshSSHFormIdentityState()
	case tuiSSHFormPassphraseRow:
		if value == "" {
			m.snapshot.setStatus(newTUIMessage("ui.ed135e01134a"))
			return false
		}
		if !m.sshFormPassphraseConfirm {
			m.sshFormPassphraseFirst = value
			m.sshFormPassphraseConfirm = true
			m.sshFormInput = nil
			m.sshFormCursor = 0
			m.sshFormSelectAll = false
			m.snapshot.setStatus(newTUIMessage("ui.ceeafe06e9a0"))
			return false
		}
		if value != m.sshFormPassphraseFirst {
			m.sshFormPassphraseConfirm = false
			m.sshFormPassphraseFirst = ""
			m.sshFormInput = nil
			m.sshFormCursor = 0
			m.snapshot.setStatus(newTUIMessage("ui.caa11d9c07f9"))
			return false
		}
		m.sshForm.IdentityPassphrase = value
		m.sshFormPassphraseChanged = true
		m.sshFormPassphraseCleared = false
		m.refreshSSHFormIdentityState()
	case tuiSSHFormPasswordRow:
		if value == "" {
			m.snapshot.setStatus(newTUIMessage("ui.105a94955dbf"))
			return false
		}
		if !m.sshFormPasswordConfirm {
			m.sshFormPasswordFirst = value
			m.sshFormPasswordConfirm = true
			m.sshFormInput = nil
			m.sshFormCursor = 0
			m.sshFormSelectAll = false
			m.snapshot.setStatus(newTUIMessage("ui.42102da9ab24"))
			return false
		}
		if value != m.sshFormPasswordFirst {
			m.sshFormPasswordConfirm = false
			m.sshFormPasswordFirst = ""
			m.sshFormInput = nil
			m.sshFormCursor = 0
			m.snapshot.setStatus(newTUIMessage("ui.61bd5aed686a"))
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
			m.snapshot.setStatus(newTUIMessage("ui.b9bb1f61210a", err.Error()))
			return false
		}
		m.sshForm.Options[optionIndex] = value
		m.sshFormAddingOption = false
	}
	m.cancelSSHFormFieldEdit()
	m.snapshot.setStatus(newTUIMessage("ui.b517d49f5af0"))
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
		m.snapshot.setStatus(newTUIMessage("ui.53fd80f4bc25"))
		return nil
	}
	switch message.Type {
	case tea.KeyCtrlC:
		m.resetSSHForm()
		return m.handleKey(tuiKeyInterrupt)
	case tea.KeyEsc:
		m.resetSSHForm()
		m.snapshot.setStatus(newTUIMessage("ui.ddde7e676aff"))
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
						m.snapshot.setStatus(newTUIMessage("ui.7239b70a44b1"))
						return nil
					}
					if m.sshFormSelected == tuiSSHFormPassphraseRow {
						m.sshForm.IdentityPassphrase = ""
						m.sshFormPassphraseChanged = false
						m.sshFormPassphraseCleared = true
						m.refreshSSHFormIdentityState()
						m.snapshot.setStatus(newTUIMessage("ui.de08d3aaa40f"))
					} else {
						m.sshForm.Password = ""
						m.sshFormPasswordChanged = false
						m.sshFormPasswordCleared = true
						m.snapshot.setStatus(newTUIMessage("ui.648e14253453"))
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
			m.snapshot.setStatus(newTUIMessage("ui.45eaa93cc0a5"))
		default:
			m.snapshot.setStatus(newTUIMessage("ui.7239b70a44b1"))
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
		m.snapshot.setStatus(newTUIMessage("ui.ddde7e676aff"))
		return nil
	default:
		m.beginSSHFormFieldEdit()
		return nil
	}
}

func (m *tuiModel) deleteSelectedSSHFormOption() tea.Cmd {
	if m.sshFormReadOnly {
		m.snapshot.setStatus(newTUIMessage("ui.7239b70a44b1"))
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
	m.snapshot.setStatus(newTUIMessage("ui.5afdc128a7fa"))
	return nil
}

func (m *tuiModel) saveSSHForm() tea.Cmd {
	if m.sshFormReadOnly {
		m.snapshot.setStatus(newTUIMessage("ui.7239b70a44b1"))
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
			m.snapshot.setStatus(newTUIMessage("ui.6b44c425cbe5", err.Error()))
			return nil
		}
		if kind == cliSSHIdentityUnencrypted {
			profile.IdentityPassphrase = ""
		}
	}
	if err := validateCLISSHProfile(profile); err != nil {
		m.snapshot.setStatus(newTUIMessage("ui.ff400816f107", err.Error()))
		return nil
	}
	existing := m.sshFormExisting
	originalName := m.sshFormOriginalName
	expectedFingerprint := m.sshFormFingerprint
	m.busy = true
	m.snapshot.setStatus(newTUIMessage("ui.c9ddeb2e56ea", profile.Name))
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
			text:         newTUIMessage("ssh.saved", profile.Name),
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
		m.snapshot.setStatus(newTUIMessage("ui.b04a5f561d60"))
	case tea.KeyEnter:
		m.commitSSHFormField()
	case tea.KeyBackspace, tea.KeyCtrlH:
		if m.sshFormSelectAll {
			m.clearSSHFormInput()
		} else if m.sshFormCursor > 0 {
			previous := tuiPreviousGrapheme(m.sshFormInput, m.sshFormCursor)
			m.sshFormInput = append(
				m.sshFormInput[:previous],
				m.sshFormInput[m.sshFormCursor:]...,
			)
			m.sshFormCursor = previous
		}
	case tea.KeyDelete:
		if m.sshFormSelectAll {
			m.clearSSHFormInput()
		} else if m.sshFormCursor < len(m.sshFormInput) {
			m.sshFormInput = append(
				m.sshFormInput[:m.sshFormCursor],
				m.sshFormInput[tuiNextGrapheme(m.sshFormInput, m.sshFormCursor):]...,
			)
		}
	case tea.KeyLeft:
		if m.sshFormSelectAll {
			m.sshFormCursor = 0
			m.sshFormSelectAll = false
		} else if m.sshFormCursor > 0 {
			m.sshFormCursor = tuiPreviousGrapheme(m.sshFormInput, m.sshFormCursor)
		}
	case tea.KeyRight:
		if m.sshFormSelectAll {
			m.sshFormCursor = len(m.sshFormInput)
			m.sshFormSelectAll = false
		} else if m.sshFormCursor < len(m.sshFormInput) {
			m.sshFormCursor = tuiNextGrapheme(m.sshFormInput, m.sshFormCursor)
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
		m.sshFormCursor = tuiGraphemeCursor(m.sshFormInput, m.sshFormCursor)
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
		m.snapshot.setStatus(newTUIMessage("ui.d7d54ef0ad99"))
		return
	}
	m.beginSSHDeleteConfirmForName(
		m.snapshot.SSHProfiles[m.snapshot.SelectedSSH].Name,
	)
}

func (m *tuiModel) beginSSHDeleteConfirmForName(name string) {
	m.sshDeleteName = name
	m.sshDeleteConfirmOpen = true
	m.snapshot.setStatus(newTUIMessage("ui.3ab645ceabd9"))
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
		m.snapshot.setStatus(newTUIMessage("ui.f253809789f3"))
	case tea.KeyEnter:
		name := m.sshDeleteName
		m.sshDeleteConfirmOpen = false
		m.sshDeleteName = ""
		if m.sshFormOpen {
			m.resetSSHForm()
		}
		m.busy = true
		m.snapshot.setStatus(newTUIMessage("ui.109782297cac", name))
		return func() tea.Msg {
			err := deleteCLISSHProfile(name)
			status := ""
			if err == nil {
				status = "SSH profile " + name + " deleted"
			}
			return tuiSSHCommandResultMsg{
				action: "delete",
				status: status,
				text:   newTUIMessage("ssh.deleted", name),
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
		m.snapshot.setStatus(newTUIMessage("ui.71211c541b69"))
		return
	}
	profile := m.snapshot.Profiles[m.snapshot.SelectedRow]
	if profile.Current {
		m.snapshot.setStatus(newTUIMessage("ui.8a396d70c226"))
		return
	}
	if m.service == nil {
		m.snapshot.setStatus(newTUIMessage("ui.bf82ed01550e"))
		return
	}
	m.profileDeleteOpen = true
	m.profileDeletePath = profile.Path
	m.profileDeleteName = profile.Name
	m.profileDeleteKind = "local"
	if profile.SubscriptionURL != "" {
		m.profileDeleteKind = "subscription"
	}
	m.snapshot.setStatus(newTUIMessage("ui.828cf3849088"))
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
		m.snapshot.setStatus(newTUIMessage("ui.9d01b7c8edde"))
	case tea.KeyEnter:
		path := m.profileDeletePath
		name := m.profileDeleteName
		selected := m.snapshot.SelectedRow
		service := m.service
		m.resetProfileDeleteConfirm()
		return m.startOperation(func(state *tuiOperationState) {
			if service == nil {
				state.snapshot.setStatus(newTUIMessage("ui.bf82ed01550e"))
				return
			}
			if !prepareTUIBackendRevision(state, service) {
				return
			}
			status, err := state.service.deleteProfile(path, state.backendRevision)
			if err != nil {
				state.snapshot.setStatus(newTUIMessage("ui.183e219b9094", err.Error()))
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
			state.snapshot.setStatus(newTUIMessage("ui.e8a70ca44cc7", name))
		})
	case tea.KeyRunes:
		if len(message.Runes) == 1 && message.Runes[0] == 'q' {
			m.resetProfileDeleteConfirm()
			return m.handleKey(tuiKeyQuit)
		}
	}
	return nil
}
