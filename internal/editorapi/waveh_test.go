package editorapi

import (
	"errors"
	"os"
	"path/filepath"
	"sync"

	"2t5-deck-plus/internal/preset"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- H1: per-launch API token ---------------------------------------------------------

const testTok = "0123456789abcdef0123456789abcdef"

func tokenEnv(t *testing.T) *env {
	return envWith(t, false, func(d *Deps) { d.Token = testTok })
}

func rawReq(e *env, method, path, body string, hdr map[string]string, host string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Host = host
	r.Header.Set("X-LD-Editor", "1")
	if body != "" {
		r.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func TestToken_MutationsNeedItGetsDoNot(t *testing.T) {
	e := tokenEnv(t)
	body := `{"id":"com.example.Tok"}`
	for name, hdr := range map[string]map[string]string{
		"none":      nil,
		"wrong":     {"X-LD-Token": testTok[:31] + "x"},
		"empty":     {"X-LD-Token": ""},
		"prefix":    {"X-LD-Token": testTok[:16]},
		"too long":  {"X-LD-Token": testTok + "0"},
		"lowercase": {"X-LD-Token": strings.ToUpper(testTok)},
	} {
		rec := rawReq(e, "POST", "/api/apps", body, hdr, "127.0.0.1:2541")
		if rec.Code != 403 || !strings.Contains(rec.Body.String(), "X-LD-Token") {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body.String())
		}
	}
	if _, ok := e.store.Snapshot().Preset("com.example.Tok"); ok {
		t.Fatal("a refused request changed the store")
	}
	if rec := rawReq(e, "POST", "/api/apps", body, map[string]string{"X-LD-Token": testTok}, "127.0.0.1:2541"); rec.Code != 201 {
		t.Errorf("right token: %d %s", rec.Code, rec.Body.String())
	}
	// reads need no token
	for _, p := range []string{"/api/live", "/api/apps", "/api/warnings"} {
		if rec := rawReq(e, "GET", p, "", nil, "127.0.0.1:2541"); rec.Code != 200 {
			t.Errorf("GET %s without a token = %d", p, rec.Code)
		}
	}
	// the other mutation checks still apply with the token
	if rec := rawReq(e, "POST", "/api/apps", body, map[string]string{"X-LD-Token": testTok, "X-LD-Editor": ""}, "127.0.0.1:2541"); rec.Code != 403 {
		t.Errorf("token without the editor header = %d", rec.Code)
	}
}

func TestToken_EmptyHostRefusedOnMutationsOnlyWhenConfigured(t *testing.T) {
	e := tokenEnv(t)
	rec := rawReq(e, "POST", "/api/undo", "", map[string]string{"X-LD-Token": testTok}, "")
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), "Host") {
		t.Errorf("empty Host + token = %d %s", rec.Code, rec.Body.String())
	}
	if rec := rawReq(e, "GET", "/api/health", "", nil, ""); rec.Code != 200 {
		t.Errorf("GET with an empty Host = %d (reads stay allowed)", rec.Code)
	}
	// without a configured token the old behaviour: an empty Host is allowed
	plain := newEnv(t, false)
	if rec := rawReq(plain, "POST", "/api/undo", "", nil, ""); rec.Code != 200 {
		t.Errorf("no token configured, empty Host = %d", rec.Code)
	}
}

func TestToken_NotConfiguredChangesNothing(t *testing.T) {
	e := newEnv(t, false)
	if code, out := e.do(t, "POST", "/api/apps", `{"id":"com.example.NoTok"}`); code != 201 {
		t.Errorf("%d %v", code, out)
	}
}

func TestToken_CheckMutationTokenIsConstantTimeShape(t *testing.T) {
	r := httptest.NewRequest("POST", "/x", nil)
	r.Host = "127.0.0.1:1"
	r.Header.Set("X-LD-Editor", "1")
	if code, _ := CheckMutationToken(r, ""); code != 0 {
		t.Errorf("no token configured = %d", code)
	}
	if code, _ := CheckMutationToken(r, testTok); code != 403 {
		t.Errorf("missing = %d", code)
	}
	r.Header.Set(TokenHeader, testTok)
	if code, msg := CheckMutationToken(r, testTok); code != 0 {
		t.Errorf("right = %d %s", code, msg)
	}
}

// ---- H2 / H3: the HTTP import in two steps; import without programs ----------------------

func importTokenOf(t *testing.T, e *env, zip []byte, q string) (string, map[string]any) {
	t.Helper()
	rec := e.raw("POST", "/api/import?dryRun=1"+q, zip, "Content-Type", "application/zip")
	if rec.Code != 200 {
		t.Fatalf("dry run: %d %s", rec.Code, rec.Body.String())
	}
	m := decodeMap(t, rec)
	tok, _ := m["token"].(string)
	if tok == "" || m["dryRun"] != true {
		t.Fatalf("dry run reply: %v", m)
	}
	return tok, m
}

