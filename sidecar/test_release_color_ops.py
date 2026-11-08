"""
Tests for D1 knob-press "release/normalize" sidecar op:
  - ResolveDriver.set_clips_color() -- uniform SetClipColor on every
    OCCUPIED video clip under the playhead (same enumeration as
    clips_at_playhead), plus clearing the D1 highlight-tracking state
    (nothing left to "restore" once every clip is the uniform color).
  - sidecar.handle()'s "set_clips_color" dispatch

This REPLACES the old clear_highlight call inside
engine.dispatchSelectClipRelease (D1 knob-PRESS on the Edit page) --
clear_highlight itself is UNCHANGED and still used by engine.Stop().

Uses the same mock ResolveDriver / mock Timeline+TimelineItem pattern as
test_select_clip_ops.py / test_highlight_clip_ops.py (DaVinciResolveScript
patched out before import so resolve_driver.py's module-level import never
touches the real Blackmagic framework). SetClipColor is ALWAYS a MagicMock
method here -- never called against a live Resolve project, per the hard
hermeticity constraint (SetClipColor mutates a real project).
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
    """A TimelineItem stub exposing GetName/GetStart/GetEnd plus a bare
    MagicMock SetClipColor/GetClipColor -- enough for set_clips_color's
    track-scan + color-set, mirroring test_select_clip_ops.py's helper of
    the same name."""
    item = MagicMock()
    item.GetName.return_value = name
    item.GetStart.return_value = start
    item.GetEnd.return_value = end
    return item


def _make_driver(cur_tc, fps_setting, track_items, highlight_state_path=None):
    """Same shape as test_select_clip_ops.py's _make_driver_for_track_ops,
    plus the highlight-feature attributes (_highlight, highlight_state_path)
    that set_clips_color must clear -- same pattern as
    test_highlight_clip_ops.py's _make_driver_for_highlight."""
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    drv._highlight = None
    drv.highlight_state_path = (
        str(highlight_state_path) if highlight_state_path is not None
        else "/nonexistent/should-not-be-touched.json"
    )
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
    drv._highlight = None
    drv.highlight_state_path = "/nonexistent/should-not-be-touched.json"
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = None
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve
    return drv


# ---------------------------------------------------------------------------
# set_clips_color
# ---------------------------------------------------------------------------


def test_set_clips_color_colors_every_occupied_track():
    item1 = _make_item_with_range("A.mov", 0, 100)
    item3 = _make_item_with_range("C.mov", 0, 50)
    drv = _make_driver("00:00:00:05", "25", {1: [item1], 2: [], 3: [item3]})

    ok, err, count = drv.set_clips_color("Blue")

    assert ok is True
    assert err is None
    assert count == 2
    item1.SetClipColor.assert_called_once_with("Blue")
    item3.SetClipColor.assert_called_once_with("Blue")


def test_set_clips_color_skips_clips_not_under_playhead():
    item = _make_item_with_range("A.mov", 200, 300)
    drv = _make_driver("00:00:00:05", "25", {1: [item]})

    ok, err, count = drv.set_clips_color("Blue")

    assert ok is True
    assert count == 0
    item.SetClipColor.assert_not_called()


def test_set_clips_color_no_occupied_tracks_returns_zero_not_error():
    drv = _make_driver("00:00:00:05", "25", {})
    ok, err, count = drv.set_clips_color("Blue")
    assert ok is True
    assert err is None
    assert count == 0


def test_set_clips_color_no_timeline_returns_error():
    drv = _make_driver_no_timeline()
    ok, err, count = drv.set_clips_color("Blue")
    assert ok is False
    assert err == "no_timeline"
    assert count is None


def test_set_clips_color_drop_frame_timecode_refused():
    drv = _make_driver("00:00:10;00", "29.97", {1: []})
    ok, err, count = drv.set_clips_color("Blue")
    assert ok is False
    assert err == "drop_frame_unsupported"


def test_set_clips_color_clears_inmemory_and_ondisk_highlight_state(tmp_path):
    item = _make_item_with_range("A.mov", 0, 100)
    state_path = tmp_path / "h.json"
    drv = _make_driver("00:00:00:05", "25", {1: [item]}, highlight_state_path=state_path)
    drv._highlight = {"unique_id": "uid-A", "original_color": "Orange", "track": 1}
    state_path.write_text('{"unique_id": "uid-A", "original_color": "Orange", "track": 1}')

    ok, err, count = drv.set_clips_color("Blue")

    assert ok is True
    assert drv._highlight is None
    assert not state_path.exists()


def test_set_clips_color_noop_highlight_state_when_nothing_highlighted(tmp_path):
    item = _make_item_with_range("A.mov", 0, 100)
    state_path = tmp_path / "does-not-exist.json"
    drv = _make_driver("00:00:00:05", "25", {1: [item]}, highlight_state_path=state_path)

    ok, err, count = drv.set_clips_color("Blue")

    assert ok is True
    assert drv._highlight is None
    assert not state_path.exists()


def test_set_clips_color_never_reads_original_color_pure_forward_set():
    """set_clips_color must NOT call GetClipColor -- it's a pure forward
    SetClipColor(color), unlike clear_highlight/highlight_clip which read
    the original color to restore later."""
    item = _make_item_with_range("A.mov", 0, 100)
    drv = _make_driver("00:00:00:05", "25", {1: [item]})

    drv.set_clips_color("Blue")

    item.GetClipColor.assert_not_called()


# ---------------------------------------------------------------------------
# sidecar.handle() dispatch
# ---------------------------------------------------------------------------


def test_handle_set_clips_color_dispatches():
    drv = MagicMock()
    drv.set_clips_color.return_value = (True, None, 3)
    resp = sc.handle({"op": "set_clips_color", "color": "Blue"}, {}, drv)
    assert resp["ok"] is True
    assert resp["err"] is None
    assert resp["count"] == 3
    drv.set_clips_color.assert_called_once_with(color="Blue")


def test_handle_set_clips_color_propagates_error():
    drv = MagicMock()
    drv.set_clips_color.return_value = (False, "no_timeline", None)
    resp = sc.handle({"op": "set_clips_color"}, {}, drv)
    assert resp["ok"] is False
    assert resp["err"] == "no_timeline"
    assert resp["count"] is None
