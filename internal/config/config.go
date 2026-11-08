package config

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
)

// PressAction overrides what happens when a knob is pressed (note_on) on a
// wheel or CDL binding. It is ignored on standalone click/key bindings (those
// already have a well-defined press action).
//
// Type values:
//
//	"key"           – inject a keyboard shortcut via keys.Send.  Key field must be set.
//	"click"         – mouse-click a named calibration point.     Point field must be set.
//	"none"          – do nothing (suppress the default reset action).
//	"selectWheel"   – set the active color wheel.               Wheel field must be set.
//	"resetActiveWheel" – click the active wheel's _reset point. No extra fields needed.
type PressAction struct {
	// Type is "key", "click", "none", "selectWheel", or "resetActiveWheel". Required.
	Type string `json:"type"`

	// Key is the shortcut spec for type "key" (e.g. "Cmd+Z").
	Key string `json:"key,omitempty"`

	// Point names a calibration.json key for type "click".
	Point string `json:"point,omitempty"`

	// Wheel names the color wheel to make active, for type "selectWheel".
	// Valid values: "lift", "gamma", "gain", "offset".
	Wheel string `json:"wheel,omitempty"`
}

// Binding maps a control to a typed action.
//
// Types:
//
//   - "wheel" (or "" / "cdl" for backward compat CDL): existing behaviours.
//
//   - "cdl":   API CDL parameter — Param and Ch are set.
//
//   - "click": mouse-click a named calibration point — Point is set.
//
//   - "key":   send a keyboard shortcut — Key is set (e.g. "Alt+S", "Cmd+Z").
//
//   - "balance": push the active primary wheel toward a hue — Hue (0-7) is set.
//
//   - "selectWheel": set the active color wheel — used as Press.Type on knob bindings.
//
//   - "resetActiveWheel": click the active wheel's reset point (for buttons like L1).
//
//   - "property": Edit-page relative read-modify-write of a TimelineItem
//     property (or a ganged list of properties) via the sidecar's
//     bump_property op — Property or Gang is set, plus Sens/MaxDelta/Min/Max.
//
//   - "jog": Edit-page playhead scrub via the sidecar's jog op — MaxDelta
//     optionally caps the per-tick frame delta (Sens is unused for jog;
//     acceleration comes from internal/tc.Accelerate). JogTier5Min/10Min/
//     30Min tune the speed-based snap tiers; JogSustainMs/JogSustainSeconds
//     tune the DURATION-based sustained-spin escalation (dial continuously
//     for JogSustainMs → force a JogSustainSeconds snap, overriding the
//     speed tier).
//
//   - "jogSeconds": Edit-page D1 knob rotation — COARSE playhead jump, the
//     coarse counterpart to "jog" above. Each firm detent (raw relative-CC
//     steps accumulated via the SAME DetentThreshold mechanism the
//     (unreachable-in-production) "selectClip" binding below uses) moves
//     the playhead by ±SecondsPerStep seconds via the sidecar's
//     jog_seconds op, sign matching the dial's spin direction. No
//     acceleration curve, no speed tiers, no sustain escalation — always
//     exactly one fixed-size jump per detent. Superseded on D1 by
//     "jogMinuteSnap" below (a fixed ±60s OFFSET from wherever the
//     playhead happened to be felt less predictable than landing on a
//     round timecode) — this type/its dispatch machinery is kept,
//     unreachable in production but still directly unit-tested, rather
//     than deleted, same posture as "selectClip" before it.
//
//   - "jogMinuteSnap": Edit-page D1 knob rotation — the CURRENT D1
//     binding. Each firm detent (same DetentThreshold accumulator as
//     "selectClip"/"jogSeconds" above) snaps the playhead onto the NEXT
//     (dial forward) or PREVIOUS (dial back) whole-minute timecode
//     boundary (SS:FF == "00:00" in the shown timecode) via the
//     sidecar's jog_minute_snap op — landing exactly on a boundary
//     still advances a full minute in that direction (never a no-op).
//     An out-of-timeline-range snap is a genuine no-op (playhead stays
//     put) rather than clamping to the timeline edge. No binding-
//     specific fields beyond the shared DetentThreshold.
//
//   - "quickexport": C5 button — folder-picker (remembered across presses)
//     plus render via the sidecar's render op, dispatched asynchronously
//     so it never blocks the MIDI-callback goroutine.
//
//   - "selectClip": Edit-page D1 knob rotation — cycles which video
//     track's clip (under the playhead) the property/reset dials
//     target, via the sidecar's clips_at_playhead op. No binding-
//     specific fields; the currently-selected track is Engine state
//     (activeTrack), not something stored in bindings.json.
//
//   - "addMarker": Edit-page C1 button — adds a marker at the playhead
//     via the sidecar's add_marker op. MarkerDurationSeconds/
//     MarkerColor/MarkerName configure the marker; MarkerDurationSeconds
//     and MarkerColor fall back to 1s/"Blue" (applied at dispatch time
//     by engine.go, same pattern as Sens/MaxDelta defaults) when zero/
//     absent. MarkerName is optional (empty means no name).
//
//   - "markerColor": Edit-page P1-8btn buttons — sets the C1 "marker
//     mode" active marker's color via the sidecar's set_marker_color
//     op, ONLY while marker mode is active (a no-op press otherwise —
//     see engine.go's dispatchSetMarkerColor). Reuses the MarkerColor
//     field (no per-binding default applied by config.Load — P1-8btn
//     always specify an explicit color in bindings.json).
//
//   - "toggleHud": C6 button — toggles the native always-on-top HUD
//     status-monitor panel's visibility via the app's injected toggle
//     callback (internal/hud, wired through engine.Engine — see
//     dispatchNoteOn's IsToggleHud() case). Pure UI action: no CGEvent,
//     no arming, no Resolve-frontmost/page gate. No binding-specific
//     fields.
//
//   - "colorByRenderState": paints EVERY video clip in the timeline by
//     whether its name marks it a render — Rendered/Neutral name the two
//     Resolve clip colours (default Green/Yellow). Offline clips are left
//     alone so Resolve's own missing-media flag survives. The only binding
//     that writes to the project; fires on a press, never from a poll, and
//     Resolve's undo covers it.
//
//   - "toggleHudAndColor": "toggleHud" and "colorByRenderState" on one
//     button — toggles the HUD, and repaints the timeline on the press that
//     brings the HUD BACK (never on the way out, which would write to the
//     project just as you stop looking at it). Takes the same
//     Rendered/Neutral fields.
//
//   - "selectClipVert": Edit-page Up/Down buttons — direction-button
//     clip selection (replaces D1's knob-turn "selectClip" rotation).
//     Steps the selection to the next occupied video track above/below
//     the current selection at the playhead, via the sidecar's
//     select_clip_vertical op. Direction is derived from the CONTROL
//     NAME at dispatch time ("Up" → toward higher/topmost track
//     numbers, "Down" → toward lower/bottommost), not a binding field —
//     see engine.go's dispatchSelectClipVertical. No binding-specific
//     fields.
//
//   - "selectClipHoriz": Edit-page Left/Right buttons (page-aware —
//     these controls are HARD-WIRED to color-page node prev/next
//     navigation everywhere else; only the "edit" bank's binding for
//     them is consulted, and only while page=="edit") — selects the
//     previous/next clip (by start time, same track preferred, falling
//     back to the nearest clip on any track) and moves the playhead
//     onto it, via the sidecar's select_clip_horizontal op. Direction
//     is derived from the control name ("Left" → previous, "Right" →
//     next), same convention as "selectClipVert". No binding-specific
//     fields.
//
//   - "selectClipClear": Edit-page C5 button — unselects the current
//     direction-button selection and restores the tinted clip's
//     original color via the sidecar's existing clear_highlight op. No
//     binding-specific fields.
//
//   - "modifierHold" (Part B, 2026-08-12 REV 2 design review): the ONE
//     mechanism for all four deck modifier buttons -- Cmd/note 68,
//     Alt/note 69, Shift/note 66, Ctrl/note 67. Press sets the engine's
//     held-overlay state to Modifier (e.g. "cmd"); release clears it. A
//     DIFFERENT, deliberately SEPARATE mechanism from the layer system
//     (internal/layer) — never modeled as a layer number, and posts NO
//     CGEvent at press/release time at all. See engine.go's
//     resolveOverlayBinding for how a held overlay changes what a LATER
//     control resolves against ({page}-{mod} then col-{mod}, checked
//     before the normal layer/page cascade). Replaces the reverted
//     "modifier" type, whose press performed a real armed keys.SendDown
//     with no reliable release safety net.
//
//     A real, OS-level held-modifier "passthrough" capability was built
//     on top of this overlay (2026-08-13, Alt/Shift/Ctrl) and then
//     REMOVED the same day: two independent live tests (first a
//     KeyDown/KeyUp posting, then a corrected kCGEventFlagsChanged
//     posting) both proved macOS does not apply a synthetically posted
//     modifier to REAL hardware input (e.g. Alt+real-mouse-scroll) --
//     only a CGEvent tap could, and a design review already ruled that
//     out as NO-GO. It also actively cost the user: its safety-net
//     watchdog fired mid-gesture once and force-released a held modifier
//     during real use. See this package's git history (commits
//     b815a1b..7dca2d1) for the full built-then-reverted design if this
//     is ever revisited with a CGEvent tap. The overlay half above is
//     unaffected and is the only capability modifierHold has now.
//
//     Overlay PRECEDENCE when more than one modifierHold button is held at
//     once: the overlay is a SINGLE name (heldOverlayName) -- a later
//     press unconditionally OVERWRITES whatever overlay name was held
//     before (see engine.go's setHeldOverlay), so the MOST RECENTLY
//     PRESSED modifier's chord bank is what a subsequent control resolves
//     against. Releasing the currently-reflected overlay name clears it
//     to "" (nothing held) even if another modifier is still down -- it
//     does NOT revert to that other modifier's chord bank. This is a
//     deliberately simple "last-pressed-wins, no stack" rule, not a
//     limitation of the underlying data structure -- combination chord
//     banks (e.g. a hypothetical "col-alt-shift") are explicitly OUT OF
//     SCOPE for this pass, but nothing about this design makes them
//     impossible to add later (Modifier would just need to become a
//     composite key on such a future binding).
//
// Optional: "mode" sets the active primary mode as a side-effect of any action.
// Optional: "press" overrides the knob-press (note_on) action for wheel/cdl bindings.
type Binding struct {
	// Type is "wheel", "cdl", "click", "key", "balance", "selectWheel",
	// "resetActiveWheel", "property", "jog", "jogSeconds", "jogMinuteSnap",
	// "keyDelta" (see KeyCW/KeyCCW below),
	// "scrollDelta" (see Flags/LinesPerDetent below),
	// "quickexport", "selectClip", "addMarker", "markerColor",
	// "markerAtPlayhead", "toggleHud", "modifierHold",
	// "colorByRenderState", "toggleHudAndColor",
	// "selectClipVert", "selectClipHoriz", "selectClipClear", or
	// "finderTag" (see Tag below). Empty /
	// "cdl" are treated as "cdl" for backward compatibility.
	Type string `json:"type,omitempty"`

	// HoldMs (wave H) delays a "key" press: it fires only if the control is
	// still held after this many milliseconds (0 or absent = fire at once, the
	// behaviour before this field existed; at most 2000). A float so a stored
	// 300.0 cannot break the whole config load. Read by the engine's Resolve
	// key path; the same field on other apps is run by internal/appdeck.
	HoldMs float64 `json:"holdMs,omitempty"`

	// Method is the legacy field; still accepted when Type is absent.
	// Deprecated: use Type instead.
	Method string `json:"method,omitempty"`

	// CDL fields — used when Type/Method is "" or "cdl".
	Param string `json:"param,omitempty"` // offset|slope|power|sat
	Ch    string `json:"ch,omitempty"`    // r|g|b|master

	// Wheel field — used when Type/Method is "wheel".
	// Names a key in calibration.json (e.g. "lift_master").
	Wheel string `json:"wheel,omitempty"`

	// Sens overrides the default drag sensitivity (pixels per accumulated step)
	// for wheel and balance bindings, and the delta-per-step multiplier for
	// "property" bindings. Zero or absent means use the type's default.
	Sens float64 `json:"sens,omitempty"`

	// MaxDrag caps the absolute pixel delta for a single wheel drag tick.
	// Zero or absent means use the global defaultMaxDragPx constant in main.
	// For balance bindings, zero or absent means use Config.MaxBalanceDragPx.
	MaxDrag float64 `json:"maxDrag,omitempty"`

	// Point names a calibration.json key to click — used when Type is "click".
	Point string `json:"point,omitempty"`

	// Key is a keyboard shortcut spec — used when Type is "key".
	// Format: modifier(s) joined by "+", then key name.
	// Examples: "Alt+S", "Cmd+Z", "Ctrl+Shift+Z", "Left".
	Key string `json:"key,omitempty"`

	// Mode sets the active primary mode as a side-effect of this binding.
	// Values: "wheels", "bars", "log". Empty means no mode change.
	Mode string `json:"mode,omitempty"`

	// Layer is the target layer (1–3) — used when Type is "layer".
	// The binding is a pure state mutation: pressing it changes which bank
	// subsequent controls resolve against. See internal/layer.
	Layer int `json:"layer,omitempty"`

	// PMode selects the P1-P8 slider/button mode — "hue", "sat" or "lum",
	// mirroring the console's engraved Hue/Sat/Lum buttons. Used when Type
	// is "pmode".
	PMode string `json:"pmode,omitempty"`

	// Target names the reset target this binding selects — used when Type is
	// "selectTarget". The legacy "selectWheel" spelling carries the same
	// meaning in Wheel; TargetName() reads whichever is set.
	Target string `json:"target,omitempty"`

	// Rendered/Neutral are the Resolve clip colours "colorByRenderState"
	// paints with. Both optional — the sidecar falls back to Green/Yellow, so
	// the binding needs nothing but its type to work.
	Rendered string `json:"rendered,omitempty"`
	Neutral  string `json:"neutral,omitempty"`

	// Press overrides the knob-press (note_on) action for wheel and cdl bindings.
	// When nil the existing default applies (wheel → click _reset; cdl → reset_param).
	// Ignored for standalone click and key bindings.
	Press *PressAction `json:"press,omitempty"`

	// Hue selects the color balance hue index (0–7) for type "balance".
	// 0=Red, 1=Orange, 2=Yellow, 3=Green, 4=Cyan, 5=Blue, 6=Purple, 7=Magenta.
	Hue int `json:"hue,omitempty"`

	// Curve selects which Hue-vs curve a "hueCurve" binding drags: "sat" (Hue vs
	// Sat) or "lum" (Hue vs Lum). The hue position is Hue (0-7), as for balance.
	Curve string `json:"curve,omitempty"`

	// Property names a single TimelineItem property to relatively
	// read-modify-write (e.g. "Pan", "Tilt", "CropTop") — used when Type is
	// "property" and Gang is empty.
	Property string `json:"property,omitempty"`

	// Gang lists multiple TimelineItem property names to bump together with
	// the SAME per-tick delta (each is read+modified INDEPENDENTLY server-
	// side — never mirrored from one member to another), e.g.
	// ["ZoomX","ZoomY"] for a uniform-scale dial. Used when Type is
	// "property"; non-empty Gang takes precedence over Property.
	Gang []string `json:"gang,omitempty"`

	// Min/Max optionally clamp the ABSOLUTE resulting property value (not
	// the per-tick delta — see MaxDelta for that) after each bump_property
	// call. Pointers so an explicit 0 is distinguishable from "unset"; nil
	// means unclamped in that direction. Used when Type is "property".
	Min *float64 `json:"min,omitempty"`
	Max *float64 `json:"max,omitempty"`

	// MaxDelta caps the absolute per-tick delta for "property" bindings
	// (post-Sens-multiplication, pre-server-side-clamp) and the absolute
	// per-tick frame count for "jog" bindings. Same shape as MaxDrag for
	// wheel bindings, named distinctly because the unit differs by binding
	// (property: pixels/zoom-multiplier/degrees; jog: whole frames). Zero
	// or absent means no cap.
	MaxDelta float64 `json:"maxDelta,omitempty"`

	// Base and Gamma tune the "jog" acceleration curve, where the per-tick
	// frame delta = Base * |accumulatedStep|^Gamma (sign preserved, rounded
	// to whole frames, then capped by MaxDelta). Base is the frames moved by
	// the slowest single-step turn (keep ~1.0 for precise 1-frame nudging);
	// Gamma>1 makes a fast spin accelerate — higher Gamma ramps harder. Zero
	// or absent falls back to the jog defaults (Base 1.0, Gamma 1.6). Unused
	// by non-jog bindings.
	Base  float64 `json:"base,omitempty"`
	Gamma float64 `json:"gamma,omitempty"`

	// JogTier5Min, JogTier10Min, JogTier30Min tune the |accumulatedStep|
	// thresholds that map ControlDial spin speed to jog behavior — see
	// internal/tc.JogTier, which this engine calls with these exact
	// values. Below JogTier5Min the jog stays in the FINE tier (unchanged
	// frame-by-frame scrub via Base/Gamma above); at or above JogTier5Min
	// it snaps the playhead to the next 5-second round timecode boundary;
	// at or above JogTier10Min, the next 10-second boundary; at or above
	// JogTier30Min, the next 30-second boundary. Zero or absent for any
	// field falls back to the internal/tc package defaults
	// (DefaultJogTier5Min=4, DefaultJogTier10Min=8, DefaultJogTier30Min=13)
	// — this is what makes the tiers editable in bindings.json without a
	// rebuild while still having sensible code defaults. Unused by
	// non-jog bindings.
	JogTier5Min  int `json:"jogTier5Min,omitempty"`
	JogTier10Min int `json:"jogTier10Min,omitempty"`
	JogTier30Min int `json:"jogTier30Min,omitempty"`

	// JogSustainMs and JogSustainSeconds tune the SUSTAINED-spin
	// escalation: when the ControlDial has been dialled CONTINUOUSLY (no
	// pause) for at least JogSustainMs milliseconds, the jog snap tier
	// escalates to JogSustainSeconds (a 1-minute boundary by default),
	// OVERRIDING whatever JogTier5Min/JogTier10Min/JogTier30Min would
	// otherwise have picked from that tick's raw |step| speed -- see
	// internal/tc.JogStreak/SustainSnapSeconds, which this engine calls
	// with these exact values once per 50ms drain tick. Zero or absent
	// for either field falls back to the internal/tc package defaults
	// (DefaultJogSustainMs=1000, DefaultJogSustainSeconds=60) -- this is
	// what makes the sustain threshold editable in bindings.json without
	// a rebuild, mirroring the JogTier5Min/etc. precedent above. Unused
	// by non-jog bindings.
	JogSustainMs      int `json:"jogSustainMs,omitempty"`
	JogSustainSeconds int `json:"jogSustainSeconds,omitempty"`

	// MarkerDurationSeconds sets the duration (in seconds) of the marker
	// added by an "addMarker" binding, via the sidecar's add_marker op
	// (converted to frames there using the live timeline's fps). Zero or
	// absent falls back to defaultMarkerDurationSeconds (1) — applied at
	// dispatch time by engine.go's dispatchAddMarker, NOT here in
	// config.Load, same pattern as Sens/MaxDelta's per-binding defaults.
	// Unused by non-addMarker bindings.
	MarkerDurationSeconds float64 `json:"markerDurationSeconds,omitempty"`

	// MarkerColor is the Resolve marker color name (e.g. "Blue") — used
	// by TWO binding types, both sharing this one field (never both at
	// once on the same binding, since a control has exactly one Type):
	//   - "addMarker": the color the new marker is created with. Zero/
	//     absent falls back to defaultMarkerColor ("Blue") at dispatch
	//     time in engine.go, same pattern as MarkerDurationSeconds above.
	//   - "markerColor" (P1-8btn): the color a P-button sets the C1
	//     marker-mode active marker to. No config.Load default applied —
	//     P1-8btn always specify an explicit color in bindings.json.
	// Not validated here — the sidecar's Timeline.AddMarker call is the
	// source of truth for valid marker color names. Unused by any other
	// binding type.
	MarkerColor string `json:"markerColor,omitempty"`

	// MarkerName is an optional name/label for the marker added by an
	// "addMarker" binding. Empty means no name (the sidecar passes ""
	// through to Timeline.AddMarker, a valid "unnamed marker" value).
	// Unused by non-addMarker bindings.
	MarkerName string `json:"markerName,omitempty"`

	// Tag is the macOS Finder colour tag NAME applied by a "finderTag"
	// binding -- one of Red, Orange, Yellow, Green, Blue, Purple, Gray (see
	// internal/finder.ColourNames). The EMPTY string is meaningful here and
	// is not "unset": it means "clear the colour tags", which is what P8btn
	// is bound to. A finderTag binding is therefore identified by its Type
	// alone, never by Tag being non-empty.
	//
	// Not validated here -- internal/finder.ApplyColour rejects an unknown
	// colour at dispatch time, same posture as MarkerColor above deferring
	// to the sidecar. Unused by any other binding type.
	Tag string `json:"tag,omitempty"`

	// DetentThreshold tunes the D1 knob's per-detent sensitivity: raw
	// relative-CC steps accumulate (see engine.go's selectClipDetent) and
	// only fire ONE action once the accumulated magnitude reaches this
	// many raw steps — never more than one action per firm detent,
	// regardless of how large a single burst is. Shared by THREE binding
	// types (never more than one at once on the same binding, since a
	// control has exactly one Type): "selectClip" (advance the clip
	// selection by one), "jogSeconds" (jump the playhead by one
	// ±SecondsPerStep step), and "jogMinuteSnap" (snap the playhead to
	// the next/previous whole-minute timecode boundary).
	// Zero or absent falls back to defaultSelectClipDetentThreshold (a
	// conservative default tuned against the physical encoder's
	// oversensitive raw-step rate) at dispatch time in engine.go, same
	// pattern as MarkerDurationSeconds/MarkerColor's per-binding
	// defaults above. A value of 1 restores the original (pre-tuning)
	// "fire on every raw step" behavior — useful for tests exercising
	// something OTHER than the sensitivity curve itself. Unused by any
	// other binding type.
	DetentThreshold int `json:"detentThreshold,omitempty"`

	// SecondsPerStep sets the coarse playhead-jump size (in seconds) for a
	// "jogSeconds" binding (D1's coarse counterpart to the ControlDial's
	// fine "jog"): each firm detent (see DetentThreshold above) moves the
	// playhead by exactly this many seconds via the sidecar's jog_seconds
	// op, sign matching the dial's spin direction. Zero or absent falls
	// back to defaultJogSecondsSeconds (60) at dispatch time in
	// engine.go, same per-binding-default pattern as
	// MarkerDurationSeconds/MarkerColor/DetentThreshold above. Unused by
	// non-jogSeconds bindings.
	SecondsPerStep float64 `json:"secondsPerStep,omitempty"`

	// WhenPMode gates a "markerAtPlayhead" binding (Feature 1, P1-8btn on
	// the Edit page) to fire only while the live P-row mode (hue/sat/lum,
	// see PMode/IsPMode above) equals this value. Empty (the zero value)
	// means ungated -- fires regardless of the live P-mode. Checked at
	// dispatch time by engine.go's dispatchMarkerAtPlayhead, NOT here in
	// config.Load, same pattern as every other per-binding dispatch-time
	// field above. This is a one-off gate scoped to markerAtPlayhead's own
	// direct intercept, not a generic cross-type mechanism -- see the
	// REV 2 design review's correction on this point. Unused by any other
	// binding type.
	WhenPMode string `json:"whenPMode,omitempty"`

	// Modifier names the held modifier for a "modifierHold" binding: the
	// held-overlay state name a press sets and a release clears. NEVER
	// passed to keys.Send directly — purely (a) the engine's internal
	// held-overlay state name, and (b) the bank-name SUFFIX
	// resolveOverlayBinding appends when looking up a chord target ("cmd"
	// -> tries "{page}-cmd" then "col-cmd"). Not validated here. Unused by
	// any other binding type.
	Modifier string `json:"modifier,omitempty"`

	// KeyCW/KeyCCW are the keyboard shortcut specs for a "keyDelta"
	// binding's two rotation directions (2026-08-13) -- KeyCW fires on a
	// clockwise detent, KeyCCW on a counter-clockwise one, each via
	// keys.Send (same spec syntax as the "key" binding's Key field, e.g.
	// "Cmd+="). DetentThreshold (shared with selectClip/jogSeconds/
	// jogMinuteSnap above) tunes the per-detent raw-CC-step sensitivity.
	//
	// UNLIKE selectClip/jogSeconds/jogMinuteSnap, a keyDelta binding CAN
	// fire MORE THAN ONCE in a single 50ms drain tick when a fast spin
	// crosses several detents at once -- see engine.go's keyDeltaDetent, a
	// DISTINCT accumulator from selectClipDetent (which deliberately caps
	// at exactly one fire per call, "no multi-jump per turn": a
	// whole-minute snap or a clip-selection jump is a "big" action where
	// several jumps in one tick would be jarring). A keyDelta binding's
	// action is a single lightweight keystroke (e.g. Resolve's own Zoom
	// Timeline In/Out) -- firing several per tick during a fast spin is
	// the expected feel here, not an edge case to suppress, so its
	// accumulator returns a signed COUNT of detents fired this call
	// instead of clamping to one.
	//
	// Both fields are keys.Send specs, not validated here (same posture as
	// the "key" binding's Key field) -- an empty KeyCW/KeyCCW for the
	// direction actually spun is a silent no-op at dispatch time (see
	// engine.go's dispatchKeyDelta), not a config.Load-time error.
	KeyCW  string `json:"keyCW,omitempty"`
	KeyCCW string `json:"keyCCW,omitempty"`

	// Flags is the modifier-name spec for a "scrollDelta" binding
	// (2026-08-13) -- e.g. "Alt" -- parsed via keys.ParseModifierFlags
	// (the SAME modifier-name table Send's own "Mod+Key" spec parsing
	// uses for every part except the final key name, reused rather than
	// a second parser) into the CGEventFlags bitmask carried DIRECTLY ON
	// the posted scroll event itself, via mouse.Scroll. See
	// mouse.Scroll's doc comment for why an explicit per-event flags
	// bitmask -- not an ambient synthetic modifier hold (the abandoned
	// modifierHold "passthrough" capability, see that Type's doc comment
	// above) -- is what actually gets honoured by macOS for a
	// hardware-shaped event like a scroll.
	//
	// Empty is a valid "no modifier" spec (0 flags, same as
	// ParseModifierFlags("")). Not validated here -- an unknown modifier
	// name is a dispatch-time no-op + log line (same defensive posture
	// as KeyCW/KeyCCW's empty-spec no-op above), not a config.Load-time
	// error. Unused by any other binding type.
	Flags string `json:"flags,omitempty"`

	// LinesPerDetent is the signed scroll-wheel LINE count posted per
	// firm detent for a "scrollDelta" binding (2026-08-13). A call that
	// fires N detents (positive = clockwise, negative =
	// counter-clockwise -- see keyDeltaDetent, reused as-is by
	// engine.go's scrollDeltaAccum for the identical multi-fire-per-tick
	// accumulation keyDelta already uses) posts a single scroll event of
	// N * LinesPerDetent lines. Zero/absent falls back to
	// defaultLinesPerDetent (1) at dispatch time in engine.go, same
	// per-binding-default pattern as SecondsPerStep/
	// MarkerDurationSeconds above.
	//
	// SIGN IS THE CONFIGURABLE FIX for a direction that comes out
	// backwards live (e.g. if a clockwise turn ends up zooming OUT
	// instead of IN): flip LinesPerDetent's own sign in bindings.json
	// (e.g. 1 -> -1) to invert BOTH directions at once -- no code change
	// or rebuild needed, just a config edit. This field's sign IS the
	// "clockwise = positive detent count" convention's only knob,
	// deliberately not a second "invert"/"reverse" boolean living
	// alongside it. Unused by any other binding type.
	LinesPerDetent int `json:"linesPerDetent,omitempty"`

	// Cmd and Args are the program and argument vector of a "shell" binding
	// (generic app verb, executed by internal/appdeck only, never by the
	// Resolve dispatchers). Run with exec.Command: no shell, no expansion.
	Cmd  string   `json:"cmd,omitempty"`
	Args []string `json:"args,omitempty"`
}

