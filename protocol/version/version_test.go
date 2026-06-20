package version

import "testing"

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.2.0", "1.2.0", 0},
		{"1.2.0", "1.2.1", -1},
		{"1.3.0", "1.2.9", 1},
		{"1.2", "1.2.1", -1},   // missing component is 0
		{"1.2.0", "1.2", 0},    // trailing zeros equal
		{"dev", "0.0.0", 0},    // non-numeric parses as 0
		{"dev", "0.1.0", -1},   // dev is older than any release > 0
		{"0.2.0", "dev", 1},    // release is newer than dev
		{"dev", "dev", 0},      // reinstall of dev
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q,%q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
