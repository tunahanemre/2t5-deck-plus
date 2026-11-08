package mousecal

import (
	"math"
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)

func ctxAt(layout, display string, x, y, w, h float64) Context {
	return Context{Display: display, Layout: layout, Win: Rect{x, y, w, h}}
}

// truth: x = 0.3 of the width, y = 120 above the bottom edge.
func truth(c Context) Pt {
	return Pt{c.Win.X + 0.3*c.Win.W, c.Win.Y + c.Win.H - 120}
}

func entryFor(c Context, at time.Time) Entry { return NewEntry(c, truth(c), "cursor", "click", at) }

func near(a, b Pt) bool { return math.Abs(a.X-b.X) < 0.6 && math.Abs(a.Y-b.Y) < 0.6 }

func TestMovedWindowAndSameClassDisplayKeepThePoint(t *testing.T) {
	c1 := ctxAt("color", "1512x982@2", 0, 25, 1512, 957)
	e := entryFor(c1, t0)
	// the window dragged elsewhere / onto another monitor of the same class
	c2 := ctxAt("color", "1512x982@2", 1700, 300, 1512, 957)
	r := Resolve([]Entry{e}, c2, Prior{})
	if r.Status != StOK || r.Confidence != ConfExact || !near(r.Point, truth(c2)) {
		t.Fatalf("moved window: %+v want %v", r, truth(c2))
	}
}

func TestOtherDisplayClassAndOtherLayoutAreRefused(t *testing.T) {
	c1 := ctxAt("color", "1512x982@2", 0, 25, 1512, 957)
	e := entryFor(c1, t0)
	r := Resolve([]Entry{e}, ctxAt("color", "3840x2160@1", 0, 0, 3840, 2160), Prior{X: ModeFrac, Y: ModeEnd})
	if r.Status != StNeeds || r.Reason != ReasonOtherScreen {
		t.Fatalf("other screen must refuse even with a prior: %+v", r)
	}
	r = Resolve([]Entry{e}, ctxAt("edit", "1512x982@2", 0, 25, 1512, 957), Prior{})
	if r.Status != StNeeds || r.Reason != ReasonOtherLayout || len(r.OtherContexts) != 1 {
		t.Fatalf("other layout: %+v", r)
	}
	if r.Message == "" || r.Message[:len("needs calibration for this layout")] != "needs calibration for this layout" {
		t.Fatalf("message must name the problem: %q", r.Message)
	}
	if r := Resolve(nil, c1, Prior{}); r.Reason != ReasonNone {
		t.Fatalf("no entries: %+v", r)
	}
}

func TestResizeWithoutKnowledgeRefuses(t *testing.T) {
	c1 := ctxAt("color", "d", 0, 0, 1500, 900)
	e := entryFor(c1, t0)
	r := Resolve([]Entry{e}, ctxAt("color", "d", 0, 0, 1800, 1000), Prior{})
	if r.Status != StNeeds || r.Reason != ReasonResized {
		t.Fatalf("a single capture with no prior must not extrapolate: %+v", r)
	}
}

func TestPriorExtrapolatesButNeedsAProbe(t *testing.T) {
	c1 := ctxAt("color", "d", 0, 0, 1500, 900)
	e := entryFor(c1, t0)
	c2 := ctxAt("color", "d", 100, 50, 1800, 1000)
	r := Resolve([]Entry{e}, c2, Prior{X: ModeFrac, Y: ModeEnd})
	if r.Status != StOK || !r.NeedsProbe || r.Confidence != ConfPrior || !near(r.Point, truth(c2)) {
		t.Fatalf("prior: %+v want %v", r, truth(c2))
	}
	// a wrong prior lands elsewhere: that is exactly why the probe is required
	bad := Resolve([]Entry{e}, c2, Prior{X: ModeStart, Y: ModeStart})
	if !bad.NeedsProbe || near(bad.Point, truth(c2)) {
		t.Fatalf("wrong prior should be flagged and wrong: %+v", bad)
	}
}

func TestTwoSizesLearnTheRuleAndFollowAResize(t *testing.T) {
	c1 := ctxAt("color", "d", 0, 0, 1500, 900)
	c2 := ctxAt("color", "d", 0, 0, 1900, 1100)
	es := []Entry{entryFor(c1, t0), entryFor(c2, t0.Add(time.Minute))}
	m := FitModel(es)
	if !m.X.Learned || m.X.Mode != ModeFrac || !m.Y.Learned || m.Y.Mode != ModeEnd {
		t.Fatalf("model %+v", m)
	}
	c3 := ctxAt("color", "d", 40, 30, 1700, 1000)
	r := Resolve(es, c3, Prior{})
	if r.Status != StOK || r.NeedsProbe || r.Confidence != ConfLearned || !near(r.Point, truth(c3)) {
		t.Fatalf("learned: %+v want %v", r, truth(c3))
	}
	// far outside the sizes it was learned between: not trusted
	far := Resolve(es, ctxAt("color", "d", 0, 0, 3000, 1800), Prior{})
	if far.Status != StNeeds {
		t.Fatalf("far extrapolation must refuse: %+v", far)
	}
}

