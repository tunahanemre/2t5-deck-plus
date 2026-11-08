package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/finder"
)

// Finder profile tests. Everything here runs through the three package seams
// (finderSelectionFn, finderApplyTagFn, frontmostBundleIDFn) -- no Apple
// Event is ever sent, no file is ever tagged, and no CGWindowList walk
// happens, so the suite is safe on a machine with a live Finder full of
// production review folders.

// --- config helpers -------------------------------------------------------
//
// Built through the REAL config.Load (via the package's existing loadConfig
// helper) rather than a struct literal, so these tests also prove what spec
// §4 asserts: config.Load has no bank whitelist and accepts both a bank named
// for a bundle id and the new "finderTag" type.

// --- isFinderTagControl ---------------------------------------------------

func TestIsFinderTagControl_FindsPButtonsInTheFinderBank(t *testing.T) {
	cfg := loadConfig(t, `{"com.apple.finder": {
		"P1btn": {"type": "finderTag", "tag": "Red"},
		"P8btn": {"type": "finderTag", "tag": ""}
	}}`)

	b, ok := isFinderTagControl(cfg, "P1btn")
	if !ok || b.Tag != "Red" {
		t.Fatalf("isFinderTagControl(P1btn) = (%+v, %v), want a Red finderTag", b, ok)
	}
	// P8's EMPTY tag is meaningful ("clear"), not an unset field.
	b, ok = isFinderTagControl(cfg, "P8btn")
	if !ok {
		t.Fatal("isFinderTagControl(P8btn) = false — an empty Tag must still be a finderTag binding")
	}
	if b.Tag != "" {
		t.Errorf("P8btn Tag = %q, want the empty clear-colour value", b.Tag)
	}
	if _, ok := isFinderTagControl(cfg, "C1"); ok {
		t.Error("isFinderTagControl(C1) = true, want false — C1 has no Finder binding")
	}
}

// 🔴 The regression this guards: routing the Finder bank through Cascade.
// noteBankGuard whitelists binding types and falls through to `col` for
// anything else, so a Cascade lookup would resolve P1btn to its col-bank
// binding and fire the WRONG action. A control absent from the Finder bank
// must simply miss, never inherit.
func TestFinderBankBinding_DoesNotFallThroughToCol(t *testing.T) {
	cfg := loadConfig(t, `{
		"col":              {"P1btn": {"type": "key", "key": "Cmd+Z"}},
		"com.apple.finder": {"P2btn": {"type": "finderTag", "tag": "Orange"}}
	}`)

	if b, ok := finderBankBinding(cfg, "P1btn"); ok {
		t.Fatalf("finderBankBinding(P1btn) resolved to %+v — it inherited the col bank; the Finder lookup must not cascade", b)
	}
	if _, ok := isFinderTagControl(cfg, "P1btn"); ok {
		t.Fatal("isFinderTagControl(P1btn) = true via the col bank — wrong action would fire")
	}
	if _, ok := isFinderTagControl(cfg, "P2btn"); !ok {
		t.Fatal("isFinderTagControl(P2btn) = false, want true")
	}
}

func TestIsFinderTagControl_RejectsANonFinderTagTypeInTheBank(t *testing.T) {
	cfg := loadConfig(t, `{"com.apple.finder": {"P1btn": {"type": "key", "key": "Cmd+Z"}}}`)
	if _, ok := isFinderTagControl(cfg, "P1btn"); ok {
		t.Error("a non-finderTag binding in the Finder bank was accepted")
	}
}

func TestIsFinderTagControl_NilConfigIsSafe(t *testing.T) {
	if _, ok := isFinderTagControl(nil, "P1btn"); ok {
		t.Error("isFinderTagControl(nil) = true, want false")
	}
}

// --- threshold + labels ---------------------------------------------------

func TestFinderTagConfirmThreshold(t *testing.T) {
	if got := finderTagConfirmThreshold(nil); got != defaultFinderTagConfirmThreshold {
		t.Errorf("nil config threshold = %d, want %d", got, defaultFinderTagConfirmThreshold)
	}
	cfg := loadConfig(t, `{}`)
	if got := finderTagConfirmThreshold(cfg); got != defaultFinderTagConfirmThreshold {
		t.Errorf("unset threshold = %d, want the default %d", got, defaultFinderTagConfirmThreshold)
	}
	cfg.TopLevel.FinderTagConfirmThreshold = 3
	if got := finderTagConfirmThreshold(cfg); got != 3 {
		t.Errorf("configured threshold = %d, want 3", got)
	}
}

