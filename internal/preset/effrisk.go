package preset

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"2t5-deck-plus/internal/actions"
	"2t5-deck-plus/internal/config"
)

// Import risk on the EFFECTIVE state (wave N). The deck does not run an
// assignment's own raw values: an app assignment inherits missing fields from
// Global (the same control's assignment, and settings.actionDefaults), so a
// bundle with Global defaults {key:{key:"Cmd+Q"}} and an app C1 {"type":"key"}
// runs Cmd+Q although neither assignment looks risky on its own. Every import
// is therefore judged on the state it would produce: the risk of every
// assignment of every preset, merged exactly as the runtime merges it, compared
// with the same assignment before the change.

type riskKey struct{ preset, bank, control string }

type effInfo struct {
	risk string
	raw  map[string]any
	sig  string // payload signature (see payloadSig)
}

// payloadSig is what an assignment would actually DO, read from the binding
// the engine decodes: its effective type, program and arguments, every key spec
// and whether it is delayed. Two assignments of the same risk CLASS can differ
// here (a shell with no program, then the same shell with "/bin/sh -c ..."), and
// that difference is exactly what an import must not slip in unseen.
func payloadSig(raw map[string]any) string {
	b, err := config.DecodeBinding(raw)
	if err != nil {
		return "undecodable"
	}
	var press string
	if b.Press != nil {
		press = b.Press.Type + ":" + b.Press.Key
	}
	return fmt.Sprintf("%s|%s|%q|%s|%s|%s|%s|%v", b.EffectiveType(), b.Cmd, b.Args, b.Key, b.KeyCW, b.KeyCCW, press, b.HoldMs > 0)
}

// effectiveRisks classifies every assignment of every preset as it would run.
func effectiveRisks(ps map[string]*Preset) map[riskKey]effInfo {
	snap := newSnapshot(0, ps)
	out := map[riskKey]effInfo{}
	for _, id := range sortedKeys(ps) {
		p := ps[id]
		for bn, bm := range p.Banks {
			for cn := range bm {
				if e, ok := snap.Effective(id, bn, cn); ok {
					out[riskKey{id, bn, cn}] = effInfo{risk: e.Risk, raw: e.Raw, sig: payloadSig(e.Raw)}
				}
			}
		}
	}
	return out
}

func dangerousRisk(r string) bool {
	return r == actions.RiskShell || r == actions.RiskDestructive || r == actions.RiskUnknown
}

// worse says whether after is a dangerous effective entry that is new or whose
// payload differs from what the same assignment was before: not just a worse
// risk CLASS, any change of what a dangerous entry does.
func worse(before effInfo, hadBefore bool, after effInfo) bool {
	if !dangerousRisk(after.risk) {
		return false
	}
	return !hadBefore || before.sig != after.sig
}

func overlay(cur, in map[string]*Preset) map[string]*Preset {
	m := make(map[string]*Preset, len(cur)+len(in))
	for id, p := range cur {
		m[id] = p
	}
	for id, p := range in {
		m[id] = p
	}
	return m
}

// InheritedRisk is a dangerous assignment whose danger comes from what it
// inherits (or from a setting), not from its own values.
type InheritedRisk struct {
	Preset  string `json:"preset"`
	Bank    string `json:"bank"`
	Control string `json:"control"`
	Risk    string `json:"risk"`
	Reason  string `json:"reason"`
}

// inheritReason names the fields an assignment gets from elsewhere.
func inheritReason(own Assignment, eff map[string]any) string {
	var parts []string
	for _, k := range sortedKeys(eff) {
		if _, has := own[k]; has {
			continue
		}
		switch k {
		case "key", "keyCW", "keyCCW", "cmd", "args", "press", "type", "method":
			parts = append(parts, fmt.Sprintf("%s=%v", k, eff[k]))
		}
	}
	if len(parts) == 0 {
		return "its effective values differ from its own"
	}
	return "inherits " + strings.Join(parts, ", ") + " from Global (the Global assignment or its settings.actionDefaults)"
}

// Settings that weaken a safety guard. Every key of config.TopLevel was audited
// (2026-11-04); only these two weaken a confirmation or a guard:
//
//	finderTagConfirmThreshold  the selection size above which a Finder tag press asks again; negative DISABLES the question, a larger number asks later
//	dodeeMostGuard             refuses edits while a DODEE / MOST project is open; false switches the guard off
//
// Everything else in TopLevel is a tuning value (debounce, scrub pixels, colours,
// HUD placement, startupReset ...). settings.actionDefaults changes are judged
// through their effect on the assignments (effectiveRisks), not here.
//
// Guards are judged on the DECODED config.TopLevel of the old and the new
// settings (the engine's own decoder: encoding/json, case- and fold-insensitive),
// never on raw map keys, so "DodeeMostGuard", "DODEEMOSTGUARD" and
// "dodeeMoſtGuard" weaken exactly what they weaken for the engine. Non-canonical
// spellings are refused outright (canonicalSettingsIssue) as well.
type guardSetting struct {
	key     string
	why     string
	weakens func(old, nu config.TopLevel) bool
}

