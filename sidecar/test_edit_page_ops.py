"""
Tests for Phase 2 edit-page ops: bump_property, jog, render.

Uses mock ResolveDriver / mock Timeline+TimelineItem objects so Resolve
does not need to be running. Mirrors the mocking pattern already used in
test_sidecar_status.py: patch DaVinciResolveScript out before import so
resolve_driver.py's module-level `import DaVinciResolveScript` never
touches the real Blackmagic framework.
"""
import os
import sys
from unittest.mock import MagicMock, patch

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


def _make_driver_with_item(item):
    """Build a real ResolveDriver whose self.resolve is a mock resolving
    `item` (or no clip at all, when item is None) as the TOPMOST-OCCUPIED
    video clip at the playhead -- track=None's default target since the
    2026-07-08 fix (_target_item(track=None) scans occupied video tracks
    via _occupied_video_items(), it no longer calls GetCurrentVideoItem()
    at all). Wires GetCurrentTimecode/GetSetting/GetTrackCount/
    GetItemListInTrack so that scan resolves `item` as the sole occupied
    clip on video track 1 (or finds nothing, when item is None) at frame
    5 (00:00:00:05 @ 25fps). connect() is skipped — self.resolve is set
    directly so no real DaVinciResolveScript import happens.

    GetCurrentVideoItem() is ALSO still wired (harmless, matches `item`)
    purely for any incidental caller that happens to touch it directly —
    no current test in this file relies on it after the fix."""
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    tl = MagicMock()
    tl.GetCurrentVideoItem.return_value = item
    tl.GetCurrentTimecode.return_value = "00:00:00:05"
    tl.GetSetting.return_value = "25"
    tl.GetTrackCount.side_effect = lambda kind: 1 if item is not None else 0
    tl.GetItemListInTrack.side_effect = (
        lambda kind, n: [item] if (item is not None and n == 1) else []
    )
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = tl
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve
    drv._tl_mock = tl  # test-only handle
    drv._proj_mock = proj
    drv._pm_mock = pm
    return drv


def _make_item(props, start=0, end=100):
    """A TimelineItem stub with property get/set behavior AND
    GetStart/GetEnd (defaulting to a wide range covering frame 5, the
    fixed playhead frame _make_driver_with_item uses) -- GetStart/GetEnd
    are needed since the 2026-07-08 fix resolves track=None's default
    target via the occupied-track scan (start <= cur_frames < end), not
    GetCurrentVideoItem()."""
    item = MagicMock()
    item.GetProperty.side_effect = lambda name: props.get(name)

    def _set(name, val):
        props[name] = val
        return True

    item.SetProperty.side_effect = _set
    item.GetStart.return_value = start
    item.GetEnd.return_value = end
    return item


# ---------------------------------------------------------------------------
# bump_property — single property
# ---------------------------------------------------------------------------


def test_bump_property_single_reads_deltas_and_sets():
    item = _make_item({"Pan": 10.0})
    drv = _make_driver_with_item(item)

    ok, err, values = drv.bump_property(property_name="Pan", delta=2.5)

    assert ok is True
    assert err is None
    assert values == {"Pan": 12.5}
    item.SetProperty.assert_called_once_with("Pan", 12.5)


def test_bump_property_defaults_missing_current_value_to_zero():
    item = _make_item({})  # GetProperty returns None
    drv = _make_driver_with_item(item)

    ok, err, values = drv.bump_property(property_name="Tilt", delta=3.0)

    assert ok is True
    assert values == {"Tilt": 3.0}


def test_bump_property_no_clip_returns_error():
    drv = _make_driver_with_item(None)
    ok, err, values = drv.bump_property(property_name="Pan", delta=1.0)
    assert ok is False
    assert err == "no_clip"
    assert values is None


def test_bump_property_no_property_or_gang_is_error():
    item = _make_item({})
    drv = _make_driver_with_item(item)
    ok, err, values = drv.bump_property(delta=1.0)
    assert ok is False
    assert err == "no_property"


# ---------------------------------------------------------------------------
# bump_property — gang (zoom X/Y): independent read+delta, never mirrored
# ---------------------------------------------------------------------------


def test_bump_property_gang_applies_independently_not_mirrored():
    # ZoomX and ZoomY start at DIFFERENT values -- proves each is read+
    # deltaed on its own, not copied from one to the other.
    item = _make_item({"ZoomX": 1.0, "ZoomY": 1.2})
    drv = _make_driver_with_item(item)

    ok, err, values = drv.bump_property(gang=["ZoomX", "ZoomY"], delta=0.1)

    assert ok is True
    assert values == {"ZoomX": 1.1, "ZoomY": 1.3}


# ---------------------------------------------------------------------------
# bump_property — min/max clamp on the ABSOLUTE resulting value
# ---------------------------------------------------------------------------


def test_bump_property_clamps_to_max():
    item = _make_item({"CropTop": 95.0})
    drv = _make_driver_with_item(item)
    ok, err, values = drv.bump_property(property_name="CropTop", delta=20.0, max_val=100.0)
    assert ok is True
    assert values == {"CropTop": 100.0}


def test_bump_property_clamps_to_min():
    item = _make_item({"CropTop": 5.0})
    drv = _make_driver_with_item(item)
    ok, err, values = drv.bump_property(property_name="CropTop", delta=-20.0, min_val=0.0)
    assert ok is True
    assert values == {"CropTop": 0.0}


# ---------------------------------------------------------------------------
# bump_property — MUST re-resolve the occupied-track scan fresh every call
# (the blocking bug: cached _item() kept mutating the pre-jog clip).
# Re-resolving "fresh" now means re-running _occupied_video_items() every
# call (never caching the topmost-occupied item across calls) rather than
# re-calling GetCurrentVideoItem() -- same intent, new mechanism since the
# 2026-07-08 fix.
# ---------------------------------------------------------------------------


