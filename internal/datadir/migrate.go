package datadir

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Names of the per-user folder before and after the rename to 2T5 DECK+. The
// folder lives in the user's Application Support (os.UserConfigDir) and holds
// the internal state (trust store, arm markers) and ai-access.json.
const (
	AppSupportName       = "2t5-deck-plus"
	LegacyAppSupportName = "ld-deck"
)

// LegacyDataFiles maps the state files that were renamed inside the data root
// (new name -> old name, both under state/).
var LegacyDataFiles = map[string]string{
	"2t5-deck-plus-state.json":     "ld-deck-state.json",
	"2t5-deck-plus-highlight.json": "ld-deck-highlight.json",
}

// AppSupportDirs returns the new and the legacy per-user folder
// (~/Library/Application Support/{2t5-deck-plus,ld-deck} on macOS).
func AppSupportDirs() (newDir, oldDir string, err error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", "", err
	}
	return filepath.Join(base, AppSupportName), filepath.Join(base, LegacyAppSupportName), nil
}

// MigrateDir COPIES oldDir to newDir when newDir does not exist yet and oldDir
// does. It never moves or deletes anything, never overwrites, and keeps file
// modes (the trust store is 0700, ai-access.json 0600). The copy is built in a
// sibling temp folder and renamed into place, so a crash leaves no half-made
// newDir. It reports whether a copy was made (false = nothing to do).
func MigrateDir(oldDir, newDir string) (bool, error) {
	if _, err := os.Stat(newDir); err == nil {
		return false, nil
	}
	fi, err := os.Stat(oldDir)
	if err != nil || !fi.IsDir() {
		return false, nil
	}
	tmp := fmt.Sprintf("%s.migrating-%d", newDir, os.Getpid())
	_ = os.RemoveAll(tmp) // our own leftover from a crashed earlier attempt with this pid
	if err := copyTree(oldDir, tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return false, fmt.Errorf("datadir: copy %s -> %s: %w", oldDir, newDir, err)
	}
	if err := os.Rename(tmp, newDir); err != nil {
		_ = os.RemoveAll(tmp)
		if _, e2 := os.Stat(newDir); e2 == nil {
			return false, nil // another process finished first
		}
		return false, err
	}
	return true, nil
}

// MigrateAppSupport runs MigrateDir for the per-user Application Support folder.
func MigrateAppSupport() (bool, string, string, error) {
	n, o, err := AppSupportDirs()
	if err != nil {
		return false, "", "", err
	}
	done, err := MigrateDir(o, n)
	return done, o, n, err
}

// MigrateDataFiles copies each renamed state file inside the data root from its
// old name to its new one when the new one is missing and the old one exists.
// It creates no directory (a missing state/ means nothing to migrate, and an
// unmounted volume is therefore never written to). Returns the new names copied.
func MigrateDataFiles() ([]string, error) {
	var copied []string
	dir := filepath.Join(Root(), State)
	if !isDir(dir) {
		return nil, nil
	}
	for newName, oldName := range LegacyDataFiles {
		dst, src := filepath.Join(dir, newName), filepath.Join(dir, oldName)
		if _, err := os.Stat(dst); err == nil {
			continue
		}
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := copyFile(src, dst, 0o644); err != nil {
			return copied, err
		}
		copied = append(copied, newName)
	}
	return copied, nil
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case d.IsDir():
			return os.MkdirAll(target, info.Mode().Perm()|0o700)
		case info.Mode().IsRegular():
			return copyFile(p, target, info.Mode().Perm())
		default:
			return nil // sockets, symlinks: not part of the state
		}
	})
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
