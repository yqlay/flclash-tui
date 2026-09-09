//go:build linux && !cgo && cli

package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func profileCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash profile list|import|import-file|current|use|update|rename|edit|delete|link")
		fmt.Println("Import accepts Mihomo YAML, URI/Base64 lists, supported JSON, and common client proxy lines.")
		fmt.Println("Profile names resolve inside the active FlClash data directory.")
		return nil
	}
	command := args[0]
	fs := flag.NewFlagSet("profile "+command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configArg := fs.String("config", "", "profile YAML path")
	directoryArg := fs.String("directory", "", "FlClash data directory")
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	paths, err := resolvePaths(*configArg, *directoryArg)
	if err != nil {
		return err
	}
	if *configArg == "" && *directoryArg == "" {
		_, status, statusErr := currentManagedService()
		if statusErr == nil {
			paths.homeDir = status.HomeDir
			paths.configPath = status.ConfigPath
		} else if restored, restoreErr := restoreTUIActiveProfile(paths); restoreErr == nil {
			paths = restored
		}
	}
	positional := fs.Args()
	switch command {
	case "list":
		profiles, err := listCLIProfiles(paths)
		if err != nil {
			return err
		}
		if *jsonOutput {
			return writeCLIJSON(os.Stdout, profiles)
		}
		for _, profile := range profiles {
			marker := " "
			if profile.Current {
				marker = "*"
			}
			kind := "local"
			if profile.SubscriptionURL != "" {
				kind = "subscription"
			}
			fmt.Printf("%s %-32s %s\n", marker, profile.Name, kind)
		}
		return nil
	case "current":
		fmt.Println(paths.configPath)
		return nil
	case "import":
		if len(positional) != 1 {
			return errors.New("usage: flclash profile import URL")
		}
		payload, err := fetchTUISubscriptionDetails(positional[0])
		if err != nil {
			return err
		}
		client, status, err := currentManagedService()
		if err != nil {
			return err
		}
		path, err := tuiSubscriptionImportPath(status.HomeDir, payload)
		if err != nil {
			return err
		}
		status, err = client.putProfile(
			path,
			payload.Data,
			"",
			true,
			&positional[0],
			status.Revision,
		)
		if err != nil {
			return err
		}
		fmt.Println(status.ResultPath)
		return nil
	case "import-file":
		if len(positional) != 1 {
			return errors.New("usage: flclash profile import-file PATH")
		}
		data, name, err := readTUILocalProfile(positional[0])
		if err != nil {
			appendCLIApplicationLog(paths.homeDir, "ERROR", "profile_import_file", "local profile validation failed")
			return err
		}
		client, status, err := currentManagedService()
		if err != nil {
			return err
		}
		path, err := nextTUIImportedProfilePath(status.HomeDir, name)
		if err != nil {
			return err
		}
		status, err = client.putProfile(
			path,
			data,
			"",
			true,
			nil,
			status.Revision,
		)
		if err != nil {
			return err
		}
		fmt.Println(status.ResultPath)
		return nil
	case "use":
		if len(positional) != 1 {
			return errors.New("usage: flclash profile use NAME")
		}
		target, err := resolveCLIProfile(paths.homeDir, positional[0])
		if err != nil {
			return err
		}
		client, status, err := currentManagedService()
		if err != nil {
			return err
		}
		status, err = client.reloadAtRevision(target, status.Revision)
		if err != nil {
			return err
		}
		fmt.Printf("Active profile: %s (revision %d)\n", filepath.Base(target), status.Revision)
		return nil
	case "update":
		target, err := cliProfileTarget(paths, positional)
		if err != nil {
			return err
		}
		sourceURL, err := loadTUISubscriptionSource(paths.homeDir, target)
		if err != nil {
			return err
		}
		previous, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		updated, err := fetchTUISubscription(sourceURL)
		if err != nil {
			return err
		}
		if previousSettings := loadTUIConfiguredSettings(target, true); previousSettings != nil {
			updated, err = applyTUISettingsToConfig(updated, *previousSettings)
			if err != nil {
				return fmt.Errorf("preserve local settings: %w", err)
			}
		}
		client, status, err := currentManagedService()
		if err != nil {
			return err
		}
		if _, err := client.putProfile(
			target,
			updated,
			tuiBytesSHA256(previous),
			false,
			nil,
			status.Revision,
		); err != nil {
			return err
		}
		fmt.Printf("Updated %s\n", filepath.Base(target))
		return nil
	case "rename":
		if len(positional) != 2 {
			return errors.New("usage: flclash profile rename NAME NEW_NAME")
		}
		target, err := resolveCLIProfile(paths.homeDir, positional[0])
		if err != nil {
			return err
		}
		if filepath.Clean(target) == filepath.Clean(paths.configPath) {
			return errors.New("activate another profile before renaming the current profile")
		}
		client, status, err := currentManagedService()
		if err != nil {
			return err
		}
		status, err = client.renameProfile(target, positional[1], status.Revision)
		if err != nil {
			return err
		}
		fmt.Println(status.ResultPath)
		return nil
	case "edit":
		target, err := cliProfileTarget(paths, positional)
		if err != nil {
			return err
		}
		return editManagedConfig(target)
	case "delete":
		if len(positional) != 1 {
			return errors.New("usage: flclash profile delete NAME")
		}
		target, err := resolveCLIProfile(paths.homeDir, positional[0])
		if err != nil {
			return err
		}
		if filepath.Clean(target) == filepath.Clean(paths.configPath) {
			return errors.New("cannot delete the active profile")
		}
		client, status, err := currentManagedService()
		if err != nil {
			return err
		}
		if _, err := client.deleteProfile(target, status.Revision); err != nil {
			return err
		}
		fmt.Printf("Deleted %s\n", filepath.Base(target))
		return nil
	case "link":
		if len(positional) != 1 {
			return errors.New("usage: flclash profile link [--config PATH] URL")
		}
		if _, err := os.Stat(paths.configPath); err != nil {
			return err
		}
		sourceURL := positional[0]
		if _, err := newTUISubscriptionRequest(sourceURL); err != nil {
			return err
		}
		client, status, err := currentManagedService()
		if err != nil {
			return err
		}
		if _, err := client.linkProfile(
			paths.configPath,
			sourceURL,
			status.Revision,
		); err != nil {
			return err
		}
		fmt.Printf("Linked %s to its subscription source\n", filepath.Base(paths.configPath))
		return nil
	default:
		return fmt.Errorf("unknown profile command %q; use `flclash profile -help`", command)
	}
}

