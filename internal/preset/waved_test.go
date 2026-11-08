package preset

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const safari = "com.apple.Safari"

func readAll(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	names, err := presetFiles(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			t.Fatal(err)
		}
		out[n] = string(b)
	}
	return out
}

func noTemps(t *testing.T, dir string) {
	t.Helper()
	es, _ := os.ReadDir(dir)
	for _, e := range es {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %s", e.Name())
		}
	}
}

// ---- B3: multi-file atomicity ------------------------------------------------

func twoAppStore(t *testing.T) (*Store, string) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	mustAdd(t, s, safari)
	return s, dir
}

func TestApplyRollsBackWhenALaterRenameFails(t *testing.T) {
	s, dir := twoAppStore(t)
	before := readAll(t, dir)
	seq, undoLen := s.Snapshot().Seq, len(s.undo)
	boom := errors.New("disk said no")
	s.renameFn = func(o, n string) error {
		if strings.HasSuffix(n, safari+".json") {
			return boom
		}
		return os.Rename(o, n)
	}
	err := s.commit("two files", func(ps map[string]*Preset) error {
		ps[chrome].Banks["base"] = map[string]Assignment{"C1": raw(t, `{"type":"none"}`)}
		ps[safari].Banks["base"] = map[string]Assignment{"C1": raw(t, `{"type":"none"}`)}
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "disk said no") {
		t.Fatalf("err = %v", err)
	}
	after := readAll(t, dir)
	for n, b := range before {
		if after[n] != b {
			t.Errorf("%s changed after a failed apply", n)
		}
	}
	if len(after) != len(before) {
		t.Errorf("files %d -> %d", len(before), len(after))
	}
	noTemps(t, dir)
	if s.Snapshot().Seq != seq || len(s.undo) != undoLen || len(s.redo) != 0 {
		t.Errorf("snapshot/stacks moved: seq %d->%d undo %d->%d", seq, s.Snapshot().Seq, undoLen, len(s.undo))
	}
	if s.Snapshot().presets[chrome].Assigned() != 0 {
		t.Error("snapshot took the failed change")
	}
	// our own rollback is never reported as an external edit
	changed, err := s.CheckExternal()
	if err != nil || changed {
		t.Errorf("CheckExternal after rollback = %v, %v", changed, err)
	}
	// and the store still works once the disk behaves
	s.renameFn = os.Rename
	if err := s.Assign(chrome, "base", "C1", raw(t, `{"type":"none"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestApplyRollsBackNewAndRemovedFiles(t *testing.T) {
	s, dir := twoAppStore(t)
	before := readAll(t, dir)
	s.removeFn = func(p string) error {
		if strings.HasSuffix(p, safari+".json") {
			return errors.New("cannot remove")
		}
		return os.Remove(p)
	}
	// adds a new app (new file), edits chrome, removes safari (fails last)
	err := s.commit("mixed", func(ps map[string]*Preset) error {
		ps["com.new.App"] = &Preset{Schema: 1, ID: "com.new.App", Name: "N", Packs: []string{"generic"}, Banks: map[string]map[string]Assignment{}, Settings: map[string]any{}, Extra: map[string]any{}}
		ps[chrome].Name = "Renamed"
		delete(ps, safari)
		return nil
	})
	if err == nil {
		t.Fatal("expected an error")
	}
	after := readAll(t, dir)
	if len(after) != len(before) {
		t.Fatalf("file set changed: %v", after)
	}
	for n, b := range before {
		if after[n] != b {
			t.Errorf("%s not restored", n)
		}
	}
	noTemps(t, dir)
	if ok, _ := s.CheckExternal(); ok {
		t.Error("rollback seen as external edit")
	}
}

func TestUndoTravelFailureKeepsStacksAndFiles(t *testing.T) {
	s, dir := twoAppStore(t)
	for i, c := range []string{"C1", "C2", "C3"} {
		_ = i
		if err := s.Assign(chrome, "base", c, raw(t, `{"type":"none"}`)); err != nil {
			t.Fatal(err)
		}
	}
	before := readAll(t, dir)
	u, r := len(s.undo), len(s.redo)
	s.renameFn = func(o, n string) error { return errors.New("nope") }
	if _, _, err := s.Undo(); err == nil {
		t.Fatal("undo should fail")
	}
	if _, err := s.JumpTo(3); err == nil {
		t.Fatal("jump should fail")
	}
	if len(s.undo) != u || len(s.redo) != r {
		t.Errorf("stacks moved: %d/%d -> %d/%d", u, r, len(s.undo), len(s.redo))
	}
	for n, b := range readAll(t, dir) {
		if before[n] != b {
			t.Errorf("%s changed", n)
		}
	}
	noTemps(t, dir)
	s.renameFn = os.Rename
	if _, ok, err := s.Undo(); err != nil || !ok {
		t.Fatalf("undo after recovery: %v %v", ok, err)
	}
}

func TestStageFailureTouchesNothing(t *testing.T) {
	s, dir := twoAppStore(t)
	before := readAll(t, dir)
	// make the presets dir unwritable for temp files: replace dir perms
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Skip("chmod unsupported")
	}
	defer os.Chmod(dir, 0o755)
	if os.Geteuid() == 0 {
		t.Skip("root ignores permissions")
	}
	err := s.Assign(chrome, "base", "C1", raw(t, `{"type":"none"}`))
	os.Chmod(dir, 0o755)
	if err == nil {
		t.Fatal("expected a write error")
	}
	for n, b := range readAll(t, dir) {
		if before[n] != b {
			t.Errorf("%s changed", n)
		}
	}
	noTemps(t, dir)
}

// ---- B4: ordered notification --------------------------------------------------

func TestDeliverDropsStaleSnapshots(t *testing.T) {
	s, _ := emptyStore(t)
	var got []int
	s.OnChange(func(sn *Snapshot) { got = append(got, sn.Seq) })
	base := s.Snapshot().Seq
	subs, _ := s.subsLocked()
	s.deliver(subs, &Snapshot{Seq: base + 6})
	s.deliver(subs, &Snapshot{Seq: base + 5}) // late, older
	s.deliver(subs, &Snapshot{Seq: base + 6}) // duplicate
	s.deliver(subs, &Snapshot{Seq: base + 7})
	want := []int{base + 6, base + 7}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("got %v want %v", got, want)
	}
}

func TestConcurrentEditsNotifyInOrder(t *testing.T) {
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	var mu sync.Mutex
	var seqs []int
	s.OnChange(func(sn *Snapshot) { mu.Lock(); seqs = append(seqs, sn.Seq); mu.Unlock() })
	var wg sync.WaitGroup
	ctl := []string{"C1", "C2", "C3", "C4", "C5", "C6", "C7", "C8"}
	for _, c := range ctl {
		wg.Add(1)
		go func(c string) {
			defer wg.Done()
			_ = s.Assign(chrome, "base", c, map[string]any{"type": "none"})
		}(c)
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if len(seqs) == 0 {
		t.Fatal("no notifications")
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] <= seqs[i-1] {
			t.Fatalf("out of order: %v", seqs)
		}
	}
	if seqs[len(seqs)-1] != s.Snapshot().Seq {
		t.Errorf("last delivered %d != current %d", seqs[len(seqs)-1], s.Snapshot().Seq)
	}
}

func TestSeqGate(t *testing.T) {
	var g SeqGate
	var ran []int
	for _, q := range []int{3, 2, 3, 4, 1, 9} {
		q := q
		g.Do(q, func() { ran = append(ran, q) })
	}
	if len(ran) != 3 || ran[0] != 3 || ran[1] != 4 || ran[2] != 9 {
		t.Errorf("ran %v", ran)
	}
}

// ---- B2: backups by class ------------------------------------------------------

func countReason(s *Store, reason string) int {
	n := 0
	for _, b := range s.Backups() {
		if b.Reason == reason {
			n++
		}
	}
	return n
}

func TestBackupRetentionByClass(t *testing.T) {
	s, _, c := clockStore(t, 3)
	mustAdd(t, s, chrome)
	// 5 days with a write each: dailies are kept to 3
	for d := 0; d < 5; d++ {
		c.t = c.t.Add(24 * time.Hour)
		mustAssign(t, s, chrome, "base", "C1", map[string]any{"type": "key", "key": "Cmd+" + string(rune('a'+d))})
	}
	if got := countReason(s, "daily"); got != 3 {
		t.Fatalf("dailies = %d, want 3", got)
	}
	dailies := map[string]bool{}
	for _, b := range s.Backups() {
		if b.Reason == "daily" {
			dailies[b.Name] = true
		}
	}
	// a burst of manual / restore backups never evicts a daily
	for i := 0; i < 8; i++ {
		c.t = c.t.Add(time.Minute)
		if _, ok, err := s.BackupNow(); err != nil || !ok {
			t.Fatalf("manual backup: %v %v", ok, err)
		}
	}
	if got := countReason(s, "manual"); got != 3 {
		t.Errorf("manual = %d, want 3 (own limit)", got)
	}
	for _, b := range s.Backups() {
		delete(dailies, b.Name)
	}
	if len(dailies) != 0 {
		t.Errorf("dailies evicted by non-daily backups: %v", dailies)
	}
	if got := countReason(s, "daily"); got != 3 {
		t.Errorf("dailies = %d after the burst", got)
	}
}

func TestOldStyleBackupNamesAreReadAndClassified(t *testing.T) {
	s, dir, _ := clockStore(t, 2)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C1", map[string]any{"type": "none"}) // makes a daily
	bd := filepath.Join(dir, ".backups")
	var daily string
	for _, b := range s.Backups() {
		daily = b.Name
	}
	// rename it to the pre-class naming
	old := "presets-20261103-090000.zip"
	if err := os.Rename(filepath.Join(bd, daily), filepath.Join(bd, old)); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, b := range s.Backups() {
		if b.Name == old {
			found = true
			if b.Reason != "daily" {
				t.Errorf("old-style reason = %q", b.Reason)
			}
		}
	}
	if !found {
		t.Fatalf("old-style backup not listed: %v", s.Backups())
	}
}

func TestDailyMarkerSetOnlyAfterASuccessfulBackup(t *testing.T) {
	s, dir, _ := clockStore(t, 0)
	mustAdd(t, s, chrome) // first write of the day: daily made
	if countReason(s, "daily") != 1 {
		t.Fatalf("want a daily, got %v", s.Backups())
	}
	// next day, the backups dir cannot be created: block it with a file
	c := &clock{time.Date(2026, 11, 4, 10, 0, 0, 0, time.Local)}
	s.opts.Now = c.now
	bd := filepath.Join(dir, ".backups")
	saved := filepath.Join(dir, "backups-saved")
	if err := os.Rename(bd, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bd, []byte("in the way"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logged int
	s.opts.Logf = func(f string, a ...any) { logged++ }
	mustAssign(t, s, chrome, "base", "C1", map[string]any{"type": "none"}) // the edit still succeeds
	if logged == 0 {
		t.Error("failed backup was not logged")
	}
	if s.lastBackupDay == "20261104" {
		t.Fatal("day marked done although no backup was written")
	}
	// the obstacle goes away: the NEXT write retries and succeeds
	os.Remove(bd)
	os.Rename(saved, bd)
	mustAssign(t, s, chrome, "base", "C2", map[string]any{"type": "none"})
	if s.lastBackupDay != "20261104" {
		t.Error("retry did not mark the day")
	}
	n := 0
	for _, b := range s.Backups() {
		if strings.HasPrefix(b.Name, "presets-20261104-") && b.Reason == "daily" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("daily backups for 11-04 = %d, want 1", n)
	}
	// and no more are made that day
	mustAssign(t, s, chrome, "base", "C3", map[string]any{"type": "none"})
	if countReason(s, "daily") != 2 {
		t.Errorf("dailies = %d", countReason(s, "daily"))
	}
}

// ---- B5: unreadable presets ----------------------------------------------------

func reopen(t *testing.T, dir string, opts Options) *Store {
	t.Helper()
	opts.PollInterval = -1
	if opts.LiveResolvePath == "" {
		opts.LiveResolvePath = "-"
	}
	s, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestUnreadableAppFileIsKeptListedAndNeverDropped(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	s.Close()
	bad := filepath.Join(dir, chrome+".json")
	garbage := []byte(`{"id": "com.google.Chrome", "banks": [`)
	if err := os.WriteFile(bad, garbage, 0o644); err != nil {
		t.Fatal(err)
	}
	s2 := reopen(t, dir, Options{})
	if _, ok := s2.Snapshot().Preset(chrome); ok {
		t.Fatal("a broken preset was loaded")
	}
	got, _ := os.ReadFile(bad)
	if string(got) != string(garbage) {
		t.Error("the broken file was touched")
	}
	pl := s2.ProblemList()
	if len(pl) != 1 || pl[0].ID != chrome || pl[0].File != chrome+".json" || pl[0].Error == "" {
		t.Fatalf("problems = %+v", pl)
	}
	var sawErr bool
	for _, w := range s2.Warnings() {
		if w.Kind == "unreadable-file" && w.Severity == "error" && w.Field == chrome+".json" {
			sawErr = true
		}
	}
	if !sawErr {
		t.Error("no error-level warning")
	}
	// editing other things must not touch the broken file either
	mustAdd(t, s2, safari)
	got, _ = os.ReadFile(bad)
	if string(got) != string(garbage) {
		t.Error("the broken file was touched by another edit")
	}
	// the backups taken for the new write hold the broken file as it was
	if _, err := s2.RestoreLastGood(chrome); !errors.Is(err, ErrNoGoodCopy) {
		t.Errorf("restore without any good copy = %v", err)
	}
}

func TestRestoreLastGoodAfterAnOutsideEditBrokeTheFile(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C1", map[string]any{"type": "key", "key": "Cmd+T"})
	if _, err := s.RestoreLastGood(chrome); !errors.Is(err, ErrNotBroken) {
		t.Fatalf("restore of a healthy preset = %v", err)
	}
	bad := filepath.Join(dir, chrome+".json")
	garbage := []byte("{ not json")
	os.WriteFile(bad, garbage, 0o644)
	changed, err := s.CheckExternal()
	if err != nil || changed {
		t.Fatalf("CheckExternal = %v %v", changed, err)
	}
	if p, ok := s.Snapshot().Preset(chrome); !ok || p.Assigned() != 1 {
		t.Fatal("the last good state was dropped")
	}
	if len(s.Problems()) != 1 {
		t.Fatalf("problems = %v", s.Problems())
	}
	undo := len(s.undo)
	src, err := s.RestoreLastGood(chrome)
	if err != nil || src.Source != "memory" {
		t.Fatalf("restore: %+v %v", src, err)
	}
	b, _ := os.ReadFile(bad)
	if _, err := ParsePreset(b); err != nil {
		t.Fatalf("file still broken: %v", err)
	}
	if len(s.Problems()) != 0 {
		t.Errorf("problem not cleared: %v", s.Problems())
	}
	if len(s.undo) != undo+1 {
		t.Errorf("restore is %d undo steps", len(s.undo)-undo)
	}
	// the broken bytes were kept first
	es, _ := os.ReadDir(filepath.Join(dir, ".backups"))
	kept := 0
	for _, e := range es {
		if strings.HasPrefix(e.Name(), "broken-"+chrome+"-") {
			kept++
			if body, _ := os.ReadFile(filepath.Join(dir, ".backups", e.Name())); string(body) != string(garbage) {
				t.Error("kept copy differs from the broken file")
			}
		}
	}
	if kept != 1 {
		t.Errorf("kept copies = %d", kept)
	}
	if ok, _ := s.CheckExternal(); ok {
		t.Error("our own restore was seen as an external edit")
	}
	if _, err := s.RestoreLastGood(chrome); !errors.Is(err, ErrNotBroken) {
		t.Errorf("second restore = %v", err)
	}
}

func TestRestoreFromHistoryWhenNotLoaded(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C1", map[string]any{"type": "key", "key": "Cmd+T"})
	mustAssign(t, s, chrome, "base", "C2", map[string]any{"type": "key", "key": "Cmd+W"}) // history now holds the C1-only version
	s.Close()
	os.WriteFile(filepath.Join(dir, chrome+".json"), []byte("garbage"), 0o644)
	s2 := reopen(t, dir, Options{})
	if pl := s2.ProblemList(); len(pl) != 1 || !pl[0].CanRestore {
		t.Fatalf("problems = %+v", pl)
	}
	src, err := s2.RestoreLastGood(chrome)
	if err != nil || src.Source != "history" || src.File == "" {
		t.Fatalf("restore: %+v %v", src, err)
	}
	p, ok := s2.Snapshot().Preset(chrome)
	if !ok || p.Assigned() != 1 {
		t.Fatalf("restored preset = %+v", p)
	}
	if len(s2.Problems()) != 0 {
		t.Error("problem not cleared")
	}
	// undo removes it again (the broken bytes are safe in .backups)
	if _, ok, err := s2.Undo(); err != nil || !ok {
		t.Fatalf("undo: %v %v", ok, err)
	}
	if _, ok := s2.Snapshot().Preset(chrome); ok {
		t.Error("undo kept the restored preset")
	}
}

func resolveStore(t *testing.T) (*Store, string) {
	s, dir := seededStore(t)
	if _, ok := s.Snapshot().Preset(ResolveID); !ok {
		t.Fatal("seed has no Resolve preset")
	}
	return s, dir
}

func reopenSeeded(t *testing.T, dir string) *Store {
	return reopen(t, dir, Options{
		SeedFrom:        filepath.Join("testdata", "2t5-deck-plus-repo-bindings.json"),
		LiveResolvePath: filepath.Join("testdata", "live-ld-resolve-bindings.json"),
	})
}

func TestUnreadableResolveRunsOnSeedFallbackReadOnly(t *testing.T) {
	s, dir := resolveStore(t)
	s.Close()
	rf := filepath.Join(dir, ResolveID+".json")
	garbage := []byte(`{"id":"com.blackmagic-design.DaVinciResolve","banks":`)
	os.WriteFile(rf, garbage, 0o644)
	s2 := reopenSeeded(t, dir)
	p, ok := s2.Snapshot().Preset(ResolveID)
	if !ok || p.Assigned() == 0 {
		t.Fatal("Resolve has no fallback cascade")
	}
	// the compose still works: Resolve is not left without bindings
	if _, err := s2.Snapshot().ComposeBindingsJSON(); err != nil {
		t.Fatal(err)
	}
	pl := s2.ProblemList()
	if len(pl) != 1 || !pl[0].Fallback || !pl[0].CanRestore {
		t.Fatalf("problems = %+v", pl)
	}
	var msg string
	for _, w := range s2.Warnings() {
		if w.Kind == "unreadable-file" {
			msg = w.Message
		}
	}
	if !strings.Contains(msg, "shipped default") || !strings.Contains(msg, "read-only") {
		t.Errorf("warning does not say so: %q", msg)
	}
	got, _ := os.ReadFile(rf)
	if string(got) != string(garbage) {
		t.Fatal("the broken Resolve file was overwritten")
	}
	// editing the stand-in is refused; other presets are still editable
	err := s2.Assign(ResolveID, "col", "C1", map[string]any{"type": "none"})
	if !errors.Is(err, ErrFallbackReadOnly) {
		t.Errorf("edit of the fallback = %v", err)
	}
	if err := s2.SetSetting(ResolveID, "x", 1); !errors.Is(err, ErrFallbackReadOnly) {
		t.Errorf("setting of the fallback = %v", err)
	}
	mustAdd(t, s2, chrome)
	got, _ = os.ReadFile(rf)
	if string(got) != string(garbage) {
		t.Fatal("another edit overwrote the broken Resolve file")
	}
	// restore: no history yet, so the .seed copy is the source
	src, err := s2.RestoreLastGood(ResolveID)
	if err != nil || src.Source != "seed" {
		t.Fatalf("restore: %+v %v", src, err)
	}
	if len(s2.Problems()) != 0 {
		t.Error("problem not cleared")
	}
	if err := s2.Assign(ResolveID, "col", "C1", map[string]any{"type": "none"}); err != nil {
		t.Errorf("Resolve still read-only after restore: %v", err)
	}
	es, _ := os.ReadDir(filepath.Join(dir, ".backups"))
	kept := false
	for _, e := range es {
		kept = kept || strings.HasPrefix(e.Name(), "broken-"+ResolveID+"-")
	}
	if !kept {
		t.Error("broken Resolve file not kept")
	}
}

func TestFixingTheResolveFileExternallyLiftsTheFallback(t *testing.T) {
	s, dir := resolveStore(t)
	good, _ := os.ReadFile(filepath.Join(dir, ResolveID+".json"))
	s.Close()
	rf := filepath.Join(dir, ResolveID+".json")
	os.WriteFile(rf, []byte("nope"), 0o644)
	s2 := reopenSeeded(t, dir)
	os.WriteFile(rf, good, 0o644)
	changed, err := s2.CheckExternal()
	if err != nil || !changed {
		t.Fatalf("CheckExternal = %v %v", changed, err)
	}
	if len(s2.Problems()) != 0 || s2.fallback[ResolveID] {
		t.Error("fallback/problem not cleared")
	}
	if err := s2.Assign(ResolveID, "col", "C1", map[string]any{"type": "none"}); err != nil {
		t.Errorf("edit after the fix: %v", err)
	}
}

func TestSameSizeSameMtimeEditIsDetected(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C1", map[string]any{"type": "key", "key": "Cmd+A"})
	path := filepath.Join(dir, chrome+".json")
	before, _ := os.Stat(path)
	b, _ := os.ReadFile(path)
	edited := []byte(strings.Replace(string(b), "Cmd+A", "Cmd+B", 1))
	if len(edited) != len(b) {
		t.Fatal("test edit changed the size")
	}
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatal(err)
	}
	changed, err := s.CheckExternal()
	if err != nil || !changed {
		t.Fatalf("same-size same-mtime edit missed: %v %v", changed, err)
	}
	p, _ := s.Snapshot().Preset(chrome)
	if k, _ := p.Banks["base"]["C1"]["key"].(string); k != "Cmd+B" {
		t.Errorf("key = %q", k)
	}
}

// ---- 7: single-writer lock ---------------------------------------------------------

func TestSecondInstanceOpensReadOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	a := reopen(t, dir, Options{Lock: true})
	if ro, _ := a.ReadOnly(); ro {
		t.Fatal("first instance is read-only")
	}
	mustAdd(t, a, chrome)
	b := reopen(t, dir, Options{Lock: true})
	ro, holder := b.ReadOnly()
	if !ro || holder != os.Getpid() {
		t.Fatalf("second instance: ro=%v holder=%d (want %d)", ro, holder, os.Getpid())
	}
	if _, ok := b.Snapshot().Preset(chrome); !ok {
		t.Error("read-only instance cannot see the presets")
	}
	var roe *ReadOnlyError
	if err := b.Assign(chrome, "base", "C1", map[string]any{"type": "none"}); !errors.As(err, &roe) || roe.Holder != os.Getpid() {
		t.Errorf("write on the read-only instance = %v", err)
	}
	if _, err := b.AddApp(safari, "", nil); !errors.As(err, &roe) {
		t.Errorf("AddApp = %v", err)
	}
	if _, _, err := b.BackupNow(); !errors.As(err, &roe) {
		t.Errorf("BackupNow = %v", err)
	}
	if _, _, err := b.Undo(); err != nil { // nothing to undo: no write attempted
		t.Errorf("Undo with empty stack = %v", err)
	}
	// it still follows the writer's changes
	mustAdd(t, a, safari)
	if ok, err := b.CheckExternal(); err != nil || !ok {
		t.Fatalf("read-only instance did not pick up the writer's change: %v %v", ok, err)
	}
	if _, ok := b.Snapshot().Preset(safari); !ok {
		t.Error("new app not seen")
	}
	// the lock moves on when the writer closes
	a.Close()
	c := reopen(t, dir, Options{Lock: true})
	if ro, _ := c.ReadOnly(); ro {
		t.Error("third instance is read-only after the holder closed")
	}
	if err := c.Assign(chrome, "base", "C1", map[string]any{"type": "none"}); err != nil {
		t.Errorf("write after takeover: %v", err)
	}
}

func TestReadOnlyOnEmptyDirDoesNotWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	a := reopen(t, dir, Options{Lock: true})
	_ = a
	// simulate: holder has the dir but it is empty of presets
	for _, e := range func() []os.DirEntry { es, _ := os.ReadDir(dir); return es }() {
		if strings.HasSuffix(e.Name(), ".json") {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	b := reopen(t, dir, Options{Lock: true})
	if ro, _ := b.ReadOnly(); !ro {
		t.Fatal("expected read-only")
	}
	if names, _ := presetFiles(dir); len(names) != 0 {
		t.Errorf("read-only instance wrote %v", names)
	}
	if _, ok := b.Snapshot().Preset(GlobalID); !ok {
		t.Error("no in-memory Global")
	}
}
