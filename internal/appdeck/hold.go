package appdeck

import (
	"fmt"
	"log"
	"math"

	"2t5-deck-plus/internal/actions"
)

// Hold to fire (wave H, item 4). An optional "holdMs" (0 = off, at most 2000) on
// a key or shell press: the action fires only once the control has been held
// that long. It fires AT the threshold, while the control is still down (one
// time.AfterFunc through the After seam); a tap that is released earlier cancels
// the timer, does nothing and reports "hold to fire (N ms)". Layer, modifier and
// HUD presses carry engine-side effects and stay immediate. Absent = today's
// behaviour exactly.

// MaxHoldMs is the longest hold the field accepts.
const MaxHoldMs = 2000

// holdOf reads holdMs from a raw assignment: 0 when absent, not a positive
// number, or NaN; clamped to MaxHoldMs.
func holdOf(raw map[string]any) int {
	v, ok := raw["holdMs"]
	if !ok {
		return 0
	}
	f, ok := actions.ToFloat(v)
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) || f < 1 {
		return 0
	}
	if f > MaxHoldMs {
		f = MaxHoldMs
	}
	return int(f)
}

type holdPend struct {
	gen    uint64
	ms     int
	cancel func()
	base   Result // PresetID, Source, Bank, Action for the tap report
	bundle string // the frontmost app the press began under
	epoch  uint64 // ArmEpoch at press
}

// holdFor says whether a press of this type honours holdMs.
func holdFor(typ string) bool { return typ == "key" || typ == "shell" }

func (x *Executor) armHold(in Input, ms int, typ, key, cmd string, args []string, res Result) Result {
	x.hmu.Lock()
	if x.holds == nil {
		x.holds = map[string]*holdPend{}
	}
	if old := x.holds[in.Control]; old != nil && old.cancel != nil {
		old.cancel()
	}
	x.hgen++
	p := &holdPend{gen: x.hgen, ms: ms, base: res, bundle: in.Bundle, epoch: x.armEpoch.Load()}
	x.holds[in.Control] = p
	gen := p.gen
	x.hmu.Unlock()

	cancel := x.after(durMs(ms), func() { x.holdFire(in, gen, typ, key, cmd, args) })
	x.hmu.Lock()
	if cur := x.holds[in.Control]; cur != nil && cur.gen == gen {
		cur.cancel = cancel
	} else {
		cancel() // replaced or cancelled while arming
	}
	x.hmu.Unlock()

	res.Handled = true
	res.Text = fmt.Sprintf("hold %d ms to fire", ms)
	return res
}

// holdFire runs at the threshold. A stale or cancelled timer (generation
// mismatch) does nothing.
func (x *Executor) holdFire(in Input, gen uint64, typ, key, cmd string, args []string) {
	x.hmu.Lock()
	p := x.holds[in.Control]
	if p == nil || p.gen != gen {
		x.hmu.Unlock()
		return
	}
	delete(x.holds, in.Control)
	res := p.base
	bundle, epoch := p.bundle, p.epoch
	x.hmu.Unlock()

	// A hold belongs to the app and the arm state it began in. If the frontmost
	// app changed meanwhile, or the switch was touched, it must not fire into
	// something else.
	if x.armEpoch.Load() != epoch {
		log.Printf("[appdeck] control=%q hold cancelled: the arm switch changed", in.Control)
		return
	}
	if x.HoldGuard != nil && !x.HoldGuard(bundle) {
		log.Printf("[appdeck] control=%q hold cancelled: app changed", in.Control)
		return
	}

	res.Handled = true
	switch typ {
	case "key":
		if key == "" {
			return
		}
		if err := x.sendKey(key); err != nil {
			return
		}
		res.Fired, res.Text = true, x.said(key)
	case "shell":
		if cmd == "" {
			return
		}
		x.shell(cmd, args)
		res.Fired, res.Text = true, x.said("run "+cmd)
	}
	if x.OnHold != nil {
		x.OnHold(in, res)
	}
}

// cancelHold drops a pending hold for control (the release came first).
func (x *Executor) cancelHold(control string) (ms int, base Result, was bool) {
	x.hmu.Lock()
	defer x.hmu.Unlock()
	p := x.holds[control]
	if p == nil {
		return 0, Result{}, false
	}
	delete(x.holds, control)
	if p.cancel != nil {
		p.cancel()
	}
	return p.ms, p.base, true
}

// CancelHolds drops every pending hold (hot reload, engine Stop).
func (x *Executor) CancelHolds() {
	x.hmu.Lock()
	all := x.holds
	x.holds = nil
	x.hmu.Unlock()
	for _, p := range all {
		if p.cancel != nil {
			p.cancel()
		}
	}
}

func (x *Executor) holdCount() int {
	x.hmu.Lock()
	defer x.hmu.Unlock()
	return len(x.holds)
}
