package main

import (
	"fmt"
	"strings"

	"2t5-deck-plus/internal/engine"
	"2t5-deck-plus/internal/hud"
)

// formatHUD derives the HUD's rich v2 display content (see
// docs/specs/2026-07-06-ld-resolve-hud-rich.md) from a point-in-time
// engine.Status snapshot. Pure and side-effect-free -- fully unit-testable
// without CGO or a real window (see format_test.go). Called only from
// runStatusTicker, gated on hud.Visible() (see app.go). All string/label
// logic lives here; hud.html's window.hud.render(state) only does DOM
// binding on the model this function already fully computed.
func formatHUD(status engine.Status) hud.HUDModel {
	connected := status.Running && status.SidecarConnected
	label, value := hudContext(status)
	return hud.HUDModel{
		LED: hudDot(status).String(),
		// Stale passes Status.SidecarStale straight through -- see
		// hud.HUDModel.Stale's doc comment for what it means and why it's
		// a separate signal from LED/connected.
		Stale:            status.SidecarStale,
		Page:             hudPageBadge(status),
		GateOpen:         status.GateAllow,
		Timecode:         status.Timecode,
		TimecodeSub:      hudTimecodeSub(status, connected),
		ErrorMessage:     hudErrorMessage(status, connected),
		Knobs:            hudKnobs(status),
		ContextLabel:     label,
		ContextValue:     value,
		Clip:             status.ActiveClip,
		EditClip:         hudEditClipName(status),
		Node:             status.Node,
		Bank:             status.Bank,
		Layer:            status.Layer,
		Target:           status.Target,
		PMode:            status.PMode,
		Project:          status.Project,
		Timeline:         status.TimelineName,
		TimelineDuration: status.TimelineDuration,
		ClipIn:           status.ClipIn,
		ClipOut:          status.ClipOut,
		ClipDuration:     status.ClipDuration,
		ClipTotal:        status.ClipTotal,
		ClipRendered:     status.ClipRendered,
		MarkerCount:      status.MarkerCount,
		LEDs:             status.LEDs,
		ClipHead:         status.ClipHead,
		ClipTail:         status.ClipTail,
		Fine:             status.FineMode,
		LastAction:       status.LastAction,
	}
}

// hudEditClipName is the Edit-page target clip's name row shown near the
// knob map (see hud.HUDModel.EditClip's doc comment) -- "" off the Edit
// page (the template hides the row entirely), "—" on the Edit page when
// this tick has no resolvable target clip (never a stale name), otherwise
// the clip's live name.
func hudEditClipName(status engine.Status) string {
	if status.Page != "edit" {
		return ""
	}
	if !status.HaveEditClip || status.EditClipName == "" {
		return "—"
	}
	return status.EditClipName
}

// hudNum formats a knob's live numeric readout (Zoom/Pan/Tilt) to 3 decimal
// places -- e.g. 1.4 -> "1.400". Three rather than two to match the precision
// Resolve itself shows in its Inspector, so a value read off the band and a
// value read off Resolve agree digit for digit. Used only by hudKnobs'
// Edit-page branch.
func hudNum(v float64) string {
	return fmt.Sprintf("%.3f", v)
}

// hudDot: green = running+connected; amber = running, not connected; grey =
// stopped. Unchanged from v1 -- the LED's 3-state logic doesn't change
// just because the rendering got richer.
func hudDot(status engine.Status) hud.DotColor {
	if !status.Running {
		return hud.DotGrey
	}
	if status.SidecarConnected {
		return hud.DotGreen
	}
	return hud.DotAmber
}

// hudPageBadge title-cases the Resolve page name ("color" -> "Color"), or
// "—" before the engine's first status tick.
func hudPageBadge(status engine.Status) string {
	if status.Page == "" {
		return "—"
	}
	return titleCase(status.Page)
}

// hudTimecodeSub is the line under the timecode hero. When the sidecar is
// actually connected it's "<project> · <timeline>" (either half gracefully
// omitted if empty, never a bare " · "); otherwise it's a short
// connection-status message, matching the approved mockup's offline state
// text exactly ("engine running · no Resolve link").
func hudTimecodeSub(status engine.Status, connected bool) string {
	if connected {
		return joinNonEmpty(" · ", status.Project, status.TimelineName)
	}
	if !status.Running {
		return "engine stopped"
	}
	return "engine running · no Resolve link"
}

