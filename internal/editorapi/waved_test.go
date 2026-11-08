package editorapi

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"2t5-deck-plus/internal/preset"
)

type fakeEngine struct {
	mu sync.Mutex
	st EngineState
}

func (f *fakeEngine) EngineState() EngineState { f.mu.Lock(); defer f.mu.Unlock(); return f.st }
func (f *fakeEngine) WaitApplied(seq int, d time.Duration) EngineState {
	return f.EngineState()
}
func (f *fakeEngine) set(st EngineState) { f.mu.Lock(); f.st = st; f.mu.Unlock() }

type fakePerms PermState

func (f fakePerms) Permissions() PermState { return PermState(f) }

type fakeArm struct{ armed bool }

func (f *fakeArm) Armed() bool     { return f.armed }
func (f *fakeArm) SetArmed(b bool) { f.armed = b }

// envWith builds an env whose Deps the caller may extend.
func envWith(t *testing.T, seed bool, mut func(d *Deps)) *env {
	t.Helper()
	e := newEnv(t, seed)
	d := Deps{Store: e.store, Frontmost: e.front, Bus: e.bus,
		Running: fakeRunning{{ID: "com.google.Chrome", Name: "Google Chrome"}},
		Live:    fakeLive{EngineRunning: true, Sidecar: "connected"}, Simulate: e.sim.Simulate}
	mut(&d)
	e.h = NewHandler(d)
	return e
}

// ---- Host header allow-list --------------------------------------------------------

func TestHostAllowList(t *testing.T) {
	e := newEnv(t, false)
	for _, h := range []string{"127.0.0.1:2541", "127.0.0.1", "localhost:2541", "LOCALHOST", "[::1]:2541", "[::1]", "wails", "wails.localhost", ""} {
		for _, m := range []string{"GET", "POST"} {
			path := "/api/health"
			if m == "POST" {
				path = "/api/undo"
			}
			req := httptest.NewRequest(m, path, nil)
			req.Header.Set("X-LD-Editor", "1")
			req.Host = h
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			if rec.Code != 200 {
				t.Errorf("%s Host %q = %d", m, h, rec.Code)
			}
		}
	}
	for _, h := range []string{"evil.example", "evil.example:2541", "127.0.0.1.evil.example", "localhost.evil.example:80", "example.com",
		"192.168.1.5:2541", "0.0.0.0:2541", "127.0.0.2", "[::2]:1"} {
		for _, m := range []string{"GET", "POST", "PUT", "DELETE"} {
			req := httptest.NewRequest(m, "/api/health", nil)
			req.Host = h
			rec := httptest.NewRecorder()
			e.h.ServeHTTP(rec, req)
			if rec.Code != 403 {
				t.Errorf("%s Host %q = %d, want 403", m, h, rec.Code)
			}
			if !strings.Contains(rec.Body.String(), `"error"`) {
				t.Errorf("no error body: %s", rec.Body.String())
			}
		}
	}
	// extra hosts are honoured
	e2 := envWith(t, false, func(d *Deps) { d.AllowedHosts = []string{"deck.local"} })
	req := httptest.NewRequest("GET", "/api/health", nil)
	req.Host = "deck.local:80"
	rec := httptest.NewRecorder()
	e2.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("extra host = %d", rec.Code)
	}
	// an evil Origin on a good host is still refused (the old rule stays)
	if code, _ := e.do(t, "POST", "/api/undo", "", "Origin", "https://evil.example"); code != 403 {
		t.Errorf("cross-origin = %d", code)
	}
}

// ---- B1: engine / UI divergence ---------------------------------------------------------

