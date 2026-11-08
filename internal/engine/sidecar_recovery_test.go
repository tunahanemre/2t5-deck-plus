package engine

// Tests for BUG 3 (2026-08-12 live report): the sidecar process dies
// whenever DaVinci Resolve is restarted, and never comes back until the
// whole app is relaunched -- confirmed live (Resolve's PID newer than the
// app's, sidecar process gone, an engine restart did NOT respawn it). The
// July self-heal (ResolveDriver._ensure_connected, sidecar-side) covers a
// dead Resolve-scripting CONNECTION from an otherwise-alive sidecar
// process; it does nothing for the sidecar PROCESS itself being gone,
// since nothing on the Go side ever re-dials once Start()'s initial dial
// has succeeded -- queryStatus's error branch (engine.go) used to just
// zero out the cached status and try again next tick, forever, with
// Status().SidecarConnected/SidecarError left stale at whatever they were
// set to at Start() time.
//
// Fix: recoverSidecar (engine.go), triggered from queryStatus's error
// branch. Single-flighted (sidecarRecovering) so the 50ms drain tick /
// 200ms status-cache TTL doesn't spawn a launch attempt per tick;
// backed off (sidecarRecoveryBackoff, lastSidecarRecoveryAttempt) so a
// fast-failing launch (e.g. python3 discovery itself broken) doesn't
// hammer DiscoverPython3 every tick either. Calls launchSidecarFn again
// (same seam Start() itself uses) -- which is itself idempotent by
// design (internal/sidecar.Launch dials-before-spawning, reusing a
// still-alive process if the failure was a transient blip rather than a
// genuine death) -- then Reconnect()s the EXISTING *ipc.Client in place
// (see internal/ipc/client_test.go) so every one of the dozens of call
// sites across this file that already hold that pointer keeps working
// with zero changes. Status().SidecarConnected/SidecarError are updated
// on both success and failure, so the existing frontend wiring
// (app/frontend/src/main.js's statSidecarEl/sidecarErrorEl, already fed
// by these exact fields) reflects a dead-then-recovered sidecar live,
// not just at Start() time.

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"2t5-deck-plus/internal/ipc"
)

var errFakeLaunch = errors.New("simulated launch failure")

// startKillableFakeSidecar is startFakeSidecar's cousin: it also tracks
// every accepted connection so kill() can simulate a REAL process death --
// not just "stop accepting new connections" (closing only the listener)
// but "every connection this process ever held goes away too" (closing
// every already-accepted conn), which is what actually happens to
// e.client's live connection when the real sidecar.py process is killed
// out from under it.
func startKillableFakeSidecar(t *testing.T, page string) (sockPath string, kill func()) {
	t.Helper()
	f, err := os.CreateTemp("", "ldr-recov-*.sock")
	if err != nil {
		t.Fatalf("fake sidecar tempname: %v", err)
	}
	sockPath = f.Name()
	f.Close()
	os.Remove(sockPath)
	t.Cleanup(func() { os.Remove(sockPath) })

	return sockPath, startKillableFakeSidecarAt(t, sockPath, page)
}

