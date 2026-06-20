# Vendored test fixtures

Frozen snapshots, **not** live copies. Do not edit to track upstream.

- `notes/` — a vault with the **directory/slug layout** of the `web` repo's
  `example-vaults/notes` showcase, but the page prose is replaced with lorem ipsum
  so the fixtures don't read as real (and drifting) product docs. Structure that the
  tests anchor on is preserved verbatim: filenames/slugs, frontmatter, H1 headings,
  the `index.md` wikilink graph, and the sentinels `"Quick Start vault"`,
  `"What is Leyline?"`, and `"Markdown rendering"`. Don't re-copy from the `web` repo
  wholesale — that would reintroduce the prose; port only structural changes by hand.
- `themes/notes`, `themes/leyline_base` — snapshot of the `web` repo's `config/themes`,
  minus `leyline_base/theme/static/vendor/` (KaTeX/MathJax — the tests never request
  those assets, and the theme loader skips missing layers silently).

Tests anchor here via `testdataDir(t)` (`runtime.Caller`), never via umbrella sniffing
or a sibling-repo path.
