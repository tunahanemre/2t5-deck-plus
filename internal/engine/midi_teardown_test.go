package engine

// Regression coverage for BUG 2 (2026-08-12 dispatch prompt): C1 (Space,
// play/pause) intermittently dispatched TWICE for one physical press,
// specifically observed right after an engine restart with no teardown log
// preceding it -- i.e. a genuinely SEQUENTIAL Stop()->Start() cycle, not the
// concurrent-Start() TOCTOU already fixed in 0a0ccdb (that fix closed
// concurrent entry into Start(), not a clean serial restart).
//
// Root cause, traced into gitlab.com/gomidi/midi/v2 v2.3.23's vendored
// source (module cache, not reproduced here):
//   - midi.ListenTo's own returned stop() (listen.go) calls ONLY
//     rtmidi.MIDIIn.CancelCallback() -- never in.Close(). CancelCallback
//     clears the RtMidiIn C++ object's callback pointer; the underlying
//     native CoreMIDI connection (MIDIInputPortCreate + MIDIPortConnectSource,
//     opened once inside in.Open()) is torn down ONLY by Close()
//     (MIDIPortDispose, via rtmidi_in_free) -- see RtMidi.cpp/in.go in the
//     module cache.
//   - midi.FindInPort builds a BRAND NEW, unopened *in wrapper on every
//     call (rtmididrv.Driver.Ins() constructs a fresh struct each time; it
//     never reuses or tracks a previously-opened port).
// Combined: every engine Stop() that called only the bare ListenTo stop()
// left THAT Start()'s native CoreMIDI connection to the Loupedeck alive
// forever (only its Go-level callback was cancelled), and the very next
// Start() opened an ADDITIONAL, independent CoreMIDI connection on top of
// it. CoreMIDI fans one physical press out to every connected port -- the
// identical mechanism already root-caused for concurrent Start() in
// 0a0ccdb, just triggered here by a clean, sequential restart instead of a
// race. Whether the orphaned connection's Go-level callback stays reliably
// silent after CancelCallback (a plain field mutation, no memory barrier
// against the CoreMIDI I/O thread) is exactly the kind of window that
// explains "intermittent, not constant" duplicate dispatch.
//
// Fix: openMIDIListener (engine.go) wraps midi.ListenTo so the returned
// stop() ALSO closes the port -- eliminating the leaked connection at its
// source, rather than trying to out-debounce an ever-growing pile of stale
// listeners. TestOpenMIDIListener_* below tests that unit directly, against
// a fake drivers.In (no real MIDI/CoreMIDI touched -- midi.ListenTo itself
// is pure Go logic operating through the drivers.In interface).
//
// TestNoteOnDebounce_SurvivesStopStartCycle further down is the
// engine-level regression test the task asked for in those literal terms
// ("one MIDI event produces exactly one dispatch after a Stop->Start
// cycle"): it captures the callback from TWO separate Start() calls
// (bracketing a real Stop()) and fires the SAME synthetic event through
// BOTH -- simulating exactly what an un-fixed vendor-level leak like the
// one above would deliver -- proving the existing shared debounce state
// (Engine.noteDebounceLast, an Engine field that intentionally survives
// Stop()/Start(), see isDuplicateNoteOn's doc comment) is a second,
// independent line of defense even if a listener-leak regression like
// this one recurred. It asserts on the debounce's own log line rather
// than a downstream dispatch side-effect: the STALE callback closes over
// its OWN Start() call's client/bank/etc, which Stop() has already torn
// down by the time these events fire, so a downstream op landing at the
// (new) fake sidecar is not a reliable signal here -- but the debounce
// check itself runs unconditionally, BEFORE any of that per-Start()
// closure state is touched, for every control on every registered
// callback, so its log line is the correct, precise observation point
// for "did the SECOND delivery of one physical press get recognized as a
// duplicate."

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/drivers"
)

