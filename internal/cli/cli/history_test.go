package cli

import (
	"testing"
	"time"
)

func TestParseDurationLoose(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 0, false},
		{"7d", 7 * 24 * time.Hour, false},
		{"2w", 2 * 7 * 24 * time.Hour, false},
		{"3h", 3 * time.Hour, false},
		{"30m", 30 * time.Minute, false},
		{"bad", 0, true},
	}
	for _, c := range cases {
		got, err := parseDurationLoose(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("%q err=%v wantErr=%v", c.in, err, c.wantErr)
		}
		if !c.wantErr && got != c.want {
			t.Errorf("%q = %v, want %v", c.in, got, c.want)
		}
	}
}
