// Package meta loads and validates .leyline/vaultconfig/meta — free-form YAML
// for vault-level metadata. Today only `created_at` is read; `encryption:` and
// `recovery:` blocks are reserved seams preserved as raw nodes so future code
// can claim them without a schema migration.
//
// The parser is intentionally strict. The file is operator-edited and
// committed alongside the vault, so a malicious or buggy YAML file must not
// be able to exhaust memory, recurse forever, or smuggle untyped data in.
package meta

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// MaxFileSize caps the raw .leyline/vaultconfig/meta file. Beyond this we stop reading;
// anything legitimate is well under 1 KB. The cap bounds memory before the
// YAML decoder ever sees the bytes.
const MaxFileSize = 64 * 1024

// MaxNodeDepth caps post-parse YAML nesting depth. Defends against
// pathological documents (deeply nested mappings/sequences) and billion-laughs-
// class alias expansion attacks. The current schema needs at most ~3 levels.
const MaxNodeDepth = 10

// Config is the in-memory representation of .leyline/vaultconfig/meta. Only the fields
// the daemon understands today are typed; Encryption and Recovery are
// preserved as raw nodes so future code can claim them without a schema migration.
type Config struct {
	CreatedAt time.Time `yaml:"created_at"`

	// Encryption and Recovery are reserved seams. They round-trip as raw nodes
	// so the daemon neither validates nor mutates them.
	Encryption *yaml.Node `yaml:"encryption,omitempty"`
	Recovery   *yaml.Node `yaml:"recovery,omitempty"`
}

// Load reads and parses .leyline/vaultconfig/meta with the hardening rules above.
// A missing file is not an error — Load returns (nil, nil) so callers can
// treat absence as "no metadata."
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("open meta: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, MaxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read meta: %w", err)
	}
	if len(data) > MaxFileSize {
		return nil, fmt.Errorf("meta exceeds %d bytes", MaxFileSize)
	}

	// Pass 1: parse to a node tree to enforce depth limits and reject
	// anchors/aliases. yaml.v3's Decoder does not expose these as options,
	// so we walk the tree ourselves.
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse meta: %w", err)
	}
	if err := checkNode(&root, 0); err != nil {
		return nil, fmt.Errorf("validate meta: %w", err)
	}
	if root.Kind == 0 {
		// Empty document.
		return &Config{}, nil
	}

	// Pass 2: decode with KnownFields(true) so unknown top-level keys are an
	// error rather than silently dropped. Callers thus catch typos like
	// `creatd_at` instead of getting a zero-value timestamp.
	cfg := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("decode meta: %w", err)
	}
	return cfg, nil
}

// checkNode walks the YAML node tree enforcing depth and forbidding aliases.
// Aliases are rejected because billion-laughs-style attacks expand small
// alias chains into huge document trees; we don't need them and refusing
// outright is simpler than counting expansions.
func checkNode(n *yaml.Node, depth int) error {
	if n == nil {
		return nil
	}
	if depth > MaxNodeDepth {
		return errors.New("nesting depth exceeds limit")
	}
	if n.Kind == yaml.AliasNode {
		return errors.New("YAML aliases are not allowed")
	}
	if n.Anchor != "" {
		return errors.New("YAML anchors are not allowed")
	}
	for _, child := range n.Content {
		if err := checkNode(child, depth+1); err != nil {
			return err
		}
	}
	return nil
}
