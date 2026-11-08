package appdeck

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"2t5-deck-plus/internal/keys"
	"2t5-deck-plus/internal/mouse"
)

// B2: the executor the app wires at start (disarmed by the start policy) posts
// through NO seam until the user arms it, and the key and mouse packages are
// already paused before anything can run.
func TestStartDisarmed_NoInputSeamBeforeArming(t *testing.T) {
	x, r, s := setup(t)
	restoreArmed(t, x)
	assign(t, s, chrome, "base", "C1", map[string]any{"type": "key", "key": "Cmd+T"})
	assign(t, s, chrome, "base", "C2", map[string]any{"type": "shell", "cmd": "/usr/bin/true"})
	assign(t, s, chrome, "base", "D1", map[string]any{"type": "scrollDelta", "linesPerDetent": 3, "detentThreshold": 1})
	x.swap(s)

	x.SetArmed(false) // what editorapi.ApplyStartPolicy does, before the engine starts
	if !keys.Paused() || !mouse.Paused() {
		t.Fatalf("key/mouse packages not paused at start: keys=%v mouse=%v", keys.Paused(), mouse.Paused())
	}
	x.Handle(Input{Bundle: chrome, Layer: 1, Control: "C1", Kind: KindPress})
	x.Handle(Input{Bundle: chrome, Layer: 1, Control: "C2", Kind: KindPress})
	x.Handle(Input{Bundle: chrome, Layer: 1, Control: "D1", Kind: KindTurn, Step: 3})
	if len(r.keys) != 0 || len(r.shells) != 0 || len(r.scrolls) != 0 {
		t.Fatalf("a seam was called before arming: keys=%v shells=%v scrolls=%v", r.keys, r.shells, r.scrolls)
	}
	x.SetArmed(true) // the deliberate arming
	if keys.Paused() || mouse.Paused() {
		t.Error("still paused after arming")
	}
	x.Handle(Input{Bundle: chrome, Layer: 1, Control: "C1", Kind: KindPress})
	if len(r.keys) != 1 {
		t.Errorf("armed press did not post: %v", r.keys)
	}
}

// ---- H4: hold to fire ----------------------------------------------------------------

type holdTimer struct {
	d         time.Duration
	f         func()
	cancelled bool
}

type holdClock struct {
	mu sync.Mutex
	ts []*holdTimer
}

func (c *holdClock) After(d time.Duration, f func()) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &holdTimer{d: d, f: f}
	c.ts = append(c.ts, t)
	return func() { c.mu.Lock(); t.cancelled = true; c.mu.Unlock() }
}
func (c *holdClock) last() *holdTimer { c.mu.Lock(); defer c.mu.Unlock(); return c.ts[len(c.ts)-1] }

func holdRig(t *testing.T, assignment map[string]any) (*Executor, *rec, *holdClock, *[]Result) {
	x, r, s := setup(t)
	restoreArmed(t, x)
	assign(t, s, chrome, "base", "C1", assignment)
	x.swap(s)
	c := &holdClock{}
	x.After = c.After
	var fired []Result
	var mu sync.Mutex
	x.OnHold = func(in Input, res Result) { mu.Lock(); fired = append(fired, res); mu.Unlock() }
	return x, r, c, &fired
}

func press(x *Executor) Result {
	return x.Handle(Input{Bundle: chrome, Layer: 1, Control: "C1", Kind: KindPress})
}
func release(x *Executor) Result {
	return x.Handle(Input{Bundle: chrome, Layer: 1, Control: "C1", Kind: KindRelease})
}

func TestHold_KeyFiresAtTheThresholdNotAtPress(t *testing.T) {
	x, r, c, fired := holdRig(t, map[string]any{"type": "key", "key": "Cmd+W", "holdMs": 400})
	res := press(x)
	if !res.Handled || res.Fired || len(r.keys) != 0 {
		t.Fatalf("press: %+v keys=%v (must wait)", res, r.keys)
	}
	if !strings.Contains(res.Text, "400") {
		t.Errorf("press text = %q", res.Text)
	}
	if c.last().d != 400*time.Millisecond {
		t.Errorf("timer = %v", c.last().d)
	}
	c.last().f() // the threshold passes with the control still down
	if len(r.keys) != 1 || r.keys[0] != "Cmd+W" {
		t.Fatalf("keys = %v", r.keys)
	}
	if len(*fired) != 1 || !(*fired)[0].Fired || (*fired)[0].Text != "Cmd+W" || (*fired)[0].PresetID != chrome {
		t.Errorf("OnHold = %+v", *fired)
	}
	// the release after a fire is an ordinary release: nothing more
	if res := release(x); res.Fired || strings.Contains(res.Text, "hold to fire") {
		t.Errorf("release after fire = %+v", res)
	}
	c.last().f() // a stale second run does nothing
	if len(r.keys) != 1 {
		t.Errorf("fired twice: %v", r.keys)
	}
}

