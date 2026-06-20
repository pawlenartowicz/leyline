# leyline-server deploy/example

Fork-ready deploy templates for leyline-server. Each sub-directory mirrors
the shape used by `leyline-web/deploy/example/` for consistency. These
service units assume a source/tarball install under `/opt/leyline/` — the
deb/rpm/apk packages ship their own FHS units (`packaging/server/`) and do
not use these.

## Placeholders

All templates use literal placeholder strings. Substitute before installing:

| Placeholder | Meaning |
|---|---|
| `<operator-user>` | SSH login user running deploy scripts (e.g. `frog`, `deploy`) |
| `example.com` | Your public domain or hostname |
| `/opt/leyline/` | Install prefix for source/tarball installs (deb/rpm/apk packages use FHS paths under `/usr`, `/etc`, `/var`) |

## Files

| Path | Purpose |
|---|---|
| `systemd/leyline-server.service` | systemd unit (Debian/Ubuntu/RHEL-family) |
| `openrc/leyline-server.initd` | OpenRC init script (Alpine Linux) |
| `sudoers-leyline-server.txt` | Narrow `sudo` grants for the deploy user |
| `sysctl/99-leyline.conf` | inotify watch limit for fsnotify vault watching |
| `journald/00-disk-cap.conf` | Cap on the system journal (`SystemMaxUse=200M`, `RuntimeMaxUse=50M`) |
| `caddy/Caddyfile` | Caddy reverse-proxy with WebSocket keepalive |
| `nginx/leyline-server.conf` | nginx reverse-proxy with WebSocket upgrade headers |

## Quick install (Alpine + OpenRC)

```sh
# As root:
install -m 0755 openrc/leyline-server.initd /etc/init.d/leyline-server
# Substitute <operator-user>:
sed 's/<operator-user>/YOUR_DEPLOY_USER/g' sudoers-leyline-server.txt \
  | install -m 0440 /dev/stdin /etc/sudoers.d/leyline-server
visudo -cf /etc/sudoers.d/leyline-server    # verify before use
install -m 0644 sysctl/99-leyline.conf /etc/sysctl.d/99-leyline.conf
sysctl --system
rc-update add leyline-server default
```

## Journald cap

leyline-server logs to stderr; systemd persists those records. Without a
cap, the system journal grows to 10% of `/` or 4 GB (systemd default,
whichever is smaller) — on a 10 GB VPS that's a 1 GB ceiling the operator
never agreed to. Apply the drop-in once at deploy time:

```sh
# As root:
# 1. Ensure persistent journal directory exists (Fedora: present by default;
#    minimal Debian/Ubuntu: absent → silent volatile mode).
install -d -m 2755 -o root -g systemd-journal /var/log/journal

# 2. Install the cap drop-in.
install -m 0644 journald/00-disk-cap.conf /etc/systemd/journald.conf.d/00-disk-cap.conf

# 3. Reload journald with the new caps.
systemctl restart systemd-journald

# 4. Enforce the new cap on the existing backlog immediately.
journalctl --vacuum-size=200M
```

The drop-in caps the *whole* system journal (journald has no per-unit
size limit). The 200M total is sized for a 10 GB VPS hosting leyline plus
typical system services; bump it on larger disks.

## Notes

- `sudoers-leyline-server.txt` assumes the default vault name `default`.
  Adjust the `access_path` grant line if you use a different vault ID or
  multiple vaults.
- WebSocket connections are long-lived (server ping interval 30 s). nginx
  `proxy_read_timeout` is set to 120 s (`2 × ping_interval`) matching the
  client read deadline. Caddy handles this transparently with `flush_interval -1`.
- If you front Leyline with a browser-resident reader UI (anything that
  opens a WebSocket from a real browser), list its origin in
  `sync.allowed_origins` in `server.yaml`. CLI and Electron-hosted plugin
  clients omit the `Origin` header and are always accepted; the allowlist
  only gates browser-initiated upgrades.
- The nginx example assumes certbot manages the TLS server block. The three
  TLS directives documented at the top of `nginx/leyline-server.conf`
  (TLS 1.2 minimum, modern cipher selection, HSTS) are the floor — do not
  remove them when adapting to your hostname. Caddy applies equivalent
  defaults automatically.
