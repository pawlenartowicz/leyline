package theme

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func makeTheme(t *testing.T, root, name, manifestBody string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Join(dir, "theme", "templates"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "web.yaml"), []byte(manifestBody), 0644); err != nil {
		t.Fatal(err)
	}
	body := []byte("served-by: " + name)
	if err := os.WriteFile(filepath.Join(dir, "theme", "templates", "marker.html"), body, 0644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadRegistry_SingleTheme(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root, "_base", "defaults:\n  guest_role: view\n")
	r, err := LoadRegistry(root)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if _, ok := r.Get("_base"); !ok {
		t.Fatal("expected _base to be registered")
	}
}

func TestLoadRegistry_ParentChain(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root, "_base", "defaults:\n  guest_role: view\n")
	makeTheme(t, root, "static_notes", "parent_theme: _base\ndefaults:\n  guest_role: view\n")
	makeTheme(t, root, "child", "parent_theme: static_notes\ndefaults:\n  guest_role: view\n")

	r, err := LoadRegistry(root)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	chain, err := r.Chain("child")
	if err != nil {
		t.Fatalf("Chain: %v", err)
	}
	want := []string{"child", "static_notes", "_base"}
	if len(chain) != 3 {
		t.Fatalf("chain length = %d, want 3 (got %v)", len(chain), themeNames(chain))
	}
	for i, want := range want {
		if chain[i].Name != want {
			t.Errorf("chain[%d] = %q, want %q", i, chain[i].Name, want)
		}
	}
}

func TestLoadRegistry_RejectsCycle(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root, "a", "parent_theme: b\n")
	makeTheme(t, root, "b", "parent_theme: a\n")

	_, err := LoadRegistry(root)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error %q should mention cycle", err)
	}
}

// TestLoadRegistry_RejectsThreeNodeCycle extends TestLoadRegistry_RejectsCycle
// with a 3-node cycle (A→B→C→A) to verify the cycle detector handles
// indirect cycles correctly.
func TestLoadRegistry_RejectsThreeNodeCycle(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root, "alpha", "parent_theme: beta\n")
	makeTheme(t, root, "beta", "parent_theme: gamma\n")
	makeTheme(t, root, "gamma", "parent_theme: alpha\n")

	_, err := LoadRegistry(root)
	if err == nil {
		t.Fatal("expected cycle error for 3-node cycle A→B→C→A")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error %q should mention cycle", err)
	}
}

func TestLoadRegistry_RejectsMissingParent(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root, "child", "parent_theme: ghost\n")

	_, err := LoadRegistry(root)
	if err == nil {
		t.Fatal("expected error for missing parent")
	}
}

func TestRegistry_FileLookup(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root, "_base", "")
	makeTheme(t, root, "child", "parent_theme: _base\n")
	if err := os.WriteFile(
		filepath.Join(root, "child", "theme", "templates", "marker.html"),
		[]byte("served-by: child-override"), 0644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "_base", "theme", "templates", "only-in-base.html"),
		[]byte("base"), 0644,
	); err != nil {
		t.Fatal(err)
	}

	r, err := LoadRegistry(root)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}

	loc, err := r.ResolveFile("child", "", "templates/marker.html")
	if err != nil {
		t.Fatalf("ResolveFile marker: %v", err)
	}
	got, _ := os.ReadFile(loc)
	if string(got) != "served-by: child-override" {
		t.Errorf("marker.html resolved to %q (path %q), want child-override", got, loc)
	}

	loc, err = r.ResolveFile("child", "", "templates/only-in-base.html")
	if err != nil {
		t.Fatalf("ResolveFile only-in-base: %v", err)
	}
	got, _ = os.ReadFile(loc)
	if string(got) != "base" {
		t.Errorf("only-in-base.html resolved to %q (path %q), want base", got, loc)
	}

	if _, err := r.ResolveFile("child", "", "templates/no-such.html"); err == nil {
		t.Error("expected not-found error")
	}
}

func TestRegistry_VaultOverrideWins(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root, "_base", "")
	makeTheme(t, root, "child", "parent_theme: _base\n")

	vault := t.TempDir()
	overrideDir := filepath.Join(vault, ".leyline", "vaultconfig", "theme", "templates")
	if err := os.MkdirAll(overrideDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(overrideDir, "marker.html"),
		[]byte("served-by: vault-override"), 0644); err != nil {
		t.Fatal(err)
	}

	r, err := LoadRegistry(root)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	loc, err := r.ResolveFile("child", vault, "templates/marker.html")
	if err != nil {
		t.Fatalf("ResolveFile: %v", err)
	}
	got, _ := os.ReadFile(loc)
	if string(got) != "served-by: vault-override" {
		t.Errorf("vault override not honoured: served %q from %q", got, loc)
	}
}

func themeNames(ts []*Theme) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name
	}
	return out
}

