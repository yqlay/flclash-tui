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

[[ -x "${bin_dir}/flclash" ]] || {
  printf 'FAIL: portable executable was not installed\n' >&2
  exit 1
}
[[ -L "${bin_dir}/flclash" && -L "${bin_dir}/flc" ]] || {
  printf 'FAIL: portable command links were not installed\n' >&2
  exit 1
}
assert_equal 'fixture flclash' "$("${bin_dir}/flclash")" 'installed portable executable'

# Relative directory arguments must produce absolute, usable command targets.
(
  cd "$test_dir"
  mkdir relative-work
  install_portable "${download_dir}/${asset_name}" "$version" "$arch" relative-work relative-store relative-bin >/dev/null
  [[ $(readlink relative-bin/flclash) == /* ]] || {
    printf 'FAIL: portable link target is not absolute\n' >&2
    exit 1
  }
  assert_equal 'fixture flclash' "$(relative-bin/flclash)" 'relative portable directories'
)

# Reinstall a real executing ELF: writing through its old inode causes ETXTBSY.
elf_dir="${test_dir}/elf/${package_name}"
mkdir -p "$elf_dir/data" "${test_dir}/elf-first" "${test_dir}/elf-second"
cp "$(command -v bash)" "${elf_dir}/flclash"
ln -s flclash "${elf_dir}/flc"
tar -C "${test_dir}/elf" -czf "${test_dir}/elf.tar.gz" "$package_name"
install_portable "${test_dir}/elf.tar.gz" "$version" "$arch" "${test_dir}/elf-first" "${test_dir}/elf-installed" "${test_dir}/elf-bin" >/dev/null
old_elf=$(readlink "${test_dir}/elf-bin/flclash")
"${test_dir}/elf-bin/flclash" -c 'trap '\''kill "$child" 2>/dev/null || true; wait "$child" 2>/dev/null || true; exit 0'\'' TERM; sleep 60 & child=$!; wait "$child"; :' &
fixture_pid=$!
trap 'kill "$fixture_pid" 2>/dev/null || true; wait "$fixture_pid" 2>/dev/null || true; rm -rf -- "$test_dir"' EXIT
sleep 0.05
install_portable "${test_dir}/elf.tar.gz" "$version" "$arch" "${test_dir}/elf-second" "${test_dir}/elf-installed" "${test_dir}/elf-bin" >/dev/null
[[ $(readlink "${test_dir}/elf-bin/flclash") != "$old_elf" ]] || {
  printf 'FAIL: same-version reinstall reused the executing inode\n' >&2
  exit 1
}
kill -0 "$fixture_pid"
[[ -x "$old_elf" ]] || { printf 'FAIL: old executing instance was removed\n' >&2; exit 1; }
kill "$fixture_pid"
wait "$fixture_pid" 2>/dev/null || true
trap 'rm -rf -- "$test_dir"' EXIT

# A failed install must not switch either old command link.
old_flclash=$(readlink "${bin_dir}/flclash")
old_flc=$(readlink "${bin_dir}/flc")
mkdir -p "${test_dir}/incomplete/${package_name}/data" "${test_dir}/bad-work"
cp "${stage_dir}/flclash" "${test_dir}/incomplete/${package_name}/flclash"
tar -C "${test_dir}/incomplete" -czf "${test_dir}/incomplete.tar.gz" "$package_name"
if (install_portable "${test_dir}/incomplete.tar.gz" "$version" "$arch" "${test_dir}/bad-work" "$install_dir" "$bin_dir") >/dev/null 2>&1; then
  printf 'FAIL: incomplete portable archive was accepted\n' >&2
  exit 1
fi
assert_equal "$old_flclash" "$(readlink "${bin_dir}/flclash")" 'failed install preserves flclash'
assert_equal "$old_flc" "$(readlink "${bin_dir}/flc")" 'failed install preserves flc'

# Inject a failure after the first link has switched: rollback both commands.
mkdir "${test_dir}/switch-failure-work"
if (
  mv() {
    if [[ ${3-} == */.flclash-links.*/flc && ${4-} == "${bin_dir}/flc" ]]; then
      return 1
    fi
    command mv "$@"
  }
  install_portable "${download_dir}/${asset_name}" "$version" "$arch" "${test_dir}/switch-failure-work" "$install_dir" "$bin_dir"
) >/dev/null 2>&1; then
  printf 'FAIL: interrupted command-link switch succeeded\n' >&2
  exit 1
fi
assert_equal "$old_flclash" "$(readlink "${bin_dir}/flclash")" 'link-switch failure restores flclash'
assert_equal "$old_flc" "$(readlink "${bin_dir}/flc")" 'link-switch failure restores flc'

# If rollback itself cannot remove a new link, retain its complete instance.
failed_bin="${test_dir}/rollback-failure-bin"
mkdir "${test_dir}/rollback-failure-work"
if (
  mv() {
    if [[ ${3-} == */.flclash-links.*/flc && ${4-} == "${failed_bin}/flc" ]]; then
      return 1
    fi
    command mv "$@"
  }
  rm() {
    if [[ ${3-} == "${failed_bin}/flclash" ]]; then
      return 1
    fi
    command rm "$@"
  }
  install_portable "${download_dir}/${asset_name}" "$version" "$arch" "${test_dir}/rollback-failure-work" "$install_dir" "$failed_bin"
) >/dev/null 2>&1; then
  printf 'FAIL: command switch with failed rollback succeeded\n' >&2
  exit 1
fi
assert_equal 'fixture flclash' "$("${failed_bin}/flclash")" 'failed rollback preserves referenced instance'

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
