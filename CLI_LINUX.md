# FlClash CLI for Linux

This repository includes a Linux command-line entry point that uses the same
Go/Mihomo core as the FlClash desktop application. The Flutter UI and desktop
IPC transport are not involved in CLI mode.

## Build

Initialize the Mihomo submodule and build the binary:

```bash
git submodule update --init --recursive
make cli-linux
```

The binary is written to `dist/flclash-cli`.

## Run a proxy

Create a Clash/Mihomo configuration at `~/.config/flclash/config.yaml`, then:

```bash
./dist/flclash-cli run
```

You can use another configuration directory or file:

```bash
./dist/flclash-cli run --directory ~/.config/flclash-work
./dist/flclash-cli run --config /path/to/config.yaml
```

The process stays in the foreground. `Ctrl-C` stops listeners and shuts down
the Mihomo executor. `SIGHUP` reloads the configuration.

## Validate configuration

```bash
./dist/flclash-cli check --config /path/to/config.yaml
```

## Control a running instance

If the configuration enables Mihomo's external controller, the CLI can inspect
proxy groups and select a proxy:

```bash
./dist/flclash-cli proxy list --controller 127.0.0.1:9090
./dist/flclash-cli proxy select --controller 127.0.0.1:9090 PROXY Tokyo
```

If the controller has a secret, pass `--secret` or keep the controller bound
to localhost.

## Limitations

The first Linux CLI version intentionally keeps system-proxy changes outside
the core process. Use the `mixed-port` or `socks-port` from the configuration
in the application or shell that should use the proxy. TUN mode remains
available through the Mihomo configuration and may require elevated Linux
network permissions.
