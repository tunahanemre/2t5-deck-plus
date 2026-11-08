package actions

import "strings"

// Pack and action definitions. Engine defaults quoted in Default fields come
// from internal/engine/engine.go (defaultWheelSens 15, defaultPropertySens 1,
// defaultMarkerDurationSeconds 1, defaultMarkerColor Blue,
// defaultSelectClipDetentThreshold 4, defaultJogSecondsSeconds 60,
// defaultLinesPerDetent 1) and internal/tc (jog tiers 4/8/13, sustain
// 1000 ms / 60 s). A Default is only shown when the key is absent; the engine
// applies the same value at dispatch time.

func f64(v float64) *float64 { return &v }

var labelField = Field{Key: "label", Label: "Label", Kind: "string", Help: "Name shown on the deck picture and the HUD."}

func numF(key, label, unit string, def, min, max, step float64, help string) Field {
	return Field{Key: key, Label: label, Unit: unit, Kind: "number", Default: def, Min: f64(min), Max: f64(max), Step: f64(step), Help: help}
}
func intF(key, label, unit string, def, min, max float64, help string) Field {
	return Field{Key: key, Label: label, Unit: unit, Kind: "int", Default: def, Min: f64(min), Max: f64(max), Step: f64(1), Help: help}
}
func strF(key, label, help string) Field {
	return Field{Key: key, Label: label, Kind: "string", Help: help}
}
func keyF(key, label, help string, required bool) Field {
	return Field{Key: key, Label: label, Kind: "key", Help: help, Required: required}
}
func enumF(key, label string, def string, help string, opts ...string) Field {
	f := Field{Key: key, Label: label, Kind: "enum", Help: help}
	if def != "" {
		f.Default = def
	}
	for _, o := range opts {
		f.Options = append(f.Options, Option{Value: o, Label: o})
	}
	return f
}

// comboF is free text with suggestions (Resolve owns the valid colour names).
func comboF(key, label, def, help string, opts ...string) Field {
	f := enumF(key, label, def, help, opts...)
	f.Kind = "combo"
	return f
}

var detentField = Field{Key: "detentThreshold", Label: "Clicks per step", Unit: "raw clicks", Kind: "int",
	Default: 4.0, Min: f64(1), Max: f64(64), Step: f64(1),
	Help: "How many raw encoder clicks make one step. Higher = less sensitive. 1 = every click counts."}

// Velocity feel (wave C). Optional on every turn action; absent = the
// action's own behaviour, byte-identical to before. Primary stays false.
// Applied to non-Resolve apps now; the Resolve engine paths are staged for the
// hardware session (docs/notes/2026-11-04-velocity-profiles.md).
var velocityProfileField = Field{Key: "velocityProfile", Label: "Velocity feel", Kind: "enum", Default: "linear",
	Options: []Option{{"linear", "Linear (as is)"}, {"precise", "Precise"}, {"standard", "Standard"}, {"fast", "Fast"}},
	Help:    "How spin speed changes the output. Linear = off (today's behaviour). Precise = finer at low speed, Standard = 1:1 for a steady turn then faster, Fast = ramps early. Applies to apps other than Resolve now; Resolve follows after the hardware session."}

var momentumField = Field{Key: "momentumMs", Label: "Momentum", Unit: "ms", Kind: "int", Default: 0.0, Min: f64(0), Max: f64(2000), Step: f64(10),
	Help: "How long the control keeps turning after you let go, as a decay time. 0 = off. Applies to apps other than Resolve now."}

func withFeel(f ...Field) []Field { return append(f, velocityProfileField, momentumField) }

// detentField1 is detentField for keyDelta / scrollDelta: with the key absent
// both the engine (keyDeltaDetent) and appdeck fire on every click (threshold
// 1), so the displayed default is 1, not the 4 of the Resolve selectClip family.
var detentField1 = func() Field {
	f := detentField
	f.Default = 1.0
	return f
}()

