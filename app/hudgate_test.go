package main

import "testing"

// The HUD gate's state machine, driven directly through observeResolveRunning
// so no clock, goroutine or real DaVinci Resolve is involved. Resolve is not
// running on the machine these were written on and cannot be assumed present
// anywhere, which is exactly why the process-presence check is a seam.
//
// Invariant under test throughout: visible == (user intent AND Resolve up).

func equalBools(got, want []bool) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

// newGateApp returns an App whose gate is seeded to a known state, with the
// visibility recorder already installed and drained.
func newGateApp(t *testing.T, resolveUp, userWants bool) (*App, *[]bool) {
	t.Helper()
	applied := stubHudVisible(t)
	a := &App{}
	a.seedResolveRunning(resolveUp)
	a.hudUserWants = userWants
	a.hudResolveRequired = true // the legacy gate (hudWithoutResolve off); the ON default is tested below
	*applied = nil
	return a, applied
}

// hudWithoutResolve ON (the default): Resolve closing never hides the HUD, and
// switching the setting re-asserts visibility at once.
func TestHUDGate_WithoutResolveOn_StaysVisible(t *testing.T) {
	applied := stubHudVisible(t)
	a := &App{}
	a.seedResolveRunning(false)
	a.hudUserWants = true
	*applied = nil
	if !a.applyHUDVisibility() {
		t.Fatal("HUD must show without Resolve when hudWithoutResolve is on")
	}
	a.setHUDSettings(true, false)
	if got := (*applied)[len(*applied)-1]; got {
		t.Error("turning the setting off with Resolve closed must hide the HUD")
	}
	a.setHUDSettings(true, true)
	if got := (*applied)[len(*applied)-1]; !got {
		t.Error("turning the setting on must show the HUD again")
	}
}

// hudEnabled: the general on/off hides the HUD even with Resolve running, brings
// it back, and a repeated push of the same value never undoes a C6 toggle.
func TestHUDGate_Enabled_OffHidesOnShows(t *testing.T) {
	a, applied := newGateApp(t, true, true)
	a.setHUDSettings(true, false) // first value: recorded only
	a.setHUDSettings(false, false)
	if got := (*applied)[len(*applied)-1]; got {
		t.Fatal("hudEnabled off must hide the HUD while Resolve runs")
	}
	a.setHUDSettings(true, false)
	if got := (*applied)[len(*applied)-1]; !got {
		t.Fatal("hudEnabled on must show the HUD again")
	}
	n := len(*applied)
	a.setHUDSettings(true, false) // unrelated recompose
	if len(*applied) != n {
		t.Error("an unchanged push must not touch visibility")
	}
}

// --- 1. running -> closed hides ------------------------------------------

func TestHUDGate_ResolveQuits_HidesTheHUD(t *testing.T) {
	a, applied := newGateApp(t, true, true)

	// hudFlipSamples consecutive "not running" samples are required.
	for i := 0; i < hudFlipSamples; i++ {
		a.observeResolveRunning(false)
	}

	if want := []bool{false}; !equalBools(*applied, want) {
		t.Fatalf("applied = %v, want %v (exactly one hide, on the committing sample)", *applied, want)
	}
	if a.hudResolveUp {
		t.Error("hudResolveUp = true after Resolve quit")
	}
	if !a.hudUserWants {
		t.Error("hudUserWants was cleared by the auto-hide — intent must survive so the HUD returns with Resolve")
	}
}

// --- 2. closed -> running restores ---------------------------------------

func TestHUDGate_ResolveStarts_RestoresTheHUD(t *testing.T) {
	a, applied := newGateApp(t, false, true)

	for i := 0; i < hudFlipSamples; i++ {
		a.observeResolveRunning(true)
	}

	if want := []bool{true}; !equalBools(*applied, want) {
		t.Fatalf("applied = %v, want %v", *applied, want)
	}
}

// A user who had the HUD OFF must not have it forced on just because Resolve
// launched. Resolve coming back restores his intent, it does not override it.
func TestHUDGate_ResolveStarts_StaysHiddenWhenUserDoesNotWantIt(t *testing.T) {
	a, applied := newGateApp(t, false, false)

	for i := 0; i < hudFlipSamples; i++ {
		a.observeResolveRunning(true)
	}

	if want := []bool{false}; !equalBools(*applied, want) {
		t.Fatalf("applied = %v, want %v", *applied, want)
	}
}

// --- 3. manual hide while running stays hidden ---------------------------

// The watcher must never pop the panel back up after a deliberate hide. This
// is the regression that a naive "show whenever Resolve is running" would
// cause, and it would be more annoying than the bug being fixed.
func TestHUDGate_ManualHideWhileResolveRunning_StaysHidden(t *testing.T) {
	stubResolveRunning(t, true)
	a, applied := newGateApp(t, true, true)

	if got := a.ToggleHUD(); got {
		t.Fatalf("ToggleHUD() = %v, want false (manual hide)", got)
	}

	// Many subsequent samples, all saying Resolve is still up.
	for i := 0; i < 10; i++ {
		a.observeResolveRunning(true)
	}

	for i, v := range *applied {
		if v {
			t.Fatalf("applied[%d] = true — the watcher re-showed a manually hidden HUD", i)
		}
	}
}

// --- 4. no flap on a single missed sample --------------------------------

