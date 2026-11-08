package editorapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

type countIcons struct{ calls atomic.Int32 }

func (c *countIcons) AppIconPNG(id string) ([]byte, error) {
	c.calls.Add(1)
	if strings.HasSuffix(id, ".noicon") {
		return nil, ErrNoIcon
	}
	if id == "com.bad.png" {
		return []byte("not a png"), nil
	}
	img := image.NewNRGBA(image.Rect(0, 0, 256, 256))
	for y := 0; y < 256; y++ {
		for x := 0; x < 256; x++ {
			img.SetNRGBA(x, y, color.NRGBA{200, 30, 30, 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, img)
	return b.Bytes(), nil
}

func (e *env) raw(method, path string, body []byte, hdr ...string) *httptest.ResponseRecorder {
	req := newReq(method, path, bytes.NewReader(body))
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/zip") // default; a caller's header below wins
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *env) withIcons(ic IconProvider) {
	e.h = NewHandler(Deps{Store: e.store, Frontmost: e.front, Bus: e.bus, Icons: ic,
		Live: fakeLive{EngineRunning: true, Sidecar: "connected"}, Simulate: e.sim.Simulate})
}

func TestIconEndpoints(t *testing.T) {
	e := newEnv(t, false)
	ic := &countIcons{}
	e.withIcons(ic)
	dir := e.store.Dir()

	rec := e.raw("GET", "/api/icon/com.google.Chrome.png", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "image/png" || !strings.Contains(rec.Header().Get("Cache-Control"), "max-age") {
		t.Fatalf("icon: %d %v", rec.Code, rec.Header())
	}
	img, err := png.Decode(rec.Body)
	if err != nil || img.Bounds().Dx() != 256 {
		t.Fatalf("master: %v %v", err, img)
	}
	if _, err := os.Stat(filepath.Join(dir, ".icons", "com.google.Chrome.png")); err != nil {
		t.Error("master not cached under .icons")
	}
	// cached: provider not asked again; sized variants come from the master
	n := ic.calls.Load()
	rec = e.raw("GET", "/api/appicon/com.google.Chrome?size=64", nil)
	if rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if im, _ := png.Decode(bytes.NewReader(rec.Body.Bytes())); im == nil || im.Bounds().Dx() != 64 {
		t.Error("size 64 not honoured")
	}
	if c := color.NRGBAModel.Convert(func() color.Color { im, _ := png.Decode(bytes.NewReader(rec.Body.Bytes())); return im.At(32, 32) }()).(color.NRGBA); c.R < 190 || c.G > 40 || c.A != 255 {
		t.Errorf("resize colour %v", c)
	}
	if ic.calls.Load() != n {
		t.Error("provider called again for a cached master")
	}
	if _, err := os.Stat(filepath.Join(dir, ".icons", "com.google.Chrome@64.png")); err != nil {
		t.Error("sized icon not cached")
	}
	// ETag / 304
	etag := rec.Header().Get("ETag")
	if etag == "" {
		t.Fatal("no ETag")
	}
	if r2 := e.raw("GET", "/api/appicon/com.google.Chrome?size=64", nil, "If-None-Match", etag); r2.Code != 304 || r2.Body.Len() != 0 {
		t.Errorf("conditional = %d", r2.Code)
	}
	if r2 := e.raw("GET", "/api/appicon/com.google.Chrome?size=64", nil, "If-None-Match", `"other"`); r2.Code != 200 {
		t.Errorf("stale etag = %d", r2.Code)
	}
	// default size, bounds, junk
	for _, c := range []struct {
		path string
		code int
	}{
		{"/api/appicon/com.google.Chrome", 200},
		{"/api/appicon/com.google.Chrome?size=256", 200},
		{"/api/appicon/com.google.Chrome?size=32", 200},
		{"/api/appicon/com.google.Chrome?size=31", 400},
		{"/api/appicon/com.google.Chrome?size=257", 400},
		{"/api/appicon/com.google.Chrome?size=abc", 400},
		{"/api/icon/com.missing.noicon.png", 404},
		{"/api/icon/com.bad.png.png", 404}, // provider returned non-PNG bytes
		{"/api/icon/..%2F..%2Fetc%2Fpasswd.png", 400},
		{"/api/icon/.hidden.png", 400},
		{"/api/icon/a..b.png", 400},
		{"/api/appicon/%2e%2e", 400},
	} {
		if rec := e.raw("GET", c.path, nil); rec.Code != c.code {
			t.Errorf("%s = %d want %d", c.path, rec.Code, c.code)
		}
	}
	// a miss is remembered for a minute
	n = ic.calls.Load()
	e.raw("GET", "/api/icon/com.missing.noicon.png", nil)
	if ic.calls.Load() != n {
		t.Error("miss not cached")
	}
	// nothing escaped .icons
	es, _ := os.ReadDir(dir)
	for _, x := range es {
		if strings.HasSuffix(x.Name(), ".png") {
			t.Errorf("png outside .icons: %s", x.Name())
		}
	}
	// no provider = 404
	e2 := newEnv(t, false)
	if rec := e2.raw("GET", "/api/icon/com.google.Chrome.png", nil); rec.Code != 404 {
		t.Errorf("no provider = %d", rec.Code)
	}
	_ = errors.Is
}

func TestCopyRestoreEndpoints(t *testing.T) {
	e := newEnv(t, false)
	e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`)
	e.do(t, "POST", "/api/apps", `{"id":"com.example.Plain","name":"Plain"}`)
	e.do(t, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"D1","assignment":{"action":"browser.scroll"}}`)
	e.do(t, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"C1","assignment":{"type":"key","key":"Cmd+T"}}`)

	code, r := e.do(t, "POST", "/api/preset/com.example.Plain/copy", `{"from":"com.google.Chrome","scope":"all"}`)
	if code != 200 || r["copied"].(float64) != 1 || len(r["skipped"].([]any)) != 1 {
		t.Fatalf("copy: %d %v", code, r)
	}
	sk := r["skipped"].([]any)[0].(map[string]any)
	if sk["control"] != "D1" || sk["bank"] != "base" || sk["reason"] == "" {
		t.Errorf("skipped %v", sk)
	}
	code, r = e.do(t, "POST", "/api/preset/com.example.Plain/copy", `{"from":"com.google.Chrome","scope":"control","bank":"base","control":"D1","force":true}`)
	if code != 200 || r["copied"].(float64) != 1 {
		t.Fatalf("force: %d %v", code, r)
	}
	// copy-from alias with banks
	if code, r = e.do(t, "POST", "/api/preset/com.example.Plain/copy-from", `{"from":"com.google.Chrome","banks":"all"}`); code != 200 {
		t.Errorf("copy-from: %d %v", code, r)
	}
	for _, c := range []struct {
		body  string
		code  int
		field string
	}{
		{`{"from":"com.google.Chrome","scope":"x"}`, 400, "scope"},
		{`{"scope":"all"}`, 400, "from"},
		{`{"from":"com.nope","scope":"all"}`, 400, "from"},
		{`{"from":"com.example.Plain","scope":"all"}`, 400, "from"},
		{`{"from":"com.google.Chrome","scope":"control","bank":"base","control":"D2"}`, 404, "control"},
		{`{`, 400, ""},
	} {
		code, r := e.do(t, "POST", "/api/preset/com.example.Plain/copy", c.body)
		if code != c.code || (c.field != "" && r["field"] != c.field) {
			t.Errorf("%s: %d %v", c.body, code, r)
		}
	}
	if code, _ := e.do(t, "POST", "/api/preset/com.nope/copy", `{"from":"com.google.Chrome","scope":"all"}`); code != 404 {
		t.Errorf("unknown target = %d", code)
	}

	code, r = e.do(t, "POST", "/api/preset/com.google.Chrome/restore", `{"scope":"control","bank":"base","control":"C1"}`)
	if code != 200 || r["ok"] != true || r["effective"] != nil {
		t.Errorf("restore control: %d %v", code, r)
	}
	if code, r = e.do(t, "POST", "/api/preset/com.google.Chrome/restore", `{"scope":"bank","bank":"base"}`); code != 200 {
		t.Errorf("restore bank: %d %v", code, r)
	}
	if code, r = e.do(t, "POST", "/api/preset/com.example.Plain/reset-defaults", ``); code != 400 || r["reason"] != "no-shipped-preset" {
		t.Errorf("reset-defaults of a custom app: %d %v", code, r)
	}
	for _, c := range []string{`{"scope":"nope"}`, `{"scope":"bank"}`, `{"scope":"control","bank":"base"}`} {
		if code, _ := e.do(t, "POST", "/api/preset/com.google.Chrome/restore", c); code != 400 {
			t.Errorf("%s = %d", c, code)
		}
	}
	if code, _ := e.do(t, "POST", "/api/preset/com.nope/restore", `{"scope":"app"}`); code != 404 {
		t.Errorf("restore unknown = %d", code)
	}
	// Resolve from the seed
	s := newEnv(t, true)
	s.do(t, "PUT", "/api/preset/"+"com.blackmagic-design.DaVinciResolve"+"/value", `{"bank":"col","control":"Exposure","key":"label","value":"Mine"}`)
	if code, r := s.do(t, "POST", "/api/preset/com.blackmagic-design.DaVinciResolve/restore", `{"scope":"app"}`); code != 200 {
		t.Errorf("resolve restore: %d %v", code, r)
	}
}

func zipBody(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	for n, c := range files {
		w, _ := zw.Create(n)
		w.Write([]byte(c))
	}
	zw.Close()
	return b.Bytes()
}

func TestExportImportEndpoints(t *testing.T) {
	e := newEnv(t, false)
	e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`)
	e.do(t, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"C1","assignment":{"type":"key","key":"Cmd+T"}}`)

	rec := e.raw("GET", "/api/export?app=all", nil)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/zip" || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("export: %d %v", rec.Code, rec.Header())
	}
	all := rec.Body.Bytes()
	if rec := e.raw("GET", "/api/export?app=com.google.Chrome", nil); rec.Code != 200 {
		t.Errorf("export one = %d", rec.Code)
	}
	if rec := e.raw("GET", "/api/preset/com.google.Chrome/export", nil); rec.Code != 200 {
		t.Errorf("export alias = %d", rec.Code)
	}
	if rec := e.raw("GET", "/api/export?app=com.nope", nil); rec.Code != 404 {
		t.Errorf("export unknown = %d", rec.Code)
	}

	e2 := newEnv(t, false)
	code, r := func() (int, map[string]any) {
		rec := e2.raw("POST", "/api/import?dry-run=1", all, "Content-Type", "application/zip")
		return rec.Code, decodeMap(t, rec)
	}()
	if code != 200 || r["dryRun"] != true || len(r["files"].([]any)) != 2 {
		t.Fatalf("dry run: %d %v", code, r)
	}
	if _, ok := e2.store.Snapshot().Preset("com.google.Chrome"); ok {
		t.Fatal("dry run wrote")
	}
	// multipart upload, real import
	var mb bytes.Buffer
	mw := multipart.NewWriter(&mb)
	fw, _ := mw.CreateFormFile("file", "x.zip")
	fw.Write(all)
	mw.Close()
	rec = e2.raw("POST", "/api/import?dryRun=1", mb.Bytes(), "Content-Type", mw.FormDataContentType())
	if rec.Code != 200 {
		t.Fatalf("multipart dry run: %d %s", rec.Code, rec.Body.String())
	}
	rec = e2.raw("POST", "/api/import?token="+decodeMap(t, rec)["token"].(string), nil)
	if rec.Code != 200 {
		t.Fatalf("multipart import: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := e2.store.Snapshot().Preset("com.google.Chrome"); !ok {
		t.Fatal("import did not add Chrome")
	}
	if code, r := e2.do(t, "POST", "/api/undo", ""); code != 200 || r["label"] != "Import presets" {
		t.Errorf("import not one undo step: %v", r)
	}
	// rejects
	evil := zipBody(t, map[string]string{"manifest.json": `{"kind":"2t5-deck-plus-presets","schema":1}`, "../x.json": "{}"})
	rec = e2.raw("POST", "/api/import", evil)
	m := decodeMap(t, rec)
	if rec.Code != 400 || m["problems"] == nil || !strings.Contains(m["error"].(string), "unsafe") {
		t.Errorf("zip slip: %d %v", rec.Code, m)
	}
	big := bytes.Repeat([]byte{'x'}, 5<<20+10)
	if rec := e2.raw("POST", "/api/import", big); rec.Code != 413 {
		t.Errorf("too big = %d", rec.Code)
	}
	if rec := e2.raw("POST", "/api/import", nil); rec.Code != 400 {
		t.Errorf("empty = %d", rec.Code)
	}
	if rec := e2.raw("POST", "/api/import", []byte("garbage")); rec.Code != 400 {
		t.Errorf("garbage = %d", rec.Code)
	}
	var nf bytes.Buffer
	mw = multipart.NewWriter(&nf)
	mw.WriteField("other", "x")
	mw.Close()
	if rec := e2.raw("POST", "/api/import", nf.Bytes(), "Content-Type", mw.FormDataContentType()); rec.Code != 400 {
		t.Errorf("multipart without file = %d", rec.Code)
	}
	if rec := e2.raw("POST", "/api/import", all, "Origin", "https://evil.example"); rec.Code != 403 {
		t.Errorf("cross-origin import = %d", rec.Code)
	}
}

func decodeMap(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	e := &env{}
	_ = e
	m := map[string]any{}
	if err := jsonUnmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("bad JSON %q: %v", rec.Body.String(), err)
	}
	return m
}

func TestBackupEndpoints(t *testing.T) {
	e := newEnv(t, false)
	e.do(t, "POST", "/api/apps", `{"id":"com.a.b"}`)
	e.do(t, "PUT", "/api/preset/com.a.b/assign", `{"bank":"base","control":"C1","assignment":{"type":"key","key":"A"}}`)
	for _, base := range []string{"/api/backups", "/api/snapshots"} {
		code, r := e.do(t, "GET", base, "")
		bs := r["backups"].([]any)
		if code != 200 || len(bs) < 1 || r["keep"].(float64) != 14 {
			t.Fatalf("%s: %d %v", base, code, r)
		}
	}
	_, r := e.do(t, "GET", "/api/backups", "")
	name := r["backups"].([]any)[0].(map[string]any)["name"].(string)
	code, r := e.do(t, "POST", "/api/snapshots/"+name+"/restore", "")
	if code != 200 || r["ok"] != true {
		t.Fatalf("restore: %d %v", code, r)
	}
	if _, ok := e.store.Snapshot().Preset("com.a.b"); ok {
		t.Error("restore kept the later app")
	}
	if code, _ := e.do(t, "POST", "/api/undo", ""); code != 200 {
		t.Fatal()
	}
	if _, ok := e.store.Snapshot().Preset("com.a.b"); !ok {
		t.Error("undo of restore failed")
	}
	if code, _ := e.do(t, "POST", "/api/backups/presets-20200101-000000.zip/restore", ""); code != 404 {
		t.Errorf("unknown backup = %d", code)
	}
	if code, _ := e.do(t, "POST", "/api/backups/..%2Fx/restore", ""); code != 404 {
		t.Errorf("traversal backup = %d", code)
	}
}

func TestCoverageSearchHistoryWarningsEndpoints(t *testing.T) {
	e := newEnv(t, false)
	e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`)
	e.do(t, "PUT", "/api/preset/_global/assign", `{"bank":"base","control":"C2","assignment":{"type":"key","key":"Cmd+Z","label":"Oops"}}`)
	code, r := e.do(t, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"C1","assignment":{"type":"key","key":"Cmd+T"}}`)
	if code != 200 || r["warnings"] == nil || len(r["warnings"].([]any)) != 0 {
		t.Fatalf("assign warnings field: %v", r)
	}
	code, r = e.do(t, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"C3","assignment":{"type":"key","key":"cmd+t"}}`)
	w := r["warnings"].([]any)
	if code != 200 || len(w) != 1 || w[0].(map[string]any)["kind"] != "duplicate-key" || w[0].(map[string]any)["severity"] != "hint" {
		t.Fatalf("duplicate hint: %v", r)
	}
	code, r = e.do(t, "PUT", "/api/preset/com.google.Chrome/value", `{"bank":"base","control":"C3","key":"key","value":"Cmd+T"}`)
	if code != 200 || len(r["warnings"].([]any)) != 1 {
		t.Errorf("value warnings: %v", r)
	}
	// the assignment is still saved: hints never block
	if p, _ := e.store.Snapshot().Preset("com.google.Chrome"); p.Banks["base"]["C3"] == nil {
		t.Error("duplicate blocked the save")
	}

	code, r = e.do(t, "GET", "/api/coverage?app=com.google.Chrome", "")
	banks := r["banks"].([]any)
	b0 := banks[0].(map[string]any)
	if code != 200 || b0["bank"] != "base" || b0["assigned"].(float64) != 2 || b0["inherited"].(float64) != 1 || len(b0["controls"].([]any)) != len(ControlIDs()) {
		t.Errorf("coverage: %d %v", code, b0)
	}
	if code, _ = e.do(t, "GET", "/api/coverage", ""); code != 400 {
		t.Errorf("coverage no app = %d", code)
	}
	if code, _ = e.do(t, "GET", "/api/coverage?app=com.nope", ""); code != 404 {
		t.Errorf("coverage unknown = %d", code)
	}
	code, r = e.do(t, "GET", "/api/search?q=oops", "")
	h := r["hits"].([]any)
	if code != 200 || len(h) != 1 || h[0].(map[string]any)["control"] != "C2" || h[0].(map[string]any)["app"] != "_global" {
		t.Errorf("search: %v", r)
	}
	if code, r = e.do(t, "GET", "/api/search?q=", ""); code != 200 || len(r["hits"].([]any)) != 0 {
		t.Errorf("empty search: %d %v", code, r)
	}
	if code, _ = e.do(t, "GET", "/api/search?q=a&limit=0", ""); code != 400 {
		t.Errorf("bad limit = %d", code)
	}

	code, r = e.do(t, "GET", "/api/history", "")
	es := r["entries"].([]any)
	cur := int(r["current"].(float64))
	if code != 200 || cur != len(es)-1 || es[0].(map[string]any)["label"] != "Start of history" || es[cur].(map[string]any)["t"].(float64) < 1e12 {
		t.Fatalf("history: %v", r)
	}
	if code, r = e.do(t, "POST", "/api/history/jump", `{"index":1}`); code != 200 || r["current"].(float64) != 1 {
		t.Fatalf("jump: %d %v", code, r)
	}
	if _, ok := e.store.Snapshot().Preset("com.google.Chrome"); !ok {
		t.Error("jump 1 should keep the first change (Add app)")
	}
	if p, _ := e.store.Snapshot().Preset("com.google.Chrome"); p.Banks["base"]["C1"] != nil {
		t.Error("jump 1 kept later changes")
	}
	for _, b := range []string{`{"index":99}`, `{"index":-1}`, `{}`, `{`} {
		if code, _ = e.do(t, "POST", "/api/history/jump", b); code != 400 {
			t.Errorf("jump %s = %d", b, code)
		}
	}
	if code, r = e.do(t, "POST", "/api/redo", ""); code != 200 || r["ok"] != true {
		t.Errorf("redo after jump: %v", r)
	}

	code, r = e.do(t, "GET", "/api/warnings", "")
	if code != 200 || r["warnings"] == nil || r["counts"] == nil {
		t.Errorf("warnings: %d %v", code, r)
	}
	// a messy file dropped on disk shows up and does not break the API
	os.WriteFile(filepath.Join(e.store.Dir(), "com.messy.json"), []byte(`{"schema":1,"id":"com.messy","name":"M","packs":[],"banks":{"base":{"C1":{"type":"warp"}}},"settings":{}}`), 0o644)
	os.WriteFile(filepath.Join(e.store.Dir(), "com.broken.json"), []byte(`nope`), 0o644)
	e.store.CheckExternal()
	for _, pe := range e.store.PendingExternal() { // a type nobody can judge is held back (wave O): approve it
		if err := e.store.ApproveExternal(pe.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	_, r = e.do(t, "GET", "/api/warnings", "")
	if !strings.Contains(string(mustJSON(r)), "unknown-type") || !strings.Contains(string(mustJSON(r)), "unreadable-file") {
		t.Errorf("messy warnings: %v", r)
	}
	if code, _ = e.do(t, "GET", "/api/preset/com.messy", ""); code != 200 {
		t.Errorf("messy preset get = %d", code)
	}
}

var _ http.Handler

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
func mustJSON(v any) []byte               { b, _ := json.Marshal(v); return b }
