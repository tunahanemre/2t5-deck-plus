"""
Tests for the D1 "clip-select" feature's sidecar ops:
  - ResolveDriver.clips_at_playhead()   -- occupied video tracks under the playhead
  - ResolveDriver._video_item_on_track() -- resolve a SPECIFIC track's clip
  - bump_property()/reset_property()'s new optional `track` param
  - sidecar.handle()'s "clips_at_playhead" dispatch

Uses the same mock ResolveDriver / mock Timeline+TimelineItem pattern as
test_edit_page_ops.py (DaVinciResolveScript patched out before import so
resolve_driver.py's module-level import never touches the real Blackmagic
framework).
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


def _make_item_with_range(name, start, end):
    """A TimelineItem stub exposing GetName/GetStart/GetEnd only (no
    property get/set) -- enough for clips_at_playhead/_video_item_on_track
    track-scanning tests."""
    item = MagicMock()
    item.GetName.return_value = name
    item.GetStart.return_value = start
    item.GetEnd.return_value = end
    return item


def _make_item_full(props, start, end, name="clip"):
    """A TimelineItem stub with BOTH the property get/set behavior (from
    test_edit_page_ops.py's _make_item) AND GetName/GetStart/GetEnd --
    needed for bump_property/reset_property(track=...) tests, which read
    a track's clip via _video_item_on_track then mutate its properties."""
    item = MagicMock()
    item.GetProperty.side_effect = lambda n: props.get(n)

    def _set(n, val):
        props[n] = val
        return True

    item.SetProperty.side_effect = _set
    item.GetName.return_value = name
    item.GetStart.return_value = start
    item.GetEnd.return_value = end
    return item


def _make_driver_for_track_ops(cur_tc, fps_setting, track_items):
    """track_items: dict[int, list[item]] -- video track number (1-based) ->
    items on that track, as GetItemListInTrack("video", n) would return.
    GetTrackCount("video") is derived as max(track_items) (or 0 if empty)."""
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
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


# ---------------------------------------------------------------------------
# clips_at_playhead
# ---------------------------------------------------------------------------


def test_clips_at_playhead_returns_occupied_tracks_only():
    # Playhead at 00:00:00:05 @25fps = frame 5.
    item1 = _make_item_with_range("A.mov", 0, 100)
    item3 = _make_item_with_range("C.mov", 0, 50)
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {1: [item1], 2: [], 3: [item3]})

    ok, err, clips = drv.clips_at_playhead()

    assert ok is True
    assert err is None
    assert clips == [{"track": 1, "name": "A.mov"}, {"track": 3, "name": "C.mov"}]


def test_clips_at_playhead_skips_clips_not_under_playhead():
    # item's range does not contain frame 5.
    item = _make_item_with_range("A.mov", 200, 300)
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {1: [item]})

    ok, err, clips = drv.clips_at_playhead()

    assert ok is True
    assert clips == []


def test_clips_at_playhead_name_defaults_to_empty_string():
    item = MagicMock()
    item.GetName.return_value = None
    item.GetStart.return_value = 0
    item.GetEnd.return_value = 100
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {1: [item]})

    ok, err, clips = drv.clips_at_playhead()

    assert ok is True
    assert clips == [{"track": 1, "name": ""}]


def test_clips_at_playhead_preserves_track_ascending_order():
    item2 = _make_item_with_range("B.mov", 0, 100)
    item5 = _make_item_with_range("E.mov", 0, 100)
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {5: [item5], 2: [item2]})

    ok, err, clips = drv.clips_at_playhead()

    assert ok is True
    assert [c["track"] for c in clips] == [2, 5]


def test_clips_at_playhead_no_timeline_returns_error():
    drv = _make_driver_no_timeline()
    ok, err, clips = drv.clips_at_playhead()
    assert ok is False
    assert err == "no_timeline"
    assert clips is None


def test_clips_at_playhead_drop_frame_timecode_refused():
    drv = _make_driver_for_track_ops("00:00:10;00", "29.97", {1: []})
    ok, err, clips = drv.clips_at_playhead()
    assert ok is False
    assert err == "drop_frame_unsupported"


def test_clips_at_playhead_no_video_tracks_returns_empty_list():
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {})
    ok, err, clips = drv.clips_at_playhead()
    assert ok is True
    assert clips == []


