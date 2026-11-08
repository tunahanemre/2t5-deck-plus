"""
Tests for the direction-button clip-selection feature's sidecar ops
(Up/Down/Left/Right/C5, Edit page -- replaces D1's knob-turn "selectClip"
rotation):
  - ResolveDriver.current_playhead_frame() -- absolute playhead frame,
    used by the Go engine's playhead-left-the-clip auto-reset
  - ResolveDriver.select_clip_vertical()   -- Up/Down: step to the next
    occupied video track, clamped (no wrap) at the ends
  - ResolveDriver.select_clip_horizontal() -- Left/Right: previous/next
    clip by start time, GLOBALLY closest across all video tracks (ties
    broken to the top-most track), moves the playhead
  - ResolveDriver._select_and_tint()       -- shared restore-prev +
    tint-new + persist-state tail (reuses clear_highlight/
    _write_highlight_state/_delete_highlight_state)
  - sidecar.handle()'s "select_clip_vertical"/"select_clip_horizontal"
    dispatch, and the "status" op's new "playhead_frame" field

Uses the same mock ResolveDriver / mock Timeline+TimelineItem pattern as
test_select_clip_ops.py / test_highlight_clip_ops.py (DaVinciResolveScript
patched out before import so resolve_driver.py's module-level import never
touches the real Blackmagic framework). SetClipColor/GetClipColor/
ClearClipColor/SetCurrentTimecode are ALWAYS MagicMock methods here --
never called against a live Resolve project, per the hard hermeticity
constraint.
"""
import os
import sys
from unittest.mock import MagicMock

# ---------------------------------------------------------------------------
# Import resolve_driver/sidecar with DaVinciResolveScript mocked out.
# ---------------------------------------------------------------------------


def _import_modules():
    sidecar_dir = os.path.dirname(os.path.abspath(__file__))
    if sidecar_dir not in sys.path:
        sys.path.insert(0, sidecar_dir)

    dvr_stub = MagicMock()
    dvr_stub.scriptapp.return_value = MagicMock()
    sys.modules.setdefault("DaVinciResolveScript", dvr_stub)

    import resolve_driver
    import sidecar as sc
    return resolve_driver, sc


resolve_driver, sc = _import_modules()


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_item(name, start, end, color="", unique_id="uid-1"):
    """A TimelineItem stub exposing GetName/GetStart/GetEnd (track-scan
    resolution) PLUS GetClipColor/SetClipColor/ClearClipColor/GetUniqueId
    (highlight/tint bookkeeping) -- the union of test_select_clip_ops.py's
    and test_highlight_clip_ops.py's item helpers, needed since this
    feature's ops do both track-range resolution AND tinting in one call."""
    item = MagicMock()
    item.GetName.return_value = name
    item.GetStart.return_value = start
    item.GetEnd.return_value = end
    item.GetClipColor.return_value = color
    item.GetUniqueId.return_value = unique_id
    return item


def _make_driver(cur_tc, fps_setting, track_items, highlight_state_path):
    """Same shape as test_highlight_clip_ops.py's _make_driver_for_highlight."""
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    drv._highlight = None
    drv.highlight_state_path = str(highlight_state_path)
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = cur_tc
    tl.GetSetting.return_value = fps_setting
    max_track = max(track_items.keys()) if track_items else 0
    tl.GetTrackCount.side_effect = lambda kind: max_track
    tl.GetItemListInTrack.side_effect = lambda kind, n: track_items.get(n, [])
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = tl
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve
    drv._tl_mock = tl
    return drv


def _make_driver_no_timeline(highlight_state_path):
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    drv._highlight = None
    drv.highlight_state_path = str(highlight_state_path)
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = None
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve
    return drv


# ---------------------------------------------------------------------------
# current_playhead_frame
# ---------------------------------------------------------------------------


def test_current_playhead_frame_returns_absolute_frame(tmp_path):
    drv = _make_driver("00:00:00:05", "25", {1: []}, tmp_path / "h.json")
    assert drv.current_playhead_frame() == 5


def test_current_playhead_frame_none_when_no_timeline(tmp_path):
    drv = _make_driver_no_timeline(tmp_path / "h.json")
    assert drv.current_playhead_frame() is None


def test_current_playhead_frame_none_on_drop_frame(tmp_path):
    drv = _make_driver("00:00:10;00", "29.97", {}, tmp_path / "h.json")
    assert drv.current_playhead_frame() is None


