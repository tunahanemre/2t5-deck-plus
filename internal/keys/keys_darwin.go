// Package keys provides keyboard shortcut injection via CoreGraphics
// CGEventCreateKeyboardEvent.
//
// Usage:
//
//	keys.Send("Cmd+Z")         // undo
//	keys.Send("Alt+S")         // save as
//	keys.Send("Ctrl+Shift+Z")  // redo (some apps)
//	keys.Send("Left")          // arrow key
//
// Gate: Send only posts events when DaVinci Resolve is the frontmost
// application (reuses macapp.IsResolveFrontmost). The Color-page requirement
// is NOT enforced for keystrokes.
//
// Requires Accessibility permission (System Settings → Privacy & Security →
// Accessibility). Without it the events are posted but have no effect.
package keys

/*
#cgo LDFLAGS: -framework CoreGraphics

#include <CoreGraphics/CoreGraphics.h>
#include <stdint.h>

// postKeyDown posts a key-down event ONLY for the given virtual key code
// with the supplied modifier flags -- split out from the combined postKey
// below so a modifier key can be pressed and left HELD across multiple
// subsequent posts (see internal/engine's Task 6 held-modifier feature).
static void postKeyDown(uint16_t keyCode, CGEventFlags flags) {
    CGEventRef down = CGEventCreateKeyboardEvent(NULL, keyCode, true);
    CGEventSetFlags(down, flags);
    CGEventPost(kCGHIDEventTap, down);
    CFRelease(down);
}

// postKeyUp posts a key-up event ONLY for the given virtual key code --
// the release half of postKeyDown above.
static void postKeyUp(uint16_t keyCode, CGEventFlags flags) {
    CGEventRef up = CGEventCreateKeyboardEvent(NULL, keyCode, false);
    CGEventSetFlags(up, flags);
    CGEventPost(kCGHIDEventTap, up);
    CFRelease(up);
}

// postKey posts a key-down + key-up event for the given virtual key code
// with the supplied modifier flags -- Send's existing combined behaviour,
// now implemented in terms of postKeyDown/postKeyUp above so there is only
// one place that actually calls CGEventCreateKeyboardEvent per direction.
static void postKey(uint16_t keyCode, CGEventFlags flags) {
    postKeyDown(keyCode, flags);
    postKeyUp(keyCode, flags);
}

*/
import "C"
import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"

	"2t5-deck-plus/internal/deckenv"
)

// ---------------------------------------------------------------------------
// Arm guard — SAFETY CRITICAL.
//
// Send MUST check Armed() before posting any real CGEvent and no-op if
// false. Default (unarmed) is the safe state: nothing is ever posted to the
// system. A process only becomes armed if the DECKPLUS_ARMED=1
// environment variable was set before this package's first use, or Arm()
// was called explicitly.
//
// `go test` never sets DECKPLUS_ARMED, so this package is a guaranteed
// no-op under test regardless of which code path a test exercises — this is
// what makes it IMPOSSIBLE (not just unlikely) for `go test` to type real
// keystrokes into whatever application has focus on the user's Mac.
//
// Only the real application entrypoint (cmd/2t5-deck-plus/main.go's live run
// path, and later the Wails app) should ever call Arm(). Never call it from
// a test.
// ---------------------------------------------------------------------------

var (
	armed       atomic.Bool
	paused      atomic.Bool // the editor's safety switch (SetPaused): see canPost
	armInitOnce sync.Once
	noopMu      sync.Mutex
	noopCalls   []NoopCall
)

// NoopCall records one Send/SendDown/SendUp invocation that was
// suppressed (no real CGEvent posted) because the package was unarmed.
//
// Spec is the human-readable spec passed to Send, or the bare key name
// passed to SendDown/SendUp. Flags is the CGEventFlags bitmask that call
// would have posted with, as a plain uint64 -- deliberately NOT the cgo
// C.CGEventFlags type, so a caller outside this cgo-using package (e.g.
// internal/engine, for Task 7's flag-merge test) can read it without
// needing to import "C" itself. Kind distinguishes a combined Send
// ("down+up") from a split SendDown ("down") / SendUp ("up") call --
// added so a caller can assert exactly which half of a held-modifier
// press fired, not just that something did.
type NoopCall struct {
	Spec  string
	Flags uint64
	Kind  string
}

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

// Armed reports whether this process is currently armed to post real
// keyboard input via CGEvent. False (the default) means Send is a safe
// no-op that touches nothing on the system (after normal spec validation).
func Armed() bool {
	ensureArmInit()
	return armed.Load()
}

// Arm explicitly arms this process to post real keyboard input via CGEvent.
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

