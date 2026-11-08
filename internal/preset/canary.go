package preset

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Silent-loss canary and pre-update snapshot (wave F).
//
// BTT, Loupedeck and Stream Deck all lost users' assignments through a
// version or migration step and nobody noticed until later. The store's
// atomic writes, undo and dated backups protect against loss at SAVE time,
// not against "something dropped 40 assignments while the app was closed".
//
// <presets dir>/.canary.json records, per preset, the set of assigned
// controls ("bank.control") as of the last change the store itself made (or
// the last outside change it saw), plus the build id and preset schema that
// wrote it. It is written debounced and atomically after every committed
// change, and at close. It is a dot file: never part of a backup, export or
// import, and never listed as a preset.
//
//   - At open the file is compared with what loaded. A preset that lost
//     controls with no change of the store's own to explain it (the store
//     leaves a .history copy whenever it rewrites a file, newer than the
//     canary's own timestamp) is reported as a canary: GET /api/warnings and
//     /api/live engine.canary, until POST /api/canary/ack dismisses it.
//   - The same comparison runs when the poll sees an OUTSIDE edit.
//   - When the build id or schema differs from the recorded one, a backup of
//     reason "pre-update" is made before the store writes anything. The
//     newest 3 are kept apart from the other backup classes.

// BuildID identifies the running build. app/editor.go sets it from a
// build-time stamp (or the executable's mtime) before Open; tests inject it.
// "" disables the pre-update backup for installs that predate the canary.
var BuildID string

const (
	canaryFileName = ".canary.json"
	canarySchema   = 1
	preUpdateKeep  = 3
	// defaultCanaryDebounce is how long the canary file waits for a quiet
	// moment after a change (Options.CanaryDebounce 0 = this; <0 = write at once).
	defaultCanaryDebounce = 400 * time.Millisecond
	maxLostControls       = 50
)

// CanaryLoss is one preset that lost assignments.
type CanaryLoss struct {
	Preset   string   `json:"preset"`
	Before   int      `json:"before"`
	After    int      `json:"after"`
	Controls []string `json:"controls,omitempty"` // "bank.control" that are gone (at most 50)
}

// CanaryInfo is a pending, unacknowledged loss.
type CanaryInfo struct {
	Lost  []CanaryLoss `json:"lost"`
	Since int64        `json:"since"` // Unix ms the loss was noticed
	// Backup is the newest existing backup that still holds the lost
	// assignments (or the newest backup); "" when there is none. Restoring it
	// (POST /api/backups/{name}/restore) makes a restore backup first and is
	// one undo step.
	Backup string `json:"backup,omitempty"`
	Hint   string `json:"hint,omitempty"`
}

type canaryFile struct {
	Schema       int                 `json:"schema"`
	Build        string              `json:"build"`
	PresetSchema int                 `json:"presetSchema"`
	Written      int64               `json:"written"`
	Presets      map[string][]string `json:"presets"`
	Pending      *CanaryInfo         `json:"pending,omitempty"`
}

type canaryState struct {
	mu      sync.Mutex
	on      bool
	base    map[string][]string
	pending *CanaryInfo
	dirty   bool
	closed  bool
	timer   *time.Timer
}

func (s *Store) canaryPath() string { return filepath.Join(s.dir, canaryFileName) }

func controlsOf(p *Preset) []string {
	if p == nil {
		return nil
	}
	var out []string
	for bank, m := range p.Banks {
		for c := range m {
			out = append(out, bank+"."+c)
		}
	}
	sort.Strings(out)
	return out
}

func readCanaryFile(path string) *canaryFile {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cf canaryFile
	if json.Unmarshal(b, &cf) != nil || cf.Schema != canarySchema || cf.Presets == nil {
		return nil
	}
	return &cf
}

// preUpdate makes the pre-update backup when the recorded build or schema
// differs from the running one. Called by Open before the store writes
// anything; cf is the canary file as found (nil when missing or unreadable).
// An install from before the canary existed (no file, presets present) is
// covered once, but only when a build id is known.
func (s *Store) preUpdate(cf *canaryFile, havePresets bool) {
	if s.readonly || !havePresets {
		return
	}
	need := BuildID != ""
	if cf != nil {
		need = cf.Build != BuildID || cf.PresetSchema != SchemaVersion
	}
	if !need {
		return
	}
	// The start is a first start of a new build whether or not the safety copy
	// could be made: a failed backup must not lead to an ARMED start.
	s.preUpdateMade = true
	if _, err := s.backupLocked("pre-update"); err != nil {
		s.opts.Logf("preset: pre-update backup FAILED (the app will still start disarmed): %v", err)
	}
}

