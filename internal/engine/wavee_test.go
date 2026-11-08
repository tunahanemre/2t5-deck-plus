package engine

// Wave E (technical-critic pass 1) engine tests: F3 self-frontmost, F5/F6
// sidecar supervision, F7 arm switch, F8 held overlays, F9 layers per app,
// F14 observer channel. Everything is faked: no hardware, no real input.

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"

	"2t5-deck-plus/internal/appdeck"
	"2t5-deck-plus/internal/ipc"
	"2t5-deck-plus/internal/preset"
)

const tSelf = "com.2t5.deck-plus"

func fakeSelf(t *testing.T, id string) {
	t.Helper()
	orig := selfBundleIDFn
	selfBundleIDFn = func() string { return id }
	t.Cleanup(func() { selfBundleIDFn = orig })
}

// ---- F7: the switch beyond appdeck ------------------------------------------------------

func TestSidecarOpsAreAllClassified(t *testing.T) {
	src, err := os.ReadFile("../../sidecar/sidecar.py")
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`(?m)^\s*(?:el)?if op == "([a-z_]+)"`)
	found := map[string]bool{}
	for _, m := range re.FindAllStringSubmatch(string(src), -1) {
		found[m[1]] = true
	}
	if len(found) < 20 {
		t.Fatalf("parsed only %d ops from sidecar.py: %v", len(found), found)
	}
	known := map[string]bool{}
	for op := range sidecarReadOnlyOps {
		known[op] = true
	}
	for _, op := range sidecarMutatingOps {
		if known[op] {
			t.Errorf("op %q is in both lists", op)
		}
		known[op] = true
	}
	var missing, stale []string
	for op := range found {
		if !known[op] {
			missing = append(missing, op)
		}
	}
	for op := range known {
		if !found[op] {
			stale = append(stale, op)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)
	if len(missing) > 0 {
		t.Errorf("sidecar ops not classified for the arm switch (add to sidecarReadOnlyOps or sidecarMutatingOps): %v", missing)
	}
	if len(stale) > 0 {
		t.Errorf("classified ops the sidecar no longer has: %v", stale)
	}
}

func TestDisarmedHoldsBackSidecarWritesButNotQueries(t *testing.T) {
	h := newLazySidecarHarness(t)
	h.running.Store(true)
	h.front.Store(true)
	ad := appdeck.NewDisarmed()
	t.Cleanup(func() { ad.SetArmed(true) })
	h.eng.SetAppDeck(ad)
	h.start(t)
	waitFor(t, "sidecar connected", func() bool { return h.eng.getClient() != nil })
	c := h.eng.getClient()

	resp, err := c.Send(map[string]any{"op": "jog", "frames": 5})
	if err != nil || resp["disarmed"] != true || resp["ok"] != false {
		t.Errorf("disarmed jog = %v %v", resp, err)
	}
	for _, op := range sidecarMutatingOps {
		if r, _ := c.Send(map[string]any{"op": op}); r["disarmed"] != true {
			t.Errorf("disarmed %s reached the sidecar: %v", op, r)
		}
	}
	for op := range sidecarReadOnlyOps {
		if r, _ := c.Send(map[string]any{"op": op}); r["disarmed"] == true {
			t.Errorf("disarmed %s was refused but is read-only", op)
		}
	}
	ad.SetArmed(true)
	if r, _ := c.Send(map[string]any{"op": "jog", "frames": 5}); r["disarmed"] == true {
		t.Errorf("armed again but jog still refused: %v", r)
	}
}

func TestDisarmedFinderTagWritesNothing(t *testing.T) {
	f := &fakeFinder{sel: []string{"/a.mov", "/b.mov"}, n: 2}
	f.install(t)
	e := &Engine{}
	ad := appdeck.NewDisarmed()
	t.Cleanup(func() { ad.SetArmed(true) })
	e.SetAppDeck(ad)
	jobs := startWorker(t, e, defaultFinderTagConfirmThreshold)
	jobs <- finderTagJob{control: "P1btn", colour: "Red"}
	s := waitForAction(t, e, "")
	if s != "disarmed: Red not applied to 2 files" {
		t.Errorf("LastAction = %q", s)
	}
	expectNoApply(t, f)
	ad.SetArmed(true)
	jobs <- finderTagJob{control: "P1btn", colour: "Red"}
	<-f.applied // armed: it writes
}

