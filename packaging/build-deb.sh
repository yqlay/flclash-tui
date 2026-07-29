#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-0.3.4}"
ARCH="${ARCH:-amd64}"
OUTPUT_DIR="${OUTPUT_DIR:-${ROOT_DIR}/dist}"
PACKAGE_NAME="flclash-cli_${VERSION}_${ARCH}"
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/flclash-cli-deb.XXXXXX")"

cleanup() {
  rm -rf "${WORK_DIR}"
}
trap cleanup EXIT

if [[ "${ARCH}" != "amd64" ]]; then
  echo "Only amd64 Debian packaging is supported by this script." >&2
  exit 2
fi

mkdir -p "${OUTPUT_DIR}"
mkdir -p "${WORK_DIR}/DEBIAN"
mkdir -p "${WORK_DIR}/usr/bin"
mkdir -p "${WORK_DIR}/usr/share/doc/flclash-cli"
chmod 0755 "${WORK_DIR}" "${WORK_DIR}/DEBIAN" "${WORK_DIR}/usr" \
  "${WORK_DIR}/usr/share" "${WORK_DIR}/usr/share/doc" \
  "${WORK_DIR}/usr/share/doc/flclash-cli"

(
  cd "${ROOT_DIR}/core"
  CGO_ENABLED=0 GOOS=linux GOARCH="${ARCH}" go build \
    -tags cli \
    -trimpath \
    -ldflags "-s -w" \
    -o "${WORK_DIR}/usr/bin/flclash-cli" \
    .
)

chmod 0755 "${WORK_DIR}/usr/bin/flclash-cli"
install -m 0644 "${ROOT_DIR}/README.md" "${WORK_DIR}/usr/share/doc/flclash-cli/README.md"
install -m 0644 "${ROOT_DIR}/CLI_LINUX.md" "${WORK_DIR}/usr/share/doc/flclash-cli/CLI_LINUX.md"
install -m 0644 "${ROOT_DIR}/LICENSE" "${WORK_DIR}/usr/share/doc/flclash-cli/LICENSE"
install -m 0644 "${ROOT_DIR}/NOTICE" "${WORK_DIR}/usr/share/doc/flclash-cli/NOTICE"

cat > "${WORK_DIR}/DEBIAN/control" <<EOF
Package: flclash-cli
Version: ${VERSION}-1
Section: net
Priority: optional
Architecture: ${ARCH}
Maintainer: yqlay <yqlay@users.noreply.github.com>
Description: Linux TUI and CLI client derived from FlClash
 Unofficial Linux terminal interface and command-line client that reuses the FlClash Mihomo core.
 Supports interactive proxy management, config profiles, settings, and scriptable control.
EOF

dpkg-deb --build --root-owner-group "${WORK_DIR}" "${OUTPUT_DIR}/${PACKAGE_NAME}.deb" >/dev/null
echo "Built ${OUTPUT_DIR}/${PACKAGE_NAME}.deb"

TARBALL_STAGE="$(mktemp -d "${TMPDIR:-/tmp}/flclash-cli-tar.XXXXXX")"
trap 'rm -rf "${TARBALL_STAGE}"; cleanup' EXIT
mkdir -p "${TARBALL_STAGE}/${PACKAGE_NAME}"
cp -a "${WORK_DIR}/usr/bin/flclash-cli" "${TARBALL_STAGE}/${PACKAGE_NAME}/"
cp -a "${WORK_DIR}/usr/share/doc/flclash-cli/." "${TARBALL_STAGE}/${PACKAGE_NAME}/"
tar -C "${TARBALL_STAGE}" -czf "${OUTPUT_DIR}/${PACKAGE_NAME}.tar.gz" "${PACKAGE_NAME}"
echo "Built ${OUTPUT_DIR}/${PACKAGE_NAME}.tar.gz"

sha256sum "${OUTPUT_DIR}/${PACKAGE_NAME}.deb" \
  | awk -v file="${PACKAGE_NAME}.deb" '{print $1 "  " file}' > "${OUTPUT_DIR}/${PACKAGE_NAME}.deb.sha256"
sha256sum "${OUTPUT_DIR}/${PACKAGE_NAME}.tar.gz" \
  | awk -v file="${PACKAGE_NAME}.tar.gz" '{print $1 "  " file}' > "${OUTPUT_DIR}/${PACKAGE_NAME}.tar.gz.sha256"