// ---- start-disarmed policy (wave G, B2) ---------------------------------------------------

// FirstRunMarker is the dot file in the presets dir that records "the user has
// armed the deck once with this install". It is never part of a backup or export.
const FirstRunMarker = ".first-run-done"

// EnvStartDisarmed forces a disarmed start when set to "1".
const EnvStartDisarmed = "DECKPLUS_START_DISARMED"

// EnvStartDisarmedLegacy is the name this variable had before the app was
// renamed to 2T5 DECK+. It is still honoured (disarming is the safe direction,
// so either name set to "1" is enough).
const EnvStartDisarmedLegacy = "LD_DECK_START_DISARMED"

// DisarmedMarker is the dot file in the presets dir that records "the deck was
// disarmed on purpose and not armed again": the next launch of ANY build starts
// disarmed while it exists. A deliberate disarm (POST /api/arm {armed:false},
// the panic key) writes it, arming removes it. Never part of a backup or export.
const DisarmedMarker = ".disarmed"

// Start reasons (GET /api/live startReason), in the order they are tried.
const (
	StartReasonEnv          = "env"                   // DECKPLUS_START_DISARMED=1
	StartReasonStoreDown    = "store-unavailable"     // the presets could not be opened: no editor, deck held disarmed (logged; there is no API to report it)
	StartReasonReadOnly     = "read-only"             // another 2T5 DECK+ holds the presets lock
	StartReasonDisarmedLast = "disarmed-last-session" // .disarmed exists: it was left disarmed
	StartReasonStateDown    = "state-unavailable"     // the internal state folder cannot be used: held disarmed
	StartReasonExternal     = "external-changes"      // Open held back a risky change found in the presets folder
	StartReasonFirstRun     = "first-run"             // never armed by the user on this install
	StartReasonPreUpdate    = "pre-update"            // new build or schema (also when its safety backup failed)
)

// StartDisarmed says whether the app must start with the deck DISARMED, and why
// (first match, in this order): getenv(DECKPLUS_START_DISARMED or the old LD_DECK_START_DISARMED) == "1" ("env");
// this store is a read-only second instance ("read-only"); the deck was left
// disarmed on purpose ("disarmed-last-session"); no .first-run-done marker
// ("first-run"); this very Open met a new build or schema ("pre-update"). The
// markers are the only things persisted.
func (s *Store) StartDisarmed(getenv func(string) string) (bool, string) {
	if getenv != nil && (getenv(EnvStartDisarmed) == "1" || getenv(EnvStartDisarmedLegacy) == "1") {
		return true, StartReasonEnv
	}
	if s.readonly {
		return true, StartReasonReadOnly
	}
	if s.stateErr != nil {
		return true, StartReasonStateDown
	}
	if s.externalAtOpen {
		return true, StartReasonExternal
	}
	if s.markerExists(DisarmedMarker, true) { // EITHER place saying "disarmed" is enough
		return true, StartReasonDisarmedLast
	}
	if !s.markerExists(FirstRunMarker, false) { // with a state dir only the INTERNAL copy counts
		return true, StartReasonFirstRun
	}
	if s.preUpdateMade {
		return true, StartReasonPreUpdate
	}
	return false, ""
}

// markerExists looks for a marker. either: true = present in the presets folder
// OR the internal state folder; false = the authoritative place only (the
// internal folder when one is configured, else the presets folder).
func (s *Store) markerExists(name string, either bool) bool {
	share := func() bool { _, err := os.Stat(filepath.Join(s.dir, name)); return err == nil }
	if s.opts.StateDir == "" {
		return share()
	}
	_, err := os.Stat(filepath.Join(s.opts.StateDir, name))
	internal := err == nil
	if either {
		return internal || share()
	}
	return internal
}

