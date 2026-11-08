import json, os, socket, struct, sys, traceback
from cdl_state import NodeCDL
from resolve_driver import ResolveDriver

SOCK = "/tmp/2t5-deck-plus.sock"

# REMOVED 2026-08-25: this file used to also bind a SECOND socket,
# PING_SOCK (= SOCK + ".ping"), served by its own always-on
# `threading.Thread` (_serve_ping_forever/_handle_ping_conn, both removed),
# so the Go side (internal/ipc.PingAlive, also removed) could ask "is the
# process alive?" independently of whatever SOCK's single connection was
# doing (2026-08-24 fix, live production bug).
#
# It was defeated the very next day (2026-08-25, live log evidence: 8
# spurious recoveries from one playback session) by a fact that "a second
# socket on a second thread" does not escape: CPython's GIL is PROCESS-
# wide, not per-socket or per-thread. handle()'s "status" op blocks inside
# DaVinciResolveScript / fusionscript.so (Resolve's C extension) during
# playback, and that call does not release the GIL -- so NO other Python
# thread in this process can run either, including the ping listener's.
# The probe timed out at exactly the moments "status" did, reporting
# "wedged or dead" for a perfectly healthy, merely-busy sidecar. A second
# thread inside the SAME interpreter can never be a liveness probe against
# a call that holds the GIL, no matter which socket it listens on.
#
# The fix on the Go side (engine.go's queryStatus) is a RULE change, not a
# better probe: a "status" timeout never triggers recovery, period --
# regardless of count or duration -- and only a hard transport error on
# SOCK itself (this process actually exiting, closing SOCK) does. Nothing
# on the Python side needs to answer anything while busy; there is nothing
# left for a second socket to prove. See engine.go's Engine struct field
# comment (the block that used to be consecutiveStatusTimeouts) for the
# full three-attempt history.
#
# The ORIGINAL, unrelated "ping" op inside handle() below (op == "ping",
# served over SOCK itself, pre-dating PING_SOCK) is NOT part of what was
# removed here and stays exactly as it was.

def read_frame(conn):
    hdr = conn.recv(4)
    if len(hdr) < 4:
        return None
    (n,) = struct.unpack(">I", hdr)
    buf = b""
    while len(buf) < n:
        chunk = conn.recv(n - len(buf))
        if not chunk:
            # Peer closed (or half-closed its write side) mid-frame.
            # conn.recv() on a closed stream returns b"" immediately and
            # keeps doing so forever -- without this check, this loop
            # spins forever (never sleeping, never returning, never
            # raising), hanging serve_connection() on this ONE connection
            # forever. Because main()'s accept loop is single-threaded
            # (see serve_connection()'s doc comment), a hung connection
            # here is just as much a total outage as the uncaught-
            # exception crash this same hardening pass fixed elsewhere --
            # it just fails silently (100% CPU, no log) instead of loudly.
            # Raising here routes it through serve_connection's normal
            # framing-level except-and-drop-this-connection path instead.
            raise ConnectionError(
                "sidecar: peer closed mid-frame (got %d of %d bytes)" % (len(buf), n)
            )
        buf += chunk
    return json.loads(buf)

def write_frame(conn, obj):
    body = json.dumps(obj).encode()
    conn.sendall(struct.pack(">I", len(body)) + body)

def _listener_alive(path):
    """Return True if something is actively accepting connections at
    `path` right now -- used by main() to refuse a silent takeover (see
    main()'s doc comment on the 2026-08-13 "duplicate sidecar feedback
    loop" incident).

    A socket FILE existing at `path` is NOT proof of a live listener: a
    process that died (killed, crashed) without cleanup leaves that file
    behind, bound to a now-closed socket -- connecting to it fails
    immediately with ECONNREFUSED, the OS's way of saying "nothing is
    listening here anymore." That is exactly the "stale, safe to take
    over" case, not "something is alive" -- so a connect() FAILURE here
    means False (safe to proceed), and only a successful connect() means
    True (a real predecessor is listening and must not be disturbed).
    """
    if not os.path.exists(path):
        return False
    probe = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    probe.settimeout(0.3)
    try:
        probe.connect(path)
    except OSError:
        return False
    finally:
        probe.close()
    return True


