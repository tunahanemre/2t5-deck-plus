package appdeck

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"sync"
	"testing"
	"time"

	"2t5-deck-plus/internal/accel"
)

type clock struct {
	mu   sync.Mutex
	now  time.Time
	jobs []func()
	n    int // After calls
}

func newClock() *clock { return &clock{now: time.Unix(1_000_000, 0)} }

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
func (c *clock) After(d time.Duration, f func()) func() {
	c.mu.Lock()
	c.n++
	idx := len(c.jobs)
	c.jobs = append(c.jobs, f)
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		if idx < len(c.jobs) {
			c.jobs[idx] = nil
		}
		c.mu.Unlock()
	}
}

// run advances the clock by step and runs every pending timer once, repeating
// until steps are used or nothing is pending. Returns the number of timer runs.
func (c *clock) run(step time.Duration, maxSteps int) int {
	runs := 0
	for i := 0; i < maxSteps; i++ {
		c.mu.Lock()
		jobs := c.jobs
		c.jobs = nil
		c.mu.Unlock()
		pending := false
		for _, j := range jobs {
			if j != nil {
				pending = true
			}
		}
		if !pending {
			return runs
		}
		c.advance(step)
		for _, j := range jobs {
			if j != nil {
				j()
				runs++
			}
		}
	}
	return runs
}

func feelSetup(t *testing.T, a map[string]any) (*Executor, *rec, *clock) {
	t.Helper()
	x, r, s := setup(t)
	assign(t, s, chrome, "base", "D1", a)
	x.swap(s)
	c := newClock()
	x.Now, x.After = c.Now, c.After
	return x, r, c
}

func turn(x *Executor, c *clock, step int) Result {
	return x.Handle(Input{Bundle: chrome, Layer: 1, Control: "D1", Kind: KindTurn, Step: step, Now: c.Now()})
}

func totalLines(r *rec) int {
	n := 0
	for _, s := range r.scrolls {
		var d, f int
		if _, err := fmt.Sscanf(s, "%d/%d", &d, &f); err == nil {
			n += d
		}
	}
	return n
}

func TestAbsentAndLinearAreByteIdentical(t *testing.T) {
	base := map[string]any{"type": "scrollDelta", "linesPerDetent": 2, "detentThreshold": 3}
	variants := []map[string]any{
		base,
		{"type": "scrollDelta", "linesPerDetent": 2, "detentThreshold": 3, "velocityProfile": "linear"},
		{"type": "scrollDelta", "linesPerDetent": 2, "detentThreshold": 3, "velocityProfile": "linear", "momentumMs": 0},
	}
	rng := rand.New(rand.NewSource(7))
	steps := make([]int, 300)
	for i := range steps {
		steps[i] = rng.Intn(41) - 20
	}
	var refRes []Result
	var refScroll []string
	for vi, v := range variants {
		x, r, c := feelSetup(t, v)
		x.TailGuard = func(string) bool { return true }
		var res []Result
		for _, st := range steps {
			res = append(res, turn(x, c, st))
			c.advance(13 * time.Millisecond)
		}
		if c.n != 0 {
			t.Fatalf("variant %d scheduled %d timers; the old path must not", vi, c.n)
		}
		for _, q := range res {
			if q.Accel != 0 {
				t.Fatalf("variant %d: Accel must stay 0, got %v", vi, q.Accel)
			}
		}
		if vi == 0 {
			refRes, refScroll = res, r.scrolls
			if len(refScroll) == 0 {
				t.Fatal("reference fired nothing")
			}
			continue
		}
		if !reflect.DeepEqual(res, refRes) || !reflect.DeepEqual(r.scrolls, refScroll) {
			t.Fatalf("variant %d differs from the absent-field behaviour", vi)
		}
	}
}

func TestProfileScalesByRateWithCarry(t *testing.T) {
	// fast, one tick every 500 ms = 4 ticks/s -> multiplier 1 + 0.5*(4/6).
	x, r, c := feelSetup(t, map[string]any{"type": "scrollDelta", "linesPerDetent": 1, "detentThreshold": 1, "velocityProfile": "fast"})
	p, _ := accel.ProfileByName("fast")
	m := p.Multiplier(4)
	var last Result
	for i := 0; i < 6; i++ {
		last = turn(x, c, 1)
		c.advance(500 * time.Millisecond)
	}
	if last.Accel < m-1e-9 || last.Accel > m+1e-9 {
		t.Fatalf("Accel=%v want %v", last.Accel, m)
	}
	// 6 ticks * 1.3333 = 8 lines exactly (carry loses nothing)
	if got := totalLines(r); got != 8 {
		t.Fatalf("lines=%d want 8 (%v)", got, r.scrolls)
	}
}

