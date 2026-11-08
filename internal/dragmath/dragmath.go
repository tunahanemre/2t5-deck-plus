package dragmath

// Clamp limits the absolute value of delta to maxPx.
// If maxPx <= 0 the delta is returned unchanged (no cap applied).
// The sign of delta is preserved.
//
//	Clamp(150, 40)  => 40
//	Clamp(-150, 40) => -40
//	Clamp(10, 40)   => 10
func Clamp(delta, maxPx float64) float64 {
	if maxPx <= 0 {
		return delta
	}
	if delta > maxPx {
		return maxPx
	}
	if delta < -maxPx {
		return -maxPx
	}
	return delta
}

// Point is a 2D coordinate.
type Point struct {
	X, Y float64
}

// Endpoint computes the drag destination from a start point.
//
// Parameters:
//   - x, y: start position (global display coordinates, top-left origin)
//   - axis: "v" (vertical drag) or "h" (horizontal drag)
//   - step: accumulated step count (positive = brighter/right, negative = darker/left)
//   - sens: pixels per step (sensitivity)
//
// For axis "v": positive step means dragging UP (smaller y) which maps to
// a brighter/higher value in Resolve's color wheels.
// For axis "h": positive step means dragging RIGHT (larger x).
func Endpoint(x, y float64, axis string, step int, sens float64) (float64, float64) {
	d := float64(step) * sens
	if axis == "h" {
		return x + d, y
	}
	// v: positive step => up (brighter) => smaller y (screen Y grows downward)
	return x, y - d
}
