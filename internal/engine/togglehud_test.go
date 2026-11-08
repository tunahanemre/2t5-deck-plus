package engine

// Tests for the HUD toggle (C6 button): dispatchNoteOn's IsToggleHud() case.
//
// The HUD itself (internal/hud, a CGO/NSPanel package) is NEVER touched by
// these tests -- dispatchNoteOn only invokes an injected `toggleHUD func()
// bool` callback, completely decoupled from internal/hud (engine does not,
// and must never, import internal/hud). All hermetic: no CGO, no window,
// DECKPLUS_ARMED never set.

import (
	"testing"

	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/mode"
)

func TestDispatchNoteOn_ToggleHud_InvokesCallbackOnce(t *testing.T) {
	calls := 0
	toggleHUD := func() bool {
		calls++
		return true
	}

	b := config.Binding{Type: "toggleHud"}
	dispatchNoteOn("C6", b, mode.New(), nil, nil, 1, 0, func() string { return "color" }, nil, nil,
		nil, nil, nil, toggleHUD)

	if calls != 1 {
		t.Fatalf("toggleHUD callback invoked %d times, want 1", calls)
	}
}

// TestDispatchNoteOn_ToggleHud_Ungated_FiresOffPageAndNonFrontmost proves the
// design doc's "ungated -- no arming, no gate" requirement: the callback
// must fire regardless of which page queryPage() reports (unlike e.g.
// IsResetActiveWheel/IsClick, which check gate.Allow/frontmost before
// acting). dispatchNoteOn's IsToggleHud() case never calls gate.Allow or
// macapp.FrontmostBundleID at all, so this holds true no matter what the
// real frontmost app happens to be on the machine running this test --
// exercised here across several arbitrary/off-Color pages, including one
// that isn't a real Resolve page name at all, to make the point that no
// page value can suppress the fire.
func TestDispatchNoteOn_ToggleHud_Ungated_FiresOffPageAndNonFrontmost(t *testing.T) {
	pages := []string{"", "edit", "cut", "fusion", "deliver", "media", "not-a-real-page"}
	for _, page := range pages {
		t.Run(page, func(t *testing.T) {
			calls := 0
			toggleHUD := func() bool {
				calls++
				return false
			}
			b := config.Binding{Type: "toggleHud"}
			dispatchNoteOn("C6", b, mode.New(), nil, nil, 1, 0, func() string { return page }, nil, nil,
				nil, nil, nil, toggleHUD)

			if calls != 1 {
				t.Errorf("page=%q: toggleHUD callback invoked %d times, want 1 (ungated)", page, calls)
			}
		})
	}
}

// TestDispatchNoteOn_ToggleHud_NilCallback_NoPanic mirrors the headless CLI
// (cmd/2t5-deck-plus/main.go), which never calls SetToggleHUDCallback, so
// Engine.toggleHUD stays nil and reaches dispatchNoteOn as a nil func value.
func TestDispatchNoteOn_ToggleHud_NilCallback_NoPanic(t *testing.T) {
	b := config.Binding{Type: "toggleHud"}
	dispatchNoteOn("C6", b, mode.New(), nil, nil, 1, 0, func() string { return "color" }, nil, nil,
		nil, nil, nil, nil)
	// No panic = pass.
}
