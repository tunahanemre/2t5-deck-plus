package config

import (
	"regexp"
	"strings"
)

// The slider mode (Hue / Sat / Lum) is a LAYER of the P1-P8 sliders and the
// P1-P8 buttons (Tunahan, 2026-11-04: "hue sat and lum must be layers for p1-p8
// sliders and buttons"). It is not a layer number: it is a second, independent
// dimension, shaped like the held-modifier overlay.
//
// Bank names follow the overlay family: "{root}-pm-{mode}", where root is the
// page (Resolve: col, edit ...), the bundle id (apps) or "default" (Global).
// A preset's own (logical) name for it is "pm-hue" / "pm-sat" / "pm-lum", the
// way a modifier bank is just "shift".
//
// Only the 16 P controls can be assigned in a mode bank. Every other control is
// untouched by the mode.

// PModes are the three slider modes, in deck order.
var PModes = []string{"hue", "sat", "lum"}

// ValidPMode reports whether m is one of hue / sat / lum.
func ValidPMode(m string) bool {
	for _, x := range PModes {
		if x == m {
			return true
		}
	}
	return false
}

var pControlRe = regexp.MustCompile(`^P[1-8](btn)?$`)

// IsPControl is true for P1..P8 (the sliders) and P1btn..P8btn (the round keys).
func IsPControl(control string) bool { return pControlRe.MatchString(control) }

// PModeBank is the logical bank name of a mode ("pm-hue").
func PModeBank(mode string) string { return "pm-" + mode }

// ModeBankOf returns the runtime bank for a root: "col" + "sat" -> "col-pm-sat".
func ModeBankOf(root, mode string) string { return root + "-pm-" + mode }

// ParseModeBank recognises a logical ("pm-lum") or runtime ("col-pm-lum",
// "com.x.App-pm-lum") mode bank and returns its mode.
func ParseModeBank(bank string) (mode string, ok bool) {
	for _, m := range PModes {
		if bank == "pm-"+m || strings.HasSuffix(bank, "-pm-"+m) {
			return m, true
		}
	}
	return "", false
}

// ModeBinding resolves a P control for the live slider mode: "{page}-pm-{mode}"
// then "col-pm-{mode}", the same two-step order as the held-modifier overlay.
// ok=false when the control is not a P control, the mode is empty/unknown, or
// the mode bank does not assign it: the caller then walks its ordinary cascade,
// so a preset with no per-mode data behaves exactly as before.
func (c *Config) ModeBinding(page, mode, control string) (Binding, string, bool) {
	if !IsPControl(control) || !ValidPMode(mode) {
		return Binding{}, "", false
	}
	for _, root := range []string{page, "col"} {
		if root == "" {
			continue
		}
		bank := ModeBankOf(root, mode)
		if b, ok := c.Binding(bank, control); ok {
			return b, bank, true
		}
	}
	return Binding{}, "", false
}
