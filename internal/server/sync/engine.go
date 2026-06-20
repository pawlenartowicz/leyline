package sync

import (
	protocol "github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/internal/server/allowed"
	"github.com/pawlenartowicz/leyline/internal/server/storage"
)

// HelloResolution is the result of ResolveHello. State is one of the
// protocol.HelloState* constants. Ops is empty for up_to_date and base_lost.
type HelloResolution struct {
	State string
	Head  protocol.Hash
	Ops   []protocol.Op // nil for up_to_date and base_lost
}

// ResolveHello determines what the server should tell the client after
// receiving a Hello frame. It returns the appropriate HelloState and, for
// catchup and bootstrap states, the op slice the client needs.
//
// clientBase nil → bootstrap (client has no base).
// clientBase == head AND (clientManifestDigest nil OR matches server's
// computed digest at head) → up_to_date.
// clientBase == head AND clientManifestDigest non-nil AND mismatch →
// catchup with empty op list. The empty Catchup frame nudges the client
// into its session-start reconcile path; any drifted local content
// surfaces as fresh T1 ops on the next push.
// clientBase not reachable from head → base_lost.
// otherwise → catchup.
//
// includeVaultConfig selects the recipient's manifest-digest view: it MUST
// equal caps.VaultAdmin so the digest matches what the send layer delivers
// (admins hold the .leyline/vaultconfig/* tree; non-admins do not).
func ResolveHello(g *storage.GitStore, allow *allowed.Rules, clientBase *protocol.Hash, clientManifestDigest *protocol.Hash, includeVaultConfig bool) (HelloResolution, error) {
	head, err := g.HeadHash()
	if err != nil {
		return HelloResolution{}, err
	}

	if clientBase == nil {
		return HelloResolution{
			State: protocol.HelloStateBootstrap,
			Head:  head,
		}, nil
	}

	if *clientBase == head {
		// When the client supplied a manifest digest, verify it against
		// the server's view at HEAD. Mismatch → return catchup with an
		// empty op list so the client re-runs its session-start reconcile.
		if clientManifestDigest != nil {
			serverDigest, err := ServerManifestDigestAtHead(g, allow, head, includeVaultConfig)
			if err != nil {
				return HelloResolution{}, err
			}
			if serverDigest != *clientManifestDigest {
				return HelloResolution{
					State: protocol.HelloStateCatchup,
					Head:  head,
					Ops:   nil,
				}, nil
			}
		}
		return HelloResolution{
			State: protocol.HelloStateUpToDate,
			Head:  head,
		}, nil
	}

	reachable, err := g.ReachableFromHead(*clientBase)
	if err != nil {
		return HelloResolution{}, err
	}
	if !reachable {
		return HelloResolution{
			State: protocol.HelloStateBaseLost,
			Head:  head,
		}, nil
	}

	ops, err := WalkCatchup(g, allow, *clientBase, head)
	if err != nil {
		return HelloResolution{}, err
	}
	return HelloResolution{
		State: protocol.HelloStateCatchup,
		Head:  head,
		Ops:   ops.Ops,
	}, nil
}