// startKillableFakeSidecarAt is startKillableFakeSidecar with an EXPLICIT
// path, so a "respawn" stub (simulating launchSidecarFn recovering a dead
// process) can rebind at the exact SAME path the engine is already
// configured to dial -- matching sidecar.py's real behavior (always binds
// SOCK, a fixed path, unlinking any stale file first) rather than an
// unrelated fresh random path a real respawn would never produce.
func startKillableFakeSidecarAt(t *testing.T, sockPath, page string) (kill func()) {
	t.Helper()
	os.Remove(sockPath) // mirrors sidecar.py main()'s own os.unlink(SOCK) before bind
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("fake sidecar listen at %s: %v", sockPath, err)
	}

	var mu sync.Mutex
	var conns []net.Conn
	killed := false

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			go func(c net.Conn) {
				defer c.Close()
				for {
					body, err := ipc.ReadFrame(c)
					if err != nil {
						return
					}
					var m map[string]any
					_ = json.Unmarshal(body, &m)
					var reply []byte
					if op, _ := m["op"].(string); op == "status" {
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

	kill = func() {
		mu.Lock()
		defer mu.Unlock()
		if killed {
			return
		}
		killed = true
		ln.Close()
		for _, c := range conns {
			c.Close()
		}
	}
	t.Cleanup(kill)
	return kill
}

func waitForCondition(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("condition never became true within %s", timeout)
	}
}

func TestSidecarRecovery_RespawnsAndReconnectsAfterProcessDeath(t *testing.T) {
	eng := newTestEngine(t)
	sockPath, kill1 := startKillableFakeSidecar(t, "edit")
	eng.sockPath = sockPath

	var launchCalls int32
	origLaunch := launchSidecarFn
	t.Cleanup(func() { launchSidecarFn = origLaunch })
	launchSidecarFn = func(string) (func(), string, error) {
		n := atomic.AddInt32(&launchCalls, 1)
		if n == 1 {
			// Start()'s own launch call: sidecar #1 is already listening
			// for real (started above) -- nothing to spawn.
			return func() {}, sockPath, nil
		}
		// A recovery attempt: simulate sidecar.Launch respawning a dead
		// process by starting a brand-new fake sidecar listening at the
		// SAME path (matches the real invariant: sidecar.Launch always
		// (re)listens at sidecar.SockPath, which the engine always dials
		// via its own e.sockPath -- see Start()'s dial-after-launch
		// comment for why these must agree, and recoverSidecar's doc
		// comment for why it follows the same convention).
		kill2 := startKillableFakeSidecarAt(t, sockPath, "edit")
		return kill2, sockPath, nil
	}
	stubMIDI(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	if !eng.Status().SidecarConnected {
		t.Fatal("Status().SidecarConnected = false immediately after Start")
	}

	kill1() // simulate the sidecar process dying (e.g. a Resolve restart)

	// Wait for recovery to actually TRIGGER (the launchCalls counter,
	// not Status().SidecarConnected -- SidecarConnected starts true from
	// Start() and the respawn here is set up synchronously/fast enough
	// that polling for it to first flip false would be racy by
	// construction and could miss a genuinely-instant recovery entirely;
	// TestSidecarRecovery_LaunchFailure_ReportsErrorAndKeepsRetrying below
	// is what actually proves SidecarConnected/SidecarError reflect a
	// sustained outage). This test's job is just: the engine must recover
	// on its own, without a restart, at all.
	waitForCondition(t, 5*time.Second, func() bool {
		return atomic.LoadInt32(&launchCalls) >= 2
	})

	waitForCondition(t, 5*time.Second, func() bool {
		return eng.Status().SidecarConnected
	})

	got := eng.Status()
	if got.SidecarError != "" {
		t.Errorf("Status().SidecarError = %q after successful recovery, want empty", got.SidecarError)
	}
	if n := atomic.LoadInt32(&launchCalls); n < 2 {
		t.Errorf("launchSidecarFn called %d times, want >= 2 (Start's own call + at least one recovery attempt)", n)
	}
}

func TestSidecarRecovery_IsSingleFlighted(t *testing.T) {
	eng := newTestEngine(t)
	sockPath, kill1 := startKillableFakeSidecar(t, "edit")
	eng.sockPath = sockPath

	var launchCalls int32
	launchGate := make(chan struct{}) // blocks every launch after the first until the test releases it
	origLaunch := launchSidecarFn
	t.Cleanup(func() { launchSidecarFn = origLaunch })
	launchSidecarFn = func(string) (func(), string, error) {
		n := atomic.AddInt32(&launchCalls, 1)
		if n == 1 {
			return func() {}, sockPath, nil
		}
		<-launchGate // hold every recovery attempt open, simulating a slow respawn
		kill2 := startKillableFakeSidecarAt(t, sockPath, "edit")
		return kill2, sockPath, nil
	}
	stubMIDI(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	kill1()

	// Many status ticks (50ms cadence) will observe the dead connection
	// while the first recovery attempt sits blocked on launchGate -- none
	// of them may start a SECOND concurrent launch attempt.
	time.Sleep(400 * time.Millisecond)
	close(launchGate)

	waitForCondition(t, 5*time.Second, func() bool {
		return eng.Status().SidecarConnected
	})

	if n := atomic.LoadInt32(&launchCalls); n != 2 {
		t.Errorf("launchSidecarFn called %d times while blocked, want exactly 2 "+
			"(Start's own call + exactly ONE recovery attempt in flight at a time)", n)
	}
}

// ---------------------------------------------------------------------------
// 2026-08-13 fix: "duplicate sidecar feedback loop" live incident.
// engine.go's queryStatus used to treat ANY client.Send failure -- including
// a mere ipc.ErrTimeout -- as "the sidecar died," triggering recoverSidecar,
// which (before this fix) ALSO never killed the previous sidecar before
// spawning a replacement. Together this meant a single slow "status" round
// trip (DaVinci Resolve's scripting API going briefly unresponsive during
// playback -- reliably reproduced on a very large timeline)
// spawned a duplicate sidecar.py process on top of the still-alive original
// every time the user pressed Space. Live evidence: three sidecar.py
// processes found running simultaneously.
// ---------------------------------------------------------------------------

// startHangingFakeSidecar listens at sockPath and accepts connections but
// never reads/writes/closes anything -- every request the engine sends
// times out at ipc.SendTimeout instead of ever getting a reply. Simulates a
// sidecar that is alive (accepting the connection) but wedged/busy
// (DaVinci Resolve's scripting API unresponsive), as opposed to
// startKillableFakeSidecar's kill(), which simulates a genuinely dead
// process (closed connections -> a hard, non-timeout error).
func startHangingFakeSidecar(t *testing.T, sockPath string) {
	t.Helper()
	os.Remove(sockPath)
	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("fake hanging sidecar listen at %s: %v", sockPath, err)
	}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn // deliberately never touched again -- just held open
		}
	}()
	t.Cleanup(func() { ln.Close() })
}

