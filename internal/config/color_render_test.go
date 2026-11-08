package config

import "testing"

func TestColorByRenderStateType(t *testing.T) {
	full := Binding{Type: "colorByRenderState", Rendered: "Green", Neutral: "Yellow"}
	if !full.IsColorByRenderState() {
		t.Error("colorByRenderState must satisfy IsColorByRenderState()")
	}
	if full.Rendered != "Green" || full.Neutral != "Yellow" {
		t.Errorf("colours = %q/%q, want Green/Yellow", full.Rendered, full.Neutral)
	}

	// "colorRenders" is the short spelling; it must mean the same thing.
	if !(Binding{Type: "colorRenders"}).IsColorByRenderState() {
		t.Error("colorRenders must alias colorByRenderState")
	}

	// The colours are optional — the sidecar defaults them — so a binding
	// carrying nothing but its type must still be recognised.
	bare := Binding{Type: "colorByRenderState"}
	if !bare.IsColorByRenderState() {
		t.Error("a bare colorByRenderState binding must still be recognised")
	}
	if bare.Rendered != "" || bare.Neutral != "" {
		t.Error("unset colours must stay empty so the sidecar can default them")
	}

	// It must not collide with the other project-touching type.
	if (Binding{Type: "resetActiveWheel"}).IsColorByRenderState() {
		t.Error("resetActiveWheel must not read as colorByRenderState")
	}
}

func TestToggleHudAndColorIsDistinct(t *testing.T) {
	both := Binding{Type: "toggleHudAndColor", Rendered: "Green", Neutral: "Yellow"}
	if !both.IsToggleHudAndColor() {
		t.Error("toggleHudAndColor must satisfy IsToggleHudAndColor()")
	}
	// It carries the same colour fields as the standalone paint action.
	if both.Rendered != "Green" || both.Neutral != "Yellow" {
		t.Errorf("colours = %q/%q, want Green/Yellow", both.Rendered, both.Neutral)
	}
	// Neither half may claim it: dispatch picks an arm by type, and an
	// overlap would make the winner depend on the order of the switch.
	if both.IsToggleHud() || both.IsColorByRenderState() {
		t.Error("toggleHudAndColor must not alias either half")
	}
	// ...and the halves must not claim the combined type either.
	if (Binding{Type: "toggleHud"}).IsToggleHudAndColor() {
		t.Error("plain toggleHud must not read as toggleHudAndColor")
	}
	if (Binding{Type: "colorByRenderState"}).IsToggleHudAndColor() {
		t.Error("plain colorByRenderState must not read as toggleHudAndColor")
	}
}

// TestColorByRenderStateIsNotAWheelBinding: the dispatch switch tests
// b.IsWheel() before reaching this case, so a false positive there would
// silently swallow the binding.
func TestColorByRenderStateIsNotAWheelBinding(t *testing.T) {
	b := Binding{Type: "colorByRenderState"}
	if b.IsWheel() || b.IsResyncAll() || b.IsResetSelected() {
		t.Error("colorByRenderState must not satisfy any other dispatch predicate")
	}
}
