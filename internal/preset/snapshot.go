package preset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"2t5-deck-plus/internal/actions"
	"2t5-deck-plus/internal/config"
)

// FieldValue is one effective field with its provenance:
// "app" (set in this preset), "global" (inherited from Global), "default"
// (registry default, display only).
type FieldValue struct {
	Value  any    `json:"value"`
	Source string `json:"source"`
}

// Effective is the resolved view of one (preset, bank, control).
type Effective struct {
	Source   string `json:"source"`   // "app" = the preset's own assignment, "global" = Global's
	PresetID string `json:"presetId"` // preset that owns the winning assignment
	Bank     string `json:"bank"`     // logical bank in that preset
	Control  string `json:"control"`
	Action   string `json:"action"`
	Summary  string `json:"summary"`
	// Risk: safe | keys | shell | destructive (what the effective assignment can do when it fires).
	Risk   string                `json:"risk"`
	Fields map[string]FieldValue `json:"fields"`
	// Raw is the merged assignment (own keys plus values filled from Global
	// and settings.actionDefaults; registry defaults are NOT filled in).
	Raw map[string]any `json:"raw"`
}

// EffectiveBinding is what the engine consumes.
type EffectiveBinding struct {
	Binding     config.Binding
	Source      string // "app" or "global"
	PresetID    string
	Bank        string // runtime bank name, e.g. com.google.Chrome-2 or default
	LogicalBank string
	Action      string
	FieldSource map[string]string
	// Raw is the merged raw assignment, including keys config.Binding does
	// not know yet (e.g. shell's cmd/args).
	Raw map[string]any
}

// Snapshot is an immutable view of every preset at one moment.
type Snapshot struct {
	Seq     int
	presets map[string]*Preset
	fold    map[string]string // lower-case id -> id, for case-insensitive bundle matching
	reg     *actions.Registry
	cache   sync.Map
}

func newSnapshot(seq int, ps map[string]*Preset) *Snapshot {
	fold := make(map[string]string, len(ps))
	for id := range ps {
		fold[strings.ToLower(id)] = id
	}
	return &Snapshot{Seq: seq, presets: ps, fold: fold, reg: actions.Default()}
}

// canon maps a bundle id from the outside (the frontmost app) to the stored
// preset id: exact match first, else the preset whose id differs only in letter
// case (macOS bundle ids are matched case-insensitively). Unknown ids come back
// unchanged.
func (s *Snapshot) canon(id string) string {
	if _, ok := s.presets[id]; ok {
		return id
	}
	if c, ok := s.fold[strings.ToLower(id)]; ok {
		return c
	}
	return id
}

// Preset returns one preset (do not mutate it).
func (s *Snapshot) Preset(id string) (*Preset, bool) {
	if id == "global" {
		id = GlobalID
	}
	p, ok := s.presets[id]
	return p, ok
}

