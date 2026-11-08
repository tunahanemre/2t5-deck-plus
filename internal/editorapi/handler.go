// Package editorapi is the HTTP/JSON API of the 2T5 DECK+ preset editor. It is
// mounted by the Wails asset handler (the app) and by cmd/deck-editor-dev.
// It imports no MIDI, engine or input package: everything it needs from the
// running app arrives through the small interfaces in Deps.
//
// Borrowed: the deck picture data (device profile + layout rects) from
// DECKMAP internal/device/profiles/loupedeck-plus.json (copied, embedded).
package editorapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"2t5-deck-plus/internal/actions"
	"2t5-deck-plus/internal/mousecal"
	"2t5-deck-plus/internal/preset"
)

// AppRef identifies an application.
type AppRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// FrontmostProvider reports the frontmost GUI app (zero value = unknown).
type FrontmostProvider interface{ Frontmost() AppRef }

// RunningProvider lists running GUI apps for the picker.
type RunningProvider interface{ Running() []AppRef }

// LiveState is what the engine and sidecar tell the editor.
type LiveState struct {
	EngineRunning bool   `json:"engineRunning"`
	Sidecar       string `json:"sidecar"` // waiting | connected | retrying
	// Project is the title of Resolve's front window (the project name as Resolve
	// shows it), "" when Resolve has none; GET /api/live "resolveProject".
	Project string `json:"project,omitempty"`
	// SliderMode is the engine's P-row mode ("hue", "sat", "lum"; "" = unknown), GET /api/live "sliderMode".
	// It is the engine's own state, set by the Hue / Sat / Lum key presses it sees; the deck's LEDs are firmware and cannot be read back.
	SliderMode string `json:"-"`
	// CustomMode is the engine-tracked Custom Mode toggle, GET /api/live "customMode".
	CustomMode bool `json:"-"`
	// Layer is the engine's active deck layer (1-3), GET /api/live "layer": the editor follows it (0 = unknown).
	Layer int `json:"-"`
}

// DeckStateSetter changes the deck's lamp state (Hue / Sat / Lum, Custom Mode) as a press would. mode "" and custom nil leave that part alone.
type DeckStateSetter interface {
	SetDeckState(mode string, custom *bool) error
}

// LiveProvider reports engine / sidecar state.
type LiveProvider interface{ Live() LiveState }

// FrontInfo is what the app knows about why the frontmost app is what it is
// (wave C, GET /api/live "why").
type FrontInfo struct {
	// EditorFocused: 2T5 DECK+ itself has focus, so Frontmost() reports the last
	// real app instead of the editor.
	EditorFocused bool
	// Since is Unix milliseconds when the reported frontmost app (or the
	// editor-focused state) last changed. 0 = unknown.
	Since int64
}

// FrontInfoProvider supplies FrontInfo. Optional.
type FrontInfoProvider interface{ FrontInfo() FrontInfo }

// SimRequest is the body of POST /api/simulate.
type SimRequest struct {
	Control string `json:"control"`
	Kind    string `json:"kind"` // turn | press | release
	Ticks   int    `json:"ticks"`
	// SliderMode ("hue", "sat", "lum") tries the control as if that slider mode
	// were live; omitted = the Simulator's Mode func, else no mode.
	SliderMode string `json:"sliderMode,omitempty"`
}

