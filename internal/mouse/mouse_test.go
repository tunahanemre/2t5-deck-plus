package mouse

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestDeckPlusArmedNeverSetDuringTests is the hard tripwire: it FAILS the
// suite outright if DECKPLUS_ARMED=1 is somehow present in the test
// environment. go test must never run with this variable set — if it is,
// every function below would post real CGEvent mouse input onto whatever
// is running this test suite.
func TestDeckPlusArmedNeverSetDuringTests(t *testing.T) {
	if v := os.Getenv("DECKPLUS_ARMED") + os.Getenv("LD_DECK_ARMED"); strings.Contains(v, "1") {
		t.Fatalf("DECKPLUS_ARMED=1 is set in the test environment — refusing to continue. "+
			"This variable must NEVER be set for `go test`; real mouse input would be posted "+
			"to the live desktop. (got DECKPLUS_ARMED+LD_DECK_ARMED=%q)", v)
	}
}

// TestUnarmedByDefault_NoRealInputPosted is the core safety regression test
// for this package. It proves that with DECKPLUS_ARMED unset (the only
// state `go test` should ever run in), every CGEvent-posting function takes
// the no-op path — nothing is posted to the system — while the no-op record
// gives the assertion teeth (a silently-passing test that calls real CGO
// code and just "happens" not to crash would prove nothing).
//
// This test intentionally never calls Arm() — doing so from a test would
// permanently flip the package-global armed flag for the remaining lifetime
// of this test binary process, which could cause a LATER test in this same
// binary to post real input. See envArmed_test.go-style isolation notes on
// TestEnvArmedDetection below for how the env-var mapping is tested instead.
func TestUnarmedByDefault_NoRealInputPosted(t *testing.T) {
	if Armed() {
		t.Fatal("mouse.Armed() == true with DECKPLUS_ARMED unset — arm guard is broken; " +
			"refusing to exercise posting functions in this state")
	}

	ResetNoopCalls()
	t.Cleanup(ResetNoopCalls)

	Press(1, 2)
	DragTo(3, 4)
	Release(5, 6)
	Drag(7, 8, 9, 10)
	Click(11, 12)
	DoubleClick(13, 14)

	calls := NoopCalls()
	wantPrefixes := []string{"Press", "DragTo", "Release", "Drag", "Click", "DoubleClick"}
	if len(calls) != len(wantPrefixes) {
		t.Fatalf("NoopCalls() = %d entries %v, want %d entries (one per posting function call above)",
			len(calls), calls, len(wantPrefixes))
	}
	for i, want := range wantPrefixes {
		if len(calls[i]) < len(want) || calls[i][:len(want)] != want {
			t.Errorf("NoopCalls()[%d] = %q, want prefix %q", i, calls[i], want)
		}
	}
}

// TestEnvArmedDetection unit-tests the env-var → bool mapping used by the
// arm guard, WITHOUT ever touching the real `armed` flag or calling Arm().
// envArmed() is a pure function (reads os.Getenv, returns a bool, no side
// effects), so this is safe to exercise directly with t.Setenv — it cannot
// arm this process and cannot cause any function above to post real input.
func TestEnvArmedDetection(t *testing.T) {
	cases := []struct {
		val  string
		want bool
	}{
		{"", false},
		{"0", false},
		{"true", false}, // only the exact string "1" arms — no truthy coercion
		{"TRUE", false},
		{"yes", false},
		{"1", true},
	}
	for _, tc := range cases {
		t.Run("DECKPLUS_ARMED="+tc.val, func(t *testing.T) {
			t.Setenv("DECKPLUS_ARMED", tc.val)
			if got := envArmed(); got != tc.want {
				t.Errorf("envArmed() with DECKPLUS_ARMED=%q = %v, want %v", tc.val, got, tc.want)
			}
		})
	}
}

// Deliberately no test calls CursorPos() here. CursorPos is a live CGO read
// of the real cursor position (ungated by design — see the arm-guard docs
// in mouse_darwin.go) and was never previously exercised by this package's
// test suite (there was no mouse_test.go before this file). Keeping it out
// of `go test` entirely, even though it is read-only and provably safe,
// keeps this suite's guarantee simple: zero new CGO calls introduced by
// testing, not "zero except this one we promise is harmless."

// ---------------------------------------------------------------------------
// Scroll (2026-08-13) — flagged CGEvent scroll for the "scrollDelta" binding
// type. See Scroll's doc comment in mouse_darwin.go for the full mechanism.
// ---------------------------------------------------------------------------

// TestScroll_UnarmedRecordsDeltaAndFlags is the crux regression test for
// the "scrollDelta" feature's whole premise: the EXPLICIT flags bitmask
// carried on the posted event — not the delta alone — is what makes
// Alt+scroll actually zoom Resolve's timeline natively (see Scroll's doc
// comment for why an ambient held modifier does not work). A test that
// only checked the delta would miss the entire point of this feature, so
// both are asserted explicitly here, not just "a call happened."
func TestScroll_UnarmedRecordsDeltaAndFlags(t *testing.T) {
	if Armed() {
		t.Fatal("mouse.Armed() == true with DECKPLUS_ARMED unset — arm guard is broken; " +
			"refusing to exercise Scroll() in this state")
	}

	ResetNoopCalls()
	t.Cleanup(ResetNoopCalls)

	const kCGEventFlagMaskAlternate = 0x80000 // Alt/Option, matches internal/keys.modifierTable
	Scroll(3, kCGEventFlagMaskAlternate)

	calls := NoopCalls()
	if len(calls) != 1 {
		t.Fatalf("NoopCalls() = %d entries %v, want 1", len(calls), calls)
	}
	// Same recordNoop formatting Press/DragTo/etc. above rely on
	// (fmt.Sprintf("%s%v", name, args)) — reproduced here rather than
	// string-matching substrings, so this test actually pins BOTH values,
	// not just "some number appeared somewhere."
	want := fmt.Sprintf("Scroll%v", []float64{3, kCGEventFlagMaskAlternate})
	if calls[0] != want {
		t.Errorf("NoopCalls()[0] = %q, want %q (delta=3 AND flags=0x80000 must both be recorded)", calls[0], want)
	}
}