func TestHold_ATapDoesNothingAndSaysSo(t *testing.T) {
	x, r, c, fired := holdRig(t, map[string]any{"type": "key", "key": "Cmd+W", "holdMs": 400})
	press(x)
	tm := c.last()
	res := release(x)
	if !tm.cancelled {
		t.Error("the timer was not cancelled on release")
	}
	if !res.Handled || res.Fired || res.Text != "hold to fire (400 ms)" {
		t.Errorf("tap release = %+v", res)
	}
	tm.f() // a timer that raced the cancel must not fire
	if len(r.keys) != 0 || len(*fired) != 0 {
		t.Errorf("a tap fired: keys=%v hold=%v", r.keys, *fired)
	}
}

func TestHold_ShellHonoursItToo(t *testing.T) {
	x, r, c, _ := holdRig(t, map[string]any{"type": "shell", "cmd": "/usr/bin/true", "holdMs": 250})
	press(x)
	if len(r.shells) != 0 {
		t.Fatal("shell ran at press")
	}
	c.last().f()
	if len(r.shells) != 1 {
		t.Errorf("shells = %v", r.shells)
	}
}

func TestHold_ClampedAndOffByDefault(t *testing.T) {
	// values the editor refuses can still arrive in a hand-edited file: they never delay a press by more than 2 s
	for raw, want := range map[any]int{9000: 2000, 2000: 2000, 1999.0: 1999, 250: 250, 0: 0, -5: 0, 0.5: 0, "x": 0, nil: 0} {
		if got := holdOf(map[string]any{"holdMs": raw}); got != want {
			t.Errorf("holdOf(%v) = %d, want %d", raw, got, want)
		}
	}
	if holdOf(nil) != 0 || holdOf(map[string]any{}) != 0 {
		t.Error("absent holdMs is not 0")
	}
	for _, v := range []any{0, 0.0} {
		x2, r2, c2, _ := holdRig(t, map[string]any{"type": "key", "key": "Cmd+T", "holdMs": v})
		res := press(x2)
		if !res.Fired || len(r2.keys) != 1 || len(c2.ts) != 0 {
			t.Errorf("holdMs=%v delayed the press: %+v", v, res)
		}
	}
}

func TestHold_NotForTurnsLayersOrModifiers(t *testing.T) {
	for _, a := range []map[string]any{
		{"type": "layer", "layer": 2, "holdMs": 500},
		{"type": "modifierHold", "modifier": "shift", "holdMs": 500},
		{"type": "toggleHud", "holdMs": 500},
	} {
		x, _, c, _ := holdRig(t, a)
		res := press(x)
		if !res.Fired || res.Effect == nil || len(c.ts) != 0 {
			t.Errorf("%v: %+v timers=%d (these carry engine effects: immediate)", a["type"], res, len(c.ts))
		}
	}
}

func TestHold_HotReloadAndResetCancel(t *testing.T) {
	x, r, c, _ := holdRig(t, map[string]any{"type": "key", "key": "Cmd+W", "holdMs": 400})
	press(x)
	tm := c.last()
	x.SetSnapshot(x.snap.Load()) // a hot reload while the control is held
	if !tm.cancelled {
		t.Error("SetSnapshot left the hold timer running")
	}
	tm.f()
	if len(r.keys) != 0 {
		t.Errorf("a hold fired after a hot reload: %v", r.keys)
	}
	press(x)
	tm2 := c.last()
	x.CancelHolds()
	if !tm2.cancelled {
		t.Error("CancelHolds left the timer")
	}
	if n := x.holdCount(); n != 0 {
		t.Errorf("%d holds pending", n)
	}
}

func TestHold_SecondPressReplacesThePending(t *testing.T) {
	x, r, c, _ := holdRig(t, map[string]any{"type": "key", "key": "Cmd+W", "holdMs": 400})
	press(x)
	first := c.last()
	press(x)
	if !first.cancelled {
		t.Error("the first pending hold was not cancelled")
	}
	first.f()
	c.last().f()
	if len(r.keys) != 1 {
		t.Errorf("keys = %v", r.keys)
	}
}

