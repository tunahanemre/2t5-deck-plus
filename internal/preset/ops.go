package preset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"2t5-deck-plus/internal/actions"
)

// ErrNoSeed means there is no default to restore the preset to.
var ErrNoSeed = errors.New("no default (seed) available for this preset")

func (s *Store) seedDir() string { return filepath.Join(s.dir, ".seed") }

// writeSeedCopy keeps the first-seed Resolve preset in .seed/ (never overwritten).
func (s *Store) writeSeedCopy(p *Preset) {
	path := filepath.Join(s.seedDir(), p.ID+".json")
	if _, err := os.Stat(path); err == nil {
		return
	}
	if err := os.Mkdir(s.seedDir(), 0o755); err != nil && !os.IsExist(err) { // parent (presets dir) exists
		s.opts.Logf("preset: seed copy: %v", err)
		return
	}
	b, err := p.Marshal()
	if err == nil {
		err = WriteAtomic(path, b)
	}
	if err != nil {
		s.opts.Logf("preset: seed copy: %v", err)
	}
}

// resolveSeed is the Resolve default: the .seed/ copy, else (stores created
// before the copy existed) what seeding would produce from the seed sources.
func (s *Store) resolveSeed() (*Preset, error) {
	if b, err := os.ReadFile(filepath.Join(s.seedDir(), ResolveID+".json")); err == nil {
		if p, err := ParsePreset(b); err == nil {
			return p, nil
		}
	}
	ps, err := s.seed()
	if err == nil {
		for _, p := range ps {
			if p.ID == ResolveID {
				return p, nil
			}
		}
	}
	return nil, ErrNoSeed
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

// ---- copy ----------------------------------------------------------------

// CopySkip is one control a copy did not bring over.
type CopySkip struct {
	Bank    string `json:"bank"`
	Control string `json:"control"`
	Action  string `json:"action"`
	Reason  string `json:"reason"`
}

// CopyResult summarises a copy.
type CopyResult struct {
	Copied  int        `json:"copied"`
	Skipped []CopySkip `json:"skipped"`
	// Removed counts target controls a replace deletes (they are not in the source).
	Removed int `json:"removed"`
}

// CopyPlan is Copy as a dry run: it reports what Copy would do (copied, skipped
// with reasons, removed) and writes nothing, takes no undo step and makes no backup.
func (s *Store) CopyPlan(dstID, srcID, scope, bank, control, mode string, force bool) (CopyResult, error) {
	return s.copyImpl(true, dstID, srcID, scope, bank, control, mode, force)
}

// Copy copies assignments from preset srcID into dstID. scope is all, bank or
// control (bank / control name the part); mode merge keeps the target's other
// controls, replace makes the copied bank(s) equal to the source's. Actions
// whose pack the target lacks are skipped (and reported) unless force; skipped
// controls are left untouched in the target.
func (s *Store) Copy(dstID, srcID, scope, bank, control, mode string, force bool) (CopyResult, error) {
	return s.copyImpl(false, dstID, srcID, scope, bank, control, mode, force)
}

func (s *Store) copyImpl(dry bool, dstID, srcID, scope, bank, control, mode string, force bool) (CopyResult, error) {
	res := CopyResult{Skipped: []CopySkip{}}
	if mode == "" {
		mode = "merge"
	}
	if mode != "merge" && mode != "replace" {
		return res, &actions.FieldError{Field: "mode", Msg: `must be "merge" or "replace"`}
	}
	switch scope {
	case "all":
	case "bank":
		if bank == "" {
			return res, &actions.FieldError{Field: "bank", Msg: "bank is required for scope bank"}
		}
	case "control":
		if bank == "" || control == "" {
			return res, &actions.FieldError{Field: "control", Msg: "bank and control are required for scope control"}
		}
	default:
		return res, &actions.FieldError{Field: "scope", Msg: `must be "all", "bank" or "control"`}
	}
	if srcID == "" {
		return res, &actions.FieldError{Field: "from", Msg: "from is required"}
	}
	run := func(ps map[string]*Preset) error {
		res = CopyResult{Skipped: []CopySkip{}}
		dst, err := getPreset(ps, dstID)
		if err != nil {
			return err
		}
		src, err := getPreset(ps, srcID)
		if err != nil {
			return &actions.FieldError{Field: "from", Msg: err.Error()}
		}
		if src.ID == dst.ID {
			return &actions.FieldError{Field: "from", Msg: "cannot copy a preset onto itself"}
		}
		if usesLegacyBanks(src.ID) != usesLegacyBanks(dst.ID) {
			return &actions.FieldError{Field: "from", Msg: "DaVinci Resolve uses its own banks; copy between Resolve and other apps is not possible"}
		}
		var banks []string
		switch scope {
		case "all":
			set := map[string]bool{}
			for b := range src.Banks {
				set[b] = true
			}
			if mode == "replace" {
				for b := range dst.Banks {
					set[b] = true
				}
			}
			banks = sortedKeys(set)
		default:
			if err := checkBank(dst.ID, bank); err != nil {
				return err
			}
			banks = []string{bank}
		}
		if scope == "control" {
			if err := s.checkControl(control); err != nil {
				return err
			}
			if _, ok := src.Banks[bank][control]; !ok {
				return fmt.Errorf("%w: %s.%s in %s", ErrNoControl, bank, control, src.Name)
			}
		}
		for _, b := range banks {
			if !ValidBank(dst.ID, b) {
				res.Skipped = append(res.Skipped, CopySkip{Bank: b, Reason: "not a valid bank for the target"})
				continue
			}
			names := sortedKeys(src.Banks[b])
			keep := map[string]bool{} // target controls that must survive a replace
			for _, c := range names {
				if scope == "control" && c != control {
					continue
				}
				a := src.Banks[b][c]
				act := s.reg.Infer(a)
				if err := s.checkControl(c); err != nil {
					res.Skipped = append(res.Skipped, CopySkip{b, c, act.ID, "not a control of this deck"})
					keep[c] = true
					continue
				}
				if act.Pack != "" && !contains(dst.Packs, act.Pack) && !s.reg.PackAlways(act.Pack) && !force {
					res.Skipped = append(res.Skipped, CopySkip{b, c, act.ID, fmt.Sprintf("%s needs the %q pack, which %s does not have", act.Label, act.Pack, dst.Name)})
					keep[c] = true
					continue
				}
				if dst.Banks[b] == nil {
					dst.Banks[b] = map[string]Assignment{}
				}
				dst.Banks[b][c] = cloneMap(a)
				res.Copied++
			}
			if mode == "replace" && scope != "control" {
				for c := range dst.Banks[b] {
					if _, inSrc := src.Banks[b][c]; !inSrc && !keep[c] {
						delete(dst.Banks[b], c)
						res.Removed++
					}
				}
			}
			if len(dst.Banks[b]) == 0 && !usesLegacyBanks(dst.ID) {
				delete(dst.Banks, b)
			}
		}
		return nil
	}
	if dry {
		cur := s.Snapshot()
		ps := make(map[string]*Preset, len(cur.presets))
		for id, p := range cur.presets {
			ps[id] = p.Clone()
		}
		err := run(ps)
		return res, err
	}
	err := s.commit("Copy controls", run)
	return res, err
}

// ---- restore defaults ------------------------------------------------------

// ErrNoShippedPreset means an app-scope restore has nothing to restore to: the
// app was not created from a starter template and no shipped seed exists.
var ErrNoShippedPreset = errors.New("no shipped preset for this app")

// RestoreInfo says what a restore returned the app to.
type RestoreInfo struct {
	// Source: "resolve-seed" | "template" | "template-guess" (several templates
	// share the bundle id and none was recorded: the best overlap was chosen,
	// TemplateName says which) | "seed" | "builtin" (Global, empty)
	// | "none" (control/bank scope of an app with no shipped preset: the
	// control or bank is simply unassigned).
	Source       string `json:"source"`
	TemplateID   string `json:"templateId,omitempty"`
	TemplateName string `json:"templateName,omitempty"`
	// Controls is the number of assignments the app has after an app-scope restore.
	Controls int `json:"controls"`
}

// RestoreDefaults is RestoreDefaultsInfo without the report.
func (s *Store) RestoreDefaults(id, scope, bank, control string) error {
	_, err := s.RestoreDefaultsInfo(id, scope, bank, control)
	return err
}

// RestoreDefaultsInfo returns overrides to the shipped preset: the Resolve
// seed, the starter template an app was created from, or a seeded app preset
// (e.g. Finder). Global has no shipped bindings (empty). An app with no shipped
// preset: scope app fails with ErrNoShippedPreset (nothing is changed), control
// and bank scopes unassign. scope is control, bank or app; app also resets the
// preset's settings. Name and packs are never touched. One undo step.
func (s *Store) RestoreDefaultsInfo(id, scope, bank, control string) (*RestoreInfo, error) {
	switch scope {
	case "app":
	case "bank":
		if bank == "" {
			return nil, &actions.FieldError{Field: "bank", Msg: "bank is required for scope bank"}
		}
	case "control":
		if bank == "" || control == "" {
			return nil, &actions.FieldError{Field: "control", Msg: "bank and control are required for scope control"}
		}
	default:
		return nil, &actions.FieldError{Field: "scope", Msg: `must be "control", "bank" or "app"`}
	}
	var resolveSeed *Preset
	if id == ResolveID {
		var err error
		if resolveSeed, err = s.resolveSeed(); err != nil {
			return nil, err
		}
	}
	name := id
	if cur, ok := s.Snapshot().Preset(id); ok && cur != nil && cur.Name != "" {
		name = cur.Name
	}
	var label string
	switch scope {
	case "control":
		label = "Restore " + control + " defaults in " + name
	case "bank":
		label = "Restore bank " + bank + " defaults in " + name
	default:
		label = "Restore " + name + " defaults"
	}
	info := &RestoreInfo{Source: "none"}
	err := s.commit(label, func(ps map[string]*Preset) error {
		p, err := getPreset(ps, id)
		if err != nil {
			return err
		}
		if scope != "app" {
			if err := checkBank(p.ID, bank); err != nil {
				return err
			}
		}
		if scope == "control" {
			if err := s.checkControl(control); err != nil {
				return err
			}
		}
		var seed *Preset
		recordTpl := ""
		*info = RestoreInfo{Source: "none"}
		switch {
		case id == ResolveID:
			seed, info.Source = resolveSeed, "resolve-seed"
		case id == GlobalID:
			info.Source = "builtin"
		default:
			tpl, guessed := templateOf(p)
			if guessed && scope != "app" {
				tpl = nil // a guess only drives a whole-app restore
			}
			if tpl != nil {
				if seed, err = s.normTemplate(tpl); err != nil {
					return err
				}
				info.Source, info.TemplateID, info.TemplateName = "template", tpl.ID, tpl.Name
				if guessed {
					info.Source = "template-guess"
				}
				if scope == "app" {
					recordTpl = tpl.ID
				}
			} else if sp, _ := s.seedFor(id); sp != nil {
				seed, info.Source = sp, "seed"
			}
		}
		if scope == "app" && info.Source == "none" {
			return ErrNoShippedPreset
		}
		switch scope {
		case "control":
			if sa, ok := seed.pick(bank, control); ok {
				if p.Banks[bank] == nil {
					p.Banks[bank] = map[string]Assignment{}
				}
				p.Banks[bank][control] = sa
			} else {
				delete(p.Banks[bank], control)
			}
		case "bank":
			if seed != nil && len(seed.Banks[bank]) > 0 {
				p.Banks[bank] = cloneBank(seed.Banks[bank])
			} else {
				delete(p.Banks, bank)
			}
		case "app":
			p.Banks = map[string]map[string]Assignment{}
			p.Settings = map[string]any{}
			if seed != nil {
				sc := seed.Clone()
				p.Banks, p.Settings = sc.Banks, sc.Settings
			}
			if recordTpl != "" { // later restores are exact
				if p.Extra == nil {
					p.Extra = map[string]any{}
				}
				p.Extra[TemplateKey] = recordTpl
			}
			info.Controls = p.Assigned()
		}
		if scope != "app" && len(p.Banks[bank]) == 0 && !usesLegacyBanks(p.ID) {
			delete(p.Banks, bank)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return info, nil
}

func (p *Preset) pick(bank, control string) (Assignment, bool) {
	if p == nil {
		return nil, false
	}
	a, ok := p.Banks[bank][control]
	if !ok {
		return nil, false
	}
	return cloneMap(a), true
}

func cloneBank(b map[string]Assignment) map[string]Assignment {
	out := make(map[string]Assignment, len(b))
	for c, a := range b {
		out[c] = cloneMap(a)
	}
	return out
}

// ---- history ----------------------------------------------------------------

// HistoryEntry is one state in the undo timeline. Index k is "the state after
// k changes"; entry 0 is the start of the kept history.
type HistoryEntry struct {
	Index int       `json:"index"`
	Label string    `json:"label"`
	T     time.Time `json:"t"`
	Done  bool      `json:"done"` // applied in the current state
}

// History lists the timeline (oldest first) and the current index.
func (s *Store) History() ([]HistoryEntry, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []HistoryEntry{{Index: 0, Label: "Start of history", T: s.opened, Done: true}}
	for i, e := range s.undo {
		out = append(out, HistoryEntry{Index: i + 1, Label: e.label, T: e.t, Done: true})
	}
	n := len(s.undo)
	for k := 0; k < len(s.redo); k++ {
		e := s.redo[len(s.redo)-1-k]
		out = append(out, HistoryEntry{Index: n + 1 + k, Label: e.label, T: e.t})
	}
	return out, n
}

// JumpTo undoes or redoes to the state after index changes (one disk write).
// The result is the label of the last change stepped over ("" when already there).
func (s *Store) JumpTo(index int) (string, error) {
	s.mu.Lock()
	n := len(s.undo)
	if index < 0 || index > n+len(s.redo) {
		s.mu.Unlock()
		return "", &actions.FieldError{Field: "index", Msg: fmt.Sprintf("must be between 0 and %d", n+len(s.redo))}
	}
	if index == n {
		s.mu.Unlock()
		return "", nil
	}
	var label string
	var err error
	if index < n {
		label, err = s.travelLocked(true, n-index)
	} else {
		label, err = s.travelLocked(false, index-n)
	}
	if err != nil {
		s.mu.Unlock()
		return "", err
	}
	subs, snap := s.subsLocked()
	s.mu.Unlock()
	s.deliver(subs, snap)
	s.recheckRefused()
	return label, nil
}
