package preset

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// 2026-11-04: the app's own first-run Resolve preset was held back as an "outside change" (and so vanished from the
// picker, with the deck forced disarmed) because the trust manifest left over from an earlier install/run had no
// matching copy of it.

// seedStore is trustStore with a bundled bindings file to seed Resolve from.
func seedStore(t *testing.T, root string) (*Store, string) {
	t.Helper()
	dir := filepath.Join(root, "presets")
	o := Options{LiveResolvePath: "-", SeedFrom: filepath.Join("..", "..", "config", "bindings.json"), PollInterval: -1, StateDir: filepath.Join(root, "state"), CanaryDebounce: -1, Lock: true}
	s, err := Open(dir, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, dir
}

func editManifest(t *testing.T, root string, f func(m map[string]any)) {
	t.Helper()
	p := filepath.Join(root, "state", "trust", "manifest.json")
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	f(m)
	b, _ = json.Marshal(m)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSeedTrust_ManifestWithoutAnEntryButWithTheIdenticalLastGoodCopy(t *testing.T) {
	root := t.TempDir()
	s, _ := seedStore(t, root)
	s.MarkFirstRunDone()
	s.Close()
	editManifest(t, root, func(m map[string]any) { delete(m["presets"].(map[string]any), ResolveID) })
	s2, _ := seedStore(t, root)
	if p, _ := s2.Snapshot().Preset(ResolveID); p == nil {
		t.Fatal("Resolve vanished from memory")
	}
	if pend := s2.PendingExternal(); len(pend) != 0 {
		t.Fatalf("our own unchanged file was held back as an outside change: %+v", pend)
	}
	if d, why := s2.StartDisarmed(func(string) string { return "" }); d {
		t.Errorf("start disarmed (%s) for no reason", why)
	}
}

func TestSeedTrust_FirstRunSeedIsNeverJudgedAgainstAStaleManifest(t *testing.T) {
	root := t.TempDir()
	s, dir := seedStore(t, root)
	s.MarkFirstRunDone()
	s.Close()
	// an older install's record: a stripped Resolve copy that the manifest vouches for
	stripped := []byte(`{"schema":1,"id":"` + ResolveID + `","name":"DaVinci Resolve","packs":["resolve","generic"],"banks":{},"settings":{}}`)
	lg := filepath.Join(root, "state", "trust", "lastgood", ResolveID+".json")
	if err := os.WriteFile(lg, stripped, 0o600); err != nil {
		t.Fatal(err)
	}
	editManifest(t, root, func(m map[string]any) { m["presets"].(map[string]any)[ResolveID] = hashHex(stripped) })
	// the presets folder is new/empty: the app seeds it itself
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	s2, _ := seedStore(t, root)
	if p, _ := s2.Snapshot().Preset(ResolveID); p == nil || len(p.Banks) == 0 {
		t.Fatal("the seeded Resolve preset is not what runs")
	}
	if pend := s2.PendingExternal(); len(pend) != 0 {
		t.Fatalf("the app's own seed was held back: %+v", pend)
	}
}

func TestSeedTrust_ARealOutsideChangeIsStillHeld(t *testing.T) {
	root := t.TempDir()
	s, dir := trustStore(t, root)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C2", keyA("Cmd+T"))
	s.MarkFirstRunDone()
	s.Close()
	editManifest(t, root, func(m map[string]any) { delete(m["presets"].(map[string]any), chrome) }) // no hash either: lastgood still the baseline
	os.WriteFile(filepath.Join(dir, chrome+".json"), pjson(chrome, attackerShell), 0o644)
	s2 := reopenTrust(t, root)
	if p, _ := s2.Snapshot().Preset(chrome); p == nil || hasShell(p) {
		t.Fatal("the running version must stay, without the program")
	}
	if len(s2.PendingExternal()) != 1 {
		t.Fatalf("pending = %+v", s2.PendingExternal())
	}
}

func TestCanary_AStaleLossNoticeIsDroppedOnceThePresetIsBackInFull(t *testing.T) {
	root := t.TempDir()
	s, dir := trustStore(t, root)
	mustAdd(t, s, chrome)
	mustAssign(t, s, chrome, "base", "C2", keyA("Cmd+T"))
	s.MarkFirstRunDone()
	s.Close()
	cp := filepath.Join(dir, ".canary.json")
	var cf map[string]any
	b, err := os.ReadFile(cp)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(b, &cf)
	cf["pending"] = map[string]any{"lost": []any{map[string]any{"preset": chrome, "before": 1, "after": 0, "controls": []any{"base.C2"}}}, "since": 1}
	b, _ = json.Marshal(cf)
	os.WriteFile(cp, b, 0o600)
	s2 := reopenTrust(t, root)
	if ci := s2.Canary(); ci != nil {
		t.Fatalf("a loss that was repaired is still reported: %+v", ci)
	}
}
