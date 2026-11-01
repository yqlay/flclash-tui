//go:build linux && !cgo && cli

package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestUpdatePackageFormats(t *testing.T) {
	for _, method := range []string{"deb", "rpm", "archlinux", "apk", "ipk", "portable"} {
		t.Run(method, func(t *testing.T) {
			name := "flclash-tui_1.2.3_arm64." + cliPackageExtension(method)
			release := cliRelease{TagName: "v1.2.3"}
			for _, candidate := range []string{"flclash-tui_1.2.3_amd64.rpm", name} {
				release.Assets = append(release.Assets, cliReleaseAsset{Name: candidate, DownloadURL: "https://github.com/yqlay/flclash-tui/releases/download/v1.2.3/" + candidate, Digest: "sha256:" + strings.Repeat("a", 64)})
			}
			asset, err := selectCLIUpdatePackage(release, "1.2.3", "arm64", method)
			if err != nil || asset.Name != name {
				t.Fatalf("asset=%+v err=%v", asset, err)
			}
			release.Assets = append(release.Assets, release.Assets[1])
			if _, err := selectCLIUpdatePackage(release, "1.2.3", "arm64", method); err == nil {
				t.Fatal("duplicate assets accepted")
			}
		})
	}
	if err := validateCLISelectedAsset(cliRelease{}, cliReleaseAsset{Name: "../flclash.deb"}); err == nil {
		t.Fatal("asset path traversal accepted")
	}
}

func TestUpdateUsesOwnerOfExecutingBinary(t *testing.T) {
	for _, method := range []string{"deb", "rpm", "archlinux", "apk", "ipk", "unmanaged"} {
		t.Run(method, func(t *testing.T) {
			executable := filepath.Join(t.TempDir(), "flclash")
			if err := os.WriteFile(executable, []byte("fixture"), 0o755); err != nil {
				t.Fatal(err)
			}
			commands := map[string]string{"deb": "dpkg-query", "rpm": "rpm", "archlinux": "pacman", "apk": "apk", "ipk": "opkg"}
			query := func(command string, args ...string) (string, error) {
				if args[len(args)-1] != executable {
					t.Fatal("ownership check did not target the executing binary")
				}
				if command != commands[method] {
					return "", errors.New("not owned")
				}
				switch method {
				case "deb":
					return "flclash-tui: " + executable, nil
				case "apk":
					return executable + " is owned by flclash-tui-1.2.3-r1", nil
				case "ipk":
					return "flclash-tui - " + executable, nil
				default:
					return "flclash-tui", nil
				}
			}
			target, err := detectCLIInstallation(executable, query)
			if method == "unmanaged" {
				if err == nil || !strings.Contains(err.Error(), "type -a") {
					t.Fatalf("unmanaged binary accepted: %v", err)
				}
			} else if err != nil || target.Method != method {
				t.Fatalf("target=%+v err=%v", target, err)
			}
		})
	}
}