// Deps wires the handler to the app. Store is required; the rest may be nil.
type Deps struct {
	Store     *preset.Store
	Frontmost FrontmostProvider
	Running   RunningProvider
	Live      LiveProvider
	// Why explains the active preset in /api/live (nil = reasons are derived
	// without the editor-focused state and "since" is 0).
	Why FrontInfoProvider
	Bus *Bus
	// Icons renders app icons for /api/icon and /api/appicon (nil = 404).
	Icons IconProvider
	// Simulate enables POST /api/simulate (dev server only). nil = 404.
	Simulate func(SimRequest) (Event, error)
	// Engine reports whether the engine took the newest snapshot (wave D).
	// nil = every change counts as applied at once.
	Engine EngineProvider
	// DeckState sets the slider mode / Custom Mode like a deck press (POST /api/deck-state); nil = 404.
	DeckState DeckStateSetter
	// Perms reports macOS permission readiness for GET /api/live. nil = the
	// "permissions" block is left out.
	Perms PermissionsProvider
	// Arm is the safety switch behind POST /api/arm. nil = 404, and /api/live
	// reports armed:true.
	Arm ArmProvider
	// Switches is the "why did it switch" log behind GET /api/switches and
	// /api/live why.switchLog (wave F). nil = an always-empty log.
	Switches SwitchProvider
	// ArmStart is how the app came up (ApplyStartPolicy); nil = armed at start.
	ArmStart *ArmStart
	// Token, when set, must accompany every mutating request as the header
	// X-LD-Token (constant-time compare, 403 otherwise), and a mutating request
	// with an empty Host is refused. The packaged app generates one per launch
	// (memory only) and hands it to the page through App.EditorToken(); the dev
	// server and the tests run without one unless --token is given. "" = none.
	Token string
	// ArmMark reports whether the .disarmed marker could be saved (the packaged
	// app's ArmMarkerWriter): warnings and preflight show "could not save the
	// disarmed state". nil = nothing to report.
	ArmMark interface{ LastError() string }
	// Env answers the preflight checks the store and the engine cannot (WS-01
	// mount, the old app running). nil = those checks are "unknown".
	Env EnvProvider
	// Events is the events ring behind GET /api/events-log (nil = the handler
	// keeps its own, which still records what the handler itself does).
	Events *EventLog
	// Native is the import token store shared with the app's native import
	// (nil = the handler keeps its own).
	Native *NativeIO
	// AllowedHosts are extra Host header names the API accepts besides
	// 127.0.0.1, localhost, [::1] and the Wails asset host.
	AllowedHosts []string
	// Calib is the mouse-calibration service behind /api/calibration (nil = 404).
	Calib *mousecal.Service
	// AI is the "AI access" switch behind GET/PUT /api/ai-access (nil = 404, the editor hides the control).
	AI AIAccessProvider
	// System answers GET /api/dependencies with the Mac facts the permission and
	// engine providers do not hold. nil = those rows read "unknown".
	System SystemProvider
	// OpenURL opens one System Settings pane for POST /api/open-settings (the
	// app passes the same opener its Open*Settings methods use). nil = 404.
	OpenURL func(url string) error
	// SetupDir is the per-user folder that holds the first-run marker (setup.json). "" = no marker: the editor then keeps no first-run state.
	SetupDir string
	// Version is the product version shown by GET /api/about ("" = unknown; the app passes wails.json's productVersion).
	Version string
	// Extra routes (e.g. the dev server's /api/dev/frontmost) are added by
	// wrapping the returned handler.
}

type server struct {
	d  Deps
	ic iconCache

	nativeOnce sync.Once
	nativeOwn  *NativeIO
	eventsOnce sync.Once
	eventsOwn  *EventLog
}

// NewHandler returns the API handler. Routes are absolute (/api/...).
func NewHandler(d Deps) http.Handler {
	if d.Bus == nil {
		d.Bus = NewBus()
	}
	s := &server{d: d}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/deck", s.deck)
	mux.HandleFunc("GET /api/registry", s.registry)
	mux.HandleFunc("GET /api/apps", s.apps)
	mux.HandleFunc("POST /api/apps", s.addApp)
	mux.HandleFunc("POST /api/apps/capture", s.capture)
	mux.HandleFunc("GET /api/running", s.running)
	mux.HandleFunc("GET /api/preset/{id}", s.getPreset)
	mux.HandleFunc("PUT /api/preset/{id}/assign", s.assign)
	mux.HandleFunc("DELETE /api/preset/{id}/assign", s.unassign)
	mux.HandleFunc("PUT /api/preset/{id}/value", s.value)
	mux.HandleFunc("PUT /api/preset/{id}/settings", s.setting)
	mux.HandleFunc("PUT /api/preset/{id}/packs", s.packs)
	mux.HandleFunc("POST /api/undo", func(w http.ResponseWriter, r *http.Request) { s.step(w, true) })
	mux.HandleFunc("POST /api/redo", func(w http.ResponseWriter, r *http.Request) { s.step(w, false) })
	mux.HandleFunc("GET /api/live", s.live)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("GET /api/client-log", s.clientLog)
	mux.HandleFunc("POST /api/client-log", s.clientLog)
	mux.HandleFunc("GET /api/events-log", s.eventsLog)
	mux.HandleFunc("GET /api/preflight", s.preflight)
	mux.HandleFunc("GET /api/pending-external", s.pendingExternal)
	mux.HandleFunc("POST /api/pending-external/{id}/approve", s.approveExternal)
	mux.HandleFunc("POST /api/pending-external/{id}/reject", s.rejectExternal)
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("POST /api/simulate", s.simulate)
	s.routesB(mux)
	s.routesC(mux)
	s.routesD(mux)
	s.routesCal(mux)
	s.routesAI(mux)
	s.routesDeps(mux)
	s.routesAbout(mux)
	s.registerModes(mux)
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeErr(w, http.StatusNotFound, "", "no such endpoint "+r.Method+" "+r.URL.Path)
	})
	return hostGuard(d.AllowedHosts, sameOriginToken(d.Token, mux))
}

