package caps

import (
	"reflect"
	"testing"
	"time"
)

func TestParseCapability(t *testing.T) {
	if c, ok := ParseCapability("sync.pull"); !ok || c != SyncPull {
		t.Errorf("ParseCapability(sync.pull) = (%q, %v), want (SyncPull, true)", c, ok)
	}
	if c, ok := ParseCapability("not.a.cap"); ok || c != "" {
		t.Errorf("ParseCapability(not.a.cap) = (%q, %v), want (\"\", false)", c, ok)
	}
}

func TestSet_Strings(t *testing.T) {
	s := NewSet(SyncPush, SyncPull, HistoryTag)
	got := s.Strings()
	want := []string{"history.tag", "sync.pull", "sync.push"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Set.Strings() = %v, want %v (alphabetical)", got, want)
	}
}

func TestResolve_BuiltinRoles(t *testing.T) {
	cases := []struct {
		role string
		want []Capability
	}{
		{"admin", []Capability{SyncPull, SyncPush, KeysManage, VaultAdmin, HistoryTag, HistoryRevert}},
		{"editor", []Capability{SyncPull, SyncPush, HistoryRevert}},
		{"reader", []Capability{SyncPull}},
	}
	for _, tc := range cases {
		t.Run(tc.role, func(t *testing.T) {
			s, err := Resolve(tc.role, nil, time.Time{})
			if err != nil {
				t.Fatalf("Resolve(%q): %v", tc.role, err)
			}
			for _, c := range tc.want {
				if !s.Has(c) {
					t.Errorf("%s missing %s", tc.role, c)
				}
			}
		})
	}
}

func TestResolve_CustomRole(t *testing.T) {
	custom := map[string]Set{
		"research-lead": {caps: setOf(SyncPull, SyncPush, KeysManage)},
	}
	s, err := Resolve("research-lead", custom, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Has(KeysManage) || s.Has(VaultAdmin) {
		t.Fatalf("unexpected caps: %+v", s)
	}
}

func TestResolve_UnknownRole(t *testing.T) {
	if _, err := Resolve("ghost", nil, time.Time{}); err != ErrUnknownRole {
		t.Fatalf("want ErrUnknownRole, got %v", err)
	}
}

func TestResolve_BuiltinShadowsCustom(t *testing.T) {
	custom := map[string]Set{"admin": {caps: setOf(SyncPull)}}
	s, _ := Resolve("admin", custom, time.Time{})
	if !s.Has(VaultAdmin) {
		t.Fatal("built-in admin must shadow custom")
	}
}

func TestSet_Has(t *testing.T) {
	s := Set{caps: setOf(SyncPull)}
	if !s.Has(SyncPull) || s.Has(VaultAdmin) {
		t.Fatal("Has mismatch")
	}
	expired := Set{caps: setOf(SyncPull), expiresAt: time.Now().Add(-time.Hour)}
	if expired.Has(SyncPull) {
		t.Fatal("expired set must not grant caps")
	}
}

func TestSet_Equal(t *testing.T) {
	t1 := time.Unix(1, 0)
	a := Set{caps: setOf(SyncPull, SyncPush), expiresAt: t1}
	b := Set{caps: setOf(SyncPush, SyncPull), expiresAt: t1}
	if !a.Equal(b) {
		t.Fatal("same caps + expiresAt → equal")
	}
	c := Set{caps: setOf(SyncPull), expiresAt: t1}
	if a.Equal(c) {
		t.Fatal("differing caps → not equal")
	}
	d := Set{caps: setOf(SyncPull, SyncPush), expiresAt: t1.Add(time.Second)}
	if a.Equal(d) {
		t.Fatal("differing expiresAt → not equal")
	}
}

func TestReserved_GuestSuffix(t *testing.T) {
	for _, name := range []string{"admin", "editor", "reader", "foo_guest", "editor_guest"} {
		if !IsReserved(name) {
			t.Errorf("%s should be reserved", name)
		}
	}
	for _, name := range []string{"foo", "research-lead", "guest_admin"} {
		if IsReserved(name) {
			t.Errorf("%s should not be reserved", name)
		}
	}
}

// TestNewSet_KnownCaps verifies that NewSet constructed from known capabilities
// holds exactly those capabilities and no others.
func TestNewSet_KnownCaps(t *testing.T) {
	s := NewSet(SyncPull, SyncPush)
	if !s.Has(SyncPull) {
		t.Error("NewSet(SyncPull, SyncPush): SyncPull missing")
	}
	if !s.Has(SyncPush) {
		t.Error("NewSet(SyncPull, SyncPush): SyncPush missing")
	}
	if s.Has(VaultAdmin) {
		t.Error("NewSet(SyncPull, SyncPush): VaultAdmin should not be present")
	}
}

// TestNewSet_UnknownCaps pins the contract for NewSet with unknown capability
// names: NewSet is a dumb constructor that does NOT validate or drop caps.
// It is the caller's (roles parser's) responsibility to filter. Passing an
// unknown cap string produces a Set that holds it.
func TestNewSet_UnknownCaps(t *testing.T) {
	unknown := Capability("unknown.cap")
	s := NewSet(unknown)
	// NewSet does not drop unknowns — it keeps them verbatim.
	caps := s.Capabilities()
	if len(caps) != 1 || caps[0] != unknown {
		t.Errorf("NewSet(unknown): Capabilities() = %v, want [%q]", caps, unknown)
	}
}

// TestKnown_RoundTrip verifies that every capability returned by Known() can
// be re-constructed through NewSet and round-trips back cleanly.
func TestKnown_RoundTrip(t *testing.T) {
	allKnown := []Capability{SyncPull, SyncPush, KeysManage, VaultAdmin, HistoryTag, HistoryRevert}
	for _, c := range allKnown {
		if !Known(c) {
			t.Errorf("Known(%q) = false, want true", c)
		}
	}

	// Round-trip: build a Set from all known caps, get Capabilities(), feed
	// back into NewSet, verify the resulting Set is equal.
	original := NewSet(allKnown...)
	caps := original.Capabilities()
	rebuilt := NewSet(caps...)
	if !original.Equal(rebuilt) {
		t.Errorf("round-trip mismatch: original=%v rebuilt=%v", original.Capabilities(), rebuilt.Capabilities())
	}
}

// TestCapabilities_StableSortOrder calls Capabilities() twice on the same Set
// and asserts the slices are identical. The sort must be deterministic — any
// map-iteration non-determinism would surface here.
func TestCapabilities_StableSortOrder(t *testing.T) {
	s := NewSet(HistoryRevert, SyncPull, VaultAdmin, SyncPush, KeysManage, HistoryTag)
	first := s.Capabilities()
	second := s.Capabilities()
	if len(first) != len(second) {
		t.Fatalf("Capabilities() length changed: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Errorf("Capabilities()[%d] changed: %q vs %q", i, first[i], second[i])
		}
	}
	// Also assert the slice is actually sorted.
	for i := 1; i < len(first); i++ {
		if first[i] < first[i-1] {
			t.Errorf("Capabilities() not sorted at index %d: %q < %q", i, first[i], first[i-1])
		}
	}
}
