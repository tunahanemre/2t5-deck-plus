package datadir

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// fakeHome points the per-user folder at a temp HOME and clears the env overrides.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	old := userHomeDir
	userHomeDir = func() (string, error) { return home, nil }
	t.Cleanup(func() { userHomeDir = old })
	t.Setenv(EnvVar, "")
	t.Setenv(LegacyEnvVar, "")
	return home
}

func appSupport(home string) string {
	return filepath.Join(home, "Library", "Application Support", "2t5-deck-plus")
}

func homeRootOf(home string) string { return filepath.Join(appSupport(home), "data") }

func writeTestMarker(t *testing.T, home, body string) {
	t.Helper()
	if err := os.MkdirAll(appSupport(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(appSupport(home), MarkerName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readTestMarker(t *testing.T, home string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(appSupport(home), MarkerName))
	if err != nil {
		return ""
	}
	return string(b)
}

// fakeVolume points the external roots at a temp "WS-01" whose app folder exists
// (mounted) or not. Returns the data root it would use.
func fakeVolume(t *testing.T, mounted bool) string {
	t.Helper()
	vol := filepath.Join(t.TempDir(), "Volumes", "WS")
	od, ol := extDefault, extLegacy
	t.Cleanup(func() { extDefault, extLegacy = od, ol })
	extDefault = filepath.Join(vol, "2t5-deck-plus", "data")
	extLegacy = filepath.Join(vol, "ld-deck", "data")
	if mounted {
		if err := os.MkdirAll(filepath.Dir(extDefault), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return extDefault
}

// --- Root() resolution -------------------------------------------------------

// His install must resolve to exactly today's WS-01 paths, with the real
// constants and a stubbed stat, and Root() must write nothing.
func TestRoot_WS01Present_SameAsToday(t *testing.T) {
	home := fakeHome(t)
	cases := []struct {
		name     string
		existing []string
		want     string
	}{
		{"data folder", []string{DefaultRoot}, DefaultRoot},
		{"both", []string{DefaultRoot, LegacyDefaultRoot}, DefaultRoot},
		{"legacy only", []string{LegacyDefaultRoot}, LegacyDefaultRoot},
		{"app folder only (first run on the volume)", []string{filepath.Dir(DefaultRoot)}, DefaultRoot},
	}
	for _, c := range cases {
		fakeDirs(t, c.existing...)
		if got := Root(); got != c.want {
			t.Errorf("%s: Root() = %q, want %q", c.name, got, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "Library")); !os.IsNotExist(err) {
		t.Errorf("Root() created something under HOME")
	}
}

func TestRoot_EnvWinsOverMarkerAndVolume(t *testing.T) {
	home := fakeHome(t)
	writeTestMarker(t, home, "external\n/Volumes/X/data\n")
	t.Setenv(EnvVar, "/tmp/from-env")
	if got := Root(); got != "/tmp/from-env" {
		t.Errorf("Root() = %q, want the env override", got)
	}
	t.Setenv(EnvVar, "")
	t.Setenv(LegacyEnvVar, "/tmp/from-legacy-env")
	if got := Root(); got != "/tmp/from-legacy-env" {
		t.Errorf("Root() = %q, want the legacy env override", got)
	}
}

func TestRoot_NoVolume_NoMarker_UsesHome(t *testing.T) {
	home := fakeHome(t)
	fakeDirs(t)
	if got, want := Root(), homeRootOf(home); got != want {
		t.Errorf("Root() = %q, want %q", got, want)
	}
}

func TestRoot_NoVolume_MarkerExternal_StaysExternal(t *testing.T) {
	home := fakeHome(t)
	fakeDirs(t)
	writeTestMarker(t, home, "external\n"+LegacyDefaultRoot+"\n")
	if got := Root(); got != LegacyDefaultRoot {
		t.Errorf("Root() = %q, want the recorded external root %q", got, LegacyDefaultRoot)
	}
}

func TestRoot_NoVolume_MarkerHome_UsesHome(t *testing.T) {
	home := fakeHome(t)
	fakeDirs(t)
	writeTestMarker(t, home, "home\n"+homeRootOf(home)+"\n")
	if got, want := Root(), homeRootOf(home); got != want {
		t.Errorf("Root() = %q, want %q", got, want)
	}
}

// An older build left the per-user folder behind but knew no marker: that Mac
// used WS-01, so it must not grow a home tree.
func TestRoot_PriorInstallWithoutMarker_TreatedAsExternal(t *testing.T) {
	home := fakeHome(t)
	fakeDirs(t, appSupport(home))
	if got := Root(); got != DefaultRoot {
		t.Errorf("Root() = %q, want %q (unreachable external)", got, DefaultRoot)
	}
	fakeDirs(t, filepath.Join(filepath.Dir(appSupport(home)), "ld-deck"))
	if got := Root(); got != DefaultRoot {
		t.Errorf("Root() with only the legacy folder = %q, want %q", got, DefaultRoot)
	}
}

func TestRoot_GarbageMarkerIgnored(t *testing.T) {
	home := fakeHome(t)
	ext := fakeVolume(t, false)
	writeTestMarker(t, home, "banana\n")
	// the folder exists and the marker is unusable: same as a prior install
	if got := Root(); got != ext {
		t.Errorf("Root() = %q, want %q", got, ext)
	}
}

// --- Dir / EnsureDirFor ------------------------------------------------------

func TestDir_HomeFallback_CreatesTreeAndMarker(t *testing.T) {
	home := fakeHome(t)
	fakeVolume(t, false)
	for _, sub := range []string{Config, Logs, State} {
		dir, err := Dir(sub)
		if err != nil {
			t.Fatalf("Dir(%q) = %v", sub, err)
		}
		if want := filepath.Join(homeRootOf(home), sub); dir != want {
			t.Errorf("Dir(%q) = %q, want %q", sub, dir, want)
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o755 {
			t.Errorf("Dir(%q): %v %v", sub, fi, err)
		}
	}
	m := filepath.Join(appSupport(home), MarkerName)
	fi, err := os.Stat(m)
	if err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("marker mode = %v, want 0600", fi.Mode().Perm())
	}
	if got := readTestMarker(t, home); got != "home\n"+homeRootOf(home)+"\n" {
		t.Errorf("marker = %q", got)
	}
	// still home on the next resolution
	if got := Root(); got != homeRootOf(home) {
		t.Errorf("Root() after fallback = %q", got)
	}
}

func TestDir_ExternalReachable_WritesMarkerOnce(t *testing.T) {
	home := fakeHome(t)
	ext := fakeVolume(t, true)
	dir, err := Dir(Config)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(ext, Config); dir != want {
		t.Errorf("Dir = %q, want %q", dir, want)
	}
	if got := readTestMarker(t, home); got != "external\n"+ext+"\n" {
		t.Fatalf("marker = %q", got)
	}
	m := filepath.Join(appSupport(home), MarkerName)
	before, _ := os.Stat(m)
	if fi, _ := os.Stat(m); fi.Mode().Perm() != 0o600 {
		t.Errorf("marker mode = %v", fi.Mode().Perm())
	}
	if _, err := Dir(Logs); err != nil {
		t.Fatal(err)
	}
	if err := EnsureDirFor(filepath.Join(ext, State, "x.json")); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(m)
	if !os.SameFile(before, after) {
		t.Errorf("marker was rewritten although it already said external")
	}
	if _, err := os.Stat(homeRootOf(home)); !os.IsNotExist(err) {
		t.Errorf("a home tree appeared although the external root is reachable")
	}
}

// Once external, forever external: the volume vanishing must not create a home tree.
func TestDir_MarkerExternal_VolumeGone_ErrorsCreatesNothing(t *testing.T) {
	home := fakeHome(t)
	ext := fakeVolume(t, true)
	if _, err := Dir(Config); err != nil {
		t.Fatal(err)
	}
	// unmount: remove the whole fake volume
	if err := os.RemoveAll(filepath.Dir(filepath.Dir(ext))); err != nil {
		t.Fatal(err)
	}
	dir, err := Dir(Config)
	var ue *UnreachableError
	if !errors.As(err, &ue) {
		t.Fatalf("Dir() = %q, %v; want *UnreachableError", dir, err)
	}
	if ue.Root != ext {
		t.Errorf("error names %q, want %q", ue.Root, ext)
	}
	if err := EnsureDirFor(filepath.Join(ext, State, "x.json")); err == nil {
		t.Errorf("EnsureDirFor() = nil, want an error")
	}
	if _, err := os.Stat(homeRootOf(home)); !os.IsNotExist(err) {
		t.Errorf("a home tree was created while the marker says external")
	}
	if _, err := os.Stat(filepath.Dir(filepath.Dir(ext))); !os.IsNotExist(err) {
		t.Errorf("the missing volume was re-created")
	}
}

func TestDir_MarkerExternalWritten_RealWS01Constants_NothingCreated(t *testing.T) {
	home := fakeHome(t)
	fakeDirs(t) // WS-01 "unmounted"
	writeTestMarker(t, home, "external\n"+DefaultRoot+"\n")
	if _, err := Dir(Config); err == nil {
		t.Fatal("Dir() = nil error, want UnreachableError")
	}
	if _, err := os.Stat(homeRootOf(home)); !os.IsNotExist(err) {
		t.Errorf("home tree created")
	}
}

func TestDir_MarkerHome_UnreachableVolume_UsesHome(t *testing.T) {
	home := fakeHome(t)
	fakeVolume(t, false)
	writeTestMarker(t, home, "home\n"+homeRootOf(home)+"\n")
	dir, err := Dir(State)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(homeRootOf(home), State); dir != want {
		t.Errorf("Dir = %q, want %q", dir, want)
	}
}

// If WS-01 does appear, it wins over a "home" marker and is recorded.
func TestDir_MarkerHome_VolumeAppears_ExternalWins(t *testing.T) {
	home := fakeHome(t)
	ext := fakeVolume(t, true)
	writeTestMarker(t, home, "home\n"+homeRootOf(home)+"\n")
	dir, err := Dir(Config)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(ext, Config); dir != want {
		t.Errorf("Dir = %q, want %q", dir, want)
	}
	if got := readTestMarker(t, home); got != "external\n"+ext+"\n" {
		t.Errorf("marker = %q", got)
	}
}

func TestDir_LegacyExternalRoot_StillWorks(t *testing.T) {
	home := fakeHome(t)
	ext := fakeVolume(t, false)
	legacy := filepath.Join(filepath.Dir(filepath.Dir(ext)), "ld-deck", "data")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	dir, err := Dir(Config)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(legacy, Config); dir != want {
		t.Errorf("Dir = %q, want %q", dir, want)
	}
	if got := readTestMarker(t, home); got != "external\n"+legacy+"\n" {
		t.Errorf("marker = %q", got)
	}
}

func TestEnsureDirFor_HomeFallback(t *testing.T) {
	home := fakeHome(t)
	fakeVolume(t, false)
	p := filepath.Join(homeRootOf(home), State, "2t5-deck-plus-state.json")
	if err := EnsureDirFor(p); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Dir(p)); err != nil || !fi.IsDir() {
		t.Errorf("state dir missing: %v", err)
	}
	if readTestMarker(t, home) == "" {
		t.Errorf("marker not written")
	}
}

func TestDir_EnvOverride_WritesNoMarker(t *testing.T) {
	home := fakeHome(t)
	t.Setenv(EnvVar, filepath.Join(t.TempDir(), "data"))
	if _, err := Dir(Config); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(appSupport(home)); !os.IsNotExist(err) {
		t.Errorf("env override touched the per-user folder")
	}
}

// Creating the per-user folder for the marker must not pre-empt the one-time
// ld-deck -> 2t5-deck-plus copy.
func TestMarkerWrite_DoesNotPreemptLegacyFolderMigration(t *testing.T) {
	home := fakeHome(t)
	fakeVolume(t, true)
	old := filepath.Join(filepath.Dir(appSupport(home)), "ld-deck")
	if err := os.MkdirAll(old, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(old, "ai-access.json"), []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Dir(Config); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(appSupport(home), "ai-access.json"))
	if err != nil || string(b) != "tok" {
		t.Errorf("legacy folder was not carried over: %q %v", b, err)
	}
}

func TestHomeRoot_NoHome(t *testing.T) {
	fakeHome(t)
	userHomeDir = func() (string, error) { return "", errors.New("no HOME") }
	fakeDirs(t)
	if got := Root(); got != DefaultRoot {
		t.Errorf("Root() with no home = %q, want %q (today's behaviour)", got, DefaultRoot)
	}
}
