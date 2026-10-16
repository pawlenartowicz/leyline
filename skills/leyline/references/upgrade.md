# Upgrading a deployment

Moving a running box to a new release. Three things move: the two packages (`leyline-server` with `leyline-admin`, and `leyline-web`) and the theme clone. Follow the steps in order. Step 1 changes nothing and is not optional.

## 1. Inventory (read-only)

```sh
leyline-admin status                 # server version
leyline-web --version
systemctl list-units 'leyline-web@*' --no-legend
systemctl cat 'leyline-web@*' | grep -E '^# /|ExecStart'
```

There is often more than one web instance. The `ExecStart` line tells you which layout the box has:

| `ExecStart` reads | Layout | Go to |
|---|---|---|
| `-config /etc/leyline-web/%i.yaml -themes /opt/leyline-web/themes/config/themes` | current: config outside git, one shared theme clone | step 2 |
| `-config /opt/leyline-web/%i/config/config.yaml` | `0.7` and older: the config is a tracked file in a per-instance clone | "A box on the 0.7 layout" |

`%i` is the instance name. Each `# /path` line names a file the unit is built from: the packaged one is `/usr/lib/systemd/system/leyline-web@.service`, and any line under `/etc/systemd/` is a drop-in that can override it. Read what a drop-in sets before trusting the table.

Then record, per instance, where it listens, what it mounts and what it serves:

```sh
leyline-web rules --effective -config <its config file> | grep '^# vault'
grep -nE '^(domain|listen)' <its config file>
curl -s http://127.0.0.1:<port>/ | grep -o '<title>[^<]*'
```

Keep the titles. Step 3 compares against them.

On the current layout, also confirm the shared clone is unedited:

```sh
sudo git -C /opt/leyline-web/themes status -sb      # expect no ' M' lines
```

Untracked folders (`??`) under `config/themes/` are custom themes and are fine. A modified tracked file means someone edited a shipped theme; stop and ask the operator before any checkout.

## 2. Upgrade (current layout)

Download, then themes, then install:

```sh
leyline-admin update                           # downloads the packages; prints "installed → latest" and one install line
TAG=vX.Y.Z                                     # the "latest" it printed, with a leading v
sudo git -C /opt/leyline-web/themes fetch --tags
sudo git -C /opt/leyline-web/themes checkout "$(sudo git -C /opt/leyline-web/themes tag -l "${TAG%.*}.*" | sort -V | tail -1)"   # newest theme tag of that minor; if this fails, stop: do not install
sudo apt install /tmp/leyline-server_amd64.deb /tmp/leyline-web_amd64.deb   # the line `update` printed
sudo systemctl restart 'leyline-web@*'
```

- **Why this order.** On `.deb` and `.rpm` the package install restarts `leyline-server` and every running `leyline-web@*` instance. A new engine that starts on old themes can exit when a required template is missing. A running engine re-reads templates only on a config reload or when a vault's `web.yaml` or `webignore` changes, so keep the gap between the checkout and the install short.
- **`leyline-admin update` only downloads.** It checks both packages against the latest GitHub release and prints the install line. It does not install, restart, or touch the theme clone. Install both packages in one command so the server and the reader never run different versions.
- **From `0.6.x`, `leyline-admin update` cannot find newer packages.** It looks for a file name that carries the version, and from `0.7.0` the release files have none. It prints `no deb asset for amd64`. Download by URL for that one hop: `https://github.com/pawlenartowicz/leyline/releases/download/<tag>/leyline-server_amd64.deb` and `leyline-web_amd64.deb` (use `.rpm` or `.apk` and `arm64` as needed).

## 3. Verify

```sh
leyline-web --version
curl -s http://127.0.0.1:<port>/ | grep -o '<title>[^<]*'    # per instance; must match step 1
curl -s https://<host>/_leyline/healthz                      # sync server
journalctl -u 'leyline-web@*' --since -5min | grep -i 'unpopulated'
```

