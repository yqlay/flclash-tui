# FlClash TUI

[中文首页](README.md) · [Full CLI/TUI reference](CLI_LINUX.md) · [Releases](https://github.com/yqlay/flclash-tui/releases)

[![Release](https://img.shields.io/github/v/release/yqlay/flclash-tui?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![Downloads](https://img.shields.io/github/downloads/yqlay/flclash-tui/total?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![License](https://img.shields.io/github/license/yqlay/flclash-tui?style=flat-square)](LICENSE)

FlClash TUI is a Linux terminal proxy manager for Mihomo/Clash configurations, SSH sessions, and headless servers. It provides a full-screen TUI, a scriptable CLI, and `flc` for proxying one command at a time.

This is an unofficial terminal-focused derivative of [FlClash](https://github.com/chen08209/FlClash), not an official FlClash or Mihomo release. See [NOTICE](NOTICE) and [LICENSE](LICENSE).

## Highlights

- Eight TUI pages, including a Dashboard with a white dotted baseline plus blue upload, green download, and cyan-overlap live traffic curves.
- Selectable route tests and non-blocking lower-right notifications; `Ctrl+N` opens framed history/details, and feedback is also written to Logs.
- One Backend per Linux user, safely shared by multiple TUI/CLI frontends.
- Default `silent` mode keeps ordinary programs direct while `flc COMMAND` uses an authenticated local proxy.
- Subscription URL and local YAML imports, atomic Profile writes, and rollback on failure.
- Connection History survives Backend restarts and supports state, text, and count filters.
- Native AMD64/ARM64 Debian packages and portable archives through one architecture-aware installer.

## Install

The recommended installer detects AMD64/ARM64, selects the native Debian or portable package, and verifies GitHub's SHA-256 asset digest:

```bash
curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash
```

Force a portable installation:

```bash
curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash -s -- --method portable
```

Packages can also be downloaded manually from [Releases](https://github.com/yqlay/flclash-tui/releases).

Build from source:

```bash
git clone --recurse-submodules https://github.com/yqlay/flclash-tui.git
cd flclash-tui
make cli-linux
./dist/flclash
```

## Quick start

```bash
flclash
```

1. In **Profiles**, choose `Import subscription URL` or `Import local YAML file`.
2. Select the imported Profile and press Enter to activate it.
3. Choose a proxy group and node in **Proxies**.
4. In the default silent mode, run:

```bash
flc curl https://example.com
flc git clone https://github.com/owner/repository.git
```

For desktop applications, switch to `rule`, `global`, or `direct` before enabling **System proxy**. On headless systems, prefer `flc`, application-specific proxy settings, or TUN.

## Three distinct states

- **Backend** coordinates Profile transactions, History, Core lifecycle, and System proxy changes for one user.
- **Core** is Mihomo and its traffic listeners; a running Backend does not imply a running Core.
- **System proxy** is a Linux desktop preference, not the listener itself, and has no effect on applications that ignore it.

The displayed **Proxy port** is Mihomo's `mixed-port`, accepting HTTP and SOCKS5 on one port. Native modes and silent mode's authenticated FLC listener share the current runtime port. System proxy remains `DISABLED · locked by silent mode` in silent mode.

## Common commands

```bash
flclash
flclash status
flclash core start|stop|restart
flclash backend status|stop|restart
flclash mode rule|global|direct|silent
flclash port [PORT|off]
flclash sys status|on|off
flclash tun status|user on|system on|off

flclash profile import URL
flclash profile import-file /path/to/config.yaml
flclash profile list

flclash history show --state active --search example --limit 20
flclash history clear
flclash logs --lines 100 --follow
```

See [CLI_LINUX.md](CLI_LINUX.md) for commands, TUI keys, silent/FLC behavior, TUN, History, logs, data paths, and external Core mode. Runtime help is available through `flclash --help`, `flclash COMMAND --help`, and `flc --help`.

## Data and security

- Default data directory: `~/.config/flclash/`.
- Backend and Core use user-private Unix sockets.
- The silent-mode FLC listener is loopback-only and uses temporary credentials.
- Internal `.flclash-*-runtime-*.yaml` files are hidden from user Profiles.
- Logs do not record subscription URLs, YAML contents, or FLC credentials.

## Credits

Thanks to [FlClash](https://github.com/chen08209/FlClash), [Mihomo](https://github.com/MetaCubeX/mihomo), and their contributors.
