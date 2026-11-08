package main

import (
	"2t5-deck-plus/internal/deps"
	"2t5-deck-plus/internal/macapp"
	"2t5-deck-plus/internal/sidecar"
)

// setupMarkerDir is where the per-Mac first-run marker lives ("" under go test or
// when HOME is unknown: no marker, nothing written).
func setupMarkerDir() string {
	if underGoTest() {
		return ""
	}
	d, err := deps.MarkerDir()
	if err != nil {
		return ""
	}
	return d
}

// systemProbes are the real, read-only Mac reads behind GET /api/dependencies.
// None of them opens a MIDI port, signals a process or asks macOS for a
// permission. The logic that turns them into statuses is in internal/deps and
// is tested there with stubs.
func systemProbes() deps.Probes {
	return deps.Probes{
		OSVersion: func() (deps.Version, bool) { return deps.ParseVersion(macapp.OSVersion()) },
		Python: func() deps.PythonFacts {
			st := sidecar.ProbePython()
			return deps.PythonFacts{Err: st.Err, Found: st.Found, Path: st.Path, Version: st.Version,
				VersionOK: st.VersionOK, StdlibOK: st.StdlibOK, ResolveModule: st.ResolveModule}
		},
		ResolveInstalled: sidecar.ResolveScriptingInstalled,
		ResolveRunning:   macapp.IsResolveRunning,
		MIDISources:      macapp.MIDISourceNames,
		RunningApps: func() []deps.App {
			var out []deps.App
			for _, a := range macapp.RunningApps() {
				out = append(out, deps.App{ID: a.ID, Name: a.Name})
			}
			return out
		},
		AppRunning: macapp.IsAppRunning,
	}
}
