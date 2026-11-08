package editorapi

import (
	"errors"
	"net/http"
	"sort"
	"time"

	"2t5-deck-plus/internal/mousecal"
)

// Mouse calibration (GET/POST /api/calibration...). The engine lives in
// internal/mousecal; this file is only the HTTP face. Nothing here posts input:
// the one route that can move or click (POST .../test) goes through the
// service, which refuses unless the process is armed and the switch is on.

func (s *server) routesCal(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/calibration", s.calApps)
	mux.HandleFunc("GET /api/calibration/{app}", s.calOverview)
	mux.HandleFunc("GET /api/calibration/{app}/session", s.calSession)
	mux.HandleFunc("POST /api/calibration/{app}/capture", s.calStart)
	mux.HandleFunc("GET /api/calibration/{app}/capture/{id}", s.calGet)
	mux.HandleFunc("GET /api/calibration/{app}/capture/{id}/preview.png", s.calPreview)
	mux.HandleFunc("POST /api/calibration/{app}/capture/{id}/confirm", s.calConfirm)
	mux.HandleFunc("POST /api/calibration/{app}/capture/{id}/discard", s.calDiscard)
	mux.HandleFunc("POST /api/calibration/{app}/test", s.calTest)
	mux.HandleFunc("DELETE /api/calibration/{app}/target/{key}", s.calClear)
	mux.HandleFunc("PUT /api/calibration/{app}/options", s.calOptions)
}

func (s *server) cal(w http.ResponseWriter) *mousecal.Service {
	if s.d.Calib == nil {
		writeErr(w, http.StatusNotFound, "", "mouse calibration is not available in this host")
		return nil
	}
	return s.d.Calib
}

func calFail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, mousecal.ErrNoTarget):
		writeErr(w, http.StatusNotFound, "target", err.Error())
	case errors.Is(err, mousecal.ErrNoCapture):
		writeErr(w, http.StatusNotFound, "id", err.Error())
	case errors.Is(err, mousecal.ErrWrongState):
		writeErr(w, http.StatusConflict, "id", err.Error())
	default:
		fail(w, err)
	}
}

// calApps lists the apps that have mouse targets, with their counts.
func (s *server) calApps(w http.ResponseWriter, r *http.Request) {
	c := s.cal(w)
	if c == nil {
		return
	}
	type row struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Targets    int    `json:"targets"`
		Calibrated int    `json:"calibrated"`
		Stale      int    `json:"stale"`
		Needs      int    `json:"needs"`
		Legacy     int    `json:"legacy"`
		Imported   int    `json:"imported"`
		Unknown    int    `json:"unknown"`
	}
	rows := []row{}
	for _, p := range s.d.Store.Snapshot().Presets() {
		if len(mousecal.DeriveTargets(p)) == 0 {
			continue
		}
		ov, err := c.Overview(p.ID)
		if err != nil {
			continue
		}
		rows = append(rows, row{p.ID, ov.App.Name, ov.Summary.Total, ov.Summary.Calibrated, ov.Summary.Stale, ov.Summary.Needs, ov.Summary.Legacy, ov.Summary.Imported, ov.Summary.Unknown})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	writeJSON(w, 200, map[string]any{"apps": rows})
}

func (s *server) calOverview(w http.ResponseWriter, r *http.Request) {
	c := s.cal(w)
	if c == nil {
		return
	}
	ov, err := c.Overview(r.PathValue("app"))
	if err != nil {
		writeErr(w, http.StatusNotFound, "app", err.Error())
		return
	}
	writeJSON(w, 200, ov)
}

func (s *server) calSession(w http.ResponseWriter, r *http.Request) {
	c := s.cal(w)
	if c == nil {
		return
	}
	scope := r.URL.Query().Get("scope")
	if scope != "layout" {
		scope = "missing"
	}
	steps, prob, err := c.Session(r.PathValue("app"), scope)
	if err != nil {
		writeErr(w, http.StatusNotFound, "app", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"scope": scope, "steps": steps, "problem": prob})
}

func (s *server) calStart(w http.ResponseWriter, r *http.Request) {
	c := s.cal(w)
	if c == nil {
		return
	}
	var b struct {
		Target  string `json:"target"`
		Via     string `json:"via"`
		DelayMs int    `json:"delayMs"`
	}
	if !decode(w, r, &b) {
		return
	}
	v, err := c.StartCapture(r.PathValue("app"), b.Target, b.Via, time.Duration(b.DelayMs)*time.Millisecond)
	if err != nil {
		calFail(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) calGet(w http.ResponseWriter, r *http.Request) {
	c := s.cal(w)
	if c == nil {
		return
	}
	v, err := c.Capture(r.PathValue("app"), r.PathValue("id"))
	if err != nil {
		calFail(w, err)
		return
	}
	writeJSON(w, 200, v)
}

func (s *server) calPreview(w http.ResponseWriter, r *http.Request) {
	c := s.cal(w)
	if c == nil {
		return
	}
	b, err := c.PreviewPNG(r.PathValue("app"), r.PathValue("id"))
	if err != nil {
		if errors.Is(err, mousecal.ErrNoCapture) {
			calFail(w, err)
			return
		}
		writeErr(w, http.StatusNotFound, "", err.Error())
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *server) calConfirm(w http.ResponseWriter, r *http.Request) {
	c := s.cal(w)
	if c == nil {
		return
	}
	v, err := c.Confirm(r.PathValue("app"), r.PathValue("id"))
	if err != nil {
		calFail(w, err)
		return
	}
	out := map[string]any{"ok": true, "capture": v}
	s.stamp(w, out)
	writeJSON(w, 200, out)
}

func (s *server) calDiscard(w http.ResponseWriter, r *http.Request) {
	c := s.cal(w)
	if c == nil {
		return
	}
	v, err := c.Discard(r.PathValue("app"), r.PathValue("id"))
	if err != nil {
		calFail(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "capture": v})
}

func (s *server) calTest(w http.ResponseWriter, r *http.Request) {
	c := s.cal(w)
	if c == nil {
		return
	}
	var b struct {
		Target  string `json:"target"`
		Mode    string `json:"mode"`
		Confirm bool   `json:"confirm"`
	}
	if !decode(w, r, &b) {
		return
	}
	res, err := c.Test(r.PathValue("app"), b.Target, b.Mode, b.Confirm)
	if err != nil {
		calFail(w, err)
		return
	}
	writeJSON(w, 200, res)
}

func (s *server) calClear(w http.ResponseWriter, r *http.Request) {
	c := s.cal(w)
	if c == nil {
		return
	}
	if err := c.Clear(r.PathValue("app"), r.PathValue("key"), r.URL.Query().Get("entry")); err != nil {
		calFail(w, err)
		return
	}
	s.okReply(w)
}

func (s *server) calOptions(w http.ResponseWriter, r *http.Request) {
	c := s.cal(w)
	if c == nil {
		return
	}
	var b struct {
		Verify      *string `json:"verify"`
		BlockLegacy *bool   `json:"blockLegacy"`
	}
	if !decode(w, r, &b) {
		return
	}
	if err := c.SetOptions(r.PathValue("app"), b.Verify, b.BlockLegacy); err != nil {
		calFail(w, err)
		return
	}
	s.okReply(w)
}
