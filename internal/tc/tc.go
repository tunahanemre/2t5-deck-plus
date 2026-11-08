// Package tc provides timecode/frame-count math for the Edit-page jog
// control, plus the ControlDial's turn-to-frame-delta acceleration curve.
//
// Two distinct concerns live here:
//
//  1. Accelerate — pure Go, engine-side: converts an accumulated MIDI
//     encoder step (from the ControlDial's relative CC, see
//     internal/midi.RelStep) into a signed whole-frame delta, tuned so a
//     slow single-step turn moves ~1 frame and a fast burst moves ~30
//     frames. This is what internal/engine actually calls at runtime to
//     build the "jog {frames}" sidecar command.
//
//  2. ParseFrames/FormatFrames/Advance — pure TC-string <-> frame-count
//     math for a non-drop timecode ("HH:MM:SS:FF"). Only Python
//     (sidecar/resolve_driver.py) ever touches a live Resolve Timeline
//     object, so the sidecar's own jog handler re-implements this exact
//     algorithm directly against Timeline.GetCurrentTimecode()/
//     GetSetting()/SetCurrentTimecode() (see resolve_driver.py's jog()
//     doc comment, which references this package as its reference
//     implementation). Keeping the algorithm here first, with thorough
//     Go tests, is what makes it "small and tested" per the design spec
//     before it gets mirrored into the sidecar.
//
// Drop-frame timecode (fps 29.97/59.94, semicolon frame separator) is
// deliberately unsupported in v1 — the user's live-verified Edit timeline
// is 25fps non-drop, and drop-frame math (skipped frame numbers at
// minute boundaries) is deferred rather than guessed at.
package tc

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"2t5-deck-plus/internal/accel"
)

// ErrDropFrameUnsupported is returned by ParseFrames/Advance when the
// timecode string uses drop-frame notation (semicolon frame separator).
var ErrDropFrameUnsupported = errors.New("tc: drop-frame timecode not supported")

// ErrInvalidTimecode is returned when a timecode string cannot be parsed
// as "HH:MM:SS:FF".
var ErrInvalidTimecode = errors.New("tc: invalid timecode format, want HH:MM:SS:FF")

// IsDropFrame reports whether tcStr uses drop-frame notation. Resolve (and
// the wider NLE convention) marks drop-frame timecode with a semicolon
// before the frame field, e.g. "01:00:00;00", instead of the non-drop
// "01:00:00:00".
func IsDropFrame(tcStr string) bool {
	return strings.Contains(tcStr, ";")
}

// ParseFrames converts a non-drop "HH:MM:SS:FF" timecode string into an
// absolute frame count at the given integer fps.
func ParseFrames(tcStr string, fps int) (int, error) {
	if IsDropFrame(tcStr) {
		return 0, ErrDropFrameUnsupported
	}
	parts := strings.Split(tcStr, ":")
	if len(parts) != 4 {
		return 0, ErrInvalidTimecode
	}
	var nums [4]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, fmt.Errorf("%w: %v", ErrInvalidTimecode, err)
		}
		nums[i] = n
	}
	h, m, s, f := nums[0], nums[1], nums[2], nums[3]
	return ((h*60+m)*60+s)*fps + f, nil
}

// FormatFrames converts an absolute frame count back to a "HH:MM:SS:FF"
// non-drop timecode string at the given integer fps. Negative frame
// counts clamp to zero (timecode never goes negative).
func FormatFrames(frames int, fps int) string {
	if frames < 0 {
		frames = 0
	}
	if fps <= 0 {
		fps = 1
	}
	f := frames % fps
	totalSec := frames / fps
	s := totalSec % 60
	totalMin := totalSec / 60
	m := totalMin % 60
	h := totalMin / 60
	return fmt.Sprintf("%02d:%02d:%02d:%02d", h, m, s, f)
}

// Advance moves a non-drop timecode by deltaFrames (positive or
// negative), clamping the result to a minimum of 00:00:00:00 (timecode
// never goes negative). Returns ErrDropFrameUnsupported if tcStr uses
// drop-frame notation.
func Advance(tcStr string, deltaFrames int, fps int) (string, error) {
	if fps <= 0 {
		return "", fmt.Errorf("tc: invalid fps %d", fps)
	}
	cur, err := ParseFrames(tcStr, fps)
	if err != nil {
		return "", err
	}
	next := cur + deltaFrames
	if next < 0 {
		next = 0
	}
	return FormatFrames(next, fps), nil
}

