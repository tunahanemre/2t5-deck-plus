package preset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Wave E (technical-critic pass 1): ids, case-insensitive duplicates, compose dry-run.

func pjson(id, banks string) []byte {
	if banks == "" {
		banks = "{}"
	}
	return []byte(fmt.Sprintf(`{"schema":1,"id":%q,"name":%q,"packs":["generic"],"banks":%s,"settings":{}}`, id, id, banks))
}

func bundleOf(t *testing.T, files map[string][]byte) []byte {
	t.Helper()
	b, err := zipBundle(files, "all", "export", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var badAppIDs = []string{"col", "edit", "default", "global", "_global2", "Col", "DEFAULT", "x", "com", "com.", ".com.x", "com..x", "com.x y"}

func TestWaveE_CheckAppIDRejectsReserved(t *testing.T) {
	for _, id := range badAppIDs {
		if CheckAppID(id) == nil {
			t.Errorf("CheckAppID(%q) accepted", id)
		}
	}
	for _, id := range []string{chrome, "com.apple.Safari", "com.example.Plain", "org.mozilla.firefox"} {
		if err := CheckAppID(id); err != nil {
			t.Errorf("CheckAppID(%q) = %v", id, err)
		}
	}
	// Global and Resolve are stored ids, not app ids.
	if CheckStoredID(GlobalID) != nil || CheckStoredID(ResolveID) != nil {
		t.Error("stored ids Global / Resolve refused")
	}
	if CheckAppID(GlobalID) == nil {
		t.Error("Global accepted as an add-able app id")
	}
	if CheckAppID(strings.ToUpper(ResolveID)) == nil {
		t.Error("a case variant of the Resolve id accepted")
	}
}

func TestWaveE_ParseBundleRejectsBadIDs(t *testing.T) {
	s, _ := emptyStore(t)
	for _, id := range []string{"col", "edit", "default", "global"} {
		_, err := s.ParseBundle(bundleOf(t, map[string][]byte{id + ".json": pjson(id, "")}))
		var ie *ImportError
		if !errors.As(err, &ie) {
			t.Errorf("bundle with id %q: err = %v, want ImportError", id, err)
		}
	}
	// A normal export (Global + Resolve + app) still parses.
	if _, err := s.ParseBundle(bundleOf(t, map[string][]byte{GlobalID + ".json": pjson(GlobalID, ""), chrome + ".json": pjson(chrome, "")})); err != nil {
		t.Errorf("good bundle refused: %v", err)
	}
}

func TestWaveE_ImportOfReservedIDChangesNothing(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	seq := s.Snapshot().Seq
	_, err := s.Import(bundleOf(t, map[string][]byte{"col.json": pjson("col", `{"base":{"D1":{"type":"none"}}}`)}), false)
	if err == nil {
		t.Fatal("import of preset id col accepted")
	}
	if s.Snapshot().Seq != seq {
		t.Error("snapshot changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "col.json")); err == nil {
		t.Error("col.json written")
	}
	if _, err := s.Snapshot().ComposeBindingsJSON(); err != nil {
		t.Errorf("store no longer composes: %v", err)
	}
}

func TestWaveE_AddAppRejectsReservedAndNonBundleIDs(t *testing.T) {
	s, dir := emptyStore(t)
	for _, id := range []string{"col", "edit", "default", "global", "foo"} {
		if _, err := s.AddApp(id, "", nil); err == nil {
			t.Errorf("AddApp(%q) accepted", id)
		}
		if _, err := os.Stat(filepath.Join(dir, id+".json")); err == nil {
			t.Errorf("%s.json written", id)
		}
	}
}

func TestWaveE_ExternalBadIDFileIsAProblemNotLoaded(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	for _, id := range []string{"col", "edit", "default"} {
		os.WriteFile(filepath.Join(dir, id+".json"), pjson(id, `{"base":{"D1":{"type":"none"}}}`), 0o644)
	}
	changed, err := s.CheckExternal()
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("a bad-id external file changed the snapshot")
	}
	for _, id := range []string{"col", "edit", "default"} {
		if _, ok := s.Snapshot().Preset(id); ok {
			t.Errorf("%s loaded", id)
		}
		if _, bad := s.Problems()[id+".json"]; !bad {
			t.Errorf("%s.json not listed as a problem: %v", id, s.Problems())
		}
	}
	if _, err := s.Snapshot().ComposeBindingsJSON(); err != nil {
		t.Errorf("store no longer composes: %v", err)
	}
	// later edits still work and reach the snapshot
	mustAssign(t, s, chrome, "base", "C1", map[string]any{"type": "key", "key": "Cmd+T"})
}

func TestWaveE_OpenSkipsBadIDFile(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	s.Close()
	os.WriteFile(filepath.Join(dir, "default.json"), pjson("default", `{"base":{"D1":{"type":"none"}}}`), 0o644)
	s2 := reopen(t, dir, Options{})
	if _, ok := s2.Snapshot().Preset("default"); ok {
		t.Error("default loaded at Open")
	}
	if _, bad := s2.Problems()["default.json"]; !bad {
		t.Error("not a problem")
	}
	if _, err := s2.Snapshot().ComposeBindingsJSON(); err != nil {
		t.Error(err)
	}
}

func TestWaveE_CaseInsensitiveDuplicateAddApp(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, "com.apple.Safari")
	_, err := s.AddApp("com.apple.safari", "", nil)
	if !errors.Is(err, ErrExists) {
		t.Fatalf("lower-case duplicate err = %v", err)
	}
	if n := len(s.Snapshot().Presets()); n != 2 { // Global + Safari
		t.Errorf("presets = %d", n)
	}
	es, _ := os.ReadDir(dir)
	c := 0
	for _, e := range es {
		if strings.HasSuffix(strings.ToLower(e.Name()), "safari.json") {
			c++
		}
	}
	if c != 1 {
		t.Errorf("safari files = %d", c)
	}
}

func TestWaveE_CaseInsensitiveDuplicateImport(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, "com.apple.Safari")
	_, err := s.Import(bundleOf(t, map[string][]byte{"com.apple.safari.json": pjson("com.apple.safari", "")}), false)
	if err == nil {
		t.Error("import differing only in case accepted")
	}
	_, err = s.ParseBundle(bundleOf(t, map[string][]byte{
		"com.a.B.json": pjson("com.a.B", ""), "com.a.b.json": pjson("com.a.b", "")}))
	if err == nil {
		t.Error("bundle with two ids differing only in case accepted")
	}
}

