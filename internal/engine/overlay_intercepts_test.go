package engine

// Tests for making the direct-intercept family (isSelectClipRelease,
// isSelectClipVert, isSelectClipHorizControl, isSelectClipClear,
// isAddMarkerControl, isJogPressControl, isMarkerColorControl,
// isMarkerAtPlayheadControl, all via the shared editBankBinding) overlay-
// aware (2026-08-13) -- required so a held-modifier chord over one of these
// eight controls (e.g. Shift+Up/Down/Left/Right -> the existing
// selectClipVert/selectClipHoriz clip selection) is actually reachable.
// Without this, authoring a chord in "edit-{mod}" for one of these eight
// would silently do nothing: they all `return` from Start()'s MIDI
// callback BEFORE the generic "Held-modifier CHORD OVERLAY" block is ever
// reached (see editBankBinding's doc comment).
//
// This is the DECIDING test file for whether the arrow-keys-as-real-
// keystrokes feature (edit.Up/Down/Left/Right -> type "key", with
// edit-shift carrying the moved selectClipVert/selectClipHoriz bindings)
// ships in this pass. Per the explicit instruction: only if every test
// here is GREEN does that config layout ship; otherwise it must be
// reverted to the pre-existing selectClipVert/selectClipHoriz-on-
// edit.Up/Down/Left/Right layout with no edit-shift bank at all.

import (
	"context"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"

	"2t5-deck-plus/internal/keys"
)

// hasNoop reports whether keys.NoopCalls() contains an entry matching
// spec/kind -- e.g. hasNoop(keys.NoopCalls(), "alt", "down"). Previously
// shared from the now-removed passthrough_test.go; this file is its sole
// remaining consumer.
func hasNoop(calls []keys.NoopCall, spec, kind string) bool {
	for _, c := range calls {
		if c.Spec == spec && c.Kind == kind {
			return true
		}
	}
	return false
}

// arrowDeviceMap: Up/Down/Left/Right match the real device-map.json note
// numbers (76/77/78/79); C5 (53) is a SECOND direct-intercept control
// (selectClipClear) used to prove a control with NO edit-shift entry falls
// through to its normal edit binding while Shift is held; Shift (66)
// matches the real device-map.json note.
const arrowDeviceMap = `{"knobs":{},"sliders":{},"buttons":{"76":"Up","77":"Down","78":"Left","79":"Right","53":"C5","66":"Shift"}}`

// arrowBindings mirrors the shipped layout this test file is deciding
// whether to keep: plain arrow keys on "edit", the SAME selectClipVert/
// selectClipHoriz bindings moved onto "edit-shift", C5 (selectClipClear)
// present on "edit" but DELIBERATELY ABSENT from "edit-shift" (the
// fall-through case), and Shift as a modifierHold button.
const arrowBindings = `{
	"_config": {"keyDebounceMs": 1},
	"col": {
		"Shift": {"type": "modifierHold", "modifier": "shift"}
	},
	"edit": {
		"Up":    {"type": "key", "key": "Up"},
		"Down":  {"type": "key", "key": "Down"},
		"Left":  {"type": "key", "key": "Left"},
		"Right": {"type": "key", "key": "Right"},
		"C5":    {"type": "selectClipClear"}
	},
	"edit-shift": {
		"Up":    {"type": "selectClipVert"},
		"Down":  {"type": "selectClipVert"},
		"Left":  {"type": "selectClipHoriz"},
		"Right": {"type": "selectClipHoriz"}
	}
}`

// pressShift presses the Shift deck button (note 66) through the given
// callback, holding it for the rest of the calling test until releaseShift
// is called.
func pressShift(cb func(msg midi.Message, timestampms int32)) {
	cb(midi.NoteOn(0, 66, 100), 0)
}

func releaseShift(cb func(msg midi.Message, timestampms int32)) {
	cb(midi.NoteOffVelocity(0, 66, 64), 0)
}

// ---------------------------------------------------------------------------
// REQUIREMENT 1: arrow alone on the Edit page posts a real arrow keystroke
// and does NOT select a clip.
// ---------------------------------------------------------------------------

