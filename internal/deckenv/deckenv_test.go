package deckenv

import "testing"

func TestGetNewWinsThenLegacy(t *testing.T) {
	t.Setenv("DECKPLUS_ZZ", "")
	t.Setenv("LD_DECK_ZZ", "")
	if got, ok := Lookup("ZZ"); got != "" || ok {
		t.Fatalf("unset: got %q,%v", got, ok)
	}
	t.Setenv("LD_DECK_ZZ", "old")
	if got := Get("ZZ"); got != "old" {
		t.Fatalf("legacy fallback: got %q", got)
	}
	t.Setenv("DECKPLUS_ZZ", "new")
	if got := Get("ZZ"); got != "new" {
		t.Fatalf("new must win: got %q", got)
	}
}