func TestPortableUpdateRetainsCustomDirectories(t *testing.T) {
	root := t.TempDir()
	store, bin := filepath.Join(root, "custom store"), filepath.Join(root, "custom bin")
	instance := filepath.Join(store, "1.2.3-amd64.fixture")
	for _, dir := range []string{instance, bin} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(instance, "flclash")
	if err := os.WriteFile(executable, []byte("fixture"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"flclash", "flc"} {
		if err := os.Symlink(executable, filepath.Join(bin, name)); err != nil {
			t.Fatal(err)
		}
	}
	marker := filepath.Join(instance, ".flclash-install")
	if err := os.WriteFile(marker, []byte("portable-v1\n"+store+"\n"+bin+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target, err := detectCLIInstallation(filepath.Join(bin, "flclash"), func(string, ...string) (string, error) {
		t.Fatal("portable update queried a native package manager")
		return "", nil
	})
	if err != nil || target.Method != "portable" || target.InstallDir != store || target.BinDir != bin {
		t.Fatalf("target=%+v err=%v", target, err)
	}
	if err := os.Remove(filepath.Join(bin, "flc")); err != nil {
		t.Fatal(err)
	}
	if _, err := detectCLIInstallation(executable, nil); err == nil {
		t.Fatal("mismatched command links accepted")
	}
}

func TestNativeUpdateCommands(t *testing.T) {
	for _, tc := range []struct {
		method, manager string
		want            []string
	}{
		{"deb", "apt-get", []string{"apt-get", "install", "--yes", "/tmp/pkg with spaces.deb"}},
		{"rpm", "dnf", []string{"dnf", "--assumeyes", "install", "/tmp/pkg with spaces.deb"}},
		{"rpm", "dnf5", []string{"dnf5", "--assumeyes", "install", "/tmp/pkg with spaces.deb"}},
		{"rpm", "yum", []string{"yum", "--assumeyes", "install", "/tmp/pkg with spaces.deb"}},
		{"rpm", "zypper", []string{"zypper", "--non-interactive", "install", "--allow-unsigned-rpm", "/tmp/pkg with spaces.deb"}},
		{"archlinux", "pacman", []string{"pacman", "--noconfirm", "-U", "/tmp/pkg with spaces.deb"}},
		{"apk", "apk", []string{"apk", "add", "--allow-untrusted", "/tmp/pkg with spaces.deb"}},
		{"ipk", "opkg", []string{"opkg", "install", "/tmp/pkg with spaces.deb"}},
	} {
		got, err := cliNativeInstallCommand(tc.method, "/tmp/pkg with spaces.deb", func(name string) bool { return name == tc.manager })
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s %s: %v %v", tc.method, tc.manager, got, err)
		}
	}
	if _, err := cliNativeInstallCommand("rpm", "fixture", func(string) bool { return false }); err == nil {
		t.Fatal("missing RPM manager accepted")
	}
}

func TestUpdateRejectsShadowedCommandEntries(t *testing.T) {
	root := t.TempDir()
	current, old := filepath.Join(root, "system"), filepath.Join(root, "old")
	for _, path := range []string{current, old} {
		if err := os.WriteFile(path, []byte("fixture"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	target := cliInstallation{Method: "deb", Executable: current}
	if err := checkCLIUpdateEntries(target, func(name string) (string, error) {
		if name == "flc" {
			return old, nil
		}
		return current, nil
	}); err == nil || !strings.Contains(err.Error(), "flc selects") {
		t.Fatalf("shadowed wrapper accepted: %v", err)
	}
	if err := checkCLIUpdateEntries(target, func(string) (string, error) { return current, nil }); err != nil {
		t.Fatal(err)
	}
}

func packageTarFixture(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var result bytes.Buffer
	w := tar.NewWriter(&result)
	for name, data := range files {
		if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func gzipPackageFixture(t *testing.T, data []byte) []byte {
	t.Helper()
	var result bytes.Buffer
	w := gzip.NewWriter(&result)
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return result.Bytes()
}

func TestUpdateArchivePackageIdentity(t *testing.T) {
	for _, method := range []string{"apk", "archlinux", "ipk"} {
		for _, bad := range []string{"", "name", "version", "architecture"} {
			t.Run(method+"/"+bad, func(t *testing.T) {
				name, version, arch := "flclash-tui", "1.2.3-1", cliPackageArchitecture(method, "amd64")
				switch bad {
				case "name":
					name = "other"
				case "version":
					version = "1.2.4-1"
				case "architecture":
					arch = "aarch64"
				}
				metadata := fmt.Sprintf("pkgname = %s\npkgver = %s\narch = %s\n", name, version, arch)
				var data []byte
				if method == "ipk" {
					metadata = fmt.Sprintf("Package: %s\nVersion: %s\nArchitecture: %s\n", name, version, arch)
					control := gzipPackageFixture(t, packageTarFixture(t, map[string]string{"./control": metadata}))
					data = gzipPackageFixture(t, packageTarFixture(t, map[string]string{"./control.tar.gz": string(control)}))
				} else {
					data = packageTarFixture(t, map[string]string{".PKGINFO": metadata})
					if method == "apk" {
						data = gzipPackageFixture(t, data)
					} else {
						w, err := zstd.NewWriter(nil)
						if err != nil {
							t.Fatal(err)
						}
						data = w.EncodeAll(data, nil)
						w.Close()
					}
				}
				path := filepath.Join(t.TempDir(), "fixture."+cliPackageExtension(method))
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}
				err := validateCLIUpdatePackage(path, "1.2.3", "amd64", method)
				if (err != nil) != (bad != "") {
					t.Fatalf("validation=%v, bad=%s", err, bad)
				}
			})
		}
	}
}

func TestBuiltLinuxPackages(t *testing.T) {
	dir := os.Getenv("FLCLASH_TEST_PACKAGE_DIR")
	if dir == "" {
		t.Skip("built-package fixture not requested")
	}
	version, arch := os.Getenv("FLCLASH_TEST_PACKAGE_VERSION"), os.Getenv("FLCLASH_TEST_PACKAGE_ARCH")
	if version == "" {
		t.Fatal("FLCLASH_TEST_PACKAGE_VERSION is required")
	}
	if arch == "" {
		arch = runtime.GOARCH
	}
	for _, method := range []string{"deb", "rpm", "archlinux", "apk", "ipk", "portable"} {
		t.Run(method, func(t *testing.T) {
			path := filepath.Join(dir, fmt.Sprintf("flclash-tui_%s_%s.%s", version, arch, cliPackageExtension(method)))
			if err := validateCLIUpdatePackage(path, version, arch, method); err != nil {
				t.Fatal(err)
			}
			if err := validateCLIUpdatePackage(path, "999.0.0", arch, method); err == nil {
				t.Fatal("wrong release version accepted")
			}
		})
	}
}
