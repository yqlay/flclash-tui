#!/usr/bin/env bash
set -euo pipefail

readonly FLCLASH_REPOSITORY="${FLCLASH_INSTALL_REPOSITORY:-yqlay/flclash-tui}"
readonly FLCLASH_API_ROOT="${FLCLASH_INSTALL_API_ROOT:-https://api.github.com/repos/${FLCLASH_REPOSITORY}}"
readonly FLCLASH_DOWNLOAD_ROOT="${FLCLASH_INSTALL_DOWNLOAD_ROOT:-https://github.com/${FLCLASH_REPOSITORY}/releases/download}"
flclash_install_work_dir=''

log() {
  printf 'flclash installer: %s\n' "$*"
}

die() {
  printf 'flclash installer: %s\n' "$*" >&2
  exit 1
}

cleanup_install_work_dir() {
  if [[ -n "$flclash_install_work_dir" && -d "$flclash_install_work_dir" ]]; then
    rm -rf -- "$flclash_install_work_dir"
  fi
}

usage() {
  cat <<'EOF'
Usage: install.sh [OPTIONS]

Install the latest FlClash TUI release for this Linux architecture.

Options:
  --version VERSION       Install a specific version instead of latest
  --method METHOD         auto, deb, or portable (default: auto)
  --arch ARCH             Override architecture detection: amd64 or arm64
  --install-dir PATH      Portable version storage directory
  --bin-dir PATH          Portable command symlink directory
  -h, --help              Show this help

Examples:
  curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash
  curl -fsSL https://raw.githubusercontent.com/yqlay/flclash-tui/main/install.sh | bash -s -- --method portable
EOF
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || die "required command not found: $1"
}

normalize_arch() {
  case "$1" in
    x86_64|amd64)
      printf '%s\n' 'amd64'
      ;;
    aarch64|arm64)
      printf '%s\n' 'arm64'
      ;;
    *)
      return 1
      ;;
  esac
}

parse_release_tag() {
  sed -nE 's/^[[:space:]]*"tag_name":[[:space:]]*"([^"]+)".*/\1/p' "$1" | head -n 1
}

parse_release_asset_digest() {
  local release_file=$1
  local asset_name=$2
  awk -v target="$asset_name" '
    /"name":[[:space:]]*"/ {
      name = $0
      sub(/^.*"name":[[:space:]]*"/, "", name)
      sub(/".*$/, "", name)
      matched = (name == target)
    }
    matched && /"digest":[[:space:]]*"sha256:/ {
      digest = $0
      sub(/^.*"digest":[[:space:]]*"sha256:/, "", digest)
      sub(/".*$/, "", digest)
      print digest
      exit
    }
  ' "$release_file"
}

validate_version() {
  [[ "$1" =~ ^[0-9]+([.][0-9]+){1,3}([.-][0-9A-Za-z.-]+)?$ ]]
}

download_file() {
  local url=$1
  local output=$2
  curl \
    --fail \
    --location \
    --silent \
    --show-error \
    --retry 3 \
    --connect-timeout 10 \
    --max-time 300 \
    --output "$output" \
    "$url"
}

verify_sha256() {
  local file=$1
  local expected=$2
  [[ "$expected" =~ ^[0-9a-fA-F]{64}$ ]] || die "release does not provide a valid SHA-256 digest for $(basename "$file")"
  local actual
  actual=$(sha256sum "$file" | awk '{print $1}')
  [[ "${actual,,}" == "${expected,,}" ]] || die "SHA-256 verification failed for $(basename "$file")"
  log "verified SHA-256 for $(basename "$file")"
}

install_deb() {
  local package_path=$1
  require_command dpkg
  require_command apt-get
  log "installing Debian package $(basename "$package_path")"
  if [[ $(id -u) -eq 0 ]]; then
    apt-get install --yes "$package_path"
    return
  fi
  require_command sudo
  sudo apt-get install --yes "$package_path"
}

prepare_portable_link() {
  local link_path=$1
  if [[ -e "$link_path" && ! -L "$link_path" ]]; then
    die "refusing to replace non-symlink command: $link_path"
  fi
}

install_portable() {
  local archive_path=$1
  local version=$2
  local arch=$3
  local work_dir=$4
  local install_root=$5
  local bin_dir=$6
  local package_name="flclash-tui_${version}_${arch}"
  local extract_dir="${work_dir}/extract"
  local source_dir="${extract_dir}/${package_name}"
  local destination="${install_root}/${version}-${arch}"

  mkdir -p "$extract_dir"
  tar -xzf "$archive_path" -C "$extract_dir"
  [[ -x "${source_dir}/flclash" ]] || die "portable archive does not contain ${package_name}/flclash"
  [[ -d "${source_dir}/data" ]] || die "portable archive does not contain bundled Geo data"

  prepare_portable_link "${bin_dir}/flclash"
  prepare_portable_link "${bin_dir}/flc"
  mkdir -p "$destination" "$bin_dir"
  cp -a "${source_dir}/." "$destination/"
  ln -sfn "${destination}/flclash" "${bin_dir}/flclash"
  ln -sfn "${destination}/flc" "${bin_dir}/flc"

  log "installed portable files in $destination"
  log "linked commands in $bin_dir"
  case ":${PATH}:" in
    *":${bin_dir}:"*) ;;
    *) log "add $bin_dir to PATH before running flclash" ;;
  esac
  log "portable installation does not include the privileged system TUN helper"
}

