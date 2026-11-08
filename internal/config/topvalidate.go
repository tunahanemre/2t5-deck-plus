package config

import (
	"encoding/json"
	"fmt"
	"math"
	"reflect"
)

// Ranges for the numeric TopLevel settings (the preset `_config`). A value of 0
// means "use the built-in default" for most of them and is always allowed; any
// other value must lie inside [Min, Max]. The editor and imports REFUSE a value
// outside its range (naming the field); Load clamps it, so the engine never
// runs with an absurd one even if a file reaches it some other way.
//
// Why the limits matter: maxScrubPx <= 0 switches the drag clamp off
// (dragmath.Clamp returns the raw delta), a negative keyDebounceMs removes the
// bounce protection on destructive keys, a huge one swallows presses,
// fineFactor above 1 makes "fine" coarser, and so on.
type Range struct {
	Min, Max float64
	Doc      string
}

// TopLevelRanges, keyed by the JSON field name.
var TopLevelRanges = map[string]Range{
	"coalesceWindowMs":          {20, 500, "ms to gather balance ticks"},
	"fineFactor":                {0.05, 1, "sens multiplier while fine mode is held (never coarser than normal)"},
	"pxPerUnit":                 {0.1, 100, "screen pixels per accumulated step"},
	"maxScrubPx":                {10, 400, "per-tick scrub clamp; small enough that one tick cannot fling a control across the screen"},
	"minScrubPx":                {0, 400, "smallest delta that triggers a scrub (never above maxScrubPx)"},
	"markerResizeFramesPerStep": {1, 100, "frames per marker-resize step"},
	"keyDebounceMs":             {20, 500, "duplicate key press suppression window; too low gives no bounce protection on destructive keys, too high swallows presses"},
	"selectionGraceMs":          {0, 5000, "clip selection auto-reset grace"},
	"finderTagConfirmThreshold": {-1, 1000, "selection size above which a Finder tag press asks again; -1 disables the question, above 1000 it almost never asks"},
	"hud.heightPx":              {0, 5000, "HUD height in pixels"},
}

// Default values of the ranged settings (what the engine uses for 0).
var topLevelDefaults = map[string]float64{
	"coalesceWindowMs": defaultCoalesceWindowMs, "fineFactor": defaultFineFactor, "pxPerUnit": defaultPxPerUnit,
	"maxScrubPx": defaultMaxScrubPx, "minScrubPx": defaultMinScrubPx, "markerResizeFramesPerStep": defaultMarkerResizeFramesPerStep,
	"keyDebounceMs": defaultKeyDebounceMs, "selectionGraceMs": defaultSelectionGraceMs,
}

// MultiplierRange bounds each entry of wheelSensMultipliers.
var MultiplierRange = Range{0.05, 5, "per-wheel sensitivity multiplier"}

// LoadRanges are the WIDE bounds Load clamps to (the wave-O limits): they only stop
// absurd values (a clamp switched off, a huge debounce). Tests and old configs use
// small values on purpose (keyDebounceMs 1 restores per-step behaviour), so Load must
// not raise them to the tight TopLevelRanges, which apply only to editor writes,
// imports, restores, outside-edit checks and the guard flags.
var LoadRanges = map[string]Range{
	"coalesceWindowMs":          {0, 2000, ""},
	"fineFactor":                {0.05, 1, ""},
	"pxPerUnit":                 {0.01, 1000, ""},
	"maxScrubPx":                {1, 2000, ""},
	"minScrubPx":                {0, 2000, ""},
	"markerResizeFramesPerStep": {1, 100, ""},
	"keyDebounceMs":             {0, 1000, ""},
	"selectionGraceMs":          {0, 5000, ""},
	"finderTagConfirmThreshold": {-1, 100000, ""},
	"hud.heightPx":              {0, 5000, ""},
}

// RangeError names the TopLevel field that is out of range.
type RangeError struct {
	Field string
	Msg   string
}

func (e *RangeError) Error() string { return e.Field + ": " + e.Msg }

