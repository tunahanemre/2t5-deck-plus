"""
Dispatcher-level coverage for sidecar.py's handle() (Bug A hardening,
2026-08-12).

Context: sidecar.py:346 called `driver.marker_at_playhead()` inside
handle() -- handle(msg, nodes, drv)'s parameter is `drv`, not `driver` --
so every call raised an uncaught NameError that took the ENTIRE sidecar
process down (killing the Resolve gate, timecode reads, markers and clip
selection until the app was relaunched). This was never caught because
every existing test exercises ResolveDriver methods (e.g.
ResolveDriver.marker_at_playhead) DIRECTLY and never goes through
handle()'s dispatcher -- the exact wiring layer the bug was in.

This file closes that gap generically: it enumerates every op name
handle() actually supports BY PARSING sidecar.py's own source (not a
hand-maintained list a future op could silently bypass), then calls
handle() -- the real dispatcher, not ResolveDriver directly -- for each
one with a fully-populated mock driver, asserting no exception escapes and
a dict comes back. A future NameError/AttributeError typo in any op's
wiring, in any branch, fails this test at that exact op.

Process-level crash hardening itself (an exception inside handle() must
degrade to one failed op, not kill the process) lives in
serve_connection() in sidecar.py -- exercised separately in
test_serve_connection_hardening.py, since that boundary is one level
ABOVE handle() and is not observable by calling handle() directly.
"""
import ast
import os
import sys
from unittest.mock import MagicMock

import pytest

# ---------------------------------------------------------------------------
# Import handle() from sidecar.py without executing main() or touching a
# real Resolve install -- same pattern as test_sidecar_status.py.
# ---------------------------------------------------------------------------


def _sidecar_path():
    return os.path.join(os.path.dirname(os.path.abspath(__file__)), "sidecar.py")


def _import_sidecar_handle():
    sidecar_dir = os.path.dirname(os.path.abspath(__file__))
    if sidecar_dir not in sys.path:
        sys.path.insert(0, sidecar_dir)

    dvr_stub = MagicMock()
    dvr_stub.scriptapp.return_value = MagicMock()
    sys.modules.setdefault("DaVinciResolveScript", dvr_stub)

    import sidecar as sc
    return sc.handle


handle = _import_sidecar_handle()


# ---------------------------------------------------------------------------
# Enumerate every op name handle() checks for, straight from the AST of
# sidecar.py's own source. Covers both "if op == ..." and "elif op == ..."
# (elif is just a nested If in the AST, ast.walk descends into it) and
# both operand orders (op == "x" and "x" == op) even though the source
# today always writes it as op == "x" -- future-proofing the scan itself
# rather than the specific style handle() happens to use right now.
# ---------------------------------------------------------------------------


def _enumerate_ops():
    src = open(_sidecar_path()).read()
    tree = ast.parse(src)
    handle_fn = next(
        n for n in tree.body
        if isinstance(n, ast.FunctionDef) and n.name == "handle"
    )

    ops = []
    for node in ast.walk(handle_fn):
        if not (isinstance(node, ast.Compare) and len(node.ops) == 1
                and isinstance(node.ops[0], ast.Eq)):
            continue
        left, right = node.left, node.comparators[0]
        for a, b in ((left, right), (right, left)):
            if (isinstance(a, ast.Name) and a.id == "op"
                    and isinstance(b, ast.Constant) and isinstance(b.value, str)):
                if b.value not in ops:
                    ops.append(b.value)
    return ops


def test_enumerate_ops_finds_the_known_op_count():
    """Sanity check on the scan itself, not on handle()'s behavior: if this
    drifts, either a new op was added (update the count, and see the
    failure below for the REAL signal -- an unconfigured op message) or
    the AST scan regressed and is silently seeing fewer ops than exist,
    which would make every other test in this file a false pass."""
    ops = _enumerate_ops()
    assert len(ops) >= 26, (
        f"expected at least 26 ops from the AST scan of handle(), got "
        f"{len(ops)}: {ops!r} -- did the scan itself break?"
    )


# ---------------------------------------------------------------------------
# A fully-populated mock ResolveDriver: every method any op branch in
# handle() calls gets an explicit return_value shaped exactly like the
# real ResolveDriver method's documented return type (tuple arity,
# dict-or-None, etc.) -- a bare MagicMock() would fail multi-value
# unpacking (e.g. "ok, err, values = drv.bump_property(...)") with a
# TypeError that has NOTHING to do with handle()'s own wiring, which
# would make this test noisy/wrong rather than a precise wiring check.
# ---------------------------------------------------------------------------


