//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
)

func proxyCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		printProxyCLIUsage("")
		return nil
	}
	command := args[0]
	wantArgs := 0
	switch command {
	case "list", "groups":
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
	if err := parseCLIFlags(fs, args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printProxyCLIUsage(command)
			return nil
		}
		return err
	}
	positional := fs.Args()
	if len(positional) != wantArgs {
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
		return "delay NODE [--test-url URL] [--json]"
	case "speed":
		return "speed NODE [--json]"
	default:
		return "groups|nodes|select|delay|speed [OPTIONS]"
	}
}

func printProxyCLIUsage(command string) {
	fmt.Println("Usage: flclash proxy " + proxyCLIUsage(command))
	if command == "" {
		for _, name := range []string{"groups", "nodes", "select", "delay", "speed"} {
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
