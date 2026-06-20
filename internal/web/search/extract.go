package search

import (
	"regexp"
	"strings"

	"github.com/microcosm-cc/bluemonday"

	"github.com/pawlenartowicz/leyline/internal/web/render"
	"github.com/pawlenartowicz/leyline/internal/web/webignore"
)

// htmlStripPolicy is a bluemonday policy that strips all HTML tags but
// keeps text content. Identical allow-list semantics to the rendering
// path — built once since bluemonday.Policy is concurrent-safe after
// construction.
var htmlStripPolicy = bluemonday.StrictPolicy()

// Extracted holds the text segments pulled from a document for indexing.
// Title and Tags are indexed separately so query scoring can boost matches
// in those fields.
type Extracted struct {
	// Title holds the document title (from frontmatter or first H1).
	Title string
	// Tags holds space-joined tag strings (from frontmatter tags/aliases).
	Tags string
	// Body holds the main searchable text.
	Body string
}

// ExtractText derives indexable text from a file's raw bytes, keyed on the
// dispatch Mode. Only Markdown, Text, and HTML modes are indexed; other
// modes return a zero Extracted.
//
// path is the vault-relative path (used to dispatch on extension when the
// Mode is not already resolved). bytes are the raw file contents.
func ExtractText(path string, data []byte, mode webignore.Mode) Extracted {
	switch mode {
	case webignore.ModeMarkdown:
		return extractMarkdown(data)
	case webignore.ModeText:
		return Extracted{Body: string(data)}
	case webignore.ModeHTML:
		return extractHTML(data)
	default:
		return Extracted{}
	}
}

// extractMarkdown pulls frontmatter fields into Title/Tags and lightly
// strips the body markdown for the Body text. Code content and wikilink
// targets are kept; fence/heading/emphasis syntax is dropped.
func extractMarkdown(data []byte) Extracted {
	fm, body, err := render.ExtractFrontmatter(data)
	if err != nil {
		// Treat unparseable frontmatter as no frontmatter; index body raw.
		body = data
	}

	title := fm.Title
	// Append aliases to title so alias searches hit the title field.
	var titleParts []string
	if title != "" {
		titleParts = append(titleParts, title)
	}
	titleParts = append(titleParts, fm.Aliases...)
	titleText := strings.Join(titleParts, " ")

	tagsText := strings.Join(fm.Tags, " ")

	bodyText := stripMarkdownSyntax(string(body))
	return Extracted{
		Title: titleText,
		Tags:  tagsText,
		Body:  bodyText,
	}
}

// mdFenceRE matches fenced code blocks (``` or ~~~ with optional lang).
// We keep the code content but drop the fence delimiters.
var mdFenceRE = regexp.MustCompile("(?m)^[`~]{3,}[^\n]*\n((?:[^`~]|[`~]{1,2}[^`~])*?)^[`~]{3,}[^\n]*$")

// mdHeadingRE matches ATX headings (#, ##, …).
var mdHeadingRE = regexp.MustCompile(`(?m)^#{1,6}\s+`)

// mdEmphRE matches emphasis markers (* and _) and bold (**/__).
var mdEmphRE = regexp.MustCompile(`\*{1,3}|_{1,3}`)

// mdLinkRE matches [text](url) — keep the text, drop the URL.
var mdLinkRE = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)

// mdWikilinkRE matches [[target]] and [[target|alias]] — keep target or alias.
var mdWikilinkRE = regexp.MustCompile(`\[\[([^\]|]+)(?:\|([^\]]*))?\]\]`)

// mdImageRE matches ![alt](url) — keep the alt text.
var mdImageRE = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)

// stripMarkdownSyntax removes markdown formatting syntax while preserving
// the visible text, code content, and wikilink targets.
func stripMarkdownSyntax(md string) string {
	// Fenced code blocks: keep content, strip delimiters.
	s := mdFenceRE.ReplaceAllStringFunc(md, func(m string) string {
		sub := mdFenceRE.FindStringSubmatch(m)
		if len(sub) > 1 {
			return sub[1]
		}
		return ""
	})
	// Images: keep alt text.
	s = mdImageRE.ReplaceAllString(s, "$1 ")
	// Wikilinks: keep alias if present, otherwise target.
	s = mdWikilinkRE.ReplaceAllStringFunc(s, func(m string) string {
		sub := mdWikilinkRE.FindStringSubmatch(m)
		if len(sub) > 2 && sub[2] != "" {
			return sub[2] + " "
		}
		if len(sub) > 1 {
			return sub[1] + " "
		}
		return ""
	})
	// Standard links: keep text.
	s = mdLinkRE.ReplaceAllString(s, "$1 ")
	// Heading markers.
	s = mdHeadingRE.ReplaceAllString(s, "")
	// Emphasis markers.
	s = mdEmphRE.ReplaceAllString(s, "")
	return s
}

// extractHTML sanitizes the HTML (strip all tags, keep text) via bluemonday.
func extractHTML(data []byte) Extracted {
	text := htmlStripPolicy.SanitizeBytes(data)
	return Extracted{Body: string(text)}
}
