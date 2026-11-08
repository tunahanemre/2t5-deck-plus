package preset

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"2t5-deck-plus/internal/actions"
	"2t5-deck-plus/internal/config"
)

// Errors the API maps to HTTP statuses.
var (
	ErrNoPreset  = errors.New("no such preset")
	ErrExists    = errors.New("preset already exists")
	ErrNoControl = errors.New("control is not assigned in this preset")
)

// Options configure Open.
type Options struct {
	// SeedFrom is 2T5 DECK+'s bindings.json (production: <cfgDir>/bindings.json).
	// Used to seed app banks (Finder) and as fallback for Resolve.
	SeedFrom string
	// LiveResolvePath is the live ld-resolve bindings.json (read-only).
	// "" = ~/Library/Application Support/ld-resolve/bindings.json, "-" = none.
	LiveResolvePath string
	// Controls, when non-empty, restricts control names to this set.
	Controls []string
	// PollInterval for external edits; 0 = 2s, negative = no poll goroutine.
	PollInterval time.Duration
	Logf         func(format string, args ...any)
	Now          func() time.Time
	MaxUndo      int // default 200
	HistoryKeep  int // default 30
	BackupKeep   int // dated backups kept per class (daily / other), default 14
	// Lock takes an exclusive flock on <dir>/.lock. When another process
	// already holds it the store opens READ-ONLY: every write returns a
	// *ReadOnlyError naming the holder's pid. Off by default (library use and
	// tests); the app and the dev server turn it on.
	Lock bool
	// StateDir is the INTERNAL-disk folder (the app uses
	// ~/Library/Application Support/2t5-deck-plus/state, mode 0700) that holds the
	// authoritative `.disarmed` and `.first-run-done` markers. The presets folder
	// may be on a share others can write to, so a marker there can be deleted or
	// forged: with a StateDir, "disarmed" is true if EITHER place says so, and
	// "first run done" only if the internal place says so. If the folder cannot be
	// used the store fails closed (StartDisarmed: "state-unavailable") and warns.
	// "" = share-only markers, as in earlier waves (tests, library use).
	StateDir string
	// StateDirErr, when set, makes the store fail closed as if the state folder were
	// unusable (the app sets it when the folder cannot be located: HOME unset, or
	// a test run without an explicit override).
	StateDirErr error
	// OnQuarantine is called (outside the store's lock) when the poll holds back
	// an outside change that adds risk; the app puts it in the events ring.
	OnQuarantine func(PendingExternal)
	// CanaryDebounce delays the .canary.json write after a change (see
	// canary.go): 0 = 400 ms, negative = write at once (tests).
	CanaryDebounce time.Duration
}

// ReadOnlyError is returned by every write while another process holds the
// presets-dir lock.
type ReadOnlyError struct{ Holder int }

func (e *ReadOnlyError) Error() string {
	if e.Holder > 0 {
		return fmt.Sprintf("the presets are read-only here: another 2T5 DECK+ (process %d) is editing them", e.Holder)
	}
	return "the presets are read-only here: another 2T5 DECK+ is editing them"
}

// ErrFallbackReadOnly is returned for an edit of a preset that is only an
// in-memory fallback because its file could not be read.
var ErrFallbackReadOnly = errors.New("this preset's file is unreadable and the shipped default is loaded read-only: restore the last good version or fix the file first")

type fileState struct {
	hash  [32]byte
	mtime time.Time
	size  int64
}

type histEntry struct {
	label string
	snap  *Snapshot
	t     time.Time // when the change was made
}

