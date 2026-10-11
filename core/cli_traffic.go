//go:build linux && !cgo && cli

package main

import (
	"errors"
	"fmt"
)

func trafficCommand(args []string) error {
	if len(args) == 0 || cliSubcommandHelp(args) {
		fmt.Println("Usage: flclash traffic reset")
		fmt.Println("Reset the managed Backend traffic counters.")
		return nil
	}
	if args[0] != "reset" || len(args) != 1 {
		if len(args) > 1 && cliSubcommandHelp(args[1:]) {
			return trafficCommand(nil)
		}
		return errors.New("usage: flclash traffic reset")
	}
	client, status, err := currentManagedService()
	if err != nil {
		return err
	}
	status, err = client.resetTraffic(status.Revision)
	if err != nil {
		return err
	}
	fmt.Printf("Traffic counters reset (revision %d)\n", status.Revision)
	return nil
}
