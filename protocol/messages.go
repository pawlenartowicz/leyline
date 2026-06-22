package protocol

import (
	"fmt"

	"github.com/fxamacker/cbor/v2"
)

// Envelope is a peek-only header used to dispatch on Type before decoding
// the full frame. Key 0 carries the numeric MsgType; other keys are
// ignored at this stage.
type Envelope struct {
	Type MsgType `cbor:"0,keyasint"`
}

// --- Auth + transport frames ---

// AuthMsg. ClientID is a per-installation UUID the server keys its
// per-client stage and idempotency cache on; independent of the
// human-readable keyname (which travels via Key + the audit log).
type AuthMsg struct {
	Type          MsgType `cbor:"0,keyasint"`
	Key           string  `cbor:"1,keyasint"`
	PluginVersion string  `cbor:"2,keyasint"`
	ClientID      string  `cbor:"3,keyasint"`
}

// AuthOKMsg carries the handshake metadata the plugin UI and CLI auth
// log depend on (VaultID, Label, Name, ServerVersion, MinPluginVersion,
// PingInterval, PingTimeout) plus Role + Caps for authorization, and
// Head — the server's HEAD at auth time, so the client can compare
// against its local Base before issuing Hello.
type AuthOKMsg struct {
	Type             MsgType  `cbor:"0,keyasint"`
	VaultID          string   `cbor:"1,keyasint"`
	Label            string   `cbor:"2,keyasint"`
	Name             string   `cbor:"3,keyasint"`
	Role             string   `cbor:"4,keyasint"`
	ServerVersion    string   `cbor:"5,keyasint"`
	MinPluginVersion string   `cbor:"6,keyasint"`
	PingInterval     int      `cbor:"7,keyasint"`
	PingTimeout      int      `cbor:"8,keyasint"`
	Caps             []string `cbor:"9,keyasint"`
	Head             Hash     `cbor:"10,keyasint"`
}

// AuthFailMsg. MinVersion lets a too-old plugin surface "upgrade to ≥X"
// to the user instead of a bare auth failure.
type AuthFailMsg struct {
	Type       MsgType `cbor:"0,keyasint"`
	Reason     string  `cbor:"1,keyasint"`
	MinVersion string  `cbor:"2,keyasint,omitempty"`
}

// PingMsg is the client-side keepalive; the server replies with PongMsg.
type PingMsg struct {
	Type MsgType `cbor:"0,keyasint"`
}

// PongMsg is the server's reply to PingMsg.
type PongMsg struct {
	Type MsgType `cbor:"0,keyasint"`
}

// ErrorMsg is a server-emitted error frame. Code is one of the Err* wire
// constants. Path scopes the error to a single op when relevant.
// RetryAfter (seconds) is set on rate-limit errors so the client can back off.
type ErrorMsg struct {
	Type       MsgType `cbor:"0,keyasint"`
	Code       string  `cbor:"1,keyasint"`
	Message    string  `cbor:"2,keyasint"`
	Path       string  `cbor:"3,keyasint,omitempty"`
	RetryAfter int     `cbor:"4,keyasint,omitempty"`
}

// TagCreatedMsg is broadcast to all clients when a tag lands. Kind is
// "named" or "review"; By is the keyname of the author for audit display.
type TagCreatedMsg struct {
	Type   MsgType `cbor:"0,keyasint"`
	Name   string  `cbor:"1,keyasint"`
	Commit string  `cbor:"2,keyasint"`
	Kind   string  `cbor:"3,keyasint"`
	By     string  `cbor:"4,keyasint"`
}

// TagDeletedMsg is broadcast when a tag is removed; By identifies the
// deleting key for audit display.
type TagDeletedMsg struct {
	Type   MsgType `cbor:"0,keyasint"`
	Name   string  `cbor:"1,keyasint"`
	Commit string  `cbor:"2,keyasint"`
	By     string  `cbor:"3,keyasint"`
}

// --- Client → server frames ---

