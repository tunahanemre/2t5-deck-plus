package editorapi

import (
	"archive/zip"
	"bytes"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"

	"2t5-deck-plus/internal/preset"
	"strings"
	"testing"
)

// Wave E, F11: mutating requests need the X-LD-Editor header, a JSON (or zip)
// content type and a non-null Origin.

func bareOn(e *env, method, path, body string, hdr ...string) int {
	req := httptest.NewRequest(method, path, strings.NewReader(body)) // no helper: no X-LD-Editor, no Content-Type
	req.Host = "127.0.0.1:2541"
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec.Code
}

func TestWaveE_MutationsNeedTheEditorHeader(t *testing.T) {
	e := newEnv(t, false)
	json := "Content-Type"
	for _, c := range []struct{ m, p, b string }{
		{"POST", "/api/arm", `{"armed":false}`},
		{"POST", "/api/undo", ``},
		{"POST", "/api/apps", `{"id":"com.example.App"}`},
		{"PUT", "/api/preset/_global/assign", `{"bank":"base","control":"C1","assignment":{"type":"none"}}`},
		{"DELETE", "/api/apps/com.example.App", ``},
	} {
		if code := bareOn(e, c.m, c.p, c.b, json, "application/json"); code != 403 {
			t.Errorf("%s %s without the header = %d, want 403", c.m, c.p, code)
		}
		if code := bareOn(e, c.m, c.p, c.b, json, "application/json", "X-LD-Editor", "0"); code != 403 {
			t.Errorf("%s %s with header 0 = %d, want 403", c.m, c.p, code)
		}
	}
	// nothing was armed / changed by the refused requests
	if code := bareOn(e, "GET", "/api/health", ""); code != 200 {
		t.Errorf("GET without header = %d", code)
	}
	if code := bareOn(e, "POST", "/api/undo", ``, "X-LD-Editor", "1", json, "application/json"); code != 200 {
		t.Errorf("good undo = %d", code)
	}
}

func TestWaveE_OriginNullRefused(t *testing.T) {
	e := newEnv(t, false)
	h := []string{"X-LD-Editor", "1", "Content-Type", "application/json"}
	if code := bareOn(e, "POST", "/api/undo", ``, append(h, "Origin", "null")...); code != 403 {
		t.Errorf("Origin null = %d", code)
	}
	if code := bareOn(e, "POST", "/api/undo", ``, append(h, "Origin", "https://evil.example")...); code != 403 {
		t.Errorf("cross-origin = %d", code)
	}
	if code := bareOn(e, "POST", "/api/undo", ``, append(h, "Origin", "http://127.0.0.1:2541")...); code != 200 {
		t.Errorf("same origin = %d", code)
	}
	if code := bareOn(e, "POST", "/api/undo", ``, append(h, "Origin", "wails://wails")...); code != 200 {
		t.Errorf("wails origin = %d", code)
	}
}

func TestWaveE_ContentTypeEnforced(t *testing.T) {
	e := newEnv(t, false)
	h := "X-LD-Editor"
	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x"} {
		if code := bareOn(e, "POST", "/api/apps", `{"id":"com.example.App"}`, h, "1", "Content-Type", ct); code != 415 {
			t.Errorf("JSON route with Content-Type %q = %d, want 415", ct, code)
		}
	}
	if code := bareOn(e, "POST", "/api/apps", `{"id":"com.example.App"}`, h, "1", "Content-Type", "application/json; charset=utf-8"); code != 201 {
		t.Errorf("json with charset = %d", code)
	}
	// import takes a zip (or multipart), never JSON or text
	for _, ct := range []string{"application/json", "text/plain", ""} {
		if code := bareOn(e, "POST", "/api/import", "PK", h, "1", "Content-Type", ct); code != 415 {
			t.Errorf("import with Content-Type %q = %d, want 415", ct, code)
		}
	}
	for _, ct := range []string{"application/zip", "application/octet-stream", "application/x-zip-compressed"} {
		if code := bareOn(e, "POST", "/api/import", "garbage", h, "1", "Content-Type", ct); code != 400 {
			t.Errorf("import with Content-Type %q = %d, want 400 (bad zip, guard passed)", ct, code)
		}
	}
}

