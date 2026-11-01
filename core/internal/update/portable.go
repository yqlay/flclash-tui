//go:build linux && !cgo && cli

package update

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func installCLIUpdatePackage(ctx context.Context, target cliInstallation, release cliRelease, path, version string) error {
	if target.Method != "portable" {
		return installCLINativePackage(target.Method, path)
	}
	var installer cliReleaseAsset
	found := false
	for _, asset := range release.Assets {
		if asset.Name == "install.sh" {
			if found {
				return errors.New("release contains duplicate installer assets")
			}
			installer, found = asset, true
		}
	}
	if !found {
		return errors.New("release has no verified portable installer; rerun install.sh --method portable")
	}
	if err := validateCLISelectedAsset(release, installer); err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "flclash-portable-update-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	script := filepath.Join(work, "install.sh")
	if err := downloadCLIUpdateAsset(ctx, cliUpdateHTTPClient, installer, script); err != nil {
		return err
	}
	if err := verifyCLIUpdateDigest(script, installer.Digest); err != nil {
		return fmt.Errorf("verify installer: %w", err)
	}
	repository, err := trustedCLIReleaseRepository(release)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "bash", script, "--version", version, "--method", "portable", "--install-dir", target.InstallDir, "--bin-dir", target.BinDir)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "FLCLASH_INSTALL_") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env,
		"FLCLASH_INSTALL_REPOSITORY="+repository,
		"FLCLASH_INSTALL_ASSET_PATH="+path,
	)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("install portable update: %w", err)
	}
	return nil
}
