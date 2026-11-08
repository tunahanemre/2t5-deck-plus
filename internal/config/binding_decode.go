package config

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// The engine reads a bindings document with encoding/json into Binding, and
// encoding/json matches field names CASE-INSENSITIVELY (and folds odd letters
// such as the Kelvin sign). Anything that judges an assignment from its raw map
// ("is it a shell command? does it send Cmd+Q?") must therefore not look at raw
// map keys at all: it must decode the way the engine does and read the struct.
// These helpers are that single path.

// DecodeBinding decodes one raw assignment into a Binding exactly as the engine
// does when it loads the composed bindings document.
func DecodeBinding(raw map[string]any) (Binding, error) {
	var b Binding
	data, err := json.Marshal(raw)
	if err != nil {
		return b, err
	}
	if err := json.Unmarshal(data, &b); err != nil {
		return Binding{}, err
	}
	return b, nil
}

// EffectiveType is the type the engine runs: Type, else the legacy Method,
// else "cdl".
func (b Binding) EffectiveType() string { return b.effectiveType() }

// NonCanonical is one key of a raw assignment that the engine's decoder would
// read as a known field although it is not spelled exactly like it.
type NonCanonical struct {
	Path      string // e.g. "Cmd" or "press.Key"
	Canonical string // e.g. "cmd"
}

func (n NonCanonical) Error() string {
	return fmt.Sprintf("field name %q is not the canonical spelling %q (the deck reads it as %q); write it exactly as %q", n.Path, n.Canonical, n.Canonical, n.Canonical)
}

// NonCanonicalKeys lists the keys of raw (and of nested objects such as press)
// that case-fold or unicode-fold to a known Binding field name without being
// that exact name. A stored preset must use canonical keys only, so the
// editor's reading and the engine's reading of it can never differ.
func NonCanonicalKeys(raw map[string]any) []NonCanonical {
	return nonCanonical(reflect.TypeOf(Binding{}), raw, "")
}

func jsonName(f reflect.StructField) string {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return ""
	}
	name := strings.Split(tag, ",")[0]
	if name == "" {
		name = f.Name
	}
	return name
}

func nonCanonical(t reflect.Type, raw map[string]any, prefix string) []NonCanonical {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	fields := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue
		}
		if n := jsonName(f); n != "" {
			fields[n] = f.Type
		}
	}
	var out []NonCanonical
	for k, v := range raw {
		if ft, exact := fields[k]; exact {
			if sub, ok := v.(map[string]any); ok {
				out = append(out, nonCanonical(ft, sub, prefix+k+".")...)
			}
			continue
		}
		for name, ft := range fields {
			if strings.EqualFold(k, name) { // the same fold encoding/json uses
				out = append(out, NonCanonical{Path: prefix + k, Canonical: prefix + name})
				if sub, ok := v.(map[string]any); ok {
					out = append(out, nonCanonical(ft, sub, prefix+name+".")...)
				}
				break
			}
		}
	}
	return out
}

// BindingFieldNames lists the canonical JSON field names of Binding (and
// "press.<name>" for its nested press object), for tests and tools.
func BindingFieldNames() []string {
	var out []string
	var walk func(t reflect.Type, prefix string)
	walk = func(t reflect.Type, prefix string) {
		for t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" {
				continue
			}
			n := jsonName(f)
			if n == "" {
				continue
			}
			out = append(out, prefix+n)
			ft := f.Type
			for ft.Kind() == reflect.Ptr {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				walk(ft, prefix+n+".")
			}
		}
	}
	walk(reflect.TypeOf(Binding{}), "")
	return out
}
