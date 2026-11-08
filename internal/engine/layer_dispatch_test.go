package engine

import (
	"context"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"
)

// TestNoteOnHonoursLiveLayer is the regression guard for the review board's
// PART A finding (2026-08-12): resolveNoteBank -- the ONLY production caller
// of the layer-aware resolver -- called resolveNoteBinding(cfg, page, 1,
// control) with a HARDCODED layer 1, never layerState.Get(). Every other
// layer-aware helper in this file (editBankBinding, the seven direct
// intercepts, resolveTurnBinding) correctly threads the live layer through;
// this one call site did not. Consequence, verified by the orchestrator
// against the live app: the layer system had NEVER remapped a button. L1/
// L2/L3 set layerState and tinted the HUD, but every note_on/button press
// always resolved at layer 1 regardless.
//
// Unlike TestResolveNoteBank (a pure-function unit test of resolveNoteBank
// itself) and TestResolveNoteBindingPrefersLayerBank (a pure-function test
// of resolveNoteBinding), THIS test drives the real Start()/MIDI-callback
// path end to end -- pressing L2, then pressing a button, through the exact
// same callback production code takes. A pure-function test of
// resolveNoteBank alone cannot catch this class of bug: the function itself
// was byte-correct once given a layer argument, the bug was that Start()
// never PASSED one. Only an end-to-end test proves the wiring, not just the
// resolver.
func TestNoteOnHonoursLiveLayer(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"40":"L1","41":"L2","92":"TestBtn","93":"OtherBtn"}}`
	bindings := `{
		"_config": {"keyDebounceMs": 1},
		"col": {
			"L1":       {"type":"layer","layer":1},
			"L2":       {"type":"layer","layer":2},
			"TestBtn":  {"type":"cdl","param":"offset","ch":"master"},
			"OtherBtn": {"type":"cdl","param":"gain","ch":"master"}
		},
		"col-2": {
			"TestBtn": {"type":"cdl","param":"contrast","ch":"master"}
		}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)
	stubStateFile(t)

	sockPath, received := startRecordingSidecar(t, "color")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	cb := *cbPtr

	// Precondition: still at layer 1 -- TestBtn resolves col's binding.
	cb(midi.NoteOn(0, 92, 100), 0)
	waitForRecs(t, received, 1)
	if p := received()[0]["param"]; p != "offset" {
		t.Fatalf("layer 1 (precondition): param = %v, want offset", p)
	}

	// Switch to layer 2 (L2 press).
	cb(midi.NoteOn(0, 41, 100), 0)

	// A control WITH a col-2 entry must now resolve the layer-2 binding.
	// Sleep past the (deliberately shrunk) debounce window first -- this is
	// a REPEATED press of the same control name ("TestBtn"), and
	// isDuplicateNoteOn is keyed per-control, not per-binding-result.
	time.Sleep(5 * time.Millisecond)
	cb(midi.NoteOn(0, 92, 100), 0)
	waitForRecs(t, received, 2)
	if p := received()[1]["param"]; p != "contrast" {
		t.Fatalf("layer 2: TestBtn param = %v, want contrast (col-2 binding must fire -- this is the bug: it always fired layer 1's binding)", p)
	}

	// A control ABSENT from col-2 must still fall through to its layer-1
	// binding -- the cascade's documented "an unfinished layer never
	// leaves a control dead" contract, proven end to end.
	cb(midi.NoteOn(0, 93, 100), 0)
	waitForRecs(t, received, 3)
	if p := received()[2]["param"]; p != "gain" {
		t.Fatalf("layer 2, OtherBtn absent from col-2: param = %v, want gain (must fall through to layer 1)", p)
	}

	// Switch back to layer 1 (L1 press) and confirm TestBtn resolves its
	// layer-1 binding again -- not stuck on layer 2's. Same debounce-window
	// consideration as above (repeated "TestBtn" press).
	cb(midi.NoteOn(0, 40, 100), 0)
	time.Sleep(5 * time.Millisecond)
	cb(midi.NoteOn(0, 92, 100), 0)
	waitForRecs(t, received, 4)
	if p := received()[3]["param"]; p != "offset" {
		t.Fatalf("back at layer 1: TestBtn param = %v, want offset", p)
	}
}
