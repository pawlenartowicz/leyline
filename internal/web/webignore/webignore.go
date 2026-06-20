// Package webignore filters vault paths from web exposure.
//
// Two pieces:
//
//   - Matcher  — multi-section, gitignore-syntax exclusion list at
//                <vault>/.leyline/vaultconfig/webignore. Sections:
//                  [view]           — paths hidden from web exposure
//                  [history-ignore] — paths served from the live filesystem
//                                     regardless of the active version
//                                     selector (auth and config files go here)
//                  [edit-ignore]    — paths where the edit-mode switch is
//                                     suppressed even when the role grants
//                                     edit access
//                Pre-section lines (single-list files without section headers)
//                are assigned to [view] for backward compatibility. Absent
//                file = nothing excluded except system-enforced rules
//                (`.leyline/` always in history-ignore and edit-ignore).
//   - Dispatch — extension → render mode, built from operator config
//                (text_extensions) plus engine-built-in mappings (.md →
//                markdown, image set → asset). See dispatch.go.
//
// Both run before any rendering. A request for a path excluded from [view]
// returns 404 before the file is opened.
package webignore

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/format/gitignore"
	"github.com/pawlenartowicz/leyline/protocol/layout"
)

// Section names recognised in the webignore file.
const (
	SectionView          = "view"
	SectionHistoryIgnore = "history-ignore"
	SectionEditIgnore    = "edit-ignore"
)

// systemEnforced lists patterns that the matcher always adds to specific
// sections regardless of what the on-disk webignore file says. `.leyline/`
// must never time-travel (it carries the auth and config plane), and the
// edit-mode switch makes no sense over operator config files.
var systemEnforced = map[string][]string{
	SectionHistoryIgnore: {layout.LeylineDir + "/"},
	SectionEditIgnore:    {layout.LeylineDir + "/"},
}

// Matcher holds parsed exclusion lists per section.
type Matcher struct {
	view    gitignore.Matcher
	history gitignore.Matcher
	edit    gitignore.Matcher

	// patterns mirrors the matchers' inputs so the rules-introspection
	// CLI can print the merged effective set per section. Each entry is
	// the raw line that produced the gitignore.Pattern.
	patterns map[string][]EffectiveRule
}

// EffectiveRule is one line of the rule set for a single section, tagged
// with where it came from so operators can debug why a path is excluded.
type EffectiveRule struct {
	Pattern string
	Source  string // "config" | "system-enforced" | "runtime:<name>"
}

// LoadOptions configures the matcher constructor. Runtime-injected rules
// are appended to the named section after config + system-enforced rules,
// allowing the server to inject additional patterns without modifying the
// on-disk webignore file (e.g. pinning a nav_file to the live filesystem).
type LoadOptions struct {
	HistoryRuntime []RuntimeRule
	EditRuntime    []RuntimeRule
	ViewRuntime    []RuntimeRule
	Logger         *slog.Logger
}

// RuntimeRule is one runtime-injected pattern plus a source tag used by the
// effective-rules CLI output.
type RuntimeRule struct {
	Pattern string
	Source  string // e.g. "runtime:nav_file"
}

// Load reads <vaultRoot>/.leyline/vaultconfig/webignore. Absent file ->
// matcher with system-enforced rules only. Other I/O errors are returned.
func Load(vaultRoot string) (*Matcher, error) {
	return LoadWithOptions(vaultRoot, LoadOptions{})
}

// LoadWithOptions is the constructor that accepts runtime-injected rules.
func LoadWithOptions(vaultRoot string, opts LoadOptions) (*Matcher, error) {
	parsed := map[string][]string{
		SectionView:          nil,
		SectionHistoryIgnore: nil,
		SectionEditIgnore:    nil,
	}
	p := layout.WebignoreFile(vaultRoot)
	f, err := os.Open(p)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		defer f.Close()
		if perr := parseSections(f, parsed, opts.Logger, p); perr != nil {
			return nil, perr
		}
	}
	return buildMatcher(parsed, opts), nil
}

// LoadFromString parses a webignore body from memory. Useful for tests
// and the rules CLI.
func LoadFromString(body string, opts LoadOptions) (*Matcher, error) {
	parsed := map[string][]string{
		SectionView:          nil,
		SectionHistoryIgnore: nil,
		SectionEditIgnore:    nil,
	}
	if err := parseSections(strings.NewReader(body), parsed, opts.Logger, "<memory>"); err != nil {
		return nil, err
	}
	return buildMatcher(parsed, opts), nil
}

// buildMatcher merges config, system-enforced, and runtime rules for each
// section and constructs the gitignore.Matcher per section.
func buildMatcher(parsed map[string][]string, opts LoadOptions) *Matcher {
	rules := map[string][]EffectiveRule{
		SectionView:          collectRules(parsed[SectionView], systemEnforced[SectionView], opts.ViewRuntime),
		SectionHistoryIgnore: collectRules(parsed[SectionHistoryIgnore], systemEnforced[SectionHistoryIgnore], opts.HistoryRuntime),
		SectionEditIgnore:    collectRules(parsed[SectionEditIgnore], systemEnforced[SectionEditIgnore], opts.EditRuntime),
	}
	return &Matcher{
		view:     gitignore.NewMatcher(toPatterns(rules[SectionView])),
		history:  gitignore.NewMatcher(toPatterns(rules[SectionHistoryIgnore])),
		edit:     gitignore.NewMatcher(toPatterns(rules[SectionEditIgnore])),
		patterns: rules,
	}
}