// ---- F3: the editor window itself is frontmost -----------------------------------------------

func TestSelfFrontmost_SwallowsPressAndTurnButReportsThem(t *testing.T) {
	fakeSelf(t, tSelf)
	r := newAdRig(t, nil2(t))
	r.setFront(tSelf)
	time.Sleep(60 * time.Millisecond) // past the frontmost cache

	// C8 has a Global key; C9 a Chrome key; L1 a Resolve layer verb.
	for _, n := range []uint8{42, 41, 40} {
		r.cb(midi.NoteOn(0, n, 100), 0)
		ev := r.nextEvent(t)
		if ev.Kind != "press" || !strings.Contains(ev.Out.Text, "editor focused") || !strings.Contains(ev.Out.Text, "not fired") {
			t.Errorf("press %d event = %+v", n, ev)
		}
		r.cb(midi.NoteOff(0, n), 0)
		if ev := r.nextEvent(t); ev.Kind != "release" {
			t.Errorf("release %d event = %+v", n, ev)
		}
	}
	r.cb(midi.ControlChange(0, 36, 3), 0)
	ev := r.nextEvent(t)
	if ev.Kind != "turn" || ev.Control != "D1" || !strings.Contains(ev.Out.Text, "editor focused") {
		t.Errorf("turn event = %+v", ev)
	}
	if k := r.snapKeys(); len(k) != 0 {
		t.Errorf("keys posted INTO the editor: %v", k)
	}
	if sc := r.snapScroll(); len(sc) != 0 {
		t.Errorf("scroll posted INTO the editor: %v", sc)
	}
	if l := r.eng.Status().Layer; l != 1 {
		t.Errorf("a Resolve verb ran with the editor focused: layer=%d", l)
	}
	// back to Chrome: normal again
	r.setFront(tChrome)
	time.Sleep(60 * time.Millisecond)
	r.cb(midi.NoteOn(0, 41, 100), 0)
	if ev := r.nextEvent(t); ev.Out.Text != "Cmd+T" {
		t.Errorf("after leaving the editor: %+v", ev)
	}
}

func TestSelfFrontmost_MatchIsCaseInsensitiveAndEmptySelfNeverMatches(t *testing.T) {
	fakeSelf(t, tSelf)
	if !isSelfFront("COM.2T5.DECK-PLUS") || isSelfFront("com.google.Chrome") || isSelfFront("") {
		t.Error("isSelfFront wrong")
	}
	fakeSelf(t, "") // not run from a .app bundle
	if isSelfFront("") || isSelfFront("com.google.Chrome") {
		t.Error("an empty self id must match nothing")
	}
}

// ---- F8: a held overlay is always released ---------------------------------------------------

func holdSetup(t *testing.T) func(*preset.Store) {
	return func(s *preset.Store) {
		mustAssign(t, s, preset.ResolveID, "col", "L1", map[string]any{"type": "layer", "layer": 2})
		mustAssign(t, s, tChrome, "base", "C9", map[string]any{"type": "modifierHold", "modifier": "shift"})
	}
}

func TestHeldOverlay_ReleasedAfterHotReloadDuringTheHold(t *testing.T) {
	r := newAdRig(t, holdSetup(t))
	r.cb(midi.NoteOn(0, 41, 100), 0)
	adWaitFor(t, "overlay held", func() bool { return r.eng.getHeldOverlay() == "shift" })
	// the binding stops being a modifierHold while the button is down
	mustAssign(t, r.store, tChrome, "base", "C9", map[string]any{"type": "key", "key": "Cmd+T"})
	r.recompose(t)
	r.cb(midi.NoteOff(0, 41), 0)
	adWaitFor(t, "overlay released", func() bool { return r.eng.getHeldOverlay() == "" })
}

