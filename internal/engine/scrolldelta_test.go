package engine

// Tests for "scrollDelta" (2026-08-13): knob rotation -> a synthetic
// scroll-wheel CGEvent carrying a modifier flags bitmask DIRECTLY ON THE
// EVENT ITSELF, superseding keyDelta on edit-shift.D1. Mirrors
// keydelta_test.go's structure closely -- same accumulator (keyDeltaDetent,
// reused as-is), same frontmost-only gate, same testability shape (a pure
// count/delta resolver extracted from the gated dispatch function) -- with
// one addition keyDelta's own test file never had: a "Resolve IS frontmost"
// path that proves the actual mouse.Scroll call (delta AND flags) via
// mouse.NoopCalls(), not just the SKIP return value, since mouse.Scroll's
// OWN arm guard (DECKPLUS_ARMED, never set under `go test`) makes that
// safe to exercise even when the frontmost gate passes.

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"

	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/keys"
	"2t5-deck-plus/internal/macapp"
	"2t5-deck-plus/internal/mouse"
)

// syncLogBuffer is a mutex-guarded bytes.Buffer -- needed because Start()'s
// background drain-loop goroutine keeps calling log.Printf (dispatch lines,
// periodic status-poll housekeeping, etc.) for as long as the engine is
// running, so a plain bytes.Buffer read from the test goroutine WHILE
// Start() is still active is a genuine data race, not just a theoretical
// one: caught live via `go test -race` on this exact test, racing this
// test's own logBuf.String() read against dispatchScrollDelta's
// log.Printf call. Write/String are both locked so neither side can
// observe a torn read/write.
type syncLogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// ---------------------------------------------------------------------------
// resolveTurnBinding classifies a "scrollDelta" binding correctly -- the
// turn-side config-integration proof, mirroring
// TestResolveTurnBinding_OverlayFirst (keyDelta) in overlay_intercepts_test.go.
// ---------------------------------------------------------------------------

func TestResolveTurnBinding_OverlayFirst_ScrollDelta(t *testing.T) {
	cfg := loadConfig(t, `{
	  "edit":       {"D1": {"type":"jogMinuteSnap"}},
	  "edit-shift": {"D1": {"type":"scrollDelta","flags":"Alt","linesPerDetent":1}}
	}`)
	b, bank, ok := resolveTurnBinding(cfg, "edit", 1, "D1", "shift")
	if !ok || bank != "edit-shift" || !b.IsScrollDelta() {
		t.Errorf("resolveTurnBinding(edit,1,D1,shift) = bank=%q scrollDelta=%v ok=%v, want edit-shift/true/true", bank, b.IsScrollDelta(), ok)
	}
	if b.IsKeyDelta() {
		t.Error("edit-shift.D1 resolved as scrollDelta must NOT also classify as keyDelta")
	}
}

// ---------------------------------------------------------------------------
// scrollDeltaFromCount -- the pure count/linesPerDetent -> delta resolver,
// extracted from dispatchScrollDelta for the same testability reason
// keyDeltaKeyAndCount was extracted from dispatchKeyDelta.
// ---------------------------------------------------------------------------

func TestScrollDeltaFromCount_Clockwise(t *testing.T) {
	delta, ok := scrollDeltaFromCount(3, 2)
	if !ok || delta != 6 {
		t.Errorf("scrollDeltaFromCount(3,2) = delta=%d ok=%v, want 6/true", delta, ok)
	}
}

func TestScrollDeltaFromCount_CounterClockwise(t *testing.T) {
	delta, ok := scrollDeltaFromCount(-2, 3)
	if !ok || delta != -6 {
		t.Errorf("scrollDeltaFromCount(-2,3) = delta=%d ok=%v, want -6/true", delta, ok)
	}
}

func TestScrollDeltaFromCount_ZeroCount_NotOK(t *testing.T) {
	if _, ok := scrollDeltaFromCount(0, 1); ok {
		t.Error("scrollDeltaFromCount(0,1) ok = true, want false")
	}
}

