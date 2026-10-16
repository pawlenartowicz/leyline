---
name: leyline
description: Use when setting up, operating or upgrading a Leyline note-sync deployment — installing or configuring a leyline-server, upgrading the server, the web reader or a site's theme clone to a new release, creating or destroying vaults, minting or revoking API keys, connecting a laptop or CLI client, or publishing the leyline-web reader. For operators running the released binaries, not for developing Leyline itself.
---

# Leyline operations

Leyline is a self-hosted collaborative sync system for Markdown notes — plain files on disk, real-time line-level three-way merge, API-key auth, aimed at small teams. This skill covers operator tasks against the released binaries.

## The three binaries

| Binary | Runs on | Role |
|---|---|---|
| `leyline-server` | the server box | WebSocket sync + REST admin + git-backed history. One long-running daemon. |
| `leyline` | each user's laptop | Client CLI + sync daemon (`autosync`/`mirror`), plus a `leyline admin` operator subcommand. |
| `leyline-web` | the server box (or any host with the vaults) | Read-only web reader; reads the vault filesystem directly. |

The server box also ships `leyline-admin`, an operator CLI that talks to a local `leyline-server` over a UNIX socket. Binaries are linux-only and install from GitHub releases at `github.com/pawlenartowicz/leyline`: `.deb`/`.rpm`/`.apk` packages for `leyline-server` and `leyline-web`, a tarball for the `leyline` CLI.

## Facts that span every task

- **Vault address is always `<host>/<vaultID>`** — no `wss://`, `https://`, or any protocol prefix, anywhere (config files, keys file, CLI args). The host is the public hostname; the vault ID matches `^[a-z0-9][a-z0-9-]{0,62}$`.
- **API keys look like `ley_` followed by 20 alphanumeric characters.** The server stores only a hash; a freshly minted key is printed once and never again.
- **The `.leyline/` directory at the vault root holds the control plane.** `.leyline/vaultconfig/` (server-authoritative, admin-only: `access` keys, `allowed` sync rules, `roles`, `web.yaml`, `webignore`) syncs to admins; `.leyline/README.md` is the only piece readers see. Client-local files (`leylinesetup`, `leylineignore`, `backend/`) never leave the laptop.
- **Version parity:** every `0.y.*` release of all four components (`leyline-server`, `leyline`, `leyline-web`, and the `leyline-web` theme repo) is mutually compatible. Crossing a minor (`0.y` → `0.(y+1)`) carries no compatibility guarantee — deploy matching minors. **This skill targets the `0.8` line**, and `references/upgrade.md` also covers a box still on the `0.7` layout.

## Before you change a running deployment

A live box holds state this skill cannot know: which layout it is on, more web instances than you expect, unit drop-ins, a site's settings kept inside a git clone. Three rules follow, and they apply to every reference below.

1. **Look first.** Run the read-only inventory in `references/upgrade.md` before any command that changes the box. Do not write a command for the operator, or run one, until you have seen the state it acts on.
2. **Know where each site's config lives before touching any git clone.** On the current layout it is `/etc/leyline-web/<instance>.yaml` and the shared theme clone holds no site settings. On a `0.7` box the config is a tracked file inside `/opt/leyline-web/<instance>`; a `git checkout <tag>`, `git checkout -f` or `git reset --hard` there makes the instance serve the bundled example site in place of the real one, and the health check still returns 200.
3. **Check what is served, before and after.** Record each site's page title before the change and compare after. A 200 from `/_health` only says a vault is registered.

## Which reference to read

| Task | Read |
|---|---|
| Install the server, write its config, run it under systemd, put a reverse proxy in front, verify health | `references/server.md` |
| Create or destroy vaults, mint or revoke keys, set roles/capabilities, tune the `allowed` sync rules | `references/vaults.md` |
| Connect a laptop: install the CLI, write the keys file, initialize a vault, pick a sync mode, run the daemon | `references/client.md` |
| Publish the web reader: clone the theme repo, write the site config and the vault's `web.yaml`/`webignore`, guest vs paired mode, deploy | `references/web.md` |
| Upgrade a running deployment to a new release: theme clone, packages, verification, moving a `0.7` box to the current layout, rollback | `references/upgrade.md` |

Templates to copy and edit (never executed): `templates/server.yaml`, `templates/leyline-server.service`.
