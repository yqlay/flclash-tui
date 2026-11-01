#!/bin/sh
if command -v rc-update >/dev/null 2>&1 && command -v rc-service >/dev/null 2>&1; then
    rc-service flclash-tun-helper stop || true
    rc-update del flclash-tun-helper default || true
fi
exit 0