func TestHeldOverlay_ReleasedAfterAppdeckIsRemovedDuringTheHold(t *testing.T) {
	r := newAdRig(t, holdSetup(t))
	r.cb(midi.NoteOn(0, 41, 100), 0)
	adWaitFor(t, "overlay held", func() bool { return r.eng.getHeldOverlay() == "shift" })
	r.eng.SetAppDeck(nil)
	r.cb(midi.NoteOff(0, 41), 0)
	adWaitFor(t, "overlay released", func() bool { return r.eng.getHeldOverlay() == "" })
}

// ---- F9: the layer follows the app -----------------------------------------------------------

func layerSetup(t *testing.T) func(*preset.Store) {
	return func(s *preset.Store) {
		mustAssign(t, s, preset.ResolveID, "col", "L1", map[string]any{"type": "layer", "layer": 2})
		mustAssign(t, s, tChrome, "base", "C9", map[string]any{"type": "layer", "layer": 3})
		mustAssign(t, s, preset.GlobalID, "base", "C8", map[string]any{"type": "key", "key": "Cmd+Shift+4"})
	}
}

func TestLayer_DoesNotLeakBetweenApps(t *testing.T) {
	r := newAdRig(t, layerSetup(t))
	r.cb(midi.NoteOn(0, 41, 100), 0) // Chrome: layer 3
	adWaitFor(t, "layer 3", func() bool { return r.eng.Status().Layer == 3 })
	r.setFront("com.apple.Safari")
	time.Sleep(60 * time.Millisecond)
	r.cb(midi.NoteOn(0, 42, 100), 0) // first press in the other app
	adWaitFor(t, "layer back to 1 in the other app", func() bool { return r.eng.Status().Layer == 1 })
	// and Chrome gets its layer back when it returns
	r.setFront(tChrome)
	time.Sleep(60 * time.Millisecond)
	r.cb(midi.NoteOn(0, 43, 100), 0) // an unassigned button: only samples the frontmost
	adWaitFor(t, "chrome layer restored", func() bool { return r.eng.Status().Layer == 3 })
}

// ---- F14: the observer goroutine ends with the engine -----------------------------------------

func TestInputObserverChannelIsClosedByStop(t *testing.T) {
	r := newAdRig(t, nil2(t))
	r.eng.hooks.obsMu.RLock()
	ch := r.eng.hooks.obsCh
	r.eng.hooks.obsMu.RUnlock()
	if ch == nil {
		t.Fatal("no observer channel while observing")
	}
	var calls atomic.Int32
	r.eng.SetInputObserver(func(InputEvent) { calls.Add(1) })
	if err := r.eng.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("unexpected event on the closed channel")
		}
	case <-time.After(time.Second):
		t.Fatal("the observer channel is still open after Stop")
	}
	before := calls.Load()
	r.eng.emitInput(InputEvent{Control: "C1"}) // after Stop: must not panic, must not deliver
	time.Sleep(30 * time.Millisecond)
	if calls.Load() != before {
		t.Error("an event reached the observer after Stop")
	}
}

// ---- F5: nothing is spawned for a Resolve that is not running ---------------------------------

func TestLazySidecar_ResolveQuitsWhileConnected_ClientAndSidecarAreDropped(t *testing.T) {
	h := newLazySidecarHarness(t)
	var stops atomic.Int32
	launchSidecarFn = func(string) (func(), string, error) {
		h.launches.Add(1)
		return func() { stops.Add(1) }, "", nil
	}
	h.running.Store(true)
	h.front.Store(true)
	h.start(t)
	waitFor(t, "connected", func() bool { return h.eng.Status().SidecarConnected })

	h.running.Store(false)
	h.front.Store(false)
	waitFor(t, "client dropped", func() bool { return h.eng.getClient() == nil })
	st := h.eng.Status()
	if st.SidecarState != SidecarStateWaiting || st.SidecarConnected || st.SidecarError != "" {
		t.Errorf("after Resolve quit: state=%q connected=%v err=%q", st.SidecarState, st.SidecarConnected, st.SidecarError)
	}
	if stops.Load() != 1 {
		t.Errorf("sidecar stopped %d times, want 1", stops.Load())
	}
	// and it comes back when Resolve does
	n := h.launches.Load()
	h.running.Store(true)
	h.front.Store(true)
	waitFor(t, "reconnected", func() bool { return h.eng.Status().SidecarConnected && h.launches.Load() > n })
}

