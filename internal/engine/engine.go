// Package engine wraps the standalone 2t5-deck-plus orchestration loop (formerly
// the body of cmd/2t5-deck-plus/main.go) into a startable/stoppable lifecycle:
// New / Start / Stop / Status. The grading logic itself — MIDI decode,
// coalescing, gating, calibration lookup, CDL routing, RGB-scrubber balance
// dispatch — is moved verbatim; only the orchestration shell changes so it can
// be embedded in a long-lived host process (a future Wails app) instead of
// being the entire lifetime of a CLI process.
package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/drivers"
	_ "gitlab.com/gomidi/midi/v2/drivers/rtmididrv"

	"2t5-deck-plus/internal/accel"
	"2t5-deck-plus/internal/appdeck"
	"2t5-deck-plus/internal/calib"
	"2t5-deck-plus/internal/coalesce"
	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/datadir"
	"2t5-deck-plus/internal/dragmath"
	"2t5-deck-plus/internal/gate"
	"2t5-deck-plus/internal/ipc"
	"2t5-deck-plus/internal/keys"
	"2t5-deck-plus/internal/layer"
	"2t5-deck-plus/internal/macapp"
	mididec "2t5-deck-plus/internal/midi"
	"2t5-deck-plus/internal/mode"
	"2t5-deck-plus/internal/mouse"
	"2t5-deck-plus/internal/preset"
	"2t5-deck-plus/internal/rgbmix"
	"2t5-deck-plus/internal/router"
	"2t5-deck-plus/internal/sidecar"
	"2t5-deck-plus/internal/target"
	"2t5-deck-plus/internal/tc"
)

// Status is a point-in-time snapshot of the engine's running state, suitable
// for display in a UI (Phase 1 Status tab) or polling from a CLI.
type Status struct {
	Running           bool
	SidecarConnected  bool
	ResolveFrontmost  bool
	Page              string
	GateAllow         bool
	ActiveWheel       string
	Mode              string
	Project           string
	LastMIDI          string
	AccessibilityOK   bool
	ScreenRecordingOK bool
	// SidecarError is a human-readable message describing why the sidecar
	// is not connected (python3-not-found, import failure, dial failure,
	// etc). Empty when SidecarConnected is true.
	SidecarError string
	// SidecarState is the lazy-sidecar lifecycle for the UI: "waiting"
	// (Resolve not running/frontmost; quiet and normal, SidecarError is
	// empty), "connecting" (first attempt in flight), "connected", or
	// "retrying" (an attempt failed or a dead sidecar is being recovered;
	// SidecarError says why). Empty while the engine is stopped.
	SidecarState string
	// SidecarStale is true when the fields below (Timecode, TimelineName,
	// TimelineDuration, ClipIn/Out/Duration, ClipTotal/Rendered,
	// MarkerCount, LEDs, EditClip*) are a CARRIED-FORWARD snapshot from the
	// last genuinely successful sidecar round trip, not a fresh read this
	// tick -- set when this tick's "status" call timed out but a dedicated
	// ping probe confirmed the sidecar process is still alive, just busy
	// (mid-playback is the normal case; see queryStatus's 2026-08-24 doc
	// comment). SidecarConnected stays true throughout -- this is
	// deliberately NOT a connectivity flag, it only says "these specific
	// numbers are a moment old." formatHUD (app/format.go) uses this to
	// show a subtle stale indicator instead of either freezing numbers
	// silently or blanking them, which is what the live bug looked like.
	SidecarStale bool
	// ActiveClip is the D1 knob's current selectClip target, formatted as
	// "V<track> · <clip name>" (e.g. "V4 · DSCF9804.MOV") -- the app's
	// visual feedback for which clip the property/reset dials affect,
	// since Resolve has no scripting API to highlight a clip on-screen.
	// Empty when not on the Edit page, or when D1 hasn't been turned yet
	// (activeTrack unset/"auto").
	ActiveClip string
	// MarkerMode is the app's visual feedback for the C1 "marker mode"
	// state (create+resize+color a marker via ControlDial/P1-8btn) --
	// e.g. "active @1.5s Blue" while a marker is targeted, or "" when
	// marker mode is inactive (no C1 press yet this session, or a
	// ControlDial press has since exited it). See engine.go's
	// markerModeStatus.
	MarkerMode string

	// --- HUD v2 fields (rich WKWebView readout) ---------------------------

	// Timecode is the live playhead position ("HH:MM:SS:FF"), read from the
	// sidecar's "status" op (Timeline.GetCurrentTimecode()). Empty when
	// there is no live timeline (no project open, or Resolve/sidecar
	// unreachable) -- the HUD renders dashes ("--:--:--:--") in that case,
	// never a stale value.
	Timecode string
	// TimelineName is the current timeline's name (Timeline.GetName()),
	// read alongside Timecode from the same "status" op round trip. Empty
	// under the same conditions as Timecode.
	TimelineName string
	// Bank is the current note_on/button dispatch bank ("col" or "edit"),
	// read from the engine's bankMu-guarded getBank() closure. "col" today
	// is always the drain loop's own currentBank for buttons (see
	// resolveNoteBank's doc comment) -- this field exists purely for the
	// HUD's diagnostic foot row, not for any dispatch decision.
	Bank string

	// Layer is the console's active layer (1-3). Replaces Bank as the HUD's
	// mode chip: Bank has always rendered the constant "col", whereas the
	// layer is the single most safety-critical thing on that surface --
	// misreading it mis-fires every knob at once.
	Layer int

	// HeldOverlay is Part B's held-modifier CHORD OVERLAY name (e.g. "cmd"),
	// or "" when nothing is held -- see resolveOverlayBinding. Deliberately
	// a SEPARATE field from Layer, never folded into it: the HUD must be
	// able to show "held a chord" and "which base layer" at the same time,
	// and a held chord must never be mis-rendered as "Layer 1" just because
	// the overlay has no dedicated data-layer accent of its own.
	HeldOverlay string

	// Target is the active reset target -- what Col will reset.
	Target string

	// TimelineDuration is the timeline's total length as HH:MM:SS:FF.
	TimelineDuration string

	// ClipIn/ClipOut/ClipDuration are the selected clip's timeline-relative
	// in point, out point and length, so the user can see how much room a
	// clip has before extending it.
	ClipIn       string
	ClipOut      string
	ClipDuration string

	// ClipTotal/ClipRendered/MarkerCount are the timeline census shown in the
	// status column; LEDs is one character per clip ("r" = the name contains
	// "render", "-" = it does not) driving the status strip.
	ClipTotal    int
	ClipRendered int
	MarkerCount  int
	LEDs         string

	// ClipHead/ClipTail are the source frames available before the in point
	// and after the out point -- the handles you have to trim into.
	ClipHead string
	ClipTail string

	// PMode is the P1-P8 slider/button mode ("hue", "sat" or "lum"),
	// mirroring the console's engraved Hue/Sat/Lum buttons. Shown as its own
	// HUD indicator because it changes what the whole P row does, and the
	// engraving alone cannot say which of the three is live.
	PMode string
	// CustomMode is the engine-tracked Custom Mode toggle (press CustomMode to flip it).
	// The deck's own LED is firmware and cannot be read back, so this is the engine's state, not the lamp's.
	CustomMode bool
	// FineMode reports whether the fineModeHold button (C3) is currently
	// held, read from the engine's fineModeAtomic flag (already
	// atomic.LoadInt32'd elsewhere in the drain loop for
	// dispatchBalanceStep).
	FineMode bool
	// Node is the current CDL node index (1-based), read from the
	// MIDI-callback goroutine's node counter -- promoted to atomic.Int32
	// specifically so this per-tick read from the drain/ticker goroutine is
	// race-free (see Start()'s nodeAtomic doc comment for the full history:
	// node was previously an unsynchronized plain int, safe only because
	// every prior reader/writer lived in the MIDI-callback goroutine; this
	// field is the first cross-goroutine reader).
	Node int
	// LastAction is a human-readable echo of the most recently DISPATCHED
	// action (e.g. "Gamma +3", "C1 → marker @1.0s Blue", "cut", "Reset
	// Pan") -- set via the engine's setLastAction (mirrors setLastMIDI's
	// shape/lock exactly). Like LastMIDI, this reports the action a
	// binding's handler attempted/performed at its own dispatch point, not
	// a confirmed downstream Resolve-side effect -- see setLastAction's
	// doc comment.
	LastAction string

	// --- HUD v2 Edit-page target-clip readout (2026-07-08) ------------

	// EditClipName is the Edit-page property/reset target clip's name --
	// the SAME clip _target_item resolves sidecar-side for bump_property/
	// reset_property (the direction-button-selected clip when a selection
	// is active, else the top-occupied clip at the playhead). Empty when
	// HaveEditClip is false (see below) -- the HUD renders "—" in that
	// case via app/format.go's hudEditClipName, never a stale name.
	EditClipName string
	// HaveEditClip gates whether EditClipName/EditClipPan/EditClipTilt/
	// EditClipZoom are populated THIS tick -- mirrors havePlayheadFrame's
	// "only act on a genuinely successful read" pattern in queryStatus
	// below: a status tick whose "edit_clip" came back null/absent
	// (no target clip, or any fake-sidecar test double that doesn't
	// include the field) leaves this false, so formatHUD never renders a
	// stale prior-tick value as if it were current.
	HaveEditClip bool
	// EditClipPan/EditClipTilt/EditClipZoom are the target clip's live
	// Pan/Tilt/ZoomX property values (read via GetProperty sidecar-side,
	// see ResolveDriver.edit_clip_info), populated into the HUD's Pos X/
	// Pos Y/Scale knob cells on the Edit page (app/format.go's hudKnobs).
	// Zero/meaningless when HaveEditClip is false.
	EditClipPan  float64
	EditClipTilt float64
	EditClipZoom float64
}

// Engine owns the loaded config/calibration, the IPC sidecar client, and the
// running MIDI-listen + drain-tick goroutine. It is safe to Start and Stop
// repeatedly; modeTracker and aw (active wheel) persist across restarts so a
// Stop/Start cycle in a host UI doesn't silently reset the user's selection.
type Engine struct {
	cfgDir string
	// cfg is the live binding configuration (hot reload). Provenance: ld-resolve
	// branch feature/mapping-editor-phase-a, commit 6437f3a (Engine.cfg ->
	// atomic.Pointer[config.Config], SetConfig, per-event Load). A *Config
	// published via SetConfig must never be mutated afterwards.
	cfg atomic.Pointer[config.Config]

	// hooks is the app-aware layer (appdeck, input observer); see appdeck_hook.go.
	hooks appHooks
	cal   *calib.Calib

	// sockPath is the sidecar IPC unix socket path. Defaults to
	// "/tmp/2t5-deck-plus.sock"; overridable (same-package only) for tests.
	sockPath string

	// resourcesDir is passed to sidecar.Launch as the base directory whose
	// "sidecar" subfolder holds sidecar.py. Defaults to "." (repo root) for
	// the CLI; a future Wails .app bundle will point this at
	// Contents/Resources. Overridable (same-package only) for tests.
	resourcesDir string

	// toggleHUD is invoked by the C6 button's "toggleHud" note_on case (see
	// dispatchNoteOn's IsToggleHud() branch) to flip the native HUD status
	// monitor's visibility. nil is a SAFE NO-OP — this is what keeps the
	// headless CLI (cmd/2t5-deck-plus/main.go) working: it shares this exact
	// Engine but has no NSApplication for an NSPanel to attach to, and never
	// calls SetToggleHUDCallback, so C6 is simply an inert button there.
	// Only app.NewApp wires a non-nil value (a.ToggleHUD), mirroring how
	// SetResourcesDir is set once, before Start(), with no mutex needed —
	// same single-assignment-then-read-only lifecycle.
	toggleHUD func() bool

	// clientPtr is the LATE-BOUND sidecar client. nil until the lazy
	// supervisor (superviseSidecar) connects; read lock-free from the MIDI
	// callback, the drain loop and the status poller via getClient().
	// The pointer's identity is stable once published (recoverSidecar
	// reconnects it in place), and only superviseSidecar ever stores a
	// non-nil value (Stop stores nil).
	clientPtr atomic.Pointer[ipc.Client]

	// highlightRestorePending: the connect-time restore_highlight was held back
	// because the deck was disarmed; it runs once the deck is armed (B7).
	highlightRestorePending atomic.Bool

	// holds are the pending hold-to-fire timers of Resolve key presses (hold.go).
	holds holdTable

	// supervisorWG joins superviseSidecar in Stop().
	supervisorWG sync.WaitGroup

	// sidecarStop tears down whatever sidecar.Launch started (or is a no-op
	// if a live sidecar was reused). Set by Start, called by Stop. Also
	// mutated by recoverSidecar (BUG 3, 2026-08-12) when a recovery attempt
	// itself spawns a new process -- see recoverSidecar's doc comment for
	// why that mutation COMPOSES rather than replaces, and why it (like
	// every read/write here after Start's initial assignment) goes through
	// mu, not the bare field.
	sidecarStop func()

	// sidecarRecovering/sidecarRecoveryMu/lastSidecarRecoveryAttempt/
	// sidecarRecoveryWG back recoverSidecar (BUG 3, 2026-08-12: the
	// sidecar process dying — e.g. a DaVinci Resolve restart taking it
	// down — and never coming back until the whole app is relaunched,
	// because nothing ever re-dialed once Start()'s initial dial had
	// succeeded). recoverSidecar runs on its own detached goroutine
	// (triggered from queryStatus's error branch, which fires every
	// ~200ms via statusCacheTTL) so a slow respawn never blocks the MIDI/
	// status drain loop; these four fields are what keep that goroutine
	// safe and singular:
	//   - sidecarRecovering: single-flight gate (CompareAndSwap) so the
	//     ~200ms status-cache TTL doesn't spawn a launchSidecarFn call
	//     per tick while one is already in flight.
	//   - sidecarRecoveryMu/lastSidecarRecoveryAttempt: a backoff gate
	//     (sidecarRecoveryBackoff) so a FAST-failing launch (e.g.
	//     DiscoverPython3 itself broken) doesn't hammer that probe every
	//     tick either — separate from sidecarRecovering because the
	//     single-flight gate alone only prevents CONCURRENT attempts, not
	//     back-to-back-the-instant-one-finishes ones.
	//   - sidecarRecoveryWG: lets Stop() wait for any in-flight recovery
	//     goroutine to fully finish BEFORE Stop() touches e.client/
	//     e.sidecarStop — without this, a recovery goroutine racing past
	//     Stop() could Reconnect() a *ipc.Client Stop() just Close()d
	//     (resurrecting a connection nothing will ever close again), or
	//     silently drop a freshly-spawned process's stop() on the floor
	//     (a leaked python3 process) if Stop()'s own sidecarStop=nil
	//     happened to interleave with recoverSidecar's composition.
	sidecarRecovering          atomic.Bool
	sidecarRecoveryMu          sync.Mutex
	lastSidecarRecoveryAttempt time.Time
	sidecarRecoveryWG          sync.WaitGroup

	// REMOVED 2026-08-25, THIRD attempt at this bug class (live production
	// bug -- HUD numbers vanishing and the sidecar getting killed/respawned
	// mid-PLAYBACK). History, so a fourth attempt doesn't retry either dead
	// end:
	//   1. (2026-08-13) this field used to be consecutiveStatusTimeouts, an
	//      atomic.Int32 counting back-to-back ipc.ErrTimeout results before
	//      recoverSidecar was allowed to fire, gated by a package var
	//      maxConsecutiveStatusTimeouts. A COUNT of timeouts can never tell
	//      "busy" apart from "wedged" -- both produce identical
	//      ipc.ErrTimeout results -- so any fixed threshold is just a bet on
	//      how long a busy period can last, and playback has no upper
	//      bound. Removed, not retuned.
	//   2. (2026-08-24) replaced by a ping-based liveness discriminator
	//      (internal/ipc.PingAlive against sidecar.py's dedicated PING_SOCK,
	//      served by its own threading.Thread) -- probed only after a
	//      "status" timeout, on the theory that a second socket served by a
	//      second thread is architecturally independent of whatever the
	//      main connection's slow call is doing. Live evidence the very
	//      next day (2026-08-25) proved that wrong: CPython's GIL is
	//      PROCESS-wide, not per-socket or per-thread. sidecar.py's
	//      "status" handler blocks inside fusionscript.so (DaVinci
	//      Resolve's C extension) during playback, and that call does not
	//      release the GIL -- so NO other Python thread in that process can
	//      run, including the ping listener. The probe therefore timed out
	//      at exactly the moments the thing it was meant to discriminate
	//      did, reporting "wedged or dead" for a perfectly healthy, merely
	//      busy sidecar on every single playback (8 recoveries in one log
	//      excerpt). A same-process thread can never serve as a liveness
	//      probe against a call that holds the GIL. Not tunable, not a bug
	//      in the probe's timeout value -- the whole approach was
	//      structurally incapable of answering the question it was asked.
	//      PING_SOCK/PingAlive were removed outright (not repurposed --
	//      see internal/ipc's package doc, ping.go/ping_test.go/
	//      sidecar.py's PING_SOCK deletion, all 2026-08-25): they had no
	//      reachable case where they were both callable and correct. Every
	//      call site was already gated behind "a status round trip just
	//      returned ipc.ErrTimeout" (client.go's contract: read timeout
	//      only, connection kept alive) -- precisely the GIL-blocked
	//      window. A genuinely dead process instead surfaces as a HARD
	//      transport error (EOF/broken pipe/connection refused) on the
	//      MAIN socket, which was already being routed straight to
	//      recoverSidecar without ever consulting the ping result. Leaving
	//      the machinery in place -- even relabelled "advisory" or
	//      "logging only" -- would still emit a false "ping probe also
	//      failed" verdict into the logs during ordinary healthy playback,
	//      for any future engineer to trust.
	//   3. (2026-08-25, current) the rule changed instead of the mechanism:
	//      a TIMEOUT MUST NEVER TRIGGER RECOVERY, full stop, regardless of
	//      count or duration -- queryStatus below serves the last-known
	//      snapshot marked stale and keeps polling. Only a HARD transport
	//      error (errors.Is(err, ipc.ErrTimeout) == false) triggers
	//      recoverSidecar. A dead sidecar always produces one of those
	//      eventually (the OS closes the socket), never a bare timeout
	//      forever, so nothing real is lost. Deliberate, accepted
	//      trade-off: a sidecar that is alive but PERMANENTLY wedged (stuck
	//      forever on something that is NOT a Resolve call holding the
	//      GIL -- e.g. a genuine Python-level deadlock) will now never
	//      self-recover; it serves stale values indefinitely until the app
	//      is relaunched. That is strictly better than killing a healthy
	//      sidecar every time the user presses play, and it is recoverable
	//      by hand. Do not "fix" the permanently-wedged case with a long
	//      timeout escalation -- that reintroduces attempt #1's exact wrong
	//      assumption (a count/duration can discriminate busy from wedged)
	//      with a bigger number. No counter, no threshold, no discriminator,
	//      no state to keep here.

	modeTracker *mode.Tracker
	aw          *target.State

	cancel context.CancelFunc
	done   chan struct{}

	mu     sync.Mutex
	status Status

	// lastFolderMu guards lastFolder — the C5 Quick-Export button's
	// remembered target directory. Separate from mu (Status' lock) since
	// it's touched on a different, much rarer path (a user pressing C5)
	// with no reason to couple the two locks. Seeded from stateFilePath in
	// New() so the remembered folder survives a process restart; persisted
	// immediately on every setLastFolder call (see saveSentinel).
	lastFolderMu sync.Mutex
	lastFolder   string
	savedPMode   string
	customMode   atomic.Bool // Custom Mode toggle, engine state (not persisted: starts off)
	ledSink      atomic.Pointer[LEDSink]
	stateHook    atomic.Pointer[StateSink] // deck-state listener for the editor (separate from the LED output)
	layerNow     atomic.Int32              // the layer last reported to stateHook (0 = none yet)
	pmodeRef     *target.State

	// activeTrackMu guards activeTrack — the D1 knob's "selectClip"
	// rotation state: which video track's clip the Edit-page property/
	// reset dials currently target. 0 means unset/"auto" (use Resolve's
	// own GetCurrentVideoItem() rather than a track pinned by D1).
	// Persists across Start/Stop, like aw (target.State) above — a
	// user's D1 selection should survive an engine restart within the
	// same process, not silently reset to "auto". Not persisted to disk
	// (unlike lastFolder): a fresh process always starts at "auto".
	activeTrackMu sync.Mutex
	activeTrack   int

	// markerMu guards activeMarkerFrame/activeMarkerColor/
	// activeMarkerDurationSeconds -- the C1 "marker mode" state: which
	// marker (identified by its frame/start position -- Resolve's
	// GetMarkers()/AddMarker have no separate marker ID, frame IS the
	// key) the ControlDial (resize) and P1-8btn (recolor) controls
	// currently target. Entered by a C1 press (dispatchAddMarker calls
	// setActiveMarker with the sidecar's returned frame), exited by a
	// ControlDial press (dispatchControlDialPress calls
	// clearActiveMarker). -1 means marker mode is INACTIVE -- 0 is a
	// legitimate frame (a marker at the very start of the timeline), so
	// it cannot double as the inactive sentinel the way activeTrack's
	// 0-means-auto convention above does; New() explicitly seeds -1
	// rather than relying on the int zero value. Not persisted to disk
	// (like activeTrack, unlike lastFolder) and NOT reset across a
	// Stop/Start within the same process (same persistence rationale as
	// activeTrack above) -- though a marker frame surviving a PROJECT
	// SWITCH during the Stop window is a live-verification edge case: a
	// stale resize/recolor would simply fail gracefully via the
	// sidecar's ok=false "marker_not_found" path against the new
	// project, not corrupt anything.
	markerMu                    sync.Mutex
	activeMarkerFrame           int
	activeMarkerColor           string
	activeMarkerDurationSeconds float64

	// activeMarkerAbsStart/activeMarkerAbsEnd are the active marker's range in
	// ABSOLUTE frames -- the same counting convention as
	// current_playhead_frame(), and deliberately NOT the relative frameId that
	// GetMarkers() keys use. Comparing a relative marker key against an
	// absolute playhead would be off by Timeline.GetStartFrame() (90000 on a
	// timeline starting at 01:00:00:00) and auto-exit would fire constantly.
	// Both zero means "range unknown" -- auto-exit then never fires.
	activeMarkerAbsStart int
	activeMarkerAbsEnd   int

	// selectionMu guards selection -- the Up/Down/Left/Right/C5
	// direction-button clip-selection state (replaces D1's knob-turn
	// selectClip rotation). Distinct from, but kept in LOCKSTEP with,
	// activeTrack/getActiveTrack()/setActiveTrack() above: bump_property/
	// reset_property's existing "track" override (the "must not regress
	// the now-working edit knobs" build constraint for this feature)
	// reads ONLY activeTrack, never `selection` directly, so every
	// dispatch function that sets/clears `selection` also calls
	// setActiveTrack with the matching track number (or 0). `selection`
	// carries EXTRA bookkeeping activeTrack alone never needed (D1's
	// activeTrack was purely a track number) -- the clip's frame range,
	// required by this feature's playhead-left-the-clip auto-reset (see
	// the drain loop's [start,end) comparison against
	// Status-tick-derived playhead_frame). Persists across Start/Stop
	// within the same process, same posture as activeTrack itself. A
	// fresh process always starts with selection.active==false.
	selectionMu sync.Mutex
	selection   clipSelection

	// noteDebounceMu guards noteDebounceLast -- the note_on duplicate-
	// dispatch debounce (BUG 1, 2026-08-12 live report). See
	// isDuplicateNoteOn's doc comment.
	noteDebounceMu   sync.Mutex
	noteDebounceLast map[string]time.Time

	// heldOverlayMu guards heldOverlayName -- Part B's held-modifier CHORD
	// OVERLAY (2026-08-12 REV 2 design review), replacing the reverted
	// type:"modifier" design (see engine.go's git history). Written by the
	// MIDI callback goroutine (a modifierHold binding's press/release);
	// read by that SAME goroutine (resolveOverlayBinding, on every
	// subsequent note_on) AND by the drain/status-tick goroutine
	// (Status().HeldOverlay, for the HUD) -- explicit synchronisation
	// required, the same "new shared engine state written by one goroutine
	// and read by another needs its own mutex" lesson this codebase has
	// hit repeatedly. A SEPARATE piece of state from layerState
	// (internal/layer) -- never a layer number, see resolveOverlayBinding's
	// doc comment for why conflating the two was rejected. The zero value
	// (empty string) is "nothing held," always safe, and is exactly what a
	// fresh Engine starts with -- there is no lazy-allocation concern the
	// old heldModifierSet map needed, since this is a single string, not a
	// set. NEVER written to stateFilePath/sentinelState -- see
	// TestSentinelStateNeverPersistsHeldOverlay -- the deck must never boot
	// back up believing a chord is held.
	heldOverlayMu   sync.Mutex
	heldOverlayName string
}

// clipSelection is the Up/Down/Left/Right direction-button clip-selection
// state (see Engine.selection's doc comment above). The zero value
// (active==false) means nothing is currently selected via these buttons.
// unique_id/original_color are intentionally NOT mirrored here -- the
// sidecar owns that bookkeeping entirely (self._highlight, shared with
// D1's highlight/clear_highlight/restore_highlight machinery); Go's copy
// is purely a change-detector for the [start,end) auto-reset range check.
//
// confirmed/setAt implement BUG 3's fix (2026-08-12 live report): a
// horizontal select moves the playhead via the sidecar's SetCurrentTimecode,
// but the drain loop's status tick can race ahead and read the OLD playhead
// value before Resolve commits that write -- observed live 17ms after a
// Right press, wrongly clearing a selection that was never actually wrong.
// confirmed is false the instant a selection whose commanded playhead has
// NOT yet been observed is set (dispatchSelectClipHorizontal only -- a
// vertical select never moves the playhead, so it starts already
// confirmed=true; there is no race to protect against), and flips to true
// the first time a status tick's playhead_frame falls inside [start,end).
// The auto-reset only clears immediately once confirmed; before that it is
// suppressed until setAt+graceWindow elapses, so a genuinely stuck/failed
// write still clears eventually rather than pinning the selection forever.
type clipSelection struct {
	active    bool
	track     int
	start     int
	end       int
	confirmed bool
	setAt     time.Time
}

// stateFilePath is where a minimal sentinel JSON is persisted on graceful
// shutdown. The file has no offset data in v8 (scrubs are stateless); it exists
// purely for forward-compatibility and to record the last-shutdown timestamp.
//
// It resolves under the datadir root (WS-01 by default), NOT $HOME any more.
// That move is what lets TestMain point the whole package at a temp dir via
// DECKPLUS_DATA_DIR and finally close the inherited coupling documented on
// stubStateFile in marker_test.go -- before this, every test that called
// Start()/Stop() without stubbing wrote the real file the production app
// reads at its own startup. datadir.Path is pure (no filesystem access), so
// it is safe in a package-level initializer; the mount guard runs later, at
// write time, in saveSentinel.
var stateFilePath = datadir.Path(datadir.State, "2t5-deck-plus-state.json")

// dodeeMostRe matches project names that must not receive balance drag events.
var dodeeMostRe = regexp.MustCompile(`(?i)(DODEE|MOST\s*PRODUCTION)`)

// mouseClickFn is used for single-click resets. Tests can replace this.
var mouseClickFn = mouse.Click

// mouseDoubleClickFn is used for double-click resets. Tests can replace this.
var mouseDoubleClickFn = mouse.DoubleClick

// mouseScrubFn is the function used to perform a channel value-box scrub.
// Signature: press(x,y) → dragto(x+delta_px, y) → release.
// Tests can replace this to capture calls without CGO.
var mouseScrubFn = func(x, y, deltaPx float64) {
	mouse.Press(x, y)
	mouse.DragTo(x+deltaPx, y)
	mouse.Release(x+deltaPx, y)
}

// startMIDIFn resolves the Loupedeck input port and registers the MIDI
// callback. Tests replace this to avoid requiring real Loupedeck hardware —
// the production implementation is midi.FindInPort + openMIDIListener
// (below), the latter a thin wrapper around midi.ListenTo used by the
// pre-refactor CLI.
var startMIDIFn = func(callback func(msg midi.Message, timestampms int32)) (stop func(), err error) {
	in, err := midi.FindInPort("Loupedeck")
	if err != nil {
		return nil, fmt.Errorf("Loupedeck not found (quit the Logi app): %w", err)
	}
	return openMIDIListener(in, callback)
}

// openMIDIListener wraps midi.ListenTo so the returned stop() ALSO closes
// the underlying MIDI port, not just cancels its callback. Extracted from
// startMIDIFn (rather than inlined) purely so it's unit-testable against a
// fake drivers.In — startMIDIFn itself can never be exercised by `go test`
// (it touches the real Loupedeck, the one hardware port this whole test
// suite is forbidden from opening), but midi.ListenTo is pure Go logic
// dispatched entirely through the drivers.In interface, so a fake
// satisfying that interface exercises the real call with zero hardware
// involved.
//
// Root cause this closes (BUG 2, 2026-08-12 — traced into
// gitlab.com/gomidi/midi/v2 v2.3.23's vendored source, module cache, not
// reproduced in this repo): midi.ListenTo's own returned stop() (listen.go)
// calls ONLY rtmidi.MIDIIn.CancelCallback() — it never calls in.Close().
// CancelCallback only clears the RtMidiIn C++ object's callback pointer;
// the underlying native CoreMIDI connection (MIDIInputPortCreate +
// MIDIPortConnectSource, opened once inside in.Open()) is torn down ONLY
// by Close() (MIDIPortDispose, via rtmidi_in_free — see RtMidi.cpp/in.go
// in the module cache). Meanwhile midi.FindInPort builds a BRAND NEW,
// unopened *in wrapper on every call (rtmididrv.Driver.Ins() constructs a
// fresh struct each time; it never reuses or tracks a previously-opened
// port). So every PRIOR engine Stop()→Start() cycle that called only the
// bare ListenTo stop() left that Start()'s native CoreMIDI connection to
// the Loupedeck alive forever, and the very next Start() opened an
// ADDITIONAL, independent one on top of it — exactly the same "CoreMIDI
// fans one physical press out to every connected port" mechanism already
// root-caused and fixed for CONCURRENT Start() calls in the Running-guard
// TOCTOU fix (commit 0a0ccdb), just triggered here by a genuinely
// SEQUENTIAL restart instead of a race, and INTERMITTENT rather than
// deterministic because the orphaned connection's Go-level callback
// staying silent after CancelCallback depends on an unsynchronized field
// read racing the CoreMIDI I/O thread rather than a hard guarantee.
// Closing the port on stop() (not just cancelling its callback) means a
// subsequent Start() is the ONLY live connection again — this closes the
// leak at its source instead of trying to out-debounce an ever-growing
// pile of stale listeners.
func openMIDIListener(in drivers.In, callback func(msg midi.Message, timestampms int32)) (stop func(), err error) {
	stopListen, err := midi.ListenTo(in, callback)
	if err != nil {
		return nil, err
	}
	return func() {
		stopListen()
		if closeErr := in.Close(); closeErr != nil {
			log.Printf("[midi] port close error: %v", closeErr)
		}
	}, nil
}

// launchSidecarFn ensures the Python IPC sidecar is reachable (spawning it
// via python3 discovery, or reusing a live socket left by scripts/run.sh or
// a prior app launch — see internal/sidecar.Launch). Tests replace this to
// avoid discovering/spawning a real python3 process; the production
// implementation is the exact sidecar.Launch call used by the CLI/app.
var launchSidecarFn = sidecar.Launch

// sidecarRecoveryBackoff bounds how often recoverSidecar will actually
// attempt a launchSidecarFn call — a package var (not a bare const) so
// tests can shrink it instead of waiting out the real default. Guards
// against a FAST-failing launch (e.g. DiscoverPython3 itself broken)
// being retried every ~200ms status tick forever; a launch that itself
// takes a while (a genuine cold respawn, up to sidecar.Launch's own 15s
// socket-wait budget) is naturally self-limiting without this at all —
// this only matters for the fast-fail case.
var sidecarRecoveryBackoff = 5 * time.Second

// recoverSidecar is BUG 3's fix (2026-08-12 live report): the sidecar
// process dying — confirmed live via a DaVinci Resolve restart taking it
// down — used to never come back; nothing on the Go side ever re-dialed
// once Start()'s initial dial had succeeded, so Status().SidecarConnected/
// SidecarError stayed stale at whatever Start() set them to while the
// gate, timecode, markers and clip selection all silently stopped
// working. This is the July self-heal's missing other half:
// ResolveDriver._ensure_connected() (sidecar.py) already recovers a dead
// Resolve-SCRIPTING connection from an otherwise-alive sidecar process —
// this recovers the sidecar PROCESS itself being gone.
//
// Called from queryStatus's error branch on ONE condition only (2026-08-25,
// third attempt at this bug class -- see the Engine struct's field comment
// above for the full history of what did NOT work): a HARD Send failure on
// an already-connected client (errors.Is(err, ipc.ErrTimeout) == false --
// EOF, broken pipe, connection refused, ErrNotConnected; deliberately NOT
// the client==nil/never-connected-at-all case Start() itself already
// surfaces and this is not asked to retry). A mere ipc.ErrTimeout NEVER
// reaches this call, no matter how many consecutive timeouts occur or how
// long they persist -- queryStatus serves a stale cached snapshot and keeps
// polling instead. By the time this runs, the peer is genuinely gone: the
// OS itself reported the connection dead, not a guess from a slow reply.
//
// REPLACE, never ADD (2026-08-13 fix, "duplicate sidecar feedback loop"
// live incident): kills whatever THIS engine's own previous
// launchSidecarFn call is tracking via e.sidecarStop and waits for it to
// actually exit BEFORE spawning a replacement. Before this fix, a new
// process was spawned on top of the old one on every recovery attempt —
// sidecar.py's then-unconditional os.unlink+bind silently stole the socket
// out from under a still-alive predecessor (now independently guarded on
// the Python side too, see sidecar.py's main()), orphaning it: still
// running, still holding a DaVinci Resolve script-connection slot, just
// unreachable — and each lost slot made every SUBSEQUENT read slower,
// triggering more recoveries, piling up more orphans. Live evidence: three
// sidecar.py processes found running simultaneously.
//
// client's identity is never replaced: it's the SAME *ipc.Client pointer
// every dispatch* call site across this file already holds, captured once
// in Start() — recovery reconnects that pointer's underlying connection IN
// PLACE (ipc.Client.Reconnect, see internal/ipc/client_test.go), so a
// successful recovery is invisible to every other call site: the very
// next Send() after this just works, no plumbing changes needed
// anywhere.
//
// Runs on its own detached goroutine so a slow respawn (launchSidecarFn
// can legitimately block up to sidecar.Launch's own 15s socket-wait
// budget on a genuine cold spawn) never blocks the 50ms MIDI/status drain
// tick that triggers it. Single-flighted (sidecarRecovering) and backed
// off (sidecarRecoveryMu/lastSidecarRecoveryAttempt/
// sidecarRecoveryBackoff) — see the Engine struct's doc comment on those
// fields for why both are needed, not just one.
func (e *Engine) recoverSidecar(ctx context.Context, client *ipc.Client) {
	if !e.sidecarRecovering.CompareAndSwap(false, true) {
		return // a recovery attempt is already in flight
	}
	e.sidecarRecoveryWG.Add(1)
	go func() {
		defer e.sidecarRecoveryWG.Done()
		defer e.sidecarRecovering.Store(false)

		if ctx.Err() != nil {
			return // engine already stopping/stopped — nothing to recover
		}
		// The client this recovery was started for is no longer THE client
		// (dropSidecar closed and cleared it after Resolve quit): relaunching a
		// sidecar and reconnecting a closed client would resurrect what the
		// supervisor just tore down (wave G, B8).
		if e.clientPtr.Load() != client {
			return
		}
		// Nothing to recover while Resolve itself is not running (F5): the
		// supervisor drops the sidecar then and relaunches it when Resolve
		// is back. Checked before the backoff is consumed.
		if running, _ := resolveStateFn(); !running {
			return
		}

		e.sidecarRecoveryMu.Lock()
		if time.Since(e.lastSidecarRecoveryAttempt) < sidecarRecoveryBackoff {
			e.sidecarRecoveryMu.Unlock()
			return
		}
		e.lastSidecarRecoveryAttempt = time.Now()
		e.sidecarRecoveryMu.Unlock()

		log.Printf("[sidecar] status round trip failed — attempting recovery")

		// Kill (and wait for) whatever this engine's own previous launch
		// spawned BEFORE attempting a new one -- see this function's own
		// "REPLACE, never ADD" doc comment above. Nilled under e.mu
		// BEFORE being called (and before launchSidecarFn runs) so Stop()
		// — which also reads/clears e.sidecarStop under e.mu, see below —
		// can never observe a half-updated value; Stop() cannot race this
		// goroutine at all, since it waits on sidecarRecoveryWG first.
		// prevStop may be a harmless no-op (a reused/dev-mode sidecar this
		// engine didn't itself spawn, per sidecar.Launch's dial-before-
		// spawn contract) or nil (a previous recovery attempt already
		// killed it and a later launchSidecarFn call failed before
		// replacing it) -- both are safe to call/skip unconditionally.
		e.mu.Lock()
		prevStop := e.sidecarStop
		e.sidecarStop = nil
		e.mu.Unlock()
		if prevStop != nil {
			prevStop()
		}

		stopSidecar, launchErr := e.launchCtx(ctx)
		if launchErr != nil {
			if ctx.Err() != nil {
				return // stopping: launchCtx cleans up whatever it started
			}
			log.Printf("[sidecar] recovery: launch failed: %v", launchErr)
			e.mu.Lock()
			e.status.SidecarConnected = false
			e.status.SidecarError = launchErr.Error()
			e.status.SidecarState = SidecarStateRetrying
			e.mu.Unlock()
			return
		}

		if ctx.Err() != nil {
			// Engine stopped while launchSidecarFn was in flight (up to
			// 15s on a cold spawn). Stop() waits on sidecarRecoveryWG
			// before touching e.client/e.sidecarStop (see Stop()), so it
			// has NOT yet run past that point — but don't reconnect a
			// client Stop() is about to tear down, and don't leave
			// whatever launchSidecarFn just did untracked: stop it
			// ourselves rather than assigning it to e.sidecarStop.
			stopSidecar()
			return
		}

		// prevStop was already killed above, so this is a plain replace,
		// not a compose -- e.sidecarStop now owns exactly the process
		// this attempt just started (or the no-op stop for a reused live
		// one), with no lingering reference to whatever came before.
		e.mu.Lock()
		e.sidecarStop = stopSidecar
		e.mu.Unlock()

		if e.clientPtr.Load() != client { // dropped while the launch was in flight
			e.mu.Lock()
			e.sidecarStop = nil
			e.mu.Unlock()
			stopSidecar()
			return
		}
		if reconnectErr := client.Reconnect(e.sockPath); reconnectErr != nil {
			log.Printf("[sidecar] recovery: reconnect dial failed: %v", reconnectErr)
			e.mu.Lock()
			e.status.SidecarConnected = false
			e.status.SidecarError = fmt.Sprintf("relaunched sidecar but reconnect failed: %v", reconnectErr)
			e.status.SidecarState = SidecarStateRetrying
			e.mu.Unlock()
			return
		}

		e.mu.Lock()
		e.status.SidecarConnected = true
		e.status.SidecarError = ""
		e.status.SidecarState = SidecarStateConnected
		e.mu.Unlock()
		log.Printf("[sidecar] recovery: reconnected")
	}()
}