func TestWaveE_ExternalCaseVariantIsAProblem(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, "com.apple.Safari")
	os.WriteFile(filepath.Join(dir, "com.apple.safari.json"), pjson("com.apple.safari", ""), 0o644)
	// on a case-insensitive volume that overwrote Safari's file (id mismatch -> problem);
	// on a case-sensitive one it is a second file. Either way no second preset may load.
	s.CheckExternal()
	n := 0
	for _, p := range s.Snapshot().Presets() {
		if strings.EqualFold(p.ID, "com.apple.safari") {
			n++
		}
	}
	if n > 1 {
		t.Errorf("%d presets differing only in case", n)
	}
}

// com.a with a shift bank produces runtime bank "com.a-shift"; a separate app
// "com.a-shift" with a base bank produces the same name.
func TestWaveE_CommitRejectsWhatWouldNotCompose(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, "com.a.b")
	// AddApp now refuses this pair up front (wave G, B10); put the second app in
	// through the commit path so the dry-run guard is still what is under test.
	if err := s.commit("add colliding app", func(ps map[string]*Preset) error {
		ps["com.a.b-shift"] = newGlobal()
		ps["com.a.b-shift"].ID, ps["com.a.b-shift"].Name = "com.a.b-shift", "com.a.b-shift"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	mustAssign(t, s, "com.a.b", "shift", "C1", map[string]any{"type": "key", "key": "Cmd+T"})
	before := s.Snapshot()
	err := s.Assign("com.a.b-shift", "base", "C1", map[string]any{"type": "key", "key": "Cmd+Y"})
	if err == nil {
		t.Fatal("a change that cannot compose was accepted")
	}
	if !strings.Contains(err.Error(), "com.a.b-shift") {
		t.Errorf("error does not name the cause: %v", err)
	}
	if s.Snapshot() != before {
		t.Error("snapshot changed")
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "com.a.b-shift.json")); strings.Contains(string(b), "Cmd+Y") {
		t.Error("rejected change reached the disk")
	}
	if _, err := s.Snapshot().ComposeBindingsJSON(); err != nil {
		t.Errorf("store no longer composes: %v", err)
	}
	// and a harmless edit afterwards works
	mustAssign(t, s, "com.a.b", "base", "C2", map[string]any{"type": "key", "key": "Cmd+U"})
}

