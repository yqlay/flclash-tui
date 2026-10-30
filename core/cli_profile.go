//go:build linux && !cgo && cli

package main

import (
	"errors"
	"flag"
	"fmt"
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
		fmt.Println("Options may precede or follow names; -- ends option parsing.")
		fmt.Println("--json returns structured results except for interactive edit.")
		return nil
	}
	command := args[0]
	fs := newCLIFlagSet("profile " + command)
	configArg := fs.String("config", "", "profile YAML path")
	directoryArg := fs.String("directory", "", "FlClash data directory")
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := parseCLIFlags(fs, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return profileCommand([]string{"--help"})
		}
		return err
	}
	positional := fs.Args()
	switch command {
	case "list", "current":
		if len(positional) != 0 {
			return fmt.Errorf("usage: flclash profile %s [--json]", command)
		}
	case "import", "import-file", "use", "delete", "link":
		if len(positional) != 1 {
			return fmt.Errorf("profile %s requires exactly one argument", command)
		}
	case "rename":
		if len(positional) != 2 {
			return errors.New("usage: flclash profile rename NAME NEW_NAME")
		}
	case "edit", "update":
		if len(positional) > 1 {
			return fmt.Errorf("profile %s accepts at most one profile name", command)
		}
	default:
		return fmt.Errorf("unknown profile command %q; use `flclash profile -help`", command)
	}
	paths, err := resolvePaths(*configArg, *directoryArg)
	if err != nil {
		return err
	}
	if *configArg == "" {
		_, status, statusErr := currentManagedService()
		if statusErr == nil && status.HomeDir != "" &&
			(*directoryArg == "" || filepath.Clean(status.HomeDir) == filepath.Clean(paths.HomeDir)) {
			paths.HomeDir = status.HomeDir
			paths.ConfigPath = status.ConfigPath
		} else if restored, restoreErr := restoreTUIActiveProfile(paths); restoreErr == nil {
			paths = restored
		}
	}
	currentService := func() (*tuiServiceClient, tuiServiceStatus, error) {
		client, status, err := currentManagedService()
		if err == nil {
			// Profile commands may target a different file within this Backend's
			// home, but an explicit --directory must never import into another.
			err = validateTUIServiceTarget(paths, status, false, true)
		}
		return client, status, err
	}
	printResult := func(path, message string, revision uint64) error {
		if *jsonOutput {
			return writeCLIJSON(os.Stdout, map[string]any{
				"operation": command, "path": path, "revision": revision,
			})
		}
		fmt.Println(safeCLITerminalLine(message))
		return nil
	}
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
			fmt.Printf("%s %-32s %s", marker, safeCLITerminalLine(profile.Name), kind)
			if profile.SubscriptionInfo != nil {
				fmt.Printf(" · %s · %s", tuiSubscriptionCompact(profile.SubscriptionInfo), tuiSubscriptionExpiry(profile.SubscriptionInfo))
			}
			fmt.Println()
		}
		return nil
	case "current":
		if *jsonOutput {
			return writeCLIJSON(os.Stdout, map[string]string{"path": paths.ConfigPath})
		}
		fmt.Println(safeCLITerminalLine(paths.ConfigPath))
		return nil
	case "import":
		if len(positional) != 1 {
			return errors.New("usage: flclash profile import URL")
		}
		payload, err := fetchTUISubscriptionDetails(positional[0])
		if err != nil {
			return err
		}
		client, status, err := currentService()
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
			parseTUISubscriptionInfo(payload.UserInfo),
		)
		if err != nil {
			return err
		}
		return printResult(status.ResultPath, status.ResultPath, status.Revision)
	case "import-file":
		if len(positional) != 1 {
			return errors.New("usage: flclash profile import-file PATH")
		}
		data, name, err := readTUILocalProfile(positional[0])
		if err != nil {
			appendCLIApplicationLog(paths.HomeDir, "ERROR", "profile_import_file", "local profile validation failed")
			return err
		}
		client, status, err := currentService()
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
		return printResult(status.ResultPath, status.ResultPath, status.Revision)
	case "use":
		if len(positional) != 1 {
			return errors.New("usage: flclash profile use NAME")
		}
		target, err := resolveCLIProfile(paths.HomeDir, positional[0])
		if err != nil {
			return err
		}
		client, status, err := currentService()
		if err != nil {
			return err
		}
		status, err = activateManagedProfileForCommand(client, target, status.Revision)
		if err != nil {
			return err
		}
		return printResult(target, fmt.Sprintf("Active profile: %s (revision %d)", filepath.Base(target), status.Revision), status.Revision)
	case "update":
		target, err := cliProfileTarget(paths, positional)
		if err != nil {
			return err
		}
		sourceURL, err := loadTUISubscriptionSource(paths.HomeDir, target)
		if err != nil {
			return err
		}
		previous, err := os.ReadFile(target)
		if err != nil {
			return err
		}
		payload, err := fetchTUISubscriptionDetails(sourceURL)
		if err != nil {
			return err
		}
		updated := payload.Data
		if previousSettings := loadTUIConfiguredSettings(target, true); previousSettings != nil {
			updated, err = applyTUISettingsToConfig(updated, *previousSettings)
			if err != nil {
				return fmt.Errorf("preserve local settings: %w", err)
			}
		}
		client, status, err := currentService()
		if err != nil {
			return err
		}
		status, err = client.putProfile(
			target,
			updated,
			tuiBytesSHA256(previous),
			false,
			&sourceURL,
			status.Revision,
			parseTUISubscriptionInfo(payload.UserInfo),
		)
		if err != nil {
			return err
		}
		return printResult(target, "Updated "+filepath.Base(target), status.Revision)
	case "rename":
		if len(positional) != 2 {
			return errors.New("usage: flclash profile rename NAME NEW_NAME")
		}
		target, err := resolveCLIProfile(paths.HomeDir, positional[0])
		if err != nil {
			return err
		}
		if filepath.Clean(target) == filepath.Clean(paths.ConfigPath) {
			return errors.New("activate another profile before renaming the current profile")
		}
		client, status, err := currentService()
		if err != nil {
			return err
		}
		status, err = client.renameProfile(target, positional[1], status.Revision)
		if err != nil {
			return err
		}
		return printResult(status.ResultPath, status.ResultPath, status.Revision)
	case "edit":
		if *jsonOutput {
			return errors.New("profile edit does not support --json; it opens an interactive editor")
		}
		target, err := cliProfileTarget(paths, positional)
		if err != nil {
			return err
		}
		return editManagedConfig(target)
	case "delete":
		if len(positional) != 1 {
			return errors.New("usage: flclash profile delete NAME")
		}
		target, err := resolveCLIProfile(paths.HomeDir, positional[0])
		if err != nil {
			return err
		}
		if filepath.Clean(target) == filepath.Clean(paths.ConfigPath) {
			return errors.New("cannot delete the active profile")
		}
		client, status, err := currentService()
		if err != nil {
			return err
		}
		status, err = client.deleteProfile(target, status.Revision)
		if err != nil {
			return err
		}
		return printResult(target, "Deleted "+filepath.Base(target), status.Revision)
	case "link":
		if len(positional) != 1 {
			return errors.New("usage: flclash profile link [--config PATH] URL")
		}
		if _, err := os.Stat(paths.ConfigPath); err != nil {
			return err
		}
		sourceURL := positional[0]
		if _, err := newTUISubscriptionRequest(sourceURL); err != nil {
			return err
		}
		client, status, err := currentService()
		if err != nil {
			return err
		}
		status, err = client.linkProfile(
			paths.ConfigPath,
			sourceURL,
			status.Revision,
		)
		if err != nil {
			return err
		}
		return printResult(paths.ConfigPath, "Linked "+filepath.Base(paths.ConfigPath)+" to its subscription source", status.Revision)
	default:
		return fmt.Errorf("unknown profile command %q; use `flclash profile -help`", command)
	}
}

func listCLIProfiles(paths cliPaths) ([]tuiProfile, error) {
	entries, err := os.ReadDir(paths.HomeDir)
	if err != nil {
		return nil, err
	}
	sources := loadTUISubscriptionSources(paths.HomeDir)
	infos := loadTUISubscriptionInfo(paths.HomeDir)
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
		path := filepath.Join(paths.HomeDir, entry.Name())
		var info *tuiSubscriptionInfo
		if details, found := infos[entry.Name()]; found && sources[entry.Name()] != "" {
			info = &details
		}
		profiles = append(profiles, tuiProfile{
			Name:             entry.Name(),
			Path:             path,
			Current:          filepath.Clean(path) == filepath.Clean(paths.ConfigPath),
			SubscriptionURL:  sources[entry.Name()],
			SubscriptionInfo: info,
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
		return paths.ConfigPath, nil
	}
	if len(positional) != 1 {
		return "", errors.New("expected at most one profile name")
	}
	return resolveCLIProfile(paths.HomeDir, positional[0])
}

type controllerClient struct {
	options controllerOptions
	client  *http.Client
}
