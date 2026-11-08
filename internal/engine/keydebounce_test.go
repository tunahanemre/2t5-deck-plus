package engine

// Tests for BUG 1 (2026-08-12 live report): the Loupedeck+ driver is known
// to emit ONE physical button press as several (4-5 observed live) IDENTICAL
// note_on messages within a few milliseconds. For a TOGGLE binding (the
// motivating case: col.C1/edit.C1 -> key Space, play/pause) each duplicate
// re-fires the toggle, so an odd-sized burst plays and an even-sized burst
// nets to nothing ("plays but doesn't pause"). Fixed by debouncing repeat
// note_on PRESSES (vel>0) per control at the dispatch layer, inside Start()'s
// MIDI callback, before every other note_on intercept -- so it protects any
// control uniformly, not just "key"-type bindings.
//
// All hermetic: no live Resolve, fake unix-socket sidecar, no CGO/keys.Send
// (DECKPLUS_ARMED never set -- see the "property" binding used below,
// which reaches the sidecar directly with no macapp.FrontmostBundleID()
// gate, so these tests don't depend on this machine's frontmost-app state
// the way a "key"-type binding's own dispatch would).

import (
	"context"
	"fmt"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"
)

func keyDebounceDeviceMap() string {
	return `{"knobs":{},"sliders":{},"buttons":{"38":"Temperature","40":"Exposure","48":"ControlDial"}}`
}

func keyDebounceBindingsWithWindow(windowMs int) string {
	return fmt.Sprintf(`{
		"_config": {"keyDebounceMs": %d},
		"col": {},
		"edit": {
			"Temperature": {"type": "property", "property": "CropTop", "sens": 2.0},
			"Exposure":    {"type": "property", "property": "CropBottom", "sens": 2.0}
		}
	}`, windowMs)
}

func TestNoteOnDebounce_DuplicateBurst_SuppressesRepeats(t *testing.T) {
	eng := newEngineWithConfig(t, keyDebounceDeviceMap(), keyDebounceBindingsWithWindow(2000))
	sockPath, received := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	// One physical press, modelled as 5 identical note_on messages within
	// a few ms -- exactly the live-observed Loupedeck+ burst shape.
	for i := 0; i < 5; i++ {
		cb(midi.NoteOn(0, 38, 100), 0)
	}

	waitForRecs(t, received, 1)
	time.Sleep(100 * time.Millisecond) // let any (wrongly) suppressed extras land
	recs := received()
	if n := countRecs(recs, "reset_property"); n != 1 {
		t.Fatalf("reset_property sent %d times for a 5-message burst, want 1 (duplicates must be suppressed)", n)
	}
}

func TestNoteOnDebounce_AfterWindowElapses_NextPressStillDispatches(t *testing.T) {
	eng := newEngineWithConfig(t, keyDebounceDeviceMap(), keyDebounceBindingsWithWindow(20))
	sockPath, received := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	cb(midi.NoteOn(0, 38, 100), 0)
	waitForRecs(t, received, 1)

	// A genuinely separate, later press (window has elapsed) must still
	// dispatch -- debounce must not permanently latch a control.
	time.Sleep(60 * time.Millisecond)
	cb(midi.NoteOn(0, 38, 100), 0)
	waitForRecs(t, received, 2)

	if n := countRecs(received(), "reset_property"); n != 2 {
		t.Fatalf("reset_property sent %d times across 2 presses >1 window apart, want 2", n)
	}
}

func TestNoteOnDebounce_IsPerControl_NotGlobal(t *testing.T) {
	eng := newEngineWithConfig(t, keyDebounceDeviceMap(), keyDebounceBindingsWithWindow(2000))
	sockPath, received := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr
	// Two DIFFERENT controls fired back-to-back must both dispatch, even
	// though they land inside the SAME (generous) debounce window --
	// debounce state is keyed per control, not a single global gate.
	cb(midi.NoteOn(0, 38, 100), 0) // Temperature -> CropTop
	cb(midi.NoteOn(0, 40, 100), 0) // Exposure -> CropBottom

	waitForRecs(t, received, 2)
	recs := received()
	if recs[0]["property"] != "CropTop" {
		t.Errorf("recs[0].property = %v, want CropTop", recs[0]["property"])
	}
	if recs[1]["property"] != "CropBottom" {
		t.Errorf("recs[1].property = %v, want CropBottom", recs[1]["property"])
	}
}

