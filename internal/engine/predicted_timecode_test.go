package engine

// Tests for predictedTimecode -- the "live HUD timecode during playback"
// fix (2026-08-25). Before this fix, a "status" round-trip timeout froze
// Status.Timecode at its last-known value for however long the timeout
// streak lasted (which, per queryStatus's own 2026-08-25 doc comment, can
// be the entire duration of a playback) -- the HUD only "jumped to truth"
// on pause. The chosen fix extrapolates locally from the last CONFIRMED
// read: predicted = last_confirmed_frame + elapsed_wall_clock * fps,
// resynced to truth on every genuinely successful read, bounded by
// maxTimecodePredictionWindow so a permanently-wedged sidecar doesn't
// fabricate a clock that runs forever. See predictedTimecode's doc comment
// for the full design, including the honestly-acknowledged limitations
// (reverse playback, jog/scrub, non-1.0 play rate -- none of which Resolve's
// scripting API exposes any signal for).
//
// All hermetic: pure-function tests against predictedTimecode directly, no
// Start()/sidecar/real time.Sleep involved -- every "elapsed" is
// constructed via a fixed confirmedAt/now pair, so these are fast and
// deterministic.

import (
	"testing"
	"time"

	"2t5-deck-plus/internal/tc"
)

// TestPredictedTimecode_NotStale_ReturnsRawTimecodeUnchanged proves the
// byte-identical case: a fresh (non-stale) tick is untouched by this
// feature, regardless of what havePlayheadFrame/fps/confirmedAt hold --
// mirrors the pre-existing behavior for every caller before this fix
// existed.
func TestPredictedTimecode_NotStale_ReturnsRawTimecodeUnchanged(t *testing.T) {
	now := time.Now()
	s := cachedStatus{
		stale:             false,
		timecode:          "01:23:45:12",
		havePlayheadFrame: true,
		playheadFrame:     999999, // deliberately inconsistent with timecode -- must be ignored
		fps:               25,
		confirmedAt:       now.Add(-1 * time.Hour), // deliberately stale-looking -- must be ignored
	}
	if got := predictedTimecode(s, now); got != "01:23:45:12" {
		t.Errorf("predictedTimecode(not stale) = %q, want raw timecode %q unchanged", got, "01:23:45:12")
	}
}

// TestPredictedTimecode_NotStale_NoLiveTimeline_ReturnsEmpty proves the
// "no live timeline" case (empty timecode, formatHUD renders dashes) is
// also untouched.
func TestPredictedTimecode_NotStale_NoLiveTimeline_ReturnsEmpty(t *testing.T) {
	now := time.Now()
	s := cachedStatus{stale: false, timecode: ""}
	if got := predictedTimecode(s, now); got != "" {
		t.Errorf("predictedTimecode(no timeline) = %q, want empty", got)
	}
}

// TestPredictedTimecode_Stale_ExtrapolatesForwardFromLastConfirmed proves
// the actual fix: a stale tick with a usable frame+fps baseline advances
// the displayed timecode by elapsed-wall-clock * fps, not just resurfacing
// the frozen last-known string.
func TestPredictedTimecode_Stale_ExtrapolatesForwardFromLastConfirmed(t *testing.T) {
	confirmedAt := time.Now()
	now := confirmedAt.Add(2 * time.Second)
	s := cachedStatus{
		stale:             true,
		timecode:          "00:00:40:00", // the frozen carried-forward string -- must NOT be what's returned
		havePlayheadFrame: true,
		playheadFrame:     1000,
		fps:               25,
		confirmedAt:       confirmedAt,
	}
	want := tc.FramesToTC(1000+2*25, 25) // 1000 + elapsed(2s) * fps(25) = 1050
	if got := predictedTimecode(s, now); got != want {
		t.Errorf("predictedTimecode(stale, 2s elapsed @25fps) = %q, want %q (extrapolated, not frozen at %q)", got, want, s.timecode)
	}
	if got := predictedTimecode(s, now); got == s.timecode {
		t.Errorf("predictedTimecode must not just return the frozen carried-forward timecode while extrapolating")
	}
}

// TestPredictedTimecode_Stale_NoPlayheadFrame_FallsBackToRawTimecode proves
// the safety fallback when there is no frame baseline to extrapolate from
// (e.g. no live timeline this tick, or a fake-sidecar test double that
// never populates playhead_frame) -- same "can't extrapolate, don't
// pretend to" posture as the freeze case below.
func TestPredictedTimecode_Stale_NoPlayheadFrame_FallsBackToRawTimecode(t *testing.T) {
	confirmedAt := time.Now()
	now := confirmedAt.Add(2 * time.Second)
	s := cachedStatus{
		stale:             true,
		timecode:          "00:00:40:00",
		havePlayheadFrame: false,
		playheadFrame:     1000, // present but must be ignored -- havePlayheadFrame is the gate
		fps:               25,
		confirmedAt:       confirmedAt,
	}
	if got := predictedTimecode(s, now); got != "00:00:40:00" {
		t.Errorf("predictedTimecode(no playhead frame) = %q, want raw timecode %q", got, "00:00:40:00")
	}
}

// TestPredictedTimecode_Stale_FpsZero_FallsBackToRawTimecode proves the
// same fallback when fps is unusable (no live timeline ever had a valid
// frame rate this session).
func TestPredictedTimecode_Stale_FpsZero_FallsBackToRawTimecode(t *testing.T) {
	confirmedAt := time.Now()
	now := confirmedAt.Add(2 * time.Second)
	s := cachedStatus{
		stale:             true,
		timecode:          "00:00:40:00",
		havePlayheadFrame: true,
		playheadFrame:     1000,
		fps:               0,
		confirmedAt:       confirmedAt,
	}
	if got := predictedTimecode(s, now); got != "00:00:40:00" {
		t.Errorf("predictedTimecode(fps<=0) = %q, want raw timecode %q", got, "00:00:40:00")
	}
}

