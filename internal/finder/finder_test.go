package finder

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// TestDeckPlusArmedNeverSetDuringTests is a hard tripwire, mirroring the ones in
// internal/keys and internal/mouse. This package MUTATES THE USER'S FILES and
// there is no undo, so a test binary running with the arm variable set must
// stop rather than proceed.
func TestDeckPlusArmedNeverSetDuringTests(t *testing.T) {
	if v := os.Getenv("DECKPLUS_ARMED") + os.Getenv("LD_DECK_ARMED"); strings.Contains(v, "1") {
		t.Fatalf("DECKPLUS_ARMED=1 is set in the test environment — refusing to continue. "+
			"internal/finder writes real Finder tags to real files and there is no undo. "+
			"(got DECKPLUS_ARMED+LD_DECK_ARMED=%q)", v)
	}
}

func TestEnvArmedDetection(t *testing.T) {
	for _, tc := range []struct {
		val  string
		want bool
	}{{"1", true}, {"0", false}, {"", false}, {"true", false}, {"yes", false}} {
		t.Run("DECKPLUS_ARMED="+tc.val, func(t *testing.T) {
			t.Setenv("DECKPLUS_ARMED", tc.val)
			if got := envArmed(); got != tc.want {
				t.Errorf("envArmed() with DECKPLUS_ARMED=%q = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

// --- TagName: the iOS-corpus rule (spec §6) -------------------------------

func TestTagName_StripsTheColourIndex(t *testing.T) {
	cases := map[string]string{
		"Red\n1":    "Red",   // iOS-written: name Red, index 1 (Gray)
		"Green\n1":  "Green", // ditto
		"Yellow\n1": "Yellow",
		"Red\n6":    "Red", // canonical macOS-written
		"Red":       "Red", // NSURLTagNamesKey form
		"Draft\n0":  "Draft",
		"":          "",
	}
	for in, want := range cases {
		if got := TagName(in); got != want {
			t.Errorf("TagName(%q) = %q, want %q", in, got, want)
		}
	}
}

// The exact data-loss scenario the spec calls out: an iOS-tagged corpus where
// every entry carries index 1 (Gray). Comparing the index would report these
// files as Gray, so the Gray button would "toggle off" a visibly RED file.
func TestHasColour_UsesNameNotIndex_OnTheIOSCorpus(t *testing.T) {
	iosRed := []string{"Red\n1"}

	if !hasColour(iosRed, "Red") {
		t.Error(`hasColour(["Red\n1"], "Red") = false — an index-based comparison; Red would never toggle off`)
	}
	if hasColour(iosRed, "Gray") {
		t.Error(`hasColour(["Red\n1"], "Gray") = true — index-based; the Gray button would strip a visibly red file`)
	}
}

// --- decideBatchAction (spec §7) ------------------------------------------

func TestDecideBatchAction(t *testing.T) {
	cases := []struct {
		name     string
		existing [][]string
		colour   string
		want     action
	}{
		{"empty colour always clears", [][]string{{"Red"}}, "", actionClear},
		{"none have it -> add", [][]string{{}, {"Blue"}}, "Red", actionAdd},
		{"all have it -> remove", [][]string{{"Red"}, {"Red"}}, "Red", actionRemove},
		{"MIXED -> add (never a checkerboard)", [][]string{{"Red"}, {}}, "Red", actionAdd},
		{"all have it, iOS entries -> remove", [][]string{{"Red\n1"}, {"Red\n1"}}, "Red", actionRemove},
		{"no files -> add", nil, "Red", actionAdd},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decideBatchAction(c.existing, c.colour); got != c.want {
				t.Errorf("decideBatchAction() = %v, want %v", got, c.want)
			}
		})
	}
}

// --- nextTags (spec §7) ---------------------------------------------------

func sorted(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

func TestNextTags(t *testing.T) {
	cases := []struct {
		name     string
		existing []string
		colour   string
		a        action
		want     []string
	}{
		{"add to untagged", nil, "Red", actionAdd, []string{"Red"}},
		{"add is exclusive — other colour drops", []string{"Blue"}, "Red", actionAdd, []string{"Red"}},
		{"add preserves project tags", []string{"Draft", "Blue"}, "Red", actionAdd, []string{"Draft", "Red"}},
		{"remove drops only that colour", []string{"Red"}, "Red", actionRemove, nil},
		{"remove keeps other colours", []string{"Red", "Blue"}, "Red", actionRemove, []string{"Blue"}},
		{"remove preserves project tags", []string{"Draft", "Red"}, "Red", actionRemove, []string{"Draft"}},
		{"clear drops every colour", []string{"Red", "Blue"}, "", actionClear, nil},
		{"clear NEVER drops project tags", []string{"Draft", "Approved", "Red"}, "", actionClear, []string{"Approved", "Draft"}},
		{"iOS entries are canonicalised on write", []string{"Red\n1"}, "Green", actionAdd, []string{"Green"}},
		{"iOS project tag keeps its name only", []string{"Draft\n0"}, "Red", actionAdd, []string{"Draft", "Red"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := nextTags(c.existing, c.colour, c.a)
			if len(got) == 0 && len(c.want) == 0 {
				return
			}
			if !reflect.DeepEqual(sorted(got), sorted(c.want)) {
				t.Errorf("nextTags(%q, %q, %v) = %q, want %q", c.existing, c.colour, c.a, got, c.want)
			}
		})
	}
}

// --- arm guard ------------------------------------------------------------

func TestApplyColour_UnarmedRefusesToWrite(t *testing.T) {
	ResetNoopCalls()
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := ApplyColour([]string{p}, "Red")
	if !errors.Is(err, ErrNotArmed) {
		t.Fatalf("ApplyColour() unarmed error = %v, want ErrNotArmed", err)
	}
	if n != 0 {
		t.Errorf("ApplyColour() unarmed wrote %d files, want 0", n)
	}
	if got, err := ReadTags(p); err != nil || len(got) != 0 {
		t.Errorf("file was tagged while unarmed: tags=%q err=%v", got, err)
	}
	found := false
	for _, c := range NoopCalls() {
		if c == "ApplyColour" {
			found = true
		}
	}
	if !found {
		t.Errorf("NoopCalls() = %q, want it to record ApplyColour", NoopCalls())
	}
}

func TestApplyColour_RejectsAnUnknownColour(t *testing.T) {
	if _, err := ApplyColour([]string{"/tmp/whatever"}, "Chartreuse"); err == nil {
		t.Fatal("ApplyColour() with an unknown colour returned nil error")
	}
}

func TestApplyColour_EmptySelectionIsNotAnError(t *testing.T) {
	n, err := ApplyColour(nil, "Red")
	if err != nil || n != 0 {
		t.Fatalf("ApplyColour(nil) = (%d, %v), want (0, nil)", n, err)
	}
}

// --- real filesystem round trip -------------------------------------------
//
// Exercises the actual NSURL/CGO layer, deliberately via writeTagsUnguarded
// rather than by arming the package: Arm() flips a process-global that would
// stay set for every later test in this binary, which is exactly the hazard
// the tripwire above exists to prevent. Same-package access lets the test
// reach the real writer without ever opening that door.

func skipIfUnsupported(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "not supported") {
		t.Skipf("extended attributes unsupported on this filesystem: %v", err)
	}
}

func TestReadWriteTags_RoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "clip.mov")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	tags, err := ReadTags(p)
	if err != nil {
		t.Fatalf("ReadTags() on a fresh file: %v", err)
	}
	if len(tags) != 0 {
		t.Fatalf("fresh file already has tags: %q", tags)
	}

	err = writeTagsUnguarded(p, []string{"Red", "Draft"})
	skipIfUnsupported(t, err)
	if err != nil {
		t.Fatalf("writeTagsUnguarded(): %v", err)
	}

	got, err := ReadTags(p)
	if err != nil {
		t.Fatalf("ReadTags() after write: %v", err)
	}
	if want := []string{"Draft", "Red"}; !reflect.DeepEqual(sorted(got), want) {
		t.Fatalf("ReadTags() = %q, want %q", sorted(got), want)
	}

	// Clearing must remove everything and must not error on an already-clean
	// file (P8 pressed twice).
	if err := writeTagsUnguarded(p, nil); err != nil {
		t.Fatalf("writeTagsUnguarded(nil): %v", err)
	}
	if got, err := ReadTags(p); err != nil || len(got) != 0 {
		t.Fatalf("after clear: tags=%q err=%v, want none", got, err)
	}
	if err := writeTagsUnguarded(p, nil); err != nil {
		t.Fatalf("clearing an already-clear file errored: %v", err)
	}
}

// A colour written through this path must come back under NSURLTagNamesKey as
// the bare NAME -- which is what makes the name-based comparison in §6 work
// against files this tool writes as well as files iOS wrote.
func TestWriteTags_ColourReadsBackAsAName(t *testing.T) {
	p := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := writeTagsUnguarded(p, []string{"Green"})
	skipIfUnsupported(t, err)
	if err != nil {
		t.Fatalf("writeTagsUnguarded(): %v", err)
	}
	got, err := ReadTags(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || TagName(got[0]) != "Green" {
		t.Fatalf("ReadTags() = %q, want a single entry named Green", got)
	}
}

func TestReadTags_MissingFileErrors(t *testing.T) {
	if _, err := ReadTags(filepath.Join(t.TempDir(), "nope.txt")); err == nil {
		t.Fatal("ReadTags() on a missing file returned nil error")
	}
}