// TestNoteOnDebounce_DoesNotBlockFineModeHoldRelease proves the debounce
// respects the "must NOT break velocity-0 note-off handling" constraint: a
// fineModeHold PRESS immediately followed (within the SAME debounce window)
// by its own genuine RELEASE (vel==0, a different MIDI event, not a
// duplicate) must still turn fine mode off. The debounce is only ever
// consulted for vel>0 note_on presses (see Start()'s MIDI callback), so the
// vel==0 branch is untouched by construction -- this test proves that
// end-to-end rather than trusting the placement alone.
func TestNoteOnDebounce_DoesNotBlockFineModeHoldRelease(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"48":"C3"}}`
	bindings := `{
		"_config": {"keyDebounceMs": 2000},
		"col": {"C3": {"type": "fineModeHold"}},
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
	cb(midi.NoteOn(0, 48, 100), 0) // C3 press -> fine mode ON
	deadline := time.Now().Add(2 * time.Second)
	for !eng.Status().FineMode && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !eng.Status().FineMode {
		t.Fatal("FineMode not ON after C3 press (test precondition)")
	}

	// Immediate release (vel==0), well within the 2000ms debounce window
	// configured above -- must still be processed as a note-off, not
	// swallowed by the press-debounce.
	cb(midi.NoteOn(0, 48, 0), 0)
	deadline = time.Now().Add(2 * time.Second)
	for eng.Status().FineMode && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if eng.Status().FineMode {
		t.Fatal("FineMode still ON after C3 release -- vel==0 must not be blocked by the press-debounce")
	}
}

// TestNoteOnDebounce_DoesNotBlockFineModeHoldRelease_RealNoteOff is the same
// proof as the test immediately above, but with a REAL MIDI Note Off (status
// 0x8n) instead of the vel==0 note-on idiom -- the 2026-08-12 live
// diagnostic trace confirmed the Loupedeck sends the former (raw=80 31 40,
// i.e. channel 0, note 49, velocity 64) for at least C1, and msg.GetNoteOn
// returns false outright for it, so before the GetNoteEnd fix this exact
// press-then-release sequence would have left fine mode stuck ON forever
// with no path to turn it off. Uses velocity 64 (not 0), matching the live
// trace exactly, to prove recognition does not depend on velocity at all
// for a real Note Off.
func TestNoteOnDebounce_DoesNotBlockFineModeHoldRelease_RealNoteOff(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"48":"C3"}}`
	bindings := `{
		"_config": {"keyDebounceMs": 2000},
		"col": {"C3": {"type": "fineModeHold"}},
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
	cb(midi.NoteOn(0, 48, 100), 0) // C3 press -> fine mode ON
	deadline := time.Now().Add(2 * time.Second)
	for !eng.Status().FineMode && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if !eng.Status().FineMode {
		t.Fatal("FineMode not ON after C3 press (test precondition)")
	}

	// Immediate REAL Note Off (not vel==0 note-on), well within the 2000ms
	// debounce window configured above -- must still be processed as a
	// release, not swallowed by the press-debounce (which only ever governs
	// vel>0 note_on presses) and not silently dropped for lacking a
	// matching msg.GetNoteOn() (it never had one).
	cb(midi.NoteOffVelocity(0, 48, 64), 0)
	deadline = time.Now().Add(2 * time.Second)
	for eng.Status().FineMode && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if eng.Status().FineMode {
		t.Fatal("FineMode still ON after C3 real Note Off release -- a genuine 0x8n Note Off must not be blocked by the press-debounce")
	}
}
