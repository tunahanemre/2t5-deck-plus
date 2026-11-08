package engine

// Tests for Part B's held-modifier CHORD OVERLAY (2026-08-12 REV 2 design
// review): the "modifierHold" config.Binding type, the Engine's
// heldOverlayName state (setHeldOverlay/clearHeldOverlay/getHeldOverlay),
// resolveOverlayBinding's {page}-{mod}/col-{mod} resolution, the press
// intercept (mirroring fineModeHold's press path), and the release branch's
// new case (msg.GetNoteEnd, covering BOTH a vel==0 note-on and a real MIDI
// Note Off -- see the "RealNoteOff"-suffixed tests below for the latter,
// added after the 2026-08-12 live finding that the device sends genuine
// Note Off, not vel==0, for at least C1).
//
// This REPLACES the reverted type:"modifier" design (see
// internal/engine's git history for modifier_test.go, deleted alongside
// that revert) -- deliberately NOT modeled as another layer number (see
// resolveOverlayBinding's doc comment), and the press/release here posts
// NO CGEvent at all: keys.NoopCalls() should stay EMPTY throughout every
// test in this file, proving the "one JSON binding away from a stuck
// modifier" risk the old design carried is structurally gone, not just
// patched.
//
// All Start()-based tests stub stateFilePath (see stubStateFile in
// marker_test.go) to stay isolated from the real, currently-running app's
// ~/.2t5-deck-plus-state.json.

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"

	"2t5-deck-plus/internal/keys"
)

// ---------------------------------------------------------------------------
// resolveOverlayBinding -- pure-function unit tests, no Start()
// ---------------------------------------------------------------------------

func TestResolveOverlayBinding_PageBankWinsOverColBank(t *testing.T) {
	cfg := loadConfig(t, `{
	  "col-cmd":  {"C2": {"type":"key","key":"Cmd+S"}},
	  "edit-cmd": {"C2": {"type":"key","key":"Cmd+K"}}
	}`)
	b, bank, ok := resolveOverlayBinding(cfg, "edit", "cmd", "C2")
	if !ok || bank != "edit-cmd" || b.Key != "Cmd+K" {
		t.Errorf("got bank=%q key=%q ok=%v, want edit-cmd/Cmd+K/true", bank, b.Key, ok)
	}
}

func TestResolveOverlayBinding_FallsBackToColBank(t *testing.T) {
	cfg := loadConfig(t, `{
	  "col-cmd": {"C2": {"type":"key","key":"Cmd+S"}}
	}`)
	b, bank, ok := resolveOverlayBinding(cfg, "edit", "cmd", "C2")
	if !ok || bank != "col-cmd" || b.Key != "Cmd+S" {
		t.Errorf("got bank=%q key=%q ok=%v, want col-cmd/Cmd+S/true", bank, b.Key, ok)
	}
}

func TestResolveOverlayBinding_MissWhenModEmpty(t *testing.T) {
	cfg := loadConfig(t, `{"col-cmd": {"C2": {"type":"key","key":"Cmd+S"}}}`)
	if _, _, ok := resolveOverlayBinding(cfg, "edit", "", "C2"); ok {
		t.Error("empty mod must never resolve, regardless of what banks exist")
	}
}

func TestResolveOverlayBinding_MissWhenNeitherBankDefinesControl(t *testing.T) {
	cfg := loadConfig(t, `{"col-cmd": {"C2": {"type":"key","key":"Cmd+S"}}}`)
	if _, _, ok := resolveOverlayBinding(cfg, "edit", "cmd", "C3"); ok {
		t.Error("a control neither overlay bank defines must miss")
	}
}

// ---------------------------------------------------------------------------
// Engine-level overlay state machine -- direct unit tests, no Start()
// ---------------------------------------------------------------------------

func TestSetGetHeldOverlay_Basic(t *testing.T) {
	eng := &Engine{}
	if got := eng.getHeldOverlay(); got != "" {
		t.Fatalf("getHeldOverlay() on a fresh Engine = %q, want \"\"", got)
	}
	eng.setHeldOverlay("cmd")
	if got := eng.getHeldOverlay(); got != "cmd" {
		t.Fatalf("getHeldOverlay() after setHeldOverlay(cmd) = %q, want cmd", got)
	}
}

