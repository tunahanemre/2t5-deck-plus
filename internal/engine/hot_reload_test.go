package engine

// Provenance: adapted from ld-resolve feature/mapping-editor-phase-a (6437f3a) hot_reload_test.go.

// Acceptance tests for the 2026-08-21 hot-reload refactor (Engine.cfg ->
// atomic.Pointer[config.Config], SetConfig, per-event "cfg :=
// e.cfg.Load()" shadowing in both the MIDI-callback closure and the
// drain-loop's tick case -- see engine.go's Engine.cfg doc comment and the
// mapping-editor design doc, docs/specs/2026-08-21-ld-resolve-mapping-
// editor-design.md §7.2).
//
// These are deliberately END-TO-END, through the real Start()/Stop()
// lifecycle (not a unit test of the shadow assignment in isolation): the
// whole point of this refactor is "an edit to bindings.json applies to a
// RUNNING engine, no restart" -- a test that never actually runs the
// engine across the SetConfig call would only prove the code compiles,
// not that the promised user-facing behavior is real. Both tests here
// call SetConfig exactly once, on an Engine already mid-Start(), and never
// call Stop()/Start() again before asserting the new behavior.
//
// Binding-type choice: the design brief that specified this test suggested
// a "key" binding as the example "distinctive binding, observable via
// Status().LastAction". That does not work headlessly: dispatchNoteOn's
// IsKey() case SKIPs before keys.Send() whenever Resolve is not the real
// frontmost app (see editpage_test.go's own note on this, line ~967) --
// which is always true under `go test`, so a "key" binding's LastAction
// would read "" both BEFORE and AFTER a SetConfig rebind, proving nothing.
// "layer" is used instead below: resolveLayerSwitch(cfg, ...) is resolved
// from the SAME shadowed `cfg` at the very top of the MIDI-callback
// closure, is pure internal state (no CGEvent, no frontmost dependency,
// no sidecar round trip), and is directly observable via Status().Layer --
// exactly the same "no frontmost/CGEvent dependency" reasoning
// midi_nonblocking_test.go's own acceptance test already uses a "layer"
// binding for. This is a substitution of WHICH binding type demonstrates
// the mechanism, not a narrowing of what's being proven: resolveLayerSwitch
// takes the identical shadowed `cfg` parameter every other resolution call
// in the same closure relies on, so a passing rebind here is representative
// of the whole closure, not a special case.

import (
	"context"
	"os"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"

	"2t5-deck-plus/internal/config"
)

// newConfigFrom builds a *config.Config directly (not a full *Engine, unlike
// newEngineWithConfig) from an explicit device-map.json + bindings.json
// pair, for tests that need to construct a SECOND config generation to feed
// to SetConfig.
func newConfigFrom(t *testing.T, deviceMapJSON, bindingsJSON string) *config.Config {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/device-map.json", []byte(deviceMapJSON), 0644); err != nil {
		t.Fatalf("write device-map.json: %v", err)
	}
	if err := os.WriteFile(dir+"/bindings.json", []byte(bindingsJSON), 0644); err != nil {
		t.Fatalf("write bindings.json: %v", err)
	}
	cfg, err := config.Load(dir+"/device-map.json", dir+"/bindings.json")
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	return cfg
}

