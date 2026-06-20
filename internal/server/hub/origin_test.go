package hub

import "testing"

func TestOriginAllowed(t *testing.T) {
	allow := []string{"https://reader.example.com", "https://admin.example.com"}
	cases := []struct {
		name   string
		origin string
		want   bool
	}{
		{"empty origin (CLI/Electron)", "", true},
		{"matched origin", "https://reader.example.com", true},
		{"second matched origin", "https://admin.example.com", true},
		{"mismatched origin", "https://evil.example.com", false},
		{"scheme mismatch", "http://reader.example.com", false},
		{"port mismatch", "https://reader.example.com:8443", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := originAllowed(c.origin, allow); got != c.want {
				t.Errorf("originAllowed(%q) = %v, want %v", c.origin, got, c.want)
			}
		})
	}

	// Empty allowlist: present Origin must be rejected; empty Origin still accepted.
	if originAllowed("https://anything.example.com", nil) {
		t.Errorf("empty allowlist must reject any present Origin")
	}
	if !originAllowed("", nil) {
		t.Errorf("empty allowlist must still accept missing Origin")
	}
}
