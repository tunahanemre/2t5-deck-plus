"""
Tests for the D1 "active-clip highlight" feature's sidecar ops:
  - ResolveDriver.highlight_clip()   -- restore-prev + tint-new + persist state
  - ResolveDriver.clear_highlight()  -- restore current + clear state (engine Stop)
  - ResolveDriver.restore_highlight() -- crash-safety restore from state file
    (engine Start / sidecar connect)
  - ResolveDriver._find_item_by_unique_id() -- locate a clip by GetUniqueId(),
    independent of the current playhead position
  - sidecar.handle()'s "highlight_clip"/"clear_highlight"/"restore_highlight"
    dispatch

Uses the same mock ResolveDriver / mock Timeline+TimelineItem pattern as
test_select_clip_ops.py (DaVinciResolveScript patched out before import so
resolve_driver.py's module-level import never touches the real Blackmagic
framework). SetClipColor/GetClipColor/ClearClipColor are ALWAYS MagicMock
methods here -- never called against a live Resolve project, per the hard
hermeticity constraint (these calls mutate a real project).

State-file crash-safety tests use pytest's tmp_path fixture (a real temp
file on disk) assigned to drv.highlight_state_path -- never the real
<data root>/state/2t5-deck-plus-highlight.json production path (see
resolve_driver.data_root / DECKPLUS_DATA_DIR).
"""
import json
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


def _make_item_with_color(name, start, end, color="", unique_id="uid-1"):
    """A TimelineItem stub exposing GetName/GetStart/GetEnd (for track-scan
    resolution) PLUS GetClipColor/SetClipColor/ClearClipColor/GetUniqueId
    (for the highlight feature). color="" mirrors the spec's documented
    GetClipColor() behavior for "no color set"."""
    item = MagicMock()
    item.GetName.return_value = name
    item.GetStart.return_value = start
    item.GetEnd.return_value = end
    item.GetClipColor.return_value = color
    item.GetUniqueId.return_value = unique_id
    return item


def _make_driver_for_highlight(cur_tc, fps_setting, track_items, highlight_state_path):
    """Same shape as test_select_clip_ops.py's _make_driver_for_track_ops,
    plus the highlight-feature attributes that __init__ would normally set
    (bypassed here since tests construct via __new__, same pattern as every
    other test file in this repo)."""
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
# _find_item_by_unique_id
# ---------------------------------------------------------------------------


def test_find_item_by_unique_id_hits_track_hint_fast_path(tmp_path):
    item1 = _make_item_with_color("A.mov", 0, 100, unique_id="uid-A")
    item2 = _make_item_with_color("B.mov", 0, 100, unique_id="uid-B")
    drv = _make_driver_for_highlight("00:00:00:05", "25", {1: [item1], 2: [item2]}, tmp_path / "h.json")

    found = drv._find_item_by_unique_id("uid-B", track_hint=2)

    assert found is item2
    # Fast path: only the hinted track was scanned, GetTrackCount never consulted.
    drv._tl_mock.GetTrackCount.assert_not_called()


def test_find_item_by_unique_id_falls_back_to_full_scan_when_hint_misses(tmp_path):
    item1 = _make_item_with_color("A.mov", 0, 100, unique_id="uid-A")
    item2 = _make_item_with_color("B.mov", 0, 100, unique_id="uid-B")
    # Hint points at track 1, but the clip is actually on track 2 (e.g. it
    # was moved since the tint was applied).
    drv = _make_driver_for_highlight("00:00:00:05", "25", {1: [item1], 2: [item2]}, tmp_path / "h.json")

    found = drv._find_item_by_unique_id("uid-B", track_hint=1)

    assert found is item2


def test_find_item_by_unique_id_no_hint_scans_all_tracks(tmp_path):
    item1 = _make_item_with_color("A.mov", 0, 100, unique_id="uid-A")
    item2 = _make_item_with_color("B.mov", 0, 100, unique_id="uid-B")
    drv = _make_driver_for_highlight("00:00:00:05", "25", {1: [item1], 2: [item2]}, tmp_path / "h.json")

    found = drv._find_item_by_unique_id("uid-B", track_hint=None)

    assert found is item2


def test_find_item_by_unique_id_not_found_returns_none(tmp_path):
    item1 = _make_item_with_color("A.mov", 0, 100, unique_id="uid-A")
    drv = _make_driver_for_highlight("00:00:00:05", "25", {1: [item1]}, tmp_path / "h.json")

    found = drv._find_item_by_unique_id("uid-ZZZ", track_hint=None)

    assert found is None


