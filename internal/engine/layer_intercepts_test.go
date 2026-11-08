package engine

import "testing"

func TestEditBankBindingPrefersLayer(t *testing.T) {
	cfg := loadConfig(t, `{
	  "edit":   {"C1": {"type":"addMarker"}},
	  "edit-2": {"C1": {"type":"key","key":"Cmd+K"}}
	}`)
	b, ok := editBankBinding(cfg, "edit", 2, "C1", "")
	if !ok || !b.IsKey() {
		t.Errorf("layer 2 must win: IsKey=%v ok=%v", b.IsKey(), ok)
	}
	b1, ok1 := editBankBinding(cfg, "edit", 1, "C1", "")
	if !ok1 || !b1.IsAddMarker() {
		t.Error("layer 1 must be unchanged")
	}
}

func TestEditBankBindingInheritsWhenLayerSilent(t *testing.T) {
	cfg := loadConfig(t, `{
	  "edit":   {"C1": {"type":"addMarker"}},
	  "edit-2": {"Up": {"type":"key","key":"Cmd+K"}}
	}`)
	b, ok := editBankBinding(cfg, "edit", 2, "C1", "")
	if !ok || !b.IsAddMarker() {
		t.Error("a control the layer does not redefine must inherit from edit")
	}
}

// TestAllSevenInterceptsAreLayerAware is the guard for the review board's
// CRITICAL finding. These seven controls are intercepted in the raw MIDI
// callback and never reach resolveNoteBinding, so a test suite built only
// against the cascade would pass green while every one of them silently
// ignored layers. Each therefore needs its own assertion here.
func TestAllSevenInterceptsAreLayerAware(t *testing.T) {
	cfg := loadConfig(t, `{
	  "edit": {
	    "D1":          {"type":"selectClip"},
	    "Up":          {"type":"selectClipVert"},
	    "Left":        {"type":"selectClipHoriz"},
	    "C5":          {"type":"selectClipClear"},
	    "C1":          {"type":"addMarker"},
	    "ControlDial": {"type":"jog","base":1.0},
	    "P1btn":       {"type":"markerColor"}
	  },
	  "edit-2": {
	    "D1":          {"type":"key","key":"Cmd+1"},
	    "Up":          {"type":"key","key":"Cmd+2"},
	    "Left":        {"type":"key","key":"Cmd+3"},
	    "C5":          {"type":"key","key":"Cmd+4"},
	    "C1":          {"type":"key","key":"Cmd+5"},
	    "ControlDial": {"type":"key","key":"Cmd+6"},
	    "P1btn":       {"type":"key","key":"Cmd+7"}
	  }
	}`)

	// At layer 1 each intercept must still claim its control.
	claims := []struct {
		name    string
		control string
		fn      func(control string, layer int) bool
	}{
		{"isSelectClipRelease", "D1", func(c string, l int) bool { return isSelectClipRelease(cfg, "edit", l, c, "") }},
		{"isSelectClipVert", "Up", func(c string, l int) bool { return isSelectClipVert(cfg, "edit", l, c, "") }},
		{"isSelectClipHorizControl", "Left", func(c string, l int) bool { return isSelectClipHorizControl(cfg, "edit", l, c, "") }},
		{"isSelectClipClear", "C5", func(c string, l int) bool { return isSelectClipClear(cfg, "edit", l, c, "") }},
		{"isAddMarkerControl", "C1", func(c string, l int) bool { return isAddMarkerControl(cfg, "edit", l, c, "") }},
		{"isJogPressControl", "ControlDial", func(c string, l int) bool { return isJogPressControl(cfg, "edit", l, c, "") }},
		{"isMarkerColorControl", "P1btn", func(c string, l int) bool { return isMarkerColorControl(cfg, "edit", l, c, "") }},
	}

	for _, c := range claims {
		if !c.fn(c.control, 1) {
			t.Errorf("%s must still claim %q at layer 1", c.name, c.control)
		}
		if c.fn(c.control, 2) {
			t.Errorf("%s must NOT claim %q at layer 2 — the layer redefines it as a key", c.name, c.control)
		}
	}
}
