package datadir

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDirs makes only the listed paths "exist" for the default-root choice.
func fakeDirs(t *testing.T, existing ...string) {
	t.Helper()
	old := isDir
	t.Cleanup(func() { isDir = old })
	isDir = func(p string) bool {
		for _, e := range existing {
			if e == p {
				return true
			}
		}
		return false
	}
}

func TestRoot_DefaultsToWS01(t *testing.T) {
	t.Setenv(EnvVar, "")
	t.Setenv(LegacyEnvVar, "")
	fakeHome(t)
	fakeDirs(t, DefaultRoot, LegacyDefaultRoot) // both present: the new folder wins
	if got := Root(); got != DefaultRoot {
		t.Errorf("Root() with %s unset = %q, want %q", EnvVar, got, DefaultRoot)
	}
	fakeDirs(t, LegacyDefaultRoot) // folder not renamed yet: the app keeps working on the old one
	if got := Root(); got != LegacyDefaultRoot {
		t.Errorf("Root() before the folder move = %q, want %q", got, LegacyDefaultRoot)
	}
	home := fakeHome(t)
	fakeDirs(t) // neither, no marker: the per-user home root (see marker_test.go)
	want := filepath.Join(home, "Library", "Application Support", "2t5-deck-plus", "data")
	if got := Root(); got != want {
		t.Errorf("Root() with neither folder = %q, want %q", got, want)
	}
}

func TestRoot_EnvOverrideWins(t *testing.T) {
	t.Setenv(EnvVar, "/tmp/somewhere-else")
	if got := Root(); got != "/tmp/somewhere-else" {
		t.Errorf("Root() = %q, want the override", got)
	}
}

// A whitespace-only override is a mistake (an unset variable exported as ""),
// not a request to use the current directory.
func TestRoot_BlankOverrideFallsBackToDefault(t *testing.T) {
	t.Setenv(EnvVar, "   ")
	t.Setenv(LegacyEnvVar, "")
	fakeHome(t)
	fakeDirs(t, DefaultRoot)
	if got := Root(); got != DefaultRoot {
		t.Errorf("Root() with a blank override = %q, want %q", got, DefaultRoot)
	}
}

func TestPath_IsPureAndCreatesNothing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "root")
	t.Setenv(EnvVar, root)

	got := Path(State, "2t5-deck-plus-state.json")
	want := filepath.Join(root, "state", "2t5-deck-plus-state.json")
	if got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("Path() must not create anything, but %s exists", root)
	}
}

func TestDir_CreatesSubdirUnderReachableRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "2T5-DECK-PLUS-DATA") // parent exists, root does not
	t.Setenv(EnvVar, root)

	for _, sub := range []string{Config, Logs, State} {
		dir, err := Dir(sub)
		if err != nil {
			t.Fatalf("Dir(%q) error = %v, want nil", sub, err)
		}
		if want := filepath.Join(root, sub); dir != want {
			t.Errorf("Dir(%q) = %q, want %q", sub, dir, want)
		}
		if info, err := os.Stat(dir); err != nil || !info.IsDir() {
			t.Errorf("Dir(%q) did not create %s", sub, dir)
		}
	}
}

// The whole point of the guard: an unmounted volume must produce an error and
// leave the filesystem untouched, never a freshly created path on the boot
// disk. Regression cover for the 2026-08-27 incident that put 162 GB on the
// internal SSD via an unmounted mount point.
func TestDir_UnreachableRoot_ErrorsAndCreatesNothing(t *testing.T) {
	base := t.TempDir()
	missingVolume := filepath.Join(base, "Volumes", "NOT-MOUNTED")
	root := filepath.Join(missingVolume, "2T5-DECK-PLUS-DATA")
	t.Setenv(EnvVar, root)

	dir, err := Dir(Config)
	if err == nil {
		t.Fatalf("Dir() with a missing root parent returned %q and no error — the mount guard is not working", dir)
	}
	var ue *UnreachableError
	if !errors.As(err, &ue) {
		t.Fatalf("Dir() error = %T (%v), want *UnreachableError", err, err)
	}
	if _, statErr := os.Stat(missingVolume); !os.IsNotExist(statErr) {
		t.Fatalf("Dir() created %s despite the guard — this is the boot-disk-fill bug", missingVolume)
	}
}

func TestEnsureDirFor_UnreachableRoot_ErrorsAndCreatesNothing(t *testing.T) {
	base := t.TempDir()
	missingVolume := filepath.Join(base, "Volumes", "NOT-MOUNTED")
	root := filepath.Join(missingVolume, "2T5-DECK-PLUS-DATA")
	t.Setenv(EnvVar, root)

	if err := EnsureDirFor(filepath.Join(root, "state", "x.json")); err == nil {
		t.Fatal("EnsureDirFor() inside an unreachable root returned nil, want an error")
	}
	if _, err := os.Stat(missingVolume); !os.IsNotExist(err) {
		t.Fatalf("EnsureDirFor() created %s despite the guard", missingVolume)
	}
}

