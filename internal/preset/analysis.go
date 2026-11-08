package preset

import (
	"fmt"
	"strings"

	"2t5-deck-plus/internal/actions"
)

// Read-only analysis of a snapshot: coverage, search and warnings. Nothing
// here writes, and nothing here may panic on odd data: every type assertion
// is checked, because presets come from files people edit.

var bankOrder = []string{"base", "shift", "cmd", "alt", "ctrl", "fn", "layer-2", "layer-3", "pm-hue", "pm-sat", "pm-lum"}

func orderBanks(set map[string]bool, legacy bool) []string {
	if legacy {
		return sortedKeys(set)
	}
	var out []string
	for _, b := range bankOrder {
		if set[b] {
			out = append(out, b)
			delete(set, b)
		}
	}
	return append(out, sortedKeys(set)...)
}

// ---- coverage ----------------------------------------------------------------

// CoverageControl is one control's state in one bank.
type CoverageControl struct {
	ID      string `json:"id"`
	State   string `json:"state"` // app | global | none
	Action  string `json:"action,omitempty"`
	Summary string `json:"summary,omitempty"`
	// Overridden (overrides lens): the app assigns this control itself AND it
	// differs from what Global has there, or Global has nothing there. Always
	// false for Global and for inherited / unassigned controls.
	Overridden bool `json:"overridden"`
	// ChangedFromDefault (Store.CoverageLens only): differs from the shipped
	// default (.seed): edited, added by the user, or removed by the user.
	ChangedFromDefault bool `json:"changedFromDefault"`
}

// CoverageBank counts one bank.
type CoverageBank struct {
	Bank       string            `json:"bank"`
	Assigned   int               `json:"assigned"`
	Inherited  int               `json:"inherited"`
	Unassigned int               `json:"unassigned"`
	Controls   []CoverageControl `json:"controls"`
}

// CoverageTotals sums the banks.
type CoverageTotals struct {
	Assigned   int `json:"assigned"`
	Inherited  int `json:"inherited"`
	Unassigned int `json:"unassigned"`
}

// Coverage is the answer of GET /api/coverage.
type Coverage struct {
	App    string         `json:"app"`
	Name   string         `json:"name"`
	Banks  []CoverageBank `json:"banks"`
	Totals CoverageTotals `json:"totals"`
}

// Coverage reports, per bank, which of controls (the deck's control ids, in
// display order) the app assigns itself, inherits from Global, or leaves
// unassigned. Banks: the app's and Global's banks plus base. For Resolve (own
// verbatim banks) a control counts as inherited when Global's base bank
// assigns it, because that is the fall-through its cascade ends in.
func (s *Snapshot) Coverage(appID string, controls []string) (Coverage, bool) {
	p, ok := s.Preset(appID)
	if !ok {
		return Coverage{}, false
	}
	cov := Coverage{App: p.ID, Name: p.Name, Banks: []CoverageBank{}}
	g := s.global()
	legacy := usesLegacyBanks(p.ID)
	set := map[string]bool{}
	for b := range p.Banks {
		set[b] = true
	}
	if !legacy {
		set["base"] = true
		if g != nil {
			for b := range g.Banks {
				set[b] = true
			}
		}
	}
	for _, b := range orderBanks(set, legacy) {
		cb := CoverageBank{Bank: b, Controls: []CoverageControl{}}
		for _, c := range controls {
			cc := CoverageControl{ID: c, State: "none"}
			if _, own := lookup(p, b, c); own {
				if e, ok := s.Effective(p.ID, b, c); ok {
					cc = CoverageControl{ID: c, State: "app", Action: e.Action, Summary: e.Summary}
					cc.Overridden = p.ID != GlobalID && s.overridesGlobal(p, b, c, e)
				}
			} else if legacy {
				if _, inh := lookup(g, "base", c); inh {
					if e, ok := s.Effective(GlobalID, "base", c); ok {
						cc = CoverageControl{ID: c, State: "global", Action: e.Action, Summary: e.Summary}
					}
				}
			} else if e, ok := s.Effective(p.ID, b, c); ok {
				cc = CoverageControl{ID: c, State: "global", Action: e.Action, Summary: e.Summary}
			}
			switch cc.State {
			case "app":
				cb.Assigned++
			case "global":
				cb.Inherited++
			default:
				cb.Unassigned++
			}
			cb.Controls = append(cb.Controls, cc)
		}
		cov.Totals.Assigned += cb.Assigned
		cov.Totals.Inherited += cb.Inherited
		cov.Totals.Unassigned += cb.Unassigned
		cov.Banks = append(cov.Banks, cb)
	}
	return cov, true
}

