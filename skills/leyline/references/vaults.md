# Vault and key management

Creating and destroying vaults, minting and revoking API keys, roles and capabilities, and the `allowed` sync rules.

## Two entry points, same verbs

Operator commands come in two forms with identical verbs, arguments, output, and exit codes:

- **On the server box:** the standalone `leyline-admin` binary, talking over the UNIX socket. Positional argument is the bare `<vaultID>`. Socket access is the authority — no key needed.
- **From a laptop:** the `leyline admin` subcommand of the `leyline` CLI, talking over HTTPS. Positional argument is the full `<host>/<vaultID>`. Authority comes from a key you hold that has server-wide admin rights (see below).

Examples below show the server-box form; for the laptop, prefix with `leyline`, use `<host>/<vaultID>`, and supply a key. Add `--json` to any verb for machine-readable output.

## Create a vault

```sh
leyline-admin vault create research
```

This registers the vault, creates its directory and `.leyline/` control plane, and mints the first admin key — **printed once to stdout, never recoverable**. Capture it. Options:

- `--path PATH` — vault directory (defaults under `vaults_dir`). Refuses a non-empty target.
- `--server-wide-admin` — flag the vault so any `vault.admin` key in it also holds server-wide operator authority (needed to run laptop `leyline admin` against the server). Reserve this for an operations vault; anyone with admin there can administer the whole server.
- `--admin-email EMAIL`, `--admin-key-name NAME` — label the first key.

From a laptop you need a server-wide-admin key already in hand:

```sh
leyline admin vault create research --server https://notes.example.com --key ley_xxxxxxxxxxxxxxxxxxxx
# or, if ~/.config/leyline/keys already has a server-wide-admin row for this host, the key resolves automatically:
leyline admin vault create notes.example.com/research
```

## Destroy, reset, reload

```sh
leyline-admin vault list                  # id, path, server_wide_admins, hydrated, last-activity
leyline-admin vault destroy research      # disconnect clients, drop registry entry, move dir to trash
leyline-admin vault reset research        # wipe content, keep .leyline/ and registry entry
leyline-admin vault reload research       # evict from memory; next hit rehydrates from disk
```

`destroy` moves the directory to the trash dir rather than deleting it; clean trash yourself. `reset` is the "empty this vault but keep it" path.

## Mint and revoke keys

```sh
leyline-admin key list research
leyline-admin key add research --name alice --role reader --email alice@example.com   # prints the key once
leyline-admin key remove research alice                                               # revokes immediately
```

`key remove` is the revocation path — the key stops authenticating and any live connection using it is dropped. Removing a key that granted server-wide authority (i.e. an admin key in a `server_wide_admins` vault) revokes that authority too.

Recovery when every admin key is lost: `key bootstrap-admin research --name rescue` force-adds a fresh admin key. On the server box the socket implies the authority; from a laptop it requires an existing server-wide-admin key.

## Roles and capabilities

Roles are named capability sets — the system never compares role strings, only capabilities. Built-ins:

| Role | Capabilities |
|---|---|
| `reader` | `sync.pull` |
| `editor` | `sync.pull`, `sync.push`, `history.revert` |
| `admin` | `sync.pull`, `sync.push`, `keys.manage`, `vault.admin`, `history.tag`, `history.revert` |

Give a reader `reader`, a normal collaborator `editor`, and a vault operator `admin`. The individual capabilities mean:

- `sync.pull` — receive changes (read).
- `sync.push` — send changes (write).
- `keys.manage` — mint/revoke keys in this vault.
- `vault.admin` — vault-level admin, including editing `.leyline/vaultconfig/`.
- `history.tag` — create/delete tags and review marks.
- `history.revert` — revert commits and restore snapshots.

Custom roles live in `.leyline/vaultconfig/roles` (an admin-only, synced file). To use one, name it in `key add --role <name>`; the role must already exist there or be a built-in.

## The `allowed` sync rules

`.leyline/vaultconfig/allowed` (admin-only, synced, INI-style) is the server's authoritative whitelist. Three sections:

- **`[sync]`** — which file extensions and sizes are allowed to sync at all.
- **`[history]`** — which of those are git-tracked (get history).
- **`[limits]`** — per-file size caps.

Edit it as an admin — either directly in the vault (it syncs back to the server on push) or through the web panel in paired mode (see `references/web.md`). Files outside `[sync]` never reach the server; files in `[sync]` but outside `[history]` sync without being versioned. Independently of `allowed`, hidden paths (`.*`) are always excluded except `.leyline/` for admins, and path validation rejects `..`, backslashes, null bytes, and Windows reserved names.

## Where auth lives

Keys are stored hashed in `.leyline/vaultconfig/access` — server-authoritative, written only by the key API, then committed and synced read-only to admins. Never hand-edit it. The server watches `.leyline/vaultconfig/` and re-evaluates connected clients when `access`, `allowed`, or `roles` changes, dropping clients whose key was revoked or whose role changed.
