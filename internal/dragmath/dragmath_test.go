package dragmath

import (
	"math"
	"testing"
)

// approxEq returns true when a and b differ by less than eps.
func approxEq(a, b, eps float64) bool {
	return math.Abs(a-b) < eps
}

func TestClamp(t *testing.T) {
	// step×sens=150 with max 40 → capped to 40
	if got := Clamp(150, 40); got != 40 {
		t.Errorf("Clamp(150, 40) = %v, want 40", got)
	}
	// negative preserved → -40
	if got := Clamp(-150, 40); got != -40 {
		t.Errorf("Clamp(-150, 40) = %v, want -40", got)
	}
	// small value unchanged
	if got := Clamp(10, 40); got != 10 {
		t.Errorf("Clamp(10, 40) = %v, want 10", got)
	}
	// negative small unchanged
	if got := Clamp(-10, 40); got != -10 {
		t.Errorf("Clamp(-10, 40) = %v, want -10", got)
	}
	// exact boundary
	if got := Clamp(40, 40); got != 40 {
		t.Errorf("Clamp(40, 40) = %v, want 40", got)
	}
	// maxPx=0 means no cap
	if got := Clamp(999, 0); got != 999 {
		t.Errorf("Clamp(999, 0) = %v, want 999 (no cap)", got)
	}
	// maxPx<0 means no cap
	if got := Clamp(999, -1); got != 999 {
		t.Errorf("Clamp(999, -1) = %v, want 999 (no cap)", got)
	}
}

func TestEndpoint(t *testing.T) {
	// start (900,400), axis v, sensitivity 3 px/step, step +5 -> drag UP 15px -> y=385
	x, y := Endpoint(900, 400, "v", 5, 3)
	if x != 900 || y != 385 {
		t.Errorf("got %v,%v want 900,385", x, y)
	}
	// step -4 on v -> down 12 -> y=412
	if _, y := Endpoint(900, 400, "v", -4, 3); y != 412 {
		t.Errorf("y=%v want 412", y)
	}
	// axis h: +2 sens 3 -> x+6
	if x, _ := Endpoint(900, 400, "h", 2, 3); x != 906 {
		t.Errorf("x=%v want 906", x)
	}
}
