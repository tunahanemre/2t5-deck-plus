package actions

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"

	"2t5-deck-plus/internal/config"
)

// FieldError names the field that failed validation.
type FieldError struct {
	Field string `json:"field"`
	Msg   string `json:"error"`
}

func (e *FieldError) Error() string {
	if e.Field == "" {
		return e.Msg
	}
	return e.Field + ": " + e.Msg
}

func ferr(field, format string, a ...any) error {
	return &FieldError{Field: field, Msg: fmt.Sprintf(format, a...)}
}

// ToFloat converts a JSON-decoded number (json.Number, float64, ints).
func ToFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// Normalize returns a copy of raw ready to store: null values are dropped
// (null means "inherit"), the "type" is filled from a known "action", the
// action's Fixed values are written, and Seed defaults are materialised.
func (r *Registry) Normalize(raw map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(raw)+2)
	for k, v := range raw {
		if v != nil {
			out[k] = v
		}
	}
	id, _ := out["action"].(string)
	if id == "" || id == OtherID {
		return out, nil
	}
	a, ok := r.byID[id]
	if !ok {
		return nil, ferr("action", "unknown action %q", id)
	}
	if t, _ := out["type"].(string); t != "" && !a.Matches(t) {
		return nil, ferr("type", "type %q does not belong to action %q (expects %q)", t, id, a.Type)
	}
	if t, _ := out["type"].(string); t == "" {
		out["type"] = a.Type
	}
	for k, v := range a.Fixed {
		out[k] = v
	}
	for _, f := range a.Fields {
		if _, set := out[f.Key]; !set && f.Seed && f.Default != nil {
			out[f.Key] = f.Default
		}
	}
	return out, nil
}

// Validate checks an assignment. strict (editor writes) rejects unknown
// types and missing required fields; lenient (loading from disk) only checks
// the fields it knows. Unknown extra keys always pass through.
func (r *Registry) Validate(raw map[string]any, strict bool) error {
	// type, method and the action alias must tell the same story: the runtime
	// reads type (else method) and ignores the alias, so an alias that points
	// at a harmless action over a shell type would pass for something it is not.
	if nc := config.NonCanonicalKeys(raw); len(nc) > 0 {
		sort.Slice(nc, func(i, j int) bool { return nc[i].Path < nc[j].Path })
		return ferr(nc[0].Path, "%v", nc[0])
	}
	b, derr := config.DecodeBinding(raw)
	ty, me := b.Type, b.Method
	if derr != nil { // not readable by the engine: the exact keys are all we can name
		ty, _ = raw["type"].(string)
		me, _ = raw["method"].(string)
	}
	if ty != "" && me != "" && ty != me {
		return ferr("method", "method %q disagrees with type %q (the deck runs the type); remove one", me, ty)
	}
	if id, _ := raw["action"].(string); id != "" && id != OtherID {
		al, ok := r.byID[id]
		if !ok {
			return ferr("action", "unknown action %q", id)
		}
		if et := explicitType(raw); et != "" && !al.Matches(et) {
			field := "type"
			if ty == "" {
				field = "method"
			}
			return ferr(field, "%s %q does not belong to action %q (expects %q): the deck runs the %s, not the action", field, et, id, al.Type, field)
		}
	}
	// holdMs (wave H) means the same on every action that can be held, and an
	// ignored but malformed value must not reach the engine's typed decode.
	if v, set := raw["holdMs"]; set && v != nil {
		if err := ValidateValue(holdMsField, v); err != nil {
			return err
		}
	}
	unreadable := func() error {
		if derr != nil {
			return ferr("", "the deck cannot read this assignment: %v", derr)
		}
		return nil
	}
	a := r.Infer(raw)
	if a.ID == OtherID {
		if strict {
			if id, _ := raw["action"].(string); id != OtherID {
				return ferr("type", "unknown binding type %q (use action \"other\" to store it verbatim)", a.Type)
			}
		}
		return unreadable()
	}
	if t, _ := raw["type"].(string); t != "" && !a.Matches(t) {
		return ferr("type", "type %q does not belong to action %q", t, a.ID)
	}
	for _, f := range a.Fields {
		v, set := raw[f.Key]
		if !set || v == nil {
			if strict && f.Required {
				if _, fixed := a.Fixed[f.Key]; !fixed {
					return ferr(f.Key, "required for %s", a.Label)
				}
			}
			continue
		}
		if err := ValidateValue(f, v); err != nil {
			return err
		}
	}
	return unreadable()
}

