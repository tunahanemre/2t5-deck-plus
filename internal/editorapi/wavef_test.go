package editorapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"2t5-deck-plus/internal/preset"
)

var t0 = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)

func ms(n int) time.Time { return t0.Add(time.Duration(n) * time.Millisecond) }

func obs(id, active string) SwitchObs { return SwitchObs{FrontID: id, Active: active} }

const chromeID = "com.google.Chrome"

func TestSwitchLog_StableSwitchIsRecorded(t *testing.T) {
	l := NewSwitchLog()
	l.Note(ms(0), obs(preset.ResolveID, preset.ResolveID))
	l.Note(ms(700), obs(preset.ResolveID, preset.ResolveID))
	l.Note(ms(1400), obs(chromeID, chromeID))
	l.Note(ms(1800), obs(chromeID, chromeID)) // 400 ms later: settled
	sn := l.Snapshot(ms(1800))
	if len(sn.Log) != 1 {
		t.Fatalf("log = %+v", sn.Log)
	}
	e := sn.Log[0]
	if e.From != preset.ResolveID || e.To != chromeID || e.Reason != WhyFrontmost || e.PresetID != chromeID || e.T != ms(1400).UnixMilli() {
		t.Errorf("event = %+v", e)
	}
	if sn.IgnoredBlips != 0 {
		t.Errorf("blips = %d", sn.IgnoredBlips)
	}
}

func TestSwitchLog_SnapshotCommitsASettledPendingChange(t *testing.T) {
	l := NewSwitchLog()
	l.Note(ms(0), obs(preset.ResolveID, preset.ResolveID))
	l.Note(ms(1000), obs(chromeID, chromeID))
	if n := len(l.Snapshot(ms(1200)).Log); n != 0 {
		t.Errorf("committed after 200 ms (%d)", n)
	}
	if n := len(l.Snapshot(ms(1400)).Log); n != 1 {
		t.Errorf("not committed after 400 ms (%d)", n)
	}
}

func TestSwitchLog_BlipUnder400msIsCountedNotLogged(t *testing.T) {
	l := NewSwitchLog()
	l.Note(ms(0), obs(chromeID, chromeID))
	l.Note(ms(1000), obs("com.apple.dock", preset.GlobalID)) // a Dock click / notification
	l.Note(ms(1250), obs(chromeID, chromeID))                // back after 250 ms
	l.Note(ms(2000), obs(chromeID, chromeID))
	sn := l.Snapshot(ms(2000))
	if len(sn.Log) != 0 {
		t.Errorf("a blip was logged: %+v", sn.Log)
	}
	if sn.IgnoredBlips != 1 {
		t.Errorf("blips = %d", sn.IgnoredBlips)
	}
}

func TestSwitchLog_LongExcursionAndReturnAreTwoSwitches(t *testing.T) {
	l := NewSwitchLog()
	l.Note(ms(0), obs(chromeID, chromeID))
	l.Note(ms(1000), obs(preset.FinderID, preset.FinderID))
	l.Note(ms(1600), obs(chromeID, chromeID)) // 600 ms: real
	l.Note(ms(2100), obs(chromeID, chromeID))
	sn := l.Snapshot(ms(2100))
	if len(sn.Log) != 2 || sn.IgnoredBlips != 0 {
		t.Fatalf("log = %+v blips = %d", sn.Log, sn.IgnoredBlips)
	}
	if sn.Log[0].To != chromeID || sn.Log[1].To != preset.FinderID { // newest first
		t.Errorf("order: %+v", sn.Log)
	}
}

func TestSwitchLog_BlipThenRealSwitchToAThirdApp(t *testing.T) {
	l := NewSwitchLog()
	l.Note(ms(0), obs(chromeID, chromeID))
	l.Note(ms(1000), obs("com.apple.dock", preset.GlobalID))
	l.Note(ms(1200), obs(preset.FinderID, preset.FinderID)) // 200 ms, then somewhere else
	l.Note(ms(2000), obs(preset.FinderID, preset.FinderID))
	sn := l.Snapshot(ms(2000))
	if len(sn.Log) != 1 || sn.Log[0].From != chromeID || sn.Log[0].To != preset.FinderID {
		t.Fatalf("log = %+v", sn.Log)
	}
	if sn.IgnoredBlips != 1 {
		t.Errorf("blips = %d", sn.IgnoredBlips)
	}
}

