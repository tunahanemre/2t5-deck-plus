package actions

import "testing"

func TestHueCurveActionValidates(t *testing.T) {
	r := Default()
	a, err := r.Normalize(map[string]any{"type": "hueCurve", "curve": "sat", "hue": 2.0, "sens": 6.0})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Validate(a, true); err != nil {
		t.Fatal(err)
	}
	if got := r.Infer(a).ID; got != "resolve.hueCurve" {
		t.Fatalf("Infer = %q", got)
	}
	bad, err := r.Normalize(map[string]any{"type": "hueCurve", "curve": "hue", "hue": 2.0})
	if err == nil {
		if err = r.Validate(bad, true); err == nil {
			t.Fatal("curve must be sat or lum")
		}
	}
	bad, err = r.Normalize(map[string]any{"type": "hueCurve", "curve": "lum", "hue": 8.0})
	if err == nil {
		if err = r.Validate(bad, true); err == nil {
			t.Fatal("hue must be 0-7")
		}
	}
}