func TestImportToken_TheRealApplyNeedsAPreviewedToken(t *testing.T) {
	e := newEnv(t, false)
	zip := shellZip(t, e)
	rec := e.raw("POST", "/api/import?confirmRisk=1", zip, "Content-Type", "application/zip")
	m := decodeMap(t, rec)
	if rec.Code != 409 || m["needsToken"] != true || m["files"] == nil {
		t.Fatalf("tokenless real import = %d %v", rec.Code, m)
	}
	if _, ok := e.store.Snapshot().Preset("com.evil.App"); ok {
		t.Fatal("a tokenless import installed")
	}
	for _, tok := range []string{"nope", "0000000000000000000000000000000f"} {
		rec = e.raw("POST", "/api/import?confirmRisk=1&token="+tok, nil)
		if rec.Code != 409 || decodeMap(t, rec)["needsToken"] != true {
			t.Errorf("unknown token %q = %d %s", tok, rec.Code, rec.Body.String())
		}
	}
	tok, _ := importTokenOf(t, e, zip, "")
	if rec = e.raw("POST", "/api/import?confirmRisk=1&token="+tok, nil); rec.Code != 200 {
		t.Fatalf("apply = %d %s", rec.Code, rec.Body.String())
	}
	// single use
	if rec = e.raw("POST", "/api/import?confirmRisk=1&token="+tok, nil); rec.Code != 409 {
		t.Errorf("a used token applied again: %d", rec.Code)
	}
	// the token applies the PREVIEWED bytes, whatever body comes along
	e2 := newEnv(t, false)
	tok, _ = importTokenOf(t, e2, zip, "")
	other := zipBody(t, map[string]string{"manifest.json": `{"kind":"2t5-deck-plus-presets","schema":1}`})
	if rec = e2.raw("POST", "/api/import?confirmRisk=1&token="+tok, other); rec.Code != 200 {
		t.Fatalf("apply with a different body = %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := e2.store.Snapshot().Preset("com.evil.App"); !ok {
		t.Error("the previewed zip was not what got applied")
	}
}

func TestImportToken_SlotAndTTLCaps(t *testing.T) {
	n := &NativeIO{}
	clock := t0
	n.Now = func() time.Time { return clock }
	e := newEnv(t, false)
	n.Store = e.store
	e2 := envWith(t, false, func(d *Deps) { d.Native = n })
	zip := shellZip(t, e)
	var toks []string
	for i := 0; i < 5; i++ {
		clock = clock.Add(time.Second)
		tok, _ := importTokenOf(t, e2, zip, "")
		toks = append(toks, tok)
	}
	if rec := e2.raw("POST", "/api/import?confirmRisk=1&token="+toks[0], nil); rec.Code != 409 {
		t.Errorf("the oldest of 5 previews survived the 4-slot cap: %d", rec.Code)
	}
	clock = clock.Add(11 * time.Minute)
	if rec := e2.raw("POST", "/api/import?confirmRisk=1&token="+toks[4], nil); rec.Code != 409 {
		t.Errorf("an expired token applied: %d", rec.Code)
	}
}

func TestImportToken_SharedWithTheNativeStore(t *testing.T) {
	e := newEnv(t, false)
	n := &NativeIO{Store: e.store}
	e2 := envWith(t, false, func(d *Deps) { d.Native = n })
	zip := shellZip(t, e)
	tok, _ := importTokenOf(t, e2, zip, "")
	if _, err := n.ApplyImport(tok, true); err != nil { // the native door redeems an HTTP preview
		t.Fatalf("native apply of an HTTP token: %v", err)
	}
}

func TestImportSkipRisky_HTTPDryRunAndApply(t *testing.T) {
	e := newEnv(t, false)
	zip := shellZip(t, e)
	tok, m := importTokenOf(t, e, zip, "&skipRisky=1")
	dr, _ := m["dropped"].([]any)
	if len(dr) != 1 || m["needsConfirm"] != false {
		t.Fatalf("dry run: dropped=%v needsConfirm=%v", m["dropped"], m["needsConfirm"])
	}
	row := dr[0].(map[string]any)
	if row["preset"] != "com.evil.App" || row["bank"] != "base" || row["control"] != "C1" || row["action"] == "" {
		t.Errorf("dropped row = %v", row)
	}
	// without skipRisky and without confirmRisk the same token is refused (risk), and the token survives
	if rec := e.raw("POST", "/api/import?token="+tok, nil); rec.Code != 409 || decodeMap(t, rec)["needsConfirm"] != true {
		t.Fatalf("risky apply = %d %s", rec.Code, rec.Body.String())
	}
	rec := e.raw("POST", "/api/import?skipRisky=1&token="+tok, nil)
	out := decodeMap(t, rec)
	if rec.Code != 200 || len(out["dropped"].([]any)) != 1 {
		t.Fatalf("skipRisky apply = %d %v", rec.Code, out)
	}
	p, ok := e.store.Snapshot().Preset("com.evil.App")
	if !ok || p.Assigned() != 0 {
		t.Errorf("preset = %v ok=%v (the shell assignment must not be installed)", p, ok)
	}
	if code, r := e.do(t, "POST", "/api/undo", ""); code != 200 || r["label"] != "Import presets" {
		t.Errorf("not one undo step: %v", r)
	}
}

// ---- H7: the events ring ---------------------------------------------------------------

func TestEventLog_RingKeeps50NewestFirst(t *testing.T) {
	l := NewEventLog()
	for i := 0; i < 60; i++ {
		l.Add("arm", fmt.Sprintf("e%d", i), "test", nil)
	}
	ev := l.List()
	if len(ev) != 50 || ev[0].Text != "e59" || ev[49].Text != "e10" {
		t.Fatalf("len=%d first=%s last=%s", len(ev), ev[0].Text, ev[49].Text)
	}
	var nl *EventLog
	nl.Add("x", "y", "", nil)
	if got := nl.List(); got == nil || len(got) != 0 {
		t.Errorf("nil log = %v", got)
	}
}

func TestEventLog_ShellRecordsTheBaseNameNeverArguments(t *testing.T) {
	if got := ShellBase("/usr/bin/open"); got != "open" {
		t.Errorf("ShellBase = %q", got)
	}
	if got := ShellBase("  /opt/x/my tool  "); got != "my tool" {
		t.Errorf("ShellBase = %q", got)
	}
	if ShellBase("") != "" {
		t.Error("empty")
	}
}

func TestEventLog_HandlerRecordsArmImportRestoreBackup(t *testing.T) {
	ring := NewEventLog()
	arm := &fakeArm{armed: true}
	e := envWith(t, false, func(d *Deps) { d.Events = ring; d.Arm = arm; d.Native = &NativeIO{} })
	e.h = NewHandler(Deps{Store: e.store, Frontmost: e.front, Bus: e.bus, Events: ring, Arm: arm, Live: fakeLive{EngineRunning: true}})
	mustPost := func(path, body string) {
		t.Helper()
		if code, out := e.do(t, "POST", path, body); code/100 != 2 {
			t.Fatalf("%s: %d %v", path, code, out)
		}
	}
	mustPost("/api/arm", `{"armed":false}`)
	mustPost("/api/arm", `{"armed":false}`) // no change: no event
	mustPost("/api/arm", `{"armed":true}`)
	mustPost("/api/apps", `{"id":"com.example.Ring"}`)
	mustPost("/api/backups", ``)
	code, out := e.do(t, "GET", "/api/events-log", "")
	if code != 200 || out["keep"].(float64) != 50 {
		t.Fatalf("events-log = %d %v", code, out)
	}
	var kinds []string
	for _, x := range out["events"].([]any) {
		kinds = append(kinds, x.(map[string]any)["kind"].(string))
	}
	if strings.Join(kinds, ",") != "backup,arm,arm" {
		t.Errorf("kinds (newest first) = %v", kinds)
	}
	first := out["events"].([]any)[1].(map[string]any)
	if first["text"] != "armed" || first["source"] != "editor" || first["t"] == nil {
		t.Errorf("arm event = %v", first)
	}
	// import and restore
	zip := shellZip(t, e)
	tok, _ := importTokenOf(t, e, zip, "&skipRisky=1")
	if rec := e.raw("POST", "/api/import?skipRisky=1&token="+tok, nil); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	bs := e.store.Backups()
	if len(bs) == 0 {
		t.Fatal("no backup")
	}
	mustPost("/api/backups/"+bs[0].Name+"/restore", ``)
	_, out = e.do(t, "GET", "/api/events-log", "")
	kinds = nil
	for _, x := range out["events"].([]any) {
		kinds = append(kinds, x.(map[string]any)["kind"].(string))
	}
	if kinds[0] != "restore" || kinds[1] != "import" {
		t.Errorf("kinds = %v", kinds)
	}
}

func TestEventLog_SwitchesAreCopiedAndSwitchLogUnchanged(t *testing.T) {
	ring := NewEventLog()
	l := NewSwitchLog()
	l.OnSwitch = ring.AddSwitch
	l.Note(ms(0), obs(chromeID, chromeID))
	l.Note(ms(1000), obs("com.apple.finder", "com.apple.finder"))
	sn := l.Snapshot(ms(1500))
	ev := ring.List()
	if len(sn.Log) != 1 || len(ev) != 1 || ev[0].Kind != "switch" || ev[0].Detail["to"] != "com.apple.finder" {
		t.Errorf("switch log %v ring %v", sn.Log, ev)
	}
}

// ---- H6: preflight ---------------------------------------------------------------------

type fakeEnv struct {
	mounted bool
	detail  string
	resolve bool
}

func (f *fakeEnv) WS01() (bool, string)   { return f.mounted, f.detail }
func (f *fakeEnv) LDResolveRunning() bool { return f.resolve }

func preflight(t *testing.T, e *env) (map[string]map[string]any, []string, map[string]any) {
	t.Helper()
	code, out := e.do(t, "GET", "/api/preflight", "")
	if code != 200 {
		t.Fatalf("preflight = %d %v", code, out)
	}
	by := map[string]map[string]any{}
	var order []string
	for _, c := range out["checks"].([]any) {
		m := c.(map[string]any)
		for _, k := range []string{"id", "label", "status", "detail", "fix"} {
			if _, ok := m[k]; !ok {
				t.Errorf("check lacks %q: %v", k, m)
			}
		}
		by[m["id"].(string)] = m
		order = append(order, m["id"].(string))
	}
	return by, order, out
}

func TestPreflight_OrderedChecksAndStatuses(t *testing.T) {
	arm := &fakeArm{armed: true}
	eng := &fakeEngine{}
	eng.set(EngineState{AppliedSeq: 0})
	env := &fakeEnv{mounted: true, detail: "mounted"}
	e := envWith(t, false, func(d *Deps) {
		d.Arm = arm
		d.Engine = eng
		d.Perms = fakePerms{Accessibility: true, ScreenRecording: true, AutomationFinder: "granted"}
		d.Env = env
		d.Live = fakeLive{EngineRunning: true, Sidecar: "connected"}
	})
	eng.set(EngineState{AppliedSeq: e.store.Snapshot().Seq})
	by, order, out := preflight(t, e)
	want := []string{"accessibility", "screenRecording", "automationFinder", "ws01", "presetsDir", "canary", "engineApplied", "sidecar", "ldResolve", "armed", "disarmedState", "pendingExternal"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Fatalf("order = %v", order)
	}
	for id, c := range by {
		want := "ok"
		if id == "presetsDir" {
			want = "warn" // this test store runs without the folder lock (TestPreflight_ReadOnly... covers it)
		}
		if id == "disarmedState" {
			want = "unknown" // no marker writer in this test
		}
		if c["status"] != want {
			t.Errorf("%s = %v (%v)", id, c["status"], c["detail"])
		}
	}
	if out["ok"] != true {
		t.Errorf("ok = %v", out["ok"])
	}
	// flip things
	eng.set(EngineState{AppliedSeq: 0, ComposeError: "boom"})
	env.mounted, env.detail, env.resolve = false, "not mounted", true
	arm.armed = false
	e.h = NewHandler(Deps{Store: e.store, Frontmost: e.front, Bus: e.bus, Arm: arm, Engine: eng, Env: env,
		Perms: fakePerms{Accessibility: false, ScreenRecording: false, AutomationFinder: "denied"},
		Live:  fakeLive{EngineRunning: true, Sidecar: "retrying"}})
	by, _, out = preflight(t, e)
	for id, st := range map[string]string{"accessibility": "fail", "screenRecording": "warn", "automationFinder": "warn", "ws01": "fail",
		"engineApplied": "fail", "sidecar": "warn", "ldResolve": "warn", "armed": "ok"} {
		if by[id]["status"] != st {
			t.Errorf("%s = %v, want %s (%v)", id, by[id]["status"], st, by[id]["detail"])
		}
	}
	if out["ok"] != false {
		t.Errorf("ok = %v with failures", out["ok"])
	}
	if !strings.Contains(by["ldResolve"]["detail"].(string), "MIDI") {
		t.Errorf("ldResolve detail = %v", by["ldResolve"]["detail"])
	}
	if by["accessibility"]["fix"] == "" || by["ws01"]["fix"] == "" {
		t.Error("a failing check without a fix")
	}
	c := out["counts"].(map[string]any)
	if c["fail"].(float64) < 3 {
		t.Errorf("counts = %v", c)
	}
}

func TestPreflight_UnknownWithoutProviders(t *testing.T) {
	e := newEnv(t, false)
	by, _, _ := preflight(t, e)
	for _, id := range []string{"accessibility", "screenRecording", "automationFinder", "ws01", "ldResolve", "armed"} {
		if by[id]["status"] != "unknown" {
			t.Errorf("%s = %v without a provider", id, by[id]["status"])
		}
	}
}

func TestPreflight_StillComposingIsWarnAndCanaryIsWarn(t *testing.T) {
	eng := &fakeEngine{}
	eng.set(EngineState{AppliedSeq: -1})
	e, _ := canaryEnv(t)
	e.h = NewHandler(Deps{Store: e.store, Frontmost: e.front, Bus: e.bus, Engine: eng})
	by, _, _ := preflight(t, e)
	if by["canary"]["status"] != "warn" || !strings.Contains(by["canary"]["detail"].(string), "assignments") {
		t.Errorf("canary = %v", by["canary"])
	}
	if by["engineApplied"]["status"] != "warn" {
		t.Errorf("engineApplied = %v (behind but no error)", by["engineApplied"])
	}
}

func TestPreflight_ReadOnlyStoreFailsThePresetsCheck(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	holder, err := preset.Open(dir, preset.Options{LiveResolvePath: "-", PollInterval: -1, Lock: true})
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	ro, err := preset.Open(dir, preset.Options{LiveResolvePath: "-", PollInterval: -1, Lock: true})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	e := newEnv(t, false)
	e.h = NewHandler(Deps{Store: ro, Frontmost: e.front, Bus: e.bus})
	by, _, _ := preflight(t, e)
	if by["presetsDir"]["status"] != "fail" || !strings.Contains(by["presetsDir"]["detail"].(string), "another") {
		t.Errorf("presetsDir (read-only) = %v", by["presetsDir"])
	}
	e.h = NewHandler(Deps{Store: holder, Frontmost: e.front, Bus: e.bus})
	by, _, _ = preflight(t, e)
	if by["presetsDir"]["status"] != "ok" || !strings.Contains(by["presetsDir"]["detail"].(string), "lock") {
		t.Errorf("presetsDir (holder) = %v", by["presetsDir"])
	}
}

func TestMountState(t *testing.T) {
	dir := t.TempDir()
	if ok, d := MountState(dir, ".sentinel"); ok || !strings.Contains(d, "sentinel") {
		// a temp dir is on the boot disk: refused whatever the sentinel says
		t.Errorf("boot-disk dir = %v %q", ok, d)
	}
	if ok, d := MountState(filepath.Join(dir, "missing"), ".sentinel"); ok || d == "" {
		t.Errorf("missing = %v %q", ok, d)
	}
}

func TestLive_ResolveProjectFromTheEngineStatus(t *testing.T) {
	e := envWith(t, false, func(d *Deps) { d.Live = fakeLive{EngineRunning: true, Sidecar: "connected", Project: "SANDBOX-TEST"} })
	_, live := e.do(t, "GET", "/api/live", "")
	if live["resolveProject"] != "SANDBOX-TEST" {
		t.Errorf("resolveProject = %v", live["resolveProject"])
	}
	e = newEnv(t, false)
	_, live = e.do(t, "GET", "/api/live", "")
	if v, ok := live["resolveProject"]; !ok || v != "" {
		t.Errorf("resolveProject without a title = %v %v (always present, empty)", v, ok)
	}
}

func TestAPI_BackupLabelDiffAndCopyDryRun(t *testing.T) {
	e := newEnv(t, false)
	for _, id := range []string{"com.google.Chrome", "com.apple.Safari"} {
		if code, out := e.do(t, "POST", "/api/apps", `{"id":"`+id+`"}`); code != 201 {
			t.Fatal(code, out)
		}
	}
	e.do(t, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"C1","assignment":{"type":"key","key":"Cmd+T"}}`)
	// labelled backup
	code, out := e.do(t, "POST", "/api/backups", `{"label":"before the shoot"}`)
	if code != 201 || out["backup"].(map[string]any)["label"] != "before the shoot" {
		t.Fatalf("labelled backup = %d %v", code, out)
	}
	name := out["backup"].(map[string]any)["name"].(string)
	_, list := e.do(t, "GET", "/api/backups", "")
	found := false
	for _, b := range list["backups"].([]any) {
		if m := b.(map[string]any); m["name"] == name && m["label"] == "before the shoot" {
			found = true
		}
	}
	if !found {
		t.Errorf("label not listed: %v", list)
	}
	// a body-less POST still works
	if code, _ := e.do(t, "POST", "/api/backups", ""); code != 201 {
		t.Errorf("body-less backup = %d", code)
	}
	// diff
	e.do(t, "DELETE", "/api/preset/com.google.Chrome/assign?bank=base&control=C1", "")
	code, out = e.do(t, "GET", "/api/backups/"+name+"/diff", "")
	if code != 200 {
		t.Fatalf("diff = %d %v", code, out)
	}
	d := out["diff"].(map[string]any)
	var chromeRow map[string]any
	for _, f := range d["files"].([]any) {
		if m := f.(map[string]any); m["id"] == "com.google.Chrome" {
			chromeRow = m
		}
	}
	if chromeRow == nil || chromeRow["status"] != "changed" || chromeRow["added"].(float64) != 1 || d["label"] != "before the shoot" {
		t.Errorf("diff = %v", d)
	}
	if code, _ := e.do(t, "GET", "/api/snapshots/"+name+"/diff", ""); code != 200 {
		t.Errorf("snapshots alias = %d", code)
	}
	if code, _ := e.do(t, "GET", "/api/backups/presets-19990101-000000-manual.zip/diff", ""); code != 404 {
		t.Errorf("unknown = %d", code)
	}
	// copy dry run
	before := e.store.Snapshot().Seq
	code, out = e.do(t, "POST", "/api/preset/com.apple.Safari/copy", `{"from":"com.google.Chrome","scope":"all","dryRun":true}`)
	if code != 200 || out["dryRun"] != true || out["copied"].(float64) != 0 {
		t.Fatalf("copy dry run = %d %v", code, out)
	}
	e.do(t, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"C2","assignment":{"type":"key","key":"Cmd+Y"}}`)
	before = e.store.Snapshot().Seq
	code, out = e.do(t, "POST", "/api/preset/com.apple.Safari/copy?dryRun=1", `{"from":"com.google.Chrome","scope":"all"}`)
	if code != 200 || out["copied"].(float64) != 1 || out["skippedByReason"] == nil {
		t.Fatalf("copy dry run 2 = %d %v", code, out)
	}
	if e.store.Snapshot().Seq != before {
		t.Error("the dry run wrote")
	}
	if p, _ := e.store.Snapshot().Preset("com.apple.Safari"); p.Assigned() != 0 {
		t.Error("the dry run copied")
	}
}

