#!/bin/sh
set -e

SERVICE=leyline-server

# Fresh install? rpm: $1==1 fresh / $1==2 upgrade. deb: $1==configure, $2 empty
# = fresh. apk: this runs only on fresh (upgrades go to postupgrade.sh), so the
# fall-through default is "fresh".
is_fresh() {
	case "$1" in
		configure) [ -z "$2" ] ;;   # deb
		2)         return 1 ;;       # rpm upgrade
		*)         return 0 ;;       # rpm fresh ($1==1) or apk post-install
	esac
}

# Vaults dir is created here (untracked) so uninstall never deletes user data.
mkdir -p /var/lib/leyline/vaults
chown -R leyline:leyline /var/lib/leyline 2>/dev/null || true
chmod 0750 /var/lib/leyline/vaults 2>/dev/null || true

if command -v systemctl >/dev/null 2>&1; then
	systemctl daemon-reload || true
	if is_fresh "$@"; then
		systemctl enable --now "$SERVICE" || true
	else
		systemctl try-restart "$SERVICE" || true
	fi
elif command -v rc-update >/dev/null 2>&1; then
	# apk + openrc, fresh install path (upgrades handled by postupgrade.sh).
	rc-update add "$SERVICE" default || true
	rc-service "$SERVICE" restart || true
fi