// effectiveType returns the canonical type string, honouring the legacy Method field.
func (b Binding) effectiveType() string {
	if b.Type != "" {
		return b.Type
	}
	if b.Method != "" {
		return b.Method
	}
	return "cdl"
}

// IsCDL returns true if this is a CDL binding (default).
func (b Binding) IsCDL() bool {
	t := b.effectiveType()
	return t == "" || t == "cdl"
}

// IsSelectTarget returns true if this binding selects the active reset target.
//
// "selectWheel" is accepted as an alias so every existing config keeps working
// with no edits — the layer spec's decision 3. effectiveType() only falls back
// Type→Method and does no type-string aliasing, so the alias must live here
// rather than being inherited for free.
func (b Binding) IsSelectTarget() bool {
	t := b.effectiveType()
	return t == "selectTarget" || t == "selectWheel"
}

// TargetName returns the reset target this binding selects, reading the legacy
// Wheel field when Target is unset.
func (b Binding) TargetName() string {
	if b.Target != "" {
		return b.Target
	}
	return b.Wheel
}

// IsResetSelected returns true if this binding resets the active target.
// "resetActiveWheel" is accepted as an alias, same rationale as
// IsSelectTarget.
func (b Binding) IsResetSelected() bool {
	t := b.effectiveType()
	return t == "resetSelected" || t == "resetActiveWheel"
}

