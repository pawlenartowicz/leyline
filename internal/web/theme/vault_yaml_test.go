package theme

import (
	"os"
	"path/filepath"
	"testing"
)

func writeVaultYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	leylineDir := filepath.Join(dir, ".leyline", "vaultconfig")
	if err := os.MkdirAll(leylineDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leylineDir, "web.yaml"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadVaultYAML_Present(t *testing.T) {
	vault := writeVaultYAML(t, `
parent_theme: documentation
guest_role: view
`)
	v, err := LoadVaultYAML(vault)
	if err != nil {
		t.Fatalf("LoadVaultYAML: %v", err)
	}
	if v.ParentTheme != "documentation" {
		t.Errorf("ParentTheme = %q", v.ParentTheme)
	}
	if v.GuestRole != "view" {
		t.Errorf("GuestRole = %q", v.GuestRole)
	}
}

func TestLoadVaultYAML_Absent(t *testing.T) {
	dir := t.TempDir()
	v, err := LoadVaultYAML(dir)
	if err != nil {
		t.Fatalf("LoadVaultYAML on missing file: %v (should be nil err, zero value VaultYAML)", err)
	}
	if v.ParentTheme != "" || v.GuestRole != "" {
		t.Errorf("absent file should yield zero VaultYAML, got %+v", v)
	}
}

func TestLoadVaultYAML_RejectsBadGuestRole(t *testing.T) {
	vault := writeVaultYAML(t, "guest_role: superuser\n")
	if _, err := LoadVaultYAML(vault); err == nil {
		t.Fatal("expected error for invalid guest_role")
	}
}

func TestLoadVaultYAML_FooterFields(t *testing.T) {
	vault := writeVaultYAML(t, `
footer:
  license: "CC BY-SA 4.0"
  copyright: "© 2026 Pawel Lenartowicz"
`)
	v, err := LoadVaultYAML(vault)
	if err != nil {
		t.Fatalf("LoadVaultYAML: %v", err)
	}
	if v.Footer.License != "CC BY-SA 4.0" {
		t.Errorf("Footer.License = %q", v.Footer.License)
	}
	if v.Footer.Copyright != "© 2026 Pawel Lenartowicz" {
		t.Errorf("Footer.Copyright = %q", v.Footer.Copyright)
	}
}

func TestLoadVaultYAML_FooterAbsent(t *testing.T) {
	vault := writeVaultYAML(t, "guest_role: view\n")
	v, err := LoadVaultYAML(vault)
	if err != nil {
		t.Fatalf("LoadVaultYAML: %v", err)
	}
	if v.Footer.License != "" || v.Footer.Copyright != "" {
		t.Errorf("absent footer block should yield zero FooterYAML, got %+v", v.Footer)
	}
}

func TestLoadVaultYAML_HeaderFields(t *testing.T) {
	vault := writeVaultYAML(t, `
vault_id: research
vault_tagline: "Notes and projects"
vault_home: index.md
header:
  navigation: nav
  logo: assets/logo.png
  site_title: "Research Lab"
`)
	v, err := LoadVaultYAML(vault)
	if err != nil {
		t.Fatalf("LoadVaultYAML: %v", err)
	}
	if v.VaultID != "research" {
		t.Errorf("VaultID = %q", v.VaultID)
	}
	if v.VaultTagline != "Notes and projects" {
		t.Errorf("VaultTagline = %q", v.VaultTagline)
	}
	if v.VaultHome != "index.md" {
		t.Errorf("VaultHome = %q", v.VaultHome)
	}
	if v.Header.Navigation != "nav" {
		t.Errorf("Header.Navigation = %q", v.Header.Navigation)
	}
	if v.Header.Logo != "assets/logo.png" {
		t.Errorf("Header.Logo = %q", v.Header.Logo)
	}
	if v.Header.SiteTitle != "Research Lab" {
		t.Errorf("Header.SiteTitle = %q", v.Header.SiteTitle)
	}
}

func TestLoadVaultYAML_SidebarScalarReferences(t *testing.T) {
	vault := writeVaultYAML(t, "right_sidebar: references\n")
	v, err := LoadVaultYAML(vault)
	if err != nil {
		t.Fatalf("LoadVaultYAML: %v", err)
	}
	if v.RightSidebar.Mode != SidebarReferences {
		t.Errorf("RightSidebar.Mode = %q, want %q", v.RightSidebar.Mode, SidebarReferences)
	}
	if len(v.RightSidebar.Widgets) != 0 {
		t.Errorf("RightSidebar.Widgets = %v, want empty", v.RightSidebar.Widgets)
	}
}

func TestLoadVaultYAML_SidebarWidgetStack(t *testing.T) {
	vault := writeVaultYAML(t, "left_sidebar: [navigation]\nright_sidebar: [table_of_content, related.md]\n")
	v, err := LoadVaultYAML(vault)
	if err != nil {
		t.Fatalf("LoadVaultYAML: %v", err)
	}
	if v.LeftSidebar.Mode != SidebarWidgets || len(v.LeftSidebar.Widgets) != 1 || v.LeftSidebar.Widgets[0] != "navigation" {
		t.Errorf("LeftSidebar = %+v, want widgets [navigation]", v.LeftSidebar)
	}
	want := []string{"table_of_content", "related.md"}
	if v.RightSidebar.Mode != SidebarWidgets || len(v.RightSidebar.Widgets) != 2 ||
		v.RightSidebar.Widgets[0] != want[0] || v.RightSidebar.Widgets[1] != want[1] {
		t.Errorf("RightSidebar.Widgets = %v, want %v", v.RightSidebar.Widgets, want)
	}
}

func TestLoadVaultYAML_RejectsSentinelInList(t *testing.T) {
	vault := writeVaultYAML(t, "right_sidebar: [table_of_content, references]\n")
	if _, err := LoadVaultYAML(vault); err == nil {
		t.Fatal("expected error: references is a mode, not a stackable widget")
	}
}

func TestLoadVaultYAML_RejectsUnknownBuiltin(t *testing.T) {
	vault := writeVaultYAML(t, "left_sidebar: [sitemap]\n") // no extension, not a builtin
	if _, err := LoadVaultYAML(vault); err == nil {
		t.Fatal("expected error: 'sitemap' is neither a builtin nor a file (no extension)")
	}
}

func TestLoadVaultYAML_RejectsBadScalar(t *testing.T) {
	vault := writeVaultYAML(t, "left_sidebar: navigation\n") // widget name as scalar
	if _, err := LoadVaultYAML(vault); err == nil {
		t.Fatal("expected error: scalar must be body/none/references")
	}
}

func TestLoadVaultYAML_SidebarAbsent(t *testing.T) {
	vault := writeVaultYAML(t, "guest_role: view\n")
	v, err := LoadVaultYAML(vault)
	if err != nil {
		t.Fatalf("LoadVaultYAML: %v", err)
	}
	if !v.LeftSidebar.IsZero() || !v.RightSidebar.IsZero() {
		t.Errorf("absent sidebars should be zero; got left=%+v right=%+v", v.LeftSidebar, v.RightSidebar)
	}
}

func TestCollapse_SidebarBottomDefaultNone(t *testing.T) {
	got := Collapse(Manifest{}, VaultYAML{})
	if got.LeftSidebar.Mode != SidebarNone || got.RightSidebar.Mode != SidebarNone {
		t.Errorf("bottom default = left %q / right %q, want both %q",
			got.LeftSidebar.Mode, got.RightSidebar.Mode, SidebarNone)
	}
}

func TestCollapse_VaultSidebarOverridesTheme(t *testing.T) {
	m := Manifest{Defaults: Defaults{
		LeftSidebar: SidebarSpec{Mode: SidebarWidgets, Widgets: []string{"navigation"}},
	}}
	got := Collapse(m, VaultYAML{LeftSidebar: SidebarSpec{Mode: SidebarBody}})
	if got.LeftSidebar.Mode != SidebarBody {
		t.Errorf("LeftSidebar.Mode = %q, want %q (vault override)", got.LeftSidebar.Mode, SidebarBody)
	}
}

func TestCollapse_TocFollowBottomDefaultDrift(t *testing.T) {
	got := Collapse(Manifest{}, VaultYAML{})
	if got.TocFollow != "drift" {
		t.Errorf("TocFollow bottom default = %q, want %q", got.TocFollow, "drift")
	}
}

func TestCollapse_VaultTocFollowOverridesTheme(t *testing.T) {
	m := Manifest{Defaults: Defaults{TocFollow: "drift"}}
	got := Collapse(m, VaultYAML{TocFollow: "pin"})
	if got.TocFollow != "pin" {
		t.Errorf("TocFollow = %q, want %q (vault override)", got.TocFollow, "pin")
	}
}

func TestLoadVaultYAML_TocFollowInvalid(t *testing.T) {
	vault := writeVaultYAML(t, "toc_follow: sideways\n")
	if _, err := LoadVaultYAML(vault); err == nil {
		t.Error("expected error for invalid toc_follow, got nil")
	}
}

func TestCollapse_VaultInheritsThemeSidebar(t *testing.T) {
	m := Manifest{Defaults: Defaults{
		LeftSidebar: SidebarSpec{Mode: SidebarWidgets, Widgets: []string{"navigation"}},
	}}
	got := Collapse(m, VaultYAML{}) // vault unset → inherit theme
	if got.LeftSidebar.Mode != SidebarWidgets || len(got.LeftSidebar.Widgets) != 1 {
		t.Errorf("LeftSidebar = %+v, want inherited widgets [navigation]", got.LeftSidebar)
	}
}

func boolPtr(b bool) *bool { return &b }

func TestCollapse_VaultOverridesGuestRole(t *testing.T) {
	m := Manifest{
		ShowTitles: boolPtr(false),
		Defaults:   Defaults{GuestRole: "view"},
	}
	got := Collapse(m, VaultYAML{GuestRole: "none"})
	if got.GuestRole != "none" {
		t.Errorf("GuestRole = %q, want none (vault override)", got.GuestRole)
	}
	if got.ShowTitles {
		t.Errorf("ShowTitles should remain false (theme-flag, not vault-overridable)")
	}
}

func TestCollapse_EmptyVaultYieldsTheme(t *testing.T) {
	m := Manifest{
		ShowTitles: boolPtr(true),
		Defaults:   Defaults{GuestRole: "view"},
	}
	got := Collapse(m, VaultYAML{})
	if got.GuestRole != "view" || !got.ShowTitles {
		t.Errorf("Collapse(theme, empty) = %+v, want theme values preserved", got)
	}
}

func TestCollapse_PreservesEmpty(t *testing.T) {
	got := Collapse(Manifest{}, VaultYAML{})
	if got.VaultName != "" {
		t.Errorf("VaultName bottom default = %q, want \"\" (templates render the special-font fallback)", got.VaultName)
	}
}

func TestCollapse_BottomDefaults(t *testing.T) {
	got := Collapse(Manifest{}, VaultYAML{})
	if !got.ShowTitles {
		t.Errorf("ShowTitles bottom default should be true, got false")
	}
	if got.GuestRole != "view" {
		t.Errorf("GuestRole bottom default = %q, want view", got.GuestRole)
	}
}

func TestCollapse_HonoursExplicitFalse(t *testing.T) {
	got := Collapse(Manifest{ShowTitles: boolPtr(false)}, VaultYAML{})
	if got.ShowTitles {
		t.Errorf("ShowTitles explicit *false should resolve to false")
	}
}

// --- Auth resolution tests ---

func TestCollapse_AuthThemeLoginButtonHeader(t *testing.T) {
	m := Manifest{Defaults: Defaults{Auth: AuthDefaults{LoginButton: "header"}}}
	got := Collapse(m, VaultYAML{})
	if got.Auth.LoginButton != "header" {
		t.Errorf("theme LoginButton=header should resolve to header, got %q", got.Auth.LoginButton)
	}
}

func TestCollapse_AuthThemeLoginButtonFooter(t *testing.T) {
	m := Manifest{Defaults: Defaults{Auth: AuthDefaults{LoginButton: "footer"}}}
	got := Collapse(m, VaultYAML{})
	if got.Auth.LoginButton != "footer" {
		t.Errorf("theme LoginButton=footer should resolve to footer, got %q", got.Auth.LoginButton)
	}
}

func TestCollapse_AuthLoginButtonDefaultsToNone(t *testing.T) {
	// No LoginButton declared anywhere → bottom default "none".
	got := Collapse(Manifest{}, VaultYAML{})
	if got.Auth.LoginButton != "none" {
		t.Errorf("LoginButton bottom default should be \"none\", got %q", got.Auth.LoginButton)
	}
}

func TestCollapse_AuthRedirectToLoginDefault(t *testing.T) {
	got := Collapse(Manifest{}, VaultYAML{})
	if got.Auth.RedirectToLogin {
		t.Errorf("RedirectToLogin bottom default should be false")
	}
}

func TestCollapse_AuthVaultOverridesLoginButton(t *testing.T) {
	// Theme says header; vault says footer → vault wins.
	m := Manifest{Defaults: Defaults{Auth: AuthDefaults{LoginButton: "header"}}}
	v := VaultYAML{Auth: AuthYAML{LoginButton: "footer"}}
	got := Collapse(m, v)
	if got.Auth.LoginButton != "footer" {
		t.Errorf("vault LoginButton=footer should override theme header, got %q", got.Auth.LoginButton)
	}
}

func TestCollapse_AuthVaultOverridesRedirectToLogin(t *testing.T) {
	// Theme sets redirect_to_login: true; vault sets it back to false.
	m := Manifest{Defaults: Defaults{Auth: AuthDefaults{RedirectToLogin: true}}}
	v := VaultYAML{Auth: AuthYAML{RedirectToLogin: boolPtr(false)}}
	got := Collapse(m, v)
	if got.Auth.RedirectToLogin {
		t.Errorf("vault RedirectToLogin=*false should override theme true")
	}
}

func TestCollapse_AuthVaultUnsetInheritsTheme(t *testing.T) {
	m := Manifest{Defaults: Defaults{Auth: AuthDefaults{
		LoginButton:     "header",
		RedirectToLogin: true,
	}}}
	got := Collapse(m, VaultYAML{})
	if got.Auth.LoginButton != "header" {
		t.Errorf("vault absent: LoginButton should inherit theme \"header\", got %q", got.Auth.LoginButton)
	}
	if !got.Auth.RedirectToLogin {
		t.Errorf("vault absent: RedirectToLogin should inherit theme true")
	}
}

func TestLoadVaultYAML_AuthBlock(t *testing.T) {
	vault := writeVaultYAML(t, `
auth:
  login_button: header
  redirect_to_login: false
`)
	v, err := LoadVaultYAML(vault)
	if err != nil {
		t.Fatalf("LoadVaultYAML: %v", err)
	}
	if v.Auth.LoginButton != "header" {
		t.Errorf("Auth.LoginButton = %q, want \"header\"", v.Auth.LoginButton)
	}
	if v.Auth.RedirectToLogin == nil || *v.Auth.RedirectToLogin {
		t.Errorf("Auth.RedirectToLogin = %v, want *false", v.Auth.RedirectToLogin)
	}
}

func TestLoadVaultYAML_AuthBlockAbsent(t *testing.T) {
	vault := writeVaultYAML(t, "guest_role: view\n")
	v, err := LoadVaultYAML(vault)
	if err != nil {
		t.Fatalf("LoadVaultYAML: %v", err)
	}
	if v.Auth.LoginButton != "" {
		t.Errorf("absent auth block: LoginButton should be \"\", got %q", v.Auth.LoginButton)
	}
	if v.Auth.RedirectToLogin != nil {
		t.Errorf("absent auth block: RedirectToLogin should be nil, got %v", v.Auth.RedirectToLogin)
	}
}

func TestLoadVaultYAML_AuthLoginButtonInvalid(t *testing.T) {
	vault := writeVaultYAML(t, `
auth:
  login_button: sidebar
`)
	if _, err := LoadVaultYAML(vault); err == nil {
		t.Fatal("expected error for invalid login_button, got nil")
	}
}
