package editorapi

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"2t5-deck-plus/internal/preset"
)

func TestNativeExportImportRoundTrip(t *testing.T) {
	src := newEnv(t, true)
	src.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`)
	src.do(t, "PUT", "/api/preset/com.google.Chrome/assign", `{"bank":"base","control":"C1","assignment":{"type":"key","key":"Cmd+T"}}`)
	nio := &NativeIO{Store: src.store}

	data, name, err := nio.ExportBytes("com.google.Chrome")
	if err != nil || !strings.HasPrefix(name, "2t5-deck-plus-presets-com.google.Chrome-") {
		t.Fatal(name, err)
	}
	// the bytes are the HTTP export's: same entries
	rec := src.raw("GET", "/api/export?app=com.google.Chrome", nil)
	if rec.Code != 200 || len(rec.Body.Bytes()) == 0 {
		t.Fatal(rec.Code)
	}
	dir := t.TempDir()
	path, err := nio.WriteExport(filepath.Join(dir, "chrome"), data)
	if err != nil || filepath.Ext(path) != ".zip" {
		t.Fatalf("WriteExport = %q %v", path, err)
	}
	if st, err := os.Stat(path); err != nil || st.Size() != int64(len(data)) {
		t.Fatalf("file: %v", err)
	}
	if _, err := nio.WriteExport("", data); err == nil {
		t.Error("empty path accepted")
	}
	if _, err := nio.WriteExport(filepath.Join(dir, "no", "such", "dir", "x.zip"), data); err == nil {
		t.Error("unwritable path accepted")
	}

	// import into a fresh store: preview writes nothing, apply is one undo step
	dst := newEnv(t, false)
	n2 := &NativeIO{Store: dst.store}
	before := dst.store.Snapshot().Seq
	pv, err := n2.PreviewImport(path)
	if err != nil || pv.Token == "" || !pv.DryRun || pv.Name != filepath.Base(path) {
		t.Fatalf("preview = %+v %v", pv, err)
	}
	if len(pv.Files) != 1 || pv.Files[0].ID != "com.google.Chrome" || pv.Files[0].Status != "new" {
		t.Errorf("diff = %+v", pv.Files)
	}
	if dst.store.Snapshot().Seq != before {
		t.Fatal("preview wrote")
	}
	res, err := n2.ApplyImport(pv.Token, false)
	if err != nil || !res.OK || res.Seq <= before {
		t.Fatalf("apply = %+v %v", res, err)
	}
	if p, ok := dst.store.Snapshot().Preset("com.google.Chrome"); !ok || p.Assigned() != 1 {
		t.Error("imported preset missing")
	}
	// single use
	if _, err := n2.ApplyImport(pv.Token, false); !errors.Is(err, ErrUnknownToken) {
		t.Errorf("token reuse = %v", err)
	}
	if _, err := n2.ApplyImport("nope", false); !errors.Is(err, ErrUnknownToken) {
		t.Errorf("unknown token = %v", err)
	}
	if _, ok, _ := dst.store.Undo(); !ok {
		t.Fatal("import is not undoable")
	}
	if _, ok := dst.store.Snapshot().Preset("com.google.Chrome"); ok {
		t.Error("undo kept the import")
	}
}

func TestNativeImportRefusals(t *testing.T) {
	e := newEnv(t, false)
	n := &NativeIO{Store: e.store}
	dir := t.TempDir()
	if _, err := n.PreviewImport(filepath.Join(dir, "missing.zip")); err == nil {
		t.Error("missing file accepted")
	}
	empty := filepath.Join(dir, "empty.zip")
	os.WriteFile(empty, nil, 0o644)
	if _, err := n.PreviewImport(empty); err == nil {
		t.Error("empty file accepted")
	}
	junk := filepath.Join(dir, "junk.zip")
	os.WriteFile(junk, []byte("not a zip at all"), 0o644)
	_, err := n.PreviewImport(junk)
	var ie *preset.ImportError
	if !errors.As(err, &ie) {
		t.Errorf("junk = %v", err)
	}
	big := filepath.Join(dir, "big.zip")
	os.WriteFile(big, make([]byte, maxImportBytes+10), 0o644)
	if _, err := n.PreviewImport(big); err == nil || !strings.Contains(err.Error(), "5 MB") {
		t.Errorf("big = %v", err)
	}
	if len(n.pending) != 0 {
		t.Error("a refused import left a pending token")
	}
}

func TestNativePendingExpiryAndLimit(t *testing.T) {
	e := newEnv(t, false)
	e.do(t, "POST", "/api/apps", `{"id":"com.google.Chrome"}`)
	data, _, _ := e.store.Export("all")
	path := filepath.Join(t.TempDir(), "a.zip")
	os.WriteFile(path, data, 0o644)
	now := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)
	n := &NativeIO{Store: e.store, Now: func() time.Time { return now }}
	first, err := n.PreviewImport(path)
	if err != nil {
		t.Fatal(err)
	}
	// expired
	now = now.Add(11 * time.Minute)
	if _, err := n.ApplyImport(first.Token, false); !errors.Is(err, ErrUnknownToken) {
		t.Errorf("expired token = %v", err)
	}
	// at most maxPending tokens: the oldest goes
	var toks []string
	for i := 0; i < maxPending+2; i++ {
		now = now.Add(time.Second)
		pv, err := n.PreviewImport(path)
		if err != nil {
			t.Fatal(err)
		}
		toks = append(toks, pv.Token)
	}
	if len(n.pending) != maxPending {
		t.Errorf("pending = %d", len(n.pending))
	}
	if _, err := n.ApplyImport(toks[0], false); !errors.Is(err, ErrUnknownToken) {
		t.Errorf("evicted token = %v", err)
	}
	if _, err := n.ApplyImport(toks[len(toks)-1], false); err != nil {
		t.Errorf("newest token = %v", err)
	}
}
