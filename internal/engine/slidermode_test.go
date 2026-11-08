package engine

// Slider mode (Hue / Sat / Lum) as a layer of P1-P8 / P1btn-P8btn.
// Hermetic, like keydebounce_test.go: fake sidecar socket, stubbed MIDI, no
// frontmost-app dependence (property presses reach the sidecar ungated), no real
// input and no hardware.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"

	"2t5-deck-plus/internal/calib"
	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/mode"
	"2t5-deck-plus/internal/target"
)

func newPModeStateForTest() *target.State {
	t := target.New()
	t.Set("hue")
	return t
}

func loadModeCfg(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	dm := filepath.Join(dir, "device-map.json")
	bf := filepath.Join(dir, "bindings.json")
	if err := os.WriteFile(dm, []byte(modeDeviceMap()), 0o644); err != nil {
		t.Fatal(err)
	}
	b := `{"col":{"P1":{"type":"balance","hue":0,"sens":8}},
	  "col-shift":{"P1":{"type":"key","key":"x"}},
	  "col-pm-sat":{"P1":{"type":"hueCurve","curve":"sat","hue":0,"sens":6}}}`
	if err := os.WriteFile(bf, []byte(b), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(dm, bf)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func calibWith(t *testing.T, key string, x, y float64) *calib.Calib {
	t.Helper()
	f := filepath.Join(t.TempDir(), "calibration.json")
	js := fmt.Sprintf(`{"points":{%q:{"x":%v,"y":%v,"axis":"v"}}}`, key, x, y)
	if err := os.WriteFile(f, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := calib.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func modeDeviceMap() string {
	return `{"knobs":{},"sliders":{},"buttons":{"80":"P1btn","81":"P2btn","98":"Hue","99":"Sat","100":"Lum","117":"CustomMode","38":"Temperature"}}`
}

func modeBindings(withModes bool) string {
	b := `{
		"_config": {"keyDebounceMs": 20},
		"col": {"Hue": {"type":"pmode","pmode":"hue"}, "Sat": {"type":"pmode","pmode":"sat"}, "Lum": {"type":"pmode","pmode":"lum"}},
		"edit": {
			"P1btn": {"type": "property", "property": "CropTop"},
			"P2btn": {"type": "property", "property": "CropLeft"},
			"Temperature": {"type": "property", "property": "CropRight"}
		}`
	if withModes {
		b += `,
		"col-pm-sat": {"P1btn": {"type": "property", "property": "CropBottom"}},
		"edit-pm-lum": {"P1btn": {"type": "property", "property": "ZoomX"}}`
	}
	return b + "}"
}

func pressAndWait(t *testing.T, cb func(midi.Message, int32), note uint8) {
	t.Helper()
	cb(midi.NoteOn(0, note, 100), 0)
	time.Sleep(60 * time.Millisecond) // clear the 20 ms debounce window
}

func lastProperty(received func() []map[string]any) string {
	r := received()
	if len(r) == 0 {
		return ""
	}
	p, _ := r[len(r)-1]["property"].(string)
	return p
}

func TestSliderModeIsALayerForTheP1BtnKeys(t *testing.T) {
	eng := newEngineWithConfig(t, modeDeviceMap(), modeBindings(true))
	sockPath, received := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)
	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()
	cb := *cbPtr

	// Hue mode (default): no hue bank -> today's behaviour (edit P1btn).
	pressAndWait(t, cb, 80)
	waitForRecs(t, received, 1)
	if got := lastProperty(received); got != "CropTop" {
		t.Fatalf("hue mode P1btn = %q, want the ordinary edit binding CropTop", got)
	}
	// Sat: col-pm-sat wins (page-independent), and it is engine state.
	pressAndWait(t, cb, 99)
	if m := eng.SliderMode(); m != "sat" {
		t.Fatalf("SliderMode = %q after Sat, want sat", m)
	}
	pressAndWait(t, cb, 80)
	waitForRecs(t, received, 2)
	if got := lastProperty(received); got != "CropBottom" {
		t.Fatalf("sat mode P1btn = %q, want CropBottom", got)
	}
	// A P key with no sat assignment falls back; a non-P control is never touched by the mode.
	pressAndWait(t, cb, 81)
	waitForRecs(t, received, 3)
	if got := lastProperty(received); got != "CropLeft" {
		t.Fatalf("sat mode P2btn = %q, want fallback CropLeft", got)
	}
	pressAndWait(t, cb, 38)
	waitForRecs(t, received, 4)
	if got := lastProperty(received); got != "CropRight" {
		t.Fatalf("Temperature must ignore the mode, got %q", got)
	}
	// Lum: the page bank edit-pm-lum is tried before col-pm-lum.
	pressAndWait(t, cb, 100)
	pressAndWait(t, cb, 80)
	waitForRecs(t, received, 5)
	if got := lastProperty(received); got != "ZoomX" {
		t.Fatalf("lum mode P1btn = %q, want ZoomX", got)
	}
	// Back to Hue: the original binding again.
	pressAndWait(t, cb, 98)
	pressAndWait(t, cb, 80)
	waitForRecs(t, received, 6)
	if got := lastProperty(received); got != "CropTop" {
		t.Fatalf("back in hue mode P1btn = %q, want CropTop", got)
	}
}

func TestNoModeDataBehavesAsBefore(t *testing.T) {
	eng := newEngineWithConfig(t, modeDeviceMap(), modeBindings(false))
	sockPath, received := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)
	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()
	cb := *cbPtr
	n := 0
	for _, key := range []uint8{99, 100, 98} { // every mode
		pressAndWait(t, cb, key)
		pressAndWait(t, cb, 80)
		n++
		waitForRecs(t, received, n)
		if got := lastProperty(received); got != "CropTop" {
			t.Fatalf("with no per-mode data, P1btn must stay CropTop in every mode; got %q after key %d", got, key)
		}
	}
}

func TestCustomModeIsEngineStateAndLEDsFollow(t *testing.T) {
	eng := newEngineWithConfig(t, modeDeviceMap(), modeBindings(false))
	sockPath, _ := startRecordingSidecar(t, "edit")
	eng.sockPath = sockPath
	stubSidecarLaunch(t)
	cbPtr := stubMIDICapture(t)
	var mu sync.Mutex
	var seen []string
	eng.SetLEDSink(func(m string, c bool) {
		mu.Lock()
		seen = append(seen, m+map[bool]string{true: "+custom", false: ""}[c])
		mu.Unlock()
	})
	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer eng.Stop()
	cb := *cbPtr
	if eng.CustomMode() {
		t.Fatal("Custom Mode must start off")
	}
	pressAndWait(t, cb, 117)
	if !eng.CustomMode() {
		t.Fatal("CustomMode press must turn the toggle on")
	}
	pressAndWait(t, cb, 99)
	pressAndWait(t, cb, 117)
	if eng.CustomMode() {
		t.Fatal("second CustomMode press must turn it off")
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{"", "hue", "hue+custom", "sat+custom", "sat"}
	// first entry is SetLEDSink's immediate push (mode unknown before Start), then Start's restore push.
	if len(seen) < len(want) {
		t.Fatalf("LED sink saw %v, want at least %v", seen, want)
	}
	tail := seen[len(seen)-3:]
	if tail[0] != "hue+custom" || tail[1] != "sat+custom" || tail[2] != "sat" {
		t.Fatalf("LED sink tail = %v, want [hue+custom sat+custom sat]", tail)
	}
}

func TestTrackDeckStateWhenAnotherAppIsInFront(t *testing.T) {
	e := &Engine{}
	pm := newPModeStateForTest()
	var got []string
	s := LEDSink(func(m string, c bool) { got = append(got, m) })
	e.SetLEDSink(s)
	if nm := e.trackDeckState("Lum", pm); nm != "lum" || pm.Get() != "lum" {
		t.Fatalf("Lum press: new=%q state=%q", nm, pm.Get())
	}
	if nm := e.trackDeckState("P1", pm); nm != "" || pm.Get() != "lum" {
		t.Fatal("a non-mode control must not touch the mode")
	}
	e.trackDeckState("CustomMode", pm)
	if !e.CustomMode() {
		t.Fatal("CustomMode toggle not tracked")
	}
}

func TestResolveTurnBindingModeFallsThrough(t *testing.T) {
	cfg := loadModeCfg(t)
	// no mode / unknown mode / non-P control: identical to the plain resolver
	for _, c := range []struct{ control, pmode string }{{"P1", ""}, {"P1", "bogus"}, {"Temperature", "sat"}} {
		a, abank, aok := resolveTurnBinding(cfg, "col", 1, c.control, "")
		b, bbank, bok := resolveTurnBindingMode(cfg, "col", 1, c.control, "", c.pmode)
		if aok != bok || abank != bbank || a.Type != b.Type || a.Hue != b.Hue {
			t.Errorf("%v: plain %v/%s vs mode %v/%s", c, aok, abank, bok, bbank)
		}
	}
	b, bank, ok := resolveTurnBindingMode(cfg, "col", 1, "P1", "", "sat")
	if !ok || bank != "col-pm-sat" || !b.IsHueCurve() || b.HueCurveKey() != "hvs_red" {
		t.Fatalf("sat P1 = %+v bank=%q ok=%v", b, bank, ok)
	}
	// hue mode has no hue bank: the ordinary col balance binding.
	b, bank, ok = resolveTurnBindingMode(cfg, "col", 1, "P1", "", "hue")
	if !ok || bank != "col" || !b.IsBalance() {
		t.Fatalf("hue P1 = %+v bank=%q", b, bank)
	}
	// a held modifier overlay still beats the mode
	b, bank, ok = resolveTurnBindingMode(cfg, "col", 1, "P1", "shift", "sat")
	if !ok || bank != "col-shift" {
		t.Fatalf("overlay must win over the mode, got %q", bank)
	}
}

func TestDispatchHueCurveRefusesWithoutCalibrationOrGate(t *testing.T) {
	var drags int
	old := mouseDragFn
	mouseDragFn = func(x1, y1, x2, y2 float64) { drags++ }
	defer func() { mouseDragFn = old }()
	tr := mode.New()
	page := func() string { return "color" }
	good := config.Binding{Type: "hueCurve", Curve: "sat", Hue: 0, Sens: 6}

	if got := dispatchHueCurve("P1", 3, good, tr, nil, page); got != "" || drags != 0 {
		t.Fatalf("no calibration must refuse, got %q drags=%d", got, drags)
	}
	if got := dispatchHueCurve("P1", 3, good, tr, calib.Empty(), page); got != "" || drags != 0 {
		t.Fatalf("uncalibrated target must refuse, got %q drags=%d", got, drags)
	}
	bad := good
	bad.Hue = 9
	if got := dispatchHueCurve("P1", 3, bad, tr, calib.Empty(), page); got != "" || drags != 0 {
		t.Fatal("hue out of range must refuse")
	}
	bad = good
	bad.Curve = "hue"
	if got := dispatchHueCurve("P1", 3, bad, tr, calib.Empty(), page); got != "" || drags != 0 {
		t.Fatal("unknown curve must refuse")
	}
	// calibrated, but the page is not Color: the gate refuses (Resolve is not frontmost under test either)
	cal := calibWith(t, "hvs_red", 100, 200)
	if got := dispatchHueCurve("P1", 3, good, tr, cal, func() string { return "edit" }); got != "" || drags != 0 {
		t.Fatalf("gate must refuse off the Color page, got %q drags=%d", got, drags)
	}
}
