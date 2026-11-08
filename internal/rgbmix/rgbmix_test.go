package rgbmix

import (
	"math"
	"testing"
)

const eps = 1e-6

func approxEq(a, b float64) bool {
	return math.Abs(a-b) < eps
}

// TestRGBVector_CardinalHues verifies the exact RGB components at the six
// cardinal hue angles (multiples of 60°).
func TestRGBVector_CardinalHues(t *testing.T) {
	cases := []struct {
		hue     float64
		wantR   float64
		wantG   float64
		wantB   float64
		comment string
	}{
		{0, 1.0, -0.5, -0.5, "red"},
		{60, 0.5, 0.5, -1.0, "yellow"},
		{120, -0.5, 1.0, -0.5, "green"},
		{180, -1.0, 0.5, 0.5, "cyan"},
		{240, -0.5, -0.5, 1.0, "blue"},
		{300, 0.5, -1.0, 0.5, "magenta"},
	}
	for _, tc := range cases {
		r, g, b := RGBVector(tc.hue)
		if !approxEq(r, tc.wantR) || !approxEq(g, tc.wantG) || !approxEq(b, tc.wantB) {
			t.Errorf("RGBVector(%.0f°) [%s] = (%.4f, %.4f, %.4f), want (%.4f, %.4f, %.4f)",
				tc.hue, tc.comment, r, g, b, tc.wantR, tc.wantG, tc.wantB)
		}
	}
}

// TestRGBVector_SumToZero verifies that R+G+B = 0 (luminance-neutral) for
// a representative sample of hue angles.
func TestRGBVector_SumToZero(t *testing.T) {
	for deg := 0.0; deg < 360.0; deg += 15.0 {
		r, g, b := RGBVector(deg)
		sum := r + g + b
		if math.Abs(sum) > eps {
			t.Errorf("RGBVector(%.0f°): R+G+B = %.8f, want ~0", deg, sum)
		}
	}
}

// TestRGBVector_UnitMagnitudeMax verifies that no component exceeds ±1.
func TestRGBVector_UnitMagnitudeMax(t *testing.T) {
	for deg := 0.0; deg < 360.0; deg += 5.0 {
		r, g, b := RGBVector(deg)
		for _, v := range []float64{r, g, b} {
			if v < -1.0-eps || v > 1.0+eps {
				t.Errorf("RGBVector(%.0f°) component %.4f out of [-1, 1]", deg, v)
			}
		}
	}
}

// TestRGBVector_Periodicity verifies that the function is periodic at 360°.
func TestRGBVector_Periodicity(t *testing.T) {
	for deg := 0.0; deg < 360.0; deg += 30.0 {
		r1, g1, b1 := RGBVector(deg)
		r2, g2, b2 := RGBVector(deg + 360.0)
		if !approxEq(r1, r2) || !approxEq(g1, g2) || !approxEq(b1, b2) {
			t.Errorf("RGBVector(%.0f°) != RGBVector(%.0f°): (%.4f,%.4f,%.4f) vs (%.4f,%.4f,%.4f)",
				deg, deg+360, r1, g1, b1, r2, g2, b2)
		}
	}
}
