package render

import (
	"strings"

	"golang.org/x/net/html"
)

// TOCEntry is one heading in a page's table of contents. Level is the heading
// level (2 or 3); ID is the anchor goldmark's WithAutoHeadingID assigned to
// the heading; Text is the heading's plain-text content (inline markup
// stripped). Templates render `<a href="#{{.ID}}">{{.Text}}</a>`.
type TOCEntry struct {
	Level int
	ID    string
	Text  string
}

// ExtractTOC parses rendered page HTML and returns its h2/h3 headings in
// document order — the usual table-of-contents depth. Only headings carrying
// an id attribute are returned: without the anchor a ToC link has nowhere to
// point. h1 is excluded because it is the page title (rendered separately) and
// repeating it in the ToC is noise. Returns nil for empty input or a parse
// failure (a missing ToC is never a page error).
func ExtractTOC(fragment string) []TOCEntry {
	if strings.TrimSpace(fragment) == "" {
		return nil
	}
	doc, err := html.Parse(strings.NewReader(fragment))
	if err != nil {
		return nil
	}
	var out []TOCEntry
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			level := 0
			switch n.Data {
			case "h2":
				level = 2
			case "h3":
				level = 3
			}
			if level > 0 {
				if id := attrValue(n, "id"); id != "" {
					if text := strings.TrimSpace(nodeText(n)); text != "" {
						out = append(out, TOCEntry{Level: level, ID: id, Text: text})
					}
				}
				return // headings don't nest other headings
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

// attrValue returns the value of the named attribute, or "" if absent.
func attrValue(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// nodeText concatenates the text content of n's subtree.
func nodeText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.TextNode {
			sb.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return sb.String()
}