// TestSetHeldOverlay_Overwrites proves there is no "two held at once" set
// like the reverted design's heldModifierSet -- a later press simply
// replaces whatever was held.
func TestSetHeldOverlay_Overwrites(t *testing.T) {
	eng := &Engine{}
	eng.setHeldOverlay("cmd")
	eng.setHeldOverlay("alt")
	if got := eng.getHeldOverlay(); got != "alt" {
		t.Fatalf("getHeldOverlay() after overwrite = %q, want alt", got)
	}
}

// TestClearHeldOverlay_OnlyClearsMatchingName is the idempotency guard: a
// release for a modifier name that is NOT the one currently held must not
// clear it -- makes duplicate/stray note-offs safe by construction.
func TestClearHeldOverlay_OnlyClearsMatchingName(t *testing.T) {
	eng := &Engine{}
	eng.setHeldOverlay("cmd")
	eng.clearHeldOverlay("alt") // stray release for a DIFFERENT modifier
	if got := eng.getHeldOverlay(); got != "cmd" {
		t.Fatalf("getHeldOverlay() after mismatched clear = %q, want cmd (must be untouched)", got)
	}
	eng.clearHeldOverlay("cmd") // the actual release
	if got := eng.getHeldOverlay(); got != "" {
		t.Fatalf("getHeldOverlay() after matching clear = %q, want \"\"", got)
	}
}

// TestClearHeldOverlay_DuplicateReleaseIsNoop is the exact idempotency case
// the task calls out: the note_on debounce (commit c2b73a5) does not cover
// vel==0, so duplicate physical note-offs can all reach the release branch
// -- the second (and any subsequent) call must be a harmless no-op.
func TestClearHeldOverlay_DuplicateReleaseIsNoop(t *testing.T) {
	eng := &Engine{}
	eng.setHeldOverlay("cmd")
	eng.clearHeldOverlay("cmd")
	eng.clearHeldOverlay("cmd") // duplicate -- must not panic or misbehave
	eng.clearHeldOverlay("cmd") // and again
	if got := eng.getHeldOverlay(); got != "" {
		t.Fatalf("getHeldOverlay() after repeated clears = %q, want \"\"", got)
	}
}

func TestClearHeldOverlay_NothingHeld_Noop(t *testing.T) {
	eng := &Engine{}
	eng.clearHeldOverlay("cmd") // nothing was ever held
	if got := eng.getHeldOverlay(); got != "" {
		t.Fatalf("getHeldOverlay() = %q, want \"\"", got)
	}
}

// TestHeldOverlayState_ConcurrentRaceFree proves heldOverlayMu actually
// guards heldOverlayName -- one goroutine repeatedly sets/clears
// (simulating the MIDI callback) while a second repeatedly reads (simulating
// the drain/status-tick goroutine's Status().HeldOverlay poll). Must pass
// under `go test -race`.
func TestHeldOverlayState_ConcurrentRaceFree(t *testing.T) {
	eng := &Engine{}
	const iterations = 200
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			eng.setHeldOverlay("cmd")
			eng.clearHeldOverlay("cmd")
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < iterations; i++ {
			_ = eng.getHeldOverlay()
		}
	}()

	wg.Wait()
	// No assertion beyond "the race detector found nothing" -- final state
	// is inherently nondeterministic given the concurrent goroutines.
}

// ---------------------------------------------------------------------------
// Full Start()/MIDI drain-loop integration via note_on/note_off
// ---------------------------------------------------------------------------

// chordDeviceMap/chordBindings are shared by the integration tests below:
// Cmd (note 50) is a modifierHold button; Target (note 51) has a col-bank
// binding AND a col-cmd chord override; Other (note 52) has ONLY a col-bank
// binding (no chord), proving the "held overlay never strands an un-charted
// control" fallthrough. cdl bindings (not "key") are used so dispatch is
// OBSERVABLE via the fake sidecar without depending on
// macapp.BundleIDIsResolve(), which always SKIPs under `go test` -- same
// technique every other Start()-integration test in this package uses.
const chordDeviceMap = `{"knobs":{},"sliders":{},"buttons":{"50":"Cmd","51":"Target","52":"Other"}}`

