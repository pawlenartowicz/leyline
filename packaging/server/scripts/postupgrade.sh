#!/bin/sh
# apk upgrade path: restart the running service in place.
set -e
rc-service leyline-server restart || true
