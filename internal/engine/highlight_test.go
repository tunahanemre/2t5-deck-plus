package engine

// Tests for the D1 "active-clip highlight" feature -- tinting the
// D1-selected clip's color in Resolve (via the sidecar's highlight_clip/
// clear_highlight/restore_highlight ops) so the user can SEE which stacked
// layer is active, since Resolve has no clip-selection-highlight API:
//   - dispatchHighlightClip (the drain loop's D1-rotation-changed handler)
//   - full Start()/MIDI drain-loop integration: D1 rotation sends
//     highlight_clip with the configured color, gated by
//     _config.highlightEnabled
//   - Engine.Start() sends restore_highlight once, unconditionally
//     (crash-safety, regardless of the CURRENT highlightEnabled setting)
//   - Engine.Stop() sends clear_highlight
//
// All hermetic: no live Resolve, fake unix-socket sidecars only, no
// DECKPLUS_ARMED. The fake sidecars in this file never call a real
// SetClipColor/GetClipColor -- they only record the JSON op and reply
// {"ok": true}, exactly like startRecordingSidecar/startClipsSidecar
// already do for every other op in this package's other test files.
//
// Every D1 binding below sets "detentThreshold": 1, restoring the
// original "fire on every raw CC step" behavior (see selectClipDetent's
// doc comment in engine.go) -- these tests are about highlight/restore
// behavior, not the sensitivity curve itself, and each turn simulates
// exactly one raw MIDI CC step.

import (
	"context"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"
)

// ---------------------------------------------------------------------------
// dispatchHighlightClip (unit-level, direct call)
// ---------------------------------------------------------------------------

func TestDispatchHighlightClip_SendsOpWithTrackAndColor(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	dispatchHighlightClip(client, 3, "Teal")

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "highlight_clip" {
		t.Fatalf("op = %v, want highlight_clip", recs[0]["op"])
	}
	if got := recs[0]["track"].(float64); got != 3 {
		t.Errorf("track = %v, want 3", got)
	}
	if recs[0]["color"] != "Teal" {
		t.Errorf("color = %v, want Teal", recs[0]["color"])
	}
}

func TestDispatchHighlightClip_NilClient_NoOp(t *testing.T) {
	// Must not panic -- same defensive posture as dispatchSelectClip's
	// nil-client guard.
	dispatchHighlightClip(nil, 3, "Teal")
}

func TestDispatchHighlightClip_ZeroTrack_NoOp(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	dispatchHighlightClip(client, 0, "Teal")

	time.Sleep(100 * time.Millisecond)
	if len(received()) != 0 {
		t.Errorf("track=0 should never call the sidecar, got %v", received())
	}
}

// ---------------------------------------------------------------------------
// Full Start()/MIDI drain-loop integration: D1 rotation -> highlight_clip
// ---------------------------------------------------------------------------

func TestDrainLoop_D1_SelectClip_HighlightEnabledByDefault_SendsHighlightClip(t *testing.T) {
	// No "_config" block at all -- HighlightEnabled must default to true
	// and HighlightColor to "Teal" (see config.TestTopLevelDefaults).
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "selectClip", "detentThreshold": 1}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	clips := []any{
		map[string]any{"track": 1, "name": "A.mov"},
		map[string]any{"track": 2, "name": "B.mov"},
	}
	sockPath, received := startClipsSidecar(t, "edit", clips)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.ControlChange(0, 41, 1), 0) // D1 up: unset -> track 1

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 1 {
		t.Fatalf("activeTrack = %d, want 1 (test precondition)", got)
	}

	var found map[string]any
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range received() {
			if r["op"] == "highlight_clip" {
				found = r
				break
			}
		}
		if found != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if found == nil {
		t.Fatalf("no highlight_clip op sent, received=%v", received())
	}
	if got := found["track"].(float64); got != 1 {
		t.Errorf("highlight_clip track = %v, want 1", got)
	}
	if found["color"] != "Teal" {
		t.Errorf("highlight_clip color = %v, want Teal (default)", found["color"])
	}
}

