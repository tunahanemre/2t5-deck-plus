package preset

import (
	"2t5-deck-plus/internal/config"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// DefaultLiveResolvePath is the live ld-resolve config (read-only here).
func DefaultLiveResolvePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Library", "Application Support", "ld-resolve", "bindings.json")
}

// readBindings parses a bindings.json into bank -> control -> raw assignment,
// plus the "_config" object. Numbers keep their literal text.
func readBindings(path string) (banks map[string]map[string]Assignment, cfg map[string]any, err error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var top map[string]any
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err := d.Decode(&top); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	banks = map[string]map[string]Assignment{}
	for k, v := range top {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("%s: %q is not an object", path, k)
		}
		if k == "_config" {
			cfg = m
			continue
		}
		banks[k] = map[string]Assignment{}
		for cn, cv := range m {
			am, ok := cv.(map[string]any)
			if !ok {
				return nil, nil, fmt.Errorf("%s: %s.%s is not an object", path, k, cn)
			}
			banks[k][cn] = am
		}
	}
	return banks, cfg, nil
}

// seed builds the first presets. It only READS the source files; the caller
// writes the result into the presets dir, which is the one place Open writes.
//
//	Resolve  <- live ld-resolve bindings.json minus app banks; _config -> settings
//	         (fallback: SeedFrom, with a log line)
//	apps     <- app banks (bundle-id named) of SeedFrom, e.g. com.apple.finder
//	Global   <- empty, packs [generic]
func (s *Store) seed() ([]*Preset, error) {
	live := s.opts.LiveResolvePath
	if live == "" {
		live = DefaultLiveResolvePath()
	}
	var out []*Preset

	var rbanks map[string]map[string]Assignment
	var rcfg map[string]any
	var err error
	if live != "-" && live != "" {
		rbanks, rcfg, err = readBindings(live)
		if err != nil {
			s.opts.Logf("seed: live ld-resolve bindings unreadable (%v); falling back to %s", err, s.opts.SeedFrom)
			rbanks = nil
		}
	}
	var sbanks map[string]map[string]Assignment
	var scfg map[string]any
	if s.opts.SeedFrom != "" {
		sbanks, scfg, err = readBindings(s.opts.SeedFrom)
		if err != nil {
			s.opts.Logf("seed: %v", err)
			sbanks = nil
		}
	}
	fromBundled := false
	if rbanks == nil {
		rbanks, rcfg = sbanks, scfg
		fromBundled = true
	}
	if rbanks != nil {
		build := func(banks map[string]map[string]Assignment, cfg map[string]any) *Preset {
			rp := &Preset{Schema: SchemaVersion, ID: ResolveID, Name: DefaultName(ResolveID), Packs: DefaultPacks(ResolveID),
				Banks: map[string]map[string]Assignment{}, Settings: map[string]any{}, Extra: map[string]any{}}
			for bn, bm := range banks {
				if !isAppBank(bn) {
					rp.Banks[bn] = bm
				}
			}
			if cfg != nil {
				rp.Settings = cfg
			}
			return rp
		}
		rp := build(rbanks, rcfg)
		if fromBundled {
			// Only a Resolve preset built from the shipped default carries the
			// standard slider-mode mapping. One built from a user's live
			// ld-resolve bindings has no per-mode slots: they opt in later
			// (Store.ApplyStandardModes).
			withStandardModes(rp)
		}
		if msg := canonicalIssue(rp); msg != "" {
			// Seeding goes through the same check every later Open applies, so the
			// first and the second Open agree. The Resolve preset falls back to the
			// bundled default (SeedFrom); without one it is not created.
			s.seedNote("the live ld-resolve bindings cannot be used as they are: " + msg +
				". Rename the field in " + live + " (it is read, never written, by 2T5 DECK+), then delete presets/" + ResolveID + ".json to seed again.")
			rp = nil
			if sbanks != nil {
				if alt := build(sbanks, scfg); canonicalIssue(alt) == "" {
					rp = withStandardModes(alt)
					s.seedNote("the Resolve preset was seeded from the bundled default instead")
				}
			}
		}
		if rp != nil {
			out = append(out, rp)
		}
	} else {
		s.opts.Logf("seed: no Resolve source available; Resolve preset not created")
	}

	apps := map[string]*Preset{}
	names := make([]string, 0, len(sbanks))
	for bn := range sbanks {
		names = append(names, bn)
	}
	sort.Strings(names)
	for _, bn := range names {
		if !isAppBank(bn) {
			continue
		}
		app, bank := splitAppBank(bn)
		if CheckAppID(app) != nil || foldConflict(apps, app) != "" { // not a usable bundle id, or a case variant of one already seeded
			continue
		}
		p := apps[app]
		if p == nil {
			p = &Preset{Schema: SchemaVersion, ID: app, Name: DefaultName(app), Packs: DefaultPacks(app),
				Banks: map[string]map[string]Assignment{}, Settings: map[string]any{}, Extra: map[string]any{}}
			apps[app] = p
		}
		p.Banks[bank] = sbanks[bn]
	}
	for _, id := range sortedKeys(apps) {
		if msg := canonicalIssue(apps[id]); msg != "" {
			s.seedNote("app " + id + " from the bundled bindings was not created: " + msg)
			continue
		}
		out = append(out, apps[id])
	}
	out = append(out, newGlobal())
	return out, nil
}

func sortedKeys[V any](m map[string]V) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

// canonicalIssue is "" when every assignment of p uses the exact field spellings;
// else it names the first offender with the fix.
func canonicalIssue(p *Preset) string {
	for _, bn := range sortedKeys(p.Banks) {
		for _, cn := range sortedKeys(p.Banks[bn]) {
			if nc := config.NonCanonicalKeys(p.Banks[bn][cn]); len(nc) > 0 {
				return fmt.Sprintf("%s.%s.%s: field %q must be spelled %q", p.ID, bn, cn, nc[0].Path, nc[0].Canonical)
			}
		}
	}
	return ""
}

// seedNote records something the user should know about the first seeding; it
// shows in Warnings (kind seed-fallback) and in the log.
func (s *Store) seedNote(msg string) {
	s.opts.Logf("seed: %s", msg)
	s.seedNotes = append(s.seedNotes, msg)
}