const defaultFinderTagThreshold = 10

func threshold(tl config.TopLevel) int {
	if tl.FinderTagConfirmThreshold == 0 {
		return defaultFinderTagThreshold
	}
	return tl.FinderTagConfirmThreshold
}

func guardOn(tl config.TopLevel) bool { return tl.DodeeMostGuard == nil || *tl.DodeeMostGuard }

// lessSafeLower / lessSafeHigher: the setting's effective value moved towards less
// safe, measured against the safer of (old, default): a change that does not get
// back to at least what it was (or the default) needs confirmation.
func eff(v, def float64) float64 {
	if v == 0 {
		return def
	}
	return v
}

func lowerIsUnsafe(name string, old, nu float64) bool {
	def := config.EffectiveTopLevelDefault(name)
	return eff(nu, def) < math.Max(eff(old, def), def)
}

func higherIsUnsafe(name string, old, nu float64) bool {
	def := config.EffectiveTopLevelDefault(name)
	return eff(nu, def) > math.Min(eff(old, def), def)
}

var guardSettings = []guardSetting{
	{"finderTagConfirmThreshold", "asks less (or never) before tagging a big Finder selection", func(old, nu config.TopLevel) bool {
		o, n := threshold(old), threshold(nu)
		if o < 0 { // the question is already switched off: nothing here can weaken it further
			return false
		}
		return n < 0 || n > o || n > 1000
	}},
	{"dodeeMostGuard", "switches off the DODEE / MOST project guard", func(old, nu config.TopLevel) bool { return guardOn(old) && !guardOn(nu) }},
	{"maxScrubPx", "a larger scrub clamp lets one turn drag further", func(old, nu config.TopLevel) bool {
		return nu.MaxScrubPx < 0 && old.MaxScrubPx >= 0 || higherIsUnsafe("maxScrubPx", float64(old.MaxScrubPx), float64(nu.MaxScrubPx))
	}},
	{"pxPerUnit", "more pixels per step moves the pointer further per tick", func(old, nu config.TopLevel) bool {
		return higherIsUnsafe("pxPerUnit", old.PxPerUnit, nu.PxPerUnit)
	}},
	{"minScrubPx", "a smaller minimum lets tiny jitters scrub", func(old, nu config.TopLevel) bool {
		return lowerIsUnsafe("minScrubPx", old.MinScrubPx, nu.MinScrubPx)
	}},
	{"keyDebounceMs", "a shorter debounce gives less bounce protection on destructive keys", func(old, nu config.TopLevel) bool {
		return nu.KeyDebounceMs < 0 && old.KeyDebounceMs >= 0 || lowerIsUnsafe("keyDebounceMs", float64(old.KeyDebounceMs), float64(nu.KeyDebounceMs))
	}},
	{"fineFactor", "a larger fine factor makes fine mode coarser", func(old, nu config.TopLevel) bool {
		return higherIsUnsafe("fineFactor", old.FineFactor, nu.FineFactor)
	}},
	{"wheelSensMultipliers", "a larger wheel multiplier makes the wheel move further per click", func(old, nu config.TopLevel) bool {
		defs := map[string]float64{"lift": 1, "gamma": 1, "gain": 0.7, "offset": 0.5}
		for w, n := range nu.WheelSensMultipliers {
			def := defs[w]
			if def == 0 {
				def = 1
			}
			o := def
			if v, ok := old.WheelSensMultipliers[w]; ok {
				o = v
			}
			if n > math.Min(o, def) {
				return true
			}
		}
		return false
	}},
}

// SettingChange is one changed key of a preset's settings in an import.
type SettingChange struct {
	Preset  string `json:"preset"`
	Key     string `json:"key"`
	Old     any    `json:"old"`
	New     any    `json:"new"`
	Weakens bool   `json:"weakens"`
	Why     string `json:"why,omitempty"`
}

