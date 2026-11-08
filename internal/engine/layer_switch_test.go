package engine

import (
	"testing"

	"2t5-deck-plus/internal/config"
)

// loadRealConfig loads the SHIPPED config/device-map.json + config/bindings.json,
// so tests can pin the real console layout rather than a fixture.
func loadRealConfig(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := config.Load("../../config/device-map.json", "../../config/bindings.json")
	if err != nil {
		t.Fatalf("load real config: %v", err)
	}
	return cfg
}

func TestLayerSwitchResolves(t *testing.T) {
	cfg := loadConfig(t, `{
	  "col": {"L2": {"type":"layer","layer":2}}
	}`)
	b, ok := resolveLayerSwitch(cfg, "color", "L2")
	if !ok || !b.IsLayer() || b.Layer != 2 {
		t.Errorf("got IsLayer=%v Layer=%d ok=%v, want true/2/true", b.IsLayer(), b.Layer, ok)
	}
}

// TestLayerSwitchCannotBeShadowed is the escape hatch guard: if a layer bank
// could redefine L1/L2/L3 there would be no way back out of that layer.
func TestLayerSwitchCannotBeShadowed(t *testing.T) {
	cfg := loadConfig(t, `{
	  "col":   {"L1": {"type":"layer","layer":1}},
	  "col-2": {"L1": {"type":"key","key":"Cmd+Q"}}
	}`)
	b, ok := resolveLayerSwitch(cfg, "color", "L1")
	if !ok || !b.IsLayer() || b.Layer != 1 {
		t.Errorf("a layer bank must not be able to steal a layer switch: IsLayer=%v Layer=%d ok=%v",
			b.IsLayer(), b.Layer, ok)
	}
}

func TestLayerSwitchMissForNonLayerControl(t *testing.T) {
	cfg := loadConfig(t, `{"col": {"Undo": {"type":"key","key":"Cmd+Z"}}}`)
	if _, ok := resolveLayerSwitch(cfg, "color", "Undo"); ok {
		t.Error("a non-layer binding must not resolve as a layer switch")
	}
}

// TestLayerSwitchHonoursPageBank lets the user put a layer switch in a page
// bank if they want, while still refusing layer banks.
func TestLayerSwitchHonoursPageBank(t *testing.T) {
	cfg := loadConfig(t, `{
	  "col":  {"C4": {"type":"key","key":"Cmd+Z"}},
	  "edit": {"C4": {"type":"layer","layer":3}}
	}`)
	b, ok := resolveLayerSwitch(cfg, "edit", "C4")
	if !ok || b.Layer != 3 {
		t.Errorf("page-bank layer switch should resolve: Layer=%d ok=%v", b.Layer, ok)
	}
	// ...but not on a page that does not define it.
	if _, ok := resolveLayerSwitch(cfg, "color", "C4"); ok {
		t.Error("col.C4 is a key, not a layer switch — must miss on the color page")
	}
}

// TestRealConfigLayerSwitchesResolve pins the shipped bindings.json: L1/L2/L3
// must actually be reachable as layer switches.
func TestRealConfigLayerSwitchesResolve(t *testing.T) {
	cfg := loadRealConfig(t)
	for control, want := range map[string]int{"L1": 1, "L2": 2, "L3": 3} {
		b, ok := resolveLayerSwitch(cfg, "color", control)
		if !ok || b.Layer != want {
			t.Errorf("%s: got Layer=%d ok=%v, want %d/true", control, b.Layer, ok, want)
		}
	}
}

// TestRealConfigCmdChordResolves pins the shipped bindings.json's ONE
// required Part B chord (2026-08-12 REV 2 design review): col.Cmd is a
// modifierHold button naming overlay "cmd", and col-cmd.C2 is the Cmd+S
// save-project chord it unlocks -- pinned both through the real config's
// col bank (the Cmd button itself) and through resolveOverlayBinding (the
// actual chord resolution path Start() uses).
func TestRealConfigCmdChordResolves(t *testing.T) {
	cfg := loadRealConfig(t)

	cmdBtn, ok := cfg.Binding("col", "Cmd")
	if !ok || !cmdBtn.IsModifierHold() || cmdBtn.Modifier != "cmd" {
		t.Fatalf("col.Cmd = %+v ok=%v, want a modifierHold binding with modifier=cmd", cmdBtn, ok)
	}

	for _, page := range []string{"edit", "color", "fusion", "cut", "deliver", "fairlight", "media", ""} {
		b, bank, ok := resolveOverlayBinding(cfg, page, "cmd", "C2")
		if !ok || bank != "col-cmd" || !b.IsKey() || b.Key != "Cmd+S" {
			t.Errorf("page=%q: resolveOverlayBinding(cfg, page, \"cmd\", \"C2\") = bank=%q key=%q ok=%v, want col-cmd/Cmd+S/true",
				page, bank, b.Key, ok)
		}
	}

	// C2 alone (no chord held) must keep its normal per-bank binding,
	// completely unrelated to the chord's key/type.
	if b, ok := cfg.Binding("edit", "C2"); !ok || !b.IsColorByRenderState() {
		t.Errorf("edit.C2 (no chord) = %+v ok=%v, want unaffected colorByRenderState", b, ok)
	}
}