func TestOverlayIntercepts_ArrowAlone_EditPage_PostsKeyNotSelection(t *testing.T) {
	stubStateFile(t)
	keys.ResetNoopCalls()
	t.Cleanup(keys.ResetNoopCalls)

	sockPath, received := startRecordingSidecar(t, "edit")
	eng := newEngineWithConfig(t, arrowDeviceMap, arrowBindings)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 76, 100), 0) // Up press, Shift NOT held

	// NOTE: cannot assert a posted "Up" keystroke here via keys.NoopCalls()
	// -- dispatchNoteOn's IsKey() case is gated on
	// macapp.BundleIDIsResolve(macapp.FrontmostBundleID()), which is ALWAYS
	// false under `go test` (no real Resolve process is frontmost on the
	// test-running machine), so a "key" binding's actual dispatch is
	// structurally unobservable here -- the SAME reason every OTHER test in
	// this package that needs to observe a chord/binding's dispatch uses a
	// "cdl" binding instead of "key" (see overlay_test.go's comment on this
	// exact convention). See
	// TestOverlayIntercepts_ArrowAlone_EditPage_ResolvesToNonSelectionBinding_Observable
	// below for the positive proof via a "cdl"-typed fixture. This test's
	// job is the NEGATIVE half: prove no selection fired.
	for _, rec := range received() {
		if rec["op"] == "select_clip_vertical" || rec["op"] == "select_clip_horizontal" {
			t.Errorf("sidecar received %v -- Up alone (no Shift) must NOT select a clip", rec)
		}
	}
}

// TestOverlayIntercepts_ArrowAlone_EditPage_ResolvesToNonSelectionBinding_Observable
// is the POSITIVE half of the "arrow alone -> not a selection" proof: same
// scenario, but edit.Up is a "property" binding instead of "key" --
// specifically so its dispatch is OBSERVABLE. Two independent reasons "key"
// itself cannot be used here: (1) dispatchNoteOn's IsKey() case is gated on
// macapp.BundleIDIsResolve(), always false under `go test`; (2) once past
// the direct-intercept family (which has no type whitelist at all),
// resolution falls to the NORMAL resolveNoteBank/Cascade path, which DOES
// apply noteBankGuard's page-bank whitelist (IsProperty/IsJog/IsKey/
// IsColorByRenderState/IsToggleHudAndColor) -- "cdl" is NOT on that list
// (confirmed by first writing this test with "cdl" and watching it time
// out: the edit-bank binding was silently REJECTED by the guard, falling
// through to a nonexistent col.Up and never dispatching at all), so
// "property" is the only type that is BOTH guard-whitelisted AND
// observable via the sidecar without a frontmost gate.
func TestOverlayIntercepts_ArrowAlone_EditPage_ResolvesToNonSelectionBinding_Observable(t *testing.T) {
	stubStateFile(t)
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"76":"Up","66":"Shift"}}`
	bindings := `{
		"_config": {"keyDebounceMs": 1},
		"col": {
			"Shift": {"type": "modifierHold", "modifier": "shift"}
		},
		"edit": {
			"Up": {"type": "property", "property": "Pan"}
		},
		"edit-shift": {
			"Up": {"type": "selectClipVert"}
		}
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
	cb(midi.NoteOn(0, 76, 100), 0) // Up press, Shift NOT held

	waitForRecs(t, received, 1)
	if op := received()[0]["op"]; op != "reset_property" {
		t.Fatalf("sidecar op = %v, want reset_property (property press default) -- Up alone must resolve edit.Up's plain binding, not select a clip", op)
	}
}

func TestOverlayIntercepts_LeftRightAlone_EditPage_PostsKeyNotSelection(t *testing.T) {
	stubStateFile(t)
	keys.ResetNoopCalls()
	t.Cleanup(keys.ResetNoopCalls)

	sockPath, received := startRecordingSidecar(t, "edit")
	eng := newEngineWithConfig(t, arrowDeviceMap, arrowBindings)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 78, 100), 0) // Left press, Shift NOT held

	// See the Up test's comment above: "key" dispatch is unobservable
	// under `go test` (frontmost gate). This is the negative half; the
	// Up test's _Observable variant already covers the positive half for
	// the (structurally identical) Left/Right code path.
	for _, rec := range received() {
		if rec["op"] == "select_clip_horizontal" {
			t.Errorf("sidecar received %v -- Left alone (no Shift, Edit page) must NOT select a clip", rec)
		}
	}
}

