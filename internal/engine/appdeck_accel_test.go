package engine

import (
	"testing"
	"time"

	"2t5-deck-plus/internal/appdeck"
)

// Wave C: the event carries the multiplier appdeck really applied; without
// one it stays 1 (today's value).
func TestEmitAppdeck_CarriesRealAccel(t *testing.T) {
	e := &Engine{}
	ch := make(chan InputEvent, 4)
	e.SetInputObserver(func(ev InputEvent) { ch <- ev })
	now := time.Now()
	e.emitAppdeck("D1", "turn", 2, appdeck.Result{Handled: true, Fired: true, Accel: 2.5, Action: "generic.scrollDelta"}, false, now)
	e.emitAppdeck("D2", "turn", 2, appdeck.Result{Handled: true, Fired: true, Action: "generic.scrollDelta"}, false, now)
	e.ObserveAppdeckTail("D1", appdeck.Result{Handled: true, Fired: true, Accel: 4, Text: "scroll +1"})
	e.ObserveAppdeckTail("D3", appdeck.Result{Handled: false})
	want := []struct {
		c string
		a float64
	}{{"D1", 2.5}, {"D2", 1}, {"D1", 4}}
	for _, w := range want {
		select {
		case ev := <-ch:
			if ev.Control != w.c || ev.Accel != w.a {
				t.Fatalf("got %s accel %v, want %s %v", ev.Control, ev.Accel, w.c, w.a)
			}
		case <-time.After(time.Second):
			t.Fatal("event missing")
		}
	}
	select {
	case ev := <-ch:
		t.Fatalf("unhandled tail must not emit: %+v", ev)
	case <-time.After(100 * time.Millisecond):
	}
}
