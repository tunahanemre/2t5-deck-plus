package engine

// Tests for the D1 "jogSeconds" feature: a COARSE playhead jump, the
// coarse counterpart to the ControlDial's fine "jog" binding. D1 was freed
// by the direction-button clip-selection feature (see selectclip_test.go/
// selectclip_sensitivity_test.go's "OLD, unreachable in production"
// selectClip machinery) and is now reused here for an entirely unrelated
// purpose: each firm detent of the knob moves the playhead by
// ±secondsPerStep seconds (default 60) via the sidecar's jog_seconds op.
//
// Covers:
//   - dispatchJogSeconds (pure-ish drain-loop handler: sends jog_seconds
//     with the correct signed seconds value, nil-client-safe)
//   - full Start()/MIDI drain-loop integration: the SAME detent-
//     accumulator debounce as D1's (unreachable) selectClip binding —
//     sub-threshold raw CC steps produce NO sidecar call; crossing the
//     configured (or default) detentThreshold fires exactly ONE
//     jog_seconds op
//   - default/custom secondsPerStep and detentThreshold fallbacks
//   - Edit-page-only gating (D1 is unbound on every other bank/page)
//   - jogSeconds never touches Engine.activeTrack (no selectClip-style
//     bookkeeping at all)
//
// All hermetic: no live Resolve, fake unix-socket sidecars only, no
// DECKPLUS_ARMED.

import (
	"context"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"
)

// ---------------------------------------------------------------------------
// dispatchJogSeconds
// ---------------------------------------------------------------------------

func TestDispatchJogSeconds_PositiveDirection_SendsPositiveSeconds(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	dispatchJogSeconds(1, client, 60)

	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "jog_seconds" {
		t.Fatalf("op = %v, want jog_seconds", recs[0]["op"])
	}
	if got := recs[0]["seconds"].(float64); got != 60 {
		t.Errorf("seconds = %v, want 60", got)
	}
}

func TestDispatchJogSeconds_NegativeDirection_SendsNegativeSeconds(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	dispatchJogSeconds(-1, client, 60)

	waitForRecs(t, received, 1)
	recs := received()
	if got := recs[0]["seconds"].(float64); got != -60 {
		t.Errorf("seconds = %v, want -60", got)
	}
}

func TestDispatchJogSeconds_CustomSecondsPerStep(t *testing.T) {
	sockPath, received := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	dispatchJogSeconds(1, client, 15)

	waitForRecs(t, received, 1)
	recs := received()
	if got := recs[0]["seconds"].(float64); got != 15 {
		t.Errorf("seconds = %v, want 15", got)
	}
}

func TestDispatchJogSeconds_NilClient_NoPanicNoOp(t *testing.T) {
	got := dispatchJogSeconds(1, nil, 60)
	if got != "" {
		t.Errorf("LastAction = %q, want empty (no-sidecar SKIP)", got)
	}
}

func TestDispatchJogSeconds_ReturnsHumanReadableLastAction(t *testing.T) {
	sockPath, _ := startRecordingSidecar(t, "edit")
	client := dialRecordingClient(t, sockPath)

	got := dispatchJogSeconds(1, client, 60)
	if got != "D1 jog +60s" {
		t.Errorf("LastAction = %q, want %q", got, "D1 jog +60s")
	}

	got = dispatchJogSeconds(-1, client, 60)
	if got != "D1 jog -60s" {
		t.Errorf("LastAction = %q, want %q", got, "D1 jog -60s")
	}
}

// ---------------------------------------------------------------------------
// Full Start()/MIDI drain-loop integration
// ---------------------------------------------------------------------------

func TestDrainLoop_D1_JogSeconds_SubThresholdSteps_NoOp(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "jogSeconds", "secondsPerStep": 60, "detentThreshold": 3}}
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
	// be enough to fire a jog_seconds op yet.
	cb(midi.ControlChange(0, 41, 1), 0)
	time.Sleep(80 * time.Millisecond)
	cb(midi.ControlChange(0, 41, 1), 0)
	time.Sleep(80 * time.Millisecond)

	for _, r := range received() {
		if r["op"] == "jog_seconds" {
			t.Fatalf("jog_seconds fired after only 2 sub-threshold steps, want none yet: %v", received())
		}
	}
}