// IsPMode returns true if this binding selects the P1-P8 mode (hue/sat/lum).
func (b Binding) IsPMode() bool { return b.effectiveType() == "pmode" }

// IsLayer returns true if this binding switches the console's active layer.
// The Layer field carries the target (1–3); internal/layer clamps the range,
// so an out-of-range value in config is ignored rather than honoured.
func (b Binding) IsLayer() bool { return b.effectiveType() == "layer" }

// IsWheel returns true if this is a wheel (mouse drag) binding.
func (b Binding) IsWheel() bool { return b.effectiveType() == "wheel" }

// IsClick returns true if this is a calibration-point click binding.
func (b Binding) IsClick() bool { return b.effectiveType() == "click" }

// IsKey returns true if this is a keyboard shortcut binding.
func (b Binding) IsKey() bool { return b.effectiveType() == "key" }

// IsBalance returns true if this is a color-balance drag binding.
func (b Binding) IsBalance() bool { return b.effectiveType() == "balance" }

// IsHueCurve is a vertical mouse drag on the Color page's Hue-vs-Sat / Hue-vs-Lum
// curve at one of the eight hue positions (Resolve's scripting API cannot touch
// those curves). Refused unless the matching calibration target exists.
func (b Binding) IsHueCurve() bool { return b.effectiveType() == "hueCurve" }

