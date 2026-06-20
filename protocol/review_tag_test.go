package protocol

import (
	"testing"
	"time"
)

func TestFormatParseReviewTag_RoundTrip(t *testing.T) {
	want := time.Date(2026, 5, 18, 15, 4, 5, 0, time.UTC)
	tag := FormatReviewTag(want)
	if tag != "reviewed-2026-05-18T15-04-05Z" {
		t.Errorf("FormatReviewTag = %q, want %q", tag, "reviewed-2026-05-18T15-04-05Z")
	}
	got, ok := ParseReviewTag(tag)
	if !ok {
		t.Fatalf("ParseReviewTag(%q): ok=false", tag)
	}
	if !got.Equal(want) {
		t.Errorf("ParseReviewTag(%q) = %v, want %v", tag, got, want)
	}
}

func TestFormatReviewTag_ConvertsToUTC(t *testing.T) {
	loc := time.FixedZone("EST", -5*3600)
	local := time.Date(2026, 5, 18, 10, 4, 5, 0, loc) // 15:04:05Z
	if tag := FormatReviewTag(local); tag != "reviewed-2026-05-18T15-04-05Z" {
		t.Errorf("FormatReviewTag(EST) = %q, want UTC form", tag)
	}
}

func TestParseReviewTag_Negative(t *testing.T) {
	cases := []string{
		"",
		"not-a-tag",
		"reviewed-",
		"reviewed-2026-05-18T15:04:05Z", // colons (not legal git refname form)
		"reviewed-not-a-time",
		"tagged-2026-05-18T15-04-05Z", // wrong prefix
	}
	for _, c := range cases {
		if _, ok := ParseReviewTag(c); ok {
			t.Errorf("ParseReviewTag(%q) ok=true, want false", c)
		}
	}
}
