"""
Tests for the C1 "marker mode" feature's resize/recolor sidecar ops
(create was already shipped -- see test_add_marker_ops.py):

  - ResolveDriver.resize_marker(frame, delta_frames) -- extends/shrinks
    the duration of the marker AT `frame` (the marker's start position
    never changes -- Resolve has no marker-update API, so this is
    GetMarkers -> DeleteMarkerAtFrame -> AddMarker with the same
    color/name/note/customData and a new duration, clamped to a minimum
    of 1 frame).
  - ResolveDriver.set_marker_color(frame, color) -- same delete+re-add
    shape, changing only color (duration/name/note/customData
    preserved).
  - ResolveDriver.delete_marker(frame) -- 2026-08-12, the P-button
    same-colour-press toggle (see engine.go's dispatchMarkerAtPlayhead):
    removes the marker AT `frame` entirely via
    Timeline.DeleteMarkerAtFrame, with NO re-add -- the one op in this
    file that does not follow the delete+re-add "fake update" shape.
  - sidecar.handle()'s "resize_marker"/"set_marker_color"/"delete_marker"
    dispatch.

*** LIVE-VERIFICATION FLAG ***: GetMarkers()'s returned dict is
documented to key markers by integer frameId, but this has NOT been
exercised against a real Resolve timeline in this hermetic suite (same
hard hermeticity constraint as test_add_marker_ops.py -- these calls
mutate a real project). _find_marker()'s string-key fallback is a
defensive guess, not a confirmed requirement -- verify against a real
GetMarkers() result before trusting the int-key assumption in
production.

Uses the same mock ResolveDriver / mock Timeline pattern as
test_add_marker_ops.py -- DaVinciResolveScript patched out before
import so resolve_driver.py's module-level import never touches the
real Blackmagic framework. GetMarkers/DeleteMarkerAtFrame/AddMarker are
ALWAYS MagicMock methods here -- never called against a live Resolve
project.
"""
import os
import sys
from unittest.mock import MagicMock


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


def _make_driver_with_markers(markers, delete_result=True, add_result=True, fps="25"):
    """Timeline mock whose GetMarkers()/DeleteMarkerAtFrame()/AddMarker()
    are all controllable -- no _read_timeline_tc_frames() involvement
    (resize_marker/set_marker_color resolve a marker by an EXPLICIT
    frame the caller already knows, not the current playhead position,
    so no GetCurrentTimecode()/drop-frame refusal logic applies here)."""
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    tl = MagicMock()
    tl.GetMarkers.return_value = markers
    tl.DeleteMarkerAtFrame.return_value = delete_result
    tl.AddMarker.return_value = add_result
    tl.GetSetting.return_value = fps
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


_MARKER_AT_25 = {
    25: {"color": "Blue", "duration": 25, "name": "", "note": "", "customData": ""}
}


# ---------------------------------------------------------------------------
# resize_marker -- duration math (extend/shrink/clamp)
# ---------------------------------------------------------------------------


def test_resize_marker_extends_duration_positive_delta():
    drv = _make_driver_with_markers(dict(_MARKER_AT_25))
    ok, err, duration, fps = drv.resize_marker(frame=25, delta_frames=5)
    assert ok is True
    assert err is None
    assert duration == 30
    assert fps == 25.0


def test_resize_marker_shrinks_duration_negative_delta():
    drv = _make_driver_with_markers(dict(_MARKER_AT_25))
    ok, err, duration, fps = drv.resize_marker(frame=25, delta_frames=-10)
    assert ok is True
    assert duration == 15


def test_resize_marker_clamps_to_minimum_one_frame():
    drv = _make_driver_with_markers(dict(_MARKER_AT_25))
    ok, err, duration, fps = drv.resize_marker(frame=25, delta_frames=-1000)
    assert ok is True
    assert duration == 1


def test_resize_marker_preserves_color_name_note_customdata():
    markers = {
        25: {"color": "Red", "duration": 25, "name": "Beat", "note": "cue", "customData": "xyz"}
    }
    drv = _make_driver_with_markers(markers)
    drv.resize_marker(frame=25, delta_frames=5)
    args = drv._tl_mock.AddMarker.call_args[0]
    # AddMarker(frameId, color, name, note, duration, customData)
    assert args[0] == 25
    assert args[1] == "Red"
    assert args[2] == "Beat"
    assert args[3] == "cue"
    assert args[4] == 30
    assert args[5] == "xyz"


def test_resize_marker_deletes_before_readding_at_same_frame():
    drv = _make_driver_with_markers(dict(_MARKER_AT_25))
    drv.resize_marker(frame=25, delta_frames=5)
    drv._tl_mock.DeleteMarkerAtFrame.assert_called_once_with(25)
    drv._tl_mock.AddMarker.assert_called_once()