// Store owns the presets directory.
type Store struct {
	dir  string
	opts Options
	reg  *actions.Registry

	mu       sync.Mutex
	snap     *Snapshot
	seq      int
	undo     []histEntry
	redo     []histEntry
	files    map[string]fileState
	problems map[string]string
	// fallback marks presets that are an in-memory copy of the shipped default
	// because their file is unreadable; they are read-only until fixed.
	fallback map[string]bool
	force    map[string]bool // ids applyLocked must write even when unchanged
	subs     []func(*Snapshot)
	ctl      map[string]bool

	lockFile *os.File
	readonly bool
	holder   int

	deliverMu sync.Mutex
	delivered int // newest snapshot Seq handed to the subscribers

	// File-operation seams (tests inject failures); Open sets the defaults.
	renameFn func(oldpath, newpath string) error
	removeFn func(path string) error

	can        canaryState // silent-loss canary (canary.go)
	extInstall bool        // installLocked is installing an outside change (the poll)
	// seedNotes: things the first seeding wants the user to know (Warnings, kind seed-fallback).
	seedNotes []string
	// trust: the internal-disk record of what was last accepted (trust.go); nil without a StateDir.
	trust *trustManifest
	// trustMu guards trust (leaf lock; readers run without s.mu). trustUnreadable:
	// the manifest existed but could not be used at Open (fail closed + warning).
	trustMu         sync.RWMutex
	trustUnreadable bool
	// hashCache: sha256 of backup zips by name+size+mtime (Backups listing).
	hashMu    sync.Mutex
	hashCache map[backupHashKey]string
	// externalAtOpen: Open held back a risky difference between the folder and the last-good copy.
	externalAtOpen bool
	// seededAtOpen: ids this Open wrote itself from the seed sources (first run of a presets folder). They are the app's own
	// files, never an outside change, so the trust gate must not compare them with an older manifest's copies.
	seededAtOpen map[string]bool
	// stateErr: the internal state folder could not be created or written.
	stateErr error
	// pending: quarantined outside changes (quarantine.go), keyed by id.
	pending map[string]*PendingExternal
	// pendingLabel names the manual backup being made (BackupNowLabelled).
	pendingLabel string
	// preUpdateMade: Open met a new build or schema and tried a pre-update backup (it may have failed).
	preUpdateMade bool
	// admitFail: preset files that parse but were refused by admit (compose
	// collision, case clash ...): file name -> true. They are re-evaluated on
	// every commit and poll even when their bytes did not change.
	admitFail map[string]bool

	lastBackupDay string // YYYYMMDD of the newest backup (see backup.go)
	opened        time.Time

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// Open loads (and when the directory holds no preset yet, seeds) the store.
// dir must be an existing directory or a path whose parent exists.
func Open(dir string, opts Options) (*Store, error) {
	if opts.Logf == nil {
		opts.Logf = func(string, ...any) {}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxUndo <= 0 {
		opts.MaxUndo = 200
	}
	if opts.HistoryKeep <= 0 {
		opts.HistoryKeep = 30
	}
	if opts.PollInterval == 0 {
		opts.PollInterval = 2 * time.Second
	}
	if st, err := os.Stat(dir); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		if err := os.Mkdir(dir, 0o755); err != nil { // not MkdirAll: never conjure a missing volume path
			return nil, fmt.Errorf("preset: create %s: %w", dir, err)
		}
	} else if !st.IsDir() {
		return nil, fmt.Errorf("preset: %s is not a directory", dir)
	}
	var lockFile *os.File
	var roHolder int
	readonly := false
	if opts.Lock {
		f, ok, holder, err := tryLock(filepath.Join(dir, ".lock"))
		if err != nil {
			return nil, fmt.Errorf("preset: lock %s: %w", dir, err)
		}
		lockFile, readonly, roHolder = f, !ok, holder
	}
	s := &Store{dir: dir, lockFile: lockFile, readonly: readonly, holder: roHolder, opts: opts, reg: actions.Default(), files: map[string]fileState{}, problems: map[string]string{}, fallback: map[string]bool{}, force: map[string]bool{}, admitFail: map[string]bool{}, stop: make(chan struct{}),
		renameFn: os.Rename, removeFn: os.Remove}
	if len(opts.Controls) > 0 {
		s.ctl = map[string]bool{}
		for _, c := range opts.Controls {
			s.ctl[c] = true
		}
	}
	ps := map[string]*Preset{}
	entries, err := presetFiles(dir)
	if err != nil {
		s.Close()
		return nil, err
	}
	fail := func(err error) (*Store, error) { s.Close(); return nil, err }
	s.restrictDir()
	s.initStateDir()
	canaryOnDisk := readCanaryFile(s.canaryPath())
	s.preUpdate(canaryOnDisk, len(entries) > 0) // before anything below can write
	if len(entries) == 0 {
		seeded, err := s.seed()
		if err != nil {
			return fail(err)
		}
		for _, p := range seeded {
			if err := admit(ps, p); err != nil { // the same gate every later Open applies
				s.seedNote(fmt.Sprintf("%s from the seed sources was not created: %v", p.ID, err))
				continue
			}
			if !s.readonly { // a read-only store keeps the seed in memory only
				if err := s.writeLocked(p, false); err != nil {
					return fail(err)
				}
				if p.ID == ResolveID {
					s.writeSeedCopy(p)
				}
			}
			ps[p.ID] = p
			if !s.readonly {
				if s.seededAtOpen == nil {
					s.seededAtOpen = map[string]bool{}
				}
				s.seededAtOpen[p.ID] = true
			}
		}
	} else {
		for _, name := range entries {
			p, err := s.readFile(name)
			parsed := err == nil
			if err == nil {
				err = admit(ps, p)
			}
			if err != nil {
				if parsed {
					s.admitFail[name] = true
				}
				s.problems[name] = err.Error()
				s.noteBroken(name)
				opts.Logf("preset: skipping %s: %v", name, err)
				continue
			}
			ps[p.ID] = p
		}
		// Resolve must never be left without a cascade: when its file is
		// unreadable, run on the shipped default, read-only, until it is fixed.
		if ps[ResolveID] == nil {
			if _, bad := s.problems[ResolveID+".json"]; bad {
				if fb, err := s.resolveSeed(); err == nil {
					ps[ResolveID] = fb
					s.fallback[ResolveID] = true
					opts.Logf("preset: %s.json is unreadable; running on the shipped default (read-only)", ResolveID)
				}
			}
		}
	}
	if ps[GlobalID] == nil {
		g := newGlobal()
		if !s.readonly {
			if err := s.writeLocked(g, false); err != nil {
				return fail(err)
			}
		}
		ps[GlobalID] = g
	}
	s.trustGateAtOpen(ps) // hold back risky differences from the last-good copy (trust.go)
	s.snap = newSnapshot(0, ps)
	s.opened = opts.Now()
	s.initBackupDay()
	s.canaryOpen(canaryOnDisk, ps)
	s.trustSyncLocked(ps) // the trust store now says what was accepted
	if opts.PollInterval > 0 {
		s.wg.Add(1)
		go s.poll()
	}
	return s, nil
}

func newGlobal() *Preset {
	return &Preset{Schema: SchemaVersion, ID: GlobalID, Name: "Global", Packs: []string{"generic"},
		Banks: map[string]map[string]Assignment{}, Settings: map[string]any{}, Extra: map[string]any{}}
}

// Close stops the poll goroutine.
func (s *Store) Close() {
	s.stopOnce.Do(func() {
		close(s.stop)
		if s.lockFile != nil {
			s.lockFile.Close() // releases the flock
		}
	})
	s.wg.Wait()
	s.canaryClose()
	s.mu.Lock()
	if s.snap != nil {
		s.trustSyncLocked(s.snap.presets) // clean close: the last-good copy matches what ran
	}
	s.mu.Unlock()
}

// ReadOnly reports whether another process holds the presets-dir lock, and
// that process's pid (0 when unknown).
func (s *Store) ReadOnly() (bool, int) { return s.readonly, s.holder }

// LockHeld reports whether this process holds the presets-dir lock (false for a
// store opened without Options.Lock, and for a read-only one).
func (s *Store) LockHeld() bool { return s.lockFile != nil && !s.readonly }

func (s *Store) roErr() error {
	if s.readonly {
		return &ReadOnlyError{Holder: s.holder}
	}
	return nil
}

// Dir is the presets directory.
func (s *Store) Dir() string { return s.dir }

// Registry is the action registry in use.
func (s *Store) Registry() *actions.Registry { return s.reg }

// Snapshot returns the current immutable snapshot.
func (s *Store) Snapshot() *Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snap
}