func TestMutationReportsAppliedAndComposeFailure(t *testing.T) {
	eng := &fakeEngine{}
	e := envWith(t, false, func(d *Deps) { d.Engine = eng })
	e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`)
	eng.set(EngineState{AppliedSeq: e.store.Snapshot().Seq, AppliedAt: 123})

	rec := e.raw("PUT", "/api/preset/com.google.Chrome/assign", []byte(`{"bank":"base","control":"C1","assignment":{"type":"key","key":"Cmd+T"}}`), "Content-Type", "application/json")
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	// engine did not take it yet (still on the old seq), no error: pending
	body := rec.Body.String()
	if !strings.Contains(body, `"applied":false`) || strings.Contains(body, "composeError") {
		t.Errorf("pending reply = %s", body)
	}
	// compose failed
	eng.set(EngineState{AppliedSeq: 1, ComposeError: "compose: runtime bank produced twice", AppliedAt: 123})
	code, out := e.do(t, "PUT", "/api/preset/com.google.Chrome/value", `{"bank":"base","control":"C1","key":"label","value":"Tab"}`)
	if code != 200 || out["ok"] != true || out["applied"] != false || out["composeError"] != "compose: runtime bank produced twice" {
		t.Fatalf("failed compose reply = %d %v", code, out)
	}
	if out["appliedSeq"] != float64(1) {
		t.Errorf("appliedSeq = %v", out["appliedSeq"])
	}
	// header carries it too
	rec = e.raw("POST", "/api/undo", nil)
	if rec.Header().Get("X-Applied-Seq") != "1" {
		t.Errorf("X-Applied-Seq = %q", rec.Header().Get("X-Applied-Seq"))
	}
	// applied
	seq := e.store.Snapshot().Seq
	eng.set(EngineState{AppliedSeq: seq, AppliedAt: 999})
	code, out = e.do(t, "PUT", "/api/preset/com.google.Chrome/settings", `{"key":"x","value":1}`)
	seq = e.store.Snapshot().Seq
	eng.set(EngineState{AppliedSeq: seq, AppliedAt: 999})
	code, out = e.do(t, "PUT", "/api/preset/com.google.Chrome/settings", `{"key":"x","value":2}`)
	_ = code
	eng.set(EngineState{AppliedSeq: e.store.Snapshot().Seq, AppliedAt: 999})
	code, out = e.do(t, "PUT", "/api/preset/com.google.Chrome/settings", `{"key":"x","value":2}`) // no change: seq stays
	if code != 200 || out["applied"] != true || out["composeError"] != nil {
		t.Errorf("applied reply = %d %v", code, out)
	}

	// GET /api/live engine block
	eng.set(EngineState{AppliedSeq: 3, ComposeError: "boom", AppliedAt: 4242})
	code, live := e.do(t, "GET", "/api/live", "")
	eb, _ := live["engine"].(map[string]any)
	if code != 200 || eb == nil {
		t.Fatalf("live: %d %v", code, live)
	}
	if eb["appliedSeq"] != float64(3) || eb["composeError"] != "boom" || eb["appliedAt"] != float64(4242) ||
		eb["lastSeq"] != float64(e.store.Snapshot().Seq) || eb["readonly"] != false {
		t.Errorf("engine block = %v", eb)
	}
	if _, ok := eb["problems"].([]any); !ok {
		t.Errorf("engine.problems missing: %v", eb)
	}
	// no provider: counts as applied
	e3 := newEnv(t, false)
	_, live = e3.do(t, "GET", "/api/live", "")
	eb, _ = live["engine"].(map[string]any)
	if eb["appliedSeq"] != eb["lastSeq"] || eb["composeError"] != "" {
		t.Errorf("no provider engine block = %v", eb)
	}
	_, out = e3.do(t, "POST", "/api/apps", `{"id":"com.a.b"}`)
	if out["applied"] != true {
		t.Errorf("add app reply = %v", out)
	}
}

// ---- 12: permissions, 9: arm ---------------------------------------------------------------

func TestLivePermissionsAndArm(t *testing.T) {
	arm := &fakeArm{armed: true}
	e := envWith(t, false, func(d *Deps) {
		d.Perms = fakePerms{Accessibility: true, ScreenRecording: false, AutomationFinder: "unknown"}
		d.Arm = arm
	})
	_, live := e.do(t, "GET", "/api/live", "")
	p, _ := live["permissions"].(map[string]any)
	if p["accessibility"] != true || p["screenRecording"] != false || p["automationFinder"] != "unknown" {
		t.Errorf("permissions = %v", p)
	}
	if live["armed"] != true {
		t.Errorf("armed = %v", live["armed"])
	}
	code, out := e.do(t, "POST", "/api/arm", `{"armed":false}`)
	if code != 200 || out["armed"] != false || arm.armed {
		t.Fatalf("disarm: %d %v armed=%v", code, out, arm.armed)
	}
	_, live = e.do(t, "GET", "/api/live", "")
	if live["armed"] != false {
		t.Errorf("live armed = %v", live["armed"])
	}
	if code, out = e.do(t, "GET", "/api/arm", ""); code != 200 || out["armed"] != false {
		t.Errorf("GET arm = %d %v", code, out)
	}
	if code, _ = e.do(t, "POST", "/api/arm", `{}`); code != 400 {
		t.Errorf("missing armed = %d", code)
	}
	if code, _ = e.do(t, "POST", "/api/arm", `{"armed":"yes"}`); code != 400 {
		t.Errorf("bad armed = %d", code)
	}
	if code, out = e.do(t, "POST", "/api/arm", `{"armed":true}`); code != 200 || !arm.armed {
		t.Errorf("re-arm = %d %v", code, out)
	}
	// no provider: 404 on the switch, armed:true and no permissions block in live
	e2 := newEnv(t, false)
	if code, _ = e2.do(t, "POST", "/api/arm", `{"armed":false}`); code != 404 {
		t.Errorf("no switch = %d", code)
	}
	_, live = e2.do(t, "GET", "/api/live", "")
	if live["armed"] != true || live["permissions"] != nil {
		t.Errorf("no providers live = %v", live)
	}
}

// ---- 7: read-only second instance --------------------------------------------------------------

func TestReadOnlyStoreAnswers423(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	opts := preset.Options{LiveResolvePath: "-", PollInterval: -1, Controls: ControlIDs(), Lock: true}
	a, err := preset.Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := preset.Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	h := NewHandler(Deps{Store: b})
	do := func(m, p, body string) (int, string) {
		req := newReq(m, p, strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code, rec.Body.String()
	}
	for _, c := range [][3]string{
		{"POST", "/api/apps", `{"id":"com.google.Chrome"}`},
		{"PUT", "/api/preset/_global/assign", `{"bank":"base","control":"C1","assignment":{"type":"none"}}`},
		{"POST", "/api/backups", ``},
		{"POST", "/api/apps/from-template", `{"templateId":"safari"}`},
	} {
		code, body := do(c[0], c[1], c[2])
		if code != 423 || !strings.Contains(body, `"readonly":true`) || !strings.Contains(body, "process") {
			t.Errorf("%s %s = %d %s", c[0], c[1], code, body)
		}
	}
	code, body := do("GET", "/api/live", "")
	if code != 200 || !strings.Contains(body, `"readonly":true`) || !strings.Contains(body, `"holderPid":`+strconv.Itoa(os.Getpid())) {
		t.Errorf("live: %d %s", code, body)
	}
	if code, _ := do("GET", "/api/apps", ""); code != 200 {
		t.Errorf("reads must keep working: %d", code)
	}
}

// ---- B5 over HTTP ---------------------------------------------------------------------------------

func TestUnreadablePresetWarningsAndRestoreOverHTTP(t *testing.T) {
	e := newEnv(t, false)
	e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`)
	e.do(t, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"C1","assignment":{"type":"key","key":"Cmd+T"}}`)
	bad := filepath.Join(e.store.Dir(), "com.google.Chrome.json")
	os.WriteFile(bad, []byte("{ broken"), 0o644)
	if _, err := e.store.CheckExternal(); err != nil {
		t.Fatal(err)
	}
	code, w := e.do(t, "GET", "/api/warnings", "")
	counts, _ := w["counts"].(map[string]any)
	if code != 200 || counts["error"] != float64(1) {
		t.Fatalf("warnings: %d %v", code, w)
	}
	var found map[string]any
	for _, x := range w["warnings"].([]any) {
		m := x.(map[string]any)
		if m["kind"] == "unreadable-file" {
			found = m
		}
	}
	if found == nil || found["severity"] != "error" || found["field"] != "com.google.Chrome.json" {
		t.Fatalf("warning = %v", found)
	}
	_, live := e.do(t, "GET", "/api/live", "")
	probs := live["engine"].(map[string]any)["problems"].([]any)
	if len(probs) != 1 || probs[0].(map[string]any)["id"] != "com.google.Chrome" || probs[0].(map[string]any)["canRestore"] != true {
		t.Fatalf("engine.problems = %v", probs)
	}
	code, out := e.do(t, "POST", "/api/preset/com.google.Chrome/restore-last-good", "")
	if code != 200 || out["ok"] != true || out["source"] != "memory" {
		t.Fatalf("restore: %d %v", code, out)
	}
	if code, out = e.do(t, "POST", "/api/preset/com.google.Chrome/restore-last-good", ""); code != 409 {
		t.Errorf("second restore = %d %v", code, out)
	}
	if code, _ = e.do(t, "POST", "/api/preset/com.nope.App/restore-last-good", ""); code != 404 {
		t.Errorf("unknown = %d", code)
	}
	_, w = e.do(t, "GET", "/api/warnings", "")
	if w["counts"].(map[string]any)["error"] != float64(0) {
		t.Errorf("error still counted: %v", w["counts"])
	}
	// the restore is one undo step
	_, h := e.do(t, "GET", "/api/history", "")
	entries := h["entries"].([]any)
	last := entries[len(entries)-1].(map[string]any)
	if !strings.Contains(last["label"].(string), "last good") {
		t.Errorf("history label = %v", last["label"])
	}
}

// ---- 10, 11: templates and apps -------------------------------------------------------------------------

func TestTemplatesAndAppRemoval(t *testing.T) {
	e := newEnv(t, true)
	code, out := e.do(t, "GET", "/api/templates", "")
	list, _ := out["templates"].([]any)
	if code != 200 || len(list) < 6 {
		t.Fatalf("templates: %d %v", code, out)
	}
	byID := map[string]map[string]any{}
	for _, x := range list {
		m := x.(map[string]any)
		byID[m["id"].(string)] = m
		if m["name"] == "" || m["bundleId"] == "" || m["summary"] == "" || m["controls"].(float64) < 1 || m["exists"] != false {
			t.Errorf("template row %v", m)
		}
	}
	wantBundle := map[string]string{"google-chrome": "com.google.Chrome", "safari": "com.apple.Safari", "blender": "org.blenderfoundation.blender",
		"adobe-photoshop": "com.adobe.Photoshop", "terminal": "com.apple.Terminal", "iterm2": "com.googlecode.iterm2"}
	for id, b := range wantBundle {
		if byID[id]["bundleId"] != b {
			t.Errorf("%s bundle = %v", id, byID[id]["bundleId"])
		}
	}
	// every template can be created through the real deck-restricted store and validates
	for id := range byID {
		e2 := newEnv(t, false)
		code, row := e2.do(t, "POST", "/api/apps/from-template", `{"templateId":"`+id+`"}`)
		if code != 201 || row["id"] != byID[id]["bundleId"] || row["templateId"] != id || row["controls"] != byID[id]["controls"] {
			t.Errorf("%s: %d %v", id, code, row)
			continue
		}
		_, w := e2.do(t, "GET", "/api/warnings", "")
		for _, x := range w["warnings"].([]any) {
			if m := x.(map[string]any); m["severity"] != "hint" {
				t.Errorf("%s warning %v", id, m)
			}
		}
		_, pr := e2.do(t, "GET", "/api/preset/"+row["id"].(string), "")
		for bn, bm := range pr["effective"].(map[string]any) {
			for cn, ev := range bm.(map[string]any) {
				if r := ev.(map[string]any)["risk"]; r != "safe" && r != "keys" {
					t.Errorf("%s %s.%s risk %v", id, bn, cn, r)
				}
			}
		}
	}
	// refuse existing, unknown, missing id
	if code, out = e.do(t, "POST", "/api/apps/from-template", `{"templateId":"safari"}`); code != 201 {
		t.Fatalf("add safari: %d %v", code, out)
	}
	if code, out = e.do(t, "POST", "/api/apps/from-template", `{"templateId":"safari"}`); code != 409 {
		t.Errorf("duplicate = %d %v", code, out)
	}
	if code, out = e.do(t, "POST", "/api/apps/from-template", `{"templateId":"nope"}`); code != 404 || out["field"] != "templateId" {
		t.Errorf("unknown = %d %v", code, out)
	}
	if code, _ = e.do(t, "POST", "/api/apps/from-template", `{}`); code != 400 {
		t.Errorf("missing = %d", code)
	}
	_, out = e.do(t, "GET", "/api/templates", "")
	for _, x := range out["templates"].([]any) {
		m := x.(map[string]any)
		if m["id"] == "safari" && m["exists"] != true {
			t.Errorf("safari should exist: %v", m)
		}
	}
	// one undo step each
	_, h := e.do(t, "GET", "/api/history", "")
	n := len(h["entries"].([]any))
	e.do(t, "POST", "/api/undo", "")
	if _, ok := e.store.Snapshot().Preset("com.apple.Safari"); ok {
		t.Error("undo kept safari")
	}
	e.do(t, "POST", "/api/redo", "")

	// remove: undoable, never Global or Resolve
	code, out = e.do(t, "DELETE", "/api/apps/com.apple.Safari", "")
	if code != 200 || out["removed"] != "com.apple.Safari" {
		t.Fatalf("remove: %d %v", code, out)
	}
	_, h = e.do(t, "GET", "/api/history", "")
	if len(h["entries"].([]any)) != n+1 {
		t.Errorf("remove is not one step")
	}
	e.do(t, "POST", "/api/undo", "")
	if _, ok := e.store.Snapshot().Preset("com.apple.Safari"); !ok {
		t.Error("undo did not bring safari back")
	}
	for _, id := range []string{"_global", "global", preset.ResolveID} {
		if code, out = e.do(t, "DELETE", "/api/apps/"+id, ""); code != 400 || out["field"] != "id" {
			t.Errorf("remove %s = %d %v", id, code, out)
		}
	}
	if code, _ = e.do(t, "DELETE", "/api/apps/com.nope.App", ""); code != 404 {
		t.Errorf("remove unknown = %d", code)
	}
	if _, ok := e.store.Snapshot().Preset(preset.ResolveID); !ok {
		t.Error("Resolve lost")
	}
}

func TestAddAppValidatesBundleID(t *testing.T) {
	e := newEnv(t, false)
	for _, id := range []string{"notabundle", "", "a b.c", "com..x", "_global", "../x.y"} {
		code, out := e.do(t, "POST", "/api/apps", `{"id":"`+id+`"}`)
		if code != 400 || out["field"] != "id" {
			t.Errorf("id %q = %d %v", id, code, out)
		}
	}
	for _, id := range []string{"com.example.App", "org.x.y"} {
		if code, out := e.do(t, "POST", "/api/apps", `{"id":"`+id+`"}`); code != 201 {
			t.Errorf("id %q = %d %v", id, code, out)
		}
	}
}

// ---- backups, coverage lens, settings, risk -------------------------------------------------------------

func TestManualBackupAndReason(t *testing.T) {
	e := newEnv(t, false)
	e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`)
	code, out := e.do(t, "POST", "/api/backups", "")
	if code != 201 || out["made"] != true {
		t.Fatalf("manual backup: %d %v", code, out)
	}
	_, list := e.do(t, "GET", "/api/backups", "")
	reasons := map[string]bool{}
	for _, b := range list["backups"].([]any) {
		reasons[b.(map[string]any)["reason"].(string)] = true
	}
	if !reasons["manual"] || !reasons["daily"] {
		t.Errorf("reasons = %v", reasons)
	}
}

