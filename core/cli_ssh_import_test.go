//go:build linux && !cgo && cli

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseOpenSSHConfigFileImportsConcreteHosts(t *testing.T) {
	directory := t.TempDir()
	included := filepath.Join(directory, "extra.conf")
	if err := os.WriteFile(included, []byte("Host jump\n  HostName bastion.example.edu\n  User jumpuser\n  Port 2222\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "config")
	content := strings.Join([]string{
		"Host *",
		"  Compression yes",
		"Host school school.example.edu",
		"  HostName ssh.example.edu",
		"  User student",
		"  Port 2222",
		"  IdentityFile ~/.ssh/id_ed25519",
		"  ProxyJump jump",
		"Host wild-*",
		"  User nobody",
		"Include extra.conf",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := parseOpenSSHConfigFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("parsed hosts = %#v", entries)
	}
	if entries[0].Name != "school" || entries[0].HostName != "ssh.example.edu" ||
		entries[0].User != "student" || entries[0].Port != 2222 ||
		entries[0].Jump != "jump" || !strings.HasSuffix(entries[0].Identity, ".ssh/id_ed25519") {
		t.Fatalf("school host = %+v", entries[0])
	}
	if entries[1].Name != "jump" || entries[1].HostName != "bastion.example.edu" ||
		entries[1].User != "jumpuser" || entries[1].Port != 2222 {
		t.Fatalf("included host = %+v", entries[1])
	}
}

func TestImportCLISSHConfigHostsSkipsExistingAndPatterns(t *testing.T) {
	configRoot := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", configRoot)
	directory := t.TempDir()
	path := filepath.Join(directory, "config")
	content := strings.Join([]string{
		"Host school",
		"  HostName ssh.example.edu",
		"  User student",
		"Host home",
		"  HostName home.example.edu",
		"  User user",
		"Host *",
		"  User ignore",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := addCLISSHProfile(cliSSHProfile{
		Name:     "school",
		Username: "student",
		Host:     "already.example.edu",
		Port:     22,
	}); err != nil {
		t.Fatal(err)
	}
	imported, skipped, err := importCLISSHConfigHosts(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(imported) != 1 || imported[0] != "home" {
		t.Fatalf("imported = %v", imported)
	}
	if len(skipped) != 1 || !strings.Contains(skipped[0], "school") {
		t.Fatalf("skipped = %v", skipped)
	}
	profile, err := loadCLISSHProfile("home")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Host != "home.example.edu" || profile.Username != "user" {
		t.Fatalf("imported profile = %+v", profile)
	}
}
