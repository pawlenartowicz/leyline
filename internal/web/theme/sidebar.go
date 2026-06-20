package theme

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Sidebar mode keywords. A side is EITHER a stack of block widgets
// (Mode == SidebarWidgets) OR one of the scalar sentinels below.
const (
	SidebarWidgets    = "widgets"    // Widgets holds the ordered block-widget stack
	SidebarBody       = "body"       // article column absorbs this side's width
	SidebarNone       = "none"       // reserved blank gutter
	SidebarReferences = "references" // page footnotes rendered as gutter sidenotes (sole-occupant)
)

// builtinSidebarWidgets are the engine-generated block widgets that may appear
// as list members. Their content comes from the vault/page, not a file, so
// they carry no extension — that is what distinguishes them from custom file
// widgets (which always carry one, e.g. related.md / sitemap.nav). The
// scalar-only sentinels (body/none/references) are NOT listed here: they are
// not stackable.
var builtinSidebarWidgets = map[string]bool{
	"navigation":       true,
	"table_of_content": true,
	"search_field":     true,
}

// SidebarSpec is one side's (left or right) configuration. It is either a
// stack of block widgets or a single scalar sentinel — never both. The zero
// value (Mode == "") means "unset"; overlay() treats it as inherit and
// Collapse() applies the bottom default.
//
// YAML accepts two shapes:
//
//	right_sidebar: none                       # scalar sentinel
//	right_sidebar: [table_of_content, foo.md] # widget stack
type SidebarSpec struct {
	Mode    string   // "" unset | widgets | body | none | references
	Widgets []string // ordered widget names; non-empty only when Mode == SidebarWidgets
}

// IsZero reports whether the spec is unset (so overlay/Collapse can decide
// inherit-vs-default). Mirrors the reflective check overlay() uses.
func (s SidebarSpec) IsZero() bool { return s.Mode == "" && len(s.Widgets) == 0 }

// UnmarshalYAML accepts a scalar sentinel (body|none|references) or a sequence
// of widget names. Anything else — an unknown scalar, a sentinel used inside a
// list, an unknown bare (extensionless) widget name — is a decode error, so
// LoadManifest/LoadVaultYAML reject malformed config at parse time.
func (s *SidebarSpec) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var str string
		if err := value.Decode(&str); err != nil {
			return err
		}
		str = strings.TrimSpace(str)
		switch str {
		case "", "~", "null":
			*s = SidebarSpec{}
		case SidebarBody, SidebarNone, SidebarReferences:
			*s = SidebarSpec{Mode: str}
		default:
			return fmt.Errorf("invalid sidebar value %q (a scalar must be one of: body, none, references; put widgets in a list, e.g. [%s])", str, str)
		}
		return nil
	case yaml.SequenceNode:
		var items []string
		if err := value.Decode(&items); err != nil {
			return err
		}
		widgets := make([]string, 0, len(items))
		for _, it := range items {
			it = strings.TrimSpace(it)
			if it == "" {
				return fmt.Errorf("sidebar widget name must not be empty")
			}
			switch it {
			case SidebarBody, SidebarNone, SidebarReferences:
				return fmt.Errorf("%q is a sidebar mode, not a stackable widget — set it as the side's scalar value (right_sidebar: %s), not a list item", it, it)
			}
			if !strings.Contains(it, ".") && !builtinSidebarWidgets[it] {
				return fmt.Errorf("unknown builtin sidebar widget %q (builtins: navigation, table_of_content, search_field; a custom widget must be a file with an extension, e.g. %s.md)", it, it)
			}
			widgets = append(widgets, it)
		}
		*s = SidebarSpec{Mode: SidebarWidgets, Widgets: widgets}
		return nil
	default:
		return fmt.Errorf("sidebar must be a scalar (body|none|references) or a list of widgets")
	}
}

// resolveSidebar applies the vault override and the bottom default to a
// chain-merged theme spec. Used by Collapse for both sides. Bottom default is
// SidebarNone (no rail) so a theme that declares nothing renders no sidebar —
// matching the engine's pre-widget behaviour, where the rail was opt-in.
func resolveSidebar(themeSpec, vaultSpec SidebarSpec) SidebarSpec {
	out := themeSpec
	if !vaultSpec.IsZero() {
		out = vaultSpec
	}
	if out.IsZero() {
		out = SidebarSpec{Mode: SidebarNone}
	}
	return out
}
