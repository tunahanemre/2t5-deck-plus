package config

import (
	"encoding/json"
	"os"
	"testing"
)

func TestLoad(t *testing.T) {
	c, err := Load("testdata/device-map.json", "testdata/bindings.json")
	if err != nil {
		t.Fatal(err)
	}

	// Knob 36 (Saturation): CC and note must both resolve.
	if got := c.ControlForCC(36); got != "Saturation" {
		t.Errorf("ControlForCC(36) = %q, want %q", got, "Saturation")
	}
	if got := c.ControlForNote(36); got != "Saturation" {
		t.Errorf("ControlForNote(36) = %q, want %q", got, "Saturation")
	}

	// Slider 25 (P1): CC and note must both resolve.
	if got := c.ControlForCC(25); got != "P1" {
		t.Errorf("ControlForCC(25) = %q, want %q", got, "P1")
	}
	if got := c.ControlForNote(25); got != "P1" {
		t.Errorf("ControlForNote(25) = %q, want %q", got, "P1")
	}

	// Button 65 (Col): note-only; CC must not resolve.
	if got := c.ControlForNote(65); got != "Col" {
		t.Errorf("ControlForNote(65) = %q, want %q", got, "Col")
	}
	if got := c.ControlForCC(65); got != "" {
		t.Errorf("ControlForCC(65) = %q, want empty (buttons are note-only)", got)
	}

	// Button 79 (Right): note-only.
	if got := c.ControlForNote(79); got != "Right" {
		t.Errorf("ControlForNote(79) = %q, want %q", got, "Right")
	}

	// Binding lookup.
	b, ok := c.Binding("col", "Saturation")
	if !ok || b.Param != "sat" || b.Ch != "master" {
		t.Errorf("Binding(col, Saturation) = %+v ok=%v", b, ok)
	}

	// Missing binding must be !ok.
	if _, ok := c.Binding("col", "nope"); ok {
		t.Errorf("missing binding should be !ok")
	}

	// Wheel binding with explicit Sens (uses legacy "method" field).
	wb, ok := c.Binding("col", "D1")
	if !ok {
		t.Fatal("D1 binding missing")
	}
	if !wb.IsWheel() {
		t.Errorf("D1 should be a wheel binding, got method=%q type=%q", wb.Method, wb.Type)
	}
	if wb.Wheel != "lift_master" {
		t.Errorf("D1 wheel = %q, want %q", wb.Wheel, "lift_master")
	}
	if wb.Sens != 20 {
		t.Errorf("D1 sens = %v, want 20", wb.Sens)
	}

	// CDL binding must have Sens == 0 (omitted in JSON).
	if sat, _ := c.Binding("col", "Saturation"); sat.Sens != 0 {
		t.Errorf("Saturation sens = %v, want 0 (omitted)", sat.Sens)
	}

	// D1 has no maxDrag → zero (omitted).
	if d1, _ := c.Binding("col", "D1"); d1.MaxDrag != 0 {
		t.Errorf("D1 maxDrag = %v, want 0 (omitted)", d1.MaxDrag)
	}

	// D2 has explicit maxDrag 30.
	d2, ok := c.Binding("col", "D2")
	if !ok {
		t.Fatal("D2 binding missing")
	}
	if d2.MaxDrag != 30.0 {
		t.Errorf("D2 maxDrag = %v, want 30.0", d2.MaxDrag)
	}

	// Click binding (L1 -> mode_wheels).
	l1, ok := c.Binding("col", "L1")
	if !ok {
		t.Fatal("L1 binding missing")
	}
	if !l1.IsClick() {
		t.Errorf("L1 should be a click binding, got type=%q method=%q", l1.Type, l1.Method)
	}
	if l1.Point != "mode_wheels" {
		t.Errorf("L1 point = %q, want %q", l1.Point, "mode_wheels")
	}
	if l1.Mode != "wheels" {
		t.Errorf("L1 mode = %q, want %q", l1.Mode, "wheels")
	}

	// Key binding (Undo -> Cmd+Z).
	undo, ok := c.Binding("col", "Undo")
	if !ok {
		t.Fatal("Undo binding missing")
	}
	if !undo.IsKey() {
		t.Errorf("Undo should be a key binding, got type=%q method=%q", undo.Type, undo.Method)
	}
	if undo.Key != "Cmd+Z" {
		t.Errorf("Undo key = %q, want %q", undo.Key, "Cmd+Z")
	}

	// L2 bars mode.
	l2, ok := c.Binding("col", "L2")
	if !ok {
		t.Fatal("L2 binding missing")
	}
	if l2.Mode != "bars" {
		t.Errorf("L2 mode = %q, want bars", l2.Mode)
	}

	// L3 log mode.
	l3, ok := c.Binding("col", "L3")
	if !ok {
		t.Fatal("L3 binding missing")
	}
	if l3.Mode != "log" {
		t.Errorf("L3 mode = %q, want log", l3.Mode)
	}
}

// TestPressActionParsing verifies that the "press" object on wheel/cdl bindings
// is parsed correctly and that omitted press fields remain nil.
func TestPressActionParsing(t *testing.T) {
	c, err := Load("testdata/device-map.json", "testdata/bindings.json")
	if err != nil {
		t.Fatal(err)
	}

	// D1 has no press field → nil.
	d1, _ := c.Binding("col", "D1")
	if d1.Press != nil {
		t.Errorf("D1.Press = %+v, want nil", d1.Press)
	}

	// Temp: wheel binding with press→key.
	temp, ok := c.Binding("col", "Temp")
	if !ok {
		t.Fatal("Temp binding missing")
	}
	if !temp.IsWheel() {
		t.Errorf("Temp should be a wheel binding")
	}
	if temp.Press == nil {
		t.Fatal("Temp.Press is nil, want key action")
	}
	if temp.Press.Type != "key" {
		t.Errorf("Temp.Press.Type = %q, want %q", temp.Press.Type, "key")
	}
	if temp.Press.Key != "Cmd+Z" {
		t.Errorf("Temp.Press.Key = %q, want %q", temp.Press.Key, "Cmd+Z")
	}

	// Tint: wheel binding with press→none.
	tint, ok := c.Binding("col", "Tint")
	if !ok {
		t.Fatal("Tint binding missing")
	}
	if tint.Press == nil {
		t.Fatal("Tint.Press is nil, want none action")
	}
	if tint.Press.Type != "none" {
		t.Errorf("Tint.Press.Type = %q, want %q", tint.Press.Type, "none")
	}

	// WheelClick: wheel binding with press→click.
	wc, ok := c.Binding("col", "WheelClick")
	if !ok {
		t.Fatal("WheelClick binding missing")
	}
	if wc.Press == nil {
		t.Fatal("WheelClick.Press is nil, want click action")
	}
	if wc.Press.Type != "click" {
		t.Errorf("WheelClick.Press.Type = %q, want %q", wc.Press.Type, "click")
	}
	if wc.Press.Point != "gain_reset" {
		t.Errorf("WheelClick.Press.Point = %q, want %q", wc.Press.Point, "gain_reset")
	}

	// CDLKey: cdl binding with press→key.
	ck, ok := c.Binding("col", "CDLKey")
	if !ok {
		t.Fatal("CDLKey binding missing")
	}
	if ck.Press == nil {
		t.Fatal("CDLKey.Press is nil, want key action")
	}
	if ck.Press.Type != "key" {
		t.Errorf("CDLKey.Press.Type = %q, want %q", ck.Press.Type, "key")
	}
	if ck.Press.Key != "Ctrl+Z" {
		t.Errorf("CDLKey.Press.Key = %q, want %q", ck.Press.Key, "Ctrl+Z")
	}
}

// ---------------------------------------------------------------------------
// Helpers for inline JSON config loading.
// ---------------------------------------------------------------------------

// minimalDM is the smallest device-map JSON accepted by Load.
const minimalDM = `{"knobs":{"36":"Knob1"},"sliders":{},"buttons":{}}`