def _full_mock_driver():
    drv = MagicMock()
    drv.current_timecode.return_value = ("01:00:00:00", "TL01")
    drv.timeline_duration_frames.return_value = (100, 25)
    drv.active_clip_range.return_value = (0, 100, 100, "clip.mov", 0, 0)
    drv.timeline_clip_map.return_value = (1, 1, 0, "-")
    drv.timeline_extent.return_value = (0, 100)
    drv.current_playhead_frame.return_value = 10
    drv.current_page.return_value = "edit"
    drv.current_node.return_value = None
    drv.current_video_item_id.return_value = "uid-1"
    drv.edit_clip_info.return_value = None
    drv.refresh.return_value = True
    drv.bump_property.return_value = (True, None, {})
    drv.reset_property.return_value = (True, None, {})
    drv.clips_at_playhead.return_value = (True, None, [])
    drv.highlight_clip.return_value = (True, None)
    drv.clear_highlight.return_value = (True, None)
    drv.set_clips_color.return_value = (True, None, 0)
    drv.color_by_render_state.return_value = (True, None, 0, 0)
    drv.select_clip_vertical.return_value = (True, None, 1, "uid-2", 0, 10, "Blue")
    drv.select_clip_horizontal.return_value = (True, None, 1, "uid-2", 0, 10, "Blue")
    drv.restore_highlight.return_value = (True, None)
    drv.jog.return_value = (True, None, "01:00:00:05")
    drv.jog_snap.return_value = (True, None, "01:00:00:05")
    drv.jog_seconds.return_value = (True, None, "01:00:01:00")
    drv.jog_minute_snap.return_value = (True, None, "01:01:00:00")
    drv.render.return_value = (True, None, {})
    drv.add_marker.return_value = (True, None, 10, 25, 25.0)
    drv.marker_at_playhead.return_value = {"found": False}
    drv.resize_marker.return_value = (True, None, 30, 25.0)
    drv.set_marker_color.return_value = (True, None)
    drv.delete_marker.return_value = (True, None)
    drv.write.return_value = (True, None)
    return drv


# ---------------------------------------------------------------------------
# One representative, well-formed request message per op -- enough to
# reach and exercise every real branch in handle() (including the "node"
# CDL tail: delta/sat/reset/reset_param, which use a REAL NodeCDL, not the
# mock driver, so their param/ch values must be ones NodeCDL actually
# accepts, e.g. cdl_state.py's CH={"r","g","b"} and _NEUTRAL's param keys).
# Deliberately NOT exhaustive of every optional field/branch each op has
# (e.g. jog's snap_seconds variant, add_marker's track pass-through) --
# those are covered in depth by the feature-specific test files
# (test_edit_page_ops.py, test_add_marker_ops.py, etc). This file's job is
# breadth (every op reachable at all through the real dispatcher), not
# depth.
# ---------------------------------------------------------------------------

_OP_MESSAGES = {
    "ping": {},
    "status": {},
    "refresh": {},
    "set_node": {"node": 1},
    "bump_property": {"property": "pan", "gang": False, "delta": 1.0},
    "reset_property": {"property": "pan"},
    "clips_at_playhead": {},
    "highlight_clip": {"track": 1, "color": "Blue"},
    "clear_highlight": {},
    "set_clips_color": {"color": "Blue"},
    "color_by_render_state": {"rendered": "Green", "neutral": "Yellow"},
    "select_clip_vertical": {"direction": 1, "current_track": 0},
    "select_clip_horizontal": {"direction": 1, "current_track": 0, "current_start": 0},
    "restore_highlight": {},
    "jog": {"frames": 5},
    "jog_seconds": {"seconds": 60.0},
    "jog_minute_snap": {"direction": 1},
    "render": {"target_dir": "/tmp/2t5-deck-plus-test"},
    "add_marker": {"duration_seconds": 1.0, "color": "Blue", "name": "M01"},
    "marker_at_playhead": {},
    "resize_marker": {"frame": 10, "delta_frames": 5},
    "set_marker_color": {"frame": 10, "color": "Blue"},
    "delete_marker": {"frame": 10},
    "delta": {"node": 1, "param": "slope", "ch": "master", "amt": 0.1},
    "sat": {"node": 1, "amt": 0.1},
    "reset": {"node": 1},
    "reset_param": {"node": 1, "param": "slope", "ch": "master"},
}


def test_every_configured_op_has_a_real_source_op_backing_it():
    """Inverse check: catches a stale entry in _OP_MESSAGES for an op that
    no longer exists in handle() (e.g. renamed/removed) -- keeps the map
    honest in both directions."""
    ops = set(_enumerate_ops())
    stale = [op for op in _OP_MESSAGES if op not in ops]
    assert not stale, (
        f"_OP_MESSAGES has entries for ops handle() no longer defines: "
        f"{stale!r} -- were they renamed/removed?"
    )


def test_every_op_from_source_has_a_configured_message():
    """The core "cannot silently go uncovered" guarantee: if a new
    `if op == "something_new":` branch is added to handle() without a
    matching entry here, THIS assertion fails immediately (loudly, at
    review/CI time) instead of the new op quietly never being dispatcher-
    tested until it breaks in production the way marker_at_playhead did."""
    ops = _enumerate_ops()
    missing = [op for op in ops if op not in _OP_MESSAGES]
    assert not missing, (
        f"handle() supports op(s) with no test message configured in "
        f"_OP_MESSAGES: {missing!r} -- add an entry so this file actually "
        f"dispatches through handle() for it."
    )


@pytest.mark.parametrize("op", _enumerate_ops())
def test_op_dispatches_through_handle_without_exception(op):
    """The actual regression test for the marker_at_playhead class of bug:
    calls the REAL handle() dispatcher (not ResolveDriver directly) for
    every known op, with a fully-populated mock driver standing in for a
    working Resolve connection. A NameError/AttributeError/etc. from a
    wiring typo in ANY op's branch fails here, pinpointed to that op."""
    if op not in _OP_MESSAGES:
        pytest.skip(f"op {op!r} has no configured message -- see "
                     f"test_every_op_from_source_has_a_configured_message")

    drv = _full_mock_driver()
    msg = {"op": op, **_OP_MESSAGES[op]}

    try:
        resp = handle(msg, {}, drv)
    except Exception as exc:  # noqa: BLE001 -- this IS the assertion
        pytest.fail(f"handle() raised for op={op!r}: {exc!r}")

    assert isinstance(resp, dict), (
        f"handle() for op={op!r} did not return a dict: {resp!r}"
    )
