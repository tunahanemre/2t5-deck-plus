package preset

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The trust store (wave P). The presets folder, its .backups/ and its .history/
// sit on a volume others can write to, so nothing found there is trusted just
// for being there. On the INTERNAL disk (Options.StateDir/trust, 0700) the store
// keeps
//
//	lastgood/<id>.json   the presets exactly as this app last accepted them
//	manifest.json        sha256 of each accepted preset (canonical marshal), of each
//	                     app-made backup zip and of each app-made history copy
//
// and uses it to (1) compare the folder with what was last good when the app
// starts, holding back any change that adds risk (the same comparison the poll
// uses), (2) tell app-made backups and history copies from planted ones.
// Everything is bounded (file count, file size) and best effort: a trust store
// that cannot be written never blocks an edit; it only means the next start has
// less to compare against. Without a StateDir none of this runs (library use,
// tests): behaviour as in earlier waves.

const (
	maxTrustFiles = 500
	maxTrustBytes = 2 << 20
)

type trustManifest struct {
	Version int               `json:"version"`
	Presets map[string]string `json:"presets"`
	Backups map[string]string `json:"backups"`
	History map[string]string `json:"history"`
}

func newManifest() *trustManifest {
	return &trustManifest{Version: 1, Presets: map[string]string{}, Backups: map[string]string{}, History: map[string]string{}}
}

func hashHex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

func (s *Store) trustDir() string {
	if s.opts.StateDir == "" || s.stateErr != nil {
		return ""
	}
	return filepath.Join(s.opts.StateDir, "trust")
}

// loadTrust reads the manifest. (nil, false) = there is none (first run: trust
// once). (nil, true) = a manifest file EXISTS but cannot be read, parsed or has
// another version: the caller fails closed (disarmed start + warning).
func (s *Store) loadTrust() (*trustManifest, bool) {
	d := s.trustDir()
	if d == "" {
		return nil, false
	}
	b, err := readCapped(filepath.Join(d, "manifest.json"), 16<<20)
	if err != nil {
		return nil, !os.IsNotExist(err)
	}
	var m trustManifest
	if json.Unmarshal(b, &m) != nil || m.Version != 1 {
		return nil, true
	}
	if m.Presets == nil {
		m.Presets = map[string]string{}
	}
	if m.Backups == nil {
		m.Backups = map[string]string{}
	}
	if m.History == nil {
		m.History = map[string]string{}
	}
	return &m, false
}

// readCapped reads a file, failing (not truncating) when it is over max bytes.
func readCapped(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", filepath.Base(path), max)
	}
	return b, nil
}

// saveTrust writes the manifest. Caller holds trustMu (or is single-threaded in Open).
func (s *Store) saveTrust() {
	d := s.trustDir()
	if d == "" || s.trust == nil || s.readonly {
		return
	}
	if err := os.MkdirAll(filepath.Join(d, "lastgood"), 0o700); err != nil {
		s.opts.Logf("trust: %v", err)
		return
	}
	b, err := json.Marshal(s.trust)
	if err == nil {
		err = WriteAtomic(filepath.Join(d, "manifest.json"), b)
	}
	if err != nil {
		s.opts.Logf("trust: manifest: %v", err)
	}
}

// trustSyncLocked makes the trust store say what is accepted NOW (the snapshot),
// writing only what changed. Called after every install and at close.
func (s *Store) trustSyncLocked(ps map[string]*Preset) {
	d := s.trustDir()
	if d == "" || s.readonly {
		return
	}
	s.trustMu.Lock()
	defer s.trustMu.Unlock()
	if s.trust == nil {
		s.trust = newManifest()
	}
	if err := os.MkdirAll(filepath.Join(d, "lastgood"), 0o700); err != nil {
		s.opts.Logf("trust: %v", err)
		return
	}
	changed := false
	n := 0
	for _, id := range sortedKeys(ps) {
		n++
		if n > maxTrustFiles {
			break
		}
		b := marshalOf(ps[id])
		if len(b) == 0 || len(b) > maxTrustBytes {
			continue
		}
		if s.trust.Presets[id] == hashHex(b) {
			continue
		}
		if err := WriteAtomic(filepath.Join(d, "lastgood", id+".json"), b); err != nil {
			s.opts.Logf("trust: lastgood %s: %v", id, err)
			continue
		}
		s.trust.Presets[id] = hashHex(b)
		changed = true
	}
	for id := range s.trust.Presets {
		if _, ok := ps[id]; !ok {
			os.Remove(filepath.Join(d, "lastgood", id+".json"))
			delete(s.trust.Presets, id)
			changed = true
		}
	}
	if changed {
		s.saveTrust()
	}
}

// trustBackup / trustHistory record an app-made file.
func (s *Store) trustBackup(name string, data []byte) {
	if s.trustDir() == "" || s.readonly {
		return
	}
	s.trustMu.Lock()
	defer s.trustMu.Unlock()
	if s.trust == nil {
		s.trust = newManifest()
	}
	s.trust.Backups[name] = hashHex(data)
	if es, err := os.ReadDir(s.backupsDir()); err == nil { // forget zips that are gone
		have := map[string]bool{}
		for _, e := range es {
			have[e.Name()] = true
		}
		for n := range s.trust.Backups {
			if !have[n] && n != name {
				delete(s.trust.Backups, n)
			}
		}
	}
	s.saveTrust()
}