func writeTmpFile(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "*.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

func loadInline(t *testing.T, bindingsJSON string) *Config {
	t.Helper()
	dm := writeTmpFile(t, minimalDM)
	bn := writeTmpFile(t, bindingsJSON)
	cfg, err := Load(dm, bn)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// ---------------------------------------------------------------------------
// TestNewBindingTypes — balance, selectWheel, resetActiveWheel parsing.
// ---------------------------------------------------------------------------

func TestNewBindingTypes(t *testing.T) {
	cfg := loadInline(t, `{
		"col": {
			"P1": {"type": "balance", "hue": 0, "sens": 8},
			"P5": {"type": "balance", "hue": 4, "sens": 8},
			"Exposure": {"type": "wheel", "wheel": "lift_master", "sens": 15,
				"press": {"type": "selectWheel", "wheel": "lift"}},
			"L1": {"type": "resetActiveWheel"}
		}
	}`)

	// P1: balance hue=0
	p1, ok := cfg.Binding("col", "P1")
	if !ok {
		t.Fatal("P1 binding missing")
	}
	if !p1.IsBalance() {
		t.Errorf("P1 should be balance, got type=%q", p1.Type)
	}
	if p1.Hue != 0 {
		t.Errorf("P1.Hue = %d, want 0", p1.Hue)
	}
	if p1.Sens != 8 {
		t.Errorf("P1.Sens = %v, want 8", p1.Sens)
	}

	// P5: balance hue=4 (Cyan)
	p5, ok := cfg.Binding("col", "P5")
	if !ok {
		t.Fatal("P5 binding missing")
	}
	if !p5.IsBalance() {
		t.Errorf("P5 should be balance")
	}
	if p5.Hue != 4 {
		t.Errorf("P5.Hue = %d, want 4", p5.Hue)
	}

	// Exposure: wheel with press→selectWheel
	exp, ok := cfg.Binding("col", "Exposure")
	if !ok {
		t.Fatal("Exposure binding missing")
	}
	if !exp.IsWheel() {
		t.Errorf("Exposure should be wheel")
	}
	if exp.Press == nil {
		t.Fatal("Exposure.Press is nil, want selectWheel")
	}
	if exp.Press.Type != "selectWheel" {
		t.Errorf("Exposure.Press.Type = %q, want selectWheel", exp.Press.Type)
	}
	if exp.Press.Wheel != "lift" {
		t.Errorf("Exposure.Press.Wheel = %q, want lift", exp.Press.Wheel)
	}

	// L1: resetActiveWheel
	l1, ok := cfg.Binding("col", "L1")
	if !ok {
		t.Fatal("L1 binding missing")
	}
	if !l1.IsResetActiveWheel() {
		t.Errorf("L1 should be resetActiveWheel, got type=%q", l1.Type)
	}
}

// ---------------------------------------------------------------------------
// TestTopLevelDefaults — ensure defaults are applied when _config is absent.
// ---------------------------------------------------------------------------

func TestTopLevelDefaults(t *testing.T) {
	cfg := loadInline(t, `{"col":{}}`)

	if cfg.TopLevel.HueOffsetsDeg != defaultHueOffsetsDeg {
		t.Errorf("default HueOffsetsDeg = %v, want %v", cfg.TopLevel.HueOffsetsDeg, defaultHueOffsetsDeg)
	}
	if cfg.TopLevel.CoalesceWindowMs != defaultCoalesceWindowMs {
		t.Errorf("default CoalesceWindowMs = %d, want %d", cfg.TopLevel.CoalesceWindowMs, defaultCoalesceWindowMs)
	}
	if cfg.TopLevel.PxPerUnit != defaultPxPerUnit {
		t.Errorf("default PxPerUnit = %v, want %v", cfg.TopLevel.PxPerUnit, defaultPxPerUnit)
	}
	if cfg.TopLevel.MaxScrubPx != defaultMaxScrubPx {
		t.Errorf("default MaxScrubPx = %v, want %v", cfg.TopLevel.MaxScrubPx, defaultMaxScrubPx)
	}
	if cfg.TopLevel.MinScrubPx != defaultMinScrubPx {
		t.Errorf("default MinScrubPx = %v, want %v", cfg.TopLevel.MinScrubPx, defaultMinScrubPx)
	}
	if cfg.TopLevel.StartupReset != defaultStartupReset {
		t.Errorf("default StartupReset = %q, want %q", cfg.TopLevel.StartupReset, defaultStartupReset)
	}
	if cfg.TopLevel.HighlightEnabled == nil || *cfg.TopLevel.HighlightEnabled != true {
		t.Errorf("default HighlightEnabled = %v, want true", cfg.TopLevel.HighlightEnabled)
	}
	if cfg.TopLevel.HighlightColor != defaultHighlightColor {
		t.Errorf("default HighlightColor = %q, want %q", cfg.TopLevel.HighlightColor, defaultHighlightColor)
	}
	if cfg.TopLevel.ReleaseColor != defaultReleaseColor {
		t.Errorf("default ReleaseColor = %q, want %q", cfg.TopLevel.ReleaseColor, defaultReleaseColor)
	}
	if cfg.TopLevel.KeyDebounceMs != defaultKeyDebounceMs {
		t.Errorf("default KeyDebounceMs = %d, want %d", cfg.TopLevel.KeyDebounceMs, defaultKeyDebounceMs)
	}
	if cfg.TopLevel.SelectionGraceMs != defaultSelectionGraceMs {
		t.Errorf("default SelectionGraceMs = %d, want %d", cfg.TopLevel.SelectionGraceMs, defaultSelectionGraceMs)
	}
}

// TestTopLevelKeyDebounceMsOverride -- BUG 1 (2026-08-12): an explicit
// _config.keyDebounceMs value is honoured.
func TestTopLevelKeyDebounceMsOverride(t *testing.T) {
	cfg := loadInline(t, `{
		"_config": {"keyDebounceMs": 120},
		"col": {}
	}`)
	if cfg.TopLevel.KeyDebounceMs != 120 {
		t.Errorf("KeyDebounceMs = %d, want 120", cfg.TopLevel.KeyDebounceMs)
	}
}

// TestTopLevelSelectionGraceMsOverride -- BUG 3 (2026-08-12): an explicit
// _config.selectionGraceMs value is honoured.
func TestTopLevelSelectionGraceMsOverride(t *testing.T) {
	cfg := loadInline(t, `{
		"_config": {"selectionGraceMs": 1000},
		"col": {}
	}`)
	if cfg.TopLevel.SelectionGraceMs != 1000 {
		t.Errorf("SelectionGraceMs = %d, want 1000", cfg.TopLevel.SelectionGraceMs)
	}
}

// TestTopLevelReleaseColorOverride — D1 knob-press "release/normalize"
// config: an explicit _config.releaseColor value is honoured.
func TestTopLevelReleaseColorOverride(t *testing.T) {
	cfg := loadInline(t, `{
		"_config": {"releaseColor": "Orange"},
		"col": {}
	}`)
	if cfg.TopLevel.ReleaseColor != "Orange" {
		t.Errorf("ReleaseColor = %q, want Orange", cfg.TopLevel.ReleaseColor)
	}
}

// TestTopLevelHighlightOverride — D1 active-clip highlight config: explicit
// _config.highlightEnabled/highlightColor values are honoured, including an
// explicit "false" (which must NOT be silently coerced back to the true
// default -- HighlightEnabled is a *bool for exactly this reason).
func TestTopLevelHighlightOverride(t *testing.T) {
	cfg := loadInline(t, `{
		"_config": {"highlightEnabled": false, "highlightColor": "Pink"},
		"col": {}
	}`)
	if cfg.TopLevel.HighlightEnabled == nil || *cfg.TopLevel.HighlightEnabled != false {
		t.Errorf("HighlightEnabled = %v, want false", cfg.TopLevel.HighlightEnabled)
	}
	if cfg.TopLevel.HighlightColor != "Pink" {
		t.Errorf("HighlightColor = %q, want Pink", cfg.TopLevel.HighlightColor)
	}
}

// TestTopLevelHighlightOverride_EnabledTrueExplicit — explicit true is also
// preserved (not just relying on the nil-default coincidentally matching).
func TestTopLevelHighlightOverride_EnabledTrueExplicit(t *testing.T) {
	cfg := loadInline(t, `{
		"_config": {"highlightEnabled": true},
		"col": {}
	}`)
	if cfg.TopLevel.HighlightEnabled == nil || *cfg.TopLevel.HighlightEnabled != true {
		t.Errorf("HighlightEnabled = %v, want true", cfg.TopLevel.HighlightEnabled)
	}
	if cfg.TopLevel.HighlightColor != defaultHighlightColor {
		t.Errorf("HighlightColor = %q, want default %q (unset in this test)", cfg.TopLevel.HighlightColor, defaultHighlightColor)
	}
}

// TestTopLevelOverride — explicit _config values are honoured.
func TestTopLevelOverride(t *testing.T) {
	// Partial override: pxPerUnit and hueOffsetsDeg.
	bindsJSON := `{
		"_config": {"pxPerUnit": 15.0, "maxScrubPx": 100.0},
		"col": {}
	}`
	cfg := loadInline(t, bindsJSON)
	if cfg.TopLevel.PxPerUnit != 15.0 {
		t.Errorf("PxPerUnit = %v, want 15.0", cfg.TopLevel.PxPerUnit)
	}
	if cfg.TopLevel.MaxScrubPx != 100.0 {
		t.Errorf("MaxScrubPx = %v, want 100.0", cfg.TopLevel.MaxScrubPx)
	}
	// HueOffsetsDeg was omitted → all zeros parsed → default should be applied.
	if cfg.TopLevel.HueOffsetsDeg != defaultHueOffsetsDeg {
		t.Errorf("HueOffsetsDeg = %v, want default %v", cfg.TopLevel.HueOffsetsDeg, defaultHueOffsetsDeg)
	}
}

// TestTopLevelFullOverride — all-non-zero custom offsets are preserved as-is.
func TestTopLevelFullOverride(t *testing.T) {
	custom := [8]float64{0, 40, 100, 160, 220, 265, 295, 325}
	b, _ := json.Marshal(custom)
	bindsJSON := `{"_config":{"hueOffsetsDeg":` + string(b) + `},"col":{}}`
	cfg := loadInline(t, bindsJSON)
	if cfg.TopLevel.HueOffsetsDeg != custom {
		t.Errorf("HueOffsetsDeg = %v, want %v", cfg.TopLevel.HueOffsetsDeg, custom)
	}
}

// TestBindingTypeClassification covers IsCDL/IsWheel/IsClick/IsKey.
func TestBindingTypeClassification(t *testing.T) {
	cases := []struct {
		name    string
		b       Binding
		isCDL   bool
		isWheel bool
		isClick bool
		isKey   bool
	}{
		{"empty is cdl", Binding{}, true, false, false, false},
		{"method=cdl", Binding{Method: "cdl"}, true, false, false, false},
		{"type=cdl", Binding{Type: "cdl"}, true, false, false, false},
		{"method=wheel", Binding{Method: "wheel"}, false, true, false, false},
		{"type=wheel", Binding{Type: "wheel"}, false, true, false, false},
		{"type=click", Binding{Type: "click"}, false, false, true, false},
		{"type=key", Binding{Type: "key"}, false, false, false, true},
		{"type wins over method", Binding{Type: "click", Method: "wheel"}, false, false, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.b.IsCDL(); got != tc.isCDL {
				t.Errorf("IsCDL = %v, want %v", got, tc.isCDL)
			}
			if got := tc.b.IsWheel(); got != tc.isWheel {
				t.Errorf("IsWheel = %v, want %v", got, tc.isWheel)
			}
			if got := tc.b.IsClick(); got != tc.isClick {
				t.Errorf("IsClick = %v, want %v", got, tc.isClick)
			}
			if got := tc.b.IsKey(); got != tc.isKey {
				t.Errorf("IsKey = %v, want %v", got, tc.isKey)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Phase 2: property/jog/quickexport binding types.
// ---------------------------------------------------------------------------

func TestPropertyBinding_SingleProperty(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"Contrast": {"type": "property", "property": "Pan", "sens": 2.0, "maxDelta": 40}
		}
	}`)
	b, ok := cfg.Binding("edit", "Contrast")
	if !ok {
		t.Fatal("Contrast binding missing")
	}
	if !b.IsProperty() {
		t.Errorf("Contrast should be a property binding, got type=%q", b.Type)
	}
	if b.Property != "Pan" {
		t.Errorf("Contrast.Property = %q, want Pan", b.Property)
	}
	if b.Sens != 2.0 {
		t.Errorf("Contrast.Sens = %v, want 2.0", b.Sens)
	}
	if b.MaxDelta != 40 {
		t.Errorf("Contrast.MaxDelta = %v, want 40", b.MaxDelta)
	}
	if len(b.Gang) != 0 {
		t.Errorf("Contrast.Gang = %v, want empty", b.Gang)
	}
}

func TestPropertyBinding_Gang(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"Exposure": {"type": "property", "gang": ["ZoomX", "ZoomY"], "sens": 0.01, "maxDelta": 0.5}
		}
	}`)
	b, ok := cfg.Binding("edit", "Exposure")
	if !ok {
		t.Fatal("Exposure binding missing")
	}
	if !b.IsProperty() {
		t.Errorf("Exposure should be a property binding")
	}
	if len(b.Gang) != 2 || b.Gang[0] != "ZoomX" || b.Gang[1] != "ZoomY" {
		t.Errorf("Exposure.Gang = %v, want [ZoomX ZoomY]", b.Gang)
	}
	if b.Property != "" {
		t.Errorf("Exposure.Property = %q, want empty (gang mode)", b.Property)
	}
}

func TestPropertyBinding_MinMax(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"Temperature": {"type": "property", "property": "CropTop", "sens": 5, "min": 0, "max": 100}
		}
	}`)
	b, ok := cfg.Binding("edit", "Temperature")
	if !ok {
		t.Fatal("Temperature binding missing")
	}
	if b.Min == nil || *b.Min != 0 {
		t.Errorf("Temperature.Min = %v, want pointer to 0", b.Min)
	}
	if b.Max == nil || *b.Max != 100 {
		t.Errorf("Temperature.Max = %v, want pointer to 100", b.Max)
	}
}

func TestPropertyBinding_MinMaxOmittedAreNil(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"Vibrance": {"type": "property", "property": "CropLeft", "sens": 5}
		}
	}`)
	b, ok := cfg.Binding("edit", "Vibrance")
	if !ok {
		t.Fatal("Vibrance binding missing")
	}
	if b.Min != nil {
		t.Errorf("Vibrance.Min = %v, want nil (omitted)", b.Min)
	}
	if b.Max != nil {
		t.Errorf("Vibrance.Max = %v, want nil (omitted)", b.Max)
	}
}