def test_find_item_by_unique_id_no_timeline_returns_none(tmp_path):
    drv = _make_driver_no_timeline(tmp_path / "h.json")
    found = drv._find_item_by_unique_id("uid-A", track_hint=1)
    assert found is None


# ---------------------------------------------------------------------------
# highlight_clip
# ---------------------------------------------------------------------------


def test_highlight_clip_no_previous_tints_new_and_persists_state(tmp_path):
    item = _make_item_with_color("A.mov", 0, 100, color="", unique_id="uid-A")
    state_path = tmp_path / "h.json"
    drv = _make_driver_for_highlight("00:00:00:05", "25", {2: [item]}, state_path)

    ok, err = drv.highlight_clip(track=2, color="Teal")

    assert ok is True
    assert err is None
    item.SetClipColor.assert_called_once_with("Teal")
    assert drv._highlight == {"unique_id": "uid-A", "original_color": "", "track": 2}
    assert state_path.exists()
    assert json.loads(state_path.read_text()) == {"unique_id": "uid-A", "original_color": "", "track": 2}


def test_highlight_clip_captures_existing_color_as_original(tmp_path):
    item = _make_item_with_color("A.mov", 0, 100, color="Orange", unique_id="uid-A")
    drv = _make_driver_for_highlight("00:00:00:05", "25", {2: [item]}, tmp_path / "h.json")

    ok, err = drv.highlight_clip(track=2, color="Teal")

    assert ok is True
    assert drv._highlight["original_color"] == "Orange"
    item.SetClipColor.assert_called_once_with("Teal")


def test_highlight_clip_leaked_own_highlight_color_captures_empty(tmp_path):
    # FIX 1: a clip already tinted the TOOL's own highlight color (e.g. a
    # leaked Beige tint from a prior select whose restore was lost -- app
    # force-kill before restore_highlight ran, or a stray old-D1 tint)
    # must NOT be captured as "original" -- otherwise C5/clear would hand
    # the leak right back instead of clearing it.
    item = _make_item_with_color("A.mov", 0, 100, color="Teal", unique_id="uid-A")
    drv = _make_driver_for_highlight("00:00:00:05", "25", {2: [item]}, tmp_path / "h.json")

    ok, err = drv.highlight_clip(track=2, color="Teal")

    assert ok is True
    assert drv._highlight["original_color"] == ""
    item.SetClipColor.assert_called_once_with("Teal")


def test_highlight_clip_leaked_own_highlight_color_clears_not_restores_to_tint(tmp_path):
    # End-to-end: select the leaked-tint clip, then clear -- must land on
    # NO color (ClearClipColor), never SetClipColor(color) a second time.
    item = _make_item_with_color("A.mov", 0, 100, color="Teal", unique_id="uid-A")
    drv = _make_driver_for_highlight("00:00:00:05", "25", {2: [item]}, tmp_path / "h.json")
    drv.highlight_clip(track=2, color="Teal")

    ok, err = drv.clear_highlight()

    assert ok is True
    item.ClearClipColor.assert_called_once_with()
    assert item.SetClipColor.call_args_list == [(("Teal",), {})]


def test_highlight_clip_restores_previous_clip_before_tinting_new(tmp_path):
    item1 = _make_item_with_color("A.mov", 0, 100, color="Orange", unique_id="uid-A")
    item2 = _make_item_with_color("B.mov", 0, 100, color="", unique_id="uid-B")
    drv = _make_driver_for_highlight("00:00:00:05", "25", {1: [item1], 2: [item2]}, tmp_path / "h.json")

    # First tint: A (track 1) -- captures original "Orange".
    ok1, _ = drv.highlight_clip(track=1, color="Teal")
    assert ok1 is True
    item1.SetClipColor.assert_called_once_with("Teal")

    # Move to B (track 2): A must be restored to "Orange" first, THEN B tinted.
    ok2, _ = drv.highlight_clip(track=2, color="Teal")
    assert ok2 is True
    item1.SetClipColor.assert_called_with("Orange")  # restore call
    assert item1.SetClipColor.call_count == 2  # tint, then restore
    item2.SetClipColor.assert_called_once_with("Teal")
    assert drv._highlight == {"unique_id": "uid-B", "original_color": "", "track": 2}


