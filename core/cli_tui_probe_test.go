//go:build linux && !cgo && cli

package main

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestProxyProbesStayOffTheCoreLane(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.service = &tuiServiceClient{}
	model.snapshot.Groups = []tuiGroup{{Name: "Proxy", Nodes: []string{"a", "b", "c"}}}
	model.snapshot.SelectedGroup = 0
	if command := model.testSelectedProxyGroupSpeeds(); command == nil || model.coreBusy || model.busy || model.speedInFlight != tuiSpeedProbeParallelism || len(model.speedQueue) != 1 {
		t.Fatalf("speed lane = command:%v core:%t busy:%t inFlight:%d queued:%d", command != nil, model.coreBusy, model.busy, model.speedInFlight, len(model.speedQueue))
	}
	if command := model.testSelectedProxyGroupDelays(); command == nil || model.coreBusy || model.delayInFlight != len(model.snapshot.Groups[0].Nodes) {
		t.Fatalf("delay lane = command:%v core:%t inFlight:%d", command != nil, model.coreBusy, model.delayInFlight)
	}
	model.snapshot.Page = tuiPageSSH
	model.snapshot.FocusSidebar = false
	model.snapshot.SSHProfiles = []tuiSSHProfile{{Name: "home", Username: "user", Host: "192.0.2.1", Port: 22}}
	model.snapshot.SelectedSSH = 0
	if command := model.runSelectedSSHAction("connect"); command == nil || !model.sshBusy || model.coreBusy {
		t.Fatal("SSH connect did not take only the SSH lane")
	}
	if command := model.runSelectedSSHAction("disconnect"); command != nil {
		t.Fatal("second SSH write was accepted")
	}
	if !strings.Contains(model.snapshot.Status, "Another operation is still running") {
		t.Fatalf("second SSH status = %q", model.snapshot.Status)
	}
}

func TestCoreWriteDropsLateProxyProbeResults(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.snapshot.Groups = []tuiGroup{{
		Name:   "Proxy",
		Nodes:  []string{"node"},
		Delays: map[string]tuiDelayResult{"node": {Testing: true, MedianMillis: 1}},
		Speeds: map[string]tuiSpeedResult{"node": {Testing: true}},
	}}
	model.proxyProbeEpoch = 2
	model.proxyProbeSeq = map[string]uint64{
		proxyProbeKey(tuiProbeDelay, "Proxy", "node"): 3,
		proxyProbeKey(tuiProbeSpeed, "Proxy", "node"): 4,
	}
	model.coreBusy = true
	model.syncLaneBusy()
	model.update(tuiProxyDelayResultMsg{
		epoch: 1, seq: 3, pooled: true, groupName: "Proxy", node: "node",
		result: tuiDelayResult{MedianMillis: 80, Samples: 5},
	})
	model.update(tuiProxyGroupSpeedResultMsg{
		epoch: 1, seq: 4, pooled: true, groupName: "Proxy", node: "node",
		result: tuiSpeedResult{BytesPerSecond: 999},
	})
	delay := model.snapshot.Groups[0].Delays["node"]
	speed := model.snapshot.Groups[0].Speeds["node"]
	if !model.coreBusy || !delay.Testing || delay.MedianMillis != 1 || !speed.Testing || speed.BytesPerSecond != 0 {
		t.Fatalf("late probe wrote over a core write: delay=%+v speed=%+v core:%t", delay, speed, model.coreBusy)
	}
}

