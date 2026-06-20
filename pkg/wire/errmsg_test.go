package wire

import (
	"strings"
	"testing"

	protocol "github.com/pawlenartowicz/leyline/protocol"
)

func TestFriendlyMessage(t *testing.T) {
	cases := []struct {
		code       string
		message    string
		wantSubstr string
	}{
		{protocol.ErrVaultFull, "vault file-count limit reached", "Vault size limit"},
		{"some_unknown", "raw", "raw"},
		{protocol.ErrServerError, "internal failure", "internal failure"},
	}
	for _, tc := range cases {
		got := FriendlyMessage(tc.code, tc.message)
		if !strings.Contains(got, tc.wantSubstr) {
			t.Errorf("FriendlyMessage(%q, %q) = %q, want substring %q",
				tc.code, tc.message, got, tc.wantSubstr)
		}
	}
}

func TestFriendlyMessage_AllProtocolCodes(t *testing.T) {
	// For each protocol error code: either a specific friendly mapping
	// exists (non-empty result different from the raw message) OR the
	// code passes through to the raw message unchanged. Either way the
	// result must be non-empty and must not panic.
	const rawMsg = "raw server message"
	allCodes := []string{
		protocol.ErrFileTooLarge,
		protocol.ErrFileNotFound,
		protocol.ErrRateLimited,
		protocol.ErrVaultNotFound,
		protocol.ErrPermissionDenied,
		protocol.ErrInvalidPath,
		protocol.ErrDiskWriteFailed,
		protocol.ErrInvalidData,
		protocol.ErrServerError,
		protocol.ErrFileAlreadyExists,
		protocol.ErrTypeNotAllowed,
		protocol.ErrStuckFile,
		protocol.ErrVaultFull,
	}
	for _, code := range allCodes {
		t.Run(code, func(t *testing.T) {
			got := FriendlyMessage(code, rawMsg)
			if got == "" {
				t.Errorf("FriendlyMessage(%q, %q) returned empty string", code, rawMsg)
			}
			// vault_full is the only specifically mapped code; all others pass through.
			if code == protocol.ErrVaultFull {
				if strings.Contains(got, rawMsg) {
					t.Errorf("vault_full should have a custom message, but got raw: %q", got)
				}
			} else {
				if got != rawMsg {
					t.Errorf("unmapped code %q: expected pass-through %q, got %q", code, rawMsg, got)
				}
			}
		})
	}
}
