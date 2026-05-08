// Package handlepick extracts the first URL from a free-form prompt string.
// Used by the compress agent to dereference a handle JSON without parsing
// JSON strictly (the prompt may have arbitrary surrounding text written by
// the LLM planner).
package handlepick

import (
	"regexp"
	"strings"
)

var urlRe = regexp.MustCompile(`https?://[^\s"<>]+`)

// FirstURL returns the first http/https URL in s with trailing punctuation
// stripped, or ("", false) if none is found.
func FirstURL(s string) (string, bool) {
	m := urlRe.FindString(s)
	if m == "" {
		return "", false
	}
	m = strings.TrimRight(m, ".,;:!?)")
	return m, true
}
