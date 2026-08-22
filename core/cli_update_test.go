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
	"os/exec"
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
		if request.UserAgent() != "flclash/"+cliVersion {
			t.Fatalf("user agent = %q", request.UserAgent())
		}
		if request.Header.Get("Accept") != "application/vnd.github+json" {
			t.Fatalf("accept = %q", request.Header.Get("Accept"))
		}
		_, _ = io.WriteString(writer, `{
			"tag_name":"v9.8.7",
			"name":"FlClash TUI v9.8.7",
			"html_url":"https://github.example/releases/v9.8.7",
			"assets":[
				{
					"name":"flclash-tui_9.8.7_amd64.deb",
					"browser_download_url":"https://github.example/update.deb",
					"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
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
		len(release.Assets) != 1 ||
		release.Assets[0].Digest != "sha256:"+strings.Repeat("a", 64) {
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
				Name:        "flclash-tui_0.3.4_amd64.deb",
				DownloadURL: "https://github.com/yqlay/flclash-tui/releases/download/v0.3.4/flclash-tui_0.3.4_amd64.deb",
				Digest:      "sha256:" + strings.Repeat("a", 64),
			},
		},
	}
	deb, err := selectCLIUpdateAsset(release, "0.3.4", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(deb.Name, "_amd64.deb") {
		t.Fatalf("selected asset = %+v", deb)
	}
	if _, err := selectCLIUpdateAsset(release, "0.3.4", "mips64"); err == nil {
		t.Fatal("unsupported architecture was accepted")
	}
	release.Assets[0].DownloadURL = "https://example.test/update.deb"
	if _, err := selectCLIUpdateAsset(release, "0.3.4", "amd64"); err == nil {
		t.Fatal("untrusted update URL was accepted")
	}
	release.Assets[0].DownloadURL = "https://github.com/yqlay/flclash-tui/releases/download/v0.3.4/flclash-tui_0.3.4_amd64.deb"
	release.Assets[0].Digest = ""
	if _, err := selectCLIUpdateAsset(release, "0.3.4", "amd64"); err == nil {
		t.Fatal("asset without a GitHub digest was accepted")
	}
}

func TestSelectCLIUpdateAssetsSupportsRenamesAndLegacyPackages(
	t *testing.T,
) {
	for _, test := range []struct {
		name       string
		goArch     string
		repository string
		debName    string
	}{
		{
			name:       "legacy package",
			goArch:     "amd64",
			repository: "flclash-tui",
			debName:    "flclash-cli_0.3.12_amd64.deb",
		},
		{
			name:       "renamed repository and package",
			goArch:     "arm64",
			repository: "flclash-terminal",
			debName:    "flclash-terminal-v0.3.12-linux-aarch64.deb",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			baseURL := "https://github.com/yqlay/" + test.repository +
				"/releases/download/v0.3.12/"
			release := cliRelease{
				TagName: "v0.3.12",
				HTMLURL: "https://github.com/yqlay/" +
					test.repository + "/releases/tag/v0.3.12",
				Assets: []cliReleaseAsset{
					{
						Name:        test.debName,
						DownloadURL: baseURL + test.debName,
						Digest:      "sha256:" + strings.Repeat("b", 64),
					},
				},
			}
			deb, err := selectCLIUpdateAsset(
				release,
				"0.3.12",
				test.goArch,
			)
			if err != nil {
				t.Fatal(err)
			}
			if deb.Name != test.debName {
				t.Fatalf("selected asset = %q", deb.Name)
			}
		})
	}
}

func TestSelectCLIUpdateAssetsRejectsAmbiguousPackages(t *testing.T) {
	release := cliRelease{
		TagName: "v0.3.12",
		Assets: []cliReleaseAsset{
			{
				Name: "flclash-one_0.3.12_amd64.deb",
			},
			{
				Name: "flclash-two_0.3.12_amd64.deb",
			},
		},
	}
	if _, err := selectCLIUpdateAsset(
		release,
		"0.3.12",
		"amd64",
	); err == nil {
		t.Fatal("ambiguous Debian assets were accepted")
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
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	directory := t.TempDir()
	debPath := filepath.Join(directory, "update.deb")
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
	digest := fmt.Sprintf("sha256:%x", checksum)
	if err := verifyCLIUpdateDigest(debPath, digest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(debPath, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyCLIUpdateDigest(debPath, digest); err == nil {
		t.Fatal("tampered update passed checksum verification")
	}
}

func TestParseCLIUpdateDigestRejectsInvalidValues(t *testing.T) {
	for _, digest := range []string{
		"",
		"md5:" + strings.Repeat("a", 32),
		"sha256:short",
		"sha256:" + strings.Repeat("z", 64),
	} {
		if _, err := parseCLIUpdateDigest(digest); err == nil {
			t.Fatalf("invalid digest %q was accepted", digest)
		}
	}
}

func TestValidateCLIUpdateDebianPackageMetadata(t *testing.T) {
	if _, err := exec.LookPath("dpkg-deb"); err != nil {
		t.Skip("dpkg-deb is unavailable")
	}
	root := filepath.Join(t.TempDir(), "package")
	if err := os.MkdirAll(filepath.Join(root, "DEBIAN"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "usr", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	control := `Package: flclash-next
Version: 0.3.12-1
Architecture: amd64
Maintainer: test <test@example.com>
Description: updater metadata test
`
	if err := os.WriteFile(
		filepath.Join(root, "DEBIAN", "control"),
		[]byte(control),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "usr", "bin", "flclash"),
		[]byte("#!/bin/sh\n"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}
	debPath := filepath.Join(t.TempDir(), "flclash.deb")
	if output, err := exec.Command(
		"dpkg-deb",
		"--build",
		"--root-owner-group",
		root,
		debPath,
	).CombinedOutput(); err != nil {
		t.Fatalf("build test package: %v: %s", err, output)
	}
	if err := validateCLIUpdateDebianPackage(
		debPath,
		"0.3.12",
		"amd64",
	); err != nil {
		t.Fatal(err)
	}
	if err := validateCLIUpdateDebianPackage(
		debPath,
		"0.3.12",
		"arm64",
	); err == nil {
		t.Fatal("wrong package architecture was accepted")
	}
}

func TestCLIUpdateProgressShowsBarPercentageAndSpeed(t *testing.T) {
	var output bytes.Buffer
	progress := newCLIDownloadProgress(&output, 1024, false)
	progress.startedAt = time.Now().Add(-time.Second)
	if _, err := progress.Write(make([]byte, 1024)); err != nil {
		t.Fatal(err)
	}
	progress.finish(true)

	line := strings.TrimSpace(output.String())
	for _, expected := range []string{
		"[============================]",
		"100.0%",
		"1.0 KB/1.0 KB",
	} {
		if !strings.Contains(line, expected) {
			t.Fatalf("progress does not contain %q: %q", expected, line)
		}
	}
	if !strings.HasSuffix(line, "/s") {
		t.Fatalf("download speed is not on the right side: %q", line)
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
