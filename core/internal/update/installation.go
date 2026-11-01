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
	"syscall"
	"time"
)

type cliInstallation struct {
	Method     string
	Executable string
	InstallDir string
	BinDir     string
}

func checkCLIUpdateEntries(target cliInstallation, lookup func(string) (string, error)) error {
	for _, name := range []string{"flclash", "flc"} {
		selected, err := lookup(name)
		if err != nil {
			continue
		}
		resolved, err := filepath.EvalSymlinks(selected)
		if err != nil || resolved != target.Executable {
			return fmt.Errorf("%s selects %s instead of this installation %s; inspect 'type -a flclash flc' and resolve the conflicting entry before updating", name, selected, target.Executable)
		}
	}
	return nil
}

func queryCLIInstalledPackage(name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, name, args...).Output()
	return strings.TrimSpace(string(output)), err
}

func detectCLIInstallation(executable string, query func(string, ...string) (string, error)) (cliInstallation, error) {
	executable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return cliInstallation{}, err
	}
	target := cliInstallation{Executable: executable}
	marker := filepath.Join(filepath.Dir(executable), ".flclash-install")
	if info, err := os.Lstat(marker); err == nil {
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 || !ok || int(owner.Uid) != os.Geteuid() {
			return target, errors.New("unsafe portable installation marker")
		}
		data, err := os.ReadFile(marker)
		if err != nil {
			return target, err
		}
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		if len(lines) != 3 || lines[0] != "portable-v1" || !filepath.IsAbs(lines[1]) || !filepath.IsAbs(lines[2]) {
			return target, errors.New("invalid portable installation marker; rerun the portable installer")
		}
		target.Method, target.InstallDir, target.BinDir = "portable", filepath.Clean(lines[1]), filepath.Clean(lines[2])
		relative, err := filepath.Rel(target.InstallDir, executable)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return target, errors.New("portable executable is outside its installation directory")
		}
		for _, name := range []string{"flclash", "flc"} {
			path, err := filepath.EvalSymlinks(filepath.Join(target.BinDir, name))
			if err != nil || path != executable {
				return target, fmt.Errorf("portable %s entry no longer points to this executable; rerun the portable installer", name)
			}
		}
		return target, nil
	} else if !os.IsNotExist(err) {
		return target, err
	}
	checks := []struct {
		method, command string
		args            []string
	}{
		{"deb", "dpkg-query", []string{"-S", executable}},
		{"rpm", "rpm", []string{"-qf", "--qf", "%{NAME}", executable}},
		{"archlinux", "pacman", []string{"-Qoq", executable}},
		{"apk", "apk", []string{"info", "--who-owns", executable}},
		{"ipk", "opkg", []string{"search", executable}},
	}
	for _, check := range checks {
		output, err := query(check.command, check.args...)
		if err != nil {
			continue
		}
		owner := output
		switch check.method {
		case "deb":
			owner, _, _ = strings.Cut(output, ":")
		case "apk":
			_, owner, _ = strings.Cut(output, " is owned by ")
		case "ipk":
			owner, _, _ = strings.Cut(output, " - ")
		}
		if owner == "flclash-tui" || owner == "flclash-cli" || check.method == "apk" && strings.HasPrefix(owner, "flclash-tui-") {
			target.Method = check.method
			return target, nil
		}
	}
	return target, fmt.Errorf("%s is not owned by a supported package manager or portable installer; inspect 'type -a flclash flc', use the packaged /usr/bin/flclash, or rerun install.sh --method portable", executable)
}

func cliNativeInstallCommand(method, path string, available func(string) bool) ([]string, error) {
	switch method {
	case "deb":
		if available("apt-get") {
			return []string{"apt-get", "install", "--yes", path}, nil
		}
		return []string{"dpkg", "-i", path}, nil
	case "rpm":
		if available("zypper") {
			return []string{"zypper", "--non-interactive", "install", "--allow-unsigned-rpm", path}, nil
		}
		for _, name := range []string{"dnf", "dnf5", "yum"} {
			if available(name) {
				return []string{name, "--assumeyes", "install", path}, nil
			}
		}
		return nil, errors.New("RPM updates require dnf, dnf5, yum, or zypper")
	case "archlinux":
		return []string{"pacman", "--noconfirm", "-U", path}, nil
	case "apk":
		return []string{"apk", "add", "--allow-untrusted", path}, nil
	case "ipk":
		return []string{"opkg", "install", path}, nil
	default:
		return nil, fmt.Errorf("unsupported installation method %s", method)
	}
}

func installCLINativePackage(method, path string) error {
	args, err := cliNativeInstallCommand(method, path, func(name string) bool {
		_, err := exec.LookPath(name)
		return err == nil
	})
	if err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		args = append([]string{"sudo"}, args...)
	}
	command := exec.Command(args[0], args[1:]...)
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("install %s update: %w", method, err)
	}
	return nil
}