// One transient "not running" reading (Launch Services answering late, a
// momentary hiccup) must not blink the panel. This is what hudFlipSamples
// buys, and at hudWatchInterval it costs at most a few seconds of latency.
func TestHUDGate_SingleMissedSample_DoesNotFlap(t *testing.T) {
	a, applied := newGateApp(t, true, true)

	a.observeResolveRunning(false) // one bad sample...
	a.observeResolveRunning(true)  // ...immediately contradicted

	if len(*applied) != 0 {
		t.Fatalf("applied = %v, want no visibility changes at all", *applied)
	}
	if !a.hudResolveUp {
		t.Error("hudResolveUp flipped on a single disagreeing sample")
	}
	if a.hudPending != 0 {
		t.Errorf("hudPending = %d after an agreeing sample, want 0 (the counter must reset)", a.hudPending)
	}
}

// The counter must require CONSECUTIVE disagreement, not cumulative: an
// alternating signal should never commit a flip no matter how long it runs.
func TestHUDGate_AlternatingSamples_NeverFlip(t *testing.T) {
	a, applied := newGateApp(t, true, true)

	for i := 0; i < 20; i++ {
		a.observeResolveRunning(i%2 == 1)
	}

	if len(*applied) != 0 {
		t.Fatalf("applied = %v, want no visibility changes from an alternating signal", *applied)
	}
	if !a.hudResolveUp {
		t.Error("an alternating signal committed a state flip")
	}
}

// --- intent across a full Resolve restart --------------------------------

func TestHUDGate_IntentSurvivesResolveRestart(t *testing.T) {
	stubResolveRunning(t, true)
	a, applied := newGateApp(t, true, false)

	// User switches the HUD on while Resolve is up.
	if got := a.ToggleHUD(); !got {
		t.Fatalf("ToggleHUD() = %v, want true", got)
	}
	// Resolve quits...
	for i := 0; i < hudFlipSamples; i++ {
		a.observeResolveRunning(false)
	}
	// ...and comes back.
	for i := 0; i < hudFlipSamples; i++ {
		a.observeResolveRunning(true)
	}

	if want := []bool{true, false, true}; !equalBools(*applied, want) {
		t.Fatalf("applied = %v, want %v (show, auto-hide on quit, auto-restore on relaunch)", *applied, want)
	}
}

// A toggle pressed while Resolve is closed must still be REMEMBERED, so the
// panel appears when Resolve launches. It just must not reach screen now.
func TestHUDGate_ToggleWhileResolveClosed_IsRememberedNotShown(t *testing.T) {
	stubResolveRunning(t, false)
	a, applied := newGateApp(t, false, false)

	if got := a.ToggleHUD(); got {
		t.Fatalf("ToggleHUD() with Resolve closed = %v, want false", got)
	}
	if !a.hudUserWants {
		t.Fatal("intent was not recorded — the HUD would not come back when Resolve launches")
	}

	for i := 0; i < hudFlipSamples; i++ {
		a.observeResolveRunning(true)
	}
	if want := []bool{false, true}; !equalBools(*applied, want) {
		t.Fatalf("applied = %v, want %v (nothing on screen while closed, then shown on launch)", *applied, want)
	}
}

// --- seeding --------------------------------------------------------------

// seedResolveRunning must commit immediately and apply nothing: startup calls
// it before the window exists, and it is establishing the baseline, not
// reacting to a change.
func TestSeedResolveRunning_CommitsImmediatelyAndAppliesNothing(t *testing.T) {
	applied := stubHudVisible(t)
	a := &App{}
	a.hudPending = 1

	a.seedResolveRunning(true)

	if !a.hudResolveUp {
		t.Error("seedResolveRunning(true) did not commit")
	}
	if a.hudPending != 0 {
		t.Errorf("hudPending = %d, want 0", a.hudPending)
	}
	if len(*applied) != 0 {
		t.Errorf("seedResolveRunning applied %v, want no visibility calls", *applied)
	}
}

// --- applyHUDVisibility ---------------------------------------------------

func TestApplyHUDVisibility_IsTheAndOfIntentAndPresence(t *testing.T) {
	cases := []struct {
		resolveUp, userWants, want bool
	}{
		{false, false, false},
		{false, true, false}, // the bug being fixed: wanted, but no Resolve
		{true, false, false},
		{true, true, true},
	}
	for _, c := range cases {
		a, applied := newGateApp(t, c.resolveUp, c.userWants)
		got := a.applyHUDVisibility()
		if got != c.want {
			t.Errorf("applyHUDVisibility() with resolveUp=%v userWants=%v = %v, want %v",
				c.resolveUp, c.userWants, got, c.want)
		}
		if want := []bool{c.want}; !equalBools(*applied, want) {
			t.Errorf("applied = %v, want %v", *applied, want)
		}
	}
}

// --- watcher lifecycle ----------------------------------------------------

// runHUDWatcher must exit when the window closes, like runStatusTicker.
func TestRunHUDWatcher_StopsOnTickerStop(t *testing.T) {
	stubResolveRunning(t, true)
	stubHudVisible(t)
	a := &App{tickerStop: make(chan struct{})}

	done := make(chan struct{})
	go func() { a.runHUDWatcher(); close(done) }()

	close(a.tickerStop)
	<-done // hangs (and the test times out) if the watcher ignores tickerStop
}