func TestSimulator_ShowsLatencyForPostedActionsOnly(t *testing.T) {
	e := newEnv(t, false)
	if code, out := e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`); code != 201 {
		t.Fatal(code, out)
	}
	e.do(t, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"C1","assignment":{"type":"key","key":"Cmd+T"}}`)
	code, ev := e.do(t, "POST", "/api/simulate", `{"control":"C1","kind":"press"}`)
	if code != 200 || ev["latencyMs"] == nil || ev["latencyMs"].(float64) <= 0 {
		t.Errorf("press event = %d %v", code, ev)
	}
	_, ev = e.do(t, "POST", "/api/simulate", `{"control":"C1","kind":"release"}`)
	if _, has := ev["latencyMs"]; has {
		t.Errorf("release has latency: %v", ev)
	}
	_, ev = e.do(t, "POST", "/api/simulate", `{"control":"C2","kind":"press"}`)
	if _, has := ev["latencyMs"]; has {
		t.Errorf("unassigned has latency: %v", ev)
	}
}

func TestAPI_TemplatesExposeRequires(t *testing.T) {
	e := newEnv(t, false)
	_, out := e.do(t, "GET", "/api/templates", "")
	var blender, chrome map[string]any
	for _, x := range out["templates"].([]any) {
		m := x.(map[string]any)
		switch m["id"] {
		case "blender":
			blender = m
		case "google-chrome":
			chrome = m
		}
	}
	req, _ := blender["requires"].([]any)
	if len(req) != 1 || req[0].(map[string]any)["id"] != "blender-emulate-numpad" || req[0].(map[string]any)["help"] != "Blender Preferences > Input > Emulate Numpad" {
		t.Errorf("blender requires = %v", blender["requires"])
	}
	if r, ok := chrome["requires"].([]any); !ok || len(r) != 0 {
		t.Errorf("chrome requires = %v (an empty array)", chrome["requires"])
	}
}

