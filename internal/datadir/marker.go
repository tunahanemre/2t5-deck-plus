package datadir

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// MarkerName is the file, inside the per-user folder
// (~/Library/Application Support/2t5-deck-plus/), that records which kind of
// data root this Mac uses. Two lines: the kind ("external" or "home"), then the
// root path. The Python sidecar reads the same file (sidecar/resolve_driver.py).
const MarkerName = "data-root"

const (
	markerExternal = "external"
	markerHome     = "home"
)

// The external (studio drive) roots. Variables only so tests can point them at a temp
// dir; production never changes them.
var (
	extDefault = DefaultRoot
	extLegacy  = LegacyDefaultRoot
)

// userHomeDir is injectable so no test touches the real home folder.
var userHomeDir = os.UserHomeDir

var errNoHome = errors.New("datadir: cannot resolve the home directory")

// kind says how the root was chosen.
type kind int

const (
	kindEnv         kind = iota // DECKPLUS_DATA_DIR / LD_DECK_DATA_DIR
	kindExternal                // the external root, reachable now
	kindHome                    // ~/Library/Application Support/2t5-deck-plus/data
	kindUnreachable             // this Mac uses the external root, which is not there: refuse
)

type resolution struct {
	root string
	kind kind
}

// perUserDir is ~/Library/Application Support/2t5-deck-plus.
func perUserDir() (string, error) {
	h, err := userHomeDir()
	if err != nil || h == "" {
		return "", errNoHome
	}
	return filepath.Join(h, "Library", "Application Support", AppSupportName), nil
}

// HomeRoot is the data root used on a Mac with no external volume:
// ~/Library/Application Support/2t5-deck-plus/data.
func HomeRoot() (string, error) {
	d, err := perUserDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "data"), nil
}

// externalRoot returns the external root when that volume's app folder is there:
// the new data folder, else the old (ld-deck) one, else the new root whose
// parent exists (first run on the mounted volume). Stat only.
func externalRoot() (string, bool) {
	switch {
	case isDir(extDefault):
		return extDefault, true
	case isDir(extLegacy):
		return extLegacy, true
	case isDir(filepath.Dir(extDefault)):
		return extDefault, true
	}
	return "", false
}

// readMarker returns the recorded kind and root, or "" when there is none.
func readMarker() (k, root string) {
	d, err := perUserDir()
	if err != nil {
		return "", ""
	}
	b, err := os.ReadFile(filepath.Join(d, MarkerName))
	if err != nil {
		return "", ""
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	k = strings.TrimSpace(lines[0])
	if k != markerExternal && k != markerHome {
		return "", ""
	}
	if len(lines) > 1 {
		root = strings.TrimSpace(lines[1])
	}
	return k, root
}

// priorInstall reports whether the per-user folder already exists without a
// marker: an older build ran here, and every older build used the external root.
// A Mac like that must not silently grow a second tree at home.
func priorInstall() bool {
	d, err := perUserDir()
	if err != nil {
		return false
	}
	return isDir(d) || isDir(filepath.Join(filepath.Dir(d), LegacyAppSupportName))
}

// resolve picks the root. Read-only: it stats and reads the marker, writes nothing.
func resolve() resolution {
	for _, name := range []string{EnvVar, LegacyEnvVar} {
		if v := strings.TrimSpace(os.Getenv(name)); v != "" {
			return resolution{v, kindEnv}
		}
	}
	if r, ok := externalRoot(); ok {
		return resolution{r, kindExternal}
	}
	mk, mroot := readMarker()
	if mk == "" && priorInstall() {
		mk, mroot = markerExternal, ""
	}
	if mk == markerExternal {
		if mroot == "" {
			mroot = extDefault
		}
		return resolution{mroot, kindUnreachable}
	}
	if h, err := HomeRoot(); err == nil {
		return resolution{h, kindHome}
	}
	return resolution{extDefault, kindUnreachable} // no home to fall back to: today's behaviour
}

// writeMarker records the choice atomically (temp file + rename, mode 0600).
// A legacy per-user folder is copied across first, so creating the new folder
// here can never pre-empt MigrateAppSupport.
func writeMarker(k, root string) error {
	d, err := perUserDir()
	if err != nil {
		return err
	}
	_, _ = MigrateDir(filepath.Join(filepath.Dir(d), LegacyAppSupportName), d)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(d, MarkerName+".tmp-")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := tmp.Chmod(0o600); err == nil {
		_, err = tmp.WriteString(k + "\n" + root + "\n")
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(name, filepath.Join(d, MarkerName))
	}
	if err != nil {
		_ = os.Remove(name)
	}
	return err
}

// prepare is the single gate before anything is created. It returns the root
// to use, or *UnreachableError (creating nothing) when this Mac uses the
// external root and that volume is not there. It is also the only place the
// marker is written.
func prepare() (string, error) {
	r := resolve()
	switch r.kind {
	case kindUnreachable:
		return "", &UnreachableError{Root: r.root, Parent: filepath.Dir(r.root)}
	case kindEnv:
		parent := filepath.Dir(r.root)
		if fi, err := os.Stat(parent); err != nil || !fi.IsDir() {
			return "", &UnreachableError{Root: r.root, Parent: parent}
		}
	case kindExternal:
		parent := filepath.Dir(r.root)
		if !isDir(parent) {
			return "", &UnreachableError{Root: r.root, Parent: parent}
		}
		if mk, mr := readMarker(); mk != markerExternal || mr != r.root {
			_ = writeMarker(markerExternal, r.root) // best effort: the volume itself is fine
		}
	case kindHome:
		if mk, _ := readMarker(); mk != markerHome {
			if err := writeMarker(markerHome, r.root); err != nil {
				return "", err
			}
		}
	}
	return r.root, nil
}
