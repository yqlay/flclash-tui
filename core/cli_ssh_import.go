//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

type openSSHHostConfig struct {
	Name     string
	HostName string
	User     string
	Port     int
	Identity string
	Jump     string
}

func cliSSHImportCommand(args []string) error {
	if cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash ssh import [HOST...] [--file PATH]")
		fmt.Println("Import Host entries from an OpenSSH config into FlClash SSH profiles.")
		return nil
	}
	filePath := ""
	hosts := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch {
		case argument == "--file":
			if index+1 >= len(args) || strings.TrimSpace(args[index+1]) == "" {
				return errors.New("usage: flclash ssh import [HOST...] [--file PATH]")
			}
			index++
			filePath = args[index]
		case strings.HasPrefix(argument, "--file="):
			filePath = strings.TrimPrefix(argument, "--file=")
		case strings.HasPrefix(argument, "-"):
			return fmt.Errorf("unknown SSH import option %q", argument)
		default:
			hosts = append(hosts, argument)
		}
	}
	imported, skipped, err := importCLISSHConfigHosts(filePath, hosts)
	if err != nil {
		return err
	}
	if len(imported) == 0 && len(skipped) == 0 {
		fmt.Println("No importable OpenSSH Host entries found")
		return nil
	}
	for _, name := range imported {
		fmt.Printf("SSH profile %s imported\n", name)
	}
	for _, message := range skipped {
		fmt.Printf("skipped: %s\n", message)
	}
	if len(imported) == 0 {
		return errors.New("no SSH profiles were imported")
	}
	return nil
}

func importCLISSHConfigHosts(filePath string, wanted []string) ([]string, []string, error) {
	if strings.TrimSpace(filePath) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, nil, err
		}
		filePath = filepath.Join(home, ".ssh", "config")
	}
	entries, err := parseOpenSSHConfigFile(expandCLISSHIdentityPath(filePath), 0)
	if err != nil {
		return nil, nil, err
	}
	wantedSet := map[string]bool{}
	for _, name := range wanted {
		wantedSet[strings.ToLower(strings.TrimSpace(name))] = true
	}
	imported := make([]string, 0, len(entries))
	skipped := make([]string, 0)
	for _, entry := range entries {
		if len(wantedSet) > 0 && !wantedSet[strings.ToLower(entry.Name)] {
			continue
		}
		profile, skipReason := cliSSHProfileFromOpenSSHHost(entry)
		if skipReason != "" {
			skipped = append(skipped, entry.Name+": "+skipReason)
			continue
		}
		if err := addCLISSHProfile(profile); err != nil {
			skipped = append(skipped, profile.Name+": "+err.Error())
			continue
		}
		imported = append(imported, profile.Name)
	}
	return imported, skipped, nil
}

func cliSSHProfileFromOpenSSHHost(entry openSSHHostConfig) (cliSSHProfile, string) {
	name := sanitizeCLISSHImportedName(entry.Name)
	if name == "" {
		return cliSSHProfile{}, "Host alias is not a valid FlClash profile name"
	}
	host := strings.TrimSpace(entry.HostName)
	if host == "" {
		host = strings.TrimSpace(entry.Name)
	}
	port := entry.Port
	if port == 0 {
		port = 22
	}
	profile := normalizeCLISSHProfile(cliSSHProfile{
		Name:     name,
		Username: strings.TrimSpace(entry.User),
		Host:     host,
		Port:     port,
		Jump:     strings.TrimSpace(entry.Jump),
		Identity: strings.TrimSpace(entry.Identity),
	})
	if err := validateCLISSHProfile(profile); err != nil {
		return cliSSHProfile{}, err.Error()
	}
	return profile, ""
}

