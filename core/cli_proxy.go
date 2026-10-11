//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
)

func proxyCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		printProxyCLIUsage("")
		return nil
	}
	command := args[0]
	if command == "provider" {
		return proxyProviderCommand(args[1:])
	}
	wantArgs := 0
	switch command {
	case "list", "groups", "providers":
	case "nodes", "delay", "speed":
		wantArgs = 1
	case "select":
		wantArgs = 2
	default:
		return fmt.Errorf("unknown proxy command %q; use `flclash proxy -help`", command)
	}
	fs := newCLIFlagSet("proxy " + command)
	address := fs.String("controller", "", "Mihomo external controller address")
	secret := fs.String("secret", "", "Mihomo external controller secret")
	jsonOutput := fs.Bool("json", false, "print JSON only")
	testURL := fs.String("test-url", defaultCLITestURL, "delay test URL")
	groupName := fs.String("group", "", "test every node in this proxy group")
	if err := parseCLIFlags(fs, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printProxyCLIUsage(command)
			return nil
		}
		return err
	}
	positional := fs.Args()
	if *groupName != "" && command != "delay" && command != "speed" {
		return errors.New("--group is only valid for proxy delay and proxy speed")
	}
	if command == "delay" || command == "speed" {
		if *groupName != "" && len(positional) != 0 {
			return fmt.Errorf("usage: flclash proxy %s", proxyCLIUsage(command))
		}
		if *groupName == "" && len(positional) != 1 {
			return fmt.Errorf("usage: flclash proxy %s", proxyCLIUsage(command))
		}
	} else if len(positional) != wantArgs {
		return fmt.Errorf("usage: flclash proxy %s", proxyCLIUsage(command))
	}
	if *address == "" && *secret != "" {
		return errors.New("--secret requires an explicit --controller")
	}
	if command == "speed" && *address != "" {
		return errors.New("proxy speed requires the managed FlClash backend")
	}
	var client controllerClient
	var service *tuiServiceClient
	if *address != "" {
		client = controllerClient{options: controllerOptions{address: *address, secret: *secret}}
	} else {
		managedService, status, err := currentManagedService()
		if err != nil {
			return err
		}
		service = managedService
		client = managedController(status)
	}

	switch command {
	case "list", "groups":
		if *jsonOutput {
			data, err := client.request(http.MethodGet, "/proxies", nil)
			if err != nil {
				return err
			}
			if !json.Valid(data) {
				return errors.New("controller returned invalid JSON for /proxies")
			}
			_, err = os.Stdout.Write(append(data, '\n'))
			return err
		}
		return client.listProxies()
	case "nodes":
		return client.listProxyNodes(positional[0], *jsonOutput)
	case "providers":
		return client.listProxyProviders(*jsonOutput)
	case "select":
		if service == nil {
			if *jsonOutput {
				if err := client.setProxy(positional[0], positional[1]); err != nil {
					return err
				}
				return writeCLIJSON(os.Stdout, map[string]any{"group": positional[0], "node": positional[1]})
			}
			return client.selectProxy(positional[0], positional[1])
		}
		status, err := service.status()
		if err != nil {
			return err
		}
		status, err = service.selectProxy(
			positional[0],
			positional[1],
			status.Revision,
		)
		if err != nil {
			return err
		}
		if *jsonOutput {
			return writeCLIJSON(os.Stdout, map[string]any{
				"group": positional[0], "node": positional[1],
				"revision": status.Revision, "flc_outbound": status.FLCOutbound,
			})
		}
		if strings.EqualFold(status.Mode, tuiSilentMode) {
			fmt.Printf(
				"selected %q in %q · flc follows %q (revision %d)\n",
				positional[1],
				positional[0],
				status.FLCOutbound,
				status.Revision,
			)
			return nil
		}
		fmt.Printf(
			"selected %q in %q (revision %d)\n",
			positional[1],
			positional[0],
			status.Revision,
		)
		return nil
	case "delay":
		if *groupName != "" {
			return client.testProxyGroupDelay(*groupName, *testURL, *jsonOutput)
		}
		delay, err := testTUIProxyDelaySamples(client, positional[0], *testURL)
		if err != nil {
			return err
		}
		if *jsonOutput {
			return writeCLIJSON(os.Stdout, struct {
				Node string `json:"node"`
				tuiDelayResult
			}{positional[0], delay})
		}
		fmt.Printf("%s: %s\n", safeCLITerminalLine(positional[0]), formatTUIDelay(delay))
		return nil
	case "speed":
		if service == nil {
			return errors.New("proxy speed requires the managed FlClash backend")
		}
		if *groupName != "" {
			return service.testProxyGroupSpeed(client, *groupName, *jsonOutput)
		}
		result, err := service.testProxySpeed(positional[0])
		if err != nil {
			return err
		}
		if *jsonOutput {
			return writeCLIJSON(os.Stdout, struct {
				Node string `json:"node"`
				tuiSpeedResult
			}{positional[0], result})
		}
		fmt.Printf("%s: %s\n", safeCLITerminalLine(positional[0]), formatTUISpeed(result))
		return nil
	default:
		return fmt.Errorf("unknown proxy command %q; use `flclash proxy -help`", args[0])
	}
}