func TestJogBinding(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"ControlDial": {"type": "jog", "maxDelta": 30}
		}
	}`)
	b, ok := cfg.Binding("edit", "ControlDial")
	if !ok {
		t.Fatal("ControlDial binding missing")
	}
	if !b.IsJog() {
		t.Errorf("ControlDial should be a jog binding, got type=%q", b.Type)
	}
	if b.MaxDelta != 30 {
		t.Errorf("ControlDial.MaxDelta = %v, want 30", b.MaxDelta)
	}
}

func TestJogBinding_SnapTierThresholds(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"ControlDial": {"type": "jog", "maxDelta": 90, "jogTier5Min": 4, "jogTier10Min": 8, "jogTier30Min": 13}
		}
	}`)
	b, ok := cfg.Binding("edit", "ControlDial")
	if !ok {
		t.Fatal("ControlDial binding missing")
	}
	if b.JogTier5Min != 4 {
		t.Errorf("ControlDial.JogTier5Min = %v, want 4", b.JogTier5Min)
	}
	if b.JogTier10Min != 8 {
		t.Errorf("ControlDial.JogTier10Min = %v, want 8", b.JogTier10Min)
	}
	if b.JogTier30Min != 13 {
		t.Errorf("ControlDial.JogTier30Min = %v, want 13", b.JogTier30Min)
	}
}

// TestRealBindingsConfig_ControlDial_SnapTierThresholds loads the ACTUAL
// repo config/bindings.json (not an inline/testdata fixture) to guard
// against JSON typos/drift in the shipped ControlDial jog binding -- the
// speed-tiered jog feature is only useful if these thresholds are
// actually wired into the file the app loads at runtime.
func TestRealBindingsConfig_ControlDial_SnapTierThresholds(t *testing.T) {
	cfg, err := Load("../../config/device-map.json", "../../config/bindings.json")
	if err != nil {
		t.Fatalf("Load real repo config: %v", err)
	}
	b, ok := cfg.Binding("edit", "ControlDial")
	if !ok {
		t.Fatal("edit.ControlDial binding missing from config/bindings.json")
	}
	if !b.IsJog() {
		t.Fatalf("edit.ControlDial should be type=jog, got %q", b.Type)
	}
	if b.JogTier5Min <= 0 || b.JogTier10Min <= 0 || b.JogTier30Min <= 0 {
		t.Errorf("edit.ControlDial jog tier thresholds should be explicitly set in bindings.json (editable without rebuild), got %d/%d/%d",
			b.JogTier5Min, b.JogTier10Min, b.JogTier30Min)
	}
	if !(b.JogTier5Min < b.JogTier10Min && b.JogTier10Min < b.JogTier30Min) {
		t.Errorf("jog tier thresholds should be strictly increasing (5min < 10min < 30min), got %d/%d/%d",
			b.JogTier5Min, b.JogTier10Min, b.JogTier30Min)
	}
}

// TestRealBindingsConfig_ControlDial_SustainThresholds mirrors
// TestRealBindingsConfig_ControlDial_SnapTierThresholds above, loading the
// ACTUAL repo config/bindings.json to guard against JSON typos/drift in
// the shipped sustained-spin escalation thresholds.
//
// The "JogSustainSeconds should be a COARSER tier than JogTier30Min"
// ordering check this test used to run unconditionally is only meaningful
// while JogTier30Min holds a REAL, reachable step-count threshold (e.g. the
// default 13) -- comparing it against JogSustainSeconds (a SECONDS value)
// was always a numeric proxy for "sustained spin should escalate PAST the
// fastest ordinary speed tier, not undercut it," not a same-unit
// comparison. 2026-07-08's "ControlDial = linear fine jog" config
// deliberately disables the speed tiers with huge sentinel step-counts
// (jogTier5Min/10Min/30Min ~999997-999999 -- see
// TestRealBindingsConfig_ControlDial_SnapTierThresholds above) so
// ControlDial behaves as pure accelerated fine-frame jog; once
// JogTier30Min is a disable-sentinel rather than a real threshold, the
// ordering proxy is vacuous (60 will never exceed a value in the
// hundreds-of-thousands), not a real regression. What still matters when
// tiers are disabled: the sustain-escalation TRIGGER (JogSustainMs) must
// ALSO be practically unreachable, so the two mechanisms are consistently
// off together -- a config that disabled only the speed tiers while
// leaving sustain-escalation reachable within, say, a few seconds would be
// a real (if surprising) bug this test should still catch.
func TestRealBindingsConfig_ControlDial_SustainThresholds(t *testing.T) {
	cfg, err := Load("../../config/device-map.json", "../../config/bindings.json")
	if err != nil {
		t.Fatalf("Load real repo config: %v", err)
	}
	b, ok := cfg.Binding("edit", "ControlDial")
	if !ok {
		t.Fatal("edit.ControlDial binding missing from config/bindings.json")
	}
	if b.JogSustainMs <= 0 {
		t.Errorf("edit.ControlDial.JogSustainMs should be explicitly set in bindings.json (editable without rebuild), got %d", b.JogSustainMs)
	}
	if b.JogSustainSeconds <= 0 {
		t.Errorf("edit.ControlDial.JogSustainSeconds should be explicitly set in bindings.json (editable without rebuild), got %d", b.JogSustainSeconds)
	}

	// disabledTierSentinelFloor: any JogTier30Min at or above this is
	// unambiguously a "disable this tier" sentinel, not a plausible real
	// step-count threshold reachable by a human turning a physical dial
	// (the package default is 13; even a very fast/long spin tops out far
	// below five figures per coalesced drain tick).
	const disabledTierSentinelFloor = 100000
	if b.JogTier30Min >= disabledTierSentinelFloor {
		// practicallyUnreachableMs: 24 hours of CONTINUOUS, uninterrupted
		// dial spin -- nobody triggers this by accident, so treating
		// JogSustainMs at or above it as "also disabled" (consistent with
		// the disabled speed tiers) is safe.
		const practicallyUnreachableMs = 24 * 60 * 60 * 1000
		if b.JogSustainMs < practicallyUnreachableMs {
			t.Errorf("edit.ControlDial: speed tiers are disabled (JogTier30Min=%d) but JogSustainMs=%d is still practically reachable -- sustain-escalation would silently re-enable a coarse jump the disabled speed tiers were supposed to prevent",
				b.JogTier30Min, b.JogSustainMs)
		}
		return
	}
	if b.JogSustainSeconds <= b.JogTier30Min {
		t.Errorf("JogSustainSeconds (%d) should be a COARSER tier than JogTier30Min (%d) -- sustained spin escalates past the fastest speed tier, not below it",
			b.JogSustainSeconds, b.JogTier30Min)
	}
}