def test_current_playhead_frame_never_touches_resolve_on_refusal(tmp_path):
    """Unlike current_video_item_id(), this method has no self-healing
    contract -- a refusal must never touch self.resolve (it delegates
    entirely to the side-effect-free _read_timeline_tc_frames)."""
    drv = _make_driver_no_timeline(tmp_path / "h.json")
    healthy = drv.resolve
    drv.current_playhead_frame()
    assert drv.resolve is healthy


# ---------------------------------------------------------------------------
# select_clip_vertical
# ---------------------------------------------------------------------------


def test_select_clip_vertical_no_selection_up_lands_on_first_occupied(tmp_path):
    item1 = _make_item("A.mov", 0, 100, unique_id="uid-A")
    item3 = _make_item("C.mov", 0, 100, unique_id="uid-C")
    drv = _make_driver("00:00:00:05", "25", {1: [item1], 3: [item3]}, tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_vertical(
        direction=1, current_track=None, color="Teal")

    assert ok is True
    assert err is None
    assert track == 1
    assert uid == "uid-A"
    assert (start, end) == (0, 100)
    item1.SetClipColor.assert_called_once_with("Teal")


def test_select_clip_vertical_no_selection_down_lands_on_last_occupied(tmp_path):
    item1 = _make_item("A.mov", 0, 100, unique_id="uid-A")
    item3 = _make_item("C.mov", 0, 100, unique_id="uid-C")
    drv = _make_driver("00:00:00:05", "25", {1: [item1], 3: [item3]}, tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_vertical(
        direction=-1, current_track=0, color="Teal")

    assert ok is True
    assert track == 3
    assert uid == "uid-C"


def test_select_clip_vertical_steps_up_one_track(tmp_path):
    item1 = _make_item("A.mov", 0, 100, unique_id="uid-A")
    item2 = _make_item("B.mov", 0, 100, unique_id="uid-B")
    item4 = _make_item("D.mov", 0, 100, unique_id="uid-D")
    drv = _make_driver("00:00:00:05", "25", {1: [item1], 2: [item2], 4: [item4]}, tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_vertical(
        direction=1, current_track=2, color="Teal")

    assert ok is True
    assert track == 4
    assert uid == "uid-D"


def test_select_clip_vertical_clamps_at_top_no_wrap(tmp_path):
    item1 = _make_item("A.mov", 0, 100, unique_id="uid-A")
    item2 = _make_item("B.mov", 0, 100, unique_id="uid-B")
    drv = _make_driver("00:00:00:05", "25", {1: [item1], 2: [item2]}, tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_vertical(
        direction=1, current_track=2, color="Teal")

    assert ok is True
    assert track == 2  # stays at the top, does NOT wrap to 1
    assert uid == "uid-B"


def test_select_clip_vertical_clamps_at_bottom_no_wrap(tmp_path):
    item1 = _make_item("A.mov", 0, 100, unique_id="uid-A")
    item2 = _make_item("B.mov", 0, 100, unique_id="uid-B")
    drv = _make_driver("00:00:00:05", "25", {1: [item1], 2: [item2]}, tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_vertical(
        direction=-1, current_track=1, color="Teal")

    assert ok is True
    assert track == 1  # stays at the bottom, does NOT wrap to 2
    assert uid == "uid-A"


def test_select_clip_vertical_current_track_not_in_occupied_list_bootstraps(tmp_path):
    # The previously-selected track is no longer occupied at THIS playhead
    # position (e.g. playhead moved elsewhere) -- falls back to the
    # bootstrap ("not found") behavior, same as nextTrackIndex's Go twin.
    item1 = _make_item("A.mov", 0, 100, unique_id="uid-A")
    drv = _make_driver("00:00:00:05", "25", {1: [item1]}, tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_vertical(
        direction=1, current_track=9, color="Teal")

    assert ok is True
    assert track == 1


def test_select_clip_vertical_no_occupied_track_returns_no_clip(tmp_path):
    drv = _make_driver("00:00:00:05", "25", {1: []}, tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_vertical(
        direction=1, current_track=None, color="Teal")

    assert ok is False
    assert err == "no_clip"
    assert track is None


def test_select_clip_vertical_no_timeline_propagates_refusal(tmp_path):
    drv = _make_driver_no_timeline(tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_vertical(
        direction=1, current_track=None, color="Teal")

    assert ok is False
    assert err == "no_timeline"


def test_select_clip_vertical_captures_original_color_before_tint(tmp_path):
    item = _make_item("A.mov", 0, 100, color="Orange", unique_id="uid-A")
    drv = _make_driver("00:00:00:05", "25", {1: [item]}, tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_vertical(
        direction=1, current_track=None, color="Teal")

    assert orig == "Orange"
    item.SetClipColor.assert_called_once_with("Teal")


def test_select_clip_vertical_leaked_own_highlight_color_captures_empty(tmp_path):
    # FIX 1: a clip already tinted the TOOL's own highlight color (leaked
    # from a prior select whose restore was lost) must NOT be captured as
    # "original" -- see _capture_original_color's doc comment.
    item = _make_item("A.mov", 0, 100, color="Teal", unique_id="uid-A")
    drv = _make_driver("00:00:00:05", "25", {1: [item]}, tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_vertical(
        direction=1, current_track=None, color="Teal")

    assert ok is True
    assert orig == ""
    item.SetClipColor.assert_called_once_with("Teal")


def test_select_clip_vertical_leaked_tint_clears_not_restores_to_tint(tmp_path):
    # End-to-end: select the leaked-tint clip, then C5/clear -- must land
    # on NO color, never hand the leaked tint back via SetClipColor again.
    item = _make_item("A.mov", 0, 100, color="Teal", unique_id="uid-A")
    drv = _make_driver("00:00:00:05", "25", {1: [item]}, tmp_path / "h.json")
    drv.select_clip_vertical(direction=1, current_track=None, color="Teal")

    ok, err = drv.clear_highlight()

    assert ok is True
    item.ClearClipColor.assert_called_once_with()
    assert item.SetClipColor.call_args_list == [(("Teal",), {})]


def test_select_clip_vertical_restores_previous_clip_on_move(tmp_path):
    item1 = _make_item("A.mov", 0, 100, color="Orange", unique_id="uid-A")
    item2 = _make_item("B.mov", 0, 100, color="", unique_id="uid-B")
    drv = _make_driver("00:00:00:05", "25", {1: [item1], 2: [item2]}, tmp_path / "h.json")

    drv.select_clip_vertical(direction=1, current_track=None, color="Teal")
    assert item1.SetClipColor.call_args_list == [(("Teal",), {})]

    drv.select_clip_vertical(direction=1, current_track=1, color="Teal")

    # item1 restored to its captured original ("Orange") before item2 tinted.
    assert item1.SetClipColor.call_args_list == [(("Teal",), {}), (("Orange",), {})]
    item2.SetClipColor.assert_called_once_with("Teal")


def test_select_clip_vertical_no_previous_color_uses_clear_clip_color(tmp_path):
    item1 = _make_item("A.mov", 0, 100, color="", unique_id="uid-A")
    item2 = _make_item("B.mov", 0, 100, color="", unique_id="uid-B")
    drv = _make_driver("00:00:00:05", "25", {1: [item1], 2: [item2]}, tmp_path / "h.json")

    drv.select_clip_vertical(direction=1, current_track=None, color="Teal")
    drv.select_clip_vertical(direction=1, current_track=1, color="Teal")

    item1.ClearClipColor.assert_called_once_with()


def test_select_clip_vertical_tint_failure_reports_error_via_select_and_tint(tmp_path):
    # _select_and_tint's own try/except around GetClipColor/GetUniqueId --
    # distinct from _occupied_video_items' boundary-check GetStart/GetEnd
    # calls, which run BEFORE this item is even chosen as the target.
    item1 = _make_item("A.mov", 0, 100, unique_id="uid-A")
    item1.GetClipColor.side_effect = RuntimeError("boom")
    drv = _make_driver("00:00:00:05", "25", {1: [item1]}, tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_vertical(
        direction=1, current_track=None, color="Teal")

    assert ok is False
    assert track is None
    item1.SetClipColor.assert_not_called()


# ---------------------------------------------------------------------------
# select_clip_horizontal
# ---------------------------------------------------------------------------


def test_select_clip_horizontal_prev_picks_globally_closest_not_same_track(tmp_path):
    # FIX 2: a clip on ANOTHER track can sit strictly between the anchor
    # and the next/prev clip on the anchor's OWN track -- that closer clip
    # must win, even though it's not on the same track. (The old
    # same-track-preferred behavior picked same_track_prev here -- this is
    # the exact bug the fix corrects.)
    same_track_prev = _make_item("A0.mov", 0, 100, unique_id="uid-A0")
    closer_other_track = _make_item("B.mov", 50, 150, unique_id="uid-B")
    current = _make_item("A1.mov", 200, 300, unique_id="uid-A1")
    drv = _make_driver(
        "00:00:08:00", "25",
        {1: [same_track_prev, current], 2: [closer_other_track]},
        tmp_path / "h.json",
    )

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=-1, current_track=1, current_start=200, color="Teal")

    assert ok is True
    assert track == 2
    assert uid == "uid-B"
    assert start == 50


def test_select_clip_horizontal_falls_back_to_nearest_other_track(tmp_path):
    other_track_prev = _make_item("B0.mov", 0, 100, unique_id="uid-B0")
    current = _make_item("A1.mov", 200, 300, unique_id="uid-A1")
    drv = _make_driver(
        "00:00:08:00", "25", {1: [current], 2: [other_track_prev]}, tmp_path / "h.json",
    )

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=-1, current_track=1, current_start=200, color="Teal")

    assert ok is True
    assert track == 2
    assert uid == "uid-B0"


def test_select_clip_horizontal_next_picks_globally_closest_not_same_track(tmp_path):
    # FIX 2, Right/next twin of the Left/prev test above.
    current = _make_item("A0.mov", 0, 100, unique_id="uid-A0")
    same_track_next = _make_item("A1.mov", 200, 300, unique_id="uid-A1")
    closer_other_track = _make_item("B.mov", 120, 180, unique_id="uid-B")
    drv = _make_driver(
        "00:00:02:00", "25",
        {1: [current, same_track_next], 2: [closer_other_track]},
        tmp_path / "h.json",
    )

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=1, current_track=1, current_start=0, color="Teal")

    assert ok is True
    assert track == 2
    assert uid == "uid-B"
    assert start == 120


def test_select_clip_horizontal_picks_closest_same_track_candidate(tmp_path):
    far = _make_item("Far.mov", 0, 50, unique_id="uid-far")
    near = _make_item("Near.mov", 100, 150, unique_id="uid-near")
    current = _make_item("Current.mov", 200, 300, unique_id="uid-cur")
    drv = _make_driver("00:00:08:00", "25", {1: [far, near, current]}, tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=-1, current_track=1, current_start=200, color="Teal")

    assert ok is True
    assert uid == "uid-near"


def test_select_clip_horizontal_moves_playhead_to_new_clip_start(tmp_path):
    prev_clip = _make_item("A0.mov", 40, 100, unique_id="uid-A0")
    current = _make_item("A1.mov", 200, 300, unique_id="uid-A1")
    drv = _make_driver("00:00:08:00", "25", {1: [prev_clip, current]}, tmp_path / "h.json")

    drv.select_clip_horizontal(direction=-1, current_track=1, current_start=200, color="Teal")

    # frame 40 @ 25fps -> 00:00:01:15
    drv._tl_mock.SetCurrentTimecode.assert_called_once_with("00:00:01:15")


def test_select_clip_horizontal_moves_playhead_to_globally_closest_clip_on_other_track(tmp_path):
    # FIX 2: the playhead must land on the GLOBALLY closest clip's start,
    # even when that clip is on a different track than the current
    # selection.
    same_track_prev = _make_item("A0.mov", 0, 100, unique_id="uid-A0")
    closer_other_track = _make_item("B.mov", 60, 150, unique_id="uid-B")
    current = _make_item("A1.mov", 200, 300, unique_id="uid-A1")
    drv = _make_driver(
        "00:00:08:00", "25",
        {1: [same_track_prev, current], 2: [closer_other_track]},
        tmp_path / "h.json",
    )

    drv.select_clip_horizontal(direction=-1, current_track=1, current_start=200, color="Teal")

    # frame 60 @ 25fps -> 00:00:02:10 -- the OTHER track's clip, not
    # frame 0 (same_track_prev), and not left at the anchor.
    drv._tl_mock.SetCurrentTimecode.assert_called_once_with("00:00:02:10")


def test_select_clip_horizontal_prev_tie_breaks_to_top_track(tmp_path):
    # FIX 2 tie-break: two clips share the IDENTICAL start frame on
    # different tracks, both closer than any other candidate -- resolves
    # to the TOP-most (highest-numbered) track, matching this file's
    # top-occupied default elsewhere (_target_item(track=None), etc).
    low_track_tie = _make_item("Lo.mov", 50, 100, unique_id="uid-lo")
    high_track_tie = _make_item("Hi.mov", 50, 100, unique_id="uid-hi")
    current = _make_item("Cur.mov", 200, 300, unique_id="uid-cur")
    drv = _make_driver(
        "00:00:08:00", "25",
        {1: [low_track_tie], 2: [current], 5: [high_track_tie]},
        tmp_path / "h.json",
    )

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=-1, current_track=2, current_start=200, color="Teal")

    assert ok is True
    assert track == 5
    assert uid == "uid-hi"
    assert start == 50


def test_select_clip_horizontal_next_tie_breaks_to_top_track(tmp_path):
    # Right/next twin of the tie-break test above.
    low_track_tie = _make_item("Lo.mov", 200, 250, unique_id="uid-lo")
    high_track_tie = _make_item("Hi.mov", 200, 250, unique_id="uid-hi")
    current = _make_item("Cur.mov", 0, 100, unique_id="uid-cur")
    drv = _make_driver(
        "00:00:00:00", "25",
        {1: [low_track_tie], 2: [current], 5: [high_track_tie]},
        tmp_path / "h.json",
    )

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=1, current_track=2, current_start=0, color="Teal")

    assert ok is True
    assert track == 5
    assert uid == "uid-hi"
    assert start == 200


def test_select_clip_horizontal_leaked_own_highlight_color_captures_empty(tmp_path):
    # FIX 1, exercised through select_clip_horizontal's own call into the
    # shared _select_and_tint tail.
    prev_clip = _make_item("A0.mov", 0, 100, color="Teal", unique_id="uid-A0")
    current = _make_item("A1.mov", 200, 300, unique_id="uid-A1")
    drv = _make_driver("00:00:08:00", "25", {1: [prev_clip, current]}, tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=-1, current_track=1, current_start=200, color="Teal")

    assert ok is True
    assert orig == ""
    prev_clip.SetClipColor.assert_called_once_with("Teal")


def test_select_clip_horizontal_no_selection_bootstraps_from_top_occupied(tmp_path):
    # Also doubles as a tie-break regression guard: item1 (track 1) and
    # prev_on_top_track (track 3) both start at frame 0 -- the tie
    # resolves to the higher track (3), same as
    # test_select_clip_horizontal_prev_tie_breaks_to_top_track above.
    item1 = _make_item("A.mov", 0, 100, unique_id="uid-A")
    prev_on_top_track = _make_item("C0.mov", 0, 50, unique_id="uid-C0")
    top_occupied = _make_item("C1.mov", 50, 150, unique_id="uid-C1")
    drv = _make_driver(
        "00:00:02:10", "25",
        {1: [item1], 3: [prev_on_top_track, top_occupied]},
        tmp_path / "h.json",
    )

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=-1, current_track=None, current_start=None, color="Teal")

    assert ok is True
    assert track == 3
    assert uid == "uid-C0"


# ---------------------------------------------------------------------------
# BUG 2 (2026-08-12 live report): bootstrapping must anchor on the PLAYHEAD,
# not the bootstrap clip's own GetStart(). Real case: a timeline-long clip on
# a lower video track (e.g. a full-length background graphic) is the
# topmost-occupied clip at almost every playhead position; its GetStart() is
# always 0, so the OLD code made every Right press resolve relative to frame
# 0 (the globally-earliest clip on the whole timeline) instead of the next
# clip near wherever the user actually was. Confirmed against the exact
# reported log trace: Right always landed on track=2 start=175 (the
# earliest clip after frame 0) no matter where the playhead was.
# ---------------------------------------------------------------------------


def test_select_clip_horizontal_no_selection_anchors_on_playhead_not_bootstrap_clip_start(tmp_path):
    spanning = _make_item("BG.mov", 0, 23925, unique_id="uid-bg")
    early = _make_item("Early.mov", 175, 500, unique_id="uid-early")
    near = _make_item("Near.mov", 6000, 6100, unique_id="uid-near")
    # Playhead at frame 5000 (00:03:20:00 @ 25fps) -- deep inside `spanning`,
    # nowhere near frame 0 or frame 175.
    drv = _make_driver(
        "00:03:20:00", "25",
        {4: [spanning], 2: [early, near]},
        tmp_path / "h.json",
    )

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=1, current_track=None, current_start=None, color="Teal")

    assert ok is True
    # Must pick `near` (the next clip after the PLAYHEAD) -- the OLD
    # frame-0-anchored bug always picked `early` instead (see the test
    # below, which pins down that exact old/buggy answer for the record).
    assert uid == "uid-near"
    assert track == 2
    assert start == 6000


def test_select_clip_horizontal_no_selection_playhead_mid_clip_left_skips_own_clip(tmp_path):
    # Refinement the naive "just use cur_frames" fix must also get right:
    # bootstrapping while the playhead sits PARTWAY THROUGH the topmost
    # occupied clip (not at its own start) must never offer that SAME clip
    # back as its own "previous" result just because its start is
    # numerically less than the playhead -- Left must land on a genuinely
    # different, earlier clip, exactly as it would if the anchor clip had
    # been the (correctly) currently-selected one.
    earlier = _make_item("Earlier.mov", 0, 40, unique_id="uid-earlier")
    current = _make_item("Current.mov", 50, 150, unique_id="uid-current")
    # Playhead at frame 90 (00:00:03:15 @ 25fps) -- mid `current`.
    drv = _make_driver(
        "00:00:03:15", "25",
        {1: [earlier, current]},
        tmp_path / "h.json",
    )

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=-1, current_track=None, current_start=None, color="Teal")

    assert ok is True
    assert uid == "uid-earlier"
    assert track == 1
    assert start == 0


def test_select_clip_horizontal_no_selection_stacked_tracks_anchors_on_playhead(tmp_path):
    # Several tracks stacked and occupied at the exact playhead (V1
    # background, V2 an earlier separate clip that has already ended, V3
    # topmost) -- only the TOPMOST occupied clip (the anchor) is excluded
    # from candidacy; v1_bg (also under the playhead, on a LOWER track) is
    # NOT excluded and remains a valid candidate -- it simply loses to
    # v2_prev_clip because v2_prev_clip's start (300) is closer to the
    # playhead (500) than v1_bg's start (0).
    v1_bg = _make_item("V1bg.mov", 0, 10000, unique_id="uid-v1bg")
    v2_prev_clip = _make_item("V2prev.mov", 300, 400, unique_id="uid-v2prev")
    v3_top = _make_item("V3top.mov", 450, 550, unique_id="uid-v3top")
    drv = _make_driver(
        "00:00:20:00", "25",  # frame 500
        {1: [v1_bg], 2: [v2_prev_clip], 3: [v3_top]},
        tmp_path / "h.json",
    )

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=-1, current_track=None, current_start=None, color="Teal")

    assert ok is True
    assert uid == "uid-v2prev"
    assert track == 2
    assert start == 300


def test_select_clip_horizontal_no_selection_bootstrap_tie_breaks_to_top_track(tmp_path):
    # Tie-break regression guard for the bootstrap (no-selection) path
    # specifically, now that the anchor is the PLAYHEAD rather than a
    # clip's own start -- two candidates share the identical start frame on
    # different tracks; the higher-numbered track must still win.
    anchor_clip = _make_item("Anchor.mov", 400, 600, unique_id="uid-anchor")
    low_tie = _make_item("Lo.mov", 100, 200, unique_id="uid-lo")
    high_tie = _make_item("Hi.mov", 100, 200, unique_id="uid-hi")
    drv = _make_driver(
        "00:00:20:00", "25",  # frame 500, mid anchor_clip
        {5: [anchor_clip], 1: [low_tie], 3: [high_tie]},
        tmp_path / "h.json",
    )

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=-1, current_track=None, current_start=None, color="Teal")

    assert ok is True
    assert track == 3
    assert uid == "uid-hi"


def test_select_clip_horizontal_no_more_clips_in_direction_errors(tmp_path):
    only = _make_item("A.mov", 0, 100, unique_id="uid-A")
    drv = _make_driver("00:00:04:00", "25", {1: [only]}, tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=-1, current_track=1, current_start=0, color="Teal")

    assert ok is False
    assert err == "no_more_clips"
    assert track is None
    only.SetClipColor.assert_not_called()
    # No-op: playhead must not move when there's nothing to move to.
    drv._tl_mock.SetCurrentTimecode.assert_not_called()


def test_select_clip_horizontal_no_more_clips_across_multiple_tracks_is_noop(tmp_path):
    # FIX 2: several tracks are occupied, but NONE has a clip further in
    # the requested direction -- must no-op exactly like the single-track
    # case (the global scan across all tracks still finds nothing beyond
    # the anchor), never SetCurrentTimecode, never tint, current
    # selection/playhead left exactly as they were.
    a = _make_item("A.mov", 0, 100, unique_id="uid-a")
    b = _make_item("B.mov", 0, 50, unique_id="uid-b")
    c = _make_item("C.mov", 50, 100, unique_id="uid-c")
    drv = _make_driver("00:00:04:00", "25", {1: [a], 2: [b], 3: [c]}, tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=-1, current_track=1, current_start=0, color="Teal")

    assert ok is False
    assert err == "no_more_clips"
    assert track is None
    drv._tl_mock.SetCurrentTimecode.assert_not_called()
    a.SetClipColor.assert_not_called()
    b.SetClipColor.assert_not_called()
    c.SetClipColor.assert_not_called()


def test_select_clip_horizontal_no_timeline_propagates_refusal(tmp_path):
    drv = _make_driver_no_timeline(tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=1, current_track=1, current_start=0, color="Teal")

    assert ok is False
    assert err == "no_timeline"


def test_select_clip_horizontal_restores_previous_tint_on_move(tmp_path):
    prev_clip = _make_item("A0.mov", 0, 100, color="Orange", unique_id="uid-A0")
    current = _make_item("A1.mov", 200, 300, color="", unique_id="uid-A1")
    drv = _make_driver("00:00:08:00", "25", {1: [prev_clip, current]}, tmp_path / "h.json")

    # First select `current` directly via the shared tail (simulating a
    # prior vertical select), then move Left onto prev_clip.
    drv._select_and_tint(current, 1, "Teal")
    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=-1, current_track=1, current_start=200, color="Teal")

    assert ok is True
    # current restored to its captured original ("") -> ClearClipColor.
    current.ClearClipColor.assert_called_once_with()
    prev_clip.SetClipColor.assert_called_once_with("Teal")


