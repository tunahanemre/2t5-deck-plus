package engine

import (
	"reflect"
	"testing"

	"2t5-deck-plus/internal/config"
)

// legacyResolveNoteBank is a verbatim copy of the pre-layer resolveNoteBank,
// kept here as the ORACLE for the layer-1 equivalence test below. Do not
// "simplify" it to call the new code — the whole point is that it is an
// independent restatement of the old behaviour.
func legacyResolveNoteBank(cfg *config.Config, page string, colBank string, control string) string {
	if page == "edit" {
		if eb, ok := cfg.Binding("edit", control); ok && (eb.IsProperty() || eb.IsJog() || eb.IsKey()) {
			return "edit"
		}
	}
	return colBank
}

// TestResolveNoteBindingLayerOneMatchesLegacy is the primary regression guard
// for the whole layer system: at layer 1, resolution must be byte-identical to
// what shipped before layers existed — asserted, not assumed.
//
// It runs against the REAL config/bindings.json rather than a fixture, so it
// covers every control the user actually has bound.
func TestResolveNoteBindingLayerOneMatchesLegacy(t *testing.T) {
	cfg, err := config.Load("../../config/device-map.json", "../../config/bindings.json")
	if err != nil {
		t.Fatalf("load real config: %v", err)
	}

	// Every control defined anywhere in the real config, across every page
	// the engine can report.
	controls := map[string]bool{}
	for bank, m := range cfg.AllBindings() {
		if bank == "_config" {
			continue
		}
		for control := range m {
			controls[control] = true
		}
	}
	if len(controls) < 20 {
		t.Fatalf("expected the real config to define many controls, got %d", len(controls))
	}

	// Deliberate, enumerated departures from pre-layer behaviour. The oracle
	// below stays a VERBATIM copy of the shipped-before-layers resolver — it
	// is worthless as a guard the moment it is edited to agree with whatever
	// the code now does — so an intended change is recorded here instead.
	//
	// edit/C2: colorByRenderState was added to noteBankGuard's whitelist so
	// Edit C2 paints the timeline. Before, a page bank could only shadow col
	// with a property, jog or key binding, so this one was rejected and C2
	// fell through to col's Alt+P.
	intentional := map[string]string{
		"edit/C2": "colorByRenderState joined the noteBankGuard whitelist",
	}

	pages := []string{"color", "edit", "fusion", "fairlight", "cut", "deliver", "media", ""}
	checked, diverged := 0, 0
	for _, page := range pages {
		for control := range controls {
			if why, ok := intentional[page+"/"+control]; ok {
				diverged++
				t.Logf("page=%q control=%q: intended divergence (%s)", page, control, why)
				continue
			}
			gotB, gotBank, gotOK := resolveNoteBinding(cfg, page, 1, control)

			wantBank := legacyResolveNoteBank(cfg, page, "col", control)
			wantB, wantOK := cfg.Binding(wantBank, control)

			if gotOK != wantOK {
				t.Errorf("page=%q control=%q: ok = %v, want %v", page, control, gotOK, wantOK)
				continue
			}
			if !gotOK {
				continue
			}
			if gotBank != wantBank {
				t.Errorf("page=%q control=%q: bank = %q, want %q", page, control, gotBank, wantBank)
			}
			// Binding contains a []string (Gang), so it is not comparable
			// with != and needs DeepEqual.
			if !reflect.DeepEqual(gotB, wantB) {
				t.Errorf("page=%q control=%q: binding differs\n got %+v\nwant %+v", page, control, gotB, wantB)
			}
			checked++
		}
	}
	if checked == 0 {
		t.Fatal("equivalence test checked nothing — the oracle is not wired up")
	}
	// A divergence that never fires is a stale exemption quietly widening the
	// hole it was granted for.
	if diverged != len(intentional) {
		t.Errorf("%d intended divergences declared but %d encountered — stale exemption?",
			len(intentional), diverged)
	}
	t.Logf("layer-1 equivalence verified across %d page/control resolutions", checked)
}

// TestEditPaintButtonResolvesToTheEditBank pins the divergence declared above
// from the other side: the exemption exists so this resolution happens, so
// assert it rather than merely excusing it. Regression guard for the miss that
// shipped -- the binding was correct, noteBankGuard rejected it, and the
// button went on doing col's older action.
func TestEditPaintButtonResolvesToTheEditBank(t *testing.T) {
	cfg, err := config.Load("../../config/device-map.json", "../../config/bindings.json")
	if err != nil {
		t.Fatalf("load real config: %v", err)
	}
	b, bank, ok := resolveNoteBinding(cfg, "edit", 1, "C2")
	if !ok || bank != "edit" {
		t.Fatalf("Edit C2 resolved to bank %q ok=%v, want edit", bank, ok)
	}
	if !b.IsColorByRenderState() {
		t.Errorf("Edit C2 = %+v, want a colorByRenderState binding", b)
	}
}

