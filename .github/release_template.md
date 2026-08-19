
## Linux TUI packages

Choose the package matching `dpkg --print-architecture`:

| Architecture | Debian / Ubuntu | Portable archive |
| --- | --- | --- |
| AMD64 | [`flclash-tui_VERSION_amd64.deb`](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_amd64.deb) | [`flclash-tui_VERSION_amd64.tar.gz`](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_amd64.tar.gz) |
| ARM64 | [`flclash-tui_VERSION_arm64.deb`](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_arm64.deb) | [`flclash-tui_VERSION_arm64.tar.gz`](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_arm64.tar.gz) |

Each package has an adjacent `.sha256` file. `SHA256SUMS` covers every uploaded artifact.

The Debian package installs `flclash`, the `flc` command wrapper, bundled Geo data,
and the restricted system TUN helper service. The portable archive does not install
the system service and is intended for user-scoped operation.

See the [English documentation](https://github.com/yqlay/flclash-tui/blob/main/README.md)
or [中文文档](https://github.com/yqlay/flclash-tui/blob/main/README_zh_CN.md).