# ---------------------------------------------------------------------------
# PERF (2026-08-12): every Left/Right press previously re-walked
# GetItemListInTrack for EVERY video track from scratch -- the dominant cost
# of press-to-react latency on a timeline with many clips/tracks. Cached for
# a short TTL, invalidated on a (best-effort) timeline-name change. Scoped
# ONLY to select_clip_horizontal's own full-timeline scan -- deliberately
# NOT shared with _occupied_video_items (used by the 250ms status tick, D1,
# and select_clip_vertical), so this change cannot alter the freshness of
# any of those other paths.
# ---------------------------------------------------------------------------


def test_horiz_scan_track_items_caches_within_ttl(tmp_path):
    # Exercises the cache helper directly (rather than through
    # select_clip_horizontal end-to-end) so this test isolates the
    # candidate-scan cache from _select_and_tint's UNRELATED
    # highlight-restore lookup (_find_item_by_unique_id), which also calls
    # GetItemListInTrack on every successful select and would otherwise
    # make a naive before/after call-count comparison flaky/misleading.
    a = _make_item("A.mov", 0, 100, unique_id="uid-a")
    b = _make_item("B.mov", 200, 300, unique_id="uid-b")
    drv = _make_driver("00:00:08:00", "25", {1: [a, b]}, tmp_path / "h.json")
    tl = drv._tl_mock

    drv._horiz_scan_track_items(tl)
    calls_after_first = tl.GetItemListInTrack.call_count
    assert calls_after_first == 1  # one video track

    # A second call immediately after must NOT re-walk any video track.
    drv._horiz_scan_track_items(tl)
    calls_after_second = tl.GetItemListInTrack.call_count

    assert calls_after_second == calls_after_first