def test_bump_property_follows_playhead_no_stale_cache():
    item_a = _make_item({"Pan": 0.0})
    item_b = _make_item({"Pan": 100.0})

    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = "00:00:00:05"
    tl.GetSetting.return_value = "25"
    tl.GetTrackCount.return_value = 1
    tl.GetItemListInTrack.side_effect = [[item_a], [item_b]]
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = tl
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve

    ok1, _, values1 = drv.bump_property(property_name="Pan", delta=1.0)
    ok2, _, values2 = drv.bump_property(property_name="Pan", delta=1.0)

    assert ok1 is True and ok2 is True
    assert values1 == {"Pan": 1.0}
    # If the scan were cached, this would mutate item_a again (Pan=2.0)
    # instead of reading item_b's fresh Pan=100.0.
    assert values2 == {"Pan": 101.0}
    item_a.SetProperty.assert_called_once_with("Pan", 1.0)
    item_b.SetProperty.assert_called_once_with("Pan", 101.0)


def test_bump_property_exception_returns_error_not_raise():
    item = _make_item({"Pan": 0.0})
    item.SetProperty.side_effect = Exception("boom")
    drv = _make_driver_with_item(item)
    ok, err, values = drv.bump_property(property_name="Pan", delta=1.0)
    assert ok is False
    assert "boom" in err
    assert values is None


# ---------------------------------------------------------------------------
# reset_property — FIX 1: edit-page knob-CLICK resets the property/gang to
# its Resolve default (absolute set, not a relative bump). Mirrors the
# bump_property test shapes above one-for-one.
# ---------------------------------------------------------------------------


def test_reset_property_single_sets_zero_default():
    item = _make_item({"Pan": 37.5})
    drv = _make_driver_with_item(item)

    ok, err, values = drv.reset_property(property_name="Pan")

    assert ok is True
    assert err is None
    assert values == {"Pan": 0.0}
    item.SetProperty.assert_called_once_with("Pan", 0.0)


def test_reset_property_crop_sets_zero_default():
    item = _make_item({"CropTop": 42.0})
    drv = _make_driver_with_item(item)

    ok, err, values = drv.reset_property(property_name="CropTop")

    assert ok is True
    assert values == {"CropTop": 0.0}


def test_reset_property_zoom_sets_one_default_not_zero():
    # Zoom's "no correction" resting value is 1.0 (1x scale), NOT 0 — the
    # one property family where the default isn't zero.
    item = _make_item({"ZoomX": 2.4})
    drv = _make_driver_with_item(item)

    ok, err, values = drv.reset_property(property_name="ZoomX")

    assert ok is True
    assert values == {"ZoomX": 1.0}
    item.SetProperty.assert_called_once_with("ZoomX", 1.0)


def test_reset_property_rotation_sets_zero_default():
    item = _make_item({"RotationAngle": 15.0})
    drv = _make_driver_with_item(item)

    ok, err, values = drv.reset_property(property_name="RotationAngle")

    assert ok is True
    assert values == {"RotationAngle": 0.0}


def test_reset_property_unlisted_property_falls_back_to_zero():
    # A property with no entry in PROPERTY_DEFAULTS still resets cleanly
    # to 0.0 rather than raising a KeyError.
    item = _make_item({"SomeFutureProperty": 9.0})
    drv = _make_driver_with_item(item)

    ok, err, values = drv.reset_property(property_name="SomeFutureProperty")

    assert ok is True
    assert values == {"SomeFutureProperty": 0.0}


def test_reset_property_gang_sets_each_members_own_default_independently():
    # ZoomX (default 1.0) and CropTop (default 0.0) in the SAME gang call
    # proves each member gets ITS OWN default, not one shared value.
    item = _make_item({"ZoomX": 3.0, "CropTop": 50.0})
    drv = _make_driver_with_item(item)

    ok, err, values = drv.reset_property(gang=["ZoomX", "CropTop"])

    assert ok is True
    assert values == {"ZoomX": 1.0, "CropTop": 0.0}


def test_reset_property_no_clip_returns_error():
    drv = _make_driver_with_item(None)
    ok, err, values = drv.reset_property(property_name="Pan")
    assert ok is False
    assert err == "no_clip"
    assert values is None


def test_reset_property_no_property_or_gang_is_error():
    item = _make_item({})
    drv = _make_driver_with_item(item)
    ok, err, values = drv.reset_property()
    assert ok is False
    assert err == "no_property"


def test_reset_property_follows_playhead_no_stale_cache():
    # Same fresh-resolve contract as bump_property: a reset must apply to
    # whatever clip is CURRENTLY under the playhead, never a cached item
    # from before a jog moved it.
    item_a = _make_item({"Pan": 12.0})
    item_b = _make_item({"Pan": 88.0})

    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = "00:00:00:05"
    tl.GetSetting.return_value = "25"
    tl.GetTrackCount.return_value = 1
    tl.GetItemListInTrack.side_effect = [[item_a], [item_b]]
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = tl
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve

    ok1, _, values1 = drv.reset_property(property_name="Pan")
    ok2, _, values2 = drv.reset_property(property_name="Pan")

    assert ok1 is True and ok2 is True
    assert values1 == {"Pan": 0.0}
    assert values2 == {"Pan": 0.0}
    item_a.SetProperty.assert_called_once_with("Pan", 0.0)
    item_b.SetProperty.assert_called_once_with("Pan", 0.0)


