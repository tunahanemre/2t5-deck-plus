package engine

import (
	"strings"

	"2t5-deck-plus/internal/config"
)

// humanWheelName converts a calibration-point-style wheel key (e.g.
// "gamma_master", "lift", "offset_master") into the display name the HUD
// and Status.ActiveWheel-derived text already use ("Gamma", "Lift",
// "Offset") -- strips a trailing "_master" (the calibration key suffix
// used by wheel-type bindings' Wheel field, see config.Binding's doc
// comment), then title-cases the first byte. Mirrors app/format.go's
// titleCase (package main, not importable from here) rather than sharing
// code across packages for one one-line helper. Empty input returns empty.
func humanWheelName(wheel string) string {
	if wheel == "" {
		return ""
	}
	name := strings.TrimSuffix(wheel, "_master")
	return strings.ToUpper(name[:1]) + name[1:]
}

// propertyLabel returns the human-readable name of a "property"-type
// binding's target -- the joined Gang list (e.g. "ZoomX/ZoomY") when Gang
// is set, else the single Property name (e.g. "Pan"). Mirrors the exact
// Gang-takes-precedence-over-Property convention already used by
// dispatchNoteOn's IsProperty() case and processNonBalanceDrain's property
// branch when building the sidecar request.
func propertyLabel(b config.Binding) string {
	if len(b.Gang) > 0 {
		return strings.Join(b.Gang, "/")
	}
	return b.Property
}
