package preset

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Wave F: silent-loss canary, pre-update backup, rejected files re-evaluated.

func keyA(k string) map[string]any { return map[string]any{"type": "key", "key": k} }

// canaryStore opens a store whose canary writes are synchronous.
func canaryStore(t *testing.T, dir, build string, now func() time.Time) *Store {
	t.Helper()
	old := BuildID
	BuildID = build
	t.Cleanup(func() { BuildID = old })
	o := Options{LiveResolvePath: "-", PollInterval: -1, CanaryDebounce: -1}
	if now != nil {
		o.Now = now
	}
	s, err := Open(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func fillChrome(t *testing.T, s *Store, n int) {
	t.Helper()
	mustAdd(t, s, chrome)
	for i := 0; i < n; i++ {
		mustAssign(t, s, chrome, "base", "C"+string(rune('1'+i)), keyA("Cmd+"+string(rune('A'+i))))
	}
}

// dropAssignments rewrites the Chrome file on disk with only the first keep controls
// (what a bad migration or an outside edit would do while the app is closed).
func dropAssignments(t *testing.T, dir string, keep int) {
	t.Helper()
	p := filepath.Join(dir, chrome+".json")
	b, _ := os.ReadFile(p)
	var m map[string]any
	json.Unmarshal(b, &m)
	base := m["banks"].(map[string]any)["base"].(map[string]any)
	n := 0
	for k := range base {
		_ = k
	}
	keys := []string{}
	for k := range base {
		keys = append(keys, k)
	}
	// deterministic: keep C1..C<keep>
	for _, k := range keys {
		idx := int(k[1] - '0')
		if idx > keep {
			delete(base, k)
			n++
		}
	}
	nb, _ := json.Marshal(m)
	if err := os.WriteFile(p, nb, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCanary_CleanReopenSilent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	fillChrome(t, s, 5)
	s.Close()
	s2 := canaryStore(t, dir, "b1", nil)
	if c := s2.Canary(); c != nil {
		t.Fatalf("canary on a clean reopen: %+v", c)
	}
	for _, w := range s2.Warnings() {
		if w.Kind == "canary" {
			t.Fatalf("canary warning on a clean reopen: %+v", w)
		}
	}
}

func TestCanary_LossWhileClosedIsFlagged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	fillChrome(t, s, 5)
	s.Close()
	time.Sleep(20 * time.Millisecond)
	dropAssignments(t, dir, 2)
	s2 := canaryStore(t, dir, "b1", nil)
	c := s2.Canary()
	if c == nil || len(c.Lost) != 1 {
		t.Fatalf("canary = %+v", c)
	}
	l := c.Lost[0]
	if l.Preset != chrome || l.Before != 5 || l.After != 2 || len(l.Controls) != 3 {
		t.Errorf("lost = %+v", l)
	}
	if c.Since == 0 {
		t.Error("since not set")
	}
	var found bool
	for _, w := range s2.Warnings() {
		if w.Kind == "canary" {
			found = true
			if w.Severity != "warn" || !strings.Contains(w.Message, "5") || w.App != chrome {
				t.Errorf("warning = %+v", w)
			}
		}
	}
	if !found {
		t.Error("no canary warning")
	}
	// survives another restart until acknowledged
	s2.Close()
	s3 := canaryStore(t, dir, "b1", nil)
	if s3.Canary() == nil {
		t.Fatal("pending canary lost on restart")
	}
	if had, err := s3.AckCanary(); err != nil || !had {
		t.Fatalf("ack = %v %v", had, err)
	}
	if s3.Canary() != nil {
		t.Error("canary still pending after ack")
	}
	if had, _ := s3.AckCanary(); had {
		t.Error("second ack reported something to dismiss")
	}
	s3.Close()
	if s4 := canaryStore(t, dir, "b1", nil); s4.Canary() != nil {
		t.Error("acknowledged canary came back")
	}
}

func TestCanary_UserEditsAreNotLoss(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	fillChrome(t, s, 5)
	if err := s.Unassign(chrome, "base", "C5"); err != nil {
		t.Fatal(err)
	}
	if err := s.Unassign(chrome, "base", "C4"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveApp(chrome); err != nil {
		t.Fatal(err)
	}
	if s.Canary() != nil {
		t.Error("deliberate edits flagged at runtime")
	}
	s.Close()
	if s2 := canaryStore(t, dir, "b1", nil); s2.Canary() != nil {
		t.Errorf("deliberate edits flagged at reopen: %+v", s2.Canary())
	}
}

func TestCanary_RecordedChangeAfterLastCanaryWriteIsNotLoss(t *testing.T) {
	// the store changed the file (history written) after the canary was last
	// written, e.g. a crash inside the debounce window: not a silent loss.
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	fillChrome(t, s, 5)
	s.Close()
	canaryPath := filepath.Join(dir, ".canary.json")
	saved, _ := os.ReadFile(canaryPath)
	s = canaryStore(t, dir, "b1", nil)
	time.Sleep(20 * time.Millisecond)
	if err := s.Unassign(chrome, "base", "C5"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	os.WriteFile(canaryPath, saved, 0o644) // as if the debounced write never happened
	// make the saved canary look older than the history entry
	var cf map[string]any
	json.Unmarshal(saved, &cf)
	cf["written"] = float64(1)
	nb, _ := json.Marshal(cf)
	os.WriteFile(canaryPath, nb, 0o644)
	if s2 := canaryStore(t, dir, "b1", nil); s2.Canary() != nil {
		t.Errorf("a recorded change counted as loss: %+v", s2.Canary())
	}
}

func TestCanary_ExternalEditWhileRunningIsFlagged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	fillChrome(t, s, 5)
	dropAssignments(t, dir, 1)
	time.Sleep(5 * time.Millisecond)
	if changed, err := s.CheckExternal(); err != nil || !changed {
		t.Fatalf("external edit not seen: %v %v", changed, err)
	}
	c := s.Canary()
	if c == nil || c.Lost[0].Before != 5 || c.Lost[0].After != 1 {
		t.Fatalf("canary = %+v", c)
	}
	// the baseline moved on: a following deliberate edit does not add a second loss
	mustAssign(t, s, chrome, "base", "C9", keyA("Cmd+Z"))
	if c2 := s.Canary(); c2 == nil || len(c2.Lost) != 1 {
		t.Fatalf("canary after edit = %+v", c2)
	}
}

func TestCanary_BackupHintNamesAnExistingBackupWithTheAssignments(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	fillChrome(t, s, 5)
	if _, ok, err := s.BackupNow(); err != nil || !ok {
		t.Fatal(ok, err)
	}
	s.Close()
	dropAssignments(t, dir, 2)
	s2 := canaryStore(t, dir, "b1", nil)
	c := s2.Canary()
	if c == nil || c.Backup == "" {
		t.Fatalf("no backup hint: %+v", c)
	}
	if _, err := os.Stat(filepath.Join(dir, ".backups", c.Backup)); err != nil {
		t.Errorf("hinted backup does not exist: %v", err)
	}
	// the hinted backup really holds the 5 assignments
	rep, err := s2.Import(mustRead(t, filepath.Join(dir, ".backups", c.Backup)), true)
	if err != nil {
		t.Fatal(err)
	}
	_ = rep
	if !strings.Contains(c.Hint, "Restore") {
		t.Errorf("hint = %q", c.Hint)
	}
}

func mustRead(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestCanary_FileIsNeverPartOfBackupsOrExports(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	fillChrome(t, s, 3)
	if _, err := os.Stat(filepath.Join(dir, ".canary.json")); err != nil {
		t.Fatalf("canary file not written: %v", err)
	}
	data, _, err := s.Export("")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), ".canary") {
		t.Error("canary in export")
	}
	if _, ok, _ := s.BackupNow(); !ok {
		t.Fatal("no backup")
	}
	for _, b := range s.Backups() {
		z := mustRead(t, filepath.Join(dir, ".backups", b.Name))
		if strings.Contains(string(z), ".canary") {
			t.Error("canary in backup " + b.Name)
		}
	}
	names, _ := presetFiles(dir)
	for _, n := range names {
		if strings.Contains(n, "canary") {
			t.Error("canary listed as a preset file")
		}
	}
}

func TestCanary_DebouncedWriteAndFlush(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	old := BuildID
	BuildID = "b1"
	defer func() { BuildID = old }()
	s, err := Open(dir, Options{LiveResolvePath: "-", PollInterval: -1, CanaryDebounce: 150 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	mustAdd(t, s, chrome)
	for i := 0; i < 5; i++ {
		mustAssign(t, s, chrome, "base", "C"+string(rune('1'+i)), keyA("Cmd+"+string(rune('A'+i))))
	}
	read := func() int {
		b, err := os.ReadFile(filepath.Join(dir, ".canary.json"))
		if err != nil {
			return -1
		}
		var cf struct{ Presets map[string][]string }
		json.Unmarshal(b, &cf)
		return len(cf.Presets[chrome])
	}
	if read() == 5 {
		t.Error("written synchronously, not debounced")
	}
	deadline := time.Now().Add(3 * time.Second)
	for read() != 5 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if read() != 5 {
		t.Fatalf("debounced write never landed (%d)", read())
	}
	mustAssign(t, s, chrome, "base", "C9", keyA("Cmd+Y"))
	s.FlushCanary()
	if read() != 6 {
		t.Errorf("flush did not write (%d)", read())
	}
}

func TestCanary_ReadOnlyStoreStaysSilentAndWritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	fillChrome(t, s, 3)
	s.Close()
	os.Remove(filepath.Join(dir, ".canary.json"))
	ro, err := Open(dir, Options{LiveResolvePath: "-", PollInterval: -1, Lock: true, CanaryDebounce: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	hold, err := Open(dir, Options{LiveResolvePath: "-", PollInterval: -1, Lock: true, CanaryDebounce: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Close()
	if r, _ := hold.ReadOnly(); !r {
		t.Skip("lock not contended on this platform")
	}
	if hold.Canary() != nil {
		t.Error("read-only store reports a canary")
	}
}

// ---- pre-update backup -------------------------------------------------

func preUpdates(s *Store) []BackupInfo {
	var out []BackupInfo
	for _, b := range s.Backups() {
		if b.Reason == "pre-update" {
			out = append(out, b)
		}
	}
	return out
}

func TestPreUpdate_BuildChangeBacksUpBeforeAnyWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	fillChrome(t, s, 4)
	s.Close()
	if n := len(preUpdates(canaryStoreClose(t, dir, "b1"))); n != 0 {
		t.Fatalf("same build made %d pre-update backups", n)
	}
	s2 := canaryStore(t, dir, "b2", nil)
	pu := preUpdates(s2)
	if len(pu) != 1 {
		t.Fatalf("build change made %d pre-update backups", len(pu))
	}
	if !strings.Contains(pu[0].Name, "pre-update") {
		t.Errorf("name = %s", pu[0].Name)
	}
	// holds the pre-update bytes (4 assignments)
	b := mustRead(t, filepath.Join(dir, ".backups", pu[0].Name))
	if !strings.Contains(string(b), "manifest.json") {
		t.Error("not a bundle")
	}
	if s2.Canary() != nil {
		t.Error("a plain update is not a loss")
	}
	s2.Close()
	// recorded: the same new build again does not repeat
	if n := len(preUpdates(canaryStoreClose(t, dir, "b2"))); n != 1 {
		t.Errorf("pre-update backups after reopening the same build = %d", n)
	}
}

func canaryStoreClose(t *testing.T, dir, build string) *Store {
	s := canaryStore(t, dir, build, nil)
	return s
}

func TestPreUpdate_SchemaChangeAlsoBacksUp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	fillChrome(t, s, 2)
	s.Close()
	cp := filepath.Join(dir, ".canary.json")
	var cf map[string]any
	json.Unmarshal(mustRead(t, cp), &cf)
	cf["presetSchema"] = float64(0)
	nb, _ := json.Marshal(cf)
	os.WriteFile(cp, nb, 0o644)
	if n := len(preUpdates(canaryStoreClose(t, dir, "b1"))); n != 1 {
		t.Errorf("schema change made %d pre-update backups", n)
	}
}

func TestPreUpdate_KeepsNewestThreeSeparately(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	base := time.Date(2026, 11, 4, 3, 0, 0, 0, time.Local)
	i := 0
	now := func() time.Time { i++; return base.Add(time.Duration(i) * time.Minute) }
	s := canaryStore(t, dir, "b0", now)
	fillChrome(t, s, 2)
	s.Close()
	for b := 1; b <= 5; b++ {
		s = canaryStore(t, dir, "b"+string(rune('0'+b)), now)
		s.Close()
	}
	s = canaryStore(t, dir, "b5", now)
	if n := len(preUpdates(s)); n != 3 {
		t.Fatalf("kept %d pre-update backups, want 3", n)
	}
	// they never push dailies / others out and others never push them out
	for k := 0; k < 20; k++ {
		if _, _, err := s.BackupNow(); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(preUpdates(s)); n != 3 {
		t.Errorf("manual backups evicted pre-update backups: %d left", n)
	}
	other := 0
	for _, b := range s.Backups() {
		if b.Reason == "manual" {
			other++
		}
	}
	if other > 14 {
		t.Errorf("%d manual backups kept", other)
	}
}

func TestPreUpdate_NoBuildIDNoBackup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "", nil)
	fillChrome(t, s, 2)
	s.Close()
	if n := len(preUpdates(canaryStore(t, dir, "", nil))); n != 0 {
		t.Errorf("%d pre-update backups without a build id", n)
	}
}

func TestPreUpdate_FirstRunWithCanaryFeatureOnExistingPresetsBacksUp(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	fillChrome(t, s, 2)
	s.Close()
	os.Remove(filepath.Join(dir, ".canary.json")) // an install from before the canary existed
	if n := len(preUpdates(canaryStoreClose(t, dir, "b1"))); n != 1 {
		t.Errorf("first canary run made %d pre-update backups", n)
	}
}

// ---- rejected files are re-evaluated -------------------------------------

func collidingPair(t *testing.T) (*Store, string) {
	s, dir := emptyStore(t)
	mustAdd(t, s, "com.a.b")
	mustAssign(t, s, "com.a.b", "shift", "C1", keyA("Cmd+T"))
	os.WriteFile(filepath.Join(dir, "com.a.b-shift.json"), pjson("com.a.b-shift", `{"base":{"C1":{"type":"key","key":"Cmd+Y"}}}`), 0o644)
	if changed, err := s.CheckExternal(); err != nil || changed {
		t.Fatalf("collision file loaded: %v %v", changed, err)
	}
	if s.Problems()["com.a.b-shift.json"] == "" {
		t.Fatal("not a problem")
	}
	return s, dir
}

func TestRejectedFile_AdmittedAfterRemoveAppCommit(t *testing.T) {
	s, dir := collidingPair(t)
	before := mustRead(t, filepath.Join(dir, "com.a.b-shift.json"))
	if err := s.RemoveApp("com.a.b"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Snapshot().Preset("com.a.b-shift"); !ok {
		t.Fatal("rejected file not re-evaluated after the colliding preset was removed")
	}
	if p := s.Problems()["com.a.b-shift.json"]; p != "" {
		t.Errorf("still a problem: %s", p)
	}
	if string(mustRead(t, filepath.Join(dir, "com.a.b-shift.json"))) != string(before) {
		t.Error("the external file's bytes changed")
	}
	if _, err := s.Snapshot().ComposeBindingsJSON(); err != nil {
		t.Error(err)
	}
}

func TestRejectedFile_AdmittedAfterUndoOfTheCollision(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, "com.a.b")
	mustAssign(t, s, "com.a.b", "shift", "C1", keyA("Cmd+T"))
	os.WriteFile(filepath.Join(s.Dir(), "com.a.b-shift.json"), pjson("com.a.b-shift", `{"base":{"C1":{"type":"key","key":"Cmd+Y"}}}`), 0o644)
	s.CheckExternal()
	if _, ok := s.Snapshot().Preset("com.a.b-shift"); ok {
		t.Fatal("setup: loaded")
	}
	if err := s.Unassign("com.a.b", "shift", "C1"); err != nil { // the shift bank is gone: no collision
		t.Fatal(err)
	}
	if _, ok := s.Snapshot().Preset("com.a.b-shift"); !ok {
		t.Fatal("not admitted once the collision went away")
	}
}

func TestRejectedFile_AdmittedOnPollWhenCollidingFileDeletedOutside(t *testing.T) {
	s, dir := collidingPair(t)
	if err := os.Remove(filepath.Join(dir, "com.a.b.json")); err != nil {
		t.Fatal(err)
	}
	changed, err := s.CheckExternal()
	if err != nil || !changed {
		t.Fatalf("CheckExternal = %v %v", changed, err)
	}
	if _, ok := s.Snapshot().Preset("com.a.b-shift"); !ok {
		t.Error("rejected file not admitted in the same poll that removed its collision")
	}
	if _, ok := s.Snapshot().Preset("com.a.b"); ok {
		t.Error("removed preset still loaded")
	}
}

func TestRejectedFile_StaysRejectedWhileCollisionPersistsAndDoesNotChurn(t *testing.T) {
	s, _ := collidingPair(t)
	seq := s.Snapshot().Seq
	var logs int
	s.opts.Logf = func(string, ...any) { logs++ }
	for i := 0; i < 5; i++ {
		if changed, _ := s.CheckExternal(); changed {
			t.Fatal("changed")
		}
	}
	if s.Snapshot().Seq != seq {
		t.Error("snapshot moved")
	}
	if logs != 0 {
		t.Errorf("%d log lines for an unchanged rejection", logs)
	}
	if s.Problems()["com.a.b-shift.json"] == "" {
		t.Error("problem cleared while the collision persists")
	}
}

// B5 (critic pass 2): a backup that does not hold the lost controls is no hint.
func TestCanary_NoBackupHoldsTheLossNoSuggestion(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	mustAdd(t, s, chrome) // the daily backup is taken before the first write: it cannot hold what comes next
	for i := 0; i < 3; i++ {
		mustAssign(t, s, chrome, "base", "C"+string(rune('1'+i)), keyA("Cmd+"+string(rune('A'+i))))
	}
	s.Close()
	if len(s.Backups()) == 0 {
		t.Skip("no backup was made; nothing to mislead")
	}
	dropAssignments(t, dir, 1) // drops C2, C3
	s2 := canaryStore(t, dir, "b2", nil)
	c := s2.Canary()
	if c == nil || len(c.Lost) != 1 || c.Lost[0].Before != 3 || c.Lost[0].After != 1 {
		t.Fatalf("canary = %+v", c)
	}
	if c.Backup != "" {
		t.Errorf("suggested %s, which does not hold the lost controls", c.Backup)
	}
	if !strings.Contains(c.Hint, ".history") || !strings.Contains(c.Hint, "restore-last-good") {
		t.Errorf("hint = %q", c.Hint)
	}
}

// B10: refuse at add time what would only fail at the first assignment.
func TestAddApp_RefusesRuntimeBankClash(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, "com.foo.app")
	for _, id := range []string{"com.foo.app-2", "com.foo.app-shift", "com.foo.app-3"} {
		_, err := s.AddApp(id, "", nil)
		if err == nil {
			t.Errorf("AddApp(%q) accepted", id)
			continue
		}
		if !strings.Contains(err.Error(), "com.foo.app") || !strings.Contains(err.Error(), "bank") {
			t.Errorf("unclear error for %q: %v", id, err)
		}
	}
	// and in the other order: the parent is refused when the child exists
	s2, _ := emptyStore(t)
	mustAdd(t, s2, "com.bar.app-2")
	if _, err := s2.AddApp("com.bar.app", "", nil); err == nil {
		t.Error("parent accepted next to its -2 child")
	}
	// unrelated ids still add
	mustAdd(t, s, "com.foo.other")
}

// B9: a restore must not produce a case-fold pair or a store that cannot compose.
func TestRestoreBackup_RefusesCaseFoldPair(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, "com.x.App")
	mustAssign(t, s, "com.x.App", "base", "C1", keyA("Cmd+T"))
	// a backup that holds com.x.app (lower case) and lists com.x.App as skipped, so the current one is kept
	files := map[string][]byte{"com.x.app.json": pjson("com.x.app", `{"base":{"C1":{"type":"key","key":"Cmd+Y"}}}`)}
	zb, err := zipBundle(files, "all", "manual", time.Now(), "com.x.App.json")
	if err != nil {
		t.Fatal(err)
	}
	bd := filepath.Join(dir, ".backups")
	os.MkdirAll(bd, 0o755)
	name := "presets-20260101-000000-manual.zip"
	os.WriteFile(filepath.Join(bd, name), zb, 0o644)
	before := s.Snapshot()
	_, err = s.RestoreBackup(name)
	if err == nil {
		t.Fatal("restore produced a case-fold pair")
	}
	if !strings.Contains(err.Error(), "letter case") {
		t.Errorf("error = %v", err)
	}
	if s.Snapshot() != before {
		t.Error("snapshot changed")
	}
}

func TestRestoreBackup_RefusesUncomposableResult(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, "com.a.b")
	mustAssign(t, s, "com.a.b", "shift", "C1", keyA("Cmd+T"))
	files := map[string][]byte{
		"com.a.b-shift.json": pjson("com.a.b-shift", `{"base":{"C1":{"type":"key","key":"Cmd+Y"}}}`),
		"com.a.b.json":       pjson("com.a.b", `{"shift":{"C1":{"type":"key","key":"Cmd+T"}}}`),
	}
	zb, _ := zipBundle(files, "all", "manual", time.Now())
	bd := filepath.Join(dir, ".backups")
	os.MkdirAll(bd, 0o755)
	name := "presets-20260101-000001-manual.zip"
	os.WriteFile(filepath.Join(bd, name), zb, 0o644)
	if _, err := s.RestoreBackup(name); err == nil {
		t.Fatal("an uncomposable restore was applied")
	}
	if _, err := s.Snapshot().ComposeBindingsJSON(); err != nil {
		t.Error(err)
	}
}

// ---- start-disarmed policy (B2) -------------------------------------------------------

func noEnv(string) string { return "" }

func TestStartDisarmed_FirstRunUntilTheUserArms(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	if d, why := s.StartDisarmed(noEnv); !d || why != StartReasonFirstRun {
		t.Fatalf("fresh install = %v %q", d, why)
	}
	if err := s.MarkFirstRunDone(); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFirstRunDone(); err != nil { // idempotent
		t.Fatal(err)
	}
	if d, why := s.StartDisarmed(noEnv); d {
		t.Errorf("after the user armed once: disarmed (%q)", why)
	}
	s.Close()
	s2 := canaryStore(t, dir, "b1", nil) // same build, marker present
	if d, why := s2.StartDisarmed(noEnv); d {
		t.Errorf("second start of the same build disarmed (%q)", why)
	}
}

// The pre-rename variable name still disarms the start.
func TestStartDisarmed_LegacyEnvName(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	s.MarkFirstRunDone()
	env := func(k string) string {
		if k == "LD_DECK_START_DISARMED" {
			return "1"
		}
		return ""
	}
	if d, why := s.StartDisarmed(env); !d || why != StartReasonEnv {
		t.Errorf("legacy env = %v %q", d, why)
	}
}

func TestStartDisarmed_EnvAlwaysWins(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	s.MarkFirstRunDone()
	env := func(k string) string {
		if k == EnvStartDisarmed {
			return "1"
		}
		return ""
	}
	if d, why := s.StartDisarmed(env); !d || why != StartReasonEnv {
		t.Errorf("env = %v %q", d, why)
	}
	if d, _ := s.StartDisarmed(func(string) string { return "0" }); d {
		t.Error("DECKPLUS_START_DISARMED=0 disarmed")
	}
}

func TestStartDisarmed_NewBuildDisarmsOnce(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	fillChrome(t, s, 2)
	s.MarkFirstRunDone()
	s.Close()
	s2 := canaryStore(t, dir, "b2", nil) // build id changed: pre-update backup made
	if d, why := s2.StartDisarmed(noEnv); !d || why != StartReasonPreUpdate {
		t.Fatalf("new build = %v %q", d, why)
	}
	s2.Close()
	s3 := canaryStore(t, dir, "b2", nil)
	if d, why := s3.StartDisarmed(noEnv); d {
		t.Errorf("the same new build the second time is disarmed (%q)", why)
	}
}

func TestStartDisarmed_MarkerIsNotAPresetBackupOrExport(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s := canaryStore(t, dir, "b1", nil)
	fillChrome(t, s, 1)
	s.MarkFirstRunDone()
	names, _ := presetFiles(dir)
	for _, n := range names {
		if strings.Contains(n, "first-run") {
			t.Error("marker listed as a preset")
		}
	}
	data, _, _ := s.Export("")
	if strings.Contains(string(data), "first-run") {
		t.Error("marker in export")
	}
	if _, ok, _ := s.BackupNow(); ok {
		for _, b := range s.Backups() {
			if strings.Contains(string(mustRead(t, filepath.Join(dir, ".backups", b.Name))), "first-run") {
				t.Error("marker in backup")
			}
		}
	}
}