func proxyCLIUsage(command string) string {
	switch command {
	case "list", "groups":
		return command + " [--json]"
	case "nodes":
		return "nodes GROUP [--json]"
	case "select":
		return "select GROUP NODE [--json]"
	case "delay":
		return "delay NODE [--test-url URL] [--json] | delay --group GROUP [--test-url URL] [--json]"
	case "speed":
		return "speed NODE [--json] | speed --group GROUP [--json]"
	case "providers":
		return "providers [--json]"
	case "provider":
		return "provider update NAME [--json]"
	default:
		return "groups|nodes|select|delay|speed|providers|provider [OPTIONS]"
	}
}

func printProxyCLIUsage(command string) {
	fmt.Println("Usage: flclash proxy " + proxyCLIUsage(command))
	if command == "" {
		for _, name := range []string{"groups", "nodes", "select", "delay", "speed", "providers", "provider"} {
			fmt.Println("  " + proxyCLIUsage(name))
		}
		fmt.Println("  list is an alias for groups")
	}
	fmt.Println("Options may precede or follow arguments; -- ends option parsing.")
	fmt.Println("--controller URL --secret SECRET: use only this external controller (speed requires the managed Backend).")
}

func (c controllerClient) listProxyNodes(group string, jsonOutput bool) error {
	data, err := c.request(http.MethodGet, "/proxies", nil)
	if err != nil {
		return err
	}
	var response tuiProxyResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return err
	}
	proxy, ok := response.Proxies[group]
	if !ok || len(proxy.All) == 0 {
		return fmt.Errorf("proxy group %q was not found or has no nodes", group)
	}
	if jsonOutput {
		return writeCLIJSON(os.Stdout, map[string]any{
			"group": group,
			"now":   proxy.Now,
			"nodes": proxy.All,
		})
	}
	for _, node := range proxy.All {
		marker := " "
		if node == proxy.Now {
			marker = "*"
		}
		fmt.Printf("%s %s\n", marker, safeCLITerminalLine(node))
	}
	return nil
}

func proxyProviderCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash proxy " + proxyCLIUsage("provider"))
		return nil
	}
	if args[0] != "update" {
		return fmt.Errorf("unknown proxy provider command %q; use `flclash proxy provider -help`", args[0])
	}
	fs := newCLIFlagSet("proxy provider update")
	address := fs.String("controller", "", "Mihomo external controller address")
	secret := fs.String("secret", "", "Mihomo external controller secret")
	jsonOutput := fs.Bool("json", false, "print JSON only")
	if err := parseCLIFlags(fs, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println("Usage: flclash proxy " + proxyCLIUsage("provider"))
			return nil
		}
		return err
	}
	if len(fs.Args()) != 1 {
		return errors.New("usage: flclash proxy provider update NAME")
	}
	if *address == "" && *secret != "" {
		return errors.New("--secret requires an explicit --controller")
	}
	name := fs.Args()[0]
	var client controllerClient
	if *address != "" {
		client = controllerClient{options: controllerOptions{address: *address, secret: *secret}}
	} else {
		_, status, err := currentManagedService()
		if err != nil {
			return err
		}
		client = managedController(status)
	}
	if err := client.updateProvider(name); err != nil {
		return err
	}
	if *jsonOutput {
		return writeCLIJSON(os.Stdout, map[string]any{"name": name, "updated": true})
	}
	fmt.Printf("updated provider %s\n", safeCLITerminalLine(name))
	return nil
}

func (c controllerClient) proxyGroupNodes(group string) ([]string, error) {
	data, err := c.request(http.MethodGet, "/proxies", nil)
	if err != nil {
		return nil, err
	}
	var response tuiProxyResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	proxy, ok := response.Proxies[group]
	if !ok || len(proxy.All) == 0 {
		return nil, fmt.Errorf("proxy group %q was not found or has no nodes", group)
	}
	return append([]string(nil), proxy.All...), nil
}

