# Web reader setup

Publishing vaults with `leyline-web`: clone the theme repo, configure the instance, choose guest vs paired mode, deploy.

## What the web reader is

`leyline-web` is a read-only HTTP server that renders a vault's Markdown (Obsidian-flavored) directly from the filesystem — no WebSocket to `leyline-server`, it just reads the same files on disk. It serves each vault at `/<vault-prefix>/...`. Cache invalidation is automatic (every request rehashes the source file).

Two repos are involved:

- **The engine** — the `leyline-web` binary, from GitHub releases at `github.com/pawlenartowicz/leyline`.
- **The theme/scaffolding repo** — `github.com/pawlenartowicz/leyline-web`. It holds the themes, a demo config, and deploy stubs. A box has one clone of it at `/opt/leyline-web/themes`, shared by every site and never edited. **Keep the clone on a `v0.y.*` tag matching your engine's minor** — the engine ↔ theme-template contract can break across minors.

## Instance config

Each site (an "instance") has its own config file, `/etc/leyline-web/<instance>.yaml`, outside any git clone. It maps URL prefixes to vault paths and sets site-wide options. `listen` is the only required key. Set `default_theme` too: a non-empty vault with no theme (neither `default_theme` here nor `parent_theme` in its `web.yaml`) makes the instance fail at startup. The essentials:

```yaml
domain: notes.example.com      # public host; enables SEO (sitemap, canonical, OpenGraph). Bare host assumed https. Empty = SEO off.
listen: "127.0.0.1:8091"       # bind loopback behind a reverse proxy
dev_mode: false                # true hot-reloads themes; ALWAYS false in production
default_theme: notes           # used when a vault's web.yaml sets no parent_theme
# server_address:              # empty = guest mirror (read-only); set = paired mode (see below)

text_extensions: [.py, .json, .yaml, .txt, .csv]   # non-Markdown/-image files to render as text; anything unlisted 404s

vaults:                        # URL prefix → absolute vault path (a relative one resolves against /etc/leyline-web)
  "/":       /var/lib/leyline/vaults/landing
  "/notes":  /var/lib/leyline/vaults/research
```

Markdown and the common image types are always rendered; `text_extensions` covers additional text formats. Set `dev_mode: false` for production or theme edits require no restart but every request pays a reload cost.

## Per-vault config

Each vault carries its own presentation config in `.leyline/vaultconfig/web.yaml` (admin-only, synced) — this is vault content, not instance config. Common keys: `parent_theme`, `vault_id` (**required in paired mode**), `vault_name`, `guest_role` (`view` for anonymous reads, `none` to require login), `left_sidebar`/`right_sidebar`, `header.*`, `footer.*`.

What the reader exposes is gated by `.leyline/vaultconfig/webignore` — a multi-section gitignore file with `[view]` (hide from the web), `[history-ignore]`, and `[edit-ignore]` sections. `.leyline/` is always pinned to hidden regardless of what's written there. Note this is a **separate** gate from the server's `allowed` file, which the web reader does not consume.

## Guest mode vs paired mode

Set by the site config key `server_address`:

- **Guest mirror (empty, the default)** — read-only. Reads come straight off the vault filesystem. No management surface; `/<vault>/_panel` returns 404. This is all you need to publish notes for reading.
- **Paired mode (`server_address: <bare-host>`)** — additionally serves a capability-gated management panel at `/<vault>/_panel`. A logged-in admin can edit config files and manage keys/vaults there; the panel relays those mutations to the named `leyline-server` **as that user** (using their key from the login cookie), reusing the normal server auth — no new server privileges. `server_address` is a bare host, no scheme (the panel always dials `wss://`/`https://`, so it must be TLS-fronted; a loopback like `127.0.0.1:8090` will not work). Each paired vault must set `vault_id` in its `web.yaml` so the panel can address it as `<host>/<vault_id>`.

Reads work identically in both modes — pairing only adds the write/management panel.

## Deploy

The `leyline-web` package ships a template unit, `leyline-web@<instance>`, that runs `leyline-web -config /etc/leyline-web/<instance>.yaml -themes /opt/leyline-web/themes/config/themes`.

Once per box, clone the themes at the tag of the installed engine's minor:

```sh
sudo git clone https://github.com/pawlenartowicz/leyline-web /opt/leyline-web/themes
minor=$(leyline-web --version | cut -d. -f1,2)
sudo git -C /opt/leyline-web/themes checkout "$(sudo git -C /opt/leyline-web/themes tag -l "v${minor}.*" | sort -V | tail -1)"
```

Per site, write the config and enable the unit:

```sh
sudo $EDITOR /etc/leyline-web/mysite.yaml      # the keys above; absolute vault paths
sudo systemctl enable --now leyline-web@mysite
```

The clone and the config are root-owned and must be readable by the `leyline` user the unit runs as. Nobody edits a tracked file in the clone: a custom theme is a new folder under `/opt/leyline-web/themes/config/themes/`, and a site-specific look goes in the vault's `.leyline/vaultconfig/theme/`. That is what keeps an upgrade to a plain tag checkout (`references/upgrade.md`).

Each instance needs its own `listen` port. Put it behind the same reverse proxy as the sync server. When co-located on one host, the proxy routes `/_leyline/*` to `leyline-server` and everything else to `leyline-web` — see `references/server.md` for the proxy config. `SIGHUP` reloads the instance config in place; a parse error keeps the old config serving.

Verify:

```sh
curl -s http://127.0.0.1:8091/healthz                        # 200 once a vault is registered
curl -s http://127.0.0.1:8091/ | grep -o '<title>[^<]*'      # the site you expect, not the bundled example
leyline-web rules --effective -config /etc/leyline-web/mysite.yaml   # each mount's vault root, and the effective webignore rules
```

The health route returns 200 for any registered vault. Only the page itself shows that the right site is up. A `vaults:` path that does not exist is not refused either: the engine logs `vault unpopulated; serving built-in fallback until restart` and serves a fallback page.

## Failure modes

- **Everything 404s** — no vault registered, or the `vaults:` paths don't resolve. A relative path resolves against the config file's directory, `/etc/leyline-web`.
- **A file type won't render** — not Markdown/image and not in `text_extensions`; add its extension.
- **`_panel` 404s in paired mode** — `server_address` unset, the session isn't authenticated, the session holds no management capability for the vault, or the vault's `web.yaml` is missing `vault_id`.
- **Panel writes fail** — `server_address` points at a loopback or non-TLS host; it must be the public TLS-fronted hostname.
- **Theme looks wrong, or the instance exits at start, after an upgrade** — the engine and the theme clone are on different minors. Check the clone out at the matching `v0.y.*` tag and restart the instances (`references/upgrade.md`).
- **The site turned into the bundled example site** — a `0.7` box whose per-instance clone was moved off the commit that holds the site's `config/config.yaml`. See "A box on the 0.7 layout" and "Undo" in `references/upgrade.md`.
- **The instance fails to start with `config:`** — `/etc/leyline-web/<instance>.yaml` is missing or does not parse.