const chordBindings = `{
	"_config": {"keyDebounceMs": 1},
	"col": {
		"Cmd":    {"type": "modifierHold", "modifier": "cmd"},
		"Target": {"type": "cdl", "param": "offset", "ch": "master"},
		"Other":  {"type": "cdl", "param": "gain", "ch": "master"}
	},
	"col-cmd": {
		"Target": {"type": "cdl", "param": "contrast", "ch": "master"}
	},
	"edit": {}
}`

func TestNoteOnModifierHold_Press_SetsOverlay_NoOSSideEffect(t *testing.T) {
	stubStateFile(t)
	keys.ResetNoopCalls()
	t.Cleanup(keys.ResetNoopCalls)

	eng := newEngineWithConfig(t, chordDeviceMap, chordBindings)
	eng.sockPath = startFakeSidecar(t)
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 50, 100), 0) // Cmd press

	if got := eng.getHeldOverlay(); got != "cmd" {
		t.Errorf("getHeldOverlay() = %q, want cmd", got)
	}
	// The whole point of the overlay design over the reverted "modifier"
	// type: press/release themselves post ZERO CGEvents. Any real
	// keystroke only ever happens through a normal "key" binding's OWN
	// dispatch, never here.
	if calls := keys.NoopCalls(); len(calls) != 0 {
		t.Errorf("NoopCalls() = %v, want empty -- modifierHold press must have NO OS-level side effect", calls)
	}
}

func TestNoteOnModifierHold_PressThenRelease_ClearsOverlay(t *testing.T) {
	stubStateFile(t)
	keys.ResetNoopCalls()
	t.Cleanup(keys.ResetNoopCalls)

	eng := newEngineWithConfig(t, chordDeviceMap, chordBindings)
	eng.sockPath = startFakeSidecar(t)
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 50, 100), 0) // press
	cb(midi.NoteOn(0, 50, 0), 0)   // release (vel==0)

	if got := eng.getHeldOverlay(); got != "" {
		t.Errorf("getHeldOverlay() after release = %q, want \"\"", got)
	}
	if calls := keys.NoopCalls(); len(calls) != 0 {
		t.Errorf("NoopCalls() = %v, want empty", calls)
	}
}

// TestNoteOnModifierHold_DuplicateRelease_IsIdempotent fires TWO note-offs
// for the same physical release (the note_on debounce does not cover
// vel==0 -- see clearHeldOverlay's doc comment) and confirms the second is
// a harmless no-op, not a panic or a corruption of a NEWLY-held overlay
// that happened to be set in between.
func TestNoteOnModifierHold_DuplicateRelease_IsIdempotent(t *testing.T) {
	stubStateFile(t)
	eng := newEngineWithConfig(t, chordDeviceMap, chordBindings)
	eng.sockPath = startFakeSidecar(t)
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 50, 100), 0) // press
	cb(midi.NoteOn(0, 50, 0), 0)   // release #1 -- clears
	cb(midi.NoteOn(0, 50, 0), 0)   // release #2 (duplicate) -- must be a no-op

	if got := eng.getHeldOverlay(); got != "" {
		t.Errorf("getHeldOverlay() after duplicate release = %q, want \"\"", got)
	}
}

