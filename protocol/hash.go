package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// HashSize is the byte length of a content hash (SHA-256).
const HashSize = 32

// Hash is a fixed-length content hash. On the wire it travels as a 32-byte
// CBOR byte string; in Go it is comparable and map-keyable. The zero value
// is the sentinel "unknown / not yet hashed".
type Hash [HashSize]byte

// HashBytes computes the SHA-256 of data.
func HashBytes(data []byte) Hash {
	return sha256.Sum256(data)
}

// IsZero reports whether h is the all-zero sentinel.
func (h Hash) IsZero() bool {
	return h == Hash{}
}

// Hex returns the lowercase hex encoding (64 chars). Used at display
// boundaries — logs, CLI output, error messages.
func (h Hash) Hex() string {
	return hex.EncodeToString(h[:])
}

// String aliases Hex for fmt.Stringer.
func (h Hash) String() string { return h.Hex() }

// ParseHashHex decodes a 64-character lowercase hex string. Empty input
// returns the zero Hash (treated as sentinel by callers).
func ParseHashHex(s string) (Hash, error) {
	var h Hash
	if s == "" {
		return h, nil
	}
	if len(s) != hex.EncodedLen(HashSize) {
		return h, fmt.Errorf("hash: expected %d hex chars, got %d", hex.EncodedLen(HashSize), len(s))
	}
	if _, err := hex.Decode(h[:], []byte(s)); err != nil {
		return h, fmt.Errorf("hash: invalid hex: %w", err)
	}
	return h, nil
}

// MarshalCBOR emits a 32-byte CBOR byte string. Default reflection would
// encode the underlying [32]byte as a CBOR array of 32 small ints — wrong
// for our purposes.
func (h Hash) MarshalCBOR() ([]byte, error) {
	return cbor.Marshal(h[:])
}

// UnmarshalCBOR accepts a CBOR byte string of exactly HashSize bytes.
func (h *Hash) UnmarshalCBOR(data []byte) error {
	var b []byte
	if err := cbor.Unmarshal(data, &b); err != nil {
		return fmt.Errorf("hash cbor: %w", err)
	}
	if len(b) != HashSize {
		return fmt.Errorf("hash: expected %d bytes, got %d", HashSize, len(b))
	}
	copy(h[:], b)
	return nil
}

// MarshalText encodes the hash as lowercase hex. JSON encoders use this
// automatically; that lets state files (state.json, data.json) stay
// human-readable while the wire stays raw bytes.
func (h Hash) MarshalText() ([]byte, error) {
	if h.IsZero() {
		return []byte{}, nil
	}
	out := make([]byte, hex.EncodedLen(HashSize))
	hex.Encode(out, h[:])
	return out, nil
}

// UnmarshalText accepts hex (the format MarshalText produces) or empty.
func (h *Hash) UnmarshalText(text []byte) error {
	if len(text) == 0 {
		*h = Hash{}
		return nil
	}
	parsed, err := ParseHashHex(string(text))
	if err != nil {
		return err
	}
	*h = parsed
	return nil
}
