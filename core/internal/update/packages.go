//go:build linux && !cgo && cli

package update

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/klauspost/compress/zstd"
)

func cliPackageExtension(method string) string {
	return map[string]string{"deb": "deb", "rpm": "rpm", "archlinux": "pkg.tar.zst", "apk": "apk", "ipk": "ipk", "portable": "tar.gz"}[method]
}

func cliPackageArchitecture(method, goArch string) string {
	if method == "deb" {
		return cliDebianArchitecture(goArch)
	}
	if method == "ipk" {
		return map[string]string{"amd64": "x86_64", "arm64": "aarch64_generic"}[goArch]
	}
	return map[string]string{"amd64": "x86_64", "arm64": "aarch64"}[goArch]
}

func selectCLIUpdatePackage(release cliRelease, version, goArch, method string) (cliReleaseAsset, error) {
	if cliPackageArchitecture(method, goArch) == "" || cliPackageExtension(method) == "" {
		return cliReleaseAsset{}, fmt.Errorf("automatic %s update is not supported on architecture %s", method, goArch)
	}
	name := fmt.Sprintf("flclash-tui_%s_%s.%s", version, goArch, cliPackageExtension(method))
	var selected cliReleaseAsset
	best := -1
	matches := 0
	for _, asset := range release.Assets {
		score := -1
		if method == "deb" {
			score = scoreCLIUpdateDebAsset(asset.Name, version, goArch)
		} else if asset.Name == name {
			score = 100
		}
		if score < 0 {
			continue
		}
		if score == best {
			matches++
		}
		if score > best {
			best, selected = score, asset
			matches = 1
		}
	}
	if best < 0 {
		return cliReleaseAsset{}, fmt.Errorf("release %s does not contain a recognizable FlClash %s package for %s", release.TagName, method, goArch)
	}
	if matches > 1 {
		return cliReleaseAsset{}, fmt.Errorf("release %s contains multiple equally suitable %s packages", release.TagName, method)
	}
	return selected, validateCLISelectedAsset(release, selected)
}

func validateCLISelectedAsset(release cliRelease, asset cliReleaseAsset) error {
	if asset.Name == "" || strings.ContainsAny(asset.Name, "/\\\x00\r\n") || asset.Name == "." || asset.Name == ".." {
		return errors.New("invalid update asset filename")
	}
	if _, err := parseCLIUpdateDigest(asset.Digest); err != nil {
		return fmt.Errorf("invalid GitHub digest for %s: %w", asset.Name, err)
	}
	repository, err := trustedCLIReleaseRepository(release)
	if err != nil {
		return err
	}
	return validateCLIUpdateAssetURLForRepository(asset.DownloadURL, repository)
}

func readCLIPackageTarField(reader io.Reader, filename string) ([]byte, error) {
	archive := tar.NewReader(io.LimitReader(reader, 512<<20))
	for {
		header, err := archive.Next()
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", filename, err)
		}
		if strings.TrimPrefix(header.Name, "./") == filename {
			if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > 1<<20 {
				return nil, errors.New("invalid package metadata member")
			}
			return io.ReadAll(io.LimitReader(archive, (1<<20)+1))
		}
	}
}

func readCLIIPKControl(reader io.Reader) ([]byte, error) {
	outer, err := gzip.NewReader(reader)
	if err != nil {
		return nil, err
	}
	defer outer.Close()
	control, err := readCLIPackageTarField(outer, "control.tar.gz")
	if err != nil {
		return nil, err
	}
	inner, err := gzip.NewReader(bytes.NewReader(control))
	if err != nil {
		return nil, err
	}
	defer inner.Close()
	return readCLIPackageTarField(inner, "control")
}

func validateCLIUpdatePackage(path, version, goArch, method string) error {
	if method == "deb" {
		return validateCLIUpdateDebianPackage(path, version, goArch)
	}
	if method == "rpm" {
		output, err := queryCLIInstalledPackage("rpm", "-qp", "--qf", "%{NAME}\n%{VERSION}\n%{ARCH}\n", path)
		if err != nil {
			return fmt.Errorf("read RPM package metadata: %w", err)
		}
		values := strings.Split(output, "\n")
		if len(values) != 3 {
			return errors.New("invalid RPM package metadata")
		}
		return validateCLIPackageIdentity(values[0], values[1], values[2], version, cliPackageArchitecture(method, goArch))
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	var metadata []byte
	switch method {
	case "archlinux":
		reader, readErr := zstd.NewReader(file, zstd.WithDecoderMaxMemory(64<<20))
		if readErr != nil {
			return readErr
		}
		defer reader.Close()
		metadata, err = readCLIPackageTarField(reader, ".PKGINFO")
	case "apk":
		reader, readErr := gzip.NewReader(file)
		if readErr != nil {
			return readErr
		}
		defer reader.Close()
		metadata, err = readCLIPackageTarField(reader, ".PKGINFO")
	case "ipk":
		metadata, err = readCLIIPKControl(file)
	case "portable":
		return validateCLIPortableArchive(file, version, goArch)
	default:
		return fmt.Errorf("unsupported update package %s", method)
	}
	if err != nil {
		return err
	}
	fields := map[string]string{}
	separator := " = "
	if method == "ipk" {
		separator = ":"
	}
	for _, line := range bytes.Split(metadata, []byte{'\n'}) {
		key, value, found := strings.Cut(string(line), separator)
		if found {
			fields[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
		}
	}
	name, packageVersion, architecture := fields["pkgname"], fields["pkgver"], fields["arch"]
	if method == "ipk" {
		name, packageVersion, architecture = fields["package"], fields["version"], fields["architecture"]
	}
	return validateCLIPackageIdentity(name, packageVersion, architecture, version, cliPackageArchitecture(method, goArch))
}

func validateCLIPackageIdentity(name, actualVersion, arch, version, expectedArch string) error {
	if name != "flclash-tui" && name != "flclash-cli" {
		return fmt.Errorf("unexpected package name %q", name)
	}
	if expectedArch == "" || arch != expectedArch {
		return fmt.Errorf("package architecture is %s, expected %s", arch, expectedArch)
	}
	if actualVersion != version && !strings.HasPrefix(actualVersion, version+"-") && !strings.HasPrefix(actualVersion, version+"~") && !strings.HasPrefix(actualVersion, version+"+") {
		return fmt.Errorf("package version is %s, expected %s", actualVersion, version)
	}
	return nil
}

func validateCLIPortableArchive(file io.Reader, version, arch string) error {
	reader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer reader.Close()
	archive := tar.NewReader(io.LimitReader(reader, 512<<20))
	prefix := "flclash-tui_" + version + "_" + arch + "/"
	found := map[string]bool{}
	for {
		h, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if !strings.HasPrefix(h.Name, prefix) || strings.Contains(h.Name, "/../") || strings.HasSuffix(h.Name, "/..") {
			return errors.New("portable archive contains an unexpected path")
		}
		relative := strings.TrimPrefix(h.Name, prefix)
		if relative == "flclash" && h.Typeflag == tar.TypeReg && h.Mode&0o111 != 0 && h.Size > 0 {
			found[relative] = true
		} else if relative == "flc" && h.Typeflag == tar.TypeSymlink && h.Linkname == "flclash" {
			found[relative] = true
		} else if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
			return errors.New("portable archive contains an unexpected file type")
		}
		if strings.HasPrefix(relative, "data/") && h.Typeflag == tar.TypeReg {
			found["data"] = true
		}
	}
	if !found["flclash"] || !found["flc"] || !found["data"] {
		return errors.New("portable archive is incomplete")
	}
	return nil
}