// initStateDir creates and probes the internal state folder (0700).
func (s *Store) initStateDir() {
	if s.opts.StateDirErr != nil {
		s.stateErr = s.opts.StateDirErr
		return
	}
	d := s.opts.StateDir
	if d == "" || s.readonly {
		return
	}
	if err := os.MkdirAll(d, 0o700); err != nil {
		s.stateErr = err
		return
	}
	_ = os.Chmod(d, 0o700)
	probe := filepath.Join(d, ".probe")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		s.stateErr = err
		return
	}
	os.Remove(probe)
}

// writeMarker writes a marker to the internal folder (when set) and the presets
// folder; the first error is returned but both are attempted.
func (s *Store) writeMarker(name, text string) error {
	var first error
	for _, dir := range s.markerDirs() {
		p := filepath.Join(dir, name)
		if _, err := os.Stat(p); err == nil {
			continue
		}
		if err := WriteAtomic(p, []byte(text)); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (s *Store) removeMarker(name string) error {
	var first error
	for _, dir := range s.markerDirs() {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) && first == nil {
			first = err
		}
	}
	return first
}

func (s *Store) markerDirs() []string {
	if s.opts.StateDir != "" {
		return []string{s.opts.StateDir, s.dir}
	}
	return []string{s.dir}
}

// MarkDisarmed records a deliberate disarm (see DisarmedMarker) in the internal
// state folder (when set) and the presets folder. Idempotent; a read-only store
// writes nothing.
func (s *Store) MarkDisarmed() error {
	if s.readonly {
		return nil
	}
	return s.writeMarker(DisarmedMarker, "the deck was disarmed on purpose\n")
}

// ClearDisarmed removes the disarmed marker everywhere (the user armed the deck).
func (s *Store) ClearDisarmed() error {
	if s.readonly {
		return nil
	}
	return s.removeMarker(DisarmedMarker)
}

// MarkFirstRunDone records that the user armed the deck once (POST /api/arm
// {armed:true}). Idempotent; a read-only store writes nothing. With a StateDir
// only the internal copy counts for the start policy; the presets-folder copy is
// kept for older builds.
func (s *Store) MarkFirstRunDone() error {
	if s.readonly {
		return nil
	}
	return s.writeMarker(FirstRunMarker, "the deck was armed once by the user\n")
}

// canaryOpen runs at the end of Open (single-threaded): compare, merge the
// pending state, take the new baseline and write it.
func (s *Store) canaryOpen(cf *canaryFile, ps map[string]*Preset) {
	if s.readonly {
		return
	}
	c := &s.can
	c.mu.Lock()
	defer c.mu.Unlock()
	c.on = true
	if cf != nil {
		c.pending = cf.Pending
		lost := s.lossAgainst(cf.Presets, ps, cf.Written)
		if len(lost) > 0 {
			c.pending = mergePending(c.pending, lost, s.opts.Now().UnixMilli())
		}
	}
	c.pending = prunePending(c.pending, ps)
	c.base = s.baselineLocked(ps, c.base)
	if cf != nil && cf.Presets != nil {
		// presets whose file is unreadable keep their old record
		for id, v := range cf.Presets {
			if _, ok := c.base[id]; !ok && s.problems[id+".json"] != "" {
				c.base[id] = v
			}
		}
	}
	if c.pending != nil {
		s.fillHint(c.pending)
	}
	changed := cf == nil || cf.Build != BuildID || cf.PresetSchema != SchemaVersion || !sameBase(cf.Presets, c.base) || !samePending(cf.Pending, c.pending)
	if changed {
		c.dirty = true
		s.writeCanaryLocked()
	}
}

func sameBase(a, b map[string][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || strings.Join(v, ",") != strings.Join(w, ",") {
			return false
		}
	}
	return true
}

func samePending(a, b *CanaryInfo) bool {
	if a == nil || b == nil {
		return a == b
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// baselineLocked is the per-preset control record of the loaded presets.
func (s *Store) baselineLocked(ps map[string]*Preset, old map[string][]string) map[string][]string {
	out := make(map[string][]string, len(ps))
	for id, p := range ps {
		out[id] = controlsOf(p)
	}
	for id, v := range old { // a preset whose file is unreadable keeps its record
		if _, ok := out[id]; !ok && s.problems[id+".json"] != "" {
			out[id] = v
		}
	}
	return out
}

// lossAgainst lists presets that lost controls relative to base. A preset the
// store itself rewrote after canaryWrittenMs (it leaves a .history copy) is
// not a silent loss; a preset whose file is unreadable is already an error.
func (s *Store) lossAgainst(base map[string][]string, ps map[string]*Preset, writtenMs int64) []CanaryLoss {
	var out []CanaryLoss
	ids := make([]string, 0, len(base))
	for id := range base {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if s.problems[id+".json"] != "" {
			continue
		}
		cur := map[string]bool{}
		for _, c := range controlsOf(ps[id]) {
			cur[c] = true
		}
		var gone []string
		for _, c := range base[id] {
			if !cur[c] {
				gone = append(gone, c)
			}
		}
		if len(gone) == 0 {
			continue
		}
		if writtenMs > 0 && s.historyNewer(id, writtenMs) {
			continue
		}
		if len(gone) > maxLostControls {
			gone = gone[:maxLostControls]
		}
		out = append(out, CanaryLoss{Preset: id, Before: len(base[id]), After: len(cur), Controls: gone})
	}
	return out
}

// historyNewer says whether the store saved a .history copy of id after ms.
func (s *Store) historyNewer(id string, ms int64) bool {
	es, err := os.ReadDir(filepath.Join(s.dir, ".history", id))
	if err != nil {
		return false
	}
	for _, e := range es {
		if fi, err := e.Info(); err == nil && fi.ModTime().UnixMilli() > ms {
			return true
		}
	}
	return false
}

// mergePending adds new losses to an existing pending state: the earliest
// "before" and the newest "after" per preset.
// prunePending drops a recorded loss that no longer holds: the preset is loaded again with every control that was
// listed as gone, and at least as many as it had before. A loss notice left over from an earlier launch (for
// example one caused by a wrongly quarantined seed) must not keep claiming "91 -> 0" while 91 are assigned.
func prunePending(p *CanaryInfo, ps map[string]*Preset) *CanaryInfo {
	if p == nil {
		return nil
	}
	out := &CanaryInfo{Since: p.Since, Backup: p.Backup, Hint: p.Hint}
	for _, l := range p.Lost {
		cur := controlsOf(ps[l.Preset])
		have := make(map[string]bool, len(cur))
		for _, c := range cur {
			have[c] = true
		}
		restored := ps[l.Preset] != nil && len(cur) >= l.Before && len(l.Controls) > 0
		for _, c := range l.Controls {
			if !have[c] {
				restored = false
			}
		}
		if !restored {
			out.Lost = append(out.Lost, l)
		}
	}
	if len(out.Lost) == 0 {
		return nil
	}
	return out
}

func mergePending(old *CanaryInfo, lost []CanaryLoss, now int64) *CanaryInfo {
	if old == nil {
		return &CanaryInfo{Lost: lost, Since: now}
	}
	out := &CanaryInfo{Since: old.Since, Lost: append([]CanaryLoss(nil), old.Lost...)}
	for _, l := range lost {
		merged := false
		for i := range out.Lost {
			if out.Lost[i].Preset != l.Preset {
				continue
			}
			o := out.Lost[i]
			seen := map[string]bool{}
			for _, c := range o.Controls {
				seen[c] = true
			}
			for _, c := range l.Controls {
				if !seen[c] && len(o.Controls) < maxLostControls {
					o.Controls = append(o.Controls, c)
				}
			}
			o.After = l.After
			out.Lost[i] = o
			merged = true
		}
		if !merged {
			out.Lost = append(out.Lost, l)
		}
	}
	sort.Slice(out.Lost, func(i, j int) bool { return out.Lost[i].Preset < out.Lost[j].Preset })
	return out
}

// canaryNoteLocked is called under s.mu whenever a new snapshot is installed.
// external: the change came from outside the store (the poll), so a drop in
// assignments is a canary; the store's own changes (edits, undo, import,
// restore) are recorded undoable changes and only move the baseline.
func (s *Store) canaryNoteLocked(ps map[string]*Preset, external bool) {
	c := &s.can
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.on || c.closed {
		return
	}
	if external {
		if lost := s.lossAgainst(c.base, ps, 0); len(lost) > 0 {
			c.pending = mergePending(c.pending, lost, s.opts.Now().UnixMilli())
			s.fillHint(c.pending)
		}
	}
	c.base = s.baselineLocked(ps, c.base)
	c.dirty = true
	d := s.opts.CanaryDebounce
	switch {
	case d < 0:
		s.writeCanaryLocked()
	default:
		if d == 0 {
			d = defaultCanaryDebounce
		}
		if c.timer == nil {
			c.timer = time.AfterFunc(d, s.canaryTimer)
		} else {
			c.timer.Reset(d)
		}
	}
}

func (s *Store) canaryTimer() {
	c := &s.can
	c.mu.Lock()
	defer c.mu.Unlock()
	s.writeCanaryLocked()
}

// writeCanaryLocked writes the file if dirty. Caller holds can.mu.
func (s *Store) writeCanaryLocked() {
	c := &s.can
	if !c.dirty {
		return
	}
	cf := canaryFile{Schema: canarySchema, Build: BuildID, PresetSchema: SchemaVersion,
		Written: s.opts.Now().UnixMilli(), Presets: c.base, Pending: c.pending}
	b, err := json.Marshal(cf)
	if err == nil {
		err = WriteAtomic(s.canaryPath(), b)
	}
	if err != nil {
		s.opts.Logf("preset: canary write: %v", err)
		return
	}
	c.dirty = false
}

// FlushCanary writes the canary file now if a change is waiting for the debounce.
func (s *Store) FlushCanary() {
	c := &s.can
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.timer != nil {
		c.timer.Stop()
	}
	if c.on {
		s.writeCanaryLocked()
	}
}

func (s *Store) canaryClose() {
	s.FlushCanary()
	c := &s.can
	c.mu.Lock()
	c.on, c.closed = false, true
	c.mu.Unlock()
}

// Canary returns the pending silent-loss report, nil when there is none.
func (s *Store) Canary() *CanaryInfo {
	c := &s.can
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil {
		return nil
	}
	cp := *c.pending
	cp.Lost = append([]CanaryLoss(nil), c.pending.Lost...)
	if cp.Backup != "" { // a backup that has been pruned since is no hint
		if _, err := os.Stat(filepath.Join(s.backupsDir(), cp.Backup)); err != nil {
			s.fillHint(&cp)
		}
	}
	return &cp
}

// AckCanary dismisses the pending report ("this was intentional"). had is
// false when nothing was pending.
func (s *Store) AckCanary() (had bool, err error) {
	if err := s.roErr(); err != nil {
		return false, err
	}
	c := &s.can
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil {
		return false, nil
	}
	c.pending = nil
	c.dirty = true
	s.writeCanaryLocked()
	return true, nil
}

// fillHint names the newest backup that still holds ALL the lost assignments
// (its count for every lost preset is at least "before"). When no backup does
// (dailies are made before the first write of a day; a pre-update backup is
// made after the damage), it says so and points at the per-preset history.
func (s *Store) fillHint(p *CanaryInfo) {
	p.Backup, p.Hint = "", ""
	bs := s.Backups()
	for i, b := range bs {
		if i >= 30 || len(p.Lost) == 0 {
			break
		}
		ok := true
		for _, l := range p.Lost {
			if backupCount(filepath.Join(s.backupsDir(), b.Name), l.Preset) < l.Before {
				ok = false
				break
			}
		}
		if ok {
			p.Backup = b.Name
			break
		}
	}
	if p.Backup != "" {
		p.Hint = fmt.Sprintf("Restore %s from Backups: it holds the lost assignments, makes a restore backup first and is one undo step. Dismiss this if the change was intentional.", p.Backup)
		return
	}
	p.Hint = "No backup holds the lost assignments. Look in .history/<preset id>/ (the store keeps the last versions of every preset it rewrote) or, if the file is unreadable, use POST /api/preset/{id}/restore-last-good. Dismiss this if the change was intentional."
}

// backupCount is the number of assignments preset id holds in a backup zip, -1 when absent.
func backupCount(path, id string) int {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return -1
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != id+".json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return -1
		}
		b, _ := io.ReadAll(io.LimitReader(rc, maxEntryBytes))
		rc.Close()
		p, err := ParsePreset(b)
		if err != nil {
			return -1
		}
		return len(controlsOf(p))
	}
	return -1
}