// Wave G (B10): the colliding pair is refused when the second app is ADDED, with a
// clear 400; the commit-time 409 for what still slips through is tested in
// internal/preset (TestWaveE_CommitRejectsWhatWouldNotCompose).
func TestWaveE_CollidingAppIsRefusedAtAddTime(t *testing.T) {
	e := newEnv(t, false)
	if code, out := e.do(t, "POST", "/api/apps", `{"id":"com.a.b"}`); code != 201 {
		t.Fatalf("add: %d %v", code, out)
	}
	code, out := e.do(t, "POST", "/api/apps", `{"id":"com.a.b-shift"}`)
	if code != 400 || out["field"] != "id" || !strings.Contains(out["error"].(string), "com.a.b") {
		t.Errorf("colliding add = %d %v", code, out)
	}
	if _, ok := e.store.Snapshot().Preset("com.a.b-shift"); ok {
		t.Error("the refused app is in the store")
	}
}

// ---- F4: import risk over HTTP and the native door -----------------------------------------

func shellZip(t *testing.T, e *env) []byte {
	t.Helper()
	src := newEnv(t, false)
	if code, out := src.do(t, "POST", "/api/apps", `{"id":"com.evil.App"}`); code != 201 {
		t.Fatal(code, out)
	}
	if code, out := src.do(t, "PUT", "/api/preset/com.evil.App/assign", `{"bank":"base","control":"C1","assignment":{"type":"shell","cmd":"/usr/bin/open","args":["-a","Calculator"]}}`); code != 200 {
		t.Fatal(code, out)
	}
	rec := src.raw("GET", "/api/export?app=com.evil.App", nil)
	return rec.Body.Bytes()
}