func TestRefreshKeepsManualDelayAndTestingSpeed(t *testing.T) {
	current := tuiSnapshot{Groups: []tuiGroup{{
		Name:   "Proxy",
		Nodes:  []string{"node"},
		Delays: map[string]tuiDelayResult{"node": {Manual: true, MedianMillis: 9, Samples: 5}},
		Speeds: map[string]tuiSpeedResult{"node": {Testing: true}},
	}}}
	updated := current
	updated.Groups = []tuiGroup{{Name: "Proxy", Nodes: []string{"node"}}}
	merged := mergeTUIRefresh(current, updated)
	delay := merged.Groups[0].Delays["node"]
	speed := merged.Groups[0].Speeds["node"]
	if !delay.Manual || delay.MedianMillis != 9 || !speed.Testing {
		t.Fatalf("refresh dropped probe state: delay=%+v speed=%+v", delay, speed)
	}
	current.DashboardDelay = tuiDelayResult{MedianMillis: 40, Samples: 5}
	current.DashboardSpeed = tuiSpeedResult{BytesPerSecond: 1000}
	current.Groups[0].Speeds = map[string]tuiSpeedResult{"node": {BytesPerSecond: 50}}
	updated.DashboardDelay = tuiDelayResult{Testing: true}
	updated.DashboardSpeed = tuiSpeedResult{Testing: true}
	updated.Groups[0].Speeds = map[string]tuiSpeedResult{"node": {Testing: true}}
	merged = mergeTUIRefresh(current, updated)
	nodeSpeed := merged.Groups[0].Speeds["node"]
	if merged.DashboardDelay.Testing || merged.DashboardDelay.MedianMillis != 40 ||
		merged.DashboardSpeed.Testing || merged.DashboardSpeed.BytesPerSecond != 1000 ||
		nodeSpeed.Testing || nodeSpeed.BytesPerSecond != 50 {
		t.Fatalf("refresh restored stale probes: delay=%+v speed=%+v node=%+v", merged.DashboardDelay, merged.DashboardSpeed, nodeSpeed)
	}
}

func TestExitChangeDropsLateDashboardProbe(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir(), ConfigPath: "/tmp/flclash-probe-exit.yaml"}, nil, true)
	model.service = &tuiServiceClient{}
	model.coreRunning = true
	model.snapshot.Settings.Mode = "rule"
	model.snapshot.Settings.MixedPort = 7890
	model.snapshot.ActiveProxyPort = 7890
	model.snapshot.Groups = []tuiGroup{{Name: "group", Now: "old", Nodes: []string{"old", "new"}}}
	model.syncNetworkExit()
	if command := model.enqueueDashboardDelay(); command == nil || !model.snapshot.DashboardDelay.Testing {
		t.Fatal("dashboard delay did not start")
	}
	epoch := model.proxyProbeEpoch
	seq := model.proxyProbeSeq[proxyProbeKey(tuiProbeDashboardDelay, "", "")]
	model.snapshot.Groups[0].Now = "new"
	if !model.syncNetworkExit() || model.snapshot.DashboardDelay.Testing || model.proxyProbeEpoch != epoch {
		t.Fatal("route change cancelled node probes or left the dashboard spinner")
	}
	model.update(tuiProxyDelayResultMsg{
		epoch: epoch, seq: seq, pooled: true, dashboard: true,
		result: tuiDelayResult{MedianMillis: 80, Samples: 5},
	})
	if model.snapshot.DashboardDelay.MedianMillis != 0 || model.snapshot.DashboardDelay.Testing {
		t.Fatalf("late dashboard probe wrote the old exit: %+v", model.snapshot.DashboardDelay)
	}
}

func TestEditorBlocksCoreAndSSHWrites(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.editorBusy = true
	model.syncLaneBusy()
	model.snapshot.Page = tuiPageDashboard
	model.snapshot.FocusSidebar = false
	model.snapshot.SSHProfiles = []tuiSSHProfile{{Name: "home", Username: "user", Host: "192.0.2.1", Port: 22}}
	model.snapshot.SelectedSSH = 0
	if command := model.startOperation(func(*tuiOperationState) {}); command != nil || !model.editorBusy {
		t.Fatal("editor allowed a core write")
	}
	model.snapshot.Page = tuiPageSSH
	if command := model.runSelectedSSHAction("connect"); command != nil || model.sshBusy {
		t.Fatal("editor allowed an SSH write")
	}
	_, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if command != nil {
		t.Fatal("editor accepted a non-navigation key")
	}
	model.snapshot.Groups = []tuiGroup{{Name: "Proxy", Nodes: []string{"a"}}}
	if command := model.testSelectedProxyDelay(); command != nil {
		t.Fatal("editor accepted a proxy probe")
	}
}

