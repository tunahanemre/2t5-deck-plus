package preset

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"2t5-deck-plus/internal/actions"
)

// Wave P (critic pass 7): nothing guest-writable is loaded without the gate.

const attackerShell = `{"base":{"C1":{"type":"shell","cmd":"/usr/bin/open","args":["-a","Calculator"]},"C2":{"type":"key","key":"Cmd+T"}}}`

func trustStore(t *testing.T, root string) (*Store, string) {
	t.Helper()
	dir := filepath.Join(root, "presets")
	o := Options{LiveResolvePath: "-", PollInterval: -1, StateDir: filepath.Join(root, "state"), CanaryDebounce: -1, Lock: true}
	s, err := Open(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func reopenTrust(t *testing.T, root string) *Store {
	t.Helper()
	s, _ := trustStore(t, root)
	t.Cleanup(s.Close)
	return s
}

func hasShell(p *Preset) bool {
	for _, bm := range p.Banks {
		for _, a := range bm {
			if actions.EffectiveType(a) == "shell" {
				return true
			}
		}
	}
	return false
}

func TestP1_StartupGapAndQuarantineSurvivesARestart(t *testing.T) {
	root := t.TempDir()
	s, dir := trustStore(t, root)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C2", keyA("Cmd+T"))
	s.MarkFirstRunDone()
	s.Close()
	// a guest drops a file with a program while the app is closed
	attacker := pjson(chrome, attackerShell)
	os.WriteFile(filepath.Join(dir, chrome+".json"), attacker, 0o644)

	s2 := reopenTrust(t, root)
	p, _ := s2.Snapshot().Preset(chrome)
	if hasShell(p) {
		t.Fatal("startup applied a risky file with no gate")
	}
	if p.Banks["base"]["C2"] == nil {
		t.Error("the last-good version is not what runs")
	}
	if pend := s2.PendingExternal(); len(pend) != 1 || pend[0].Risks[0] != "shell" {
		t.Fatalf("pending = %+v", s2.PendingExternal())
	}
	if d, why := s2.StartDisarmed(func(string) string { return "" }); !d || why != StartReasonExternal {
		t.Errorf("start = %v %q, want disarmed (external-changes)", d, why)
	}
	if got, _ := os.ReadFile(filepath.Join(dir, chrome+".json")); !bytes.Equal(got, attacker) {
		t.Error("the file on disk was touched")
	}
	// quit and come back: the quarantine does not evaporate
	s2.Close()
	s3 := reopenTrust(t, root)
	if p, _ := s3.Snapshot().Preset(chrome); hasShell(p) {
		t.Fatal("the relaunch applied what the previous run had held back")
	}
	pend := s3.PendingExternal()
	if len(pend) != 1 {
		t.Fatalf("pending after the restart = %+v", pend)
	}
	if d, why := s3.StartDisarmed(func(string) string { return "" }); !d || why != StartReasonExternal {
		t.Errorf("restart = %v %q", d, why)
	}
	// approve is explicit; afterwards the next start is clean
	if err := s3.ApproveExternal(pend[0].ID, true); err != nil {
		t.Fatal(err)
	}
	s3.Close()
	s4 := reopenTrust(t, root)
	if p, _ := s4.Snapshot().Preset(chrome); !hasShell(p) {
		t.Error("the approved change is gone")
	}
	if len(s4.PendingExternal()) != 0 {
		t.Errorf("pending after approving: %+v", s4.PendingExternal())
	}
}

func TestP1_RuntimeQuarantineThenRestart(t *testing.T) {
	root := t.TempDir()
	s, dir := trustStore(t, root)
	mustAdd(t, s, chrome)
	s.MarkFirstRunDone()
	os.WriteFile(filepath.Join(dir, chrome+".json"), pjson(chrome, attackerShell), 0o644)
	if changed, _ := s.CheckExternal(); changed || len(s.PendingExternal()) != 1 {
		t.Fatalf("runtime: changed=%v pending=%v", changed, s.PendingExternal())
	}
	s.Close() // the clean close must not turn the held-back bytes into the new "last good"
	s2 := reopenTrust(t, root)
	if p, _ := s2.Snapshot().Preset(chrome); hasShell(p) {
		t.Fatal("the restart applied a quarantined file")
	}
	if len(s2.PendingExternal()) != 1 {
		t.Errorf("pending lost across the restart: %v", s2.PendingExternal())
	}
}

func TestP1_HarmlessDifferencesAtStartupAreAcceptedAndNothingHeldWithoutAManifest(t *testing.T) {
	root := t.TempDir()
	s, dir := trustStore(t, root)
	mustAdd(t, s, chrome)
	s.MarkFirstRunDone()
	s.Close()
	os.WriteFile(filepath.Join(dir, chrome+".json"), pjson(chrome, `{"base":{"C1":{"type":"key","key":"Cmd+T"}}}`), 0o644)
	s2 := reopenTrust(t, root)
	if p, _ := s2.Snapshot().Preset(chrome); p.Banks["base"]["C1"] == nil || len(s2.PendingExternal()) != 0 {
		t.Error("a harmless difference was held back")
	}
	if d, _ := s2.StartDisarmed(func(string) string { return "" }); d {
		t.Error("a harmless difference forced a disarmed start")
	}
	// no manifest yet (first run of this build): trust once and build one
	root2 := t.TempDir()
	dir2 := filepath.Join(root2, "presets")
	plain, err := Open(dir2, Options{LiveResolvePath: "-", PollInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	mustAdd(t, plain, chrome)
	plain.Close()
	os.WriteFile(filepath.Join(dir2, chrome+".json"), pjson(chrome, attackerShell), 0o644)
	s3, err := Open(dir2, Options{LiveResolvePath: "-", PollInterval: -1, StateDir: filepath.Join(root2, "state")})
	if err != nil {
		t.Fatal(err)
	}
	defer s3.Close()
	if p, _ := s3.Snapshot().Preset(chrome); !hasShell(p) {
		t.Error("without a manifest the folder is trusted once")
	}
	if _, err := os.Stat(filepath.Join(root2, "state", "trust", "manifest.json")); err != nil {
		t.Errorf("no manifest built: %v", err)
	}
}

func plantBackup(t *testing.T, dir, name string, files map[string][]byte) {
	t.Helper()
	zb, err := zipBundle(files, "all", "manual", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(filepath.Join(dir, ".backups"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, ".backups", name), zb, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestP1_PlantedBackupIsJudgedLikeAnImportButOurOwnRestoresFreely(t *testing.T) {
	root := t.TempDir()
	s, dir := trustStore(t, root)
	defer s.Close()
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C2", keyA("Cmd+T"))
	own, ok, err := s.BackupNowLabelled("mine")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	plantBackup(t, dir, "presets-20260102-000000-manual.zip", map[string][]byte{chrome + ".json": pjson(chrome, attackerShell)})
	byName := map[string]BackupInfo{}
	for _, b := range s.Backups() {
		byName[b.Name] = b
	}
	if !byName[own.Name].Trusted || byName["presets-20260102-000000-manual.zip"].Trusted {
		t.Errorf("trust flags: own=%v planted=%v", byName[own.Name].Trusted, byName["presets-20260102-000000-manual.zip"].Trusted)
	}
	// restoring the planted zip without confirmation is refused with the report
	_, err = s.RestoreBackup("presets-20260102-000000-manual.zip")
	var re *RiskError
	if !errors.As(err, &re) || re.Report.Risk.Shell != 1 {
		t.Fatalf("planted restore = %v", err)
	}
	if p, _ := s.Snapshot().Preset(chrome); hasShell(p) {
		t.Fatal("a planted backup restored a program")
	}
	// skipRisky restores everything but the program
	rep, err := s.RestoreBackupOpts("presets-20260102-000000-manual.zip", ImportOptions{SkipRisky: true})
	if err != nil || len(rep.Dropped) != 1 {
		t.Fatalf("skipRisky restore: %+v %v", rep.Dropped, err)
	}
	if p, _ := s.Snapshot().Preset(chrome); hasShell(p) {
		t.Error("skipRisky restored the program")
	}
	// our own backup restores without a question
	if _, err := s.RestoreBackup(own.Name); err != nil {
		t.Errorf("own backup: %v", err)
	}
	// ...and the planted one with an explicit confirmation
	if _, err := s.RestoreBackupOpts("presets-20260102-000000-manual.zip", ImportOptions{ConfirmRisk: true}); err != nil {
		t.Errorf("confirmed planted restore: %v", err)
	}
}

func TestP1_BackupsNeverCarryQuarantinedBytes(t *testing.T) {
	root := t.TempDir()
	s, dir := trustStore(t, root)
	defer s.Close()
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C2", keyA("Cmd+T"))
	os.WriteFile(filepath.Join(dir, chrome+".json"), pjson(chrome, attackerShell), 0o644)
	s.CheckExternal()
	if len(s.PendingExternal()) != 1 {
		t.Fatal("setup: nothing held")
	}
	b, ok, err := s.BackupNow()
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	zb, _ := os.ReadFile(filepath.Join(dir, ".backups", b.Name))
	if strings.Contains(string(zb), "Calculator") || strings.Contains(string(zb), "usr/bin/open") {
		t.Error("an app-made backup carries the quarantined outside bytes")
	}
	if !strings.Contains(unzipText(t, zb, chrome+".json"), "Cmd+T") {
		t.Error("the backup does not hold what is running")
	}
}

func unzipText(t *testing.T, zb []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(zb), int64(len(zb)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == name {
			rc, _ := f.Open()
			var buf bytes.Buffer
			buf.ReadFrom(rc)
			rc.Close()
			return buf.String()
		}
	}
	return ""
}

func TestP2_PlantedHistoryIsNotRestoredAsLastGood(t *testing.T) {
	root := t.TempDir()
	s, dir := trustStore(t, root)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C1", keyA("Cmd+T"))
	mustAssign(t, s, chrome, "base", "C2", keyA("Cmd+Y")) // saves the previous file as an app-made history copy
	s.Close()
	hdir := filepath.Join(dir, ".history", chrome)
	appCopies, _ := os.ReadDir(hdir)
	if len(appCopies) == 0 {
		t.Fatal("no app-made history")
	}
	// a guest plants a NEWER history copy with a program
	os.WriteFile(filepath.Join(hdir, "99990101T000000.000.json"), pjson(chrome, attackerShell), 0o644)
	// the preset file gets broken, so restore-last-good has to look at the history
	os.WriteFile(filepath.Join(dir, chrome+".json"), []byte("{broken"), 0o644)
	s2 := reopenTrust(t, root)
	src, err := s2.RestoreLastGood(chrome)
	if err != nil {
		t.Fatalf("restore-last-good: %v", err)
	}
	if src.File == "99990101T000000.000.json" {
		t.Fatal("the planted history copy was used")
	}
	if p, _ := s2.Snapshot().Preset(chrome); hasShell(p) {
		t.Error("a planted history copy restored a program")
	}
	// with ONLY a planted copy there is nothing to restore
	s2.Close()
	for _, e := range appCopies {
		os.Remove(filepath.Join(hdir, e.Name()))
	}
	os.WriteFile(filepath.Join(dir, chrome+".json"), []byte("{broken"), 0o644)
	s3 := reopenTrust(t, root)
	if _, err := s3.RestoreLastGood(chrome); err == nil {
		if p, _ := s3.Snapshot().Preset(chrome); hasShell(p) {
			t.Error("only a planted copy existed and it was restored")
		}
	}
}

func TestP3_HomeUnsetOrTestRunFailsClosed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "presets")
	s, err := Open(dir, Options{LiveResolvePath: "-", PollInterval: -1, StateDirErr: errors.New("HOME is not set")})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.MarkFirstRunDone()
	if d, why := s.StartDisarmed(func(string) string { return "" }); !d || why != StartReasonStateDown {
		t.Errorf("start = %v %q", d, why)
	}
	found := false
	for _, w := range s.Warnings() {
		if w.Kind == "state-dir" && strings.Contains(w.Message, "HOME") {
			found = true
		}
	}
	if !found {
		t.Error("no warning")
	}
}

// ---- wave P P3 ------------------------------------------------------------------------------------

func TestP3_RejectedNameIsBoundedAndTheListIsPruned(t *testing.T) {
	s, dir := emptyStore(t)
	long := "com.example." + strings.Repeat("longname.", 25) + "App" // ~230 characters
	if err := os.WriteFile(filepath.Join(dir, long+".json"), pjson(long, attackerShell), 0o644); err != nil {
		t.Skipf("the volume refuses such a name: %v", err)
	}
	s.CheckExternal()
	pend := s.PendingExternal()
	if len(pend) != 1 {
		t.Fatalf("pending = %v problems=%v", pend, s.Problems())
	}
	if err := s.RejectExternal(pend[0].ID); err != nil {
		t.Fatalf("a long id can never be rejected: %v", err)
	}
	es, _ := os.ReadDir(filepath.Join(dir, ".backups"))
	for _, e := range es {
		if strings.HasPrefix(e.Name(), "rejected-") && len(e.Name()) > 120 {
			t.Errorf("rejected file name is %d characters", len(e.Name()))
		}
	}
	// pruning: only the newest 30 rejected-* copies stay
	bd := filepath.Join(dir, ".backups")
	for i := 0; i < 45; i++ {
		os.WriteFile(filepath.Join(bd, fmt.Sprintf("rejected-x-%03d.json", i)), []byte("{}"), 0o644)
		os.Chtimes(filepath.Join(bd, fmt.Sprintf("rejected-x-%03d.json", i)), time.Unix(1000+int64(i), 0), time.Unix(1000+int64(i), 0))
	}
	s.pruneRejected(bd)
	n := 0
	oldest := ""
	es, _ = os.ReadDir(bd)
	for _, e := range es {
		if strings.HasPrefix(e.Name(), "rejected-x-") {
			n++
			if oldest == "" || e.Name() < oldest {
				oldest = e.Name()
			}
		}
	}
	if n > maxRejected || oldest < "rejected-x-015.json" {
		t.Errorf("kept %d (oldest %s)", n, oldest)
	}
}

func TestP3_PendingListAndWarningsAreCapped(t *testing.T) {
	s, dir := emptyStore(t)
	for i := 0; i < maxPending+8; i++ {
		id := fmt.Sprintf("com.example.App%03d", i)
		os.WriteFile(filepath.Join(dir, id+".json"), pjson(id, attackerShell), 0o644)
	}
	s.CheckExternal()
	if n := len(s.PendingExternal()); n > maxPending {
		t.Errorf("%d pending", n)
	}
	w := 0
	for _, x := range s.Warnings() {
		if x.Kind == "pending-external" {
			w++
		}
	}
	if w > maxPending {
		t.Errorf("%d warnings", w)
	}
}

func TestP3_AUserEditOverwritesAndClearsTheStalePendingItem(t *testing.T) {
	s, dir := emptyStore(t)
	mustAdd(t, s, chrome)
	os.WriteFile(filepath.Join(dir, chrome+".json"), pjson(chrome, attackerShell), 0o644)
	s.CheckExternal()
	pe := s.PendingExternal()[0]
	mustAssign(t, s, chrome, "base", "C5", keyA("Cmd+T")) // the user's own edit rewrites the file
	if len(s.PendingExternal()) != 0 {
		t.Fatal("stale attacker content still waits for approval")
	}
	if err := s.ApproveExternal(pe.ID, true); !errors.Is(err, ErrNoPending) {
		t.Errorf("approving the stale item: %v", err)
	}
	if p, _ := s.Snapshot().Preset(chrome); hasShell(p) {
		t.Error("stale attacker content was applied over the user's edit")
	}
}

func TestP3_ConfirmationIsCheckedInsideTheCommit(t *testing.T) {
	s, _ := emptyStore(t)
	data := bundleOf(t, map[string][]byte{chrome + ".json": pjson(chrome, `{"base":{"C1":{"type":"shell","cmd":"/usr/bin/open"}}}`)})
	plain, _ := s.ImportOpts(data, true, ImportOptions{})
	sig := RiskSignature(plain)
	if _, err := s.ImportOpts(data, false, ImportOptions{ConfirmRisk: true, ExpectRiskSig: sig}); err != nil {
		t.Fatalf("matching signature: %v", err)
	}
	s2, _ := emptyStore(t)
	var rc *RiskChangedError
	_, err := s2.ImportOpts(data, false, ImportOptions{ConfirmRisk: true, ExpectRiskSig: "stale"})
	if !errors.As(err, &rc) || !errors.Is(err, ErrRiskChanged) || rc.Report.Risk.Shell != 1 {
		t.Fatalf("stale signature = %v", err)
	}
	if _, ok := s2.Snapshot().Preset(chrome); ok {
		t.Error("installed despite the stale confirmation")
	}
}

func TestP3_PresetsFolderIsRestrictedAndPollIsCapped(t *testing.T) {
	s, dir := emptyStore(t)
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("presets dir mode = %v %v", fi.Mode().Perm(), err)
	}
	// more files than the poll looks at: the extra ones must not look removed
	mustAdd(t, s, chrome)
	for i := 0; i < maxPollFiles+20; i++ {
		os.WriteFile(filepath.Join(dir, fmt.Sprintf("com.zz.F%04d.json", i)), []byte("{}"), 0o644)
	}
	if changed, err := s.CheckExternal(); err != nil {
		t.Fatal(err, changed)
	}
	if _, ok := s.Snapshot().Preset(chrome); !ok {
		t.Error("a preset vanished because the folder holds more files than the poll cap")
	}
	// a file over the size cap is ignored with a message, not read
	big := make([]byte, maxPollBytes+10)
	os.WriteFile(filepath.Join(dir, "com.aa.Big.json"), big, 0o644)
	s.CheckExternal()
	if msg := s.Problems()["com.aa.Big.json"]; !strings.Contains(msg, "larger") {
		t.Errorf("problem = %q", msg)
	}
}
