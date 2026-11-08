package sidecar

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// These tests never run a real Python, never run xcode-select, and never touch
// /usr/bin/python3: every system read is a package var replaced here.

func withProbe(t *testing.T, fn func(string) (string, error), clt bool) *[]string {
	t.Helper()
	var ran []string
	origRun, origCLT := runProbeFn, cltInstalledFn
	runProbeFn = func(p string) (string, error) { ran = append(ran, p); return fn(p) }
	cltInstalledFn = func() bool { return clt }
	t.Cleanup(func() { runProbeFn, cltInstalledFn = origRun, origCLT })
	return &ran
}

func TestProbePython_NothingOnDisk(t *testing.T) {
	dir := t.TempDir()
	withCandidates(t, []string{filepath.Join(dir, "a"), filepath.Join(dir, "b")})
	ran := withProbe(t, func(string) (string, error) { t.Fatal("ran a missing interpreter"); return "", nil }, true)
	st := ProbePython()
	if st.Found || st.Err != "" {
		t.Errorf("want a clean not-found, got %+v", st)
	}
	if len(*ran) != 0 {
		t.Errorf("ran %v", *ran)
	}
}

func TestProbePython_UsableAndResolveCapable(t *testing.T) {
	dir := t.TempDir()
	a, b := fakeExecutable(t, dir, "py-a"), fakeExecutable(t, dir, "py-b")
	withCandidates(t, []string{a, b})
	withProbe(t, func(p string) (string, error) {
		if p == a {
			return "3.12.4|1|0\n", nil // works, no Resolve module
		}
		return "3.11.9|1|1\n", nil // the one Launch would pick
	}, true)
	st := ProbePython()
	if !st.Found || st.Path != b || st.Version != "3.11.9" || !st.VersionOK || !st.StdlibOK || !st.ResolveModule {
		t.Errorf("want py-b with the Resolve module, got %+v", st)
	}
}

func TestProbePython_UsableWithoutResolveStillFound(t *testing.T) {
	dir := t.TempDir()
	a := fakeExecutable(t, dir, "py-a")
	withCandidates(t, []string{a})
	withProbe(t, func(string) (string, error) { return "3.13.0|1|0", nil }, true)
	st := ProbePython()
	if !st.Found || !st.VersionOK || !st.StdlibOK || st.ResolveModule || st.Path != a {
		t.Errorf("%+v", st)
	}
}

func TestProbePython_TooOldAndBrokenStdlib(t *testing.T) {
	dir := t.TempDir()
	a := fakeExecutable(t, dir, "py-a")
	withCandidates(t, []string{a})
	withProbe(t, func(string) (string, error) { return "3.5.2|1|0", nil }, true)
	if st := ProbePython(); !st.Found || st.VersionOK {
		t.Errorf("3.5 must not be VersionOK: %+v", st)
	}
	withProbe(t, func(string) (string, error) { return "3.12.0|0|0", nil }, true)
	if st := ProbePython(); !st.Found || st.StdlibOK {
		t.Errorf("a failed stdlib import must not be StdlibOK: %+v", st)
	}
	withProbe(t, func(string) (string, error) { return "2.7.18|1|0", nil }, true)
	if st := ProbePython(); st.VersionOK {
		t.Errorf("Python 2 must not be VersionOK: %+v", st)
	}
}

func TestProbePython_AUsableLaterOneBeatsAnUnusableEarlierOne(t *testing.T) {
	dir := t.TempDir()
	a, b := fakeExecutable(t, dir, "py-old"), fakeExecutable(t, dir, "py-new")
	withCandidates(t, []string{a, b})
	withProbe(t, func(p string) (string, error) {
		if p == a {
			return "3.5.0|1|0", nil
		}
		return "3.12.0|1|0", nil
	}, true)
	if st := ProbePython(); st.Path != b || !st.VersionOK {
		t.Errorf("%+v", st)
	}
}

func TestProbePython_RunFailureIsAnErrorNotNotFound(t *testing.T) {
	dir := t.TempDir()
	a := fakeExecutable(t, dir, "py-a")
	withCandidates(t, []string{a})
	withProbe(t, func(string) (string, error) { return "", errors.New("timed out") }, true)
	st := ProbePython()
	if st.Found || !strings.Contains(st.Err, "timed out") {
		t.Errorf("%+v", st)
	}
	withProbe(t, func(string) (string, error) { return "garbage", nil }, true)
	if st := ProbePython(); st.Found || st.Err == "" {
		t.Errorf("unparsable output: %+v", st)
	}
}

func TestProbePython_AppleStubIsSkippedWithoutCommandLineTools(t *testing.T) {
	withCandidates(t, []string{"/usr/bin/python3"})
	ran := withProbe(t, func(string) (string, error) { return "3.9.6|1|0", nil }, false)
	st := ProbePython()
	if len(*ran) != 0 {
		t.Fatalf("ran the Apple stub with no Command Line Tools (that opens a system install prompt): %v", *ran)
	}
	if st.Found {
		t.Errorf("%+v", st)
	}
	ran = withProbe(t, func(string) (string, error) { return "3.9.6|1|0", nil }, true)
	if st := ProbePython(); !st.Found || len(*ran) != 1 {
		t.Errorf("with the tools installed the stub is a normal candidate: %+v %v", st, *ran)
	}
}

func TestDiscoverPython3_SkipsAppleStubWithoutCommandLineTools(t *testing.T) {
	withCandidates(t, []string{"/usr/bin/python3"})
	var verified []string
	withVerify(t, func(p string) error { verified = append(verified, p); return nil })
	orig := cltInstalledFn
	cltInstalledFn = func() bool { return false }
	t.Cleanup(func() { cltInstalledFn = orig })
	if _, err := DiscoverPython3(); !errors.Is(err, ErrPython3NotFound) {
		t.Errorf("err = %v, want ErrPython3NotFound", err)
	}
	if len(verified) != 0 {
		t.Errorf("ran the Apple stub: %v", verified)
	}
}

func TestResolveScriptingInstalled_UsesTheModulesFolder(t *testing.T) {
	dir := t.TempDir()
	orig := resolveModulesDirFn
	resolveModulesDirFn = func() string { return dir }
	t.Cleanup(func() { resolveModulesDirFn = orig })
	if ResolveScriptingInstalled() {
		t.Error("empty folder reported as installed")
	}
	fakeExecutable(t, dir, "DaVinciResolveScript.py")
	if !ResolveScriptingInstalled() {
		t.Error("folder with DaVinciResolveScript.py reported as missing")
	}
}