func TestLateGroupProbeDoesNotStealStatus(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.service = &tuiServiceClient{}
	model.snapshot.Groups = []tuiGroup{
		{Name: "A", Nodes: []string{"a1", "a2"}},
		{Name: "B", Nodes: []string{"b1", "b2"}},
	}
	if command := model.enqueueDelayProbes("A", []string{"a1", "a2"}, true); command == nil {
		t.Fatal("group A delay did not start")
	}
	epoch := model.proxyProbeEpoch
	batchA := model.delayBatch.id
	seqA := model.proxyProbeSeq[proxyProbeKey(tuiProbeDelay, "A", "a1")]
	if command := model.enqueueDelayProbes("B", []string{"b1", "b2"}, true); command == nil {
		t.Fatal("group B delay did not start")
	}
	model.snapshot.setStatus(newTUIMessage("ui.c5d054f540f9", "b1", "9 ms"))
	kept := model.snapshot.Status
	model.update(tuiProxyDelayResultMsg{
		epoch: epoch, seq: seqA, batch: batchA, pooled: true,
		groupName: "A", node: "a1",
		result: tuiDelayResult{MedianMillis: 21, Samples: 5},
	})
	if model.snapshot.Groups[0].Delays["a1"].MedianMillis != 21 {
		t.Fatal("late group A delay was dropped")
	}
	if !model.snapshot.Groups[1].Delays["b1"].Testing || model.snapshot.Status != kept {
		t.Fatalf("late delay stole group B status %q", model.snapshot.Status)
	}
}

func TestSingleProbeKeepsStatusUntilItFinishes(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.service = &tuiServiceClient{}
	model.snapshot.ActiveProxyPort = 7890
	model.snapshot.Groups = []tuiGroup{
		{Name: "A", Nodes: []string{"a1"}},
		{Name: "B", Nodes: []string{"b1"}},
	}
	if command := model.enqueueDelayProbes("A", []string{"a1"}, true); command == nil {
		t.Fatal("group delay did not start")
	}
	epoch := model.proxyProbeEpoch
	batchA := model.delayBatch.id
	seqA := model.proxyProbeSeq[proxyProbeKey(tuiProbeDelay, "A", "a1")]
	if command := model.enqueueDelayProbes("B", []string{"b1"}, false); command == nil {
		t.Fatal("single delay did not start")
	}
	seqB := model.proxyProbeSeq[proxyProbeKey(tuiProbeDelay, "B", "b1")]
	batchB := model.delayBatch.id
	model.snapshot.setStatus(newTUIMessage("ui.b93900bded31"))
	waiting := model.snapshot.Status
	model.update(tuiProxyDelayResultMsg{
		epoch: epoch, seq: seqA, batch: batchA, pooled: true,
		groupName: "A", node: "a1",
		result: tuiDelayResult{MedianMillis: 21, Samples: 5},
	})
	if model.snapshot.Groups[0].Delays["a1"].MedianMillis != 21 || model.snapshot.Status != waiting {
		t.Fatalf("older delay stole the single-node status %q", model.snapshot.Status)
	}
	model.update(tuiProxyDelayResultMsg{
		epoch: epoch, seq: seqB, batch: batchB, pooled: true,
		groupName: "B", node: "b1",
		result: tuiDelayResult{MedianMillis: 8, Samples: 5},
	})
	if model.snapshot.Groups[1].Delays["b1"].MedianMillis != 8 || model.snapshot.Status == waiting {
		t.Fatalf("single-node result did not take the status %q", model.snapshot.Status)
	}
	finished := model.snapshot.Status
	dashSeq := model.proxyProbeSeq[proxyProbeKey(tuiProbeDashboardDelay, "", "")]
	if command := model.enqueueDashboardDelay(); command == nil {
		t.Fatal("dashboard delay did not start")
	}
	if model.proxyProbeSeq[proxyProbeKey(tuiProbeDashboardDelay, "", "")] == dashSeq {
		dashSeq = model.proxyProbeSeq[proxyProbeKey(tuiProbeDashboardDelay, "", "")]
	}
	model.update(tuiProxyDelayResultMsg{
		epoch: epoch, seq: model.proxyProbeSeq[proxyProbeKey(tuiProbeDashboardDelay, "", "")],
		pooled: true, dashboard: true,
		result: tuiDelayResult{MedianMillis: 15, Samples: 5},
	})
	if model.snapshot.DashboardDelay.MedianMillis != 15 || model.snapshot.Status == finished {
		t.Fatalf("finished probe kept the dashboard status %q delay=%+v", model.snapshot.Status, model.snapshot.DashboardDelay)
	}
}

