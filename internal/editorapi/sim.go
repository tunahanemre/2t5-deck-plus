package editorapi

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"2t5-deck-plus/internal/accel"
	"2t5-deck-plus/internal/actions"
	"2t5-deck-plus/internal/preset"
)

// PreviewOut renders the plausible result of an input event for a resolved
// binding, in the action's own unit. The real engine reports its own numbers;
// this is what the dev simulator (and a test) uses. ticks is signed:
// positive = clockwise.
func PreviewOut(eb preset.EffectiveBinding, kind string, ticks int, ticksPerSec float64) Out {
	b := eb.Binding
	sign := 1.0
	if ticks < 0 {
		sign = -1
	}
	n := math.Abs(float64(ticks))
	thr := float64(b.DetentThreshold)
	if thr < 1 {
		thr = 4
	}
	steps := math.Floor(n / thr)
	pm := func(v float64, unit string) Out {
		return Out{Text: fmt.Sprintf("%+g %s", v, unit), Value: v, Unit: unit}
	}
	switch {
	case kind != "turn":
		switch b.Type {
		case "key":
			return Out{Text: b.Key, Unit: "key"}
		case "shell":
			return Out{Text: "run " + fmt.Sprint(eb.Raw["cmd"]), Unit: "command"}
		case "layer":
			return Out{Text: fmt.Sprintf("layer %d", b.Layer), Value: float64(b.Layer), Unit: "layer"}
		case "none":
			return Out{Text: "disabled"}
		}
		a, _ := actions.Default().ForType(b.Type)
		return Out{Text: a.Label}
	case b.IsJog():
		base, gamma := b.Base, b.Gamma
		if base == 0 {
			base = 1
		}
		if gamma == 0 {
			gamma = 1.6
		}
		v := math.Round(base * math.Pow(n, gamma))
		if b.MaxDelta > 0 && v > b.MaxDelta {
			v = b.MaxDelta
		}
		return pm(sign*v, "frames")
	case b.IsJogSeconds():
		sec := b.SecondsPerStep
		if sec == 0 {
			sec = 60
		}
		return pm(sign*steps*sec, "seconds")
	case b.IsJogMinuteSnap():
		return Out{Text: map[bool]string{true: "next minute", false: "previous minute"}[sign > 0], Value: sign * math.Max(steps, 0), Unit: "minutes"}
	case b.IsScrollDelta():
		l := b.LinesPerDetent
		if l == 0 {
			l = 1
		}
		return pm(sign*steps*float64(l), "lines")
	case b.IsKeyDelta():
		k := b.KeyCW
		if sign < 0 {
			k = b.KeyCCW
		}
		return Out{Text: k, Value: steps, Unit: "presses"}
	case b.IsProperty():
		sens := b.Sens
		if sens == 0 {
			sens = 1
		}
		return pm(sign*n*sens, "units")
	case b.Type == "wheel" || b.IsCDL() || b.IsBalance():
		sens := b.Sens
		if sens == 0 {
			sens = 15
		}
		return pm(sign*n*sens, "px")
	}
	a, _ := actions.Default().ForType(b.Type)
	return Out{Text: a.Label, Value: sign * n, Unit: "clicks"}
}

// Simulator turns requests into events using the real preset resolution, so
// the live test view shows what the actual presets would do.
type Simulator struct {
	Store     *preset.Store
	Frontmost FrontmostProvider
	Bus       *Bus
	Layer     func() int    // nil = layer 1
	Page      func() string // Resolve page ("edit", "col"); nil = "edit"
	Mode      func() string // live slider mode; nil = none
	Now       func() time.Time
	// Arm, when set and disarmed, makes events for actions the appdeck runs
	// (keys, scroll, shell) carry out.text prefixed "disarmed: ", like the app.
	Arm ArmProvider
}