// HueCurveKey is the calibration target key of a hueCurve binding, e.g.
// "hvs_red", or "" when curve or hue is out of range.
func (b Binding) HueCurveKey() string { return HueCurveKey(b.Curve, b.Hue) }

var hueKeyNames = []string{"red", "orange", "yellow", "green", "cyan", "blue", "purple", "magenta"}

// HueCurveKey builds the calibration target key for a curve ("sat"|"lum") at
// hue index 0-7: hvs_red .. hvl_magenta.
func HueCurveKey(curve string, hue int) string {
	if hue < 0 || hue >= len(hueKeyNames) {
		return ""
	}
	switch curve {
	case "sat":
		return "hvs_" + hueKeyNames[hue]
	case "lum":
		return "hvl_" + hueKeyNames[hue]
	}
	return ""
}

// IsResetActiveWheel returns true if this is a reset-active-wheel button binding.
func (b Binding) IsResetActiveWheel() bool { return b.effectiveType() == "resetActiveWheel" }

// IsSelectWheel returns true if this is a select-active-wheel press action binding.
// Note: selectWheel is used as the Type on a standalone Binding (not just in PressAction).
func (b Binding) IsSelectWheel() bool { return b.effectiveType() == "selectWheel" }

// IsFineModeHold returns true if this is a fine-mode-hold button binding.
// While the button is held, sens is multiplied by FineFactor.
func (b Binding) IsFineModeHold() bool { return b.effectiveType() == "fineModeHold" }

