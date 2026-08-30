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
backend_started=false
cleanup() {
  if [[ "$backend_started" == true ]]; then
    "$binary" exit >/dev/null 2>&1 || true
  fi
  rm -rf -- "$test_dir"
}
trap cleanup EXIT

initial_status=$("$binary" status --json)
if grep -q '"backend": "running"' <<<"$initial_status"; then
  printf 'TUI exit test requires no pre-existing FlClash backend\n' >&2
  exit 1
fi
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

managed_dir="${test_dir}/managed-home"
mkdir -p "$managed_dir"
backend_started=true

# q must terminate only the current frontend. The managed Backend and Core stay
# alive, while the frontend session lock is released synchronously.
timeout 15s bash -c '
  (sleep 0.2; printf "\033]11;rgb:0000/0000/0000\007\033[1;1R"; sleep 0.8; printf q) |
    script -qfec "$1 tui --directory $2" /dev/null >/dev/null
' bash "$binary" "$managed_dir"

after_q_status=$("$binary" status --json)
backend_pid=$(sed -n 's/.*"backend_pid": \([0-9][0-9]*\).*/\1/p' <<<"$after_q_status")
frontends=$(sed -n 's/.*"frontends": \([0-9][0-9]*\).*/\1/p' <<<"$after_q_status")
if [[ -z "$backend_pid" ]] || ! kill -0 "$backend_pid" 2>/dev/null; then
  printf 'q stopped the managed Backend instead of only the TUI frontend\n' >&2
  exit 1
fi
if [[ "$frontends" != 0 ]]; then
  printf 'q left %s TUI frontend session(s) registered\n' "$frontends" >&2
  exit 1
fi

# flclash exit must also clean up a frontend that cannot handle graceful TERM.
# The frontend PID is read from its validated session lock through `clients`;
# SIGKILL is therefore scoped to this FlClash TUI rather than a process name.
(
  { printf "\033]11;rgb:0000/0000/0000\007\033[1;1R"; sleep 30; } |
    script -qfec "$binary tui --directory $managed_dir" /dev/null >/dev/null
) &
stuck_job=$!
stuck_pid=""
for _ in $(seq 1 100); do
  stuck_pid=$("$binary" backend clients | sed -n 's/^PID[[:space:]]*\([0-9][0-9]*\).*/\1/p' | head -1)
  if [[ -n "$stuck_pid" ]]; then
    break
  fi
  sleep 0.05
done
if [[ -z "$stuck_pid" ]]; then
  printf 'could not observe the second TUI frontend registration\n' >&2
  exit 1
fi
kill -STOP "$stuck_pid"
timeout 15s "$binary" exit >/dev/null
# script(1) can stop itself after its foreground TUI is killed. Resume its
# direct child so it can reap the dead TUI before process-liveness assertions.
stuck_children=$(pgrep -P "$stuck_job" 2>/dev/null || true)
if [[ -n "$stuck_children" ]]; then
  # shellcheck disable=SC2086
  kill -CONT $stuck_children 2>/dev/null || true
fi
sleep 0.1
stuck_children=$(pgrep -P "$stuck_job" 2>/dev/null || true)
if [[ -n "$stuck_children" ]]; then
  # The remaining child is the test's idle input producer, not FlClash.
  # shellcheck disable=SC2086
  kill -TERM $stuck_children 2>/dev/null || true
fi
wait "$stuck_job" 2>/dev/null || true
backend_started=false
if kill -0 "$stuck_pid" 2>/dev/null || kill -0 "$backend_pid" 2>/dev/null; then
  printf 'flclash exit left frontend PID %s or Backend PID %s running\n' "$stuck_pid" "$backend_pid" >&2
  exit 1
fi
if [[ -e "${runtime_root}/.flclash-cli-service.sock" ]]; then
  printf 'flclash exit left the Backend Unix socket behind\n' >&2
  exit 1
fi
timeout 5s "$binary" exit >/dev/null

# Start a fresh managed runtime to independently verify Ctrl+C from input mode.
"$binary" start --directory "$managed_dir" >/dev/null
backend_started=true
after_restart_status=$("$binary" status --json)
backend_pid=$(sed -n 's/.*"backend_pid": \([0-9][0-9]*\).*/\1/p' <<<"$after_restart_status")
if [[ -z "$backend_pid" ]] || ! kill -0 "$backend_pid" 2>/dev/null; then
  printf 'could not restart Backend before Ctrl+C test\n' >&2
  exit 1
fi

# Ctrl+C must use the same shutdown path even while an input editor owns the
# keyboard. It may return only after both frontend and Backend processes exit.
timeout 15s bash -c '
  (sleep 0.2; printf "\033]11;rgb:0000/0000/0000\007\033[1;1R"; sleep 0.8; printf p; sleep 0.3; printf "\003") |
    script -qfec "$1 tui --directory $2" /dev/null >/dev/null
' bash "$binary" "$managed_dir"

for _ in $(seq 1 100); do
  if ! kill -0 "$backend_pid" 2>/dev/null; then
    break
  fi
  sleep 0.05
done
if kill -0 "$backend_pid" 2>/dev/null; then
  printf 'Ctrl+C left Backend PID %s running\n' "$backend_pid" >&2
  exit 1
fi
backend_started=false

after_interrupt_status=$("$binary" status --json)
if ! grep -q '"backend": "stopped"' <<<"$after_interrupt_status" ||
  ! grep -q '"core": "stopped"' <<<"$after_interrupt_status"; then
  printf 'Ctrl+C left Backend or Core running:\n%s\n' "$after_interrupt_status" >&2
  exit 1
fi
if [[ -e "${runtime_root}/.flclash-cli-service.sock" ]]; then
  printf 'Ctrl+C left the Backend Unix socket behind\n' >&2
  exit 1
fi
if [[ -d "$frontend_dir" ]]; then
  find "$frontend_dir" -maxdepth 1 -type f -printf '%f\n' | sort >"$test_dir/final"
else
  : >"$test_dir/final"
fi
if comm -13 "$test_dir/before" "$test_dir/final" | grep -q .; then
  printf 'Ctrl+C left a TUI frontend session behind\n' >&2
  exit 1
fi

printf 'TUI q/Ctrl+C/flclash-exit process lifecycle test passed\n'
