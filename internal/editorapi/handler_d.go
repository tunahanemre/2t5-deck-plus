package editorapi

import (
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"2t5-deck-plus/internal/preset"
)

// Wave D endpoints and plumbing: engine/UI divergence reporting (B1), host
// header allow-list, permissions readiness, the arm switch, restore-last-good,
// starter templates and app removal.

// EngineState is what the app knows about the running engine's config.
type EngineState struct {
	// AppliedSeq is the Seq of the newest snapshot the engine composed and took.
	AppliedSeq int `json:"appliedSeq"`
	// ComposeError is the text of the newest failed compose ("" = none). The
	// engine keeps running the previous config while it is set.
	ComposeError string `json:"composeError"`
	// AppliedAt is when AppliedSeq was applied, Unix ms (0 = never).
	AppliedAt int64 `json:"appliedAt"`
}

// EngineProvider reports and awaits the engine's config state.
type EngineProvider interface {
	EngineState() EngineState
	// WaitApplied blocks up to d until the engine has taken snapshot seq (or
	// a compose of it, or of a newer one, failed), then returns the state.
	WaitApplied(seq int, d time.Duration) EngineState
}

// PermState is the permission readiness block of GET /api/live.
type PermState struct {
	Accessibility   bool `json:"accessibility"`
	ScreenRecording bool `json:"screenRecording"`
	// AutomationFinder: unknown | granted | denied.
	AutomationFinder string `json:"automationFinder"`
}

// PermissionsProvider reports macOS permission readiness (cheap, read-only).
type PermissionsProvider interface{ Permissions() PermState }

// ArmProvider is the armed / disarmed safety switch.
type ArmProvider interface {
	Armed() bool
	SetArmed(bool)
}

// ArmSourcer is implemented by an ArmProvider that reports its own changes
// (appdeck.Executor does, through OnArmChange): the handler then lets it do the
// bookkeeping instead of repeating it.
type ArmSourcer interface {
	SetArmedFrom(armed bool, source string)
}

// ArmCoverer is implemented by an ArmProvider that can say what disarming
// holds back (appdeck.Executor does): the names listed in GET /api/live
// "armCoverage" and GET /api/arm, so the UI states exactly what is covered
// instead of a blanket "nothing will fire". Known names: keys, mouse, scroll,
// shell, sidecar-writes, finder-tags.
type ArmCoverer interface{ ArmCoverage() []string }

// applyWait is how long a mutation reply waits for the engine to take the
// change, so a failed compose is reported in the same response.
const applyWait = 300 * time.Millisecond

func (s *server) routesD(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/arm", s.setArm)
	mux.HandleFunc("POST /api/deck-state", s.setDeckState)
	mux.HandleFunc("GET /api/arm", s.getArm)
	mux.HandleFunc("POST /api/preset/{id}/restore-last-good", s.restoreLastGood)
	mux.HandleFunc("GET /api/templates", s.templates)
	mux.HandleFunc("POST /api/apps/from-template", s.fromTemplate)
	mux.HandleFunc("DELETE /api/apps/{id}", s.removeApp)
}

// ---- engine / UI divergence (B1) --------------------------------------------------

// applyInfo is added to mutation replies: did the engine take the change?
type applyInfo struct {
	// Applied: the engine runs the saved config (false while it is still
	// composing, or when the compose failed: see ComposeError).
	Applied    bool `json:"applied"`
	AppliedSeq int  `json:"appliedSeq"`
	// ComposeError is only present when the compose failed.
	ComposeError string `json:"composeError,omitempty"`
}

func (s *server) info(w http.ResponseWriter) applyInfo {
	seq := s.seq()
	st := EngineState{AppliedSeq: seq}
	if s.d.Engine != nil {
		st = s.d.Engine.WaitApplied(seq, applyWait)
	}
	w.Header().Set("X-Applied-Seq", strconv.Itoa(st.AppliedSeq))
	return applyInfo{Applied: st.AppliedSeq >= seq && st.ComposeError == "", AppliedSeq: st.AppliedSeq, ComposeError: st.ComposeError}
}

// stamp adds the apply fields to a reply map (and sets X-Applied-Seq).
func (s *server) stamp(w http.ResponseWriter, out map[string]any) {
	in := s.info(w)
	out["applied"], out["appliedSeq"] = in.Applied, in.AppliedSeq
	if in.ComposeError != "" {
		out["composeError"] = in.ComposeError
	}
}

// okReply is the common {ok, seq} reply with the apply fields.
func (s *server) okReply(w http.ResponseWriter) {
	out := map[string]any{"ok": true, "seq": s.seq()}
	s.stamp(w, out)
	writeJSON(w, 200, out)
}

