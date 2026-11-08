package preset

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"2t5-deck-plus/internal/config"
)

func sum(t *testing.T, p string) [32]byte {
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

// The seeded Resolve preset, composed, equals the live ld-resolve config
// binding for binding and setting for setting.
func TestSeededResolveEqualsLiveConfig(t *testing.T) {
	s, _ := seededStore(t)
	dev := filepath.Join("testdata", "device-map.json")
	got, err := s.Snapshot().ComposeConfig(dev)
	if err != nil {
		t.Fatal(err)
	}
	want, err := config.Load(dev, filepath.Join("testdata", "live-ld-resolve-bindings.json"))
	if err != nil {
		t.Fatal(err)
	}
	wb := want.AllBindings()
	if len(wb) == 0 {
		t.Fatal("live config has no banks")
	}
	n, total := 0, 0
	for _, m := range wb {
		total += len(m)
	}
	for bank, m := range wb {
		for ctl, w := range m {
			g, ok := got.Binding(bank, ctl)
			if !ok {
				t.Errorf("%s.%s missing from composed config", bank, ctl)
				continue
			}
			if !reflect.DeepEqual(g, w) {
				t.Errorf("%s.%s differs:\n got %+v\nwant %+v", bank, ctl, g, w)
			}
			n++
		}
	}
	for bank, m := range got.AllBindings() {
		if strings.Contains(bank, ".") { // app banks (finder) are extra by design
			continue
		}
		for ctl := range m {
			if _, ok := want.Binding(bank, ctl); !ok {
				t.Errorf("composed %s.%s not in live config", bank, ctl)
			}
		}
	}
	if !reflect.DeepEqual(got.TopLevel, want.TopLevel) {
		t.Errorf("TopLevel differs:\n got %+v\nwant %+v", got.TopLevel, want.TopLevel)
	}
	if n != total || n < 80 {
		t.Errorf("compared %d of %d bindings", n, total)
	}
	// the Finder bank is composed under its bundle id so the existing engine branch keeps working
	if b, ok := got.Binding(FinderID, "P1btn"); !ok || b.Type != "finderTag" || b.Tag != "Red" {
		t.Errorf("finder P1btn = %+v %v", b, ok)
	}
}

func TestSeedCreatesExpectedPresets(t *testing.T) {
	s, dir := seededStore(t)
	snap := s.Snapshot()
	var ids []string
	for _, p := range snap.Presets() {
		ids = append(ids, p.ID)
	}
	if !reflect.DeepEqual(ids, []string{GlobalID, ResolveID, FinderID}) {
		t.Fatalf("presets = %v", ids)
	}
	g, _ := snap.Preset(GlobalID)
	if len(g.Packs) != 1 || g.Packs[0] != "generic" || g.Assigned() != 0 {
		t.Errorf("global = %+v", g)
	}
	r, _ := snap.Preset(ResolveID)
	if _, ok := r.Banks["com.apple.finder"]; ok {
		t.Error("app bank leaked into Resolve preset")
	}
	if _, ok := r.Settings["hud"]; !ok {
		t.Error("_config not mapped to settings")
	}
	f, _ := snap.Preset(FinderID)
	if len(f.Banks["base"]) != 8 || f.Packs[0] != "finder" {
		t.Errorf("finder = %+v", f)
	}
	for _, n := range []string{"_global.json", ResolveID + ".json", FinderID + ".json"} {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("missing %s", n)
		}
	}
}

// Seeding reads its sources and writes only inside the presets dir.
func TestSeedNeverWritesOutsidePresetsDir(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "live.json")
	repo := filepath.Join(root, "repo.json")
	copyFile(t, filepath.Join("testdata", "live-ld-resolve-bindings.json"), live)
	copyFile(t, filepath.Join("testdata", "2t5-deck-plus-repo-bindings.json"), repo)
	h1, h2 := sum(t, live), sum(t, repo)
	st1, _ := os.Stat(live)
	dir := filepath.Join(root, "presets")
	s, err := Open(dir, Options{SeedFrom: repo, LiveResolvePath: live, PollInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if sum(t, live) != h1 || sum(t, repo) != h2 {
		t.Error("a seed source was modified")
	}
	if st2, _ := os.Stat(live); !st2.ModTime().Equal(st1.ModTime()) {
		t.Error("seed source mtime changed")
	}
	es, _ := os.ReadDir(root)
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	if !reflect.DeepEqual(names, []string{"live.json", "presets", "repo.json"}) {
		t.Errorf("root now holds %v", names)
	}
}

func TestSeedFallsBackWhenLiveUnreadable(t *testing.T) {
	root := t.TempDir()
	var logs []string
	s, err := Open(filepath.Join(root, "presets"), Options{
		SeedFrom:        filepath.Join("testdata", "2t5-deck-plus-repo-bindings.json"),
		LiveResolvePath: filepath.Join(root, "does-not-exist.json"),
		PollInterval:    -1,
		Logf:            func(f string, a ...any) { logs = append(logs, f) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, ok := s.Snapshot().Preset(ResolveID); !ok {
		t.Fatal("no Resolve preset from fallback")
	}
	found := false
	for _, l := range logs {
		if strings.Contains(l, "falling back") {
			found = true
		}
	}
	if !found {
		t.Errorf("no fallback log line: %v", logs)
	}
}

func TestOpenDoesNotReseedNonEmptyDir(t *testing.T) {
	s, dir := seededStore(t)
	if err := s.Unassign(FinderID, "base", "P1btn"); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s2, err := Open(dir, Options{SeedFrom: filepath.Join("testdata", "2t5-deck-plus-repo-bindings.json"), LiveResolvePath: "-", PollInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	f, _ := s2.Snapshot().Preset(FinderID)
	if _, ok := f.Banks["base"]["P1btn"]; ok {
		t.Error("reseeded over user edit")
	}
}

func TestOpenRefusesMissingParent(t *testing.T) {
	_, err := Open(filepath.Join(t.TempDir(), "nope", "presets"), Options{PollInterval: -1, LiveResolvePath: "-"})
	if err == nil {
		t.Fatal("created a path under a missing parent")
	}
}
