package webignore

import "testing"

func TestDispatch_BuiltinExtensions(t *testing.T) {
	d := NewDispatch(nil)

	cases := []struct {
		path string
		mode Mode
		ok   bool
	}{
		{"notes/today.md", ModeMarkdown, true},
		{"notes/report.typ", ModeTypst, true},
		{"notes/REPORT.TYP", ModeTypst, true},
		{"images/cover.png", ModeAsset, true},
		{"images/cover.PNG", ModeAsset, true},
		{"images/anim.gif", ModeAsset, true},
		{"images/photo.JPG", ModeAsset, true},
		{"art/scene.webp", ModeAsset, true},
		{"diagram.svg", ModeAsset, true},
		{"papers/intro.pdf", ModeAsset, true},
		{"papers/INTRO.PDF", ModeAsset, true},
		{"export/page.html", ModeHTML, true},
		{"export/PAGE.HTML", ModeHTML, true},
		{"binary.bin", "", false},
		{"data.json", "", false},
		{"no-extension", "", false},
	}
	for _, c := range cases {
		mode, ok := d.Mode(c.path)
		if ok != c.ok || mode != c.mode {
			t.Errorf("Mode(%q) = (%q, %v), want (%q, %v)", c.path, mode, ok, c.mode, c.ok)
		}
	}
}

func TestDispatch_TextExtensionsFromConfig(t *testing.T) {
	d := NewDispatch([]string{".tex", ".py", ".json", ".yaml"})

	cases := []struct {
		path string
		mode Mode
		ok   bool
	}{
		{"thesis.tex", ModeText, true},
		{"build.py", ModeText, true},
		{"config.json", ModeText, true},
		{"manifest.yaml", ModeText, true},
		{"manifest.YAML", ModeText, true},
		{"data.csv", ModeTabular, true},
	}
	for _, c := range cases {
		mode, ok := d.Mode(c.path)
		if ok != c.ok || mode != c.mode {
			t.Errorf("Mode(%q) = (%q, %v), want (%q, %v)", c.path, mode, ok, c.mode, c.ok)
		}
	}
}

func TestDispatch_BuiltinTakesPrecedenceOverText(t *testing.T) {
	d := NewDispatch([]string{".md", ".png"})

	if mode, ok := d.Mode("a.md"); !ok || mode != ModeMarkdown {
		t.Errorf("Mode(a.md) = (%q, %v), want (markdown, true)", mode, ok)
	}
	if mode, ok := d.Mode("a.png"); !ok || mode != ModeAsset {
		t.Errorf("Mode(a.png) = (%q, %v), want (asset, true)", mode, ok)
	}
}

func TestDispatch_NormalizesExtensionForm(t *testing.T) {
	d := NewDispatch([]string{"py", ".tex"})

	if _, ok := d.Mode("a.py"); !ok {
		t.Error("py without dot should normalize to .py")
	}
	if _, ok := d.Mode("a.tex"); !ok {
		t.Error(".tex with dot should work")
	}
}

func TestDispatch_TabularBuiltinExtensions(t *testing.T) {
	d := NewDispatch(nil)

	cases := []struct {
		path string
		mode Mode
		ok   bool
	}{
		{"data/sample.csv", ModeTabular, true},
		{"data/SAMPLE.CSV", ModeTabular, true},
		{"data/sample.tsv", ModeTabular, true},
		{"data/sample.TSV", ModeTabular, true},
		{"data/sample.psv", ModeTabular, true},
		{"data/sample.PSV", ModeTabular, true},
	}
	for _, c := range cases {
		mode, ok := d.Mode(c.path)
		if ok != c.ok || mode != c.mode {
			t.Errorf("Mode(%q) = (%q, %v), want (%q, %v)", c.path, mode, ok, c.mode, c.ok)
		}
	}
}

func TestDispatch_TabularBuiltinBeatsTextExtensions(t *testing.T) {
	// Operator listing csv/tsv in text_extensions must not downgrade
	// the built-in tabular routing — ModeTabular still wins.
	d := NewDispatch([]string{".csv", ".tsv"})
	for _, ext := range []string{"a.csv", "b.tsv"} {
		mode, ok := d.Mode(ext)
		if !ok || mode != ModeTabular {
			t.Errorf("Mode(%q) = (%q, %v), want (ModeTabular, true)", ext, mode, ok)
		}
	}
}
