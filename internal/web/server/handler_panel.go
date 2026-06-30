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

	// One operator-vault-list fetch, reused for the switcher, the Vaults
	// section, and cross-vault path resolution. Server-gated on server-wide
	// admin; a non-SWA caller gets an error (→ no switcher, mounted vault only).
	var vaultRows []gateway.VaultInfo
	var vaultErr error
	if allowed["vaults"] {
		vaultRows, vaultErr = deps.Gateway.VaultsList(key)
	}

	selVaultID, selRoot := resolveSelected(deps, r, vaultRows)

	plate := selVaultID
	if plate == "" {
		plate = deps.Vault.Name() // vault declares no vault_id → fall back to mount name
	}

	view := panelView{
		Vault:         plate,
		Selected:      selVaultID,
		Host:          deps.Gateway.Host(),
		Banner:        r.URL.Query().Get("msg"),
		BasePath:      deps.vaultInfo().BasePath(),
		CSSChain:      deps.CSSChain,
		PanelCSSChain: deps.PanelCSSChain,
		Action:        actionFor(deps, selVaultID),
		Active:        secs[0].Key,
		Allowed:       allowed,
		RoleOptions:   builtinRoles,
		Switcher:      switcherFor(vaultRows, selVaultID),
	}

	// Effective-config dump is the MOUNTED vault's resolved config; only show it
	// when not switched away (deps.Defaults describes deps.Vault, not selRoot).
	mounted := selVaultID == deps.VaultID
	if allowed["webyaml"] {
		view.WebYAML = buildConfigData(deps, selRoot, "webyaml", mounted)
	}
	if allowed["webignore"] {
		view.WebIgnore = buildConfigData(deps, selRoot, "webignore", mounted)
	}
	if allowed["roles"] {
		view.Roles = buildConfigData(deps, selRoot, "roles", mounted)
	}
	if allowed["keys"] {
		view.Keys = buildKeysData(deps, selVaultID, key)
	}
	if allowed["vaults"] {
		view.Vaults = panelVaults{Rows: vaultRows, Err: errString(vaultErr)}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = deps.Templates.Panel.ExecuteTemplate(w, "panel.html", view)
}

// switcherFor turns the operator vault list into switcher options, marking the
// active vault. nil rows (non-SWA / unavailable) → nil → the template renders no
// switcher.
func switcherFor(rows []gateway.VaultInfo, selVaultID string) []vaultOption {
	if len(rows) == 0 {
		return nil
	}
	out := make([]vaultOption, 0, len(rows))
	for _, v := range rows {
		out = append(out, vaultOption{ID: v.ID, Selected: v.ID == selVaultID})
	}
	return out
}

