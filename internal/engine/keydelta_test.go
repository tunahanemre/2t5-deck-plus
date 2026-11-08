package engine

// Tests for "keyDelta" (2026-08-13): knob rotation -> repeated keystroke,
// first use Shift+D1 -> Resolve timeline zoom. Two independent pieces:
// (a) the overlay-aware resolveTurnBinding change that makes Shift+D1
// resolvable at all, and (b) the keyDelta binding type itself
// (keyDeltaDetent's multi-fire accumulator + dispatchKeyDelta's gate).

import (
	"context"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"

	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/macapp"
)

// ---------------------------------------------------------------------------
// resolveTurnBinding overlay-awareness -- the SAME {page}-{mod} then
// col-{mod} precedence editBankBinding already proved for note_on/buttons,
// now extended to control_change/turns.
// ---------------------------------------------------------------------------

// TestResolveTurnBinding_OverlayFirst proves a held modifier's chord bank
// is tried BEFORE the normal layer/page cascade -- the turn-side mirror of
// TestRealConfig_AllFourModifierChords_C1Resolves (note_on side).
func TestResolveTurnBinding_OverlayFirst(t *testing.T) {
	cfg := loadConfig(t, `{
	  "edit":       {"D1": {"type":"jogMinuteSnap"}},
	  "edit-shift": {"D1": {"type":"keyDelta","keyCW":"Cmd+=","keyCCW":"Cmd+-"}}
	}`)
	b, bank, ok := resolveTurnBinding(cfg, "edit", 1, "D1", "shift")
	if !ok || bank != "edit-shift" || !b.IsKeyDelta() {
		t.Errorf("resolveTurnBinding(edit,1,D1,shift) = bank=%q keyDelta=%v ok=%v, want edit-shift/true/true", bank, b.IsKeyDelta(), ok)
	}
}

// TestResolveTurnBinding_NoOverlayHeld_FallsThroughToNormalCascade proves
// mod=="" is byte-identical to the pre-2026-08-13 behaviour (no overlay
// rung consulted at all) -- the "unmodified" regression guard, mirroring
// TestRealConfig_C1_Unmodified_PostsSpace_EveryPage on the note_on side.
func TestResolveTurnBinding_NoOverlayHeld_FallsThroughToNormalCascade(t *testing.T) {
	cfg := loadConfig(t, `{
	  "edit":       {"D1": {"type":"jogMinuteSnap"}},
	  "edit-shift": {"D1": {"type":"keyDelta","keyCW":"Cmd+=","keyCCW":"Cmd+-"}}
	}`)
	b, bank, ok := resolveTurnBinding(cfg, "edit", 1, "D1", "")
	if !ok || bank != "edit" || !b.IsJogMinuteSnap() {
		t.Errorf("resolveTurnBinding(edit,1,D1,\"\") = bank=%q jogMinuteSnap=%v ok=%v, want edit/true/true (unmodified D1 unaffected)", bank, b.IsJogMinuteSnap(), ok)
	}
}

// TestResolveTurnBinding_OverlayMiss_FallsThrough proves a held modifier
// with NO chord defined for this control falls through to the normal
// cascade rather than resolving to nothing -- the "unfinished overlay
// never leaves a control dead" contract, turn side.
func TestResolveTurnBinding_OverlayMiss_FallsThrough(t *testing.T) {
	cfg := loadConfig(t, `{
	  "edit": {"D1": {"type":"jogMinuteSnap"}}
	}`)
	b, bank, ok := resolveTurnBinding(cfg, "edit", 1, "D1", "shift")
	if !ok || bank != "edit" || !b.IsJogMinuteSnap() {
		t.Errorf("resolveTurnBinding(edit,1,D1,shift) with no edit-shift/col-shift bank = bank=%q ok=%v, want edit/true (fall through to normal cascade)", bank, ok)
	}
}

// TestResolveTurnBinding_ColShiftFallback proves the SAME {page}-{mod}
// then col-{mod} precedence resolveOverlayBinding documents: a control
// with no {page}-{mod} entry but a col-{mod} one still resolves via the
// overlay, not the normal cascade.
func TestResolveTurnBinding_ColShiftFallback(t *testing.T) {
	cfg := loadConfig(t, `{
	  "col":       {"P1": {"type":"balance","hue":0}},
	  "col-shift": {"P1": {"type":"keyDelta","keyCW":"Right","keyCCW":"Left"}}
	}`)
	b, bank, ok := resolveTurnBinding(cfg, "color", 1, "P1", "shift")
	if !ok || bank != "col-shift" || !b.IsKeyDelta() {
		t.Errorf("resolveTurnBinding(color,1,P1,shift) = bank=%q keyDelta=%v ok=%v, want col-shift/true/true", bank, b.IsKeyDelta(), ok)
	}
}

