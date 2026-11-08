package engine

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/finder"
	"2t5-deck-plus/internal/macapp"
)

// Finder profile — the deck's first app-aware profile.
// Spec: docs/specs/2026-09-03-finder-tag-profile.md
//
// With a Finder window frontmost the 8 P-buttons apply a macOS colour tag to
// the current Finder selection instead of doing their Resolve job. Everything
// else on the deck is unchanged, and Resolve-frontmost behaviour is untouched.

// finderBankName is the bindings.json bank holding the profile. Named for the
// bundle id rather than "finder" on purpose: when the global spec's app rung
// lands, this bank is already the data that rung consumes, not a special case
// somebody has to migrate.
const finderBankName = "com.apple.finder"

// defaultFinderTagConfirmThreshold is the selection size above which a press
// must be confirmed by a second press. Overridable via
// _config.finderTagConfirmThreshold.
const defaultFinderTagConfirmThreshold = 10

// finderTagConfirmWindow is how long a pending confirmation stays valid. A
// var, not a const, purely so tests can shrink it and cover expiry without a
// two-second sleep -- never reassigned in production.
var finderTagConfirmWindow = 2 * time.Second

// finderTagQueueCap bounds the work queue. A press that arrives with the
// queue full is DROPPED with a log line rather than queued: the alternative
// is an unbounded backlog of irreversible metadata writes replaying long
// after the user stopped pressing, which is far worse than a lost press.
const finderTagQueueCap = 8

// --- seams (spec §10) -----------------------------------------------------
//
// Package-level func vars, matching startMIDIFn / hudSetVisibleFn /
// executableFn. Three, not two: the frontmost check needs one as well, or an
// engine test could not exercise the hook without a real CGWindowList walk
// (and a real frontmost app) on the machine running the suite.
//
// finderSelectionFn and finderApplyTagFn are separate so the Apple Event and
// the filesystem write fake INDEPENDENTLY -- a test can hand the dispatcher a
// selection without any tag ever being written, which is the only way to
// cover the batch logic without touching real files.

var finderSelectionFn = finder.Selection

var finderApplyTagFn = finder.ApplyColour

var frontmostBundleIDFn = macappFrontmostBundleID

// finderTagJob is one queued press. The colour is captured at press time (it
// is a property of the button); the SELECTION is deliberately not -- it is
// read in the worker, at execution time, so a press always acts on what is
// selected when it runs rather than on a stale snapshot.
type finderTagJob struct {
	control string
	colour  string
}

// finderBankBinding looks up a control in the Finder profile bank.
//
// 🔴 Deliberately NOT routed through Cascade, and this is not an oversight to
// tidy up later. noteBankGuard (engine.go:1455) whitelists binding types and
// falls through to `col` for anything not on the list -- the shipped
// colorByRenderState bug its own comment documents. "finderTag" is not on
// that whitelist, so a Cascade lookup would silently resolve a P-button to
// its col-bank binding and fire the wrong action entirely. A dedicated lookup
// parallel to editBankBinding is the correct shape here; do not "unify" them
// without first fixing noteBankGuard.
func finderBankBinding(cfg *config.Config, control string) (config.Binding, bool) {
	if cfg == nil {
		return config.Binding{}, false
	}
	return cfg.Binding(finderBankName, control)
}

// isFinderTagControl reports whether control carries a finderTag binding, and
// returns it. A pure map lookup -- no CGO, no Apple Event -- because it is
// deliberately called BEFORE the frontmost check on every single deck press
// (see the hook in engine.go). FrontmostBundleID is a CGWindowList walk
// costing ~1-3ms and must not be paid by controls that could never be a
// Finder tag button.
func isFinderTagControl(cfg *config.Config, control string) (config.Binding, bool) {
	b, ok := finderBankBinding(cfg, control)
	if !ok || !b.IsFinderTag() {
		return config.Binding{}, false
	}
	return b, true
}

// finderTagConfirmThreshold resolves the effective threshold for a config.
func finderTagConfirmThreshold(cfg *config.Config) int {
	if cfg == nil || cfg.TopLevel.FinderTagConfirmThreshold == 0 {
		return defaultFinderTagConfirmThreshold
	}
	return cfg.TopLevel.FinderTagConfirmThreshold
}

// colourLabel renders a colour for humans; "" is the P8 clear action.
func colourLabel(colour string) string {
	if colour == "" {
		return "Clear tags"
	}
	return colour
}

// isFinderPermissionError reports whether err looks like a denied or missing
// Automation grant. AppleScript surfaces those as -1743 (not authorised to
// send Apple Events) and -600 (application isn't running / not reachable).
func isFinderPermissionError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "-1743") || strings.Contains(s, "-600")
}

