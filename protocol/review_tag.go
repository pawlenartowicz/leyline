package protocol

import (
	"strings"
	"time"
)

// FormatReviewTag returns the canonical review-tag name for t — UTC time
// in the form `reviewed-YYYY-MM-DDTHH-MM-SSZ`. The dashes replace colons
// because `:` is forbidden in git refnames.
//
// t is converted to UTC before formatting; passing a non-UTC time is fine
// (the second-level precision is the only thing this preserves).
func FormatReviewTag(t time.Time) string {
	return ReviewTagPrefix + t.UTC().Format(ReviewTagTimeLayout)
}

// ParseReviewTag reverses FormatReviewTag — returns the timestamp encoded
// in the tag name and ok=true. Names that don't start with the review
// prefix, or whose suffix doesn't parse as the canonical layout, return
// the zero time and ok=false.
func ParseReviewTag(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, ReviewTagPrefix) {
		return time.Time{}, false
	}
	t, err := time.Parse(ReviewTagTimeLayout, name[len(ReviewTagPrefix):])
	if err != nil {
		return time.Time{}, false
	}
	return t.UTC(), true
}
