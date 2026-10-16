# Server setup

Standing up `leyline-server`: install, configure, run under systemd, front with a reverse proxy, verify.

## Install

Binaries are linux-only and published on GitHub releases at `github.com/pawlenartowicz/leyline`. Prefer the native package for your distro — it drops the binary on `$PATH`, installs a default config and systemd unit, and creates the `leyline` service user.

```sh
# Debian/Ubuntu
sudo apt install ./leyline-server_amd64.deb
# Fedora/RHEL
sudo dnf install ./leyline-server_amd64.rpm
# Alpine
sudo apk add --allow-untrusted ./leyline-server_amd64.apk
```

The package ships `leyline-admin` alongside the server. Requires the `git` CLI 2.x on `$PATH` (used for revert/restore).

If you install the bare binary instead of a package, use `templates/leyline-server.service` and `templates/server.yaml` from this skill as your starting unit and config.

## Configure

Copy `templates/server.yaml` to `/etc/leyline/config.yaml` and edit. The essentials:

- **`vaults_dir`** (required) — parent directory for vault subdirectories, e.g. `/var/lib/leyline/vaults`.
- **`registry`** — the TOML file recording which vaults exist. It is written on `vault create`, so it must be writable. Under the hardened unit `/etc` is read-only, so point it at the state dir (`/var/lib/leyline/registry.toml`), not next to the config.
- **`server.host` / `server.port`** — bind `127.0.0.1:8090` when a reverse proxy fronts the server; bind `0.0.0.0` only if nothing does.

Defaults worth knowing: `push_rate_limit` 1/sec/key, `max_connections_per_key` 7, per-IP connection cap 20 (hardcoded), 5 failed auths per IP per minute (hardcoded), `git_gc_at` 05:00 UTC. Full list is commented in the template.

After editing config, restart the service. `leyline-admin reload-config` re-reads `server.yaml` live but does **not** re-read the registry — for registry changes, restart.

## Run under systemd

The package installs and can enable the unit:

```sh
sudo systemctl enable --now leyline-server
sudo systemctl status leyline-server
journalctl -u leyline-server -f        # logs (slog JSON by default)
```

Logging env vars (set in the unit's `[Service]` with `Environment=`): `LEYLINE_LOG_FORMAT=text`, `LEYLINE_LOG_LEVEL=debug|info|warn|error`.

Vaults are discovered lazily — there is no startup scan. The first client or REST hit for a vault hydrates it from disk.

## Reverse proxy

The server speaks plain HTTP; terminate TLS at the proxy. All of the server's routes live under the `/_leyline/` prefix, and the only WebSocket endpoint is `/_leyline/sync/{vault}`. That prefix is what lets one hostname serve both the sync server and the web reader: route `/_leyline/*` to the server port and everything else to `leyline-web`.

Caddy, server-only:

```
sync.example.com {
    reverse_proxy /_leyline/* 127.0.0.1:8090
}
```

Caddy, server + web reader co-located behind one host (the common case) — the web reader conventionally listens on `127.0.0.1:8091` (see `references/web.md`):

```
notes.example.com {
    reverse_proxy /_leyline/* 127.0.0.1:8090   # sync server
    reverse_proxy 127.0.0.1:8091               # web reader (everything else)
}
```

nginx needs the WebSocket upgrade headers on the `/_leyline/` location:

```nginx
location /_leyline/ {
    proxy_pass http://127.0.0.1:8090;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "upgrade";
    proxy_set_header Host $host;
}
```

The proxy must present a real TLS certificate: clients dial `wss://` and `https://`, so a loopback address or a self-signed cert without `--insecure` will fail.

## Verify

```sh
curl -s https://notes.example.com/_leyline/healthz    # plain "ok"
curl -s https://notes.example.com/_leyline/health     # JSON with hub stats
```

Both are unauthenticated. `/_leyline/operator/status` is also open (for monitoring) and returns uptime, hydrated-vault count, active connections, and build version. From the server box, `leyline-admin status` prints the same.

## Server-box operator CLI

`leyline-admin` talks to the running server over the UNIX socket (`/run/leyline/admin.sock`, mode 0600 — file permissions **are** the auth, no key needed). It manages vaults and keys (see `references/vaults.md`) and reads the registry directly when the server is down:

```sh
leyline-admin vault list          # works offline; prints [offline] and omits runtime columns
leyline-admin status              # requires the server running
```

## Multiple servers on one host

A server instance is fully defined by its `server.yaml` and shares no global state. Run several side by side as long as these differ between them: `server.port`, `vaults_dir`, `registry`, and `admin_socket`. Give each its own systemd unit and point the reverse proxy at each port by hostname. `leyline-admin` targets one instance — select it with `--socket <path>` (or `LEYLINE_ADMIN_SOCKET`).

## Failure modes

- **404 on first client hit** — vault ID not in the registry, or its directory/access file is missing. Create the vault first.
- **Registry not writable** — `vault create` fails. Point `registry:` at a writable dir; under the hardened unit that means the state dir, not `/etc`.
- **WebSocket won't connect through the proxy** — the `/_leyline/*` location is missing the HTTP/1.1 upgrade headers (nginx), or TLS isn't actually terminated at the proxy.
- **`leyline-admin` refuses the socket** — socket permissions are wider than 0600; the binary treats that as a broken auth boundary and won't connect.