// errString is "" for a nil error, else err.Error().
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// buildConfigData reads one vaultconfig file's editor data from root's
// .leyline/vaultconfig/. includeEffective adds the fully-resolved effective
// config (web.yaml only) — pass false when scoped to a vault other than the
// mounted one, since deps.Defaults describes only the mounted vault. A read
// error surfaces as Err; a missing file is a true create (empty Content).
func buildConfigData(deps *PageDeps, root, sectionKey string, includeEffective bool) panelConfig {
	rel, _ := configRelPath(sectionKey)
	content, _, err := readConfigFile(root, rel)
	pc := panelConfig{Content: string(content)}
	if err != nil {
		pc.Err = "read error: " + err.Error()
	}
	if sectionKey == "webignore" {
		pc.Content = scaffoldWebignore(pc.Content)
	}
	if sectionKey == "webyaml" && includeEffective {
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

// resolveSelected resolves the panel's target vault from the optional ?vault=
// query param against the operator vault list. Absent, equal to the mounted
// vault, unknown, or rows==nil (non-SWA / list unavailable) → the mounted vault
// (deps.VaultID, deps.Vault.Root) — the unchanged single-vault path. A known
// other vault → its id + on-disk Path (the co-located-disk assumption, §B): web
// reads that vault's vaultconfig directly off the shared filesystem.
func resolveSelected(deps *PageDeps, r *http.Request, rows []gateway.VaultInfo) (vaultID, root string) {
	want := r.URL.Query().Get("vault")
	if want == "" || want == deps.VaultID {
		return deps.VaultID, deps.Vault.Root
	}
	for _, v := range rows {
		if v.ID == want {
			return v.ID, v.Path
		}
	}
	return deps.VaultID, deps.Vault.Root
}

// actionFor is the panel POST target for vaultID: the mount path, plus
// ?vault=<id> when targeting a vault other than the mounted one, so the POST and
// the redirect that follows stay scoped to the selected vault.
func actionFor(deps *PageDeps, vaultID string) string {
	base := panelActionPath(deps)
	if vaultID != "" && vaultID != deps.VaultID {
		return base + "?vault=" + url.QueryEscape(vaultID)
	}
	return base
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
	if !sectionAllowed(cs, section) {
		http.NotFound(w, r)
		return
	}

	// Cross-vault target rides in ?vault= (the form Action carried it). VaultsList
	// is server-gated on SWA; a non-SWA caller gets nil rows → mounted vault, and
	// any forged cross-vault op is server-rejected regardless. Vaults ops are
	// server-wide and take their target id from the form, so they ignore selVaultID.
	var rows []gateway.VaultInfo
	if r.URL.Query().Get("vault") != "" {
		rows, _ = deps.Gateway.VaultsList(key)
	}
	selVaultID, selRoot := resolveSelected(deps, r, rows)

	if rel, isCfg := configRelPath(section); isCfg {
		newContent := []byte(r.FormValue("content"))
		if vErr := validateConfig(section, newContent); vErr != nil {
			redirectPanel(deps, w, r, "invalid "+section+": "+vErr.Error())
			return
		}
		_, preHash, rErr := readConfigFile(selRoot, rel)
		if rErr != nil {
			redirectPanel(deps, w, r, "read error: "+rErr.Error())
			return
		}
		ack, pErr := deps.Gateway.PushFile(r.Context(), selVaultID, key, rel, newContent, preHash)
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
		handleKeysPost(deps, w, r, selVaultID, key)
	case "vaults":
		handleVaultsPost(deps, w, r, key)
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
// log, and the Referer header. no-store stops it being cached to disk. The
// "Back to panel" link preserves the selected vault (?vault=). See
// handleKeysPost / handleVaultsPost create.
func renderSecretOnce(deps *PageDeps, w http.ResponseWriter, r *http.Request, message string) {
	back := panelActionPath(deps)
	if v := r.URL.Query().Get("vault"); v != "" {
		back += "?vault=" + url.QueryEscape(v)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = secretOnceTmpl.Execute(w, secretOnceView{Message: message, Back: back})
}

func redirectPanel(deps *PageDeps, w http.ResponseWriter, r *http.Request, msg string) {
	q := url.Values{}
	if v := r.URL.Query().Get("vault"); v != "" {
		q.Set("vault", v)
	}
	q.Set("msg", msg)
	http.Redirect(w, r, panelActionPath(deps)+"?"+q.Encode(), http.StatusSeeOther)
}

// builtinRoles are the role names offered in the keys-section <select>. v1
// lists the three built-ins; the server validates the relayed role regardless,
// so a custom role assigned out-of-band still sticks. Structured custom-role
// selection is the frontend-design phase.
var builtinRoles = []string{"admin", "editor", "reader"}

func buildKeysData(deps *PageDeps, vaultID, key string) panelKeys {
	rows, err := deps.Gateway.KeysList(vaultID, key)
	if err != nil {
		return panelKeys{Err: err.Error()}
	}
	return panelKeys{Rows: rows}
}

// handleKeysPost relays a key mutation to the server as the user, targeting
// vaultID (the selected vault, which may differ from the mounted one). The
// cleartext of a freshly created key is surfaced once — the server never
// returns it again.
func handleKeysPost(deps *PageDeps, w http.ResponseWriter, r *http.Request, vaultID, key string) {
	switch r.FormValue("op") {
	case "create":
		created, err := deps.Gateway.KeysCreate(vaultID, key, r.FormValue("name"), r.FormValue("role"))
		if err != nil {
			redirectPanel(deps, w, r, "create failed: "+err.Error())
			return
		}
		renderSecretOnce(deps, w, r, "New key for "+created.Name+": "+created.Key+" — copy it now, it is shown only once.")
	case "revoke":
		name := r.FormValue("name")
		if err := deps.Gateway.KeysDelete(vaultID, key, name); err != nil {
			redirectPanel(deps, w, r, "revoke failed: "+err.Error())
			return
		}
		redirectPanel(deps, w, r, "revoked "+name)
	case "role":
		name := r.FormValue("name")
		if err := deps.Gateway.KeysUpdateRole(vaultID, key, name, r.FormValue("role")); err != nil {
			redirectPanel(deps, w, r, "role change failed: "+err.Error())
			return
		}
		redirectPanel(deps, w, r, "role updated for "+name)
	default:
		http.Error(w, "unknown op", http.StatusBadRequest)
	}
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
		renderSecretOnce(deps, w, r, "Vault "+created.ID+" created. Admin key: "+created.AdminKey+" — copy it now, it is shown only once.")
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
