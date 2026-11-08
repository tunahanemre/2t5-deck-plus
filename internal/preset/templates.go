package preset

import (
	"embed"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
)

// Starter presets (wave D): a few apps with safe, ready-made bindings, shipped
// in templates/*.json. Each file is {templateId, name, bundleId, summary,
// notes, preset}; "preset" is an ordinary preset file. Nothing in a template
// closes, quits or deletes (a test pins that), and every assignment goes
// through the same Normalize + strict Validate an editor write does.

//go:embed templates/*.json templates/icons/*.png
var templatesFS embed.FS

// Template is one starter preset.
type Template struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	BundleID string `json:"bundleId"`
	Summary  string `json:"summary"`
	Notes    string `json:"notes,omitempty"`
	// Requires lists what must be set up outside 2T5 DECK+ for the template to
	// work (always an array), e.g. Blender's Emulate Numpad.
	Requires []Requirement `json:"requires"`
	Controls int           `json:"controls"`
	Packs    []string      `json:"packs"`

	preset *Preset
}

// Requirement is one outside-the-deck condition of a template.
type Requirement struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Help  string `json:"help"`
}

// ErrNoTemplate is returned for an unknown template id.
var ErrNoTemplate = fmt.Errorf("no such template")

var (
	tplOnce sync.Once
	tplList []*Template
	tplErr  error
)