// collectRules assembles config → system-enforced → runtime in that order.
// System-enforced patterns already present in config are deduplicated so they
// don't appear twice in the effective-rules output.
func collectRules(config []string, sys []string, runtime []RuntimeRule) []EffectiveRule {
	out := make([]EffectiveRule, 0, len(config)+len(sys)+len(runtime))
	for _, p := range config {
		out = append(out, EffectiveRule{Pattern: p, Source: "config"})
	}
	for _, p := range sys {
		if containsPattern(out, p) {
			continue
		}
		out = append(out, EffectiveRule{Pattern: p, Source: "system-enforced"})
	}
	for _, r := range runtime {
		out = append(out, EffectiveRule{Pattern: r.Pattern, Source: r.Source})
	}
	return out
}

// containsPattern reports whether the pattern string is already present in rules.
func containsPattern(rules []EffectiveRule, pattern string) bool {
	for _, r := range rules {
		if r.Pattern == pattern {
			return true
		}
	}
	return false
}

// toPatterns converts EffectiveRule entries into gitignore.Pattern values
// consumable by gitignore.NewMatcher.
func toPatterns(rules []EffectiveRule) []gitignore.Pattern {
	out := make([]gitignore.Pattern, 0, len(rules))
	for _, r := range rules {
		out = append(out, gitignore.ParsePattern(r.Pattern, nil))
	}
	return out
}

// ExcludedFromView reports whether relPath is hidden from web exposure.
func (m *Matcher) ExcludedFromView(relPath string) bool {
	return matchSection(m, m.viewMatcher(), relPath)
}

// HistoryIgnored reports whether relPath must serve from the filesystem
// regardless of the active version selector.
func (m *Matcher) HistoryIgnored(relPath string) bool {
	return matchSection(m, m.historyMatcher(), relPath)
}

// EditIgnored reports whether relPath should suppress the edit-mode
// switch when the resolved role grants edit access.
func (m *Matcher) EditIgnored(relPath string) bool {
	return matchSection(m, m.editMatcher(), relPath)
}

// viewMatcher returns the [view] section matcher, safe to call on a nil Matcher.
func (m *Matcher) viewMatcher() gitignore.Matcher {
	if m == nil {
		return nil
	}
	return m.view
}

// historyMatcher returns the [history-ignore] section matcher, safe to call on a nil Matcher.
func (m *Matcher) historyMatcher() gitignore.Matcher {
	if m == nil {
		return nil
	}
	return m.history
}

// editMatcher returns the [edit-ignore] section matcher, safe to call on a nil Matcher.
func (m *Matcher) editMatcher() gitignore.Matcher {
	if m == nil {
		return nil
	}
	return m.edit
}

// matchSection splits relPath on "/" and runs it through gm.Match with
// isDir=false. A nil Matcher or nil matcher always returns false (nothing excluded).
func matchSection(m *Matcher, gm gitignore.Matcher, relPath string) bool {
	if m == nil || gm == nil {
		return false
	}
	parts := strings.Split(relPath, "/")
	return gm.Match(parts, false)
}

// EffectiveRules returns the merged rule list for every section, ordered
// (config → system-enforced → runtime) as the matcher consumes them.
// Used by the `leyline-web rules --effective` CLI subcommand.
func (m *Matcher) EffectiveRules() map[string][]EffectiveRule {
	if m == nil {
		return nil
	}
	out := make(map[string][]EffectiveRule, len(m.patterns))
	for k, v := range m.patterns {
		copy := make([]EffectiveRule, len(v))
		for i := range v {
			copy[i] = v[i]
		}
		out[k] = copy
	}
	return out
}

// parseSections fills `out` with the patterns for each known section.
// Pre-section lines (before any header) accumulate under [view] so that
// older single-list webignore files keep working without modification.
// Unknown section names emit a startup warning and their lines are dropped.
func parseSections(r io.Reader, out map[string][]string, logger *slog.Logger, srcPath string) error {
	current := SectionView
	unknown := ""
	s := bufio.NewScanner(r)
	for s.Scan() {
		raw := strings.TrimRight(s.Text(), "\r")
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if name, ok := parseSectionHeader(trimmed); ok {
			switch name {
			case SectionView, SectionHistoryIgnore, SectionEditIgnore:
				current = name
				unknown = ""
			default:
				if logger != nil {
					logger.Warn("webignore: unknown section ignored",
						"path", srcPath, "section", name)
				}
				current = ""
				unknown = name
			}
			continue
		}
		if current == "" {
			// Lines under an unknown section are silently dropped after
			// the section-header warning above.
			_ = unknown
			continue
		}
		out[current] = append(out[current], raw)
	}
	if err := s.Err(); err != nil {
		return fmt.Errorf("webignore: scan %s: %w", srcPath, err)
	}
	return nil
}

func parseSectionHeader(line string) (string, bool) {
	if len(line) < 3 || line[0] != '[' || line[len(line)-1] != ']' {
		return "", false
	}
	return strings.TrimSpace(line[1 : len(line)-1]), true
}