// ---------------------------------------------------------------------------
// REQUIREMENT 2: Shift + arrow selects a clip and does NOT post an arrow
// keystroke.
// ---------------------------------------------------------------------------

func TestOverlayIntercepts_ShiftPlusUp_SelectsClip_NoKeyPosted(t *testing.T) {
	stubStateFile(t)
	keys.ResetNoopCalls()
	t.Cleanup(keys.ResetNoopCalls)

	sockPath, received := startRecordingSidecar(t, "edit")
	eng := newEngineWithConfig(t, arrowDeviceMap, arrowBindings)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	pressShift(cb)
	cb(midi.NoteOn(0, 76, 100), 0) // Up press, WHILE Shift held

	waitForRecs(t, received, 1)
	found := false
	for _, rec := range received() {
		if rec["op"] == "select_clip_vertical" {
			found = true
		}
	}
	if !found {
		t.Errorf("sidecar never received select_clip_vertical -- Shift+Up must select a clip via edit-shift.Up")
	}
	if hasNoop(keys.NoopCalls(), "Up", "down+up") {
		t.Error("Shift+Up posted a plain Up keystroke -- must NOT, the overlay chord must win")
	}
}

func TestOverlayIntercepts_ShiftPlusLeft_SelectsClip_NoKeyPosted(t *testing.T) {
	stubStateFile(t)
	keys.ResetNoopCalls()
	t.Cleanup(keys.ResetNoopCalls)

	sockPath, received := startRecordingSidecar(t, "edit")
	eng := newEngineWithConfig(t, arrowDeviceMap, arrowBindings)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	pressShift(cb)
	cb(midi.NoteOn(0, 78, 100), 0) // Left press, WHILE Shift held

	waitForRecs(t, received, 1)
	found := false
	for _, rec := range received() {
		if rec["op"] == "select_clip_horizontal" {
			found = true
		}
	}
	if !found {
		t.Errorf("sidecar never received select_clip_horizontal -- Shift+Left must select a clip via edit-shift.Left")
	}
	if hasNoop(keys.NoopCalls(), "Left", "down+up") {
		t.Error("Shift+Left posted a plain Left keystroke -- must NOT, the overlay chord must win")
	}
}

// ---------------------------------------------------------------------------
// REQUIREMENT 3: Color-page Left/Right node nav is unchanged, with and
// without Shift held.
// ---------------------------------------------------------------------------

func TestOverlayIntercepts_ColorPage_LeftRight_NodeNav_Unaffected_NoShift(t *testing.T) {
	stubStateFile(t)
	keys.ResetNoopCalls()
	t.Cleanup(keys.ResetNoopCalls)

	sockPath, received := startRecordingSidecar(t, "color")
	eng := newEngineWithConfig(t, arrowDeviceMap, arrowBindings)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 79, 100), 0) // Right press, Color page, no Shift

	waitForRecs(t, received, 1)
	if op := received()[0]["op"]; op != "set_node" {
		t.Fatalf("sidecar op = %v, want set_node (Color-page node nav must fire, unchanged)", op)
	}
	if hasNoop(keys.NoopCalls(), "Right", "down+up") {
		t.Error("Right on the Color page posted a plain Right keystroke -- must stay node-nav only")
	}
}

func TestOverlayIntercepts_ColorPage_LeftRight_NodeNav_Unaffected_WithShiftHeld(t *testing.T) {
	stubStateFile(t)
	keys.ResetNoopCalls()
	t.Cleanup(keys.ResetNoopCalls)

	sockPath, received := startRecordingSidecar(t, "color")
	eng := newEngineWithConfig(t, arrowDeviceMap, arrowBindings)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	pressShift(cb)
	cb(midi.NoteOn(0, 79, 100), 0) // Right press, Color page, WHILE Shift held

	waitForRecs(t, received, 1)
	var sawSelect bool
	for _, rec := range received() {
		if rec["op"] == "select_clip_horizontal" {
			sawSelect = true
		}
	}
	if sawSelect {
		t.Error("Shift+Right on the Color page selected a clip -- selection is an Edit-page-only feature, Color page must stay node-nav regardless of Shift")
	}
	foundSetNode := false
	for _, rec := range received() {
		if rec["op"] == "set_node" {
			foundSetNode = true
		}
	}
	if !foundSetNode {
		t.Error("sidecar never received set_node -- Color-page node nav must still fire even while Shift is held")
	}
}