func TestJogBinding_SnapTierThresholdsOmittedAreZero(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"ControlDial": {"type": "jog", "maxDelta": 90}
		}
	}`)
	b, ok := cfg.Binding("edit", "ControlDial")
	if !ok {
		t.Fatal("ControlDial binding missing")
	}
	if b.JogTier5Min != 0 || b.JogTier10Min != 0 || b.JogTier30Min != 0 {
		t.Errorf("ControlDial jog tier thresholds should default to zero (package defaults apply downstream), got %d/%d/%d",
			b.JogTier5Min, b.JogTier10Min, b.JogTier30Min)
	}
}

// ---------------------------------------------------------------------------
// JogSustainMs / JogSustainSeconds -- sustained-spin escalation thresholds
// (see internal/tc.JogStreak/SustainSnapSeconds, which the engine calls
// with these exact Binding values).
// ---------------------------------------------------------------------------

func TestJogBinding_SustainFields_Parsed(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"ControlDial": {"type": "jog", "maxDelta": 90, "jogSustainMs": 1200, "jogSustainSeconds": 90}
		}
	}`)
	b, ok := cfg.Binding("edit", "ControlDial")
	if !ok {
		t.Fatal("ControlDial binding missing")
	}
	if b.JogSustainMs != 1200 {
		t.Errorf("ControlDial.JogSustainMs = %v, want 1200", b.JogSustainMs)
	}
	if b.JogSustainSeconds != 90 {
		t.Errorf("ControlDial.JogSustainSeconds = %v, want 90", b.JogSustainSeconds)
	}
}

func TestJogBinding_SustainFieldsOmittedAreZero(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"ControlDial": {"type": "jog", "maxDelta": 90}
		}
	}`)
	b, ok := cfg.Binding("edit", "ControlDial")
	if !ok {
		t.Fatal("ControlDial binding missing")
	}
	if b.JogSustainMs != 0 || b.JogSustainSeconds != 0 {
		t.Errorf("ControlDial sustain fields should default to zero (internal/tc package defaults apply downstream), got %d/%d",
			b.JogSustainMs, b.JogSustainSeconds)
	}
}

func TestQuickExportBinding(t *testing.T) {
	cfg := loadInline(t, `{
		"col": {
			"C5": {"type": "quickexport"}
		}
	}`)
	b, ok := cfg.Binding("col", "C5")
	if !ok {
		t.Fatal("C5 binding missing")
	}
	if !b.IsQuickExport() {
		t.Errorf("C5 should be a quickexport binding, got type=%q", b.Type)
	}
}

func TestPropertyJogQuickExport_NotOtherTypes(t *testing.T) {
	prop := Binding{Type: "property"}
	jog := Binding{Type: "jog"}
	qe := Binding{Type: "quickexport"}

	if prop.IsJog() || prop.IsQuickExport() || prop.IsWheel() || prop.IsCDL() {
		t.Errorf("property binding incorrectly classified: %+v", prop)
	}
	if jog.IsProperty() || jog.IsQuickExport() || jog.IsCDL() {
		t.Errorf("jog binding incorrectly classified: %+v", jog)
	}
	if qe.IsProperty() || qe.IsJog() || qe.IsCDL() {
		t.Errorf("quickexport binding incorrectly classified: %+v", qe)
	}
}

// TestEditBankSiblingOfCol verifies that an "edit" top-level key parses as
// its own independent bank (page-as-bank design), coexisting with "col".
func TestEditBankSiblingOfCol(t *testing.T) {
	cfg := loadInline(t, `{
		"col": {"Exposure": {"type": "wheel", "wheel": "lift_master"}},
		"edit": {"Exposure": {"type": "property", "gang": ["ZoomX","ZoomY"], "sens": 0.01}}
	}`)
	colB, ok := cfg.Binding("col", "Exposure")
	if !ok || !colB.IsWheel() {
		t.Errorf("col/Exposure should be a wheel binding, got %+v ok=%v", colB, ok)
	}
	editB, ok := cfg.Binding("edit", "Exposure")
	if !ok || !editB.IsProperty() {
		t.Errorf("edit/Exposure should be a property binding, got %+v ok=%v", editB, ok)
	}
}

// ---------------------------------------------------------------------------
// D1 "clip-select" feature: selectClip binding type.
// ---------------------------------------------------------------------------

func TestSelectClipBinding(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"D1": {"type": "selectClip"}
		}
	}`)
	b, ok := cfg.Binding("edit", "D1")
	if !ok {
		t.Fatal("D1 binding missing")
	}
	if !b.IsSelectClip() {
		t.Errorf("D1 should be a selectClip binding, got type=%q", b.Type)
	}
}

func TestSelectClipBinding_NotOtherTypes(t *testing.T) {
	sc := Binding{Type: "selectClip"}
	if sc.IsProperty() || sc.IsJog() || sc.IsQuickExport() || sc.IsCDL() || sc.IsWheel() {
		t.Errorf("selectClip binding incorrectly classified: %+v", sc)
	}
	prop := Binding{Type: "property"}
	jog := Binding{Type: "jog"}
	qe := Binding{Type: "quickexport"}
	if prop.IsSelectClip() || jog.IsSelectClip() || qe.IsSelectClip() {
		t.Errorf("other binding types incorrectly classified as selectClip: prop=%v jog=%v qe=%v",
			prop.IsSelectClip(), jog.IsSelectClip(), qe.IsSelectClip())
	}
}

// TestRealBindingsConfig_D1_JogMinuteSnapBinding loads the ACTUAL repo
// config/bindings.json and confirms D1 is bound to "jogMinuteSnap" (the
// whole-minute playhead-snap feature) -- NOT "jogSeconds", NOT
// "selectClip", and NOT unbound. This replaces the former
// TestRealBindingsConfig_D1_JogSecondsBinding, which guarded the PRIOR
// D1 binding (a fixed ±60s coarse jump) before it was superseded by this
// snap-to-whole-minute-grid gesture. Both selectClip's and jogSeconds'
// underlying Go/Python machinery are intentionally left in place
// (unreachable in production, still directly unit-tested by their own
// inline fixtures elsewhere in this file/package) -- only the REAL
// config file's D1 binding itself changes across these features.
func TestRealBindingsConfig_D1_JogMinuteSnapBinding(t *testing.T) {
	cfg, err := Load("../../config/device-map.json", "../../config/bindings.json")
	if err != nil {
		t.Fatalf("Load real repo config: %v", err)
	}
	b, ok := cfg.Binding("edit", "D1")
	if !ok {
		t.Fatal("edit.D1 binding missing from config/bindings.json")
	}
	if !b.IsJogMinuteSnap() {
		t.Fatalf("edit.D1 should be type=jogMinuteSnap, got %q", b.Type)
	}
	if b.IsJogSeconds() {
		t.Error("edit.D1 must not ALSO classify as jogSeconds")
	}
	if b.IsSelectClip() {
		t.Error("edit.D1 must not ALSO classify as selectClip")
	}
}

// TestRealBindingsConfig_D1_UnboundOnColBank confirms D1's "col" (Color
// page) bank binding is untouched by the jogMinuteSnap feature -- D1 stays
// unbound there, same as before (jogMinuteSnap is Edit-page only).
func TestRealBindingsConfig_D1_UnboundOnColBank(t *testing.T) {
	cfg, err := Load("../../config/device-map.json", "../../config/bindings.json")
	if err != nil {
		t.Fatalf("Load real repo config: %v", err)
	}
	if b, ok := cfg.Binding("col", "D1"); ok {
		t.Fatalf("col.D1 should be unbound (jogMinuteSnap is Edit-page only), got type=%q", b.Type)
	}
}

// ---------------------------------------------------------------------------
// "keyDelta" (2026-08-13): knob rotation -> repeated keystroke. First use:
// Shift+D1 -> Resolve timeline zoom (config/bindings.json's edit-shift.D1).
// ---------------------------------------------------------------------------

func TestKeyDeltaBinding(t *testing.T) {
	cfg := loadInline(t, `{
		"edit-shift": {
			"D1": {"type": "keyDelta", "keyCW": "Cmd+=", "keyCCW": "Cmd+-"}
		}
	}`)
	b, ok := cfg.Binding("edit-shift", "D1")
	if !ok {
		t.Fatal("edit-shift.D1 binding missing")
	}
	if !b.IsKeyDelta() {
		t.Errorf("edit-shift.D1 should be a keyDelta binding, got type=%q", b.Type)
	}
	if b.KeyCW != "Cmd+=" {
		t.Errorf("KeyCW = %q, want Cmd+=", b.KeyCW)
	}
	if b.KeyCCW != "Cmd+-" {
		t.Errorf("KeyCCW = %q, want Cmd+-", b.KeyCCW)
	}
}

func TestKeyDeltaBinding_NotOtherTypes(t *testing.T) {
	kd := Binding{Type: "keyDelta", KeyCW: "Cmd+=", KeyCCW: "Cmd+-"}
	if kd.IsKey() || kd.IsProperty() || kd.IsJog() || kd.IsJogSeconds() || kd.IsJogMinuteSnap() || kd.IsSelectClip() || kd.IsCDL() || kd.IsWheel() {
		t.Errorf("keyDelta binding incorrectly classified: %+v", kd)
	}
	key := Binding{Type: "key", Key: "Cmd+="}
	if key.IsKeyDelta() {
		t.Errorf("a plain key binding must not classify as keyDelta: %+v", key)
	}
}

func TestKeyDeltaBinding_DetentThreshold(t *testing.T) {
	cfg := loadInline(t, `{
		"edit-shift": {
			"D1": {"type": "keyDelta", "keyCW": "Cmd+=", "keyCCW": "Cmd+-", "detentThreshold": 2}
		}
	}`)
	b, ok := cfg.Binding("edit-shift", "D1")
	if !ok || b.DetentThreshold != 2 {
		t.Errorf("edit-shift.D1 = %+v ok=%v, want detentThreshold=2", b, ok)
	}
}