func TestFastSpinMultiplies(t *testing.T) {
	x, r, c := feelSetup(t, map[string]any{"type": "scrollDelta", "linesPerDetent": 1, "detentThreshold": 1, "velocityProfile": "fast"})
	for i := 0; i < 10; i++ {
		turn(x, c, 2) // 20 ticks over ~100 ms: well above 50/s
		c.advance(10 * time.Millisecond)
	}
	if got := totalLines(r); got < 60 {
		t.Fatalf("fast spin should multiply well beyond 20, got %d", got)
	}
	// reverse direction scrolls negative
	before := totalLines(r)
	c.advance(time.Second)
	turn(x, c, -3)
	if totalLines(r) >= before {
		t.Fatal("ccw must reduce")
	}
}

func TestPreciseSlowIsFiner(t *testing.T) {
	x, r, c := feelSetup(t, map[string]any{"type": "scrollDelta", "linesPerDetent": 1, "detentThreshold": 1, "velocityProfile": "precise"})
	for i := 0; i < 10; i++ {
		turn(x, c, 1)
		c.advance(time.Second) // 4 ticks/s ... actually 1 tick per window: rate 4 -> ~0.6
	}
	if got := totalLines(r); got >= 10 || got < 4 {
		t.Fatalf("precise slow turn should be finer than 1:1, got %d", got)
	}
}

func TestKeyDeltaFeel(t *testing.T) {
	x, r, c := feelSetup(t, map[string]any{"type": "keyDelta", "keyCW": "Right", "keyCCW": "Left", "detentThreshold": 1, "velocityProfile": "fast"})
	for i := 0; i < 10; i++ {
		turn(x, c, 2)
		c.advance(10 * time.Millisecond)
	}
	if len(r.keys) < 40 {
		t.Fatalf("fast keyDelta should send many more than 20 keys, got %d", len(r.keys))
	}
	for _, k := range r.keys {
		if k != "Right" {
			t.Fatalf("unexpected key %q", k)
		}
	}
}

func TestMomentumNeedsTailGuard(t *testing.T) {
	x, r, c := feelSetup(t, map[string]any{"type": "scrollDelta", "detentThreshold": 1, "momentumMs": 300})
	for i := 0; i < 5; i++ {
		turn(x, c, 3)
		c.advance(10 * time.Millisecond)
	}
	if c.n != 0 {
		t.Fatal("no TailGuard: no tail may be scheduled")
	}
	n := len(r.scrolls)
	c.run(16*time.Millisecond, 50)
	if len(r.scrolls) != n {
		t.Fatal("nothing may fire without a guard")
	}
}

func TestMomentumTailDecaysAndStops(t *testing.T) {
	x, r, c := feelSetup(t, map[string]any{"type": "scrollDelta", "linesPerDetent": 1, "detentThreshold": 1, "momentumMs": 300})
	x.TailGuard = func(b string) bool { return b == chrome }
	var tails []Result
	x.OnTail = func(in Input, res Result) { tails = append(tails, res) }
	for i := 0; i < 8; i++ {
		turn(x, c, 3) // 3 ticks per 20 ms
		c.advance(20 * time.Millisecond)
	}
	live := totalLines(r)
	if c.n == 0 {
		t.Fatal("tail timer should be armed")
	}
	runs := c.run(16*time.Millisecond, 400)
	if runs == 0 {
		t.Fatal("tail never ran")
	}
	tail := totalLines(r) - live
	if tail <= 0 {
		t.Fatalf("tail produced nothing")
	}
	// bounded by v*tau: v <= 3 ticks/20ms-ish ~ 150/s ; tau 0.3 s
	if tail > 60 {
		t.Fatalf("tail too long: %d", tail)
	}
	if len(tails) == 0 || tails[0].Action == "" && tails[0].PresetID == "" {
		t.Fatalf("OnTail results missing provenance: %+v", tails)
	}
	// finished: no pending timers, nothing more fires
	n := len(r.scrolls)
	c.advance(5 * time.Second)
	if c.run(16*time.Millisecond, 10) != 0 || len(r.scrolls) != n {
		t.Fatal("tail must be finished")
	}
}