func TestSwitchLog_ReasonsFollowWhy(t *testing.T) {
	l := NewSwitchLog()
	l.Note(ms(0), obs(chromeID, chromeID))
	l.Note(ms(1000), obs(preset.ResolveID, preset.ResolveID))
	l.Note(ms(2000), obs("com.unknown.App", preset.GlobalID))
	l.Note(ms(3000), SwitchObs{FrontID: "com.unknown.App", Active: preset.GlobalID, EditorFocused: true})
	l.Note(ms(4000), SwitchObs{FrontID: "com.unknown.App", Active: preset.GlobalID, EditorFocused: true})
	sn := l.Snapshot(ms(4000))
	var got []string
	for i := len(sn.Log) - 1; i >= 0; i-- {
		got = append(got, sn.Log[i].Reason)
	}
	want := []string{WhyResolveCascade, WhyGlobalFallback, WhyEditorFocused}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("reasons = %v, want %v", got, want)
	}
}

func TestSwitchLog_RingKeepsTheLast8(t *testing.T) {
	l := NewSwitchLog()
	at := 0
	l.Note(ms(at), obs("a.a", "a.a"))
	for i := 0; i < 12; i++ {
		at += 1000
		l.Note(ms(at), obs(fmt.Sprintf("x.app%d", i), preset.GlobalID))
	}
	at += 1000
	l.Note(ms(at), obs("x.app11", preset.GlobalID))
	sn := l.Snapshot(ms(at))
	if len(sn.Log) != 8 {
		t.Fatalf("len = %d", len(sn.Log))
	}
	if sn.Log[0].To != "x.app11" || sn.Log[7].To != "x.app4" {
		t.Errorf("ring = %v ... %v", sn.Log[0].To, sn.Log[7].To)
	}
}

func TestSwitchLog_UnknownFrontIsNotAChange(t *testing.T) {
	l := NewSwitchLog()
	l.Note(ms(0), obs(chromeID, chromeID))
	l.Note(ms(1000), SwitchObs{}) // lookup failed
	l.Note(ms(2000), obs(chromeID, chromeID))
	sn := l.Snapshot(ms(3000))
	if len(sn.Log) != 0 || sn.IgnoredBlips != 0 {
		t.Errorf("%+v", sn)
	}
}

func TestSwitchLog_NilAndConcurrentSafe(t *testing.T) {
	var nl *SwitchLog
	nl.Note(ms(0), obs("a.a", "a.a"))
	if sn := nl.Snapshot(ms(0)); sn.Log == nil || len(sn.Log) != 0 {
		t.Errorf("nil log snapshot = %+v", sn)
	}
	l := NewSwitchLog()
	done := make(chan bool)
	for g := 0; g < 4; g++ {
		go func(g int) {
			for i := 0; i < 500; i++ {
				l.Note(ms(i*100+g), obs(fmt.Sprintf("x.a%d", i%3), preset.GlobalID))
				l.Snapshot(ms(i * 100))
			}
			done <- true
		}(g)
	}
	for g := 0; g < 4; g++ {
		<-done
	}
}

// ---- HTTP -------------------------------------------------------------------