func TestAPI_DeliberateDisarmIsRemembered(t *testing.T) {
	arm := &fakeArm{armed: true}
	e := envWith(t, false, func(d *Deps) { d.Arm = arm })
	env := func(string) string { return "" }
	e.store.MarkFirstRunDone()
	if d, _ := e.store.StartDisarmed(env); d {
		t.Fatal("setup")
	}
	if code, _ := e.do(t, "POST", "/api/arm", `{"armed":false}`); code != 200 {
		t.Fatal(code)
	}
	if d, why := e.store.StartDisarmed(env); !d || why != preset.StartReasonDisarmedLast {
		t.Errorf("after a disarm: %v %q", d, why)
	}
	if code, _ := e.do(t, "POST", "/api/arm", `{"armed":true}`); code != 200 {
		t.Fatal(code)
	}
	if d, why := e.store.StartDisarmed(env); d {
		t.Errorf("after arming again: %q", why)
	}
}

// ---- wave L: the marker follows the switch without blocking it ------------------------------------

func TestArmMarkerWriter_LatestStateWinsAndSetNeverBlocks(t *testing.T) {
	var mu sync.Mutex
	var applied []string
	block := make(chan struct{})
	w := &ArmMarkerWriter{
		Mark:  func() error { <-block; mu.Lock(); applied = append(applied, "mark"); mu.Unlock(); return nil },
		Clear: func() error { mu.Lock(); applied = append(applied, "clear"); mu.Unlock(); return nil },
	}
	done := make(chan struct{})
	go func() {
		w.Set(false) // the writer blocks inside Mark: Set must still return at once
		w.Set(true)
		w.Set(false)
		w.Set(true) // the final wanted state
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Set blocked behind a slow disk write")
	}
	close(block)
	w.Close()
	mu.Lock()
	defer mu.Unlock()
	if len(applied) == 0 || applied[len(applied)-1] != "clear" {
		t.Errorf("applied = %v: the last wanted state (armed -> clear) must be the last write", applied)
	}
	w.Set(false) // after Close: ignored, no panic
}

