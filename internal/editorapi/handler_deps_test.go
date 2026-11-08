package editorapi

import (
	"errors"
	"strings"
	"testing"

	"2t5-deck-plus/internal/deps"
)

// fakeSystem answers GET /api/dependencies with fixed Mac facts. Nothing here
// reads MIDI, runs a process or touches System Settings.
type fakeSystem struct {
	in    deps.Inputs
	fresh []bool
}

func (f *fakeSystem) Facts(fresh bool) deps.Inputs { f.fresh = append(f.fresh, fresh); return f.in }

func okFacts() deps.Inputs {
	t, f := true, false
	none := []string{}
	return deps.Inputs{
		OS:               &deps.Version{Major: 15, Minor: 3},
		Python:           &deps.PythonFacts{Found: true, Path: "/opt/homebrew/bin/python3", Version: "3.12.4", VersionOK: true, StdlibOK: true, ResolveModule: true},
		ResolveInstalled: &t, ResolveRunning: &f,
		MIDI:      &deps.MIDIFacts{Sources: []string{"Loupedeck"}},
		OtherApps: &none,
	}
}

func depItems(t *testing.T, e *env, query string) (map[string]map[string]any, map[string]any) {
	t.Helper()
	code, out := e.do(t, "GET", "/api/dependencies"+query, "")
	if code != 200 {
		t.Fatalf("GET /api/dependencies = %d %v", code, out)
	}
	by := map[string]map[string]any{}
	for _, it := range out["items"].([]any) {
		m := it.(map[string]any)
		for _, k := range []string{"id", "name", "kind", "required", "feature", "howToGet", "howToCheck", "without", "status", "detail", "fixAction"} {
			if _, ok := m[k]; !ok {
				t.Errorf("item %v lacks %q", m["id"], k)
			}
		}
		by[m["id"].(string)] = m
	}
	return by, out
}

func TestDependencies_MergesCatalogWithChecks(t *testing.T) {
	sys := &fakeSystem{in: okFacts()}
	e := envWith(t, false, func(d *Deps) {
		d.System = sys
		d.Perms = fakePerms{Accessibility: false, ScreenRecording: true, AutomationFinder: "granted"}
		d.Live = fakeLive{EngineRunning: true, Sidecar: "connected"}
	})
	by, out := depItems(t, e, "")
	if by["accessibility"]["status"] != "missing" || by["accessibility"]["fixAction"] != "open-accessibility-settings" {
		t.Errorf("accessibility = %v", by["accessibility"])
	}
	if by["screen-recording"]["status"] != "ok" || by["screen-recording"]["fixAction"] != "" {
		t.Errorf("an ok item offers no fix: %v", by["screen-recording"])
	}
	if by["loupedeck"]["status"] != "ok" || by["python3"]["status"] != "ok" {
		t.Errorf("loupedeck=%v python3=%v", by["loupedeck"]["status"], by["python3"]["status"])
	}
	if by["apple-silicon"]["status"] != "info" || by["shell-commands"]["status"] != "info" {
		t.Errorf("informational items are status info: %v %v", by["apple-silicon"]["status"], by["shell-commands"]["status"])
	}
	req, _ := out["requiredMissing"].([]any)
	if len(req) != 1 || req[0] != "accessibility" {
		t.Errorf("requiredMissing = %v, want [accessibility]", req)
	}
	if out["ok"] != false {
		t.Errorf("ok = %v with a required item missing", out["ok"])
	}
}

func TestDependencies_SidecarStateFeedsResolveScripting(t *testing.T) {
	f := okFacts()
	r := true
	f.ResolveRunning = &r
	e := envWith(t, false, func(d *Deps) {
		d.System = &fakeSystem{in: f}
		d.Perms = fakePerms{Accessibility: true, ScreenRecording: true, AutomationFinder: "granted"}
		d.Live = fakeLive{EngineRunning: true, Sidecar: "retrying"}
	})
	by, out := depItems(t, e, "")
	if by["resolve-scripting"]["status"] != "missing" {
		t.Errorf("resolve-scripting = %v (%v)", by["resolve-scripting"]["status"], by["resolve-scripting"]["detail"])
	}
	// an optional item that is missing never lands in requiredMissing
	if req, _ := out["requiredMissing"].([]any); len(req) != 0 {
		t.Errorf("requiredMissing = %v", req)
	}
	if out["ok"] != true {
		t.Errorf("ok = %v: only optional items are missing", out["ok"])
	}
}