// IsResetActiveWheelByCenter returns true if this is a reset-by-center binding.
// Executes a double-click (or reset-button click, depending on resetMode) to
// zero the active wheel's balance indicator.
func (b Binding) IsResetActiveWheelByCenter() bool {
	return b.effectiveType() == "resetActiveWheelByCenter"
}

// IsColorByRenderState returns true if this binding paints the timeline's clip
// colours by render state. Alias "colorRenders" reads better on a button label
// than the full type name does.
func (b Binding) IsColorByRenderState() bool {
	t := b.effectiveType()
	return t == "colorByRenderState" || t == "colorRenders"
}

// IsResyncAll returns true if this is a resync-all-wheels binding.
// Fires the active reset path for all 4 wheels in sequence.
func (b Binding) IsResyncAll() bool { return b.effectiveType() == "resyncAll" }

// IsProperty returns true if this is an Edit-page relative
// read-modify-write TimelineItem property binding (single Property or a
// Gang of properties), dispatched via the sidecar's bump_property op.
func (b Binding) IsProperty() bool { return b.effectiveType() == "property" }

// IsJog returns true if this is an Edit-page playhead-scrub binding,
// dispatched via the sidecar's jog op.
func (b Binding) IsJog() bool { return b.effectiveType() == "jog" }

// IsJogSeconds returns true if this is the D1 knob's Edit-page COARSE
// playhead-jump binding — the coarse counterpart to the ControlDial's fine
// "jog": each firm detent (see DetentThreshold) moves the playhead by
// ±SecondsPerStep seconds (default 60), sign matching the dial's spin
// direction, via the sidecar's jog_seconds op. Reuses the exact same raw-
// step detent-accumulator pattern as the (unreachable-in-production) D1
// "selectClip" binding below — see selectClipDetent's doc comment in
// engine.go.
//
// D1 itself is bound to "jogMinuteSnap" (see IsJogMinuteSnap below) in the
// shipped config/bindings.json as of the D1 whole-minute-snap feature —
// this type/predicate is never actually reached via the real config file
// any more (same as IsSelectClip below before it): its dispatch machinery
// (engine.go's dispatchJogSeconds) is kept, unreachable in production but
// still directly unit-tested, rather than deleted.
func (b Binding) IsJogSeconds() bool { return b.effectiveType() == "jogSeconds" }

// IsJogMinuteSnap returns true if this is the D1 knob's Edit-page
// whole-minute playhead-SNAP binding — the CURRENT D1 binding, superseding
// "jogSeconds" above. Each firm detent (see DetentThreshold) snaps the
// playhead onto the NEXT (dial forward) or PREVIOUS (dial back) whole-
// minute timecode boundary (SS:FF == "00:00" in the shown timecode) via
// the sidecar's jog_minute_snap op — landing exactly on a boundary still
// advances a full minute in that direction (never a no-op), mirroring the
// ControlDial's jog_snap tier behavior. An out-of-timeline-range snap is a
// genuine no-op (playhead stays put) rather than clamping to the edge —
// see ResolveDriver.jog_minute_snap's docstring. Reuses the exact same
// raw-step detent-accumulator pattern as "selectClip"/"jogSeconds" above
// — see selectClipDetent's doc comment in engine.go.
func (b Binding) IsJogMinuteSnap() bool { return b.effectiveType() == "jogMinuteSnap" }

// IsKeyDelta returns true if this is a knob-rotation "repeated keystroke"
// binding (2026-08-13) -- each firm detent posts KeyCW (clockwise) or
// KeyCCW (counter-clockwise) via keys.Send, gated on Resolve being
// frontmost, the SAME gate a "key" (button) binding uses -- a knob turn's
// keystroke is exactly as real an OS-level event as a button's. See
// KeyCW's doc comment for how the per-tick fire count differs from
// selectClip/jogSeconds/jogMinuteSnap above.
func (b Binding) IsKeyDelta() bool { return b.effectiveType() == "keyDelta" }

// IsScrollDelta returns true if this is a knob-rotation "flagged CGEvent
// scroll" binding (2026-08-13) -- each firm detent posts a synthetic
// scroll-wheel event carrying Flags' parsed modifier bitmask DIRECTLY ON
// THE EVENT via mouse.Scroll, gated on Resolve being frontmost ONLY (the
// SAME gate "key"/"keyDelta" bindings use -- see engine.go's
// dispatchScrollDelta -- deliberately NOT gate.Allow's additional
// Color-page requirement, since this is an Edit-page action).
//
// First use: Shift+D1 -> Alt+scroll -> Resolve's own NATIVE timeline zoom,
// replacing keyDelta's earlier "Cmd+="/"Cmd+-" keystroke guess on the same
// control (config/bindings.json's edit-shift.D1) after live testing showed
// "Cmd+-" zoomed out but "Cmd+=" did not zoom in -- rather than keep
// guessing at Resolve's own shortcut, this posts a scroll event that already
// carries the Alt flag, so Resolve sees a genuine Alt+scroll and zooms via
// its own built-in gesture handling. keyDelta itself is UNCHANGED and still
// available for other controls -- this is a config choice on D1, not a type
// removal (see keyDelta's own doc comment section in engine.go).
func (b Binding) IsScrollDelta() bool { return b.effectiveType() == "scrollDelta" }

// IsQuickExport returns true if this is the Quick Export button binding
// (folder-picker + render, dispatched asynchronously).
func (b Binding) IsQuickExport() bool { return b.effectiveType() == "quickexport" }

// IsSelectClip returns true if this is the D1 knob's Edit-page clip-select
// rotation binding — cycles which occupied video track's clip the
// property/reset dials target, via the sidecar's clips_at_playhead op.
//
// D1 itself is bound to "jogMinuteSnap" (see IsJogMinuteSnap below), not
// "selectClip", in the shipped config/bindings.json as of the D1 whole-
// minute-snap feature — it was "jogSeconds" (see IsJogSeconds above)
// immediately before that, and unbound entirely before THAT, freed by the
// direction-button clip-selection feature (Up/Down/Left/Right/C5, see
// IsSelectClipVert/IsSelectClipHoriz/IsSelectClipClear below). Either way
// this type/predicate is never actually reached via the real config file:
// its dispatch machinery (engine.go's dispatchSelectClip/nextTrackIndex/
// selectClipDetent/dispatchSelectClipRelease) is kept, unreachable in
// production but still directly unit-tested, rather than deleted — see
// this repo's backend-developer memory for the "Free D1" build note.
func (b Binding) IsSelectClip() bool { return b.effectiveType() == "selectClip" }

// IsSelectClipVert returns true if this is the Up/Down button's
// direction-button vertical clip-selection binding (Edit page) — steps
// the selection to the next occupied video track above/below the
// current selection at the playhead, via the sidecar's
// select_clip_vertical op. Direction ("Up" vs "Down") is derived from
// the control name at dispatch time, not stored on the binding.
func (b Binding) IsSelectClipVert() bool { return b.effectiveType() == "selectClipVert" }