// overridesGlobal: the app's own control differs from Global's effective
// value in the same bank (Resolve: Global's base bank), or Global has none.
func (s *Snapshot) overridesGlobal(p *Preset, bank, control string, mine Effective) bool {
	gbank := bank
	if usesLegacyBanks(p.ID) {
		gbank = "base"
	}
	ge, ok := s.Effective(GlobalID, gbank, control)
	if !ok {
		return true
	}
	return !sameValue(map[string]any(mine.Raw), map[string]any(ge.Raw))
}

// CoverageLens is Coverage plus the shipped-default comparison: each control
// also says whether it differs from the .seed default of the app (see
// DefaultsDiff for the seed rules). One call draws the whole lens.
func (s *Store) CoverageLens(appID string, controls []string) (Coverage, bool) {
	snap := s.Snapshot()
	cov, ok := snap.Coverage(appID, controls)
	if !ok {
		return cov, false
	}
	seed, _ := s.seedFor(cov.App)
	for bi := range cov.Banks {
		bn := cov.Banks[bi].Bank
		for ci := range cov.Banks[bi].Controls {
			c := &cov.Banks[bi].Controls[ci]
			var sa, ca Assignment
			var sOK, cOK bool
			if seed != nil {
				sa, sOK = seed.Banks[bn][c.ID]
			}
			if p, ok := snap.Preset(cov.App); ok {
				ca, cOK = p.Banks[bn][c.ID]
			}
			switch {
			case sOK && cOK:
				c.ChangedFromDefault = len(diffMaps(sa, ca)) > 0
			case sOK != cOK:
				c.ChangedFromDefault = true
			}
		}
	}
	return cov, true
}

// ---- search --------------------------------------------------------------------

// Hit is one search result.
type Hit struct {
	App     string `json:"app"`
	AppName string `json:"appName"`
	Bank    string `json:"bank"`
	Control string `json:"control"`
	Action  string `json:"action"`
	Label   string `json:"label,omitempty"`
	Summary string `json:"summary"`
}

func stringValues(a Assignment) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			out = append(out, x)
		case map[string]any:
			for _, k := range sortedKeys(x) {
				walk(x[k])
			}
		case []any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(map[string]any(a))
	return out
}

// Search finds assignments of every preset whose action label, action id,
// summary, key specs, label text or app name contain every word of q
// (case-insensitive). At most limit hits; truncated says more existed.
func (s *Snapshot) Search(q string, limit int) (hits []Hit, truncated bool) {
	words := strings.Fields(strings.ToLower(q))
	hits = []Hit{}
	if len(words) == 0 {
		return
	}
	for _, p := range s.Presets() {
		for _, bn := range sortedKeys(p.Banks) {
			for _, cn := range sortedKeys(p.Banks[bn]) {
				a := p.Banks[bn][cn]
				act := s.reg.Infer(a)
				sum := ""
				if e, ok := s.Effective(p.ID, bn, cn); ok {
					sum = e.Summary
				}
				hay := strings.ToLower(strings.Join(append([]string{p.Name, p.ID, act.Label, act.ID, sum, cn, bn}, stringValues(a)...), "\n"))
				match := true
				for _, w := range words {
					if !strings.Contains(hay, w) {
						match = false
						break
					}
				}
				if !match {
					continue
				}
				if limit > 0 && len(hits) >= limit {
					return hits, true
				}
				lbl, _ := a["label"].(string)
				hits = append(hits, Hit{p.ID, p.Name, bn, cn, act.ID, lbl, sum})
			}
		}
	}
	return
}