// settingChanges lists the settings keys that differ between old and nu. A
// changed key weakens a guard when the DECODED old and new settings say so for a
// guard whose field name the key folds to.
func settingChanges(id string, old, nu *Preset) []SettingChange {
	var oldS, newS map[string]any
	if old != nil {
		oldS = old.Settings
	}
	if nu != nil {
		newS = nu.Settings
	}
	oldTL, _ := config.DecodeTopLevel(oldS)
	newTL, newErr := config.DecodeTopLevel(newS)
	keys := map[string]bool{}
	for k := range oldS {
		keys[k] = true
	}
	for k := range newS {
		keys[k] = true
	}
	var out []SettingChange
	for _, k := range sortedKeys(keys) {
		ov, hadOld := oldS[k]
		nv, hasNew := newS[k]
		if hadOld == hasNew && bytes.Equal(marshalAny(ov), marshalAny(nv)) {
			continue
		}
		c := SettingChange{Preset: id, Key: k, Old: ov, New: nv}
		for _, g := range guardSettings {
			if strings.EqualFold(g.key, k) && (newErr != nil || g.weakens(oldTL, newTL)) {
				c.Weakens, c.Why = true, g.why
			}
		}
		out = append(out, c)
	}
	return out
}

// settingsIssue is "" when p's settings use canonical key spellings and every
// ranged value is inside its range; else it names the first offender. The
// engine's decoder would read a "Sens"-style variant just the same, and an
// absurd value would reach the engine's clamps, so both are refused.
func settingsIssue(p *Preset) *actions.FieldError {
	if p == nil || len(p.Settings) == 0 {
		return nil
	}
	if nc := config.NonCanonicalTopLevelKeys(p.Settings); len(nc) > 0 {
		return &actions.FieldError{Field: nc[0].Path, Msg: fmt.Sprintf("settings key %q is not the canonical spelling %q (the deck reads it as %q); write it exactly as %q", nc[0].Path, nc[0].Canonical, nc[0].Canonical, nc[0].Canonical)}
	}
	tl, err := config.DecodeTopLevel(p.Settings)
	if err != nil {
		return &actions.FieldError{Field: "settings", Msg: "the deck cannot read these settings: " + err.Error()}
	}
	if err := config.ValidateTopLevel(tl); err != nil {
		var re *config.RangeError
		if errors.As(err, &re) {
			return &actions.FieldError{Field: re.Field, Msg: re.Msg}
		}
		return &actions.FieldError{Field: "settings", Msg: err.Error()}
	}
	return nil
}

// effectiveRiskPass recomputes the risk numbers of a report from the effective
// post-change state and fills Inherited. files' own Risk counts are rebuilt:
// an assignment counts when it is added or changed in the bundle, or when its
// effective risk got worse because of something else that changed.
func (s *Store) effectiveRiskPass(r *ImportReport, b *Bundle, cur map[string]*Preset) {
	post := overlay(cur, b.Presets)
	pre, aft := effectiveRisks(cur), effectiveRisks(post)
	r.Inherited = []InheritedRisk{}
	extra := ImportRisk{}
	for i := range r.Files {
		f := &r.Files[i]
		f.Risk = ImportRisk{}
		f.RunsProgram = []string{}
		nu, old := b.Presets[f.ID], cur[f.ID]
		for _, bn := range sortedKeys(nu.Banks) {
			for _, cn := range sortedKeys(nu.Banks[bn]) {
				k := riskKey{f.ID, bn, cn}
				a := nu.Banks[bn][cn]
				rawChanged := true
				if old != nil {
					if oa, ok := old.Banks[bn][cn]; ok && bytes.Equal(marshalAny(oa), marshalAny(a)) {
						rawChanged = false
					}
				}
				after := aft[k]
				before, had := pre[k]
				if !rawChanged && !(had && before.sig != after.sig) {
					continue
				}
				countRisk(&f.Risk, after, bn, cn, &f.RunsProgram)
				if dangerousRisk(after.risk) && s.reg.RiskOfRaw(a) != after.risk {
					r.Inherited = append(r.Inherited, InheritedRisk{f.ID, bn, cn, after.risk, inheritReason(a, after.raw)})
				}
			}
		}
		noteMoreCommands(f)
	}
	// presets the bundle does not contain, whose assignments got worse (Global defaults changed)
	for _, k := range sortedRiskKeys(aft) {
		if _, inBundle := b.Presets[k.preset]; inBundle {
			continue
		}
		before, had := pre[k]
		if !worse(before, had, aft[k]) {
			continue
		}
		countRisk(&extra, aft[k], k.bank, k.control, nil)
		own := cur[k.preset].Banks[k.bank][k.control]
		r.Inherited = append(r.Inherited, InheritedRisk{k.preset, k.bank, k.control, aft[k].risk, inheritReason(own, aft[k].raw)})
	}
	r.Risk = extra
	for _, f := range r.Files {
		r.Risk = r.Risk.Add(f.Risk)
	}
}

func sortedRiskKeys(m map[riskKey]effInfo) []riskKey {
	out := make([]riskKey, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.preset != b.preset {
			return a.preset < b.preset
		}
		if a.bank != b.bank {
			return a.bank < b.bank
		}
		return a.control < b.control
	})
	return out
}