// IsSelectClipHoriz returns true if this is the Left/Right button's
// direction-button horizontal clip-selection binding (Edit page only —
// Left/Right are hard-wired to color-page node prev/next navigation on
// every other page; only the "edit" bank's binding is ever consulted,
// and only while page=="edit") — selects the previous/next clip and
// moves the playhead onto it, via the sidecar's select_clip_horizontal
// op. Direction ("Left" vs "Right") is derived from the control name at
// dispatch time, not stored on the binding.
func (b Binding) IsSelectClipHoriz() bool { return b.effectiveType() == "selectClipHoriz" }

// IsSelectClipClear returns true if this is the C5 button's "unselect
// the direction-button clip selection" binding (Edit page) — restores
// the tinted clip's original color via the sidecar's existing
// clear_highlight op and clears the selection state.
func (b Binding) IsSelectClipClear() bool { return b.effectiveType() == "selectClipClear" }

// IsAddMarker returns true if this is the C1 button's Edit-page
// add-marker-at-playhead binding, dispatched via the sidecar's add_marker
// op.
func (b Binding) IsAddMarker() bool { return b.effectiveType() == "addMarker" }

// IsMarkerColor returns true if this is a P1-8btn's Edit-page "set the C1
// marker-mode active marker's color" binding, dispatched via the
// sidecar's set_marker_color op (only while marker mode is active).
func (b Binding) IsMarkerColor() bool { return b.effectiveType() == "markerColor" }

// IsMarkerAtPlayhead returns true if this is a P1-8btn's Feature 1
// Edit-page "recolour the marker under the playhead, else add a new one"
// binding -- see engine.go's dispatchMarkerAtPlayhead. Supersedes
// "markerColor" on P1-8btn (that type stays in the codebase, still
// referenced by dispatchSetMarkerColor for the C1 marker-mode feature;
// only the P-button bindings themselves move to this new type).
func (b Binding) IsMarkerAtPlayhead() bool { return b.effectiveType() == "markerAtPlayhead" }

// IsModifierHold returns true if this is a held-modifier CHORD OVERLAY
// binding (Part B, 2026-08-12 REV 2 design review) — press sets the
// engine's held-overlay state to Modifier (a bank-name suffix, e.g.
// "cmd"), release clears it — changing which BANK a later control's press
// resolves against (see engine.go's resolveOverlayBinding and, for the
// eight direct-intercept controls, editBankBinding). Unlike the reverted
// "modifier" type, this performs NO direct OS side effect at press/release
// time at all: any real keystroke a chord ultimately sends goes through
// the ordinary "key" binding dispatch path (dispatchNoteOn's IsKey()
// case), self-contained with its own modifier flags in a single down+up
// CGEvent, never a separately-held key some other later event could
// inherit. A real, OS-level held-modifier "passthrough" capability was
// built on top of this (2026-08-13) and REMOVED the same day, proven not
// to affect real hardware input — see this Binding's Type doc comment.
func (b Binding) IsModifierHold() bool { return b.effectiveType() == "modifierHold" }

// IsToggleHud returns true if this is the C6 button's "toggle the native
// always-on-top HUD status monitor" binding. Pure UI action dispatched via
// an injected Go callback (internal/hud, wired through engine.Engine) — no
// CGEvent, no arming, no Resolve-frontmost/page gate.
func (b Binding) IsToggleHud() bool { return b.effectiveType() == "toggleHud" }

// IsFinderTag returns true if this is a P1-8btn Finder-profile colour-tag
// binding -- applied to the current Finder selection when a Finder window is
// frontmost, see engine.go's dispatchFinderTag.
//
// Identified by Type alone: Tag == "" is the meaningful "clear colour tags"
// value bound to P8btn, not an unset field.
func (b Binding) IsFinderTag() bool { return b.effectiveType() == "finderTag" }

// IsToggleHudAndColor returns true if this binding toggles the HUD and, on the
// press that brings it BACK, repaints the timeline by render state — one
// button for "show me the state again", in both places the state is shown.
//
// A distinct type rather than a flag on toggleHud, because noteBankGuard's
// whitelist is a list of TYPES: a boolean rider would be invisible to it, and
// the binding would fall through to col exactly as colorByRenderState did.
func (b Binding) IsToggleHudAndColor() bool {
	return b.effectiveType() == "toggleHudAndColor"
}

// defaultHueOffsetsDeg is the default angular offset (degrees, θ for RGBVector)
// for each of the 8 balance sliders: Red, Orange, Yellow, Green, Cyan, Blue, Purple, Magenta.
// Arc-compressed per colorist review: warm arc is wide; Blue/Purple/Magenta are bunched.
var defaultHueOffsetsDeg = [8]float64{0, 45, 105, 165, 225, 270, 300, 330}

// defaultCoalesceWindowMs is the default accumulation window for balance
// events before flushing a single scrub sequence per channel.
const defaultCoalesceWindowMs = 120

// defaultFineFactor is the sens multiplier applied while fine mode is held.
const defaultFineFactor = 0.5

// defaultStartupReset is the default startup-reset policy.
const defaultStartupReset = "never"

// defaultDodeeMostGuard enables the DODEE/MOST window-title guard by default.
const defaultDodeeMostGuard = true

// defaultHUDWithoutResolve: the HUD may show without Resolve running.
const defaultHUDWithoutResolve = true

// defaultHUDEnabled: the HUD is switched on (the user can switch it off).
const defaultHUDEnabled = true

// defaultPxPerUnit is the default number of screen pixels per accumulated
// balance step for the RGB-scrubber.
const defaultPxPerUnit = 10.0

// defaultMaxScrubPx is the default per-tick scrub pixel clamp (absolute value).
const defaultMaxScrubPx = 80.0

// defaultMinScrubPx is the minimum delta (absolute) that triggers a scrub.
// Channels whose |delta_px| is below this threshold are skipped for this flush.
const defaultMinScrubPx = 1.0

// defaultHighlightColor is the Resolve clip-color palette name used to tint
// the D1-selected clip when _config.highlightColor is absent.
const defaultHighlightColor = "Teal"

// defaultReleaseColor is the Resolve clip-color palette name applied to
// EVERY occupied video clip under the playhead by D1's knob-PRESS
// "release/normalize" action when _config.releaseColor is absent.
const defaultReleaseColor = "Blue"

// defaultMarkerResizeFramesPerStep is the fallback frame delta applied per
// coalesced ControlDial step while C1 marker mode is active, when
// _config.markerResizeFramesPerStep is absent. 1 frame/step is the
// finest-grained resize the ControlDial can express.
const defaultMarkerResizeFramesPerStep = 1

// defaultKeyDebounceMs is the fallback per-control note_on PRESS debounce
// window (ms) when _config.keyDebounceMs is absent -- see engine.go's
// isDuplicateNoteOn. Tuned against a live-observed Loupedeck+ duplicate
// burst: one physical press producing 4-5 identical note_on messages within
// ~4ms. 50ms comfortably covers that burst while staying far below the
// ~100ms+ gap between genuinely separate human presses.
const defaultKeyDebounceMs = 50

// defaultSelectionGraceMs is the fallback grace window (ms) when
// _config.selectionGraceMs is absent -- see engine.go's direction-button
// selection auto-reset. A horizontal select moves the playhead via
// SetCurrentTimecode; the very next status tick can race ahead of Resolve
// actually committing that write and read the OLD playhead position. The
// auto-reset suppresses clearing a selection until EITHER an observed
// playhead falls inside the selected clip's range (confirming arrival) OR
// this grace window elapses, whichever comes first.
const defaultSelectionGraceMs = 500

// TopLevel holds optional top-level configuration parsed from bindings.json
// under the "_config" key.
// ResetTarget describes how to reset one named target — the table behind the
// universal Col reset.
//
// Entries reuse the existing action vocabulary rather than inventing new
// primitives, so adding a target is config-only; only a new KIND of target
// needs code. Type is one of:
//
//	"click"          — mouse-click a calibration point (Point). Color page only.
//	"resetParam"     — sidecar reset_param (Param, Ch). Color page only.
//	"resetProperty"  — sidecar reset_property for each of Properties. Edit page only.
//
// The page restrictions are enforced by the engine, not here — see
// targetValidForPage.
type ResetTarget struct {
	Type       string   `json:"type"`
	Point      string   `json:"point,omitempty"`
	Param      string   `json:"param,omitempty"`
	Ch         string   `json:"ch,omitempty"`
	Properties []string `json:"properties,omitempty"`
}

