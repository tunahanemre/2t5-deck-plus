package editorapi

import (
	"testing"
	"time"
)

func TestBusWaitReturnsAtOnceOnPublish(t *testing.T) {
	b := NewBus()
	go func() { time.Sleep(20 * time.Millisecond); b.Publish(Event{Type: "deck-state", Mode: "sat"}) }()
	t0 := time.Now()
	next, evs := b.Wait(0, 5*time.Second, nil)
	if len(evs) != 1 || evs[0].Mode != "sat" || next != 1 {
		t.Fatalf("got next=%d evs=%v", next, evs)
	}
	if d := time.Since(t0); d > 500*time.Millisecond {
		t.Fatalf("long poll took %v", d)
	}
}

func TestBusWaitTimesOutAndHonoursDone(t *testing.T) {
	b := &Bus{} // zero value must work too
	if _, evs := b.Wait(0, 30*time.Millisecond, nil); len(evs) != 0 {
		t.Fatal("expected nothing")
	}
	done := make(chan struct{})
	close(done)
	t0 := time.Now()
	b.Wait(0, 5*time.Second, done)
	if time.Since(t0) > time.Second {
		t.Fatal("done was ignored")
	}
}