type sourcerArm struct {
	armed bool
	froms []string
}

func (a *sourcerArm) Armed() bool     { return a.armed }
func (a *sourcerArm) SetArmed(b bool) { a.armed = b }
func (a *sourcerArm) SetArmedFrom(b bool, src string) {
	a.armed = b
	a.froms = append(a.froms, fmt.Sprintf("%v/%s", b, src))
}

func TestAPI_ArmSourcerOwnsTheBookkeeping(t *testing.T) {
	ring := NewEventLog()
	arm := &sourcerArm{armed: true}
	e := envWith(t, false, func(d *Deps) { d.Arm = arm; d.Events = ring })
	e.h = NewHandler(Deps{Store: e.store, Frontmost: e.front, Bus: e.bus, Arm: arm, Events: ring, Live: fakeLive{EngineRunning: true}})
	e.do(t, "POST", "/api/arm", `{"armed":false}`)
	if len(arm.froms) != 1 || arm.froms[0] != "false/editor" {
		t.Errorf("froms = %v", arm.froms)
	}
	if len(ring.List()) != 0 {
		t.Errorf("the handler logged an arm event itself although the switch reports its own: %v", ring.List())
	}
	if d, _ := e.store.StartDisarmed(func(string) string { return "" }); !d {
		t.Log("first-run reason applies anyway")
	}
	// the handler did not write the disarmed marker itself either (the switch's callback does)
	if _, err := os.Stat(filepath.Join(e.store.Dir(), preset.DisarmedMarker)); err == nil {
		t.Error("the handler wrote the marker although the switch owns it")
	}
	// arming still records the first arming
	e.do(t, "POST", "/api/arm", `{"armed":true}`)
	if d, why := e.store.StartDisarmed(func(string) string { return "" }); d && why == preset.StartReasonFirstRun {
		t.Error("arming did not mark the first run done")
	}
}