def test_handle_clips_at_playhead_dispatches():
    drv = MagicMock()
    drv.clips_at_playhead.return_value = (True, None, [{"track": 1, "name": "A.mov"}])
    resp = sc.handle({"op": "clips_at_playhead"}, {}, drv)
    assert resp["ok"] is True
    assert resp["clips"] == [{"track": 1, "name": "A.mov"}]
    drv.clips_at_playhead.assert_called_once_with()


def test_handle_clips_at_playhead_propagates_error():
    drv = MagicMock()
    drv.clips_at_playhead.return_value = (False, "no_timeline", None)
    resp = sc.handle({"op": "clips_at_playhead"}, {}, drv)
    assert resp["ok"] is False
    assert resp["err"] == "no_timeline"
    assert resp["clips"] is None


# ---------------------------------------------------------------------------
# _video_item_on_track
# ---------------------------------------------------------------------------


def test_video_item_on_track_returns_item_when_playhead_in_range():
    item = _make_item_with_range("A.mov", 0, 100)
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {2: [item]})

    got, err = drv._video_item_on_track(2)

    assert got is item
    assert err is None


def test_video_item_on_track_empty_track_returns_no_clip_on_track():
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {2: []})

    got, err = drv._video_item_on_track(2)

    assert got is None
    assert err == "no_clip_on_track"


def test_video_item_on_track_playhead_outside_range_returns_no_clip_on_track():
    item = _make_item_with_range("A.mov", 200, 300)
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {2: [item]})

    got, err = drv._video_item_on_track(2)

    assert got is None
    assert err == "no_clip_on_track"


def test_video_item_on_track_no_timeline_returns_error():
    drv = _make_driver_no_timeline()
    got, err = drv._video_item_on_track(2)
    assert got is None
    assert err == "no_timeline"


def test_video_item_on_track_drop_frame_refused():
    drv = _make_driver_for_track_ops("00:00:10;00", "29.97", {2: []})
    got, err = drv._video_item_on_track(2)
    assert got is None
    assert err == "drop_frame_unsupported"


# ---------------------------------------------------------------------------
# bump_property(track=...)
# ---------------------------------------------------------------------------


def test_bump_property_with_track_targets_that_tracks_clip():
    item1 = _make_item_full({"Pan": 0.0}, 0, 100, "A.mov")
    item2 = _make_item_full({"Pan": 50.0}, 0, 100, "B.mov")
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {1: [item1], 2: [item2]})

    ok, err, values = drv.bump_property(property_name="Pan", delta=5.0, track=2)

    assert ok is True
    assert err is None
    assert values == {"Pan": 55.0}
    item2.SetProperty.assert_called_once_with("Pan", 55.0)
    item1.SetProperty.assert_not_called()


def test_bump_property_with_track_no_clip_on_that_track_errors():
    item1 = _make_item_full({"Pan": 0.0}, 0, 100, "A.mov")
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {1: [item1], 2: []})

    ok, err, values = drv.bump_property(property_name="Pan", delta=5.0, track=2)

    assert ok is False
    assert err == "no_clip_on_track"
    assert values is None
    item1.SetProperty.assert_not_called()


def test_bump_property_track_none_uses_top_occupied_clip():
    # FIXED 2026-07-08: track=None (no active D1 override) resolves via
    # the TOPMOST-OCCUPIED video track's clip -- NOT GetCurrentVideoItem(),
    # which is target-track-dependent and unreliable (returns None
    # whenever Resolve's own "current" track is empty, even while other
    # tracks plainly have clips at the playhead -- see resolve_driver.py's
    # _target_item/current_video_item_id doc comments). Two occupied
    # tracks (1 and 3) at the playhead -- the HIGHER track (3) must be
    # the one mutated, never the lower one.
    item1 = _make_item_full({"Pan": 0.0}, 0, 100, "A.mov")
    item3 = _make_item_full({"Pan": 10.0}, 0, 100, "C.mov")
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {1: [item1], 3: [item3]})

    ok, err, values = drv.bump_property(property_name="Pan", delta=2.5, track=None)

    assert ok is True
    assert values == {"Pan": 12.5}
    item3.SetProperty.assert_called_once_with("Pan", 12.5)
    item1.SetProperty.assert_not_called()


# ---------------------------------------------------------------------------
# reset_property(track=...)
# ---------------------------------------------------------------------------


def test_reset_property_with_track_targets_that_tracks_clip():
    item1 = _make_item_full({"Pan": 37.5}, 0, 100, "A.mov")
    item2 = _make_item_full({"CropTop": 42.0}, 0, 100, "B.mov")
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {1: [item1], 2: [item2]})

    ok, err, values = drv.reset_property(property_name="CropTop", track=2)

    assert ok is True
    assert err is None
    assert values == {"CropTop": 0.0}
    item2.SetProperty.assert_called_once_with("CropTop", 0.0)
    item1.SetProperty.assert_not_called()


