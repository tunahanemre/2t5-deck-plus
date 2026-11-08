package preset

import (
	"strings"
	"testing"

	"2t5-deck-plus/internal/config"
)

func modeFixture(t *testing.T) *Store {
	t.Helper()
	s := resolveFixture(t)
	a := func(app, bank, ctl, js string) {
		t.Helper()
		if err := s.Assign(app, bank, ctl, raw(t, js)); err != nil {
			t.Fatalf("assign %s %s %s: %v", app, bank, ctl, err)
		}
	}
	// Lightroom-style HSL panel: P1 does something different in each mode.
	a(chrome, "base", "P1", `{"type":"key","key":"Cmd+1"}`)
	a(chrome, "pm-sat", "P1", `{"type":"key","key":"Cmd+2"}`)
	a(chrome, "pm-lum", "P1", `{"type":"key","key":"Cmd+3"}`)
	a(GlobalID, "pm-sat", "P2", `{"type":"key","key":"Cmd+G"}`)
	a(GlobalID, "base", "P2", `{"type":"key","key":"Cmd+B"}`)
	a(GlobalID, "base", "P3", `{"type":"key","key":"Cmd+7"}`)
	a(GlobalID, "shift", "P1", `{"type":"key","key":"Cmd+Shift+Q"}`)
	return s
}

func TestModeBanksAreValidOnlyForPControls(t *testing.T) {
	s := modeFixture(t)
	for _, c := range []struct {
		id, bank, ctl string
		ok            bool
	}{
		{chrome, "pm-hue", "P1btn", true},
		{chrome, "pm-sat", "P8", true},
		{GlobalID, "pm-lum", "P4btn", true},
		{chrome, "pm-sat", "C1", false},
		{chrome, "pm-sat", "D1", false},
		{chrome, "pm-sat", "P9", false},
		{ResolveID, "col-pm-sat", "P1", true},
		{ResolveID, "edit-pm-lum", "Exposure", false},
		{chrome, "base", "C1", true},
		{chrome, "foo-pm-hue", "P1", false}, // not a bank an app can have
	} {
		if got := ValidBankControl(c.id, c.bank, c.ctl); got != c.ok {
			t.Errorf("ValidBankControl(%s,%s,%s)=%v want %v", c.id, c.bank, c.ctl, got, c.ok)
		}
	}
	err := s.Assign(chrome, "pm-sat", "C1", raw(t, `{"type":"key","key":"x"}`))
	if err == nil || !strings.Contains(err.Error(), "P1-P8") {
		t.Fatalf("a non-P control in a mode bank must be refused with the reason, got %v", err)
	}
}

func TestResolveForModeRungOrder(t *testing.T) {
	snap := modeFixture(t).Snapshot()
	key := func(pmode, ctl string, layer int, mod string) (string, string) {
		eb, ok := snap.ResolveForModeMod(chrome, mod, pmode, layer, ctl)
		if !ok {
			return "", ""
		}
		return eb.Binding.Key, eb.Bank
	}
	cases := []struct {
		name, pmode, ctl, mod string
		layer                 int
		key, bank             string
	}{
		{"hue has no bank: app base", "hue", "P1", "", 1, "Cmd+1", chrome},
		{"no mode: app base", "", "P1", "", 1, "Cmd+1", chrome},
		{"sat: app mode bank wins over base", "sat", "P1", "", 1, "Cmd+2", chrome + "-pm-sat"},
		{"lum", "lum", "P1", "", 1, "Cmd+3", chrome + "-pm-lum"},
		{"mode beats layer 2 of the app", "sat", "P1", "", 2, "Cmd+2", chrome + "-pm-sat"},
		{"held modifier beats the mode", "sat", "P1", "shift", 1, "Cmd+Shift+Q", "default-shift"},
		{"global mode beats global base", "sat", "P2", "", 1, "Cmd+G", "default-pm-sat"},
		{"global mode never beats the app's own base", "sat", "P1", "", 1, "Cmd+2", chrome + "-pm-sat"},
		{"mode set but nothing there: falls to base", "lum", "P2", "", 1, "Cmd+B", "default"},
		{"P control with no mode data at all", "sat", "P3", "", 1, "Cmd+7", "default"},
	}
	for _, c := range cases {
		k, b := key(c.pmode, c.ctl, c.layer, c.mod)
		if k != c.key || b != c.bank {
			t.Errorf("%s: got %q in %q, want %q in %q", c.name, k, b, c.key, c.bank)
		}
	}
	// A control that is not a P control ignores the mode completely.
	k1, _ := key("sat", "C1", 1, "")
	k2, _ := key("", "C1", 1, "")
	if k1 != k2 || k1 == "" {
		t.Errorf("C1 must not depend on the mode: %q vs %q", k1, k2)
	}
}

