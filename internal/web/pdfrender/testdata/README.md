# Vendored test fixtures

`paper.pdf` — synthetic 2-page lorem ipsum PDF (~2.5 KB), used by `pdfrender_test.go`.
Page 1 carries the standalone word "Inference" in its title, which the metadata test
smoke-checks for text extraction. Deliberately tiny and self-contained — not a snapshot
of the `web` showcase (the server package keeps that copy at
`internal/server/testdata/notes/others/paper.pdf`). Tests read `testdata/paper.pdf`
relative to the package dir; regenerate with any tool that produces a multi-page PDF
containing "Inference" on page 1.
