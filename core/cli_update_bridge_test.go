//go:build linux && !cgo && cli

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFetchLatestCLIReleaseUsesCLIVersionUserAgent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		want := "flclash/" + cliVersion
		if request.UserAgent() != want {
			t.Fatalf("user agent = %q, want %q", request.UserAgent(), want)
		}
		_, _ = io.WriteString(writer, `{"tag_name":"v9.8.7","assets":[]}`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := fetchLatestCLIRelease(ctx, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if release.TagName != "v9.8.7" {
		t.Fatalf("tag = %q", release.TagName)
	}
}