var resolveColours = []string{"Blue", "Cyan", "Green", "Yellow", "Red", "Pink", "Purple", "Fuchsia", "Rose", "Lavender", "Sky", "Mint", "Lemon", "Sand", "Cocoa", "Cream"}
var clipColours = []string{"Orange", "Apricot", "Yellow", "Lime", "Olive", "Green", "Teal", "Navy", "Blue", "Purple", "Violet", "Pink", "Tan", "Beige", "Brown", "Chocolate"}

// pressField is the knob press-to-reset override (config.PressAction). The
// value is ONE JSON object under "press"; Group describes its parts so the
// editor can draw a type picker plus the parameter that type needs. Absent =
// the built-in press (reset the wheel / reset the parameter).
var pressField = Field{Key: "press", Label: "On knob press", Kind: "object",
	Help: `What pressing the knob does instead of the built-in reset: nothing, a shortcut, a click on a calibrated point, select a wheel, or reset the active wheel. Leave unset for the built-in reset.`,
	Group: []Field{
		{Key: "type", Label: "Press does", Kind: "enum", Required: true, Options: []Option{
			{"none", "Nothing"}, {"key", "Send a shortcut"}, {"click", "Click a calibrated point"},
			{"selectWheel", "Select a wheel"}, {"resetActiveWheel", "Reset the active wheel (not run by the engine yet: does nothing)"}}},
		{Key: "key", Label: "Shortcut", Kind: "key", Help: `Needed when "Press does" is a shortcut, e.g. Cmd+Z.`},
		{Key: "point", Label: "Calibrated point", Kind: "string", Help: `Needed for a click: a name from calibration.json.`},
		{Key: "wheel", Label: "Wheel", Kind: "enum", Help: `Needed for select a wheel.`,
			Options: []Option{{"lift", "lift"}, {"gamma", "gamma"}, {"gain", "gain"}, {"offset", "offset"}}},
	}}

// holdField (wave H): optional on key and shell presses. Absent or 0 = fire at
// once, as before. Never seeded.
var holdField = Field{Key: "holdMs", Label: "Hold to fire (ms)", Unit: "ms", Kind: "int", Default: 0.0, Min: f64(0), Max: f64(2000), Step: f64(50),
	Help: "The press fires only if the control is held this long; a shorter tap does nothing. 0 = fire at once. Recommended for anything that closes, quits or deletes."}

// withHold adds holdField to every press action that posts a key or runs a
// command (the types the executors can delay), unless it already has one.
func withHold(ps []Pack) []Pack {
	for i := range ps {
		for j := range ps[i].Actions {
			a := &ps[i].Actions[j]
			if a.Kind != "press" || (a.Type != "key" && a.Type != "shell") {
				continue
			}
			if _, has := a.FieldByKey(holdField.Key); !has {
				a.Fields = append(append([]Field(nil), a.Fields...), holdField)
			}
		}
	}
	return ps
}

func allPacks() []Pack {
	return withHold([]Pack{genericPack(), globalPack(), resolvePack(), browserPack(), finderPack()})
}

