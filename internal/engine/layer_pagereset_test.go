package engine

import (
	"testing"

	"2t5-deck-plus/internal/layer"
)

func TestLayerResetsOnPageChange(t *testing.T) {
	lay := layer.New()
	lay.Set(3)
	next := maybeResetLayerOnPageChange(lay, "color", true, "edit")
	if got := lay.Get(); got != 1 {
		t.Errorf("layer = %d after page change, want 1", got)
	}
	if next != "edit" {
		t.Errorf("remembered page = %q, want edit", next)
	}
}

func TestLayerSurvivesSamePage(t *testing.T) {
	lay := layer.New()
	lay.Set(2)
	next := maybeResetLayerOnPageChange(lay, "color", true, "color")
	if got := lay.Get(); got != 2 {
		t.Errorf("layer = %d on same page, want 2", got)
	}
	if next != "color" {
		t.Errorf("remembered page = %q, want color", next)
	}
}

// TestLayerSurvivesFailedStatusRead is the guard the review board asked for
// three times over: under script-connection contention the sidecar returns
// None, and treating that as "the page changed" would silently discard live
// state. Same shape as the D1 clip-selection auto-clear's s.ok gate.
func TestLayerSurvivesFailedStatusRead(t *testing.T) {
	lay := layer.New()
	lay.Set(2)
	next := maybeResetLayerOnPageChange(lay, "color", false, "")
	if got := lay.Get(); got != 2 {
		t.Errorf("layer = %d after FAILED read, want 2 — a transient None is not a page change", got)
	}
	if next != "color" {
		t.Errorf("remembered page = %q, want color (unchanged)", next)
	}
}

// TestLayerResetIsIdempotentAtLayerOne documents that a page change while
// already on layer 1 is a no-op rather than an error.
func TestLayerResetIsIdempotentAtLayerOne(t *testing.T) {
	lay := layer.New()
	next := maybeResetLayerOnPageChange(lay, "color", true, "fusion")
	if got := lay.Get(); got != 1 {
		t.Errorf("layer = %d, want 1", got)
	}
	if next != "fusion" {
		t.Errorf("remembered page = %q, want fusion", next)
	}
}