func TestSpeedProgressStaysOnCurrentBatch(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.service = &tuiServiceClient{}
	model.snapshot.ActiveProxyPort = 7890
	if command := model.enqueueDashboardSpeed(); command == nil || !model.snapshot.DashboardSpeed.Testing {
		t.Fatal("dashboard speed did not start")
	}
	epoch := model.proxyProbeEpoch
	dashSeq := model.proxyProbeSeq[proxyProbeKey(tuiProbeDashboardSpeed, "", "")]
	model.snapshot.Groups = []tuiGroup{{Name: "Proxy", Nodes: []string{"n1", "n2", "n3"}}}
	if command := model.enqueueSpeedProbes("Proxy", []string{"n1", "n2", "n3"}, true); command == nil {
		t.Fatal("group speed did not start")
	}
	batch := model.speedBatch.id
	seqN1 := model.proxyProbeSeq[proxyProbeKey(tuiProbeSpeed, "Proxy", "n1")]
	if !strings.Contains(model.snapshot.Status, "0/3") {
		t.Fatalf("group speed status = %q", model.snapshot.Status)
	}
	model.speedQueue = append([]tuiProxyProbeJob{{
		kind: tuiProbeSpeed, group: "Other", node: "foreign", batch: 99, epoch: epoch, seq: 1,
	}}, model.speedQueue...)
	model.update(tuiProxyGroupSpeedResultMsg{
		epoch: epoch, seq: seqN1, batch: batch, pooled: true,
		groupName: "Proxy", node: "n1",
		result: tuiSpeedResult{BytesPerSecond: 50},
	})
	if strings.Contains(model.snapshot.Status, "foreign") || !strings.Contains(model.snapshot.Status, "n2") {
		t.Fatalf("speed progress = %q", model.snapshot.Status)
	}
	progress := model.snapshot.Status
	model.update(tuiProxyGroupSpeedResultMsg{
		epoch: epoch, seq: dashSeq, pooled: true,
		result: tuiSpeedResult{BytesPerSecond: 100},
	})
	if model.snapshot.DashboardSpeed.BytesPerSecond != 100 || model.snapshot.Status != progress {
		t.Fatalf("dashboard result stole group status %q speed=%+v", model.snapshot.Status, model.snapshot.DashboardSpeed)
	}
	if model.snapshot.Groups[0].Speeds["n1"].BytesPerSecond != 50 || model.speedInFlight > tuiSpeedProbeParallelism {
		t.Fatalf("speed apply inFlight=%d result=%+v", model.speedInFlight, model.snapshot.Groups[0].Speeds["n1"])
	}
}