func TestWaveE_ExternalUncomposableFileIsAProblem(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, "com.a.b")
	mustAssign(t, s, "com.a.b", "shift", "C1", map[string]any{"type": "key", "key": "Cmd+T"})
	os.WriteFile(filepath.Join(dir, "com.a.b-shift.json"), pjson("com.a.b-shift", `{"base":{"C1":{"type":"key","key":"Cmd+Y"}}}`), 0o644)
	changed, err := s.CheckExternal()
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		t.Error("an uncomposable external file was loaded")
	}
	if msg := s.Problems()["com.a.b-shift.json"]; msg == "" {
		t.Errorf("not reported as a problem: %v", s.Problems())
	}
	if _, err := s.Snapshot().ComposeBindingsJSON(); err != nil {
		t.Error(err)
	}
}

func TestWaveE_SeedAndTemplatesUseTheValidator(t *testing.T) {
	if err := TemplatesError(); err != nil {
		t.Fatal(err)
	}
	for _, tp := range Templates() {
		if err := CheckAppID(tp.BundleID); err != nil {
			t.Errorf("template %s: %v", tp.ID, err)
		}
	}
	s, _ := seededStore(t) // real seed data still seeds every app
	if _, ok := s.Snapshot().Preset(FinderID); !ok {
		t.Error("Finder preset not seeded")
	}
	if len(s.Problems()) != 0 {
		t.Errorf("problems after seeding: %v", s.Problems())
	}
}

func TestWaveE_ResolveMatchesBundleIDsCaseInsensitively(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, "com.apple.Safari")
	mustAssign(t, s, "com.apple.Safari", "base", "C1", map[string]any{"type": "key", "key": "Cmd+T"})
	snap := s.Snapshot()
	for _, b := range []string{"com.apple.Safari", "com.apple.safari", "COM.APPLE.SAFARI"} {
		eb, ok := snap.ResolveFor(b, 1, "C1")
		if !ok || eb.PresetID != "com.apple.Safari" || eb.Source != "app" {
			t.Errorf("ResolveFor(%q) = %+v %v", b, eb, ok)
		}
		if eb, ok := snap.ResolveForMod(b, "shift", 1, "C1"); ok && eb.PresetID != "com.apple.Safari" {
			t.Errorf("ResolveForMod(%q) = %+v", b, eb)
		}
		if e, ok := snap.Effective(b, "base", "C1"); !ok || e.PresetID != "com.apple.Safari" {
			t.Errorf("Effective(%q) = %+v %v", b, e, ok)
		}
	}
}

// ---- F4: import risk ------------------------------------------------------------------

func shellBundle(t *testing.T, id string) []byte {
	t.Helper()
	long := strings.Repeat("x", 400)
	return bundleOf(t, map[string][]byte{id + ".json": pjson(id,
		`{"base":{"C1":{"type":"shell","cmd":"/usr/bin/open","args":["-a","Calculator"]},`+
			`"C2":{"type":"shell","cmd":"/bin/echo","args":["`+long+`"]},`+
			`"C3":{"type":"key","key":"Cmd+Q"},"C4":{"type":"key","key":"Cmd+T"}}}`)})
}

