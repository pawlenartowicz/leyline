package render

import (
	"strings"
	"testing"
)

func renderCV(t *testing.T, body, vaultPrefix, vaultID string, idMap map[string]string) string {
	t.Helper()
	r := NewMarkdownRenderer(MarkdownOptions{})
	got, _, err := r.Render([]byte(body), URLContext{
		VaultPrefix: vaultPrefix,
		VaultID:     vaultID,
		IDMap:       idMap,
	})
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	return got
}

func TestCrossVault_KnownVaultRewrites(t *testing.T) {
	out := renderCV(t,
		"see [[@research/climate-paper]] for context",
		"/notes", "static-notes",
		map[string]string{"research": "/research"},
	)
	if !strings.Contains(out, `href="/research/climate-paper"`) {
		t.Errorf("href missing: %s", out)
	}
	if !strings.Contains(out, `class="leyline-cross-vault"`) {
		t.Errorf("class missing: %s", out)
	}
	if !strings.Contains(out, "climate-paper") {
		t.Errorf("label text missing: %s", out)
	}
}

func TestCrossVault_AliasOverridesLabel(t *testing.T) {
	out := renderCV(t,
		"see [[@research/climate-paper|Display Title]]",
		"/notes", "static-notes",
		map[string]string{"research": "/research"},
	)
	if !strings.Contains(out, "Display Title") {
		t.Errorf("alias label missing: %s", out)
	}
	if !strings.Contains(out, `href="/research/climate-paper"`) {
		t.Errorf("href missing: %s", out)
	}
}

func TestCrossVault_AnchorAppended(t *testing.T) {
	out := renderCV(t,
		"[[@research/page#section]]",
		"/notes", "static-notes",
		map[string]string{"research": "/research"},
	)
	if !strings.Contains(out, `href="/research/page#section"`) {
		t.Errorf("href with anchor missing: %s", out)
	}
}

func TestCrossVault_NoPathRendersPrefixRoot(t *testing.T) {
	out := renderCV(t,
		"[[@research]]",
		"/notes", "static-notes",
		map[string]string{"research": "/research"},
	)
	if !strings.Contains(out, `href="/research/"`) {
		t.Errorf("no-path href missing: %s", out)
	}
}

func TestCrossVault_EmptyPathRendersPrefixRoot(t *testing.T) {
	out := renderCV(t,
		"[[@research/]]",
		"/notes", "static-notes",
		map[string]string{"research": "/research"},
	)
	if !strings.Contains(out, `href="/research/"`) {
		t.Errorf("empty-path href missing: %s", out)
	}
}

func TestCrossVault_UnknownVaultProducesUnresolvedSpan(t *testing.T) {
	out := renderCV(t,
		"[[@missing/page|fallback]]",
		"/notes", "static-notes",
		map[string]string{"research": "/research"},
	)
	if !strings.Contains(out, `class="leyline-cross-vault is-unresolved"`) {
		t.Errorf("unresolved class missing: %s", out)
	}
	if !strings.Contains(out, `title="unknown vault: missing"`) {
		t.Errorf("unresolved title missing: %s", out)
	}
	if !strings.Contains(out, "fallback") {
		t.Errorf("label text missing: %s", out)
	}
}

func TestCrossVault_SameVaultCollapsesPrefix(t *testing.T) {
	out := renderCV(t,
		"[[@personal-site/about]]",
		"/", "personal-site",
		map[string]string{"personal-site": "/", "research": "/research"},
	)
	// Same-vault collapse → href has no prefix component.
	if !strings.Contains(out, `href="/about"`) {
		t.Errorf("collapsed href missing: %s", out)
	}
	if strings.Contains(out, `is-unresolved`) {
		t.Errorf("should not be unresolved: %s", out)
	}
}

func TestCrossVault_MalformedAtSyntaxUntouched(t *testing.T) {
	// `@` followed by something that doesn't match the vault-id regex.
	out := renderCV(t,
		"[[@!nope]]",
		"/", "personal-site",
		map[string]string{},
	)
	// The wikilink falls through to its unresolved-text path; the literal
	// "@!nope" should be visible somewhere in the output.
	if !strings.Contains(out, "@!nope") {
		t.Errorf("malformed @ should render as plain text: %s", out)
	}
	if strings.Contains(out, `leyline-cross-vault`) {
		t.Errorf("should not carry cross-vault class: %s", out)
	}
}

func TestCrossVault_ChildTextPreservedForBareTarget(t *testing.T) {
	// Bare `[[@research/page]]` — no alias, so the wikilink parser appends
	// the implicit label "@research/page" as a text segment. The
	// transformer should adopt that segment so the rendered anchor text
	// matches what the author typed (modulo `@` retention).
	out := renderCV(t,
		"[[@research/page]]",
		"/", "personal-site",
		map[string]string{"research": "/research"},
	)
	if !strings.Contains(out, `>@research/page</a>`) {
		t.Errorf("bare label not preserved: %s", out)
	}
}

// TestCrossvault_RejectsProtocolPrefix verifies that a vault IDMap entry
// whose value is a full URL (wss:// / https://) does not produce a
// protocol-prefixed href. The crossVaultRE vault_id pattern restricts IDs
// to `[a-zA-Z0-9][a-zA-Z0-9_-]*`, so an entry keyed to "research" will
// never have its ID confused with a scheme. The risk is in the prefix
// stored in IDMap. This test pins the current sanitised output.
func TestCrossvault_RejectsProtocolPrefix(t *testing.T) {
	// IDMap maps vault ID "foo" to a prefix that was accidentally given a
	// wss:// prefix. buildCrossVaultHref joins it with joinVaultURL; the
	// result must NOT start with wss://.
	out := renderCV(t,
		"[[@foo/page]]",
		"/notes", "static-notes",
		map[string]string{"foo": "wss://research"},
	)
	// The rendered href must not begin with wss:// — it should be either
	// sanitised or rendered as an unresolved span.
	if strings.Contains(out, "wss://") {
		t.Errorf("href must not contain wss:// protocol prefix: %s", out)
	}
}

// TestCrossvault_TitleXSS verifies that a cross-vault link alias containing
// an XSS payload is HTML-escaped in the rendered output.
func TestCrossvault_TitleXSS(t *testing.T) {
	payload := `<svg onload="alert(1)">`
	out := renderCV(t,
		"[[@research/page|"+payload+"]]",
		"/notes", "static-notes",
		map[string]string{"research": "/research"},
	)
	if strings.Contains(out, `<svg onload`) {
		t.Errorf("XSS payload not escaped in cross-vault link: %s", out)
	}
	if !strings.Contains(out, "&lt;svg") {
		t.Errorf("escaped SVG tag missing from output: %s", out)
	}
}