func TestNewTurnAndResetCancelTail(t *testing.T) {
	x, r, c := feelSetup(t, map[string]any{"type": "scrollDelta", "linesPerDetent": 1, "detentThreshold": 1, "momentumMs": 500})
	x.TailGuard = func(string) bool { return true }
	for i := 0; i < 6; i++ {
		turn(x, c, 4)
		c.advance(15 * time.Millisecond)
	}
	// let the tail start
	c.run(16*time.Millisecond, 12)
	// an app/snapshot swap kills it
	x.ResetAccumulators()
	n := len(r.scrolls)
	c.run(16*time.Millisecond, 100)
	if len(r.scrolls) != n {
		t.Fatalf("tail fired after ResetAccumulators: %d -> %d", n, len(r.scrolls))
	}
}

func TestSnapshotSwapKillsTailAndGuardStopsIt(t *testing.T) {
	x, r, c := feelSetup(t, map[string]any{"type": "scrollDelta", "linesPerDetent": 1, "detentThreshold": 1, "momentumMs": 500})
	allowed := true
	x.TailGuard = func(string) bool { return allowed }
	for i := 0; i < 6; i++ {
		turn(x, c, 4)
		c.advance(15 * time.Millisecond)
	}
	c.run(16*time.Millisecond, 12)
	allowed = false // user left the app
	n := len(r.scrolls)
	c.run(16*time.Millisecond, 100)
	if len(r.scrolls) != n {
		t.Fatal("tail kept firing after the guard said stop")
	}
	// snapshot swap
	allowed = true
	for i := 0; i < 6; i++ {
		turn(x, c, 4)
		c.advance(15 * time.Millisecond)
	}
	c.run(16*time.Millisecond, 12)
	snap := x.snap.Load()
	x.SetSnapshot(snap)
	n = len(r.scrolls)
	c.run(16*time.Millisecond, 100)
	if len(r.scrolls) != n {
		t.Fatal("tail kept firing after SetSnapshot")
	}
}

func TestTurnDuringTailResumesCleanly(t *testing.T) {
	x, r, c := feelSetup(t, map[string]any{"type": "scrollDelta", "linesPerDetent": 1, "detentThreshold": 1, "momentumMs": 400})
	x.TailGuard = func(string) bool { return true }
	for i := 0; i < 6; i++ {
		turn(x, c, 4)
		c.advance(15 * time.Millisecond)
	}
	c.run(16*time.Millisecond, 10) // tail running
	before := totalLines(r)
	turn(x, c, -5) // opposite direction: new gesture
	if totalLines(r) >= before {
		t.Fatal("opposite turn must move the other way at once")
	}
	c.run(16*time.Millisecond, 400)
	// the new tail is negative: total must end below the pre-reverse total
	if totalLines(r) >= before {
		t.Fatalf("tail of the reversed turn should go negative: %d >= %d", totalLines(r), before)
	}
}

func TestFeelRace(t *testing.T) {
	x, _, _ := feelSetup(t, map[string]any{"type": "scrollDelta", "detentThreshold": 1, "velocityProfile": "standard", "momentumMs": 200})
	x.Now, x.After = nil, nil // real clock and timers
	x.TailGuard = func(string) bool { return true }
	var wg sync.WaitGroup
	for g := 0; g < 3; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				x.Handle(Input{Bundle: chrome, Layer: 1, Control: "D1", Kind: KindTurn, Step: 2})
				time.Sleep(2 * time.Millisecond)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			x.SetSnapshot(x.snap.Load())
			time.Sleep(10 * time.Millisecond)
		}
	}()
	wg.Wait()
	time.Sleep(400 * time.Millisecond)
	x.ResetAccumulators()
}

