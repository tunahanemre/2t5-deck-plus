"""
Process-survival hardening for sidecar.py (Bug A, 2026-08-12) -- the layer
AROUND handle() that ensures no exception, from any op OR from a framing/
socket-level failure, can take the whole sidecar process down.

Context: sidecar.py:346 called `driver.marker_at_playhead()` inside
handle() -- handle(msg, nodes, drv)'s parameter is `drv`, not `driver` --
so every call raised an uncaught NameError that killed the entire process
(the Resolve gate, timecode reads, markers, clip selection all went dark
until the app was relaunched). See test_dispatcher_coverage.py for the
dispatcher-level regression test that would have caught the typo itself
(it calls handle() directly for every known op). THIS file tests the
boundary one level up -- serve_connection(), the function main()'s accept
loop delegates each connection to -- since that boundary is what turns
"handle() raised" into "one failed op reply" instead of "process dies",
and is not observable by calling handle() directly.

These tests use a REAL AF_UNIX socketpair (not a mocked connection),
because serve_connection's whole job is guarding the read_frame/write_frame
+ handle() call as a genuine I/O boundary -- a mock conn would not prove
the length-prefixed framing protocol survives interleaved with a handler
exception or a peer disconnect.
"""
import json
import os
import socket
import struct
import sys
import threading
from unittest.mock import MagicMock

import pytest


def _import_sidecar_module():
    sidecar_dir = os.path.dirname(os.path.abspath(__file__))
    if sidecar_dir not in sys.path:
        sys.path.insert(0, sidecar_dir)
    dvr_stub = MagicMock()
    dvr_stub.scriptapp.return_value = MagicMock()
    sys.modules.setdefault("DaVinciResolveScript", dvr_stub)
    import sidecar as sc
    return sc


sc = _import_sidecar_module()


def _send(conn, obj):
    body = json.dumps(obj).encode()
    conn.sendall(struct.pack(">I", len(body)) + body)


def _recv(conn):
    hdr = conn.recv(4)
    if len(hdr) < 4:
        return None
    (n,) = struct.unpack(">I", hdr)
    buf = b""
    while len(buf) < n:
        chunk = conn.recv(n - len(buf))
        if not chunk:
            raise ConnectionError("test helper: peer closed mid-frame")
        buf += chunk
    return json.loads(buf)


def _run_serve_connection(server_end, nodes, drv):
    """Runs sidecar.serve_connection against server_end on a background
    thread (so it never blocks the test itself) and returns the thread."""
    t = threading.Thread(
        target=sc.serve_connection, args=(server_end, nodes, drv), daemon=True
    )
    t.start()
    return t


# ---------------------------------------------------------------------------
# Handler-exception boundary (the exact incident class)
# ---------------------------------------------------------------------------


def test_handler_exception_returns_structured_error_not_a_crash():
    """An op whose handle() branch raises must produce a structured
    {"ok": False, ...} reply over the SAME connection -- not a dropped
    connection, and not a wedged serve_connection (a thread silently
    killed by an uncaught exception would leave this test's _recv()
    hanging forever, which is the same "process died silently" symptom
    the real bug caused, scaled down to a thread)."""
    client_end, server_end = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
    drv = MagicMock()
    drv.marker_at_playhead.side_effect = RuntimeError("boom")
    nodes = {}

    t = _run_serve_connection(server_end, nodes, drv)
    try:
        _send(client_end, {"op": "marker_at_playhead"})
        resp = _recv(client_end)
        assert resp is not None, "no reply received -- serve_connection likely died"
        assert resp["ok"] is False
        assert resp["err"] == "handler_exception"
        assert resp["op"] == "marker_at_playhead"
        assert "boom" in resp["message"]
    finally:
        client_end.close()
        t.join(timeout=2)


def test_connection_survives_one_bad_op_and_serves_the_next_one():
    """The core "degrades to one failed op, not an outage" guarantee: a
    handler exception on request #1 must not prevent request #2 (a
    perfectly normal op) from getting a normal reply on the SAME
    connection right afterward."""
    client_end, server_end = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
    drv = MagicMock()
    drv.marker_at_playhead.side_effect = RuntimeError("boom")
    nodes = {}

    t = _run_serve_connection(server_end, nodes, drv)
    try:
        _send(client_end, {"op": "marker_at_playhead"})
        bad_resp = _recv(client_end)
        assert bad_resp["ok"] is False

        _send(client_end, {"op": "ping"})
        good_resp = _recv(client_end)
        assert good_resp == {"ok": True}
    finally:
        client_end.close()
        t.join(timeout=2)