func TestRecoverSidecar_DoesNothingWhileResolveIsNotRunning(t *testing.T) {
	e := newTestEngine(t)
	var launches atomic.Int32
	origState, origLaunch, origBackoff := resolveStateFn, launchSidecarFn, sidecarRecoveryBackoff
	t.Cleanup(func() { resolveStateFn, launchSidecarFn, sidecarRecoveryBackoff = origState, origLaunch, origBackoff })
	sidecarRecoveryBackoff = 0
	resolveStateFn = func() (bool, bool) { return false, false }
	launchSidecarFn = func(string) (func(), string, error) { launches.Add(1); return func() {}, "", nil }
	cl := &ipc.Client{}
	e.clientPtr.Store(cl) // recovery only acts for the engine's CURRENT client (wave G, B8)

	e.recoverSidecar(context.Background(), cl)
	e.sidecarRecoveryWG.Wait()
	if launches.Load() != 0 {
		t.Fatalf("recoverSidecar launched a sidecar %d times for a Resolve that is not running", launches.Load())
	}
	// Resolve running: it does try (the gate is not a blanket off switch)
	resolveStateFn = func() (bool, bool) { return true, false }
	e.recoverSidecar(context.Background(), cl)
	e.sidecarRecoveryWG.Wait()
	if launches.Load() != 1 {
		t.Fatalf("recoverSidecar launches = %d with Resolve running, want 1", launches.Load())
	}
}

// ---- F6: Stop does not wait for a cold launch --------------------------------------------------

