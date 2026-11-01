#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
VERSION="${VERSION:-$(sed -n 's/^const cliVersion = "\(.*\)"/\1/p' "$ROOT_DIR/core/cli_main.go")}"
ARCH="${ARCH:-amd64}"
OUTPUT_DIR="${OUTPUT_DIR:-${ROOT_DIR}/dist}"
FORMATS="${FORMATS:-deb rpm archlinux apk ipk portable}"
NFPM="${NFPM:-nfpm}"
case "$ARCH" in
  amd64) IPK_ARCH=x86_64 ;;
  arm64) IPK_ARCH=aarch64_generic ;;
  *) printf 'Unsupported architecture: %s (expected amd64 or arm64)\n' "$ARCH" >&2; exit 2 ;;
esac
[[ "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+([+-][A-Za-z0-9.-]+)?$ ]] || { printf 'Invalid version\n' >&2; exit 2; }
for format in $FORMATS; do
  case "$format" in deb|rpm|archlinux|apk|ipk|portable) ;; *) printf 'Unsupported format: %s\n' "$format" >&2; exit 2 ;; esac
done
command -v "$NFPM" >/dev/null || { printf 'Install nFPM v2.48.0 (see packaging/README.md)\n' >&2; exit 1; }
WORK_DIR="$(mktemp -d "${TMPDIR:-/tmp}/flclash-packages.XXXXXX")"
trap 'rm -rf -- "$WORK_DIR"' EXIT
PACKAGE_ROOT="$WORK_DIR/root"
mkdir -p "$OUTPUT_DIR" "$PACKAGE_ROOT/usr/bin" "$PACKAGE_ROOT/usr/share/doc/flclash-tui" "$PACKAGE_ROOT/usr/share/flclash-tui/data"
OUTPUT_DIR="$(cd "$OUTPUT_DIR" && pwd -P)"
(
  cd "$ROOT_DIR/core"
  CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -tags cli -trimpath -ldflags '-s -w' -o "$PACKAGE_ROOT/usr/bin/flclash" .
)
for doc in README.md README_EN.md CLI_LINUX.md LICENSE NOTICE; do
  install -m 0644 "$ROOT_DIR/$doc" "$PACKAGE_ROOT/usr/share/doc/flclash-tui/$doc"
done
for geo in GEOIP.metadb GEOIP.dat GEOSITE.dat ASN.mmdb; do
  install -m 0644 "$ROOT_DIR/assets/data/$geo" "$PACKAGE_ROOT/usr/share/flclash-tui/data/$geo"
done
export VERSION ARCH IPK_ARCH PACKAGE_ROOT
cd "$ROOT_DIR"
sed "s/@IPK_ARCH@/$IPK_ARCH/g" packaging/nfpm.yaml > "$WORK_DIR/nfpm.yaml"
stem="flclash-tui_${VERSION}_${ARCH}"
for format in $FORMATS; do
  if [[ "$format" == portable ]]; then
    stage="$WORK_DIR/$stem"
    mkdir -p "$stage"
    cp -a "$PACKAGE_ROOT/usr/bin/flclash" "$stage/"
    ln -s flclash "$stage/flc"
    cp -a "$PACKAGE_ROOT/usr/share/doc/flclash-tui/." "$stage/"
    cp -a "$PACKAGE_ROOT/usr/share/flclash-tui/data" "$stage/"
    tar -C "$WORK_DIR" -czf "$OUTPUT_DIR/$stem.tar.gz" "$stem"
  else
    extension="$format"
    [[ "$format" != archlinux ]] || extension=pkg.tar.zst
    "$NFPM" package --config "$WORK_DIR/nfpm.yaml" --packager "$format" --target "$OUTPUT_DIR/$stem.$extension"
  fi
done
