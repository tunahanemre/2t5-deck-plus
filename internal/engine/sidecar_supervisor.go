package engine

// Lazy Resolve sidecar (spec docs/specs/2026-08-30-ld-deck-global-design.md
// section 4.3).
//
// Live failure this closes (2026-11-03): the app started before Resolve,
// Start() launched the sidecar and dialled for ~10s (plus Launch's own 15s
// socket wait), timed out, and NEVER retried, so Resolve control stayed dead
// until the app was restarted. Worse, the deck is a global controller now:
// most of the time Resolve is legitimately not the app in front.
//
// So the sidecar is brought up by ONE supervisor goroutine, off the MIDI /
// input path entirely:
//
//   - Resolve not running, or running but not frontmost -> "waiting". No
//     launch, no error, nothing logged per tick.
//   - Resolve running AND frontmost, no client yet -> attempt launch + dial.
//     A failed attempt tears down what it started (REPLACE, never ADD: see
//     recoverSidecar) and is retried after a backoff of 2s, 4s, 8s, then 15s,
//     for as long as Resolve stays running and frontmost.
//   - Resolve quits -> retries stop and the backoff resets; the next time
//     Resolve is up the ladder starts from 2s again.
//   - A client exists -> the supervisor only publishes SidecarState. A dead
//     connection (HARD transport error, never a timeout: commit 4a1f0ba) is
//     still healed by recoverSidecar, which reconnects the SAME client in
//     place. The two never race: the supervisor does not touch sidecar
//     process state while a client exists, and recoverSidecar only runs on a
//     client the supervisor already published.

import (
	"context"
	"fmt"
	"log"
	"time"

	"2t5-deck-plus/internal/ipc"
	"2t5-deck-plus/internal/macapp"
)

// SidecarState values (Status.SidecarState).
const (
	SidecarStateWaiting    = "waiting"
	SidecarStateConnecting = "connecting"
	SidecarStateConnected  = "connected"
	SidecarStateRetrying   = "retrying"
)

// Package-var seams (tests shrink / replace these; production values below).
var (
	// resolveStateFn reports whether Resolve is running and whether it is
	// the frontmost app. Both calls are cheap (NSWorkspace / one window-list
	// walk) and run once per supervisor poll.
	resolveStateFn = func() (running, frontmost bool) {
		if !macapp.IsResolveRunning() {
			return false, false
		}
		return macapp.BundleIDIsResolve(macapp.FrontmostBundleID()), true
	}

	// sidecarSupervisorPoll is how often the supervisor looks at Resolve.
	sidecarSupervisorPoll = 1 * time.Second

	// sidecarConnectBackoffBase / sidecarConnectBackoffCap define the retry
	// ladder: base, 2*base, 4*base ... capped (2s, 4s, 8s, 15s).
	sidecarConnectBackoffBase = 2 * time.Second
	sidecarConnectBackoffCap  = 15 * time.Second

	// sidecarDialTries / sidecarDialDelay: after Launch reports the socket
	// up, the dial itself is retried briefly (the sidecar can bind a moment
	// after launch on a cold Resolve scripting start).
	sidecarDialTries = 10
	sidecarDialDelay = 500 * time.Millisecond
)

// startWaitsForFirstSidecarAttempt is a TEST-ONLY seam (always false in
// production): when true, Start() blocks until the supervisor's first poll
// has finished, restoring the pre-lazy "Start() returns with the sidecar
// already connected" timing that ~250 older engine tests assume. The
// lazy-sidecar tests turn it off to prove Start() itself never blocks.
var startWaitsForFirstSidecarAttempt = false

// getClient returns the late-bound sidecar client, or nil while none is
// connected. Every Start() closure reads the client through this.
func (e *Engine) getClient() *ipc.Client { return e.clientPtr.Load() }

func (e *Engine) setSidecarState(state, errMsg string) {
	e.mu.Lock()
	e.status.SidecarState = state
	e.status.SidecarError = errMsg
	e.mu.Unlock()
}