// ---------------------------------------------------------------------------
// keyDeltaDetent -- the multi-fire-per-tick accumulator.
// ---------------------------------------------------------------------------

// TestKeyDeltaDetent_SubThreshold_NoFire proves a step below threshold
// accumulates without firing.
func TestKeyDeltaDetent_SubThreshold_NoFire(t *testing.T) {
	var d keyDeltaDetent
	if got := d.add(2, 4); got != 0 {
		t.Errorf("add(2,4) = %d, want 0 (below threshold)", got)
	}
}

// TestKeyDeltaDetent_ExactlyOneDetent proves crossing the threshold once
// fires exactly one detent.
func TestKeyDeltaDetent_ExactlyOneDetent(t *testing.T) {
	var d keyDeltaDetent
	if got := d.add(4, 4); got != 1 {
		t.Errorf("add(4,4) = %d, want 1", got)
	}
}

// TestKeyDeltaDetent_FastSpin_FiresMultipleDetentsInOneCall is the
// property that DISTINGUISHES keyDeltaDetent from selectClipDetent: a
// single call whose accumulated step crosses the threshold SEVERAL times
// returns a count > 1, not clamped to ±1 -- see keyDeltaDetent's doc
// comment for why a keyDelta binding wants this (a lightweight keystroke)
// where D1's other turn bindings deliberately do not (a "big" action).
func TestKeyDeltaDetent_FastSpin_FiresMultipleDetentsInOneCall(t *testing.T) {
	var d keyDeltaDetent
	if got := d.add(17, 4); got != 4 {
		t.Errorf("add(17,4) = %d, want 4 (17/4 = 4 whole detents, remainder 1 carried)", got)
	}
	// The remainder (1) must still be there for the NEXT call.
	if got := d.add(3, 4); got != 1 {
		t.Errorf("add(3,4) after carrying remainder 1 = %d, want 1 (1+3=4, one more detent)", got)
	}
}

// TestKeyDeltaDetent_NegativeDirection mirrors the positive-direction
// tests for counter-clockwise spins.
func TestKeyDeltaDetent_NegativeDirection(t *testing.T) {
	var d keyDeltaDetent
	if got := d.add(-9, 4); got != -2 {
		t.Errorf("add(-9,4) = %d, want -2 (9/4 = 2 whole detents, remainder -1 carried)", got)
	}
}

// TestKeyDeltaDetent_ThresholdZeroOrLess_TreatedAsOne mirrors
// selectClipDetent's convention: threshold<=0 fires on every nonzero step.
func TestKeyDeltaDetent_ThresholdZeroOrLess_TreatedAsOne(t *testing.T) {
	var d keyDeltaDetent
	if got := d.add(3, 0); got != 3 {
		t.Errorf("add(3,0) = %d, want 3 (threshold<=0 treated as 1, fires on every step)", got)
	}
}

// TestKeyDeltaDetent_PerControlIndependence proves two SEPARATE
// keyDeltaDetent instances (as keyDeltaAccum's map produces, one per
// control name) never share state -- pins the map-of-instances design
// choice documented on keyDeltaAccum in Start().
func TestKeyDeltaDetent_PerControlIndependence(t *testing.T) {
	var d1, d2 keyDeltaDetent
	d1.add(3, 4)
	if got := d2.add(3, 4); got != 0 {
		t.Errorf("a fresh keyDeltaDetent saw a nonzero accumulation from a DIFFERENT instance -- state leaked, got %d want 0", got)
	}
}

// ---------------------------------------------------------------------------
// keyDeltaKeyAndCount -- the pure sign/count/empty-key logic, extracted
// from dispatchKeyDelta specifically so it is testable independent of the
// frontmost gate (see its own doc comment).
// ---------------------------------------------------------------------------

func TestKeyDeltaKeyAndCount_Clockwise(t *testing.T) {
	key, n, ok := keyDeltaKeyAndCount(3, "Cmd+=", "Cmd+-")
	if !ok || key != "Cmd+=" || n != 3 {
		t.Errorf("keyDeltaKeyAndCount(3,...) = key=%q n=%d ok=%v, want Cmd+=/3/true", key, n, ok)
	}
}

