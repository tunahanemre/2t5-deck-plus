package leds

import (
	"fmt"
	"sync"

	"gitlab.com/gomidi/midi/v2"
	_ "gitlab.com/gomidi/midi/v2/drivers/rtmididrv"
)

// MIDISender posts note-ons to the Loupedeck's MIDI output port. The port is
// opened on the first NoteOn, never at construction, so building one touches no
// hardware. NOT used by any test.
type MIDISender struct {
	mu   sync.Mutex
	name string
	out  func(midi.Message) error
}

// NewMIDISender targets the output port whose name contains name ("Loupedeck").
func NewMIDISender(name string) *MIDISender { return &MIDISender{name: name} }

// NoteOn implements Sender.
func (s *MIDISender) NoteOn(note, velocity uint8) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.out == nil {
		port, err := midi.FindOutPort(s.name)
		if err != nil {
			return fmt.Errorf("no MIDI output port %q: %w", s.name, err)
		}
		send, err := midi.SendTo(port)
		if err != nil {
			return fmt.Errorf("open MIDI output %q: %w", s.name, err)
		}
		s.out = send
	}
	if velocity == 0 {
		return s.out(midi.NoteOff(0, note))
	}
	return s.out(midi.NoteOn(0, note, velocity))
}