// jogCurve maps an accumulated ControlDial encoder step to a signed frame
// delta: a slow single-step turn (magnitude 1) yields ~1 frame; a fast
// burst (magnitude ~8, accumulated within one 50ms drain tick) yields
// roughly 30 frames. This is a DIFFERENT curve than CDL's (accel.New(0.002,
// 1.5), tuned for fractional CDL units) — jog needs whole frames with a
// much steeper acceleration for the same encoder hardware.
var jogCurve = accel.New(1.0, 1.6)

// Accelerate converts an accumulated ControlDial encoder step into a
// signed frame delta using the DEFAULT jog acceleration curve, rounded to
// the nearest whole frame (SetCurrentTimecode has no sub-frame concept).
// step==0 always yields 0.
func Accelerate(step int) int {
	return int(math.Round(jogCurve.Amount(step)))
}

// AccelerateWith is Accelerate with a per-binding curve: base frames per
// unit step and gamma acceleration exponent (see config.Binding.Base /
// .Gamma). Non-positive base or gamma falls back to the default curve's
// value (1.0 / 1.6), so an unset config field behaves exactly like
// Accelerate. This is what the engine calls so the jog feel is tunable
// from bindings.json without a rebuild.
func AccelerateWith(step int, base, gamma float64) int {
	if base <= 0 {
		base = 1.0
	}
	if gamma <= 0 {
		gamma = 1.6
	}
	return int(math.Round(accel.New(base, gamma).Amount(step)))
}

// ---------------------------------------------------------------------------
// Speed-tiered jog: fine frame-by-frame at slow spin, snap-to-round-second
// at fast spin. See config.Binding.JogTier5Min/JogTier10Min/JogTier30Min
// for the bindings.json-tunable thresholds, and internal/engine's IsJog
// branch for the caller.
// ---------------------------------------------------------------------------

// Default |step| thresholds for JogTier, used whenever a caller passes 0
// for the corresponding threshold (i.e. bindings.json leaves it unset).
const (
	// DefaultJogTier5Min is the |step| at and above which the jog snaps to
	// the next 5-second timecode boundary instead of nudging by frames.
	DefaultJogTier5Min = 4
	// DefaultJogTier10Min is the |step| at and above which the jog snaps
	// to the next 10-second boundary.
	DefaultJogTier10Min = 8
	// DefaultJogTier30Min is the |step| at and above which the jog snaps
	// to the next 30-second boundary.
	DefaultJogTier30Min = 13
)

// JogTier maps the |step| magnitude of one coalesced ControlDial drain
// tick to a jog tier:
//
//	0  - FINE tier: frame-by-frame scrub via Accelerate/AccelerateWith,
//	     unchanged slow-dial behavior for precise scrubbing.
//	5  - snap the playhead to the next 5-second round timecode boundary.
//	10 - snap to the next 10-second boundary.
//	30 - snap to the next 30-second boundary.
//
// t5Min/t10Min/t30Min are the tunable |step| thresholds (bindings.json's
// ControlDial jog binding — see config.Binding); pass 0 (or a negative
// value) for any of them to fall back to the package defaults
// (DefaultJogTier5Min=4, DefaultJogTier10Min=8, DefaultJogTier30Min=13).
// absStep should already be the absolute value of the accumulated encoder
// step; direction is decided separately by the caller (sign of the raw
// step) and fed to SnapBoundaryFrames.
func JogTier(absStep, t5Min, t10Min, t30Min int) int {
	if t5Min <= 0 {
		t5Min = DefaultJogTier5Min
	}
	if t10Min <= 0 {
		t10Min = DefaultJogTier10Min
	}
	if t30Min <= 0 {
		t30Min = DefaultJogTier30Min
	}
	switch {
	case absStep >= t30Min:
		return 30
	case absStep >= t10Min:
		return 10
	case absStep >= t5Min:
		return 5
	default:
		return 0
	}
}

// SnapBoundaryFrames returns the absolute frame count of the next round
// timecode boundary — a multiple of snapSeconds*fps — strictly in the
// direction of dir (positive = forward/later, negative = backward/
// earlier) from curFrames. If curFrames already sits exactly on a
// boundary, the result still advances a FULL tier in that direction
// (never a no-op) — e.g. from exactly a 5s boundary with dir>0, the
// result is one full 5s tier later, not the same boundary again. The
// result is clamped to a minimum of 0 (timecode never goes negative).
//
// dir==0, fps<=0, or snapSeconds<=0 are all "cannot snap" conditions and
// return curFrames UNCHANGED. This is a deliberate sentinel: the
// sidecar's jog_snap (hand-mirrored from this function into Python — see
// resolve_driver.py, since only that process touches a live Resolve
// Timeline) uses the equivalent "unchanged" outcome — there, a
// drop-frame timecode or non-integer fps (23.976, 29.97, 59.94) — as the
// signal to fall back to a fine-frame jog instead of mis-snapping.
func SnapBoundaryFrames(curFrames, snapSeconds, fps, dir int) int {
	if dir == 0 || fps <= 0 || snapSeconds <= 0 {
		return curFrames
	}
	tierFrames := snapSeconds * fps
	floorBoundary := (curFrames / tierFrames) * tierFrames
	var next int
	switch {
	case dir > 0:
		next = floorBoundary + tierFrames
	case floorBoundary == curFrames:
		next = floorBoundary - tierFrames
	default:
		next = floorBoundary
	}
	if next < 0 {
		next = 0
	}
	return next
}

