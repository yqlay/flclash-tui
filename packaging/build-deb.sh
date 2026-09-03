#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-$(sed -n 's/^const cliVersion = "\(.*\)"/\1/p' "$ROOT_DIR/core/cli_main.go")}"
ARCH="${ARCH:-amd64}"
OUTPUT_DIR="${OUTPUT_DIR:-${ROOT_DIR}/dist}"
PACKAGE_NAME="flclash-tui_${VERSION}_${ARCH}"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/flclash-tui-deb.XXXXXX")"

cleanup() {
  rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

case "${ARCH}" in
  amd64|arm64)
    ;;
  *)
    echo "Unsupported Debian architecture: ${ARCH} (expected amd64 or arm64)." >&2
    exit 2
    ;;
esac

mkdir -p "${OUTPUT_DIR}"
mkdir -p "${WORK_DIR}/DEBIAN"
mkdir -p "${WORK_DIR}/usr/bin"
mkdir -p "${WORK_DIR}/usr/share/doc/flclash-tui"
mkdir -p "${WORK_DIR}/usr/share/flclash-tui/data"
mkdir -p "${WORK_DIR}/usr/lib/systemd/system"
mkdir -p "${WORK_DIR}/usr/share/polkit-1/actions"
chmod 0755 "${WORK_DIR}" "${WORK_DIR}/DEBIAN" "${WORK_DIR}/usr" \
  "${WORK_DIR}/usr/share" "${WORK_DIR}/usr/share/doc" \
  "${WORK_DIR}/usr/share/doc/flclash-tui" \
  "${WORK_DIR}/usr/share/flclash-tui" \
  "${WORK_DIR}/usr/share/flclash-tui/data"

(
  cd "${ROOT_DIR}/core"
  CGO_ENABLED=0 GOOS=linux GOARCH="${ARCH}" go build \
    -tags cli \
    -trimpath \
    -ldflags "-s -w" \
    -o "${WORK_DIR}/usr/bin/flclash" \
    .
)

chmod 0755 "${WORK_DIR}/usr/bin/flclash"
ln -s flclash "${WORK_DIR}/usr/bin/flc"
install -m 0644 "${ROOT_DIR}/README.md" "${WORK_DIR}/usr/share/doc/flclash-tui/README.md"
install -m 0644 "${ROOT_DIR}/README_EN.md" "${WORK_DIR}/usr/share/doc/flclash-tui/README_EN.md"
install -m 0644 "${ROOT_DIR}/CLI_LINUX.md" "${WORK_DIR}/usr/share/doc/flclash-tui/CLI_LINUX.md"
install -m 0644 "${ROOT_DIR}/LICENSE" "${WORK_DIR}/usr/share/doc/flclash-tui/LICENSE"
install -m 0644 "${ROOT_DIR}/NOTICE" "${WORK_DIR}/usr/share/doc/flclash-tui/NOTICE"
install -m 0644 "${ROOT_DIR}/packaging/flclash-tun-helper.service" \
  "${WORK_DIR}/usr/lib/systemd/system/flclash-tun-helper.service"
install -m 0644 "${ROOT_DIR}/packaging/org.flclash.tun.policy" \
  "${WORK_DIR}/usr/share/polkit-1/actions/org.flclash.tun.policy"
for geo_file in GEOIP.metadb GEOIP.dat GEOSITE.dat ASN.mmdb; do
  install -m 0644 \
    "${ROOT_DIR}/assets/data/${geo_file}" \
    "${WORK_DIR}/usr/share/flclash-tui/data/${geo_file}"
done

cat > "${WORK_DIR}/DEBIAN/control" <<EOF
Package: flclash-tui
Version: ${VERSION}-1
Section: net
Priority: optional
Architecture: ${ARCH}
Maintainer: yqlay <yqlay@users.noreply.github.com>
Conflicts: flclash-cli
Replaces: flclash-cli
Provides: flclash-cli
Depends: iproute2, iptables, polkitd | policykit-1
Recommends: openssh-client
Description: Linux terminal client derived from FlClash
 Unofficial Linux terminal interface and command-line client that reuses the FlClash Mihomo core.
 Supports interactive proxy management, config profiles, settings, and scriptable control.
EOF

cat > "${WORK_DIR}/DEBIAN/postinst" <<'EOF'
#!/bin/sh
set -e
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
  systemctl enable --now flclash-tun-helper.service || true
fi
EOF
chmod 0755 "${WORK_DIR}/DEBIAN/postinst"

cat > "${WORK_DIR}/DEBIAN/prerm" <<'EOF'
#!/bin/sh
set -e
if [ "$1" = remove ] && command -v systemctl >/dev/null 2>&1; then
  systemctl disable --now flclash-tun-helper.service || true
fi
EOF
chmod 0755 "${WORK_DIR}/DEBIAN/prerm"

cat > "${WORK_DIR}/DEBIAN/postrm" <<'EOF'
#!/bin/sh
set -e
if command -v systemctl >/dev/null 2>&1; then
  systemctl daemon-reload || true
fi
EOF
chmod 0755 "${WORK_DIR}/DEBIAN/postrm"

dpkg-deb --build --root-owner-group "${WORK_DIR}" "${OUTPUT_DIR}/${PACKAGE_NAME}.deb" >/dev/null
echo "Built ${OUTPUT_DIR}/${PACKAGE_NAME}.deb"

TARBALL_STAGE="$(mktemp -d "${TMPDIR:-/tmp}/flclash-tui-tar.XXXXXX")"
trap 'rm -rf "${TARBALL_STAGE}"; cleanup' EXIT
mkdir -p "${TARBALL_STAGE}/${PACKAGE_NAME}"
cp -a "${WORK_DIR}/usr/bin/flclash" "${TARBALL_STAGE}/${PACKAGE_NAME}/"
cp -a "${WORK_DIR}/usr/bin/flc" "${TARBALL_STAGE}/${PACKAGE_NAME}/"
cp -a "${WORK_DIR}/usr/share/doc/flclash-tui/." "${TARBALL_STAGE}/${PACKAGE_NAME}/"
cp -a "${WORK_DIR}/usr/share/flclash-tui/data" "${TARBALL_STAGE}/${PACKAGE_NAME}/"
tar -C "${TARBALL_STAGE}" -czf "${OUTPUT_DIR}/${PACKAGE_NAME}.tar.gz" "${PACKAGE_NAME}"
echo "Built ${OUTPUT_DIR}/${PACKAGE_NAME}.tar.gz"
