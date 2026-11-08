package keys

// System media keys (Play/Pause, Next, Previous, Brightness). macOS carries
// these as NX_SYSDEFINED events (subtype 8), not as virtual key codes, so
// they cannot go through keyTable. A key spec of the form "media:<name>"
// (e.g. "media:play") is routed here by Send, which means every path that
// sends a key spec (press, keyDelta turn, hold-to-fire) reaches the SAME
// canPost() guard as an ordinary key: never posted while disarmed, paused, or
// after the panic action. This file holds only the pure encoding; the single
// posting call is in media_post_darwin.m.

import (
	"fmt"
	"strings"
)

// mediaPrefix marks a media key spec.
const mediaPrefix = "media:"

// NX_KEYTYPE_* codes from IOKit's ev_keymap.h.
const (
	nxKeytypeBrightnessUp   = 2
	nxKeytypeBrightnessDown = 3
	nxKeytypePlay           = 16
	nxKeytypeNext           = 17
	nxKeytypePrevious       = 18
)

// nxSysDefinedSubtype is the NX_SUBTYPE_AUX_CONTROL_BUTTONS subtype (8).
const nxSysDefinedSubtype = 8

// Key states carried in bits 8..15 of data1: 0xA down, 0xB up.
const (
	nxStateDown = 0xA
	nxStateUp   = 0xB
)

// mediaKeyTable maps the lower-case name after "media:" to its NX key type.
var mediaKeyTable = map[string]int{
	"play":           nxKeytypePlay,
	"next":           nxKeytypeNext,
	"prev":           nxKeytypePrevious,
	"brightnessup":   nxKeytypeBrightnessUp,
	"brightnessdown": nxKeytypeBrightnessDown,
}

// IsMediaSpec reports whether spec names a media key ("media:..." prefix).
func IsMediaSpec(spec string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(spec)), mediaPrefix)
}

// ParseMediaSpec resolves "media:<name>" to its NX key type. Modifiers are not
// allowed (a media key has none).
func ParseMediaSpec(spec string) (int, error) {
	s := strings.ToLower(strings.TrimSpace(spec))
	if !strings.HasPrefix(s, mediaPrefix) {
		return 0, fmt.Errorf("keys: %q is not a media key spec", spec)
	}
	code, ok := mediaKeyTable[strings.TrimPrefix(s, mediaPrefix)]
	if !ok {
		return 0, fmt.Errorf("keys: unknown media key %q", spec)
	}
	return code, nil
}

// MediaData1 is the NSEvent data1 word for a media key: (keyCode << 16) |
// (keyState << 8), key state 0xA for down and 0xB for up.
func MediaData1(code int, down bool) int64 {
	st := int64(nxStateUp)
	if down {
		st = nxStateDown
	}
	return int64(code)<<16 | st<<8
}

// MediaModifierFlags is the modifierFlags word NSEvent carries with the event
// (the key state again, in bits 8..15).
func MediaModifierFlags(down bool) uint64 {
	if down {
		return nxStateDown << 8
	}
	return nxStateUp << 8
}
