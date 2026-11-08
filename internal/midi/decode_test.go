package midi

import "testing"

func TestRelStep(t *testing.T) {
	cases := []struct {
		val  int
		want int
	}{
		{0, 0},    // stopped
		{1, 1},    // smallest CW
		{63, 63},  // max CW
		{64, 0},   // center / no movement
		{65, -63}, // smallest CCW
		{127, -1}, // slowest CCW
	}
	for _, c := range cases {
		if got := RelStep(c.val); got != c.want {
			t.Errorf("RelStep(%d)=%d, want %d", c.val, got, c.want)
		}
	}
}
