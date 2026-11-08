"""
_listener_alive() (2026-08-13 fix, "duplicate sidecar feedback loop" live
incident) -- the guard main() now runs BEFORE its own os.unlink(SOCK)+bind,
so it never silently steals the socket out from under a still-alive
predecessor. See sidecar.py's main() doc comment for the full incident:
before this, a recovery-spawned sidecar always won the takeover, orphaning
the previous (merely busy, not dead) process -- still running, still
holding a DaVinci Resolve script-connection slot, just unreachable. Live
evidence: three sidecar.py processes found running simultaneously.

These tests exercise _listener_alive directly against REAL AF_UNIX sockets
(no mocking) -- the whole point is proving it tells a live listener apart
from a merely-stale socket FILE, which only real bind()/listen()/connect()
semantics can prove.
"""
import os
import socket
import sys
from unittest.mock import MagicMock


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


def _unique_sock_path(tag):
    return "/tmp/ldr-listener-alive-%s-%d.sock" % (tag, os.getpid())


def test_listener_alive_false_when_path_does_not_exist():
    path = _unique_sock_path("nofile")
    assert not os.path.exists(path)
    assert sc._listener_alive(path) is False


def test_listener_alive_false_for_stale_socket_file():
    """A socket FILE left behind by a process that died without cleanup --
    bound but never listen()ed, then closed -- must NOT read as alive.
    This is the exact "safe to take over" case main() must still allow."""
    path = _unique_sock_path("stale")
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.bind(path)
    s.close()  # never called listen(); the socket file remains on disk
    try:
        assert os.path.exists(path)
        assert sc._listener_alive(path) is False
    finally:
        os.unlink(path)


def test_listener_alive_true_for_a_real_listener():
    path = _unique_sock_path("live")
    srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    srv.bind(path)
    srv.listen(1)
    try:
        assert sc._listener_alive(path) is True
    finally:
        srv.close()
        os.unlink(path)


def test_listener_alive_false_after_the_listener_closes():
    """A listener that WAS alive and then shuts down (without unlinking)
    must flip back to "not alive" -- proves this isn't a one-shot/cached
    check but reflects live state at call time."""
    path = _unique_sock_path("thenclosed")
    srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    srv.bind(path)
    srv.listen(1)
    assert sc._listener_alive(path) is True
    srv.close()
    try:
        assert sc._listener_alive(path) is False
    finally:
        if os.path.exists(path):
            os.unlink(path)
