package accel

import (
	"math"
	"sort"
	"time"
)

// Velocity profiles (2T5 DECK+ wave C). A profile maps the RAW turn rate of an
// encoder (ticks per second, as the engine's 250 ms rate window reports it)
// to an output multiplier applied to the ticks before they reach the action.
//
// Design rules:
//   - "linear" is the identity (multiplier 1 at every rate) and the default
//     everywhere: an assignment without a velocityProfile behaves exactly as
//     before. Callers must bypass the profile code entirely for "linear".
//   - The tables below are the documented constants. Between knots the
//     multiplier is interpolated linearly; below the first knot and above
//     the last it is held flat. Every table is non-decreasing in the
//     multiplier and strictly increasing in rate.
//   - Everything here is pure; time is always injected.
//
// Not a copy of any Blackmagic behaviour: no acceleration curve is documented
// for their dials (research report 2026-11-03, item 2).

// Profile names.
const (
	ProfileLinear   = "linear"
	ProfilePrecise  = "precise"
	ProfileStandard = "standard"
	ProfileFast     = "fast"
)

// Knot is one point of a profile table.
type Knot struct {
	TicksPerSec float64
	Multiplier  float64
}

// Profile is a named velocity-to-multiplier table.
type Profile struct {
	Name  string
	Knots []Knot // nil = identity
}

// Documented constants. Rates are raw ticks per second.
var profiles = map[string]Profile{
	ProfileLinear: {Name: ProfileLinear},
	// precise: slow turns are finer than 1:1 (half speed at rest), only a
	// hard spin reaches 2.5x.
	ProfilePrecise: {Name: ProfilePrecise, Knots: []Knot{{0, 0.5}, {10, 0.75}, {30, 1.0}, {80, 1.5}, {200, 2.5}}},
	// standard: 1:1 for a deliberate turn (about 8 ticks/s), then ramps to 6x.
	ProfileStandard: {Name: ProfileStandard, Knots: []Knot{{0, 0.8}, {8, 1.0}, {25, 1.6}, {60, 3.0}, {150, 6.0}}},
	// fast: starts 1:1 and ramps early up to 12x.
	ProfileFast: {Name: ProfileFast, Knots: []Knot{{0, 1.0}, {6, 1.5}, {20, 3.0}, {50, 6.0}, {120, 12.0}}},
}

// ProfileNames lists the valid profile names in display order.
func ProfileNames() []string {
	return []string{ProfileLinear, ProfilePrecise, ProfileStandard, ProfileFast}
}

// IsLinear reports whether name selects the identity profile (empty = linear).
func IsLinear(name string) bool { return name == "" || name == ProfileLinear }

// ProfileByName returns a profile; ok is false for an unknown name (the
// returned Profile is then linear). An empty name is linear.
func ProfileByName(name string) (Profile, bool) {
	if name == "" {
		return profiles[ProfileLinear], true
	}
	p, ok := profiles[name]
	if !ok {
		return profiles[ProfileLinear], false
	}
	return p, true
}

// Table returns a copy of the knots (nil for linear).
func (p Profile) Table() []Knot { return append([]Knot(nil), p.Knots...) }

// Multiplier returns the output multiplier at rate ticks/s (sign ignored;
// NaN and negatives count as 0). Linear returns exactly 1.
func (p Profile) Multiplier(ticksPerSec float64) float64 {
	k := p.Knots
	if len(k) == 0 {
		return 1
	}
	r := math.Abs(ticksPerSec)
	if math.IsNaN(r) {
		r = 0
	}
	if r <= k[0].TicksPerSec {
		return k[0].Multiplier
	}
	last := k[len(k)-1]
	if r >= last.TicksPerSec || math.IsInf(r, 1) {
		return last.Multiplier
	}
	i := sort.Search(len(k), func(i int) bool { return k[i].TicksPerSec > r })
	a, b := k[i-1], k[i]
	t := (r - a.TicksPerSec) / (b.TicksPerSec - a.TicksPerSec)
	return a.Multiplier + t*(b.Multiplier-a.Multiplier)
}

// Scale returns signed ticks multiplied by the profile multiplier at rate.
func (p Profile) Scale(ticks int, ticksPerSec float64) float64 {
	return float64(ticks) * p.Multiplier(ticksPerSec)
}

// Carry turns fractional scaled ticks into whole ticks without losing the
// remainder: Add returns the integer part to act on now and keeps the rest
// for the next call. The zero value is ready; not safe for concurrent use.
type Carry struct{ frac float64 }

// Add accumulates v and returns the whole ticks now due (truncated towards
// zero, so the carry never flips sign).
func (c *Carry) Add(v float64) int {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0
	}
	c.frac += v
	// absorb float error (ten additions of 0.3 must make 3, not 2.9999999)
	if r := math.Round(c.frac); math.Abs(c.frac-r) < 1e-9 {
		c.frac = r
	}
	n := math.Trunc(c.frac)
	c.frac -= n
	return int(n)
}

// Reset drops the remainder.
func (c *Carry) Reset() { c.frac = 0 }

// Momentum constants.
const (
	// MaxMomentumMs is the longest decay time constant the registry allows.
	MaxMomentumMs = 2000
	// momentumFloor is the rate (ticks/s) under which the tail is finished.
	momentumFloor = 1.0
	// momentumMaxTaus bounds a tail to this many time constants.
	momentumMaxTaus = 6
)

// Momentum is an exponential-decay tail that keeps a control turning after
// the hand lets go (a weighted wheel). Time is injected. The zero value is
// idle. Not safe for concurrent use; the caller serialises.
//
// Kick(now, v) records the current signed output rate v (ticks/s). Advance(now)
// returns the signed ticks produced since the previous Kick/Advance:
// the exact integral of v*exp(-t/tau), so the result does not depend on how
// often Advance is called.
type Momentum struct {
	Tau time.Duration // decay time constant; <= 0 disables the tail

	v     float64
	at    time.Time
	start time.Time
	live  bool
}

// Kick (re)starts the tail at signed rate v ticks/s.
func (m *Momentum) Kick(now time.Time, v float64) {
	if m.Tau <= 0 || math.IsNaN(v) || math.Abs(v) < momentumFloor {
		m.live = false
		return
	}
	m.v, m.at, m.start, m.live = v, now, now, true
}

// Active reports whether a tail is still running.
func (m *Momentum) Active() bool { return m.live }

// Stop ends the tail immediately.
func (m *Momentum) Stop() { m.live = false }

// Advance returns the signed ticks produced between the last call and now.
func (m *Momentum) Advance(now time.Time) float64 {
	if !m.live {
		return 0
	}
	dt := now.Sub(m.at)
	if dt <= 0 {
		return 0
	}
	tau := m.Tau.Seconds()
	end := now
	if limit := m.start.Add(time.Duration(momentumMaxTaus) * m.Tau); end.After(limit) {
		end = limit
		dt = end.Sub(m.at)
		if dt < 0 {
			dt = 0
		}
	}
	d := dt.Seconds()
	// integral of v*e^(-t/tau) over d = v*tau*(1-e^(-d/tau))
	ticks := m.v * tau * (1 - math.Exp(-d/tau))
	m.v *= math.Exp(-d / tau)
	m.at = end
	if math.Abs(m.v) < momentumFloor || !end.Before(m.start.Add(time.Duration(momentumMaxTaus)*m.Tau)) {
		m.live = false
	}
	return ticks
}