func TestWaveE_ImportReportCountsRiskAndShowsCommands(t *testing.T) {
	s, _ := emptyStore(t)
	rep, err := s.Import(shellBundle(t, "com.evil.App"), true)
	if err != nil {
		t.Fatal(err)
	}
	f := rep.Files[0]
	if f.Risk.Shell != 2 || f.Risk.Destructive != 1 || f.Risk.Keys != 1 {
		t.Errorf("risk = %+v", f.Risk)
	}
	if len(f.RunsProgram) != 2 || !strings.Contains(f.RunsProgram[0], "/usr/bin/open -a Calculator") {
		t.Errorf("runsProgram = %q", f.RunsProgram)
	}
	for _, l := range f.RunsProgram {
		if len(l) > 220 {
			t.Errorf("command line not truncated (%d chars)", len(l))
		}
	}
	if !rep.NeedsConfirm {
		t.Error("report does not say it needs confirmation")
	}
}

func TestWaveE_RealImportOfShellNeedsConfirm(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	seq := s.Snapshot().Seq
	_, err := s.Import(shellBundle(t, "com.evil.App"), false)
	var re *RiskError
	if !errors.As(err, &re) || !errors.Is(err, ErrNeedsConfirm) {
		t.Fatalf("err = %v, want *RiskError", err)
	}
	if len(re.Report.Files) != 1 || re.Report.Files[0].Risk.Shell != 2 {
		t.Errorf("report in error = %+v", re.Report)
	}
	if s.Snapshot().Seq != seq {
		t.Error("store changed")
	}
	if _, err := os.Stat(filepath.Join(dir, "com.evil.App.json")); err == nil {
		t.Error("file written")
	}
	if len(s.Backups()) != 0 && s.Backups()[0].Reason == "import" {
		t.Error("an import backup was made for a refused import")
	}
	if _, err := s.ImportWith(shellBundle(t, "com.evil.App"), false, true); err != nil {
		t.Fatalf("confirmed import: %v", err)
	}
	if _, ok := s.Snapshot().Preset("com.evil.App"); !ok {
		t.Error("confirmed import did not add the preset")
	}
}

func TestWaveE_ImportOfPlainKeysAndOwnShellNeedsNoConfirm(t *testing.T) {
	s, _ := emptyStore(t)
	if _, err := s.Import(bundleOf(t, map[string][]byte{chrome + ".json": pjson(chrome, `{"base":{"C1":{"type":"key","key":"Cmd+T"}}}`)}), false); err != nil {
		t.Fatalf("plain key import: %v", err)
	}
	// the user's own shell action, exported and re-imported unchanged, is not new risk
	mustAdd(t, s, "com.example.Mine")
	mustAssign(t, s, "com.example.Mine", "base", "C1", map[string]any{"type": "shell", "cmd": "/usr/bin/say", "args": []any{"hi"}})
	data, _, err := s.Export("com.example.Mine")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := s.Import(data, false)
	if err != nil {
		t.Fatalf("re-import of an unchanged shell action: %v", err)
	}
	if rep.NeedsConfirm || rep.Files[0].Risk.Shell != 0 {
		t.Errorf("unchanged shell action counted as risk: %+v", rep)
	}
	// but a CHANGED command is new risk
	mustAssign(t, s, "com.example.Mine", "base", "C1", map[string]any{"type": "shell", "cmd": "/usr/bin/say", "args": []any{"bye"}})
	if _, err := s.Import(data, false); !errors.Is(err, ErrNeedsConfirm) {
		t.Errorf("changed shell command re-imported without confirm: %v", err)
	}
}

// ---- F10: backups skip unreadable files, restore is partial ---------------------------