func TestDependencies_FreshQueryReachesTheProbe(t *testing.T) {
	sys := &fakeSystem{in: okFacts()}
	e := envWith(t, false, func(d *Deps) { d.System = sys })
	depItems(t, e, "")
	depItems(t, e, "?fresh=1")
	if len(sys.fresh) != 2 || sys.fresh[0] || !sys.fresh[1] {
		t.Errorf("fresh flags = %v, want [false true]", sys.fresh)
	}
}

func TestDependencies_NoProvidersIsAllUnknownNeverOK(t *testing.T) {
	e := newEnv(t, false)
	by, out := depItems(t, e, "")
	for id := range deps.CheckedIDs() {
		if by[id]["status"] != "unknown" {
			t.Errorf("%s = %v with no providers", id, by[id]["status"])
		}
	}
	if req, _ := out["requiredMissing"].([]any); len(req) != 0 {
		t.Errorf("unknown must not count as missing: %v", req)
	}
}

type recURL struct {
	urls []string
	err  error
}

func (r *recURL) open(u string) error { r.urls = append(r.urls, u); return r.err }

func TestOpenSettings_OnlyKnownPanesAndNeverAnArbitraryURL(t *testing.T) {
	rec := &recURL{}
	e := envWith(t, false, func(d *Deps) { d.OpenURL = rec.open })
	want := map[string]string{
		"accessibility":    "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
		"screen-recording": "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
		"automation":       "x-apple.systempreferences:com.apple.preference.security?Privacy_Automation",
	}
	for pane, url := range want {
		code, out := e.do(t, "POST", "/api/open-settings", `{"pane":"`+pane+`"}`)
		if code != 200 || out["ok"] != true {
			t.Fatalf("%s: %d %v", pane, code, out)
		}
		if got := rec.urls[len(rec.urls)-1]; got != url {
			t.Errorf("%s opened %q, want %q", pane, got, url)
		}
	}
	if code, _ := e.do(t, "POST", "/api/open-settings", `{"pane":"software-update"}`); code != 200 {
		t.Errorf("software-update = %d", code)
	}
	n := len(rec.urls)
	for _, bad := range []string{`{"pane":"https://example.com"}`, `{"pane":"file:///etc/passwd"}`, `{"pane":""}`, `{"url":"x-apple.systempreferences:x"}`, `{}`, `not json`} {
		code, _ := e.do(t, "POST", "/api/open-settings", bad)
		if code != 400 {
			t.Errorf("%s = %d, want 400", bad, code)
		}
	}
	if len(rec.urls) != n {
		t.Errorf("a refused request opened something: %v", rec.urls[n:])
	}
}

func TestOpenSettings_NoOpenerIs404AndFailureIs500(t *testing.T) {
	e := newEnv(t, false)
	if code, _ := e.do(t, "POST", "/api/open-settings", `{"pane":"accessibility"}`); code != 404 {
		t.Errorf("without an opener = %d, want 404", code)
	}
	rec := &recURL{err: errors.New("open failed")}
	e = envWith(t, false, func(d *Deps) { d.OpenURL = rec.open })
	code, out := e.do(t, "POST", "/api/open-settings", `{"pane":"accessibility"}`)
	if code != 500 || !strings.Contains(strings.ToLower(out["error"].(string)), "open failed") {
		t.Errorf("failure = %d %v", code, out)
	}
}

func TestOpenSettings_NeedsTheEditorHeaderAndTokenLikeEveryMutation(t *testing.T) {
	rec := &recURL{}
	e := envWith(t, false, func(d *Deps) { d.OpenURL = rec.open; d.Token = testTok })
	r := rawReq(e, "POST", "/api/open-settings", `{"pane":"accessibility"}`, nil, "127.0.0.1:2541")
	if r.Code != 403 || len(rec.urls) != 0 {
		t.Errorf("no token: %d, opened %v", r.Code, rec.urls)
	}
	r = rawReq(e, "POST", "/api/open-settings", `{"pane":"accessibility"}`, map[string]string{"X-LD-Token": testTok}, "127.0.0.1:2541")
	if r.Code != 200 || len(rec.urls) != 1 {
		t.Errorf("with token: %d, opened %v", r.Code, rec.urls)
	}
}

// ---- the WS-01 row only belongs on a Mac that keeps its data on WS-01 ------------

type envNotApplicable struct{ fakeEnv }

func (envNotApplicable) WS01Applicable() bool { return false }