main() {
  local requested_version=''
  local requested_method='auto'
  local requested_arch=''
  local install_root="${FLCLASH_INSTALL_DIR:-}"
  local bin_dir="${FLCLASH_INSTALL_BIN_DIR:-}"

  while [[ $# -gt 0 ]]; do
    case "$1" in
      --version)
        [[ $# -ge 2 ]] || die '--version requires a value'
        requested_version=$2
        shift 2
        ;;
      --method)
        [[ $# -ge 2 ]] || die '--method requires a value'
        requested_method=$2
        shift 2
        ;;
      --arch)
        [[ $# -ge 2 ]] || die '--arch requires a value'
        requested_arch=$2
        shift 2
        ;;
      --install-dir)
        [[ $# -ge 2 ]] || die '--install-dir requires a value'
        install_root=$2
        shift 2
        ;;
      --bin-dir)
        [[ $# -ge 2 ]] || die '--bin-dir requires a value'
        bin_dir=$2
        shift 2
        ;;
      -h|--help)
        usage
        return
        ;;
      *)
        die "unknown option: $1"
        ;;
    esac
  done

  [[ $(uname -s) == 'Linux' ]] || die 'this installer supports Linux only'
  require_command curl
  require_command sha256sum

  local machine=${requested_arch:-$(uname -m)}
  local arch
  arch=$(normalize_arch "$machine") || die "unsupported architecture: $machine (expected amd64 or arm64)"

  case "$requested_method" in
    auto)
      if [[ -f /etc/debian_version ]] && command -v dpkg >/dev/null 2>&1 && command -v apt-get >/dev/null 2>&1; then
        requested_method='deb'
      else
        requested_method='portable'
      fi
      ;;
    deb|portable) ;;
    *) die "unsupported installation method: $requested_method" ;;
  esac

  if [[ "$requested_method" == 'deb' ]]; then
    local dpkg_arch
    dpkg_arch=$(dpkg --print-architecture)
    [[ "$dpkg_arch" == "$arch" ]] || die "dpkg architecture $dpkg_arch does not match detected architecture $arch"
  else
    require_command tar
  fi

  local work_dir
  work_dir=$(mktemp -d "${TMPDIR:-/tmp}/flclash-install.XXXXXX")
  flclash_install_work_dir=$work_dir
  trap cleanup_install_work_dir EXIT
  local release_file="${work_dir}/release.json"
  local release_endpoint="${FLCLASH_API_ROOT}/releases/latest"
  if [[ -n "$requested_version" ]]; then
    requested_version=${requested_version#v}
    validate_version "$requested_version" || die "invalid version: $requested_version"
    release_endpoint="${FLCLASH_API_ROOT}/releases/tags/v${requested_version}"
  fi

  log "reading release metadata"
  download_file "$release_endpoint" "$release_file"
  local tag
  tag=$(parse_release_tag "$release_file")
  [[ -n "$tag" ]] || die 'release metadata does not contain a tag name'
  local version=${tag#v}
  validate_version "$version" || die "release has invalid version: $tag"
  if [[ -n "$requested_version" && "$version" != "$requested_version" ]]; then
    die "release version $version does not match requested version $requested_version"
  fi

  local extension='deb'
  if [[ "$requested_method" == 'portable' ]]; then
    extension='tar.gz'
  fi
  local asset_name="flclash-tui_${version}_${arch}.${extension}"
  local digest
  digest=$(parse_release_asset_digest "$release_file" "$asset_name")
  [[ -n "$digest" ]] || die "release $tag does not contain a verified $asset_name asset"

  local asset_path="${work_dir}/${asset_name}"
  local asset_url="${FLCLASH_DOWNLOAD_ROOT}/${tag}/${asset_name}"
  log "downloading $asset_name"
  download_file "$asset_url" "$asset_path"
  verify_sha256 "$asset_path" "$digest"

  if [[ "$requested_method" == 'deb' ]]; then
    install_deb "$asset_path"
  else
    [[ -n "${HOME:-}" ]] || die 'HOME is required for a portable installation'
    install_root=${install_root:-${XDG_DATA_HOME:-${HOME}/.local/share}/flclash-tui}
    bin_dir=${bin_dir:-${HOME}/.local/bin}
    install_portable "$asset_path" "$version" "$arch" "$work_dir" "$install_root" "$bin_dir"
  fi
  log "FlClash TUI $version installation completed"
}

if [[ -z "${BASH_SOURCE[0]-}" || "${BASH_SOURCE[0]}" == "$0" ]]; then
  main "$@"
fi