// recordNoop appends a call description to the no-op call log. Used so
// tests can assert the no-op path was taken (no real CGEvent posted)
// without needing a live display or Accessibility permission.
func recordNoop(spec string, flags C.CGEventFlags, kind string) {
	noopMu.Lock()
	defer noopMu.Unlock()
	noopCalls = append(noopCalls, NoopCall{Spec: spec, Flags: uint64(flags), Kind: kind})
}

// NoopCalls returns a snapshot of every Send/SendDown/SendUp call that was
// suppressed (no-op'd) because the package was unarmed. Test-only
// visibility hook.
func NoopCalls() []NoopCall {
	noopMu.Lock()
	defer noopMu.Unlock()
	out := make([]NoopCall, len(noopCalls))
	copy(out, noopCalls)
	return out
}

// ResetNoopCalls clears the recorded no-op call log. Test-only.
func ResetNoopCalls() {
	noopMu.Lock()
	defer noopMu.Unlock()
	noopCalls = nil
}

// cgKeyCode is the macOS virtual key code for a named key.
type cgKeyCode = C.uint16_t

// keyTable maps canonical key names to CGKeyCodes (US layout).
// Arrow key names are capitalised; letter names are lower-case internally.
var keyTable = map[string]cgKeyCode{
	// Letters A-Z
	"a": 0, "b": 11, "c": 8, "d": 2, "e": 14, "f": 3, "g": 5, "h": 4,
	"i": 34, "j": 38, "k": 40, "l": 37, "m": 46, "n": 45, "o": 31,
	"p": 35, "q": 12, "r": 15, "s": 1, "t": 17, "u": 32, "v": 9,
	"w": 13, "x": 7, "y": 16, "z": 6,
	// Digits 0-9
	"0": 29, "1": 18, "2": 19, "3": 20, "4": 21, "5": 23,
	"6": 22, "7": 26, "8": 28, "9": 25,
	// Arrow keys (title case)
	"left": 123, "right": 124, "down": 125, "up": 126,
	// Common keys
	"return": 36, "enter": 76, "space": 49, "tab": 48, "escape": 53,
	"delete": 51, "forwarddelete": 117,
	"home": 115, "end": 119, "pageup": 116, "pagedown": 121,
	"f1": 122, "f2": 120, "f3": 99, "f4": 118, "f5": 96, "f6": 97,
	"f7": 98, "f8": 100, "f9": 101, "f10": 109, "f11": 103, "f12": 111,
	// Punctuation / common specials
	"-": 27, "=": 24, "[": 33, "]": 30, "\\": 42, ";": 41, "'": 39,
	",": 43, ".": 47, "/": 44, "`": 50,
	// Modifier keys (left-hand variants, US layout) -- added for Task 6's
	// held deck-modifier feature (Alt/Ctrl/Cmd/Shift), which needs to press
	// and hold ONE of these as a standalone key via SendDown/SendUp, not as
	// a "Mod+Key" combo prefix. Distinct from modifierTable below, which
	// maps the SAME names to CGEventFlags BITS for use as a Send spec
	// PREFIX (e.g. "Cmd+Z"). No runtime ambiguity between the two tables:
	// Send only ever consults modifierTable for a spec's non-final parts
	// and keyTable for its final (key-name) part; SendDown/SendUp only
	// ever consult keyTable directly for a bare name.
	"command": 55, "cmd": 55,
	"shift":  56,
	"option": 58, "alt": 58,
	"control": 59, "ctrl": 59,
}

// modifierTable maps modifier name (lower-case) to CGEventFlags.
var modifierTable = map[string]C.CGEventFlags{
	"cmd":     C.CGEventFlags(0x100000), // kCGEventFlagMaskCommand
	"command": C.CGEventFlags(0x100000),
	"ctrl":    C.CGEventFlags(0x40000), // kCGEventFlagMaskControl
	"control": C.CGEventFlags(0x40000),
	"alt":     C.CGEventFlags(0x80000), // kCGEventFlagMaskAlternate
	"option":  C.CGEventFlags(0x80000),
	"shift":   C.CGEventFlags(0x20000), // kCGEventFlagMaskShift
}