// ---- warnings --------------------------------------------------------------------

// Warning is a hint about a preset. Never blocking.
type Warning struct {
	Severity string `json:"severity"` // warn | hint
	Kind     string `json:"kind"`
	App      string `json:"app,omitempty"`
	Bank     string `json:"bank,omitempty"`
	Control  string `json:"control,omitempty"`
	Field    string `json:"field,omitempty"`
	Message  string `json:"message"`
	// Suggest names the field that would fix it ("holdMs"); a hint to the UI,
	// never a requirement.
	Suggest string `json:"suggest,omitempty"`
}

type keyUse struct{ Control, Field, Spec string }

func normSpec(s string) string { return strings.ToLower(strings.ReplaceAll(s, " ", "")) }

// keySpecs lists the key specs an assignment sends (schema key fields, the
// usual key names, and press.key).
func (s *Snapshot) keySpecs(a Assignment) []keyUse {
	act := s.reg.Infer(a)
	names := map[string]bool{"key": true, "keyCW": true, "keyCCW": true}
	for _, f := range act.Fields {
		if f.Kind == "key" {
			names[f.Key] = true
		}
	}
	var out []keyUse
	for _, k := range sortedKeys(names) {
		if v, ok := a[k].(string); ok && v != "" {
			out = append(out, keyUse{Field: k, Spec: v})
		}
	}
	if pm, ok := a["press"].(map[string]any); ok {
		if t, _ := pm["type"].(string); t == "key" {
			if v, ok := pm["key"].(string); ok && v != "" {
				out = append(out, keyUse{Field: "press.key", Spec: v})
			}
		}
	}
	return out
}

// DuplicatesFor lists hints for the control's own key specs that another
// control in the same app and bank also sends.
func (s *Snapshot) DuplicatesFor(appID, bank, control string) []Warning {
	out := []Warning{}
	p, ok := s.Preset(appID)
	if !ok {
		return out
	}
	a, ok := p.Banks[bank][control]
	if !ok {
		return out
	}
	mine := s.keySpecs(a)
	for _, oc := range sortedKeys(p.Banks[bank]) {
		if oc == control {
			continue
		}
		for _, ou := range s.keySpecs(p.Banks[bank][oc]) {
			for _, mu := range mine {
				if normSpec(mu.Spec) == normSpec(ou.Spec) {
					out = append(out, Warning{Severity: "hint", Kind: "duplicate-key", App: p.ID, Bank: bank, Control: control, Field: mu.Field,
						Message: fmt.Sprintf("%s is also sent by %s", mu.Spec, oc)})
				}
			}
		}
	}
	return out
}

var skipOrphan = map[string]bool{"type": true, "action": true, "method": true, "label": true, "mode": true, "press": true}