// TestPredictedTimecode_Stale_WithinWindow_StillExtrapolates proves the
// window boundary is inclusive: exactly maxTimecodePredictionWindow elapsed
// still extrapolates (only STRICTLY beyond it freezes -- see the next
// test).
func TestPredictedTimecode_Stale_WithinWindow_StillExtrapolates(t *testing.T) {
	confirmedAt := time.Now()
	now := confirmedAt.Add(maxTimecodePredictionWindow)
	s := cachedStatus{
		stale:             true,
		timecode:          "00:00:00:00",
		havePlayheadFrame: true,
		playheadFrame:     0,
		fps:               25,
		confirmedAt:       confirmedAt,
	}
	want := tc.FramesToTC(int(maxTimecodePredictionWindow.Seconds()*25), 25)
	if got := predictedTimecode(s, now); got != want {
		t.Errorf("predictedTimecode(elapsed == window) = %q, want %q (still extrapolating at the boundary)", got, want)
	}
}

// TestPredictedTimecode_Stale_BeyondWindow_FreezesAtLastConfirmed proves
// the "prefer freezing over unbounded" design: once wall-clock elapsed
// since the last CONFIRMED read exceeds maxTimecodePredictionWindow, this
// function stops inventing numbers and falls back to the plain
// last-confirmed timecode string -- exactly this feature's pre-existing
// frozen behavior, not whatever the extrapolation had most recently
// reached. This is what keeps a permanently-wedged sidecar (an accepted,
// already-documented long-term state elsewhere in this file) from showing
// a predicted clock that silently keeps counting forward for minutes/hours
// unattended.
func TestPredictedTimecode_Stale_BeyondWindow_FreezesAtLastConfirmed(t *testing.T) {
	confirmedAt := time.Now()
	now := confirmedAt.Add(maxTimecodePredictionWindow + time.Second)
	s := cachedStatus{
		stale:             true,
		timecode:          "00:00:40:00",
		havePlayheadFrame: true,
		playheadFrame:     1000,
		fps:               25,
		confirmedAt:       confirmedAt,
	}
	if got := predictedTimecode(s, now); got != "00:00:40:00" {
		t.Errorf("predictedTimecode(elapsed beyond window) = %q, want frozen last-confirmed %q, not a further-extrapolated value", got, "00:00:40:00")
	}
}

// TestPredictedTimecode_NegativeElapsed_FallsBackToRawTimecode is a
// defensive guard against clock weirdness (now before confirmedAt, which
// should never happen in production since both derive from time.Now() in
// strictly increasing call order, but must not produce a NEGATIVE
// extrapolated frame count if it somehow did).
func TestPredictedTimecode_NegativeElapsed_FallsBackToRawTimecode(t *testing.T) {
	confirmedAt := time.Now()
	now := confirmedAt.Add(-1 * time.Second)
	s := cachedStatus{
		stale:             true,
		timecode:          "00:00:40:00",
		havePlayheadFrame: true,
		playheadFrame:     1000,
		fps:               25,
		confirmedAt:       confirmedAt,
	}
	if got := predictedTimecode(s, now); got != "00:00:40:00" {
		t.Errorf("predictedTimecode(negative elapsed) = %q, want raw timecode %q, not a backward-extrapolated guess", got, "00:00:40:00")
	}
}

// TestPredictedTimecode_ResyncsOnNextConfirmedRead proves the "resync hard
// and often" half of the design: predictedTimecode is a pure function of
// its arguments (no persisted extrapolation state of its own), so the
// drain loop's own per-tick call pattern -- extrapolate during a stale
// streak, then use the FRESH s.timecode the instant a real read lands --
// naturally means no accumulated drift ever survives a successful read,
// regardless of how long the preceding stale streak ran or how far it had
// extrapolated.
func TestPredictedTimecode_ResyncsOnNextConfirmedRead(t *testing.T) {
	confirmedAt := time.Now()
	staleTick := confirmedAt.Add(8 * time.Second)
	staleSnapshot := cachedStatus{
		stale:             true,
		timecode:          "00:00:40:00",
		havePlayheadFrame: true,
		playheadFrame:     1000,
		fps:               25,
		confirmedAt:       confirmedAt,
	}
	extrapolated := predictedTimecode(staleSnapshot, staleTick)
	if extrapolated == staleSnapshot.timecode {
		t.Fatalf("test precondition: expected extrapolation to move past the frozen timecode, got %q unchanged", extrapolated)
	}

	// The NEXT tick's status round trip actually succeeds -- a fresh,
	// non-stale snapshot with its own new confirmed truth, drawn from a
	// DIFFERENT point in the timeline than the extrapolation above would
	// have predicted, proving the caller trusts the fresh read outright
	// rather than blending/averaging with the prior extrapolation.
	freshSnapshot := cachedStatus{stale: false, timecode: "00:01:15:10"}
	if got := predictedTimecode(freshSnapshot, staleTick.Add(time.Millisecond)); got != "00:01:15:10" {
		t.Errorf("predictedTimecode after resync = %q, want the fresh confirmed value %q, not anything derived from the prior extrapolation", got, "00:01:15:10")
	}
}
