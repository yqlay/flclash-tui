//go:build linux && !cgo && cli

package main

import (
	"flag"
	"fmt"
	"strings"
)

func parseCLIJSONFlag(name string, args []string) (bool, error) {
	fs := newCLIFlagSet(name)
	jsonOutput := fs.Bool("json", false, "print JSON")
	if err := parseCLIFlags(fs, args); err != nil {
		return false, err
	}
	if len(fs.Args()) != 0 {
		return false, fmt.Errorf("usage: flclash %s [--json]", name)
	}
	return *jsonOutput, nil
}

// Go's flag parser stops at the first positional. Management commands accept
// options on either side of positionals. Never use this for command/OpenSSH
// passthrough: those arguments belong to the child process.
func parseCLIFlags(fs *flag.FlagSet, args []string) error {
	var options, positional []string
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if arg == "--" {
			positional = append(positional, args[index+1:]...)
			break
		}
		if arg == "-" || !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		name := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
		name, _, hasValue := strings.Cut(name, "=")
		f := fs.Lookup(name)
		if f == nil {
			if name == "h" || name == "help" {
				return flag.ErrHelp
			}
			return fmt.Errorf("flag provided but not defined: -%s", name)
		}
		options = append(options, arg)
		isBool := false
		if value, ok := f.Value.(interface{ IsBoolFlag() bool }); ok {
			isBool = value.IsBoolFlag()
		}
		if !hasValue && !isBool {
			if index+1 == len(args) || args[index+1] == "--" || strings.HasPrefix(args[index+1], "--") {
				return fmt.Errorf("flag needs an argument: -%s", name)
			}
			next := args[index+1]
			if strings.HasPrefix(next, "-") {
				nextName, _, _ := strings.Cut(strings.TrimPrefix(next, "-"), "=")
				if fs.Lookup(nextName) != nil || nextName == "help" || nextName == "h" {
					return fmt.Errorf("flag needs an argument: -%s", name)
				}
			}
			index++
			options = append(options, args[index])
		}
	}
	options = append(options, "--")
	return fs.Parse(append(options, positional...))
}