func TestAPI_ImportReportShowsInheritedRiskAndSettingsChanges(t *testing.T) {
	e := newEnv(t, false)
	zip := zipBody(t, map[string]string{
		"manifest.json":          `{"kind":"2t5-deck-plus-presets","schema":1,"created":"2026-11-04T00:00:00Z","app":"all","files":["_global.json","com.google.Chrome.json"]}`,
		"_global.json":           `{"schema":1,"id":"_global","name":"Global","packs":["generic"],"banks":{},"settings":{"actionDefaults":{"key":{"key":"Cmd+Q"}}}}`,
		"com.google.Chrome.json": `{"schema":1,"id":"com.google.Chrome","name":"Chrome","packs":["generic"],"banks":{"base":{"C1":{"type":"key"}}},"settings":{}}`,
	})
	rec := e.raw("POST", "/api/import?dryRun=1", zip, "Content-Type", "application/zip")
	m := decodeMap(t, rec)
	if rec.Code != 200 || m["needsConfirm"] != true {
		t.Fatalf("%d %v", rec.Code, m)
	}
	inh, _ := m["inherited"].([]any)
	if len(inh) != 1 || !strings.Contains(inh[0].(map[string]any)["reason"].(string), "Cmd+Q") {
		t.Errorf("inherited = %v", m["inherited"])
	}
	files := m["files"].([]any)
	if _, ok := files[0].(map[string]any)["settingsChanged"]; !ok {
		t.Errorf("files[0] lacks settingsChanged: %v", files[0])
	}
}

// ---- wave N P3: marker writer, explicit disarm, start persistence ---------------------------------

func TestArmMarkerWriter_SetRacingCloseNeverPanics(t *testing.T) {
	for trial := 0; trial < 400; trial++ {
		w := &ArmMarkerWriter{Mark: func() error { return nil }, Clear: func() error { return nil }}
		var wg sync.WaitGroup
		for g := 0; g < 4; g++ {
			wg.Add(1)
			go func(g int) {
				defer wg.Done()
				for i := 0; i < 50; i++ {
					w.Set((g+i)%2 == 0)
				}
			}(g)
		}
		go w.Close()
		wg.Wait()
		w.Close()
	}
}

