package engine

// Hermetic tests for the lazy Resolve sidecar supervisor
// (sidecar_supervisor.go). No python, no Resolve, no MIDI hardware: every
// seam (resolveStateFn, launchSidecarFn, startMIDIFn, the fake unix-socket
// sidecar) is injected.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// lazySidecarHarness wires an engine whose sidecar start is fully lazy
// (startWaitsForFirstSidecarAttempt off, so Start() timing is the real
// production timing) with injectable Resolve state and launch behaviour.
type lazySidecarHarness struct {
	eng      *Engine
	running  atomic.Bool
	front    atomic.Bool
	launches atomic.Int32
	mu       sync.Mutex
	stamps   []time.Time
	failN    atomic.Int32 // fail this many launches, then succeed
}

func newLazySidecarHarness(t *testing.T) *lazySidecarHarness {
	t.Helper()
	h := &lazySidecarHarness{eng: newTestEngine(t)}
	h.eng.sockPath = startFakeSidecar(t)
	stubMIDI(t)

	origWait := startWaitsForFirstSidecarAttempt
	origState := resolveStateFn
	origLaunch := launchSidecarFn
	t.Cleanup(func() {
		startWaitsForFirstSidecarAttempt = origWait
		resolveStateFn = origState
		launchSidecarFn = origLaunch
	})
	startWaitsForFirstSidecarAttempt = false
	resolveStateFn = func() (bool, bool) { return h.running.Load(), h.front.Load() }
	launchSidecarFn = func(string) (func(), string, error) {
		h.launches.Add(1)
		h.mu.Lock()
		h.stamps = append(h.stamps, time.Now())
		h.mu.Unlock()
		if h.failN.Load() > 0 {
			h.failN.Add(-1)
			return nil, "", errors.New("synthetic launch failure")
		}
		return func() {}, "", nil
	}
	return h
}