func TestDrainLoop_D1_JogSeconds_ThresholdCrossing_FiresOneJumpForward(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "jogSeconds", "secondsPerStep": 60, "detentThreshold": 3}}
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
	if recs[0]["op"] != "jog_seconds" {
		t.Fatalf("op = %v, want jog_seconds", recs[0]["op"])
	}
	if got := recs[0]["seconds"].(float64); got != 60 {
		t.Errorf("seconds = %v, want 60 (default secondsPerStep, forward direction)", got)
	}

	// Exactly ONE op fired for the 3-step burst -- no multi-jump, and the
	// accumulator resets rather than immediately re-firing.
	time.Sleep(120 * time.Millisecond)
	if got := len(received()); got != 1 {
		t.Errorf("recorded op count = %d, want exactly 1 (no multi-jump, no spurious refire)", got)
	}
}

func TestDrainLoop_D1_JogSeconds_NegativeDirection_FiresNegativeSeconds(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "jogSeconds", "secondsPerStep": 60, "detentThreshold": 1}}
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
	if recs[0]["op"] != "jog_seconds" {
		t.Fatalf("op = %v, want jog_seconds", recs[0]["op"])
	}
	if got := recs[0]["seconds"].(float64); got != -60 {
		t.Errorf("seconds = %v, want -60 (backward direction)", got)
	}
}

func TestDrainLoop_D1_JogSeconds_DefaultSecondsPerStepAppliesWhenBindingOmitsIt(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "jogSeconds", "detentThreshold": 1}}
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

	waitForRecs(t, received, 1)
	recs := received()
	if got := recs[0]["seconds"].(float64); got != defaultJogSecondsSeconds {
		t.Errorf("seconds = %v, want %v (defaultJogSecondsSeconds, binding omitted secondsPerStep)", got, defaultJogSecondsSeconds)
	}
}

func TestDrainLoop_D1_JogSeconds_DefaultDetentThresholdAppliesWhenBindingOmitsIt(t *testing.T) {
	// No "detentThreshold" in the binding at all -- must fall back to
	// defaultSelectClipDetentThreshold (4, shared with the selectClip
	// binding), not fire on every raw step.
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "jogSeconds", "secondsPerStep": 60}}
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
		if r["op"] == "jog_seconds" {
			t.Fatalf("jog_seconds fired after %d sub-threshold steps, want none yet: %v", defaultSelectClipDetentThreshold-1, received())
		}
	}

	cb(midi.ControlChange(0, 41, 1), 0)
	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "jog_seconds" {
		t.Fatalf("op = %v, want jog_seconds after crossing the default threshold", recs[0]["op"])
	}
}

func TestDrainLoop_D1_JogSeconds_CustomSecondsPerStepFromBinding(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "jogSeconds", "secondsPerStep": 15, "detentThreshold": 1}}
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

	waitForRecs(t, received, 1)
	recs := received()
	if got := recs[0]["seconds"].(float64); got != 15 {
		t.Errorf("seconds = %v, want 15 (binding's own secondsPerStep)", got)
	}
}

// TestDrainLoop_D1_JogSeconds_OnlyFiresOnEditPage proves D1's jogSeconds
// binding is Edit-page only -- the "col" bank has no D1 binding at all
// (same as the real shipped config/bindings.json), so turning D1 while
// any other page is active must produce zero jog_seconds ops.
func TestDrainLoop_D1_JogSeconds_OnlyFiresOnEditPage(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "jogSeconds", "secondsPerStep": 60, "detentThreshold": 1}}
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
		if r["op"] == "jog_seconds" {
			t.Fatalf("jog_seconds fired on a non-Edit page (D1 has no col-bank binding), want none: %v", received())
		}
	}
}

// TestDrainLoop_D1_JogSeconds_DoesNotTouchActiveTrack proves jogSeconds is
// fully isolated from the (unreachable-in-production) selectClip
// machinery on the SAME D1 knob: repeated turns past several detents must
// never change Engine.activeTrack, unlike a selectClip binding would.
func TestDrainLoop_D1_JogSeconds_DoesNotTouchActiveTrack(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "jogSeconds", "secondsPerStep": 60, "detentThreshold": 1}}
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
		t.Errorf("activeTrack = %d, want 0 (jogSeconds must never touch D1's OLD selectClip activeTrack state)", got)
	}
}
