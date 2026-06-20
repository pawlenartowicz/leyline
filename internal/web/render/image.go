package render

import (
	"path/filepath"
	"strings"
)

// ContentType returns the MIME type for the given filename based solely on
// the extension. Returns "" for unknown extensions; the caller should treat
// unknown as 404 rather than letting net/http sniff the body.
func ContentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".pdf":
		return "application/pdf"
	case ".mjs":
		// ES module scripts must be served with a JavaScript MIME or
		// browsers refuse to execute them. http.ServeFile's sniff path
		// returns text/plain for .mjs, so the lookup table answers
		// explicitly here.
		return "application/javascript; charset=utf-8"
	case ".woff2":
		return "font/woff2"
	}
	return ""
}
