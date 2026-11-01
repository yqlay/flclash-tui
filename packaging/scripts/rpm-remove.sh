#!/bin/sh
# RPM passes 0 for the final removal, and 1 while replacing an older package.
if [ "${1:-}" = 0 ]; then
    if command -v systemctl >/dev/null 2>&1; then
        systemctl disable --now flclash-tun-helper.service || true
    fi
fi
exit 0