func TestScrollDeltaFromCount_ZeroLinesPerDetent_NotOK(t *testing.T) {
	if _, ok := scrollDeltaFromCount(3, 0); ok {
		t.Error("scrollDeltaFromCount(3,0) ok = true, want false")
	}
}

// TestScrollDeltaFromCount_NegativeLinesPerDetent_InvertsSign proves a
// negative LinesPerDetent (the documented config-level fix for a live
// direction that comes out backwards) flips the resulting delta's sign for
// BOTH spin directions, without any code change.
func TestScrollDeltaFromCount_NegativeLinesPerDetent_InvertsSign(t *testing.T) {
	cw, ok := scrollDeltaFromCount(2, -1)
	if !ok || cw != -2 {
		t.Errorf("scrollDeltaFromCount(2,-1) = delta=%d ok=%v, want -2/true (inverted CW)", cw, ok)
	}
	ccw, ok := scrollDeltaFromCount(-2, -1)
	if !ok || ccw != 2 {
		t.Errorf("scrollDeltaFromCount(-2,-1) = delta=%d ok=%v, want 2/true (inverted CCW)", ccw, ok)
	}
}

// TestScrollDeltaFromCount_FastSpin_ComposesWithKeyDeltaDetent proves the
// "fast spin fires several scroll steps in one tick" requirement
// end-to-end by composing the REUSED keyDeltaDetent accumulator (same
// struct scrollDeltaAccum uses in Start()'s drain loop) with
// scrollDeltaFromCount -- the same two-piece composition
// TestKeyDeltaBinding_ConfigResolvesAndDispatches already proves for
// keyDelta.
func TestScrollDeltaFromCount_FastSpin_ComposesWithKeyDeltaDetent(t *testing.T) {
	var d keyDeltaDetent
	count := d.add(17, 4) // 17/4 = 4 whole detents, remainder 1 carried (same as TestKeyDeltaDetent_FastSpin_FiresMultipleDetentsInOneCall)
	if count != 4 {
		t.Fatalf("keyDeltaDetent.add(17,4) = %d, want 4 (precondition for this test)", count)
	}
	delta, ok := scrollDeltaFromCount(count, 3) // 3 lines per detent
	if !ok || delta != 12 {
		t.Errorf("scrollDeltaFromCount(4,3) = delta=%d ok=%v, want 12/true (4 detents x 3 lines/detent in one call)", delta, ok)
	}
}

// ---------------------------------------------------------------------------
// dispatchScrollDelta -- the gated dispatch function itself.
// ---------------------------------------------------------------------------

// TestDispatchScrollDelta_ZeroCount_Noop proves count==0 short-circuits
// before the frontmost gate and before flags parsing.
func TestDispatchScrollDelta_ZeroCount_Noop(t *testing.T) {
	if got := dispatchScrollDelta("D1", 0, 1, "Alt"); got != "" {
		t.Errorf("dispatchScrollDelta(D1,0,1,Alt) = %q, want \"\"", got)
	}
}

// TestDispatchScrollDelta_ZeroLinesPerDetent_Noop proves an explicit-zero
// linesPerDetent (defensive; production always applies
// defaultLinesPerDetent before calling this) is a clean no-op too.
func TestDispatchScrollDelta_ZeroLinesPerDetent_Noop(t *testing.T) {
	if got := dispatchScrollDelta("D1", 3, 0, "Alt"); got != "" {
		t.Errorf("dispatchScrollDelta(D1,3,0,Alt) = %q, want \"\"", got)
	}
}

// TestDispatchScrollDelta_UnknownFlagsSpec_Noop proves a config typo in the
// "flags" field (e.g. a misspelled modifier name) is a defensive no-op +
// log line, not a panic or a scroll posted with garbage flags -- checked
// BEFORE the frontmost gate, same ordering as keyDeltaKeyAndCount's
// empty-key-spec check in dispatchKeyDelta.
func TestDispatchScrollDelta_UnknownFlagsSpec_Noop(t *testing.T) {
	if got := dispatchScrollDelta("D1", 1, 1, "Bogus"); got != "" {
		t.Errorf("dispatchScrollDelta(D1,1,1,Bogus) = %q, want \"\" (unknown modifier name)", got)
	}
}

