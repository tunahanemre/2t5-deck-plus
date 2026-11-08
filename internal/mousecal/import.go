package mousecal

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// SourceLdResolve names the origin of imported points.
const SourceLdResolve = "ld-resolve"

// ImportLayout is the layout an imported Resolve point is filed under: every
// point of the old calibration.json is a control of the Color page (wheels,
// master bars, reset arrows, the temp / tint sliders).
const ImportLayout = "color"

// LegacyPoint is one point of the old calibration.json (absolute screen
// coordinates in display points, top-left origin).
type LegacyPoint struct {
	X, Y float64
	Axis string
}

// ImportInfo is the record that the import ran: what came in, what did not and
// what the conversion assumed. It sits in the calibration block next to the
// targets, so the editor and the next start can see it.
type ImportInfo struct {
	From     string   `json:"from"`
	At       string   `json:"at"`
	Display  string   `json:"display,omitempty"` // display class the points were filed under
	Layout   string   `json:"layout,omitempty"`
	Imported []string `json:"imported"`
	Skipped  []string `json:"skipped,omitempty"` // old names with no target in the new system
	Note     string   `json:"note,omitempty"`
}

var errNoChange = errors.New("mousecal: nothing to import")

var importable = regexp.MustCompile(`^(lift|gamma|gain|offset)_(master|reset)$`)

// MapLegacyKey maps an old calibration.json name to the mousecal target key.
// known are the keys of the current preset's targets. ok=false: no target uses
// that name (the old *_center and balance_red points belong to an older engine
// that did not use them).
func MapLegacyKey(old string, known map[string]bool) (key string, ok bool) {
	switch {
	case known[old], importable.MatchString(old), old == "temp_slider", old == "tint_slider":
		return old, true
	}
	return "", false
}

// displayHolding returns the display class whose frame contains every point.
func displayHolding(pts []LegacyPoint, ds []Display) (Display, bool) {
	for _, d := range ds {
		all := len(pts) > 0
		for _, p := range pts {
			if !d.Frame.Contains(Pt{p.X, p.Y}) {
				all = false
				break
			}
		}
		if all {
			return d, true
		}
	}
	return Display{}, false
}

// ImportLegacy converts the old absolute points into imported entries of block
// b, once. It returns the new block and the record; changed=false (and a reason)
// when nothing was done: the block already has targets or an earlier import,
// there are no points, or no display holds the points (then they are left as
// they are: the engine's old-point fallback keeps serving them).
//
// The old file does not record the Resolve window frame, so the points cannot be
// made window-relative honestly. They are stored as absolute points under the
// display class that holds them, valid only while they lie inside Resolve's
// window (see resolveImported).
func ImportLegacy(b Block, pts map[string]LegacyPoint, known map[string]bool, ds []Display, now time.Time) (nb Block, changed bool, why string) {
	if b.Import != nil {
		return b, false, "already imported"
	}
	if len(b.Targets) > 0 {
		return b, false, "the app already has calibrations"
	}
	if len(pts) == 0 {
		return b, false, "no old points"
	}
	names := make([]string, 0, len(pts))
	all := make([]LegacyPoint, 0, len(pts))
	for n, p := range pts {
		names = append(names, n)
		all = append(all, p)
	}
	sort.Strings(names)
	disp, ok := displayHolding(all, ds)
	if !ok {
		return b, false, "no connected display holds all the old points"
	}
	nb = b.clone()
	info := &ImportInfo{From: SourceLdResolve, At: now.UTC().Format(time.RFC3339), Display: disp.Class(), Layout: ImportLayout, Imported: []string{}}
	for _, n := range names {
		key, ok := MapLegacyKey(n, known)
		if !ok {
			info.Skipped = append(info.Skipped, n)
			continue
		}
		p := pts[n]
		e := Entry{ID: "imp-" + strings.ReplaceAll(key, "_", "-"), At: info.At, Via: "import", Display: info.Display, Layout: ImportLayout,
			Abs: true, AX: round2(p.X), AY: round2(p.Y), Axis: p.Axis, Source: SourceLdResolve}
		nb.Targets[key] = &TargetData{Entries: []Entry{e}}
		info.Imported = append(info.Imported, key)
	}
	if len(info.Imported) == 0 {
		return b, false, "no old point matches a target"
	}
	info.Note = fmt.Sprintf("Absolute screen points from ld-resolve's calibration.json; the Resolve window they were taken in was not recorded. Assumed Color page, screen %s. Not re-verified.", disp.Class())
	nb.Import = info
	return nb, true, ""
}

// MigrateLegacy runs ImportLegacy for an app against the live store, once.
// pts are the old points (the engine's calibration.json). It reads the displays
// from the system, never posts input and never needs the app to be running.
func (s *Service) MigrateLegacy(appID string, pts map[string]LegacyPoint) (imported int, why string) {
	if s.Preset == nil || s.Store == nil || s.Sys == nil {
		return 0, "not wired"
	}
	p := s.Preset(appID)
	if p == nil {
		return 0, "no preset"
	}
	env, err := s.Sys.Env(appID)
	if err != nil {
		return 0, "cannot read the displays: " + err.Error()
	}
	known := map[string]bool{}
	for _, t := range DeriveTargets(p) {
		known[t.Key] = true
	}
	var rec *ImportInfo
	err = s.Store.Update(appID, "Import ld-resolve mouse calibration", func(b Block) (Block, error) {
		nb, ch, w := ImportLegacy(b, pts, known, env.Displays, s.now())
		if !ch {
			why = w
			return b, errNoChange
		}
		rec = nb.Import
		return nb, nil
	})
	if err != nil && !errors.Is(err, errNoChange) {
		return 0, err.Error()
	}
	if rec == nil {
		return 0, why
	}
	s.logf("[calibration] imported %d point(s) from ld-resolve onto screen %s (not re-verified); skipped %v", len(rec.Imported), rec.Display, rec.Skipped)
	return len(rec.Imported), ""
}
