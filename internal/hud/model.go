package hud

// DotColor is the HUD's leading status-dot / LED color.
type DotColor int

const (
	// DotGrey means the engine is stopped.
	DotGrey DotColor = iota
	// DotAmber means the engine is running but the sidecar is not connected.
	DotAmber
	// DotGreen means the engine is running and the sidecar is connected.
	DotGreen
)

// String returns the JSON-facing color name ("grey"/"amber"/"green") sent
// to the web template's window.hud.render(state) as state.led.
func (d DotColor) String() string {
	switch d {
	case DotGreen:
		return "green"
	case DotAmber:
		return "amber"
	default:
		return "grey"
	}
}

// HUDKnob is one of the 4 knob-map cells in the HUD's knob row -- a static
// per-page label (e.g. "Gamma", "Scale") plus whether it's the currently
// active one (Color page: the selected wheel; Edit page: never -- each
// property knob maps continuously, none is "selected").
type HUDKnob struct {
	Label  string `json:"label"`
	Active bool   `json:"active"`
	// Value is the knob's LIVE numeric readout -- Edit page only, for the
	// Scale/Pos X/Pos Y cells (Zoom/Pan/Tilt of the target clip; see
	// app/format.go's hudKnobs). "—" when the Edit page has no resolvable
	// target clip this tick (never a stale/zero value). "" everywhere
	// else -- Color/other pages NEVER show a numeric value (the CDL grade
	// isn't readable from the scripting API), and Edit's Crop cell stays
	// label-only.
	Value string `json:"value"`
}

// HUDModel is the fully-formatted, display-ready content for one HUD
// paint -- produced by a pure formatter (see app/format.go's formatHUD)
// from a plain engine.Status snapshot. Update marshals this to JSON and
// pushes it into the web content via evaluateJavaScript(window.hud.render
// (<json>)); no formatting/business logic lives in this package or in the
// web template's JS -- every field here is already display-ready.
type HUDModel struct {
	// LED is "green" (running+connected), "amber" (running, not
	// connected), or "grey" (stopped) -- see DotColor.String().
	LED string `json:"led"`
	// Stale is true when the numbers below (Timecode, TimelineDuration,
	// ClipIn/Out/Duration, ClipTotal/Rendered, MarkerCount, LEDs, EditClip)
	// are a moment old -- carried forward from the last successful sidecar
	// read because DaVinci Resolve's own scripting API is currently busy
	// (2026-08-24 fix; mirrors engine.Status.SidecarStale -- see that
	// field's doc comment for the full story). LED stays "green" the whole
	// time (the sidecar IS connected); the web template uses Stale only to
	// add a subtle "these are current-ish, not frozen-as-live" cue (a slow
	// pulse on the status dot) -- deliberately not a second color on LED
	// itself, which already means something else (connectivity).
	Stale bool `json:"stale"`
	// Page is the title-cased Resolve page name ("Color", "Edit", ...) or
	// "—" before the engine's first status tick.
	Page string `json:"page"`
	// GateOpen mirrors Status.GateAllow.
	GateOpen bool `json:"gateOpen"`
	// Timecode is the live playhead position ("HH:MM:SS:FF"), or "" when
	// there's no live timeline -- the template renders "--:--:--:--" for
	// "".
	Timecode string `json:"timecode"`
	// TimecodeSub is the line under the timecode hero: "<project> ·
	// <timeline>" when connected, else a connection-status message (e.g.
	// "engine running · no Resolve link").
	TimecodeSub string `json:"timecodeSub"`
	// ErrorMessage is the diagnostic line shown when the engine isn't
	// fully up (stopped, or running but sidecar-disconnected). "" means
	// don't show it (fully connected).
	ErrorMessage string `json:"errorMessage"`
	// Knobs is always exactly 4 entries, one per physical knob channel —
	// see formatHUD's knob-map doc comment for the mandatory 3-way
	// per-page branch (Color/Edit/other).
	Knobs [4]HUDKnob `json:"knobs"`
	// ContextLabel/ContextValue are the ctx row's label+value pair --
	// "Wheel"/<active wheel> on Color, "Marker"/<marker status> on Edit
	// (when marker mode is active), "" / "" everywhere else (the
	// mandatory 3-way branch, same shape as v1's Row4).
	ContextLabel string `json:"contextLabel"`
	ContextValue string `json:"contextValue"`
	// Clip is the D1-selected clip label (Status.ActiveClip passed
	// through as-is -- already "" off the Edit page).
	Clip string `json:"clip"`
	// EditClip is the Edit-page target clip's NAME (see app/format.go's
	// hudEditClipName) -- the SAME clip the Scale/Pos X/Pos Y knob Values
	// above are read from (_target_item's default top-occupied clip, or
	// the direction-button-selected override). Unlike Clip (which is only
	// populated once a D1/direction-button selection is actively pinned),
	// EditClip always reflects the current target, even the unpinned
	// top-occupied default. "—" when Edit page has no target clip this
	// tick; "" off the Edit page entirely (the template hides the row).
	EditClip string `json:"editClip"`
	// Node/Bank/Fine/LastAction are the diagnostic foot row.
	Node int    `json:"node"`
	Bank string `json:"bank"`
	// Layer is the active console layer (1-3). The frontend tints the whole
	// panel by it -- L1 red, L2 green, L3 blue -- because on a console with
	// fixed engraving and no key screens a colour is pre-attentive where a
	// digit is something you have to stop and read.
	Layer int `json:"layer"`
	// Target is the active reset target, i.e. what Col will reset.
	Target string `json:"target"`
	// Project and Timeline are carried separately (not pre-joined) so the
	// band can align one left and one right and keep both legible.
	Project  string `json:"project"`
	Timeline string `json:"timeline"`
	// TimelineDuration is the timeline's total length (HH:MM:SS:FF), shown
	// beneath the running timecode so position always has a denominator.
	TimelineDuration string `json:"timelineDuration"`
	// ClipIn/ClipOut/ClipDuration are the selected clip's timeline in point,
	// out point and length -- what you need to judge how far a clip can be
	// extended before you trim it.
	ClipIn       string `json:"clipIn"`
	ClipOut      string `json:"clipOut"`
	ClipDuration string `json:"clipDuration"`
	// ClipTotal/ClipRendered/MarkerCount are the timeline census; LEDs is one
	// character per clip driving the status strip ("r" = rendered).
	ClipTotal    int    `json:"clipTotal"`
	ClipRendered int    `json:"clipRendered"`
	MarkerCount  int    `json:"markerCount"`
	LEDs         string `json:"leds"`

	// ClipHead/ClipTail are the available source handles either side of the
	// clip -- how much room there is to extend it.
	ClipHead string `json:"clipHead"`
	ClipTail string `json:"clipTail"`
	// PMode is the P1-P8 mode ("hue"/"sat"/"lum"), shown as its own
	// indicator: it changes what the entire P row does, and the console's
	// engraving cannot say which of the three is currently live.
	PMode      string `json:"pmode"`
	Fine       bool   `json:"fine"`
	LastAction string `json:"lastAction"`
}
