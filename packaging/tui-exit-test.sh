#!/usr/bin/env bash
set -euo pipefail

binary=${1:-}
if [[ -z "$binary" || ! -x "$binary" ]]; then
  printf 'usage: %s /path/to/flclash\n' "$0" >&2
  exit 2
fi
if ! command -v script >/dev/null 2>&1; then
  printf 'script(1) is required for the TUI exit test\n' >&2
  exit 1
fi

uid=$(id -u)
runtime_root="/run/user/${uid}"
if [[ ! -d "$runtime_root" || ! -O "$runtime_root" ]]; then
  runtime_root="${TMPDIR:-/tmp}/flclash-runtime-${uid}"
else
  runtime_root="${runtime_root}/flclash"
fi
frontend_dir="${runtime_root}/.flclash-frontends"
test_dir=$(mktemp -d)
trap 'rm -rf -- "$test_dir"' EXIT
if [[ -d "$frontend_dir" ]]; then
  find "$frontend_dir" -maxdepth 1 -type f -printf '%f\n' | sort >"$test_dir/before"
else
  : >"$test_dir/before"
fi

# Bubble Tea probes terminal color and cursor position before reading keys.
# Supply valid terminal responses followed by q through a real pseudo-terminal.
timeout 5s bash -c '
  printf "\033]11;rgb:0000/0000/0000\007\033[1;1Rq" |
    script -qfec "$1 tui --no-start --config /dev/null --controller 127.0.0.1:1" /dev/null \
      >/dev/null
' bash "$binary"

if [[ -d "$frontend_dir" ]]; then
  find "$frontend_dir" -maxdepth 1 -type f -printf '%f\n' | sort >"$test_dir/after"
else
  : >"$test_dir/after"
fi
if comm -13 "$test_dir/before" "$test_dir/after" | grep -q .; then
  printf 'q left a TUI frontend session behind\n' >&2
  exit 1
fi

printf 'TUI q exit test passed\n'
