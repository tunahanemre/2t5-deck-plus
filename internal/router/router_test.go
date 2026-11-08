package router

import (
	"sync"
	"testing"

	"2t5-deck-plus/internal/accel"
	"2t5-deck-plus/internal/config"
)

type fakeCfg struct{}

func (fakeCfg) ControlForCC(n int) string {
	if n == 36 {
		return "Saturation"
	}
	return ""
}
func (fakeCfg) ControlForNote(n int) string {
	if n == 36 {
		return "Saturation"
	}
	if n == 65 {
		return "Col"
	}
	return ""
}
func (fakeCfg) Binding(bank, ctrl string) (config.Binding, bool) {
	if bank == "col" && ctrl == "Saturation" {
		return config.Binding{Param: "sat", Ch: "master"}, true
	}
	return config.Binding{}, false
}

func TestRouteSat(t *testing.T) {
	r := New(fakeCfg{}, accel.New(0.01, 1.0), 1)
	control := fakeCfg{}.ControlForCC(36)
	cmd, ok := r.Route("col", control, 5) // step 5 -> 0.05
	if !ok {
		t.Fatal("expected a command")
	}
	if cmd.Op != "sat" || cmd.Node != 1 || cmd.Amt < 0.049 || cmd.Amt > 0.051 {
		t.Errorf("cmd = %+v", cmd)
	}
}

func TestRouteUnknownControl(t *testing.T) {
	r := New(fakeCfg{}, accel.New(0.01, 1.0), 1)
	if _, ok := r.Route("col", "", 5); ok {
		t.Errorf("empty control must be unrouted")
	}
	if _, ok := r.Route("col", "Nope", 5); ok {
		t.Errorf("unbound control must be unrouted")
	}
}

// TestConcurrentSetNodeAndRoute mirrors the live engine's cross-goroutine
// shape: SetNode is called from the MIDI-callback goroutine (Left/Right
// hardwired nav) while Route is called from the ticker/drain goroutine
// (knob-turn CDL dispatch) — an inherited, pre-existing data race on the
// unguarded node field, fixed with a dedicated mutex. This test's only job
// is to fail under `go test -race` if that guard regresses.
func TestConcurrentSetNodeAndRoute(t *testing.T) {
	r := New(fakeCfg{}, accel.New(0.01, 1.0), 1)
	control := fakeCfg{}.ControlForCC(36)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for n := 1; n <= 200; n++ {
			r.SetNode(n)
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			r.Route("col", control, 5)
		}
	}()

	wg.Wait()
}
