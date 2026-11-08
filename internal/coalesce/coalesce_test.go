package coalesce

import "testing"

func TestAccumulateAndDrain(t *testing.T) {
	a := New()
	a.Add("knob1", 3)
	a.Add("knob1", -1) // net +2
	a.Add("knob2", 5)
	out := a.Drain()
	if out["knob1"] != 2 {
		t.Errorf("knob1 = %d want 2", out["knob1"])
	}
	if out["knob2"] != 5 {
		t.Errorf("knob2 = %d want 5", out["knob2"])
	}
	// after drain, empty
	if len(a.Drain()) != 0 {
		t.Errorf("second drain must be empty")
	}
}
