//go:build linux && !cgo && cli

package main

import tea "github.com/charmbracelet/bubbletea"

const (
	tuiDelayProbeParallelism = 4
	tuiSpeedProbeParallelism = 2
	tuiProbeDelay            = "delay"
	tuiProbeSpeed            = "speed"
	tuiProbeDashboardDelay   = "dashboard-delay"
	tuiProbeDashboardSpeed   = "dashboard-speed"
)

type tuiProbeBatchState struct {
	id      uint64
	group   string
	total   int
	done    int
	ok      int
	summary bool
}

type tuiProxyProbeJob struct {
	kind              string
	epoch             uint64
	seq               uint64
	batch             uint64
	backendGeneration uint64
	backendRevision   uint64
	group             string
	node              string
	testURL           string
	mixedPort         int
}

func (m *tuiModel) syncLaneBusy() {
	m.busy = m.coreBusy || m.editorBusy || m.sshBusy
}

func (m *tuiModel) orphanBusy() bool {
	return m.busy && !m.coreBusy && !m.editorBusy && !m.sshBusy
}

func (m *tuiModel) coreWriteBlocked() bool {
	return m.coreBusy || m.editorBusy || m.orphanBusy()
}

func (m *tuiModel) sshWriteBlocked() bool {
	return m.sshBusy || m.editorBusy || m.orphanBusy()
}

func (m *tuiModel) proxyProbeBlocked() bool {
	return m.coreBusy || m.editorBusy || m.orphanBusy()
}

func (m *tuiModel) beginSSHWrite() (uint64, bool) {
	if m.sshWriteBlocked() {
		m.snapshot.setStatus(newTUIMessage("ui.c90e0573178b"))
		return 0, false
	}
	m.sshBusy = true
	m.sshWriteSequence++
	m.syncLaneBusy()
	return m.sshWriteSequence, true
}

func proxyProbeKey(kind, group, node string) string {
	return kind + "\x00" + group + "\x00" + node
}

func (m *tuiModel) bumpProxyProbe(kind, group, node string) (uint64, uint64) {
	if m.proxyProbeSeq == nil {
		m.proxyProbeSeq = map[string]uint64{}
	}
	key := proxyProbeKey(kind, group, node)
	m.proxyProbeSeq[key]++
	return m.proxyProbeEpoch, m.proxyProbeSeq[key]
}

func (m *tuiModel) proxyProbeCurrent(kind, group, node string, epoch, seq, generation, revision uint64) bool {
	if epoch != m.proxyProbeEpoch || generation != m.backendGeneration || revision < m.backendRevision {
		return false
	}
	if m.proxyProbeSeq == nil {
		return seq == 0
	}
	current, ok := m.proxyProbeSeq[keyOf(kind, group, node)]
	if !ok {
		return seq == 0
	}
	return current == seq
}

func keyOf(kind, group, node string) string {
	return proxyProbeKey(kind, group, node)
}

func (m *tuiModel) invalidateDashboardProbes() {
	m.bumpProxyProbe(tuiProbeDashboardDelay, "", "")
	m.bumpProxyProbe(tuiProbeDashboardSpeed, "", "")
	m.delayQueue = filterProbeJobs(m.delayQueue, func(job tuiProxyProbeJob) bool {
		return job.kind != tuiProbeDashboardDelay
	})
	m.speedQueue = filterProbeJobs(m.speedQueue, func(job tuiProxyProbeJob) bool {
		return job.kind != tuiProbeDashboardSpeed
	})
}

func (m *tuiModel) invalidateProxyProbes() {
	m.proxyProbeEpoch++
	m.delayQueue = nil
	m.speedQueue = nil
	m.delayBatch = tuiProbeBatchState{}
	m.speedBatch = tuiProbeBatchState{}
	if m.snapshot.DashboardDelay.Testing {
		m.snapshot.DashboardDelay = tuiDelayResult{}
	}
	if m.snapshot.DashboardSpeed.Testing {
		m.snapshot.DashboardSpeed = tuiSpeedResult{}
	}
	for index := range m.snapshot.Groups {
		group := &m.snapshot.Groups[index]
		for node, delay := range group.Delays {
			if delay.Testing {
				delete(group.Delays, node)
			}
		}
		for node, speed := range group.Speeds {
			if speed.Testing {
				delete(group.Speeds, node)
			}
		}
	}
}

