package preset

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"

	"2t5-deck-plus/internal/actions"
)

// Plain-language summaries (wave I). Effective.Summary is what the editor shows
// under a control and in lists. It never carries an engine id: wheel names like
// lift_master, property names like CropTop, control ids like P1btn and raw
// arrays are turned into words here, while Effective.Fields and Raw keep the
// ids for anyone who needs them.

// summarize describes an effective assignment in one line.
func summarize(act actions.Action, e Effective) string {
	custom, _ := e.Raw["label"].(string)
	custom = strings.TrimSpace(custom)
	label := act.Label
	if custom != "" {
		label = custom
	}
	// 1. a shortcut: the label, and the key unless the label already says it
	if act.Type == "key" {
		if key, _ := e.Raw["key"].(string); key != "" {
			return labelWithKey(label, key)
		}
		return label
	}
	// 2. actions whose raw fields read badly as "name: value, value"
	if desc := describe(act, e); desc != "" {
		if custom != "" {
			return custom + ": " + desc
		}
		return desc
	}
	// 3. generic: the first two non-default fields, each in words
	parts := []string{}
	for _, f := range act.Fields {
		if f.Key == "label" {
			continue
		}
		fv, ok := e.Fields[f.Key]
		if !ok || fv.Source == "default" || fv.Value == nil {
			continue
		}
		str := fieldText(f, fv.Value, e)
		if str == "" {
			continue
		}
		parts = append(parts, str)
		if len(parts) == 2 {
			break
		}
	}
	if len(parts) == 0 {
		return label
	}
	return label + ": " + strings.Join(parts, ", ")
}

// labelWithKey is the summary of a shortcut: just the label when it equals or
// restates the key ("Delete" for Delete, "Enter" for Return), else "Label (key)".
func labelWithKey(label, key string) string {
	if restates(label, key) {
		return label
	}
	return label + " (" + key + ")"
}

var keySynonyms = map[string]string{"enter": "return", "esc": "escape", "del": "delete", "spacebar": "space", "backspace": "delete"}

func normKeyWord(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	w := b.String()
	if syn, ok := keySynonyms[w]; ok {
		return syn
	}
	return w
}

func restates(label, key string) bool {
	l := normKeyWord(label)
	return l != "" && (l == normKeyWord(key) || strings.EqualFold(strings.TrimSpace(label), strings.TrimSpace(key)))
}

// describe handles the actions whose fields are ids or index numbers.
func describe(act actions.Action, e Effective) string {
	raw := e.Raw
	sens := func() string {
		f, _ := act.FieldByKey("sens")
		v := fieldValue(e, "sens")
		if v == nil {
			return ""
		}
		return fieldText(f, v, e)
	}
	join := func(parts ...string) string {
		var out []string
		for _, p := range parts {
			if p != "" {
				out = append(out, p)
			}
		}
		return strings.Join(out, ", ")
	}
	switch act.Type {
	case "property":
		return join(propertyNames(raw), sens())
	case "wheel":
		w, _ := raw["wheel"].(string)
		if w == "" {
			return ""
		}
		return join(wheelName(w), sens())
	case "cdl":
		param, _ := raw["param"].(string)
		ch, _ := raw["ch"].(string)
		if param == "" {
			return ""
		}
		return join(cdlName(param, ch), sens())
	case "hueCurve":
		curve, _ := raw["curve"].(string)
		hue := 0
		if v, ok := actions.ToFloat(fieldValue(e, "hue")); ok {
			hue = int(v)
		}
		name := "hue"
		if hue >= 0 && hue < len(hueNames) {
			name = hueNames[hue]
		}
		label := "Hue vs Sat"
		if curve == "lum" {
			label = "Hue vs Lum"
		}
		return join(fmt.Sprintf("%s: %s (%d)", label, name, hue), sens())
	case "balance":
		hue := 0
		if v, ok := actions.ToFloat(fieldValue(e, "hue")); ok {
			hue = int(v)
		}
		name := "hue"
		if hue >= 0 && hue < len(hueNames) {
			name = hueNames[hue]
		}
		return join(fmt.Sprintf("Hue: %s (%d)", name, hue), sens())
	}
	return ""
}

var hueNames = []string{"red", "orange", "yellow", "green", "cyan", "blue", "purple", "magenta"}

func fieldValue(e Effective, key string) any {
	if fv, ok := e.Fields[key]; ok && fv.Value != nil {
		return fv.Value
	}
	return nil
}

// propertyNames: "Zoom X + Zoom Y" for a gang, "Crop Top" for one property.
func propertyNames(raw map[string]any) string {
	var names []string
	switch g := raw["gang"].(type) {
	case []any:
		for _, x := range g {
			if s, ok := x.(string); ok && s != "" {
				names = append(names, humanize(s))
			}
		}
	case []string:
		for _, s := range g {
			if s != "" {
				names = append(names, humanize(s))
			}
		}
	}
	if len(names) == 0 {
		if p, _ := raw["property"].(string); p != "" {
			names = []string{humanize(p)}
		}
	}
	return strings.Join(names, " + ")
}