def test_resize_marker_empty_name_falls_back_to_default_marker_name():
    # *** CONFIRMED LIVE, Resolve 21.0.0.47 ***: AddMarker rejects an
    # empty-string name. _MARKER_AT_25 was read back with name="" (e.g.
    # a marker created before this fix, or one whose name was never
    # set) -- re-adding it verbatim with "" would turn a legitimate
    # resize into a spurious add_marker_failed. Must fall back to
    # DEFAULT_MARKER_NAME instead.
    drv = _make_driver_with_markers(dict(_MARKER_AT_25))
    ok, err, duration, fps = drv.resize_marker(frame=25, delta_frames=5)
    args = drv._tl_mock.AddMarker.call_args[0]
    assert args[2] == resolve_driver.DEFAULT_MARKER_NAME
    assert args[2] != ""
    assert ok is True
    assert err is None


def test_resize_marker_returns_fps_generically_not_hardcoded():
    drv = _make_driver_with_markers(dict(_MARKER_AT_25), fps="30")
    ok, err, duration, fps = drv.resize_marker(frame=25, delta_frames=5)
    assert fps == 30.0


# ---------------------------------------------------------------------------
# resize_marker -- graceful failures
# ---------------------------------------------------------------------------


def test_resize_marker_not_found_returns_graceful_error():
    drv = _make_driver_with_markers({})
    ok, err, duration, fps = drv.resize_marker(frame=25, delta_frames=5)
    assert ok is False
    assert err == "marker_not_found"
    assert duration is None
    assert fps is None
    drv._tl_mock.DeleteMarkerAtFrame.assert_not_called()


def test_resize_marker_delete_failed_returns_graceful_error():
    drv = _make_driver_with_markers(dict(_MARKER_AT_25), delete_result=False)
    ok, err, duration, fps = drv.resize_marker(frame=25, delta_frames=5)
    assert ok is False
    assert err == "delete_failed"
    drv._tl_mock.AddMarker.assert_not_called()


def test_resize_marker_add_failed_returns_graceful_error():
    drv = _make_driver_with_markers(dict(_MARKER_AT_25), add_result=False)
    ok, err, duration, fps = drv.resize_marker(frame=25, delta_frames=5)
    assert ok is False
    assert err == "add_marker_failed"


def test_resize_marker_no_timeline_returns_error():
    drv = _make_driver_no_timeline()
    ok, err, duration, fps = drv.resize_marker(frame=25, delta_frames=5)
    assert ok is False
    assert err == "no_timeline"
    assert duration is None


# ---------------------------------------------------------------------------
# set_marker_color -- color changes, duration/name/note preserved
# ---------------------------------------------------------------------------


def test_set_marker_color_changes_color_preserves_rest():
    markers = {
        25: {"color": "Blue", "duration": 30, "name": "Beat", "note": "cue", "customData": "xyz"}
    }
    drv = _make_driver_with_markers(markers)
    ok, err = drv.set_marker_color(frame=25, color="Red")
    assert ok is True
    assert err is None
    args = drv._tl_mock.AddMarker.call_args[0]
    assert args[0] == 25
    assert args[1] == "Red"
    assert args[2] == "Beat"
    assert args[3] == "cue"
    assert args[4] == 30
    assert args[5] == "xyz"


def test_set_marker_color_deletes_before_readding():
    drv = _make_driver_with_markers(dict(_MARKER_AT_25))
    drv.set_marker_color(frame=25, color="Green")
    drv._tl_mock.DeleteMarkerAtFrame.assert_called_once_with(25)
    drv._tl_mock.AddMarker.assert_called_once()


def test_set_marker_color_empty_name_falls_back_to_default_marker_name():
    # Same Resolve-21 empty-name gotcha as resize_marker above.
    drv = _make_driver_with_markers(dict(_MARKER_AT_25))
    ok, err = drv.set_marker_color(frame=25, color="Green")
    args = drv._tl_mock.AddMarker.call_args[0]
    assert args[2] == resolve_driver.DEFAULT_MARKER_NAME
    assert args[2] != ""
    assert ok is True
    assert err is None


def test_set_marker_color_not_found_returns_graceful_error():
    drv = _make_driver_with_markers({})
    ok, err = drv.set_marker_color(frame=25, color="Red")
    assert ok is False
    assert err == "marker_not_found"


def test_set_marker_color_delete_failed_returns_graceful_error():
    drv = _make_driver_with_markers(dict(_MARKER_AT_25), delete_result=False)
    ok, err = drv.set_marker_color(frame=25, color="Red")
    assert ok is False
    assert err == "delete_failed"


def test_set_marker_color_add_failed_returns_graceful_error():
    drv = _make_driver_with_markers(dict(_MARKER_AT_25), add_result=False)
    ok, err = drv.set_marker_color(frame=25, color="Red")
    assert ok is False
    assert err == "add_marker_failed"