func TestAPI_SwitchesEndpointAndLiveWhy(t *testing.T) {
	log := NewSwitchLog()
	e := envWith(t, false, func(d *Deps) { d.Switches = log })
	log.Note(ms(0), obs(chromeID, chromeID))
	log.Note(ms(500), obs(preset.FinderID, preset.FinderID))
	log.Note(ms(1000), obs(preset.FinderID, preset.FinderID))
	log.Note(ms(2000), obs("com.apple.dock", preset.GlobalID))
	log.Note(ms(2100), obs(preset.FinderID, preset.FinderID))
	log.Note(ms(2600), obs(preset.FinderID, preset.FinderID))
	code, out := e.do(t, "GET", "/api/switches", "")
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	sw, _ := out["switches"].([]any)
	if len(sw) != 1 || out["ignoredBlips"].(float64) != 1 || out["blipMs"].(float64) != 400 || out["keep"].(float64) != 8 {
		t.Errorf("switches = %v", out)
	}
	row := sw[0].(map[string]any)
	for _, k := range []string{"t", "from", "to", "reason", "presetId"} {
		if _, ok := row[k]; !ok {
			t.Errorf("event lacks %q: %v", k, row)
		}
	}
	code, live := e.do(t, "GET", "/api/live", "")
	if code != 200 {
		t.Fatal(code)
	}
	why := live["why"].(map[string]any)
	if l, _ := why["switchLog"].([]any); len(l) != 1 {
		t.Errorf("why.switchLog = %v", why["switchLog"])
	}
	if why["ignoredBlips"].(float64) != 1 {
		t.Errorf("why.ignoredBlips = %v", why["ignoredBlips"])
	}
}

func TestAPI_SwitchesWithoutProviderIsEmptyNotMissing(t *testing.T) {
	e := newEnv(t, false)
	code, out := e.do(t, "GET", "/api/switches", "")
	if code != 200 {
		t.Fatal(code)
	}
	if sw, ok := out["switches"].([]any); !ok || len(sw) != 0 || out["ignoredBlips"].(float64) != 0 {
		t.Errorf("%v", out)
	}
	_, live := e.do(t, "GET", "/api/live", "")
	why := live["why"].(map[string]any)
	if l, ok := why["switchLog"].([]any); !ok || len(l) != 0 {
		t.Errorf("why.switchLog = %v", why["switchLog"])
	}
}

func TestAPI_SwitchesIsReadOnly(t *testing.T) {
	e := newEnv(t, false)
	if code, _ := e.do(t, "POST", "/api/switches", "", "X-LD-Editor", "1"); code == 200 {
		t.Error("POST /api/switches answered 200")
	}
}

// ---- canary over HTTP -----------------------------------------------------------

func canaryEnv(t *testing.T) (*env, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "presets")
	open := func() *preset.Store {
		st, err := preset.Open(dir, preset.Options{LiveResolvePath: "-", PollInterval: -1, Controls: ControlIDs(), CanaryDebounce: -1})
		if err != nil {
			t.Fatal(err)
		}
		return st
	}
	st := open()
	if _, err := st.AddApp(chromeID, "", nil); err != nil {
		t.Fatal(err)
	}
	for i, c := range []string{"C1", "C2", "C3", "C4"} {
		if err := st.Assign(chromeID, "base", c, map[string]any{"type": "key", "key": "Cmd+" + string(rune('A'+i))}); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok, _ := st.BackupNow(); !ok {
		t.Fatal("backup")
	}
	st.Close()
	// outside edit while closed: keep only C1
	p := filepath.Join(dir, chromeID+".json")
	b, _ := os.ReadFile(p)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	base := m["banks"].(map[string]any)["base"].(map[string]any)
	for _, c := range []string{"C2", "C3", "C4"} {
		delete(base, c)
	}
	nb, _ := json.Marshal(m)
	os.WriteFile(p, nb, 0o644)
	st = open()
	t.Cleanup(st.Close)
	e := &env{store: st, front: &fakeFront{app: AppRef{ID: chromeID, Name: "Google Chrome"}}, bus: NewBus()}
	e.h = NewHandler(Deps{Store: st, Frontmost: e.front, Bus: e.bus, Live: fakeLive{EngineRunning: true, Sidecar: "connected"}})
	return e, dir
}