func TestColourLabel(t *testing.T) {
	if got := colourLabel(""); got != "Clear tags" {
		t.Errorf(`colourLabel("") = %q, want "Clear tags"`, got)
	}
	if got := colourLabel("Red"); got != "Red" {
		t.Errorf("colourLabel(Red) = %q", got)
	}
}

func TestIsFinderPermissionError(t *testing.T) {
	if !isFinderPermissionError(errors.New("AppleScript error -1743: Not authorized")) {
		t.Error("-1743 not recognised as a permission error")
	}
	if !isFinderPermissionError(errors.New("AppleScript error -600: not running")) {
		t.Error("-600 not recognised as a permission error")
	}
	if isFinderPermissionError(errors.New("some other failure")) {
		t.Error("an unrelated error was classed as a permission error")
	}
	if isFinderPermissionError(nil) {
		t.Error("nil classed as a permission error")
	}
}

// --- worker harness -------------------------------------------------------

type fakeFinder struct {
	sel      []string
	selErr   error
	applied  chan [2]any // {paths, colour}
	n        int
	applyErr error
}

func (f *fakeFinder) install(t *testing.T) {
	t.Helper()
	f.applied = make(chan [2]any, 16)
	origSel, origApply := finderSelectionFn, finderApplyTagFn
	finderSelectionFn = func() ([]string, error) { return f.sel, f.selErr }
	finderApplyTagFn = func(paths []string, colour string) (int, error) {
		f.applied <- [2]any{paths, colour}
		return f.n, f.applyErr
	}
	t.Cleanup(func() { finderSelectionFn, finderApplyTagFn = origSel, origApply })
}

// startWorker runs the worker for the test's lifetime and returns its queue.
func startWorker(t *testing.T, e *Engine, threshold int) chan finderTagJob {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	jobs := make(chan finderTagJob, finderTagQueueCap)
	done := make(chan struct{})
	go func() { e.finderTagWorker(ctx, jobs, threshold); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("finderTagWorker did not exit on context cancel")
		}
	})
	return jobs
}

func lastAction(e *Engine) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status.LastAction
}

