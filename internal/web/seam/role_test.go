package seam

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeSessions and fakeSession are test doubles for the Sessions / Session
// interfaces. They allow precise control over HasVault and HasCap responses
// without importing auth.

type fakeSession struct {
	hasVault map[string]bool
	hasCap   map[string]bool // key: "prefix:capName"
}

func (s *fakeSession) HasVault(prefix string) bool { return s.hasVault[prefix] }
func (s *fakeSession) HasCap(prefix, capName string) bool {
	return s.hasCap[prefix+":"+capName]
}

type fakeSessions struct {
	session Session // nil means no session found
}

func (fs *fakeSessions) FromRequest(_ *http.Request) Session { return fs.session }

// helpers

func newReq() *http.Request { return httptest.NewRequest("GET", "/", nil) }

// readerSession returns a fakeSession that claims HasVault(prefix) and
// HasCap(prefix, "sync.pull") for the given prefix.
func readerSession(prefix string) *fakeSession {
	return &fakeSession{
		hasVault: map[string]bool{prefix: true},
		hasCap:   map[string]bool{prefix + ":sync.pull": true},
	}
}

// ── Existing GuestRole tests (sessions == nil) ──────────────────────────────

func TestResolve_GuestViewIsRoleView(t *testing.T) {
	if got := Resolve(VaultMeta{GuestRole: "view"}, newReq(), nil); got != RoleView {
		t.Errorf("Resolve(view, nil) = %v, want RoleView", got)
	}
}

func TestResolve_GuestNoneIsRoleNone(t *testing.T) {
	if got := Resolve(VaultMeta{GuestRole: "none"}, newReq(), nil); got != RoleNone {
		t.Errorf("Resolve(none, nil) = %v, want RoleNone", got)
	}
}

func TestResolve_GuestEditIsRoleEdit(t *testing.T) {
	if got := Resolve(VaultMeta{GuestRole: "edit"}, newReq(), nil); got != RoleEdit {
		t.Errorf("Resolve(edit, nil) = %v, want RoleEdit", got)
	}
	if !RoleEdit.GrantsEdit() {
		t.Error("RoleEdit.GrantsEdit() should be true")
	}
	if RoleView.GrantsEdit() {
		t.Error("RoleView.GrantsEdit() should be false")
	}
}

func TestResolve_GuestProposeFallsBackToView_Phase2c(t *testing.T) {
	if got := Resolve(VaultMeta{GuestRole: "propose"}, newReq(), nil); got != RoleView {
		t.Errorf("Resolve(propose, nil) = %v, want RoleView (Phase 2c fallback)", got)
	}
}

func TestResolve_UnknownDefaultsToView(t *testing.T) {
	if got := Resolve(VaultMeta{GuestRole: ""}, newReq(), nil); got != RoleView {
		t.Errorf("Resolve(empty, nil) = %v, want RoleView (default)", got)
	}
}

// ── New session tests ────────────────────────────────────────────────────────

// 1. sessions present but FromRequest returns nil → falls through to GuestRole.
func TestResolve_SessionsAbsent_FallsThrough(t *testing.T) {
	ss := &fakeSessions{session: nil}
	cases := []struct {
		guestRole string
		want      Role
	}{
		{"none", RoleNone},
		{"view", RoleView},
		{"edit", RoleEdit},
	}
	for _, c := range cases {
		got := Resolve(VaultMeta{GuestRole: c.guestRole, Prefix: "v"}, newReq(), ss)
		if got != c.want {
			t.Errorf("nil session, GuestRole=%q: got %v, want %v", c.guestRole, got, c.want)
		}
	}
}

// 2. Authenticated reader with sync.pull → RoleView regardless of GuestRole.
func TestResolve_AuthenticatedReader_AlwaysRoleView(t *testing.T) {
	const prefix = "myvault"
	ss := &fakeSessions{session: readerSession(prefix)}
	for _, guestRole := range []string{"none", "view", "edit"} {
		got := Resolve(VaultMeta{GuestRole: guestRole, Prefix: prefix}, newReq(), ss)
		if got != RoleView {
			t.Errorf("authenticated sync.pull, GuestRole=%q: got %v, want RoleView", guestRole, got)
		}
	}
}

// 3. Authenticated but vault not in session → falls through to GuestRole.
func TestResolve_AuthenticatedWrongVault_FallsThrough(t *testing.T) {
	// Session claims "other-vault", not "myvault".
	ss := &fakeSessions{session: readerSession("other-vault")}
	got := Resolve(VaultMeta{GuestRole: "none", Prefix: "myvault"}, newReq(), ss)
	if got != RoleNone {
		t.Errorf("wrong vault in session, GuestRole=none: got %v, want RoleNone", got)
	}
}

// 4. Authenticated but lacking sync.pull → falls through to GuestRole.
func TestResolve_AuthenticatedNoSyncPull_FallsThrough(t *testing.T) {
	const prefix = "myvault"
	// HasVault returns true but no sync.pull cap.
	noRead := &fakeSession{
		hasVault: map[string]bool{prefix: true},
		hasCap:   map[string]bool{}, // no caps
	}
	ss := &fakeSessions{session: noRead}
	got := Resolve(VaultMeta{GuestRole: "none", Prefix: prefix}, newReq(), ss)
	if got != RoleNone {
		t.Errorf("authenticated, no sync.pull, GuestRole=none: got %v, want RoleNone", got)
	}
}

// 5. sessions == nil → falls through gracefully (no panic).
func TestResolve_NilSessions_FallsThrough(t *testing.T) {
	got := Resolve(VaultMeta{GuestRole: "none", Prefix: "v"}, newReq(), nil)
	if got != RoleNone {
		t.Errorf("nil sessions, GuestRole=none: got %v, want RoleNone", got)
	}
}