func TestResolveChain_SingleTheme(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root, "solo", "show_titles: false\ndefaults:\n  guest_role: none\n")
	r, err := LoadRegistry(root)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	merged, err := r.ResolveChain("solo")
	if err != nil {
		t.Fatalf("ResolveChain: %v", err)
	}
	if merged.ShowTitles == nil || *merged.ShowTitles {
		t.Errorf("ShowTitles = %v, want explicit *false", merged.ShowTitles)
	}
	if merged.Defaults.GuestRole != "none" {
		t.Errorf("GuestRole = %q, want none", merged.Defaults.GuestRole)
	}
}

func TestResolveChain_ChildInheritsSilentField(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root, "_base", "show_titles: false\ndefaults:\n  guest_role: none\n")
	makeTheme(t, root, "child", "parent_theme: _base\n")
	r, err := LoadRegistry(root)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	merged, err := r.ResolveChain("child")
	if err != nil {
		t.Fatalf("ResolveChain: %v", err)
	}
	if merged.ShowTitles == nil || *merged.ShowTitles {
		t.Errorf("ShowTitles = %v, want inherited *false", merged.ShowTitles)
	}
	if merged.Defaults.GuestRole != "none" {
		t.Errorf("GuestRole = %q, want inherited 'none'", merged.Defaults.GuestRole)
	}
}

func TestResolveChain_ChildOverridesParent(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root, "_base", "show_titles: false\n")
	makeTheme(t, root, "child", "parent_theme: _base\nshow_titles: true\n")
	r, err := LoadRegistry(root)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	merged, err := r.ResolveChain("child")
	if err != nil {
		t.Fatalf("ResolveChain: %v", err)
	}
	if merged.ShowTitles == nil || !*merged.ShowTitles {
		t.Errorf("ShowTitles = %v, want child's *true to win", merged.ShowTitles)
	}
}

// TestResolveChain_GuestRole exercises the inheritance machinery on a
// theme-side string scalar. (SiteName used to fill this slot; it is now
// deleted, so the test was retargeted to GuestRole — the only remaining
// overridable string scalar that follows the same overlay semantics.)
func TestResolveChain_GuestRole(t *testing.T) {
	t.Run("inherited from grandparent", func(t *testing.T) {
		root := t.TempDir()
		makeTheme(t, root, "_base", "defaults:\n  guest_role: none\n")
		makeTheme(t, root, "mid", "parent_theme: _base\n")
		makeTheme(t, root, "leaf", "parent_theme: mid\n")
		reg, err := LoadRegistry(root)
		if err != nil {
			t.Fatal(err)
		}
		merged, err := reg.ResolveChain("leaf")
		if err != nil {
			t.Fatal(err)
		}
		if merged.Defaults.GuestRole != "none" {
			t.Errorf("GuestRole = %q, want none (inherited from grandparent)", merged.Defaults.GuestRole)
		}
	})

	t.Run("child overrides parent", func(t *testing.T) {
		root := t.TempDir()
		makeTheme(t, root, "_base", "defaults:\n  guest_role: none\n")
		makeTheme(t, root, "mid", "parent_theme: _base\ndefaults:\n  guest_role: none\n")
		makeTheme(t, root, "leaf", "parent_theme: mid\ndefaults:\n  guest_role: view\n")
		reg, err := LoadRegistry(root)
		if err != nil {
			t.Fatal(err)
		}
		merged, err := reg.ResolveChain("leaf")
		if err != nil {
			t.Fatal(err)
		}
		if merged.Defaults.GuestRole != "view" {
			t.Errorf("GuestRole = %q, want view (child wins)", merged.Defaults.GuestRole)
		}
	})

	t.Run("unset stays empty after Collapse default applies", func(t *testing.T) {
		root := t.TempDir()
		makeTheme(t, root, "_base", "defaults: {}\n")
		reg, err := LoadRegistry(root)
		if err != nil {
			t.Fatal(err)
		}
		merged, err := reg.ResolveChain("_base")
		if err != nil {
			t.Fatal(err)
		}
		res := Collapse(merged, VaultYAML{})
		if res.GuestRole != "view" {
			t.Errorf("GuestRole bottom default = %q, want view", res.GuestRole)
		}
	})
}

func TestResolveChain_AllSilentYieldsBottomDefaults(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root, "_base", "")
	makeTheme(t, root, "child", "parent_theme: _base\n")
	r, err := LoadRegistry(root)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	merged, err := r.ResolveChain("child")
	if err != nil {
		t.Fatalf("ResolveChain: %v", err)
	}
	res := Collapse(merged, VaultYAML{})
	if res.ShowTitles != true {
		t.Errorf("ShowTitles bottom default = %v, want true", res.ShowTitles)
	}
	if res.GuestRole != "view" {
		t.Errorf("GuestRole bottom default = %q, want view", res.GuestRole)
	}
}

func TestResolveChain_MissingThemeError(t *testing.T) {
	root := t.TempDir()
	makeTheme(t, root, "_base", "")
	r, err := LoadRegistry(root)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}
	if _, err := r.ResolveChain("ghost"); err == nil {
		t.Fatal("expected error for missing theme")
	}
}