// TestDispatchScrollDelta_NotFrontmost_Skips mirrors
// TestDispatchKeyDelta_NotFrontmost_Skips exactly, including its live-
// environment caveat: macapp.FrontmostBundleID() queries REAL macOS state,
// so this SKIP path is only exercisable when Resolve is not actually the
// frontmost app on the machine running the test.
func TestDispatchScrollDelta_NotFrontmost_Skips(t *testing.T) {
	if macapp.BundleIDIsResolve(macapp.FrontmostBundleID()) {
		t.Skip("Resolve is the real frontmost app on this machine right now -- the not-frontmost SKIP path cannot be exercised in this environment")
	}
	if got := dispatchScrollDelta("D1", 3, 1, "Alt"); got != "" {
		t.Errorf("dispatchScrollDelta(D1,3,1,Alt) with Resolve not frontmost = %q, want \"\" (SKIP)", got)
	}
}

// TestDispatchScrollDelta_Frontmost_PostsCorrectDeltaAndFlags is the crux
// end-to-end proof this dispatch function's whole feature rests on: when
// the frontmost gate passes, dispatchScrollDelta calls mouse.Scroll with
// BOTH the correct signed delta (count * linesPerDetent) AND the correct
// flags bitmask parsed from the binding's Flags spec -- asserted via
// mouse.NoopCalls(), which is safe to exercise here even with Resolve
// genuinely frontmost because mouse.Scroll has its OWN independent arm
// guard (DECKPLUS_ARMED, never set under `go test`) that no-ops
// regardless of the frontmost gate's outcome. This is the inverse
// live-environment condition from the SKIP test above -- exactly one of
// the two is exercisable on any given machine, so both are written rather
// than picked based on a guess about this environment.
func TestDispatchScrollDelta_Frontmost_PostsCorrectDeltaAndFlags(t *testing.T) {
	if !macapp.BundleIDIsResolve(macapp.FrontmostBundleID()) {
		t.Skip("Resolve is not the real frontmost app on this machine right now -- the frontmost-gate-passes path cannot be exercised in this environment")
	}
	if mouse.Armed() {
		t.Fatal("mouse.Armed() == true with DECKPLUS_ARMED unset -- arm guard is broken; refusing to exercise dispatchScrollDelta in this state")
	}

	mouse.ResetNoopCalls()
	t.Cleanup(mouse.ResetNoopCalls)

	got := dispatchScrollDelta("D1", 3, 2, "Alt") // 3 detents x 2 lines/detent = 6
	if got == "" {
		t.Fatal("dispatchScrollDelta(D1,3,2,Alt) with Resolve frontmost = \"\", want a non-empty summary")
	}

	wantFlags, err := keys.ParseModifierFlags("Alt")
	if err != nil {
		t.Fatalf("keys.ParseModifierFlags(Alt) unexpected error: %v", err)
	}
	calls := mouse.NoopCalls()
	if len(calls) != 1 {
		t.Fatalf("mouse.NoopCalls() = %d entries %v, want 1", len(calls), calls)
	}
	want := fmt.Sprintf("Scroll%v", []float64{6, float64(wantFlags)})
	if calls[0] != want {
		t.Errorf("mouse.NoopCalls()[0] = %q, want %q (delta=6 AND flags=Alt(%#x) both required)", calls[0], want, wantFlags)
	}
}