func (s *Simulator) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Simulate publishes one event for a control.
func (s *Simulator) Simulate(req SimRequest) (Event, error) {
	switch req.Kind {
	case "turn", "press", "release":
	default:
		return Event{}, &actions.FieldError{Field: "kind", Msg: "kind must be turn, press or release"}
	}
	known := false
	for _, c := range ControlIDs() {
		if c == req.Control {
			known = true
		}
	}
	if !known {
		return Event{}, &actions.FieldError{Field: "control", Msg: fmt.Sprintf("%q is not a control of this deck", req.Control)}
	}
	ticks := req.Ticks
	if req.Kind == "turn" && ticks == 0 {
		ticks = 1
	}
	if ticks > 1000 || ticks < -1000 {
		return Event{}, &actions.FieldError{Field: "ticks", Msg: "ticks must be within +-1000"}
	}
	var front AppRef
	if s.Frontmost != nil {
		front = s.Frontmost.Frontmost()
	}
	layer := 1
	if s.Layer != nil {
		layer = s.Layer()
	}
	pmode := req.SliderMode
	if pmode == "" && s.Mode != nil {
		pmode = s.Mode()
	}
	t0 := time.Now()
	snap := s.Store.Snapshot()
	var eb preset.EffectiveBinding
	var ok bool
	if front.ID == preset.ResolveID {
		page := "edit"
		if s.Page != nil {
			page = s.Page()
		}
		eb, ok = snap.ResolveLegacyMode(page, pmode, layer, req.Control)
	} else {
		eb, ok = snap.ResolveForModeMod(front.ID, "", pmode, layer, req.Control)
	}
	tps := math.Abs(float64(ticks)) * 20 // a 50 ms drain tick
	ev := Event{T: s.now().UnixMilli(), Control: req.Control, Kind: req.Kind, RawTicks: ticks, TicksPerSec: tps}
	if ok {
		ev.PresetID, ev.Source, ev.Bank, ev.Action = eb.PresetID, eb.Source, eb.Bank, eb.Action
		ev.Out = PreviewOut(eb, req.Kind, ticks, tps)
		if s.Arm != nil && !s.Arm.Armed() && ev.Out.Text != "" && (front.ID != preset.ResolveID || eb.PresetID == preset.GlobalID) {
			switch {
			case req.Kind == "press" && (eb.Binding.Type == "key" || eb.Binding.Type == "shell"),
				req.Kind == "turn" && (eb.Binding.Type == "keyDelta" || eb.Binding.Type == "scrollDelta"):
				ev.Out.Text = "disarmed: " + ev.Out.Text
			}
		}
		if req.Kind == "turn" {
			if front.ID == preset.ResolveID {
				// Resolve verbs: indicative only (the Resolve engine paths do not use a profile yet)
				ev.Accel = math.Round(math.Pow(math.Abs(float64(ticks)), 0.6)*100) / 100
			} else {
				// appdeck turns: the real multiplier of the assignment's velocity profile
				name, _ := eb.Raw["velocityProfile"].(string)
				p, _ := accel.ProfileByName(name)
				ev.Accel = p.Multiplier(tps)
			}
		}
		if req.Kind != "release" && ev.Out.Text != "" && !strings.HasPrefix(ev.Out.Text, "disarmed: ") {
			// the simulator posts nothing: this is the time it took to resolve and
			// preview the assignment, enough to see the field in the dev UI
			if us := time.Since(t0).Microseconds(); us > 0 {
				ev.LatencyMs = float64(us) / 1000
			} else {
				ev.LatencyMs = 0.001
			}
		}
	} else {
		ev.Source = "none"
		ev.Out = Out{Text: "unassigned"}
	}
	return s.Bus.Publish(ev), nil
}

// Run publishes plausible random traffic until ctx ends: knob-turn bursts
// with varying ticks per second, and the odd button press.
func (s *Simulator) Run(ctx context.Context, rng *rand.Rand) {
	knobs := []string{"D1", "D2", "ControlDial", "Exposure", "Contrast", "Saturation", "Temperature"}
	buttons := []string{"C1", "C2", "C3", "P1btn", "P2btn", "Undo", "Redo", "L1", "L2"}
	for ctx.Err() == nil {
		if rng.Intn(5) == 0 {
			c := buttons[rng.Intn(len(buttons))]
			_, _ = s.Simulate(SimRequest{Control: c, Kind: "press"})
			time.Sleep(60 * time.Millisecond)
			_, _ = s.Simulate(SimRequest{Control: c, Kind: "release"})
		} else {
			c := knobs[rng.Intn(len(knobs))]
			dir := 1
			if rng.Intn(2) == 0 {
				dir = -1
			}
			burst := 3 + rng.Intn(10)
			speed := 1 + rng.Intn(14)
			for i := 0; i < burst && ctx.Err() == nil; i++ {
				_, _ = s.Simulate(SimRequest{Control: c, Kind: "turn", Ticks: dir * speed})
				time.Sleep(50 * time.Millisecond)
			}
		}
		select {
		case <-ctx.Done():
		case <-time.After(time.Duration(300+rng.Intn(1500)) * time.Millisecond):
		}
	}
}