// HUDPlacement configures where the HUD panel draws.
//
// Absent, or with an empty Display / zero HeightPx, the HUD keeps its existing
// behaviour (virtual screen if present, else the mainScreen corner card).
// A configured display that is not currently connected is likewise not an
// error -- it simply falls back.
type HUDPlacement struct {
	// Display is matched as a case-insensitive SUBSTRING against each
	// screen's localizedName, so "PB287Q" matches "ASUS PB287Q".
	Display string `json:"display,omitempty"`
	// Edge is "bottom" (default) or "top".
	Edge string `json:"edge,omitempty"`
	// HeightPx is the strip's fixed height in points.
	HeightPx int `json:"heightPx,omitempty"`
}

type TopLevel struct {
	// HUDWithoutResolve lets the HUD show while DaVinci Resolve is not
	// running (the app's own HUD gate otherwise hides it with Resolve).
	// Pointer so an explicit false is distinguishable from unset; default
	// true (defaultHUDWithoutResolve).
	HUDWithoutResolve *bool `json:"hudWithoutResolve"`

	// HUDEnabled is the general HUD on/off, kept across launches. Off hides
	// the HUD whatever else is true (Resolve running or not); on lets the
	// other rules (placement, HUDWithoutResolve) decide. Pointer so an
	// explicit false is distinguishable from unset; default true.
	HUDEnabled *bool `json:"hudEnabled"`

	// HUD configures the HUD panel's placement (the lower-third strip).
	HUD HUDPlacement `json:"hud"`

	// ResetTargets maps a target name to how it is reset. Absent means an
	// empty (not nil-dereferencing) map — a lookup simply misses.
	ResetTargets map[string]ResetTarget `json:"resetTargets"`

	// HueOffsetsDeg overrides the 8 hue angles (degrees) fed into RGBVector.
	// Must have exactly 8 elements when present; otherwise ignored.
	HueOffsetsDeg [8]float64 `json:"hueOffsetsDeg"`

	// CoalesceWindowMs is the accumulation window (ms) for balance events.
	// Within each window all incoming ticks are summed per channel; at the end
	// of the window one scrub is fired per channel whose delta_px ≥ minScrubPx.
	// Zero or absent means use defaultCoalesceWindowMs (120).
	CoalesceWindowMs int `json:"coalesceWindowMs"`

	// WheelSensMultipliers is a per-wheel scaling factor applied on top of the
	// binding's own Sens value. Keys are "lift", "gamma", "gain", "offset".
	// Zero or absent per-wheel key uses 1.0 (no extra scaling).
	// Default: lift=1.0, gamma=1.0, gain=0.7, offset=0.5.
	WheelSensMultipliers map[string]float64 `json:"wheelSensMultipliers"`

	// FineFactor is the sens multiplier applied while the fine-mode button is held.
	// Zero or absent means use defaultFineFactor (0.5).
	FineFactor float64 `json:"fineFactor"`

	// StartupReset controls whether all 4 wheels are re-synced on launch.
	//   "never"  – never reset on launch (default).
	//   "always" – always reset on launch.
	StartupReset string `json:"startupReset"`

	// DodeeMostGuard enables the DODEE/MOST window-title guard. When true,
	// ALL balance/wheel events are refused if the frontmost Resolve window title
	// matches /(DODEE|MOST\s*PRODUCTION)/i.
	// Default true.
	DodeeMostGuard *bool `json:"dodeeMostGuard"`

	// PxPerUnit is the pixel-per-step multiplier for the RGB-scrubber.
	// delta_px = step × sens × wheelMult × component × PxPerUnit.
	// Zero or absent means use defaultPxPerUnit (10.0).
	PxPerUnit float64 `json:"pxPerUnit"`

	// MaxScrubPx is the per-channel scrub pixel clamp (absolute value per flush).
	// Zero or absent means use defaultMaxScrubPx (80.0).
	MaxScrubPx float64 `json:"maxScrubPx"`

	// MinScrubPx is the minimum |delta_px| that triggers a scrub.
	// Channels below this threshold are skipped for the flush.
	// Zero or absent means use defaultMinScrubPx (1.0).
	MinScrubPx float64 `json:"minScrubPx"`

	// HighlightEnabled controls the D1 active-clip highlight feature:
	// tinting the D1-selected clip's color in Resolve so the user can SEE
	// which stacked layer is active (Resolve has no clip-selection-
	// highlight API). Pointer so an explicit "false" is distinguishable
	// from "unset" -- default is true (the user wants this ON by
	// default), matching the DodeeMostGuard *bool precedent above. See
	// engine.go's dispatchHighlightClip.
	HighlightEnabled *bool `json:"highlightEnabled"`

	// HighlightColor is the Resolve clip-color palette name applied by
	// the D1 active-clip highlight feature. Must be one of Resolve's
	// fixed clip-color palette names (Orange, Apricot, Yellow, Lime,
	// Olive, Green, Teal, Navy, Blue, Purple, Violet, Pink, Tan, Beige,
	// Brown, Chocolate) -- not validated here (the sidecar's
	// SetClipColor call is the source of truth). Zero/absent means use
	// defaultHighlightColor ("Teal").
	HighlightColor string `json:"highlightColor,omitempty"`

	// ReleaseColor is the Resolve clip-color palette name applied to EVERY
	// occupied video clip under the playhead by D1's knob-PRESS "release /
	// normalize" action -- a uniform "everything back to default" gesture,
	// distinct from HighlightColor (which tints only the ONE D1-selected
	// clip while turning). Same fixed Resolve clip-color palette-name
	// constraint as HighlightColor (not validated here). Zero/absent means
	// use defaultReleaseColor ("Blue"). See engine.go's
	// dispatchSelectClipRelease.
	ReleaseColor string `json:"releaseColor,omitempty"`

	// MarkerResizeFramesPerStep tunes the C1 "marker mode" ControlDial
	// resize action: the active marker's duration changes by
	// coalescedStep * MarkerResizeFramesPerStep frames per drain tick
	// (see engine.go's dispatchMarkerResize) -- extending on a
	// clockwise/positive step, shrinking on a counter-clockwise/negative
	// one. Zero/absent means use defaultMarkerResizeFramesPerStep (1).
	MarkerResizeFramesPerStep int `json:"markerResizeFramesPerStep,omitempty"`

	// KeyDebounceMs is the per-control note_on PRESS debounce window (ms)
	// -- see engine.go's isDuplicateNoteOn. Zero/absent means use
	// defaultKeyDebounceMs (50).
	KeyDebounceMs int `json:"keyDebounceMs,omitempty"`

	// SelectionGraceMs is the direction-button selection auto-reset's
	// race-suppression grace window (ms) -- see engine.go's drain-loop
	// selection auto-reset and defaultSelectionGraceMs's doc comment.
	// Zero/absent means use defaultSelectionGraceMs (500).
	SelectionGraceMs int `json:"selectionGraceMs,omitempty"`

	// FinderTagConfirmThreshold is the selection size above which a Finder
	// colour-tag press requires a SECOND press of the same button within
	// finderTagConfirmWindow to actually run -- see engine.go's
	// dispatchFinderTag. Zero/absent means use
	// defaultFinderTagConfirmThreshold (10).
	//
	// This exists because one EP16 folder holds 815 files: Cmd+A plus one
	// stray press is ~1630 extended-attribute writes on a live Google Drive
	// mirror (the same churn shape as the 2026-08-30 Drive livelock) and 815
	// irreversible approval decisions. A negative value disables the guard.
	FinderTagConfirmThreshold int `json:"finderTagConfirmThreshold,omitempty"`
}