func listCLIProfiles(paths cliPaths) ([]tuiProfile, error) {
	entries, err := os.ReadDir(paths.homeDir)
	if err != nil {
		return nil, err
	}
	sources := loadTUISubscriptionSources(paths.homeDir)
	profiles := make([]tuiProfile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if isTUIRuntimeProfileName(entry.Name()) {
			continue
		}
		extension := strings.ToLower(filepath.Ext(entry.Name()))
		if extension != ".yaml" && extension != ".yml" {
			continue
		}
		path := filepath.Join(paths.homeDir, entry.Name())
		profiles = append(profiles, tuiProfile{
			Name:            entry.Name(),
			Path:            path,
			Current:         filepath.Clean(path) == filepath.Clean(paths.configPath),
			SubscriptionURL: sources[entry.Name()],
		})
	}
	sort.Slice(profiles, func(i, j int) bool { return profiles[i].Name < profiles[j].Name })
	return profiles, nil
}

func resolveCLIProfile(homeDir, value string) (string, error) {
	if value == "" {
		return "", errors.New("profile name must not be empty")
	}
	path := value
	if !filepath.IsAbs(path) {
		path = filepath.Join(homeDir, path)
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err := tuiProfileStateKey(homeDir, path); err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("profile must be a regular file")
	}
	return path, nil
}

func cliProfileTarget(paths cliPaths, positional []string) (string, error) {
	if len(positional) == 0 {
		return paths.configPath, nil
	}
	if len(positional) != 1 {
		return "", errors.New("expected at most one profile name")
	}
	return resolveCLIProfile(paths.homeDir, positional[0])
}

type controllerClient struct {
	options controllerOptions
	client  *http.Client
}