// TestScroll_UnarmedRecordsNegativeDeltaAndZeroFlags proves the counter-
// clockwise (negative delta) case and an unmodified (flags=0) scroll also
// round-trip through the no-op log correctly, not just the positive/
// Alt-flagged case above.
func TestScroll_UnarmedRecordsNegativeDeltaAndZeroFlags(t *testing.T) {
	ResetNoopCalls()
	t.Cleanup(ResetNoopCalls)

	Scroll(-2, 0)

	calls := NoopCalls()
	if len(calls) != 1 {
		t.Fatalf("NoopCalls() = %d entries %v, want 1", len(calls), calls)
	}
	want := fmt.Sprintf("Scroll%v", []float64{-2, 0})
	if calls[0] != want {
		t.Errorf("NoopCalls()[0] = %q, want %q", calls[0], want)
	}
}

// TestScroll_DoesNotAppearInExistingPostingFunctionsList is a narrow
// regression guard: Scroll must NOT have been folded into
// TestUnarmedByDefault_NoRealInputPosted above (that test's exact 6-call,
// in-order assertion pins the pre-existing Press/DragTo/Release/Drag/Click/
// DoubleClick set) — Scroll gets its own independent coverage instead, so
// adding it never risks silently changing what that pre-existing test
// proves about the colour-wheel drag paths.
func TestScroll_DoesNotAppearInExistingPostingFunctionsList(t *testing.T) {
	ResetNoopCalls()
	t.Cleanup(ResetNoopCalls)

	Press(1, 2)
	DragTo(3, 4)
	Release(5, 6)
	Drag(7, 8, 9, 10)
	Click(11, 12)
	DoubleClick(13, 14)

	calls := NoopCalls()
	if len(calls) != 6 {
		t.Fatalf("NoopCalls() = %d entries %v, want 6 (unchanged colour-wheel drag path set, Scroll not among them)", len(calls), calls)
	}
	for _, c := range calls {
		if len(c) >= len("Scroll") && c[:len("Scroll")] == "Scroll" {
			t.Errorf("NoopCalls() unexpectedly contains a Scroll entry: %v", calls)
		}
	}
}

// Wave E (F7): the in-app pause switch. Never arms the package.
func TestShouldPostTruthTable(t *testing.T) {
	for _, c := range []struct{ armed, paused, want bool }{
		{false, false, false}, {false, true, false}, {true, false, true}, {true, true, false},
	} {
		if got := shouldPost(c.armed, c.paused); got != c.want {
			t.Errorf("shouldPost(armed=%v, paused=%v) = %v, want %v", c.armed, c.paused, got, c.want)
		}
	}
}

func TestPausedPostingIsRecordedNoops(t *testing.T) {
	if Armed() {
		t.Fatal("mouse.Armed() in a test")
	}
	SetPaused(true)
	t.Cleanup(func() { SetPaused(false); ResetNoopCalls() })
	if !Paused() {
		t.Fatal("Paused() = false after SetPaused(true)")
	}
	ResetNoopCalls()
	Click(1, 2)
	Drag(1, 2, 3, 4)
	Scroll(3, 0)
	if n := len(NoopCalls()); n != 3 {
		t.Errorf("recorded %d no-ops, want 3: %v", n, NoopCalls())
	}
}

// B6: flipping the arm switch between Press and Release must not drop the button-up.
func TestButtonLatch_ReleaseFollowsAPostedPress(t *testing.T) {
	var l buttonLatch
	if !l.press(true) {
		t.Fatal("armed press not posted")
	}
	if !l.release(false) { // paused between press and release
		t.Error("the button-up of a posted button-down was dropped")
	}
	if l.release(false) { // and only once
		t.Error("a second release was posted for one press")
	}
}

func TestButtonLatch_SuppressedPressKeepsReleaseSuppressed(t *testing.T) {
	var l buttonLatch
	if l.press(false) {
		t.Fatal("paused press posted")
	}
	if l.release(false) {
		t.Error("a release was posted for a press that never posted")
	}
	if !l.release(true) { // armed release without a press is the caller's business, as before
		t.Error("armed release suppressed")
	}
}

// The calibration dry run (MoveTo) is a posting function like the rest: unarmed
// it moves nothing and says so, and CanPost agrees.
func TestMoveTo_UnarmedMovesNothing(t *testing.T) {
	if Armed() {
		t.Fatal("armed in a test: refusing to call MoveTo")
	}
	ResetNoopCalls()
	t.Cleanup(ResetNoopCalls)
	if MoveTo(21, 22) {
		t.Fatal("MoveTo reported a move while unarmed")
	}
	if CanPost() {
		t.Fatal("CanPost true while unarmed")
	}
	calls := NoopCalls()
	if len(calls) != 1 || len(calls[0]) < 6 || calls[0][:6] != "MoveTo" {
		t.Fatalf("NoopCalls() = %v, want one MoveTo entry", calls)
	}
}
