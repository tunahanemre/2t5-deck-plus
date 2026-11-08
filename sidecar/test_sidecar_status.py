"""
Tests for the sidecar 'status' op (Task 6, v2).

Uses a mock ResolveDriver so Resolve does not need to be running.
The handle() function from sidecar.py is tested directly (no socket I/O).
"""
import sys
import os
import importlib
from unittest.mock import MagicMock, patch

# ---------------------------------------------------------------------------
# Import the handle() function from sidecar.py without executing main().
# We patch away DaVinciResolveScript so no real Resolve API is imported.
# ---------------------------------------------------------------------------

def _import_sidecar_handle():
    """Return the handle function from sidecar.py with Resolve mocked out."""
    # Ensure sidecar/ is on sys.path
    sidecar_dir = os.path.dirname(os.path.abspath(__file__))
    if sidecar_dir not in sys.path:
        sys.path.insert(0, sidecar_dir)

    # Provide a stub for DaVinciResolveScript before importing resolve_driver
    dvr_stub = MagicMock()
    dvr_stub.scriptapp.return_value = MagicMock()
    sys.modules.setdefault("DaVinciResolveScript", dvr_stub)

    import sidecar as sc
    return sc.handle


handle = _import_sidecar_handle()


# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

def _make_driver(page, node=None, timecode=None, timeline_name=None, clip_id=None,
                  playhead_frame=None, edit_clip=None):
    """Return a mock ResolveDriver whose current_page() returns `page`,
    current_node() returns `node`, current_timecode() returns
    (timecode, timeline_name), current_video_item_id() returns
    `clip_id` (default None, matching the "no current clip" case),
    current_playhead_frame() returns `playhead_frame` (default None,
    matching the "no live timeline" case), and edit_clip_info() returns
    `edit_clip` (default None, matching the "no target clip" case)."""
    drv = MagicMock()
    drv.current_page.return_value = page
    drv.current_node.return_value = node
    drv.current_timecode.return_value = (timecode, timeline_name)
    drv.current_video_item_id.return_value = clip_id
    drv.current_playhead_frame.return_value = playhead_frame
    drv.edit_clip_info.return_value = edit_clip
    return drv


# ---------------------------------------------------------------------------
# Tests
# ---------------------------------------------------------------------------

def test_status_returns_page_color():
    drv = _make_driver("color")
    resp = handle({"op": "status"}, {}, drv)
    assert resp["ok"] is True
    assert resp["page"] == "color"


def test_status_returns_page_edit():
    drv = _make_driver("edit")
    resp = handle({"op": "status"}, {}, drv)
    assert resp["ok"] is True
    assert resp["page"] == "edit"


def test_status_returns_none_when_resolve_unavailable():
    drv = _make_driver(None)
    resp = handle({"op": "status"}, {}, drv)
    assert resp["ok"] is True
    assert resp["page"] is None


def test_status_includes_current_node_key():
    """status response must always include 'current_node' key."""
    drv = _make_driver("color", node={"name": "Node 1", "index": 1})
    resp = handle({"op": "status"}, {}, drv)
    assert "current_node" in resp


def test_status_current_node_none_when_unavailable():
    drv = _make_driver("color", node=None)
    resp = handle({"op": "status"}, {}, drv)
    assert resp["current_node"] is None


def test_status_current_node_name_and_index():
    drv = _make_driver("color", node={"name": "CST Node", "index": 3})
    resp = handle({"op": "status"}, {}, drv)
    assert resp["current_node"]["name"] == "CST Node"
    assert resp["current_node"]["index"] == 3


def test_ping_still_works():
    drv = _make_driver("color")
    resp = handle({"op": "ping"}, {}, drv)
    assert resp == {"ok": True}


def test_unknown_op_returns_error():
    drv = _make_driver("color")
    resp = handle({"op": "bogus"}, {}, drv)
    assert resp.get("ok") is False
    assert "err" in resp


# ---------------------------------------------------------------------------
# HUD v2 (docs/specs/2026-07-06-ld-resolve-hud-rich.md): "status" op grows
# "timecode"/"timeline_name", read via ResolveDriver.current_timecode().
# ---------------------------------------------------------------------------

def test_status_includes_timecode_and_timeline_name():
    drv = _make_driver("edit", timecode="01:04:22:11", timeline_name="EP10-v0")
    resp = handle({"op": "status"}, {}, drv)
    assert resp["ok"] is True
    assert resp["timecode"] == "01:04:22:11"
    assert resp["timeline_name"] == "EP10-v0"


def test_status_timecode_and_timeline_name_none_when_no_timeline():
    """Graceful fallback: no live timeline -- current_timecode() returns
    (None, None), and the status response must carry that through as-is
    (never raise, never substitute a fake value)."""
    drv = _make_driver("color", timecode=None, timeline_name=None)
    resp = handle({"op": "status"}, {}, drv)
    assert resp["ok"] is True
    assert resp["timecode"] is None
    assert resp["timeline_name"] is None


