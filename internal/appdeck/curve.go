package appdeck

import (
	"fmt"
	"math"

	"2t5-deck-plus/internal/accel"
	"2t5-deck-plus/internal/actions"
	"2t5-deck-plus/internal/tc"
)

// The editor's sensitivity meter. Every number here comes from the functions
// the executor (and, for Resolve jog, the engine) calls: accel.ProfileByName /
// Multiplier, feelOf, the detent threshold and lines defaults, tc.AccelerateWith.

// CurvePoint is one sample of the sensitivity curve.
type CurvePoint struct {
	TicksPerSec  float64 `json:"ticksPerSec"`
	OutputPerSec float64 `json:"outputPerSec"`
	// Multiplier is the effective gain against a 1:1 turn: outputPerSec
	// divided by (ticksPerSec x the output of one raw tick at 1:1).
	Multiplier float64 `json:"multiplier"`
}

// CurveInfo is the result of Curve.
type CurveInfo struct {
	Points     []CurvePoint
	Unit       string
	Profile    string
	MomentumMs int
	// Staged is true when the action's profile/momentum is saved but not yet
	// applied by the engine (Resolve verbs): the points then show what the
	// control does TODAY.
	Staged bool
	// Approx is true when the points are a smooth model of a lumpy
	// per-tick behaviour (Resolve jog).
	Approx bool
}

// CurveSamples is the number of intervals sampled (points = CurveSamples+1).
const CurveSamples = 24

// drainTick is the engine's drain period (50 ms) used to model Resolve jog.
const drainTick = 0.05

// Curve samples output per second for a turn action. typ is the binding type
// (scrollDelta, keyDelta, jog, jogSeconds); raw the assignment fields.
func Curve(typ string, raw map[string]any, maxTicksPerSec float64) (CurveInfo, error) {
	if maxTicksPerSec <= 0 || math.IsNaN(maxTicksPerSec) || math.IsInf(maxTicksPerSec, 0) {
		return CurveInfo{}, fmt.Errorf("maxTicksPerSec must be a positive number")
	}
	profile, momentumMs := feelOf(raw)
	prof, _ := accel.ProfileByName(profile)
	num := func(key string, def float64) float64 {
		if v, ok := raw[key]; ok {
			if f, ok := actions.ToFloat(v); ok && !math.IsNaN(f) && !math.IsInf(f, 0) {
				return f
			}
		}
		return def
	}
	thrDef := float64(defaultDetentThreshold)
	if typ == "jogSeconds" {
		thrDef = 4 // engine defaultSelectClipDetentThreshold (Resolve verb)
	}
	thr := num("detentThreshold", thrDef)
	if thr <= 0 {
		thr = thrDef
	}
	info := CurveInfo{Profile: profile, MomentumMs: momentumMs}
	// perTick: output of one raw tick at 1:1; model: output/s at a rate.
	var perTick float64
	var model func(rate, m float64) float64
	switch typ {
	case "scrollDelta":
		lines := math.Abs(num("linesPerDetent", defaultLinesPerDetent))
		if lines == 0 {
			lines = defaultLinesPerDetent
		}
		info.Unit = "lines"
		perTick = lines / thr
		model = func(rate, m float64) float64 { return rate * m * perTick }
	case "keyDelta":
		info.Unit = "keys"
		perTick = 1 / thr
		model = func(rate, m float64) float64 { return rate * m * perTick }
	case "jogSeconds":
		sec := math.Abs(num("secondsPerStep", 60))
		info.Unit, info.Staged = "seconds", true
		perTick = sec / thr
		model = func(rate, _ float64) float64 { return rate * perTick } // engine: no profile yet
	case "jog":
		base, gamma := num("base", 1), num("gamma", 1.6)
		maxDelta := num("maxDelta", 0)
		info.Unit, info.Staged, info.Approx = "frames", true, true
		perTick = base
		if perTick <= 0 {
			perTick = 1
		}
		model = func(rate, _ float64) float64 {
			if rate == 0 {
				return 0
			}
			// frames per drain tick for the ticks that arrive in one drain tick
			step := int(math.Max(1, math.Round(rate*drainTick)))
			frames := float64(tc.AccelerateWith(step, base, gamma))
			if maxDelta > 0 && frames > maxDelta {
				frames = maxDelta
			}
			return rate * frames / float64(step)
		}
	default:
		return CurveInfo{}, fmt.Errorf("%q is not a turn action with a speed curve", typ)
	}
	for i := 0; i <= CurveSamples; i++ {
		rate := maxTicksPerSec * float64(i) / CurveSamples
		m := 1.0
		if !info.Staged {
			m = prof.Multiplier(rate)
		}
		out := model(rate, m)
		gain := 1.0
		if rate > 0 {
			gain = out / (rate * perTick)
		} else if !info.Staged {
			gain = m
		}
		info.Points = append(info.Points, CurvePoint{TicksPerSec: rate, OutputPerSec: out, Multiplier: gain})
	}
	return info, nil
}