// Warnings checks every preset. controlOK, when non-nil, says whether a
// control id exists on the deck.
func (s *Snapshot) Warnings(controlOK func(string) bool) []Warning {
	out := []Warning{}
	add := func(sev, kind string, p *Preset, bank, control, field, format string, args ...any) {
		out = append(out, Warning{Severity: sev, Kind: kind, App: p.ID, Bank: bank, Control: control, Field: field, Message: fmt.Sprintf(format, args...)})
	}
	for _, p := range s.Presets() {
		for _, bn := range sortedKeys(p.Banks) {
			if !ValidBank(p.ID, bn) {
				add("warn", "unknown-bank", p, bn, "", "", "%q is not a bank this app can use", bn)
			}
			bank := p.Banks[bn]
			specs := map[string][]keyUse{}
			for _, cn := range sortedKeys(bank) {
				a := bank[cn]
				if controlOK != nil && !controlOK(cn) {
					add("warn", "unknown-control", p, bn, cn, "", "%s is not a control of this deck", cn)
				}
				act := s.reg.Infer(a)
				id, _ := a["action"].(string)
				if id != "" && id != actions.OtherID {
					if _, known := s.reg.Get(id); !known {
						add("warn", "unknown-action", p, bn, cn, "action", "unknown action %q", id)
					}
				}
				if act.ID == actions.OtherID {
					if id == actions.OtherID {
						add("hint", "unknown-type", p, bn, cn, "type", "binding type %q is kept verbatim but not editable here", act.Type)
					} else {
						add("warn", "unknown-type", p, bn, cn, "type", "binding type %q is not known to this editor", act.Type)
					}
				} else {
					if t, _ := a["type"].(string); t != "" && !act.Matches(t) {
						add("warn", "type-mismatch", p, bn, cn, "type", "type %q does not belong to %s", t, act.Label)
					}
					for _, f := range act.Fields {
						v, set := a[f.Key]
						if !set || v == nil {
							if _, fixed := act.Fixed[f.Key]; f.Required && !fixed {
								add("warn", "missing-required", p, bn, cn, f.Key, "%s needs %s", act.Label, strings.ToLower(f.Label))
							}
							continue
						}
						if err := actions.ValidateValue(f, v); err != nil {
							kind := "invalid-value"
							if f.Kind == "key" {
								kind = "invalid-key"
							}
							add("warn", kind, p, bn, cn, f.Key, "%v", err)
						}
					}
					if pv, ok := a["press"]; ok {
						if _, inSchema := act.FieldByKey("press"); !inSchema {
							if err := actions.ValidateValue(actions.Field{Key: "press", Kind: "object"}, pv); err != nil {
								add("warn", "invalid-value", p, bn, cn, "press", "%v", err)
							}
						}
					}
					for _, k := range sortedKeys(a) {
						if skipOrphan[k] {
							continue
						}
						if _, known := act.FieldByKey(k); known {
							continue
						}
						if _, fixed := act.Fixed[k]; fixed {
							continue
						}
						add("hint", "orphaned-value", p, bn, cn, k, "%q is not a setting of %s and has no effect here", k, act.Label)
					}
					if (act.Type == "key" || act.Type == "shell") && actions.RiskOf(act.Type, a) == actions.RiskDestructive && !hasHold(a) {
						out = append(out, Warning{Severity: "hint", Kind: "destructive-no-hold", App: p.ID, Bank: bn, Control: cn, Field: "holdMs", Suggest: "holdMs",
							Message: fmt.Sprintf("%s closes, quits or deletes and fires on a tap: set \"Hold to fire\" so a short press does nothing", act.Label)})
					}
					if act.Unimplemented {
						add("warn", "unimplemented-action", p, bn, cn, "", "%s is staged (Labs) and does nothing yet", act.Label)
					}
					if act.Pack != "" && !contains(p.Packs, act.Pack) {
						add("hint", "pack-missing", p, bn, cn, "", "%s belongs to the %q pack, which %s does not list", act.Label, act.Pack, p.Name)
					}
				}
				for _, u := range s.keySpecs(a) {
					u.Control = cn
					specs[normSpec(u.Spec)] = append(specs[normSpec(u.Spec)], u)
				}
			}
			for _, k := range sortedKeys(specs) {
				uses := specs[k]
				ctls := map[string]bool{}
				for _, u := range uses {
					ctls[u.Control] = true
				}
				if len(ctls) < 2 {
					continue
				}
				names := sortedKeys(ctls)
				add("hint", "duplicate-key", p, bn, names[0], "", "%s is sent by %s", uses[0].Spec, strings.Join(names, ", "))
			}
		}
	}
	// The panic disarm key overrides everything else on its control, in every app
	// and bank (the engine checks it first). Say which assignments that silences.
	panic := map[string]string{} // control -> where it is the panic key
	for _, p := range s.Presets() {
		for _, bn := range sortedKeys(p.Banks) {
			for _, cn := range sortedKeys(p.Banks[bn]) {
				if actions.EffectiveType(p.Banks[bn][cn]) == "disarm" {
					if _, seen := panic[cn]; !seen {
						panic[cn] = p.Name + " / " + bn
					}
				}
			}
		}
	}
	if len(panic) > 0 {
		for _, p := range s.Presets() {
			for _, bn := range sortedKeys(p.Banks) {
				for _, cn := range sortedKeys(p.Banks[bn]) {
					where, ok := panic[cn]
					if !ok || actions.EffectiveType(p.Banks[bn][cn]) == "disarm" {
						continue
					}
					out = append(out, Warning{Severity: "warn", Kind: "panic-overrides", App: p.ID, Bank: bn, Control: cn,
						Message: fmt.Sprintf("%s is the panic disarm key (set in %s): it disarms the deck in every app, so this assignment never fires", cn, where)})
				}
			}
		}
	}
	return out
}