func TestRetestDropsStaleResultAndKeepsSpinner(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.service = &tuiServiceClient{}
	model.snapshot.Groups = []tuiGroup{{Name: "Proxy", Nodes: []string{"node"}}}
	if command := model.testSelectedProxySpeed(); command == nil || model.speedInFlight != 1 {
		t.Fatal("speed did not start")
	}
	epoch := model.proxyProbeEpoch
	seq := model.proxyProbeSeq[proxyProbeKey(tuiProbeSpeed, "Proxy", "node")]
	sshSeq := model.sshDelaySequence
	if command := model.testSelectedProxySpeed(); command == nil || !model.snapshot.Groups[0].Speeds["node"].Testing {
		t.Fatal("retest did not keep the spinner")
	}
	model.update(tuiProxyGroupSpeedResultMsg{
		epoch: epoch, seq: seq, pooled: true, groupName: "Proxy", node: "node",
		result: tuiSpeedResult{BytesPerSecond: 999},
	})
	speed := model.snapshot.Groups[0].Speeds["node"]
	if !speed.Testing || speed.BytesPerSecond != 0 || model.speedInFlight < 0 || model.speedInFlight > tuiSpeedProbeParallelism {
		t.Fatalf("stale retest applied: %+v inFlight=%d", speed, model.speedInFlight)
	}
	if model.sshDelaySequence != sshSeq {
		t.Fatal("proxy retest bumped an SSH probe sequence")
	}
}

func TestConfigChangeDropsNodeProbes(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir(), ConfigPath: "/tmp/flclash-probe-a.yaml"}, nil, true)
	model.service = &tuiServiceClient{}
	model.coreRunning = true
	model.snapshot.Settings.Mode = "rule"
	model.snapshot.Settings.MixedPort = 7890
	model.snapshot.ActiveProxyPort = 7890
	model.snapshot.Groups = []tuiGroup{{Name: "group", Now: "node", Nodes: []string{"node"}}}
	model.syncNetworkExit()
	if command := model.enqueueDelayProbes("group", []string{"node"}, false); command == nil {
		t.Fatal("node delay did not start")
	}
	epoch := model.proxyProbeEpoch
	seq := model.proxyProbeSeq[proxyProbeKey(tuiProbeDelay, "group", "node")]
	model.paths.ConfigPath = "/tmp/flclash-probe-b.yaml"
	if !model.syncNetworkExit() || model.proxyProbeEpoch == epoch || model.snapshot.Groups[0].Delays["node"].Testing {
		t.Fatal("config change left the node probe current")
	}
	model.update(tuiProxyDelayResultMsg{
		epoch: epoch, seq: seq, pooled: true, groupName: "group", node: "node",
		result: tuiDelayResult{MedianMillis: 30, Samples: 5},
	})
	if model.snapshot.Groups[0].Delays["node"].MedianMillis != 0 {
		t.Fatalf("late node probe wrote after config change: %+v", model.snapshot.Groups[0].Delays["node"])
	}
}

