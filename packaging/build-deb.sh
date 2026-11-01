#!/usr/bin/env bash
set -euo pipefail
export FORMATS='deb portable'
exec bash "$(dirname "${BASH_SOURCE[0]}")/build-linux.sh" "$@"