// TestNoteOnModifierHold_PressThenRealNoteOffRelease_ClearsOverlay is the
// CRITICAL regression test for the 2026-08-12 live finding: the Loupedeck
// sends a REAL MIDI Note Off (status 0x8n, confirmed live via
// raw=80 31 40 -- channel 0, note 49, velocity 64) for at least C1, not the
// note-on-with-vel==0 idiom every other release test in this file uses.
// msg.GetNoteOn returns false outright for a real Note Off (confirmed by
// reading the vendored gitlab.com/gomidi/midi/v2@v2.3.23 message.go), so
// before the msg.GetNoteEnd fix, this exact sequence left the Cmd chord
// overlay stuck HELD forever -- Cmd could be pressed but never released,
// permanently routing every subsequent press through the col-cmd bank
// (C2 -> Cmd+S) with no way back. Uses velocity 64, not 0, matching the
// live trace exactly, to prove recognition is independent of the Note
// Off's velocity byte.
func TestNoteOnModifierHold_PressThenRealNoteOffRelease_ClearsOverlay(t *testing.T) {
	stubStateFile(t)
	keys.ResetNoopCalls()
	t.Cleanup(keys.ResetNoopCalls)

	eng := newEngineWithConfig(t, chordDeviceMap, chordBindings)
	eng.sockPath = startFakeSidecar(t)
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 50, 100), 0)         // Cmd press
	cb(midi.NoteOffVelocity(0, 50, 64), 0) // Cmd release -- REAL Note Off, not vel==0

	if got := eng.getHeldOverlay(); got != "" {
		t.Errorf("getHeldOverlay() after real Note Off release = %q, want \"\" -- a real 0x8n Note Off must clear the overlay exactly like a vel==0 note-on does", got)
	}
	if calls := keys.NoopCalls(); len(calls) != 0 {
		t.Errorf("NoopCalls() = %v, want empty", calls)
	}

	// Prove the deck is not left stuck: pressing Target NOW (Cmd already
	// released) must resolve the NORMAL col binding (offset), not the
	// col-cmd chord (contrast) -- if the overlay had failed to clear, this
	// would wrongly still fire the chord.
	sockPath, received := startRecordingSidecar(t, "color")
	eng2 := newEngineWithConfig(t, chordDeviceMap, chordBindings)
	eng2.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr2 := stubMIDICapture(t)
	if err := eng2.Start(context.Background()); err != nil {
		t.Fatalf("Start (eng2): %v", err)
	}
	defer eng2.Stop()
	cb2 := *cbPtr2
	cb2(midi.NoteOn(0, 50, 100), 0)         // Cmd press
	cb2(midi.NoteOffVelocity(0, 50, 64), 0) // Cmd release, REAL Note Off
	cb2(midi.NoteOn(0, 51, 100), 0)         // Target press, Cmd no longer held
	waitForRecs(t, received, 1)
	if p := received()[0]["param"]; p != "offset" {
		t.Fatalf("param = %v, want offset -- overlay must be fully cleared after a real Note Off release, not just internally flagged clear while still routing chords", p)
	}
}

// TestNoteOnModifierHold_DuplicateRealNoteOffRelease_IsIdempotent mirrors
// TestNoteOnModifierHold_DuplicateRelease_IsIdempotent but with two REAL
// Note Off messages instead of two vel==0 note-ons -- the note_on debounce
// does not cover releases of EITHER convention (see isDuplicateNoteOn's doc
// comment), so a device that emits a duplicate real Note Off burst (the
// same class of duplication BUG 1 already proved happens for presses) must
// not panic or misbehave.
func TestNoteOnModifierHold_DuplicateRealNoteOffRelease_IsIdempotent(t *testing.T) {
	stubStateFile(t)
	eng := newEngineWithConfig(t, chordDeviceMap, chordBindings)
	eng.sockPath = startFakeSidecar(t)
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 50, 100), 0)         // press
	cb(midi.NoteOffVelocity(0, 50, 64), 0) // release #1 (real Note Off) -- clears
	cb(midi.NoteOffVelocity(0, 50, 64), 0) // release #2 (duplicate) -- must be a no-op

	if got := eng.getHeldOverlay(); got != "" {
		t.Errorf("getHeldOverlay() after duplicate real Note Off release = %q, want \"\"", got)
	}
}

