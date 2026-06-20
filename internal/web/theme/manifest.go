// Package theme handles theme manifests, parent-chain resolution, and per-file
// lookup across vault override → active theme → parent chain.
package theme

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Manifest is one theme's web.yaml.
//
// Two kinds of chain-merged knobs live here:
//   - Theme-template flags at top level (ShowTitles, Versions). These are
//     decisions baked into the theme's templates; vaults cannot override them.
//   - `defaults:` block. Values the engine treats as a per-vault (and, in
//     future, per-file) editable cascade.
//
// Inheritance convention (used by overlay/ResolveChain) applies to both:
//   - Overridable scalar where the zero value is meaningful (e.g. bool's false)
//     must be a pointer type so "unset in YAML" is distinguishable from
//     "explicitly the zero value".
//   - Overridable string is bare string; "" is the unset sentinel.
//   - Overridable struct is replaced wholesale by overlay() whenever any sub-
//     field is non-zero. Sub-field-level inheritance is deferred.
//   - Bottom defaults live exclusively in Collapse(). No other layer
//     (LoadRegistry's missing-manifest fallback, LoadManifest, LoadVaultYAML)
//     may pre-fill a default — otherwise overlay() mistakes the pre-fill for
//     an explicit declaration and parent-chain inheritance silently breaks.
type Manifest struct {
	ParentTheme string           `yaml:"parent_theme"`
	ShowTitles  *bool            `yaml:"show_titles"`
	Versions    VersionsDefaults `yaml:"versions"`
	Defaults    Defaults         `yaml:"defaults"`
	// Custom is the free-form theme-author space — opaque to the engine,
	// passed through to templates as a sibling of .Theme/.Vault/.Page.
	// Merge semantics: per-key overlay across the parent chain and the
	// vault layer (vault wins on collisions; unset keys inherit). Values
	// are whatever yaml.v3 unmarshals — strings, bools, ints, floats,
	// nested maps/lists. Nested maps replace wholesale (no deep merge).
	// No validation; unknown keys render as Go's zero/nil in templates.
	Custom map[string]any `yaml:"custom"`
}

// Defaults is the raw, parse-time view of one theme's `defaults:` block.
// Holds values the engine treats as cascading-and-overridable: theme chain →
// vault overlay → (future) per-file frontmatter. Theme-template flags that are
// never vault-editable live on Manifest at top level, not here.
type Defaults struct {
	GuestRole  string           `yaml:"guest_role"`  // view | edit | none; "" = unset
	EditSwitch EditSwitchConfig `yaml:"edit_switch"` // edit-mode UI flag
	Auth       AuthDefaults     `yaml:"auth"`        // web-auth chrome and redirect behaviour
	// Menu picks the sidebar nav-tree rendering mode. "auto" (default)
	// renders compact <details>-collapsed groups when the nav exceeds the
	// auto-compact threshold and plain <ul> otherwise. "compact" forces
	// collapse; "long" forces fully-expanded. "" = inherit/unset.
	Menu string `yaml:"menu"` // "" | auto | compact | long
	// LeftSidebar / RightSidebar configure each rail: a widget stack or one
	// of the scalar sentinels (body/none/references). Zero value = inherit;
	// Collapse applies the SidebarNone bottom default. See SidebarSpec.
	LeftSidebar  SidebarSpec `yaml:"left_sidebar"`
	RightSidebar SidebarSpec `yaml:"right_sidebar"`
	// TocFollow picks the right-rail table-of-contents scroll motion.
	// "drift" (default) rests the ToC level with the article top, then eases
	// it down to ~30vh and holds; "pin" is plain sticky near the top with no
	// drift. "" = inherit/unset. Ignored when the right rail is `references`
	// (that mode floats margin notes, not a ToC rail). Consumed by the base
	// theme's body[data-toc-follow="…"] CSS + the rail-drift JS.
	TocFollow string `yaml:"toc_follow"` // "" | drift | pin
}

