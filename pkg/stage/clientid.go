package stage

import (
	"errors"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/pawlenartowicz/leyline/protocol/fileutil"
)

// EnsureClientID reads the UUID at path, generating one if absent. The
// file is 0600 — local-only state, never synced.
func EnsureClientID(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err == nil {
		s := strings.TrimSpace(string(data))
		if s == "" {
			// Treat as missing; fall through to generation below.
		} else {
			if _, perr := uuid.Parse(s); perr != nil {
				return "", errors.New("client_id file corrupt: not a UUID")
			}
			return s, nil
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	id := uuid.NewString()
	if err := fileutil.AtomicWrite(path, []byte(id), 0o600); err != nil {
		return "", err
	}
	return id, nil
}

// ResetClientID removes path (next EnsureClientID will generate a fresh
// one). Idempotent — missing file is not an error.
func ResetClientID(path string) error {
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