func genericPack() Pack {
	return Pack{ID: "generic", Label: "Generic", Actions: []Action{
		{ID: "generic.key", Label: "Keyboard shortcut", Type: "key", Kind: "press",
			Fields: []Field{keyF("key", "Shortcut", `Modifiers joined by +, then the key, e.g. "Cmd+Shift+Z" or "Left".`, true)}},
		{ID: "generic.keyDelta", Label: "Turn sends two shortcuts", Type: "keyDelta", Kind: "turn",
			Help:   "Clockwise fires the first shortcut, counter-clockwise the second.",
			Fields: withFeel(keyF("keyCW", "Clockwise shortcut", "", true), keyF("keyCCW", "Counter-clockwise shortcut", "", true), detentField1)},
		{ID: "generic.scrollDelta", Label: "Turn scrolls", Type: "scrollDelta", Kind: "turn",
			Fields: withFeel(
				Field{Key: "linesPerDetent", Label: "Scroll distance", Unit: "lines per step", Kind: "int", Default: 1.0, Min: f64(-50), Max: f64(50), Step: f64(1),
					Help: "Scroll-wheel lines per step. Negative reverses the direction."},
				Field{Key: "flags", Label: "Hold modifier", Kind: "string", Help: `Modifier names joined by +, e.g. "Alt" (Alt+scroll zooms a timeline). Empty = none.`},
				detentField1)},
		{ID: "generic.shell", Label: "Run a command", Type: "shell", Kind: "press",
			Help: "Runs a program (no shell expansion). Needs engine support for the shell type.",
			Fields: []Field{
				{Key: "cmd", Label: "Program", Kind: "string", Required: true, Help: "Absolute path or a name on PATH, e.g. /usr/bin/open."},
				{Key: "args", Label: "Arguments", Kind: "stringList", Help: "One argument per entry."}}},
		{ID: "generic.layer", Label: "Switch layer", Type: "layer", Kind: "press",
			Fields: []Field{intF("layer", "Layer", "", 1, 1, 3, "The deck layer (1-3) this button selects.")}},
		{ID: "generic.modifierHold", Label: "Hold a modifier bank", Type: "modifierHold", Kind: "press",
			Help: "While held, other controls use the chord bank of this modifier.",
			Fields: []Field{{Key: "modifier", Label: "Modifier", Kind: "enum", Required: true,
				Options: []Option{{"cmd", "Cmd"}, {"alt", "Alt"}, {"shift", "Shift"}, {"ctrl", "Ctrl"}, {"fn", "Fn"}}}}},
		{ID: "generic.disarm", Label: "Panic: disarm the deck", Type: "disarm", Kind: "press",
			Help: "Pressing it disarms the deck at once: no key, click, scroll or command fires until you arm it again in the editor. It works in every app, Resolve included, and overrides anything else assigned to that control in any app or bank (the editor warns about those). Pick a control you do not need for anything else. Never assigned by default."},
		{ID: "generic.none", Label: "Disabled", Type: "none", Kind: "both", Help: "Does nothing and blocks the fallback.", Fields: nil},
		{ID: "generic.toggleHud", Label: "Toggle HUD", Type: "toggleHud", Kind: "press", Fields: nil},
	}}
}

