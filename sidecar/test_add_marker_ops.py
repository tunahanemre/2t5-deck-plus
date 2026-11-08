"""
Tests for the C1 "add marker at playhead" sidecar op:
  - ResolveDriver.add_marker() -- computes frameId RELATIVE TO THE
    TIMELINE'S OWN START FRAME (Timeline.GetStartFrame()), converts
    duration_seconds -> duration_frames using the live timeline's fps,
    and calls Timeline.AddMarker(frameId, color, name, note, duration,
    customData). Existence is checked EXPLICITLY via GetMarkers() BEFORE
    calling AddMarker: if frame_id is already a key there, returns a
    graceful {"ok": False, "err": "marker_exists"} without ever calling
    AddMarker. If AddMarker is called and STILL returns False (frame was
    free), returns {"ok": False, "err": "add_failed"} instead -- NOT
    "marker_exists", which would misreport the cause.
  - sidecar.handle()'s "add_marker" dispatch.

*** CONFIRMED LIVE, Resolve 21.0.0.47 (2026-07-02) ***: Timeline.AddMarker
returns False when passed an empty-string `name`, REGARDLESS of whether a
marker already exists at that frame -- proven live: AddMarker(f, "Blue",
"", "", 25, "") -> False; the identical call with a non-empty name -> True
on the same empty frame. This is why add_marker never passes "" as the
name argument. As of 2026-07-03, an omitted/empty name is AUTO-NUMBERED
("M01", "M02", ...) by scanning the current timeline's GetMarkers() for
existing "M<int>" names and taking max+1 -- see
ResolveDriver._next_auto_marker_name. DEFAULT_MARKER_NAME ("Marker") is
now only the LAST-RESORT fallback, used when GetMarkers() itself returns
None (can't be scanned at all). See resolve_driver.py's
DEFAULT_MARKER_NAME module-level comment.

*** LIVE-VERIFICATION FLAG (still open) ***: the frameId = cur_frames -
GetStartFrame() subtraction below is modeled on Resolve's documented
AddMarker contract (frameId is 0-based relative to the timeline's first
frame) but has NOT been exercised against a real Resolve timeline in this
session. Verify on a real timeline (especially one whose start timecode
is NOT 00:00:00:00, e.g. Resolve's 01:00:00:00 default) before trusting
this in production -- see resolve_driver.add_marker's docstring for the
same flag. Likewise GetMarkers()'s key type (int vs str) is assumed int
here, unverified against a real timeline.

Uses the same mock ResolveDriver / mock Timeline pattern as
test_edit_page_ops.py's _make_driver_for_jog (DaVinciResolveScript
patched out before import so resolve_driver.py's module-level import
never touches the real Blackmagic framework). GetMarkers/AddMarker are
ALWAYS MagicMock methods here -- never called against a live Resolve
project.
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

# _make_item_with_range is test_select_clip_ops.py's TimelineItem stub
# factory (GetName/GetStart/GetEnd only) -- reused here as-is rather than
# duplicated, since the Batch 1 "marker spans the D1-selected clip"
# add_marker(track=N) tests need the exact same clip-stub shape that
# feature's own _video_item_on_track tests already use.
from test_select_clip_ops import _make_item_with_range  # noqa: E402


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------


def _make_driver(
    cur_tc, fps_setting, start_frame=0, add_marker_result=True, existing_markers=None
):
    """Same shape as test_edit_page_ops.py's _make_driver_for_jog, plus a
    mocked GetStartFrame() (the timeline's own start-frame offset),
    GetMarkers() (defaults to an empty dict -- no pre-existing markers,
    so the marker_exists pre-check never fires unless a test opts in via
    `existing_markers`), and AddMarker() (defaults to returning True --
    "marker added")."""
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = cur_tc
    tl.GetSetting.return_value = fps_setting
    tl.GetStartFrame.return_value = start_frame
    tl.GetMarkers.return_value = existing_markers if existing_markers is not None else {}
    tl.AddMarker.return_value = add_marker_result
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = tl
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve
    drv._tl_mock = tl
    return drv


def _make_driver_no_timeline():
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = None
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve
    return drv


def _make_driver_for_track_marker_ops(
    cur_tc,
    fps_setting,
    track_items=None,
    start_frame=0,
    add_marker_result=True,
    existing_markers=None,
    get_item_list_side_effect=None,
):
    """Combines _make_driver's marker-creation mocks (GetStartFrame/
    GetMarkers/AddMarker) with test_select_clip_ops._make_driver_for_track_ops's
    track-walk mocks (GetTrackCount/GetItemListInTrack) -- needed because
    add_marker(track=N) exercises BOTH _video_item_on_track (the track
    walk) and the marker-creation path (GetMarkers/AddMarker) in the same
    call, and neither existing single-purpose helper covers both.

    track_items: dict[int, list[item]], same shape as
    _make_driver_for_track_ops's parameter -- items as
    GetItemListInTrack("video", n) would return them. Use
    _make_item_with_range(name, start, end) (imported above) to build
    them.

    get_item_list_side_effect: optional callable(kind, n) overriding the
    default `track_items.get(n, [])` lookup -- lets a test simulate
    _video_item_on_track raising for a specific track (an "other
    refusal" distinct from the plain "no_clip_on_track" empty-list
    case), by raising instead of returning a list.
    """
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = cur_tc
    tl.GetSetting.return_value = fps_setting
    tl.GetStartFrame.return_value = start_frame
    tl.GetMarkers.return_value = existing_markers if existing_markers is not None else {}
    tl.AddMarker.return_value = add_marker_result
    track_items = track_items or {}
    max_track = max(track_items.keys()) if track_items else 0
    tl.GetTrackCount.side_effect = lambda kind: max_track
    if get_item_list_side_effect is not None:
        tl.GetItemListInTrack.side_effect = get_item_list_side_effect
    else:
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


# ---------------------------------------------------------------------------
# add_marker -- frameId math (relative to timeline start)
# ---------------------------------------------------------------------------


def test_add_marker_frame_relative_to_zero_start_frame():
    # cur_tc "00:00:01:00" @ 25fps -> cur_frames = 25. start_frame=0 -> frameId=25.
    drv = _make_driver("00:00:01:00", "25", start_frame=0)
    ok, err, frame, _duration, _fps = drv.add_marker(duration_seconds=1.0, color="Blue")
    assert ok is True
    assert err is None
    assert frame == 25
    drv._tl_mock.AddMarker.assert_called_once()
    args = drv._tl_mock.AddMarker.call_args[0]
    assert args[0] == 25  # frameId


def test_add_marker_frame_relative_to_nonzero_start_frame():
    # A timeline that does NOT start at 00:00:00:00 (e.g. Resolve's common
    # 01:00:00:00 default) has a nonzero GetStartFrame() -- frameId must be
    # computed relative to THAT, not the raw absolute timecode-derived
    # frame count. cur_tc "00:00:00:10" @ 25fps -> cur_frames = 10;
    # start_frame = 5 -> frameId = 5.
    drv = _make_driver("00:00:00:10", "25", start_frame=5)
    ok, err, frame, _duration, _fps = drv.add_marker(duration_seconds=1.0, color="Blue")
    assert ok is True
    assert frame == 5
    args = drv._tl_mock.AddMarker.call_args[0]
    assert args[0] == 5


# ---------------------------------------------------------------------------
# add_marker -- duration_frames = round(duration_seconds * fps)
# ---------------------------------------------------------------------------


def test_add_marker_duration_frames_default_one_second_at_25fps():
    drv = _make_driver("00:00:00:00", "25", start_frame=0)
    drv.add_marker(duration_seconds=1.0, color="Blue")
    args = drv._tl_mock.AddMarker.call_args[0]
    # AddMarker(frameId, color, name, note, duration, customData)
    assert args[4] == 25


def test_add_marker_duration_frames_scales_with_seconds():
    drv = _make_driver("00:00:00:00", "25", start_frame=0)
    drv.add_marker(duration_seconds=2.0, color="Blue")
    args = drv._tl_mock.AddMarker.call_args[0]
    assert args[4] == 50


def test_add_marker_duration_frames_uses_live_fps_generically():
    # 30fps timeline, duration_seconds=1 -> 30 frames (not hardcoded 25).
    drv = _make_driver("00:00:00:00", "30", start_frame=0)
    drv.add_marker(duration_seconds=1.0, color="Blue")
    args = drv._tl_mock.AddMarker.call_args[0]
    assert args[4] == 30


# ---------------------------------------------------------------------------
# add_marker -- color/name/note passthrough
# ---------------------------------------------------------------------------


def test_add_marker_passes_color_name_note():
    drv = _make_driver("00:00:00:00", "25", start_frame=0)
    drv.add_marker(duration_seconds=1.0, color="Red", name="Beat", note="cue")
    args = drv._tl_mock.AddMarker.call_args[0]
    # AddMarker(frameId, color, name, note, duration, customData)
    assert args[1] == "Red"
    assert args[2] == "Beat"
    assert args[3] == "cue"
    assert args[5] == ""


def test_add_marker_default_note_is_empty_string_not_none():
    drv = _make_driver("00:00:00:00", "25", start_frame=0)
    drv.add_marker(duration_seconds=1.0, color="Blue")
    args = drv._tl_mock.AddMarker.call_args[0]
    # AddMarker(frameId, color, name, note, duration, customData)
    assert args[3] == ""


# ---------------------------------------------------------------------------
# add_marker -- name resolution: never empty ("" rejected by AddMarker on
# Resolve 21), auto-numbered "M01"/"M02"/... when omitted/empty, explicit
# names always win, DEFAULT_MARKER_NAME only as the last-resort fallback
# when GetMarkers() itself can't be scanned (returns None).
# ---------------------------------------------------------------------------


def test_add_marker_omitted_name_auto_numbers_m01_when_no_existing_markers():
    # _make_driver's default GetMarkers() return is {} (empty dict --
    # genuinely zero markers, NOT None), so the auto-number starts at 1.
    drv = _make_driver("00:00:00:00", "25", start_frame=0)
    ok, err, frame, _duration, _fps = drv.add_marker(duration_seconds=1.0, color="Blue")
    args = drv._tl_mock.AddMarker.call_args[0]
    assert args[2] == "M01"
    assert args[2] != ""
    assert ok is True
    assert err is None


def test_add_marker_explicit_empty_string_name_auto_numbers_same_as_omitted():
    drv = _make_driver("00:00:00:00", "25", start_frame=0)
    drv.add_marker(duration_seconds=1.0, color="Blue", name="")
    args = drv._tl_mock.AddMarker.call_args[0]
    assert args[2] == "M01"
    assert args[2] != ""


def test_add_marker_explicit_nonempty_name_is_not_overridden():
    drv = _make_driver("00:00:00:00", "25", start_frame=0)
    drv.add_marker(duration_seconds=1.0, color="Blue", name="Beat")
    args = drv._tl_mock.AddMarker.call_args[0]
    assert args[2] == "Beat"


def test_add_marker_auto_numbers_next_m_name_ignoring_non_m_markers():
    # existing markers: "M07" at frame 5, and a user's own non-"M"-pattern
    # marker "EP10-ID03" at frame 9 (must be ignored by the scan) -- the
    # next auto name on a NEW free frame must be "M08" (max M-number + 1),
    # not "M02" or "M10".
    drv = _make_driver(
        "00:00:00:20",
        "25",
        start_frame=0,
        existing_markers={
            5: {"name": "M07"},
            9: {"name": "EP10-ID03"},
        },
    )
    ok, err, frame, _duration, _fps = drv.add_marker(duration_seconds=1.0, color="Blue")
    args = drv._tl_mock.AddMarker.call_args[0]
    assert args[2] == "M08"
    assert ok is True


def test_add_marker_getmarkers_none_falls_back_to_default_marker_name():
    # GetMarkers() itself returns None (distinct from an empty dict --
    # can't be safely scanned at all) -- add_marker falls back to
    # DEFAULT_MARKER_NAME ("Marker"), the last-resort fallback, NOT
    # "M01" (which would incorrectly assume zero markers).
    drv = _make_driver("00:00:00:00", "25", start_frame=0)
    drv._tl_mock.GetMarkers.return_value = None
    ok, err, frame, _duration, _fps = drv.add_marker(duration_seconds=1.0, color="Blue")
    args = drv._tl_mock.AddMarker.call_args[0]
    assert args[2] == resolve_driver.DEFAULT_MARKER_NAME
    assert args[2] != ""
    assert ok is True


# ---------------------------------------------------------------------------
# ResolveDriver._next_auto_marker_name -- direct unit tests (scan edge cases)
# ---------------------------------------------------------------------------


def test_next_auto_marker_name_empty_dict_returns_m01():
    assert resolve_driver.ResolveDriver._next_auto_marker_name({}) == "M01"


def test_next_auto_marker_name_none_returns_default_marker_name():
    assert (
        resolve_driver.ResolveDriver._next_auto_marker_name(None)
        == resolve_driver.DEFAULT_MARKER_NAME
    )


def test_next_auto_marker_name_ignores_non_matching_and_missing_names():
    markers = {
        1: {"name": "m99"},  # lowercase -- pattern is case-sensitive, ignored
        2: {"name": "M3x"},  # trailing char -- doesn't fully match ^M(\d+)$
        3: {"name": ""},
        4: {},  # no "name" key at all
    }
    assert resolve_driver.ResolveDriver._next_auto_marker_name(markers) == "M01"


def test_next_auto_marker_name_scan_exception_falls_back_to_default_marker_name():
    # A markers object that raises when scanned can't be handled safely --
    # must fall back to DEFAULT_MARKER_NAME, never raise or emit "".
    class _UnscannableMarkers:
        def values(self):
            raise RuntimeError("boom")

    assert (
        resolve_driver.ResolveDriver._next_auto_marker_name(_UnscannableMarkers())
        == resolve_driver.DEFAULT_MARKER_NAME
    )


# ---------------------------------------------------------------------------
# add_marker -- marker_exists vs add_failed (the mislabeling fix)
#
# Existence is checked EXPLICITLY via GetMarkers() BEFORE AddMarker is
# ever called, so "the frame is genuinely occupied" (marker_exists) is
# now distinguishable from "AddMarker rejected the call for some other
# reason" (add_failed) -- previously any AddMarker False was blindly
# reported as "marker_exists", which was a lie whenever the real cause
# was the empty-name bug (frame was EMPTY).
# ---------------------------------------------------------------------------


def test_add_marker_already_exists_returns_graceful_error_without_calling_addmarker():
    # frame_id = cur_frames(10) - start_frame(0) = 10, already present in
    # GetMarkers() -> marker_exists WITHOUT ever calling AddMarker.
    drv = _make_driver(
        "00:00:00:10", "25", start_frame=0, existing_markers={10: {"name": "x"}}
    )
    ok, err, frame, _duration, _fps = drv.add_marker(duration_seconds=1.0, color="Blue")
    assert ok is False
    assert err == "marker_exists"
    # frame is still reported (the frameId this call attempted), not None.
    assert frame == 10
    drv._tl_mock.AddMarker.assert_not_called()


def test_add_marker_addmarker_rejects_on_free_frame_returns_add_failed_not_marker_exists():
    # frame is FREE (GetMarkers() empty) but AddMarker itself still
    # returns False for some other reason -- must be reported as
    # "add_failed", never mislabeled "marker_exists".
    drv = _make_driver(
        "00:00:00:10", "25", start_frame=0, existing_markers={}, add_marker_result=False
    )
    ok, err, frame, _duration, _fps = drv.add_marker(duration_seconds=1.0, color="Blue")
    assert ok is False
    assert err == "add_failed"
    assert err != "marker_exists"
    assert frame == 10
    drv._tl_mock.AddMarker.assert_called_once()


# ---------------------------------------------------------------------------
# add_marker -- shared timeline/fps refusal codes (via _read_timeline_tc_frames)
# ---------------------------------------------------------------------------


def test_add_marker_no_timeline_returns_error():
    drv = _make_driver_no_timeline()
    ok, err, frame, _duration, _fps = drv.add_marker(duration_seconds=1.0, color="Blue")
    assert ok is False
    assert err == "no_timeline"
    assert frame is None


def test_add_marker_drop_frame_timecode_refused():
    drv = _make_driver("00:00:10;00", "29.97", start_frame=0)
    ok, err, frame, _duration, _fps = drv.add_marker(duration_seconds=1.0, color="Blue")
    assert ok is False
    assert err == "drop_frame_unsupported"
    drv._tl_mock.AddMarker.assert_not_called()


def test_add_marker_non_integer_fps_refused():
    drv = _make_driver("00:00:10:00", "23.976", start_frame=0)
    ok, err, frame, _duration, _fps = drv.add_marker(duration_seconds=1.0, color="Blue")
    assert ok is False
    assert err == "non_integer_fps_unsupported"
    drv._tl_mock.AddMarker.assert_not_called()


# ---------------------------------------------------------------------------
# add_marker -- Batch 1 "marker spans the D1-selected clip" (`track` param)
#
# When track is given AND _video_item_on_track(track) resolves a clip,
# frame/duration come from that clip (ignoring duration_seconds) instead
# of the playhead. ANY refusal (no_clip_on_track, or any other error)
# falls back to the pre-existing playhead+duration_seconds behavior --
# the opposite of bump_property/reset_property's hard-failure treatment
# of the identical error code.
# ---------------------------------------------------------------------------


def test_add_marker_track_with_clip_uses_clip_frame_and_duration():
    # cur_tc "00:00:01:00" @ 25fps -> cur_frames = 25 (playhead, unused by
    # the clip branch except to locate which clip is under it). Track 2's
    # clip spans [10, 110) -- frame_id must come from the clip's OWN
    # start (10), NOT the playhead (25), and duration_frames = 110-10=100
    # frames + 1 tail compensation = 101, ignoring duration_seconds entirely.
    item = _make_item_with_range("V2clip", start=10, end=110)
    drv = _make_driver_for_track_marker_ops(
        "00:00:01:00", "25", track_items={2: [item]}, start_frame=0
    )
    ok, err, frame, duration, fps = drv.add_marker(
        duration_seconds=1.0, color="Blue", track=2
    )
    assert ok is True
    assert err is None
    assert frame == 10
    assert duration == 101
    assert fps == 25.0
    args = drv._tl_mock.AddMarker.call_args[0]
    assert args[0] == 10  # frameId
    assert args[4] == 101  # duration (frames)+comp, not 25 (1.0s * 25fps)


def test_add_marker_track_with_clip_and_nonzero_start_frame():
    # Timeline start_frame=5 must be subtracted from the CLIP's start too
    # (same relative-to-timeline-start convention as the playhead path).
    # _video_item_on_track requires the playhead to fall WITHIN the
    # clip's absolute range (start <= cur_frames < end), so cur_tc must
    # land inside [55, 105) -- "00:00:02:10" @ 25fps -> cur_frames=60.
    item = _make_item_with_range("V1clip", start=55, end=105)
    drv = _make_driver_for_track_marker_ops(
        "00:00:02:10", "25", track_items={1: [item]}, start_frame=5
    )
    ok, err, frame, duration, fps = drv.add_marker(
        duration_seconds=1.0, color="Blue", track=1
    )
    assert ok is True
    assert frame == 50  # 55 - 5
    assert duration == 51  # (105 - 55) + tail compensation


def test_add_marker_track_no_clip_on_track_falls_back_to_playhead():
    # Track 3 has no items at all -> _video_item_on_track returns
    # "no_clip_on_track" -> add_marker must fall back to the playhead
    # (cur_frames=25, start_frame=0 -> frame_id=25) + duration_seconds
    # (2.0s * 25fps = 50 frames), exactly as if track had been omitted.
    drv = _make_driver_for_track_marker_ops(
        "00:00:01:00", "25", track_items={3: []}, start_frame=0
    )
    ok, err, frame, duration, fps = drv.add_marker(
        duration_seconds=2.0, color="Blue", track=3
    )
    assert ok is True
    assert err is None
    assert frame == 25
    assert duration == 50
    assert fps == 25.0
    args = drv._tl_mock.AddMarker.call_args[0]
    assert args[0] == 25
    assert args[4] == 50


def test_add_marker_track_other_refusal_falls_back_to_playhead():
    # A refusal OTHER than "no_clip_on_track" (here: GetItemListInTrack
    # itself raises, which _video_item_on_track catches and reports as
    # repr(e), not "no_clip_on_track") must ALSO fall back gracefully --
    # add_marker treats every refusal from _video_item_on_track
    # identically, unlike bump_property/reset_property's hard failure.
    def _raising_item_list(kind, n):
        if n == 4:
            raise RuntimeError("scripting API glitch")
        return []

    drv = _make_driver_for_track_marker_ops(
        "00:00:00:20",
        "25",
        track_items={4: []},
        start_frame=0,
        get_item_list_side_effect=_raising_item_list,
    )
    ok, err, frame, duration, fps = drv.add_marker(
        duration_seconds=1.0, color="Blue", track=4
    )
    assert ok is True
    assert err is None
    assert frame == 20  # playhead fallback: cur_frames(20) - start_frame(0)
    assert duration == 25  # 1.0s * 25fps fallback


def test_add_marker_track_clip_duration_clamped_to_minimum_one_frame():
    # A zero-length clip (GetEnd() == GetStart()) can never actually be
    # "found under the playhead" by _video_item_on_track's own filter
    # (start <= cur_frames < end is an empty range when start==end) --
    # so this test stubs _video_item_on_track directly (bypassing the
    # track-walk/playhead machinery entirely) to exercise add_marker's
    # OWN defensive max(1, end - start) clamp in isolation, the same way
    # a pathological/unexpected Resolve API return would. resize_marker's
    # clamp operates on a resize delta, not initial creation, so it
    # can't be reused here -- this clamp is add_marker's own.
    item = _make_item_with_range("ZeroLen", start=200, end=200)
    drv = _make_driver("00:00:00:00", "25", start_frame=0)
    drv._video_item_on_track = MagicMock(return_value=(item, None))
    ok, err, frame, duration, fps = drv.add_marker(
        duration_seconds=1.0, color="Blue", track=1
    )
    assert ok is True
    assert frame == 200
    assert duration == 1


def test_add_marker_track_marker_already_exists_at_clip_start():
    # A marker already sits at the clip's (relative) start frame -- must
    # report marker_exists WITHOUT ever calling AddMarker, exactly like
    # the playhead path's existing pre-check, just keyed off the clip's
    # frame instead of the playhead's. Playhead must land inside the
    # clip's range [30, 80) for _video_item_on_track to find it --
    # "00:00:02:00" @ 25fps -> cur_frames=50.
    item = _make_item_with_range("V1clip", start=30, end=80)
    drv = _make_driver_for_track_marker_ops(
        "00:00:02:00",
        "25",
        track_items={1: [item]},
        start_frame=0,
        existing_markers={30: {"name": "x"}},
    )
    ok, err, frame, duration, fps = drv.add_marker(
        duration_seconds=1.0, color="Blue", track=1
    )
    assert ok is False
    assert err == "marker_exists"
    assert frame == 30
    assert duration is None
    assert fps is None
    drv._tl_mock.AddMarker.assert_not_called()


def test_add_marker_track_none_byte_identical_to_omitted():
    # track=None (the explicit default) must behave identically to
    # calling add_marker without the track kwarg at all -- no accidental
    # branch is taken just because the parameter was passed explicitly.
    drv = _make_driver("00:00:01:00", "25", start_frame=0)
    ok, err, frame, duration, fps = drv.add_marker(
        duration_seconds=1.0, color="Blue", track=None
    )
    assert ok is True
    assert frame == 25
    assert duration == 25
    assert fps == 25.0


# ---------------------------------------------------------------------------
# sidecar.handle() dispatch
# ---------------------------------------------------------------------------


def test_handle_add_marker_dispatches():
    drv = MagicMock()
    drv.add_marker.return_value = (True, None, 42, 25, 25.0)
    resp = sc.handle(
        {"op": "add_marker", "duration_seconds": 1, "color": "Blue"}, {}, drv
    )
    assert resp["ok"] is True
    assert resp["err"] is None
    assert resp["frame"] == 42
    assert resp["duration"] == 25
    assert resp["fps"] == 25.0
    drv.add_marker.assert_called_once_with(
        duration_seconds=1.0, color="Blue", name=None, note=None, track=None
    )


def test_handle_add_marker_passes_name_and_note():
    drv = MagicMock()
    drv.add_marker.return_value = (True, None, 7, 63, 25.0)
    resp = sc.handle(
        {
            "op": "add_marker",
            "duration_seconds": 2.5,
            "color": "Red",
            "name": "Beat",
            "note": "cue",
        },
        {},
        drv,
    )
    assert resp["ok"] is True
    drv.add_marker.assert_called_once_with(
        duration_seconds=2.5, color="Red", name="Beat", note="cue", track=None
    )


def test_handle_add_marker_defaults_duration_and_color_when_absent():
    drv = MagicMock()
    drv.add_marker.return_value = (True, None, 3, 25, 25.0)
    sc.handle({"op": "add_marker"}, {}, drv)
    drv.add_marker.assert_called_once_with(
        duration_seconds=1.0, color="Blue", name=None, note=None, track=None
    )


def test_handle_add_marker_passes_track_through():
    # Batch 1 "marker spans the D1-selected clip" feature: "track", when
    # present in the request, must reach ResolveDriver.add_marker
    # unmodified -- same pass-through convention already covered for
    # bump_property/reset_property/highlight_clip.
    drv = MagicMock()
    drv.add_marker.return_value = (True, None, 100, 40, 25.0)
    resp = sc.handle(
        {"op": "add_marker", "color": "Blue", "track": 3}, {}, drv
    )
    assert resp["ok"] is True
    assert resp["duration"] == 40
    assert resp["fps"] == 25.0
    drv.add_marker.assert_called_once_with(
        duration_seconds=1.0, color="Blue", name=None, note=None, track=3
    )


def test_handle_add_marker_propagates_marker_exists_error():
    drv = MagicMock()
    drv.add_marker.return_value = (False, "marker_exists", 10, None, None)
    resp = sc.handle({"op": "add_marker", "color": "Blue"}, {}, drv)
    assert resp["ok"] is False
    assert resp["err"] == "marker_exists"
    assert resp["frame"] == 10
    assert resp["duration"] is None
    assert resp["fps"] is None


def test_handle_add_marker_propagates_add_failed_error():
    # sidecar.handle() is a pure passthrough -- verifies the new
    # "add_failed" err string (distinct from "marker_exists") survives
    # the dispatch layer unmodified, whatever ResolveDriver.add_marker
    # returns.
    drv = MagicMock()
    drv.add_marker.return_value = (False, "add_failed", 10, None, None)
    resp = sc.handle({"op": "add_marker", "color": "Blue"}, {}, drv)
    assert resp["ok"] is False
    assert resp["err"] == "add_failed"
    assert resp["frame"] == 10
    assert resp["duration"] is None
    assert resp["fps"] is None