def test_reset_property_exception_returns_error_not_raise():
    item = _make_item({"Pan": 0.0})
    item.SetProperty.side_effect = Exception("boom")
    drv = _make_driver_with_item(item)
    ok, err, values = drv.reset_property(property_name="Pan")
    assert ok is False
    assert "boom" in err
    assert values is None


# ---------------------------------------------------------------------------
# sidecar.handle() dispatch — reset_property
# ---------------------------------------------------------------------------


def test_handle_reset_property_dispatches():
    drv = MagicMock()
    drv.reset_property.return_value = (True, None, {"CropTop": 0.0})
    resp = sc.handle({"op": "reset_property", "property": "CropTop"}, {}, drv)
    assert resp["ok"] is True
    assert resp["values"] == {"CropTop": 0.0}
    drv.reset_property.assert_called_once_with(property_name="CropTop", gang=None, track=None)


def test_handle_reset_property_gang_dispatches():
    drv = MagicMock()
    drv.reset_property.return_value = (True, None, {"ZoomX": 1.0, "ZoomY": 1.0})
    resp = sc.handle({"op": "reset_property", "gang": ["ZoomX", "ZoomY"]}, {}, drv)
    assert resp["ok"] is True
    drv.reset_property.assert_called_once_with(property_name=None, gang=["ZoomX", "ZoomY"], track=None)


def test_handle_reset_property_passes_track_through():
    # D1 selectClip feature: the Go engine includes "track" when it has an
    # active-track selection (non-zero) -- the sidecar must pass it straight
    # through to reset_property so the reset targets THAT track's clip.
    drv = MagicMock()
    drv.reset_property.return_value = (True, None, {"CropTop": 0.0})
    resp = sc.handle({"op": "reset_property", "property": "CropTop", "track": 3}, {}, drv)
    assert resp["ok"] is True
    drv.reset_property.assert_called_once_with(property_name="CropTop", gang=None, track=3)


# ---------------------------------------------------------------------------
# sidecar.handle() dispatch — bump_property
# ---------------------------------------------------------------------------


def test_handle_bump_property_dispatches():
    drv = MagicMock()
    drv.bump_property.return_value = (True, None, {"Pan": 5.0})
    resp = sc.handle({"op": "bump_property", "property": "Pan", "delta": 5.0}, {}, drv)
    assert resp["ok"] is True
    assert resp["values"] == {"Pan": 5.0}
    drv.bump_property.assert_called_once_with(
        property_name="Pan", gang=None, delta=5.0, min_val=None, max_val=None, track=None
    )


def test_handle_bump_property_gang_dispatches():
    drv = MagicMock()
    drv.bump_property.return_value = (True, None, {"ZoomX": 1.1, "ZoomY": 1.1})
    resp = sc.handle(
        {"op": "bump_property", "gang": ["ZoomX", "ZoomY"], "delta": 0.1, "min": 0.5, "max": 4.0},
        {},
        drv,
    )
    assert resp["ok"] is True
    drv.bump_property.assert_called_once_with(
        property_name=None, gang=["ZoomX", "ZoomY"], delta=0.1, min_val=0.5, max_val=4.0, track=None
    )


def test_handle_bump_property_passes_track_through():
    # D1 selectClip feature: same track-passthrough contract as reset_property.
    drv = MagicMock()
    drv.bump_property.return_value = (True, None, {"Pan": 5.0})
    resp = sc.handle({"op": "bump_property", "property": "Pan", "delta": 5.0, "track": 4}, {}, drv)
    assert resp["ok"] is True
    drv.bump_property.assert_called_once_with(
        property_name="Pan", gang=None, delta=5.0, min_val=None, max_val=None, track=4
    )


# ---------------------------------------------------------------------------
# jog
# ---------------------------------------------------------------------------


def _make_driver_for_jog(cur_tc, fps_setting):
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = cur_tc
    tl.GetSetting.return_value = fps_setting
    tl.SetCurrentTimecode.return_value = True
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = tl
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve
    drv._tl_mock = tl
    return drv


def test_jog_forward_computes_new_timecode():
    drv = _make_driver_for_jog("00:00:10:00", "25")
    ok, err, new_tc = drv.jog(30)
    assert ok is True
    assert err is None
    assert new_tc == "00:00:11:05"
    drv._tl_mock.SetCurrentTimecode.assert_called_once_with("00:00:11:05")


def test_jog_negative_clamps_at_zero():
    drv = _make_driver_for_jog("00:00:00:05", "25")
    ok, err, new_tc = drv.jog(-30)
    assert ok is True
    assert new_tc == "00:00:00:00"


def test_jog_drop_frame_timecode_refused():
    drv = _make_driver_for_jog("00:00:10;00", "29.97")
    ok, err, new_tc = drv.jog(5)
    assert ok is False
    assert err == "drop_frame_unsupported"
    drv._tl_mock.SetCurrentTimecode.assert_not_called()


def test_jog_non_integer_fps_refused():
    drv = _make_driver_for_jog("00:00:10:00", "23.976")
    ok, err, new_tc = drv.jog(5)
    assert ok is False
    assert err == "non_integer_fps_unsupported"
    drv._tl_mock.SetCurrentTimecode.assert_not_called()


def test_jog_no_timeline_returns_error():
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = None
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve
    ok, err, new_tc = drv.jog(5)
    assert ok is False
    assert err == "no_timeline"


def test_handle_jog_dispatches():
    drv = MagicMock()
    drv.jog.return_value = (True, None, "00:00:11:05")
    resp = sc.handle({"op": "jog", "frames": 30}, {}, drv)
    assert resp["ok"] is True
    assert resp["tc"] == "00:00:11:05"
    drv.jog.assert_called_once_with(30)


