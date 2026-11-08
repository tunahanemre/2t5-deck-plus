package editorapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"2t5-deck-plus/internal/preset"
)

type fakeFront struct {
	mu  sync.Mutex
	app AppRef
}

func (f *fakeFront) Frontmost() AppRef { f.mu.Lock(); defer f.mu.Unlock(); return f.app }

type fakeRunning []AppRef

func (f fakeRunning) Running() []AppRef { return f }

type fakeLive LiveState

func (f fakeLive) Live() LiveState { return LiveState(f) }

type env struct {
	h     http.Handler
	store *preset.Store
	front *fakeFront
	bus   *Bus
	sim   *Simulator
}

func newEnv(t *testing.T, seed bool) *env {
	t.Helper()
	opts := preset.Options{LiveResolvePath: "-", PollInterval: -1, Controls: ControlIDs()}
	if seed {
		opts.LiveResolvePath = filepath.Join("..", "preset", "testdata", "live-ld-resolve-bindings.json")
		opts.SeedFrom = filepath.Join("..", "preset", "testdata", "2t5-deck-plus-repo-bindings.json")
	}
	st, err := preset.Open(filepath.Join(t.TempDir(), "presets"), opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	e := &env{store: st, front: &fakeFront{app: AppRef{ID: "com.google.Chrome", Name: "Google Chrome"}}, bus: NewBus()}
	e.sim = &Simulator{Store: st, Frontmost: e.front, Bus: e.bus}
	e.h = NewHandler(Deps{Store: st, Frontmost: e.front, Bus: e.bus,
		Running: fakeRunning{{ID: "com.apple.finder", Name: "Finder"}, {ID: "com.google.Chrome", Name: "Google Chrome"}},
		Live:    fakeLive{EngineRunning: true, Sidecar: "connected"}, Simulate: e.sim.Simulate})
	return e
}

func (e *env) do(t *testing.T, method, path, body string, hdr ...string) (int, map[string]any) {
	t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	req := newReq(method, path, rd)
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 && strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		d := json.NewDecoder(rec.Body)
		var v any
		if err := d.Decode(&v); err != nil {
			t.Fatalf("%s %s: bad JSON: %v", method, path, err)
		}
		if m, ok := v.(map[string]any); ok {
			out = m
		} else {
			out = map[string]any{"_list": v}
		}
	}
	return rec.Code, out
}