// EditorHeader is the custom request header every mutating request must carry
// (value "1"). A cross-site page cannot set it without a CORS preflight, which
// this API never answers, so a hostile web page cannot drive the editor even
// when it can reach the loopback port.
const EditorHeader = "X-LD-Editor"

// zipRoutes take a zip (or multipart) body; every other body is JSON.
func zipRoute(path string) bool { return path == "/api/import" }

func mediaType(r *http.Request) string {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return ""
	}
	return mt
}

// CheckMutation vets a request that changes state (any method but GET, HEAD
// and OPTIONS): a non-null Origin on this host (or a non-web scheme such as
// wails://), the X-LD-Editor header, and a JSON content type for any body (a
// zip or multipart for import). It returns the HTTP status and message to
// refuse with, or 0.
func CheckMutation(r *http.Request) (int, string) {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return 0, ""
	}
	if o := r.Header.Get("Origin"); o != "" {
		if o == "null" {
			return http.StatusForbidden, "request with a null origin refused"
		}
		u, err := url.Parse(o)
		if err != nil {
			return http.StatusForbidden, "unreadable Origin refused"
		}
		if (u.Scheme == "http" || u.Scheme == "https") && u.Host != r.Host {
			return http.StatusForbidden, "cross-origin request refused"
		}
	}
	if r.Header.Get(EditorHeader) != "1" {
		return http.StatusForbidden, "missing " + EditorHeader + " header: only the 2T5 DECK+ editor may change presets"
	}
	if r.ContentLength != 0 { // a body is present (or its length unknown)
		mt := mediaType(r)
		if zipRoute(r.URL.Path) {
			switch mt {
			case "application/zip", "application/x-zip-compressed", "application/octet-stream", "multipart/form-data":
				return 0, ""
			}
			return http.StatusUnsupportedMediaType, "import takes a zip file (Content-Type application/zip)"
		}
		if mt != "application/json" {
			return http.StatusUnsupportedMediaType, "send the body as application/json"
		}
	}
	return 0, ""
}

// TokenHeader carries the per-launch API token on every mutating request when
// the handler was built with Deps.Token.
const TokenHeader = "X-LD-Token"

// CheckMutationToken is CheckMutation plus the per-launch token: when token is
// not empty a mutating request must carry it (constant-time compare) and must
// have a Host header. It returns the HTTP status and message to refuse with, or 0.
func CheckMutationToken(r *http.Request, token string) (int, string) {
	if code, msg := CheckMutation(r); code != 0 {
		return code, msg
	}
	if token == "" {
		return 0, ""
	}
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return 0, ""
	}
	if strings.TrimSpace(r.Host) == "" {
		return http.StatusForbidden, "request without a Host header refused"
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get(TokenHeader)), []byte(token)) != 1 {
		return http.StatusForbidden, "missing or wrong " + TokenHeader + " header: this editor session's token is required to change presets"
	}
	return 0, ""
}

// sameOrigin applies CheckMutation to every request.
func sameOrigin(next http.Handler) http.Handler { return sameOriginToken("", next) }

// sameOriginToken applies CheckMutationToken to every request.
func sameOriginToken(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code, msg := CheckMutationToken(r, token); code != 0 {
			if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
				log.Printf("[editor] refused %s %s (%d): %s", r.Method, r.URL.Path, code, msg) // a silent refusal looks like "the button does nothing"
			}
			writeErr(w, code, "", msg)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
}

func writeErr(w http.ResponseWriter, code int, field, msg string) {
	body := map[string]string{"error": msg}
	if field != "" {
		body["field"] = field
	}
	writeJSON(w, code, body)
}

