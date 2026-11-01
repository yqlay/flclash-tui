#!/bin/sh
if command -v rc-update >/dev/null 2>&1 && command -v rc-service >/dev/null 2>&1; then
    rc-update add flclash-tun-helper default || true
    rc-service flclash-tun-helper start || true
fi
exit 0
