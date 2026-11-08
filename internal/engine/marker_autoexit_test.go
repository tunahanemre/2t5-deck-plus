package engine

import "testing"

func TestMarkerAutoExitsWhenPlayheadLeavesRange(t *testing.T) {
	e := &Engine{activeMarkerFrame: 100, activeMarkerAbsStart: 90100, activeMarkerAbsEnd: 90125}
	maybeAutoExitMarker(e, true, true, 90500)
	if e.getActiveMarkerFrame() != -1 {
		t.Errorf("marker mode should have auto-exited, frame = %d", e.getActiveMarkerFrame())
	}
}

func TestMarkerSurvivesInsideRange(t *testing.T) {
	e := &Engine{activeMarkerFrame: 100, activeMarkerAbsStart: 90100, activeMarkerAbsEnd: 90125}
	maybeAutoExitMarker(e, true, true, 90110)
	if e.getActiveMarkerFrame() != 100 {
		t.Error("inside the range marker mode must persist")
	}
}

func TestMarkerRangeEndIsExclusive(t *testing.T) {
	e := &Engine{activeMarkerFrame: 100, activeMarkerAbsStart: 90100, activeMarkerAbsEnd: 90125}
	maybeAutoExitMarker(e, true, true, 90125)
	if e.getActiveMarkerFrame() != -1 {
		t.Error("the end frame is exclusive, so 90125 is outside")
	}
}

// TestMarkerSurvivesFailedStatusRead — the same gate as the layer and target
// resets. A transient sidecar None must never look like "the playhead left".
func TestMarkerSurvivesFailedStatusRead(t *testing.T) {
	e := &Engine{activeMarkerFrame: 100, activeMarkerAbsStart: 90100, activeMarkerAbsEnd: 90125}
	maybeAutoExitMarker(e, false, true, 90500)
	if e.getActiveMarkerFrame() != 100 {
		t.Error("a failed status read is not 'left the range'")
	}
}

// TestMarkerSurvivesUnreadablePlayhead — havePlayheadFrame false means the
// read succeeded but the playhead specifically was not obtainable.
func TestMarkerSurvivesUnreadablePlayhead(t *testing.T) {
	e := &Engine{activeMarkerFrame: 100, activeMarkerAbsStart: 90100, activeMarkerAbsEnd: 90125}
	maybeAutoExitMarker(e, true, false, 0)
	if e.getActiveMarkerFrame() != 100 {
		t.Error("an unreadable playhead is not 'left the range'")
	}
}

// TestMarkerNoAutoExitWithoutRange guards markers created before a range was
// recorded: with no known range, never auto-exit (C1 still exits explicitly).
func TestMarkerNoAutoExitWithoutRange(t *testing.T) {
	e := &Engine{activeMarkerFrame: 100} // abs range zero == unknown
	maybeAutoExitMarker(e, true, true, 90500)
	if e.getActiveMarkerFrame() != 100 {
		t.Error("with no recorded range, auto-exit must not fire")
	}
}

func TestMarkerAutoExitNoopWhenInactive(t *testing.T) {
	e := &Engine{activeMarkerFrame: -1}
	maybeAutoExitMarker(e, true, true, 90500) // must not panic or change anything
	if e.getActiveMarkerFrame() != -1 {
		t.Error("inactive marker mode stays inactive")
	}
}