func TestAPI_CanaryInWarningsLiveAndAck(t *testing.T) {
	e, _ := canaryEnv(t)
	if c := e.store.Canary(); c == nil {
		t.Fatalf("setup did not produce a silent loss (problems: %v)", e.store.Problems())
	}
	_, w := e.do(t, "GET", "/api/warnings", "")
	var canary map[string]any
	for _, x := range w["warnings"].([]any) {
		m := x.(map[string]any)
		if m["kind"] == "canary" {
			if m["severity"] != "warn" {
				t.Errorf("severity = %v", m["severity"])
			}
			canary = m
		}
	}
	if canary == nil {
		t.Fatalf("no canary warning: %v", w)
	}
	if c, _ := w["canary"].(map[string]any); c == nil || c["since"] == nil || len(c["lost"].([]any)) != 1 {
		t.Errorf("warnings.canary = %v", w["canary"])
	}
	_, live := e.do(t, "GET", "/api/live", "")
	eng := live["engine"].(map[string]any)
	c, _ := eng["canary"].(map[string]any)
	if c == nil || c["backup"] == "" {
		t.Fatalf("engine.canary = %v", eng["canary"])
	}
	raw := httptest.NewRequest("POST", "/api/canary/ack", nil) // no X-LD-Editor header
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, raw)
	if rec.Code != 403 {
		t.Errorf("ack without the editor header = %d", rec.Code)
	}
	if e.store.Canary() == nil {
		t.Fatal("a refused ack dismissed the canary")
	}
	code, out := e.do(t, "POST", "/api/canary/ack", "", "X-LD-Editor", "1")
	if code != 200 || out["ok"] != true || out["dismissed"] != true {
		t.Fatalf("ack = %d %v", code, out)
	}
	_, w2 := e.do(t, "GET", "/api/warnings", "")
	if w2["canary"] != nil {
		t.Errorf("canary after ack: %v", w2["canary"])
	}
	for _, x := range w2["warnings"].([]any) {
		if x.(map[string]any)["kind"] == "canary" {
			t.Error("canary warning after ack")
		}
	}
	_, live2 := e.do(t, "GET", "/api/live", "")
	if live2["engine"].(map[string]any)["canary"] != nil {
		t.Error("engine.canary after ack")
	}
	code, out = e.do(t, "POST", "/api/canary/ack", "", "X-LD-Editor", "1")
	if code != 200 || out["dismissed"] != false {
		t.Errorf("second ack = %d %v", code, out)
	}
}

func TestAPI_NoCanaryNoField(t *testing.T) {
	e := newEnv(t, false)
	_, w := e.do(t, "GET", "/api/warnings", "")
	if w["canary"] != nil {
		t.Errorf("warnings.canary = %v", w["canary"])
	}
	_, live := e.do(t, "GET", "/api/live", "")
	if live["engine"].(map[string]any)["canary"] != nil {
		t.Error("engine.canary present without a loss")
	}
}

func TestSwitchLog_NoteFrontResolvesActivePreset(t *testing.T) {
	e := newEnv(t, false)
	if _, err := e.store.AddApp(chromeID, "", nil); err != nil {
		t.Fatal(err)
	}
	snap := e.store.Snapshot()
	l := NewSwitchLog()
	l.NoteFront(ms(0), AppRef{ID: "com.unknown.A"}, false, snap)
	l.NoteFront(ms(1000), AppRef{ID: chromeID}, false, snap)
	l.NoteFront(ms(1500), AppRef{ID: chromeID}, false, snap)
	l.NoteFront(ms(1600), AppRef{}, false, snap) // lookup failed
	sn := l.Snapshot(ms(2000))
	if len(sn.Log) != 1 || sn.Log[0].PresetID != chromeID || sn.Log[0].Reason != WhyFrontmost {
		t.Fatalf("%+v", sn.Log)
	}
	l.NoteFront(ms(3000), AppRef{ID: "com.unknown.B"}, false, snap)
	sn = l.Snapshot(ms(3500))
	if sn.Log[0].PresetID != preset.GlobalID || sn.Log[0].Reason != WhyGlobalFallback {
		t.Fatalf("%+v", sn.Log[0])
	}
}

