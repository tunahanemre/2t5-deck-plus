// Package actions is the action registry of the 2T5 DECK+ preset editor: a
// catalogue of what a control can DO, grouped into packs, with a schema for
// every tunable field (label, unit, range, default).
//
// An action is a config.Binding "type" plus the schema of the fields that
// type reads. Nothing here is tied to a control: the same action can sit on
// any knob or button; the units live on the action (Resolve scrub is in
// frames, a browser scroll is in lines), never on the control.
//
// Borrowed: the idea of save-time validation of key specs from DECKMAP
// internal/store/store.go (c39fecd, validateBinding); copied as a pure
// validator (keyspec.go) instead of importing internal/keys, which is a cgo
// package that can arm real input.
package actions

import (
	"sort"

	"2t5-deck-plus/internal/config"
)

// Option is one choice of an enum field.
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Field describes one tunable of an action.
type Field struct {
	Key      string   `json:"key"`
	Label    string   `json:"label"`
	Unit     string   `json:"unit,omitempty"`
	Kind     string   `json:"kind"` // int|number|string|key|enum|bool|stringList|object
	Min      *float64 `json:"min,omitempty"`
	Max      *float64 `json:"max,omitempty"`
	Step     *float64 `json:"step,omitempty"`
	Default  any      `json:"default,omitempty"`
	Options  []Option `json:"options,omitempty"`
	Help     string   `json:"help,omitempty"`
	Required bool     `json:"required,omitempty"`
	// Group lists the sub-fields of an "object" field (e.g. the press group).
	// Values stay one JSON object under Key.
	Group []Field `json:"group,omitempty"`
	// Primary marks the one to three essential values an editor keeps on its
	// main screen; everything else belongs under "Advanced".
	Primary bool `json:"primary,omitempty"`
	// Seed makes Normalize write Default into the assignment when the editor
	// assigns the action, so the stored value and the engine agree.
	Seed bool `json:"-"`
}

// Action is one assignable behaviour.
type Action struct {
	ID    string `json:"id"`
	Pack  string `json:"pack"`
	Label string `json:"label"`
	// Type is the config.Binding type this compiles to.
	Type string `json:"type"`
	// Aliases are other type spellings the engine treats as this action.
	Aliases []string `json:"aliases,omitempty"`
	// Kind is the gesture it suits: press, turn or both.
	Kind string `json:"kind"`
	// Fixed values are written into the binding when the action is assigned.
	Fixed  map[string]any `json:"fixed,omitempty"`
	Fields []Field        `json:"fields"`
	Help   string         `json:"help,omitempty"`
	// Labs marks an experimental action the editor hides behind a Labs
	// switch. It is never seeded into any preset.
	Labs bool `json:"labs,omitempty"`
	// Unimplemented marks an action the engine does not run yet: it can be
	// browsed but a binding of this type does nothing.
	Unimplemented bool `json:"unimplemented,omitempty"`
	// Risk is what the action can do when it fires: safe | keys | shell |
	// destructive (see risk.go). Computed from the type and the fixed values.
	Risk string `json:"risk"`
}

// Pack is a named group of actions offered together.
type Pack struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Actions []Action `json:"actions"`
	// Always makes the pack available to every app and to Global without
	// ticking it in the app's pack list (the Global pack of everyday actions).
	Always bool `json:"always,omitempty"`
}

// Registry holds every pack.
type Registry struct {
	packs  []Pack
	byID   map[string]Action
	byType map[string]Action // canonical (non-fixed) action per type/alias
}

// OtherID is the synthetic action for a binding type the registry does not know.
const OtherID = "other"

var defaultReg = build()

// Default returns the shared registry.
func Default() *Registry { return defaultReg }

func build() *Registry {
	r := &Registry{byID: map[string]Action{}, byType: map[string]Action{}}
	for _, p := range allPacks() {
		for i := range p.Actions {
			a := &p.Actions[i]
			a.Pack = p.ID
			a.Fields = append(a.Fields, labelField)
			for fi := range a.Fields {
				for _, k := range primaryFields[a.ID] {
					if a.Fields[fi].Key == k {
						a.Fields[fi].Primary = true
					}
				}
			}
			a.Risk = actionRisk(*a)
			r.byID[a.ID] = *a
			if len(a.Fixed) == 0 || a.ID == "generic.key" {
				for _, t := range append([]string{a.Type}, a.Aliases...) {
					if _, dup := r.byType[t]; !dup {
						r.byType[t] = *a
					}
				}
			}
		}
		r.packs = append(r.packs, p)
	}
	return r
}

// Packs lists every pack in display order.
func (r *Registry) Packs() []Pack { return r.packs }

// PackAlways reports whether the pack is offered to every app.
func (r *Registry) PackAlways(id string) bool {
	for _, p := range r.packs {
		if p.ID == id {
			return p.Always
		}
	}
	return false
}

