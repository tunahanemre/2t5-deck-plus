package mousecal

import (
	"encoding/json"
	"strings"
	"testing"

	"2t5-deck-plus/internal/preset"
)

func TestHueCurveTargetsAreDerivedAndDescribed(t *testing.T) {
	p := &preset.Preset{ID: preset.ResolveID, Banks: map[string]map[string]preset.Assignment{
		"col-pm-sat": {"P1": {"type": "hueCurve", "curve": "sat", "hue": float64(0)}, "P6": {"type": "hueCurve", "curve": "sat", "hue": json.Number("5")}},
		"col-pm-lum": {"P3": {"type": "hueCurve", "curve": "lum", "hue": float64(2)}, "P4": {"type": "hueCurve", "curve": "bogus", "hue": float64(2)}},
	}}
	got := map[string]Target{}
	for _, tg := range DeriveTargets(p) {
		got[tg.Key] = tg
	}
	for _, k := range []string{"hvs_red", "hvs_blue", "hvl_yellow"} {
		tg, ok := got[k]
		if !ok {
			t.Fatalf("target %s missing: %v", k, got)
		}
		if tg.Kind != KindDragV || tg.Axis != "v" || !strings.Contains(tg.Instruction, "Curves") {
			t.Errorf("%s: %+v", k, tg)
		}
	}
	if len(got) != 3 {
		t.Fatalf("a bad curve must not create a target; got %v", got)
	}
}
