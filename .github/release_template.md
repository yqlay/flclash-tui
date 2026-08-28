
## Linux TUI packages

Recommended installation (automatically selects AMD64 or ARM64):

```bash
curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash
```

The installer verifies the GitHub-provided SHA-256 asset digest before installing.
Debian and Ubuntu receive the native package with the TUN helper; other Linux
distributions receive the user-scoped portable build.

Manual downloads:

| Architecture | Debian / Ubuntu | Portable archive |
| --- | --- | --- |
| AMD64 | [`flclash-tui_VERSION_amd64.deb`](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_amd64.deb) | [`flclash-tui_VERSION_amd64.tar.gz`](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_amd64.tar.gz) |
| ARM64 | [`flclash-tui_VERSION_arm64.deb`](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_arm64.deb) | [`flclash-tui_VERSION_arm64.tar.gz`](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_arm64.tar.gz) |

`SHA256SUMS` contains checksums for all architecture-specific packages. GitHub also
displays its own SHA-256 digest for every uploaded asset.

The Debian package installs `flclash`, the `flc` command wrapper, bundled Geo data,
and the restricted system TUN helper service. The portable archive does not install
the system service and is intended for user-scoped operation.

See the [中文文档](https://github.com/yqlay/flclash-tui/blob/main/README.md)
or [English documentation](https://github.com/yqlay/flclash-tui/blob/main/README_EN.md).
