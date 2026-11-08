package mousecal

import (
	"encoding/hex"
	"image"
	"math"
)

// A probe is a coarse picture of the screen around a calibrated point: an 8x8
// grid of average brightness, hex encoded (64 bytes). It is not an image
// match: it exists to notice that a panel moved, hid or resized under the
// pointer. It tolerates changing digits and a hover highlight by being coarse
// and by being compared with a generous threshold.
const probeGrid = 8

// ProbeSize is the side (display points) of the square the probe covers.
const ProbeSize = 64

// CellDiff is how different (0..1 of full brightness) one grid cell must be to
// count as changed.
const CellDiff = 0.2

// ProbeThreshold is the share of changed cells (0..1) above which two probes
// are taken as different pictures. A mean over the patch would not do: the
// control itself is a small part of it, so a panel that moved away leaves the
// mean almost unchanged; counting changed cells does notice. Changing digits or
// a hover highlight touch only a cell or two.
const ProbeThreshold = 0.08

// ProbeOf reduces an image to its probe. Empty on a nil or tiny image.
func ProbeOf(img image.Image) string {
	if img == nil {
		return ""
	}
	b := img.Bounds()
	if b.Dx() < probeGrid || b.Dy() < probeGrid {
		return ""
	}
	out := make([]byte, probeGrid*probeGrid)
	for gy := 0; gy < probeGrid; gy++ {
		for gx := 0; gx < probeGrid; gx++ {
			x0, x1 := b.Min.X+gx*b.Dx()/probeGrid, b.Min.X+(gx+1)*b.Dx()/probeGrid
			y0, y1 := b.Min.Y+gy*b.Dy()/probeGrid, b.Min.Y+(gy+1)*b.Dy()/probeGrid
			var sum, n float64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					r, g, bl, _ := img.At(x, y).RGBA()
					sum += (0.2126*float64(r) + 0.7152*float64(g) + 0.0722*float64(bl)) / 65535 * 255
					n++
				}
			}
			if n > 0 {
				out[gy*probeGrid+gx] = byte(math.Round(sum / n))
			}
		}
	}
	return hex.EncodeToString(out)
}

// ProbeDistance is the share (0..1) of grid cells whose brightness differs by
// more than CellDiff. ok is false when either probe is missing or malformed.
func ProbeDistance(a, b string) (d float64, ok bool) {
	ba, err1 := hex.DecodeString(a)
	bb, err2 := hex.DecodeString(b)
	if err1 != nil || err2 != nil || len(ba) != probeGrid*probeGrid || len(bb) != len(ba) {
		return 0, false
	}
	n := 0
	for i := range ba {
		if math.Abs(float64(ba[i])-float64(bb[i]))/255 > CellDiff {
			n++
		}
	}
	return float64(n) / float64(len(ba)), true
}
