package preset

import (
	"path/filepath"
	"testing"
)

func resolveFixture(t *testing.T) *Store {
	t.Helper()
	s, _ := emptyStore(t)
	mustAdd(t, s, chrome)
	a := func(app, bank, ctl, js string) {
		if err := s.Assign(app, bank, ctl, raw(t, js)); err != nil {
			t.Fatalf("assign %s %s %s: %v", app, bank, ctl, err)
		}
	}
	a(GlobalID, "base", "C1", `{"type":"key","key":"Cmd+1"}`)
	a(GlobalID, "base", "C2", `{"type":"key","key":"Cmd+2"}`)
	a(GlobalID, "layer-2", "C2", `{"type":"key","key":"Cmd+Shift+2"}`)
	a(GlobalID, "shift", "C3", `{"type":"key","key":"Cmd+Shift+3"}`)
	a(GlobalID, "base", "D1", `{"type":"scrollDelta","linesPerDetent":2,"detentThreshold":6}`)
	a(chrome, "base", "C1", `{"type":"key","key":"Cmd+R"}`)
	a(chrome, "layer-2", "C1", `{"type":"key","key":"Cmd+T"}`)
	a(chrome, "base", "D1", `{"action":"browser.scroll","linesPerDetent":5}`)
	a(chrome, "shift", "C4", `{"type":"key","key":"Cmd+W"}`)
	return s
}

func TestResolveForRungOrder(t *testing.T) {
	snap := resolveFixture(t).Snapshot()
	cases := []struct {
		name         string
		bundle       string
		layer        int
		ctl          string
		wantOK       bool
		wantKey, src string
		wantBank     string
	}{
		{"app base", chrome, 1, "C1", true, "Cmd+R", "app", chrome},
		{"app layer wins over app base", chrome, 2, "C1", true, "Cmd+T", "app", chrome + "-2"},
		{"layer 2 miss -> app base before global layer", chrome, 2, "D1", true, "", "app", chrome},
		{"app lacks control -> global base", chrome, 1, "C2", true, "Cmd+2", "global", "default"},
		{"layer 2: app has none -> global layer 2 beats global base", chrome, 2, "C2", true, "Cmd+Shift+2", "global", "default-2"},
		{"unknown app uses global", "com.unknown.App", 1, "C1", true, "Cmd+1", "global", "default"},
		{"empty bundle uses global", "", 1, "C1", true, "Cmd+1", "global", "default"},
		{"nothing anywhere", chrome, 1, "C9", false, "", "", ""},
		{"layer 3 falls to layer-1 chain", chrome, 3, "C1", true, "Cmd+R", "app", chrome},
	}
	for _, c := range cases {
		eb, ok := snap.ResolveFor(c.bundle, c.layer, c.ctl)
		if ok != c.wantOK {
			t.Errorf("%s: ok=%v", c.name, ok)
			continue
		}
		if !ok {
			continue
		}
		if eb.Source != c.src || eb.Bank != c.wantBank || (c.wantKey != "" && eb.Binding.Key != c.wantKey) {
			t.Errorf("%s: got source=%s bank=%s key=%q", c.name, eb.Source, eb.Bank, eb.Binding.Key)
		}
	}
}

func TestResolveForFieldLevelInheritance(t *testing.T) {
	snap := resolveFixture(t).Snapshot()
	eb, ok := snap.ResolveFor(chrome, 1, "D1")
	if !ok {
		t.Fatal("no D1")
	}
	if eb.Binding.Type != "scrollDelta" || eb.Binding.LinesPerDetent != 5 || eb.Binding.DetentThreshold != 1 {
		// browser.scroll seeds detentThreshold=1 into the app assignment on Assign
		t.Errorf("binding = %+v", eb.Binding)
	}
	if eb.FieldSource["linesPerDetent"] != "app" || eb.Action != "browser.scroll" {
		t.Errorf("field source = %v action %s", eb.FieldSource, eb.Action)
	}
	// An app assignment WITHOUT a seeded threshold inherits Global's 6.
	s := resolveFixture(t)
	if err := s.Assign(chrome, "base", "D1", raw(t, `{"type":"scrollDelta","linesPerDetent":5}`)); err != nil {
		t.Fatal(err)
	}
	eb, _ = s.Snapshot().ResolveFor(chrome, 1, "D1")
	if eb.Binding.DetentThreshold != 6 || eb.FieldSource["detentThreshold"] != "global" || eb.Binding.LinesPerDetent != 5 {
		t.Errorf("inherit: %+v %v", eb.Binding, eb.FieldSource)
	}
}

func TestResolveForModOverlay(t *testing.T) {
	snap := resolveFixture(t).Snapshot()
	if eb, ok := snap.ResolveForMod(chrome, "shift", 1, "C4"); !ok || eb.Binding.Key != "Cmd+W" || eb.Bank != chrome+"-shift" {
		t.Errorf("app shift: %+v %v", eb, ok)
	}
	if eb, ok := snap.ResolveForMod(chrome, "shift", 1, "C3"); !ok || eb.Source != "global" || eb.Bank != "default-shift" {
		t.Errorf("global shift: %+v %v", eb, ok)
	}
	// overlay miss falls through to the normal chain
	if eb, ok := snap.ResolveForMod(chrome, "shift", 1, "C1"); !ok || eb.Binding.Key != "Cmd+R" {
		t.Errorf("overlay fall-through: %+v %v", eb, ok)
	}
}