def test_horiz_scan_track_items_rescans_after_ttl_expires(tmp_path):
    a = _make_item("A.mov", 0, 100, unique_id="uid-a")
    b = _make_item("B.mov", 200, 300, unique_id="uid-b")
    drv = _make_driver("00:00:08:00", "25", {1: [a, b]}, tmp_path / "h.json")
    tl = drv._tl_mock

    drv._horiz_scan_track_items(tl)
    calls_after_first = tl.GetItemListInTrack.call_count

    # Simulate the TTL elapsing by backdating the cached timestamp.
    name, ts, tracks = drv._horiz_scan_cache
    drv._horiz_scan_cache = (name, ts - 10.0, tracks)

    drv._horiz_scan_track_items(tl)
    calls_after_second = tl.GetItemListInTrack.call_count

    assert calls_after_second > calls_after_first


def test_horiz_scan_track_items_invalidates_on_timeline_name_change(tmp_path):
    a = _make_item("A.mov", 0, 100, unique_id="uid-a")
    b = _make_item("B.mov", 200, 300, unique_id="uid-b")
    drv = _make_driver("00:00:08:00", "25", {1: [a, b]}, tmp_path / "h.json")
    tl = drv._tl_mock
    tl.GetName.return_value = "Timeline 1"

    drv._horiz_scan_track_items(tl)
    calls_after_first = tl.GetItemListInTrack.call_count

    tl.GetName.return_value = "Timeline 2"  # user switched timelines
    drv._horiz_scan_track_items(tl)
    calls_after_second = tl.GetItemListInTrack.call_count

    assert calls_after_second > calls_after_first


