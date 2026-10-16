# Client setup

Connecting a laptop with the `leyline` CLI: install, keys file, initialize a vault, pick a sync mode, run the daemon.

## Install

Linux-only, from GitHub releases at `github.com/pawlenartowicz/leyline`:

```sh
arch=$(uname -m); case $arch in x86_64) arch=amd64;; aarch64) arch=arm64;; esac
cd /tmp
curl -fsSL "https://github.com/pawlenartowicz/leyline/releases/latest/download/leyline_linux_${arch}.tar.gz" | tar xz
install -d ~/.local/bin && install -m 0755 leyline ~/.local/bin/
leyline --help
```

The CLI ships as a tarball with a single binary; there is no `.deb`/`.rpm`/`.apk` for it.

## The keys file

Operator and client keys live in `~/.config/leyline/keys` (mode 0600), one row per vault binding, whitespace-columnar:

```
# <host>/<vaultID>            <key>                   [keyname]
notes.example.com/research    ley_xxxxxxxxxxxxxxxxxxxx  alice
```

Lines starting with `#` and blank lines are skipped; `-` in the keyname column means unnamed. The CLI resolves a key by matching `<host>/<vaultID>`; a server-wide-admin key matches any vault on its host. `leyline init` appends a row for you, so you usually don't hand-edit this — but it's where a key you were given goes.

Note the address form: `notes.example.com/research`, **no `wss://` or `https://`**.

## Initialize a vault

From the directory you want synced (or one you'll fill from the server):

```sh
leyline init
```

Interactive bootstrap: it prompts for the vault address (`<host>/<vaultID>`) and key, writes `.leyline/leylinesetup` (the client-local config that also marks the vault root), appends the key to `~/.config/leyline/keys`, creates a default `.leyline/leylineignore`, and tests the connection.

The first sync has to reconcile whatever is already on disk against the server. Pick the mode with a flag (per-invocation only, never persisted):

- **`--merge`** (default) — keep local files and merge them with the server. On a collision, your version is renamed aside rather than lost.
- **`--from-server`** — move all local files to a trash folder and take a clean server checkout (recoverable via `leyline trash restore`).
- **`--from-local`** — keep local files and push them up as the vault's content. Requires a `vault.admin` key.

Use `--from-server` on a fresh laptop joining an existing vault; `--from-local` once, by an admin, to seed a new vault from an existing folder.

## Sync modes

Two axes: push-or-not, and one-shot-or-daemon.

| Command | Pushes? | Runs continuously? |
|---|---|---|
| `leyline sync` | yes | no (one-shot) |
| `leyline pull` | no | no (one-shot) |
| `leyline autosync` | yes | yes (watches for changes) |
| `leyline mirror` | no | yes (polls, default 30s) |

`sync`/`autosync` are for participants who edit and publish. `pull`/`mirror` are read-only followers: they receive changes but hold local edits, freezing a conflicting path until you resolve it (add `--discard` to instead let the server version win with no freeze).

## Run the daemon

```sh
leyline autosync            # detaches to background; logs to .leyline/backend/daemon.log
leyline autosync -d         # stays in foreground with live logs
leyline mirror              # read-only follower, background
leyline status             # role, dirty-file count, last sync
leyline stop               # stop the daemon
leyline list               # every vault initialized on this machine, with status
```

For an always-on laptop or a headless follower box, wrap `leyline autosync -d` (or `mirror -d`) in a user systemd service so it restarts on boot.

## Conflicts

Three-way line-level merge runs on the client. A conflict is written into the file in place — an Obsidian `[!conflict]` callout for Markdown, a comment block for code, or a sidecar file for binaries (set `diff_mode = git` in `leylinesetup` for traditional `<<<<<<<` markers instead). Resolve by editing the file to the state you want and re-syncing.

```sh
leyline conflicts               # list pending conflicts
```

## Safety nets

- A large batch of deletes (≥25% of the vault and ≥10 files) parks the push and drops a `LEYLINE_CONFIRM_NEEDED.txt` marker at the vault root rather than pushing them — this catches an `rm -rf` or an unmounted drive. Run `leyline confirm` to proceed or `leyline restore-local` to undo.
- Inbound deletes move files to `.leyline/trash/<timestamp>/` instead of unlinking; `leyline trash list` / `leyline trash restore <path>` recover them. Trash is not auto-pruned.

## History (needs `history.*` capability)

```sh
leyline tag <name> [commit]     # name a vetted state
leyline review                  # auto-named review mark (reviewed-<timestamp>)
leyline history                 # recent commits
leyline revert <commit>         # undo a commit
leyline restore <commit>        # roll the tree back to a snapshot
```

These proxy through the running daemon, so the daemon must be up.
