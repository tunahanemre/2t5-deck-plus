"""add_marker's default span: the clip under the playhead, not a flat 1s."""
from unittest.mock import MagicMock

import resolve_driver


def _clip(start, end):
    it = MagicMock()
    it.GetStart.return_value = start
    it.GetEnd.return_value = end
    return it


def _drv(playhead_clip, on_track=None, cur_frames=9100, tl_start=0, fps=25.0):
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    tl = MagicMock()
    tl.GetStartFrame.return_value = tl_start
    tl.GetMarkers.return_value = {}
    tl.AddMarker.return_value = True
    drv.resolve = MagicMock()
    drv._read_timeline_tc_frames = lambda: (tl, cur_frames, fps, None)
    drv._target_item = lambda track=None: (playhead_clip, None)
    drv._video_item_on_track = lambda t: (on_track, None if on_track else "no_clip_on_track")
    return drv


def test_no_selection_spans_clip_under_playhead():
    ok, err, frame_id, dur_frames, fps = _drv(_clip(9000, 9500)).add_marker()
    assert ok is True and err is None
    assert frame_id == 9000
    assert dur_frames == 501          # clip length 500 + tail compensation


def test_stale_selection_falls_through_to_playhead_clip_not_one_second():
    """A selected track EMPTY at the playhead must not yield a WORSE marker
    than having no selection at all."""
    ok, err, frame_id, dur_frames, fps = _drv(_clip(9000, 9500), on_track=None).add_marker(track=2)
    assert dur_frames == 501


def test_explicit_selection_still_wins():
    drv = _drv(_clip(9000, 9500), on_track=_clip(1000, 1100))
    ok, err, frame_id, dur_frames, fps = drv.add_marker(track=2)
    assert frame_id == 1000 and dur_frames == 101


def test_no_clip_anywhere_falls_back_to_one_second():
    ok, err, frame_id, dur_frames, fps = _drv(None, on_track=None).add_marker(duration_seconds=1.0)
    assert dur_frames == 25           # 1.0s @ 25fps


def test_timeline_start_offset_applied():
    drv = _drv(_clip(90200, 90400), cur_frames=90300, tl_start=90000)
    ok, err, frame_id, dur_frames, fps = drv.add_marker()
    assert frame_id == 200            # relative to timeline start
    assert dur_frames == 201


# ---------------------------------------------------------------------------
# Tail compensation (2026-08-30). Resolve renders a marker of duration N one
# frame SHORT of an N-frame clip, so a clip-spanning marker is written with
# span+1. These pin the compensation to the clip branch ONLY -- the arithmetic
# itself was cleared first (GetEnd() verified exclusive live), so if Resolve
# ever changes this behaviour, flip MARKER_CLIP_SPAN_TAIL_COMPENSATION to 0
# and exactly these expectations move.
# ---------------------------------------------------------------------------


def test_clip_span_marker_is_written_one_frame_longer_than_the_clip():
    """The value handed to Resolve's AddMarker -- not just the return -- must
    carry the compensation, otherwise the marker still stops short."""
    drv = _drv(_clip(9000, 9500))
    ok, err, frame_id, dur_frames, fps = drv.add_marker()
    written = drv._read_timeline_tc_frames()[0].AddMarker.call_args[0][4]
    assert written == 501
    assert dur_frames == written, "reported duration must equal what Resolve holds"


def test_duration_seconds_path_is_never_compensated():
    """The flat duration_seconds fallback is not measured against a cut, so it
    must stay exactly duration_seconds * fps."""
    ok, err, frame_id, dur_frames, fps = _drv(None, on_track=None).add_marker(
        duration_seconds=2.0
    )
    assert dur_frames == 50  # 2.0s @ 25fps, uncompensated


def test_zero_length_clip_still_clamps_to_one_frame_not_two():
    """Compensation sits INSIDE the max(1, ...) clamp, so the pathological
    zero-length clip stays at 1."""
    ok, err, frame_id, dur_frames, fps = _drv(_clip(200, 200)).add_marker()
    assert dur_frames == 1
