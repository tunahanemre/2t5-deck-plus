package deps

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
)

// The per-Mac "setup done" marker. Borrowed from DIT+ first-run (dit-server commit
// 7b9ca89, dit/firstrun.py marker()/mark_done()), adapted: a JSON file in this
// user's own folder (~/Library/Application Support/2t5-deck-plus/setup.json),
// written atomically. Absent = first run on this Mac. It also remembers which
// REQUIRED items were ok at the last look, so the Requirements screen comes
// back when one of those later goes missing, and not for things that never
// worked (the person already saw them) .

// MarkerName is the file inside MarkerDir.
const MarkerName = "setup.json"

// appSupportName matches datadir.AppSupportName.
const appSupportName = "2t5-deck-plus"

// userHomeDir is injectable so no test touches the real home folder.
var userHomeDir = os.UserHomeDir

// Marker is the file's content.
type Marker struct {
	Host            string   `json:"host"`
	DoneAt          string   `json:"doneAt"`
	OK              []string `json:"ok"` // required ids that were ok at the last look
	RequiredMissing []string `json:"requiredMissing"`
	OptionalSkipped []string `json:"optionalSkipped"`
}

// MarkerDir is ~/Library/Application Support/2t5-deck-plus.
func MarkerDir() (string, error) {
	h, err := userHomeDir()
	if err != nil || h == "" {
		return "", errors.New("deps: cannot resolve the home directory")
	}
	return filepath.Join(h, "Library", "Application Support", appSupportName), nil
}

// ReadMarker returns nil (and no error) for a missing or unreadable marker:
// both mean "this Mac has not finished setup".
func ReadMarker(dir string) (*Marker, error) {
	raw, err := os.ReadFile(filepath.Join(dir, MarkerName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var m Marker
	if json.Unmarshal(raw, &m) != nil {
		return nil, nil
	}
	return &m, nil
}

// WriteMarker records the marker atomically (temp file + rename, mode 0600).
func WriteMarker(dir string, m Marker) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, MarkerName+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }() // no-op after a successful rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, filepath.Join(dir, MarkerName))
}

// Reconciled is what the marker says about the current statuses.
type Reconciled struct {
	FirstRun bool
	// WasOK: required ids that the marker recorded as ok.
	WasOK map[string]bool
	// CameBack: required ids that were ok and are missing now.
	CameBack []string
	// NewOK / Changed: the ok list to store back when it grew.
	NewOK   []string
	Changed bool
}

// Reconcile compares statuses (id -> ok|missing|unknown) with the marker.
// Only required, non-informational items are tracked. unknown counts as neither.
func Reconcile(cat []Entry, status map[string]string, m *Marker) Reconciled {
	r := Reconciled{FirstRun: m == nil, WasOK: map[string]bool{}}
	if m == nil {
		return r
	}
	was := map[string]bool{}
	for _, id := range m.OK {
		was[id] = true
	}
	now := map[string]bool{}
	for id := range was {
		now[id] = true
	}
	for _, e := range cat {
		if !e.Required || e.Informational {
			continue
		}
		if was[e.ID] {
			r.WasOK[e.ID] = true
			if status[e.ID] == Missing {
				r.CameBack = append(r.CameBack, e.ID)
			}
		}
		if status[e.ID] == OK && !now[e.ID] {
			now[e.ID] = true
			r.Changed = true
		}
	}
	for id := range now {
		r.NewOK = append(r.NewOK, id)
	}
	sort.Strings(r.NewOK)
	return r
}
