//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

const (
	cliGitHubRepository       = "yqlay/flclash-tui"
	cliLatestReleaseAPIURL    = "https://api.github.com/repos/" + cliGitHubRepository + "/releases/latest"
	cliUpdateWarning          = "If this version works well, do not update lightly. / 当前版本使用正常时，请勿轻易更新。"
	cliUpdateMaxMetadataBytes = 1 << 20
	cliUpdateMaxAssetBytes    = 256 << 20
	cliUpdateTimeout          = 30 * time.Minute
)

var cliUpdateHTTPClient = &http.Client{Timeout: cliUpdateTimeout}

type cliReleaseAsset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

type cliRelease struct {
	TagName    string            `json:"tag_name"`
	Name       string            `json:"name"`
	HTMLURL    string            `json:"html_url"`
	Draft      bool              `json:"draft"`
	Prerelease bool              `json:"prerelease"`
	Assets     []cliReleaseAsset `json:"assets"`
}

func updateCommand(args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	checkOnly := fs.Bool("check", false, "only check whether an update is available")
	yes := fs.Bool("yes", false, "confirm the update without prompting")
	downloadOnly := fs.Bool("download-only", false, "download and verify without installing")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("update does not accept positional arguments")
	}

	ctx, cancel := context.WithTimeout(context.Background(), cliUpdateTimeout)
	defer cancel()
	release, err := fetchLatestCLIRelease(
		ctx,
		cliUpdateHTTPClient,
		cliLatestReleaseAPIURL,
	)
	if err != nil {
		return fmt.Errorf("check GitHub release: %w", err)
	}
	latestVersion := normalizeCLIVersion(release.TagName)
	if latestVersion == "" {
		return fmt.Errorf("GitHub release has invalid version %q", release.TagName)
	}
	if !isNewerCLIVersion(latestVersion, cliVersion) {
		fmt.Printf(
			"FlClash TUI %s is already up to date (latest: %s).\n",
			cliVersion,
			latestVersion,
		)
		return nil
	}

	fmt.Printf("Update available: %s -> %s\n", cliVersion, latestVersion)
	fmt.Println(cliUpdateWarning)
	if release.HTMLURL != "" {
		fmt.Printf("Release: %s\n", release.HTMLURL)
	}
	if *checkOnly {
		return nil
	}
	if !*yes {
		if !isInteractiveInput() {
			return errors.New("confirmation required; rerun with --yes after reviewing the warning")
		}
		confirmed, confirmErr := confirmCLIUpdate(os.Stdin, os.Stdout)
		if confirmErr != nil {
			return confirmErr
		}
		if !confirmed {
			fmt.Println("Update cancelled.")
			return nil
		}
	}

	debAsset, checksumAsset, err := selectCLIUpdateAssets(release, latestVersion, runtime.GOARCH)
	if err != nil {
		return err
	}
	updateDirectory, err := cliUpdateDirectory()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(updateDirectory, 0o700); err != nil {
		return fmt.Errorf("create update directory: %w", err)
	}
	debPath := filepath.Join(updateDirectory, debAsset.Name)
	checksumPath := filepath.Join(updateDirectory, checksumAsset.Name)
	fmt.Printf("Downloading %s...\n", debAsset.Name)
	if err := downloadCLIUpdateAssetWithProgress(
		ctx,
		cliUpdateHTTPClient,
		debAsset,
		debPath,
		os.Stdout,
		isCLIProgressTerminal(os.Stdout),
	); err != nil {
		return err
	}
	if err := downloadCLIUpdateAsset(
		ctx,
		cliUpdateHTTPClient,
		checksumAsset,
		checksumPath,
	); err != nil {
		return err
	}
	if err := verifyCLIUpdateChecksum(debPath, checksumPath); err != nil {
		return fmt.Errorf("verify update: %w", err)
	}
	fmt.Printf("Verified SHA-256: %s\n", debPath)
	if *downloadOnly {
		fmt.Printf("Downloaded update. Install when ready:\n  sudo dpkg -i %s\n", debPath)
		return nil
	}
	if err := installCLIUpdate(debPath); err != nil {
		return err
	}
	fmt.Printf("FlClash TUI %s installed. Start flclash again to use it.\n", latestVersion)
	return nil
}

func fetchLatestCLIRelease(
	ctx context.Context,
	client *http.Client,
	endpoint string,
) (cliRelease, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return cliRelease{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "flclash/"+cliVersion)
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := client.Do(request)
	if err != nil {
		return cliRelease{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return cliRelease{}, fmt.Errorf(
			"GitHub returned %s: %s",
			response.Status,
			strings.TrimSpace(string(body)),
		)
	}
	var release cliRelease
	decoder := json.NewDecoder(
		io.LimitReader(response.Body, cliUpdateMaxMetadataBytes),
	)
	if err := decoder.Decode(&release); err != nil {
		return cliRelease{}, err
	}
	if release.Draft {
		return cliRelease{}, errors.New("latest GitHub release is still a draft")
	}
	if release.TagName == "" {
		return cliRelease{}, errors.New("GitHub release does not contain a tag")
	}
	return release, nil
}

func normalizeCLIVersion(value string) string {
	value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "v"))
	if _, ok := parseCLIVersion(value); !ok {
		return ""
	}
	return value
}