func TestHold_DisarmedFiresNothingButSaysSo(t *testing.T) {
	x, r, c, fired := holdRig(t, map[string]any{"type": "key", "key": "Cmd+W", "holdMs": 400})
	x.SetArmed(false)
	press(x)
	c.last().f()
	if len(r.keys) != 0 {
		t.Errorf("disarmed hold posted %v", r.keys)
	}
	if len(*fired) != 1 || (*fired)[0].Text != "disarmed: Cmd+W" {
		t.Errorf("OnHold = %+v", *fired)
	}
}

func TestHold_WithoutHoldMsEverythingIsAsBefore(t *testing.T) {
	x, r, c, _ := holdRig(t, map[string]any{"type": "key", "key": "Cmd+T"})
	res := press(x)
	if !res.Handled || !res.Fired || res.Text != "Cmd+T" || len(r.keys) != 1 || len(c.ts) != 0 {
		t.Errorf("press = %+v keys=%v timers=%d", res, r.keys, len(c.ts))
	}
	if res := release(x); res.Handled {
		t.Errorf("release = %+v (a key release was never handled)", res)
	}
}

// ---- H5: panic disarm ------------------------------------------------------------------

func TestDisarmAction_FullDisarmFromAnyApp(t *testing.T) {
	for _, bundle := range []string{chrome, "com.blackmagic-design.DaVinciResolve", "com.unknown.App", "com.2t5.deck-plus"} {
		x, r, s := setup(t)
		restoreArmed(t, x)
		assign(t, s, "_global", "base", "C9", map[string]any{"type": "disarm"})
		assign(t, s, "_global", "base", "C8", map[string]any{"type": "key", "key": "Cmd+T"})
		x.swap(s)
		var changes []string
		x.OnArmChange = func(armed bool, src string) { changes = append(changes, fmt.Sprintf("%v/%s", armed, src)) }
		if !x.IsDisarmPress(bundle, "", 1, "C9") || x.IsDisarmPress(bundle, "", 1, "C8") {
			t.Fatalf("%s: IsDisarmPress wrong", bundle)
		}
		if !x.Armed() {
			t.Fatal("must start armed")
		}
		res := x.Handle(Input{Bundle: bundle, Layer: 1, Control: "C9", Kind: KindPress})
		if !res.Handled || !res.Fired || res.Effect == nil || res.Effect.Kind != "disarm" || res.Text != "deck disarmed" {
			t.Errorf("%s: %+v", bundle, res)
		}
		if x.Armed() || !keys.Paused() || !mouse.Paused() {
			t.Errorf("%s: not fully disarmed: armed=%v keys=%v mouse=%v", bundle, x.Armed(), keys.Paused(), mouse.Paused())
		}
		x.Handle(Input{Bundle: bundle, Layer: 1, Control: "C8", Kind: KindPress})
		if len(r.keys) != 0 {
			t.Errorf("%s: a key fired after the panic key: %v", bundle, r.keys)
		}
		if len(changes) != 1 || changes[0] != "false/deck-key C9" {
			t.Errorf("%s: OnArmChange = %v", bundle, changes)
		}
		// pressing again changes nothing and reports nothing
		res = x.Handle(Input{Bundle: bundle, Layer: 1, Control: "C9", Kind: KindPress})
		if res.Text != "deck already disarmed" || len(changes) != 1 {
			t.Errorf("%s: second press: %+v changes=%v", bundle, res, changes)
		}
		// release is not handled
		if res := x.Handle(Input{Bundle: bundle, Layer: 1, Control: "C9", Kind: KindRelease}); res.Handled {
			t.Errorf("%s: release handled", bundle)
		}
	}
}

func TestSetArmedFrom_ReportsRealChangesOnly(t *testing.T) {
	x := New()
	restoreArmed(t, x)
	var n int
	x.OnArmChange = func(bool, string) { n++ }
	x.SetArmedFrom(true, "x") // already armed
	x.SetArmedFrom(false, "x")
	x.SetArmedFrom(false, "x")
	x.SetArmedFrom(true, "x")
	if n != 2 {
		t.Errorf("changes reported = %d, want 2", n)
	}
}

// ---- H7: shell outcome reporting (program name and status only) ---------------------------

func TestShellOutcome_ReportsExitAndNeverArguments(t *testing.T) {
	orig := execFn
	t.Cleanup(func() { execFn = orig })
	type got struct {
		cmd  string
		exit int
		note string
	}
	run := func(err error) got {
		execFn = func(ctx context.Context, cmd string, args []string) error { return err }
		ch := make(chan got, 1)
		runShellWith("/usr/bin/open", []string{"-a", "SECRET"}, func(cmd string, exit int, note string) { ch <- got{cmd, exit, note} })
		select {
		case g := <-ch:
			return g
		case <-time.After(2 * time.Second):
			t.Fatal("no report")
			return got{}
		}
	}
	if g := run(nil); g.exit != 0 || g.note != "" || g.cmd != "/usr/bin/open" {
		t.Errorf("ok = %+v", g)
	}
	if g := run(exec.Command("/usr/bin/false").Run()); g.exit != 1 {
		t.Errorf("exit 1 = %+v", g)
	}
	if g := run(errors.New("fork/exec /nope -a SECRET: no such file")); g.exit != -1 || g.note != "start failed" || strings.Contains(g.note, "SECRET") {
		t.Errorf("start failure = %+v", g)
	}
}