// ---- B2: the app starts DISARMED (policy applied before the engine can run) ---------------

type orderArm struct {
	armed bool
	calls []string
}

func (o *orderArm) Armed() bool { return o.armed }
func (o *orderArm) SetArmed(b bool) {
	o.armed = b
	o.calls = append(o.calls, fmt.Sprintf("SetArmed(%v)", b))
}

func TestApplyStartPolicy_DisarmsFirstRunBeforeAnythingElse(t *testing.T) {
	e := newEnv(t, false)
	arm := &orderArm{armed: true}
	// armed by default (Tunahan 2026-11-04): a first run no longer starts disarmed
	st := ApplyStartPolicy(e.store, arm, func(string) string { return "" })
	if !st.Armed || st.Reason != "" || !arm.armed {
		t.Fatalf("start = %+v arm = %+v", st, arm)
	}
	// once the user has armed, and nothing else applies: armed, and the switch is left alone
	e.store.MarkFirstRunDone()
	e.store.ClearDisarmed() // the user armed: the remembered disarmed start is gone (wave N)
	arm2 := &orderArm{armed: true}
	st = ApplyStartPolicy(e.store, arm2, func(string) string { return "" })
	if !st.Armed || st.Reason != "" || !arm2.armed {
		t.Errorf("start = %+v calls = %v", st, arm2.calls)
	}
	// env forces it
	arm3 := &orderArm{armed: true}
	st = ApplyStartPolicy(e.store, arm3, func(k string) string {
		if k == preset.EnvStartDisarmed {
			return "1"
		}
		return ""
	})
	if st.Armed || st.Reason != preset.StartReasonEnv || arm3.armed {
		t.Errorf("env start = %+v armed=%v", st, arm3.armed)
	}
	// no switch at all (dev server): nothing to do, reported as armed
	if st := ApplyStartPolicy(e.store, nil, nil); !st.Armed {
		t.Errorf("nil switch = %+v", st)
	}
}

func TestAPI_LiveReportsArmedAtStartAndArmingMarksFirstRun(t *testing.T) {
	arm := &fakeArm{armed: true}
	var start *ArmStart
	e := envWith(t, false, func(d *Deps) { d.Arm = arm })
	start = &ArmStart{Armed: false, Reason: preset.StartReasonFirstRun}
	arm.SetArmed(false)
	e = envWith(t, false, func(d *Deps) { d.Arm = arm; d.ArmStart = start })
	_, live := e.do(t, "GET", "/api/live", "")
	if live["armedAtStart"] != false || live["startReason"] != "first-run" || live["armed"] != false {
		t.Fatalf("live = armedAtStart %v startReason %v armed %v", live["armedAtStart"], live["startReason"], live["armed"])
	}
	// disarming does not mark; arming does
	if code, _ := e.do(t, "POST", "/api/arm", `{"armed":false}`); code != 200 {
		t.Fatal(code)
	}
	if d, _ := e.store.StartDisarmed(func(string) string { return "" }); !d {
		t.Error("marker written by a disarm")
	}
	if code, out := e.do(t, "POST", "/api/arm", `{"armed":true}`); code != 200 || out["armed"] != true {
		t.Fatalf("arm = %d %v", code, out)
	}
	if d, why := e.store.StartDisarmed(func(string) string { return "" }); d {
		t.Errorf("arming deliberately did not mark the first run done (%q)", why)
	}
	_, live = e.do(t, "GET", "/api/live", "")
	if live["armedAtStart"] != false { // it is about how the app STARTED
		t.Errorf("armedAtStart changed after arming: %v", live["armedAtStart"])
	}
}

func TestAPI_LiveWithoutStartPolicyIsArmedAtStart(t *testing.T) {
	e := newEnv(t, false)
	_, live := e.do(t, "GET", "/api/live", "")
	if live["armedAtStart"] != true {
		t.Errorf("armedAtStart = %v", live["armedAtStart"])
	}
	if _, has := live["startReason"]; has {
		t.Error("startReason present without a policy")
	}
}