// fail maps store/registry errors to HTTP statuses.
func fail(w http.ResponseWriter, err error) {
	var fe *actions.FieldError
	var roe *preset.ReadOnlyError
	switch {
	case errors.As(err, &roe):
		writeJSON(w, http.StatusLocked, map[string]any{"error": roe.Error(), "readonly": true, "holderPid": roe.Holder})
	case errors.Is(err, preset.ErrFallbackReadOnly):
		writeErr(w, http.StatusConflict, "id", err.Error())
	case errors.Is(err, preset.ErrWouldNotCompose):
		writeErr(w, http.StatusConflict, "", err.Error())
	case errors.Is(err, preset.ErrNotBroken):
		writeErr(w, http.StatusConflict, "id", err.Error())
	case errors.Is(err, preset.ErrNoGoodCopy):
		writeErr(w, http.StatusNotFound, "id", err.Error())
	case errors.Is(err, preset.ErrNoShippedPreset):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error(), "field": "id", "reason": "no-shipped-preset"})
	case errors.Is(err, preset.ErrNoTemplate):
		writeErr(w, http.StatusNotFound, "templateId", err.Error())
	case errors.Is(err, preset.ErrSystemApp):
		writeErr(w, http.StatusBadRequest, "id", err.Error())
	case errors.As(err, &fe):
		writeErr(w, http.StatusBadRequest, fe.Field, fe.Msg)
	case errors.Is(err, preset.ErrNoPreset):
		writeErr(w, http.StatusNotFound, "id", err.Error())
	case errors.Is(err, preset.ErrNoControl):
		writeErr(w, http.StatusNotFound, "control", err.Error())
	case errors.Is(err, preset.ErrExists):
		writeErr(w, http.StatusConflict, "id", err.Error())
	default:
		writeErr(w, http.StatusInternalServerError, "", err.Error())
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	d := json.NewDecoder(r.Body)
	d.UseNumber()
	if err := d.Decode(v); err != nil {
		log.Printf("[editor] %s %s: bad JSON body (%d bytes announced): %v", r.Method, r.URL.Path, r.ContentLength, err) // a 400 here was invisible: "the button does nothing"
		writeErr(w, http.StatusBadRequest, "", "bad JSON body: "+err.Error())
		return false
	}
	return true
}

func (s *server) deck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(DeckJSON())
}

func (s *server) registry(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"packs": actions.Default().Packs(), "settings": actions.Settings()})
}

func (s *server) front() AppRef {
	if s.d.Frontmost == nil {
		return AppRef{}
	}
	return s.d.Frontmost.Frontmost()
}

type appRow struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Packs     []string `json:"packs"`
	IsGlobal  bool     `json:"isGlobal"`
	Assigned  int      `json:"assigned"`
	Frontmost bool     `json:"frontmost"`
	// Installed: the app is on this Mac (its bundle was found). Global is always true.
	Installed bool `json:"installed"`
}

func (s *server) apps(w http.ResponseWriter, r *http.Request) {
	front := s.front()
	rows := []appRow{}
	for _, p := range s.d.Store.Snapshot().Presets() {
		rows = append(rows, appRow{ID: p.ID, Name: p.Name, Packs: p.Packs, IsGlobal: p.ID == preset.GlobalID,
			Assigned: p.Assigned(), Frontmost: front.ID != "" && p.ID == front.ID, Installed: p.ID == preset.GlobalID || s.installed(p.ID)})
	}
	writeJSON(w, 200, rows)
}

func rowOf(p *preset.Preset, front AppRef) appRow {
	return appRow{ID: p.ID, Name: p.Name, Packs: p.Packs, IsGlobal: p.ID == preset.GlobalID, Assigned: p.Assigned(), Frontmost: p.ID == front.ID}
}

