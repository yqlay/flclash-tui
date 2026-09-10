//go:build linux && !cgo && cli

package main

import (
	"net/http"

	"core/internal/subscription"
)

const (
	tuiSubscriptionUserAgent = subscription.UserAgent
	tuiSubscriptionMaxBytes  = subscription.MaxBytes
)

type tuiSubscriptionPayload = subscription.Payload

func normalizeTUISubscription(data []byte) (tuiSubscriptionPayload, error) {
	return subscription.Normalize(data)
}

func tuiNewSubscriptionFileName(disposition string) string {
	return subscription.NewFileName(disposition)
}

func tuiSubscriptionImportPath(homeDir string, payload tuiSubscriptionPayload) (string, error) {
	return subscription.ImportPath(homeDir, payload)
}

func tuiImportedProfileName(sourceName string) string {
	return subscription.ImportedProfileName(sourceName)
}

func nextTUIImportedProfilePath(homeDir, sourceName string) (string, error) {
	return subscription.NextImportedProfilePath(homeDir, sourceName)
}

func fetchTUISubscription(value string) ([]byte, error) {
	return subscription.Fetch(value)
}

func fetchTUISubscriptionDetails(value string) (tuiSubscriptionPayload, error) {
	return subscription.FetchDetails(value)
}

func newTUISubscriptionRequest(value string) (*http.Request, error) {
	return subscription.NewRequest(value)
}

func buildTUIProxyProfile(proxies []map[string]any, format string) (tuiSubscriptionPayload, error) {
	return subscription.BuildProxyProfile(proxies, format)
}

func tuiAnyMap(value any) (map[string]any, bool) {
	return subscription.AnyMap(value)
}

func tuiAnyString(value any) string {
	return subscription.AnyString(value)
}