func TestKeyDeltaKeyAndCount_CounterClockwise(t *testing.T) {
	key, n, ok := keyDeltaKeyAndCount(-2, "Cmd+=", "Cmd+-")
	if !ok || key != "Cmd+-" || n != 2 {
		t.Errorf("keyDeltaKeyAndCount(-2,...) = key=%q n=%d ok=%v, want Cmd+-/2/true", key, n, ok)
	}
}

func TestKeyDeltaKeyAndCount_ZeroCount_NotOK(t *testing.T) {
	_, _, ok := keyDeltaKeyAndCount(0, "Cmd+=", "Cmd+-")
	if ok {
		t.Error("keyDeltaKeyAndCount(0,...) ok = true, want false")
	}
}

func TestKeyDeltaKeyAndCount_EmptyKeySpecForFiredDirection_NotOK(t *testing.T) {
	// KeyCCW unset -- a spin in the CCW direction must be a no-op, not a
	// panic or a bare "".
	_, _, ok := keyDeltaKeyAndCount(-1, "Cmd+=", "")
	if ok {
		t.Error("keyDeltaKeyAndCount(-1, Cmd+=, \"\") ok = true, want false (empty KeyCCW)")
	}
	// The OTHER direction, with its own key set, must be unaffected.
	key, n, ok := keyDeltaKeyAndCount(1, "Cmd+=", "")
	if !ok || key != "Cmd+=" || n != 1 {
		t.Errorf("keyDeltaKeyAndCount(1, Cmd+=, \"\") = key=%q n=%d ok=%v, want Cmd+=/1/true", key, n, ok)
	}
}

// ---------------------------------------------------------------------------
// dispatchKeyDelta -- the gated dispatch function itself. Only the SKIP
// paths are observable under `go test` (macapp.BundleIDIsResolve() always
// false, same limitation dispatchNoteOn's IsKey() case has -- see
// overlay_intercepts_test.go's comment on this exact issue). The
// posting-N-keystrokes behavior is covered instead by
// TestKeyDeltaKeyAndCount_* above (the pure core dispatchKeyDelta calls
// before the gate) and keyDeltaDetent's own tests (how many detents fire
// per tick) -- composing those two proves the end-to-end count/key
// selection without needing to fight the frontmost gate.
// ---------------------------------------------------------------------------

// TestDispatchKeyDelta_ZeroCount_Noop proves count==0 short-circuits
// before the frontmost gate is even reached.
func TestDispatchKeyDelta_ZeroCount_Noop(t *testing.T) {
	if got := dispatchKeyDelta("D1", 0, "Cmd+=", "Cmd+-"); got != "" {
		t.Errorf("dispatchKeyDelta(D1,0,...) = %q, want \"\"", got)
	}
}

// TestDispatchKeyDelta_NotFrontmost_Skips proves the SAME frontmost gate a
// "key" (button) binding uses is applied here too. macapp.FrontmostBundleID
// queries the REAL macOS frontmost-app state, not a stub -- this
// UNDER-TEST assumption ("Resolve is never frontmost") is repeated as fact
// throughout this package's other test files, but is only true when the
// terminal running `go test` itself has OS focus at that instant; on a
// machine where Resolve is genuinely the frontmost app (e.g. the user
// actively using it, as several sessions' dispatch prompts explicitly
// require), gate.Allow's dependency evaluates true and this SKIP path
// cannot be exercised at all -- skip rather than report a false failure
// for a live-environment condition, not a logic defect.
func TestDispatchKeyDelta_NotFrontmost_Skips(t *testing.T) {
	if macapp.BundleIDIsResolve(macapp.FrontmostBundleID()) {
		t.Skip("Resolve is the real frontmost app on this machine right now -- the not-frontmost SKIP path cannot be exercised in this environment")
	}
	if got := dispatchKeyDelta("D1", 3, "Cmd+=", "Cmd+-"); got != "" {
		t.Errorf("dispatchKeyDelta(D1,3,...) with Resolve not frontmost = %q, want \"\" (SKIP)", got)
	}
}

// TestDispatchKeyDelta_EmptyKeySpec_Noop proves an empty key spec for the
// fired direction short-circuits before the frontmost gate too (via
// keyDeltaKeyAndCount).
func TestDispatchKeyDelta_EmptyKeySpec_Noop(t *testing.T) {
	if got := dispatchKeyDelta("D1", -1, "Cmd+=", ""); got != "" {
		t.Errorf("dispatchKeyDelta(D1,-1,Cmd+=,\"\") = %q, want \"\"", got)
	}
}