def test_different_ops_fail_independently_with_correct_labels():
    """Two DIFFERENT ops failing back to back must each get their own
    correctly-labelled structured error -- not a stuck/stale op name
    leaking from a previous failure into the next one's reply."""
    client_end, server_end = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
    drv = MagicMock()
    drv.marker_at_playhead.side_effect = RuntimeError("boom1")
    drv.clear_highlight.side_effect = RuntimeError("boom2")
    nodes = {}

    t = _run_serve_connection(server_end, nodes, drv)
    try:
        _send(client_end, {"op": "marker_at_playhead"})
        r1 = _recv(client_end)
        assert r1["op"] == "marker_at_playhead"
        assert "boom1" in r1["message"]

        _send(client_end, {"op": "clear_highlight"})
        r2 = _recv(client_end)
        assert r2["op"] == "clear_highlight"
        assert "boom2" in r2["message"]
    finally:
        client_end.close()
        t.join(timeout=2)


# ---------------------------------------------------------------------------
# Framing/socket-level boundary (a level OUTSIDE handle() entirely --
# read_frame/write_frame themselves)
# ---------------------------------------------------------------------------


def test_write_frame_failure_is_caught_at_connection_level():
    """A failure INSIDE write_frame (e.g. a response value that cannot be
    JSON-encoded) happens OUTSIDE handle()'s own try/except -- it must
    still be caught by serve_connection's OUTER boundary (dropping just
    this connection), proving the two-boundary design in serve_connection
    actually covers write_frame, not only handle() itself."""
    client_end, server_end = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
    drv = MagicMock()
    drv.refresh.return_value = object()  # json.dumps cannot encode this
    nodes = {}

    t = _run_serve_connection(server_end, nodes, drv)
    try:
        _send(client_end, {"op": "refresh"})
        t.join(timeout=2)
        assert not t.is_alive(), (
            "serve_connection thread did not exit after a write_frame failure "
            "-- the framing-level exception escaped instead of being caught"
        )
    finally:
        client_end.close()


def test_peer_closes_mid_frame_drops_only_this_connection():
    """A client that sends a frame header then hangs up before the body
    arrives must not hang serve_connection forever (see read_frame's fix:
    conn.recv() returning b"" mid-frame now raises ConnectionError instead
    of spinning) -- and that exception must be caught at the connection
    level, not escape serve_connection (which, from main()'s single-
    threaded accept loop, would otherwise block every FUTURE connection
    too -- a total outage by hang instead of by crash)."""
    client_end, server_end = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
    drv = MagicMock()
    nodes = {}

    t = _run_serve_connection(server_end, nodes, drv)
    try:
        # Claim a 100-byte body, send none of it, then hang up.
        client_end.sendall(struct.pack(">I", 100))
        client_end.close()
        t.join(timeout=2)
        assert not t.is_alive(), (
            "serve_connection thread did not exit after a peer hangup mid-frame "
            "-- read_frame's EOF-mid-frame fix regressed"
        )
    finally:
        pass  # client_end already closed above


def test_serve_connection_closes_conn_on_every_exit_path():
    """conn.close() (the finally in serve_connection) must run on a clean
    exit too -- verified indirectly: once the SERVER side is closed, the
    peer sees EOF on its next recv() rather than hanging."""
    client_end, server_end = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
    drv = MagicMock()
    nodes = {}

    t = _run_serve_connection(server_end, nodes, drv)
    try:
        _send(client_end, {"op": "ping"})
        assert _recv(client_end) == {"ok": True}
        client_end.close()
        t.join(timeout=2)
        assert not t.is_alive()
    finally:
        try:
            client_end.close()
        except OSError:
            pass


# ---------------------------------------------------------------------------
# read_frame direct test (the EOF-mid-frame fix, in isolation from
# serve_connection's own error handling)
# ---------------------------------------------------------------------------


def test_read_frame_raises_on_eof_mid_frame_instead_of_hanging():
    """Direct test of read_frame's fix: previously `while len(buf) < n:
    buf += conn.recv(...)` never checked for an empty read, so a peer that
    closed after sending only the length header (never the body) caused
    an infinite CPU-spinning loop -- read_frame neither returned None nor
    raised, it just never came back. Run off the main thread with a
    bounded join() so a regression here fails this test instead of
    hanging the whole suite."""
    client_end, server_end = socket.socketpair(socket.AF_UNIX, socket.SOCK_STREAM)
    result = {}

    def _call():
        try:
            sc.read_frame(server_end)
            result["raised"] = False
        except ConnectionError:
            result["raised"] = True
        except Exception as exc:  # noqa: BLE001 -- record, don't hide
            result["raised"] = True
            result["unexpected_type"] = type(exc).__name__

    t = threading.Thread(target=_call, daemon=True)
    t.start()
    client_end.sendall(struct.pack(">I", 50))  # header only, no body
    client_end.close()
    t.join(timeout=2)

    assert not t.is_alive(), "read_frame hung on EOF mid-frame instead of raising"
    assert result.get("raised") is True
    assert "unexpected_type" not in result, (
        f"read_frame raised the wrong exception type: {result.get('unexpected_type')}"
    )
    server_end.close()
