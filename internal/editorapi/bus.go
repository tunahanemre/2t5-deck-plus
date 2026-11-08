package editorapi

import (
	"sync"
	"time"
)

// Out is the human-readable result of an input event, in the action's own
// unit (frames, lines, a key name ...).
type Out struct {
	Text  string  `json:"text"`
	Value float64 `json:"value"`
	Unit  string  `json:"unit"`
}

// Event is one deck input as the live test view shows it. The engine observer
// (or the simulator) publishes it; /api/events serves it.
//
// T is Unix milliseconds. Kind is "turn", "press" or "release". Source is
// "app" or "global" (which preset supplied the binding), Bank the runtime
// bank, Action the registry action id.
type Event struct {
	Seq         int64   `json:"seq"`
	T           int64   `json:"t"`
	Type        string  `json:"type"` // always "input"
	Control     string  `json:"control"`
	Kind        string  `json:"kind"`
	RawTicks    int     `json:"rawTicks"`
	TicksPerSec float64 `json:"ticksPerSec"`
	Accel       float64 `json:"accel"`
	Out         Out     `json:"out"`
	Fine        bool    `json:"fine"`
	PresetID    string  `json:"presetId"`
	Source      string  `json:"source"`
	Bank        string  `json:"bank"`
	Action      string  `json:"action"`
	// LatencyMs is the executor's own time from receiving the event to issuing the
	// post (not the MIDI read, not the target app); absent when nothing was posted.
	LatencyMs float64 `json:"latencyMs,omitempty"`
	// Mode / Custom ride on Type "deck-state" events only: the deck's slider mode ("hue", "sat", "lum")
	// and Custom Mode toggle the moment the engine changed them, so the editor repaints its LEDs without
	// waiting for a poll.
	Mode   string `json:"mode,omitempty"`
	Custom *bool  `json:"custom,omitempty"`
	// Layer rides on "deck-state" events too: the engine's active layer (1-3), pushed the moment it changes.
	Layer int `json:"layer,omitempty"`
}

// RingSize is the number of events the Bus keeps.
const RingSize = 2000

// Bus is a ring buffer of the last RingSize events. Safe for concurrent use;
// Publish never blocks.
type Bus struct {
	mu   sync.Mutex
	ring []Event
	next int64         // seq of the next event
	chg  chan struct{} // closed (and replaced) on every Publish: what a long poll waits on
}

// NewBus returns an empty bus (first event gets seq 1).
func NewBus() *Bus { return &Bus{ring: make([]Event, 0, RingSize), next: 1} }

// Publish stamps Seq (and Type "input" when empty) and stores the event.
// A zero T is the caller's problem: the engine sets real time.
func (b *Bus) Publish(e Event) Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	e.Seq = b.next
	b.next++
	if e.Type == "" {
		e.Type = "input"
	}
	if len(b.ring) < RingSize {
		b.ring = append(b.ring, e)
	} else {
		copy(b.ring, b.ring[1:])
		b.ring[RingSize-1] = e
	}
	if b.chg != nil {
		close(b.chg)
	}
	b.chg = make(chan struct{})
	return e
}

// Wait is Since that, when nothing newer than since exists yet, blocks until an
// event is published, the timeout passes or done is closed (a long poll: the
// editor hears about a press the moment the engine publishes it).
func (b *Bus) Wait(since int64, timeout time.Duration, done <-chan struct{}) (int64, []Event) {
	b.mu.Lock()
	if b.next-1 > since {
		b.mu.Unlock()
		return b.Since(since)
	}
	if b.chg == nil {
		b.chg = make(chan struct{})
	}
	ch := b.chg
	b.mu.Unlock()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-ch:
	case <-t.C:
	case <-done:
	}
	return b.Since(since)
}

// Since returns the events with Seq > since and the cursor to pass next time
// (the newest Seq published so far, or since when nothing was published).
// Events that fell out of the ring are simply absent.
func (b *Bus) Since(since int64) (next int64, events []Event) {
	b.mu.Lock()
	defer b.mu.Unlock()
	next = b.next - 1
	if next < since {
		next = since
	}
	events = []Event{}
	for _, e := range b.ring {
		if e.Seq > since {
			events = append(events, e)
		}
	}
	return next, events
}
