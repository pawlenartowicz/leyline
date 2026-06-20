package render

import (
	"bufio"
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"strings"
)

// ParseCustomNavFile reads a hand-curated nav file and returns the top-level
// NavNodes.
//
// Tokenization is quote-aware: `#` starts a comment only outside quotes;
// `:` separates title from link only outside quotes. A line beginning
// (after optional whitespace) with `-` is a child of the most recent
// top-level item. Depth is capped at 1: children may not have children.
//
// Malformed lines (missing closing quote, embedded `"`, child without a
// parent, child without a link, etc.) are skipped with a warn-level log
// that includes file path + line number. The function therefore never
// returns an error for malformed content; it only errors on I/O failures
// reading the file.
//
// vaultPrefix is the vault's URL mount prefix ("/" for the root vault),
// used to absolutise vault-relative link targets such as `notes/foo.md`.
// External URLs (containing `://`), cross-vault refs (starting `@`), and
// anchors (starting `#`) are passed through untouched. Wikilinks
// (`[[note]]` or `[[note#section]]`) are resolved against the supplied
// resolver and basename index when non-nil; on resolver miss the link
// degrades to plain text (URL becomes empty) so the broken target stays
// visible to the author.
//
// idMap, if non-nil, lets the parser absolutise cross-vault `@vaultID/path`
// targets in nav files. Unknown vault IDs render as plain text (empty URL).
// The crossVaultTransformer handles the same shape in body content; nav links
// re-use the same lookup so authors can mix both.
func ParseCustomNavFile(
	path, vaultPrefix string,
	resolver WikilinkResolver,
	idMap map[string]string,
	logger *slog.Logger,
) ([]*NavNode, error) {
	if logger == nil {
		logger = slog.Default()
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read nav file %s: %w", path, err)
	}
	return parseCustomNav(data, path, vaultPrefix, resolver, idMap, logger), nil
}

func parseCustomNav(
	data []byte,
	path, vaultPrefix string,
	resolver WikilinkResolver,
	idMap map[string]string,
	logger *slog.Logger,
) []*NavNode {
	var out []*NavNode
	var lastTop *NavNode
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 1<<20)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		trim := strings.TrimSpace(raw)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		isChild := strings.HasPrefix(trim, "-")
		if isChild {
			trim = strings.TrimSpace(trim[1:])
		}
		title, link, err := parseNavLine(trim)
		if err != nil {
			logger.Warn("nav file: skipping malformed line",
				"file", path, "line", lineNo, "err", err.Error())
			continue
		}
		if isChild {
			if lastTop == nil {
				logger.Warn("nav file: child line before any top-level item",
					"file", path, "line", lineNo)
				continue
			}
			if link == "" {
				logger.Warn("nav file: child requires a link target",
					"file", path, "line", lineNo)
				continue
			}
			url := resolveNavTarget(link, vaultPrefix, resolver, idMap)
			lastTop.Children = append(lastTop.Children, &NavNode{
				Title: title,
				URL:   url,
			})
			continue
		}
		var url string
		if link != "" {
			url = resolveNavTarget(link, vaultPrefix, resolver, idMap)
		}
		node := &NavNode{
			Title:      title,
			URL:        url,
			ParentOnly: link == "",
		}
		out = append(out, node)
		lastTop = node
	}
	if err := scanner.Err(); err != nil {
		logger.Warn("nav file: scan error",
			"file", path, "err", err.Error())
	}
	return out
}

// parseNavLine extracts the quoted title and (optional) quoted link from a
// single line that has already had its child `-` prefix and surrounding
// whitespace stripped. Errors describe the parse problem without quoting
// the line (the caller logs file + line number for context).
func parseNavLine(line string) (title, link string, err error) {
	rest := line
	title, rest, err = takeQuoted(rest)
	if err != nil {
		return "", "", err
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return title, "", nil
	}
	if rest[0] != ':' {
		return "", "", fmt.Errorf("expected ':' after title, got %q", rest[:1])
	}
	rest = strings.TrimSpace(rest[1:])
	if rest == "" {
		return "", "", fmt.Errorf("expected link target after ':'")
	}
	link, rest, err = takeQuoted(rest)
	if err != nil {
		return "", "", err
	}
	rest = strings.TrimSpace(rest)
	if rest != "" {
		return "", "", fmt.Errorf("unexpected trailing characters after link")
	}
	return title, link, nil
}