func TestStop_DoesNotWaitForASidecarLaunchInFlight(t *testing.T) {
	h := newLazySidecarHarness(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	var stopped atomic.Int32
	launchSidecarFn = func(string) (func(), string, error) {
		close(entered)
		<-release // a cold Python start, up to 15 s in production
		return func() { stopped.Add(1) }, "", nil
	}
	h.running.Store(true)
	h.front.Store(true)
	if err := h.eng.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("launch never started")
	}
	done := make(chan struct{})
	go func() { h.eng.Stop(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("Stop is blocked on a sidecar launch that is still in flight")
	}
	close(release) // the launch finally returns: what it started must not be left running
	waitFor(t, "late sidecar stopped", func() bool { return stopped.Load() == 1 })
	if h.eng.getClient() != nil {
		t.Error("a client was published after Stop")
	}
}

// Fn + C6 (here C8 = hold the fn bank, C9 = toggleHud in Global's fn bank) toggles the
// HUD from any app and with the editor window itself frontmost: the hold and the toggle
// post nothing into the editor, so they are not swallowed (Tunahan 2026-11-05: "its not").
func TestFnChordTogglesHud_EvenWithEditorFocused(t *testing.T) {
	fakeSelf(t, tSelf)
	r := newAdRig(t, func(s *preset.Store) {
		mustAssign(t, s, preset.GlobalID, "base", "C8", map[string]any{"type": "modifierHold", "modifier": "fn"})
		mustAssign(t, s, preset.GlobalID, "fn", "C9", map[string]any{"type": "toggleHud"})
	})
	var mu sync.Mutex
	calls := 0
	r.eng.SetToggleHUDCallback(func() bool { mu.Lock(); calls++; mu.Unlock(); return true })
	count := func() int { mu.Lock(); defer mu.Unlock(); return calls }

	for _, front := range []string{tSelf, tChrome, "com.apple.finder"} {
		r.setFront(front)
		time.Sleep(60 * time.Millisecond) // past the frontmost cache
		before := count()
		r.cb(midi.NoteOn(0, 41, 100), 0) // C9 alone: no Fn held
		r.cb(midi.NoteOff(0, 41), 0)
		time.Sleep(80 * time.Millisecond) // past the 50 ms key debounce
		if count() != before {
			t.Fatalf("front=%s: C9 without Fn toggled the HUD", front)
		}
		r.cb(midi.NoteOn(0, 42, 100), 0) // hold Fn
		time.Sleep(80 * time.Millisecond)
		r.cb(midi.NoteOn(0, 41, 100), 0) // C6
		adWaitFor(t, "toggleHUD callback ("+front+")", func() bool { return count() == before+1 })
		r.cb(midi.NoteOff(0, 41), 0)
		r.cb(midi.NoteOff(0, 42), 0) // release Fn
		time.Sleep(80 * time.Millisecond)
		if h := r.eng.getHeldOverlay(); h != "" {
			t.Fatalf("front=%s: the fn overlay stayed held after release: %q", front, h)
		}
	}
	if k := r.snapKeys(); len(k) != 0 {
		t.Errorf("keys posted: %v", k)
	}
	for len(r.events) > 0 {
		<-r.events
	}
}

// L1 / L2 / L3 from Global switch the deck layer outside Resolve and with the editor
// focused (here C8 / C9 stand in for L2 / L3); the layer inherits base, so the keys keep
// working once a layer is active.
func TestGlobalLayerKeysSwitchLayerOutsideResolve(t *testing.T) {
	fakeSelf(t, tSelf)
	r := newAdRig(t, func(s *preset.Store) {
		mustAssign(t, s, preset.GlobalID, "base", "C8", map[string]any{"type": "layer", "layer": 2})
		mustAssign(t, s, preset.GlobalID, "base", "C9", map[string]any{"type": "layer", "layer": 3})
		mustAssign(t, s, preset.GlobalID, "base", "L1", map[string]any{"type": "layer", "layer": 1})
	})
	for _, front := range []string{tChrome, "com.apple.finder", tSelf} {
		r.setFront(front)
		time.Sleep(60 * time.Millisecond)
		for _, st := range []struct {
			note uint8
			want int
		}{{42, 2}, {41, 3}, {40, 1}} {
			r.cb(midi.NoteOn(0, st.note, 100), 0)
			adWaitFor(t, fmt.Sprintf("layer %d in %s", st.want, front), func() bool { return r.eng.Status().Layer == st.want })
			r.cb(midi.NoteOff(0, st.note), 0)
			time.Sleep(80 * time.Millisecond)
		}
	}
}

// The layer change reaches the editor through the same state hook as Hue / Sat / Lum, the
// moment the press is handled (not on a poll): measured press -> hook, well under 100 ms.
func TestLayerChangeIsPushedToTheEditorAtOnce(t *testing.T) {
	r := newAdRig(t, func(s *preset.Store) {
		mustAssign(t, s, preset.GlobalID, "base", "C8", map[string]any{"type": "layer", "layer": 2})
		mustAssign(t, s, preset.GlobalID, "base", "C9", map[string]any{"type": "layer", "layer": 3})
	})
	type got struct {
		layer int
		at    time.Time
	}
	ch := make(chan got, 8)
	r.eng.SetStateHook(func(mode string, custom bool, layer int) { ch <- got{layer, time.Now()} })
	for _, st := range []struct {
		note  uint8
		layer int
	}{{42, 2}, {41, 3}} {
		for len(ch) > 0 {
			<-ch
		}
		t0 := time.Now()
		r.cb(midi.NoteOn(0, st.note, 100), 0)
		select {
		case g := <-ch:
			d := g.at.Sub(t0)
			t.Logf("layer %d pushed %v after the press", g.layer, d)
			if g.layer != st.layer || d > 100*time.Millisecond {
				t.Fatalf("layer push = %+v after %v, want layer %d under 100 ms", g, d, st.layer)
			}
		case <-time.After(time.Second):
			t.Fatalf("layer %d was never pushed to the editor", st.layer)
		}
		r.cb(midi.NoteOff(0, st.note), 0)
		time.Sleep(80 * time.Millisecond)
	}
}
