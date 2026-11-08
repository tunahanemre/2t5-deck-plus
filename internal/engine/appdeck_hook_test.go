package engine

// Hermetic tests for the app-aware layer (appdeck hook, Global fall-through,
// input observer). Frontmost is faked through frontmostBundleIDFn; keys and
// scrolls go to appdeck's recording seams, so nothing posts real input.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"gitlab.com/gomidi/midi/v2"

	"2t5-deck-plus/internal/appdeck"
	"2t5-deck-plus/internal/preset"
)

const (
	tChrome = "com.google.Chrome"
	tDevMap = `{"knobs":{"36":"D1"},"sliders":{},"buttons":{"40":"L1","41":"C9","42":"C8","43":"P1btn"}}`
)

type adRig struct {
	eng    *Engine
	ad     *appdeck.Executor
	store  *preset.Store
	cb     func(midi.Message, int32)
	mu     sync.Mutex
	keys   []string
	scroll []int
	shells []string
	events chan InputEvent
	front  *string
	devMap string
}

func (r *adRig) setFront(id string) { r.mu.Lock(); *r.front = id; r.mu.Unlock() }

func (r *adRig) recompose(t *testing.T) {
	t.Helper()
	snap := r.store.Snapshot()
	cfg, err := snap.ComposeConfig(r.devMap)
	if err != nil {
		t.Fatalf("ComposeConfig: %v", err)
	}
	r.eng.SetConfig(cfg)
	r.ad.SetSnapshot(snap)
}

func newAdRig(t *testing.T, setup func(s *preset.Store)) *adRig {
	t.Helper()
	stubStateFile(t)
	dir := t.TempDir()
	devMap := filepath.Join(dir, "device-map.json")
	if err := os.WriteFile(devMap, []byte(tDevMap), 0o644); err != nil {
		t.Fatal(err)
	}
	st, err := preset.Open(filepath.Join(dir, "presets"), preset.Options{PollInterval: -1, LiveResolvePath: "-"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	for _, id := range []string{preset.ResolveID, tChrome} {
		if _, ok := st.Snapshot().Preset(id); !ok {
			if _, err := st.AddApp(id, id, nil); err != nil {
				t.Fatal(err)
			}
		}
	}
	setup(st)

	eng := newEngineWithConfig(t, tDevMap, `{}`)
	sock, _ := startRecordingSidecar(t, "color")
	eng.sockPath = sock
	stubSidecarLaunch(t)
	cbp := stubMIDICapture(t)

	r := &adRig{eng: eng, store: st, events: make(chan InputEvent, 64), devMap: devMap}
	front := tChrome
	r.front = &front
	orig := frontmostBundleIDFn
	frontmostBundleIDFn = func() string { r.mu.Lock(); defer r.mu.Unlock(); return *r.front }
	t.Cleanup(func() { frontmostBundleIDFn = orig })

	ad := appdeck.New()
	ad.SendKey = func(s string) error { r.mu.Lock(); r.keys = append(r.keys, s); r.mu.Unlock(); return nil }
	ad.Scroll = func(d int, f uint64) { r.mu.Lock(); r.scroll = append(r.scroll, d); r.mu.Unlock() }
	// Every posting seam is replaced: a shell assignment in a test must never
	// run a real program (wave E, F16).
	ad.Shell = func(cmd string, args []string) { r.mu.Lock(); r.shells = append(r.shells, cmd); r.mu.Unlock() }
	r.ad = ad
	eng.SetAppDeck(ad)
	eng.SetInputObserver(func(ev InputEvent) { r.events <- ev })
	r.recompose(t)

	if err := eng.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { eng.Stop() })
	r.cb = *cbp
	// Let the first status tick run: it records the sidecar page, and the
	// page-change layer auto-reset would otherwise clobber an early layer press.
	time.Sleep(150 * time.Millisecond)
	return r
}

func (r *adRig) nextEvent(t *testing.T) InputEvent {
	t.Helper()
	select {
	case ev := <-r.events:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("no observer event")
		return InputEvent{}
	}
}

func (r *adRig) noEvent(t *testing.T) {
	t.Helper()
	select {
	case ev := <-r.events:
		t.Fatalf("unexpected event %+v", ev)
	case <-time.After(150 * time.Millisecond):
	}
}

func (r *adRig) snapKeys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.keys...)
}

func (r *adRig) snapScroll() []int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]int(nil), r.scroll...)
}

func mustAssign(t *testing.T, s *preset.Store, app, bank, control string, a map[string]any) {
	t.Helper()
	if err := s.Assign(app, bank, control, a); err != nil {
		t.Fatalf("assign: %v", err)
	}
}

