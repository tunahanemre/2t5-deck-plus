// Package hud provides a native, always-on-top macOS status monitor
// (NSPanel hosting a WKWebView) — a rich, styled instrument readout of the
// engine's live state, so the user can glance at it without leaving
// Resolve (which usually runs fullscreen). Monitor-only: no controls, no
// input -- the web content is visually rich (HUD v2) but functionally
// inert; see hud_darwin.m's LDHUDWebView doc comment for the load-bearing
// focus guard that keeps it that way.
//
// The panel is toggled by two independent sources that both funnel into
// this package's Toggle(): the main app window's "Show HUD" button
// (app.ToggleHUD, via a var seam so tests never call the real Toggle) and
// the Loupedeck C6 button (a nilable callback wired into
// internal/engine.Engine — see engine.go's IsToggleHud() case). Content is
// derived from a formatHUD-style pure function living in the caller (see
// app/format.go) and pushed in via Update; this package owns ONLY the
// native window + the embedded web template, never the formatting logic
// (window.hud.render(state) in hud.html does DOM binding only).
//
// See docs/specs/2026-07-05-ld-resolve-hud-design.md (v1, native NSTextField
// rows) and docs/specs/2026-07-06-ld-resolve-hud-rich.md (v2, this
// WKWebView rewrite) for the full design and review-resolved corrections.
package hud

/*
#cgo LDFLAGS: -framework AppKit -framework CoreGraphics -framework WebKit
#include <stdlib.h>

// hudShow/hudHide/hudUpdate are implemented in hud_darwin.m (Objective-C).
// Declared inline here (no separate .h) -- matches internal/macapp's
// convention (frontmost_darwin.go declares its extern the same way).
extern void hudShow(const char* html);
extern void hudHide(void);
extern void hudSetPlacement(const char* displayName, const char* edge, double heightPx);
extern void hudUpdate(const char* stateJSON);
*/
import "C"

import (
	_ "embed"
	"encoding/json"
	"sync"
	"sync/atomic"
	"unsafe"

	"2t5-deck-plus/internal/deckenv"
)

// htmlTemplate is the HUD's entire self-contained HTML/CSS/JS page
// (productionized from docs/specs/2026-07-06-ld-resolve-hud-rich.md's
// approved mockup) — embedded at build time so the shipped .app never
// touches the network or the filesystem for it. Loaded into the WKWebView
// exactly once, on first Show() (see _ensurePanel in hud_darwin.m); every
// subsequent Update() call only pushes a new JSON state via
// evaluateJavaScript, never reloads the page.
//
//go:embed hud.html
var htmlTemplate string

// ---------------------------------------------------------------------------
// Arm guard — SAFETY CRITICAL. Mirrors internal/mouse and internal/keys
// EXACTLY (see mouse_darwin.go's doc comment for the full rationale this
// package inherits verbatim).
//
// This is NOT input-injection like mouse/keys, but the same hazard class
// applies: without a gate, ANY code path that reaches Show/Hide/Update
// (including a future test written against this package directly, not just
// through the app.go/engine.go seams) would create a REAL NSPanel window
// during `go test`. Default (unarmed) is the safe state: every function
// below is a no-op that touches no AppKit API.
//
// A process only becomes armed if the DECKPLUS_ARMED=1 environment
// variable was set before this package's first use, or Arm() was called
// explicitly. `go test` never sets DECKPLUS_ARMED and never calls Arm()
// (Arm() is called exactly once, from app/main.go's func main() — a
// function body `go test` never executes, exactly like
// cmd/2t5-deck-plus/main.go's mouse.Arm()/keys.Arm() placement). This is what
// makes it IMPOSSIBLE (not just unlikely) for `go test` to pop a real
// window on the user's screen.
// ---------------------------------------------------------------------------

var (
	armed       atomic.Bool
	armInitOnce sync.Once
	noopMu      sync.Mutex
	noopCalls   []string

	// visible is the Go-side desired-visibility state — the source of truth
	// for Visible() and Toggle()'s return value. Tracked independently of
	// whatever the real NSPanel's actual on-screen state is (which is only
	// ever mutated asynchronously, via dispatch_async, from hud_darwin.m),
	// so Visible()/Toggle() never need to cross the CGO boundary just to
	// read state — cheap enough to call unconditionally from the 250ms
	// status ticker (see app/app.go's runStatusTicker).
	visible atomic.Bool
)

// envArmed reports whether the DECKPLUS_ARMED env var requests arming.
// Pulled out as a pure function (no global state touched) so its mapping
// logic can be unit tested in isolation, without ever flipping the real
// `armed` flag or calling a real AppKit function.
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

// Armed reports whether this process is currently armed to create/show a
// real NSPanel. False (the default) means every function in this package is
// a safe no-op.
func Armed() bool {
	ensureArmInit()
	return armed.Load()
}

// Arm explicitly arms this process to create/show a real NSPanel. Idempotent.
// Intended to be called exactly once, from app/main.go's func main() — never
// from a test, never from app.NewApp()/App.startup() (both of those ARE
// exercised directly by tests in app_test.go, unlike func main(), which
// `go test` never invokes).
func Arm() {
	ensureArmInit()
	armed.Store(true)
}

