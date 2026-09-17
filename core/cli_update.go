//go:build linux && !cgo && cli

package main

import (
	"context"
	"net/http"

	"core/internal/update"
)

func init() {
	update.Version = cliVersion
}

func updateCommand(args []string) error {
	update.Version = cliVersion
	return update.Command(args)
}

func isNewerCLIVersion(latest, current string) bool {
	return update.IsNewer(latest, current)
}

func normalizeCLIVersion(value string) string {
	return update.NormalizeVersion(value)
}

func fetchLatestCLIRelease(ctx context.Context, client *http.Client, endpoint string) (update.Release, error) {
	update.Version = cliVersion
	return update.FetchLatest(ctx, client, endpoint)
}

var cliUpdateHTTPClient = update.HTTPClient()

const (
	cliLatestReleaseAPIURL = update.LatestReleaseAPIURL
	cliUpdateWarning       = update.Warning
)