def main():
    nodes = {}  # node_index -> NodeCDL

    # Refuse to silently steal SOCK out from under a live predecessor
    # (2026-08-13 fix, "duplicate sidecar feedback loop" live incident).
    # Before this check, main() unconditionally os.unlink()ed and rebound
    # SOCK -- if a PREVIOUS sidecar.py was still alive and simply busy (a
    # slow DaVinci Resolve scripting call, not dead), this orphaned it:
    # still running, still holding a Resolve script-connection slot, but
    # unreachable, since every new client connection now went to THIS
    # process instead. Repeated across a few recovery cycles this piled up
    # multiple orphaned sidecar.py processes fighting over Resolve's
    # limited script-connection slots -- live evidence: three sidecar.py
    # processes found running simultaneously. The Go engine now also kills
    # its own previously-tracked sidecar BEFORE spawning a replacement
    # (see engine.go's recoverSidecar) -- this check is the second,
    # independent line of defense for any launch path that doesn't go
    # through that kill-first sequence (a manual double-launch, a future
    # bug of the same shape, etc.). See _listener_alive's doc comment for
    # why a merely-stale socket FILE must never trip this.
    if _listener_alive(SOCK):
        print(
            "sidecar: refusing to start -- another sidecar is already "
            "listening on %s. Exiting without touching it." % SOCK,
            file=sys.stderr,
            flush=True,
        )
        sys.exit(1)

    drv = ResolveDriver()
    drv.connect()
    if os.path.exists(SOCK):
        os.unlink(SOCK)
    srv = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    srv.bind(SOCK)
    srv.listen(1)
    print("sidecar listening on", SOCK, flush=True)

    while True:
        conn, _ = srv.accept()
        serve_connection(conn, nodes, drv)


def serve_connection(conn, nodes, drv):
    """Serve one accepted connection until the client disconnects, then
    close it and return -- never lets an exception escape to main()'s
    accept loop and take the whole sidecar process down.

    Two error boundaries, deliberately different in scope (see the
    2026-08-12 "marker_at_playhead called undefined 'driver'" incident --
    an uncaught NameError inside a single op's handle() branch killed the
    entire process, silently taking the Resolve gate, timecode reads,
    markers and clip selection down with it until the app was relaunched):

      - A handler exception -- anything handle() itself raises, e.g. a bug
        in one op's wiring -- is caught PER-MESSAGE, right around the
        handle() call: the traceback is logged (so it's still visible in
        the sidecar's stdout/stderr, same as an uncaught one would have
        been) and a structured {"ok": False, "err": "handler_exception",
        "op": ...} reply is sent back over THIS SAME connection instead of
        letting the exception propagate. The loop then continues -- one
        bad op degrades to one failed op, not a dead connection or a dead
        process.
      - A framing/socket-level exception (a malformed frame, the client
        hanging up mid-write, a broken pipe) is NOT any op's fault --
        read_frame/write_frame can raise entirely outside of handle().
        That is caught at the connection level: this ONE connection is
        dropped and logged, while main()'s accept loop keeps running and
        can accept the next connection.
    """
    try:
        while True:
            msg = read_frame(conn)
            if msg is None:
                break
            op = msg.get("op") if isinstance(msg, dict) else None
            try:
                resp = handle(msg, nodes, drv)
            except Exception as exc:
                traceback.print_exc()
                resp = {
                    "ok": False,
                    "err": "handler_exception",
                    "op": op,
                    "message": str(exc),
                }
            write_frame(conn, resp)
    except Exception:
        # Connection/framing-level failure -- not an op's fault, and there
        # is no reliable peer left to reply to. Drop this connection only;
        # the process and the accept loop live on.
        traceback.print_exc()
    finally:
        conn.close()