// ---------------------------------------------------------------------------
// openMIDIListener: unit test against a fake drivers.In (no real MIDI/
// CoreMIDI involved -- midi.ListenTo is pure Go logic dispatched through
// the drivers.In interface, so a fake satisfying that interface exercises
// the REAL midi.ListenTo call, just not real hardware underneath it).
// ---------------------------------------------------------------------------

type fakeMIDIIn struct {
	open        bool
	closeCalls  int
	closeErr    error
	listenCalls int
	cancelCalls int
}

func (f *fakeMIDIIn) Open() error     { f.open = true; return nil }
func (f *fakeMIDIIn) IsOpen() bool    { return f.open }
func (f *fakeMIDIIn) Number() int     { return 1 }
func (f *fakeMIDIIn) String() string  { return "fake Loupedeck" }
func (f *fakeMIDIIn) Underlying() any { return nil }
func (f *fakeMIDIIn) Close() error {
	f.closeCalls++
	f.open = false
	return f.closeErr
}
func (f *fakeMIDIIn) Listen(onMsg func(msg []byte, milliseconds int32), config drivers.ListenConfig) (stopFn func(), err error) {
	f.listenCalls++
	return func() { f.cancelCalls++ }, nil
}

var _ drivers.In = (*fakeMIDIIn)(nil)

func TestOpenMIDIListener_StopBothCancelsAndClosesThePort(t *testing.T) {
	in := &fakeMIDIIn{}
	stop, err := openMIDIListener(in, func(msg midi.Message, _ int32) {})
	if err != nil {
		t.Fatalf("openMIDIListener: %v", err)
	}
	if in.listenCalls != 1 {
		t.Fatalf("Listen called %d times, want 1", in.listenCalls)
	}
	if in.closeCalls != 0 {
		t.Fatalf("port closed before stop() was even called: closeCalls=%d", in.closeCalls)
	}

	stop()

	if in.cancelCalls != 1 {
		t.Errorf("cancelCalls = %d, want 1 (midi.ListenTo's own stop() must still run)", in.cancelCalls)
	}
	// This is the actual regression assertion: the pre-fix startMIDIFn
	// returned midi.ListenTo's bare stop() (cancel-only) directly, so
	// closeCalls would stay 0 forever -- the exact leak that let a
	// Stop()->Start() cycle accumulate one more live CoreMIDI connection
	// to the Loupedeck on every restart.
	if in.closeCalls != 1 {
		t.Errorf("closeCalls = %d, want 1 -- stop() must Close() the port, not just cancel its callback", in.closeCalls)
	}
}

func TestOpenMIDIListener_StopSurvivesACloseError(t *testing.T) {
	// Close() erroring (e.g. the port was somehow already gone) must not
	// panic or block -- stop() has no return value for the caller to
	// check, and Start()'s "defer stop()" teardown must complete either way.
	in := &fakeMIDIIn{closeErr: errors.New("boom")}
	stop, err := openMIDIListener(in, func(msg midi.Message, _ int32) {})
	if err != nil {
		t.Fatalf("openMIDIListener: %v", err)
	}
	stop() // must not panic
	if in.closeCalls != 1 {
		t.Errorf("closeCalls = %d, want 1 even when Close() itself errors", in.closeCalls)
	}
}

func TestOpenMIDIListener_EachCallGetsItsOwnIndependentClose(t *testing.T) {
	// Mirrors midi.FindInPort's real behavior (a brand-new *in wrapper
	// per call, per rtmididrv.Driver.Ins()): two separate
	// openMIDIListener calls against two separate fake ports (simulating
	// two sequential Start()s) must each close ONLY their own port when
	// stopped -- never cross-close the other one.
	in1 := &fakeMIDIIn{}
	stop1, err := openMIDIListener(in1, func(msg midi.Message, _ int32) {})
	if err != nil {
		t.Fatalf("openMIDIListener (1st): %v", err)
	}

	in2 := &fakeMIDIIn{}
	stop2, err := openMIDIListener(in2, func(msg midi.Message, _ int32) {})
	if err != nil {
		t.Fatalf("openMIDIListener (2nd): %v", err)
	}

	stop1()
	if in1.closeCalls != 1 {
		t.Errorf("in1.closeCalls = %d, want 1", in1.closeCalls)
	}
	if in2.closeCalls != 0 {
		t.Errorf("in2.closeCalls = %d, want 0 -- stop1() must not touch the second port", in2.closeCalls)
	}

	stop2()
	if in2.closeCalls != 1 {
		t.Errorf("in2.closeCalls = %d, want 1", in2.closeCalls)
	}
}

