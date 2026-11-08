package engine

// The arm switch beyond appdeck (wave E, F7).
//
// appdeck.Executor owns the switch (POST /api/arm). While it is disarmed the
// UI promises "nothing will fire", so the engine reads the same flag for the
// things it does itself: Resolve sidecar ops that change Resolve, and the
// Finder tag writes. (Key presses, clicks, drags and scrolls are held back in
// internal/keys and internal/mouse, which the Executor pauses; shell commands
// in the Executor itself.) No switch installed = armed, as before.

// sidecarReadOnlyOps are the sidecar ops that may still be sent while
// disarmed. The list is the COMPLETE set of sidecar/sidecar.py ops that do not
// change Resolve (TestSidecarOpsAreAllClassified reads the dispatcher and
// fails when an op appears that is in neither list), so everything else,
// including any op added later, is held back (default deny):
//
//	ping, status, clips_at_playhead, marker_at_playhead   pure queries
//	refresh, set_node                                      the sidecar's own connection / node state
//
// clear_highlight and restore_highlight are NOT here (wave G, B7): they call
// SetClipColor, i.e. they write to Resolve, and restore_highlight ran at every
// connect. Held back while disarmed; the decision is that a stranded tint is
// better than a write while the UI promises nothing fires. The connect-time
// restore is remembered (highlightRestorePending) and runs when the deck is
// armed; Stop() while disarmed leaves the state file for the next armed connect.
var sidecarReadOnlyOps = map[string]bool{
	"ping": true, "status": true, "clips_at_playhead": true, "marker_at_playhead": true,
	"refresh": true, "set_node": true,
}

// sidecarMutatingOps are the ops held back while disarmed. Documentation and
// the drift test: the gate itself is "not read-only".
var sidecarMutatingOps = []string{
	"bump_property", "reset_property", "highlight_clip", "set_clips_color", "color_by_render_state",
	"select_clip_vertical", "select_clip_horizontal", "jog", "jog_seconds", "jog_minute_snap", "render",
	"add_marker", "resize_marker", "set_marker_color", "delete_marker",
	"delta", "sat", "reset", "reset_param",
	"clear_highlight", "restore_highlight",
}

// writesAllowed reports whether the engine may change things outside itself:
// true unless an appdeck executor is installed and disarmed.
func (e *Engine) writesAllowed() bool {
	ad := e.hooks.appdeck.Load()
	return ad == nil || ad.Armed()
}

// sidecarGate is the ipc.Client gate: true refuses the op.
func (e *Engine) sidecarGate(op string) bool {
	if sidecarReadOnlyOps[op] {
		return false
	}
	return !e.writesAllowed()
}
