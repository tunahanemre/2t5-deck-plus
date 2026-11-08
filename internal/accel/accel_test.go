package accel

import (
	"math"
	"testing"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func TestAmount(t *testing.T) {
	// base=0.002 per step, gamma=1.5
	a := New(0.002, 1.5)
	if !approx(a.Amount(0), 0.0) {
		t.Fatalf("zero step must be zero, got %v", a.Amount(0))
	}
	// magnitude 1: 0.002 * 1^1.5 = 0.002
	if !approx(a.Amount(1), 0.002) {
		t.Errorf("step 1 = %v want 0.002", a.Amount(1))
	}
	// magnitude 4: 0.002 * 4^1.5 = 0.016 ; sign preserved
	if !approx(a.Amount(-4), -0.016) {
		t.Errorf("step -4 = %v want -0.016", a.Amount(-4))
	}
}