func resolvePack() Pack {
	sensWheel := numF("sens", "Sensitivity", "px per click", 15, 0.1, 500, 0.5, "Drag pixels per accumulated encoder step.")
	maxDrag := numF("maxDrag", "Max drag per tick", "px", 0, 0, 2000, 1, "Caps one drag. 0 = the global default.")
	return Pack{ID: "resolve", Label: "DaVinci Resolve", Actions: []Action{
		{ID: "resolve.scrubFrames", Label: "Scrub timeline (frames)", Type: "jog", Kind: "turn",
			Help: "Frame-accurate playhead scrub that accelerates with spin speed and snaps to round timecodes at high speed.",
			Fields: []Field{
				numF("base", "Frames per click", "frames", 1, 0.1, 50, 0.1, "Frames moved by the slowest single click."),
				numF("gamma", "Acceleration", "power", 1.6, 0.5, 4, 0.05, "Above 1 a fast spin accelerates; higher ramps harder."),
				numF("maxDelta", "Max frames per tick", "frames", 0, 0, 100000, 1, "Caps one tick. 0 = no cap."),
				intF("jogTier5Min", "Snap to 5 s from speed", "clicks/tick", 4, 0, 1000000000, "Spin speed at which the playhead snaps to the next 5-second timecode."),
				intF("jogTier10Min", "Snap to 10 s from speed", "clicks/tick", 8, 0, 1000000000, "Spin speed for the next 10-second timecode."),
				intF("jogTier30Min", "Snap to 30 s from speed", "clicks/tick", 13, 0, 1000000000, "Spin speed for the next 30-second timecode."),
				intF("jogSustainMs", "Sustained spin after", "ms", 1000, 0, 1000000000, "Turning without a pause this long escalates the snap size."),
				intF("jogSustainSeconds", "Sustained spin snap", "seconds", 60, 0, 100000, "Snap size used once the sustained spin kicks in."),
				velocityProfileField, momentumField,
			}},
		{ID: "resolve.scrubSeconds", Label: "Jump timeline (seconds)", Type: "jogSeconds", Kind: "turn",
			Help:   "Each firm click moves the playhead a fixed number of seconds.",
			Fields: []Field{numF("secondsPerStep", "Jump size", "seconds", 60, 0.04, 36000, 1, "Seconds per step; sign follows the spin direction."), detentField, velocityProfileField, momentumField}},
		// Labs (wave C): staged wheel modes from the Speed Editor vocabulary
		// (research report 2026-11-03, item 1). Registry entries only: the
		// engine has no dispatch for these types yet, so a binding of this
		// type does nothing. Never seeded into any preset.
		{ID: "resolve.shuttle", Label: "Shuttle (speed from turn)", Type: "shuttle", Kind: "turn", Labs: true, Unimplemented: true,
			Help: "Staged, does nothing yet. The playhead runs at a speed set by how far the dial is turned from rest, up to 32x, and a software spring brings it back to a stop.",
			Fields: []Field{
				numF("maxSpeed", "Top speed", "x", 32, 1, 32, 1, "Speed multiplier at full displacement."),
				intF("deadZone", "Dead zone", "clicks", 2, 0, 20, "Displacement around rest that still counts as stopped."),
				intF("springMs", "Spring return", "ms", 150, 0, 2000, "How fast the displacement decays to rest when the dial stops."),
			}},
		{ID: "resolve.scrollTimeline", Label: "Scroll timeline (a minute per turn)", Type: "scrollTimeline", Kind: "turn", Labs: true, Unimplemented: true,
			Help: "Staged, does nothing yet. One full turn of the dial moves the timeline about one minute.",
			Fields: []Field{
				numF("secondsPerRotation", "Time per full turn", "seconds", 60, 1, 3600, 1, "Timeline seconds moved by one full rotation."),
			}},
		{ID: "resolve.snapMinute", Label: "Snap to next minute", Type: "jogMinuteSnap", Kind: "turn",
			Help:   "Each firm click lands the playhead on the next or previous whole-minute timecode.",
			Fields: []Field{detentField}},
		{ID: "resolve.selectClip", Label: "Cycle clip under playhead", Type: "selectClip", Kind: "turn", Fields: []Field{detentField}},
		{ID: "resolve.selectClipVert", Label: "Select clip up/down", Type: "selectClipVert", Kind: "press",
			Help: "Direction follows the control name (Up / Down)."},
		{ID: "resolve.selectClipHoriz", Label: "Select clip previous/next", Type: "selectClipHoriz", Kind: "press",
			Help: "Direction follows the control name (Left / Right)."},
		{ID: "resolve.selectClipClear", Label: "Clear clip selection", Type: "selectClipClear", Kind: "press"},
		{ID: "resolve.property", Label: "Clip property dial", Type: "property", Kind: "turn",
			Help: "Relative read-modify-write of a timeline item property (Pan, ZoomX, CropTop...).",
			Fields: []Field{
				strF("property", "Property", "Resolve TimelineItem property name, e.g. Pan."),
				{Key: "gang", Label: "Gang of properties", Kind: "stringList", Help: "Several properties bumped together, e.g. ZoomX and ZoomY. Overrides Property."},
				numF("sens", "Sensitivity", "units per click", 1, -1000, 1000, 0.1, "Property units added per accumulated step. Negative reverses the turn direction."),
				numF("maxDelta", "Max change per tick", "property units", 0, 0, 100000, 1, "0 = no cap."),
				{Key: "min", Label: "Lowest value", Kind: "number", Help: "Clamp for the resulting value. Leave unset for no clamp."},
				{Key: "max", Label: "Highest value", Kind: "number", Help: "Clamp for the resulting value. Leave unset for no clamp."},
			}},
		{ID: "resolve.wheel", Label: "Colour wheel drag", Type: "wheel", Kind: "both",
			Fields: []Field{strF("wheel", "Calibrated wheel", "Name from calibration.json, e.g. lift_master."), sensWheel, maxDrag, pressField}},
		{ID: "resolve.cdl", Label: "CDL parameter", Type: "cdl", Kind: "both",
			Fields: []Field{
				enumF("param", "Parameter", "", "", "offset", "slope", "power", "sat"),
				enumF("ch", "Channel", "", "", "r", "g", "b", "master"),
				sensWheel, maxDrag, pressField}},
		{ID: "resolve.balance", Label: "Colour balance toward a hue", Type: "balance", Kind: "both",
			Fields: []Field{
				intF("hue", "Hue index", "0-7", 0, 0, 7, "0 Red, 1 Orange, 2 Yellow, 3 Green, 4 Cyan, 5 Blue, 6 Purple, 7 Magenta."),
				sensWheel, maxDrag}},
		{ID: "resolve.hueCurve", Label: "Hue curve point (Hue vs Sat / Hue vs Lum)", Type: "hueCurve", Kind: "turn",
			Help: "Drags the curve point at one hue up or down on the Color page. Needs the Curves tab open on that curve and one calibrated pointer position per hue; without it the turn is refused, never clicked.",
			Fields: []Field{
				enumF("curve", "Curve", "", "Hue vs Sat or Hue vs Lum.", "sat", "lum"),
				intF("hue", "Hue index", "0-7", 0, 0, 7, "0 Red, 1 Orange, 2 Yellow, 3 Green, 4 Cyan, 5 Blue, 6 Purple, 7 Magenta."),
				sensWheel, maxDrag}},
		{ID: "resolve.click", Label: "Click a calibrated point", Type: "click", Kind: "press",
			Fields: []Field{{Key: "point", Label: "Point", Kind: "string", Required: true, Help: "Name from calibration.json."}}},
		{ID: "resolve.selectWheel", Label: "Select active wheel", Type: "selectWheel", Aliases: []string{"selectTarget"}, Kind: "press",
			Fields: []Field{enumF("wheel", "Wheel", "", "", "lift", "gamma", "gain", "offset"), strF("target", "Reset target", "Generic reset target name; overrides Wheel.")}},
		{ID: "resolve.resetWheel", Label: "Reset active wheel", Type: "resetActiveWheel", Aliases: []string{"resetSelected"}, Kind: "press"},
		{ID: "resolve.resetWheelCenter", Label: "Reset wheel by centre", Type: "resetActiveWheelByCenter", Kind: "press"},
		{ID: "resolve.fineMode", Label: "Fine mode while held", Type: "fineModeHold", Kind: "press", Help: "Sensitivity is multiplied by the fine factor while held."},
		{ID: "resolve.resyncAll", Label: "Resync all wheels", Type: "resyncAll", Kind: "press"},
		{ID: "resolve.pmode", Label: "Slider mode (Hue/Sat/Lum)", Type: "pmode", Kind: "press",
			Fields: []Field{{Key: "pmode", Label: "Mode", Kind: "enum", Required: true, Options: []Option{{"hue", "Hue"}, {"sat", "Sat"}, {"lum", "Lum"}}}}},
		{ID: "resolve.quickExport", Label: "Quick export", Type: "quickexport", Kind: "press", Help: "Remembered-folder render of the timeline."},
		{ID: "resolve.addMarker", Label: "Add marker at playhead", Type: "addMarker", Kind: "press",
			Fields: []Field{
				numF("markerDurationSeconds", "Marker length", "seconds", 1, 0.04, 36000, 0.5, ""),
				comboF("markerColor", "Marker colour", "Blue", "", resolveColours...),
				strF("markerName", "Marker name", "Optional.")}},
		{ID: "resolve.markerColor", Label: "Set marker-mode colour", Type: "markerColor", Kind: "press",
			Fields: []Field{comboF("markerColor", "Colour", "", "", resolveColours...)}},
		{ID: "resolve.markerAtPlayhead", Label: "Marker colour at playhead", Type: "markerAtPlayhead", Kind: "press",
			Help: "Recolours the marker under the playhead, or adds one.",
			Fields: []Field{
				comboF("markerColor", "Marker colour", "Blue", "", resolveColours...),
				numF("markerDurationSeconds", "Marker length", "seconds", 1, 0.04, 36000, 0.5, ""),
				strF("markerName", "Marker name", "Optional."),
				enumF("whenPMode", "Only in slider mode", "", "Fire only while this slider mode is active. Empty = always.", "", "hue", "sat", "lum")}},
		{ID: "resolve.colorRenders", Label: "Colour clips by render state", Type: "colorByRenderState", Aliases: []string{"colorRenders"}, Kind: "press",
			Fields: []Field{comboF("rendered", "Rendered clips", "Green", "", clipColours...), comboF("neutral", "Other clips", "Yellow", "", clipColours...)}},
		{ID: "resolve.toggleHudColor", Label: "Toggle HUD and recolour", Type: "toggleHudAndColor", Kind: "press",
			Fields: []Field{comboF("rendered", "Rendered clips", "Green", "", clipColours...), comboF("neutral", "Other clips", "Yellow", "", clipColours...)}},
	}}
}

