package tc

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestParseFrames(t *testing.T) {
	cases := []struct {
		tc   string
		fps  int
		want int
	}{
		{"00:00:00:00", 25, 0},
		{"00:00:01:00", 25, 25},
		{"00:00:10:05", 25, 10*25 + 5},
		{"00:01:00:00", 25, 60 * 25},
		{"01:00:00:00", 25, 3600 * 25},
	}
	for _, c := range cases {
		got, err := ParseFrames(c.tc, c.fps)
		if err != nil {
			t.Fatalf("ParseFrames(%q, %d) unexpected error: %v", c.tc, c.fps, err)
		}
		if got != c.want {
			t.Errorf("ParseFrames(%q, %d) = %d, want %d", c.tc, c.fps, got, c.want)
		}
	}
}

func TestParseFrames_DropFrameRejected(t *testing.T) {
	_, err := ParseFrames("00:00:10;00", 30)
	if !errors.Is(err, ErrDropFrameUnsupported) {
		t.Fatalf("ParseFrames(drop-frame) err = %v, want ErrDropFrameUnsupported", err)
	}
}

func TestParseFrames_InvalidFormat(t *testing.T) {
	cases := []string{"bogus", "00:00:00", "aa:bb:cc:dd", ""}
	for _, tcStr := range cases {
		if _, err := ParseFrames(tcStr, 25); err == nil {
			t.Errorf("ParseFrames(%q) expected error, got nil", tcStr)
		}
	}
}

func TestFormatFrames(t *testing.T) {
	cases := []struct {
		frames int
		fps    int
		want   string
	}{
		{0, 25, "00:00:00:00"},
		{25, 25, "00:00:01:00"},
		{10*25 + 5, 25, "00:00:10:05"},
		{60 * 25, 25, "00:01:00:00"},
		{3600 * 25, 25, "01:00:00:00"},
		{-5, 25, "00:00:00:00"}, // negative frames clamp to zero
	}
	for _, c := range cases {
		got := FormatFrames(c.frames, c.fps)
		if got != c.want {
			t.Errorf("FormatFrames(%d, %d) = %q, want %q", c.frames, c.fps, got, c.want)
		}
	}
}

func TestFormatFrames_RoundTrip(t *testing.T) {
	fps := 25
	for _, frames := range []int{0, 1, 24, 25, 26, 3599, 3600, 90000} {
		s := FormatFrames(frames, fps)
		got, err := ParseFrames(s, fps)
		if err != nil {
			t.Fatalf("round trip ParseFrames(%q): %v", s, err)
		}
		if got != frames {
			t.Errorf("round trip frames=%d -> %q -> %d", frames, s, got)
		}
	}
}

func TestAdvance(t *testing.T) {
	got, err := Advance("00:00:10:00", 30, 25)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	// 10*25=250 frames +30 = 280 -> 280/25=11s rem5 -> 00:00:11:05
	if want := "00:00:11:05"; got != want {
		t.Errorf("Advance forward = %q, want %q", got, want)
	}
}

func TestAdvance_Negative(t *testing.T) {
	got, err := Advance("00:00:11:05", -30, 25)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if want := "00:00:10:00"; got != want {
		t.Errorf("Advance backward = %q, want %q", got, want)
	}
}

func TestAdvance_ClampsAtZero(t *testing.T) {
	got, err := Advance("00:00:00:05", -30, 25)
	if err != nil {
		t.Fatalf("Advance: %v", err)
	}
	if want := "00:00:00:00"; got != want {
		t.Errorf("Advance clamp = %q, want %q (never negative)", got, want)
	}
}

func TestAdvance_DropFrameRejected(t *testing.T) {
	_, err := Advance("00:00:10;00", 5, 30)
	if !errors.Is(err, ErrDropFrameUnsupported) {
		t.Fatalf("Advance(drop-frame) err = %v, want ErrDropFrameUnsupported", err)
	}
}