def handle(msg, nodes, drv):
    op = msg.get("op")
    if op == "ping":
        return {"ok": True}
    if op == "status":
        # Returns the current Resolve page AND current_node info so the Go
        # gate can decide whether to post mouse drags and whether the active
        # node is a CST (color space transform) or Fusion node.
        # "timecode"/"timeline_name" (HUD v2, docs/specs/2026-07-06-ld-
        # resolve-hud-rich.md) feed the HUD's timecode-hero row -- both are
        # None/null when there's no live timeline, which the Go engine
        # threads straight through as "" (formatHUD renders dashes).
        #
        # "clip_id" (D1 selectClip auto-reset, still populated -- see
        # ResolveDriver.current_video_item_id()'s docstring): GetCurrentVideoItem
        # ().GetUniqueId(), or null when there's no current clip -- the Go
        # engine compares this tick-over-tick to detect "the top-occupied
        # clip changed" for D1's OLD selectClip auto-reset (that BINDING is
        # unreachable in production -- D1 is now bound to "jogMinuteSnap"
        # instead, see the "jog_minute_snap" op below, an unrelated
        # feature that never consults clip_id at all -- still unit-tested;
        # "jogSeconds"/"jog_seconds" was D1's binding for one feature in
        # between and is likewise kept but unreachable).
        #
        # "playhead_frame" (direction-button clip-selection auto-reset):
        # the absolute playhead frame number, or null on any timeline-
        # resolution refusal -- the Go engine compares this against a
        # selected clip's stored [start, end) range to detect "the
        # playhead moved outside the selected clip" and auto-clears the
        # Up/Down/Left/Right selection. See
        # ResolveDriver.current_playhead_frame()'s docstring.
        #
        # "edit_clip" (HUD v2 Edit-page readout, 2026-07-08): the target
        # clip's name + live Pan/Tilt/ZoomX, or null when there's no
        # target clip. "track" -- same optional D1/direction-selection
        # override pass-through convention as bump_property/reset_property
        # above -- lets this read the EXACT clip the property/reset knobs
        # currently affect, not a second/independent "current clip"
        # concept. See ResolveDriver.edit_clip_info()'s docstring.
        #
        # Self-healing reconnect: the Go engine polls this op every 250ms,
        # so a cheap single-shot reconnect attempt here (no-op once already
        # connected) means a sidecar that started disconnected -- or whose
        # handle went dead after a Resolve restart -- recovers within one
        # tick, with no app restart needed. See
        # ResolveDriver._ensure_connected()'s docstring.
        drv._ensure_connected()
        timecode, timeline_name = drv.current_timecode()
        _tl_dur = drv.timeline_duration_frames()
        _clip_rng = drv.active_clip_range()
        _cmap = drv.timeline_clip_map()
        _ph_pm = ""
        try:
            _tl0, _span = drv.timeline_extent()
            _phf = drv.current_playhead_frame()
            if _phf is not None and _span > 0:
                _ph_pm = "%d;" % max(0, min(1000, (int(_phf) - _tl0) * 1000 // _span))
        except Exception:
            _ph_pm = ""
        return {
            "ok": True,
            "page": drv.current_page(),
            "current_node": drv.current_node(),
            "timecode": timecode,
            "timeline_name": timeline_name,
            "timeline_duration_frames": _tl_dur[0],
            "timeline_fps": _tl_dur[1],
            "clip_start": _clip_rng[0],
            "clip_end": _clip_rng[1],
            "clip_duration": _clip_rng[2],
            "clip_name": _clip_rng[3],
            "clip_head": _clip_rng[4],
            "clip_tail": _clip_rng[5],
            "tl_clip_total": _cmap[0],
            "tl_clip_rendered": _cmap[1],
            "tl_marker_count": _cmap[2],
            # "<playhead per-mille>;<row>|<row>" -- the strip is cached for a
            # few seconds but the playhead is not, so it rides in fresh on top
            # of the cached rows. See timeline_clip_map's encoding note.
            "tl_leds": _ph_pm + _cmap[3],
            "clip_id": drv.current_video_item_id(),
            "playhead_frame": drv.current_playhead_frame(),
            "edit_clip": drv.edit_clip_info(track=msg.get("track")),
        }
    if op == "refresh":
        return {"ok": drv.refresh()}
    if op == "set_node":
        # Acknowledge node change; node tracking lives in Go main.
        # Sidecar state is per-node (nodes dict), so this is a no-op-safe ack.
        return {"ok": True, "node": int(msg.get("node", 1))}
    if op == "bump_property":
        # Edit-page relative read-modify-write (Pan/Tilt/Zoom/Crop). No
        # "node" concept -- operates on the clip under the playhead,
        # re-resolved fresh every call (see ResolveDriver.bump_property).
        # "track" (D1 selectClip feature) is optional: when the Go engine
        # has an active-track selection it is passed straight through so
        # the bump targets THAT track's clip instead of
        # GetCurrentVideoItem(); absent/None preserves the pre-D1 behavior.
        #
        # _ensure_connected() FIRST (2026-07-08 self-heal fix): unlike
        # "status", this op previously had NO reconnect attempt of its own
        # -- it relied entirely on self.resolve already being set by a
        # PRIOR status tick. That was fine until current_video_item_id()
        # (part of the same status tick, called AFTER this op's own tick)
        # could poison self.resolve back to None on a cosmetic read
        # failure (now fixed separately, see ResolveDriver.
        # current_video_item_id()'s docstring) -- every knob turn after
        # that point silently hit `NoneType.GetProjectManager()` until the
        # next status tick happened to reconnect. Calling
        # _ensure_connected() here closes that gap structurally: even if
        # self.resolve is somehow None when this op runs (poisoned by a
        # bug elsewhere, or a genuinely dropped connection), the knob turn
        # itself now retries the connection instead of silently no-oping.
        # Cheap (single scriptapp() attempt, no-op once already connected)
        # -- same call the "status" op already makes every 250ms.
        drv._ensure_connected()
        ok, err, values = drv.bump_property(
            property_name=msg.get("property"),
            gang=msg.get("gang"),
            delta=float(msg.get("delta", 0.0)),
            min_val=msg.get("min"),
            max_val=msg.get("max"),
            track=msg.get("track"),
        )
        return {"ok": ok, "err": err, "values": values}
    if op == "reset_property":
        # Edit-page knob press: absolute reset of a property/gang to its
        # Resolve default (Crop*→0, Zoom*→1.0, transform→0, Opacity→100).
        # "track" -- see bump_property's comment above.
        #
        # _ensure_connected() FIRST -- same self-heal rationale as
        # bump_property's identical call above.
        drv._ensure_connected()
        ok, err, values = drv.reset_property(
            property_name=msg.get("property"),
            gang=msg.get("gang"),
            track=msg.get("track"),
        )
        return {"ok": ok, "err": err, "values": values}
    if op == "clips_at_playhead":
        # D1 selectClip feature: ordered list of every occupied video
        # track's clip under the playhead -- the rotation source list AND
        # the Status "active clip" display's clip-name source.
        ok, err, clips = drv.clips_at_playhead()
        return {"ok": ok, "err": err, "clips": clips}
    if op == "highlight_clip":
        # D1 active-clip highlight: tint the D1-selected clip's color in
        # Resolve so the user can SEE which stacked layer is active
        # (Resolve has no clip-selection-highlight API). Restores
        # whichever clip was PREVIOUSLY tinted first -- see
        # ResolveDriver.highlight_clip.
        ok, err = drv.highlight_clip(track=msg.get("track"), color=msg.get("color"))
        return {"ok": ok, "err": err}
    if op == "clear_highlight":
        # Engine Stop(): restore whatever clip is currently tinted. A
        # no-op if nothing is highlighted.
        ok, err = drv.clear_highlight()
        return {"ok": ok, "err": err}
    if op == "set_clips_color":
        # D1 knob-press "release/normalize" (Edit page): set EVERY
        # occupied video clip under the playhead to a uniform color
        # (default "Blue") and clear the D1 highlight-tracking state --
        # see engine.go's dispatchSelectClipRelease. Replaces the old
        # clear_highlight call there; clear_highlight itself is
        # unchanged and still used by engine Stop().
        ok, err, count = drv.set_clips_color(color=msg.get("color"))
        return {"ok": ok, "err": err, "count": count}
    if op == "color_by_render_state":
        # Deck-triggered (never polled): paint every video clip green when its
        # name contains "render", yellow otherwise, leaving offline clips
        # alone so Resolve's own missing-media flag survives. Mutates the
        # project; covered by Resolve's undo.
        ok, err, painted, skipped = drv.color_by_render_state(
            rendered=msg.get("rendered") or "Green",
            neutral=msg.get("neutral") or "Yellow",
        )
        return {"ok": ok, "err": err, "painted": painted, "skipped": skipped}
    if op == "select_clip_vertical":
        # Up/Down button (Edit page): step to the next occupied video
        # track in "direction" (+1 Up, -1 Down) relative to
        # "current_track" (0/absent = no current selection -- bootstraps
        # from the topmost occupied clip at the playhead). Replaces D1's
        # knob-turn rotation for the direction-button clip-selection
        # feature -- see ResolveDriver.select_clip_vertical's docstring.
        drv._ensure_connected()
        ok, err, track, unique_id, start, end, original_color = drv.select_clip_vertical(
            direction=int(msg.get("direction", 0)),
            current_track=msg.get("current_track"),
            color=msg.get("color", ""),
        )
        return {
            "ok": ok, "err": err, "track": track, "unique_id": unique_id,
            "start": start, "end": end, "original_color": original_color,
        }
    if op == "select_clip_horizontal":
        # Left/Right button (Edit page, page-aware -- see engine.go's
        # hard-wired Left/Right nav notes): select the previous/next
        # clip relative to the current selection's (track, start),
        # preferring the same track, falling back to the nearest clip on
        # any track, and moves the playhead onto it. See
        # ResolveDriver.select_clip_horizontal's docstring.
        drv._ensure_connected()
        ok, err, track, unique_id, start, end, original_color = drv.select_clip_horizontal(
            direction=int(msg.get("direction", 0)),
            current_track=msg.get("current_track"),
            current_start=msg.get("current_start"),
            color=msg.get("color", ""),
        )
        return {
            "ok": ok, "err": err, "track": track, "unique_id": unique_id,
            "start": start, "end": end, "original_color": original_color,
        }
    if op == "restore_highlight":
        # Crash-safety: called once on engine Start()/sidecar connect --
        # restores a stray tint left behind by a force-killed process.
        ok, err = drv.restore_highlight()
        return {"ok": ok, "err": err}
    if op == "jog":
        # Edit-page playhead scrub via SetCurrentTimecode. Two forms from
        # the Go engine's speed-tiered ControlDial jog (see
        # internal/engine's IsJog branch):
        #   {"frames": N}                                  -- fine tier.
        #   {"snap_seconds": 5|10|30, "dir": ±1, "frames": N} -- fast
        #     tiers: try jog_snap first; "frames" rides along as a
        #     fallback used ONLY if the live timeline is drop-frame or
        #     non-integer-fps (jog_snap refuses rather than mis-snap --
        #     see ResolveDriver.jog_snap). Any other jog_snap failure
        #     (e.g. no_timeline) is returned as-is, unchanged -- falling
        #     back to jog() would fail identically.
        snap_seconds = msg.get("snap_seconds")
        if snap_seconds:
            ok, err, new_tc = drv.jog_snap(int(snap_seconds), int(msg.get("dir", 1)))
            if not ok and err in ("drop_frame_unsupported", "non_integer_fps_unsupported"):
                ok, err, new_tc = drv.jog(int(msg.get("frames", 0)))
        else:
            ok, err, new_tc = drv.jog(int(msg.get("frames", 0)))
        return {"ok": ok, "err": err, "tc": new_tc}
    if op == "jog_seconds":
        # D1 button/knob (Edit page): COARSE playhead jump by ±seconds --
        # the coarse counterpart to the "jog" op above, via
        # ResolveDriver.jog_seconds. The Go engine's detent accumulator
        # (jogSecondsAccum) already collapses many raw dial ticks into ONE
        # call here with a fixed "seconds" value (default ±60) -- no
        # acceleration/tier logic on this side at all, unlike jog/jog_snap.
        #
        # _ensure_connected() FIRST -- same self-heal rationale as
        # bump_property/reset_property's identical call above: this op has
        # no per-tick status-poll self-heal of its own, so a poisoned/
        # disconnected self.resolve must reconnect within THIS call rather
        # than silently no-op until the next status tick.
        drv._ensure_connected()
        ok, err, new_tc = drv.jog_seconds(float(msg.get("seconds", 0.0)))
        return {"ok": ok, "err": err, "tc": new_tc}
    if op == "jog_minute_snap":
        # D1 knob (Edit page): the CURRENT D1 binding -- snap the
        # playhead to the next/previous whole-minute timecode boundary
        # via ResolveDriver.jog_minute_snap, superseding the "jog_seconds"
        # op above (kept, unreachable in production). The Go engine's
        # detent accumulator (jogMinuteSnapAccum) already collapses many
        # raw dial ticks into ONE call here with a fixed "direction" value
        # (±1) -- no acceleration/tier/offset logic on this side at all.
        #
        # _ensure_connected() FIRST -- same self-heal rationale as
        # jog_seconds/bump_property/reset_property's identical call
        # above: this op has no per-tick status-poll self-heal of its
        # own, so a poisoned/disconnected self.resolve must reconnect
        # within THIS call rather than silently no-op until the next
        # status tick.
        drv._ensure_connected()
        ok, err, new_tc = drv.jog_minute_snap(int(msg.get("direction", 0)))
        return {"ok": ok, "err": err, "tc": new_tc}
    if op == "render":
        # Quick Export: LoadRenderPreset("H.264 Master") + AddRenderJob + StartRendering.
        ok, err, info = drv.render(msg.get("target_dir", ""))
        return {"ok": ok, "err": err, "render": info}
    if op == "add_marker":
        # C1 button (Edit page): add a marker at the playhead via
        # Timeline.AddMarker -- see engine.go's dispatchAddMarker and
        # ResolveDriver.add_marker's docstring (frameId-relative-to-
        # timeline-start math flagged there for live verification).
        # "track" (Batch 1 "marker spans the D1-selected clip" feature):
        # optional, same pass-through convention as bump_property/
        # reset_property/highlight_clip above -- when present AND that
        # track has a clip under the playhead, add_marker spans the clip
        # instead of using duration_seconds; any other outcome (absent,
        # or a refusal from that track) is a graceful fallback to the
        # original playhead+duration_seconds behavior (see
        # ResolveDriver.add_marker's "track:" doc comment). "duration"
        # (frames) and "fps" ride along in the response so the Go engine
        # can display the marker's real duration in seconds, same shape
        # as resize_marker's response.
        ok, err, frame, duration, fps = drv.add_marker(
            duration_seconds=float(msg.get("duration_seconds", 1.0)),
            color=msg.get("color", "Blue"),
            name=msg.get("name"),
            note=msg.get("note"),
            track=msg.get("track"),
        )
        return {"ok": ok, "err": err, "frame": frame, "duration": duration, "fps": fps}
    if op == "marker_at_playhead":
        # C1 "adopt an existing marker": returns the marker whose
        # [start, start+duration) contains the playhead, so C1 can pick it up
        # for resize/recolour instead of creating an overlapping duplicate.
        # A miss is {"found": False}, not an error.
        return drv.marker_at_playhead()

    if op == "resize_marker":
        # C1 marker-mode ControlDial resize (see engine.go's
        # dispatchMarkerResize): extend/shrink the duration of the
        # marker at "frame" by "delta_frames" -- frame stays the key
        # (Resolve has no marker-update API, ResolveDriver.resize_marker
        # implements this as delete+re-add under the hood). "duration"
        # (new duration, frames) and "fps" ride along in the response so
        # the Go engine can refresh Status.MarkerMode's seconds display
        # without an extra round trip.
        ok, err, duration, fps = drv.resize_marker(
            frame=int(msg.get("frame", 0)),
            delta_frames=int(msg.get("delta_frames", 0)),
        )
        return {"ok": ok, "err": err, "frame": msg.get("frame"), "duration": duration, "fps": fps}
    if op == "set_marker_color":
        # P1-8btn marker-mode recolor (see engine.go's
        # dispatchSetMarkerColor): set the color of the marker at
        # "frame", preserving duration/name/note -- same delete+re-add
        # shape as resize_marker, see ResolveDriver.set_marker_color.
        ok, err = drv.set_marker_color(frame=int(msg.get("frame", 0)), color=msg.get("color"))
        return {"ok": ok, "err": err, "frame": msg.get("frame")}
    if op == "delete_marker":
        # P-button same-colour-press toggle (2026-08-12, see engine.go's
        # dispatchMarkerAtPlayhead): delete the marker at "frame"
        # entirely -- no re-add, unlike resize_marker/set_marker_color's
        # delete+re-add "fake update" shape. See
        # ResolveDriver.delete_marker.
        ok, err = drv.delete_marker(frame=int(msg.get("frame", 0)))
        return {"ok": ok, "err": err, "frame": msg.get("frame")}
    node = int(msg.get("node", 1))
    n = nodes.setdefault(node, NodeCDL())
    if op == "delta":
        n.apply(msg["param"], msg["ch"], float(msg["amt"]))
    elif op == "sat":
        n.apply_sat(float(msg["amt"]))
    elif op == "reset":
        n.reset()
    elif op == "reset_param":
        # Reset a single CDL parameter back to neutral.
        # param: offset|slope|power|sat; ch: r|g|b|master (ignored for sat)
        n.reset_param(msg["param"], msg.get("ch", "master"))
    else:
        return {"ok": False, "err": "unknown op"}
    ok, err = drv.write(n.to_setcdl(node))
    return {"ok": ok, "node": node, "err": err}

if __name__ == "__main__":
    main()
