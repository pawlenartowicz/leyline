package theme

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"gopkg.in/yaml.v3"

	"github.com/pawlenartowicz/leyline/protocol/layout"
)

// VaultYAML is the parsed <vault>/.leyline/vaultconfig/web.yaml.
type VaultYAML struct {
	ParentTheme  string `yaml:"parent_theme"`
	GuestRole    string `yaml:"guest_role"` // "" = inherit theme default
	VaultID      string `yaml:"vault_id"`   // identity used by cross-vault @-links; empty → excluded from idMap
	VaultName    string `yaml:"vault_name"` // sidebar root + <title>; "" = special-font "Leyline" fallback
	VaultTagline string `yaml:"vault_tagline"`
	VaultHome    string `yaml:"vault_home"` // entry page; fallback README.md
	// LeftSidebar / RightSidebar override the theme's rail config for this
	// vault (replace-whole-side, no merge). Zero value inherits the theme
	// chain. See SidebarSpec for the accepted shapes.
	LeftSidebar  SidebarSpec `yaml:"left_sidebar"`
	RightSidebar SidebarSpec `yaml:"right_sidebar"`
	// PDFRenderer picks the inline-PDF viewer strategy. "" or "server"
	// rasterizes via poppler and serves page images with a text-selection
	// overlay; "browser" falls back to a same-origin <iframe> pointing at
	// the raw PDF so the user's native browser viewer (PDFium on Chrome,
	// Firefox's built-in viewer, etc.) takes over. The browser fallback
	// exists for operators who can't install poppler on their host.
	PDFRenderer string           `yaml:"pdf_renderer"`
	Versions    VersionsDefaults `yaml:"versions"`
	Header      HeaderYAML       `yaml:"header"`
	Footer      FooterYAML       `yaml:"footer"`
	Auth        AuthYAML         `yaml:"auth"`
	// Menu overrides the theme's sidebar nav-tree mode for this vault.
	// "" = inherit theme; accepted: auto | compact | long.
	Menu string `yaml:"menu"`
	// TocFollow overrides the theme's right-rail ToC scroll motion for this
	// vault. "" = inherit theme; accepted: drift | pin.
	TocFollow string `yaml:"toc_follow"`
	// TextAlign overrides the theme's prose alignment for this vault.
	// "" = inherit theme; accepted: justify | left.
	TextAlign string `yaml:"text_align"`
	// Custom is the vault-level overlay for the free-form theme-author
	// space. Merged per-key onto the chain-merged theme custom map by
	// Collapse — a key set here wins; unset keys inherit. Nested maps
	// replace wholesale. See Manifest.Custom for the wider contract.
	Custom map[string]any `yaml:"custom"`
	// Search configures the full-text search index for this vault.
	// Opt-in per vault (enabled: false by default). See SearchYAML.
	Search SearchYAML `yaml:"search"`
}

// SearchYAML is the search: block in web.yaml. All fields are optional;
// zero values produce the documented defaults.
type SearchYAML struct {
	// Enabled must be explicitly true to activate search for this vault.
	// Default: false (opt-in, conservative on footprint).
	Enabled bool `yaml:"enabled"`
	// MaxIndexBytes caps the total extracted-text size before search is
	// disabled with a logged warning. Zero → 50 MiB default.
	// Accepts plain bytes (integer) — e.g. 52428800 for 50 MiB.
	MaxIndexBytes int64 `yaml:"max_index_bytes"`
	// MinQueryLen rejects queries shorter than this many characters.
	// Zero → 2. Set to 0 to disable the floor.
	MinQueryLen int `yaml:"min_query_len"`
}

// AuthYAML is the per-vault auth override block inside web.yaml.
// LoginButton is a string ("" = inherit). RedirectToLogin is *bool so the
// vault can explicitly revert a theme's true back to false; nil = inherit.
type AuthYAML struct {
	LoginButton     string `yaml:"login_button"`      // "" = inherit; "header" | "footer" | "none"
	RedirectToLogin *bool  `yaml:"redirect_to_login"` // nil = inherit from theme chain
}

