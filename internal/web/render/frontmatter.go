// Package render handles markdown→HTML, text→<pre>, and asset content-type
// resolution.
package render

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// Frontmatter is the typed view of YAML frontmatter that templates consume
// directly, plus the full Raw map for theme code that needs arbitrary fields.
type Frontmatter struct {
	Title   string
	Aliases []string
	Tags    []string
	Raw     map[string]any
}

// ExtractFrontmatter splits src into (frontmatter, remaining body). A document
// without a leading fence is returned with zero-value Frontmatter and the
// original body. A fence opened but not closed is treated as no frontmatter.
//
// Fences accept 1+ leading dashes per the Obsidian convention.
func ExtractFrontmatter(src []byte) (Frontmatter, []byte, error) {
	open, openLen := readFence(src)
	if !open {
		return Frontmatter{}, src, nil
	}
	rest := src[openLen:]
	idx := findClosingFence(rest)
	if idx < 0 {
		return Frontmatter{}, src, nil
	}
	yamlBody := rest[:idx]
	bodyStart := idx + closingFenceLen(rest, idx)
	body := rest[bodyStart:]

	if len(bytes.TrimSpace(yamlBody)) == 0 {
		return Frontmatter{}, body, nil
	}
	raw := map[string]any{}
	if err := yaml.Unmarshal(yamlBody, &raw); err != nil {
		return Frontmatter{}, nil, fmt.Errorf("parse frontmatter: %w", err)
	}
	fm := Frontmatter{Raw: raw}
	if v, ok := raw["title"].(string); ok {
		fm.Title = v
	}
	if v, ok := raw["aliases"].([]any); ok {
		for _, x := range v {
			if s, ok := x.(string); ok {
				fm.Aliases = append(fm.Aliases, s)
			}
		}
	}
	if v, ok := raw["tags"].([]any); ok {
		for _, x := range v {
			if s, ok := x.(string); ok {
				fm.Tags = append(fm.Tags, s)
			}
		}
	}
	return fm, body, nil
}

// readFence returns (true, n) if src starts with a fence line ("-"+ followed
// by newline), where n is the number of bytes consumed by the fence + newline.
func readFence(src []byte) (bool, int) {
	dashes := 0
	for dashes < len(src) && src[dashes] == '-' {
		dashes++
	}
	if dashes == 0 {
		return false, 0
	}
	if dashes < len(src) && src[dashes] == '\n' {
		return true, dashes + 1
	}
	if dashes+1 < len(src) && src[dashes] == '\r' && src[dashes+1] == '\n' {
		return true, dashes + 2
	}
	return false, 0
}

// findClosingFence returns the byte index of the closing fence line in body,
// or -1 if no closing fence exists. The fence must be at the start of a line.
func findClosingFence(body []byte) int {
	for off := 0; off < len(body); {
		if ok, _ := readFence(body[off:]); ok {
			return off
		}
		nl := bytes.IndexByte(body[off:], '\n')
		if nl < 0 {
			return -1
		}
		off += nl + 1
	}
	return -1
}

// closingFenceLen returns the byte length of the fence line at body[idx:],
// including its trailing newline (so callers can skip past it cleanly).
func closingFenceLen(body []byte, idx int) int {
	_, n := readFence(body[idx:])
	return n
}