func TestWaveE_BackupSkipsUnreadableFilesAndStaysRestorable(t *testing.T) {
	s, dir, _ := clockStore(t, 0)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C1", map[string]any{"type": "key", "key": "Cmd+T"})
	os.WriteFile(filepath.Join(dir, "com.broken.App.json"), []byte(`{"id":`), 0o644)
	os.WriteFile(filepath.Join(dir, "default.json"), pjson("default", ""), 0o644) // bad id
	b, ok, err := s.BackupNow()
	if err != nil || !ok {
		t.Fatalf("backup: %v %v", ok, err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".backups", b.Name))
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := s.ParseBundle(data)
	if err != nil {
		t.Fatalf("the backup does not parse: %v", err)
	}
	if bundle.Presets[chrome] == nil || len(bundle.Presets) != 2 { // Chrome + Global
		t.Errorf("backup holds %d presets", len(bundle.Presets))
	}
	sk := map[string]bool{}
	for _, f := range bundle.Manifest.Skipped {
		sk[f] = true
	}
	if !sk["com.broken.App.json"] || !sk["default.json"] {
		t.Errorf("manifest.skipped = %v", bundle.Manifest.Skipped)
	}
	mustAssign(t, s, chrome, "base", "C1", map[string]any{"type": "key", "key": "Cmd+Y"})
	if _, err := s.RestoreBackup(b.Name); err != nil {
		t.Fatalf("restore: %v", err)
	}
	p, _ := s.Snapshot().Preset(chrome)
	if k, _ := p.Banks["base"]["C1"]["key"].(string); k != "Cmd+T" {
		t.Errorf("key after restore = %q", k)
	}
}

func TestWaveE_RestoreOfAnOldBackupWithABrokenFileIsPartial(t *testing.T) {
	s, dir, _ := clockStore(t, 0)
	mustAdd(t, s, chrome)
	mustAdd(t, s, "com.keep.Me")
	zipb := bundleOf(t, map[string][]byte{
		chrome + ".json":       pjson(chrome, `{"base":{"C1":{"type":"key","key":"Cmd+R"}}}`),
		"com.keep.Me.json":     []byte(`{"id":"com.keep.Me","banks":`), // broken in the backup
		"default.json":         pjson("default", ""),                   // reserved id
		"com.other.Gone.json":  []byte("garbage"),
		"com.good.New.json":    pjson("com.good.New", `{"base":{"C2":{"type":"key","key":"Cmd+U"}}}`),
		"com.bad.Control.json": pjson("com.bad.Control", `{"weird":{"C1":{"type":"none"}}}`),
	})
	if err := os.MkdirAll(filepath.Join(dir, ".backups"), 0o755); err != nil {
		t.Fatal(err)
	}
	name := "presets-20261101-090000-daily.zip"
	if err := os.WriteFile(filepath.Join(dir, ".backups", name), zipb, 0o644); err != nil {
		t.Fatal(err)
	}
	// strict parse (import) refuses it ...
	if _, err := s.ParseBundle(zipb); err == nil {
		t.Fatal("ParseBundle accepted a bundle with broken files")
	}
	// ... restore takes what it can
	rep, err := s.RestoreBackup(name)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	skipped := map[string]bool{}
	for _, sk := range rep.Skipped {
		skipped[sk.File] = true
		if sk.Reason == "" {
			t.Errorf("%s skipped without a reason", sk.File)
		}
	}
	for _, f := range []string{"com.keep.Me.json", "default.json", "com.other.Gone.json", "com.bad.Control.json"} {
		if !skipped[f] {
			t.Errorf("%s not reported as skipped: %+v", f, rep.Skipped)
		}
	}
	snap := s.Snapshot()
	if p, ok := snap.Preset(chrome); !ok || p.Banks["base"]["C1"]["key"] != "Cmd+R" {
		t.Error("Chrome not restored")
	}
	if _, ok := snap.Preset("com.good.New"); !ok {
		t.Error("the good new preset was not restored")
	}
	if _, ok := snap.Preset("com.keep.Me"); !ok {
		t.Error("a preset whose backup copy was broken was deleted by the restore")
	}
	if _, ok := snap.Preset("default"); ok {
		t.Error("reserved id restored")
	}
	if rep.NeedsConfirm {
		t.Error("restore report asks for risk confirmation")
	}
}
