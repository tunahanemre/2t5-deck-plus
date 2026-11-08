// Package leds drives the four state lamps of the Loupedeck+ (Hue, Sat, Lum,
// Custom Mode) from the engine's state.
//
// STATUS: UNVERIFIED ON HARDWARE, OFF BY DEFAULT. No public source documents a
// MIDI output protocol for the Loupedeck+ lamps (the LED section of the slider-mode design notes). The only convention available is the one other MIDI controllers use,
// and darktable's Loupedeck profile relies on: a lamp is a note, lit with
// note-on velocity 127 and dark with velocity 0, on the same note number the
// button sends. That is what Plan produces. Whether the Loupedeck+ firmware
// honours it is exactly what the hardware probe (cmd/ld-led-probe notes in the
// plan) is for; until it is confirmed the driver is only installed when
// DECKPLUS_LED_OUT=1, and it never sends while the deck is disarmed.
package leds

import (
	"sync"
)

// Lamp is one state lamp and the note it is addressed by.
type Lamp struct {
	Name string
	Note uint8
}

// The notes are the buttons' own note numbers from config/device-map.json.
var (
	Hue    = Lamp{"Hue", 98}
	Sat    = Lamp{"Sat", 99}
	Lum    = Lamp{"Lum", 100}
	Custom = Lamp{"CustomMode", 117}
)

// Msg is one note-on to send (channel 1; velocity 0 = lamp dark).
type Msg struct {
	Lamp Lamp
	Vel  uint8
}

// Plan is the full lamp state for a slider mode and the Custom Mode toggle:
// exactly one of Hue / Sat / Lum lit (none for an unknown mode), Custom Mode lit
// when on.
func Plan(mode string, custom bool) []Msg {
	v := func(on bool) uint8 {
		if on {
			return 127
		}
		return 0
	}
	return []Msg{
		{Hue, v(mode == "hue")},
		{Sat, v(mode == "sat")},
		{Lum, v(mode == "lum")},
		{Custom, v(custom)},
	}
}

// Sender posts one note-on. The real one (NewMIDISender) opens the output port
// lazily on first use; tests pass a recorder.
type Sender interface {
	NoteOn(note, velocity uint8) error
}

// Driver sends only what changed and only while armed.
type Driver struct {
	mu    sync.Mutex
	send  Sender
	armed func() bool
	last  map[uint8]uint8
	// Logf receives refusals and errors; nil = silent.
	Logf func(format string, args ...any)
}

// New returns a Driver. armed must report the deck's arm state (the same switch
// the key, mouse and sidecar writers obey); a nil armed means never armed.
func New(s Sender, armed func() bool) *Driver {
	return &Driver{send: s, armed: armed, last: map[uint8]uint8{}}
}

func (d *Driver) logf(f string, a ...any) {
	if d.Logf != nil {
		d.Logf(f, a...)
	}
}

// Update brings the lamps to the given state. Disarmed: nothing is sent and
// nothing is remembered, so arming later lights them on the next change.
func (d *Driver) Update(mode string, custom bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.send == nil || d.armed == nil || !d.armed() {
		return
	}
	for _, m := range Plan(mode, custom) {
		if v, seen := d.last[m.Lamp.Note]; seen && v == m.Vel {
			continue
		}
		if err := d.send.NoteOn(m.Lamp.Note, m.Vel); err != nil {
			d.logf("[leds] %s: %v", m.Lamp.Name, err)
			return
		}
		d.last[m.Lamp.Note] = m.Vel
	}
}

// Forget drops the remembered lamp state (call after a reconnect so the next
// Update resends everything).
func (d *Driver) Forget() {
	d.mu.Lock()
	d.last = map[uint8]uint8{}
	d.mu.Unlock()
}