func TestAdvance_InvalidFPS(t *testing.T) {
	if _, err := Advance("00:00:00:00", 1, 0); err == nil {
		t.Error("Advance with fps=0 expected error, got nil")
	}
	if _, err := Advance("00:00:00:00", 1, -1); err == nil {
		t.Error("Advance with negative fps expected error, got nil")
	}
}

func TestIsDropFrame(t *testing.T) {
	if !IsDropFrame("00:01:00;05") {
		t.Error("expected semicolon-separated TC to be detected as drop-frame")
	}
	if IsDropFrame("00:01:00:05") {
		t.Error("colon-separated TC must not be detected as drop-frame")
	}
}

// ---------------------------------------------------------------------------
// Accelerate — ControlDial jog acceleration curve
// ---------------------------------------------------------------------------

func TestAccelerate_Zero(t *testing.T) {
	if got := Accelerate(0); got != 0 {
		t.Errorf("Accelerate(0) = %d, want 0", got)
	}
}

func TestAccelerate_SlowTurnIsAboutOneFrame(t *testing.T) {
	got := Accelerate(1)
	if got != 1 {
		t.Errorf("Accelerate(1) = %d, want 1 (slow single-step turn)", got)
	}
}

func TestAccelerate_FastTurnIsAboutThirtyFrames(t *testing.T) {
	got := Accelerate(8)
	// Spec target: fast turns land "around 30" frames — allow a
	// generous band since this is a tuned curve, not an exact contract.
	if got < 15 || got > 45 {
		t.Errorf("Accelerate(8) = %d, want roughly 15-45 (spec: fast turns ~30fr/tick)", got)
	}
}

func TestAccelerate_SignPreserved(t *testing.T) {
	pos := Accelerate(5)
	neg := Accelerate(-5)
	if neg != -pos {
		t.Errorf("Accelerate(-5) = %d, want %d (sign of Accelerate(5) negated)", neg, -pos)
	}
}

func TestAccelerate_MonotonicIncreasing(t *testing.T) {
	prev := 0
	for _, step := range []int{1, 2, 4, 8, 16} {
		got := Accelerate(step)
		if got < prev {
			t.Errorf("Accelerate(%d) = %d, not monotonically >= previous %d", step, got, prev)
		}
		prev = got
	}
}

func TestAccelerate_NoNaN(t *testing.T) {
	for _, step := range []int{-100, -1, 0, 1, 100} {
		got := Accelerate(step)
		if math.IsNaN(float64(got)) {
			t.Errorf("Accelerate(%d) produced NaN", step)
		}
	}
}

// ---------------------------------------------------------------------------
// JogTier — speed-tiered jog: |step| -> fine(0) / snap(5|10|30)
// ---------------------------------------------------------------------------

func TestJogTier_DefaultThresholds(t *testing.T) {
	cases := []struct {
		absStep int
		want    int
	}{
		{0, 0},
		{1, 0},
		{3, 0}, // fine tier, just below default t5Min=4
		{4, 5}, // default t5Min
		{7, 5},
		{8, 10}, // default t10Min
		{12, 10},
		{13, 30}, // default t30Min
		{100, 30},
	}
	for _, c := range cases {
		if got := JogTier(c.absStep, 0, 0, 0); got != c.want {
			t.Errorf("JogTier(%d, defaults) = %d, want %d", c.absStep, got, c.want)
		}
	}
}

func TestJogTier_CustomThresholds(t *testing.T) {
	// t5Min=2, t10Min=5, t30Min=10
	cases := []struct {
		absStep int
		want    int
	}{
		{1, 0},
		{2, 5},
		{4, 5},
		{5, 10},
		{9, 10},
		{10, 30},
		{50, 30},
	}
	for _, c := range cases {
		if got := JogTier(c.absStep, 2, 5, 10); got != c.want {
			t.Errorf("JogTier(%d, t5=2,t10=5,t30=10) = %d, want %d", c.absStep, got, c.want)
		}
	}
}

