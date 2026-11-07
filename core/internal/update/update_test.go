//go:build linux && !cgo && cli

package update

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

func TestMain(m *testing.M) {
	if Version == "" {
		Version = "1.0.1"
	}
	os.Exit(m.Run())
}

type zeroReader struct{}

func (zeroReader) Read(data []byte) (int, error) {
	return len(data), nil
}

func TestFetchLatestCLIRelease(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.UserAgent() != "flclash/"+Version {
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

func TestFetchLatestCLIReleaseRejectsInvalidResponseBoundaries(t *testing.T) {
	for _, test := range []struct {
		name string
		body io.Reader
	}{
		{
			name: "trailing JSON",
			body: strings.NewReader(
				`{"tag_name":"v1.2.3"} {"tag_name":"v9.9.9"}`,
			),
		},
		{
			name: "oversized metadata",
			body: io.LimitReader(zeroReader{}, cliUpdateMaxMetadataBytes+1),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(
				writer http.ResponseWriter,
				request *http.Request,
			) {
				_, _ = io.Copy(writer, test.body)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if _, err := fetchLatestCLIRelease(
				ctx,
				server.Client(),
				server.URL,
			); err == nil {
				t.Fatal("invalid GitHub response was accepted")
			}
		})
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
		{latest: "1.0.0-beta.10", current: "1.0.0-beta.2", newer: true},
		{latest: "1.0.0-beta.2", current: "1.0.0-beta.10", newer: false},
		{latest: "1.0.0-rc.1", current: "1.0.0-beta.11", newer: true},
		{latest: "1.0.0-alpha.1", current: "1.0.0-alpha", newer: true},
		{latest: "1.0.0-01", current: "1.0.0-1", newer: false},
		{latest: "1.0.0+", current: "0.9.0", newer: false},
		{latest: "01.0.0", current: "0.9.0", newer: false},
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

func TestCLIUpdateProgressClearsShorterRedraw(t *testing.T) {
	var output bytes.Buffer
	progress := newCLIDownloadProgress(&output, 2*1024*1024, true)
	progress.columns = 80
	start := time.Now()
	progress.startedAt = start
	progress.written = 1023 * 1024
	progress.draw(start.Add(100*time.Millisecond), false)
	progress.written = 1024 * 1024
	progress.draw(start.Add(time.Second), false)
	progress.written = 2 * 1024 * 1024
	progress.draw(start.Add(2*time.Second), true)

	screen := progressScreen(output.String())
	if strings.Contains(screen, "MB/sMB/s") || strings.HasSuffix(strings.TrimRight(screen, "\n"), "MB/sMB/s") {
		t.Fatalf("shorter progress redraw left the previous tail:\n%q", screen)
	}
	if !strings.Contains(screen, "[============================]  100.0%  2.0 MB/2.0 MB  1.0 MB/s") {
		t.Fatalf("final progress line = %q", screen)
	}
}

func TestCLIUpdateProgressNearlyCompleteStaysConsistent(t *testing.T) {
	if got := formatBytes(1024*1024 - 1); got != "1.0 MB" {
		t.Fatalf("formatBytes(1MiB-1) = %q", got)
	}
	line := renderCLIProgressLine(1024*1024-1, 1024*1024, float64(1024*1024-1), 0)
	if strings.Contains(line, "100.0%") || strings.Contains(line, "1024.0 KB") || strings.Contains(line, "[============================]") {
		t.Fatalf("nearly complete line looks finished: %q", line)
	}
	if !strings.Contains(line, "99.9%") || !strings.Contains(line, "1.0 MB/1.0 MB") {
		t.Fatalf("nearly complete line = %q", line)
	}
}

func TestCLIUpdateProgressFitsNarrowTerminal(t *testing.T) {
	const (
		half  = 6 * 1024 * 1024
		whole = 12 * 1024 * 1024
		huge  = int64(1) << 62
	)
	// Every width the bar shrinker can be asked to fit, including 20 and 40.
	for width := 2; width <= 80; width++ {
		assertCLIProgressFits(t, half, whole, float64(half), time.Second, width)
		assertCLIProgressFits(t, huge, 0, float64(huge), time.Second, width)
	}
}

func assertCLIProgressFits(t *testing.T, written, total int64, speed float64, elapsed time.Duration, width int) {
	t.Helper()
	full := renderCLIProgressLine(written, total, speed, 0)
	line := renderCLIProgressLine(written, total, speed, width)
	if width > 1 && len(line) >= width {
		t.Fatalf("width %d progress line is %d columns: %q", width, len(line), line)
	}
	if total <= 0 && len(full) >= width && (len(line) != width-1 || !strings.HasSuffix(line, ".")) {
		t.Fatalf("width %d did not clip %q to %q", width, full, line)
	}
	var output bytes.Buffer
	progress := newCLIDownloadProgress(&output, total, true)
	progress.columns = width
	now := time.Now()
	progress.startedAt = now.Add(-elapsed)
	progress.written = written
	progress.draw(now, false)
	raw := output.String()
	if strings.Contains(raw, "\n") {
		t.Fatalf("width %d progress wrapped or finished early: %q", width, raw)
	}
	payload := drawnCLIProgressLine(raw)
	if payload != line || (width > 1 && len(payload) >= width) {
		t.Fatalf("width %d drawn line is %d columns: %q want %q", width, len(payload), payload, line)
	}
	if total > 0 && width >= 8 && !strings.Contains(payload, "%") {
		t.Fatalf("width %d progress lost the percent: %q", width, payload)
	}
}

func drawnCLIProgressLine(raw string) string {
	start := strings.LastIndex(raw, "\r")
	if start < 0 {
		return raw
	}
	payload := raw[start+1:]
	if end := strings.Index(payload, "\033[K"); end >= 0 {
		payload = payload[:end]
	}
	return payload
}

func TestCLIUpdateProgressClearsWrappedLineAfterResize(t *testing.T) {
	var output bytes.Buffer
	progress := newCLIDownloadProgress(&output, 12*1024*1024, true)
	progress.columns = 80
	progress.startedAt = time.Now().Add(-time.Second)
	progress.written = 6 * 1024 * 1024
	progress.draw(time.Now(), false)
	progress.columns = 20
	progress.draw(time.Now().Add(time.Second), false)
	screen := progressScreen(output.String())
	for _, line := range strings.Split(screen, "\n") {
		if line != "" && len(line) >= 20 {
			t.Fatalf("resized progress still occupies %d columns: %q", len(line), screen)
		}
	}
	if strings.TrimSpace(screen) == "" || !strings.Contains(screen, "%") {
		t.Fatalf("resized progress = %q", screen)
	}
}

// progressScreen applies the cursor controls used by the download line.
func progressScreen(raw string) string {
	lines := []string{""}
	row, column := 0, 0
	ensure := func() {
		for len(lines) <= row {
			lines = append(lines, "")
		}
	}
	for index := 0; index < len(raw); {
		if raw[index] == '\r' {
			column = 0
			index++
			continue
		}
		if raw[index] == '\n' {
			row++
			column = 0
			ensure()
			index++
			continue
		}
		if raw[index] == '\033' && index+1 < len(raw) && raw[index+1] == '[' {
			end := index + 2
			for end < len(raw) && raw[end] >= '0' && raw[end] <= '9' {
				end++
			}
			if end < len(raw) {
				command := raw[end]
				argument := 1
				if number := raw[index+2 : end]; number != "" {
					argument = 0
					for _, digit := range number {
						argument = argument*10 + int(digit-'0')
					}
				}
				index = end + 1
				switch command {
				case 'K':
					ensure()
					if column < len(lines[row]) {
						lines[row] = lines[row][:column]
					}
				case 'A':
					row -= argument
					if row < 0 {
						row = 0
					}
				case 'B':
					row += argument
					ensure()
				}
				continue
			}
		}
		ensure()
		if column < len(lines[row]) {
			text := []byte(lines[row])
			text[column] = raw[index]
			lines[row] = string(text)
		} else {
			lines[row] += raw[index : index+1]
		}
		column++
		index++
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
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
