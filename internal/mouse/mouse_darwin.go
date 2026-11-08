package mouse

/*
#cgo LDFLAGS: -framework CoreGraphics

#include <CoreGraphics/CoreGraphics.h>
#include <stdlib.h>
#include <stdint.h>

// cursorPos returns the current mouse cursor position in global display coordinates.
static CGPoint cursorPos(void) {
    CGEventRef e = CGEventCreate(NULL);
    CGPoint p = CGEventGetLocation(e);
    CFRelease(e);
    return p;
}

// postMouseEvent posts a mouse event at the given position.
static void postMouseEvent(CGEventType type, CGMouseButton btn, CGFloat x, CGFloat y) {
    CGPoint pt = CGPointMake(x, y);
    CGEventRef e = CGEventCreateMouseEvent(NULL, type, pt, btn);
    CGEventPost(kCGHIDEventTap, e);
    CFRelease(e);
}

// postScrollEvent posts a single-wheel (vertical) line-unit scroll-wheel
// event carrying the given signed line delta AND the given CGEventFlags
// modifier bitmask SET DIRECTLY ON THE EVENT ITSELF -- see Scroll's Go doc
// comment below (2026-08-13) for why an explicit per-event flags bitmask,
// not ambient synthetic modifier state, is the mechanism that actually gets
// honoured by macOS for a real target application like DaVinci Resolve.
// There is no CGEventCreateScrollWheelEvent variant that takes flags
// directly, so they are set via CGEventSetFlags on the created event before
// posting, same two-step shape as keys_darwin.go's postKeyDown/postKey.
static void postScrollEvent(int32_t delta, CGEventFlags flags) {
    CGEventRef e = CGEventCreateScrollWheelEvent(NULL, kCGScrollEventUnitLine, 1, delta);
    CGEventSetFlags(e, flags);
    CGEventPost(kCGHIDEventTap, e);
    CFRelease(e);
}
*/
import "C"

import (
	"fmt"
	"sync"
	"sync/atomic"

	"2t5-deck-plus/internal/deckenv"
)

// ---------------------------------------------------------------------------
// Arm guard — SAFETY CRITICAL.
//
// Every function below that posts a real CGEvent MUST check Armed() first
// and no-op if false. Default (unarmed) is the safe state: nothing is ever
// posted to the system. A process only becomes armed if the
// DECKPLUS_ARMED=1 environment variable was set before this package's
// first use, or Arm() was called explicitly.
//
// `go test` never sets DECKPLUS_ARMED, so this package is a guaranteed
// no-op under test regardless of which code path a test exercises — this is
// what makes it IMPOSSIBLE (not just unlikely) for `go test` to move the
// user's real cursor or click on their real desktop.
//
// Only the real application entrypoint (cmd/2t5-deck-plus/main.go's live run
// path, and later the Wails app) should ever call Arm(). Never call it from
// a test or from the --calibrate path (which only reads the cursor via
// CursorPos — a read-only call that is not gated and needs no arming).
// ---------------------------------------------------------------------------

var (
	armed       atomic.Bool
	paused      atomic.Bool // the editor's safety switch (SetPaused): see canPost
	armInitOnce sync.Once
	noopMu      sync.Mutex
	noopCalls   []string
)

// envArmed reports whether the DECKPLUS_ARMED env var requests arming.
// Pulled out as a pure function (no global state touched) so its mapping
// logic can be unit tested in isolation via t.Setenv, without ever flipping
// the real `armed` flag or calling a CGEvent-posting function.
func envArmed() bool {
	return deckenv.Get("ARMED") == "1"
}

func ensureArmInit() {
	armInitOnce.Do(func() {
		if envArmed() {
			armed.Store(true)
		}
	})
}

// Armed reports whether this process is currently armed to post real mouse
// input via CGEvent. False (the default) means every posting function in
// this package is a safe no-op that touches nothing on the system.
func Armed() bool {
	ensureArmInit()
	return armed.Load()
}

// Arm explicitly arms this process to post real mouse input via CGEvent.
// Idempotent. Intended to be called exactly once, by the real application's
// startup path, before the control loop begins.
func Arm() {
	ensureArmInit()
	armed.Store(true)
}

// SetPaused is the in-app safety switch (wave E, F7): while paused, every
// posting function of this package is a recorded no-op even in a process that
// is armed (Armed), exactly as if it were unarmed. Memory only, off by
// default; it can only ever make the package quieter, never arm it.
func SetPaused(p bool) { paused.Store(p) }

// Paused reports whether the safety switch is holding input back.
func Paused() bool { return paused.Load() }

// shouldPost is the one rule: post only when armed and not paused.
func shouldPost(armed, paused bool) bool { return armed && !paused }

// canPost is the guard every posting function checks.
func canPost() bool { return shouldPost(Armed(), paused.Load()) }

// CanPost reports whether posting is allowed right now (armed and not paused):
// what a caller asks to learn "would this click go out" without posting it.
func CanPost() bool { return canPost() }

