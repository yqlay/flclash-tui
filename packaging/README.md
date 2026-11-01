# Linux packages

Build dependencies: Go matching CI and [nFPM](https://nfpm.goreleaser.com/docs/)
v2.48.0. The build does not install packages or restart host services.

```bash
go install github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.48.0
make package-linux                         # AMD64, all formats
ARCH=arm64 make package-linux              # ARM64, all formats
FORMATS='rpm portable' make package-linux  # selected formats
make package-linux-deb                     # compatibility entry: deb + tar.gz
```

`OUTPUT_DIR` changes the output directory; `NFPM` selects the packaging tool.
All formats contain the same static Go executable, `flc` link, documentation,
and bundled Geo data. The release publishes both AMD64 and ARM64 versions:

| Format | Systems | TUN service |
| --- | --- | --- |
| `.deb` | Debian, Ubuntu, derivatives | systemd |
| `.rpm` | Fedora, RHEL/Rocky/Alma, openSUSE | systemd |
| `.pkg.tar.zst` | Arch, Manjaro, Arch Linux ARM | systemd |
| `.apk` | Alpine | OpenRC |
| `.ipk` | OpenWrt with opkg | Not installed |
| `.tar.gz` | Other Linux distributions, manual/user installs | Not installed |

The IPK architectures are `x86_64` and `aarch64_generic`; OpenWrt firmware must
accept that architecture. OpenWrt versions using apk have a different ABI/package
architecture from Alpine: use the portable archive there, not the Alpine APK.
32-bit ARM, MIPS and other CPU architectures are not release targets. TUN still
requires a suitable kernel, cgroup v2, routing tools, and authorization. Container
package checks do not certify privileged TUN on every kernel or init system.
SSH features require the OpenSSH client; Dropbear-only OpenWrt installations
need `openssh-client` installed separately.

The installer selects an available native package manager, or portable fallback.
Alpine/OpenWrt may need `bash`, `curl`, and CA certificates installed before using
the Bash installer; manually installing their native package does not need Bash.

`flclash update` asks the package database which package owns the running binary.
It selects that format and validates its identity, architecture, version and
GitHub SHA-256 digest. Portable installs record their custom directories beside
the executable and update both command links atomically. Older portable installs
need one reinstall with `install.sh --method portable` to create this marker.
An unmanaged copy cannot silently upgrade an unrelated system package. Native
installation reports a conflicting `PATH` entry rather than deleting user files.

CI checks all twelve built packages, then installs, runs and removes the AMD64
packages in Debian, Fedora, Rocky, openSUSE, Arch, Alpine and OpenWrt containers.
`packaging/native-package-test.sh DISTRO PACKAGE_DIR VERSION` runs that check
locally with Docker. Publication waits for those checks to pass.