func (s *Store) trustHistory(id, file string, data []byte) {
	if s.trustDir() == "" || s.readonly {
		return
	}
	s.trustMu.Lock()
	defer s.trustMu.Unlock()
	if s.trust == nil {
		return
	}
	// only copies of what this app itself had accepted
	if parsed, err := ParsePreset(data); err != nil || s.trust.Presets[id] != hashHex(marshalOf(parsed)) {
		return
	}
	s.trust.History[id+"/"+file] = hashHex(data)
	prefix := id + "/"
	if es, err := os.ReadDir(filepath.Join(s.dir, ".history", id)); err == nil {
		have := map[string]bool{}
		for _, e := range es {
			have[prefix+e.Name()] = true
		}
		for k := range s.trust.History {
			if strings.HasPrefix(k, prefix) && !have[k] && k != prefix+file {
				delete(s.trust.History, k)
			}
		}
	}
	s.saveTrust()
}

// backupTrusted (and historyTrusted) read s.trust under trustMu: they are called
// from Backups()/RestoreBackupOpts without s.mu while trustBackup writes the map.
// trustMu is a leaf lock (nothing else is taken while it is held), so callers may
// or may not hold s.mu.
// backupTrusted says whether a backup zip is one this app made (its hash is in
// the manifest). Without a trust store every backup counts as trusted.
func (s *Store) backupTrusted(name string, data []byte) bool {
	if s.trustDir() == "" {
		return true
	}
	h := hashHex(data) // outside the lock
	s.trustMu.RLock()
	defer s.trustMu.RUnlock()
	if s.trust == nil {
		return false
	}
	return s.trust.Backups[name] == h
}

// historyTrusted likewise for a .history copy.
func (s *Store) historyTrusted(id, file string, data []byte) bool {
	if s.trustDir() == "" {
		return true
	}
	h := hashHex(data)
	s.trustMu.RLock()
	defer s.trustMu.RUnlock()
	if s.trust == nil {
		return false
	}
	return s.trust.History[id+"/"+file] == h
}

// trustGateAtOpen compares what the folder holds with what was last accepted and
// holds back any difference that adds risk: the last-good version stays in
// memory, the file stays on disk as a pending item, and the start is forced
// disarmed (StartReasonExternal). No manifest = trust once; the caller builds it.
func (s *Store) trustGateAtOpen(ps map[string]*Preset) {
	d := s.trustDir()
	if d == "" {
		return
	}
	m, bad := s.loadTrust()
	if bad {
		// A manifest exists but cannot be used: we cannot tell what was accepted,
		// so fail closed. The folder is trusted as it is (nothing to compare), but
		// the start is disarmed (external-changes) with a warning; trustSyncLocked
		// then writes a fresh manifest.
		s.trustUnreadable = true
		s.externalAtOpen = true
		s.opts.Logf("trust: manifest.json exists but cannot be used: starting disarmed")
		return
	}
	if m == nil {
		return // first time: trust what is there once; trustSyncLocked writes the manifest
	}
	s.trustMu.Lock()
	s.trust = m
	s.trustMu.Unlock()
	lg := map[string]*Preset{}
	for id, want := range m.Presets {
		b, err := os.ReadFile(filepath.Join(d, "lastgood", id+".json"))
		if err != nil || hashHex(b) != want {
			continue
		}
		if p, err := ParsePreset(b); err == nil {
			lg[id] = p
		}
	}
	// A last-good copy the manifest has no hash for (a crash or an older build between writing the copy and saving the
	// manifest) is still the app's own file: only the app writes this internal folder. Where the manifest HAS a hash and
	// the copy does not match it, the copy stays distrusted (above). Without this a file byte-identical to its own
	// last-good copy was held back as an "outside change" and the whole preset vanished (Resolve, 2026-11-04).
	if es, err := os.ReadDir(filepath.Join(d, "lastgood")); err == nil {
		for _, e := range es {
			id := strings.TrimSuffix(e.Name(), ".json")
			if e.IsDir() || id == e.Name() {
				continue
			}
			if _, known := m.Presets[id]; known {
				continue
			}
			if _, live := ps[id]; !live { // an orphan copy of a preset that is gone is not a baseline
				continue
			}
			if b, err := os.ReadFile(filepath.Join(d, "lastgood", e.Name())); err == nil {
				if p, err := ParsePreset(b); err == nil && p.ID == id {
					lg[id] = p
				}
			}
		}
	}
	base := make(map[string]*Preset, len(lg))
	for id, p := range lg {
		base[id] = p
	}
	for _, id := range sortedKeys(ps) {
		p := ps[id]
		prev := lg[id]
		if s.seededAtOpen[id] { // written by this very Open from the seed sources: ours, not an outside change
			base[id] = p
			continue
		}
		if prev != nil && bytes.Equal(marshalOf(prev), marshalOf(p)) {
			continue
		}
		words, cats := s.externalRisk(base, p)
		if len(words) == 0 {
			base[id] = p
			continue
		}
		file := id + ".json"
		raw, _ := os.ReadFile(filepath.Join(s.dir, file))
		s.quarantineLocked(file, raw, p, "change", words, cats)
		s.externalAtOpen = true
		if prev != nil {
			ps[id] = prev
			base[id] = prev
		} else {
			delete(ps, id)
		}
	}
	for _, id := range sortedKeys(lg) {
		if _, still := ps[id]; still {
			continue
		}
		if hasPanicKey(lg[id]) {
			// missing OR unreadable (a corrupt file is in s.problems): either way the
			// panic key must not silently vanish. Keep the last-good copy in memory.
			what := "is missing"
			if _, broken := s.problems[id+".json"]; broken {
				what = "cannot be read"
			}
			s.quarantineLocked(id+".json", nil, nil, "removal",
				[]string{"the file of " + lg[id].Name + ", which holds a panic key, " + what}, []string{"panic-removed"})
			ps[id] = lg[id]
			s.externalAtOpen = true
		}
	}
}

// sortedManifestIDs is for tests.
func (m *trustManifest) sortedIDs() []string {
	ids := make([]string, 0, len(m.Presets))
	for id := range m.Presets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
