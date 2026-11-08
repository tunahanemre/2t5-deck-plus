package preset

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"2t5-deck-plus/internal/actions"
)

// Export / import / dated backups of the presets directory.
//
// A bundle is a zip holding a manifest.json plus one <id>.json per preset
// (presets only; .history, .icons, .backups and .seed are never included).
// Backups are bundles too, so a backup can be imported like any export.

// Bundle constants.
const (
	BundleKind = "2t5-deck-plus-presets"
	// LegacyBundleKind is the kind the exports carried before the app was renamed
	// to 2T5 DECK+. Imports still accept it; exports always write BundleKind.
	LegacyBundleKind = "ld-deck-presets"
	BundleSchema     = 1

	maxBundleEntries = 200
	maxEntryBytes    = 2 << 20
	maxBundleBytes   = 16 << 20
	defaultKeepBack  = 14
)

// Manifest is manifest.json inside a bundle.
type Manifest struct {
	Kind    string `json:"kind"`
	Schema  int    `json:"schema"`
	Created string `json:"created"`
	App     string `json:"app"` // "all" or a preset id
	Reason  string `json:"reason,omitempty"`
	// Label is the name the user gave a manual snapshot ("before the shoot").
	Label string   `json:"label,omitempty"`
	Files []string `json:"files"`
	// Skipped lists preset files that were on disk but unreadable (not valid
	// presets) when the bundle was made; they are left out of it.
	Skipped []string `json:"skipped,omitempty"`
	// MadeWith / MadeWithURL are a plain-text credit for whoever opens the zip. Nothing reads them:
	// older apps ignore unknown manifest fields, so adding them breaks no import in either direction.
	MadeWith    string `json:"madeWith,omitempty"`
	MadeWithURL string `json:"madeWithUrl,omitempty"`
}

// The credit written into every bundle's manifest (exports and backups).
const (
	MadeWith    = "Made with 2T5 DECK+ · 2T5 Film"
	MadeWithURL = "https://2t5.co"
)

// ImportError lists everything wrong with a bundle (nothing is imported then).
type ImportError struct{ Problems []string }

func (e *ImportError) Error() string {
	return "import refused: " + strings.Join(e.Problems, "; ")
}

// Backup names: presets-YYYYMMDD-HHMMSS[-class][-n].zip. class is daily,
// import, restore or manual; older backups carry none (their class is read
// from the manifest). -n only breaks a name collision.
var backupRe = regexp.MustCompile(`^presets-\d{8}-\d{6}(-(daily|import|restore|manual|pre-update))?(-\d+)?\.zip$`)

// Backup classes: dailies are pruned separately from everything else, so a
// burst of imports or restores can never push the daily history out.
const (
	classDaily     = "daily"
	classOther     = "other"
	classPreUpdate = "pre-update" // wave F: kept apart, newest 3 (canary.go)
)

// ErrNoBackup is returned for an unknown backup name.
var ErrNoBackup = errors.New("no such backup")

func (s *Store) backupsDir() string { return filepath.Join(s.dir, ".backups") }

func zipBundle(files map[string][]byte, app, reason string, now time.Time, skipped ...string) ([]byte, error) {
	return zipBundleLabel(files, app, reason, "", now, skipped...)
}

func zipBundleLabel(files map[string][]byte, app, reason, label string, now time.Time, skipped ...string) ([]byte, error) {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	mb, _ := json.MarshalIndent(Manifest{Kind: BundleKind, Schema: BundleSchema, Created: now.Format(time.RFC3339),
		App: app, Reason: reason, Label: label, Files: names, Skipped: skipped, MadeWith: MadeWith, MadeWithURL: MadeWithURL}, "", "  ")
	all := []struct {
		n string
		b []byte
	}{{"manifest.json", append(mb, '\n')}}
	for _, n := range names {
		all = append(all, struct {
			n string
			b []byte
		}{n, files[n]})
	}
	for _, f := range all {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: f.n, Method: zip.Deflate, Modified: now})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.b); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Export renders the current presets as a bundle. app is "all" (or "") or a
// preset id. The second result is a suggested file name.
func (s *Store) Export(app string) ([]byte, string, error) {
	snap := s.Snapshot()
	files := map[string][]byte{}
	label := "all"
	if app == "" || app == "all" {
		for _, p := range snap.Presets() {
			b, err := p.Marshal()
			if err != nil {
				return nil, "", err
			}
			files[p.ID+".json"] = b
		}
	} else {
		p, ok := snap.Preset(app)
		if !ok {
			return nil, "", fmt.Errorf("%w: %q", ErrNoPreset, app)
		}
		b, err := p.Marshal()
		if err != nil {
			return nil, "", err
		}
		files[p.ID+".json"] = b
		label = p.ID
	}
	now := s.opts.Now()
	data, err := zipBundle(files, label, "export", now)
	if err != nil {
		return nil, "", err
	}
	return data, fmt.Sprintf("2t5-deck-plus-presets-%s-%s.zip", label, now.Format("20060102")), nil
}

// ---- backups -----------------------------------------------------------

// BackupInfo describes one dated backup.
type BackupInfo struct {
	Name   string    `json:"name"`
	Size   int64     `json:"size"`
	Time   time.Time `json:"time"`
	Reason string    `json:"reason"` // daily | import | restore | manual | unknown
	// Label is the user's name for a manual snapshot (absent when none).
	Label string `json:"label,omitempty"`
	// Trusted: this app made it (or there is no trust store). A zip that is not
	// ours, for example one planted in the shared folder, restores like an import.
	Trusted bool `json:"trusted"`
}

// backupReason reads the class word from a backup name ("" for old names).
func backupReason(name string) string {
	m := backupRe.FindStringSubmatch(name)
	if m == nil {
		return ""
	}
	return m[2]
}

func parseBackupTime(name string) time.Time {
	t, err := time.ParseInLocation("20060102-150405", strings.TrimSuffix(strings.TrimPrefix(name, "presets-"), ".zip")[:15], time.Local)
	if err != nil {
		return time.Time{}
	}
	return t
}

