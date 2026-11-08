package preset

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"2t5-deck-plus/internal/actions"
)

const chrome = "com.google.Chrome"

func mustAdd(t *testing.T, s *Store, id string) {
	t.Helper()
	if _, err := s.AddApp(id, "", nil); err != nil {
		t.Fatal(err)
	}
}

func TestAddAppDefaultsAndDuplicates(t *testing.T) {
	s, dir := emptyStore(t)
	p, err := s.AddApp(chrome, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "Google Chrome" || len(p.Packs) != 2 || p.Packs[0] != "browser" {
		t.Errorf("chrome = %+v", p)
	}
	if _, err := s.AddApp(chrome, "", nil); !errors.Is(err, ErrExists) {
		t.Errorf("dup err = %v", err)
	}
	if _, err := s.AddApp("../evil", "", nil); err == nil {
		t.Error("path id accepted")
	}
	if _, err := s.AddApp(GlobalID, "", nil); err == nil {
		t.Error("global re-add accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, chrome+".json")); err != nil {
		t.Error(err)
	}
}

func TestAssignValidationErrorsNameTheField(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	cases := []struct {
		name  string
		app   string
		bank  string
		ctl   string
		asg   string
		field string
	}{
		{"bad key spec", chrome, "base", "D1", `{"type":"key","key":"Cmb+Z"}`, "key"},
		{"over max", chrome, "base", "D1", `{"action":"browser.scroll","linesPerDetent":999}`, "linesPerDetent"},
		{"unknown type", chrome, "base", "D1", `{"type":"warp"}`, "type"},
		{"bad bank", chrome, "col", "D1", `{"type":"none"}`, "bank"},
		{"bad control", chrome, "base", "../x", `{"type":"none"}`, "control"},
		{"shell without cmd", chrome, "base", "C1", `{"type":"shell"}`, "cmd"},
	}
	for _, c := range cases {
		err := s.Assign(c.app, c.bank, c.ctl, raw(t, c.asg))
		var fe *actions.FieldError
		if !errors.As(err, &fe) || fe.Field != c.field {
			t.Errorf("%s: want field %q, got %v", c.name, c.field, err)
		}
	}
	if err := s.Assign("com.nope", "base", "D1", raw(t, `{"type":"none"}`)); !errors.Is(err, ErrNoPreset) {
		t.Errorf("unknown preset: %v", err)
	}
	// nothing leaked in
	snap := s.Snapshot()
	p, _ := snap.Preset(chrome)
	if p.Assigned() != 0 {
		t.Errorf("failed assigns left %d assignments", p.Assigned())
	}
}

func TestAssignNormalizesAliasAndRoundTripsOther(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	if err := s.Assign(chrome, "base", "D1", raw(t, `{"action":"browser.scroll"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Assign(chrome, "base", "D2", raw(t, `{"action":"other","type":"teleport","dest":"moon","n":1.50}`)); err != nil {
		t.Fatal(err)
	}
	e, ok := s.Snapshot().Effective(chrome, "base", "D1")
	if !ok || e.Raw["type"] != "scrollDelta" || e.Action != "browser.scroll" {
		t.Fatalf("D1 = %+v", e)
	}
	e2, _ := s.Snapshot().Effective(chrome, "base", "D2")
	if e2.Action != "other" {
		t.Errorf("D2 action = %s", e2.Action)
	}
	b, _ := os.ReadFile(filepath.Join(dir, chrome+".json"))
	if !strings.Contains(string(b), `"dest": "moon"`) || !strings.Contains(string(b), `1.50`) {
		t.Errorf("raw not preserved:\n%s", b)
	}
}

func TestPerControlAndPerValueInheritance(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	// Global: a scroll on D1 with a sensitivity, and a different control C1.
	if err := s.Assign(GlobalID, "base", "D1", raw(t, `{"type":"scrollDelta","linesPerDetent":2,"detentThreshold":6,"label":"G scroll"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Assign(GlobalID, "base", "C1", raw(t, `{"type":"key","key":"Cmd+Z"}`)); err != nil {
		t.Fatal(err)
	}
	snap := s.Snapshot()

	// (2) app lacks the control: Global's assignment, provenance global
	e, ok := snap.Effective(chrome, "base", "C1")
	if !ok || e.Source != "global" || e.PresetID != GlobalID || e.Fields["key"].Source != "global" {
		t.Fatalf("C1 fallback = %+v", e)
	}
	// (3) neither
	if _, ok := snap.Effective(chrome, "base", "C2"); ok {
		t.Error("C2 should be unassigned")
	}

	// (1) app has the control but sets only linesPerDetent: other fields from Global
	if err := s.Assign(chrome, "base", "D1", raw(t, `{"type":"scrollDelta","linesPerDetent":5}`)); err != nil {
		t.Fatal(err)
	}
	e, _ = s.Snapshot().Effective(chrome, "base", "D1")
	if e.Source != "app" {
		t.Fatalf("source %s", e.Source)
	}
	want := map[string]FieldValue{
		"linesPerDetent":  {Value: json5("5"), Source: "app"},
		"detentThreshold": {Value: json5("6"), Source: "global"},
		"flags":           {Value: nil, Source: "default"},
	}
	for k, w := range want {
		g := e.Fields[k]
		if g.Source != w.Source || (w.Value != nil && g.Value != w.Value) {
			t.Errorf("field %s = %+v want %+v", k, g, w)
		}
	}
	if e.Raw["detentThreshold"] == nil {
		t.Error("global value not merged into Raw")
	}
	// provenance of Global's label is not inherited (label is a schema field, so it is)
	if e.Fields["label"].Source != "global" {
		t.Errorf("label source = %s", e.Fields["label"].Source)
	}

	// clearing a value goes back to inheriting
	if err := s.SetValue(chrome, "base", "D1", "linesPerDetent", raw(t, `{"v":9}`)["v"]); err != nil {
		t.Fatal(err)
	}
	if err := s.SetValue(chrome, "base", "D1", "linesPerDetent", nil); err != nil {
		t.Fatal(err)
	}
	e, _ = s.Snapshot().Effective(chrome, "base", "D1")
	if e.Fields["linesPerDetent"].Source != "global" {
		t.Errorf("after clear: %+v", e.Fields["linesPerDetent"])
	}
	// "set" means key present: an explicit 0 is a set value
	if err := s.SetValue(chrome, "base", "D1", "detentThreshold", raw(t, `{"v":1}`)["v"]); err != nil {
		t.Fatal(err)
	}
	e, _ = s.Snapshot().Effective(chrome, "base", "D1")
	if e.Fields["detentThreshold"].Source != "app" {
		t.Errorf("explicit value not app: %+v", e.Fields["detentThreshold"])
	}
}

// json5 builds a json.Number the way the decoder does.
func json5(s string) any { return raw2(s) }

func raw2(s string) any {
	m := map[string]any{}
	_ = m
	return jsonNumber(s)
}

func TestActionDefaultsFromGlobalSettings(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	if err := s.SetSetting(GlobalID, "actionDefaults", raw(t, `{"scrollDelta":{"detentThreshold":9}}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.Assign(chrome, "base", "D1", raw(t, `{"type":"scrollDelta","linesPerDetent":4}`)); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Snapshot().Effective(chrome, "base", "D1")
	if fv := e.Fields["detentThreshold"]; fv.Source != "global" || fv.Value != jsonNumber("9") {
		t.Errorf("actionDefaults not applied: %+v", fv)
	}
}

func TestSetValueCopiesGlobalControlAndValidates(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	if err := s.Assign(GlobalID, "base", "D1", raw(t, `{"type":"scrollDelta","linesPerDetent":2}`)); err != nil {
		t.Fatal(err)
	}
	// app has no D1; setting a value there creates an app assignment of the same action
	if err := s.SetValue(chrome, "base", "D1", "linesPerDetent", jsonNumber("7")); err != nil {
		t.Fatal(err)
	}
	e, _ := s.Snapshot().Effective(chrome, "base", "D1")
	if e.Source != "app" || e.Raw["type"] != "scrollDelta" {
		t.Fatalf("%+v", e)
	}
	var fe *actions.FieldError
	if err := s.SetValue(chrome, "base", "D1", "linesPerDetent", jsonNumber("1000")); !errors.As(err, &fe) || fe.Field != "linesPerDetent" {
		t.Errorf("range: %v", err)
	}
	if err := s.SetValue(chrome, "base", "D1", "nonsense", jsonNumber("1")); !errors.As(err, &fe) || fe.Field != "nonsense" {
		t.Errorf("unknown field: %v", err)
	}
	if err := s.SetValue(chrome, "base", "D1", "type", "key"); !errors.As(err, &fe) {
		t.Errorf("type change: %v", err)
	}
	if err := s.SetValue(chrome, "base", "C9", "label", "x"); !errors.Is(err, ErrNoControl) {
		t.Errorf("unassigned: %v", err)
	}
	if err := s.SetValue(GlobalID, "base", "C9", "label", "x"); !errors.Is(err, ErrNoControl) {
		t.Errorf("global unassigned: %v", err)
	}
}

func TestUnassignFallsBackToGlobal(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	_ = s.Assign(GlobalID, "base", "C1", raw(t, `{"type":"key","key":"Cmd+Z"}`))
	_ = s.Assign(chrome, "base", "C1", raw(t, `{"type":"key","key":"Cmd+R"}`))
	if e, _ := s.Snapshot().Effective(chrome, "base", "C1"); e.Source != "app" {
		t.Fatal("app should win")
	}
	if err := s.Unassign(chrome, "base", "C1"); err != nil {
		t.Fatal(err)
	}
	if e, ok := s.Snapshot().Effective(chrome, "base", "C1"); !ok || e.Source != "global" {
		t.Errorf("after unassign: %+v %v", e, ok)
	}
	if err := s.Unassign(chrome, "base", "C1"); err != nil { // idempotent
		t.Error(err)
	}
}

func TestUndoRedoAndLimit(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	_ = s.Assign(chrome, "base", "C1", raw(t, `{"type":"key","key":"Cmd+R"}`))
	_ = s.SetValue(chrome, "base", "C1", "key", "Cmd+T")
	if lab, ok, err := s.Undo(); err != nil || !ok || lab != "Set C1 key" {
		t.Fatalf("undo = %q %v %v", lab, ok, err)
	}
	if e, _ := s.Snapshot().Effective(chrome, "base", "C1"); e.Raw["key"] != "Cmd+R" {
		t.Errorf("undo did not revert: %v", e.Raw)
	}
	b, _ := os.ReadFile(filepath.Join(dir, chrome+".json"))
	if !strings.Contains(string(b), "Cmd+R") {
		t.Error("undo not written to disk")
	}
	if lab, ok, _ := s.Redo(); !ok || lab != "Set C1 key" {
		t.Errorf("redo = %q %v", lab, ok)
	}
	if e, _ := s.Snapshot().Effective(chrome, "base", "C1"); e.Raw["key"] != "Cmd+T" {
		t.Error("redo did not re-apply")
	}
	// a new edit clears redo
	_, _, _ = s.Undo()
	_ = s.SetValue(chrome, "base", "C1", "label", "x")
	if _, ok, _ := s.Redo(); ok {
		t.Error("redo survived a new edit")
	}
	// undo past AddApp removes the file again; redo restores it
	for {
		_, ok, err := s.Undo()
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
	}
	if _, err := os.Stat(filepath.Join(dir, chrome+".json")); !os.IsNotExist(err) {
		t.Error("undoing AddApp left the file")
	}
	if _, ok, _ := s.Redo(); !ok {
		t.Fatal("cannot redo AddApp")
	}
	if _, err := os.Stat(filepath.Join(dir, chrome+".json")); err != nil {
		t.Error("redo AddApp did not restore the file")
	}
}

func TestUndoStackCappedAt200(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	_ = s.Assign(chrome, "base", "C1", raw(t, `{"type":"key","key":"Cmd+R"}`))
	for i := 0; i < 250; i++ {
		if err := s.SetValue(chrome, "base", "C1", "label", "v"+string(rune('A'+i%26))+strings.Repeat("x", i)); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for {
		_, ok, _ := s.Undo()
		if !ok {
			break
		}
		n++
	}
	if n != 200 {
		t.Errorf("undo depth %d, want 200", n)
	}
}

func TestNoOpEditPushesNoUndo(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	_ = s.Assign(chrome, "base", "C1", raw(t, `{"type":"key","key":"Cmd+R"}`))
	_ = s.Assign(chrome, "base", "C1", raw(t, `{"type":"key","key":"Cmd+R"}`)) // identical
	_, _, _ = s.Undo()                                                         // Assign
	lab, _, _ := s.Undo()                                                      // must be AddApp, not a duplicate Assign
	if lab != "Add app Google Chrome" {
		t.Errorf("second undo = %q", lab)
	}
}

func TestHistoryKeepsLast30(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	_ = s.Assign(chrome, "base", "C1", raw(t, `{"type":"key","key":"Cmd+R"}`))
	for i := 0; i < 45; i++ {
		_ = s.SetValue(chrome, "base", "C1", "label", strings.Repeat("l", i+1))
	}
	es, err := os.ReadDir(filepath.Join(dir, ".history", chrome))
	if err != nil {
		t.Fatal(err)
	}
	if len(es) != 30 {
		t.Errorf("history files = %d, want 30", len(es))
	}
}

func TestOnChangeAndUnsubscribe(t *testing.T) {
	s, _ := emptyStore(t)
	var n int32
	var lastSeq atomic.Int64
	off := s.OnChange(func(sn *Snapshot) { atomic.AddInt32(&n, 1); lastSeq.Store(int64(sn.Seq)) })
	mustAdd(t, s, chrome)
	_ = s.Assign(chrome, "base", "C1", raw(t, `{"type":"none"}`))
	_, _, _ = s.Undo()
	if n != 3 || lastSeq.Load() != int64(s.Snapshot().Seq) {
		t.Errorf("notified %d times, seq %d vs %d", n, lastSeq.Load(), s.Snapshot().Seq)
	}
	off()
	_ = s.Assign(chrome, "base", "C2", raw(t, `{"type":"none"}`))
	if n != 3 {
		t.Error("unsubscribed callback fired")
	}
}

func TestExternalEditPickedUpOwnWritesIgnored(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	var fired int32
	s.OnChange(func(*Snapshot) { atomic.AddInt32(&fired, 1) })
	_ = s.Assign(chrome, "base", "C1", raw(t, `{"type":"key","key":"Cmd+R"}`))
	before := atomic.LoadInt32(&fired)
	if changed, err := s.CheckExternal(); err != nil || changed {
		t.Fatalf("own write treated as external: %v %v", changed, err)
	}
	if atomic.LoadInt32(&fired) != before {
		t.Error("own write fired OnChange again")
	}
	// someone edits the file
	path := filepath.Join(dir, chrome+".json")
	b, _ := os.ReadFile(path)
	edited := strings.Replace(string(b), "Cmd+R", "Cmd+Shift+R", 1)
	if err := os.WriteFile(path, []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(5 * time.Second)
	_ = os.Chtimes(path, future, future)
	changed, err := s.CheckExternal()
	if err != nil || !changed {
		t.Fatalf("external edit missed: %v %v", changed, err)
	}
	if e, _ := s.Snapshot().Effective(chrome, "base", "C1"); e.Raw["key"] != "Cmd+Shift+R" {
		t.Errorf("not reloaded: %v", e.Raw)
	}
	if atomic.LoadInt32(&fired) != before+1 {
		t.Error("OnChange not fired for external edit")
	}
	// a new preset file dropped in appears; a broken file is reported and ignored
	os.WriteFile(filepath.Join(dir, "com.new.App.json"), []byte(`{"schema":1,"id":"com.new.App","name":"New","packs":["generic"],"banks":{},"settings":{}}`), 0o644)
	os.WriteFile(filepath.Join(dir, "com.bad.App.json"), []byte(`{nope`), 0o644)
	if changed, _ := s.CheckExternal(); !changed {
		t.Error("new file missed")
	}
	if _, ok := s.Snapshot().Preset("com.new.App"); !ok {
		t.Error("new preset not loaded")
	}
	if _, ok := s.Snapshot().Preset("com.bad.App"); ok {
		t.Error("broken preset loaded")
	}
	if len(s.Problems()) == 0 {
		t.Error("broken file not reported")
	}
	// external edit can be undone
	if _, ok, _ := s.Undo(); !ok {
		t.Error("external edit not undoable")
	}
}

func TestPollGoroutineDetects(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s, err := Open(dir, Options{LiveResolvePath: "-", PollInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got := make(chan struct{}, 1)
	s.OnChange(func(*Snapshot) {
		select {
		case got <- struct{}{}:
		default:
		}
	})
	os.WriteFile(filepath.Join(dir, "com.new.App.json"), []byte(`{"schema":1,"id":"com.new.App","name":"New","packs":["generic"],"banks":{},"settings":{}}`), 0o644)
	select {
	case <-got:
	case <-time.After(3 * time.Second):
		t.Fatal("poll did not notice the new file")
	}
}

func TestFilesAreAtomicAndClean(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	_ = s.Assign(chrome, "base", "C1", raw(t, `{"type":"none"}`))
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

func TestSetPacks(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, "com.some.App")
	if err := s.SetPacks("com.some.App", []string{"browser", "generic"}); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Snapshot().Preset("com.some.App")
	if len(p.Packs) != 2 {
		t.Errorf("packs %v", p.Packs)
	}
	var fe *actions.FieldError
	if err := s.SetPacks("com.some.App", []string{"nope"}); !errors.As(err, &fe) {
		t.Errorf("unknown pack: %v", err)
	}
}

func TestControlsRestriction(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s, err := Open(dir, Options{LiveResolvePath: "-", PollInterval: -1, Controls: []string{"D1", "C1"}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Assign(GlobalID, "base", "Nope", raw(t, `{"type":"none"}`)); err == nil {
		t.Error("control outside the deck accepted")
	}
	if err := s.Assign(GlobalID, "base", "D1", raw(t, `{"type":"none"}`)); err != nil {
		t.Error(err)
	}
}