// buttonLatch remembers that a Press really posted a button-down, so the
// matching Release always goes out even if the arm switch was flipped between
// the two: a posted down without its up leaves the button stuck down in the
// target app (wave G, B6). A Press that was suppressed latches nothing, so its
// Release stays suppressed too. Pure, so it is tested without CGEvent.
type buttonLatch struct{ down atomic.Bool }

// press says whether to post the down, and latches it when it is posted.
func (l *buttonLatch) press(can bool) bool {
	if can {
		l.down.Store(true)
	}
	return can
}

// release says whether to post the up: always when a posted down is latched.
func (l *buttonLatch) release(can bool) bool {
	return l.down.Swap(false) || can
}

var leftButton buttonLatch

// recordNoop appends a call description to the no-op call log. Used so
// tests can assert the no-op path was taken (no real CGEvent posted)
// without needing a live display or Accessibility permission.
func recordNoop(name string, args ...float64) {
	noopMu.Lock()
	defer noopMu.Unlock()
	noopCalls = append(noopCalls, fmt.Sprintf("%s%v", name, args))
}

// NoopCalls returns a snapshot of every call that was suppressed (no-op'd)
// because the package was unarmed. Test-only visibility hook.
func NoopCalls() []string {
	noopMu.Lock()
	defer noopMu.Unlock()
	out := make([]string, len(noopCalls))
	copy(out, noopCalls)
	return out
}

// ResetNoopCalls clears the recorded no-op call log. Test-only.
func ResetNoopCalls() {
	noopMu.Lock()
	defer noopMu.Unlock()
	noopCalls = nil
}

// CursorPos returns the current mouse cursor position in global display
// coordinates (top-left origin), matching the coordinate space used by Drag.
//
// CursorPos is READ-ONLY — it never posts an event to the system, so it is
// NOT gated by Armed() and is safe to call from --calibrate or tests.
func CursorPos() (float64, float64) {
	p := C.cursorPos()
	return float64(p.x), float64(p.y)
}

// Drag performs a left-mouse-button drag from (x1,y1) to (x2,y2).
//
// Posts three events in sequence:
//  1. kCGEventLeftMouseDown at (x1,y1)
//  2. kCGEventLeftMouseDragged to (x2,y2)
//  3. kCGEventLeftMouseUp at (x2,y2)
//
// Use this for atomic one-shot drags (master bars, temp/tint sliders).
// For balance sliders use Press/DragTo/Release to hold the button across ticks.
//
// Coordinates are global display points (top-left origin).
// Requires Accessibility permission (System Settings → Privacy & Security →
// Accessibility); without it the events are posted but have no effect.
//
// No-ops (posts nothing) unless Armed() — see the arm-guard docs above.
func Drag(x1, y1, x2, y2 float64) {
	if !canPost() {
		recordNoop("Drag", x1, y1, x2, y2)
		return
	}
	C.postMouseEvent(C.kCGEventLeftMouseDown, C.kCGMouseButtonLeft,
		C.CGFloat(x1), C.CGFloat(y1))
	C.postMouseEvent(C.kCGEventLeftMouseDragged, C.kCGMouseButtonLeft,
		C.CGFloat(x2), C.CGFloat(y2))
	C.postMouseEvent(C.kCGEventLeftMouseUp, C.kCGMouseButtonLeft,
		C.CGFloat(x2), C.CGFloat(y2))
}

// Press posts a left-mouse-button DOWN at (x,y), leaving the button held.
// The caller is responsible for eventually calling Release to avoid leaving
// the button stuck down.
//
// Coordinates are global display points (top-left origin).
//
// No-ops (posts nothing) unless Armed() — see the arm-guard docs above.
func Press(x, y float64) {
	if !leftButton.press(canPost()) {
		recordNoop("Press", x, y)
		return
	}
	C.postMouseEvent(C.kCGEventLeftMouseDown, C.kCGMouseButtonLeft,
		C.CGFloat(x), C.CGFloat(y))
}

// DragTo posts a left-mouse-button DRAGGED event to (x,y) while the button
// remains held (between a Press and a Release call).
//
// Coordinates are global display points (top-left origin).
//
// No-ops (posts nothing) unless Armed() — see the arm-guard docs above.
func DragTo(x, y float64) {
	if !canPost() {
		recordNoop("DragTo", x, y)
		return
	}
	C.postMouseEvent(C.kCGEventLeftMouseDragged, C.kCGMouseButtonLeft,
		C.CGFloat(x), C.CGFloat(y))
}

// Release posts a left-mouse-button UP at (x,y), ending a hold started by Press.
//
// Coordinates are global display points (top-left origin).
//
// No-ops (posts nothing) unless Armed() — see the arm-guard docs above —
// except that the release of a button-down this package really posted is
// always posted (buttonLatch), whatever the arm switch says by then.
func Release(x, y float64) {
	if !leftButton.release(canPost()) {
		recordNoop("Release", x, y)
		return
	}
	C.postMouseEvent(C.kCGEventLeftMouseUp, C.kCGMouseButtonLeft,
		C.CGFloat(x), C.CGFloat(y))
}