// HeaderYAML groups theme-consumed config for the page header region.
type HeaderYAML struct {
	Navigation string `yaml:"navigation"` // .nav filename under .leyline/vaultconfig/ (same format as a .nav sidebar widget)
	Logo       string `yaml:"logo"`       // vault-relative path
	BrandLink  string `yaml:"brand_link"` // brand/logo anchor href; "" → template fallback "/"
	// SiteTitle is the visible brand text in the header bar. "" → the
	// template's "Leyline" logotype default. Independent of vault_name,
	// which drives the sidebar root, browser-tab <title>, and vault identity.
	SiteTitle string `yaml:"site_title"`
}

// FooterYAML groups theme-consumed config for the page footer region.
// License/Copyright are free text; both optional.
type FooterYAML struct {
	Navigation string `yaml:"navigation"`
	License    string `yaml:"license"`
	Copyright  string `yaml:"copyright"`
	BuiltWith  bool   `yaml:"built_with"`
}

// LoadVaultYAML reads <vaultDir>/.leyline/vaultconfig/web.yaml. Absent file →
// zero value, nil error. Other I/O or parse errors are returned.
func LoadVaultYAML(vaultDir string) (VaultYAML, error) {
	p := layout.WebYAMLFile(vaultDir)
	data, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return VaultYAML{}, nil
		}
		return VaultYAML{}, fmt.Errorf("read vault web.yaml: %w", err)
	}
	var v VaultYAML
	if err := yaml.Unmarshal(data, &v); err != nil {
		return VaultYAML{}, fmt.Errorf("parse vault web.yaml at %s: %w", p, err)
	}
	switch v.GuestRole {
	case "", "view", "edit", "none", "propose":
	default:
		return VaultYAML{}, fmt.Errorf("vault web.yaml at %s: invalid guest_role %q (Phase 2c accepts: view, edit, none, propose)", p, v.GuestRole)
	}
	switch v.PDFRenderer {
	case "", "server", "browser":
	default:
		return VaultYAML{}, fmt.Errorf("vault web.yaml at %s: invalid pdf_renderer %q (must be \"server\" or \"browser\")", p, v.PDFRenderer)
	}
	switch v.Auth.LoginButton {
	case "", "header", "footer", "none":
	default:
		return VaultYAML{}, fmt.Errorf("vault web.yaml at %s: invalid auth.login_button %q (want: header | footer | none)", p, v.Auth.LoginButton)
	}
	switch v.Menu {
	case "", "auto", "compact", "long":
	default:
		return VaultYAML{}, fmt.Errorf("vault web.yaml at %s: invalid menu %q (want: auto | compact | long)", p, v.Menu)
	}
	switch v.TocFollow {
	case "", "drift", "pin":
	default:
		return VaultYAML{}, fmt.Errorf("vault web.yaml at %s: invalid toc_follow %q (want: drift | pin)", p, v.TocFollow)
	}
	switch v.TextAlign {
	case "", "justify", "left":
	default:
		return VaultYAML{}, fmt.Errorf("vault web.yaml at %s: invalid text_align %q (want: justify | left)", p, v.TextAlign)
	}
	if err := validateVersions(v.Versions, p); err != nil {
		return VaultYAML{}, err
	}
	return v, nil
}

