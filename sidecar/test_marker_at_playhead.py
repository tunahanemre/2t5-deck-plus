"""Tests for ResolveDriver.marker_at_playhead -- the C1 "adopt an existing
marker" lookup.

Without adoption, pressing C1 while parked inside an existing marker either
fails with marker_exists (on its exact start frame) or silently creates an
overlapping duplicate.
"""
from unittest.mock import MagicMock

import resolve_driver


def _driver(markers, cur_frames, tl_start=0, fps="25"):
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    tl = MagicMock()
    tl.GetMarkers.return_value = markers
    tl.GetStartFrame.return_value = tl_start
    tl.GetSetting.return_value = fps
    drv.resolve = MagicMock()
    drv._ensure_connected = lambda: None
    drv._read_timeline_tc_frames = lambda: (tl, cur_frames, float(fps), None)
    drv._tl_mock = tl
    return drv


def test_finds_marker_containing_playhead():
    drv = _driver({9000: {"color": "Blue", "duration": 50, "name": "M01"}}, cur_frames=9020)
    got = drv.marker_at_playhead()
    assert got["ok"] is True
    assert got["found"] is True
    assert got["frame"] == 9000
    assert got["color"] == "Blue"


def test_start_frame_is_inclusive():
    drv = _driver({9000: {"color": "Blue", "duration": 50}}, cur_frames=9000)
    assert drv.marker_at_playhead()["found"] is True


def test_end_frame_is_exclusive():
    # [9000, 9050) -- frame 9050 is NOT inside.
    drv = _driver({9000: {"color": "Blue", "duration": 50}}, cur_frames=9050)
    assert drv.marker_at_playhead()["found"] is False


def test_miss_outside_all_ranges():
    drv = _driver({9000: {"color": "Blue", "duration": 10}}, cur_frames=9500)
    assert drv.marker_at_playhead()["found"] is False


def test_overlap_latest_start_wins():
    drv = _driver({
        9000: {"color": "Blue", "duration": 200},
        9050: {"color": "Red", "duration": 100},
    }, cur_frames=9060)
    assert drv.marker_at_playhead()["frame"] == 9050


def test_no_markers_is_a_clean_miss():
    drv = _driver({}, cur_frames=9000)
    got = drv.marker_at_playhead()
    assert got["ok"] is True and got["found"] is False


def test_getmarkers_none_is_a_clean_miss():
    drv = _driver(None, cur_frames=9000)
    got = drv.marker_at_playhead()
    assert got["ok"] is True and got["found"] is False


def test_timeline_start_frame_offset_is_applied():
    # Timeline starts at 01:00:00:00 == 90000; a marker at frameId 100 is at
    # absolute frame 90100.
    drv = _driver({100: {"color": "Green", "duration": 25}}, cur_frames=90110, tl_start=90000)
    got = drv.marker_at_playhead()
    assert got["found"] is True
    assert got["frame"] == 100


def test_duration_returned_in_seconds():
    drv = _driver({9000: {"color": "Blue", "duration": 50}}, cur_frames=9010)
    got = drv.marker_at_playhead()
    assert abs(got["duration"] - 2.0) < 1e-6   # 50 frames @ 25fps
    assert got["fps"] == 25.0


def test_read_failure_is_a_clean_miss_not_a_crash():
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    drv.resolve = MagicMock()
    drv._ensure_connected = lambda: None
    drv._read_timeline_tc_frames = lambda: (None, None, None, "no_timeline")
    got = drv.marker_at_playhead()
    assert got["ok"] is True and got["found"] is False
    # Critically: the shared handle must NOT be nulled by a read failure.
    assert drv.resolve is not None


def test_absolute_range_accounts_for_timeline_start_frame():
    """The engine compares the playhead (ABSOLUTE) against the marker range.

    GetMarkers keys are relative to Timeline.GetStartFrame(), so the driver
    must hand back an absolute range or auto-exit fires constantly on any
    timeline starting at 01:00:00:00.
    """
    drv = _driver({100: {"color": "Green", "duration": 25}}, cur_frames=90110, tl_start=90000)
    got = drv.marker_at_playhead()
    assert got["frame"] == 100          # relative, as Resolve stores it
    assert got["abs_start"] == 90100    # absolute, as the playhead is counted
    assert got["abs_end"] == 90125
    assert got["abs_start"] <= 90110 < got["abs_end"]