func TestSubsetRetestDropsQueuedProbesWithoutExceedingParallelism(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.service = &tuiServiceClient{}
	model.snapshot.Groups = []tuiGroup{
		{Name: "Proxy", Nodes: []string{"n1", "n2", "n3", "n4", "n5"}},
		{Name: "Other", Nodes: []string{"o1", "o2", "o3"}},
	}
	model.snapshot.SelectedGroup = 0
	model.snapshot.SelectedNode = 0
	if command := model.testSelectedProxyGroupSpeeds(); command == nil || model.speedInFlight != tuiSpeedProbeParallelism || len(model.speedQueue) != 3 {
		t.Fatalf("group speed inFlight=%d queued=%d", model.speedInFlight, len(model.speedQueue))
	}
	groupSpeedBatch := model.speedBatch.id
	if groupSpeedBatch == 0 {
		t.Fatal("group speed batch id was not assigned")
	}
	if command := model.enqueueSpeedProbes("Other", []string{"o1", "o2", "o3"}, true); model.speedInFlight != tuiSpeedProbeParallelism || len(model.speedQueue) != 6 {
		t.Fatalf("second group command:%v inFlight=%d queued=%d", command != nil, model.speedInFlight, len(model.speedQueue))
	}
	epoch := model.proxyProbeEpoch
	n1Seq := model.proxyProbeSeq[proxyProbeKey(tuiProbeSpeed, "Proxy", "n1")]
	n2Seq := model.proxyProbeSeq[proxyProbeKey(tuiProbeSpeed, "Proxy", "n2")]
	if command := model.testSelectedProxySpeed(); model.speedInFlight < 0 || model.speedInFlight > tuiSpeedProbeParallelism {
		t.Fatalf("single speed retest command:%v inFlight=%d", command != nil, model.speedInFlight)
	}
	if strings.Contains(model.snapshot.Status, "0/5") || strings.Contains(model.snapshot.Status, "0/3") || !strings.Contains(model.snapshot.Status, "0/1") || !strings.Contains(model.snapshot.Status, "n1") {
		t.Fatalf("single speed status = %q", model.snapshot.Status)
	}
	speeds := model.snapshot.Groups[0].Speeds
	if !speeds["n1"].Testing || !speeds["n2"].Testing {
		t.Fatalf("active speed spinners = n1:%+v n2:%+v", speeds["n1"], speeds["n2"])
	}
	for _, node := range []string{"n3", "n4", "n5"} {
		if speeds[node].Testing {
			t.Fatalf("queued speed %s stayed testing", node)
		}
	}
	for _, node := range []string{"o1", "o2", "o3"} {
		if !model.snapshot.Groups[1].Speeds[node].Testing {
			t.Fatalf("other group %s lost its spinner", node)
		}
	}
	queuedOther := 0
	queuedProxy := 0
	for _, job := range model.speedQueue {
		switch job.group {
		case "Other":
			queuedOther++
		case "Proxy":
			queuedProxy++
			if job.node != "n1" {
				t.Fatalf("proxy speed queue kept %s", job.node)
			}
		default:
			t.Fatalf("unexpected speed job %+v", job)
		}
	}
	if queuedOther != 3 || queuedProxy != 1 {
		t.Fatalf("speed queue other=%d proxy=%d", queuedOther, queuedProxy)
	}

	model.update(tuiProxyGroupSpeedResultMsg{
		epoch: epoch, seq: n1Seq, batch: groupSpeedBatch, pooled: true, groupName: "Proxy", node: "n1",
		result: tuiSpeedResult{BytesPerSecond: 999},
	})
	if model.snapshot.Groups[0].Speeds["n1"].BytesPerSecond != 0 || !model.snapshot.Groups[0].Speeds["n1"].Testing {
		t.Fatalf("stale speed wrote %+v", model.snapshot.Groups[0].Speeds["n1"])
	}
	model.update(tuiProxyGroupSpeedResultMsg{
		epoch: epoch, seq: n2Seq, batch: groupSpeedBatch, pooled: true, groupName: "Proxy", node: "n2",
		result: tuiSpeedResult{BytesPerSecond: 40},
	})
	speed := model.snapshot.Groups[0].Speeds["n2"]
	if speed.Testing || speed.BytesPerSecond != 40 || model.speedInFlight < 0 || model.speedInFlight > tuiSpeedProbeParallelism {
		t.Fatalf("live speed %+v inFlight=%d", speed, model.speedInFlight)
	}
	if strings.Contains(model.snapshot.Status, "/5") || strings.Contains(model.snapshot.Status, "/3") || !strings.Contains(model.snapshot.Status, "0/1") {
		t.Fatalf("speed result restored an abandoned total: %q", model.snapshot.Status)
	}

	model.snapshot.Groups[0].Nodes = []string{"n1", "n2", "n3", "n4", "n5", "n6"}
	if command := model.testSelectedProxyGroupDelays(); command == nil || model.delayInFlight != tuiDelayProbeParallelism || len(model.delayQueue) != 2 {
		t.Fatalf("group delay inFlight=%d queued=%d", model.delayInFlight, len(model.delayQueue))
	}
	groupDelayBatch := model.delayBatch.id
	if groupDelayBatch == 0 {
		t.Fatal("group delay batch id was not assigned")
	}
	n1DelaySeq := model.proxyProbeSeq[proxyProbeKey(tuiProbeDelay, "Proxy", "n1")]
	n2DelaySeq := model.proxyProbeSeq[proxyProbeKey(tuiProbeDelay, "Proxy", "n2")]
	if command := model.testSelectedProxyDelay(); model.delayInFlight < 0 || model.delayInFlight > tuiDelayProbeParallelism {
		t.Fatalf("single delay retest command:%v inFlight=%d", command != nil, model.delayInFlight)
	}
	delays := model.snapshot.Groups[0].Delays
	if !delays["n1"].Testing {
		t.Fatal("retested delay lost its spinner")
	}
	for _, node := range []string{"n2", "n3", "n4"} {
		if !delays[node].Testing {
			t.Fatalf("in-flight delay %s was cleared", node)
		}
	}
	for _, node := range []string{"n5", "n6"} {
		if delays[node].Testing {
			t.Fatalf("queued delay %s stayed testing", node)
		}
	}
	model.update(tuiProxyDelayResultMsg{
		epoch: epoch, seq: n1DelaySeq, batch: groupDelayBatch, pooled: true,
		groupName: "Proxy", node: "n1",
		result: tuiDelayResult{MedianMillis: 999, Samples: 5},
	})
	if model.snapshot.Groups[0].Delays["n1"].MedianMillis != 0 || !model.snapshot.Groups[0].Delays["n1"].Testing {
		t.Fatalf("stale delay wrote %+v", model.snapshot.Groups[0].Delays["n1"])
	}
	model.update(tuiProxyDelayResultMsg{
		epoch: epoch, seq: n2DelaySeq, batch: groupDelayBatch, pooled: true,
		groupName: "Proxy", node: "n2",
		result: tuiDelayResult{MedianMillis: 12, Samples: 5},
	})
	delay := model.snapshot.Groups[0].Delays["n2"]
	retested := model.snapshot.Groups[0].Delays["n1"]
	if delay.Testing || delay.MedianMillis != 12 || retested.MedianMillis != 0 || !retested.Testing || model.delayInFlight < 0 || model.delayInFlight > tuiDelayProbeParallelism {
		t.Fatalf("live delay %+v retested %+v inFlight=%d", delay, retested, model.delayInFlight)
	}
	if strings.Contains(model.snapshot.Status, "/5") || strings.Contains(model.snapshot.Status, "/3") || strings.Contains(model.snapshot.Status, "/6") {
		t.Fatalf("delay result restored an abandoned total: %q", model.snapshot.Status)
	}
}

