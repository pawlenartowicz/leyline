package server

import (
	"html/template"

	"github.com/pawlenartowicz/leyline/internal/web/gateway"
	"github.com/pawlenartowicz/leyline/protocol/access"
)

// This file is the engine side of the _panel: the *data* the panel template
// renders from, and the one deliberate presentation-in-engine exception (the
// show-once secret page, §D of the panel-themeable-templates design). All other
// panel presentation — markup, CSS, section order, copy, icons — lives in the
// theme's panel.html / panel.css, loaded through the theme chain. The engine
// passes mechanics: the cap-allowed section set, which is active, and each
// section's typed data.

// panelView is the single struct the chain-loaded panel.html renders from. The
// template owns layout, order, grouping, copy and icons; the engine owns the
// data. Presence of a key in Allowed means the session holds that section's
// capability ("permission = widget") — the template shows a section/nav item
// only when its key is allowed. Active is the section key shown first.
type panelView struct {
	Vault    string
	Host     string
	Selected string        // active vaultID (the rail plate text + ?vault= state)
	Switcher []vaultOption // vaults the SWA can switch to; empty → render no switcher
	Banner   string        // ?msg= round-trip banner (redirect results)

	// BasePath + the two chains let panel.html link the active theme in its own
	// <head>: one theme.css <link> per CSSChain layer, one panel.css <link> per
	// PanelCSSChain layer, each at "{{.BasePath}}_theme/<layer>/<file>".
	BasePath      string
	CSSChain      []string
	PanelCSSChain []string

	Action      string          // POST target for every section form (this vault's _panel)
	Active      string          // section key shown first; the rest start hidden
	Allowed     map[string]bool // section key → cap-allowed (presence gates render)
	RoleOptions []string        // role names offered in the keys-section <select>

	// Per-section typed data; only allowed sections are populated.
	WebYAML   panelConfig
	WebIgnore panelConfig
	Roles     panelConfig
	Keys      panelKeys
	Vaults    panelVaults
}

// panelConfig is one vaultconfig file's editor data: the on-disk text, an
// optional effective-config dump (web.yaml only), and a read error if any.
type panelConfig struct {
	Content   string
	Effective string
	Err       string
}

// panelKeys / panelVaults carry a relayed register's rows or the relay error
// (e.g. a non-server-wide admin hitting the vaults register).
type panelKeys struct {
	Rows []access.KeyInfo
	Err  string
}

type panelVaults struct {
	Rows []gateway.VaultInfo
	Err  string
}

// vaultOption is one entry in the rail's vault switcher. Selected marks the
// vault the panel is currently scoped to. The list is the operator vault list
// (server-gated on server-wide admin), reused from the Vaults section's fetch.
type vaultOption struct {
	ID       string
	Selected bool
}

// secretOnceTmpl is the one-time-secret interstitial shown after minting a key
// or creating a vault — the lone deliberate presentation-in-engine exception
// (§D). It is a separate HTTP response served Cache-Control: no-store: the
// cleartext secret renders in the page body (html/template-escaped), never in
// a redirect URL, which would leak it to the access log, browser history, and
// Referer header (see handleKeysPost / handleVaultsPost create). It cannot ride
// inside panel.html (a different response with different cache semantics), so it
// keeps its own minimal self-contained styling rather than the themed chain.
var secretOnceTmpl = template.Must(template.New("secret").Parse(`<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Secret — copy it now</title>
<style>
  body { margin: 0; font: 400 15px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; color: #1B1622; background: #F1ECDF; }
  main { max-width: 40rem; margin: 2rem auto; padding: 0 1rem; }
  .banner { padding: 0.7rem 0.9rem; border: 1px solid #5A7048; background: #D6DCC8; border-radius: 4px; overflow-wrap: anywhere; }
  a.btn { display: inline-block; margin-top: 1rem; padding: 0.4rem 0.8rem; border: 1px solid #D2CABA; border-radius: 3px; color: #7A4636; text-decoration: none; }
  a.btn:hover { border-color: #7A4636; }
</style>
</head>
<body>
<main>
  <div class="banner">{{.Message}}</div>
  <p><a class="btn" href="{{.Back}}">Back to panel</a></p>
</main>
</body></html>`))

type secretOnceView struct {
	Message string
	Back    string
}