// ---- H9: latency -------------------------------------------------------------------------

func TestLatency_MeasuredFromHandleEntryToThePost(t *testing.T) {
	x, r, s := setup(t)
	restoreArmed(t, x)
	assign(t, s, chrome, "base", "C1", map[string]any{"type": "key", "key": "Cmd+T"})
	assign(t, s, chrome, "base", "D1", map[string]any{"type": "scrollDelta", "linesPerDetent": 1, "detentThreshold": 1})
	assign(t, s, chrome, "base", "C3", map[string]any{"type": "layer", "layer": 2})
	x.swap(s)
	c := newClock()
	x.Now = c.Now
	x.SendKey = func(spec string) error {
		c.advance(3 * time.Millisecond)
		r.mu.Lock()
		r.keys = append(r.keys, spec)
		r.mu.Unlock()
		return nil
	}
	x.Scroll = func(d int, f uint64) { c.advance(1500 * time.Microsecond) }
	res := x.Handle(Input{Bundle: chrome, Layer: 1, Control: "C1", Kind: KindPress})
	if res.LatencyMs != 3 {
		t.Errorf("key latency = %v, want 3", res.LatencyMs)
	}
	res = x.Handle(Input{Bundle: chrome, Layer: 1, Control: "D1", Kind: KindTurn, Step: 2})
	if res.LatencyMs != 1.5*2 && res.LatencyMs != 1.5 { // one post per call
		t.Errorf("scroll latency = %v", res.LatencyMs)
	}
	if res := x.Handle(Input{Bundle: chrome, Layer: 1, Control: "C1", Kind: KindRelease}); res.LatencyMs != 0 {
		t.Errorf("release latency = %v", res.LatencyMs)
	}
	// nothing posted (a turn below the detent): no latency
	assign(t, s, chrome, "base", "D2", map[string]any{"type": "keyDelta", "keyCW": "Right", "keyCCW": "Left", "detentThreshold": 8})
	x.swap(s)
	if res := x.Handle(Input{Bundle: chrome, Layer: 1, Control: "D2", Kind: KindTurn, Step: 1}); res.Fired || res.LatencyMs != 0 {
		t.Errorf("sub-detent turn: %+v", res)
	}
	// a clock that does not advance: unmeasurable, reported as 0 (omitted), never negative
	x.Now = func() time.Time { return c.now }
	x.SendKey = func(string) error { return nil }
	if res := x.Handle(Input{Bundle: chrome, Layer: 1, Control: "C1", Kind: KindPress}); res.LatencyMs != 0 {
		t.Errorf("zero-duration latency = %v", res.LatencyMs)
	}
}

// ---- wave K, finding 2 and 6/7: holds belong to their app and arm state --------------------

func TestHold_DoesNotFireAfterTheFrontmostAppChanged(t *testing.T) {
	x, r, c, fired := holdRig(t, map[string]any{"type": "key", "key": "Cmd+W", "holdMs": 400})
	front := chrome
	var asked []string
	x.HoldGuard = func(bundle string) bool { asked = append(asked, bundle); return front == bundle }
	press(x)
	front = "com.apple.finder" // Cmd+Tab during the hold
	c.last().f()
	if len(r.keys) != 0 || len(*fired) != 0 {
		t.Errorf("the hold fired into the new app: keys=%v hold=%v", r.keys, *fired)
	}
	if len(asked) != 1 || asked[0] != chrome {
		t.Errorf("guard asked about %v, want the press bundle", asked)
	}
	// still frontmost: fires
	front = chrome
	press(x)
	c.last().f()
	if len(r.keys) != 1 {
		t.Errorf("keys = %v", r.keys)
	}
}