// TestNoteOnFineModeHold_RealNoteOffRelease_TurnsFineModeOff is the C5
// counterpart of TestNoteOnModifierHold_PressThenRealNoteOffRelease_
// ClearsOverlay: the live-shipped C5 binding (fineModeHold) has the exact
// same bug -- before the fix, C5 could be pressed to turn fine mode ON but
// a real Note Off release would never be recognized (msg.GetNoteOn returns
// false for it), latching fine mode ON permanently with no way to turn it
// off from the deck.
func TestNoteOnFineModeHold_RealNoteOffRelease_TurnsFineModeOff(t *testing.T) {
	stubStateFile(t)
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"68":"C5"}}`
	bindings := `{
		"col":  {"C5": {"type": "fineModeHold"}},
		"edit": {}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)
	eng.sockPath = startFakeSidecar(t)
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 68, 100), 0) // press: fine mode ON
	deadline := time.Now().Add(2 * time.Second)
	for !eng.Status().FineMode && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !eng.Status().FineMode {
		t.Fatal("Status().FineMode not ON after C5 press (test precondition)")
	}

	cb(midi.NoteOffVelocity(0, 68, 64), 0) // release: REAL Note Off, not vel==0
	deadline = time.Now().Add(2 * time.Second)
	for eng.Status().FineMode && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if eng.Status().FineMode {
		t.Error("Status().FineMode still ON after C5 real Note Off release, want OFF -- the GetNoteEnd fix must recognize a real 0x8n Note Off exactly like a vel==0 note-on")
	}
}

// TestNoteOnChord_HeldPlusTarget_FiresChordBinding is the required chord
// itself: Cmd held, then Target pressed, must resolve col-cmd's binding
// (param=contrast) rather than col's own (param=offset).
func TestNoteOnChord_HeldPlusTarget_FiresChordBinding(t *testing.T) {
	stubStateFile(t)
	sockPath, received := startRecordingSidecar(t, "color")
	eng := newEngineWithConfig(t, chordDeviceMap, chordBindings)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 50, 100), 0) // Cmd press (held)
	cb(midi.NoteOn(0, 51, 100), 0) // Target press, WHILE held

	waitForRecs(t, received, 1)
	if p := received()[0]["param"]; p != "contrast" {
		t.Fatalf("param = %v, want contrast (the col-cmd chord binding must fire)", p)
	}
}

// TestNoteOnChord_TargetAloneKeepsNormalBinding proves "C2 alone keeps its
// normal binding" -- pressing Target WITHOUT Cmd held must resolve col's
// own binding, completely unaffected by the chord's existence.
func TestNoteOnChord_TargetAloneKeepsNormalBinding(t *testing.T) {
	stubStateFile(t)
	sockPath, received := startRecordingSidecar(t, "color")
	eng := newEngineWithConfig(t, chordDeviceMap, chordBindings)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 51, 100), 0) // Target press, Cmd never held

	waitForRecs(t, received, 1)
	if p := received()[0]["param"]; p != "offset" {
		t.Fatalf("param = %v, want offset (Target's normal col binding, unaffected)", p)
	}
}

// TestNoteOnChord_HeldButControlHasNoChord_FallsThrough proves a held
// overlay never strands a control the overlay doesn't redefine -- Other has
// no col-cmd entry, so it must still fire its normal col binding even while
// Cmd is held.
func TestNoteOnChord_HeldButControlHasNoChord_FallsThrough(t *testing.T) {
	stubStateFile(t)
	sockPath, received := startRecordingSidecar(t, "color")
	eng := newEngineWithConfig(t, chordDeviceMap, chordBindings)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 50, 100), 0) // Cmd press (held)
	cb(midi.NoteOn(0, 52, 100), 0) // Other press, WHILE held -- no col-cmd entry

	waitForRecs(t, received, 1)
	if p := received()[0]["param"]; p != "gain" {
		t.Fatalf("param = %v, want gain (Other's normal col binding -- a held overlay must not strand an un-charted control)", p)
	}
}

