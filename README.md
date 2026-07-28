<div>

[**简体中文**](README_zh_CN.md)

</div>

# FlClash CLI for Linux

An unofficial Linux command-line client derived from [FlClash](https://github.com/chen08209/FlClash). It reuses FlClash's Go/Mihomo core and adds a foreground CLI entry point, configuration validation, reload support, and proxy-group control through the Mihomo External Controller.

This is an independent derivative project, not an official FlClash release. See [NOTICE](NOTICE) and [LICENSE](LICENSE) for copyright and license information.

## Quick start

```bash
git clone --recurse-submodules https://github.com/yqlay/flclash-cli.git
cd flclash-cli
make cli-linux
./dist/flclash-cli run --config ~/.config/flclash/config.yaml
```

See [CLI_LINUX.md](CLI_LINUX.md) for configuration, proxy control, and limitations.

Prebuilt Debian packages are available on the [Releases](https://github.com/yqlay/flclash-cli/releases) page:

```bash
sudo dpkg -i flclash-cli_0.1.0_amd64.deb
```

## Original FlClash application

The original Flutter graphical application remains in this repository because the CLI reuses its core integration and platform code. For the upstream GUI project and releases, see [chen08209/FlClash](https://github.com/chen08209/FlClash).

## FlClash

[![Downloads](https://img.shields.io/github/downloads/chen08209/FlClash/total?style=flat-square&logo=github)](https://github.com/chen08209/FlClash/releases/)[![Last Version](https://img.shields.io/github/release/chen08209/FlClash/all.svg?style=flat-square)](https://github.com/chen08209/FlClash/releases/)[![License](https://img.shields.io/github/license/chen08209/FlClash?style=flat-square)](LICENSE)

[![Channel](https://img.shields.io/badge/Telegram-Channel-blue?style=flat-square&logo=telegram)](https://t.me/FlClash)

A multi-platform proxy client based on ClashMeta, simple and easy to use, open-source and ad-free.

on Desktop:
<p style="text-align: center;">
    <img alt="desktop" src="snapshots/desktop.gif">
</p>

on Mobile:
<p style="text-align: center;">
    <img alt="mobile" src="snapshots/mobile.gif">
</p>

## Features

✈️ Multi-platform: Android, Windows, macOS and Linux

💻 Adaptive multiple screen sizes, Multiple color themes available

💡 Based on Material You Design, [Surfboard](https://github.com/getsurfboard/surfboard)-like UI

☁️ Supports data sync via WebDAV

✨ Support subscription link, Dark mode

## Use

### Linux

⚠️ Make sure to install the following dependencies before using them

   ```bash
    sudo apt-get install libayatana-appindicator3-dev
    sudo apt-get install libkeybinder-3.0-dev
   ```

For terminal-only use without the Flutter GUI, build the Linux CLI:

```bash
make cli-linux
./dist/flclash-cli run --config ~/.config/flclash/config.yaml
```

The CLI reuses FlClash's Go/Mihomo core and supports configuration validation, foreground execution, SIGHUP reload, and proxy-group control through the External Controller. See [Linux CLI documentation](CLI_LINUX.md).

### Android

Support the following actions

   ```bash
    com.follow.clash.action.START
    
    com.follow.clash.action.STOP
    
    com.follow.clash.action.TOGGLE
   ```

## Download

<a href="https://chen08209.github.io/FlClash-fdroid-repo/repo?fingerprint=789D6D32668712EF7672F9E58DEEB15FBD6DCEEC5AE7A4371EA72F2AAE8A12FD"><img alt="Get it on F-Droid" src="snapshots/get-it-on-fdroid.svg" width="200px"/></a> <a href="https://github.com/chen08209/FlClash/releases"><img alt="Get it on GitHub" src="snapshots/get-it-on-github.svg" width="200px"/></a>

### Homebrew

```bash
brew tap chen08209/tap
brew install --cask flclash
```

## Build

1. Update submodules
   ```bash
   git submodule update --init --recursive
   ```

2. Install `Flutter` and `Golang` environment

3. Build Application

    - android

        1. Install `Android SDK`, `Android NDK`

        2. Set `ANDROID_NDK` environment variable

        3. Run build script

           ```bash
           dart setup.dart android
           ```

    - windows

        1. Requires a Windows client

        2. Install `GCC`, `Inno Setup`

        3. Run build script

           ```bash
           dart setup.dart windows
           ```

    - linux

        1. Requires a Linux client

        2. Dependencies are auto-installed by setup script, or manually:
           ```bash
           sudo apt-get install -y libayatana-appindicator3-dev libkeybinder-3.0-dev
           ```

        3. Run build script

           ```bash
           dart setup.dart linux
           ```

    - macOS

        1. Requires a macOS client

        2. Run build script

           ```bash
           dart setup.dart macos
           ```

## Star

The easiest way to support developers is to click on the star (⭐) at the top of the page.

<p style="text-align: center;">
    <a href="https://api.star-history.com/svg?repos=chen08209/FlClash&Date">
        <img alt="start" width=50% src="https://api.star-history.com/svg?repos=chen08209/FlClash&Date"/>
    </a>
</p>