func recordNoop(name string) {
	noopMu.Lock()
	defer noopMu.Unlock()
	noopCalls = append(noopCalls, name)
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

// Show makes the HUD panel visible, creating it (lazily, on the main queue)
// on first call. Safe to call from any goroutine/thread. The embedded
// htmlTemplate is passed through every call (cheap — a compile-time
// constant string) but only actually loaded into the WKWebView the FIRST
// time (see hud_darwin.m's _ensurePanel, guarded by the same
// hudPanel!=nil singleton check as v1) — later calls' html argument is
// simply ignored.
//
// No-ops (touches no AppKit API) unless Armed() — see the arm-guard docs
// above.
// Configure sets the HUD's placement. displayName is a substring matched
// (case-insensitively) against each screen's localizedName; heightPx is the
// strip's fixed height; edge is "bottom" (default) or "top".
//
// An empty displayName or a zero height disables the fixed placement and
// leaves the existing behaviour untouched, which is also what happens when
// the named display simply is not connected -- absence is a normal resting
// state, not an error.
//
// Safe to call before Show(); the values are read on every frame computation.
func Configure(displayName, edge string, heightPx int) {
	if !Armed() {
		recordNoop("Configure")
		return
	}
	cName := C.CString(displayName)
	defer C.free(unsafe.Pointer(cName))
	cEdge := C.CString(edge)
	defer C.free(unsafe.Pointer(cEdge))
	C.hudSetPlacement(cName, cEdge, C.double(heightPx))
}

func Show() {
	visible.Store(true)
	if !Armed() {
		recordNoop("Show")
		return
	}
	cHTML := C.CString(htmlTemplate)
	defer C.free(unsafe.Pointer(cHTML))
	C.hudShow(cHTML)
}

// Hide orders the panel out (hides it) without closing or releasing it — the
// panel is a process-lifetime singleton (see hud_darwin.m); a later Show()
// just re-shows the same object. Safe to call from any goroutine/thread.
//
// No-ops (touches no AppKit API) unless Armed() — see the arm-guard docs
// above.
func Hide() {
	visible.Store(false)
	if !Armed() {
		recordNoop("Hide")
		return
	}
	C.hudHide()
}

// Toggle flips the HUD's desired visibility and returns the NEW state.
// Atomic (safe if called concurrently from the frontend's "Show HUD" button
// and the Loupedeck C6 button at the same instant) via a compare-and-swap
// loop on the Go-side `visible` flag.
//
// No-ops (touches no AppKit API) unless Armed() — see the arm-guard docs
// above. The Go-side visible flag still flips even when unarmed, so Toggle's
// return value is meaningful in tests that call it directly (none do today —
// app.go's ToggleHUD goes through the hudToggleFn seam instead — but this
// keeps the contract honest).
func Toggle() bool {
	for {
		old := visible.Load()
		next := !old
		if visible.CompareAndSwap(old, next) {
			if !Armed() {
				recordNoop("Toggle")
				return next
			}
			if next {
				cHTML := C.CString(htmlTemplate)
				C.hudShow(cHTML)
				C.free(unsafe.Pointer(cHTML))
			} else {
				C.hudHide()
			}
			return next
		}
	}
}

// Visible reports the HUD's current desired-visibility state. Pure Go read
// of an atomic bool — never crosses into CGO/AppKit, so it's cheap to call
// unconditionally every tick (see app/app.go's runStatusTicker, which only
// calls Update when this returns true).
func Visible() bool {
	return visible.Load()
}

// Update pushes a new HUDModel to the panel by marshaling it to JSON and
// evaluating "window.hud.render(<json>)" in the WKWebView. Safe to call
// from any goroutine/thread — in production it's called from the 250ms
// status-ticker goroutine (app/app.go's runStatusTicker), never the main
// thread.
//
// No-ops (touches no AppKit API) unless Armed() — see the arm-guard docs
// above. A JSON marshal error (should be unreachable -- HUDModel is a
// plain struct of strings/bools/ints/a fixed-size array, all always
// marshalable) is logged nowhere on purpose: this package has no logger
// dependency, matching v1's posture that a formatting failure here must
// never be fatal for the caller.
func Update(m HUDModel) {
	if !Armed() {
		recordNoop("Update")
		return
	}
	body, err := json.Marshal(m)
	if err != nil {
		return
	}

	// SAFE despite hudUpdate's own dispatch_async: hud_darwin.m converts
	// the C string to an NSString (copying the bytes) SYNCHRONOUSLY, before
	// hudUpdate returns here — the deferred C.free call below never races
	// the async block that later evaluates it as JavaScript. See
	// hud_darwin.m's hudUpdate doc comment for the full explanation (same
	// pattern as v1, just a single JSON string instead of 5 row strings).
	cJSON := C.CString(string(body))
	defer C.free(unsafe.Pointer(cJSON))
	C.hudUpdate(cJSON)
}