// TestRealBindingsConfig_D1_ShiftScrollDeltaBinding loads the ACTUAL repo
// config/bindings.json and confirms edit-shift.D1 is now the Shift+D1
// timeline-zoom scrollDelta binding (2026-08-13, superseding the earlier
// keyDelta version this test used to pin -- see
// TestRealBindingsConfig_D1_ShiftKeyDeltaBinding's git history: the
// keyDelta approach's guessed "Cmd+=" Zoom In shortcut turned out not to
// work live while "Cmd+-" Zoom Out did, so D1 moved to posting a flagged
// scroll event that makes Resolve zoom via its own native Alt+scroll
// gesture instead of guessing at a keyboard shortcut at all). Also
// confirms plain edit.D1 (no Shift held) remains UNCHANGED -- still
// jogMinuteSnap, never shadowed by the edit-shift bank.
func TestRealBindingsConfig_D1_ShiftScrollDeltaBinding(t *testing.T) {
	cfg, err := Load("../../config/device-map.json", "../../config/bindings.json")
	if err != nil {
		t.Fatalf("Load real repo config: %v", err)
	}
	b, ok := cfg.Binding("edit-shift", "D1")
	if !ok {
		t.Fatal("edit-shift.D1 binding missing from config/bindings.json")
	}
	if !b.IsScrollDelta() {
		t.Fatalf("edit-shift.D1 should be type=scrollDelta, got %q", b.Type)
	}
	if b.Flags != "Alt" {
		t.Errorf("edit-shift.D1.Flags = %q, want \"Alt\"", b.Flags)
	}
	if b.LinesPerDetent != 1 {
		t.Errorf("edit-shift.D1.LinesPerDetent = %d, want 1", b.LinesPerDetent)
	}

	// keyDelta itself must still be a live, working type in this codebase
	// (kept for other controls) -- this binding just no longer uses it.
	if b.IsKeyDelta() {
		t.Error("edit-shift.D1 should NOT also classify as keyDelta")
	}

	// Plain edit.D1 (no Shift held) must be UNTOUCHED by this change.
	plain, ok := cfg.Binding("edit", "D1")
	if !ok || !plain.IsJogMinuteSnap() {
		t.Errorf("edit.D1 = %+v ok=%v, want unchanged type=jogMinuteSnap", plain, ok)
	}
}

// ---------------------------------------------------------------------------
// "scrollDelta" (2026-08-13): knob rotation -> flagged CGEvent scroll.
// Replaces keyDelta on edit-shift.D1 -- see IsScrollDelta's doc comment for
// why. keyDelta itself is untouched and still directly tested above.
// ---------------------------------------------------------------------------

func TestScrollDeltaBinding(t *testing.T) {
	cfg := loadInline(t, `{
		"edit-shift": {
			"D1": {"type": "scrollDelta", "flags": "Alt", "linesPerDetent": 1}
		}
	}`)
	b, ok := cfg.Binding("edit-shift", "D1")
	if !ok {
		t.Fatal("edit-shift.D1 binding missing")
	}
	if !b.IsScrollDelta() {
		t.Errorf("edit-shift.D1 should be a scrollDelta binding, got type=%q", b.Type)
	}
	if b.Flags != "Alt" {
		t.Errorf("Flags = %q, want Alt", b.Flags)
	}
	if b.LinesPerDetent != 1 {
		t.Errorf("LinesPerDetent = %d, want 1", b.LinesPerDetent)
	}
}

func TestScrollDeltaBinding_NotOtherTypes(t *testing.T) {
	sd := Binding{Type: "scrollDelta", Flags: "Alt", LinesPerDetent: 1}
	if sd.IsKey() || sd.IsKeyDelta() || sd.IsProperty() || sd.IsJog() || sd.IsJogSeconds() || sd.IsJogMinuteSnap() || sd.IsSelectClip() || sd.IsCDL() || sd.IsWheel() {
		t.Errorf("scrollDelta binding incorrectly classified: %+v", sd)
	}
	kd := Binding{Type: "keyDelta", KeyCW: "Cmd+=", KeyCCW: "Cmd+-"}
	if kd.IsScrollDelta() {
		t.Errorf("a keyDelta binding must not classify as scrollDelta: %+v", kd)
	}
}

func TestScrollDeltaBinding_DetentThreshold(t *testing.T) {
	// scrollDelta shares DetentThreshold with keyDelta/selectClip/etc,
	// not a new field of its own.
	cfg := loadInline(t, `{
		"edit-shift": {
			"D1": {"type": "scrollDelta", "flags": "Alt", "linesPerDetent": 1, "detentThreshold": 2}
		}
	}`)
	b, ok := cfg.Binding("edit-shift", "D1")
	if !ok || b.DetentThreshold != 2 {
		t.Errorf("edit-shift.D1 = %+v ok=%v, want detentThreshold=2", b, ok)
	}
}

// TestScrollDeltaBinding_EmptyFlags_ZeroLinesPerDetent proves both fields
// are legitimately absent-able in config -- absent Flags means "no
// modifier" (0 flags), absent LinesPerDetent means "let engine.go's
// defaultLinesPerDetent apply" -- neither is a config.Load-time error.
func TestScrollDeltaBinding_EmptyFlags_ZeroLinesPerDetent(t *testing.T) {
	cfg := loadInline(t, `{
		"edit-shift": {
			"D1": {"type": "scrollDelta"}
		}
	}`)
	b, ok := cfg.Binding("edit-shift", "D1")
	if !ok || !b.IsScrollDelta() {
		t.Fatalf("edit-shift.D1 = %+v ok=%v, want scrollDelta", b, ok)
	}
	if b.Flags != "" {
		t.Errorf("Flags = %q, want \"\" (absent)", b.Flags)
	}
	if b.LinesPerDetent != 0 {
		t.Errorf("LinesPerDetent = %d, want 0 (absent, engine.go applies the default)", b.LinesPerDetent)
	}
}

// TestRealBindingsConfig_DirectionButtons_SelectClipBindings loads the
// ACTUAL repo config/bindings.json to guard against JSON typos/drift in
// the shipped Up/Down/Left/Right direction-button clip-selection wiring.
//
// Layout changed 2026-08-13 (arrows-as-real-keystrokes + Shift chord,
// shipped only after the overlay-aware direct-intercept change proved
// itself with a full green test suite -- see
// internal/engine/overlay_intercepts_test.go): edit.Up/Down/Left/Right are
// now plain "key" bindings (real Up/Down/Left/Right arrow keystrokes,
// pressed alone); the selectClipVert/selectClipHoriz bindings MOVED to the
// new "edit-shift" bank, reachable only while the Shift deck button
// (modifierHold) is held. Same clip-selection feature as before, just
// gated behind Shift instead of being the Edit page's only behavior for
// these four controls.
//
// C5 (selectClipClear) is deliberately NOT asserted here -- a same-day
// follow-up config change (commit 9dc5c19, "config: drop C5
// (selectClipClear) — selection auto-deselects, C5 redundant") removed
// C5's binding from bindings.json entirely: the direction-button
// selection's own playhead-left-the-clip auto-reset already clears it, so
// a dedicated "clear" button was redundant. This test's C5 assertion
// wasn't updated alongside that removal -- fixed here rather than
// re-adding a binding the user intentionally deleted.
func TestRealBindingsConfig_DirectionButtons_SelectClipBindings(t *testing.T) {
	cfg, err := Load("../../config/device-map.json", "../../config/bindings.json")
	if err != nil {
		t.Fatalf("Load real repo config: %v", err)
	}
	wantArrowKey := map[string]string{"Up": "Up", "Down": "Down", "Left": "Left", "Right": "Right"}
	for control, wantKey := range wantArrowKey {
		b, ok := cfg.Binding("edit", control)
		if !ok {
			t.Fatalf("edit.%s binding missing from config/bindings.json", control)
		}
		if !b.IsKey() || b.Key != wantKey {
			t.Errorf("edit.%s = %+v, want type=key/key=%q (plain arrow keystroke)", control, b, wantKey)
		}
	}
	for _, control := range []string{"Up", "Down"} {
		b, ok := cfg.Binding("edit-shift", control)
		if !ok {
			t.Fatalf("edit-shift.%s binding missing from config/bindings.json", control)
		}
		if !b.IsSelectClipVert() {
			t.Errorf("edit-shift.%s should be type=selectClipVert, got %q", control, b.Type)
		}
	}
	for _, control := range []string{"Left", "Right"} {
		b, ok := cfg.Binding("edit-shift", control)
		if !ok {
			t.Fatalf("edit-shift.%s binding missing from config/bindings.json", control)
		}
		if !b.IsSelectClipHoriz() {
			t.Errorf("edit-shift.%s should be type=selectClipHoriz, got %q", control, b.Type)
		}
	}
	if _, ok := cfg.Binding("edit", "C5"); ok {
		t.Error("edit.C5 binding present in config/bindings.json, want absent (dropped in commit 9dc5c19 -- selection auto-deselects, C5 was redundant)")
	}
	if _, ok := cfg.Binding("edit-shift", "C5"); ok {
		t.Error("edit-shift.C5 binding present in config/bindings.json, want absent (never had one; C5 was dropped entirely, not moved to the Shift chord)")
	}
}

// ---------------------------------------------------------------------------
// Direction-button clip-selection: selectClipVert/selectClipHoriz/
// selectClipClear binding types.
// ---------------------------------------------------------------------------

func TestSelectClipVertBinding(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"Up": {"type": "selectClipVert"},
			"Down": {"type": "selectClipVert"}
		}
	}`)
	for _, control := range []string{"Up", "Down"} {
		b, ok := cfg.Binding("edit", control)
		if !ok {
			t.Fatalf("%s binding missing", control)
		}
		if !b.IsSelectClipVert() {
			t.Errorf("%s should be a selectClipVert binding, got type=%q", control, b.Type)
		}
	}
}

func TestSelectClipHorizBinding(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"Left": {"type": "selectClipHoriz"},
			"Right": {"type": "selectClipHoriz"}
		}
	}`)
	for _, control := range []string{"Left", "Right"} {
		b, ok := cfg.Binding("edit", control)
		if !ok {
			t.Fatalf("%s binding missing", control)
		}
		if !b.IsSelectClipHoriz() {
			t.Errorf("%s should be a selectClipHoriz binding, got type=%q", control, b.Type)
		}
	}
}

