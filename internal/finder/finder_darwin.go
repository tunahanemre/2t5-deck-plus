// Package finder reads the Finder selection and applies macOS colour tags to
// it. It backs 2T5 DECK+'s Finder profile: with a Finder window frontmost the 8
// P-buttons become colour-tag buttons (see docs/specs/2026-09-03-finder-tag-profile.md).
//
// These tags are not decoration. On this machine they are production approval
// decisions with four downstream consumers (dodee-review-sync,
// dodee-episode/lib/review.py, the Dodee Status Service, and the
// Finder->Resolve clip mirror) across ~1929 tagged files, there is no undo,
// and Finder's own Cmd+Z cannot see a write made from outside Finder. Hence
// the arm guard below and the caller-side logging requirement.
package finder

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	"2t5-deck-plus/internal/deckenv"
)

/*
#cgo LDFLAGS: -framework Foundation -framework AppKit

#include <stdlib.h>

extern char* finderSelection(char **errOut);
extern int   finderProbe(char **errOut);
extern char* finderReadTags(const char *path, char **errOut);
extern int   finderWriteTags(const char *path, const char *joined, char **errOut);
*/
import "C"
import "unsafe"

// sep matches kSep in tags_darwin.m -- ASCII unit separator, chosen because a
// _kMDItemUserTags entry is itself "Name\nIndex" so newline is ambiguous.
const sep = "\x1f"

// ErrNotArmed is returned by ApplyColour when the process has not been armed.
var ErrNotArmed = errors.New("finder: not armed — refusing to write tags (DECKPLUS_ARMED not set and Arm() not called)")

// --- arm guard -------------------------------------------------------------
//
// Same shape as internal/mouse and internal/keys, and for the same reason:
// this package MUTATES THE USER'S FILES. A `go test` run that silently
// retagged a production review folder would be worse than one that posts a
// stray keystroke, because there is no undo. The gate lives here, at the
// package boundary, not at each call site -- a future caller that forgets it
// still cannot write (the lesson from the dead mouseClickFn seam, 2026-07-01).

var (
	armOnce   sync.Once
	armedMu   sync.Mutex
	armed     bool
	noopCalls []string
)

// envArmed reports whether DECKPLUS_ARMED requests arming. A pure function of
// the environment (touches no package state) precisely so it can be unit
// tested with t.Setenv without ever calling Arm(), which would flip the
// package global for the remaining lifetime of the test binary.
func envArmed() bool {
	return deckenv.Get("ARMED") == "1"
}

func ensureArmInit() {
	armOnce.Do(func() {
		if envArmed() {
			armedMu.Lock()
			armed = true
			armedMu.Unlock()
		}
	})
}

// Armed reports whether tag WRITES are permitted in this process.
func Armed() bool {
	ensureArmInit()
	armedMu.Lock()
	defer armedMu.Unlock()
	return armed
}

// Arm enables tag writes. Idempotent. Called ONLY from the real application
// entrypoint, never from a test, mirroring mouse.Arm/keys.Arm.
func Arm() {
	ensureArmInit()
	armedMu.Lock()
	armed = true
	armedMu.Unlock()
}

func recordNoop(name string) {
	armedMu.Lock()
	noopCalls = append(noopCalls, name)
	armedMu.Unlock()
}

// NoopCalls returns the calls suppressed by the arm guard, for tests that
// want to assert the no-op path was taken rather than assume it.
func NoopCalls() []string {
	armedMu.Lock()
	defer armedMu.Unlock()
	return append([]string(nil), noopCalls...)
}

// ResetNoopCalls clears the recorded no-op calls.
func ResetNoopCalls() {
	armedMu.Lock()
	noopCalls = nil
	armedMu.Unlock()
}

// --- colour vocabulary -----------------------------------------------------

// ColourNames is the set of macOS colour tag names. Everything else a file
// carries (project tags like "Draft", "Approved") is a NON-colour tag and is
// preserved untouched by every operation here -- see nextTags.
var ColourNames = map[string]bool{
	"Red":    true,
	"Orange": true,
	"Yellow": true,
	"Green":  true,
	"Blue":   true,
	"Purple": true,
	"Gray":   true,
}

// TagName extracts the NAME from a tag entry, discarding any "\n<index>"
// suffix.
//
// 🔴 The name is the only trustworthy part. The live IDLES-02 review corpus
// was tagged from iOS and carries entries like "Red\n1", "Green\n1",
// "Yellow\n1" -- colour index 1 (Gray) on every file regardless of its actual
// name. An implementation that compared the INDEX would answer "is this Red?"
// with "no, it's Gray" across the entire corpus: Red would never toggle off,
// and the Gray button would strip a visibly-red file. NSURLTagNamesKey
// already returns bare names, so this is belt-and-braces for entries that
// arrive from anywhere else.
func TagName(entry string) string {
	if i := strings.IndexByte(entry, '\n'); i >= 0 {
		return entry[:i]
	}
	return entry
}

// --- batch decision (pure, unit-tested without a filesystem) ---------------

type action int

const (
	actionAdd action = iota
	actionRemove
	actionClear
)

func (a action) String() string {
	switch a {
	case actionAdd:
		return "add"
	case actionRemove:
		return "remove"
	default:
		return "clear"
	}
}

// decideBatchAction picks ONE action for the whole selection.
//
// Deciding per file produces a checkerboard the user can never converge: with
// a mixed selection, every press flips each file independently and the batch
// never reaches a uniform state. So: if every file already carries the
// colour, the press removes it from all; otherwise the press adds it to all.
// An empty colour is always a clear.
func decideBatchAction(existing [][]string, colour string) action {
	if colour == "" {
		return actionClear
	}
	if len(existing) == 0 {
		return actionAdd
	}
	for _, tags := range existing {
		if !hasColour(tags, colour) {
			return actionAdd
		}
	}
	return actionRemove
}

