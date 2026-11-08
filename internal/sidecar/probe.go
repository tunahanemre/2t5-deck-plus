package sidecar

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// This file answers "could the Resolve helper run on this Mac?" without
// starting it: which Python it would use, whether that Python is new enough and
// has every standard-library module the helper imports, and whether it can
// import DaVinciResolveScript. Every read goes through a package var so tests
// never run a real Python.

// appleStub is the Command Line Tools stub. Running it on a Mac without the
// tools makes macOS pop an "install developer tools" window, so it is only ever
// run when the tools are there.
const appleStub = "/usr/bin/python3"

// minPython is the oldest Python 3 Resolve's own scripting module supports.
const minPythonMinor = 6

// probeScript prints "<version>|<stdlib ok>|<resolve module ok>". The stdlib
// list is every module sidecar.py, resolve_driver.py and cdl_state.py import.
const probeScript = `import sys
v = sys.version_info
std = 1
try:
    import json, os, re, socket, struct, time, traceback
except Exception:
    std = 0
res = 1
try:
    import DaVinciResolveScript
except Exception:
    res = 0
print("%d.%d.%d|%d|%d" % (v[0], v[1], v[2], std, res))
`

const probeTimeout = 8 * time.Second

// PythonStatus is one probe's answer.
type PythonStatus struct {
	// Err is set when an interpreter exists but could not be asked (timeout, bad output).
	Err string
	// Found: an interpreter that runs.
	Found         bool
	Path, Version string
	VersionOK     bool
	StdlibOK      bool
	ResolveModule bool
}

func (s PythonStatus) usable() bool { return s.Found && s.VersionOK && s.StdlibOK }

// runProbeFn runs probeScript with the interpreter and returns its output.
var runProbeFn = runProbe

func runProbe(pythonPath string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, pythonPath, "-c", probeScript)
	cmd.Env = append(os.Environ(), "PYTHONPATH="+resolveModulesDirFn()+string(os.PathListSeparator)+os.Getenv("PYTHONPATH"))
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return "", fmt.Errorf("%s did not answer within %s", pythonPath, probeTimeout)
	}
	if err != nil {
		return "", fmt.Errorf("%s: %w", pythonPath, err)
	}
	return string(out), nil
}

// cltInstalledFn reports whether the Command Line Tools (or Xcode) are installed.
// `xcode-select -p` only prints the developer folder; it opens no window.
var cltInstalledFn = func() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "xcode-select", "-p").Run() == nil
}

// skipCandidate says a candidate must not be run on this Mac.
func skipCandidate(path string) bool { return path == appleStub && !cltInstalledFn() }

func parseProbe(out string) (PythonStatus, error) {
	f := strings.Split(strings.TrimSpace(out), "|")
	if len(f) != 3 {
		return PythonStatus{}, fmt.Errorf("unreadable answer %q", strings.TrimSpace(out))
	}
	parts := strings.Split(f[0], ".")
	if len(parts) < 2 {
		return PythonStatus{}, fmt.Errorf("unreadable version %q", f[0])
	}
	major, e1 := strconv.Atoi(parts[0])
	minor, e2 := strconv.Atoi(parts[1])
	if e1 != nil || e2 != nil {
		return PythonStatus{}, fmt.Errorf("unreadable version %q", f[0])
	}
	return PythonStatus{Found: true, Version: f[0], VersionOK: major == 3 && minor >= minPythonMinor, StdlibOK: f[1] == "1", ResolveModule: f[2] == "1"}, nil
}

// ProbePython looks at the same candidates Launch does, in the same order.
// It returns the one Launch would use (the first that can import
// DaVinciResolveScript); failing that, the first usable one; failing that, the
// first that runs at all. Read only: it starts a short-lived python process per
// candidate and nothing else.
func ProbePython() PythonStatus {
	var best *PythonStatus
	var lastErr string
	for _, c := range candidatesFn() {
		if c == "" {
			continue
		}
		if fi, err := os.Stat(c); err != nil || fi.IsDir() {
			continue
		}
		if skipCandidate(c) {
			continue
		}
		out, err := runProbeFn(c)
		if err != nil {
			lastErr = err.Error()
			continue
		}
		st, err := parseProbe(out)
		if err != nil {
			lastErr = c + ": " + err.Error()
			continue
		}
		st.Path = c
		if st.usable() && st.ResolveModule {
			return st
		}
		if best == nil || (!best.usable() && st.usable()) {
			cp := st
			best = &cp
		}
	}
	if best != nil {
		return *best
	}
	return PythonStatus{Err: lastErr}
}

// resolveModulesDirFn is where Resolve keeps DaVinciResolveScript.py (a var so
// tests can point it at a temp folder).
var resolveModulesDirFn = func() string { return resolveModulesDir }

// ResolveScriptingInstalled reports whether Resolve's scripting module is on disk.
func ResolveScriptingInstalled() bool {
	fi, err := os.Stat(filepath.Join(resolveModulesDirFn(), "DaVinciResolveScript.py"))
	return err == nil && !fi.IsDir()
}
