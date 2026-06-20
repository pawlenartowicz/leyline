// Package vaultaddr parses and formats the canonical vault address form
// `<host>/<vaultID>` used everywhere in leyline. There is no protocol prefix
// — neither `wss://` nor `https://` — on a vault address: the host carries
// no scheme.
//
// Parse accepts the canonical bare form along with lenient inputs that may
// include a scheme (wss:// ws:// https:// http://), trailing slash, query
// string, or fragment — anything the user might paste from a browser. It
// returns the host and vault ID separately so callers don't slice strings
// by hand.
//
// DialURL and APIURL are the two URL forms the rest of the codebase needs:
//   - DialURL → wss://<host>/_leyline/sync/<vaultID> (WebSocket connect)
//   - APIURL  → https://<host>              (REST base URL)
package vaultaddr

import (
	"errors"
	"fmt"
	"strings"

	"github.com/pawlenartowicz/leyline/protocol/pathutil"
)

// ErrEmpty is returned by Parse when the input is empty after trimming.
var ErrEmpty = errors.New("empty vault address")

// schemePrefixes lists every scheme Parse strips. Order matters only in that
// `wss://` and `https://` (longer) come before `ws://` / `http://` so the
// HasPrefix walk doesn't match the short form first.
var schemePrefixes = []string{"wss://", "ws://", "https://", "http://"}

// Parse normalizes a vault address and splits it into host + vaultID.
//
// On success, host is non-empty and vaultID is a syntactically valid
// vault id per pathutil.ValidateVaultID. The returned values are safe to
// re-assemble via Format.
//
// Note: Parse does NOT verify the host (DNS, port, etc.) — only that it is
// non-empty after scheme stripping. Vault-id validation is structural only;
// the server may still reject an unknown id at handshake time.
func Parse(s string) (host, vaultID string, err error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", "", ErrEmpty
	}
	for _, p := range schemePrefixes {
		if strings.HasPrefix(s, p) {
			s = s[len(p):]
			break
		}
	}
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimRight(s, "/")
	slash := strings.Index(s, "/")
	if slash <= 0 || slash == len(s)-1 {
		return "", "", fmt.Errorf("vault address must be host/vaultID, got %q", s)
	}
	host = s[:slash]
	vaultID = s[slash+1:]
	if strings.Contains(vaultID, "/") {
		return "", "", fmt.Errorf("vault address must be host/vaultID (single slash), got %q", s)
	}
	if err := pathutil.ValidateVaultID(vaultID); err != nil {
		return "", "", fmt.Errorf("vault id: %w", err)
	}
	return host, vaultID, nil
}

// Format reassembles a canonical vault address. Caller is responsible for
// having already validated the inputs (or constructed them from Parse).
func Format(host, vaultID string) string {
	return host + "/" + vaultID
}

// Normalize is the round-trip Parse + Format — returns the canonical bare
// form regardless of how the input was presented.
func Normalize(s string) (string, error) {
	host, vaultID, err := Parse(s)
	if err != nil {
		return "", err
	}
	return Format(host, vaultID), nil
}

// DialURL returns the WebSocket dial URL for a vault address.
func DialURL(addr string) (string, error) {
	host, vaultID, err := Parse(addr)
	if err != nil {
		return "", err
	}
	return "wss://" + host + "/_leyline/sync/" + vaultID, nil
}

// APIURL returns the REST API base for a vault address (scheme + host, no
// path). Add `/_leyline/api/v1/<vault>/...` (or `/_leyline/admin/<vault>/...`)
// at the call site.
func APIURL(addr string) (string, error) {
	host, _, err := Parse(addr)
	if err != nil {
		return "", err
	}
	return "https://" + host, nil
}
