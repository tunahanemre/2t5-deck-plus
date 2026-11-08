package engine

// Tests for the D1 "jogMinuteSnap" feature: the CURRENT D1 binding,
// superseding "jogSeconds" (see jogseconds_test.go -- that binding's
// machinery is kept, unreachable in production, still unit-tested).
// Each firm detent of the knob snaps the playhead to the next/previous
// whole-minute timecode boundary via the sidecar's jog_minute_snap op --
// unlike jogSeconds' fixed ±N-second OFFSET from wherever the playhead
// started, this always lands on a round minute.
//
// Covers:
//   - dispatchJogMinuteSnap (pure-ish drain-loop handler: sends
//     jog_minute_snap with the correct signed direction value,
//     nil-client-safe)
//   - full Start()/MIDI drain-loop integration: the SAME detent-
//     accumulator debounce as D1's selectClip/jogSeconds bindings --
//     sub-threshold raw CC steps produce NO sidecar call; crossing the
//     configured (or default) detentThreshold fires exactly ONE
//     jog_minute_snap op
//   - default/custom detentThreshold fallbacks
//   - Edit-page-only gating (D1 is unbound on every other bank/page)
//   - jogMinuteSnap never touches Engine.activeTrack (no selectClip-style
//     bookkeeping at all)
//
// All hermetic: no live Resolve, fake unix-socket sidecars only, no
// DECKPLUS_ARMED. The actual whole-minute boundary MATH lives sidecar-
// side (ResolveDriver.jog_minute_snap, covered by
// sidecar/test_edit_page_ops.py) -- these tests only cover the Go
// engine's dispatch/debounce/gating responsibilities.

import (
	"context"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"
)

// ---------------------------------------------------------------------------
// dispatchJogMinuteSnap
// ---------------------------------------------------------------------------

func TestDispatchJogMinuteSnap_PositiveDirection_SendsPositiveDirection(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	dispatchJogMinuteSnap(1, client)

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "jog_minute_snap" {
		t.Fatalf("op = %v, want jog_minute_snap", recs[0]["op"])
	}
	if got := recs[0]["direction"].(float64); got != 1 {
		t.Errorf("direction = %v, want 1", got)
	}
}

func TestDispatchJogMinuteSnap_NegativeDirection_SendsNegativeDirection(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	dispatchJogMinuteSnap(-1, client)

	waitForRecs(t, received, 1)
	recs := received()
	if got := recs[0]["direction"].(float64); got != -1 {
		t.Errorf("direction = %v, want -1", got)
	}
}

func TestDispatchJogMinuteSnap_NilClient_NoPanicNoOp(t *testing.T) {
	got := dispatchJogMinuteSnap(1, nil)
	if got != "" {
		t.Errorf("LastAction = %q, want empty (no-sidecar SKIP)", got)
	}
}

func TestDispatchJogMinuteSnap_ReturnsHumanReadableLastAction(t *testing.T) {
	sockPath, _ := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	got := dispatchJogMinuteSnap(1, client)
	if got != "D1 snap +1min" {
		t.Errorf("LastAction = %q, want %q", got, "D1 snap +1min")
	}

	got = dispatchJogMinuteSnap(-1, client)
	if got != "D1 snap -1min" {
		t.Errorf("LastAction = %q, want %q", got, "D1 snap -1min")
	}
}

// ---------------------------------------------------------------------------
// Full Start()/MIDI drain-loop integration
// ---------------------------------------------------------------------------

func TestDrainLoop_D1_JogMinuteSnap_SubThresholdSteps_NoOp(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "jogMinuteSnap", "detentThreshold": 3}}
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
	// Two raw steps, each in its OWN drain tick (threshold=3) -- must NOT
	// be enough to fire a jog_minute_snap op yet.
	cb(midi.ControlChange(0, 41, 1), 0)
	time.Sleep(80 * time.Millisecond)
	cb(midi.ControlChange(0, 41, 1), 0)
	time.Sleep(80 * time.Millisecond)

	for _, r := range received() {
		if r["op"] == "jog_minute_snap" {
			t.Fatalf("jog_minute_snap fired after only 2 sub-threshold steps, want none yet: %v", received())
		}
	}
}

