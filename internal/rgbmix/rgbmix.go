// Package rgbmix converts a hue angle to a unit RGB vector using the standard
// color-balance decomposition. Each slider on the Loupedeck+ maps to a hue θ;
// the resulting (R,G,B) components tell the scrubber how much to move each
// value box in Resolve's Primaries panel.
//
// Formula (ISO/HSL balance math, θ from the red axis):
//
//	R(θ) = cos(θ)
//	G(θ) = cos(θ − 2π/3)
//	B(θ) = cos(θ − 4π/3)
//
// At θ=0° (red): R=+1, G=−½, B=−½.
// At θ=120° (green): R=−½, G=+1, B=−½.
// At θ=240° (blue): R=−½, G=−½, B=+1.
package rgbmix

import "math"

// RGBVector converts a hue angle (in degrees, measured from the red axis) to
// a signed unit RGB balance vector. The three components always sum to zero,
// ensuring luminance-neutral pushes.
//
// Examples:
//
//	RGBVector(0)   → (+1.000, −0.500, −0.500)   red
//	RGBVector(60)  → (+0.500, +0.500, −1.000)   yellow
//	RGBVector(120) → (−0.500, +1.000, −0.500)   green
//	RGBVector(180) → (−1.000, +0.500, +0.500)   cyan
//	RGBVector(240) → (−0.500, −0.500, +1.000)   blue
//	RGBVector(300) → (+0.500, −1.000, +0.500)   magenta
func RGBVector(hueDeg float64) (r, g, b float64) {
	θ := hueDeg * math.Pi / 180.0
	r = math.Cos(θ)
	g = math.Cos(θ - 2*math.Pi/3)
	b = math.Cos(θ - 4*math.Pi/3)
	return
}