// ---------------------------------------------------------------------------
// Engine-level regression: "one MIDI event produces exactly one dispatch
// after a Stop->Start cycle" -- fires the same synthetic event through
// BOTH a stale (pre-Stop) and a fresh (post-Start) captured callback,
// proving the shared per-control debounce state means even a simulated
// listener-leak (CoreMIDI-style fan-out to more than one live callback)
// still nets to exactly one dispatch.
// ---------------------------------------------------------------------------

func midiStopStartDeviceMap() string {
	return `{"knobs":{},"sliders":{},"buttons":{"38":"Temperature"}}`
}

func midiStopStartBindings() string {
	// "property" (no macapp.FrontmostBundleID() gate, see keydebounce_test.go's
	// identical rationale) so this test's outcome does not depend on this
	// machine's frontmost app.
	return `{
		"_config": {"keyDebounceMs": 2000},
		"col": {},
		"edit": {"Temperature": {"type": "property", "property": "CropTop", "sens": 2.0}}
	}`
}

func TestNoteOnDebounce_SurvivesStopStartCycle(t *testing.T) {
	eng := newEngineWithConfig(t, midiStopStartDeviceMap(), midiStopStartBindings())
	eng.sockPath = startFakeSidecar(t)
	stubSidecarLaunch(t)

	// A capturing startMIDIFn stub that records EVERY registered callback
	// across multiple Start() calls (unlike stubMIDICapture, which only
	// ever remembers the latest one) -- this test needs both the stale
	// (first-Start) and fresh (second-Start) callback simultaneously.
	var callbacks []func(msg midi.Message, timestampms int32)
	origStartMIDIFn := startMIDIFn
	t.Cleanup(func() { startMIDIFn = origStartMIDIFn })
	startMIDIFn = func(callback func(msg midi.Message, timestampms int32)) (func(), error) {
		callbacks = append(callbacks, callback)
		return func() {}, nil
	}

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start (1st): %v", err)
	}
	if len(callbacks) != 1 {
		t.Fatalf("callbacks captured after 1st Start = %d, want 1", len(callbacks))
	}
	staleCallback := callbacks[0]

	if err := eng.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start (2nd): %v", err)
	}
	defer eng.Stop()
	if len(callbacks) != 2 {
		t.Fatalf("callbacks captured after 2nd Start = %d, want 2", len(callbacks))
	}
	freshCallback := callbacks[1]

	var logBuf bytes.Buffer
	origLogOutput := log.Writer()
	log.SetOutput(&logBuf)
	t.Cleanup(func() { log.SetOutput(origLogOutput) })

	// Simulate CoreMIDI fanning ONE physical press out to BOTH the stale
	// (should-be-torn-down) and fresh listener -- worst case, matching the
	// live evidence (two note_on deliveries ~1ms apart for one press).
	staleCallback(midi.NoteOn(0, 38, 100), 0)
	freshCallback(midi.NoteOn(0, 38, 100), 0)
	time.Sleep(50 * time.Millisecond) // let any async dispatch finish logging

	got := strings.Count(logBuf.String(), "SUPPRESSED duplicate")
	if got != 1 {
		t.Fatalf(
			"expected exactly 1 SUPPRESSED-duplicate log line for one physical "+
				"press delivered through both a stale and a fresh callback across "+
				"a Stop->Start cycle, got %d\nlog output:\n%s",
			got, logBuf.String(),
		)
	}
}
