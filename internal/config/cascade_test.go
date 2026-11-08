package config

import "testing"

func cascadeTestCfg() *Config {
	return &Config{bindings: map[string]map[string]Binding{
		"col":    {"A": {Type: "key", Key: "Cmd+1"}, "B": {Type: "key", Key: "Cmd+2"}},
		"col-2":  {"A": {Type: "key", Key: "Cmd+9"}},
		"edit":   {"A": {Type: "property", Property: "Pan"}, "C": {Type: "click", Point: "p"}},
		"edit-2": {"C": {Type: "property", Property: "Tilt"}},
	}}
}

func TestLayerBank(t *testing.T) {
	for _, tc := range []struct {
		page string
		n    int
		want string
	}{
		{"col", 1, "col"}, {"col", 0, "col"}, {"col", 2, "col-2"},
		{"edit", 3, "edit-3"}, {"edit", 1, "edit"},
	} {
		if got := LayerBank(tc.page, tc.n); got != tc.want {
			t.Errorf("LayerBank(%q,%d) = %q, want %q", tc.page, tc.n, got, tc.want)
		}
	}
}

// TestCascadeLayerOneIsTwoRung is the primary regression guard: col-2 defines
// A, but at layer 1 it must never be consulted.
func TestCascadeLayerOneIsTwoRung(t *testing.T) {
	b, bank, ok := cascadeTestCfg().Cascade("col", 1, "A", nil)
	if !ok || bank != "col" || b.Key != "Cmd+1" {
		t.Errorf("layer 1 got bank=%q key=%q ok=%v, want col/Cmd+1/true", bank, b.Key, ok)
	}
}

func TestCascadeLayerTwoPrefersLayerBank(t *testing.T) {
	b, bank, ok := cascadeTestCfg().Cascade("col", 2, "A", nil)
	if !ok || bank != "col-2" || b.Key != "Cmd+9" {
		t.Errorf("got bank=%q key=%q ok=%v, want col-2/Cmd+9/true", bank, b.Key, ok)
	}
}

// TestCascadeFallsBackToLayerOne is the inheritance contract: anything not
// redefined in the active layer keeps doing its layer-1 job.
func TestCascadeFallsBackToLayerOne(t *testing.T) {
	b, bank, ok := cascadeTestCfg().Cascade("col", 2, "B", nil)
	if !ok || bank != "col" || b.Key != "Cmd+2" {
		t.Errorf("got bank=%q key=%q ok=%v, want col/Cmd+2/true", bank, b.Key, ok)
	}
}

func TestCascadeEditPageFallsThroughToCol(t *testing.T) {
	_, bank, ok := cascadeTestCfg().Cascade("edit", 1, "B", nil)
	if !ok || bank != "col" {
		t.Errorf("got bank=%q ok=%v, want col/true", bank, ok)
	}
}

func TestCascadeGuardAppliesToPageBankOnly(t *testing.T) {
	c := cascadeTestCfg()
	onlyProperty := func(b Binding) bool { return b.IsProperty() }

	// edit.C is a "click" — rejected by the guard, and col has no C at all.
	if _, _, ok := c.Cascade("edit", 1, "C", onlyProperty); ok {
		t.Error("guard should have rejected edit.C (click) with no col fallback")
	}
	// edit-2.C is a property — accepted at layer 2.
	b, bank, ok := c.Cascade("edit", 2, "C", onlyProperty)
	if !ok || bank != "edit-2" || b.Property != "Tilt" {
		t.Errorf("got bank=%q prop=%q ok=%v, want edit-2/Tilt/true", bank, b.Property, ok)
	}
}

// TestCascadeGuardNeverAppliesToColBank documents that the whitelist is a
// legacy artifact of the col/edit split: col holds buttons with no edit
// counterpart, so guarding it would silently drop them.
func TestCascadeGuardNeverAppliesToColBank(t *testing.T) {
	c := cascadeTestCfg()
	rejectEverything := func(Binding) bool { return false }
	_, bank, ok := c.Cascade("col", 1, "A", rejectEverything)
	if !ok || bank != "col" {
		t.Errorf("col bank must ignore the guard: bank=%q ok=%v", bank, ok)
	}
}

func TestCascadeMiss(t *testing.T) {
	if _, _, ok := cascadeTestCfg().Cascade("col", 3, "ZZZ", nil); ok {
		t.Error("unknown control must miss")
	}
}
