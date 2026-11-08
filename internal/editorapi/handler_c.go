package editorapi

import (
	"math"
	"net/http"
	"strings"
	"time"

	"2t5-deck-plus/internal/actions"
	"2t5-deck-plus/internal/appdeck"
	"2t5-deck-plus/internal/preset"
)

// Wave C endpoints: GET /api/live "why", POST /api/curve, GET /api/defaults/diff.

func (s *server) routesC(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/curve", s.curve)
	mux.HandleFunc("GET /api/defaults/diff", s.defaultsDiff)
	mux.HandleFunc("GET /api/switches", s.getSwitches)
	mux.HandleFunc("POST /api/canary/ack", s.ackCanary)
}

// Reasons of Why.Reason.
const (
	WhyFrontmost      = "frontmost"
	WhyEditorFocused  = "editor-focused-kept-last"
	WhyGlobalFallback = "no-preset-global-fallback"
	WhyResolveCascade = "resolve-cascade"
)

type whyJSON struct {
	ActiveID  string `json:"activeId"`
	Reason    string `json:"reason"`
	Frontmost AppRef `json:"frontmost"`
	Since     int64  `json:"since"`
	// SwitchLog is the last 8 active-app changes, newest first, and
	// IgnoredBlips the focus blips under 400 ms that changed nothing (wave F).
	SwitchLog    []SwitchEvent `json:"switchLog"`
	IgnoredBlips int           `json:"ignoredBlips"`
}

// why explains the active preset. active is the preset id in effect.
// switches reads the switch log (never nil Log).
func (s *server) switches() SwitchSnapshot {
	if s.d.Switches == nil {
		return SwitchSnapshot{Log: []SwitchEvent{}}
	}
	sn := s.d.Switches.Snapshot(time.Now())
	if sn.Log == nil {
		sn.Log = []SwitchEvent{}
	}
	return sn
}

// GET /api/switches: the last 8 active-app changes (newest first) and the
// count of focus blips shorter than 400 ms that were ignored.
func (s *server) getSwitches(w http.ResponseWriter, r *http.Request) {
	sn := s.switches()
	writeJSON(w, 200, map[string]any{"switches": sn.Log, "ignoredBlips": sn.IgnoredBlips,
		"blipMs": int(BlipWindow / time.Millisecond), "keep": SwitchKeep})
}

func (s *server) why(front AppRef, active string) whyJSON {
	info := FrontInfo{}
	if s.d.Why != nil {
		info = s.d.Why.FrontInfo()
	}
	reason := WhyReason(front.ID, active, info.EditorFocused)
	sw := s.switches()
	return whyJSON{ActiveID: active, Reason: reason, Frontmost: front, Since: info.Since,
		SwitchLog: sw.Log, IgnoredBlips: sw.IgnoredBlips}
}

type curveReq struct {
	Action         string         `json:"action"`
	Fields         map[string]any `json:"fields"`
	MaxTicksPerSec any            `json:"maxTicksPerSec"`
}

const (
	defaultCurveMax = 120.0
	maxCurveMax     = 1000.0
)

// curve serves the real sensitivity curve: the points come from
// appdeck.Curve, which samples the same profile, threshold and unit functions
// the executor runs.
func (s *server) curve(w http.ResponseWriter, r *http.Request) {
	var req curveReq
	if !decode(w, r, &req) {
		return
	}
	reg := actions.Default()
	a, ok := reg.Get(req.Action)
	if !ok {
		if a2, ok2 := reg.ForType(req.Action); ok2 && req.Action != "" {
			a, ok = a2, true
		}
	}
	if !ok {
		writeErr(w, http.StatusBadRequest, "action", "unknown action "+quote(req.Action))
		return
	}
	switch a.Type {
	case "scrollDelta", "keyDelta", "jog", "jogSeconds":
	default:
		writeErr(w, http.StatusBadRequest, "action", a.Label+" has no speed curve (only turn actions that scroll, send keys or move the playhead do)")
		return
	}
	max := defaultCurveMax
	if req.MaxTicksPerSec != nil {
		f, ok := actions.ToFloat(req.MaxTicksPerSec)
		if !ok || math.IsNaN(f) || f < 1 || f > maxCurveMax {
			writeErr(w, http.StatusBadRequest, "maxTicksPerSec", "maxTicksPerSec must be a number from 1 to 1000")
			return
		}
		max = f
	}
	raw := map[string]any{"action": a.ID, "type": a.Type}
	for k, v := range req.Fields {
		if v != nil && k != "action" && k != "type" {
			raw[k] = v
		}
	}
	norm, err := reg.Normalize(raw)
	if err != nil {
		fail(w, err)
		return
	}
	if err := reg.Validate(norm, false); err != nil {
		fail(w, err)
		return
	}
	info, err := appdeck.Curve(a.Type, norm, max)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "action", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{
		"action": a.ID, "unit": info.Unit, "profile": info.Profile, "momentumMs": info.MomentumMs,
		"staged": info.Staged, "approx": info.Approx, "maxTicksPerSec": max, "points": info.Points,
	})
}

func quote(s string) string { return "\"" + strings.ReplaceAll(s, "\"", "'") + "\"" }

// defaultsDiff serves GET /api/defaults/diff?app=<id> (read-only).
func (s *server) defaultsDiff(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("app")
	if id == "" {
		writeErr(w, http.StatusBadRequest, "app", "app is required")
		return
	}
	if id == "global" {
		id = preset.GlobalID
	}
	d, err := s.d.Store.DefaultsDiff(id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, d)
}

// POST /api/canary/ack dismisses the silent-loss report ("this was
// intentional"). The presets are not touched; restoring is a separate,
// undoable POST /api/backups/{name}/restore.
func (s *server) ackCanary(w http.ResponseWriter, r *http.Request) {
	had, err := s.d.Store.AckCanary()
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "dismissed": had})
}
