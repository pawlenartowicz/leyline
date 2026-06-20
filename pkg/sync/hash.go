package sync

import (
	protocol "github.com/pawlenartowicz/leyline/protocol"
)

// HashBytes returns the SHA-256 of b as a protocol.Hash (raw 32 bytes).
func HashBytes(b []byte) protocol.Hash {
	return protocol.HashBytes(b)
}
