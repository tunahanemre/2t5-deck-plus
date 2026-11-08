package engine

// Regression coverage for the 2026-08-12 C1/Space "double-fire" root cause:
// the MIDI callback goroutine (registered via startMIDIFn -- see Start())
// must NEVER block on an IPC round trip to the sidecar. It used to,
// indirectly: queryPage() called queryStatus() directly, and on a cache
// miss (statusCacheTTL, 200ms) queryStatus() performed a synchronous,
// mutex-serialised client.Send -- expensive on a large timeline (timecode,
// full clip map, per-clip offline checks). Live evidence: an 8.3 SECOND
// stall inside one such call. CoreMIDI queues note events while its
// callback is blocked; when the call finally returned, every queued press
// flushed within ~1ms of each other, so a single physical Space press
// became two -- play+pause, cancelling out.
//
// The fix (engine.go): queryPage() now only ever PEEKS the last-known
// statusCache value (statusCacheMu-guarded), even when stale/expired --
// never triggers a fetch. The ticker/drain goroutine (50ms tick) is the
// ONLY thing that ever populates the cache via queryStatus(). A one-time
// synchronous priming fetch happens in Start(), before the MIDI port
// opens (same goroutine as the Start() caller, never the callback), so a
// press in the first tick's ~50ms window still resolves against a known
// page rather than "".
//
// This test proves the CONTRACT end to end, through the real Start()/
// MIDI-callback path (not just a unit test of queryPage in isolation):
// with a fake sidecar whose "status" op is slow -- simulating a large,
// currently-open timeline -- a note_on fired while a status round trip is
// legitimately in flight must still dispatch promptly.

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"

	"2t5-deck-plus/internal/ipc"
)

// startSlowStatusSidecar spins up a fake unix-socket sidecar that answers
// the FIRST "status" op instantly -- so Start()'s one-time priming fetch
// (see engine.go, just before the MIDI port opens) completes fast and
// this test doesn't pay a multi-second Start() cost -- then sleeps `delay`
// before answering every SUBSEQUENT "status" op, simulating a sidecar that
// is slow once a real (large) timeline is open, same shape as the live
// 2026-08-12 incident. Every non-"status" op always replies instantly.
func startSlowStatusSidecar(t *testing.T, page string, delay time.Duration) (sockPath string) {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-slow-*.sock")
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

	var statusCalls int32

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
					if op == "status" {
						if atomic.AddInt32(&statusCalls, 1) > 1 {
							time.Sleep(delay)
						}
						reply, _ = json.Marshal(map[string]any{"ok": true, "page": page})
					} else {
						reply, _ = json.Marshal(map[string]any{"ok": true})
					}
					if err := ipc.WriteFrame(c, reply); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return sockPath
}

// TestNoteOnDispatchesPromptly_WhenSidecarStatusIsSlow is the end-to-end
// regression guard: a note_on fired while the sidecar's "status" op is
// mid-flight (slow) must still dispatch essentially immediately, never
// waiting on that call. Uses a "layer" switch control (L1/L2) because its
// resolution -- resolveLayerSwitch(cfg, queryPage(), control) -- calls
// queryPage() and is otherwise pure local state (no client.Send, no
// CGEvent/frontmost dependency), so the timing measured is queryPage's
// alone, and the resulting layer change is directly observable via
// Status().LastAction as proof the press genuinely dispatched rather than
// returning fast because it silently no-op'ed.
func TestNoteOnDispatchesPromptly_WhenSidecarStatusIsSlow(t *testing.T) {
	deviceMap := `{"knobs":{},"sliders":{},"buttons":{"40":"L1","41":"L2"}}`
	bindings := `{
		"col": {
			"L1": {"type":"layer","layer":1},
			"L2": {"type":"layer","layer":2}
		}
	}`
	eng := newEngineWithConfig(t, deviceMap, bindings)
	stubStateFile(t)

	const slowDelay = 1 * time.Second
	sockPath := startSlowStatusSidecar(t, "color", slowDelay)
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()
	cb := *cbPtr

	// Let the ticker's SECOND status call -- the first SLOW one -- begin,
	// and let the ~200ms status-cache TTL lapse well past that, so the
	// cache is genuinely stale AND a status round trip is provably
	// in-flight/blocked on the drain goroutine at the moment of the press
	// below. Matches the live incident's exact conditions.
	time.Sleep(300 * time.Millisecond)

	start := time.Now()
	cb(midi.NoteOn(0, 41, 100), 0) // L2 press
	elapsed := time.Since(start)

	// Generous relative to a real dispatch (pure in-memory work, no I/O,
	// microseconds in practice) and to CI scheduling jitter, but two
	// orders of magnitude below slowDelay -- if queryPage() (directly, or
	// indirectly via any call it makes) ever blocked on the sidecar again,
	// this would fail long before slowDelay elapses.
	const promptBound = 150 * time.Millisecond
	if elapsed > promptBound {
		t.Fatalf("note_on dispatch took %v while a sidecar status round trip was mid-flight (slow) -- "+
			"the MIDI callback goroutine must never block on it (2026-08-12 root cause: an 8.3s stall "+
			"here queued a burst of Loupedeck presses that all flushed within ~1ms once the call finally "+
			"returned, turning one Space press into a play+pause pair that cancelled out)", elapsed)
	}

	// Prove the press genuinely dispatched (not just "returned fast
	// because nothing happened") -- resolveLayerSwitch runs entirely off
	// queryPage()'s (possibly stale) cached page, so a real layer switch
	// during the slow window is the whole point.
	if got := eng.Status().LastAction; got != "Layer 2" {
		t.Errorf("LastAction = %q, want %q -- the press must have actually dispatched, not silently no-op'ed", got, "Layer 2")
	}
}
