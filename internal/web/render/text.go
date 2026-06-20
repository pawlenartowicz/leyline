package render

// RenderText renders a plain-text vault file (.py, .sh, .json, …) as syntax-
// highlighted HTML. The filename drives lexer selection (extension match
// only — no content sniffing); unrecognised extensions and inputs over the
// 1 MiB cap fall through to a bare html-escaped <pre>. Output is class-only
// so theme palette swaps stay cache-compatible — see syntax.go for the
// shared chroma plumbing.
func RenderText(content []byte, filename string) string {
	return highlightFile(content, filename)
}
