---
name: ssh-tui-ux
description: Use when changing Linux TUI/CLI SSH Capture copy, empty states, picker labels, SSH dashboard hints, or the Connect vs Capture vs ssh -D vs ssh -R user flow.
---

# SSH TUI/CLI UX

Linux CLI/TUI only (`core/cli_tui_*.go`, `core/cli_ssh*.go`, `CLI_LINUX.md`). Do not change Flutter GUI, shortcuts, flags, or version unless the user asked.

Change the **TUI widgets, list rows, overlay, and key flow**. Do not add help paragraphs, tutorials, or copy-paste SSH commands into the dashboard. If the user cannot tell Capture from Connect, fix layout, status, and what Enter does — not a longer caption.

## What each action does

| User action | What happens | Traffic exits via |
|---|---|---|
| Capture (Enter/`a` on Capture row, `ssh attach`) | Attach a SOCKS **already listening on this FlClash machine**. No new login. | `-D` / ControlMaster: the SSH **server**. `-R`: the SSH **client**. |
| Enter on a saved profile / `ssh connect` | Start or reuse a tunnel **to that saved host**. | That host. |
| Idle refresh / `ssh list` | Must **not** scan `/proc` or Windows `ssh.exe`. | — |

`ssh -D` SOCKS lives on the **client**. Capture it **on the client**.
`ssh -R` / `RemoteForward <port>` SOCKS lives on the **server**. Capture it **on the server**.
A session on another machine, or plain `ssh` without `-D`/`-R`/ControlMaster, will not appear. Do not open with that limitation; put the next step first.

## Display rules

1. Say **this machine** when Capture scans. Never imply it can see the other host's `ssh`.
2. Every candidate line must say **where traffic exits**, then the local SOCKS port.
   - Local `-D` / ControlMaster: `exit via <ssh-host> · SOCKS 127.0.0.1:<port>`
   - Inbound `-R`: `exit via client · SOCKS ←R 127.0.0.1:<port>`
3. Capture row: one action clause. Example: `Capture live SSH     Enter · SOCKS on THIS machine`.
4. Capture dashboard (row selected): at most three short lines — when it scans, what can appear, what to do if empty.
5. Empty states: **next command first**, reason second. Rank:
   1. Incoming `sshd` but no `-R` SOCKS → `On the SSH client: ssh -R 10808 USER@THISHOST — then Capture again.`
   2. Local VS Code/`ssh.exe` without a reachable `-D`
   3. Saved profiles exist → use the profile list + Enter to connect
   4. Generic: nothing listening here
6. Picker overlay: `Enter attaches · no new login · Esc cancel`. Loading: `Looking for SOCKS on this machine…`.
7. Status while highlighting Capture: attach language, not “connect” or “probe ControlMaster” alone.
8. Keep TUI English. Do not add a new page, shortcut, or flag for this copy.

## When editing

- `core/cli_tui_profiles_page.go` — Capture row + dashboard
- `core/cli_tui_view.go` / `core/cli_tui_keys.go` / `core/cli_tui_ssh_page.go` — overlay, status, checking line
- `core/cli_ssh_discover.go` — `formatCLICaptureCandidate`, `formatCLICaptureEmptyHint`
- `core/cli_ssh.go` — attach/connect one-liners
- Update tests that assert those strings
- `CGO_ENABLED=0 GOOS=linux go test -tags cli -count=1 ./...` from `core/`

## Do not

- Teach users to hijack plain `ssh` or to Capture A’s `-D` from B
- Change `ControlMaster` in the user’s ssh config
- Scan on idle TUI refresh
- Treat mihomo/Clash mixed-port as SSH SOCKS
