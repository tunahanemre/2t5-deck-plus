package preset

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"2t5-deck-plus/internal/actions"
)

// External-edit quarantine (wave O, P2-E). The presets folder is on a volume
// others can write to: anyone on the share could drop a preset file that runs a
// program, or delete one that holds the panic key. An outside change that would
// ADD or WORSEN risk (a program, a destructive key, an unknown type, a weaker
// guard, a lost or silencing panic key, Global defaults that worsen anything) is
// therefore NOT applied: the previous in-memory state stays, the file stays on
// disk untouched, and the change waits as a pending item until the user approves
// it (one undo step, after a backup) or rejects it (the file is restored from
// memory, after a copy of the rejected file to .backups/rejected-*). Outside
// edits that add no risk keep applying as before.

// PendingExternal is one quarantined outside change.
type PendingExternal struct {
	ID         string   `json:"id"`
	Preset     string   `json:"preset"`
	File       string   `json:"file"`
	Kind       string   `json:"kind"`    // change | removal
	Changes    []string `json:"changes"` // plain words, one per risk
	Risks      []string `json:"risks"`   // shell | destructive | unknown | guard-settings | panic-removed | panic-override | removal
	DetectedAt int64    `json:"detectedAt"`

	raw []byte
	p   *Preset
}

// maxPending bounds the pending list (and so the warnings built from it).
const maxPending = 50

// maxRejected is how many rejected-* copies .backups keeps.
const maxRejected = 30

// ErrNoPending: unknown (or already settled) pending id.
var ErrNoPending = errors.New("no such pending outside change")

// ErrConfirmNeeded: approving a risky outside change needs confirmRisk.
var ErrConfirmNeeded = errors.New("this outside change adds risk: confirm it explicitly (confirmRisk true) to apply it")

func pendingID(file string, raw []byte) string {
	sum := sha256.Sum256(append([]byte(file+"\x00"), raw...))
	return hex.EncodeToString(sum[:])[:12]
}

func riskPhrase(info effInfo) (string, string) {
	switch info.risk {
	case actions.RiskShell:
		return "run a program", "shell"
	case actions.RiskDestructive:
		return "send a destructive key press", "destructive"
	default:
		return "do something this editor cannot judge", "unknown"
	}
}

// externalRisk lists, in plain words, what applying p on top of ps would add or
// worsen. Empty = nothing risky.
func (s *Store) externalRisk(ps map[string]*Preset, p *Preset) (words, cats []string) {
	post := overlay(ps, map[string]*Preset{p.ID: p})
	pre, aft := effectiveRisks(ps), effectiveRisks(post)
	for _, k := range sortedRiskKeys(aft) {
		before, had := pre[k]
		if !worse(before, had, aft[k]) {
			continue
		}
		what, cat := riskPhrase(aft[k])
		name := k.preset
		if q := post[k.preset]; q != nil && q.Name != "" {
			name = q.Name
		}
		words = append(words, fmt.Sprintf("%s in %s (%s) would %s", k.control, name, k.bank, what))
		cats = append(cats, cat)
	}
	for _, c := range settingChanges(p.ID, ps[p.ID], p) {
		if c.Weakens {
			words = append(words, fmt.Sprintf("setting %s would %s", c.Key, c.Why))
			cats = append(cats, "guard-settings")
		}
	}
	if old := ps[p.ID]; old != nil {
		for _, bn := range sortedKeys(old.Banks) {
			for _, cn := range sortedKeys(old.Banks[bn]) {
				oa := old.Banks[bn][cn]
				if actions.EffectiveType(oa) != "disarm" {
					continue
				}
				na, ok := p.Banks[bn][cn]
				if !ok || actions.EffectiveType(na) != "disarm" || !bytes.Equal(marshalAny(oa), marshalAny(na)) {
					words = append(words, fmt.Sprintf("the panic key %s in %s (%s) would be removed or changed", cn, old.Name, bn))
					cats = append(cats, "panic-removed")
				}
			}
		}
	}
	// a new panic key that silences other assignments
	for _, bn := range sortedKeys(p.Banks) {
		for _, cn := range sortedKeys(p.Banks[bn]) {
			na := p.Banks[bn][cn]
			if actions.EffectiveType(na) != "disarm" {
				continue
			}
			if old := ps[p.ID]; old != nil {
				if oa, ok := old.Banks[bn][cn]; ok && bytes.Equal(marshalAny(oa), marshalAny(na)) {
					continue
				}
			}
			for _, qid := range sortedKeys(post) {
				for _, qb := range sortedKeys(post[qid].Banks) {
					if qa, ok := post[qid].Banks[qb][cn]; ok && actions.EffectiveType(qa) != "disarm" {
						words = append(words, fmt.Sprintf("a panic key on %s would silence %s (%s)", cn, post[qid].Name, qb))
						cats = append(cats, "panic-override")
					}
				}
			}
		}
	}
	return words, cats
}