// waitForAction blocks until LastAction differs from prev.
func waitForAction(t *testing.T, e *Engine, prev string) string {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s := lastAction(e); s != prev {
			return s
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for LastAction to change from %q", prev)
	return ""
}

func expectNoApply(t *testing.T, f *fakeFinder) {
	t.Helper()
	select {
	case got := <-f.applied:
		t.Fatalf("tags were applied when they must not be: %v", got)
	default:
	}
}

// --- worker behaviour -----------------------------------------------------

func TestFinderTagWorker_AppliesAndReportsCount(t *testing.T) {
	f := &fakeFinder{sel: []string{"/a.mov", "/b.mov", "/c.mov"}, n: 3}
	f.install(t)
	e := &Engine{}
	jobs := startWorker(t, e, defaultFinderTagConfirmThreshold)

	jobs <- finderTagJob{control: "P1btn", colour: "Red"}

	got := <-f.applied
	if colour := got[1].(string); colour != "Red" {
		t.Errorf("applied colour = %q, want Red", colour)
	}
	if paths := got[0].([]string); len(paths) != 3 {
		t.Errorf("applied to %d paths, want 3", len(paths))
	}
	if s := waitForAction(t, e, ""); s != "Red: 3/3 tagged" {
		t.Errorf("LastAction = %q, want %q", s, "Red: 3/3 tagged")
	}
}

// P8's clear action reports under a human label, not an empty string.
func TestFinderTagWorker_ClearActionLabels(t *testing.T) {
	f := &fakeFinder{sel: []string{"/a.mov"}, n: 1}
	f.install(t)
	e := &Engine{}
	jobs := startWorker(t, e, defaultFinderTagConfirmThreshold)

	jobs <- finderTagJob{control: "P8btn", colour: ""}

	if colour := (<-f.applied)[1].(string); colour != "" {
		t.Errorf("applied colour = %q, want the empty clear value", colour)
	}
	if s := waitForAction(t, e, ""); s != "Clear tags: 1/1 tagged" {
		t.Errorf("LastAction = %q", s)
	}
}

// Per-file tolerance: a partial batch still reports, and still counts.
func TestFinderTagWorker_PartialFailureIsReportedNotFatal(t *testing.T) {
	f := &fakeFinder{
		sel:      []string{"/a.mov", "/b.mov", "/c.mov"},
		n:        2,
		applyErr: fmt.Errorf("1 of 3 files failed: operation not supported"),
	}
	f.install(t)
	e := &Engine{}
	jobs := startWorker(t, e, defaultFinderTagConfirmThreshold)

	jobs <- finderTagJob{control: "P4btn", colour: "Green"}
	<-f.applied

	if s := waitForAction(t, e, ""); s != "Green: 2/3 tagged" {
		t.Errorf("LastAction = %q, want %q", s, "Green: 2/3 tagged")
	}
}

func TestFinderTagWorker_EmptySelectionDoesNotWrite(t *testing.T) {
	f := &fakeFinder{sel: nil}
	f.install(t)
	e := &Engine{}
	jobs := startWorker(t, e, defaultFinderTagConfirmThreshold)

	jobs <- finderTagJob{control: "P1btn", colour: "Red"}

	if s := waitForAction(t, e, ""); s != "Finder: nothing selected" {
		t.Errorf("LastAction = %q", s)
	}
	expectNoApply(t, f)
}

func TestFinderTagWorker_PermissionErrorIsLoudAndDoesNotWrite(t *testing.T) {
	f := &fakeFinder{selErr: errors.New("AppleScript error -1743: Not authorized to send Apple events to Finder.")}
	f.install(t)
	e := &Engine{}
	jobs := startWorker(t, e, defaultFinderTagConfirmThreshold)

	jobs <- finderTagJob{control: "P1btn", colour: "Red"}

	s := waitForAction(t, e, "")
	if !strings.Contains(s, "denied") || !strings.Contains(s, "Automation") {
		t.Errorf("LastAction = %q, want a visible, actionable permission error naming Automation", s)
	}
	expectNoApply(t, f)
}

// --- selection-size guard -------------------------------------------------

func TestFinderTagWorker_LargeSelectionRequiresASecondPress(t *testing.T) {
	sel := make([]string, 40)
	for i := range sel {
		sel[i] = fmt.Sprintf("/f%02d.mov", i)
	}
	f := &fakeFinder{sel: sel, n: 40}
	f.install(t)
	e := &Engine{}
	jobs := startWorker(t, e, 10)

	// First press: prompt only, nothing written.
	jobs <- finderTagJob{control: "P1btn", colour: "Red"}
	s := waitForAction(t, e, "")
	if s != "Press again to tag 40 files" {
		t.Fatalf("first press LastAction = %q, want a confirmation prompt", s)
	}
	expectNoApply(t, f)

	// Second press of the SAME button: goes through.
	jobs <- finderTagJob{control: "P1btn", colour: "Red"}
	got := <-f.applied
	if len(got[0].([]string)) != 40 {
		t.Fatalf("confirmed press applied to %d paths, want 40", len(got[0].([]string)))
	}
	if s := waitForAction(t, e, s); s != "Red: 40/40 tagged" {
		t.Errorf("LastAction = %q", s)
	}
}

// A different button is not a confirmation — it re-prompts for itself.
func TestFinderTagWorker_DifferentButtonDoesNotConfirm(t *testing.T) {
	sel := make([]string, 20)
	for i := range sel {
		sel[i] = fmt.Sprintf("/f%02d.mov", i)
	}
	f := &fakeFinder{sel: sel, n: 20}
	f.install(t)
	e := &Engine{}
	jobs := startWorker(t, e, 10)

	jobs <- finderTagJob{control: "P1btn", colour: "Red"}
	s := waitForAction(t, e, "")

	jobs <- finderTagJob{control: "P2btn", colour: "Orange"}
	// Still a prompt, still nothing written.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	expectNoApply(t, f)
	if s2 := lastAction(e); s2 != "Press again to tag 20 files" {
		t.Errorf("LastAction after the different button = %q (prev %q)", s2, s)
	}
}

func TestFinderTagWorker_ConfirmationExpires(t *testing.T) {
	orig := finderTagConfirmWindow
	finderTagConfirmWindow = 5 * time.Millisecond
	t.Cleanup(func() { finderTagConfirmWindow = orig })

	sel := make([]string, 20)
	for i := range sel {
		sel[i] = fmt.Sprintf("/f%02d.mov", i)
	}
	f := &fakeFinder{sel: sel, n: 20}
	f.install(t)
	e := &Engine{}
	jobs := startWorker(t, e, 10)

	jobs <- finderTagJob{control: "P1btn", colour: "Red"}
	waitForAction(t, e, "")

	time.Sleep(30 * time.Millisecond) // let the window lapse

	jobs <- finderTagJob{control: "P1btn", colour: "Red"}
	time.Sleep(100 * time.Millisecond)
	expectNoApply(t, f)
}

// A small selection is never gated.
func TestFinderTagWorker_SmallSelectionNeedsNoConfirmation(t *testing.T) {
	f := &fakeFinder{sel: []string{"/a.mov", "/b.mov"}, n: 2}
	f.install(t)
	e := &Engine{}
	jobs := startWorker(t, e, 10)

	jobs <- finderTagJob{control: "P1btn", colour: "Red"}
	<-f.applied // applies immediately, no prompt
}

// --- selection freshness --------------------------------------------------

// Selection is read at EXECUTION time, not press time: a press queued while
// the user was still selecting must act on the final selection.
func TestFinderTagWorker_ReadsSelectionAtExecutionTime(t *testing.T) {
	f := &fakeFinder{sel: []string{"/old.mov"}, n: 1}
	f.install(t)
	e := &Engine{}
	jobs := startWorker(t, e, defaultFinderTagConfirmThreshold)

	f.sel = []string{"/new1.mov", "/new2.mov"}
	f.n = 2
	jobs <- finderTagJob{control: "P1btn", colour: "Red"}

	paths := (<-f.applied)[0].([]string)
	if len(paths) != 2 || paths[0] != "/new1.mov" {
		t.Fatalf("applied to %v, want the selection as it was at execution time", paths)
	}
}

// --- queue bound ----------------------------------------------------------

// A full queue drops the press rather than growing an unbounded backlog of
// irreversible metadata writes that replay long after the user stopped.
func TestEnqueueFinderTag_DropsWhenFull(t *testing.T) {
	jobs := make(chan finderTagJob, finderTagQueueCap)
	for i := 0; i < finderTagQueueCap; i++ {
		enqueueFinderTag(jobs, "P1btn", "Red")
	}
	if len(jobs) != finderTagQueueCap {
		t.Fatalf("queue holds %d, want %d", len(jobs), finderTagQueueCap)
	}
	enqueueFinderTag(jobs, "P1btn", "Red") // must not block, must not grow
	if len(jobs) != finderTagQueueCap {
		t.Fatalf("queue grew past its cap: %d", len(jobs))
	}
}

// --- the SHIPPED config ---------------------------------------------------

// Loads the real config/bindings.json (precedent: layer_resolution_test.go)
// and asserts the profile is actually wired: all 8 P-buttons resolve to
// finderTag bindings with the colours the spec's table names. This is what
// catches a hand-edit that breaks the bank without touching any Go code.
func TestShippedConfig_FinderBankIsComplete(t *testing.T) {
	cfg, err := config.Load("../../config/device-map.json", "../../config/bindings.json")
	if err != nil {
		t.Fatalf("config.Load on the shipped config: %v", err)
	}

	want := map[string]string{
		"P1btn": "Red",
		"P2btn": "Orange",
		"P3btn": "Yellow",
		"P4btn": "Green",
		"P5btn": "Gray",
		"P6btn": "Blue",
		"P7btn": "Purple",
		"P8btn": "", // clear
	}
	for control, colour := range want {
		b, ok := isFinderTagControl(cfg, control)
		if !ok {
			t.Errorf("%s has no finderTag binding in the shipped %s bank", control, finderBankName)
			continue
		}
		if b.Tag != colour {
			t.Errorf("%s tag = %q, want %q", control, b.Tag, colour)
		}
	}

	// Every colour named must be one internal/finder will actually accept —
	// a typo here would only surface as a runtime error mid-review.
	for control, colour := range want {
		if colour != "" && !finder.ColourNames[colour] {
			t.Errorf("%s names %q, which is not a macOS colour tag", control, colour)
		}
	}
}
