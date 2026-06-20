package protocol

//go:generate go run ./testdata/wire/gen

// ProtocolVersion is the wire-format version. The wire *is* the version —
// no in-frame version field, no negotiation. v1 frames carry IDs 30–38
// alongside the auth/ping/error/tag IDs (1–3, 20–24).
const ProtocolVersion = 1

// MsgType is the on-wire numeric type tag for every frame. Sent at envelope
// map key 0. IDs are immutable once assigned: deprecated types keep their
// number reserved; new types append.
type MsgType uint8

const (
	// Auth + transport.
	MsgAuth     MsgType = 1
	MsgAuthOK   MsgType = 2
	MsgAuthFail MsgType = 3
	MsgPing     MsgType = 20
	MsgPong     MsgType = 21
	MsgError    MsgType = 22
	// Broadcast-style tag frames.
	MsgTagCreated MsgType = 23
	MsgTagDeleted MsgType = 24

	// Client → server.
	MsgHello     MsgType = 30
	MsgPushBatch MsgType = 31
	MsgFlush     MsgType = 32

	// Server → client.
	MsgHelloOK   MsgType = 33
	MsgCatchup   MsgType = 34
	MsgBootstrap MsgType = 35
	MsgPushAck   MsgType = 36
	MsgBroadcast MsgType = 37
	MsgFlushAck  MsgType = 38
)

// HelloOK state values — what the server tells the client to do next.
const (
	HelloStateUpToDate  = "up_to_date"
	HelloStateCatchup   = "catchup"
	HelloStateBootstrap = "bootstrap"
	HelloStateBaseLost  = "base_lost"
)

// PushAck result values.
const (
	PushAckOK        = "ok"
	PushAckStaleBase = "stale_base"
)

// OpType values for Op.Type. The wire carries these as short strings (CBOR
// tstr); Go callers use the typed constants.
const (
	OpWrite  = "write"
	OpDelete = "delete"
	OpRename = "rename"
)

// Role constants.
const (
	RoleAdmin  = "admin"
	RoleEditor = "editor"
	RoleReader = "reader"
)

// Error code constants — string tokens sent in MsgError and PushAck frames.
const (
	ErrFileTooLarge      = "file_too_large"
	ErrFileNotFound      = "file_not_found"
	ErrRateLimited       = "rate_limited"
	ErrVaultNotFound     = "vault_not_found"
	ErrPermissionDenied  = "permission_denied"
	ErrInvalidPath       = "invalid_path"
	ErrDiskWriteFailed   = "disk_write_failed"
	ErrInvalidData       = "invalid_data"
	ErrServerError       = "server_error"
	ErrFileAlreadyExists = "file_already_exists"
	ErrTypeNotAllowed    = "type_not_allowed"
	ErrStuckFile         = "stuck_file"
	ErrVaultFull         = "vault_full"
)

// CloseReasonProtocolMismatch is sent in the WS close frame (code 1002) when
// any received frame fails to decode as a v1 CBOR envelope, or carries an
// unknown MsgType at any point in the session.
const CloseReasonProtocolMismatch = "expected CBOR (v1 wire)"

// String maps a MsgType to its canonical name (e.g. MsgAuthOK → "AuthOK").
// Used by structured logs across server, CLI, and tests. Unknown IDs
// render as "MsgType(<n>)" — keeps log lines parseable even when a peer
// sends a frame this build doesn't know.
func (t MsgType) String() string {
	switch t {
	case MsgAuth:
		return "Auth"
	case MsgAuthOK:
		return "AuthOK"
	case MsgAuthFail:
		return "AuthFail"
	case MsgPing:
		return "Ping"
	case MsgPong:
		return "Pong"
	case MsgError:
		return "Error"
	case MsgTagCreated:
		return "TagCreated"
	case MsgTagDeleted:
		return "TagDeleted"
	case MsgHello:
		return "Hello"
	case MsgPushBatch:
		return "PushBatch"
	case MsgFlush:
		return "Flush"
	case MsgHelloOK:
		return "HelloOK"
	case MsgCatchup:
		return "Catchup"
	case MsgBootstrap:
		return "Bootstrap"
	case MsgPushAck:
		return "PushAck"
	case MsgBroadcast:
		return "Broadcast"
	case MsgFlushAck:
		return "FlushAck"
	default:
		return msgTypeUnknownLabel(t)
	}
}

func msgTypeUnknownLabel(t MsgType) string {
	const digits = "0123456789"
	if t < 10 {
		return "MsgType(" + string(digits[t]) + ")"
	}
	if t < 100 {
		return "MsgType(" + string(digits[t/10]) + string(digits[t%10]) + ")"
	}
	return "MsgType(" + string(digits[t/100]) + string(digits[(t/10)%10]) + string(digits[t%10]) + ")"
}

// IsKnownErrCode reports whether code is one of the documented Err* values
// the protocol defines. Receive paths can use this to detect a peer sending
// an undocumented error code — useful for catching server-side typos that
// would otherwise leak through to the client error-mapping layer silently.
func IsKnownErrCode(code string) bool {
	switch code {
	case ErrFileTooLarge, ErrFileNotFound, ErrRateLimited, ErrVaultNotFound,
		ErrPermissionDenied, ErrInvalidPath, ErrDiskWriteFailed, ErrInvalidData,
		ErrServerError, ErrFileAlreadyExists, ErrTypeNotAllowed, ErrStuckFile,
		ErrVaultFull:
		return true
	}
	return false
}

// Review-tag conventions. Naming:
//   `reviewed-<RFC3339-with-dashes-instead-of-colons>`
// e.g. reviewed-2026-05-18T15-04-05Z. The `-` substitution is because `:`
// is forbidden in git refnames; the format roundtrips via Format/Parse
// helpers below. Used by server commit naming + CLI/plugin display.
const (
	ReviewTagPrefix     = "reviewed-"
	ReviewTagTimeLayout = "2006-01-02T15-04-05Z"
)
