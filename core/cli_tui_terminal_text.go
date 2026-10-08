//go:build linux && !cgo && cli

package main

func tuiSafeInputViewport(value []rune, cursor, width int) string {
	cursor = minTUI(maxTUIIndex(cursor), len(value))
	cleanCursor := len([]rune(safeCLITerminalLine(string(value[:cursor]))))
	clean := []rune(safeCLITerminalLine(string(value)))
	return tuiInputViewport(clean, minTUI(cleanCursor, len(clean)), width)
}

// Sanitize opaque strings only on a rendering copy. Backend identifiers,
// matching/search inputs and persisted data retain their original bytes.
// Trusted ANSI is added later by the chart, selection and input renderer.
func safeTUIMessageForRender(message tuiMessage, multiline bool) tuiMessage {
	clean := safeCLITerminalLine
	if multiline {
		clean = safeCLITerminalText
	}
	message.Raw = clean(message.Raw)
	message.Args = append([]any(nil), message.Args...)
	for index, argument := range message.Args {
		switch value := argument.(type) {
		case string:
			message.Args[index] = clean(value)
		case tuiMessage:
			message.Args[index] = safeTUIMessageForRender(value, multiline)
		}
	}
	message.Parts = append([]tuiMessage(nil), message.Parts...)
	for index := range message.Parts {
		message.Parts[index] = safeTUIMessageForRender(message.Parts[index], multiline)
	}
	return message
}

func safeTUIConnectionForRender(connection tuiConnection) tuiConnection {
	connection.ID = safeCLITerminalLine(connection.ID)
	connection.Host = safeCLITerminalLine(connection.Host)
	connection.Network = safeCLITerminalLine(connection.Network)
	connection.Chain = safeCLITerminalLine(connection.Chain)
	connection.Process = safeCLITerminalLine(connection.Process)
	connection.ProcessPath = safeCLITerminalLine(connection.ProcessPath)
	connection.SourceIP = safeCLITerminalLine(connection.SourceIP)
	connection.InboundName = safeCLITerminalLine(connection.InboundName)
	connection.InboundUser = safeCLITerminalLine(connection.InboundUser)
	connection.Source = safeCLITerminalLine(connection.Source)
	return connection
}

func safeTUINetworkForRender(info tuiNetworkInfo) tuiNetworkInfo {
	info.PublicIP = safeCLITerminalLine(info.PublicIP)
	info.Country = safeCLITerminalLine(info.Country)
	info.IntranetIP = safeCLITerminalLine(info.IntranetIP)
	info.Route = safeCLITerminalLine(info.Route)
	info.Error = safeCLITerminalLine(info.Error)
	return info
}

