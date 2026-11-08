package main

import (
	"encoding/json"
	"strings"
	"testing"

	"2t5-deck-plus/internal/preset"
)

func jsonOf(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("%v: %s", err, s)
	}
	return m
}

// The whole editor flow in the --sim server: a deck button as the trigger, a
// resized window, a moved panel, a second screen, permissions: nothing real is
// posted anywhere (the fake records).
func TestSimCalibrationFlow(t *testing.T) {
	s, _ := testServer(t, true)
	cal := "/api/calibration/" + preset.ResolveID
	post := func(path, body string) map[string]any {
		rec := get(t, s.Handler, "POST", path, body)
		return jsonOf(t, rec.Body.String())
	}
	get1 := func(path string) map[string]any { return jsonOf(t, get(t, s.Handler, "GET", path, "").Body.String()) }

	// 1. wait for a deck key, hover the control, press a deck button through the simulator
	v := post(cal+"/capture", `{"target":"gain_G","via":"deck-key"}`)
	if v["state"] != "waiting" {
		t.Fatalf("%v", v)
	}
	post("/api/dev/calibration", `{"action":"hover","target":"gain_G"}`)
	if r := post("/api/simulate", `{"control":"c1","kind":"press"}`); !strings.Contains(r["out"].(map[string]any)["text"].(string), "calibration capture") {
		t.Fatalf("the press must be consumed as the trigger: %v", r)
	}
	v = get1(cal + "/capture/" + v["id"].(string))
	if v["state"] != "captured" {
		t.Fatalf("%v", v)
	}
	post(cal+"/capture/"+v["id"].(string)+"/confirm", "")
	if ov := get1(cal); ov["summary"].(map[string]any)["calibrated"].(float64) != 1 {
		t.Fatalf("%v", ov["summary"])
	}
	// 2. the window moves: still fine; a 4K screen: needs calibration, says so
	post("/api/dev/calibration", `{"action":"window","x":200,"y":60}`)
	tr := post(cal+"/test", `{"target":"gain_G","mode":"move"}`)
	if tr["found"] != true || tr["moved"] != true {
		t.Fatalf("%v", tr)
	}
	post("/api/dev/calibration", `{"action":"screen","display":"4k"}`)
	tr = post(cal+"/test", `{"target":"gain_G","mode":"move"}`)
	if tr["found"] == true || tr["needsCalibration"] != true || !strings.Contains(tr["message"].(string), "needs calibration for this screen") {
		t.Fatalf("%v", tr)
	}
	// 3. Accessibility off: clear message with the settings link
	post("/api/dev/permissions", `{"accessibility":false}`)
	post("/api/dev/calibration", `{"action":"screen","display":"laptop"}`)
	post("/api/dev/calibration", `{"action":"window","x":0,"y":25,"w":1512,"h":957}`)
	ov := get1(cal)
	if ov["permissions"].(map[string]any)["accessibility"] != false {
		t.Fatalf("%v", ov["permissions"])
	}
	tr = post(cal+"/test", `{"target":"gain_G","mode":"click","confirm":true}`)
	if tr["clicked"] == true || tr["problem"].(map[string]any)["settingsUrl"] == "" {
		t.Fatalf("%v", tr)
	}
	// 4. a bad dev action is rejected
	if rec := get(t, s.Handler, "POST", "/api/dev/calibration", `{"action":"explode"}`); rec.Code != 400 {
		t.Fatalf("%d", rec.Code)
	}
	if d := get1("/api/dev/calibration"); len(d["clicks"].([]any)) != 0 {
		t.Fatalf("nothing may be clicked in this flow: %v", d["clicks"])
	}
}
