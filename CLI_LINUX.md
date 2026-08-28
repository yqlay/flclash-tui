# FlClash Linux CLI/TUI quick reference

This is the complete Linux CLI/TUI reference. The concise Chinese landing page is [README.md](README.md), with an English version in [README_EN.md](README_EN.md). Runtime help is available through `flclash --help`, `flclash COMMAND --help`, and `flc --help`.

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
- **TUN** is a separately leased user- or system-scoped entry managed by the packaged root helper.
- **Proxy port** is Mihomo's `mixed-port`: one port accepts HTTP and SOCKS5 proxy clients.

CLI and TUI frontends submit revisioned transactions to Backend over a private Unix socket. Backend validates, writes atomically, reloads Core, and rolls back on failure. Multiple TUI frontends may attach to the same Backend, but only one Backend is allowed per user.

Managed proxy listeners accept only loopback sockets owned by the Backend UID. Each user therefore sees and closes only connections accepted by that user's Core. Port conflicts do not prevent a second user from starting: Backend keeps the configured port as the preference and selects a free runtime port.

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

`net delay` warms the active route, takes five RTT samples, and reports
the median and mean successive-sample jitter. `net speed` measures aggregate
Cloudflare download throughput across four concurrent streams, stopping after
99,999,999 bytes or five seconds. Both commands use the Backend's active runtime
Proxy port, including an automatically selected fallback port. In `silent`
mode they use the authenticated private FLC listener, so the reported public IP,
latency, and throughput describe the selected FLC outbound rather than the
machine's direct connection.

Profiles and configuration:

```bash
flclash profile list [--json]
flclash profile current
flclash profile import URL
flclash profile import-file /path/to/config.yaml
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

flclash history show [--follow] [--json] [--state all|active|done] [--search TEXT] [--limit N]
flclash history clear
flclash connections show [--json]
flclash connections close ID
flclash connections close all
```

History is Backend's shared, persistent, up-to-500-entry record derived from active Mihomo connections. It contains active and recently completed flows, not HTTP bodies. Backend reloads it after restart; restored entries begin as completed until Mihomo reports them active again. `history clear` clears both memory and disk without closing connections, while `connections close all` does not erase History.

`profile import-file` validates and copies a regular `.yaml`/`.yml` file into the FlClash data directory without modifying the source. It preserves the basename and adds `-2`, `-3`, and so on when needed. Internal `.flclash-silent-runtime-*` and `.flclash-managed-runtime-*` YAML files are never shown as Profiles.

TUN scope is explicit:

```bash
flclash tun on              # current UID; same as: tun user on
flclash tun user off
flclash tun system on       # Polkit authorization; exclusive for the machine
flclash tun status
```

Multiple user-scoped leases may coexist. A system-scoped lease conflicts with every other TUN lease and is released automatically when its owning Backend exits. User scope restores after Backend restart; system scope stays off until authorized again. The `.deb` installs `flclash-tun-helper.service`; portable tar installations retain non-TUN features but report that the helper is unavailable.

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

`silent` is the default. It does not proactively take over user network connections: ordinary applications remain direct and only `flc`-prefixed commands use FlClash. Backend disables System proxy, TUN, LAN/DNS/controller/user listeners in a temporary runtime overlay. Once an FLC outbound is selected it exposes one authenticated loopback listener on the same runtime Proxy port used by native modes; before that it exposes no traffic entry, and Backend remains available with Core stopped. The shared YAML is not modified.

```bash
flclash flc select PROXY
flclash core start
flc curl https://example.com
```

Outside silent mode, `flc` uses the normal Proxy port. It sets `HTTP_PROXY`, `HTTPS_PROXY`, and `ALL_PROXY` only for the child process. It verifies the entry and fails closed instead of silently running direct. Applications must support proxy environment variables. Use an explicit shell for pipelines:

```bash
flc sh -c 'curl -s https://example.com | jq .'
```

Live `flclash port PORT` changes are Backend transactions: target TCP/UDP availability, the new listener, old-listener closure, and managed System proxy are checked, with rollback on failure. Listener recreation is not a zero-gap handoff.

## TUI pages and keys

```text
1 Dashboard     Core/System proxy/TUN/Mode/Proxy port/network/memory/30-sample traffic chart
2 Proxies       groups/nodes/Providers, selection, delay and speed tests
3 Profiles      import URL/local YAML, activate, update, rename, edit
4 History       persistent shared active and recent connection history
5 Connections   current Core connections and close actions
6 Logs          Core and application events, export and clear
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
q               exit only this TUI               Ctrl+C        shutdown Backend + Core
```

The Dashboard overlays upload (blue), download (green), and overlap (cyan) as
line-only curves on one shared-scale, 30-sample chart. The colored `↑` and `↓`
speed values above the chart act as its legend.
`Rule route`/`Silent route` and `Cloudflare DL` are selectable with ↑/↓ or
`w`/`s`; Enter runs the selected test, while `d` and `v` remain direct shortcuts.
Compact terminals expose the chart and selected rows through automatic
Dashboard scrolling, and the layout remains operable down to `40x10`.
Long-running tests run outside the input loop; group speed tests are serial and
each node uses four download streams for at most 100 MB or five seconds. Delay
tests use five samples and show median/jitter when space permits.

Operation progress, results, warnings, and errors open on a dedicated
notification page instead of being appended to the footer. Enter confirms and
closes the current notification, Esc closes it without changing the underlying
page, and ↑/↓ or PgUp/PgDn scroll long messages. Notifications are also written
to Logs; repeated progress for one operation replaces its previous entry.

Application events use timestamped INFO/WARN/ERROR records. The TUI keeps the latest 500 entries; `flclash logs` reads the persistent Backend log, which rotates at 5 MiB and keeps one backup. Subscription URLs, YAML contents, and FLC credentials are not logged.

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
