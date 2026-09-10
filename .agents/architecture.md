# Architecture

## Core Integration

The Go proxy core in `core/` operates in two modes.

Android lib mode:

- Go core is compiled as a C shared library, `libclash.so`, through `go build -buildmode=c-shared` with CGO.
- Flutter calls it via FFI through the `service` plugin.
- Dart-side implementation: `lib/core/lib.dart` (`CoreLib`).

Desktop core mode:

- Go core runs as a separate process with `CGO_ENABLED=0`.
- Flutter communicates via JSON over socket, using a Unix socket on macOS/Linux and TCP on Windows.
- Dart-side implementation: `lib/core/service.dart` (`CoreService`).

`lib/core/controller.dart` (`CoreController`) selects the implementation based on platform. `lib/core/interface.dart` defines the shared `CoreHandlerInterface`.

Key Go core files:

- `core/hub.go`: handler functions.
- `core/action.go`: dispatch.
- `core/lib.go`: CGO exports.
- `core/server.go`: socket server.

## State Management

Provider files in `lib/providers/`:

- `app.dart`: runtime/UI state, logs, traffic, delays, loading, navigation.
- `config.dart`: persistent config providers, app settings, theme, VPN, proxy style.
- `state.dart`: derived/computed providers, navigation, proxy, tray, color scheme.
- `action.dart`: business logic notifiers, setup, backup, core lifecycle, proxy selection.
- `database.dart`: Drift database provider wrappers.

`globalState` in `lib/state.dart` is a singleton holding app lifecycle, timers, theme, and start/stop state. Providers are generated into `lib/providers/generated/`.

## Database

The app uses Drift/SQLite in `lib/database/`. Current schema version is 2.

Tables:

- `Profiles`
- `Scripts`
- `Rules`
- `ProfileRuleLinks` (`profile_rule_mapping`)
- `ProxyGroups`
- `IconRecords` (`icon_records`)

Rule scenes distinguish global added rules, profile added rules, profile custom rules, and disabled links. Rule and proxy-group ordering use fractional indexing.

Generated Drift output lives in `lib/database/generated/database.g.dart`. After schema changes, run code generation and add or update focused database tests under `test/database/` when converter or migration behavior changes.

## Manager Stack

Managers are nested `InheritedWidget`/`StatefulWidget` components in `lib/application.dart`:

```text
AppEnvManager > StatusManager > ThemeManager
  > [Desktop: WindowManager > TrayManager > HotKeyManager > ProxyManager]
  > ConnectivityManager > CoreManager > AppStateManager
  > [Mobile: AndroidManager > VpnManager | Desktop: WindowHeaderContainer]
```

Each manager in `lib/manager/` handles a specific platform concern. Desktop-only managers are conditionally inserted.

## Core Controller and Actions

`lib/core/controller.dart` (`CoreController`) is a singleton facade over `CoreHandlerInterface`. Public methods delegate to the platform-specific interface, either Android FFI or desktop socket. It has an `@visibleForTesting` constructor and `resetInstance()` for test injection.

Business logic lives in Riverpod notifier classes in `lib/providers/action.dart`:

- `CommonAction`: update check and common UI operations.
- `SetupAction`: config setup and TUN management.
- `BackupAction`: backup/restore with WebDAV sync.
- `CoreAction`: core lifecycle, init, connect, restart, shutdown.
- `SystemAction`: system integration, tray, exit, brightness.
- `StoreAction`: profile storage operations.
- `ThemeAction`: theme state updates.
- `ProxiesAction`: group management and proxy selection.
- `ProfilesAction`: profile CRUD, auto-update, import.

## Platform Managers

Desktop:

- `WindowManager`
- `TrayManager`
- `HotKeyManager`
- `ProxyManager`

Mobile:

- `AndroidManager`
- `TileManager`
- `VpnManager`

Shared:

- `ConnectivityManager`
- `CoreManager`
- `AppStateManager`
- `StatusManager`
- `ThemeManager`

## Build System

`setup.dart` is the release build orchestrator:

1. On Windows, pre-builds Go core via `dart run build_tool windows` and reads `core_sha256.json`.
2. Writes `env.json` (`APP_ENV`).
3. Passes SHA256 as `--dart-define=CORE_SHA256=$val`, embedded at compile time for Windows.
4. Activates `flutter_distributor` for packaging.

Go core building is handled by `build_tool`, a standalone Dart CLI in `plugins/setup/buildkit/build_tool/`.

Platform build hooks inside `flutter build` trigger `build_tool` automatically:

- macOS: podspec script phase, `build_pod.sh`, `build_tool macos`.
- Linux: CMake include, `buildkit/cmake/buildkit.cmake`, `build_tool linux`.
- Windows: CMake include, `buildkit/cmake/buildkit.cmake`, `build_tool windows`. Debug passes `--dev` via `CMAKE_BUILD_TYPE`.
- Android: Gradle include, `buildkit/gradle/plugin.gradle`, `build_tool android`.

Windows helper auth:

- Release: Core SHA256 is embedded in both the Flutter app and the Rust helper. The app pings the helper and verifies the token matches.
- Debug: The Rust helper skips token verification when built in debug mode, so `flutter run` works without the SHA256 flow.

