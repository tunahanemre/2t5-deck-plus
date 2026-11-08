package appdeck

// Velocity feel (wave C): optional per-assignment velocityProfile and
// momentumMs for keyDelta / scrollDelta. Absent or "linear" + 0 = the old
// path in turn(), which this file is never reached from.
//
// Pipeline for a feel turn: raw step -> rate (ticks/s over a 250 ms window,
// the same window the engine reports) -> profile multiplier -> fractional
// carry -> whole ticks -> the usual detent accumulator -> fire().
// Momentum: after the hand stops (no input for tailIdle) an exponential tail
// keeps feeding ticks at the last output rate, every tailInterval, until it
// decays. A new turn cancels the tail. The tail only runs when TailGuard is
// set and true, so a tail can never act in an app the user has left.

import (
	"math"
	"sync"
	"time"

	"2t5-deck-plus/internal/accel"
	"2t5-deck-plus/internal/actions"
	"2t5-deck-plus/internal/config"
)

const (
	rateWindow   = 250 * time.Millisecond
	tailInterval = 16 * time.Millisecond
	tailIdle     = 70 * time.Millisecond
)

// feelOf reads velocityProfile / momentumMs from a merged raw assignment.
// Anything invalid counts as absent (linear / 0): the editor validates, and a
// hand-edited file must never break turning.
func feelOf(raw map[string]any) (profile string, momentumMs int) {
	if raw == nil {
		return accel.ProfileLinear, 0
	}
	if s, ok := raw["velocityProfile"].(string); ok {
		if _, known := accel.ProfileByName(s); known {
			profile = s
		}
	}
	if profile == "" {
		profile = accel.ProfileLinear
	}
	if v, ok := raw["momentumMs"]; ok {
		if f, ok := actions.ToFloat(v); ok && !math.IsNaN(f) && !math.IsInf(f, 0) && f > 0 {
			if f > accel.MaxMomentumMs {
				f = accel.MaxMomentumMs
			}
			momentumMs = int(f)
		}
	}
	return profile, momentumMs
}

// feelState is the per-control state of a feel turn. mu guards everything
// below it; the tail goroutine and the MIDI goroutine both take it.
type feelState struct {
	mu   sync.Mutex
	at   []time.Time
	tick []int
	cry  accel.Carry
	mom  accel.Momentum

	lastIn  time.Time
	pendV   float64 // signed output rate (ticks/s) at the last input
	started bool    // tail Kick done for the current gesture
	armed   bool    // a tail timer is pending
	gen     int     // bumped on arm and on stop; a timer from an older gen is stale
	cancel  func()
	bundle  string
}

func (x *Executor) feelState(key string) *feelState {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.fs == nil {
		x.fs = map[string]*feelState{}
	}
	f := x.fs[key]
	if f == nil {
		f = &feelState{}
		x.fs[key] = f
	}
	return f
}

// stopTails kills every running tail (snapshot swap, reset). States stay so
// the next turn re-arms normally.
func (x *Executor) stopTails() {
	x.mu.Lock()
	all := make([]*feelState, 0, len(x.fs))
	for _, f := range x.fs {
		all = append(all, f)
	}
	x.mu.Unlock()
	for _, f := range all {
		f.mu.Lock()
		f.mom.Stop()
		f.started = false
		f.armed = false
		f.gen++
		if f.cancel != nil {
			f.cancel()
			f.cancel = nil
		}
		f.mu.Unlock()
	}
}

func (x *Executor) now() time.Time {
	if x.Now != nil {
		return x.Now()
	}
	return time.Now()
}

func (x *Executor) after(d time.Duration, f func()) func() {
	if x.After != nil {
		return x.After(d, f)
	}
	t := time.AfterFunc(d, f)
	return func() { t.Stop() }
}

// rate records |ticks| at now and returns ticks/s over rateWindow.
func (f *feelState) rate(now time.Time, ticks int) float64 {
	if ticks < 0 {
		ticks = -ticks
	}
	f.at = append(f.at, now)
	f.tick = append(f.tick, ticks)
	cut := 0
	for cut < len(f.at) && now.Sub(f.at[cut]) > rateWindow {
		cut++
	}
	f.at, f.tick = f.at[cut:], f.tick[cut:]
	sum := 0
	for _, t := range f.tick {
		sum += t
	}
	return float64(sum) / rateWindow.Seconds()
}

// feelTurn is the feel path of turn(): it returns the detent count to fire
// and sets res.Accel.
func (x *Executor) feelTurn(key string, d *detent, in Input, b config.Binding, typ, profile string, momentumMs int, res *Result) int {
	prof, _ := accel.ProfileByName(profile)
	now := in.Now
	if now.IsZero() {
		now = x.now()
	}
	f := x.feelState(key)
	f.mu.Lock()
	defer f.mu.Unlock()
	rate := f.rate(now, in.Step)
	m := prof.Multiplier(rate)
	res.Accel = m
	whole := f.cry.Add(float64(in.Step) * m)
	count := d.add(whole, b.DetentThreshold)

	// A new turn always cancels a running tail.
	f.mom.Stop()
	f.started = false
	if momentumMs > 0 && x.TailGuard != nil && in.Step != 0 {
		sign := 1.0
		if in.Step < 0 {
			sign = -1
		}
		f.pendV = sign * rate * m
		f.lastIn = now
		f.mom.Tau = time.Duration(momentumMs) * time.Millisecond
		f.bundle = in.Bundle
		base := Result{Handled: true, PresetID: res.PresetID, Source: res.Source, Bank: res.Bank, Action: res.Action, Accel: m}
		if !f.armed {
			f.armed = true
			f.gen++
			gen := f.gen
			f.cancel = x.after(tailInterval, func() { x.tailStep(f, d, in, b, typ, base, gen) })
		}
	}
	return count
}

// tailStep runs on the timer goroutine.
//
// The step fires WHILE HOLDING f.mu (wave E, F15): stopTails (snapshot swap,
// app switch) takes the same lock, so once it returns no step that had already
// decided to fire can still post afterwards. OnTail runs after the unlock.
func (x *Executor) tailStep(f *feelState, d *detent, in Input, b config.Binding, typ string, base Result, gen int) {
	var res Result
	var fired bool

	f.mu.Lock()
	if f.gen != gen || !f.armed {
		f.mu.Unlock()
		return
	}
	guard := x.TailGuard
	now := x.now()
	rearm := true
	var count int
	switch {
	case guard == nil || !guard(f.bundle):
		f.mom.Stop()
		f.started, rearm = false, false
	case !f.started && now.Sub(f.lastIn) < tailIdle:
		// the hand is still on the dial: the live input is doing the work
	case !f.started:
		f.mom.Kick(now, f.pendV)
		f.started = f.mom.Active()
		rearm = f.started
	default:
		ticks := f.mom.Advance(now)
		whole := f.cry.Add(ticks)
		count = d.add(whole, b.DetentThreshold)
		rearm = f.mom.Active()
		if !rearm {
			f.started = false
		}
	}
	if rearm {
		f.cancel = x.after(tailInterval, func() { x.tailStep(f, d, in, b, typ, base, gen) })
	} else {
		f.armed = false
		f.cancel = nil
	}
	if count != 0 {
		res = base
		x.fire(in.Control, b, typ, count, &res)
		fired = res.Fired
	}
	f.mu.Unlock()

	if fired && x.OnTail != nil {
		x.OnTail(in, res)
	}
}

func durMs(ms int) time.Duration { return time.Duration(ms) * time.Millisecond }