func TestDrainLoop_D1_JogMinuteSnap_ThresholdCrossing_FiresOneSnapForward(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "jogMinuteSnap", "detentThreshold": 3}}
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
	cb(midi.ControlChange(0, 41, 1), 0)
	time.Sleep(80 * time.Millisecond)
	cb(midi.ControlChange(0, 41, 1), 0)
	time.Sleep(80 * time.Millisecond)
	cb(midi.ControlChange(0, 41, 1), 0) // 3rd step crosses threshold=3

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "jog_minute_snap" {
		t.Fatalf("op = %v, want jog_minute_snap", recs[0]["op"])
	}
	if got := recs[0]["direction"].(float64); got != 1 {
		t.Errorf("direction = %v, want 1 (forward direction)", got)
	}

	// Exactly ONE op fired for the 3-step burst -- no multi-snap, and the
	// accumulator resets rather than immediately re-firing.
	time.Sleep(120 * time.Millisecond)
	if got := len(received()); got != 1 {
		t.Errorf("recorded op count = %d, want exactly 1 (no multi-snap, no spurious refire)", got)
	}
}

func TestDrainLoop_D1_JogMinuteSnap_NegativeDirection_FiresNegativeDirection(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "jogMinuteSnap", "detentThreshold": 1}}
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
	cb(midi.ControlChange(0, 41, 127), 0) // D1 down: relative-CC negative step

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "jog_minute_snap" {
		t.Fatalf("op = %v, want jog_minute_snap", recs[0]["op"])
	}
	if got := recs[0]["direction"].(float64); got != -1 {
		t.Errorf("direction = %v, want -1 (backward direction)", got)
	}
}

func TestDrainLoop_D1_JogMinuteSnap_DefaultDetentThresholdAppliesWhenBindingOmitsIt(t *testing.T) {
	// No "detentThreshold" in the binding at all -- must fall back to
	// defaultSelectClipDetentThreshold (4, shared with the
	// selectClip/jogSeconds bindings), not fire on every raw step.
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "jogMinuteSnap"}}
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
	for i := 0; i < defaultSelectClipDetentThreshold-1; i++ {
		cb(midi.ControlChange(0, 41, 1), 0)
		time.Sleep(80 * time.Millisecond)
	}
	for _, r := range received() {
		if r["op"] == "jog_minute_snap" {
			t.Fatalf("jog_minute_snap fired after %d sub-threshold steps, want none yet: %v", defaultSelectClipDetentThreshold-1, received())
		}
	}

	cb(midi.ControlChange(0, 41, 1), 0)
	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "jog_minute_snap" {
		t.Fatalf("op = %v, want jog_minute_snap after crossing the default threshold", recs[0]["op"])
	}
}

func TestDrainLoop_D1_JogMinuteSnap_CustomDetentThresholdFromBinding(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "jogMinuteSnap", "detentThreshold": 2}}
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
	cb(midi.ControlChange(0, 41, 1), 0)
	time.Sleep(80 * time.Millisecond)
	for _, r := range received() {
		if r["op"] == "jog_minute_snap" {
			t.Fatalf("jog_minute_snap fired after only 1 step, want threshold=2: %v", received())
		}
	}
	cb(midi.ControlChange(0, 41, 1), 0)

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "jog_minute_snap" {
		t.Fatalf("op = %v, want jog_minute_snap after crossing custom threshold=2", recs[0]["op"])
	}
}

// TestDrainLoop_D1_JogMinuteSnap_OnlyFiresOnEditPage proves D1's
// jogMinuteSnap binding is Edit-page only -- the "col" bank has no D1
// binding at all (same as the real shipped config/bindings.json), so
// turning D1 while any other page is active must produce zero
// jog_minute_snap ops.
func TestDrainLoop_D1_JogMinuteSnap_OnlyFiresOnEditPage(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "jogMinuteSnap", "detentThreshold": 1}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	sockPath, received := startRecordingSidecar(t, "color")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.ControlChange(0, 41, 1), 0)
	time.Sleep(150 * time.Millisecond)

	for _, r := range received() {
		if r["op"] == "jog_minute_snap" {
			t.Fatalf("jog_minute_snap fired on a non-Edit page (D1 has no col-bank binding), want none: %v", received())
		}
	}
}

// TestDrainLoop_D1_JogMinuteSnap_DoesNotTouchActiveTrack proves
// jogMinuteSnap is fully isolated from the (unreachable-in-production)
// selectClip machinery on the SAME D1 knob: repeated turns past several
// detents must never change Engine.activeTrack, unlike a selectClip
// binding would.
func TestDrainLoop_D1_JogMinuteSnap_DoesNotTouchActiveTrack(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "jogMinuteSnap", "detentThreshold": 1}}
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
	for i := 0; i < 3; i++ {
		cb(midi.ControlChange(0, 41, 1), 0)
		time.Sleep(80 * time.Millisecond)
	}
	waitForRecs(t, received, 3)

	if got := eng.getActiveTrack(); got != 0 {
		t.Errorf("activeTrack = %d, want 0 (jogMinuteSnap must never touch D1's OLD selectClip activeTrack state)", got)
	}
}
