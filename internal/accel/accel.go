package accel

import "math"

type Curve struct {
	base  float64 // amount per unit step
	gamma float64 // >1 accelerates with magnitude
}

func New(base, gamma float64) Curve { return Curve{base: base, gamma: gamma} }

// Amount converts an aggregated signed step into a signed CDL amount.
func (c Curve) Amount(step int) float64 {
	if step == 0 {
		return 0
	}
	mag := math.Abs(float64(step))
	out := c.base * math.Pow(mag, c.gamma)
	if step < 0 {
		return -out
	}
	return out
}
