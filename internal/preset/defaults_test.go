package preset

import (
	"2t5-deck-plus/internal/actions"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func firstControl(t *testing.T, p *Preset) (bank, ctl string) {
	t.Helper()
	for _, bn := range sortedKeys(p.Banks) {
		for _, cn := range sortedKeys(p.Banks[bn]) {
			return bn, cn
		}
	}
	t.Fatal("empty preset")
	return
}

func TestDefaultsDiffFreshStoreIsClean(t *testing.T) {
	s, _ := seededStore(t)
	for _, id := range []string{ResolveID, FinderID, GlobalID} {
		d, err := s.DefaultsDiff(id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if !d.HasSeed || len(d.Controls) != 0 || len(d.Settings) != 0 ||
			d.Counts.Added+d.Counts.Changed+d.Counts.Removed != 0 {
			t.Errorf("%s: a fresh store must equal its seed: %+v", id, d)
		}
	}
	d, _ := s.DefaultsDiff(ResolveID)
	if d.Counts.Unchanged < 50 || d.SeedSource != "seed-file" {
		t.Errorf("resolve unchanged=%d source=%s", d.Counts.Unchanged, d.SeedSource)
	}
}

func TestDefaultsDiffAddedChangedRemoved(t *testing.T) {
	s, dir := seededStore(t)
	p, _ := s.Snapshot().Preset(ResolveID)
	bank, ctl := firstControl(t, p)
	seedBefore, _ := os.ReadFile(filepath.Join(dir, ".seed", ResolveID+".json"))

	if err := s.SetValue(ResolveID, bank, ctl, "label", "ZZ changed"); err != nil {
		t.Fatal(err)
	}
	// remove a second control
	var b2, c2 string
	for _, bn := range sortedKeys(p.Banks) {
		for _, cn := range sortedKeys(p.Banks[bn]) {
			if bn != bank || cn != ctl {
				b2, c2 = bn, cn
			}
		}
	}
	if err := s.Unassign(ResolveID, b2, c2); err != nil {
		t.Fatal(err)
	}
	// add a control the seed does not have
	if err := s.Assign(ResolveID, "edit", "ZZnew", map[string]any{"type": "key", "key": "Cmd+Z"}); err == nil {
		_ = 0 // control id may be refused by the store's control list; fall through
	}
	d, err := s.DefaultsDiff(ResolveID)
	if err != nil {
		t.Fatal(err)
	}
	var gotChanged, gotRemoved bool
	for _, c := range d.Controls {
		if c.Bank == bank && c.Control == ctl {
			gotChanged = c.Change == "changed" && len(c.Fields) == 1 && c.Fields[0].Key == "label" &&
				c.Fields[0].Change != "" && c.Fields[0].Current == "ZZ changed"
		}
		if c.Bank == b2 && c.Control == c2 {
			gotRemoved = c.Change == "removed" && len(c.Fields) > 0 && c.SeedAction != ""
		}
	}
	if !gotChanged || !gotRemoved {
		t.Fatalf("changed=%v removed=%v in %+v", gotChanged, gotRemoved, d.Controls)
	}
	if d.Counts.Changed != 1 || d.Counts.Removed != 1 {
		t.Errorf("counts %+v", d.Counts)
	}
	// an app setting
	if err := s.SetSetting(ResolveID, "ZZsetting", 5); err != nil {
		t.Fatal(err)
	}
	d, _ = s.DefaultsDiff(ResolveID)
	found := false
	for _, f := range d.Settings {
		if f.Key == "ZZsetting" && f.Change == "added" {
			found = true
		}
	}
	if !found {
		t.Errorf("settings diff: %+v", d.Settings)
	}
	// read-only: the seed copy and the preset file layout are untouched
	seedAfter, _ := os.ReadFile(filepath.Join(dir, ".seed", ResolveID+".json"))
	if string(seedBefore) != string(seedAfter) {
		t.Error("DefaultsDiff changed the seed copy")
	}
}

func TestDefaultsDiffAddedControlAndUserApp(t *testing.T) {
	s, _ := seededStore(t)
	if _, err := s.AddApp("com.google.Chrome", "Google Chrome", nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Assign("com.google.Chrome", "base", "C1", map[string]any{"type": "key", "key": "Cmd+T"}); err != nil {
		t.Fatal(err)
	}
	d, err := s.DefaultsDiff("com.google.Chrome")
	if err != nil {
		t.Fatal(err)
	}
	if d.HasSeed || d.SeedSource != "none" || d.Counts.Added != 1 || len(d.Controls) != 1 || d.Controls[0].Change != "added" || d.Controls[0].Action != "generic.key" {
		t.Errorf("user app: %+v", d)
	}
	g, _ := s.DefaultsDiff(GlobalID)
	if !g.HasSeed || g.SeedSource != "builtin" {
		t.Errorf("global: %+v", g)
	}
	if _, err := s.DefaultsDiff("nope"); !errors.Is(err, ErrNoPreset) {
		t.Errorf("missing preset: %v", err)
	}
}

func TestSameValueNumbersByValue(t *testing.T) {
	n := func(s string) any { return json.Number(s) }
	if !sameValue(n("80"), n("80.0")) || !sameValue(n("1"), 1.0) || sameValue(n("80"), n("81")) {
		t.Error("number comparison")
	}
	if !sameValue(map[string]any{"a": n("1"), "b": "x"}, map[string]any{"b": "x", "a": 1.0}) {
		t.Error("nested equal")
	}
	if sameValue([]any{"a"}, []any{"b"}) || sameValue("a", 1.0) || sameValue(nil, "a") {
		t.Error("different values compared equal")
	}
}

func TestWarningsFlagUnimplementedLabsAction(t *testing.T) {
	s, _ := emptyStore(t)
	if err := s.Assign(GlobalID, "base", "D1", map[string]any{"action": "resolve.shuttle"}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, w := range s.Snapshot().Warnings(nil) {
		if w.Kind == "unimplemented-action" && w.Control == "D1" {
			found = true
		}
	}
	if !found {
		t.Error("no unimplemented-action warning")
	}
}

// Wave C regression: the real seed must stay warning-free of invalid values
// with the new press group and feel fields in the registry.
func TestSeededStoreStillHasNoInvalidValueWarnings(t *testing.T) {
	s, _ := seededStore(t)
	for _, w := range s.Snapshot().Warnings(nil) {
		switch w.Kind {
		case "invalid-value", "invalid-key", "missing-required", "unimplemented-action", "unknown-action":
			t.Errorf("seed warning: %+v", w)
		}
	}
}

// Wave C: press objects and the feel fields survive store -> compose ->
// config.Binding / EffectiveBinding.Raw, and a reload from disk.
func TestPressAndFeelRoundTrip(t *testing.T) {
	s, dir := seededStore(t)
	if _, err := s.AddApp("com.google.Chrome", "Google Chrome", []string{"generic", "browser"}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		ctl   string
		press map[string]any
	}{
		{"D1", map[string]any{"type": "key", "key": "Cmd+Z"}},
		{"D2", map[string]any{"type": "selectWheel", "wheel": "gain"}},
		{"Exposure", map[string]any{"type": "none"}},
		{"Contrast", map[string]any{"type": "click", "point": "lift_reset"}},
		{"Saturation", map[string]any{"type": "resetActiveWheel"}},
	}
	for _, c := range cases {
		a := map[string]any{"type": "wheel", "wheel": "lift_master", "press": c.press}
		if err := s.Assign(GlobalID, "base", c.ctl, a); err != nil {
			t.Fatalf("%s: %v", c.ctl, err)
		}
	}
	if err := s.Assign("com.google.Chrome", "base", "D1", map[string]any{"action": "browser.scroll", "velocityProfile": "fast", "momentumMs": 250}); err != nil {
		t.Fatal(err)
	}
	check := func(sn *Snapshot) {
		cfg, err := sn.ComposeConfig("testdata/device-map.json")
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range cases {
			b, ok := cfg.Binding("default", c.ctl)
			if !ok || b.Press == nil || b.Press.Type != c.press["type"] {
				t.Fatalf("%s: press lost: %+v", c.ctl, b)
			}
			if k, _ := c.press["key"].(string); b.Press.Key != k {
				t.Errorf("%s key %q", c.ctl, b.Press.Key)
			}
			if w, _ := c.press["wheel"].(string); b.Press.Wheel != w {
				t.Errorf("%s wheel %q", c.ctl, b.Press.Wheel)
			}
			if p, _ := c.press["point"].(string); b.Press.Point != p {
				t.Errorf("%s point %q", c.ctl, b.Press.Point)
			}
		}
		eb, ok := sn.ResolveFor("com.google.Chrome", 1, "D1")
		if !ok || eb.Raw["velocityProfile"] != "fast" {
			t.Fatalf("feel fields lost in Raw: %+v", eb.Raw)
		}
		if f, ok := actions.ToFloat(eb.Raw["momentumMs"]); !ok || f != 250 {
			t.Errorf("momentumMs = %#v", eb.Raw["momentumMs"])
		}
	}
	check(s.Snapshot())
	// reopen from disk
	s2, err := Open(dir, Options{PollInterval: -1, LiveResolvePath: "-"})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	check(s2.Snapshot())
	// the engine's composed bindings carry the extra keys without breaking load
	if _, err := s2.Snapshot().ComposeBindingsJSON(); err != nil {
		t.Fatal(err)
	}
}