func safeTUISnapshotForRender(source tuiSnapshot) tuiSnapshot {
	snapshot := cloneTUISnapshot(source)
	for index := range snapshot.Groups {
		group := &snapshot.Groups[index]
		group.Name, group.Now, group.Type = safeCLITerminalLine(group.Name), safeCLITerminalLine(group.Now), safeCLITerminalLine(group.Type)
		for index, node := range group.Nodes {
			group.Nodes[index] = safeCLITerminalLine(node)
		}
		delays := make(map[string]tuiDelayResult, len(group.Delays))
		for node, delay := range group.Delays {
			delay.Error = safeCLITerminalLine(delay.Error)
			delays[safeCLITerminalLine(node)] = delay
		}
		group.Delays = delays
		speeds := make(map[string]tuiSpeedResult, len(group.Speeds))
		for node, speed := range group.Speeds {
			speed.Error = safeCLITerminalLine(speed.Error)
			speeds[safeCLITerminalLine(node)] = speed
		}
		group.Speeds = speeds
	}
	for index := range snapshot.GroupOrder {
		snapshot.GroupOrder[index] = safeCLITerminalLine(snapshot.GroupOrder[index])
	}
	for index := range snapshot.Connections {
		snapshot.Connections[index] = safeTUIConnectionForRender(snapshot.Connections[index])
	}
	for index := range snapshot.Requests {
		snapshot.Requests[index].TuiConnection = safeTUIConnectionForRender(snapshot.Requests[index].TuiConnection)
	}
	for index := range snapshot.Profiles {
		profile := &snapshot.Profiles[index]
		profile.Name, profile.Path, profile.SubscriptionURL = safeCLITerminalLine(profile.Name), safeCLITerminalLine(profile.Path), safeCLITerminalLine(profile.SubscriptionURL)
	}
	for index := range snapshot.SSHProfiles {
		profile := &snapshot.SSHProfiles[index]
		profile.Name, profile.Username, profile.Host, profile.Destination = safeCLITerminalLine(profile.Name), safeCLITerminalLine(profile.Username), safeCLITerminalLine(profile.Host), safeCLITerminalLine(profile.Destination)
		profile.Jump, profile.Identity, profile.LastError = safeCLITerminalLine(profile.Jump), safeCLITerminalLine(profile.Identity), safeCLITerminalLine(profile.LastError)
		for index := range profile.Options {
			profile.Options[index] = safeCLITerminalLine(profile.Options[index])
		}
	}
	for index := range snapshot.Providers {
		provider := &snapshot.Providers[index]
		provider.Name, provider.Type, provider.Vehicle, provider.UpdatedAt = safeCLITerminalLine(provider.Name), safeCLITerminalLine(provider.Type), safeCLITerminalLine(provider.Vehicle), safeCLITerminalLine(provider.UpdatedAt)
	}
	for index := range snapshot.Logs {
		snapshot.Logs[index] = safeCLITerminalText(snapshot.Logs[index])
	}
	snapshot.Notifications = append([]tuiNotification(nil), source.Notifications...)
	for index := range snapshot.Notifications {
		notification := &snapshot.Notifications[index]
		notification.title = safeCLITerminalLine(notification.title)
		notification.message = safeCLITerminalText(notification.message)
		notification.text = safeTUIMessageForRender(notification.text, true)
	}
	snapshot.Status, snapshot.StatusMessage = safeCLITerminalLine(snapshot.Status), safeTUIMessageForRender(snapshot.StatusMessage, false)
	snapshot.DangerConfirmTitle, snapshot.DangerConfirmMessage = safeCLITerminalLine(snapshot.DangerConfirmTitle), safeCLITerminalText(snapshot.DangerConfirmMessage)
	snapshot.ProfileDelete.Name, snapshot.ProfileDelete.Kind = safeCLITerminalLine(snapshot.ProfileDelete.Name), safeCLITerminalLine(snapshot.ProfileDelete.Kind)
	snapshot.SSHDetailName = safeCLITerminalLine(snapshot.SSHDetailName)
	snapshot.FLCOutbound = safeCLITerminalLine(snapshot.FLCOutbound)
	snapshot.Network, snapshot.SSHNetwork, snapshot.SSHDirectNetwork = safeTUINetworkForRender(snapshot.Network), safeTUINetworkForRender(snapshot.SSHNetwork), safeTUINetworkForRender(snapshot.SSHDirectNetwork)
	snapshot.SSHDirectProbe.IntranetIP, snapshot.SSHDirectProbe.Reason = safeCLITerminalLine(snapshot.SSHDirectProbe.IntranetIP), safeCLITerminalLine(snapshot.SSHDirectProbe.Reason)
	snapshot.SSHForm.Name, snapshot.SSHForm.Username, snapshot.SSHForm.Host, snapshot.SSHForm.Destination = safeCLITerminalLine(snapshot.SSHForm.Name), safeCLITerminalLine(snapshot.SSHForm.Username), safeCLITerminalLine(snapshot.SSHForm.Host), safeCLITerminalLine(snapshot.SSHForm.Destination)
	snapshot.SSHForm.Jump, snapshot.SSHForm.Identity, snapshot.SSHForm.IdentityError, snapshot.SSHForm.DeleteName = safeCLITerminalLine(snapshot.SSHForm.Jump), safeCLITerminalLine(snapshot.SSHForm.Identity), safeCLITerminalLine(snapshot.SSHForm.IdentityError), safeCLITerminalLine(snapshot.SSHForm.DeleteName)
	snapshot.SSHForm.Options = append([]string(nil), source.SSHForm.Options...)
	for index := range snapshot.SSHForm.Options {
		snapshot.SSHForm.Options[index] = safeCLITerminalLine(snapshot.SSHForm.Options[index])
	}
	snapshot.SSHCredentialPrompt.Profile, snapshot.SSHCredentialPrompt.Identity = safeCLITerminalLine(snapshot.SSHCredentialPrompt.Profile), safeCLITerminalLine(snapshot.SSHCredentialPrompt.Identity)
	snapshot.SelectionOptions = append([]string(nil), source.SelectionOptions...)
	for index := range snapshot.SelectionOptions {
		snapshot.SelectionOptions[index] = safeCLITerminalLine(snapshot.SelectionOptions[index])
	}
	snapshot.SelectionTitle = safeCLITerminalLine(snapshot.SelectionTitle)
	snapshot.SelectionHint = safeCLITerminalLine(snapshot.SelectionHint)
	snapshot.InputTitle, snapshot.InputHint = safeCLITerminalLine(snapshot.InputTitle), safeCLITerminalLine(snapshot.InputHint)
	snapshot.InputValue = safeCLITerminalLine(snapshot.InputValue)
	snapshot.SSHForm.FieldInput = safeCLITerminalLine(snapshot.SSHForm.FieldInput)
	snapshot.Settings.Mode, snapshot.Settings.LogLevel = safeCLITerminalLine(snapshot.Settings.Mode), safeCLITerminalLine(snapshot.Settings.LogLevel)
	snapshot.Memory.Error, snapshot.Memory.CoreError = safeCLITerminalLine(snapshot.Memory.Error), safeCLITerminalLine(snapshot.Memory.CoreError)
	snapshot.Update.LatestVersion, snapshot.Update.ReleaseURL, snapshot.Update.Error = safeCLITerminalLine(snapshot.Update.LatestVersion), safeCLITerminalLine(snapshot.Update.ReleaseURL), safeCLITerminalLine(snapshot.Update.Error)
	snapshot.DashboardDelay.Error, snapshot.DashboardSpeed.Error = safeCLITerminalLine(snapshot.DashboardDelay.Error), safeCLITerminalLine(snapshot.DashboardSpeed.Error)
	snapshot.SSHDelay.Error, snapshot.SSHSpeed.Error = safeCLITerminalLine(snapshot.SSHDelay.Error), safeCLITerminalLine(snapshot.SSHSpeed.Error)
	snapshot.SSHDirectDelay.Error, snapshot.SSHDirectSpeed.Error = safeCLITerminalLine(snapshot.SSHDirectDelay.Error), safeCLITerminalLine(snapshot.SSHDirectSpeed.Error)
	return snapshot
}