// TestC6IsPlainHudToggleEverywhere: C6 is the HUD button and nothing else.
// The paint used to ride along on it; moving the paint off must leave C6
// resolving to col's plain toggle on every page, including Edit.
func TestC6IsPlainHudToggleEverywhere(t *testing.T) {
	cfg, err := config.Load("../../config/device-map.json", "../../config/bindings.json")
	if err != nil {
		t.Fatalf("load real config: %v", err)
	}
	for _, page := range []string{"edit", "color", "fusion", ""} {
		b, bank, ok := resolveNoteBinding(cfg, page, 1, "C6")
		if !ok || bank != "col" || !b.IsToggleHud() {
			t.Errorf("page=%q: C6 resolved to bank=%q toggleHud=%v ok=%v, want col/true/true",
				page, bank, b.IsToggleHud(), ok)
		}
		if b.IsToggleHudAndColor() || b.IsColorByRenderState() {
			t.Errorf("page=%q: C6 must not paint anything", page)
		}
	}
}

func TestResolveNoteBindingPrefersLayerBank(t *testing.T) {
	cfg := loadConfig(t, `{
	  "col":   {"C1": {"type":"key","key":"Alt+S"}},
	  "col-2": {"C1": {"type":"key","key":"Alt+X"}}
	}`)
	b, bank, ok := resolveNoteBinding(cfg, "color", 2, "C1")
	if !ok || bank != "col-2" || b.Key != "Alt+X" {
		t.Errorf("got bank=%q key=%q ok=%v, want col-2/Alt+X/true", bank, b.Key, ok)
	}
}

func TestResolveNoteBindingInheritsFromLayerOne(t *testing.T) {
	cfg := loadConfig(t, `{
	  "col":   {"Undo": {"type":"key","key":"Cmd+Z"}},
	  "col-2": {"C1":   {"type":"key","key":"Alt+X"}}
	}`)
	b, bank, ok := resolveNoteBinding(cfg, "color", 2, "Undo")
	if !ok || bank != "col" || b.Key != "Cmd+Z" {
		t.Errorf("got bank=%q key=%q ok=%v, want col/Cmd+Z/true — layer 2 must inherit", bank, b.Key, ok)
	}
}

// TestResolveTurnBindingHasNoGuard pins the asymmetry between press and turn
// resolution: turns have never had the property/jog/key whitelist.
func TestResolveTurnBindingHasNoGuard(t *testing.T) {
	cfg := loadConfig(t, `{
	  "col":  {"K": {"type":"cdl","param":"sat"}},
	  "edit": {"K": {"type":"click","point":"p"}}
	}`)
	_, bank, ok := resolveTurnBinding(cfg, "edit", 1, "K", "")
	if !ok || bank != "edit" {
		t.Errorf("got bank=%q ok=%v, want edit/true — turn resolution has no whitelist", bank, ok)
	}
}

// TestJogStreakSeesCurrentLayer guards the specific bug the review board
// flagged: the drain loop computes its bank ONCE per tick and reuses it at
// three sites, so a jog-streak pre-scan resolving against a stale layer would
// lag the main dispatch by a tick.
func TestJogStreakSeesCurrentLayer(t *testing.T) {
	cfg := loadConfig(t, `{
	  "edit":   {"ControlDial": {"type":"jog","base":1.0}},
	  "edit-2": {"ControlDial": {"type":"property","property":"Opacity"}}
	}`)
	b, _, ok := resolveTurnBinding(cfg, "edit", 2, "ControlDial", "")
	if !ok || !b.IsProperty() || b.IsJog() {
		t.Errorf("at layer 2 ControlDial must resolve as property, got IsJog=%v IsProperty=%v ok=%v",
			b.IsJog(), b.IsProperty(), ok)
	}
	b1, _, _ := resolveTurnBinding(cfg, "edit", 1, "ControlDial", "")
	if !b1.IsJog() {
		t.Error("at layer 1 ControlDial must still be a jog")
	}
}

// TestResolveNoteBindingGuardStillApplies pins the property/jog/key whitelist:
// a page-bank binding of some other type must NOT shadow col.
func TestResolveNoteBindingGuardStillApplies(t *testing.T) {
	cfg := loadConfig(t, `{
	  "col":  {"X": {"type":"key","key":"Cmd+1"}},
	  "edit": {"X": {"type":"click","point":"somewhere"}}
	}`)
	_, bank, ok := resolveNoteBinding(cfg, "edit", 1, "X")
	if !ok || bank != "col" {
		t.Errorf("got bank=%q ok=%v, want col/true", bank, ok)
	}
}