def test_reset_property_with_track_no_clip_on_that_track_errors():
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {2: []})

    ok, err, values = drv.reset_property(property_name="Pan", track=2)

    assert ok is False
    assert err == "no_clip_on_track"
    assert values is None


def test_reset_property_track_none_uses_top_occupied_clip():
    # Same top-occupied contract as bump_property above.
    item1 = _make_item_full({"CropTop": 5.0}, 0, 100, "A.mov")
    item3 = _make_item_full({"Pan": 37.5}, 0, 100, "C.mov")
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {1: [item1], 3: [item3]})

    ok, err, values = drv.reset_property(property_name="Pan", track=None)

    assert ok is True
    assert values == {"Pan": 0.0}
    item3.SetProperty.assert_called_once_with("Pan", 0.0)
    item1.SetProperty.assert_not_called()


# ---------------------------------------------------------------------------
# _target_item(track=...) -- the shared "D1 override vs. default" resolver
# behind bump_property/reset_property. FIXED 2026-07-08: when track is
# None/absent -- i.e. no active D1 track override -- targets the TOPMOST
# OCCUPIED video clip at the playhead via _occupied_video_items() (the
# same top-down scan behind clips_at_playhead()), NOT GetCurrentVideoItem()
# (unreliable -- see the method's doc comment). Targets that specific
# track's clip via _video_item_on_track() when a track override IS given.
# Exercised indirectly (through bump_property/reset_property) above; these
# tests exercise it directly, including the "no occupied track" fallback.
# ---------------------------------------------------------------------------


def test_target_item_no_track_uses_top_occupied_clip():
    # Several tracks occupied at the playhead -- must return the TOP one
    # (highest track number), never a lower one, and must actually walk
    # the tracks (this is the whole point of the fix).
    item1 = _make_item_with_range("A.mov", 0, 100)
    item2 = _make_item_with_range("B.mov", 0, 100)
    item4 = _make_item_with_range("D.mov", 0, 100)
    drv = _make_driver_for_track_ops(
        "00:00:00:05", "25", {1: [item1], 2: [item2], 4: [item4]}
    )

    got_item, err = drv._target_item(track=None)

    assert got_item is item4
    assert err is None


def test_target_item_no_track_returns_second_from_top_when_top_track_empty():
    # The bug this fix closes: the top-numbered track (3) is EMPTY at the
    # playhead while tracks 1 and 2 are occupied -- must still resolve to
    # track 2's clip (the topmost OCCUPIED one), not "no_clip" (which is
    # what a Resolve-current-track-scoped GetCurrentVideoItem() could
    # wrongly report if track 3 happened to be Resolve's own "current").
    item1 = _make_item_with_range("A.mov", 0, 100)
    item2 = _make_item_with_range("B.mov", 0, 100)
    drv = _make_driver_for_track_ops(
        "00:00:00:05", "25", {1: [item1], 2: [item2], 3: []}
    )

    got_item, err = drv._target_item(track=None)

    assert got_item is item2
    assert err is None


def test_target_item_no_track_no_occupied_track_returns_no_clip_error():
    """Genuine gap: NO video track has a clip at the playhead -- track=None
    must report "no_clip", never silently pick some other frame's clip."""
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {1: [], 2: []})

    got_item, err = drv._target_item(track=None)

    assert got_item is None
    assert err == "no_clip"


def test_target_item_no_track_no_timeline_propagates_refusal_code():
    drv = _make_driver_no_timeline()

    got_item, err = drv._target_item(track=None)

    assert got_item is None
    assert err == "no_timeline"


def test_target_item_with_track_targets_that_tracks_clip():
    item1 = _make_item_full({"Pan": 0.0}, 0, 100, "A.mov")
    item2 = _make_item_full({"Pan": 50.0}, 0, 100, "B.mov")
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {1: [item1], 2: [item2]})

    got_item, err = drv._target_item(track=2)

    assert got_item is item2
    assert err is None


def test_target_item_with_track_no_clip_on_that_track_errors():
    drv = _make_driver_for_track_ops("00:00:00:05", "25", {2: []})

    got_item, err = drv._target_item(track=2)

    assert got_item is None
    assert err == "no_clip_on_track"