// sentinelState is what we write to stateFilePath on graceful shutdown.
type sentinelState struct {
	LastShutdown string `json:"lastShutdown"`
	// LastExportFolder remembers the C5 Quick-Export button's last-used
	// target directory across restarts. Empty means "not yet set" — the
	// next Quick Export press shows the folder picker.
	LastExportFolder string `json:"lastExportFolder,omitempty"`

	// PMode remembers the P-row mode (hue/sat/lum) across restarts. The deck
	// lights its own Hue/Sat/Lum button and that light survives an app
	// restart, so defaulting back to "hue" left the software disagreeing with
	// what the hardware was showing.
	PMode string `json:"pmode,omitempty"`
}

// saveSentinel writes a shutdown timestamp (plus the current
// LastExportFolder, so it survives being called from two different call
// sites — the startup block and the drain-loop's shutdown defer — without
// either one clobbering the other) to stateFilePath.
func saveSentinel(lastFolder string, pmode string) {
	ps := sentinelState{LastShutdown: time.Now().Format(time.RFC3339), LastExportFolder: lastFolder, PMode: pmode}
	b, err := json.MarshalIndent(ps, "", "  ")
	if err != nil {
		log.Printf("[state] marshal error: %v", err)
		return
	}
	// Guarded: EnsureDirFor refuses to create the directory when the path
	// is inside an unmounted data root, so a missing WS-01 can never cause
	// the root to be materialised on the boot disk. Losing a sentinel write
	// is harmless (it is a forward-compat timestamp), so this only logs.
	if err := datadir.EnsureDirFor(stateFilePath); err != nil {
		log.Printf("[state] not saving sentinel: %v", err)
		return
	}
	if err := os.WriteFile(stateFilePath, b, 0644); err != nil {
		log.Printf("[state] write error %s: %v", stateFilePath, err)
		return
	}
	log.Printf("[state] sentinel saved to %s", stateFilePath)
}

// defaultWheelSens is the fallback drag sensitivity (pixels per accumulated step)
// used when a wheel binding does not specify its own "sens" field.
const defaultWheelSens = 15.0

// maxDragPx is the global default cap on the absolute pixel delta for a single
// wheel drag tick. Prevents the cursor from overshooting the wheel's master bar
// on fast turns. Per-binding "maxDrag" overrides this when non-zero.
const maxDragPx = 40.0

// defaultPropertySens is the fallback delta-per-accumulated-step multiplier
// used when an Edit-page "property" binding does not specify its own
// "sens" in bindings.json. 1.0 (identity) rather than 0 so an accidentally
// omitted sens still produces a visible effect instead of silently no-op'ing.
const defaultPropertySens = 1.0

// defaultMarkerDurationSeconds is the fallback duration (seconds) used
// when an "addMarker" binding does not specify its own
// markerDurationSeconds in bindings.json.
const defaultMarkerDurationSeconds = 1.0

// defaultMarkerColor is the fallback Resolve marker color name used when
// an "addMarker" binding does not specify its own markerColor in
// bindings.json.
const defaultMarkerColor = "Blue"

// defaultSelectClipDetentThreshold is the fallback accumulated-raw-step
// magnitude required to fire one detent's worth of action (see
// selectClipDetent below) when a binding does not specify its own
// detentThreshold in bindings.json. The physical Loupedeck encoder emits
// several raw relative-CC steps per felt "click" of the dial -- firing on
// every single raw step (threshold 1, the original behavior) was reported
// as "too sensitive / cycling not working well": a slow, deliberate turn
// intended to move ONE clip would blow past several. 4 matches this exact
// hardware's already-tuned per-tick magnitude used elsewhere for the same
// reason (internal/tc.DefaultJogTier5Min). Shared by TWO features on the
// SAME physical D1 knob (never both bound at once, see Binding.Type):
// the (unreachable-in-production) "selectClip" binding this constant was
// originally tuned for, and the "jogSeconds" coarse playhead-jump binding
// -- both debounce the identical hardware, so there is no reason for
// their fallback thresholds to diverge.
const defaultSelectClipDetentThreshold = 4

// defaultJogSecondsSeconds is the fallback coarse playhead-jump size (in
// seconds) used when a "jogSeconds" binding does not specify its own
// secondsPerStep in bindings.json -- see dispatchJogSeconds below.
const defaultJogSecondsSeconds = 60.0

// defaultLinesPerDetent is the fallback scroll-wheel line count posted per
// firm detent when a "scrollDelta" binding does not specify its own
// linesPerDetent in bindings.json -- see config.Binding.LinesPerDetent and
// dispatchScrollDelta below. 1 (the smallest meaningful nonzero value)
// rather than 0, same "an accidentally omitted tunable still produces a
// visible effect instead of silently no-op'ing" rationale as
// defaultPropertySens.
const defaultLinesPerDetent = 1

// chooseFolderFn prompts a native macOS folder picker and returns the
// selected POSIX path (empty string, nil error means the user cancelled).
// Package-var seam — same pattern as mouseClickFn/startMIDIFn/
// launchSidecarFn — so `go test` NEVER pops a real dialog: tests replace
// this with a fake. Production default shells out to `osascript`. Called
// ONLY from dispatchQuickExport, which callers must invoke inside its own
// goroutine (see dispatchNoteOn's IsQuickExport() case) — chooseFolderFn
// can block for as long as the user takes to respond to the dialog, and
// must never block the MIDI-callback goroutine.
var chooseFolderFn = defaultChooseFolder

// chooseFolder is what the dispatchers get: the folder picker unless the arm
// switch is off. A dialog is the deck asking the Mac to do something, so a
// disarmed deck opens none ("" = cancelled, nothing is exported) (wave G, B7).
func (e *Engine) chooseFolder() (string, error) {
	if !e.writesAllowed() {
		log.Printf("[note_on] Quick Export folder dialog NOT opened: the deck is disarmed")
		e.setLastAction("disarmed: Quick Export folder dialog not opened")
		return "", nil
	}
	return chooseFolderFn()
}

