# FlClash TUI

[**中文文档**](README_zh_CN.md)

[![Release](https://img.shields.io/github/v/release/yqlay/flclash-tui?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![Downloads](https://img.shields.io/github/downloads/yqlay/flclash-tui/total?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![License](https://img.shields.io/github/license/yqlay/flclash-tui?style=flat-square)](LICENSE)

FlClash TUI is a Linux terminal proxy manager for Mihomo/Clash configurations. It combines a full-screen TUI for interactive use with a predictable CLI for SSH sessions, headless hosts, and scripts. Running `flclash` opens the TUI; running `flclash COMMAND` performs one management operation; prefixing an external program with `flc` runs only that program through FlClash.

This is an unofficial derivative of [FlClash](https://github.com/chen08209/FlClash). It reuses FlClash's Go/Mihomo integration but has a terminal-specific lifecycle, Backend, command set, and interface. It is not an official FlClash or Mihomo release. See [NOTICE](NOTICE) and [LICENSE](LICENSE).

## The four states that matter

The Dashboard deliberately shows these as separate rows:

| State | What it controls | What it does not mean |
| --- | --- | --- |
| **Backend** | The detached per-user coordinator, private IPC, profile transactions, shared History, Core lifecycle, and System proxy changes | A running Backend does not mean proxy listeners are running |
| **Core** | The managed Mihomo executor and its listeners | Starting Core does not automatically change desktop proxy settings |
| **System proxy** | Linux desktop HTTP/HTTPS proxy settings, currently through GNOME/MATE `gsettings` | Applications that ignore system proxy settings are not captured |
| **TUN** | Explicit user-scoped or system-scoped network interception | Debian installs a restricted root helper; Backend/Core stays under the user's UID |

`flclash core stop` therefore stops Core listeners but leaves Backend available for CLI/TUI clients. `flclash backend stop`, `flclash shutdown`, or managed-TUI `Ctrl+C` stops both Backend and Core and disconnects every frontend.

### Proxy port and Mihomo `mixed-port`

The interface calls this value **Proxy port**. In Mihomo YAML its key is `mixed-port` because one TCP port accepts both HTTP proxy and SOCKS5 proxy clients; “mixed” describes the protocols accepted by the listener, not mixed or random traffic. For example, a Proxy port of `7891` can be used as `http://127.0.0.1:7891` by HTTP/HTTPS-aware applications and as `socks5://127.0.0.1:7891` by SOCKS5-aware applications.

## Architecture and write boundary

In managed mode there is exactly one Backend per Linux user and any number of CLI/TUI frontends:

```text
TUI pages (including Dashboard) ─┐
                                  ├─ revisioned requests ─▶ Backend ─▶ private Core socket ─▶ Mihomo
flclash CLI commands ──────────┘                         ├─ atomic YAML/profile writes
                                                            ├─ Linux System proxy
                                                            └─ shared History and state
```

The Dashboard is a TUI page, not a separate daemon. “All frontends modify state through Backend” means that CLI and every TUI page submit a request over a user-only Unix socket. Backend validates the expected revision and profile digest, performs an atomic write or OS operation, asks Core to reload, and rolls back on failure. Frontends may read and display data, but they do not directly overwrite the shared YAML or set the System proxy. This prevents two terminals from silently overwriting each other.

An explicit external-controller session (`flclash tui --no-start --controller ...` or `flclash proxy ... --controller ...`) is the advanced exception: it can inspect or select nodes in that external Core, but it does not own FlClash's shared YAML, Backend lifecycle, or System proxy.

Backend and Core management use private Unix sockets rather than a public TCP controller. The Backend socket is mode `0600`; mutations carry a monotonically increasing revision and retry IDs. Profile edits additionally use content digests and per-profile locks.

Each UID has its own Backend, Core, TUI clients, runtime configuration, and connection history. Managed listeners are loopback-only and reject clients whose Linux socket owner does not match the Backend UID. If the configured Proxy port is occupied, Backend chooses an available runtime port without overwriting the preferred value; status and TUI show both values when they differ.

TUN has two scopes. `tun user on` captures only the current UID and may coexist with other users' user TUNs. `tun system on` requires Polkit administrator authorization, captures all users, and is exclusive with every other TUN lease. The helper creates the device and policy routes, passes its file descriptor to the unprivileged Backend, and cleans up when Backend disconnects. User TUN restores after Backend restart; system TUN must be authorized again.

## Install

### Debian or Ubuntu packages

Choose the package matching `dpkg --print-architecture` from [GitHub Releases](https://github.com/yqlay/flclash-tui/releases). Version 0.5.2 packages are named:

```text
flclash-tui_0.5.2_amd64.deb
flclash-tui_0.5.2_arm64.deb
```

Example for AMD64:

```bash
wget https://github.com/yqlay/flclash-tui/releases/download/v0.5.2/flclash-tui_0.5.2_amd64.deb
wget https://github.com/yqlay/flclash-tui/releases/download/v0.5.2/flclash-tui_0.5.2_amd64.deb.sha256
sha256sum -c flclash-tui_0.5.2_amd64.deb.sha256
sudo dpkg -i flclash-tui_0.5.2_amd64.deb
```

The package installs `/usr/bin/flclash`, the `/usr/bin/flc` entry point, documentation, and bundled GeoIP/GeoSite/ASN data. Missing or unusable Geo files can be restored locally before Core initialization, so first startup does not depend on downloading those files from GitHub.

### Build from source

```bash
git clone --recurse-submodules https://github.com/yqlay/flclash-tui.git
cd flclash-tui
make cli-linux
./dist/flclash
```

The build writes `dist/flclash`, creates `dist/flc` as a symlink to the same binary, and copies Geo resources to `dist/data/`. Keep `data/` beside a portable source build. The terminal build needs Go, Git, Make, and the Mihomo submodule; it does not require starting the Flutter application.

## Quick start

```bash
flclash
```

With the implicit default data directory, Backend creates a minimal DIRECT-only profile when none exists. Core remains stopped, so the Proxy port is not occupied yet.

1. Open **Profiles**, select **Import subscription URL**, paste the URL, and press Enter.
2. Select the downloaded profile and press Enter to activate it.
3. Open **Proxies**, open a group, and select a node.
4. The default is `silent`; choose `rule`, `global`, or `direct` only when a normal proxy entry is needed.
5. In silent mode, the first `flc COMMAND` automatically selects the first usable proxy group and starts **Core**. You can still choose a different FLC outbound explicitly. To proxy desktop applications, leave silent mode before enabling **System proxy**.

On a headless server, use `flc`, an application's explicit proxy setting, or TUN instead of expecting desktop `gsettings` to affect remote shells.

## Traffic modes and `flc`

### Native modes

- `rule`: traffic reaching Mihomo follows rules in the active profile.
- `global`: traffic reaching Mihomo uses Mihomo's global selection.
- `direct`: traffic reaching Mihomo is sent directly.

These modes decide what Mihomo does **after traffic reaches it**. They do not by themselves redirect every process on the machine; use System proxy, an explicit Proxy port, `flc`, or TUN to provide an entry path.

### Silent mode

`silent` is the default FlClash runtime mode rather than a Mihomo YAML mode. It means: **FlClash does not proactively take over the user's network connections; ordinary programs remain direct, and only commands prefixed with `flc` receive a proxy environment**.

In silent mode Backend builds a temporary runtime overlay that:

- turns off the normal Proxy port and all ordinary HTTP/SOCKS/redir/tproxy listeners;
- disables System proxy, TUN, LAN access, DNS listening, external controllers, user listeners, tunnels, and server listeners in the runtime copy;
- after an FLC outbound is configured, exposes exactly one random loopback-only mixed listener for `flc`; without one, exposes no traffic entry at all;
- protects that listener with random per-runtime credentials;
- routes it directly through the selected FLC outbound (a node or proxy group);
- leaves the shared profile YAML byte-for-byte unchanged and removes runtime overlays during shutdown.

On first use, `flc COMMAND` selects the first usable proxy group, creates the private listener, and starts Core automatically. Select an outbound explicitly only when you want to override that choice:

```bash
flclash flc select PROXY  # optional override
flc curl https://example.com
```

`flc COMMAND [ARG...]` sets upper- and lower-case `HTTP_PROXY`, `HTTPS_PROXY`, and `ALL_PROXY` only for its child process. In silent mode it obtains the authenticated private URL from Backend; in `rule`, `global`, or `direct` mode it uses the normal Proxy port. It checks that the entry is reachable and **fails closed** if Backend, Core, or the relevant listener is unavailable—it never silently runs the requested command directly.

```bash
flc curl https://example.com
flc wget https://example.com/file
flc git clone https://github.com/owner/repository.git
flc sh -c 'curl -s https://example.com | jq .'
```

Pipes and redirections require an explicit shell as in the final example. The child application must honor standard proxy variables; `flc` cannot force an application that ignores them through a proxy. GNU Wget is invoked with `--no-config` so a stale `~/.wgetrc` proxy cannot override the live entry.

Useful management commands:

```bash
flclash flc status
flclash flc select 'Proxy Group'
flclash flc test
flclash flc env
```

`flclash flc test` performs a real HTTP request through the active command proxy. `flclash flc env` (and `flclash env`) prints shell exports; in silent mode that output contains temporary credentials, so do not paste it into logs or a persistent shell profile.

Backend owns live Proxy-port changes. The configured value remains the preferred port; if it is occupied, Backend selects a free runtime port. It verifies the new loopback listener and closure of the old port, then updates the desktop System proxy to the actual port. A failure restores the previous profile, Core listener, and managed System proxy. Listener recreation can still cause a brief transition gap.

## Command reference

Use `flclash --help`, `flclash COMMAND --help`, and `flc --help` as the executable source of truth.

### Dashboard and lifecycle commands

| Command | Meaning |
| --- | --- |
| `flclash` or `flclash tui` | Open a TUI frontend; starts/reuses Backend but leaves Core's existing state unchanged |
| `flclash core [status]` | Print `RUNNING` or `STOPPED` |
| `flclash core start` | Start Core listeners; starts Backend first if necessary |
| `flclash core stop` | Stop Core and the System proxy owned by Backend; keep Backend running |
| `flclash core restart` | Stop and start Core without replacing Backend |
| `flclash core reload [--config PATH]` | Validate and reload/switch the active profile |
| `flclash backend [status]` | Show Backend and Core status |
| `flclash backend start` | Start the detached Backend without requiring Core listeners to start |
| `flclash backend restart` | Replace Backend; preserve whether Core was running |
| `flclash backend stop` | Gracefully stop Backend and Core and disconnect frontends |
| `flclash backend logs` | Read Backend logs; accepts the same options as `logs` |
| `flclash backend clients` | List attached TUI PIDs, TTYs, and start times |
| `flclash shutdown` | Alias for `flclash backend stop` |
| `flclash status [--json] [--watch]` | Show Backend PID/version/revision, Core, profile, mode, Proxy port, FLC, System proxy, and frontend count |
| `flclash logs [--lines N] [--follow]` | Show or follow the detached Backend log |

Top-level `start`, `stop`, `restart`, and `reload` are compatibility shortcuts for the corresponding Core operations.

### Commands matching Dashboard rows

| Command | Meaning |
| --- | --- |
| `flclash sys [status]` | Show whether the managed Linux System proxy is enabled |
| `flclash sys on` | Start Core if necessary, then enable System proxy; rejected in silent mode |
| `flclash sys off` | Disable the System proxy without stopping Core |
| `flclash tun [status\|on\|off]` | Inspect or toggle current-user TUN |
| `flclash tun user on\|off` | Explicitly control current-UID interception |
| `flclash tun system on\|off` | Control exclusive whole-system TUN; enabling requires Polkit |
| `flclash mode` | Print the current synthetic/native traffic mode |
| `flclash mode rule\|global\|direct\|silent` | Change mode; silent uses a runtime overlay |
| `flclash port` | Print the configured Proxy port; silent reports it as off while retaining the configured value |
| `flclash port PORT` | Set Mihomo `mixed-port` to `1..65535` |
| `flclash port off` | Set the normal Proxy port to `0` |
| `flclash flc status\|select NAME\|test\|env` | Manage the command-only proxy entry |
| `flclash net show` or `net refresh` | Detect public IP, country, intranet address, and route (`DIRECT` or Proxy port) |
| `flclash net delay` | Measure five warm normal-route RTT samples and report median/jitter |
| `flclash net speed` | Measure aggregate Cloudflare download throughput with four streams, up to 100 MB or five seconds |

In silent mode use `flclash flc test`; normal-route `net delay` and `net speed` are intentionally unavailable.

### Profiles and configuration

| Command | Meaning |
| --- | --- |
| `flclash profile list [--json]` | List `.yaml`/`.yml` profiles; `*` marks active and the type is local/subscription |
| `flclash profile current` | Print the active profile path |
| `flclash profile import URL` | Download, validate, and create a linked profile through Backend |
| `flclash profile use NAME` | Validate and activate a profile by name/path |
| `flclash profile update [NAME]` | Refresh a profile from its saved subscription URL while preserving local settings |
| `flclash profile rename NAME NEW_NAME` | Rename an inactive profile safely |
| `flclash profile edit [NAME]` | Edit a temporary copy with `$VISUAL`/`$EDITOR`, then validate and submit it |
| `flclash profile delete NAME` | Delete an inactive profile |
| `flclash profile link --config PATH URL` | Attach a saved subscription source to an older local profile |
| `flclash config path\|show\|validate` | Inspect the active profile |
| `flclash config edit` | Transactionally edit and hot-reload the active profile |
| `flclash config backup` | Create a timestamped backup through Backend |
| `flclash config restore` | Restore the newest valid backup and reload Core |
| `flclash check --config PATH` | Validate a YAML profile without starting Core |

Profile creation, editing, subscription refresh, rename, delete, backup, restore, active-profile switching, local setting changes, and proxy selections use Backend transactions. A stale revision or content digest is rejected rather than overwriting a newer change.

### Proxies, History, and active connections

| Command | Meaning |
| --- | --- |
| `flclash proxy groups [--json]` | List selectable groups and their current nodes |
| `flclash proxy nodes GROUP [--json]` | List nodes in one group |
| `flclash proxy select GROUP NODE` | Validate membership, select through Backend, and remember it; roll back Core if saving fails |
| `flclash proxy delay NODE [--test-url URL]` | Test one node's delay |
| `flclash proxy speed NODE` | Run the Backend-managed 100 MB/5 s download test |
| `flclash history show [--follow] [--json]` | Show or follow the shared recent connection History |
| `flclash history clear` | Clear History only; active Core connections stay open |
| `flclash connections show [--json]` | Show connections currently open in Core |
| `flclash connections close ID` | Close one active connection |
| `flclash connections close all` | Close every active connection |

**History is not an HTTP body/request recorder.** Backend samples Mihomo's active connections, records when a flow first appears, keeps active and recently completed entries (up to 500), and displays destination host/address, network, route chain, and activity state. It is shared by all TUI/CLI frontends and continues collecting while Backend runs even if no TUI is attached. Clearing History does not close Connections; closing Connections does not erase History.

For an explicitly managed external controller, proxy inspection/selection accepts `--controller ADDRESS` and optional `--secret SECRET`. Managed mode uses the private Core Unix socket and does not require a TCP controller.

### Diagnostics, shell integration, and advanced commands

| Command | Meaning |
| --- | --- |
| `flclash geo status\|update` | Inspect bundled Geo resources or ask Core to update them |
| `flclash env [--json]` | Print proxy environment values for the active normal/private entry |
| `flclash doctor [--json]` | Check Backend protocol, Core API, profile validity, and active proxy entry |
| `flclash completion bash\|zsh\|fish` | Generate top-level and subcommand completion definitions |
| `flclash update --check` | Check the trusted GitHub Release channel without installing |
| `flclash update [--yes] [--download-only]` | Download, verify checksum/package metadata, and optionally install a Debian update |
| `flclash run [--config PATH]` | Advanced foreground Core mode without shared Backend; `Ctrl+C` stops it and `SIGHUP` reloads |
| `flclash version` | Print the CLI version |

Install generated completion for the current shell, for example:

```bash
flclash completion bash > ~/.local/share/bash-completion/completions/flclash
flclash completion zsh > ~/.zfunc/_flclash
flclash completion fish > ~/.config/fish/completions/flclash.fish
```

Compatibility spellings remain available: `service` for `backend`, `system-proxy` for `sys`, `outbound-mode` for `mode`, `mixed-port` for `port`, `requests` for `history`, `connections close-all` for `connections close all`, and `flclash exec -- COMMAND` for `flc COMMAND`. New documentation uses the shorter primary names.

## TUI guide

### Pages

| Key | Page | Contents and Enter action |
| --- | --- | --- |
| `1` | **Dashboard** | Core, System proxy, TUN, Mode, Proxy port, network detection, route tests, memory, traffic, History count, frontends, and active profile |
| `2` | **Proxies** | Groups, nodes, Providers, saved selection, delay tests, and serial download tests; Enter opens/selects/updates |
| `3` | **Profiles** | Import subscription, activate, refresh, rename, and edit profiles |
| `4` | **History** | Shared active/recent connection history; `x` clears History without closing connections |
| `5` | **Connections** | Current Core connections; `d` closes selected and `x` closes all |
| `6` | **Logs** | Captured Core events; `e` exports and `x` clears the displayed buffer |
| `7` | **Settings** | Mode, FLC outbound, Proxy port, LAN, IPv6, unified delay, TCP concurrent, log level, TUN, Core, and System proxy |
| `8` | **Maintenance** | Edit current YAML, backup/restore, update Geo resources, reset traffic counters, and check releases |

Rows show the current state first and the available action second. For example, `Core STOPPED · Enter to start` describes the current state, not the result of pressing Enter.
Selecting **Mode** on Dashboard or Settings opens a list containing `rule`, `silent`, `global`, and `direct`; move with ↑/↓ or `w`/`s`, then press Enter to apply the highlighted mode.

### Navigation and keys

```text
← / Esc       focus/open navigation        → / Enter    open content or apply selected row
↑↓ or w/s    move selection               Tab/Shift-Tab switch sidebar/content focus
1..8          open a page directly          ?            toggle the in-app key guide
r             refresh displayed state       R            reload active configuration
PgUp/PgDn     scroll compact Dashboard      [ / ]        switch Groups/Providers

Dashboard:    d route delay · v route speed · n refresh network detection
Proxies:      Enter open/select · Esc groups · d node/group delay · v speed · A group delay
Profiles:     Enter activate/import · n import · U refresh linked · F2/u rename · e edit
History:      x clear shared History
Connections: d close selected · x close all
Logs:        e export · x clear view
Settings:    S System proxy · c Core · t TUN · m mode list · p set port · +/- adjust
Maintenance: b backup · B restore · g Geo update · z reset traffic

q             detach only this TUI; Backend/Core and other frontends continue
Ctrl+C        managed mode: gracefully stop Backend and Core, then disconnect all frontends
```

Long-running network tests and profile operations run outside the input/render loop. Whole-group speed tests run serially so nodes do not compete for bandwidth. Each speed test reads at most 100 MB and lasts at most five seconds.

The layout is responsive. At normal sizes it shows sidebar and content together. Narrow terminals switch to a full-width navigation/content layout; Dashboard becomes scrollable. The complete interface remains operable at `40x10`, while smaller terminals show the required size instead of drawing broken borders.

### Multiple frontends and graceful shutdown

Opening `flclash` in another terminal attaches to the same Backend. Dashboard shows frontend count, and `flclash backend clients` lists sessions. `q` unregisters and detaches only the current TUI. `Ctrl+C` waits for Backend's shutdown acknowledgement, stops the managed System proxy and Core, closes Backend, and causes other frontends to exit. Child processes are waited/reaped, so normal detach/restart/shutdown does not leave zombie Backend processes.

In explicit external mode (`--no-start`) the TUI does not own the external Core; `Ctrl+C` can only leave that frontend and does not kill the unrelated process.

## Profiles, data, and security

The default data directory is:

```text
~/.config/flclash/
```

It contains profiles, the active-state file, logs, backups, the private Core socket, and working data. The Backend manager socket and per-user ownership lock normally live under `/run/user/<UID>/flclash/`, with a UID-specific secure temporary fallback when no user runtime directory exists. `--directory` and `--config` select data/profile paths; they do not bypass the one-Backend-per-user rule.

Relevant behavior:

- shared profile and state files are written atomically with user-only state permissions;
- active profile changes are validated before hot reload;
- failed writes/reloads restore the previous file and Core state where possible;
- subscription refresh preserves local port, mode, TUN, LAN, IPv6, unified-delay, TCP-concurrent, and log-level settings;
- Backend accepts profiles only inside its managed data directory and rejects symlink/non-regular profile transaction targets;
- the private silent listener is loopback-only, authenticated, randomized, and absent outside silent mode;
- no private FLC URL is exposed by ordinary status output.

## Troubleshooting

- **Backend running, Core stopped:** this is normal. Run `flclash core start`; Backend can remain available without occupying a Proxy port.
- **`sys on` does not affect a shell/server app:** System proxy is a desktop preference. Use `flc COMMAND`, configure the application with the Proxy port, or use TUN.
- **`ping` still fails:** ICMP does not use HTTP/SOCKS proxy variables. Test with `flclash flc test`, `flclash net delay`, or `curl`.
- **Silent mode rejects System proxy/TUN/network speed:** intentional; silent mode provides only the private `flc` path. Use `flclash flc test`.
- **`flc` refuses to run:** it fails closed. Check `flclash status`, `flclash flc status`, the selected FLC outbound, and `flclash doctor`.
- **A mutation reports a revision/content conflict:** another frontend changed state. Refresh (`r` or rerun the command) and apply the choice again.
- **A different `--directory` is rejected:** one managed Backend is allowed per user. Stop the active Backend before intentionally switching its data directory.
- **TUN helper unavailable:** install the `.deb` and inspect `systemctl status flclash-tun-helper`; the portable tarball does not install privileged integration.
- **TUN cannot start:** ensure `/dev/net/tun`, cgroup v2, `iproute2`, and `iptables` are available, and that no conflicting system lease exists.
- **HTTPS uses an `http://` proxy URL:** this is expected. HTTPS clients use HTTP `CONNECT` through the mixed Proxy port.

## Update

```bash
flclash update --check
flclash update
```

The updater reads releases from `yqlay/flclash-tui`, selects the current architecture's Debian package, verifies its SHA-256 and internal package name/version/architecture, then invokes `sudo dpkg -i`. `--download-only` verifies without installing and `--yes` confirms non-interactively.

> If the current version works well, do not update lightly.

## Project relationship and license

- Original graphical client: [chen08209/FlClash](https://github.com/chen08209/FlClash)
- Mihomo/Clash.Meta Core: retained as the `core/Clash.Meta` submodule
- TUI interaction layer includes work adapted from [SaladDay/cc-switch-cli](https://github.com/SaladDay/cc-switch-cli)
- License: [GNU General Public License v3.0](LICENSE)

Third-party components remain the property of their authors. See [NOTICE](NOTICE) for attribution and disclaimers.
