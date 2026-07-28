# FlClash CLI for Linux

This repository includes a Linux TUI/CLI entry point that uses the same
Go/Mihomo core as the FlClash desktop application. Running `flclash-cli`
without a subcommand opens a full-screen terminal interface; subcommands remain
available for scripts and headless servers.

## Build

Initialize the Mihomo submodule and build the binary:

```bash
git submodule update --init --recursive
make cli-linux
```

The binary is written to `dist/flclash-cli`.

## Install the Debian package

Download `flclash-cli_0.2.1_amd64.deb` from the [GitHub Releases](https://github.com/yqlay/flclash-cli/releases) page, then install it with:

```bash
sudo dpkg -i flclash-cli_0.2.1_amd64.deb
```

The package installs the executable at `/usr/bin/flclash-cli` and documentation under `/usr/share/doc/flclash-cli`.

## Run the TUI

Create a Clash/Mihomo configuration at `~/.config/flclash/config.yaml`, then:

```bash
./dist/flclash-cli
```

The TUI provides dashboard, proxy-group and node switching, live connections,
traffic totals, logs, core settings, provider updates, YAML profile switching,
configuration reload, and Linux desktop system-proxy control.

Keyboard shortcuts:

```text
1 dashboard    2 proxies       3 connections   4 logs
5 settings     6 profiles      7 providers
↑/↓ group      ←/→ node        Enter switch    r refresh
R reload       a/v/t settings  s system proxy  c start/stop  x close connections
? help         q quit
```

You can use another configuration directory or file:

```bash
./dist/flclash-cli --directory ~/.config/flclash-work
./dist/flclash-cli --config /path/to/config.yaml
```

Use `--controller` and `--no-start` to open the TUI for a core already running
in another process. Use a different data directory, mixed port, and controller
port for each instance.

The original foreground mode is still available:

```bash
./dist/flclash-cli run --config /path/to/config.yaml
```

The process stays in the foreground. `Ctrl-C` stops listeners and shuts down
the Mihomo executor. `SIGHUP` reloads the configuration.

## Validate configuration

```bash
./dist/flclash-cli check --config /path/to/config.yaml
```

## Control a running instance

If the configuration enables Mihomo's external controller, the CLI can inspect
proxy groups and select a proxy:

```bash
./dist/flclash-cli proxy list --controller 127.0.0.1:9090
./dist/flclash-cli proxy select --controller 127.0.0.1:9090 PROXY Tokyo
```

If the controller has a secret, pass `--secret` or keep the controller bound
to localhost.

TUN mode is exposed on the settings page and may require elevated Linux
network permissions. Desktop system-proxy support currently targets GNOME and
MATE `gsettings`, matching the Linux integration used by FlClash.