def test_status_current_timecode_called_once():
    """The status handler must call current_timecode() exactly once (not
    once per field) -- it returns both values in a single tuple."""
    drv = _make_driver("edit", timecode="00:00:01:00", timeline_name="T1")
    handle({"op": "status"}, {}, drv)
    drv.current_timecode.assert_called_once()


# ---------------------------------------------------------------------------
# D1 auto-reset: "status" op grows "clip_id", read via
# ResolveDriver.current_video_item_id() -- the Go engine (engine.go's drain
# loop) compares this tick-over-tick to detect a playhead move to a
# different clip and auto-clear the D1 selectClip override.
# ---------------------------------------------------------------------------

def test_status_includes_clip_id():
    drv = _make_driver("edit", clip_id="uid-abc123")
    resp = handle({"op": "status"}, {}, drv)
    assert resp["ok"] is True
    assert resp["clip_id"] == "uid-abc123"


def test_status_clip_id_none_when_no_current_clip():
    """Graceful fallback: no current video item -- current_video_item_id()
    returns None, and the status response must carry that through as-is
    (never raise, never substitute a fake value)."""
    drv = _make_driver("color", clip_id=None)
    resp = handle({"op": "status"}, {}, drv)
    assert resp["ok"] is True
    assert resp["clip_id"] is None


def test_status_current_video_item_id_called_once():
    drv = _make_driver("edit", clip_id="uid-xyz")
    handle({"op": "status"}, {}, drv)
    drv.current_video_item_id.assert_called_once()


# ---------------------------------------------------------------------------
# Direction-button clip-selection auto-reset: "status" op grows
# "playhead_frame", read via ResolveDriver.current_playhead_frame() -- the
# Go engine compares this against a selected clip's stored [start, end)
# range to detect the playhead has left the clip.
# ---------------------------------------------------------------------------

def test_status_includes_playhead_frame():
    drv = _make_driver("edit", playhead_frame=42)
    resp = handle({"op": "status"}, {}, drv)
    assert resp["ok"] is True
    assert resp["playhead_frame"] == 42


def test_status_playhead_frame_none_when_no_timeline():
    """Graceful fallback: no live timeline -- current_playhead_frame()
    returns None, and the status response must carry that through as-is."""
    drv = _make_driver("color", playhead_frame=None)
    resp = handle({"op": "status"}, {}, drv)
    assert resp["ok"] is True
    assert resp["playhead_frame"] is None


def test_status_current_playhead_frame_called_once():
    drv = _make_driver("edit", playhead_frame=10)
    handle({"op": "status"}, {}, drv)
    drv.current_playhead_frame.assert_called_once()


# ---------------------------------------------------------------------------
# HUD v2 Edit-page readout (2026-07-08): "status" op grows "edit_clip",
# read via ResolveDriver.edit_clip_info() -- the SAME _target_item-resolved
# clip bump_property/reset_property target, name + live Pan/Tilt/ZoomX.
# ---------------------------------------------------------------------------

def test_status_includes_edit_clip():
    edit_clip = {"name": "DSCF9804.MOV", "pan": 12.5, "tilt": -3.25, "zoom": 1.4}
    drv = _make_driver("edit", edit_clip=edit_clip)
    resp = handle({"op": "status"}, {}, drv)
    assert resp["ok"] is True
    assert resp["edit_clip"] == edit_clip


def test_status_edit_clip_none_when_no_target_clip():
    """Graceful fallback: no target clip -- edit_clip_info() returns None,
    and the status response must carry that through as-is (never raise,
    never substitute a fake value)."""
    drv = _make_driver("color", edit_clip=None)
    resp = handle({"op": "status"}, {}, drv)
    assert resp["ok"] is True
    assert resp["edit_clip"] is None


def test_status_edit_clip_info_called_once():
    drv = _make_driver("edit", edit_clip={"name": "x.mov", "pan": 0.0, "tilt": 0.0, "zoom": 1.0})
    handle({"op": "status"}, {}, drv)
    drv.edit_clip_info.assert_called_once()


def test_status_edit_clip_info_receives_track_from_request():
    """"track" is the SAME optional D1/direction-selection override
    pass-through convention as bump_property/reset_property -- the Go
    engine includes it when it has an active track override, so
    edit_clip_info() must be called with that exact value."""
    drv = _make_driver("edit", edit_clip=None)
    handle({"op": "status", "track": 4}, {}, drv)
    drv.edit_clip_info.assert_called_once_with(track=4)


def test_status_edit_clip_info_receives_none_track_when_absent():
    drv = _make_driver("edit", edit_clip=None)
    handle({"op": "status"}, {}, drv)
    drv.edit_clip_info.assert_called_once_with(track=None)
