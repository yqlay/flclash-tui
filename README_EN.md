# FlClash TUI

[中文首页](README.md) · [Full documentation](CLI_LINUX.md) · [Releases](https://github.com/yqlay/flclash-tui/releases)

[![Release](https://img.shields.io/github/v/release/yqlay/flclash-tui?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![Downloads](https://img.shields.io/github/downloads/yqlay/flclash-tui/total?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![License](https://img.shields.io/github/license/yqlay/flclash-tui?style=flat-square)](LICENSE)

A Mihomo/Clash terminal proxy manager for Linux, SSH, and headless servers. Run `flclash` for the full-screen TUI or `flc COMMAND` to proxy only one command.

## Install

The installer detects AMD64/ARM64 and selects a Debian or portable package:

```bash
curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash
```

Force a portable installation:

```bash
curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash -s -- --method portable
```

Packages are also available from [Releases](https://github.com/yqlay/flclash-tui/releases).

## Quick start

```bash
flclash
```

1. Open **Profiles** and import a subscription URL or local YAML file.
2. Select the Profile and press Enter to activate it.
3. Open **Proxies** and select a proxy group and node.
4. The default mode is `silent`; use `flc` for commands that need the proxy:

```bash
flc curl https://example.com
flc git clone https://github.com/owner/repository.git
flc sh -c 'curl -s https://example.com | jq .'
```

For desktop applications, switch to `rule` or `global` and enable **System proxy** on the Dashboard. On headless systems, use `flc`, application-specific proxy settings, or TUN.

## SSH proxy

Run FlClash only on the machine whose traffic needs proxying. This example sends local traffic through the network exit of `user@host`:

```bash
flclash ssh add home user@host --password --local-port 1080
flclash ssh connect home
flc ssh curl https://example.com

# Encrypted private key; key passphrase and SSH password may both be saved
flclash ssh add school user@host --identity ~/.ssh/id_ed25519 --passphrase --password

# Reuse the tunnel from another local SOCKS5-aware application
ALL_PROXY=socks5h://127.0.0.1:1080 curl https://example.com

flclash ssh list
flclash ssh test home
flclash ssh disconnect home
```

SSH profiles can also be managed from the TUI **SSH** page. Enter opens a per-profile Dashboard with the SSH exit IP, latency, download speed, active connections, and live/cumulative traffic for that SSH SOCKS5 port. Metering requires neither root nor eBPF. `Key passphrase` unlocks the private key and `SSH password` authenticates to the server; both may be set. A connected profile is read-only; disconnect it before editing.

History, Connections, and Logs support `/` search and Enter for full details. `f` filters History state or log level. Destructive clears and connection closes require confirmation; Logs combine persistent Backend records with current TUI events.

## Common commands

```bash
flclash status
flclash mode rule|global|direct|silent
flclash port [PORT|off]
flclash sys status|on|off
flclash tun status|user on|system on|off
flclash profile import URL
flclash profile import-file /path/to/config.yaml
flclash logs --lines 100 --follow
flclash history show --limit 20
flclash exit                       # stop frontends, Backend, Core, and SSH
```

In the TUI, `q` exits only the current frontend, `Ctrl+C` exits everything, `Ctrl+N` opens notification details, and `?` shows all keys.

See [CLI_LINUX.md](CLI_LINUX.md) for all commands, TUN, Profiles, History, logs, data paths, and troubleshooting. Runtime help is available through `flclash --help`, `flclash COMMAND --help`, and `flc --help`.

This is an unofficial terminal derivative of [FlClash](https://github.com/chen08209/FlClash). See [NOTICE](NOTICE) and [LICENSE](LICENSE).
