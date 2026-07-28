//go:build linux && !cgo && cli

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

const cliVersion = "0.2.1"

type cliPaths struct {
	homeDir    string
	configPath string
}

type controllerOptions struct {
	address string
	secret  string
}

func main() {
	var err error
	if len(os.Args) < 2 {
		err = tuiCommand(nil)
	} else {
		switch os.Args[1] {
		case "tui", "ui":
			err = tuiCommand(os.Args[2:])
		case "run", "start":
			err = runCommand(os.Args[2:])
		case "check", "validate":
			err = checkCommand(os.Args[2:])
		case "proxy":
			err = proxyCommand(os.Args[2:])
		case "version", "--version", "-v":
			fmt.Printf("FlClash CLI %s (Mihomo core)\n", cliVersion)
		case "help", "--help", "-h":
			printUsage(os.Stdout)
		default:
			err = fmt.Errorf("unknown command %q", os.Args[1])
		}
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "flclash-cli: %v\n", err)
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		os.Exit(1)
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "FlClash CLI - Linux command-line client powered by the FlClash Mihomo core")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  flclash-cli [tui] --config ./config.yaml")
	fmt.Fprintln(w, "  flclash-cli run --config ./config.yaml")
	fmt.Fprintln(w, "  flclash-cli check --config ./config.yaml")
	fmt.Fprintln(w, "  flclash-cli proxy list --controller 127.0.0.1:9090")
	fmt.Fprintln(w, "  flclash-cli proxy select --controller 127.0.0.1:9090 GROUP NODE")
	fmt.Fprintln(w, "  flclash-cli version")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	fmt.Fprintln(w, "  tui, ui           Open the full-screen terminal interface (default)")
	fmt.Fprintln(w, "  run, start       Start the proxy in the foreground")
	fmt.Fprintln(w, "  check, validate   Validate a Clash/Mihomo YAML configuration")
	fmt.Fprintln(w, "  proxy             Inspect or change a running core through its API")
	fmt.Fprintln(w, "  version           Print the CLI version")
}

func runCommand(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configArg := fs.String("config", "", "path to config.yaml")
	directoryArg := fs.String("directory", "", "FlClash data directory")
	testURL := fs.String("test-url", "https://www.gstatic.com/generate_204", "URL used by proxy-group delay tests")
	if err := fs.Parse(args); err != nil {
		return err
	}

	paths, err := resolvePaths(*configArg, *directoryArg)
	if err != nil {
		return err
	}
	if _, err := startCore(paths, *testURL, "", ""); err != nil {
		return err
	}
	setupParams, err := json.Marshal(SetupParams{TestURL: *testURL, SelectedMap: map[string]string{}})
	if err != nil {
		return err
	}

	fmt.Printf("FlClash CLI is running\n")
	fmt.Printf("  config: %s\n", paths.configPath)
	fmt.Printf("  data:   %s\n", paths.homeDir)
	fmt.Println("Press Ctrl-C to stop.")

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)
	reload := make(chan os.Signal, 1)
	signal.Notify(reload, syscall.SIGHUP)
	defer signal.Stop(interrupt)
	defer signal.Stop(reload)

	for {
		select {
		case <-interrupt:
			handleShutdown()
			return nil
		case <-reload:
			if message := handleSetupConfig(setupParams); message != "" {
				fmt.Fprintf(os.Stderr, "flclash-cli: reload failed: %s\n", message)
			} else {
				fmt.Println("configuration reloaded")
			}
		}
	}
}

func checkCommand(args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	configArg := fs.String("config", "", "path to config.yaml")
	directoryArg := fs.String("directory", "", "directory used to resolve config.yaml")
	if err := fs.Parse(args); err != nil {
		return err
	}
	paths, err := resolvePaths(*configArg, *directoryArg)
	if err != nil {
		return err
	}
	if _, err := os.Stat(paths.configPath); err != nil {
		return fmt.Errorf("config file %q: %w", paths.configPath, err)
	}
	if message := handleValidateConfig(paths.configPath); message != "" {
		return errors.New(message)
	}
	fmt.Printf("configuration is valid: %s\n", paths.configPath)
	return nil
}