func sanitizeCLISSHImportedName(name string) string {
	var builder strings.Builder
	for _, value := range strings.TrimSpace(name) {
		switch {
		case unicode.IsLetter(value) || unicode.IsDigit(value) || value == '.' || value == '_' || value == '-':
			builder.WriteRune(value)
		default:
			if builder.Len() > 0 && !strings.HasSuffix(builder.String(), "-") {
				builder.WriteByte('-')
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}

func parseOpenSSHConfigFile(path string, depth int) ([]openSSHHostConfig, error) {
	if depth > 8 {
		return nil, fmt.Errorf("OpenSSH config include depth exceeded at %q", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("read OpenSSH config %q: %w", path, err)
	}
	defer file.Close()
	var (
		entries []openSSHHostConfig
		current *openSSHHostConfig
	)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		keyword, value, ok := splitOpenSSHConfigLine(scanner.Text())
		if !ok {
			continue
		}
		switch strings.ToLower(keyword) {
		case "host":
			current = nil
			for _, alias := range strings.Fields(value) {
				if openSSHHostPattern(alias) {
					continue
				}
				entries = append(entries, openSSHHostConfig{Name: alias})
				current = &entries[len(entries)-1]
				break
			}
		case "match":
			current = nil
		case "include":
			included, includeErr := parseOpenSSHConfigIncludes(path, value, depth)
			if includeErr != nil {
				return nil, includeErr
			}
			entries = append(entries, included...)
			current = nil
		case "hostname":
			if current != nil {
				current.HostName = value
			}
		case "user":
			if current != nil {
				current.User = value
			}
		case "port":
			if current == nil {
				continue
			}
			port, convErr := strconv.Atoi(value)
			if convErr != nil || port < 1 || port > 65535 {
				return nil, fmt.Errorf("invalid Port %q for Host %q", value, current.Name)
			}
			current.Port = port
		case "identityfile":
			if current != nil && current.Identity == "" {
				current.Identity = expandCLISSHIdentityPath(value)
			}
		case "proxyjump", "jumphost":
			if current != nil && current.Jump == "" {
				current.Jump = value
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read OpenSSH config %q: %w", path, err)
	}
	return entries, nil
}

func parseOpenSSHConfigIncludes(parent, spec string, depth int) ([]openSSHHostConfig, error) {
	var entries []openSSHHostConfig
	for _, pattern := range strings.Fields(spec) {
		pattern = expandCLISSHIdentityPath(unquoteOpenSSHConfigValue(pattern))
		if !filepath.IsAbs(pattern) {
			pattern = filepath.Join(filepath.Dir(parent), pattern)
		}
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("expand OpenSSH Include %q: %w", pattern, err)
		}
		if len(matches) == 0 {
			if info, statErr := os.Stat(pattern); statErr == nil && info.Mode().IsRegular() {
				matches = []string{pattern}
			}
		}
		for _, match := range matches {
			included, includeErr := parseOpenSSHConfigFile(match, depth+1)
			if includeErr != nil {
				if errors.Is(includeErr, os.ErrNotExist) {
					continue
				}
				return nil, includeErr
			}
			entries = append(entries, included...)
		}
	}
	return entries, nil
}

func splitOpenSSHConfigLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	if strings.HasPrefix(line, "=") {
		return "", "", false
	}
	keyword, value, found := strings.Cut(line, " ")
	if !found {
		keyword, value, found = strings.Cut(line, "=")
	} else if strings.Contains(keyword, "=") {
		keyword, value, found = strings.Cut(line, "=")
	} else if strings.HasPrefix(strings.TrimSpace(value), "=") {
		value = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(value), "="))
		found = true
	}
	if !found {
		return "", "", false
	}
	keyword = strings.TrimSpace(keyword)
	value = unquoteOpenSSHConfigValue(strings.TrimSpace(value))
	if keyword == "" || value == "" {
		return "", "", false
	}
	return keyword, value, true
}

func unquoteOpenSSHConfigValue(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') ||
			(value[0] == '\'' && value[len(value)-1] == '\'') {
			return value[1 : len(value)-1]
		}
	}
	if comment := strings.Index(value, " #"); comment >= 0 {
		value = strings.TrimSpace(value[:comment])
	}
	return value
}

func openSSHHostPattern(alias string) bool {
	return strings.ContainsAny(alias, "*?!")
}
