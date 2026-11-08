package engine

// Tests for the D1 "clip-select" feature's Batch-2 fix/refinement pass
// (sensitivity + auto-reset), on top of the already-shipped selectClip/
// highlight/release/status-display feature covered by selectclip_test.go
// and highlight_test.go:
//   - selectClipDetent.add/reset (pure accumulator math: threshold
//     crossing, no-multi-jump, direction cancellation, reset)
//   - full Start()/MIDI drain-loop integration: sub-threshold raw CC
//     steps produce NO track movement; crossing the configured
//     detentThreshold moves by exactly one clip
//   - full Start()/MIDI drain-loop integration: the clip-id auto-reset
//     (a tick-over-tick change in the sidecar's status "clip_id" field
//     clears a pinned D1 selection; an unchanged clip_id, even across
//     many ticks, never does)
//
// All hermetic: no live Resolve, fake unix-socket sidecars only, no
// DECKPLUS_ARMED.

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"

	"2t5-deck-plus/internal/ipc"
)

// ---------------------------------------------------------------------------
// selectClipDetent (pure accumulator math)
// ---------------------------------------------------------------------------

func TestSelectClipDetent_FiresOnceThresholdCrossed(t *testing.T) {
	var d selectClipDetent
	for i := 0; i < 3; i++ {
		if got := d.add(1, 4); got != 0 {
			t.Fatalf("add() call %d = %d, want 0 (below threshold)", i+1, got)
		}
	}
	if got := d.add(1, 4); got != 1 {
		t.Fatalf("4th add() = %d, want 1 (threshold crossed)", got)
	}
}

func TestSelectClipDetent_ResetsAfterFiring_NoImmediateRefire(t *testing.T) {
	var d selectClipDetent
	for i := 0; i < 4; i++ {
		d.add(1, 4)
	}
	// Accumulator must be back at 0 after firing -- a single further
	// sub-threshold step must NOT immediately fire again.
	if got := d.add(1, 4); got != 0 {
		t.Errorf("add() right after firing = %d, want 0 (accumulator reset, not carrying remainder)", got)
	}
}

func TestSelectClipDetent_NegativeDirectionFires(t *testing.T) {
	var d selectClipDetent
	for i := 0; i < 3; i++ {
		if got := d.add(-1, 4); got != 0 {
			t.Fatalf("add(-1) call %d = %d, want 0 (below threshold)", i+1, got)
		}
	}
	if got := d.add(-1, 4); got != -1 {
		t.Fatalf("4th add(-1) = %d, want -1 (threshold crossed downward)", got)
	}
}

func TestSelectClipDetent_LargeSingleBurst_FiresExactlyOnce_NoMultiJump(t *testing.T) {
	var d selectClipDetent
	// A single tick's accumulated raw step can itself be far past
	// threshold (e.g. several raw CC ticks summed by the coalesce
	// accumulator within one 50ms drain tick) -- must still fire only
	// ONE clip's worth of movement, never proportional to the overshoot.
	if got := d.add(11, 4); got != 1 {
		t.Fatalf("add(11, threshold=4) = %d, want 1 (fires once, overshoot discarded)", got)
	}
	// Confirm the overshoot was actually discarded (reset to 0), not
	// carried forward as a head start toward the NEXT fire.
	for i := 0; i < 3; i++ {
		if got := d.add(1, 4); got != 0 {
			t.Fatalf("post-burst add() call %d = %d, want 0 (no carried-forward overshoot)", i+1, got)
		}
	}
	if got := d.add(1, 4); got != 1 {
		t.Errorf("post-burst 4th add() = %d, want 1 (fresh threshold reached normally)", got)
	}
}

func TestSelectClipDetent_DirectionReversalCancelsPartialAccumulation(t *testing.T) {
	var d selectClipDetent
	d.add(1, 4)
	d.add(1, 4) // acc=2, below threshold
	if got := d.add(-2, 4); got != 0 {
		t.Fatalf("reversal add(-2) = %d, want 0 (cancels out to acc=0, not a fire)", got)
	}
	// Confirm acc is genuinely back at 0 (a fresh 3 sub-threshold steps,
	// then the 4th fires) rather than something unexpected like -2.
	for i := 0; i < 3; i++ {
		d.add(1, 4)
	}
	if got := d.add(1, 4); got != 1 {
		t.Errorf("add() after reversal-then-4-more = %d, want 1", got)
	}
}