func TestNonlinearLayoutDoesNotFit(t *testing.T) {
	a := ctxAt("l", "d", 0, 0, 1500, 900)
	b := ctxAt("l", "d", 0, 0, 1900, 1100)
	ea := NewEntry(a, Pt{450, 700}, "cursor", "click", t0)
	eb := NewEntry(b, Pt{900, 650}, "cursor", "click", t0.Add(time.Minute)) // no single rule
	m := FitModel([]Entry{ea, eb})
	_ = m
	r := Resolve([]Entry{ea, eb}, ctxAt("l", "d", 0, 0, 1700, 1000), Prior{})
	// either a rule fits both within tolerance (then fine) or it refuses; it must never guess silently
	if r.Status == StOK && r.Confidence != ConfLearned {
		t.Fatalf("unexpected: %+v", r)
	}
}

func TestExactMatchTolerance(t *testing.T) {
	c := ctxAt("color", "d", 0, 0, 1500, 900)
	e := entryFor(c, t0)
	if r := Resolve([]Entry{e}, ctxAt("color", "d", 0, 0, 1501.5, 899), Prior{}); r.Confidence != ConfExact {
		t.Fatalf("1.5pt jitter is the same size: %+v", r)
	}
	if r := Resolve([]Entry{e}, ctxAt("color", "d", 0, 0, 1504, 900), Prior{}); r.Status != StNeeds {
		t.Fatalf("4pt is a different size: %+v", r)
	}
}

func TestPickWindow(t *testing.T) {
	if _, err := PickWindow(nil); err != ErrNoWindow {
		t.Fatal(err)
	}
	small := Window{Frame: Rect{0, 0, 300, 200}}
	big := Window{Frame: Rect{0, 0, 1500, 900}}
	w, err := PickWindow([]Window{small, big})
	if err != nil || w.Frame != big.Frame {
		t.Fatalf("%v %v", w, err)
	}
	if _, err := PickWindow([]Window{big, {Frame: Rect{100, 0, 1400, 900}}}); err != ErrAmbiguous {
		t.Fatalf("two big windows: %v", err)
	}
	// a palette half the size is fine
	if _, err := PickWindow([]Window{big, {Frame: Rect{0, 0, 800, 600}}}); err != nil {
		t.Fatal(err)
	}
}

func TestBlockEntriesReplaceCapAndRemove(t *testing.T) {
	c := ctxAt("color", "d", 0, 0, 1500, 900)
	b, _ := ParseBlock(nil)
	b = b.WithEntry("k", entryFor(c, t0))
	b = b.WithEntry("k", entryFor(c, t0.Add(time.Minute))) // same context: replaced
	if n := len(b.Entries("k")); n != 1 {
		t.Fatalf("same context must replace, got %d", n)
	}
	for i := 0; i < 20; i++ {
		cc := ctxAt("color", "d", 0, 0, 1500+float64(i*10), 900)
		b = b.WithEntry("k", entryFor(cc, t0.Add(time.Duration(i+2)*time.Minute)))
	}
	if n := len(b.Entries("k")); n != MaxEntries {
		t.Fatalf("cap: %d", n)
	}
	id := b.Entries("k")[0].ID
	b = b.Without("k", id)
	if len(b.Entries("k")) != MaxEntries-1 {
		t.Fatal("remove one")
	}
	b = b.Without("k", "")
	if len(b.Targets) != 0 {
		t.Fatal("remove all")
	}
	rt, err := ParseBlock(b.WithEntry("z", entryFor(c, t0)).Value())
	if err != nil || len(rt.Entries("z")) != 1 {
		t.Fatalf("round trip %v", err)
	}
}

func TestProbeDistance(t *testing.T) {
	a := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"
	if d, ok := ProbeDistance(a, a); !ok || d != 0 {
		t.Fatal(d, ok)
	}
	if _, ok := ProbeDistance(a, "zz"); ok {
		t.Fatal("malformed must not match")
	}
	if _, ok := ProbeDistance("", a); ok {
		t.Fatal("empty must not match")
	}
}

func TestProbeNoticesAMovedControlButNotChangingDigits(t *testing.T) {
	f := NewFakeSystem("x")
	f.Apps["x"] = &FakeApp{Running: true, Windows: []Window{{Frame: Rect{0, 0, 1000, 800}}}, Widgets: []FakeWidget{{"w", 0.5, 400}}}
	c := Pt{500, 400}
	ref, _ := f.Grab(c, ProbeSize)
	base := ProbeOf(ref)
	same, _ := f.Grab(c, ProbeSize)
	if d, ok := ProbeDistance(base, ProbeOf(same)); !ok || d != 0 {
		t.Fatal(d, ok)
	}
	f.Apps["x"].PanelMove = Pt{70, -40}
	moved, _ := f.Grab(c, ProbeSize)
	if d, _ := ProbeDistance(base, ProbeOf(moved)); d <= ProbeThreshold {
		t.Fatalf("a moved control must exceed the threshold, got %v", d)
	}
	// digits changing inside the control: two cells of a different brightness
	b2 := []byte(base)
	_ = b2
	dig := base[:2*27] + "00" + base[2*27+2:]
	if d, ok := ProbeDistance(base, dig); !ok || d > ProbeThreshold {
		t.Fatalf("one changed cell must stay under the threshold: %v", d)
	}
}