func (m *tuiModel) rejectProxyProbe() bool {
	if !m.proxyProbeBlocked() {
		return false
	}
	m.snapshot.setStatus(newTUIMessage("ui.c90e0573178b"))
	return true
}

func (m *tuiModel) enqueueDelayProbes(group string, nodes []string, summary bool) tea.Cmd {
	if m.rejectProxyProbe() {
		return nil
	}
	m.delayBatch.id++
	m.delayBatch.group = group
	m.delayBatch.total = len(nodes)
	m.delayBatch.done = 0
	m.delayBatch.ok = 0
	m.delayBatch.summary = summary
	m.discardQueuedGroupProbes(&m.delayQueue, group, false)
	testURL := m.tuiDelayTestURL()
	for _, node := range nodes {
		m.replaceQueuedProbe(&m.delayQueue, m.newProbeJob(tuiProbeDelay, group, node, testURL, 0, m.delayBatch.id))
		setTUIGroupDelay(&m.snapshot, group, node, tuiDelayResult{Testing: true})
	}
	return tea.Batch(m.pumpDelayProbes()...)
}

func (m *tuiModel) enqueueSpeedProbes(group string, nodes []string, summary bool) tea.Cmd {
	if m.service == nil {
		m.snapshot.setStatus(newTUIMessage("ui.85df53e87e9c"))
		return nil
	}
	if m.rejectProxyProbe() {
		return nil
	}
	m.speedBatch.id++
	m.speedBatch.group = group
	m.speedBatch.total = len(nodes)
	m.speedBatch.done = 0
	m.speedBatch.ok = 0
	m.speedBatch.summary = summary
	m.discardQueuedGroupProbes(&m.speedQueue, group, true)
	for _, node := range nodes {
		m.replaceQueuedProbe(&m.speedQueue, m.newProbeJob(tuiProbeSpeed, group, node, "", 0, m.speedBatch.id))
		setTUIGroupSpeed(&m.snapshot, group, node, tuiSpeedResult{Testing: true})
	}
	// The new batch owns the status line. A single-node retest must not keep
	// the previous group summary's node count.
	if summary {
		m.snapshot.setStatus(newTUIMessage("ui.fffba2149b7e", group, len(nodes), len(nodes)*100))
	} else if len(nodes) > 0 {
		m.snapshot.setStatus(newTUIMessage("ui.be8bb45e2eb4", group, 0, len(nodes), nodes[0]))
	}
	return tea.Batch(m.pumpSpeedProbes()...)
}

func (m *tuiModel) dashboardProbePort() int {
	return managedListenerPort(
		m.coreRunning || !m.ownsCore,
		m.snapshot.Settings.Mode,
		m.snapshot.ActiveProxyPort,
		m.snapshot.Settings.MixedPort,
		m.snapshot.Settings.MixedPort,
	)
}

func (m *tuiModel) enqueueDashboardDelay() tea.Cmd {
	if m.service == nil {
		m.snapshot.setStatus(newTUIMessage("ui.df7bb6bc35d4"))
		return nil
	}
	if m.dashboardProbePort() <= 0 {
		m.snapshot.setStatus(tuiMessage{Raw: "normal Proxy port is not active; use `flclash flc test` in silent mode"})
		return nil
	}
	if m.rejectProxyProbe() {
		return nil
	}
	m.snapshot.DashboardDelay = tuiDelayResult{Testing: true}
	job := m.newProbeJob(tuiProbeDashboardDelay, "", "", defaultCLITestURL, m.dashboardProbePort(), 0)
	m.replaceQueuedProbe(&m.delayQueue, job)
	return tea.Batch(m.pumpDelayProbes()...)
}

