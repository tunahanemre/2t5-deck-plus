package preset

import "testing"

// A preset whose bytes differ from the backup only in non-effective ways
// (schema number, unknown top-level keys) reports "unchanged".
func TestDiffPresetUnchangedWhenOnlyFormattingDiffers(t *testing.T) {
	s, _ := seededStore(t)
	a := &Preset{Schema: 1, ID: "x.app", Name: "X", Packs: []string{}, Banks: map[string]map[string]Assignment{}, Settings: map[string]any{}}
	b := &Preset{Schema: 2, ID: "x.app", Name: "X", Packs: nil, Banks: nil, Settings: nil, Extra: map[string]any{"note": "hi"}}
	if f := s.diffPreset(a, b); f.Status != "unchanged" || f.Added+f.Removed+f.Changed != 0 {
		t.Fatalf("want unchanged, got %+v", f)
	}
	c := &Preset{Schema: 1, ID: "x.app", Name: "X", Settings: map[string]any{"k": "v"}}
	if f := s.diffPreset(a, c); f.Status != "changed" {
		t.Fatalf("settings differ: want changed, got %q", f.Status)
	}
	d := &Preset{Schema: 1, ID: "x.app", Name: "Renamed"}
	if f := s.diffPreset(a, d); f.Status != "changed" {
		t.Fatalf("name differs: want changed, got %q", f.Status)
	}
}