// Warnings checks the whole store: the snapshot plus preset files that could
// not be loaded.
func (s *Store) Warnings() []Warning {
	var ok func(string) bool
	if s.ctl != nil {
		ok = func(c string) bool { return s.ctl[c] }
	}
	out := s.Snapshot().Warnings(ok)
	for _, p := range s.ProblemList() {
		msg := fmt.Sprintf("%s could not be read and is ignored (the file is left untouched): %s", p.File, p.Error)
		switch {
		case p.Fallback:
			msg = fmt.Sprintf("%s could not be read: DaVinci Resolve is running on the shipped default (read-only) so it is not left without bindings. Restore the last good version or fix the file (left untouched): %s", p.File, p.Error)
		case p.CanRestore:
			msg += ". An earlier readable version exists: restore it with restore-last-good."
		}
		out = append(out, Warning{Severity: "error", Kind: "unreadable-file", Field: p.File, Message: msg})
	}
	if s.stateErr != nil {
		out = append(out, Warning{Severity: "warn", Kind: "state-dir",
			Message: "could not use the internal state folder (" + s.stateErr.Error() + "): the deck starts disarmed until it can. Check ~/Library/Application Support/2t5-deck-plus/state."})
	}
	if s.trustUnreadable {
		out = append(out, Warning{Severity: "warn", Kind: "state-dir",
			Message: "the internal trust record (manifest.json) could not be read, so outside edits made while the app was closed could not be checked: the deck starts disarmed (external-changes). Review your presets, then arm it."})
	}
	s.mu.Lock()
	notes := append([]string(nil), s.seedNotes...)
	pendings := make([]PendingExternal, 0, len(s.pending))
	for _, pe := range s.pending {
		pendings = append(pendings, *pe)
	}
	s.mu.Unlock()
	sortPending(pendings)
	for _, pe := range pendings {
		out = append(out, Warning{Severity: "warn", Kind: "pending-external", App: pe.Preset, Field: pe.File,
			Message: "an outside change to " + pe.File + " was NOT applied because it " + strings.Join(pe.Changes, "; ") + ". Approve or reject it in the editor."})
	}
	for _, n := range notes {
		out = append(out, Warning{Severity: "warn", Kind: "seed-fallback", Message: n})
	}
	if c := s.Canary(); c != nil {
		for _, l := range c.Lost {
			name := l.Preset
			if p, ok := s.Snapshot().Preset(l.Preset); ok && p.Name != "" {
				name = p.Name
			}
			msg := fmt.Sprintf("%s had %d assignments at the last check and has %d now", name, l.Before, l.After)
			if len(l.Controls) > 0 {
				shown := l.Controls
				if len(shown) > 6 {
					shown = shown[:6]
				}
				msg += " (gone: " + strings.Join(shown, ", ")
				if len(l.Controls) > len(shown) {
					msg += fmt.Sprintf(", +%d more", len(l.Controls)-len(shown))
				}
				msg += ")"
			}
			msg += "; no change made in the editor explains it. " + c.Hint
			out = append(out, Warning{Severity: "warn", Kind: "canary", App: l.Preset, Message: msg})
		}
	}
	return out
}

// hasHold reports whether an assignment has a usable holdMs (a number >= 1).
func hasHold(a Assignment) bool {
	f, ok := actions.ToFloat(a["holdMs"])
	return ok && f >= 1
}