# ---------------------------------------------------------------------------
# jog_snap -- speed-tiered jog, fast dial snaps to round timecode boundaries
# ---------------------------------------------------------------------------


def test_jog_snap_forward_not_on_boundary():
    drv = _make_driver_for_jog("00:00:07:00", "25")
    ok, err, new_tc = drv.jog_snap(5, 1)
    assert ok is True
    assert err is None
    assert new_tc == "00:00:10:00"
    drv._tl_mock.SetCurrentTimecode.assert_called_once_with("00:00:10:00")


def test_jog_snap_forward_exactly_on_boundary_moves_full_tier():
    drv = _make_driver_for_jog("00:00:05:00", "25")
    ok, err, new_tc = drv.jog_snap(5, 1)
    assert ok is True
    assert new_tc == "00:00:10:00"


def test_jog_snap_backward_not_on_boundary():
    drv = _make_driver_for_jog("00:00:07:00", "25")
    ok, err, new_tc = drv.jog_snap(5, -1)
    assert ok is True
    assert new_tc == "00:00:05:00"


def test_jog_snap_backward_exactly_on_boundary_moves_full_tier():
    drv = _make_driver_for_jog("00:00:05:00", "25")
    ok, err, new_tc = drv.jog_snap(5, -1)
    assert ok is True
    assert new_tc == "00:00:00:00"


def test_jog_snap_clamps_at_zero():
    drv = _make_driver_for_jog("00:00:00:00", "25")
    ok, err, new_tc = drv.jog_snap(5, -1)
    assert ok is True
    assert new_tc == "00:00:00:00"


def test_jog_snap_thirty_second_tier():
    drv = _make_driver_for_jog("01:00:00:00", "25")
    ok, err, new_tc = drv.jog_snap(30, 1)
    assert ok is True
    assert new_tc == "01:00:30:00"


def test_jog_snap_ten_second_tier():
    drv = _make_driver_for_jog("00:00:23:00", "25")
    ok, err, new_tc = drv.jog_snap(10, 1)
    assert ok is True
    assert new_tc == "00:00:30:00"


def test_jog_snap_drop_frame_timecode_refused():
    drv = _make_driver_for_jog("00:00:10;00", "29.97")
    ok, err, new_tc = drv.jog_snap(5, 1)
    assert ok is False
    assert err == "drop_frame_unsupported"
    drv._tl_mock.SetCurrentTimecode.assert_not_called()


def test_jog_snap_non_integer_fps_refused():
    drv = _make_driver_for_jog("00:00:10:00", "23.976")
    ok, err, new_tc = drv.jog_snap(5, 1)
    assert ok is False
    assert err == "non_integer_fps_unsupported"
    drv._tl_mock.SetCurrentTimecode.assert_not_called()


def test_jog_snap_no_timeline_returns_error():
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = None
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve
    ok, err, new_tc = drv.jog_snap(5, 1)
    assert ok is False
    assert err == "no_timeline"


def test_jog_snap_bad_snap_seconds_refused():
    drv = _make_driver_for_jog("00:00:10:00", "25")
    ok, err, new_tc = drv.jog_snap(0, 1)
    assert ok is False
    assert err == "bad_snap_seconds"
    drv._tl_mock.SetCurrentTimecode.assert_not_called()


def test_handle_jog_dispatches_snap_when_snap_seconds_present():
    drv = MagicMock()
    drv.jog_snap.return_value = (True, None, "00:00:10:00")
    resp = sc.handle({"op": "jog", "snap_seconds": 5, "dir": 1, "frames": 9}, {}, drv)
    assert resp["ok"] is True
    assert resp["tc"] == "00:00:10:00"
    drv.jog_snap.assert_called_once_with(5, 1)
    drv.jog.assert_not_called()


def test_handle_jog_falls_back_to_frames_on_drop_frame():
    drv = MagicMock()
    drv.jog_snap.return_value = (False, "drop_frame_unsupported", None)
    drv.jog.return_value = (True, None, "00:00:00:09")
    resp = sc.handle({"op": "jog", "snap_seconds": 5, "dir": 1, "frames": 9}, {}, drv)
    assert resp["ok"] is True
    assert resp["tc"] == "00:00:00:09"
    drv.jog_snap.assert_called_once_with(5, 1)
    drv.jog.assert_called_once_with(9)


def test_handle_jog_falls_back_to_frames_on_non_integer_fps():
    drv = MagicMock()
    drv.jog_snap.return_value = (False, "non_integer_fps_unsupported", None)
    drv.jog.return_value = (True, None, "00:00:00:09")
    resp = sc.handle({"op": "jog", "snap_seconds": 5, "dir": 1, "frames": 9}, {}, drv)
    assert resp["ok"] is True
    assert resp["tc"] == "00:00:00:09"
    drv.jog.assert_called_once_with(9)


def test_handle_jog_snap_no_fallback_on_other_errors():
    # no_timeline would fail identically for jog() too -- the fallback is
    # scoped ONLY to drop-frame/non-integer-fps (the "don't mis-snap"
    # constraint), not a blanket retry-on-any-failure.
    drv = MagicMock()
    drv.jog_snap.return_value = (False, "no_timeline", None)
    resp = sc.handle({"op": "jog", "snap_seconds": 5, "dir": 1, "frames": 9}, {}, drv)
    assert resp["ok"] is False
    assert resp["err"] == "no_timeline"
    drv.jog.assert_not_called()


