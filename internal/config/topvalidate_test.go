package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func loadWith(t *testing.T, cfg string) *Config {
	t.Helper()
	dir := t.TempDir()
	dm := filepath.Join(dir, "device-map.json")
	os.WriteFile(dm, []byte(`{"knobs":{},"sliders":{},"buttons":{}}`), 0o644)
	bf := filepath.Join(dir, "bindings.json")
	os.WriteFile(bf, []byte(`{"_config":`+cfg+`,"col":{}}`), 0o644)
	c, err := Load(dm, bf)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestLoadClampsRangedSettings(t *testing.T) {
	c := loadWith(t, `{"maxScrubPx":-5,"pxPerUnit":1e300,"keyDebounceMs":-3,"coalesceWindowMs":-1,"fineFactor":50,"selectionGraceMs":99999999,"markerResizeFramesPerStep":5000}`)
	tl := c.TopLevel
	if tl.MaxScrubPx != defaultMaxScrubPx || tl.PxPerUnit != 1000 || tl.FineFactor != 1 || tl.SelectionGraceMs != 5000 || tl.MarkerResizeFramesPerStep != 100 {
		t.Errorf("not clamped: %+v", tl)
	}
	if tl.KeyDebounceMs != defaultKeyDebounceMs || tl.CoalesceWindowMs != defaultCoalesceWindowMs {
		t.Errorf("a negative window must become the DEFAULT, never 0=off: debounce=%d coalesce=%d", tl.KeyDebounceMs, tl.CoalesceWindowMs)
	}
	d := loadWith(t, `{}`)
	if d.TopLevel.MaxScrubPx != defaultMaxScrubPx || d.TopLevel.KeyDebounceMs != defaultKeyDebounceMs {
		t.Error("defaults changed")
	}
}

func TestValidateTopLevelRanges(t *testing.T) {
	ok := []TopLevel{{}, {MaxScrubPx: 80, KeyDebounceMs: 50, FineFactor: 0.5}, {FinderTagConfirmThreshold: -1}, {PxPerUnit: 0.1}, {MaxScrubPx: 10, MinScrubPx: 10}}
	for _, tl := range ok {
		if err := ValidateTopLevel(tl); err != nil {
			t.Errorf("%+v: %v", tl, err)
		}
	}
	bad := map[string]TopLevel{
		"maxScrubPx":                {MaxScrubPx: -1},
		"pxPerUnit":                 {PxPerUnit: 1e300},
		"keyDebounceMs":             {KeyDebounceMs: -1},
		"coalesceWindowMs":          {CoalesceWindowMs: 5000},
		"fineFactor":                {FineFactor: 50},
		"selectionGraceMs":          {SelectionGraceMs: 9999999},
		"markerResizeFramesPerStep": {MarkerResizeFramesPerStep: -2},
		"finderTagConfirmThreshold": {FinderTagConfirmThreshold: -2},
		"minScrubPx":                {MaxScrubPx: 10, MinScrubPx: 50},
	}
	for field, tl := range bad {
		err := ValidateTopLevel(tl)
		re, ok := err.(*RangeError)
		if !ok || re.Field != field {
			t.Errorf("%s: err = %v", field, err)
		}
	}
	if (&RangeError{"x", "y"}).Error() != "x: y" {
		t.Error("Error text")
	}
}

func TestNonCanonicalTopLevelKeysAndDecode(t *testing.T) {
	nc := NonCanonicalTopLevelKeys(map[string]any{"DodeeMostGuard": false, "DODEEMOSTGUARD": false, "dodeeMoſtGuard": false, "FinderTagConfirmThreshold": -1, "fineFactor": 0.5, "actionDefaults": map[string]any{}, "somethingElse": 1})
	if len(nc) != 4 {
		t.Errorf("flagged %d: %v", len(nc), nc)
	}
	// the engine reads every one of those spellings
	for _, k := range []string{"DodeeMostGuard", "DODEEMOSTGUARD", "dodeeMoſtGuard"} {
		tl, err := DecodeTopLevel(map[string]any{k: false})
		if err != nil || tl.DodeeMostGuard == nil || *tl.DodeeMostGuard {
			t.Errorf("%q: the engine decoder did not read it as false (%v)", k, err)
		}
	}
}

func TestTightenedRangesRejectInRangeExtremesAndAcceptTheLiveValues(t *testing.T) {
	// the extremes the critic used: in the old ranges, weakening guards all the same
	for name, tl := range map[string]TopLevel{
		"debounce 1":         {KeyDebounceMs: 1},
		"debounce 1000":      {KeyDebounceMs: 1000},
		"maxScrub 2000":      {MaxScrubPx: 2000},
		"pxPerUnit 1000":     {PxPerUnit: 1000},
		"coalesce 2000":      {CoalesceWindowMs: 2000},
		"multiplier 100":     {WheelSensMultipliers: map[string]float64{"lift": 100}},
		"multiplier 0.001":   {WheelSensMultipliers: map[string]float64{"lift": 0.001}},
		"minScrub above max": {MaxScrubPx: 10, MinScrubPx: 400},
		"threshold 5000":     {FinderTagConfirmThreshold: 5000},
	} {
		if ValidateTopLevel(tl) == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// the user's real values (ld-resolve live _config, read only) sit inside the ranges
	home, _ := os.UserHomeDir()
	raw, err := os.ReadFile(filepath.Join(home, "Library/Application Support/ld-resolve/bindings.json"))
	if err != nil {
		t.Skip("no live ld-resolve config on this machine")
	}
	var doc struct {
		Config map[string]any `json:"_config"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Skip("unreadable live config")
	}
	tl, err := DecodeTopLevel(doc.Config)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateTopLevel(tl); err != nil {
		t.Errorf("the live config is refused by the new ranges: %v", err)
	}
	for _, m := range []float64{1, 1, 0.7, 0.5} {
		if m < MultiplierRange.Min || m > MultiplierRange.Max {
			t.Errorf("default multiplier %v outside its range", m)
		}
	}
}

// Regression (wave P): Load clamps to the WIDE bounds only. A config with
// keyDebounceMs:1 (tests, "restores per-step behaviour") must load as 1, and a
// tight-range-violating but sane value must not be raised.
func TestLoadClampUsesWideBoundsNotTheTightEditorRanges(t *testing.T) {
	tl := TopLevel{KeyDebounceMs: 1, MaxScrubPx: 5, PxPerUnit: 0.05, CoalesceWindowMs: 5, WheelSensMultipliers: map[string]float64{"a": 0.01, "b": 8}}
	clampTopLevel(&tl)
	if tl.KeyDebounceMs != 1 || tl.MaxScrubPx != 5 || tl.PxPerUnit != 0.05 || tl.CoalesceWindowMs != 5 {
		t.Fatalf("Load clamp raised sane low values: %+v", tl)
	}
	if tl.WheelSensMultipliers["a"] != 0.01 || tl.WheelSensMultipliers["b"] != 8 {
		t.Fatalf("multipliers changed: %v", tl.WheelSensMultipliers)
	}
	if err := ValidateTopLevel(TopLevel{KeyDebounceMs: 1}); err == nil {
		t.Fatal("the editor-side validation must still refuse keyDebounceMs 1")
	}
	bad := TopLevel{KeyDebounceMs: -5, MaxScrubPx: -1, FineFactor: 7}
	clampTopLevel(&bad)
	if bad.KeyDebounceMs != 0 || bad.MaxScrubPx != 0 || bad.FineFactor != 1 {
		t.Fatalf("absurd values must still be clamped: %+v", bad)
	}
}
