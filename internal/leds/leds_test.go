package leds

import (
	"errors"
	"testing"
)

type rec struct {
	sent [][2]uint8
	err  error
}

func (r *rec) NoteOn(n, v uint8) error {
	if r.err != nil {
		return r.err
	}
	r.sent = append(r.sent, [2]uint8{n, v})
	return nil
}

func TestPlanLightsExactlyOneModeAndCustom(t *testing.T) {
	lit := func(mode string, custom bool) (names string) {
		for _, m := range Plan(mode, custom) {
			if m.Vel > 0 {
				names += m.Lamp.Name + " "
			}
		}
		return
	}
	for in, want := range map[[2]any]string{{"hue", false}: "Hue ", {"sat", true}: "Sat CustomMode ", {"lum", false}: "Lum ", {"", false}: "", {"bogus", true}: "CustomMode "} {
		if got := lit(in[0].(string), in[1].(bool)); got != want {
			t.Errorf("%v: %q want %q", in, got, want)
		}
	}
}

func TestDriverSendsOnlyChangesAndOnlyWhenArmed(t *testing.T) {
	r := &rec{}
	armed := false
	d := New(r, func() bool { return armed })
	d.Update("hue", false)
	if len(r.sent) != 0 {
		t.Fatal("disarmed: nothing may be sent")
	}
	armed = true
	d.Update("hue", false)
	if len(r.sent) != 4 {
		t.Fatalf("first update sends all 4 lamps, sent %d", len(r.sent))
	}
	d.Update("hue", false)
	if len(r.sent) != 4 {
		t.Fatal("unchanged state must send nothing")
	}
	d.Update("sat", false)
	if len(r.sent) != 6 || r.sent[4] != [2]uint8{98, 0} || r.sent[5] != [2]uint8{99, 127} {
		t.Fatalf("hue->sat: %v", r.sent[4:])
	}
	d.Forget()
	d.Update("sat", false)
	if len(r.sent) != 10 {
		t.Fatalf("after Forget everything is resent: %d", len(r.sent))
	}
}

func TestDriverErrorsAreLoggedNotFatal(t *testing.T) {
	r := &rec{err: errors.New("no port")}
	var logged int
	d := New(r, func() bool { return true })
	d.Logf = func(string, ...any) { logged++ }
	d.Update("hue", false)
	if logged != 1 {
		t.Fatalf("logged %d", logged)
	}
	if New(nil, nil) == nil {
		t.Fatal("nil sender must still construct")
	}
	New(nil, nil).Update("hue", false) // must not panic
}
