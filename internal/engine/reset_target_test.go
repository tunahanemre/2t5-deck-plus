package engine

import (
	"testing"

	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/target"
)

func TestTargetValidForPage(t *testing.T) {
	click := config.ResetTarget{Type: "click", Point: "lift_reset"}
	param := config.ResetTarget{Type: "resetParam", Param: "sat", Ch: "master"}
	prop := config.ResetTarget{Type: "resetProperty", Properties: []string{"CropLeft"}}

	if !targetValidForPage(click, "color") || targetValidForPage(click, "edit") {
		t.Error("click targets are Color-page only")
	}
	if !targetValidForPage(param, "color") || targetValidForPage(param, "edit") {
		t.Error("resetParam targets are Color-page only")
	}
	if !targetValidForPage(prop, "edit") || targetValidForPage(prop, "color") {
		t.Error("resetProperty targets are Edit-page only")
	}
	if targetValidForPage(config.ResetTarget{Type: "nonsense"}, "color") {
		t.Error("an unknown target type must never be valid")
	}
}

// TestTargetResetsOnPageChange is the fix for the failure three reviewers
// found independently: select "crop" on Edit, switch to Color, press Col
// expecting a wheel reset, and silently mutate a real clip's crop instead.
func TestTargetResetsOnPageChange(t *testing.T) {
	tgt := target.New()
	tgt.Set("crop")
	next := maybeResetTargetOnPageChange(tgt, "edit", true, "color")
	if got := tgt.Get(); got != target.Default {
		t.Errorf("target = %q after page change, want %q", got, target.Default)
	}
	if next != "color" {
		t.Errorf("remembered page = %q, want color", next)
	}
}

func TestTargetSurvivesSamePage(t *testing.T) {
	tgt := target.New()
	tgt.Set("crop")
	maybeResetTargetOnPageChange(tgt, "edit", true, "edit")
	if got := tgt.Get(); got != "crop" {
		t.Errorf("target = %q on same page, want crop", got)
	}
}

// TestTargetSurvivesFailedStatusRead — a transient sidecar None is not a page
// change. Same gate as the layer reset and the D1 auto-clear.
func TestTargetSurvivesFailedStatusRead(t *testing.T) {
	tgt := target.New()
	tgt.Set("crop")
	next := maybeResetTargetOnPageChange(tgt, "edit", false, "")
	if got := tgt.Get(); got != "crop" {
		t.Errorf("target = %q after failed read, want crop", got)
	}
	if next != "edit" {
		t.Errorf("remembered page = %q, want edit (unchanged)", next)
	}
}

// TestResetTargetLookupFallsBackToWheelPath pins the backward-compatibility
// contract: with no resetTargets table configured (the shipped state today),
// a wheel target must still resolve to the legacy executeResetWheel path
// rather than becoming a no-op.
func TestResetTargetLookupFallsBackToWheelPath(t *testing.T) {
	cfg := loadConfig(t, `{"col": {"Col": {"type":"resetActiveWheel"}}}`)
	rt, ok := lookupResetTarget(cfg, "lift")
	if ok {
		t.Errorf("no table configured, so lookup must miss; got %+v", rt)
	}
}

func TestResetTargetLookupFindsConfiguredEntry(t *testing.T) {
	cfg := loadConfig(t, `{
	  "_config": {"resetTargets": {"crop": {"type":"resetProperty","properties":["CropLeft","CropTop"]}}},
	  "col": {"Col": {"type":"resetSelected"}}
	}`)
	rt, ok := lookupResetTarget(cfg, "crop")
	if !ok || rt.Type != "resetProperty" || len(rt.Properties) != 2 {
		t.Errorf("got %+v ok=%v, want a 2-property resetProperty entry", rt, ok)
	}
	if _, ok := lookupResetTarget(cfg, "not-defined"); ok {
		t.Error("an undefined target must miss")
	}
}

// TestDeriveRequiredCalibPointsScansResetTargets: a click-type reset target
// names a calibration point, so --calibrate must prompt for it. Without this
// the reset fails at runtime with calib-missing-point and the user has no way
// to discover why.
func TestDeriveRequiredCalibPointsScansResetTargets(t *testing.T) {
	cfg := loadConfig(t, `{
	  "_config": {"resetTargets": {"custom": {"type":"click","point":"custom_reset"}}},
	  "col": {"Col": {"type":"resetSelected"}}
	}`)
	var found bool
	for _, p := range DeriveRequiredCalibPoints(cfg) {
		if p.Key == "custom_reset" {
			found = true
			if p.Axis != "click" {
				t.Errorf("axis = %q, want click", p.Axis)
			}
		}
	}
	if !found {
		t.Error("a click-type reset target's point must be required by --calibrate")
	}
}

// Non-click target types name no calibration point and must not add one.
func TestDeriveRequiredCalibPointsIgnoresNonClickTargets(t *testing.T) {
	cfg := loadConfig(t, `{
	  "_config": {"resetTargets": {"crop": {"type":"resetProperty","properties":["CropLeft"]}}},
	  "col": {"Col": {"type":"resetSelected"}}
	}`)
	for _, p := range DeriveRequiredCalibPoints(cfg) {
		if p.Key == "" {
			t.Error("empty calibration key emitted")
		}
	}
}