def test_highlight_clip_previous_original_empty_uses_clear_clip_color(tmp_path):
    item1 = _make_item_with_color("A.mov", 0, 100, color="", unique_id="uid-A")
    item2 = _make_item_with_color("B.mov", 0, 100, color="", unique_id="uid-B")
    drv = _make_driver_for_highlight("00:00:00:05", "25", {1: [item1], 2: [item2]}, tmp_path / "h.json")

    drv.highlight_clip(track=1, color="Teal")
    drv.highlight_clip(track=2, color="Teal")

    item1.ClearClipColor.assert_called_once_with()
    item1.SetClipColor.assert_called_once_with("Teal")  # only the initial tint, never a "restore to ''"


def test_highlight_clip_same_track_again_restores_and_retints_idempotently(tmp_path):
    # D1 clamp case: dispatchSelectClip can call setActiveTrack with the
    # SAME track twice in a row (already at the top/bottom). highlight_clip
    # must handle this as restore-self-then-retint, not error or double-tint.
    item = _make_item_with_color("A.mov", 0, 100, color="Orange", unique_id="uid-A")
    drv = _make_driver_for_highlight("00:00:00:05", "25", {1: [item]}, tmp_path / "h.json")

    drv.highlight_clip(track=1, color="Teal")
    ok, err = drv.highlight_clip(track=1, color="Teal")

    assert ok is True
    assert err is None
    # restore call used the ORIGINAL "Orange", not the intermediate "Teal".
    assert item.SetClipColor.call_args_list == [
        (("Teal",), {}), (("Orange",), {}), (("Teal",), {}),
    ]
    assert drv._highlight == {"unique_id": "uid-A", "original_color": "Orange", "track": 1}


def test_highlight_clip_no_clip_on_track_errors_and_does_not_touch_previous(tmp_path):
    item1 = _make_item_with_color("A.mov", 0, 100, color="Orange", unique_id="uid-A")
    drv = _make_driver_for_highlight("00:00:00:05", "25", {1: [item1], 2: []}, tmp_path / "h.json")

    drv.highlight_clip(track=1, color="Teal")
    ok, err = drv.highlight_clip(track=2, color="Teal")

    assert ok is False
    assert err == "no_clip_on_track"
    # The failed move to track 2 still restored A (best-effort: never leaves
    # A stuck tinted just because B couldn't be resolved).
    assert item1.SetClipColor.call_args_list == [(("Teal",), {}), (("Orange",), {})]
    assert drv._highlight is None


def test_highlight_clip_no_timeline_errors(tmp_path):
    drv = _make_driver_no_timeline(tmp_path / "h.json")
    ok, err = drv.highlight_clip(track=1, color="Teal")
    assert ok is False
    assert err == "no_timeline"


# ---------------------------------------------------------------------------
# clear_highlight
# ---------------------------------------------------------------------------


def test_clear_highlight_noop_when_nothing_highlighted(tmp_path):
    drv = _make_driver_for_highlight("00:00:00:05", "25", {}, tmp_path / "h.json")
    ok, err = drv.clear_highlight()
    assert ok is True
    assert err is None


def test_clear_highlight_restores_and_deletes_state_file(tmp_path):
    item = _make_item_with_color("A.mov", 0, 100, color="Orange", unique_id="uid-A")
    state_path = tmp_path / "h.json"
    drv = _make_driver_for_highlight("00:00:00:05", "25", {1: [item]}, state_path)
    drv.highlight_clip(track=1, color="Teal")
    assert state_path.exists()

    ok, err = drv.clear_highlight()

    assert ok is True
    assert err is None
    item.SetClipColor.assert_called_with("Orange")
    assert drv._highlight is None
    assert not state_path.exists()


def test_clear_highlight_empty_original_uses_clear_clip_color(tmp_path):
    item = _make_item_with_color("A.mov", 0, 100, color="", unique_id="uid-A")
    drv = _make_driver_for_highlight("00:00:00:05", "25", {1: [item]}, tmp_path / "h.json")
    drv.highlight_clip(track=1, color="Teal")

    ok, err = drv.clear_highlight()

    assert ok is True
    item.ClearClipColor.assert_called_once_with()


def test_clear_highlight_clip_gone_still_clears_state(tmp_path):
    item = _make_item_with_color("A.mov", 0, 100, color="Orange", unique_id="uid-A")
    state_path = tmp_path / "h.json"
    drv = _make_driver_for_highlight("00:00:00:05", "25", {1: [item]}, state_path)
    drv.highlight_clip(track=1, color="Teal")
    # Simulate the clip vanishing (deleted from the timeline) before Stop().
    drv._tl_mock.GetItemListInTrack.side_effect = lambda kind, n: []

    ok, err = drv.clear_highlight()

    assert ok is False
    assert err == "clip_not_found"
    assert drv._highlight is None
    assert not state_path.exists()