// Config holds the resolved device map and bindings.
type Config struct {
	// ccToControl maps CC number -> control name (knobs + sliders)
	ccToControl map[int]string
	// noteToControl maps note number -> control name (knobs via shared N, sliders via M, buttons)
	noteToControl map[int]string
	bindings      map[string]map[string]Binding // bank -> control -> binding

	// TopLevel holds the parsed top-level config options (hue offsets, direction, balance clamp).
	TopLevel TopLevel
}

// deviceMap mirrors the JSON schema in config/device-map.json.
// Knobs have shared CC==note==N. Sliders have CC==note==M. Buttons are note-only.
type deviceMap struct {
	Knobs   map[string]string `json:"knobs"`
	Sliders map[string]string `json:"sliders"`
	Buttons map[string]string `json:"buttons"`
}

// rawBindingsFile is used internally to parse the bindings.json structure,
// which may include an optional "_config" top-level key alongside bank maps.
type rawBindingsFile struct {
	Config   *TopLevel                     `json:"_config"`
	Bindings map[string]map[string]Binding `json:"-"` // populated manually below
}

// decodeTopLevel reads the optional "_config" block of a bindings file.
func decodeTopLevel(rawMap map[string]json.RawMessage, topLevel *TopLevel) error {
	if cfgRaw, ok := rawMap["_config"]; ok {
		return json.Unmarshal(cfgRaw, topLevel)
	}
	return nil
}

// decodeBanks reads every bank (all keys but "_config") into typed bindings,
// exactly as the engine's config load does.
func decodeBanks(rawMap map[string]json.RawMessage) (map[string]map[string]Binding, error) {
	bindings := map[string]map[string]Binding{}
	for k, raw := range rawMap {
		if k == "_config" {
			continue
		}
		var controls map[string]Binding
		if err := json.Unmarshal(raw, &controls); err != nil {
			return nil, fmt.Errorf("bank %q: %w", k, err)
		}
		bindings[k] = controls
	}
	return bindings, nil
}

// CheckBindings runs the typed decode Load does on a bindings.json document
// (the "_config" block and every bank into config.Binding) without needing a
// device map, so the preset store can refuse, before writing, an edit the
// engine's own load would reject.
func CheckBindings(data []byte) error {
	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(data, &rawMap); err != nil {
		return err
	}
	var top TopLevel
	if err := decodeTopLevel(rawMap, &top); err != nil {
		return fmt.Errorf("_config: %w", err)
	}
	if err := ValidateTopLevel(top); err != nil {
		return fmt.Errorf("_config: %w", err)
	}
	_, err := decodeBanks(rawMap)
	return err
}

// Load reads device-map.json and bindings.json and returns a Config.
func Load(devicePath, bindingsPath string) (*Config, error) {
	var dm deviceMap
	if err := readJSON(devicePath, &dm); err != nil {
		return nil, err
	}

	// Parse bindings.json: it is a map where all keys are banks EXCEPT the
	// optional "_config" key. We unmarshal into a raw map, extract "_config",
	// then re-interpret the remaining keys as bank→control→Binding.
	var rawMap map[string]json.RawMessage
	if err := readRawJSON(bindingsPath, &rawMap); err != nil {
		return nil, err
	}

	var topLevel TopLevel
	if err := decodeTopLevel(rawMap, &topLevel); err != nil {
		return nil, err
	}
	// Whatever reached this point, the engine runs with values inside the
	// documented ranges (see TopLevelRanges). Clamped BEFORE the defaults so a
	// negative debounce becomes 0 = "the default", never "no protection".
	clampTopLevel(&topLevel)

	// Apply defaults for TopLevel fields.
	if topLevel.HueOffsetsDeg == ([8]float64{}) {
		topLevel.HueOffsetsDeg = defaultHueOffsetsDeg
	}
	if topLevel.CoalesceWindowMs == 0 {
		topLevel.CoalesceWindowMs = defaultCoalesceWindowMs
	}
	if topLevel.FineFactor == 0 {
		topLevel.FineFactor = defaultFineFactor
	}
	if topLevel.StartupReset == "" {
		topLevel.StartupReset = defaultStartupReset
	}
	if topLevel.DodeeMostGuard == nil {
		v := defaultDodeeMostGuard
		topLevel.DodeeMostGuard = &v
	}
	if topLevel.HUDEnabled == nil {
		v := defaultHUDEnabled
		topLevel.HUDEnabled = &v
	}
	if topLevel.HUDWithoutResolve == nil {
		v := defaultHUDWithoutResolve
		topLevel.HUDWithoutResolve = &v
	}
	if topLevel.PxPerUnit == 0 {
		topLevel.PxPerUnit = defaultPxPerUnit
	}
	if topLevel.MaxScrubPx == 0 {
		topLevel.MaxScrubPx = defaultMaxScrubPx
	}
	if topLevel.MinScrubPx == 0 {
		topLevel.MinScrubPx = defaultMinScrubPx
	}
	if topLevel.HighlightEnabled == nil {
		v := true
		topLevel.HighlightEnabled = &v
	}
	if topLevel.HighlightColor == "" {
		topLevel.HighlightColor = defaultHighlightColor
	}
	if topLevel.ReleaseColor == "" {
		topLevel.ReleaseColor = defaultReleaseColor
	}
	if topLevel.MarkerResizeFramesPerStep == 0 {
		topLevel.MarkerResizeFramesPerStep = defaultMarkerResizeFramesPerStep
	}
	if topLevel.KeyDebounceMs == 0 {
		topLevel.KeyDebounceMs = defaultKeyDebounceMs
	}
	if topLevel.SelectionGraceMs == 0 {
		topLevel.SelectionGraceMs = defaultSelectionGraceMs
	}
	// Apply per-wheel sens multiplier defaults: lift=1.0, gamma=1.0, gain=0.7, offset=0.5.
	if topLevel.WheelSensMultipliers == nil {
		topLevel.WheelSensMultipliers = map[string]float64{}
	}
	defaultMultipliers := map[string]float64{
		"lift": 1.0, "gamma": 1.0, "gain": 0.7, "offset": 0.5,
	}
	for w, def := range defaultMultipliers {
		if _, ok := topLevel.WheelSensMultipliers[w]; !ok {
			topLevel.WheelSensMultipliers[w] = def
		}
	}

	bindings, err := decodeBanks(rawMap)
	if err != nil {
		return nil, err
	}

	cc2c := map[int]string{}
	n2c := map[int]string{}

	// Knobs: CC == note == N
	for k, v := range dm.Knobs {
		if i, err := strconv.Atoi(k); err == nil {
			cc2c[i] = v
			n2c[i] = v
		}
	}
	// Sliders: CC == note == M
	for k, v := range dm.Sliders {
		if i, err := strconv.Atoi(k); err == nil {
			cc2c[i] = v
			n2c[i] = v
		}
	}
	// Buttons: note-only
	for k, v := range dm.Buttons {
		if i, err := strconv.Atoi(k); err == nil {
			n2c[i] = v
		}
	}

	return &Config{
		ccToControl:   cc2c,
		noteToControl: n2c,
		bindings:      bindings,
		TopLevel:      topLevel,
	}, nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func readRawJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// ControlForCC resolves a CC number to a control name (knobs + sliders).
func (c *Config) ControlForCC(cc int) string { return c.ccToControl[cc] }

// ControlForNote resolves a note number to a control name (knobs, sliders, and buttons).
func (c *Config) ControlForNote(note int) string { return c.noteToControl[note] }

// Binding returns the binding for a (bank, control) pair.
// Works for both v1 CDL bindings and v2 wheel bindings.
func (c *Config) Binding(bank, control string) (Binding, bool) {
	if m, ok := c.bindings[bank]; ok {
		b, ok := m[control]
		return b, ok
	}
	return Binding{}, false
}

// AllBindings returns the full bank→control→Binding map for iteration.
// Callers must not mutate the returned map.
func (c *Config) AllBindings() map[string]map[string]Binding {
	return c.bindings
}