// finderTagWorker is the SINGLE serialized consumer of the tag queue.
//
// One worker, never `go handleFinderTag(...)` per press. The note_on debounce
// is per-control at 50ms, so a ~100ms double-tap on one button passes both
// presses, and P1-then-P2 has no debounce coupling at all. Concurrent
// read-modify-write cycles over the same files would lose a toggle or land
// out of order, and these writes are irreversible.
//
// Serializing also gives the confirmation latch below a home that needs no
// lock: only this goroutine ever touches it.
func (e *Engine) finderTagWorker(ctx context.Context, jobs <-chan finderTagJob, threshold int) {
	var promptControl string
	var promptAt time.Time
	warnedAboutPermission := false

	for {
		select {
		case <-ctx.Done():
			return
		case job, ok := <-jobs:
			if !ok {
				return
			}

			paths, err := finderSelectionFn()
			if err != nil {
				if isFinderPermissionError(err) {
					if !warnedAboutPermission {
						warnedAboutPermission = true
						log.Printf("[finderTag] PERMISSION DENIED reading the Finder selection: %v — "+
							"grant 2T5 DECK+ control of Finder in System Settings > Privacy & Security > Automation, "+
							"then press again. (Further occurrences logged briefly.)", err)
					} else {
						log.Printf("[finderTag] permission still denied: %v", err)
					}
					e.setLastAction("Finder access denied — see Privacy & Security > Automation")
					continue
				}
				log.Printf("[finderTag] control=%q colour=%q selection FAILED: %v", job.control, job.colour, err)
				e.setLastAction("Finder: could not read selection")
				continue
			}
			warnedAboutPermission = false

			if len(paths) == 0 {
				log.Printf("[finderTag] control=%q colour=%q selection empty — nothing to tag", job.control, job.colour)
				e.setLastAction("Finder: nothing selected")
				continue
			}

			// Selection-size guard. One EP16 folder holds 815 files; Cmd+A
			// plus a stray press is ~1630 xattr writes on a live Drive mirror
			// and 815 irreversible approval decisions.
			if threshold >= 0 && len(paths) > threshold {
				confirmed := job.control == promptControl && time.Since(promptAt) <= finderTagConfirmWindow
				if !confirmed {
					promptControl, promptAt = job.control, time.Now()
					log.Printf("[finderTag] control=%q colour=%q selection=%d exceeds confirm threshold %d — "+
						"awaiting a second press of the same button within %s",
						job.control, job.colour, len(paths), threshold, finderTagConfirmWindow)
					e.setLastAction(fmt.Sprintf("Press again to tag %d files", len(paths)))
					continue
				}
			}
			promptControl, promptAt = "", time.Time{}

			// Log EVERY press with full paths. These tags are production
			// approval decisions with four downstream consumers, there is no
			// undo, and Finder's Cmd+Z cannot see a write made from outside
			// Finder — this log is the only forensic trail that will exist.
			log.Printf("[finderTag] control=%q colour=%q files=%d paths=%s",
				job.control, colourLabel(job.colour), len(paths), strings.Join(paths, " | "))

			if !e.writesAllowed() { // the arm switch is off: report, write nothing
				log.Printf("[finderTag] control=%q colour=%q DISARMED — %d files not tagged", job.control, colourLabel(job.colour), len(paths))
				e.setLastAction(fmt.Sprintf("disarmed: %s not applied to %d files", colourLabel(job.colour), len(paths)))
				continue
			}

			n, err := finderApplyTagFn(paths, job.colour)
			if err != nil {
				log.Printf("[finderTag] control=%q colour=%q applied %d/%d, errors: %v",
					job.control, colourLabel(job.colour), n, len(paths), err)
			}
			e.setLastAction(fmt.Sprintf("%s: %d/%d tagged", colourLabel(job.colour), n, len(paths)))
		}
	}
}

// enqueueFinderTag offers a press to the worker, never blocking the MIDI
// callback. A full queue drops the press with a log line — see
// finderTagQueueCap.
func enqueueFinderTag(jobs chan<- finderTagJob, control, colour string) {
	select {
	case jobs <- finderTagJob{control: control, colour: colour}:
	default:
		log.Printf("[finderTag] control=%q colour=%q DROPPED — queue full (%d pending); "+
			"the previous press is still running", control, colourLabel(colour), finderTagQueueCap)
	}
}

// macappFrontmostBundleID is a tiny indirection so frontmostBundleIDFn can be
// declared without importing macapp at the var site's package scope order.
func macappFrontmostBundleID() string { return macapp.FrontmostBundleID() }
