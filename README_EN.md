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

1. Open **Profiles** and import a subscription URL or local profile file.
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
flclash ssh add home host --user user --password --local-port 1080
flclash ssh connect home
flc ssh curl https://example.com

# Follow host policy by default; direct mode requires the host FlClash TUN off
flc ssh -d curl https://example.com

# Encrypted private key; key passphrase and SSH password may both be saved
flclash ssh add school host --user user --identity ~/.ssh/id_ed25519 --passphrase --password

# Reuse the tunnel from another local SOCKS5-aware application
ALL_PROXY=socks5h://127.0.0.1:1080 curl https://example.com

flclash ssh list
flclash ssh test home
flclash ssh disconnect home
```

`flc ssh COMMAND` does not force a remote proxy port. After SSH decrypts on B, B's Clash, TUN, routing rules, or other network policy determines the exit. `flc ssh -d COMMAND` is a strict direct check: B must have compatible FlClash with a queryable Backend, and it refuses to run if B reports a transparent TUN, an unknown state, or an incompatible version.

SSH profiles can also be managed from the TUI **SSH** page. Its main view keeps a bordered profile list and the selected profile's Dashboard visible together; Tab cycles focus through the sidebar, profile list, and Dashboard. Username and Host are separate fields, preventing accidental use of the current WSL username. Unencrypted keys need no passphrase; an encrypted key without a saved passphrase opens a masked one-time prompt inside the TUI. When a key and login password coexist, the specified key is tried first and the password is reserved for fallback or a required second factor. OpenSSH warnings go to Logs without corrupting the TUI. The Dashboard shows A and B inet IPs first, then the verified-direct exit followed by B-managed exit IP, latency, and speed; direct is visibly blocked while a transparent TUN is active.

Under WSL, do not use a `/mnt/c/...` key that appears as mode `0777`; OpenSSH rejects it. Copy it into `~/.ssh/` and run `chmod 600 ~/.ssh/KEY` first.

History, Connections, and Logs support `/` search and Enter for full details. `f` filters History state or log level. Destructive clears and connection closes require confirmation; Logs combine persistent Backend records with current TUI events.

## Common commands

```bash
flclash status
flclash mode rule|global|direct|silent
flclash port [PORT|off]
flclash sys status|on|off
flclash tun status|user on|system on|off
flclash profile import URL
flclash profile import-file /path/to/nodes.txt
flclash logs --lines 100 --follow
flclash history show --limit 20
flclash exit                       # stop frontends, Backend, Core, and SSH
```

In the TUI, `q` or closing its terminal exits only the current frontend and releases its record; `Ctrl+C` exits everything, `Ctrl+N` opens notification details, and `?` shows all keys.

Profile import accepts Mihomo/Clash YAML, URI lists and Base64 wrappers, SIP008, sing-box/Xray JSON, and common Surge/Quantumult X/Loon proxy lines; SIP008 v2ray-plugin/simple-obfs settings are preserved. Local files may use any extension. Duplicate or Core-reserved node names are disambiguated; an unsupported or malformed node rejects the whole import instead of being silently dropped.

See [CLI_LINUX.md](CLI_LINUX.md) for all commands, TUN, Profiles, History, logs, data paths, and troubleshooting. Runtime help is available through `flclash --help`, `flclash COMMAND --help`, and `flc --help`.

This is an unofficial terminal derivative of [FlClash](https://github.com/chen08209/FlClash). See [NOTICE](NOTICE) and [LICENSE](LICENSE).