- A title you did not record in step 1 means the instance reads a different config or a different vault than before.
- The log line `vault unpopulated; serving built-in fallback until restart` means a path under `vaults:` does not exist. The engine does not refuse to start on it.
- `/_health` returns 200 for any registered vault. It does not tell you the right site is up.
- Browsers can keep showing the page they cached before the upgrade. Check with a hard refresh.

## A box on the 0.7 layout

Here each site's settings are a local change to the tracked `config/config.yaml` inside `/opt/leyline-web/<instance>`. They may be a commit on a local branch or an uncommitted edit. **Never run `git checkout <tag>`, `git checkout -f` or `git reset --hard <tag>` in such a clone.** Leaving the commit that holds the settings makes the instance serve the bundled example site, with a healthy process and a 200 health check.

Look at every clone first:

```sh
d=/opt/leyline-web/<instance>
git -C $d status -sb                 # branch or detached HEAD, ahead/behind, local edits
git -C $d log --oneline -3
git -C $d diff --stat                # uncommitted edits
```

Run git as the user that owns the clone (`stat -c %U $d/.git`). If git reports `dubious ownership`, do not add a `safe.directory` exception; run it as that owner with `sudo -u <owner> git`.

### Moving it to the current layout

There is no fallback to the old paths: after the package upgrade an instance without `/etc/leyline-web/<instance>.yaml` fails to start, and the install restarts running instances. So the move comes before the install.

1. `sudo install -d -m 0755 /etc/leyline-web`
2. Per instance: confirm the clone's `config/config.yaml` has the site's `vaults:` map (not the example vaults), then copy it to `/etc/leyline-web/<instance>.yaml` and make every path under `vaults:` absolute. A mount of a bundled example vault points at `/opt/leyline-web/themes/example-vaults/<name>`.
3. `sudo git clone https://github.com/pawlenartowicz/leyline-web /opt/leyline-web/themes`, then check out the newest theme tag of the new minor there, as in step 2. An old instance folder named `themes` has to be moved aside first. Copy any custom theme folder from an old clone's `config/themes/` into `/opt/leyline-web/themes/config/themes/`; an instance whose `default_theme` is missing there fails to start.
4. Per instance: `leyline-web rules --effective -config /etc/leyline-web/<instance>.yaml | grep '^# vault'`. Every root must be the path you expect. A relative path left in the file shows up as a root under `/etc/leyline-web/`.
5. Install the packages and restart the instances.
6. Verify as in step 3. Then move each old clone aside, restart, verify again, and only then delete it.

### Staying on 0.7 for a patch release

The theme clone of each instance moves by rebase, never by checkout. `git -C $d fetch --tags` first.

- **Settings committed on a branch, no uncommitted edits:** `git -C $d rebase <tag>`.
- **Uncommitted edits:** commit them on a branch first (`git -C $d switch -c site` if HEAD is detached, then `git -C $d commit -am "site config"`), then rebase.

A rebase conflict means the release and the site changed the same lines. `git -C $d rebase --abort` returns the clone to where it was. Resolve it with the operator; do not force it. Restart the instance afterwards and verify as in step 3.

## Undo

- **Shared theme clone:** `sudo git -C /opt/leyline-web/themes checkout -` returns to the tag it was on before. Restart the instances afterwards.
- **A `0.7` per-instance clone:** do not use `checkout -` there. It returns to the last position that was checked out, which after a rebase can be the bare tag without the site's settings. `git -C $d reflog -8` lists where the clone has been; the commit to go back to is the one that holds the site's `vaults:` map (`git -C $d show <commit>:config/config.yaml`). Right after a finished rebase that commit is `ORIG_HEAD`, and `git -C $d reset --hard ORIG_HEAD` returns to it. Read the `vaults:` map in `config/config.yaml` before restarting the instance.
- **Packages:** install the previous release's package files over the new ones, then restart. Vault data and the WAL need no change within the `0.x` series.