func (s *server) addApp(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID    string   `json:"id"`
		Name  string   `json:"name"`
		Packs []string `json:"packs"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !preset.ValidBundleID(body.ID) {
		writeErr(w, http.StatusBadRequest, "id", quote(body.ID)+" is not a bundle id: use the reverse-DNS form, like com.company.App")
		return
	}
	p, err := s.d.Store.AddApp(body.ID, body.Name, body.Packs)
	if err != nil {
		fail(w, err)
		return
	}
	s.writeRow(w, http.StatusCreated, rowOf(p, s.front()))
}

func (s *server) capture(w http.ResponseWriter, r *http.Request) {
	f := s.front()
	if f.ID == "" {
		writeErr(w, http.StatusConflict, "", "no frontmost app to capture")
		return
	}
	if p, ok := s.d.Store.Snapshot().Preset(f.ID); ok {
		writeJSON(w, 200, rowOf(p, f))
		return
	}
	p, err := s.d.Store.AddApp(f.ID, f.Name, nil)
	if err != nil {
		fail(w, err)
		return
	}
	s.writeRow(w, http.StatusCreated, rowOf(p, f))
}

func (s *server) running(w http.ResponseWriter, r *http.Request) {
	list := []AppRef{}
	if s.d.Running != nil {
		list = append(list, s.d.Running.Running()...)
	}
	sort.Slice(list, func(i, j int) bool { return strings.ToLower(list[i].Name) < strings.ToLower(list[j].Name) })
	writeJSON(w, 200, list)
}

func (s *server) getPreset(w http.ResponseWriter, r *http.Request) {
	snap := s.d.Store.Snapshot()
	p, ok := snap.Preset(r.PathValue("id"))
	if !ok {
		writeErr(w, http.StatusNotFound, "id", "no such preset "+r.PathValue("id"))
		return
	}
	// effective view over the preset's own controls plus, for apps, the
	// controls only Global assigns (so the editor can show what is inherited)
	eff := map[string]map[string]preset.Effective{}
	add := func(bank, control string) {
		if e, ok := snap.Effective(p.ID, bank, control); ok {
			if eff[bank] == nil {
				eff[bank] = map[string]preset.Effective{}
			}
			eff[bank][control] = e
		}
	}
	for bn, bm := range p.Banks {
		for cn := range bm {
			add(bn, cn)
		}
	}
	if g, ok := snap.Preset(preset.GlobalID); ok && p.ID != preset.GlobalID && p.ID != preset.ResolveID {
		for bn, bm := range g.Banks {
			for cn := range bm {
				add(bn, cn)
			}
		}
	}
	writeJSON(w, 200, map[string]any{"preset": p, "effective": eff, "seq": snap.Seq})
}

func (s *server) assign(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Bank       string         `json:"bank"`
		Control    string         `json:"control"`
		Assignment map[string]any `json:"assignment"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Assignment == nil {
		writeErr(w, http.StatusBadRequest, "assignment", "assignment is required")
		return
	}
	if err := s.d.Store.Assign(r.PathValue("id"), body.Bank, body.Control, body.Assignment); err != nil {
		fail(w, err)
		return
	}
	s.effectiveReply(w, r.PathValue("id"), body.Bank, body.Control)
}

func (s *server) effectiveReply(w http.ResponseWriter, id, bank, control string) {
	snap := s.d.Store.Snapshot()
	out := map[string]any{"ok": true, "seq": snap.Seq, "warnings": snap.DuplicatesFor(id, bank, control)}
	if e, ok := snap.Effective(id, bank, control); ok {
		out["effective"] = e
	}
	s.stamp(w, out)
	writeJSON(w, 200, out)
}

func (s *server) unassign(w http.ResponseWriter, r *http.Request) {
	bank, control := r.URL.Query().Get("bank"), r.URL.Query().Get("control")
	if bank == "" || control == "" {
		writeErr(w, http.StatusBadRequest, "bank", "bank and control are required")
		return
	}
	if err := s.d.Store.Unassign(r.PathValue("id"), bank, control); err != nil {
		fail(w, err)
		return
	}
	s.effectiveReply(w, r.PathValue("id"), bank, control)
}

func (s *server) value(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Bank    string `json:"bank"`
		Control string `json:"control"`
		Key     string `json:"key"`
		Value   any    `json:"value"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Key == "" {
		writeErr(w, http.StatusBadRequest, "key", "key is required")
		return
	}
	if err := s.d.Store.SetValue(r.PathValue("id"), body.Bank, body.Control, body.Key, body.Value); err != nil {
		fail(w, err)
		return
	}
	s.effectiveReply(w, r.PathValue("id"), body.Bank, body.Control)
}

func (s *server) setting(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Key   string `json:"key"`
		Value any    `json:"value"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.d.Store.SetSetting(r.PathValue("id"), body.Key, body.Value); err != nil {
		fail(w, err)
		return
	}
	s.okReply(w)
}

func (s *server) packs(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Packs []string `json:"packs"`
	}
	if !decode(w, r, &body) {
		return
	}
	if err := s.d.Store.SetPacks(r.PathValue("id"), body.Packs); err != nil {
		fail(w, err)
		return
	}
	s.okReply(w)
}

