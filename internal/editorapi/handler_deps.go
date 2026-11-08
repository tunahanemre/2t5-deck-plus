package editorapi

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"sync"
	"time"

	"2t5-deck-plus/docs"
	"2t5-deck-plus/internal/deps"
)

// GET /api/dependencies answers "does this Mac have everything the app needs?"
// as the shared list in docs/dependencies.json, each row with a live status.
// POST /api/open-settings opens one System Settings pane for a fix button.
// POST /api/setup/done marks this Mac's first-run screen as seen.
// The first two only read or open a window; none changes a macOS setting.

// SystemProvider supplies the Mac facts the permission and engine providers do
// not hold (Python, Resolve, MIDI, other deck apps, macOS version).
// fresh forces the slow reads to run again. deps.System implements it.
type SystemProvider interface{ Facts(fresh bool) deps.Inputs }

// settingsPanes are the only things POST /api/open-settings will open. The
// caller names a pane; it never supplies a URL.
var settingsPanes = map[string]string{
	"accessibility":    "x-apple.systempreferences:com.apple.preference.security?Privacy_Accessibility",
	"screen-recording": "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture",
	"automation":       "x-apple.systempreferences:com.apple.preference.security?Privacy_Automation",
	"software-update":  "x-apple.systempreferences:com.apple.preferences.softwareupdate",
}

var (
	catalogOnce sync.Once
	catalog     []deps.Entry
	catalogErr  error
)

func loadCatalog() ([]deps.Entry, error) {
	catalogOnce.Do(func() { catalog, catalogErr = deps.LoadCatalog(docs.DependenciesJSON) })
	return catalog, catalogErr
}

func (s *server) routesDeps(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/dependencies", s.dependencies)
	mux.HandleFunc("POST /api/open-settings", s.openSettings)
	mux.HandleFunc("POST /api/setup/done", s.setupDone)
}

// depSnapshot is one look at the Mac.
type depSnapshot struct {
	items    []map[string]any
	status   map[string]string
	required []string // required and missing
	optional []string // optional and missing
	counts   map[string]int
}

func (s *server) depLook(cat []deps.Entry, fresh bool) depSnapshot {
	var in deps.Inputs
	if s.d.System != nil {
		in = s.d.System.Facts(fresh)
	}
	// the permission and sidecar rows come from the same providers the preflight card uses
	if s.d.Perms != nil {
		p := s.d.Perms.Permissions()
		in.Perms = &deps.Perms{Accessibility: p.Accessibility, ScreenRecording: p.ScreenRecording, AutomationFinder: p.AutomationFinder}
	}
	if s.d.Live != nil {
		in.Sidecar = s.d.Live.Live().Sidecar
	}
	res := map[string]deps.Result{}
	for _, c := range deps.Evaluate(in) {
		res[c.ID] = c
	}
	snap := depSnapshot{status: map[string]string{}, required: []string{}, optional: []string{}, counts: map[string]int{"ok": 0, "missing": 0, "unknown": 0, "info": 0}}
	for _, e := range cat {
		status, detail := "info", ""
		if !e.Informational {
			c, ok := res[e.ID]
			if !ok {
				c = deps.Result{ID: e.ID, Status: deps.Unknown, Detail: "no check is wired for this yet"}
			}
			status, detail = c.Status, c.Detail
		}
		fix := e.FixAction
		if status == deps.OK || status == "info" {
			fix = "" // nothing to fix
		}
		snap.status[e.ID] = status
		snap.counts[status]++
		if status == deps.Missing {
			if e.Required {
				snap.required = append(snap.required, e.ID)
			} else {
				snap.optional = append(snap.optional, e.ID)
			}
		}
		snap.items = append(snap.items, map[string]any{
			"id": e.ID, "name": e.Name, "kind": e.Kind, "required": e.Required, "feature": e.Feature, "features": e.Features,
			"howToGet": e.HowToGet, "howToCheck": e.HowToCheck, "without": e.Without,
			"status": status, "detail": detail, "fixAction": fix, "copyText": e.CopyText, "informational": e.Informational,
		})
	}
	return snap
}

func (s *server) dependencies(w http.ResponseWriter, r *http.Request) {
	cat, err := loadCatalog()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	snap := s.depLook(cat, r.URL.Query().Get("fresh") == "1")
	out := map[string]any{"ok": len(snap.required) == 0, "items": snap.items, "counts": snap.counts, "requiredMissing": snap.required,
		"firstRun": false, "setupNeeded": len(snap.required) > 0, "cameBack": []string{}}
	if s.d.SetupDir != "" {
		m, _ := deps.ReadMarker(s.d.SetupDir)
		rc := deps.Reconcile(cat, snap.status, m)
		if rc.Changed && m != nil {
			m.OK = rc.NewOK
			_ = deps.WriteMarker(s.d.SetupDir, *m) // best effort: a failed write only means one more look next time
		}
		for _, it := range snap.items {
			if it["required"] == true && it["informational"] != true {
				it["wasOK"] = rc.WasOK[it["id"].(string)]
			}
		}
		out["firstRun"] = rc.FirstRun
		out["setupNeeded"] = rc.FirstRun || len(snap.required) > 0
		if rc.CameBack != nil {
			out["cameBack"] = rc.CameBack
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// setupDone records that this Mac has seen the Requirements screen. Closing it with a
// required item missing is allowed; the marker remembers what was ok so only a LATER loss brings it back.
func (s *server) setupDone(w http.ResponseWriter, r *http.Request) {
	if s.d.SetupDir == "" {
		writeErr(w, http.StatusNotFound, "", "this host keeps no setup marker")
		return
	}
	cat, err := loadCatalog()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	snap := s.depLook(cat, false)
	ok := []string{}
	for _, e := range cat {
		if e.Required && !e.Informational && snap.status[e.ID] == deps.OK {
			ok = append(ok, e.ID)
		}
	}
	sort.Strings(ok)
	host, _ := os.Hostname()
	m := deps.Marker{Host: host, DoneAt: time.Now().Format(time.RFC3339), OK: ok, RequiredMissing: snap.required, OptionalSkipped: snap.optional}
	if err := deps.WriteMarker(s.d.SetupDir, m); err != nil {
		writeErr(w, http.StatusInternalServerError, "", "could not save the setup marker: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "firstRun": false, "setupNeeded": len(snap.required) > 0, "requiredMissing": snap.required, "optionalSkipped": snap.optional})
}

func (s *server) openSettings(w http.ResponseWriter, r *http.Request) {
	if s.d.OpenURL == nil {
		writeErr(w, http.StatusNotFound, "", "this host cannot open System Settings")
		return
	}
	var body struct {
		Pane string `json:"pane"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "pane", "expected {\"pane\": \"accessibility\"}")
		return
	}
	url, ok := settingsPanes[body.Pane]
	if !ok {
		writeErr(w, http.StatusBadRequest, "pane", "unknown pane")
		return
	}
	if err := s.d.OpenURL(url); err != nil {
		writeErr(w, http.StatusInternalServerError, "", "could not open System Settings: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pane": body.Pane})
}
