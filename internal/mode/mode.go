// Package mode tracks the active primary mode (wheels | bars | log).
//
// The mode is set by L1/L2/L3 bindings and governs which actions are allowed:
//   - wheels and log: wheel/cdl turn-and-click actions are active.
//   - bars: wheel/cdl actions are suppressed (Bars page in Primaries palette).
package mode

// Mode is the active primary mode.
type Mode string

const (
	// Wheels is the default mode — knobs drive the Wheels palette.
	Wheels Mode = "wheels"
	// Bars suppresses wheel/cdl knob actions (Bars palette active).
	Bars Mode = "bars"
	// Log mode — knobs drive the Log Wheels palette.
	Log Mode = "log"
)

// Tracker holds the current active mode.
type Tracker struct {
	current Mode
}

// New returns a Tracker with the default mode (Wheels).
func New() *Tracker {
	return &Tracker{current: Wheels}
}

// Set changes the active mode. Unknown strings are ignored and the current mode
// is preserved.
func (t *Tracker) Set(m string) {
	switch Mode(m) {
	case Wheels, Bars, Log:
		t.current = Mode(m)
	}
}

// Current returns the active mode.
func (t *Tracker) Current() Mode {
	return t.current
}

// AllowKnobAction returns true when the current mode permits wheel/cdl knob
// turn and click-reset actions (i.e. mode is wheels or log; suppressed for bars).
func (t *Tracker) AllowKnobAction() bool {
	return t.current == Wheels || t.current == Log
}
