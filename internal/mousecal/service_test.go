package mousecal

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"2t5-deck-plus/internal/preset"
)

const rid = preset.ResolveID

type rig struct {
	t     *testing.T
	st    *preset.Store
	fake  *FakeSystem
	svc   *Service
	now   time.Time
	armed bool
	after []func()
}

func newRig(t *testing.T) *rig {
	t.Helper()
	st, err := preset.Open(filepath.Join(t.TempDir(), "presets"), preset.Options{
		LiveResolvePath: filepath.Join("..", "preset", "testdata", "live-ld-resolve-bindings.json"),
		SeedFrom:        filepath.Join("..", "preset", "testdata", "2t5-deck-plus-repo-bindings.json"),
		PollInterval:    -1,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	r := &rig{t: t, st: st, fake: NewFakeSystem(rid), now: t0}
	r.fake.ArmedFn = func() bool { return r.armed }
	r.svc = &Service{
		Sys: r.fake, Store: PresetSettings{st},
		Preset: func(id string) *preset.Preset { p, _ := st.Snapshot().Preset(id); return p },
		Now:    func() time.Time { return r.now },
		After:  func(d time.Duration, fn func()) func() { r.after = append(r.after, fn); return func() {} },
	}
	return r
}

func (r *rig) tick(d time.Duration) { r.now = r.now.Add(d) }

// hoverAndCapture puts the simulated pointer on the true widget spot and saves it.
func (r *rig) calibrate(key string) {
	r.t.Helper()
	p, ok := r.fake.WidgetPoint(rid, key)
	if !ok {
		r.t.Fatalf("no widget %s", key)
	}
	r.fake.SetCursor(p)
	v, err := r.svc.StartCapture(rid, key, ViaNow, 0)
	if err != nil || v.State != CCaptured {
		r.t.Fatalf("capture %s: %+v %v", key, v, err)
	}
	if _, err := r.svc.Confirm(rid, v.ID); err != nil {
		r.t.Fatal(err)
	}
	r.tick(time.Minute)
}

func TestDeriveTargetsForResolve(t *testing.T) {
	r := newRig(t)
	ts := DeriveTargets(r.svc.Preset(rid))
	keys := map[string]Target{}
	for _, x := range ts {
		keys[x.Key] = x
	}
	for _, k := range []string{"lift_master", "gamma_master", "gain_master", "lift_R", "offset_B"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("missing target %s (have %d)", k, len(ts))
		}
	}
	if keys["lift_master"].Kind != KindDragH || keys["lift_master"].Axis != "h" {
		t.Errorf("%+v", keys["lift_master"])
	}
	if got := keys["gain_R"].Label; got != "Gain Red value box" {
		t.Errorf("label %q", got)
	}
	if len(keys["gain_R"].UsedBy) == 0 {
		t.Error("usedBy must name the controls")
	}
}

func TestCaptureConfirmUndoAndLocate(t *testing.T) {
	r := newRig(t)
	ov, err := r.svc.Overview(rid)
	if err != nil || ov.Summary.Needs == 0 || ov.Summary.Calibrated != 0 || ov.Context == nil {
		t.Fatalf("fresh overview %+v %v", ov.Summary, err)
	}
	r.calibrate("gain_R")
	l := r.svc.Locate(rid, "gain_R", LocateOpts{})
	want, _ := r.fake.WidgetPoint(rid, "gain_R")
	if !l.OK() || !near(l.Point, want) || l.Confidence != ConfExact || !l.Probe.Checked || !l.Probe.OK {
		t.Fatalf("locate %+v want %v", l, want)
	}
	// undo removes it: the editor's normal history covers calibration
	if _, ok, err := r.st.Undo(); err != nil || !ok {
		t.Fatalf("undo %v %v", ok, err)
	}
	r.tick(time.Second)
	if l := r.svc.Locate(rid, "gain_R", LocateOpts{}); l.OK() {
		t.Fatalf("after undo the target must be uncalibrated: %+v", l)
	}
	if _, ok, _ := r.st.Redo(); !ok {
		t.Fatal("redo")
	}
}

func TestMovedWindowStillClicksTheRightSpot(t *testing.T) {
	r := newRig(t)
	r.calibrate("lift_master")
	r.fake.With(rid, func(a *FakeApp) { a.Windows[0].Frame.X += 120; a.Windows[0].Frame.Y += 10 })
	r.tick(time.Second)
	l := r.svc.Locate(rid, "lift_master", LocateOpts{})
	want, _ := r.fake.WidgetPoint(rid, "lift_master")
	if !l.OK() || !near(l.Point, want) {
		t.Fatalf("moved window: %+v want %v", l, want)
	}
}

func TestPanelMovedWithSameWindowIsCaughtByTheProbe(t *testing.T) {
	r := newRig(t)
	r.calibrate("lift_R")
	r.fake.With(rid, func(a *FakeApp) { a.PanelMove = Pt{70, -40} })
	r.tick(time.Second)
	l := r.svc.Locate(rid, "lift_R", LocateOpts{Input: true})
	if l.OK() || l.Blocked == nil || l.Blocked.Code != PLayoutChanged {
		t.Fatalf("a moved panel must block the click: %+v", l)
	}
	if !strings.Contains(l.Say(), "needs calibration for this layout") {
		t.Fatalf("message: %q", l.Say())
	}
	// warn mode lets it through with a note; off skips the check
	if err := r.svc.SetOptions(rid, ptr(VerifyWarn), nil); err != nil {
		t.Fatal(err)
	}
	r.tick(5 * time.Second)
	l = r.svc.Locate(rid, "lift_R", LocateOpts{})
	if !l.OK() || l.Warn == "" {
		t.Fatalf("warn: %+v", l)
	}
}

func ptr[T any](v T) *T { return &v }

func TestResizeFollowsAfterTwoSizesAndNeedsProbeBefore(t *testing.T) {
	r := newRig(t)
	r.calibrate("gain_G")
	// resized: only the Resolve prior is available, probe must agree (it does: the fake follows frac/end)
	r.fake.With(rid, func(a *FakeApp) { a.Windows[0].Frame = Rect{0, 25, 1300, 800} })
	r.tick(time.Second)
	l := r.svc.Locate(rid, "gain_G", LocateOpts{})
	want, _ := r.fake.WidgetPoint(rid, "gain_G")
	if !l.OK() || l.Confidence != ConfPrior || !near(l.Point, want) || !l.Probe.OK {
		t.Fatalf("prior+probe: %+v want %v", l, want)
	}
	// without Screen Recording the unverified extrapolation is refused
	r.fake.ScreenRec = false
	r.tick(5 * time.Second)
	l = r.svc.Locate(rid, "gain_G", LocateOpts{})
	if l.OK() || l.Blocked == nil || l.Blocked.Code != PUnverified || l.Blocked.SettingsURL != URLScreenRecording {
		t.Fatalf("unverified resize must be refused with the setting link: %+v", l)
	}
	r.fake.ScreenRec = true
	// calibrate at the new size: a second size, the rule is learned, no probe needed any more
	r.calibrate("gain_G")
	r.fake.With(rid, func(a *FakeApp) { a.Windows[0].Frame = Rect{0, 25, 1400, 850} })
	r.fake.ScreenRec = false
	r.tick(5 * time.Second)
	l = r.svc.Locate(rid, "gain_G", LocateOpts{})
	want, _ = r.fake.WidgetPoint(rid, "gain_G")
	if !l.OK() || l.Confidence != ConfLearned || !near(l.Point, want) {
		t.Fatalf("learned: %+v want %v", l, want)
	}
}

func TestOtherMonitorClassAndOtherPageNeedCalibration(t *testing.T) {
	r := newRig(t)
	r.calibrate("offset_R")
	r.fake.With(rid, func(a *FakeApp) { a.Windows[0].Frame = Rect{1512, 0, 3840, 2160} })
	r.tick(time.Second)
	l := r.svc.Locate(rid, "offset_R", LocateOpts{Input: true})
	if l.OK() || l.Reason != ReasonOtherScreen {
		t.Fatalf("4K screen: %+v", l)
	}
	ov, _ := r.svc.Overview(rid)
	for _, tv := range ov.Targets {
		if tv.Key == "offset_R" && tv.Status != TStale {
			t.Fatalf("status %s", tv.Status)
		}
	}
	// back, other page
	r.fake.With(rid, func(a *FakeApp) { a.Windows[0].Frame = Rect{0, 25, 1512, 957}; a.Layout = "edit" })
	r.tick(time.Second)
	if l := r.svc.Locate(rid, "offset_R", LocateOpts{}); l.OK() || l.Reason != ReasonOtherLayout {
		t.Fatalf("edit page: %+v", l)
	}
	// calibrating for this layout keeps the other one
	r.calibrate("offset_R")
	r.fake.With(rid, func(a *FakeApp) { a.Layout = "color" })
	r.tick(time.Second)
	if l := r.svc.Locate(rid, "offset_R", LocateOpts{}); !l.OK() {
		t.Fatalf("color entry must survive: %+v", l)
	}
}

func TestCaptureRefusals(t *testing.T) {
	r := newRig(t)
	start := func() *PendingView {
		v, err := r.svc.StartCapture(rid, "gain_R", ViaNow, 0)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	r.fake.SetCursor(Pt{5000, 5000})
	if v := start(); v.State != CFailed || !strings.Contains(v.Message, "not over") {
		t.Fatalf("pointer outside: %+v", v)
	}
	r.fake.SetCursor(Pt{300, 300})
	r.fake.With(rid, func(a *FakeApp) { a.Front = false })
	if v := start(); v.State != CFailed || !strings.Contains(v.Message, "not the front app") {
		t.Fatalf("not front: %+v", v)
	}
	r.fake.With(rid, func(a *FakeApp) { a.Front = true; a.Running = false })
	if v := start(); v.State != CFailed || !strings.Contains(v.Message, "not running") {
		t.Fatalf("not running: %+v", v)
	}
	r.fake.With(rid, func(a *FakeApp) {
		a.Running = true
		a.Windows = append(a.Windows, Window{Frame: Rect{100, 100, 1400, 850}})
	})
	r.tick(time.Second)
	if v := start(); v.State != CFailed || !strings.Contains(v.Message, "two big windows") {
		t.Fatalf("ambiguous: %+v", v)
	}
	if _, err := r.svc.StartCapture(rid, "no_such_target", ViaNow, 0); err != ErrNoTarget {
		t.Fatalf("unknown target: %v", err)
	}
	if _, err := r.svc.Confirm(rid, "nope"); err != ErrNoCapture {
		t.Fatalf("%v", err)
	}
	// a failed capture cannot be confirmed
	v := start()
	if _, err := r.svc.Confirm(rid, v.ID); err == nil || !strings.Contains(err.Error(), "not in a state") {
		t.Fatalf("confirm failed capture: %v", err)
	}
}

func TestCaptureNeverPostsInput(t *testing.T) {
	r := newRig(t)
	r.armed = true
	r.calibrate("gain_B")
	if _, err := r.svc.Test(rid, "gain_B", "move", false); err != nil {
		t.Fatal(err)
	}
	if len(r.fake.Clicks) != 0 {
		t.Fatalf("capture and move test must never click: %v", r.fake.Clicks)
	}
	if len(r.fake.Moves) != 1 {
		t.Fatalf("one dry-run move expected, got %v", r.fake.Moves)
	}
}

func TestTestModes(t *testing.T) {
	r := newRig(t)
	r.calibrate("gamma_B")
	r.armed = false
	res, err := r.svc.Test(rid, "gamma_B", "move", false)
	if err != nil || !res.Found || res.Moved || len(r.fake.Moves) != 0 || res.Marker == nil {
		t.Fatalf("disarmed move: %+v %v", res, err)
	}
	r.armed = true
	res, _ = r.svc.Test(rid, "gamma_B", "move", false)
	if !res.Moved || res.Clicked || len(r.fake.Clicks) != 0 {
		t.Fatalf("armed move: %+v", res)
	}
	// real click: refused without confirm, refused disarmed, done armed+confirmed
	if res, _ = r.svc.Test(rid, "gamma_B", "click", false); res.Clicked || len(r.fake.Clicks) != 0 {
		t.Fatalf("no confirm: %+v", res)
	}
	r.armed = false
	if res, _ = r.svc.Test(rid, "gamma_B", "click", true); res.Clicked || len(r.fake.Clicks) != 0 {
		t.Fatalf("disarmed: %+v", res)
	}
	r.armed = true
	r.fake.With(rid, func(a *FakeApp) { a.Front = false })
	r.tick(time.Second)
	if res, _ = r.svc.Test(rid, "gamma_B", "click", true); res.Clicked || res.Problem == nil || res.Problem.Code != PNotFront {
		t.Fatalf("not front: %+v", res)
	}
	r.fake.With(rid, func(a *FakeApp) { a.Front = true })
	r.fake.Accessibility = false
	r.tick(time.Second)
	if res, _ = r.svc.Test(rid, "gamma_B", "click", true); res.Clicked || res.Problem == nil || res.Problem.Code != PNoAccess || res.Problem.SettingsURL == "" {
		t.Fatalf("no accessibility: %+v", res)
	}
	r.fake.Accessibility = true
	r.tick(time.Second)
	if res, _ = r.svc.Test(rid, "gamma_B", "click", true); !res.Clicked || len(r.fake.Clicks) != 1 {
		t.Fatalf("armed confirmed click: %+v", res)
	}
	want, _ := r.fake.WidgetPoint(rid, "gamma_B")
	if !near(r.fake.Clicks[0], want) {
		t.Fatalf("clicked %v want %v", r.fake.Clicks[0], want)
	}
	// an uncalibrated target does not move or click at all
	n := len(r.fake.Moves)
	res, _ = r.svc.Test(rid, "lift_G", "move", false)
	if res.Found || !res.NeedsRecal || len(r.fake.Moves) != n {
		t.Fatalf("uncalibrated: %+v", res)
	}
}

func TestDeckKeyAndCountdownCapture(t *testing.T) {
	r := newRig(t)
	p, _ := r.fake.WidgetPoint(rid, "lift_B")
	v, err := r.svc.StartCapture(rid, "lift_B", ViaDeckKey, 0)
	if err != nil || v.State != CWaiting || !r.svc.DeckKeyWaiting() {
		t.Fatalf("%+v %v", v, err)
	}
	r.fake.SetCursor(p)
	if !r.svc.OnDeckKey() {
		t.Fatal("the press must be consumed")
	}
	if r.svc.OnDeckKey() {
		t.Fatal("only the first press is consumed")
	}
	got, _ := r.svc.Capture(rid, v.ID)
	if got.State != CCaptured || got.Marker == nil || got.Via != ViaDeckKey {
		t.Fatalf("%+v", got)
	}
	// countdown: nothing is read until the timer fires
	v, _ = r.svc.StartCapture(rid, "lift_G", ViaCountdown, 3*time.Second)
	if v.State != CWaiting || v.DeadlineMs == 0 {
		t.Fatalf("%+v", v)
	}
	p2, _ := r.fake.WidgetPoint(rid, "lift_G")
	r.fake.SetCursor(p2)
	r.after[len(r.after)-1]()
	got, _ = r.svc.Capture(rid, v.ID)
	if got.State != CCaptured {
		t.Fatalf("%+v", got)
	}
	// a new capture ends the earlier one of the same app
	r.svc.StartCapture(rid, "lift_R", ViaDeckKey, 0)
	if _, err := r.svc.Capture(rid, v.ID); err != ErrNoCapture {
		t.Fatalf("previous capture must be gone: %v", err)
	}
}

func TestDiscardDoesNotSave(t *testing.T) {
	r := newRig(t)
	p, _ := r.fake.WidgetPoint(rid, "gain_R")
	r.fake.SetCursor(p)
	v, _ := r.svc.StartCapture(rid, "gain_R", ViaNow, 0)
	if _, err := r.svc.Discard(rid, v.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.svc.Confirm(rid, v.ID); err == nil {
		t.Fatal("a discarded capture must not confirm")
	}
	if l := r.svc.Locate(rid, "gain_R", LocateOpts{}); l.OK() {
		t.Fatalf("nothing may be saved: %+v", l)
	}
}

func TestPreviewPictureHasAMarker(t *testing.T) {
	r := newRig(t)
	p, _ := r.fake.WidgetPoint(rid, "gain_R")
	r.fake.SetCursor(p)
	v, _ := r.svc.StartCapture(rid, "gain_R", ViaNow, 0)
	b, err := r.svc.PreviewPNG(rid, v.ID)
	if err != nil || len(b) < 100 || string(b[1:4]) != "PNG" {
		t.Fatalf("%d %v", len(b), err)
	}
	r.fake.ScreenRec = false
	v, _ = r.svc.StartCapture(rid, "gain_R", ViaNow, 0)
	if v.HasPreview {
		t.Fatal("no preview without Screen Recording")
	}
	if _, err := r.svc.PreviewPNG(rid, v.ID); err == nil {
		t.Fatal("must say there is no picture")
	}
}

func TestEngineResolverRefusesInsteadOfClickingBlind(t *testing.T) {
	r := newRig(t)
	res := r.svc.EngineResolver(rid)
	if _, _, _, ok := res("lift_master"); ok {
		t.Fatal("uncalibrated must refuse")
	}
	bl := r.svc.RecentBlocked(rid)
	if len(bl) != 1 || bl[0].Target != "lift_master" || bl[0].Label != "Lift master slider" {
		t.Fatalf("%+v", bl)
	}
	r.calibrate("lift_master")
	x, y, axis, ok := res("lift_master")
	want, _ := r.fake.WidgetPoint(rid, "lift_master")
	if !ok || axis != "h" || !near(Pt{x, y}, want) {
		t.Fatalf("%v %v %q %v", x, y, axis, ok)
	}
	// permission lost: refused, with the reason recorded
	r.fake.Accessibility = false
	r.tick(10 * time.Second)
	if _, _, _, ok := res("lift_master"); ok {
		t.Fatal("no Accessibility: must refuse")
	}
}

func TestLegacyPointsKeepWorkingUntilBlocked(t *testing.T) {
	r := newRig(t)
	r.svc.Legacy = func(k string) (Pt, string, bool) {
		if k == "gain_master" {
			return Pt{657, 995}, "h", true
		}
		return Pt{}, "", false
	}
	l := r.svc.Locate(rid, "gain_master", LocateOpts{})
	if !l.OK() || l.Confidence != ConfLegacy || l.Point.X != 657 {
		t.Fatalf("%+v", l)
	}
	ov, _ := r.svc.Overview(rid)
	for _, tv := range ov.Targets {
		if tv.Key == "gain_master" && tv.Status != TLegacy {
			t.Fatalf("status %s", tv.Status)
		}
	}
	if err := r.svc.SetOptions(rid, nil, ptr(true)); err != nil {
		t.Fatal(err)
	}
	r.tick(time.Second)
	if l := r.svc.Locate(rid, "gain_master", LocateOpts{}); l.OK() {
		t.Fatalf("blockLegacy must refuse: %+v", l)
	}
	// once calibrated, the new entry wins even with legacy allowed
	_ = r.svc.SetOptions(rid, nil, ptr(false))
	r.calibrate("gain_master")
	l = r.svc.Locate(rid, "gain_master", LocateOpts{})
	if l.Confidence == ConfLegacy {
		t.Fatalf("%+v", l)
	}
}

func TestSessionAndClear(t *testing.T) {
	r := newRig(t)
	all, _, _ := r.svc.Session(rid, "layout")
	missing, _, _ := r.svc.Session(rid, "missing")
	if len(all) != len(missing) || len(all) < 12 {
		t.Fatalf("%d %d", len(all), len(missing))
	}
	r.calibrate("gain_R")
	missing, _, _ = r.svc.Session(rid, "missing")
	all2, _, _ := r.svc.Session(rid, "layout")
	if len(missing) != len(all)-1 || len(all2) != len(all) {
		t.Fatalf("%d %d", len(missing), len(all2))
	}
	if err := r.svc.Clear(rid, "gain_R", ""); err != nil {
		t.Fatal(err)
	}
	r.tick(time.Second)
	if m, _, _ := r.svc.Session(rid, "missing"); len(m) != len(all) {
		t.Fatalf("cleared target is missing again: %d", len(m))
	}
	if err := r.svc.Clear(rid, "gain_R", "nope"); err == nil {
		t.Fatal("unknown entry id")
	}
	r.fake.With(rid, func(a *FakeApp) { a.Running = false })
	r.tick(time.Second)
	if _, p, _ := r.svc.Session(rid, "layout"); p == nil || p.Code != PNotRunning {
		t.Fatalf("%+v", p)
	}
}

func TestUnknownPageNeverGuesses(t *testing.T) {
	r := newRig(t)
	r.calibrate("gain_R")
	r.fake.With(rid, func(a *FakeApp) { a.Layout = "" })
	r.tick(time.Second)
	l := r.svc.Locate(rid, "gain_R", LocateOpts{Input: true})
	if l.OK() || l.Blocked == nil || l.Blocked.Code != PLayoutUnknown {
		t.Fatalf("%+v", l)
	}
	v, _ := r.svc.StartCapture(rid, "gain_R", ViaNow, 0)
	if v.State != CFailed || !strings.Contains(v.Message, "page is not known") {
		t.Fatalf("capture with the page unknown must fail: %+v", v)
	}
}
