// Package datadir resolves the SINGLE on-disk root that holds every piece of
// 2T5 DECK+ runtime data: the live config the app reads and merges, the
// tail-able log, and the crash-safety state files (Go's shutdown sentinel and
// the Python sidecar's active-clip highlight record).
//
// Before this package those three lived in three unrelated places under the
// user's home directory (~/Library/Application Support/2t5-deck-plus/,
// ~/Library/Logs/2t5-deck-plus.log, $HOME/.2t5-deck-plus-*.json), which meant a
// "self-contained on WS-01" install was impossible and every `go test` run
// that constructed an Engine wrote the SAME state file the production app
// reads at startup. One resolved root fixes both: production points at
// WS-01, tests point at a temp dir via EnvVar, and nothing hardcodes a
// literal anywhere else in the tree.
//
// # The mount guard (load-bearing, not defensive padding)
//
// DefaultRoot lives on an external volume. When 2T5-WS-01 is not mounted,
// /Volumes/2T5-WS-01 is an ordinary missing path — and a plain
// os.MkdirAll(DefaultRoot) would cheerfully CREATE that whole chain on the
// boot disk, then fill it. That is not hypothetical: an rsync at an unmounted
// mount point put 162 GB on this Mac's internal SSD on 2026-08-27 and broke
// unrelated tooling. So every directory-creating call in this package first
// requires the root's PARENT to already exist (for the default root that is
// /Volumes/2T5-WS-01/01-APPS/2t5-deck-plus, or .../ld-deck before the folder is
// renamed — a directory that can only exist on the real volume, i.e. a mount
// sentinel) and returns *UnreachableError naming the
// volume otherwise. Nothing here ever creates the root's parent.
//
// # Resolution order, and the home fallback
//
//  1. DECKPLUS_DATA_DIR, then LD_DECK_DATA_DIR: always wins.
//  2. The WS-01 root (DefaultRoot, else LegacyDefaultRoot, else DefaultRoot when
//     its parent folder exists), when that volume is there.
//  3. Otherwise, a Mac that has never used the external root gets
//     ~/Library/Application Support/2t5-deck-plus/data (config/, logs/, state/
//     below it), so the app works on a machine with no 2T5-WS-01 volume.
//
// Step 3 is deliberately not a plain "if missing, use home": on the owner's Mac
// WS-01 can be unmounted at login, and a silent fallback would seed a second
// config tree on the boot disk. So the choice is sticky. The first time the
// external root is reachable, Dir/EnsureDirFor record it in the marker file
// ~/Library/Application Support/2t5-deck-plus/data-root (kind + path, written
// atomically, mode 0600). While that marker says "external" and the volume is
// missing, the root stays the external path and Dir returns *UnreachableError
// (nothing created, as before). A per-user folder that already exists without a
// marker (any older build) is treated as "external" too. The home root, and the
// "home" marker, are only written on a Mac with neither. Root() itself only
// reads; writes happen in Dir/EnsureDirFor.
package datadir

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvVar overrides the data root. Tests MUST set it (see internal/engine's
// TestMain) so no test run can reach the production root; it is also the
// supported escape hatch for running 2T5 DECK+ on a machine that has no
// 2T5-WS-01 volume.
const EnvVar = "DECKPLUS_DATA_DIR"

// LegacyEnvVar is the name EnvVar had before the rename to 2T5 DECK+. It is
// still read when EnvVar is unset or empty.
const LegacyEnvVar = "LD_DECK_DATA_DIR"

// DefaultRoot is the WS-01 data root. Its parent, /Volumes/2T5-WS-01/01-APPS/2t5-deck-plus,
// is the mount guard for the default root (see the package doc).
const DefaultRoot = "/Volumes/2T5-WS-01/01-APPS/2t5-deck-plus/data"

// LegacyDefaultRoot is where the data lived before the app folder was renamed.
// Root falls back to it while DefaultRoot does not exist, so the app works
// both before and after the folder is moved.
const LegacyDefaultRoot = "/Volumes/2T5-WS-01/01-APPS/ld-deck/data"

// The three subdirectories of the root. Callers pass these to Dir/Path
// rather than spelling the names, so the layout is defined in exactly one
// place.
const (
	Config = "config"
	Logs   = "logs"
	State  = "state"
)

// Root returns the resolved data root (see the package doc for the rules).
// Read-only: it stats and may read the small marker file, but never writes or
// creates anything, so it is safe to call from a package-level var initializer
// (which is how internal/engine seeds stateFilePath).
func Root() string {
	return resolve().root
}

var isDir = func(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

// Path joins sub and name onto the root WITHOUT creating anything or
// checking that the volume is mounted. Use it to name a file; use Dir or
// EnsureDirFor before actually writing one.
func Path(sub, name string) string {
	return filepath.Join(Root(), sub, name)
}

// Dir returns Root()/sub, creating it if the root is reachable. It returns
// *UnreachableError — and creates nothing at all — when the root's parent is
// missing, which is what an unmounted volume looks like.
func Dir(sub string) (string, error) {
	root, err := prepare()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, sub)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("datadir: create %s: %w", dir, err)
	}
	return dir, nil
}

// EnsureDirFor creates the directory that holds path. When path lies inside
// the data root the mount guard applies first; when it does not (a test that
// has redirected a specific file to its own t.TempDir, say) the parent is
// created directly. This is the call site for writers that already hold a
// full file path rather than a subdirectory name.
func EnsureDirFor(path string) error {
	dir := filepath.Dir(path)
	if underRoot(dir) {
		if _, err := prepare(); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("datadir: create %s: %w", dir, err)
	}
	return nil
}

// underRoot reports whether dir is the data root or lives beneath it.
func underRoot(dir string) bool {
	root := Root()
	return dir == root || strings.HasPrefix(dir, root+string(filepath.Separator))
}

// UnreachableError reports that the data root cannot be used because the
// directory that must contain it does not exist — on the default root that
// means the external volume is not mounted.
type UnreachableError struct {
	Root   string
	Parent string
}

func (e *UnreachableError) Error() string {
	if vol := volumeName(e.Root); vol != "" {
		return fmt.Sprintf("2t5-deck-plus data root %q is unreachable: volume %q does not appear to be mounted (%q does not exist). "+
			"Mount it, or point %s at another directory. Refusing to create the path — that would write to the boot disk.",
			e.Root, vol, e.Parent, EnvVar)
	}
	return fmt.Sprintf("2t5-deck-plus data root %q is unreachable: %q does not exist. "+
		"Create it, or point %s at another directory.", e.Root, e.Parent, EnvVar)
}

// volumeName returns the volume component of a /Volumes/<name>/... path, or
// "" for a path that is not on an external volume.
func volumeName(p string) string {
	const prefix = "/Volumes/"
	if !strings.HasPrefix(p, prefix) {
		return ""
	}
	rest := strings.TrimPrefix(p, prefix)
	if i := strings.IndexByte(rest, filepath.Separator); i >= 0 {
		return rest[:i]
	}
	return rest
}