// ---------------------------------------------------------------------------
// Full Start()/MIDI drain-loop integration -- proves tickMod is ACTUALLY
// wired into the real drain loop's resolveTurnBinding call sites (not just
// correct as a pure function in isolation above). Uses "property" as an
// OBSERVABLE PROXY type for the edit-shift.D1 binding, same established
// technique jogminutesnap_test.go/editpage_test.go etc. use elsewhere in
// this package for anything that would otherwise dispatch a CGEvent
// keystroke -- "keyDelta" itself posts via keys.Send, gated on
// macapp.BundleIDIsResolve() (always false under `go test`, see
// dispatchKeyDelta's own test section above), so its ACTUAL dispatch is
// unobservable through the full MIDI-callback path; resolution to the
// correct BANK is what this test proves, via the sidecar op the resolved
// binding's type produces.
// ---------------------------------------------------------------------------

// TestDrainLoop_ShiftD1_ResolvesViaOverlayBank_NotPlainEditD1 holds Shift,
// turns D1 past threshold, and confirms the drain loop dispatched via
// edit-shift's binding (a property bump_property op) rather than plain
// edit.D1's jogMinuteSnap (a jog_minute_snap op) -- proving
// resolveTurnBinding's overlay rung is live in Start(), not just correct
// in the pure-function tests above.
func TestDrainLoop_ShiftD1_ResolvesViaOverlayBank_NotPlainEditD1(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{"62":"Shift"}}`
	bindings := `{
		"_config": {"keyDebounceMs": 1},
		"col": {"Shift": {"type": "modifierHold", "modifier": "shift"}},
		"edit": {"D1": {"type": "jogMinuteSnap", "detentThreshold": 1}},
		"edit-shift": {"D1": {"type": "property", "property": "Pan", "sens": 1, "detentThreshold": 1}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 62, 100), 0) // hold Shift (overlay only, zero CGEvents)
	time.Sleep(20 * time.Millisecond)
	cb(midi.ControlChange(0, 41, 1), 0) // turn D1 one detent WHILE Shift is held

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "bump_property" {
		t.Fatalf("op = %v, want bump_property (edit-shift.D1, the overlay bank) -- got jog_minute_snap would mean the overlay rung was never consulted", recs[0]["op"])
	}
	if recs[0]["property"] != "Pan" {
		t.Errorf("property = %v, want Pan", recs[0]["property"])
	}
}

// TestDrainLoop_D1_NoShiftHeld_StillResolvesPlainEditD1 is the paired
// regression guard: with Shift NEVER pressed, D1 must resolve exactly as
// before (jogMinuteSnap), proving the SAME Engine/config that now supports
// the Shift+D1 overlay leaves the unmodified control untouched.
func TestDrainLoop_D1_NoShiftHeld_StillResolvesPlainEditD1(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{"62":"Shift"}}`
	bindings := `{
		"_config": {"keyDebounceMs": 1},
		"col": {"Shift": {"type": "modifierHold", "modifier": "shift"}},
		"edit": {"D1": {"type": "jogMinuteSnap", "detentThreshold": 1}},
		"edit-shift": {"D1": {"type": "property", "property": "Pan", "sens": 1, "detentThreshold": 1}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.ControlChange(0, 41, 1), 0) // turn D1, Shift never touched

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "jog_minute_snap" {
		t.Fatalf("op = %v, want jog_minute_snap (plain edit.D1, unmodified)", recs[0]["op"])
	}
}

// TestKeyDeltaBinding_ConfigResolvesAndDispatches is a small end-to-end
// sanity check tying config.Binding.IsKeyDelta()/KeyCW/KeyCCW to
// dispatchKeyDelta's own signature, without duplicating the config
// package's own parsing tests.
func TestKeyDeltaBinding_ConfigResolvesAndDispatches(t *testing.T) {
	b := config.Binding{Type: "keyDelta", KeyCW: "Cmd+=", KeyCCW: "Cmd+-"}
	if !b.IsKeyDelta() {
		t.Fatal("binding should classify as keyDelta")
	}
	// count==0 (nothing fired yet this tick) must still be a clean no-op
	// when routed through the real field names.
	if got := dispatchKeyDelta("D1", 0, b.KeyCW, b.KeyCCW); got != "" {
		t.Errorf("dispatchKeyDelta with config fields, count=0 = %q, want \"\"", got)
	}
}
