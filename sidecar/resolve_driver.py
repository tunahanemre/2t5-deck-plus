import json, os, re, sys, time

API = "/Library/Application Support/Blackmagic Design/DaVinci Resolve/Developer/Scripting"
LIB = "/Applications/DaVinci Resolve/DaVinci Resolve.app/Contents/Libraries/Fusion/fusionscript.so"

# The 2T5 DECK+ data root, kept byte-identical to Go's internal/datadir:
# same env vars, same defaults, same subdirectory names. sidecar.Launch also
# passes DECKPLUS_DATA_DIR down explicitly, so parent and child agree even
# under a test override.
DECKPLUS_DATA_ENV = "DECKPLUS_DATA_DIR"
DECKPLUS_LEGACY_DATA_ENV = "LD_DECK_DATA_DIR"  # the name before the rename; read when the new one is unset
DECKPLUS_DEFAULT_DATA_ROOT = "/Volumes/2T5-WS-01/01-APPS/2t5-deck-plus/data"
DECKPLUS_LEGACY_DEFAULT_DATA_ROOT = "/Volumes/2T5-WS-01/01-APPS/ld-deck/data"  # before the folder is renamed


DECKPLUS_MARKER_NAME = "data-root"  # per-user marker, written by Go's internal/datadir
DECKPLUS_APP_SUPPORT_NAME = "2t5-deck-plus"
DECKPLUS_LEGACY_APP_SUPPORT_NAME = "ld-deck"


def _per_user_dir():
    return os.path.join(os.path.expanduser("~"), "Library", "Application Support",
                        DECKPLUS_APP_SUPPORT_NAME)


def home_data_root():
    """The data root of a Mac with no WS-01 volume (same as Go's datadir.HomeRoot)."""
    return os.path.join(_per_user_dir(), "data")


def _read_marker():
    """('external'|'home'|'', recorded root). Read-only."""
    try:
        with open(os.path.join(_per_user_dir(), DECKPLUS_MARKER_NAME)) as f:
            lines = f.read().strip().split("\n")
    except Exception:
        return "", ""
    kind = lines[0].strip()
    if kind not in ("external", "home"):
        return "", ""
    return kind, (lines[1].strip() if len(lines) > 1 else "")


def data_root():
    """Resolve the data root, mirroring Go's internal/datadir.Root. Never
    creates or writes anything (it only stats and reads the marker):
      1. env, else the old env name;
      2. the WS-01 root: the new default folder, else the old one when only
         that exists, else the new default when its parent folder exists;
      3. else, if this Mac has recorded that it uses the external root (marker
         says external, or an older build's per-user folder exists without a
         marker), the external path, which ensure_data_dir() then refuses;
      4. else the per-user home root."""
    for name in (DECKPLUS_DATA_ENV, DECKPLUS_LEGACY_DATA_ENV):
        v = (os.environ.get(name) or "").strip()
        if v:
            return v
    if os.path.isdir(DECKPLUS_DEFAULT_DATA_ROOT):
        return DECKPLUS_DEFAULT_DATA_ROOT
    if os.path.isdir(DECKPLUS_LEGACY_DEFAULT_DATA_ROOT):
        return DECKPLUS_LEGACY_DEFAULT_DATA_ROOT
    if os.path.isdir(os.path.dirname(DECKPLUS_DEFAULT_DATA_ROOT)):
        return DECKPLUS_DEFAULT_DATA_ROOT
    kind, recorded = _read_marker()
    if not kind:
        support = _per_user_dir()
        prior = os.path.isdir(support) or os.path.isdir(
            os.path.join(os.path.dirname(support), DECKPLUS_LEGACY_APP_SUPPORT_NAME))
        if prior:
            kind = "external"
    if kind == "external":
        return recorded or DECKPLUS_DEFAULT_DATA_ROOT
    return home_data_root()


def ensure_data_dir(sub):
    """Create <data root>/<sub> and return it, or return None when the root
    is unreachable.

    The guard mirrors Go's datadir.checkReachable and exists for the same
    reason: the default root is on an external volume, and an unguarded
    makedirs() would CREATE /Volumes/2T5-WS-01/... on the boot disk when
    WS-01 is not mounted (that is how 162 GB landed on this Mac's internal
    SSD on 2026-08-27). The root's parent can only exist on the real volume,
    so it serves as the mount sentinel."""
    root = data_root()
    if root != home_data_root() and not os.path.isdir(os.path.dirname(root)):
        return None
    d = os.path.join(root, sub)
    try:
        os.makedirs(d, exist_ok=True)
    except Exception:
        return None
    return d


# D1 active-clip highlight crash-safety state file (see
# ResolveDriver.highlight_clip/clear_highlight/restore_highlight below).
# Module-level default; tests override the per-INSTANCE
# `highlight_state_path` attribute instead of this constant, so the real
# production path is never touched during `pytest`.
HIGHLIGHT_STATE_PATH = os.path.join(data_root(), "state", "2t5-deck-plus-highlight.json")

# *** CONFIRMED LIVE, Resolve 21.0.0.47 (2026-07-02) *** --
# Timeline.AddMarker(frameId, color, name, note, duration, customData)
# returns False when `name` is the empty string "" -- empirically proven
# on a live timeline (AddMarker(..., "", ...) -> False; the identical call
# with a non-empty name -> True). This is NOT documented anywhere and is
# NOT the same failure mode as "a marker already exists at this frame" --
# add_marker/resize_marker/set_marker_color must never pass "" as the
# AddMarker name argument.
#
# resize_marker/set_marker_color use this as their ONLY fallback (they
# re-add an EXISTING marker whose stored name may itself be empty).
# add_marker (2026-07-03) prefers an AUTO-NUMBERED "M01"/"M02"/... name
# (see _next_auto_marker_name below) when the caller omits a name --
# DEFAULT_MARKER_NAME is add_marker's fallback of LAST resort only, used
# when the live timeline's marker state can't be safely scanned (e.g.
# GetMarkers() returns None, or the scan itself raises).
DEFAULT_MARKER_NAME = "Marker"

# Resolve renders a marker of duration N one frame SHORT of an N-frame clip:
# a marker written with duration == clip.GetEnd() - clip.GetStart() stops one
# frame before the clip's last frame instead of landing flush on the cut.
# Established empirically on the user's own timeline (2026-08-30) after the
# arithmetic was cleared: GetEnd() is genuinely exclusive (verified live --
# GetEnd() - GetStart() == GetDuration() on every sampled clip), and every
# range test in this codebase is consistently half-open, so the discrepancy
# is in Resolve's own interpretation of AddMarker's duration argument, not
# in our math. Applied ONLY where a marker is written to span a clip exactly
# (add_marker's clip branch) -- never to the duration_seconds default, which
# is not measured against a cut. resize_marker is unaffected: it operates on
# a delta against whatever duration Resolve already holds. Added INSIDE
# add_marker's max(1, ...) clamp, so a pathological zero-length clip still
# yields 1 frame rather than 2.
MARKER_CLIP_SPAN_TAIL_COMPENSATION = 1

