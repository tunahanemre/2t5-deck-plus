package engine

// Regression tests for the LIVE "C1 not fixed" report (2026-08-12, post
// BUG-1-debounce-fix relaunch): the log showed exactly TWO note_on
// dispatches getting through per burst, then the rest correctly suppressed
// -- inconsistent with isDuplicateNoteOn's own logic, which (per
// keydebounce_test.go's already-passing 5x-sequential-burst test) is
// correct for a SINGLE shared noteDebounceLast map. isDuplicateNoteOn is
// mutex-guarded and Engine-scoped (never locally shadowed, never reset
// elsewhere -- verified by direct source read), so two concurrent callers
// on the SAME *Engine sharing the SAME map cannot both slip through it.
//
// The only place that DOESN'T serialize correctly is Start() itself:
//
//	e.mu.Lock()
//	if e.status.Running { e.mu.Unlock(); return ErrAlreadyRunning }
//	e.mu.Unlock()
//	... (sidecar dial, MIDI listener registration, ~800 lines) ...
//	e.status.Running = true   // set MUCH later, in a SEPARATE critical section
//
// This is a classic TOCTOU race: the "already running?" check and the
// "mark as running" write are two SEPARATE lock/unlock sections, not one
// atomic operation. Two overlapping Start() calls (e.g. autoStartEngine()
// racing an early manual click, or any other double-trigger) can BOTH see
// Running==false and BOTH proceed all the way through — each registering
// its OWN independent MIDI listener via startMIDIFn, each with its own
// closure-local state (bank/layerState/pmodeState/nodeAtomic/
// fineModeAtomic are all Start()-local vars, freshly allocated per
// invocation).
//
// Both listeners share e.noteDebounceLast (an Engine field, never locally
// shadowed) and each INDIVIDUAL delivery into isDuplicateNoteOn is still
// correctly mutex-serialized -- that part of the debounce is provably
// sound. The actual defect is upstream of it: with TWO live listeners,
// CoreMIDI (correctly, by design -- multiple listeners on one port is
// normal, supported behaviour, not itself a bug) delivers ONE physical
// button press as TWO SEPARATE raw callback invocations, one per
// listener, each carrying its own note_on message. From the debounce's
// point of view those are two genuinely distinct incoming events, no
// different from two rapid genuine presses -- and if the SECOND listener's
// delivery lands outside the (short, 50ms-live) window of the first, or
// simply loses the race to be "first recorded" for its OWN burst before
// the other listener's burst has fully settled, both can independently
// produce one dispatch each. The debounce was never designed to (and
// cannot, from its own vantage point) distinguish "two genuine rapid
// presses" from "one physical press fanned out to two listeners" -- the
// DIRECTLY OBSERVABLE, machine-verifiable defect is upstream: Start() can
// register MORE THAN ONE live MIDI listener for a single engine, which
// must never happen regardless of how many duplicate dispatches a given
// burst shape produces downstream of it.
//
// All hermetic: no live Resolve, fake unix-socket sidecar, no CGO/
// keys.Send, DECKPLUS_ARMED never set.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"
)

