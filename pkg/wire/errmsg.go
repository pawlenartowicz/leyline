package wire

import protocol "github.com/pawlenartowicz/leyline/protocol"

// FriendlyMessage maps a wire error (code, message) pair into a string suitable
// for user-facing output. Known codes get a tailored replacement; unknown
// codes fall through to the raw server message.
func FriendlyMessage(code, message string) string {
	switch code {
	case protocol.ErrVaultFull:
		return "Vault size limit reached. Ask the vault operator to raise the cap."
	default:
		return message
	}
}
