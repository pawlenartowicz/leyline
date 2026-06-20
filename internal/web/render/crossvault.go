package render

import (
	"regexp"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	"go.abhg.dev/goldmark/wikilink"
)

// crossVaultRE matches a wikilink target of the form `@<vault_id>` or
// `@<vault_id>/<path>`. The wikilink parser already strips the `#fragment`
// suffix into the Node's Fragment field, so the regex sees only the
// pre-fragment portion. vault_id matches the same shape as our YAML
// identifiers (alphanumeric, `_`, `-`; must start alphanumeric).
var crossVaultRE = regexp.MustCompile(`^@([a-zA-Z0-9][a-zA-Z0-9_-]*)(?:/(.*))?$`)

// crossVaultUnresolvedKind tags the custom AST node emitted when a
// `[[@vault/path]]` wikilink references an unknown vault. The renderer
// emits a visible `<span class="leyline-cross-vault is-unresolved">` so
// the author sees the broken reference without breaking page render.
var crossVaultUnresolvedKind = ast.NewNodeKind("LeylineCrossVaultUnresolved")

type crossVaultUnresolvedNode struct {
	ast.BaseInline
	VaultID string
}

func (*crossVaultUnresolvedNode) Kind() ast.NodeKind { return crossVaultUnresolvedKind }

func (n *crossVaultUnresolvedNode) Dump(src []byte, level int) {
	ast.DumpHelper(n, src, level, map[string]string{"VaultID": n.VaultID}, nil)
}

// crossVaultTransformer walks the inline AST and rewrites every
// `*wikilink.Node` whose Target begins with `@`. Known vault IDs produce
// a regular `*ast.Link`; unknown vault IDs produce a `crossVaultUnresolved`
// node. Malformed `@`-targets (regex miss) are left alone — the wikilink
// renderer falls through to its plain-text path, making the typo visible.
//
// The transformer reads the active `VaultID` and `IDMap` from URLContext.
// When IDMap is empty or VaultID is unset, every `@`-wikilink turns into
// an unresolved span (or is left alone if no IDMap), which is the
// expected behaviour for vaults that haven't been wired into the
// operator's idMap yet.
type crossVaultTransformer struct{}

func (crossVaultTransformer) Transform(doc *ast.Document, _ text.Reader, pCtx parser.Context) {
	raw := pCtx.Get(urlContextKey)
	if raw == nil {
		return
	}
	urlCtx, ok := raw.(URLContext)
	if !ok {
		return
	}
	idMap := urlCtx.IDMap
	currentVault := urlCtx.VaultID

	type replacement struct {
		parent ast.Node
		old    ast.Node
		new    ast.Node
	}
	var replacements []replacement

	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		wn, ok := n.(*wikilink.Node)
		if !ok {
			return ast.WalkContinue, nil
		}
		target := string(wn.Target)
		if !strings.HasPrefix(target, "@") {
			return ast.WalkContinue, nil
		}
		match := crossVaultRE.FindStringSubmatch(target)
		if match == nil {
			// Malformed `@vault` syntax — leave the wikilink alone so the
			// renderer surfaces the typo as plain text.
			return ast.WalkContinue, nil
		}
		vaultID := match[1]
		path := ""
		if len(match) >= 3 {
			path = match[2]
		}
		fragment := string(wn.Fragment)

		prefix, known := idMap[vaultID]
		if !known {
			// Unknown vault: substitute with an unresolved-span node.
			replacement := &crossVaultUnresolvedNode{VaultID: vaultID}
			adoptChildren(replacement, wn)
			replacements = append(replacements, struct {
				parent ast.Node
				old    ast.Node
				new    ast.Node
			}{wn.Parent(), wn, replacement})
			return ast.WalkSkipChildren, nil
		}

		// Known vault. Same-vault collapse: drop the prefix so URLs stay
		// clean and survive operator-side remounts.
		if vaultID == currentVault {
			prefix = ""
		}
		href := buildCrossVaultHref(prefix, path, fragment)

		link := ast.NewLink()
		link.Destination = []byte(href)
		link.SetAttributeString("class", []byte("leyline-cross-vault"))
		adoptChildren(link, wn)
		replacements = append(replacements, struct {
			parent ast.Node
			old    ast.Node
			new    ast.Node
		}{wn.Parent(), wn, link})
		return ast.WalkSkipChildren, nil
	})

	for _, r := range replacements {
		if r.parent == nil {
			continue
		}
		r.parent.ReplaceChild(r.parent, r.old, r.new)
	}
}

// adoptChildren moves every child of src onto dst so the replacement node
// inherits the wikilink's label children. The wikilink parser always
// appends a single Text segment carrying either the alias label or the
// implicit target text (parser.go:88), which is what we want to surface.
func adoptChildren(dst, src ast.Node) {
	for c := src.FirstChild(); c != nil; {
		next := c.NextSibling()
		src.RemoveChild(src, c)
		dst.AppendChild(dst, c)
		c = next
	}
}

func buildCrossVaultHref(prefix, path, fragment string) string {
	// Defensive guard: IDMap values should always start with "/" (vault
	// registry enforces this), but defend against a future loosening or a
	// misconfigured test fixture producing a scheme-prefixed href.
	if prefix != "" && !strings.HasPrefix(prefix, "/") {
		// Fall back to a root-relative href so we never emit an open redirect.
		prefix = "/"
	}
	href := joinVaultURL(prefix, path)
	if fragment != "" {
		href += "#" + fragment
	}
	return href
}

// crossVaultUnresolvedRenderer emits `<span class="leyline-cross-vault
// is-unresolved" title="unknown vault: …">{children}</span>` for the
// custom AST node. The renderer walks children so the wikilink's label
// text comes through unmodified.
type crossVaultUnresolvedRenderer struct{}

func (crossVaultUnresolvedRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(crossVaultUnresolvedKind, renderCrossVaultUnresolved)
}

func renderCrossVaultUnresolved(w util.BufWriter, _ []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	cv := n.(*crossVaultUnresolvedNode)
	if entering {
		_, _ = w.WriteString(`<span class="leyline-cross-vault is-unresolved" title="unknown vault: `)
		_, _ = w.WriteString(escapeAttr(cv.VaultID))
		_, _ = w.WriteString(`">`)
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString(`</span>`)
	return ast.WalkContinue, nil
}

// escapeAttr provides a minimal HTML-attribute escape for the unknown
// vault ID. The vault-ID regex already restricts the character set to
// `[a-zA-Z0-9_-]`, so this is defensive; the helper exists so the path
// stays correct if the regex is loosened later.
func escapeAttr(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '"':
			b.WriteString("&quot;")
		case '&':
			b.WriteString("&amp;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
