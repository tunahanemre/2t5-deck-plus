package router

import (
	"sync"

	"2t5-deck-plus/internal/accel"
	"2t5-deck-plus/internal/config"
)

// Cmd is the CDL command sent to the sidecar.
type Cmd struct {
	Op    string  `json:"op"` // "delta" or "sat"
	Node  int     `json:"node"`
	Param string  `json:"param,omitempty"` // offset|slope|power
	Ch    string  `json:"ch,omitempty"`    // r|g|b|master
	Amt   float64 `json:"amt"`
}

// Cfg is the configuration interface the Router depends on.
// ControlForCC resolves a CC number (knobs + sliders).
// ControlForNote resolves a note number (knobs, sliders, and buttons).
type Cfg interface {
	ControlForCC(cc int) string
	ControlForNote(note int) string
	Binding(bank, control string) (config.Binding, bool)
}

// Router translates (bank, control, step) into CDL commands.
//
// node is guarded by mu: in the live engine, SetNode is called from the
// MIDI-callback goroutine (Left/Right hardwired nav) while Route is called
// from the ticker/drain goroutine (knob-turn CDL dispatch) — an inherited,
// pre-existing data race (same category as target.State/mode.Tracker,
// which already carry their own mutex for the identical cross-goroutine
// shape). cfg and acc are set once in New and never mutated, so they need no
// lock.
type Router struct {
	cfg Cfg
	acc accel.Curve

	mu   sync.Mutex
	node int
}

func New(cfg Cfg, acc accel.Curve, node int) *Router {
	return &Router{cfg: cfg, acc: acc, node: node}
}

func (r *Router) SetNode(n int) {
	r.mu.Lock()
	r.node = n
	r.mu.Unlock()
}

// Route maps a CC number + accumulated step to a CDL command.
// control must be pre-resolved (caller may use ControlForCC or ControlForNote).
func (r *Router) Route(bank, control string, step int) (Cmd, bool) {
	if control == "" {
		return Cmd{}, false
	}
	b, ok := r.cfg.Binding(bank, control)
	if !ok {
		return Cmd{}, false
	}
	amt := r.acc.Amount(step)
	r.mu.Lock()
	node := r.node
	r.mu.Unlock()
	if b.Param == "sat" {
		return Cmd{Op: "sat", Node: node, Amt: amt}, true
	}
	return Cmd{Op: "delta", Node: node, Param: b.Param, Ch: b.Ch, Amt: amt}, true
}
