# FlClash TUI for Linux

This repository includes a Linux TUI/CLI entry point that uses the same
Go/Mihomo core as the FlClash desktop application. Running `flclash`
without a subcommand opens a full-screen terminal interface; subcommands remain
available for scripts and headless servers.

## Build

Initialize the Mihomo submodule and build the binary:

```bash
git submodule update --init --recursive
make cli-linux
```

The binary is written to `dist/flclash`; bundled Geo databases are copied
to `dist/data`. Keep that directory beside the binary when moving a source
build to an offline host.

## Install the Debian package

Download the `v0.3.14` package matching `dpkg --print-architecture` from the
[GitHub Releases](https://github.com/yqlay/flclash-tui/releases) page, then
install it with:

```bash
sudo dpkg -i flclash-tui_0.3.14_amd64.deb
# or: sudo dpkg -i flclash-tui_0.3.14_arm64.deb
```

The package installs the executable at `/usr/bin/flclash`, bundled offline
Geo databases under `/usr/share/flclash-tui/data`, and documentation under
`/usr/share/doc/flclash-tui`. It declares `Conflicts/Replaces` for the old
`flclash-cli` Debian package, so installing it migrates the executable name
without deleting profiles or settings. Before the core starts, missing Geo data is copied
from the package into the selected FlClash data directory. A valid existing
database is preserved; an incomplete or invalid MMDB download is repaired from
the bundled copy. Initial startup therefore does not depend on GitHub access.

## Run the TUI

```bash
./dist/flclash
```

On first launch, when the default configuration is missing, the TUI creates a
minimal DIRECT-only configuration automatically. Open Profiles, select the
visible `+ Import subscription URL` row, and press Enter. Paste the URL and
press Enter again, then select the downloaded profile and activate it. Existing
configuration files are never overwritten.

Imported profiles remember their subscription source in the user-only TUI
state file. Select a profile and press `U` to download and apply its latest
subscription directly from the saved URL; refresh never asks for the URL
again. Profiles imported by an older version without source metadata remain
local profiles instead of pretending to support refresh. Link one once with
`flclash profile link --config PROFILE URL`, or import the subscription as
a new linked profile. Refresh preserves local mode, port, TUN, LAN, IPv6,
unified-delay, TCP-concurrent, and log-level settings. The replacement is
validated and written atomically; an active profile is hot-reloaded without
stopping running listeners, and the original file is restored when reload
fails.

To give a downloaded profile a readable file name, select the inactive profile
in Profiles and press `F2` or `u`. Type the new name in the visible input panel
and press Enter; `.yaml` is added automatically when omitted. Existing names,
paths, unsupported extensions, and the active profile are protected. Activate
another profile first when the current profile needs to be renamed.

Opening the TUI loads the core configuration but leaves all proxy listeners
stopped, so the mixed port is not occupied while settings are being prepared.
The Dashboard exposes `Service`, `System proxy`, `TUN`, `Mode`, and `Mixed
port`. Turning `System proxy` on automatically applies staged settings, starts
the service, and then enables the desktop proxy; there is no separate manual
start step. If desktop proxy setup fails, the automatic service start is rolled
back. Stopping the service also disables a system proxy owned by that TUI
instance. Tools contains the complete settings list (`Mixed port`, `TUN`,
`Allow LAN`, `IPv6`, `Unified delay`, `TCP concurrent`, `Mode`, and `Log
level`) plus YAML and maintenance actions. Unified delay defaults to the
original FlClash warmed-connection measurement instead of including every
first-connection and TLS setup cost. TCP concurrent also follows the original
FlClash default. Rows show the current state before the available action—for
example, `Service RUNNING · Enter to stop` means the service is currently
running.

By default, the TUI starts or reconnects to a detached local Service and uses
private Unix sockets for service and Core management, so it does not reserve a
TCP controller port. Pressing `q` detaches only the TUI and returns to the
shell; a running Service keeps proxying in the background. Reopening
`flclash` reconnects to it. Pressing `Ctrl+C`, or running
`flclash stop`, stops the managed Service/Core.

Exactly one managed FlClash Service/Core is allowed per Linux user. The
manager socket and kernel-backed ownership lock live under
`/run/user/<UID>/flclash` (with a UID-specific secure temporary fallback), so
changing `--directory` or `XDG_CONFIG_HOME` cannot create a second backend.
Multiple TUI frontends are allowed and all attach to that single backend. An
additional frontend opens with a notice listing the PID and TTY of frontends
that are already active; Dashboard keeps the frontend count current. `q`
closes only the current frontend. `Ctrl+C` stops the shared backend, so every
attached frontend will report that the controller stopped. Frontend session
locks disappear automatically after a clean exit or crash. On the first
v0.3.12 launch, the client also detects and migrates the legacy manager socket
from the previous configuration-directory location.

No settings shortcut has to be memorized: all settings and lifecycle actions
are selectable rows. The single-letter keys remain optional accelerators.
Port, mode, LAN, IPv6, log-level, and TUN changes are persisted to the active
YAML profile. Importing or activating a profile while stopped does not start
proxy listeners. Subscription downloads identify as a Mihomo client for
providers that reject generic browser or Go HTTP user agents.

The active profile, subscription bindings, and last proxy selection for each group are stored in
`.flclash-cli-state.json` under the active data directory. The file is written
atomically with user-only permissions. On the next launch, the TUI restores
that profile, its YAML settings, and matching proxy-group selections. Runtime
Service state belongs to the detached Service: after `q`, reopening the TUI
shows the same running state. After `Ctrl+C` or `flclash stop`, the next
launch starts with listeners stopped.

The sidebar follows the graphical FlClash information architecture:

- Dashboard: service state, system proxy, TUN, outbound mode, mixed port,
  public-IP network detection, intranet IP, speeds, traffic totals, and
  connection counts. It also refreshes system memory, process RSS, and Go heap
  once per second. Managed and external modes show TUI and Core RSS
  separately. Press `d` to test the current route latency, `v` to test current
  route download speed, or `n` to refresh both IP checks.
- Proxies: proxy groups and nodes in configuration-file order. In the group
  list, use `↑↓/ws`, press Enter to open that group's nodes, and press `d` to
  test every node's delay or `v` to test every node's download speed. In node
  selection, use `↑↓/ws`, press Enter to apply the node, press `d` to test its
  delay, press `v` to test its download speed, and press Esc to return to
  groups. `A` also tests the current group's delays. Press `[`/`]` to switch
  between Groups and Providers. Speed tests run serially so nodes do not
  compete for bandwidth.
- Profiles: import a subscription, activate/rename profiles, and press `e` to
  edit the selected YAML in `$EDITOR`. Active-profile edits are validated and
  hot-reloaded; invalid edits or failed reloads restore the previous file.
- Requests: active and recently completed requests observed during the current
  TUI session. Press `x` to clear this local history.
- Connections: current connections and traffic. Press `d` to close the selected
  connection or `x` to close all.
- Logs: latest captured core events. Press `e` to export them under the active
  data directory's `logs/` folder or `x` to clear the view.
- Tools: all core settings, current-YAML editing, backup/restore, Geo database
  updates, traffic-counter reset, and a GitHub Release update check.

The terminal runtime uses Bubble Tea's model-update-view event loop. Controller
polling and long-running actions execute outside the input loop, while the
renderer updates only terminal rows that changed.

The layout is responsive. At normal sizes it keeps the sidebar and content
panels. Below 88 columns or 18 rows it switches to a full-width navigation or
content view; `←`/Esc opens navigation and `→`/Enter opens its selected page.
Dashboard becomes a viewport whenever all status panels do not fit. Use
`PgUp`/`PgDn` to reach network, memory, traffic, frontend,
and configuration rows. Terminals down to `44x10` remain operable; smaller
terminals show an exact resize requirement without drawing broken borders.

Keyboard shortcuts:

```text
← sidebar      → content       Tab switch focus
1 dashboard    2 proxies       3 profiles      4 requests
5 connections  6 logs          7 tools         U refresh subscription
↑↓/ws move     Enter open/apply Esc back        d delay
PgUp/PgDn scroll compact Dashboard              mouse drag selects text for copying
[/] proxy view r refresh       R reload         S system proxy
c start/stop   x clear/all     v speed         e edit/export
F2/u rename    n import/check  A test group    q detach TUI
Ctrl+C stop Service/Core and exit
```

Each download speed test requests at most 100 MB for at most five seconds. A
completed transfer uses `100 MB / actual duration`; an incomplete transfer
uses `downloaded MB / 5 seconds`. A whole-group test can therefore consume up
to 100 MB per node.

`q` exits only the frontend. `Ctrl+C` stops the managed Service/Core and exits.
Neither key stops a Core connected through explicit `--no-start` external mode.

The TUI starts with the sidebar focused. Use `↑↓/ws` and Enter to open a page,
or press `1`–`7` directly. `←` always focuses the sidebar and `→` always opens
the highlighted sidebar page and focuses its content. `Tab` also switches
between the two panels. On Proxies, Enter opens node selection and Esc returns
to proxy groups; `[`/`]` changes between Groups and Providers. Dashboard and Tools support `↑↓/ws` selection
followed by Enter. Action keys are page-scoped, so `e`, `x`, and Enter do not
trigger unrelated operations on another page.

You can use another configuration directory or file:

```bash
./dist/flclash --directory ~/.config/flclash-work
./dist/flclash --config /path/to/config.yaml
```

Use `--controller` and `--no-start` to open the TUI for a core already running
in another process. `--directory` and `--config` select storage and profiles;
they do not bypass the one-managed-backend-per-user rule. If an explicitly
requested target conflicts with the active backend, FlClash reports the active
configuration and asks you to stop it first.

The original foreground mode is still available:

```bash
./dist/flclash run --config /path/to/config.yaml
```

The process stays in the foreground. `Ctrl-C` stops listeners and shuts down
the Mihomo executor. `SIGHUP` reloads the configuration.

## Run any command through FlClash

Prefix any external command with `flclash` to run it through the Mixed Port of
the currently running managed Service/Core:

```bash
flclash git clone https://github.com/owner/repository.git
flclash curl https://example.com
flclash wget https://example.com/file
flclash npm install
```

The wrapper reads the live `mixed-port` value from the Core's private Unix API,
checks that the listener accepts connections, and then replaces its process
with the requested command. It sets uppercase and lowercase `HTTP_PROXY`,
`HTTPS_PROXY`, and `ALL_PROXY` values to
`http://127.0.0.1:<mixed-port>` only for that command. It does not persist
environment variables or change desktop system-proxy settings.
The external program must support these standard proxy environment variables;
the wrapper can launch any executable but cannot force software that ignores
proxy variables to route through them.

Successful commands receive their original arguments, terminal, signals,
standard streams, and exit behavior without extra status text. A stopped
Service/Core, unavailable controller, invalid or closed Mixed Port, missing
command, and process-start failure are reported in both English and Chinese.

`flclash exec COMMAND ...` is an equivalent explicit spelling. Use
`flclash -- COMMAND ...` when an external executable has the same name as a
built-in FlClash command. Pipelines and redirection require an explicit shell:

```bash
flclash sh -c 'curl -s https://example.com | jq .'
```

## Validate configuration

```bash
./dist/flclash check --config /path/to/config.yaml
```

## Control a running instance

If the configuration enables Mihomo's external controller, the CLI can inspect
proxy groups and select a proxy:

```bash
./dist/flclash proxy list --controller 127.0.0.1:9090
./dist/flclash proxy select --controller 127.0.0.1:9090 PROXY Tokyo
```

If the controller has a secret, pass `--secret` or keep the controller bound
to localhost.

TUN mode is exposed on Dashboard and Tools and may require elevated Linux
network permissions. Desktop system-proxy support currently targets GNOME and
MATE `gsettings`, matching the Linux integration used by FlClash.

## Update from GitHub Releases

Check the latest public release without changing the installed version:

```bash
flclash update --check
```

Download, verify, and install an available update:

```bash
flclash update
```

The updater starts from the trusted `yqlay/flclash-tui` GitHub release channel
and follows GitHub's repository-rename metadata. It discovers a recognizable
FlClash Debian package for the current architecture instead of requiring one
hard-coded filename, so legacy `flclash-cli`, current `flclash-tui`, and future
package naming can migrate safely. A matching `.sha256` or aggregate
`SHA256SUMS` asset is accepted. Before installation, the updater verifies the
GitHub owner/repository relationship, SHA-256, and the Debian package's
internal package name, version, and architecture. Installation uses
`sudo dpkg -i` and may request the system password. For unattended use,
`--yes` confirms the warning; `--download-only` verifies the package but does
not install it. While the package downloads, an interactive terminal shows a
live progress bar with the percentage, downloaded size, and current transfer
speed.

**If the current version works well, do not update lightly. / 当前版本使用正常时，请勿轻易更新。**