func TestEndpointsHappyPath(t *testing.T) {
	e := newEnv(t, true)

	code, deck := e.do(t, "GET", "/api/deck", "")
	if code != 200 || deck["layout"] == nil || deck["controls"] == nil {
		t.Fatalf("deck: %d %v", code, deck)
	}
	code, reg := e.do(t, "GET", "/api/registry", "")
	packs := reg["packs"].([]any)
	if code != 200 || len(packs) != 5 {
		t.Fatalf("registry: %d %d packs", code, len(packs))
	}
	code, apps := e.do(t, "GET", "/api/apps", "")
	rows := apps["_list"].([]any)
	if code != 200 || len(rows) != 3 || rows[0].(map[string]any)["isGlobal"] != true || rows[1].(map[string]any)["id"] != preset.ResolveID {
		t.Fatalf("apps: %v", apps)
	}

	// add + capture + duplicate
	code, row := e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`)
	if code != 201 || row["name"] != "Google Chrome" || row["frontmost"] != true {
		t.Fatalf("add: %d %v", code, row)
	}
	if code, _ = e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`); code != 409 {
		t.Errorf("dup add = %d", code)
	}
	if code, _ = e.do(t, "POST", "/api/apps/capture", ""); code != 200 {
		t.Errorf("capture existing = %d", code)
	}
	e.front.app = AppRef{ID: "com.apple.Safari", Name: "Safari"}
	if code, row = e.do(t, "POST", "/api/apps/capture", ""); code != 201 || row["id"] != "com.apple.Safari" {
		t.Errorf("capture new = %d %v", code, row)
	}
	e.front.app = AppRef{}
	if code, _ = e.do(t, "POST", "/api/apps/capture", ""); code != 409 {
		t.Errorf("capture nothing = %d", code)
	}
	code, run := e.do(t, "GET", "/api/running", "")
	if code != 200 || len(run["_list"].([]any)) != 2 {
		t.Errorf("running: %v", run)
	}

	// assign / get / value / settings / undo / redo
	code, r := e.do(t, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"D1","assignment":{"action":"browser.scroll","linesPerDetent":4}}`)
	if code != 200 || r["ok"] != true {
		t.Fatalf("assign: %d %v", code, r)
	}
	eff := r["effective"].(map[string]any)
	if eff["action"] != "browser.scroll" || eff["source"] != "app" {
		t.Errorf("assign effective: %v", eff)
	}
	code, g := e.do(t, "PUT", "/api/preset/global/assign", `{"bank":"base","control":"C1","assignment":{"type":"key","key":"Cmd+Z"}}`)
	if code != 200 {
		t.Fatalf("global assign: %d %v", code, g)
	}
	code, p := e.do(t, "GET", "/api/preset/com.google.Chrome", "")
	if code != 200 {
		t.Fatal(code, p)
	}
	effAll := p["effective"].(map[string]any)["base"].(map[string]any)
	if effAll["D1"] == nil || effAll["C1"].(map[string]any)["source"] != "global" {
		t.Errorf("effective view lacks inherited global control: %v", effAll)
	}
	d1 := effAll["D1"].(map[string]any)["fields"].(map[string]any)
	if d1["linesPerDetent"].(map[string]any)["source"] != "app" || d1["detentThreshold"] == nil {
		t.Errorf("fields: %v", d1)
	}
	if p["preset"].(map[string]any)["id"] != "com.google.Chrome" {
		t.Error("raw preset missing")
	}

	code, r = e.do(t, "PUT", "/api/preset/com.google.Chrome/value", `{"bank":"base","control":"D1","key":"linesPerDetent","value":-2}`)
	if code != 200 {
		t.Fatalf("value: %d %v", code, r)
	}
	code, r = e.do(t, "PUT", "/api/preset/com.google.Chrome/value", `{"bank":"base","control":"D1","key":"linesPerDetent","value":null}`)
	if code != 200 {
		t.Fatalf("value null: %d %v", code, r)
	}
	code, r = e.do(t, "PUT", "/api/preset/_global/settings", `{"key":"actionDefaults","value":{"scrollDelta":{"detentThreshold":3}}}`)
	if code != 200 {
		t.Fatalf("settings: %d %v", code, r)
	}
	code, r = e.do(t, "PUT", "/api/preset/com.google.Chrome/packs", `{"packs":["browser","generic"]}`)
	if code != 200 {
		t.Fatalf("packs: %d %v", code, r)
	}
	code, r = e.do(t, "DELETE", "/api/preset/com.google.Chrome/assign?bank=base&control=D1", "")
	if code != 200 {
		t.Fatalf("unassign: %d %v", code, r)
	}
	code, r = e.do(t, "POST", "/api/undo", "")
	if code != 200 || r["ok"] != true || r["label"] != "Clear D1" {
		t.Errorf("undo: %v", r)
	}
	code, r = e.do(t, "POST", "/api/redo", "")
	if code != 200 || r["ok"] != true {
		t.Errorf("redo: %v", r)
	}
	// nothing left to redo
	if _, r = e.do(t, "POST", "/api/redo", ""); r["ok"] != false {
		t.Errorf("redo past end: %v", r)
	}

	code, live := e.do(t, "GET", "/api/live", "")
	if code != 200 || live["engineRunning"] != true || live["sidecar"] != "connected" || live["activeApp"] != "" && live["frontmost"] == nil {
		t.Errorf("live: %v", live)
	}
}

func TestErrorShapeAndStatuses(t *testing.T) {
	e := newEnv(t, false)
	if c, _ := e.do(t, "POST", "/api/apps", `{"id":"com.a.b"}`); c != 201 {
		t.Fatal(c)
	}
	cases := []struct {
		name, method, path, body string
		code                     int
		field                    string
	}{
		{"bad key spec", "PUT", "/api/preset/com.a.b/assign", `{"bank":"base","control":"C1","assignment":{"type":"key","key":"Cmb+Z"}}`, 400, "key"},
		{"range", "PUT", "/api/preset/com.a.b/assign", `{"bank":"base","control":"D1","assignment":{"type":"scrollDelta","linesPerDetent":900}}`, 400, "linesPerDetent"},
		{"unknown type", "PUT", "/api/preset/com.a.b/assign", `{"bank":"base","control":"D1","assignment":{"type":"warp"}}`, 400, "type"},
		{"not a deck control", "PUT", "/api/preset/com.a.b/assign", `{"bank":"base","control":"Nope","assignment":{"type":"none"}}`, 400, "control"},
		{"no assignment", "PUT", "/api/preset/com.a.b/assign", `{"bank":"base","control":"D1"}`, 400, "assignment"},
		{"bad json", "PUT", "/api/preset/com.a.b/assign", `{`, 400, ""},
		{"no preset", "PUT", "/api/preset/com.zzz/assign", `{"bank":"base","control":"D1","assignment":{"type":"none"}}`, 404, "id"},
		{"get no preset", "GET", "/api/preset/com.zzz", "", 404, "id"},
		{"value on unassigned", "PUT", "/api/preset/com.a.b/value", `{"bank":"base","control":"D1","key":"label","value":"x"}`, 404, "control"},
		{"value no key", "PUT", "/api/preset/com.a.b/value", `{"bank":"base","control":"D1"}`, 400, "key"},
		{"unassign no params", "DELETE", "/api/preset/com.a.b/assign", "", 400, "bank"},
		{"bad pack", "PUT", "/api/preset/com.a.b/packs", `{"packs":["zzz"]}`, 400, "packs"},
		{"bad app id", "POST", "/api/apps", `{"id":"../x"}`, 400, "id"},
		{"simulate bad kind", "POST", "/api/simulate", `{"control":"D1","kind":"wiggle"}`, 400, "kind"},
		{"unknown endpoint", "GET", "/api/nope", "", 404, ""},
		{"wrong method", "DELETE", "/api/deck", "", 404, ""},
	}
	for _, c := range cases {
		code, out := e.do(t, c.method, c.path, c.body)
		if code != c.code {
			t.Errorf("%s: status %d want %d (%v)", c.name, code, c.code, out)
			continue
		}
		if c.code != 0 {
			if out["error"] == nil || out["error"] == "" {
				t.Errorf("%s: no error text: %v", c.name, out)
			}
			if c.field != "" && out["field"] != c.field {
				t.Errorf("%s: field %v want %s", c.name, out["field"], c.field)
			}
		}
	}
}

func TestCrossOriginMutationRefused(t *testing.T) {
	e := newEnv(t, false)
	code, _ := e.do(t, "POST", "/api/undo", "", "Origin", "https://evil.example")
	if code != 403 {
		t.Errorf("cross-origin = %d", code)
	}
	code, _ = e.do(t, "POST", "/api/undo", "", "Origin", "http://127.0.0.1:2541") // the test request host
	if code != 200 {
		t.Errorf("same-origin = %d", code)
	}
	code, _ = e.do(t, "POST", "/api/undo", "", "Origin", "wails://wails")
	if code != 200 {
		t.Errorf("wails origin = %d", code)
	}
}

func TestEventsRingAndSince(t *testing.T) {
	e := newEnv(t, false)
	_, out := e.do(t, "GET", "/api/events?since=0", "")
	if out["next"].(float64) != 0 || len(out["events"].([]any)) != 0 {
		t.Fatalf("empty: %v", out)
	}
	for i := 0; i < 3; i++ {
		e.bus.Publish(Event{Control: "D1", Kind: "turn", RawTicks: i + 1, PresetID: "p", Source: "app", Bank: "b", Action: "a", Out: Out{Text: "t", Value: 1, Unit: "u"}})
	}
	_, out = e.do(t, "GET", "/api/events?since=1", "")
	evs := out["events"].([]any)
	if len(evs) != 2 || out["next"].(float64) != 3 {
		t.Fatalf("since=1: %v", out)
	}
	first := evs[0].(map[string]any)
	for _, k := range []string{"seq", "t", "type", "control", "kind", "rawTicks", "ticksPerSec", "accel", "out", "fine", "presetId", "source", "bank", "action"} {
		if _, ok := first[k]; !ok {
			t.Errorf("event JSON lacks %q", k)
		}
	}
	if first["type"] != "input" || first["seq"].(float64) != 2 {
		t.Errorf("event = %v", first)
	}
	_, out = e.do(t, "GET", "/api/events?since=3", "")
	if len(out["events"].([]any)) != 0 || out["next"].(float64) != 3 {
		t.Errorf("caught up: %v", out)
	}
}

func TestBusRingKeepsLast2000(t *testing.T) {
	b := NewBus()
	for i := 0; i < RingSize+500; i++ {
		b.Publish(Event{Control: "D1"})
	}
	next, evs := b.Since(0)
	if len(evs) != RingSize || next != RingSize+500 || evs[0].Seq != 501 || evs[len(evs)-1].Seq != next {
		t.Errorf("ring: len=%d next=%d first=%d", len(evs), next, evs[0].Seq)
	}
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				b.Publish(Event{})
				b.Since(0)
			}
		}()
	}
	wg.Wait()
}

func TestSimulateUsesRealResolutionAndUnits(t *testing.T) {
	e := newEnv(t, true)
	// Resolve frontmost: D1 is jogMinuteSnap in the Resolve edit bank, but the
	// simulator resolves through ResolveFor (Resolve's banks are legacy), so
	// give Global a scrub and Chrome its own scroll to see both sources.
	e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`)
	e.do(t, "PUT", "/api/preset/_global/assign", `{"bank":"base","control":"D1","assignment":{"action":"resolve.scrubFrames","base":2,"gamma":1,"maxDelta":50}}`)
	e.do(t, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"D2","assignment":{"action":"browser.scroll","linesPerDetent":3,"detentThreshold":1}}`)

	e.front.app = AppRef{ID: "com.google.Chrome", Name: "Google Chrome"}
	code, ev := e.do(t, "POST", "/api/simulate", `{"control":"D2","kind":"turn","ticks":4}`)
	out := ev["out"].(map[string]any)
	if code != 200 || ev["source"] != "app" || ev["action"] != "browser.scroll" || out["unit"] != "lines" || out["value"].(float64) != 12 {
		t.Errorf("chrome D2: %d %v", code, ev)
	}
	for _, bad := range []string{"frame", "minute"} {
		if strings.Contains(strings.ToLower(out["text"].(string)+out["unit"].(string)), bad) {
			t.Errorf("browser output mentions %s", bad)
		}
	}
	code, ev = e.do(t, "POST", "/api/simulate", `{"control":"D1","kind":"turn","ticks":-3}`)
	out = ev["out"].(map[string]any)
	if ev["source"] != "global" || ev["presetId"] != "_global" || out["unit"] != "frames" || out["value"].(float64) != -6 {
		t.Errorf("global D1 fallback: %d %v", code, ev)
	}
	_, ev = e.do(t, "POST", "/api/simulate", `{"control":"C9x","kind":"press"}`)
	if ev["field"] != "control" {
		t.Errorf("unknown control: %v", ev)
	}
	_, ev = e.do(t, "POST", "/api/simulate", `{"control":"Tab","kind":"press"}`)
	if ev["source"] != "none" || ev["out"].(map[string]any)["text"] != "unassigned" {
		t.Errorf("unassigned: %v", ev)
	}
	// events are visible through /api/events
	_, evs := e.do(t, "GET", "/api/events?since=0", "")
	if len(evs["events"].([]any)) != 3 {
		t.Errorf("events = %d", len(evs["events"].([]any)))
	}
}

func TestSimulateDisabledWithoutHook(t *testing.T) {
	st, _ := preset.Open(filepath.Join(t.TempDir(), "p"), preset.Options{LiveResolvePath: "-", PollInterval: -1})
	defer st.Close()
	h := NewHandler(Deps{Store: st})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, newReq("POST", "/api/simulate", strings.NewReader(`{"control":"D1","kind":"press"}`)))
	if rec.Code != 404 {
		t.Errorf("simulate without hook = %d", rec.Code)
	}
	// nil providers are tolerated
	for _, p := range []string{"/api/live", "/api/running", "/api/apps", "/api/events"} {
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, newReq("GET", p, nil))
		if rec.Code != 200 {
			t.Errorf("%s with nil providers = %d", p, rec.Code)
		}
	}
}

func TestDeckProfileEmbedded(t *testing.T) {
	ids := ControlIDs()
	if len(ids) != 62 {
		t.Errorf("controls = %d", len(ids))
	}
	var p struct {
		Layout struct {
			Rects map[string]struct{ X, Y, W, H float64 } `json:"rects"`
		} `json:"layout"`
	}
	if err := json.Unmarshal(DeckJSON(), &p); err != nil || len(p.Layout.Rects) < 50 {
		t.Errorf("layout rects: %d %v", len(p.Layout.Rects), err)
	}
}

func TestSimulateResolveFrontmostUsesLegacyBanksAndFrames(t *testing.T) {
	e := newEnv(t, true)
	e.front.app = AppRef{ID: preset.ResolveID, Name: "DaVinci Resolve"}
	_, ev := e.do(t, "POST", "/api/simulate", `{"control":"ControlDial","kind":"turn","ticks":5}`)
	out := ev["out"].(map[string]any)
	if ev["action"] != "resolve.scrubFrames" || ev["bank"] != "edit" || out["unit"] != "frames" || out["value"].(float64) != 5 {
		t.Errorf("ControlDial: %v", ev)
	}
	_, ev = e.do(t, "POST", "/api/simulate", `{"control":"D1","kind":"turn","ticks":8}`)
	if ev["action"] != "resolve.snapMinute" || ev["out"].(map[string]any)["unit"] != "minutes" {
		t.Errorf("D1: %v", ev)
	}
}

// newReq is httptest.NewRequest with the loopback Host the editor's own page
// sends (the handler refuses other hosts).
func newReq(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.Host = "127.0.0.1:2541"
	r.Header.Set("X-LD-Editor", "1") // the editor's own requests carry it (wave E, F11)
	if body != nil && body != http.NoBody {
		r.Header.Set("Content-Type", "application/json") // callers override
	}
	return r
}
