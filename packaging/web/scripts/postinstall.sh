#!/bin/sh
set -e

# Parent dir for per-site clones. Untracked so uninstall never removes operator data.
mkdir -p /opt/leyline-web
chown leyline:leyline /opt/leyline-web 2>/dev/null || true

if command -v systemctl >/dev/null 2>&1; then
	systemctl daemon-reload || true
	# Restart only already-running instances; opt-in instances are never auto-enabled.
	systemctl try-restart 'leyline-web@*' || true
fi