def test_handle_jog_frames_only_path_unaffected():
    # No snap_seconds key at all -- the pre-existing plain frames path.
    drv = MagicMock()
    drv.jog.return_value = (True, None, "00:00:11:05")
    resp = sc.handle({"op": "jog", "frames": 30}, {}, drv)
    assert resp["ok"] is True
    drv.jog.assert_called_once_with(30)
    drv.jog_snap.assert_not_called()


# ---------------------------------------------------------------------------
# jog_seconds -- D1's coarse playhead jump (the coarse counterpart to jog)
# ---------------------------------------------------------------------------


def _make_driver_for_jog_seconds(cur_tc, fps_setting, start_frame=0, end_frame=10**9):
    """Mirrors _make_driver_for_jog above, plus GetStartFrame/GetEndFrame
    mocks (generous defaults so a test that doesn't care about clamping
    never accidentally clamps)."""
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = cur_tc
    tl.GetSetting.return_value = fps_setting
    tl.SetCurrentTimecode.return_value = True
    tl.GetStartFrame.return_value = start_frame
    tl.GetEndFrame.return_value = end_frame
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = tl
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve
    drv._tl_mock = tl
    return drv


def test_jog_seconds_forward_computes_new_timecode():
    drv = _make_driver_for_jog_seconds("00:00:10:00", "25")
    ok, err, new_tc = drv.jog_seconds(60)
    assert ok is True
    assert err is None
    # 10s + 60s = 70s = 00:01:10:00 @ 25fps.
    assert new_tc == "00:01:10:00"
    drv._tl_mock.SetCurrentTimecode.assert_called_once_with("00:01:10:00")


def test_jog_seconds_backward_computes_new_timecode():
    drv = _make_driver_for_jog_seconds("00:02:00:00", "25")
    ok, err, new_tc = drv.jog_seconds(-60)
    assert ok is True
    assert err is None
    # 120s - 60s = 60s = 00:01:00:00.
    assert new_tc == "00:01:00:00"


def test_jog_seconds_clamps_at_timeline_start():
    # 10s in, jumping back 60s would land at a negative frame -- clamp to
    # the timeline's own start frame (100), not 0.
    drv = _make_driver_for_jog_seconds("00:00:10:00", "25", start_frame=100, end_frame=10**6)
    ok, err, new_tc = drv.jog_seconds(-60)
    assert ok is True
    assert err is None
    assert new_tc == drv._format_frames(100, 25)
    drv._tl_mock.SetCurrentTimecode.assert_called_once_with(drv._format_frames(100, 25))


def test_jog_seconds_clamps_at_timeline_end():
    # Timeline ends at frame 300 (frames [100, 300) valid, half-open) --
    # jumping forward 60s from frame 290 must clamp to the LAST valid
    # frame (299), never reach or pass 300.
    drv = _make_driver_for_jog_seconds("00:00:11:15", "25", start_frame=100, end_frame=300)
    # 00:00:11:15 @ 25fps = 11*25+15 = 290 frames.
    ok, err, new_tc = drv.jog_seconds(60)
    assert ok is True
    assert err is None
    assert new_tc == drv._format_frames(299, 25)
    drv._tl_mock.SetCurrentTimecode.assert_called_once_with(drv._format_frames(299, 25))


def test_jog_seconds_within_bounds_not_clamped():
    drv = _make_driver_for_jog_seconds("00:00:20:00", "25", start_frame=100, end_frame=10**6)
    ok, err, new_tc = drv.jog_seconds(60)
    assert ok is True
    # 20s + 60s = 80s = 00:01:20:00, well within bounds.
    assert new_tc == "00:01:20:00"


def test_jog_seconds_drop_frame_timecode_refused():
    drv = _make_driver_for_jog_seconds("00:00:10;00", "29.97")
    ok, err, new_tc = drv.jog_seconds(60)
    assert ok is False
    assert err == "drop_frame_unsupported"
    drv._tl_mock.SetCurrentTimecode.assert_not_called()


def test_jog_seconds_non_integer_fps_refused():
    drv = _make_driver_for_jog_seconds("00:00:10:00", "23.976")
    ok, err, new_tc = drv.jog_seconds(60)
    assert ok is False
    assert err == "non_integer_fps_unsupported"
    drv._tl_mock.SetCurrentTimecode.assert_not_called()


def test_jog_seconds_no_timeline_returns_error():
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = None
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve
    ok, err, new_tc = drv.jog_seconds(60)
    assert ok is False
    assert err == "no_timeline"


def test_jog_seconds_disconnected_self_heals_via_ensure_connected():
    """jog_seconds() itself doesn't call _ensure_connected() (that lives in
    sidecar.py's op handler, mirroring bump_property -- see
    test_resolve_connect.py), but calling it against a None self.resolve
    must degrade gracefully (never raise), consistent with jog()/
    jog_snap()'s existing behavior against a dead/disconnected handle."""
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    drv.resolve = None
    ok, err, new_tc = drv.jog_seconds(60)
    assert ok is False
    assert err is not None
    assert new_tc is None


def test_handle_jog_seconds_dispatches():
    drv = MagicMock()
    drv.jog_seconds.return_value = (True, None, "00:01:10:00")
    resp = sc.handle({"op": "jog_seconds", "seconds": 60}, {}, drv)
    assert resp["ok"] is True
    assert resp["tc"] == "00:01:10:00"
    drv.jog_seconds.assert_called_once_with(60.0)


def test_handle_jog_seconds_negative_dispatches():
    drv = MagicMock()
    drv.jog_seconds.return_value = (True, None, "00:00:50:00")
    resp = sc.handle({"op": "jog_seconds", "seconds": -60}, {}, drv)
    assert resp["ok"] is True
    drv.jog_seconds.assert_called_once_with(-60.0)