type cliSemanticVersion struct {
	numbers    [3]int
	prerelease string
}

func parseCLIVersion(value string) (cliSemanticVersion, bool) {
	value = strings.TrimSpace(strings.TrimPrefix(value, "v"))
	version := cliSemanticVersion{}
	if value == "" {
		return version, false
	}
	if index := strings.IndexByte(value, '+'); index >= 0 {
		value = value[:index]
	}
	if index := strings.IndexByte(value, '-'); index >= 0 {
		version.prerelease = value[index+1:]
		value = value[:index]
		if version.prerelease == "" {
			return cliSemanticVersion{}, false
		}
	}
	parts := strings.Split(value, ".")
	if len(parts) != len(version.numbers) {
		return cliSemanticVersion{}, false
	}
	for index, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return cliSemanticVersion{}, false
		}
		version.numbers[index] = number
	}
	return version, true
}

func isNewerCLIVersion(latest, current string) bool {
	latestVersion, latestOK := parseCLIVersion(latest)
	currentVersion, currentOK := parseCLIVersion(current)
	if !latestOK || !currentOK {
		return false
	}
	for index := range latestVersion.numbers {
		if latestVersion.numbers[index] != currentVersion.numbers[index] {
			return latestVersion.numbers[index] > currentVersion.numbers[index]
		}
	}
	if latestVersion.prerelease == currentVersion.prerelease {
		return false
	}
	if latestVersion.prerelease == "" {
		return true
	}
	if currentVersion.prerelease == "" {
		return false
	}
	return latestVersion.prerelease > currentVersion.prerelease
}

func selectCLIUpdateAssets(
	release cliRelease,
	version,
	goArch string,
) (cliReleaseAsset, cliReleaseAsset, error) {
	debArch := map[string]string{
		"amd64": "amd64",
		"arm64": "arm64",
		"386":   "i386",
	}[goArch]
	if debArch == "" {
		return cliReleaseAsset{}, cliReleaseAsset{}, fmt.Errorf(
			"automatic update is not supported on architecture %s",
			goArch,
		)
	}
	debName := fmt.Sprintf("flclash-tui_%s_%s.deb", version, debArch)
	checksumName := debName + ".sha256"
	var debAsset cliReleaseAsset
	var checksumAsset cliReleaseAsset
	for _, asset := range release.Assets {
		switch asset.Name {
		case debName:
			debAsset = asset
		case checksumName:
			checksumAsset = asset
		}
	}
	if debAsset.DownloadURL == "" || checksumAsset.DownloadURL == "" {
		return cliReleaseAsset{}, cliReleaseAsset{}, fmt.Errorf(
			"release %s does not contain %s and its checksum",
			release.TagName,
			debName,
		)
	}
	for _, asset := range []cliReleaseAsset{debAsset, checksumAsset} {
		if err := validateCLIUpdateAssetURL(asset.DownloadURL); err != nil {
			return cliReleaseAsset{}, cliReleaseAsset{}, fmt.Errorf(
				"release asset %s: %w",
				asset.Name,
				err,
			)
		}
	}
	return debAsset, checksumAsset, nil
}

func validateCLIUpdateAssetURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return errors.New("download URL is invalid")
	}
	expectedPrefix := "/" + cliGitHubRepository + "/releases/download/"
	if parsed.Scheme != "https" ||
		!strings.EqualFold(parsed.Hostname(), "github.com") ||
		!strings.HasPrefix(parsed.EscapedPath(), expectedPrefix) {
		return errors.New("download URL is outside the trusted GitHub repository")
	}
	return nil
}

func cliUpdateDirectory() (string, error) {
	cacheDirectory, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("resolve user cache directory: %w", err)
	}
	return filepath.Join(cacheDirectory, "flclash-tui", "updates"), nil
}

func downloadCLIUpdateAsset(
	ctx context.Context,
	client *http.Client,
	asset cliReleaseAsset,
	target string,
) error {
	return downloadCLIUpdateAssetWithProgress(
		ctx,
		client,
		asset,
		target,
		nil,
		false,
	)
}