func TestJogTier_ZeroThresholdFallsBackToDefaultIndividually(t *testing.T) {
	// Only t5Min overridden; t10Min/t30Min fall back to defaults (8/13).
	if got := JogTier(9, 2, 0, 0); got != 10 {
		t.Errorf("JogTier(9, t5=2, t10=default, t30=default) = %d, want 10", got)
	}
	if got := JogTier(1, 2, 0, 0); got != 0 {
		t.Errorf("JogTier(1, t5=2, ...) = %d, want 0 (still below custom t5=2)", got)
	}
}

func TestJogTier_NegativeThresholdTreatedAsUnset(t *testing.T) {
	if got := JogTier(5, -1, -1, -1); got != JogTier(5, 0, 0, 0) {
		t.Errorf("JogTier with negative thresholds should behave like defaults")
	}
}

// ---------------------------------------------------------------------------
// SnapBoundaryFrames — frame<->second boundary math for the snap jog tiers
// ---------------------------------------------------------------------------

func TestSnapBoundaryFrames_Forward_NotOnBoundary(t *testing.T) {
	fps := 25
	cur, err := ParseFrames("00:00:07:00", fps) // 7s, not on a 5s boundary
	if err != nil {
		t.Fatal(err)
	}
	got := SnapBoundaryFrames(cur, 5, fps, 1)
	if want := "00:00:10:00"; FormatFrames(got, fps) != want {
		t.Errorf("SnapBoundaryFrames forward from 7s @5s tier = %q, want %q", FormatFrames(got, fps), want)
	}
}

func TestSnapBoundaryFrames_Forward_ExactlyOnBoundary_MovesFullTier(t *testing.T) {
	fps := 25
	cur, err := ParseFrames("00:00:05:00", fps) // exactly on a 5s boundary
	if err != nil {
		t.Fatal(err)
	}
	got := SnapBoundaryFrames(cur, 5, fps, 1)
	if want := "00:00:10:00"; FormatFrames(got, fps) != want {
		t.Errorf("SnapBoundaryFrames forward from exact 5s boundary = %q, want %q (must advance a full tier, not stay put)", FormatFrames(got, fps), want)
	}
}

func TestSnapBoundaryFrames_Backward_NotOnBoundary(t *testing.T) {
	fps := 25
	cur, err := ParseFrames("00:00:07:00", fps)
	if err != nil {
		t.Fatal(err)
	}
	got := SnapBoundaryFrames(cur, 5, fps, -1)
	if want := "00:00:05:00"; FormatFrames(got, fps) != want {
		t.Errorf("SnapBoundaryFrames backward from 7s @5s tier = %q, want %q", FormatFrames(got, fps), want)
	}
}

func TestSnapBoundaryFrames_Backward_ExactlyOnBoundary_MovesFullTier(t *testing.T) {
	fps := 25
	cur, err := ParseFrames("00:00:05:00", fps)
	if err != nil {
		t.Fatal(err)
	}
	got := SnapBoundaryFrames(cur, 5, fps, -1)
	if want := "00:00:00:00"; FormatFrames(got, fps) != want {
		t.Errorf("SnapBoundaryFrames backward from exact 5s boundary = %q, want %q (must retreat a full tier)", FormatFrames(got, fps), want)
	}
}

func TestSnapBoundaryFrames_ClampsAtZero(t *testing.T) {
	fps := 25
	got := SnapBoundaryFrames(0, 5, fps, -1) // already at 0, backward
	if got != 0 {
		t.Errorf("SnapBoundaryFrames backward from 0 = %d, want 0 (clamped, never negative)", got)
	}
}

func TestSnapBoundaryFrames_ThirtySecondTier(t *testing.T) {
	fps := 25
	cur, err := ParseFrames("01:00:00:00", fps) // exactly on a 30s boundary
	if err != nil {
		t.Fatal(err)
	}
	got := SnapBoundaryFrames(cur, 30, fps, 1)
	if want := "01:00:30:00"; FormatFrames(got, fps) != want {
		t.Errorf("SnapBoundaryFrames forward from 01:00:00 @30s tier = %q, want %q", FormatFrames(got, fps), want)
	}
}