func inRange(name string, v float64) error {
	r, ok := TopLevelRanges[name]
	if !ok {
		return nil
	}
	if v == 0 && name != "finderTagConfirmThreshold" {
		return nil // 0 = the built-in default
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || v < r.Min || v > r.Max {
		return &RangeError{name, fmt.Sprintf("%v is outside the allowed range %v to %v (%s)", v, r.Min, r.Max, r.Doc)}
	}
	return nil
}

// ValidateTopLevel checks every ranged numeric field of a decoded `_config`
// (before defaults are applied: 0 = default is fine).
func ValidateTopLevel(tl TopLevel) error {
	for _, c := range []struct {
		name string
		v    float64
	}{
		{"coalesceWindowMs", float64(tl.CoalesceWindowMs)}, {"fineFactor", tl.FineFactor}, {"pxPerUnit", tl.PxPerUnit},
		{"maxScrubPx", tl.MaxScrubPx}, {"minScrubPx", tl.MinScrubPx}, {"markerResizeFramesPerStep", float64(tl.MarkerResizeFramesPerStep)},
		{"keyDebounceMs", float64(tl.KeyDebounceMs)}, {"selectionGraceMs", float64(tl.SelectionGraceMs)},
		{"finderTagConfirmThreshold", float64(tl.FinderTagConfirmThreshold)}, {"hud.heightPx", float64(tl.HUD.HeightPx)},
	} {
		if err := inRange(c.name, c.v); err != nil {
			return err
		}
	}
	for i, h := range tl.HueOffsetsDeg {
		if math.IsNaN(h) || h < -360 || h > 360 {
			return &RangeError{"hueOffsetsDeg", fmt.Sprintf("entry %d (%v) is outside -360 to 360", i, h)}
		}
	}
	for w, m := range tl.WheelSensMultipliers {
		if math.IsNaN(m) || m < MultiplierRange.Min || m > MultiplierRange.Max {
			return &RangeError{"wheelSensMultipliers", fmt.Sprintf("%q: %v is outside %v to %v", w, m, MultiplierRange.Min, MultiplierRange.Max)}
		}
	}
	// a minimum above the maximum would make every scrub either skipped or clamped to nonsense
	maxPx, minPx := tl.MaxScrubPx, tl.MinScrubPx
	if maxPx == 0 {
		maxPx = defaultMaxScrubPx
	}
	if minPx == 0 {
		minPx = defaultMinScrubPx
	}
	if minPx > maxPx {
		return &RangeError{"minScrubPx", fmt.Sprintf("%v is above maxScrubPx (%v)", minPx, maxPx)}
	}
	return nil
}

func clampTo(name string, v float64) float64 {
	r := LoadRanges[name]
	if v == 0 && name != "finderTagConfirmThreshold" {
		return v
	}
	if math.IsNaN(v) {
		return 0
	}
	if v < 0 && name != "finderTagConfirmThreshold" {
		return 0 // a negative value means "the default", never "off"
	}
	return math.Min(math.Max(v, r.Min), r.Max)
}

// clampTopLevel pulls every ranged field into its range (Load calls it after the
// defaults), so a value that got past the editor's checks still cannot disable a clamp.
func clampTopLevel(tl *TopLevel) {
	tl.CoalesceWindowMs = int(clampTo("coalesceWindowMs", float64(tl.CoalesceWindowMs)))
	tl.FineFactor = clampTo("fineFactor", tl.FineFactor)
	tl.PxPerUnit = clampTo("pxPerUnit", tl.PxPerUnit)
	tl.MaxScrubPx = clampTo("maxScrubPx", tl.MaxScrubPx)
	tl.MinScrubPx = clampTo("minScrubPx", tl.MinScrubPx)
	tl.MarkerResizeFramesPerStep = int(clampTo("markerResizeFramesPerStep", float64(tl.MarkerResizeFramesPerStep)))
	tl.KeyDebounceMs = int(clampTo("keyDebounceMs", float64(tl.KeyDebounceMs)))
	tl.SelectionGraceMs = int(clampTo("selectionGraceMs", float64(tl.SelectionGraceMs)))
	tl.FinderTagConfirmThreshold = int(clampTo("finderTagConfirmThreshold", float64(tl.FinderTagConfirmThreshold)))
	tl.HUD.HeightPx = int(clampTo("hud.heightPx", float64(tl.HUD.HeightPx)))
	for w, m := range tl.WheelSensMultipliers {
		if math.IsNaN(m) || math.IsInf(m, 0) || m <= 0 {
			tl.WheelSensMultipliers[w] = 1 // never zero/negative/NaN; tight bounds are editor-side only
		}
	}
	if tl.MinScrubPx > 0 && tl.MaxScrubPx > 0 && tl.MinScrubPx > tl.MaxScrubPx {
		tl.MinScrubPx = tl.MaxScrubPx
	}
}

// DecodeTopLevel decodes a preset's settings map into TopLevel exactly as the
// engine decodes `_config` (encoding/json: case-insensitive, unknown keys such as
// actionDefaults ignored).
func DecodeTopLevel(settings map[string]any) (TopLevel, error) {
	var tl TopLevel
	data, err := json.Marshal(settings)
	if err != nil {
		return tl, err
	}
	if err := json.Unmarshal(data, &tl); err != nil {
		return TopLevel{}, err
	}
	return tl, nil
}

// NonCanonicalTopLevelKeys lists settings keys that fold to a TopLevel field
// name without being that exact spelling (see NonCanonicalKeys).
func NonCanonicalTopLevelKeys(settings map[string]any) []NonCanonical {
	return nonCanonical(reflect.TypeOf(TopLevel{}), settings, "")
}

// EffectiveTopLevelDefault is the built-in default of a ranged field (0 if none).
func EffectiveTopLevelDefault(name string) float64 { return topLevelDefaults[name] }