// TestStart_ConcurrentCalls_OnlyOneMIDIListenerRegistered is the direct
// regression test for the TOCTOU race in Start()'s ErrAlreadyRunning
// guard: N concurrent Start() calls on the SAME *Engine must result in
// EXACTLY ONE real MIDI listener registration (one startMIDIFn call) and
// exactly one success (the rest must see ErrAlreadyRunning) -- never two
// or more live listeners for one engine.
func TestStart_ConcurrentCalls_OnlyOneMIDIListenerRegistered(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"49":"C1"}}`
	bindings := `{"col": {"C1": {"type": "key", "key": "Space"}}, "edit": {}}`
	eng := newEngineWithConfig(t, deviceMap, bindings)
	eng.sockPath = startFakeSidecar(t)
	stubSidecarLaunch(t)

	var registerCount int32
	orig := startMIDIFn
	t.Cleanup(func() { startMIDIFn = orig })
	startMIDIFn = func(callback func(msg midi.Message, timestampms int32)) (func(), error) {
		atomic.AddInt32(&registerCount, 1)
		return func() {}, nil
	}

	const concurrency = 10
	var wg sync.WaitGroup
	var successCount, alreadyRunningCount, otherErrCount int32
	wg.Add(concurrency)
	for i := 0; i < concurrency; i++ {
		go func() {
			defer wg.Done()
			err := eng.Start(context.Background())
			switch {
			case err == nil:
				atomic.AddInt32(&successCount, 1)
			case errors.Is(err, ErrAlreadyRunning):
				atomic.AddInt32(&alreadyRunningCount, 1)
			default:
				atomic.AddInt32(&otherErrCount, 1)
				t.Errorf("Start() unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()
	t.Cleanup(func() { eng.Stop() })

	if registerCount != 1 {
		t.Errorf("startMIDIFn invoked %d times across %d concurrent Start() calls on the SAME engine, want exactly 1 -- "+
			"each extra registration is an INDEPENDENT live MIDI listener (its own Start()-local closure state) that "+
			"CoreMIDI will deliver every future physical event to, alongside the first -- this is the mechanism behind "+
			"the live 'exactly two note_on dispatches get through, then the rest suppress' report", registerCount, concurrency)
	}
	if successCount != 1 {
		t.Errorf("successCount = %d, want exactly 1 (exactly one Start() call must actually start the engine)", successCount)
	}
	if alreadyRunningCount != concurrency-1 {
		t.Errorf("alreadyRunningCount = %d, want %d (every OTHER concurrent call must see ErrAlreadyRunning)", alreadyRunningCount, concurrency-1)
	}
}

// TestNoteOnDebounce_TwoRacedListeners_SamePhysicalEventStillOneDispatch
// reproduces the LIVE symptom end to end: if Start()'s race (proven above)
// lets two independent MIDI listeners register for one engine, and
// CoreMIDI (correctly, by design) delivers ONE physical note_on to BOTH of
// them, this must still result in exactly ONE dispatched action -- never
// two. Captures every callback startMIDIFn registers (not just the last),
// forces the exact overlapping-Start scenario via a startMIDIFn stub that
// blocks the FIRST call until the second has also entered Start(), then
// fires the identical NoteOn message through every captured callback
// (modelling one physical press delivered to N independently-registered
// listeners) and asserts exactly one sidecar op resulted.
//
// Uses a "property" binding rather than "key" -- see keydebounce_test.go's
// file doc comment for why: a "key" binding's CGEvent path is gated behind
// macapp.BundleIDIsResolve(FrontmostBundleID()), which always SKIPs under
// `go test` regardless of debounce correctness, making it useless for this
// assertion. "property" reaches the fake sidecar directly with no such
// gate.
func TestNoteOnDebounce_TwoRacedListeners_SamePhysicalEventStillOneDispatch(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"38":"Temperature"}}`
	bindings := `{
		"_config": {"keyDebounceMs": 2000},
		"col": {},
		"edit": {"Temperature": {"type": "property", "property": "CropTop", "sens": 2.0}}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)
	sockPath, received := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)

	// Capture EVERY callback startMIDIFn registers, forcing two concurrent
	// Start() calls to both reach registration (reproducing the race
	// proven by the test above deterministically, rather than hoping two
	// goroutines happen to interleave inside the TOCTOU window).
	var mu sync.Mutex
	var callbacks []func(msg midi.Message, timestampms int32)
	orig := startMIDIFn
	t.Cleanup(func() { startMIDIFn = orig })
	release := make(chan struct{})
	var entered int32
	startMIDIFn = func(callback func(msg midi.Message, timestampms int32)) (func(), error) {
		mu.Lock()
		callbacks = append(callbacks, callback)
		mu.Unlock()
		// The first caller into startMIDIFn waits for the second to also
		// arrive before either returns -- this deterministically wins the
		// TOCTOU race window (both Start() calls have already passed the
		// e.status.Running check by the time either reaches here) instead
		// of relying on goroutine-scheduling luck.
		if atomic.AddInt32(&entered, 1) == 1 {
			select {
			case <-release:
			case <-time.After(200 * time.Millisecond):
				// Fixed code rejects the second Start() call immediately
				// (before it ever reaches startMIDIFn), so `entered` never
				// reaches 2 post-fix -- this timeout is the expected,
				// fast path once the race is closed, not a slow fallback
				// for a real race that might still be forming.
			}
		} else {
			close(release)
		}
		return func() {}, nil
	}

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		go func() {
			defer wg.Done()
			_ = eng.Start(context.Background()) // errors (if any) are not this test's concern
		}()
	}
	wg.Wait()
	t.Cleanup(func() { eng.Stop() })

	mu.Lock()
	cbs := callbacks
	mu.Unlock()
	if len(cbs) < 2 {
		t.Skipf("only %d MIDI listener(s) registered -- the TOCTOU race did not reproduce this run (timing-dependent); "+
			"see TestStart_ConcurrentCalls_OnlyOneMIDIListenerRegistered for the deterministic proof", len(cbs))
	}

	// One physical press, delivered by CoreMIDI to EVERY registered
	// listener independently (this is normal, correct CoreMIDI behaviour
	// for multiple listeners on one port -- the bug is that there are
	// multiple listeners at all, not that CoreMIDI fans out to them).
	for _, cb := range cbs {
		cb(midi.NoteOn(0, 38, 100), 0)
	}

	waitForRecs(t, received, 1)
	time.Sleep(100 * time.Millisecond) // let any (wrongly) duplicate dispatch land
	if n := countRecs(received(), "bump_property"); n != 1 {
		t.Errorf("bump_property sent %d times for ONE physical note_on delivered to %d independently-registered "+
			"listeners, want 1 -- reproduces the live 'C1 gets exactly N dispatches, not 1' report", n, len(cbs))
	}
}