// ParseModifierFlags parses a "+"-joined list of modifier names (Cmd,
// Command, Ctrl, Control, Alt, Option, Shift; case-insensitive, surrounding
// whitespace ignored) into the combined CGEventFlags bitmask, as a plain
// uint64 — deliberately NOT the cgo C.CGEventFlags type, same rationale as
// NoopCall.Flags: a caller outside this cgo-using package can read/compare
// it without importing "C" itself.
//
// This is the SAME modifierTable lookup Send's own spec parsing uses for
// every part of a spec except its final (key-name) segment — extracted here
// so a caller that wants ONLY the modifier bitmask, with no trailing key
// name, has one place to get it rather than re-deriving the mapping.
// (2026-08-13: internal/config's "scrollDelta" binding type needs exactly
// this — it builds the flags it carries on a posted CGEvent scroll event
// via internal/mouse.Scroll, which has no "key" to parse a spec's final
// segment as.) Send itself now calls this for its own modifier-prefix
// parsing, so there is exactly one modifier-name→flags mapping in this
// package, not two.
//
// Returns an error if any part is not a recognised modifier name. An empty
// spec returns (0, nil) — zero modifiers is a valid "no flags" answer here,
// unlike Send's own empty-spec rejection (Send additionally requires a
// trailing key name, a constraint that does not apply to a modifier-only
// spec).
func ParseModifierFlags(spec string) (uint64, error) {
	if spec == "" {
		return 0, nil
	}
	var flags C.CGEventFlags
	for _, part := range strings.Split(spec, "+") {
		f, ok := modifierTable[strings.ToLower(strings.TrimSpace(part))]
		if !ok {
			return 0, fmt.Errorf("keys: unknown modifier %q in %q", part, spec)
		}
		flags |= f
	}
	return uint64(flags), nil
}

// Send parses a key combo specification and posts the corresponding key-down
// + key-up events via CoreGraphics.
//
// Format: zero or more modifier names joined by "+", followed by the key name.
// Modifier names (case-insensitive): Cmd, Command, Ctrl, Control, Alt, Option, Shift.
// Key names: A-Z, 0-9, Left, Right, Up, Down, Return, Enter, Space, Tab,
// Escape, Delete, F1-F12, and common punctuation.
//
// Examples:
//
//	"Cmd+Z"        → Command+Z
//	"Ctrl+Shift+Z" → Control+Shift+Z
//	"Alt+S"        → Option+S
//	"Left"         → left arrow (no modifiers)
//
// Returns an error if the key name is not recognised. Modifier-only combos
// (no key name) return an error.
//
// No-ops (posts nothing) unless Armed() — see the arm-guard docs above.
// Spec validation (and its error returns) happens identically whether armed
// or not; only the final CGEvent post is gated.
func Send(spec string) error {
	if IsMediaSpec(spec) { // "media:play" etc.: see media_darwin.go
		return sendMedia(spec)
	}
	parts := strings.Split(spec, "+")
	if len(parts) == 0 || spec == "" {
		return fmt.Errorf("keys: empty spec")
	}

	// All parts except the last are modifiers; the last is the key.
	// ParseModifierFlags is the SAME parsing this line used to do inline —
	// see its doc comment for why this is now the one place that mapping
	// lives.
	modSpec := strings.Join(parts[:len(parts)-1], "+")
	flags64, err := ParseModifierFlags(modSpec)
	if err != nil {
		return err
	}
	flags := C.CGEventFlags(flags64)

	keyName := strings.ToLower(strings.TrimSpace(parts[len(parts)-1]))
	kc, ok := keyTable[keyName]
	if !ok {
		return fmt.Errorf("keys: unknown key %q in %q", parts[len(parts)-1], spec)
	}

	if !canPost() {
		recordNoop(spec, flags, "down+up")
		return nil
	}

	C.postKey(kc, flags)
	return nil
}

// SendDown posts a key-down event ONLY (no matching key-up) for the named
// key, with no modifier flags — the press half of a held key. Used
// directly by tools/flag_probe's CGEvent-inheritance diagnostic, and any
// other non-modifier held-key use. NOTE: this posts a kCGEventKeyDown, the
// WRONG CGEvent TYPE for synthesizing a held MODIFIER specifically (real
// hardware modifier input generates kCGEventFlagsChanged instead) — a
// 2026-08-13 held deck-modifier "passthrough" feature built on this
// finding was tried and then removed the same day after two live tests
// showed the modifier still didn't apply to real hardware input either
// way; see internal/config's modifierHold doc comment for the full story.
// Fine for a genuinely non-modifier held key (flag_probe's actual use).
//
// Unlike Send, name is a single bare key name — no "Mod+Key" combo syntax
// (a modifier key cannot itself be modified). Case-insensitive, same
// keyTable lookup Send's final spec segment uses.
//
// No-ops (posts nothing, records a NoopCall) unless Armed() — same
// arm-guard posture as Send.
func SendDown(name string) error {
	kc, ok := keyTable[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return fmt.Errorf("keys: unknown key %q", name)
	}
	if !canPost() {
		recordNoop(name, 0, "down")
		return nil
	}
	C.postKeyDown(kc, 0)
	return nil
}

// SendUp posts a key-up event ONLY for the named key — the release half of
// SendDown. Same lookup/arm-guard posture as SendDown, and the same
// non-modifier-only caveat (see SendDown's doc comment).
func SendUp(name string) error {
	kc, ok := keyTable[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return fmt.Errorf("keys: unknown key %q", name)
	}
	if !canPost() {
		recordNoop(name, 0, "up")
		return nil
	}
	C.postKeyUp(kc, 0)
	return nil
}