func TestPreflight_WS01RowIsSkippedWhenTheDataIsNotThere(t *testing.T) {
	e := envWith(t, false, func(d *Deps) { d.Env = &envNotApplicable{fakeEnv{mounted: false, detail: "not mounted"}} })
	by, order, _ := preflight(t, e)
	if _, ok := by["ws01"]; ok {
		t.Errorf("ws01 row shown on a Mac with no WS-01: %v", order)
	}
	if _, ok := by["ldResolve"]; !ok {
		t.Errorf("the other env rows must stay: %v", order)
	}
	// a plain EnvProvider (no WS01Applicable) keeps the row, as before
	e = envWith(t, false, func(d *Deps) { d.Env = &fakeEnv{mounted: false, detail: "not mounted"} })
	by, _, _ = preflight(t, e)
	if by["ws01"]["status"] != "fail" {
		t.Errorf("ws01 = %v", by["ws01"])
	}
}

// ---- first run: the per-Mac marker ------------------------------------------------

type mutPerms struct{ p PermState }

func (m *mutPerms) Permissions() PermState { return m.p }

func TestSetup_FirstRunDoneAndComeBack(t *testing.T) {
	dir := t.TempDir()
	perms := &mutPerms{PermState{Accessibility: true, ScreenRecording: true, AutomationFinder: "granted"}}
	f := okFacts()
	f.MIDI = &deps.MIDIFacts{Sources: []string{"IAC"}} // the deck is not plugged in: required, missing, never worked
	e := envWith(t, false, func(d *Deps) { d.System = &fakeSystem{in: f}; d.Perms = perms; d.SetupDir = dir })

	_, out := depItems(t, e, "")
	if out["firstRun"] != true || out["setupNeeded"] != true {
		t.Fatalf("first run: %v", out)
	}
	code, done := e.do(t, "POST", "/api/setup/done", "")
	if code != 200 || done["firstRun"] != false {
		t.Fatalf("done: %d %v", code, done)
	}
	by, out := depItems(t, e, "")
	if out["firstRun"] != false {
		t.Errorf("after done: %v", out["firstRun"])
	}
	// the deck never worked on this Mac: missing, but it did NOT "come back"
	if by["loupedeck"]["wasOK"] != false || len(out["cameBack"].([]any)) != 0 {
		t.Errorf("loupedeck wasOK=%v cameBack=%v", by["loupedeck"]["wasOK"], out["cameBack"])
	}
	if by["accessibility"]["wasOK"] != true {
		t.Errorf("accessibility wasOK = %v", by["accessibility"]["wasOK"])
	}
	// Accessibility is taken away later: it comes back
	perms.p.Accessibility = false
	by, out = depItems(t, e, "")
	cb := out["cameBack"].([]any)
	if len(cb) != 1 || cb[0] != "accessibility" || by["accessibility"]["wasOK"] != true {
		t.Errorf("cameBack = %v", cb)
	}
	// the person closes the panel (done again): it stays quiet until the next ok -> missing
	e.do(t, "POST", "/api/setup/done", "")
	_, out = depItems(t, e, "")
	if len(out["cameBack"].([]any)) != 0 {
		t.Errorf("cameBack after done = %v", out["cameBack"])
	}
	perms.p.Accessibility = true
	depItems(t, e, "")
	perms.p.Accessibility = false
	_, out = depItems(t, e, "")
	if cb := out["cameBack"].([]any); len(cb) != 1 {
		t.Errorf("a second loss must come back: %v", cb)
	}
}

func TestSetup_WithoutAFolderThereIsNoMarker(t *testing.T) {
	e := envWith(t, false, func(d *Deps) { d.System = &fakeSystem{in: okFacts()} })
	_, out := depItems(t, e, "")
	if out["firstRun"] != false {
		t.Errorf("no marker support must not claim a first run: %v", out["firstRun"])
	}
	if code, _ := e.do(t, "POST", "/api/setup/done", ""); code != 404 {
		t.Errorf("done without a folder = %d", code)
	}
}

func TestSetup_DoneNeedsTheTokenLikeEveryMutation(t *testing.T) {
	e := envWith(t, false, func(d *Deps) { d.SetupDir = t.TempDir(); d.Token = testTok })
	if r := rawReq(e, "POST", "/api/setup/done", "", nil, "127.0.0.1:2541"); r.Code != 403 {
		t.Errorf("no token: %d", r.Code)
	}
}

func TestDependencies_FeatureTagsReachTheClient(t *testing.T) {
	e := envWith(t, false, func(d *Deps) { d.System = &fakeSystem{in: okFacts()} })
	by, _ := depItems(t, e, "")
	if f, _ := by["resolve-scripting"]["features"].([]any); len(f) != 1 || f[0] != "resolve" {
		t.Errorf("features = %v", by["resolve-scripting"]["features"])
	}
}