// Old presets (no mode data) resolve identically whatever the mode is.
func TestModeIsInvisibleWithoutModeData(t *testing.T) {
	s, _ := seededStore(t)
	snap := s.Snapshot()
	n := 0
	for _, p := range snap.Presets() {
		for _, mode := range append([]string{""}, config.PModes...) {
			for _, bank := range sortedKeys(p.Banks) {
				for _, ctl := range sortedKeys(p.Banks[bank]) {
					for layer := 1; layer <= 3; layer++ {
						for _, mod := range []string{"", "shift"} {
							a, aok := snap.ResolveForMod(p.ID, mod, layer, ctl)
							b, bok := snap.ResolveForModeMod(p.ID, mod, mode, layer, ctl)
							if aok != bok || a.Bank != b.Bank || a.Action != b.Action || a.Source != b.Source {
								t.Fatalf("%s %s mod=%q layer=%d mode=%q: %v/%s vs %v/%s", p.ID, ctl, mod, layer, mode, aok, a.Bank, bok, b.Bank)
							}
							n++
						}
					}
				}
			}
		}
	}
	for _, page := range []string{"col", "edit", "fusion", "color"} {
		for _, ctl := range []string{"P1", "P8", "P1btn", "P8btn", "Hue", "Exposure", "C1"} {
			for _, mode := range config.PModes {
				a, aok := snap.ResolveLegacy(page, 1, ctl)
				b, bok := snap.ResolveLegacyMode(page, mode, 1, ctl)
				if aok != bok || a.Bank != b.Bank || a.Action != b.Action {
					t.Fatalf("legacy %s %s %s: %v/%s vs %v/%s", page, ctl, mode, aok, a.Bank, bok, b.Bank)
				}
				n++
			}
		}
	}
	if n < 200 {
		t.Fatalf("only %d comparisons: fixture too small to mean anything", n)
	}
}

func TestModeBanksComposeToRuntimeNamesAndLoad(t *testing.T) {
	s := modeFixture(t)
	snap := s.Snapshot()
	raw, err := snap.ComposeBindingsJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := config.CheckBindings(raw); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"` + chrome + `-pm-sat"`, `"` + chrome + `-pm-lum"`, `"default-pm-sat"`} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("composed bindings lack bank %s", want)
		}
	}
	// split/runtime round trip
	for _, m := range config.PModes {
		rb := RuntimeBank(chrome, "pm-"+m)
		if app, bank := splitAppBank(rb); app != chrome || bank != "pm-"+m {
			t.Errorf("splitAppBank(%q) = %q,%q", rb, app, bank)
		}
	}
	if RuntimeBank(GlobalID, "pm-hue") != "default-pm-hue" || RuntimeBank(ResolveID, "col-pm-hue") != "col-pm-hue" {
		t.Fatal("runtime bank names wrong")
	}
}

func TestModeEditsAreUndoable(t *testing.T) {
	s := modeFixture(t)
	if _, ok := s.Snapshot().Effective(chrome, "pm-hue", "P4"); ok {
		t.Fatal("precondition")
	}
	if err := s.Assign(chrome, "pm-hue", "P4", raw(t, `{"type":"key","key":"Cmd+4"}`)); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Snapshot().Effective(chrome, "pm-hue", "P4"); !ok {
		t.Fatal("assignment missing")
	}
	if _, _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Snapshot().Effective(chrome, "pm-hue", "P4"); ok {
		t.Fatal("undo must remove the mode assignment")
	}
	if _, _, err := s.Redo(); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Snapshot().Effective(chrome, "pm-hue", "P4"); !ok {
		t.Fatal("redo must bring it back")
	}
}

func TestResolveStandardCarriesTheModeMappingButTheOwnersPresetDoesNot(t *testing.T) {
	// Owner's install: seeded from the live ld-resolve bindings -> NO per-mode slots.
	s, _ := seededStore(t)
	cur, _ := s.Snapshot().Preset(ResolveID)
	for _, bn := range StandardModeBankNames() {
		if _, has := cur.Banks[bn]; has {
			t.Fatalf("a Resolve preset seeded from live bindings must have NO per-mode slots, has %s", bn)
		}
	}
	// Defaults diff of a fresh store stays clean (the standard modes are opt-in, not drift).
	if d, err := s.DefaultsDiff(ResolveID); err != nil || d.Counts.Added+d.Counts.Changed+d.Counts.Removed != 0 {
		t.Fatalf("fresh diff: %v %+v", err, d)
	}
	// A Resolve preset built from the shipped default carries the standard mapping.
	dir := t.TempDir() + "/presets"
	b, err := Open(dir, Options{SeedFrom: "testdata/2t5-deck-plus-repo-bindings.json", LiveResolvePath: "-", PollInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	bp, _ := b.Snapshot().Preset(ResolveID)
	for _, bn := range []string{"col-pm-sat", "col-pm-lum"} {
		bm := bp.Banks[bn]
		if len(bm) != 8 {
			t.Fatalf("standard %s has %d controls, want P1-P8", bn, len(bm))
		}
		for i := 1; i <= 8; i++ {
			if a := bm["P"+string(rune('0'+i))]; a["type"] != "hueCurve" || a["curve"] != strings.TrimPrefix(bn, "col-pm-") {
				t.Errorf("%s P%d = %v", bn, i, a)
			}
		}
	}
	if _, ok := bp.Banks["col-pm-hue"]; ok {
		t.Fatal("Hue mode keeps today's Colour balance: no hue bank in the standard")
	}
	// Opting in on the owner's preset: one mode, one undo step, nothing else touched.
	nb := len(cur.Banks)
	banks, err := s.ApplyStandardModes("sat")
	if err != nil || len(banks) != 1 || banks[0] != "col-pm-sat" {
		t.Fatalf("apply sat: %v %v", banks, err)
	}
	after, _ := s.Snapshot().Preset(ResolveID)
	if len(after.Banks["col-pm-sat"]) != 8 || len(after.Banks) != nb+1 {
		t.Fatalf("banks %d->%d", nb, len(after.Banks))
	}
	if _, _, err := s.Undo(); err != nil {
		t.Fatal(err)
	}
	if again, _ := s.Snapshot().Preset(ResolveID); len(again.Banks) != nb {
		t.Fatal("undo must remove the standard mapping again")
	}
	if _, err := s.ApplyStandardModes("hue"); err == nil {
		t.Fatal("hue has no standard mapping")
	}
}