func TestCoreAndSSHWritesOverlap(t *testing.T) {
	model := newTUIModel(controllerClient{}, cliPaths{HomeDir: t.TempDir()}, nil, false)
	model.service = &tuiServiceClient{}
	model.snapshot.Page = tuiPageSSH
	model.snapshot.FocusSidebar = false
	model.snapshot.SSHProfiles = []tuiSSHProfile{{Name: "home", Username: "user", Host: "192.0.2.1", Port: 22}}
	model.snapshot.SelectedSSH = 0
	model.snapshot.Groups = []tuiGroup{{Name: "Proxy", Nodes: []string{"a"}, Now: "a"}}
	if command := model.runSelectedSSHAction("connect"); command == nil || !model.sshBusy {
		t.Fatal("SSH connect did not start")
	}
	sshSeq := model.sshWriteSequence
	if command := model.startOperation(func(*tuiOperationState) {}); command == nil || !model.coreBusy || !model.sshBusy {
		t.Fatal("SSH write blocked a core write")
	}
	if command := model.testSelectedProxyDelay(); command != nil {
		t.Fatal("core write allowed a proxy probe")
	}
	model.coreBusy = false
	model.syncLaneBusy()
	if command := model.testSelectedProxyDelay(); command == nil || model.coreBusy || !model.sshBusy {
		t.Fatal("SSH write blocked a proxy probe")
	}
	model.update(tuiSSHCommandResultMsg{action: "connect", sequence: sshSeq + 1})
	if !model.sshBusy {
		t.Fatal("mismatched SSH result cleared the lane")
	}
	model.update(tuiSSHCommandResultMsg{action: "connect", sequence: sshSeq})
	if model.sshBusy {
		t.Fatal("matching SSH result left the lane busy")
	}
}