func TestSelectClipClearBinding(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"C5": {"type": "selectClipClear"}
		}
	}`)
	b, ok := cfg.Binding("edit", "C5")
	if !ok {
		t.Fatal("C5 binding missing")
	}
	if !b.IsSelectClipClear() {
		t.Errorf("C5 should be a selectClipClear binding, got type=%q", b.Type)
	}
}

func TestDirectionButtonBindings_NotOtherTypes(t *testing.T) {
	vert := Binding{Type: "selectClipVert"}
	horiz := Binding{Type: "selectClipHoriz"}
	clear := Binding{Type: "selectClipClear"}
	if vert.IsSelectClipHoriz() || vert.IsSelectClipClear() || vert.IsSelectClip() || vert.IsProperty() || vert.IsJog() {
		t.Errorf("selectClipVert binding incorrectly classified: %+v", vert)
	}
	if horiz.IsSelectClipVert() || horiz.IsSelectClipClear() || horiz.IsSelectClip() || horiz.IsKey() {
		t.Errorf("selectClipHoriz binding incorrectly classified: %+v", horiz)
	}
	if clear.IsSelectClipVert() || clear.IsSelectClipHoriz() || clear.IsSelectClip() {
		t.Errorf("selectClipClear binding incorrectly classified: %+v", clear)
	}
	prop := Binding{Type: "property"}
	if prop.IsSelectClipVert() || prop.IsSelectClipHoriz() || prop.IsSelectClipClear() {
		t.Errorf("property binding incorrectly classified as a direction-button select type: %+v", prop)
	}
}

// ---------------------------------------------------------------------------
// C1 "add marker at playhead" feature: addMarker binding type.
// ---------------------------------------------------------------------------

func TestAddMarkerBinding(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"C1": {"type": "addMarker", "markerDurationSeconds": 1, "markerColor": "Blue"}
		}
	}`)
	b, ok := cfg.Binding("edit", "C1")
	if !ok {
		t.Fatal("C1 binding missing")
	}
	if !b.IsAddMarker() {
		t.Errorf("C1 should be an addMarker binding, got type=%q", b.Type)
	}
	if b.MarkerDurationSeconds != 1 {
		t.Errorf("MarkerDurationSeconds = %v, want 1", b.MarkerDurationSeconds)
	}
	if b.MarkerColor != "Blue" {
		t.Errorf("MarkerColor = %q, want Blue", b.MarkerColor)
	}
}

func TestAddMarkerBinding_OptionalMarkerName(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"C1": {"type": "addMarker", "markerDurationSeconds": 2.5, "markerColor": "Red", "markerName": "Beat"}
		}
	}`)
	b, ok := cfg.Binding("edit", "C1")
	if !ok {
		t.Fatal("C1 binding missing")
	}
	if b.MarkerName != "Beat" {
		t.Errorf("MarkerName = %q, want Beat", b.MarkerName)
	}
	if b.MarkerDurationSeconds != 2.5 {
		t.Errorf("MarkerDurationSeconds = %v, want 2.5", b.MarkerDurationSeconds)
	}
}

// TestAddMarkerBinding_FieldsOmittedAreZero confirms config.Load applies NO
// defaults to these fields (unlike TopLevel.ReleaseColor/HighlightColor,
// which ARE resolved once at Load time) -- MarkerDurationSeconds/
// MarkerColor follow the same per-binding pattern as Sens/MaxDelta: the raw
// zero value is preserved here, and the "0 -> default 1s/Blue" fallback is
// applied later, at dispatch time, by engine.go's dispatchAddMarker.
func TestAddMarkerBinding_FieldsOmittedAreZero(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"C1": {"type": "addMarker"}
		}
	}`)
	b, ok := cfg.Binding("edit", "C1")
	if !ok {
		t.Fatal("C1 binding missing")
	}
	if b.MarkerDurationSeconds != 0 {
		t.Errorf("MarkerDurationSeconds = %v, want 0 (defaults applied later by engine.go, not config.Load)", b.MarkerDurationSeconds)
	}
	if b.MarkerColor != "" {
		t.Errorf("MarkerColor = %q, want \"\" (defaults applied later by engine.go, not config.Load)", b.MarkerColor)
	}
	if b.MarkerName != "" {
		t.Errorf("MarkerName = %q, want \"\"", b.MarkerName)
	}
}

// ---------------------------------------------------------------------------
// Feature 1: Hue-gated marker P-buttons -- markerAtPlayhead binding type +
// whenPMode gate field.
// ---------------------------------------------------------------------------

func TestMarkerAtPlayheadBinding(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"P1btn": {"type": "markerAtPlayhead", "markerColor": "Red", "whenPMode": "hue"}
		}
	}`)
	b, ok := cfg.Binding("edit", "P1btn")
	if !ok {
		t.Fatal("P1btn binding missing")
	}
	if !b.IsMarkerAtPlayhead() {
		t.Errorf("P1btn should be a markerAtPlayhead binding, got type=%q", b.Type)
	}
	if b.MarkerColor != "Red" {
		t.Errorf("MarkerColor = %q, want Red", b.MarkerColor)
	}
	if b.WhenPMode != "hue" {
		t.Errorf("WhenPMode = %q, want hue", b.WhenPMode)
	}
}

// TestMarkerAtPlayheadBinding_WhenPModeOmittedIsEmpty confirms a binding
// with no whenPMode field reports WhenPMode=="" (ungated -- Task 2's
// dispatch treats an empty WhenPMode as "always fires", not "never
// fires"), same "field omitted -> zero value, no config.Load default"
// pattern as MarkerColor/MarkerDurationSeconds above.
func TestMarkerAtPlayheadBinding_WhenPModeOmittedIsEmpty(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"P1btn": {"type": "markerAtPlayhead", "markerColor": "Red"}
		}
	}`)
	b, ok := cfg.Binding("edit", "P1btn")
	if !ok {
		t.Fatal("P1btn binding missing")
	}
	if b.WhenPMode != "" {
		t.Errorf("WhenPMode = %q, want \"\" (omitted)", b.WhenPMode)
	}
}

func TestMarkerAtPlayheadBinding_NotOtherTypes(t *testing.T) {
	mp := Binding{Type: "markerAtPlayhead", MarkerColor: "Red", WhenPMode: "hue"}
	if mp.IsAddMarker() || mp.IsMarkerColor() || mp.IsProperty() || mp.IsJog() || mp.IsCDL() ||
		mp.IsWheel() || mp.IsSelectClip() || mp.IsKey() || mp.IsBalance() {
		t.Errorf("markerAtPlayhead binding incorrectly classified: %+v", mp)
	}
	am := Binding{Type: "addMarker"}
	mc := Binding{Type: "markerColor"}
	prop := Binding{Type: "property"}
	if am.IsMarkerAtPlayhead() || mc.IsMarkerAtPlayhead() || prop.IsMarkerAtPlayhead() {
		t.Errorf("other binding types incorrectly classified as markerAtPlayhead: addMarker=%v markerColor=%v property=%v",
			am.IsMarkerAtPlayhead(), mc.IsMarkerAtPlayhead(), prop.IsMarkerAtPlayhead())
	}
}

// ---------------------------------------------------------------------------
// Part B (2026-08-12 REV 2 design review): held-modifier CHORD OVERLAY --
// modifierHold binding type + Modifier field. Replaces the reverted
// "modifier" type -- see engine.go's git history for why that design was
// unacceptable. This one performs NO OS-level side effect at press/release
// time at all: Modifier is purely an overlay-bank-name suffix (e.g. "cmd"),
// consulted by resolveOverlayBinding (internal/engine), never passed to
// keys.SendDown/SendUp.
// ---------------------------------------------------------------------------

func TestModifierHoldBinding(t *testing.T) {
	cfg := loadInline(t, `{
		"col": {
			"Cmd": {"type": "modifierHold", "modifier": "cmd"}
		}
	}`)
	b, ok := cfg.Binding("col", "Cmd")
	if !ok {
		t.Fatal("Cmd binding missing")
	}
	if !b.IsModifierHold() {
		t.Errorf("Cmd should be a modifierHold binding, got type=%q", b.Type)
	}
	if b.Modifier != "cmd" {
		t.Errorf("Modifier = %q, want cmd", b.Modifier)
	}
}

func TestModifierHoldBinding_NotOtherTypes(t *testing.T) {
	m := Binding{Type: "modifierHold", Modifier: "cmd"}
	if m.IsFineModeHold() || m.IsAddMarker() || m.IsMarkerColor() || m.IsMarkerAtPlayhead() ||
		m.IsKey() || m.IsProperty() || m.IsJog() || m.IsCDL() || m.IsWheel() || m.IsLayer() {
		t.Errorf("modifierHold binding incorrectly classified: %+v", m)
	}
	fmh := Binding{Type: "fineModeHold"}
	key := Binding{Type: "key", Key: "Alt+S"}
	if fmh.IsModifierHold() || key.IsModifierHold() {
		t.Errorf("other binding types incorrectly classified as modifierHold: fineModeHold=%v key=%v",
			fmh.IsModifierHold(), key.IsModifierHold())
	}
}

// ---------------------------------------------------------------------------
// D1 "selectClip" sensitivity: DetentThreshold field.
// ---------------------------------------------------------------------------

func TestSelectClipBinding_DetentThreshold(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"D1": {"type": "selectClip", "detentThreshold": 6}
		}
	}`)
	b, ok := cfg.Binding("edit", "D1")
	if !ok {
		t.Fatal("D1 binding missing")
	}
	if !b.IsSelectClip() {
		t.Errorf("D1 should be a selectClip binding, got type=%q", b.Type)
	}
	if b.DetentThreshold != 6 {
		t.Errorf("DetentThreshold = %v, want 6", b.DetentThreshold)
	}
}

// TestSelectClipBinding_DetentThresholdOmittedIsZero confirms config.Load
// applies NO default (same pattern as MarkerDurationSeconds/MarkerColor
// above) -- the "0 -> defaultSelectClipDetentThreshold" fallback is applied
// later, at dispatch time, by engine.go's Start().
func TestSelectClipBinding_DetentThresholdOmittedIsZero(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"D1": {"type": "selectClip"}
		}
	}`)
	b, ok := cfg.Binding("edit", "D1")
	if !ok {
		t.Fatal("D1 binding missing")
	}
	if b.DetentThreshold != 0 {
		t.Errorf("DetentThreshold = %v, want 0 (default applied later by engine.go, not config.Load)", b.DetentThreshold)
	}
}