func dedupe(l []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range l {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// quarantineLocked records an outside change as pending (replacing an older
// pending item for the same preset). Caller holds s.mu.
func (s *Store) quarantineLocked(file string, raw []byte, p *Preset, kind string, words, cats []string) PendingExternal {
	if s.pending == nil {
		s.pending = map[string]*PendingExternal{}
	}
	for id, old := range s.pending {
		if old.File == file {
			delete(s.pending, id)
		}
	}
	pe := &PendingExternal{ID: pendingID(file, raw), Preset: strings.TrimSuffix(file, ".json"), File: file, Kind: kind,
		Changes: dedupe(words), Risks: dedupe(cats), DetectedAt: s.opts.Now().UnixMilli(), raw: raw, p: p}
	s.pending[pe.ID] = pe
	for len(s.pending) > maxPending { // bounded: the oldest waiting item is dropped from the list (its file stays on disk)
		oldest := ""
		for id, o := range s.pending {
			if oldest == "" || o.DetectedAt < s.pending[oldest].DetectedAt || o.DetectedAt == s.pending[oldest].DetectedAt && id < oldest {
				oldest = id
			}
		}
		s.opts.Logf("preset: too many pending outside changes; %s dropped from the list", s.pending[oldest].File)
		delete(s.pending, oldest)
	}
	s.opts.Logf("preset: outside change to %s NOT applied (waiting for approval): %s", file, strings.Join(pe.Changes, "; "))
	return *pe
}

// clearPendingLocked forgets a pending item for file (it was settled or the file became harmless).
func (s *Store) clearPendingLocked(file string) {
	for id, old := range s.pending {
		if old.File == file {
			delete(s.pending, id)
		}
	}
}

// PendingExternal lists the quarantined outside changes, oldest first.
func (s *Store) PendingExternal() []PendingExternal {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []PendingExternal{}
	for _, pe := range s.pending {
		out = append(out, *pe)
	}
	sortPending(out)
	return out
}

func sortPending(l []PendingExternal) {
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && (l[j].DetectedAt < l[j-1].DetectedAt || l[j].DetectedAt == l[j-1].DetectedAt && l[j].ID < l[j-1].ID); j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}

// ApproveExternal applies a quarantined outside change as one undo step, after a
// backup. confirmRisk must be true (the change adds risk by definition).
func (s *Store) ApproveExternal(id string, confirmRisk bool) error {
	if !confirmRisk {
		return ErrConfirmNeeded
	}
	s.mu.Lock()
	pe := s.pending[id]
	s.mu.Unlock()
	if pe == nil {
		return fmt.Errorf("%w: %q", ErrNoPending, id)
	}
	return s.commitThen("Approve outside change to "+pe.Preset, func(ps map[string]*Preset) error {
		if s.pending[id] == nil {
			return fmt.Errorf("%w: %q", ErrNoPending, id)
		}
		if pe.Kind == "removal" {
			delete(ps, pe.Preset)
		} else {
			if err := admit(ps, pe.p); err != nil {
				return fmt.Errorf("the outside file no longer fits: %v", err)
			}
			ps[pe.p.ID] = pe.p.Clone()
		}
		if _, err := s.backupLocked("import"); err != nil {
			return fmt.Errorf("backup before approving failed, nothing applied: %w", err)
		}
		return nil
	}, func() { s.clearPendingLocked(pe.File) })
}

// RejectExternal throws a quarantined outside change away: the rejected file is
// copied to .backups/rejected-<preset>-<time>.json and the file is restored from
// the in-memory state (or removed when the preset is not in memory).
func (s *Store) RejectExternal(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.roErr(); err != nil {
		return err
	}
	pe := s.pending[id]
	if pe == nil {
		return fmt.Errorf("%w: %q", ErrNoPending, id)
	}
	dir := s.backupsDir()
	if err := os.Mkdir(dir, 0o755); err != nil && !os.IsExist(err) {
		return fmt.Errorf("could not keep a copy of the rejected file, nothing changed: %w", err)
	}
	if pe.Kind != "removal" {
		label := pe.Preset
		if len(label) > 60 { // ids can be ~230 characters; a file name cannot
			label = label[:60] + "-" + hashHex([]byte(pe.Preset))[:8]
		}
		name := fmt.Sprintf("rejected-%s-%s.json", label, s.opts.Now().Format("20060102-150405"))
		if err := WriteAtomic(filepath.Join(dir, name), pe.raw); err != nil {
			return fmt.Errorf("could not keep a copy of the rejected file, nothing changed: %w", err)
		}
		s.pruneRejected(dir)
	}
	path := filepath.Join(s.dir, pe.File)
	if mem := s.snap.presets[pe.Preset]; mem != nil {
		if err := s.writeLocked(mem, false); err != nil {
			return err
		}
	} else if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	} else {
		delete(s.files, pe.Preset)
	}
	delete(s.pending, id)
	return nil
}

// pruneRejected keeps the newest maxRejected rejected-* copies.
func (s *Store) pruneRejected(dir string) {
	es, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type rf struct {
		name string
		mod  int64
	}
	var l []rf
	for _, e := range es {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "rejected-") {
			if fi, err := e.Info(); err == nil {
				l = append(l, rf{e.Name(), fi.ModTime().UnixNano()})
			}
		}
	}
	sort.Slice(l, func(i, j int) bool {
		if l[i].mod != l[j].mod {
			return l[i].mod > l[j].mod
		}
		return l[i].name > l[j].name
	})
	for i := maxRejected; i < len(l); i++ {
		os.Remove(filepath.Join(dir, l[i].name))
	}
}