// Collapse turns the chain-merged Manifest plus the vault yaml into a
// template-ready Resolved. Dereferences pointers, applies bottom defaults, and
// applies vault overrides for cascading values (today: guest_role).
//
// Theme-template flags (ShowTitles, Versions) live at Manifest top level and
// are never vault-overridable. Cascading values come from Manifest.Defaults
// with vault overrides applied here.
//
// VaultName, License, Copyright, Header, and Footer are vault-direct fields
// the caller (server.New) fills onto Resolved after Collapse returns —
// VaultName intentionally stays "" when unset so templates can render their
// special-font fallback instead of a plain string.
func Collapse(m Manifest, vault VaultYAML) Resolved {
	guest := m.Defaults.GuestRole
	if vault.GuestRole != "" {
		guest = vault.GuestRole
	}
	// Vault `versions:` block overrides the chain-merged theme defaults
	// wholesale when present (same convention as Defaults — sub-field
	// inheritance is deferred). Empty vault block falls through to theme.
	versions := m.Versions
	if !vault.Versions.IsZero() {
		versions = vault.Versions
	}

	resolvedGuestRole := orString(guest, "view")

	menu := m.Defaults.Menu
	if vault.Menu != "" {
		menu = vault.Menu
	}

	tocFollow := m.Defaults.TocFollow
	if vault.TocFollow != "" {
		tocFollow = vault.TocFollow
	}

	textAlign := m.Defaults.TextAlign
	if vault.TextAlign != "" {
		textAlign = vault.TextAlign
	}

	return Resolved{
		ShowTitles: derefBool(m.ShowTitles, true),
		GuestRole:  resolvedGuestRole,
		EditSwitch: ResolvedEditSwitch{
			Enabled: derefBool(m.Defaults.EditSwitch.Enabled, false),
		},
		Auth: resolveAuth(m.Defaults.Auth, vault.Auth),
		Versions: ResolvedVersions{
			Switcher: derefBool(versions.Switcher, false),
			Default:  orString(versions.Default, "head"),
			ShowHead: derefBool(versions.ShowHead, true),
			Mode:     orString(versions.Mode, "all_versions"),
			NavFile:  versions.NavFile,
		},
		Menu:         orString(menu, "auto"),
		LeftSidebar:  resolveSidebar(m.Defaults.LeftSidebar, vault.LeftSidebar),
		RightSidebar: resolveSidebar(m.Defaults.RightSidebar, vault.RightSidebar),
		TocFollow:    orString(tocFollow, "drift"),
		TextAlign:    orString(textAlign, "justify"),
		Custom:       MergeCustom(m.Custom, vault.Custom),
	}
}

// MergeCustom returns a per-key overlay of src onto base — a new map where
// every base key carries over unless src declares the same key, in which
// case src wins. Both inputs may be nil; the result is nil when both are.
// Nested maps replace wholesale (no deep merge), matching the rest of the
// overlay convention.
func MergeCustom(base, src map[string]any) map[string]any {
	if len(base) == 0 && len(src) == 0 {
		return nil
	}
	out := make(map[string]any, len(base)+len(src))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range src {
		out[k] = v
	}
	return out
}

// resolveAuth computes ResolvedAuth from the chain-merged theme AuthDefaults
// and the vault AuthYAML override layer.
//
// Precedence for LoginButton:
//  1. vault.Auth.LoginButton (if non-empty)
//  2. themeAuth.LoginButton (if non-empty)
//  3. "none" (no login chrome)
//
// Precedence for RedirectToLogin:
//  1. vault.Auth.RedirectToLogin (if non-nil)
//  2. themeAuth.RedirectToLogin (plain bool bottom default: false)
func resolveAuth(themeAuth AuthDefaults, vaultAuth AuthYAML) ResolvedAuth {
	redirectToLogin := themeAuth.RedirectToLogin
	if vaultAuth.RedirectToLogin != nil {
		redirectToLogin = *vaultAuth.RedirectToLogin
	}

	loginButton := "none"
	if themeAuth.LoginButton != "" {
		loginButton = themeAuth.LoginButton
	}
	if vaultAuth.LoginButton != "" {
		loginButton = vaultAuth.LoginButton
	}

	return ResolvedAuth{
		LoginButton:     loginButton,
		RedirectToLogin: redirectToLogin,
	}
}

func derefBool(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

func orString(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