// Backups lists the dated backups, newest first.
func (s *Store) Backups() []BackupInfo {
	es, err := os.ReadDir(s.backupsDir())
	out := []BackupInfo{}
	if err != nil {
		return out
	}
	hashed := 0
	for _, e := range es {
		if e.IsDir() || !backupRe.MatchString(e.Name()) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		reason := backupReason(e.Name())
		if reason == "" {
			reason = s.manifestReason(e.Name())
		}
		bi := BackupInfo{Name: e.Name(), Size: fi.Size(), Time: parseBackupTime(e.Name()), Reason: reason, Trusted: true}
		if s.trustDir() != "" {
			bi.Trusted = s.backupTrustedCached(e.Name(), fi, &hashed)
		}
		if reason == "manual" {
			bi.Label = s.manifestLabel(e.Name())
		}
		out = append(out, bi)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Time.Equal(out[j].Time) {
			return out[i].Time.After(out[j].Time)
		}
		return out[i].Name > out[j].Name
	})
	return out
}

// manifestReason reads "reason" from an old-style backup's manifest.
func (s *Store) manifestReason(name string) string {
	zr, err := zip.OpenReader(filepath.Join(s.backupsDir(), name))
	if err != nil {
		return "unknown"
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != "manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			break
		}
		body, _ := io.ReadAll(io.LimitReader(rc, 1<<16))
		rc.Close()
		var m Manifest
		if json.Unmarshal(body, &m) == nil && m.Reason != "" {
			return m.Reason
		}
	}
	return "unknown"
}

// manifestLabel reads "label" from a backup's manifest ("" when none).
func (s *Store) manifestLabel(name string) string {
	zr, err := zip.OpenReader(filepath.Join(s.backupsDir(), name))
	if err != nil {
		return ""
	}
	defer zr.Close()
	for _, f := range zr.File {
		if f.Name != "manifest.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return ""
		}
		body, _ := io.ReadAll(io.LimitReader(rc, 1<<16))
		rc.Close()
		var m Manifest
		if json.Unmarshal(body, &m) == nil {
			return m.Label
		}
	}
	return ""
}

// CleanLabel trims a snapshot label, drops control characters and cuts it to 80
// characters; "" when nothing is left.
func CleanLabel(l string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(l) {
		// control characters and every invisible formatting character (bidi
		// overrides U+202A-202E / U+2066-2069, zero-width, BOM, line and paragraph
		// separators): a label must read the way it is stored
		if r < 0x20 || r == 0x7f || unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp) {
			continue
		}
		b.WriteRune(r)
	}
	out := []rune(strings.TrimSpace(b.String()))
	if len(out) > 80 {
		out = out[:80]
	}
	return string(out)
}

func classOfReason(reason string) string {
	switch reason {
	case classDaily:
		return classDaily
	case classPreUpdate:
		return classPreUpdate
	}
	return classOther
}

func (s *Store) initBackupDay() {
	day := s.opts.Now().Format("20060102")
	for _, b := range s.Backups() {
		if b.Reason == classDaily && strings.HasPrefix(b.Name, "presets-"+day+"-") {
			s.lastBackupDay = day
			return
		}
	}
}

// dailyBackupLocked backs up before the first write of each calendar day.
// The day is marked done only once a backup file really exists; a failure is
// logged, never blocks the edit, and is retried by the next write.
func (s *Store) dailyBackupLocked() {
	day := s.opts.Now().Format("20060102")
	if s.lastBackupDay == day {
		return
	}
	made, err := s.backupLocked(classDaily)
	if err != nil {
		s.opts.Logf("preset: backup: %v (will retry on the next write)", err)
		return
	}
	if made {
		s.lastBackupDay = day
	}
}

// BackupNow makes a manual backup of the preset files on disk (not an undo
// step). ok is false when there is nothing to back up.
func (s *Store) BackupNow() (BackupInfo, bool, error) { return s.BackupNowLabelled("") }

// BackupNowLabelled is BackupNow with a name for the snapshot (stored in its
// manifest and listed in Backups).
func (s *Store) BackupNowLabelled(label string) (BackupInfo, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.roErr(); err != nil {
		return BackupInfo{}, false, err
	}
	s.pendingLabel = CleanLabel(label)
	made, err := s.backupLocked("manual")
	s.pendingLabel = ""
	if err != nil || !made {
		return BackupInfo{}, false, err
	}
	bs := s.Backups()
	for _, b := range bs {
		if b.Reason == "manual" {
			return b, true, nil
		}
	}
	return BackupInfo{}, true, nil
}

// backupLocked zips the preset files currently on disk into .backups/ and
// prunes each class (daily / everything else) to its newest 14 on its own.
// made is false when there was nothing to back up (no file written).
func (s *Store) backupLocked(reason string) (made bool, err error) {
	switch reason {
	case "daily", "import", "restore", "manual", "pre-update":
	default:
		reason = "manual"
	}
	files := map[string][]byte{}
	var skipped []string
	if s.snap != nil {
		// Built from what is RUNNING, never from the bytes on disk: the folder may hold
		// outside content that was held back, and a backup must not carry it.
		for _, id := range sortedKeys(s.snap.presets) {
			files[id+".json"] = marshalOf(s.snap.presets[id])
		}
		// files on disk that are not part of what runs: unreadable ones are left out
		// and listed (as before); readable ones the app has not accepted (an outside
		// file waiting for the poll, or held back) are left out silently
		if names, err := presetFiles(s.dir); err == nil {
			for _, n := range names {
				if _, ok := files[n]; ok {
					continue
				}
				if b, rerr := os.ReadFile(filepath.Join(s.dir, n)); rerr != nil || checkBackupFile(n, b) != nil {
					skipped = append(skipped, n)
					s.opts.Logf("preset: backup: leaving out %s", n)
				}
			}
		}
	} else {
		names, err := presetFiles(s.dir)
		if err != nil {
			return false, err
		}
		for _, n := range names {
			b, err := os.ReadFile(filepath.Join(s.dir, n))
			if err == nil {
				err = checkBackupFile(n, b)
			}
			if err != nil { // one unreadable file must not make the whole backup unrestorable
				skipped = append(skipped, n)
				s.opts.Logf("preset: backup: leaving out %s: %v", n, err)
				continue
			}
			files[n] = b
		}
	}
	if len(files) == 0 {
		return false, nil
	}
	now := s.opts.Now()
	data, err := zipBundleLabel(files, "all", reason, s.pendingLabel, now, skipped...)
	if err != nil {
		return false, err
	}
	dir := s.backupsDir()
	if err := os.Mkdir(dir, 0o755); err != nil {
		if !os.IsExist(err) { // parent (presets dir) exists
			return false, err
		}
		if st, serr := os.Stat(dir); serr != nil || !st.IsDir() {
			return false, fmt.Errorf("%s is not a directory", dir)
		}
	}
	base := "presets-" + now.Format("20060102-150405") + "-" + reason
	name := base + ".zip"
	for i := 1; i < 1000; i++ {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			break // free (or unreadable: the write below reports it)
		}
		name = fmt.Sprintf("%s-%d.zip", base, i)
	}
	if err := WriteAtomic(filepath.Join(dir, name), data); err != nil {
		return false, err
	}
	s.trustBackup(name, data)
	keep := s.opts.BackupKeep
	if keep <= 0 {
		keep = defaultKeepBack
	}
	seen := map[string]int{}
	for _, b := range s.Backups() { // newest first
		c := classOfReason(b.Reason)
		seen[c]++
		limit := keep
		if c == classPreUpdate {
			limit = preUpdateKeep
		}
		if seen[c] > limit {
			os.Remove(filepath.Join(dir, b.Name))
		}
	}
	return true, nil
}