// TestScrollDeltaBinding_ConfigResolvesAndDispatches is a small end-to-end
// sanity check tying config.Binding.IsScrollDelta()/Flags/LinesPerDetent to
// dispatchScrollDelta's own signature, mirroring
// TestKeyDeltaBinding_ConfigResolvesAndDispatches without duplicating the
// config package's own parsing tests.
func TestScrollDeltaBinding_ConfigResolvesAndDispatches(t *testing.T) {
	b := config.Binding{Type: "scrollDelta", Flags: "Alt", LinesPerDetent: 1}
	if !b.IsScrollDelta() {
		t.Fatal("binding should classify as scrollDelta")
	}
	// count==0 (nothing fired yet this tick) must still be a clean no-op
	// when routed through the real field names.
	if got := dispatchScrollDelta("D1", 0, b.LinesPerDetent, b.Flags); got != "" {
		t.Errorf("dispatchScrollDelta with config fields, count=0 = %q, want \"\"", got)
	}
}

// ---------------------------------------------------------------------------
// Full Start()/MIDI drain-loop integration -- proves the switch-case wiring
// (resolveTurnBinding's overlay rung -> b.IsScrollDelta() -> scrollDeltaAccum
// -> dispatchScrollDelta) is ACTUALLY live in Start(), not just correct as
// pure functions above. Unlike keydelta_test.go's
// TestDrainLoop_ShiftD1_ResolvesViaOverlayBank_NotPlainEditD1 (which needs
// "property" as an OBSERVABLE PROXY type because keyDelta's own dispatch is
// unobservable through NoopCalls when SKIPped), this test uses the REAL
// "scrollDelta" type directly: dispatchScrollDelta logs
// "method=scrollDelta" on EVERY path it can take once a detent has fired
// (both the eventual mouse.Scroll post and the not-frontmost SKIP), and
// logs NOTHING when the accumulator hasn't yet crossed threshold -- so
// asserting that substring in the captured log proves resolution AND a
// firing detent, independent of live frontmost state.
// ---------------------------------------------------------------------------

func TestDrainLoop_ShiftD1_ScrollDeltaWiringLive_LogsMethodScrollDelta(t *testing.T) {
	deviceMap := `{"knobs":{"41":"D1"},"sliders":{},"buttons":{"62":"Shift"}}`
	bindings := `{
		"_config": {"keyDebounceMs": 1},
		"col": {"Shift": {"type": "modifierHold", "modifier": "shift"}},
		"edit": {"D1": {"type": "jogMinuteSnap", "detentThreshold": 1}},
		"edit-shift": {"D1": {"type": "scrollDelta", "flags": "Alt", "linesPerDetent": 1, "detentThreshold": 1}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)

	// scrollDelta never talks to the sidecar (no ipc client involved), but
	// Start()'s drain loop still needs a live page ("edit") from the
	// status poll for resolveTurnBinding to pick the edit/edit-shift
	// banks at all -- same reason every other Start() integration test in
	// this package wires a fake sidecar even when the binding under test
	// doesn't itself send an op.
	sockPath, _ := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	logBuf := &syncLogBuffer{}
	origLogOutput := log.Writer()
	log.SetOutput(logBuf)
	t.Cleanup(func() { log.SetOutput(origLogOutput) })

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 62, 100), 0) // hold Shift (overlay only, zero CGEvents)
	time.Sleep(20 * time.Millisecond)
	cb(midi.ControlChange(0, 41, 1), 0) // turn D1 one detent WHILE Shift is held

	// Poll rather than a single fixed-sleep-then-read (same shape as
	// waitForRecs above) -- the drain tick's alignment relative to wall
	// time is not deterministic, so a fixed sleep is a source of test
	// flakiness a poll loop avoids, on top of syncLogBuffer fixing the
	// underlying data race.
	deadline := time.Now().Add(2 * time.Second)
	var got string
	for time.Now().Before(deadline) {
		got = logBuf.String()
		if strings.Contains(got, "method=scrollDelta") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !strings.Contains(got, "method=scrollDelta") {
		t.Fatalf("expected a [drain] log line for method=scrollDelta (proves edit-shift.D1's scrollDelta binding resolved via the overlay rung and the switch-case dispatched with a firing detent), got none\nlog output:\n%s", got)
	}
}
