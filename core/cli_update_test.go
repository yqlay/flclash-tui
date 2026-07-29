//go:build linux && !cgo && cli

package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFetchLatestCLIRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.UserAgent() != "flclash-cli/"+cliVersion {
			t.Fatalf("user agent = %q", request.UserAgent())
		}
		if request.Header.Get("Accept") != "application/vnd.github+json" {
			t.Fatalf("accept = %q", request.Header.Get("Accept"))
		}
		_, _ = io.WriteString(writer, `{
			"tag_name":"v9.8.7",
			"name":"FlClash CLI v9.8.7",
			"html_url":"https://github.example/releases/v9.8.7",
			"assets":[
				{
					"name":"flclash-cli_9.8.7_amd64.deb",
					"browser_download_url":"https://github.example/update.deb",
					"size":123
				}
			]
		}`)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	release, err := fetchLatestCLIRelease(ctx, server.Client(), server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if release.TagName != "v9.8.7" ||
		release.HTMLURL != "https://github.example/releases/v9.8.7" ||
		len(release.Assets) != 1 {
		t.Fatalf("release = %+v", release)
	}
}

func TestCLIVersionComparison(t *testing.T) {
	tests := []struct {
		latest  string
		current string
		newer   bool
	}{
		{latest: "0.3.4", current: "0.3.3", newer: true},
		{latest: "v1.0.0", current: "0.9.9", newer: true},
		{latest: "0.3.3", current: "0.3.3", newer: false},
		{latest: "0.3.2", current: "0.3.3", newer: false},
		{latest: "1.0.0", current: "1.0.0-beta.1", newer: true},
		{latest: "1.0.0-beta.1", current: "1.0.0", newer: false},
		{latest: "invalid", current: "0.3.3", newer: false},
	}
	for _, test := range tests {
		if got := isNewerCLIVersion(test.latest, test.current); got != test.newer {
			t.Fatalf(
				"isNewerCLIVersion(%q, %q) = %t, want %t",
				test.latest,
				test.current,
				got,
				test.newer,
			)
		}
	}
}

func TestSelectCLIUpdateAssets(t *testing.T) {
	release := cliRelease{
		TagName: "v0.3.4",
		Assets: []cliReleaseAsset{
			{
				Name:        "flclash-cli_0.3.4_amd64.deb",
				DownloadURL: "https://github.com/yqlay/flclash-cli/releases/download/v0.3.4/flclash-cli_0.3.4_amd64.deb",
			},
			{
				Name:        "flclash-cli_0.3.4_amd64.deb.sha256",
				DownloadURL: "https://github.com/yqlay/flclash-cli/releases/download/v0.3.4/flclash-cli_0.3.4_amd64.deb.sha256",
			},
		},
	}
	deb, checksum, err := selectCLIUpdateAssets(release, "0.3.4", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(deb.Name, "_amd64.deb") ||
		checksum.Name != deb.Name+".sha256" {
		t.Fatalf("selected assets = %+v %+v", deb, checksum)
	}
	if _, _, err := selectCLIUpdateAssets(release, "0.3.4", "mips64"); err == nil {
		t.Fatal("unsupported architecture was accepted")
	}
	release.Assets[0].DownloadURL = "https://example.test/update.deb"
	if _, _, err := selectCLIUpdateAssets(release, "0.3.4", "amd64"); err == nil {
		t.Fatal("untrusted update URL was accepted")
	}
}

func TestDownloadAndVerifyCLIUpdate(t *testing.T) {
	debData := []byte("test Debian package")
	checksum := sha256.Sum256(debData)
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/update.deb":
			_, _ = writer.Write(debData)
		case "/update.deb.sha256":
			_, _ = fmt.Fprintf(
				writer,
				"%x  flclash-cli_9.8.7_amd64.deb\n",
				checksum,
			)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	directory := t.TempDir()
	debPath := filepath.Join(directory, "update.deb")
	checksumPath := debPath + ".sha256"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := downloadCLIUpdateAsset(
		ctx,
		server.Client(),
		cliReleaseAsset{
			Name:        "update.deb",
			DownloadURL: server.URL + "/update.deb",
			Size:        int64(len(debData)),
		},
		debPath,
	); err != nil {
		t.Fatal(err)
	}
	checksumData := fmt.Appendf(
		nil,
		"%x  flclash-cli_9.8.7_amd64.deb\n",
		checksum,
	)
	if err := downloadCLIUpdateAsset(
		ctx,
		server.Client(),
		cliReleaseAsset{
			Name:        "update.deb.sha256",
			DownloadURL: server.URL + "/update.deb.sha256",
			Size:        int64(len(checksumData)),
		},
		checksumPath,
	); err != nil {
		t.Fatal(err)
	}
	if err := verifyCLIUpdateChecksum(debPath, checksumPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(debPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyCLIUpdateChecksum(debPath, checksumPath); err == nil {
		t.Fatal("tampered update passed checksum verification")
	}
}

func TestConfirmCLIUpdateDefaultsToNo(t *testing.T) {
	for _, test := range []struct {
		input     string
		confirmed bool
	}{
		{input: "\n", confirmed: false},
		{input: "n\n", confirmed: false},
		{input: "y\n", confirmed: true},
		{input: "YES\n", confirmed: true},
	} {
		var output bytes.Buffer
		confirmed, err := confirmCLIUpdate(strings.NewReader(test.input), &output)
		if err != nil {
			t.Fatal(err)
		}
		if confirmed != test.confirmed {
			t.Fatalf("input %q confirmed = %t", test.input, confirmed)
		}
		if !strings.Contains(output.String(), "[y/N]") {
			t.Fatalf("confirmation prompt = %q", output.String())
		}
	}
}
