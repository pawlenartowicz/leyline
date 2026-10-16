#!/bin/sh
set -e

# /etc/leyline-web holds one <instance>.yaml per site; /opt/leyline-web holds the
# shared theme clone. Both are untracked so uninstall never removes operator data.
# install -d sets the mode itself; mkdir -p would take it from the umask.
install -d -m 0755 /etc/leyline-web /opt/leyline-web

if command -v systemctl >/dev/null 2>&1; then
	systemctl daemon-reload || true
	# Restart only already-running instances; opt-in instances are never auto-enabled.
	systemctl try-restart 'leyline-web@*' || true
fi