func TestCoverageLensAndRiskAndFineFactor(t *testing.T) {
	e := newEnv(t, true)
	e.do(t, "PUT", "/api/preset/_global/assign", `{"bank":"base","control":"C1","assignment":{"type":"key","key":"Cmd+Z"}}`)
	e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`)
	e.do(t, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"C1","assignment":{"type":"key","key":"Cmd+W"}}`)
	code, cov := e.do(t, "GET", "/api/coverage?app=com.google.Chrome", "")
	if code != 200 {
		t.Fatal(code, cov)
	}
	var c1 map[string]any
	for _, b := range cov["banks"].([]any) {
		if b.(map[string]any)["bank"] != "base" {
			continue
		}
		for _, c := range b.(map[string]any)["controls"].([]any) {
			if c.(map[string]any)["id"] == "C1" {
				c1 = c.(map[string]any)
			}
		}
	}
	if c1 == nil || c1["overridden"] != true || c1["changedFromDefault"] != true {
		t.Errorf("C1 = %v", c1)
	}
	// effective carries risk
	_, pr := e.do(t, "GET", "/api/preset/com.google.Chrome", "")
	eff := pr["effective"].(map[string]any)["base"].(map[string]any)["C1"].(map[string]any)
	if eff["risk"] != "destructive" {
		t.Errorf("risk = %v", eff["risk"])
	}
	// registry: action risk + settings schema
	_, reg := e.do(t, "GET", "/api/registry", "")
	for _, pk := range reg["packs"].([]any) {
		for _, a := range pk.(map[string]any)["actions"].([]any) {
			if a.(map[string]any)["risk"] == nil {
				t.Errorf("action without risk: %v", a)
			}
		}
	}
	sets, _ := reg["settings"].([]any)
	if len(sets) == 0 {
		t.Fatal("no settings schema")
	}
	ff := sets[0].(map[string]any)
	if ff["key"] != "fineFactor" || ff["unit"] != "× while fine mode is held" || ff["min"] != 0.05 || ff["max"] != float64(1) || ff["default"] != 0.5 {
		t.Errorf("fineFactor schema = %v", ff)
	}
	// round trip through PUT settings, rejected out of range
	rid := preset.ResolveID
	code, out := e.do(t, "PUT", "/api/preset/"+rid+"/settings", `{"key":"fineFactor","value":0.3}`)
	if code != 200 || out["ok"] != true {
		t.Fatalf("fineFactor set: %d %v", code, out)
	}
	_, pr = e.do(t, "GET", "/api/preset/"+rid, "")
	if pr["preset"].(map[string]any)["settings"].(map[string]any)["fineFactor"] != 0.3 {
		t.Errorf("round trip: %v", pr["preset"].(map[string]any)["settings"])
	}
	for _, v := range []string{`0`, `2`, `-1`, `"x"`} {
		if code, out = e.do(t, "PUT", "/api/preset/"+rid+"/settings", `{"key":"fineFactor","value":`+v+`}`); code != 400 || out["field"] != "fineFactor" {
			t.Errorf("fineFactor %s = %d %v", v, code, out)
		}
	}
}
