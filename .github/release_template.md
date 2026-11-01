## Linux packages

Recommended installation (selects AMD64/ARM64 and the package manager):

```bash
curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash
```

| Format / systems | AMD64 | ARM64 |
| --- | --- | --- |
| Debian / Ubuntu | [DEB](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_amd64.deb) | [DEB](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_arm64.deb) |
| Fedora / RHEL / Rocky / Alma / openSUSE | [RPM](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_amd64.rpm) | [RPM](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_arm64.rpm) |
| Arch / Manjaro / Arch Linux ARM | [Arch package](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_amd64.pkg.tar.zst) | [Arch package](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_arm64.pkg.tar.zst) |
| Alpine | [APK](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_amd64.apk) | [APK](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_arm64.apk) |
| OpenWrt with opkg | [IPK, x86_64](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_amd64.ipk) | [IPK, aarch64_generic](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_arm64.ipk) |
| Other Linux / user installation | [Portable](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_amd64.tar.gz) | [Portable](https://github.com/yqlay/flclash-tui/releases/download/vVERSION/flclash-tui_VERSION_arm64.tar.gz) |

The installer and updater verify GitHub SHA-256 asset digests. `SHA256SUMS`
covers every architecture-specific package. Native updates use the package
manager that owns the running executable; managed portable updates preserve
their installation directories.

Debian, RPM and Arch include the systemd TUN helper; Alpine includes OpenRC
integration. TUN also requires cgroup v2 and suitable kernel/routing support.
OpenWrt IPK and portable packages do not install a privileged helper. OpenWrt
firmware must accept the IPK architecture; OpenWrt systems using apk should use
the portable archive, not the Alpine APK. Other CPU architectures are not
release targets.

See [中文用法](https://github.com/yqlay/flclash-tui/blob/main/README.md),
[English usage](https://github.com/yqlay/flclash-tui/blob/main/README_EN.md), and
[package notes](https://github.com/yqlay/flclash-tui/blob/main/packaging/README.md).
