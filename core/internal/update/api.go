//go:build linux && !cgo && cli

package update

import (
	"context"
	"net/http"
)

const (
	LatestReleaseAPIURL = cliLatestReleaseAPIURL
	Warning             = cliUpdateWarning
)

func HTTPClient() *http.Client {
	return cliUpdateHTTPClient
}

type Release = cliRelease

func IsNewer(latest, current string) bool {
	return isNewerCLIVersion(latest, current)
}

func NormalizeVersion(value string) string {
	return normalizeCLIVersion(value)
}

func FetchLatest(ctx context.Context, client *http.Client, apiURL string) (Release, error) {
	return fetchLatestCLIRelease(ctx, client, apiURL)
}
