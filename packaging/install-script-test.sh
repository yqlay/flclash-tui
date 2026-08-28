#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

# shellcheck source=../install.sh
source "${ROOT_DIR}/install.sh"

assert_equal() {
  local expected=$1
  local actual=$2
  local description=$3
  if [[ "$actual" != "$expected" ]]; then
    printf 'FAIL: %s: expected %q, got %q\n' "$description" "$expected" "$actual" >&2
    exit 1
  fi
}

assert_equal 'amd64' "$(normalize_arch x86_64)" 'x86_64 architecture'
assert_equal 'amd64' "$(normalize_arch amd64)" 'amd64 architecture'
assert_equal 'arm64' "$(normalize_arch aarch64)" 'aarch64 architecture'
assert_equal 'arm64' "$(normalize_arch arm64)" 'arm64 architecture'
if normalize_arch riscv64 >/dev/null 2>&1; then
  printf 'FAIL: unsupported architecture was accepted\n' >&2
  exit 1
fi

test_dir=$(mktemp -d "${TMPDIR:-/tmp}/flclash-install-test.XXXXXX")
trap 'rm -rf -- "$test_dir"' EXIT
fixture="${test_dir}/release.json"
cat > "$fixture" <<'EOF'
{
  "tag_name": "v1.2.3",
  "assets": [
    {
      "name": "flclash-tui_1.2.3_amd64.deb",
      "digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
    },
    {
      "name": "flclash-tui_1.2.3_arm64.tar.gz",
      "digest": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
    }
  ]
}
EOF

assert_equal 'v1.2.3' "$(parse_release_tag "$fixture")" 'release tag parsing'
assert_equal \
  'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa' \
  "$(parse_release_asset_digest "$fixture" 'flclash-tui_1.2.3_amd64.deb')" \
  'Debian asset digest parsing'
assert_equal \
  'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb' \
  "$(parse_release_asset_digest "$fixture" 'flclash-tui_1.2.3_arm64.tar.gz')" \
  'portable asset digest parsing'
assert_equal '' "$(parse_release_asset_digest "$fixture" 'missing.deb')" 'missing asset digest'

validate_version '0.5.4'
validate_version '1.2.3-rc.1'
if validate_version '../bad'; then
  printf 'FAIL: invalid version was accepted\n' >&2
  exit 1
fi

version='1.2.3'
arch='amd64'
package_name="flclash-tui_${version}_${arch}"
asset_name="${package_name}.tar.gz"
stage_dir="${test_dir}/stage/${package_name}"
api_dir="${test_dir}/api/releases/tags"
download_dir="${test_dir}/downloads/v${version}"
install_dir="${test_dir}/installed"
bin_dir="${test_dir}/bin"
mkdir -p "$stage_dir/data" "$api_dir" "$download_dir"
printf '#!/usr/bin/env sh\nprintf "fixture flclash\\n"\n' > "${stage_dir}/flclash"
chmod 0755 "${stage_dir}/flclash"
ln -s flclash "${stage_dir}/flc"
printf 'fixture geo data\n' > "${stage_dir}/data/GEOIP.dat"
tar -C "${test_dir}/stage" -czf "${download_dir}/${asset_name}" "$package_name"
asset_digest=$(sha256sum "${download_dir}/${asset_name}" | awk '{print $1}')
cat > "${api_dir}/v${version}" <<EOF
{
  "tag_name": "v${version}",
  "assets": [
    {
      "name": "${asset_name}",
      "digest": "sha256:${asset_digest}"
    }
  ]
}
EOF

FLCLASH_INSTALL_API_ROOT="file://${test_dir}/api" \
FLCLASH_INSTALL_DOWNLOAD_ROOT="file://${test_dir}/downloads" \
bash "${ROOT_DIR}/install.sh" \
  --version "$version" \
  --method portable \
  --arch "$arch" \
  --install-dir "$install_dir" \
  --bin-dir "$bin_dir" >/dev/null

# Reinstalling the same version must safely refresh the portable files and links.
FLCLASH_INSTALL_API_ROOT="file://${test_dir}/api" \
FLCLASH_INSTALL_DOWNLOAD_ROOT="file://${test_dir}/downloads" \
bash "${ROOT_DIR}/install.sh" \
  --version "$version" \
  --method portable \
  --arch "$arch" \
  --install-dir "$install_dir" \
  --bin-dir "$bin_dir" >/dev/null

[[ -x "${install_dir}/${version}-${arch}/flclash" ]] || {
  printf 'FAIL: portable executable was not installed\n' >&2
  exit 1
}
[[ -L "${bin_dir}/flclash" && -L "${bin_dir}/flc" ]] || {
  printf 'FAIL: portable command links were not installed\n' >&2
  exit 1
}
assert_equal 'fixture flclash' "$("${bin_dir}/flclash")" 'installed portable executable'

printf 'tampered\n' >> "${download_dir}/${asset_name}"
if FLCLASH_INSTALL_API_ROOT="file://${test_dir}/api" \
  FLCLASH_INSTALL_DOWNLOAD_ROOT="file://${test_dir}/downloads" \
  bash "${ROOT_DIR}/install.sh" \
    --version "$version" \
    --method portable \
    --arch "$arch" \
    --install-dir "$install_dir" \
    --bin-dir "$bin_dir" >/dev/null 2>&1; then
  printf 'FAIL: tampered portable asset was accepted\n' >&2
  exit 1
fi

printf 'install script tests passed\n'
