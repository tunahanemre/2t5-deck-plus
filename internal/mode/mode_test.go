package mode

import "testing"

func TestNewDefaultIsWheels(t *testing.T) {
	tr := New()
	if tr.Current() != Wheels {
		t.Errorf("default mode = %q, want %q", tr.Current(), Wheels)
	}
}

func TestAllowKnobActionWheels(t *testing.T) {
	tr := New()
	if !tr.AllowKnobAction() {
		t.Error("wheels mode must allow knob actions")
	}
}

func TestAllowKnobActionLog(t *testing.T) {
	tr := New()
	tr.Set("log")
	if !tr.AllowKnobAction() {
		t.Error("log mode must allow knob actions")
	}
}

func TestAllowKnobActionBars(t *testing.T) {
	tr := New()
	tr.Set("bars")
	if tr.AllowKnobAction() {
		t.Error("bars mode must suppress knob actions")
	}
}

func TestSetModeRoundTrip(t *testing.T) {
	tr := New()
	for _, m := range []string{"bars", "log", "wheels"} {
		tr.Set(m)
		if string(tr.Current()) != m {
			t.Errorf("after Set(%q) Current() = %q", m, tr.Current())
		}
	}
}

func TestSetUnknownIgnored(t *testing.T) {
	tr := New()
	tr.Set("bars")
	tr.Set("bogus") // unknown — should leave "bars" unchanged
	if tr.Current() != Bars {
		t.Errorf("unknown Set must not change mode; got %q", tr.Current())
	}
}
