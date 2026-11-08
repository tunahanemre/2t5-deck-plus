package appdeck

import (
	"sync"
	"testing"
	"time"

	"2t5-deck-plus/internal/keys"
	"2t5-deck-plus/internal/mouse"
)

// Wave E: F7 (the switch covers keys, mouse, scroll, shell), F15 (a stale
// momentum step must not fire after a stop), F16 (no real seams by accident).

func restoreArmed(t *testing.T, x *Executor) {
	t.Helper()
	t.Cleanup(func() { x.SetArmed(true); keys.SetPaused(false); mouse.SetPaused(false) })
}

func TestWaveE_DisarmingPausesTheKeyAndMousePackages(t *testing.T) {
	x := New()
	restoreArmed(t, x)
	if keys.Paused() || mouse.Paused() {
		t.Fatal("packages paused before the switch was used")
	}
	x.SetArmed(false)
	if !keys.Paused() || !mouse.Paused() {
		t.Errorf("disarmed but keys paused=%v mouse paused=%v", keys.Paused(), mouse.Paused())
	}
	x.SetArmed(true)
	if keys.Paused() || mouse.Paused() {
		t.Error("armed again but the packages stay paused")
	}
}

func TestWaveE_NewDisarmed(t *testing.T) {
	x := NewDisarmed()
	restoreArmed(t, x)
	if x.Armed() {
		t.Fatal("NewDisarmed is armed")
	}
	if !keys.Paused() || !mouse.Paused() {
		t.Error("NewDisarmed left the packages unpaused")
	}
}

func TestWaveE_ArmCoverageNamesWhatTheSwitchHolds(t *testing.T) {
	want := map[string]bool{"keys": true, "mouse": true, "scroll": true, "shell": true, "sidecar-writes": true, "finder-tags": true}
	got := New().ArmCoverage()
	if len(got) != len(want) {
		t.Fatalf("coverage = %v", got)
	}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected coverage entry %q", g)
		}
	}
}

// A tail step that already decided to fire must not run after stopTails has
// returned: stopTails (SetSnapshot / ResetAccumulators) now waits for it.
func TestWaveE_StopTailsWaitsForAnInFlightTailStep(t *testing.T) {
	x, r, c := feelSetup(t, map[string]any{"type": "scrollDelta", "linesPerDetent": 1, "detentThreshold": 1, "momentumMs": 500})
	x.TailGuard = func(string) bool { return true }
	for i := 0; i < 8; i++ {
		turn(x, c, 4)
		c.advance(15 * time.Millisecond)
	}
	inFire := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	x.Scroll = func(d int, f uint64) {
		r.mu.Lock()
		r.scrolls = append(r.scrolls, "tail")
		r.mu.Unlock()
		once.Do(func() { inFire <- struct{}{}; <-release })
	}
	done := make(chan struct{})
	go func() { c.run(16*time.Millisecond, 400); close(done) }()
	select {
	case <-inFire:
	case <-time.After(2 * time.Second):
		t.Fatal("the tail never fired")
	}
	stopped := make(chan struct{})
	go func() { x.ResetAccumulators(); close(stopped) }()
	select {
	case <-stopped:
		t.Fatal("ResetAccumulators returned while a tail step was still firing: it could fire after the stop")
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("ResetAccumulators never returned")
	}
	<-done
	r.mu.Lock()
	n := len(r.scrolls)
	r.mu.Unlock()
	c.advance(5 * time.Second)
	c.run(16*time.Millisecond, 50)
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.scrolls) != n {
		t.Errorf("a tail fired after the stop: %d -> %d", n, len(r.scrolls))
	}
}
