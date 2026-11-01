//go:build linux && !cgo && cli

package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type portableInstallerTransport struct{ data string }

func (r portableInstallerTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(r.data)), Header: make(http.Header)}, nil
}

func TestPortableUpdateVerifiesInstallerBeforeExecution(t *testing.T) {
	root := t.TempDir()
	log := filepath.Join(root, "invocation")
	bash := filepath.Join(root, "bash")
	if err := os.WriteFile(bash, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$TEST_INSTALL_LOG\"\nprintf '%s\\n' \"$FLCLASH_INSTALL_ASSET_PATH\" \"$FLCLASH_INSTALL_REPOSITORY\" \"${FLCLASH_INSTALL_API_ROOT-unset}\" >> \"$TEST_INSTALL_LOG\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root)
	t.Setenv("TEST_INSTALL_LOG", log)
	t.Setenv("FLCLASH_INSTALL_API_ROOT", "https://untrusted.example")
	data := "verified fixture installer"
	hash := sha256.Sum256([]byte(data))
	previous := cliUpdateHTTPClient
	cliUpdateHTTPClient = &http.Client{Transport: portableInstallerTransport{data: data}}
	t.Cleanup(func() { cliUpdateHTTPClient = previous })
	release := cliRelease{Assets: []cliReleaseAsset{{Name: "install.sh", DownloadURL: "https://github.com/yqlay/flclash-tui/releases/download/v1.2.3/install.sh", Digest: "sha256:" + hex.EncodeToString(hash[:])}}}
	target := cliInstallation{Method: "portable", InstallDir: root + "/custom store", BinDir: root + "/custom bin"}
	archive := root + "/verified archive.tar.gz"
	if err := installCLIUpdatePackage(context.Background(), target, release, archive, "1.2.3"); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{target.InstallDir, target.BinDir, archive, "yqlay/flclash-tui", "unset"} {
		if !strings.Contains(string(output), value+"\n") {
			t.Fatalf("missing literal argument/environment %q: %q", value, output)
		}
	}
	if err := os.Remove(log); err != nil {
		t.Fatal(err)
	}
	release.Assets[0].Digest = "sha256:" + strings.Repeat("0", 64)
	if err := installCLIUpdatePackage(context.Background(), target, release, archive, "1.2.3"); err == nil {
		t.Fatal("tampered installer accepted")
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal("unverified installer executed")
	}
}
