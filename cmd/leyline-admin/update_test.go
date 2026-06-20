package main

import "testing"

func TestCheckBundle_SkewRejected(t *testing.T) {
	probe := func(path string) (string, error) {
		if path == "server" {
			return "0.2.0", nil
		}
		return "0.1.0", nil // leyline-admin disagrees
	}
	_, err := checkBundle("server", "leyline-admin", probe)
	if err == nil {
		t.Fatal("expected skew rejection")
	}
}

func TestCheckBundle_MatchAccepted(t *testing.T) {
	probe := func(string) (string, error) { return "0.2.0", nil }
	v, err := checkBundle("server", "leyline-admin", probe)
	if err != nil {
		t.Fatal(err)
	}
	if v != "0.2.0" {
		t.Errorf("version = %q, want 0.2.0", v)
	}
}

func TestDowngradeGuard(t *testing.T) {
	// installed >= to-install and not assume-yes → needs confirm (no reader → abort)
	if err := confirmIfDowngrade("0.2.0", "0.1.0", false, nil, nil); err == nil {
		t.Fatal("expected abort on declined downgrade with no input")
	}
	// strictly newer → no prompt, no error
	if err := confirmIfDowngrade("0.1.0", "0.2.0", false, nil, nil); err != nil {
		t.Fatalf("strictly newer should proceed: %v", err)
	}
	// assume-yes bypasses
	if err := confirmIfDowngrade("0.2.0", "0.1.0", true, nil, nil); err != nil {
		t.Fatalf("--yes should bypass: %v", err)
	}
}

func TestResolveServerPaths_Discovery(t *testing.T) {
	server, admin := resolveServerPaths("/opt/custom/bin/leyline-admin", "", "")
	if admin != "/opt/custom/bin/leyline-admin" {
		t.Errorf("admin = %q, want the running executable", admin)
	}
	if server != "/opt/custom/bin/leyline-server" {
		t.Errorf("server = %q, want the sibling leyline-server", server)
	}
}

func TestResolveServerPaths_FlagsWin(t *testing.T) {
	server, admin := resolveServerPaths("/opt/custom/bin/leyline-admin", "/x/leyline-server", "/y/leyline-admin")
	if server != "/x/leyline-server" || admin != "/y/leyline-admin" {
		t.Errorf("flags should win: server=%q admin=%q", server, admin)
	}
}

func TestResolveWebPath(t *testing.T) {
	if got := resolveWebPath(""); got != defaultWebPath {
		t.Errorf("default web path = %q, want %q", got, defaultWebPath)
	}
	if got := resolveWebPath("/opt/leyline-web-test/bin/leyline-web"); got != "/opt/leyline-web-test/bin/leyline-web" {
		t.Errorf("override not honored: %q", got)
	}
}

func TestResolveService(t *testing.T) {
	if got := resolveService("", true); got != "leyline-server" {
		t.Errorf("server-mode default = %q, want leyline-server", got)
	}
	if got := resolveService("", false); got != "leyline-web" {
		t.Errorf("web-mode default = %q, want leyline-web", got)
	}
	if got := resolveService("leyline-test", true); got != "leyline-test" {
		t.Errorf("override = %q, want leyline-test", got)
	}
}
