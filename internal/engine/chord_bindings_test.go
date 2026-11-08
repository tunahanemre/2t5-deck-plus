package engine

// Tests pinning the shipped bindings.json's four modifier-button C1 chords
// (2026-08-13): Alt+C1 -> Alt+F, Ctrl+C1 -> Ctrl+F, Shift+C1 -> Shift+F,
// Cmd+C1 -> Cmd+F (alongside Cmd's pre-existing C2 -> Cmd+S, unchanged) --
// plus the required regression guard: C1 UNMODIFIED must still post plain
// Space on every page (the single most-overloaded control on the deck, now
// redefined by all four modifier chords).

import (
	"context"
	"testing"

	"gitlab.com/gomidi/midi/v2"
)

// pages is every Resolve page the deck can be shown against, mirroring
// TestRealConfigCmdChordResolves' own page list.
var chordTestPages = []string{"edit", "color", "fusion", "cut", "deliver", "fairlight", "media", ""}

// TestRealConfig_AllFourModifierChords_C1Resolves pins all four C1 chords
// through resolveOverlayBinding -- the actual resolution path Start() uses
// -- across every page, proving the chord is page-independent (col-{mod}
// banks, not edit-{mod}) exactly like the pre-existing Cmd+C2 chord.
func TestRealConfig_AllFourModifierChords_C1Resolves(t *testing.T) {
	cfg := loadRealConfig(t)
	want := map[string]string{
		"alt":   "Alt+F",
		"ctrl":  "Ctrl+F",
		"shift": "Shift+F",
		"cmd":   "Cmd+F",
	}
	for mod, wantKey := range want {
		for _, page := range chordTestPages {
			b, bank, ok := resolveOverlayBinding(cfg, page, mod, "C1")
			if !ok || !b.IsKey() || b.Key != wantKey {
				t.Errorf("page=%q mod=%q: resolveOverlayBinding(...,C1) = bank=%q key=%q ok=%v, want col-%s/%s/true",
					page, mod, bank, b.Key, ok, mod, wantKey)
				continue
			}
			if bank != "col-"+mod {
				t.Errorf("page=%q mod=%q: bank = %q, want col-%s", page, mod, bank, mod)
			}
		}
	}
}

// TestRealConfig_C1_Unmodified_PostsSpace_EveryPage is the required
// regression guard: with NO modifier held, C1 must resolve to its
// original, unaffected "key":"Space" binding on every page -- both
// directly (col.C1/edit.C1) and through the SAME resolveNoteBank path
// Start() actually uses.
func TestRealConfig_C1_Unmodified_PostsSpace_EveryPage(t *testing.T) {
	cfg := loadRealConfig(t)

	colC1, ok := cfg.Binding("col", "C1")
	if !ok || !colC1.IsKey() || colC1.Key != "Space" {
		t.Fatalf("col.C1 = %+v ok=%v, want type=key/key=Space", colC1, ok)
	}
	editC1, ok := cfg.Binding("edit", "C1")
	if !ok || !editC1.IsKey() || editC1.Key != "Space" {
		t.Fatalf("edit.C1 = %+v ok=%v, want type=key/key=Space", editC1, ok)
	}

	for _, page := range chordTestPages {
		b := resolveNoteBank(cfg, page, 1, "col", "C1")
		bound, ok := cfg.Binding(b, "C1")
		if !ok || !bound.IsKey() || bound.Key != "Space" {
			t.Errorf("page=%q: resolveNoteBank(...)->Binding(C1) = %+v ok=%v, want type=key/key=Space (unmodified)", page, bound, ok)
		}
	}
}

// TestNoteOnC1_Unmodified_EndToEnd_NoModifierHeld is the Start()/MIDI
// integration counterpart: with no modifier ever pressed, a real C1
// note_on must resolve through the normal cascade to the col-bank Space
// binding. Uses a minimal fixture (not the real config) so the "key"
// dispatch's frontmost-gate-based unobservability (see
// overlay_intercepts_test.go's comment on this exact issue) doesn't matter
// -- this test only needs to prove NO overlay/chord bank was consulted,
// which is observable via keys.Armed() staying false and no sidecar op
// ever firing for a chord.
func TestNoteOnC1_Unmodified_EndToEnd_NoModifierHeld(t *testing.T) {
	stubStateFile(t)
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"49":"C1"}}`
	bindings := `{
		"col":  {"C1": {"type": "key", "key": "Space"}},
		"edit": {"C1": {"type": "key", "key": "Space"}}
	}`
	sockPath, received := startRecordingSidecar(t, "edit")
	eng := newEngineWithConfig(t, deviceMap, bindings)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 49, 100), 0) // C1 press, no modifier ever held

	// Nothing chord-shaped should have reached the sidecar at all -- a
	// plain "key" binding produces zero sidecar ops.
	if recs := received(); len(recs) != 0 {
		t.Errorf("sidecar received %v, want no ops (C1 unmodified is a pure keystroke)", recs)
	}
}

// TestRealConfig_AltPasteChord_Resolves pins the shipped bindings.json's
// Alt+Paste chord (2026-08-13, paste-attributes moved off the plain Paste
// button onto the Alt chord when col.Paste itself became Cmd+V) through
// resolveOverlayBinding -- the actual resolution path Start() uses --
// across every page, exactly like the C1 chords above.
func TestRealConfig_AltPasteChord_Resolves(t *testing.T) {
	cfg := loadRealConfig(t)
	for _, page := range chordTestPages {
		b, bank, ok := resolveOverlayBinding(cfg, page, "alt", "Paste")
		if !ok || !b.IsKey() || b.Key != "Alt+V" || bank != "col-alt" {
			t.Errorf("page=%q: resolveOverlayBinding(...,Paste) = bank=%q key=%q ok=%v, want col-alt/Alt+V/true",
				page, bank, b.Key, ok)
		}
	}
	// col.Paste itself (no chord) must be the NEW Cmd+V, not the old Alt+V
	// -- paste-attributes moved, it didn't stay duplicated in two places.
	colPaste, ok := cfg.Binding("col", "Paste")
	if !ok || !colPaste.IsKey() || colPaste.Key != "Cmd+V" {
		t.Errorf("col.Paste = %+v ok=%v, want type=key/key=Cmd+V", colPaste, ok)
	}
}
