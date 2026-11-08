// Package preset is the per-app preset store of 2T5 DECK+: one JSON file per
// app (plus a Global fallback), raw-JSON-first so "key present = set" works
// and unknown keys round-trip, with per-value inheritance from Global,
// 200-deep undo/redo, atomic writes, a history directory and an mtime poll
// for external edits.
//
// Borrowed:
//   - atomic temp-file-in-same-dir + fsync + rename write: LD-RESOLVE
//     feature/mapping-editor-phase-a internal/editorstore/editorstore.go
//     (writeAtomic), reimplemented locally;
//   - the apps registry / bank naming (default, <bundle>, <bundle>-<mod>) and
//     "validate before persist" idea: DECKMAP internal/store/store.go and
//     internal/binding/cascade.go (c39fecd). Nothing imported across repos.
package preset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"2t5-deck-plus/internal/config"
)

// Well-known preset ids.
const (
	GlobalID  = "_global"
	ResolveID = "com.blackmagic-design.DaVinciResolve"
	FinderID  = "com.apple.finder"
)

// SchemaVersion is the preset file schema.
const SchemaVersion = 1

// Assignment is one control's raw binding object (config.Binding JSON plus an
// optional "action" alias). Values decoded with json.Number so text such as
// 80.0 survives a round trip.
type Assignment = map[string]any

// Preset is one app's file.
type Preset struct {
	Schema   int                              `json:"schema"`
	ID       string                           `json:"id"`
	Name     string                           `json:"name"`
	Packs    []string                         `json:"packs"`
	Banks    map[string]map[string]Assignment `json:"banks"`
	Settings map[string]any                   `json:"settings"`
	// Extra holds unknown top-level keys, kept verbatim.
	Extra map[string]any `json:"-"`
}

var idRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]*$`)
var controlRe = regexp.MustCompile(`^[A-Za-z0-9_]+$`)
var bankRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Modifier bank names of non-Resolve presets.
var modifiers = []string{"shift", "cmd", "alt", "ctrl", "fn"}

// ValidID reports whether id can be a preset id / file name.
func ValidID(id string) bool { return idRe.MatchString(id) && !strings.Contains(id, "..") }

// IsLegacyBank is true for presets that keep verbatim runtime bank names.
func usesLegacyBanks(id string) bool { return id == ResolveID }

// ValidBank checks a logical bank name for preset id.
func ValidBank(id, bank string) bool {
	if !bankRe.MatchString(bank) {
		return false
	}
	if usesLegacyBanks(id) {
		return true
	}
	if bank == "base" {
		return true
	}
	for _, m := range modifiers {
		if bank == m {
			return true
		}
	}
	if _, ok := config.ParseModeBank(bank); ok {
		return strings.HasPrefix(bank, "pm-") // the logical slider-mode banks
	}
	return bank == "layer-2" || bank == "layer-3"
}

// ValidBankControl is ValidBank plus the one control rule: a slider-mode bank
// (pm-hue, pm-sat, pm-lum; Resolve: col-pm-hue ...) takes only P1-P8 and
// P1btn-P8btn. Every other bank takes any control.
func ValidBankControl(id, bank, control string) bool {
	if !ValidBank(id, bank) {
		return false
	}
	if _, ok := config.ParseModeBank(bank); ok {
		return config.IsPControl(control)
	}
	return true
}

// ParsePreset decodes a preset file.
func ParsePreset(b []byte) (*Preset, error) {
	var top map[string]any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&top); err != nil {
		return nil, err
	}
	p := &Preset{Schema: SchemaVersion, Banks: map[string]map[string]Assignment{}, Settings: map[string]any{}, Extra: map[string]any{}}
	for k, v := range top {
		switch k {
		case "schema":
			if n, ok := v.(json.Number); ok {
				i, _ := n.Int64()
				p.Schema = int(i)
			}
		case "id":
			p.ID, _ = v.(string)
		case "name":
			p.Name, _ = v.(string)
		case "packs":
			l, ok := v.([]any)
			if !ok {
				return nil, fmt.Errorf("packs must be a list")
			}
			for _, e := range l {
				s, ok := e.(string)
				if !ok {
					return nil, fmt.Errorf("packs must be a list of text")
				}
				p.Packs = append(p.Packs, s)
			}
		case "banks":
			bm, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("banks must be an object")
			}
			for bn, bv := range bm {
				cm, ok := bv.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("bank %q must be an object", bn)
				}
				p.Banks[bn] = map[string]Assignment{}
				for cn, cv := range cm {
					am, ok := cv.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("assignment %s.%s must be an object", bn, cn)
					}
					p.Banks[bn][cn] = am
				}
			}
		case "settings":
			sm, ok := v.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("settings must be an object")
			}
			p.Settings = sm
		default:
			p.Extra[k] = v
		}
	}
	if p.ID == "" {
		return nil, fmt.Errorf("preset has no id")
	}
	if !ValidID(p.ID) {
		return nil, fmt.Errorf("bad preset id %q", p.ID)
	}
	return p, nil
}

func marshalNoEscape(v any) ([]byte, error) {
	var buf bytes.Buffer
	e := json.NewEncoder(&buf)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Marshal renders the preset: fixed top-level order, every nested object with
// sorted keys, two-space indent, trailing newline. Byte-stable: parsing the
// output and marshalling again gives identical bytes.
func (p *Preset) Marshal() ([]byte, error) {
	banks := p.Banks
	if banks == nil {
		banks = map[string]map[string]Assignment{}
	}
	settings := p.Settings
	if settings == nil {
		settings = map[string]any{}
	}
	packs := p.Packs
	if packs == nil {
		packs = []string{}
	}
	type kv struct {
		k string
		v any
	}
	fields := []kv{{"schema", p.Schema}, {"id", p.ID}, {"name", p.Name}, {"packs", packs}, {"banks", banks}, {"settings", settings}}
	extra := make([]string, 0, len(p.Extra))
	for k := range p.Extra {
		extra = append(extra, k)
	}
	sort.Strings(extra)
	for _, k := range extra {
		fields = append(fields, kv{k, p.Extra[k]})
	}
	var out bytes.Buffer
	out.WriteString("{")
	for i, f := range fields {
		if i > 0 {
			out.WriteString(",")
		}
		kb, _ := marshalNoEscape(f.k)
		vb, err := marshalNoEscape(f.v)
		if err != nil {
			return nil, err
		}
		out.Write(kb)
		out.WriteString(":")
		out.Write(vb)
	}
	out.WriteString("}")
	var ind bytes.Buffer
	if err := json.Indent(&ind, out.Bytes(), "", "  "); err != nil {
		return nil, err
	}
	ind.WriteString("\n")
	return ind.Bytes(), nil
}

// Clone deep-copies a preset.
func (p *Preset) Clone() *Preset {
	c := &Preset{Schema: p.Schema, ID: p.ID, Name: p.Name}
	c.Packs = append([]string(nil), p.Packs...)
	c.Banks = map[string]map[string]Assignment{}
	for bn, bm := range p.Banks {
		c.Banks[bn] = map[string]Assignment{}
		for cn, a := range bm {
			c.Banks[bn][cn] = cloneMap(a)
		}
	}
	c.Settings = cloneMap(p.Settings)
	c.Extra = cloneMap(p.Extra)
	return c
}

func cloneMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = cloneVal(v)
	}
	return out
}

func cloneVal(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cloneMap(x)
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = cloneVal(e)
		}
		return out
	}
	return v
}

// Assigned counts the controls assigned in the preset.
func (p *Preset) Assigned() int {
	n := 0
	for _, bm := range p.Banks {
		n += len(bm)
	}
	return n
}

// MarshalJSON gives the API shape (same as the file).
func (p *Preset) MarshalJSON() ([]byte, error) {
	b, err := p.Marshal()
	if err != nil {
		return nil, err
	}
	return bytes.TrimSpace(b), nil
}

// RuntimeBank maps a preset's logical bank to the runtime bank name used by
// the composed config.Config: Resolve verbatim; Global base -> "default",
// layer-n -> "default-n", mod -> "default-mod"; apps the same with the bundle
// id in place of "default".
func RuntimeBank(presetID, bank string) string {
	if usesLegacyBanks(presetID) {
		return bank
	}
	root := presetID
	if presetID == GlobalID {
		root = "default"
	}
	switch {
	case bank == "base":
		return root
	case strings.HasPrefix(bank, "layer-"):
		n, _ := strconv.Atoi(strings.TrimPrefix(bank, "layer-"))
		return layerBank(root, n)
	}
	return root + "-" + bank
}

// layerBank mirrors config.LayerBank (kept in sync by a test).
func layerBank(page string, n int) string {
	if n <= 1 {
		return page
	}
	return fmt.Sprintf("%s-%d", page, n)
}

// splitAppBank splits a runtime bank of an app ("com.x.y", "com.x.y-shift",
// "com.x.y-2") into app id and logical bank.
func splitAppBank(name string) (app, bank string) {
	for _, m := range config.PModes {
		if strings.HasSuffix(name, "-pm-"+m) {
			return strings.TrimSuffix(name, "-pm-"+m), "pm-" + m
		}
	}
	for _, m := range modifiers {
		if strings.HasSuffix(name, "-"+m) {
			return strings.TrimSuffix(name, "-"+m), m
		}
	}
	for _, n := range []string{"2", "3"} {
		if strings.HasSuffix(name, "-"+n) {
			return strings.TrimSuffix(name, "-"+n), "layer-" + n
		}
	}
	return name, "base"
}

// isAppBank: runtime bank names of apps are bundle ids and contain a dot;
// Resolve's legacy banks (col, edit-shift, col-fn ...) never do.
func isAppBank(name string) bool { return strings.Contains(name, ".") }
