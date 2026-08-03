# FlClash Linux CLI/TUI quick reference

This file is a compact installed-manual companion. The complete English guide is [README.md](README.md); the complete Chinese guide is [README_zh_CN.md](README_zh_CN.md). Runtime help is available through `flclash --help`, `flclash COMMAND --help`, and `flc --help`.

## Build and install

```bash
git submodule update --init --recursive
make cli-linux
./dist/flclash
```

`dist/flclash` is the manager and `dist/flc` is the command-wrapper entry point. Keep `dist/data/` beside a portable build. Debian packages install `/usr/bin/flclash`, `/usr/bin/flc`, documentation, and bundled Geo data.

## Process model

- **Backend** is the detached per-user coordinator and the only managed writer of shared YAML/state and System proxy settings.
- **Core** is Mihomo and its traffic listeners. It may be stopped while Backend stays running.
- **System proxy** changes supported Linux desktop HTTP/HTTPS settings; it is not the same as starting Core.
- **TUN** is a separate network-layer entry and may require additional privileges.
- **Proxy port** is Mihomo's `mixed-port`: one port accepts HTTP and SOCKS5 proxy clients.

CLI and TUI frontends submit revisioned transactions to Backend over a private Unix socket. Backend validates, writes atomically, reloads Core, and rolls back on failure. Multiple TUI frontends may attach to the same Backend, but only one Backend is allowed per user.

Lifecycle keys and commands:

```text
q in TUI                 detach this frontend only
Ctrl+C in managed TUI    stop Backend + Core and disconnect all frontends
flclash core stop        stop Core, keep Backend
flclash backend stop     stop Backend + Core
flclash shutdown         alias for backend stop
```

## Core command map

```bash
flclash                          # open TUI
flclash core status
flclash core start
flclash core stop
flclash core restart
flclash core reload [--config PATH]

flclash backend status
flclash backend start
flclash backend restart
flclash backend stop
flclash backend logs
flclash backend clients

flclash status [--json] [--watch]
flclash logs [--lines N] [--follow]
```

Dashboard-equivalent commands:

```bash
flclash sys status|on|off
flclash tun status|on|off
flclash mode                         # get
flclash mode rule|global|direct|silent
flclash port                         # get
flclash port PORT|off
flclash flc status
flclash flc select NAME
flclash flc test
flclash flc env
flclash net show|refresh|delay|speed
```

Profiles and configuration:

```bash
flclash profile list [--json]
flclash profile current
flclash profile import URL
flclash profile use NAME
flclash profile update [NAME]
flclash profile rename NAME NEW_NAME
flclash profile edit [NAME]
flclash profile delete NAME
flclash profile link --config PATH URL

flclash config path|show|validate|edit|backup|restore
flclash check --config PATH
```

Proxy state, History, and connections:

```bash
flclash proxy groups [--json]
flclash proxy nodes GROUP [--json]
flclash proxy select GROUP NODE
flclash proxy delay NODE [--test-url URL]
flclash proxy speed NODE

flclash history show [--follow] [--json]
flclash history clear
flclash connections show [--json]
flclash connections close ID
flclash connections close all
```

History is Backend's shared, up-to-500-entry record derived from active Mihomo connections. It contains active and recently completed flows, not HTTP bodies. `history clear` does not close connections; `connections close all` does not erase History.

Other commands:

```bash
flclash geo status|update
flclash env [--json]
flclash doctor [--json]
flclash completion bash|zsh|fish
flclash update --check
flclash update [--yes] [--download-only]
flclash run [--config PATH]            # advanced foreground mode
flclash version
```

Compatibility aliases include `service`, `system-proxy`, `outbound-mode`, `mixed-port`, `requests`, `connections close-all`, and `flclash exec -- COMMAND`. Prefer `backend`, `sys`, `mode`, `port`, `history`, `connections close all`, and `flc COMMAND` in new scripts.

## Silent mode and `flc`

`silent` means that ordinary applications remain direct and only `flc`-prefixed commands use FlClash. Backend disables normal ports, System proxy, TUN, LAN/DNS/controller/user listeners in a temporary runtime overlay and exposes one authenticated loopback listener. The shared YAML is not modified.

```bash
flclash core start
flclash flc select PROXY
flclash mode silent
flc curl https://example.com
```

Outside silent mode, `flc` uses the normal Proxy port. It sets `HTTP_PROXY`, `HTTPS_PROXY`, and `ALL_PROXY` only for the child process. It verifies the entry and fails closed instead of silently running direct. Applications must support proxy environment variables. Use an explicit shell for pipelines:

```bash
flc sh -c 'curl -s https://example.com | jq .'
```

## TUI pages and keys

```text
1 Dashboard     Core/System proxy/TUN/Mode/Proxy port/network/memory/traffic
2 Proxies       groups/nodes/Providers, selection, delay and speed tests
3 Profiles      import/activate/update/rename/edit
4 History       shared active and recent connection history
5 Connections   current Core connections and close actions
6 Logs          captured Core events, export and clear
7 Settings      complete traffic/Core/System proxy settings
8 Maintenance   YAML edit, backup/restore, Geo, traffic reset, update check
```

```text
←/→, Tab       switch navigation/content      ↑↓ or w/s    move
Enter           open/apply selected row        Esc           back
r / R           refresh / reload config         ?             help
[/]             Groups/Providers                PgUp/PgDn     Dashboard scroll
d / v           page-scoped delay / speed       n             network/import
S / c / t / m   System proxy/Core/TUN/mode list p, +, -       Proxy port
U, F2/u, e      update/rename/edit Profile      x             clear/close all
q               detach this TUI                 Ctrl+C        shutdown Backend + Core
```

The layout remains operable down to `40x10`. Long-running tests run outside the input loop; group speed tests are serial and each node downloads at most 100 MB for at most five seconds.

Selecting Mode, or pressing `m` on Dashboard/Settings, opens the `rule`, `silent`, `global`, and `direct` list before making any change. Use ↑/↓ or `w`/`s` and press Enter to confirm.

## Data and advanced external mode

Default data lives in `~/.config/flclash/`. The Backend socket and single-user ownership lock normally live in `/run/user/<UID>/flclash/`. `--directory` and `--config` select managed data; they do not create a second Backend.

To inspect a separately managed Mihomo instance:

```bash
flclash tui --no-start --controller 127.0.0.1:9090
flclash proxy groups --controller 127.0.0.1:9090
flclash proxy select --controller 127.0.0.1:9090 GROUP NODE
```

This external mode does not own FlClash's shared YAML, Backend, or System proxy. Keep external controllers bound securely and provide `--secret` where required.