def test_handle_jog_seconds_defaults_seconds_to_zero_when_absent():
    drv = MagicMock()
    drv.jog_seconds.return_value = (True, None, "00:00:10:00")
    resp = sc.handle({"op": "jog_seconds"}, {}, drv)
    assert resp["ok"] is True
    drv.jog_seconds.assert_called_once_with(0.0)


# ---------------------------------------------------------------------------
# jog_minute_snap -- D1's CURRENT binding: snap the playhead to the next/
# previous whole-minute timecode boundary (SS:FF == "00:00")
# ---------------------------------------------------------------------------


def _make_driver_for_jog_minute_snap(cur_tc, fps_setting, start_frame=0, end_frame=10**9):
    """Mirrors _make_driver_for_jog_seconds above."""
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = cur_tc
    tl.GetSetting.return_value = fps_setting
    tl.SetCurrentTimecode.return_value = True
    tl.GetStartFrame.return_value = start_frame
    tl.GetEndFrame.return_value = end_frame
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = tl
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve
    drv._tl_mock = tl
    return drv


def test_jog_minute_snap_forward_not_on_boundary():
    drv = _make_driver_for_jog_minute_snap("00:03:24:10", "25")
    ok, err, new_tc = drv.jog_minute_snap(1)
    assert ok is True
    assert err is None
    assert new_tc == "00:04:00:00"
    drv._tl_mock.SetCurrentTimecode.assert_called_once_with("00:04:00:00")


def test_jog_minute_snap_backward_not_on_boundary():
    drv = _make_driver_for_jog_minute_snap("00:03:24:10", "25")
    ok, err, new_tc = drv.jog_minute_snap(-1)
    assert ok is True
    assert err is None
    assert new_tc == "00:03:00:00"


def test_jog_minute_snap_forward_exactly_on_boundary_still_advances():
    # Exactly on a whole-minute boundary -- must still move a FULL minute
    # forward, never a no-op (mirrors jog_snap's "always a full tier").
    drv = _make_driver_for_jog_minute_snap("00:03:00:00", "25")
    ok, err, new_tc = drv.jog_minute_snap(1)
    assert ok is True
    assert new_tc == "00:04:00:00"


def test_jog_minute_snap_backward_exactly_on_boundary_still_advances():
    drv = _make_driver_for_jog_minute_snap("00:03:00:00", "25")
    ok, err, new_tc = drv.jog_minute_snap(-1)
    assert ok is True
    assert new_tc == "00:02:00:00"


def test_jog_minute_snap_non_25fps_sanity():
    # 24fps: 00:01:30:12 = (90*24)+12 = 2172 frames; framesPerMinute=1440.
    drv = _make_driver_for_jog_minute_snap("00:01:30:12", "24")
    ok, err, new_tc = drv.jog_minute_snap(1)
    assert ok is True
    assert new_tc == "00:02:00:00"

    drv2 = _make_driver_for_jog_minute_snap("00:01:30:12", "24")
    ok, err, new_tc = drv2.jog_minute_snap(-1)
    assert ok is True
    assert new_tc == "00:01:00:00"


def test_jog_minute_snap_start_frame_offset_aligns_to_displayed_tc():
    # Timeline TC starts at 01:00:00:00 (start_frame = 1h worth of frames
    # @ 25fps = 90000) -- boundaries must align to the DISPLAYED timecode
    # (SS:FF == "00:00"), not to modulo-0 from absolute frame 0.
    drv = _make_driver_for_jog_minute_snap(
        "01:03:24:10", "25", start_frame=90000, end_frame=10**7
    )
    ok, err, new_tc = drv.jog_minute_snap(1)
    assert ok is True
    assert new_tc == "01:04:00:00"

    drv2 = _make_driver_for_jog_minute_snap(
        "01:03:24:10", "25", start_frame=90000, end_frame=10**7
    )
    ok, err, new_tc = drv2.jog_minute_snap(-1)
    assert ok is True
    assert new_tc == "01:03:00:00"


def test_jog_minute_snap_clamps_forward_at_timeline_end_noop():
    # end=5000 (last valid frame 4999); cur=00:03:10:00 (4750 frames) --
    # the next boundary (6000) falls OUTSIDE [0, 5000), so this must be a
    # genuine no-op: playhead unchanged, SetCurrentTimecode never called.
    drv = _make_driver_for_jog_minute_snap(
        "00:03:10:00", "25", start_frame=0, end_frame=5000
    )
    ok, err, new_tc = drv.jog_minute_snap(1)
    assert ok is True
    assert err is None
    assert new_tc == "00:03:10:00"
    drv._tl_mock.SetCurrentTimecode.assert_not_called()


def test_jog_minute_snap_clamps_backward_at_timeline_start_noop():
    # start=1000 (not a whole-minute-aligned frame); cur=00:00:48:00
    # (1200 frames) -- the previous boundary (0) falls OUTSIDE [1000, end),
    # so this must be a genuine no-op.
    drv = _make_driver_for_jog_minute_snap(
        "00:00:48:00", "25", start_frame=1000, end_frame=10**6
    )
    ok, err, new_tc = drv.jog_minute_snap(-1)
    assert ok is True
    assert err is None
    assert new_tc == "00:00:48:00"
    drv._tl_mock.SetCurrentTimecode.assert_not_called()


def test_jog_minute_snap_within_bounds_not_clamped():
    drv = _make_driver_for_jog_minute_snap(
        "00:00:20:00", "25", start_frame=0, end_frame=10**6
    )
    ok, err, new_tc = drv.jog_minute_snap(1)
    assert ok is True
    assert new_tc == "00:01:00:00"
    drv._tl_mock.SetCurrentTimecode.assert_called_once_with("00:01:00:00")