func baseSetup(t *testing.T) func(*preset.Store) {
	return func(s *preset.Store) {
		mustAssign(t, s, preset.ResolveID, "col", "L1", map[string]any{"type": "layer", "layer": 2})
		mustAssign(t, s, tChrome, "base", "C9", map[string]any{"type": "key", "key": "Cmd+T"})
		mustAssign(t, s, tChrome, "base", "D1", map[string]any{"type": "scrollDelta", "linesPerDetent": 3, "detentThreshold": 1})
		if _, err := s.AddApp("com.apple.finder", "Finder", nil); err != nil {
			t.Fatal(err)
		}
		mustAssign(t, s, "com.apple.finder", "base", "P1btn", map[string]any{"type": "finderTag", "tag": "Red"})
		mustAssign(t, s, preset.GlobalID, "base", "C8", map[string]any{"type": "key", "key": "Cmd+Shift+4"})
	}
}

func adWaitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestAppdeckPress_ChromeFrontmost_RunsAppVerb_NeverResolve(t *testing.T) {
	r := newAdRig(t, nil2(t))
	// L1 is a Resolve layer button; Chrome does not assign it: nothing happens,
	// Resolve's layer switch must NOT run.
	r.cb(midi.NoteOn(0, 40, 100), 0)
	r.noEvent(t)
	if l := r.eng.Status().Layer; l != 1 {
		t.Fatalf("Resolve layer switch leaked through a foreign app: layer=%d", l)
	}
	// C9: Chrome key.
	r.cb(midi.NoteOn(0, 41, 100), 0)
	ev := r.nextEvent(t)
	if ev.Control != "C9" || ev.Kind != "press" || ev.PresetID != tChrome || ev.Source != "app" || ev.Out.Text != "Cmd+T" {
		t.Fatalf("event = %+v", ev)
	}
	if k := r.snapKeys(); len(k) != 1 || k[0] != "Cmd+T" {
		t.Fatalf("keys = %v", k)
	}
	if got := r.eng.Status().LastAction; got != "Cmd+T" {
		t.Fatalf("LastAction = %q", got)
	}
	// C8: Global key reaches Chrome.
	r.cb(midi.NoteOn(0, 42, 100), 0)
	ev = r.nextEvent(t)
	if ev.Source != "global" || ev.PresetID != preset.GlobalID {
		t.Fatalf("global event = %+v", ev)
	}
	// Release of an appdeck-handled press is observed as a release.
	r.cb(midi.NoteOff(0, 41), 0)
	if ev = r.nextEvent(t); ev.Kind != "release" || ev.Control != "C9" {
		t.Fatalf("release event = %+v", ev)
	}
}

func nil2(t *testing.T) func(*preset.Store) { return baseSetup(t) }

func TestAppdeckPress_ResolveFrontmost_LegacyThenGlobalFallthrough(t *testing.T) {
	r := newAdRig(t, nil2(t))
	r.setFront(preset.ResolveID)
	// Resolve's own binding wins and runs the legacy path.
	r.cb(midi.NoteOn(0, 40, 100), 0)
	adWaitFor(t, "layer 2", func() bool { return r.eng.Status().Layer == 2 })
	ev := r.nextEvent(t)
	if ev.Control != "L1" || ev.Kind != "press" || ev.PresetID != preset.ResolveID || ev.Action != "layer" || ev.Out.Text != "Layer 2" {
		t.Fatalf("resolve event = %+v", ev)
	}
	if len(r.snapKeys()) != 0 {
		t.Fatalf("appdeck must not run for a Resolve-bound control: %v", r.snapKeys())
	}
	// C8 has no Resolve binding: Global's key runs via the fall-through.
	r.cb(midi.NoteOn(0, 42, 100), 0)
	ev = r.nextEvent(t)
	if ev.Source != "global" || ev.Out.Text != "Cmd+Shift+4" {
		t.Fatalf("fall-through event = %+v", ev)
	}
	// C9 (Chrome-only) has no Resolve/Global binding: nothing.
	r.cb(midi.NoteOn(0, 41, 100), 0)
	r.noEvent(t)
	if k := r.snapKeys(); len(k) != 1 {
		t.Fatalf("keys = %v", k)
	}
}

