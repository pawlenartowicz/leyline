package admin

import (
	"fmt"
	"strings"
)

// ResolveInput is the input to ResolveKey.
type ResolveInput struct {
	DirectKey       string   // --key / LEYLINE_KEY
	DirectServer    string   // --server (required with --key)
	PositionalVault string   // canonical <host>/<vaultID>; empty for server-scoped verbs
	Keyname         string   // --keyname
	Keystore        []KeyRow // from LoadKeystore
}

// ResolveOutput is what the caller passes downstream to the HTTP client.
type ResolveOutput struct {
	Key       string
	ServerURL string
}

// ResolveKey picks the right API key and server URL from the available inputs:
//
//  1. Direct mode: --key + --server. If a positional vault is also present,
//     its host must match --server's host.
//  2. Vault-scoped (positional vault given):
//     Step A: exact vault match in keystore.
//     Step B: no exact match → host match on the same server.
//  3. Server-scoped (no positional): all keystore rows are candidates.
//  4. Tie-break: --keyname picks one candidate; >1 candidates without --keyname errors.
//  5. 0 candidates → error.
func ResolveKey(in ResolveInput) (*ResolveOutput, error) {
	if in.DirectKey != "" {
		if in.DirectServer == "" {
			return nil, fmt.Errorf("--key requires --server")
		}
		if in.PositionalVault != "" {
			vHost := hostOf(in.PositionalVault)
			sHost := hostOfServerURL(in.DirectServer)
			if vHost != "" && sHost != "" && vHost != sHost {
				return nil, fmt.Errorf("host mismatch: positional vault host %q != --server host %q", vHost, sHost)
			}
		}
		return &ResolveOutput{Key: in.DirectKey, ServerURL: in.DirectServer}, nil
	}

	if len(in.Keystore) == 0 {
		return nil, fmt.Errorf("no key in keystore for %s", contextLabel(in.PositionalVault))
	}

	var candidates []KeyRow
	if in.PositionalVault != "" {
		// Step A: exact vault match.
		for _, r := range in.Keystore {
			if r.Vault == in.PositionalVault {
				candidates = append(candidates, r)
			}
		}
		// Step B: host fallback only when Step A found nothing.
		if len(candidates) == 0 {
			host := hostOf(in.PositionalVault)
			for _, r := range in.Keystore {
				if hostOf(r.Vault) == host {
					candidates = append(candidates, r)
				}
			}
		}
	} else {
		candidates = append(candidates, in.Keystore...)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("no key in keystore for %s", contextLabel(in.PositionalVault))
	}

	if in.Keyname != "" {
		for _, r := range candidates {
			if r.Name == in.Keyname {
				return &ResolveOutput{Key: r.Key, ServerURL: serverURLOf(r.Vault)}, nil
			}
		}
		return nil, fmt.Errorf("--keyname %q matched no candidates", in.Keyname)
	}

	if len(candidates) == 1 {
		return &ResolveOutput{Key: candidates[0].Key, ServerURL: serverURLOf(candidates[0].Vault)}, nil
	}

	var sb strings.Builder
	sb.WriteString("multiple keystore rows match — pass --keyname:\n")
	for _, r := range candidates {
		name := r.Name
		if name == "" {
			name = "-"
		}
		fmt.Fprintf(&sb, "  %-30s --keyname %s\n", r.Vault, name)
	}
	return nil, fmt.Errorf("%s", sb.String())
}

func contextLabel(positional string) string {
	if positional == "" {
		return "any server"
	}
	return positional
}

// serverURLOf assumes https:// for the host portion of a canonical vault
// address. Users who need plain HTTP must use --key + --server.
func serverURLOf(vault string) string {
	host := hostOf(vault)
	if host == "" {
		return ""
	}
	return "https://" + host
}
