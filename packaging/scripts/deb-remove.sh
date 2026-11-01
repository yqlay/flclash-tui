#!/bin/sh
# dpkg also runs prerm for upgrades; do not disable the helper in that case.
if [ "${1:-}" = remove ]; then
    if command -v systemctl >/dev/null 2>&1; then
        systemctl disable --now flclash-tun-helper.service || true
    fi
fi
exit 0
