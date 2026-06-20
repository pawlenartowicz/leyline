package theme

import "testing"

func TestLoadManifest_CustomParsed(t *testing.T) {
	p := writeManifest(t, `
custom:
  accent: "#5a7"
  font_size: 16
  dark_default: true
  colors:
    primary: "#000"
`)
	m, err := LoadManifest(p)
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if got := m.Custom["accent"]; got != "#5a7" {
		t.Errorf("accent = %v, want #5a7", got)
	}
	if got := m.Custom["font_size"]; got != 16 {
		t.Errorf("font_size = %v (%T), want 16 (int)", got, got)
	}
	if got := m.Custom["dark_default"]; got != true {
		t.Errorf("dark_default = %v, want true", got)
	}
	nested, ok := m.Custom["colors"].(map[string]any)
	if !ok {
		t.Fatalf("colors should parse as map[string]any, got %T", m.Custom["colors"])
	}
	if nested["primary"] != "#000" {
		t.Errorf("colors.primary = %v, want #000", nested["primary"])
	}
}

func TestLoadVaultYAML_CustomParsed(t *testing.T) {
	vault := writeVaultYAML(t, `
custom:
  accent: "#c44"
`)
	v, err := LoadVaultYAML(vault)
	if err != nil {
		t.Fatalf("LoadVaultYAML: %v", err)
	}
	if v.Custom["accent"] != "#c44" {
		t.Errorf("vault custom accent = %v, want #c44", v.Custom["accent"])
	}
}

func TestMergeCustom_BothNilReturnsNil(t *testing.T) {
	if got := MergeCustom(nil, nil); got != nil {
		t.Errorf("MergeCustom(nil, nil) = %v, want nil", got)
	}
}

func TestMergeCustom_BaseOnlyCopiedThrough(t *testing.T) {
	base := map[string]any{"a": 1, "b": 2}
	got := MergeCustom(base, nil)
	if got["a"] != 1 || got["b"] != 2 {
		t.Errorf("MergeCustom(base, nil) = %v, want %v", got, base)
	}
	// Defensive copy: mutating the result must not change base.
	got["a"] = 99
	if base["a"] != 1 {
		t.Errorf("MergeCustom must return a fresh map; base mutated: %v", base)
	}
}

func TestMergeCustom_SrcOverridesBasePerKey(t *testing.T) {
	base := map[string]any{"accent": "#5a7", "font": "Inter"}
	src := map[string]any{"accent": "#c44"} // overrides accent, leaves font
	got := MergeCustom(base, src)
	if got["accent"] != "#c44" {
		t.Errorf("src should win for accent; got %v", got["accent"])
	}
	if got["font"] != "Inter" {
		t.Errorf("base should survive for unset src key; got %v", got["font"])
	}
}

func TestMergeCustom_NestedMapReplacedWholesale(t *testing.T) {
	// Documented contract: nested maps replace wholesale (no deep merge).
	base := map[string]any{"colors": map[string]any{"primary": "#000", "secondary": "#888"}}
	src := map[string]any{"colors": map[string]any{"primary": "#fff"}} // secondary intentionally dropped
	got := MergeCustom(base, src)
	merged, ok := got["colors"].(map[string]any)
	if !ok {
		t.Fatalf("colors should remain map[string]any, got %T", got["colors"])
	}
	if _, present := merged["secondary"]; present {
		t.Errorf("nested map should replace wholesale; secondary leaked through: %v", merged)
	}
	if merged["primary"] != "#fff" {
		t.Errorf("primary = %v, want #fff", merged["primary"])
	}
}

func TestOverlayManifest_CustomMergedPerKey(t *testing.T) {
	parent := Manifest{Custom: map[string]any{"accent": "#5a7", "font": "Inter"}}
	child := Manifest{Custom: map[string]any{"accent": "#c44"}} // child overrides accent only
	merged := overlayManifest(parent, child)
	if merged.Custom["accent"] != "#c44" {
		t.Errorf("child accent should win; got %v", merged.Custom["accent"])
	}
	if merged.Custom["font"] != "Inter" {
		t.Errorf("parent font should survive; got %v", merged.Custom["font"])
	}
}

func TestOverlayManifest_CustomChildOnlyPreserved(t *testing.T) {
	parent := Manifest{}
	child := Manifest{Custom: map[string]any{"accent": "#c44"}}
	merged := overlayManifest(parent, child)
	if merged.Custom["accent"] != "#c44" {
		t.Errorf("child-only custom should land on merged; got %v", merged.Custom)
	}
}

func TestCollapse_CustomVaultOverridesTheme(t *testing.T) {
	m := Manifest{Custom: map[string]any{"accent": "#5a7", "font": "Inter"}}
	v := VaultYAML{Custom: map[string]any{"accent": "#c44"}}
	got := Collapse(m, v)
	if got.Custom["accent"] != "#c44" {
		t.Errorf("vault accent should override theme; got %v", got.Custom["accent"])
	}
	if got.Custom["font"] != "Inter" {
		t.Errorf("theme font should survive when vault doesn't set it; got %v", got.Custom["font"])
	}
}

func TestCollapse_CustomNilWhenNoLayerDeclares(t *testing.T) {
	got := Collapse(Manifest{}, VaultYAML{})
	if got.Custom != nil {
		t.Errorf("Custom should be nil when no layer declares it; got %v", got.Custom)
	}
}

func TestCollapse_CustomThemeOnlySurvivesEmptyVault(t *testing.T) {
	m := Manifest{Custom: map[string]any{"accent": "#5a7"}}
	got := Collapse(m, VaultYAML{})
	if got.Custom["accent"] != "#5a7" {
		t.Errorf("theme custom should pass through empty vault; got %v", got.Custom)
	}
}