class ResolveDriver:
    # Self-healing connection tuning (see connect()/_ensure_connected()
    # below). scriptapp("Resolve") transiently returns None while many
    # app/sidecar processes are being spawned/killed in quick succession
    # (Resolve script-connection-slot contention) -- these bound how long
    # connect()'s startup retry loop waits, and how the "am I still
    # connected" cheap per-tick check behaves. Tests override both to tiny
    # values so the retry loop never sleeps for real wall-clock seconds.
    CONNECT_RETRY_TIMEOUT = 15.0  # seconds, bounded -- connect() always returns
    CONNECT_RETRY_INTERVAL = 0.5  # seconds between scriptapp() attempts

    def __init__(self):
        os.environ.setdefault("RESOLVE_SCRIPT_API", API)
        os.environ.setdefault("RESOLVE_SCRIPT_LIB", LIB)
        sys.path.insert(0, os.path.join(API, "Modules"))
        self.resolve = None
        self.item = None
        # D1 active-clip highlight feature: the currently-tinted clip's
        # restore state ({"unique_id","original_color","track"}), or None
        # when nothing is highlighted. Mirrored to highlight_state_path on
        # disk the instant a clip is tinted, for crash-safety -- see
        # restore_highlight's docstring.
        self._highlight = None
        self.highlight_state_path = HIGHLIGHT_STATE_PATH

    def connect(self):
        """Connect to a running Resolve instance via
        DaVinciResolveScript.scriptapp("Resolve"), retrying with a short
        bounded backoff instead of giving up after a single None.

        scriptapp("Resolve") transiently returns None while Resolve's
        script-connection slots are contended -- many app/sidecar
        processes being spawned/killed at once (e.g. during a rebuild+
        relaunch cycle). A sidecar launched during that churn window that
        only tries ONCE gets stuck with self.resolve=None FOREVER, even
        though Resolve becomes reachable a couple seconds later -- every
        Resolve-dependent op then no-ops for the rest of the sidecar's
        life. Retries every CONNECT_RETRY_INTERVAL seconds until either
        scriptapp() returns non-None or CONNECT_RETRY_TIMEOUT elapses --
        still bounded, so this never hangs the sidecar's startup
        indefinitely if Resolve genuinely isn't running.

        If the timeout elapses without connecting, self.resolve stays
        None and this returns False -- _ensure_connected() (below), run
        every status-poll tick by the sidecar's "status" op handler,
        keeps retrying (a single cheap attempt per tick) after that, so a
        sidecar that started during a longer churn window than
        CONNECT_RETRY_TIMEOUT still self-heals once Resolve becomes
        reachable, with no app restart required.
        """
        import DaVinciResolveScript as dvr
        deadline = time.monotonic() + self.CONNECT_RETRY_TIMEOUT
        attempt = 0
        while True:
            attempt += 1
            try:
                self.resolve = dvr.scriptapp("Resolve")
            except Exception:
                self.resolve = None
            if self.resolve is not None:
                print("sidecar: connected to Resolve (attempt %d)" % attempt, flush=True)
                return True
            if time.monotonic() >= deadline:
                print(
                    "sidecar: scriptapp('Resolve') still None after %d attempts "
                    "(%.1fs) -- giving up for now; status polls will keep retrying"
                    % (attempt, self.CONNECT_RETRY_TIMEOUT),
                    flush=True,
                )
                return False
            time.sleep(self.CONNECT_RETRY_INTERVAL)

    def _is_alive(self):
        """Cheap liveness probe for the shared self.resolve connection --
        ONE lightweight Resolve call (GetCurrentPage()), wrapped so ANY
        failure (a dead handle after Resolve quit/restarted, or self.resolve
        itself being unusable for any other reason) reads as "not alive"
        rather than raising. Never touches self.resolve itself -- purely a
        read-only check; the caller (_ensure_connected(), below) decides
        what to do with the answer.

        *** ADDED 2026-07-08, Part A hardening ***: this is now the SOLE
        place genuine dead-handle detection happens in this file. Before
        this change, each individual status-tick read method
        (current_page/current_node/current_timecode/current_video_item_id)
        decided for itself whether ITS OWN exception meant "the connection
        died" -- which is exactly how a cosmetic/data-shaped read failure
        in current_video_item_id() ended up poisoning self.resolve for
        every other op (see that method's "FIXED 2026-07-08" docstring).
        Decoupling "did THIS read fail" from "is the connection dead" onto
        a single, deliberately trivial probe removes that whole class of
        bug: a read failing is now always just "couldn't get this value
        this tick," and "is the connection actually dead" is answered
        exactly once, cheaply, in one place.

        The return value from GetCurrentPage() itself is never inspected
        (Resolve can legitimately return None/"" for it in some transient
        states without the connection being dead) -- only whether the call
        raised.
        """
        try:
            self.resolve.GetCurrentPage()
            return True
        except Exception:
            return False

    def _ensure_connected(self):
        """Cheap per-tick reconnect attempt for the self-healing sidecar
        connection. Called at the TOP of sidecar.py's "status" op handler
        (the Go engine polls status every 250ms), and also at the top of
        every other op handler that depends on self.resolve
        (bump_property/reset_property/jog_seconds/select_clip_vertical/
        select_clip_horizontal -- see sidecar.py) so a sidecar that's
        currently disconnected -- either connect() gave up at startup, or
        a previously-good handle went dead later (e.g. Resolve restarted)
        -- reconnects on its own, with no app restart needed.

        *** CHANGED 2026-07-08, Part A hardening ***: reconnects when
        self.resolve is None **OR** the cheap _is_alive() liveness probe
        fails -- previously this only checked "is self.resolve None",
        which relied entirely on some OTHER method having already noticed
        a dead handle and dropped it to None first. Now that the
        individual read methods no longer null self.resolve on a read
        failure (see current_page()/current_node()/current_timecode()/
        current_video_item_id()'s docstrings), THIS is the only place that
        decides the connection is dead and needs replacing -- so it must
        be able to detect that itself, not just observe a None someone
        else already set.

        Still deliberately a SINGLE scriptapp() attempt, NOT connect()'s
        bounded-retry loop -- this runs every ~250ms while disconnected,
        so a multi-second retry loop here would stall the sidecar's
        response to every OTHER queued op in the meantime. Genuinely
        cheap: _is_alive() is only called at all when self.resolve is
        already set (short-circuited via `and`), so the common "already
        connected and healthy" case costs exactly one extra
        GetCurrentPage() call, no scriptapp() call.
        """
        if self.resolve is not None and self._is_alive():
            return
        try:
            import DaVinciResolveScript as dvr
            self.resolve = dvr.scriptapp("Resolve")
        except Exception:
            self.resolve = None

    def _item(self):
        if self.item:
            return self.item
        proj = self.resolve.GetProjectManager().GetCurrentProject()
        tl = proj.GetCurrentTimeline() if proj else None
        self.item = tl.GetCurrentVideoItem() if tl else None
        return self.item

    def refresh(self):
        self.item = None
        return self._item() is not None

    def current_page(self):
        """Return the current Resolve page name (e.g. 'color', 'edit', 'cut').
        Returns None when Resolve is not connected or GetCurrentPage is unavailable.

        *** CHANGED 2026-07-08 (connection-poisoning hardening, Part A) ***
        No longer drops self.resolve to None on a read failure -- a
        cosmetic/transient read failure here is never grounds to poison
        the shared connection every OTHER op (bump_property/reset_property
        /jog/...) depends on. This generalizes current_video_item_id()'s
        2026-07-08 fix (see that method's docstring for the original
        live-verified incident) to every status-tick read in this file:
        current_page/current_node/current_timecode/current_video_item_id/
        current_playhead_frame all now share the SAME "a failed read means
        'couldn't determine this tick's value', never 'the connection is
        dead'" contract. Genuine dead-handle recovery (Resolve quit/
        restarted underneath the sidecar) is _ensure_connected()'s job
        alone now, via its _is_alive() liveness probe -- not each
        individual read method's.
        """
        try:
            if self.resolve is None:
                return None
            page = self.resolve.GetCurrentPage()
            return page if page else None
        except Exception:
            return None

    def current_node(self):
        """Return info about the currently selected color node, or None.

        Returns a dict {"name": str, "index": int} when a color node is
        selected on the Color page.  Returns None when Resolve is unavailable,
        the page is not Color, or node info cannot be retrieved.

        This is used by the Go gate to detect CST / Fusion nodes and refuse
        balance drag events on them.

        *** CHANGED 2026-07-08 -- see current_page()'s docstring above ***:
        no longer drops self.resolve to None on a read failure. This method
        does a fair amount of chained navigation (project -> timeline ->
        item -> node graph -> node) -- exactly the kind of "more
        non-connection work per call" surface that current_video_item_id()'s
        original incident showed can raise for reasons that have nothing to
        do with connection health.
        """
        try:
            if self.resolve is None:
                return None
            pm = self.resolve.GetProjectManager()
            if pm is None:
                return None
            proj = pm.GetCurrentProject()
            if proj is None:
                return None
            cm = proj.GetColorPageGallery() if hasattr(proj, "GetColorPageGallery") else None
            # GetCurrentTimeline + GetCurrentVideoItem is the reliable path.
            tl = proj.GetCurrentTimeline()
            if tl is None:
                return None
            item = tl.GetCurrentVideoItem()
            if item is None:
                return None
            # GetCurrentNodeGraph returns a NodeGraph object.
            ng = item.GetCurrentNodeGraph() if hasattr(item, "GetCurrentNodeGraph") else None
            if ng is None:
                return None
            node = ng.GetCurrentNode() if hasattr(ng, "GetCurrentNode") else None
            if node is None:
                return None
            name = node.GetName() if hasattr(node, "GetName") else ""
            idx = node.GetIndex() if hasattr(node, "GetIndex") else 0
            return {"name": name if name else "", "index": idx if idx else 0}
        except Exception:
            return None

    def current_timecode(self):
        """Return (timecode, timeline_name) for the HUD's timecode-hero
        display -- HUD v2 (docs/specs/2026-07-06-ld-resolve-hud-rich.md).

        Deliberately independent of _read_timeline_tc_frames (jog's shared
        preamble): the HUD only needs the raw display string + timeline
        name, never a parsed frame count, integer-fps check, or drop-frame
        refusal -- those guards exist purely because jog/jog_snap need to
        do FRAME MATH on the timecode, which the HUD never does. Returns
        (None, None) gracefully whenever Resolve/project/timeline is
        unavailable (no timeline open, no project, sidecar not connected) --
        never raises, mirroring current_page/current_node's try/except
        shape above.

        *** CHANGED 2026-07-08 -- see current_page()'s docstring above ***:
        no longer drops self.resolve to None on a read failure.
        """
        try:
            if self.resolve is None:
                return None, None
            proj = self.resolve.GetProjectManager().GetCurrentProject()
            tl = proj.GetCurrentTimeline() if proj else None
            if tl is None:
                return None, None
            tc = tl.GetCurrentTimecode()
            name = tl.GetName()
            return (tc if tc else None), (name if name else None)
        except Exception:
            return None, None

    def current_video_item_id(self):
        """Return the topmost-occupied video clip's GetUniqueId() -- the
        SAME clip _target_item(track=None) resolves as the default (no
        active D1 override) Edit-page property/reset target -- or None
        when no video track has a clip at the playhead (a genuine gap),
        the timeline/project/Resolve connection is unavailable, or an
        error occurs.

        Read alongside timecode/timeline_name by the "status" op (see
        sidecar.py) every ~250ms poll -- the Go engine (engine.go's drain
        loop) compares this against the PREVIOUS tick's value to detect
        "the top-occupied clip changed" and auto-clears the D1 selectClip
        override (activeTrack) when it does, so the Edit-page property/
        reset dials fall back to this exact same top-occupied target
        (see _target_item) instead of staying pinned to a track that may
        no longer even be relevant.

        *** FIXED 2026-07-08, root-caused live against Resolve 21 ***
        Previously called GetCurrentVideoItem().GetUniqueId() directly.
        GetCurrentVideoItem() is UNRELIABLE as a clip-identity source for
        exactly the same reason it was unreliable as _target_item's old
        default (see that method's doc comment): it is scoped to
        Resolve's own internal "current target track" ONLY, and returns
        None whenever THAT track is empty at the playhead -- even while
        other video tracks plainly have clips there. That meant this
        clip-id could silently go None (looking like "no clip / playhead
        on a gap") while the knobs were actually still parked on a real,
        visible clip, breaking the auto-reset's own "did the target
        actually change" comparison. GetCurrentVideoItem() is no longer
        used anywhere in the edit-target or clip-identity paths of this
        class -- only current_node() (Color-page node lookup, a
        different concept) and the CDL _item() path (Color-page CDL
        writes, also a different concept) still call it, unaffected by
        this fix.

        Reuses _occupied_video_items() -- the SAME top-down scan behind
        clips_at_playhead()/set_clips_color()/_target_item(track=None) --
        so "topmost occupied clip" means identically the same thing
        everywhere in this file. Frame/fps parsing is deliberately
        hand-rolled here rather than delegated to
        _read_timeline_tc_frames(), which swallows its own exceptions
        into an error-code return -- correct for jog()/clips_at_playhead
        ()/etc, which must NOT drop a live connection just because a
        timeline happens to be drop-frame or has no timecode right now,
        but wrong here: delegating would silently break this method's
        self-healing contract, since a genuinely dead self.resolve
        handle and a benign "no timeline open" refusal are
        indistinguishable from _read_timeline_tc_frames()'s return alone.
        Keeping the exception path local lets any real failure --
        including a dead handle -- propagate to THIS method's own
        try/except rather than being swallowed by
        _read_timeline_tc_frames()'s error-code return.

        Deliberately independent of _item() (never touches the self.item
        cache that manages) -- this is a pure read for CHANGE DETECTION,
        not a target resolution. Never raises.

        *** FIXED 2026-07-08, root-caused live against Resolve: cosmetic
        read failures were POISONING the shared connection ***
        This method used to mirror current_page()/current_node()/
        current_timecode()'s dead-handle guard exactly -- ANY exception
        in the try block dropped self.resolve to None. That contract is
        correct for a genuinely DEAD handle (Resolve restarted
        underneath the sidecar), but current_video_item_id() does
        meaningfully MORE work per call than those three methods (its
        own inline timecode/fps parse, plus the occupied-track scan) --
        more surface area for an exception that has NOTHING to do with
        connection health. Live-verified incident: on the user's real
        timeline this method's try block raised (most likely
        `float(fps_raw)` on a None/empty "timelineFrameRate" setting, or
        `int(GetTrackCount("video"))` on a None track count -- see
        _occupied_video_items()'s companion guard above), the broad
        except caught it and set self.resolve = None -- and because this
        method runs INSIDE the "status" op (sidecar.py), AFTER that
        op's own _ensure_connected() call already ran for this tick,
        self.resolve stayed None for every op dispatched afterward until
        the NEXT status tick's _ensure_connected() happened to run.
        bump_property/reset_property have no _ensure_connected() call of
        their own (fixed alongside this, see sidecar.py), so they hit
        `NoneType.GetProjectManager()` and silently failed -- "the knobs
        do nothing" even though the engine's MIDI dispatch was working
        correctly the whole time. Confirmed live: a "status" call
        returned a valid page + timecode but clip_id=None, and the very
        next bump_property call on the SAME poisoned handle errored
        `AttributeError('NoneType' object has no attribute
        'GetProjectManager')`.

        Fix, two parts:
        (1) The broad except below no longer touches self.resolve at
        all -- a clip-id read failing (for ANY reason) means "couldn't
        determine the clip this tick," never "the connection is dead."
        *** UPDATE 2026-07-08, Part A hardening ***: this WAS a
        deliberate, permanent divergence from current_page()/
        current_node()/current_timecode()'s dead-handle-drops-to-None
        contract when first written -- those three were left on the old
        contract at the time because they're simple direct getters with
        little surface for a benign/data-shaped exception to hide behind.
        That reasoning turned out not to matter: a read failure is NEVER
        grounds to poison the shared connection, regardless of how much
        work sits inside the try block. current_page()/current_node()/
        current_timecode() were subsequently updated to the SAME
        never-null contract (see their docstrings) -- this is no longer a
        divergence, it is the general rule for every status-tick read in
        this file. Genuine dead-handle detection now lives solely in
        _ensure_connected()'s _is_alive() liveness probe.
        (2) The likely actual throw points are ALSO now guarded
        explicitly (None/empty -> return None) rather than relying
        solely on the broad except to paper over them -- fps_raw
        None/unparseable, and a malformed timecode -- so a normal
        "this timeline doesn't have this field set" case degrades
        cleanly and readably instead of via a caught TypeError.
        _occupied_video_items() carries the matching guard for a None/0
        GetTrackCount().
        """
        try:
            if self.resolve is None:
                return None
            proj = self.resolve.GetProjectManager().GetCurrentProject()
            tl = proj.GetCurrentTimeline() if proj else None
            if tl is None:
                return None
            cur_tc = tl.GetCurrentTimecode()
            if not cur_tc or ";" in cur_tc:
                return None
            fps_raw = tl.GetSetting("timelineFrameRate")
            if fps_raw is None or fps_raw == "":
                return None
            try:
                fps_f = float(fps_raw)
            except (TypeError, ValueError):
                return None
            fps = int(round(fps_f))
            if fps <= 0 or abs(fps_f - fps) > 1e-6:
                return None
            parts = cur_tc.split(":")
            if len(parts) != 4:
                return None
            try:
                h, m, s, f = (int(p) for p in parts)
            except (TypeError, ValueError):
                return None
            cur_frames = ((h * 60 + m) * 60 + s) * fps + f
            occupied = self._occupied_video_items(tl, cur_frames)
            if not occupied:
                return None
            _top_track, item = occupied[-1]
            uid = item.GetUniqueId()
            return uid if uid else None
        except Exception:
            # Deliberately NOT self.resolve = None -- see the "FIXED
            # 2026-07-08" section above. A cosmetic/data-shaped read
            # failure here must never poison the shared connection that
            # every other op (bump_property/reset_property/jog/...)
            # depends on.
            return None

    def current_playhead_frame(self):
        """Absolute playhead frame number, in the SAME frame-counting
        convention as _read_timeline_tc_frames()/item.GetStart()/
        item.GetEnd() (no timeline-start-frame offset -- unlike
        add_marker's frameId, which IS relative to Timeline.
        GetStartFrame(); see add_marker's doc comment for that distinct
        convention).

        Read alongside page/clip_id/timecode by the "status" op (see
        sidecar.py) every ~250ms poll -- the Go engine (engine.go's
        drain loop) compares this against a direction-button-selected
        clip's stored [start, end) range to detect "the playhead moved
        OUTSIDE the selected clip" and auto-clears the selection (see
        select_clip_vertical/select_clip_horizontal below).

        None on any refusal (no_timeline, no_timecode,
        drop_frame_unsupported, non_integer_fps_unsupported,
        bad_timecode_format) or exception -- delegates entirely to the
        side-effect-free _read_timeline_tc_frames() helper, which never
        touches self.resolve on failure. UNLIKE current_video_item_id(),
        this method has no self-healing contract of its own to protect
        (it does no extra non-connection work beyond what
        _read_timeline_tc_frames() already does), so there is no reason
        to hand-roll its own try/except the way current_video_item_id()
        deliberately does -- see that method's docstring for why THAT
        one can't simply delegate.
        """
        _tl, cur_frames, _fps, err = self._read_timeline_tc_frames()
        if err is not None:
            return None
        return cur_frames

    def edit_clip_info(self, track=None):
        """HUD v2 Edit-page readout: {"name": str, "pan": float,
        "tilt": float, "zoom": float} for the SAME clip bump_property/
        reset_property target -- resolved via _target_item(track), so this
        is always exactly the clip the property/reset knobs currently
        affect (the direction-button-selected clip when a selection is
        active i.e. track is a real activeTrack override, else the
        top-occupied clip at the playhead). None when there is no target
        clip (no timeline, playhead on a gap, a track override with no
        clip there, etc) -- see _target_item's docstring for the full
        resolution contract.

        Read alongside page/current_node/timecode/clip_id/playhead_frame
        by the "status" op (see sidecar.py) -- "track" is the Go engine's
        activeTrack/selection override, the SAME value already threaded
        into bump_property/reset_property's "track" field (0/absent means
        no override, forwarded here as None exactly like those two ops),
        so this always reads the identical target the knobs affect this
        tick, never a second, independently-resolved concept of "current
        clip."

        Graceful, matching this file's status-tick contract (see
        current_page()'s 2026-07-08 docstring): never raises, and never
        touches self.resolve on failure -- a failed HUD-display read (the
        target clip vanishing mid-read, a stale MagicMock in tests, a
        GetProperty() hiccup) is exactly the kind of cosmetic read failure
        that must never poison the shared connection. _target_item() itself
        already can't raise (both its track=None and track=<n> branches
        catch their own exceptions internally) -- the try/except below
        exists for the GetName()/GetProperty() calls on the resolved item,
        which is a live Resolve object that could still fail on access.
        """
        try:
            item, _err = self._target_item(track)
            if item is None:
                return None
            name = item.GetName() if hasattr(item, "GetName") else ""
            pan = item.GetProperty("Pan")
            tilt = item.GetProperty("Tilt")
            zoom = item.GetProperty("ZoomX")
            return {
                "name": name or "",
                "pan": float(pan) if pan is not None else 0.0,
                "tilt": float(tilt) if tilt is not None else 0.0,
                "zoom": float(zoom) if zoom is not None else 0.0,
            }
        except Exception:
            return None

    def write(self, setcdl_dict):
        try:
            item = self._item()
            if not item:
                return False, "no_clip"
            ok = item.SetCDL(setcdl_dict)
            return bool(ok), None
        except Exception as e:
            self.item = None  # force re-navigate next call
            return False, repr(e)

    # PROPERTY_DEFAULTS is the "no correction" resting value for each
    # TimelineItem transform/crop property, used by reset_property (a knob
    # press). Resolve's neutral state: translation/rotation/crop = 0,
    # scale (Zoom) = 1.0 (1x, not 0), opacity = 100 (%). Anything not
    # listed falls back to 0.0.
    PROPERTY_DEFAULTS = {
        "Pan": 0.0,
        "Tilt": 0.0,
        "ZoomX": 1.0,
        "ZoomY": 1.0,
        "RotationAngle": 0.0,
        "AnchorPointX": 0.0,
        "AnchorPointY": 0.0,
        "Pitch": 0.0,
        "Yaw": 0.0,
        "CropLeft": 0.0,
        "CropRight": 0.0,
        "CropTop": 0.0,
        "CropBottom": 0.0,
        "CropSoftness": 0.0,
        "Opacity": 100.0,
    }

    def _video_item_on_track(self, track):
        """Resolve the TimelineItem on video track `track` (1-based)
        whose frame range contains the current playhead position -- the
        D1 selectClip feature's "target THIS specific track's clip"
        resolution, used by bump_property/reset_property when a track is
        supplied instead of the default top-occupied-clip target (see
        _target_item) -- NOT Resolve's own GetCurrentVideoItem() concept
        of "current," which is scoped to whatever track Resolve itself
        considers current and not necessarily the one the user pinned
        via D1 (or the topmost clip actually visible at the playhead).

        Shares timeline/playhead-frame resolution with jog()/jog_snap()/
        clips_at_playhead() via _read_timeline_tc_frames() -- refuses
        identically (no_timeline, drop_frame_unsupported, etc) rather
        than risk resolving against a stale/wrong frame count.

        Returns (item, err). err is one of the _read_timeline_tc_frames()
        refusal codes, or "no_clip_on_track" when the named track has no
        clip at the current playhead frame. item is None on any failure.
        """
        tl, cur_frames, _fps, err = self._read_timeline_tc_frames()
        if err is not None:
            return None, err
        try:
            items = tl.GetItemListInTrack("video", int(track)) or []
            for item in items:
                start = item.GetStart()
                end = item.GetEnd()
                if start <= cur_frames < end:
                    return item, None
            return None, "no_clip_on_track"
        except Exception as e:
            return None, repr(e)

    def _occupied_video_items(self, tl, cur_frames):
        """Ordered list of (track: int, item) for every OCCUPIED video
        track (1..GetTrackCount("video")) whose clip's frame range
        contains cur_frames. Empty video tracks are skipped entirely.

        Shared track-walk enumeration behind clips_at_playhead() (reads
        item.GetName() for display), set_clips_color() (calls
        item.SetClipColor() to normalize every occupied clip), and
        current_video_item_id() (change-detection read, every ~250ms
        status tick) -- all three need the exact same "which clips are
        under the playhead right now" answer, so the walk itself lives
        in one place.

        *** GUARD added 2026-07-08 (connection-poisoning bug fix) ***:
        GetTrackCount("video") returning None/0 (rather than raising) is
        treated as "no tracks" -- returns [] instead of letting
        `int(None) + 1` raise a bare TypeError. This matters most for
        current_video_item_id(), the status-tick caller: that method
        previously let ANY exception here propagate up into its own
        try/except, which (before this same fix) dropped self.resolve to
        None -- turning a merely-empty/quirky timeline read into a
        poisoned shared connection for every OTHER op. See
        current_video_item_id()'s docstring for the full incident.
        """
        count = tl.GetTrackCount("video")
        if not count:
            return []
        result = []
        for track in range(1, int(count) + 1):
            items = tl.GetItemListInTrack("video", track) or []
            for item in items:
                start = item.GetStart()
                end = item.GetEnd()
                if start <= cur_frames < end:
                    result.append((track, item))
                    break
        return result

    def clips_at_playhead(self):
        """Return an ordered list of {"track": int, "name": str} for
        every OCCUPIED video track (1..GetTrackCount("video")) whose
        clip's frame range contains the current playhead position. Empty
        video tracks (no clip at the playhead) are skipped entirely --
        this is the D1 knob's selectClip rotation source list, and also
        the source for the Status "active clip" display's clip name.

        Shares timeline/playhead-frame resolution with jog()/jog_snap()/
        _video_item_on_track() via _read_timeline_tc_frames(), and the
        track-walk itself with set_clips_color() via
        _occupied_video_items().

        Returns (ok: bool, err: str|None, clips: list[dict]|None).
        """
        tl, cur_frames, _fps, err = self._read_timeline_tc_frames()
        if err is not None:
            return False, err, None
        try:
            clips = []
            for track, item in self._occupied_video_items(tl, cur_frames):
                name = item.GetName() if hasattr(item, "GetName") else ""
                clips.append({"track": track, "name": name or ""})
            return True, None, clips
        except Exception as e:
            return False, repr(e), None

    def set_clips_color(self, color):
        """D1 knob-PRESS "release/normalize" action: set the clip color
        of EVERY occupied video track under the playhead to `color`
        (e.g. "Blue") -- a clean "everything back to a uniform default"
        gesture, unlike highlight_clip (which tints only the ONE
        D1-selected clip while turning). Shares the exact same
        track-walk enumeration as clips_at_playhead() via
        _occupied_video_items(), so "occupied" means identically the
        same thing for both features.

        Also clears the D1 active-clip highlight tracking state
        (in-memory self._highlight + the on-disk crash-safety file) --
        once every clip is the uniform release color there is nothing
        left to "restore", so any dangling highlighted-clip reference
        must not survive this call. This is DELIBERATELY not a call to
        clear_highlight(): clear_highlight restores the highlighted
        clip's ORIGINAL pre-tint color, which would fight this method's
        own SetClipColor(color) if that clip is also among the ones
        being normalized here. This is a pure forward set -- it never
        reads GetClipColor().

        Returns (ok: bool, err: str|None, count: int|None). count is the
        number of clips actually colored (0 when no occupied tracks --
        not an error). err carries the timeline-resolution refusal code
        (no_timeline/drop_frame_unsupported/etc, from
        _read_timeline_tc_frames) or an exception repr from a
        SetClipColor call; ok=False and count=None on either. Highlight
        state is only cleared on the success path -- a failed/refused
        call leaves existing highlight state untouched (nothing was
        actually normalized, so there's nothing to invalidate).
        """
        tl, cur_frames, _fps, err = self._read_timeline_tc_frames()
        if err is not None:
            return False, err, None
        try:
            items = self._occupied_video_items(tl, cur_frames)
            for _track, item in items:
                item.SetClipColor(color)
            self._highlight = None
            self._delete_highlight_state()
            return True, None, len(items)
        except Exception as e:
            return False, repr(e), None

    def _find_item_by_unique_id(self, unique_id, track_hint=None):
        """Locate a TimelineItem by GetUniqueId(), used by
        clear_highlight/restore_highlight to find the previously-tinted
        clip regardless of whether the playhead has since moved off its
        track -- UNLIKE _video_item_on_track, this does NOT require the
        playhead to still be over the clip (the tint must stay
        restorable even after the user scrubs or jogs away).

        track_hint (the track the clip was tinted on) is tried FIRST as a
        fast path -- the clip usually hasn't moved tracks -- then falls
        back to a full scan of every video track if the hint misses or is
        absent (e.g. the clip was moved to a different track since being
        tinted).

        Returns the TimelineItem, or None if not found (no live timeline,
        or the clip was deleted).
        """
        try:
            proj = self.resolve.GetProjectManager().GetCurrentProject()
            tl = proj.GetCurrentTimeline() if proj else None
            if tl is None:
                return None
        except Exception:
            return None

        def _scan_track(track):
            try:
                items = tl.GetItemListInTrack("video", int(track)) or []
            except Exception:
                return None
            for item in items:
                try:
                    if item.GetUniqueId() == unique_id:
                        return item
                except Exception:
                    continue
            return None

        if track_hint is not None:
            item = _scan_track(track_hint)
            if item is not None:
                return item

        try:
            count = tl.GetTrackCount("video")
        except Exception:
            return None
        for track in range(1, int(count) + 1):
            if track_hint is not None and track == int(track_hint):
                continue  # already tried above
            item = _scan_track(track)
            if item is not None:
                return item
        return None

    def _write_highlight_state(self, state):
        """Persist `state` to highlight_state_path -- best-effort; a write
        failure (e.g. an unmounted data volume) is swallowed since the
        in-memory self._highlight is still authoritative for the rest of THIS
        process's lifetime, and crash-safety is inherently best-effort."""
        try:
            d = os.path.dirname(self.highlight_state_path)
            if d and not os.path.isdir(d):
                # Only ever create it through the mount-guarded helper when
                # the path is inside the data root; a test pointing this at
                # its own tmpdir already has the directory.
                if ensure_data_dir("state") is None:
                    return
            with open(self.highlight_state_path, "w") as f:
                json.dump(state, f)
        except Exception:
            pass

    def _delete_highlight_state(self):
        try:
            os.remove(self.highlight_state_path)
        except FileNotFoundError:
            pass
        except Exception:
            pass

    @staticmethod
    def _restore_item_color(item, original_color):
        """Restore `item`'s clip color to original_color -- ClearClipColor()
        when original_color is empty/None (Resolve's GetClipColor()
        convention for "no color set"), otherwise SetClipColor(original_color).
        Returns True/False (never raises)."""
        try:
            if not original_color:
                item.ClearClipColor()
            else:
                item.SetClipColor(original_color)
            return True
        except Exception:
            return False

    @staticmethod
    def _capture_original_color(item, highlight_color):
        """Capture `item`'s CURRENT clip color as the value every restore
        path (_restore_item_color / clear_highlight / restore_highlight)
        will later hand back -- but NEVER capture the tool's OWN highlight
        color as "original". If the clip is already colored exactly
        `highlight_color` when we go to tint it (a leaked/leftover tint
        from a prior select whose restore was lost -- app force-kill before
        restore_highlight's crash-safety ran, a stray old-D1 highlight,
        etc.), capturing it as "original" would make the restore path hand
        it right back, so the leak would never self-correct -- C5/clear on
        an already-Beige clip would restore it right back to Beige instead
        of clearing it. Returns "" in that case (Resolve's own
        GetClipColor() convention for "no color set" -- the same value
        _restore_item_color() maps to ClearClipColor()), so
        select-then-clear on a leaked-tint clip lands on NO color, not
        back on the tint.

        Caveat: a clip the user genuinely, deliberately colored the exact
        highlight color is indistinguishable from a leaked tint here and
        gets treated as one. Accepted -- `highlight_color` (config
        `highlightColor`, e.g. "Beige") is this tool's reserved color, and
        a self-correcting leak is the cheaper failure mode than a stray
        tint that never clears.

        Never raises on its own (GetClipColor() exceptions propagate to
        the caller's existing try/except, same as before this helper
        existed).
        """
        current = item.GetClipColor() or ""
        if current == highlight_color:
            return ""
        return current

    def highlight_clip(self, track, color):
        """D1 active-clip highlight: restore the currently-tinted clip (if
        any) to its stored original color, THEN tint the clip at `track`
        with `color` -- so at most ONE clip is ever tinted at a time, and
        moving D1 from clip A to clip B always restores A exactly
        (including "no color" -> ClearClipColor). Persists
        {"unique_id","original_color","track"} to highlight_state_path the
        instant the new tint is applied, for crash-safety (see
        restore_highlight).

        Restoring the PREVIOUS clip is best-effort: if it can no longer be
        found (deleted, moved off every track), the failure does NOT block
        tinting the NEW clip -- a user turning D1 must always see the new
        target tinted. In-memory/on-disk highlight state is cleared before
        attempting the new tint either way, so a failed restore never
        leaves stale state pointing at the wrong clip.

        The NEW clip's original color is captured via
        _capture_original_color(), which never captures `color` itself as
        "original" -- if the clip is already tinted `color` (a leaked tint
        from a prior select whose restore was lost), the capture is treated
        as "no color" so the leak self-corrects on the next restore instead
        of being handed right back.

        Returns (ok: bool, err: str|None). err carries the NEW clip's
        resolution error (e.g. "no_clip_on_track") when tinting fails; a
        previous-restore failure that doesn't block the new tint is not
        surfaced as an error (ok=True) since the new tint DID succeed.
        """
        prev_restore_err = None
        if self._highlight is not None:
            prev = self._highlight
            self._highlight = None
            self._delete_highlight_state()
            prev_item = self._find_item_by_unique_id(prev["unique_id"], prev.get("track"))
            if prev_item is None:
                prev_restore_err = "restore_prev_not_found"
            elif not self._restore_item_color(prev_item, prev["original_color"]):
                prev_restore_err = "restore_prev_failed"

        item, err = self._video_item_on_track(track)
        if item is None:
            return False, err or prev_restore_err

        try:
            # See _capture_original_color's doc comment: never captures
            # `color` itself as "original" (leaked-tint self-correction).
            original_color = self._capture_original_color(item, color)
            unique_id = item.GetUniqueId()
        except Exception as e:
            return False, repr(e)

        try:
            item.SetClipColor(color)
        except Exception as e:
            return False, repr(e)

        state = {"unique_id": unique_id, "original_color": original_color, "track": int(track)}
        self._highlight = state
        self._write_highlight_state(state)
        return True, None

    def clear_highlight(self):
        """Engine Stop(): restore the currently-highlighted clip (if any)
        to its original color and clear all highlight state (in-memory +
        state file). A no-op (ok=True) when nothing is highlighted --
        safe to call unconditionally, regardless of whether the highlight
        feature is currently enabled in config.

        Returns (ok: bool, err: str|None).
        """
        if self._highlight is None:
            return True, None
        prev = self._highlight
        self._highlight = None
        self._delete_highlight_state()
        item = self._find_item_by_unique_id(prev["unique_id"], prev.get("track"))
        if item is None:
            return False, "clip_not_found"
        if not self._restore_item_color(item, prev["original_color"]):
            return False, "restore_failed"
        return True, None

    def restore_highlight(self):
        """Crash-safety: called once on engine Start()/sidecar connect. If
        highlight_state_path exists (a prior process was force-killed
        mid-tint, so clear_highlight never ran), find that clip by
        unique_id and restore its original color, then delete the state
        file -- so a force-kill leaves at most one stray-tinted clip,
        which self-heals on the next launch.

        A no-op (ok=True, err=None) when no state file exists. The state
        file is ALWAYS deleted by the end of this call (even when the
        clip can no longer be found, or the file is corrupt) so a stale
        file never blocks future D1 use or retries forever.

        Returns (ok: bool, err: str|None).
        """
        try:
            with open(self.highlight_state_path, "r") as f:
                state = json.load(f)
        except FileNotFoundError:
            return True, None
        except Exception as e:
            self._delete_highlight_state()
            return False, "state_file_corrupt: %s" % repr(e)

        unique_id = state.get("unique_id")
        original_color = state.get("original_color", "")
        track = state.get("track")

        item = self._find_item_by_unique_id(unique_id, track)
        self._delete_highlight_state()
        self._highlight = None
        if item is None:
            return False, "clip_not_found"
        if not self._restore_item_color(item, original_color):
            return False, "restore_failed"
        return True, None

    # -------------------------------------------------------------------
    # Direction-button clip selection (Up/Down/Left/Right, Edit page) --
    # replaces D1's knob-turn "selectClip" rotation. Shares self._highlight
    # / _write_highlight_state / _delete_highlight_state / clear_highlight
    # / _restore_item_color / _find_item_by_unique_id with the D1
    # highlight feature above -- crash-safety (restore_highlight on
    # engine Start()) and Stop()'s clear_highlight protect THIS feature's
    # tints for free, since both operate purely on the shared
    # self._highlight state, agnostic of which feature did the tinting.
    # -------------------------------------------------------------------

    def _select_and_tint(self, item, track, color):
        """Shared tail for select_clip_vertical/select_clip_horizontal:
        best-effort restore whichever clip is currently tinted (via the
        EXISTING clear_highlight() -- its return value is intentionally
        discarded here, same "never blocks tinting the new clip" posture
        as highlight_clip's own restore-prev step, see highlight_clip's
        docstring), then tint `item` on `track` and persist the new
        highlight state (self._highlight / the on-disk crash-safety
        file -- the SAME state highlight_clip/clear_highlight/
        restore_highlight already manage).

        Returns (ok, err, track, unique_id, start, end, original_color)
        -- richer than highlight_clip's plain (ok, err), since the
        Up/Down/Left/Right caller (engine.go) needs the clip's frame
        range for its playhead-left-the-clip auto-reset, and needs
        track/unique_id for its own selection-state bookkeeping. Every
        field beyond (ok, err) is None on failure.

        `item`'s original color is captured via _capture_original_color(),
        which never captures `color` itself as "original" -- selecting a
        clip already tinted `color` (a leaked highlight from a prior
        select/D1 use whose restore was lost, e.g. an app restart) captures
        "" instead, so C5/clear on it lands on NO color rather than handing
        the leaked tint right back. The returned `original_color` reflects
        this same corrected value, not the raw GetClipColor() read.
        """
        self.clear_highlight()
        try:
            start = int(item.GetStart())
            end = int(item.GetEnd())
            # See _capture_original_color's doc comment: never captures
            # `color` itself as "original" (leaked-tint self-correction --
            # e.g. a clip left Beige by a lost restore must clear to NO
            # color on C5, not restore right back to Beige).
            original_color = self._capture_original_color(item, color)
            unique_id = item.GetUniqueId()
        except Exception as e:
            return False, repr(e), None, None, None, None, None
        try:
            item.SetClipColor(color)
        except Exception as e:
            return False, repr(e), None, None, None, None, None
        state = {"unique_id": unique_id, "original_color": original_color, "track": int(track)}
        self._highlight = state
        self._write_highlight_state(state)
        return True, None, int(track), unique_id, start, end, original_color

    def select_clip_vertical(self, direction, current_track, color):
        """Up/Down button (Edit page): select the clip on the next
        OCCUPIED video track in `direction` (+1 = Up/toward higher,
        topmost track numbers; -1 = Down/toward lower, bottommost track
        numbers) relative to `current_track` (0/None = no current
        selection -- bootstraps from the TOPMOST occupied clip at the
        playhead, same "no selection yet" default as
        _target_item(track=None)/bump_property's own top-occupied
        default).

        Mirrors engine.go's nextTrackIndex bidirectional-clamp-NO-WRAP
        semantics exactly, reimplemented here in Python since this
        op resolves the whole step (occupied scan + index math) in ONE
        sidecar round trip rather than two (D1's OLD rotation asked the
        Go engine to fetch clips_at_playhead separately and compute the
        index itself -- nextTrackIndex/dispatchSelectClip are unchanged
        and still exist, exercised directly by their own Go unit tests,
        but this NEW feature does not reuse that two-round-trip shape).
        Reaching the top/bottom occupied track and pressing further in
        the same direction RE-SELECTS that same track's clip (idempotent
        restore-self-then-retint via _select_and_tint, matching D1's old
        clamp behavior) rather than erroring.

        Returns (ok, err, track, unique_id, start, end, original_color)
        -- see _select_and_tint's doc comment for the shape. ok=False
        (all trailing fields None) on a timeline-resolution refusal or
        when NO video track has a clip at the playhead at all
        ("no_clip").
        """
        tl, cur_frames, _fps, err = self._read_timeline_tc_frames()
        if err is not None:
            return False, err, None, None, None, None, None
        try:
            occupied = self._occupied_video_items(tl, cur_frames)
        except Exception as e:
            return False, repr(e), None, None, None, None, None
        if not occupied:
            return False, "no_clip", None, None, None, None, None

        tracks = [t for t, _item in occupied]
        n = len(tracks)
        idx = tracks.index(current_track) if current_track in tracks else -1
        if idx == -1:
            new_idx = n - 1 if direction < 0 else 0
        else:
            new_idx = idx + direction
            new_idx = max(0, min(new_idx, n - 1))

        new_track, item = occupied[new_idx]
        return self._select_and_tint(item, new_track, color)

    # PERF (2026-08-12): select_clip_horizontal is the ONE op in this file
    # that scans EVERY clip on EVERY video track (not just occupied-at-
    # playhead, like _occupied_video_items) -- every Left/Right press
    # re-walked GetItemListInTrack per track from scratch, the dominant
    # cost of press-to-react latency on a timeline with many clips/tracks.
    # Cached for HORIZ_SCAN_TTL_S, keyed loosely by GetName() as a
    # best-effort "did the user switch timelines" signal -- NOT a full
    # content hash (that would cost as much as the walk it's meant to
    # avoid). Deliberately much SHORTER than CLIP_MAP_TTL_S (the HUD
    # strip's 3s display-only cache, see timeline_clip_map above): a stale
    # HUD pixel is harmless, but a stale clip list here feeds a
    # SetCurrentTimecode + tint, so the staleness window is kept tight.
    #
    # Deliberately NOT shared with _occupied_video_items (the 250ms
    # status-tick / D1 / select_clip_vertical scan) -- stacking an
    # independent cache under an already-250ms poller could double ITS
    # effective staleness window, and that path was not the one reported
    # as slow. Scope stays narrow: only select_clip_horizontal's own
    # full-timeline scan is cached.
    HORIZ_SCAN_TTL_S = 0.25

    def _horiz_scan_track_items(self, tl):
        """{track:int -> [item,...]} for every video track (1..
        GetTrackCount("video")), cached for HORIZ_SCAN_TTL_S -- see the
        constant's doc comment above. Empty dict means no video tracks at
        all (GetTrackCount returned 0/None). Used ONLY by
        select_clip_horizontal.
        """
        now = time.monotonic()
        try:
            name = tl.GetName()
        except Exception:
            name = None
        cached = getattr(self, "_horiz_scan_cache", None)
        if cached is not None and cached[0] == name and (now - cached[1]) < self.HORIZ_SCAN_TTL_S:
            return cached[2]

        count = tl.GetTrackCount("video") or 0
        tracks = {}
        for track in range(1, int(count) + 1):
            tracks[track] = tl.GetItemListInTrack("video", track) or []
        self._horiz_scan_cache = (name, now, tracks)
        return tracks

    def select_clip_horizontal(self, direction, current_track, current_start, color):
        """Left/Right button (Edit page, page-aware -- see engine.go's
        hard-wired Left/Right nav notes): select the PREVIOUS
        (direction=-1, Left) or NEXT (direction=+1, Right) clip relative
        to (current_track, current_start) -- the currently-selected
        clip's track + start frame. 0/None `current_track` means no
        current selection yet -- bootstraps the anchor from the
        TOPMOST occupied clip at the playhead (same "no selection"
        default as select_clip_vertical/_target_item(track=None)).

        "Previous"/"next" is the GLOBALLY closest clip by start frame
        across ALL occupied video tracks -- NOT same-track-preferred.
        There can be clips on OTHER tracks sitting between the anchor and
        the next/previous clip on its own track; those closer clips must
        win regardless of which track they're on. Left (direction=-1):
        among every clip whose start < anchor_start, the one with the
        LARGEST start (closest below). Right (direction=+1): among every
        clip whose start > anchor_start, the one with the SMALLEST start
        (closest above). Ties (identical start on more than one track)
        resolve to the TOP-most (highest-numbered) track, consistent with
        this file's top-occupied default (_target_item(track=None) /
        select_clip_vertical's/select_clip_horizontal's own bootstrap).
        Moves the playhead to the found clip's START FRAME via
        SetCurrentTimecode (the SAME absolute-frame convention as
        jog()/_video_item_on_track -- item.GetStart()/GetEnd() are
        already in the same frame space as GetCurrentTimecode()'s
        parsed frame count, unlike AddMarker's timeline-start-relative
        frameId), THEN tints it -- same restore-prev-first, best-effort
        posture as select_clip_vertical/highlight_clip (via
        _select_and_tint).

        *** FIXED 2026-08-12 (BUG 2, live report) ***: when bootstrapping
        (no current selection), the anchor FRAME is the PLAYHEAD
        (cur_frames), never the bootstrap clip's own GetStart(). The old
        code anchored on the topmost-occupied clip's start -- fine when
        that clip's start happens to sit near the playhead, but wrong
        whenever it's a TIMELINE-LONG clip (e.g. a full-length background
        graphic on a lower video track): its GetStart() is always 0 no
        matter where the playhead is, so Right always resolved relative
        to frame 0 (the globally-earliest clip on the whole timeline)
        instead of the next clip near wherever the user actually was.
        The topmost-occupied clip itself (the bootstrap anchor) is
        excluded from candidacy by identity, not by the anchor_start
        comparison alone -- otherwise, whenever the playhead sits
        PARTWAY THROUGH that clip (its start is strictly less than the
        playhead), it would qualify as its own "previous" result and
        Left would just re-select the clip already under the playhead.
        This exclusion has no effect on the non-bootstrap path below:
        there, the CURRENTLY SELECTED clip is excluded for free by the
        strict </> comparison against its own start (current_start),
        exactly as before.

        Returns (ok, err, track, unique_id, start, end, original_color)
        -- same shape as select_clip_vertical. err is "no_more_clips"
        when no clip exists in that direction on ANY track (already at
        the first/last clip, globally) -- a graceful "nothing to move
        to", and a true no-op: the playhead is never moved and nothing
        is (re)tinted, so the current selection and playhead position are
        left exactly as they were. Unlike select_clip_vertical's clamp
        (which re-selects the same track), horizontal has no "stay put but
        re-select" fallback once every clip in that direction is
        exhausted.
        """
        tl, cur_frames, fps, err = self._read_timeline_tc_frames()
        if err is not None:
            return False, err, None, None, None, None, None
        try:
            anchor_track = current_track
            anchor_start = current_start
            anchor_item = None
            if not anchor_track:
                occupied = self._occupied_video_items(tl, cur_frames)
                if not occupied:
                    return False, "no_clip", None, None, None, None, None
                anchor_track, anchor_item = occupied[-1]
                # See "*** FIXED 2026-08-12 ***" above: anchor on the
                # PLAYHEAD, not anchor_item's own start.
                anchor_start = cur_frames

            tracks = self._horiz_scan_track_items(tl)
            if not tracks:
                return False, "no_clip", None, None, None, None, None

            # Globally closest across ALL video tracks by start frame --
            # NOT same-track-preferred (see docstring: a clip on another
            # track can sit strictly between the anchor and the next/prev
            # clip on its OWN track, and that closer clip must win).
            candidates = []
            for track, items in tracks.items():
                for it in items:
                    if it is anchor_item:
                        # Bootstrap only (anchor_item is None on the
                        # non-bootstrap path) -- never offer the clip the
                        # playhead is already sitting inside back as its
                        # own "previous"/"next" result. See the
                        # "*** FIXED 2026-08-12 ***" docstring note above.
                        continue
                    st = int(it.GetStart())
                    is_candidate = st < anchor_start if direction < 0 else st > anchor_start
                    if is_candidate:
                        candidates.append((st, track, it))

            if not candidates:
                return False, "no_more_clips", None, None, None, None, None

            if direction < 0:
                # Left/previous: closest = LARGEST start below the anchor.
                # Tie (same start on multiple tracks) -> highest track
                # number wins -- max() on (start, track) achieves both in
                # one pass (primary key start, secondary key track).
                _start, new_track, item = max(candidates, key=lambda c: (c[0], c[1]))
            else:
                # Right/next: closest = SMALLEST start above the anchor.
                # Tie -> highest track number wins -- min() on
                # (start, -track) minimizes start first, then minimizes
                # -track (== maximizes track) among ties.
                _start, new_track, item = min(candidates, key=lambda c: (c[0], -c[1]))

            new_tc = self._format_frames(max(0, int(item.GetStart())), fps)
            tl.SetCurrentTimecode(new_tc)
        except Exception as e:
            return False, repr(e), None, None, None, None, None

        return self._select_and_tint(item, new_track, color)

    def _target_item(self, track=None):
        """Resolve the TimelineItem that an Edit-page property op
        (bump_property/reset_property) should target -- the single,
        named place this "D1 override vs. default" contract lives, so
        the two callers can never silently diverge on it.

        track: optional D1-selected video-track number, forwarded
        straight from the Go engine's activeTrack (0/unset there is
        never passed through -- see engine.go's "track" omit-when-zero
        convention). When given (D1 has an active override), resolves
        THAT SPECIFIC video track's clip via _video_item_on_track() --
        a clear "no_clip_on_track" error (never a silent fallback to
        some other track's clip) when that track has no clip at the
        playhead.

        When track is None (the default -- NO active D1 override, e.g.
        D1 has never been turned this session, or the engine's auto-
        reset just cleared a stale selection because the top-occupied
        clip changed -- see engine.go's clip-id auto-reset), this
        targets the TOPMOST OCCUPIED video clip at the playhead: the
        clip on the HIGHEST-numbered video track whose frame range
        contains the current playhead frame, found via the SAME
        top-down occupied-track scan (_occupied_video_items(), shared
        with clips_at_playhead()/set_clips_color()) -- NOT
        GetCurrentVideoItem(). "no_clip" only when NO video track has a
        clip at the playhead at all (a genuine gap).

        *** FIXED 2026-07-08, root-caused live against Resolve 21 ***
        GetCurrentVideoItem() used to be this default's source (via the
        now-removed _current_video_item_fresh()). It is UNRELIABLE for
        this purpose: Resolve scopes it to whatever video track is its
        OWN internal "current target track" only, returning None
        whenever THAT track is empty at the playhead -- even while
        OTHER video tracks plainly have clips there. Reproduced live:
        GetCurrentVideoItem() returned None across 6 consecutive polls
        (bump_property errored "no_clip" every time) while a manual
        top-down scan found occupied clips on V1/V2/V3/V4 at that exact
        frame -- the knobs controlled nothing even though clips were
        plainly present under the playhead. The occupied-track scan has
        no such blind spot.

        Returns (item, err) -- item is None on any failure; err is
        "no_clip_on_track" / a _video_item_on_track refusal code for the
        track!=None path; for the track=None path, err is one of
        _read_timeline_tc_frames()'s refusal codes ("no_timeline" etc),
        an exception repr from the scan itself, or "no_clip" when the
        scan succeeds but finds no occupied video track at the playhead.
        """
        if track is not None:
            return self._video_item_on_track(track)
        tl, cur_frames, _fps, err = self._read_timeline_tc_frames()
        if err is not None:
            return None, err
        try:
            occupied = self._occupied_video_items(tl, cur_frames)
        except Exception as e:
            return None, repr(e)
        if not occupied:
            return None, "no_clip"
        _top_track, item = occupied[-1]
        return item, None

    def reset_property(self, property_name=None, gang=None, track=None):
        """Set one or more TimelineItem properties on a clip back to
        their Resolve default (see PROPERTY_DEFAULTS) -- the "0 for each
        crop direction / transform back to default" knob press. Same
        clip-resolution and (property_name | gang) shape as
        bump_property, but an ABSOLUTE set to the default rather than a
        relative delta.

        track: see _target_item's doc comment -- None (default) targets
        the topmost-occupied video clip at the playhead; a track number
        targets that specific D1-selected track's clip instead.

        Returns (ok: bool, err: str|None, values: dict[str,float]|None).
        """
        item, err = self._target_item(track)
        if item is None:
            return False, err, None
        names = list(gang) if gang else ([property_name] if property_name else [])
        if not names:
            return False, "no_property", None
        try:
            values = {}
            for name in names:
                default = self.PROPERTY_DEFAULTS.get(name, 0.0)
                item.SetProperty(name, default)
                values[name] = default
            return True, None, values
        except Exception as e:
            return False, repr(e), None

    def bump_property(self, property_name=None, gang=None, delta=0.0,
                       min_val=None, max_val=None, track=None):
        """Relative read-modify-write of one or more TimelineItem
        properties.

        track: see _target_item's doc comment -- None (default) targets
        the topmost-occupied video clip at the playhead; a track number
        targets that specific D1-selected track's clip instead.

        property_name: single property name (e.g. "Pan"), OR
        gang: a list of property names (e.g. ["ZoomX", "ZoomY"]) to bump
        with the SAME delta -- each member is read + deltaed
        INDEPENDENTLY (never mirrored from one to another), so pre-
        existing per-axis differences are preserved.

        min_val/max_val optionally clamp the ABSOLUTE resulting value
        (not the delta) for every named property.

        Returns (ok: bool, err: str|None, values: dict[str,float]|None).
        """
        item, err = self._target_item(track)
        if item is None:
            return False, err, None
        names = list(gang) if gang else ([property_name] if property_name else [])
        if not names:
            return False, "no_property", None
        try:
            values = {}
            for name in names:
                cur = item.GetProperty(name)
                cur = float(cur) if cur is not None else 0.0
                new_val = cur + delta
                if min_val is not None:
                    new_val = max(new_val, float(min_val))
                if max_val is not None:
                    new_val = min(new_val, float(max_val))
                item.SetProperty(name, new_val)
                values[name] = new_val
            return True, None, values
        except Exception as e:
            return False, repr(e), None

    def _read_timeline_tc_frames(self):
        """Shared preamble for jog()/jog_snap(): resolve the live
        Timeline, read its current timecode + integer frame rate, and
        convert to an absolute frame count.

        Returns (tl, cur_frames, fps, err). On success err is None and
        tl/cur_frames/fps are populated; on any refusal tl/cur_frames/fps
        are None and err is one of "no_timeline" / "no_timecode" /
        "drop_frame_unsupported" / "non_integer_fps_unsupported" /
        "bad_timecode_format" -- the exact refusal codes jog() has always
        returned, now shared so jog_snap() refuses identically rather
        than mis-computing a boundary against a drop-frame or
        non-integer-fps (23.976/29.97/59.94) timeline.
        """
        try:
            proj = self.resolve.GetProjectManager().GetCurrentProject()
            tl = proj.GetCurrentTimeline() if proj else None
            if tl is None:
                return None, None, None, "no_timeline"
            cur_tc = tl.GetCurrentTimecode()
            if not cur_tc:
                return None, None, None, "no_timecode"
            if ";" in cur_tc:
                return None, None, None, "drop_frame_unsupported"
            fps_raw = tl.GetSetting("timelineFrameRate")
            fps_f = float(fps_raw)
            fps = int(round(fps_f))
            if abs(fps_f - fps) > 1e-6:
                return None, None, None, "non_integer_fps_unsupported"
            parts = cur_tc.split(":")
            if len(parts) != 4:
                return None, None, None, "bad_timecode_format"
            h, m, s, f = (int(p) for p in parts)
            cur_frames = ((h * 60 + m) * 60 + s) * fps + f
            return tl, cur_frames, fps, None
        except Exception as e:
            return None, None, None, repr(e)

    @staticmethod
    def _format_frames(frames, fps):
        """Format an absolute frame count as a non-drop 'HH:MM:SS:FF'
        timecode string -- mirrors internal/tc.FormatFrames (see tc.go)
        exactly. frames is assumed already clamped to >=0 by the caller."""
        nf = frames % fps
        total_sec = frames // fps
        ns = total_sec % 60
        total_min = total_sec // 60
        nm = total_min % 60
        nh = total_min // 60
        return "%02d:%02d:%02d:%02d" % (nh, nm, ns, nf)

    def jog(self, delta_frames):
        """Advance the timeline playhead by delta_frames (signed) via
        SetCurrentTimecode. Reads the current timecode + timeline frame
        rate fresh on every call (no caching -- the playhead moves).

        Mirrors internal/tc's ParseFrames/FormatFrames/Advance algorithm
        (see internal/tc/tc.go) since only this Python process can touch
        the live Resolve Timeline object. Drop-frame timecodes (semicolon
        frame separator) and non-integer frame rates (23.976, 29.97,
        59.94) are refused rather than silently mis-computed -- v1 is
        live-verified only against the confirmed 25fps non-drop Edit
        timeline; drop-frame math is deferred.
        """
        tl, cur_frames, fps, err = self._read_timeline_tc_frames()
        if err is not None:
            return False, err, None
        try:
            new_frames = max(0, cur_frames + int(delta_frames))
            new_tc = self._format_frames(new_frames, fps)
            ok = tl.SetCurrentTimecode(new_tc)
            return bool(ok), None, new_tc
        except Exception as e:
            return False, repr(e), None

    def jog_snap(self, snap_seconds, direction):
        """Snap the playhead to the next round `snap_seconds`-boundary
        timecode in `direction` (+1 forward/later, -1 backward/earlier)
        -- the fast-dial jog tiers (5s/10s/30s from internal/tc.JogTier).

        Mirrors internal/tc.SnapBoundaryFrames (see tc.go) exactly: if
        the playhead already sits EXACTLY on a boundary, this still moves
        a FULL tier in that direction (never a no-op). Shares the same
        timeline/fps/drop-frame guards as jog() via
        _read_timeline_tc_frames -- a drop-frame or non-integer-fps
        (23.976/29.97/59.94) timeline is refused here exactly like jog(),
        rather than risk mis-snapping it; the caller (sidecar.py's
        "jog" dispatch) falls back to the plain frame-delta jog() in that
        case using the "frames" value the Go engine always includes
        alongside a snap request.

        Returns (ok: bool, err: str|None, new_tc: str|None).
        """
        if snap_seconds is None or snap_seconds <= 0:
            return False, "bad_snap_seconds", None
        tl, cur_frames, fps, err = self._read_timeline_tc_frames()
        if err is not None:
            return False, err, None
        try:
            tier_frames = snap_seconds * fps
            floor_boundary = (cur_frames // tier_frames) * tier_frames
            if direction is not None and direction >= 0:
                new_frames = floor_boundary + tier_frames
            elif floor_boundary == cur_frames:
                new_frames = floor_boundary - tier_frames
            else:
                new_frames = floor_boundary
            new_frames = max(0, new_frames)
            new_tc = self._format_frames(new_frames, fps)
            ok = tl.SetCurrentTimecode(new_tc)
            return bool(ok), None, new_tc
        except Exception as e:
            return False, repr(e), None

    def jog_seconds(self, seconds):
        """D1 knob (Edit page): COARSE playhead jump by ±seconds -- the
        coarse counterpart to jog()'s fine per-frame scrub (see engine.go's
        "jogSeconds" binding / dispatchJogSeconds). Fires ONE fixed-size
        jump per call -- unlike jog()'s per-tick accelerating scrub and
        jog_snap()'s tier system, this has no acceleration curve at all;
        the Go engine's detent accumulator (jogSecondsAccum) is what turns
        many raw dial ticks into exactly one call here.

        Superseded on D1 by jog_minute_snap() below (a fixed ±N-second
        OFFSET from wherever the playhead happened to be started felt
        less predictable to the user than landing on a round timecode) --
        kept, unreachable in production but still directly unit-tested,
        rather than deleted.

        Reads the current timecode + integer frame rate via the SAME
        shared _read_timeline_tc_frames() preamble as jog()/jog_snap()
        (same drop-frame/non-integer-fps refusal codes), converts seconds
        to a signed frame delta (round(seconds * fps)), and CLAMPS the
        result to the live timeline's [GetStartFrame(), GetEndFrame())
        extent -- unlike jog()'s bare max(0, ...) floor, this never runs
        the playhead past EITHER end of the timeline (a 60s jump near the
        head or tail would otherwise silently request an out-of-range
        timecode). Half-open, mirroring this codebase's existing
        start/end clip-range convention (see add_marker's
        "clip.GetEnd() - clip.GetStart()" comment above) -- the last
        valid absolute frame is GetEndFrame() - 1.

        Returns (ok: bool, err: str|None, new_tc: str|None). Never raises
        -- any unexpected exception (including GetStartFrame()/
        GetEndFrame() returning something non-numeric) is caught and
        returned as (False, repr(e), None), same posture as jog()/
        jog_snap().
        """
        tl, cur_frames, fps, err = self._read_timeline_tc_frames()
        if err is not None:
            return False, err, None
        try:
            delta_frames = int(round(float(seconds) * fps))
            start = int(tl.GetStartFrame())
            end = int(tl.GetEndFrame())
            new_frames = cur_frames + delta_frames
            if end > start:
                new_frames = max(start, min(new_frames, end - 1))
            else:
                new_frames = max(start, new_frames)
            new_tc = self._format_frames(new_frames, fps)
            ok = tl.SetCurrentTimecode(new_tc)
            return bool(ok), None, new_tc
        except Exception as e:
            return False, repr(e), None

    def jog_minute_snap(self, direction):
        """D1 knob (Edit page): snap the playhead to the next/previous
        WHOLE-MINUTE timecode boundary (SS:FF == "00:00") in the given
        `direction` (+1 forward/later, -1 backward/earlier) -- the
        CURRENT D1 binding, replacing the fixed ±60s offset jump
        (jog_seconds, above, kept but no longer bound to D1) with a
        snap-to-grid gesture: D1 now always lands the playhead on a round
        minute, never an arbitrary offset from wherever it started.

        A "whole minute" boundary is any frame `f` (in the SAME frame
        space _read_timeline_tc_frames() returns) where
        `f % (60*fps) == 0`. This holds regardless of the timeline's TC
        start offset (e.g. a timeline starting at 01:00:00:00) because
        cur_frames is parsed directly off the DISPLAYED HH:MM:SS:FF
        digits -- NOT off GetStartFrame()/frame 0 -- so a multiple of
        60*fps in that same frame space always round-trips back through
        _format_frames() to SS:FF == "00:00" in the shown timecode,
        whatever hour it falls in (_format_frames uses the exact same
        from-00:00:00:00 formatting convention, so both sides of this
        computation agree on the same origin -- no separate GetStartFrame
        offset math is needed here).

        Landing EXACTLY on a boundary still advances a FULL minute in
        `direction` -- never a no-op -- mirroring jog_snap()'s "always a
        full tier" contract:
          - direction > 0: the next boundary strictly AFTER cur_frames.
          - direction < 0: the previous boundary strictly BEFORE
            cur_frames.

        Clamped to the live timeline's [GetStartFrame(), GetEndFrame())
        extent, but UNLIKE jog_seconds() this does NOT clamp to the edge:
        if the computed boundary would land outside that range (already
        at/before the first in-range minute and going back, or at/past
        the last in-range minute and going forward), this is a genuine
        NO-OP -- SetCurrentTimecode is never called and the playhead does
        not move. (An out-of-range snap should leave the playhead exactly
        where the user left it, not silently jump a partial/unpredictable
        distance to the timeline's edge.)

        Returns (ok: bool, err: str|None, new_tc: str|None). Never raises
        -- any unexpected exception (including GetStartFrame()/
        GetEndFrame() returning something non-numeric) is caught and
        returned as (False, repr(e), None), same posture as jog()/
        jog_snap()/jog_seconds(). direction of 0 or None is refused
        up front as "bad_direction" (mirrors jog_snap's bad_snap_seconds
        guard) -- the Go engine's detent accumulator never fires with a
        zero direction, but this guards the sidecar op directly too.
        """
        if direction is None or direction == 0:
            return False, "bad_direction", None
        tl, cur_frames, fps, err = self._read_timeline_tc_frames()
        if err is not None:
            return False, err, None
        try:
            frames_per_minute = 60 * fps
            start = int(tl.GetStartFrame())
            end = int(tl.GetEndFrame())
            last_valid = (end - 1) if end > start else start

            if direction > 0:
                new_frames = (cur_frames // frames_per_minute + 1) * frames_per_minute
            else:
                if cur_frames % frames_per_minute == 0:
                    new_frames = cur_frames - frames_per_minute
                else:
                    new_frames = (cur_frames // frames_per_minute) * frames_per_minute

            if new_frames < start or new_frames > last_valid:
                # Out of the timeline's range -- genuine no-op, playhead
                # stays exactly where it was (SetCurrentTimecode is never
                # called).
                return True, None, self._format_frames(cur_frames, fps)

            new_tc = self._format_frames(new_frames, fps)
            ok = tl.SetCurrentTimecode(new_tc)
            return bool(ok), None, new_tc
        except Exception as e:
            return False, repr(e), None

    def add_marker(self, duration_seconds=1.0, color="Blue", name=None, note=None, track=None):
        """C1 button (Edit page): add a marker at the current playhead
        position -- see engine.go's addMarkerBinding/dispatchAddMarker.

        track: optional D1-selected video-track number (Batch 1, "marker
        spans the D1-selected clip" feature). When None (default,
        unchanged from before this feature), behaves exactly as before --
        marker at the playhead, duration from duration_seconds. When
        given, resolves that track's clip under the playhead via
        _video_item_on_track() and, if one is found, OVERRIDES both frame
        and duration to span that clip exactly: frame = clip.GetStart() -
        tl.GetStartFrame(), duration_frames = max(1, clip.GetEnd() -
        clip.GetStart()) + MARKER_CLIP_SPAN_TAIL_COMPENSATION (this op's
        OWN explicit clamp -- resize_marker's clamp operates on a resize
        delta, not initial creation, so it is not reused here; see that
        constant for why the raw span lands one frame short of the cut).
        The COMPENSATED value is what is both written to Resolve and
        returned, so this op never reports a duration Resolve does not
        actually hold. Unlike bump_property/reset_property (which
        treat _video_item_on_track's "no_clip_on_track" -- or any other
        refusal -- as a hard failure), add_marker treats ANY refusal here
        as a GRACEFUL FALLBACK to the playhead+duration_seconds values
        already computed: a stale/no D1 selection must never turn a C1
        press into an error, it just degrades to the pre-existing
        fixed-duration-at-playhead behavior. The marker's name/color/note
        and the marker_exists pre-check (below) are unaffected by which
        branch supplied frame/duration.

        *** FLAGGED FOR LIVE VERIFICATION ***: Resolve's
        Timeline.AddMarker(frameId, color, name, note, duration,
        customData) expects frameId 0-based RELATIVE TO THE TIMELINE'S
        OWN START FRAME (Timeline.GetStartFrame()), NOT the absolute
        frame count implied by the playhead's raw timecode string. A
        timeline that starts at 01:00:00:00 (a common Resolve default)
        has a nonzero GetStartFrame(), and frameId must be computed
        relative to THAT, not 0 -- frameId = cur_frames -
        tl.GetStartFrame(), where cur_frames is the same absolute-frame
        parse _read_timeline_tc_frames() already performs from
        GetCurrentTimecode(). This subtraction has NOT been confirmed
        against a real Resolve project/AddMarker call in this session
        (hard hermeticity constraint -- AddMarker mutates a real
        project); verify the marker lands exactly at the visible
        playhead position on a real timeline (especially a non-zero-
        start one) before trusting this in production.

        duration_frames = round(duration_seconds * fps), where fps is
        read the same generic way _read_timeline_tc_frames() already
        does (tl.GetSetting("timelineFrameRate")) -- confirmed 25fps on
        the user's timeline, but not hardcoded.

        name, when omitted/empty, is AUTO-NUMBERED as "M01", "M02", ...
        (see _next_auto_marker_name below) rather than a static default
        -- *** CONFIRMED LIVE, Resolve 21.0.0.47 ***: Timeline.AddMarker
        returns False when passed an empty-string name, unrelated to
        whether a marker already exists at that frame, so an empty name
        is never sent regardless. The auto-number is derived by scanning
        the CURRENT timeline's GetMarkers() for names matching "M<int>"
        and taking max+1 (1 if none match) -- this survives app restarts
        (no local counter, always re-derived from live timeline state)
        and correctly ignores the user's own non-"M" markers (e.g.
        "EP10-ID03"). DEFAULT_MARKER_NAME ("Marker") is used only as the
        LAST-RESORT fallback, when GetMarkers() itself can't be scanned
        (returns None) -- see DEFAULT_MARKER_NAME's module-level
        comment. note still defaults to "" (not None) when omitted --
        only the name argument has the empty-string AddMarker gotcha.

        Existence is checked EXPLICITLY via GetMarkers() BEFORE calling
        AddMarker (and before the auto-number scan), so a real "marker
        already exists here" failure is distinguishable from any other
        AddMarker rejection: if frame_id is already a key in
        GetMarkers(), returns a graceful {"ok": False, "err":
        "marker_exists"} without ever calling AddMarker. Otherwise
        AddMarker is called; if it STILL returns False (e.g. a race, or
        an unknown Resolve-side rejection), this returns {"ok": False,
        "err": "add_failed"} -- NOT "marker_exists", which would be a
        lie (the frame was empty, something else made AddMarker fail).

        *** LIVE-VERIFICATION FLAG ***: GetMarkers()'s key type (int vs
        str) is assumed int here (same assumption as _find_marker,
        below, which additionally falls back to a str-key lookup) --
        not exercised against a real Resolve timeline in this session.

        Returns (ok: bool, err: str|None, frame: int|None,
        duration: int|None, fps: float|None). frame is the computed
        frameId, reported even on a marker_exists/add_failed failure (so
        the caller can see exactly where the marker attempt landed) --
        None only on a timeline-resolution refusal (no_timeline,
        drop_frame_unsupported, etc) or an exception, where no frameId
        was ever computed. duration (the FINAL duration_frames actually
        used -- clip length when track resolved a clip, else the
        duration_seconds-derived value) and fps ride along on every
        successful add (mirroring resize_marker's response shape) so the
        Go engine can display the marker's real duration in seconds
        without a second round trip; both are None on any failure/
        refusal, same as frame.
        """
        tl, cur_frames, fps, err = self._read_timeline_tc_frames()
        if err is not None:
            return False, err, None, None, None
        try:
            frame_id = int(cur_frames) - int(tl.GetStartFrame())
            duration_frames = int(round(duration_seconds * fps))
            # Marker span precedence:
            #   1. an explicitly SELECTED clip (direction buttons) wins
            #   2. else the TOP-OCCUPIED clip under the playhead
            #   3. else duration_seconds (the 1s default)
            #
            # Step 2 is why a STALE selection no longer produces a WORSE
            # marker than no selection at all: an explicit track that is
            # empty at the playhead falls through INTO step 2, not past it
            # to the flat default.
            item = None
            if track is not None:
                item, _item_err = self._video_item_on_track(track)
            if item is None:
                # Top-occupied clip at the playhead. Uses the same
                # _occupied_video_items scan behind _target_item -- NEVER
                # GetCurrentVideoItem(), which is target-track-dependent and
                # returns None whenever Resolve's own current track is empty
                # at the playhead even with clips plainly stacked there
                # (proven live and removed for exactly this reason on
                # 2026-07-08).
                item, _ = self._target_item(track=None)
            if item is not None:
                try:
                    frame_id = int(item.GetStart()) - int(tl.GetStartFrame())
                    duration_frames = max(1, int(item.GetEnd()) - int(item.GetStart())
                                        + MARKER_CLIP_SPAN_TAIL_COMPENSATION)
                except Exception:
                    pass  # keep the playhead+duration_seconds values
            raw_markers = tl.GetMarkers()
            existing = raw_markers or {}
            if frame_id in existing:
                return False, "marker_exists", frame_id, None, None
            marker_name = name or self._next_auto_marker_name(raw_markers)
            ok = tl.AddMarker(frame_id, color, marker_name, note or "", duration_frames, "")
            if not ok:
                return False, "add_failed", frame_id, None, None
            return True, None, frame_id, duration_frames, fps
        except Exception as e:
            return False, repr(e), None, None, None

    _AUTO_MARKER_NAME_RE = re.compile(r"^M(\d+)$")

    @classmethod
    def _next_auto_marker_name(cls, markers):
        """Generate add_marker's next auto-numbered name "M01", "M02",
        ... by scanning `markers` (a raw GetMarkers() result, NOT the
        `existing = markers or {}` coercion used by the marker_exists
        check) for names matching ^M(\\d+)$ and taking max(matched
        ints) + 1 -- 1 if no marker matches. Ignores any marker with a
        non-matching name (e.g. a user's own "EP10-ID03") and any
        marker missing/with an empty "name" key. Re-derived from LIVE
        timeline state on every call (no local counter) so numbering is
        correct and gap-free across app restarts and survives markers
        added/deleted outside this app.

        Returns DEFAULT_MARKER_NAME (never an empty string) if `markers`
        is None (GetMarkers() itself failed/returned nothing scannable)
        or if the scan raises for any reason -- an empty dict `{}`
        (genuinely zero markers on the timeline) is NOT treated as this
        failure case and correctly yields "M01".
        """
        if markers is None:
            return DEFAULT_MARKER_NAME
        try:
            nums = []
            for marker in markers.values():
                marker_name = (marker.get("name") or "") if isinstance(marker, dict) else ""
                match = cls._AUTO_MARKER_NAME_RE.match(marker_name)
                if match:
                    nums.append(int(match.group(1)))
            next_n = (max(nums) + 1) if nums else 1
            return "M%02d" % next_n
        except Exception:
            return DEFAULT_MARKER_NAME

    def _current_timeline(self):
        """Resolve the live Timeline object only -- no timecode/fps
        parsing, unlike _read_timeline_tc_frames(). resize_marker/
        set_marker_color operate on a marker at an EXPLICIT frame the
        caller already knows (the C1-created marker's own start
        position, tracked engine-side in Go, not derived from the
        current playhead), so they have no need for
        _read_timeline_tc_frames()'s GetCurrentTimecode()/drop-frame/
        non-integer-fps refusal logic -- that logic exists purely to
        convert the CURRENT playhead position into a frame count, which
        these two ops never do. Returns None (no exception) on any
        failure, same convention as _find_item_by_unique_id's preamble.
        """
        try:
            proj = self.resolve.GetProjectManager().GetCurrentProject()
            return proj.GetCurrentTimeline() if proj else None
        except Exception:
            return None

    @staticmethod
    def _find_marker(markers, frame):
        """Look up a marker dict by frame in a GetMarkers() result.

        *** LIVE-VERIFICATION FLAG ***: tries the int key first
        (Resolve's documented GetMarkers() key type -- frameId), then a
        string-key fallback in case the live API marshals differently.
        Neither path has been exercised against a real Resolve timeline
        in this hermetic suite (same hard hermeticity constraint as
        add_marker's frameId-math flag above) -- verify against a real
        GetMarkers() result before trusting the int-key assumption in
        production.

        Returns the marker dict, or None if `markers` is None/empty or
        has no entry at `frame`.
        """
        if not markers:
            return None
        m = markers.get(frame)
        if m is not None:
            return m
        return markers.get(str(frame))



    def _fps(self):
        """Timeline frame rate as a float, or None. Cosmetic read -- never
        nulls self.resolve."""
        try:
            tl = self._current_timeline()
            if tl is None:
                return None
            raw = tl.GetSetting("timelineFrameRate")
            return float(raw) if raw else None
        except Exception:
            return None

    def active_clip_range(self):
        """SOURCE in/out of the clip the edit knobs target, plus its length and
        the handles available either side.

        SOURCE, not timeline: the question this answers is "how much can I
        extend this clip", and a timeline position cannot answer it. The source
        in/out say where in the original media the clip is cut from, and the
        handles say how many frames of media remain beyond each edge.

        head handle = source_in (frames available before the in point)
        tail handle = media_total - source_out (frames available after the out)

        GetSourceStartFrame/GetSourceEndFrame are preferred; GetLeftOffset is
        the fallback for older API surfaces. Cosmetic throughout -- any refusal
        returns Nones, never raises, and never nulls self.resolve.

        Returns (source_in, source_out, duration, name, head, tail).
        """
        try:
            self._ensure_connected()
            item, _ = self._target_item(track=None)
            if item is None:
                return None, None, None, None, None, None

            src_in = src_out = None
            try:
                src_in = int(item.GetSourceStartFrame())
                src_out = int(item.GetSourceEndFrame())
            except Exception:
                try:
                    left = int(item.GetLeftOffset())
                    dur = int(item.GetEnd()) - int(item.GetStart())
                    src_in, src_out = left, left + dur
                except Exception:
                    return None, None, None, item.GetName(), None, None

            duration = max(0, src_out - src_in)

            # Media total, for the tail handle. Optional -- a missing media
            # pool item simply means no handle figures, not an error.
            head = src_in
            tail = None
            try:
                mp = item.GetMediaPoolItem()
                if mp is not None:
                    frames = mp.GetClipProperty("Frames")
                    if frames:
                        tail = max(0, int(frames) - src_out)
                    else:
                        # "Frames" is absent on some media (notably rendered
                        # .mov exports). "End" is the last source frame index
                        # and answers the same question one off.
                        end = mp.GetClipProperty("End")
                        if end:
                            tail = max(0, (int(end) + 1) - src_out)
            except Exception:
                tail = None

            return src_in, src_out, duration, item.GetName(), head, tail
        except Exception:
            return None, None, None, None, None, None

    def timeline_duration_frames(self):
        """Total timeline length in frames, for the HUD's second timecode.

        GetEndFrame() is exclusive, so the length is End - Start. Returns
        (frames, fps) or (None, None) on any refusal -- a cosmetic read, so it
        NEVER nulls self.resolve (2026-07-08 connection-poison rule).
        """
        try:
            self._ensure_connected()
            tl = self._current_timeline()
            if tl is None:
                return None, None
            start = tl.GetStartFrame()
            end = tl.GetEndFrame()
            fps = self._fps() if hasattr(self, "_fps") else None
            if not isinstance(start, int) or not isinstance(end, int):
                return None, None
            return max(0, end - start), fps
        except Exception:
            return None, None


    # The clip walk below is EXPENSIVE -- GetItemListInTrack per video track
    # plus GetName() per clip, against an API slow enough that running it on
    # the 250ms status tick blew the poll budget and made the app report the
    # sidecar as disconnected. It is cached and refreshed at most this often.
    CLIP_MAP_TTL_S = 3.0

    def timeline_clip_map(self, limit=512):
        """Per-clip render state for the HUD's LED strip, plus marker count.

        CACHED for CLIP_MAP_TTL_S -- see the constant above. The timeline's
        shape changes on human timescales, so a few seconds of staleness costs
        nothing, whereas walking it 4x a second costs the status poll.

        Returns (total, rendered, markers, leds) where leds is an encoded
        mini-timeline: one ROW PER VIDEO TRACK, highest track first so it reads
        top-down like the edit page, and within each row one bar per clip
        placed at its real position. That placement is the point -- a packed
        row of dots says how many clips there are, but a positioned row says
        where the gaps and the long clips are, which is the thing you actually
        look at a timeline to see.

        Encoding: rows joined by "|", clips within a row by ",", each clip
        "start:width:state" in per-mille of timeline duration, where state is
        0 = plain, 1 = the name contains "render", 2 = OFFLINE. Offline wins
        over rendered: a missing file is the thing you need to know first.
        Compact enough to ride the status poll. sidecar.py prepends the live
        playhead position as "<per-mille>;" -- fresh every tick, because the
        cache below would otherwise freeze the one value that always moves.

        Capped at `limit` bars total -- total still reports the true count, so
        a truncated strip is visible as such rather than silently
        under-reporting.

        Cosmetic: any refusal yields zeros/empties, never raises, and never
        nulls self.resolve (2026-07-08 connection-poison rule).
        """
        now = time.monotonic()
        cached = getattr(self, "_clip_map_cache", None)
        if cached is not None and (now - cached[0]) < self.CLIP_MAP_TTL_S:
            return cached[1]

        try:
            self._ensure_connected()
            tl = self._current_timeline()
            if tl is None:
                self._clip_map_cache = (now, (0, 0, 0, ""))
                return 0, 0, 0, ""

            markers = 0
            try:
                markers = len(tl.GetMarkers() or {})
            except Exception:
                markers = 0

            # Timeline extent, so every bar can be placed as a fraction of it.
            try:
                tl_start = int(tl.GetStartFrame())
                span = max(1, int(tl.GetEndFrame()) - tl_start)
            except Exception:
                tl_start, span = 0, 1
            # Published for the status tick, which needs the same frame of
            # reference to place the playhead but must not pay for the walk.
            self._tl_extent = (tl_start, span)

            tracks = []
            try:
                for tr in range(1, int(tl.GetTrackCount("video")) + 1):
                    tracks.append(tl.GetItemListInTrack("video", tr) or [])
            except Exception:
                tracks = []

            total = 0
            rendered = 0
            drawn = 0
            rows = []
            # Highest track first: V3 above V2 above V1, matching the edit page.
            for items in reversed(tracks):
                try:
                    items = sorted(items, key=lambda i: int(i.GetStart()))
                except Exception:
                    pass
                bars = []
                for it in items:
                    total += 1
                    try:
                        nm = (it.GetName() or "").lower()
                    except Exception:
                        nm = ""
                    is_r = "render" in nm
                    if is_r:
                        rendered += 1
                    if drawn >= limit:
                        continue
                    state = 1 if is_r else 0
                    if self._item_offline(it):
                        state = 2
                    try:
                        st = int(it.GetStart()) - tl_start
                        wd = max(1, int(it.GetEnd()) - int(it.GetStart()))
                    except Exception:
                        continue
                    bars.append("%d:%d:%d" % (
                        max(0, st * 1000 // span),
                        max(1, wd * 1000 // span),
                        state,
                    ))
                    drawn += 1
                rows.append(",".join(bars))

            # Trailing empty video tracks are noise, not information.
            while rows and rows[0] == "":
                rows.pop(0)
            out = (total, rendered, markers, "|".join(rows))
            self._clip_map_cache = (now, out)
            return out
        except Exception:
            self._clip_map_cache = (now, (0, 0, 0, ""))
            return 0, 0, 0, ""

    def color_by_render_state(self, rendered="Green", neutral="Yellow"):
        """Paint EVERY video clip in the timeline by whether it is a render.

        The HUD strip already says this, but only for as long as you are
        looking at the HUD. Writing it into the clip colours puts the same
        answer on the timeline itself, where the edit happens.

        Rendered ("render" in the name) -> `rendered`, everything else ->
        `neutral`. OFFLINE clips are SKIPPED, not coloured: Resolve already
        flags missing media in the timeline, and overwriting that with a flat
        yellow would hide the more urgent fact. "Offline" here means a named
        file that is not on disk and nothing else -- see _item_offline, which
        used to over-report and left generators stuck on their old colour.

        This MUTATES the project -- the only method here that does -- so it is
        deliberately not on any poll path. It runs once, when something asks
        it to, and Resolve's own undo covers it.

        SKIPS any clip already the right colour. Reads are ~free (0.08ms/clip
        measured across 427 clips) while SetClipColor costs ~32ms, so checking
        first turns a 12-second repaint into a fraction of a second whenever
        most of the timeline is already correct -- which is every press after
        the first.

        Returns (ok, err, painted, skipped).
        """
        try:
            self._ensure_connected()
            tl = self._current_timeline()
            if tl is None:
                return False, "no_timeline", 0, 0

            painted = skipped = 0
            for tr in range(1, int(tl.GetTrackCount("video")) + 1):
                for it in (tl.GetItemListInTrack("video", tr) or []):
                    try:
                        if self._item_offline(it):
                            skipped += 1
                            continue
                        nm = (it.GetName() or "").lower()
                        want = rendered if "render" in nm else neutral
                        if it.GetClipColor() == want:
                            skipped += 1
                            continue
                        it.SetClipColor(want)
                        painted += 1
                    except Exception:
                        skipped += 1
            # The strip is cached; force it to redraw against the new state.
            self._clip_map_cache = None
            return True, None, painted, skipped
        except Exception as e:
            return False, repr(e), 0, 0

    _OFFLINE_TTL_S = 30.0

    def _item_offline(self, item):
        """True when a timeline item's media is not actually there.

        POSITIVE EVIDENCE ONLY: a file path that is named and is not on disk.
        Anything less answers False.

        The first cut also treated "no media pool item" as offline, on the
        theory that an orphan has no media. It painted Fusion compositions,
        generators and titles red and then skipped them in the repaint, so
        they kept whatever colour they already had -- red in the HUD, stale in
        the timeline, and online the whole time. Absence of a media pool item
        says the clip is not FILE-BACKED, which is not the same claim.

        The disk check is cached per path for _OFFLINE_TTL_S -- the clip map is
        walked every few seconds and a timeline can hold hundreds of clips, so
        an uncached stat storm would cost more than the answer is worth.

        Cosmetic: any refusal answers False (assume online) rather than
        raising, so a probe failure never paints the whole timeline red.
        """
        try:
            mp = item.GetMediaPoolItem()
            if mp is None:
                return False
            path = mp.GetClipProperty("File Path")
            if not path:
                return False
            cache = getattr(self, "_offline_cache", None)
            if cache is None:
                cache = self._offline_cache = {}
            now = time.monotonic()
            hit = cache.get(path)
            if hit is not None and (now - hit[0]) < self._OFFLINE_TTL_S:
                return hit[1]
            missing = not os.path.exists(path)
            cache[path] = (now, missing)
            return missing
        except Exception:
            return False

    def timeline_extent(self):
        """(start_frame, span) as last seen by timeline_clip_map, so the status
        tick can place the playhead on the same scale the strip was drawn on
        without re-walking the timeline."""
        return getattr(self, "_tl_extent", (0, 1))

    def marker_at_playhead(self):
        """Return the marker whose [start, start+duration) contains the
        playhead, so C1 can ADOPT it for resize/recolour instead of creating
        an overlapping duplicate.

        Without this, C1 pressed while parked inside an existing marker either
        fails with marker_exists (on its exact start frame) or silently
        creates a second, overlapping marker.

        Frame math matches add_marker exactly: GetMarkers() keys are relative
        to the timeline's OWN start frame, so the playhead's absolute frame is
        converted via `cur_frames - tl.GetStartFrame()` before comparing.

        Overlapping markers: the latest start at or before the playhead wins,
        which is the one whose "region" the user most recently entered.

        A read failure is a clean miss ({"found": False}), never an error and
        never a reason to null self.resolve -- see the 2026-07-08
        connection-poison fix: a cosmetic read must not poison the shared
        handle for every other op.

        Returns {"ok": True, "found": bool} plus, when found,
        {"frame": int, "color": str, "duration": float (seconds), "fps": float}.
        """
        self._ensure_connected()
        tl, cur_frames, fps, err = self._read_timeline_tc_frames()
        if err or tl is None or not isinstance(cur_frames, int) or not fps:
            return {"ok": True, "found": False}

        frame_id = cur_frames - tl.GetStartFrame()
        markers = tl.GetMarkers() or {}

        best = None
        for start, meta in markers.items():
            try:
                dur = int(meta.get("duration", 1) or 1)
            except (TypeError, ValueError):
                dur = 1
            if start <= frame_id < start + dur:
                if best is None or start > best[0]:
                    best = (start, meta, dur)

        if best is None:
            return {"ok": True, "found": False}

        start, meta, dur = best
        tl_start = tl.GetStartFrame()
        return {
            "ok": True,
            "found": True,
            "frame": start,
            "color": meta.get("color") or "Blue",
            "duration": float(dur) / float(fps),
            "fps": float(fps),
            # ABSOLUTE range, for the engine's auto-exit check. GetMarkers
            # keys are relative to the timeline's own start frame, while
            # current_playhead_frame() is absolute -- comparing the two
            # directly would be off by GetStartFrame() (90000 on a timeline
            # starting at 01:00:00:00) and auto-exit would fire constantly.
            "abs_start": start + tl_start,
            "abs_end": start + tl_start + dur,
        }

    def resize_marker(self, frame, delta_frames):
        """C1 marker-mode ControlDial resize: extend (positive
        delta_frames, dial turned clockwise) or shrink (negative,
        counter-clockwise) the duration of the marker AT `frame` --
        see engine.go's dispatchMarkerResize. The marker's frame (start
        position) is the identity that never changes across a resize;
        only its duration does.

        Resolve's Timeline has no marker-UPDATE API (see module
        docstring / user-confirmed API surface: AddMarker/GetMarkers/
        DeleteMarkerAtFrame only) -- so this is implemented as
        GetMarkers() -> look up the marker at `frame` ->
        DeleteMarkerAtFrame(frame) -> AddMarker(frame, ...) with the
        SAME color/name/note/customData and a new duration, clamped to
        a minimum of 1 frame (a marker can never shrink to zero or
        negative duration). name falls back to DEFAULT_MARKER_NAME if
        the marker being resized was itself read back with an empty
        name -- Resolve's AddMarker rejects an empty-string name (see
        DEFAULT_MARKER_NAME's module-level comment); re-adding it with
        "" would silently turn a legitimate resize into an
        "add_marker_failed" error.

        Returns (ok: bool, err: str|None, duration: int|None,
        fps: float|None). duration is the NEW duration in frames
        (matching GetMarkers()'s own "duration" field, which is
        frame-based) -- fps rides along (read via the same
        tl.GetSetting("timelineFrameRate") call add_marker/jog already
        use) purely so the Go engine can convert to a seconds-based
        Status.MarkerMode display without a second round trip; it is
        NOT used for any duration math here (that's all whole frames).
        duration/fps are None on any failure -- err is one of
        "no_timeline" (from _current_timeline), "marker_not_found" (no
        entry in GetMarkers() at `frame`), "delete_failed"
        (DeleteMarkerAtFrame returned False), "add_marker_failed"
        (AddMarker returned False, e.g. a race re-adding at an
        already-occupied frame), or an exception repr.
        """
        tl = self._current_timeline()
        if tl is None:
            return False, "no_timeline", None, None
        try:
            markers = tl.GetMarkers()
            marker = self._find_marker(markers, frame)
            if marker is None:
                return False, "marker_not_found", None, None
            new_duration = max(1, int(marker.get("duration", 1)) + int(delta_frames))
            if not tl.DeleteMarkerAtFrame(frame):
                return False, "delete_failed", None, None
            ok = tl.AddMarker(
                frame,
                marker.get("color", "Blue"),
                marker.get("name", "") or DEFAULT_MARKER_NAME,
                marker.get("note", "") or "",
                new_duration,
                marker.get("customData", "") or "",
            )
            if not ok:
                return False, "add_marker_failed", None, None
            try:
                fps = float(tl.GetSetting("timelineFrameRate"))
            except Exception:
                fps = None
            return True, None, new_duration, fps
        except Exception as e:
            return False, repr(e), None, None

    def set_marker_color(self, frame, color):
        """P1-8btn marker-mode recolor: set the color of the marker AT
        `frame` to `color`, preserving duration/name/note/customData --
        see engine.go's dispatchSetMarkerColor. Same delete+re-add shape
        as resize_marker (Resolve has no marker-update API), changing
        only the color argument passed to AddMarker -- including the
        same DEFAULT_MARKER_NAME empty-name fallback as resize_marker,
        for the same reason (AddMarker rejects "" as a name).

        Returns (ok: bool, err: str|None) -- same error vocabulary as
        resize_marker ("no_timeline", "marker_not_found",
        "delete_failed", "add_marker_failed", or an exception repr).
        """
        tl = self._current_timeline()
        if tl is None:
            return False, "no_timeline"
        try:
            markers = tl.GetMarkers()
            marker = self._find_marker(markers, frame)
            if marker is None:
                return False, "marker_not_found"
            if not tl.DeleteMarkerAtFrame(frame):
                return False, "delete_failed"
            ok = tl.AddMarker(
                frame,
                color,
                marker.get("name", "") or DEFAULT_MARKER_NAME,
                marker.get("note", "") or "",
                marker.get("duration", 1),
                marker.get("customData", "") or "",
            )
            if not ok:
                return False, "add_marker_failed"
            return True, None
        except Exception as e:
            return False, repr(e)

    def delete_marker(self, frame):
        """P-button same-colour-press toggle (2026-08-12): remove the
        marker AT `frame` entirely via Timeline.DeleteMarkerAtFrame -- see
        engine.go's dispatchMarkerAtPlayhead, the third leg of the
        P-button three-way (no marker -> add; marker, different colour ->
        recolour; marker, SAME colour as the pressed button -> delete).

        Unlike resize_marker/set_marker_color (which delete+re-add to fake
        an "update", since Resolve's Timeline has no marker-update API),
        this is a genuine delete -- no re-add. Uses the same
        `_current_timeline()` preamble and `_find_marker()` lookup as
        resize_marker/set_marker_color for the identical reason: frame is
        an EXPLICIT frame the Go caller already knows (the marker just
        found by marker_at_playhead), not derived from the current
        playhead, so no GetCurrentTimecode()/drop-frame refusal logic
        applies here.

        Returns (ok: bool, err: str|None) -- same error vocabulary as
        resize_marker/set_marker_color for the shared preamble
        ("no_timeline", "marker_not_found"), plus "delete_failed"
        (DeleteMarkerAtFrame returned False, e.g. the user deleted the
        same marker directly in Resolve a moment earlier) when the marker
        was found but the delete call itself still failed.
        """
        tl = self._current_timeline()
        if tl is None:
            return False, "no_timeline"
        try:
            markers = tl.GetMarkers()
            marker = self._find_marker(markers, frame)
            if marker is None:
                return False, "marker_not_found"
            if not tl.DeleteMarkerAtFrame(frame):
                return False, "delete_failed"
            return True, None
        except Exception as e:
            return False, repr(e)

    def render(self, target_dir):
        """Queue and start a render of the current timeline into
        target_dir using the "H.264 Master" render preset.

        Uses LoadRenderPreset (NOT a from-scratch SetRenderSettings
        format/codec dict) so the user's own Deliver-page format/codec
        settings are never clobbered -- SetRenderSettings is still used
        afterward, but ONLY for TargetDir + CustomName, which is the
        normal/minimal way to point an already-loaded preset at an
        output location. CustomName auto-increments (checks target_dir
        for an existing "<name>.mov") so repeated Quick Export presses
        never silently overwrite a prior render.
        """
        try:
            proj = self.resolve.GetProjectManager().GetCurrentProject()
            if proj is None:
                return False, "no_project", None
            tl = proj.GetCurrentTimeline()
            if tl is None:
                return False, "no_timeline", None
            if not proj.LoadRenderPreset("H.264 Master"):
                return False, "preset_load_failed", None
            base_name = tl.GetName() or "render"
            custom_name = self._auto_increment_name(target_dir, base_name)
            if not proj.SetRenderSettings({"TargetDir": target_dir, "CustomName": custom_name}):
                return False, "set_render_settings_failed", None
            job_id = proj.AddRenderJob()
            if not job_id:
                return False, "add_render_job_failed", None
            ok = proj.StartRendering([job_id])
            return bool(ok), None, {
                "jobId": job_id,
                "customName": custom_name,
                "targetDir": target_dir,
            }
        except Exception as e:
            return False, repr(e), None

    @staticmethod
    def _auto_increment_name(target_dir, base_name):
        name = base_name
        n = 1
        while os.path.exists(os.path.join(target_dir, name + ".mov")):
            n += 1
            name = "%s_%d" % (base_name, n)
        return name


if __name__ == "__main__":
    from cdl_state import NodeCDL
    d = ResolveDriver()
    assert d.connect(), "could not connect (Resolve open? External scripting = Local?)"
    n = NodeCDL(); n.apply("offset", "b", 0.15)  # visible blue lift
    print("write:", d.write(n.to_setcdl(1)))