// ---------------------------------------------------------------------------
// 2026-08-25 fix: THIRD attempt at this bug class. The 2026-08-13 remedy
// (maxConsecutiveStatusTimeouts, a COUNT of consecutive ipc.ErrTimeout
// results tolerated before escalating -- ~15s worth at the real 5s
// SendTimeout) was itself the bug: its own doc comment reasoned that was
// "generous relative to a normal Resolve-playback blip", but playback is
// NOT a blip -- it lasts as long as the user plays, with no upper bound, so
// any fixed count/threshold eventually always loses.
//
// The 2026-08-24 remedy for THAT (pingAliveFn / internal/ipc.PingAlive,
// probing sidecar.py's dedicated PING_SOCK on its own thread) was tried and
// defeated the very next day: CPython's GIL is process-wide, so a second
// socket served by a second thread in the SAME sidecar.py process cannot
// answer either while the main thread blocks inside fusionscript.so
// (Resolve's C extension, which does not release the GIL) servicing
// "status" -- the exact scenario the probe existed to discriminate. Live
// evidence: 8 spurious "status timed out and the dedicated ping probe also
// failed" recoveries in one playback session. PING_SOCK/PingAlive are
// REMOVED entirely (internal/ipc/ping.go, sidecar.py's PING_SOCK) -- not
// repurposed, not retuned; every case they were ever consulted in was
// exactly the case a same-process probe cannot answer.
//
// The fix this time changes the RULE, not the mechanism: a "status" timeout
// (ipc.ErrTimeout) NEVER triggers recovery, full stop, regardless of count
// or duration -- serve the last-known snapshot marked stale and keep
// polling. Only a HARD transport error (anything that is NOT ipc.ErrTimeout
// -- EOF, broken pipe, connection refused, ErrNotConnected) escalates to
// recoverSidecar, and does so immediately, no threshold. The two tests below
// are the required regression coverage for that contract; see engine.go's
// Engine struct field comment (the block that used to be
// consecutiveStatusTimeouts) for the full three-attempt history.
// ---------------------------------------------------------------------------