`plugins/setup/` is an FFI plugin that exists only as a build harness. It carries no Dart API, only platform build hooks that trigger Go compilation. Windows builds also compile a Rust helper in `services/helper/` through `RustBuilder`.

Build configuration defaults live in `build_tool/lib/src/options.dart` and can be overridden via `build_config.yaml`.

Architecture detection is automatic. The `--description` flag passed to `flutter_distributor` adds arch suffixes to artifact names, such as `FlClash-0.8.93-macos-arm64.dmg`.

## Local Plugins

- `setup`: build harness FFI plugin.
- `proxy`: system proxy configuration.
- `rust_api`: Flutter Rust Bridge FFI plugin.
- `tray_manager`: system tray fork/customization.
- `wifi_ssid`: Wi-Fi SSID detection.
- `window_ext`: window extensions.
- `flutter_distributor`: app packaging/distribution.

## Rust Helper Service

`services/helper/` is a Windows-only privileged helper for starting the core as admin and managing TUN. It is built with:

```bash
cargo build --release --features windows-service
```

It uses token-based auth with the Flutter app.

## Linux CLI / TUI

The Linux terminal client is built from `core/` with `CGO_ENABLED=0 go build -tags cli`. It shares `package main` with the Mihomo hub (`hub.go`, `action.go`, `common.go`) because Backend and TUI call unexported hub functions such as `handleSetupConfig`, `handleValidateConfig`, `handleInitClash`, and `handleStartListener` in-process. Do not move Backend or TUI into another package unless those hub APIs are first exported or extracted; `package main` cannot be imported.

Leaf packages under `core/internal/` are for code that does not call hub:

- `internal/paths`: home/config resolution, runtime directory, file locks. Silent/managed runtime YAML prefixes live here (`SilentRuntimePrefix`, `ManagedRuntimePrefix`).
- `internal/protocol`: Backend JSON DTOs (`TuiConnection`, `TuiRequest`, `TuiServiceStatus`, …). `package main` keeps type aliases.
- `internal/subscription`: subscription conversion, HTTP fetch, and import filenames. `package main` keeps thin wrappers (`normalizeTUISubscription`, `fetchTUISubscriptionDetails`, …) so TUI/Backend call sites stay unchanged.
- `internal/hubapi`: Core listener/config interface. CLI code goes through `cliHub` (`cli_hub_adapter.go`), which still calls unexported `handle*` in `hub.go`. Do not move `hub.go`; Android CGO and the desktop server keep calling it directly.

New CLI code goes in a `core/cli_*.go` file named for the domain, not into a kitchen-sink file. Current file map:

Entry and commands:

- `cli_main.go`: `main`, usage, `run`/`check`, path resolution
- `cli_dispatch.go`: `flclash` / `flc` command switch
- `cli_management.go`: Dashboard-equivalent subcommands
- `cli_command.go`: `flc` process wrapper
- `cli_controller.go`: Mihomo HTTP `controllerClient`
- `cli_proxy.go` / `cli_profile.go`: `proxy` and `profile` commands

Backend:

- `cli_service.go`: protocol types and Unix-socket client
- `cli_service_lifecycle.go`: start/find/upgrade Backend process
- `cli_service_server.go`: Backend server loop
- `cli_service_runtime.go` and `cli_service_*.go`: revisioned transactions, silent/FLC, reload, settings
- `cli_silent.go`, `cli_history_state.go`, `cli_tun_*.go`, `cli_singleton.go`

SSH (independent of Mihomo; do not import Backend from SSH):

- `cli_ssh.go`: profile types and management commands
- `cli_ssh_profile.go`: config CRUD and validation
- `cli_ssh_tunnel.go`: OpenSSH control/forward/askpass
- `cli_ssh_attach.go`, `cli_ssh_import.go`, `cli_ssh_relay.go`

TUI:

- `cli_tui.go`: snapshot types, `flclash` TUI entry, core init
- `cli_tui_bubbletea.go`: model, `Init`, `update`
- `cli_tui_view.go`, `cli_tui_keys.go`, `cli_tui_refresh.go`, `cli_tui_settings.go`, `cli_tui_actions.go`, `cli_tui_input.go`
- `cli_tui_ssh_page.go`: SSH page/forms
- `cli_tui_render.go`, `cli_tui_pages.go`, `cli_tui_dashboard.go`, `cli_tui_lists.go`, `cli_tui_profiles_page.go`: drawing
- `cli_tui_snapshot.go`, `cli_tui_sysproxy.go`, `cli_tui_legacy.go`
- TUI tests: `cli_tui_test.go`, `cli_tui_nav_test.go`, `cli_tui_refresh_test.go`, `cli_tui_settings_test.go`, `cli_tui_helpers_test.go`

Target longer-term packages under `core/internal/` (ssh, subscription, update, paths, protocol) are for code that does not call hub. Extract a package only when its tests do not need `tuiModel` or hub functions, and keep the dependency direction `cmd/tui -> backend -> silent/tun/history/subscription`; SSH stays a sibling of Backend.

Keep `//go:build linux && !cgo && cli` on every CLI file so it is not compiled into the desktop Core.