def test_select_clip_horizontal_cache_reflects_edit_after_ttl(tmp_path):
    # Correctness beats speed: once the TTL has elapsed, a clip added since
    # the last scan must be visible -- the cache never serves permanently
    # stale results.
    a = _make_item("A.mov", 0, 100, unique_id="uid-a")
    drv = _make_driver("00:00:08:00", "25", {1: [a]}, tmp_path / "h.json")

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=1, current_track=1, current_start=0, color="Teal")
    assert ok is False
    assert err == "no_more_clips"

    # A clip is added on another track, and the cache TTL elapses.
    added = _make_item("Added.mov", 150, 250, unique_id="uid-added")
    drv._tl_mock.GetTrackCount.side_effect = lambda kind: 2
    drv._tl_mock.GetItemListInTrack.side_effect = lambda kind, n: {1: [a], 2: [added]}.get(n, [])
    name, ts, tracks = drv._horiz_scan_cache
    drv._horiz_scan_cache = (name, ts - 10.0, tracks)

    ok, err, track, uid, start, end, orig = drv.select_clip_horizontal(
        direction=1, current_track=1, current_start=0, color="Teal")
    assert ok is True
    assert uid == "uid-added"


# ---------------------------------------------------------------------------
# sidecar.handle() dispatch
# ---------------------------------------------------------------------------


