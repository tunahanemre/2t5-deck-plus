package preset

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Known apps: display names and default packs. Unknown bundle ids get their
// id as name and the generic pack (the user may tick more).

type appInfo struct {
	Name  string
	Packs []string
}

var browserPacks = []string{"browser", "generic"}

var knownApps = map[string]appInfo{
	ResolveID:                    {"DaVinci Resolve", []string{"resolve", "generic"}},
	FinderID:                     {"Finder", []string{"finder", "generic"}},
	"com.google.Chrome":          {"Google Chrome", browserPacks},
	"com.apple.Safari":           {"Safari", browserPacks},
	"company.thebrowser.Browser": {"Arc", browserPacks},
	"org.mozilla.firefox":        {"Firefox", browserPacks},
	"com.brave.Browser":          {"Brave", browserPacks},
	"com.microsoft.edgemac":      {"Microsoft Edge", browserPacks},
	"com.tinyspeck.slackmacgap":  {"Slack", []string{"generic"}},
}

// DefaultPacks returns the packs offered for a bundle id.
func DefaultPacks(id string) []string {
	if id == GlobalID {
		return []string{"generic"}
	}
	if a, ok := knownApps[id]; ok {
		return append([]string(nil), a.Packs...)
	}
	return []string{"generic"}
}

// DefaultName returns the display name for a bundle id.
func DefaultName(id string) string {
	if id == GlobalID {
		return "Global"
	}
	if a, ok := knownApps[id]; ok {
		return a.Name
	}
	return id
}

var bundleSegRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]*$`)

// ValidBundleID reports whether id looks like a macOS bundle identifier, the
// reverse-DNS form com.company.App: at least two dot-separated segments (one
// dot), each made of letters, digits, "-" or "_", the first starting with a
// letter, at most 255 characters. It is stricter than ValidID, which only
// says the id can be a file name.
func ValidBundleID(id string) bool {
	if !ValidID(id) || len(id) > 255 {
		return false
	}
	segs := strings.Split(id, ".")
	if len(segs) < 2 {
		return false
	}
	for _, s := range segs {
		if !bundleSegRe.MatchString(s) {
			return false
		}
	}
	c := segs[0][0]
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

// ErrSystemApp is returned when a removal targets Global or the Resolve preset.
var ErrSystemApp = errors.New("Global and DaVinci Resolve cannot be removed")

// RemoveApp deletes an app preset (one undo step; the file is kept in
// .history/<id>/ as a .removed copy). Global and the Resolve preset are never
// removed.
func (s *Store) RemoveApp(id string) error {
	if id == "global" {
		id = GlobalID
	}
	if id == GlobalID || id == ResolveID {
		return ErrSystemApp
	}
	return s.commit("Remove app "+DefaultName(id), func(ps map[string]*Preset) error {
		if _, ok := ps[id]; !ok {
			return fmt.Errorf("%w: %q", ErrNoPreset, id)
		}
		delete(ps, id)
		return nil
	})
}
