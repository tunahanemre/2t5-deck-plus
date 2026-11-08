package actions

import (
	"fmt"
	"strings"
)

// Pure key-spec validator. It mirrors internal/keys (keyTable / modifierTable
// names) without importing that cgo package, which can arm real input. A test
// (keyspec_drift_test.go, darwin) cross-checks it against keys.ValidateSpec so
// the two cannot drift.

var specModifiers = map[string]bool{
	"cmd": true, "command": true, "ctrl": true, "control": true,
	"alt": true, "option": true, "shift": true,
}

var specKeys = func() map[string]bool {
	m := map[string]bool{}
	for _, k := range []string{
		"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q", "r", "s", "t", "u", "v", "w", "x", "y", "z",
		"0", "1", "2", "3", "4", "5", "6", "7", "8", "9",
		"left", "right", "down", "up",
		"return", "enter", "space", "tab", "escape", "delete", "forwarddelete",
		"home", "end", "pageup", "pagedown",
		"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9", "f10", "f11", "f12",
		"-", "=", "[", "]", "\\", ";", "'", ",", ".", "/", "`",
		"command", "cmd", "shift", "option", "alt", "control", "ctrl",
	} {
		m[k] = true
	}
	return m
}()

// specMedia mirrors keys.mediaKeyTable ("media:<name>" system media keys);
// keyspec_drift_test.go cross-checks it against media_darwin.go.
var specMedia = map[string]bool{"play": true, "next": true, "prev": true, "brightnessup": true, "brightnessdown": true}

// ValidateKeySpec checks a "Mod+Mod+Key" spec exactly as internal/keys.Send
// parses it. An empty spec is an error.
func ValidateKeySpec(spec string) error {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(spec)), "media:") {
		if !specMedia[strings.ToLower(strings.TrimSpace(spec))[len("media:"):]] {
			return fmt.Errorf("unknown media key %q", spec)
		}
		return nil
	}
	parts := strings.Split(spec, "+")
	if spec == "" {
		return fmt.Errorf("empty key spec")
	}
	for _, p := range parts[:len(parts)-1] {
		if !specModifiers[strings.ToLower(strings.TrimSpace(p))] {
			return fmt.Errorf("unknown modifier %q in %q", p, spec)
		}
	}
	last := parts[len(parts)-1]
	if !specKeys[strings.ToLower(strings.TrimSpace(last))] {
		return fmt.Errorf("unknown key %q in %q", last, spec)
	}
	return nil
}

// ValidateModifierFlags checks a "Mod+Mod" flags spec (empty is valid).
func ValidateModifierFlags(spec string) error {
	if spec == "" {
		return nil
	}
	for _, p := range strings.Split(spec, "+") {
		if !specModifiers[strings.ToLower(strings.TrimSpace(p))] {
			return fmt.Errorf("unknown modifier %q in %q", p, spec)
		}
	}
	return nil
}
