package preset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"

	"2t5-deck-plus/internal/actions"
)

// Defaults diff (wave C, read-only first step of the three-way merge of
// shipped defaults and user changes; research report item 8). It compares the
// shipped seed of a preset with its current state and writes nothing.
//
// Seed per preset: Resolve = the .seed/ copy (else what seeding would
// produce); an app the seed sources define (e.g. Finder) = that seeded
// preset; Global = the empty built-in; any other app = an empty baseline
// (SeedSource "none"), so everything in it reads as added.

// FieldDiff is one differing value of a control (or setting).
type FieldDiff struct {
	Key     string `json:"key"`
	Change  string `json:"change"` // added | changed | removed
	Seed    any    `json:"seed,omitempty"`
	Current any    `json:"current,omitempty"`
}

// ControlDiff is one control that differs from the seed.
type ControlDiff struct {
	Bank       string      `json:"bank"`
	Control    string      `json:"control"`
	Change     string      `json:"change"` // added | changed | removed
	SeedAction string      `json:"seedAction,omitempty"`
	Action     string      `json:"action,omitempty"`
	Fields     []FieldDiff `json:"fields"`
}

// DiffCounts summarises a DefaultsDiff.
type DiffCounts struct {
	Added     int `json:"added"`
	Changed   int `json:"changed"`
	Removed   int `json:"removed"`
	Unchanged int `json:"unchanged"`
}

// DefaultsDiff is the result of Store.DefaultsDiff.
type DefaultsDiff struct {
	App        string `json:"app"`
	Name       string `json:"name"`
	HasSeed    bool   `json:"hasSeed"`
	SeedSource string `json:"seedSource"` // seed-file | rebuilt | builtin | template | none
	// TemplateID / TemplateName name the standard preset compared against
	// (SeedSource "template"); empty otherwise.
	TemplateID   string        `json:"templateId,omitempty"`
	TemplateName string        `json:"templateName,omitempty"`
	Counts       DiffCounts    `json:"counts"`
	Controls     []ControlDiff `json:"controls"`
	Settings     []FieldDiff   `json:"settings"`
}

// seedFor returns the shipped default of preset id and where it came from.
func (s *Store) seedFor(id string) (*Preset, string) {
	if id == ResolveID {
		if p, err := s.resolveSeed(); err == nil {
			return p, "rebuilt" // resolveSeed prefers .seed/, else rebuilds
		}
		return nil, "none"
	}
	if id == GlobalID {
		return newGlobal(), "builtin"
	}
	if ps, err := s.seed(); err == nil {
		for _, p := range ps {
			if p.ID == id {
				return p, "seed-file"
			}
		}
	}
	return nil, "none"
}