func hasColour(tags []string, colour string) bool {
	for _, t := range tags {
		if TagName(t) == colour {
			return true
		}
	}
	return false
}

// nextTags computes a file's replacement tag list.
//
// Non-colour tags are ALWAYS carried through untouched. Adding a colour first
// drops every other colour (colours are exclusive -- a file shows one swatch),
// removing drops only the colour being toggled, and clearing drops every
// colour while still leaving named tags alone.
//
// Names are written back canonically (no "\n<index>" suffix), which
// incidentally repairs the iOS-written entries described on TagName.
func nextTags(existing []string, colour string, a action) []string {
	out := make([]string, 0, len(existing)+1)
	for _, t := range existing {
		name := TagName(t)
		if !ColourNames[name] {
			out = append(out, name) // project tag: preserved
			continue
		}
		switch a {
		case actionRemove:
			if name != colour {
				out = append(out, name)
			}
		case actionClear, actionAdd:
			// every colour drops; actionAdd re-adds one below
		}
	}
	if a == actionAdd && colour != "" {
		out = append(out, colour)
	}
	return out
}

// --- CGO boundary ----------------------------------------------------------

func cErr(p *C.char) string {
	if p == nil {
		return "unknown error"
	}
	defer C.free(unsafe.Pointer(p))
	return C.GoString(p)
}

func splitSep(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, sep)
}

// Selection returns the POSIX paths of the current Finder selection.
//
// This is the one call in the package that sends an Apple Event, so it is the
// one that needs TCC's Automation grant and the main-queue hop (see
// tags_darwin.m). An empty selection is an empty slice, not an error.
func Selection() ([]string, error) {
	var cerr *C.char
	out := C.finderSelection(&cerr)
	if out == nil {
		return nil, fmt.Errorf("finder: reading selection: %s", cErr(cerr))
	}
	defer C.free(unsafe.Pointer(out))
	return splitSep(C.GoString(out)), nil
}

// Probe sends the cheapest possible Apple Event to Finder so a missing or
// revoked Automation grant is discovered at launch rather than mid-review.
func Probe() error {
	var cerr *C.char
	if C.finderProbe(&cerr) == 0 {
		return fmt.Errorf("finder: Apple Event probe failed: %s", cErr(cerr))
	}
	return nil
}

// ReadTags returns a file's tag names. No Apple Event -- a direct NSURL
// resource-value read.
func ReadTags(path string) ([]string, error) {
	cp := C.CString(path)
	defer C.free(unsafe.Pointer(cp))
	var cerr *C.char
	out := C.finderReadTags(cp, &cerr)
	if out == nil {
		return nil, fmt.Errorf("finder: reading tags of %s: %s", path, cErr(cerr))
	}
	defer C.free(unsafe.Pointer(out))
	return splitSep(C.GoString(out)), nil
}

// WriteTags replaces a file's tag list. Gated by the arm guard.
func WriteTags(path string, tags []string) error {
	if !Armed() {
		recordNoop("WriteTags")
		return ErrNotArmed
	}
	return writeTagsUnguarded(path, tags)
}

// writeTagsUnguarded is the raw write. Only WriteTags and ApplyColour (both
// of which check Armed() first) may call it.
func writeTagsUnguarded(path string, tags []string) error {
	cp := C.CString(path)
	defer C.free(unsafe.Pointer(cp))
	cj := C.CString(strings.Join(tags, sep))
	defer C.free(unsafe.Pointer(cj))
	var cerr *C.char
	if C.finderWriteTags(cp, cj, &cerr) == 0 {
		return fmt.Errorf("finder: writing tags of %s: %s", path, cErr(cerr))
	}
	return nil
}

// ApplyColour toggles a colour tag across a whole selection and returns how
// many files were successfully written.
//
// colour "" clears colour tags (the P8 button). Any other value must be one
// of ColourNames.
//
// Per-file failures are TOLERATED, never fatal to the batch: SMB and some
// network mounts return ENOTSUP for extended attributes, and losing the rest
// of a 40-file approval pass because file 3 lives on a share would be worse
// than a partial result the caller can report. The returned count is the
// successes; the returned error, when non-nil alongside a non-zero count, is
// an aggregate describing what failed.
func ApplyColour(paths []string, colour string) (int, error) {
	if colour != "" && !ColourNames[colour] {
		return 0, fmt.Errorf("finder: %q is not a macOS colour tag", colour)
	}
	if len(paths) == 0 {
		return 0, nil
	}
	if !Armed() {
		recordNoop("ApplyColour")
		return 0, ErrNotArmed
	}

	existing := make([][]string, 0, len(paths))
	readable := make([]string, 0, len(paths))
	var failures []string
	for _, p := range paths {
		tags, err := ReadTags(p)
		if err != nil {
			failures = append(failures, err.Error())
			continue
		}
		existing = append(existing, tags)
		readable = append(readable, p)
	}

	a := decideBatchAction(existing, colour)

	n := 0
	for i, p := range readable {
		if err := writeTagsUnguarded(p, nextTags(existing[i], colour, a)); err != nil {
			failures = append(failures, err.Error())
			continue
		}
		n++
	}

	if len(failures) > 0 {
		return n, fmt.Errorf("finder: %s %q: %d of %d files failed: %s",
			a, colour, len(failures), len(paths), strings.Join(failures, "; "))
	}
	return n, nil
}