// Click performs a left-mouse-button click (DOWN then UP) at (x,y).
// It does not move the cursor before posting the events.
//
// Coordinates are global display points (top-left origin).
// Requires Accessibility permission; without it the events have no effect.
//
// No-ops (posts nothing) unless Armed() — see the arm-guard docs above.
func Click(x, y float64) {
	if !canPost() {
		recordNoop("Click", x, y)
		return
	}
	C.postMouseEvent(C.kCGEventLeftMouseDown, C.kCGMouseButtonLeft,
		C.CGFloat(x), C.CGFloat(y))
	C.postMouseEvent(C.kCGEventLeftMouseUp, C.kCGMouseButtonLeft,
		C.CGFloat(x), C.CGFloat(y))
}

// DoubleClick performs a double left-mouse-button click at (x,y).
// Posts DOWN/UP twice in rapid succession (matching OS double-click semantics).
// Some applications (e.g. DaVinci Resolve color wheel) treat a double-click
// on the wheel center as a balance reset.
//
// Coordinates are global display points (top-left origin).
// Requires Accessibility permission; without it the events have no effect.
//
// No-ops (posts nothing) unless Armed() — see the arm-guard docs above.
func DoubleClick(x, y float64) {
	if !canPost() {
		recordNoop("DoubleClick", x, y)
		return
	}
	C.postMouseEvent(C.kCGEventLeftMouseDown, C.kCGMouseButtonLeft,
		C.CGFloat(x), C.CGFloat(y))
	C.postMouseEvent(C.kCGEventLeftMouseUp, C.kCGMouseButtonLeft,
		C.CGFloat(x), C.CGFloat(y))
	C.postMouseEvent(C.kCGEventLeftMouseDown, C.kCGMouseButtonLeft,
		C.CGFloat(x), C.CGFloat(y))
	C.postMouseEvent(C.kCGEventLeftMouseUp, C.kCGMouseButtonLeft,
		C.CGFloat(x), C.CGFloat(y))
}

// Scroll posts a synthetic vertical scroll-wheel event carrying delta lines
// (signed; positive/negative meaning is caller-defined -- see
// internal/config's "scrollDelta" Binding.LinesPerDetent doc comment and
// internal/engine's dispatchScrollDelta for this app's clockwise/
// counter-clockwise convention) and flags (a CGEventFlags modifier bitmask,
// e.g. 0x80000 for Alt/Option -- see internal/keys.ParseModifierFlags for
// computing this from a modifier-name spec like "Alt") carried DIRECTLY ON
// THE POSTED EVENT ITSELF.
//
// This is the entire mechanism behind this app's 2026-08-13 "scrollDelta"
// feature: two independent live tests already proved macOS does NOT apply a
// synthetically-HELD AMBIENT modifier (the abandoned keys.SendModifierDown/
// SendUp "passthrough" design -- see config.Binding's modifierHold doc
// comment for the full story, including the safety-net watchdog that once
// force-released a held modifier mid-gesture) to a REAL hardware event such
// as a physical mouse/trackpad scroll. But macOS DOES honour explicit flags
// carried on a POSTED synthetic event -- exactly the same mechanism that
// already makes every chord keystroke in this app work (keys.Send's parsed
// modifier prefix). Posting a scroll event that already carries e.g. the
// Alt flag makes the receiving app (DaVinci Resolve) see a genuine
// Alt+scroll and react to it NATIVELY (its own timeline zoom) -- no
// modifier hold/release/watchdog machinery required at all, and no need to
// identify Resolve's Zoom In/Out keyboard shortcuts (keyDelta's earlier,
// unconfirmed approach on this same control).
//
// A posted scroll event is delivered to whatever is under the CURRENT mouse
// cursor position, exactly like a real scroll gesture -- Scroll does NOT
// move or warp the cursor to compensate. The caller is expected to already
// have the cursor positioned over the intended target (e.g. Resolve's
// timeline) before this fires; there is no window/control-independent way
// to target a scroll event at something the cursor isn't over.
//
// No-ops (posts nothing) unless Armed() -- see the arm-guard docs above.
func Scroll(delta int, flags uint64) {
	if !canPost() {
		recordNoop("Scroll", float64(delta), float64(flags))
		return
	}
	C.postScrollEvent(C.int32_t(delta), C.CGEventFlags(flags))
}

// MoveTo moves the pointer to (x,y) WITHOUT pressing any button: the calibration
// editor's dry run ("this is where it would click"). Coordinates are global
// display points (top-left origin). It reports whether the pointer was moved.
//
// No-ops (moves nothing, returns false) unless Armed() and not paused, like
// every other posting function; a test process can never move the pointer.
func MoveTo(x, y float64) bool {
	if !canPost() {
		recordNoop("MoveTo", x, y)
		return false
	}
	C.postMouseEvent(C.kCGEventMouseMoved, C.kCGMouseButtonLeft, C.CGFloat(x), C.CGFloat(y))
	return true
}
