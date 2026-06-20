// Package admin implements the `leyline admin` subcommand: HTTPS operator
// surface, talking to leyline-server's /_leyline/operator/* and /_leyline/admin/{vault}/*
// routes. Keystore is ~/.config/leyline/keys, columnar:
//
//	<host>/<vaultID>  ley_xxx  [keyname]
//
// File permissions on the keystore are the auth boundary — mode must be 0600.
package admin

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/pawlenartowicz/leyline/protocol/vaultaddr"
)

// KeyRow is one parsed line of ~/.config/leyline/keys.
type KeyRow struct {
	Vault string // canonical <host>/<vaultID>
	Key   string // ley_xxx
	Name  string // optional keyname; empty when row has "-" or omits the column
}

// LoadKeystore reads path. A missing file returns an empty slice with no
// error (keystore is optional; --key bypasses it).
// Returns an error if the file has permissions wider than 0600 — the keystore
// contains cleartext API keys, and file permissions are the auth boundary.
func LoadKeystore(path string) ([]KeyRow, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open keystore: %w", err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat keystore: %w", err)
	}
	if perm := info.Mode().Perm(); perm&0o177 != 0 {
		return nil, fmt.Errorf("keystore %s has unsafe permissions %04o (want 0600): fix with: chmod 600 %s", path, perm, path)
	}

	var rows []KeyRow
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		name := ""
		if len(fields) >= 3 && fields[2] != "-" {
			name = fields[2]
		}
		rows = append(rows, KeyRow{Vault: fields[0], Key: fields[1], Name: name})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read keystore: %w", err)
	}
	return rows, nil
}

// hostOf extracts the host portion of a canonical <host>/<vaultID> vault
// address. Returns "" when the address is malformed.
func hostOf(vault string) string {
	host, _, err := vaultaddr.Parse(vault)
	if err != nil {
		return ""
	}
	return host
}

// vaultIDOf extracts the bare vault ID from a canonical <host>/<vaultID>.
// Returns "" when the address is malformed.
func vaultIDOf(vault string) string {
	_, id, err := vaultaddr.Parse(vault)
	if err != nil {
		return ""
	}
	return id
}

// hostOfServerURL extracts the host from a https://... URL.
func hostOfServerURL(serverURL string) string {
	u, err := url.Parse(serverURL)
	if err != nil {
		return ""
	}
	return u.Host
}
