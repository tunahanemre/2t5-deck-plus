"""
Tests for the sidecar's self-healing Resolve connection:
  - ResolveDriver.connect()          -- bounded-retry startup connect
  - ResolveDriver._is_alive()        -- cheap liveness probe (2026-07-08)
  - ResolveDriver._ensure_connected() -- cheap per-status-tick reconnect,
    now driven by _is_alive() rather than just "is self.resolve None"
  - current_page()/current_node()/current_timecode()/current_video_item_id()
    never null self.resolve on a read failure (2026-07-08 Part A hardening
    -- a read failing is NEVER grounds to poison the shared connection;
    dead-handle detection is _ensure_connected()'s job alone now)
  - sidecar.handle()'s "status" op calling _ensure_connected() first

Root cause this fixes: DaVinciResolveScript.scriptapp("Resolve") can
transiently return None while Resolve's script-connection slots are
contended (many app/sidecar processes spawned/killed in quick
succession). The OLD connect() tried exactly once and silently kept a
None handle forever, even after Resolve became reachable a few seconds
later -- every Resolve-dependent op then permanently no-op'd until the
sidecar process was restarted.

Separately (2026-07-08, Part A): the OLD per-read "drop self.resolve to
None on any exception" guard in current_page/current_node/
current_timecode/current_video_item_id was live-verified to poison the
shared connection on a merely COSMETIC read failure that had nothing to
do with connection health (see current_video_item_id()'s docstring in
resolve_driver.py for the full incident) -- every read method now leaves
self.resolve untouched on failure, and _ensure_connected()'s new
_is_alive() probe is the only place a dead handle gets detected and
replaced.

Uses the same "stub DaVinciResolveScript before import" pattern as
test_sidecar_status.py/test_highlight_clip_ops.py -- no real Resolve API
is ever touched. CONNECT_RETRY_TIMEOUT/CONNECT_RETRY_INTERVAL are
overridden to tiny values per-test so the bounded-retry loop never sleeps
for real wall-clock seconds.
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
    return resolve_driver, sc, dvr_stub


resolve_driver, sc, _dvr_stub = _import_modules()


def _fresh_driver(fast_retry=True):
    """A ResolveDriver instance with retry tuning shrunk to test-safe
    values (never sleeps for real wall-clock seconds), and its own
    DaVinciResolveScript stub swapped into sys.modules for this test's
    scriptapp() call sequence."""
    drv = resolve_driver.ResolveDriver()
    if fast_retry:
        drv.CONNECT_RETRY_TIMEOUT = 0.05
        drv.CONNECT_RETRY_INTERVAL = 0.01
    return drv


def _set_scriptapp(*, side_effect=None, return_value=None):
    """Install a fresh DaVinciResolveScript stub in sys.modules with the
    given scriptapp() behavior -- resolve_driver.connect()/
    _ensure_connected() both do `import DaVinciResolveScript as dvr`
    locally, which resolves to whatever is currently cached in
    sys.modules under that name."""
    stub = MagicMock()
    if side_effect is not None:
        stub.scriptapp.side_effect = side_effect
    else:
        stub.scriptapp.return_value = return_value
    sys.modules["DaVinciResolveScript"] = stub
    return stub


# ---------------------------------------------------------------------------
# connect() -- bounded retry with backoff
# ---------------------------------------------------------------------------


def test_connect_retries_then_succeeds():
    """scriptapp() returns None twice, then a real object on the third
    attempt -- connect() must NOT give up after the first None, it must
    keep retrying (bounded) until it gets a non-None handle."""
    live = MagicMock()
    stub = _set_scriptapp(side_effect=[None, None, live])
    drv = _fresh_driver()
    drv.CONNECT_RETRY_TIMEOUT = 5.0  # plenty of headroom for 3 attempts
    drv.CONNECT_RETRY_INTERVAL = 0.001

    ok = drv.connect()

    assert ok is True
    assert drv.resolve is live
    assert stub.scriptapp.call_count == 3


def test_connect_succeeds_immediately_when_scriptapp_returns_object_first_try():
    live = MagicMock()
    _set_scriptapp(return_value=live)
    drv = _fresh_driver()

    ok = drv.connect()

    assert ok is True
    assert drv.resolve is live


def test_connect_gives_up_after_timeout_when_never_connects():
    """scriptapp() ALWAYS returns None -- connect() must still return
    (bounded), not hang forever, and self.resolve must stay None."""
    stub = _set_scriptapp(return_value=None)
    drv = _fresh_driver()
    drv.CONNECT_RETRY_TIMEOUT = 0.03
    drv.CONNECT_RETRY_INTERVAL = 0.01

    ok = drv.connect()

    assert ok is False
    assert drv.resolve is None
    # Bounded but retried more than once within the timeout window.
    assert stub.scriptapp.call_count >= 2


def test_connect_treats_scriptapp_exception_like_none_and_keeps_retrying():
    """A raising scriptapp() call (e.g. transient RPC hiccup) must not
    crash connect() -- treated the same as a None return, retried."""
    live = MagicMock()
    stub = _set_scriptapp(side_effect=[RuntimeError("transient"), live])
    drv = _fresh_driver()
    drv.CONNECT_RETRY_TIMEOUT = 5.0
    drv.CONNECT_RETRY_INTERVAL = 0.001

    ok = drv.connect()

    assert ok is True
    assert drv.resolve is live
    assert stub.scriptapp.call_count == 2


# ---------------------------------------------------------------------------
# _ensure_connected() -- cheap single-shot per-status-tick reconnect
# ---------------------------------------------------------------------------


def test_ensure_connected_reconnects_when_disconnected():
    live = MagicMock()
    stub = _set_scriptapp(return_value=live)
    drv = _fresh_driver()
    assert drv.resolve is None

    drv._ensure_connected()

    assert drv.resolve is live
    assert stub.scriptapp.call_count == 1


def test_ensure_connected_is_noop_single_attempt_when_still_disconnected():
    """Simulates Resolve still not reachable this tick -- must return
    (not loop/sleep) after exactly one scriptapp() attempt, leaving
    self.resolve None so the NEXT status tick tries again."""
    stub = _set_scriptapp(return_value=None)
    drv = _fresh_driver()

    drv._ensure_connected()

    assert drv.resolve is None
    assert stub.scriptapp.call_count == 1


def test_ensure_connected_does_nothing_once_already_connected():
    """No-op (zero scriptapp() calls) once self.resolve is already set --
    must never re-attempt while genuinely connected."""
    stub = _set_scriptapp(return_value=MagicMock())
    drv = _fresh_driver()
    already_connected = MagicMock()
    drv.resolve = already_connected

    drv._ensure_connected()

    assert drv.resolve is already_connected
    assert stub.scriptapp.call_count == 0


def test_ensure_connected_swallows_exception_and_stays_disconnected():
    stub = _set_scriptapp(side_effect=RuntimeError("still contended"))
    drv = _fresh_driver()

    drv._ensure_connected()  # must not raise

    assert drv.resolve is None


# ---------------------------------------------------------------------------
# _is_alive() -- cheap liveness probe (2026-07-08, Part A hardening)
# ---------------------------------------------------------------------------


def test_is_alive_true_when_probe_succeeds():
    drv = _fresh_driver()
    healthy = MagicMock()
    healthy.GetCurrentPage.return_value = "color"
    drv.resolve = healthy

    assert drv._is_alive() is True


def test_is_alive_true_even_when_probe_returns_none():
    """The PROBE's return value is never inspected -- only whether it
    raised. Resolve can legitimately return None/"" for GetCurrentPage()
    in some transient states without the connection itself being dead."""
    drv = _fresh_driver()
    handle = MagicMock()
    handle.GetCurrentPage.return_value = None
    drv.resolve = handle

    assert drv._is_alive() is True


def test_is_alive_false_when_probe_raises():
    drv = _fresh_driver()
    dead_handle = MagicMock()
    dead_handle.GetCurrentPage.side_effect = RuntimeError("Resolve gone")
    drv.resolve = dead_handle

    assert drv._is_alive() is False


def test_is_alive_false_when_resolve_is_none():
    """Defensive: _ensure_connected() always short-circuits before calling
    _is_alive() when self.resolve is None, but _is_alive() itself must
    still degrade gracefully (not raise) if ever called directly against
    a None handle."""
    drv = _fresh_driver()
    drv.resolve = None

    assert drv._is_alive() is False


# ---------------------------------------------------------------------------
# _ensure_connected() driven by _is_alive(), not just "is resolve None"
# (2026-07-08, Part A hardening)
# ---------------------------------------------------------------------------


def test_ensure_connected_reconnects_when_handle_is_dead_not_just_none():
    """The NEW case this hardening adds: self.resolve is SET (not None)
    but the liveness probe fails -- e.g. Resolve restarted and no read
    method dropped the handle to None anymore (see the "never poisons"
    tests above). _ensure_connected() must detect this itself via
    _is_alive() and reconnect, rather than staying stuck because
    self.resolve was never None to begin with."""
    dead_handle = MagicMock()
    dead_handle.GetCurrentPage.side_effect = RuntimeError("Resolve restarted")
    drv = _fresh_driver()
    drv.resolve = dead_handle

    live = MagicMock()
    live.GetCurrentPage.return_value = "edit"
    stub = _set_scriptapp(return_value=live)

    drv._ensure_connected()

    assert drv.resolve is live
    assert stub.scriptapp.call_count == 1


def test_ensure_connected_does_not_probe_when_already_none():
    """When self.resolve is already None, _ensure_connected() must not
    bother calling _is_alive() at all (nothing to probe) -- goes straight
    to a reconnect attempt. Verified indirectly: a None resolve with no
    GetCurrentPage stub configured must not raise via an attempted probe
    call on None."""
    stub = _set_scriptapp(return_value=MagicMock())
    drv = _fresh_driver()
    assert drv.resolve is None

    drv._ensure_connected()  # must not raise (no AttributeError on None probe)

    assert drv.resolve is not None
    assert stub.scriptapp.call_count == 1


# ---------------------------------------------------------------------------
# Part A hardening (2026-07-08): a read failure -- even against a genuinely
# dead handle (Resolve restarted underneath the sidecar) -- must NEVER null
# self.resolve anymore. Dead-handle DETECTION is now solely
# _ensure_connected()'s job, via _is_alive() (see the next section below) --
# not each individual read method's. A read simply returns its "couldn't
# determine this tick" value and leaves self.resolve exactly as it found it.
# ---------------------------------------------------------------------------


def test_current_page_never_poisons_resolve_on_exception():
    drv = _fresh_driver()
    dead_handle = MagicMock()
    dead_handle.GetCurrentPage.side_effect = RuntimeError("Resolve gone")
    drv.resolve = dead_handle

    page = drv.current_page()

    assert page is None
    assert drv.resolve is dead_handle  # NOT dropped to None


def test_current_node_never_poisons_resolve_on_exception():
    drv = _fresh_driver()
    dead_handle = MagicMock()
    dead_handle.GetProjectManager.side_effect = RuntimeError("Resolve gone")
    drv.resolve = dead_handle

    node = drv.current_node()

    assert node is None
    assert drv.resolve is dead_handle  # NOT dropped to None


def test_current_timecode_never_poisons_resolve_on_exception():
    drv = _fresh_driver()
    dead_handle = MagicMock()
    dead_handle.GetProjectManager.side_effect = RuntimeError("Resolve gone")
    drv.resolve = dead_handle

    tc, name = drv.current_timecode()

    assert tc is None
    assert name is None
    assert drv.resolve is dead_handle  # NOT dropped to None


def test_current_video_item_id_never_poisons_resolve_on_exception():
    """*** FIXED 2026-07-08 (connection-poisoning bug) ***: unlike
    current_page/current_node/current_timecode above, current_video_item_id
    must NEVER drop self.resolve to None on exception -- this is a
    deliberate, permanent divergence from those three methods' dead-handle
    contract (see the method's own docstring for the full live-verified
    incident: a cosmetic read failure here used to poison self.resolve,
    silently breaking bump_property/reset_property for every knob turn
    until the next status tick's _ensure_connected() happened to run).
    A raising GetProjectManager() here still correctly returns None (the
    clip-id genuinely can't be determined this tick) -- it just must not
    also kill the shared connection."""
    drv = _fresh_driver()
    raising_handle = MagicMock()
    raising_handle.GetProjectManager.side_effect = RuntimeError("transient")
    drv.resolve = raising_handle

    clip_id = drv.current_video_item_id()

    assert clip_id is None
    assert drv.resolve is raising_handle  # NOT dropped to None


def test_current_video_item_id_returns_top_occupied_unique_id():
    # FIXED 2026-07-08: resolves via the topmost-occupied video track's
    # clip (_occupied_video_items()), NOT GetCurrentVideoItem() -- two
    # occupied tracks (1 and 2) at the playhead, must return track 2's
    # (the higher one's) uid.
    drv = _fresh_driver()
    item1 = MagicMock()
    item1.GetUniqueId.return_value = "uid-track1"
    item1.GetStart.return_value = 0
    item1.GetEnd.return_value = 100
    item2 = MagicMock()
    item2.GetUniqueId.return_value = "uid-abc123"
    item2.GetStart.return_value = 0
    item2.GetEnd.return_value = 100
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = "00:00:00:05"
    tl.GetSetting.return_value = "25"
    tl.GetTrackCount.return_value = 2
    tl.GetItemListInTrack.side_effect = lambda kind, n: {1: [item1], 2: [item2]}.get(n, [])
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = tl
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve

    clip_id = drv.current_video_item_id()

    assert clip_id == "uid-abc123"
    assert drv.resolve is resolve  # healthy handle must not get dropped


def test_current_video_item_id_none_when_no_timeline():
    drv = _fresh_driver()
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = None
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve

    assert drv.current_video_item_id() is None
    assert drv.resolve is resolve  # "no timeline" is not a dead handle


def test_current_video_item_id_none_when_no_current_clip():
    """The playhead is on a genuine gap -- NO video track has a clip
    there. This is a valid Resolve state, not an error."""
    drv = _fresh_driver()
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = "00:00:00:05"
    tl.GetSetting.return_value = "25"
    tl.GetTrackCount.return_value = 1
    tl.GetItemListInTrack.return_value = []
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = tl
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve

    assert drv.current_video_item_id() is None
    assert drv.resolve is resolve


def test_current_video_item_id_returns_second_from_top_when_top_track_empty():
    """The bug this fix closes, mirrored at the clip-id layer: the
    top-numbered track is EMPTY at the playhead while a lower track is
    occupied -- must still resolve to that lower track's uid, not None."""
    drv = _fresh_driver()
    item1 = MagicMock()
    item1.GetUniqueId.return_value = "uid-track1"
    item1.GetStart.return_value = 0
    item1.GetEnd.return_value = 100
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = "00:00:00:05"
    tl.GetSetting.return_value = "25"
    tl.GetTrackCount.return_value = 3
    tl.GetItemListInTrack.side_effect = lambda kind, n: {1: [item1]}.get(n, [])
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = tl
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    drv.resolve = resolve

    assert drv.current_video_item_id() == "uid-track1"
    assert drv.resolve is resolve


def test_current_video_item_id_none_when_resolve_none():
    drv = _fresh_driver()
    drv.resolve = None
    assert drv.current_video_item_id() is None


# ---------------------------------------------------------------------------
# current_video_item_id() guarded throw points (2026-07-08 fix): each of
# these is a plausible root cause of the live incident (a data-shaped read
# failure, not a dead connection) -- every one must return None AND leave
# a healthy self.resolve handle untouched.
# ---------------------------------------------------------------------------


def _make_resolve_stub(tl):
    proj = MagicMock()
    proj.GetCurrentTimeline.return_value = tl
    pm = MagicMock()
    pm.GetCurrentProject.return_value = proj
    resolve = MagicMock()
    resolve.GetProjectManager.return_value = pm
    return resolve


def test_current_video_item_id_none_when_fps_raw_is_none():
    """GetSetting("timelineFrameRate") returning None (rather than a
    parseable string) used to raise inside float(fps_raw) -- caught by the
    old broad except, which nulled self.resolve. Now explicitly guarded."""
    drv = _fresh_driver()
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = "00:00:00:05"
    tl.GetSetting.return_value = None
    tl.GetTrackCount.return_value = 1
    resolve = _make_resolve_stub(tl)
    drv.resolve = resolve

    assert drv.current_video_item_id() is None
    assert drv.resolve is resolve


def test_current_video_item_id_none_when_fps_raw_unparseable():
    drv = _fresh_driver()
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = "00:00:00:05"
    tl.GetSetting.return_value = "not-a-number"
    tl.GetTrackCount.return_value = 1
    resolve = _make_resolve_stub(tl)
    drv.resolve = resolve

    assert drv.current_video_item_id() is None
    assert drv.resolve is resolve


def test_current_video_item_id_none_when_track_count_is_none():
    """GetTrackCount("video") returning None used to raise inside
    int(count) + 1 (_occupied_video_items) -- now guarded there directly
    (treated as zero tracks)."""
    drv = _fresh_driver()
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = "00:00:00:05"
    tl.GetSetting.return_value = "25"
    tl.GetTrackCount.return_value = None
    resolve = _make_resolve_stub(tl)
    drv.resolve = resolve

    assert drv.current_video_item_id() is None
    assert drv.resolve is resolve


def test_current_video_item_id_none_when_item_raises():
    """A specific occupied-track item raising mid-scan (e.g. GetStart()
    throwing on a transient Resolve-side hiccup for that one clip) must
    still degrade to None without poisoning self.resolve."""
    drv = _fresh_driver()
    bad_item = MagicMock()
    bad_item.GetStart.side_effect = RuntimeError("clip vanished mid-scan")
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = "00:00:00:05"
    tl.GetSetting.return_value = "25"
    tl.GetTrackCount.return_value = 1
    tl.GetItemListInTrack.return_value = [bad_item]
    resolve = _make_resolve_stub(tl)
    drv.resolve = resolve

    assert drv.current_video_item_id() is None
    assert drv.resolve is resolve


def test_current_video_item_id_none_when_timecode_malformed():
    drv = _fresh_driver()
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = "not-a-timecode"
    tl.GetSetting.return_value = "25"
    tl.GetTrackCount.return_value = 1
    resolve = _make_resolve_stub(tl)
    drv.resolve = resolve

    assert drv.current_video_item_id() is None
    assert drv.resolve is resolve


# ---------------------------------------------------------------------------
# bump_property/reset_property self-heal (2026-07-08 fix): sidecar.handle()
# now calls _ensure_connected() at the TOP of both op handlers -- the same
# self-heal call "status" already made -- so a poisoned/disconnected
# self.resolve reconnects within the SAME op call instead of silently
# no-op'ing (the "knobs do nothing" bug) until the next status tick.
# ---------------------------------------------------------------------------


def test_bump_property_op_calls_ensure_connected_before_bump():
    call_order = []
    drv = MagicMock()
    drv._ensure_connected.side_effect = lambda: call_order.append("ensure_connected")
    drv.bump_property.side_effect = (
        lambda **kw: call_order.append("bump_property") or (True, None, {"Pan": 1.0})
    )

    resp = sc.handle({"op": "bump_property", "property": "Pan", "delta": 1.0}, {}, drv)

    assert call_order == ["ensure_connected", "bump_property"]
    assert resp["ok"] is True


def test_reset_property_op_calls_ensure_connected_before_reset():
    call_order = []
    drv = MagicMock()
    drv._ensure_connected.side_effect = lambda: call_order.append("ensure_connected")
    drv.reset_property.side_effect = (
        lambda **kw: call_order.append("reset_property") or (True, None, {"Pan": 0.0})
    )

    resp = sc.handle({"op": "reset_property", "property": "Pan"}, {}, drv)

    assert call_order == ["ensure_connected", "reset_property"]
    assert resp["ok"] is True


def test_bump_property_op_reconnects_after_poisoned_resolve_end_to_end():
    """The actual regression, end-to-end: self.resolve starts None (the
    poisoned state a stray current_video_item_id() exception used to
    leave behind), but bump_property's own _ensure_connected() call
    reconnects it within this SAME call, so the knob turn still lands."""
    drv = _fresh_driver()
    drv.resolve = None  # simulate the poisoned/disconnected state

    item = MagicMock()
    item.GetProperty.side_effect = lambda name: 10.0
    item.SetProperty.return_value = True
    item.GetStart.return_value = 0
    item.GetEnd.return_value = 100
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = "00:00:00:05"
    tl.GetSetting.return_value = "25"
    tl.GetTrackCount.return_value = 1
    tl.GetItemListInTrack.side_effect = lambda kind, n: [item] if n == 1 else []
    live = _make_resolve_stub(tl)
    _set_scriptapp(return_value=live)

    resp = sc.handle({"op": "bump_property", "property": "Pan", "delta": 2.5}, {}, drv)

    assert resp["ok"] is True
    assert resp["values"] == {"Pan": 12.5}
    assert drv.resolve is live


def test_reset_property_op_reconnects_after_poisoned_resolve_end_to_end():
    drv = _fresh_driver()
    drv.resolve = None

    item = MagicMock()
    item.SetProperty.return_value = True
    item.GetStart.return_value = 0
    item.GetEnd.return_value = 100
    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = "00:00:00:05"
    tl.GetSetting.return_value = "25"
    tl.GetTrackCount.return_value = 1
    tl.GetItemListInTrack.side_effect = lambda kind, n: [item] if n == 1 else []
    live = _make_resolve_stub(tl)
    _set_scriptapp(return_value=live)

    resp = sc.handle({"op": "reset_property", "property": "Pan"}, {}, drv)

    assert resp["ok"] is True
    assert resp["values"] == {"Pan": 0.0}
    assert drv.resolve is live


def test_jog_seconds_op_calls_ensure_connected_before_jog_seconds():
    call_order = []
    drv = MagicMock()
    drv._ensure_connected.side_effect = lambda: call_order.append("ensure_connected")
    drv.jog_seconds.side_effect = (
        lambda seconds: call_order.append("jog_seconds") or (True, None, "00:01:10:00")
    )

    resp = sc.handle({"op": "jog_seconds", "seconds": 60}, {}, drv)

    assert call_order == ["ensure_connected", "jog_seconds"]
    assert resp["ok"] is True


def test_jog_seconds_op_reconnects_after_poisoned_resolve_end_to_end():
    """Same regression shape as bump_property/reset_property above: D1's
    jog_seconds binding has no per-tick status-poll self-heal of its own
    (unlike jog/jog_snap, which piggyback on a healthy handle set by a
    prior status tick) -- its own _ensure_connected() call must reconnect
    a poisoned/disconnected self.resolve within THIS same call."""
    drv = _fresh_driver()
    drv.resolve = None  # simulate the poisoned/disconnected state

    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = "00:00:10:00"
    tl.GetSetting.return_value = "25"
    tl.GetStartFrame.return_value = 0
    tl.GetEndFrame.return_value = 10**6
    tl.SetCurrentTimecode.return_value = True
    live = _make_resolve_stub(tl)
    _set_scriptapp(return_value=live)

    resp = sc.handle({"op": "jog_seconds", "seconds": 60}, {}, drv)

    assert resp["ok"] is True
    assert resp["tc"] == "00:01:10:00"
    assert drv.resolve is live


def test_jog_minute_snap_op_calls_ensure_connected_before_jog_minute_snap():
    call_order = []
    drv = MagicMock()
    drv._ensure_connected.side_effect = lambda: call_order.append("ensure_connected")
    drv.jog_minute_snap.side_effect = (
        lambda direction: call_order.append("jog_minute_snap") or (True, None, "00:04:00:00")
    )

    resp = sc.handle({"op": "jog_minute_snap", "direction": 1}, {}, drv)

    assert call_order == ["ensure_connected", "jog_minute_snap"]
    assert resp["ok"] is True


def test_jog_minute_snap_op_reconnects_after_poisoned_resolve_end_to_end():
    """Same regression shape as jog_seconds/bump_property/reset_property
    above: D1's jog_minute_snap binding has no per-tick status-poll
    self-heal of its own -- its own _ensure_connected() call must
    reconnect a poisoned/disconnected self.resolve within THIS same
    call."""
    drv = _fresh_driver()
    drv.resolve = None  # simulate the poisoned/disconnected state

    tl = MagicMock()
    tl.GetCurrentTimecode.return_value = "00:03:24:10"
    tl.GetSetting.return_value = "25"
    tl.GetStartFrame.return_value = 0
    tl.GetEndFrame.return_value = 10**6
    tl.SetCurrentTimecode.return_value = True
    live = _make_resolve_stub(tl)
    _set_scriptapp(return_value=live)

    resp = sc.handle({"op": "jog_minute_snap", "direction": 1}, {}, drv)

    assert resp["ok"] is True
    assert resp["tc"] == "00:04:00:00"
    assert drv.resolve is live


def test_current_page_healthy_handle_does_not_get_dropped():
    """Sanity check: a working handle must NOT be reset just because
    current_page() was called -- only an actual exception drops it."""
    drv = _fresh_driver()
    healthy = MagicMock()
    healthy.GetCurrentPage.return_value = "color"
    drv.resolve = healthy

    page = drv.current_page()

    assert page == "color"
    assert drv.resolve is healthy


def test_dead_handle_then_reconnects_next_status_tick():
    """End-to-end self-heal (2026-07-08 shape): a dead handle's read
    FAILS but does NOT drop to None on its own anymore (see the "never
    poisons" tests above) -- the VERY NEXT _ensure_connected() call (as
    sidecar.py's status handler runs it, at the top of the same handler,
    on the NEXT tick) detects the dead handle itself via _is_alive() and
    picks up a fresh live handle."""
    drv = _fresh_driver()
    dead_handle = MagicMock()
    dead_handle.GetCurrentPage.side_effect = RuntimeError("Resolve restarted")
    drv.resolve = dead_handle

    # Tick N: read fails, but self.resolve is left exactly as it was.
    assert drv.current_page() is None
    assert drv.resolve is dead_handle

    # Tick N+1: _ensure_connected() detects the dead handle via
    # _is_alive() (self.resolve was never None) and reconnects on its own.
    live = MagicMock()
    live.GetCurrentPage.return_value = "edit"
    _set_scriptapp(return_value=live)
    drv._ensure_connected()
    assert drv.resolve is live
    assert drv.current_page() == "edit"


def test_cosmetic_read_failure_does_not_cascade_to_other_ops():
    """The exact class of bug this hardening fixes: a read failure in ONE
    status-tick method (current_node here, standing in for the original
    current_video_item_id incident) must never leave self.resolve in a
    state where a DIFFERENT, unrelated op (bump_property) then fails too.
    Before this fix, a broad "drop to None on any exception" guard on the
    failing read would poison self.resolve for every subsequent op until
    the next lucky reconnect."""
    drv = _fresh_driver()
    flaky = MagicMock()
    flaky.GetProjectManager.side_effect = RuntimeError("transient hiccup")
    drv.resolve = flaky

    # current_node() fails this tick...
    assert drv.current_node() is None
    # ...but self.resolve is untouched, so a DIFFERENT op dispatched right
    # after in the same tick (as sidecar.py's ops do) still has a handle
    # to work with -- it may still fail on ITS OWN terms (this mock's
    # GetProjectManager always raises), but never because current_node()
    # poisoned the connection out from under it.
    assert drv.resolve is flaky
    ok, err, values = drv.bump_property(property_name="Pan", delta=1.0)
    assert ok is False
    assert "transient hiccup" in err
    # Still the SAME handle -- bump_property's own exception handling
    # (resolve_driver.py's bump_property) doesn't null it either.
    assert drv.resolve is flaky


# ---------------------------------------------------------------------------
# sidecar.handle()'s "status" op must call _ensure_connected() first.
# ---------------------------------------------------------------------------


def test_status_op_calls_ensure_connected():
    drv = MagicMock()
    drv.current_page.return_value = "color"
    drv.current_node.return_value = None
    drv.current_timecode.return_value = (None, None)

    sc.handle({"op": "status"}, {}, drv)

    drv._ensure_connected.assert_called_once()


def test_status_op_calls_ensure_connected_before_reading_page():
    """Ordering matters: _ensure_connected() must run BEFORE
    current_page()/current_node()/current_timecode() are read, so a
    reconnect that happens on this very tick is reflected in the SAME
    status response, not one tick later."""
    call_order = []
    drv = MagicMock()
    drv._ensure_connected.side_effect = lambda: call_order.append("ensure_connected")
    drv.current_page.side_effect = lambda: call_order.append("current_page") or "color"
    drv.current_node.side_effect = lambda: call_order.append("current_node") or None
    drv.current_timecode.side_effect = lambda: call_order.append("current_timecode") or (None, None)

    sc.handle({"op": "status"}, {}, drv)

    assert call_order[0] == "ensure_connected"


def test_status_op_still_works_when_disconnected_end_to_end():
    """A real ResolveDriver (not a MagicMock) whose sidecar just started
    disconnected: the status op must self-heal via _ensure_connected()
    and reflect the reconnect in this SAME response."""
    drv = _fresh_driver()
    assert drv.resolve is None
    live = MagicMock()
    live.GetCurrentPage.return_value = "edit"
    live.GetProjectManager.return_value.GetCurrentProject.return_value = None
    _set_scriptapp(return_value=live)

    resp = sc.handle({"op": "status"}, {}, drv)

    assert resp["ok"] is True
    assert resp["page"] == "edit"
    assert drv.resolve is live