// writeRow writes an app row with the apply fields flattened into it.
func (s *server) writeRow(w http.ResponseWriter, code int, row appRow) {
	in := s.info(w)
	writeJSON(w, code, struct {
		appRow
		applyInfo
	}{row, in})
}

type engineJSON struct {
	AppliedSeq   int              `json:"appliedSeq"`
	LastSeq      int              `json:"lastSeq"`
	ComposeError string           `json:"composeError"`
	AppliedAt    int64            `json:"appliedAt"`
	Problems     []preset.Problem `json:"problems"`
	Readonly     bool             `json:"readonly"`
	HolderPid    int              `json:"holderPid,omitempty"`
	// Canary is the pending silent-loss report (wave F), absent when none.
	Canary *preset.CanaryInfo `json:"canary,omitempty"`
}

func (s *server) engineBlock() engineJSON {
	last := s.seq()
	e := engineJSON{AppliedSeq: last, LastSeq: last, Problems: s.d.Store.ProblemList()}
	if s.d.Engine != nil {
		st := s.d.Engine.EngineState()
		e.AppliedSeq, e.ComposeError, e.AppliedAt = st.AppliedSeq, st.ComposeError, st.AppliedAt
	}
	e.Readonly, e.HolderPid = s.d.Store.ReadOnly()
	e.Canary = s.d.Store.Canary()
	return e
}

// ---- Host header allow-list ------------------------------------------------------------

// HostGuard rejects requests whose Host header is not the editor's own, with
// 403 (DNS-rebinding defence: a page on evil.example resolving to 127.0.0.1
// still sends Host: evil.example). Allowed: 127.0.0.1, localhost, [::1] on any
// port, the Wails asset host ("wails", "wails.localhost") and extra. An empty
// Host is allowed: browsers always send one, so only non-browser callers
// (and Wails' internal asset requests) omit it. GETs are guarded too.
func HostGuard(next http.Handler, extra ...string) http.Handler { return hostGuard(extra, next) }