func TestFeelOfParsing(t *testing.T) {
	cases := []struct {
		raw map[string]any
		p   string
		ms  int
	}{
		{nil, "linear", 0},
		{map[string]any{}, "linear", 0},
		{map[string]any{"velocityProfile": "fast", "momentumMs": 250.0}, "fast", 250},
		{map[string]any{"velocityProfile": "x", "momentumMs": 99999.0}, "linear", 2000},
		{map[string]any{"velocityProfile": 4, "momentumMs": -1.0}, "linear", 0},
	}
	for _, c := range cases {
		p, ms := feelOf(c.raw)
		if p != c.p || ms != c.ms {
			t.Errorf("%v -> %s,%d want %s,%d", c.raw, p, ms, c.p, c.ms)
		}
	}
}

// The curve endpoint must agree with what the executor really does.
func TestCurveMatchesExecutor(t *testing.T) {
	raw := map[string]any{"type": "scrollDelta", "linesPerDetent": 3.0, "detentThreshold": 2.0, "velocityProfile": "standard"}
	info, err := Curve("scrollDelta", raw, 120)
	if err != nil {
		t.Fatal(err)
	}
	if info.Unit != "lines" || len(info.Points) != CurveSamples+1 || info.Staged {
		t.Fatalf("%+v", info)
	}
	p, _ := accel.ProfileByName("standard")
	for _, pt := range info.Points {
		if m := p.Multiplier(pt.TicksPerSec); pt.TicksPerSec > 0 && (abs(pt.Multiplier-m) > 1e-9 || abs(pt.OutputPerSec-pt.TicksPerSec*m*1.5) > 1e-9) {
			t.Fatalf("point %+v vs multiplier %v", pt, m)
		}
	}
	// Run the executor at a steady 40 ticks/s and compare lines per second.
	x, r, c := feelSetup(t, map[string]any{"type": "scrollDelta", "linesPerDetent": 3, "detentThreshold": 2, "velocityProfile": "standard"})
	// 2 ticks every 51 ms (about 39 ticks/s; the 250 ms window then holds 10
	// ticks = 40/s, the 50 ms spacing would sit exactly on the window edge).
	for i := 0; i < 200; i++ {
		turn(x, c, 2)
		c.advance(51 * time.Millisecond)
	}
	got := float64(totalLines(r)) / (200 * 0.051)
	want := (func() float64 { i2, _ := Curve("scrollDelta", raw, 40); return i2.Points[CurveSamples].OutputPerSec })()
	if abs(got-want)/want > 0.05 {
		t.Fatalf("executor %.2f lines/s vs curve %.2f", got, want)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestCurveLinearAndStaged(t *testing.T) {
	info, _ := Curve("keyDelta", map[string]any{"detentThreshold": 4.0}, 80)
	for _, pt := range info.Points {
		if pt.Multiplier != 1 || abs(pt.OutputPerSec-pt.TicksPerSec/4) > 1e-9 {
			t.Fatalf("linear keyDelta: %+v", pt)
		}
	}
	// Resolve verbs: staged, multiplier 1 whatever the saved profile says
	js, _ := Curve("jogSeconds", map[string]any{"secondsPerStep": 10.0, "detentThreshold": 2.0, "velocityProfile": "fast"}, 60)
	if !js.Staged || js.Unit != "seconds" || js.Profile != "fast" {
		t.Fatalf("%+v", js)
	}
	for _, pt := range js.Points {
		if pt.Multiplier != 1 {
			t.Fatalf("staged jogSeconds must report today's gain: %+v", pt)
		}
	}
	jog, _ := Curve("jog", map[string]any{"base": 1.0, "gamma": 1.6}, 100)
	if !jog.Staged || !jog.Approx || jog.Unit != "frames" || jog.Points[0].OutputPerSec != 0 {
		t.Fatalf("%+v", jog)
	}
	if jog.Points[CurveSamples].Multiplier <= jog.Points[1].Multiplier {
		t.Fatal("jog gamma 1.6 must accelerate")
	}
	if _, err := Curve("key", nil, 50); err == nil {
		t.Fatal("non-turn type must error")
	}
	for _, bad := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, err := Curve("scrollDelta", nil, bad); err == nil {
			t.Fatalf("maxTicksPerSec %v must error", bad)
		}
	}
	// negative lines are a direction: the meter shows speed
	n, _ := Curve("scrollDelta", map[string]any{"linesPerDetent": -3.0}, 10)
	if n.Points[CurveSamples].OutputPerSec <= 0 {
		t.Fatal("meter shows magnitude")
	}
}