func resolvePaths(configArg, directoryArg string) (cliPaths, error) {
	var homeDir string
	var configPath string

	if directoryArg != "" {
		homeDir = directoryArg
		if configArg == "" {
			configArg = "config.yaml"
		}
		if !filepath.IsAbs(configArg) {
			configArg = filepath.Join(homeDir, configArg)
		}
	} else if configArg != "" {
		configPath = configArg
		homeDir = filepath.Dir(configArg)
	} else {
		configRoot, err := os.UserConfigDir()
		if err != nil {
			return cliPaths{}, fmt.Errorf("resolve user config directory: %w", err)
		}
		homeDir = filepath.Join(configRoot, "flclash")
		configArg = "config.yaml"
	}

	absoluteHome, err := filepath.Abs(homeDir)
	if err != nil {
		return cliPaths{}, err
	}
	if configPath == "" {
		configPath = configArg
	}
	absoluteConfig, err := filepath.Abs(configPath)
	if err != nil {
		return cliPaths{}, err
	}
	return cliPaths{homeDir: absoluteHome, configPath: absoluteConfig}, nil
}

func proxyCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("proxy requires list or select")
	}
	fs := flag.NewFlagSet("proxy "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	address := fs.String("controller", "127.0.0.1:9090", "Mihomo external controller address")
	secret := fs.String("secret", "", "Mihomo external controller secret")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	client := controllerClient{options: controllerOptions{address: *address, secret: *secret}}

	switch args[0] {
	case "list":
		return client.listProxies()
	case "select":
		positional := fs.Args()
		if len(positional) != 2 {
			return errors.New("usage: proxy select [--controller address] GROUP NODE")
		}
		return client.selectProxy(positional[0], positional[1])
	default:
		return fmt.Errorf("unknown proxy command %q", args[0])
	}
}

type controllerClient struct {
	options controllerOptions
}

func (c controllerClient) requestStreamFirst(path string) ([]byte, error) {
	base := c.options.address
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(base, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	if c.options.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.options.secret)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("controller returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	line, err := bufio.NewReader(resp.Body).ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	return bytesTrimSpace(line), nil
}

func bytesTrimSpace(data []byte) []byte {
	return []byte(strings.TrimSpace(string(data)))
}

func (c controllerClient) request(method, path string, body io.Reader) ([]byte, error) {
	base := c.options.address
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "http://" + base
	}
	req, err := http.NewRequest(method, strings.TrimRight(base, "/")+path, body)
	if err != nil {
		return nil, err
	}
	if c.options.secret != "" {
		req.Header.Set("Authorization", "Bearer "+c.options.secret)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("controller returned %s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	return data, nil
}

func (c controllerClient) listProxies() error {
	data, err := c.request(http.MethodGet, "/proxies", nil)
	if err != nil {
		return err
	}
	var response struct {
		Proxies map[string]struct {
			Type string   `json:"type"`
			Now  string   `json:"now"`
			All  []string `json:"all"`
		} `json:"proxies"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return err
	}
	groups := make([]string, 0, len(response.Proxies))
	for name := range response.Proxies {
		groups = append(groups, name)
	}
	sort.Strings(groups)
	for _, name := range groups {
		proxy := response.Proxies[name]
		if len(proxy.All) == 0 {
			continue
		}
		fmt.Printf("%s (%s) -> %s\n", name, proxy.Type, proxy.Now)
		for _, item := range proxy.All {
			fmt.Printf("  - %s\n", item)
		}
	}
	return nil
}

func (c controllerClient) selectProxy(group, proxy string) error {
	body, err := json.Marshal(map[string]string{"name": proxy})
	if err != nil {
		return err
	}
	path := "/proxies/" + url.PathEscape(group)
	if _, err := c.request(http.MethodPut, path, strings.NewReader(string(body))); err != nil {
		return err
	}
	fmt.Printf("selected %q in %q\n", proxy, group)
	return nil
}

func (c controllerClient) closeAllConnections() error {
	_, err := c.request(http.MethodDelete, "/connections", nil)
	return err
}

func (c controllerClient) closeConnection(id string) error {
	_, err := c.request(http.MethodDelete, "/connections/"+url.PathEscape(id), nil)
	return err
}

func (c controllerClient) patchConfig(values map[string]interface{}) error {
	body, err := json.Marshal(values)
	if err != nil {
		return err
	}
	_, err = c.request(http.MethodPatch, "/configs", strings.NewReader(string(body)))
	return err
}

func (c controllerClient) updateProvider(name string) error {
	path := "/providers/proxies/" + url.PathEscape(name)
	_, err := c.request(http.MethodPut, path, nil)
	return err
}

func (c controllerClient) updateGeo() error {
	_, err := c.request(http.MethodPost, "/configs/geo", nil)
	return err
}