// A path OUTSIDE the data root (a test that redirected one specific file to
// its own temp dir) must not be blocked by the mount guard.
func TestEnsureDirFor_PathOutsideRoot_IsCreatedNormally(t *testing.T) {
	t.Setenv(EnvVar, filepath.Join(t.TempDir(), "unmounted", "2T5-DECK-PLUS-DATA"))

	elsewhere := filepath.Join(t.TempDir(), "nested", "file.json")
	if err := EnsureDirFor(elsewhere); err != nil {
		t.Fatalf("EnsureDirFor(%q) error = %v, want nil", elsewhere, err)
	}
	if info, err := os.Stat(filepath.Dir(elsewhere)); err != nil || !info.IsDir() {
		t.Errorf("EnsureDirFor() did not create %s", filepath.Dir(elsewhere))
	}
}

// The error a user actually sees must name the volume they need to mount and
// the escape hatch, not just a bare ENOENT.
func TestUnreachableError_NamesVolumeAndEscapeHatch(t *testing.T) {
	err := &UnreachableError{Root: DefaultRoot, Parent: filepath.Dir(DefaultRoot)}
	msg := err.Error()
	for _, want := range []string{"2T5-WS-01", DefaultRoot, EnvVar} {
		if !strings.Contains(msg, want) {
			t.Errorf("UnreachableError message %q does not mention %q", msg, want)
		}
	}
}

func TestVolumeName(t *testing.T) {
	cases := map[string]string{
		"/Volumes/2T5-WS-01/01-APPS/2t5-deck-plus/data": "2T5-WS-01",
		"/Volumes/2T5-WS-01":                            "2T5-WS-01",
		"/Users/someone/data":                           "",
		"relative/path":                                 "",
	}
	for in, want := range cases {
		if got := volumeName(in); got != want {
			t.Errorf("volumeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRootEnvPrecedenceAndLegacy(t *testing.T) {
	t.Setenv(EnvVar, "")
	t.Setenv(LegacyEnvVar, "/tmp/legacy-root")
	if got := Root(); got != "/tmp/legacy-root" {
		t.Errorf("legacy env: %q", got)
	}
	t.Setenv(EnvVar, "/tmp/new-root")
	if got := Root(); got != "/tmp/new-root" {
		t.Errorf("new env must win: %q", got)
	}
}

func TestMigrateDirCopiesNeverMoves(t *testing.T) {
	base := t.TempDir()
	oldDir, newDir := filepath.Join(base, "ld-deck"), filepath.Join(base, "2t5-deck-plus")
	if err := os.MkdirAll(filepath.Join(oldDir, "state", "trust"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "ai-access.json"), []byte(`{"token":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(oldDir, "state", "trust", "manifest.json"), []byte("m"), 0o600); err != nil {
		t.Fatal(err)
	}
	done, err := MigrateDir(oldDir, newDir)
	if err != nil || !done {
		t.Fatalf("first run: %v %v", done, err)
	}
	b, err := os.ReadFile(filepath.Join(newDir, "ai-access.json"))
	if err != nil || string(b) != `{"token":"x"}` {
		t.Fatalf("copied file: %q %v", b, err)
	}
	if fi, _ := os.Stat(filepath.Join(newDir, "ai-access.json")); fi.Mode().Perm() != 0o600 {
		t.Errorf("mode lost: %v", fi.Mode())
	}
	if _, err := os.Stat(filepath.Join(oldDir, "state", "trust", "manifest.json")); err != nil {
		t.Errorf("old copy must stay: %v", err)
	}
	// second run and a changed new dir are left alone
	if err := os.WriteFile(filepath.Join(newDir, "ai-access.json"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if done, err := MigrateDir(oldDir, newDir); done || err != nil {
		t.Errorf("second run: %v %v", done, err)
	}
	if b, _ := os.ReadFile(filepath.Join(newDir, "ai-access.json")); string(b) != "changed" {
		t.Errorf("overwrote the new dir: %q", b)
	}
	// nothing to do when the old one is missing
	if done, err := MigrateDir(filepath.Join(base, "nope"), filepath.Join(base, "x")); done || err != nil {
		t.Errorf("missing old: %v %v", done, err)
	}
}

func TestMigrateDataFilesCopiesRenamedStateFiles(t *testing.T) {
	root := t.TempDir()
	t.Setenv(EnvVar, root)
	st := filepath.Join(root, State)
	if err := os.MkdirAll(st, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(st, "ld-deck-state.json"), []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := MigrateDataFiles()
	if err != nil || len(got) != 1 || got[0] != "2t5-deck-plus-state.json" {
		t.Fatalf("got %v %v", got, err)
	}
	if b, _ := os.ReadFile(filepath.Join(st, "2t5-deck-plus-state.json")); string(b) != "old" {
		t.Errorf("content %q", b)
	}
	if _, err := os.Stat(filepath.Join(st, "ld-deck-state.json")); err != nil {
		t.Errorf("old file must stay: %v", err)
	}
	if again, _ := MigrateDataFiles(); len(again) != 0 {
		t.Errorf("not idempotent: %v", again)
	}
	// no state dir: nothing created
	t.Setenv(EnvVar, filepath.Join(t.TempDir(), "unmounted"))
	if got, err := MigrateDataFiles(); got != nil || err != nil {
		t.Errorf("unmounted: %v %v", got, err)
	}
}