// AuthDefaults is the raw, parse-time auth config inside a theme's `defaults:`
// block.
//
// The whole Auth block is replaced wholesale by overlay() when any sub-field
// is non-zero (same convention as EditSwitchConfig / VersionsDefaults).
// Sub-field-level inheritance between theme layers is deferred.
type AuthDefaults struct {
	// LoginButton controls where the auth chrome (Log in link or account
	// pill) is rendered. "" = inherit; bottom default is "none". Accepted
	// values: "header" | "footer" | "none".
	LoginButton string `yaml:"login_button"`
	// RedirectToLogin controls what an unauthenticated visitor gets on a
	// resource that requires authentication. false (default) → 404, matching
	// webignore no-existence-leak behaviour. true → 302 to login_path?return=…
	// Plain bool because false is the safe floor and the bottom default is
	// unambiguous; vault overrides use *bool (see AuthYAML) so they can still
	// revert a theme's true to false.
	RedirectToLogin bool `yaml:"redirect_to_login"`
}

// EditSwitchConfig controls whether a theme renders the edit-mode switch
// partial when the resolved role grants edit access. Pointer-Enabled so
// "unset in YAML" is distinguishable from explicit false.
type EditSwitchConfig struct {
	Enabled *bool `yaml:"enabled"`
}

// Resolved is the post-collapse view templates consume. Every field is a plain
// value — never nil — populated by Collapse() after chain merge + vault overlay.
//
// Vault-owned fields (VaultName, VaultTagline, VaultHome, License, Copyright,
// Header, Footer) piggyback on Resolved as a transport and are filled by
// server.New from VaultYAML — see the comment on Collapse.
type Resolved struct {
	ShowTitles   bool
	VaultName    string
	VaultTagline string
	VaultHome    string
	License      string
	Copyright    string
	// LeftSidebar / RightSidebar are the resolved per-rail configs (bottom
	// default applied — never zero after Collapse). Templates branch on
	// .Mode and range over .Widgets via the per-page render context.
	LeftSidebar  SidebarSpec
	RightSidebar SidebarSpec
	// TocFollow is the resolved right-rail ToC scroll motion: "drift" | "pin".
	// Never empty after Collapse — "drift" is the bottom default.
	TocFollow string
	GuestRole string
	EditSwitch   ResolvedEditSwitch
	Auth         ResolvedAuth
	Header       ResolvedHeader
	Footer       ResolvedFooter
	Versions     ResolvedVersions
	Menu         string // auto | compact | long; bottom default "auto"
	// Custom is the chain-merged + vault-overlaid free-form map.
	// See Manifest.Custom for semantics. Nil when no layer declared
	// `custom:` — templates should `{{with .Custom}}…{{end}}` or use
	// `{{index .Custom "key"}}` to tolerate the nil.
	Custom map[string]any
}

// ResolvedAuth is the post-collapse auth configuration templates consume.
// Templates branch on LoginButton via `{{if eq .Theme.Defaults.Auth.LoginButton "header"}}`.
type ResolvedAuth struct {
	// LoginButton is the resolved auth-chrome placement: "header" | "footer" |
	// "none". Never empty after Collapse — "none" is the bottom default.
	LoginButton string
	// RedirectToLogin is true when unauthenticated visitors hitting
	// auth-required content should receive a 302 to the login page rather
	// than a 404.
	RedirectToLogin bool
}

// ResolvedEditSwitch is the post-collapse view of the edit-mode switch
// configuration. Enabled is a plain bool here so templates can use
// `{{if .Theme.Defaults.EditSwitch.Enabled}}` directly.
type ResolvedEditSwitch struct {
	Enabled bool
}

// ResolvedHeader mirrors HeaderYAML for template consumption. Fields are
// optional — themes decide whether to render anything when a value is
// unset.
type ResolvedHeader struct {
	Navigation string
	Logo       string
	BrandLink  string // "" → templates fall back to "/"
	// SiteTitle is the visible header-bar brand text; "" → templates fall
	// back to the "Leyline" logotype default (never VaultName — header and
	// sidebar titles are independent).
	SiteTitle string
}