// takeQuoted scans a leading `"..."` token from s. The interior must not
// contain a `"` (no escapes in V1). Returns the unquoted value, the
// remainder of s after the closing quote, or an error if the token is
// malformed.
func takeQuoted(s string) (value, rest string, err error) {
	if len(s) == 0 || s[0] != '"' {
		return "", "", fmt.Errorf("expected '\"' to start a quoted token")
	}
	for i := 1; i < len(s); i++ {
		if s[i] == '"' {
			return s[1:i], s[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("unterminated quoted token")
}

// resolveNavTarget maps a nav-file link target to an emit-ready URL.
//
//   - External URLs (containing `://`) and `mailto:` pass through.
//   - Anchor-only targets (`#section`) pass through.
//   - Cross-vault targets (`@vaultID[/path][#section]`) resolve against
//     idMap; unknown vaults degrade to empty (link is rendered as text).
//   - Wikilink targets (`[[note]]` or `[[note#section]]`) resolve against
//     the supplied resolver; misses degrade to empty.
//   - Anything else is treated as a vault-relative path; the vault prefix
//     is prepended unless the path already starts with `/`.
func resolveNavTarget(
	target, vaultPrefix string,
	resolver WikilinkResolver,
	idMap map[string]string,
) string {
	t := strings.TrimSpace(target)
	if t == "" {
		return ""
	}
	if strings.Contains(t, "://") || strings.HasPrefix(t, "mailto:") {
		return t
	}
	if strings.HasPrefix(t, "#") {
		return t
	}
	if strings.HasPrefix(t, "@") {
		return resolveCrossVaultNavTarget(t, idMap)
	}
	if strings.HasPrefix(t, "[[") && strings.HasSuffix(t, "]]") {
		inner := t[2 : len(t)-2]
		var fragment string
		if i := strings.Index(inner, "#"); i >= 0 {
			fragment = inner[i+1:]
			inner = inner[:i]
		}
		if resolver == nil {
			return ""
		}
		url, ok := resolver.Resolve(inner)
		if !ok {
			return ""
		}
		if fragment != "" {
			return url + "#" + fragment
		}
		return url
	}
	if strings.HasPrefix(t, "/") {
		return t
	}
	return joinVaultURL(vaultPrefix, t)
}

func resolveCrossVaultNavTarget(target string, idMap map[string]string) string {
	body := strings.TrimPrefix(target, "@")
	var fragment string
	if i := strings.Index(body, "#"); i >= 0 {
		fragment = body[i+1:]
		body = body[:i]
	}
	vaultID := body
	path := ""
	if i := strings.Index(body, "/"); i >= 0 {
		vaultID = body[:i]
		path = body[i+1:]
	}
	if vaultID == "" {
		return ""
	}
	prefix, ok := idMap[vaultID]
	if !ok {
		return ""
	}
	url := joinVaultURL(prefix, path)
	if fragment != "" {
		url += "#" + fragment
	}
	return url
}

// joinVaultURL combines a vault prefix ("/" or "/notes") with a
// vault-relative path ("about", "about/index", ""). The trailing-slash
// rules follow the wikilink resolver: a bare prefix root collapses to
// the prefix without a trailing slash.
func joinVaultURL(prefix, path string) string {
	path = strings.TrimPrefix(path, "/")
	if prefix == "" || prefix == "/" {
		if path == "" {
			return "/"
		}
		return "/" + path
	}
	if path == "" {
		return prefix + "/"
	}
	return prefix + "/" + path
}