func (m *tuiModel) enqueueDashboardSpeed() tea.Cmd {
	if m.service == nil {
		m.snapshot.setStatus(newTUIMessage("ui.e1c7c4a7c00b"))
		return nil
	}
	if m.dashboardProbePort() <= 0 {
		m.snapshot.setStatus(tuiMessage{Raw: "normal Proxy port is not active; use `flclash flc test` in silent mode"})
		return nil
	}
	if m.rejectProxyProbe() {
		return nil
	}
	m.snapshot.DashboardSpeed = tuiSpeedResult{Testing: true}
	job := m.newProbeJob(tuiProbeDashboardSpeed, "", "", "", m.dashboardProbePort(), 0)
	m.replaceQueuedProbe(&m.speedQueue, job)
	return tea.Batch(m.pumpSpeedProbes()...)
}

func (m *tuiModel) newProbeJob(kind, group, node, testURL string, mixedPort int, batch uint64) tuiProxyProbeJob {
	epoch, seq := m.bumpProxyProbe(kind, group, node)
	return tuiProxyProbeJob{
		kind:              kind,
		epoch:             epoch,
		seq:               seq,
		batch:             batch,
		backendGeneration: m.backendGeneration,
		backendRevision:   m.backendRevision,
		group:             group,
		node:              node,
		testURL:           testURL,
		mixedPort:         mixedPort,
	}
}

func (m *tuiModel) replaceQueuedProbe(queue *[]tuiProxyProbeJob, job tuiProxyProbeJob) {
	replaced := false
	for index := range *queue {
		if (*queue)[index].kind == job.kind && (*queue)[index].group == job.group && (*queue)[index].node == job.node {
			(*queue)[index] = job
			replaced = true
			break
		}
	}
	if !replaced {
		*queue = append(*queue, job)
	}
}

// Queued jobs have not started, so dropping them must clear the Testing
// spinner. In-flight jobs are already off the queue and keep their slot.
func (m *tuiModel) discardQueuedGroupProbes(queue *[]tuiProxyProbeJob, group string, speed bool) {
	kept := make([]tuiProxyProbeJob, 0, len(*queue))
	for _, job := range *queue {
		if job.group == group {
			if speed {
				clearTUITestingSpeed(&m.snapshot, job.group, job.node)
			} else {
				clearTUITestingDelay(&m.snapshot, job.group, job.node)
			}
			continue
		}
		kept = append(kept, job)
	}
	*queue = kept
}

func clearTUITestingDelay(snapshot *tuiSnapshot, groupName, node string) {
	for index := range snapshot.Groups {
		group := &snapshot.Groups[index]
		if group.Name != groupName || !group.Delays[node].Testing {
			continue
		}
		delete(group.Delays, node)
	}
}

func clearTUITestingSpeed(snapshot *tuiSnapshot, groupName, node string) {
	for index := range snapshot.Groups {
		group := &snapshot.Groups[index]
		if group.Name != groupName || !group.Speeds[node].Testing {
			continue
		}
		delete(group.Speeds, node)
	}
}

func filterProbeJobs(jobs []tuiProxyProbeJob, keep func(tuiProxyProbeJob) bool) []tuiProxyProbeJob {
	filtered := jobs[:0:0]
	for _, job := range jobs {
		if keep(job) {
			filtered = append(filtered, job)
		}
	}
	return filtered
}

func (m *tuiModel) pumpDelayProbes() []tea.Cmd {
	var commands []tea.Cmd
	for m.delayInFlight < tuiDelayProbeParallelism && len(m.delayQueue) > 0 {
		job := m.delayQueue[0]
		m.delayQueue = m.delayQueue[1:]
		if !m.proxyProbeCurrent(job.kind, job.group, job.node, job.epoch, job.seq, job.backendGeneration, job.backendRevision) {
			continue
		}
		m.delayInFlight++
		commands = append(commands, m.runDelayProbe(job))
	}
	return commands
}

