package editorapi

import (
	"net/http/httptest"
	"strings"
	"testing"

	"2t5-deck-plus/internal/mousecal"
	"2t5-deck-plus/internal/preset"
)

type calEnv struct {
	*env
	fake  *mousecal.FakeSystem
	armed bool
}

func newCalEnv(t *testing.T) *calEnv {
	t.Helper()
	e := newEnv(t, true)
	c := &calEnv{env: e, fake: mousecal.NewFakeSystem(preset.ResolveID)}
	c.fake.ArmedFn = func() bool { return c.armed }
	svc := &mousecal.Service{Sys: c.fake, Store: mousecal.PresetSettings{Store: e.store},
		Preset: func(id string) *preset.Preset { p, _ := e.store.Snapshot().Preset(id); return p }}
	e.h = NewHandler(Deps{Store: e.store, Frontmost: e.front, Bus: e.bus, Calib: svc,
		Running: fakeRunning{{ID: preset.ResolveID, Name: "DaVinci Resolve"}}, Live: fakeLive{EngineRunning: true, Sidecar: "connected"}})
	return c
}

const cal = "/api/calibration/" + preset.ResolveID

func TestCalibrationFlowOverHTTP(t *testing.T) {
	c := newCalEnv(t)
	code, ov := c.do(t, "GET", cal, "")
	if code != 200 {
		t.Fatalf("overview %d %v", code, ov)
	}
	sum := ov["summary"].(map[string]any)
	if sum["total"].(float64) < 12 || sum["calibrated"].(float64) != 0 || ov["context"] == nil || ov["problem"] != nil {
		t.Fatalf("%v", sum)
	}
	perm := ov["permissions"].(map[string]any)
	if perm["accessibility"] != true || perm["accessibilityUrl"] == "" {
		t.Fatalf("%v", perm)
	}
	// hover and capture
	want, _ := c.fake.WidgetPoint(preset.ResolveID, "gain_R")
	c.fake.SetCursor(want)
	code, v := c.do(t, "POST", cal+"/capture", `{"target":"gain_R","via":"cursor"}`)
	if code != 200 || v["state"] != "captured" || v["marker"] == nil || v["hasPreview"] != true {
		t.Fatalf("capture %d %v", code, v)
	}
	id := v["id"].(string)
	// nothing saved yet
	_, ov = c.do(t, "GET", cal, "")
	if ov["summary"].(map[string]any)["calibrated"].(float64) != 0 {
		t.Fatal("a capture must not save until confirmed")
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, newReq("GET", cal+"/capture/"+id+"/preview.png", strings.NewReader("")))
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("preview %d %s", rec.Code, rec.Header().Get("Content-Type"))
	}
	code, r := c.do(t, "POST", cal+"/capture/"+id+"/confirm", "")
	if code != 200 || r["ok"] != true {
		t.Fatalf("confirm %d %v", code, r)
	}
	if code, r = c.do(t, "POST", cal+"/capture/"+id+"/confirm", ""); code != 409 {
		t.Fatalf("second confirm must conflict: %d %v", code, r)
	}
	_, ov = c.do(t, "GET", cal, "")
	if ov["summary"].(map[string]any)["calibrated"].(float64) != 1 {
		t.Fatalf("%v", ov["summary"])
	}
	// undo reverts it like any other edit
	if code, r = c.do(t, "POST", "/api/undo", ""); code != 200 || r["ok"] != true {
		t.Fatalf("undo %d %v", code, r)
	}
	_, ov = c.do(t, "GET", cal, "")
	if ov["summary"].(map[string]any)["calibrated"].(float64) != 0 {
		t.Fatal("undo must remove the calibration")
	}
}