func countRisk(dst *ImportRisk, e effInfo, bn, cn string, runs *[]string) {
	if actions.EffectiveType(e.raw) == "disarm" {
		dst.Panic++
	}
	switch e.risk {
	case actions.RiskShell:
		dst.Shell++
		if runs != nil && len(*runs) < maxRunsProgram {
			*runs = append(*runs, bn+"."+cn+": "+commandLine(e.raw))
		}
	case actions.RiskDestructive:
		dst.Destructive++
	case actions.RiskKeys:
		dst.Keys++
	case actions.RiskUnknown:
		dst.Unknown++
	}
}

// neutralise is skipRisky's second stage: starting from presets that are free of
// risky raw assignments, drop whatever STILL makes an assignment worse once
// merged (the offending settings.actionDefaults entry when the danger is only
// inherited, otherwise the assignment itself) and restore guard settings that
// the bundle weakens. Everything it removes is reported.
func (s *Store) neutralise(out map[string]*Preset, cur map[string]*Preset, dropped []DroppedAssignment) []DroppedAssignment {
	// guard settings stay as they are now
	for _, id := range sortedKeys(out) {
		for _, c := range settingChanges(id, cur[id], out[id]) {
			if !c.Weakens {
				continue
			}
			if old := cur[id]; old != nil {
				if v, ok := old.Settings[c.Key]; ok {
					out[id].Settings[c.Key] = cloneVal(v)
				} else {
					delete(out[id].Settings, c.Key)
				}
			} else {
				delete(out[id].Settings, c.Key)
			}
			dropped = append(dropped, DroppedAssignment{id, "settings", c.Key, "settings"})
		}
	}
	// panic keys stay exactly as they are now
	for _, id := range sortedKeys(out) {
		old := cur[id]
		if old == nil {
			continue
		}
		for _, bn := range sortedKeys(old.Banks) {
			for _, cn := range sortedKeys(old.Banks[bn]) {
				oa := old.Banks[bn][cn]
				if actions.EffectiveType(oa) != "disarm" {
					continue
				}
				if na, ok := out[id].Banks[bn][cn]; ok && bytes.Equal(marshalAny(oa), marshalAny(na)) {
					continue
				}
				if out[id].Banks[bn] == nil {
					out[id].Banks[bn] = map[string]Assignment{}
				}
				out[id].Banks[bn][cn] = oa
				dropped = append(dropped, DroppedAssignment{id, bn, cn, "generic.disarm"})
			}
		}
	}
	pre := effectiveRisks(cur)
	for round := 0; round < 8; round++ {
		aft := effectiveRisks(overlay(cur, out))
		progressed := false
		for _, k := range sortedRiskKeys(aft) {
			before, had := pre[k]
			if !worse(before, had, aft[k]) {
				continue
			}
			p := out[k.preset]
			var own Assignment
			if p != nil {
				own = p.Banks[k.bank][k.control]
			} else if cp := cur[k.preset]; cp != nil {
				own = cp.Banks[k.bank][k.control]
			}
			// danger that is only inherited: remove the defaults entry that supplies it
			if g := out[GlobalID]; g != nil && own != nil && !dangerousRisk(s.reg.RiskOfRaw(own)) {
				typ := actions.EffectiveType(aft[k].raw)
				if ad, _ := g.Settings["actionDefaults"].(map[string]any); ad != nil {
					if _, has := ad[typ]; has {
						// keep the PREVIOUS entry (the payload the deck already had), else none
						if prev := actionDefaults(cur[GlobalID], typ); prev != nil {
							ad[typ] = cloneVal(prev)
						} else {
							delete(ad, typ)
						}
						dropped = append(dropped, DroppedAssignment{GlobalID, "settings", "actionDefaults." + typ, "settings"})
						progressed = true
						break
					}
				}
			}
			if p == nil {
				continue // not part of the bundle: nothing to drop
			}
			if old := cur[k.preset]; old != nil {
				if oa, ok := old.Banks[k.bank][k.control]; ok {
					p.Banks[k.bank][k.control] = oa
					dropped = append(dropped, DroppedAssignment{k.preset, k.bank, k.control, s.reg.Infer(aft[k].raw).ID})
					progressed = true
					break
				}
			}
			delete(p.Banks[k.bank], k.control)
			if len(p.Banks[k.bank]) == 0 {
				delete(p.Banks, k.bank)
			}
			dropped = append(dropped, DroppedAssignment{k.preset, k.bank, k.control, s.reg.Infer(aft[k].raw).ID})
			progressed = true
			break
		}
		if !progressed {
			break
		}
	}
	return dropped
}

// bankMapOrEmpty is the preset's banks, or an empty map for a missing preset.
func (p *Preset) bankMapOrEmpty() map[string]map[string]Assignment {
	if p == nil {
		return nil
	}
	return p.Banks
}
