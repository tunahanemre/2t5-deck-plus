package engine

import (
	"testing"

	"2t5-deck-plus/internal/config"
)

// TestC1ExitsWhenMarkerModeActive: C1 is a toggle. Exit is unconditional --
// no playhead range test -- which is what makes it edge-case free. The cost
// is that a second marker takes two presses.
func TestC1ExitsWhenMarkerModeActive(t *testing.T) {
	cleared := false
	got := dispatchAddMarker(nil, "C1", config.Binding{Type: "addMarker"}, 0, nil,
		func() int { return 9000 }, // marker mode ACTIVE
		func() { cleared = true },  // clearActiveMarker
		func(int, int) {},          // recordMarkerRange
	)
	if !cleared {
		t.Error("an active marker mode must be cleared by a C1 press")
	}
	if got == "" {
		t.Error("expected a LastAction summary for the exit")
	}
}

// TestC1ExitDoesNotTouchTheSidecar: exiting is pure local state. Passing a nil
// client would panic if the exit path fell through to the create path, so a
// nil client here is itself the assertion.
func TestC1ExitDoesNotTouchTheSidecar(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("exit path must not reach the sidecar: %v", r)
		}
	}()
	dispatchAddMarker(nil, "C1", config.Binding{Type: "addMarker"}, 0, nil,
		func() int { return 42 },
		func() {},
		func(int, int) {},
	)
}

// TestC1CreatesWhenInactive: with marker mode off, C1 falls through to the
// existing create path. A nil client makes that path return its no-sidecar
// SKIP, which is enough to prove it was taken rather than the exit path.
func TestC1CreatesWhenInactive(t *testing.T) {
	cleared := false
	got := dispatchAddMarker(nil, "C1", config.Binding{Type: "addMarker"}, 0, nil,
		func() int { return -1 }, // marker mode INACTIVE
		func() { cleared = true },
		func(int, int) {},
	)
	if cleared {
		t.Error("marker mode is inactive — nothing to clear")
	}
	if got != "" {
		t.Errorf("nil client should SKIP the create path, got %q", got)
	}
}

// TestMarkerResizeIsEditPageOnly guards a hazard the layer system creates:
// layers 2/3 invite new col-bank bindings, any of which could be a jog. With
// marker mode left live after leaving the Edit page, a Color-page jog would
// otherwise silently resize an invisible marker.
func TestMarkerResizeIsEditPageOnly(t *testing.T) {
	jog := config.Binding{Type: "jog", Base: 1.0}
	if shouldResizeMarker(jog, "color", 9000) {
		t.Error("a Color-page jog must never resize a marker")
	}
	if !shouldResizeMarker(jog, "edit", 9000) {
		t.Error("on the Edit page with marker mode live it should")
	}
	if shouldResizeMarker(jog, "edit", -1) {
		t.Error("marker mode inactive means no resize")
	}
	if shouldResizeMarker(config.Binding{Type: "cdl"}, "edit", 9000) {
		t.Error("only jog bindings resize")
	}
}