// sidecarBackoff returns the wait after the n-th consecutive failed attempt
// (n starts at 1): base, 2*base, 4*base ... capped.
func sidecarBackoff(n int) time.Duration {
	d := sidecarConnectBackoffBase
	for i := 1; i < n; i++ {
		d *= 2
		if d >= sidecarConnectBackoffCap {
			return sidecarConnectBackoffCap
		}
	}
	if d > sidecarConnectBackoffCap {
		return sidecarConnectBackoffCap
	}
	return d
}

// superviseSidecar runs until ctx is cancelled. Started by Start() after the
// MIDI port is open; joined by Stop() via supervisorWG.
func (e *Engine) superviseSidecar(ctx context.Context, firstTickDone chan<- struct{}) {
	defer e.supervisorWG.Done()

	failures := 0
	var nextTry time.Time
	notRunning := 0 // consecutive polls that saw Resolve not running while a client exists

	tick := func() {
		if ctx.Err() != nil {
			return
		}
		if e.getClient() != nil {
			// Resolve quit under a live client (wave E, F5): the sidecar can
			// never serve anything again, and recoverSidecar would keep
			// spawning a Python process every few seconds for as long as the
			// user works in other apps. Drop the client, stop the sidecar and
			// wait for Resolve like at startup. Two polls in a row, so one
			// failed lookup cannot tear a healthy sidecar down.
			if running, _ := resolveStateFn(); !running {
				notRunning++
				if notRunning >= 2 && e.dropSidecar() {
					notRunning, failures, nextTry = 0, 0, time.Time{}
					log.Printf("[sidecar] Resolve is no longer running — sidecar stopped, waiting for Resolve")
					return
				}
			} else {
				notRunning = 0
			}
			// Connected (or healing via recoverSidecar): publish only
			// (plus the held-back highlight restore once the deck is armed).
			e.runPendingHighlightRestore()
			e.mu.Lock()
			if e.status.SidecarConnected {
				e.status.SidecarState = SidecarStateConnected
			} else {
				e.status.SidecarState = SidecarStateRetrying
			}
			e.mu.Unlock()
			failures = 0
			nextTry = time.Time{}
			return
		}

		running, frontmost := resolveStateFn()
		if !running {
			failures = 0
			nextTry = time.Time{}
			e.setSidecarState(SidecarStateWaiting, "")
			return
		}
		if !frontmost {
			// Resolve is up but the user is elsewhere: stay quiet, keep the
			// ladder position, resume when Resolve comes to the front.
			e.setSidecarState(SidecarStateWaiting, "")
			return
		}
		if time.Now().Before(nextTry) {
			return
		}

		state := SidecarStateConnecting
		if failures > 0 {
			state = SidecarStateRetrying
		}
		e.mu.Lock()
		e.status.SidecarState = state
		e.mu.Unlock()

		if err := e.connectSidecarOnce(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			failures++
			wait := sidecarBackoff(failures)
			nextTry = time.Now().Add(wait)
			log.Printf("[sidecar] connect attempt %d failed: %v — retrying in %v while Resolve is up", failures, err, wait)
			e.setSidecarState(SidecarStateRetrying, err.Error())
			return
		}
		failures = 0
		nextTry = time.Time{}
	}

	tick()
	if firstTickDone != nil {
		close(firstTickDone)
	}
	t := time.NewTicker(sidecarSupervisorPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			tick()
		}
	}
}

// dropSidecar tears down the client and the sidecar process after Resolve
// quit and publishes "waiting". It takes the recovery single-flight latch so it
// never overlaps a recoverSidecar goroutine; false means one is mid-flight and
// the caller retries on its next poll.
func (e *Engine) dropSidecar() bool {
	if !e.sidecarRecovering.CompareAndSwap(false, true) {
		return false
	}
	defer e.sidecarRecovering.Store(false)
	client := e.clientPtr.Swap(nil)
	e.mu.Lock()
	stop := e.sidecarStop
	e.sidecarStop = nil
	e.status.SidecarConnected = false
	e.status.SidecarError = ""
	e.status.SidecarState = SidecarStateWaiting
	e.mu.Unlock()
	if client != nil {
		client.Close()
	}
	if stop != nil {
		stop()
	}
	return true
}