func defaultChooseFolder() (string, error) {
	out, err := exec.Command("osascript", "-e",
		`POSIX path of (choose folder with prompt "Choose Quick Export folder")`).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// statusCacheTTL is how long a sidecar status response is reused.
const statusCacheTTL = 200 * time.Millisecond

// CalibrateCapture is a single calibration prompt entry. Exported so
// cmd/2t5-deck-plus's --calibrate flow (which stays in main.go) can consume it.
type CalibrateCapture struct {
	Key  string
	Axis string
	Hint string
}

// DeriveRequiredCalibPoints scans all bindings in cfg and returns the union of
// calibration points that --calibrate must capture.
//
// Rules:
//   - type "wheel":  require the wheel's master bar point (b.Wheel) and the
//     implied reset-button point. The reset key is derived by replacing the
//     "_master" suffix with "_reset"; if no "_master" suffix is present the
//     suffix "_reset" is appended (e.g. "my_wheel" → "my_wheel_reset").
//     Skipped when the binding has a custom press override (reset never clicked).
//   - type "click":  require the named point (b.Point).
//   - type "balance": require the 12 per-wheel per-channel box positions:
//     for each of {lift, gamma, gain, offset}, require <wheel>_R, <wheel>_G, <wheel>_B.
//   - type "selectWheel" / "resetActiveWheel": no new calib points needed.
//   - type "cdl" / "key": no calibration points needed.
//
// The returned slice contains each point at most once (union semantics) and is
// ordered deterministically.
func DeriveRequiredCalibPoints(cfg *config.Config) []CalibrateCapture {
	seenMaster := map[string]bool{}
	seenReset := map[string]bool{}
	seenClick := map[string]bool{}
	seenBox := map[string]bool{}
	var hasBalance bool

	var masters, resets, clicks, boxes []CalibrateCapture

	for _, controls := range cfg.AllBindings() {
		for _, b := range controls {
			switch {
			case b.IsWheel():
				if b.Wheel == "" {
					continue
				}
				// Master bar point — always required for the drag action.
				if !seenMaster[b.Wheel] {
					seenMaster[b.Wheel] = true
					masters = append(masters, CalibrateCapture{
						Key:  b.Wheel,
						Axis: "h",
						Hint: fmt.Sprintf("Hover over the %q master bar in Resolve Primaries → Wheels", b.Wheel),
					})
				}
				// Reset button point — only required when using the default press action.
				if b.Press == nil {
					resetKey := strings.Replace(b.Wheel, "_master", "_reset", 1)
					if resetKey == b.Wheel {
						resetKey = b.Wheel + "_reset"
					}
					if !seenReset[resetKey] {
						seenReset[resetKey] = true
						resets = append(resets, CalibrateCapture{
							Key:  resetKey,
							Axis: "click",
							Hint: fmt.Sprintf("Hover over the reset button (↺) for %q in Resolve", b.Wheel),
						})
					}
				}
			case b.IsClick():
				if b.Point == "" {
					continue
				}
				if !seenClick[b.Point] {
					seenClick[b.Point] = true
					clicks = append(clicks, CalibrateCapture{
						Key:  b.Point,
						Axis: "click",
						Hint: fmt.Sprintf("Hover over the %q click target in Resolve", b.Point),
					})
				}
			case b.IsBalance():
				// All balance bindings share the same 12 value-box positions.
				hasBalance = true
				// selectWheel and resetActiveWheel add no new calib points.
			}
		}
	}

	// If any balance binding exists, require 12 box positions:
	// for each of 4 wheels, R/G/B value-box centers.
	if hasBalance {
		for _, w := range []string{"lift", "gamma", "gain", "offset"} {
			for _, ch := range []string{"R", "G", "B"} {
				k := w + "_" + ch
				if !seenBox[k] {
					seenBox[k] = true
					wh := w
					c := ch
					boxes = append(boxes, CalibrateCapture{
						Key:  k,
						Axis: "click",
						Hint: fmt.Sprintf("Hover over the %s value box for the %q wheel in Resolve Primaries", c, wh),
					})
				}
			}
		}
	}

	// Deterministic ordering: masters → resets → clicks → balance-boxes.
	sortCaptures(masters)
	sortCaptures(resets)
	sortCaptures(clicks)
	sortCaptures(boxes)

	result := make([]CalibrateCapture, 0, len(masters)+len(resets)+len(clicks)+len(boxes))
	result = append(result, masters...)
	result = append(result, resets...)
	result = append(result, clicks...)
	result = append(result, boxes...)
	// Reset targets of type "click" name a calibration point too. Without
	// this scan a future click-type target with a genuinely new point would
	// silently never be prompted by --calibrate, and the reset would fail at
	// runtime with "calib-missing-point".
	for name, rt := range cfg.TopLevel.ResetTargets {
		if rt.Type != "click" || rt.Point == "" || seenClick[rt.Point] {
			continue
		}
		seenClick[rt.Point] = true
		result = append(result, CalibrateCapture{
			Key:  rt.Point,
			Axis: "click",
			Hint: fmt.Sprintf("hover the reset control for target %q", name),
		})
	}

	return result
}

// sortCaptures sorts a slice of CalibrateCapture in-place by key name.
func sortCaptures(s []CalibrateCapture) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Key < s[j-1].Key; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// dispatchPressAction executes a PressAction (the optional per-binding knob-press
// override). It is called by dispatchNoteOn when b.Press != nil.
//
// Returns a human-readable LastAction summary of the action actually
// dispatched (see Engine.setLastAction's doc comment for the "best-effort,
// dispatch-point" semantics), or "" for every SKIP/no-op/unknown-type path.
func dispatchPressAction(
	control string,
	pa *config.PressAction,
	cal *calib.Calib,
	queryPage func() string,
	aw *target.State,
) string {
	switch pa.Type {
	case "none":
		log.Printf("[note_on] control=%q press=none (suppressed)", control)
		return ""

	case "key":
		frontmostID := macapp.FrontmostBundleID()
		if !macapp.BundleIDIsResolve(frontmostID) {
			log.Printf("[note_on] control=%q press=key SKIP resolve-not-frontmost key=%q", control, pa.Key)
			return ""
		}
		log.Printf("[note_on] control=%q press=key key=%q", control, pa.Key)
		if err := keys.Send(pa.Key); err != nil {
			log.Printf("[note_on] control=%q press=key key=%q error: %v", control, pa.Key, err)
		}
		return pa.Key

	case "click":
		if cal == nil {
			log.Printf("[note_on] control=%q press=click SKIP no-calibration point=%q", control, pa.Point)
			return ""
		}
		frontmostID := macapp.FrontmostBundleID()
		if !macapp.BundleIDIsResolve(frontmostID) {
			log.Printf("[note_on] control=%q press=click SKIP resolve-not-frontmost point=%q", control, pa.Point)
			return ""
		}
		pt, ptOk := cal.Point(pa.Point)
		if !ptOk {
			log.Printf("[note_on] control=%q press=click SKIP calib-missing-point point=%q (re-run --calibrate)", control, pa.Point)
			return ""
		}
		log.Printf("[note_on] control=%q press=click point=%q at (%.1f,%.1f)", control, pa.Point, pt.X, pt.Y)
		mouse.Click(pt.X, pt.Y)
		return control

	case "selectWheel":
		if aw != nil {
			aw.Set(pa.Wheel)
		}
		log.Printf("[note_on] control=%q press=selectWheel wheel=%q", control, pa.Wheel)
		if aw == nil {
			return ""
		}
		return "Wheel: " + humanWheelName(pa.Wheel)

	default:
		log.Printf("[note_on] control=%q press SKIP unknown type=%q", control, pa.Type)
		return ""
	}
}

// executeResetWheel resets the active wheel by double-clicking its R, G, and B
// value boxes in sequence. Each double-click tells Resolve to reset that
// channel's value box to default. Returns true if all three double-clicks fired.
func executeResetWheel(
	wheel string,
	cal *calib.Calib,
) bool {
	if cal == nil {
		log.Printf("[reset] wheel=%q SKIP no-calibration", wheel)
		return false
	}
	for i, ch := range []string{"R", "G", "B"} {
		if i > 0 {
			time.Sleep(50 * time.Millisecond)
		}
		key := wheel + "_" + ch
		pt, ok := cal.Point(key)
		if !ok {
			log.Printf("[reset] wheel=%q SKIP calib-missing-point box=%q (run --calibrate)", wheel, key)
			return false
		}
		log.Printf("[reset] wheel=%q double-click %s box at (%.1f,%.1f)", wheel, ch, pt.X, pt.Y)
		mouseDoubleClickFn(pt.X, pt.Y)
	}
	return true
}

// resolveNoteBank picks which config bank note_on (knob-press / button)
// dispatch resolves control against for the current page AND layer.
//
// layer must be the LIVE layer (layerState.Get()), not a hardcoded 1 — PART
// A, 2026-08-12 review board finding: this was the ONLY production caller of
// the layer-aware resolveNoteBinding, and until this fix it passed a
// hardcoded 1 regardless of what the console's L1/L2/L3 buttons had
// selected. Every OTHER layer-aware helper in this file (editBankBinding,
// the seven direct MIDI-callback intercepts, resolveTurnBinding) already
// threaded the live layer through correctly; this one call site silently
// did not, so no button ever resolved above layer 1 even after switching —
// see TestNoteOnHonoursLiveLayer for the end-to-end regression guard.
//
// On any page OTHER than "edit", at layer 1, this is always colBank (==
// getBank(), "col" today) — byte-identical to pre-Phase-2 behavior,
// unaffected by the edit bank's existence. At layer >1 it may instead
// resolve to a layer bank (col-{layer} or {page}-{layer}) — see
// config.Cascade.
//
// On the Edit page, at layer 1, it shadows to "edit" ONLY when the edit bank redefines
// this EXACT control as a knob-turn type (property or jog) OR a plain
// key binding (type "key", e.g. C2's Edit-page ⌘B Blade/Razor) —
// mirroring the drain loop's page-aware bank resolution for turns, but
// with one extra guard turns don't need: note_on's col bank also holds
// buttons (Copy/Paste/Undo/Redo/C6/L1/L2/L3/P1-8/C1/C3) that have no
// edit-bank counterpart at all. Blindly shadowing to "edit" whenever
// page=="edit" (as the drain loop does) would make cfg.Binding("edit",
// control) miss for every one of those buttons and silently drop the
// press — exactly the regression this function exists to prevent. Any
// control the edit bank does NOT define, or defines as something other
// than property/jog/key, keeps resolving against colBank even while the
// Edit page is showing, so buttons and any col-only knob keep working
// unchanged on every page.
func resolveNoteBank(cfg *config.Config, page string, layer int, colBank string, control string) string {
	if _, bank, ok := resolveNoteBinding(cfg, page, layer, control); ok {
		return bank
	}
	return colBank
}

// maybeAutoExitMarker ends marker mode once the playhead leaves the active
// marker's [absStart, absEnd).
//
// A fallback, not the primary exit: C1 is an unconditional toggle and owns
// deliberate exit. This just means you can never be left in marker mode after
// dialling away from the marker you were editing.
//
// Gated on a successful status round-trip AND on the playhead specifically
// being readable -- the third feature in this codebase to need that gate,
// after the D1 selection auto-clear and the layer/target page resets.
//
// Resize changes marker DURATION, not playhead position, so this cannot fire
// mid-resize. With no recorded range (both bounds zero) it never fires, so a
// marker created before ranges were tracked is not silently dropped.
func maybeAutoExitMarker(e *Engine, statusOK bool, havePlayheadFrame bool, playheadFrame int) {
	if !statusOK || !havePlayheadFrame {
		return
	}
	if e.getActiveMarkerFrame() < 0 {
		return
	}
	start, end := e.getActiveMarkerAbsRange()
	if start == 0 && end == 0 {
		return
	}
	if playheadFrame < start || playheadFrame >= end {
		// Mirrors the direction-button selection auto-reset's log shape
		// exactly (see the drain loop's "[drain] control=selection
		// method=autoreset ..." lines) -- same pattern, same diagnosability,
		// deliberately not a second one invented for this feature.
		log.Printf("[drain] control=marker method=autoreset playhead=%d outside [%d,%d), clearing active marker", playheadFrame, start, end)
		e.clearActiveMarker()
	}
}

// getActiveMarkerAbsRange reads the active marker's absolute frame range.
func (e *Engine) getActiveMarkerAbsRange() (int, int) {
	e.markerMu.Lock()
	defer e.markerMu.Unlock()
	return e.activeMarkerAbsStart, e.activeMarkerAbsEnd
}

// setActiveMarkerAbsRange records the active marker's absolute frame range,
// used only by the auto-exit check.
func (e *Engine) setActiveMarkerAbsRange(start, end int) {
	e.markerMu.Lock()
	e.activeMarkerAbsStart = start
	e.activeMarkerAbsEnd = end
	e.markerMu.Unlock()
}

// shouldResizeMarker gates the marker-resize intercept on the Edit page.
//
// It previously fired on binding TYPE alone (any IsJog binding, with marker
// mode live). Marker-mode exit is range-based rather than page-based, so
// leaving the Edit page with marker mode still active and then turning a
// Color-page jog binding would silently resize an invisible marker instead of
// doing its job. The layer system makes that reachable: layers 2/3 explicitly
// invite new col-bank bindings, and any of them could be a jog.
func shouldResizeMarker(b config.Binding, page string, activeMarkerFrame int) bool {
	return page == "edit" && b.IsJog() && activeMarkerFrame >= 0
}

// editBankBinding resolves a control against the edit banks: the held-
// modifier OVERLAY first (2026-08-13, e.g. "edit-shift"/"col-shift" while
// Shift is held), then layer, then plain edit.
//
// The eight direct MIDI-callback intercepts (isSelectClipRelease,
// isSelectClipVert, isSelectClipHorizControl, isSelectClipClear,
// isAddMarkerControl, isJogPressControl, isMarkerColorControl,
// isMarkerAtPlayheadControl) all previously hardcoded cfg.Binding("edit",
// control). Because they run BEFORE resolveNoteBinding is ever reached,
// that made every one of them structurally invisible to the layer system —
// D1, Up/Down, Left/Right, C5, C1, the ControlDial press and P1-8btn would
// all have silently ignored the active layer. Two reviewers found this
// independently and both noted it is the third time this codebase has been
// specced as if dispatch were purely type-driven when a hardcoded intercept
// runs first. The layer rung fixed that (2026-08-12).
//
// mod extends this AGAIN (2026-08-13) for the identical reason, this time
// for the held-modifier overlay: without it, authoring a chord binding for
// one of these eight controls in "col-{mod}" or "edit-{mod}" would silently
// do nothing, since the overlay resolution normally wired in at
// resolveNoteBank/dispatchNoteOn's call site (Start()'s MIDI callback) is
// NEVER reached for any of the eight — they all `return` from Start()
// before that point. Fixed the SAME way the layer rung was: this shared
// function is the ONE place that needs to know about it, not eight
// separate call sites.
//
// mod is the engine's CURRENTLY held-overlay name (e.g. "shift"), or "" if
// none — pass e.getHeldOverlay()'s value, computed ONCE per MIDI event by
// the caller (Start()) and threaded through, not re-fetched per intercept.
// When mod != "", the overlay bank is tried FIRST, via resolveOverlayBinding
// (the EXACT SAME {page}-{mod} then col-{mod} precedence the generic,
// non-direct-intercept dispatch path already uses at Start()'s "Held-
// modifier CHORD OVERLAY" block) — this is deliberately the SAME function,
// not a parallel hand-rolled lookup, so there is one resolution story for
// overlay bank order, not two that could silently drift apart. A miss on
// the overlay (no chord for this control) falls through to the layer/plain
// edit lookup exactly as before — the "unfinished layer/overlay never
// leaves a control dead" contract this whole cascade already guarantees
// elsewhere.
//
// It deliberately does NOT use config.Cascade for the layer/plain-edit
// rungs: these intercepts only ever consult the edit banks, never col
// directly (the overlay rung is the one exception, since resolveOverlayBinding
// itself may fall back to a col-{mod} bank), so the full four-rung walk
// would be wrong here.
func editBankBinding(cfg *config.Config, page string, layer int, control string, mod string) (config.Binding, bool) {
	if mod != "" {
		if b, _, ok := resolveOverlayBinding(cfg, page, mod, control); ok {
			return b, true
		}
	}
	if layer > 1 {
		if b, ok := cfg.Binding(config.LayerBank("edit", layer), control); ok {
			return b, true
		}
	}
	return cfg.Binding("edit", control)
}

// targetName reads the active reset target, tolerating a nil holder (the
// headless CLI and several tests pass nil) by falling back to the default.
func targetName(tgt *target.State) string {
	if tgt == nil {
		return target.Default
	}
	return tgt.Get()
}

// lookupResetTarget finds how to reset a named target, from
// _config.resetTargets.
//
// A miss is NOT an error and must not be treated as one: with no table
// configured (the shipped state today) every lookup misses, and the caller
// falls back to the legacy executeResetWheel path. That fallback is what keeps
// Col byte-identical to the old resetActiveWheel until a table is added.
func lookupResetTarget(cfg *config.Config, name string) (config.ResetTarget, bool) {
	if cfg == nil || cfg.TopLevel.ResetTargets == nil {
		return config.ResetTarget{}, false
	}
	rt, ok := cfg.TopLevel.ResetTargets[name]
	return rt, ok
}

// targetValidForPage is the second safety rail behind the universal Col reset.
//
// Even with the page-change reset below, a target must never fire against the
// wrong page: mouse-driven click and CDL resets belong to the Color page,
// script-API property resets to the Edit page. Without this, a Col press could
// silently mutate a real clip's crop while the user is looking at a grade —
// and since the API cannot read these values back, there is no confirmation
// step available to catch it.
func targetValidForPage(rt config.ResetTarget, page string) bool {
	switch rt.Type {
	case "click", "resetParam":
		return page == "color"
	case "resetProperty":
		return page == "edit"
	default:
		return false
	}
}

// maybeResetTargetOnPageChange mirrors maybeResetLayerOnPageChange for the
// active reset target, with the same successful-round-trip gate and the same
// reason. Three reviewers independently found that resetting the layer but not
// the target leaves Col armed with a stale, cross-page target.
func maybeResetTargetOnPageChange(tgt *target.State, prevPage string, ok bool, page string) string {
	if !ok {
		return prevPage
	}
	if page != prevPage {
		tgt.Reset()
		return page
	}
	return prevPage
}

// maybeResetLayerOnPageChange drops the console back to layer 1 whenever
// Resolve's page changes, and returns the page to remember for the next tick.
//
// Takes primitives rather than the ticker's cachedStatus because that type is
// declared inside Start() and so cannot be named at package scope — which also
// makes this directly unit-testable without standing up a status pipeline.
//
// It acts ONLY on a successful sidecar round-trip. A failed read leaves both
// the layer and the remembered page untouched. This is not defensive
// programming for its own sake: under Resolve script-connection contention the
// sidecar returns None, and reading that as "the page changed" would silently
// discard a live layer selection. The D1 clip-selection auto-clear needs the
// identical gate for the identical reason — see the 2026-07-08
// connection-poison fix.
func maybeResetLayerOnPageChange(lay *layer.State, prevPage string, ok bool, page string) string {
	if !ok {
		return prevPage
	}
	if page != prevPage {
		lay.Reset()
		return page
	}
	return prevPage
}

// resolvePModeSwitch looks a control up as a P1-P8 mode switch (hue/sat/lum),
// consulting only the layer-1 banks -- same escape-proofing rationale as
// resolveLayerSwitch.
func resolvePModeSwitch(cfg *config.Config, page string, control string) (config.Binding, bool) {
	for _, bank := range []string{page, "col"} {
		if b, ok := cfg.Binding(bank, control); ok && b.IsPMode() {
			return b, true
		}
	}
	return config.Binding{}, false
}

// resolveLayerSwitch looks a control up as a layer switch, consulting ONLY the
// layer-1 banks ({page} then col) — never col-{N} or {page}-{N}.
//
// This is deliberate and load-bearing. If a layer-2 bank could redefine
// L1/L2/L3 there would be no way back out of that layer: the console would be
// stranded until the config was edited and the app relaunched. The user still
// chooses WHICH buttons switch layers by editing the layer-1 banks; those
// buttons just cannot be stolen.
//
// It is also why layer switching is a direct callback intercept rather than a
// dispatchNoteOn case — the normal cascade cannot express "layer-1 banks only",
// and a dispatchNoteOn arm would be unreachable dead code.
func resolveLayerSwitch(cfg *config.Config, page string, control string) (config.Binding, bool) {
	for _, bank := range []string{page, "col"} {
		if b, ok := cfg.Binding(bank, control); ok && b.IsLayer() {
			return b, true
		}
	}
	return config.Binding{}, false
}

// resolveOverlayBinding resolves a control against the held-modifier CHORD
// OVERLAY banks (Part B, 2026-08-12 REV 2 design review): {page}-{mod} then
// col-{mod}, e.g. while Cmd is held on the Edit page, "edit-cmd" then
// "col-cmd". Returns ok=false (never colBank, never the normal cascade) when
// mod is empty or neither overlay bank redefines control -- callers are
// expected to fall through to the ordinary resolveNoteBank/Cascade path on a
// miss, exactly as an unfinished LAYER does.
//
// Deliberately NOT modeled as another rung of Cascade's layer walk (i.e. NOT
// LayerBank("col", someInt)): both reviewers rejected reusing the layer int
// for this. internal/layer.Max is 3 and State.Set silently clamps anything
// outside [Min,Max] (layer_test.go's TestSetIgnoresInvalid literally uses 4
// as its "obviously invalid" example) -- a momentary chord and a persistent
// base layer are two different kinds of state, and conflating them would
// also reintroduce the exact page-change race this design exists to avoid
// BY CONSTRUCTION: maybeResetLayerOnPageChange resets ONLY layerState to 1
// on a Resolve page change (see its doc comment). Splitting the overlay into
// its own separate, non-persisted piece of state means a page change can
// never leave a stale HELD CHORD BIT hiding inside what looks like an
// ordinary layer number -- there is no such bit to leave stale, because the
// overlay was never folded into layerState in the first place.
//
// No guard/whitelist (unlike noteBankGuard, which resolveNoteBank passes to
// Cascade): the overlay is a wholly separate resolution PASS tried BEFORE
// the normal cascade, not one of Cascade's own rungs, so there is no
// col-vs-page button/knob-turn asymmetry to protect against here.
//
// Direct-intercept-family controls (marker buttons, direction-select, jog,
// ControlDial press -- see editBankBinding's doc comment) are NEVER reached
// through this function: they return from Start()'s MIDI callback earlier
// than the point this is called from. A chord over one of those would need
// editBankBinding itself extended to also consult an edit-{mod} bank, which
// it does not today.
func resolveOverlayBinding(cfg *config.Config, page, mod, control string) (config.Binding, string, bool) {
	if mod == "" {
		return config.Binding{}, "", false
	}
	for _, bank := range []string{page + "-" + mod, "col-" + mod} {
		if b, ok := cfg.Binding(bank, control); ok {
			return b, bank, true
		}
	}
	return config.Binding{}, "", false
}

// sendColorByRenderState asks the sidecar to paint every video clip by whether
// it is a render: green yes, yellow no, offline left alone. The HUD strip says
// the same thing, but only while you are looking at the HUD -- this puts it on
// the timeline.
//
// The ONE thing this app writes into the project. Deliberately manual: it
// fires from a button press and never from a poll, and Resolve's own undo
// covers it. No page gate -- clip colours are an Edit-page fact, but setting
// them from Color is legitimate.
//
// Returns the painted count and whether the call got that far, so callers can
// tell "painted nothing" from "never ran".
func sendColorByRenderState(client *ipc.Client, control string, b config.Binding) (int, bool) {
	if client == nil {
		log.Printf("[note_on] control=%q colorByRenderState SKIP no-sidecar", control)
		return 0, false
	}
	resp, err := client.Send(map[string]any{
		"op":       "color_by_render_state",
		"rendered": b.Rendered,
		"neutral":  b.Neutral,
	})
	if err != nil {
		log.Printf("[note_on] control=%q colorByRenderState send error: %v", control, err)
		return 0, false
	}
	painted, _ := resp["painted"].(float64)
	log.Printf("[note_on] control=%q colorByRenderState painted=%d", control, int(painted))
	return int(painted), true
}

// noteBankGuard carries the whitelist described in resolveNoteBank's doc
// comment above: a page bank only shadows col for controls it redefines as a
// knob-turn type (property or jog), a plain key, or a page-specific action.
//
// The whitelist is why a new page-bank type is unreachable until it is named
// here: the cascade falls straight through to col, and the button silently
// keeps doing whatever col said. colorByRenderState hit exactly that — Edit C6
// resolved to col's toggleHud and the timeline never painted.
//
// config.Cascade applies this to page banks ONLY, never to col — see its doc
// comment for why guarding col would silently drop every button that has no
// edit-bank counterpart.
func noteBankGuard(b config.Binding) bool {
	return b.IsProperty() || b.IsJog() || b.IsKey() ||
		b.IsColorByRenderState() || b.IsToggleHudAndColor()
}

// resolveNoteBinding resolves a note_on (button / knob-press) control for the
// current page AND layer, returning the binding, the bank it came from, and
// whether anything matched.
//
// At layer 1 this is byte-identical to the pre-layer resolveNoteBank; that is
// asserted directly by TestResolveNoteBindingLayerOneMatchesLegacy, which
// compares it against an independent verbatim copy of the old function across
// every control in the real config/bindings.json.
func resolveNoteBinding(cfg *config.Config, page string, layer int, control string) (config.Binding, string, bool) {
	return cfg.Cascade(page, layer, control, noteBankGuard)
}

// resolveTurnBinding resolves a control_change (knob turn) control for the
// current page, layer, AND held-modifier overlay.
//
// mod is the engine's currently held-overlay name (e.g. "shift"), or ""
// if none -- SAME contract as editBankBinding's mod parameter (2026-08-13):
// pass e.getHeldOverlay()'s value, computed ONCE per resolution window by
// the caller and threaded through, not re-fetched per check. When mod !=
// "", the overlay bank is tried FIRST, via resolveOverlayBinding -- the
// EXACT SAME {page}-{mod} then col-{mod} precedence editBankBinding (the
// note_on/button side of this same overlay feature) already uses, so
// there is ONE overlay resolution story for the whole console, not two
// that could silently drift apart (e.g. a turn resolving col-{mod} but a
// button resolving something else for the identical held modifier). A
// miss on the overlay (no chord for this control) falls through to the
// ordinary layer/page cascade exactly as before -- the "unfinished
// layer/overlay never leaves a control dead" contract this whole cascade
// already guarantees elsewhere.
//
// Unlike note_on there is NO type whitelist on the NORMAL cascade rung:
// turn resolution has always shadowed the page bank unconditionally (the
// drain loop's old `if s.page == "edit" { currentBank = "edit" }`),
// because the col-vs-edit button asymmetry that motivates noteBankGuard
// does not exist for knobs. The overlay rung ALSO has no whitelist --
// same posture as resolveOverlayBinding's own doc comment explains for
// the note_on side: it is a separate resolution PASS tried before
// Cascade, not one of Cascade's own rungs.
//
// The drain loop computes its bank once per tick and reuses it at three
// sites. Each must call this independently (WITH THE SAME mod value for
// any two sites processing the same tick's data, exactly like tickLayer),
// or a fast turn straddling two sites' resolutions mid-gesture could see
// the overlay bank at one site and not the other -- the identical
// staleness risk tickLayer already guards against for layer.
func resolveTurnBinding(cfg *config.Config, page string, layer int, control string, mod string) (config.Binding, string, bool) {
	return resolveTurnBindingMode(cfg, page, layer, control, mod, "")
}

// resolveTurnBindingMode is resolveTurnBinding with the live slider mode
// (hue/sat/lum) as one more rung, between the held-modifier overlay and the
// layer/page cascade: overlay -> {page}-pm-{mode} -> col-pm-{mode} -> cascade.
// The mode rung only exists for P1-P8; an empty mode, any other control, or a
// mode bank that does not assign the control falls straight through, so a
// preset without per-mode data resolves exactly as before.
func resolveTurnBindingMode(cfg *config.Config, page string, layer int, control string, mod string, pmode string) (config.Binding, string, bool) {
	if mod != "" {
		if b, bank, ok := resolveOverlayBinding(cfg, page, mod, control); ok {
			return b, bank, true
		}
	}
	if b, bank, ok := cfg.ModeBinding(page, pmode, control); ok {
		return b, bank, true
	}
	return cfg.Cascade(page, layer, control, nil)
}

// dispatchNoteOn handles a button/knob-press (note_on) event using the type-driven
// binding dispatch. Returns whether the event was consumed.
//
// chooseFolder/getLastFolder/setLastFolder are only used by the
// IsQuickExport() case (the C5 button) — production callers pass
// chooseFolderFn / e.getLastFolder / e.setLastFolder; tests pass fakes so
// no real folder-picker dialog ever appears under `go test`.
//
// activeTrack is the D1 knob's currently-selected video track number (0 if
// unset/"auto") — only consulted by the IsProperty() case (a knob-press
// reset), forwarded to the sidecar's reset_property op as "track" exactly
// like processNonBalanceDrain's property branch forwards it to
// bump_property.
//
// toggleHUD is only consulted by the IsToggleHud() case (the C6 button) — a
// nilable callback (production callers pass e.toggleHUD, wired via
// SetToggleHUDCallback; tests pass a fake or nil) that flips the native HUD
// panel's visibility. nil is a safe no-op, matching cmd/2t5-deck-plus/main.go's
// headless CLI, which never wires a callback.
//
// Returns a human-readable LastAction summary of the action actually
// dispatched (see Engine.setLastAction's doc comment for the "best-effort,
// dispatch-point" semantics -- e.g. this fires even when the underlying
// CGEvent/mouse click was itself the last thing before a downstream Resolve
// refusal), or "" for every SKIP/no-op case (IsBalance()'s note_on is a
// documented no-op; a nil toggleHUD callback under the headless CLI is
// likewise never reported).
func dispatchNoteOn(
	control string,
	b config.Binding,
	modeTracker *mode.Tracker,
	cal *calib.Calib,
	client *ipc.Client,
	node int,
	activeTrack int,
	queryPage func() string,
	aw *target.State,
	cfg *config.Config,
	chooseFolder func() (string, error),
	getLastFolder func() string,
	setLastFolder func(string),
	toggleHUD func() bool,
) string {
	switch {
	case b.IsWheel():
		if !modeTracker.AllowKnobAction() {
			log.Printf("[note_on] control=%q wheel-click SKIP mode=%q", control, modeTracker.Current())
			return ""
		}
		if b.Press != nil {
			return dispatchPressAction(control, b.Press, cal, queryPage, aw)
		}
		// Default: click the wheel's reset button (↺).
		if cal == nil {
			log.Printf("[note_on] control=%q wheel-click SKIP no-calibration", control)
			return ""
		}
		frontmostID := macapp.FrontmostBundleID()
		frontmost := macapp.BundleIDIsResolve(frontmostID)
		page := queryPage()
		if !gate.Allow(frontmost, page) {
			log.Printf("[note_on] control=%q wheel-click SKIP gate frontmost=%v page=%q", control, frontmost, page)
			return ""
		}
		resetKey := strings.Replace(b.Wheel, "_master", "_reset", 1)
		if resetKey == b.Wheel {
			resetKey = b.Wheel + "_reset"
		}
		resetPt, ptOk := cal.Point(resetKey)
		if !ptOk {
			log.Printf("[note_on] control=%q wheel-click SKIP calib-missing-point reset_key=%q (re-run --calibrate)", control, resetKey)
			return ""
		}
		log.Printf("[note_on] control=%q wheel-click reset_key=%q at (%.1f,%.1f)", control, resetKey, resetPt.X, resetPt.Y)
		mouse.Click(resetPt.X, resetPt.Y)
		return humanWheelName(b.Wheel) + " reset"

	case b.IsCDL():
		if !modeTracker.AllowKnobAction() {
			log.Printf("[note_on] control=%q cdl-reset SKIP mode=%q", control, modeTracker.Current())
			return ""
		}
		if b.Press != nil {
			return dispatchPressAction(control, b.Press, cal, queryPage, aw)
		}
		if client == nil {
			log.Printf("[note_on] control=%q cdl-reset SKIP no-sidecar", control)
			return ""
		}
		cmd := map[string]any{
			"op":    "reset_param",
			"node":  node,
			"param": b.Param,
			"ch":    b.Ch,
		}
		// Own goroutine (2026-08-12 dispatch-generalization pass), same
		// "must never hold the MIDI-callback goroutine" reasoning as
		// IsQuickExport/IsColorByRenderState above -- safe to generalize
		// here specifically because a param reset writes NO local Engine
		// state any other control's dispatch reads (unlike the marker
		// paths, see dispatchAddMarker's doc comment) and resets to a
		// FIXED default, so two resets racing out of order converge on the
		// identical final value regardless of which one the sidecar
		// processes last -- not an accumulator like set_node, which stayed
		// synchronous for exactly that reason.
		log.Printf("[note_on] control=%q cdl-reset param=%q ch=%q (dispatching async)", control, b.Param, b.Ch)
		go func() {
			if _, err := client.Send(cmd); err != nil {
				log.Println("send reset_param:", err)
			}
		}()
		return control + " reset"

	case b.IsProperty():
		// Edit-page knob press → reset this knob's property (or gang) back
		// to its Resolve default (Crop*→0, Zoom*→1.0, Pan/Tilt/Rotation→0,
		// Opacity→100). Script-API path via the sidecar, like the turn
		// (bump_property): no CGEvent, no frontmost/page gate.
		if client == nil {
			log.Printf("[note_on] control=%q property-reset SKIP no-sidecar", control)
			return ""
		}
		cmd := map[string]any{"op": "reset_property"}
		if len(b.Gang) > 0 {
			cmd["gang"] = b.Gang
		} else {
			cmd["property"] = b.Property
		}
		if activeTrack != 0 {
			cmd["track"] = activeTrack
		}
		log.Printf("[note_on] control=%q property-reset property=%q gang=%v track=%d (dispatching async)", control, b.Property, b.Gang, activeTrack)
		// Own goroutine, same rationale as the IsCDL() reset_param send
		// just above -- a property reset converges on the same fixed
		// default regardless of send order, so it carries none of the
		// marker paths' cross-control-state risk.
		go func() {
			if _, err := client.Send(cmd); err != nil {
				log.Println("send reset_property:", err)
			}
		}()
		return propertyLabel(b) + " reset"

	case b.IsClick():
		if b.Mode != "" {
			prev := modeTracker.Current()
			modeTracker.Set(b.Mode)
			log.Printf("[note_on] control=%q click mode %q→%q", control, prev, modeTracker.Current())
		}
		if cal == nil {
			log.Printf("[note_on] control=%q click SKIP no-calibration point=%q", control, b.Point)
			return ""
		}
		frontmostID := macapp.FrontmostBundleID()
		frontmost := macapp.BundleIDIsResolve(frontmostID)
		if !frontmost {
			log.Printf("[note_on] control=%q click SKIP resolve-not-frontmost point=%q", control, b.Point)
			return ""
		}
		pt, ptOk := cal.Point(b.Point)
		if !ptOk {
			log.Printf("[note_on] control=%q click SKIP calib-missing-point point=%q (re-run --calibrate)", control, b.Point)
			return ""
		}
		log.Printf("[note_on] control=%q click point=%q at (%.1f,%.1f)", control, b.Point, pt.X, pt.Y)
		mouse.Click(pt.X, pt.Y)
		if b.Mode != "" {
			return strings.ToLower(b.Mode)
		}
		return control

	case b.IsKey():
		frontmostID := macapp.FrontmostBundleID()
		frontmost := macapp.BundleIDIsResolve(frontmostID)
		if !frontmost {
			log.Printf("[note_on] control=%q key SKIP resolve-not-frontmost key=%q", control, b.Key)
			return ""
		}
		log.Printf("[note_on] control=%q key=%q", control, b.Key)
		if err := keys.Send(b.Key); err != nil {
			log.Printf("[note_on] control=%q key=%q error: %v", control, b.Key, err)
		}
		return control

	case b.IsResetSelected():
		// Universal reset: resets whatever target is currently selected.
		//
		// If _config.resetTargets defines an entry for the active target, it
		// wins and is dispatched by type, page-gated. Otherwise this falls
		// through to the legacy wheel path below — which is what keeps Col
		// byte-identical to the old resetActiveWheel while no table is
		// configured (the shipped state today).
		if rt, found := lookupResetTarget(cfg, targetName(aw)); found {
			page := queryPage()
			if !targetValidForPage(rt, page) {
				log.Printf("[note_on] control=%q resetSelected SKIP target=%q type=%q not valid on page=%q",
					control, targetName(aw), rt.Type, page)
				return ""
			}
			switch rt.Type {
			case "click":
				if cal == nil {
					log.Printf("[note_on] control=%q resetSelected SKIP no-calibration point=%q", control, rt.Point)
					return ""
				}
				frontmostID := macapp.FrontmostBundleID()
				if !gate.Allow(macapp.BundleIDIsResolve(frontmostID), page) {
					log.Printf("[note_on] control=%q resetSelected SKIP gate", control)
					return ""
				}
				pt, ok := cal.Point(rt.Point)
				if !ok {
					log.Printf("[note_on] control=%q resetSelected SKIP calib-missing-point %q (re-run --calibrate)", control, rt.Point)
					return ""
				}
				log.Printf("[note_on] control=%q resetSelected click point=%q at (%.1f,%.1f)", control, rt.Point, pt.X, pt.Y)
				mouse.Click(pt.X, pt.Y)
				return "Reset " + targetName(aw)
			case "resetParam":
				if client == nil {
					log.Printf("[note_on] control=%q resetSelected SKIP no-sidecar", control)
					return ""
				}
				cmd := map[string]any{"op": "reset_param", "node": node, "param": rt.Param, "ch": rt.Ch}
				log.Printf("[note_on] control=%q resetSelected reset_param param=%q ch=%q (dispatching async)", control, rt.Param, rt.Ch)
				// Own goroutine -- same reasoning as the IsCDL() case
				// above (converges on a fixed default regardless of send
				// order, no local state another control reads).
				go func() {
					if _, err := client.Send(cmd); err != nil {
						log.Println("send reset_param:", err)
					}
				}()
				return "Reset " + targetName(aw)
			case "resetProperty":
				if client == nil {
					log.Printf("[note_on] control=%q resetSelected SKIP no-sidecar", control)
					return ""
				}
				// 2026-08-12 dispatch-generalization pass: this is the
				// HEAVIEST of the safe-to-generalize paths -- one press can
				// chain MULTIPLE sequential client.Send calls (one per
				// rt.Properties entry, the "gang" reset case), all on the
				// MIDI-callback goroutine before this fix. Own goroutine,
				// same reasoning as IsCDL()/the resetParam case just above:
				// rt.Properties is read-only config (never mutated after
				// load, safe to close over), each send resets its property
				// to a FIXED default, and none of them write any local
				// Engine state another control's dispatch reads -- so the
				// whole loop can run out-of-band exactly like a single
				// reset can, with the same "order doesn't change the
				// converged-to value" property carrying it.
				go func() {
					for _, prop := range rt.Properties {
						cmd := map[string]any{"op": "reset_property", "property": prop}
						if activeTrack != 0 {
							cmd["track"] = activeTrack
						}
						if _, err := client.Send(cmd); err != nil {
							log.Println("send reset_property:", err)
						}
					}
				}()
				log.Printf("[note_on] control=%q resetSelected reset_property props=%v track=%d (dispatching async)", control, rt.Properties, activeTrack)
				return "Reset " + targetName(aw)
			}
		}

		// Legacy wheel path: double-click R, G, B for the active wheel
		// (3 sequential double-clicks). Requires Resolve frontmost + Color page.
		if cal == nil {
			log.Printf("[note_on] control=%q resetActiveWheel SKIP no-calibration", control)
			return ""
		}
		frontmostID := macapp.FrontmostBundleID()
		frontmost := macapp.BundleIDIsResolve(frontmostID)
		page := queryPage()
		if !gate.Allow(frontmost, page) {
			log.Printf("[note_on] control=%q resetActiveWheel SKIP gate frontmost=%v page=%q", control, frontmost, page)
			return ""
		}
		wheel := "lift"
		if aw != nil {
			wheel = aw.Get()
		}
		log.Printf("[note_on] control=%q resetActiveWheel wheel=%q (3 double-clicks: R, G, B)", control, wheel)
		executeResetWheel(wheel, cal)
		return "Reset " + humanWheelName(wheel)

	case b.IsColorByRenderState():
		// Own goroutine, same reason as IsQuickExport below: a repaint walks
		// the whole timeline through the sidecar and must never hold the
		// MIDI-callback goroutine while it does. The button echoes at once;
		// the count lands in the log when the work finishes.
		log.Printf("[note_on] control=%q colorByRenderState (dispatching async)", control)
		go sendColorByRenderState(client, control, b)
		return "Colouring timeline"

	case b.IsToggleHudAndColor():
		// Two jobs on one button: hide the HUD, then on the press that brings
		// it back, repaint the timeline too. The HUD reappearing is already a
		// "show me where things stand" gesture, so the repaint rides along.
		//
		// Only on the SHOW half. Painting on the way out would write to the
		// project at the moment you stop looking at the result.
		if toggleHUD == nil {
			log.Printf("[note_on] control=%q toggleHudAndColor SKIP no-callback (headless)", control)
			return ""
		}
		if visible := toggleHUD(); !visible {
			log.Printf("[note_on] control=%q toggleHudAndColor visible=false (no paint)", control)
			return "HUD hidden"
		}
		go sendColorByRenderState(client, control, b)
		return "HUD shown · colouring"

	case b.IsResyncAll():
		// Double-click R/G/B for all 4 wheels: 12 double-clicks total.
		// Requires Resolve frontmost + Color page.
		frontmostID := macapp.FrontmostBundleID()
		frontmost := macapp.BundleIDIsResolve(frontmostID)
		page := queryPage()
		if !gate.Allow(frontmost, page) {
			log.Printf("[note_on] control=%q resyncAll SKIP gate frontmost=%v page=%q", control, frontmost, page)
			return ""
		}
		log.Printf("[note_on] control=%q resyncAll firing double-click reset on all 4 wheels (12 actions)", control)
		for i, wheel := range []string{"lift", "gamma", "gain", "offset"} {
			if i > 0 {
				time.Sleep(50 * time.Millisecond)
			}
			executeResetWheel(wheel, cal)
		}
		log.Printf("[note_on] control=%q resyncAll complete", control)
		return "Resync all"

	case b.IsFineModeHold():
		// fineModeHold: note_on activates fine mode. The caller handles
		// note_off. In production this case is actually unreachable --
		// Start()'s note_on handler intercepts fineModeHold directly
		// (atomic.StoreInt32(&fineModeAtomic, 1) + its own setLastAction
		// call) BEFORE dispatchNoteOn is ever called -- but a direct unit
		// test calling dispatchNoteOn with a fineModeHold binding still
		// gets a sensible return value.
		log.Printf("[note_on] control=%q fineModeHold pressed (fine mode ON)", control)
		return "Fine mode on"

	case b.IsSelectTarget():
		// "selectWheel" is accepted as an alias for "selectTarget", and
		// TargetName() reads Wheel when Target is unset — so every existing
		// config keeps working with no edits.
		name := b.TargetName()
		if aw != nil {
			aw.Set(name)
		}
		log.Printf("[note_on] control=%q selectTarget target=%q", control, name)
		if aw == nil {
			return ""
		}
		return "Target: " + humanWheelName(name)

	case b.IsBalance():
		// Balance bindings produce no action on note_on (turn-only).
		log.Printf("[note_on] control=%q balance hue=%d no-action-on-press", control, b.Hue)
		return ""

	case b.IsQuickExport():
		// C5: folder-picker (remembered across presses) + render, via the
		// sidecar's script API. Runs in its OWN goroutine so a slow or
		// cancelled folder-picker dialog can never block the MIDI-callback
		// goroutine that called dispatchNoteOn — this was the "freeze all
		// buttons" bug identified in review. Script-API op: no arming, no
		// Resolve-frontmost/Color gate (unlike Copy/Paste's CGEvent path).
		// The returned LastAction ("Quick Export") is the immediate,
		// synchronous "button was pressed" echo -- dispatchQuickExport's
		// own eventual success/failure resolves later, asynchronously, and
		// is intentionally NOT threaded back into LastAction (would need a
		// callback param on a fire-and-forget goroutine; kept out of scope
		// to control blast radius, see this task's build notes).
		log.Printf("[note_on] control=%q quickexport (dispatching async)", control)
		go dispatchQuickExport(client, chooseFolder, getLastFolder, setLastFolder)
		return "Quick Export"

	case b.IsToggleHud():
		// C6: toggles the native HUD status-monitor panel's visibility. Pure
		// UI action -- no CGEvent, no arming, no Resolve-frontmost/page gate
		// (same ungated posture as IsQuickExport() above, for the same
		// reason: this isn't a Resolve action, so there's nothing to gate).
		// toggleHUD is nil under the headless CLI (cmd/2t5-deck-plus/main.go
		// never calls SetToggleHUDCallback -- there is no NSApplication
		// there for an NSPanel to attach to), so this is a safe no-op in
		// that case. toggleHUD() itself is synchronous (the Go-side
		// `visible` flag flips before the AppKit dispatch_async fires --
		// see internal/hud/hud_darwin.go), so the returned bool is
		// immediately trustworthy here.
		if toggleHUD == nil {
			log.Printf("[note_on] control=%q toggleHud SKIP no-callback (headless)", control)
			return ""
		}
		visible := toggleHUD()
		log.Printf("[note_on] control=%q toggleHud visible=%v", control, visible)
		if visible {
			return "HUD shown"
		}
		return "HUD hidden"
	}
	return ""
}

// dispatchQuickExport implements the C5 Quick Export button's body: use
// the remembered target folder if one exists, otherwise prompt
// chooseFolder (and remember its result via setLastFolder), then send a
// "render" op to the sidecar. Extracted as its own function — separate
// from the `go` call in dispatchNoteOn's IsQuickExport() case — so it can
// be unit-tested directly (synchronously) without needing to prove
// asynchrony in every test; TestDispatchNoteOn_QuickExport_RunsAsync_
// DoesNotBlock covers the asynchrony property specifically.
func dispatchQuickExport(
	client *ipc.Client,
	chooseFolder func() (string, error),
	getLastFolder func() string,
	setLastFolder func(string),
) {
	folder := getLastFolder()
	if folder == "" {
		f, err := chooseFolder()
		if err != nil {
			log.Printf("[quickexport] folder picker error: %v", err)
			return
		}
		if f == "" {
			log.Printf("[quickexport] folder picker cancelled")
			return
		}
		folder = f
		setLastFolder(folder)
	}
	if client == nil {
		log.Printf("[quickexport] SKIP no-sidecar folder=%q", folder)
		return
	}
	log.Printf("[quickexport] rendering to folder=%q", folder)
	resp, err := client.Send(map[string]any{"op": "render", "target_dir": folder})
	if err != nil {
		log.Printf("[quickexport] send render error: %v", err)
		return
	}
	if ok, _ := resp["ok"].(bool); !ok {
		log.Printf("[quickexport] render failed: %v", resp["err"])
	}
}

// processNonBalanceDrain handles a non-balance drain event (wheel, cdl).
//
// activeTrack is the D1 knob's currently-selected video track number (0 if
// unset/"auto") — only consulted by the "property" branch, where it is
// forwarded to the sidecar's bump_property op as "track" so the bump
// targets that specific track's clip instead of GetCurrentVideoItem().
// Every other branch (wheel/cdl/jog) ignores it — jog is a playhead-global
// scrub with no per-clip target concept.
//
// sustainSnapSeconds, when >0, OVERRIDES the speed-tier tc.JogTier result
// for the "jog" branch — the caller (Start()'s drain loop) computes this
// once per tick from the ControlDial's continuous-dial streak duration
// (see internal/tc.JogStreak/SustainSnapSeconds): dial continuously for
// >= the binding's JogSustainMs and every jog op forces a
// JogSustainSeconds snap, regardless of that tick's own |step| speed. 0
// means "no override, use JogTier's tier unchanged" — every other branch
// (wheel/cdl/property/selectClip) ignores this parameter entirely.
//
// Returns a human-readable LastAction summary of the action actually
// dispatched (see Engine.setLastAction's doc comment), or "" for every
// SKIP/no-op path (including the final "click and key bindings produce no
// output" fallthrough).
func processNonBalanceDrain(
	control string,
	step int,
	b config.Binding,
	modeTracker *mode.Tracker,
	cal *calib.Calib,
	client *ipc.Client,
	r *router.Router,
	bank string,
	cfg *config.Config,
	queryPage func() string,
	activeTrack int,
	sustainSnapSeconds int,
) string {
	if b.IsWheel() {
		if !modeTracker.AllowKnobAction() {
			log.Printf("[drain] control=%q step=%d method=wheel SKIP mode=%q", control, step, modeTracker.Current())
			return ""
		}
		if cal == nil {
			log.Printf("[drain] control=%q step=%d method=wheel SKIP no-calibration", control, step)
			return ""
		}
		pt, ptOk := cal.Point(b.Wheel)
		if !ptOk {
			log.Printf("[drain] control=%q step=%d method=wheel SKIP calib-missing-point wheel=%q (run --calibrate)", control, step, b.Wheel)
			return ""
		}
		frontmostID := macapp.FrontmostBundleID()
		frontmost := macapp.BundleIDIsResolve(frontmostID)
		page := queryPage()
		allowed := gate.Allow(frontmost, page)
		sens := b.Sens
		if sens == 0 {
			sens = defaultWheelSens
		}
		capPx := b.MaxDrag
		if capPx == 0 {
			capPx = maxDragPx
		}
		rawDelta := float64(step) * sens
		clampedDelta := dragmath.Clamp(rawDelta, capPx)
		var x2, y2 float64
		if clampedDelta >= 0 {
			x2, y2 = dragmath.Endpoint(pt.X, pt.Y, pt.Axis, 1, clampedDelta)
		} else {
			x2, y2 = dragmath.Endpoint(pt.X, pt.Y, pt.Axis, -1, -clampedDelta)
		}
		log.Printf("[drain] control=%q step=%d method=wheel frontmost=%v frontmostID=%q page=%q gateAllow=%v dragFrom=(%.1f,%.1f) dragTo=(%.1f,%.1f) dispatched=%v",
			control, step, frontmost, frontmostID, page, allowed, pt.X, pt.Y, x2, y2, allowed)
		if !allowed {
			return ""
		}
		mouse.Drag(pt.X, pt.Y, x2, y2)
		return fmt.Sprintf("%s %+d", humanWheelName(b.Wheel), step)
	}

	if b.IsHueCurve() {
		return dispatchHueCurve(control, step, b, modeTracker, cal, queryPage)
	}

	if b.IsCDL() {
		if !modeTracker.AllowKnobAction() {
			log.Printf("[drain] control=%q step=%d method=cdl SKIP mode=%q", control, step, modeTracker.Current())
			return ""
		}
		if client == nil {
			log.Printf("[drain] control=%q step=%d method=cdl SKIP no-sidecar", control, step)
			return ""
		}
		if cmd, ok := r.Route(bank, control, step); ok {
			if _, err := client.Send(cmd); err != nil {
				log.Println("send:", err)
			}
			return fmt.Sprintf("%s %+.2f", control, cmd.Amt)
		}
		return ""
	}

	if b.IsProperty() {
		// Script-API op, not CGEvent: no modeTracker/gate check (those are
		// Color-page-specific concepts — this fires whenever the caller
		// already resolved bank=="edit", i.e. page=="edit"; see Start()'s
		// page-aware bank resolution).
		if client == nil {
			log.Printf("[drain] control=%q step=%d method=property SKIP no-sidecar", control, step)
			return ""
		}
		sens := b.Sens
		if sens == 0 {
			sens = defaultPropertySens
		}
		rawDelta := float64(step) * sens
		if b.MaxDelta > 0 {
			rawDelta = dragmath.Clamp(rawDelta, b.MaxDelta)
		}
		cmd := map[string]any{
			"op":    "bump_property",
			"delta": rawDelta,
		}
		if len(b.Gang) > 0 {
			cmd["gang"] = b.Gang
		} else {
			cmd["property"] = b.Property
		}
		if b.Min != nil {
			cmd["min"] = *b.Min
		}
		if b.Max != nil {
			cmd["max"] = *b.Max
		}
		if activeTrack != 0 {
			cmd["track"] = activeTrack
		}
		log.Printf("[drain] control=%q step=%d method=property property=%q gang=%v delta=%.4f track=%d",
			control, step, b.Property, b.Gang, rawDelta, activeTrack)
		if _, err := client.Send(cmd); err != nil {
			log.Println("send bump_property:", err)
		}
		return fmt.Sprintf("%s %+.2f", propertyLabel(b), rawDelta)
	}

	if b.IsJog() {
		if client == nil {
			log.Printf("[drain] control=%q step=%d method=jog SKIP no-sidecar", control, step)
			return ""
		}
		// frames is the FINE-tier frame delta (unchanged behavior) --
		// always computed, since a fast spin's snap command also carries
		// it as a fallback (see below).
		frames := tc.AccelerateWith(step, b.Base, b.Gamma)
		if b.MaxDelta > 0 {
			frames = int(dragmath.Clamp(float64(frames), b.MaxDelta))
		}

		absStep := step
		if absStep < 0 {
			absStep = -absStep
		}
		snapSeconds := tc.JogTier(absStep, b.JogTier5Min, b.JogTier10Min, b.JogTier30Min)

		// Sustained-spin escalation: a continuous dial streak (tracked by
		// the caller across drain ticks) overrides the per-tick speed
		// tier entirely — even a slow, steady dial that would normally
		// stay in the FINE tier (snapSeconds==0) escalates to the
		// sustain tier once the streak crosses the threshold.
		if sustainSnapSeconds > 0 {
			snapSeconds = sustainSnapSeconds
		}

		if snapSeconds == 0 {
			// Slow dial: fine frame-by-frame scrub, unchanged from before
			// the speed-tiered jog feature.
			if frames == 0 {
				return ""
			}
			log.Printf("[drain] control=%q step=%d method=jog frames=%d", control, step, frames)
			if _, err := client.Send(map[string]any{"op": "jog", "frames": frames}); err != nil {
				log.Println("send jog:", err)
			}
			return fmt.Sprintf("Jog %+df", frames)
		}

		// Fast dial: snap the playhead to the next round snapSeconds-second
		// timecode boundary in the spin direction. Only the sidecar knows
		// the live timeline's current TC/fps (see resolve_driver.py's
		// jog_snap), so Go just requests the tier + direction — plus the
		// SAME fine-tier "frames" value as a fallback the sidecar uses if
		// the live timeline is drop-frame or non-integer-fps (can't be
		// known from here).
		dir := 1
		if step < 0 {
			dir = -1
		}
		log.Printf("[drain] control=%q step=%d method=jog snap=%ds dir=%d frames=%d(fallback)", control, step, snapSeconds, dir, frames)
		if _, err := client.Send(map[string]any{
			"op":           "jog",
			"snap_seconds": snapSeconds,
			"dir":          dir,
			"frames":       frames,
		}); err != nil {
			log.Println("send jog snap:", err)
		}
		return fmt.Sprintf("Jog %+ds", snapSeconds*dir)
	}
	// click and key bindings produce no output from knob-turn events.
	return ""
}

// dispatchBalanceStep processes one balance flush step using the RGB-scrubber
// mechanic. For each channel (R, G, B), it computes delta_px = perTick ×
// component × pxPerUnit, clamps it, and fires a horizontal Press→DragTo→Release
// scrub on the calibrated value box. Channels whose |delta_px| < minScrubPx are
// skipped.
//
// The three scrubs are sequential: R, then G, then B (~20ms between each).
//
// This function is extracted from the drain loop so it can be unit-tested
// without CGO or a live display.
//
// Returns a human-readable LastAction summary ("<Wheel> <±step>", same
// shape as processNonBalanceDrain's wheel branch) for the balance flush as
// a whole, or "" when the top-level allowed/modeOK/cal guards refuse the
// entire flush. Individual per-channel skips (delta below MinScrubPx, a
// missing calibration box) do not suppress the returned summary — some
// channels may still have been silently skipped even when a non-empty
// string is returned, mirroring this function's existing per-channel log
// lines.
func dispatchBalanceStep(
	control string,
	step int,
	b config.Binding,
	allowed bool,
	modeOK bool,
	cal *calib.Calib,
	aw *target.State,
	tl config.TopLevel,
	fineMode bool,
) string {
	if !allowed {
		log.Printf("[drain] control=%q step=%d method=balance SKIP gate", control, step)
		return ""
	}
	if !modeOK {
		log.Printf("[drain] control=%q step=%d method=balance SKIP mode", control, step)
		return ""
	}
	if cal == nil {
		log.Printf("[drain] control=%q step=%d method=balance SKIP no-calibration", control, step)
		return ""
	}

	wheel := aw.Get()

	// Compute sens (binding → per-wheel mult → fine mode).
	sens := b.Sens
	if sens == 0 {
		sens = defaultWheelSens
	}
	if mult, ok := tl.WheelSensMultipliers[wheel]; ok && mult > 0 {
		sens *= mult
	}
	if fineMode && tl.FineFactor > 0 {
		sens *= tl.FineFactor
	}

	// Per-tick base magnitude (before per-channel component weighting).
	perTick := float64(step) * sens

	// Lookup hue angle θ from binding hue index.
	hue := b.Hue
	if hue < 0 || hue >= 8 {
		hue = 0
	}
	θ := tl.HueOffsetsDeg[hue]

	// Decompose into RGB components.
	rComp, gComp, bComp := rgbmix.RGBVector(θ)

	type chanScrub struct {
		ch   string
		comp float64
	}
	channels := []chanScrub{
		{"R", rComp},
		{"G", gComp},
		{"B", bComp},
	}

	for i, cs := range channels {
		delta := perTick * cs.comp * tl.PxPerUnit
		delta = dragmath.Clamp(delta, tl.MaxScrubPx)
		if delta < 0 {
			delta = -dragmath.Clamp(-delta, tl.MaxScrubPx)
		}
		if delta < tl.MinScrubPx && delta > -tl.MinScrubPx {
			log.Printf("[drain] control=%q method=balance wheel=%q hue=%.0f° channel=%s delta_px=%.3f < minScrubPx=%.1f — skip",
				control, wheel, θ, cs.ch, delta, tl.MinScrubPx)
			continue
		}

		boxKey := wheel + "_" + cs.ch
		pt, ok := cal.Point(boxKey)
		if !ok {
			log.Printf("[drain] control=%q method=balance SKIP calib-missing-point box=%q (run --calibrate)", control, boxKey)
			continue
		}

		if i > 0 {
			time.Sleep(20 * time.Millisecond)
		}

		log.Printf("[drain] control=%q method=balance wheel=%q hue=%.0f° channel=%s comp=%.4f perTick=%.3f delta_px=%.2f fineMode=%v scrub at (%.1f,%.1f)",
			control, wheel, θ, cs.ch, cs.comp, perTick, delta, fineMode, pt.X, pt.Y)
		mouseScrubFn(pt.X, pt.Y, delta)
	}
	return fmt.Sprintf("%s %+d", humanWheelName(wheel), step)
}

// ---------------------------------------------------------------------------
// D1 "clip-select" feature: cycle which video track's clip the Edit-page
// property/reset dials target, purely via the sidecar's script-API
// clips_at_playhead op (no CGEvent, no calibration — Resolve has no
// "select clip" API, so there is no on-screen highlight; the app's Status
// UI shows the active target instead — see Status.ActiveClip below).
// ---------------------------------------------------------------------------

// clipEntry is one occupied-video-track entry returned by the sidecar's
// clips_at_playhead op: the track number and the clip's name.
type clipEntry struct {
	Track int
	Name  string
}

// parseClipsAtPlayhead decodes a clips_at_playhead sidecar response
// (resp["ok"], resp["clips"]) into a typed, ordered []clipEntry. Returns
// ok=false (nil clips) when resp is nil or resp["ok"] is not true; malformed
// individual entries (not a JSON object) are skipped rather than aborting
// the whole parse. Never panics on an unexpected/absent shape — callers
// treat ok=false or an empty slice identically (safe no-op).
func parseClipsAtPlayhead(resp map[string]any) (ok bool, clips []clipEntry) {
	if resp == nil {
		return false, nil
	}
	if v, _ := resp["ok"].(bool); !v {
		return false, nil
	}
	raw, _ := resp["clips"].([]any)
	for _, r := range raw {
		m, mok := r.(map[string]any)
		if !mok {
			continue
		}
		tf, _ := m["track"].(float64)
		name, _ := m["name"].(string)
		clips = append(clips, clipEntry{Track: int(tf), Name: name})
	}
	return true, clips
}

// nextTrackIndex returns the index into clips that the D1 rotation should
// move to, given the CURRENTLY active track number (0/unset if none) and a
// direction (+1 up toward higher track numbers, -1 down). BIDIRECTIONAL and
// CLAMPED to the ends of clips — NO WRAP: dialing past the top simply stops
// on the top clip, dialing past the bottom stops on the bottom clip. This is
// the restored, user-confirmed-desired behavior ("it was stopping in the
// bottom or top clips each direction") — an earlier revision of this
// function had drifted to wrapping (top -> bottom, bottom -> top) despite
// this doc comment already documenting clamp; that drift is the fix here,
// not a new design.
//
// If current is not present in clips (0/unset, or the playhead moved off
// that track since D1 was last turned), the move starts from "just before
// the first entry" (conceptual index -1) — an upward turn lands on the
// first (lowest-numbered) occupied track, and a downward turn clamps to
// that same first entry. This is a deterministic, documented default
// rather than an attempt to guess which track the user "meant".
//
// Returns -1 (a sentinel, never a valid slice index) if clips is empty —
// callers (dispatchSelectClip) must check len(clips) before calling.
func nextTrackIndex(clips []clipEntry, current int, dir int) int {
	if len(clips) == 0 {
		return -1
	}
	idx := -1
	for i, c := range clips {
		if c.Track == current {
			idx = i
			break
		}
	}
	n := len(clips)
	if idx == -1 {
		// No current selection yet: up -> first occupied clip, down -> last.
		if dir < 0 {
			return n - 1
		}
		return 0
	}
	// Clamp at both ends -- no modulo/wrap. Spinning past the top/bottom
	// simply stops there.
	next := idx + dir
	if next < 0 {
		return 0
	}
	if next >= n {
		return n - 1
	}
	return next
}

// ---------------------------------------------------------------------------
// D1 "selectClip" sensitivity: selectClipDetent accumulates raw per-tick
// relative-CC steps and only "fires" (returns a nonzero direction) once the
// accumulated magnitude crosses a threshold — see defaultSelectClipDetentThreshold
// and config.Binding.DetentThreshold's doc comments for WHY (the physical
// encoder emits several raw steps per felt detent).
// ---------------------------------------------------------------------------

// selectClipDetent is a small, pure, stateful accumulator owned exclusively
// by the drain/tick goroutine (same posture as jogStreak/balanceAccum in
// Start() — no mutex needed). Zero value is immediately usable (acc starts
// at 0).
type selectClipDetent struct {
	acc int
}

// add accumulates step into the detent's running total and returns the
// direction to fire THIS call: -1, 0, or +1 — never more than one clip's
// worth of movement, even if step (or the accumulated total) is far past
// threshold ("no multi-jump per turn"). threshold<=0 is treated as 1 (fire
// on every nonzero step — restores the original, pre-tuning behavior;
// mainly useful for tests of something other than the sensitivity curve
// itself).
//
// Firing RESETS acc to 0 (rather than subtracting threshold and carrying
// the remainder forward) — a deliberate debounce: build up to threshold,
// fire once, start counting fresh. This is what guarantees a single fast
// burst (step alone >= threshold, or several already-accumulated ticks
// crossing it at once) can never fire twice in a row from leftover
// overshoot.
func (d *selectClipDetent) add(step, threshold int) int {
	if threshold <= 0 {
		threshold = 1
	}
	d.acc += step
	switch {
	case d.acc >= threshold:
		d.acc = 0
		return 1
	case d.acc <= -threshold:
		d.acc = 0
		return -1
	}
	return 0
}

// reset zeroes the accumulator — called whenever D1's activeTrack is (or
// already is) unset/"auto", so a stale sub-threshold accumulation left over
// from a just-cleared selection (D1 press-release, or the clip-id auto-
// reset) never leaks into the NEXT selection gesture. Safe/idempotent to
// call every tick while activeTrack is 0 — a no-op once acc is already 0.
func (d *selectClipDetent) reset() {
	d.acc = 0
}

// dispatchHighlightClip sends the sidecar's highlight_clip op so Resolve
// visibly tints the D1-selected clip's color — the on-screen "active clip"
// feedback Status.ActiveClip's text alone can't provide (Resolve has no
// clip-selection-highlight API). The sidecar (ResolveDriver.highlight_clip)
// owns the "restore the previously-tinted clip first" logic — this
// function only sends the op for the NEW track; it never needs to know
// what was previously tinted.
//
// A nil client or track==0 is a silent no-op. track==0 should never
// actually reach here in practice (dispatchSelectClip only calls its
// setActiveTrack callback with a real occupied track number), but the
// guard is kept for the same defensive reasons as dispatchSelectClip's own
// nil-client check.
func dispatchHighlightClip(client *ipc.Client, track int, color string) {
	if client == nil || track == 0 {
		return
	}
	log.Printf("[drain] control=D1 method=highlight track=%d color=%q", track, color)
	resp, err := client.Send(map[string]any{"op": "highlight_clip", "track": track, "color": color})
	if err != nil {
		log.Printf("[drain] control=D1 method=highlight send error: %v", err)
		return
	}
	if ok, _ := resp["ok"].(bool); !ok {
		log.Printf("[drain] control=D1 method=highlight failed: %v", resp["err"])
	}
}

// dispatchSelectClip handles the D1 knob's "selectClip" rotation: queries
// the sidecar for every occupied video track under the playhead
// (clips_at_playhead), finds the CURRENTLY active track's position in that
// ordered list via nextTrackIndex, and calls setActiveTrack with the newly
// selected track's number. A nil client, step==0, a failed/not-ok sidecar
// response, or an empty occupied list are all silent no-ops — activeTrack
// is left unchanged.
//
// Returns a human-readable LastAction summary ("D1 → V<track>") when a
// track was actually selected, or "" for every no-op path above.
func dispatchSelectClip(
	step int,
	client *ipc.Client,
	getActiveTrack func() int,
	setActiveTrack func(int),
) string {
	if client == nil {
		log.Printf("[drain] control=D1 method=selectClip SKIP no-sidecar")
		return ""
	}
	if step == 0 {
		return ""
	}
	resp, err := client.Send(map[string]any{"op": "clips_at_playhead"})
	if err != nil {
		log.Printf("[drain] control=D1 method=selectClip send clips_at_playhead error: %v", err)
		return ""
	}
	ok, clips := parseClipsAtPlayhead(resp)
	if !ok || len(clips) == 0 {
		log.Printf("[drain] control=D1 method=selectClip no occupied video tracks under playhead (ok=%v)", ok)
		return ""
	}

	dir := 1
	if step < 0 {
		dir = -1
	}
	current := getActiveTrack()
	newIdx := nextTrackIndex(clips, current, dir)
	newTrack := clips[newIdx].Track
	setActiveTrack(newTrack)
	log.Printf("[drain] control=D1 method=selectClip step=%d current=%d -> track=%d clip=%q",
		step, current, newTrack, clips[newIdx].Name)
	return fmt.Sprintf("D1 → V%d", newTrack)
}

// ---------------------------------------------------------------------------
// D1 "jogSeconds": coarse playhead jump, the coarse counterpart to the
// ControlDial's fine "jog" binding. Reuses selectClipDetent (the identical
// accumulator type/pattern the D1 selectClip binding above already proved)
// to debounce the physical encoder's oversensitive raw-step rate down to
// ONE jump per firm detent -- see the drain loop's b.IsJogSeconds() case in
// Start(), and jogSecondsAccum's own doc comment there. Unlike selectClip,
// this feature has no per-clip target concept at all -- no activeTrack, no
// selection state, no auto-reset wiring of any kind.
// ---------------------------------------------------------------------------

// dispatchJogSeconds implements D1's "jogSeconds" coarse playhead-jump
// binding: fires ONE ±secondsPerStep jump via the sidecar's jog_seconds
// op, in the direction already resolved by the drain loop's detent
// accumulator (fireDir, -1 or +1 -- 0 must never reach here, the same
// precondition dispatchSelectClip's step argument has for its own callers).
// Script-API only (no CGEvent, no gate) -- same defensive posture as
// dispatchSelectClip/dispatchAddMarker above: a nil client SKIPs
// gracefully like every other dispatch* helper in this file.
//
// Returns a human-readable LastAction summary ("D1 jog +60s"/"D1 jog
// -60s") on a send attempt, or "" when skipped (no-sidecar).
func dispatchJogSeconds(fireDir int, client *ipc.Client, secondsPerStep float64) string {
	if client == nil {
		log.Printf("[drain] control=D1 method=jogSeconds SKIP no-sidecar")
		return ""
	}
	seconds := float64(fireDir) * secondsPerStep
	log.Printf("[drain] control=D1 method=jogSeconds dir=%d secondsPerStep=%.1f seconds=%+.1f", fireDir, secondsPerStep, seconds)
	if _, err := client.Send(map[string]any{"op": "jog_seconds", "seconds": seconds}); err != nil {
		log.Println("send jog_seconds:", err)
	}
	return fmt.Sprintf("D1 jog %+.0fs", seconds)
}

// ---------------------------------------------------------------------------
// D1 "jogMinuteSnap": the CURRENT D1 binding, superseding "jogSeconds"
// above. Reuses selectClipDetent (the identical accumulator type/pattern
// selectClip/jogSeconds above already proved) to debounce the physical
// encoder's oversensitive raw-step rate down to ONE snap per firm detent
// -- see the drain loop's b.IsJogMinuteSnap() case in Start(), and
// jogMinuteSnapAccum's own doc comment there. Like jogSeconds, this
// feature has no per-clip target concept at all -- no activeTrack, no
// selection state, no auto-reset wiring of any kind. Unlike jogSeconds,
// the destination is not a fixed offset from the CURRENT playhead
// position -- it's always the next/previous point on the whole-minute
// timecode grid, computed sidecar-side (see ResolveDriver.jog_minute_snap).
// ---------------------------------------------------------------------------

// dispatchJogMinuteSnap implements D1's "jogMinuteSnap" whole-minute
// playhead-snap binding: fires ONE snap via the sidecar's jog_minute_snap
// op, in the direction already resolved by the drain loop's detent
// accumulator (fireDir, -1 or +1 -- 0 must never reach here, the same
// precondition dispatchSelectClip/dispatchJogSeconds's own fireDir
// arguments have for their callers). Script-API only (no CGEvent, no
// gate) -- same defensive posture as every other dispatch* helper in this
// file: a nil client SKIPs gracefully.
//
// Returns a human-readable LastAction summary ("D1 snap +1min"/"D1 snap
// -1min") on a send attempt, or "" when skipped (no-sidecar). Does not
// inspect the sidecar's response "tc" field for the LastAction text --
// same fire-and-forget posture as dispatchJogSeconds (which this binding
// replaces on D1): the destination boundary is a deterministic function of
// direction alone (the sidecar does the actual frame math against the
// live timeline), so no response round-trip is needed just to describe
// which way the snap went.
func dispatchJogMinuteSnap(fireDir int, client *ipc.Client) string {
	if client == nil {
		log.Printf("[drain] control=D1 method=jogMinuteSnap SKIP no-sidecar")
		return ""
	}
	log.Printf("[drain] control=D1 method=jogMinuteSnap dir=%d", fireDir)
	if _, err := client.Send(map[string]any{"op": "jog_minute_snap", "direction": fireDir}); err != nil {
		log.Println("send jog_minute_snap:", err)
	}
	return fmt.Sprintf("D1 snap %+dmin", fireDir)
}

// ---------------------------------------------------------------------------
// "keyDelta" (2026-08-13): knob rotation -> repeated keystroke. First use:
// Shift+D1 -> Resolve timeline zoom in/out (config/bindings.json's
// edit-shift.D1), but the type itself is control-agnostic -- any turn
// binding can be "keyDelta" via config alone, same posture as "property"/
// "jog". See config.Binding.KeyCW's doc comment for the full design and
// why its accumulator (keyDeltaDetent below) deliberately does NOT reuse
// selectClipDetent's single-fire-per-call cap.
//
// SUPERSEDED ON D1 same day by "scrollDelta" (see below) after live
// testing showed keyDelta's guessed Zoom In/Out shortcuts ("Cmd+="/
// "Cmd+-") only half-worked (Zoom Out fired, Zoom In did not) --
// edit-shift.D1 in the shipped config/bindings.json is scrollDelta now.
// keyDelta itself is UNCHANGED and kept, unreachable in production but
// still directly unit-tested, same posture this file already applies to
// "jogSeconds"/"selectClip" after each was superseded on D1 by something
// newer -- it remains available for any other control that genuinely wants
// a repeated-keystroke turn action.
// ---------------------------------------------------------------------------

// keyDeltaDetent accumulates raw per-tick CC steps for a "keyDelta"
// binding and returns the SIGNED COUNT of firm detents fired this call --
// unlike selectClipDetent.add (which caps at exactly one fire per call,
// "no multi-jump per turn"), this may return a magnitude greater than 1: a
// fast spin that crosses several detents within a single 50ms drain tick
// fires that many keystrokes, consuming exactly count*threshold from the
// running total and leaving any sub-threshold remainder for the next call
// -- same "never lose a partial detent" posture as selectClipDetent, just
// without the single-fire cap. See config.Binding.KeyCW's doc comment for
// why a keyDelta binding's lightweight keystroke action wants this
// behaviour where D1's OTHER turn bindings (selectClip/jogSeconds/
// jogMinuteSnap) deliberately do not.
//
// Owned exclusively by the drain/tick goroutine, same posture as
// selectClipDetent -- no mutex needed.
type keyDeltaDetent struct {
	acc int
}

// add accumulates step into the running total and returns the signed
// detent count to fire THIS call: positive N means N clockwise detents,
// negative N means N counter-clockwise, 0 means none crossed yet.
// threshold<=0 is treated as 1 (fire on every nonzero step), same
// convention as selectClipDetent.add.
func (d *keyDeltaDetent) add(step, threshold int) int {
	if threshold <= 0 {
		threshold = 1
	}
	d.acc += step
	count := 0
	for d.acc >= threshold {
		d.acc -= threshold
		count++
	}
	for d.acc <= -threshold {
		d.acc += threshold
		count--
	}
	return count
}

// keyDeltaKeyAndCount resolves a "keyDelta" fire count (positive =
// clockwise, negative = counter-clockwise, magnitude = detents fired this
// call -- see keyDeltaDetent) to the actual key spec to send and how many
// times. Pure and side-effect-free (no CGEvent, no gate) -- extracted
// specifically so it is directly unit-testable independent of
// dispatchKeyDelta's macapp.BundleIDIsResolve() frontmost gate, which
// (like dispatchNoteOn's IsKey() case) always evaluates false under `go
// test`, making the SIGN/COUNT/empty-key-spec logic otherwise
// unobservable through the gated function alone.
//
// ok is false for count==0, or for an empty key spec on the fired
// direction (a config gap -- KeyCW/KeyCCW are not validated at
// config.Load time, see their doc comment) -- both silent no-ops at the
// caller.
func keyDeltaKeyAndCount(count int, keyCW, keyCCW string) (key string, n int, ok bool) {
	if count == 0 {
		return "", 0, false
	}
	key = keyCW
	n = count
	if count < 0 {
		key = keyCCW
		n = -count
	}
	if key == "" {
		return "", 0, false
	}
	return key, n, true
}

// dispatchKeyDelta implements a "keyDelta" binding: fires |count| copies
// of keyCW (count > 0) or keyCCW (count < 0) via keys.Send, one per
// detent -- see keyDeltaDetent's doc comment for why this, unlike
// selectClip/jogSeconds/jogMinuteSnap, may fire more than once per call.
//
// Gated on Resolve being frontmost -- the SAME check dispatchNoteOn's
// IsKey() case uses for a "key" (button) binding: a knob turn's keystroke
// is exactly as real an OS-level event as a button's, so it gets the
// identical gate, not the property/jog turns' script-API "no gate at all"
// posture (those never post a CGEvent at all).
//
// count==0 and an empty key spec for the fired direction are both silent
// no-ops (via keyDeltaKeyAndCount above), same defensive posture as every
// other dispatch* helper in this file.
func dispatchKeyDelta(control string, count int, keyCW, keyCCW string) string {
	key, n, ok := keyDeltaKeyAndCount(count, keyCW, keyCCW)
	if !ok {
		if count != 0 {
			log.Printf("[drain] control=%q method=keyDelta SKIP empty key spec count=%d", control, count)
		}
		return ""
	}
	frontmostID := macapp.FrontmostBundleID()
	frontmost := macapp.BundleIDIsResolve(frontmostID)
	if !frontmost {
		log.Printf("[drain] control=%q method=keyDelta SKIP resolve-not-frontmost count=%d", control, count)
		return ""
	}
	for i := 0; i < n; i++ {
		if err := keys.Send(key); err != nil {
			log.Printf("[drain] control=%q method=keyDelta key=%q error: %v", control, key, err)
		}
	}
	log.Printf("[drain] control=%q method=keyDelta key=%q x%d", control, key, n)
	return fmt.Sprintf("%s x%d", key, n)
}

// ---------------------------------------------------------------------------
// "scrollDelta" (2026-08-13): knob rotation -> a synthetic scroll-wheel
// CGEvent that carries a modifier flags bitmask DIRECTLY ON THE EVENT
// ITSELF. First use: Shift+D1 -> Alt+scroll -> Resolve's own NATIVE
// timeline zoom (config/bindings.json's edit-shift.D1), superseding
// keyDelta on that control (see keyDelta's section doc comment above for
// why) -- but the type itself is control-agnostic, same posture as
// keyDelta/property/jog.
//
// THE WHOLE POINT of this binding type, and why it exists at all: two
// independent live tests already proved macOS does NOT apply a
// synthetically-HELD AMBIENT modifier (the abandoned modifierHold
// "passthrough" capability -- keys.SendModifierDown/Up posting
// kCGEventFlagsChanged -- see config.Binding's modifierHold doc comment for
// the full built-then-reverted story) to a REAL hardware event such as a
// physical mouse/trackpad scroll. But macOS DOES honour EXPLICIT flags
// carried on a POSTED synthetic event -- exactly the same mechanism that
// already makes every chord keystroke in this app work (keys.Send's parsed
// modifier prefix). So instead of holding a synthetic Alt and hoping a real
// scroll picks it up, this posts a scroll event that ALREADY carries the
// Alt flag -- Resolve then sees a genuine Alt+scroll and zooms the timeline
// NATIVELY, no modifier hold/release/watchdog machinery involved, and no
// need to identify Resolve's Zoom In/Out keyboard shortcuts at all (which
// keyDelta's approach on this same control needed and got only half right).
//
// Reuses keyDeltaDetent (see above) AS-IS for the multi-fire-per-tick
// accumulation -- a fast spin that crosses several detents in one 50ms
// drain tick posts that many scroll events, same "expected feel, not an
// edge case to suppress" rationale keyDelta already established for a
// lightweight per-detent action. scrollDeltaAccum (Start()'s drain-loop
// closure) is its OWN separate map instance from keyDeltaAccum, same
// per-control-name map-of-instances posture, just keyed for this type
// instead.
// ---------------------------------------------------------------------------

// scrollDeltaFromCount resolves a "scrollDelta" fire count (positive =
// clockwise, negative = counter-clockwise, magnitude = detents fired this
// call -- see keyDeltaDetent) and the binding's LinesPerDetent into the
// signed scroll-wheel line delta to post: count * linesPerDetent. Pure and
// side-effect-free (no CGEvent, no gate) -- extracted for the same
// testability reason as keyDeltaKeyAndCount: directly unit-testable
// independent of dispatchScrollDelta's macapp.BundleIDIsResolve() frontmost
// gate.
//
// ok is false for count==0 (nothing fired this call) or linesPerDetent==0.
// linesPerDetent==0 reaching this function is itself a defensive
// impossibility in production (the drain-loop call site below always
// applies defaultLinesPerDetent before calling), not a config-time
// scenario -- config.Binding.LinesPerDetent's own zero/absent case is
// resolved by the CALLER, exactly like DetentThreshold's fallback is,
// keeping this pure function's own contract simple: "0 in still means 0
// out, no default guessing here."
func scrollDeltaFromCount(count, linesPerDetent int) (delta int, ok bool) {
	if count == 0 || linesPerDetent == 0 {
		return 0, false
	}
	return count * linesPerDetent, true
}

// dispatchScrollDelta implements a "scrollDelta" binding: posts one
// mouse.Scroll event per detent fired this call (|count|, see
// scrollDeltaFromCount above), each carrying flagsSpec's parsed modifier
// bitmask (via keys.ParseModifierFlags, the SAME parser Send's own
// "Mod+Key" spec parsing uses) DIRECTLY ON THE EVENT.
//
// Gated on Resolve being frontmost ONLY -- the SAME
// macapp.BundleIDIsResolve(macapp.FrontmostBundleID()) check
// dispatchKeyDelta uses for a knob-turn keystroke, deliberately NOT
// gate.Allow (which additionally requires the Color page -- gate.Allow
// exists for the colour-wheel mouse-DRAG paths; this is an Edit-page
// zoom action and must not inherit that restriction).
//
// A posted scroll event lands on whatever is under the CURRENT mouse
// cursor position -- see mouse.Scroll's doc comment. This function does
// NOT move or warp the cursor to compensate; the caller is expected to
// already have the cursor over Resolve's timeline.
//
// count==0 (nothing fired this call) is a silent no-op (via
// scrollDeltaFromCount). An unparseable flagsSpec (a config typo) logs and
// no-ops rather than posting an unflagged scroll -- same defensive
// posture as every other dispatch* helper in this file, and checked BEFORE
// the frontmost gate (a static config problem, not a live-state one).
func dispatchScrollDelta(control string, count, linesPerDetent int, flagsSpec string) string {
	delta, ok := scrollDeltaFromCount(count, linesPerDetent)
	if !ok {
		return ""
	}
	flags, err := keys.ParseModifierFlags(flagsSpec)
	if err != nil {
		log.Printf("[drain] control=%q method=scrollDelta flags=%q error: %v", control, flagsSpec, err)
		return ""
	}
	frontmostID := macapp.FrontmostBundleID()
	frontmost := macapp.BundleIDIsResolve(frontmostID)
	if !frontmost {
		log.Printf("[drain] control=%q method=scrollDelta SKIP resolve-not-frontmost count=%d", control, count)
		return ""
	}
	mouse.Scroll(delta, flags)
	log.Printf("[drain] control=%q method=scrollDelta delta=%d flags=%#x", control, delta, flags)
	return fmt.Sprintf("scroll %+d (flags=%#x)", delta, flags)
}

// isSelectClipRelease reports whether a note_on event for `control` on the
// current `page` is D1's "release / unselect" action — the mirror of D1's
// TURN (dispatchSelectClip/setActiveTrack), but for the knob's PRESS. True
// only when page=="edit" AND the edit bank binds this exact control as
// "selectClip". Byte-identical false on every other page (D1's "col" bank
// has no binding at all today, so this is unreachable there in practice,
// but the page check is kept explicit rather than relying on that
// absence), and false for any other edit-bank control type — e.g. a
// "property" knob's press already has its own reset_property action via
// dispatchNoteOn's IsProperty() case, which this predicate must never
// shadow.
func isSelectClipRelease(cfg *config.Config, page string, layer int, control string, mod string) bool {
	if page != "edit" {
		return false
	}
	eb, ok := editBankBinding(cfg, page, layer, control, mod)
	return ok && eb.IsSelectClip()
}

// dispatchSelectClipRelease implements D1's knob-PRESS action on the Edit
// page: clears the pinned active-clip target back to "auto"
// (setActiveTrack(0)) and sets EVERY occupied video clip under the
// playhead to a uniform `releaseColor` (e.g. "Blue", from
// cfg.TopLevel.ReleaseColor) via the sidecar's set_clips_color op — a
// clean normalize/deselect gesture. This is DELIBERATELY not
// clear_highlight (which restores the ONE previously-highlighted clip's
// original color and is still used, unchanged, by Engine.Stop()) —
// set_clips_color also clears the D1 highlight-tracking state itself
// (see ResolveDriver.set_clips_color), since after this call every clip
// is the uniform release color and there is nothing left to "restore".
//
// setActiveTrack ALWAYS runs, even with a nil client (nil-client safe, same
// defensive posture as dispatchHighlightClip/dispatchSelectClip above) —
// the sidecar send is best-effort and simply skipped when there's no
// connection, matching every other client==nil guard in this file.
//
// Always returns "D1 release" -- this action unconditionally runs (unlike
// every other dispatch* function in this file, which has at least one
// no-op path), so there is no "" case.
func dispatchSelectClipRelease(client *ipc.Client, setActiveTrack func(int), releaseColor string) string {
	setActiveTrack(0)
	log.Printf("[note_on] control=D1 method=selectClip-release activeTrack -> 0 (auto)")
	if client == nil {
		return "D1 release"
	}
	if _, err := client.Send(map[string]any{"op": "set_clips_color", "color": releaseColor}); err != nil {
		log.Printf("[note_on] control=D1 method=selectClip-release set_clips_color send error: %v", err)
	}
	return "D1 release"
}

// ---------------------------------------------------------------------------
// Direction-button clip selection (Up/Down/Left/Right/C5, Edit page) --
// REPLACES D1's knob-turn selectClip rotation above (D1 itself was freed,
// then later reused for the unrelated "jogSeconds" coarse playhead-jump
// feature -- see that section above; D1 is NEVER bound to "selectClip" in
// the shipped config/bindings.json. nextTrackIndex/dispatchSelectClip/
// selectClipDetent/isSelectClipRelease/dispatchSelectClipRelease above are
// left in place, unreachable in production, still exercised by their own
// direct unit tests).
//
//   - Up/Down ("selectClipVert" binding): step vertically to the next
//     occupied video track above/below the current selection at the
//     playhead, via the sidecar's select_clip_vertical op. Direction
//     ("Up" +1 / "Down" -1) is derived from the control name at dispatch
//     time.
//   - Left/Right ("selectClipHoriz" binding, EDIT PAGE ONLY): select the
//     previous/next clip by start time and move the playhead onto it, via
//     the sidecar's select_clip_horizontal op. These two controls are
//     HARD-WIRED in Start()'s note_on callback to color-page node
//     prev/next navigation on every other page -- the page-aware check
//     lives inline in that switch, not here (see Start()'s "Left"/"Right"
//     cases).
//   - C5 ("selectClipClear" binding): unselects and restores the tinted
//     clip's original color via the sidecar's EXISTING clear_highlight op.
//
// Every dispatch function below keeps Engine.selection and
// activeTrack/getActiveTrack()/setActiveTrack() in LOCKSTEP -- see
// Engine.selection's doc comment for why bump_property/reset_property's
// "track" override must never regress.
// ---------------------------------------------------------------------------

// selectionResult is the parsed response shape shared by the sidecar's
// select_clip_vertical/select_clip_horizontal ops: {ok, err, track,
// unique_id, start, end, original_color}. unique_id/original_color are
// intentionally not parsed here -- Go never consumes them (the sidecar
// owns that bookkeeping entirely via self._highlight, see
// Engine.selection's doc comment) -- only the fields Go's `selection`
// state and lockstep activeTrack actually need.
type selectionResult struct {
	ok    bool
	err   string
	track int
	start int
	end   int
}

// parseSelectionResult decodes a select_clip_vertical/select_clip_horizontal
// sidecar response into a typed selectionResult. Never panics on a
// nil/malformed response -- a missing/wrong-typed field just leaves the
// corresponding zero value, and r.ok stays false unless the response
// explicitly carried "ok":true.
func parseSelectionResult(resp map[string]any) selectionResult {
	var r selectionResult
	if resp == nil {
		return r
	}
	r.ok, _ = resp["ok"].(bool)
	r.err, _ = resp["err"].(string)
	if tf, ok := resp["track"].(float64); ok {
		r.track = int(tf)
	}
	if sf, ok := resp["start"].(float64); ok {
		r.start = int(sf)
	}
	if ef, ok := resp["end"].(float64); ok {
		r.end = int(ef)
	}
	return r
}

// isSelectClipVert reports whether a note_on event for `control`
// ("Up"/"Down") on the current `page` is the direction-button vertical
// clip-selection action -- true only when page=="edit" AND the edit bank
// binds this exact control as "selectClipVert". Same direct-intercept
// shape as isSelectClipRelease/isAddMarkerControl/isJogPressControl/
// isMarkerColorControl above -- resolveNoteBank only shadows
// property/jog/key controls to the edit bank, so without this direct
// check a lookup through the normal noteBank/dispatchNoteOn path would
// simply miss (no col-bank binding exists for Up/Down either).
func isSelectClipVert(cfg *config.Config, page string, layer int, control string, mod string) bool {
	if page != "edit" {
		return false
	}
	eb, ok := editBankBinding(cfg, page, layer, control, mod)
	return ok && eb.IsSelectClipVert()
}

// isSelectClipHorizControl reports whether `control` ("Left"/"Right") is
// bound in the edit bank as "selectClipHoriz" on the current `page` --
// used by Start()'s HARD-WIRED "Left"/"Right" note_on cases (NOT
// resolveNoteBank/dispatchNoteOn, since those two controls have no
// col-bank binding at all and are handled as a special-cased nav switch
// regardless of page) to decide whether to select a clip instead of the
// color-page node prev/next navigation. Same page-gated shape as
// isSelectClipVert above.
func isSelectClipHorizControl(cfg *config.Config, page string, layer int, control string, mod string) bool {
	if page != "edit" {
		return false
	}
	eb, ok := editBankBinding(cfg, page, layer, control, mod)
	return ok && eb.IsSelectClipHoriz()
}

// isSelectClipClear reports whether a note_on event for `control` (C5) on
// the current `page` is the direction-button "unselect all" action -- true
// only when page=="edit" AND the edit bank binds this exact control as
// "selectClipClear". Same direct-intercept shape as isSelectClipVert above.
func isSelectClipClear(cfg *config.Config, page string, layer int, control string, mod string) bool {
	if page != "edit" {
		return false
	}
	eb, ok := editBankBinding(cfg, page, layer, control, mod)
	return ok && eb.IsSelectClipClear()
}

// dispatchSelectClipVertical implements the Up/Down button's vertical
// clip-selection action (Edit page): asks the sidecar's
// select_clip_vertical op to step to the next occupied video track in
// `direction` (+1 = Up, -1 = Down) relative to the CURRENT selection (or
// bootstrap from the top/bottom occupied clip if nothing is selected yet
// -- see ResolveDriver.select_clip_vertical's doc comment for the exact
// clamp/no-wrap semantics, a Python-side reimplementation of
// nextTrackIndex's contract). On success, updates BOTH `selection` (via
// setSelection) AND activeTrack (via setActiveTrack) in lockstep -- see
// this section's doc comment.
//
// A nil client, a send error, or a refused/failed sidecar response are
// all silent no-ops that leave selection/activeTrack UNCHANGED (never a
// partial update).
//
// Returns a human-readable LastAction summary ("Up → V<track>"/
// "Down → V<track>") on success, or "" on any no-op/refusal.
func dispatchSelectClipVertical(
	client *ipc.Client,
	control string,
	direction int,
	color string,
	getSelection func() clipSelection,
	setSelection func(clipSelection),
	setActiveTrack func(int),
) string {
	if client == nil {
		log.Printf("[note_on] control=%q method=selectClipVert SKIP no-sidecar", control)
		return ""
	}
	currentTrack := 0
	if sel := getSelection(); sel.active {
		currentTrack = sel.track
	}
	resp, err := client.Send(map[string]any{
		"op":            "select_clip_vertical",
		"direction":     direction,
		"current_track": currentTrack,
		"color":         color,
	})
	if err != nil {
		log.Printf("[note_on] control=%q method=selectClipVert send error: %v", control, err)
		return ""
	}
	r := parseSelectionResult(resp)
	if !r.ok {
		log.Printf("[note_on] control=%q method=selectClipVert failed: %v", control, r.err)
		return ""
	}
	setActiveTrack(r.track)
	// confirmed: true -- vertical select never moves the playhead (it
	// re-targets which track is selected AT the current, freshly-read
	// playhead), so there is no SetCurrentTimecode race to protect
	// against here. See clipSelection's doc comment.
	setSelection(clipSelection{active: true, track: r.track, start: r.start, end: r.end, confirmed: true, setAt: time.Now()})
	log.Printf("[note_on] control=%q method=selectClipVert -> track=%d start=%d end=%d", control, r.track, r.start, r.end)
	return fmt.Sprintf("%s → V%d", control, r.track)
}

// dispatchSelectClipHorizontal implements the Left/Right button's
// horizontal clip-selection action (Edit page only -- see Start()'s
// hard-wired "Left"/"Right" note_on cases, which are the only call
// sites): asks the sidecar's select_clip_horizontal op to select the
// previous (direction=-1, Left) or next (direction=+1, Right) clip
// relative to the current selection's (track, start) -- or bootstrap
// from the top-occupied clip at the playhead if nothing is selected yet
// -- and move the Resolve playhead onto it (see
// ResolveDriver.select_clip_horizontal's doc comment). On success,
// updates BOTH `selection` and activeTrack in lockstep, same shape as
// dispatchSelectClipVertical above.
//
// Same no-op/failure posture as dispatchSelectClipVertical: a nil
// client, send error, or refused/failed response leaves state unchanged.
//
// Returns "Left → V<track>"/"Right → V<track>" on success, or "" on any
// no-op/refusal.
func dispatchSelectClipHorizontal(
	client *ipc.Client,
	control string,
	direction int,
	color string,
	getSelection func() clipSelection,
	setSelection func(clipSelection),
	setActiveTrack func(int),
) string {
	if client == nil {
		log.Printf("[note_on] control=%q method=selectClipHoriz SKIP no-sidecar", control)
		return ""
	}
	currentTrack := 0
	currentStart := 0
	if sel := getSelection(); sel.active {
		currentTrack = sel.track
		currentStart = sel.start
	}
	resp, err := client.Send(map[string]any{
		"op":            "select_clip_horizontal",
		"direction":     direction,
		"current_track": currentTrack,
		"current_start": currentStart,
		"color":         color,
	})
	if err != nil {
		log.Printf("[note_on] control=%q method=selectClipHoriz send error: %v", control, err)
		return ""
	}
	r := parseSelectionResult(resp)
	if !r.ok {
		log.Printf("[note_on] control=%q method=selectClipHoriz failed: %v", control, r.err)
		return ""
	}
	setActiveTrack(r.track)
	// confirmed: false -- the sidecar just moved the playhead via
	// SetCurrentTimecode (see select_clip_horizontal in resolve_driver.py);
	// the drain loop's next status tick can race ahead of Resolve
	// committing that write (BUG 3, 2026-08-12 live report). The auto-reset
	// below suppresses clearing this selection until either an observed
	// playhead lands inside [start,end) (setting confirmed=true) or
	// setAt+graceWindow elapses. See clipSelection's doc comment.
	setSelection(clipSelection{active: true, track: r.track, start: r.start, end: r.end, confirmed: false, setAt: time.Now()})
	log.Printf("[note_on] control=%q method=selectClipHoriz -> track=%d start=%d end=%d", control, r.track, r.start, r.end)
	return fmt.Sprintf("%s → V%d", control, r.track)
}

// dispatchSelectClipClear implements the C5 button's "unselect all"
// action (Edit page): clears the direction-button selection
// (setActiveTrack(0) + clearSelection()) and restores the tinted clip's
// ORIGINAL color via the sidecar's EXISTING clear_highlight op -- the
// exact same restore-original-color contract D1's Stop()/highlight
// machinery already use (ResolveDriver.clear_highlight). Deliberately
// NOT set_clips_color (D1's OLD knob-press "release" action), which
// forces a uniform release color instead of restoring -- this feature's
// C5 restores, matching the locked design's "restore the selected/
// tinted clip's ORIGINAL color" requirement.
//
// setActiveTrack/clearSelection ALWAYS run, even with a nil client (same
// nil-client-safe posture as dispatchSelectClipRelease above) -- the
// sidecar send is best-effort.
//
// Always returns "C5 unselect" -- unconditional, same "no no-op path"
// shape as dispatchSelectClipRelease.
func dispatchSelectClipClear(client *ipc.Client, setActiveTrack func(int), clearSelection func()) string {
	setActiveTrack(0)
	clearSelection()
	log.Printf("[note_on] control=C5 method=selectClipClear activeTrack -> 0, selection cleared")
	if client == nil {
		return "C5 unselect"
	}
	if _, err := client.Send(map[string]any{"op": "clear_highlight"}); err != nil {
		log.Printf("[note_on] control=C5 method=selectClipClear clear_highlight send error: %v", err)
	}
	return "C5 unselect"
}

// ---------------------------------------------------------------------------
// C1 "add marker at playhead" feature: adds a Resolve marker at the
// current playhead position on the Edit page, via the sidecar's
// add_marker op. Script-API only (no CGEvent, no calibration), same
// posture as the D1 selectClip/highlight/release features above.
// ---------------------------------------------------------------------------

// isAddMarkerControl reports whether a note_on event for `control` on the
// current `page` is the Edit-page "add marker at playhead" action — the
// mirror of isSelectClipRelease's shape (checked directly against the
// edit bank). True only when page=="edit" AND the edit bank binds this
// exact control as "addMarker". This direct check matters because
// addMarker is neither IsProperty(), IsJog(), nor IsKey() — the only
// types resolveNoteBank shadows to the "edit" bank for — so without it, a
// C1 press on the Edit page would silently fall through resolveNoteBank's
// colBank fallback and re-fire the Color page's col-bank C1 binding
// (type "key", Alt+S), exactly the regression this function exists to
// prevent.
func isAddMarkerControl(cfg *config.Config, page string, layer int, control string, mod string) bool {
	if page != "edit" {
		return false
	}
	eb, ok := editBankBinding(cfg, page, layer, control, mod)
	return ok && eb.IsAddMarker()
}

// dispatchAddMarker sends the sidecar's add_marker op for an Edit-page
// "addMarker" binding (C1 by default). Applies bindings.json field
// defaults the same way processNonBalanceDrain/dispatchNoteOn apply
// Sens/MaxDelta defaults for other binding types: b.MarkerDurationSeconds
// == 0 falls back to defaultMarkerDurationSeconds (1s); b.MarkerColor ==
// "" falls back to defaultMarkerColor ("Blue"). b.MarkerName is passed
// through as the "name" field only when non-empty — an empty MarkerName
// omits the key entirely rather than sending an explicit empty string,
// matching the "gang omits property" / "min/max omits when nil" key-
// presence convention already used elsewhere in this file. Nil-client
// safe (logs SKIP, no-op) — same defensive posture as every other
// client==nil guard in this file.
//
// activeTrack is the D1 knob's currently-selected video track number (0
// if none) — included in the request as "track" only when non-zero
// (same omit-when-zero idiom as bump_property/reset_property,
// engine.go:612-615/888-889). When the sidecar finds a clip on that
// track under the playhead, the marker spans that clip exactly (frame =
// clip start, duration = clip length) instead of the fixed 1s-at-
// playhead default; any other outcome (no track selected, no clip found)
// falls back to the playhead+configured-duration behavior unchanged.
//
// setActiveMarker is called on a SUCCESSFUL add_marker response
// (resp["ok"]==true) with the sidecar's returned frame plus color, and a
// duration that PREFERS the response's actual "duration"/"fps" (the
// clip's real length, converted to seconds) over the locally-sent
// duration_seconds — reusing dispatchMarkerResize's exact
// !hasDur||!hasFps||fps<=0 guard shape (engine.go, see
// dispatchMarkerResize above) to decide whether the response's duration
// is usable, falling back to the configured/local seconds value
// otherwise. Unlike dispatchMarkerResize, an unusable response
// duration/fps does NOT skip setActiveMarker here — entering marker mode
// must always happen on a successful add, regardless of which duration
// value ends up being shown. This is C1's "enter marker mode" side
// effect (see the package's marker-mode section doc comment above
// isJogPressControl below). A failed/marker_exists response does NOT
// call setActiveMarker — nothing new was created, so marker mode's
// active-marker state (whatever it was) is left untouched rather than
// entered or cleared.
// Returns a human-readable LastAction summary ("C1 → marker @<duration>s
// <color>") on a successful add, or "" when the request was skipped
// (no-sidecar) or refused (send error, not-ok response). Note this can't
// show the sidecar's auto-numbered marker name ("M04") -- add_marker's
// response only carries frame/duration/fps, not the name it actually
// picked (see resolve_driver.py's _next_auto_marker_name) -- adding that
// would need a sidecar response-shape change, out of scope for this pass.
func dispatchAddMarker(client *ipc.Client, control string, b config.Binding, activeTrack int, setActiveMarker func(frame int, color string, durationSeconds float64), getActiveMarkerFrame func() int, clearActiveMarker func(), recordMarkerRange func(start, end int)) string {
	// C1 is a TOGGLE. Exit is unconditional -- no playhead range test -- which
	// is what makes it free of edge cases: there is no state in which marker
	// mode is live and C1 fails to end it. The cost is that creating a second
	// marker takes two presses (exit, then create), which the user accepted
	// over a range-gated exit that could strand the mode.
	//
	// Checked before the nil-client guard below: exiting is pure local state
	// and must work even with no sidecar.
	if getActiveMarkerFrame != nil && clearActiveMarker != nil && getActiveMarkerFrame() >= 0 {
		clearActiveMarker()
		log.Printf("[note_on] control=%q markerMode-exit (C1 toggle)", control)
		return "Marker mode off"
	}
	if client == nil {
		log.Printf("[note_on] control=%q method=addMarker SKIP no-sidecar", control)
		return ""
	}

	// ADOPT: if the playhead sits inside an existing marker, pick that marker
	// up for resize/recolour rather than creating a second one on top of it.
	// This also fixes a live failure -- C1 on an existing marker's exact start
	// frame previously returned marker_exists and did nothing at all.
	if setActiveMarker != nil {
		if resp, err := client.Send(map[string]any{"op": "marker_at_playhead"}); err == nil && resp != nil {
			if found, _ := resp["found"].(bool); found {
				// JSON numbers arrive as float64, matching how the existing
				// create path reads resp["frame"] just below.
				frameF, okF := resp["frame"].(float64)
				frame := int(frameF)
				dur, okD := resp["duration"].(float64)
				color, _ := resp["color"].(string)
				if okF && okD {
					if color == "" {
						color = defaultMarkerColor
					}
					setActiveMarker(frame, color, dur)
					if recordMarkerRange != nil {
						as, okA := resp["abs_start"].(float64)
						ae, okB := resp["abs_end"].(float64)
						if okA && okB {
							recordMarkerRange(int(as), int(ae))
						}
					}
					log.Printf("[note_on] control=%q markerMode-adopt frame=%d color=%q dur=%.3fs", control, frame, color, dur)
					return "Marker adopted"
				}
			}
		}
	}

	duration := b.MarkerDurationSeconds
	if duration == 0 {
		duration = defaultMarkerDurationSeconds
	}
	color := b.MarkerColor
	if color == "" {
		color = defaultMarkerColor
	}
	cmd := map[string]any{
		"op":               "add_marker",
		"duration_seconds": duration,
		"color":            color,
	}
	if b.MarkerName != "" {
		cmd["name"] = b.MarkerName
	}
	if activeTrack != 0 {
		cmd["track"] = activeTrack
	}
	log.Printf("[note_on] control=%q method=addMarker duration_seconds=%.2f color=%q name=%q track=%d",
		control, duration, color, b.MarkerName, activeTrack)
	resp, err := client.Send(cmd)
	if err != nil {
		log.Printf("[note_on] control=%q method=addMarker send error: %v", control, err)
		return ""
	}
	if ok, _ := resp["ok"].(bool); !ok {
		log.Printf("[note_on] control=%q method=addMarker failed: %v", control, resp["err"])
		return ""
	}
	frameF, _ := resp["frame"].(float64)
	frame := int(frameF)

	// Prefer the sidecar's actual duration/fps (the D1-selected clip's real
	// length, when a track was sent and a clip was found there; otherwise
	// the same duration_seconds this call sent, echoed back) over the
	// locally-sent seconds value, so the Marker status row shows the
	// clip's real duration rather than the button's configured default.
	durationSeconds := duration
	if durFrames, hasDur := resp["duration"].(float64); hasDur {
		if fps, hasFps := resp["fps"].(float64); hasFps && fps > 0 {
			durationSeconds = durFrames / fps
		}
	}
	setActiveMarker(frame, color, durationSeconds)
	log.Printf("[note_on] control=%q method=addMarker enteredMarkerMode frame=%d color=%q durationSeconds=%.2f",
		control, frame, color, durationSeconds)
	return fmt.Sprintf("C1 → marker @%.1fs %s", durationSeconds, color)
}

// ---------------------------------------------------------------------------
// C1 "marker mode" (create/resize/color/exit) state machine, built on top
// of C1's add-marker-at-playhead above:
//
//   - ENTER: a C1 press creates a fresh marker (dispatchAddMarker, above)
//     AND enters marker mode via setActiveMarker(frame, color, duration)
//     — each C1 press replaces whatever marker was previously active.
//   - RESIZE: while marker mode is active, ControlDial rotation extends
//     (clockwise/positive step) or shrinks (counter-clockwise/negative)
//     the active marker's duration via the sidecar's resize_marker op
//     (dispatchMarkerResize) INSTEAD OF the ControlDial's normal jog
//     action — see the drain loop's b.IsJog() branch in Start(). When
//     marker mode is NOT active, the ControlDial is unaffected: plain
//     jog, byte-identical to before this feature (all speed/sustain
//     tiers unchanged).
//   - COLOR: while marker mode is active, P1-8btn (bound as type
//     "markerColor") sets the active marker's color via the sidecar's
//     set_marker_color op (dispatchSetMarkerColor). A no-op everywhere
//     else (marker mode inactive, or any page other than Edit).
//   - EXIT: a ControlDial PRESS (note_on, not a turn) finalizes/exits
//     marker mode (dispatchControlDialPress -> clearActiveMarker) — the
//     dial reverts to plain jog on its very next turn. Pressing the
//     ControlDial while marker mode was never entered is a no-op,
//     byte-identical to the pre-marker-mode behavior proven by
//     TestNoteOnPageAware_EditPage_JogKnobClick_NoSidecarOp
//     (editpage_test.go) — dispatchNoteOn's switch has no case for
//     IsJog() at all; this feature's direct intercept in Start()'s
//     note_on callback (isJogPressControl, mirroring isAddMarkerControl/
//     isSelectClipRelease's shape above) is the ONLY place a ControlDial
//     PRESS ever does anything.
//
// All four steps are script-API only (no CGEvent, no calibration) — same
// posture as the D1 selectClip/highlight/release and C1 add-marker
// features above.
// ---------------------------------------------------------------------------

// isJogPressControl reports whether a note_on event for `control` on the
// current `page` is the ControlDial knob's PRESS action (marker-mode
// exit) — the mirror of isAddMarkerControl/isSelectClipRelease's shape
// (checked directly against the edit bank). True only when page=="edit"
// AND the edit bank binds this exact control as "jog". This direct check
// matters for the identical reason isAddMarkerControl's does: resolveNoteBank
// only shadows "property"/"jog" turn-typed controls to the edit bank for
// DRAIN (turn) events, not note_on (press) events — dispatchNoteOn's
// switch has no case for IsJog() at all, so without this direct
// intercept a ControlDial press could never reach marker-mode-exit
// logic (it would just keep silently no-op'ing, as it always has).
func isJogPressControl(cfg *config.Config, page string, layer int, control string, mod string) bool {
	if page != "edit" {
		return false
	}
	eb, ok := editBankBinding(cfg, page, layer, control, mod)
	return ok && eb.IsJog()
}

// dispatchControlDialPress implements the ControlDial knob's PRESS action
// on the Edit page: exits marker mode if currently active
// (clearActiveMarker — activeMarkerFrame -> -1, the dial reverts to plain
// jog on the next turn), or is a silent no-op if marker mode was never
// entered — see this section's doc comment above for why that no-op is
// byte-identical to the feature's pre-existing (unintentional) no-op
// behavior.
//
// Returns "Marker mode exit" when marker mode was actually active (and is
// now cleared), or "" for the not-active no-op.
func dispatchControlDialPress(getActiveMarkerFrame func() int, clearActiveMarker func()) string {
	if getActiveMarkerFrame() < 0 {
		log.Printf("[note_on] control=ControlDial method=markerMode-exit SKIP not-active")
		return ""
	}
	clearActiveMarker()
	log.Printf("[note_on] control=ControlDial method=markerMode-exit activeMarkerFrame -> -1 (inactive)")
	return "Marker mode exit"
}

// isMarkerColorControl reports whether a note_on event for `control` on
// the current `page` is a P-button's Edit-page "set the marker-mode
// active marker's color" action — same direct-intercept shape as
// isAddMarkerControl/isSelectClipRelease/isJogPressControl above, and for
// the identical reason: "markerColor" is neither IsProperty() nor
// IsJog(), so resolveNoteBank would never shadow it to the edit bank —
// and P1-8btn have NO col-bank binding at all today (only the SLIDERS
// P1-P8, a different control, exist there), so a colBank lookup would
// just miss and dispatchNoteOn would never even be called.
func isMarkerColorControl(cfg *config.Config, page string, layer int, control string, mod string) bool {
	if page != "edit" {
		return false
	}
	eb, ok := editBankBinding(cfg, page, layer, control, mod)
	return ok && eb.IsMarkerColor()
}

// dispatchSetMarkerColor sends the sidecar's set_marker_color op for the
// currently-active marker, ONLY while marker mode is active (getActiveMarkerFrame()
// >= 0) — a P-button press is a silent no-op otherwise, per this
// feature's "not in marker mode → no-op" design rule. Nil-client is also
// a silent no-op, same defensive posture as every other client==nil
// guard in this file. On a successful response, updateActiveMarkerColor
// refreshes Status.MarkerMode's color text immediately (the color SENT
// is already known here — no need to parse anything back out of the
// response).
// Returns "Marker → <color>" on a successful recolor, or "" for every
// no-op/refused path (not-active, no-sidecar, send error, not-ok response).
func dispatchSetMarkerColor(client *ipc.Client, control string, b config.Binding, getActiveMarkerFrame func() int, updateActiveMarkerColor func(string)) string {
	frame := getActiveMarkerFrame()
	if frame < 0 {
		log.Printf("[note_on] control=%q method=markerColor SKIP not-active", control)
		return ""
	}
	if client == nil {
		log.Printf("[note_on] control=%q method=markerColor SKIP no-sidecar", control)
		return ""
	}
	log.Printf("[note_on] control=%q method=markerColor frame=%d color=%q", control, frame, b.MarkerColor)
	resp, err := client.Send(map[string]any{"op": "set_marker_color", "frame": frame, "color": b.MarkerColor})
	if err != nil {
		log.Printf("[note_on] control=%q method=markerColor send error: %v", control, err)
		return ""
	}
	if ok, _ := resp["ok"].(bool); !ok {
		log.Printf("[note_on] control=%q method=markerColor failed: %v", control, resp["err"])
		return ""
	}
	updateActiveMarkerColor(b.MarkerColor)
	return "Marker → " + b.MarkerColor
}

// ---------------------------------------------------------------------------
// Feature 1 (2026-08-12): Hue-gated marker P-buttons -- P1-8btn, Edit page
// only, while P-mode is "hue": recolour the marker whose span contains the
// playhead, or add a new one if none does. Pure Go orchestration of three
// already-shipped, already-tested sidecar ops (marker_at_playhead,
// set_marker_color, add_marker) -- zero new Python, see the REV 2 design
// doc's Feature 1 section. Unlike the C1 addMarker/markerColor pair above,
// this is NOT a toggle and does not itself manage marker-mode ENTER/EXIT
// state beyond marking the touched marker active (so ControlDial resize
// keeps targeting it) -- C1 remains the sole entry/exit point for marker
// mode.
// ---------------------------------------------------------------------------

// isMarkerAtPlayheadControl reports whether a note_on event for `control`
// on the current `page` is a P-button's Feature 1 Edit-page
// "recolour-or-add marker at playhead" action -- same direct-intercept
// shape as isAddMarkerControl/isMarkerColorControl above, and for the
// identical reason: "markerAtPlayhead" is neither IsProperty() nor
// IsJog(), so resolveNoteBank would never shadow it to the edit bank, and
// P1-8btn have no col-bank binding at all to fall back to.
func isMarkerAtPlayheadControl(cfg *config.Config, page string, layer int, control string, mod string) bool {
	if page != "edit" {
		return false
	}
	eb, ok := editBankBinding(cfg, page, layer, control, mod)
	return ok && eb.IsMarkerAtPlayhead()
}

// dispatchMarkerAtPlayhead implements Feature 1's three-way P-button
// toggle: probes marker_at_playhead; if a marker's [start, start+duration)
// span contains the playhead AND its colour case-insensitively DIFFERS
// from the pressed button's configured colour, recolours it via
// set_marker_color (carrying the probe's frame and the binding's
// MarkerColor) and marks it ACTIVE via setActiveMarker (so ControlDial
// resize keeps targeting the marker just touched) -- add_marker is never
// sent on this branch, and a recolour never moves the playhead.
//
// If a marker IS found and its colour case-insensitively MATCHES the
// pressed button's colour, the SAME press instead DELETES it via
// delete_marker and EXITS marker mode via clearActiveMarker -- a deleted
// marker can never be a valid resize target, so leaving marker mode active
// would strand the ControlDial trying to resize a marker that no longer
// exists. An absent or empty "color" field in the probe response is
// treated as a non-match (never triggers a delete) -- a probe reply
// missing the field degrades to the safer recolour path rather than
// risking an unintended delete.
//
// Otherwise (no marker at the playhead) creates a new marker via
// add_marker in the binding's configured colour, THEN (2026-08-25, the
// "marker-mode trap" fix) issues one follow-up marker_at_playhead probe to
// learn its absolute [start,end) range for recordMarkerRange -- see the
// inline comment at that call site for why this second round trip exists
// and why its absence was the actual root cause of marker mode never
// auto-exiting for a freshly created marker. activeTrack is accepted
// for logging only and is deliberately NEVER forwarded as add_marker's
// "track" argument (2026-08-12 CHANGE 2: clip selection no longer
// influences a new marker's duration) -- the sidecar's own three-tier
// duration precedence (explicit track -> top-occupied clip under the
// playhead -> flat duration_seconds default) always resolves at its
// SECOND tier here, regardless of what the direction buttons have
// selected. That containment/duration maths lives entirely in the
// sidecar and is not reimplemented here.
//
// pmode is the live P-row mode (hue/sat/lum), read by the caller via
// Engine.getPMode() and passed in explicitly so this gate is unit-testable
// without a live Start() session. b.WhenPMode=="" means ungated (fires
// regardless of pmode); a mismatch suppresses the ENTIRE action -- not even
// the marker_at_playhead probe is sent -- and logs a suppression line, per
// the design doc's "P-buttons are inert on sat/lum" requirement.
//
// Nil-client safe (logs SKIP, no-op), same defensive posture as every
// other client==nil guard in this file. setActiveMarker/recordMarkerRange/
// clearActiveMarker are all nil-checked before use for the same reason.
// Returns a human-readable LastAction summary on success, "" for every
// no-op/refused path (whenPMode mismatch, no-sidecar, send error, not-ok
// response).
func dispatchMarkerAtPlayhead(client *ipc.Client, control string, b config.Binding, activeTrack int, pmode string, setActiveMarker func(frame int, color string, durationSeconds float64), recordMarkerRange func(start, end int), clearActiveMarker func()) string {
	if b.WhenPMode != "" && b.WhenPMode != pmode {
		log.Printf("[note_on] control=%q method=markerAtPlayhead SUPPRESSED whenPMode=%q livePMode=%q", control, b.WhenPMode, pmode)
		return ""
	}
	if client == nil {
		log.Printf("[note_on] control=%q method=markerAtPlayhead SKIP no-sidecar", control)
		return ""
	}

	resp, err := client.Send(map[string]any{"op": "marker_at_playhead"})
	if err != nil {
		log.Printf("[note_on] control=%q method=markerAtPlayhead probe send error: %v", control, err)
		return ""
	}
	if found, _ := resp["found"].(bool); found {
		frameF, okF := resp["frame"].(float64)
		if !okF {
			log.Printf("[note_on] control=%q method=markerAtPlayhead probe response missing frame: %v", control, resp)
			return ""
		}
		frame := int(frameF)
		probeColor, _ := resp["color"].(string)

		if probeColor != "" && strings.EqualFold(probeColor, b.MarkerColor) {
			// Same-colour press: the three-way toggle's DELETE leg,
			// instead of a no-op recolour-to-itself.
			delResp, err := client.Send(map[string]any{"op": "delete_marker", "frame": frame})
			if err != nil {
				log.Printf("[note_on] control=%q method=markerAtPlayhead delete send error: %v", control, err)
				return ""
			}
			if ok, _ := delResp["ok"].(bool); !ok {
				log.Printf("[note_on] control=%q method=markerAtPlayhead delete failed: %v", control, delResp["err"])
				return ""
			}
			if clearActiveMarker != nil {
				clearActiveMarker()
			}
			log.Printf("[note_on] control=%q method=markerAtPlayhead deleted frame=%d color=%q", control, frame, b.MarkerColor)
			return "Marker ✕ " + b.MarkerColor
		}

		recolorResp, err := client.Send(map[string]any{"op": "set_marker_color", "frame": frame, "color": b.MarkerColor})
		if err != nil {
			log.Printf("[note_on] control=%q method=markerAtPlayhead recolour send error: %v", control, err)
			return ""
		}
		if ok, _ := recolorResp["ok"].(bool); !ok {
			log.Printf("[note_on] control=%q method=markerAtPlayhead recolour failed: %v", control, recolorResp["err"])
			return ""
		}
		if setActiveMarker != nil {
			dur, _ := resp["duration"].(float64)
			setActiveMarker(frame, b.MarkerColor, dur)
			if recordMarkerRange != nil {
				as, okA := resp["abs_start"].(float64)
				ae, okB := resp["abs_end"].(float64)
				if okA && okB {
					recordMarkerRange(int(as), int(ae))
				}
			}
		}
		log.Printf("[note_on] control=%q method=markerAtPlayhead recolour frame=%d color=%q", control, frame, b.MarkerColor)
		return "Marker → " + b.MarkerColor
	}

	// CHANGE 2 (2026-08-12): "track" is deliberately NEVER forwarded here
	// -- a new marker's duration must always follow the sidecar's
	// top-occupied-clip tier of add_marker's duration precedence,
	// regardless of what the direction buttons (D1) have selected.
	// Forwarding activeTrack would force the sidecar's FIRST tier
	// (explicit track) instead, letting a stale/unrelated D1 selection
	// override the marker's span. activeTrack is still accepted as a
	// parameter purely so it can be logged for diagnostic context.
	cmd := map[string]any{
		"op":    "add_marker",
		"color": b.MarkerColor,
	}
	log.Printf("[note_on] control=%q method=markerAtPlayhead add color=%q (activeTrack=%d, ignored -- duration always follows the top-occupied clip)", control, b.MarkerColor, activeTrack)
	addResp, err := client.Send(cmd)
	if err != nil {
		log.Printf("[note_on] control=%q method=markerAtPlayhead add send error: %v", control, err)
		return ""
	}
	if ok, _ := addResp["ok"].(bool); !ok {
		log.Printf("[note_on] control=%q method=markerAtPlayhead add failed: %v", control, addResp["err"])
		return ""
	}
	frameF, _ := addResp["frame"].(float64)
	frame := int(frameF)
	var durationSeconds float64
	if durFrames, hasDur := addResp["duration"].(float64); hasDur {
		if fps, hasFps := addResp["fps"].(float64); hasFps && fps > 0 {
			durationSeconds = durFrames / fps
		}
	}
	if setActiveMarker != nil {
		setActiveMarker(frame, b.MarkerColor, durationSeconds)
		if recordMarkerRange != nil {
			// THE "MARKER-MODE TRAP" FIX (2026-08-25, live report): add_marker's
			// own response carries only the RELATIVE frame_id (what
			// resize_marker/set_marker_color need to address the sidecar),
			// never the ABSOLUTE [start,end) range maybeAutoExitMarker needs to
			// know when the playhead has left this marker. Without this
			// follow-up call, activeMarkerAbsStart/End stayed at whatever they
			// were before THIS press (0,0 the first time ever, or a PREVIOUS
			// marker's stale range on every add after that -- recordMarkerRange
			// was only ever wired into the recolour branch above, never this
			// one). maybeAutoExitMarker's "start==0 && end==0 -> never fires"
			// guard exists for markers genuinely predating range-tracking, but
			// it was silently swallowing EVERY freshly ADDED marker too --
			// exactly the live symptom that opened this fix: the ControlDial
			// stayed parked on resize_marker for hours, playhead nowhere near
			// the marker, because the range that would have ended it was never
			// recorded in the first place.
			//
			// The playhead has not moved since add_marker ran (this whole
			// dispatch is one synchronous button press), so a fresh
			// marker_at_playhead probe is guaranteed to find exactly the marker
			// just created -- reusing the SAME op/response shape the recolour
			// branch above already relies on (abs_start/abs_end) rather than
			// teaching the sidecar a new response field. One extra round trip,
			// only on marker CREATION (a single physical button press, not a
			// hot loop), so the added latency is not a concern.
			//
			// A refused/incomplete probe here is not fatal to the marker
			// itself (it already exists in Resolve) -- it just leaves the
			// auto-exit range unset, i.e. no WORSE than this bug's pre-fix
			// behavior, so it degrades gracefully rather than failing the
			// whole button press. Logged loudly either way so a recurrence is
			// diagnosable rather than silently trapping the dial again.
			if rangeResp, rangeErr := client.Send(map[string]any{"op": "marker_at_playhead"}); rangeErr != nil {
				log.Printf("[note_on] control=%q method=markerAtPlayhead range-probe send error after add frame=%d: %v", control, frame, rangeErr)
			} else if found, _ := rangeResp["found"].(bool); !found {
				log.Printf("[note_on] control=%q method=markerAtPlayhead range-probe found=false after add frame=%d (auto-exit range left unset)", control, frame)
			} else if as, okA := rangeResp["abs_start"].(float64); !okA {
				log.Printf("[note_on] control=%q method=markerAtPlayhead range-probe missing abs_start after add frame=%d: %v", control, frame, rangeResp)
			} else if ae, okB := rangeResp["abs_end"].(float64); !okB {
				log.Printf("[note_on] control=%q method=markerAtPlayhead range-probe missing abs_end after add frame=%d: %v", control, frame, rangeResp)
			} else {
				recordMarkerRange(int(as), int(ae))
			}
		}
	}
	log.Printf("[note_on] control=%q method=markerAtPlayhead added frame=%d color=%q", control, frame, b.MarkerColor)
	return "Marker + " + b.MarkerColor
}

// dispatchMarkerResize sends the sidecar's resize_marker op for the
// active marker at `frame`, extending (positive step, ControlDial turned
// clockwise) or shrinking (negative step, counter-clockwise) its
// duration by step*framesPerStep frames — see the drain loop's b.IsJog()
// branch in Start(), which calls this INSTEAD OF processNonBalanceDrain's
// normal jog dispatch whenever marker mode is active. The marker's frame
// (start position) never changes here — only its duration. step==0 and a
// nil client are both silent no-ops.
//
// On a successful response, updateDuration is called with the NEW
// duration converted to SECONDS (response "duration" is frames, matching
// GetMarkers()'s own frame-based duration field; "fps" rides along in
// the same response purely for this conversion — see
// ResolveDriver.resize_marker's doc comment) so Status.MarkerMode
// reflects the resize immediately. A response missing a usable fps (e.g.
// startRecordingSidecar-style test doubles that reply {"ok":true} with
// no extra fields, or any failure response) leaves the duration display
// untouched rather than computing garbage.
// Returns "Marker <±deltaFrames>f" on a successful resize (regardless of
// whether the response carried a usable duration/fps for the display
// update -- the resize itself is what happened), or "" for every no-op
// path (no-sidecar, step==0, send error, not-ok response).
func dispatchMarkerResize(client *ipc.Client, frame int, step int, framesPerStep int, updateDuration func(seconds float64)) string {
	if client == nil {
		log.Printf("[drain] control=ControlDial method=markerResize SKIP no-sidecar frame=%d", frame)
		return ""
	}
	if step == 0 {
		return ""
	}
	deltaFrames := step * framesPerStep
	log.Printf("[drain] control=ControlDial method=markerResize frame=%d step=%d deltaFrames=%d", frame, step, deltaFrames)
	resp, err := client.Send(map[string]any{"op": "resize_marker", "frame": frame, "delta_frames": deltaFrames})
	if err != nil {
		log.Printf("[drain] control=ControlDial method=markerResize send error: %v", err)
		return ""
	}
	if ok, _ := resp["ok"].(bool); !ok {
		log.Printf("[drain] control=ControlDial method=markerResize failed: %v", resp["err"])
		return ""
	}
	durFrames, hasDur := resp["duration"].(float64)
	fps, hasFps := resp["fps"].(float64)
	if !hasDur || !hasFps || fps <= 0 {
		log.Printf("[drain] control=ControlDial method=markerResize response missing usable duration/fps, skipping duration-display update: %v", resp)
		return fmt.Sprintf("Marker %+df", deltaFrames)
	}
	updateDuration(durFrames / fps)
	return fmt.Sprintf("Marker %+df", deltaFrames)
}

// New loads device-map.json, bindings.json, and (optionally) calibration.json
// from cfgDir. A missing/unparseable calibration.json is non-fatal (matches
// pre-refactor CLI behavior: drags/clicks are suppressed until --calibrate is
// run); a missing/unparseable device-map.json or bindings.json is fatal.
func New(cfgDir string) (*Engine, error) {
	cfg, err := config.Load(cfgDir+"/device-map.json", cfgDir+"/bindings.json")
	if err != nil {
		return nil, err
	}

	var cal *calib.Calib
	if c, err := calib.Load(cfgDir + "/calibration.json"); err == nil {
		cal = c
		log.Printf("calibration loaded from %s/calibration.json", cfgDir)
	} else {
		log.Println("no calibration.json found — wheel drags/clicks will be suppressed (run --calibrate first)")
	}

	if !macapp.AccessibilityTrusted() {
		fmt.Println("[2t5-deck-plus] WARNING: Accessibility not granted.")
		fmt.Println("  Grant 2t5-deck-plus Accessibility in System Settings → Privacy & Security → Accessibility,")
		fmt.Println("  then relaunch. Mouse drags will be suppressed until granted.")
	}

	// Seed lastFolder from a prior run's sentinel, if one exists, so the C5
	// Quick Export button remembers its target directory across restarts.
	// A missing/unparseable sentinel is non-fatal (same posture as the
	// missing-calibration.json handling above) — lastFolder just stays "".
	var lastFolder, savedPMode string
	if b, err := os.ReadFile(stateFilePath); err == nil {
		var s sentinelState
		if json.Unmarshal(b, &s) == nil {
			lastFolder = s.LastExportFolder
			savedPMode = s.PMode
		}
	}

	e := &Engine{
		cfgDir:            cfgDir,
		cal:               cal,
		sockPath:          "/tmp/2t5-deck-plus.sock",
		resourcesDir:      ".",
		modeTracker:       mode.New(),
		aw:                target.New(),
		lastFolder:        lastFolder,
		savedPMode:        savedPMode,
		activeMarkerFrame: -1, // marker mode starts inactive (0 is a valid frame, see the field's doc comment)
	}
	e.cfg.Store(cfg)
	return e, nil
}

// SetConfig atomically replaces the binding configuration used by every
// dispatch path. Lock-free, safe from any goroutine while Start() runs; cfg
// must not be mutated afterwards. Provenance: ld-resolve 6437f3a.
func (e *Engine) SetConfig(cfg *config.Config) {
	if cfg != nil {
		e.cfg.Store(cfg)
		// a hot reload may have changed or removed the binding a pending
		// hold-to-fire press was waiting on: nothing held fires across a reload
		e.holdCancelAll()
	}
}

// currentConfig returns the live configuration.
func (e *Engine) currentConfig() *config.Config { return e.cfg.Load() }

// liveCfg adapts the engine to router.Cfg so the CDL router resolves against
// the current config on every call (ld-resolve changed router.New instead;
// an adapter keeps internal/router untouched).
type liveCfg struct{ e *Engine }

func (l liveCfg) ControlForCC(cc int) string  { return l.e.cfg.Load().ControlForCC(cc) }
func (l liveCfg) ControlForNote(n int) string { return l.e.cfg.Load().ControlForNote(n) }
func (l liveCfg) Binding(bank, control string) (config.Binding, bool) {
	return l.e.cfg.Load().Binding(bank, control)
}

// getLastFolder returns the C5 Quick-Export button's remembered target
// directory, or "" if none has been set yet (the next press shows the
// folder picker).
func (e *Engine) getLastFolder() string {
	e.lastFolderMu.Lock()
	defer e.lastFolderMu.Unlock()
	return e.lastFolder
}

// setLastFolder records the Quick-Export target directory and persists it
// immediately to stateFilePath (via saveSentinel) so it survives a crash
// or restart, not just a graceful Stop().
func (e *Engine) setLastFolder(f string) {
	e.lastFolderMu.Lock()
	e.lastFolder = f
	e.lastFolderMu.Unlock()
	saveSentinel(f, e.getPMode())
}

// pmodeRef lets saveSentinel read the live P-row mode without threading it
// through every call site.
func (e *Engine) setPModeRef(t *target.State) {
	e.lastFolderMu.Lock()
	e.pmodeRef = t
	e.lastFolderMu.Unlock()
}

func (e *Engine) getPMode() string {
	e.lastFolderMu.Lock()
	defer e.lastFolderMu.Unlock()
	if e.pmodeRef == nil {
		return ""
	}
	return e.pmodeRef.Get()
}

// trackDeckState applies the deck-state side effects of a press while another
// app is in front (see the call site). It returns the new slider mode when the
// press was Hue / Sat / Lum, so the caller can persist it (kept out of here so
// tests never write the sentinel file).
func (e *Engine) trackDeckState(control string, pm *target.State) (newMode string) {
	if m, ok := hardwiredPMode(control); ok {
		pm.Set(m)
		newMode = m
		log.Printf("[note_on] control=%q pmode=%q (other app in front)", control, m)
		e.pushLEDs()
	}
	if control == "CustomMode" {
		e.toggleCustomMode()
	}
	return newMode
}

func (e *Engine) toggleCustomMode() {
	on := !e.customMode.Load()
	e.customMode.Store(on)
	log.Printf("[note_on] CustomMode toggle -> %v", on)
	e.pushLEDs()
}

// SetDeckState sets the slider mode and/or the Custom Mode toggle exactly as a
// press on the deck would (same state, same sentinel save, same LED / editor
// push), for the editor's clickable deck picture. It is internal state with no
// key output, so it works while the deck is disarmed. mode "" = leave it;
// custom nil = leave it.
func (e *Engine) SetDeckState(mode string, custom *bool) error {
	e.lastFolderMu.Lock()
	ref := e.pmodeRef
	e.lastFolderMu.Unlock()
	if mode != "" {
		if _, ok := hardwiredPMode(map[string]string{"hue": "Hue", "sat": "Sat", "lum": "Lum"}[mode]); !ok {
			return fmt.Errorf("sliderMode must be hue, sat or lum, got %q", mode)
		}
		if ref == nil {
			return errors.New("the engine is not running")
		}
		ref.Set(mode)
		saveSentinel(e.getLastFolder(), mode)
	}
	if custom != nil {
		e.customMode.Store(*custom)
	}
	log.Printf("[deck-state] set from the editor: sliderMode=%q customMode=%v", e.getPMode(), e.customMode.Load())
	e.pushLEDs()
	return nil
}

// SliderMode is the live P-row mode ("hue", "sat", "lum"; "" before Start). Read
// directly, not from the polled Status, so the editor sees a press at once.
func (e *Engine) SliderMode() string { return e.getPMode() }

// CustomMode is the engine-tracked Custom Mode toggle.
func (e *Engine) CustomMode() bool { return e.customMode.Load() }

// LEDSink receives the engine's deck-light state whenever it changes. The engine
// itself sends no MIDI: the app installs a sink only when LED output is switched
// on (see internal/leds). Called from the MIDI callback goroutine: must not block.
type LEDSink func(mode string, custom bool)

// SetLEDSink installs (or, with nil, removes) the LED sink and pushes the
// current state once.
func (e *Engine) SetLEDSink(f LEDSink) {
	if f == nil {
		e.ledSink.Store(nil)
		return
	}
	e.ledSink.Store(&f)
	f(e.getPMode(), e.customMode.Load())
}

func (e *Engine) pushLEDs() {
	if f := e.ledSink.Load(); f != nil {
		(*f)(e.getPMode(), e.customMode.Load())
	}
	if f := e.stateHook.Load(); f != nil {
		(*f)(e.getPMode(), e.customMode.Load(), int(e.layerNow.Load()))
	}
}

// StateSink is the editor's deck-state listener: the slider mode, Custom Mode and
// the active layer, pushed the moment any of them changes.
type StateSink func(mode string, custom bool, layer int)

// noteLayer reports the active layer (1-3) to the editor through the same hook, and so
// the same /api/events push, as a Hue / Sat / Lum change. A repeat is not re-sent.
func (e *Engine) noteLayer(n int) {
	if n < 1 || int(e.layerNow.Swap(int32(n))) == n {
		return
	}
	if f := e.stateHook.Load(); f != nil {
		(*f)(e.getPMode(), e.customMode.Load(), n)
	}
}

// SetStateHook installs (nil removes) the listener the editor uses to hear a
// Hue / Sat / Lum / Custom Mode change the moment the engine makes it (no
// polling in the path). Same contract as LEDSink: called on the MIDI callback
// goroutine, must not block.
func (e *Engine) SetStateHook(f StateSink) {
	if f == nil {
		e.stateHook.Store(nil)
		return
	}
	e.stateHook.Store(&f)
}

// hardwiredPMode maps the three engraved keys to their mode. Used when another
// app is in front: Resolve's own bindings (type pmode) are not consulted there,
// but the mode is one piece of deck state and must follow the keys in every app.
func hardwiredPMode(control string) (string, bool) {
	switch control {
	case "Hue":
		return "hue", true
	case "Sat":
		return "sat", true
	case "Lum":
		return "lum", true
	}
	return "", false
}

// getActiveTrack returns the D1-selected video track number, or 0 if unset
// ("auto" — Edit-page property/reset dials use Resolve's own
// GetCurrentVideoItem() instead of a pinned track).
func (e *Engine) getActiveTrack() int {
	e.activeTrackMu.Lock()
	defer e.activeTrackMu.Unlock()
	return e.activeTrack
}

// setActiveTrack pins the Edit-page property/reset dials to the given video
// track number, as selected by the D1 knob's selectClip rotation
// (dispatchSelectClip).
func (e *Engine) setActiveTrack(track int) {
	e.activeTrackMu.Lock()
	e.activeTrack = track
	e.activeTrackMu.Unlock()
}

// getSelection returns the current direction-button clip selection (see
// Engine.selection's doc comment above). The zero value (active==false)
// means nothing is selected.
func (e *Engine) getSelection() clipSelection {
	e.selectionMu.Lock()
	defer e.selectionMu.Unlock()
	return e.selection
}

// setSelection records a new direction-button clip selection -- called by
// dispatchSelectClipVertical/dispatchSelectClipHorizontal on a successful
// sidecar response. Callers must ALSO call setActiveTrack(s.track) so
// bump_property/reset_property's "track" override stays in lockstep (see
// Engine.selection's doc comment) -- setSelection alone does not touch
// activeTrack.
func (e *Engine) setSelection(s clipSelection) {
	e.selectionMu.Lock()
	e.selection = s
	e.selectionMu.Unlock()
}

// clearSelection resets the direction-button clip selection back to
// inactive -- called by dispatchSelectClipClear (C5) and the drain loop's
// playhead-left-the-clip auto-reset. Callers must ALSO call
// setActiveTrack(0), same lockstep requirement as setSelection.
func (e *Engine) clearSelection() {
	e.selectionMu.Lock()
	e.selection = clipSelection{}
	e.selectionMu.Unlock()
}

// isDuplicateNoteOn reports whether `control`'s note_on PRESS arrived within
// `window` of the last one this Engine processed for the SAME control (BUG
// 1, 2026-08-12 live report). The Loupedeck+ driver is known to emit ONE
// physical press as several (4-5 observed live) identical note_on messages
// within a few milliseconds. That's harmless for a level-style binding
// (fineModeHold: repeated presses just set the same "on" state again), but
// for a TOGGLE binding (the motivating case: col.C1/edit.C1 -> key Space,
// play/pause) each duplicate re-fires the toggle, so an odd-sized burst
// plays and an even-sized burst nets to nothing.
//
// Called from Start()'s MIDI callback for every vel>0 note_on, before ANY
// other intercept (layer switch, pmode switch, hardwired nav,
// D1/marker/direction-select direct checks, and resolveNoteBank/
// dispatchNoteOn) -- so it debounces uniformly regardless of binding type,
// correct whether the duplicate originates in the device or the MIDI driver
// layer. Never consulted for a release (a real Note Off OR a note-on with
// vel==0 -- see msg.GetNoteEnd in Start()'s callback, which returns before
// reaching this check for either convention), so a fast genuine
// press-then-release pair (e.g. a quick fineModeHold tap) is never mistaken
// for "the same event twice".
//
// Lazily initializes noteDebounceLast on first use so a fresh Engine (New())
// needs no extra wiring. Only ever called from the single MIDI callback
// goroutine in production, but mutex-guarded regardless (cheap, and matches
// every other Engine state accessor in this file).
func (e *Engine) isDuplicateNoteOn(control string, window time.Duration) bool {
	e.noteDebounceMu.Lock()
	defer e.noteDebounceMu.Unlock()
	now := time.Now()
	if last, ok := e.noteDebounceLast[control]; ok && now.Sub(last) < window {
		return true
	}
	if e.noteDebounceLast == nil {
		e.noteDebounceLast = map[string]time.Time{}
	}
	e.noteDebounceLast[control] = now
	return false
}

// ---------------------------------------------------------------------------
// Part B (2026-08-12 REV 2 design review): held-modifier CHORD OVERLAY
// state. setHeldOverlay/clearHeldOverlay are called from the MIDI callback
// goroutine (a modifierHold binding's press/release, in Start()); getHeldOverlay
// is read from that same goroutine (resolveOverlayBinding, on every
// subsequent note_on) AND from the drain/status-tick goroutine (Status().
// HeldOverlay, for the HUD). All three are mutex-guarded regardless, matching
// every other Engine state accessor in this file.
// ---------------------------------------------------------------------------

// setHeldOverlay sets the currently-held modifier overlay name (e.g. "cmd").
// A later press simply OVERWRITES whatever was held before -- Part B ships
// exactly one modifierHold button (Cmd), so unlike the reverted design's
// heldModifierSet there is no "two held at once" case to arbitrate. Also
// idempotent for the same reason: re-pressing an already-held modifier just
// assigns the same value again.
func (e *Engine) setHeldOverlay(name string) {
	e.heldOverlayMu.Lock()
	e.heldOverlayName = name
	e.heldOverlayMu.Unlock()
}

// clearHeldOverlay clears the overlay ONLY if it currently equals name --
// this is what makes release IDEMPOTENT and safe against a stray note-off
// for a DIFFERENT modifier than the one actually held. Two reasons this
// matters, both real: (1) the note_on debounce (commit c2b73a5) only guards
// PRESS (vel>0); it does not cover a release, whether a real Note Off or a
// vel==0 note-on, so a duplicate physical release can deliver several
// note-offs for the same control, and every one of them must be a safe
// no-op past the first; (2) an unconditional clear would let
// a stray/mis-wired release for a modifier this engine never itself pressed
// silently wipe out a genuinely-held DIFFERENT one.
func (e *Engine) clearHeldOverlay(name string) {
	e.heldOverlayMu.Lock()
	if e.heldOverlayName == name {
		e.heldOverlayName = ""
	}
	e.heldOverlayMu.Unlock()
}

// getHeldOverlay reads the currently-held modifier overlay name, or "" when
// nothing is held.
func (e *Engine) getHeldOverlay() string {
	e.heldOverlayMu.Lock()
	defer e.heldOverlayMu.Unlock()
	return e.heldOverlayName
}

// getActiveMarkerFrame returns the C1 marker-mode's active marker's frame
// (start position), or -1 if marker mode is inactive.
func (e *Engine) getActiveMarkerFrame() int {
	e.markerMu.Lock()
	defer e.markerMu.Unlock()
	return e.activeMarkerFrame
}

// setActiveMarker ENTERS marker mode, pinning the ControlDial (resize)
// and P1-8btn (recolor) controls to the marker at `frame` -- called by
// dispatchAddMarker after a successful C1 add_marker send, with the
// EXACT frame/color/durationSeconds that call used, so
// Status.MarkerMode's text is accurate immediately, before any
// resize/recolor has happened yet. Each call fully overwrites any
// previously-active marker -- "each C1 press creates a fresh marker +
// makes it active" (see the design doc comment above dispatchAddMarker).
func (e *Engine) setActiveMarker(frame int, color string, durationSeconds float64) {
	e.markerMu.Lock()
	e.activeMarkerFrame = frame
	e.activeMarkerColor = color
	e.activeMarkerDurationSeconds = durationSeconds
	e.markerMu.Unlock()
}

// clearActiveMarker EXITS marker mode (ControlDial press) --
// activeMarkerFrame reverts to -1 so the ControlDial resumes normal jog
// and P1-8btn presses resume their no-op on the very next event.
func (e *Engine) clearActiveMarker() {
	// Clear the auto-exit range alongside, so a stale range can never make a
	// freshly-created marker auto-exit immediately.
	defer e.setActiveMarkerAbsRange(0, 0)

	e.markerMu.Lock()
	e.activeMarkerFrame = -1
	e.markerMu.Unlock()
}

// updateActiveMarkerDuration refreshes the marker-mode duration display
// (Status.MarkerMode) after a successful resize_marker response -- see
// dispatchMarkerResize. A no-op if marker mode is no longer active (a
// resize response landing just after a ControlDial-press exit raced
// through) -- guards against resurrecting stale duration text for an
// already-inactive marker.
func (e *Engine) updateActiveMarkerDuration(seconds float64) {
	e.markerMu.Lock()
	if e.activeMarkerFrame >= 0 {
		e.activeMarkerDurationSeconds = seconds
	}
	e.markerMu.Unlock()
}

// updateActiveMarkerColor refreshes the marker-mode color display
// (Status.MarkerMode) after a successful set_marker_color response -- see
// dispatchSetMarkerColor. Same inactive-guard rationale as
// updateActiveMarkerDuration.
func (e *Engine) updateActiveMarkerColor(color string) {
	e.markerMu.Lock()
	if e.activeMarkerFrame >= 0 {
		e.activeMarkerColor = color
	}
	e.markerMu.Unlock()
}

// markerModeStatus formats Status.MarkerMode from the current marker-mode
// state -- "" when inactive, else e.g. "active @1.5s Blue". A pure read
// of local Go state (no sidecar call, no blocking) -- called every 50ms
// drain tick alongside the other Status fields (Page/ActiveWheel/Mode);
// stays cheap because the underlying color/duration are only ever
// refreshed ON CHANGE, by setActiveMarker/updateActiveMarkerDuration/
// updateActiveMarkerColor/clearActiveMarker, never by polling the
// sidecar here.
func (e *Engine) markerModeStatus() string {
	e.markerMu.Lock()
	frame := e.activeMarkerFrame
	color := e.activeMarkerColor
	seconds := e.activeMarkerDurationSeconds
	e.markerMu.Unlock()
	if frame < 0 {
		return ""
	}
	return fmt.Sprintf("active @%.1fs %s", seconds, color)
}

// SetResourcesDir overrides the base directory passed to sidecar.Launch
// (whose "sidecar" subfolder must hold sidecar.py). New() defaults this to
// "." (repo root), correct for the CLI and for a Wails app running from the
// repo in dev mode. A bundled .app must call SetResourcesDir with its
// Contents/Resources path BEFORE Start() — added as a setter rather than a
// New() parameter to keep New(cfgDir string)'s signature stable for existing
// callers (cmd/2t5-deck-plus/main.go, engine_test.go).
func (e *Engine) SetResourcesDir(dir string) {
	e.resourcesDir = dir
}

// SetToggleHUDCallback wires the callback invoked by a C6 "toggleHud" button
// press (dispatchNoteOn's IsToggleHud() case). fn should flip the native HUD
// panel's visibility and return the new state (matching internal/hud.Toggle's
// signature) — app.NewApp wires this to a.ToggleHUD BEFORE Start(). Same
// single-assignment-before-Start lifecycle as SetResourcesDir above, so no
// mutex is needed. Never called by cmd/2t5-deck-plus/main.go (the headless CLI
// has no NSApplication for an NSPanel to attach to) — leaving e.toggleHUD nil
// there, which dispatchNoteOn's IsToggleHud() case treats as a safe no-op.
func (e *Engine) SetToggleHUDCallback(fn func() bool) {
	e.toggleHUD = fn
}

// ErrAlreadyRunning is returned by Start when the engine is already running.
// A named sentinel (rather than an ad-hoc fmt.Errorf) so callers — notably
// app.StartEngine, which auto-starts the engine on app launch AND still
// exposes a manual Start button — can distinguish "harmless, already doing
// what you asked" from a genuine start failure via errors.Is, instead of
// string-matching the message.
var ErrAlreadyRunning = errors.New("engine: already running")

// Start dials the sidecar, registers the MIDI listener, and launches the
// 50ms drain-tick loop as a goroutine. It returns as soon as that goroutine is
// launched — it does not block for the lifetime of the engine.
//
// The "already running?" check and the "claim the running slot" write MUST
// happen inside ONE critical section (see below) -- 2026-08-12 live
// incident: they used to be two SEPARATE lock/unlock sections (check here,
// unconditionally unlock, then not set e.status.Running=true until deep
// inside the function body, AFTER the MIDI listener was already
// registered — roughly 800 lines and every bit of sidecar/MIDI setup
// later). Any call to Start() that overlapped that window also passed the
// check and proceeded, registering its OWN independent MIDI listener with
// its OWN Start()-local closure state (bank/layerState/pmodeState/
// nodeAtomic/fineModeAtomic) — confirmed live via a 10-goroutine
// concurrent-Start() stress test: all 10 passed, all 10 registered a
// listener, and `go test -race` caught genuine unguarded data races on
// shared Engine fields written from both overlapping calls. Reported
// live symptom: a single physical button press dispatching through
// MULTIPLE independently-registered listeners, each running the
// (individually correct) per-control debounce against the SAME shared
// noteDebounceLast map — but the debounce cannot distinguish "N genuine
// rapid presses" from "one press delivered to N listeners" (CoreMIDI
// correctly, by design, fans one physical event out to every registered
// listener on a port), so a toggle-type binding (e.g. C1 -> Space,
// play/pause) nets to "played then instantly paused again" whenever an
// even number of listeners are alive. See TestStart_ConcurrentCalls_
// OnlyOneMIDIListenerRegistered (start_race_test.go) for the reproducing
// regression test.
// cachedStatus is a point-in-time snapshot of the sidecar's "status" op,
// cached across drain ticks (see statusCacheTTL) and read/written from
// BOTH the MIDI-callback goroutine (queryPage()) and the ticker/drain
// goroutine inside Start() (statusCacheMu-guarded there). Promoted out of
// Start()'s body (2026-08-25, the "live HUD timecode" fix) specifically so
// predictedTimecode below can be a standalone, directly unit-testable
// function -- Start() itself is thousands of lines and effectively
// untestable as a unit, the same reason every OTHER per-tick helper in
// this file (maybeAutoExitMarker, shouldResizeMarker, ...) is already a
// package-level function taking its inputs as plain parameters rather than
// closing over Start()'s local state.
type cachedStatus struct {
	page          string
	currentNode   map[string]any
	timecode      string
	timelineName  string
	timelineDurTC string
	clipInTC      string
	clipOutTC     string
	clipDurTC     string
	clipHeadTC    string
	clipTailTC    string
	tlClipTotal   int
	tlClipRender  int
	tlMarkers     int
	tlLEDs        string
	// clipID is GetCurrentVideoItem().GetUniqueId() (the TOP clip at
	// the playhead), read alongside timecode/timelineName from the
	// same "status" round trip -- "" means no current clip (or the
	// status op didn't succeed this tick, see ok below). Used by the
	// drain loop's D1 auto-reset (a tick-over-tick CHANGE in this
	// value means the playhead moved to a different clip).
	clipID string
	// playheadFrame is the absolute playhead frame number (see
	// ResolveDriver.current_playhead_frame), read alongside clip_id/
	// timecode/etc from the same "status" round trip. Consumed by the
	// direction-button (Up/Down/Left/Right) selection's auto-reset --
	// a stored selection's [start,end) range is compared against this
	// value every tick to detect the playhead has left the selected
	// clip. havePlayheadFrame gates on whether THIS tick's status
	// response actually carried a numeric value (JSON null/absent/
	// non-numeric all leave it false) -- same "only act on a genuine
	// successful read" rationale as clipID/ok's relationship for the
	// OLDER D1 clip-id auto-reset below. Any fake-sidecar test double
	// that doesn't include "playhead_frame" in its status reply (every
	// pre-existing D1/highlight/marker test fixture) simply leaves
	// this false forever, so the NEW auto-reset structurally never
	// fires against those fixtures.
	playheadFrame     int
	havePlayheadFrame bool
	// fps is the timeline's frame rate ("timeline_fps" in the "status"
	// response), read alongside playheadFrame -- 0 when absent/non-numeric
	// (no live timeline). Added 2026-08-25 alongside confirmedAt so
	// predictedTimecode has everything it needs to extrapolate the
	// playhead forward during a stale (timed-out) tick without a second
	// sidecar round trip -- see predictedTimecode's doc comment.
	fps float64
	// confirmedAt is the wall-clock instant THIS snapshot's playheadFrame/
	// fps were genuinely read from Resolve (set only in queryStatus's
	// success branch, never on a stale/carried-forward copy). Deliberately
	// separate from `at` below: `at` is touched on EVERY queryStatus call
	// (success or timeout, see queryStatus's doc comment) and so cannot
	// answer "how long has it actually been since we last knew the truth"
	// once several consecutive timeouts have each stamped `at` with their
	// own attempt time. A stale tick's cachedStatus is a struct COPY of
	// the last successful one (see queryStatus's ErrTimeout branch), so
	// confirmedAt silently rides along unchanged across any number of
	// consecutive stale ticks -- exactly the "how long has it been since
	// truth" clock predictedTimecode needs.
	confirmedAt time.Time
	// editClipName/editClipPan/editClipTilt/editClipZoom are the HUD
	// v2 Edit-page target-clip readout (see Status.EditClipName's doc
	// comment) -- read from this tick's "edit_clip" sidecar response,
	// alongside clipID/playheadFrame/etc. haveEditClip gates whether
	// they're populated THIS tick, same "only act on a genuinely
	// successful read" rationale as havePlayheadFrame above.
	editClipName string
	editClipPan  float64
	editClipTilt float64
	editClipZoom float64
	haveEditClip bool
	// ok is true only for a genuinely successful sidecar round trip
	// this tick (client!=nil AND client.Send returned no error) --
	// FALSE for the client==nil/send-error fallback paths below.
	// Without this distinction the D1 auto-reset could not tell "the
	// clip genuinely changed to none" (act on it) apart from "we
	// simply failed to reach the sidecar this tick" (must NOT act on
	// it -- a transient hiccup must never spuriously clear a D1
	// selection) -- clipID's consumer is uniquely a CHANGE-detector,
	// not a display value.
	//
	// UPDATED 2026-08-25: page/timecode/etc. no longer "degrade to
	// blank/dashes" on every ok=false tick the way this comment used
	// to say -- see stale below. They still zero out on the
	// client==nil/hard-transport-failure paths (queryStatus's
	// pre-existing posture for those, unchanged), just not on ANY
	// "status" timeout -- every timeout, regardless of count or
	// duration, is now treated as busy-but-alive (see queryStatus's
	// 2026-08-25 doc comment).
	ok bool
	// stale is true on every "status" timeout (see queryStatus's
	// 2026-08-25 doc comment: a timeout never triggers recovery, full
	// stop): this snapshot's page/timecode/timelineName/etc. are
	// CARRIED FORWARD from the last genuinely successful tick
	// (statusCache itself), not freshly read this tick -- ok is
	// simultaneously false (this tick's OWN round trip did not
	// succeed). Exists so a caller (Status.SidecarStale, via
	// formatHUD) can tell "these are real numbers from a moment ago,
	// Resolve is just busy" apart from "these are zero values because
	// nothing has ever succeeded, or the sidecar is genuinely gone"
	// (stale=false in both of those).
	stale bool
	at    time.Time
}

// maxTimecodePredictionWindow bounds how far past the last CONFIRMED read
// predictedTimecode will keep extrapolating before giving up and freezing.
// Chosen with headroom over this file's own documented worst observed
// single "status" stall (8.3s, a large-timeline cache-miss round trip --
// see queryStatus's doc comment), while staying far short of "minutes" --
// a sidecar that is genuinely PERMANENTLY wedged is an accepted,
// already-documented long-term state elsewhere in this file (queryStatus's
// 2026-08-25 fix: "serves stale values indefinitely until relaunched by
// hand"), and a predicted clock that kept counting forward for that whole
// time, unattended, would be actively fabricating a number rather than
// honestly saying "I don't know anymore." Not a hard product spec -- a
// judgment call, deliberately generous enough to bridge ordinary
// multi-second GIL stalls during heavy playback without ever visibly
// freezing, per the user's ask.
const maxTimecodePredictionWindow = 10 * time.Second

// predictedTimecode computes the HUD's displayed Timecode for one drain
// tick, given this tick's cachedStatus snapshot and the tick's own
// captured `now` (the drain loop's existing per-tick `now := time.Now()`,
// reused here rather than calling time.Now() again -- one wall-clock read
// per tick, same posture as every other per-tick timestamp in this file).
//
// The BUG this fixes (2026-08-25, live report): the HUD timecode froze
// during playback and only "jumped to truth" on pause. Root cause: once a
// "status" round trip times out, queryStatus's 2026-08-25 GIL fix (see its
// doc comment) correctly stops recovery from firing, but the SNAPSHOT it
// serves is a frozen carry-forward of the last successful read -- there is
// nothing to read that says how far the playhead has actually moved since
// then, because Resolve's scripting API cannot answer while the GIL is
// held. The chosen fix (user's explicit decision, not this function's own
// invention): compute it locally. Extrapolate from the last CONFIRMED
// read -- predicted = last_confirmed_frame + elapsed_wall_clock * fps --
// resynced to truth on every genuinely successful read, no extra sidecar
// calls added.
//
// The not-stale case is byte-identical to before this feature: a fresh
// read just returns s.timecode -- Resolve's own GetCurrentTimecode()
// string, including "" (no live timeline), which formatHUD already
// renders as dashes.
//
// Resync falls out of the caller's own branching for free: this function
// is only ever consulted for a STALE tick (see the drain loop's call
// site) -- the instant a real read lands, the caller uses that tick's
// s.timecode directly and predictedTimecode is not even called, so there
// is no separate "snap back to truth" step to get wrong.
//
// The user was shown, and explicitly accepted, that a drifted/predicted
// value renders IDENTICALLY to a confirmed one (no visual "this is a
// guess" distinction) -- the mitigation is resyncing hard and often
// (above) plus this window (below), not styling.
//
// Honestly-acknowledged limitations, all inherent to extrapolating with
// ZERO play-state signal -- DaVinci Resolve's scripting API exposes no
// IsPlaying/play-rate/direction of any kind (confirmed: no such call
// exists anywhere in this codebase's sidecar, and Resolve's own scripting
// docs have no equivalent), so the sidecar genuinely cannot ask:
//   - Playback STOPS but reads are still timing out (the GIL can lag a
//     beat behind an actual pause): the prediction keeps counting forward
//     for up to maxTimecodePredictionWindow past the last real confirm,
//     then freezes -- a bounded, self-correcting overshoot, not an
//     unbounded one.
//   - Reverse playback: this function has no way to know direction, so it
//     always extrapolates FORWARD. During a stale window while actually
//     playing in reverse, the displayed number will be wrong (counting
//     the wrong way) until the next real read snaps it back.
//   - Jog/scrub while a read happens to be timing out: same story -- an
//     assumed-forward-1x guess is wrong for whatever the actual jog
//     speed/direction was, self-corrects on the next real read.
//   - A non-1.0 play rate (J-K-L shuttle speeds): extrapolation always
//     assumes the LAST CONFIRMED fps at nominal 1x -- a 2x/4x/0.5x
//     shuttle will show the wrong speed until the next real read.
//
// All four are the same underlying limitation (no ground truth exists
// during a stale window, by construction) and all four are bounded by the
// same two mechanisms: maxTimecodePredictionWindow (never extrapolate
// further than this past the last real confirm) and resync-on-next-read
// (the moment truth is available again, it wins immediately,
// unconditionally, every tick).
//
// The freeze itself (elapsed beyond the window) falls back to s.timecode
// -- the plain last-confirmed string, i.e. exactly this feature's
// pre-existing frozen behavior -- not to whatever the prediction had most
// recently reached, so a permanently-wedged sidecar shows an honestly
// frozen number rather than a fabricated one that just stopped moving at
// an arbitrary point.
func predictedTimecode(s cachedStatus, now time.Time) string {
	if !s.stale {
		return s.timecode
	}
	if !s.havePlayheadFrame || s.fps <= 0 {
		// Can't extrapolate without a frame baseline and a frame rate --
		// same fallback as the freeze case below, for the same reason.
		return s.timecode
	}
	elapsed := now.Sub(s.confirmedAt)
	if elapsed < 0 || elapsed > maxTimecodePredictionWindow {
		return s.timecode
	}
	predictedFrame := s.playheadFrame + int(elapsed.Seconds()*s.fps)
	return tc.FramesToTC(predictedFrame, s.fps)
}

func (e *Engine) Start(ctx context.Context) error {
	e.mu.Lock()
	if e.status.Running {
		e.mu.Unlock()
		return ErrAlreadyRunning
	}
	// Claim the slot in the SAME critical section as the check above --
	// this is what actually closes the race (see the doc comment). Rolled
	// back to false on the MIDI-open failure path below (the only
	// early-return failure path between here and the old, now-redundant,
	// "Running is the only new fact here" assignment that used to sit
	// after MIDI registration) so a genuine retry after a real failure is
	// never permanently blocked.
	e.status.Running = true
	e.mu.Unlock()
	e.hooks.reopenObserver() // a restart after Stop delivers events again

	runCtx, cancel := context.WithCancel(ctx)

	// --- Lazy Resolve sidecar (spec 2026-08-30 section 4.3) ---
	// Start() NEVER launches, dials or waits for the sidecar. Resolve is
	// only one of the apps this deck serves, and the sidecar is useless
	// without it, so a supervisor goroutine (superviseSidecar, started
	// after the MIDI port is open) brings the sidecar up only when Resolve
	// is running AND frontmost, retries with backoff, and gives up quietly
	// when Resolve quits. Until it connects, getClient() returns nil and
	// every nil-client suppression path below behaves exactly as it always
	// did for "sidecar not connected". Absence of Resolve is the normal
	// case for a deck that also drives other apps, so it is NOT an error:
	// Status().SidecarState is "waiting" and SidecarError stays empty.
	getClient := e.getClient
	e.clientPtr.Store(nil)
	e.mu.Lock()
	e.status.SidecarConnected = false
	e.status.SidecarError = ""
	e.status.SidecarState = SidecarStateWaiting
	e.mu.Unlock()

	acc := accel.New(0.002, 1.5)
	r := router.New(liveCfg{e}, acc, 1)
	accum := coalesce.New()

	// bank is read from both the MIDI-callback goroutine (note_on binding
	// resolution + the "Col" nav press) and the ticker/drain goroutine
	// (binding resolution for drained CC events) — guarded by bankMu.
	//
	// node was ORIGINALLY only ever touched from within the MIDI-callback
	// goroutine (Left/Right hardwired nav + the CDL reset_param command), so
	// it needed no lock of its own. router.Router's own node field DOES
	// cross goroutines (SetNode called from here vs Route called from the
	// drain loop below) — that race is fixed with a dedicated mutex inside
	// internal/router, not here.
	//
	// HUD v2 promoted node to nodeAtomic (atomic.Int32): Status.Node (added
	// for the HUD's diagnostic foot row) is read from the ticker/drain
	// goroutine every tick, a NEW cross-goroutine reader the plain int
	// above was never safe against — same rationale as bank/bankMu just
	// above (bank got its mutex when the drain loop became a second
	// reader; node gets an atomic now for the identical reason). Every
	// existing read/write site (Left/Right nav, reset_param's "node"
	// field) still lives exclusively in the MIDI-callback goroutine and is
	// updated to Load/Store below — the atomic costs them nothing.
	bank := "col"
	var bankMu sync.Mutex
	getBank := func() string {
		bankMu.Lock()
		defer bankMu.Unlock()
		return bank
	}
	// setBank is gone: its only caller was the vestigial `case "Col"` no-op,
	// now replaced by the layer switch. bank/getBank survive solely to feed
	// the fineModeHold lookup and Status.Bank, both of which Task 15 retires.

	// layerState is the console's active layer (1-3). A shared-pointer object
	// rather than a closure like bank above, because the seven direct
	// MIDI-callback intercepts and dispatchNoteOn all need to read it and
	// none of them close over this scope. See internal/layer.
	layerState := layer.New()
	// setLayer is every layer change the engine makes: set it, then tell the editor at once.
	setLayer := func(n int) { layerState.Set(n); e.noteLayer(layerState.Get()) }
	e.hooks.setLayerState(layerState)

	// pmodeState is the P1-P8 mode ("hue"/"sat"/"lum"), selected by the
	// console's engraved Hue/Sat/Lum keys. internal/target is a generic
	// named-string holder, so it fits without a new package.
	pmodeState := target.New()
	pmodeState.Set("hue")
	if e.savedPMode != "" {
		pmodeState.Set(e.savedPMode)
		log.Printf("[state] P-row mode restored: %q", e.savedPMode)
	}
	e.setPModeRef(pmodeState)
	e.pushLEDs()

	// prevPageForLayer is the last page the status ticker successfully read,
	// used to detect page changes for the layer auto-reset. Only ever written
	// by the ticker goroutine, so it needs no mutex.
	prevPageForLayer := ""
	var nodeAtomic atomic.Int32
	nodeAtomic.Store(1)

	modeTracker := e.modeTracker
	aw := e.aw
	cfg := e.cfg.Load() // startup-time reads only; per-event paths shadow it with a fresh Load
	cal := e.cal

	// D1 active-clip highlight feature config (see config.TopLevel and
	// dispatchHighlightClip). Read once here rather than on every drain
	// tick — bindings.json is not hot-reloaded mid-run.
	// (per-event/per-tick read for hot reload, see below)

	// C1 marker-mode ControlDial resize tuning (see dispatchMarkerResize)
	// — read once here, same rationale as highlightEnabled/highlightColor
	// above (bindings.json is not hot-reloaded mid-run).
	// (per-event/per-tick read for hot reload, see below)

	// --- Startup reset gate ---
	// In v8, there are no tracked offsets. Fire resyncAll on launch ONLY IF
	// startupReset = "always". Default ("never") skips. User uses L2 for explicit resync.
	{
		tl := cfg.TopLevel
		if tl.StartupReset == "always" && cal != nil {
			log.Printf("[startup] startupReset=always → firing resyncAll")
			for i, wheel := range []string{"lift", "gamma", "gain", "offset"} {
				if i > 0 {
					time.Sleep(50 * time.Millisecond)
				}
				executeResetWheel(wheel, cal)
			}
			log.Printf("[startup] resyncAll complete")
		} else {
			log.Printf("[startup] startupReset=%q → skipping startup reset", tl.StartupReset)
		}
		// Write sentinel regardless (forward-compatibility).
		saveSentinel(e.getLastFolder(), e.getPMode())
	}

	// fineMode is set true while a fineModeHold button is pressed.
	// Written from MIDI callback goroutine; read in drain goroutine.
	var fineModeAtomic int32

	// Sidecar status cache: includes page + current_node. statusCache is read
	// and written from BOTH the MIDI-callback goroutine (queryPage() reads
	// it, synchronously, from dispatchPressAction/dispatchNoteOn and the
	// direct-intercept checks while handling a knob press) and the
	// ticker/drain goroutine (the per-tick status snapshot below, the
	// balance-flush node/page guard, and processNonBalanceDrain's own
	// queryPage() use for the jog branch) — statusCacheMu guards every
	// access to statusCache.
	var statusCache cachedStatus
	var statusCacheMu sync.Mutex

	// queryStatus is the ONLY thing that ever performs a blocking sidecar
	// round trip to populate statusCache (on a cache-miss, via client.Send
	// -- see ipc.Client.Send's own deadline for the hard upper bound on
	// that round trip). It must NEVER be called from the MIDI callback
	// goroutine.
	//
	// ROOT CAUSE (2026-08-12, confirmed by the DIAG-*/`84a663e` diagnostic
	// build's live trace): C1/Space never actually double-fired. The
	// sidecar's "status" op does real work proportional to timeline size
	// (timecode, the full clip map, per-clip offline checks, edit_clip_
	// info) -- on a large timeline (live evidence: DODEE EP15) a cache-miss
	// round trip can take SECONDS. Before this fix, queryPage() called this
	// function directly, and the MIDI-callback goroutine calls queryPage()
	// up to a dozen times per note_on (every direct-intercept check below).
	// CoreMIDI queues incoming note events while its callback is blocked;
	// when queryStatus() finally returned, every queued press (e.g. two
	// Space presses) flushed within ~1ms of each other, so a single
	// physical press became a play+pause pair that cancelled out. Two
	// earlier, individually-correct fixes (`0a0ccdb` Start() TOCTOU,
	// `dea85c7` leaked CoreMIDI port) did not touch this and could not have
	// fixed it. The fix is queryPage() below, NOT this function --
	// queryStatus itself is correct and stays the sole populator, called
	// only from the ticker/drain goroutine (50ms tick, see the "go func()"
	// below) where blocking for a slow-but-alive sidecar is acceptable.
	//
	queryStatus := func() cachedStatus {
		statusCacheMu.Lock()
		if time.Since(statusCache.at) < statusCacheTTL {
			s := statusCache
			statusCacheMu.Unlock()
			return s
		}
		statusCacheMu.Unlock()

		if getClient() == nil {
			s := cachedStatus{at: time.Now()}
			statusCacheMu.Lock()
			statusCache = s
			statusCacheMu.Unlock()
			return s
		}

		// HUD v2 Edit-page readout: include "track" exactly like
		// bump_property/reset_property's own "if activeTrack != 0"
		// convention (see processNonBalanceDrain below), so the sidecar's
		// edit_clip_info() resolves the IDENTICAL clip the property/reset
		// knobs currently target -- the direction-button-selected
		// override when one is active, else its own top-occupied default.
		cmd := map[string]any{"op": "status"}
		if t := e.getActiveTrack(); t != 0 {
			cmd["track"] = t
		}
		resp, err := getClient().Send(cmd)
		if err != nil {
			// 2026-08-13 fix ("duplicate sidecar feedback loop" live
			// incident): a TIMEOUT (ipc.ErrTimeout) is NOT the same
			// signal as a hard transport failure (EOF/broken pipe/
			// connection refused). Before this, ANY Send error here --
			// including a mere timeout -- triggered recoverSidecar
			// unconditionally, which spawns a replacement sidecar
			// process. Live evidence: DaVinci Resolve's scripting API
			// goes briefly unresponsive while PLAYING BACK, and the
			// "status" op's own cost scales with timeline size (see this
			// function's doc comment on the 8.3s stall) -- on the user's
			// large DODEE EP15 timeline this fired reliably ~sendTimeout
			// after every Space/C1 press, diagnosing a perfectly
			// healthy, merely-busy sidecar as dead on the single most-
			// used button on the deck, and piling up duplicate sidecar
			// processes each time (see recoverSidecar's doc comment).
			//
			// 2026-08-24 fix (SECOND live incident, same root class): the
			// 2026-08-13 fix's own remedy was a COUNT --
			// maxConsecutiveStatusTimeouts, tolerating up to ~15s of
			// timeouts before escalating -- reasoned to be "generous
			// relative to a normal Resolve-playback blip". Live evidence
			// this same day proved that premise wrong: PLAYBACK IS NOT A
			// BLIP, it lasts as long as the user plays, so any playback
			// longer than ~15s still tripped recovery and killed a
			// healthy sidecar mid-work (log evidence: repeated "3
			// consecutive status timeouts -- treating as wedged" during
			// an ordinary play, "sidecar disconnect" flashing in the
			// HUD, both clearing the instant the user paused). A COUNT
			// of timeouts can never distinguish "busy" from "wedged" --
			// both produce the identical ipc.ErrTimeout, by construction
			// -- so no threshold value could have been correct. That
			// fix's own remedy (a ping probe on a second, dedicated
			// socket -- internal/ipc.PingAlive against sidecar.py's
			// PING_SOCK) was tried and DEFEATED THE VERY NEXT DAY
			// (2026-08-25): the probe's second socket was still served by
			// a `threading.Thread` inside the SAME Python process, and
			// CPython's GIL is process-wide -- while the main thread
			// blocks inside fusionscript.so (Resolve's C extension, which
			// does not release the GIL) servicing "status", NO other
			// Python thread in that process can run either, including the
			// ping listener. The probe timed out at exactly the moments
			// "status" did, so it reported "wedged or dead" for a
			// perfectly healthy, merely-busy sidecar on every playback (8
			// spurious recoveries in one live log excerpt). See the
			// Engine struct's field comment above (the block that used to
			// be consecutiveStatusTimeouts) for the full three-attempt
			// history -- do not retry either dead end.
			//
			// 2026-08-25 fix (THIRD attempt, current): the RULE changed
			// instead of the mechanism. Nothing in the sidecar process can
			// answer while it holds the GIL, so nothing should be asked
			// to. A TIMEOUT MUST NEVER TRIGGER RECOVERY -- not after a
			// count, not after any duration, not via a probe. Every
			// timeout, unconditionally, serves the LAST-KNOWN snapshot
			// (statusCache itself, not a blank one -- see
			// cachedStatus.stale's doc comment) so the HUD keeps showing
			// real numbers, marked stale, instead of flickering blank
			// every ~200ms while merely busy. A playback of any length can
			// never trip recovery: every tick during it takes this exact
			// branch and returns here, never falling through to
			// recoverSidecar below. Accepted, deliberate trade-off: a
			// sidecar that is alive but PERMANENTLY wedged on something
			// that is NOT a GIL-holding Resolve call will now never self-
			// recover on its own -- it serves stale values indefinitely
			// until the app is relaunched by hand. That is strictly better
			// than killing a healthy sidecar on every playback, and a
			// genuinely dead sidecar is never silently lost: the OS
			// reports that as a hard transport error (EOF/broken pipe/
			// connection refused), handled below, not as a timeout.
			if errors.Is(err, ipc.ErrTimeout) {
				statusCacheMu.Lock()
				s := statusCache
				s.at = time.Now()
				s.ok = false
				s.stale = true
				statusCache = s
				statusCacheMu.Unlock()
				return s
			}
			// BUG 3 (2026-08-12): a HARD Send failure -- anything that is
			// NOT ipc.ErrTimeout, i.e. EOF/broken pipe/connection refused/
			// ErrNotConnected -- is the only signal that means "the
			// sidecar is actually gone" (e.g. a DaVinci Resolve restart
			// taking it down). It kicks off an async, single-flighted,
			// backed-off recovery attempt rather than silently degrading
			// forever. See recoverSidecar's doc comment. Deliberately does
			// nothing to Status().SidecarConnected/SidecarError itself
			// here — recoverSidecar owns that transition (including the
			// "still trying, here's why it's failing" message), so this
			// call site stays a pure trigger. A snapshot on THIS path is a
			// blank one, not a stale carry-forward -- the sidecar is
			// presumed genuinely gone here, so showing minutes-old numbers
			// next to a "no Resolve link" banner would be actively
			// misleading, unlike the busy-but-alive branch above.
			e.recoverSidecar(runCtx, getClient())
			s := cachedStatus{at: time.Now()}
			statusCacheMu.Lock()
			statusCache = s
			statusCacheMu.Unlock()
			return s
		}
		var s cachedStatus
		s.at = time.Now()
		s.ok = true
		// confirmedAt marks THIS tick as genuine ground truth -- see
		// cachedStatus.confirmedAt's doc comment for why this must be a
		// separate stamp from `at` above (which every tick touches,
		// success or timeout). Set unconditionally here, alongside ok --
		// even a "no live timeline" reply is a genuinely successful round
		// trip, it just carries no playhead/fps to extrapolate from
		// (predictedTimecode's own havePlayheadFrame/fps>0 guard handles
		// that, not this line).
		s.confirmedAt = s.at
		if p, ok := resp["page"].(string); ok {
			s.page = p
		}
		if n, ok := resp["current_node"].(map[string]any); ok {
			s.currentNode = n
		}
		// HUD v2: Timecode + TimelineName, read alongside page/current_node
		// from the same "status" op round trip (see sidecar.py's status
		// handler + ResolveDriver.current_timecode). Both are nullable in
		// the JSON response (no live timeline) -- a missing/non-string
		// value leaves the zero value "", which formatHUD renders as
		// dashes/blank, never a stale value from a prior tick.
		if tc, ok := resp["timecode"].(string); ok {
			s.timecode = tc
		}
		if tn, ok := resp["timeline_name"].(string); ok {
			s.timelineName = tn
		}
		// Timeline length and the selected clip's in/out, rendered as
		// timecode here rather than in the sidecar so the frame->TC rule
		// lives in exactly one place (internal/tc.FramesToTC). All four are
		// cosmetic: a missing or non-numeric field simply leaves the string
		// empty and the HUD renders a placeholder.
		fps, _ := resp["timeline_fps"].(float64)
		// predictedTimecode's extrapolation baseline -- see cachedStatus.fps's
		// doc comment. Stored even when <= 0 (no live timeline); the
		// consumer gates on fps > 0 itself.
		s.fps = fps
		if d, ok := resp["timeline_duration_frames"].(float64); ok && fps > 0 {
			s.timelineDurTC = tc.FramesToTC(int(d), fps)
		}
		if cs, ok := resp["clip_start"].(float64); ok && fps > 0 {
			s.clipInTC = tc.FramesToTC(int(cs), fps)
		}
		if ce, ok := resp["clip_end"].(float64); ok && fps > 0 {
			s.clipOutTC = tc.FramesToTC(int(ce), fps)
		}
		if cd, ok := resp["clip_duration"].(float64); ok && fps > 0 {
			s.clipDurTC = tc.FramesToTC(int(cd), fps)
		}
		// Handles: how many frames of source remain beyond each edge -- the
		// reading that answers "how far can I extend this".
		//
		// Reported as a signed FRAME COUNT, not a timecode: a handle is a trim
		// quantity, not a position, and "-225" says how far the in point can
		// move far more directly than "00:00:09:00" does. Sign carries the
		// direction: head extends backwards, tail forwards.
		if ch, ok := resp["clip_head"].(float64); ok {
			s.clipHeadTC = fmt.Sprintf("%d", -int(ch))
		}
		if ct, ok := resp["clip_tail"].(float64); ok {
			s.clipTailTC = fmt.Sprintf("+%d", int(ct))
		}
		// Timeline census: clip count, how many are renders, marker count, and
		// a per-clip LED string for the status strip.
		if v, ok := resp["tl_clip_total"].(float64); ok {
			s.tlClipTotal = int(v)
		}
		if v, ok := resp["tl_clip_rendered"].(float64); ok {
			s.tlClipRender = int(v)
		}
		if v, ok := resp["tl_marker_count"].(float64); ok {
			s.tlMarkers = int(v)
		}
		if v, ok := resp["tl_leds"].(string); ok {
			s.tlLEDs = v
		}
		// D1 auto-reset: clip_id (see sidecar.py's status handler +
		// ResolveDriver.current_video_item_id) -- nullable JSON, same
		// "missing/non-string leaves zero value" shape as timecode/
		// timelineName above.
		if cid, ok := resp["clip_id"].(string); ok {
			s.clipID = cid
		}
		// Direction-button selection auto-reset: playhead_frame (see
		// sidecar.py's status handler + ResolveDriver.current_playhead_
		// frame) -- same nullable-JSON, "missing/non-numeric leaves the
		// zero value + a false flag" shape as clipID/ok above.
		if pf, ok := resp["playhead_frame"].(float64); ok {
			s.playheadFrame = int(pf)
			s.havePlayheadFrame = true
		}
		// HUD v2 Edit-page readout: edit_clip (see sidecar.py's status
		// handler + ResolveDriver.edit_clip_info) -- a nested object
		// {"name","pan","tilt","zoom"} or null/absent (no target clip this
		// tick). All four fields are only trusted together -- an object
		// present but missing any one of the three numeric fields (should
		// never happen from a well-formed sidecar reply, but a fake-test
		// sidecar or a future format drift shouldn't half-populate the
		// HUD) leaves haveEditClip false, same "only act on a genuinely
		// successful read" rationale as havePlayheadFrame above.
		if ec, ok := resp["edit_clip"].(map[string]any); ok {
			name, okName := ec["name"].(string)
			pan, okPan := ec["pan"].(float64)
			tilt, okTilt := ec["tilt"].(float64)
			zoom, okZoom := ec["zoom"].(float64)
			if okName && okPan && okTilt && okZoom {
				s.editClipName = name
				s.editClipPan = pan
				s.editClipTilt = tilt
				s.editClipZoom = zoom
				s.haveEditClip = true
			}
		}
		statusCacheMu.Lock()
		statusCache = s
		statusCacheMu.Unlock()
		return s
	}

	// queryPage returns the LAST-KNOWN cached page, never triggering a
	// fetch — see the "8.3 second stall" root-cause note below on
	// queryStatus. This is deliberately NOT "queryStatus().page": every
	// call site is either the MIDI-callback goroutine (dozens of direct-
	// intercept checks per note_on, see the callback below) or, via
	// processNonBalanceDrain, the SAME tick that just called queryStatus()
	// directly a few lines above it in the ticker loop — so the cache is
	// always fresh enough by the time this runs, and this must be
	// unconditionally non-blocking for the goroutine that matters (MIDI).
	// A page identity changes on human timescales (the user clicking a
	// Resolve page tab); serving a page value that is a tick or two stale
	// is invisible in practice, whereas blocking the MIDI callback even
	// once is a frozen deck. The ticker (50ms tick / queryStatus's own
	// 200ms TTL below) is the ONLY thing that ever populates statusCache —
	// queryPage merely peeks it.
	queryPage := func() string {
		statusCacheMu.Lock()
		defer statusCacheMu.Unlock()
		return statusCache.page
	}

	// activeClipCache holds the last computed Status.ActiveClip string,
	// keyed by which activeTrack it was computed for. Read/written only
	// from the drain/tick goroutine's per-tick status-snapshot block
	// below, so no mutex is required (unlike statusCache, which is also
	// touched from the MIDI-callback goroutine).
	type activeClipCache struct {
		value    string
		forTrack int
		at       time.Time
	}
	var clipCache activeClipCache

	// queryActiveClip resolves Status.ActiveClip for the current page +
	// D1-selected track. Reuses the SAME ~200ms cache cadence as
	// queryStatus (statusCacheTTL) rather than hitting the sidecar every
	// 50ms tick, but a track CHANGE always forces an immediate re-query
	// (bypassing the TTL) so the display never lags a fresh D1 turn.
	// Returns "" immediately, with no sidecar call at all, when off the
	// Edit page or when activeTrack is unset (0/"auto") — D1 has never
	// been turned yet, so there is no pinned target to show.
	queryActiveClip := func(page string, activeTrack int) string {
		if page != "edit" || activeTrack == 0 {
			return ""
		}
		if clipCache.forTrack == activeTrack && time.Since(clipCache.at) < statusCacheTTL {
			return clipCache.value
		}
		if getClient() == nil {
			return ""
		}
		resp, err := getClient().Send(map[string]any{"op": "clips_at_playhead"})
		result := ""
		if err == nil {
			if ok, clips := parseClipsAtPlayhead(resp); ok {
				for _, c := range clips {
					if c.Track == activeTrack {
						result = fmt.Sprintf("V%d · %s", activeTrack, c.Name)
						break
					}
				}
			}
		}
		clipCache = activeClipCache{value: result, forTrack: activeTrack, at: time.Now()}
		return result
	}

	// cstRe matches node names that are CST / Fusion color-space nodes.
	cstRe := regexp.MustCompile(`(?i)(CST|colorspacetransform|color\s*space\s*transform)`)

	// isRefusedByNodeGuard checks the sidecar status for CST/Fusion guard refusal.
	isRefusedByNodeGuard := func(s cachedStatus) (bool, string) {
		if s.page != "color" {
			return true, "page-not-color"
		}
		if s.currentNode != nil {
			name, _ := s.currentNode["name"].(string)
			if name != "" && cstRe.MatchString(name) {
				return true, "refused-cst"
			}
		}
		return false, ""
	}

	// Lazy sidecar supervisor: off the MIDI/input path, see
	// sidecar_supervisor.go. Started BEFORE the priming queryStatus() below
	// so (when Resolve is up) the status cache is primed against the live
	// client exactly as it was when Start() dialled synchronously. The
	// MIDI-open failure path unwinds it.
	var firstAttempt chan struct{}
	if startWaitsForFirstSidecarAttempt {
		firstAttempt = make(chan struct{})
	}
	e.supervisorWG.Add(1)
	go e.superviseSidecar(runCtx, firstAttempt)
	if firstAttempt != nil {
		<-firstAttempt
	}

	// Prime statusCache with ONE synchronous fetch before the MIDI port
	// opens (so no note_on can possibly reach the callback before this
	// completes -- registration is the very next step). Without this, the
	// very first press after a fresh Start() would see queryPage()'s
	// zero-value "" (nothing cached yet, and queryPage never fetches --
	// see its doc comment) until the ticker's first 50ms tick, briefly
	// routing on an unknown page. This is the SAME class of one-time,
	// bounded (by ipc.Client.Send's own deadline) blocking startup cost
	// Start() already pays for restore_highlight just above -- Start()
	// itself runs on the caller's goroutine, never the MIDI callback, so
	// blocking here is safe by construction, unlike a fetch triggered
	// from inside the callback itself.
	queryStatus()

	// Finder profile (docs/specs/2026-09-03-finder-tag-profile.md): ONE
	// serialized worker consuming a bounded queue, started before MIDI is
	// armed so no press can ever find it missing. Start()-scoped rather than
	// an Engine field: the channel's lifetime is exactly this run's, which
	// removes any restart race by construction. It exits on runCtx.
	finderJobs := make(chan finderTagJob, finderTagQueueCap)
	go e.finderTagWorker(runCtx, finderJobs, finderTagConfirmThreshold(cfg))

	log.Printf("[midi] opening Loupedeck input port...")
	stop, err := startMIDIFn(func(msg midi.Message, _ int32) {
		// Hot reload: read the CURRENT config once per MIDI event so a
		// SetConfig lands on the very next press.
		cfg := e.cfg.Load()
		highlightColor := cfg.TopLevel.HighlightColor
		var ch uint8
		var ccNum, val uint8
		var note, vel uint8

		// control_change: rotary knobs and sliders (relative CC encoding)
		if msg.GetControlChange(&ch, &ccNum, &val) {
			control := cfg.ControlForCC(int(ccNum))
			if control == "" {
				return
			}
			step := mididec.RelStep(int(val))
			log.Printf("[midi] cc=%d val=%d -> control=%q step=%d", ccNum, val, control, step)
			e.setLastMIDI(fmt.Sprintf("cc=%d val=%d control=%q step=%d", ccNum, val, control, step))
			if step != 0 {
				accum.Add(control, step)
			}
			return
		}

		// note release: EITHER a note_on with velocity==0 (the idiom this
		// engine originally assumed exclusively) OR a REAL MIDI Note Off
		// (status 0x8n) -- confirmed live (2026-08-12 diagnostic trace,
		// raw=80 31 40) that the Loupedeck sends genuine Note Off for at
		// least C1, not vel==0. msg.GetNoteOn returns FALSE outright for a
		// real Note Off (confirmed by reading the vendored
		// gitlab.com/gomidi/midi/v2@v2.3.23 message.go source: GetNoteOn
		// requires me.Is(NoteOnMsg)), so this entire release branch was
		// UNREACHABLE for any control using the real-Note-Off convention --
		// fineModeHold (C5) and modifierHold (Cmd) could be pressed but
		// never released. msg.GetNoteEnd is the library's own "either
		// convention" accessor (true for a NoteOn with vel==0 OR a real
		// NoteOff) -- checked FIRST, before msg.GetNoteOn below, so release
		// handling stays OUTSIDE (and ahead of) the press debounce: a
		// press-then-release pair must never be collapsed by a debounce
		// that only ever governs presses.
		if msg.GetNoteEnd(&ch, &note) {
			control := cfg.ControlForNote(int(note))
			e.setLastMIDI(fmt.Sprintf("note=%d release control=%q", note, control))

			// appdeck pairing: a press that appdeck handled gets its release
			// from appdeck too (modifierHold needs it), never the Resolve
			// release logic below.
			if control != "" {
				if v, held := e.hooks.appHeld.LoadAndDelete(control); held {
					hp := v.(heldPress)
					// The overlay this press raised is ALWAYS lowered, whatever the
					// binding has become since (hot reload) and even when appdeck
					// was removed in the meantime (wave E, F8). Idempotent.
					if hp.holdMod != "" {
						e.clearHeldOverlay(hp.holdMod)
						e.setLastAction(control + " released")
					}
					if ad := e.hooks.appdeck.Load(); ad != nil {
						res := ad.Handle(appdeck.Input{PMode: e.getPMode(), Bundle: hp.bundle, Mod: hp.mod, Layer: hp.layer, Control: control, Kind: appdeck.KindRelease})
						if res.Effect != nil && res.Effect.Kind == "modifierRelease" {
							e.clearHeldOverlay(res.Effect.Modifier)
							e.setLastAction(res.Text)
						}
						if !res.Handled {
							res = appdeck.Result{Handled: true, Source: "app", PresetID: hp.bundle}
						}
						e.emitAppdeck(control, "release", 0, res, atomic.LoadInt32(&fineModeAtomic) != 0, time.Now())
					}
					return
				}
				// The editor window is frontmost: its presses were swallowed, so
				// their releases are too (never the Resolve release logic).
				if e.hooks.appdeck.Load() != nil && isSelfFront(e.frontmostFor(pressFrontmostMaxAge)) {
					// A Resolve-path hold (fineModeHold / modifierHold) stores no
					// appHeld entry. If it was pressed while Resolve was frontmost and
					// the editor took focus before the release, nothing else would ever
					// lower it: release it here, like the Resolve release block below
					// (wave G, B3). Idempotent.
					if b, ok := cfg.Binding(getBank(), control); ok {
						switch {
						case b.IsFineModeHold():
							atomic.StoreInt32(&fineModeAtomic, 0)
							log.Printf("[midi] control=%q fineModeHold released while the editor has focus (fine mode OFF)", control)
						case b.IsModifierHold():
							e.clearHeldOverlay(b.Modifier)
							log.Printf("[midi] control=%q modifierHold=%q released while the editor has focus", control, b.Modifier)
						}
					}
					e.emitEditorFocused(control, "release", 0, atomic.LoadInt32(&fineModeAtomic) != 0, time.Now())
					return
				}
			}

			// A hold-to-fire key released before its threshold: nothing fires (wave H).
			if control != "" {
				if ms, was := e.holdRelease(control); was {
					if e.observing() {
						e.emitInput(InputEvent{Control: control, Kind: "release", Accel: 1,
							Out: InputOut{Text: fmt.Sprintf("hold to fire (%d ms)", ms)}, Fine: atomic.LoadInt32(&fineModeAtomic) != 0,
							PresetID: preset.ResolveID, Source: "app", Action: "key"})
					}
					return
				}
			}

			// Check for fineModeHold/modifierHold release. This block is
			// NOT a generic "any held-type release" dispatcher -- it tests
			// specific types by name and returns unconditionally either
			// way, so an unmatched type is silently swallowed. Kept as an
			// explicit switch (not a bare `if IsFineModeHold()`) so a
			// future held-type addition just needs its own case here, per
			// the REV 2 design review's correction on this exact block.
			if control != "" {
				if b, ok := cfg.Binding(getBank(), control); ok {
					switch {
					case b.IsFineModeHold():
						atomic.StoreInt32(&fineModeAtomic, 0)
						log.Printf("[midi] control=%q fineModeHold released (fine mode OFF)", control)
						e.setLastAction("Fine mode off")
						e.emitResolveRelease(control, "Fine mode off", b, atomic.LoadInt32(&fineModeAtomic) != 0)
					case b.IsModifierHold():
						// clearHeldOverlay is IDEMPOTENT (only clears
						// if it currently equals b.Modifier) -- the
						// note_on debounce does not cover releases (real
						// Note Off nor vel==0 note-on), so duplicate
						// releases for the same physical release all
						// reach this branch safely, regardless of which
						// MIDI convention produced them.
						e.clearHeldOverlay(b.Modifier)
						log.Printf("[midi] control=%q modifierHold=%q released", control, b.Modifier)
						e.setLastAction(control + " released")
						e.emitResolveRelease(control, control+" released", b, atomic.LoadInt32(&fineModeAtomic) != 0)
					}
				}
			}
			return
		}

		// note_on (press, vel>0). GetNoteEnd above already consumed every
		// release -- a real Note Off (GetNoteOn returns false for one, so
		// this branch is naturally unreachable for it) and a note-on with
		// vel==0 (GetNoteEnd treats that as a release too, and returns
		// before we get here) -- so vel is always >0 by construction for
		// every note_on that reaches this branch.
		if msg.GetNoteOn(&ch, &note, &vel) {
			control := cfg.ControlForNote(int(note))
			e.setLastMIDI(fmt.Sprintf("note=%d vel=%d control=%q", note, vel, control))

			// The panic key comes first, before the debounce, the frontmost lookup,
			// the app and Resolve cascades and every sidecar round trip: a control
			// assigned to "disarm" anywhere disarms the deck, whatever app is in
			// front and whatever else is assigned to that control (wave K).
			if ad := e.hooks.appdeck.Load(); ad != nil && control != "" && ad.IsPanicControl(control) {
				e.panicPress(control)
				return
			}

			// Debounce: suppress a duplicate note_on PRESS for the SAME
			// control arriving within keyDebounceMs of the last one this
			// Engine processed (BUG 1, 2026-08-12 live report) -- see
			// isDuplicateNoteOn's doc comment. Checked BEFORE every other
			// note_on intercept below (layer switch, pmode switch,
			// hardwired nav, D1/marker/direction-select direct checks, and
			// resolveNoteBank/dispatchNoteOn) so every control is protected
			// uniformly, regardless of binding type. Does not touch the
			// release branch above (GetNoteEnd, handled and returned
			// before this point is ever reached) -- a fast genuine
			// press-then-release pair is never treated as "the same event
			// twice".
			if control != "" {
				debounceWindow := time.Duration(cfg.TopLevel.KeyDebounceMs) * time.Millisecond
				if e.isDuplicateNoteOn(control, debounceWindow) {
					log.Printf("[note_on] control=%q SUPPRESSED duplicate (debounce window=%dms)", control, cfg.TopLevel.KeyDebounceMs)
					return
				}
			}

			// Mouse-calibration capture: while the editor waits for "press a deck
			// button", that press is the trigger and does nothing else. After the
			// panic key and the debounce, before anything can act on the press.
			if control != "" {
				if h := e.hooks.capture.Load(); h != nil && (*h)() {
					log.Printf("[note_on] control=%q used as the calibration capture trigger", control)
					return
				}
			}

			// appdeck (generic, non-Resolve apps; inert unless SetAppDeck was
			// called). Placed right after the debounce and BEFORE every
			// Resolve intercept (layer switch, hardwired nav, selectClip ...)
			// on purpose: with another app frontmost, none of those may reach
			// Resolve. Finder-tag P-buttons are the one exception and fall
			// through to the Finder branch below. Resolve frontmost (or an
			// unknown frontmost) changes nothing here; see the Global
			// fall-through at the end of this branch.
			resolveFront := true
			if ad := e.hooks.appdeck.Load(); ad != nil && control != "" {
				hmod := e.getHeldOverlay()
				fid := e.frontmostFor(pressFrontmostMaxAge)
				if isSelfFront(fid) {
					// The panic key is the one press that is NOT swallowed here:
					// disarming posts nothing into the editor and must work with
					// the editor frontmost (wave H).
					if ad.IsDisarmPress(fid, hmod, layerState.Get(), control) {
						res := ad.Handle(appdeck.Input{PMode: e.getPMode(), Bundle: fid, Mod: hmod, Layer: layerState.Get(), Control: control, Kind: appdeck.KindPress})
						e.finishAppdeckPress(res, control, fid, hmod, layerState.Get(), setLayer, atomic.LoadInt32(&fineModeAtomic) != 0)
						return
					}
					// toggleHud (Fn + C6) and the modifier hold that pairs with it post
					// nothing into the editor, so they work with the editor frontmost too
					// (Tunahan 2026-11-05: Fn+C6 "its not" while the editor had focus: the Fn
					// press was swallowed, so C6 never saw the Fn bank).
					if typ, modName := ad.PressType(fid, hmod, layerState.Get(), control); typ == "toggleHud" || typ == "layer" || (typ == "modifierHold" && modName != "") {
						res := ad.Handle(appdeck.Input{PMode: e.getPMode(), Bundle: fid, Mod: hmod, Layer: layerState.Get(), Control: control, Kind: appdeck.KindPress})
						e.finishAppdeckPress(res, control, fid, hmod, layerState.Get(), setLayer, atomic.LoadInt32(&fineModeAtomic) != 0)
						return
					}
					// The editor window has focus: a Global key or scroll would be
					// posted into its own web view. Swallow it (never a Resolve
					// verb either) but let the live test show it.
					// Hue / Sat / Lum / Custom Mode are deck state, not actions: they
					// still change (and reach the editor's LEDs) while the editor is
					// in front, which is exactly when someone is watching those LEDs.
					if nm := e.trackDeckState(control, pmodeState); nm != "" {
						saveSentinel(e.getLastFolder(), nm)
					}
					e.emitEditorFocused(control, "press", 0, atomic.LoadInt32(&fineModeAtomic) != 0, time.Now())
					return
				}
				resolveFront = isResolveFront(fid)
				if !resolveFront {
					// Deck state follows the keys in every app: Hue / Sat / Lum set
					// the slider mode and CustomMode flips its toggle, then the press
					// carries on to whatever the front app's preset assigns it (a
					// Lightroom preset sends its targeted-adjustment key as well).
					if nm := e.trackDeckState(control, pmodeState); nm != "" {
						saveSentinel(e.getLastFolder(), nm)
					}
					tagPress := false
					if hmod == "" && macapp.BundleIDIsFinder(fid) {
						_, tagPress = isFinderTagControl(cfg, control)
					}
					if !tagPress {
						res := ad.Handle(appdeck.Input{PMode: e.getPMode(), Bundle: fid, Mod: hmod, Layer: layerState.Get(), Control: control, Kind: appdeck.KindPress})
						e.finishAppdeckPress(res, control, fid, hmod, layerState.Get(), setLayer, atomic.LoadInt32(&fineModeAtomic) != 0)
						return
					}
				}
			}

			// Resolve-path observer: one deferred emit covers every return
			// below (nil-safe, non-blocking; see appdeck_hook.go).
			oc := &noteObs{e: e}
			if e.observing() {
				defer func() {
					if oc.fired {
						e.emitResolveNote("press", oc, cfg, queryPage(), layerState.Get(), getBank(), e.getHeldOverlay(), control, atomic.LoadInt32(&fineModeAtomic) != 0)
					}
				}()
			}

			// Custom Mode toggle (engine state; the lamp is firmware). The press
			// is not swallowed: it continues to whatever the preset assigns it.
			if control == "CustomMode" {
				e.toggleCustomMode()
			}

			// Layer switch. Checked FIRST, before every other intercept, and
			// resolved against layer-1 banks only so no layer can steal the
			// buttons that get you out of it. See resolveLayerSwitch.
			//
			// This replaces the vestigial `case "Col": setBank("col")` that
			// used to sit here: setBank's only caller, and a no-op since bank
			// was already always "col".
			if lb, ok := resolveLayerSwitch(cfg, queryPage(), control); ok {
				prev := layerState.Get()
				setLayer(lb.Layer)
				now := layerState.Get()
				log.Printf("[note_on] control=%q layer %d→%d", control, prev, now)
				oc.set(fmt.Sprintf("Layer %d", now))
				return
			}

			// P-mode switch (Hue/Sat/Lum). Same shape and rationale as the
			// layer switch above: pure internal state, resolved against the
			// layer-1 banks so no layer can steal the keys that change it.
			if pb, ok := resolvePModeSwitch(cfg, queryPage(), control); ok {
				pmodeState.Set(pb.PMode)
				saveSentinel(e.getLastFolder(), pb.PMode)
				e.pushLEDs()
				log.Printf("[note_on] control=%q pmode=%q", control, pb.PMode)
				oc.set("P-mode: " + pb.PMode)
				return
			}

			// Slider-mode LAYER for the P1-P8 keys: with no modifier held, an
			// assignment in {page}-pm-{mode} / col-pm-{mode} wins over everything
			// below (the direct intercepts included, since P1btn-P8btn are among
			// them). No assignment for the live mode = fall through unchanged.
			if e.getHeldOverlay() == "" {
				if mb, mbank, ok := cfg.ModeBinding(queryPage(), pmodeState.Get(), control); ok {
					log.Printf("[note_on] control=%q slider-mode=%q bank=%q", control, pmodeState.Get(), mbank)
					if mb.Type == "disarm" {
						oc.set(e.panicDisarm(control))
						return
					}
					if holdMsOf(mb) > 0 {
						oc.set(e.holdResolveKey(control, mb))
						return
					}
					oc.set(dispatchNoteOn(control, mb, modeTracker, cal, getClient(), int(nodeAtomic.Load()), e.getActiveTrack(), queryPage, aw, cfg,
						e.chooseFolder, e.getLastFolder, e.setLastFolder, e.toggleHUD))
					return
				}
			}

			// mod is the currently-held modifier overlay name (e.g.
			// "shift"), or "" when nothing is held -- fetched ONCE here and
			// threaded through every direct-intercept check below
			// (isSelectClipHorizControl/isSelectClipRelease/isAddMarkerControl/
			// isJogPressControl/isMarkerColorControl/isMarkerAtPlayheadControl/
			// isSelectClipVert/isSelectClipClear, plus the two raw
			// editBankBinding calls in the Left/Right cases just below), so a
			// single MIDI event sees one consistent overlay snapshot rather
			// than each intercept independently re-reading e.getHeldOverlay()
			// (which is itself mutex-guarded and cheap, but re-fetching per
			// intercept invites the SAME class of "one event, inconsistent
			// state across two reads" bug this file's other single-snapshot-
			// per-tick patterns (e.g. the drain loop's `s := queryStatus()`)
			// already exist to avoid). See editBankBinding's doc comment for
			// why the direct-intercept family needs this at all: it is
			// checked BEFORE the generic "Held-modifier CHORD OVERLAY" block
			// further down, so a chord over one of these controls is
			// otherwise invisible to it.
			mod := e.getHeldOverlay()

			// Hard-wired navigation controls.
			switch control {
			case "Left":
				// Page-aware (direction-button clip selection, Edit page
				// only): checked directly against the edit bank, same
				// isSelectClipHorizControl shape as every other direct
				// intercept in this callback -- Left/Right have NO col-bank
				// binding at all (this switch is the ENTIRE color-page nav
				// behavior for them), so on every OTHER page this falls
				// through to the pre-existing node-nav below, byte-identical.
				//
				// Now ALSO overlay-aware (2026-08-13): isSelectClipHorizControl
				// tries the held overlay's chord bank first (e.g. "edit-shift"
				// while Shift is held), same precedence as every other
				// direct intercept below. When NOT selectClipHoriz (no
				// overlay held, or the overlay/edit bank binds this control
				// to something else — e.g. a plain "key" binding for the
				// bare Left arrow), and we are actually on the Edit page,
				// resolve+dispatch whatever IS bound there via the SAME
				// generic dispatchNoteOn every other resolved binding in
				// this file goes through, rather than falling through to
				// node-nav — which stays the fallback ONLY when off the
				// Edit page, or when the edit bank genuinely has no binding
				// for Left at all.
				if isSelectClipHorizControl(cfg, queryPage(), layerState.Get(), "Left", mod) {
					oc.set(dispatchSelectClipHorizontal(getClient(), "Left", -1, highlightColor, e.getSelection, e.setSelection, e.setActiveTrack))
					return
				}
				if queryPage() == "edit" {
					if eb, ok := editBankBinding(cfg, "edit", layerState.Get(), "Left", mod); ok {
						oc.set(dispatchNoteOn("Left", eb, modeTracker, cal, getClient(), int(nodeAtomic.Load()), e.getActiveTrack(), queryPage, aw, cfg,
							e.chooseFolder, e.getLastFolder, e.setLastFolder, e.toggleHUD))
						return
					}
				}
				cur := int(nodeAtomic.Load())
				if cur > 1 {
					cur--
					nodeAtomic.Store(int32(cur))
					r.SetNode(cur)
				}
				if getClient() != nil {
					if _, err := getClient().Send(map[string]any{"op": "set_node", "node": cur}); err != nil {
						log.Println("send set_node:", err)
					}
				}
				return
			case "Right":
				// Page-aware, same rationale as "Left" above (now also
				// overlay-aware, with the same "key" fallback for the bare
				// Right arrow when no chord and no selectClipHoriz binding
				// resolves).
				if isSelectClipHorizControl(cfg, queryPage(), layerState.Get(), "Right", mod) {
					oc.set(dispatchSelectClipHorizontal(getClient(), "Right", 1, highlightColor, e.getSelection, e.setSelection, e.setActiveTrack))
					return
				}
				if queryPage() == "edit" {
					if eb, ok := editBankBinding(cfg, "edit", layerState.Get(), "Right", mod); ok {
						oc.set(dispatchNoteOn("Right", eb, modeTracker, cal, getClient(), int(nodeAtomic.Load()), e.getActiveTrack(), queryPage, aw, cfg,
							e.chooseFolder, e.getLastFolder, e.setLastFolder, e.toggleHUD))
						return
					}
				}
				cur := int(nodeAtomic.Load()) + 1
				nodeAtomic.Store(int32(cur))
				r.SetNode(cur)
				if getClient() != nil {
					if _, err := getClient().Send(map[string]any{"op": "set_node", "node": cur}); err != nil {
						log.Println("send set_node:", err)
					}
				}
				return
			}

			if control == "" {
				return
			}

			// --- Finder profile: colour-tag the Finder selection ---------
			//
			// FIRST intercept after the empty-control guard, and that
			// position is load-bearing. queryPage() (4780) is a lock-and-peek
			// of statusCache.page refreshed by the 50ms sidecar ticker; it
			// does NOT change when you Cmd-Tab away from Resolve, and a
			// sidecar timeout deliberately PRESERVES it. So queryPage()
			// still reads "edit" while Finder is frontmost, and any Finder
			// branch placed at or after the markerAtPlayhead intercept
			// (~5171 below) would be dead code -- the press would have
			// already been consumed and dropped a marker in Resolve.
			//
			// This corrects a bug rather than trading one behaviour for
			// another. 2T5 DECK+'s premise is that the deck acts on the
			// FRONTMOST app; markerAtPlayhead reaching into a BACKGROUND
			// Resolve was only ever possible because the sidecar path never
			// had a frontmost gate. Nothing is lost here -- the control
			// starts obeying the rule the rest of the app already follows.
			// Resolve-frontmost behaviour is unchanged.
			//
			// Carry forward (deliberately NOT fixed here): every other
			// sidecar-path control has the same missing gate -- clip
			// colours, selection, dispatchMarkerAtPlayhead's siblings. Under
			// the frontmost principle those are the same bug. Logged, not
			// widened.
			//
			// Gated on mod == "" (precedent: the Esc marker-exit intercept
			// below) so deck chords stay free for future Finder actions.
			//
			// ORDER MATTERS on the two conditions: isFinderTagControl is a
			// pure map lookup, frontmostBundleIDFn is a ~1-3ms CGWindowList
			// walk. Testing the control name first means the walk is paid
			// only by the 8 P-buttons, never by every knob and key on the
			// deck.
			//
			// Known false negative, accepted: FrontmostBundleID skips
			// windows with kCGWindowLayer != 0, so with Finder showing only
			// the Desktop and Resolve holding any on-screen window the gate
			// reads "Resolve" and tagging will not fire. The same property
			// means the user cannot fall into tagging mode by clicking the
			// empty Desktop.
			if mod == "" {
				if fb, isTag := isFinderTagControl(cfg, control); isTag {
					if macapp.BundleIDIsFinder(frontmostBundleIDFn()) {
						enqueueFinderTag(finderJobs, control, fb.Tag)
						return
					}
				}
			}

			// D1 knob-press ("release / unselect"): checked directly against
			// the edit bank (not resolveNoteBank/dispatchNoteOn) — selectClip
			// is EXCLUSIVELY an edit-bank binding type, so isSelectClipRelease
			// only ever fires on the Edit page. Handled here, before
			// noteBank/dispatchNoteOn, because release needs direct access to
			// setActiveTrack, which dispatchNoteOn's signature intentionally
			// only carries the READ-side activeTrack int (for
			// reset_property's "track" field) — same reasoning as the drain
			// loop's setTrack-wrapping-closure below, which also needs
			// setActiveTrack and is likewise handled outside dispatchNoteOn's
			// switch.
			if isSelectClipRelease(cfg, queryPage(), layerState.Get(), control, mod) {
				oc.set(dispatchSelectClipRelease(getClient(), e.setActiveTrack, cfg.TopLevel.ReleaseColor))
				return
			}

			// C1 add-marker (Edit page only): checked directly against the
			// edit bank, same shape/reasoning as the selectClip-release
			// check above — addMarker is neither IsProperty() nor IsJog(),
			// so resolveNoteBank's page-aware shadow would NOT pick it up,
			// and falling through to dispatchNoteOn would re-fire the col
			// bank's C1 binding (type "key", Alt+S) on the Edit page.
			// Intercepting here keeps the col-bank C1 (Alt+S
			// add-serial-node) completely unchanged on the Color page (and
			// every other page).
			if isAddMarkerControl(cfg, queryPage(), layerState.Get(), control, mod) {
				if eb, ok := editBankBinding(cfg, "edit", layerState.Get(), control, mod); ok {
					oc.set(dispatchAddMarker(getClient(), control, eb, e.getActiveTrack(), e.setActiveMarker, e.getActiveMarkerFrame, e.clearActiveMarker, e.setActiveMarkerAbsRange))
				}
				return
			}

			// ControlDial press (Edit page only): exits C1 marker mode if
			// active, else a no-op — see the marker-mode section's doc
			// comment above dispatchControlDialPress. Checked directly
			// against the edit bank rather than routed through
			// resolveNoteBank/dispatchNoteOn: resolveNoteBank only shadows
			// property/jog controls to the edit bank for DRAIN (turn)
			// events, never note_on (press) events, and dispatchNoteOn's
			// switch has no case for IsJog() at all — so without this
			// direct intercept a ControlDial press would keep silently
			// no-op'ing exactly as it always has (see
			// TestNoteOnPageAware_EditPage_JogKnobClick_NoSidecarOp).
			// Gated on marker mode being ACTIVE. C1 is now the toggle and
			// owns exit, so this is a redundant second way out -- harmless,
			// and it keeps dispatchControlDialPress and its tests intact.
			//
			// The gate is what actually FREES the ControlDial press: the
			// intercept used to `return` unconditionally, swallowing every
			// press whether or not marker mode was live, so the control could
			// never be bound to anything. Now, whenever marker mode is off
			// (nearly always) the press falls through to normal dispatch and
			// is available like any other button.
			if isJogPressControl(cfg, queryPage(), layerState.Get(), control, mod) && e.getActiveMarkerFrame() >= 0 {
				oc.set(dispatchControlDialPress(e.getActiveMarkerFrame, e.clearActiveMarker))
				return
			}

			// P1-8btn (Edit page only): sets the C1 marker-mode active
			// marker's color, only while marker mode is active — checked
			// directly against the edit bank for the same reason as above
			// ("markerColor" is neither IsProperty() nor IsJog(), and
			// P1-8btn have no col-bank binding at all to fall back to).
			if isMarkerColorControl(cfg, queryPage(), layerState.Get(), control, mod) {
				if eb, ok := editBankBinding(cfg, "edit", layerState.Get(), control, mod); ok {
					oc.set(dispatchSetMarkerColor(getClient(), control, eb, e.getActiveMarkerFrame, e.updateActiveMarkerColor))
				}
				return
			}

			// Feature 1 (2026-08-12): P1-8btn Hue-gated "recolour-or-add
			// marker at playhead" (Edit page only): checked directly
			// against the edit bank, same shape/reasoning as the
			// markerColor check just above -- "markerAtPlayhead" is
			// neither IsProperty() nor IsJog(), and P1-8btn have no
			// col-bank binding at all to fall back to. b.WhenPMode is
			// checked INSIDE dispatchMarkerAtPlayhead against the live
			// pmode read here.
			if isMarkerAtPlayheadControl(cfg, queryPage(), layerState.Get(), control, mod) {
				if eb, ok := editBankBinding(cfg, "edit", layerState.Get(), control, mod); ok {
					oc.set(dispatchMarkerAtPlayhead(getClient(), control, eb, e.getActiveTrack(), e.getPMode(), e.setActiveMarker, e.setActiveMarkerAbsRange, e.clearActiveMarker))
				}
				return
			}

			// Direction-button clip selection (Up/Down, Edit page only):
			// checked directly against the edit bank, same shape/reasoning
			// as the selectClip-release/addMarker/markerColor checks above
			// -- "selectClipVert" is neither IsProperty() nor IsJog(), and
			// Up/Down have no col-bank binding at all to fall back to.
			if isSelectClipVert(cfg, queryPage(), layerState.Get(), control, mod) {
				direction := 1
				if control == "Down" {
					direction = -1
				}
				oc.set(dispatchSelectClipVertical(getClient(), control, direction, highlightColor, e.getSelection, e.setSelection, e.setActiveTrack))
				return
			}

			// Direction-button clip selection "unselect all" (C5, Edit
			// page only): same direct-intercept shape as above.
			if isSelectClipClear(cfg, queryPage(), layerState.Get(), control, mod) {
				oc.set(dispatchSelectClipClear(getClient(), e.setActiveTrack, e.clearSelection))
				return
			}

			// Esc / BeforeAfter explicit marker-mode exit (Edit page only,
			// no modifier held -- 2026-08-25, the "marker-mode trap" fix).
			// A P-button same-colour press and a ControlDial press already
			// give two ways out of marker mode (see the section doc comment
			// above dispatchControlDialPress); the live incident that
			// prompted this fix showed both are easy to miss under pressure
			// ("control dial is not scrubbing" reported twice, one P-button
			// press silently converting the scrubber into a marker resizer
			// for HOURS) -- Esc is the deck's own universal "get me out of
			// this" gesture, so it becomes a third, deliberately chosen by
			// the user as the clearest one.
			//
			// Checked directly against the raw control name (not routed
			// through resolveNoteBank/dispatchNoteOn or a
			// isXControl-style predicate) because it must run BEFORE the
			// generic key dispatch further down and does NOT return --
			// BeforeAfter's own ordinary "key" binding (Escape on the Edit
			// page, see bindings.json) still fires immediately afterward,
			// completely unchanged; this only ADDS the marker-mode-exit
			// side effect, it never replaces the keystroke. `mod == ""`
			// scopes this to the PLAIN Esc press only -- a held-modifier
			// chord on this same physical button (e.g. edit-shift.
			// BeforeAfter = Cmd+Alt+Shift+R "Render in place") is a
			// deliberately different action and must not be conflated with
			// "exit marker mode" just because it happens to share a
			// control name.
			//
			// A silent no-op whenever marker mode is inactive (nearly
			// always) -- same "gate on the active flag, harmless otherwise"
			// posture as dispatchControlDialPress's identical guard above.
			if control == "BeforeAfter" && mod == "" && queryPage() == "edit" && e.getActiveMarkerFrame() >= 0 {
				e.clearActiveMarker()
				log.Printf("[note_on] control=%q method=markerMode-exit (Esc)", control)
				oc.set("Marker mode exit (Esc)")
			}

			// Held-modifier CHORD OVERLAY (Part B, 2026-08-12 REV 2 design
			// review): checked BEFORE the normal layer/page cascade
			// whenever a modifier is currently held -- see
			// resolveOverlayBinding's doc comment for the bank order
			// ({page}-{mod} -> col-{mod}) and why this is a wholly separate
			// resolution pass, never another rung of Cascade's layer walk.
			// A control the overlay does NOT redefine falls straight
			// through to the normal cascade below, so a held modifier can
			// never strand every OTHER button on the deck -- the same
			// "unfinished layer never leaves a control dead" contract the
			// layer cascade itself guarantees.
			if mod := e.getHeldOverlay(); mod != "" {
				if b, bank, ok := resolveOverlayBinding(cfg, queryPage(), mod, control); ok {
					log.Printf("[note_on] control=%q overlay=%q bank=%q", control, mod, bank)
					if b.Type == "disarm" { // panic: the Resolve preset's own disarm key (wave H)
						oc.set(e.panicDisarm(control))
						return
					}
					if holdMsOf(b) > 0 { // hold-to-fire key (wave H); never for a binding without holdMs
						oc.set(e.holdResolveKey(control, b))
						return
					}
					oc.set(dispatchNoteOn(control, b, modeTracker, cal, getClient(), int(nodeAtomic.Load()), e.getActiveTrack(), queryPage, aw, cfg,
						e.chooseFolder, e.getLastFolder, e.setLastFolder, e.toggleHUD))
					return
				}
			}

			// Page-aware bank resolution for note_on (knob press / button) —
			// see resolveNoteBank's doc comment for the full policy and why
			// it is NOT a blind "page==edit => bank=edit" shadow like the
			// drain loop's currentBank: that would also swallow every
			// col-only button (Copy/Paste/Undo/Redo/C5/C6/...) whenever the
			// Edit page is showing. queryPage() is the same cached status
			// source the wheel-click gate already uses.
			noteBank := resolveNoteBank(cfg, queryPage(), layerState.Get(), getBank(), control)
			b, ok := cfg.Binding(noteBank, control)
			if !ok {
				// Resolve has no binding for this control: Global may.
				if ad := e.hooks.appdeck.Load(); ad != nil && resolveFront {
					res := ad.Handle(appdeck.Input{PMode: e.getPMode(), Bundle: preset.ResolveID, Mod: e.getHeldOverlay(), Layer: layerState.Get(), Control: control, Kind: appdeck.KindPress})
					e.finishAppdeckPress(res, control, preset.ResolveID, e.getHeldOverlay(), layerState.Get(), setLayer, atomic.LoadInt32(&fineModeAtomic) != 0)
				}
				return
			}

			// fineModeHold press: activate fine mode.
			if b.IsFineModeHold() {
				atomic.StoreInt32(&fineModeAtomic, 1)
				log.Printf("[midi] control=%q fineModeHold pressed (fine mode ON)", control)
				oc.set("Fine mode on")
				return
			}

			// modifierHold press (Part B, e.g. the Cmd deck button): sets
			// the held-overlay name (a PURE internal-state mutation, no
			// OS-level side effect) -- release (GetNoteEnd, above) clears
			// it. Resolved through the SAME noteBank as fineModeHold above
			// so it applies on every page identically (a modifierHold
			// button is an ordinary col-bank control, not itself a chord
			// target).
			if b.IsModifierHold() {
				e.setHeldOverlay(b.Modifier)
				log.Printf("[midi] control=%q modifierHold=%q pressed (HELD)", control, b.Modifier)
				oc.set(control + " held")
				return
			}

			if b.Type == "disarm" { // panic: the Resolve preset's own disarm key (wave H)
				oc.set(e.panicDisarm(control))
				return
			}
			if holdMsOf(b) > 0 { // hold-to-fire key (wave H); never for a binding without holdMs
				oc.set(e.holdResolveKey(control, b))
				return
			}
			oc.set(dispatchNoteOn(control, b, modeTracker, cal, getClient(), int(nodeAtomic.Load()), e.getActiveTrack(), queryPage, aw, cfg,
				e.chooseFolder, e.getLastFolder, e.setLastFolder, e.toggleHUD))
		}
	})
	if err != nil {
		log.Printf("[midi] FAILED to open Loupedeck input port: %v", err)
		cancel()
		// Unwind the lazy sidecar supervisor and whatever it connected.
		e.supervisorWG.Wait()
		if c := e.clientPtr.Swap(nil); c != nil {
			c.Close()
		}
		e.mu.Lock()
		stopSC := e.sidecarStop
		e.sidecarStop = nil
		e.status.SidecarConnected = false
		e.status.SidecarError = ""
		e.status.SidecarState = ""
		e.mu.Unlock()
		if stopSC != nil {
			stopSC()
		}
		// Roll back the claim made at the top of Start() -- this call
		// never actually started anything, so a subsequent genuine retry
		// (e.g. the user pressing Start again after fixing whatever made
		// the MIDI port unavailable) must not be permanently blocked by a
		// stuck Running==true.
		e.mu.Lock()
		e.status.Running = false
		e.mu.Unlock()
		return err
	}
	log.Printf("[midi] Loupedeck input port OPEN — listening")

	e.cancel = cancel
	e.done = make(chan struct{})

	// e.status.Running was already claimed at the top of Start() (in the
	// same critical section as the ErrAlreadyRunning check, see that doc
	// comment) — no separate assignment needed here anymore.
	// SidecarConnected/SidecarError were already set above from the
	// actual launch+dial result. The engine (MIDI loop) starts regardless
	// of sidecar connectivity.

	// coalesceWindow is the accumulation window for balance events.
	// Balance events per-channel are summed within the window and flushed
	// as one scrub per channel at window close.
	// (per-event/per-tick read for hot reload, see below)

	// selectionGraceWindow is the direction-button selection auto-reset's
	// race-suppression grace window -- see clipSelection's doc comment and
	// the drain loop's auto-reset block below (BUG 3, 2026-08-12).
	// (per-event/per-tick read for hot reload, see below)

	// balanceAccum holds the accumulated balance steps per control within the
	// current coalesce window. Keyed by control name; values are summed steps.
	// Touched exclusively in the drain goroutine — no mutex required.
	balanceAccum := map[string]int{}
	var windowStart time.Time

	// jogStreak tracks how long the ControlDial has been dialled
	// continuously (see internal/tc.JogStreak) — escalates the jog snap
	// tier to a sustained tier (60s/"1-minute" by default) once the dial
	// has been turning nonstop for >= the binding's JogSustainMs,
	// overriding whatever speed tier tc.JogTier would otherwise pick from
	// a single tick's |step| magnitude. Lives in the drain-loop closure
	// (persists across ticks for the life of this Start() call), touched
	// exclusively from the drain/tick goroutine — no mutex required, same
	// posture as balanceAccum/windowStart above. Zero value is
	// immediately usable (see JogStreak's doc comment).
	var jogStreak tc.JogStreak

	// selectClipAccum accumulates raw per-tick D1 CC steps and only fires
	// a single clip-index move once the configured detentThreshold is
	// crossed (see selectClipDetent's doc comment above). Lives in the
	// drain-loop closure, touched exclusively from the drain/tick
	// goroutine — no mutex required, same posture as jogStreak/
	// balanceAccum. Zero value is immediately usable.
	var selectClipAccum selectClipDetent

	// jogSecondsAccum accumulates raw per-tick D1 CC steps for the
	// "jogSeconds" coarse playhead-jump binding and only fires a single
	// ±secondsPerStep jump once the configured detentThreshold is crossed
	// -- reuses selectClipDetent's exact accumulator type/behavior (see
	// its doc comment above), but is its OWN separate instance: D1 is
	// bound to exactly one of "selectClip" or "jogSeconds" at a time (see
	// Binding.Type), never both, so only one of these two accumulators is
	// ever actually driven in production -- kept as two distinct vars
	// (rather than one shared accumulator) so each binding type's state
	// stays independently testable and neither's reset semantics leak
	// into the other's. Lives in the drain-loop closure, touched
	// exclusively from the drain/tick goroutine -- no mutex required,
	// same posture as jogStreak/selectClipAccum. Zero value is
	// immediately usable. Unlike selectClipAccum, this has NO
	// activeTrack-transition reset wiring below -- jogSeconds has no
	// per-clip target concept to auto-reset.
	var jogSecondsAccum selectClipDetent

	// jogMinuteSnapAccum accumulates raw per-tick D1 CC steps for the
	// "jogMinuteSnap" whole-minute-snap binding -- the CURRENT D1
	// binding, superseding jogSeconds -- and only fires a single snap
	// once the configured detentThreshold is crossed -- reuses
	// selectClipDetent's exact accumulator type/behavior (see its doc
	// comment above), its OWN separate instance from
	// selectClipAccum/jogSecondsAccum: D1 is bound to exactly one of
	// "selectClip"/"jogSeconds"/"jogMinuteSnap" at a time (see
	// Binding.Type), never more than one, so at most one of these three
	// accumulators is ever actually driven in production -- kept as
	// separate vars (rather than one shared accumulator) so each
	// binding type's state stays independently testable and none of
	// their reset semantics leak into each other. Lives in the
	// drain-loop closure, touched exclusively from the drain/tick
	// goroutine -- no mutex required, same posture as the others. Zero
	// value is immediately usable. Like jogSecondsAccum, this has NO
	// activeTrack-transition reset wiring below -- jogMinuteSnap has no
	// per-clip target concept to auto-reset either.
	var jogMinuteSnapAccum selectClipDetent

	// keyDeltaAccum holds one keyDeltaDetent PER CONTROL NAME for
	// "keyDelta" bindings (2026-08-13, e.g. D1 while Shift is held -- see
	// resolveTurnBinding's overlay rung) -- a MAP, not a single instance
	// like selectClipAccum/jogSecondsAccum/jogMinuteSnapAccum above,
	// because unlike those three (each pinned to exactly one always-D1
	// binding at a time), a keyDelta binding could in principle be
	// authored on more than one control, or the SAME physical control
	// resolving to a keyDelta binding on one page/overlay and something
	// else on another -- each control name needs its own independent
	// running accumulation, never sharing one. Lives in the drain-loop
	// closure, touched exclusively from the drain/tick goroutine -- no
	// mutex required, same posture as the others. A nil map entry needs an
	// explicit allocate-on-first-use below (unlike the value-type
	// selectClipDetent vars above, since map values here are pointers so
	// each control's accumulation persists across ticks without a
	// re-insert).
	keyDeltaAccum := map[string]*keyDeltaDetent{}

	// scrollDeltaAccum is keyDeltaAccum's sibling for "scrollDelta"
	// bindings (2026-08-13, e.g. D1 while Shift is held, replacing
	// keyDelta there -- see that type's section doc comment above) --
	// reuses the SAME keyDeltaDetent struct (identical sign/count/
	// multi-fire-per-tick semantics, not a new accumulator type), but as
	// its OWN separate map instance, same per-control-name
	// map-of-instances rationale as keyDeltaAccum: a scrollDelta binding
	// could in principle be authored on more than one control, or the
	// same physical control could resolve to scrollDelta on one
	// page/overlay and something else (including keyDelta) on another.
	scrollDeltaAccum := map[string]*keyDeltaDetent{}

	// lastClipID/haveLastClipID track the D1 auto-reset's "has the
	// playhead's top clip changed since the last successful status tick"
	// comparison — drain-goroutine-only state, no mutex required, same
	// posture as jogStreak/selectClipAccum above. haveLastClipID gates the
	// FIRST successful tick (nothing to compare against yet, so it only
	// seeds lastClipID rather than firing a reset).
	var lastClipID string
	var haveLastClipID bool

	// prevActiveTrackForAccum is EDGE-TRIGGER state for
	// selectClipAccum.reset() below: it must fire exactly when activeTrack
	// TRANSITIONS from set to unset (a D1 press-release, or the auto-reset
	// above, on THIS tick) — never on every tick activeTrack simply
	// HAPPENS to already be 0, which is also true for every tick BEFORE
	// D1's very first selection is made (a naive "reset whenever
	// activeTrack==0" check would wipe the accumulator's cross-tick
	// buildup on every single tick during that initial approach, since
	// activeTrack is legitimately still 0 the whole time it's building
	// toward the FIRST fire).
	var prevActiveTrackForAccum int

	go func() {
		defer close(e.done)
		defer func() { saveSentinel(e.getLastFolder(), e.getPMode()) }()
		defer stop()

		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()

		for {
			select {
			case <-runCtx.Done():
				// Drain any pending tick value (non-blocking) then exit —
				// mirrors the old infinite `for range tick.C` shutdown-free
				// loop, just made cancellable.
				select {
				case <-tick.C:
				default:
				}
				return

			case <-tick.C:
				now := time.Now()

				// Hot reload: fresh config per drain tick (shadows the
				// startup values declared above).
				cfg := e.cfg.Load()
				highlightEnabled := cfg.TopLevel.HighlightEnabled == nil || *cfg.TopLevel.HighlightEnabled
				highlightColor := cfg.TopLevel.HighlightColor
				markerResizeFramesPerStep := cfg.TopLevel.MarkerResizeFramesPerStep
				coalesceWindow := time.Duration(cfg.TopLevel.CoalesceWindowMs) * time.Millisecond
				selectionGraceWindow := time.Duration(cfg.TopLevel.SelectionGraceMs) * time.Millisecond

				// --- Status snapshot (additive instrumentation; does not
				// alter grading logic, timing, or the sidecar protocol —
				// these are the same cached queryStatus()/FrontmostBundleID
				// calls already used elsewhere in this loop). s is hoisted
				// out of this block (rather than re-fetched below) so the
				// page-aware bank resolution below reuses the SAME cached
				// status snapshot already fetched this tick — no extra
				// sidecar round-trip.
				s := queryStatus()

				// --- Layer + reset-target auto-reset: page changed ---
				// Predictability over convenience: the user can never be
				// surprised by a stale layer, nor by Col being armed with a
				// target selected on a different page. Gated on s.ok for the
				// same reason as the D1 auto-reset immediately below -- a
				// transient sidecar None must not masquerade as a page change.
				//
				// Target first, so it sees the OLD prevPage before the layer
				// helper advances it.
				maybeResetTargetOnPageChange(aw, prevPageForLayer, s.ok, s.page)
				prevPageForLayer = maybeResetLayerOnPageChange(layerState, prevPageForLayer, s.ok, s.page)

				// --- Marker mode auto-exit: playhead left the marker ---
				// A fallback; C1's unconditional toggle owns deliberate exit.
				// Compares ABSOLUTE frames on both sides -- see
				// activeMarkerAbsStart's doc comment for why a relative
				// GetMarkers key must never be compared to this playhead.
				maybeAutoExitMarker(e, s.ok, s.havePlayheadFrame, s.playheadFrame)

				// --- D1 auto-reset: playhead moved to a different clip ---
				// Compares this tick's clip_id (GetCurrentVideoItem's
				// identity, the TOP clip at the playhead) against the
				// PREVIOUS successful tick's value; a change clears the D1
				// selectClip override so the Edit-page property/reset
				// dials fall back to GetCurrentVideoItem() (see
				// resolve_driver.py's _target_item) instead of staying
				// pinned to a track that may no longer be relevant. Placed
				// BEFORE queryActiveClip below so a reset this tick is
				// reflected in Status.ActiveClip THIS same tick, not one
				// tick late. Gated on s.ok (see cachedStatus.ok's doc
				// comment) -- a status-fetch failure this tick must never
				// be misread as "the clip changed to none" and spuriously
				// clear a live D1 selection. Does NOT fire on the very
				// first successful tick (haveLastClipID false) -- nothing
				// to compare against yet. A playhead nudge WITHIN the same
				// clip (clipID unchanged) never resets.
				//
				// Guarded with `!sel.active` (added alongside the NEW
				// direction-button selection below): this D1-era check's
				// own assumption -- "activeTrack pinned implies the TOP-
				// occupied clip is what's pinned, so a top-clip-identity
				// change means the selection is stale" -- no longer holds
				// once a selection can be pinned to a NON-top track via
				// Up/Down/Left/Right (e.g. the top-occupied track's clip_id
				// can change while the SEPARATELY-selected lower track's
				// clip still plainly contains the playhead -- "movement
				// WITHIN the [start,end) clip" per that feature's own
				// auto-reset contract just below, which must NOT reset in
				// that case). D1's own tests never populate `selection`
				// (sel.active stays false throughout), so this guard is a
				// pure no-op for them -- behavior byte-identical to before.
				if s.ok {
					sel := e.getSelection()
					if haveLastClipID && s.clipID != lastClipID && e.getActiveTrack() != 0 && !sel.active {
						log.Printf("[drain] control=D1 method=selectClip-autoreset clip_id changed %q -> %q, clearing activeTrack", lastClipID, s.clipID)
						e.setActiveTrack(0)
					}
					lastClipID = s.clipID
					haveLastClipID = true
				}

				// --- Direction-button selection auto-reset: the playhead
				// moved OUTSIDE the selected clip's [start, end) frame
				// range --- Gated on s.havePlayheadFrame (mirrors s.ok's
				// "only act on a genuinely successful read this tick"
				// rationale above) -- a status tick that doesn't carry
				// playhead_frame (a transient sidecar hiccup, or any Go
				// test's fake sidecar reply that doesn't include the
				// field) must never spuriously clear a live selection. A
				// playhead nudge STRICTLY WITHIN [start, end) never
				// resets -- only leaving the range does. Restores the
				// clip's original color via the sidecar's clear_highlight
				// op, same as C5's dispatchSelectClipClear.
				//
				// *** FIXED 2026-08-12 (BUG 3, live report) ***: race-proofed
				// against a horizontal select's own SetCurrentTimecode write
				// not having landed yet -- see clipSelection.confirmed's doc
				// comment. An in-range read confirms arrival (and is the
				// ONLY thing that can, from here on, once confirmed=true a
				// selection is treated exactly like the pre-fix immediate
				// auto-reset). An out-of-range read before confirmation is
				// SUPPRESSED (not cleared) until either arrival is confirmed
				// on a later tick, or selectionGraceMs has elapsed since the
				// selection was made -- a genuinely stuck/failed write still
				// clears eventually rather than pinning the selection
				// forever.
				if sel := e.getSelection(); sel.active && s.havePlayheadFrame {
					inRange := s.playheadFrame >= sel.start && s.playheadFrame < sel.end
					switch {
					case inRange:
						if !sel.confirmed {
							sel.confirmed = true
							e.setSelection(sel)
							log.Printf("[drain] control=selection method=autoreset confirmed arrival playhead=%d inside [%d,%d) track=%d", s.playheadFrame, sel.start, sel.end, sel.track)
						}
					case sel.confirmed:
						log.Printf("[drain] control=selection method=autoreset playhead=%d outside [%d,%d) track=%d, clearing selection", s.playheadFrame, sel.start, sel.end, sel.track)
						e.setActiveTrack(0)
						e.clearSelection()
						if getClient() != nil {
							if _, err := getClient().Send(map[string]any{"op": "clear_highlight"}); err != nil {
								log.Printf("[drain] control=selection method=autoreset clear_highlight send error: %v", err)
							}
						}
					case time.Since(sel.setAt) >= selectionGraceWindow:
						log.Printf("[drain] control=selection method=autoreset playhead=%d outside [%d,%d) track=%d, grace window elapsed unconfirmed, clearing selection", s.playheadFrame, sel.start, sel.end, sel.track)
						e.setActiveTrack(0)
						e.clearSelection()
						if getClient() != nil {
							if _, err := getClient().Send(map[string]any{"op": "clear_highlight"}); err != nil {
								log.Printf("[drain] control=selection method=autoreset clear_highlight send error: %v", err)
							}
						}
					default:
						log.Printf("[drain] control=selection method=autoreset SUPPRESSED (grace) playhead=%d outside [%d,%d) track=%d, not yet confirmed, %v since select", s.playheadFrame, sel.start, sel.end, sel.track, time.Since(sel.setAt))
					}
				}
				// The D1 sensitivity accumulator must never carry a stale
				// sub-threshold accumulation across a cleared selection
				// (D1 press-release, or the auto-reset just above) into
				// the NEXT selection gesture -- reset it EXACTLY on the
				// set-to-unset TRANSITION (edge-triggered, see
				// prevActiveTrackForAccum's doc comment), never merely
				// because activeTrack happens to already be 0 (which is
				// also true, legitimately, for every tick before D1's
				// very first selection).
				curActiveTrack := e.getActiveTrack()
				if prevActiveTrackForAccum != 0 && curActiveTrack == 0 {
					selectClipAccum.reset()
				}
				prevActiveTrackForAccum = curActiveTrack

				activeClip := queryActiveClip(s.page, e.getActiveTrack())
				{
					frontmostID := macapp.FrontmostBundleID()
					frontmost := macapp.BundleIDIsResolve(frontmostID)

					allowed := gate.Allow(frontmost, s.page)
					e.mu.Lock()
					e.status.ResolveFrontmost = frontmost
					e.status.Page = s.page
					e.status.GateAllow = allowed
					e.status.ActiveWheel = aw.Get()
					e.status.Mode = string(modeTracker.Current())
					e.status.Project = macapp.ResolveWindowTitle()
					e.status.ActiveClip = activeClip
					e.status.MarkerMode = e.markerModeStatus()
					// HUD v2 fields: Timecode/TimelineName from this tick's
					// cached sidecar status response; Bank/FineMode/Node
					// from local Go state already guarded elsewhere in this
					// file (bankMu/fineModeAtomic/nodeAtomic) -- the ticker
					// goroutine is just one more reader of each, exactly
					// like ActiveWheel/Mode above. SidecarStale mirrors
					// s.stale (see cachedStatus.stale's doc comment) -- on
					// a busy-but-alive tick s.timecode/etc. below are
					// themselves the carried-forward last-known values (not
					// blanked), so this copy needs no other change.
					// predictedTimecode extrapolates during a stale tick
					// (see its doc comment for the full design -- the
					// 2026-08-25 "live HUD timecode during playback" fix);
					// a fresh tick passes straight through to s.timecode
					// unchanged, byte-identical to before this feature.
					e.status.Timecode = predictedTimecode(s, now)
					e.status.TimelineName = s.timelineName
					e.status.SidecarStale = s.stale
					e.status.Bank = getBank()
					e.status.Layer = layerState.Get()
					e.noteLayer(e.status.Layer) // a layer reset (page change) reaches the editor within a tick
					e.status.HeldOverlay = e.getHeldOverlay()
					e.status.Target = aw.Get()
					e.status.PMode = pmodeState.Get()
					e.status.CustomMode = e.customMode.Load()
					e.status.TimelineDuration = s.timelineDurTC
					e.status.ClipIn = s.clipInTC
					e.status.ClipOut = s.clipOutTC
					e.status.ClipDuration = s.clipDurTC
					e.status.ClipHead = s.clipHeadTC
					e.status.ClipTail = s.clipTailTC
					e.status.ClipTotal = s.tlClipTotal
					e.status.ClipRendered = s.tlClipRender
					e.status.MarkerCount = s.tlMarkers
					e.status.LEDs = s.tlLEDs
					e.status.FineMode = atomic.LoadInt32(&fineModeAtomic) != 0
					e.status.Node = int(nodeAtomic.Load())
					// HUD v2 Edit-page target-clip readout, from this
					// tick's cached sidecar status response -- same
					// "just copy whatever this tick's queryStatus()
					// carried" posture as Timecode/TimelineName above.
					e.status.EditClipName = s.editClipName
					e.status.HaveEditClip = s.haveEditClip
					e.status.EditClipPan = s.editClipPan
					e.status.EditClipTilt = s.editClipTilt
					e.status.EditClipZoom = s.editClipZoom
					e.mu.Unlock()
				}

				// --- Drain MIDI accumulator ---
				drained := accum.Drain()

				// Separate balance events from non-balance events. currentBank
				// is a single race-safe snapshot for this tick (bank can be
				// concurrently written by the MIDI-callback goroutine's "Col"
				// nav press) — page-aware: the Edit page's "edit" bank wins
				// over whatever getBank() holds (which is always "col" today
				// — buttons/note_on dispatch stays on "col" unconditionally,
				// they need no page logic). Any other page (including
				// "color") keeps the pre-Phase-2 behavior byte-identical.
				// Layer + held-overlay snapshot for this tick. Each of the
				// three use sites below resolves independently via
				// resolveTurnBinding rather than sharing one precomputed
				// bank name — a single shared bank cannot express a
				// per-control cascade, and sharing one is exactly how the
				// jog-streak pre-scan would come to lag the main dispatch
				// by a tick. tickMod (2026-08-13, turns become
				// overlay-aware) is hoisted here for the SAME reason
				// tickLayer is: sites 1 and 2 below both process THIS
				// tick's drained events and must see the IDENTICAL held-
				// overlay value, or a fast turn spanning both resolutions
				// could straddle banks mid-gesture (the jog-streak
				// pre-scan resolving against one overlay state, the main
				// dispatch immediately after against a different one, if
				// the MIDI callback goroutine changed heldOverlayName in
				// between two separate e.getHeldOverlay() calls). Site 3
				// (the balance flush, below) is a SEPARATE batch spanning
				// multiple ticks and deliberately re-fetches its own fresh
				// mod at flush time, exactly like it already re-fetches a
				// fresh layerState.Get() instead of reusing tickLayer.
				if e.hooks.appdeck.Load() != nil && len(drained) > 0 {
					// sample the frontmost app BEFORE reading the layer, so the
					// first turn after an app change sees that app's layer (F9)
					e.frontmostFor(turnFrontmostMaxAge)
				}
				tickLayer := layerState.Get()
				tickMod := e.getHeldOverlay()
				tickPMode := pmodeState.Get() // one read per tick, like tickLayer / tickMod

				// Sustained-spin jog streak: detect whether THIS tick's
				// drained events include a nonzero step for a "jog"-typed
				// control (today that's exclusively ControlDial —
				// bindings.json's only "jog" binding — but this scans by
				// TYPE, not a hardcoded control name, so it generalizes to
				// any future jog control). jogStreak.Update is called
				// EVERY tick, active or not, so a tick with no jog step
				// (dial paused, even if OTHER controls are active this
				// same tick) resets the streak — see internal/tc.JogStreak.
				jogActiveThisTick := false
				var jogBindingForStreak config.Binding
				for control, step := range drained {
					if step == 0 {
						continue
					}
					if b, _, ok := resolveTurnBindingMode(cfg, s.page, tickLayer, control, tickMod, tickPMode); ok && b.IsJog() {
						jogActiveThisTick = true
						jogBindingForStreak = b
						break
					}
				}
				streakDuration := jogStreak.Update(now, jogActiveThisTick)
				sustainSnap := 0
				if jogActiveThisTick {
					sustainSnap = tc.SustainSnapSeconds(streakDuration, jogBindingForStreak.JogSustainMs, jogBindingForStreak.JogSustainSeconds)
				}

				for control, step := range drained {
					// appdeck (inert unless SetAppDeck was called): another
					// app frontmost -> appdeck decides, Resolve verbs are
					// never reached. The frontmost lookup is cached per tick.
					ad := e.hooks.appdeck.Load()
					if ad != nil && step != 0 {
						if fid := e.frontmostFor(turnFrontmostMaxAge); isSelfFront(fid) {
							e.emitEditorFocused(control, "turn", step, atomic.LoadInt32(&fineModeAtomic) != 0, now)
							continue
						} else if !isResolveFront(fid) {
							res := ad.Handle(appdeck.Input{PMode: tickPMode, Bundle: fid, Mod: tickMod, Layer: tickLayer, Control: control, Kind: appdeck.KindTurn, Step: step})
							if res.Fired && res.Text != "" {
								e.setLastAction(res.Text)
							}
							e.emitAppdeck(control, "turn", step, res, atomic.LoadInt32(&fineModeAtomic) != 0, now)
							continue
						}
					}
					b, currentBank, ok := resolveTurnBindingMode(cfg, s.page, tickLayer, control, tickMod, tickPMode)
					if !ok {
						// Resolve has no binding: Global may (appdeck, Resolve frontmost).
						if ad != nil && step != 0 {
							res := ad.Handle(appdeck.Input{PMode: tickPMode, Bundle: preset.ResolveID, Mod: tickMod, Layer: tickLayer, Control: control, Kind: appdeck.KindTurn, Step: step})
							if res.Fired && res.Text != "" {
								e.setLastAction(res.Text)
							}
							e.emitAppdeck(control, "turn", step, res, atomic.LoadInt32(&fineModeAtomic) != 0, now)
						}
						continue
					}
					turnText := ""
					switch {
					case b.IsBalance():
						balanceAccum[control] += step
					case b.IsSelectClip():
						// D1: cycle which occupied video track's clip the
						// property/reset dials target. Script-API only
						// (clips_at_playhead) — no CGEvent, no gate. When
						// the highlight feature is enabled, also tint the
						// newly-selected clip's color in Resolve so the
						// user can SEE which stacked layer is active (see
						// dispatchHighlightClip) — wrapping setActiveTrack
						// rather than adding a second call site keeps this
						// firing exactly when (and only when) D1 actually
						// selects a track, including the same-track-again
						// clamp case at the top/bottom of the list.
						//
						// Sensitivity: this tick's raw step is first run
						// through selectClipAccum (see selectClipDetent's
						// doc comment) — dispatchSelectClip is only ever
						// called with the FIRED direction (-1/0/+1,
						// PER-DETENT), never the raw per-tick step, so a
						// slow/deliberate turn that hasn't yet crossed the
						// binding's detentThreshold (b.DetentThreshold,
						// falling back to defaultSelectClipDetentThreshold)
						// produces no sidecar call and no track movement
						// at all this tick.
						threshold := b.DetentThreshold
						if threshold <= 0 {
							threshold = defaultSelectClipDetentThreshold
						}
						fireDir := selectClipAccum.add(step, threshold)
						if fireDir == 0 {
							break
						}
						setTrack := e.setActiveTrack
						if highlightEnabled {
							setTrack = func(track int) {
								e.setActiveTrack(track)
								dispatchHighlightClip(getClient(), track, highlightColor)
							}
						}
						turnSet(&turnText, e.setLastAction, dispatchSelectClip(fireDir, getClient(), e.getActiveTrack, setTrack))
					case b.IsJogSeconds():
						// D1 (jogSeconds binding): coarse playhead jump, the
						// coarse counterpart to the ControlDial's fine "jog"
						// binding. Reuses the EXACT SAME detent-accumulator
						// pattern as the selectClip case above (jogSecondsAccum
						// is its own selectClipDetent instance -- see that var's
						// doc comment) -- raw per-tick CC steps accumulate and
						// only fire ONE jump once the accumulated magnitude
						// crosses threshold, debouncing the physical encoder's
						// oversensitive raw-step rate exactly like selectClip
						// does. No activeTrack/selection bookkeeping at all --
						// unlike selectClip, jogSeconds has no per-clip target
						// concept to auto-reset.
						threshold := b.DetentThreshold
						if threshold <= 0 {
							threshold = defaultSelectClipDetentThreshold
						}
						fireDir := jogSecondsAccum.add(step, threshold)
						if fireDir == 0 {
							break
						}
						secondsPerStep := b.SecondsPerStep
						if secondsPerStep == 0 {
							secondsPerStep = defaultJogSecondsSeconds
						}
						turnSet(&turnText, e.setLastAction, dispatchJogSeconds(fireDir, getClient(), secondsPerStep))
					case b.IsJogMinuteSnap():
						// D1 (jogMinuteSnap binding, the CURRENT D1
						// binding): snap the playhead to the next/previous
						// whole-minute timecode boundary. Reuses the EXACT
						// SAME detent-accumulator pattern as the
						// selectClip/jogSeconds cases above
						// (jogMinuteSnapAccum is its own selectClipDetent
						// instance -- see that var's doc comment) -- raw
						// per-tick CC steps accumulate and only fire ONE
						// snap once the accumulated magnitude crosses
						// threshold. No activeTrack/selection bookkeeping
						// at all -- unlike selectClip, jogMinuteSnap has no
						// per-clip target concept to auto-reset, same as
						// jogSeconds.
						threshold := b.DetentThreshold
						if threshold <= 0 {
							threshold = defaultSelectClipDetentThreshold
						}
						fireDir := jogMinuteSnapAccum.add(step, threshold)
						if fireDir == 0 {
							break
						}
						turnSet(&turnText, e.setLastAction, dispatchJogMinuteSnap(fireDir, getClient()))
					case b.IsKeyDelta():
						// Knob rotation -> repeated keystroke (2026-08-13,
						// first use: Shift+D1 -> Resolve timeline zoom).
						// Reuses the SAME detent-threshold coalescing
						// pattern as selectClip/jogSeconds/jogMinuteSnap
						// above (raw per-tick CC steps accumulate, only
						// firing once a firm detent is crossed) rather than
						// posting one keystroke per raw CC message -- but,
						// UNLIKE those three, keyDeltaDetent.add can return
						// a count > 1 in one call, so a fast spin sends
						// several keystrokes this same tick instead of
						// being clamped to one -- see keyDeltaDetent's doc
						// comment. Accumulator is keyed PER CONTROL NAME
						// (keyDeltaAccum is a map, not a single instance),
						// allocated on first use.
						threshold := b.DetentThreshold
						if threshold <= 0 {
							threshold = defaultSelectClipDetentThreshold
						}
						acc, ok := keyDeltaAccum[control]
						if !ok {
							acc = &keyDeltaDetent{}
							keyDeltaAccum[control] = acc
						}
						count := acc.add(step, threshold)
						if count == 0 {
							break
						}
						turnSet(&turnText, e.setLastAction, dispatchKeyDelta(control, count, b.KeyCW, b.KeyCCW))
					case b.IsScrollDelta():
						// Knob rotation -> flagged CGEvent scroll
						// (2026-08-13, first use: Shift+D1 -> Alt+scroll
						// -> Resolve's own native timeline zoom,
						// superseding keyDelta on this control). Reuses
						// keyDeltaDetent -- the exact SAME multi-fire-
						// per-tick accumulator behaviour keyDelta above
						// uses (not a new struct: sign/count semantics
						// are byte-identical) -- keyed PER CONTROL NAME
						// in its own scrollDeltaAccum map, same
						// allocate-on-first-use pattern as keyDeltaAccum.
						threshold := b.DetentThreshold
						if threshold <= 0 {
							threshold = defaultSelectClipDetentThreshold
						}
						acc, ok := scrollDeltaAccum[control]
						if !ok {
							acc = &keyDeltaDetent{}
							scrollDeltaAccum[control] = acc
						}
						count := acc.add(step, threshold)
						if count == 0 {
							break
						}
						linesPerDetent := b.LinesPerDetent
						if linesPerDetent == 0 {
							linesPerDetent = defaultLinesPerDetent
						}
						turnSet(&turnText, e.setLastAction, dispatchScrollDelta(control, count, linesPerDetent, b.Flags))
					default:
						// Non-balance: process immediately (existing behavior).
						// C1 marker-mode intercept: while marker mode is
						// active, the ControlDial's jog-typed binding
						// resizes the active marker INSTEAD OF jogging the
						// playhead (dispatchMarkerResize) — see this file's
						// marker-mode section doc comment above
						// isJogPressControl. Every other control (wheel/
						// cdl/property), and jog itself whenever marker
						// mode is NOT active, falls through to
						// processNonBalanceDrain completely unchanged.
						if shouldResizeMarker(b, s.page, e.getActiveMarkerFrame()) {
							turnSet(&turnText, e.setLastAction, dispatchMarkerResize(getClient(), e.getActiveMarkerFrame(), step, markerResizeFramesPerStep, e.updateActiveMarkerDuration))
							break
						}
						turnSet(&turnText, e.setLastAction, processNonBalanceDrain(control, step, b, modeTracker, cal, getClient(), r, currentBank, cfg, queryPage, e.getActiveTrack(), sustainSnap))
					}
					if step != 0 && e.observing() {
						e.emitResolveTurn(control, step, turnText, b, currentBank, atomic.LoadInt32(&fineModeAtomic) != 0, now)
					}
				}

				// --- Two-threshold flush for balance events ---
				if len(balanceAccum) > 0 && windowStart.IsZero() {
					windowStart = now
				}

				// Flush when the coalesce window has elapsed.
				if !windowStart.IsZero() && now.Sub(windowStart) >= coalesceWindow {
					tl := cfg.TopLevel

					// DODEE/MOST window-title guard.
					// DODEE/MOST guard DISABLED per user request (2026-07-02).
					// To restore, revert to:
					//   dodeeMostGuard := tl.DodeeMostGuard == nil || *tl.DodeeMostGuard
					dodeeMostGuard := false
					_ = tl
					if dodeeMostGuard {
						title := macapp.ResolveWindowTitle()
						if dodeeMostRe.MatchString(title) {
							log.Printf("[balance] [refused-dodeemost] window title=%q matches guard — refusing all balance events", title)
							balanceAccum = map[string]int{}
							windowStart = time.Time{}
							continue
						}
					}

					// Sidecar status: page + node guard.
					status := queryStatus()

					// CST / Fusion guard.
					if refused, reason := isRefusedByNodeGuard(status); refused {
						if reason == "refused-cst" {
							log.Printf("[balance] [refused-cst] current_node=%v — refusing balance on CST node", status.currentNode)
						} else {
							log.Printf("[balance] [refused-fusion] page=%q — refusing balance (not on color page)", status.page)
						}
						balanceAccum = map[string]int{}
						windowStart = time.Time{}
						continue
					}

					frontmostID := macapp.FrontmostBundleID()
					frontmost := macapp.BundleIDIsResolve(frontmostID)
					allowed := gate.Allow(frontmost, status.page)
					modeOK := modeTracker.AllowKnobAction()

					// Flush all accumulated balance steps. Third of the three
					// drain-loop resolution sites — resolves independently
					// against the live layer AND a FRESH e.getHeldOverlay()
					// read here at flush time (not tickMod, which belongs to
					// whichever single tick most recently ran the code
					// above) — this flush spans MULTIPLE ticks' worth of
					// accumulated balance steps, so the overlay held at
					// flush time, exactly like the live layer already is,
					// is what governs the resolution, same rationale as
					// layerState.Get() below not reusing tickLayer.
					flushMod := e.getHeldOverlay()
					for control, step := range balanceAccum {
						b, _, ok := resolveTurnBindingMode(cfg, status.page, layerState.Get(), control, flushMod, pmodeState.Get())
						if !ok {
							continue
						}
						if !b.IsBalance() {
							continue
						}
						e.setLastAction(dispatchBalanceStep(control, step, b, allowed, modeOK,
							cal, aw, tl, atomic.LoadInt32(&fineModeAtomic) != 0))
					}

					balanceAccum = map[string]int{}
					windowStart = time.Time{}
				}
			}
		}
	}()

	return nil
}

// Stop cancels the running context, joins the drain-loop goroutine (which
// itself unregisters the MIDI listener and saves the shutdown sentinel as it
// exits), then closes the sidecar connection. Safe to call on an Engine that
// was never started, or to call more than once.
func (e *Engine) Stop() error {
	e.mu.Lock()
	if e.cancel == nil {
		e.mu.Unlock()
		return nil // never started (or already stopped)
	}
	e.mu.Unlock()

	e.cancel()
	e.holdCancelAll() // no hold-to-fire timer outlives the engine (wave H)
	if ad := e.hooks.appdeck.Load(); ad != nil {
		ad.CancelHolds()
	}

	if e.done != nil {
		<-e.done // join: drain-loop goroutine has unregistered MIDI + saved sentinel
	}

	// BUG 3 hardening (2026-08-12): wait for any in-flight recoverSidecar
	// goroutine (see its doc comment) to fully finish BEFORE touching
	// e.client/e.sidecarStop below. e.done joining the drain loop above
	// does NOT cover recoverSidecar — it runs on its own detached
	// goroutine specifically so a slow respawn never blocks that loop.
	// Without this wait, a recovery racing past Stop() could Reconnect()
	// a *ipc.Client Stop() is about to Close() (resurrecting a connection
	// nothing will ever close again), or its stopSidecar composition
	// could interleave with Stop()'s own sidecarStop=nil below and leak a
	// freshly-spawned process. recoverSidecar itself also checks
	// runCtx.Err() (already cancelled by e.cancel() above) at its two
	// mutating checkpoints and bails out early in the common case, so
	// this wait is normally instant.
	e.sidecarRecoveryWG.Wait()
	// Same for the lazy-connect supervisor: it may be mid launchSidecarFn
	// or mid dial (both ctx-aware after the call returns). Joined BEFORE
	// the client/sidecarStop teardown below so it can never publish a new
	// client that nothing will close.
	e.supervisorWG.Wait()
	// Everything that emits input events has stopped (drain loop joined, MIDI
	// listener gone): end the observer's delivery goroutine (F14).
	e.hooks.closeObserver()

	if client := e.clientPtr.Load(); client != nil {
		// D1 active-clip highlight: restore whatever clip is currently
		// tinted before tearing down the connection. A no-op if nothing
		// is highlighted (feature disabled, or D1 was never turned this
		// session) — safe/cheap to send unconditionally. Best-effort: a
		// send error here must not block the rest of Stop()'s teardown.
		if !e.writesAllowed() {
			// Disarmed: nothing is written to Resolve, not even to undo our own
			// tint. A stranded tint is the lesser evil; the state file stays and
			// restore_highlight runs at the next connect that is armed (B7).
			log.Printf("[highlight] disarmed at stop: clear_highlight not sent; a tinted clip is restored at the next armed connect")
		} else if _, err := client.Send(map[string]any{"op": "clear_highlight"}); err != nil {
			log.Printf("[highlight] clear_highlight send error: %v", err)
		}
		client.Close()
		e.clientPtr.Store(nil)
	}

	e.mu.Lock()
	sidecarStop := e.sidecarStop
	e.sidecarStop = nil
	e.mu.Unlock()
	if sidecarStop != nil {
		sidecarStop()
	}

	e.mu.Lock()
	e.status.Running = false
	e.status.SidecarConnected = false
	e.status.SidecarError = ""
	e.status.SidecarState = ""
	e.cancel = nil
	e.mu.Unlock()

	return nil
}

// Status returns a snapshot of the engine's current state. AccessibilityOK
// and ScreenRecordingOK are both probed live on every call (they can change
// at runtime as the user grants permission via System Settings without
// restarting the engine) — both underlying CGO calls
// (AXIsProcessTrusted/CGPreflightScreenCaptureAccess) are read-only and
// never trigger the system permission prompt themselves, so calling them on
// every Status() is safe and cheap.
func (e *Engine) Status() Status {
	e.mu.Lock()
	s := e.status
	e.mu.Unlock()
	s.AccessibilityOK = macapp.AccessibilityTrusted()
	s.ScreenRecordingOK = macapp.ScreenRecordingTrusted()
	return s
}

// setLastMIDI records a human-readable summary of the most recent MIDI event.
func (e *Engine) setLastMIDI(s string) {
	e.mu.Lock()
	e.status.LastMIDI = s
	e.mu.Unlock()
}

// setLastAction records a human-readable summary of the most recently
// DISPATCHED action, for the HUD's diagnostic foot row (Status.LastAction).
// Mirrors setLastMIDI's exact shape/lock. Like setLastMIDI (which records
// the raw MIDI decode before any gate/mode/calibration check runs),
// setLastAction is called at each dispatch* function's own "the action
// fired" point -- for the simple button/knob cases that means right after
// all of that function's internal SKIP guards have already passed, so an
// empty string (never call setLastAction at all) is used for every
// skipped/no-op path. This is a best-effort "what was dispatched" echo, not
// a confirmed downstream Resolve-side effect.
func (e *Engine) setLastAction(s string) {
	if s == "" {
		return
	}
	e.mu.Lock()
	e.status.LastAction = s
	e.mu.Unlock()
}

// mouseDragFn is the mouse drag the hueCurve action posts; a variable so tests
// record it instead of moving a real pointer.
var mouseDragFn = mouse.Drag

// dispatchHueCurve drags one point of the Color page's Hue-vs-Sat / Hue-vs-Lum
// curve (Resolve's scripting API has no access to those curves). It refuses,
// with the reason in the log and no input posted, when: the curve or hue is out
// of range, the mode tracker says no, there is no calibration, the matching
// target (hvs_red .. hvl_magenta) is not calibrated for the current window, or
// the gate says Resolve's Color page is not in front. It never clicks blindly.
func dispatchHueCurve(control string, step int, b config.Binding, modeTracker *mode.Tracker, cal *calib.Calib, queryPage func() string) string {
	key := b.HueCurveKey()
	if key == "" {
		log.Printf("[drain] control=%q step=%d method=hueCurve SKIP bad-curve-or-hue curve=%q hue=%d", control, step, b.Curve, b.Hue)
		return ""
	}
	if !modeTracker.AllowKnobAction() {
		log.Printf("[drain] control=%q step=%d method=hueCurve SKIP mode=%q", control, step, modeTracker.Current())
		return ""
	}
	if cal == nil {
		log.Printf("[drain] control=%q step=%d method=hueCurve SKIP no-calibration", control, step)
		return ""
	}
	pt, ok := cal.Point(key)
	if !ok {
		log.Printf("[drain] control=%q step=%d method=hueCurve SKIP not-calibrated target=%q (calibrate it in the editor)", control, step, key)
		return ""
	}
	frontmostID := macapp.FrontmostBundleID()
	allowed := gate.Allow(macapp.BundleIDIsResolve(frontmostID), queryPage())
	if !allowed {
		log.Printf("[drain] control=%q step=%d method=hueCurve SKIP gate frontmost=%q", control, step, frontmostID)
		return ""
	}
	sens := b.Sens
	if sens == 0 {
		sens = defaultWheelSens
	}
	capPx := b.MaxDrag
	if capPx == 0 {
		capPx = maxDragPx
	}
	delta := dragmath.Clamp(float64(step)*sens, capPx)
	var x2, y2 float64
	if delta >= 0 {
		x2, y2 = dragmath.Endpoint(pt.X, pt.Y, "v", 1, delta)
	} else {
		x2, y2 = dragmath.Endpoint(pt.X, pt.Y, "v", -1, -delta)
	}
	log.Printf("[drain] control=%q step=%d method=hueCurve target=%q dragFrom=(%.1f,%.1f) dragTo=(%.1f,%.1f)", control, step, key, pt.X, pt.Y, x2, y2)
	mouseDragFn(pt.X, pt.Y, x2, y2)
	return fmt.Sprintf("%s %+d", key, step)
}