func (s *server) step(w http.ResponseWriter, undo bool) {
	var label string
	var ok bool
	var err error
	if undo {
		label, ok, err = s.d.Store.Undo()
	} else {
		label, ok, err = s.d.Store.Redo()
	}
	if err != nil {
		fail(w, err)
		return
	}
	out := map[string]any{"ok": ok, "label": label, "seq": s.d.Store.Snapshot().Seq}
	s.stamp(w, out)
	writeJSON(w, 200, out)
}

func (s *server) live(w http.ResponseWriter, r *http.Request) {
	f := s.front()
	st := LiveState{Sidecar: "waiting"}
	if s.d.Live != nil {
		st = s.d.Live.Live()
	}
	active := preset.GlobalID
	if _, ok := s.d.Store.Snapshot().Preset(f.ID); ok && f.ID != "" {
		active = f.ID
	}
	out := map[string]any{"frontmost": f, "activeApp": active, "engineRunning": st.EngineRunning,
		"sidecar": st.Sidecar, "seq": s.d.Store.Snapshot().Seq, "why": s.why(f, active),
		"engine": s.engineBlock(), "armed": s.armed(), "armCoverage": s.armCoverage(), "armedAtStart": s.d.ArmStart == nil || s.d.ArmStart.Armed, "resolveProject": st.Project, "sliderMode": st.SliderMode, "customMode": st.CustomMode, "layer": st.Layer}
	if s.d.ArmStart != nil && s.d.ArmStart.Reason != "" {
		out["startReason"] = s.d.ArmStart.Reason
	}
	if s.d.Perms != nil {
		out["permissions"] = s.d.Perms.Permissions()
	}
	writeJSON(w, 200, out)
}

func (s *server) events(w http.ResponseWriter, r *http.Request) {
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	// wait=<ms>: a long poll. Nothing newer yet = hold the request until the engine publishes
	// something (at most 25 s), so a deck press reaches the editor at once instead of at the next poll.
	var (
		next int64
		evs  []Event
	)
	if ms, _ := strconv.Atoi(r.URL.Query().Get("wait")); ms > 0 {
		if ms > 25000 {
			ms = 25000
		}
		next, evs = s.d.Bus.Wait(since, time.Duration(ms)*time.Millisecond, r.Context().Done())
	} else {
		next, evs = s.d.Bus.Since(since)
	}
	writeJSON(w, 200, map[string]any{"next": next, "events": evs})
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]any{"ok": true, "presetsDir": s.d.Store.Dir(), "problems": s.d.Store.Problems(),
		"problemList": s.d.Store.ProblemList(), "seq": s.d.Store.Snapshot().Seq})
}

func (s *server) simulate(w http.ResponseWriter, r *http.Request) {
	if s.d.Simulate == nil {
		writeErr(w, http.StatusNotFound, "", "simulate is only available on the dev server")
		return
	}
	var req SimRequest
	if !decode(w, r, &req) {
		return
	}
	ev, err := s.d.Simulate(req)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, ev)
}

// clientLogCap bounds one client-log line; clientLogPerMin bounds how many a minute reach the app log.
const (
	clientLogCap    = 500
	clientLogPerMin = 120
)

var (
	clientLogMu    sync.Mutex
	clientLogStart time.Time
	clientLogCount int
)

// GET /api/client-log?m=<text> (also POST {"m":"..."}): the editor's own errors, unhandled rejections and the outcome
// of its writes, written to the app log as "[client] ...". It is a GET on purpose: when writes are what fail inside the
// packaged app, a POST could not report that. It writes one capped, single-line, printable log line and nothing else;
// at most 120 lines a minute are kept.
func (s *server) clientLog(w http.ResponseWriter, r *http.Request) {
	m := r.URL.Query().Get("m")
	if r.Method == http.MethodPost {
		var body struct {
			M string `json:"m"`
		}
		_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
		m = body.M
	}
	m = strings.Map(func(c rune) rune {
		if c < 0x20 || c == 0x7f {
			return ' '
		}
		return c
	}, m)
	if len(m) > clientLogCap {
		m = m[:clientLogCap] + "..."
	}
	clientLogMu.Lock()
	now := time.Now()
	if now.Sub(clientLogStart) > time.Minute {
		clientLogStart, clientLogCount = now, 0
	}
	clientLogCount++
	ok := clientLogCount <= clientLogPerMin
	clientLogMu.Unlock()
	if ok && m != "" {
		log.Printf("[client] %s", m)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNoContent)
}
