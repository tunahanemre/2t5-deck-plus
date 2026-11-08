package calib

import "testing"

func TestLoad(t *testing.T) {
	c, err := Load("testdata/calibration.json")
	if err != nil {
		t.Fatal(err)
	}
	p, ok := c.Point("gamma_master")
	if !ok || p.X != 900 || p.Y != 400 || p.Axis != "v" {
		t.Errorf("gamma_master = %+v ok=%v", p, ok)
	}
	if _, ok := c.Point("missing"); ok {
		t.Error("missing must be !ok")
	}

	// Reset-button point (axis "click") round-trips through Load correctly.
	rp, rok := c.Point("lift_reset")
	if !rok {
		t.Fatal("lift_reset must be present in testdata")
	}
	if rp.X != 150 || rp.Y != 300 || rp.Axis != "click" {
		t.Errorf("lift_reset = %+v, want {X:150 Y:300 Axis:click}", rp)
	}

	// Slot-4 master (offset_master) horizontal axis.
	op, oo := c.Point("offset_master")
	if !oo {
		t.Fatal("offset_master must be present in testdata")
	}
	if op.Axis != "h" {
		t.Errorf("offset_master axis = %q, want h", op.Axis)
	}

	// Slot-4 reset (offset_reset) click axis.
	orp, oro := c.Point("offset_reset")
	if !oro {
		t.Fatal("offset_reset must be present in testdata")
	}
	if orp.Axis != "click" {
		t.Errorf("offset_reset axis = %q, want click", orp.Axis)
	}

	// Mode icon click points.
	for _, key := range []string{"mode_wheels", "mode_bars", "mode_log"} {
		mp, mok := c.Point(key)
		if !mok {
			t.Fatalf("%s must be present in testdata", key)
		}
		if mp.Axis != "click" {
			t.Errorf("%s axis = %q, want click", key, mp.Axis)
		}
	}
}

func TestResolverReplacesFilePointsAndCanRefuse(t *testing.T) {
	c := Empty()
	if _, ok := c.Point("a"); ok {
		t.Fatal("empty has no points")
	}
	c.SetResolver(func(key string) (float64, float64, string, bool) {
		if key == "a" {
			return 1, 2, "h", true
		}
		return 0, 0, "", false
	})
	if p, ok := c.Point("a"); !ok || p.X != 1 || p.Y != 2 || p.Axis != "h" {
		t.Fatalf("%v %v", p, ok)
	}
	if _, ok := c.Point("b"); ok {
		t.Fatal("a refusal by the resolver must stand")
	}
	c.SetResolver(nil)
	if _, ok := c.Point("a"); ok {
		t.Fatal("resolver removed")
	}
	var nilc *Calib
	if _, ok := nilc.Legacy("a"); ok {
		t.Fatal("nil Calib has no legacy points")
	}
}
