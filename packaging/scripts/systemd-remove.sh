#!/bin/sh
if command -v systemctl >/dev/null 2>&1; then
    systemctl disable --now flclash-tun-helper.service || true
fi
exit 0