func downloadCLIUpdateAssetWithProgress(
	ctx context.Context,
	client *http.Client,
	asset cliReleaseAsset,
	target string,
	progressOutput io.Writer,
	interactiveProgress bool,
) error {
	if asset.DownloadURL == "" {
		return fmt.Errorf("asset %s has no download URL", asset.Name)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, asset.DownloadURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "flclash/"+cliVersion)
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset.Name, err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("download %s: server returned %s", asset.Name, response.Status)
	}
	if response.ContentLength > cliUpdateMaxAssetBytes ||
		asset.Size > cliUpdateMaxAssetBytes {
		return fmt.Errorf("download %s: asset is too large", asset.Name)
	}
	temp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	total := asset.Size
	if total <= 0 {
		total = response.ContentLength
	}
	progress := newCLIDownloadProgress(
		progressOutput,
		total,
		interactiveProgress,
	)
	written, copyErr := io.Copy(
		temp,
		io.TeeReader(
			io.LimitReader(response.Body, cliUpdateMaxAssetBytes+1),
			progress,
		),
	)
	if copyErr != nil {
		progress.finish(false)
		_ = temp.Close()
		return fmt.Errorf("download %s: %w", asset.Name, copyErr)
	}
	if written > cliUpdateMaxAssetBytes {
		progress.finish(false)
		_ = temp.Close()
		return fmt.Errorf("download %s: asset exceeded size limit", asset.Name)
	}
	if asset.Size > 0 && written != asset.Size {
		progress.finish(false)
		_ = temp.Close()
		return fmt.Errorf(
			"download %s: received %d bytes, expected %d",
			asset.Name,
			written,
			asset.Size,
		)
	}
	progress.finish(true)
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, target); err != nil {
		return err
	}
	return nil
}

type cliDownloadProgress struct {
	output      io.Writer
	total       int64
	written     int64
	startedAt   time.Time
	lastDrawnAt time.Time
	interactive bool
	finished    bool
}

func newCLIDownloadProgress(
	output io.Writer,
	total int64,
	interactive bool,
) *cliDownloadProgress {
	now := time.Now()
	return &cliDownloadProgress{
		output:      output,
		total:       total,
		startedAt:   now,
		lastDrawnAt: now,
		interactive: interactive,
	}
}

func (p *cliDownloadProgress) Write(data []byte) (int, error) {
	p.written += int64(len(data))
	now := time.Now()
	if p.interactive && now.Sub(p.lastDrawnAt) >= 100*time.Millisecond {
		p.draw(now, false)
	}
	return len(data), nil
}

func (p *cliDownloadProgress) finish(success bool) {
	if p.finished || p.output == nil {
		return
	}
	p.finished = true
	now := time.Now()
	if success {
		p.draw(now, true)
		return
	}
	if p.interactive {
		_, _ = fmt.Fprintln(p.output)
	}
}

func (p *cliDownloadProgress) draw(now time.Time, complete bool) {
	if p.output == nil {
		return
	}
	elapsed := now.Sub(p.startedAt)
	if elapsed <= 0 {
		elapsed = time.Nanosecond
	}
	speed := float64(p.written) / elapsed.Seconds()
	const barWidth = 28
	if p.total > 0 {
		ratio := float64(p.written) / float64(p.total)
		if ratio > 1 {
			ratio = 1
		}
		filled := int(ratio * barWidth)
		bar := strings.Repeat("=", filled) + strings.Repeat(".", barWidth-filled)
		prefix := "\r"
		if !p.interactive {
			prefix = ""
		}
		_, _ = fmt.Fprintf(
			p.output,
			"%s[%s] %6.1f%%  %s/%s  %s/s",
			prefix,
			bar,
			ratio*100,
			formatBytes(p.written),
			formatBytes(p.total),
			formatBytes(int64(speed)),
		)
	} else {
		prefix := "\r"
		if !p.interactive {
			prefix = ""
		}
		_, _ = fmt.Fprintf(
			p.output,
			"%sDownloaded %s  %s/s",
			prefix,
			formatBytes(p.written),
			formatBytes(int64(speed)),
		)
	}
	p.lastDrawnAt = now
	if complete {
		_, _ = fmt.Fprintln(p.output)
	}
}

func isCLIProgressTerminal(output io.Writer) bool {
	file, ok := output.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func verifyCLIUpdateChecksum(debPath, checksumPath string) error {
	checksumData, err := os.ReadFile(checksumPath)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(checksumData))
	if len(fields) < 1 || len(fields[0]) != sha256.Size*2 {
		return errors.New("checksum file is malformed")
	}
	expected, err := hex.DecodeString(fields[0])
	if err != nil {
		return errors.New("checksum file is malformed")
	}
	file, err := os.Open(debPath)
	if err != nil {
		return err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return err
	}
	if !equalCLIBytes(hash.Sum(nil), expected) {
		return errors.New("SHA-256 checksum mismatch")
	}
	return nil
}

func equalCLIBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	difference := byte(0)
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}

func isInteractiveInput() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func confirmCLIUpdate(reader io.Reader, writer io.Writer) (bool, error) {
	if _, err := fmt.Fprint(writer, "Download, verify, and install this update? [y/N]: "); err != nil {
		return false, err
	}
	value, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	value = strings.ToLower(strings.TrimSpace(value))
	return value == "y" || value == "yes", nil
}

func installCLIUpdate(debPath string) error {
	arguments := []string{"dpkg", "-i", debPath}
	commandName := "sudo"
	if os.Geteuid() == 0 {
		commandName = "dpkg"
		arguments = []string{"-i", debPath}
	} else if _, err := exec.LookPath("sudo"); err != nil {
		return errors.New("sudo is required to install the Debian package")
	}
	command := exec.Command(commandName, arguments...)
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("install update: %w", err)
	}
	return nil
}