func hostGuard(extra []string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !HostAllowed(r.Host, extra) {
			writeErr(w, http.StatusForbidden, "", "request for another host refused")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// HostAllowed reports whether a Host header value is accepted (see HostGuard).
func HostAllowed(hostport string, extra []string) bool {
	h := strings.ToLower(strings.TrimSpace(hostport))
	if h == "" {
		return true
	}
	if host, _, err := net.SplitHostPort(h); err == nil {
		h = host
	} else if strings.HasPrefix(h, "[") && strings.HasSuffix(h, "]") {
		h = h[1 : len(h)-1]
	}
	switch h {
	case "127.0.0.1", "localhost", "::1", "wails", "wails.localhost":
		return true
	}
	for _, e := range extra {
		if strings.EqualFold(h, e) {
			return true
		}
	}
	return false
}

// ---- arm switch ---------------------------------------------------------------------------

func (s *server) armed() bool { return s.d.Arm == nil || s.d.Arm.Armed() }

// armCoverage lists what the switch holds back ([] when the provider cannot say).
func (s *server) armCoverage() []string {
	if c, ok := s.d.Arm.(ArmCoverer); ok {
		if l := c.ArmCoverage(); l != nil {
			return l
		}
	}
	return []string{}
}

func (s *server) getArm(w http.ResponseWriter, r *http.Request) {
	if s.d.Arm == nil {
		writeErr(w, http.StatusNotFound, "", "the arm switch is not available here")
		return
	}
	writeJSON(w, 200, map[string]any{"armed": s.armed(), "armCoverage": s.armCoverage()})
}

func (s *server) setArm(w http.ResponseWriter, r *http.Request) {
	if s.d.Arm == nil {
		writeErr(w, http.StatusNotFound, "", "the arm switch is not available here")
		return
	}
	var body struct {
		Armed *bool `json:"armed"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.Armed == nil {
		writeErr(w, http.StatusBadRequest, "armed", "armed is required (true or false)")
		return
	}
	if as, ok := s.d.Arm.(ArmSourcer); ok {
		// the switch owns the bookkeeping (events ring, .disarmed marker) in its
		// own change callback, under its own lock
		as.SetArmedFrom(*body.Armed, "editor")
	} else {
		was := s.d.Arm.Armed()
		s.d.Arm.SetArmed(*body.Armed)
		if was != *body.Armed {
			s.ring().Add("arm", map[bool]string{true: "armed", false: "disarmed"}[*body.Armed], "editor", nil)
		}
		if !*body.Armed {
			// a deliberate disarm is remembered: the next launch starts disarmed too
			if err := s.d.Store.MarkDisarmed(); err != nil {
				log.Printf("[editor] could not record the disarm: %v", err)
			}
		} else if err := s.d.Store.ClearDisarmed(); err != nil {
			log.Printf("[editor] could not clear the disarm marker: %v", err)
		}
	}
	// every arm / disarm request leaves a line: a click that "does nothing" must be visible in the log (result = what the switch says now)
	log.Printf("[editor] arm request from the editor: armed=%v -> switch is now armed=%v (outside changes held: %d)", *body.Armed, s.armed(), len(s.d.Store.PendingExternal()))
	if *body.Armed {
		// The user armed the deck deliberately: from now on this install no
		// longer starts disarmed on its own account (preset.FirstRunMarker).
		if err := s.d.Store.MarkFirstRunDone(); err != nil {
			log.Printf("[editor] could not record the first arming: %v", err)
		}
	}
	writeJSON(w, 200, map[string]any{"ok": true, "armed": s.armed(), "armCoverage": s.armCoverage()})
}

// ---- restore last good (B5) -------------------------------------------------------------------

func (s *server) restoreLastGood(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "global" {
		id = preset.GlobalID
	}
	src, err := s.d.Store.RestoreLastGood(id)
	if err != nil {
		fail(w, err)
		return
	}
	s.ring().Add("restore", "last good version of "+id, "editor", map[string]any{"source": src.Source})
	out := map[string]any{"ok": true, "seq": s.seq(), "source": src.Source}
	if src.File != "" {
		out["file"] = src.File
	}
	s.stamp(w, out)
	writeJSON(w, 200, out)
}

// ---- templates & app removal ----------------------------------------------------------------------

type templateRow struct {
	preset.Template
	// Exists: a preset for the template's bundle id already exists, so adding
	// it would be refused.
	Exists bool `json:"exists"`
	// Installed: the app is on this Mac. When false the tile uses the icon bundled with the template.
	Installed bool `json:"installed"`
}

func (s *server) templates(w http.ResponseWriter, r *http.Request) {
	if err := preset.TemplatesError(); err != nil {
		writeErr(w, http.StatusInternalServerError, "", err.Error())
		return
	}
	snap := s.d.Store.Snapshot()
	rows := []templateRow{}
	for _, t := range preset.Templates() {
		_, have := snap.Preset(t.BundleID)
		rows = append(rows, templateRow{t, have, s.installed(t.BundleID)})
	}
	writeJSON(w, 200, map[string]any{"templates": rows})
}

func (s *server) fromTemplate(w http.ResponseWriter, r *http.Request) {
	var body struct {
		TemplateID string `json:"templateId"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.TemplateID == "" {
		writeErr(w, http.StatusBadRequest, "templateId", "templateId is required")
		return
	}
	p, t, err := s.d.Store.AddFromTemplate(body.TemplateID)
	if err != nil {
		fail(w, err)
		return
	}
	in := s.info(w)
	writeJSON(w, http.StatusCreated, struct {
		appRow
		TemplateID string `json:"templateId"`
		Controls   int    `json:"controls"`
		applyInfo
	}{rowOf(p, s.front()), t.ID, p.Assigned(), in})
}

func (s *server) removeApp(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.d.Store.RemoveApp(id); err != nil {
		fail(w, err)
		return
	}
	out := map[string]any{"ok": true, "seq": s.seq(), "removed": id}
	s.stamp(w, out)
	writeJSON(w, 200, out)
}

// POST /api/deck-state {sliderMode?: "hue"|"sat"|"lum", customMode?: bool}: the editor's clickable deck picture. It
// changes the engine's lamp state through the same code as a deck press (and so publishes the deck-state event). Token
// protected like every write; allowed while the deck is disarmed because it is internal state with no key output.
func (s *server) setDeckState(w http.ResponseWriter, r *http.Request) {
	if s.d.DeckState == nil {
		writeErr(w, http.StatusNotFound, "", "the deck state cannot be set here")
		return
	}
	var body struct {
		SliderMode string `json:"sliderMode"`
		CustomMode *bool  `json:"customMode"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.SliderMode == "" && body.CustomMode == nil {
		writeErr(w, http.StatusBadRequest, "sliderMode", "send sliderMode (hue, sat or lum) and/or customMode (true or false)")
		return
	}
	switch body.SliderMode {
	case "", "hue", "sat", "lum":
	default:
		writeErr(w, http.StatusBadRequest, "sliderMode", "sliderMode must be hue, sat or lum")
		return
	}
	custom := "unchanged"
	if body.CustomMode != nil {
		custom = strconv.FormatBool(*body.CustomMode)
	}
	if err := s.d.DeckState.SetDeckState(body.SliderMode, body.CustomMode); err != nil {
		log.Printf("[editor] deck-state request (sliderMode=%q customMode=%s) FAILED: %v", body.SliderMode, custom, err)
		writeErr(w, http.StatusConflict, "", err.Error())
		return
	}
	log.Printf("[editor] deck-state request: sliderMode=%q customMode=%s applied", body.SliderMode, custom)
	writeJSON(w, 200, map[string]any{"ok": true})
}
