#!/bin/sh
# Ensure the shared system user/group `leyline` exists. Tools differ by distro:
# useradd/groupadd on deb/rpm, adduser/addgroup (busybox) on apk. All failures
# are tolerated so a re-run or a pre-existing user is not fatal.
set -e

if ! getent group leyline >/dev/null 2>&1; then
	groupadd --system leyline 2>/dev/null || addgroup -S leyline 2>/dev/null || true
fi
if ! getent passwd leyline >/dev/null 2>&1; then
	useradd --system --gid leyline --home-dir /var/lib/leyline \
		--shell /usr/sbin/nologin leyline 2>/dev/null \
		|| adduser -S -G leyline -h /var/lib/leyline -s /sbin/nologin leyline 2>/dev/null \
		|| true
fi
