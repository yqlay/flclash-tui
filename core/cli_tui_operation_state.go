//go:build linux && !cgo && cli

package main

type tuiOperationTestingIndicators struct {
	dashboardDelay, dashboardSpeed bool
	delays, speeds                 map[string][]string
}

func captureTUIOperationTestingIndicators(snapshot tuiSnapshot) tuiOperationTestingIndicators {
	indicators := tuiOperationTestingIndicators{dashboardDelay: snapshot.DashboardDelay.Testing, dashboardSpeed: snapshot.DashboardSpeed.Testing, delays: make(map[string][]string), speeds: make(map[string][]string)}
	for _, group := range snapshot.Groups {
		for node, delay := range group.Delays {
			if delay.Testing {
				indicators.delays[group.Name] = append(indicators.delays[group.Name], node)
			}
		}
		for node, speed := range group.Speeds {
			if speed.Testing {
				indicators.speeds[group.Name] = append(indicators.speeds[group.Name], node)
			}
		}
	}
	return indicators
}

// A rejected result must finish only its own spinners. Results cannot clear a
// new operation's indicators after it has acquired a later operation token.
func (m *tuiModel) finishTUIOperationIndicators(state tuiOperationState) {
	if state.operationID != 0 && state.operationID != m.operationSequence {
		return
	}
	indicators := state.testingIndicators
	if state.operationID == 0 {
		indicators = captureTUIOperationTestingIndicators(state.snapshot)
	}
	if indicators.dashboardDelay && m.snapshot.DashboardDelay.Testing {
		m.snapshot.DashboardDelay = tuiDelayResult{}
	}
	if indicators.dashboardSpeed && m.snapshot.DashboardSpeed.Testing {
		m.snapshot.DashboardSpeed = tuiSpeedResult{}
	}
	for index := range m.snapshot.Groups {
		group := &m.snapshot.Groups[index]
		for _, node := range indicators.delays[group.Name] {
			if group.Delays[node].Testing {
				delete(group.Delays, node)
			}
		}
		for _, node := range indicators.speeds[group.Name] {
			if group.Speeds[node].Testing {
				delete(group.Speeds, node)
			}
		}
	}
}
