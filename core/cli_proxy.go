//go:build linux && !cgo && cli

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

func proxyCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash proxy groups|nodes|select|delay|speed [OPTIONS]")
		fmt.Println("  groups [--json]                  List proxy groups")
		fmt.Println("  nodes GROUP [--json]             List nodes in a group")
		fmt.Println("  select GROUP NODE                Select a node")
		fmt.Println("  delay NODE [--test-url URL]      Test node delay")
		fmt.Println("  speed NODE                       Test node download speed")
		return nil
	}
	fs := flag.NewFlagSet("proxy "+args[0], flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	address := fs.String("controller", "", "Mihomo external controller address")
	secret := fs.String("secret", "", "Mihomo external controller secret")
	jsonOutput := fs.Bool("json", false, "print raw JSON")
	testURL := fs.String("test-url", defaultCLITestURL, "delay test URL")
	if err := fs.Parse(args[1:]); err != nil {
		return err
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

	switch args[0] {
	case "list", "groups":
		if *jsonOutput {
			data, err := client.request(http.MethodGet, "/proxies", nil)
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(append(data, '\n'))
			return err
		}
		return client.listProxies()
	case "nodes":
		positional := fs.Args()
		if len(positional) != 1 {
			return errors.New("usage: flclash proxy nodes GROUP")
		}
		return client.listProxyNodes(positional[0], *jsonOutput)
	case "select":
		positional := fs.Args()
		if len(positional) != 2 {
			return errors.New("usage: flclash proxy select GROUP NODE")
		}
		if service == nil {
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
		positional := fs.Args()
		if len(positional) != 1 {
			return errors.New("usage: flclash proxy delay NODE")
		}
		delay, err := testTUIProxyDelaySamples(client, positional[0], *testURL)
		if err != nil {
			return err
		}
		fmt.Printf("%s: %s\n", positional[0], formatTUIDelay(delay))
		return nil
	case "speed":
		positional := fs.Args()
		if len(positional) != 1 {
			return errors.New("usage: flclash proxy speed NODE")
		}
		if service == nil {
			return errors.New("proxy speed requires the managed FlClash backend")
		}
		result, err := service.testProxySpeed(positional[0])
		if err != nil {
			return err
		}
		fmt.Printf("%s: %s\n", positional[0], formatTUISpeed(result))
		return nil
	default:
		return fmt.Errorf("unknown proxy command %q; use `flclash proxy -help`", args[0])
	}
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
		fmt.Printf("%s %s\n", marker, node)
	}
	return nil
}
