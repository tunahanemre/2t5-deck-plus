package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("bad JSON %q: %v", rec.Body.String(), err)
	}
	return m
}

func TestDevHostGuardAndFakes(t *testing.T) {
	s, _ := testServer(t, true)
	// a foreign Host is refused everywhere, the static page and the dev routes included
	for _, p := range []string{"/api/health", "/editor/", "/api/dev/frontmost"} {
		req := httptest.NewRequest("GET", p, nil)
		req.Host = "evil.example"
		rec := httptest.NewRecorder()
		s.Handler.ServeHTTP(rec, req)
		if rec.Code != 403 {
			t.Errorf("%s with a foreign host = %d", p, rec.Code)
		}
	}
	// permissions: fake, adjustable
	live := decodeBody(t, get(t, s.Handler, "GET", "/api/live", ""))
	p, _ := live["permissions"].(map[string]any)
	if p["accessibility"] != true || p["screenRecording"] != true || p["automationFinder"] != "unknown" {
		t.Fatalf("permissions = %v", p)
	}
	if rec := get(t, s.Handler, "POST", "/api/dev/permissions", `{"accessibility":false,"automationFinder":"denied"}`); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	live = decodeBody(t, get(t, s.Handler, "GET", "/api/live", ""))
	p, _ = live["permissions"].(map[string]any)
	if p["accessibility"] != false || p["automationFinder"] != "denied" {
		t.Errorf("permissions after = %v", p)
	}
	if rec := get(t, s.Handler, "POST", "/api/dev/permissions", `{"automationFinder":"maybe"}`); rec.Code != 400 {
		t.Errorf("bad finder state = %d", rec.Code)
	}
	// engine state follows the store; an injected failure shows in live and in the mutation reply
	rec := get(t, s.Handler, "POST", "/api/apps", `{"id":"com.google.Chrome"}`)
	if rec.Code != 201 || decodeBody(t, rec)["applied"] != true {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	live = decodeBody(t, get(t, s.Handler, "GET", "/api/live", ""))
	eb := live["engine"].(map[string]any)
	if eb["appliedSeq"] != eb["lastSeq"] || eb["composeError"] != "" {
		t.Errorf("engine = %v", eb)
	}
	if rec := get(t, s.Handler, "POST", "/api/dev/compose-error", `{"error":"compose failed (dev)"}`); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	rec = get(t, s.Handler, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"C1","assignment":{"type":"key","key":"Cmd+T"}}`)
	out := decodeBody(t, rec)
	if rec.Code != 200 || out["ok"] != true || out["applied"] != false || out["composeError"] != "compose failed (dev)" {
		t.Fatalf("assign under a failing compose: %d %v", rec.Code, out)
	}
	live = decodeBody(t, get(t, s.Handler, "GET", "/api/live", ""))
	eb = live["engine"].(map[string]any)
	if eb["composeError"] != "compose failed (dev)" || eb["appliedSeq"] == eb["lastSeq"] {
		t.Errorf("engine under failure = %v", eb)
	}
	get(t, s.Handler, "POST", "/api/dev/compose-error", `{"error":""}`)
	live = decodeBody(t, get(t, s.Handler, "GET", "/api/live", ""))
	eb = live["engine"].(map[string]any)
	if eb["composeError"] != "" || eb["appliedSeq"] != eb["lastSeq"] {
		t.Errorf("engine after clearing = %v", eb)
	}
}

func TestDevArmSwitchMarksSimulatedEvents(t *testing.T) {
	s, _ := testServer(t, true)
	get(t, s.Handler, "POST", "/api/dev/frontmost", `{"id":"com.google.Chrome","name":"Google Chrome"}`)
	get(t, s.Handler, "POST", "/api/apps", `{"id":"com.google.Chrome"}`)
	get(t, s.Handler, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"C1","assignment":{"type":"key","key":"Cmd+T"}}`)
	get(t, s.Handler, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"C2","assignment":{"type":"layer","layer":2}}`)
	sim := func(c string) map[string]any {
		rec := get(t, s.Handler, "POST", "/api/simulate", `{"control":"`+c+`","kind":"press"}`)
		if rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body.String())
		}
		return decodeBody(t, rec)
	}
	text := func(ev map[string]any) string { return ev["out"].(map[string]any)["text"].(string) }
	if got := text(sim("C1")); got != "Cmd+T" {
		t.Errorf("armed text = %q", got)
	}
	if rec := get(t, s.Handler, "POST", "/api/arm", `{"armed":false}`); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if live := decodeBody(t, get(t, s.Handler, "GET", "/api/live", "")); live["armed"] != false {
		t.Errorf("live armed = %v", live["armed"])
	}
	if got := text(sim("C1")); got != "disarmed: Cmd+T" {
		t.Errorf("disarmed text = %q", got)
	}
	if got := text(sim("C2")); strings.HasPrefix(got, "disarmed:") {
		t.Errorf("a layer switch posts no input, but is marked: %q", got)
	}
	// Resolve's own banks are unchanged
	get(t, s.Handler, "POST", "/api/dev/frontmost", `{"id":"com.blackmagic-design.DaVinciResolve","name":"DaVinci Resolve"}`)
	for _, c := range []string{"C1", "C2", "C3", "C4", "C5", "C6", "D1", "D2"} {
		if got := text(sim(c)); strings.HasPrefix(got, "disarmed:") {
			t.Errorf("Resolve event %s marked disarmed: %q", c, got)
		}
	}
	get(t, s.Handler, "POST", "/api/arm", `{"armed":true}`)
}

// Wave E, F11: the dev-only POST routes get the same vetting as the API.
func TestDevRoutesNeedTheEditorHeaderAndRefuseNullOrigin(t *testing.T) {
	s, _ := testServer(t, true)
	for _, p := range []string{"/api/dev/frontmost", "/api/dev/compose-error", "/api/dev/permissions"} {
		for _, hdr := range [][]string{
			{"Content-Type", "application/json"},                                       // no header
			{"Content-Type", "application/json", "X-LD-Editor", "1", "Origin", "null"}, // null origin
			{"X-LD-Editor", "1", "Content-Type", "text/plain"},                         // wrong type
		} {
			req := httptest.NewRequest("POST", p, strings.NewReader(`{"id":"com.x.y","error":"","accessibility":true}`))
			req.Host = "127.0.0.1:2541"
			for i := 0; i+1 < len(hdr); i += 2 {
				req.Header.Set(hdr[i], hdr[i+1])
			}
			rec := httptest.NewRecorder()
			s.Handler.ServeHTTP(rec, req)
			if rec.Code != 403 && rec.Code != 415 {
				t.Errorf("POST %s %v = %d, want refused", p, hdr, rec.Code)
			}
		}
	}
	if rec := get(t, s.Handler, "POST", "/api/dev/frontmost", `{"id":"com.x.y","name":"X"}`); rec.Code != 200 {
		t.Errorf("good dev request = %d %s", rec.Code, rec.Body.String())
	}
}

func TestDevSwitchLogFollowsFrontmost(t *testing.T) {
	s, _ := testServer(t, true)
	post := func(body string) {
		if rec := get(t, s.Handler, "POST", "/api/dev/frontmost", body); rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	post(`{"id":"com.apple.Safari","name":"Safari"}`)
	time.Sleep(450 * time.Millisecond)
	post(`{"id":"com.apple.finder","name":"Finder"}`) // a blip: back at once
	post(`{"id":"com.apple.Safari","name":"Safari"}`)
	sw := decodeBody(t, get(t, s.Handler, "GET", "/api/switches", ""))
	rows, _ := sw["switches"].([]any)
	if len(rows) == 0 {
		t.Fatalf("no switch logged: %v", sw)
	}
	first := rows[len(rows)-1].(map[string]any)
	if first["to"] != "com.apple.Safari" {
		t.Errorf("first = %v", first)
	}
	if sw["ignoredBlips"].(float64) != 1 {
		t.Errorf("blips = %v", sw["ignoredBlips"])
	}
}

func TestDevTokenFlagGuardsApiAndDevRoutes(t *testing.T) {
	static := t.TempDir()
	os.WriteFile(filepath.Join(static, "index.html"), []byte("x"), 0o644)
	td := filepath.Join("..", "..", "internal", "preset", "testdata")
	s, cleanup, err := New(Config{Addr: "127.0.0.1:0", Static: static, Data: filepath.Join(t.TempDir(), "presets"),
		SeedFrom: filepath.Join(td, "2t5-deck-plus-repo-bindings.json"), Live: filepath.Join(td, "live-ld-resolve-bindings.json"), NoPoll: true, Token: "tok123"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	for _, p := range []string{"/api/apps", "/api/dev/frontmost"} {
		body := `{"id":"com.example.A"}`
		if rec := get(t, s.Handler, "POST", p, body); rec.Code != 403 {
			t.Errorf("POST %s without the token = %d", p, rec.Code)
		}
	}
	req := newReq("POST", "/api/apps", strings.NewReader(`{"id":"com.example.A"}`))
	req.Header.Set("X-LD-Token", "tok123")
	rec := httptest.NewRecorder()
	s.Handler.ServeHTTP(rec, req)
	if rec.Code != 201 {
		t.Errorf("with the token = %d %s", rec.Code, rec.Body.String())
	}
	req = newReq("POST", "/api/dev/frontmost", strings.NewReader(`{"id":"com.apple.Safari"}`))
	req.Header.Set("X-LD-Token", "tok123")
	rec = httptest.NewRecorder()
	s.Handler.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("dev route with the token = %d", rec.Code)
	}
}

func TestDevEventsLogHasSwitchesAndArmChanges(t *testing.T) {
	s, _ := testServer(t, false)
	get(t, s.Handler, "POST", "/api/dev/frontmost", `{"id":"com.apple.Safari","name":"Safari"}`)
	time.Sleep(450 * time.Millisecond)
	get(t, s.Handler, "POST", "/api/arm", `{"armed":false}`)
	get(t, s.Handler, "GET", "/api/switches", "") // commits the settled switch
	out := decodeBody(t, get(t, s.Handler, "GET", "/api/events-log", ""))
	var kinds []string
	for _, e := range out["events"].([]any) {
		kinds = append(kinds, e.(map[string]any)["kind"].(string))
	}
	if strings.Join(kinds, ",") != "switch,arm" {
		t.Errorf("kinds = %v", kinds)
	}
}

func TestDevPreflightFakesAreFlippable(t *testing.T) {
	s, _ := testServer(t, false)
	status := func(id string) string {
		out := decodeBody(t, get(t, s.Handler, "GET", "/api/preflight", ""))
		for _, c := range out["checks"].([]any) {
			if m := c.(map[string]any); m["id"] == id {
				return m["status"].(string)
			}
		}
		return "missing"
	}
	if status("ws01") != "ok" || status("ldResolve") != "ok" || status("accessibility") != "ok" {
		t.Fatal("dev defaults are not all ok")
	}
	if rec := get(t, s.Handler, "POST", "/api/dev/preflight", `{"ws01":false,"ldResolveRunning":true}`); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if status("ws01") != "fail" || status("ldResolve") != "warn" {
		t.Error("flip did not show")
	}
	get(t, s.Handler, "POST", "/api/dev/permissions", `{"accessibility":false}`)
	if status("accessibility") != "fail" {
		t.Error("permission flip did not show")
	}
}
