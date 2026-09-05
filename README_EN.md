# FlClash TUI

[中文首页](README.md) · [Full documentation](CLI_LINUX.md) · [Releases](https://github.com/yqlay/flclash-tui/releases)

[![Release](https://img.shields.io/github/v/release/yqlay/flclash-tui?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![Downloads](https://img.shields.io/github/downloads/yqlay/flclash-tui/total?style=flat-square)](https://github.com/yqlay/flclash-tui/releases)
[![License](https://img.shields.io/github/license/yqlay/flclash-tui?style=flat-square)](LICENSE)

A Mihomo terminal manager for Linux, SSH, and headless hosts. Run `flclash` for the full-screen TUI. The default mode is **silent**: only `flc COMMAND` uses the proxy.

## Can't run Codex / Claude on headless Linux? One `flc` is enough

On many networks, installing Codex or even running `codex` directly just fails.

That looks like this:

<p align="center">
  <img src="readme-assets/photo-1.png" alt="Codex failing to connect without a proxy" width="920">
</p>

Hand-writing proxy env vars is messy. FlClash TUI's path for headless Linux is direct: import a subscription, pick a node, then start the command with `flc`.

## Who it is for

- Remote Linux, cloud VMs, lab machines, or WSL, with a subscription already in hand;
- Proxying only chosen commands such as Codex, Claude, git, or npm;
- Leaving other services, containers, and SSH sessions on the machine alone;
- Failing closed when the listener, Core, or node is down, instead of silently going direct.

## Step 1: Install before you have a proxy

```bash
curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash
```

Force a portable installation:

```bash
curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash -s -- --method portable
```

If GitHub is unstable, download the matching `.deb` or `.tar.gz` from [Releases](https://github.com/yqlay/flclash-tui/releases), copy it over, then `sudo dpkg -i`. Do not pin a version string; always use the latest Release.

## Step 2: Import a subscription and pick a node

Run `flclash` to open the TUI. In **Profiles**, import a subscription URL or local file, select it, and press Enter to activate. Then in **Proxies** pick a group and node (press `d` to test delay). Dashboard should stay on `silent` so other programs are left alone. Focus **Core** and press Enter to start it. Other modes also need **System proxy**.

<p align="center">
  <img src="readme-assets/photo-3.png" alt="Start Core in silent mode" width="920">
</p>

<p align="center">
  <img src="readme-assets/photo-2.png" alt="Dashboard in silent mode with Core running" width="920">
</p>

Headless hosts default to `silent`: system proxy and TUN stay off, and only commands you prefix with `flc` use the proxy.

```bash
flc example_command
```

If the command needs sudo:

```bash
flc sudo -E example_command
```

`-E` keeps the proxy environment variables under sudo.

Silent mode uses an authenticated private loopback listener. You do not need to expose a port on the LAN or the public internet. If the listener, Core, or node is down, `flc` errors and refuses to run instead of sending the command out direct.

## Step 3: Run Codex or Claude

```bash
flc codex
flc claude
```

After `flc codex` connects:

<p align="center">
  <img src="readme-assets/photo-4.png" alt="flc codex connected successfully" width="920">
</p>

The Dashboard traffic graph updates, and **Connections** shows the proxied sessions:

<p align="center">
  <img src="readme-assets/photo-5.png" alt="Connections page showing proxied sessions" width="920">
</p>

`flc COMMAND` sets uppercase and lowercase `HTTP_PROXY`, `HTTPS_PROXY`, and `ALL_PROXY` for that command only (and for children of `flc bash`). The same wrapper works for ordinary tools:

```bash
flc git clone https://github.com/owner/repository.git
flc npm install
flc curl https://example.com
flc sh -c 'git fetch --all && npm ci'
```

To run several commands in a row without repeating `flc`, open a proxied shell:

```bash
flc bash

# child commands inside this shell inherit the proxy
codex
claude
git push
```

Leave the shell and the environment variables disappear. Other shells, system services, Docker containers, and existing connections for this user are untouched.

## When you are done

```bash
flclash exit    # stop this user's frontends, Backend, Core, and SSH
```

Or open the TUI with `flclash` and press `Ctrl+C`.

No proxied shell environment is left behind. Next time, start `flclash` again, then run `flc codex` or `flc claude`.

Press `q` in the TUI, or close that terminal, to leave only the current frontend. Backend stays up, so `flc` still works.

## Desktop / system-wide proxy

To send ordinary apps such as a browser through the proxy, switch Dashboard mode to `rule` or `global`, then enable **System proxy** or TUN. On headless hosts, prefer `flc` above.

## SSH proxy

Run FlClash only on the machine whose traffic needs proxying, and use another already-online host as the SSH peer. `flc ssh COMMAND` then leaves through that host's network. The local machine does not open a public proxy port; the peer only needs SSH. If that host already has an OpenSSH ControlMaster, `connect` / `attach` reuse it for the SOCKS reverse proxy instead of opening a second login. A plain interactive `ssh` without ControlMaster cannot be captured.

```bash
flclash ssh import                 # import Host entries from ~/.ssh/config
flclash ssh add home host --user user --password --local-port 1080
flclash ssh default home
flclash ssh attach                 # capture a live ControlMaster; never start a new login
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
