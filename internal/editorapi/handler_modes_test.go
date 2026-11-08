package editorapi

import (
	"testing"

	"2t5-deck-plus/internal/preset"
)

func TestSliderModeEndpoints(t *testing.T) {
	e := newEnv(t, true)
	if c, _ := e.do(t, "POST", "/api/apps", `{"id":"com.adobe.LightroomClassicCC7"}`); c != 201 {
		t.Fatal(c)
	}
	app := "/api/preset/com.adobe.LightroomClassicCC7"
	// base P1, and a Sat-mode P1: the Hue|Sat|Lum switch edits real data
	if c, r := e.do(t, "PUT", app+"/assign", `{"bank":"base","control":"P1","assignment":{"type":"key","key":"Cmd+1"}}`); c != 200 {
		t.Fatal(c, r)
	}
	if c, r := e.do(t, "PUT", app+"/modes", `{"mode":"sat","control":"P1","assignment":{"type":"key","key":"Cmd+2"}}`); c != 200 {
		t.Fatal(c, r)
	}
	c, r := e.do(t, "GET", app+"/modes?control=P1", "")
	if c != 200 {
		t.Fatal(c, r)
	}
	modes := r["modes"].([]any)
	if len(modes) != 3 {
		t.Fatalf("modes: %v", modes)
	}
	own := map[string]bool{}
	for _, m := range modes {
		mm := m.(map[string]any)
		own[mm["mode"].(string)] = mm["own"].(bool)
	}
	if own["hue"] || !own["sat"] || own["lum"] {
		t.Fatalf("own flags: %v", own)
	}
	if fb := r["fallback"].(map[string]any); fb["assigned"] != true || fb["bank"] != "com.adobe.LightroomClassicCC7" {
		t.Fatalf("fallback: %v", fb)
	}
	// the bank is visible in the ordinary preset document
	_, pr := e.do(t, "GET", app, "")
	if _, ok := pr["preset"].(map[string]any)["banks"].(map[string]any)["pm-sat"]; !ok {
		t.Fatalf("pm-sat missing from the preset: %v", pr["preset"])
	}
	// simulate through the mode
	e.front.mu.Lock()
	e.front.app = AppRef{ID: "com.adobe.LightroomClassicCC7", Name: "Lightroom"}
	e.front.mu.Unlock()
	_, ev := e.do(t, "POST", "/api/simulate", `{"control":"P1","kind":"press","sliderMode":"sat"}`)
	if ev["bank"] != "com.adobe.LightroomClassicCC7-pm-sat" {
		t.Fatalf("simulate in sat mode used bank %v", ev["bank"])
	}
	_, ev = e.do(t, "POST", "/api/simulate", `{"control":"P1","kind":"press","sliderMode":"hue"}`)
	if ev["bank"] != "com.adobe.LightroomClassicCC7" {
		t.Fatalf("simulate in hue mode used bank %v", ev["bank"])
	}
	// undo is the same history as every other edit
	if c, _ := e.do(t, "POST", "/api/undo", ""); c != 200 {
		t.Fatal("undo")
	}
	_, r = e.do(t, "GET", app+"/modes?control=P1", "")
	for _, m := range r["modes"].([]any) {
		if m.(map[string]any)["own"] == true {
			t.Fatalf("undo left a mode assignment: %v", m)
		}
	}
	// refusals
	if c, r := e.do(t, "PUT", app+"/modes", `{"mode":"sat","control":"C1","assignment":{"type":"key","key":"x"}}`); c != 400 {
		t.Fatalf("non-P control: %d %v", c, r)
	}
	if c, _ := e.do(t, "PUT", app+"/modes", `{"mode":"blue","control":"P1","assignment":{"type":"key","key":"x"}}`); c != 400 {
		t.Fatal("bad mode")
	}
	if c, _ := e.do(t, "GET", "/api/preset/com.nope.App/modes?control=P1", ""); c != 404 {
		t.Fatal("unknown app")
	}
	if c, _ := e.do(t, "DELETE", app+"/modes?mode=sat&control=P1", ""); c != 200 {
		t.Fatal("delete of an absent mode assignment is a no-op")
	}
}

func TestResolveSliderModesAndStandardOptIn(t *testing.T) {
	e := newEnv(t, true)
	res := "/api/preset/" + preset.ResolveID
	c, r := e.do(t, "GET", res+"/modes?control=P1", "")
	if c != 200 || r["page"] != "col" {
		t.Fatal(c, r)
	}
	for _, m := range r["modes"].([]any) {
		if mm := m.(map[string]any); mm["own"] != false || mm["bank"] == "" {
			t.Fatalf("a Resolve preset seeded from live bindings must start with empty mode slots: %v", mm)
		}
	}
	if fb := r["fallback"].(map[string]any); fb["assigned"] != true || fb["bank"] != "col" {
		t.Fatalf("hue fallback must be today's col binding: %v", fb)
	}
	_, st := e.do(t, "GET", res+"/modes/standard", "")
	for _, b := range st["banks"].([]any) {
		if b.(map[string]any)["applied"] != false {
			t.Fatalf("standard must not be applied before opt-in: %v", b)
		}
	}
	if c, _ := e.do(t, "POST", res+"/modes/standard", `{"mode":"hue"}`); c != 400 {
		t.Fatal("hue has no standard mapping")
	}
	if c, r := e.do(t, "POST", res+"/modes/standard", `{"mode":"lum"}`); c != 200 {
		t.Fatal(c, r)
	}
	_, r = e.do(t, "GET", res+"/modes?control=P3", "")
	got := map[string]bool{}
	for _, m := range r["modes"].([]any) {
		got[m.(map[string]any)["mode"].(string)] = m.(map[string]any)["own"].(bool)
	}
	if got["sat"] || !got["lum"] {
		t.Fatalf("opt-in lum only: %v", got)
	}
	// page-specific bank for the Edit page
	if c, r := e.do(t, "PUT", res+"/modes", `{"mode":"sat","page":"edit","control":"P1btn","assignment":{"type":"key","key":"m"}}`); c != 200 {
		t.Fatal(c, r)
	}
	_, r = e.do(t, "GET", res+"/modes?control=P1btn&page=edit", "")
	if r["modes"].([]any)[1].(map[string]any)["bank"] != "edit-pm-sat" {
		t.Fatalf("%v", r["modes"])
	}
	if c, _ := e.do(t, "POST", "/api/preset/_global/modes/standard", ""); c != 400 {
		t.Fatal("standard exists for Resolve only")
	}
}

func TestLiveReportsCustomMode(t *testing.T) {
	e := newEnv(t, false)
	e.h = NewHandler(Deps{Store: e.store, Frontmost: e.front, Bus: e.bus,
		Live: fakeLive{EngineRunning: true, Sidecar: "connected", SliderMode: "sat", CustomMode: true}, Simulate: e.sim.Simulate})
	_, live := e.do(t, "GET", "/api/live", "")
	if live["sliderMode"] != "sat" || live["customMode"] != true {
		t.Fatalf("live: %v", live)
	}
}