# ---------------------------------------------------------------------------
# restore_highlight (crash-safety)
# ---------------------------------------------------------------------------


def test_restore_highlight_no_state_file_is_noop(tmp_path):
    drv = _make_driver_for_highlight("00:00:00:05", "25", {}, tmp_path / "does-not-exist.json")
    ok, err = drv.restore_highlight()
    assert ok is True
    assert err is None


def test_restore_highlight_restores_and_deletes_state_file(tmp_path):
    item = _make_item_with_color("A.mov", 0, 100, color="Orange", unique_id="uid-A")
    state_path = tmp_path / "h.json"
    state_path.write_text(json.dumps({"unique_id": "uid-A", "original_color": "Orange", "track": 1}))
    drv = _make_driver_for_highlight("00:00:00:05", "25", {1: [item]}, state_path)

    ok, err = drv.restore_highlight()

    assert ok is True
    assert err is None
    item.SetClipColor.assert_called_once_with("Orange")
    assert not state_path.exists()
    assert drv._highlight is None


def test_restore_highlight_empty_original_uses_clear_clip_color(tmp_path):
    item = _make_item_with_color("A.mov", 0, 100, color="", unique_id="uid-A")
    state_path = tmp_path / "h.json"
    state_path.write_text(json.dumps({"unique_id": "uid-A", "original_color": "", "track": 1}))
    drv = _make_driver_for_highlight("00:00:00:05", "25", {1: [item]}, state_path)

    ok, err = drv.restore_highlight()

    assert ok is True
    item.ClearClipColor.assert_called_once_with()


def test_restore_highlight_finds_clip_moved_off_hinted_track(tmp_path):
    item = _make_item_with_color("A.mov", 0, 100, color="Orange", unique_id="uid-A")
    state_path = tmp_path / "h.json"
    # State file says track 1, but the clip is actually now on track 3.
    state_path.write_text(json.dumps({"unique_id": "uid-A", "original_color": "Orange", "track": 1}))
    drv = _make_driver_for_highlight("00:00:00:05", "25", {1: [], 3: [item]}, state_path)

    ok, err = drv.restore_highlight()

    assert ok is True
    item.SetClipColor.assert_called_once_with("Orange")
    assert not state_path.exists()


def test_restore_highlight_clip_not_found_still_deletes_state_file(tmp_path):
    state_path = tmp_path / "h.json"
    state_path.write_text(json.dumps({"unique_id": "uid-GONE", "original_color": "Orange", "track": 1}))
    drv = _make_driver_for_highlight("00:00:00:05", "25", {1: []}, state_path)

    ok, err = drv.restore_highlight()

    assert ok is False
    assert err == "clip_not_found"
    assert not state_path.exists()  # self-heals -- never retries forever
    assert drv._highlight is None


def test_restore_highlight_corrupt_state_file_deletes_and_returns_error(tmp_path):
    state_path = tmp_path / "h.json"
    state_path.write_text("{not valid json")
    drv = _make_driver_for_highlight("00:00:00:05", "25", {}, state_path)

    ok, err = drv.restore_highlight()

    assert ok is False
    assert err is not None
    assert not state_path.exists()


# ---------------------------------------------------------------------------
# sidecar.handle() dispatch
# ---------------------------------------------------------------------------


def test_handle_highlight_clip_dispatches():
    drv = MagicMock()
    drv.highlight_clip.return_value = (True, None)
    resp = sc.handle({"op": "highlight_clip", "track": 3, "color": "Teal"}, {}, drv)
    assert resp == {"ok": True, "err": None}
    drv.highlight_clip.assert_called_once_with(track=3, color="Teal")


def test_handle_highlight_clip_propagates_error():
    drv = MagicMock()
    drv.highlight_clip.return_value = (False, "no_clip_on_track")
    resp = sc.handle({"op": "highlight_clip", "track": 3, "color": "Teal"}, {}, drv)
    assert resp == {"ok": False, "err": "no_clip_on_track"}


def test_handle_clear_highlight_dispatches():
    drv = MagicMock()
    drv.clear_highlight.return_value = (True, None)
    resp = sc.handle({"op": "clear_highlight"}, {}, drv)
    assert resp == {"ok": True, "err": None}
    drv.clear_highlight.assert_called_once_with()


def test_handle_restore_highlight_dispatches():
    drv = MagicMock()
    drv.restore_highlight.return_value = (True, None)
    resp = sc.handle({"op": "restore_highlight"}, {}, drv)
    assert resp == {"ok": True, "err": None}
    drv.restore_highlight.assert_called_once_with()
