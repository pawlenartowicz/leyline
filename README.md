# Leyline

Self-hosted, collaborative sync for plain-file notes — Markdown-first, real-time, for small research teams.

**Documentation:** [pawlenartowicz.pl/leyline](https://pawlenartowicz.pl/leyline)

## Versioning

Versions are `0.MINOR.PATCH`; the current one is the latest
[release](https://github.com/pawlenartowicz/leyline/releases). Every release of `leyline`
(server + CLI) **and** `leyline-web` is compatible with every other release of the same
minor — run matching minors across the server, CLI, web engine, and the
[`leyline-web`](https://github.com/pawlenartowicz/leyline-web) theme clone (check it out at a
`v0.y.*` tag of that minor). During the `0.x` prerelease a **minor** bump (`0.y` → `0.(y+1)`)
may break compatibility; a **patch** bump never does. Mixing minors — e.g. a newer engine
with older themes — is unsupported.
