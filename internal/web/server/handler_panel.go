package server

import (
	"bytes"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/pawlenartowicz/leyline/internal/web/auth"
	"github.com/pawlenartowicz/leyline/internal/web/gateway"
	"github.com/pawlenartowicz/leyline/internal/web/theme"
	"github.com/pawlenartowicz/leyline/protocol"
	"github.com/pawlenartowicz/leyline/protocol/caps"
	protroles "github.com/pawlenartowicz/leyline/protocol/roles"
)

type panelSection struct {
	Key   string
	Title string
	Cap   caps.Capability
}

// panelSections is the full v1 section table, in display order. sectionsFor
// filters it by the session's caps — presence of the cap is the only gate
// ("permission = widget"); never a role-string compare.
var panelSections = []panelSection{
	{Key: "webyaml", Title: "web.yaml", Cap: caps.VaultAdmin},
	{Key: "webignore", Title: "webignore", Cap: caps.VaultAdmin},
	{Key: "roles", Title: "Roles", Cap: caps.VaultAdmin},
	{Key: "keys", Title: "Keys / access", Cap: caps.KeysManage},
	// Vaults is gated locally on VaultAdmin; the server enforces true
	// server-wide-admin on the relayed /_leyline/operator/* call.
	{Key: "vaults", Title: "Vaults", Cap: caps.VaultAdmin},
}

func sectionsFor(cs caps.Set) []panelSection {
	var out []panelSection
	for _, s := range panelSections {
		if cs.Has(s.Cap) {
			out = append(out, s)
		}
	}
	return out
}

// panelAuth resolves the request to (rawKey, caps) for this vault, or false
// when the session does not authenticate for this vault. rawKey is the
// cleartext ley_ token from the cookie binding — the credential the gateway
// relays to the server as the user.
func panelAuth(deps *PageDeps, r *http.Request) (rawKey string, cs caps.Set, ok bool) {
	bindings, found := auth.ReadCookie(r)
	if !found {
		return "", caps.Set{}, false
	}
	key, has := bindings[deps.Vault.Prefix]
	if !has {
		return "", caps.Set{}, false
	}
	sess, sok := deps.Stores.ProbeBindings(map[string]string{deps.Vault.Prefix: key})
	if !sok || !sess.HasVault(deps.Vault.Prefix) {
		return "", caps.Set{}, false
	}
	return key, sess.CapsFor(deps.Vault.Prefix), true
}

func PanelHandler(deps *PageDeps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Unpaired web → the panel does not exist. 404 (no existence leak).
		if !deps.Gateway.Paired() {
			http.NotFound(w, r)
			return
		}
		key, cs, ok := panelAuth(deps, r)
		if !ok {
			// Unauthenticated or no access for this vault → 404, matching the
			// .leyline existence-leak policy (RespondUnauthorized w/ nil sess).
			http.NotFound(w, r)
			return
		}
		secs := sectionsFor(cs)
		if len(secs) == 0 {
			// Authenticated but holds no management cap → nothing to manage.
			http.NotFound(w, r)
			return
		}
		if r.Method == http.MethodPost {
			handlePanelPost(deps, w, r, cs) // Phase 2
			return
		}
		renderPanel(deps, w, r, secs, key)
	})
}