func (m *tuiModel) pumpSpeedProbes() []tea.Cmd {
	var commands []tea.Cmd
	for m.speedInFlight < tuiSpeedProbeParallelism && len(m.speedQueue) > 0 {
		job := m.speedQueue[0]
		m.speedQueue = m.speedQueue[1:]
		if !m.proxyProbeCurrent(job.kind, job.group, job.node, job.epoch, job.seq, job.backendGeneration, job.backendRevision) {
			continue
		}
		m.speedInFlight++
		commands = append(commands, m.runSpeedProbe(job))
	}
	return commands
}

func (m *tuiModel) runDelayProbe(job tuiProxyProbeJob) tea.Cmd {
	client := m.client
	var service *tuiServiceClient
	if m.service != nil {
		service = m.service.forInstance(m.backendInstanceID)
	}
	return func() tea.Msg {
		message := tuiProxyDelayResultMsg{
			epoch: job.epoch, seq: job.seq, batch: job.batch,
			backendGeneration: job.backendGeneration, backendRevision: job.backendRevision,
			groupName: job.group, node: job.node, pooled: true,
			dashboard: job.kind == tuiProbeDashboardDelay,
		}
		var err error
		if job.kind == tuiProbeDashboardDelay {
			if service == nil {
				err = errString("Backend is not running")
			} else {
				message.result, err = testManagedRouteDelayForCommand(service, job.mixedPort, job.testURL)
			}
		} else {
			message.result, err = testTUIProxyDelaySamples(client, job.node, job.testURL)
		}
		if err != nil {
			message.result = tuiDelayResult{Error: err.Error()}
		}
		return message
	}
}