// Problems lists preset files that could not be loaded (file -> reason).
func (s *Store) Problems() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for k, v := range s.problems {
		out[k] = v
	}
	return out
}

// OnChange registers fn, called (outside the lock) with the new snapshot after
// every edit, undo, redo or external reload. Returns an unsubscribe func.
func (s *Store) OnChange(fn func(*Snapshot)) func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.subs = append(s.subs, fn)
	idx := len(s.subs) - 1
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		if idx < len(s.subs) {
			s.subs[idx] = nil
		}
	}
}

func presetFiles(dir string) ([]string, error) {
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range es {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, ".") || !strings.HasSuffix(n, ".json") {
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func (s *Store) path(id string) string { return filepath.Join(s.dir, id+".json") }

func (s *Store) readFile(name string) (*Preset, error) {
	full := filepath.Join(s.dir, name)
	b, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	p, err := ParsePreset(b)
	if err != nil {
		return nil, err
	}
	if p.ID+".json" != name {
		return nil, fmt.Errorf("file name %s does not match preset id %q", name, p.ID)
	}
	st, err := os.Stat(full)
	if err == nil {
		s.files[p.ID] = fileState{sha256.Sum256(b), st.ModTime(), st.Size()}
	}
	return p, nil
}

// noteBroken remembers an unreadable file's content so the poll does not
// re-parse (and re-log) it until it changes.
func (s *Store) noteBroken(name string) {
	full := filepath.Join(s.dir, name)
	b, err := os.ReadFile(full)
	if err != nil {
		return
	}
	if st, err := os.Stat(full); err == nil {
		s.files[strings.TrimSuffix(name, ".json")] = fileState{sha256.Sum256(b), st.ModTime(), st.Size()}
	}
}

// writeLocked writes one preset atomically, saving the previous file into
// .history/<id>/ first. Caller holds s.mu (or is single-threaded in Open).
func (s *Store) writeLocked(p *Preset, history bool) error {
	b, err := p.Marshal()
	if err != nil {
		return err
	}
	path := s.path(p.ID)
	if history {
		if old, err := os.ReadFile(path); err == nil {
			if bytes.Equal(old, b) {
				return nil
			}
			s.saveHistory(p.ID, old, "")
		}
	}
	if err := writeAtomic(path, b); err != nil {
		return err
	}
	s.clearPendingLocked(p.ID + ".json")
	if st, err := os.Stat(path); err == nil {
		s.files[p.ID] = fileState{sha256.Sum256(b), st.ModTime(), st.Size()}
	}
	return nil
}

func (s *Store) removeLocked(id string) error {
	path := s.path(id)
	if old, err := os.ReadFile(path); err == nil {
		s.saveHistory(id, old, ".removed")
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	delete(s.files, id)
	return nil
}

func (s *Store) saveHistory(id string, data []byte, tag string) {
	hdir := filepath.Join(s.dir, ".history", id)
	if err := os.MkdirAll(hdir, 0o755); err != nil { // inside the presets dir, whose existence was checked in Open
		s.opts.Logf("preset: history dir: %v", err)
		return
	}
	ts := s.opts.Now().Format("20060102T150405.000")
	name := filepath.Join(hdir, ts+tag+".json")
	for i := 1; i < 1000; i++ {
		if _, err := os.Stat(name); err != nil {
			break
		}
		name = filepath.Join(hdir, fmt.Sprintf("%s-%d%s.json", ts, i, tag))
	}
	if err := os.WriteFile(name, data, 0o644); err != nil {
		s.opts.Logf("preset: history write: %v", err)
		return
	}
	s.trustHistory(id, filepath.Base(name), data)
	es, _ := os.ReadDir(hdir)
	var names []string
	for _, e := range es {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for len(names) > s.opts.HistoryKeep {
		os.Remove(filepath.Join(hdir, names[0]))
		names = names[1:]
	}
}

// writeAtomic: temp file in the same directory, fsync, rename (borrowed from
// LD-RESOLVE internal/editorstore writeAtomic).
func writeAtomic(path string, data []byte) error { return WriteAtomic(path, data) }

// WriteAtomic writes data to path via a temp file in the same directory,
// fsync and rename, so a reader never sees a half-written file.
func WriteAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".preset-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// ---- mutations -------------------------------------------------------

// commit applies fn to a deep copy of all presets, validates nothing itself
// (fn does), writes the changed files and publishes a new snapshot.
func (s *Store) commit(label string, fn func(ps map[string]*Preset) error) error {
	return s.commitThen(label, fn, nil)
}

// commitThen is commit with an after hook, run under the lock once the new
// snapshot is installed (and not at all when nothing changed or it failed).
func (s *Store) commitThen(label string, fn func(ps map[string]*Preset) error, after func()) error {
	subs, snap, err := s.commitLocked(label, fn, after)
	if err != nil || snap == nil {
		return err
	}
	s.deliver(subs, snap)
	s.recheckRefused()
	return nil
}

// commitLocked is the part of commitThen that runs under s.mu. snap is nil
// when nothing changed.
func (s *Store) commitLocked(label string, fn func(ps map[string]*Preset) error, after func()) ([]func(*Snapshot), *Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.roErr(); err != nil {
		return nil, nil, err
	}
	defer func() { s.force = map[string]bool{} }() // runs before the unlock above
	cur := s.snap
	ps := make(map[string]*Preset, len(cur.presets))
	for id, p := range cur.presets {
		ps[id] = p.Clone()
	}
	if err := fn(ps); err != nil {
		return nil, nil, err
	}
	// Dry-run of the compose the engine will do: a change the deck could not
	// build its configuration from is refused here, before anything is written
	// (otherwise the engine keeps the old config and later edits never arrive).
	// A store that already fails to compose is not blocked from repairing itself.
	if err := composeCheck(ps); err != nil && composeCheck(cur.presets) == nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrWouldNotCompose, err)
	}
	for id := range s.fallback { // the in-memory stand-in for an unreadable file is never edited
		if !bytes.Equal(marshalOf(cur.presets[id]), marshalOf(ps[id])) && !s.force[id] {
			return nil, nil, ErrFallbackReadOnly
		}
	}
	if err := s.applyLocked(cur, ps); err != nil {
		return nil, nil, err
	}
	if !s.changedLocked(cur, ps) {
		return nil, nil, nil
	}
	s.undo = append(s.undo, histEntry{label, cur, s.opts.Now()})
	if len(s.undo) > s.opts.MaxUndo {
		s.undo = s.undo[len(s.undo)-s.opts.MaxUndo:]
	}
	s.redo = nil
	subs, snap := s.installLocked(ps)
	if after != nil {
		after()
	}
	return subs, snap, nil
}

func marshalOf(p *Preset) []byte {
	if p == nil {
		return nil
	}
	b, _ := p.Marshal()
	return b
}

func (s *Store) changedLocked(cur *Snapshot, ps map[string]*Preset) bool {
	for id, p := range ps {
		if s.force[id] {
			return true
		}
		if !bytes.Equal(marshalOf(cur.presets[id]), marshalOf(p)) {
			return true
		}
	}
	for id := range cur.presets {
		if ps[id] == nil {
			return true
		}
	}
	return false
}

// fileOp is one staged file change of applyLocked: a write (data != nil) or
// a removal (data == nil), with what was on disk before so it can be undone.
type fileOp struct {
	id     string
	path   string
	data   []byte
	tmp    string
	old    []byte
	hadOld bool
}

// applyLocked changes the files for the move from cur to ps all-or-nothing:
// every changed file is first written to a temp file in the same directory,
// and only when ALL temps exist are they renamed into place (then removals).
// If a rename or removal fails, the files already changed are put back to the
// bytes they had on disk and an error is returned; the caller's stacks and
// snapshot stay untouched. Caller holds s.mu, so the poll never sees a
// half-applied state.
func (s *Store) applyLocked(cur *Snapshot, ps map[string]*Preset) error {
	if err := s.roErr(); err != nil {
		return err
	}
	ids := make([]string, 0, len(ps))
	for id := range ps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var ops []*fileOp
	for _, id := range ids {
		if !s.force[id] && bytes.Equal(marshalOf(cur.presets[id]), marshalOf(ps[id])) {
			continue
		}
		b, err := ps[id].Marshal()
		if err != nil {
			return fmt.Errorf("preset: write %s: %w", id, err)
		}
		ops = append(ops, &fileOp{id: id, path: s.path(id), data: b})
	}
	var gone []string
	for id := range cur.presets {
		if ps[id] == nil {
			gone = append(gone, id)
		}
	}
	sort.Strings(gone)
	for _, id := range gone {
		ops = append(ops, &fileOp{id: id, path: s.path(id)})
	}
	if len(ops) == 0 {
		return nil
	}
	s.dailyBackupLocked() // once, before the first write of the calendar day
	for _, op := range ops {
		old, err := os.ReadFile(op.path)
		switch {
		case err == nil:
			op.old, op.hadOld = old, true
		case !os.IsNotExist(err):
			return fmt.Errorf("preset: read %s before writing: %w", op.id, err)
		}
	}
	cleanup := func() {
		for _, op := range ops {
			if op.tmp != "" {
				os.Remove(op.tmp)
				op.tmp = ""
			}
		}
	}
	for _, op := range ops {
		if op.data == nil {
			continue
		}
		tmp, err := stageTemp(s.dir, op.data)
		if err != nil {
			cleanup()
			return fmt.Errorf("preset: write %s: %w", op.id, err)
		}
		op.tmp = tmp
	}
	var applied []*fileOp
	for _, op := range ops {
		var err error
		if op.data != nil {
			err = s.renameFn(op.tmp, op.path)
			if err == nil {
				op.tmp = ""
			}
		} else if err = s.removeFn(op.path); os.IsNotExist(err) {
			err = nil
		}
		if err != nil {
			cleanup()
			msg := fmt.Sprintf("preset: write %s: %v", op.id, err)
			if rerr := s.rollbackLocked(applied); rerr != nil {
				msg += fmt.Sprintf(" (and the earlier changes could not all be undone: %v)", rerr)
			}
			return errors.New(msg)
		}
		applied = append(applied, op)
	}
	for _, op := range applied {
		if op.data == nil {
			if op.hadOld {
				s.saveHistory(op.id, op.old, ".removed")
			}
			delete(s.files, op.id)
			continue
		}
		if op.hadOld && !bytes.Equal(op.old, op.data) {
			if _, perr := ParsePreset(op.old); perr == nil { // unreadable bytes are kept elsewhere, not in history
				s.saveHistory(op.id, op.old, "")
			}
		}
		s.recordFileLocked(op.id, op.path, op.data)
	}
	return nil
}

// rollbackLocked puts the already-applied ops back to their previous bytes.
func (s *Store) rollbackLocked(applied []*fileOp) error {
	var first error
	for i := len(applied) - 1; i >= 0; i-- {
		op := applied[i]
		var err error
		if op.hadOld {
			err = WriteAtomic(op.path, op.old)
			if err == nil {
				s.recordFileLocked(op.id, op.path, op.old)
			}
		} else {
			if err = os.Remove(op.path); os.IsNotExist(err) {
				err = nil
			}
			if err == nil {
				delete(s.files, op.id)
			}
		}
		if err != nil {
			s.opts.Logf("preset: rollback of %s failed: %v", op.id, err)
			if first == nil {
				first = fmt.Errorf("%s: %w", op.id, err)
			}
		}
	}
	return first
}

func (s *Store) recordFileLocked(id, path string, data []byte) {
	s.clearPendingLocked(id + ".json") // our own write replaced whatever outside bytes waited here
	if st, err := os.Stat(path); err == nil {
		s.files[id] = fileState{sha256.Sum256(data), st.ModTime(), st.Size()}
	}
}

// stageTemp writes data to a fsynced temp file next to the final path.
func stageTemp(dir string, data []byte) (string, error) {
	tmp, err := os.CreateTemp(dir, ".preset-*.tmp")
	if err != nil {
		return "", err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return "", err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

func (s *Store) installLocked(ps map[string]*Preset) ([]func(*Snapshot), *Snapshot) {
	s.seq++
	s.snap = newSnapshot(s.seq, ps)
	s.canaryNoteLocked(ps, s.extInstall)
	s.trustSyncLocked(ps)
	return s.subsLocked()
}

// deliver hands snap to the subscribers, never out of order: deliveries are
// serialised and a snapshot older than one already delivered is dropped, so a
// slow editing goroutine can never overwrite a newer snapshot downstream.
// Subscribers must not call back into the Store's mutators (they would wait
// on this very lock).
func (s *Store) deliver(subs []func(*Snapshot), snap *Snapshot) {
	s.deliverMu.Lock()
	defer s.deliverMu.Unlock()
	if snap.Seq <= s.delivered {
		return
	}
	s.delivered = snap.Seq
	for _, f := range subs {
		f(snap)
	}
}

// Undo reverts the last change; label names what was undone.
func (s *Store) Undo() (string, bool, error) { return s.step(true) }

// Redo re-applies the last undone change.
func (s *Store) Redo() (string, bool, error) { return s.step(false) }

func (s *Store) step(undo bool) (string, bool, error) {
	s.mu.Lock()
	from := s.redo
	if undo {
		from = s.undo
	}
	if len(from) == 0 {
		s.mu.Unlock()
		return "", false, nil
	}
	label, err := s.travelLocked(undo, 1)
	if err != nil {
		s.mu.Unlock()
		return "", false, err
	}
	subs, snap := s.subsLocked()
	s.mu.Unlock()
	s.deliver(subs, snap)
	s.recheckRefused()
	return label, true, nil
}

// moveLocked moves one entry between the undo and redo stacks and returns the
// snapshot to land on. cur is the snapshot the move starts from.
func (s *Store) moveLocked(undo bool, cur *Snapshot) (label string, target *Snapshot) {
	if undo {
		e := s.undo[len(s.undo)-1]
		s.undo = s.undo[:len(s.undo)-1]
		s.redo = append(s.redo, histEntry{e.label, cur, e.t})
		return e.label, e.snap
	}
	e := s.redo[len(s.redo)-1]
	s.redo = s.redo[:len(s.redo)-1]
	s.undo = append(s.undo, histEntry{e.label, cur, e.t})
	return e.label, e.snap
}

// travelLocked takes n undo (or redo) steps as ONE disk write and ONE new
// snapshot. On a write error the stacks are left exactly as they were.
func (s *Store) travelLocked(undo bool, n int) (string, error) {
	savedU, savedR := append([]histEntry(nil), s.undo...), append([]histEntry(nil), s.redo...)
	start := s.snap
	cur := start
	label := ""
	for i := 0; i < n; i++ {
		label, cur = s.moveLocked(undo, cur)
	}
	target := make(map[string]*Preset, len(cur.presets))
	for id, p := range cur.presets {
		target[id] = p.Clone()
	}
	if err := s.applyLocked(start, target); err != nil {
		s.undo, s.redo = savedU, savedR
		return "", err
	}
	s.installLocked(target)
	return label, nil
}

// subsLocked returns the subscribers and the current snapshot, for notify.
func (s *Store) subsLocked() ([]func(*Snapshot), *Snapshot) {
	subs := make([]func(*Snapshot), 0, len(s.subs))
	for _, f := range s.subs {
		if f != nil {
			subs = append(subs, f)
		}
	}
	return subs, s.snap
}

func (s *Store) checkControl(c string) error {
	if !controlRe.MatchString(c) || (s.ctl != nil && !s.ctl[c]) {
		return &actions.FieldError{Field: "control", Msg: fmt.Sprintf("%q is not a control of this deck", c)}
	}
	return nil
}

func checkBank(id, bank string, controls ...string) error {
	if !ValidBank(id, bank) {
		return &actions.FieldError{Field: "bank", Msg: fmt.Sprintf("%q is not a valid bank for this preset", bank)}
	}
	for _, c := range controls {
		if !ValidBankControl(id, bank, c) {
			return &actions.FieldError{Field: "control", Msg: fmt.Sprintf("%q cannot be set per slider mode: only P1-P8 and P1btn-P8btn take Hue / Sat / Lum assignments", c)}
		}
	}
	return nil
}

func getPreset(ps map[string]*Preset, id string) (*Preset, error) {
	if id == "global" {
		id = GlobalID
	}
	p := ps[id]
	if p == nil {
		return nil, fmt.Errorf("%w: %q", ErrNoPreset, id)
	}
	return p, nil
}

// Assign sets the whole assignment of a control (strictly validated).
func (s *Store) Assign(appID, bank, control string, assignment map[string]any) error {
	return s.commit(fmt.Sprintf("Assign %s", control), func(ps map[string]*Preset) error {
		p, err := getPreset(ps, appID)
		if err != nil {
			return err
		}
		if err := checkBank(p.ID, bank, control); err != nil {
			return err
		}
		if err := s.checkControl(control); err != nil {
			return err
		}
		a, err := s.reg.Normalize(assignment)
		if err != nil {
			return err
		}
		if err := s.reg.Validate(a, true); err != nil {
			return err
		}
		if p.Banks[bank] == nil {
			p.Banks[bank] = map[string]Assignment{}
		}
		p.Banks[bank][control] = a
		return nil
	})
}

// Unassign removes a control's assignment (it then falls back to Global).
func (s *Store) Unassign(appID, bank, control string) error {
	return s.commit(fmt.Sprintf("Clear %s", control), func(ps map[string]*Preset) error {
		p, err := getPreset(ps, appID)
		if err != nil {
			return err
		}
		if err := checkBank(p.ID, bank, control); err != nil {
			return err
		}
		delete(p.Banks[bank], control)
		if len(p.Banks[bank]) == 0 && !usesLegacyBanks(p.ID) {
			delete(p.Banks, bank)
		}
		return nil
	})
}

var commonKeys = map[string]bool{"label": true, "mode": true, "press": true}

// SetValue sets one field of a control's assignment; value nil removes it so
// it is inherited again. A control that only Global assigns is first copied
// into the app preset (action and type only) so the edit has somewhere to live.
func (s *Store) SetValue(appID, bank, control, key string, value any) error {
	return s.commit(fmt.Sprintf("Set %s %s", control, key), func(ps map[string]*Preset) error {
		p, err := getPreset(ps, appID)
		if err != nil {
			return err
		}
		if err := checkBank(p.ID, bank, control); err != nil {
			return err
		}
		if err := s.checkControl(control); err != nil {
			return err
		}
		if key == "type" || key == "action" {
			return &actions.FieldError{Field: key, Msg: "change the action with a full assignment, not a single value"}
		}
		a, ok := p.Banks[bank][control]
		if !ok {
			ga, gok := ps[GlobalID].Banks[bank][control]
			if p.ID == GlobalID || usesLegacyBanks(p.ID) || !gok {
				return fmt.Errorf("%w: %s.%s", ErrNoControl, bank, control)
			}
			a = Assignment{}
			for _, k := range []string{"type", "action"} {
				if v, ok := ga[k]; ok {
					a[k] = v
				}
			}
			if p.Banks[bank] == nil {
				p.Banks[bank] = map[string]Assignment{}
			}
			p.Banks[bank][control] = a
		}
		act := s.reg.Infer(a)
		if act.ID != actions.OtherID && !commonKeys[key] {
			if _, known := act.FieldByKey(key); !known {
				return &actions.FieldError{Field: key, Msg: fmt.Sprintf("is not a field of %q", act.Label)}
			}
		}
		if value == nil {
			delete(a, key)
			return nil
		}
		if f, known := act.FieldByKey(key); known {
			if err := actions.ValidateValue(f, value); err != nil {
				return err
			}
		} else if key == "press" {
			if err := actions.ValidateValue(actions.Field{Key: "press", Kind: "object"}, value); err != nil {
				return err
			}
		}
		a[key] = value
		return nil
	})
}

// SetSetting sets (or, with nil, clears) one preset-level setting.
func (s *Store) SetSetting(appID, key string, value any) error {
	return s.setSetting(appID, key, "Set setting "+key, func(any) (any, error) { return value, nil })
}

// UpdateSetting changes one preset-level setting from its current value inside
// ONE store change (read, modify, write cannot interleave with another edit),
// under the given history label. fn returns the new value; nil clears the key.
func (s *Store) UpdateSetting(appID, key, label string, fn func(old any) (any, error)) error {
	return s.setSetting(appID, key, label, fn)
}

func (s *Store) setSetting(appID, key, label string, fn func(old any) (any, error)) error {
	return s.commit(label, func(ps map[string]*Preset) error {
		p, err := getPreset(ps, appID)
		if err != nil {
			return err
		}
		if key == "" {
			return &actions.FieldError{Field: "key", Msg: "setting key is empty"}
		}
		value, err := fn(p.Settings[key])
		if err != nil {
			return err
		}
		if sf, known := actions.SettingFor(key); known && value != nil {
			if !sf.AppliesTo(p.ID) {
				return &actions.FieldError{Field: "key", Msg: fmt.Sprintf("%s is not a setting of %s", sf.Label, p.Name)}
			}
			if err := actions.ValidateValue(sf.Field, value); err != nil {
				return err
			}
		}
		if value == nil {
			delete(p.Settings, key)
		} else {
			p.Settings[key] = value
		}
		// the whole settings block must stay canonical and in range (400 naming the field)
		if fe := settingsIssue(p); fe != nil {
			return fe
		}
		return nil
	})
}

// SetPacks replaces the pack list of a preset (unknown pack ids rejected).
func (s *Store) SetPacks(appID string, packs []string) error {
	known := map[string]bool{}
	for _, pk := range s.reg.Packs() {
		known[pk.ID] = true
	}
	for _, pk := range packs {
		if !known[pk] {
			return &actions.FieldError{Field: "packs", Msg: fmt.Sprintf("unknown pack %q", pk)}
		}
	}
	return s.commit("Set packs", func(ps map[string]*Preset) error {
		p, err := getPreset(ps, appID)
		if err != nil {
			return err
		}
		p.Packs = append([]string{}, packs...)
		return nil
	})
}

// AddApp creates an empty preset. name/packs default from the known-apps table.
func (s *Store) AddApp(id, name string, packs []string) (*Preset, error) {
	if err := CheckAppID(id); err != nil {
		return nil, err
	}
	if name == "" {
		name = DefaultName(id)
	}
	if len(packs) == 0 {
		packs = DefaultPacks(id)
	}
	var created *Preset
	err := s.commit("Add app "+name, func(ps map[string]*Preset) error {
		if ps[id] != nil {
			return fmt.Errorf("%w: %q", ErrExists, id)
		}
		if other := foldConflict(ps, id); other != "" {
			return fmt.Errorf("%w: %q differs from the existing %q only in letter case", ErrExists, id, other)
		}
		if err := bankClash(ps, id); err != nil {
			return err
		}
		created = &Preset{Schema: SchemaVersion, ID: id, Name: name, Packs: append([]string{}, packs...),
			Banks: map[string]map[string]Assignment{}, Settings: map[string]any{}, Extra: map[string]any{}}
		ps[id] = created
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// ---- external edits --------------------------------------------------

func (s *Store) poll() {
	defer s.wg.Done()
	t := time.NewTicker(s.opts.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
			if _, err := s.CheckExternal(); err != nil {
				s.opts.Logf("preset: poll: %v", err)
			}
		}
	}
}

const (
	hashAlwaysBytes = 512 << 10
	racyWindow      = 3 * time.Second
	maxPollFiles    = 500
	maxPollBytes    = 2 << 20
)

// polled is what pollRead found out about one file, without the store lock.
type polled struct {
	st   os.FileInfo
	b    []byte
	h    [32]byte
	read bool // b and h are valid; false = the file looked untouched and was not read
	big  bool // over maxPollBytes: never read
}

// pollRead stats and reads the candidate files (and those of the refused-file
// list) outside the store's lock. A file that looks untouched (same mtime and
// size) is still read when it is small: a same-size edit inside the filesystem's
// mtime granularity changes neither. Big files are read only while their mtime
// is recent (git's "racily clean" rule).
func (s *Store) pollRead(names, extra []string, known map[string]fileState) map[string]*polled {
	out := make(map[string]*polled, len(names))
	look := func(name string, force bool) {
		if out[name] != nil && out[name].read {
			return
		}
		id := strings.TrimSuffix(name, ".json")
		full := filepath.Join(s.dir, name)
		st, err := os.Stat(full)
		if err != nil {
			return
		}
		pf := &polled{st: st}
		out[name] = pf
		if st.Size() > maxPollBytes {
			pf.big = true
			return
		}
		k, have := known[id]
		if !force && have && k.mtime.Equal(st.ModTime()) && k.size == st.Size() &&
			st.Size() > hashAlwaysBytes && s.opts.Now().Sub(st.ModTime()) > racyWindow {
			return
		}
		// Bounded read: the file may have grown since the stat, so never trust the
		// stat size. One byte over the cap = "big" (never read whole).
		f, err := os.Open(full)
		if err != nil {
			return
		}
		b, err := io.ReadAll(io.LimitReader(f, maxPollBytes+1))
		f.Close()
		if err != nil {
			return
		}
		if len(b) > maxPollBytes {
			pf.big = true
			return
		}
		pf.b, pf.h, pf.read = b, sha256.Sum256(b), true
	}
	for _, n := range names {
		look(n, false)
	}
	for _, n := range extra {
		look(n, true)
	}
	return out
}

// CheckExternal looks for preset files changed, added or removed by someone
// else (our own writes are recognised by content hash and ignored). It
// returns whether the snapshot changed. The poll goroutine calls it.
func (s *Store) CheckExternal() (bool, error) {
	// Phase 1, WITHOUT the store lock: list, stat, read and hash the files. On a
	// slow or wedged share this is where the time goes, and edits must not wait
	// for it. Capped: at most maxPollFiles files, none over maxPollBytes.
	s.mu.Lock()
	known := make(map[string]fileState, len(s.files))
	for k, v := range s.files {
		known[k] = v
	}
	extra := s.admitFailNamesLocked()
	s.mu.Unlock()
	names, err := presetFiles(s.dir)
	if err != nil {
		return false, err
	}
	allNames := names // a file beyond the cap still exists: it must not look "removed"
	if len(names) > maxPollFiles {
		s.opts.Logf("preset: %d preset files in the folder; only the first %d are looked at", len(names), maxPollFiles)
		names = names[:maxPollFiles]
	}
	pre := s.pollRead(names, extra, known)

	s.mu.Lock()
	cur := s.snap
	ps := make(map[string]*Preset, len(cur.presets))
	for id, p := range cur.presets {
		ps[id] = p // shared, read-only
	}
	changed := false
	var what []string
	var held []PendingExternal // quarantined during this pass: reported after the unlock
	flush := func() {
		if s.opts.OnQuarantine != nil {
			for _, pe := range held {
				s.opts.OnQuarantine(pe)
			}
		}
	}
	seen := map[string]bool{}
	for _, n := range allNames {
		seen[strings.TrimSuffix(n, ".json")] = true
	}
	for _, name := range names {
		id := strings.TrimSuffix(name, ".json")
		pf := pre[name]
		if pf == nil || pf.st == nil {
			continue
		}
		st := pf.st
		known, have := s.files[id]
		if pf.big {
			s.problems[name] = fmt.Sprintf("the file is larger than %d KiB: ignored (a preset is never that big)", maxPollBytes>>10)
			continue
		}
		if !pf.read { // looked untouched (see pollRead)
			continue
		}
		b, h := pf.b, pf.h
		if have && known.hash == h {
			s.files[id] = fileState{h, st.ModTime(), st.Size()}
			continue // a refused file (admitFail) is re-evaluated after the pass below
		}
		p, perr := ParsePreset(b)
		if perr == nil && p.ID+".json" != name {
			perr = fmt.Errorf("file name %s does not match preset id %q", name, p.ID)
		}
		refused := false
		if perr == nil {
			if perr = admit(ps, p); perr != nil {
				refused = true
			}
		}
		s.files[id] = fileState{h, st.ModTime(), st.Size()}
		if perr != nil {
			s.problems[name] = perr.Error()
			if refused {
				s.admitFail[name] = true
			} else {
				delete(s.admitFail, name)
			}
			s.opts.Logf("preset: external edit of %s ignored: %v", name, perr)
			continue
		}
		delete(s.admitFail, name)
		delete(s.problems, name)
		if cur0 := ps[p.ID]; cur0 != nil && !s.fallback[p.ID] && bytes.Equal(marshalOf(cur0), marshalOf(p)) {
			s.clearPendingLocked(name) // the file is the same as what is running again
			continue
		}
		if words, cats := s.externalRisk(ps, p); len(words) > 0 {
			held = append(held, s.quarantineLocked(name, b, p, "change", words, cats))
			continue // not applied; the file stays as it is on disk
		}
		s.clearPendingLocked(name)
		delete(s.fallback, p.ID)
		ps[p.ID] = p
		changed = true
		what = append(what, p.ID)
	}
	for name := range s.problems { // a broken file that is gone is no longer a problem
		if !seen[strings.TrimSuffix(name, ".json")] {
			if s.fallback[strings.TrimSuffix(name, ".json")] {
				s.problems[name] = "the file is missing; the shipped default is loaded read-only"
			} else {
				delete(s.problems, name)
			}
		}
	}
	for id := range cur.presets {
		if !seen[id] && id != GlobalID && !s.fallback[id] {
			if _, tracked := s.files[id]; tracked {
				if hasPanicKey(cur.presets[id]) { // losing the panic key from outside is held back
					file := id + ".json"
					already := false
					for _, pe := range s.pending {
						if pe.File == file && pe.Kind == "removal" {
							already = true
						}
					}
					if !already {
						held = append(held, s.quarantineLocked(file, nil, nil, "removal",
							[]string{fmt.Sprintf("the file of %s, which holds a panic key, was deleted", cur.presets[id].Name)}, []string{"panic-removed"}))
					}
					continue
				}
				s.clearPendingLocked(id + ".json")
				delete(ps, id)
				delete(s.files, id)
				changed = true
				what = append(what, id+" (removed)")
			}
		}
	}
	// Refused files (compose collision, case clash) are judged again against
	// the presets as they are NOW, bytes unchanged: the colliding preset may
	// have just been removed above, by us or by someone else.
	for _, name := range s.admitFailNamesLocked() {
		id := strings.TrimSuffix(name, ".json")
		if !seen[id] {
			delete(s.admitFail, name)
			continue
		}
		pf := pre[name]
		if pf == nil || !pf.read {
			continue
		}
		b := pf.b
		p, perr := ParsePreset(b)
		if perr == nil && p.ID+".json" != name {
			perr = fmt.Errorf("file name %s does not match preset id %q", name, p.ID)
		}
		if perr == nil {
			perr = admit(ps, p)
		}
		if perr != nil {
			if s.problems[name] != perr.Error() {
				s.problems[name] = perr.Error()
				s.opts.Logf("preset: %s is still refused: %v", name, perr)
			}
			continue
		}
		delete(s.admitFail, name)
		delete(s.problems, name)
		if words, cats := s.externalRisk(ps, p); len(words) > 0 {
			held = append(held, s.quarantineLocked(name, b, p, "change", words, cats))
			continue
		}
		delete(s.fallback, p.ID)
		ps[p.ID] = p
		changed = true
		what = append(what, p.ID)
	}
	if !changed {
		s.mu.Unlock()
		flush()
		return false, nil
	}
	sort.Strings(what)
	s.undo = append(s.undo, histEntry{"External edit: " + strings.Join(what, ", "), cur, s.opts.Now()})
	if len(s.undo) > s.opts.MaxUndo {
		s.undo = s.undo[len(s.undo)-s.opts.MaxUndo:]
	}
	s.redo = nil
	s.extInstall = true
	subs, snap := s.installLocked(ps)
	s.extInstall = false
	s.mu.Unlock()
	s.deliver(subs, snap)
	flush()
	return true, nil
}

// hasPanicKey says whether any assignment of p is the panic disarm action.
func hasPanicKey(p *Preset) bool {
	if p == nil {
		return false
	}
	for _, bm := range p.Banks {
		for _, a := range bm {
			if actions.EffectiveType(a) == "disarm" {
				return true
			}
		}
	}
	return false
}

// admitFailNamesLocked lists the refused-but-parseable preset files, sorted.
func (s *Store) admitFailNamesLocked() []string {
	out := make([]string, 0, len(s.admitFail))
	for n := range s.admitFail {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// recheckRefused re-evaluates refused files after a successful commit, undo
// or history jump (a removed or edited preset may have lifted the collision).
func (s *Store) recheckRefused() {
	s.mu.Lock()
	n := len(s.admitFail)
	s.mu.Unlock()
	if n == 0 {
		return
	}
	if _, err := s.CheckExternal(); err != nil {
		s.opts.Logf("preset: recheck: %v", err)
	}
}

// admit says whether p may join ps: its id is allowed (CheckStoredID), it does
// not differ from another preset's id only in letter case, and the deck can
// still compose its configuration with it. The error becomes the problem text
// of a file that is refused (it stays untouched on disk).
func admit(ps map[string]*Preset, p *Preset) error {
	if err := CheckStoredID(p.ID); err != nil {
		return fmt.Errorf("%v", err)
	}
	if other := foldConflict(ps, p.ID); other != "" {
		return fmt.Errorf("preset id %q differs from %q only in letter case", p.ID, other)
	}
	// Stored presets use the canonical field spellings only: the engine's decoder
	// matches names case-insensitively, so "Cmd" or "KEY" would be read by the deck
	// but not by the editor's classifier.
	for _, bn := range sortedKeys(p.Banks) {
		for _, cn := range sortedKeys(p.Banks[bn]) {
			if nc := config.NonCanonicalKeys(p.Banks[bn][cn]); len(nc) > 0 {
				return fmt.Errorf("%s.%s: %v", bn, cn, nc[0])
			}
		}
	}
	if fe := settingsIssue(p); fe != nil {
		return fmt.Errorf("settings: %v", fe)
	}
	trial := make(map[string]*Preset, len(ps)+1)
	for id, q := range ps {
		trial[id] = q
	}
	trial[p.ID] = p
	if err := composeCheck(trial); err != nil {
		return fmt.Errorf("%v: the deck could not build its configuration with this file", err)
	}
	return nil
}

// restrictDir sets the presets folder to mode 0700, best effort: on a shared
// volume ownership and modes may be ignored, so a failure is only logged.
func (s *Store) restrictDir() {
	if s.readonly {
		return
	}
	if fi, err := os.Stat(s.dir); err == nil && fi.Mode().Perm() == 0o700 {
		return
	}
	if err := os.Chmod(s.dir, 0o700); err != nil {
		s.opts.Logf("preset: could not set the presets folder to mode 0700 (%v); other accounts on the share may still read it", err)
		return
	}
	s.opts.Logf("preset: presets folder set to mode 0700")
}
