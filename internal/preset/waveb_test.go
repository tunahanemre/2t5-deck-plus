package preset

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"2t5-deck-plus/internal/actions"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

// clockStore: empty store (Global only) with a controllable clock.
func clockStore(t *testing.T, keep int) (*Store, string, *clock) {
	t.Helper()
	c := &clock{time.Date(2026, 11, 3, 10, 0, 0, 0, time.Local)}
	dir := filepath.Join(t.TempDir(), "presets")
	s, err := Open(dir, Options{LiveResolvePath: "-", PollInterval: -1, Now: c.now, BackupKeep: keep})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, dir, c
}

func mustAssign(t *testing.T, s *Store, app, bank, ctl string, a map[string]any) {
	t.Helper()
	if err := s.Assign(app, bank, ctl, a); err != nil {
		t.Fatalf("assign %s %s: %v", app, ctl, err)
	}
}

func chromeAndSafari(t *testing.T) (*Store, string, *clock) {
	s, dir, c := clockStore(t, 0)
	if _, err := s.AddApp("com.google.Chrome", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddApp("com.example.Plain", "Plain", nil); err != nil { // generic only
		t.Fatal(err)
	}
	mustAssign(t, s, "com.google.Chrome", "base", "D1", map[string]any{"action": "browser.scroll"})
	mustAssign(t, s, "com.google.Chrome", "base", "C1", map[string]any{"type": "key", "key": "Cmd+T"})
	mustAssign(t, s, "com.google.Chrome", "shift", "C1", map[string]any{"type": "key", "key": "Cmd+Shift+T"})
	return s, dir, c
}

func TestCopy(t *testing.T) {
	type tc struct {
		name, scope, bank, control, mode string
		force                            bool
		copied, skipped                  int
		wantBase                         []string // controls expected in Plain base
		wantErr                          string
	}
	cases := []tc{
		{name: "all merge skips scroll (no browser pack)", scope: "all", copied: 2, skipped: 1, wantBase: []string{"C1"}},
		{name: "all merge force", scope: "all", force: true, copied: 3, wantBase: []string{"C1", "D1"}},
		{name: "bank", scope: "bank", bank: "shift", copied: 1},
		{name: "control", scope: "control", bank: "base", control: "C1", copied: 1, wantBase: []string{"C1"}},
		{name: "control not assigned", scope: "control", bank: "base", control: "C2", wantErr: "control"},
		{name: "bad scope", scope: "weird", wantErr: "scope"},
		{name: "bad mode", scope: "all", mode: "x", wantErr: "mode"},
		{name: "bank scope needs bank", scope: "bank", wantErr: "bank"},
		{name: "bad bank", scope: "bank", bank: "col", copied: 0, wantErr: "bank"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, _, _ := chromeAndSafari(t)
			res, err := s.Copy("com.example.Plain", "com.google.Chrome", c.scope, c.bank, c.control, c.mode, c.force)
			if c.wantErr != "" {
				var fe *actions.FieldError
				if !errors.As(err, &fe) && !errors.Is(err, ErrNoControl) {
					t.Fatalf("want error about %s, got %v", c.wantErr, err)
				}
				if fe != nil && fe.Field != c.wantErr {
					t.Errorf("field %q want %q", fe.Field, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if res.Copied != c.copied || len(res.Skipped) != c.skipped {
				t.Errorf("copied %d skipped %v", res.Copied, res.Skipped)
			}
			p, _ := s.Snapshot().Preset("com.example.Plain")
			if c.wantBase != nil {
				for _, ctl := range c.wantBase {
					if _, ok := p.Banks["base"][ctl]; !ok {
						t.Errorf("base lacks %s", ctl)
					}
				}
				if len(p.Banks["base"]) != len(c.wantBase) {
					t.Errorf("base has %d controls", len(p.Banks["base"]))
				}
			}
			// exactly one undo step
			n := 0
			for {
				_, ok, _ := s.Undo()
				if !ok {
					break
				}
				n++
			}
			if n < 1 {
				t.Error("nothing to undo")
			}
		})
	}
}

func TestCopyModesAndGuards(t *testing.T) {
	s, _, _ := chromeAndSafari(t)
	mustAssign(t, s, "com.example.Plain", "base", "C2", map[string]any{"type": "key", "key": "Cmd+Z"})
	mustAssign(t, s, "com.example.Plain", "base", "D1", map[string]any{"type": "scrollDelta", "linesPerDetent": 7})
	// merge keeps C2
	if _, err := s.Copy("com.example.Plain", "com.google.Chrome", "bank", "base", "", "merge", false); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Snapshot().Preset("com.example.Plain")
	if _, ok := p.Banks["base"]["C2"]; !ok {
		t.Error("merge dropped C2")
	}
	// replace drops C2, but the skipped D1 (browser.scroll needs the pack) keeps Plain's own value
	if _, err := s.Copy("com.example.Plain", "com.google.Chrome", "bank", "base", "", "replace", false); err != nil {
		t.Fatal(err)
	}
	p, _ = s.Snapshot().Preset("com.example.Plain")
	if _, ok := p.Banks["base"]["C2"]; ok {
		t.Error("replace kept C2")
	}
	if v := p.Banks["base"]["D1"]["linesPerDetent"]; v == nil || fmt.Sprint(v) != "7" {
		t.Errorf("replace overwrote a skipped control: %v", p.Banks["base"]["D1"])
	}
	// guards
	if _, err := s.Copy("com.example.Plain", "com.example.Plain", "all", "", "", "", false); err == nil {
		t.Error("self copy allowed")
	}
	if _, err := s.Copy("com.example.Plain", "nope", "all", "", "", "", false); err == nil {
		t.Error("missing source allowed")
	}
	if _, err := s.Copy("nope", "com.google.Chrome", "all", "", "", "", false); !errors.Is(err, ErrNoPreset) {
		t.Errorf("missing target: %v", err)
	}
	if _, err := s.Copy("com.example.Plain", ResolveID, "all", "", "", "", false); err == nil {
		t.Error("copy from missing Resolve allowed")
	}
}

func TestRestoreDefaultsApp(t *testing.T) {
	s, _, _ := chromeAndSafari(t)
	if err := s.RestoreDefaults("com.google.Chrome", "control", "base", "C1"); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Snapshot().Preset("com.google.Chrome")
	if _, ok := p.Banks["base"]["C1"]; ok || p.Banks["base"]["D1"] == nil {
		t.Errorf("control scope: %v", p.Banks)
	}
	if err := s.RestoreDefaults("com.google.Chrome", "bank", "shift", ""); err != nil {
		t.Fatal(err)
	}
	p, _ = s.Snapshot().Preset("com.google.Chrome")
	if _, ok := p.Banks["shift"]; ok {
		t.Error("bank scope left the bank")
	}
	_ = s.SetSetting("com.google.Chrome", "x", 1)
	// Chrome here has no recorded templateId and two templates share its bundle
	// id: app scope degrades to a guess instead of refusing.
	info, err := s.RestoreDefaultsInfo("com.google.Chrome", "app", "", "")
	if err != nil || info.Source != "template-guess" || info.TemplateName == "" {
		t.Fatalf("ambiguous chrome restore: %v %+v", err, info)
	}
	p, _ = s.Snapshot().Preset("com.google.Chrome")
	if p.Assigned() != info.Controls || p.Name == "" || p.Extra[TemplateKey] != info.TemplateID {
		t.Errorf("guess restore: %+v", p)
	}
	for _, bad := range [][3]string{{"x", "", ""}, {"bank", "", ""}, {"control", "base", ""}, {"control", "col", "C1"}} {
		if err := s.RestoreDefaults("com.google.Chrome", bad[0], bad[1], bad[2]); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	if err := s.RestoreDefaults("nope", "app", "", ""); !errors.Is(err, ErrNoPreset) {
		t.Errorf("missing preset: %v", err)
	}
}

func TestRestoreDefaultsResolveFromSeed(t *testing.T) {
	s, dir := seededStore(t)
	seedFile := filepath.Join(dir, ".seed", ResolveID+".json")
	seedBytes, err := os.ReadFile(seedFile)
	if err != nil {
		t.Fatalf("seed copy not written: %v", err)
	}
	orig, _ := s.Snapshot().Preset(ResolveID)
	var bank, ctl string
	var origA Assignment
	for bn, bm := range orig.Banks {
		for cn, a := range bm {
			bank, ctl, origA = bn, cn, cloneMap(a)
		}
	}
	origBytes := marshalAny(origA)
	if err := s.SetValue(ResolveID, bank, ctl, "label", "changed"); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreDefaults(ResolveID, "control", bank, ctl); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Snapshot().Preset(ResolveID)
	if !bytes.Equal(marshalAny(p.Banks[bank][ctl]), origBytes) {
		t.Errorf("control not restored: %s vs %s", marshalAny(p.Banks[bank][ctl]), origBytes)
	}
	// whole app: wreck it, restore, equal to the seed file
	_ = s.Unassign(ResolveID, bank, ctl)
	_ = s.SetSetting(ResolveID, "zzz", 1)
	if err := s.RestoreDefaults(ResolveID, "app", "", ""); err != nil {
		t.Fatal(err)
	}
	cur, _ := os.ReadFile(filepath.Join(dir, ResolveID+".json"))
	if !bytes.Equal(cur, seedBytes) {
		t.Error("app scope did not reproduce the seed")
	}
	// the seed copy is never overwritten (a reopen keeps it)
	s.Close()
	s2, err := Open(dir, Options{LiveResolvePath: "-", PollInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if b, _ := os.ReadFile(seedFile); !bytes.Equal(b, seedBytes) {
		t.Error("seed copy changed")
	}
	// no .seed file: fall back to the seed sources
	os.Remove(seedFile)
	s3, _ := Open(dir, Options{SeedFrom: filepath.Join("testdata", "2t5-deck-plus-repo-bindings.json"), LiveResolvePath: filepath.Join("testdata", "live-ld-resolve-bindings.json"), PollInterval: -1})
	defer s3.Close()
	if err := s3.RestoreDefaults(ResolveID, "app", "", ""); err != nil {
		t.Errorf("fallback: %v", err)
	}
}

func listBackups(dir string) []string {
	es, _ := os.ReadDir(filepath.Join(dir, ".backups"))
	var out []string
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestDailyBackup(t *testing.T) {
	s, dir, c := clockStore(t, 3)
	if len(listBackups(dir)) != 0 {
		t.Fatal("backup before any write")
	}
	if _, err := s.AddApp("com.a.b", "", nil); err != nil {
		t.Fatal(err)
	}
	if got := listBackups(dir); len(got) != 1 {
		t.Fatalf("first write of the day: %v", got)
	}
	c.t = c.t.Add(time.Hour)
	mustAssign(t, s, "com.a.b", "base", "C1", map[string]any{"type": "key", "key": "A"})
	if got := listBackups(dir); len(got) != 1 {
		t.Fatalf("second write same day: %v", got)
	}
	// the backup holds the state BEFORE the write, presets only
	zr, err := zip.OpenReader(filepath.Join(dir, ".backups", listBackups(dir)[0]))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	zr.Close()
	if strings.Join(names, ",") != "manifest.json,_global.json" {
		t.Errorf("zip content %v", names)
	}
	// a no-op write (no change) never backs up on a new day
	c.t = c.t.Add(24 * time.Hour)
	_ = s.Unassign("com.a.b", "base", "Z9")
	if got := listBackups(dir); len(got) != 1 {
		t.Fatalf("no-op on new day: %v", got)
	}
	// new day, real write; reopen the same day does not add one
	_ = s.SetSetting("com.a.b", "k", 1)
	if got := listBackups(dir); len(got) != 2 {
		t.Fatalf("new day: %v", got)
	}
	s.Close()
	s2, _ := Open(dir, Options{LiveResolvePath: "-", PollInterval: -1, Now: c.now, BackupKeep: 3})
	defer s2.Close()
	_ = s2.SetSetting("com.a.b", "k", 2)
	if got := listBackups(dir); len(got) != 2 {
		t.Fatalf("reopen same day: %v", got)
	}
	// prune: keep 3
	for i := 0; i < 4; i++ {
		c.t = c.t.Add(24 * time.Hour)
		_ = s2.SetSetting("com.a.b", "k", 10+i)
	}
	got := listBackups(dir)
	if len(got) != 3 {
		t.Fatalf("prune: %v", got)
	}
	if bs := s2.Backups(); len(bs) != 3 || bs[0].Name < bs[2].Name {
		t.Errorf("Backups order: %v", bs)
	}
	// dot dirs are never inside a backup
	hist := filepath.Join(dir, ".history")
	if _, err := os.Stat(hist); err != nil {
		t.Fatal("expected a .history dir")
	}
	zr, _ = zip.OpenReader(filepath.Join(dir, ".backups", got[0]))
	for _, f := range zr.File {
		if strings.Contains(f.Name, "/") || strings.HasPrefix(f.Name, ".") {
			t.Errorf("backup holds %s", f.Name)
		}
	}
	zr.Close()
}

func TestRestoreBackupUndoable(t *testing.T) {
	s, dir, c := clockStore(t, 0)
	_, _ = s.AddApp("com.a.b", "", nil)
	mustAssign(t, s, "com.a.b", "base", "C1", map[string]any{"type": "key", "key": "A"})
	name := s.Backups()[0].Name // state before the first write: Global only
	c.t = c.t.Add(time.Second)
	rep, err := s.RestoreBackup(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Snapshot().Preset("com.a.b"); ok {
		t.Error("app added after the backup still there")
	}
	found := false
	for _, f := range rep.Files {
		found = found || (f.ID == "com.a.b" && f.Status == "removed")
	}
	if !found {
		t.Errorf("report lacks removal: %+v", rep.Files)
	}
	if _, err := os.Stat(filepath.Join(dir, "com.a.b.json")); !os.IsNotExist(err) {
		t.Error("file not removed")
	}
	if _, ok, _ := s.Undo(); !ok {
		t.Fatal("restore not undoable")
	}
	p, ok := s.Snapshot().Preset("com.a.b")
	if !ok || p.Banks["base"]["C1"] == nil {
		t.Error("undo did not bring the app back")
	}
	for _, bad := range []string{"../x.zip", "presets-1.zip", "nope", ""} {
		if _, err := s.RestoreBackup(bad); !errors.Is(err, ErrNoBackup) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func mkZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for n, b := range files {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(b))
	}
	zw.Close()
	return buf.Bytes()
}

const manifestOK = `{"kind":"2t5-deck-plus-presets","schema":1,"created":"x","app":"all","files":[]}`
const presetChrome = `{"schema":1,"id":"com.google.Chrome","name":"Google Chrome","packs":["browser","generic"],"banks":{"base":{"C1":{"type":"key","key":"Cmd+R"}}},"settings":{}}`

func TestExportImportRoundTrip(t *testing.T) {
	s, _, _ := chromeAndSafari(t)
	data, name, err := s.Export("all")
	if err != nil || !strings.HasPrefix(name, "2t5-deck-plus-presets-all-") {
		t.Fatal(err, name)
	}
	one, name1, err := s.Export("com.google.Chrome")
	if err != nil || !strings.Contains(name1, "com.google.Chrome") {
		t.Fatal(err, name1)
	}
	if _, _, err := s.Export("nope"); !errors.Is(err, ErrNoPreset) {
		t.Errorf("export unknown: %v", err)
	}
	// import into a fresh store
	s2, dir2, _ := clockStore(t, 0)
	rep, err := s2.Import(data, true)
	if err != nil || !rep.DryRun {
		t.Fatal(err)
	}
	if got := listBackups(dir2); len(got) != 0 {
		t.Errorf("dry run backed up: %v", got)
	}
	if _, ok := s2.Snapshot().Preset("com.google.Chrome"); ok {
		t.Fatal("dry run wrote")
	}
	status := map[string]string{}
	for _, f := range rep.Files {
		status[f.ID] = f.Status
	}
	if status["com.google.Chrome"] != "new" || status["_global"] != "unchanged" {
		t.Errorf("dry-run report %v", status)
	}
	before := len(s2.undo)
	rep, err = s2.Import(data, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.undo) != before+1 {
		t.Errorf("import is %d undo steps", len(s2.undo)-before)
	}
	// the import's own backup, plus the day's first (daily) backup
	if got := listBackups(dir2); len(got) != 2 || !strings.HasSuffix(got[0], "-daily.zip") || !strings.HasSuffix(got[1], "-import.zip") {
		t.Errorf("import backups: %v", got)
	}
	a, _ := s.Snapshot().Preset("com.google.Chrome")
	b, ok := s2.Snapshot().Preset("com.google.Chrome")
	if !ok || !bytes.Equal(marshalOf(a), marshalOf(b)) {
		t.Error("round trip differs")
	}
	if _, ok, _ := s2.Undo(); !ok {
		t.Fatal()
	}
	if _, ok := s2.Snapshot().Preset("com.google.Chrome"); ok {
		t.Error("undo did not remove the import")
	}
	// single-preset zip merges, others untouched
	if _, err := s2.Import(one, false); err != nil {
		t.Fatal(err)
	}
	if _, ok := s2.Snapshot().Preset("com.example.Plain"); ok {
		t.Error("single import brought another app")
	}
	// second import of identical data: unchanged, no extra undo step
	n := len(s2.undo)
	rep, _ = s2.Import(one, false)
	if len(s2.undo) != n || rep.Files[0].Status != "unchanged" {
		t.Errorf("idempotent import: %v undo %d->%d", rep.Files, n, len(s2.undo))
	}
}

func TestImportRejects(t *testing.T) {
	s, dir, _ := clockStore(t, 0)
	big := strings.Repeat("a", maxEntryBytes+10)
	cases := []struct {
		name  string
		files map[string]string
		want  string
	}{
		{"zip slip", map[string]string{"manifest.json": manifestOK, "../evil.json": presetChrome}, "unsafe path"},
		{"subdir", map[string]string{"manifest.json": manifestOK, "sub/com.google.Chrome.json": presetChrome}, "unsafe path"},
		{"absolute", map[string]string{"manifest.json": manifestOK, "/etc/x.json": presetChrome}, "unsafe path"},
		{"backslash", map[string]string{"manifest.json": manifestOK, `..\x.json`: presetChrome}, "unsafe path"},
		{"no manifest", map[string]string{"com.google.Chrome.json": presetChrome}, "manifest.json is missing"},
		{"wrong kind", map[string]string{"manifest.json": `{"kind":"other","schema":1}`, "com.google.Chrome.json": presetChrome}, "not a 2T5 DECK+"},
		{"future schema", map[string]string{"manifest.json": `{"kind":"2t5-deck-plus-presets","schema":9}`, "com.google.Chrome.json": presetChrome}, "schema 9"},
		{"name/id mismatch", map[string]string{"manifest.json": manifestOK, "other.json": presetChrome}, "does not match"},
		{"bad json", map[string]string{"manifest.json": manifestOK, "a.json": "{"}, "a.json"},
		{"bad bank", map[string]string{"manifest.json": manifestOK, "com.google.Chrome.json": strings.Replace(presetChrome, `"base"`, `"col"`, 1)}, "not a valid bank"},
		{"bad key spec", map[string]string{"manifest.json": manifestOK, "com.google.Chrome.json": strings.Replace(presetChrome, "Cmd+R", "Cmb+R", 1)}, "key"},
		{"future preset schema", map[string]string{"manifest.json": manifestOK, "com.google.Chrome.json": strings.Replace(presetChrome, `"schema":1`, `"schema":5`, 1)}, "schema 5"},
		{"huge entry", map[string]string{"manifest.json": manifestOK, "com.google.Chrome.json": big}, "too large"},
		{"empty", map[string]string{"manifest.json": manifestOK}, "no presets"},
		{"stray file", map[string]string{"manifest.json": manifestOK, "evil.sh": "x"}, "unexpected file"},
	}
	for _, c := range cases {
		_, err := s.Import(mkZip(t, c.files), false)
		var ie *ImportError
		if !errors.As(err, &ie) || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v (want %q)", c.name, err, c.want)
		}
	}
	if _, err := s.Import([]byte("not a zip"), false); err == nil {
		t.Error("garbage accepted")
	}
	// nothing was written, no backup, no stray files
	es, _ := os.ReadDir(dir)
	var names []string
	for _, e := range es {
		if e.Name() != canaryFileName { // the silent-loss canary (wave F) is the store's own
			names = append(names, e.Name())
		}
	}
	if len(names) != 1 || names[0] != "_global.json" {
		t.Errorf("dir after rejects: %v", names)
	}
	// macOS junk entries are ignored
	ok := mkZip(t, map[string]string{"manifest.json": manifestOK, "com.google.Chrome.json": presetChrome, "__MACOSX/._x": "x", ".DS_Store": "x"})
	if _, err := s.Import(ok, false); err != nil {
		t.Errorf("junk entries: %v", err)
	}
}

func TestCoverageSearchWarningsHistory(t *testing.T) {
	s, _, _ := chromeAndSafari(t)
	mustAssign(t, s, "_global", "base", "C2", map[string]any{"type": "key", "key": "Cmd+Z", "label": "Undo it"})
	mustAssign(t, s, "_global", "base", "C1", map[string]any{"type": "key", "key": "Cmd+Q"})
	ctls := []string{"D1", "C1", "C2", "C3"}
	cov, ok := s.Snapshot().Coverage("com.google.Chrome", ctls)
	if !ok {
		t.Fatal()
	}
	base := cov.Banks[0]
	if base.Bank != "base" || base.Assigned != 2 || base.Inherited != 1 || base.Unassigned != 1 || len(base.Controls) != 4 {
		t.Errorf("base %+v", base)
	}
	if base.Controls[1].State != "app" || base.Controls[2].State != "global" || base.Controls[3].State != "none" {
		t.Errorf("states %+v", base.Controls)
	}
	if len(cov.Banks) != 2 || cov.Banks[1].Bank != "shift" {
		t.Errorf("banks %+v", cov.Banks)
	}
	if cov.Totals.Assigned != 3 {
		t.Errorf("totals %+v", cov.Totals)
	}
	if _, ok := s.Snapshot().Coverage("nope", ctls); ok {
		t.Error("coverage of missing app")
	}
	if g, _ := s.Snapshot().Coverage("global", ctls); g.App != GlobalID || g.Banks[0].Inherited != 0 {
		t.Errorf("global coverage %+v", g)
	}

	hits, _ := s.Snapshot().Search("undo", 10)
	if len(hits) != 1 || hits[0].App != GlobalID || hits[0].Control != "C2" {
		t.Errorf("search label: %+v", hits)
	}
	hits, _ = s.Snapshot().Search("cmd+shift+t", 10)
	if len(hits) != 1 || hits[0].Bank != "shift" {
		t.Errorf("search key: %+v", hits)
	}
	hits, _ = s.Snapshot().Search("chrome scroll", 10)
	if len(hits) != 1 || hits[0].Control != "D1" {
		t.Errorf("search app+action: %+v", hits)
	}
	if h, tr := s.Snapshot().Search("key", 1); len(h) != 1 || !tr {
		t.Errorf("limit/truncated: %v %v", h, tr)
	}
	if h, _ := s.Snapshot().Search("   ", 10); len(h) != 0 {
		t.Error("blank query matched")
	}

	// duplicates: same spec elsewhere in the same app+bank
	mustAssign(t, s, "com.google.Chrome", "base", "C3", map[string]any{"type": "key", "key": "cmd + t"})
	d := s.Snapshot().DuplicatesFor("com.google.Chrome", "base", "C3")
	if len(d) != 1 || d[0].Kind != "duplicate-key" || !strings.Contains(d[0].Message, "C1") {
		t.Errorf("dups %+v", d)
	}
	if d := s.Snapshot().DuplicatesFor("com.google.Chrome", "shift", "C1"); len(d) != 0 {
		t.Errorf("other bank is not a dup: %+v", d)
	}
	if d := s.Snapshot().DuplicatesFor("nope", "base", "C1"); d == nil || len(d) != 0 {
		t.Errorf("missing app: %#v", d)
	}

	// history
	es, cur := s.History()
	if es[0].Index != 0 || es[0].Label != "Start of history" || cur != len(es)-1 {
		t.Errorf("history %+v cur %d", es, cur)
	}
	last := cur
	if _, err := s.JumpTo(last - 2); err != nil {
		t.Fatal(err)
	}
	if _, c2 := s.History(); c2 != last-2 {
		t.Errorf("after jump back cur %d", c2)
	}
	p, _ := s.Snapshot().Preset("com.google.Chrome")
	if _, ok := p.Banks["base"]["C3"]; ok {
		t.Error("jump back left C3")
	}
	if es2, _ := s.History(); len(es2) != len(es) || es2[len(es2)-1].Done {
		t.Errorf("redo tail lost or marked done: %+v", es2)
	}
	if _, err := s.JumpTo(last); err != nil {
		t.Fatal(err)
	}
	p, _ = s.Snapshot().Preset("com.google.Chrome")
	if _, ok := p.Banks["base"]["C3"]; !ok {
		t.Error("jump forward lost C3")
	}
	if _, err := s.JumpTo(0); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Snapshot().Preset("com.google.Chrome"); ok {
		t.Error("jump to 0 kept the app")
	}
	for _, bad := range []int{-1, last + 1} {
		if _, err := s.JumpTo(bad); err == nil {
			t.Errorf("jump %d accepted", bad)
		}
	}
	if _, ok, _ := s.Redo(); !ok {
		t.Error("redo after jump to 0")
	}
}

func TestWarnings(t *testing.T) {
	s, dir, _ := chromeAndSafari(t)
	// hand-write a messy preset on disk and let the poll-free check load it
	messy := `{"schema":1,"id":"com.messy.App","name":"Messy","packs":["generic"],"banks":{
	 "base":{"C1":{"type":"warp"},"C2":{"action":"nonexistent.thing"},"C3":{"type":"key"},"C4":{"type":"key","key":"Cmb+Z"},
	         "C5":{"type":"key","key":"Cmd+Z","bogus":1},"C6":{"type":"key","key":"cmd+z"},"C7":{"action":"other","type":"mystery"},
	         "D1":{"type":"scrollDelta","linesPerDetent":9999},"D2":{"type":"jogSeconds"},"Nope":{"type":"none"}},
	 "weird":{"C1":{"type":"none"}}}}`
	if err := os.WriteFile(filepath.Join(dir, "com.messy.App.json"), []byte(messy), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "com.broken.json"), []byte(`{"id":"com.broken","banks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CheckExternal(); err != nil {
		t.Fatal(err)
	}
	// the messy file carries a type nobody can judge: held back until approved (wave O)
	for _, pe := range s.PendingExternal() {
		if err := s.ApproveExternal(pe.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	ws := s.Warnings()
	have := map[string]bool{}
	for _, w := range ws {
		have[w.Kind+"|"+w.App+"|"+w.Control] = true
		if w.Severity != "warn" && w.Severity != "hint" && w.Severity != "error" || w.Message == "" {
			t.Errorf("bad warning %+v", w)
		}
	}
	for _, want := range []string{
		"unknown-type|com.messy.App|C1", "unknown-action|com.messy.App|C2", "missing-required|com.messy.App|C3",
		"invalid-key|com.messy.App|C4", "orphaned-value|com.messy.App|C5", "duplicate-key|com.messy.App|C5",
		"unknown-type|com.messy.App|C7", "invalid-value|com.messy.App|D1", "unknown-bank|com.messy.App|",
		"pack-missing|com.messy.App|D2", "unreadable-file||",
	} {
		if !have[want] {
			t.Errorf("missing warning %s\n%v", want, ws)
		}
	}
	// a clean store has none
	s2, _, _ := clockStore(t, 0)
	if w := s2.Warnings(); len(w) != 0 {
		t.Errorf("clean store: %+v", w)
	}
	// the messy file is untouched and still loaded
	if _, ok := s.Snapshot().Preset("com.messy.App"); !ok {
		t.Error("messy preset dropped")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "com.broken.json")); !strings.Contains(string(b), "banks") {
		t.Error("broken file touched")
	}
}

func TestSeededStoreWarningsAreOnlyHints(t *testing.T) {
	s, _ := seededStore(t)
	for _, w := range s.Warnings() {
		if w.Severity == "warn" {
			t.Errorf("seed data raises a warning: %+v", w)
		}
	}
}

// Exports made before the rename to 2T5 DECK+ carry the old kind; they must still import.
func TestImportAcceptsLegacyBundleKind(t *testing.T) {
	s, _, _ := clockStore(t, 0)
	legacy := `{"kind":"` + LegacyBundleKind + `","schema":1,"created":"x","app":"all","files":[]}`
	if LegacyBundleKind != "ld-deck-presets" || BundleKind == LegacyBundleKind {
		t.Fatalf("kinds: %q / %q", LegacyBundleKind, BundleKind)
	}
	_, err := s.Import(mkZip(t, map[string]string{"manifest.json": legacy, "com.google.Chrome.json": presetChrome}), false)
	if err != nil && strings.Contains(err.Error(), "not a 2T5 DECK+") {
		t.Fatalf("legacy kind rejected: %v", err)
	}
	if err != nil {
		t.Fatalf("legacy bundle did not import: %v", err)
	}
	// and what we write now carries the new kind
	data, _, err := s.Export("all")
	if err != nil || !strings.Contains(string(zipEntry(t, data, "manifest.json")), `"kind": "`+BundleKind+`"`) {
		t.Errorf("export kind: %v", err)
	}
}

func zipEntry(t *testing.T, data []byte, name string) []byte {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			b, _ := io.ReadAll(rc)
			return b
		}
	}
	t.Fatalf("no %s in the zip", name)
	return nil
}
