package server

import (
	"html/template"
	"testing"
	"time"

	"github.com/pawlenartowicz/leyline/internal/web/render"
)

func TestPageContext_Fields(t *testing.T) {
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	ctx := PageContext{
		Title:       "Hello",
		Aliases:     []string{"hi"},
		Tags:        []string{"a"},
		Frontmatter: map[string]any{"extra": 1},
		Content:     template.HTML("<p>body</p>"),
		Path:        "notes/today.md",
		URL:         "/notes/today",
		Vault:       VaultInfo{Name: "notes", Prefix: "/notes", GuestRole: "view"},
		Theme:       ThemeInfo{Name: "static_notes"},
		Now:         now,
	}
	if ctx.Title != "Hello" || ctx.Vault.Name != "notes" || string(ctx.Content) != "<p>body</p>" {
		t.Errorf("unexpected fields in %+v", ctx)
	}
}

func TestNewPageContext_FromFrontmatter(t *testing.T) {
	fm := render.Frontmatter{
		Title:   "Today",
		Aliases: []string{"hoy"},
		Tags:    []string{"daily"},
		Raw:     map[string]any{"extra": "x"},
	}
	ctx := NewPageContext(fm,
		template.HTML("<p>x</p>"),
		"",
		"notes/today.md",
		"/notes/today",
		VaultInfo{Name: "notes", Prefix: "/notes", GuestRole: "view"},
		ThemeInfo{Name: "static_notes"},
		nil, nil)
	if ctx.Title != "Today" {
		t.Errorf("Title from frontmatter = %q", ctx.Title)
	}
	if ctx.Frontmatter["extra"] != "x" {
		t.Errorf("Frontmatter[extra] = %v", ctx.Frontmatter["extra"])
	}
	if ctx.Now.IsZero() {
		t.Error("Now should be populated")
	}
}

func TestNewPageContext_TitleFallbackToFilename(t *testing.T) {
	fm := render.Frontmatter{}
	ctx := NewPageContext(fm, "", "", "notes/Some Note.md", "/notes/Some Note",
		VaultInfo{}, ThemeInfo{}, nil, nil)
	if ctx.Title != "Some Note" {
		t.Errorf("Title fallback = %q, want filename without extension", ctx.Title)
	}
}

func TestNewPageContext_TitleFromExtractedH1(t *testing.T) {
	ctx := NewPageContext(render.Frontmatter{}, "", "Welcome home", "notes/today.md", "/notes/today",
		VaultInfo{}, ThemeInfo{}, nil, nil)
	if ctx.Title != "Welcome home" {
		t.Errorf("Title = %q, want \"Welcome home\" (extracted H1)", ctx.Title)
	}
}

func TestNewPageContext_FrontmatterBeatsExtractedH1(t *testing.T) {
	fm := render.Frontmatter{Title: "FM Wins"}
	ctx := NewPageContext(fm, "", "Body H1", "notes/x.md", "/notes/x",
		VaultInfo{}, ThemeInfo{}, nil, nil)
	if ctx.Title != "FM Wins" {
		t.Errorf("Title = %q, want \"FM Wins\"", ctx.Title)
	}
}

func TestNewPageContext_WhitespaceFrontmatterFallsThrough(t *testing.T) {
	fm := render.Frontmatter{Title: "   "}
	ctx := NewPageContext(fm, "", "Body H1", "notes/x.md", "/notes/x",
		VaultInfo{}, ThemeInfo{}, nil, nil)
	if ctx.Title != "Body H1" {
		t.Errorf("Title = %q, want \"Body H1\" (whitespace frontmatter ignored)", ctx.Title)
	}
}

func TestNewPageContext_AllEmptyFallsToFilename(t *testing.T) {
	ctx := NewPageContext(render.Frontmatter{}, "", "", "docs/getting-started.md", "/docs/getting-started",
		VaultInfo{}, ThemeInfo{}, nil, nil)
	if ctx.Title != "getting-started" {
		t.Errorf("Title = %q, want filename", ctx.Title)
	}
}