// ---------------------------------------------------------------------------
// D1 "jogSeconds" — coarse playhead-jump binding type.
// ---------------------------------------------------------------------------

func TestJogSecondsBinding(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"D1": {"type": "jogSeconds", "secondsPerStep": 60, "detentThreshold": 4}
		}
	}`)
	b, ok := cfg.Binding("edit", "D1")
	if !ok {
		t.Fatal("D1 binding missing")
	}
	if !b.IsJogSeconds() {
		t.Errorf("D1 should be a jogSeconds binding, got type=%q", b.Type)
	}
	if b.SecondsPerStep != 60 {
		t.Errorf("D1.SecondsPerStep = %v, want 60", b.SecondsPerStep)
	}
	if b.DetentThreshold != 4 {
		t.Errorf("D1.DetentThreshold = %v, want 4", b.DetentThreshold)
	}
}

// TestJogSecondsBinding_FieldsOmittedAreZero confirms config.Load applies
// NO default for SecondsPerStep/DetentThreshold (same pattern as
// MarkerDurationSeconds/MarkerColor/the selectClip DetentThreshold tests
// above) — the "0 -> defaultJogSecondsSeconds/defaultSelectClipDetentThreshold"
// fallback is applied later, at dispatch time, by engine.go's Start().
func TestJogSecondsBinding_FieldsOmittedAreZero(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"D1": {"type": "jogSeconds"}
		}
	}`)
	b, ok := cfg.Binding("edit", "D1")
	if !ok {
		t.Fatal("D1 binding missing")
	}
	if b.SecondsPerStep != 0 {
		t.Errorf("SecondsPerStep = %v, want 0 (default applied later by engine.go, not config.Load)", b.SecondsPerStep)
	}
	if b.DetentThreshold != 0 {
		t.Errorf("DetentThreshold = %v, want 0 (default applied later by engine.go, not config.Load)", b.DetentThreshold)
	}
}

func TestJogSecondsBinding_CustomSecondsPerStep(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"D1": {"type": "jogSeconds", "secondsPerStep": 15}
		}
	}`)
	b, ok := cfg.Binding("edit", "D1")
	if !ok {
		t.Fatal("D1 binding missing")
	}
	if b.SecondsPerStep != 15 {
		t.Errorf("D1.SecondsPerStep = %v, want 15", b.SecondsPerStep)
	}
}

func TestJogSecondsBinding_NotOtherTypes(t *testing.T) {
	js := Binding{Type: "jogSeconds"}
	if js.IsProperty() || js.IsJog() || js.IsQuickExport() || js.IsCDL() || js.IsWheel() || js.IsSelectClip() || js.IsKey() {
		t.Errorf("jogSeconds binding incorrectly classified: %+v", js)
	}
	prop := Binding{Type: "property"}
	jog := Binding{Type: "jog"}
	sc := Binding{Type: "selectClip"}
	qe := Binding{Type: "quickexport"}
	if prop.IsJogSeconds() || jog.IsJogSeconds() || sc.IsJogSeconds() || qe.IsJogSeconds() {
		t.Errorf("other binding types incorrectly classified as jogSeconds: prop=%v jog=%v selectClip=%v qe=%v",
			prop.IsJogSeconds(), jog.IsJogSeconds(), sc.IsJogSeconds(), qe.IsJogSeconds())
	}
}

// ---------------------------------------------------------------------------
// D1 "jogMinuteSnap" — whole-minute playhead-snap binding type (current
// D1 binding, supersedes "jogSeconds" above).
// ---------------------------------------------------------------------------

func TestJogMinuteSnapBinding(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"D1": {"type": "jogMinuteSnap", "detentThreshold": 4}
		}
	}`)
	b, ok := cfg.Binding("edit", "D1")
	if !ok {
		t.Fatal("D1 binding missing")
	}
	if !b.IsJogMinuteSnap() {
		t.Errorf("D1 should be a jogMinuteSnap binding, got type=%q", b.Type)
	}
	if b.DetentThreshold != 4 {
		t.Errorf("D1.DetentThreshold = %v, want 4", b.DetentThreshold)
	}
}

// TestJogMinuteSnapBinding_DetentThresholdOmittedIsZero confirms
// config.Load applies NO default for DetentThreshold (same pattern as the
// selectClip/jogSeconds DetentThreshold tests above) -- the
// "0 -> defaultSelectClipDetentThreshold" fallback is applied later, at
// dispatch time, by engine.go's Start().
func TestJogMinuteSnapBinding_DetentThresholdOmittedIsZero(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"D1": {"type": "jogMinuteSnap"}
		}
	}`)
	b, ok := cfg.Binding("edit", "D1")
	if !ok {
		t.Fatal("D1 binding missing")
	}
	if b.DetentThreshold != 0 {
		t.Errorf("DetentThreshold = %v, want 0 (default applied later by engine.go, not config.Load)", b.DetentThreshold)
	}
}

func TestJogMinuteSnapBinding_NotOtherTypes(t *testing.T) {
	jms := Binding{Type: "jogMinuteSnap"}
	if jms.IsProperty() || jms.IsJog() || jms.IsQuickExport() || jms.IsCDL() || jms.IsWheel() || jms.IsSelectClip() || jms.IsJogSeconds() || jms.IsKey() {
		t.Errorf("jogMinuteSnap binding incorrectly classified: %+v", jms)
	}
	prop := Binding{Type: "property"}
	jog := Binding{Type: "jog"}
	sc := Binding{Type: "selectClip"}
	js := Binding{Type: "jogSeconds"}
	qe := Binding{Type: "quickexport"}
	if prop.IsJogMinuteSnap() || jog.IsJogMinuteSnap() || sc.IsJogMinuteSnap() || js.IsJogMinuteSnap() || qe.IsJogMinuteSnap() {
		t.Errorf("other binding types incorrectly classified as jogMinuteSnap: prop=%v jog=%v selectClip=%v jogSeconds=%v qe=%v",
			prop.IsJogMinuteSnap(), jog.IsJogMinuteSnap(), sc.IsJogMinuteSnap(), js.IsJogMinuteSnap(), qe.IsJogMinuteSnap())
	}
}

func TestAddMarkerBinding_NotOtherTypes(t *testing.T) {
	am := Binding{Type: "addMarker"}
	if am.IsProperty() || am.IsJog() || am.IsQuickExport() || am.IsCDL() || am.IsWheel() || am.IsSelectClip() || am.IsKey() {
		t.Errorf("addMarker binding incorrectly classified: %+v", am)
	}
	prop := Binding{Type: "property"}
	jog := Binding{Type: "jog"}
	qe := Binding{Type: "quickexport"}
	sc := Binding{Type: "selectClip"}
	key := Binding{Type: "key"}
	if prop.IsAddMarker() || jog.IsAddMarker() || qe.IsAddMarker() || sc.IsAddMarker() || key.IsAddMarker() {
		t.Errorf("other binding types incorrectly classified as addMarker: prop=%v jog=%v qe=%v sc=%v key=%v",
			prop.IsAddMarker(), jog.IsAddMarker(), qe.IsAddMarker(), sc.IsAddMarker(), key.IsAddMarker())
	}
}

// TestRealBindingsConfig_C1_PlayPauseBinding loads the ACTUAL repo
// config/bindings.json to guard against JSON typos/drift in the shipped C1
// wiring. C1 was remapped 2026-08-12 (both banks) from its old wiring
// (edit.C1 addMarker; col.C1 key=Alt+S) to a play/pause toggle
// ("type":"key","key":"Space") on BOTH col and edit -- addMarker is slated
// to move onto P1btn-P8btn in a later build, not yet done here. Replaces
// the old TestRealBindingsConfig_C1_AddMarkerBinding, which asserted the
// now-obsolete addMarker/Alt+S wiring.
func TestRealBindingsConfig_C1_PlayPauseBinding(t *testing.T) {
	cfg, err := Load("../../config/device-map.json", "../../config/bindings.json")
	if err != nil {
		t.Fatalf("Load real repo config: %v", err)
	}
	eb, ok := cfg.Binding("edit", "C1")
	if !ok {
		t.Fatal("edit.C1 binding missing from config/bindings.json")
	}
	if !eb.IsKey() || eb.Key != "Space" {
		t.Fatalf("edit.C1 should be type=key key=Space (play/pause), got type=%q key=%q", eb.Type, eb.Key)
	}

	cb, ok := cfg.Binding("col", "C1")
	if !ok {
		t.Fatal("col.C1 binding missing from config/bindings.json")
	}
	if !cb.IsKey() || cb.Key != "Space" {
		t.Fatalf("col.C1 should be type=key key=Space (play/pause), got type=%q key=%q", cb.Type, cb.Key)
	}
}

// ---------------------------------------------------------------------------
// C1 "marker mode" feature (resize + recolor): markerColor binding type
// (P1-8btn) and the tunable _config.markerResizeFramesPerStep.
// ---------------------------------------------------------------------------

func TestMarkerColorBinding(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"P1btn": {"type": "markerColor", "markerColor": "Blue"}
		}
	}`)
	b, ok := cfg.Binding("edit", "P1btn")
	if !ok {
		t.Fatal("P1btn binding missing")
	}
	if !b.IsMarkerColor() {
		t.Errorf("P1btn should be a markerColor binding, got type=%q", b.Type)
	}
	if b.MarkerColor != "Blue" {
		t.Errorf("MarkerColor = %q, want Blue", b.MarkerColor)
	}
}