type cliGroupDelayJSON struct {
	Node         string `json:"node"`
	Error        string `json:"error,omitempty"`
	MedianMillis int    `json:"median_millis,omitempty"`
	JitterMillis int    `json:"jitter_millis,omitempty"`
	MinMillis    int    `json:"min_millis,omitempty"`
	MaxMillis    int    `json:"max_millis,omitempty"`
	Samples      int    `json:"samples,omitempty"`
}

func (c controllerClient) testProxyGroupDelay(group, testURL string, jsonOutput bool) error {
	nodes, err := c.proxyGroupNodes(group)
	if err != nil {
		return err
	}
	rows := make([]cliGroupDelayJSON, 0, len(nodes))
	for _, node := range nodes {
		delay, err := testTUIProxyDelaySamples(c, node, testURL)
		if err != nil {
			if !jsonOutput {
				fmt.Printf("%s: %s\n", safeCLITerminalLine(node), err.Error())
			}
			rows = append(rows, cliGroupDelayJSON{Node: node, Error: err.Error()})
			continue
		}
		if !jsonOutput {
			fmt.Printf("%s: %s\n", safeCLITerminalLine(node), formatTUIDelay(delay))
		}
		rows = append(rows, cliGroupDelayJSON{
			Node: node, MedianMillis: delay.MedianMillis, JitterMillis: delay.JitterMillis,
			MinMillis: delay.MinMillis, MaxMillis: delay.MaxMillis, Samples: delay.Samples,
		})
	}
	if jsonOutput {
		return writeCLIJSON(os.Stdout, rows)
	}
	return nil
}

type cliGroupSpeedJSON struct {
	Node           string  `json:"node"`
	Error          string  `json:"error,omitempty"`
	Bytes          int64   `json:"bytes,omitempty"`
	DurationMillis int64   `json:"duration_millis,omitempty"`
	BytesPerSecond float64 `json:"bytes_per_second,omitempty"`
	Complete       bool    `json:"complete,omitempty"`
}

func (c *tuiServiceClient) testProxyGroupSpeed(controller controllerClient, group string, jsonOutput bool) error {
	nodes, err := controller.proxyGroupNodes(group)
	if err != nil {
		return err
	}
	rows := make([]cliGroupSpeedJSON, 0, len(nodes))
	for _, node := range nodes {
		result, err := c.testProxySpeed(node)
		if err != nil {
			if !jsonOutput {
				fmt.Printf("%s: %s\n", safeCLITerminalLine(node), err.Error())
			}
			rows = append(rows, cliGroupSpeedJSON{Node: node, Error: err.Error()})
			continue
		}
		if !jsonOutput {
			fmt.Printf("%s: %s\n", safeCLITerminalLine(node), formatTUISpeed(result))
		}
		rows = append(rows, cliGroupSpeedJSON{
			Node: node, Bytes: result.Bytes, DurationMillis: result.DurationMillis,
			BytesPerSecond: result.BytesPerSecond, Complete: result.Complete,
		})
	}
	if jsonOutput {
		return writeCLIJSON(os.Stdout, rows)
	}
	return nil
}

func (c controllerClient) listProxyProviders(jsonOutput bool) error {
	data, err := c.request(http.MethodGet, "/providers/proxies", nil)
	if err != nil {
		return err
	}
	var response tuiProviderResponse
	if err := json.Unmarshal(data, &response); err != nil {
		return err
	}
	type providerRow struct {
		Name      string `json:"name"`
		Type      string `json:"type,omitempty"`
		Vehicle   string `json:"vehicle,omitempty"`
		Count     int    `json:"count"`
		UpdatedAt string `json:"updated_at,omitempty"`
	}
	rows := make([]providerRow, 0, len(response.Providers))
	for name, item := range response.Providers {
		if item.Name == "" {
			item.Name = name
		}
		rows = append(rows, providerRow{
			Name: item.Name, Type: item.Type, Vehicle: item.Vehicle,
			Count: len(item.Proxies), UpdatedAt: item.UpdatedAt,
		})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	if jsonOutput {
		return writeCLIJSON(os.Stdout, rows)
	}
	for _, row := range rows {
		fmt.Printf("%s · %s · %s · %d\n", safeCLITerminalLine(row.Name), cliDisplayValue(row.Type), cliDisplayValue(row.Vehicle), row.Count)
	}
	return nil
}