func TestResolveForIsCachedPerSnapshotAndFreshAfterEdit(t *testing.T) {
	s := resolveFixture(t)
	a, _ := s.Snapshot().ResolveFor(chrome, 1, "C1")
	b, _ := s.Snapshot().ResolveFor(chrome, 1, "C1")
	if a.Binding.Key != b.Binding.Key {
		t.Fatal("cache returned different results")
	}
	_ = s.SetValue(chrome, "base", "C1", "key", "Cmd+L")
	c, _ := s.Snapshot().ResolveFor(chrome, 1, "C1")
	if c.Binding.Key != "Cmd+L" {
		t.Errorf("stale after edit: %s", c.Binding.Key)
	}
}

func TestResolveBundleFallsThroughToGlobal(t *testing.T) {
	s, _ := seededStore(t)
	if err := s.Assign(GlobalID, "base", "Highlights", raw(t, `{"type":"key","key":"Cmd+Tab"}`)); err != nil {
		t.Fatal(err)
	}
	eb, ok := s.Snapshot().ResolveFor(ResolveID, 1, "Highlights")
	if !ok || eb.Source != "global" {
		t.Errorf("Resolve -> global fallback: %+v %v", eb, ok)
	}
	if _, ok := s.Snapshot().ResolveFor(ResolveID, 1, "Exposure"); ok {
		t.Error("Resolve legacy bank leaked into ResolveFor")
	}
}

func TestComposeConfigMergesAppsAndGlobal(t *testing.T) {
	s := resolveFixture(t)
	dev := filepath.Join("testdata", "device-map.json")
	cfg, err := s.Snapshot().ComposeConfig(dev)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ bank, ctl, key string }{
		{"default", "C1", "Cmd+1"}, {"default-2", "C2", "Cmd+Shift+2"}, {"default-shift", "C3", "Cmd+Shift+3"},
		{chrome, "C1", "Cmd+R"}, {chrome + "-2", "C1", "Cmd+T"}, {chrome + "-shift", "C4", "Cmd+W"},
	} {
		b, ok := cfg.Binding(c.bank, c.ctl)
		if !ok || b.Key != c.key {
			t.Errorf("%s.%s = %+v %v want %s", c.bank, c.ctl, b, ok, c.key)
		}
	}
	// the app's D1 carries the seeded browser.scroll fields
	if b, _ := cfg.Binding(chrome, "D1"); b.Type != "scrollDelta" || b.LinesPerDetent != 5 {
		t.Errorf("chrome D1 = %+v", b)
	}
	// composing twice is deterministic
	raw1, _ := s.Snapshot().ComposeBindingsJSON()
	raw2, _ := s.Snapshot().ComposeBindingsJSON()
	if string(raw1) != string(raw2) {
		t.Error("compose not deterministic")
	}
}

func TestResolveLegacyFollowsConfigCascade(t *testing.T) {
	s, _ := seededStore(t)
	snap := s.Snapshot()
	// edit page: ControlDial is a jog; D1 snaps to minutes
	if eb, ok := snap.ResolveLegacy("edit", 1, "ControlDial"); !ok || eb.Binding.Type != "jog" || eb.Bank != "edit" || eb.Source != "app" {
		t.Errorf("ControlDial = %+v %v", eb, ok)
	}
	if eb, ok := snap.ResolveLegacy("edit", 1, "D1"); !ok || eb.Binding.Type != "jogMinuteSnap" {
		t.Errorf("D1 = %+v %v", eb, ok)
	}
	// a control only in col falls back from edit to col
	if eb, ok := snap.ResolveLegacy("edit", 1, "Blacks"); !ok || eb.Binding.Type != "wheel" || eb.Bank != "col" {
		t.Errorf("Blacks = %+v %v", eb, ok)
	}
	// nothing in Resolve -> Global
	_ = s.Assign(GlobalID, "base", "Highlights", raw(t, `{"type":"key","key":"Cmd+Tab"}`))
	if eb, ok := s.Snapshot().ResolveLegacy("edit", 1, "Highlights"); !ok || eb.Source != "global" {
		t.Errorf("Highlights = %+v", eb)
	}
	// every binding in the live file resolves to itself through the cascade
	cfgBanks := s.Snapshot().presets[ResolveID].Banks
	for _, ctl := range []string{"C1", "P1btn"} {
		for _, bn := range []string{"edit", "col"} {
			if _, ok := cfgBanks[bn][ctl]; ok {
				if eb, ok := snap.ResolveLegacy(bn, 1, ctl); !ok || eb.LogicalBank != bn {
					t.Errorf("%s/%s resolved to %+v", bn, ctl, eb)
				}
			}
		}
	}
}