// wheelName: lift_master -> "Lift master wheel", temp_slider -> "Temp slider".
func wheelName(id string) string {
	h := humanize(id)
	if strings.HasSuffix(strings.ToLower(h), "slider") {
		return h
	}
	return h + " wheel"
}

func cdlName(param, ch string) string {
	p := map[string]string{"offset": "Offset", "slope": "Slope", "power": "Power", "sat": "Saturation"}[param]
	if p == "" {
		p = humanize(param)
	}
	switch ch {
	case "r":
		return p + " red"
	case "g":
		return p + " green"
	case "b":
		return p + " blue"
	case "master", "":
		if param == "sat" {
			return p
		}
		return p + " master"
	}
	return p + " " + ch
}

var (
	camelRe = regexp.MustCompile(`([a-z0-9])([A-Z])`)
	idLike  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
)

// humanize turns an engine id into words: CropTop -> "Crop Top", ZoomX -> "Zoom X",
// lift_master -> "Lift master", P1btn -> "P1 button". Text that is not id-shaped
// (a path, a sentence) comes back unchanged.
func humanize(s string) string {
	if !idLike.MatchString(s) {
		return s
	}
	t := strings.ReplaceAll(s, "_", " ")
	t = camelRe.ReplaceAllString(t, "$1 $2")
	if strings.HasSuffix(t, "btn") && len(t) > 3 {
		t = strings.TrimSuffix(t, "btn") + " button"
		t = strings.ReplaceAll(t, "  ", " ")
	}
	// keep acronyms and single letters as they are; capitalise only the first letter
	if t != "" {
		t = strings.ToUpper(t[:1]) + t[1:]
	}
	return strings.TrimSpace(t)
}

// looksLikeID: only a value that is clearly an engine id gets humanised
// (snake_case, camelCase or a *btn control id), never plain words.
func looksLikeID(s string) bool {
	if !idLike.MatchString(s) {
		return false
	}
	return strings.Contains(s, "_") || camelRe.MatchString(s) || strings.HasSuffix(s, "btn")
}

// fieldText renders one field value in words, with its unit.
func fieldText(f actions.Field, v any, e Effective) string {
	switch f.Key {
	case "gamma":
		if g, ok := actions.ToFloat(v); ok {
			if g <= 1 {
				return fmt.Sprintf("steady (curve %s)", fixed1(g))
			}
			return fmt.Sprintf("accelerates (curve %s)", fixed1(g))
		}
	}
	switch x := v.(type) {
	case []any:
		var out []string
		for _, i := range x {
			out = append(out, humanizeIfID(fmt.Sprint(i)))
		}
		return strings.Join(out, " + ")
	case []string:
		var out []string
		for _, i := range x {
			out = append(out, humanizeIfID(i))
		}
		return strings.Join(out, " + ")
	case bool:
		return ""
	case string:
		if x == "" {
			return ""
		}
		if f.Kind == "enum" {
			for _, o := range f.Options {
				if o.Value == x {
					return o.Label
				}
			}
		}
		if f.Kind == "key" {
			return x
		}
		return humanizeIfID(x)
	}
	n, isNum := actions.ToFloat(v)
	if !isNum {
		return fmt.Sprint(v)
	}
	str := numText(n)
	if f.Unit != "" && (f.Kind == "int" || f.Kind == "number") {
		str += " " + unitFor(f.Unit, n)
	}
	return str
}

func humanizeIfID(s string) string {
	if looksLikeID(s) {
		return humanize(s)
	}
	return s
}

func numText(n float64) string {
	if math.IsNaN(n) || math.IsInf(n, 0) {
		return fmt.Sprint(n)
	}
	return strconv.FormatFloat(n, 'f', -1, 64)
}

func fixed1(n float64) string {
	s := strconv.FormatFloat(n, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

var pluralUnits = map[string]string{"frames": "frame", "lines": "line", "seconds": "second", "clicks": "click", "steps": "step", "units": "unit"}

// unitFor makes a unit read right for n: "frames" -> "frame" at 1, and the
// deck's "click" is the user's "notch" ("px per click" -> "px per notch").
func unitFor(unit string, n float64) string {
	words := strings.Fields(unit)
	for i, w := range words {
		if w == "per" && i+1 < len(words) && words[i+1] == "click" {
			words[i+1] = "notch"
		}
	}
	if n == 1 || n == -1 {
		// singularise the counted noun: the word before "per", else the last word
		idx := len(words) - 1
		for i, w := range words {
			if w == "per" {
				idx = i - 1
				break
			}
		}
		if idx >= 0 && idx < len(words) {
			if sing, ok := pluralUnits[words[idx]]; ok {
				words[idx] = sing
			}
		}
	}
	return strings.Join(words, " ")
}