// Presets lists all presets: Global, Resolve, then by name.
func (s *Snapshot) Presets() []*Preset {
	out := make([]*Preset, 0, len(s.presets))
	for _, p := range s.presets {
		out = append(out, p)
	}
	rank := func(p *Preset) int {
		switch p.ID {
		case GlobalID:
			return 0
		case ResolveID:
			return 1
		}
		return 2
	}
	sort.Slice(out, func(i, j int) bool {
		if ri, rj := rank(out[i]), rank(out[j]); ri != rj {
			return ri < rj
		}
		ni, nj := strings.ToLower(out[i].Name), strings.ToLower(out[j].Name)
		if ni != nj {
			return ni < nj
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *Snapshot) global() *Preset { return s.presets[GlobalID] }

func lookup(p *Preset, bank, control string) (Assignment, bool) {
	if p == nil {
		return nil, false
	}
	a, ok := p.Banks[bank][control]
	return a, ok
}

func actionDefaults(g *Preset, typ string) map[string]any {
	if g == nil {
		return nil
	}
	ad, _ := g.Settings["actionDefaults"].(map[string]any)
	m, _ := ad[typ].(map[string]any)
	return m
}

// Effective resolves (appID, bank, control) with per-control and per-value
// inheritance from Global. ok is false when nothing is assigned anywhere.
func (s *Snapshot) Effective(appID, bank, control string) (Effective, bool) {
	if appID == "global" {
		appID = GlobalID
	}
	appID = s.canon(appID)
	p := s.presets[appID]
	if p == nil {
		return Effective{}, false
	}
	g := s.global()
	own, hasOwn := lookup(p, bank, control)
	if !hasOwn {
		if appID == GlobalID || usesLegacyBanks(appID) {
			return Effective{}, false
		}
		ga, ok := lookup(g, bank, control)
		if !ok {
			return Effective{}, false
		}
		return s.build(GlobalID, bank, control, ga, "global"), true
	}
	var ginherit Assignment
	if appID != GlobalID && !usesLegacyBanks(appID) {
		if ga, ok := lookup(g, bank, control); ok && s.reg.Infer(ga).ID == s.reg.Infer(own).ID {
			ginherit = ga
		}
	}
	var defaults map[string]any
	if appID != GlobalID { // Global has no parent to inherit from
		defaults = actionDefaults(g, s.reg.Infer(own).Type)
	}
	return s.buildMerged(appID, bank, control, own, ginherit, defaults), true
}

func (s *Snapshot) build(owner, bank, control string, a Assignment, source string) Effective {
	act := s.reg.Infer(a)
	e := Effective{Source: source, PresetID: owner, Bank: bank, Control: control, Action: act.ID, Raw: cloneMap(a), Fields: map[string]FieldValue{}}
	s.fillFields(&e, act, a, source)
	e.Summary = summarize(act, e)
	e.Risk = s.reg.RiskOfRaw(e.Raw)
	return e
}

func (s *Snapshot) buildMerged(owner, bank, control string, own, ga Assignment, defaults map[string]any) Effective {
	act := s.reg.Infer(own)
	e := Effective{Source: "app", PresetID: owner, Bank: bank, Control: control, Action: act.ID, Raw: cloneMap(own), Fields: map[string]FieldValue{}}
	s.fillFields(&e, act, own, "app")
	for _, f := range act.Fields {
		if _, set := own[f.Key]; set {
			continue
		}
		if v, ok := ga[f.Key]; ok {
			e.Fields[f.Key] = FieldValue{v, "global"}
			e.Raw[f.Key] = cloneVal(v)
		} else if v, ok := defaults[f.Key]; ok {
			e.Fields[f.Key] = FieldValue{v, "global"}
			e.Raw[f.Key] = cloneVal(v)
		}
	}
	e.Summary = summarize(act, e)
	e.Risk = s.reg.RiskOfRaw(e.Raw)
	return e
}

// fillFields records own values and registry defaults for the rest.
func (s *Snapshot) fillFields(e *Effective, act actions.Action, own Assignment, ownSource string) {
	for _, f := range act.Fields {
		if v, ok := own[f.Key]; ok {
			e.Fields[f.Key] = FieldValue{v, ownSource}
		} else {
			e.Fields[f.Key] = FieldValue{f.Default, "default"}
		}
	}
	for k, v := range own { // unknown keys stay visible
		if _, known := e.Fields[k]; !known && k != "type" && k != "action" && k != "method" {
			e.Fields[k] = FieldValue{v, ownSource}
		}
	}
}

// ResolveFor resolves the binding for a frontmost app bundle, deck layer and
// control with the app rung order
//
//	<bundle>-<layer> -> <bundle> -> default-<layer> -> default
//
// (layer rungs only when layer > 1; layer suffix as config.LayerBank). Field
// values missing from the app assignment are filled from Global. For the
// Resolve bundle the app rungs hold nothing (Resolve keeps its legacy bank
// cascade inside the engine), so this yields Global's assignment, if any.
func (s *Snapshot) ResolveFor(bundle string, layer int, control string) (EffectiveBinding, bool) {
	return s.ResolveForMod(bundle, "", layer, control)
}

// ResolveForMod is ResolveFor with a held modifier overlay ("shift", "cmd",
// "alt", "ctrl", "fn"): the overlay banks <bundle>-<mod> then default-<mod>
// are tried first, before the normal rungs, mirroring the engine's
// resolveOverlayBinding.
func (s *Snapshot) ResolveForMod(bundle, mod string, layer int, control string) (EffectiveBinding, bool) {
	return s.ResolveForModeMod(bundle, mod, "", layer, control)
}

// ResolveForModeMod is ResolveForMod with the live slider mode ("hue", "sat",
// "lum"; "" = none). For a P1-P8 / P1btn-P8btn control the mode banks are two
// more rungs, between the held-modifier overlay and the layer/base rungs:
//
//	<mod> (app, global) -> pm-<mode> (app) -> layer-n (app) -> base (app)
//	  -> pm-<mode> (global) -> layer-n (global) -> base (global)
//
// so an app's own mode assignment beats its base, and Global's mode assignment
// beats Global's base but never an app's own base: the same order as layers.
// With no mode (or a control that is not a P control) the rungs are exactly
// those of ResolveForMod, so a preset without mode data resolves as before.
func (s *Snapshot) ResolveForModeMod(bundle, mod, pmode string, layer int, control string) (EffectiveBinding, bool) {
	bundle = s.canon(bundle)
	if !config.IsPControl(control) || !config.ValidPMode(pmode) {
		pmode = ""
	}
	key := fmt.Sprintf("%s|%s|%s|%d|%s", bundle, mod, pmode, layer, control)
	if v, ok := s.cache.Load(key); ok {
		r := v.(*cachedResolve)
		return r.eb, r.ok
	}
	eb, ok := s.resolveForMod(bundle, mod, pmode, layer, control)
	s.cache.Store(key, &cachedResolve{eb, ok})
	return eb, ok
}

type cachedResolve struct {
	eb EffectiveBinding
	ok bool
}

func (s *Snapshot) resolveForMod(bundle, mod, pmode string, layer int, control string) (EffectiveBinding, bool) {
	type rung struct{ preset, bank string }
	var rungs []rung
	hasApp := bundle != "" && bundle != GlobalID && s.presets[bundle] != nil
	add := func(app bool, bank string) {
		if app {
			if hasApp {
				rungs = append(rungs, rung{bundle, bank})
			}
		} else {
			rungs = append(rungs, rung{GlobalID, bank})
		}
	}
	if mod != "" {
		add(true, mod)
		add(false, mod)
	}
	if pmode != "" {
		add(true, config.PModeBank(pmode))
	}
	if layer > 1 {
		add(true, fmt.Sprintf("layer-%d", layer))
	}
	add(true, "base")
	if pmode != "" {
		add(false, config.PModeBank(pmode))
	}
	if layer > 1 {
		add(false, fmt.Sprintf("layer-%d", layer))
	}
	add(false, "base")
	for _, r := range rungs {
		e, ok := s.Effective(r.preset, r.bank, control)
		if !ok || e.PresetID != r.preset {
			continue // a global fall-through is its own, later rung
		}
		if eb, ok := toEffectiveBinding(e, r.preset, r.bank); ok {
			return eb, true
		}
	}
	return EffectiveBinding{}, false
}

func toEffectiveBinding(e Effective, presetID, logicalBank string) (EffectiveBinding, bool) {
	var b config.Binding
	raw, err := json.Marshal(e.Raw)
	if err != nil || json.Unmarshal(raw, &b) != nil {
		return EffectiveBinding{}, false
	}
	fs := map[string]string{}
	for k, fv := range e.Fields {
		fs[k] = fv.Source
	}
	src := "app"
	if presetID == GlobalID {
		src = "global"
	}
	return EffectiveBinding{Binding: b, Source: src, PresetID: presetID, Bank: RuntimeBank(presetID, logicalBank),
		LogicalBank: logicalBank, Action: e.Action, FieldSource: fs, Raw: e.Raw}, true
}

// ResolveLegacy resolves a control for DaVinci Resolve the way the engine's
// config.Cascade does (page = "edit", "col" ...: {page}-{layer}, col-{layer},
// {page}, col) over the Resolve preset's verbatim banks, then falls through to
// Global so no control is dead. Used by the dev simulator; the engine keeps its
// own cascade and only needs ResolveFor for the Global fall-through.
func (s *Snapshot) ResolveLegacy(page string, layer int, control string) (EffectiveBinding, bool) {
	var banks []string
	if layer > 1 {
		if page != "col" {
			banks = append(banks, layerBank(page, layer))
		}
		banks = append(banks, layerBank("col", layer))
	}
	if page != "col" {
		banks = append(banks, page)
	}
	banks = append(banks, "col")
	for _, bn := range banks {
		if e, ok := s.Effective(ResolveID, bn, control); ok {
			if eb, ok := toEffectiveBinding(e, ResolveID, bn); ok {
				return eb, true
			}
		}
	}
	return s.ResolveFor("", layer, control)
}

// ResolveLegacyMode is ResolveLegacy with the live slider mode: for a P control
// the Resolve mode banks ({page}-pm-{mode}, then col-pm-{mode}) are tried first,
// as the engine does; a miss walks ResolveLegacy unchanged.
func (s *Snapshot) ResolveLegacyMode(page, pmode string, layer int, control string) (EffectiveBinding, bool) {
	if config.IsPControl(control) && config.ValidPMode(pmode) {
		for _, root := range []string{page, "col"} {
			bn := config.ModeBankOf(root, pmode)
			if e, ok := s.Effective(ResolveID, bn, control); ok {
				if eb, ok := toEffectiveBinding(e, ResolveID, bn); ok {
					return eb, true
				}
			}
		}
	}
	return s.ResolveLegacy(page, layer, control)
}

// ComposeConfig builds the runtime config.Config: Resolve banks verbatim (and
// its settings as "_config"), every app's banks renamed to <bundle>,
// <bundle>-<mod>, <bundle>-<n>, Global as default, default-<mod>, default-<n>.
// App assignments are merged with Global field by field.
//
// config has no loader from bytes (only Load(devicePath, bindingsPath)), so
// the merged bindings JSON is written into a private temp dir, loaded with
// config.Load and removed again. config.go is not changed.
func (s *Snapshot) ComposeConfig(devicePath string) (*config.Config, error) {
	raw, err := s.ComposeBindingsJSON()
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "2t5-deck-plus-compose-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "bindings.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return nil, err
	}
	return config.Load(devicePath, path)
}

// ComposeBindingsJSON is the bindings.json that ComposeConfig loads.
func (s *Snapshot) ComposeBindingsJSON() ([]byte, error) {
	out := map[string]any{}
	if rp := s.presets[ResolveID]; rp != nil {
		cfg := cloneMap(rp.Settings)
		delete(cfg, "actionDefaults")
		out["_config"] = cfg
		for bn, bm := range rp.Banks {
			m := map[string]any{}
			for cn, a := range bm {
				m[cn] = a
			}
			out[bn] = m
		}
	}
	owner := map[string]string{}
	for bn := range out {
		owner[bn] = ResolveID
	}
	for _, id := range sortedKeys(s.presets) {
		p := s.presets[id]
		if usesLegacyBanks(p.ID) {
			continue
		}
		for _, bn := range sortedKeys(p.Banks) {
			bm := p.Banks[bn]
			rb := RuntimeBank(p.ID, bn)
			if _, clash := out[rb]; clash {
				return nil, fmt.Errorf("compose: runtime bank %q is produced twice (by %s and %s)", rb, owner[rb], p.ID)
			}
			owner[rb] = p.ID
			m := map[string]any{}
			for cn := range bm {
				e, ok := s.Effective(p.ID, bn, cn)
				if !ok {
					continue
				}
				m[cn] = e.Raw
			}
			out[rb] = m
		}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