// Get returns an action by id.
func (r *Registry) Get(id string) (Action, bool) { a, ok := r.byID[id]; return a, ok }

// ForType returns the canonical action for a binding type or alias.
func (r *Registry) ForType(t string) (Action, bool) {
	if t == "" {
		t = "cdl"
	}
	a, ok := r.byType[t]
	return a, ok
}

// Types returns every binding type (and alias) the registry maps, sorted.
func (r *Registry) Types() []string {
	out := make([]string, 0, len(r.byType))
	for t := range r.byType {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// Matches reports whether the action compiles to binding type t.
func (a Action) Matches(t string) bool {
	if t == "" {
		t = "cdl"
	}
	if a.Type == t {
		return true
	}
	for _, x := range a.Aliases {
		if x == t {
			return true
		}
	}
	return false
}

// FieldByKey returns the field schema for key.
func (a Action) FieldByKey(key string) (Field, bool) {
	for _, f := range a.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

// Infer names the action of a raw assignment: the "action" alias when it is
// known, else the canonical action of its type, else a synthetic "other"
// action that carries the unknown type verbatim (so it still round-trips).
func (r *Registry) Infer(raw map[string]any) Action {
	t := rawType(raw)
	explicit := explicitType(raw) != ""
	if id, _ := raw["action"].(string); id != "" {
		if a, ok := r.byID[id]; ok && (!explicit || a.Matches(t)) {
			return a
		}
		// The alias disagrees with the type / method the RUNTIME reads: the
		// runtime wins here (Validate refuses such an assignment; this keeps
		// classification honest for anything that got in some other way).
	}
	if a, ok := r.ForType(t); ok {
		return a
	}
	return Action{ID: OtherID, Pack: "", Label: "Other (" + t + ")", Type: t, Kind: "both",
		Help: "A binding type this editor does not know. It is kept exactly as written."}
}

// explicitType is the type or method an assignment states ("" when neither),
// read the way the engine reads it.
func explicitType(raw map[string]any) string {
	if b, err := config.DecodeBinding(raw); err == nil {
		if b.Type != "" {
			return b.Type
		}
		return b.Method
	}
	if t, _ := raw["type"].(string); t != "" {
		return t
	}
	if m, _ := raw["method"].(string); m != "" {
		return m
	}
	return ""
}

// EffectiveType is the binding type the RUNTIME executes for a raw assignment
// (config.Binding: Type, else the legacy Method, else "cdl"). Everything that
// classifies, validates or infers an assignment goes through it; the "action"
// alias is only an editor label.
func EffectiveType(raw map[string]any) string { return rawType(raw) }

func rawType(raw map[string]any) string {
	if b, err := config.DecodeBinding(raw); err == nil {
		return b.EffectiveType() // what the engine runs, however the keys are spelled
	}
	// not decodable by the engine at all: fall back to the exact keys (such an
	// assignment is refused by Validate and by composeCheck anyway)
	if t, _ := raw["type"].(string); t != "" {
		return t
	}
	if m, _ := raw["method"].(string); m != "" {
		return m
	}
	return "cdl"
}

// RawType exposes the effective binding type of a raw assignment.
func RawType(raw map[string]any) string { return rawType(raw) }

// primaryFields names, per action, the essential fields (Field.Primary).
var primaryFields = map[string][]string{
	"generic.key":          {"key"},
	"generic.keyDelta":     {"keyCW", "keyCCW", "detentThreshold"},
	"generic.scrollDelta":  {"linesPerDetent", "detentThreshold"},
	"generic.shell":        {"cmd"},
	"generic.layer":        {"layer"},
	"generic.modifierHold": {"modifier"},
	"resolve.scrubFrames":  {"base", "gamma"},
	"resolve.scrubSeconds": {"secondsPerStep", "detentThreshold"},
	"resolve.snapMinute":   {"detentThreshold"},
	"resolve.selectClip":   {"detentThreshold"},
	"resolve.property":     {"property", "sens"},
	"resolve.wheel":        {"wheel", "sens"},
	"resolve.cdl":          {"param", "ch", "sens"},
	"resolve.balance":      {"hue", "sens"},
	"resolve.hueCurve":     {"curve", "hue", "sens"},
	"resolve.click":        {"point"},
	"resolve.selectWheel":  {"wheel"},
	"resolve.pmode":        {"pmode"},
	"resolve.addMarker":    {"markerColor", "markerDurationSeconds"},
	"resolve.markerColor":  {"markerColor"},
	"browser.scroll":       {"linesPerDetent", "detentThreshold"},
	"browser.tabs":         {"detentThreshold"},
	"browser.zoom":         {"detentThreshold"},
	"browser.history":      {"detentThreshold"},
	"finder.tag":           {"tag"},
}