// TestSidecarRecovery_TimeoutsNeverEscalate_RegardlessOfDuration proves
// requirement (1): however many consecutive "status" timeouts occur, and
// for however long, recovery must never fire -- and requirement (3): the
// existing busy-but-alive/stale HUD behaviour still holds (SidecarConnected
// stays true, SidecarStale flips true). Unlike its 2026-08-24 predecessor,
// this needs NO ping listener at all -- a bare timeout is now sufficient on
// its own to mean "busy, not dead."
func TestSidecarRecovery_TimeoutsNeverEscalate_RegardlessOfDuration(t *testing.T) {
	eng := newTestEngine(t)

	f, err := os.CreateTemp("", "ldr-recov-busy-*.sock")
	if err != nil {
		t.Fatalf("tempname: %v", err)
	}
	sockPath := f.Name()
	f.Close()
	os.Remove(sockPath)
	t.Cleanup(func() { os.Remove(sockPath) })
	eng.sockPath = sockPath

	// Main ops socket: alive (accepts) but never replies -- every "status"
	// call times out, exactly like DaVinci Resolve's scripting API going
	// unresponsive mid-playback (and, per the 2026-08-25 root-cause finding,
	// exactly the window in which a same-process ping thread would ALSO be
	// unable to answer -- this test deliberately has nothing standing in
	// for one, because the fix no longer consults one).
	startHangingFakeSidecar(t, sockPath)

	var launchCalls int32
	origLaunch := launchSidecarFn
	t.Cleanup(func() { launchSidecarFn = origLaunch })
	launchSidecarFn = func(string) (func(), string, error) {
		atomic.AddInt32(&launchCalls, 1)
		// Every call (Start's own, and any recovery attempt -- there must
		// not be one) reuses the same hanging fake; the counter alone is
		// the signal.
		return func() {}, sockPath, nil
	}

	origSendTimeout := ipc.SendTimeout
	ipc.SendTimeout = 20 * time.Millisecond
	t.Cleanup(func() { ipc.SendTimeout = origSendTimeout })

	stubMIDI(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	// statusCacheTTL is fixed at 200ms -- 3s here is ~15 status-tick
	// cycles, more than the OLD counter mechanism's entire worst-case floor
	// (3 timeouts * real 5s SendTimeout = ~15s) would have tolerated even
	// once, and this run holds the busy state continuously the whole time.
	// The live bug this fixes was specifically that ANY mechanism gated on
	// count/duration/a probe ALWAYS loses eventually against an unbounded
	// playback -- so the point of this test is duration, not just "some
	// timeouts happened": recovery must not fire even once, no matter how
	// long the timeouts persist.
	time.Sleep(3 * time.Second)

	if n := atomic.LoadInt32(&launchCalls); n != 1 {
		t.Fatalf("launchSidecarFn called %d times over 3s of consecutive status timeouts, want exactly 1 "+
			"(Start's own call only) -- a timeout must NEVER be treated as dead, no matter how many "+
			"consecutive status timeouts occur or how long they persist (this is the live bug across all "+
			"three attempts: something eventually killed a perfectly healthy, merely-busy sidecar)", n)
	}
	if !eng.Status().SidecarConnected {
		t.Error("Status().SidecarConnected = false after a sustained busy period -- recovery must never " +
			"have run (which owns that transition), so this must still read true from Start()")
	}
	if !eng.Status().SidecarStale {
		t.Error("Status().SidecarStale = false after status timeouts, want true -- the HUD needs to know " +
			"these are carried-forward numbers, not fresh ones, to show the stale cue instead of either " +
			"blanking or presenting them as live")
	}
}

// TestSidecarRecovery_HardTransportError_EscalatesPromptly proves
// requirement (2): a HARD transport error (here, the peer closing every
// connection it holds -- exactly what happens to e.client's live connection
// when the real sidecar.py process is killed) escalates to recoverSidecar
// promptly, with no threshold to wait out -- unlike a timeout (the test
// above), which must never escalate at all. Reuses
// startKillableFakeSidecar, the same real-death simulation
// TestSidecarRecovery_RespawnsAndReconnectsAfterProcessDeath already relies
// on, so this is explicitly a companion contrast test: same trigger
// mechanism, this one's job is asserting speed, not just "eventually
// recovers."
func TestSidecarRecovery_HardTransportError_EscalatesPromptly(t *testing.T) {
	eng := newTestEngine(t)
	sockPath, kill1 := startKillableFakeSidecar(t, "edit")
	eng.sockPath = sockPath

	var launchCalls int32
	origLaunch := launchSidecarFn
	t.Cleanup(func() { launchSidecarFn = origLaunch })
	launchSidecarFn = func(string) (func(), string, error) {
		n := atomic.AddInt32(&launchCalls, 1)
		if n == 1 {
			return func() {}, sockPath, nil
		}
		kill2 := startKillableFakeSidecarAt(t, sockPath, "edit")
		return kill2, sockPath, nil
	}

	origSendTimeout := ipc.SendTimeout
	ipc.SendTimeout = 20 * time.Millisecond
	t.Cleanup(func() { ipc.SendTimeout = origSendTimeout })

	stubMIDI(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	kill1() // a genuine peer close -- EOF/broken pipe, NOT a timeout

	// A hard transport error needs no threshold wait at all: the OS reports
	// the closed connection immediately on the next read, so recovery fires
	// on THIS tick, not after any SendTimeout. 1s is generous against the
	// 50ms drain tick and light-years faster than the old counter-based
	// ~15s floor or the ping-based ~7s (SendTimeout+PingTimeout) floor --
	// neither of those mechanisms is consulted at all for this signal.
	waitForCondition(t, 1*time.Second, func() bool {
		return atomic.LoadInt32(&launchCalls) >= 2
	})
}

func TestSidecarRecovery_KillsPreviousSidecarBeforeSpawningReplacement(t *testing.T) {
	eng := newTestEngine(t)
	sockPath, kill1 := startKillableFakeSidecar(t, "edit")
	eng.sockPath = sockPath

	var firstStopCalled atomic.Bool
	var launchCalls int32
	origLaunch := launchSidecarFn
	t.Cleanup(func() { launchSidecarFn = origLaunch })
	launchSidecarFn = func(string) (func(), string, error) {
		n := atomic.AddInt32(&launchCalls, 1)
		if n == 1 {
			// Start()'s own launch call: the returned stop() is exactly
			// what e.sidecarStop will hold once Start() returns -- wrap it
			// so the recovery attempt below can assert it was actually
			// invoked BEFORE a replacement was spawned.
			return func() { firstStopCalled.Store(true) }, sockPath, nil
		}
		// A recovery attempt: by the time launchSidecarFn is called again,
		// recoverSidecar must have ALREADY killed whatever the previous
		// attempt's stop() was -- REPLACE, never ADD (see recoverSidecar's
		// doc comment). Failing this is exactly the 2026-08-13 "duplicate
		// sidecar feedback loop" bug: a second process spawned on top of
		// a first one nothing ever told to die.
		if !firstStopCalled.Load() {
			t.Errorf("launchSidecarFn (recovery attempt #%d) was called BEFORE the previous sidecar's "+
				"stop() -- a spawn must REPLACE the previous sidecar, never ADD a second one", n)
		}
		kill2 := startKillableFakeSidecarAt(t, sockPath, "edit")
		return kill2, sockPath, nil
	}
	stubMIDI(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	kill1() // simulate the sidecar process dying (e.g. a Resolve restart) -- a HARD failure, escalates immediately

	waitForCondition(t, 5*time.Second, func() bool {
		return atomic.LoadInt32(&launchCalls) >= 2
	})
	if !firstStopCalled.Load() {
		t.Error("recovery completed a second launchSidecarFn call without ever calling the first sidecar's stop()")
	}
}

func TestSidecarRecovery_LaunchFailure_ReportsErrorAndKeepsRetrying(t *testing.T) {
	eng := newTestEngine(t)
	sockPath, kill1 := startKillableFakeSidecar(t, "edit")
	eng.sockPath = sockPath

	var launchCalls int32
	origLaunch := launchSidecarFn
	t.Cleanup(func() { launchSidecarFn = origLaunch })
	launchSidecarFn = func(string) (func(), string, error) {
		n := atomic.AddInt32(&launchCalls, 1)
		if n == 1 {
			return func() {}, sockPath, nil
		}
		if n < 4 {
			// First couple of recovery attempts fail outright (e.g.
			// DiscoverPython3 itself broken this round).
			return nil, "", errFakeLaunch
		}
		kill2 := startKillableFakeSidecarAt(t, sockPath, "edit")
		return kill2, sockPath, nil
	}
	origBackoff := sidecarRecoveryBackoff
	sidecarRecoveryBackoff = 10 * time.Millisecond // don't wait out the real default in a test
	t.Cleanup(func() { sidecarRecoveryBackoff = origBackoff })
	stubMIDI(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()

	kill1()

	waitForCondition(t, 2*time.Second, func() bool {
		return eng.Status().SidecarError != ""
	})
	if got := eng.Status(); got.SidecarConnected {
		t.Errorf("Status().SidecarConnected = true while every launch attempt is failing")
	}

	// Recovery must keep retrying (not give up permanently after the
	// first failure) and eventually succeed once launchSidecarFn does.
	waitForCondition(t, 5*time.Second, func() bool {
		return eng.Status().SidecarConnected
	})
	if got := eng.Status(); got.SidecarError != "" {
		t.Errorf("Status().SidecarError = %q after eventual recovery, want empty", got.SidecarError)
	}
}