func loadTemplates() {
	es, err := templatesFS.ReadDir("templates")
	if err != nil {
		tplErr = err
		return
	}
	for _, e := range es {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") { // templates/icons/ holds the bundled app icons
			continue
		}
		b, err := templatesFS.ReadFile("templates/" + e.Name())
		if err != nil {
			tplErr = err
			return
		}
		var raw struct {
			TemplateID string          `json:"templateId"`
			Name       string          `json:"name"`
			BundleID   string          `json:"bundleId"`
			Summary    string          `json:"summary"`
			Notes      string          `json:"notes"`
			Requires   []Requirement   `json:"requires"`
			Preset     json.RawMessage `json:"preset"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			tplErr = fmt.Errorf("template %s: %w", e.Name(), err)
			return
		}
		p, err := ParsePreset(raw.Preset)
		if err != nil {
			tplErr = fmt.Errorf("template %s: %w", e.Name(), err)
			return
		}
		if err := CheckAppID(p.ID); err != nil {
			tplErr = fmt.Errorf("template %s: %w", e.Name(), err)
			return
		}
		if raw.TemplateID+".json" != e.Name() || p.ID != raw.BundleID {
			tplErr = fmt.Errorf("template %s: ids do not match its name / bundle id", e.Name())
			return
		}
		req := raw.Requires
		if req == nil {
			req = []Requirement{}
		}
		for _, r := range req {
			if r.ID == "" || r.Label == "" {
				tplErr = fmt.Errorf("template %s: a requirement needs an id and a label", e.Name())
				return
			}
		}
		tplList = append(tplList, &Template{ID: raw.TemplateID, Name: raw.Name, BundleID: raw.BundleID,
			Summary: raw.Summary, Notes: raw.Notes, Requires: req, Controls: p.Assigned(), Packs: append([]string(nil), p.Packs...), preset: p})
	}
	sort.Slice(tplList, func(i, j int) bool { return strings.ToLower(tplList[i].Name) < strings.ToLower(tplList[j].Name) })
}

// TemplateIconPNG returns the bundled icon (256 px PNG, templates/icons/<templateId>.png) of the first
// template whose bundle id is bundleID, or nil. It is the fallback for an app that is not installed on
// this Mac, so its tile is still the real icon: the real installed icon always wins.
func TemplateIconPNG(bundleID string) []byte {
	tplOnce.Do(loadTemplates)
	for _, t := range tplList {
		if t.BundleID != bundleID {
			continue
		}
		if b, err := templatesFS.ReadFile("templates/icons/" + t.ID + ".png"); err == nil && len(b) > 0 {
			return b
		}
	}
	return nil
}

// Templates lists the starter presets, by name.
func Templates() []Template {
	tplOnce.Do(loadTemplates)
	out := make([]Template, 0, len(tplList))
	for _, t := range tplList {
		out = append(out, *t)
	}
	return out
}

// TemplatesError reports a broken embedded template (a build bug; tests fail on it).
func TemplatesError() error { tplOnce.Do(loadTemplates); return tplErr }

// AddFromTemplate creates the app preset of a template as one undo step. The
// assignments are normalised and validated like an editor write. ErrExists
// when a preset for the template's bundle id already exists (nothing is merged
// into it), ErrNoTemplate for an unknown id.
func (s *Store) AddFromTemplate(id string) (*Preset, *Template, error) {
	tplOnce.Do(loadTemplates)
	if tplErr != nil {
		return nil, nil, tplErr
	}
	var tpl *Template
	for _, t := range tplList {
		if t.ID == id {
			tpl = t
		}
	}
	if tpl == nil {
		return nil, nil, fmt.Errorf("%w: %q", ErrNoTemplate, id)
	}
	var created *Preset
	err := s.commit("Add app "+tpl.Name+" (template)", func(ps map[string]*Preset) error {
		if ps[tpl.BundleID] != nil {
			return fmt.Errorf("%w: %q", ErrExists, tpl.BundleID)
		}
		if other := foldConflict(ps, tpl.BundleID); other != "" {
			return fmt.Errorf("%w: %q differs from the existing %q only in letter case", ErrExists, tpl.BundleID, other)
		}
		p, err := s.normTemplate(tpl)
		if err != nil {
			return err
		}
		if p.Extra == nil {
			p.Extra = map[string]any{}
		}
		p.Extra[TemplateKey] = tpl.ID // so Restore defaults knows which template shipped this app
		created = p
		ps[p.ID] = p
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return created, tpl, nil
}

// TemplateKey is the top-level preset key recording which starter template an
// app was created from (kept verbatim like any unknown key).
const TemplateKey = "templateId"

// normTemplate clones the template's preset with every assignment normalised
// and strictly validated, exactly as an editor write would.
func (s *Store) normTemplate(tpl *Template) (*Preset, error) {
	p := tpl.preset.Clone()
	for bn, bm := range p.Banks {
		if err := checkBank(p.ID, bn); err != nil {
			return nil, err
		}
		for cn, a := range bm {
			if err := s.checkControl(cn); err != nil {
				return nil, err
			}
			na, err := s.reg.Normalize(a)
			if err != nil {
				return nil, fmt.Errorf("template %s %s.%s: %w", tpl.ID, bn, cn, err)
			}
			if err := s.reg.Validate(na, true); err != nil {
				return nil, fmt.Errorf("template %s %s.%s: %w", tpl.ID, bn, cn, err)
			}
			p.Banks[bn][cn] = na
		}
	}
	return p, nil
}

// templateOf finds the template an app was created from: the recorded
// templateId, else (apps added before the id was recorded) the template for its
// bundle id. When several share the bundle id (Chrome) the one whose
// assignments overlap the app's current ones most is chosen (ties: first in the
// stable template order) and guessed is true. nil when no template exists.
func templateOf(p *Preset) (tpl *Template, guessed bool) {
	tplOnce.Do(loadTemplates)
	if id, ok := p.Extra[TemplateKey].(string); ok && id != "" {
		for _, t := range tplList {
			if t.ID == id {
				return t, false
			}
		}
		return nil, false
	}
	var cands []*Template
	for _, t := range tplList {
		if t.BundleID == p.ID {
			cands = append(cands, t)
		}
	}
	switch len(cands) {
	case 0:
		return nil, false
	case 1:
		return cands[0], false
	}
	best, bestScore := cands[0], -1
	for _, t := range cands {
		if sc := overlapScore(p, t.preset); sc > bestScore {
			best, bestScore = t, sc
		}
	}
	return best, true
}

// overlapScore counts cells assigned in both presets (1) and identical (+2).
func overlapScore(a, b *Preset) int {
	n := 0
	for bn, bm := range b.Banks {
		for cn, ba := range bm {
			if aa, ok := a.Banks[bn][cn]; ok {
				n++
				if reflect.DeepEqual(map[string]any(aa), map[string]any(ba)) {
					n += 2
				}
			}
		}
	}
	return n
}