// launchCtx is launchSidecarFn that gives up when ctx ends (F6). sidecar.Launch
// itself cannot be interrupted (up to 15 s waiting for the socket on a cold
// start), so it runs in a goroutine; if ctx ends first the caller is released at
// once with ctx.Err(), and the goroutine stops whatever the launch eventually
// started, so nothing is left running and nothing waits for it.
func (e *Engine) launchCtx(ctx context.Context) (stop func(), err error) {
	type result struct {
		stop func()
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		s, _, err := launchSidecarFn(e.resourcesDir)
		ch <- result{s, err}
	}()
	select {
	case r := <-ch:
		return r.stop, r.err
	case <-ctx.Done():
		go func() {
			if r := <-ch; r.err == nil && r.stop != nil {
				r.stop()
			}
		}()
		return nil, ctx.Err()
	}
}

// connectSidecarOnce launches (or reuses) the sidecar, dials it, publishes
// the client and runs restore_highlight once. On any failure it tears down
// whatever it started and returns the error with no client published.
func (e *Engine) connectSidecarOnce(ctx context.Context) error {
	stopSidecar, launchErr := e.launchCtx(ctx)
	if launchErr != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("sidecar launch: %w", launchErr)
	}
	if ctx.Err() != nil {
		stopSidecar()
		return ctx.Err()
	}

	var client *ipc.Client
	var dialErr error
	for i := 0; i < sidecarDialTries; i++ {
		client, dialErr = ipc.Dial(e.sockPath)
		if dialErr == nil {
			break
		}
		select {
		case <-ctx.Done():
			stopSidecar()
			return ctx.Err()
		case <-time.After(sidecarDialDelay):
		}
	}
	if dialErr != nil {
		stopSidecar()
		return fmt.Errorf("sidecar not reachable: %w", dialErr)
	}
	if ctx.Err() != nil {
		client.Close()
		stopSidecar()
		return ctx.Err()
	}

	client.SetGate(e.sidecarGate) // the arm switch (arm.go)
	e.mu.Lock()
	prevStop := e.sidecarStop
	e.sidecarStop = stopSidecar
	e.status.SidecarConnected = true
	e.status.SidecarError = ""
	e.status.SidecarState = SidecarStateConnected
	e.mu.Unlock()
	if prevStop != nil {
		prevStop() // a stale handle from an earlier attempt: replace, never add
	}
	e.clientPtr.Store(client)
	log.Printf("[sidecar] connected")

	// D1 active-clip highlight crash-safety: restore any clip left
	// stray-tinted by a force-killed prior process. Unconditional, once per
	// fresh connect, regardless of the CURRENT highlightEnabled setting
	// (the leftover tint could be from a prior run where it was enabled).
	if !e.writesAllowed() {
		e.highlightRestorePending.Store(true)
		log.Printf("[highlight] restore_highlight held back: the deck is disarmed; it runs when the deck is armed")
		return nil
	}
	e.restoreHighlight(client)
	return nil
}

// restoreHighlight sends restore_highlight once and logs the outcome.
func (e *Engine) restoreHighlight(client *ipc.Client) {
	if resp, err := client.Send(map[string]any{"op": "restore_highlight"}); err != nil {
		log.Printf("[highlight] restore_highlight send error: %v", err)
	} else if ok, _ := resp["ok"].(bool); !ok {
		log.Printf("[highlight] restore_highlight: %v", resp["err"])
	}
}

// runPendingHighlightRestore runs the connect-time restore that was held back
// while disarmed, as soon as the deck is armed. Called by the supervisor poll.
func (e *Engine) runPendingHighlightRestore() {
	if !e.highlightRestorePending.Load() || !e.writesAllowed() {
		return
	}
	client := e.getClient()
	if client == nil || !e.highlightRestorePending.CompareAndSwap(true, false) {
		return
	}
	log.Printf("[highlight] deck armed: running the held-back restore_highlight")
	e.restoreHighlight(client)
}
