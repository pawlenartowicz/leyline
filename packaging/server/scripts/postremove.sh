#!/bin/sh
# Intentionally preserves user data on removal: /var/lib/leyline (vaults +
# registry) is created untracked in postinstall, so the package manager never
# owns or deletes it. /etc/leyline/config.yaml is config-noreplace. Nothing to
# do here — this script documents the deliberate omission.
exit 0