// TestHotReload_NoteOn_SetConfig_AppliesWithoutRestart is the headline
// acceptance test for Part 1: a note-on binding rebound via SetConfig on a
// RUNNING engine takes effect on the very next press of the SAME physical
// control, with no Stop()/Start() in between. This is the literal thing
// the user is buying with this phase ("ends the relaunch-per-config-change
// cycle") -- proven here, not just plausible from code inspection.
func TestHotReload_NoteOn_SetConfig_AppliesWithoutRestart(t *testing.T) {
	stubStateFile(t)
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"40":"L1"}}`
	oldBindings := `{"col": {"L1": {"type":"layer","layer":1}}}`

	eng := newEngineWithConfig(t, deviceMap, oldBindings)
	sockPath, _ := startRecordingSidecar(t, "color")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()
	cb := *cbPtr

	// --- OLD behavior: L1 press resolves against the ORIGINAL config
	// (layer 1). ---
	cb(midi.NoteOn(0, 40, 100), 0)
	deadline := time.Now().Add(2 * time.Second)
	for eng.Status().Layer != 1 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.Status(); got.Layer != 1 || got.LastAction != "Layer 1" {
		t.Fatalf("before SetConfig: Status = {Layer:%d LastAction:%q}, want {Layer:1 LastAction:\"Layer 1\"}", got.Layer, got.LastAction)
	}

	// --- Rebind the SAME physical control (L1) to a DIFFERENT layer,
	// live, on the already-running engine. No Stop()/Start() call exists
	// anywhere in this test from here on. ---
	newBindings := `{"col": {"L1": {"type":"layer","layer":2}}}`
	newCfg := newConfigFrom(t, deviceMap, newBindings)
	eng.SetConfig(newCfg)

	// --- NEW behavior: the SAME MIDI event (same note number, same
	// physical control) must now resolve against the NEW config. ---
	cb(midi.NoteOn(0, 40, 100), 0)
	deadline = time.Now().Add(2 * time.Second)
	for eng.Status().Layer != 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if got := eng.Status(); got.Layer != 2 || got.LastAction != "Layer 2" {
		t.Fatalf("after SetConfig: Status = {Layer:%d LastAction:%q}, want {Layer:2 LastAction:\"Layer 2\"} -- "+
			"the rebind must apply to the ALREADY-RUNNING engine without a restart", got.Layer, got.LastAction)
	}
}

// TestHotReload_DrainLoop_SetConfig_AppliesOnNextTick proves the SAME
// contract on the OTHER dispatch path: a "cdl" binding routed through the
// drain loop's tick case -> resolveTurnBinding -> processNonBalanceDrain ->
// router.Route. Unlike the note-on path (resolved synchronously inside the
// MIDI callback the instant a press arrives), a knob turn is decoded in the
// MIDI callback (accum.Add, untouched by this refactor -- see the design
// doc's Job 3 section on why that call site stays exactly as simple as it
// is) and only resolved 50ms later on the NEXT drain tick, via the tick
// case's OWN "cfg := e.cfg.Load()" shadow (a separate shadow from the
// MIDI-callback's, by design -- see engine.go's Start() doc comments) --
// so this test also exercises that SECOND shadow site, not the same one
// TestHotReload_NoteOn_SetConfig_AppliesWithoutRestart already covers.
func TestHotReload_DrainLoop_SetConfig_AppliesOnNextTick(t *testing.T) {
	stubStateFile(t)
	deviceMap := `{"knobs":{"36":"Saturation"},"sliders":{},"buttons":{}}`
	oldBindings := `{"col": {"Saturation": {"param":"offset","ch":"master"}}}`

	eng := newEngineWithConfig(t, deviceMap, oldBindings)
	sockPath, received := startRecordingSidecar(t, "color")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()
	cb := *cbPtr

	// --- OLD behavior: a turn resolves against the ORIGINAL binding
	// (param=offset, ch=master). ---
	cb(midi.ControlChange(0, 36, 1), 0)
	waitForRecs(t, received, 1)
	recs := received()
	if recs[0]["op"] != "delta" || recs[0]["param"] != "offset" || recs[0]["ch"] != "master" {
		t.Fatalf("before SetConfig: cmd = %v, want op=delta param=offset ch=master", recs[0])
	}

	// --- Rebind the SAME physical control (Saturation/CC36) to a
	// DIFFERENT CDL param+channel, live, on the already-running engine. No
	// Stop()/Start() call exists anywhere in this test from here on. ---
	newBindings := `{"col": {"Saturation": {"param":"slope","ch":"r"}}}`
	newCfg := newConfigFrom(t, deviceMap, newBindings)
	eng.SetConfig(newCfg)

	// --- NEW behavior: the SAME CC number must resolve against the NEW
	// config on the very next drain tick (50ms), no restart. ---
	cb(midi.ControlChange(0, 36, 1), 0)
	waitForRecs(t, received, 2)
	recs = received()
	if recs[1]["op"] != "delta" || recs[1]["param"] != "slope" || recs[1]["ch"] != "r" {
		t.Fatalf("after SetConfig: cmd = %v, want op=delta param=slope ch=r -- "+
			"the rebind must apply on the next drain tick of the ALREADY-RUNNING engine without a restart", recs[1])
	}
}
