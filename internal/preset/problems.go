package preset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"2t5-deck-plus/internal/actions"
)

// Unreadable preset files (wave D, B5). A preset file that cannot be loaded
// is never dropped silently: it is listed (Problems / ProblemList, the
// warnings, GET /api/live engine.problems), its bytes stay untouched on disk,
// and RestoreLastGood brings back the newest readable version.

var brokenStamp = regexp.MustCompile(`^\d{8}-\d{6}\.\d{3}\.json$`)

// ErrNotBroken is returned by RestoreLastGood for a preset whose file reads fine.
var ErrNotBroken = errors.New("nothing to restore: this preset's file is readable")

// ErrNoGoodCopy means no readable earlier version exists.
var ErrNoGoodCopy = errors.New("no readable earlier version of this preset exists")

// Problem is one unreadable preset file.
type Problem struct {
	File  string `json:"file"`
	ID    string `json:"id"`
	Error string `json:"error"`
	// Fallback: the preset runs on the shipped default, read-only, until the
	// file is fixed or restored (Resolve only).
	Fallback bool `json:"fallback"`
	// CanRestore: a readable earlier version exists, so restore-last-good works.
	CanRestore bool `json:"canRestore"`
}

// ProblemList lists the unreadable preset files, sorted by file name.
func (s *Store) ProblemList() []Problem {
	s.mu.Lock()
	files := make([]string, 0, len(s.problems))
	msg := map[string]string{}
	fb := map[string]bool{}
	for f, e := range s.problems {
		files = append(files, f)
		msg[f] = e
		fb[f] = s.fallback[strings.TrimSuffix(f, ".json")]
	}
	snap := s.snap
	s.mu.Unlock()
	sort.Strings(files)
	out := make([]Problem, 0, len(files))
	for _, f := range files {
		id := strings.TrimSuffix(f, ".json")
		_, _, err := s.lastGood(id, snap)
		out = append(out, Problem{File: f, ID: id, Error: msg[f], Fallback: fb[f], CanRestore: err == nil})
	}
	return out
}

// LastGoodSource names where RestoreLastGood found the version it restored.
type LastGoodSource struct {
	Source string `json:"source"` // memory | history | seed
	File   string `json:"file,omitempty"`
}

// lastGood finds the newest readable version of id: the in-memory preset (the
// last state the store loaded or wrote, still held when an outside edit broke
// the file), else the newest parseable .history copy, else (Resolve) the .seed
// copy. The in-memory fallback of an unreadable Resolve file is not "good".
func (s *Store) lastGood(id string, snap *Snapshot) (*Preset, LastGoodSource, error) {
	if !ValidID(id) {
		return nil, LastGoodSource{}, &actions.FieldError{Field: "id", Msg: fmt.Sprintf("%q is not a usable app id", id)}
	}
	s.mu.Lock()
	isFallback := s.fallback[id]
	s.mu.Unlock()
	if p, ok := snap.presets[id]; ok && !isFallback {
		return p.Clone(), LastGoodSource{Source: "memory"}, nil
	}
	hdir := filepath.Join(s.dir, ".history", id)
	if es, err := os.ReadDir(hdir); err == nil {
		names := make([]string, 0, len(es))
		for _, e := range es {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
				names = append(names, e.Name())
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		for _, n := range names {
			b, err := os.ReadFile(filepath.Join(hdir, n))
			if err != nil {
				continue
			}
			p, err := ParsePreset(b)
			if err != nil || p.ID != id {
				continue
			}
			if !s.historyTrusted(id, n, b) { // not a copy this app made: do not restore guest-writable bytes
				continue
			}
			if p.Name == "" {
				p.Name = DefaultName(id)
			}
			return p, LastGoodSource{Source: "history", File: n}, nil
		}
	}
	if id == ResolveID {
		if p, err := readSeedCopy(s.seedDir(), id); err == nil {
			return p, LastGoodSource{Source: "seed", File: id + ".json"}, nil
		}
	}
	return nil, LastGoodSource{}, ErrNoGoodCopy
}

// RestoreLastGood replaces the unreadable file of preset id with its newest
// readable version. The broken file is first copied byte for byte to
// .backups/broken-<id>-<time>.json (newest 10 per preset kept). It is one
// undo step. ErrNotBroken when the file reads fine, ErrNoGoodCopy when there
// is nothing to restore from.
func (s *Store) RestoreLastGood(id string) (LastGoodSource, error) {
	file := id + ".json"
	snap := s.Snapshot()
	good, src, err := s.lastGood(id, snap)
	if err != nil {
		s.mu.Lock()
		_, bad := s.problems[file]
		s.mu.Unlock()
		if !bad && errors.Is(err, ErrNoGoodCopy) {
			if _, known := snap.Preset(id); !known {
				return LastGoodSource{}, fmt.Errorf("%w: %q", ErrNoPreset, id)
			}
			return LastGoodSource{}, ErrNotBroken
		}
		return LastGoodSource{}, err
	}
	err = s.commitThen("Restore "+DefaultName(id)+" from the last good version", func(ps map[string]*Preset) error {
		if _, bad := s.problems[file]; !bad {
			return ErrNotBroken
		}
		if err := s.keepBrokenCopyLocked(id); err != nil {
			return fmt.Errorf("could not keep a copy of the broken file, nothing restored: %w", err)
		}
		ps[id] = good
		s.force[id] = true // the file on disk is the thing that is wrong, not the snapshot
		return nil
	}, func() {
		delete(s.problems, file)
		delete(s.fallback, id)
	})
	if err != nil {
		return LastGoodSource{}, err
	}
	return src, nil
}

// keepBrokenCopyLocked saves the bytes of the unreadable file.
func (s *Store) keepBrokenCopyLocked(id string) error {
	b, err := os.ReadFile(s.path(id))
	if os.IsNotExist(err) {
		return nil // nothing on disk to keep
	}
	if err != nil {
		return err
	}
	dir := s.backupsDir()
	if err := os.Mkdir(dir, 0o755); err != nil {
		if !os.IsExist(err) {
			return err
		}
		if st, serr := os.Stat(dir); serr != nil || !st.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		}
	}
	name := fmt.Sprintf("broken-%s-%s.json", id, s.opts.Now().Format("20060102-150405.000"))
	if err := WriteAtomic(filepath.Join(dir, name), b); err != nil {
		return err
	}
	es, _ := os.ReadDir(dir)
	var mine []string
	for _, e := range es {
		if strings.HasPrefix(e.Name(), "broken-"+id+"-") && brokenStamp.MatchString(strings.TrimPrefix(e.Name(), "broken-"+id+"-")) {
			mine = append(mine, e.Name())
		}
	}
	sort.Strings(mine)
	for len(mine) > 10 {
		os.Remove(filepath.Join(dir, mine[0]))
		mine = mine[1:]
	}
	return nil
}