func browserPack() Pack {
	scrollField := Field{Key: "linesPerDetent", Label: "Scroll distance", Unit: "lines per step", Kind: "int", Default: 3.0, Seed: true,
		Min: f64(-50), Max: f64(50), Step: f64(1), Help: "Page lines scrolled per step. Negative reverses the direction."}
	thr1 := Field{Key: "detentThreshold", Label: "Clicks per step", Unit: "raw clicks", Kind: "int", Default: 1.0, Seed: true,
		Min: f64(1), Max: f64(64), Step: f64(1), Help: "Raw encoder clicks per step. Higher = less sensitive."}
	pair := func(id, label, cw, ccw, help string) Action {
		return Action{ID: id, Label: label, Type: "keyDelta", Kind: "turn", Help: help,
			Fixed:  map[string]any{"keyCW": cw, "keyCCW": ccw},
			Fields: withFeel(detentField1)}
	}
	press := func(id, label, key string) Action {
		return Action{ID: id, Label: label, Type: "key", Kind: "press", Fixed: map[string]any{"key": key}}
	}
	return Pack{ID: "browser", Label: "Web browser", Actions: []Action{
		{ID: "browser.scroll", Label: "Scroll page", Type: "scrollDelta", Kind: "turn",
			Fields: withFeel(scrollField, thr1)},
		pair("browser.tabs", "Switch tab", "Cmd+Shift+]", "Cmd+Shift+[", "Clockwise = next tab, counter-clockwise = previous tab."),
		pair("browser.zoom", "Zoom page", "Cmd+=", "Cmd+-", "Clockwise zooms in, counter-clockwise out."),
		pair("browser.history", "Back / forward", "Cmd+]", "Cmd+[", "Clockwise = forward, counter-clockwise = back."),
		press("browser.back", "Back", "Cmd+["),
		press("browser.forward", "Forward", "Cmd+]"),
		press("browser.reload", "Reload", "Cmd+R"),
		press("browser.newTab", "New tab", "Cmd+T"),
		press("browser.closeTab", "Close tab", "Cmd+W"),
		press("browser.reopenTab", "Reopen closed tab", "Cmd+Shift+T"),
		press("browser.zoomReset", "Reset zoom", "Cmd+0"),
	}}
}