func TestSelectClipDetent_ZeroOrNegativeThreshold_FiresOnEveryStep(t *testing.T) {
	var d selectClipDetent
	if got := d.add(1, 0); got != 1 {
		t.Errorf("add(1, threshold=0) = %d, want 1 (0 treated as 1)", got)
	}
	var d2 selectClipDetent
	if got := d2.add(-1, -5); got != -1 {
		t.Errorf("add(-1, threshold=-5) = %d, want -1 (negative treated as 1)", got)
	}
}

func TestSelectClipDetent_Reset(t *testing.T) {
	var d selectClipDetent
	d.add(1, 4)
	d.add(1, 4) // acc=2
	d.reset()
	for i := 0; i < 3; i++ {
		if got := d.add(1, 4); got != 0 {
			t.Fatalf("post-reset add() call %d = %d, want 0 (reset cleared the head start)", i+1, got)
		}
	}
	if got := d.add(1, 4); got != 1 {
		t.Errorf("post-reset 4th add() = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Full Start()/MIDI drain-loop integration: detentThreshold sensitivity
// ---------------------------------------------------------------------------

func TestDrainLoop_D1_DetentThreshold_SubThresholdSteps_NoMovement(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "selectClip", "detentThreshold": 3}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	clips := []any{
		map[string]any{"track": 1, "name": "A.mov"},
		map[string]any{"track": 2, "name": "B.mov"},
	}
	sockPath, _ := startClipsSidecar(t, "edit", clips)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	// Two raw steps, each in its OWN drain tick (threshold=3) -- must
	// NOT be enough to move the selection yet.
	cb(midi.ControlChange(0, 41, 1), 0)
	time.Sleep(80 * time.Millisecond)
	cb(midi.ControlChange(0, 41, 1), 0)
	time.Sleep(80 * time.Millisecond)

	if got := eng.getActiveTrack(); got != 0 {
		t.Fatalf("activeTrack after 2 sub-threshold steps = %d, want 0 (still unset)", got)
	}

	// The 3rd step crosses the threshold -- exactly ONE clip forward.
	cb(midi.ControlChange(0, 41, 1), 0)

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 1 {
		t.Fatalf("activeTrack after 3rd (threshold-crossing) step = %d, want 1 (exactly one clip, not skipped ahead)", got)
	}
}

func TestDrainLoop_D1_DetentThreshold_DefaultAppliesWhenBindingOmitsIt(t *testing.T) {
	// No "detentThreshold" in the binding at all -- must fall back to
	// defaultSelectClipDetentThreshold (4), not fire on every raw step.
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "selectClip"}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	clips := []any{
		map[string]any{"track": 1, "name": "A.mov"},
	}
	sockPath, _ := startClipsSidecar(t, "edit", clips)
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
	if got := eng.getActiveTrack(); got != 0 {
		t.Fatalf("activeTrack after %d sub-threshold steps = %d, want 0 (default threshold not yet crossed)", defaultSelectClipDetentThreshold-1, got)
	}

	cb(midi.ControlChange(0, 41, 1), 0)
	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 1 {
		t.Fatalf("activeTrack after crossing default threshold = %d, want 1", got)
	}
}

// ---------------------------------------------------------------------------
// Full Start()/MIDI drain-loop integration: clip-id auto-reset
// ---------------------------------------------------------------------------

// startStatusSequenceSidecar spins up a fake unix-socket sidecar (same
// frame protocol as startClipsSidecar) that replies to "status" with
// {"ok":true,"page":page,"clip_id":<current>}, where <current> starts as
// initialClipID and can be changed live via the returned setClipID
// function -- lets a test simulate "the playhead moved to a different
// clip" (or deliberately did NOT) mid-run. Replies to "clips_at_playhead"
// with {"ok":true,"clips":clips} exactly like startClipsSidecar, so D1
// turns still work in the same test.
func startStatusSequenceSidecar(t *testing.T, page, initialClipID string, clips []any) (sockPath string, setClipID func(string)) {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-clipid-*.sock")
	if err != nil {
		t.Fatalf("tempname: %v", err)
	}
	sockPath = f.Name()
	f.Close()
	os.Remove(sockPath)
	t.Cleanup(func() { os.Remove(sockPath) })

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	var mu sync.Mutex
	clipID := initialClipID
	setClipID = func(id string) {
		mu.Lock()
		clipID = id
		mu.Unlock()
	}

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				for {
					body, err := ipc.ReadFrame(c)
					if err != nil {
						return
					}
					var m map[string]any
					_ = json.Unmarshal(body, &m)

					op, _ := m["op"].(string)

					var reply []byte
					switch op {
					case "status":
						mu.Lock()
						cid := clipID
						mu.Unlock()
						var cidVal any
						if cid != "" {
							cidVal = cid
						}
						reply, _ = json.Marshal(map[string]any{"ok": true, "page": page, "clip_id": cidVal})
					case "clips_at_playhead":
						reply, _ = json.Marshal(map[string]any{"ok": true, "clips": clips})
					default:
						reply, _ = json.Marshal(map[string]any{"ok": true})
					}
					if err := ipc.WriteFrame(c, reply); err != nil {
						return
					}
				}
			}(conn)
		}
	}()

	return sockPath, setClipID
}