func TestSnapBoundaryFrames_TenSecondTier(t *testing.T) {
	fps := 25
	cur, err := ParseFrames("00:00:23:00", fps) // between 20s and 30s
	if err != nil {
		t.Fatal(err)
	}
	got := SnapBoundaryFrames(cur, 10, fps, 1)
	if want := "00:00:30:00"; FormatFrames(got, fps) != want {
		t.Errorf("SnapBoundaryFrames forward from 23s @10s tier = %q, want %q", FormatFrames(got, fps), want)
	}
}

func TestSnapBoundaryFrames_ZeroDir_ReturnsUnchanged(t *testing.T) {
	if got := SnapBoundaryFrames(175, 5, 25, 0); got != 175 {
		t.Errorf("SnapBoundaryFrames with dir=0 = %d, want 175 (unchanged, cannot snap)", got)
	}
}

func TestSnapBoundaryFrames_NonPositiveFPS_ReturnsUnchanged(t *testing.T) {
	if got := SnapBoundaryFrames(175, 5, 0, 1); got != 175 {
		t.Errorf("SnapBoundaryFrames with fps<=0 = %d, want 175 (unchanged, cannot snap -- caller falls back to fine frames)", got)
	}
}

func TestSnapBoundaryFrames_NonPositiveSnapSeconds_ReturnsUnchanged(t *testing.T) {
	if got := SnapBoundaryFrames(175, 0, 25, 1); got != 175 {
		t.Errorf("SnapBoundaryFrames with snapSeconds<=0 = %d, want 175 (unchanged)", got)
	}
}

// ---------------------------------------------------------------------------
// JogStreak / SustainSnapSeconds — sustained-spin escalation to a
// 1-minute snap tier, overriding JogTier's speed-tier result once the
// ControlDial has been dialled continuously for >= jogSustainMs.
//
// JogStreak takes `now` as an explicit parameter on every call (rather
// than reading a wall clock itself) specifically so this is testable with
// synthetic time.Time values -- no seam, no sleep, no flakiness.
// ---------------------------------------------------------------------------

func TestJogStreak_FirstActiveTick_ZeroDuration(t *testing.T) {
	var s JogStreak
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	if got := s.Update(base, true); got != 0 {
		t.Errorf("first active tick duration = %v, want 0 (nothing to compare against yet)", got)
	}
}

func TestJogStreak_BuildsUpAcrossTicks(t *testing.T) {
	var s JogStreak
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	s.Update(base, true) // streak starts here

	if got := s.Update(base.Add(500*time.Millisecond), true); got != 500*time.Millisecond {
		t.Errorf("duration after 500ms = %v, want 500ms", got)
	}
	if got := s.Update(base.Add(1200*time.Millisecond), true); got != 1200*time.Millisecond {
		t.Errorf("duration after 1200ms = %v, want 1200ms", got)
	}
}

func TestJogStreak_PauseResetsImmediately(t *testing.T) {
	var s JogStreak
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	s.Update(base, true)
	s.Update(base.Add(1500*time.Millisecond), true) // streak built up past 1s

	if got := s.Update(base.Add(1550*time.Millisecond), false); got != 0 {
		t.Errorf("duration on a paused (no-step) tick = %v, want 0", got)
	}
}

func TestJogStreak_ResumeAfterPauseStartsFreshNotResumed(t *testing.T) {
	var s JogStreak
	base := time.Date(2026, 7, 2, 12, 0, 0, 0, time.UTC)
	s.Update(base, true)
	s.Update(base.Add(500*time.Millisecond), false) // pause resets the streak
	s.Update(base.Add(600*time.Millisecond), true)  // a FRESH streak starts here

	if got := s.Update(base.Add(700*time.Millisecond), true); got != 100*time.Millisecond {
		t.Errorf("duration = %v, want 100ms (measured from the NEW streak start at 600ms, not the original)", got)
	}
	if got := s.Update(base.Add(1700*time.Millisecond), true); got != 1100*time.Millisecond {
		t.Errorf("duration = %v, want 1100ms (still measured from the 600ms restart)", got)
	}
}