func finderPack() Pack {
	return Pack{ID: "finder", Label: "Finder", Actions: []Action{
		{ID: "finder.tag", Label: "Colour-tag selection", Type: "finderTag", Kind: "press",
			Help:   "Applies a Finder colour tag to the selection. Empty tag clears the colour tags.",
			Fields: []Field{enumF("tag", "Tag", "", "", "", "Red", "Orange", "Yellow", "Green", "Blue", "Purple", "Gray")}},
	}}
}

// globalPack is the "Global" pack: the everyday computer actions of Logitech
// Logi Options+ (its Computer-functions presets, read from the agent's
// card_presets_osx.json), mapped onto the types the deck already runs: a
// "key" press with the macOS shortcut, a "keyDelta" pair for a knob, or a
// "shell" press where macOS has no shortcut. Always offered for every app
// (Pack.Always). Logi's own names; the category leads the help text so the
// library search finds it. Logi actions the engine cannot express (media
// transport keys, brightness, dictation, Do Not Disturb, maximize) are not
// listed: docs/notes/2026-11-04-global-pack-logi.md. Media keys (Play/Pause,
// Next, Previous, Brightness) are "key" actions with a "media:<name>" spec,
// posted by internal/keys behind the same arm guard as any key.
func globalPack() Pack {
	key := func(id, label, cat, help, spec string) Action {
		return Action{ID: "global." + id, Label: label, Type: "key", Kind: "press",
			Help: cat + ". " + help, Fixed: map[string]any{"key": spec}}
	}
	sh := func(id, label, cat, help, cmd string, args ...string) Action {
		l := make([]any, len(args))
		for i, a := range args {
			l[i] = a
		}
		return Action{ID: "global." + id, Label: label, Type: "shell", Kind: "press",
			Help: cat + ". " + help, Fixed: map[string]any{"cmd": cmd, "args": l}}
	}
	pair := func(id, label, cat, help, cw, ccw string) Action {
		return Action{ID: "global." + id, Label: label, Type: "keyDelta", Kind: "turn",
			Help: cat + ". " + help, Fixed: map[string]any{"keyCW": cw, "keyCCW": ccw}, Fields: withFeel(detentField1)}
	}
	const vol = "set v to (output volume of (get volume settings)) %s 6\nif v > 100 then set v to 100\nif v < 0 then set v to 0\nset volume output volume v"
	return Pack{ID: "global", Label: "Global", Always: true, Actions: []Action{
		// Edit (Logi "Computer functions": Copy, Cut, Paste, Undo, Redo)
		key("copy", "Copy", "Edit", "Cmd+C.", "Cmd+C"),
		key("cut", "Cut", "Edit", "Cmd+X.", "Cmd+X"),
		key("paste", "Paste", "Edit", "Cmd+V.", "Cmd+V"),
		key("undo", "Undo", "Edit", "Cmd+Z.", "Cmd+Z"),
		key("redo", "Redo", "Edit", "Shift+Cmd+Z.", "Cmd+Shift+Z"),
		key("print", "Print", "Edit", "Cmd+P.", "Cmd+P"),
		key("refresh", "Refresh", "Edit", "Cmd+R.", "Cmd+R"),
		key("zoomIn", "Zoom in", "Edit", "Cmd+=.", "Cmd+="),
		key("zoomOut", "Zoom out", "Edit", "Cmd+-.", "Cmd+-"),
		pair("undoRedo", "Undo / Redo (turn)", "Edit", "Clockwise redoes, counter-clockwise undoes.", "Cmd+Shift+Z", "Cmd+Z"),
		pair("zoom", "Zoom in / out (turn)", "Edit", "Clockwise zooms in, counter-clockwise out.", "Cmd+=", "Cmd+-"),
		// Windows and navigation
		sh("missionControl", "Mission control", "Windows", "Opens Mission Control.", "/usr/bin/open", "-a", "Mission Control"),
		key("appExpose", "App expose", "Windows", "Ctrl+Down: every window of the front app (the macOS default shortcut).", "Ctrl+Down"),
		key("switchApp", "Switch application", "Windows", "Cmd+Tab: jumps to the previous app.", "Cmd+Tab"),
		key("showDesktop", "Show/hide desktop", "Windows", "F11 (the macOS default Show Desktop shortcut).", "F11"),
		key("desktopLeft", "Desktop left", "Windows", "Ctrl+Left: moves one Space to the left.", "Ctrl+Left"),
		key("desktopRight", "Desktop right", "Windows", "Ctrl+Right: moves one Space to the right.", "Ctrl+Right"),
		pair("desktops", "Switch between desktops (turn)", "Windows", "Clockwise moves one Space right, counter-clockwise left.", "Ctrl+Right", "Ctrl+Left"),
		key("minimize", "Minimize window", "Windows", "Cmd+M.", "Cmd+M"),
		key("closeWindow", "Close window", "Windows", "Cmd+W.", "Cmd+W"),
		key("newTab", "New tab", "Windows", "Cmd+T.", "Cmd+T"),
		key("closeTab", "Close browser tab", "Windows", "Cmd+W.", "Cmd+W"),
		key("nextTab", "Next tab (right)", "Windows", "Ctrl+Tab.", "Ctrl+Tab"),
		key("prevTab", "Previous tab (left)", "Windows", "Ctrl+Shift+Tab.", "Ctrl+Shift+Tab"),
		pair("tabs", "Switch between tabs (turn)", "Windows", "Clockwise = next tab (Ctrl+Tab), counter-clockwise = previous (Ctrl+Shift+Tab).", "Ctrl+Tab", "Ctrl+Shift+Tab"),
		// System
		key("screenCapture", "Screen capture", "System", "Shift+Cmd+3: the whole screen to a file.", "Cmd+Shift+3"),
		key("screenCaptureArea", "Screen capture (selection)", "System", "Shift+Cmd+4: drag out the area to capture.", "Cmd+Shift+4"),
		key("search", "Search", "System", "Cmd+Space: Spotlight.", "Cmd+Space"),
		sh("launchpad", "Launchpad", "System", "Opens Launchpad (the Apps view on macOS 26).", "/bin/sh", "-c", "/usr/bin/open -a Launchpad 2>/dev/null || /usr/bin/open -a Apps"),
		key("lock", "Lock", "System", "Ctrl+Cmd+Q: locks the screen.", "Ctrl+Cmd+Q"),
		key("emoji", "Emojis menu", "System", "Ctrl+Cmd+Space: the emoji and symbols picker.", "Ctrl+Cmd+Space"),
		// Media
		key("playPause", "Play/Pause", "Media", "The system Play/Pause media key (whatever app owns playback).", "media:play"),
		key("nextTrack", "Next track", "Media", "The system Next media key.", "media:next"),
		key("prevTrack", "Previous track", "Media", "The system Previous media key.", "media:prev"),
		key("brightnessUp", "Brightness up", "Media", "The system Brightness Up key (built-in or Apple display).", "media:brightnessUp"),
		key("brightnessDown", "Brightness down", "Media", "The system Brightness Down key (built-in or Apple display).", "media:brightnessDown"),
		sh("volumeUp", "Volume up", "Media", "Raises the output volume one step (osascript).", "/usr/bin/osascript", "-e", fmtVol(vol, "+")),
		sh("volumeDown", "Volume down", "Media", "Lowers the output volume one step (osascript).", "/usr/bin/osascript", "-e", fmtVol(vol, "-")),
		sh("mute", "Mute/Unmute speaker", "Media", "Toggles the output mute (osascript).", "/usr/bin/osascript", "-e", "set volume output muted (not (output muted of (get volume settings)))"),
	}}
}

func fmtVol(tpl, op string) string { return strings.Replace(tpl, "%s", op, 1) }