func TestAppdeckTurn_ChromeScroll_ObserverAndLiveSnapshotSwap(t *testing.T) {
	r := newAdRig(t, nil2(t))
	r.cb(midi.ControlChange(0, 36, 3), 0) // +3 raw ticks
	ev := r.nextEvent(t)
	if ev.Control != "D1" || ev.Kind != "turn" || ev.RawTicks == 0 || ev.Out.Unit != "lines" || ev.Out.Value != float64(3*ev.RawTicks) || ev.Action != "generic.scrollDelta" {
		t.Fatalf("turn event = %+v", ev)
	}
	if ev.TicksPerSec <= 0 || ev.Accel != 1 {
		t.Fatalf("rate/accel = %v/%v", ev.TicksPerSec, ev.Accel)
	}
	sc := r.snapScroll()
	if len(sc) != 1 || sc[0] != 3*ev.RawTicks {
		t.Fatalf("scroll = %v, rawTicks=%d", sc, ev.RawTicks)
	}
	// Live preset edit (more lines per detent) applies without a restart.
	if err := r.store.SetValue(tChrome, "base", "D1", "linesPerDetent", 7); err != nil {
		t.Fatal(err)
	}
	r.recompose(t)
	r.cb(midi.ControlChange(0, 36, 1), 0)
	ev = r.nextEvent(t)
	sc = r.snapScroll()
	if len(sc) != 2 || sc[1] != 7*ev.RawTicks {
		t.Fatalf("after swap scroll = %v", sc)
	}
	// Resolve frontmost: the Chrome scroll must not run.
	r.setFront(preset.ResolveID)
	time.Sleep(60 * time.Millisecond) // past the 40 ms frontmost cache
	r.cb(midi.ControlChange(0, 36, 1), 0)
	time.Sleep(200 * time.Millisecond) // a few drain ticks
	if len(r.snapScroll()) != 2 {
		t.Fatalf("scroll ran with Resolve frontmost: %v", r.snapScroll())
	}
}

func TestInputObserver_NilSafeAndNeverBlocksOnSlowConsumer(t *testing.T) {
	r := newAdRig(t, nil2(t))
	r.eng.SetInputObserver(nil)
	r.cb(midi.NoteOn(0, 41, 100), 0) // must not panic with no observer
	block := make(chan struct{})
	defer close(block)
	r.eng.SetInputObserver(func(InputEvent) { <-block })
	start := time.Now()
	for i := 0; i < 3*inputObserverQueue; i++ {
		r.eng.emitInput(InputEvent{Control: fmt.Sprint("X", i)})
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("emitInput blocked on a slow observer: %v", d)
	}
}

func TestSetConfigSwapChangesBehaviourMidRun_WithAppdeckInstalled(t *testing.T) {
	r := newAdRig(t, nil2(t))
	r.cb(midi.NoteOn(0, 41, 100), 0)
	r.nextEvent(t)
	if err := r.store.SetValue(tChrome, "base", "C9", "key", "Cmd+W"); err != nil {
		t.Fatal(err)
	}
	r.recompose(t)
	time.Sleep(60 * time.Millisecond) // beyond the 50 ms press debounce
	r.cb(midi.NoteOn(0, 41, 100), 0)
	r.nextEvent(t)
	if k := r.snapKeys(); len(k) != 2 || k[1] != "Cmd+W" {
		t.Fatalf("keys = %v", k)
	}
}

func TestAppdeckPress_FinderTagButtonStaysWithFinderBranch(t *testing.T) {
	var mu sync.Mutex
	var applied []string
	origSel, origApply := finderSelectionFn, finderApplyTagFn
	finderSelectionFn = func() ([]string, error) { return []string{"/x/a.mov"}, nil }
	finderApplyTagFn = func(paths []string, colour string) (int, error) {
		mu.Lock()
		applied = append(applied, colour)
		mu.Unlock()
		return len(paths), nil
	}
	t.Cleanup(func() { finderSelectionFn, finderApplyTagFn = origSel, origApply })

	r := newAdRig(t, nil2(t))
	r.setFront("com.apple.finder")
	r.cb(midi.NoteOn(0, 43, 100), 0)
	adWaitFor(t, "finder tag", func() bool { mu.Lock(); defer mu.Unlock(); return len(applied) == 1 })
	if applied[0] != "Red" || len(r.snapKeys()) != 0 {
		t.Fatalf("applied=%v keys=%v", applied, r.snapKeys())
	}
	// Any other control with Finder frontmost goes to appdeck, never Resolve.
	r.cb(midi.NoteOn(0, 40, 100), 0)
	r.noEvent(t)
	if r.eng.Status().Layer != 1 {
		t.Fatal("Resolve layer switch reached from Finder")
	}
}