// checkBackupFile says whether a preset file on disk is fit for a backup: it
// parses, its file name matches its id and the id is allowed.
func checkBackupFile(name string, body []byte) error {
	p, err := ParsePreset(body)
	if err != nil {
		return err
	}
	if p.ID+".json" != name {
		return fmt.Errorf("file name %s does not match preset id %q", name, p.ID)
	}
	return CheckStoredID(p.ID)
}

// ---- bundle parsing -----------------------------------------------------

// Bundle is a validated, parsed zip.
type Bundle struct {
	Manifest Manifest
	Presets  map[string]*Preset
}

func okEntryName(n string) bool {
	return !strings.ContainsAny(n, `/\`) && !strings.Contains(n, "..") && !strings.ContainsRune(n, 0)
}

// ParseBundle validates a zip: no path tricks, size caps, a manifest of the
// right kind and schema, and every preset through ParsePreset, the bank and
// control rules and the registry. All problems are collected.
func (s *Store) ParseBundle(data []byte) (*Bundle, error) {
	b, _, err := s.parseBundle(data, false)
	return b, err
}

// SkippedFile is a file of a bundle that a lenient parse left out.
type SkippedFile struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// parseBundle is ParseBundle; with lenient set, a problem confined to one
// preset file leaves that file out (reported in the second result) instead of
// refusing the whole bundle. Structural problems (not a zip, no manifest, too
// large, wrong kind) are always fatal.
func (s *Store) parseBundle(data []byte, lenient bool) (*Bundle, []SkippedFile, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, nil, &ImportError{[]string{"not a zip file: " + err.Error()}}
	}
	if len(zr.File) > maxBundleEntries {
		return nil, nil, &ImportError{[]string{fmt.Sprintf("too many files (%d, max %d)", len(zr.File), maxBundleEntries)}}
	}
	b := &Bundle{Presets: map[string]*Preset{}}
	var probs []string
	var skipped []SkippedFile
	// bad records a problem confined to file n.
	bad := func(n, msg string) {
		if lenient {
			skipped = append(skipped, SkippedFile{File: n, Reason: msg})
		} else {
			probs = append(probs, msg)
		}
	}
	var total int64
	haveManifest := false
	for _, f := range zr.File {
		n := f.Name
		if strings.HasPrefix(n, "__MACOSX/") || strings.HasPrefix(filepath.Base(n), "._") || filepath.Base(n) == ".DS_Store" {
			continue
		}
		if strings.HasSuffix(n, "/") && okEntryName(strings.TrimSuffix(n, "/")) {
			continue // plain directory entry
		}
		if !okEntryName(n) {
			bad(n, fmt.Sprintf("unsafe path %q", n))
			continue
		}
		if f.UncompressedSize64 > maxEntryBytes {
			bad(n, fmt.Sprintf("%s is too large", n))
			continue
		}
		rc, err := f.Open()
		if err != nil {
			bad(n, fmt.Sprintf("%s: %v", n, err))
			continue
		}
		body, err := io.ReadAll(io.LimitReader(rc, maxEntryBytes+1))
		rc.Close()
		if err != nil || len(body) > maxEntryBytes {
			bad(n, fmt.Sprintf("%s: unreadable or too large", n))
			continue
		}
		total += int64(len(body))
		if total > maxBundleBytes {
			return nil, nil, &ImportError{[]string{"bundle is too large"}}
		}
		if n == "manifest.json" {
			haveManifest = true
			if err := json.Unmarshal(body, &b.Manifest); err != nil {
				probs = append(probs, "manifest.json: "+err.Error())
			} else if b.Manifest.Kind != BundleKind && b.Manifest.Kind != LegacyBundleKind {
				probs = append(probs, "this is not a 2T5 DECK+ presets export")
			} else if b.Manifest.Schema < 1 || b.Manifest.Schema > BundleSchema {
				probs = append(probs, fmt.Sprintf("export schema %d is not supported (this app reads up to %d)", b.Manifest.Schema, BundleSchema))
			}
			continue
		}
		if n == "README.txt" {
			continue // a human note a person may add to a zip; never a preset
		}
		if !strings.HasSuffix(n, ".json") || strings.HasPrefix(n, ".") {
			bad(n, fmt.Sprintf("unexpected file %q", n))
			continue
		}
		p, err := ParsePreset(body)
		if err != nil {
			bad(n, fmt.Sprintf("%s: %v", n, err))
			continue
		}
		if p.ID+".json" != n {
			bad(n, fmt.Sprintf("%s: file name does not match preset id %q", n, p.ID))
			continue
		}
		if p.Schema < 1 || p.Schema > SchemaVersion {
			bad(n, fmt.Sprintf("%s: preset schema %d is not supported", n, p.Schema))
			continue
		}
		if err := CheckStoredID(p.ID); err != nil {
			bad(n, fmt.Sprintf("%s: %v", n, err))
			continue
		}
		if other := foldConflict(b.Presets, p.ID); other != "" {
			bad(n, fmt.Sprintf("%s: preset id differs from %q only in letter case", n, other))
			continue
		}
		if p.Name == "" {
			p.Name = DefaultName(p.ID)
		}
		if pp := s.checkPreset(p); len(pp) > 0 {
			if lenient {
				skipped = append(skipped, SkippedFile{File: n, Reason: strings.Join(pp, "; ")})
				continue
			}
			probs = append(probs, pp...)
		}
		b.Presets[p.ID] = p
	}
	if !haveManifest {
		probs = append(probs, "manifest.json is missing: not a 2T5 DECK+ presets export")
	}
	if len(b.Presets) == 0 && len(probs) == 0 {
		probs = append(probs, "the zip holds no presets")
		for _, sk := range skipped { // nothing usable at all: say why
			probs = append(probs, sk.Reason)
		}
	}
	if len(probs) > 0 {
		sort.Strings(probs)
		return nil, nil, &ImportError{probs}
	}
	sort.Slice(skipped, func(i, j int) bool { return skipped[i].File < skipped[j].File })
	return b, skipped, nil
}

// checkPreset applies the bank, control and registry rules to a preset that
// came from outside (lenient on action fields, strict on structure).
func (s *Store) checkPreset(p *Preset) []string {
	var probs []string
	if fe := settingsIssue(p); fe != nil {
		probs = append(probs, fmt.Sprintf("%s settings: %v", p.ID, fe))
	}
	for bn, bm := range p.Banks {
		if !ValidBank(p.ID, bn) {
			probs = append(probs, fmt.Sprintf("%s: %q is not a valid bank", p.ID, bn))
			continue
		}
		for cn, a := range bm {
			if err := s.checkControl(cn); err != nil {
				probs = append(probs, fmt.Sprintf("%s.%s.%s: not a control of this deck", p.ID, bn, cn))
				continue
			}
			if !ValidBankControl(p.ID, bn, cn) {
				probs = append(probs, fmt.Sprintf("%s.%s.%s: only P1-P8 and P1btn-P8btn can be set per slider mode", p.ID, bn, cn))
				continue
			}
			if err := s.reg.Validate(a, false); err != nil {
				probs = append(probs, fmt.Sprintf("%s.%s.%s: %v", p.ID, bn, cn, err))
			}
		}
	}
	return probs
}

// ---- import -------------------------------------------------------------

// ImportRisk counts what the assignments an import would ADD or CHANGE can do
// when they fire (the classes of actions.RiskOf). Unchanged assignments are not
// counted: re-importing one's own export brings no new risk.
type ImportRisk struct {
	Shell       int `json:"shell"`       // run a program
	Keys        int `json:"keys"`        // post key presses
	Destructive int `json:"destructive"` // post a key press that closes, quits or deletes
	// Unknown counts assignments whose runtime type this editor does not know
	// (what they would execute cannot be judged): dangerous, like shell.
	Unknown int `json:"unknown"`
	// Panic counts added or changed "disarm" (panic key) assignments. A panic key
	// fires from every app, so it silences everything else assigned to its
	// control: PanicOverrides counts those silenced assignments, and an import
	// that silences any is dangerous (needs confirmation).
	Panic          int `json:"panic"`
	PanicOverrides int `json:"panicOverrides"`
	// PanicRemoved counts existing panic (disarm) assignments the import removes
	// or changes: losing the panic key is a risk of its own.
	PanicRemoved int `json:"panicRemoved"`
	// Settings counts changed settings that weaken a safety guard
	// (finderTagConfirmThreshold, dodeeMostGuard; see guardSettings).
	Settings int `json:"settings"`
}

// Add sums two risk counts.
func (r ImportRisk) Add(o ImportRisk) ImportRisk {
	return ImportRisk{r.Shell + o.Shell, r.Keys + o.Keys, r.Destructive + o.Destructive, r.Unknown + o.Unknown, r.Panic + o.Panic, r.PanicOverrides + o.PanicOverrides, r.PanicRemoved + o.PanicRemoved, r.Settings + o.Settings}
}

// Dangerous: the import installs something that runs a program or is destructive.
func (r ImportRisk) Dangerous() bool {
	return r.Shell > 0 || r.Destructive > 0 || r.Unknown > 0 || r.PanicOverrides > 0 || r.PanicRemoved > 0 || r.Settings > 0
}

// ImportFile is one line of an import report.
type ImportFile struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"` // new | changed | unchanged
	Added   int    `json:"added"`
	Changed int    `json:"changed"`
	Removed int    `json:"removed"`
	// Risk counts the added / changed assignments by what they can do.
	Risk ImportRisk `json:"risk"`
	// RunsProgram lists the command lines of the added / changed shell
	// assignments ("bank.control: program arg ..."), each cut to 200 characters,
	// at most 10 (the last entry then says how many more).
	RunsProgram []string `json:"runsProgram"`
	// Overrides lists the existing assignments an imported panic key would
	// silence ("Resolve edit.C8", "Safari base.C8", ...), at most 20.
	Overrides []string `json:"overrides"`
	// SettingsChanged lists the preset-settings keys the import changes (the
	// ones that weaken a safety guard have weakens:true and need confirmation).
	SettingsChanged []SettingChange `json:"settingsChanged"`
	// PanicRemoved lists the existing panic keys this file removes or changes
	// ("Global base.C8").
	PanicRemoved []string `json:"panicRemoved"`
}

// ImportReport is the dry-run diff / result of an import.
type ImportReport struct {
	DryRun bool         `json:"dryRun"`
	Files  []ImportFile `json:"files"`
	// Risk is the sum over all files.
	Risk ImportRisk `json:"risk"`
	// Skipped lists files of a backup that a restore left out (unreadable or
	// not valid); import never skips, it refuses.
	Skipped []SkippedFile `json:"skipped"`
	// NeedsConfirm: a real import is refused unless the caller confirms the risk.
	NeedsConfirm bool `json:"needsConfirm"`
	// Inherited lists assignments that are dangerous only through what they
	// inherit from Global (settings.actionDefaults or the Global assignment).
	Inherited []InheritedRisk `json:"inherited"`
	// Dropped lists the shell and destructive assignments an import with
	// SkipRisky leaves out (always an array; empty when nothing was dropped).
	Dropped []DroppedAssignment `json:"dropped"`
}

// DroppedAssignment is one assignment a skipRisky import did not install.
type DroppedAssignment struct {
	Preset  string `json:"preset"`
	Bank    string `json:"bank"`
	Control string `json:"control"`
	Action  string `json:"action"` // registry action id, e.g. generic.shell
}

// ImportOptions are the caller's choices for a real or dry-run import.
type ImportOptions struct {
	// ConfirmRisk accepts shell and destructive assignments.
	ConfirmRisk bool
	// SkipRisky imports everything except added or changed shell and destructive
	// assignments (reported in Dropped). A control the preset already has keeps
	// its current assignment. Nothing is left to confirm afterwards.
	SkipRisky bool
	// ExpectRiskSig, with ConfirmRisk, binds the confirmation to the report the
	// user saw (RiskSignature of the plain dry-run report): checked inside the
	// commit, a different live risk set is refused with *RiskChangedError.
	ExpectRiskSig string
}

// ErrRiskChanged: the risk set changed since the preview the user confirmed.
var ErrRiskChanged = errors.New("import refused: what it would install changed since you reviewed it (the presets changed in between); review the new report and confirm again")

// RiskChangedError carries the new report.
type RiskChangedError struct{ Report ImportReport }

func (e *RiskChangedError) Error() string { return ErrRiskChanged.Error() }
func (e *RiskChangedError) Unwrap() error { return ErrRiskChanged }

// RiskSignature fingerprints everything a confirmation covers: the risk counts,
// the inherited dangers, the program lines, overridden / removed panic keys and
// the weakening settings.
func RiskSignature(rep ImportReport) string {
	type fileSig struct {
		ID           string
		Runs         []string
		Overrides    []string
		PanicRemoved []string
		Weakening    []string
	}
	var files []fileSig
	for _, f := range rep.Files {
		fs := fileSig{ID: f.ID, Runs: f.RunsProgram, Overrides: f.Overrides, PanicRemoved: f.PanicRemoved}
		for _, c := range f.SettingsChanged {
			if c.Weakens {
				fs.Weakening = append(fs.Weakening, fmt.Sprint(c.Key, c.New))
			}
		}
		files = append(files, fs)
	}
	b, _ := json.Marshal(struct {
		Risk      ImportRisk
		Inherited []InheritedRisk
		Files     []fileSig
	}{rep.Risk, rep.Inherited, files})
	return hashHex(b)
}

// filterRisky returns copies of the bundle's presets without the added or
// changed shell / destructive assignments, and the list of what was dropped. An
// assignment identical to the current one is not new risk and stays; a dropped
// control that the current preset has keeps the current assignment.
func (s *Store) filterRisky(in map[string]*Preset, cur map[string]*Preset) (map[string]*Preset, []DroppedAssignment) {
	out := make(map[string]*Preset, len(in))
	dropped := []DroppedAssignment{}
	for _, id := range sortedKeys(in) {
		np := in[id].Clone()
		old := cur[id]
		for _, bn := range sortedKeys(np.Banks) {
			for _, cn := range sortedKeys(np.Banks[bn]) {
				a := np.Banks[bn][cn]
				act := s.reg.Infer(a)
				rk := s.reg.RiskOfRaw(a)
				if rk != actions.RiskShell && rk != actions.RiskDestructive && rk != actions.RiskUnknown && actions.EffectiveType(a) != "disarm" {
					continue
				}
				if old != nil {
					if oa, ok := old.Banks[bn][cn]; ok {
						if bytes.Equal(marshalAny(oa), marshalAny(a)) {
							continue
						}
						np.Banks[bn][cn] = oa
						dropped = append(dropped, DroppedAssignment{id, bn, cn, act.ID})
						continue
					}
				}
				delete(np.Banks[bn], cn)
				dropped = append(dropped, DroppedAssignment{id, bn, cn, act.ID})
			}
			if len(np.Banks[bn]) == 0 {
				delete(np.Banks, bn)
			}
		}
		out[id] = np
	}
	dropped = s.neutralise(out, cur, dropped)
	return out, dropped
}

// ErrNeedsConfirm is the cause of a RiskError.
var ErrNeedsConfirm = errors.New("import refused: it installs actions that run programs or are destructive; confirm the risk to import it")

// RiskError is returned by a real import that installs shell or destructive
// actions without confirmRisk. Nothing was written.
type RiskError struct{ Report ImportReport }

func (e *RiskError) Error() string { return ErrNeedsConfirm.Error() }
func (e *RiskError) Unwrap() error { return ErrNeedsConfirm }

const (
	maxRunsProgram  = 10
	maxCommandChars = 200
)

func commandLine(a Assignment) string {
	cmd, _ := a["cmd"].(string)
	parts := []string{cmd}
	if args, ok := a["args"].([]any); ok {
		for _, x := range args {
			parts = append(parts, fmt.Sprint(x))
		}
	}
	line := strings.Join(parts, " ")
	if r := []rune(line); len(r) > maxCommandChars {
		line = string(r[:maxCommandChars-1]) + "…"
	}
	return line
}

func (s *Store) diffPreset(old, nu *Preset) ImportFile {
	f := ImportFile{ID: nu.ID, Name: nu.Name, RunsProgram: []string{}, Overrides: []string{}, SettingsChanged: []SettingChange{}, PanicRemoved: []string{}}
	risky := func(bn, cn string, a Assignment) { // an added or changed assignment
		if actions.EffectiveType(a) == "disarm" {
			f.Risk.Panic++
		}
		switch s.reg.RiskOfRaw(a) {
		case actions.RiskShell:
			f.Risk.Shell++
			if len(f.RunsProgram) < maxRunsProgram {
				f.RunsProgram = append(f.RunsProgram, bn+"."+cn+": "+commandLine(a))
			}
		case actions.RiskDestructive:
			f.Risk.Destructive++
		case actions.RiskKeys:
			f.Risk.Keys++
		case actions.RiskUnknown:
			f.Risk.Unknown++
		}
	}
	if old == nil {
		f.Status = "new"
		f.Added = nu.Assigned()
		for _, bn := range sortedKeys(nu.Banks) {
			for _, cn := range sortedKeys(nu.Banks[bn]) {
				risky(bn, cn, nu.Banks[bn][cn])
			}
		}
		noteMoreCommands(&f)
		return f
	}
	seen := map[string]bool{}
	for _, bn := range sortedKeys(nu.Banks) {
		for _, cn := range sortedKeys(nu.Banks[bn]) {
			a := nu.Banks[bn][cn]
			seen[bn+"\x00"+cn] = true
			oa, ok := old.Banks[bn][cn]
			switch {
			case !ok:
				f.Added++
				risky(bn, cn, a)
			case !bytes.Equal(marshalAny(oa), marshalAny(a)):
				f.Changed++
				risky(bn, cn, a)
			}
		}
	}
	for bn, bm := range old.Banks {
		for cn := range bm {
			if !seen[bn+"\x00"+cn] {
				f.Removed++
			}
		}
	}
	if bytes.Equal(marshalOf(old), marshalOf(nu)) || sameEffective(old, nu, f) {
		f.Status = "unchanged"
	} else {
		f.Status = "changed"
	}
	noteMoreCommands(&f)
	return f
}

// sameEffective is true when nothing a restore or import would apply differs:
// no assignment added, removed or changed, and the same name, packs and
// settings. File bytes may still differ (formatting, schema number, unknown
// top-level keys), which is not a change to the user's deck.
func sameEffective(old, nu *Preset, f ImportFile) bool {
	if f.Added != 0 || f.Removed != 0 || f.Changed != 0 || old.Name != nu.Name {
		return false
	}
	if len(old.Packs) != len(nu.Packs) {
		return false
	}
	for i := range old.Packs {
		if old.Packs[i] != nu.Packs[i] {
			return false
		}
	}
	return len(settingChanges(nu.ID, old, nu)) == 0
}

// noteMoreCommands appends "(+N more)" when more shell assignments than listed.
func noteMoreCommands(f *ImportFile) {
	if f.Risk.Shell > len(f.RunsProgram) {
		f.RunsProgram = append(f.RunsProgram, fmt.Sprintf("(+%d more)", f.Risk.Shell-len(f.RunsProgram)))
	}
}

func marshalAny(v any) []byte { b, _ := marshalNoEscape(v); return b }

func (s *Store) reportFor(b *Bundle, presets map[string]*Preset) ImportReport {
	r := ImportReport{Files: []ImportFile{}, Skipped: []SkippedFile{}}
	for _, id := range sortedKeys(b.Presets) {
		f := s.diffPreset(presets[id], b.Presets[id])
		r.Risk = r.Risk.Add(f.Risk)
		r.Files = append(r.Files, f)
	}
	s.effectiveRiskPass(&r, b, presets)
	for i := range r.Files {
		f := &r.Files[i]
		for _, bn := range sortedKeys(presets[f.ID].bankMapOrEmpty()) {
			for _, cn := range sortedKeys(presets[f.ID].Banks[bn]) {
				a := presets[f.ID].Banks[bn][cn]
				if actions.EffectiveType(a) != "disarm" {
					continue
				}
				na, ok := b.Presets[f.ID].Banks[bn][cn]
				if !ok || actions.EffectiveType(na) != "disarm" || !bytes.Equal(marshalAny(a), marshalAny(na)) {
					f.Risk.PanicRemoved++
					r.Risk.PanicRemoved++
					f.PanicRemoved = append(f.PanicRemoved, presets[f.ID].Name+" "+bn+"."+cn)
				}
			}
		}
	}
	for i := range r.Files {
		f := &r.Files[i]
		f.SettingsChanged = settingChanges(f.ID, presets[f.ID], b.Presets[f.ID])
		if f.SettingsChanged == nil {
			f.SettingsChanged = []SettingChange{}
		}
		for _, c := range f.SettingsChanged {
			if c.Weakens {
				f.Risk.Settings++
				r.Risk.Settings++
			}
		}
	}
	// An imported panic key silences whatever else is assigned to its control, in
	// every app and bank (the engine checks it first). Name those assignments.
	merged := make(map[string]*Preset, len(presets)+len(b.Presets))
	for id, p := range presets {
		merged[id] = p
	}
	for id, p := range b.Presets {
		merged[id] = p
	}
	for i := range r.Files {
		f := &r.Files[i]
		if f.Risk.Panic == 0 {
			continue
		}
		nu, old := b.Presets[f.ID], presets[f.ID]
		for _, bn := range sortedKeys(nu.Banks) {
			for _, cn := range sortedKeys(nu.Banks[bn]) {
				a := nu.Banks[bn][cn]
				if actions.EffectiveType(a) != "disarm" {
					continue
				}
				if old != nil {
					if oa, ok := old.Banks[bn][cn]; ok && bytes.Equal(marshalAny(oa), marshalAny(a)) {
						continue // not new
					}
				}
				for _, qid := range sortedKeys(merged) {
					q := merged[qid]
					for _, qb := range sortedKeys(q.Banks) {
						if qa, ok := q.Banks[qb][cn]; ok && actions.EffectiveType(qa) != "disarm" {
							f.Risk.PanicOverrides++
							r.Risk.PanicOverrides++
							if len(f.Overrides) < 20 {
								f.Overrides = append(f.Overrides, q.Name+" "+qb+"."+cn)
							}
						}
					}
				}
			}
		}
	}
	r.NeedsConfirm = r.Risk.Dangerous()
	return r
}

// Import adds or replaces the presets of a bundle (others are untouched).
// dryRun only reports. A real import is one undo step and is preceded by a
// backup. Nothing is written when any file is invalid, or when it installs
// shell or destructive actions (a *RiskError carries the report): use
// ImportWith(…, true) once the user confirmed the risk.
func (s *Store) Import(data []byte, dryRun bool) (ImportReport, error) {
	return s.ImportWith(data, dryRun, false)
}

// ImportWith is Import with the risk confirmation given by the caller.
func (s *Store) ImportWith(data []byte, dryRun, confirmRisk bool) (ImportReport, error) {
	return s.ImportOpts(data, dryRun, ImportOptions{ConfirmRisk: confirmRisk})
}

// ImportOpts is the import with all the caller's choices (ImportOptions).
func (s *Store) ImportOpts(data []byte, dryRun bool, o ImportOptions) (ImportReport, error) {
	confirmRisk := o.ConfirmRisk
	b, err := s.ParseBundle(data)
	if err != nil {
		return ImportReport{}, err
	}
	snap := s.Snapshot()
	if probs := mergeProblems(snap.presets, b); len(probs) > 0 {
		return ImportReport{}, &ImportError{probs}
	}
	// view is what would be installed: the bundle, minus the risky assignments
	// when SkipRisky.
	view := func(cur map[string]*Preset) (*Bundle, []DroppedAssignment) {
		if !o.SkipRisky {
			return b, []DroppedAssignment{}
		}
		nb := *b
		var d []DroppedAssignment
		nb.Presets, d = s.filterRisky(b.Presets, cur)
		return &nb, d
	}
	vb, dropped := view(snap.presets)
	rep := s.reportFor(vb, snap.presets)
	rep.Dropped = dropped
	rep.DryRun = dryRun
	if dryRun {
		return rep, nil
	}
	err = s.commit("Import presets", func(ps map[string]*Preset) error {
		if probs := mergeProblems(ps, b); len(probs) > 0 { // the store moved on since the dry run
			return &ImportError{probs}
		}
		if confirmRisk && o.ExpectRiskSig != "" {
			if plain := s.reportFor(b, ps); RiskSignature(plain) != o.ExpectRiskSig {
				return &RiskChangedError{Report: plain}
			}
		}
		vb, dropped = view(ps)
		rep = s.reportFor(vb, ps)
		rep.Dropped = dropped
		if rep.NeedsConfirm && !confirmRisk {
			return &RiskError{Report: rep}
		}
		for id, p := range vb.Presets {
			ps[id] = p
		}
		if _, err := s.backupLocked("import"); err != nil {
			return fmt.Errorf("backup before import failed, nothing imported: %w", err)
		}
		return nil
	})
	var re *RiskError
	if errors.As(err, &re) {
		return re.Report, err
	}
	return rep, err
}

// RestoreBackup makes the presets equal to a dated backup (presets added since
// are removed, Global is always kept). It is one undo step and is preceded by
// a backup of the current state.
func (s *Store) RestoreBackup(name string) (ImportReport, error) {
	return s.RestoreBackupOpts(name, ImportOptions{})
}

// RestoreBackupOpts is RestoreBackup with the caller's choices. A backup this app
// made (its hash is in the trust store; every backup when there is no trust
// store) is the user's own data and restores without confirmation, its risk shown
// for information only. A zip that is NOT one of ours (planted in the shared
// .backups/ folder, or made by an older build) is judged like an import: its
// report is computed against the CURRENT state, a dangerous one needs
// ConfirmRisk, and SkipRisky leaves the risky parts out.
func (s *Store) RestoreBackupOpts(name string, o ImportOptions) (ImportReport, error) {
	if !backupRe.MatchString(name) {
		return ImportReport{}, fmt.Errorf("%w: %q", ErrNoBackup, name)
	}
	data, err := os.ReadFile(filepath.Join(s.backupsDir(), name))
	if err != nil {
		return ImportReport{}, fmt.Errorf("%w: %q", ErrNoBackup, name)
	}
	trusted := s.backupTrusted(name, data)
	// Lenient: a backup is the user's own data and may hold a file that no
	// longer parses (older versions copied every *.json). The readable files
	// are restored, the others reported; a preset whose backup copy was left
	// out is kept as it is now (it must not vanish because its copy was broken).
	b, skipped, err := s.parseBundle(data, true)
	if err != nil {
		return ImportReport{}, err
	}
	keep := map[string]bool{GlobalID: true}
	for _, sk := range skipped {
		keep[strings.TrimSuffix(sk.File, ".json")] = true
	}
	for _, f := range b.Manifest.Skipped {
		keep[strings.TrimSuffix(f, ".json")] = true
	}
	view := func(cur map[string]*Preset) (*Bundle, []DroppedAssignment) {
		if trusted || !o.SkipRisky {
			return b, []DroppedAssignment{}
		}
		nb := *b
		var d []DroppedAssignment
		nb.Presets, d = s.filterRisky(b.Presets, cur)
		return &nb, d
	}
	vb, dropped := view(s.Snapshot().presets)
	rep := s.reportFor(vb, s.Snapshot().presets)
	rep.Dropped = dropped
	rep.Skipped = skipped
	if rep.Skipped == nil {
		rep.Skipped = []SkippedFile{}
	}
	if trusted {
		rep.NeedsConfirm = false // a restore of our own backup brings back the user's own data
	}
	for id := range s.Snapshot().presets {
		if b.Presets[id] == nil && !keep[id] {
			rep.Files = append(rep.Files, ImportFile{ID: id, Name: s.Snapshot().presets[id].Name, Status: "removed", Removed: s.Snapshot().presets[id].Assigned()})
		}
	}
	err = s.commit("Restore backup "+name, func(ps map[string]*Preset) error {
		vb, dropped = view(ps)
		if !trusted {
			r := s.reportFor(vb, ps)
			r.Dropped, r.Skipped = dropped, rep.Skipped
			if r.NeedsConfirm && !o.ConfirmRisk {
				rep = r
				return &RiskError{Report: r}
			}
		}
		if _, err := s.backupLocked("restore"); err != nil {
			return fmt.Errorf("backup before restore failed, nothing restored: %w", err)
		}
		for id := range ps {
			if b.Presets[id] == nil && !keep[id] {
				delete(ps, id)
			}
		}
		for id, p := range vb.Presets {
			ps[id] = p
		}
		// The result must be a store the deck can run: no two ids that differ
		// only in letter case (a kept preset against a restored one), and a
		// configuration that composes.
		ids := make([]string, 0, len(ps))
		for id := range ps {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			if other := foldConflict(ps, id); other != "" {
				return fmt.Errorf("restore refused: %q and %q differ only in letter case (one file on this volume); nothing was changed", id, other)
			}
		}
		if err := composeCheck(ps); err != nil {
			return fmt.Errorf("%w: restoring %s would leave a store the deck cannot compose: %v", ErrWouldNotCompose, name, err)
		}
		return nil
	})
	var re *RiskError
	if errors.As(err, &re) {
		return re.Report, err
	}
	return rep, err
}

// mergeProblems says why the bundle's presets cannot be added to existing: an
// id that differs from an existing one only in letter case (one file on the
// case-insensitive volume, two presets in memory), or a result the deck could
// not compose its configuration from.
func mergeProblems(existing map[string]*Preset, b *Bundle) []string {
	var probs []string
	trial := make(map[string]*Preset, len(existing)+len(b.Presets))
	for id, p := range existing {
		trial[id] = p
	}
	for _, id := range sortedKeys(b.Presets) {
		if other := foldConflict(trial, id); other != "" {
			probs = append(probs, fmt.Sprintf("%s: preset id differs from the existing %q only in letter case", id, other))
			continue
		}
		trial[id] = b.Presets[id]
	}
	if len(probs) == 0 {
		if err := composeCheck(trial); err != nil {
			probs = append(probs, err.Error())
		}
	}
	return probs
}

// ---- backup diff ---------------------------------------------------------------

// BackupDiffFile is one preset in a BackupDiff.
type BackupDiffFile struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Status: changed | unchanged | new (only in the backup: a restore adds the
	// preset) | removed (only now: a restore removes it).
	Status  string `json:"status"`
	Current int    `json:"current"` // assignments now
	Backup  int    `json:"backup"`  // assignments in the backup
	// Added / Removed / Changed are what RESTORING the backup would do to the
	// preset as it is now (same counting as an import report).
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Changed int    `json:"changed"`
	Summary string `json:"summary"`
}

// BackupDiff compares a dated backup with the presets as they are now.
type BackupDiff struct {
	Name    string           `json:"name"`
	Label   string           `json:"label,omitempty"`
	Reason  string           `json:"reason"`
	Files   []BackupDiffFile `json:"files"`
	Skipped []SkippedFile    `json:"skipped"`
	// Totals over Files.
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Changed int `json:"changed"`
}

// DiffBackup reports, per preset, how the backup differs from now. Nothing is written.
func (s *Store) DiffBackup(name string) (BackupDiff, error) {
	if !backupRe.MatchString(name) {
		return BackupDiff{}, fmt.Errorf("%w: %q", ErrNoBackup, name)
	}
	data, err := os.ReadFile(filepath.Join(s.backupsDir(), name))
	if err != nil {
		return BackupDiff{}, fmt.Errorf("%w: %q", ErrNoBackup, name)
	}
	b, skipped, err := s.parseBundle(data, true)
	if err != nil {
		return BackupDiff{}, err
	}
	d := BackupDiff{Name: name, Reason: backupReason(name), Files: []BackupDiffFile{}, Skipped: skipped}
	if d.Reason == "" {
		d.Reason = s.manifestReason(name)
	}
	if d.Skipped == nil {
		d.Skipped = []SkippedFile{}
	}
	d.Label = b.Manifest.Label
	cur := s.Snapshot().presets
	keep := map[string]bool{GlobalID: true}
	for _, sk := range skipped {
		keep[strings.TrimSuffix(sk.File, ".json")] = true
	}
	for _, f := range b.Manifest.Skipped {
		keep[strings.TrimSuffix(f, ".json")] = true
	}
	ids := map[string]bool{}
	for id := range cur {
		ids[id] = true
	}
	for id := range b.Presets {
		ids[id] = true
	}
	for _, id := range sortedKeys(ids) {
		now, bk := cur[id], b.Presets[id]
		f := BackupDiffFile{ID: id}
		switch {
		case now != nil && bk != nil:
			df := s.diffPreset(now, bk)
			f.Name, f.Status, f.Current, f.Backup = now.Name, df.Status, now.Assigned(), bk.Assigned()
			f.Added, f.Removed, f.Changed = df.Added, df.Removed, df.Changed
			if f.Status == "unchanged" {
				f.Summary = fmt.Sprintf("%s: the same as the backup (%d assignments)", f.Name, f.Current)
			} else {
				f.Summary = fmt.Sprintf("%s has %d assignments now and %d in this backup: restoring adds %d, removes %d, changes %d", f.Name, f.Current, f.Backup, f.Added, f.Removed, f.Changed)
			}
		case bk != nil:
			f.Name, f.Status, f.Backup, f.Added = bk.Name, "new", bk.Assigned(), bk.Assigned()
			f.Summary = fmt.Sprintf("%s is only in the backup (%d assignments): restoring brings it back", f.Name, f.Backup)
		default:
			if keep[id] {
				continue // a restore keeps it
			}
			f.Name, f.Status, f.Current, f.Removed = now.Name, "removed", now.Assigned(), now.Assigned()
			f.Summary = fmt.Sprintf("%s exists now (%d assignments) but not in this backup: restoring removes it", f.Name, f.Current)
		}
		d.Added += f.Added
		d.Removed += f.Removed
		d.Changed += f.Changed
		d.Files = append(d.Files, f)
	}
	return d, nil
}

// maxHashedPerListing caps how many zips one Backups() call reads and hashes
// from disk; the rest are listed untrusted (a restore re-checks the real bytes).
const maxHashedPerListing = 8

type backupHashKey struct {
	name  string
	size  int64
	mtime int64
}

// backupTrustedCached is backupTrusted for a listing: the hash of an unchanged
// zip (same name, size, mtime) is remembered, so a repeated listing reads nothing.
// An uncached zip is read only while the per-listing budget lasts.
func (s *Store) backupTrustedCached(name string, fi os.FileInfo, hashed *int) bool {
	k := backupHashKey{name, fi.Size(), fi.ModTime().UnixNano()}
	s.hashMu.Lock()
	sum, ok := s.hashCache[k]
	s.hashMu.Unlock()
	if !ok {
		if *hashed >= maxHashedPerListing || fi.Size() >= 64<<20 {
			return false
		}
		*hashed++
		zb, err := readCapped(filepath.Join(s.backupsDir(), name), 64<<20)
		if err != nil {
			return false
		}
		sum = hashHex(zb)
		s.hashMu.Lock()
		if s.hashCache == nil || len(s.hashCache) > 512 {
			s.hashCache = map[backupHashKey]string{}
		}
		s.hashCache[k] = sum
		s.hashMu.Unlock()
	}
	s.trustMu.RLock()
	defer s.trustMu.RUnlock()
	return s.trust != nil && s.trust.Backups[name] == sum
}
