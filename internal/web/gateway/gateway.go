// Package gateway gives leyline-web an outbound-client role: it relays a
// user's authenticated mutations to leyline-server using the user's own key,
// extracted from the leyline_auth cookie. Config-file writes go over the
// WebSocket sync protocol (a thin one-shot push); key/vault operations go
// over the existing REST admin/operator surface. The server's auth/caps model
// is unchanged — it sees a normal keyed client.
package gateway

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/pawlenartowicz/leyline/internal/buildinfo"
	"github.com/pawlenartowicz/leyline/internal/cli/cli/admin"
	leysync "github.com/pawlenartowicz/leyline/pkg/sync"
	"github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/access"
	"github.com/pawlenartowicz/leyline/protocol/vaultaddr"
)

// Gateway is the per-instance outbound client. One Gateway is shared across
// all vaults; the per-vault target is host + "/" + vaultID built at call time.
type Gateway struct {
	host    string // canonical bare host from Config.ServerAddress
	devMode bool   // dev TLS: InsecureSkipVerify on WS + REST
}

// New returns a Gateway for host, or nil when host is empty (unpaired web —
// the read-only guest mirror). Callers guard with Paired().
func New(host string, devMode bool) *Gateway {
	if host == "" {
		return nil
	}
	return &Gateway{host: host, devMode: devMode}
}

// Paired reports whether this web relays mutations (server_address set).
func (g *Gateway) Paired() bool { return g != nil && g.host != "" }

// Host is the canonical bare server address this gateway relays to ("" when
// unpaired). Used only as a display label (the panel's vault plate); the
// relay paths build the per-vault address themselves.
func (g *Gateway) Host() string {
	if g == nil {
		return ""
	}
	return g.host
}

// buildWriteOp constructs the single OpWrite for a config save. preHash is the
// hash the file had on disk immediately before this write (nil = true create);
// the server uses it for optimistic-concurrency (stale_base) detection. Seq is
// always 1 because each PushFile uses a fresh ephemeral client id with no
// idempotency history (see PushFile).
func buildWriteOp(relPath string, content []byte, preHash *protocol.Hash, ts int64) protocol.Op {
	return protocol.Op{
		Seq:     1,
		Type:    protocol.OpWrite,
		Path:    relPath,
		Data:    content,
		PreHash: preHash,
		TS:      ts,
	}
}

// PushFile relays a single config-file write to the server as the user, over
// the WS sync protocol — the same PushBatch/commit path the CLI and plugin
// use. It is a one-shot ephemeral session: dial+auth (user's key), Hello to
// learn HEAD, push one OpWrite, await PushAck, close. A fresh random client id
// per call means Seq=1/BatchID=1 with no idempotency history.
//
// The returned PushAckMsg.Result is the outcome the caller surfaces:
//   - protocol.PushAckOK       → committed; NewBase is the new HEAD.
//   - protocol.PushAckStaleBase → the file changed under the user (preHash
//     mismatch / HEAD moved); caller tells the user to reload and retry.
//   - protocol.PushAckFiltered  → policy/permission refusal (e.g. size).
//
// A non-nil error is a transport/auth failure (dial, auth, disconnect).
func (g *Gateway) PushFile(ctx context.Context, vaultID, key, relPath string, content []byte, preHash *protocol.Hash) (protocol.PushAckMsg, error) {
	addr := vaultaddr.Format(g.host, vaultID)

	var dialer *websocket.Dialer
	if g.devMode {
		dialer = &websocket.Dialer{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}

	cli := leysync.NewClient()
	authOK, err := cli.Dial(ctx, leysync.DialOpts{
		URL:           addr,
		Key:           key,
		PluginVersion: buildinfo.Value,
		ClientID:      newEphemeralID(),
		Dialer:        dialer,
	})
	if err != nil {
		return protocol.PushAckMsg{}, fmt.Errorf("gateway dial: %w", err)
	}
	defer cli.Close()

	// Hello to open the session and learn authoritative HEAD. Base = the HEAD
	// the server reported at auth; the server replies with its current HEAD.
	if err := cli.Send(protocol.HelloMsg{Type: protocol.MsgHello, Base: &authOK.Head}); err != nil {
		return protocol.PushAckMsg{}, fmt.Errorf("gateway hello: %w", err)
	}
	helloOK, err := recvUntil[*protocol.HelloOKMsg](ctx, cli, protocol.MsgHelloOK)
	if err != nil {
		return protocol.PushAckMsg{}, fmt.Errorf("gateway hello-ok: %w", err)
	}

	op := buildWriteOp(relPath, content, preHash, time.Now().UnixNano())
	if err := cli.Send(protocol.PushBatchMsg{
		Type:    protocol.MsgPushBatch,
		BatchID: 1,
		Base:    helloOK.Head,
		Ops:     []protocol.Op{op},
	}); err != nil {
		return protocol.PushAckMsg{}, fmt.Errorf("gateway push: %w", err)
	}
	ack, err := recvUntil[*protocol.PushAckMsg](ctx, cli, protocol.MsgPushAck)
	if err != nil {
		return protocol.PushAckMsg{}, fmt.Errorf("gateway push-ack: %w", err)
	}
	return *ack, nil
}

// recvUntil reads server messages until one of type want arrives, returning it
// typed as T. Intervening frames (Pong, Catchup, Broadcast) are skipped — a
// one-shot config push expects none, but the read loop may surface keepalives.
// A server ErrorMsg is returned as an error.
func recvUntil[T any](ctx context.Context, cli *leysync.Client, want protocol.MsgType) (T, error) {
	var zero T
	for {
		m, err := cli.RecvSync(ctx)
		if err != nil {
			return zero, err
		}
		if m.Type == protocol.MsgError {
			e := m.Payload.(*protocol.ErrorMsg)
			return zero, fmt.Errorf("server error %s: %s", e.Code, e.Message)
		}
		if m.Type == want {
			return m.Payload.(T), nil
		}
	}
}

// newEphemeralID returns a fresh client id for a one-shot push. github.com/
// google/uuid is already a dependency (pkg/stage.EnsureClientID uses it).
func newEphemeralID() string { return uuid.NewString() }

// rest builds an admin.Client (Bearer = user's key) pointed at the server's
// REST base for this gateway. Reuses the CLI's HTTP client verbatim.
func (g *Gateway) rest(key string) (*admin.Client, error) {
	// APIURL needs a full vault address to parse the host; the vaultID is
	// irrelevant to the base URL it returns, so any valid id works.
	base, err := vaultaddr.APIURL(vaultaddr.Format(g.host, "x"))
	if err != nil {
		return nil, err
	}
	return admin.NewClient(base, key, g.devMode), nil
}

// CreatedKey mirrors POST /_leyline/admin/{vault}/keys (201). Key is the
// cleartext ley_<20>, shown to the user exactly once.
type CreatedKey struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Role string `json:"role"`
}