// ---------------------------------------------------------------------------
// REQUIREMENT 4: a direct-intercept control with no edit-shift entry still
// falls through to its normal edit binding while Shift is held.
// ---------------------------------------------------------------------------

func TestOverlayIntercepts_NoOverlayEntry_FallsThroughToNormalEditBinding(t *testing.T) {
	stubStateFile(t)
	sockPath, received := startRecordingSidecar(t, "edit")
	eng := newEngineWithConfig(t, arrowDeviceMap, arrowBindings)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	pressShift(cb)
	cb(midi.NoteOn(0, 53, 100), 0) // C5 press, WHILE Shift held -- edit-shift has NO C5 entry

	waitForRecs(t, received, 1)
	if op := received()[0]["op"]; op != "clear_highlight" {
		t.Fatalf("sidecar op = %v, want clear_highlight -- C5 (selectClipClear) must fall through to its normal edit binding when the held overlay (shift) defines no chord for it", op)
	}
}

// ---------------------------------------------------------------------------
// REQUIREMENT 5: the P-button marker controls (also direct-intercept) keep
// working unmodified while no overlay is held.
// ---------------------------------------------------------------------------

func TestOverlayIntercepts_MarkerAtPlayhead_Unaffected_NoOverlayHeld(t *testing.T) {
	stubStateFile(t)
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"80":"P1btn"}}`
	bindings := `{
		"_config": {"keyDebounceMs": 1},
		"edit": {
			"P1btn": {"type": "markerAtPlayhead", "markerColor": "Red", "whenPMode": "hue"}
		}
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
	cb(midi.NoteOn(0, 80, 100), 0) // P1btn press, no overlay held at all

	waitForRecs(t, received, 1)
	if op := received()[0]["op"]; op != "marker_at_playhead" {
		t.Fatalf("sidecar op = %v, want marker_at_playhead -- the overlay-awareness change (mod==\"\") must be a complete no-op for the existing P-button marker feature", op)
	}
}

// ---------------------------------------------------------------------------
// Release: Shift release restores the arrow-alone behavior.
// ---------------------------------------------------------------------------

// TestOverlayIntercepts_ShiftReleased_ArrowFallsBackToPlainKey uses the
// SAME "property"-for-observability substitution as the "Alone" test above
// (see its doc comment for why "key" is unobservable AND "cdl" fails
// noteBankGuard's whitelist), and sleeps past keyDebounceMs between the
// two Up presses -- otherwise the second press (same control, "Up")
// arrives within the SAME debounce window as the first and is correctly
// suppressed as a duplicate, an orthogonal, pre-existing behavior
// unrelated to what this test is actually proving (that releasing Shift
// makes Up resolve DIFFERENTLY on its next, non-duplicate press).
func TestOverlayIntercepts_ShiftReleased_ArrowFallsBackToPlainKey(t *testing.T) {
	stubStateFile(t)
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"76":"Up","66":"Shift"}}`
	bindings := `{
		"_config": {"keyDebounceMs": 1},
		"col": {
			"Shift": {"type": "modifierHold", "modifier": "shift"}
		},
		"edit": {
			"Up": {"type": "property", "property": "Pan"}
		},
		"edit-shift": {
			"Up": {"type": "selectClipVert"}
		}
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
	pressShift(cb)
	cb(midi.NoteOn(0, 76, 100), 0) // Up while held -- selection
	releaseShift(cb)
	time.Sleep(10 * time.Millisecond) // clear the 1ms debounce window
	cb(midi.NoteOn(0, 76, 100), 0)    // Up again, Shift released -- plain edit.Up binding

	waitForRecs(t, received, 2)
	recs := received()
	if op := recs[0]["op"]; op != "select_clip_vertical" {
		t.Fatalf("recs[0].op = %v, want select_clip_vertical (the held-Shift press)", op)
	}
	if op := recs[1]["op"]; op != "reset_property" {
		t.Fatalf("recs[1].op = %v, want reset_property (edit.Up's plain property binding, after Shift released)", op)
	}
}