// ---------------------------------------------------------------------------
// Sustained-spin jog escalation: when the ControlDial has been dialled
// CONTINUOUSLY (no pause) for at least jogSustainMs, the jog snap tier
// escalates to jogSustainSeconds (a 1-minute boundary by default),
// OVERRIDING whatever JogTier's per-tick |step| speed would otherwise have
// picked -- see config.Binding.JogSustainMs/JogSustainSeconds and
// internal/engine's IsJog branch, which computes the streak once per
// 50ms drain tick and calls SustainSnapSeconds with the result.
// ---------------------------------------------------------------------------

// DefaultJogSustainMs is the duration (ms) of continuous ControlDial
// dial activity after which SustainSnapSeconds escalates to
// DefaultJogSustainSeconds, used whenever a caller passes <=0 for
// jogSustainMs (i.e. bindings.json leaves it unset).
const DefaultJogSustainMs = 1000

// DefaultJogSustainSeconds is the snap tier (seconds) forced once a
// continuous dial streak has reached the sustain threshold, used
// whenever a caller passes <=0 for jogSustainSeconds.
const DefaultJogSustainSeconds = 60

// JogStreak tracks how long the ControlDial (or any "jog"-typed control)
// has been dialled continuously, across successive 50ms drain ticks.
//
// It holds no clock of its own -- callers pass `now` on every tick using
// the SAME time.Time value already computed once per drain tick (see
// internal/engine's Start(), `now := time.Now()`), so Update's arithmetic
// is a pure function of its inputs: fully deterministic under `go test`
// with synthetic time.Time values, no wall-clock reads, no sleeps, and no
// package-level seam needed (unlike startMIDIFn/launchSidecarFn, which
// gate real side effects -- there is no side effect here to gate, just
// math). The zero value is immediately usable (mirrors how it's declared
// as a plain `var jogStreak tc.JogStreak` local in the drain loop, with
// no constructor call).
type JogStreak struct {
	start time.Time
}

// Update advances the streak for one drain tick. hasStep reports whether
// THIS tick produced a nonzero jog-type step (the dial was actively
// turned this tick, not idle). A pause (hasStep==false) resets the streak
// immediately -- the very next active tick starts a brand new streak from
// zero, it does NOT resume the old one (see
// TestJogStreak_ResumeAfterPauseStartsFreshNotResumed). Returns the
// streak's cumulative duration as of now: 0 on the streak's first active
// tick (nothing to compare against yet) or on any paused tick.
func (s *JogStreak) Update(now time.Time, hasStep bool) time.Duration {
	if !hasStep {
		s.start = time.Time{}
		return 0
	}
	if s.start.IsZero() {
		s.start = now
		return 0
	}
	return now.Sub(s.start)
}

// SustainSnapSeconds returns the sustained-spin snap tier (seconds) to
// force for this tick's jog op, or 0 if the streak hasn't reached the
// sustain threshold yet -- 0 means "no override, use JogTier's speed-tier
// result unchanged" (see internal/engine's IsJog branch, which only
// overrides snapSeconds when this returns >0).
//
// jogSustainMs/jogSustainSeconds are the bindings.json-tunable values
// (config.Binding.JogSustainMs/.JogSustainSeconds); <=0 for either falls
// back to the package defaults (DefaultJogSustainMs=1000,
// DefaultJogSustainSeconds=60), matching the JogTier/AccelerateWith
// precedent so an unset bindings.json field behaves exactly like the
// hardcoded default.
func SustainSnapSeconds(streakDuration time.Duration, jogSustainMs, jogSustainSeconds int) int {
	if jogSustainMs <= 0 {
		jogSustainMs = DefaultJogSustainMs
	}
	if jogSustainSeconds <= 0 {
		jogSustainSeconds = DefaultJogSustainSeconds
	}
	if streakDuration >= time.Duration(jogSustainMs)*time.Millisecond {
		return jogSustainSeconds
	}
	return 0
}