func TestMarkerColorBinding_NotOtherTypes(t *testing.T) {
	mc := Binding{Type: "markerColor", MarkerColor: "Blue"}
	if mc.IsProperty() || mc.IsJog() || mc.IsAddMarker() || mc.IsCDL() || mc.IsWheel() ||
		mc.IsSelectClip() || mc.IsKey() || mc.IsBalance() {
		t.Errorf("markerColor binding incorrectly classified: %+v", mc)
	}
	am := Binding{Type: "addMarker"}
	prop := Binding{Type: "property"}
	jog := Binding{Type: "jog"}
	if am.IsMarkerColor() || prop.IsMarkerColor() || jog.IsMarkerColor() {
		t.Errorf("other binding types incorrectly classified as markerColor: addMarker=%v property=%v jog=%v",
			am.IsMarkerColor(), prop.IsMarkerColor(), jog.IsMarkerColor())
	}
}

// TestMarkerColorBinding_FieldsOmittedAreZero mirrors
// TestAddMarkerBinding_FieldsOmittedAreZero -- MarkerColor gets NO default
// applied by config.Load for a markerColor binding either; engine.go's
// dispatchSetMarkerColor is expected to just pass through whatever
// bindings.json configured (P1-8btn always specify an explicit color in
// practice, unlike addMarker's optional-with-fallback fields).
func TestMarkerColorBinding_FieldsOmittedAreZero(t *testing.T) {
	cfg := loadInline(t, `{
		"edit": {
			"P1btn": {"type": "markerColor"}
		}
	}`)
	b, ok := cfg.Binding("edit", "P1btn")
	if !ok {
		t.Fatal("P1btn binding missing")
	}
	if b.MarkerColor != "" {
		t.Errorf("MarkerColor = %q, want \"\" (no config.Load default for markerColor)", b.MarkerColor)
	}
}

// ---------------------------------------------------------------------------
// HUD toggle (C6): toggleHud binding type.
// ---------------------------------------------------------------------------

func TestToggleHudBinding(t *testing.T) {
	cfg := loadInline(t, `{
		"col": {
			"C6": {"type": "toggleHud"}
		}
	}`)
	b, ok := cfg.Binding("col", "C6")
	if !ok {
		t.Fatal("C6 binding missing")
	}
	if !b.IsToggleHud() {
		t.Errorf("C6 should be a toggleHud binding, got type=%q", b.Type)
	}
}

func TestToggleHudBinding_NotOtherTypes(t *testing.T) {
	th := Binding{Type: "toggleHud"}
	if th.IsProperty() || th.IsJog() || th.IsQuickExport() || th.IsCDL() || th.IsWheel() ||
		th.IsSelectClip() || th.IsKey() || th.IsBalance() || th.IsAddMarker() || th.IsMarkerColor() {
		t.Errorf("toggleHud binding incorrectly classified: %+v", th)
	}
	qe := Binding{Type: "quickexport"}
	mc := Binding{Type: "markerColor"}
	if qe.IsToggleHud() || mc.IsToggleHud() {
		t.Errorf("other binding types incorrectly classified as toggleHud: quickexport=%v markerColor=%v",
			qe.IsToggleHud(), mc.IsToggleHud())
	}
}

// TestRealBindingsConfig_C6_ToggleHudBinding loads the ACTUAL repo
// config/bindings.json and confirms the HUD toggle is Fn + C6 (col-fn.C6;
// Tunahan 2026-11-05: "fn + c6 must toggle hud") and that plain C6 stays free,
// as it is today. Fn + C6 flips the persisted hudEnabled setting
// (app.ToggleHUD); the Global preset carries the same assignment for every app.
func TestRealBindingsConfig_C6_ToggleHudBinding(t *testing.T) {
	cfg, err := Load("../../config/device-map.json", "../../config/bindings.json")
	if err != nil {
		t.Fatalf("Load real repo config: %v", err)
	}
	b, ok := cfg.Binding("col-fn", "C6")
	if !ok {
		t.Fatal("col-fn.C6 binding missing from config/bindings.json")
	}
	if !b.IsToggleHud() {
		t.Errorf("col-fn.C6 (Fn + C6) should be type=toggleHud, got %q", b.Type)
	}
	if plain, ok := cfg.Binding("col", "C6"); ok && plain.IsToggleHud() {
		t.Errorf("plain C6 must not toggle the HUD any more, got %q", plain.Type)
	}
	// no other Fn chord shares C6
	for _, c := range []string{"C1", "C3", "C4"} {
		if fb, ok := cfg.Binding("col-fn", c); ok && fb.IsToggleHud() {
			t.Errorf("col-fn.%s must not toggle the HUD", c)
		}
	}
}

// TestRealBindingsConfig_PBtn_MarkerAtPlayheadBindings loads the ACTUAL
// repo config/bindings.json and confirms all 8 P-buttons are wired as
// Feature 1 (2026-08-12) markerAtPlayhead bindings with the documented
// color map and the "hue" P-mode gate, in the edit bank only (P1-8btn
// have no col-bank binding at all -- see engine.go's
// isMarkerAtPlayheadControl for why that's required, not incidental).
// Replaces TestRealBindingsConfig_PBtn_MarkerColorBindings, which asserted
// the now-superseded markerColor wiring -- the markerColor TYPE itself
// stays in the codebase (dispatchSetMarkerColor still references it for
// the C1 marker-mode feature), only these bindings moved.
//
// Colour map updated (2026-08-12, orchestrator/user request) to
// Red/Sand/Yellow/Green/Cyan/Blue/Purple/Pink, P1-P8 in order -- pinning
// what config/bindings.json actually ships, not a specific palette choice
// of this test's own.
func TestRealBindingsConfig_PBtn_MarkerAtPlayheadBindings(t *testing.T) {
	cfg, err := Load("../../config/device-map.json", "../../config/bindings.json")
	if err != nil {
		t.Fatalf("Load real repo config: %v", err)
	}
	want := map[string]string{
		"P1btn": "Red",
		"P2btn": "Sand",
		"P3btn": "Yellow",
		"P4btn": "Green",
		"P5btn": "Cyan",
		"P6btn": "Blue",
		"P7btn": "Purple",
		"P8btn": "Pink",
	}
	for control, color := range want {
		eb, ok := cfg.Binding("edit", control)
		if !ok {
			t.Fatalf("edit.%s binding missing from config/bindings.json", control)
		}
		if !eb.IsMarkerAtPlayhead() {
			t.Errorf("edit.%s should be type=markerAtPlayhead, got %q", control, eb.Type)
		}
		if eb.MarkerColor != color {
			t.Errorf("edit.%s MarkerColor = %q, want %q", control, eb.MarkerColor, color)
		}
		if eb.WhenPMode != "hue" {
			t.Errorf("edit.%s WhenPMode = %q, want hue", control, eb.WhenPMode)
		}
		if _, ok := cfg.Binding("col", control); ok {
			t.Errorf("col.%s should NOT exist (P-buttons are edit-bank-only for marker mode)", control)
		}
	}
}

func TestTopLevelMarkerResizeFramesPerStepDefault(t *testing.T) {
	cfg := loadInline(t, `{"col":{}}`)
	if cfg.TopLevel.MarkerResizeFramesPerStep != defaultMarkerResizeFramesPerStep {
		t.Errorf("default MarkerResizeFramesPerStep = %d, want %d",
			cfg.TopLevel.MarkerResizeFramesPerStep, defaultMarkerResizeFramesPerStep)
	}
}

func TestTopLevelMarkerResizeFramesPerStepOverride(t *testing.T) {
	cfg := loadInline(t, `{
		"_config": {"markerResizeFramesPerStep": 3},
		"col": {}
	}`)
	if cfg.TopLevel.MarkerResizeFramesPerStep != 3 {
		t.Errorf("MarkerResizeFramesPerStep = %d, want 3", cfg.TopLevel.MarkerResizeFramesPerStep)
	}
}

// TestRealBindingsConfig_MarkerResizeFramesPerStep guards against JSON
// drift in the shipped _config.markerResizeFramesPerStep value.
func TestRealBindingsConfig_MarkerResizeFramesPerStep(t *testing.T) {
	cfg, err := Load("../../config/device-map.json", "../../config/bindings.json")
	if err != nil {
		t.Fatalf("Load real repo config: %v", err)
	}
	if cfg.TopLevel.MarkerResizeFramesPerStep != 1 {
		t.Errorf("MarkerResizeFramesPerStep = %d, want 1", cfg.TopLevel.MarkerResizeFramesPerStep)
	}
}

// TestRealBindingsConfig_ModifierHoldBindings pins the shipped
// bindings.json's FOUR modifierHold deck buttons (Cmd/Alt/Shift/Ctrl) --
// guards against JSON drift in the type/modifier wiring the chord overlay
// depends on.
func TestRealBindingsConfig_ModifierHoldBindings(t *testing.T) {
	cfg, err := Load("../../config/device-map.json", "../../config/bindings.json")
	if err != nil {
		t.Fatalf("Load real repo config: %v", err)
	}
	want := map[string]string{
		"Alt":   "alt",
		"Shift": "shift",
		"Ctrl":  "ctrl",
		"Cmd":   "cmd",
	}
	for control, wantModifier := range want {
		b, ok := cfg.Binding("col", control)
		if !ok {
			t.Errorf("col.%s binding missing from config/bindings.json", control)
			continue
		}
		if !b.IsModifierHold() {
			t.Errorf("col.%s = %+v, want type=modifierHold", control, b)
			continue
		}
		if b.Modifier != wantModifier {
			t.Errorf("col.%s.modifier = %q, want %q", control, b.Modifier, wantModifier)
		}
	}
}

func TestHUDWithoutResolve_DefaultsOnAndHonoursFalse(t *testing.T) {
	cfg := loadInline(t, `{"col": {}}`)
	if v := cfg.TopLevel.HUDWithoutResolve; v == nil || !*v {
		t.Errorf("default = %v, want true", v)
	}
	cfg = loadInline(t, `{"_config": {"hudWithoutResolve": false}, "col": {}}`)
	if v := cfg.TopLevel.HUDWithoutResolve; v == nil || *v {
		t.Errorf("explicit false = %v", v)
	}
}