func (m *tuiModel) runSpeedProbe(job tuiProxyProbeJob) tea.Cmd {
	var service *tuiServiceClient
	if m.service != nil {
		service = m.service.forInstance(m.backendInstanceID)
	}
	return func() tea.Msg {
		message := tuiProxyGroupSpeedResultMsg{
			epoch: job.epoch, seq: job.seq, batch: job.batch, pooled: true,
			backendGeneration: job.backendGeneration, backendRevision: job.backendRevision,
			groupName: job.group, node: job.node,
		}
		var err error
		if service == nil {
			err = errString("Backend is not running")
		} else if job.kind == tuiProbeDashboardSpeed {
			message.result, err = testManagedRouteSpeedForCommand(service, job.mixedPort)
		} else {
			message.result, err = service.testProxySpeed(job.node)
		}
		if err != nil {
			message.result = tuiSpeedResult{Error: err.Error()}
		}
		return message
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func (m *tuiModel) applyProxyDelayResult(message tuiProxyDelayResultMsg) tea.Cmd {
	if message.pooled && m.delayInFlight > 0 {
		m.delayInFlight--
	}
	if !m.proxyProbeCurrent(delayKind(message.dashboard), message.groupName, message.node, message.epoch, message.seq, message.backendGeneration, message.backendRevision) {
		m.clearStaleDelaySpinner(message)
		return tea.Batch(m.pumpDelayProbes()...)
	}
	if message.dashboard {
		m.snapshot.DashboardDelay = message.result
		if !m.probeStatusLocked(m.delayBatch, message.batch) {
			if message.result.Error != "" {
				m.snapshot.setStatus(newTUIMessage("ui.07774aaecaa3", message.result.Error))
			} else {
				m.snapshot.setStatus(newTUIMessage("ui.593694af3dbf", formatTUIDelay(message.result)))
			}
		}
		return tea.Batch(m.pumpDelayProbes()...)
	}
	setTUIGroupDelay(&m.snapshot, message.groupName, message.node, message.result)
	if m.probeStatusLocked(m.delayBatch, message.batch) {
		return tea.Batch(m.pumpDelayProbes()...)
	}
	if message.batch == m.delayBatch.id && m.delayBatch.summary {
		m.delayBatch.done++
		if message.result.Error == "" && message.result.MedianMillis > 0 {
			m.delayBatch.ok++
		}
		if m.delayBatch.done >= m.delayBatch.total {
			m.snapshot.setStatus(newTUIMessage("ui.343980935349", m.delayBatch.group, m.delayBatch.ok, m.delayBatch.total))
		} else if message.result.Error != "" {
			m.snapshot.setStatus(newTUIMessage("ui.13f55ad346eb", message.node, message.result.Error))
		} else {
			m.snapshot.setStatus(newTUIMessage("ui.c5d054f540f9", message.node, formatTUIDelay(message.result)))
		}
	} else if message.result.Error != "" {
		finishProbeStatus(&m.delayBatch, message.batch)
		m.snapshot.setStatus(newTUIMessage("ui.13f55ad346eb", message.node, message.result.Error))
	} else {
		finishProbeStatus(&m.delayBatch, message.batch)
		m.snapshot.setStatus(newTUIMessage("ui.c5d054f540f9", message.node, formatTUIDelay(message.result)))
	}
	return tea.Batch(m.pumpDelayProbes()...)
}

// The newest delay or speed batch owns the status line until its own result
// finishes it. Older batches and dashboard probes still write their measurements.
func (m *tuiModel) probeStatusLocked(batch tuiProbeBatchState, messageBatch uint64) bool {
	return batch.id != 0 && batch.total > 0 && batch.done < batch.total && messageBatch != batch.id
}

func finishProbeStatus(batch *tuiProbeBatchState, messageBatch uint64) {
	if batch.summary || messageBatch != batch.id {
		return
	}
	batch.done = batch.total
}

func nextQueuedBatchNode(queue []tuiProxyProbeJob, batch tuiProbeBatchState) string {
	for _, job := range queue {
		if job.batch == batch.id && job.group == batch.group && job.node != "" {
			return job.node
		}
	}
	return ""
}

func (m *tuiModel) nextSpeedBatchNode(finished string) string {
	if next := nextQueuedBatchNode(m.speedQueue, m.speedBatch); next != "" {
		return next
	}
	for _, group := range m.snapshot.Groups {
		if group.Name != m.speedBatch.group {
			continue
		}
		for _, node := range group.Nodes {
			if node != finished && group.Speeds[node].Testing {
				return node
			}
		}
	}
	return finished
}

func delayKind(dashboard bool) string {
	if dashboard {
		return tuiProbeDashboardDelay
	}
	return tuiProbeDelay
}

func (m *tuiModel) clearStaleDelaySpinner(message tuiProxyDelayResultMsg) {
	if message.epoch != m.proxyProbeEpoch {
		return
	}
	if !m.seqStillCurrent(delayKind(message.dashboard), message.groupName, message.node, message.seq) {
		return
	}
	if message.dashboard {
		if m.snapshot.DashboardDelay.Testing {
			m.snapshot.DashboardDelay = tuiDelayResult{}
		}
		return
	}
	for index := range m.snapshot.Groups {
		if m.snapshot.Groups[index].Name != message.groupName {
			continue
		}
		if m.snapshot.Groups[index].Delays[message.node].Testing {
			delete(m.snapshot.Groups[index].Delays, message.node)
		}
	}
}

func (m *tuiModel) seqStillCurrent(kind, group, node string, seq uint64) bool {
	if m.proxyProbeSeq == nil {
		return seq == 0
	}
	current, ok := m.proxyProbeSeq[proxyProbeKey(kind, group, node)]
	if !ok {
		return seq == 0
	}
	return current == seq
}

func (m *tuiModel) applyProxySpeedResult(message tuiProxyGroupSpeedResultMsg) tea.Cmd {
	if message.operationID != 0 && message.operationID != m.operationSequence {
		return nil
	}
	if message.pooled && m.speedInFlight > 0 {
		m.speedInFlight--
	}
	if message.pooled || message.epoch != 0 || message.seq != 0 {
		if !m.proxyProbeCurrent(speedKind(message), message.groupName, message.node, message.epoch, message.seq, message.backendGeneration, message.backendRevision) {
			m.clearStaleSpeedSpinner(message)
			return tea.Batch(m.pumpSpeedProbes()...)
		}
		return m.finishPooledSpeed(message)
	}
	if message.backendGeneration != m.backendGeneration || message.backendRevision < m.backendRevision {
		m.finishTUIOperationIndicators(tuiOperationState{operationID: message.operationID, testingIndicators: m.groupSpeedIndicators})
		return m.startRefresh()
	}
	if message.result.Error == "" {
		message.successes++
	}
	setTUIGroupSpeed(&m.snapshot, message.groupName, message.node, message.result)
	completed := message.total - len(message.remaining)
	if len(message.remaining) > 0 {
		m.snapshot.setStatus(newTUIMessage("ui.be8bb45e2eb4", message.groupName, completed, message.total, message.remaining[0]))
		return m.testNextProxyGroupSpeed(message.groupName, message.remaining, message.total, message.successes)
	}
	m.snapshot.setStatus(newTUIMessage("ui.9d3927bdfdcb", message.groupName, message.successes, message.total))
	return m.startRefresh()
}

func speedKind(message tuiProxyGroupSpeedResultMsg) string {
	if message.groupName == "" && message.node == "" {
		return tuiProbeDashboardSpeed
	}
	return tuiProbeSpeed
}

func (m *tuiModel) finishPooledSpeed(message tuiProxyGroupSpeedResultMsg) tea.Cmd {
	if message.groupName == "" && message.node == "" {
		m.snapshot.DashboardSpeed = message.result
		if !m.probeStatusLocked(m.speedBatch, message.batch) {
			if message.result.Error != "" {
				m.snapshot.setStatus(newTUIMessage("ui.524fc802cbfb", message.result.Error))
			} else {
				m.snapshot.setStatus(newTUIMessage("ui.dccbe0cdbd03", formatTUISpeed(message.result)))
			}
		}
		return tea.Batch(m.pumpSpeedProbes()...)
	}
	setTUIGroupSpeed(&m.snapshot, message.groupName, message.node, message.result)
	if m.probeStatusLocked(m.speedBatch, message.batch) {
		return tea.Batch(m.pumpSpeedProbes()...)
	}
	if message.batch == m.speedBatch.id && m.speedBatch.summary {
		m.speedBatch.done++
		if message.result.Error == "" {
			m.speedBatch.ok++
		}
		if m.speedBatch.done >= m.speedBatch.total {
			m.snapshot.setStatus(newTUIMessage("ui.9d3927bdfdcb", m.speedBatch.group, m.speedBatch.ok, m.speedBatch.total))
		} else {
			m.snapshot.setStatus(newTUIMessage("ui.be8bb45e2eb4", m.speedBatch.group, m.speedBatch.done, m.speedBatch.total, m.nextSpeedBatchNode(message.node)))
		}
	} else if message.result.Error != "" {
		finishProbeStatus(&m.speedBatch, message.batch)
		m.snapshot.setStatus(newTUIMessage("ui.e72ac0c17cec", message.node, message.result.Error))
	} else {
		finishProbeStatus(&m.speedBatch, message.batch)
		m.snapshot.setStatus(newTUIMessage("ui.2be9d7d0142c", message.node, formatTUISpeed(message.result)))
	}
	return tea.Batch(m.pumpSpeedProbes()...)
}

func (m *tuiModel) clearStaleSpeedSpinner(message tuiProxyGroupSpeedResultMsg) {
	if message.epoch != m.proxyProbeEpoch {
		return
	}
	kind := speedKind(message)
	if !m.seqStillCurrent(kind, message.groupName, message.node, message.seq) {
		return
	}
	if kind == tuiProbeDashboardSpeed {
		if m.snapshot.DashboardSpeed.Testing {
			m.snapshot.DashboardSpeed = tuiSpeedResult{}
		}
		return
	}
	for index := range m.snapshot.Groups {
		if m.snapshot.Groups[index].Name != message.groupName {
			continue
		}
		if m.snapshot.Groups[index].Speeds[message.node].Testing {
			delete(m.snapshot.Groups[index].Speeds, message.node)
		}
	}
}