// renderPanel builds the aggregated panelView and executes the chain-loaded
// panel.html. The engine passes only mechanics — the cap-allowed section set
// (presence = caps), which is active, and each section's typed data; the
// template owns order, grouping, copy, icons and layout. secs is the
// cap-filtered, display-ordered section list, so secs[0] is the active one.
func renderPanel(deps *PageDeps, w http.ResponseWriter, r *http.Request, secs []panelSection, key string) {
	allowed := make(map[string]bool, len(secs))
	for _, s := range secs {
		allowed[s.Key] = true
	}
	view := panelView{
		Vault:         deps.Vault.Name(),
		Host:          deps.Gateway.Host(),
		MaskedKey:     maskKey(key),
		Banner:        r.URL.Query().Get("msg"),
		BasePath:      deps.vaultInfo().BasePath(),
		CSSChain:      deps.CSSChain,
		PanelCSSChain: deps.PanelCSSChain,
		Action:        panelActionPath(deps),
		Active:        secs[0].Key,
		Allowed:       allowed,
		RoleOptions:   builtinRoles,
	}
	if allowed["webyaml"] {
		view.WebYAML = buildConfigData(deps, "webyaml")
	}
	if allowed["webignore"] {
		view.WebIgnore = buildConfigData(deps, "webignore")
	}
	if allowed["roles"] {
		view.Roles = buildConfigData(deps, "roles")
	}
	if allowed["keys"] {
		view.Keys = buildKeysData(deps, key)
	}
	if allowed["vaults"] {
		view.Vaults = buildVaultsData(deps, key)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = deps.Templates.Panel.ExecuteTemplate(w, "panel.html", view)
}

// maskKey renders a cleartext ley_ token as a short, non-recoverable label for
// the rail footer (first 8 + ellipsis + last 4). Display only — never used for
// auth; short/unexpected values pass through unchanged.
func maskKey(k string) string {
	if len(k) <= 14 {
		return k
	}
	return k[:8] + "…" + k[len(k)-4:]
}

// buildConfigData reads one vaultconfig file's editor data. For web.yaml it
// also dumps the fully-resolved effective config (defaults + theme + overrides)
// as a read-only reference. A read error surfaces as Err; a missing file is a
// true create (empty Content).
func buildConfigData(deps *PageDeps, sectionKey string) panelConfig {
	rel, _ := configRelPath(sectionKey)
	content, _, err := readConfigFile(deps.Vault.Root, rel)
	pc := panelConfig{Content: string(content)}
	if err != nil {
		pc.Err = "read error: " + err.Error()
	}
	if sectionKey == "webyaml" {
		if out, mErr := yaml.Marshal(deps.Defaults); mErr == nil {
			pc.Effective = string(out)
		}
	}
	return pc
}

// panelActionPath builds the POST target for this vault's panel. The panel is
// mounted at <vault-prefix>/_panel; for the root vault the prefix is "/".
func panelActionPath(deps *PageDeps) string {
	if deps.Vault.Prefix == "/" {
		return "/_panel"
	}
	return deps.Vault.Prefix + "/_panel"
}

func handlePanelPost(deps *PageDeps, w http.ResponseWriter, r *http.Request, cs caps.Set) {
	key, _, ok := panelAuth(deps, r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	section := r.FormValue("section")

	// Re-check the cap for this section before relaying (defense in depth; the
	// server re-enforces vault.admin on vaultconfig writes regardless).
	if !sectionAllowed(cs, section) {
		http.NotFound(w, r)
		return
	}

	if rel, isCfg := configRelPath(section); isCfg {
		newContent := []byte(r.FormValue("content"))
		if vErr := validateConfig(section, newContent); vErr != nil {
			redirectPanel(deps, w, r, "invalid "+section+": "+vErr.Error())
			return
		}
		_, preHash, rErr := readConfigFile(deps.Vault.Root, rel)
		if rErr != nil {
			redirectPanel(deps, w, r, "read error: "+rErr.Error())
			return
		}
		ack, pErr := deps.Gateway.PushFile(r.Context(), deps.VaultID, key, rel, newContent, preHash)
		if pErr != nil {
			redirectPanel(deps, w, r, "save failed: "+pErr.Error())
			return
		}
		switch ack.Result {
		case protocol.PushAckOK:
			redirectPanel(deps, w, r, section+" saved")
		case protocol.PushAckStaleBase:
			redirectPanel(deps, w, r, section+" changed on the server — reload and retry")
		default:
			redirectPanel(deps, w, r, section+" rejected ("+ack.Result+")")
		}
		return
	}

	switch section {
	case "keys":
		handleKeysPost(deps, w, r, key) // Task 11
	case "vaults":
		handleVaultsPost(deps, w, r, key) // Task 12
	default:
		http.Error(w, "unknown section", http.StatusBadRequest)
	}
}

// sectionAllowed mirrors the panelSections gate for a single key.
func sectionAllowed(cs caps.Set, key string) bool {
	for _, s := range panelSections {
		if s.Key == key {
			return cs.Has(s.Cap)
		}
	}
	return false
}

// validateConfig rejects malformed config before relaying a push, so the user
// gets a clear message instead of a server-side parse failure later. web.yaml
// must parse as VaultYAML; roles must parse; webignore is lenient (no check).
func validateConfig(section string, content []byte) error {
	switch section {
	case "webyaml":
		var y theme.VaultYAML
		return yaml.Unmarshal(content, &y)
	case "roles":
		_, err := protroles.Parse(bytes.NewReader(content))
		return err
	default:
		return nil
	}
}

// renderSecretOnce shows a one-time secret (new key / vault admin key) in the
// response body instead of a redirect, keeping it out of the URL, the access
// log, and the Referer header. no-store stops it being cached to disk. See
// handleKeysPost / handleVaultsPost create.
func renderSecretOnce(deps *PageDeps, w http.ResponseWriter, message string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = secretOnceTmpl.Execute(w, secretOnceView{Message: message, Back: panelActionPath(deps)})
}

func redirectPanel(deps *PageDeps, w http.ResponseWriter, r *http.Request, msg string) {
	http.Redirect(w, r, panelActionPath(deps)+"?msg="+url.QueryEscape(msg), http.StatusSeeOther)
}

// builtinRoles are the role names offered in the keys-section <select>. v1
// lists the three built-ins; the server validates the relayed role regardless,
// so a custom role assigned out-of-band still sticks. Structured custom-role
// selection is the frontend-design phase.
var builtinRoles = []string{"admin", "editor", "reader"}

func buildKeysData(deps *PageDeps, key string) panelKeys {
	rows, err := deps.Gateway.KeysList(deps.VaultID, key)
	if err != nil {
		return panelKeys{Err: err.Error()}
	}
	return panelKeys{Rows: rows}
}

// handleKeysPost relays a key mutation to the server as the user. The cleartext
// of a freshly created key is surfaced once via the redirect banner — the
// server never returns it again.
func handleKeysPost(deps *PageDeps, w http.ResponseWriter, r *http.Request, key string) {
	switch r.FormValue("op") {
	case "create":
		created, err := deps.Gateway.KeysCreate(deps.VaultID, key, r.FormValue("name"), r.FormValue("role"))
		if err != nil {
			redirectPanel(deps, w, r, "create failed: "+err.Error())
			return
		}
		renderSecretOnce(deps, w, "New key for "+created.Name+": "+created.Key+" — copy it now, it is shown only once.")
	case "revoke":
		name := r.FormValue("name")
		if err := deps.Gateway.KeysDelete(deps.VaultID, key, name); err != nil {
			redirectPanel(deps, w, r, "revoke failed: "+err.Error())
			return
		}
		redirectPanel(deps, w, r, "revoked "+name)
	case "role":
		name := r.FormValue("name")
		if err := deps.Gateway.KeysUpdateRole(deps.VaultID, key, name, r.FormValue("role")); err != nil {
			redirectPanel(deps, w, r, "role change failed: "+err.Error())
			return
		}
		redirectPanel(deps, w, r, "role updated for "+name)
	default:
		http.Error(w, "unknown op", http.StatusBadRequest)
	}
}

func buildVaultsData(deps *PageDeps, key string) panelVaults {
	rows, err := deps.Gateway.VaultsList(key)
	if err != nil {
		return panelVaults{Err: err.Error()}
	}
	return panelVaults{Rows: rows}
}

// handleVaultsPost relays a cross-vault operation. The server enforces
// server-wide admin; a non-server-wide admin's call comes back as an error
// surfaced in the banner. A created vault's admin key is shown once.
func handleVaultsPost(deps *PageDeps, w http.ResponseWriter, r *http.Request, key string) {
	switch r.FormValue("op") {
	case "create":
		in := gateway.VaultCreateReq{
			ID:               r.FormValue("id"),
			Path:             r.FormValue("path"),
			ServerWideAdmins: r.FormValue("server_wide_admins") != "",
			AdminEmail:       r.FormValue("admin_email"),
		}
		created, err := deps.Gateway.VaultCreate(key, in)
		if err != nil {
			redirectPanel(deps, w, r, "vault create failed: "+err.Error())
			return
		}
		renderSecretOnce(deps, w, "Vault "+created.ID+" created. Admin key: "+created.AdminKey+" — copy it now, it is shown only once.")
	case "destroy":
		id := r.FormValue("id")
		if err := deps.Gateway.VaultDestroy(id, key); err != nil {
			redirectPanel(deps, w, r, "vault destroy failed: "+err.Error())
			return
		}
		redirectPanel(deps, w, r, "vault "+id+" destroyed")
	default:
		http.Error(w, "unknown op", http.StatusBadRequest)
	}
}

// configRelPath maps a config-section key to its vault-relative, forward-slash
// path under .leyline/vaultconfig/. ok=false for sections that are not a
// single editable file (keys, vaults). Paths are literal (not layout.* which
// returns OS-absolute paths) because the wire op.Path is vault-relative.
func configRelPath(sectionKey string) (string, bool) {
	switch sectionKey {
	case "webyaml":
		return ".leyline/vaultconfig/web.yaml", true
	case "webignore":
		return ".leyline/vaultconfig/webignore", true
	case "roles":
		return ".leyline/vaultconfig/roles", true
	default:
		return "", false
	}
}

// readConfigFile reads the current on-disk bytes and their hash for the
// optimistic-concurrency preHash. A missing file is a true create: (nil, nil,
// nil). web shares the disk with the server and the file is at HEAD, so the
// on-disk hash is the correct preHash.
func readConfigFile(root, relPath string) ([]byte, *protocol.Hash, error) {
	abs := filepath.Join(root, filepath.FromSlash(relPath))
	b, err := os.ReadFile(abs)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	h := protocol.HashBytes(b)
	return b, &h, nil
}
