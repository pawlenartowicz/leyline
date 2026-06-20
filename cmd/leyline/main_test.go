package main

import (
	"testing"

	"github.com/pawlenartowicz/leyline/internal/cli/cli"
)

// TestResolveInitMode verifies mutual exclusion across the three init flags.
// Zero or one of {merge, from-server, from-local} is allowed; two or more
// is a hard error so the user catches typos before any destructive side-effects.
func TestResolveInitMode(t *testing.T) {
	cases := []struct {
		name                                string
		merge, fromServer, fromLocal        bool
		wantMode                            string
		wantErr                             bool
	}{
		{"all-unset-defaults-to-merge", false, false, false, cli.InitModeMerge, false},
		{"explicit-merge", true, false, false, cli.InitModeMerge, false},
		{"from-server", false, true, false, cli.InitModeFromServer, false},
		{"from-local", false, false, true, cli.InitModeFromLocal, false},
		{"merge-and-from-server", true, true, false, "", true},
		{"merge-and-from-local", true, false, true, "", true},
		{"from-server-and-from-local", false, true, true, "", true},
		{"all-three", true, true, true, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveInitMode(tc.merge, tc.fromServer, tc.fromLocal)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got mode=%q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.wantMode {
				t.Errorf("got %q, want %q", got, tc.wantMode)
			}
		})
	}
}
