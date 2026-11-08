package midi

// RelStep decodes a Loupedeck+ relative-CC value into a signed step.
// The device uses 7-bit 2's-complement relative encoding:
//
//	0        => 0 (stopped / no movement)
//	1..63    => +value (clockwise)
//	64       => 0 (center / no movement)
//	65..127  => value-128 (counter-clockwise; 127 => -1, 65 => -63)
func RelStep(value int) int {
	if value == 0 || value == 64 {
		return 0
	}
	if value < 64 {
		return value
	}
	return value - 128
}