func TestWaveE_ImportEndpointRiskAndConfirm(t *testing.T) {
	e := newEnv(t, false)
	zip := shellZip(t, e)
	// dry run: 200 with the risk and the command lines
	rec := e.raw("POST", "/api/import?dryRun=1", zip, "Content-Type", "application/zip")
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"needsConfirm":true`, `"shell":1`, `/usr/bin/open -a Calculator`, `"runsProgram"`} {
		if !strings.Contains(body, want) {
			t.Errorf("dry run lacks %s: %s", want, body)
		}
	}
	tok, _ := decodeMap(t, rec)["token"].(string)
	if tok == "" {
		t.Fatalf("dry run gave no token: %s", body)
	}
	// real import without a token (wave H): 409 needsToken with the report
	rec = e.raw("POST", "/api/import", zip, "Content-Type", "application/zip")
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"needsToken":true`) || !strings.Contains(rec.Body.String(), `"files"`) {
		t.Fatalf("tokenless import = %d %s", rec.Code, rec.Body.String())
	}
	// with the token but without confirmRisk: 409 with the report, nothing imported, token still valid
	rec = e.raw("POST", "/api/import?token="+tok, nil)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), `"needsConfirm":true`) || !strings.Contains(rec.Body.String(), `"files"`) {
		t.Fatalf("unconfirmed import = %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := e.store.Snapshot().Preset("com.evil.App"); ok {
		t.Fatal("unconfirmed import installed the preset")
	}
	// confirmRisk=false is not a confirmation
	if rec = e.raw("POST", "/api/import?confirmRisk=0&token="+tok, nil); rec.Code != 409 {
		t.Errorf("confirmRisk=0 = %d", rec.Code)
	}
	rec = e.raw("POST", "/api/import?confirmRisk=1&token="+tok, nil)
	if rec.Code != 200 {
		t.Fatalf("confirmed import = %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := e.store.Snapshot().Preset("com.evil.App"); !ok {
		t.Error("confirmed import did not install")
	}
}

func TestWaveE_NativeApplyImportNeedsConfirmAndKeepsTheToken(t *testing.T) {
	e := newEnv(t, false)
	zip := shellZip(t, e)
	path := filepath.Join(t.TempDir(), "evil.zip")
	if err := os.WriteFile(path, zip, 0o644); err != nil {
		t.Fatal(err)
	}
	nio := &NativeIO{Store: e.store}
	pv, err := nio.PreviewImport(path)
	if err != nil || !pv.NeedsConfirm || pv.Risk.Shell != 1 || len(pv.Files[0].RunsProgram) != 1 {
		t.Fatalf("preview = %+v %v", pv, err)
	}
	_, err = nio.ApplyImport(pv.Token, false)
	var re *preset.RiskError
	if !errors.As(err, &re) {
		t.Fatalf("unconfirmed apply err = %v", err)
	}
	if _, ok := e.store.Snapshot().Preset("com.evil.App"); ok {
		t.Fatal("installed without confirmation")
	}
	res, err := nio.ApplyImport(pv.Token, true) // the token survived the refusal
	if err != nil || !res.OK {
		t.Fatalf("confirmed apply = %+v %v", res, err)
	}
	if _, ok := e.store.Snapshot().Preset("com.evil.App"); !ok {
		t.Error("not installed after confirmation")
	}
	if _, err := nio.ApplyImport(pv.Token, true); !errors.Is(err, ErrUnknownToken) {
		t.Errorf("token reuse = %v", err)
	}
}

func TestWaveE_RestoreEndpointReportsSkippedFiles(t *testing.T) {
	e := newEnv(t, false)
	e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`)
	dir := filepath.Join(e.store.Dir(), ".backups")
	os.MkdirAll(dir, 0o755)
	// a hand-made old-style backup: one good preset, one broken file
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, b := range map[string]string{
		"manifest.json":          `{"kind":"2t5-deck-plus-presets","schema":1,"created":"x","app":"all","files":[]}`,
		"com.google.Chrome.json": `{"schema":1,"id":"com.google.Chrome","name":"Google Chrome","packs":["generic"],"banks":{},"settings":{}}`,
		"com.broken.App.json":    `{"id":`,
	} {
		w, _ := zw.Create(n)
		w.Write([]byte(b))
	}
	zw.Close()
	os.WriteFile(filepath.Join(dir, "presets-20261101-090000-daily.zip"), buf.Bytes(), 0o644)
	code, out := e.do(t, "POST", "/api/backups/presets-20261101-090000-daily.zip/restore", "")
	if code != 200 {
		t.Fatalf("restore = %d %v", code, out)
	}
	sk, _ := out["skipped"].([]any)
	if len(sk) != 1 || !strings.Contains(sk[0].(map[string]any)["file"].(string), "com.broken.App.json") {
		t.Errorf("skipped = %v", out["skipped"])
	}
}

// ---- F7: armCoverage -----------------------------------------------------------------------

type coverArm struct{ fakeArm }

func (coverArm) ArmCoverage() []string { return []string{"keys", "mouse"} }

func TestWaveE_LiveAndArmReportArmCoverage(t *testing.T) {
	// a provider that cannot say: an empty list, never null
	e := envWith(t, false, func(d *Deps) { d.Arm = &fakeArm{armed: true} })
	_, live := e.do(t, "GET", "/api/live", "")
	if l, ok := live["armCoverage"].([]any); !ok || len(l) != 0 {
		t.Errorf("live armCoverage = %#v, want []", live["armCoverage"])
	}
	e = envWith(t, false, func(d *Deps) { d.Arm = &coverArm{fakeArm{armed: true}} })
	_, live = e.do(t, "GET", "/api/live", "")
	l, _ := live["armCoverage"].([]any)
	if len(l) != 2 || l[0] != "keys" || l[1] != "mouse" {
		t.Errorf("live armCoverage = %#v", live["armCoverage"])
	}
	_, arm := e.do(t, "GET", "/api/arm", "")
	if l, _ := arm["armCoverage"].([]any); len(l) != 2 {
		t.Errorf("GET /api/arm armCoverage = %#v", arm["armCoverage"])
	}
	_, arm = e.do(t, "POST", "/api/arm", `{"armed":false}`)
	if l, _ := arm["armCoverage"].([]any); len(l) != 2 || arm["armed"] != false {
		t.Errorf("POST /api/arm = %#v", arm)
	}
}
