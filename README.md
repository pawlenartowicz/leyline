# Leyline

Self-hosted, collaborative sync for plain-file notes — Markdown-first, real-time, for small research teams.

**Documentation:** [pawlenartowicz.pl/leyline](https://pawlenartowicz.pl/leyline)

## Versioning

This is **0.2**. Every `0.2.*` release of `leyline` (server + CLI) **and** `leyline-web` is
compatible with all other `0.2.*` releases — run matching minors across the server, CLI,
web engine, and the [`web`](https://github.com/pawlenartowicz/web) theme clone (check it
out at a `v0.2.*` tag). During the `0.x` prerelease a **minor** bump (`0.2` → `0.3`) may
break compatibility; a **patch** bump (`0.2.0` → `0.2.1`) never does. Mixing minors — e.g.
a `0.2` engine with `0.1` themes — is unsupported.