func (h *lazySidecarHarness) start(t *testing.T) {
	t.Helper()
	begin := time.Now()
	if err := h.eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if d := time.Since(begin); d > 400*time.Millisecond {
		t.Fatalf("Start took %v; must not block on the sidecar", d)
	}
	t.Cleanup(func() { h.eng.Stop() })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func TestLazySidecar_ResolveAbsent_NoLaunch_StartReturnsFast_QuietStatus(t *testing.T) {
	h := newLazySidecarHarness(t) // Resolve neither running nor frontmost
	h.start(t)

	time.Sleep(200 * time.Millisecond) // many supervisor polls at 20ms
	if n := h.launches.Load(); n != 0 {
		t.Fatalf("launchSidecarFn called %d times with Resolve absent; want 0", n)
	}
	st := h.eng.Status()
	if !st.Running {
		t.Fatalf("engine should be running (MIDI drain + non-Resolve controls)")
	}
	if st.SidecarState != SidecarStateWaiting {
		t.Fatalf("SidecarState = %q, want %q", st.SidecarState, SidecarStateWaiting)
	}
	if st.SidecarError != "" || st.SidecarConnected {
		t.Fatalf("waiting must be quiet: connected=%v err=%q", st.SidecarConnected, st.SidecarError)
	}
	if h.eng.getClient() != nil {
		t.Fatalf("client must stay nil while Resolve is absent")
	}
}

func TestLazySidecar_ResolveRunningButNotFrontmost_NoLaunch(t *testing.T) {
	h := newLazySidecarHarness(t)
	h.running.Store(true)
	h.start(t)
	time.Sleep(150 * time.Millisecond)
	if n := h.launches.Load(); n != 0 {
		t.Fatalf("launch attempted %d times while Resolve not frontmost; want 0", n)
	}
	if got := h.eng.Status().SidecarState; got != SidecarStateWaiting {
		t.Fatalf("SidecarState = %q, want waiting", got)
	}
}

func TestLazySidecar_ResolveAppears_LaunchesAndConnects_LateBoundClient(t *testing.T) {
	h := newLazySidecarHarness(t)
	h.start(t)
	time.Sleep(60 * time.Millisecond)
	if h.launches.Load() != 0 || h.eng.getClient() != nil {
		t.Fatalf("precondition: nothing should be launched before Resolve appears")
	}

	h.running.Store(true)
	h.front.Store(true)
	waitFor(t, "sidecar connected", func() bool { return h.eng.Status().SidecarConnected })

	if h.launches.Load() != 1 {
		t.Fatalf("launches = %d, want exactly 1", h.launches.Load())
	}
	st := h.eng.Status()
	if st.SidecarState != SidecarStateConnected || st.SidecarError != "" {
		t.Fatalf("state=%q err=%q, want connected / empty", st.SidecarState, st.SidecarError)
	}
	if h.eng.getClient() == nil {
		t.Fatalf("late-bound client was not published")
	}
}

func TestLazySidecar_FirstAttemptFails_RetriedWithGrowingBackoff(t *testing.T) {
	h := newLazySidecarHarness(t)
	h.failN.Store(3)
	h.running.Store(true)
	h.front.Store(true)
	h.start(t)

	// While failing, the state must say retrying and carry the reason.
	waitFor(t, "retrying state with error", func() bool {
		st := h.eng.Status()
		return st.SidecarState == SidecarStateRetrying && st.SidecarError != ""
	})
	waitFor(t, "eventual connect", func() bool { return h.eng.Status().SidecarConnected })

	if got := h.launches.Load(); got != 4 {
		t.Fatalf("launches = %d, want 4 (3 failures + 1 success)", got)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Backoff ladder with base 10ms / cap 40ms: gaps >= 10, 20, 40.
	want := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond}
	for i, w := range want {
		gap := h.stamps[i+1].Sub(h.stamps[i])
		if gap < w-2*time.Millisecond {
			t.Fatalf("gap before attempt %d = %v, want >= %v", i+2, gap, w)
		}
	}
	if st := h.eng.Status(); st.SidecarError != "" || st.SidecarState != SidecarStateConnected {
		t.Fatalf("after connect: state=%q err=%q", st.SidecarState, st.SidecarError)
	}
}

func TestLazySidecar_ResolveQuits_RetriesStop_BackoffResets(t *testing.T) {
	h := newLazySidecarHarness(t)
	h.failN.Store(1 << 20) // never succeeds
	h.running.Store(true)
	h.front.Store(true)
	h.start(t)

	waitFor(t, "a few failed attempts", func() bool { return h.launches.Load() >= 3 })
	h.running.Store(false)
	h.front.Store(false)
	time.Sleep(60 * time.Millisecond) // let any in-flight attempt finish
	n := h.launches.Load()
	time.Sleep(250 * time.Millisecond) // > several backoff caps
	if got := h.launches.Load(); got != n {
		t.Fatalf("launch attempts continued after Resolve quit: %d -> %d", n, got)
	}
	st := h.eng.Status()
	if st.SidecarState != SidecarStateWaiting || st.SidecarError != "" {
		t.Fatalf("after quit: state=%q err=%q, want waiting / empty", st.SidecarState, st.SidecarError)
	}

	// Resolve returns: the ladder restarts from the base delay, attempts resume.
	h.running.Store(true)
	h.front.Store(true)
	waitFor(t, "attempts resume", func() bool { return h.launches.Load() > n })
}

func TestSidecarBackoffLadder(t *testing.T) {
	origB, origC := sidecarConnectBackoffBase, sidecarConnectBackoffCap
	t.Cleanup(func() { sidecarConnectBackoffBase, sidecarConnectBackoffCap = origB, origC })
	sidecarConnectBackoffBase, sidecarConnectBackoffCap = 2*time.Second, 15*time.Second
	want := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 15 * time.Second, 15 * time.Second}
	for i, w := range want {
		if got := sidecarBackoff(i + 1); got != w {
			t.Fatalf("sidecarBackoff(%d) = %v, want %v", i+1, got, w)
		}
	}
}