// HelloMsg opens the session after Auth. Base is what the client thinks
// it's synchronized to (nil = bootstrap-from-empty). ManifestDigest is a
// rolling hash over the client's manifest at Base — the server uses it
// to detect client-side drift even when Base matches HEAD.
type HelloMsg struct {
	Type           MsgType `cbor:"0,keyasint"`
	Base           *Hash   `cbor:"1,keyasint,omitempty"`
	ManifestDigest *Hash   `cbor:"2,keyasint,omitempty"`
}

// PushBatchMsg carries a contiguous run of ops from the client.
// BatchID is client-monotonic; the server's idempotency cache dedupes on
// (client_id, batch_id). Base is the commit the client expects HEAD to
// be at when the server applies — mismatch → stale_base ack.
type PushBatchMsg struct {
	Type    MsgType `cbor:"0,keyasint"`
	BatchID uint64  `cbor:"1,keyasint"`
	Base    Hash    `cbor:"2,keyasint"`
	Ops     []Op    `cbor:"3,keyasint"`
}

// FlushMsg asks the server to commit the client's stage immediately.
// Used by graceful daemon shutdown, leyline tag, leyline review, and
// pre-Tier-3-read flushes. FlushID echoes in FlushAck for correlation.
type FlushMsg struct {
	Type    MsgType `cbor:"0,keyasint"`
	FlushID uint64  `cbor:"1,keyasint"`
}

// --- Server → client frames ---

// HelloOKMsg routes the client to one of four post-handshake paths.
// State is one of HelloStateUpToDate / Catchup / Bootstrap / BaseLost.
// Head is the server's current HEAD; the client should treat it as
// authoritative for subsequent PushBatch.Base.
type HelloOKMsg struct {
	Type  MsgType `cbor:"0,keyasint"`
	State string  `cbor:"1,keyasint"`
	Head  Hash    `cbor:"2,keyasint"`
}

// CatchupMsg carries the diff from client's Base to server's HEAD as
// ops. Filter-scoped to allowed [sync]; the client further filters by
// its local leylineignore before classification. More=true on every
// non-terminal frame in a chunked sequence; the client buffers and runs
// classification only on the terminal frame.
type CatchupMsg struct {
	Type MsgType `cbor:"0,keyasint"`
	From Hash    `cbor:"1,keyasint"`
	To   Hash    `cbor:"2,keyasint"`
	Ops  []Op    `cbor:"3,keyasint"`
	// More: omitempty means the terminal frame omits key 4 entirely;
	// absent ⟺ terminal. Receivers MUST treat key-4-missing as More=false.
	// Do not introduce a sentinel value here without coordinating with the
	// classification phase (client-side conflict detection) which fires only
	// on the terminal frame.
	More bool `cbor:"4,keyasint,omitempty"`
}

// BootstrapMsg seeds a client with the full filter-scoped manifest as
// writes. Same chunking semantics as CatchupMsg.
type BootstrapMsg struct {
	Type MsgType `cbor:"0,keyasint"`
	Head Hash    `cbor:"1,keyasint"`
	Ops  []Op    `cbor:"2,keyasint"`
	// More: same absent ⟺ terminal invariant as CatchupMsg.More.
	More bool `cbor:"3,keyasint,omitempty"`
}

// PushAckMsg acknowledges a PushBatch. Result is PushAckOK / PushAckStaleBase /
// PushAckFiltered. NewBase is the server's HEAD after applying (on ok) or the
// current HEAD the client should rebase against (on stale_base). On filtered,
// NewBase is advisory only: HEAD is unchanged (nothing committed), so the
// client must NOT rebase on it — it drops the Filtered paths and retries.
type PushAckMsg struct {
	Type    MsgType `cbor:"0,keyasint"`
	BatchID uint64  `cbor:"1,keyasint"`
	Result  string  `cbor:"2,keyasint"`
	NewBase Hash    `cbor:"3,keyasint"`
	// Filtered carries the paths the server refused under the [sync] gate.
	// Present only when Result == PushAckFiltered (renames report op.To).
	Filtered []string `cbor:"4,keyasint,omitempty"`
}