def test_jog_minute_snap_zero_direction_refused():
    drv = _make_driver_for_jog_minute_snap("00:03:00:00", "25")
    ok, err, new_tc = drv.jog_minute_snap(0)
    assert ok is False
    assert err == "bad_direction"
    assert new_tc is None
    drv._tl_mock.SetCurrentTimecode.assert_not_called()


def test_jog_minute_snap_none_direction_refused():
    drv = _make_driver_for_jog_minute_snap("00:03:00:00", "25")
    ok, err, new_tc = drv.jog_minute_snap(None)
    assert ok is False
    assert err == "bad_direction"
    assert new_tc is None


def test_jog_minute_snap_drop_frame_timecode_refused():
    drv = _make_driver_for_jog_minute_snap("00:00:10;00", "29.97")
    ok, err, new_tc = drv.jog_minute_snap(1)
    assert ok is False
    assert err == "drop_frame_unsupported"
    drv._tl_mock.SetCurrentTimecode.assert_not_called()


def test_jog_minute_snap_non_integer_fps_refused():
    drv = _make_driver_for_jog_minute_snap("00:00:10:00", "23.976")
    ok, err, new_tc = drv.jog_minute_snap(1)
    assert ok is False
    assert err == "non_integer_fps_unsupported"


def test_jog_minute_snap_no_timeline_returns_error():
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = None
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve
    ok, err, new_tc = drv.jog_minute_snap(1)
    assert ok is False
    assert err == "no_timeline"


def test_jog_minute_snap_disconnected_self_heals_via_ensure_connected():
    """jog_minute_snap() itself doesn't call _ensure_connected() (that
    lives in sidecar.py's op handler, mirroring jog_seconds -- see
    test_resolve_connect.py), but calling it against a None self.resolve
    must degrade gracefully (never raise)."""
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    drv.resolve = None
    ok, err, new_tc = drv.jog_minute_snap(1)
    assert ok is False
    assert err is not None
    assert new_tc is None


def test_handle_jog_minute_snap_dispatches_forward():
    drv = MagicMock()
    drv.jog_minute_snap.return_value = (True, None, "00:04:00:00")
    resp = sc.handle({"op": "jog_minute_snap", "direction": 1}, {}, drv)
    assert resp["ok"] is True
    assert resp["tc"] == "00:04:00:00"
    drv.jog_minute_snap.assert_called_once_with(1)


def test_handle_jog_minute_snap_dispatches_backward():
    drv = MagicMock()
    drv.jog_minute_snap.return_value = (True, None, "00:03:00:00")
    resp = sc.handle({"op": "jog_minute_snap", "direction": -1}, {}, drv)
    assert resp["ok"] is True
    drv.jog_minute_snap.assert_called_once_with(-1)


def test_handle_jog_minute_snap_defaults_direction_to_zero_when_absent():
    drv = MagicMock()
    drv.jog_minute_snap.return_value = (False, "bad_direction", None)
    resp = sc.handle({"op": "jog_minute_snap"}, {}, drv)
    assert resp["ok"] is False
    drv.jog_minute_snap.assert_called_once_with(0)


# ---------------------------------------------------------------------------
# render
# ---------------------------------------------------------------------------


def _make_driver_for_render(existing_files=()):
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    tl = MagicMock()
    tl.GetName.return_value = "MyTimeline"
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = tl
    proj.LoadRenderPreset.return_value = True
    proj.SetRenderSettings.return_value = True
    proj.AddRenderJob.return_value = "job-123"
    proj.StartRendering.return_value = True
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve
    drv._proj_mock = proj
    drv._existing = set(existing_files)
    return drv


def test_render_happy_path(tmp_path, monkeypatch):
    drv = _make_driver_for_render()
    target = str(tmp_path)

    ok, err, info = drv.render(target)

    assert ok is True
    assert err is None
    drv._proj_mock.LoadRenderPreset.assert_called_once_with("H.264 Master")
    drv._proj_mock.SetRenderSettings.assert_called_once_with(
        {"TargetDir": target, "CustomName": "MyTimeline"}
    )
    drv._proj_mock.AddRenderJob.assert_called_once()
    drv._proj_mock.StartRendering.assert_called_once_with(["job-123"])
    assert info["customName"] == "MyTimeline"
    assert info["jobId"] == "job-123"


def test_render_auto_increments_name_to_avoid_overwrite(tmp_path):
    # Pre-create "MyTimeline.mov" so the auto-increment logic must pick
    # "MyTimeline_2" instead of overwriting.
    (tmp_path / "MyTimeline.mov").write_bytes(b"")
    drv = _make_driver_for_render()

    ok, err, info = drv.render(str(tmp_path))

    assert ok is True
    assert info["customName"] == "MyTimeline_2"
    drv._proj_mock.SetRenderSettings.assert_called_once_with(
        {"TargetDir": str(tmp_path), "CustomName": "MyTimeline_2"}
    )


def test_render_preset_load_failure_returns_error(tmp_path):
    drv = _make_driver_for_render()
    drv._proj_mock.LoadRenderPreset.return_value = False
    ok, err, info = drv.render(str(tmp_path))
    assert ok is False
    assert err == "preset_load_failed"
    drv._proj_mock.SetRenderSettings.assert_not_called()


def test_render_no_project_returns_error(tmp_path):
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    pm = MagicMock()
    pm.GetCurrentProject.return_value = None
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve
    ok, err, info = drv.render(str(tmp_path))
    assert ok is False
    assert err == "no_project"