func TestHold_ArmSwitchChangesCancelPendingHolds(t *testing.T) {
	x, r, c, _ := holdRig(t, map[string]any{"type": "key", "key": "Cmd+W", "holdMs": 400})
	press(x)
	tm := c.last()
	x.SetArmed(false)
	if !tm.cancelled || x.holdCount() != 0 {
		t.Error("disarming left a pending hold")
	}
	x.SetArmed(true)
	tm.f() // a timer that raced the cancel, after arm -> disarm -> arm
	if len(r.keys) != 0 {
		t.Errorf("an old hold fired after a quick arm toggle: %v", r.keys)
	}
	// even without the cancel (a timer already running): the epoch refuses it
	press(x)
	tm2 := c.last()
	x.hmu.Lock()
	x.holds["C1"].epoch-- // as if the press began before the last toggle
	x.hmu.Unlock()
	tm2.f()
	if len(r.keys) != 0 {
		t.Errorf("a hold from before the toggle fired: %v", r.keys)
	}
	if x.ArmEpoch() < 2 {
		t.Errorf("epoch = %d", x.ArmEpoch())
	}
}

func TestSetArmed_SerialisedAndConsistent(t *testing.T) {
	x := New()
	restoreArmed(t, x)
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 300; i++ {
				x.SetArmed((g+i)%2 == 0)
			}
		}(g)
	}
	wg.Wait()
	x.SetArmed(false)
	if x.Armed() || !keys.Paused() || !mouse.Paused() {
		t.Error("after the last SetArmed(false) the three states disagree")
	}
	x.SetArmed(true)
	if !x.Armed() || keys.Paused() || mouse.Paused() {
		t.Error("after the last SetArmed(true) the three states disagree")
	}
}

func TestLatency_ZeroWhenDisarmed(t *testing.T) {
	x, r, s := setup(t)
	restoreArmed(t, x)
	assign(t, s, chrome, "base", "C1", map[string]any{"type": "key", "key": "Cmd+T"})
	x.swap(s)
	c := newClock()
	x.Now = c.Now
	x.SendKey = func(spec string) error {
		c.advance(3 * time.Millisecond)
		r.mu.Lock()
		r.keys = append(r.keys, spec)
		r.mu.Unlock()
		return nil
	}
	x.SetArmed(false)
	res := x.Handle(Input{Bundle: chrome, Layer: 1, Control: "C1", Kind: KindPress})
	if res.LatencyMs != 0 || !strings.HasPrefix(res.Text, "disarmed: ") {
		t.Errorf("disarmed press: %+v", res)
	}
}

func TestIsPanicControl_AnyPresetAnyBankAndFollowsHotReload(t *testing.T) {
	x, _, s := setup(t)
	if x.IsPanicControl("C9") {
		t.Fatal("nothing assigned yet")
	}
	assign(t, s, chrome, "shift", "C9", map[string]any{"type": "disarm"})
	assign(t, s, "_global", "base", "C8", map[string]any{"type": "key", "key": "Cmd+T"})
	x.swap(s)
	if !x.IsPanicControl("C9") || x.IsPanicControl("C8") || x.IsPanicControl("") {
		t.Error("index wrong")
	}
	s.Unassign(chrome, "shift", "C9")
	x.swap(s)
	if x.IsPanicControl("C9") {
		t.Error("index not rebuilt on swap")
	}
}

func TestSetArmedFrom_CompareAndSetReportsInStateOrder(t *testing.T) {
	x := New()
	restoreArmed(t, x)
	var mu sync.Mutex
	var seq []bool
	x.OnArmChange = func(armed bool, _ string) { mu.Lock(); seq = append(seq, armed); mu.Unlock() }
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				x.SetArmedFrom((g+i)%2 == 0, "t")
			}
		}(g)
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	// every reported change alternates: a true change is never reported twice in a row,
	// and the last report matches the final state
	for i := 1; i < len(seq); i++ {
		if seq[i] == seq[i-1] {
			t.Fatalf("two identical changes in a row at %d: %v", i, seq[i-1:i+1])
		}
	}
	if len(seq) > 0 && seq[len(seq)-1] != x.Armed() {
		t.Errorf("last reported %v, state %v", seq[len(seq)-1], x.Armed())
	}
}

func TestSetArmedFrom_OnArmRequestSeesEveryCallIncludingNoChange(t *testing.T) {
	x := New()
	restoreArmed(t, x)
	var reqs, changes []string
	x.OnArmRequest = func(a bool, src string) { reqs = append(reqs, fmt.Sprintf("%v/%s", a, src)) }
	x.OnArmChange = func(a bool, src string) { changes = append(changes, fmt.Sprintf("%v/%s", a, src)) }
	x.SetArmedFrom(false, "a")
	x.SetArmedFrom(false, "b") // already disarmed: an explicit request all the same
	x.SetArmedFrom(true, "c")
	if len(reqs) != 3 || len(changes) != 2 || reqs[1] != "false/b" {
		t.Errorf("requests = %v changes = %v", reqs, changes)
	}
}
