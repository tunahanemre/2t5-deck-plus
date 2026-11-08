package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"2t5-deck-plus/internal/preset"
)

func testServer(t *testing.T, sim bool) (*Server, string) {
	t.Helper()
	static := t.TempDir()
	if err := os.WriteFile(filepath.Join(static, "index.html"), []byte("<title>editor</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	td := filepath.Join("..", "..", "internal", "preset", "testdata")
	s, cleanup, err := New(Config{
		Addr: "127.0.0.1:0", Static: static, Data: filepath.Join(t.TempDir(), "presets"),
		SeedFrom: filepath.Join(td, "2t5-deck-plus-repo-bindings.json"), Live: filepath.Join(td, "live-ld-resolve-bindings.json"),
		Sim: sim, NoPoll: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)
	return s, static
}

func get(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newReq(method, path, strings.NewReader(body)))
	return rec
}

func TestStaticAndRedirect(t *testing.T) {
	s, _ := testServer(t, false)
	if rec := get(t, s.Handler, "GET", "/", ""); rec.Code != 302 || rec.Header().Get("Location") != "/editor/" {
		t.Errorf("/ -> %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if rec := get(t, s.Handler, "GET", "/editor/", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), "<title>editor</title>") {
		t.Errorf("/editor/ -> %d", rec.Code)
	}
	if rec := get(t, s.Handler, "GET", "/editor", ""); rec.Code != 302 {
		t.Errorf("/editor -> %d", rec.Code)
	}
	if rec := get(t, s.Handler, "GET", "/nope", ""); rec.Code != 404 {
		t.Errorf("/nope -> %d", rec.Code)
	}
	if rec := get(t, s.Handler, "GET", "/editor/../../etc/passwd", ""); rec.Code == 200 {
		t.Errorf("path traversal served")
	}
}

func TestAPIMountedAndSeeded(t *testing.T) {
	s, _ := testServer(t, false)
	rec := get(t, s.Handler, "GET", "/api/apps", "")
	var rows []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil || len(rows) != 3 {
		t.Fatalf("apps: %v %s", err, rec.Body.String())
	}
	// fake running list
	rec = get(t, s.Handler, "GET", "/api/running", "")
	var run []map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &run)
	names := map[string]bool{}
	for _, r := range run {
		names[r["name"]] = true
	}
	for _, n := range []string{"DaVinci Resolve", "Google Chrome", "Finder", "Safari", "Slack"} {
		if !names[n] {
			t.Errorf("running lacks %s", n)
		}
	}
}

func TestDevFrontmostChangesLiveAndApps(t *testing.T) {
	s, _ := testServer(t, false)
	live := func() map[string]any {
		var m map[string]any
		_ = json.Unmarshal(get(t, s.Handler, "GET", "/api/live", "").Body.Bytes(), &m)
		return m
	}
	l := live()
	if l["frontmost"].(map[string]any)["id"] != preset.ResolveID || l["sidecar"] != "connected" || l["activeApp"] != preset.ResolveID {
		t.Errorf("initial live = %v", l)
	}
	if rec := get(t, s.Handler, "POST", "/api/dev/frontmost", `{"id":"com.google.Chrome","name":"Google Chrome"}`); rec.Code != 200 {
		t.Fatalf("set frontmost = %d %s", rec.Code, rec.Body)
	}
	l = live()
	if l["frontmost"].(map[string]any)["id"] != "com.google.Chrome" || l["sidecar"] != "waiting" || l["activeApp"] != preset.GlobalID {
		t.Errorf("after switch live = %v", l)
	}
	// capture adds the (now frontmost) Chrome
	if rec := get(t, s.Handler, "POST", "/api/apps/capture", ""); rec.Code != 201 {
		t.Errorf("capture = %d %s", rec.Code, rec.Body)
	}
	if l = live(); l["activeApp"] != "com.google.Chrome" {
		t.Errorf("activeApp after capture = %v", l["activeApp"])
	}
	// unknown app joins the running list
	get(t, s.Handler, "POST", "/api/dev/frontmost", `{"id":"com.new.Thing"}`)
	if !strings.Contains(get(t, s.Handler, "GET", "/api/running", "").Body.String(), "com.new.Thing") {
		t.Error("new frontmost app not in running list")
	}
	if rec := get(t, s.Handler, "POST", "/api/dev/frontmost", `{"name":"x"}`); rec.Code != 400 {
		t.Errorf("no id = %d", rec.Code)
	}
	rec := httptest.NewRecorder()
	req := newReq("POST", "/api/dev/frontmost", strings.NewReader(`{"id":"a.b"}`))
	req.Header.Set("Origin", "https://evil.example")
	s.Handler.ServeHTTP(rec, req)
	if rec.Code != 403 {
		t.Errorf("cross-origin dev frontmost = %d", rec.Code)
	}
}

func TestSimulateEndpointAndSimLoop(t *testing.T) {
	s, _ := testServer(t, true)
	rec := get(t, s.Handler, "POST", "/api/simulate", `{"control":"D1","kind":"turn","ticks":5}`)
	if rec.Code != 200 {
		t.Fatalf("simulate = %d %s", rec.Code, rec.Body)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.StartSim(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		_, evs := s.Bus.Since(1)
		if len(evs) >= 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("simulator published nothing")
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
}

func TestSimDisabledDoesNotRun(t *testing.T) {
	s, _ := testServer(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.StartSim(ctx)
	time.Sleep(200 * time.Millisecond)
	if next, _ := s.Bus.Since(0); next != 0 {
		t.Errorf("events without --sim: %d", next)
	}
}

func TestLoopbackOnly(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:2541": true, "localhost:2541": true, "[::1]:2541": true,
		":2541": false, "0.0.0.0:2541": false, "192.168.1.5:2541": false, "garbage": false,
	} {
		if (loopbackOnly(addr) == nil) != ok {
			t.Errorf("%s ok=%v", addr, ok)
		}
	}
	if _, _, err := New(Config{Addr: "0.0.0.0:2541"}); err == nil {
		t.Error("New accepted a non-loopback address")
	}
}

func TestRunRejectsBadAddr(t *testing.T) {
	var sb strings.Builder
	if err := run([]string{"--addr", "0.0.0.0:2599"}, &sb); err == nil {
		t.Error("run accepted 0.0.0.0")
	}
}

func TestWaveBEndpointsServed(t *testing.T) {
	s, _ := testServer(t, false)
	rec := get(t, s.Handler, "GET", "/api/appicon/com.google.Chrome?size=48", "")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || rec.Header().Get("ETag") == "" {
		t.Errorf("fake icon: %d %v", rec.Code, rec.Header())
	}
	if rec := get(t, s.Handler, "GET", "/api/icon/com.example.x.noicon.png", ""); rec.Code != 404 {
		t.Errorf("noicon = %d", rec.Code)
	}
	for _, p := range []string{"/api/coverage?app=_global", "/api/search?q=finder", "/api/history", "/api/warnings", "/api/backups", "/api/snapshots", "/api/export?app=all"} {
		if rec := get(t, s.Handler, "GET", p, ""); rec.Code != 200 {
			t.Errorf("%s = %d", p, rec.Code)
		}
	}
}

func TestDevServesWaveCEndpoints(t *testing.T) {
	s, _ := testServer(t, false)
	why := func() map[string]any {
		var m map[string]any
		_ = json.Unmarshal(get(t, s.Handler, "GET", "/api/live", "").Body.Bytes(), &m)
		return m["why"].(map[string]any)
	}
	w := why()
	if w["reason"] != "resolve-cascade" || w["since"].(float64) <= 0 {
		t.Fatalf("initial why = %v", w)
	}
	get(t, s.Handler, "POST", "/api/dev/frontmost", `{"id":"com.google.Chrome","name":"Google Chrome"}`)
	if w = why(); w["reason"] != "no-preset-global-fallback" {
		t.Fatalf("chrome why = %v", w)
	}
	// dev switch: editor focused
	if rec := get(t, s.Handler, "POST", "/api/dev/frontmost", `{"editorFocused":true}`); rec.Code != 200 {
		t.Fatalf("editorFocused = %d %s", rec.Code, rec.Body)
	}
	if w = why(); w["reason"] != "editor-focused-kept-last" {
		t.Fatalf("focused why = %v", w)
	}
	get(t, s.Handler, "POST", "/api/dev/frontmost", `{"editorFocused":false}`)

	rec := get(t, s.Handler, "POST", "/api/curve", `{"action":"browser.scroll","fields":{"velocityProfile":"standard"}}`)
	var c map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &c)
	if rec.Code != 200 || len(c["points"].([]any)) != 25 || c["unit"] != "lines" {
		t.Fatalf("curve = %d %s", rec.Code, rec.Body)
	}
	if rec := get(t, s.Handler, "GET", "/api/defaults/diff?app="+preset.ResolveID, ""); rec.Code != 200 {
		t.Fatalf("defaults diff = %d %s", rec.Code, rec.Body)
	}
}

// newReq is httptest.NewRequest with the loopback Host the page itself sends.
func newReq(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.Host = "127.0.0.1:2541"
	r.Header.Set("X-LD-Editor", "1") // the editor's own requests carry it (wave E, F11)
	if body != nil && body != http.NoBody {
		r.Header.Set("Content-Type", "application/json") // callers override
	}
	return r
}
