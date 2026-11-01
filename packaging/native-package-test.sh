#!/usr/bin/env bash
set -euo pipefail
distro=${1:?distro required}
packages=$(realpath -- "${2:?package directory required}")
version=${3:?version required}
case "$distro" in
  debian) image=debian:bookworm-slim ;;
  fedora) image=fedora:latest ;;
  rocky) image=rockylinux:9 ;;
  opensuse) image=opensuse/tumbleweed:latest ;;
  arch) image=archlinux:base ;;
  alpine) image=alpine:latest ;;
  openwrt) image=ghcr.io/openwrt/rootfs:x86_64-openwrt-24.10 ;;
  *) printf 'Unknown test distribution: %s\n' "$distro" >&2; exit 2 ;;
esac
docker run --rm --entrypoint /bin/sh \
  --mount "type=bind,source=$packages,target=/packages,readonly" \
  --env "DISTRO=$distro" --env "VERSION=$version" "$image" -euxc '
  stem="/packages/flclash-tui_${VERSION}_amd64"
  case "$DISTRO" in
    debian)
      apt-get update
      DEBIAN_FRONTEND=noninteractive apt-get install --yes "$stem.deb"
      ;;
    fedora|rocky) dnf --assumeyes install "$stem.rpm" ;;
    opensuse) zypper --non-interactive install --allow-unsigned-rpm "$stem.rpm" ;;
    arch)
      pacman --noconfirm -Syu iproute2 iptables polkit
      pacman --noconfirm -U "$stem.pkg.tar.zst"
      ;;
    alpine) apk add --no-cache --allow-untrusted "$stem.apk" ;;
    openwrt) opkg install "$stem.ipk" ;;
  esac
  /usr/bin/flclash version | grep -F "FlClash TUI $VERSION "
  /usr/bin/flc version | grep -F "flc $VERSION "
  test -L /usr/bin/flc
  if [ "$DISTRO" = alpine ]; then
    test -x /etc/init.d/flclash-tun-helper
  elif [ "$DISTRO" != openwrt ]; then
    test -f /usr/lib/systemd/system/flclash-tun-helper.service
  fi
  test -f /usr/share/flclash-tui/data/GEOIP.dat
  case "$DISTRO" in
    debian) dpkg --remove flclash-tui ;;
    fedora|rocky) rpm --erase flclash-tui ;;
    opensuse) zypper --non-interactive remove flclash-tui ;;
    arch) pacman --noconfirm -R flclash-tui ;;
    alpine) apk del flclash-tui ;;
    openwrt) opkg remove flclash-tui ;;
  esac
  test ! -e /usr/bin/flclash
  test ! -e /usr/bin/flc
'
