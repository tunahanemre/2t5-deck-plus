package editorapi

import (
	"encoding/json"
	"math"
	"net/http"
	"testing"

	"2t5-deck-plus/internal/accel"
	"2t5-deck-plus/internal/preset"
)

type fakeWhy FrontInfo

func (f fakeWhy) FrontInfo() FrontInfo { return FrontInfo(f) }

func envWithWhy(t *testing.T, seed bool, w FrontInfoProvider) *env {
	t.Helper()
	e := newEnv(t, seed)
	e.h = NewHandler(Deps{Store: e.store, Frontmost: e.front, Bus: e.bus, Why: w,
		Running: fakeRunning{{ID: "com.google.Chrome", Name: "Google Chrome"}},
		Live:    fakeLive{EngineRunning: true, Sidecar: "connected"}, Simulate: e.sim.Simulate})
	return e
}

func TestLiveWhyReasons(t *testing.T) {
	info := &fakeWhy{Since: 1793739664440}
	e := envWithWhy(t, true, info)
	why := func() map[string]any {
		code, live := e.do(t, "GET", "/api/live", "")
		if code != 200 {
			t.Fatal(code)
		}
		for _, k := range []string{"frontmost", "activeApp", "engineRunning", "sidecar", "seq", "why"} {
			if _, ok := live[k]; !ok {
				t.Fatalf("live lost %q: %v", k, live)
			}
		}
		return live["why"].(map[string]any)
	}
	// Chrome frontmost, no preset yet
	w := why()
	if w["reason"] != "no-preset-global-fallback" || w["activeId"] != preset.GlobalID || w["since"].(float64) != 1793739664440 {
		t.Fatalf("no preset: %v", w)
	}
	if f := w["frontmost"].(map[string]any); f["id"] != "com.google.Chrome" || f["name"] != "Google Chrome" {
		t.Fatalf("frontmost: %v", f)
	}
	// preset exists
	if c, _ := e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`); c != 201 {
		t.Fatal(c)
	}
	if w = why(); w["reason"] != "frontmost" || w["activeId"] != "com.google.Chrome" {
		t.Fatalf("frontmost: %v", w)
	}
	// editor focused keeps the last app and says so
	*info = fakeWhy{EditorFocused: true, Since: 5}
	if w = why(); w["reason"] != "editor-focused-kept-last" || w["activeId"] != "com.google.Chrome" || w["since"].(float64) != 5 {
		t.Fatalf("editor focused: %v", w)
	}
	// Resolve: its own cascade
	*info = fakeWhy{}
	e.front.mu.Lock()
	e.front.app = AppRef{ID: preset.ResolveID, Name: "DaVinci Resolve"}
	e.front.mu.Unlock()
	if w = why(); w["reason"] != "resolve-cascade" || w["activeId"] != preset.ResolveID {
		t.Fatalf("resolve: %v", w)
	}
	// nothing known
	e.front.mu.Lock()
	e.front.app = AppRef{}
	e.front.mu.Unlock()
	if w = why(); w["reason"] != "no-preset-global-fallback" || w["activeId"] != preset.GlobalID {
		t.Fatalf("empty: %v", w)
	}
}

func TestLiveWithoutWhyProviderStillWorks(t *testing.T) {
	e := newEnv(t, false)
	_, live := e.do(t, "GET", "/api/live", "")
	w, ok := live["why"].(map[string]any)
	if !ok || w["since"].(float64) != 0 || w["reason"] == "" {
		t.Fatalf("why: %v", live["why"])
	}
}

func curvePoints(t *testing.T, m map[string]any) []map[string]float64 {
	t.Helper()
	var out []map[string]float64
	for _, p := range m["points"].([]any) {
		pm := map[string]float64{}
		for k, v := range p.(map[string]any) {
			pm[k] = v.(float64)
		}
		out = append(out, pm)
	}
	return out
}

func TestCurveEndpoint(t *testing.T) {
	e := newEnv(t, false)
	code, r := e.do(t, "POST", "/api/curve", `{"action":"browser.scroll","fields":{"linesPerDetent":2,"detentThreshold":2,"velocityProfile":"fast"},"maxTicksPerSec":120}`)
	if code != 200 || r["unit"] != "lines" || r["profile"] != "fast" || r["staged"] != false {
		t.Fatalf("curve: %d %v", code, r)
	}
	pts := curvePoints(t, r)
	if len(pts) != 25 || pts[0]["ticksPerSec"] != 0 || pts[24]["ticksPerSec"] != 120 {
		t.Fatalf("points: %v", pts)
	}
	fast, _ := accel.ProfileByName("fast")
	last := pts[24]
	if math.Abs(last["multiplier"]-fast.Multiplier(120)) > 1e-9 || math.Abs(last["outputPerSec"]-120*fast.Multiplier(120)*1.0) > 1e-9 {
		t.Fatalf("last point %v", last)
	}
	// default (no fields) is linear and 1:1 with the seeded browser defaults (3 lines, 1 click)
	code, r = e.do(t, "POST", "/api/curve", `{"action":"browser.scroll"}`)
	pts = curvePoints(t, r)
	if code != 200 || r["profile"] != "linear" || r["maxTicksPerSec"].(float64) != 120 || pts[24]["multiplier"] != 1 || pts[24]["outputPerSec"] != 360 {
		t.Fatalf("default curve: %d %v", code, r)
	}
	// binding type name works too; keyDelta unit
	code, r = e.do(t, "POST", "/api/curve", `{"action":"keyDelta","fields":{"detentThreshold":4}}`)
	if code != 200 || r["unit"] != "keys" || curvePoints(t, r)[24]["outputPerSec"] != 30 {
		t.Fatalf("keyDelta: %d %v", code, r)
	}
	// Resolve verbs are staged: they report today's behaviour
	code, r = e.do(t, "POST", "/api/curve", `{"action":"resolve.scrubFrames","fields":{"velocityProfile":"fast","momentumMs":300}}`)
	if code != 200 || r["staged"] != true || r["approx"] != true || r["unit"] != "frames" || r["profile"] != "fast" || r["momentumMs"].(float64) != 300 {
		t.Fatalf("jog: %d %v", code, r)
	}
	code, r = e.do(t, "POST", "/api/curve", `{"action":"resolve.scrubSeconds","fields":{"secondsPerStep":30}}`)
	if code != 200 || r["staged"] != true || r["unit"] != "seconds" || curvePoints(t, r)[24]["outputPerSec"] != 120.0/4*30 {
		t.Fatalf("jogSeconds: %d %v", code, r)
	}
	bad := []struct {
		name, body, field string
	}{
		{"unknown action", `{"action":"nope"}`, "action"},
		{"empty action", `{}`, "action"},
		{"press action", `{"action":"generic.key"}`, "action"},
		{"bad profile", `{"action":"browser.scroll","fields":{"velocityProfile":"turbo"}}`, "velocityProfile"},
		{"bad momentum", `{"action":"browser.scroll","fields":{"momentumMs":9999}}`, "momentumMs"},
		{"max zero", `{"action":"browser.scroll","maxTicksPerSec":0}`, "maxTicksPerSec"},
		{"max huge", `{"action":"browser.scroll","maxTicksPerSec":100000}`, "maxTicksPerSec"},
		{"max text", `{"action":"browser.scroll","maxTicksPerSec":"x"}`, "maxTicksPerSec"},
	}
	for _, c := range bad {
		code, r := e.do(t, "POST", "/api/curve", c.body)
		if code != 400 || r["field"] != c.field || r["error"] == nil {
			t.Errorf("%s: %d %v", c.name, code, r)
		}
	}
	if code, _ := e.do(t, "POST", "/api/curve", `{`); code != 400 {
		t.Errorf("bad json: %d", code)
	}
	if code, _ := e.do(t, "GET", "/api/curve", ""); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Errorf("GET curve: %d", code)
	}
}

func TestSimulatedAppTurnCarriesRealAccel(t *testing.T) {
	e := newEnv(t, false)
	if c, _ := e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`); c != 201 {
		t.Fatal(c)
	}
	put := func(a string) {
		if c, r := e.do(t, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"D1","assignment":`+a+`}`); c != 200 {
			t.Fatalf("assign: %d %v", c, r)
		}
	}
	put(`{"action":"browser.scroll"}`)
	_, ev := e.do(t, "POST", "/api/simulate", `{"control":"D1","kind":"turn","ticks":3}`)
	if ev["accel"].(float64) != 1 {
		t.Fatalf("linear accel = %v, want exactly 1", ev["accel"])
	}
	put(`{"action":"browser.scroll","velocityProfile":"fast"}`)
	_, ev = e.do(t, "POST", "/api/simulate", `{"control":"D1","kind":"turn","ticks":3}`)
	fast, _ := accel.ProfileByName("fast")
	if math.Abs(ev["accel"].(float64)-fast.Multiplier(ev["ticksPerSec"].(float64))) > 1e-9 || ev["accel"].(float64) <= 1 {
		t.Fatalf("fast accel = %v at %v tps", ev["accel"], ev["ticksPerSec"])
	}
}

func TestRegistryExposesLabsPressAndFeel(t *testing.T) {
	e := newEnv(t, false)
	_, reg := e.do(t, "GET", "/api/registry", "")
	byID := map[string]map[string]any{}
	for _, p := range reg["packs"].([]any) {
		for _, a := range p.(map[string]any)["actions"].([]any) {
			am := a.(map[string]any)
			byID[am["id"].(string)] = am
		}
	}
	for _, id := range []string{"resolve.shuttle", "resolve.scrollTimeline"} {
		a := byID[id]
		if a == nil || a["labs"] != true || a["unimplemented"] != true {
			t.Fatalf("%s: %v", id, a)
		}
	}
	if byID["generic.key"]["labs"] != nil {
		t.Error("labs must be absent on ordinary actions")
	}
	field := func(id, key string) map[string]any {
		for _, f := range byID[id]["fields"].([]any) {
			if fm := f.(map[string]any); fm["key"] == key {
				return fm
			}
		}
		t.Fatalf("%s lacks %s", id, key)
		return nil
	}
	if f := field("browser.scroll", "velocityProfile"); f["primary"] != nil || len(f["options"].([]any)) != 4 {
		t.Errorf("velocityProfile: %v", f)
	}
	if f := field("resolve.wheel", "press"); f["kind"] != "object" || len(f["group"].([]any)) != 4 {
		t.Errorf("press group: %v", f)
	}
}

func TestPressRoundTripAndValidationOverAPI(t *testing.T) {
	e := newEnv(t, false)
	put := func(press string) (int, map[string]any) {
		return e.do(t, "PUT", "/api/preset/_global/assign",
			`{"bank":"base","control":"Exposure","assignment":{"type":"wheel","wheel":"lift_master","press":`+press+`}}`)
	}
	for _, p := range []string{`{"type":"none"}`, `{"type":"key","key":"Cmd+Z"}`, `{"type":"click","point":"lift_reset"}`, `{"type":"selectWheel","wheel":"gamma"}`, `{"type":"resetActiveWheel"}`} {
		if c, r := put(p); c != 200 {
			t.Fatalf("%s: %d %v", p, c, r)
		}
		_, got := e.do(t, "GET", "/api/preset/_global", "")
		a := got["preset"].(map[string]any)["banks"].(map[string]any)["base"].(map[string]any)["Exposure"].(map[string]any)
		var want any
		_ = json.Unmarshal([]byte(p), &want)
		gb, _ := json.Marshal(a["press"])
		wb, _ := json.Marshal(want)
		if string(gb) != string(wb) {
			t.Errorf("round trip %s -> %s", wb, gb)
		}
	}
	for _, c := range []struct{ body, field string }{
		{`{"type":"key"}`, "press.key"}, {`{"type":"click"}`, "press.point"},
		{`{"type":"selectWheel"}`, "press.wheel"}, {`{"type":"selectWheel","wheel":"x"}`, "press.wheel"},
		{`{"type":"zzz"}`, "press.type"},
	} {
		if code, r := put(c.body); code != 400 || r["field"] != c.field {
			t.Errorf("%s: %d %v", c.body, code, r)
		}
	}
	// press can be cleared back to the built-in reset
	if c, _ := e.do(t, "PUT", "/api/preset/_global/value", `{"bank":"base","control":"Exposure","key":"press","value":null}`); c != 200 {
		t.Errorf("clear press: %d", c)
	}
}

func TestDefaultsDiffEndpoint(t *testing.T) {
	e := newEnv(t, true)
	code, r := e.do(t, "GET", "/api/defaults/diff?app="+preset.ResolveID, "")
	counts := r["counts"].(map[string]any)
	if code != 200 || r["hasSeed"] != true || counts["changed"].(float64)+counts["added"].(float64)+counts["removed"].(float64) != 0 || counts["unchanged"].(float64) < 50 {
		t.Fatalf("fresh: %d %v", code, r)
	}
	if len(r["controls"].([]any)) != 0 || len(r["settings"].([]any)) != 0 {
		t.Errorf("fresh diff lists: %v", r)
	}
	// change one value, remove another
	_, pr := e.do(t, "GET", "/api/preset/"+preset.ResolveID, "")
	banks := pr["preset"].(map[string]any)["banks"].(map[string]any)
	var bank, c1, c2 string
	for bn, bm := range banks {
		for cn := range bm.(map[string]any) {
			if c1 == "" {
				bank, c1 = bn, cn
			} else if c2 == "" && (bn != bank || cn != c1) {
				c2 = cn
				if bn != bank {
					c2 = ""
				}
			}
		}
	}
	if c, r := e.do(t, "PUT", "/api/preset/"+preset.ResolveID+"/value", `{"bank":"`+bank+`","control":"`+c1+`","key":"label","value":"Edited"}`); c != 200 {
		t.Fatalf("edit: %d %v", c, r)
	}
	code, r = e.do(t, "GET", "/api/defaults/diff?app="+preset.ResolveID, "")
	ctl := r["controls"].([]any)
	if code != 200 || len(ctl) != 1 {
		t.Fatalf("after edit: %d %v", code, r)
	}
	c0 := ctl[0].(map[string]any)
	f0 := c0["fields"].([]any)[0].(map[string]any)
	if c0["bank"] != bank || c0["control"] != c1 || c0["change"] != "changed" || f0["key"] != "label" || f0["current"] != "Edited" {
		t.Errorf("control diff: %v", c0)
	}
	for _, c := range []struct {
		path string
		code int
		f    string
	}{{"/api/defaults/diff", 400, "app"}, {"/api/defaults/diff?app=com.nope", 404, "id"}} {
		if code, r := e.do(t, "GET", c.path, ""); code != c.code || r["field"] != c.f {
			t.Errorf("%s: %d %v", c.path, code, r)
		}
	}
	if code, r := e.do(t, "GET", "/api/defaults/diff?app=global", ""); code != 200 || r["app"] != preset.GlobalID {
		t.Errorf("global alias: %d %v", code, r)
	}
	// strictly read-only: the diff does not move the preset sequence
	_, h1 := e.do(t, "GET", "/api/health", "")
	e.do(t, "GET", "/api/defaults/diff?app="+preset.ResolveID, "")
	_, h2 := e.do(t, "GET", "/api/health", "")
	if h1["seq"] != h2["seq"] {
		t.Errorf("diff changed seq: %v -> %v", h1["seq"], h2["seq"])
	}
}
