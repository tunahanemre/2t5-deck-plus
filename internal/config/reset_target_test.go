package config

import "testing"

// TestSelectWheelAliasesSelectTarget pins the backward-compatibility contract:
// today's shipped config uses "selectWheel"/"resetActiveWheel", and the layer
// spec's decision 3 requires that existing config and its tests need no edits.
// effectiveType() only falls back Type->Method and does no type-string
// aliasing, so the aliases live in the predicates themselves.
func TestSelectWheelAliasesSelectTarget(t *testing.T) {
	old := Binding{Type: "selectWheel", Wheel: "gamma"}
	if !old.IsSelectTarget() {
		t.Error("selectWheel must satisfy IsSelectTarget()")
	}
	if got := old.TargetName(); got != "gamma" {
		t.Errorf("TargetName() = %q, want gamma", got)
	}

	fresh := Binding{Type: "selectTarget", Target: "crop"}
	if !fresh.IsSelectTarget() {
		t.Error("selectTarget must satisfy IsSelectTarget()")
	}
	if got := fresh.TargetName(); got != "crop" {
		t.Errorf("TargetName() = %q, want crop", got)
	}
}

func TestTargetNamePrefersExplicitTarget(t *testing.T) {
	b := Binding{Type: "selectTarget", Target: "crop", Wheel: "gamma"}
	if got := b.TargetName(); got != "crop" {
		t.Errorf("TargetName() = %q, want crop — Target must win over Wheel", got)
	}
}

func TestResetActiveWheelAliasesResetSelected(t *testing.T) {
	if !(Binding{Type: "resetActiveWheel"}).IsResetSelected() {
		t.Error("resetActiveWheel must satisfy IsResetSelected()")
	}
	if !(Binding{Type: "resetSelected"}).IsResetSelected() {
		t.Error("resetSelected must satisfy IsResetSelected()")
	}
	for _, ty := range []string{"key", "wheel", "cdl", "layer", ""} {
		if (Binding{Type: ty}).IsResetSelected() {
			t.Errorf("Type=%q must not satisfy IsResetSelected()", ty)
		}
	}
}

func TestResetTargetsParsed(t *testing.T) {
	c := loadInline(t, `{
	  "_config": {"resetTargets": {
	    "lift": {"type":"click","point":"lift_reset"},
	    "sat":  {"type":"resetParam","param":"sat","ch":"master"},
	    "crop": {"type":"resetProperty","properties":["CropLeft","CropRight"]}
	  }},
	  "col": {}
	}`)
	if got := c.TopLevel.ResetTargets["lift"].Point; got != "lift_reset" {
		t.Errorf("lift.Point = %q, want lift_reset", got)
	}
	if got := c.TopLevel.ResetTargets["sat"].Param; got != "sat" {
		t.Errorf("sat.Param = %q, want sat", got)
	}
	if got := c.TopLevel.ResetTargets["crop"].Properties; len(got) != 2 || got[0] != "CropLeft" {
		t.Errorf("crop.Properties = %v", got)
	}
}

// TestResetTargetsAbsentIsEmptyNotNilDeref guards the common case: no
// resetTargets block at all must not panic anything downstream.
func TestResetTargetsAbsentIsEmptyNotNilDeref(t *testing.T) {
	c := loadInline(t, `{"col": {}}`)
	if got := len(c.TopLevel.ResetTargets); got != 0 {
		t.Errorf("absent resetTargets should be empty, got %d entries", got)
	}
	if rt, ok := c.TopLevel.ResetTargets["nope"]; ok || rt.Type != "" {
		t.Errorf("lookup in an absent table must miss cleanly, got %+v ok=%v", rt, ok)
	}
}