// DefaultsDiff compares preset id with its shipped default. ErrNoPreset when
// the preset does not exist.
func (s *Store) DefaultsDiff(id string) (*DefaultsDiff, error) {
	cur, ok := s.Snapshot().Preset(id)
	if !ok {
		return nil, ErrNoPreset
	}
	seed, src := s.seedFor(id)
	if id == ResolveID && src == "rebuilt" {
		if _, err := readSeedCopy(s.seedDir(), id); err == nil {
			src = "seed-file"
		}
	}
	// An app that came from a standard (starter) template is compared with that
	// template, the same source Restore defaults returns it to. The template is
	// embedded and read-only, so the user's edits can never change it.
	var tplID, tplName string
	if id != ResolveID && id != GlobalID {
		// Only an app that records its template (AddFromTemplate does): an app
		// the user built by hand is not compared with a template it merely shares
		// a bundle id with.
		if rec, _ := cur.Extra[TemplateKey].(string); rec != "" {
			if tpl, _ := templateOf(cur); tpl != nil {
				if tp, err := s.normTemplate(tpl); err == nil {
					seed, src = tp, "template"
					tplID, tplName = tpl.ID, tpl.Name
				}
			}
		}
	}
	d := &DefaultsDiff{App: id, Name: cur.Name, HasSeed: seed != nil, SeedSource: src, TemplateID: tplID, TemplateName: tplName,
		Controls: []ControlDiff{}, Settings: []FieldDiff{}}
	if seed == nil {
		seed = &Preset{Banks: map[string]map[string]Assignment{}, Settings: map[string]any{}}
	}
	banks := map[string]bool{}
	for b := range cur.Banks {
		banks[b] = true
	}
	for b := range seed.Banks {
		banks[b] = true
	}
	for _, bn := range sortedKeys(banks) {
		controls := map[string]bool{}
		for c := range cur.Banks[bn] {
			controls[c] = true
		}
		for c := range seed.Banks[bn] {
			controls[c] = true
		}
		for _, cn := range sortedKeys(controls) {
			sa, sOK := seed.Banks[bn][cn]
			ca, cOK := cur.Banks[bn][cn]
			cd := ControlDiff{Bank: bn, Control: cn, Fields: []FieldDiff{}}
			switch {
			case sOK && !cOK:
				cd.Change, cd.SeedAction = "removed", actionID(sa)
				cd.Fields = diffMaps(sa, nil)
				d.Counts.Removed++
			case !sOK && cOK:
				cd.Change, cd.Action = "added", actionID(ca)
				cd.Fields = diffMaps(nil, ca)
				d.Counts.Added++
			default:
				fs := diffMaps(sa, ca)
				if len(fs) == 0 {
					d.Counts.Unchanged++
					continue
				}
				cd.Change, cd.SeedAction, cd.Action, cd.Fields = "changed", actionID(sa), actionID(ca), fs
				d.Counts.Changed++
			}
			d.Controls = append(d.Controls, cd)
		}
	}
	d.Settings = diffMaps(seed.Settings, cur.Settings)
	return d, nil
}

func actionID(a Assignment) string {
	return actions.Default().Infer(a).ID
}

// diffMaps lists the keys whose values differ, in key order. Numbers compare
// by value ("80" and "80.0" are equal); everything else by canonical JSON.
func diffMaps(seed, cur map[string]any) []FieldDiff {
	keys := map[string]bool{}
	for k := range seed {
		keys[k] = true
	}
	for k := range cur {
		keys[k] = true
	}
	out := []FieldDiff{}
	for _, k := range sortedKeys(keys) {
		sv, sOK := seed[k]
		cv, cOK := cur[k]
		switch {
		case sOK && !cOK:
			out = append(out, FieldDiff{Key: k, Change: "removed", Seed: sv})
		case !sOK && cOK:
			out = append(out, FieldDiff{Key: k, Change: "added", Current: cv})
		case !sameValue(sv, cv):
			out = append(out, FieldDiff{Key: k, Change: "changed", Seed: sv, Current: cv})
		}
	}
	return out
}

func sameValue(a, b any) bool {
	if fa, ok := actions.ToFloat(a); ok {
		if fb, ok := actions.ToFloat(b); ok {
			return fa == fb
		}
	}
	ja, ea := canon(a)
	jb, eb := canon(b)
	if ea != nil || eb != nil {
		return reflect.DeepEqual(a, b)
	}
	return bytes.Equal(ja, jb)
}

// canon marshals v with sorted keys, normalising numbers to float values so
// nested "80" vs "80.0" compare equal too.
func canon(v any) ([]byte, error) {
	return json.Marshal(normNumbers(v))
}

func normNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, x := range t {
			m[k] = normNumbers(x)
		}
		return m
	case []any:
		l := make([]any, len(t))
		for i, x := range t {
			l[i] = normNumbers(x)
		}
		return l
	case json.Number:
		if f, err := t.Float64(); err == nil {
			return f
		}
	}
	return v
}

// readSeedCopy reports whether the .seed/ file for id exists and parses.
func readSeedCopy(dir, id string) (*Preset, error) {
	b, err := os.ReadFile(filepath.Join(dir, id+".json"))
	if err != nil {
		return nil, err
	}
	p, err := ParsePreset(b)
	if err != nil {
		return nil, fmt.Errorf("seed copy: %w", err)
	}
	return p, nil
}
