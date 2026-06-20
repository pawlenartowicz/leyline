package config

import (
	"log/slog"
	"sort"

	"github.com/pawlenartowicz/leyline/internal/web/theme"
)

// VaultEntry is one (prefix → filesystem path) pairing the idMap builder
// consumes. Callers (server.New, hot reload) walk vault.Registry or
// equivalent and produce these.
type VaultEntry struct {
	Prefix string
	Root   string
}

// BuildIDMap reads each vault's `.leyline/vaultconfig/web.yaml`, extracts
// `vault_id:`, and returns the vault_id → prefix map.
//
// Vaults without `web.yaml`, without `vault_id`, or whose file fails to
// parse are silently dropped from the map after a warn-level log. The
// vault stays mounted at its prefix; only cross-vault `@`-references to
// it become unresolvable.
//
// Duplicate `vault_id` across multiple vaults is resolved by sorting the
// entries alphabetically by prefix and keeping the first one; the others
// are dropped with a warn that names both conflicting prefixes. Sorting a
// copy makes the result independent of the input slice's order.
func BuildIDMap(entries []VaultEntry, logger *slog.Logger) map[string]string {
	if logger == nil {
		logger = slog.Default()
	}
	sorted := make([]VaultEntry, len(entries))
	copy(sorted, entries)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Prefix < sorted[j].Prefix })

	out := make(map[string]string)
	owner := make(map[string]string) // vault_id → first-claiming prefix
	for _, e := range sorted {
		vyaml, err := theme.LoadVaultYAML(e.Root)
		if err != nil {
			logger.Warn("idmap: skipping vault — web.yaml parse failed",
				"prefix", e.Prefix, "root", e.Root, "err", err.Error())
			continue
		}
		id := vyaml.VaultID
		if id == "" {
			logger.Warn("idmap: skipping vault — no vault_id in web.yaml",
				"prefix", e.Prefix, "root", e.Root)
			continue
		}
		if prev, exists := owner[id]; exists {
			logger.Warn("idmap: duplicate vault_id — keeping first",
				"vault_id", id, "kept_prefix", prev, "dropped_prefix", e.Prefix)
			continue
		}
		owner[id] = e.Prefix
		out[id] = e.Prefix
	}
	return out
}