def test_handle_select_clip_vertical_dispatches():
    drv = MagicMock()
    drv.select_clip_vertical.return_value = (True, None, 2, "uid-B", 0, 100, "")
    resp = sc.handle(
        {"op": "select_clip_vertical", "direction": 1, "current_track": 1, "color": "Teal"}, {}, drv)
    assert resp == {
        "ok": True, "err": None, "track": 2, "unique_id": "uid-B",
        "start": 0, "end": 100, "original_color": "",
    }
    drv.select_clip_vertical.assert_called_once_with(direction=1, current_track=1, color="Teal")
    drv._ensure_connected.assert_called_once()


def test_handle_select_clip_vertical_propagates_error():
    drv = MagicMock()
    drv.select_clip_vertical.return_value = (False, "no_clip", None, None, None, None, None)
    resp = sc.handle({"op": "select_clip_vertical", "direction": 1}, {}, drv)
    assert resp["ok"] is False
    assert resp["err"] == "no_clip"


def test_handle_select_clip_vertical_calls_ensure_connected_first():
    call_order = []
    drv = MagicMock()
    drv._ensure_connected.side_effect = lambda: call_order.append("ensure_connected")
    drv.select_clip_vertical.side_effect = (
        lambda **kw: call_order.append("select_clip_vertical")
        or (True, None, 1, "uid-A", 0, 100, "")
    )
    sc.handle({"op": "select_clip_vertical", "direction": 1}, {}, drv)
    assert call_order == ["ensure_connected", "select_clip_vertical"]