// ResolvedFooter mirrors FooterYAML for template consumption. License and
// Copyright are also duplicated as flat fields on Resolved for backward
// compatibility with the previous footer template; new templates should
// prefer the nested form.
type ResolvedFooter struct {
	Navigation string
	License    string
	Copyright  string
	BuiltWith  bool
}

// VersionsDefaults is the chain-merged versioning configuration. Pointer
// bools follow the same "unset in YAML must be distinguishable from explicit
// false" rule as ShowTitles / EditSwitch.Enabled — overlay() relies on
// nilness to decide whether a child theme has overridden the parent.
//
// Switcher controls only the dropdown UI. Default routing (`head` vs
// `latest_tag` for bare URLs) is always honored as long as the vault has a
// git repo with tags — see server.buildVaultDeps, which builds the version
// index unconditionally.
type VersionsDefaults struct {
	Switcher *bool  `yaml:"switcher"`  // render the switcher partial; UI-only
	Default  string `yaml:"default"`   // "head" | "latest_tag"; "" = unset; always-active routing knob
	ShowHead *bool  `yaml:"show_head"` // visibility of HEAD entry in the switcher
	Mode     string `yaml:"mode"`      // all_versions | only_tags | only_reviewed | only_versioned
	NavFile  string `yaml:"nav_file"`  // vault-relative path pinned to filesystem
}

// IsZero reports whether v has every field unset. Used by overlay() to
// decide whether a child layer explicitly declared a `versions:` block.
func (v VersionsDefaults) IsZero() bool {
	return v.Switcher == nil && v.Default == "" && v.ShowHead == nil &&
		v.Mode == "" && v.NavFile == ""
}

// ResolvedVersions is the post-collapse view templates consume — plain
// values with bottom defaults applied. Constructed by Collapse from the
// chain-merged VersionsDefaults.
type ResolvedVersions struct {
	Switcher bool
	Default  string // "head" | "latest_tag"
	ShowHead bool
	Mode     string // all_versions | only_tags | only_reviewed | only_versioned
	NavFile  string
}

// LoadManifest reads, parses, and validates a theme web.yaml. Pre-filling
// defaults here would defeat parent-chain inheritance (see Defaults doc), so
// fields stay zero unless explicitly declared in YAML.
func LoadManifest(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	m := &Manifest{}
	if err := yaml.Unmarshal(data, m); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	switch m.Defaults.GuestRole {
	case "", "view", "edit", "none", "propose":
	default:
		return nil, fmt.Errorf("manifest %s: invalid guest_role %q (Phase 2c accepts: view, edit, none, propose)", path, m.Defaults.GuestRole)
	}
	switch m.Defaults.Auth.LoginButton {
	case "", "header", "footer", "none":
	default:
		return nil, fmt.Errorf("manifest %s: invalid auth.login_button %q (want: header | footer | none)", path, m.Defaults.Auth.LoginButton)
	}
	switch m.Defaults.Menu {
	case "", "auto", "compact", "long":
	default:
		return nil, fmt.Errorf("manifest %s: invalid menu %q (want: auto | compact | long)", path, m.Defaults.Menu)
	}
	switch m.Defaults.TocFollow {
	case "", "drift", "pin":
	default:
		return nil, fmt.Errorf("manifest %s: invalid toc_follow %q (want: drift | pin)", path, m.Defaults.TocFollow)
	}
	if err := validateVersions(m.Versions, path); err != nil {
		return nil, err
	}
	return m, nil
}

// validateVersions checks the parsed VersionsDefaults block against the
// accepted enum values. "" passes (unset; resolved by Collapse).
func validateVersions(v VersionsDefaults, path string) error {
	switch v.Default {
	case "", "head", "latest_tag":
	default:
		return fmt.Errorf("manifest %s: invalid versions.default %q (want: head | latest_tag)", path, v.Default)
	}
	switch v.Mode {
	case "", "all_versions", "only_tags", "only_reviewed", "only_versioned":
	default:
		return fmt.Errorf("manifest %s: invalid versions.mode %q (want: all_versions | only_tags | only_reviewed | only_versioned)", path, v.Mode)
	}
	return nil
}
