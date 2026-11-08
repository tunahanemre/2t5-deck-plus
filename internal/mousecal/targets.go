package mousecal

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"2t5-deck-plus/internal/config"
	"2t5-deck-plus/internal/preset"
)

// Target kinds.
const (
	KindClick = "click"
	KindDragH = "drag-h"
	KindDragV = "drag-v"
	KindBox   = "box"
)

// Use is one control that needs a target.
type Use struct {
	Bank    string `json:"bank"`
	Control string `json:"control"`
	Why     string `json:"why"` // "wheel drag", "reset", "click", "balance", ...
}

// Target is one point an app's mouse actions need.
type Target struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Kind        string `json:"kind"`
	Axis        string `json:"axis"` // h | v | click
	Instruction string `json:"instruction"`
	UsedBy      []Use  `json:"usedBy"`
}

var wheelNames = map[string]string{"lift": "Lift", "gamma": "Gamma", "gain": "Gain", "offset": "Offset"}

func wheelName(w string) string {
	if n, ok := wheelNames[w]; ok {
		return n
	}
	if w == "" {
		return w
	}
	return strings.ToUpper(w[:1]) + w[1:]
}

var channelNames = map[string]string{"R": "Red", "G": "Green", "B": "Blue"}

// Describe gives the label, kind, axis and capture instruction of a target key.
// Resolve's colour page names are known; any other key gets a generic text.
func Describe(appID, key string) (label, kind, axis, instruction string) {
	resolve := strings.EqualFold(appID, preset.ResolveID)
	parts := strings.Split(key, "_")
	if resolve && len(parts) == 2 {
		w, suffix := parts[0], parts[1]
		if _, isWheel := wheelNames[w]; isWheel || suffix == "master" || suffix == "reset" {
			W := wheelName(w)
			switch {
			case suffix == "master":
				return W + " master slider", KindDragH, "h",
					fmt.Sprintf("Put the pointer on the middle of the %s master slider (the thin bar under the %s wheel on the Color page).", W, W)
			case suffix == "reset":
				return W + " reset button", KindClick, "click",
					fmt.Sprintf("Put the pointer on the reset arrow (↺) at the top right of the %s wheel.", W)
			case channelNames[suffix] != "":
				c := channelNames[suffix]
				return fmt.Sprintf("%s %s value box", W, c), KindBox, "click",
					fmt.Sprintf("Put the pointer in the middle of the %s number box under the %s wheel.", strings.ToLower(c), W)
			}
		}
	}
	if resolve && len(parts) == 2 && (parts[0] == "hvs" || parts[0] == "hvl") {
		curve, tab := "Hue vs Sat", "Hue vs Sat"
		if parts[0] == "hvl" {
			curve, tab = "Hue vs Lum", "Hue vs Lum"
		}
		hue := strings.ToUpper(parts[1][:1]) + parts[1][1:]
		return fmt.Sprintf("%s curve at %s", curve, hue), KindDragV, "v",
			fmt.Sprintf("Open the Curves palette on the Color page and choose %s. Put the pointer on the curve line where the %s hue sits (the %s swatch position, mid-height).", tab, strings.ToLower(hue), strings.ToLower(hue))
	}
	h := strings.ReplaceAll(key, "_", " ")
	return h, KindClick, "click", fmt.Sprintf("Put the pointer exactly where “%s” should be clicked.", h)
}

// DeriveTargets lists the targets the assignments of preset p need, in a stable
// order: master sliders, reset buttons, then clicks, then value boxes.
func DeriveTargets(p *preset.Preset) []Target {
	if p == nil {
		return nil
	}
	by := map[string]*Target{}
	order := map[string]int{}
	add := func(key, bank, control, why string, rank int) {
		if key == "" {
			return
		}
		t := by[key]
		if t == nil {
			label, kind, axis, ins := Describe(p.ID, key)
			t = &Target{Key: key, Label: label, Kind: kind, Axis: axis, Instruction: ins, UsedBy: []Use{}}
			by[key] = t
			order[key] = rank
		}
		t.UsedBy = append(t.UsedBy, Use{Bank: bank, Control: control, Why: why})
	}
	boxes := func(bank, control, why string) {
		for _, w := range []string{"lift", "gamma", "gain", "offset"} {
			for _, c := range []string{"R", "G", "B"} {
				add(w+"_"+c, bank, control, why, 3)
			}
		}
	}
	banks := make([]string, 0, len(p.Banks))
	for b := range p.Banks {
		banks = append(banks, b)
	}
	sort.Strings(banks)
	for _, bank := range banks {
		controls := make([]string, 0, len(p.Banks[bank]))
		for c := range p.Banks[bank] {
			controls = append(controls, c)
		}
		sort.Strings(controls)
		for _, control := range controls {
			a := p.Banks[bank][control]
			typ, _ := a["type"].(string)
			press, _ := a["press"].(map[string]any)
			switch typ {
			case "wheel":
				w, _ := a["wheel"].(string)
				add(w, bank, control, "wheel drag", 0)
				if press == nil && w != "" {
					r := strings.Replace(w, "_master", "_reset", 1)
					if r == w {
						r = w + "_reset"
					}
					add(r, bank, control, "wheel reset", 1)
				}
			case "hueCurve":
				curve, _ := a["curve"].(string)
				hue := 0
				switch v := a["hue"].(type) {
				case float64:
					hue = int(v)
				case json.Number:
					n, _ := v.Int64()
					hue = int(n)
				}
				add(config.HueCurveKey(curve, hue), bank, control, "hue curve drag", 0)
			case "click":
				pt, _ := a["point"].(string)
				add(pt, bank, control, "click", 2)
			case "balance", "resetActiveWheel":
				boxes(bank, control, typ)
			}
			if press != nil {
				if pt, _ := press["point"].(string); pt != "" && press["type"] == "click" {
					add(pt, bank, control, "knob press click", 2)
				}
			}
		}
	}
	// settings.resetTargets: name -> {type:"click", point:...}
	if rt, ok := p.Settings["resetTargets"].(map[string]any); ok {
		names := make([]string, 0, len(rt))
		for n := range rt {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			if m, ok := rt[n].(map[string]any); ok && m["type"] == "click" {
				pt, _ := m["point"].(string)
				add(pt, "settings", n, "reset target", 2)
			}
		}
	}
	out := make([]Target, 0, len(by))
	for _, t := range by {
		out = append(out, *t)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if order[out[i].Key] != order[out[j].Key] {
			return order[out[i].Key] < order[out[j].Key]
		}
		return out[i].Key < out[j].Key
	})
	return out
}

// PriorFor is the assumed resize behaviour of an app's targets, used only after
// a pixel probe agrees. Resolve's Color page lays its wheel row out
// proportionally across the window width and fixed in height above the bottom
// edge; this is a hypothesis, verified on hardware (see the plan, open items).
func PriorFor(appID string) Prior {
	if strings.EqualFold(appID, preset.ResolveID) {
		return Prior{X: ModeFrac, Y: ModeEnd}
	}
	return Prior{}
}