def test_handle_render_dispatches():
    drv = MagicMock()
    drv.render.return_value = (True, None, {"jobId": "j1", "customName": "X", "targetDir": "/tmp"})
    resp = sc.handle({"op": "render", "target_dir": "/tmp"}, {}, drv)
    assert resp["ok"] is True
    assert resp["render"]["jobId"] == "j1"
    drv.render.assert_called_once_with("/tmp")


# ---------------------------------------------------------------------------
# edit_clip_info — HUD v2 Edit-page readout (2026-07-08): the SAME
# _target_item-resolved clip's name + live Pan/Tilt/ZoomX. Reuses
# _make_driver_with_item/_make_item exactly like bump_property/
# reset_property above -- edit_clip_info resolves the identical target.
# ---------------------------------------------------------------------------


def test_edit_clip_info_happy_path_default_target():
    item = _make_item({"Pan": 12.5, "Tilt": -3.25, "ZoomX": 1.4})
    item.GetName.return_value = "DSCF9804.MOV"
    drv = _make_driver_with_item(item)

    info = drv.edit_clip_info()

    assert info == {"name": "DSCF9804.MOV", "pan": 12.5, "tilt": -3.25, "zoom": 1.4}


def test_edit_clip_info_no_clip_returns_none():
    drv = _make_driver_with_item(None)
    assert drv.edit_clip_info() is None


def test_edit_clip_info_missing_properties_default_to_zero():
    item = _make_item({})  # GetProperty returns None for every name
    item.GetName.return_value = "clip.mov"
    drv = _make_driver_with_item(item)

    info = drv.edit_clip_info()

    assert info == {"name": "clip.mov", "pan": 0.0, "tilt": 0.0, "zoom": 0.0}


def test_edit_clip_info_exception_returns_none_not_raise():
    """A GetProperty() hiccup on the resolved item must degrade to None,
    never raise -- same graceful-failure posture as bump_property/
    reset_property's own exception handling."""
    item = _make_item({"Pan": 0.0})
    item.GetProperty.side_effect = Exception("boom")
    drv = _make_driver_with_item(item)

    assert drv.edit_clip_info() is None


def test_edit_clip_info_never_poisons_resolve_on_exception():
    """Part A tie-in: a failed edit_clip_info read (a purely cosmetic HUD
    display read) must never null self.resolve -- see current_page()'s
    2026-07-08 docstring for the general rule this follows."""
    item = _make_item({"Pan": 0.0})
    item.GetProperty.side_effect = Exception("boom")
    drv = _make_driver_with_item(item)
    live_handle = drv.resolve

    drv.edit_clip_info()

    assert drv.resolve is live_handle


def test_edit_clip_info_track_override_targets_that_track():
    """track= is the SAME activeTrack/selection override bump_property/
    reset_property already honor via _target_item -- must resolve THAT
    track's clip, not the default top-occupied one."""
    drv = resolve_driver.ResolveDriver.__new__(resolve_driver.ResolveDriver)
    drv.item = None
    top_item = MagicMock()
    top_item.GetName.return_value = "top.mov"
    top_item.GetProperty.side_effect = lambda n: {"Pan": 1.0, "Tilt": 1.0, "ZoomX": 1.0}.get(n)
    top_item.GetStart.return_value = 0
    top_item.GetEnd.return_value = 100
    lower_item = MagicMock()
    lower_item.GetName.return_value = "lower.mov"
    lower_item.GetProperty.side_effect = lambda n: {"Pan": 5.0, "Tilt": 6.0, "ZoomX": 2.0}.get(n)
    lower_item.GetStart.return_value = 0
    lower_item.GetEnd.return_value = 100

    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = "00:00:00:05"
    tl.GetSetting.return_value = "25"
    tl.GetTrackCount.side_effect = lambda kind: 2
    tl.GetItemListInTrack.side_effect = lambda kind, n: {1: [lower_item], 2: [top_item]}.get(n, [])
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = tl
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve

    # No override: top-occupied (track 2) wins.
    assert drv.edit_clip_info() == {"name": "top.mov", "pan": 1.0, "tilt": 1.0, "zoom": 1.0}
    # Explicit track override: pinned to track 1's clip instead.
    assert drv.edit_clip_info(track=1) == {"name": "lower.mov", "pan": 5.0, "tilt": 6.0, "zoom": 2.0}


# ---------------------------------------------------------------------------
# sidecar.handle() dispatch — status op's "edit_clip" field
# ---------------------------------------------------------------------------


def test_handle_status_passes_track_to_edit_clip_info():
    drv = MagicMock()
    drv.current_page.return_value = "edit"
    drv.current_node.return_value = None
    drv.current_timecode.return_value = (None, None)
    drv.current_video_item_id.return_value = None
    drv.current_playhead_frame.return_value = None
    drv.edit_clip_info.return_value = {"name": "a.mov", "pan": 1.0, "tilt": 2.0, "zoom": 1.4}

    resp = sc.handle({"op": "status", "track": 3}, {}, drv)

    drv.edit_clip_info.assert_called_once_with(track=3)
    assert resp["edit_clip"] == {"name": "a.mov", "pan": 1.0, "tilt": 2.0, "zoom": 1.4}


def test_handle_status_edit_clip_none_when_track_absent():
    drv = MagicMock()
    drv.current_page.return_value = "edit"
    drv.current_node.return_value = None
    drv.current_timecode.return_value = (None, None)
    drv.current_video_item_id.return_value = None
    drv.current_playhead_frame.return_value = None
    drv.edit_clip_info.return_value = None

    resp = sc.handle({"op": "status"}, {}, drv)

    drv.edit_clip_info.assert_called_once_with(track=None)
    assert resp["edit_clip"] is None