func TestDrainLoop_D1_AutoReset_ClipIDChange_ClearsActiveTrack(t *testing.T) {
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
	sockPath, setClipID := startStatusSequenceSidecar(t, "edit", "clip-A", clips)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	// Let a few status ticks land on "clip-A" first (seeds haveLastClipID)
	// before turning D1.
	time.Sleep(120 * time.Millisecond)

	cb := *cbPtr
	cb(midi.ControlChange(0, 41, 1), 0) // D1 up: unset -> track 1

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 1 {
		t.Fatalf("activeTrack after D1 turn = %d, want 1 (test precondition)", got)
	}

	// Simulate the playhead moving to a different clip.
	setClipID("clip-B")

	deadline = time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 0 {
		t.Fatalf("activeTrack after clip_id changed = %d, want 0 (auto-reset to auto/GetCurrentVideoItem default)", got)
	}
}

func TestDrainLoop_D1_AutoReset_SameClipID_DoesNotClear(t *testing.T) {
	// Playhead nudges WITHIN the same clip (clip_id never changes) must
	// NEVER clear a pinned D1 selection, even across many status ticks.
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
	sockPath, _ := startStatusSequenceSidecar(t, "edit", "clip-A", clips)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	time.Sleep(120 * time.Millisecond)

	cb := *cbPtr
	cb(midi.ControlChange(0, 41, 1), 0) // D1 up: unset -> track 1

	deadline := time.Now().Add(2 * time.Second)
	for eng.getActiveTrack() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.getActiveTrack(); got != 1 {
		t.Fatalf("activeTrack after D1 turn = %d, want 1 (test precondition)", got)
	}

	// clip_id is never changed -- let many status ticks pass.
	time.Sleep(400 * time.Millisecond)

	if got := eng.getActiveTrack(); got != 1 {
		t.Errorf("activeTrack after many same-clip_id ticks = %d, want 1 (must NOT auto-reset within the same clip)", got)
	}
}

func TestDrainLoop_D1_AutoReset_NoPriorSelection_ClipIDChange_StaysZero(t *testing.T) {
	// activeTrack is already 0 ("auto") -- a clip_id change must stay a
	// harmless no-op (nothing to clear), never an error or a spurious
	// selection.
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{}}`
	bindings := `{
		"col": {},
		"edit": {"D1": {"type": "selectClip", "detentThreshold": 1}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	clips := []any{
		map[string]any{"track": 1, "name": "A.mov"},
	}
	sockPath, setClipID := startStatusSequenceSidecar(t, "edit", "clip-A", clips)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	time.Sleep(120 * time.Millisecond)
	setClipID("clip-B")
	time.Sleep(120 * time.Millisecond)

	if got := eng.getActiveTrack(); got != 0 {
		t.Errorf("activeTrack = %d, want 0 (no prior D1 selection to clear)", got)
	}
}