// ValidateValue checks one value against a field schema.
func ValidateValue(f Field, v any) error {
	switch f.Kind {
	case "int", "number":
		n, ok := ToFloat(v)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) {
			return ferr(f.Key, "must be a number")
		}
		if f.Kind == "int" && n != math.Trunc(n) {
			return ferr(f.Key, "must be a whole number")
		}
		if f.Min != nil && n < *f.Min {
			return ferr(f.Key, "%v is below the minimum %v", n, *f.Min)
		}
		if f.Max != nil && n > *f.Max {
			return ferr(f.Key, "%v is above the maximum %v", n, *f.Max)
		}
	case "string", "combo":
		if _, ok := v.(string); !ok {
			return ferr(f.Key, "must be text")
		}
	case "key":
		s, ok := v.(string)
		if !ok {
			return ferr(f.Key, "must be text")
		}
		if err := ValidateKeySpec(s); err != nil {
			return ferr(f.Key, "%v", err)
		}
	case "enum":
		s, ok := v.(string)
		if !ok {
			return ferr(f.Key, "must be text")
		}
		for _, o := range f.Options {
			if o.Value == s {
				return nil
			}
		}
		return ferr(f.Key, "%q is not one of the allowed values", s)
	case "bool":
		if _, ok := v.(bool); !ok {
			return ferr(f.Key, "must be true or false")
		}
	case "stringList":
		l, ok := v.([]any)
		if !ok {
			return ferr(f.Key, "must be a list of text")
		}
		for _, e := range l {
			if _, ok := e.(string); !ok {
				return ferr(f.Key, "must be a list of text")
			}
		}
	case "object":
		m, ok := v.(map[string]any)
		if !ok {
			return ferr(f.Key, "must be an object")
		}
		if f.Key == "press" {
			if err := validatePress(m); err != nil {
				return err
			}
		}
		for _, g := range f.Group {
			if gv, set := m[g.Key]; set && gv != nil && f.Key != "press" {
				if err := ValidateValue(Field{Key: f.Key + "." + g.Key, Kind: g.Kind, Options: g.Options, Min: g.Min, Max: g.Max}, gv); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// validatePress checks a press object (config.PressAction): the type and the
// one parameter that type needs. Unknown extra keys pass.
func validatePress(m map[string]any) error {
	t, _ := m["type"].(string)
	switch t {
	case "none", "resetActiveWheel":
	case "key":
		s, _ := m["key"].(string)
		if s == "" {
			return ferr("press.key", "a shortcut is required when the press sends a key")
		}
		if ValidateKeySpec(s) != nil {
			return ferr("press.key", "invalid key spec %q", s)
		}
	case "click":
		if p, _ := m["point"].(string); p == "" {
			return ferr("press.point", "a calibrated point is required when the press clicks")
		}
	case "selectWheel":
		w, _ := m["wheel"].(string)
		switch w {
		case "lift", "gamma", "gain", "offset":
		case "":
			return ferr("press.wheel", "a wheel is required when the press selects a wheel")
		default:
			return ferr("press.wheel", "%q is not one of lift, gamma, gain, offset", w)
		}
	default:
		return ferr("press.type", "press type %q is not one of key, click, none, selectWheel, resetActiveWheel", t)
	}
	for _, k := range []string{"key", "point", "wheel"} {
		if v, set := m[k]; set && v != nil {
			if _, ok := v.(string); !ok {
				return ferr("press."+k, "must be text")
			}
		}
	}
	return nil
}

// holdMsField is the schema of the optional hold-to-fire field, checked for
// every action type (packs.go adds it to the key and shell actions).
var holdMsField = Field{Key: "holdMs", Label: "Hold to fire (ms)", Kind: "int", Min: f64(0), Max: f64(2000)}