// VaultInfo mirrors one row of GET /_leyline/operator/vaults.
type VaultInfo struct {
	ID               string `json:"id"`
	Path             string `json:"path"`
	ServerWideAdmins bool   `json:"server_wide_admins"`
	AdminEmail       string `json:"admin_email"`
	Created          string `json:"created"`
	Hydrated         bool   `json:"hydrated"`
}

// VaultCreateReq mirrors the POST /_leyline/operator/vaults body.
type VaultCreateReq struct {
	ID               string `json:"id"`
	Path             string `json:"path,omitempty"`
	ServerWideAdmins bool   `json:"server_wide_admins"`
	AdminEmail       string `json:"admin_email,omitempty"`
	AdminKeyName     string `json:"admin_key_name,omitempty"`
}

// CreatedVault mirrors the 201 response of vault create. AdminKey is cleartext.
type CreatedVault struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	AdminKey string `json:"admin_key"`
}

func (g *Gateway) KeysList(vaultID, key string) ([]access.KeyInfo, error) {
	c, err := g.rest(key)
	if err != nil {
		return nil, err
	}
	var rows []access.KeyInfo
	if err := c.Do("GET", "/_leyline/admin/"+vaultID+"/keys", nil, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (g *Gateway) KeysCreate(vaultID, key, name, role string) (CreatedKey, error) {
	c, err := g.rest(key)
	if err != nil {
		return CreatedKey{}, err
	}
	var out CreatedKey
	body := map[string]string{"name": name, "role": role}
	if err := c.Do("POST", "/_leyline/admin/"+vaultID+"/keys", body, &out); err != nil {
		return CreatedKey{}, err
	}
	return out, nil
}

func (g *Gateway) KeysDelete(vaultID, key, name string) error {
	c, err := g.rest(key)
	if err != nil {
		return err
	}
	return c.Do("DELETE", "/_leyline/admin/"+vaultID+"/keys/"+name, nil, nil)
}

func (g *Gateway) KeysUpdateRole(vaultID, key, name, role string) error {
	c, err := g.rest(key)
	if err != nil {
		return err
	}
	body := map[string]string{"role": role}
	return c.Do("PUT", "/_leyline/admin/"+vaultID+"/keys/"+name+"/role", body, nil)
}

func (g *Gateway) VaultsList(key string) ([]VaultInfo, error) {
	c, err := g.rest(key)
	if err != nil {
		return nil, err
	}
	var rows []VaultInfo
	if err := c.Do("GET", "/_leyline/operator/vaults", nil, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

func (g *Gateway) VaultCreate(key string, in VaultCreateReq) (CreatedVault, error) {
	c, err := g.rest(key)
	if err != nil {
		return CreatedVault{}, err
	}
	var out CreatedVault
	if err := c.Do("POST", "/_leyline/operator/vaults", in, &out); err != nil {
		return CreatedVault{}, err
	}
	return out, nil
}

func (g *Gateway) VaultDestroy(vaultID, key string) error {
	c, err := g.rest(key)
	if err != nil {
		return err
	}
	return c.Do("POST", "/_leyline/admin/"+vaultID+"/destroy", map[string]any{}, nil)
}