func TestDrainLoop_D1_SelectClip_HighlightDisabled_NoHighlightClipSent(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"_config": {"highlightEnabled": false},
		"col": {},
		"edit": {"D1": {"type": "selectClip", "detentThreshold": 1}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	clips := []any{
		map[string]any{"track": 1, "name": "A.mov"},
	}
	sockPath, received := startClipsSidecar(t, "edit", clips)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.ControlChange(0, 41, 1), 0) // D1 up: unset -> track 1

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 1 {
		t.Fatalf("activeTrack = %d, want 1 (D1 selectClip must still work when highlight is disabled)", got)
	}

	// Give any (incorrect) highlight_clip send time to arrive.
	time.Sleep(150 * time.Millisecond)
	for _, r := range received() {
		if r["op"] == "highlight_clip" {
			t.Errorf("highlight_clip must never be sent when highlightEnabled=false, got %v", r)
		}
	}
}

func TestDrainLoop_D1_SelectClip_HighlightColorOverride_UsesConfiguredColor(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"_config": {"highlightColor": "Pink"},
		"col": {},
		"edit": {"D1": {"type": "selectClip", "detentThreshold": 1}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	clips := []any{
		map[string]any{"track": 1, "name": "A.mov"},
	}
	sockPath, received := startClipsSidecar(t, "edit", clips)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.ControlChange(0, 41, 1), 0)

	var found map[string]any
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, r := range received() {
			if r["op"] == "highlight_clip" {
				found = r
				break
			}
		}
		if found != nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if found == nil {
		t.Fatalf("no highlight_clip op sent, received=%v", received())
	}
	if found["color"] != "Pink" {
		t.Errorf("highlight_clip color = %v, want Pink (configured override)", found["color"])
	}
}

// ---------------------------------------------------------------------------
// Engine.Start() -- unconditional restore_highlight
// ---------------------------------------------------------------------------

func TestEngineStart_SendsRestoreHighlightOp(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{"col": {}, "edit": {"D1": {"type": "selectClip", "detentThreshold": 1}}}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startClipsSidecar(t, "edit", []any{})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	deadline := time.Now().Add(2 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		for _, r := range received() {
			if r["op"] == "restore_highlight" {
				found = true
				break
			}
		}
		if found {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !found {
		t.Fatalf("Start() never sent restore_highlight, received=%v", received())
	}
}

func TestEngineStart_SendsRestoreHighlightOp_EvenWhenHighlightDisabled(t *testing.T) {
	// The crash-safety restore must run regardless of the CURRENT
	// highlightEnabled setting -- a stray tint could be left over from a
	// PRIOR run where it was enabled.
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{"_config": {"highlightEnabled": false}, "col": {}, "edit": {"D1": {"type": "selectClip", "detentThreshold": 1}}}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startClipsSidecar(t, "edit", []any{})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	deadline := time.Now().Add(2 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		for _, r := range received() {
			if r["op"] == "restore_highlight" {
				found = true
				break
			}
		}
		if found {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !found {
		t.Fatalf("Start() never sent restore_highlight even though highlightEnabled=false, received=%v", received())
	}
}

// ---------------------------------------------------------------------------
// Engine.Stop() -- clear_highlight
// ---------------------------------------------------------------------------

func TestEngineStop_SendsClearHighlightOp(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{"col": {}, "edit": {"D1": {"type": "selectClip", "detentThreshold": 1}}}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startClipsSidecar(t, "edit", []any{})
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := eng.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	var found bool
	for _, r := range received() {
		if r["op"] == "clear_highlight" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("Stop() never sent clear_highlight, received=%v", received())
	}
}

func TestEngineStop_NeverStarted_NoPanic(t *testing.T) {
	eng := newTestEngine(t)
	if err := eng.Stop(); err != nil {
		t.Fatalf("Stop() on never-started engine: %v", err)
	}
}