// TestNoteOnFineModeHold_StillBehavesExactlyAsBefore is the regression test
// for the vel==0 block's SECOND widening (fineModeHold plus modifierHold
// now, previously fineModeHold plus the reverted modifier type): fineModeHold
// press/release must be byte-identical to its pre-Part-B behaviour.
func TestNoteOnFineModeHold_StillBehavesExactlyAsBefore(t *testing.T) {
	stubStateFile(t)
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"68":"C5"}}`
	bindings := `{
		"col":  {"C5": {"type": "fineModeHold"}},
		"edit": {}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)
	eng.sockPath = startFakeSidecar(t)
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 68, 100), 0) // press: fine mode ON
	deadline := time.Now().Add(2 * time.Second)
	for !eng.Status().FineMode && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !eng.Status().FineMode {
		t.Fatal("Status().FineMode not ON after C5 press (test precondition)")
	}

	cb(midi.NoteOn(0, 68, 0), 0) // release: fine mode OFF
	deadline = time.Now().Add(2 * time.Second)
	for eng.Status().FineMode && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if eng.Status().FineMode {
		t.Error("Status().FineMode still ON after C5 release, want OFF -- the vel==0 widening must not have broken fineModeHold")
	}
}

// TestStatusHeldOverlay_ReflectsPressAndRelease confirms Status().HeldOverlay
// (the HUD's data source, via app/format.go) tracks the overlay across the
// drain loop's own ~50ms tick, not just the synchronous getHeldOverlay()
// accessor.
func TestStatusHeldOverlay_ReflectsPressAndRelease(t *testing.T) {
	stubStateFile(t)
	eng := newEngineWithConfig(t, chordDeviceMap, chordBindings)
	eng.sockPath = startFakeSidecar(t)
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 50, 100), 0) // press

	deadline := time.Now().Add(2 * time.Second)
	for eng.Status().HeldOverlay != "cmd" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.Status().HeldOverlay; got != "cmd" {
		t.Fatalf("Status().HeldOverlay = %q, want cmd", got)
	}

	cb(midi.NoteOn(0, 50, 0), 0) // release

	deadline = time.Now().Add(2 * time.Second)
	for eng.Status().HeldOverlay != "" && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.Status().HeldOverlay; got != "" {
		t.Fatalf("Status().HeldOverlay after release = %q, want \"\"", got)
	}
}

// ---------------------------------------------------------------------------
// Non-persistence: the held overlay must NEVER reach the sentinel file
// ---------------------------------------------------------------------------

// TestSentinelStateNeverPersistsHeldOverlay is Part B's explicit
// non-persistence guard, called out by the design review: the held-
// modifier overlay is momentary chord state -- if it ever leaked into the
// sentinel, the deck could boot back up believing Cmd is still held.
// sentinelState had no Layer field before this feature (that state is
// likewise never persisted) and must gain no overlay/modifier field
// either. Asserted TWO ways: (1) sentinelState's own field set is
// enumerated via reflection, so an accidental future field addition fails
// this test immediately rather than only when someone thinks to check the
// written JSON by hand; (2) saveSentinel's ACTUAL output is scanned for
// any overlay/modifier-shaped key, catching a hypothetical future bug
// where a field is added under a JSON tag whose Go name this test's
// field-enumeration branch might not obviously flag as suspicious.
func TestSentinelStateNeverPersistsHeldOverlay(t *testing.T) {
	typ := reflect.TypeOf(sentinelState{})
	wantFields := map[string]bool{
		"LastShutdown": true, "LastExportFolder": true, "PMode": true,
	}
	if typ.NumField() != len(wantFields) {
		t.Fatalf("sentinelState has %d fields, want exactly %d (%v) -- a field was added without updating this guard",
			typ.NumField(), len(wantFields), wantFields)
	}
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		if !wantFields[name] {
			t.Errorf("sentinelState has unexpected field %q -- the held-modifier overlay (or any other live/momentary state) must never be persisted", name)
		}
	}

	tmp := t.TempDir() + "/test-sentinel.json"
	origPath := stateFilePath
	stateFilePath = tmp
	defer func() { stateFilePath = origPath }()

	saveSentinel("", "hue")

	b, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("sentinel file not written: %v", err)
	}
	var s sentinelState
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("sentinel JSON invalid: %v", err)
	}
	raw := strings.ToLower(string(b))
	for _, forbidden := range []string{"overlay", "modifier", "heldoverlay", "\"layer\""} {
		if strings.Contains(raw, forbidden) {
			t.Errorf("sentinel JSON contains %q -- must never persist held-modifier/layer state:\n%s", forbidden, string(b))
		}
	}
}
