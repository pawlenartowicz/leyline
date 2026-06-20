// Package urlx parses inbound URLs into (vault, version, path) tuples and
// holds the canonicalisation/validation helpers that turn pretty URLs into
// filesystem reads safely.
package urlx

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/pawlenartowicz/leyline/internal/web/vault"
)

// tagNameRE is the on-the-wire validator for `@<tag>` URL segments. Tags
// that fail this pattern are not exposed by the switcher and an invalid
// `@<tag>` URL segment maps to a 404 at the dispatch layer. The pattern is
// deliberately narrower than git's `git check-ref-format` rules: ASCII
// alphanumerics plus `.`, `_`, `-`. Real-world tag names (`v1.0`,
// `reviewed-2026-05-12T14-30-00Z`) all pass.
var tagNameRE = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Parser carries the vault registry so handlers can call the spec-shaped
// `ParseURL(u string)` seam without threading the registry through every
// signature. The registry is read-only after construction; Parser is safe
// for concurrent use.
type Parser struct {
	reg *vault.Registry
}

// NewParser binds a registry into a Parser.
func NewParser(reg *vault.Registry) *Parser { return &Parser{reg: reg} }

// ParseURL is the single seam through which all inbound HTTP request paths
// flow. Returns the matched vault, an optional version selector (the bare
// tag name from `@<tag>`, or `"head"` for the explicit-filesystem selector),
// and the intra-vault path with the `@<tag>` segment (if any) stripped.
//
// Tag-name validation: only the position-1 segment is interpreted as a
// version selector. Within a path, `@` is allowed normally. An invalid
// `@<tag>` segment returns an error (the dispatch layer renders 404).
func (p *Parser) ParseURL(urlPath string) (vault.Vault, *string, string, error) {
	if urlPath == "" {
		return vault.Vault{}, nil, "", fmt.Errorf("empty URL path")
	}
	if !strings.HasPrefix(urlPath, "/") {
		return vault.Vault{}, nil, "", fmt.Errorf("URL path must be absolute")
	}
	// Reject protocol-relative URLs (//host/path) which can be used for
	// open-redirect or XSS attacks if a constructed URL is ever placed in
	// an href without further validation. Two leading slashes after the
	// first slash is the tell: //<something> that starts with a host name.
	if strings.HasPrefix(urlPath, "//") {
		return vault.Vault{}, nil, "", fmt.Errorf("protocol-relative URL path not allowed")
	}
	v, sub, ok := p.reg.Match(urlPath)
	if !ok {
		return vault.Vault{}, nil, "", fmt.Errorf("no vault registered for URL %q", urlPath)
	}
	first, rest, hasRest := strings.Cut(sub, "/")
	if !strings.HasPrefix(first, "@") {
		return v, nil, sub, nil
	}
	tag := first[1:]
	if !tagNameRE.MatchString(tag) {
		return vault.Vault{}, nil, "", fmt.Errorf("invalid tag name %q in URL", tag)
	}
	if !hasRest {
		rest = ""
	}
	return v, &tag, rest, nil
}