// hudErrorMessage is the diagnostic line shown when the engine isn't fully
// up. "" (connected) means the web template hides the errline block
// entirely. Mirrors the approved mockup's offline-state copy for the
// plain "no Resolve link" case; a specific SidecarError (e.g. "python3 not
// found") is shown verbatim instead of the generic copy, same precedence
// v1's hudRow1 already used.
func hudErrorMessage(status engine.Status, connected bool) string {
	if connected {
		return ""
	}
	if !status.Running {
		return "Engine stopped."
	}
	if status.SidecarError != "" {
		return "Sidecar error: " + status.SidecarError
	}
	if status.SidecarState == engine.SidecarStateWaiting {
		return "Waiting for DaVinci Resolve."
	}
	return "Sidecar can't reach DaVinci Resolve.\nOpen Resolve, then press Start."
}

// hudKnobs is the static per-page knob-channel MAP -- labels never carry a
// live numeric value (the design doc's explicit "no KnobValues" decision:
// the tool can't read a clip's real CDL/graded value, so it never
// pretends to). This is the SAME mandatory 3-way page branch as
// hudContext (and v1's Row4 before it): Color highlights the active wheel
// (Status.ActiveWheel); Edit's four properties map continuously so none is
// ever "active"; every other page renders 4 blank cells so a Color-page
// highlight (or Edit-page labels) can never leak onto Cut/Fusion/
// Fairlight/Deliver/Media.
func hudKnobs(status engine.Status) [4]hud.HUDKnob {
	switch status.Page {
	case "color":
		labels := [4]string{"Lift", "Gamma", "Gain", "Offset"}
		active := strings.ToLower(status.ActiveWheel)
		var knobs [4]hud.HUDKnob
		for i, l := range labels {
			knobs[i] = hud.HUDKnob{Label: l, Active: strings.ToLower(l) == active}
		}
		return knobs
	case "edit":
		// Scale/Pos X/Pos Y get the target clip's live Zoom/Pan/Tilt
		// (2026-07-08, HUD v2 Edit-page values) -- the SAME clip
		// hudEditClipName's name row reflects, via Status.EditClip*/
		// HaveEditClip (see engine.go's queryStatus/status-tick
		// plumbing + sidecar's edit_clip_info). Crop stays label-only
		// (no CropLeft plumbed yet) -- Value left "" so the template
		// renders nothing in that cell, unchanged from before this
		// feature. "—" (never a stale/zero number) when this tick has
		// no resolvable target clip.
		labels := [4]string{"Scale", "Pos X", "Pos Y", "Crop"}
		var knobs [4]hud.HUDKnob
		for i, l := range labels {
			knobs[i] = hud.HUDKnob{Label: l}
		}
		if status.HaveEditClip {
			knobs[0].Value = hudNum(status.EditClipZoom)
			knobs[1].Value = hudNum(status.EditClipPan)
			knobs[2].Value = hudNum(status.EditClipTilt)
		} else {
			knobs[0].Value = "—"
			knobs[1].Value = "—"
			knobs[2].Value = "—"
		}
		return knobs
	default:
		return [4]hud.HUDKnob{}
	}
}

// hudContext is the MANDATORY 3-way page branch for the ctx row (label +
// value) -- see formatHUD's doc comment / hudKnobs above for why a 2-way
// `if edit {} else {}` would be wrong: Status.ActiveWheel is copied into
// Status every tick regardless of page (not page-gated by the engine,
// unlike ActiveClip/MarkerMode), so a page that is neither "edit" nor
// "color" must render blank, never a stale "Wheel: Lift". Returns ("","")
// on any such page, or on "edit" when marker mode is inactive (no C1
// press yet this session).
func hudContext(status engine.Status) (label, value string) {
	switch status.Page {
	case "edit":
		if status.MarkerMode == "" {
			return "", ""
		}
		return "Marker", status.MarkerMode
	case "color":
		return "Wheel", titleCase(status.ActiveWheel)
	default:
		return "", ""
	}
}

// joinNonEmpty joins the non-empty strings in parts with sep -- e.g.
// joinNonEmpty(" · ", "", "EP10-v0") == "EP10-v0", never " · EP10-v0".
func joinNonEmpty(sep string, parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// titleCase upper-cases the first byte of s. Status.ActiveWheel/Page are
// always plain ASCII words ("lift"/"gamma"/"gain"/"offset",
// "color"/"edit"/"cut"/...), so a byte-level upper-case of the first
// character is sufficient -- no need for unicode-aware title-casing. Empty
// input returns empty.
func titleCase(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
