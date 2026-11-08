// Package deckenv reads 2T5 DECK+ environment variables under their current
// DECKPLUS_<NAME> name and, when that is unset or empty, under the name the
// app had before it was renamed (LD_DECK_<NAME>). Both spellings are
// supported so existing launch scripts, plists and shells keep working; the
// new name always wins when both are set.
package deckenv

import (
	"os"
	"strings"
)

const (
	// Prefix is the current variable prefix.
	Prefix = "DECKPLUS_"
	// LegacyPrefix is the pre-rename prefix, still read as a fallback.
	LegacyPrefix = "LD_DECK_"
)

// Get returns the value of DECKPLUS_<suffix>, or of LD_DECK_<suffix> when the
// new one is unset or empty. The value is returned as stored (not trimmed).
func Get(suffix string) string {
	if v := os.Getenv(Prefix + suffix); v != "" {
		return v
	}
	return os.Getenv(LegacyPrefix + suffix)
}

// Lookup is Get plus a flag that says whether either name was set non-empty.
func Lookup(suffix string) (string, bool) {
	v := Get(suffix)
	return v, strings.TrimSpace(v) != ""
}
