package preset

import (
	"strings"
	"testing"
)

// Wave I: plain-language summaries, no engine ids.
func TestSummaries_Table(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	cases := []struct {
		name string
		a    map[string]any
		want string
	}{
		// frames / power
		{"jog 1 frame", map[string]any{"type": "jog", "base": 1, "gamma": 1}, "Scrub timeline (frames): 1 frame, steady (curve 1.0)"},
		{"jog plural", map[string]any{"type": "jog", "base": 2, "gamma": 2.2}, "Scrub timeline (frames): 2 frames, accelerates (curve 2.2)"},
		{"jog default curve omitted", map[string]any{"type": "jog", "base": 3}, "Scrub timeline (frames): 3 frames"},
		{"jog fraction", map[string]any{"type": "jog", "base": 0.5}, "Scrub timeline (frames): 0.5 frames"},
		{"jog seconds singular", map[string]any{"type": "jogSeconds", "secondsPerStep": 1}, "Jump timeline (seconds): 1 second"},
		{"scroll singular line", map[string]any{"type": "scrollDelta", "linesPerDetent": 1, "detentThreshold": 2}, "Turn scrolls: 1 line per step, 2 raw clicks"},
		{"scroll negative one", map[string]any{"type": "scrollDelta", "linesPerDetent": -1}, "Turn scrolls: -1 line per step"},
		// properties
		{"gang", map[string]any{"type": "property", "gang": []any{"ZoomX", "ZoomY"}, "sens": 0.005}, "Zoom X + Zoom Y, 0.005 units per notch"},
		{"single", map[string]any{"type": "property", "property": "CropTop", "sens": 1}, "Crop Top, 1 unit per notch"},
		{"single with default sens", map[string]any{"type": "property", "property": "Pan"}, "Pan, 1 unit per notch"},
		{"custom label keeps the description", map[string]any{"type": "property", "property": "CropTop", "sens": 2, "label": "Crop"}, "Crop: Crop Top, 2 units per notch"},
		// wheels, sliders, cdl, balance
		{"wheel", map[string]any{"type": "wheel", "wheel": "lift_master", "sens": 15}, "Lift master wheel, 15 px per notch"},
		{"slider", map[string]any{"type": "wheel", "wheel": "temp_slider", "sens": 15}, "Temp slider, 15 px per notch"},
		{"cdl", map[string]any{"type": "cdl", "param": "slope", "ch": "r", "sens": 4}, "Slope red, 4 px per notch"},
		{"cdl sat", map[string]any{"type": "cdl", "param": "sat", "ch": "master"}, "Saturation, 15 px per notch"},
		{"balance", map[string]any{"type": "balance", "hue": 0, "sens": 8}, "Hue: red (0), 8 px per notch"},
		{"balance magenta", map[string]any{"type": "balance", "hue": 7}, "Hue: magenta (7), 15 px per notch"},
		// shortcuts
		{"label equals key", map[string]any{"type": "key", "key": "Delete", "label": "Delete"}, "Delete"},
		{"synonym", map[string]any{"type": "key", "key": "Return", "label": "Enter"}, "Enter"},
		{"case and punctuation", map[string]any{"type": "key", "key": "Escape", "label": "Esc"}, "Esc"},
		{"label differs", map[string]any{"type": "key", "key": "Space", "label": "Play/Pause"}, "Play/Pause (Space)"},
		{"no label", map[string]any{"type": "key", "key": "Cmd+T"}, "Keyboard shortcut (Cmd+T)"},
		{"undo", map[string]any{"type": "key", "key": "Cmd+Z", "label": "Undo"}, "Undo (Cmd+Z)"},
		// ids in other fields
		{"finder control-like id", map[string]any{"type": "markerAtPlayhead", "markerName": "P1btn"}, "Marker colour at playhead: P1 button"},
		{"path is left alone", map[string]any{"type": "shell", "cmd": "/usr/bin/open"}, "Run a command: /usr/bin/open"},
	}
	for i, c := range cases {
		ctl := "C" + string(rune('1'+i%9))
		bank := "base"
		if i >= 9 {
			bank = "shift"
		}
		if i >= 18 {
			bank = "layer-2"
		}
		if err := s.Assign(chrome, bank, ctl, c.a); err != nil {
			t.Fatalf("%s: assign: %v", c.name, err)
		}
		e, ok := s.Snapshot().Effective(chrome, bank, ctl)
		if !ok {
			t.Fatalf("%s: no effective", c.name)
		}
		if e.Summary != c.want {
			t.Errorf("%s: summary %q, want %q", c.name, e.Summary, c.want)
		}
		// the raw ids stay in the data
		for k, v := range c.a {
			if k == "label" {
				continue
			}
			if _, ok := e.Raw[k]; !ok || (k != "type" && e.Fields[k].Value == nil && v != nil) {
				t.Errorf("%s: field %q lost from the data", c.name, k)
			}
		}
	}
}

func TestHumanize(t *testing.T) {
	for in, want := range map[string]string{
		"CropTop": "Crop Top", "ZoomX": "Zoom X", "Pan": "Pan", "lift_master": "Lift master", "temp_slider": "Temp slider",
		"P1btn": "P1 button", "/usr/bin/open": "/usr/bin/open", "two words": "two words", "": "",
	} {
		if got := humanize(in); got != want {
			t.Errorf("humanize(%q) = %q, want %q", in, got, want)
		}
	}
	if looksLikeID("Safari") || looksLikeID("Red") || !looksLikeID("lift_master") || !looksLikeID("CropTop") || !looksLikeID("P1btn") {
		t.Error("looksLikeID")
	}
	if unitFor("frames", 1) != "frame" || unitFor("frames", 2) != "frames" || unitFor("px per click", 1) != "px per notch" ||
		unitFor("lines per step", 1) != "line per step" || unitFor("raw clicks", 1) != "raw click" || unitFor("units per click", 1) != "unit per notch" || unitFor("units per click", 0.005) != "units per notch" {
		t.Error("unitFor")
	}
}

// The seeded stores (real Resolve bank cascade, Finder) read in plain words: no ids, brackets or "1 frames".
func TestSummaries_SeededStoreHasNoEngineIds(t *testing.T) {
	s, _ := seededStore(t)
	n := 0
	for _, p := range s.Snapshot().Presets() {
		for bn, bank := range p.Banks {
			for cn := range bank {
				e, ok := s.Snapshot().Effective(p.ID, bn, cn)
				if !ok {
					continue
				}
				n++
				for _, bad := range []string{"_master", "_slider", "[", "]", "1 frames", "1 power", "btn", "0-7"} {
					if strings.Contains(e.Summary, bad) {
						t.Errorf("%s %s.%s: summary %q contains %q", p.ID, bn, cn, e.Summary, bad)
					}
				}
			}
		}
	}
	if n < 50 {
		t.Errorf("only %d summaries checked", n)
	}
}
