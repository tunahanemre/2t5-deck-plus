package tc

import "testing"

func TestFramesToTC(t *testing.T) {
	for _, c := range []struct {
		frames int
		fps    float64
		want   string
	}{
		{0, 25, "00:00:00:00"},
		{24, 25, "00:00:00:24"},
		{25, 25, "00:00:01:00"},
		{1500, 25, "00:01:00:00"},
		{90000, 25, "01:00:00:00"},
		{1501, 25, "00:01:00:01"},
		{30, 30, "00:00:01:00"},
	} {
		if got := FramesToTC(c.frames, c.fps); got != c.want {
			t.Errorf("FramesToTC(%d,%v) = %q, want %q", c.frames, c.fps, got, c.want)
		}
	}
}

// A missing or nonsense rate must degrade visibly rather than divide by zero.
func TestFramesToTCGuards(t *testing.T) {
	for _, c := range []struct {
		frames int
		fps    float64
	}{{100, 0}, {100, -5}, {-1, 25}} {
		if got := FramesToTC(c.frames, c.fps); got != "--:--:--:--" {
			t.Errorf("FramesToTC(%d,%v) = %q, want placeholder", c.frames, c.fps, got)
		}
	}
}