def test_set_marker_color_no_timeline_returns_error():
    drv = _make_driver_no_timeline()
    ok, err = drv.set_marker_color(frame=25, color="Red")
    assert ok is False
    assert err == "no_timeline"


# ---------------------------------------------------------------------------
# delete_marker -- genuine delete, no re-add (P-button same-colour toggle)
# ---------------------------------------------------------------------------


def test_delete_marker_deletes_at_frame():
    drv = _make_driver_with_markers(dict(_MARKER_AT_25))
    ok, err = drv.delete_marker(frame=25)
    assert ok is True
    assert err is None
    drv._tl_mock.DeleteMarkerAtFrame.assert_called_once_with(25)


def test_delete_marker_never_re_adds():
    # The one op in this file that does NOT delete-then-re-add -- a
    # genuine removal, unlike resize_marker/set_marker_color's "fake
    # update" shape.
    drv = _make_driver_with_markers(dict(_MARKER_AT_25))
    drv.delete_marker(frame=25)
    drv._tl_mock.AddMarker.assert_not_called()


def test_delete_marker_not_found_returns_graceful_error():
    drv = _make_driver_with_markers({})
    ok, err = drv.delete_marker(frame=25)
    assert ok is False
    assert err == "marker_not_found"
    drv._tl_mock.DeleteMarkerAtFrame.assert_not_called()


def test_delete_marker_delete_failed_returns_graceful_error():
    drv = _make_driver_with_markers(dict(_MARKER_AT_25), delete_result=False)
    ok, err = drv.delete_marker(frame=25)
    assert ok is False
    assert err == "delete_failed"


def test_delete_marker_no_timeline_returns_error():
    drv = _make_driver_no_timeline()
    ok, err = drv.delete_marker(frame=25)
    assert ok is False
    assert err == "no_timeline"


# ---------------------------------------------------------------------------
# sidecar.handle() dispatch
# ---------------------------------------------------------------------------


def test_handle_resize_marker_dispatches():
    drv = MagicMock()
    drv.resize_marker.return_value = (True, None, 30, 25.0)
    resp = sc.handle(
        {"op": "resize_marker", "frame": 25, "delta_frames": 5}, {}, drv
    )
    assert resp["ok"] is True
    assert resp["err"] is None
    assert resp["frame"] == 25
    assert resp["duration"] == 30
    assert resp["fps"] == 25.0
    drv.resize_marker.assert_called_once_with(frame=25, delta_frames=5)


def test_handle_resize_marker_defaults_delta_to_zero_when_absent():
    drv = MagicMock()
    drv.resize_marker.return_value = (True, None, 25, 25.0)
    sc.handle({"op": "resize_marker", "frame": 25}, {}, drv)
    drv.resize_marker.assert_called_once_with(frame=25, delta_frames=0)


def test_handle_resize_marker_propagates_marker_not_found_error():
    drv = MagicMock()
    drv.resize_marker.return_value = (False, "marker_not_found", None, None)
    resp = sc.handle(
        {"op": "resize_marker", "frame": 99, "delta_frames": 5}, {}, drv
    )
    assert resp["ok"] is False
    assert resp["err"] == "marker_not_found"


def test_handle_set_marker_color_dispatches():
    drv = MagicMock()
    drv.set_marker_color.return_value = (True, None)
    resp = sc.handle(
        {"op": "set_marker_color", "frame": 25, "color": "Red"}, {}, drv
    )
    assert resp["ok"] is True
    assert resp["err"] is None
    assert resp["frame"] == 25
    drv.set_marker_color.assert_called_once_with(frame=25, color="Red")


def test_handle_set_marker_color_propagates_error():
    drv = MagicMock()
    drv.set_marker_color.return_value = (False, "marker_not_found")
    resp = sc.handle(
        {"op": "set_marker_color", "frame": 25, "color": "Red"}, {}, drv
    )
    assert resp["ok"] is False
    assert resp["err"] == "marker_not_found"


def test_handle_delete_marker_dispatches():
    drv = MagicMock()
    drv.delete_marker.return_value = (True, None)
    resp = sc.handle({"op": "delete_marker", "frame": 25}, {}, drv)
    assert resp["ok"] is True
    assert resp["err"] is None
    assert resp["frame"] == 25
    drv.delete_marker.assert_called_once_with(frame=25)


def test_handle_delete_marker_propagates_error():
    drv = MagicMock()
    drv.delete_marker.return_value = (False, "marker_not_found")
    resp = sc.handle({"op": "delete_marker", "frame": 99}, {}, drv)
    assert resp["ok"] is False
    assert resp["err"] == "marker_not_found"
