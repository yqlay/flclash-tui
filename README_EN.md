# FlClash TUI

[中文首页](README.md) · [Full documentation](CLI_LINUX.md) · [Releases](https://github.com/yqlay/flclash-tui/releases)

[![Release](https://img.shields.io/github/v/release/yqlay/flclash-tui?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![Downloads](https://img.shields.io/github/downloads/yqlay/flclash-tui/total?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![License](https://img.shields.io/github/license/yqlay/flclash-tui?style=flat-square)](LICENSE)

A Mihomo terminal manager for Linux, SSH, and headless hosts. Run `flclash` for the full-screen TUI. The default mode is **silent**: only `flc COMMAND` uses the proxy.

On headless Linux, Codex / Claude often cannot connect until they go through a proxy:

![Codex failing to connect without a proxy](docs/readme/codex-unproxied.png)

Import a subscription, pick a node in Proxies, then start those commands with `flc`.

## Who it is for

- Remote Linux, cloud VMs, lab machines, or WSL, with a subscription or local profile already in hand;
- Proxying only chosen commands such as Codex, Claude, git, or npm;
- Leaving other services, containers, and existing SSH sessions on the direct path;
- Failing closed when the listener, Core, or node is down, instead of silently going direct.

## Install

The installer detects AMD64/ARM64 and selects a Debian or portable package:

```bash
curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash
```

Force a portable installation:

```bash
curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash -s -- --method portable
```

If GitHub is unreachable, download the matching `.deb` or `.tar.gz` from [Releases](https://github.com/yqlay/flclash-tui/releases) and copy it onto the machine. Do not pin a version string; always use the latest Release.

## Fastest path

1. Run `flclash` to open the TUI.
2. In **Profiles**, import a subscription URL or local file, then press Enter to activate it.
3. In **Proxies**, pick a group and node. That group is the `flc` exit.
4. Keep **silent** on the Dashboard, focus **Core**, and press Enter to start it. Do not enable System proxy.
5. In another terminal, run the commands that need the proxy:

```bash
flc codex
flc claude
flc git clone https://github.com/owner/repository.git
flc npm install
flc curl https://example.com
flc bash    # child commands inherit the proxy; leaving the shell drops it
```

`flc` injects uppercase and lowercase `HTTP_PROXY`, `HTTPS_PROXY`, and `ALL_PROXY` into that command only (and into children of `flc bash`). Silent mode uses an authenticated private loopback listener and does not expose a port on the LAN or the public internet. If the listener, Core, or node is unavailable, `flc` errors and refuses to run instead of going direct.

6. When finished:

```bash
flclash exit    # stop this user's frontends, Backend, Core, and SSH
```

Press `q` in the TUI, or close that terminal, to leave only the current frontend. Backend stays up, so `flc` still works.

## Desktop / system-wide proxy

To send ordinary apps such as a browser through the proxy, switch Dashboard mode to `rule` or `global`, then enable **System proxy** or TUN. On headless hosts, prefer `flc` above.

## SSH proxy

Run FlClash only on the machine whose traffic needs proxying, and use another already-online host as the SSH peer. `flc ssh COMMAND` then leaves through that host's network. The local machine does not open a public proxy port; the peer only needs SSH.

```bash
flclash ssh import                 # import Host entries from ~/.ssh/config
flclash ssh add home host --user user --password --local-port 1080
flclash ssh default home
flc ssh curl https://example.com   # opens the default profile; rebuilds a broken tunnel

# Follow host policy by default; -d allows direct only when the host FlClash TUN is off
flc ssh -d curl https://example.com

flclash ssh add school host --user user --jump bastion --identity ~/.ssh/id_ed25519 --passphrase --password
flclash ssh test                   # open the tunnel and run a SOCKS5 handshake
flclash ssh disconnect home        # SSH only; flclash backend stop leaves tunnels up
```

With no tunnel up, `flc ssh` connects the default (or only) profile and keeps it persistent. The TUI **SSH** page manages the same profiles; press `u` to star the default. `flc ssh -d` refuses when the remote transparent TUN is on, the state is unknown, or the version is incompatible. Under WSL, do not use a `/mnt/c/...` key that shows mode `0777`; copy it to `~/.ssh/` and `chmod 600` first.

SSH fields, key passphrases, Jump, and Dashboard probes are documented in [CLI_LINUX.md](CLI_LINUX.md).

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
flclash ssh disconnect             # SSH tunnels only
flclash backend stop               # stop proxy Backend+Core; SSH stays
flclash exit                       # stop frontends, Backend, Core, and SSH
```

In the TUI, `q` exits the current frontend only; `Ctrl+C` is a full exit. Dashboard owns Core, mode, flc, proxy port, TUN, and System proxy. `?` lists keys; `Ctrl+N` opens notifications.

Profile import accepts Mihomo/Clash YAML, URI lists and Base64 wrappers, SIP008, sing-box/Xray JSON, and common Surge/Quantumult X/Loon proxy lines. An unsupported or malformed node rejects the whole import instead of being dropped silently.

See [CLI_LINUX.md](CLI_LINUX.md) for all commands, TUN, Profiles, History, logs, data paths, and troubleshooting. Runtime help is available through `flclash --help`, `flclash COMMAND --help`, and `flc --help`.

This is an unofficial terminal derivative of [FlClash](https://github.com/chen08209/FlClash). See [NOTICE](NOTICE) and [LICENSE](LICENSE).