func TestJogStreak_ZeroValueUsableWithoutInit(t *testing.T) {
	// The zero value (var s JogStreak) must be immediately usable --
	// mirrors how it's declared as a plain local var in Start()'s drain
	// loop, with no explicit constructor call.
	var s JogStreak
	if got := s.Update(time.Now(), false); got != 0 {
		t.Errorf("zero-value JogStreak, paused tick, duration = %v, want 0", got)
	}
}

func TestSustainSnapSeconds_BelowThreshold_NoEscalation_DefaultThresholds(t *testing.T) {
	if got := SustainSnapSeconds(999*time.Millisecond, 0, 0); got != 0 {
		t.Errorf("SustainSnapSeconds(999ms, defaults) = %d, want 0 (no override, defer to JogTier)", got)
	}
}

func TestSustainSnapSeconds_AtThreshold_Escalates_DefaultThresholds(t *testing.T) {
	if got := SustainSnapSeconds(time.Duration(DefaultJogSustainMs)*time.Millisecond, 0, 0); got != DefaultJogSustainSeconds {
		t.Errorf("SustainSnapSeconds(%dms, defaults) = %d, want %d", DefaultJogSustainMs, got, DefaultJogSustainSeconds)
	}
}

func TestSustainSnapSeconds_WellAboveThreshold_Escalates(t *testing.T) {
	if got := SustainSnapSeconds(5*time.Second, 0, 0); got != DefaultJogSustainSeconds {
		t.Errorf("SustainSnapSeconds(5s, defaults) = %d, want %d", got, DefaultJogSustainSeconds)
	}
}

func TestSustainSnapSeconds_CustomThresholds(t *testing.T) {
	if got := SustainSnapSeconds(1500*time.Millisecond, 2000, 45); got != 0 {
		t.Errorf("SustainSnapSeconds(1500ms, ms=2000) = %d, want 0 (below custom threshold)", got)
	}
	if got := SustainSnapSeconds(2000*time.Millisecond, 2000, 45); got != 45 {
		t.Errorf("SustainSnapSeconds(2000ms, ms=2000, sec=45) = %d, want 45", got)
	}
	if got := SustainSnapSeconds(2001*time.Millisecond, 2000, 45); got != 45 {
		t.Errorf("SustainSnapSeconds(2001ms, ms=2000, sec=45) = %d, want 45", got)
	}
}

func TestSustainSnapSeconds_ZeroOrNegativeThresholdFallsBackToDefaultIndividually(t *testing.T) {
	// Only jogSustainMs overridden; jogSustainSeconds falls back to default (60).
	if got := SustainSnapSeconds(2*time.Second, 500, 0); got != DefaultJogSustainSeconds {
		t.Errorf("SustainSnapSeconds(2s, ms=500, sec=default) = %d, want %d", got, DefaultJogSustainSeconds)
	}
	// Only jogSustainSeconds overridden; jogSustainMs falls back to default (1000).
	if got := SustainSnapSeconds(time.Duration(DefaultJogSustainMs)*time.Millisecond, 0, 90); got != 90 {
		t.Errorf("SustainSnapSeconds(defaultMs, ms=default, sec=90) = %d, want 90", got)
	}
}

func TestSustainSnapSeconds_NegativeThresholdsTreatedAsUnset(t *testing.T) {
	got := SustainSnapSeconds(2*time.Second, -1, -1)
	want := SustainSnapSeconds(2*time.Second, 0, 0)
	if got != want {
		t.Errorf("SustainSnapSeconds with negative thresholds = %d, want %d (same as defaults)", got, want)
	}
}