func TestArmMarkerWriter_RetriesWithBackoffAndReportsTheError(t *testing.T) {
	var mu sync.Mutex
	fails := 3
	var calls int
	w := &ArmMarkerWriter{
		Mark: func() error {
			mu.Lock()
			defer mu.Unlock()
			calls++
			if fails > 0 {
				fails--
				return errors.New("read-only file system")
			}
			return nil
		},
		Clear:   func() error { return nil },
		Backoff: func(int) time.Duration { return 5 * time.Millisecond },
	}
	w.Set(false)
	deadline := time.Now().Add(2 * time.Second)
	sawErr := false
	for time.Now().Before(deadline) {
		if e := w.LastError(); e != "" {
			sawErr = strings.Contains(e, "read-only")
		}
		mu.Lock()
		c := calls
		mu.Unlock()
		if c >= 4 && w.LastError() == "" {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	if !sawErr {
		t.Error("the failure was never visible in LastError")
	}
	mu.Lock()
	if calls != 4 {
		t.Errorf("calls = %d, want 3 failures + 1 success", calls)
	}
	mu.Unlock()
	if w.LastError() != "" {
		t.Errorf("error stays after a success: %q", w.LastError())
	}
	w.Close()
}

func TestArmMarkerWriter_CloseFlushesAndAStaleRetryDoesNotUndoANewerState(t *testing.T) {
	var mu sync.Mutex
	var seq []string
	failMark := true
	w := &ArmMarkerWriter{
		Mark: func() error {
			mu.Lock()
			defer mu.Unlock()
			seq = append(seq, "mark")
			if failMark {
				return errors.New("disk")
			}
			return nil
		},
		Clear:   func() error { mu.Lock(); seq = append(seq, "clear"); mu.Unlock(); return nil },
		Backoff: func(int) time.Duration { return 10 * time.Millisecond },
	}
	w.Set(false) // fails, will retry
	time.Sleep(30 * time.Millisecond)
	w.Set(true) // the user armed: the failing Mark must not be retried afterwards
	w.Close()
	mu.Lock()
	defer mu.Unlock()
	if seq[len(seq)-1] != "clear" {
		t.Errorf("sequence %v: the last write must be the newest wanted state", seq)
	}
	if w.LastError() != "" {
		t.Errorf("LastError = %q", w.LastError())
	}
}

func TestWarningsAndPreflight_SurfaceAMarkerWriteFailure(t *testing.T) {
	m := &fakeMark{err: "no space left on device"}
	e := envWith(t, false, func(d *Deps) { d.ArmMark = m })
	_, w := e.do(t, "GET", "/api/warnings", "")
	found := false
	for _, x := range w["warnings"].([]any) {
		if mm := x.(map[string]any); mm["kind"] == "disarm-marker" && strings.Contains(mm["message"].(string), "could not save the disarmed state") && mm["severity"] == "warn" {
			found = true
		}
	}
	if !found {
		t.Errorf("no disarm-marker warning: %v", w)
	}
	by, _, _ := preflight(t, e)
	if by["disarmedState"]["status"] != "warn" || !strings.Contains(by["disarmedState"]["detail"].(string), "no space") {
		t.Errorf("preflight = %v", by["disarmedState"])
	}
	m.err = ""
	by, _, _ = preflight(t, e)
	if by["disarmedState"]["status"] != "ok" {
		t.Errorf("after recovery = %v", by["disarmedState"])
	}
}

type fakeMark struct{ err string }

func (f *fakeMark) LastError() string { return f.err }

func TestApplyStartPolicy_ADisarmedStartIsRemembered(t *testing.T) {
	e := newEnv(t, false)
	env := func(k string) string {
		if k == preset.EnvStartDisarmed {
			return "1"
		}
		return ""
	}
	e.store.MarkFirstRunDone()
	st := ApplyStartPolicy(e.store, &orderArm{armed: true}, env)
	if st.Armed || st.Reason != preset.StartReasonEnv {
		t.Fatalf("start = %+v", st)
	}
	// quit without arming; next launch, no env: still disarmed, now because it was left disarmed
	if d, why := e.store.StartDisarmed(func(string) string { return "" }); !d || why != preset.StartReasonDisarmedLast {
		t.Errorf("next launch = %v %q", d, why)
	}
	// the user arms (clears the marker): the following launch is armed
	e.store.ClearDisarmed()
	if d, _ := e.store.StartDisarmed(func(string) string { return "" }); d {
		t.Error("still disarmed after arming")
	}
}

// ---- wave O P3 ------------------------------------------------------------------------------------

func TestArmMarkerWriter_CloseGivesUpOnAWedgedDisk(t *testing.T) {
	hang := make(chan struct{})
	defer close(hang)
	var logged sync.Mutex
	var msg string
	w := &ArmMarkerWriter{
		Mark:         func() error { <-hang; return nil },
		Clear:        func() error { return nil },
		CloseTimeout: 100 * time.Millisecond,
		Logf:         func(f string, a ...any) { logged.Lock(); msg = fmt.Sprintf(f, a...); logged.Unlock() },
	}
	w.Set(false)
	time.Sleep(20 * time.Millisecond) // the writer is now stuck inside Mark
	start := time.Now()
	w.Close()
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Close waited %v for a hung disk", d)
	}
	logged.Lock()
	defer logged.Unlock()
	if !strings.Contains(msg, "gave up") {
		t.Errorf("log = %q", msg)
	}
}

func TestImportConfirmation_IsBoundToTheReportThatWasPreviewed(t *testing.T) {
	bundle := func() []byte {
		return zipBody(t, map[string]string{
			"manifest.json":     `{"kind":"2t5-deck-plus-presets","schema":1,"created":"2026-11-04T00:00:00Z","app":"all","files":["com.evil.App.json"]}`,
			"com.evil.App.json": `{"schema":1,"id":"com.evil.App","name":"Evil","packs":["generic"],"banks":{"base":{"C1":{"type":"shell","cmd":"/usr/bin/open"},"C2":{"type":"key"}}},"settings":{}}`,
		})
	}
	e := newEnv(t, false)
	n := &NativeIO{Store: e.store}
	e2 := envWith(t, false, func(d *Deps) { d.Native = n })
	tok, m := importTokenOf(t, e2, bundle(), "")
	if m["needsConfirm"] != true {
		t.Fatalf("preview: %v", m)
	}
	// meanwhile Global gets a default that makes the bare key assignment destructive: the same
	// import now carries MORE risk than the user was shown
	if err := e.store.SetSetting(preset.GlobalID, "actionDefaults", map[string]any{"key": map[string]any{"key": "Cmd+Q"}}); err != nil {
		t.Fatal(err)
	}
	rec := e2.raw("POST", "/api/import?confirmRisk=1&token="+tok, nil)
	m = decodeMap(t, rec)
	if rec.Code != 409 || m["riskChanged"] != true {
		t.Fatalf("a confirmation for a stale report applied: %d %v", rec.Code, m)
	}
	if m["files"] == nil || m["risk"] == nil {
		t.Errorf("the 409 must carry the new report: %v", m)
	}
	if _, ok := e.store.Snapshot().Preset("com.evil.App"); ok {
		t.Fatal("installed despite the refusal")
	}
	// the user has now seen the new report: confirming again applies it
	if rec := e2.raw("POST", "/api/import?confirmRisk=1&token="+tok, nil); rec.Code != 200 {
		t.Fatalf("second confirmation = %d %s", rec.Code, rec.Body.String())
	}
	// an unchanged report applies at the first confirmation
	e3 := newEnv(t, false)
	e4 := envWith(t, false, func(d *Deps) { d.Native = &NativeIO{Store: e3.store} })
	tok2, _ := importTokenOf(t, e4, bundle(), "")
	if rec := e4.raw("POST", "/api/import?confirmRisk=1&token="+tok2, nil); rec.Code != 200 {
		t.Errorf("unchanged report = %d %s", rec.Code, rec.Body.String())
	}
}

// ---- wave O P2-E: pending outside changes over HTTP ---------------------------------------------------

func TestAPI_PendingExternalListApproveRejectWarningsAndPreflight(t *testing.T) {
	e := newEnv(t, false)
	if code, out := e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`); code != 201 {
		t.Fatal(code, out)
	}
	hold := func(body string) {
		raw := []byte(`{"schema":1,"id":"com.google.Chrome","name":"Google Chrome","packs":["generic"],"banks":` + body + `,"settings":{}}`)
		os.WriteFile(filepath.Join(e.store.Dir(), "com.google.Chrome.json"), raw, 0o644)
		if changed, _ := e.store.CheckExternal(); changed {
			t.Fatal("applied a risky outside change")
		}
	}
	hold(`{"base":{"C1":{"type":"shell","cmd":"/usr/bin/open"}}}`)
	code, out := e.do(t, "GET", "/api/pending-external", "")
	pend, _ := out["pending"].([]any)
	if code != 200 || len(pend) != 1 {
		t.Fatalf("pending = %d %v", code, out)
	}
	row := pend[0].(map[string]any)
	for _, k := range []string{"id", "preset", "file", "kind", "changes", "risks", "detectedAt"} {
		if _, ok := row[k]; !ok {
			t.Errorf("row lacks %q: %v", k, row)
		}
	}
	id := row["id"].(string)
	// warnings and preflight
	_, w := e.do(t, "GET", "/api/warnings", "")
	found := false
	for _, x := range w["warnings"].([]any) {
		if x.(map[string]any)["kind"] == "pending-external" {
			found = true
		}
	}
	if !found {
		t.Errorf("no pending-external warning: %v", w)
	}
	by, _, _ := preflight(t, e)
	if by["pendingExternal"]["status"] != "warn" {
		t.Errorf("preflight = %v", by["pendingExternal"])
	}
	// approve needs confirmRisk
	if code, _ := e.do(t, "POST", "/api/pending-external/"+id+"/approve", `{}`); code != 400 {
		t.Errorf("approve without confirm = %d", code)
	}
	if code, _ := e.do(t, "POST", "/api/pending-external/nope/approve", `{"confirmRisk":true}`); code != 404 {
		t.Errorf("unknown = %d", code)
	}
	// reject restores
	if code, o := e.do(t, "POST", "/api/pending-external/"+id+"/reject", ``); code != 200 {
		t.Fatalf("reject = %d %v", code, o)
	}
	if p, _ := e.store.Snapshot().Preset("com.google.Chrome"); p.Assigned() != 0 {
		t.Error("rejected change reached memory")
	}
	if code, _ := e.do(t, "POST", "/api/pending-external/"+id+"/reject", ``); code != 404 {
		t.Errorf("second reject = %d", code)
	}
	// hold again and approve
	hold(`{"base":{"C1":{"type":"shell","cmd":"/usr/bin/open"}}}`)
	_, out = e.do(t, "GET", "/api/pending-external", "")
	id = out["pending"].([]any)[0].(map[string]any)["id"].(string)
	if code, o := e.do(t, "POST", "/api/pending-external/"+id+"/approve", `{"confirmRisk":true}`); code != 200 {
		t.Fatalf("approve = %d %v", code, o)
	}
	if p, _ := e.store.Snapshot().Preset("com.google.Chrome"); p.Assigned() != 1 {
		t.Error("approved change not applied")
	}
	by, _, _ = preflight(t, e)
	if by["pendingExternal"]["status"] != "ok" {
		t.Errorf("preflight after = %v", by["pendingExternal"])
	}
	_, ev := e.do(t, "GET", "/api/events-log", "")
	kinds := ""
	for _, x := range ev["events"].([]any) {
		kinds += x.(map[string]any)["kind"].(string) + ","
	}
	if !strings.Contains(kinds, "import") || !strings.Contains(kinds, "restore") {
		t.Errorf("events = %s", kinds)
	}
}

// ---- wave P: a planted backup over HTTP ------------------------------------------------------------

func TestAPI_PlantedBackupRestoreNeedsConfirmOrSkipRisky(t *testing.T) {
	root := t.TempDir()
	st, err := preset.Open(filepath.Join(root, "presets"), preset.Options{LiveResolvePath: "-", PollInterval: -1, StateDir: filepath.Join(root, "state"), Controls: ControlIDs()})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	e := &env{store: st, front: &fakeFront{app: AppRef{ID: chromeID}}, bus: NewBus()}
	e.h = NewHandler(Deps{Store: st, Frontmost: e.front, Bus: e.bus, Live: fakeLive{EngineRunning: true}})
	if _, err := st.AddApp(chromeID, "", nil); err != nil {
		t.Fatal(err)
	}
	zipb := zipBody(t, map[string]string{
		"manifest.json":          `{"kind":"2t5-deck-plus-presets","schema":1,"created":"2026-11-04T00:00:00Z","app":"all","files":["com.google.Chrome.json"]}`,
		"com.google.Chrome.json": `{"schema":1,"id":"com.google.Chrome","name":"Chrome","packs":["generic"],"banks":{"base":{"C1":{"type":"shell","cmd":"/usr/bin/open"},"C2":{"type":"key","key":"Cmd+T"}}},"settings":{}}`,
	})
	bd := filepath.Join(st.Dir(), ".backups")
	os.MkdirAll(bd, 0o755)
	name := "presets-20260102-000000-manual.zip"
	os.WriteFile(filepath.Join(bd, name), zipb, 0o644)
	_, list := e.do(t, "GET", "/api/backups", "")
	trusted := true
	for _, b := range list["backups"].([]any) {
		if m := b.(map[string]any); m["name"] == name {
			trusted = m["trusted"].(bool)
		}
	}
	if trusted {
		t.Error("a planted zip is listed as trusted")
	}
	rec := e.raw("POST", "/api/backups/"+name+"/restore", nil)
	m := decodeMap(t, rec)
	if rec.Code != 409 || m["needsConfirm"] != true || m["risk"] == nil {
		t.Fatalf("planted restore = %d %v", rec.Code, m)
	}
	if p, _ := st.Snapshot().Preset(chromeID); p.Assigned() != 0 {
		t.Fatal("restored")
	}
	rec = e.raw("POST", "/api/backups/"+name+"/restore?skipRisky=1", nil)
	if rec.Code != 200 {
		t.Fatalf("skipRisky restore = %d %s", rec.Code, rec.Body.String())
	}
	p, _ := st.Snapshot().Preset(chromeID)
	if p.Banks["base"]["C1"] != nil || p.Banks["base"]["C2"] == nil {
		t.Errorf("banks = %v", p.Banks)
	}
}