def test_handle_select_clip_horizontal_dispatches():
    drv = MagicMock()
    drv.select_clip_horizontal.return_value = (True, None, 1, "uid-A0", 0, 100, "")
    resp = sc.handle(
        {"op": "select_clip_horizontal", "direction": -1, "current_track": 1,
         "current_start": 200, "color": "Teal"}, {}, drv)
    assert resp == {
        "ok": True, "err": None, "track": 1, "unique_id": "uid-A0",
        "start": 0, "end": 100, "original_color": "",
    }
    drv.select_clip_horizontal.assert_called_once_with(
        direction=-1, current_track=1, current_start=200, color="Teal")
    drv._ensure_connected.assert_called_once()


def test_handle_select_clip_horizontal_propagates_error():
    drv = MagicMock()
    drv.select_clip_horizontal.return_value = (False, "no_more_clips", None, None, None, None, None)
    resp = sc.handle({"op": "select_clip_horizontal", "direction": -1}, {}, drv)
    assert resp["ok"] is False
    assert resp["err"] == "no_more_clips"


def test_handle_select_clip_horizontal_calls_ensure_connected_first():
    call_order = []
    drv = MagicMock()
    drv._ensure_connected.side_effect = lambda: call_order.append("ensure_connected")
    drv.select_clip_horizontal.side_effect = (
        lambda **kw: call_order.append("select_clip_horizontal")
        or (True, None, 1, "uid-A", 0, 100, "")
    )
    sc.handle({"op": "select_clip_horizontal", "direction": -1}, {}, drv)
    assert call_order == ["ensure_connected", "select_clip_horizontal"]

# "status" op's new "playhead_frame" field is covered in
# test_sidecar_status.py (colocated with clip_id/timecode/etc's own status-
# field tests), not duplicated here.
