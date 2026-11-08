package config

import "testing"

func TestPControlAndModeBankNames(t *testing.T) {
	for c, want := range map[string]bool{"P1": true, "P8": true, "P1btn": true, "P8btn": true, "P0": false, "P9": false, "P1b": false, "Hue": false, "C1": false, "": false} {
		if IsPControl(c) != want {
			t.Errorf("IsPControl(%q) != %v", c, want)
		}
	}
	for _, c := range []struct{ bank, mode string }{{"pm-hue", "hue"}, {"col-pm-sat", "sat"}, {"com.x.App-pm-lum", "lum"}} {
		if m, ok := ParseModeBank(c.bank); !ok || m != c.mode {
			t.Errorf("ParseModeBank(%q) = %q %v", c.bank, m, ok)
		}
	}
	for _, b := range []string{"col", "col-shift", "col-2", "pm-blue", "pm-"} {
		if _, ok := ParseModeBank(b); ok {
			t.Errorf("%q is not a mode bank", b)
		}
	}
	if ModeBankOf("edit", "lum") != "edit-pm-lum" || PModeBank("sat") != "pm-sat" {
		t.Fatal("names")
	}
	if HueCurveKey("sat", 0) != "hvs_red" || HueCurveKey("lum", 7) != "hvl_magenta" || HueCurveKey("hue", 0) != "" || HueCurveKey("sat", 8) != "" || HueCurveKey("sat", -1) != "" {
		t.Fatal("HueCurveKey")
	}
}

func TestModeBindingFallbackOrder(t *testing.T) {
	c := &Config{bindings: map[string]map[string]Binding{
		"col-pm-sat":  {"P1": {Type: "key", Key: "col-sat"}, "P2": {Type: "key", Key: "col-sat-2"}, "Hue": {Type: "key", Key: "never"}},
		"edit-pm-sat": {"P1": {Type: "key", Key: "edit-sat"}},
	}}
	if b, bank, ok := c.ModeBinding("edit", "sat", "P1"); !ok || b.Key != "edit-sat" || bank != "edit-pm-sat" {
		t.Fatalf("page bank first: %v %q", b, bank)
	}
	if b, bank, ok := c.ModeBinding("edit", "sat", "P2"); !ok || b.Key != "col-sat-2" || bank != "col-pm-sat" {
		t.Fatalf("then col: %v %q", b, bank)
	}
	for _, args := range [][3]string{{"edit", "hue", "P1"}, {"edit", "", "P1"}, {"edit", "bogus", "P1"}, {"edit", "sat", "Hue"}, {"edit", "sat", "P3"}} {
		if _, _, ok := c.ModeBinding(args[0], args[1], args[2]); ok {
			t.Errorf("%v must fall through", args)
		}
	}
}
