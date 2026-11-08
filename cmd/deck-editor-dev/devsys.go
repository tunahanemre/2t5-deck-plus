package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"2t5-deck-plus/internal/deps"
)

// devSys is the fake Mac behind GET /api/dependencies: Python, Resolve, the
// deck, other deck apps and the macOS version. POST /api/dev/system changes it.
// It reads no MIDI, runs no process and never opens System Settings.
type devSys struct {
	mu               sync.Mutex
	python           string // ok | missing | old | error
	device           string // present | absent | error
	otherApp         string // "" = none
	os               string
	resolveInstalled bool
	resolveRunning   bool
	scripting        string // auto | connected | retrying | waiting
	opened           []string
	setupDir         string
}

func newDevSys() *devSys {
	return &devSys{python: "ok", device: "present", os: "15.3.1", resolveInstalled: true, resolveRunning: true, scripting: "auto"}
}

// Facts implements editorapi.SystemProvider.
func (d *devSys) Facts(bool) deps.Inputs {
	d.mu.Lock()
	defer d.mu.Unlock()
	var in deps.Inputs
	if v, ok := deps.ParseVersion(d.os); ok {
		in.OS = &v
	}
	switch d.python {
	case "ok":
		in.Python = &deps.PythonFacts{Found: true, Path: "/opt/homebrew/bin/python3", Version: "3.12.4", VersionOK: true, StdlibOK: true, ResolveModule: d.resolveInstalled}
	case "old":
		in.Python = &deps.PythonFacts{Found: true, Path: "/usr/local/bin/python3", Version: "3.5.2", StdlibOK: true}
	case "missing":
		in.Python = &deps.PythonFacts{}
	default:
		in.Python = &deps.PythonFacts{Err: "dev: the Python check failed"}
	}
	in.ResolveInstalled = &d.resolveInstalled
	r := d.resolveRunning
	in.ResolveRunning = &r
	switch d.device {
	case "present":
		in.MIDI = &deps.MIDIFacts{Sources: []string{"Loupedeck Loupedeck MIDI", "IAC Driver Bus 1"}}
	case "absent":
		in.MIDI = &deps.MIDIFacts{Sources: []string{"IAC Driver Bus 1"}}
	default:
		in.MIDI = &deps.MIDIFacts{Err: "dev: CoreMIDI is not available"}
	}
	apps := []string{}
	if d.otherApp != "" {
		apps = append(apps, d.otherApp)
	}
	in.OtherApps = &apps
	return in
}

// scriptingState is the sidecar state to report, "" = derive it as before.
func (d *devSys) scriptingState() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.scripting == "auto" {
		return ""
	}
	return d.scripting
}

func (d *devSys) openURL(u string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.opened = append(d.opened, u)
	return nil
}

func (d *devSys) snapshot() map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return map[string]any{"python": d.python, "device": d.device, "otherApp": d.otherApp, "os": d.os,
		"resolveInstalled": d.resolveInstalled, "resolveRunning": d.resolveRunning, "scripting": d.scripting}
}

func oneOf(v string, ok ...string) bool {
	for _, o := range ok {
		if v == o {
			return true
		}
	}
	return false
}

func registerDevSys(mux *http.ServeMux, dev *DevState) {
	mux.HandleFunc("GET /api/dev/system", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, dev.sys.snapshot()) })
	mux.HandleFunc("POST /api/dev/setup-reset", func(w http.ResponseWriter, r *http.Request) {
		if !dev.guard(w, r) {
			return
		}
		_ = os.Remove(filepath.Join(dev.sys.setupDir, deps.MarkerName))
		writeJSON(w, 200, map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /api/dev/opened", func(w http.ResponseWriter, r *http.Request) {
		dev.sys.mu.Lock()
		defer dev.sys.mu.Unlock()
		writeJSON(w, 200, map[string]any{"opened": append([]string{}, dev.sys.opened...)})
	})
	mux.HandleFunc("POST /api/dev/system", func(w http.ResponseWriter, r *http.Request) {
		if !dev.guard(w, r) {
			return
		}
		var b struct {
			Python           *string `json:"python"`
			Device           *string `json:"device"`
			OtherApp         *string `json:"otherApp"`
			OS               *string `json:"os"`
			ResolveInstalled *bool   `json:"resolveInstalled"`
			ResolveRunning   *bool   `json:"resolveRunning"`
			Scripting        *string `json:"scripting"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&b); err != nil {
			writeJSON(w, 400, map[string]string{"error": "bad JSON body"})
			return
		}
		bad := func(field, msg string) { writeJSON(w, 400, map[string]string{"error": msg, "field": field}) }
		switch {
		case b.Python != nil && !oneOf(*b.Python, "ok", "missing", "old", "error"):
			bad("python", "python must be ok, missing, old or error")
			return
		case b.Device != nil && !oneOf(*b.Device, "present", "absent", "error"):
			bad("device", "device must be present, absent or error")
			return
		case b.Scripting != nil && !oneOf(*b.Scripting, "auto", "connected", "retrying", "waiting"):
			bad("scripting", "scripting must be auto, connected, retrying or waiting")
			return
		}
		if b.OS != nil {
			if _, ok := deps.ParseVersion(*b.OS); !ok {
				bad("os", "os must look like 15.3.1")
				return
			}
		}
		d := dev.sys
		d.mu.Lock()
		if b.Python != nil {
			d.python = *b.Python
		}
		if b.Device != nil {
			d.device = *b.Device
		}
		if b.OtherApp != nil {
			d.otherApp = *b.OtherApp
		}
		if b.OS != nil {
			d.os = *b.OS
		}
		if b.ResolveInstalled != nil {
			d.resolveInstalled = *b.ResolveInstalled
		}
		if b.ResolveRunning != nil {
			d.resolveRunning = *b.ResolveRunning
		}
		if b.Scripting != nil {
			d.scripting = *b.Scripting
		}
		d.mu.Unlock()
		writeJSON(w, 200, d.snapshot())
	})
}