func TestCalibrationRefusalsOverHTTP(t *testing.T) {
	c := newCalEnv(t)
	if code, _ := c.do(t, "POST", cal+"/capture", `{"target":"nope"}`); code != 404 {
		t.Fatalf("unknown target %d", code)
	}
	if code, _ := c.do(t, "POST", cal+"/capture", `{"target":"gain_R","via":"telepathy"}`); code != 400 {
		t.Fatalf("unknown source %d", code)
	}
	if code, _ := c.do(t, "GET", cal+"/capture/zzz", ""); code != 404 {
		t.Fatalf("unknown capture %d", code)
	}
	if code, _ := c.do(t, "GET", "/api/calibration/com.nope.app", ""); code != 404 {
		t.Fatalf("unknown app %d", code)
	}
	// pointer outside the window: a failed capture says why
	c.fake.SetCursor(mousecal.Pt{X: 9000, Y: 9000})
	_, v := c.do(t, "POST", cal+"/capture", `{"target":"gain_R"}`)
	if v["state"] != "failed" || !strings.Contains(v["message"].(string), "not over") {
		t.Fatalf("%v", v)
	}
	// a mutating call without the editor header is refused by the shared guard
	req := newReq("POST", cal+"/capture", strings.NewReader(`{"target":"gain_R"}`))
	req.Header.Del("X-LD-Editor")
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	if rec.Code < 400 {
		t.Fatalf("CSRF guard bypassed: %d", rec.Code)
	}
}

func TestCalibrationTestRouteNeverClicksUnlessArmedAndConfirmed(t *testing.T) {
	c := newCalEnv(t)
	p, _ := c.fake.WidgetPoint(preset.ResolveID, "lift_B")
	c.fake.SetCursor(p)
	_, v := c.do(t, "POST", cal+"/capture", `{"target":"lift_B"}`)
	c.do(t, "POST", cal+"/capture/"+v["id"].(string)+"/confirm", "")
	code, res := c.do(t, "POST", cal+"/test", `{"target":"lift_B","mode":"move"}`)
	if code != 200 || res["found"] != true || res["moved"] != false || len(c.fake.Moves) != 0 {
		t.Fatalf("disarmed move %d %v", code, res)
	}
	c.armed = true
	_, res = c.do(t, "POST", cal+"/test", `{"target":"lift_B","mode":"move"}`)
	if res["moved"] != true || len(c.fake.Clicks) != 0 {
		t.Fatalf("armed move %v", res)
	}
	_, res = c.do(t, "POST", cal+"/test", `{"target":"lift_B","mode":"click"}`)
	if res["clicked"] == true || len(c.fake.Clicks) != 0 {
		t.Fatalf("click without confirm: %v", res)
	}
	_, res = c.do(t, "POST", cal+"/test", `{"target":"lift_B","mode":"click","confirm":true}`)
	if res["clicked"] != true || len(c.fake.Clicks) != 1 {
		t.Fatalf("confirmed click: %v", res)
	}
	if code, _ := c.do(t, "POST", cal+"/test", `{"target":"lift_B","mode":"bite"}`); code != 400 {
		t.Fatalf("bad mode %d", code)
	}
}

func TestCalibrationAppsSessionOptionsAndClear(t *testing.T) {
	c := newCalEnv(t)
	code, r := c.do(t, "GET", "/api/calibration", "")
	apps := r["apps"].([]any)
	if code != 200 || len(apps) == 0 {
		t.Fatalf("%d %v", code, r)
	}
	found := false
	for _, a := range apps {
		found = found || a.(map[string]any)["id"] == preset.ResolveID
	}
	if !found {
		t.Fatalf("Resolve must be listed: %v", apps)
	}
	_, s := c.do(t, "GET", cal+"/session?scope=layout", "")
	if len(s["steps"].([]any)) < 12 {
		t.Fatalf("%v", s)
	}
	if code, _ := c.do(t, "PUT", cal+"/options", `{"verify":"warn","blockLegacy":true}`); code != 200 {
		t.Fatalf("options %d", code)
	}
	_, ov := c.do(t, "GET", cal, "")
	st := ov["settings"].(map[string]any)
	if st["verify"] != "warn" || st["blockLegacy"] != true {
		t.Fatalf("%v", st)
	}
	if code, _ := c.do(t, "PUT", cal+"/options", `{"verify":"maybe"}`); code != 400 {
		t.Fatalf("bad verify %d", code)
	}
	if code, _ := c.do(t, "DELETE", cal+"/target/nope", ""); code != 404 {
		t.Fatalf("clear unknown %d", code)
	}
}

func TestCalibrationAbsentHostAnswers404(t *testing.T) {
	e := newEnv(t, true)
	if code, _ := e.do(t, "GET", cal, ""); code != 404 {
		t.Fatalf("%d", code)
	}
}
