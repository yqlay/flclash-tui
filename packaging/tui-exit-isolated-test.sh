#!/usr/bin/env bash
set -euo pipefail

# Isolate runtime sockets, process discovery and the network before exercising
# global shutdown. Never point this test at the user's live Backend or SSH.
binary=${1:-}
if [[ -z "$binary" || ! -x "$binary" ]]; then
  printf 'usage: %s /path/to/flclash\n' "$0" >&2
  exit 2
fi
binary=$(realpath -- "$binary")
test_script=$(realpath -- "$(dirname -- "$0")/tui-exit-test.sh")
if ! unshare --user --map-root-user --mount --pid --net --fork true; then
  printf 'Isolated TUI tests require user/mount/PID/network namespaces; no host test was run.\n' >&2
  exit 1
fi
exec unshare --user --map-root-user --mount --pid --net --fork --mount-proc bash -c '
  set -euo pipefail
  mount --make-rprivate /
  mount -t tmpfs -o mode=755 tmpfs /run/user
  mkdir -m 700 /run/user/0
  export XDG_CONFIG_HOME=/run/user/0/test-config
  export XDG_STATE_HOME=/run/user/0/test-state
  export FLCLASH_EXIT_TEST_ISOLATED=1
  ip link set lo up
  bash "$1" "$2"
' bash "$test_script" "$binary"
