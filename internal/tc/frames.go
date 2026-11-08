package tc

import "fmt"

// FramesToTC renders a frame count as HH:MM:SS:FF at the given rate.
//
// Non-drop only: every timeline this tool touches is integer-fps (the sidecar
// refuses drop-frame outright), so the simple arithmetic is correct here and a
// drop-frame path would be untested dead code.
//
// Returns "--:--:--:--" for a non-positive rate rather than dividing by zero,
// so a missing fps degrades to a visible placeholder instead of a panic.
func FramesToTC(frames int, fps float64) string {
	if fps <= 0 || frames < 0 {
		return "--:--:--:--"
	}
	r := int(fps + 0.5)
	if r <= 0 {
		return "--:--:--:--"
	}
	f := frames % r
	total := frames / r
	return fmt.Sprintf("%02d:%02d:%02d:%02d", total/3600, (total/60)%60, total%60, f)
}
