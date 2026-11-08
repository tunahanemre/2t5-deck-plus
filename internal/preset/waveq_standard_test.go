package preset

import "testing"

// Standard app presets (2026-11-04): shipped as embedded read-only templates,
// offered when an app is added, customised in the app's own preset file, and
// comparable with / restorable to the standard.

var standardApps = map[string]string{
	"adobe-lightroom-classic": "com.adobe.LightroomClassicCC7",
	"adobe-lightroom":         "com.adobe.lightroomCC",
	"adobe-photoshop":         "com.adobe.Photoshop",
	"adobe-premiere-pro":      "com.adobe.PremierePro.26",
	"adobe-after-effects":     "com.adobe.AfterEffects",
	"adobe-illustrator":       "com.adobe.illustrator",
}

func TestStandardAppTemplatesPresent(t *testing.T) {
	for id, bundle := range standardApps {
		tp := templateByID(t, id)
		if tp.BundleID != bundle || tp.Controls < 25 || tp.Requires == nil {
			t.Errorf("%s: %+v", id, tp)
		}
	}
}

// Anything that removes material is held: Lift and Extract in Premiere, Reset
// all in Lightroom Classic.
func TestStandardAppHoldToFire(t *testing.T) {
	want := map[string]map[string]float64{
		"adobe-premiere-pro":      {"C3": 500, "C4": 500},
		"adobe-lightroom-classic": {"C3": 800},
	}
	for id, ctl := range want {
		s := deckStore(t)
		p, _, err := s.AddFromTemplate(id)
		if err != nil {
			t.Fatal(err)
		}
		for c, ms := range ctl {
			got, _ := p.Banks["base"][c]["holdMs"].(float64)
			if n, ok := p.Banks["base"][c]["holdMs"].(interface{ Float64() (float64, error) }); ok {
				got, _ = n.Float64()
			}
			if got != ms {
				t.Errorf("%s %s holdMs = %v, want %v", id, c, p.Banks["base"][c]["holdMs"], ms)
			}
		}
	}
}

// The user's edits never touch the standard: the diff shows them against the
// template, Restore defaults brings the template back, and a fresh app still
// starts from the unchanged template.
func TestStandardPresetCompareAndRestore(t *testing.T) {
	s := deckStore(t)
	p, tp, err := s.AddFromTemplate("adobe-photoshop")
	if err != nil {
		t.Fatal(err)
	}
	d, err := s.DefaultsDiff(p.ID)
	if err != nil || d.SeedSource != "template" || d.TemplateID != tp.ID || d.Counts.Changed+d.Counts.Added+d.Counts.Removed != 0 || d.Counts.Unchanged != tp.Controls {
		t.Fatalf("fresh diff %+v err %v", d, err)
	}
	if err := s.Assign(p.ID, "base", "D1", map[string]any{"type": "keyDelta", "keyCW": "Right", "keyCCW": "Left", "detentThreshold": 1, "label": "mine"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Unassign(p.ID, "base", "C6"); err != nil {
		t.Fatal(err)
	}
	d, _ = s.DefaultsDiff(p.ID)
	if d.Counts.Changed != 1 || d.Counts.Removed != 1 {
		t.Fatalf("edited diff %+v", d.Counts)
	}
	if _, err := s.RestoreDefaultsInfo(p.ID, "app", "", ""); err != nil {
		t.Fatal(err)
	}
	d, _ = s.DefaultsDiff(p.ID)
	if d.Counts.Changed+d.Counts.Added+d.Counts.Removed != 0 {
		t.Fatalf("after restore %+v", d.Counts)
	}
	s2 := deckStore(t)
	p2, _, _ := s2.AddFromTemplate("adobe-photoshop")
	if p2.Banks["base"]["D1"]["keyCW"] != "]" {
		t.Errorf("template changed: %v", p2.Banks["base"]["D1"])
	}
}