// BroadcastMsg is the live-update analogue of CatchupMsg — sent when
// another client's batch lands. Filter-scoped; client classifies against
// its current staged log on arrival.
type BroadcastMsg struct {
	Type MsgType `cbor:"0,keyasint"`
	From Hash    `cbor:"1,keyasint"`
	To   Hash    `cbor:"2,keyasint"`
	Ops  []Op    `cbor:"3,keyasint"`
}

// FlushAckMsg confirms a Flush request. Head is the HEAD after the
// requested commit lands; equals the prior HEAD if the stage was empty.
type FlushAckMsg struct {
	Type    MsgType `cbor:"0,keyasint"`
	FlushID uint64  `cbor:"1,keyasint"`
	Head    Hash    `cbor:"2,keyasint"`
}

// --- Encoder + helpers ---

// encMode is a deterministic CBOR encoder shared across the package —
// fxamacker's docs note constructed modes are safe for concurrent use.
var encMode cbor.EncMode

func init() {
	em, err := cbor.CoreDetEncOptions().EncMode()
	if err != nil {
		panic(fmt.Sprintf("cbor enc mode: %v", err))
	}
	encMode = em
}

// Encode marshals msg as deterministic CBOR.
func Encode(msg any) ([]byte, error) {
	return encMode.Marshal(msg)
}

// Decode unmarshals CBOR data into v.
func Decode(data []byte, v any) error {
	return cbor.Unmarshal(data, v)
}

// peekType reads only the envelope to learn which concrete type to allocate.
func peekType(data []byte) (MsgType, error) {
	var env Envelope
	if err := cbor.Unmarshal(data, &env); err != nil {
		return 0, fmt.Errorf("invalid CBOR envelope: %w", err)
	}
	return env.Type, nil
}

// ParseClientMessage decodes a client → server frame. Unknown MsgType
// returns an error wrapping ErrInvalidDataErr; the caller closes the
// socket with WS 1002 + CloseReasonProtocolMismatch.
func ParseClientMessage(data []byte) (MsgType, any, error) {
	mt, err := peekType(data)
	if err != nil {
		return 0, nil, err
	}
	var msg any
	switch mt {
	case MsgAuth:
		msg = &AuthMsg{}
	case MsgHello:
		msg = &HelloMsg{}
	case MsgPushBatch:
		msg = &PushBatchMsg{}
	case MsgFlush:
		msg = &FlushMsg{}
	case MsgPing:
		msg = &PingMsg{}
	default:
		return mt, nil, fmt.Errorf("%w: unknown client message type: %d", ErrInvalidDataErr, mt)
	}
	if err := cbor.Unmarshal(data, msg); err != nil {
		return mt, nil, fmt.Errorf("unmarshal type %d: %w", mt, err)
	}
	return mt, msg, nil
}

// ParseServerMessage decodes a server → client frame.
func ParseServerMessage(data []byte) (MsgType, any, error) {
	mt, err := peekType(data)
	if err != nil {
		return 0, nil, err
	}
	var msg any
	switch mt {
	case MsgAuthOK:
		msg = &AuthOKMsg{}
	case MsgAuthFail:
		msg = &AuthFailMsg{}
	case MsgHelloOK:
		msg = &HelloOKMsg{}
	case MsgCatchup:
		msg = &CatchupMsg{}
	case MsgBootstrap:
		msg = &BootstrapMsg{}
	case MsgPushAck:
		msg = &PushAckMsg{}
	case MsgBroadcast:
		msg = &BroadcastMsg{}
	case MsgFlushAck:
		msg = &FlushAckMsg{}
	case MsgPong:
		msg = &PongMsg{}
	case MsgError:
		msg = &ErrorMsg{}
	case MsgTagCreated:
		msg = &TagCreatedMsg{}
	case MsgTagDeleted:
		msg = &TagDeletedMsg{}
	default:
		return mt, nil, fmt.Errorf("%w: unknown server message type: %d", ErrInvalidDataErr, mt)
	}
	if err := cbor.Unmarshal(data, msg); err != nil {
		return mt, nil, fmt.Errorf("unmarshal type %d: %w", mt, err)
	}
	return mt, msg, nil
}
